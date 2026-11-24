import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { ModeProvider } from "../lib/mode";
import type { DailyHistory, HistorySummary, HistoryTrade } from "../lib/api";
import { History, PAST_ONLY } from "./History";

const account = { id: "a1", name: "Main demo", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "" };
const status = { version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "", account };
const FROM = "2026-10-25T22:00:00Z";
const TO = "2026-11-24T15:00:00Z";

function summary(over: Partial<HistorySummary> = {}): HistorySummary {
  return {
    accountId: "a1", days: 30, from: FROM, to: TO, trades: 0, wins: 0, losses: 0, breakeven: 0,
    grossCents: 0, feesCents: 0, netCents: 0, feesUnknownTrades: 0, foreignTrades: 0,
    drawdown: { known: false, peakCents: 0, maxDrawdownCents: 0, maxDrawdownBp: 0, currentCents: 0, currentBp: 0 },
    ...over,
  };
}

function daily(rows: DailyHistory["rows"] = []): DailyHistory {
  return { accountId: "a1", days: 30, from: FROM, to: TO, rows };
}

function trade(over: Partial<HistoryTrade> = {}): HistoryTrade {
  return {
    positionId: 7, instrumentId: 1001, symbol: "AAPL", direction: "long", leverage: 1, units: "2", investedCents: 40000,
    openRate: "200", closeRate: "201", netProfitCents: 150, feesCents: 50, feesKnown: true,
    openedAt: "2026-11-20T15:00:00Z", closedAt: "2026-11-20T19:00:00Z", ours: true, openRequestId: "r1", etoroOrderId: 9, mode: "demo",
    ...over,
  };
}

let urls: string[] = [];

function serve(s: HistorySummary, trades: HistoryTrade[] | ((url: string) => HistoryTrade[]) = [], d: DailyHistory = daily()) {
  urls = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    urls.push(url);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/system/status")) return json(status);
    if (url.includes("/api/v1/history/summary")) return json(s);
    if (url.includes("/api/v1/history/daily")) return json(d);
    if (url.includes("/api/v1/history/trades")) return json(typeof trades === "function" ? trades(url) : trades);
    if (url.includes("/api/v1/overview/history")) return json([]);
    return new Response(JSON.stringify({ title: "Not found", status: 404 }), { status: 404 });
  });
  render(<MemoryRouter><ModeProvider><History /></ModeProvider></MemoryRouter>);
}

afterEach(() => vi.restoreAllMocks());

describe("History", () => {
  it("shows the fixed past-results note", async () => {
    serve(summary());
    expect(await screen.findByText(PAST_ONLY)).toBeInTheDocument();
    expect(PAST_ONLY).toBe("Figures describe past results only; they do not predict future results.");
  });

  it("shows no closed trades, and no 0 % win rate, for an empty history", async () => {
    serve(summary());
    expect(await screen.findByText("No closed trades in this period.")).toBeInTheDocument();
    expect(screen.getByText("no closed trades")).toBeInTheDocument(); // under the win rate
    expect(screen.queryByText(/0[.,]?0* ?%/)).not.toBeInTheDocument();
    expect(screen.queryByText(/^0 %|^0\.00 %/)).not.toBeInTheDocument();
  });

  it("shows the counts, the win rate and the drawdown from the server", async () => {
    serve(summary({
      trades: 4, wins: 2, losses: 1, breakeven: 1, winRateBp: 5000, grossCents: 1200, feesCents: 200, netCents: 1000, foreignTrades: 3,
      drawdown: { known: true, peakCents: 1000000, maxDrawdownCents: 25000, maxDrawdownBp: 250, currentCents: 1000, currentBp: 10 },
    }));
    expect(await screen.findByText("2 / 1 / 1")).toBeInTheDocument();
    expect(screen.getByText("50.00 %")).toBeInTheDocument();
    expect(screen.getByText("$10.00")).toBeInTheDocument(); // net
    expect(screen.getByText("$250.00")).toBeInTheDocument(); // max drawdown
    expect(screen.getByText(/3 more not opened by cactusTrade/)).toBeInTheDocument();
  });

  it("shows unknown fees as unknown, never as $0.00", async () => {
    serve(
      summary({ trades: 1, wins: 1, winRateBp: 10000, netCents: 150, grossCents: 150, feesUnknownTrades: 1 }),
      [trade({ feesKnown: false, feesCents: undefined })],
      daily([{ day: "2026-11-21", trades: 1, realisedCents: 150, feesCents: 0, feesUnknownTrades: 1, equityChangeCents: 900 }]),
    );
    await screen.findByText("AAPL");
    const unknown = screen.getAllByText("unknown");
    expect(unknown.length).toBeGreaterThanOrEqual(3); // summary card, daily row, trade row
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
  });

  it("puts the account and the period on every export link", async () => {
    serve(summary());
    await screen.findByText("No closed trades in this period.");
    for (const name of ["trades", "daily figures", "equity snapshots", "orders", "decisions"]) {
      const a = screen.getByRole("link", { name: `Download ${name} as CSV` });
      const u = new URL(a.getAttribute("href") ?? "", "https://x");
      expect(u.pathname).toMatch(/^\/api\/v1\/history\/export\/[a-z]+\.csv$/);
      expect(u.searchParams.get("accountId")).toBe("a1");
      expect(u.searchParams.get("from")).toBe(FROM);
      expect(u.searchParams.get("to")).toBe(TO);
      expect(a).toHaveAttribute("download");
    }
  });

  it("uses the chosen dates for the trades and the exports", async () => {
    serve(summary());
    await screen.findByText("No closed trades in this period.");
    fireEvent.change(screen.getByLabelText("Closed from"), { target: { value: "2026-11-01" } });
    fireEvent.change(screen.getByLabelText("Closed until (inclusive)"), { target: { value: "2026-11-10" } });
    const from = new Date(2026, 10, 1).toISOString();
    const to = new Date(2026, 10, 11).toISOString();
    await waitFor(() => expect(urls.some((u) => u.includes("/history/trades") && u.includes(encodeURIComponent(from)) && u.includes(encodeURIComponent(to)))).toBe(true));
    const u = new URL(screen.getByRole("link", { name: "Download trades as CSV" }).getAttribute("href") ?? "", "https://x");
    expect(u.searchParams.get("from")).toBe(from);
    expect(u.searchParams.get("to")).toBe(to);
  });

  it("lists cactusTrade's trades by default, all on request, and pages with before", async () => {
    const page = Array.from({ length: 50 }, (_, i) => trade({ positionId: 100 + i, closedAt: `2026-11-20T${String(10 + (i % 10)).padStart(2, "0")}:00:00Z` }));
    serve(summary({ trades: 51, wins: 51, winRateBp: 10000 }), (url) => (url.includes("before=") ? [trade({ positionId: 1, symbol: "OLDEST" })] : page));
    await screen.findAllByText("AAPL");
    expect(urls.find((u) => u.includes("/history/trades"))).toContain("ours=true");
    fireEvent.click(screen.getByRole("button", { name: /Older trades/ }));
    expect(await screen.findByText("OLDEST")).toBeInTheDocument();
    expect(urls.filter((u) => u.includes("/history/trades")).pop()).toContain("before=" + encodeURIComponent(page[49].closedAt));
    expect(screen.queryByRole("button", { name: /Older trades/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    await waitFor(() => expect(urls.filter((u) => u.includes("/history/trades")).pop()).not.toContain("ours="));
  });

  it("asks for the chosen period", async () => {
    serve(summary());
    await screen.findByText("No closed trades in this period.");
    fireEvent.click(screen.getByRole("button", { name: "3M" }));
    await waitFor(() => expect(urls.some((u) => u.includes("/history/summary") && u.includes("days=90"))).toBe(true));
    expect(urls.some((u) => u.includes("/history/daily") && u.includes("days=90"))).toBe(true);
  });
});
