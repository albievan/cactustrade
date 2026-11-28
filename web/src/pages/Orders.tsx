import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import { amount, api, errorText, type Order } from "../lib/api";
import { useMode } from "../lib/mode";
import { NeedAccount } from "../components/NeedAccount";
import { InfoCard } from "../components/InfoCard";
import { ProblemAlert } from "../components/ProblemAlert";
import { DirectionBadge } from "../components/DirectionBadge";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { dateTime } from "../lib/format";
import { applyFilters, choices, groupRows, nextSort, sortRows, useTableView, type Column, type Sort } from "../lib/tableView";

const stateBadge: Record<string, string> = {
  created: "text-bg-light", sent: "text-bg-info", pending: "text-bg-info", filled: "text-bg-success",
  partially_filled: "text-bg-success", rejected: "text-bg-danger", cancelled: "text-bg-secondary",
  unknown: "text-bg-warning", abandoned: "text-bg-secondary",
};

const kindWord: Record<string, string> = { open: "Open", close: "Close", cancel: "Cancel", stop: "Sell stop" };
const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : "—");
const num = (s?: string) => (s ? Number(s) : -Infinity);

// quantityText is a Trading 212 order's share quantity, with what filled when that differs.
const quantityText = (o: Order) =>
  o.quantity ? (o.filledQuantity && o.filledQuantity !== o.quantity ? `${o.filledQuantity} of ${o.quantity}` : o.quantity) : "—";

// The columns of the orders table (amounts in cur, the account's currency). Each one can be
// filtered; the "group" ones can group rows.
const columnsIn = (cur?: string): Column<Order>[] => [
  { key: "time", label: "Time", text: (o) => dateTime(o.createdAt), sort: (o) => o.createdAt, filter: "text" },
  { key: "day", label: "Day", text: (o) => new Date(o.createdAt).toLocaleDateString(), sort: (o) => o.createdAt, group: true, hidden: true },
  { key: "kind", label: "Kind", text: (o) => kindWord[o.kind] ?? o.kind, filter: "choice", group: true },
  { key: "what", label: "What", text: (o) => o.kindLabel, filter: "text" },
  { key: "share", label: "Share", text: (o) => o.symbol || String(o.instrumentId), filter: "choice", group: true },
  { key: "ticker", label: "Ticker", text: (o) => o.ticker || "—", filter: "choice", group: true },
  { key: "quantity", label: "Quantity", text: quantityText, sort: (o) => num(o.quantity), filter: "text", align: "end" },
  { key: "exchange", label: "Exchange", text: (o) => o.exchange || "—", filter: "choice", group: true },
  { key: "direction", label: "Direction", text: (o) => cap(o.direction), filter: "choice", group: true },
  { key: "amount", label: "Amount", text: (o) => (o.kind === "open" ? amount(o.amountCents, cur) : "—"), sort: (o) => (o.kind === "open" ? o.amountCents : -1), filter: "text", align: "end" },
  { key: "entry", label: "Entry", text: (o) => o.entry ?? "—", sort: (o) => num(o.entry), filter: "text", align: "end" },
  { key: "stop", label: "Stop", text: (o) => o.stopLoss ?? "—", sort: (o) => num(o.stopLoss), filter: "text", align: "end" },
  { key: "target", label: "Target", text: (o) => o.takeProfit ?? "—", sort: (o) => num(o.takeProfit), filter: "text", align: "end" },
  { key: "state", label: "State", text: (o) => o.stateLabel, filter: "choice", group: true },
  { key: "sends", label: "Sends", text: (o) => String(o.sends), sort: (o) => o.sends, filter: "choice", align: "end" },
  { key: "reason", label: "Reason", text: (o) => o.reason || "—", filter: "choice", group: true },
  { key: "request", label: "Request id", text: (o) => o.requestId, filter: "text" },
];
// The default order: the latest order at the top.
const newestFirst: Sort = { key: "time", desc: true };
// cycleSort: a click sorts by a column (ascending, then descending), a third click goes back to
// the latest at the top; the time column just flips between newest and oldest first.
function cycleSort(cur: Sort, key: string): Sort {
  if (key === "time") return { key, desc: !(cur?.key === "time" && cur.desc) };
  return nextSort(cur, key) ?? newestFirst;
}

// The orders cactusTrade sent to the broker (eToro or Trading 212) for the current account, newest first: a log, so a
// table the viewer can filter by any column, group and sort.
export function Orders() {
  const status = useMode();
  const cur = status?.account?.currency;
  const columns = useMemo(() => columnsIn(cur), [cur]);
  const shown = useMemo(() => columns.filter((c) => !c.hidden), [columns]);
  const groupable = useMemo(() => columns.filter((c) => c.group), [columns]);
  const [orders, setOrders] = useState<Order[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [resolving, setResolving] = useState<Order | null>(null);
  const [settling, setSettling] = useState<Order | null>(null);
  // R50: filters, search, grouping, sort and folded groups are the user's preferences
  const { filters, setFilters, search, setSearch, groupBy, setGroupBy, sort, setSort, closed, setClosed } =
    useTableView("orders.table", { sort: newestFirst });
  const load = useCallback(() => {
    api.orders(500).then((o) => { setOrders(o); setError(""); }).catch(setError);
  }, []);
  const accountId = status?.account?.id;
  useEffect(() => {
    if (!accountId) return;
    load();
    const t = window.setInterval(load, 15_000);
    return () => window.clearInterval(t);
  }, [load, accountId]);

  const rows = useMemo(() => sortRows(applyFilters(orders ?? [], columns, filters, search), columns, sort), [orders, columns, filters, search, sort]);
  const groupCol = groupable.find((c) => c.key === groupBy);
  const unknown = (orders ?? []).filter((o) => o.resolvable);
  const unlisted = (orders ?? []).filter((o) => o.settleable);
  const groups = useMemo(() => groupRows(rows, groupCol), [rows, groupCol]);
  const filtering = search !== "" || Object.values(filters).some(Boolean);
  const setFilter = (k: string, v: string) => setFilters((f) => ({ ...f, [k]: v }));

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
        <ul>
          <li><strong>sent / pending</strong>: eToro has it and has not filled it yet; its cash stays reserved.</li>
          <li><strong>unknown</strong>: the answer was lost. It is never sent again blindly; new entries wait until a lookup settles it.</li>
          <li><strong>abandoned</strong>: eToro never saw it, or it was not sent (stale prices, a kill).</li>
          <li><strong>accepted by eToro</strong> (a cancel request): eToro took cactusTrade's request to cancel an order. It does
            <em>not</em> mean the order was cancelled: if it had already filled, it stays filled; its own line shows which. The
            reason says why cactusTrade asked (e.g. the order was still not filled after 1 minute).</li>
          <li><strong>done</strong> (a close request): the position was closed.</li>
        </ul>
        <p className="mb-0">Filter with the boxes under each heading, search every column at once, click a heading to sort
          (again to reverse, a third time back to the latest at the top), and group by kind, share, exchange, direction, state, reason or day.
          The direction of a close or cancel is that of the position or order it acts on. The latest 500 orders are shown.</p>
      </InfoCard>
      <ProblemAlert error={error} />
      {unknown.length > 0 && (
        <div className="alert alert-warning py-2 small" role="status">
          <i className="bi bi-exclamation-triangle me-1" />{unknown.length === 1 ? "A Trading 212 order's" : `${unknown.length} Trading 212 orders'`} outcome
          is unknown: new entries on the account wait until it is matched. When more than one Trading 212 order could be it (or,
          for a stop or a sale, none), its owner says which one it is (Resolve), or that it was not placed.
        </div>
      )}
      {unlisted.length > 0 && (
        <div className="alert alert-danger py-2 small" role="alert">
          <i className="bi bi-exclamation-octagon me-1" />Trading 212 does not list {unlisted.length === 1 ? "a position" : `${unlisted.length} positions`} of
          cactusTrade's, or its stop or sale. cactusTrade does not take it for closed: it sells nothing more of it and new entries
          on the account wait until Trading 212 lists it. Check the account in Trading 212; if the position is really gone, its
          owner settles it (Settle).
        </div>
      )}
      {resolving && <ResolveDialog order={resolving} onCancel={() => setResolving(null)} onDone={() => { setResolving(null); load(); }} />}
      {settling && <SettleDialog order={settling} onCancel={() => setSettling(null)} onDone={() => { setSettling(null); load(); }} />}
      {orders && orders.length === 0 && <p className="text-secondary">No orders yet.</p>}
      {orders && orders.length > 0 && (
        <>
          <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
            <input className="form-control form-control-sm" style={{ maxWidth: 260 }} type="search" placeholder="Search all columns"
              aria-label="Search all columns" value={search} onChange={(e) => setSearch(e.target.value)} />
            <label className="small text-secondary" htmlFor="orders-group">Group by</label>
            <select id="orders-group" className="form-select form-select-sm w-auto" value={groupBy} onChange={(e) => { setGroupBy(e.target.value); setClosed({}); }}>
              <option value="">No grouping</option>
              {groupable.map((c) => <option key={c.key} value={c.key}>{c.label}</option>)}
            </select>
            {filtering && (
              <button className="btn btn-sm btn-outline-secondary" title="Clear the filters" aria-label="Clear the filters"
                onClick={() => { setFilters({}); setSearch(""); }}>
                <i className="bi bi-funnel" /> <i className="bi bi-x" />
              </button>
            )}
            <span className="small text-secondary ms-auto" aria-live="polite">Showing {rows.length} of {orders.length}</span>
          </div>
          <div className="card"><div className="table-responsive">
            <table className="table table-sm align-middle mb-0">
              <thead>
                <tr>
                  {shown.map((c) => {
                    const dir = sort?.key === c.key ? (sort.desc ? "descending" : "ascending") : "none";
                    return (
                      <th key={c.key} className={(c.align === "end" ? "text-end " : "") + "text-nowrap"} aria-sort={dir}>
                        <button type="button" className="btn btn-link btn-sm p-0 fw-semibold text-reset text-decoration-none"
                          title={`Sort by ${c.label}`} onClick={() => setSort((s) => cycleSort(s, c.key))}>
                          {c.label}
                          {dir !== "none" && <i className={"bi ms-1 " + (dir === "ascending" ? "bi-sort-up" : "bi-sort-down")} aria-hidden="true" />}
                        </button>
                      </th>
                    );
                  })}
                </tr>
                <tr>
                  {shown.map((c) => (
                    <th key={c.key} className="fw-normal">
                      {c.filter === "choice" ? (
                        <select className="form-select form-select-sm" aria-label={`Filter by ${c.label}`} value={filters[c.key] ?? ""}
                          onChange={(e) => setFilter(c.key, e.target.value)}>
                          <option value="">All</option>
                          {choices(orders, c).map((v) => <option key={v} value={v}>{v}</option>)}
                        </select>
                      ) : (
                        <input className="form-control form-control-sm" type="search" aria-label={`Filter by ${c.label}`}
                          value={filters[c.key] ?? ""} onChange={(e) => setFilter(c.key, e.target.value)} />
                      )}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.length === 0 && (
                  <tr><td colSpan={shown.length} className="text-secondary">No orders match the filters.</td></tr>
                )}
                {groups.map((g) => (
                  <Fragment key={g.value}>
                    {groupCol && (
                      <tr className="table-light">
                        <td colSpan={shown.length}>
                          <button type="button" className="btn btn-link btn-sm p-0 text-reset text-decoration-none fw-semibold"
                            aria-expanded={!closed[g.value]} onClick={() => setClosed((c) => ({ ...c, [g.value]: !c[g.value] }))}>
                            <i className={"bi me-1 " + (closed[g.value] ? "bi-chevron-right" : "bi-chevron-down")} aria-hidden="true" />
                            {groupCol.label}: {g.value}
                          </button>
                          <span className="small text-secondary ms-2">{summary(g.rows, cur)}</span>
                        </td>
                      </tr>
                    )}
                    {!closed[g.value] && g.rows.map((o) => (
                      <tr key={o.requestId}>
                        <td className="text-nowrap">{dateTime(o.createdAt)}</td>
                        <td>{kindWord[o.kind] ?? o.kind}</td>
                        <td>{o.kindLabel}</td>
                        <td>{o.symbol || o.instrumentId}</td>
                        <td className="text-nowrap">{o.ticker || "—"}</td>
                        <td className="text-end text-nowrap">{quantityText(o)}</td>
                        <td className="text-nowrap">{o.exchange || "—"}</td>
                        <td><DirectionBadge direction={o.direction} /></td>
                        <td className="text-end">{o.kind === "open" ? amount(o.amountCents, cur) : "—"}</td>
                        <td className="text-end">{o.entry ?? "—"}</td>
                        <td className="text-end">{o.stopLoss ?? "—"}</td>
                        <td className="text-end">{o.takeProfit ?? "—"}</td>
                        <td>
                          <span className={"badge " + (stateBadge[o.state] ?? "text-bg-secondary")}>{o.stateLabel}</span>
                          {o.resolvable && (
                            <button type="button" className="btn btn-sm btn-outline-warning ms-1 py-0" onClick={() => setResolving(o)}
                              aria-label={`Resolve order ${o.requestId}`}>Resolve</button>
                          )}
                          {o.settleable && (
                            <button type="button" className="btn btn-sm btn-outline-danger ms-1 py-0" onClick={() => setSettling(o)}
                              aria-label={`Settle the position of order ${o.requestId}`}>Settle</button>
                          )}
                        </td>
                        <td className="text-end">{o.sends}</td>
                        <td className="small">{o.reason}</td>
                        <td className="small text-secondary"><code>{o.requestId}</code></td>
                      </tr>
                    ))}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div></div>
        </>
      )}
    </>
  );
}

// summary describes a group: how many orders, how many opens and their amount, long vs short.
function summary(rows: Order[], cur?: string): string {
  const opens = rows.filter((o) => o.kind === "open");
  const longs = rows.filter((o) => o.direction === "long").length;
  const shorts = rows.filter((o) => o.direction === "short").length;
  const parts = [`${rows.length} ${rows.length === 1 ? "order" : "orders"}`];
  if (opens.length) parts.push(`${opens.length} ${opens.length === 1 ? "open" : "opens"} for ${amount(opens.reduce((s, o) => s + o.amountCents, 0), cur)}`);
  parts.push(`${longs} long, ${shorts} short`);
  return parts.join(" · ");
}

// SettleDialog lets the account's owner settle the position a Trading 212 buy opened that
// Trading 212 does not list, or whose stop or sale it lists nowhere (re-review 2026-11-28). The
// server checks it again against Trading 212 and refuses while a stop or sale of ours works.
function SettleDialog({ order: o, onCancel, onDone }: { order: Order; onCancel: () => void; onDone: () => void }) {
  const [error, setError] = useState("");
  async function confirm() {
    setError("");
    try {
      await api.settleOrder(o.requestId);
      onDone();
    } catch (e) {
      setError(errorText(e));
    }
  }
  return (
    <ConfirmDialog title={`Settle: ${o.kindLabel}`} confirmLabel="Settle" busyLabel="Settling…" variant="danger" icon="check2"
      onCancel={onCancel} onConfirm={confirm}>
      <p className="small">Trading 212 does not list the position this buy opened ({quantityText(o)} of {o.ticker}, request <code>{o.requestId}</code>),
        or its stop or sale. Check the account in Trading 212 first: is the position gone, and is no sell order of cactusTrade's still working on it?</p>
      <p className="small mb-0">Settling closes it in cactusTrade's records only. Nothing is sent to Trading 212, and cactusTrade never sells anything of
        it afterwards: whatever Trading 212 still holds of {o.ticker} is yours to handle.</p>
      {error && <div className="alert alert-danger py-1 small mt-2 mb-0" role="alert">{error}</div>}
    </ConfirmDialog>
  );
}

// ResolveDialog lets the account's owner say what became of a Trading 212 order whose outcome
// is unknown (T212-d): the Trading 212 order it is, or that it was not placed. The server
// checks the order's ticker, side, type and quantity, and that no other order of ours is it.
function ResolveDialog({ order: o, onCancel, onDone }: { order: Order; onCancel: () => void; onDone: () => void }) {
  const [how, setHow] = useState<"id" | "none">("id");
  const [id, setId] = useState("");
  const [error, setError] = useState("");
  const valid = how === "none" || /^[1-9][0-9]{0,18}$/.test(id.trim());
  async function confirm() {
    setError("");
    try {
      await api.resolveOrder(o.requestId, how === "none" ? { notPlaced: true } : { brokerOrderId: Number(id.trim()) });
      onDone();
    } catch (e) {
      setError(errorText(e));
    }
  }
  return (
    <ConfirmDialog title={`Resolve: ${o.kindLabel}`} confirmLabel="Resolve" busyLabel="Resolving…" variant="warning" icon="check2"
      confirmDisabled={!valid} onCancel={onCancel} onConfirm={confirm}>
      <p className="small">Trading 212 lost its answer to this order ({quantityText(o)} of {o.ticker}, request <code>{o.requestId}</code>)
        and cactusTrade cannot tell which of its orders it is: more than one could be it, or (a stop or a sale) none is listed.
        Look in the Trading 212 app, then say which one it is. "Not placed" is refused while Trading 212 lists an order that
        could be this one: a fill of ours would go unrecorded and a later sale could sell your own shares.</p>
      <div className="form-check">
        <input className="form-check-input" type="radio" id="resolve-id" checked={how === "id"} onChange={() => setHow("id")} />
        <label className="form-check-label" htmlFor="resolve-id">It is Trading 212 order</label>
        <input className="form-control form-control-sm mt-1" inputMode="numeric" aria-label="Trading 212 order id" value={id}
          disabled={how !== "id"} onChange={(e) => setId(e.target.value)} />
      </div>
      <div className="form-check mt-2">
        <input className="form-check-input" type="radio" id="resolve-none" checked={how === "none"} onChange={() => setHow("none")} />
        <label className="form-check-label" htmlFor="resolve-none">It was not placed at Trading 212</label>
      </div>
      {error && <div className="alert alert-danger py-1 small mt-2 mb-0" role="alert">{error}</div>}
    </ConfirmDialog>
  );
}
