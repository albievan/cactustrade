import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { api, errorText, usd, type AgentPanel as Panel, type Missing } from "../lib/api";
import type { Live } from "../lib/live";
import { useRefreshMode } from "../lib/mode";
import { ConfirmDialog } from "./ConfirmDialog";
import { StateSince } from "./StateSince";
import { FixButton, fixes } from "./FixButton";
import { ForceCloseDialog, forceCloseNote } from "./ForceCloseDialog";
import { ProblemAlert } from "./ProblemAlert";

const stateBadge: Record<Panel["state"], string> = {
  running: "text-bg-success", paused: "text-bg-warning", stopped: "text-bg-secondary", killed: "text-bg-danger",
};

const marketName: Record<string, string> = { LSE: "London", NYSE: "New York", ASX: "Sydney" };

// AgentPanel shows the current account's agent and its controls. Which control is enabled is
// decided by the server (panel.controls: role, access, web session or API key, real account);
// this component only shows it. The kill switch and its reset ask first, in a dialog.
//
// With live (the dashboard's stream, PLAN 6.11) the panel refetches when the stream reports a
// change of the agent (state, kill, daily latch, reconciliation, lease) and when it asks for a
// refresh (a resync, or every 15 s while the stream is down); without it the panel polls every
// 15 s. The controls never depend on the stream: they are the panel's own REST calls.
export function AgentPanel({ live }: { live?: Pick<Live, "snapshot" | "refresh"> }) {
  const refreshMode = useRefreshMode();
  const [p, setP] = useState<Panel | null>(null);
  const [error, setError] = useState<unknown>(null); // the last action's refusal (its problem may name what is missing)
  const [loadError, setLoadError] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState("");
  const [dialog, setDialog] = useState<"" | "kill" | "reset" | "force">("");

  const load = useCallback(async () => {
    try {
      setP(await api.agent());
      setLoadError("");
    } catch (e) {
      setLoadError(errorText(e));
    }
  }, []);
  const streamed = live !== undefined;
  useEffect(() => {
    void load();
    if (streamed) return;
    // Each refresh reads the account's equity (through the 10 s view cache), so a hidden tab
    // does not poll.
    const t = window.setInterval(() => { if (!document.hidden) void load(); }, 15_000);
    return () => window.clearInterval(t);
  }, [load, streamed]);
  // What the stream says about the agent: a change refetches the panel.
  const s = live?.snapshot;
  const agentKey = s ? [s.state, s.killed, s.killReason ?? "", s.dayStopped, s.reconciliation.ok, s.reconciliation.at ?? "", s.lease.other].join("|") : "";
  const refresh = live?.refresh ?? 0;
  const first = useRef(true);
  useEffect(() => {
    if (first.current) { first.current = false; return; } // the mount already loaded
    if (streamed) void load();
  }, [agentKey, refresh, streamed, load]);

  async function act(name: string, f: () => Promise<unknown>, done: string) {
    setBusy(name); setError(null); setNote("");
    try {
      await f();
      setNote(done);
    } catch (e) {
      setError(e);
    } finally {
      setDialog("");
      setBusy("");
      await load();
      await refreshMode();
    }
  }

  if (!p) {
    return loadError ? <div className="alert alert-warning">{loadError}</div> : null;
  }
  const c = p.controls;
  const gated = c.gated;
  const killed = p.state === "killed" || p.killed;
  const off = !!busy || gated;
  const missing = p.missing ?? []; // a server older than R22 sends none
  // The fixes this panel runs itself; every other one opens its page.
  const actions: Partial<Record<Missing, () => void>> = {
    reconcile: () => void act("reconcile", api.reconcileAgent, "Reconciliation ran."),
    kill_reset: () => setDialog("reset"),
  };
  return (
    <div className="card mb-4 agent-panel">
      <div className="card-body">
        <div className="d-flex align-items-start gap-3">
          <div className="d-flex flex-column gap-2 align-self-start">
            <div className="btn-group-vertical btn-group-sm" role="group" aria-label="Agent controls">
              <button className="btn btn-outline-success" title={c.startBlockedReason ? "Cannot start: " + c.startBlockedReason : "Start the agent"} aria-label="Start the agent"
                disabled={off || !c.start || killed || p.state === "running"}
                onClick={() => void act("start", api.startAgent, "The agent is running.")}>
                <i className="bi bi-play-fill" />
              </button>
              <button className="btn btn-outline-warning" title="Pause: no new entries, exits continue" aria-label="Pause the agent"
                disabled={off || !c.pause || p.state !== "running"}
                onClick={() => void act("pause", api.pauseAgent, "The agent is paused: no new entries; exits continue.")}>
                <i className="bi bi-pause-fill" />
              </button>
              <button className="btn btn-outline-secondary" title="Stop: no new entries, exits continue, limits may change" aria-label="Stop the agent"
                disabled={off || !c.stop || killed || p.state === "stopped"}
                onClick={() => void act("stop", api.stopAgent, "The agent is stopped: no new entries; exits continue.")}>
                <i className="bi bi-stop-fill" />
              </button>
              <button className="btn btn-outline-secondary" title="Reconcile with eToro now (read-only)" aria-label="Reconcile with eToro"
                disabled={off || !c.pause}
                onClick={() => void act("reconcile", api.reconcileAgent, "Reconciliation ran.")}>
                <i className="bi bi-arrow-repeat" />
              </button>
            </div>
            <button className="btn btn-danger btn-sm" title="Kill switch: stop entries and close cactusTrade's positions" aria-label="Kill switch"
              disabled={busy === "kill" || gated || !c.kill} onClick={() => setDialog("kill")}>
              <i className="bi bi-x-octagon-fill" />
            </button>
            {killed && c.reset && (
              <button className="btn btn-outline-danger btn-sm" title="Reset the kill switch (admin)" aria-label="Reset the kill switch"
                disabled={off} onClick={() => setDialog("reset")}>
                <i className="bi bi-arrow-counterclockwise" />
              </button>
            )}
            {c.forceClose && (
              <button className="btn btn-outline-danger btn-sm" aria-label="Force close"
                title="Force close: close everything cactusTrade opened now and stop the agent (admin, password; no kill switch)"
                disabled={!!busy} onClick={() => setDialog("force")}>
                <i className="bi bi-door-closed" />
              </button>
            )}
          </div>
          <div className="flex-grow-1">
            <div className="d-flex align-items-center gap-2 mb-2">
              <h2 className="h5 mb-0">Agent</h2>
              <span className={"badge text-capitalize " + stateBadge[p.state]}>{p.state}</span>
              <span className={"badge " + (p.reconciliation.ok ? "text-bg-success" : "text-bg-warning")}>
                {p.reconciliation.ok ? "reconciled" : "not reconciled"}
              </span>
              {p.dayStopped && <span className="badge text-bg-warning">daily limit reached</span>}
              <StateSince state={p.state} since={p.stateSince} />
            </div>
            {gated && (
              <div className="alert alert-secondary py-2 small mb-2">
                <i className="bi bi-lock-fill me-1" />Real trading needs the phase-7 gate: the agent cannot be controlled on a real-money account.
                {c.forceCloseBlockedReason && <div className="mt-1">No force close here: {c.forceCloseBlockedReason}.</div>}
              </div>
            )}
            {killed && (
              <div className="alert alert-danger py-2 small mb-2" role="alert">
                <strong>Kill switch on.</strong> {p.killReason || "No reason recorded."}
                {p.killedAt && <> ({new Date(p.killedAt).toLocaleString()})</>} An admin must reset it before the agent can start.
              </div>
            )}
            {c.startBlockedReason && (
              <div className="alert alert-warning py-2 small mb-2" role="status">
                <i className="bi bi-exclamation-triangle me-1" />The agent cannot be started: {c.startBlockedReason}. Exits and the kill switch still work.
              </div>
            )}
            {p.lease.other && (
              <div className="alert alert-warning py-2 small mb-2">
                Another cactusTrade process holds this account: this server sends no orders for it.
              </div>
            )}
            {missing.length > 0 && (
              <div className="small mb-2 d-flex flex-wrap align-items-center gap-2" aria-label="Before the agent can start">
                <span className="fw-semibold">Before the agent can start:</span>
                {missing.map((m) => (
                  <span key={m.what} className="d-inline-flex align-items-center gap-1 me-2">
                    <span className="text-secondary">{fixes[m.what]?.missing ?? m.what}</span>
                    {m.canFix && <FixButton what={m.what} accountId={p.account.id} onFix={actions[m.what]} disabled={!!busy} />}
                  </span>
                ))}
              </div>
            )}
            <ProblemAlert error={error} accountId={p.account.id} fixes={actions} className="py-2 small mb-2" />
            {loadError && <div className="alert alert-warning py-2 small mb-2">Not refreshed: {loadError}</div>}
            {note && <div className="alert alert-success py-2 small mb-2" role="status">{note}</div>}
            <div className="row g-2 small">
              <Fact label="Reconciled">
                {p.reconciliation.ok ? "yes" : "no"}
                {p.reconciliation.at && <> · {new Date(p.reconciliation.at).toLocaleTimeString()}</>}
                {!p.reconciliation.ok && p.reconciliation.reason && <div className="text-secondary">{p.reconciliation.reason}</div>}
              </Fact>
              <Fact label="Equity">{p.equityKnown ? usd(p.equityCents) : <span className="text-secondary">{p.equityError || "unknown"}</span>}</Fact>
              <Fact label="To the daily loss limit">
                {p.toDailyFloorCents !== undefined
                  ? <>{usd(p.toDailyFloorCents)} <span className="text-secondary">(floor {usd(p.dailyFloorCents)})</span></>
                  : <span className="text-secondary">not known yet today</span>}
              </Fact>
              <Fact label="To the drawdown kill switch">
                {p.toDrawdownFloorCents !== undefined
                  ? <>{usd(p.toDrawdownFloorCents)} <span className="text-secondary">(floor {usd(p.drawdownFloorCents)})</span></>
                  : <span className="text-secondary">unknown</span>}
              </Fact>
              <Fact label="Open positions / orders">{p.openPositions} / {p.pendingOrders}</Fact>
              <Fact label="Flattened before the close">
                {p.flatten.map((f) => (
                  <div key={f.market}>
                    {marketName[f.market] ?? f.market}:{" "}
                    {f.trading && f.flattenAt ? new Date(f.flattenAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : <span className="text-secondary">not trading</span>}
                  </div>
                ))}
              </Fact>
            </div>
            {p.blocked.length > 0 && (
              <div className="small mt-2">
                <span className="fw-semibold">Blocked:</span>{" "}
                {p.blocked.map((b) => <span key={b.instrumentId} className="badge text-bg-danger me-1" title={b.reason}>{b.symbol}</span>)}
              </div>
            )}
          </div>
        </div>
      </div>
      {dialog === "kill" && (
        <KillDialog busy={busy === "kill"} onCancel={() => setDialog("")}
          onConfirm={(reason) => act("kill", () => api.killAgent(reason), "Kill switch on: entries stopped; cactusTrade's positions are being closed.")} />
      )}
      {dialog === "reset" && (
        <ResetDialog phrase={p.resetPhrase} busy={busy === "reset"} onCancel={() => setDialog("")}
          onConfirm={(phrase) => act("reset", () => api.resetKill(phrase), "Kill switch reset: the agent is stopped.")} />
      )}
      {dialog === "force" && (
        <ForceCloseDialog accountId={p.account.id} onCancel={() => setDialog("")}
          onDone={async (reports) => {
            setDialog(""); setError(null); setNote("Force close: " + forceCloseNote(reports));
            await load();
            await refreshMode();
          }} />
      )}
    </div>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="col-sm-6 col-xl-4">
      <div className="text-secondary">{label}</div>
      <div>{children}</div>
    </div>
  );
}

function KillDialog({ busy, onCancel, onConfirm }: { busy: boolean; onCancel: () => void; onConfirm: (reason: string) => Promise<unknown> }) {
  const [reason, setReason] = useState("");
  return (
    <ConfirmDialog title="Pull the kill switch?" confirmLabel="Kill now" busyLabel="Killing…" icon="x-octagon-fill" busy={busy}
      onCancel={onCancel} onConfirm={() => onConfirm(reason)}>
      <p>New entries stop at once. cactusTrade cancels its pending orders and closes every position <strong>it</strong> opened;
        positions opened outside cactusTrade are left alone.</p>
      <p className="mb-3">Only an admin can reset the kill switch afterwards.</p>
      <label className="form-label" htmlFor="kill-reason">Reason (optional)</label>
      <input id="kill-reason" className="form-control" maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)} />
    </ConfirmDialog>
  );
}

function ResetDialog({ phrase, busy, onCancel, onConfirm }: { phrase: string; busy: boolean; onCancel: () => void; onConfirm: (phrase: string) => Promise<unknown> }) {
  const [typed, setTyped] = useState("");
  return (
    <ConfirmDialog title="Reset the kill switch?" confirmLabel="Reset" busyLabel="Resetting…" busy={busy}
      confirmDisabled={typed.trim() !== phrase} onCancel={onCancel} onConfirm={() => onConfirm(typed)}>
      <p>The high-water mark becomes the account's equity now and the agent comes back <strong>stopped</strong>.
        Nobody's positions are touched.</p>
      <label className="form-label" htmlFor="reset-phrase">Type <code>{phrase}</code> to confirm</label>
      <input id="reset-phrase" className="form-control" autoComplete="off" value={typed} onChange={(e) => setTyped(e.target.value)} />
    </ConfirmDialog>
  );
}
