import { dateTime, hourMinute } from "../lib/format";
import { useState, type ReactNode } from "react";
import { usd, type Order } from "../lib/api";
import type { Live, LiveFigures, LiveRegime, LiveSnapshot } from "../lib/live";
import type { RegimeText } from "../lib/api";
import { FixButton } from "./FixButton";
import { InfoCard } from "./InfoCard";
import { DirectionBadge } from "./DirectionBadge";

// The live dashboard's cards (PLAN 6.11). Every figure comes from the server's snapshot
// (GET /api/v1/live); these components only format it. The only arithmetic here is the
// width of a bar (a part of a whole, clamped to 0–100 %) and the time since the last update.

const marketName: Record<string, string> = { LSE: "London", NYSE: "New York", ASX: "Sydney" };

const orderBadge: Record<string, string> = {
  created: "text-bg-light", sent: "text-bg-info", pending: "text-bg-info", filled: "text-bg-success",
  partially_filled: "text-bg-success", rejected: "text-bg-danger", cancelled: "text-bg-secondary",
  unknown: "text-bg-warning", abandoned: "text-bg-secondary",
};

const connectionText: Record<Live["connection"], { label: string; cls: string; title: string }> = {
  connecting: { label: "connecting", cls: "text-bg-secondary", title: "Opening the live stream" },
  live: { label: "live", cls: "text-bg-success", title: "Live: updates arrive as they happen" },
  reconnecting: { label: "reconnecting", cls: "text-bg-warning", title: "The live stream dropped; reconnecting" },
  polling: { label: "stream down · refreshing every 15 s", cls: "text-bg-warning", title: "The live stream failed 3 times: the page refreshes every 15 seconds and keeps retrying the stream. The kill switch works without it." },
  paused: { label: "paused", cls: "text-bg-secondary", title: "The tab was hidden for a minute: the stream is closed until you come back" },
  ended: { label: "ended", cls: "text-bg-danger", title: "The server closed the stream" },
};

// LiveStatus shows the stream's state and the stale badge.
export function LiveStatus({ live }: { live: Live }) {
  const c = connectionText[live.connection];
  return (
    <span className="d-inline-flex flex-wrap align-items-center gap-1" aria-live="polite">
      <span className={"badge " + c.cls} title={c.title}>{c.label}</span>
      {live.stale && <span className="badge text-bg-warning" title={"Stale: " + live.staleReason}>stale</span>}
    </span>
  );
}

// LiveEnded explains a stream the server closed for good.
export function LiveEnded({ reason }: { reason: string }) {
  if (!reason) return null;
  return (
    <div className="alert alert-warning py-2 small" role="status">
      {reason === "signed_out"
        ? "The live view ended: you were signed out. Sign in again to resume it."
        : "The live view ended: you no longer have access to this account."}
    </div>
  );
}

// LiveStats is the row of headline figures.
export function LiveStats({ snapshot }: { snapshot: LiveSnapshot | null }) {
  const f = snapshot?.figures;
  return (
    <div className="row g-3 mb-4">
      <Stat label="Equity" icon="bi-graph-up" value={f ? usd(f.equityCents) : "—"}
        sub={snapshot?.portfolioAt ? <>as of {clock(snapshot.portfolioAt)}</> : snapshot ? "no portfolio read yet" : "waiting for the live view"} />
      <Stat label="Cash left" icon="bi-cash-coin" value={f ? usd(f.cashLeftCents) : "—"}
        sub={f ? <>cash {usd(f.cashCents)} · reserved {usd(f.reservedCents)}</> : null} />
      <Stat label="Trading budget left" icon="bi-wallet2" value={f ? usd(f.budgetLeftCents) : "—"}
        sub={f ? <>used {usd(f.budgetUsedCents)} of {usd(f.budgetCents)}</> : null} />
      <Stat label="Day P&L" icon="bi-calendar-day" value={f ? (f.dayKnown ? usd(f.dayPnlCents) : "—") : "—"}
        valueClass={f?.dayKnown ? (f.dayPnlCents < 0 ? "text-danger" : "") : ""}
        sub={f ? (f.dayKnown ? <>since the start of the risk day ({usd(f.startOfDayCents)})</> : "not known yet today") : null} />
    </div>
  );
}

function Stat({ label, icon, value, sub, valueClass = "" }: { label: string; icon: string; value: string; sub: ReactNode; valueClass?: string }) {
  return (
    <div className="col-6 col-lg-3">
      <div className="card stat h-100">
        <div className="card-body">
          <div className="text-secondary small"><i className={"bi me-1 " + icon} />{label}</div>
          <div className={"value " + valueClass}>{value}</div>
          {sub && <div className="small text-secondary">{sub}</div>}
        </div>
      </div>
    </div>
  );
}

// share is the part of a whole as a bar width (0–1).
function share(part: number, whole: number): number {
  if (!(whole > 0)) return 0;
  return Math.min(1, Math.max(0, part / whole));
}

// LimitBars shows how far the account is from every limit the risk engine applies. A
// distance at or below 0 means the risk engine refuses new entries (the server decides).
export function LimitBars({ figures, accountId }: { figures?: LiveFigures; accountId: string }) {
  return (
    <div className="card h-100"><div className="card-body">
      <h2 className="h5">Distance to the limits</h2>
      {!figures && <p className="text-secondary mb-0">Shown once a portfolio has been read.</p>}
      {figures && (
        <div className="d-flex flex-column gap-2 small">
          <Bar label="Cash left" left={figures.cashLeftCents} whole={figures.cashCents}
            text={<>{usd(figures.cashLeftCents)} of {usd(figures.cashCents)}</>} />
          {figures.budgetCents > 0
            ? <Bar label="Trading budget left" left={figures.budgetLeftCents} whole={figures.budgetCents}
                text={<>{usd(figures.budgetLeftCents)} of {usd(figures.budgetCents)}</>} />
            : (
              <div className="d-flex flex-wrap align-items-center gap-2">
                <span className="fw-semibold">Trading budget</span>
                <span className="text-secondary">No trading budget: no entries.</span>
                <FixButton what="trading_budget" accountId={accountId} />
              </div>
            )}
          {figures.dayKnown
            ? <Bar label="To the daily loss limit" left={figures.toDailyFloorCents} whole={figures.startOfDayCents - figures.dailyFloorCents}
                text={<>{usd(figures.toDailyFloorCents)} <span className="text-secondary">(floor {usd(figures.dailyFloorCents)})</span></>}
                note={figures.dayStopped ? "daily limit reached" : undefined} />
            : <Plain label="To the daily loss limit" text="not known yet today" />}
          <Bar label="To the drawdown kill switch" left={figures.toDrawdownFloorCents} whole={figures.highWaterMarkCents - figures.drawdownFloorCents}
            text={<>{usd(figures.toDrawdownFloorCents)} <span className="text-secondary">(floor {usd(figures.drawdownFloorCents)})</span></>}
            note={figures.killed ? "kill switch on" : undefined} />
          <Bar label="Position slots left" left={figures.slotsLeft} whole={figures.cap}
            text={<>{figures.slotsLeft} of {figures.cap} <span className="text-secondary">({figures.open} open or pending)</span></>} />
          <Bar label="Short exposure left" left={figures.shortLeftCents} whole={figures.shortCapCents}
            text={<>{usd(figures.shortLeftCents)} of {usd(figures.shortCapCents)} <span className="text-secondary">(used {usd(figures.shortUsedCents)})</span></>} />
          {figures.markets.map((m) => (
            <Plain key={m.market} label={marketName[m.market] ?? m.market}
              text={m.trading && m.noEntryAt && m.flattenAt
                ? <>no entries from {clock(m.noEntryAt)} ({duration(m.toNoEntrySeconds)}) · flatten at {clock(m.flattenAt)} ({duration(m.toFlattenSeconds)})</>
                : <span className="text-secondary">not trading today</span>} />
          ))}
        </div>
      )}
    </div></div>
  );
}

function Bar({ label, left, whole, text, note }: { label: string; left: number; whole: number; text: ReactNode; note?: string }) {
  const w = share(left, whole);
  const out = left <= 0;
  const cls = out ? "bg-danger" : w < 0.25 ? "bg-warning" : "bg-success";
  return (
    <div>
      <div className="d-flex flex-wrap justify-content-between gap-2">
        <span className="fw-semibold">{label}</span>
        <span>{text}{out && <span className="badge text-bg-danger ms-1">no new entries</span>}{note && <span className="badge text-bg-warning ms-1">{note}</span>}</span>
      </div>
      <div className="progress limit-bar" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(w * 100)}>
        <div className={"progress-bar " + cls} style={{ width: `${(w * 100).toFixed(1)}%` }} />
      </div>
    </div>
  );
}

function Plain({ label, text }: { label: string; text: ReactNode }) {
  return (
    <div className="d-flex flex-wrap justify-content-between gap-2">
      <span className="fw-semibold">{label}</span><span>{text}</span>
    </div>
  );
}

// LivePositions lists every position of the account; only cactusTrade's own are ever closed by it.
export function LivePositions({ snapshot }: { snapshot: LiveSnapshot | null }) {
  const ps = snapshot?.positions ?? [];
  return (
    <div className="card mb-4"><div className="card-body">
      <h2 className="h5">Open positions</h2>
      {!snapshot && <p className="text-secondary mb-0">Waiting for the live view.</p>}
      {snapshot && !snapshot.portfolioKnown && <p className="text-secondary mb-0">No portfolio read yet.</p>}
      {snapshot?.portfolioKnown && ps.length === 0 && <p className="text-secondary mb-0">No open positions.</p>}
      {ps.length > 0 && (
        <div className="table-responsive">
          <table className="table table-sm align-middle mb-0">
            <thead><tr>
              <th>Share</th><th>Opened by</th><th>Direction</th><th>Opened</th><th className="text-end">Rate</th><th className="text-end">Units</th>
              <th className="text-end">Invested</th><th className="text-end">P&amp;L</th><th className="text-end">Stop</th><th className="text-end">Target</th><th>Closed at</th>
            </tr></thead>
            <tbody>
              {ps.map((p) => (
                <tr key={p.positionId}>
                  <td className="fw-semibold" title={`position ${p.positionId}`}>{p.symbol || p.instrumentId}</td>
                  <td>{p.ours
                    ? <span className="badge text-bg-success">cactusTrade</span>
                    : <span className="badge text-bg-secondary" title="cactusTrade leaves this position alone">not opened by cactusTrade</span>}</td>
                  <td>
                    <span className={"badge " + (p.long ? "text-bg-success" : "text-bg-danger")}>{p.long ? "long" : "short"}</span>
                    {!p.realAsset && <span className="badge text-bg-light ms-1" title="a CFD, not the share itself">CFD</span>}
                    {p.leverage !== 1 && <span className="badge text-bg-warning ms-1" title="leverage">x{p.leverage}</span>}
                  </td>
                  <td className="small text-nowrap">{new Date(p.openedAt).toLocaleString()}</td>
                  <td className="text-end">{p.openRate}</td>
                  <td className="text-end">{p.units}</td>
                  <td className="text-end">{usd(p.amountCents)}</td>
                  <td className={"text-end " + (p.unrealisedPnlCents < 0 ? "text-danger" : "")}>{usd(p.unrealisedPnlCents)}</td>
                  <td className="text-end">{p.stopLossRate ?? "—"}</td>
                  <td className="text-end">{p.takeProfitRate ?? "—"}</td>
                  <td className="small">{p.flattenAt ? clock(p.flattenAt) : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="small text-secondary mt-3 mb-0">
        Every position in the account. cactusTrade closes only the ones it opened, before the market closes; the others are left alone.
      </p>
    </div></div>
  );
}

// OrderFeed is the orders and fills feed (the events are in the activity log, R26): each line
// shows its direction (long or short), the feed can show long or short orders only, and
// identical requests in a row (e.g. a cancel repeated every minute) are folded into one line.
export function OrderFeed({ orders }: { orders: Order[] }) {
  const [only, setOnly] = useState<"" | "long" | "short">("");
  const count = (d: string) => orders.filter((o) => o.direction === d).length;
  const lines = foldRepeats(only ? orders.filter((o) => o.direction === only) : orders);
  const pick = (v: "" | "long" | "short", label: string, n: number) => (
    <button type="button" className={"btn " + (only === v ? "btn-secondary" : "btn-outline-secondary")} aria-pressed={only === v}
      onClick={() => setOnly(v)}>{label} <span className="badge text-bg-light ms-1">{n}</span></button>
  );
  return (
    <div className="card h-100"><div className="card-body">
      <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
        <h2 className="h5 mb-0 flex-grow-1">Orders and fills</h2>
        {orders.length > 0 && (
          <div className="btn-group btn-group-sm" role="group" aria-label="Show orders by direction">
            {pick("", "All", orders.length)}{pick("long", "Long", count("long"))}{pick("short", "Short", count("short"))}
          </div>
        )}
      </div>
      {orders.length === 0 && <p className="text-secondary mb-0">No orders yet.</p>}
      {orders.length > 0 && lines.length === 0 && <p className="text-secondary mb-0">No {only} orders.</p>}
      {lines.length > 0 && (
        <ul className="list-unstyled small mb-0" aria-label="Orders feed">
          {lines.map(({ order: o, times, since }) => (
            <li key={o.requestId} className="d-flex flex-wrap align-items-center gap-2 py-1 border-bottom">
              <span className="text-secondary text-nowrap">{times > 1 ? `${dateTime(since)}–${sameDay(since, o.updatedAt) ? hourMinute(o.updatedAt) : dateTime(o.updatedAt)}` : dateTime(o.updatedAt)}</span>
              <DirectionBadge direction={o.direction} />
              {o.exchange && <span className="badge text-bg-light border" title={`Exchange: ${o.exchange}`}>{o.exchange}</span>}
              <span className="fw-semibold">{o.kindLabel}</span>
              {o.kind === "open" && <span>{usd(o.amountCents)}</span>}
              <span className={"badge " + (orderBadge[o.state] ?? "text-bg-secondary")}>{o.stateLabel}</span>
              {times > 1 && <span className="badge text-bg-light" title={`${times} identical requests in a row`}>×{times}</span>}
            </li>
          ))}
        </ul>
      )}
    </div></div>
  );
}

const sameDay = (a: string, b: string) => new Date(a).toDateString() === new Date(b).toDateString();

// foldRepeats folds identical requests that follow each other (same wording and state) into one
// line with how many there were and the time of the oldest one.
function foldRepeats(orders: Order[]): { order: Order; times: number; since: string }[] {
  const out: { order: Order; times: number; since: string }[] = [];
  for (const o of orders) {
    const last = out[out.length - 1];
    if (last && last.order.kind !== "open" && last.order.kindLabel === o.kindLabel && last.order.stateLabel === o.stateLabel) {
      last.times++;
      last.since = o.updatedAt;
    } else out.push({ order: o, times: 1, since: o.updatedAt });
  }
  return out;
}

// The market moods' icons and bar colours (presentation only; the words come from the server).
const moodIcon: Record<string, string> = {
  trending: "bi-graph-up-arrow", choppy: "bi-arrow-left-right", high_volatility: "bi-lightning-charge",
};
const moodBar: Record<string, string> = { trending: "t", choppy: "c", high_volatility: "h" };
const stanceBadge: Record<string, string> = {
  trend: "text-bg-success", reversion: "text-bg-success", both: "text-bg-success",
  stay_out: "text-bg-danger", wait: "text-bg-secondary", unknown: "text-bg-light",
};

// RegimeList ("Market mood", R28) shows each watchlist share's latest market mood in plain
// words, the chances of each mood as one stacked bar, when it was read, and what the agent
// may do in it at the account's current thresholds. The server provides every word.
export function RegimeList({ regimes, legend }: { regimes: LiveRegime[]; legend: RegimeText[] }) {
  const label = (r: string) => legend.find((x) => x.regime === r)?.label ?? r.replace("_", " ");
  const chances = (p: Record<string, number>) => legend.map((x) => `${x.label.toLowerCase()} ${pct(p[x.regime])}`).join(", ");
  return (
    <>
      <div className="card mb-3"><div className="card-body">
        <h2 className="h5">Market mood</h2>
        {legend.length > 0 && (
          <ul className="list-unstyled small d-flex flex-wrap gap-3 mb-2" aria-label="Market mood legend">
            {legend.map((x) => (
              <li key={x.regime} className="d-flex align-items-center gap-1" title={`${x.label}: ${x.description} (${x.agentMayDo})`}>
                <span className={"mood-swatch " + (moodBar[x.regime] ?? "")} aria-hidden="true" />
                <i className={"bi " + (moodIcon[x.regime] ?? "bi-question-circle")} aria-hidden="true" />
                <span><strong>{x.label}</strong> — {x.description} ({x.agentMayDo})</span>
              </li>
            ))}
          </ul>
        )}
        {regimes.length === 0 && <p className="text-secondary mb-0">No shares on the watchlist.</p>}
        {regimes.length > 0 && (
          <div className="table-responsive">
            <table className="table table-sm align-middle mb-0">
              <thead><tr><th>Share</th><th>Market mood</th><th>Chances</th><th>What the agent may do</th></tr></thead>
              <tbody>
                {regimes.map((r) => {
                  const p = r.probabilities ?? {};
                  const mood = r.mood?.label ?? (r.regime ? label(r.regime) : "");
                  return (
                    <tr key={r.instrumentId}>
                      <td className="fw-semibold">{r.symbol || r.instrumentId}</td>
                      <td>
                        {r.known ? (
                          <>
                            <i className={"bi me-1 " + (moodIcon[r.regime ?? ""] ?? "bi-question-circle")} aria-hidden="true" />
                            <span className="fw-semibold">{mood}</span>
                            {r.mood?.description && <span className="text-secondary small"> — {r.mood.description}</span>}
                            {r.at && <div className="text-secondary small">updated {clock(r.at)}</div>}
                          </>
                        ) : <span className="text-secondary">not reviewed yet</span>}
                      </td>
                      <td>
                        {r.known && (
                          <div className="probbar" role="img" aria-label={`Chances: ${chances(p)}`} title={chances(p)}>
                            {legend.map((x) => (
                              <div key={x.regime} className={moodBar[x.regime] ?? ""} style={{ width: pct(p[x.regime]) }} />
                            ))}
                          </div>
                        )}
                      </td>
                      <td className="small">
                        {r.mayDo && <span className={"badge me-1 " + (stanceBadge[r.stance ?? "unknown"] ?? "text-bg-light")}>{r.stance === "stay_out" ? "out" : r.stance === "wait" ? "wait" : r.stance === "unknown" ? "—" : "may act"}</span>}
                        {r.mayDo}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div></div>
      <InfoCard id="market-mood" title="What is the market mood?">
        <p className="small mb-0">
          cactusTrade's model reads each share's recent price moves and estimates how likely each mood is: trending (moving
          steadily one way), choppy (going sideways) or volatile (big swings). The agent only looks for an entry when one mood is
          clear enough for its aggressiveness, and stays out when swings are big; every entry still passes the risk limits.
        </p>
      </InfoCard>
    </>
  );
}

function pct(v: number | undefined): string {
  return v === undefined ? "0%" : `${Math.round(v * 100)}%`;
}

// clock formats a time as the user's hours and minutes.
function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

// duration formats seconds as "in 1 h 05 min" ("passed" at 0).
export function duration(seconds: number): string {
  if (seconds <= 0) return "passed";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return h > 0 ? `in ${h} h ${String(m).padStart(2, "0")} min` : `in ${Math.max(m, 1)} min`;
}
