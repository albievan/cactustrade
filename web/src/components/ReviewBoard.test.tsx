import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { ReviewBoard } from "./ReviewBoard";
import { PrefsStore, PrefsStoreProvider } from "../lib/prefs";

function review(id: number, symbol: string, market: string, exchange: string, outcome = "no_signal") {
  return { instrumentId: id, symbol, theme: "AI", at: "2026-11-26T15:00:00Z", outcome, checks: [{ rule: "regime", pass: true }], summary: `${symbol} summary`, market, exchange };
}

const board = {
  scannerEnabled: true, canAccept: true,
  reviews: [
    review(1, "AAPL", "NYSE", "New York (NYSE/NASDAQ)", "candidate"),
    review(2, "MSFT", "NYSE", "New York (NYSE/NASDAQ)"),
    review(3, "BARC.L", "LSE", "London (LSE)"),
    review(4, "CBA.ASX", "ASX", "Sydney (ASX)", "added"),
    review(5, "ODD", "", ""),
  ],
};

let urls: string[] = [];
beforeEach(() => {
  urls = [];
  window.localStorage.clear();
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    urls.push(String(input));
    return new Response(JSON.stringify(board), { status: 200 });
  });
});
afterEach(() => vi.restoreAllMocks());

let store: PrefsStore;
const mount = (prefs: Record<string, unknown> = {}) => {
  store = new PrefsStore("u1", prefs);
  return render(<MemoryRouter><PrefsStoreProvider store={store}><ReviewBoard /></PrefsStoreProvider></MemoryRouter>);
};
afterEach(() => store?.dispose());
const symbols = () => within(screen.getByRole("tabpanel")).getAllByRole("row").slice(1).map((r) => (r as HTMLTableRowElement).cells[0].textContent);

describe("ReviewBoard", () => {
  it("has a tab per exchange, in order, with counts and signals", async () => {
    mount();
    expect(await screen.findByText("BARC.L")).toBeInTheDocument();
    const tabs = within(screen.getByRole("tablist", { name: "Exchanges" })).getAllByRole("tab").map((t) => t.textContent);
    expect(tabs).toEqual(["All 5", "London (LSE) 1", "New York (NYSE/NASDAQ) 21", "Sydney (ASX) 11", "Other 1"]);
    expect(symbols()).toHaveLength(5);
    expect(urls.some((u) => u.includes("limit=200"))).toBe(true);
  });

  it("shows only the chosen exchange's reviews and remembers the tab", async () => {
    mount();
    await screen.findByText("BARC.L");
    fireEvent.click(screen.getByRole("tab", { name: /New York/ }));
    expect(screen.getByRole("tab", { name: /New York/ })).toHaveAttribute("aria-selected", "true");
    expect(symbols()).toEqual(["AAPL", "MSFT"]);
    expect(within(screen.getByRole("tabpanel")).queryByText("BARC.L")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: /Sydney/ }));
    expect(symbols()).toEqual(["CBA.ASX"]);
    expect(store.get("dashboard.reviews.tab")).toBe("ASX"); // R50: the user's preference
    fireEvent.click(screen.getByRole("tab", { name: /Other/ }));
    expect(symbols()).toEqual(["ODD"]);
    fireEvent.click(screen.getByRole("tab", { name: /All/ }));
    expect(symbols()).toHaveLength(5);
  });

  it("opens on the tab chosen at the last sign-in", async () => {
    mount({ "dashboard.reviews.tab": "LSE" });
    await screen.findByText("BARC.L");
    expect(screen.getByRole("tab", { name: /London/ })).toHaveAttribute("aria-selected", "true");
    expect(symbols()).toEqual(["BARC.L"]);
  });

  it("falls back to All when the remembered exchange has nothing under review", async () => {
    mount({ "dashboard.reviews.tab": "nowhere" });
    await screen.findByText("BARC.L");
    expect(screen.getByRole("tab", { name: /All/ })).toHaveAttribute("aria-selected", "true");
  });
});
