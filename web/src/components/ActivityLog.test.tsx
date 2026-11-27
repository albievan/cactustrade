import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ActivityLog } from "./ActivityLog";
import type { Account, AppEvent, Decision } from "../lib/api";
import type { Live } from "../lib/live";
import {
  ACTIVITY, clockSeconds, fromDecision, fromEvent, fromTick, mergeActivity, shareLine, shows,
  type ActivityEntry, type LiveTick,
} from "../lib/activity";

const account: Account = { id: "a1", name: "Main demo", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "" };

function ev(over: Partial<AppEvent> = {}): AppEvent {
  return { id: "e1", at: "2026-11-24T15:00:01Z", kind: "agent.started", severity: "info", title: "Agent started on Main demo", actor: "albie", accountId: "a1", ...over };
}

function decision(over: Partial<Decision> = {}): Decision {
  return {
    id: "d1", accountId: "a1", at: "2026-11-24T15:00:03Z", instrumentId: 1001, symbol: "AAPL", direction: "long", playbook: "trend_breakout",
    signalReason: "breakout", entry: "189.12", stopLoss: "185", suggestedSizeCents: 60000, regimeProbs: {}, verdict: "approve", sizeBp: 10000,
    verdictReason: "fine", llmCallId: "c1", riskAllowed: true, riskSizeCents: 48000, riskReason: "allowed $480.00 of $600.00 requested",
    outcome: "ordered", orderRequestId: "r1", orderState: "sent", ...over,
  };
}

function tick(over: Partial<LiveTick> = {}): LiveTick {
  return {
    accountId: "a1", at: "2026-11-24T15:05:05Z", checked: 12, ideas: 1, orders: 1, skipped: "", reasons: { signal: 8, session: 3 },
    shares: [
      { instrumentId: 1001, symbol: "AAPL", entered: true, step: "", detail: "long $480.00" },
      { instrumentId: 1002, symbol: "MSFT", entered: false, step: "signal", detail: "no signal: flat" },
    ],
    killed: false, errors: 0, ...over,
  };
}

function live(activity: ActivityEntry[] = [], refresh = 0): Live {
  return { snapshot: null, orders: [], activity, connection: "live", endedReason: "", stale: false, staleReason: "", refresh };
}

// serve answers the history: events (of a1, of another account and system-wide) and decisions.
function serve(events: AppEvent[], decisions: Decision[]) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.includes("/api/v1/events")) return json({ events, unread: 0 });
    if (url.includes("/api/v1/decisions")) return json(decisions);
    return new Response("{}", { status: 404 });
  });
}

const lines = () => within(screen.getByRole("log", { name: "Agent activity" })).getAllByRole("listitem").filter((li) => li.classList.contains("activity-line"));

afterEach(() => vi.restoreAllMocks());

describe("activity lines", () => {
  it("words a decision as the idea, the reviewer, the risk engine and the outcome", () => {
    expect(fromDecision(decision()).text).toBe("AAPL long idea (trend breakout) — reviewer approved — risk allowed $480.00 — order sent");
    expect(fromDecision(decision({ orderState: "filled" })).text).toMatch(/order filled$/);
    const refused = fromDecision(decision({ outcome: "rejected_risk", riskAllowed: false, verdict: "", orderRequestId: "" }));
    expect(refused.text).toBe("AAPL long idea (trend breakout) — risk refused");
    expect(refused.groups).toEqual(["ideas"]);
    expect(fromDecision(decision({ outcome: "order_failed" })).groups).toEqual(["ideas", "orders", "alerts"]);
    expect(fromDecision(decision({ verdict: "reduce", sizeBp: 5000 })).text).toContain("reviewer cut it to 50 %");
  });

  it("words a tick with its counts and why the other shares stopped", () => {
    const t = fromTick(tick());
    expect(t.text).toBe("Tick: checked 12 shares, 1 idea, 1 order sent · 8 no signal, 3 not in session");
    expect(t.tone).toBe("success");
    expect(fromTick(tick({ skipped: "the watchlist is empty" })).text).toBe("Tick skipped: the watchlist is empty");
    const killed = fromTick(tick({ killed: true, errors: 1 }));
    expect(killed.tone).toBe("danger");
    expect(killed.groups).toEqual(["agent", "alerts"]);
    expect(shareLine(tick().shares[1])).toBe("MSFT: no signal: flat");
    expect(shareLine(tick().shares[0])).toBe("AAPL: order sent (long $480.00)");
  });

  it("names the person behind an event, never the machine", () => {
    expect(fromEvent(ev({ kind: "agent.killed", severity: "danger", title: "Kill switch on Main demo" })).text).toBe("Kill switch on Main demo by albie");
    const scanner = fromEvent(ev({ kind: "scanner.added", actor: "scanner", title: "NVDA added to the watchlist of Main demo by the scanner" }));
    expect(scanner.text).toBe("NVDA added to the watchlist of Main demo by the scanner");
    expect(scanner.groups).toEqual(["ideas"]);
    expect(fromEvent(ev({ kind: "order.filled", actor: "agent", severity: "success" })).groups).toEqual(["orders"]);
    expect(fromEvent(ev({ kind: "order.rejected", actor: "agent", severity: "warning" })).groups).toEqual(["orders", "alerts"]);
    expect(fromEvent(ev({ accountId: undefined })).accountId).toBe("");
  });

  it("keeps the newest first, replaces a known line and keeps at most the cap", () => {
    const at = (i: number) => new Date(Date.UTC(2026, 10, 24, 15, 0, i)).toISOString();
    const many = Array.from({ length: 350 }, (_, i) => fromEvent(ev({ id: `e${i}`, at: at(i) })));
    const merged = mergeActivity([], many);
    expect(merged).toHaveLength(ACTIVITY.cap);
    expect(merged[0].key).toBe("e:e349");
    expect(merged[merged.length - 1].key).toBe("e:e50");
    const again = mergeActivity(merged, [fromEvent(ev({ id: "e349", at: at(349), title: "changed" }))]);
    expect(again).toHaveLength(ACTIVITY.cap);
    expect(again[0].text).toMatch(/^changed/);
    expect(shows(merged[0], "agent") && !shows(merged[0], "orders") && shows(merged[0], "all")).toBe(true);
  });
});

describe("ActivityLog", () => {
  it("shows long and/or short ideas and orders (R39)", async () => {
    serve([
      ev(),
      ev({ id: "o1", at: "2026-11-24T15:00:05Z", kind: "order.filled", title: "Short $50.00 of TSLA filled", direction: "short" }),
    ], [decision(), decision({ id: "d2", at: "2026-11-24T15:00:04Z", symbol: "TSLA", direction: "short" })]);
    render(<ActivityLog account={account} live={live()} />);
    await waitFor(() => expect(lines()).toHaveLength(4));
    const sides = screen.getByRole("group", { name: "Show long and/or short" });
    const long = within(sides).getByRole("button", { name: /Long/ });
    const short = within(sides).getByRole("button", { name: /Short/ });
    fireEvent.click(long); // short only
    expect(long).toHaveAttribute("aria-pressed", "false");
    expect(lines().map((l) => l.textContent)).toEqual([expect.stringContaining("Short $50.00 of TSLA"), expect.stringContaining("TSLA short idea")]);
    fireEvent.click(short); // the last side on stays on
    expect(short).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(long); fireEvent.click(short); // long only
    expect(lines()).toHaveLength(1);
    expect(lines()[0]).toHaveTextContent("AAPL long idea");
    fireEvent.click(short); // both: everything again
    expect(lines()).toHaveLength(4);
  });

  it("shows the stored history newest first, this account's and system-wide only", async () => {
    serve([
      ev(),
      ev({ id: "e2", at: "2026-11-24T15:00:02Z", accountId: "other", title: "Another account's event" }),
      ev({ id: "e3", at: "2026-11-24T14:59:00Z", accountId: undefined, kind: "system.reloaded", title: "Settings reloaded", actor: "system" }),
    ], [decision()]);
    render(<ActivityLog account={account} live={live()} />);
    await waitFor(() => expect(lines()).toHaveLength(3));
    const ls = lines();
    expect(ls[0]).toHaveTextContent("AAPL long idea");
    expect(ls[0]).toHaveTextContent(clockSeconds("2026-11-24T15:00:03Z"));
    expect(ls[0]).toHaveTextContent("Main demo");
    expect(ls[1]).toHaveTextContent("Agent started on Main demo by albie");
    expect(ls[2]).toHaveTextContent("Settings reloaded");
    expect(ls[2]).toHaveTextContent("System");
    expect(screen.queryByText(/Another account's event/)).not.toBeInTheDocument();
  });

  it("puts a live message on top", async () => {
    serve([ev()], []);
    const { rerender } = render(<ActivityLog account={account} live={live()} />);
    await waitFor(() => expect(lines()).toHaveLength(1));
    rerender(<ActivityLog account={account} live={live([fromTick(tick())])} />);
    expect(lines()).toHaveLength(2);
    expect(lines()[0]).toHaveTextContent("Tick: checked 12 shares, 1 idea, 1 order sent");
    // a decision that was pending on load is replaced by its live outcome
    rerender(<ActivityLog account={account} live={live([fromDecision(decision({ at: "2026-11-24T15:06:00Z" })), fromTick(tick())])} />);
    expect(lines()[0]).toHaveTextContent("order sent");
  });

  it("filters by kind with compact toggle buttons", async () => {
    serve([
      ev(),
      ev({ id: "e2", at: "2026-11-24T15:00:02Z", kind: "order.filled", actor: "agent", severity: "success", title: "Bought AAPL" }),
      ev({ id: "e3", at: "2026-11-24T15:00:04Z", kind: "order.rejected", actor: "agent", severity: "warning", title: "Buy MSFT refused by eToro" }),
    ], [decision()]);
    render(<ActivityLog account={account} live={live([fromTick(tick())])} />);
    await waitFor(() => expect(lines()).toHaveLength(5));
    const pick = (name: RegExp) => fireEvent.click(screen.getByRole("button", { name }));
    pick(/Show orders/);
    expect(screen.getByRole("button", { name: /Show orders/ })).toHaveAttribute("aria-pressed", "true");
    expect(lines().map((l) => l.textContent)).toEqual([
      expect.stringContaining("Buy MSFT refused"), expect.stringContaining("AAPL long idea"), expect.stringContaining("Bought AAPL"),
    ]);
    pick(/Show the agent's ideas/);
    expect(lines()).toHaveLength(1);
    pick(/Show the agent's rounds/);
    expect(lines().map((l) => l.textContent)).toEqual([expect.stringContaining("Tick:"), expect.stringContaining("Agent started")]);
    pick(/Show warnings/);
    expect(lines()).toHaveLength(1);
    expect(lines()[0]).toHaveTextContent("Buy MSFT refused");
    pick(/Show every entry/);
    expect(lines()).toHaveLength(5);
  });

  it("keeps the list still while paused and counts what arrived", async () => {
    serve([ev()], []);
    const { rerender } = render(<ActivityLog account={account} live={live()} />);
    await waitFor(() => expect(lines()).toHaveLength(1));
    fireEvent.click(screen.getByRole("button", { name: "Pause the activity log" }));
    rerender(<ActivityLog account={account} live={live([fromTick(tick())])} />);
    expect(lines()).toHaveLength(1);
    const resume = screen.getByRole("button", { name: "Resume the activity log" });
    expect(resume).toHaveTextContent("1 new");
    expect(screen.getByRole("log")).toHaveAttribute("aria-live", "off");
    fireEvent.click(resume);
    expect(lines()).toHaveLength(2);
    expect(lines()[0]).toHaveTextContent("Tick:");
  });

  it("explains the empty log and loads the history again on a refresh", async () => {
    serve([], []);
    const { rerender } = render(<ActivityLog account={account} live={live()} />);
    expect(await screen.findByText(/Entries appear here as the agent works/)).toBeInTheDocument();
    const reads = () => vi.mocked(globalThis.fetch).mock.calls.filter(([u]) => String(u).includes("/api/v1/events")).length;
    expect(reads()).toBe(1);
    await act(async () => { rerender(<ActivityLog account={account} live={live([], 1)} />); });
    await waitFor(() => expect(reads()).toBe(2));
    expect(vi.mocked(globalThis.fetch).mock.calls.some(([u]) => String(u).includes(`/api/v1/events?limit=${ACTIVITY.historyEvents}`))).toBe(true);
  });

  it("shows a failed history read and still the rest", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes("/api/v1/decisions")) return new Response(JSON.stringify([decision()]), { status: 200 });
      return new Response(JSON.stringify({ title: "Unavailable", status: 503, detail: "the database is down" }), { status: 503 });
    });
    render(<ActivityLog account={account} live={live()} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/database is down/);
    expect(lines()).toHaveLength(1);
  });
});
