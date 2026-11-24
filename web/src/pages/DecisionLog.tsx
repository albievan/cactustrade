import { Fragment, useCallback, useEffect, useState } from "react";
import { api, errorText, usd, type Decision, type DecisionDetail } from "../lib/api";
import { useMode } from "../lib/mode";
import { NeedAccount } from "../components/NeedAccount";
import { InfoCard } from "../components/InfoCard";
import { ProblemAlert } from "../components/ProblemAlert";

// The outcomes a decision can end with (domain.DecisionOutcomes), for the filter.
export const outcomes: { value: string; label: string; badge: string }[] = [
  { value: "ordered", label: "ordered", badge: "text-bg-success" },
  { value: "order_failed", label: "order failed", badge: "text-bg-warning" },
  { value: "rejected_reviewer", label: "rejected by the reviewer", badge: "text-bg-secondary" },
  { value: "rejected_cost", label: "rejected: costs", badge: "text-bg-secondary" },
  { value: "rejected_risk", label: "rejected: risk", badge: "text-bg-secondary" },
  { value: "pending", label: "pending", badge: "text-bg-info" },
];

const verdictBadge: Record<string, string> = { approve: "text-bg-success", reduce: "text-bg-warning", reject: "text-bg-danger" };

const pageSize = 100;

function outcomeOf(v: string) {
  return outcomes.find((o) => o.value === v) ?? { value: v, label: v, badge: "text-bg-secondary" };
}

// cost formats a USD amount kept to the micro-dollar; absent = unknown (no prices, OD13).
export function cost(v: string | undefined): string {
  return v === undefined ? "unknown" : "$" + v;
}

// The decision log of the current account: one row per candidate the agent handled, newest
// first. A log, so a table; the first column expands a row to its details.
export function DecisionLog() {
  const status = useMode();
  const [rows, setRows] = useState<Decision[] | null>(null);
  const [outcome, setOutcome] = useState("");
  const [more, setMore] = useState(false);
  const [open, setOpen] = useState<Record<string, DecisionDetail | "loading" | string>>({});
  const [error, setError] = useState<unknown>(null);
  const accountId = status?.account?.id;
  const load = useCallback(() => {
    api.decisions({ limit: pageSize, outcome }).then((d) => { setRows(d); setMore(d.length === pageSize); setError(""); })
      .catch(setError);
  }, [outcome]);
  useEffect(() => {
    if (!accountId) return;
    load();
  }, [load, accountId]);
  if (status && !status.account) return <><h1 className="h3">Decision log</h1><NeedAccount /></>;
  const older = () => {
    if (!rows || rows.length === 0) return;
    api.decisions({ limit: pageSize, outcome, before: rows[rows.length - 1].at })
      .then((d) => { setRows([...rows, ...d]); setMore(d.length === pageSize); }).catch(setError);
  };
  const toggle = (d: Decision) => {
    if (open[d.id] !== undefined) {
      const next = { ...open };
      delete next[d.id];
      setOpen(next);
      return;
    }
    setOpen((o) => ({ ...o, [d.id]: "loading" }));
    api.decision(d.id).then((x) => setOpen((o) => ({ ...o, [d.id]: x })))
      .catch((e) => setOpen((o) => ({ ...o, [d.id]: errorText(e) })));
  };
  return (
    <>
      <div className="d-flex align-items-center gap-2 mb-3">
        <h1 className="h3 mb-0 flex-grow-1">Decision log</h1>
        <label className="visually-hidden" htmlFor="outcome">Outcome</label>
        <select id="outcome" className="form-select form-select-sm w-auto" value={outcome} onChange={(e) => setOutcome(e.target.value)}>
          <option value="">every outcome</option>
          {outcomes.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
        <button className="btn btn-sm btn-outline-secondary" title="Refresh" aria-label="Refresh the decision log" onClick={load}>
          <i className="bi bi-arrow-clockwise" />
        </button>
      </div>
      <InfoCard id="decisions" title="What this log shows">
        <p>Every idea the agent had for <strong>{status?.account?.name ?? "this account"}</strong> and how it ended: what the
          strategy proposed, what the LLM reviewer said, what the risk engine allowed, and the order it led to. The entry is written
          <em> before</em> the order, so no order exists without its reason.</p>
        <ul className="mb-0">
          <li><strong>rejected</strong>: by the reviewer (any failure of it counts as a no), by eToro's costs, or by the risk rules
            (cash, budget, loss limits, stale prices, the kill switch).</li>
          <li><strong>ordered</strong>: sent to eToro; the order state says what eToro did with it. <strong>order failed</strong>:
            allowed, but not sent.</li>
          <li>Open a row for the regime probabilities, the reasons and, when a reviewer was asked, its prompt and answer with the
            tokens, cost and time it took. Kept 400 days.</li>
        </ul>
      </InfoCard>
      <ProblemAlert error={error} />
      {rows && rows.length === 0 && <p className="text-secondary">No decisions yet.</p>}
      {rows && rows.length > 0 && (
        <div className="card"><div className="table-responsive">
          <table className="table table-sm align-middle mb-0">
            <thead>
              <tr>
                <th><span className="visually-hidden">Details</span></th><th>Time</th><th>Share</th><th>Direction</th><th>Playbook</th>
                <th>Verdict</th><th>Risk</th><th>Outcome</th><th>Order</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((d) => {
                const o = outcomeOf(d.outcome);
                const x = open[d.id];
                return (
                  <Fragment key={d.id}>
                    <tr>
                      <td>
                        <button className="btn btn-sm btn-outline-secondary" aria-expanded={x !== undefined}
                          title={x !== undefined ? "Hide the details" : "Show the details"}
                          aria-label={`${x !== undefined ? "Hide" : "Show"} the details of ${d.symbol || d.instrumentId} at ${new Date(d.at).toLocaleString()}`}
                          onClick={() => toggle(d)}>
                          <i className={`bi ${x !== undefined ? "bi-chevron-down" : "bi-chevron-right"}`} />
                        </button>
                      </td>
                      <td className="text-nowrap">{new Date(d.at).toLocaleString()}</td>
                      <td>{d.symbol || d.instrumentId}</td>
                      <td className="text-capitalize">{d.direction}</td>
                      <td>{d.playbook}</td>
                      <td>{d.verdict ? <span className={"badge " + (verdictBadge[d.verdict] ?? "text-bg-secondary")}>{d.verdict}</span>
                        : <span className="text-secondary small">not reviewed</span>}</td>
                      <td>{d.riskAllowed ? <span className="text-success">{usd(d.riskSizeCents)}</span>
                        : <span className="text-secondary small">{d.outcome === "rejected_risk" ? "refused" : "—"}</span>}</td>
                      <td><span className={"badge " + o.badge}>{o.label}</span></td>
                      <td className="small">{d.orderState ? d.orderState.replace("_", " ") : "—"}</td>
                    </tr>
                    {x !== undefined && (
                      <tr className="decision-detail">
                        <td colSpan={9} className="bg-body-tertiary">
                          {x === "loading" ? <span className="text-secondary small">Loading…</span>
                            : typeof x === "string" ? <div className="alert alert-danger mb-0 small" role="alert">{x}</div>
                              : <Details d={x} />}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div></div>
      )}
      {more && <button className="btn btn-sm btn-outline-secondary mt-3" onClick={older}>Older decisions</button>}
    </>
  );
}

// Details is an opened row: the signal, the regime, the reviewer with its call, the risk
// engine and the order.
function Details({ d: { decision: d, call } }: { d: DecisionDetail }) {
  const probs = Object.entries(d.regimeProbs).sort((a, b) => b[1] - a[1]);
  return (
    <div className="row g-3 small py-2">
      <div className="col-md-4">
        <h3 className="h6">Signal</h3>
        <dl className="row mb-0">
          <dt className="col-5">Entry</dt><dd className="col-7">{d.entry}</dd>
          <dt className="col-5">Stop</dt><dd className="col-7">{d.stopLoss}</dd>
          <dt className="col-5">Target</dt><dd className="col-7">{d.takeProfit ?? "—"}</dd>
          <dt className="col-5">Proposed size</dt><dd className="col-7">{usd(d.suggestedSizeCents)}</dd>
        </dl>
        <p className="mb-0 mt-1">{d.signalReason}</p>
      </div>
      <div className="col-md-4">
        <h3 className="h6">Regime</h3>
        {probs.length === 0 ? <p className="text-secondary mb-0">No probabilities.</p> : (
          <ul className="list-unstyled mb-0">
            {probs.map(([k, v]) => <li key={k}><span className="text-capitalize">{k.replace("_", " ")}</span>: {Math.round(v * 1000) / 10} %</li>)}
          </ul>
        )}
      </div>
      <div className="col-md-4">
        <h3 className="h6">Risk engine</h3>
        <p className="mb-1">{d.riskAllowed ? <>Allowed {usd(d.riskSizeCents)}</> : "Not allowed"}</p>
        <p className="mb-1">{d.riskReason || "—"}</p>
        {d.orderRequestId && <p className="mb-0">Order <code>{d.orderRequestId}</code>{d.orderState && <> · {d.orderState.replace("_", " ")}</>}</p>}
      </div>
      <div className="col-12">
        <h3 className="h6">Reviewer</h3>
        <p className="mb-1">
          {d.verdict ? <><strong>{d.verdict}</strong>{d.verdict === "reduce" && <> to {d.sizeBp / 100} %</>}: </> : null}
          {d.verdictReason || (d.verdict ? "" : "Not reviewed.")}
        </p>
        {call && (
          <>
            <p className="mb-2 text-secondary">
              {call.provider} · <code>{call.model}</code> · {call.inputTokens.toLocaleString()} in / {call.outputTokens.toLocaleString()} out tokens
              {" · "}cost {cost(call.costUsd)} · {call.latencyMs.toLocaleString()} ms
              {call.error && <> · <span className="text-danger">{call.error}</span></>}
            </p>
            <details className="mb-1"><summary>Prompt as sent</summary><pre className="llm-text">{call.prompt}</pre></details>
            <details><summary>Response as received</summary><pre className="llm-text">{call.response || "(none)"}</pre></details>
          </>
        )}
        {!call && d.llmCallId && <p className="text-secondary mb-0">The call's record is no longer kept.</p>}
      </div>
    </div>
  );
}
