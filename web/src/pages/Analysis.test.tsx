import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import type { Analysis as AnalysisData, AnalysisGroup, AnalysisTradeRow } from "../lib/api";
import { Analysis, PAST_ONLY } from "./Analysis";

const acct = (id: string, name: string, platform = "etoro") => ({
  id, name, ownerId: "1", ownerName: "admin", mode: "demo", platform, envKeys: false, access: "owner", createdAt: "2026-11-23T09:00:00Z",
});

function group(key: string, label: string, over: Partial<AnalysisGroup> = {}): AnalysisGroup {
  return { key, label, trades: 0, wins: 0, losses: 0, breakeven: 0, grossCents: 0, feesCents: 0, netCents: 0, feesUnknownTrades: 0, ...over };
}

const trade = (net: number, symbol: string) => ({ account: "Demo main", symbol, positionId: 1, closedAt: "2026-11-02T15:00:00Z", netCents: net });

const row = (pos: number, symbol: string, exchange: string, net: number, outcome: "Win" | "Loss" | "Breakeven", over: Partial<AnalysisTradeRow> = {}): AnalysisTradeRow => ({
  accountId: "a1", account: "Demo main", positionId: pos, symbol, exchange, theme: "AI", playbook: "trend_breakout", direction: "long",
  openedAt: "2026-11-02T14:00:00Z", closedAt: "2026-11-02T15:00:00Z", openRate: "100", closeRate: "101", investmentCents: 10000,
  netCents: net, feesCents: 50, feesKnown: true, outcome, ours: true, ...over,
});

const trades: AnalysisTradeRow[] = [
  row(21, "CBA.ASX", "ASX", 6000, "Win", { closedAt: "2026-11-20T03:00:00Z", theme: "No theme", feesCents: 200 }),
  row(4, "TSLA", "NYSE", 9900, "Win", { ours: false, closedAt: "2026-11-10T16:00:00Z", theme: "No theme", playbook: "Not recorded" }),
  row(3, "AAPL", "NYSE", 0, "Breakeven", { direction: "short", closedAt: "2026-11-10T15:00:00Z", playbook: "Not recorded" }),
  row(2, "BARC.L", "LSE", -4000, "Loss", { closedAt: "2026-11-02T16:00:00Z", feesCents: 0, feesKnown: false, playbook: "Not recorded" }),
  row(1, "AAPL", "NYSE", 2500, "Win"),
];

function analysis(over: Partial<AnalysisData> = {}): AnalysisData {
  const lse = group("LSE", "LSE", { trades: 1, losses: 1, winRateBp: 0, netCents: -4000, grossCents: -4000, feesUnknownTrades: 1, avgNetCents: -4000, best: trade(-4000, "BARC.L"), worst: trade(-4000, "BARC.L") });
  const nyse = group("NYSE", "NYSE", { trades: 2, wins: 1, breakeven: 1, winRateBp: 5000, netCents: 2500, grossCents: 2650, feesCents: 150, avgNetCents: 1250, best: trade(2500, "AAPL"), worst: trade(0, "AAPL") });
  const asx = group("ASX", "ASX", { trades: 1, wins: 1, winRateBp: 10000, netCents: 6000, grossCents: 6200, feesCents: 200, avgNetCents: 6000, best: trade(6000, "CBA.ASX"), worst: trade(6000, "CBA.ASX") });
  const totals = group("all", "All trades", { trades: 4, wins: 2, losses: 1, breakeven: 1, winRateBp: 5000, netCents: 4500, grossCents: 4850, feesCents: 350, feesUnknownTrades: 1, avgNetCents: 1125, best: trade(6000, "CBA.ASX"), worst: trade(-4000, "BARC.L") });
  return {
    preset: "month", from: "2026-10-31T22:00:00Z", to: "2026-11-30T22:00:00Z", firstDay: "2026-11-01", lastDay: "2026-11-30", days: 30, bucket: "day",
    title: "November 2026", generatedAt: "2026-11-24T15:00:00Z", allAccounts: true, scope: "All 2 accounts you can see",
    accounts: [{ id: "a1", name: "Demo main", platform: "eToro", mode: "demo", viewOnly: false }, { id: "t1", name: "ISA", platform: "Trading 212", mode: "real", viewOnly: true }],
    totals, foreignTrades: 1, oursShareBp: 8000, tradeAccounts: 1,
    groupings: [
      { dimension: "date", title: "By day", groups: [group("2026-11-02", "Mon 2 Nov", { trades: 2, netCents: -1500 }), group("2026-11-03", "Tue 3 Nov"), group("2026-11-20", "Fri 20 Nov", { trades: 2, netCents: 6000 })] },
      { dimension: "exchange", title: "By exchange", groups: [lse, nyse, asx] },
      { dimension: "account", title: "By trading account", groups: [{ ...totals, key: "a1", label: "Demo main" }] },
      { dimension: "theme", title: "By theme", groups: [group("ai", "AI", { trades: 2, netCents: 2500 }), group("no theme", "No theme", { trades: 2, netCents: 2000 })] },
      { dimension: "direction", title: "By direction", groups: [group("long", "Long", { trades: 4, netCents: 4500 })] },
      { dimension: "playbook", title: "By playbook", groups: [group("trend_breakout", "trend_breakout", { trades: 2, netCents: 8500 }), group("Not recorded", "Not recorded", { trades: 2, netCents: -4000 })] },
      { dimension: "outcome", title: "By outcome", groups: [group("win", "Wins", { trades: 2, netCents: 8500 }), group("loss", "Losses", { trades: 1, netCents: -4000 }), group("breakeven", "Breakeven", { trades: 1 })] },
    ],
    equity: [
      { account: "Demo main", currency: "USD", known: true, openingMinor: 1000000, closingMinor: 1010000, changeMinor: 10000, maxDrawdownMinor: 30000, maxDrawdownBp: 294, viewOnly: false },
      { account: "ISA", currency: "GBP", known: true, openingMinor: 100000, closingMinor: 105000, changeMinor: 5000, maxDrawdownMinor: 0, maxDrawdownBp: 0, viewOnly: true },
    ],
    equityTotals: [
      { currency: "GBP", accounts: 1, openingMinor: 100000, closingMinor: 105000, changeMinor: 5000 },
      { currency: "USD", accounts: 1, openingMinor: 1000000, closingMinor: 1010000, changeMinor: 10000 },
    ],
    curves: [
      { currency: "GBP", accounts: 1, maxDrawdownMinor: 0, maxDrawdownBp: 0, points: [{ at: "2026-11-03T09:00:00Z", equity: 100000 }, { at: "2026-11-15T09:00:00Z", equity: 105000 }] },
      { currency: "USD", accounts: 1, maxDrawdownMinor: 30000, maxDrawdownBp: 294, points: [{ at: "2026-11-02T09:00:00Z", equity: 1000000 }, { at: "2026-11-20T09:00:00Z", equity: 1010000 }] },
    ],
    risk: { killSwitches: 1, killResets: 1, dailyStops: 2, limitChanges: 0, forceCloses: 0, riskRefusals: 3 },
    decisions: [{ label: "Order sent", n: 2 }], decisionsTotal: 2, verdicts: [{ label: "Approved", n: 2 }],
    llm: { calls: 3, failed: 0, inputTokens: 100, outputTokens: 10, knownCostUsd: "0.12", costUnknownCalls: 0 },
    activityCapped: false,
    narrative: [
      { heading: "Overview", paragraphs: ["Period: November 2026 (this month, 30 risk days). cactusTrade closed 4 trades on 1 account."] },
      { heading: "What went well", paragraphs: ["2 trades of 4 closed with a positive result."] },
      { heading: "What went badly", paragraphs: ["1 trade of 4 closed with a negative result."] },
      { heading: "Costs", paragraphs: ["eToro reported $3.50 of fees on 3 trades."] },
      { heading: "Risk & discipline", paragraphs: ["In the period the kill switch was used once."] },
      { heading: "Activity", paragraphs: ["The agent logged 2 decisions.", "Past results say nothing about future results."] },
    ],
    notes: ["Fees eToro did not report are shown as unknown and are never counted as zero."],
    trades, tradesCapped: false,
    ...over,
  };
}

let urls: string[] = [];

function serve(a: AnalysisData | ((url: string) => AnalysisData | Response), accounts = [acct("a1", "Demo main"), acct("t1", "ISA", "trading212")]) {
  urls = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    urls.push(url);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/accounts")) return json(accounts);
    if (url.includes("/api/v1/analysis?")) {
      const v = typeof a === "function" ? a(url) : a;
      return v instanceof Response ? v : json(v);
    }
    return new Response(JSON.stringify({ title: "Not found", status: 404 }), { status: 404 });
  });
  render(<MemoryRouter><Analysis /></MemoryRouter>);
}

const analysisUrls = () => urls.filter((u) => u.includes("/api/v1/analysis?"));

afterEach(() => vi.restoreAllMocks());

describe("Analysis", () => {
  it("shows this month's figures, the narrative, the charts and a table per grouping", async () => {
    serve(analysis());
    expect(await screen.findByText("What happened")).toBeInTheDocument();
    expect(analysisUrls()[0]).toContain("preset=month");
    expect(screen.getByText(PAST_ONLY)).toBeInTheDocument();
    for (const h of ["Overview", "What went well", "What went badly", "Costs", "Risk & discipline", "Activity"]) {
      expect(screen.getByRole("heading", { name: h })).toBeInTheDocument();
    }
    expect(screen.getByText("Past results say nothing about future results.")).toBeInTheDocument();
    expect(screen.getAllByText("$45.00").length).toBeGreaterThan(0); // realised P&L
    expect(screen.getAllByText("50.00 %").length).toBeGreaterThan(0); // win rate
    expect(screen.getByText("80.00 %")).toBeInTheDocument(); // opened by cactusTrade
    expect(screen.getByText(/1 more not opened by cactusTrade/)).toBeInTheDocument();
    expect(screen.getByText("unknown for 1 trade")).toBeInTheDocument();
    for (const t of ["By day (days with trades)", "By exchange", "By trading account", "By theme", "By direction", "By playbook", "By outcome"]) {
      expect(screen.getByRole("heading", { name: t })).toBeInTheDocument();
    }
    // empty date buckets are drawn in the chart but not listed in the table
    const days = screen.getByRole("heading", { name: "By day (days with trades)" }).closest(".card") as HTMLElement;
    expect(within(days).queryByText("Tue 3 Nov")).not.toBeInTheDocument();
    expect(screen.getByRole("img", { name: /Realised P&L per period/ })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Equity in USD over time" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Equity in GBP over time" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "2 wins, 1 losses and 1 breakeven of 4 trades" })).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Realised P&L by exchange" })).toBeInTheDocument();
    expect(screen.getByText("Total GBP (1 account)")).toBeInTheDocument();
    expect(screen.getByText("Total USD (1 account)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /How these figures are computed/ })).toBeInTheDocument();
    expect(screen.getByText(/never added together \(there is no currency conversion\)/)).toBeInTheDocument();
  });

  it("changes the period, the accounts and the PDF link", async () => {
    serve(analysis());
    await screen.findByText("What happened");
    fireEvent.click(screen.getByRole("button", { name: "This week" }));
    await waitFor(() => expect(analysisUrls().at(-1)).toContain("preset=week"));
    fireEvent.click(screen.getByRole("button", { name: "Demo main" }));
    fireEvent.click(screen.getByRole("button", { name: "ISA" }));
    await waitFor(() => expect(analysisUrls().at(-1)).toContain("preset=week&accountId=a1&accountId=t1"));
    expect(screen.getByRole("button", { name: "Demo main" })).toHaveAttribute("aria-pressed", "true");
    const pdf = screen.getByRole("link", { name: "Export PDF" });
    expect(pdf).toHaveAttribute("href", "/api/v1/analysis.pdf?preset=week&accountId=a1&accountId=t1");
    expect(pdf).toHaveAttribute("title", "Export this analysis as a one-page PDF");
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    await waitFor(() => expect(analysisUrls().at(-1)).toMatch(/preset=week$/));
  });

  it("asks for a custom range before loading it", async () => {
    serve(analysis());
    await screen.findByText("What happened");
    const before = analysisUrls().length;
    fireEvent.click(screen.getByRole("button", { name: "Custom" }));
    expect(screen.queryByRole("link", { name: "Export PDF" })).not.toBeInTheDocument();
    const go = screen.getByRole("button", { name: "Show this range" });
    expect(go).toBeDisabled();
    fireEvent.change(screen.getByLabelText("First risk day"), { target: { value: "2026-11-01" } });
    fireEvent.change(screen.getByLabelText("Last risk day (inclusive)"), { target: { value: "2026-11-20" } });
    fireEvent.click(go);
    await waitFor(() => expect(analysisUrls().at(-1)).toContain("preset=custom&from=2026-11-01&to=2026-11-20"));
    expect(analysisUrls().length).toBe(before + 1);
  });

  it("sorts a grouping's table by a figure", async () => {
    serve(analysis());
    await screen.findByText("What happened");
    const card = screen.getByRole("heading", { name: "By exchange" }).closest(".card") as HTMLElement;
    const order = () => within(card).getAllByRole("row").slice(1).map((r) => (r as HTMLTableRowElement).cells[0].textContent);
    expect(order()).toEqual(["LSE", "NYSE", "ASX"]);
    fireEvent.click(within(card).getByRole("button", { name: "Realised P&L" }));
    expect(order()).toEqual(["ASX", "NYSE", "LSE"]);
    expect(within(card).getAllByRole("columnheader")[3]).toHaveAttribute("aria-sort", "descending");
    fireEvent.click(within(card).getByRole("button", { name: "Realised P&L" }));
    expect(order()).toEqual(["LSE", "NYSE", "ASX"]);
    fireEvent.click(within(card).getByRole("button", { name: "Group" }));
    expect(order()).toEqual(["ASX", "LSE", "NYSE"]);
    expect(within(card).getByText(/1 unknown/)).toBeInTheDocument();
  });

  it("shows an empty state, and no 0 % win rate, without trades", async () => {
    const empty = group("all", "All trades");
    serve(analysis({
      totals: empty, foreignTrades: 0, oursShareBp: undefined, curves: [], equity: [], equityTotals: [],
      groupings: [{ dimension: "date", title: "By day", groups: [group("2026-11-24", "Tue 24 Nov")] }, { dimension: "exchange", title: "By exchange", groups: [] }],
    }));
    expect((await screen.findAllByText("No closed trades in this period.")).length).toBeGreaterThan(0);
    expect(screen.getByText("no closed trades")).toBeInTheDocument();
    expect(screen.queryByText(/^0\.00 %$/)).not.toBeInTheDocument();
    expect(screen.getByText("not recorded")).toBeInTheDocument(); // drawdown
    expect(screen.getAllByText("No trades.").length).toBeGreaterThan(0);
  });

  it("drills into a tile: its breakdown and the trades behind it", async () => {
    serve(analysis());
    const tile = await screen.findByTitle(/details behind “Closed trades/);
    expect(tile).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(tile);
    expect(tile).toHaveAttribute("aria-expanded", "true");
    const panel = screen.getByRole("region", { name: "Details: Closed trades" });
    const table = within(panel).getByRole("table", { name: "cactusTrade's closed trades" });
    // cactusTrade's 4 trades, newest first; the foreign TSLA trade is not among them
    const shares = within(table).getAllByRole("row").slice(2).map((r) => (r as HTMLTableRowElement).cells[2].textContent);
    expect(shares).toEqual(["CBA.ASX", "AAPL", "BARC.L", "AAPL"]);
    expect(within(table).getByText("unknown")).toBeInTheDocument(); // BARC.L's fees
    fireEvent.change(within(panel).getByLabelText("Filter trades by Exchange"), { target: { value: "NYSE" } });
    expect(within(panel).getByText("Showing 2 of 4")).toBeInTheDocument();
    // closing it
    fireEvent.click(within(panel).getByRole("button", { name: "Close the details" }));
    expect(screen.queryByRole("region", { name: /Details/ })).not.toBeInTheDocument();
  });

  it("drills into the win rate by any grouping, and into P&L best first", async () => {
    serve(analysis());
    fireEvent.click(await screen.findByTitle(/details behind “Win rate/));
    let panel = screen.getByRole("region", { name: "Details: Win rate" });
    const byExchange = within(panel).getByRole("table", { name: "By exchange" });
    expect(within(byExchange).getByText("50.00 %")).toBeInTheDocument(); // NYSE
    fireEvent.click(within(panel).getByRole("button", { name: "By playbook" }));
    expect(within(panel).getByRole("table", { name: "By playbook" })).toBeInTheDocument();
    expect(within(panel).getByRole("button", { name: "Result: Win" })).toBeInTheDocument(); // trades grouped by result
    // another tile replaces the panel
    fireEvent.click(screen.getByTitle(/details behind “Realised P&L/));
    panel = screen.getByRole("region", { name: "Details: Realised P&L (after fees)" });
    const table = within(panel).getByRole("table", { name: "cactusTrade's closed trades" });
    const nets = within(table).getAllByRole("row").slice(2).map((r) => (r as HTMLTableRowElement).cells[12].textContent);
    expect(nets).toEqual(["$60.00", "$25.00", "$0.00", "-$40.00"]);
  });

  it("drills into who opened the trades, the drawdown and the LLM use", async () => {
    serve(analysis({ llm: { calls: 181, failed: 2, inputTokens: 1000, outputTokens: 100, knownCostUsd: "0", costUnknownCalls: 181 } }));
    fireEvent.click(await screen.findByTitle(/details behind “Opened by cactusTrade/));
    let panel = screen.getByRole("region", { name: "Details: Opened by cactusTrade" });
    expect(within(panel).getByRole("button", { name: "Opened by: not cactusTrade (not counted)" })).toBeInTheDocument();
    expect(within(panel).getByRole("cell", { name: "TSLA" })).toBeInTheDocument();
    fireEvent.click(screen.getByTitle(/details behind “Largest drawdown/));
    panel = screen.getByRole("region", { name: "Details: Largest drawdown" });
    expect(within(panel).getByText(/Recorded equity/)).toBeInTheDocument();
    fireEvent.click(screen.getByTitle(/details behind “LLM reviewer cost/));
    panel = screen.getByRole("region", { name: "Details: LLM reviewer cost" });
    expect(within(panel).getByRole("link", { name: "LLM reviewer" })).toHaveAttribute("href", "/llm");
    expect(within(panel).getByRole("link", { name: "Decision log" })).toHaveAttribute("href", "/decisions");
  });

  it("shows a refused request", async () => {
    serve(() => new Response(JSON.stringify({ title: "Not found", status: 404, detail: "account: not found" }), { status: 404 }));
    expect(await screen.findByRole("alert")).toHaveTextContent("account: not found");
  });
});
