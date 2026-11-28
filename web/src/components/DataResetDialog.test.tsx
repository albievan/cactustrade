import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { SessionProvider } from "../lib/session";
import { System } from "../pages/System";

const me = (role: string, permissions: string[]) => ({
  user: { id: "u1", username: "albie", role, disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions, via: "session", csrfToken: "c",
});
const admin = me("admin", ["view", "agent.control", "settings.write"]);
const operator = me("operator", ["view", "agent.control"]);

const checks = (failing: Record<string, string> = {}) => [
  "reset_agents_stopped", "reset_no_live_lease", "reset_no_armed_gate", "reset_reconciled",
  "reset_no_open_position", "reset_no_open_order", "reset_no_recent_order", "reset_backup_ready",
].map((name) => ({ name, pass: !(name in failing), detail: failing[name] ?? "ok" }));

const preview = (failing: Record<string, string> = {}) => ({
  checks: checks(failing), canReset: Object.keys(failing).length === 0,
  wiped: { orders: 12, decisions: 40, trades: 3 }, kept: { users: 2, watchlist: 9 },
  realAccounts: [{ accountId: "r1", name: "Real Money Maker", demoAccountId: "d1", demoName: "Demo", daysNow: 4, required: 5 }],
  backupReady: true, backupFolder: "/backups", phrase: "DELETE ALL TRADING DATA",
});

interface Call { method: string; url: string; body?: string }

function mount(opts: { who?: unknown; pv?: unknown } = {}) {
  const calls: Call[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ method, url, body: init?.body ? String(init.body) : undefined });
    const json = (v: unknown, code = 200) => new Response(JSON.stringify(v), { status: code, headers: { "Content-Type": "application/json" } });
    if (url.endsWith("/api/v1/auth/me")) return json(opts.who ?? admin);
    if (url.endsWith("/api/v1/system/data-reset") && method === "GET") return json(opts.pv ?? preview());
    if (url.endsWith("/api/v1/system/data-reset") && method === "POST") {
      return json({ backupFile: "cactustrade-20261128T120000Z.db.age", counts: { orders: 12 }, wipedWatchlists: false, at: "2026-11-28T12:00:00Z" });
    }
    if (url.includes("/api/v1/statements/schedules")) return json({ schedules: [], accounts: [], canSchedule: false });
    return json({ title: "Not found", status: 404 }, 404);
  });
  render(<MemoryRouter><SessionProvider><System /></SessionProvider></MemoryRouter>);
  return calls;
}

const open = async () => fireEvent.click(await screen.findByRole("button", { name: "Reset all trading data" }));

afterEach(() => vi.restoreAllMocks());

describe("DataResetDialog", () => {
  it("is hidden for non-admins", async () => {
    const calls = mount({ who: operator });
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/api/v1/auth/me"))).toBe(true));
    expect(screen.queryByRole("button", { name: "Reset all trading data" })).not.toBeInTheDocument();
    expect(calls.some((c) => c.url.includes("data-reset"))).toBe(false);
  });

  it("keeps the button disabled until the phrase and the password are typed, then posts them with the watchlists kept", async () => {
    const calls = mount();
    await open();
    const dialog = await screen.findByRole("dialog");
    const confirm = () => screen.getAllByRole("button", { name: /Reset all trading data/ }).find((b) => dialog.contains(b))!;
    expect(await screen.findByTestId("reset-checks-ok")).toBeInTheDocument();
    expect(confirm()).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Your password"), { target: { value: "a long password" } });
    fireEvent.change(screen.getByLabelText(/Type/), { target: { value: "DELETE ALL TRADING" } });
    expect(confirm()).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Type/), { target: { value: "DELETE ALL TRADING DATA" } });
    expect(screen.getByLabelText("Also empty every watchlist")).not.toBeChecked();
    expect(confirm()).toBeEnabled();
    fireEvent.click(confirm());
    expect(await screen.findByText(/cactustrade-20261128T120000Z\.db\.age/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST" && c.url.endsWith("/api/v1/system/data-reset"));
    expect(JSON.parse(post!.body!)).toEqual({ confirm: "DELETE ALL TRADING DATA", password: "a long password", wipeWatchlists: false });
  });

  it("shows the refusing checks with where to fix them, the real-gate warning and the backup line", async () => {
    mount({ pv: preview({ reset_agents_stopped: "stop these agents first: Demo is running", reset_no_armed_gate: "disarm real trading first on: Real Money Maker" }) });
    await open();
    const refusals = await screen.findByTestId("reset-refusals");
    expect(refusals).toHaveTextContent("Demo is running");
    expect(screen.getByRole("link", { name: "Real trading" })).toHaveAttribute("href", "/real-trading");
    expect(screen.getByRole("link", { name: "Dashboard" })).toHaveAttribute("href", "/");
    expect(screen.getByTestId("reset-real-warning")).toHaveTextContent("Real Money Maker: 4 qualifying demo days now → 0.");
    expect(screen.getByTestId("reset-real-warning")).toHaveTextContent("until 5 new qualifying demo days on Demo");
    expect(screen.getByTestId("reset-backup")).toHaveTextContent("An encrypted backup is taken first into /backups");
    expect(screen.getByLabelText("What the reset wipes")).toHaveTextContent("orders: 12");
  });

  it("lists the watchlists as wiped when ticked", async () => {
    mount();
    await open();
    fireEvent.click(await screen.findByLabelText("Also empty every watchlist"));
    expect(screen.getByLabelText("What the reset wipes")).toHaveTextContent("watchlists: 9");
  });
});
