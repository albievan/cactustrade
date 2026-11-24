import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { RiskLimits } from "./RiskLimits";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "agent.kill", "settings.write"], via: "session", csrfToken: "c",
};
const account = { id: "a1", name: "Main demo", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "" };
const limits = { tradingBudgetCents: 500000, dailyLossBp: 200, drawdownBp: 1000, maxPositionBp: 2000, maxOpenPositions: 5,
  noEntryBeforeCloseMinutes: 30, flattenBeforeCloseMinutes: 10, maxPortfolioAgeSeconds: 5, maxQuoteAgeSeconds: 5, maxShortBp: 2500 };

function serve(agentState: string) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/auth/me")) return json(me);
    if (url.endsWith("/api/v1/system/status")) return json({ version: "0.1.0", build: "1", mode: "demo", agentState, database: "sqlite", startedAt: "", now: "", account });
    if (url.endsWith("/api/v1/risk/limits")) return json(limits);
    return new Response("{}", { status: 404 });
  });
  render(<SessionProvider><ModeProvider><RiskLimits /></ModeProvider></SessionProvider>);
}

afterEach(() => vi.restoreAllMocks());

describe("RiskLimits", () => {
  it.each(["running", "paused", "killed"])("locks the form while the agent is %s", async (state) => {
    serve(state);
    const budget = await screen.findByLabelText("Trading budget");
    expect(budget).toBeDisabled();
    expect(screen.getByLabelText("Daily loss limit")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save limits" })).not.toBeInTheDocument();
    if (state === "killed") expect(screen.getByText(/an admin must reset it on the dashboard before/)).toBeInTheDocument();
    else expect(screen.getByText(/Stop the agent to change the limits/)).toBeInTheDocument();
  });

  it("opens the form when the agent is stopped, and explains paused and killed", async () => {
    serve("stopped");
    const budget = await screen.findByLabelText("Trading budget");
    await screen.findByRole("button", { name: "Save limits" });
    expect(budget).toBeEnabled();
    expect(screen.getByText(/the limits stay locked and it can be started/)).toBeInTheDocument();
    expect(screen.getByText(/An admin must reset it on the dashboard/)).toBeInTheDocument();
  });
});
