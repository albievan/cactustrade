import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AgentPanel } from "./AgentPanel";
import type { AgentPanel as Panel } from "../lib/api";

const all = { start: true, pause: true, stop: true, kill: true, reset: true, gated: false, forceClose: false };

function panel(over: Partial<Panel> = {}): Panel {
  return {
    account: { id: "a1", name: "Main demo", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "" },
    state: "running", reconciliation: { accountId: "a1", ok: true, reason: "", at: "2026-11-24T15:00:00Z" },
    limits: { tradingBudgetCents: 500000, dailyLossBp: 200, drawdownBp: 1000, maxPositionBp: 2000, maxOpenPositions: 5,
      noEntryBeforeCloseMinutes: 30, flattenBeforeCloseMinutes: 10, maxPortfolioAgeSeconds: 5, maxQuoteAgeSeconds: 5, maxShortBp: 2500 },
    killed: false, dayStopped: false, riskDay: "2026-11-24", today: "2026-11-24", startOfDayEquityCents: 1000000, highWaterMarkCents: 1000000,
    equityKnown: true, equityCents: 990000, dailyFloorCents: 980000, toDailyFloorCents: 10000, drawdownFloorCents: 900000, toDrawdownFloorCents: 90000,
    blocked: [], flatten: [{ market: "NYSE", trading: true, closes: "2026-11-24T21:00:00Z", flattenAt: "2026-11-24T20:50:00Z" }],
    lease: { mine: true, other: false }, controls: all, resetPhrase: "RESET Main demo", openPositions: 1, pendingOrders: 0, missing: [],
    ...over,
  };
}

interface Call { method: string; url: string; body?: unknown }

// serve answers the API with p (and answers per path) and mounts the panel.
function serve(p: Panel, answers: Record<string, unknown> = {}) {
  const calls: Call[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/agent") && method === "GET") return json(p);
    const key = url.replace(/^.*\/api\/v1\//, "");
    if (key in answers) return json(answers[key]);
    return new Response("{}", { status: 404 });
  });
  mount();
  return calls;
}

// Where shows the router's current path, so a test can follow a fix button's link.
function Where() {
  const l = useLocation();
  return <div data-testid="where">{l.pathname + l.search}</div>;
}

function mount() {
  render(
    <MemoryRouter>
      <Routes><Route path="*" element={<><AgentPanel /><Where /></>} /></Routes>
    </MemoryRouter>,
  );
}

const posts = (calls: Call[]) => calls.filter((c) => c.method === "POST");

afterEach(() => vi.restoreAllMocks());

describe("AgentPanel", () => {
  it("shows the state, the distances and the controls the server allows", async () => {
    serve(panel());
    expect(await screen.findByText("running")).toBeInTheDocument();
    expect(screen.getByText("$100.00")).toBeInTheDocument(); // to the daily floor
    expect(screen.getByText("$900.00")).toBeInTheDocument(); // to the drawdown floor
    expect(screen.getByLabelText("Start the agent")).toBeDisabled(); // already running
    expect(screen.getByLabelText("Pause the agent")).toBeEnabled();
    expect(screen.getByLabelText("Stop the agent")).toBeEnabled();
    expect(screen.getByLabelText("Kill switch")).toBeEnabled();
    expect(screen.getByLabelText("Start the agent")).toHaveAttribute("title");
    expect(screen.queryByLabelText("Reset the kill switch")).not.toBeInTheDocument();
  });

  it.each([
    ["a viewer", { start: false, pause: false, stop: false, kill: false, reset: false, gated: false, forceClose: false }],
    ["an operator with view access", { start: false, pause: false, stop: false, kill: false, reset: false, gated: false, forceClose: false }],
  ])("disables every control for %s", async (_who, controls) => {
    serve(panel({ state: "stopped", controls }));
    await screen.findByText("stopped");
    for (const l of ["Start the agent", "Pause the agent", "Stop the agent", "Reconcile with eToro", "Kill switch"]) {
      expect(screen.getByLabelText(l)).toBeDisabled();
    }
  });

  it("lets an API-key-like role pause and kill but not start", async () => {
    serve(panel({ state: "stopped", controls: { ...all, start: false, reset: false } }));
    await screen.findByText("stopped");
    expect(screen.getByLabelText("Start the agent")).toBeDisabled();
    expect(screen.getByLabelText("Kill switch")).toBeEnabled();
  });

  it("asks in a dialog before pulling the kill switch, never with window.confirm", async () => {
    const confirmSpy = vi.spyOn(window, "confirm");
    const calls = serve(panel(), { "agent/kill": { accountId: "a1", already: false, cancelled: 0, closing: 1, errors: [] } });
    fireEvent.click(await screen.findByLabelText("Kill switch"));
    expect(screen.getByRole("dialog", { name: "Pull the kill switch?" })).toBeInTheDocument();
    expect(posts(calls)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(posts(calls)).toEqual([]);

    fireEvent.click(screen.getByLabelText("Kill switch"));
    fireEvent.change(screen.getByLabelText("Reason (optional)"), { target: { value: "market looks wrong" } });
    fireEvent.click(screen.getByRole("button", { name: /Kill now/ }));
    await waitFor(() => expect(posts(calls)).toEqual([{ method: "POST", url: "/api/v1/agent/kill", body: { reason: "market looks wrong" } }]));
    expect(confirmSpy).not.toHaveBeenCalled();
  });

  it("resets the kill switch only with the typed phrase", async () => {
    const calls = serve(panel({ state: "killed", killed: true, killReason: "drawdown 10 %" }), { "agent/reset": { accountId: "a1", state: "stopped" } });
    expect(await screen.findByText(/drawdown 10 %/)).toBeInTheDocument();
    expect(screen.getByLabelText("Start the agent")).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Reset the kill switch"));
    const confirm = screen.getByRole("button", { name: "Reset" });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Type/), { target: { value: "RESET main demo" } });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Type/), { target: { value: "RESET Main demo" } });
    fireEvent.click(confirm);
    await waitFor(() => expect(posts(calls)).toEqual([{ method: "POST", url: "/api/v1/agent/reset", body: { confirm: "RESET Main demo" } }]));
  });

  it("offers no reset to someone who may not reset", async () => {
    serve(panel({ state: "killed", killed: true, controls: { ...all, reset: false } }));
    await screen.findByText("killed");
    expect(screen.queryByLabelText("Reset the kill switch")).not.toBeInTheDocument();
  });

  it("on a gated real account only start is off: pause, stop and the kill switch still work (PLAN 7.21)", async () => {
    serve(panel({ state: "running", controls: { ...all, start: false, gated: true, startBlockedReason: "real trading is not armed on Live" } }));
    expect(await screen.findByText(/Real money: no new entries on this account now/)).toBeInTheDocument();
    expect(screen.getByText(/The agent cannot be started: real trading is not armed on Live/)).toBeInTheDocument();
    expect(screen.getByLabelText("Start the agent")).toBeDisabled();
    for (const l of ["Pause the agent", "Stop the agent", "Kill switch"]) {
      expect(screen.getByLabelText(l)).toBeEnabled();
    }
  });

  it("starts the agent and shows the server's refusal", async () => {
    const calls: Call[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      calls.push({ method: init?.method ?? "GET", url });
      if (url.endsWith("/api/v1/agent/start")) {
        return new Response(JSON.stringify({ title: "Conflict", status: 409, detail: "the account is not reconciled" }), { status: 409 });
      }
      return new Response(JSON.stringify(panel({ state: "stopped" })), { status: 200 });
    });
    mount();
    fireEvent.click(await screen.findByLabelText("Start the agent"));
    expect(await screen.findByText("the account is not reconciled")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST" && c.url === "/api/v1/agent/start")).toBe(true);
  });
});

describe("AgentPanel kill switch", () => {
  it("stays usable while another action (reconcile) is running", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url.endsWith("/api/v1/agent/reconcile")) return new Promise<Response>(() => undefined); // never answers
      return new Response(JSON.stringify(panel({ state: "running" })), { status: 200 });
    });
    mount();
    fireEvent.click(await screen.findByLabelText("Reconcile with eToro"));
    await waitFor(() => expect(screen.getByLabelText("Start the agent")).toBeDisabled());
    expect(screen.getByLabelText("Kill switch")).toBeEnabled();
  });
});

describe("AgentPanel missing setup (R22)", () => {
  it("lists what must be set up before the agent can start, with a Set budget button to Risk limits", async () => {
    serve(panel({
      state: "stopped",
      missing: [{ what: "trading_budget", canFix: true }, { what: "watchlist", canFix: true }, { what: "kill_reset", canFix: false }],
    }));
    const row = await screen.findByLabelText("Before the agent can start");
    expect(row).toHaveTextContent("No trading budget");
    expect(row).toHaveTextContent("The watchlist is empty");
    expect(row).toHaveTextContent("The kill switch is on");
    const budget = screen.getByRole("link", { name: /^Set budget/ });
    expect(budget).toHaveAttribute("href", "/risk");
    expect(budget).toHaveAttribute("title");
    expect(screen.getByRole("link", { name: /^Add shares/ })).toHaveAttribute("href", "/watchlist");
    // the server says this caller may not reset: no button, only the text
    expect(screen.queryByRole("button", { name: /^Reset kill switch/ })).not.toBeInTheDocument();
    fireEvent.click(budget);
    expect(screen.getByTestId("where")).toHaveTextContent("/risk");
  });

  it("shows nothing to set up when nothing is missing", async () => {
    serve(panel({ state: "stopped" }));
    await screen.findByText("stopped");
    expect(screen.queryByLabelText("Before the agent can start")).not.toBeInTheDocument();
  });

  it("links the keys of this account and runs reconcile and the reset dialog in the panel", async () => {
    const calls = serve(panel({
      state: "stopped",
      missing: [{ what: "account_keys", canFix: true }, { what: "reconcile", canFix: true }, { what: "kill_reset", canFix: true }],
    }), { "agent/reconcile": { accountId: "a1", ok: true, reason: "" } });
    expect(await screen.findByRole("link", { name: /^Add keys/ })).toHaveAttribute("href", "/accounts?keys=a1");
    fireEvent.click(screen.getByRole("button", { name: /^Reconcile:/ }));
    await waitFor(() => expect(posts(calls)).toEqual([{ method: "POST", url: "/api/v1/agent/reconcile", body: undefined }]));
    await waitFor(() => expect(screen.getByRole("button", { name: /^Reset kill switch/ })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: /^Reset kill switch/ }));
    expect(screen.getByRole("dialog", { name: "Reset the kill switch?" })).toBeInTheDocument();
  });

  it("puts the fix next to a refused start: no budget (422)", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.endsWith("/api/v1/agent/start") && init?.method === "POST") {
        return new Response(JSON.stringify({
          title: "Cannot do that now", status: 422, field: "tradingBudgetCents",
          detail: "Set a trading budget before starting the agent", missing: "trading_budget", canFix: true,
        }), { status: 422 });
      }
      return new Response(JSON.stringify(panel({ state: "stopped" })), { status: 200 });
    });
    mount();
    fireEvent.click(await screen.findByLabelText("Start the agent"));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Set a trading budget before starting the agent");
    expect(alert).not.toHaveTextContent("tradingBudgetCents");
    expect(screen.getByRole("link", { name: /^Set budget/ })).toHaveAttribute("href", "/risk");
  });

  it("says why the agent cannot start when the server blocks it (6.13), and keeps the kill switch", async () => {
    const reason = "the agent loop is not running: start-up resume failed; restart cactusTrade";
    serve(panel({ state: "stopped", controls: { ...all, start: false, startBlockedReason: reason } }));
    await screen.findByText("stopped");
    expect(screen.getByLabelText("Start the agent")).toBeDisabled();
    expect(screen.getByLabelText("Start the agent")).toHaveAttribute("title", "Cannot start: " + reason);
    expect(screen.getByRole("status")).toHaveTextContent("The agent cannot be started: " + reason);
    expect(screen.getByLabelText("Kill switch")).toBeEnabled();
  });
});

describe("AgentPanel force close (R24)", () => {
  // serveForce answers the panel, the force-close preview and the force close itself (403 for
  // a wrong password, as the server does).
  function serveForce(p: Panel) {
    const calls: Call[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, url, body });
      const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status });
      if (url.endsWith("/api/v1/agent") && method === "GET") return json(p);
      if (url === "/api/v1/agent/force-close?accountId=a1" && method === "GET") {
        return json({ accountId: "a1", name: "Main demo", positions: 3, orders: 1, summary: "closes 3 positions and cancels 1 order on Main demo" });
      }
      if (url === "/api/v1/agent/force-close?accountId=a1" && method === "POST") {
        if (body.password !== "right one") return json({ title: "Forbidden", status: 403, detail: "the password is not correct" }, 403);
        return json({ accountId: "a1", name: "Main demo", cancelled: 1, closing: 3, state: "stopped", errors: [] });
      }
      return json({}, 404);
    });
    mount();
    return calls;
  }

  it("is not offered unless the server says so", async () => {
    serveForce(panel());
    await screen.findByText("running");
    expect(screen.queryByLabelText("Force close")).not.toBeInTheDocument();
  });

  it("sits in the controls column, shows the server's summary and needs the password", async () => {
    const calls = serveForce(panel({ controls: { ...all, forceClose: true } }));
    const button = await screen.findByLabelText("Force close");
    // in the first column, after the kill switch
    const kill = screen.getByLabelText("Kill switch");
    expect(kill.parentElement).toContainElement(button);
    expect(kill.compareDocumentPosition(button) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    fireEvent.click(button);
    const dialog = screen.getByRole("dialog", { name: "Force close?" });
    expect(await screen.findByText("Closes 3 positions and cancels 1 order on Main demo.")).toBeInTheDocument();
    const confirm = within(dialog).getByRole("button", { name: /^Force close$/ });
    expect(confirm).toBeDisabled(); // no password yet
    expect(posts(calls)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Your password"), { target: { value: "guess" } });
    fireEvent.click(confirm);
    expect(await screen.findByText("the password is not correct")).toBeInTheDocument();
    expect(dialog).toBeInTheDocument(); // still open, the password cleared
    expect(screen.getByLabelText("Your password")).toHaveValue("");

    fireEvent.change(screen.getByLabelText("Your password"), { target: { value: "right one" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /^Force close$/ }));
    expect(await screen.findByText(/Main demo: 3 closes and 1 cancel sent; the agent is stopped/)).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(posts(calls)).toEqual([
      { method: "POST", url: "/api/v1/agent/force-close?accountId=a1", body: { password: "guess" } },
      { method: "POST", url: "/api/v1/agent/force-close?accountId=a1", body: { password: "right one" } },
    ]);
  });

  it("offers force close on a gated real account too (exits always work, PLAN 7.21)", async () => {
    serveForce(panel({ state: "stopped", controls: { ...all, start: false, gated: true, forceClose: true } }));
    expect(await screen.findByLabelText("Force close")).toBeEnabled();
    expect(screen.queryByText(/No force close here/)).not.toBeInTheDocument();
  });
});
