import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { Dashboard } from "./Dashboard";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "agent.kill"], via: "session", csrfToken: "c",
};
const account = { id: "a1", name: "Main demo", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "" };
const status = { version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "", account };
const agent = {
  account, state: "stopped", reconciliation: { accountId: "a1", ok: false, reason: "not reconciled yet" },
  limits: { tradingBudgetCents: 0, dailyLossBp: 200, drawdownBp: 1000, maxPositionBp: 2000, maxOpenPositions: 5,
    noEntryBeforeCloseMinutes: 30, flattenBeforeCloseMinutes: 10, maxPortfolioAgeSeconds: 5, maxQuoteAgeSeconds: 5, maxShortBp: 2500 },
  killed: false, dayStopped: false, riskDay: "", today: "2026-11-24", startOfDayEquityCents: 0, highWaterMarkCents: 0,
  equityKnown: false, equityError: "the account's eToro keys are not set", blocked: [], flatten: [],
  lease: { mine: false, other: false }, controls: { start: true, pause: true, stop: true, kill: true, reset: true, gated: false },
  resetPhrase: "RESET Main demo", openPositions: 0, pendingOrders: 0,
};

afterEach(() => vi.restoreAllMocks());

describe("Dashboard", () => {
  it("shows the agent panel and no longer says orders arrive in phase 4", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json(status);
      if (url.endsWith("/api/v1/agent")) return json(agent);
      if (url.endsWith("/api/v1/market/status")) return json([]);
      if (url.endsWith("/api/v1/broker/portfolio")) return json({ availableCashCents: 0, equityCents: 0, pendingOpenCents: 0, positions: [], asOf: "" });
      return new Response(JSON.stringify({ title: "Not found", status: 404 }), { status: 404 });
    });
    render(<MemoryRouter><SessionProvider><ModeProvider><Dashboard /></ModeProvider></SessionProvider></MemoryRouter>);
    expect(await screen.findByRole("group", { name: "Agent controls" })).toBeInTheDocument();
    expect(screen.getByText("not reconciled")).toBeInTheDocument();
    expect(screen.queryByText(/phase 4/)).not.toBeInTheDocument();
  });
});
