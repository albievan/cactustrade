import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { Accounts } from "./Accounts";
import { ApiKeys } from "./ApiKeys";
import { System } from "./System";
import { Themes } from "./Themes";
import { Users } from "./Users";

// PLAN 6.15: every page that asks before an action asks in a ConfirmDialog (no
// window.confirm or prompt): Cancel does nothing, confirming calls the API once.

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "settings.write", "users.manage"], via: "session", csrfToken: "c",
};
const account = { id: "a1", name: "Main demo", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "2026-11-23T09:00:00Z" };
const status = { version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "2026-11-23T09:00:00Z", now: "2026-11-23T09:00:00Z", account };
const theme = { id: "t1", name: "Space", keywords: ["space"], industryId: 0, enabled: true, instruments: [], shares: [], llmProfileId: "", createdAt: "", updatedAt: "" };
const users = [
  { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  { id: "2", username: "olga", role: "operator", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
];
const key = { id: "k1", name: "grafana", role: "viewer", ownerId: "1", createdAt: "2026-11-23T09:00:00Z" };

const schedule = {
  id: "s1", ownerId: "1", ownerName: "admin", period: "daily", accountIds: [], accounts: [], recipients: ["me@example.com"],
  includeCombined: true, includeAccounts: false, includeExchanges: false, enabled: true, lastPeriod: "", lastError: "", createdAt: "", updatedAt: "",
};

type Call = { url: string; method: string; body: unknown };

// serve answers the reads every page makes, and every write with 204 (or status for the
// restart, so the page does not start waiting for the server to come back).
function serve() {
  const calls: Call[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/system/restart")) return new Response(JSON.stringify({ status: 503, title: "Unavailable" }), { status: 503 });
    if (method !== "GET") return new Response(null, { status: 204 });
    if (url.endsWith("/api/v1/auth/me")) return json(me);
    if (url.endsWith("/api/v1/system/status")) return json(status);
    if (url.endsWith("/api/v1/users")) return json(users);
    if (url.endsWith("/api/v1/api-keys")) return json([key]);
    if (url.endsWith("/api/v1/themes")) return json([theme]);
    if (url.includes("/api/v1/reviews")) return json({ reviews: [], scannerEnabled: true });
    if (url.endsWith("/api/v1/accounts")) return json([account]);
    if (url.endsWith("/api/v1/platforms")) return json([{ platform: "etoro", label: "eToro", viewOnly: false, connectable: false, credentials: [] }]);
    if (url.endsWith("/keys")) return json({ apiKeySet: true, userKeySet: true, fromEnvironment: false, complete: true, connectable: false, credentials: [] });
    if (url.endsWith("/shares") || url.endsWith("/industries")) return json([]);
    if (url.endsWith("/api/v1/statements/schedules")) return json({ schedules: [schedule], accounts: [], mailServer: true, canFixMail: true });
    return new Response("{}", { status: 404 });
  });
  return calls;
}

const writes = (calls: Call[]) => calls.filter((c) => c.method !== "GET");

function mount(page: ReactNode) {
  render(<MemoryRouter><SessionProvider><ModeProvider>{page}</ModeProvider></SessionProvider></MemoryRouter>);
}

// cancelThenConfirm opens the dialog with open(), cancels it, opens it again and confirms
// (twice, quickly): exactly one write, the expected one.
async function cancelThenConfirm(calls: Call[], open: () => Promise<void>, confirm: string, want: Omit<Call, "body"> & { body?: unknown }, fill?: () => void) {
  await open();
  const dialog = screen.getByRole("dialog");
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  await open();
  fireEvent.keyDown(window, { key: "Escape" });
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(writes(calls)).toHaveLength(0);
  await open();
  fill?.();
  const button = within(screen.getByRole("dialog")).getByRole("button", { name: confirm });
  fireEvent.click(button);
  fireEvent.click(button);
  await waitFor(() => expect(writes(calls)).toHaveLength(1));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(writes(calls)).toHaveLength(1);
  expect(writes(calls)[0]).toMatchObject(want);
}

const click = (label: string) => async () => { fireEvent.click(await screen.findByLabelText(label)); };

afterEach(() => vi.restoreAllMocks());

describe("System", () => {
  it("asks before restarting", async () => {
    const calls = serve();
    mount(<System />);
    const open = async () => { fireEvent.click(await screen.findByRole("button", { name: "Restart" })); };
    await cancelThenConfirm(calls, open, "Restart", { url: "/api/v1/system/restart", method: "POST" });
    expect(await screen.findByRole("alert")).toBeInTheDocument(); // the 503 is shown
  });
});

describe("Trading statements", () => {
  it("asks before deleting a schedule", async () => {
    const calls = serve();
    mount(<System />);
    await cancelThenConfirm(calls, click("Delete: daily statements to me@example.com"), "Delete", { url: "/api/v1/statements/schedules/s1", method: "DELETE" });
  });
});

describe("ApiKeys", () => {
  it("asks before revoking a key", async () => {
    const calls = serve();
    mount(<ApiKeys />);
    await cancelThenConfirm(calls, click("Revoke grafana"), "Revoke", { url: "/api/v1/api-keys/k1", method: "DELETE" });
  });
});

describe("Themes", () => {
  it("asks before deleting a theme", async () => {
    const calls = serve();
    mount(<Themes />);
    await cancelThenConfirm(calls, click("Delete Space"), "Delete", { url: "/api/v1/themes/t1", method: "DELETE" });
  });
});

describe("Users", () => {
  it("asks before deleting a user", async () => {
    const calls = serve();
    mount(<Users />);
    await cancelThenConfirm(calls, click("Delete olga"), "Delete", { url: "/api/v1/users/2", method: "DELETE" });
    expect(screen.queryByLabelText("Delete admin")).not.toBeInTheDocument(); // not yourself
  });

  it("asks for the new password in a dialog, not a prompt", async () => {
    const calls = serve();
    mount(<Users />);
    await cancelThenConfirm(calls, click("Reset the password of olga"), "Set password",
      { url: "/api/v1/users/2", method: "PATCH", body: { password: "a long new password" } },
      () => {
        expect(within(screen.getByRole("dialog")).getByRole("button", { name: "Set password" })).toBeDisabled();
        fireEvent.change(screen.getByLabelText("New password (12+ characters)"), { target: { value: "a long new password" } });
      });
  });
});

describe("Accounts", () => {
  it("asks before deleting an account", async () => {
    const calls = serve();
    mount(<Accounts />);
    await cancelThenConfirm(calls, click("Delete Main demo"), "Delete", { url: "/api/v1/accounts/a1", method: "DELETE" });
  });
});
