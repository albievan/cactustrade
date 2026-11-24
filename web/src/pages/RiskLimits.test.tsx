import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { RiskLimits } from "./RiskLimits";
import source from "./RiskLimits.tsx?raw";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "agent.kill", "settings.write"], via: "session", csrfToken: "c",
};
const account = { id: "a1", name: "Main demo", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "" };
const limits = { tradingBudgetCents: 500000, dailyLossBp: 200, drawdownBp: 1000, maxPositionBp: 2000, maxOpenPositions: 5,
  noEntryBeforeCloseMinutes: 30, flattenBeforeCloseMinutes: 10, maxPortfolioAgeSeconds: 5, maxQuoteAgeSeconds: 5, maxShortBp: 2500 };

// serve answers GET /risk/limits with the server's canEdit and reason. The status says the
// agent is stopped and the caller owns the account in a web session every time: the page
// must follow the server, not work it out.
function serve(canEdit: boolean, editBlockedReason?: string) {
  const puts: unknown[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/auth/me")) return json(me);
    if (url.endsWith("/api/v1/system/status")) return json({ version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "", account });
    if (url.endsWith("/api/v1/risk/limits") && init?.method === "PUT") { puts.push(JSON.parse(String(init.body))); return json(limits); }
    if (url.endsWith("/api/v1/risk/limits")) return json({ ...limits, canEdit, editBlockedReason });
    return new Response("{}", { status: 404 });
  });
  render(<SessionProvider><ModeProvider><RiskLimits /></ModeProvider></SessionProvider>);
  return puts;
}

afterEach(() => vi.restoreAllMocks());

describe("RiskLimits", () => {
  it.each([
    "Stop the agent to change the limits (it is running)",
    "The kill switch is on: an admin must reset it on the dashboard before the limits can change",
    "Only the account's owner can change its limits",
    "The limits can only be changed in the web UI, not with an API key",
  ])("locks the form with the server's reason: %s", async (reason) => {
    serve(false, reason);
    const budget = await screen.findByLabelText("Trading budget");
    expect(budget).toBeDisabled();
    expect(screen.getByLabelText("Daily loss limit")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save limits" })).not.toBeInTheDocument();
    expect(await screen.findByText(reason + ".")).toBeInTheDocument();
  });

  it("opens the form when the server says so, and saves only the limits", async () => {
    const puts = serve(true);
    const save = await screen.findByRole("button", { name: "Save limits" });
    expect(screen.getByLabelText("Trading budget")).toBeEnabled();
    expect(screen.getByText(/the limits stay locked and it can be started/)).toBeInTheDocument();
    expect(screen.getByText(/An admin must reset it on the dashboard/)).toBeInTheDocument();
    fireEvent.click(save);
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual(limits); // no canEdit / editBlockedReason in the PUT body
  });

  it("has no role, access or state check of its own", () => {
    for (const banned of [".role", ".access", ".via", "agentState ===", "agentState !==", "permissions"]) {
      expect(source).not.toContain(banned);
    }
  });
});
