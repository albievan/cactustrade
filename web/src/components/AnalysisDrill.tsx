import { Fragment, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { usd, type Analysis, type AnalysisGroup, type AnalysisGrouping, type AnalysisTradeRow } from "../lib/api";
import { applyFilters, choices, groupRows, nextSort, sortRows, type Column, type Filters, type Sort } from "../lib/tableView";

// The analysis tiles' drill-down (R35): what is behind each figure. Every number comes from
// the server's analysis (its groups and its list of closed trades); this only arranges it.

export type Tile = "trades" | "winrate" | "pnl" | "average" | "fees" | "drawdown" | "ours" | "llm";

export const TILE_TITLES: Record<Tile, string> = {
  trades: "Closed trades", winrate: "Win rate", pnl: "Realised P&L (after fees)", average: "Average per trade",
  fees: "Fees reported by eToro", drawdown: "Largest drawdown", ours: "Opened by cactusTrade", llm: "LLM reviewer cost",
};

const DIMENSIONS: AnalysisGrouping["dimension"][] = ["exchange", "account", "theme", "playbook", "direction", "date"];

export function AnalysisDrill({ a, tile, onClose, equity }: { a: Analysis; tile: Tile; onClose: () => void; equity: ReactNode }) {
  const ours = a.trades.filter((t) => t.ours);
  return (
    <div className="card mb-4 border-primary" role="region" aria-label={`Details: ${TILE_TITLES[tile]}`}>
      <div className="card-body">
        <div className="d-flex align-items-center gap-2 mb-2">
          <h2 className="h5 mb-0 flex-grow-1"><i className="bi bi-zoom-in me-2" aria-hidden="true" />{TILE_TITLES[tile]}: the details</h2>
          <button type="button" className="btn btn-sm btn-outline-secondary" title="Close the details" aria-label="Close the details" onClick={onClose}>
            <i className="bi bi-x-lg" />
          </button>
        </div>
        {tile === "trades" && (
          <>
            <p className="small text-secondary">Every closed trade of positions cactusTrade opened in the period, newest first. Filter, group or sort by any column.</p>
            <TradeTable key="trades" rows={ours} label="cactusTrade's closed trades" />
          </>
        )}
        {tile === "winrate" && (
          <>
            <p className="small text-secondary">Wins, losses and the win rate of each group; below, the trades grouped by their result.</p>
            <Breakdown a={a} figure="winrate" />
            <TradeTable key="winrate" rows={ours} label="cactusTrade's closed trades" group="outcome" />
          </>
        )}
        {tile === "pnl" && (
          <>
            <p className="small text-secondary">Realised P&amp;L after the fees eToro reported, by group; below, every trade from the best to the worst.</p>
            <Breakdown a={a} figure="pnl" />
            <TradeTable key="pnl" rows={ours} label="cactusTrade's closed trades" sort={{ key: "net", desc: true }} />
          </>
        )}
        {tile === "average" && (
          <>
            <p className="small text-secondary">The average realised P&amp;L per trade of each group, with its best and worst trade.</p>
            <Breakdown a={a} figure="average" />
            <TradeTable key="average" rows={ours} label="cactusTrade's closed trades" sort={{ key: "net", desc: true }} />
          </>
        )}
        {tile === "fees" && (
          <>
            <p className="small text-secondary">
              The fees eToro reported per trade, largest first. Trades whose fees eToro did not report show “unknown”; they are
              never counted as 0.{a.totals.feesUnknownTrades > 0 && ` ${a.totals.feesUnknownTrades} ${a.totals.feesUnknownTrades === 1 ? "trade has" : "trades have"} unknown fees.`}
            </p>
            <Breakdown a={a} figure="fees" />
            <TradeTable key="fees" rows={ours} label="cactusTrade's closed trades" sort={{ key: "fees", desc: true }} />
          </>
        )}
        {tile === "drawdown" && (
          <>
            <p className="small text-secondary">
              The largest fall from a high in recorded equity, per account and per currency (currencies are never added together).
              Equity includes deposits, withdrawals and positions cactusTrade did not open.
            </p>
            {equity}
          </>
        )}
        {tile === "ours" && (
          <>
            <p className="small text-secondary">
              {ours.length} of {a.trades.length} closed {a.trades.length === 1 ? "trade" : "trades"} listed were of positions cactusTrade opened.
              The others (opened by you or before cactusTrade) are left alone and not counted in any figure.
            </p>
            <TradeTable key="ours" rows={a.trades} label="All closed trades" group="ours" withOurs />
          </>
        )}
        {tile === "llm" && <LlmDetails a={a} />}
        {a.tradesCapped && tile !== "drawdown" && tile !== "llm" && (
          <p className="small text-warning-emphasis mt-2 mb-0">Only the latest {a.trades.length} trades are listed; the figures above count them all.</p>
        )}
      </div>
    </div>
  );
}

// Breakdown shows the figure of the tile for each group of a chosen dimension.
function Breakdown({ a, figure }: { a: Analysis; figure: "winrate" | "pnl" | "average" | "fees" }) {
  const [dim, setDim] = useState<AnalysisGrouping["dimension"]>("exchange");
  const g = a.groupings.find((x) => x.dimension === dim);
  const groups = (g?.groups ?? []).filter((x) => x.trades > 0);
  return (
    <div className="mb-3">
      <div className="btn-group btn-group-sm flex-wrap mb-2" role="group" aria-label="Break down by">
        {DIMENSIONS.filter((d) => a.groupings.some((x) => x.dimension === d)).map((d) => (
          <button key={d} type="button" className={"btn " + (d === dim ? "btn-secondary" : "btn-outline-secondary")} aria-pressed={d === dim}
            onClick={() => setDim(d)}>{a.groupings.find((x) => x.dimension === d)?.title ?? d}</button>
        ))}
      </div>
      {groups.length === 0 ? <p className="small text-secondary mb-0">No closed trades in this period.</p> : (
        <div className="table-responsive">
          <table className="table table-sm align-middle mb-0" aria-label={`${g?.title ?? dim}`}>
            <thead>
              <tr>
                <th>{g?.title.replace(/^By /, "") ?? dim}</th><th className="text-end">Trades</th>
                {figure === "winrate" && <><th className="text-end">Wins</th><th className="text-end">Losses</th><th className="text-end">Breakeven</th><th className="text-end">Win rate</th></>}
                {figure === "pnl" && <><th className="text-end">Before fees</th><th className="text-end">Fees</th><th className="text-end">Realised P&amp;L</th></>}
                {figure === "average" && <><th className="text-end">Average per trade</th><th>Best</th><th>Worst</th></>}
                {figure === "fees" && <><th className="text-end">Fees reported</th><th className="text-end">Fees unknown</th></>}
              </tr>
            </thead>
            <tbody>
              {groups.map((x) => (
                <tr key={x.key}>
                  <td>{x.label}</td><td className="text-end">{x.trades}</td>
                  {figure === "winrate" && <><td className="text-end">{x.wins}</td><td className="text-end">{x.losses}</td><td className="text-end">{x.breakeven}</td><td className="text-end">{rate(x)}</td></>}
                  {figure === "pnl" && <><td className="text-end">{usd(x.grossCents)}</td><td className="text-end">{usd(x.feesCents)}</td><td className={"text-end " + tone(x.netCents)}>{usd(x.netCents)}</td></>}
                  {figure === "average" && <>
                    <td className={"text-end " + tone(x.avgNetCents ?? 0)}>{x.avgNetCents === undefined ? "—" : usd(x.avgNetCents)}</td>
                    <td className="small">{x.best ? `${x.best.symbol || "#" + x.best.positionId} ${usd(x.best.netCents)}` : "—"}</td>
                    <td className="small">{x.worst ? `${x.worst.symbol || "#" + x.worst.positionId} ${usd(x.worst.netCents)}` : "—"}</td>
                  </>}
                  {figure === "fees" && <><td className="text-end">{usd(x.feesCents)}</td><td className="text-end">{x.feesUnknownTrades || "—"}</td></>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function rate(g: AnalysisGroup): string {
  return g.winRateBp === undefined ? "—" : `${(g.winRateBp / 100).toFixed(2)} %`;
}

function tone(v: number): string {
  return v > 0 ? "pnl-up" : v < 0 ? "pnl-down" : "";
}

// The trade table's columns: each can be filtered; the "group" ones can group rows.
const when = (iso: string) => (iso && !iso.startsWith("0001") ? new Date(iso).toLocaleString() : "—");
const baseColumns: Column<AnalysisTradeRow>[] = [
  { key: "closed", label: "Closed", text: (t) => when(t.closedAt), sort: (t) => t.closedAt, filter: "text" },
  { key: "day", label: "Day", text: (t) => new Date(t.closedAt).toLocaleDateString(), sort: (t) => t.closedAt, group: true, hidden: true },
  { key: "account", label: "Account", text: (t) => t.account, filter: "choice", group: true },
  { key: "share", label: "Share", text: (t) => t.symbol || `#${t.positionId}`, filter: "choice", group: true },
  { key: "exchange", label: "Exchange", text: (t) => t.exchange, filter: "choice", group: true },
  { key: "theme", label: "Theme", text: (t) => t.theme, filter: "choice", group: true },
  { key: "playbook", label: "Playbook", text: (t) => t.playbook, filter: "choice", group: true },
  { key: "direction", label: "Direction", text: (t) => (t.direction === "long" ? "Long" : "Short"), filter: "choice", group: true },
  { key: "opened", label: "Opened", text: (t) => when(t.openedAt), sort: (t) => t.openedAt, filter: "text" },
  { key: "open", label: "Open rate", text: (t) => t.openRate ?? "—", sort: (t) => Number(t.openRate ?? -1), filter: "text", align: "end" },
  { key: "close", label: "Close rate", text: (t) => t.closeRate ?? "—", sort: (t) => Number(t.closeRate ?? -1), filter: "text", align: "end" },
  { key: "invested", label: "Invested", text: (t) => usd(t.investmentCents), sort: (t) => t.investmentCents, filter: "text", align: "end" },
  { key: "fees", label: "Fees", text: (t) => (t.feesKnown ? usd(t.feesCents) : "unknown"), sort: (t) => (t.feesKnown ? t.feesCents : -1), filter: "text", align: "end" },
  { key: "net", label: "Realised P&L", text: (t) => usd(t.netCents), sort: (t) => t.netCents, filter: "text", align: "end" },
  { key: "outcome", label: "Result", text: (t) => t.outcome, filter: "choice", group: true },
];
const oursColumn: Column<AnalysisTradeRow> = {
  key: "ours", label: "Opened by", text: (t) => (t.ours ? "cactusTrade" : "not cactusTrade (not counted)"), filter: "choice", group: true,
};

// TradeTable lists closed trades; it can be searched, filtered by any column, grouped and sorted.
export function TradeTable({ rows, label, group = "", sort: initialSort = { key: "closed", desc: true }, withOurs = false }: {
  rows: AnalysisTradeRow[]; label: string; group?: string; sort?: Sort; withOurs?: boolean;
}) {
  const columns = useMemo(() => (withOurs ? [...baseColumns, oursColumn] : baseColumns), [withOurs]);
  const shown = columns.filter((c) => !c.hidden);
  const groupable = columns.filter((c) => c.group);
  const [filters, setFilters] = useState<Filters>({});
  const [search, setSearch] = useState("");
  const [groupBy, setGroupBy] = useState(group);
  const [sort, setSort] = useState<Sort>(initialSort);
  const [closed, setClosed] = useState<Record<string, boolean>>({});
  const list = useMemo(() => sortRows(applyFilters(rows, columns, filters, search), columns, sort), [rows, columns, filters, search, sort]);
  const groupCol = groupable.find((c) => c.key === groupBy);
  const groups = useMemo(() => groupRows(list, groupCol), [list, groupCol]);
  if (rows.length === 0) return <p className="small text-secondary mb-0">No closed trades in this period.</p>;
  const filtering = search !== "" || Object.values(filters).some(Boolean);
  return (
    <>
      <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
        <input className="form-control form-control-sm" style={{ maxWidth: 240 }} type="search" placeholder="Search all columns"
          aria-label="Search the trades" value={search} onChange={(e) => setSearch(e.target.value)} />
        <label className="small text-secondary" htmlFor={`drill-group-${label}`}>Group by</label>
        <select id={`drill-group-${label}`} className="form-select form-select-sm w-auto" value={groupBy} onChange={(e) => { setGroupBy(e.target.value); setClosed({}); }}>
          <option value="">No grouping</option>
          {groupable.map((c) => <option key={c.key} value={c.key}>{c.label}</option>)}
        </select>
        {filtering && (
          <button type="button" className="btn btn-sm btn-outline-secondary" title="Clear the filters" aria-label="Clear the trade filters"
            onClick={() => { setFilters({}); setSearch(""); }}>
            <i className="bi bi-funnel" /> <i className="bi bi-x" />
          </button>
        )}
        <span className="small text-secondary ms-auto" aria-live="polite">Showing {list.length} of {rows.length}</span>
      </div>
      <div className="table-responsive">
        <table className="table table-sm align-middle mb-0" aria-label={label}>
          <thead>
            <tr>
              {shown.map((c) => {
                const dir = sort?.key === c.key ? (sort.desc ? "descending" : "ascending") : "none";
                return (
                  <th key={c.key} className={(c.align === "end" ? "text-end " : "") + "text-nowrap"} aria-sort={dir}>
                    <button type="button" className="btn btn-link btn-sm p-0 fw-semibold text-reset text-decoration-none" title={`Sort by ${c.label}`}
                      onClick={() => setSort((s) => nextSort(s, c.key) ?? initialSort)}>
                      {c.label}{dir !== "none" && <i className={"bi ms-1 " + (dir === "ascending" ? "bi-sort-up" : "bi-sort-down")} aria-hidden="true" />}
                    </button>
                  </th>
                );
              })}
            </tr>
            <tr>
              {shown.map((c) => (
                <th key={c.key} className="fw-normal">
                  {c.filter === "choice" ? (
                    <select className="form-select form-select-sm" aria-label={`Filter trades by ${c.label}`} value={filters[c.key] ?? ""}
                      onChange={(e) => setFilters((f) => ({ ...f, [c.key]: e.target.value }))}>
                      <option value="">All</option>
                      {choices(rows, c).map((v) => <option key={v} value={v}>{v}</option>)}
                    </select>
                  ) : (
                    <input className="form-control form-control-sm" type="search" aria-label={`Filter trades by ${c.label}`} value={filters[c.key] ?? ""}
                      onChange={(e) => setFilters((f) => ({ ...f, [c.key]: e.target.value }))} />
                  )}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {list.length === 0 && <tr><td colSpan={shown.length} className="text-secondary">No trades match the filters.</td></tr>}
            {groups.map((g) => (
              <Fragment key={g.value}>
                {groupCol && (
                  <tr className="table-light">
                    <td colSpan={shown.length}>
                      <button type="button" className="btn btn-link btn-sm p-0 text-reset text-decoration-none fw-semibold" aria-expanded={!closed[g.value]}
                        onClick={() => setClosed((c) => ({ ...c, [g.value]: !c[g.value] }))}>
                        <i className={"bi me-1 " + (closed[g.value] ? "bi-chevron-right" : "bi-chevron-down")} aria-hidden="true" />
                        {groupCol.label}: {g.value}
                      </button>
                      <span className="small text-secondary ms-2">{summary(g.rows)}</span>
                    </td>
                  </tr>
                )}
                {!closed[g.value] && g.rows.map((t) => (
                  <tr key={`${t.accountId}-${t.positionId}`} className={t.ours ? "" : "text-secondary"}>
                    {shown.map((c) => (
                      <td key={c.key} className={(c.align === "end" ? "text-end " : "") + (c.key === "net" ? tone(t.netCents) : "") + (c.key === "closed" || c.key === "opened" ? " text-nowrap small" : "")}>
                        {c.key === "direction"
                          ? <span className={"badge " + (t.direction === "long" ? "text-bg-success" : "text-bg-danger")}>{c.text(t)}</span>
                          : c.text(t)}
                      </td>
                    ))}
                  </tr>
                ))}
              </Fragment>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

// summary describes a group of trades: how many, wins/losses and the realised P&L (only the
// trades cactusTrade opened are added up, as in every figure).
function summary(rows: AnalysisTradeRow[]): string {
  const ours = rows.filter((t) => t.ours);
  const wins = ours.filter((t) => t.outcome === "Win").length;
  const losses = ours.filter((t) => t.outcome === "Loss").length;
  const parts = [`${rows.length} ${rows.length === 1 ? "trade" : "trades"}`];
  if (ours.length) parts.push(`${wins} won, ${losses} lost`, `realised ${usd(ours.reduce((s, t) => s + t.netCents, 0))}`);
  if (ours.length < rows.length) parts.push(`${rows.length - ours.length} not counted`);
  return parts.join(" · ");
}

function LlmDetails({ a }: { a: Analysis }) {
  const l = a.llm;
  const rows: [string, string][] = [
    ["Calls to the LLM reviewer", String(l.calls)],
    ["Calls that failed (each counted as a reject)", String(l.failed)],
    ["Input tokens", l.inputTokens.toLocaleString()],
    ["Output tokens", l.outputTokens.toLocaleString()],
    ["Known cost", `$${l.knownCostUsd}`],
    ["Calls without a price (cost unknown)", String(l.costUnknownCalls)],
  ];
  return (
    <>
      <table className="table table-sm mb-2" aria-label="LLM reviewer use">
        <tbody>{rows.map(([k, v]) => <tr key={k}><td>{k}</td><td className="text-end">{v}</td></tr>)}</tbody>
      </table>
      {a.verdicts.length > 0 && (
        <p className="small mb-2">Verdicts: {a.verdicts.map((v) => `${v.label} ${v.n}`).join(" · ")}.</p>
      )}
      {l.costUnknownCalls > 0 && (
        <p className="small mb-2">
          The cost is unknown for calls whose LLM profile has no price. Enter the price per million tokens on the{" "}
          <Link to="/llm">LLM reviewer</Link> page and later calls are costed.
        </p>
      )}
      <p className="small mb-0">Each call, with its prompt, answer and verdict, is in the <Link to="/decisions">Decision log</Link>.</p>
    </>
  );
}
