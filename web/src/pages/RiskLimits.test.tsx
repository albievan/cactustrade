import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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
    if (url.endsWith("/api/v1/agents")) return json([]);
    return new Response("{}", { status: 404 });
  });
  render(<SessionProvider><ModeProvider><RiskLimits /></ModeProvider></SessionProvider>);
  return puts;
}

afterEach(() => vi.restoreAllMocks());

const second = { ...account, id: "a2", name: "Second demo" };
const allControls = { start: true, pause: true, stop: true, kill: true, reset: true, gated: false, forceClose: true };

interface Call { method: string; url: string; body?: unknown }

// serveAgents is a small stateful server: two accounts, a1 (current, running) and a2
// (stopped). Stop, start, the current account and the limits follow what the page sends;
// the limits are editable only while the current account's agent is stopped, as on the server.
function serveAgents() {
  const st: Record<string, string> = { a1: "running", a2: "stopped" };
  let current = "a1";
  const calls: Call[] = [];
  const acct = (id: string) => (id === "a1" ? account : second);
  const card = (id: string) => ({
    account: acct(id), current: current === id, state: st[id], killed: false,
    reconciliation: { accountId: id, ok: true, reason: "" }, limitsEditable: st[id] === "stopped",
    limitsBlockedReason: st[id] === "stopped" ? undefined : `Stop the agent to change the limits (it is ${st[id]})`,
    controls: allControls, openPositions: 0, pendingOrders: 0,
  });
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url, body });
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    const id = new URL(url, "http://x").searchParams.get("accountId") ?? current;
    if (url.endsWith("/api/v1/auth/me")) return json(me);
    if (url.endsWith("/api/v1/system/status")) {
      return json({ version: "0.1.0", build: "1", mode: "demo", agentState: st[current], database: "sqlite", startedAt: "", now: "", account: acct(current) });
    }
    if (url.endsWith("/api/v1/risk/limits") && method === "PUT") return json(limits);
    if (url.endsWith("/api/v1/risk/limits")) {
      const c = card(current);
      return json({ ...limits, canEdit: c.limitsEditable, editBlockedReason: c.limitsBlockedReason });
    }
    if (url.endsWith("/api/v1/agents")) return json(["a1", "a2"].map(card));
    if (url.startsWith("/api/v1/agent/stop") && method === "POST") { st[id] = "stopped"; return json({ accountId: id, state: "stopped" }); }
    if (url.startsWith("/api/v1/agent/start") && method === "POST") { st[id] = "running"; return json({ accountId: id, state: "running" }); }
    if (url.endsWith("/api/v1/accounts/current") && method === "PUT") { current = body.accountId; return json(acct(current)); }
    if (url.startsWith("/api/v1/agent/force-close") && method === "GET") {
      return json({ accountId: id, name: acct(id).name, positions: 2, orders: 0, summary: `closes 2 positions on ${acct(id).name}` });
    }
    return new Response("{}", { status: 404 });
  });
  render(<SessionProvider><ModeProvider><RiskLimits /></ModeProvider></SessionProvider>);
  return calls;
}

const posts = (calls: Call[]) => calls.filter((c) => c.method !== "GET");

describe("RiskLimits agents (R23)", () => {
  it("shows one card per account, the icon buttons first and in order, the current account highlighted", async () => {
    serveAgents();
    const main = await screen.findByRole("group", { name: "Agent on Main demo" });
    const other = screen.getByRole("group", { name: "Agent on Second demo" });
    expect(main).toHaveAttribute("aria-current", "true");
    expect(other).not.toHaveAttribute("aria-current");
    const controls = within(main).getByRole("group", { name: "Agent controls for Main demo" });
    // the controls are the first column of the card's body
    expect(controls.parentElement?.firstElementChild).toBe(controls);
    expect(within(controls).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual([
      "Start the agent on Main demo", "Pause the agent on Main demo", "Stop the agent on Main demo", "Force close Main demo",
    ]);
    for (const b of within(controls).getAllByRole("button")) expect(b).toHaveAttribute("title");
    expect(within(main).getByText("limits locked")).toBeInTheDocument();
    expect(within(other).getByText("limits editable")).toBeInTheDocument();
    expect(within(main).getByText("demo")).toBeInTheDocument();
    expect(within(main).getByText("reconciled")).toBeInTheDocument();
    expect(screen.getByLabelText("Start the agent on Main demo")).toBeDisabled(); // running
    expect(screen.getByLabelText("Stop the agent on Second demo")).toBeDisabled(); // stopped
  });

  it("stops the agent, saves the limits and offers to start it again", async () => {
    const calls = serveAgents();
    const budget = await screen.findByLabelText("Trading budget");
    expect(budget).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Stop the agent on Main demo"));
    await waitFor(() => expect(screen.getByLabelText("Trading budget")).toBeEnabled());
    expect(screen.queryByRole("button", { name: /Start the agent again/ })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Trading budget"), { target: { value: "4000" } });
    fireEvent.click(screen.getByRole("button", { name: "Save limits" }));
    const again = await screen.findByRole("button", { name: /Start the agent again/ });
    fireEvent.click(again);
    await waitFor(() => expect(screen.queryByRole("button", { name: /Start the agent again/ })).not.toBeInTheDocument());
    expect(posts(calls).map((c) => c.method + " " + c.url)).toEqual([
      "POST /api/v1/agent/stop?accountId=a1", "PUT /api/v1/risk/limits", "POST /api/v1/agent/start?accountId=a1",
    ]);
    expect(await screen.findByText("Main demo: the agent is running.")).toBeInTheDocument();
  });

  it("does not offer a start when the agent was not stopped from this page", async () => {
    serveAgents();
    fireEvent.click(await screen.findByRole("button", { name: "Second demo" })); // switch to the stopped account
    await waitFor(() => expect(screen.getByLabelText("Trading budget")).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Save limits" }));
    expect(await screen.findByText("Saved and audited.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Start the agent again/ })).not.toBeInTheDocument();
  });

  it("switches the current account when a card's name is clicked", async () => {
    const calls = serveAgents();
    fireEvent.click(await screen.findByRole("button", { name: "Second demo" }));
    await waitFor(() => expect(screen.getByRole("group", { name: "Agent on Second demo" })).toHaveAttribute("aria-current", "true"));
    expect(posts(calls)).toEqual([{ method: "PUT", url: "/api/v1/accounts/current", body: { accountId: "a2" } }]);
    expect(await screen.findByText("Second demo", { selector: "strong" })).toBeInTheDocument(); // the limits form's account
  });

  it("asks for the password before a force close from a card", async () => {
    const calls = serveAgents();
    fireEvent.click(await screen.findByLabelText("Force close Main demo"));
    const dialog = screen.getByRole("dialog", { name: "Force close?" });
    expect(await within(dialog).findByText("Closes 2 positions on Main demo.")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /^Force close$/ })).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText("Your password"), { target: { value: "secret" } });
    expect(within(dialog).getByRole("button", { name: /^Force close$/ })).toBeEnabled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(posts(calls)).toEqual([]);
  });
});

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

describe("RiskLimits presets and style (R28)", () => {
  it("applies a preset, fills the form from the server and shows Custom once a field is edited", async () => {
    const low = { ...limits, dailyLossBp: 50, drawdownBp: 300, maxPositionBp: 500, maxOpenPositions: 3, maxShortBp: 0 };
    const preset = (id: string, label: string) => ({ id, label, summary: label + " in words", dailyLossBp: 50, drawdownBp: 300, maxPositionBp: 500, maxOpenPositions: 3, maxShortBp: 0 });
    const puts: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json({ version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "", account });
      if (url.endsWith("/api/v1/risk/limits") && init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        puts.push(body);
        return json(body.preset ? { ...low, canEdit: true, preset: "low" } : { ...body, canEdit: true, preset: "custom" });
      }
      if (url.endsWith("/api/v1/risk/limits")) return json({ ...limits, canEdit: true, preset: "custom" });
      if (url.endsWith("/api/v1/risk/presets")) return json({ current: "custom", clamped: false, presets: [preset("low", "Low"), preset("high", "High")] });
      if (url.endsWith("/api/v1/agents")) return json([]);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><ModeProvider><RiskLimits /></ModeProvider></SessionProvider>);
    const lowBtn = await screen.findByRole("button", { name: /^Low preset/ });
    expect(screen.getByRole("button", { name: /^Custom/ })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(lowBtn);
    await waitFor(() => expect(screen.getByLabelText("Daily loss limit")).toHaveValue(50));
    expect(puts).toEqual([{ preset: "low" }]);
    expect(screen.getByLabelText("Trading budget")).toHaveValue(5000);
    expect(screen.getByRole("button", { name: /^Low preset/ })).toHaveAttribute("aria-pressed", "true");
    fireEvent.change(screen.getByLabelText("Daily loss limit"), { target: { value: "70" } });
    expect(screen.getByRole("button", { name: /^Custom/ })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(screen.getByRole("button", { name: "Save limits" }));
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1]).toEqual({ ...low, dailyLossBp: 70 }); // the limits only, never canEdit or preset
    await waitFor(() => expect(screen.getByRole("button", { name: /^Custom/ })).toHaveAttribute("aria-pressed", "true"));
    expect(screen.getByRole("button", { name: /^Low preset/ })).toHaveAttribute("aria-pressed", "false");
  });
});

describe("RiskLimits on a Trading 212 practice account (R45.11)", () => {
  it("shows the limits in the account's currency, requires a minimum order value, hides shorts and keeps the fractional switch locked", async () => {
    const practice = { ...account, id: "t1", name: "Practice", platform: "trading212", currency: "GBP" };
    const t212 = { ...limits, tradingBudgetCents: 100000, maxShortBp: 0, minOrderValueCents: 1000 };
    const puts: Record<string, unknown>[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json({ version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "", account: practice });
      if (url.endsWith("/api/v1/risk/limits") && init?.method === "PUT") { puts.push(JSON.parse(String(init.body))); return json(t212); }
      if (url.endsWith("/api/v1/risk/limits")) {
        return json({ ...t212, canEdit: true, preset: "custom", platform: "trading212", currency: "GBP", fractionsVerified: false });
      }
      if (url.endsWith("/api/v1/agents")) return json([]);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><ModeProvider><RiskLimits /></ModeProvider></SessionProvider>);
    const min = await screen.findByLabelText("Minimum order value");
    expect(min).toBeRequired();
    expect(min).toHaveValue(10);
    expect(screen.getByText(/£1,000\.00 now/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Short selling")).toBeNull();
    const frac = screen.getByLabelText("Fractional shares");
    expect(frac).toBeDisabled();
    expect(screen.getByText(/Locked: whole shares only/)).toBeInTheDocument();
    fireEvent.change(min, { target: { value: "25.50" } });
    fireEvent.click(screen.getByRole("button", { name: "Save limits" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toMatchObject({ tradingBudgetCents: 100000, minOrderValueCents: 2550, maxShortBp: 0, t212Fractional: false });
  });
});
