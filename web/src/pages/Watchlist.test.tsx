import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { Watchlist } from "./Watchlist";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "settings.write"], via: "session", csrfToken: "c",
};

describe("Watchlist", () => {
  it("lists items with live quotes and eligibility", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json({ version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "2026-11-23T09:00:00Z", now: "2026-11-23T09:00:00Z",
        account: { id: "a1", name: "Main", ownerId: "1", ownerName: "admin", mode: "demo", envKeys: false, access: "owner", createdAt: "2026-11-23T09:00:00Z" } });
      if (url.endsWith("/api/v1/watchlist")) return json([{
        instrument: { instrumentId: 1001, symbol: "EXMP", name: "Example Corp", type: "Stocks", market: "NYSE" },
        eligibility: { realLong: true, checkedAt: "2026-11-23T09:00:00Z" }, addedBy: "admin", addedAt: "2026-11-23T09:00:00Z",
      }]);
      if (url.endsWith("/api/v1/watchlist/quotes")) return json([{ instrumentId: 1001, bid: "189.1", ask: "189.2", at: "2026-11-23T09:00:00Z", delayed: true }]);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><ModeProvider><Watchlist /></ModeProvider></SessionProvider>);
    expect(await screen.findByText("EXMP")).toBeInTheDocument();
    expect(await screen.findByText("189.1")).toBeInTheDocument();
    expect(screen.getByText("delayed")).toBeInTheDocument();
    expect(screen.getByText(/real share x1/)).toBeInTheDocument();
    expect(await screen.findByLabelText("Search instruments")).toBeInTheDocument();
  });
});

describe("Watchlist empty (R25)", () => {
  const acct = (id: string, name: string, ownerName = "admin") =>
    ({ id, name, ownerId: "1", ownerName, mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "2026-11-23T09:00:00Z" });
  // serve has two accounts: a1 (current, empty) and a2; switching changes the current one and
  // its watchlist.
  function serve(counts: { account: ReturnType<typeof acct>; count: number }[]) {
    let current = "a1";
    const puts: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) {
        return json({ version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "",
          account: counts.find((c) => c.account.id === current)?.account });
      }
      if (url.endsWith("/api/v1/watchlist/counts")) return json(counts.map((c) => ({ ...c, current: c.account.id === current })));
      if (url.endsWith("/api/v1/watchlist")) {
        return json(current === "a1" ? [] : [{
          instrument: { instrumentId: 1001, symbol: "EXMP", name: "Example Corp", type: "Stocks", market: "NYSE" },
          eligibility: { realLong: true }, addedBy: "albie", addedAt: "2026-11-23T09:00:00Z",
        }]);
      }
      if (url.endsWith("/api/v1/accounts/current") && init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        puts.push(body);
        current = body.accountId;
        return json(counts.find((c) => c.account.id === current)?.account);
      }
      if (url.endsWith("/api/v1/watchlist/quotes")) return json([]);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><ModeProvider><Watchlist /></ModeProvider></SessionProvider>);
    return puts;
  }

  afterEach(() => vi.restoreAllMocks());

  it("says whose watchlist is empty and links the accounts that have shares", async () => {
    const puts = serve([{ account: acct("a1", "Admin demo"), count: 0 }, { account: acct("a2", "Albie demo", "albie"), count: 3 }]);
    const empty = await screen.findByLabelText("Empty watchlist");
    expect(empty).toHaveTextContent("Admin demo has no shares on its watchlist.");
    expect(await screen.findByText(/Shares are on the watchlist of/)).toHaveTextContent("Albie demo (3 shares)");
    expect(empty).toHaveTextContent("Each eToro account has its own watchlist");
    fireEvent.click(screen.getByRole("button", { name: "Albie demo" }));
    expect(await screen.findByText("EXMP")).toBeInTheDocument();
    expect(puts).toEqual([{ accountId: "a2" }]);
  });

  it("names no other account when none has shares", async () => {
    serve([{ account: acct("a1", "Admin demo"), count: 0 }, { account: acct("a2", "Other"), count: 0 }]);
    expect(await screen.findByLabelText("Empty watchlist")).toHaveTextContent("Admin demo has no shares on its watchlist.");
    await waitFor(() => expect(screen.queryByText(/Shares are on the watchlist of/)).not.toBeInTheDocument());
  });
});
