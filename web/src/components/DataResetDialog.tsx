import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, type DataResetPreview, type DataResetResult } from "../lib/api";
import { ConfirmDialog } from "./ConfirmDialog";
import { ProblemAlert } from "./ProblemAlert";

// Where each failing check of the reset is fixed (R49.5). The server decides every check;
// the page only explains and links.
const fixes: Record<string, { to: string; label: string }> = {
  reset_agents_stopped: { to: "/", label: "Dashboard" },
  reset_no_open_position: { to: "/risk", label: "Force close all" },
  reset_no_open_order: { to: "/", label: "Dashboard" },
  reset_no_armed_gate: { to: "/real-trading", label: "Real trading" },
  reset_backup_ready: { to: "/backups", label: "Backups" },
};

const labels: Record<string, string> = {
  agent_positions: "positions cactusTrade opened",
  decisions: "decisions",
  equity_snapshots: "equity history",
  events: "events",
  llm_calls: "LLM calls",
  orders: "orders",
  reviews: "LLM reviews",
  trades: "trade history",
};

// DataResetDialog asks before all trading data of every account is reset (R49): the server's
// checks, what is wiped and kept with the rows now, a warning per real-money account (its
// demo evidence falls to 0), the backup taken first, the watchlist choice (off), the typed
// phrase and the admin's password. Nothing is decided here: the server checks everything again.
export function DataResetDialog({ onCancel, onDone }: {
  onCancel: () => void;
  onDone: (r: DataResetResult) => void | Promise<void>;
}) {
  const [pv, setPv] = useState<DataResetPreview | null>(null);
  const [phrase, setPhrase] = useState("");
  const [password, setPassword] = useState("");
  const [watchlists, setWatchlists] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { api.dataResetPreview().then(setPv).catch(setError); }, []);
  async function confirm() {
    setError(null);
    try {
      await onDone(await api.resetAllData(phrase, password, watchlists));
    } catch (e) {
      setError(e);
      setPassword("");
      api.dataResetPreview().then(setPv).catch(() => undefined);
    }
  }
  const failing = pv?.checks.filter((c) => !c.pass) ?? [];
  const typed = pv !== null && phrase.trim() === pv.phrase;
  return (
    <ConfirmDialog title="Reset all trading data?" confirmLabel="Reset all trading data" busyLabel="Backing up and resetting…"
      icon="trash" confirmDisabled={!typed || !password} onCancel={onCancel} onConfirm={confirm}>
      {pv === null && !error && <p className="text-secondary">Reading what a reset would do…</p>}
      {pv !== null && <>
        {failing.length > 0 ? (
          <div className="alert alert-warning py-2 small" data-testid="reset-refusals">
            <div className="fw-semibold mb-1">The reset is refused until:</div>
            <ul className="mb-0">
              {failing.map((c) => (
                <li key={c.name}>{c.detail}{fixes[c.name] && <> (<Link to={fixes[c.name].to}>{fixes[c.name].label}</Link>)</>}</li>
              ))}
            </ul>
          </div>
        ) : <p className="small text-success" data-testid="reset-checks-ok"><i className="bi bi-check-circle me-1" />Every check passes now; the server checks again.</p>}
        {pv.realAccounts.map((r) => (
          <div key={r.accountId} className="alert alert-danger py-2 small" role="alert" data-testid="reset-real-warning">
            <strong>{r.name}: {r.daysNow} qualifying demo day{r.daysNow === 1 ? "" : "s"} now → 0.</strong> Real trading cannot be
            armed again until {r.required} new qualifying demo day{r.required === 1 ? "" : "s"}{r.demoName ? ` on ${r.demoName}` : ""}.
          </div>
        ))}
        <p className="small mb-1">Wiped for <strong>every account</strong>:</p>
        <ul className="small" aria-label="What the reset wipes">
          {Object.entries(pv.wiped).map(([t, n]) => <li key={t}>{labels[t] ?? t}: {n}</li>)}
          {watchlists && <li>watchlists: {pv.kept.watchlist ?? 0}</li>}
        </ul>
        <p className="small">Kept: users, sessions, API keys, secrets, accounts and their sharing, settings, risk limits, kill switches
          and high-water marks, full autonomy&apos;s lowered aggressiveness after a loss-limit day, instrument blocks, instruments, themes, statement schedules, the mail outbox, real-trading gate
          records (an armed one is locked) and the audit log{watchlists ? "" : ", and the watchlists"}. History starts from the reset.</p>
        <p className="small" data-testid="reset-backup">{pv.backupReady
          ? <>An encrypted backup is taken first{pv.backupFolder ? <> into <code>{pv.backupFolder}</code></> : ""}; without it nothing is wiped.</>
          : <span className="text-danger">No backup can be taken: {pv.backupWhy}</span>}</p>
        <div className="form-check mb-2">
          <input id="reset-watchlists" type="checkbox" className="form-check-input" checked={watchlists} onChange={(e) => setWatchlists(e.target.checked)} />
          <label className="form-check-label" htmlFor="reset-watchlists">Also empty every watchlist</label>
        </div>
        <label className="form-label" htmlFor="reset-phrase">Type <code>{pv.phrase}</code></label>
        <input id="reset-phrase" className="form-control mb-2" autoComplete="off" maxLength={100} value={phrase} onChange={(e) => setPhrase(e.target.value)} />
      </>}
      <ProblemAlert error={error} className="py-2 small" />
      <label className="form-label" htmlFor="reset-password">Your password</label>
      <input id="reset-password" type="password" className="form-control" autoComplete="current-password" maxLength={256}
        value={password} onChange={(e) => setPassword(e.target.value)} />
    </ConfirmDialog>
  );
}
