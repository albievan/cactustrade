import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ModeProvider } from "../lib/mode";
import { Orders } from "./Orders";

const account = { id: "a1", name: "Main demo", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "" };
const status = { version: "0.1.0", build: "1", mode: "demo", agentState: "running", database: "sqlite", startedAt: "", now: "", account };

function serve(orders: unknown[]) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/system/status")) return json(status);
    if (url.includes("/api/v1/orders")) return json(orders);
    return new Response("{}", { status: 404 });
  });
  render(<ModeProvider><Orders /></ModeProvider>);
}

afterEach(() => vi.restoreAllMocks());

describe("Orders", () => {
  it("lists the orders in a table", async () => {
    serve([
      { requestId: "11111111-1111-4111-8111-111111111111", mode: "demo", kind: "open", kindLabel: "Short AAPL", direction: "short", instrumentId: 1001, symbol: "AAPL",
        amountCents: 200000, entry: "100", stopLoss: "103", takeProfit: "94", state: "filled", stateLabel: "filled", reason: "", sends: 1,
        createdAt: "2026-11-24T15:00:00Z", updatedAt: "2026-11-24T15:00:01Z" },
      { requestId: "22222222-2222-4222-8222-222222222222", mode: "demo", kind: "close", kindLabel: "Close request for instrument 1002 (position 5)", direction: "long", instrumentId: 1002, symbol: "",
        amountCents: 0, state: "unknown", stateLabel: "unknown", reason: "answer lost", sends: 1, createdAt: "2026-11-24T14:00:00Z", updatedAt: "2026-11-24T14:00:00Z" },
    ]);
    expect(await screen.findByText("Short AAPL")).toBeInTheDocument(); // the server's wording
    const body = within(screen.getByRole("table").querySelector("tbody")!);
    expect(body.getByText("AAPL")).toBeInTheDocument();
    expect(body.getByText("$2,000.00")).toBeInTheDocument();
    expect(body.getByText("103")).toBeInTheDocument();
    expect(body.getByText("filled")).toBeInTheDocument();
    expect(body.getByText("Short")).toBeInTheDocument(); // the direction badge
    expect(body.getByText("Close request for instrument 1002 (position 5)")).toBeInTheDocument();
    expect(body.getByText("1002")).toBeInTheDocument(); // no symbol cached: the id
    expect(body.getByText("answer lost")).toBeInTheDocument();
    expect(screen.getByLabelText("Refresh the orders")).toBeInTheDocument();
  });

  it("shows a cancel request eToro took as accepted, never filled", async () => {
    serve([
      { requestId: "33333333-3333-4333-8333-333333333333", mode: "demo", kind: "cancel", kindLabel: "Cancel request for GOOG (buy order 396359836)",
        direction: "long", instrumentId: 1002, symbol: "GOOG", amountCents: 0, state: "filled", stateLabel: "accepted by eToro", reason: "eToro accepted the request to cancel order 396359836.", sends: 1,
        createdAt: "2026-11-24T18:51:22Z", updatedAt: "2026-11-24T18:51:22Z" },
    ]);
    expect(await screen.findByText("Cancel request for GOOG (buy order 396359836)")).toBeInTheDocument();
    const body = within(screen.getByRole("table").querySelector("tbody")!);
    expect(body.getByText("accepted by eToro")).toBeInTheDocument();
    expect(body.getByText("eToro accepted the request to cancel order 396359836.")).toBeInTheDocument();
    expect(body.queryByText("filled")).not.toBeInTheDocument();
  });

  const many = [
    { requestId: "a", kind: "open", kindLabel: "Buy AAPL", direction: "long", instrumentId: 1001, symbol: "AAPL", amountCents: 100000,
      state: "filled", stateLabel: "filled", reason: "", createdAt: "2026-11-24T15:00:00Z" },
    { requestId: "b", kind: "open", kindLabel: "Short TSLA", direction: "short", instrumentId: 1111, symbol: "TSLA", amountCents: 50000,
      state: "pending", stateLabel: "pending", reason: "", createdAt: "2026-11-24T16:00:00Z" },
    { requestId: "c", kind: "cancel", kindLabel: "Cancel request for TSLA (short order 9)", direction: "short", instrumentId: 1111, symbol: "TSLA", amountCents: 0,
      state: "filled", stateLabel: "accepted by eToro", reason: "eToro accepted the request to cancel order 9.", createdAt: "2026-11-24T17:00:00Z" },
  ].map((o) => ({ mode: "demo", sends: 1, updatedAt: o.createdAt, ...o }));
  const shares = () => within(screen.getByRole("table")).getAllByRole("row").slice(2).map((r) => (r as HTMLTableRowElement).cells[3]?.textContent);

  it("puts the latest order at the top and sorts by any heading", async () => {
    serve([many[0], many[2], many[1]]);
    await screen.findByText("Buy AAPL");
    expect(shares()).toEqual(["TSLA", "TSLA", "AAPL"]); // newest first whatever the server order
    fireEvent.click(screen.getByRole("button", { name: "Time" }));
    expect(shares()).toEqual(["AAPL", "TSLA", "TSLA"]);
    fireEvent.click(screen.getByRole("button", { name: "Amount" }));
    expect((within(screen.getByRole("table")).getAllByRole("row")[2] as HTMLTableRowElement).cells[0].textContent).toBe(new Date(many[2].createdAt).toLocaleString());
  });

  it("filters by any column and searches them all", async () => {
    serve(many);
    await screen.findByText("Buy AAPL");
    fireEvent.change(screen.getByLabelText("Filter by Direction"), { target: { value: "Short" } });
    expect(shares()).toEqual(["TSLA", "TSLA"]);
    expect(screen.getByText("Showing 2 of 3")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Filter by Kind"), { target: { value: "Cancel" } });
    expect(shares()).toEqual(["TSLA"]);
    fireEvent.click(screen.getByLabelText("Clear the filters"));
    expect(shares()).toHaveLength(3);
    fireEvent.change(screen.getByLabelText("Search all columns"), { target: { value: "aapl" } });
    expect(shares()).toEqual(["AAPL"]);
    fireEvent.change(screen.getByLabelText("Search all columns"), { target: { value: "nothing like it" } });
    expect(screen.getByText("No orders match the filters.")).toBeInTheDocument();
  });

  it("groups by a column, with a summary per group, and folds a group", async () => {
    serve(many);
    await screen.findByText("Buy AAPL");
    fireEvent.change(screen.getByLabelText("Group by"), { target: { value: "direction" } });
    const short = screen.getByRole("button", { name: "Direction: Short" });
    expect(screen.getByRole("button", { name: "Direction: Long" })).toBeInTheDocument();
    expect(screen.getByText("2 orders · 1 open for $500.00 · 0 long, 2 short")).toBeInTheDocument();
    fireEvent.click(short);
    expect(short).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Short TSLA")).not.toBeInTheDocument();
    expect(screen.getByText("Buy AAPL")).toBeInTheDocument();
  });

  it("says when there are none", async () => {
    serve([]);
    expect(await screen.findByText("No orders yet.")).toBeInTheDocument();
  });
});
