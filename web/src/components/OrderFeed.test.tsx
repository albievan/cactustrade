import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Order } from "../lib/api";
import { OrderFeed } from "./LiveCards";

function order(over: Partial<Order>): Order {
  return {
    requestId: "r", mode: "demo", kind: "open", kindLabel: "Buy AAPL", direction: "long", instrumentId: 1001, symbol: "AAPL", amountCents: 50000,
    state: "filled", stateLabel: "filled", reason: "", sends: 1, createdAt: "2026-11-24T15:00:00Z", updatedAt: "2026-11-24T15:00:00Z", ...over,
  };
}

const cancel = (id: string, at: string) => order({
  requestId: id, kind: "cancel", kindLabel: "Cancel request for GOOG (buy order 396359836)", symbol: "GOOG",
  stateLabel: "accepted by eToro", amountCents: 0, updatedAt: at, createdAt: at,
});

describe("OrderFeed", () => {
  const orders = [
    order({ requestId: "s1", kindLabel: "Short TSLA", direction: "short", symbol: "TSLA" }),
    cancel("c3", "2026-11-24T14:52:00Z"), cancel("c2", "2026-11-24T14:51:00Z"), cancel("c1", "2026-11-24T14:50:00Z"),
    order({ requestId: "l1", kindLabel: "Buy AAPL" }),
  ];

  it("shows each order's direction and folds identical requests in a row", () => {
    render(<OrderFeed orders={orders} />);
    const feed = screen.getByRole("list", { name: "Orders feed" });
    const lines = within(feed).getAllByRole("listitem");
    expect(lines).toHaveLength(3); // the 3 cancels are one line
    expect(within(lines[0]).getByText("Short")).toBeInTheDocument();
    expect(within(lines[1]).getByText("×3")).toBeInTheDocument();
    expect(within(lines[1]).getByText("accepted by eToro")).toBeInTheDocument();
    expect(within(lines[2]).getByText("Long")).toBeInTheDocument();
  });

  it("shows long or short orders only", () => {
    render(<OrderFeed orders={orders} />);
    const pick = screen.getByRole("group", { name: "Show orders by direction" });
    expect(within(pick).getByRole("button", { name: /All/ })).toHaveTextContent("5");
    fireEvent.click(within(pick).getByRole("button", { name: /Short/ }));
    const feed = screen.getByRole("list", { name: "Orders feed" });
    expect(within(feed).getAllByRole("listitem")).toHaveLength(1);
    expect(within(feed).getByText("Short TSLA")).toBeInTheDocument();
    fireEvent.click(within(pick).getByRole("button", { name: /Long/ }));
    expect(within(screen.getByRole("list", { name: "Orders feed" })).queryByText("Short TSLA")).not.toBeInTheDocument();
  });

  it("says when there are none", () => {
    render(<OrderFeed orders={[]} />);
    expect(screen.getByText("No orders yet.")).toBeInTheDocument();
  });
});
