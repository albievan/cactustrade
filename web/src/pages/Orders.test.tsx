import { render, screen, within } from "@testing-library/react";
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
    expect(await screen.findByText("AAPL")).toBeInTheDocument();
    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(screen.getByText("$2,000.00")).toBeInTheDocument();
    expect(screen.getByText("103")).toBeInTheDocument();
    expect(screen.getByText("filled")).toBeInTheDocument();
    expect(screen.getByText("Short AAPL")).toBeInTheDocument(); // the server's wording
    expect(screen.getByText("Close request for instrument 1002 (position 5)")).toBeInTheDocument();
    expect(screen.getByText("1002")).toBeInTheDocument(); // no symbol cached: the id
    expect(screen.getByText("answer lost")).toBeInTheDocument();
    expect(screen.getByLabelText("Refresh the orders")).toBeInTheDocument();
  });

  it("shows a cancel request eToro took as accepted, never filled", async () => {
    serve([
      { requestId: "33333333-3333-4333-8333-333333333333", mode: "demo", kind: "cancel", kindLabel: "Cancel request for GOOG (buy order 396359836)",
        direction: "long", instrumentId: 1002, symbol: "GOOG", amountCents: 0, state: "filled", stateLabel: "accepted", reason: "", sends: 1,
        createdAt: "2026-11-24T18:51:22Z", updatedAt: "2026-11-24T18:51:22Z" },
    ]);
    expect(await screen.findByText("Cancel request for GOOG (buy order 396359836)")).toBeInTheDocument();
    const table = screen.getByRole("table");
    expect(within(table).getByText("accepted")).toBeInTheDocument();
    expect(within(table).queryByText("filled")).not.toBeInTheDocument();
  });

  it("says when there are none", async () => {
    serve([]);
    expect(await screen.findByText("No orders yet.")).toBeInTheDocument();
  });
});
