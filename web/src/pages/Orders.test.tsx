import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ModeProvider } from "../lib/mode";
import { Orders } from "./Orders";
import { dateTime } from "../lib/format";

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
      { requestId: "11111111-1111-4111-8111-111111111111", mode: "demo", platform: "etoro", resolvable: false, kind: "open", kindLabel: "Short AAPL", direction: "short", instrumentId: 1001, symbol: "AAPL", market: "NYSE", exchange: "New York (NYSE/NASDAQ)",
        amountCents: 200000, entry: "100", stopLoss: "103", takeProfit: "94", state: "filled", stateLabel: "filled", reason: "", sends: 1,
        createdAt: "2026-11-24T15:00:00Z", updatedAt: "2026-11-24T15:00:01Z" },
      { requestId: "22222222-2222-4222-8222-222222222222", mode: "demo", platform: "etoro", resolvable: false, kind: "close", kindLabel: "Close request for instrument 1002 (position 5)", direction: "long", instrumentId: 1002, symbol: "",
        amountCents: 0, state: "unknown", stateLabel: "unknown", reason: "answer lost", sends: 1, createdAt: "2026-11-24T14:00:00Z", updatedAt: "2026-11-24T14:00:00Z" },
    ]);
    expect(await screen.findByText("Short AAPL")).toBeInTheDocument(); // the server's wording
    const body = within(screen.getByRole("table").querySelector("tbody")!);
    expect(body.getByText("AAPL")).toBeInTheDocument();
    expect(body.getByText("$2,000.00")).toBeInTheDocument();
    expect(body.getByText("103")).toBeInTheDocument();
    expect(body.getByText("filled")).toBeInTheDocument();
    expect(body.getByText("Short")).toBeInTheDocument(); // the direction badge
    expect(body.getByText("New York (NYSE/NASDAQ)")).toBeInTheDocument(); // the exchange
    expect(body.getByText("Close request for instrument 1002 (position 5)")).toBeInTheDocument();
    expect(body.getByText("1002")).toBeInTheDocument(); // no symbol cached: the id
    expect(body.getByText("answer lost")).toBeInTheDocument();
    expect(screen.getByLabelText("Refresh the orders")).toBeInTheDocument();
  });

  it("shows a cancel request eToro took as accepted, never filled", async () => {
    serve([
      { requestId: "33333333-3333-4333-8333-333333333333", mode: "demo", platform: "etoro", resolvable: false, kind: "cancel", kindLabel: "Cancel request for GOOG (buy order 396359836)",
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
    expect((within(screen.getByRole("table")).getAllByRole("row")[2] as HTMLTableRowElement).cells[0].textContent).toBe(dateTime(many[2].createdAt));
  });

  it("filters and groups by exchange", async () => {
    serve(many.map((o, i) => ({ ...o, exchange: i === 0 ? "New York (NYSE/NASDAQ)" : "Sydney (ASX)" })));
    await screen.findByText("Buy AAPL");
    fireEvent.change(screen.getByLabelText("Filter by Exchange"), { target: { value: "Sydney (ASX)" } });
    expect(shares()).toEqual(["TSLA", "TSLA"]);
    fireEvent.change(screen.getByLabelText("Group by"), { target: { value: "exchange" } });
    expect(screen.getByRole("button", { name: "Exchange: Sydney (ASX)" })).toBeInTheDocument();
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

  it("shows a Trading 212 order's ticker and quantity in the account's currency, and resolves an unknown one (R45.11)", async () => {
    const t212 = { ...account, id: "t1", name: "Practice", platform: "trading212", currency: "GBP" };
    const lost = {
      requestId: "lost-1", mode: "demo", platform: "trading212", resolvable: true, kind: "open", kindLabel: "Buy BARC.L (BARCl_EQ)",
      direction: "long", instrumentId: 1, symbol: "BARC.L", market: "LSE", exchange: "London (LSE)", amountCents: 61200,
      ticker: "BARCl_EQ", quantity: "3", state: "unknown", stateLabel: "unknown", reason: "the answer was lost", sends: 1,
      createdAt: "2026-11-24T15:00:00Z", updatedAt: "2026-11-24T15:00:00Z",
    };
    const posts: { url: string; body: unknown }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/system/status")) return json({ ...status, account: t212 });
      if (init?.method === "POST") {
        posts.push({ url, body: JSON.parse(String(init.body)) });
        return json({ ...lost, state: "abandoned", stateLabel: "abandoned", resolvable: false });
      }
      if (url.includes("/api/v1/orders")) return json([lost]);
      return new Response("{}", { status: 404 });
    });
    render(<ModeProvider><Orders /></ModeProvider>);
    const row = (await screen.findByText("Buy BARC.L (BARCl_EQ)")).closest("tr") as HTMLElement;
    expect(within(row).getByText("BARCl_EQ")).toBeInTheDocument();
    expect(within(row).getByText("3")).toBeInTheDocument();
    expect(within(row).getByText("£612.00")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("outcome is unknown");
    fireEvent.click(within(row).getByRole("button", { name: "Resolve order lost-1" }));
    const dialog = await screen.findByRole("dialog");
    // an order id is required unless "not placed" is chosen
    expect(within(dialog).getByRole("button", { name: /^Resolve$/ })).toBeDisabled();
    fireEvent.click(within(dialog).getByLabelText("It was not placed at Trading 212"));
    fireEvent.click(within(dialog).getByRole("button", { name: /^Resolve$/ }));
    await vi.waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].url).toBe("/api/v1/orders/lost-1/resolve");
    expect(posts[0].body).toEqual({ notPlaced: true });
  });

  it("settles a Trading 212 position Trading 212 does not list, by its owner (re-review 2026-11-28)", async () => {
    const t212 = { ...account, id: "t1", name: "Practice", platform: "trading212", currency: "GBP" };
    const buy = {
      requestId: "buy-1", mode: "demo", platform: "trading212", resolvable: false, settleable: true, kind: "open", kindLabel: "Buy BARC.L (BARCl_EQ)",
      direction: "long", instrumentId: 1, symbol: "BARC.L", market: "LSE", exchange: "London (LSE)", amountCents: 61200,
      ticker: "BARCl_EQ", quantity: "3", state: "filled", stateLabel: "filled", reason: "filled by Trading 212", sends: 1,
      createdAt: "2026-11-24T15:00:00Z", updatedAt: "2026-11-24T15:00:00Z",
    };
    const posts: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/system/status")) return json({ ...status, account: t212 });
      if (init?.method === "POST") {
        posts.push(url);
        return json({ ...buy, settleable: false });
      }
      if (url.includes("/api/v1/orders")) return json([buy]);
      return new Response("{}", { status: 404 });
    });
    render(<ModeProvider><Orders /></ModeProvider>);
    const row = (await screen.findByText("Buy BARC.L (BARCl_EQ)")).closest("tr") as HTMLElement;
    expect(screen.getByText(/does not take it for closed/)).toBeInTheDocument();
    fireEvent.click(within(row).getByRole("button", { name: "Settle the position of order buy-1" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Nothing is sent to Trading 212");
    fireEvent.click(within(dialog).getByRole("button", { name: /^Settle$/ }));
    await vi.waitFor(() => expect(posts).toEqual(["/api/v1/orders/buy-1/settle"]));
  });
});
