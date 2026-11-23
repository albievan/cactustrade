import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
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
