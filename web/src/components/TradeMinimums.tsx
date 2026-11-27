import { useEffect, useState } from "react";
import { api, usd, type MinimumsReview, type TradeMinimumsView } from "../lib/api";
import { dateTime } from "../lib/format";
import { ProblemAlert } from "./ProblemAlert";

// The platform's trade minimums (R38): eToro's smallest long and short, what the limits allow
// now, and the changes proposed so that a trade of that size fits. The server works it all out
// and applies every rule; nothing changes until the owner presses Approve.

const statusText: Record<MinimumsReview["status"], [string, string]> = {
  fits: ["text-bg-success", "the limits fit"],
  pending: ["text-bg-warning", "waiting for your approval"],
  approved: ["text-bg-success", "approved"],
  declined: ["text-bg-secondary", "declined"],
  blocked: ["text-bg-danger", "cannot fit"],
};

export function TradeMinimums({ reloadKey, onApproved }: { reloadKey: string; onApproved: () => void }) {
  const [v, setV] = useState<TradeMinimumsView | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState("");
  useEffect(() => {
    api.tradeMinimums().then(setV).catch(setError);
  }, [reloadKey]);
  async function run(what: string, call: () => Promise<TradeMinimumsView>) {
    setBusy(what); setError(null);
    try {
      const next = await call();
      setV(next);
      if (what === "approve") onApproved();
    } catch (e) {
      setError(e);
    } finally {
      setBusy("");
    }
  }
  const r = v?.review;
  return (
    <div className="card mb-4"><div className="card-body">
      <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
        <h2 className="h5 mb-0 flex-grow-1">Trade minimums</h2>
        {r && <span className={"badge " + statusText[r.status][0]}>{statusText[r.status][1]}</span>}
        <button type="button" className="btn btn-sm btn-outline-secondary" disabled={!!busy}
          onClick={() => void run("check", api.checkTradeMinimums)}>
          <i className="bi bi-arrow-repeat me-1" />{r ? "Check again" : "Check now"}
        </button>
      </div>
      <p className="small text-secondary">
        The smallest trade eToro accepts, from its answers for the shares on the watchlist. A check runs when an account gets
        new eToro keys. It only proposes: the limits change when you approve, with the agent stopped, and cactusTrade never
        moves money into or out of the account.
      </p>
      <ProblemAlert error={error} />
      {!r && <p className="text-secondary mb-0">Not checked yet.</p>}
      {r && (
        <>
          <dl className="row small mb-2">
            <dt className="col-sm-4">Smallest long (real share)</dt>
            <dd className="col-sm-8">{r.longKnown > 0 ? <>{usd(r.longCents)}{r.longLowestCents !== r.longCents && <> (from {usd(r.longLowestCents)})</>}</> : "unknown"} · your limits allow up to {usd(r.longRoomCents)}</dd>
            <dt className="col-sm-4">Smallest short (CFD)</dt>
            <dd className="col-sm-8">{r.shortKnown > 0 ? <>{usd(r.shortCents)}{r.shortLowestCents !== r.shortCents && <> (from {usd(r.shortLowestCents)})</>}</> : "unknown"} · your limits allow up to {usd(r.shortRoomCents)}{r.current.maxShortBp === 0 && " (shorts are off)"}</dd>
            <dt className="col-sm-4">Checked</dt>
            <dd className="col-sm-8">{dateTime(r.at)} · {r.instruments} share(s) · equity {usd(r.equityCents)}</dd>
          </dl>
          {r.changes.length > 0 && (
            <>
              <p className="small fw-semibold mb-1">{r.status === "pending" ? "Proposed changes" : "Changes"}</p>
              <ul className="small" aria-label="Proposed changes">{r.changes.map((c) => <li key={c}>{c}</li>)}</ul>
            </>
          )}
          {r.problems.length > 0 && (
            <ul className="small text-danger" aria-label="Problems">{r.problems.map((c) => <li key={c}>{c}</li>)}</ul>
          )}
          {r.status === "pending" && (
            <div className="d-flex flex-wrap align-items-center gap-2">
              <button type="button" className="btn btn-sm btn-success" disabled={!!busy || !v?.canDecide}
                title={v?.decideHint ? "Cannot approve: " + v.decideHint : "Change the limits as proposed (audited)"}
                onClick={() => void run("approve", () => api.decideTradeMinimums(r.id, true))}>
                <i className="bi bi-check-lg me-1" />Approve the new limits
              </button>
              <button type="button" className="btn btn-sm btn-outline-secondary" disabled={!!busy}
                onClick={() => void run("decline", () => api.decideTradeMinimums(r.id, false))}>Decline</button>
              {v?.decideHint && <span className="small text-secondary">{v.decideHint}.</span>}
            </div>
          )}
          {r.decidedBy && <p className="small text-secondary mb-0">{statusText[r.status][1]} by {r.decidedBy}{r.decidedAt && <> on {dateTime(r.decidedAt)}</>}.</p>}
        </>
      )}
    </div></div>
  );
}
