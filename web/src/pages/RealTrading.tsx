import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorText, viewOnly, type Account, type RealGateStatus } from "../lib/api";
import { useRefreshMode } from "../lib/mode";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { ProblemAlert } from "../components/ProblemAlert";

// Real trading (PLAN 7.15): one card per real-money account with the server's checklist, the
// demo evidence, the demo account to link, the arm dialog (typed phrase + password) and
// Disarm. Every rule is the server's: the page shows its checks, its canArm / canDisarm, its
// `enabled` (whether this build may arm at all; true since the owner's consent, D39) and its
// refusals.
export function RealTrading() {
  const [accounts, setAccounts] = useState<Account[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    api.accounts()
      .then((as) => setAccounts(as.filter((a) => a.mode === "real" && !viewOnly(a.platform))))
      .catch(setError);
  }, []);
  return (
    <>
      <h1 className="h3">Real trading</h1>
      <div className="alert alert-warning" role="note">
        <strong>Real money.</strong> Real trading needs an armed gate per real-money account: an admin who owns the account,
        enough demo history on a linked demo account, the password and a typed phrase. Trading can lose money; past demo
        results do not predict future results. Anyone with trade access can disarm at any time; positions due are still
        closed after a disarm.
      </div>
      <ProblemAlert error={error} />
      {accounts === null && !error && <p className="text-secondary">Loading…</p>}
      {accounts !== null && accounts.length === 0 && (
        <p className="text-secondary">No real-money eToro accounts. Add one under Platform accounts (it is shown, but never trades without an armed gate).</p>
      )}
      <div className="row row-cols-1 row-cols-xl-2 g-3">
        {accounts?.map((a) => <div className="col" key={a.id}><GateCard account={a} /></div>)}
      </div>
    </>
  );
}

function GateCard({ account }: { account: Account }) {
  const refreshMode = useRefreshMode();
  const [demo, setDemo] = useState<string | undefined>(undefined);
  const [st, setSt] = useState<RealGateStatus | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [msg, setMsg] = useState("");
  const [arming, setArming] = useState(false);
  const [disarming, setDisarming] = useState(false);
  const load = useCallback(async () => {
    try {
      setSt(await api.realGate(account.id, demo));
      setError(null);
    } catch (e) {
      setError(e);
    }
  }, [account.id, demo]);
  useEffect(() => { void load(); }, [load]);
  async function done(text: string) {
    setMsg(text);
    await load();
    await refreshMode(); // the banner follows the server
  }
  return (
    <div className="card h-100">
      <div className="card-header d-flex align-items-center gap-2">
        <span className="fw-semibold flex-grow-1">{account.name}</span>
        {st && (st.armed
          ? <span className="badge text-bg-danger">ARMED</span>
          : <span className="badge text-bg-secondary">locked</span>)}
      </div>
      <div className="card-body">
        <ProblemAlert error={error} className="py-2 small" />
        {msg && <div className="alert alert-success py-2 small" role="status">{msg}</div>}
        {!st && !error && <p className="text-secondary">Loading…</p>}
        {st && (
          <>
            {!st.enabled && (
              <p className="small text-danger fw-semibold">Real trading is not enabled in this build: arming is refused until the owner's
                written consent. The checklist shows what the server would check.</p>
            )}
            {st.gate.state === "locked" && st.gate.disarmedAt && (
              <p className="small text-secondary">Last disarmed {new Date(st.gate.disarmedAt).toLocaleString()}: {st.gate.disarmReason}</p>
            )}
            {st.armed && st.gate.expiresAt && (
              <p className="small text-danger">Armed until {new Date(st.gate.expiresAt).toLocaleString()}.</p>
            )}
            <label className="form-label small" htmlFor={"demo-" + account.id}>Demo account whose history is the evidence</label>
            <select id={"demo-" + account.id} className="form-select form-select-sm mb-3" value={st.demoAccountId ?? ""}
              onChange={(e) => setDemo(e.target.value || undefined)}>
              <option value="">(choose)</option>
              {st.demoCandidates.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
            </select>
            <ul className="list-unstyled small mb-3" aria-label={"Checklist for " + account.name}>
              {st.checks.map((c) => (
                <li key={c.name} className={c.pass ? "text-success" : "text-danger"}>
                  <i className={"bi me-1 " + (c.pass ? "bi-check-circle-fill" : "bi-x-circle-fill")} aria-label={c.pass ? "pass" : "fail"} />
                  {c.detail}
                </li>
              ))}
              <li className="text-secondary"><i className="bi bi-keyboard me-1" />When arming: type <code>{st.phrase}</code></li>
            </ul>
            <div className="small text-secondary mb-3" aria-label="Evidence">
              Qualifying demo days: <strong>{st.evidence.count}</strong> of {st.rule.minDays} needed in the last {st.rule.windowDays} risk days
              {st.evidence.count > 0 && <> ({st.evidence.first} to {st.evidence.last})</>}; the latest at most {st.rule.maxAgeDays} days old.
              {st.evidence.days.length > 0 && <div className="text-break">{st.evidence.days.join(" · ")}</div>}
            </div>
            {st.canSetDays && (
              <DemoDaysEditor st={st} onSaved={(next) => { setSt(next); setMsg(`Arming now needs ${next.rule.minDays} qualifying demo days (audited).`); }} />
            )}
            <div className="d-flex gap-2">
              {st.canArm && (
                <button type="button" className="btn btn-outline-danger" onClick={() => { setMsg(""); setArming(true); }}>
                  <i className="bi bi-unlock me-1" />Arm real trading…
                </button>
              )}
              {st.canDisarm && (
                <button type="button" className="btn btn-danger" onClick={() => { setMsg(""); setDisarming(true); }}>
                  <i className="bi bi-lock-fill me-1" />Disarm
                </button>
              )}
            </div>
          </>
        )}
      </div>
      {arming && st && (
        <ArmDialog status={st} onCancel={() => setArming(false)}
          onDone={async () => { setArming(false); await done("Real trading armed on " + account.name + "."); }} />
      )}
      {disarming && (
        <DisarmDialog accountId={account.id} name={account.name} onCancel={() => setDisarming(false)}
          onDone={async () => { setDisarming(false); await done("Real trading is disarmed on " + account.name + "."); }} />
      )}
    </div>
  );
}

// ArmDialog asks for the phrase and the password. The server checks both and everything else;
// its refusal stays in the dialog.
function ArmDialog({ status, onCancel, onDone }: { status: RealGateStatus; onCancel: () => void; onDone: () => Promise<void> }) {
  const [confirm, setConfirm] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<unknown>(null);
  async function arm() {
    setError(null);
    try {
      await api.armRealGate(status.account.id, status.demoAccountId ?? "", confirm, password);
      await onDone();
    } catch (e) {
      setError(e);
      setPassword("");
    }
  }
  return (
    <ConfirmDialog title={"Arm real trading on " + status.account.name + "?"} confirmLabel="Arm real trading" busyLabel="Checking…"
      icon="unlock" confirmDisabled={!confirm || !password} onCancel={onCancel} onConfirm={arm}>
      <p>Once armed, the agent may trade this account with <strong>real money</strong>, within its limits, until someone disarms it or
        30 days pass. Losses are real. It trades only once its agent is started and only where the deployment allows real trading
        (<code>CACTUSTRADE_REAL_TRADING=on</code>).</p>
      <ProblemAlert error={error} className="py-2 small" />
      <label className="form-label" htmlFor="arm-confirm">Type <code>{status.phrase}</code></label>
      <input id="arm-confirm" className="form-control mb-2" autoComplete="off" maxLength={200} value={confirm} onChange={(e) => setConfirm(e.target.value)} />
      <label className="form-label" htmlFor="arm-password">Your password</label>
      <input id="arm-password" type="password" className="form-control" autoComplete="current-password" maxLength={256}
        value={password} onChange={(e) => setPassword(e.target.value)} />
    </ConfirmDialog>
  );
}

function DisarmDialog({ accountId, name, onCancel, onDone }: { accountId: string; name: string; onCancel: () => void; onDone: () => Promise<void> }) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  async function disarm() {
    setError("");
    try {
      await api.disarmRealGate(accountId, reason.trim() || undefined);
      await onDone();
    } catch (e) {
      setError(errorText(e));
    }
  }
  return (
    <ConfirmDialog title={"Disarm real trading on " + name + "?"} confirmLabel="Disarm" busyLabel="Disarming…" icon="lock-fill"
      onCancel={onCancel} onConfirm={disarm}>
      <p>New entries stop at once and the agent is stopped. Positions due are still closed; nothing is cancelled. Arming again needs
        the full checklist.</p>
      {error && <div className="alert alert-danger py-2 small" role="alert">{error}</div>}
      <label className="form-label" htmlFor="disarm-reason">Reason (optional)</label>
      <input id="disarm-reason" className="form-control" maxLength={200} value={reason} onChange={(e) => setReason(e.target.value)} />
    </ConfirmDialog>
  );
}

// DemoDaysEditor lets the account's admin set how many qualifying demo days arming needs
// (D36). The server checks who may and the range, and audits the change.
function DemoDaysEditor({ st, onSaved }: { st: RealGateStatus; onSaved: (v: RealGateStatus) => void }) {
  const [days, setDays] = useState(String(st.rule.minDays));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { setDays(String(st.rule.minDays)); }, [st.rule.minDays]);
  const n = Number(days);
  const valid = Number.isInteger(n) && n >= st.minDaysFloor && n <= st.rule.windowDays;
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      onSaved(await api.setRealGateDemoDays(st.account.id, n));
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }
  const id = "days-" + st.account.id;
  return (
    <form className="mb-3" onSubmit={save}>
      <label className="form-label small" htmlFor={id}>Qualifying demo days needed before arming</label>
      <div className="input-group input-group-sm" style={{ maxWidth: 260 }}>
        <input id={id} className="form-control" type="number" min={st.minDaysFloor} max={st.rule.windowDays} step={1}
          value={days} onChange={(e) => setDays(e.target.value)} />
        <button type="submit" className="btn btn-outline-secondary" disabled={busy || !valid || n === st.rule.minDays}>Save</button>
      </div>
      <div className="form-text">{st.minDaysFloor}–{st.rule.windowDays} days (default {st.defaultMinDays}). Changing it disarms an armed account and is audited.</div>
      <ProblemAlert error={error} className="py-2 small mt-2" />
    </form>
  );
}
