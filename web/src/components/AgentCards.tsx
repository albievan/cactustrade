import type { AgentCard } from "../lib/api";
import { StateSince } from "./StateSince";

const stateBadge: Record<AgentCard["state"], string> = {
  running: "text-bg-success", paused: "text-bg-warning", stopped: "text-bg-secondary", killed: "text-bg-danger",
};

export type AgentAction = "start" | "pause" | "stop" | "forceClose";

// AgentCardView is one account's agent on the Risk limits page (R23). The first column is a
// compact vertical group of icon buttons; which of them may be used comes from the server
// (card.controls, the same rules as the dashboard's panel): this only shows it. The current
// account (the one the limits form edits) is highlighted; its name switches to it.
export function AgentCardView({ card, busy, onAction, onSwitch }: {
  card: AgentCard;
  busy: boolean;
  onAction: (action: AgentAction) => void;
  onSwitch: () => void;
}) {
  const c = card.controls;
  const name = card.account.name;
  const killed = card.killed || card.state === "killed";
  const off = busy || c.gated;
  return (
    <div className="col">
      <div className={"card h-100 agent-card" + (card.current ? " border-primary border-2 shadow-sm" : "")}
        aria-current={card.current ? "true" : undefined} aria-label={`Agent on ${name}`} role="group">
        <div className="card-body d-flex align-items-start gap-3">
          <div className="btn-group-vertical btn-group-sm" role="group" aria-label={`Agent controls for ${name}`}>
            <button type="button" className="btn btn-outline-success" aria-label={`Start the agent on ${name}`}
              title={c.startBlockedReason ? "Cannot start: " + c.startBlockedReason : "Start the agent"}
              disabled={off || !c.start || killed || card.state === "running"} onClick={() => onAction("start")}>
              <i className="bi bi-play-fill" />
            </button>
            <button type="button" className="btn btn-outline-warning" aria-label={`Pause the agent on ${name}`}
              title="Pause: no new entries, exits continue" disabled={off || !c.pause || card.state !== "running"} onClick={() => onAction("pause")}>
              <i className="bi bi-pause-fill" />
            </button>
            <button type="button" className="btn btn-outline-secondary" aria-label={`Stop the agent on ${name}`}
              title="Stop: no new entries, exits continue, limits may change" disabled={off || !c.stop || killed || card.state === "stopped"}
              onClick={() => onAction("stop")}>
              <i className="bi bi-stop-fill" />
            </button>
            {c.forceClose && (
              <button type="button" className="btn btn-outline-danger" aria-label={`Force close ${name}`}
                title="Force close: close everything cactusTrade opened now and stop the agent (admin, password; no kill switch)"
                disabled={busy} onClick={() => onAction("forceClose")}>
                <i className="bi bi-door-closed" />
              </button>
            )}
          </div>
          <div className="flex-grow-1 min-w-0">
            <div className="d-flex align-items-center gap-2 mb-1">
              {card.current
                ? <span className="fw-semibold text-truncate">{name}</span>
                : (
                  <button type="button" className="btn btn-link p-0 fw-semibold text-start text-truncate" title={`Work on ${name}: the limits below are then ${name}'s`}
                    onClick={onSwitch} disabled={busy}>
                    {name}
                  </button>
                )}
              {card.current && <span className="badge text-bg-primary">current</span>}
            </div>
            <div className="d-flex flex-wrap gap-1 small">
              <span className={"badge " + (card.account.mode === "real" ? "text-bg-danger" : "text-bg-info")}>
                {card.account.mode === "real" ? "REAL" : "demo"}
              </span>
              <span className={"badge text-capitalize " + stateBadge[card.state]}>{card.state}</span>
              <span className={"badge " + (card.reconciliation.ok ? "text-bg-success" : "text-bg-warning")}>
                {card.reconciliation.ok ? "reconciled" : "not reconciled"}
              </span>
              <span className={"badge " + (card.limitsEditable ? "text-bg-light border" : "text-bg-secondary")} title={card.limitsBlockedReason ?? "The limits can be changed now"}>
                <i className={"bi me-1 " + (card.limitsEditable ? "bi-unlock" : "bi-lock-fill")} />
                {card.limitsEditable ? "limits editable" : "limits locked"}
              </span>
              <StateSince state={card.state} since={card.stateSince} />
            </div>
            <div className="small text-secondary mt-1">Open positions / orders: {card.openPositions} / {card.pendingOrders}</div>
            {c.startBlockedReason && <div className="small text-warning-emphasis mt-1">Cannot start: {c.startBlockedReason}.</div>}
            {c.forceCloseBlockedReason && <div className="small text-secondary mt-1"><i className="bi bi-lock-fill me-1" />No force close: {c.forceCloseBlockedReason}.</div>}
          </div>
        </div>
      </div>
    </div>
  );
}
