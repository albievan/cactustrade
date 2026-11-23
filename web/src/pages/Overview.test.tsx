import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { Overview } from "./Overview";

const acct = (id: string, name: string, platform: string, mode = "demo") => ({
  id, name, ownerId: "1", ownerName: "admin", mode, platform, envKeys: false, access: "owner", createdAt: "2026-11-23T09:00:00Z",
});

const overview = {
  asOf: "2026-11-23T15:00:00Z",
  totals: [
    { currency: "GBP", accounts: 1, cash: 12345, equity: 384547, unrealisedPnl: 10040 },
    { currency: "USD", accounts: 1, cash: 100000, equity: 150000, unrealisedPnl: -2500 },
  ],
  accounts: [
    {
      account: acct("e1", "Main demo", "etoro"), noKeys: false,
      holdings: { platform: "etoro", currency: "USD", cash: 100000, invested: 52500, equity: 150000, unrealisedPnl: -2500, realisedPnl: 0, asOf: "2026-11-23T15:00:00Z",
        positions: [{ symbol: "NVDA", name: "NVIDIA", currency: "USD", quantity: "2", avgPrice: "180", currentPrice: "175", value: 35000, cost: 36000, pnl: -1000, openedAt: "2026-11-23T14:00:00Z", long: true, cfd: false }] },
    },
    {
      account: acct("t1", "Stocks ISA", "trading212", "real"), noKeys: false,
      holdings: { platform: "trading212", currency: "GBP", cash: 12345, invested: 250000, equity: 384547, unrealisedPnl: 10040, realisedPnl: -1235, asOf: "2026-11-23T15:00:00Z",
        positions: [{ symbol: "AAPL_US_EQ", name: "Apple", currency: "USD", quantity: "2.5", avgPrice: "180.123456", currentPrice: "190.5", value: 37512, cost: 35500, pnl: 2012, openedAt: "2026-11-20T14:31:05Z", long: true, cfd: false }] },
    },
    { account: acct("t2", "Fresh ISA", "trading212"), noKeys: true, error: "no keys yet" },
    { account: acct("t3", "Broken ISA", "trading212"), noKeys: false, error: "Trading 212 account_summary: HTTP 401: the API key or secret was refused" },
  ],
};

const series = [
  { accountId: "e1", name: "Main demo", platform: "etoro", mode: "demo", currency: "USD",
    points: [{ at: "2026-11-23T14:30:00Z", cash: 100000, equity: 149000 }, { at: "2026-11-23T14:45:00Z", cash: 100000, equity: 150000 }] },
  { accountId: "t1", name: "Stocks ISA", platform: "trading212", mode: "real", currency: "GBP",
    points: [{ at: "2026-11-23T14:45:00Z", cash: 12345, equity: 384547 }] },
];

function mockApi(history: unknown[]) {
  const urls: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    urls.push(url);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/overview")) return json(overview);
    if (url.includes("/api/v1/overview/history")) return json(history);
    return new Response("{}", { status: 404 });
  });
  return urls;
}

describe("Overview", () => {
  it("shows totals per currency, every account, the chart and all positions", async () => {
    const urls = mockApi(series);
    render(<MemoryRouter><Overview /></MemoryRouter>);
    expect(await screen.findByText("Total in GBP · 1 account")).toBeInTheDocument();
    expect(screen.getByText("Total in USD · 1 account")).toBeInTheDocument();
    expect(screen.getByText("Amounts in different currencies are not added up.")).toBeInTheDocument();
    expect(screen.getAllByText("£3,845.47").length).toBeGreaterThanOrEqual(2); // the GBP total, the ISA card (and maybe an axis label)
    // account cards: no keys links to the accounts page, an error is shown as such
    expect(screen.getByText(/No keys yet/)).toBeInTheDocument();
    expect(screen.getByText(/HTTP 401: the API key or secret was refused/)).toBeInTheDocument();
    // one table of positions across platforms
    const table = screen.getByRole("table");
    expect(within(table).getByText("AAPL_US_EQ")).toBeInTheDocument();
    expect(within(table).getByText("NVDA")).toBeInTheDocument();
    expect(within(table).getAllByText("Trading 212").length).toBe(1);
    expect(within(table).getByText("+£20.12")).toBeInTheDocument();
    // one chart per currency, defaulting to a month
    expect(await screen.findByLabelText("Equity in GBP over time")).toBeInTheDocument();
    expect(screen.getByLabelText("Equity in USD over time")).toBeInTheDocument();
    expect(urls.some((u) => u.endsWith("/api/v1/overview/history?days=30"))).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "1W" }));
    await waitFor(() => expect(urls.some((u) => u.endsWith("days=7"))).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    await waitFor(() => expect(urls.some((u) => u.endsWith("days=400"))).toBe(true));
  });

  it("explains the empty chart", async () => {
    mockApi([]);
    render(<MemoryRouter><Overview /></MemoryRouter>);
    expect(await screen.findByText("The chart fills in as snapshots are recorded every 15 minutes.")).toBeInTheDocument();
  });
});
