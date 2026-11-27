// The dashboard's agent activity log (R26): one line per thing the agent did, newest first.
// Lines come from stored events and decisions on load (GET /api/v1/events, GET
// /api/v1/decisions) and from the live stream's `event`, `decision` and `tick` messages. The
// server decides everything; these functions only turn its messages into words, an icon and a
// colour, and keep the list ordered and bounded.
import { usd, type AppEvent, type Decision } from "./api";
import type { components } from "./schema";

export type LiveTick = components["schemas"]["LiveTick"];
export type LiveTickShare = components["schemas"]["LiveTickShare"];
export type LiveDecision = components["schemas"]["LiveDecision"];

// ACTIVITY holds the log's sizes (exported for the tests and the page's explanations).
export const ACTIVITY = {
  historyEvents: 100, // stored events read on load
  historyDecisions: 50, // stored decisions read on load
  cap: 300, // lines kept in memory
};

export type ActivityFilter = "all" | "orders" | "ideas" | "agent" | "alerts";
export type ActivityTone = "info" | "success" | "warning" | "danger" | "muted";

export interface ActivityEntry {
  key: string; // unique per thing: a newer message about it replaces the older line
  at: string; // ISO time
  accountId: string; // "" = system-wide
  tone: ActivityTone;
  icon: string; // a Bootstrap Icons class
  text: string;
  detail?: string;
  groups: ActivityFilter[]; // the filters that show it besides "all"
  shares?: LiveTickShare[]; // a tick: what it did with each share
  direction?: "long" | "short"; // an idea, order or position: its side (R39)
}

// Sides is which directions the log shows (R39): both = every entry; one = only the entries
// of that direction (ideas, orders and positions).
export type Sides = { long: boolean; short: boolean };

// showsSide: the line passes the long/short filter.
export function showsSide(e: ActivityEntry, s: Sides): boolean {
  if (s.long && s.short) return true;
  return (s.long && e.direction === "long") || (s.short && e.direction === "short");
}

const side = (d?: string): "long" | "short" | undefined => (d === "long" || d === "short" ? d : undefined);

// FILTERS are the filter row's buttons, in order.
export const FILTERS: { id: ActivityFilter; label: string; icon: string; title: string }[] = [
  { id: "all", label: "All", icon: "bi-list-ul", title: "Show every entry" },
  { id: "orders", label: "Orders", icon: "bi-receipt", title: "Show orders, fills and closes" },
  { id: "ideas", label: "Ideas", icon: "bi-lightbulb", title: "Show the agent's ideas and the scanner's finds" },
  { id: "agent", label: "Agent", icon: "bi-robot", title: "Show the agent's rounds and its state" },
  { id: "alerts", label: "Alerts", icon: "bi-exclamation-triangle", title: "Show warnings and errors only" },
];

// The step that stopped a share in a round, in words (the server's TraceStep names).
const stepWords: Record<string, string> = {
  session: "not in session",
  candles: "no fresh prices",
  signal: "no signal",
  costs: "costs too high",
  review: "reviewer said no",
  prices: "price check failed",
  reservations: "orders unreadable",
  risk: "risk refused",
  order: "order not sent",
};

const severityTone: Record<string, ActivityTone> = { info: "info", success: "success", warning: "warning", danger: "danger" };
const severityIcon: Record<string, string> = {
  info: "bi-info-circle", success: "bi-check-circle", warning: "bi-exclamation-triangle", danger: "bi-x-octagon",
};

// eventIcons: icons for event kinds, the first matching prefix wins.
const eventIcons: [string, string][] = [
  ["order.sent", "bi-send"],
  ["order.filled", "bi-check2-circle"],
  ["order.cancelled", "bi-x-circle"],
  ["order.", "bi-exclamation-triangle"],
  ["position.closed", "bi-box-arrow-right"],
  ["position.", "bi-box-arrow-in-down"],
  ["agent.started", "bi-play-circle"],
  ["agent.resumed", "bi-play-circle"],
  ["agent.paused", "bi-pause-circle"],
  ["agent.stopped", "bi-stop-circle"],
  ["agent.killed", "bi-sign-stop"],
  ["agent.force_closed", "bi-door-closed"],
  ["agent.kill_reset", "bi-arrow-counterclockwise"],
  ["scanner.", "bi-binoculars"],
  ["watchlist.", "bi-eye"],
  ["theme.", "bi-tags"],
  ["reconcile.", "bi-arrow-left-right"],
  ["risk.", "bi-shield-exclamation"],
];

const machines = new Set(["", "system", "agent", "scanner"]);

function plural(n: number, one: string, many = one + "s"): string {
  return `${n} ${n === 1 ? one : many}`;
}

function alerting(tone: ActivityTone): boolean {
  return tone === "warning" || tone === "danger";
}

// fromEvent turns an event of the feed into a line; a person's action names them.
export function fromEvent(e: AppEvent, accountId = e.accountId ?? ""): ActivityEntry {
  const tone = severityTone[e.severity] ?? "info";
  const icon = eventIcons.find(([p]) => e.kind.startsWith(p))?.[1] ?? severityIcon[e.severity] ?? "bi-info-circle";
  const groups: ActivityFilter[] = [];
  if (e.kind.startsWith("order.") || e.kind.startsWith("position.")) groups.push("orders");
  else if (/^(scanner|watchlist|theme)\./.test(e.kind)) groups.push("ideas");
  else groups.push("agent");
  if (alerting(tone)) groups.push("alerts");
  const by = machines.has(e.actor) ? "" : ` by ${e.actor}`;
  return { key: "e:" + e.id, at: e.at, accountId, tone, icon, text: e.title + by, detail: e.detail, groups, direction: side(e.direction) };
}

// fromDecision turns a decision into one line: the idea, the reviewer, the risk engine, the
// outcome, e.g. "AAPL long idea (trend breakout) — reviewer approved — risk allowed $480.00 —
// order sent".
export function fromDecision(d: Decision): ActivityEntry {
  const parts = [`${d.symbol || d.instrumentId} ${d.direction} idea (${d.playbook.replaceAll("_", " ")})`];
  if (d.verdict === "approve") parts.push("reviewer approved");
  else if (d.verdict === "reduce") parts.push(`reviewer cut it to ${d.sizeBp / 100} %`);
  else if (d.verdict === "reject") parts.push("reviewer rejected it");
  if (d.riskAllowed) parts.push(`risk allowed ${usd(d.riskSizeCents)}`);
  let tone: ActivityTone = "muted";
  switch (d.outcome) {
    case "ordered": parts.push(d.orderState === "filled" ? "order filled" : "order sent"); tone = "success"; break;
    case "order_failed": parts.push("order not sent"); tone = "warning"; break;
    case "rejected_cost": parts.push("costs too high"); break;
    case "rejected_risk": parts.push("risk refused"); break;
    case "rejected_reviewer": if (!d.verdict) parts.push("no usable review"); break;
    case "pending": parts.push("order being sent"); tone = "info"; break;
  }
  const groups: ActivityFilter[] = ["ideas"];
  if (d.orderRequestId) groups.push("orders");
  if (alerting(tone)) groups.push("alerts");
  return {
    key: "d:" + d.id, at: d.at, accountId: d.accountId, tone, icon: "bi-lightbulb", text: parts.join(" — "),
    detail: d.riskReason || d.verdictReason || undefined, groups, direction: side(d.direction),
  };
}

// fromTick turns an agent round into one line, e.g. "Tick: checked 12 shares, 1 idea, 1 order
// sent · 8 no signal, 3 not in session"; its shares are listed under it.
export function fromTick(t: LiveTick): ActivityEntry {
  let text: string;
  let tone: ActivityTone = "muted";
  if (t.skipped) {
    text = `Tick skipped: ${t.skipped}`;
  } else {
    text = `Tick: checked ${plural(t.checked, "share")}, ${plural(t.ideas, "idea")}`;
    if (t.orders > 0) { text += `, ${plural(t.orders, "order")} sent`; tone = "success"; }
    const why = Object.entries(t.reasons)
      .filter(([, n]) => n > 0)
      .sort(([a, x], [b, y]) => y - x || a.localeCompare(b))
      .map(([step, n]) => `${n} ${stepWords[step] ?? step}`);
    if (why.length > 0) text += " · " + why.join(", ");
  }
  if (t.errors > 0) { text += ` · ${plural(t.errors, "error")}`; tone = "warning"; }
  if (t.killed) { text += " · the kill switch tripped"; tone = "danger"; }
  const groups: ActivityFilter[] = ["agent"];
  if (alerting(tone)) groups.push("alerts");
  return {
    key: `t:${t.accountId}:${t.at}`, at: t.at, accountId: t.accountId, tone, icon: "bi-arrow-repeat", text, groups,
    shares: t.shares.length > 0 ? t.shares : undefined,
  };
}

// shareLine is one share of a round in words, e.g. "MSFT: no signal: flat".
export function shareLine(s: LiveTickShare): string {
  const name = s.symbol || String(s.instrumentId);
  if (s.entered) return `${name}: order sent${s.detail ? ` (${s.detail})` : ""}`;
  return `${name}: ${s.detail || stepWords[s.step] || s.step || "no answer"}`;
}

// mergeActivity adds incoming lines to a list: a line with a known key replaces the older one,
// the list is newest first (equal times keep the incoming line on top) and keeps at most cap.
export function mergeActivity(list: ActivityEntry[], incoming: ActivityEntry[], cap = ACTIVITY.cap): ActivityEntry[] {
  const fresh = new Map(incoming.map((e) => [e.key, e]));
  const all = [...fresh.values(), ...list.filter((e) => !fresh.has(e.key))];
  const time = (e: ActivityEntry) => Date.parse(e.at) || 0;
  return all
    .map((e, i) => ({ e, i }))
    .sort((a, b) => time(b.e) - time(a.e) || a.i - b.i)
    .slice(0, cap)
    .map(({ e }) => e);
}

// shows: the line passes the filter.
export function shows(e: ActivityEntry, f: ActivityFilter): boolean {
  return f === "all" || e.groups.includes(f);
}

// clockSeconds formats a time as the user's HH:MM:SS.
export function clockSeconds(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}
