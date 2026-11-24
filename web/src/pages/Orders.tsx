import { useCallback, useEffect, useState } from "react";
import { api, errorText, usd, type Order } from "../lib/api";
import { useMode } from "../lib/mode";
import { NeedAccount } from "../components/NeedAccount";
import { InfoCard } from "../components/InfoCard";

const stateBadge: Record<string, string> = {
  created: "text-bg-light", sent: "text-bg-info", pending: "text-bg-info", filled: "text-bg-success",
  partially_filled: "text-bg-success", rejected: "text-bg-danger", cancelled: "text-bg-secondary",
  unknown: "text-bg-warning", abandoned: "text-bg-secondary",
};

// The orders cactusTrade sent to eToro for the current account, newest first: a log, so a table.
export function Orders() {
  const status = useMode();
  const [orders, setOrders] = useState<Order[] | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    api.orders(200).then((o) => { setOrders(o); setError(""); }).catch((e) => setError(errorText(e)));
  }, []);
  const accountId = status?.account?.id;
  useEffect(() => {
    if (!accountId) return;
    load();
    const t = window.setInterval(load, 15_000);
    return () => window.clearInterval(t);
  }, [load, accountId]);
  if (status && !status.account) return <><h1 className="h3">Orders</h1><NeedAccount /></>;
  return (
    <>
      <div className="d-flex align-items-center gap-2 mb-3">
        <h1 className="h3 mb-0 flex-grow-1">Orders</h1>
        <button className="btn btn-sm btn-outline-secondary" title="Refresh" aria-label="Refresh the orders" onClick={load}>
          <i className="bi bi-arrow-clockwise" />
        </button>
      </div>
      <InfoCard id="orders" title="What this list shows">
        <p>Every request cactusTrade sent to eToro for <strong>{status?.account?.name ?? "this account"}</strong>: opens, closes and
          cancels. Each one gets its request id stored <em>before</em> it is sent, so a retry never doubles an order.</p>
        <ul className="mb-0">
          <li><strong>sent / pending</strong>: eToro has it and has not filled it yet; its cash stays reserved.</li>
          <li><strong>unknown</strong>: the answer was lost. It is never sent again blindly; new entries wait until a lookup settles it.</li>
          <li><strong>abandoned</strong>: eToro never saw it, or it was not sent (stale prices, a kill).</li>
        </ul>
      </InfoCard>
      {error && <div className="alert alert-danger" role="alert">{error}</div>}
      {orders && orders.length === 0 && <p className="text-secondary">No orders yet.</p>}
      {orders && orders.length > 0 && (
        <div className="card"><div className="table-responsive">
          <table className="table table-sm align-middle mb-0">
            <thead>
              <tr>
                <th>Time</th><th>Kind</th><th>Share</th><th>Direction</th><th className="text-end">Amount</th>
                <th className="text-end">Entry</th><th className="text-end">Stop</th><th className="text-end">Target</th>
                <th>State</th><th className="text-end">Sends</th><th>Reason</th><th>Request id</th>
              </tr>
            </thead>
            <tbody>
              {orders.map((o) => (
                <tr key={o.requestId}>
                  <td className="text-nowrap">{new Date(o.createdAt).toLocaleString()}</td>
                  <td className="text-capitalize">{o.kind}</td>
                  <td>{o.symbol || o.instrumentId}</td>
                  <td className="text-capitalize">{o.direction || "—"}</td>
                  <td className="text-end">{o.kind === "open" ? usd(o.amountCents) : "—"}</td>
                  <td className="text-end">{o.entry ?? "—"}</td>
                  <td className="text-end">{o.stopLoss ?? "—"}</td>
                  <td className="text-end">{o.takeProfit ?? "—"}</td>
                  <td><span className={"badge " + (stateBadge[o.state] ?? "text-bg-secondary")}>{o.state.replace("_", " ")}</span></td>
                  <td className="text-end">{o.sends}</td>
                  <td className="small">{o.reason}</td>
                  <td className="small text-secondary"><code>{o.requestId}</code></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div></div>
      )}
    </>
  );
}
