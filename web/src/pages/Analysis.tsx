import { useEffect, useMemo, useState } from "react";
import { EquityCharts } from "../components/EquityChart";
import { AnalysisDrill, TILE_TITLES, type Tile } from "../components/AnalysisDrill";
import { InfoCard } from "../components/InfoCard";
import { ProblemAlert } from "../components/ProblemAlert";
import {
  analysisPdfUrl, api, money, usd,
  type Account, type Analysis as AnalysisData, type AnalysisGroup, type AnalysisGrouping, type AnalysisPreset, type AnalysisQuery, type EquitySeries,
} from "../lib/api";

// The analysis page (R31): every trade of a period on the accounts you can see (or the ones
// you choose), grouped by date, exchange, account, theme, direction, playbook and outcome,
// with charts, a narrative the server writes from the figures, and a one-page PDF. Every
// figure comes from the server; this page only formats and sorts it, in neutral words.

export const PAST_ONLY = "Figures describe past results only; they do not predict future results.";

const PRESETS: { id: AnalysisPreset; label: string; title: string }[] = [
  { id: "today", label: "Today", title: "Today's risk day (from 22:00 UTC yesterday)" },
  { id: "week", label: "This week", title: "Monday to Friday of this week" },
  { id: "month", label: "This month", title: "This calendar month" },
  { id: "last_month", label: "Last month", title: "The previous calendar month" },
  { id: "all", label: "Everything", title: "The last 400 risk days" },
  { id: "custom", label: "Custom", title: "Choose the first and the last risk day" },
];

export function Analysis() {
  const [preset, setPreset] = useState<AnalysisPreset>("month");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [range, setRange] = useState<{ from: string; to: string } | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [chosen, setChosen] = useState<string[]>([]);
  const [data, setData] = useState<AnalysisData | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => { api.accounts().then(setAccounts).catch(() => setAccounts([])); }, []);

  const query: AnalysisQuery | null = useMemo(() => {
    if (preset === "custom") return range ? { preset, from: range.from, to: range.to, accountIds: chosen } : null;
    return { preset, accountIds: chosen };
  }, [preset, range, chosen]);

  useEffect(() => {
    if (!query) return;
    let live = true;
    setLoading(true);
    setError(null);
    api.analysis(query)
      .then((a) => { if (live) setData(a); })
      .catch((e) => { if (live) { setError(e); setData(null); } })
      .finally(() => { if (live) setLoading(false); });
    return () => { live = false; };
  }, [query]);

  const toggle = (id: string) => setChosen((c) => (c.includes(id) ? c.filter((x) => x !== id) : [...c, id]));

  return (
    <>
      <div className="d-flex flex-wrap align-items-center gap-2 mb-3">
        <h1 className="h3 mb-0 flex-grow-1">Analysis</h1>
        <div className="btn-group btn-group-sm flex-wrap" role="group" aria-label="Period">
          {PRESETS.map((p) => (
            <button key={p.id} type="button" className={"btn " + (preset === p.id ? "btn-success" : "btn-outline-secondary")}
              aria-pressed={preset === p.id} title={p.title} onClick={() => setPreset(p.id)}>{p.label}</button>
          ))}
        </div>
        {query && (
          <a className="btn btn-sm btn-outline-secondary" href={analysisPdfUrl(query)} download
            title="Export this analysis as a one-page PDF" aria-label="Export PDF">
            <i className="bi bi-file-earmark-pdf" />
          </a>
        )}
      </div>
      {preset === "custom" && (
        <form className="d-flex flex-wrap align-items-end gap-2 mb-3" aria-label="Custom range"
          onSubmit={(e) => { e.preventDefault(); if (from && to) setRange({ from, to }); }}>
          <label className="small">First risk day<input type="date" className="form-control form-control-sm" value={from}
            onChange={(e) => setFrom(e.target.value)} aria-label="First risk day" /></label>
          <label className="small">Last risk day<input type="date" className="form-control form-control-sm" value={to}
            onChange={(e) => setTo(e.target.value)} aria-label="Last risk day (inclusive)" /></label>
          <button type="submit" className="btn btn-sm btn-success" disabled={!from || !to} title="Show this range" aria-label="Show this range">
            <i className="bi bi-arrow-right" />
          </button>
          {!range && <span className="small text-secondary">Choose both days (at most 400 days back).</span>}
        </form>
      )}
      {accounts.length > 1 && (
        <div className="d-flex flex-wrap align-items-center gap-1 mb-3" role="group" aria-label="Accounts">
          <span className="small text-secondary me-1">Accounts:</span>
          <button type="button" className={"btn btn-sm " + (chosen.length === 0 ? "btn-success" : "btn-outline-secondary")}
            aria-pressed={chosen.length === 0} onClick={() => setChosen([])}>All</button>
          {accounts.map((a) => (
            <button key={a.id} type="button" className={"btn btn-sm " + (chosen.includes(a.id) ? "btn-success" : "btn-outline-secondary")}
              aria-pressed={chosen.includes(a.id)} onClick={() => toggle(a.id)}>{a.name}</button>
          ))}
        </div>
      )}
      <div className="alert alert-secondary py-2 small" role="note"><i className="bi bi-info-circle me-1" />{PAST_ONLY}</div>
      <InfoCard id="analysis" title="How these figures are computed">
        <ul className="small mb-0">
          <li>Trades are eToro's closed trades of positions <strong>cactusTrade opened</strong>, closed in the period. Positions
            opened outside cactusTrade are counted apart and left out of the figures.</li>
          <li><strong>Realised P&amp;L</strong> is eToro's net result per trade, after the fees eToro reported. When eToro did not
            report a trade's fees they are shown as <em>unknown</em>, never as 0.</li>
          <li>Trade figures are in US dollars. Equity is shown <strong>per currency</strong>: amounts in different currencies are
            never added together (there is no currency conversion). Equity includes deposits and withdrawals.</li>
          <li>Periods are whole risk days from 22:00 UTC. Bars by date are per day up to 31 days, per week up to about six
            months, per month beyond.</li>
          <li>A trade's theme is its share's theme today ("No theme" when it has none); its playbook comes from the decision
            that opened it ("Not recorded" when there is none).</li>
          <li>The summary is written by cactusTrade from these figures (no AI); it reports, it does not predict.</li>
        </ul>
      </InfoCard>
      <ProblemAlert error={error} />
      {loading && !data && <p className="text-secondary" role="status">Loading…</p>}
      {data && <Report a={data} />}
    </>
  );
}

function Report({ a }: { a: AnalysisData }) {
  const t = a.totals;
  const none = t.trades === 0;
  const [drill, setDrill] = useState<Tile | null>(null);
  return (
    <>
      <p className="text-secondary small mb-2">
        <strong>{a.title}</strong> · {a.days} risk {a.days === 1 ? "day" : "days"} · {a.scope}
      </p>
      {none && <div className="alert alert-light border" role="status">No closed trades in this period.</div>}
      <Kpis a={a} active={drill} onDrill={(k) => setDrill((d) => (d === k ? null : k))} />
      {drill && (
        <AnalysisDrill a={a} tile={drill} onClose={() => setDrill(null)} equity={
          <>
            {a.curves.length > 0 && <EquityCharts series={curves(a)} days={a.days} drawdown />}
            <EquityTable a={a} />
            {a.curves.length === 0 && a.equity.length === 0 && <p className="small text-secondary mb-0">No equity snapshots in this period.</p>}
          </>
        } />
      )}
      <div className="card mb-4"><div className="card-body">
        <h2 className="h5">What happened</h2>
        {a.narrative.map((s) => (
          <section key={s.heading} className="mb-2">
            <h3 className="h6 mb-1">{s.heading}</h3>
            {s.paragraphs.map((p, i) => <p key={i} className="small mb-1">{p}</p>)}
          </section>
        ))}
      </div></div>
      <div className="row g-3 mb-4">
        <div className="col-xl-6"><div className="card h-100"><div className="card-body">
          <h2 className="h5">Realised P&amp;L by {a.bucket}</h2>
          <DateBars groups={grouping(a, "date")} />
        </div></div></div>
        <div className="col-xl-6"><div className="card h-100"><div className="card-body">
          <h2 className="h5">Equity</h2>
          <EquityCharts series={curves(a)} days={a.days} drawdown />
        </div></div></div>
        {(["exchange", "account", "theme", "playbook"] as const).map((d) => (
          <div key={d} className="col-md-6 col-xl-3"><div className="card h-100"><div className="card-body">
            <h2 className="h6">Realised P&amp;L {titleOf(a, d).toLowerCase()}</h2>
            <HBars groups={grouping(a, d)} label={`Realised P&L ${titleOf(a, d).toLowerCase()}`} />
          </div></div></div>
        ))}
        <div className="col-xl-6"><div className="card h-100"><div className="card-body">
          <h2 className="h6">Wins, losses and breakeven</h2>
          <Split a={a} />
        </div></div></div>
        <div className="col-xl-6"><div className="card h-100"><div className="card-body">
          <h2 className="h6">Risk &amp; discipline, and activity</h2>
          <RiskActivity a={a} />
        </div></div></div>
      </div>
      {a.groupings.map((g) => <GroupTable key={g.dimension} g={g} bucket={a.bucket} />)}
      <EquityTable a={a} />
      <div className="small text-secondary mb-4">
        <strong>Notes</strong>
        <ul className="mb-0">{a.notes.map((n) => <li key={n}>{n}</li>)}</ul>
      </div>
    </>
  );
}

function grouping(a: AnalysisData, d: AnalysisGrouping["dimension"]): AnalysisGroup[] {
  return a.groupings.find((g) => g.dimension === d)?.groups ?? [];
}

function titleOf(a: AnalysisData, d: AnalysisGrouping["dimension"]): string {
  return a.groupings.find((g) => g.dimension === d)?.title ?? d;
}

// curves turns the per-currency equity curves into the series the equity chart draws (one
// line per currency, so currencies never share an axis).
function curves(a: AnalysisData): EquitySeries[] {
  return a.curves.map((c) => ({
    accountId: c.currency, name: `${c.currency} · ${c.accounts} ${c.accounts === 1 ? "account" : "accounts"}`, platform: "etoro", mode: "demo",
    currency: c.currency, points: c.points.map((p) => ({ at: p.at, cash: 0, equity: p.equity })),
  }));
}

function bp(v: number | undefined): string {
  return v === undefined ? "—" : `${(v / 100).toFixed(2)} %`;
}

function Kpis({ a, active, onDrill }: { a: AnalysisData; active: Tile | null; onDrill: (t: Tile) => void }) {
  const t = a.totals;
  const none = t.trades === 0;
  const dd = a.curves.length === 0 ? "not recorded" : bp(Math.max(...a.curves.map((c) => c.maxDrawdownBp)));
  const cost = a.llm.costUnknownCalls > 0 ? `$${a.llm.knownCostUsd} + ${a.llm.costUnknownCalls} unknown` : `$${a.llm.knownCostUsd}`;
  return (
    <div className="row g-3 mb-4">
      <Kpi tile="trades" active={active} onDrill={onDrill} label="Closed trades" icon="bi-list-check" value={String(t.trades)}
        sub={a.foreignTrades > 0 ? `${a.foreignTrades} more not opened by cactusTrade (not counted)` : undefined} />
      <Kpi tile="winrate" active={active} onDrill={onDrill} label="Win rate" icon="bi-percent" value={bp(t.winRateBp)} sub={none ? "no closed trades" : `${t.wins} wins / ${t.losses} losses / ${t.breakeven} breakeven`} />
      <Kpi tile="pnl" active={active} onDrill={onDrill} label="Realised P&L (after fees)" icon="bi-cash-stack" value={none ? "—" : usd(t.netCents)} sub={none ? undefined : `before reported fees ${usd(t.grossCents)}`} tone={t.netCents} />
      <Kpi tile="average" active={active} onDrill={onDrill} label="Average per trade" icon="bi-calculator" value={usd(t.avgNetCents)} tone={t.avgNetCents} />
      <Kpi tile="fees" active={active} onDrill={onDrill} label="Fees reported by eToro" icon="bi-receipt" value={usd(t.feesCents)}
        sub={t.feesUnknownTrades > 0 ? `unknown for ${t.feesUnknownTrades} ${t.feesUnknownTrades === 1 ? "trade" : "trades"}` : undefined} />
      <Kpi tile="drawdown" active={active} onDrill={onDrill} label="Largest drawdown" icon="bi-graph-down" value={dd}
        sub={a.curves.map((c) => `${c.currency} ${money(c.maxDrawdownMinor, c.currency)}`).join(" · ") || "no equity snapshots"} />
      <Kpi tile="ours" active={active} onDrill={onDrill} label="Opened by cactusTrade" icon="bi-robot" value={bp(a.oursShareBp)} sub="share of all closed trades" />
      <Kpi tile="llm" active={active} onDrill={onDrill} label="LLM reviewer cost" icon="bi-cpu" value={cost} sub={`${a.llm.calls} calls`} />
    </div>
  );
}

// Kpi is one tile; clicking it (or Enter / Space) opens its details below the tiles (R35).
function Kpi({ tile, active, onDrill, label, icon, value, sub, tone }: {
  tile: Tile; active: Tile | null; onDrill: (t: Tile) => void; label: string; icon: string; value: string; sub?: string; tone?: number;
}) {
  const cls = tone === undefined || tone === 0 ? "" : tone > 0 ? " pnl-up" : " pnl-down";
  const on = active === tile;
  return (
    <div className="col-6 col-lg-3">
      <button type="button" className={"card stat h-100 w-100 text-start kpi-drill" + (on ? " border-primary" : "")} aria-expanded={on}
        title={on ? "Hide the details" : `Show the details behind “${TILE_TITLES[tile]}”`} onClick={() => onDrill(tile)}>
        <div className="card-body w-100">
          <div className="text-secondary small d-flex"><span className="flex-grow-1"><i className={"bi me-1 " + icon} />{label}</span>
            <i className={"bi " + (on ? "bi-chevron-up" : "bi-zoom-in")} aria-hidden="true" /></div>
          <div className={"value" + cls}>{value}</div>
          {sub && <div className="small text-secondary">{sub}</div>}
        </div>
      </button>
    </div>
  );
}

// DateBars draws each date bucket's realised P&L as a bar above or below zero.
function DateBars({ groups }: { groups: AnalysisGroup[] }) {
  if (groups.every((g) => g.trades === 0)) return <p className="text-secondary mb-0">No closed trades in this period.</p>;
  const W = 600;
  const H = 160;
  const most = Math.max(1, ...groups.map((g) => Math.abs(g.netCents)));
  const mid = H / 2;
  const step = W / groups.length;
  return (
    <figure className="analysis-bars mb-0">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Realised P&L per period, bars above zero for gains and below for losses" preserveAspectRatio="none">
        <line x1={0} x2={W} y1={mid} y2={mid} className="zero" />
        {groups.map((g, i) => {
          const h = (Math.abs(g.netCents) / most) * (mid - 4);
          return (
            <rect key={g.key} x={i * step + step * 0.15} width={step * 0.7} y={g.netCents >= 0 ? mid - h : mid} height={h}
              className={g.netCents >= 0 ? "up" : "down"}>
              <title>{`${g.label}: ${usd(g.netCents)} (${g.trades} trades)`}</title>
            </rect>
          );
        })}
      </svg>
      <figcaption className="d-flex justify-content-between small text-secondary">
        <span>{groups[0]?.label}</span><span>{groups[groups.length - 1]?.label}</span>
      </figcaption>
    </figure>
  );
}

// HBars lists the groups with a bar for their realised P&L and the figure beside it.
function HBars({ groups, label }: { groups: AnalysisGroup[]; label: string }) {
  if (groups.length === 0) return <p className="text-secondary small mb-0">No trades.</p>;
  const most = Math.max(1, ...groups.map((g) => Math.abs(g.netCents)));
  return (
    <ul className="list-unstyled small mb-0 analysis-hbars" aria-label={label}>
      {groups.slice(0, 12).map((g) => {
        const pct = (Math.abs(g.netCents) / most) * 50;
        return (
          <li key={g.key} className="mb-1">
            <div className="d-flex justify-content-between gap-2">
              <span className="text-truncate" title={g.label}>{g.label} <span className="text-secondary">({g.trades})</span></span>
              <span className={g.netCents > 0 ? "pnl-up" : g.netCents < 0 ? "pnl-down" : ""}>{usd(g.netCents)}</span>
            </div>
            <div className="hbar-track" aria-hidden="true">
              <div className={"hbar " + (g.netCents >= 0 ? "up" : "down")}
                style={g.netCents >= 0 ? { left: "50%", width: `${pct}%` } : { right: "50%", width: `${pct}%` }} />
            </div>
          </li>
        );
      })}
      {groups.length > 12 && <li className="text-secondary">{groups.length - 12} more in the table below</li>}
    </ul>
  );
}

// Split draws wins, losses and breakeven as one stacked bar with a legend.
function Split({ a }: { a: AnalysisData }) {
  const t = a.totals;
  if (t.trades === 0) return <p className="text-secondary small mb-0">No trades.</p>;
  const parts = [
    { label: "Wins", n: t.wins, cls: "up" }, { label: "Losses", n: t.losses, cls: "down" }, { label: "Breakeven", n: t.breakeven, cls: "even" },
  ];
  return (
    <>
      <div className="split-bar mb-2" role="img" aria-label={`${t.wins} wins, ${t.losses} losses and ${t.breakeven} breakeven of ${t.trades} trades`}>
        {parts.filter((p) => p.n > 0).map((p) => <div key={p.label} className={p.cls} style={{ width: `${(p.n / t.trades) * 100}%` }} />)}
      </div>
      <div className="d-flex flex-wrap gap-3 small">
        {parts.map((p) => <span key={p.label}><span className={"legend-dot " + p.cls} />{p.label}: {p.n} of {t.trades}</span>)}
        <span className="text-secondary">win rate {bp(t.winRateBp)}</span>
      </div>
    </>
  );
}

function RiskActivity({ a }: { a: AnalysisData }) {
  const r = a.risk;
  const rows: [string, string | number][] = [
    ["Kill switch used / reset", `${r.killSwitches} / ${r.killResets}`],
    ["Daily loss limit stopped new entries", r.dailyStops],
    ["Risk limits changed", r.limitChanges],
    ["Force-closed", r.forceCloses],
    ["Candidates refused by the risk engine", r.riskRefusals],
    ["Decisions logged", a.decisionsTotal],
    ...a.decisions.map((d): [string, number] => [`· ${d.label}`, d.n]),
    ...a.verdicts.map((v): [string, number] => [`· Reviewer: ${v.label}`, v.n]),
    ["LLM calls (failed)", `${a.llm.calls} (${a.llm.failed})`],
    ["Closed trades not opened by cactusTrade", a.foreignTrades],
  ];
  return (
    <table className="table table-sm mb-0">
      <tbody>{rows.map(([k, v]) => <tr key={k}><td>{k}</td><td className="text-end">{v}</td></tr>)}</tbody>
    </table>
  );
}

type SortKey = "label" | "trades" | "winRateBp" | "netCents" | "feesCents" | "avgNetCents";

const COLS: { key: SortKey; label: string; end?: boolean }[] = [
  { key: "label", label: "Group" }, { key: "trades", label: "Trades", end: true }, { key: "winRateBp", label: "Win rate", end: true },
  { key: "netCents", label: "Realised P&L", end: true }, { key: "feesCents", label: "Fees", end: true }, { key: "avgNetCents", label: "Avg / trade", end: true },
];

// GroupTable is one grouping as a table sortable by any figure column (click the header).
function GroupTable({ g, bucket }: { g: AnalysisGrouping; bucket: string }) {
  const [sort, setSort] = useState<{ key: SortKey | null; desc: boolean }>({ key: null, desc: false });
  const rows = useMemo(() => {
    const shown = g.dimension === "date" ? g.groups.filter((x) => x.trades > 0) : [...g.groups];
    if (!sort.key) return shown;
    const k = sort.key;
    return [...shown].sort((x, y) => {
      const a = x[k] ?? -Infinity;
      const b = y[k] ?? -Infinity;
      const c = typeof a === "string" && typeof b === "string" ? a.localeCompare(b) : Number(a) - Number(b);
      return sort.desc ? -c : c;
    });
  }, [g, sort]);
  const click = (key: SortKey) => setSort((s) => ({ key, desc: s.key === key ? !s.desc : key !== "label" }));
  return (
    <div className="card mb-3"><div className="card-body">
      <h2 className="h6">{g.title}{g.dimension === "date" ? ` (${bucket}s with trades)` : ""}</h2>
      {rows.length === 0 ? <p className="text-secondary small mb-0">No trades.</p> : (
        <div className="table-responsive">
          <table className="table table-sm align-middle mb-0">
            <thead><tr>
              {COLS.map((c) => (
                <th key={c.key} className={c.end ? "text-end" : ""} aria-sort={sort.key === c.key ? (sort.desc ? "descending" : "ascending") : "none"}>
                  <button type="button" className="btn btn-link btn-sm p-0 text-reset fw-semibold text-decoration-none" onClick={() => click(c.key)}
                    title={`Sort by ${c.label.toLowerCase()}`}>
                    {c.label}{sort.key === c.key && <i className={"bi ms-1 " + (sort.desc ? "bi-sort-down" : "bi-sort-up")} />}
                  </button>
                </th>
              ))}
              <th className="text-end">W / L / B</th><th className="text-end">Best</th><th className="text-end">Worst</th>
            </tr></thead>
            <tbody>
              {rows.map((x) => (
                <tr key={x.key}>
                  <td>{x.label}</td>
                  <td className="text-end">{x.trades}</td>
                  <td className="text-end">{bp(x.winRateBp)}</td>
                  <td className={"text-end " + (x.netCents > 0 ? "pnl-up" : x.netCents < 0 ? "pnl-down" : "")}>{usd(x.netCents)}</td>
                  <td className="text-end">{usd(x.feesCents)}{x.feesUnknownTrades > 0 && <span className="text-secondary" title="eToro did not report these trades' fees"> + {x.feesUnknownTrades} unknown</span>}</td>
                  <td className="text-end">{usd(x.avgNetCents)}</td>
                  <td className="text-end">{x.wins} / {x.losses} / {x.breakeven}</td>
                  <td className="text-end" title={x.best ? `${x.best.symbol || "share not cached"} on ${x.best.account}` : undefined}>{usd(x.best?.netCents)}</td>
                  <td className="text-end" title={x.worst ? `${x.worst.symbol || "share not cached"} on ${x.worst.account}` : undefined}>{usd(x.worst?.netCents)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div></div>
  );
}

function EquityTable({ a }: { a: AnalysisData }) {
  if (a.equity.length === 0) return null;
  return (
    <div className="card mb-3"><div className="card-body">
      <h2 className="h6">Recorded equity (each in its own currency; currencies are never added together)</h2>
      <div className="table-responsive">
        <table className="table table-sm align-middle mb-0">
          <thead><tr>
            <th>Account</th><th>Currency</th><th className="text-end">First recorded</th><th className="text-end">Last recorded</th>
            <th className="text-end">Change</th><th className="text-end">Max drawdown</th>
          </tr></thead>
          <tbody>
            {a.equity.map((e) => (
              <tr key={e.account}>
                <td>{e.account}{e.viewOnly && <span className="badge text-bg-secondary ms-1">view-only</span>}</td>
                <td>{e.currency || "—"}</td>
                {e.known ? (
                  <>
                    <td className="text-end">{money(e.openingMinor, e.currency)}</td>
                    <td className="text-end">{money(e.closingMinor, e.currency)}</td>
                    <td className="text-end">{money(e.changeMinor, e.currency)}</td>
                    <td className="text-end">{money(e.maxDrawdownMinor, e.currency)} ({bp(e.maxDrawdownBp)})</td>
                  </>
                ) : <td colSpan={4} className="text-secondary">not recorded in the period</td>}
              </tr>
            ))}
            {a.equityTotals.map((t) => (
              <tr key={t.currency} className="fw-semibold">
                <td>Total {t.currency} ({t.accounts} {t.accounts === 1 ? "account" : "accounts"})</td><td>{t.currency}</td>
                <td className="text-end">{money(t.openingMinor, t.currency)}</td><td className="text-end">{money(t.closingMinor, t.currency)}</td>
                <td className="text-end">{money(t.changeMinor, t.currency)}</td><td />
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div></div>
  );
}
