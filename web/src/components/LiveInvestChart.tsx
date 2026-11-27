import { useEffect, useMemo, useState } from "react";
import { api, usd } from "../lib/api";
import type { LiveSnapshot } from "../lib/live";
import { dateTime, dayDate, hourMinute } from "../lib/format";

// The dashboard's live graph of the account's investments (R39): what the open positions are
// worth (long and short apart), their open P&L, and equity, one point per live snapshot
// (every 5 seconds), seeded with today's stored equity snapshots. It only plots the server's
// figures; the points live in this tab (kept across a reload in sessionStorage).
// R43: a range — Day is the live points; Week, Month, Year and Max plot the stored equity
// snapshots (every 15 minutes, kept 400 days), where "invested" is equity minus cash and the
// open P&L is not stored.

export interface InvestPoint { at: string; long: number; short: number; pnl: number; equity: number }

type View = "invested" | "pnl" | "equity";
const VIEWS: { id: View; label: string; title: string }[] = [
  { id: "invested", label: "Invested", title: "What the open positions are worth now (amount + open P&L), long and short" },
  { id: "pnl", label: "Open P&L", title: "The open positions' unrealised profit or loss" },
  { id: "equity", label: "Equity", title: "The account's equity" },
];
type Range = "day" | "week" | "month" | "year" | "max";
// days of stored history per range (the server keeps 400 days)
export const RANGES: { id: Range; label: string; days: number }[] = [
  { id: "day", label: "Day", days: 1 },
  { id: "week", label: "Week", days: 7 },
  { id: "month", label: "Month", days: 30 },
  { id: "year", label: "Year", days: 365 },
  { id: "max", label: "Max", days: 400 },
];
const MAX_DRAWN = 600; // points drawn per line; longer series are thinned evenly
const CAP = 2000; // points kept (about 2.8 hours at one every 5 seconds, plus the seed)

// pointOf turns a snapshot into a point (null without figures).
export function pointOf(s: LiveSnapshot | null): InvestPoint | null {
  if (!s?.figures) return null;
  let long = 0, short = 0, pnl = 0;
  for (const p of s.positions) {
    const worth = p.amountCents + p.unrealisedPnlCents;
    if (p.long) long += worth; else short += worth;
    pnl += p.unrealisedPnlCents;
  }
  return { at: s.figures.asOf, long, short, pnl, equity: s.figures.equityCents };
}

// addPoint appends p unless the list already has its time; oldest first, at most cap.
export function addPoint(list: InvestPoint[], p: InvestPoint, cap = CAP): InvestPoint[] {
  if (list.some((x) => x.at === p.at)) return list;
  const out = [...list, p].sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
  return out.length > cap ? out.slice(out.length - cap) : out;
}

// thin keeps at most max points, evenly spread, always with the first and the last.
export function thin<T>(pts: T[], max = MAX_DRAWN): T[] {
  if (pts.length <= max) return pts;
  const step = (pts.length - 1) / (max - 1);
  return Array.from({ length: max }, (_, i) => pts[Math.round(i * step)]);
}

interface HistPoint { t: number; invested: number; equity: number }

function load(key: string): InvestPoint[] {
  try {
    const v = JSON.parse(sessionStorage.getItem(key) ?? "[]");
    return Array.isArray(v) ? v : [];
  } catch {
    return [];
  }
}

export function LiveInvestChart({ accountId, snapshot }: { accountId: string; snapshot: LiveSnapshot | null }) {
  const key = `cactustrade.invest.${accountId}`;
  const [points, setPoints] = useState<InvestPoint[]>(() => load(key));
  const [view, setView] = useState<View>("invested");
  const [range, setRange] = useState<Range>("day");
  const [hist, setHist] = useState<HistPoint[] | null>(null);
  const days = RANGES.find((r) => r.id === range)!.days;
  useEffect(() => {
    if (range === "day") return;
    let live = true;
    setHist(null);
    api.equityHistory(days).then((ss) => {
      const mine = ss.find((s) => s.accountId === accountId);
      if (live) setHist((mine?.points ?? []).map((q) => ({ t: Date.parse(q.at), invested: q.equity - q.cash, equity: q.equity })));
    }).catch(() => { if (live) setHist([]); });
    return () => { live = false; };
  }, [accountId, range, days]);
  const shownView: View = range !== "day" && view === "pnl" ? "invested" : view;
  // today's stored equity snapshots, so the equity line does not start empty
  useEffect(() => {
    api.equityHistory(1).then((ss) => {
      const mine = ss.find((s) => s.accountId === accountId);
      if (!mine) return;
      setPoints((cur) => mine.points.reduce<InvestPoint[]>((l, q) =>
        l.some((x) => x.at === q.at) ? l : addPoint(l, { at: q.at, long: NaN, short: NaN, pnl: NaN, equity: q.equity }), cur));
    }).catch(() => {});
  }, [accountId]);
  useEffect(() => {
    const p = pointOf(snapshot);
    if (p) setPoints((cur) => addPoint(cur, p));
  }, [snapshot]);
  useEffect(() => {
    try { sessionStorage.setItem(key, JSON.stringify(points.slice(-CAP))); } catch { /* storage off: fine */ }
  }, [key, points]);

  const series = useMemo(() => {
    if (range !== "day") {
      const h = thin(hist ?? []);
      if (shownView === "equity") return [{ name: "Equity", cls: "chart-line-1", pts: h.map((p) => ({ t: p.t, v: p.equity })) }];
      return [{ name: "Invested", cls: "chart-line-1", pts: h.map((p) => ({ t: p.t, v: p.invested })) }];
    }
    const pick = (f: (p: InvestPoint) => number) => points.filter((p) => Number.isFinite(f(p))).map((p) => ({ t: Date.parse(p.at), v: f(p) }));
    if (shownView === "invested") return [
      { name: "Long", cls: "chart-line-1", pts: pick((p) => p.long) },
      { name: "Short", cls: "chart-line-2", pts: pick((p) => p.short) },
    ];
    if (shownView === "pnl") return [{ name: "Open P&L", cls: "chart-line-3", pts: pick((p) => p.pnl) }];
    return [{ name: "Equity", cls: "chart-line-1", pts: pick((p) => p.equity) }];
  }, [points, shownView, range, hist]);
  const last = pointOf(snapshot);

  return (
    <div className="card mb-4"><div className="card-body">
      <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
        <h2 className="h5 mb-0 me-auto"><i className="bi bi-graph-up me-2" aria-hidden="true" />My investments, live</h2>
        <div className="btn-group btn-group-sm" role="group" aria-label="How far back">
          {RANGES.map((r) => (
            <button key={r.id} type="button" className={"btn " + (range === r.id ? "btn-secondary" : "btn-outline-secondary")}
              aria-pressed={range === r.id} onClick={() => setRange(r.id)}>{r.label}</button>
          ))}
        </div>
        <div className="btn-group btn-group-sm" role="group" aria-label="What the graph shows">
          {VIEWS.map((v) => {
            const off = v.id === "pnl" && range !== "day";
            return (
              <button key={v.id} type="button" className={"btn " + (shownView === v.id ? "btn-success" : "btn-outline-success")}
                title={off ? "The open P&L is not stored; it is shown for the day only" : v.title} disabled={off}
                aria-pressed={shownView === v.id} onClick={() => setView(v.id)}>{v.label}</button>
            );
          })}
        </div>
      </div>
      {last && (
        <p className="small mb-2" aria-label="Now">
          <span className="me-3"><span className="legend-dot chart-bg-1" /> Long {usd(last.long)}</span>
          <span className="me-3"><span className="legend-dot chart-bg-2" /> Short {usd(last.short)}</span>
          <span className={"me-3 " + (last.pnl < 0 ? "text-danger" : "text-success")}>Open P&L {usd(last.pnl)}</span>
          <span className="text-secondary">Equity {usd(last.equity)}</span>
        </p>
      )}
      {range !== "day" && hist === null ? <p className="text-secondary small mb-0">Loading…</p> : <LineChart series={series} range={range} />}
      <p className="small text-secondary mb-0 mt-1">
        {range === "day"
          ? "A point every 5 seconds while this page is open; equity starts from today's stored snapshots."
          : "From the snapshots stored every 15 minutes (kept 400 days); invested is equity minus cash."}
      </p>
    </div></div>
  );
}

const W = 760, H = 200, PAD = { l: 84, r: 12, t: 10, b: 24 };

function LineChart({ series, range }: { series: { name: string; cls: string; pts: { t: number; v: number }[] }[]; range: Range }) {
  const all = series.flatMap((s) => s.pts);
  if (!series.some((s) => s.pts.length > 1)) {
    return <p className="text-secondary small mb-0">{range === "day" ? "The graph fills in as the live figures arrive." : "No stored snapshots in this range yet."}</p>;
  }
  const when = range === "day" ? hourMinute : range === "week" ? dateTime : dayDate;
  const t0 = Math.min(...all.map((p) => p.t)), t1 = Math.max(...all.map((p) => p.t));
  let v0 = Math.min(...all.map((p) => p.v)), v1 = Math.max(...all.map((p) => p.v));
  if (v0 === v1) { v0 -= 100; v1 += 100; }
  const x = (t: number) => PAD.l + ((t - t0) / Math.max(1, t1 - t0)) * (W - PAD.l - PAD.r);
  const y = (v: number) => PAD.t + (1 - (v - v0) / (v1 - v0)) * (H - PAD.t - PAD.b);
  const iso = (t: number) => new Date(t).toISOString();
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="w-100" role="img" aria-label={(range === "day" ? "Live graph: " : "Graph: ") + series.map((s) => s.name).join(", ")}>
      {[v0, (v0 + v1) / 2, v1].map((v) => (
        <g key={v}>
          <line x1={PAD.l} x2={W - PAD.r} y1={y(v)} y2={y(v)} className="chart-grid" />
          <text x={PAD.l - 6} y={y(v) + 4} textAnchor="end" className="chart-axis">{usd(Math.round(v))}</text>
        </g>
      ))}
      <text x={PAD.l} y={H - 6} className="chart-axis">{when(iso(t0))}</text>
      <text x={W - PAD.r} y={H - 6} textAnchor="end" className="chart-axis">{when(iso(t1))}</text>
      {series.map((s) => s.pts.length > 1 && (
        <polyline key={s.name} fill="none" className={s.cls} strokeWidth={2}
          points={s.pts.map((p) => `${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join(" ")} />
      ))}
    </svg>
  );
}
