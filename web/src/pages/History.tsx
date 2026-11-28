import { useCallback, useEffect, useState } from "react";
import { EquityCharts } from "../components/EquityChart";
import { InfoCard } from "../components/InfoCard";
import { NeedAccount } from "../components/NeedAccount";
import { ProblemAlert } from "../components/ProblemAlert";
import {
  api, exportUrl, usd, type DailyHistory, type EquitySeries, type HistoryDay, type HistoryFile, type HistorySummary, type HistoryTrade,
} from "../lib/api";
import { useMode } from "../lib/mode";
import { oneOf, usePref } from "../lib/prefs";

// The current account's history (PLAN 6.12): counts and sums of its closed trades, the
// figures per risk day, the equity curve with its drawdowns, the trades and CSV exports.
// Every figure comes from the server; this page only formats it, in neutral words.

export const PAST_ONLY = "Figures describe past results only; they do not predict future results.";

const PERIODS: { label: string; days: number }[] = [
  { label: "1W", days: 7 }, { label: "1M", days: 30 }, { label: "3M", days: 90 }, { label: "1Y", days: 365 },
];
const isPeriod = oneOf(PERIODS.map((p) => p.days));
const PAGE = 50;

const EXPORTS: { file: HistoryFile; icon: string; label: string }[] = [
  { file: "trades.csv", icon: "bi-list-check", label: "Trades" },
  { file: "daily.csv", icon: "bi-calendar3", label: "Daily figures" },
  { file: "equity.csv", icon: "bi-graph-up", label: "Equity snapshots" },
  { file: "orders.csv", icon: "bi-receipt", label: "Orders" },
  { file: "decisions.csv", icon: "bi-journal-check", label: "Decisions" },
];

type Who = "ours" | "all";
// R50: the period, whose trades and the dates are the user's preferences
const isWho = oneOf<Who>(["ours", "all"]);
const isDay = (v: unknown) => typeof v === "string" && (v === "" || /^\d{4}-\d{2}-\d{2}$/.test(v));

// dayStart turns a date field (YYYY-MM-DD, the user's calendar) into the start of that day,
// or of the next day with next (an inclusive "to"), as an RFC 3339 time.
function dayStart(value: string, next = false): string | undefined {
  if (!value) return undefined;
  const [y, m, d] = value.split("-").map(Number);
  if (!y || !m || !d) return undefined;
  return new Date(y, m - 1, d + (next ? 1 : 0)).toISOString();
}

export function History() {
  const status = useMode();
  const accountId = status?.account?.id;
  const [days, setDays] = usePref<number>("history.days", 30, isPeriod);
  const [summary, setSummary] = useState<HistorySummary | null>(null);
  const [daily, setDaily] = useState<DailyHistory | null>(null);
  const [series, setSeries] = useState<EquitySeries[] | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    if (!accountId) return;
    setError(null);
    api.historySummary(accountId, days).then(setSummary).catch(setError);
    api.historyDaily(accountId, days).then(setDaily).catch(setError);
    api.equityHistory(days).then((all) => setSeries(all.filter((s) => s.accountId === accountId))).catch(() => setSeries([]));
  }, [accountId, days]);

  if (status && !status.account) return <><h1 className="h3">History</h1><NeedAccount /></>;
  return (
    <>
      <div className="d-flex flex-wrap align-items-center gap-2 mb-3">
        <h1 className="h3 mb-0 flex-grow-1">History</h1>
        <div className="btn-group btn-group-sm" role="group" aria-label="Period">
          {PERIODS.map((p) => (
            <button key={p.label} type="button" className={"btn " + (days === p.days ? "btn-success" : "btn-outline-secondary")}
              aria-pressed={days === p.days} title={`The last ${p.days} risk days`} onClick={() => setDays(p.days)}>{p.label}</button>
          ))}
        </div>
      </div>
      <div className="alert alert-secondary py-2 small" role="note"><i className="bi bi-info-circle me-1" />{PAST_ONLY}</div>
      <InfoCard id="history" title="What these figures are">
        <ul className="small mb-0">
          <li>The counts and sums are over the trades of positions <strong>cactusTrade opened</strong> in
            {" "}<strong>{status?.account?.name ?? "this account"}</strong>, closed in the period (risk days start at 22:00 UTC).
            Positions opened outside cactusTrade are listed in the trades table but not counted.</li>
          <li>Net figures are eToro's reported net result per trade. When eToro did not report a trade's fees, they are shown as
            <em> unknown</em>, never as 0.</li>
          <li>The equity change of a day includes deposits, withdrawals and positions cactusTrade did not open.</li>
          <li>The drawdown is the fall of the account's equity from its highest recorded value (snapshots every 15 minutes).</li>
        </ul>
      </InfoCard>
      <ProblemAlert error={error} accountId={accountId} />
      <SummaryCards s={summary} />
      <div className="row g-3 mb-4">
        <div className="col-xl-6">
          <div className="card h-100"><div className="card-body">
            <h2 className="h5">Equity and drawdown</h2>
            {series && <EquityCharts series={series} days={days} drawdown />}
          </div></div>
        </div>
        <div className="col-xl-6"><Daily daily={daily} /></div>
      </div>
      {accountId && <Trades key={accountId} accountId={accountId} summary={summary} />}
    </>
  );
}

function SummaryCards({ s }: { s: HistorySummary | null }) {
  if (!s) return null;
  const none = s.trades === 0;
  const dd = s.drawdown;
  return (
    <>
      {none && <p className="text-secondary" role="status">No closed trades in this period.</p>}
      <div className="row g-3 mb-4">
        <Card label="Closed trades" icon="bi-list-check" value={String(s.trades)}
          sub={s.foreignTrades > 0 ? `${s.foreignTrades} more not opened by cactusTrade (not counted)` : undefined} />
        <Card label="Wins / losses / breakeven" icon="bi-plus-slash-minus" value={none ? "—" : `${s.wins} / ${s.losses} / ${s.breakeven}`} />
        <Card label="Win rate" icon="bi-percent" value={s.winRateBp === undefined ? "—" : bp(s.winRateBp)}
          sub={s.winRateBp === undefined ? "no closed trades" : `wins of ${s.trades} trades`} />
        <Card label="Fees" icon="bi-receipt" value={fees(s.feesCents, s.feesUnknownTrades, s.trades)}
          sub={s.feesUnknownTrades > 0 && s.feesUnknownTrades < s.trades ? `unknown for ${s.feesUnknownTrades} trades` : undefined} />
        <Card label="Realised P&L (net)" icon="bi-cash-stack" value={none ? "—" : usd(s.netCents)}
          sub={none ? undefined : `gross ${usd(s.grossCents)}`} />
        <Card label="Max drawdown" icon="bi-graph-down" value={dd.known ? usd(dd.maxDrawdownCents) : "unknown"}
          sub={dd.known ? `${bp(dd.maxDrawdownBp)} from a peak · now ${usd(dd.currentCents)} (${bp(dd.currentBp)})` : "no equity snapshots in the period"} />
      </div>
    </>
  );
}

function Card({ label, icon, value, sub }: { label: string; icon: string; value: string; sub?: string }) {
  return (
    <div className="col-6 col-lg-4 col-xxl-2">
      <div className="card stat h-100"><div className="card-body">
        <div className="text-secondary small"><i className={"bi me-1 " + icon} />{label}</div>
        <div className="value">{value}</div>
        {sub && <div className="small text-secondary">{sub}</div>}
      </div></div>
    </div>
  );
}

// fees: "unknown" when no trade's fees are known, else the known sum.
function fees(cents: number, unknown: number, trades: number): string {
  if (trades > 0 && unknown >= trades) return "unknown";
  return usd(cents);
}

// bp formats basis points as a percentage with two places (5000 → "50.00 %").
function bp(v: number): string {
  return `${(v / 100).toFixed(2)} %`;
}

function Daily({ daily }: { daily: DailyHistory | null }) {
  const rows = daily?.rows ?? [];
  return (
    <div className="card h-100"><div className="card-body">
      <h2 className="h5">Per risk day</h2>
      {daily && rows.every((r) => r.trades === 0 && r.equityChangeCents === undefined) && <p className="text-secondary mb-0">Nothing recorded in this period.</p>}
      {rows.some((r) => r.trades > 0) && <DailyBars rows={rows} />}
      {rows.length > 0 && !rows.every((r) => r.trades === 0 && r.equityChangeCents === undefined) && (
        <div className="table-responsive daily-table">
          <table className="table table-sm align-middle mb-0">
            <thead><tr>
              <th>Day</th><th className="text-end">Trades</th><th className="text-end">Realised (net)</th><th className="text-end">Fees</th>
              <th className="text-end" title="includes deposits, withdrawals and positions not opened by cactusTrade">Equity change*</th>
            </tr></thead>
            <tbody>
              {[...rows].reverse().map((r) => (
                <tr key={r.day}>
                  <td className="text-nowrap">{r.day}</td>
                  <td className="text-end">{r.trades}</td>
                  <td className="text-end">{r.trades > 0 ? usd(r.realisedCents) : "—"}</td>
                  <td className="text-end">{r.trades > 0 ? dayFees(r) : "—"}</td>
                  <td className="text-end">{r.equityChangeCents === undefined ? "—" : usd(r.equityChangeCents)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="small text-secondary mt-2 mb-0">* The equity change includes deposits, withdrawals and positions not opened by cactusTrade.</p>
    </div></div>
  );
}

function dayFees(r: HistoryDay): string {
  if (r.feesUnknownTrades >= r.trades) return "unknown";
  return r.feesUnknownTrades > 0 ? `${usd(r.feesCents)} + unknown` : usd(r.feesCents);
}

// DailyBars draws the realised figure of each day as a bar above or below zero (inline SVG).
function DailyBars({ rows }: { rows: HistoryDay[] }) {
  const W = 600;
  const H = 140;
  const most = Math.max(1, ...rows.map((r) => Math.abs(r.realisedCents)));
  const mid = H / 2;
  const step = W / rows.length;
  return (
    <figure className="daily-bars mb-2">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Realised result per risk day" preserveAspectRatio="none">
        <line x1={0} x2={W} y1={mid} y2={mid} className="zero" />
        {rows.map((r, i) => {
          const h = (Math.abs(r.realisedCents) / most) * (mid - 4);
          return (
            <rect key={r.day} x={i * step + step * 0.15} width={step * 0.7} y={r.realisedCents >= 0 ? mid - h : mid} height={h}
              className={r.realisedCents >= 0 ? "up" : "down"}>
              <title>{`${r.day}: ${usd(r.realisedCents)} (${r.trades} trades)`}</title>
            </rect>
          );
        })}
      </svg>
    </figure>
  );
}

function Trades({ accountId, summary }: { accountId: string; summary: HistorySummary | null }) {
  const [who, setWho] = usePref<Who>("history.trades.who", "ours", isWho);
  const [from, setFrom] = usePref<string>("history.trades.from", "", isDay);
  const [to, setTo] = usePref<string>("history.trades.to", "", isDay);
  const [rows, setRows] = useState<HistoryTrade[] | null>(null);
  const [more, setMore] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const fromIso = dayStart(from) ?? summary?.from;
  const toIso = dayStart(to, true) ?? (to ? undefined : summary?.to);

  const load = useCallback((before?: string) => {
    setError(null);
    api.historyTrades({ accountId, from: dayStart(from), to: dayStart(to, true), ours: who === "ours" ? true : undefined, before, limit: PAGE })
      .then((page) => {
        setRows((old) => (before && old ? [...old, ...page] : page));
        setMore(page.length === PAGE);
      })
      .catch(setError);
  }, [accountId, from, to, who]);
  useEffect(() => load(), [load]);
  const last = rows && rows.length > 0 ? rows[rows.length - 1].closedAt : undefined;

  return (
    <div className="card"><div className="card-body">
      <div className="d-flex flex-wrap align-items-end gap-2 mb-3">
        <h2 className="h5 mb-0 me-auto">Closed trades</h2>
        <div className="btn-group btn-group-sm" role="group" aria-label="Whose trades">
          <button type="button" className={"btn " + (who === "ours" ? "btn-success" : "btn-outline-secondary")} aria-pressed={who === "ours"}
            onClick={() => setWho("ours")}>cactusTrade's</button>
          <button type="button" className={"btn " + (who === "all" ? "btn-success" : "btn-outline-secondary")} aria-pressed={who === "all"}
            onClick={() => setWho("all")}>All</button>
        </div>
        <label className="small">From<input type="date" className="form-control form-control-sm" value={from} onChange={(e) => setFrom(e.target.value)} aria-label="Closed from" /></label>
        <label className="small">To<input type="date" className="form-control form-control-sm" value={to} onChange={(e) => setTo(e.target.value)} aria-label="Closed until (inclusive)" /></label>
      </div>
      <div className="d-flex flex-wrap align-items-center gap-1 mb-3" aria-label="Export as CSV">
        <span className="small text-secondary me-1">CSV{from || to ? " (the dates above)" : " (the period)"}:</span>
        {EXPORTS.map((x) => (
          <a key={x.file} className="btn btn-sm btn-outline-secondary" href={exportUrl(x.file, accountId, fromIso, toIso)} download
            title={`Download ${x.label.toLowerCase()} as CSV`} aria-label={`Download ${x.label.toLowerCase()} as CSV`}>
            <i className={"bi me-1 " + x.icon} />{x.label}
          </a>
        ))}
      </div>
      <ProblemAlert error={error} accountId={accountId} />
      {rows && rows.length === 0 && <p className="text-secondary mb-0">No closed trades{from || to ? " between these dates" : ""}.</p>}
      {rows && rows.length > 0 && (
        <div className="table-responsive">
          <table className="table table-sm align-middle mb-0">
            <thead><tr>
              <th>Closed</th><th>Share</th><th>Opened by</th><th>Direction</th><th className="text-end">Units</th><th className="text-end">Invested</th>
              <th className="text-end">Open</th><th className="text-end">Close</th><th className="text-end">Fees</th><th className="text-end">Net</th>
              <th className="text-end">Stop</th><th className="text-end">Target</th>
            </tr></thead>
            <tbody>
              {rows.map((t) => (
                <tr key={t.positionId}>
                  <td className="small text-nowrap">{new Date(t.closedAt).toLocaleString()}</td>
                  <td className="fw-semibold" title={`position ${t.positionId}`}>{t.symbol || t.instrumentId}</td>
                  <td>{t.ours ? <span className="badge text-bg-success">cactusTrade</span> : <span className="badge text-bg-secondary">not opened by cactusTrade</span>}</td>
                  <td className="text-capitalize">{t.direction}</td>
                  <td className="text-end">{t.units}</td>
                  <td className="text-end">{usd(t.investedCents)}</td>
                  <td className="text-end">{t.openRate}</td>
                  <td className="text-end">{t.closeRate}</td>
                  <td className="text-end">{t.feesKnown ? usd(t.feesCents) : <Unknown />}</td>
                  <td className="text-end">{usd(t.netProfitCents)}</td>
                  <td className="text-end">{t.stopLoss ?? "—"}</td>
                  <td className="text-end">{t.takeProfit ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {more && last && (
        <button type="button" className="btn btn-sm btn-outline-secondary mt-3" onClick={() => load(last)}>
          <i className="bi bi-chevron-down me-1" />Older trades
        </button>
      )}
    </div></div>
  );
}

function Unknown() {
  return <span className="text-secondary" title="eToro did not report this trade's fees">unknown</span>;
}
