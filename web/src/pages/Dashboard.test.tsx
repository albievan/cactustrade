import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { FakeEventSource } from "../lib/fakeEventSource";
import type { LiveSnapshot } from "../lib/live";
import type { Order } from "../lib/api";
import { Dashboard } from "./Dashboard";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "agent.kill"], via: "session", csrfToken: "c",
};
const account = { id: "a1", name: "Main demo", ownerId: "1", ownerName: "admin", mode: "demo" as const, platform: "etoro" as const, envKeys: false, access: "owner" as const, createdAt: "" };
const status = { version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "", account };
const agent = {
  account, state: "stopped", reconciliation: { accountId: "a1", ok: false, reason: "not reconciled yet" },
  limits: { tradingBudgetCents: 0, dailyLossBp: 200, drawdownBp: 1000, maxPositionBp: 2000, maxOpenPositions: 5,
    noEntryBeforeCloseMinutes: 30, flattenBeforeCloseMinutes: 10, maxPortfolioAgeSeconds: 5, maxQuoteAgeSeconds: 5, maxShortBp: 2500 },
  killed: false, dayStopped: false, riskDay: "", today: "2026-11-24", startOfDayEquityCents: 0, highWaterMarkCents: 0,
  equityKnown: false, equityError: "the account's eToro keys are not set", blocked: [], flatten: [],
  lease: { mine: false, other: false }, controls: { start: true, pause: true, stop: true, kill: true, reset: true, gated: false },
  resetPhrase: "RESET Main demo", openPositions: 0, pendingOrders: 0, missing: [],
};

function order(over: Partial<Order> = {}): Order {
  return {
    requestId: "r1", mode: "demo", kind: "open", direction: "long", instrumentId: 1001, symbol: "AAPL", amountCents: 50000,
    state: "sent", reason: "", sends: 1, createdAt: "2026-11-24T15:00:00Z", updatedAt: "2026-11-24T15:00:00Z", ...over,
  };
}

function snapshot(over: Partial<LiveSnapshot> = {}): LiveSnapshot {
  return {
    account, at: "2026-11-24T15:00:05Z", portfolioKnown: true, portfolioAt: "2026-11-24T15:00:00Z", stale: false,
    figures: {
      asOf: "2026-11-24T15:00:00Z", equityCents: 1234567, cashCents: 800000, reservedCents: 50000, cashLeftCents: 750000,
      budgetCents: 500000, budgetUsedCents: 120000, budgetLeftCents: 380000, dayKnown: true, day: "2026-11-24",
      startOfDayCents: 1200000, dayPnlCents: 34567, dailyFloorCents: 1176000, toDailyFloorCents: 58567, dayStopped: false,
      highWaterMarkCents: 1250000, drawdownFloorCents: 1125000, toDrawdownFloorCents: 109567, killed: false,
      open: 2, cap: 8, slotsLeft: 6, shortUsedCents: 0, shortCapCents: 125000, shortLeftCents: 125000,
      markets: [{ market: "NYSE", trading: true, noEntryAt: "2026-11-24T20:30:00Z", flattenAt: "2026-11-24T20:50:00Z", toNoEntrySeconds: 19800, toFlattenSeconds: 21000 }],
    },
    positions: [
      { positionId: 11, instrumentId: 1001, symbol: "AAPL", market: "NYSE", openedAt: "2026-11-24T14:00:00Z", openRate: "190.5", units: "2.5",
        amountCents: 47625, unrealisedPnlCents: 1200, stopLossRate: "185", takeProfitRate: "200", realAsset: true, long: true, leverage: 1,
        ours: true, openRequestId: "r0", flattenAt: "2026-11-24T20:50:00Z" },
      { positionId: 12, instrumentId: 1002, symbol: "MSFT", market: "NYSE", openedAt: "2026-11-20T14:00:00Z", openRate: "400", units: "1",
        amountCents: 40000, unrealisedPnlCents: -500, realAsset: true, long: true, leverage: 1, ours: false },
    ],
    openOrders: [order()], recentOrders: [order()],
    regimes: [{ instrumentId: 1001, symbol: "AAPL", known: true, regime: "trending", probabilities: { trending: 0.7, choppy: 0.2, high_volatility: 0.1 }, at: "2026-11-24T14:55:00Z" }],
    state: "running", reconciliation: { accountId: "a1", ok: true, reason: "", at: "2026-11-24T14:00:00Z" },
    killed: false, dayStopped: false, lease: { mine: true, other: false },
    ...over,
  };
}

interface Call { method: string; url: string; body?: unknown }
let calls: Call[] = [];

function serve(answers: Record<string, unknown> = {}) {
  calls = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/auth/me")) return json(me);
    if (url.endsWith("/api/v1/system/status")) return json(status);
    if (url.endsWith("/api/v1/agent") && method === "GET") return json(agent);
    if (url.endsWith("/api/v1/market/status")) return json([]);
    if (url.includes("/api/v1/orders")) return json([order({ requestId: "r9", symbol: "NVDA", state: "filled" })]);
    if (url.includes("/api/v1/reviews")) return json({ scanner: { enabled: false }, reviews: [] });
    const key = url.replace(/^.*\/api\/v1\//, "");
    if (key in answers) return json(answers[key]);
    return new Response(JSON.stringify({ title: "Not found", status: 404 }), { status: 404 });
  });
}

function mount() {
  render(<MemoryRouter><SessionProvider><ModeProvider><Dashboard /></ModeProvider></SessionProvider></MemoryRouter>);
}

const agentReads = () => calls.filter((c) => c.method === "GET" && c.url.endsWith("/api/v1/agent")).length;
const ordersReads = () => calls.filter((c) => c.url.includes("/api/v1/orders")).length;

// stream waits for the dashboard to open its live stream.
async function stream(): Promise<FakeEventSource> {
  await waitFor(() => expect(FakeEventSource.all.length).toBeGreaterThan(0));
  return FakeEventSource.latest();
}

async function advance(ms: number) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms); });
}

beforeEach(() => {
  FakeEventSource.reset();
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Dashboard", () => {
  it("shows the agent panel and no longer says orders arrive in phase 4", async () => {
    serve();
    mount();
    expect(await screen.findByRole("group", { name: "Agent controls" })).toBeInTheDocument();
    expect(screen.getByText("not reconciled")).toBeInTheDocument();
    expect(screen.queryByText(/phase 4/)).not.toBeInTheDocument();
  });

  it("opens one stream for the current account and renders its snapshot", async () => {
    serve();
    mount();
    const es = await stream();
    expect(es.url).toBe("/api/v1/live?accountId=a1");
    act(() => es.send("snapshot", snapshot()));
    expect(await screen.findByText("$12,345.67")).toBeInTheDocument(); // equity
    expect(screen.getByText("$7,500.00")).toBeInTheDocument(); // cash left
    expect(screen.getByText("$3,800.00")).toBeInTheDocument(); // budget left
    expect(screen.getByText("$345.67")).toBeInTheDocument(); // day P&L
    expect(screen.getByText("live")).toBeInTheDocument();
    // positions: ours and not ours, with stop, target and the flatten time
    expect(screen.getByText("not opened by cactusTrade")).toBeInTheDocument();
    expect(screen.getByText("185")).toBeInTheDocument();
    expect(screen.getByText("200")).toBeInTheDocument();
    // limit distances as bars, with the server's figures
    expect(screen.getByRole("progressbar", { name: "Position slots left" })).toHaveAttribute("aria-valuenow", "75");
    expect(screen.getByText(/6 of 8/)).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "To the daily loss limit" })).toBeInTheDocument();
    // regimes per share and the orders feed
    expect(screen.getByText("trending")).toBeInTheDocument();
    const feed = screen.getByRole("list", { name: "Orders feed" });
    expect(within(feed).getByText("sent")).toBeInTheDocument();
  });

  it("updates the orders feed from an order message", async () => {
    serve();
    mount();
    const es = await stream();
    act(() => es.send("snapshot", snapshot()));
    const feed = await screen.findByRole("list", { name: "Orders feed" });
    expect(within(feed).getByText("sent")).toBeInTheDocument();
    act(() => es.send("order", { accountId: "a1", order: order({ state: "filled", updatedAt: "2026-11-24T15:00:09Z" }) }));
    expect(within(feed).getByText("filled")).toBeInTheDocument();
    expect(within(feed).queryByText("sent")).not.toBeInTheDocument();
    act(() => es.send("order", { accountId: "a1", order: order({ requestId: "r2", symbol: "TSLA", kind: "close", direction: "long" }) }));
    expect(within(feed).getByText("TSLA")).toBeInTheDocument();
    act(() => es.send("event", { accountId: "a1", event: { id: "e1", at: "2026-11-24T15:00:10Z", kind: "order", severity: "success", title: "Bought AAPL", actor: "agent" } }));
    expect(screen.getByText("Bought AAPL")).toBeInTheDocument();
  });

  it("refetches the agent panel and the orders on resync", async () => {
    serve();
    mount();
    const es = await stream();
    act(() => es.send("snapshot", snapshot({ state: "stopped" })));
    await screen.findByText("live");
    await waitFor(() => expect(agentReads()).toBeGreaterThan(0));
    const agentBefore = agentReads();
    const ordersBefore = ordersReads();
    act(() => es.send("resync", { reason: "behind" }));
    await waitFor(() => expect(agentReads()).toBe(agentBefore + 1));
    expect(ordersReads()).toBe(ordersBefore + 1);
    // the panel no longer polls on its own while the stream is live
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const steady = agentReads();
    await advance(31_000);
    expect(agentReads()).toBe(steady);
  });

  it("refetches the agent panel when the stream reports a change of the agent", async () => {
    serve();
    mount();
    const es = await stream();
    act(() => es.send("snapshot", snapshot({ state: "running" })));
    await screen.findByText("live");
    await waitFor(() => expect(agentReads()).toBeGreaterThan(0));
    const before = agentReads();
    act(() => es.send("snapshot", snapshot({ state: "running" })));
    expect(agentReads()).toBe(before); // nothing changed
    act(() => es.send("snapshot", snapshot({ state: "killed", killed: true })));
    await waitFor(() => expect(agentReads()).toBe(before + 1));
  });

  it("shows a stale badge after 30 seconds without a snapshot", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    serve();
    mount();
    const es = await stream();
    act(() => es.send("snapshot", snapshot()));
    await screen.findByText("live");
    expect(screen.queryByTitle(/^Stale:/)).not.toBeInTheDocument();
    await advance(29_000);
    expect(screen.queryByTitle(/^Stale:/)).not.toBeInTheDocument();
    await advance(2_000);
    expect(screen.getByTitle(/^Stale:/)).toBeInTheDocument();
    act(() => es.send("snapshot", snapshot()));
    expect(screen.queryByTitle(/^Stale:/)).not.toBeInTheDocument();
    // the server's own stale flag (an old portfolio) shows the badge too
    act(() => es.send("snapshot", snapshot({ stale: true })));
    expect(screen.getByTitle("Stale: the portfolio is older than 30 seconds")).toHaveTextContent("stale");
  });

  it("reconnects with backoff and polls every 15 s after 3 failures; the kill switch still works", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    serve({ "agent/kill": { accountId: "a1", already: false, cancelled: 0, closing: 1, errors: [] } });
    mount();
    const first = await stream();
    await screen.findByLabelText("Kill switch");
    act(() => first.fail());
    expect(first.closed).toBe(true);
    expect(screen.getByText("reconnecting")).toBeInTheDocument();
    await advance(1_000); // 1 s backoff
    expect(FakeEventSource.all).toHaveLength(2);
    act(() => FakeEventSource.latest().fail());
    await advance(1_999);
    expect(FakeEventSource.all).toHaveLength(2);
    await advance(1); // 2 s backoff
    expect(FakeEventSource.all).toHaveLength(3);
    const agentBefore = agentReads();
    act(() => FakeEventSource.latest().fail()); // the third failure: fall back to polling
    expect(screen.getByText(/stream down/)).toBeInTheDocument();
    await waitFor(() => expect(agentReads()).toBe(agentBefore + 1));
    await advance(15_000);
    await waitFor(() => expect(agentReads()).toBe(agentBefore + 2));
    expect(ordersReads()).toBeGreaterThanOrEqual(2);
    expect(FakeEventSource.open()).toHaveLength(1); // still retrying the stream (4 s backoff)
    act(() => FakeEventSource.latest().fail());

    // The stream is down: the kill switch is enabled and works over REST.
    const kill = screen.getByLabelText("Kill switch");
    expect(kill).toBeEnabled();
    fireEvent.click(kill);
    fireEvent.click(screen.getByRole("button", { name: /Kill now/ }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST")).toEqual([{ method: "POST", url: "/api/v1/agent/kill", body: { reason: "" } }]));

    // A snapshot on a retried stream ends the fallback.
    await advance(30_000);
    act(() => FakeEventSource.latest().send("snapshot", snapshot()));
    expect(screen.getByText("live")).toBeInTheDocument();
    const steady = agentReads();
    await advance(31_000);
    expect(agentReads()).toBeLessThanOrEqual(steady + 1); // at most the change the snapshot itself caused
  });

  it("caps the backoff at 30 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    serve();
    mount();
    await stream();
    for (let i = 0; i < 6; i++) {
      act(() => FakeEventSource.latest().fail());
      await advance(Math.min(1_000 * 2 ** i, 30_000));
      expect(FakeEventSource.all).toHaveLength(i + 2);
    }
    act(() => FakeEventSource.latest().fail()); // 7th: 64 s would be next, capped at 30 s
    await advance(29_999);
    expect(FakeEventSource.all).toHaveLength(7);
    await advance(1);
    expect(FakeEventSource.all).toHaveLength(8);
  });

  it("reconnects at once after the stream's lifetime and stops for good when signed out", async () => {
    serve();
    mount();
    const first = await stream();
    act(() => first.send("snapshot", snapshot()));
    act(() => first.send("bye", { reason: "lifetime" }));
    expect(first.closed).toBe(true);
    expect(FakeEventSource.all).toHaveLength(2);
    const second = FakeEventSource.latest();
    act(() => second.send("bye", { reason: "signed_out" }));
    expect(second.closed).toBe(true);
    expect(await screen.findByText(/you were signed out/)).toBeInTheDocument();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await advance(60_000);
    expect(FakeEventSource.all).toHaveLength(2);
  });

  it("closes the stream after 60 s hidden and reopens it when shown", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    serve();
    mount();
    const es = await stream();
    act(() => es.send("snapshot", snapshot()));
    let hidden = true;
    vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
    act(() => { document.dispatchEvent(new Event("visibilitychange")); });
    await advance(59_000);
    expect(es.closed).toBe(false);
    await advance(1_000);
    expect(es.closed).toBe(true);
    expect(screen.getByText("paused")).toBeInTheDocument();
    hidden = false;
    act(() => { document.dispatchEvent(new Event("visibilitychange")); });
    expect(FakeEventSource.all).toHaveLength(2);
    expect(FakeEventSource.latest().closed).toBe(false);
  });
});
