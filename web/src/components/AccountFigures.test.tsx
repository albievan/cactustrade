import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AccountFigures } from "./AccountFigures";
import type { AccountHoldings } from "../lib/api";

const account = { id: "a1", name: "Money Maker", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "" };
const holdings = (over: Partial<AccountHoldings> = {}) => ({
  account, noKeys: false,
  holdings: {
    platform: "etoro", currency: "USD", cash: 9_800_000, invested: 200_000, equity: 10_005_715, unrealisedPnl: -1_250, realisedPnl: 0,
    positions: [], asOf: "2026-11-27T18:00:00Z",
    summary: { longs: 3, shorts: 1, longValue: 150_000, shortValue: 50_000, longPnl: -2_000, shortPnl: 750, unknownValues: 0 },
  },
  ...over,
}) as unknown as AccountHoldings;

describe("AccountFigures", () => {
  it("shows the balance, shares held long, shorts and P&L from the server's summary", () => {
    render(<AccountFigures h={holdings()} />);
    const t = screen.getByRole("table", { name: "Figures of Money Maker" });
    expect(t).toHaveTextContent("BalanceUS$98,000.00 cashUS$100,057.15 equity");
    expect(t).toHaveTextContent("Shares held (long)3 positionsUS$1,500.00 · P&L -US$20.00");
    expect(t).toHaveTextContent("Shorts held1 positionUS$500.00 · P&L US$7.50");
    expect(t).toHaveTextContent("P&Lopen -US$12.50");
    expect(t).not.toHaveTextContent("realised");
  });

  it("says why there are no figures, and nothing without keys", () => {
    const { rerender, container } = render(<AccountFigures h={undefined} />);
    expect(screen.getByText("Figures loading…")).toBeInTheDocument();
    rerender(<AccountFigures h={{ account, noKeys: false, error: "eToro did not answer" } as unknown as AccountHoldings} />);
    expect(screen.getByText("Figures: eToro did not answer")).toBeInTheDocument();
    rerender(<AccountFigures h={{ account, noKeys: true } as unknown as AccountHoldings} />);
    expect(container).toBeEmptyDOMElement();
  });
});
