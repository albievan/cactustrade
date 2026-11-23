import type { EquitySeries } from "../lib/api";
import { money } from "../lib/api";

// An inline SVG line chart of equity over time: one line per account. Accounts in different
// currencies get separate charts (amounts in different currencies are never put on one
// axis). Colours come from CSS variables, so light and dark themes both work.

const W = 760;
const H = 240;
const PAD = { l: 84, r: 16, t: 12, b: 30 };
const COLOURS = 6;

interface Props { series: EquitySeries[]; days: number }

export function EquityCharts({ series, days }: Props) {
  const withPoints = series.filter((s) => s.points.length > 0);
  if (withPoints.length === 0) {
    return <p className="text-secondary mb-0">The chart fills in as snapshots are recorded every 15 minutes.</p>;
  }
  const byCurrency = new Map<string, EquitySeries[]>();
  for (const s of withPoints) byCurrency.set(s.currency, [...(byCurrency.get(s.currency) ?? []), s]);
  // a stable colour per account across charts
  const colour = new Map(withPoints.map((s, i) => [s.accountId, `var(--chart-${(i % COLOURS) + 1})`]));
  return (
    <>
      {[...byCurrency.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([cur, ss]) => (
        <Chart key={cur} currency={cur} series={ss} colour={colour} days={days} />
      ))}
    </>
  );
}

function Chart({ currency, series, colour, days }: { currency: string; series: EquitySeries[]; colour: Map<string, string>; days: number }) {
  const pts = series.flatMap((s) => s.points);
  const ts = pts.map((p) => Date.parse(p.at));
  let t0 = Math.min(...ts);
  let t1 = Math.max(...ts);
  if (t0 === t1) { t0 -= 30 * 60_000; t1 += 30 * 60_000; }
  let lo = Math.min(...pts.map((p) => p.equity));
  let hi = Math.max(...pts.map((p) => p.equity));
  const pad = Math.max(Math.round((hi - lo) * 0.08), 100);
  lo -= pad; hi += pad;
  const x = (t: number) => PAD.l + ((t - t0) / (t1 - t0)) * (W - PAD.l - PAD.r);
  const y = (v: number) => PAD.t + (1 - (v - lo) / (hi - lo)) * (H - PAD.t - PAD.b);
  const yTicks = [0, 1, 2, 3, 4].map((i) => lo + ((hi - lo) * i) / 4);
  const xTicks = [0, 1, 2, 3, 4].map((i) => t0 + ((t1 - t0) * i) / 4);
  const fmtT = (t: number) => {
    const d = new Date(t);
    return days <= 1 || t1 - t0 <= 36 * 3600_000
      ? d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
      : d.toLocaleDateString(undefined, { day: "numeric", month: "short" });
  };
  return (
    <figure className="equity-chart mb-3">
      <figcaption className="small fw-semibold mb-1">Equity in {currency}</figcaption>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Equity in ${currency} over time`} preserveAspectRatio="none">
        {yTicks.map((v) => (
          <g key={`y${v}`}>
            <line x1={PAD.l} x2={W - PAD.r} y1={y(v)} y2={y(v)} className="grid" />
            <text x={PAD.l - 6} y={y(v) + 4} textAnchor="end" className="axis">{money(Math.round(v), currency)}</text>
          </g>
        ))}
        {xTicks.map((t) => (
          <text key={`x${t}`} x={x(t)} y={H - 8} textAnchor="middle" className="axis">{fmtT(t)}</text>
        ))}
        {series.map((s) => {
          const c = colour.get(s.accountId);
          if (s.points.length === 1) {
            const p = s.points[0];
            return <circle key={s.accountId} cx={x(Date.parse(p.at))} cy={y(p.equity)} r={3.5} fill={c} />;
          }
          const d = s.points.map((p, i) => `${i === 0 ? "M" : "L"}${x(Date.parse(p.at)).toFixed(1)},${y(p.equity).toFixed(1)}`).join(" ");
          return <path key={s.accountId} d={d} fill="none" stroke={c} strokeWidth={2} vectorEffect="non-scaling-stroke" />;
        })}
      </svg>
      <div className="d-flex flex-wrap gap-3 small">
        {series.map((s) => (
          <span key={s.accountId}><span className="legend-dot" style={{ background: colour.get(s.accountId) }} />{s.name}</span>
        ))}
      </div>
    </figure>
  );
}
