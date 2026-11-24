import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { ChangeDefaultPassword } from "./ChangeDefaultPassword";

afterEach(() => vi.restoreAllMocks());

const mustChange = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: true, createdAt: "", updatedAt: "" },
  permissions: [], via: "session", csrfToken: "c",
};

function serveStatus(status: unknown, code = 200) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/system/status")) return new Response(JSON.stringify(status), { status: code });
    return new Response(JSON.stringify(mustChange), { status: 200 });
  });
}

describe("ChangeDefaultPassword", () => {
  // Review minor 1: a signed-in page outside the Layout shows the real-money banner too.
  it("shows the real-money banner while real trading is armed", async () => {
    serveStatus({ version: "", build: "", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "",
      realArmed: [{ id: "r1", name: "Live" }], realArmedUnknown: false });
    render(<SessionProvider><ModeProvider><ChangeDefaultPassword /></ModeProvider></SessionProvider>);
    expect(await screen.findByText(/REAL MONEY — real trading armed on: Live/)).toBeInTheDocument();
  });

  it("shows no banner when nothing is armed", async () => {
    serveStatus({ version: "", build: "", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "",
      realArmed: [], realArmedUnknown: false });
    render(<SessionProvider><ModeProvider><ChangeDefaultPassword /></ModeProvider></SessionProvider>);
    expect(await screen.findByText(/still has the default password/)).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText(/REAL MONEY/)).not.toBeInTheDocument();
  });

  it("shows the unknown banner when the status cannot be read", async () => {
    serveStatus({ title: "Internal error", status: 500 }, 500);
    render(<SessionProvider><ModeProvider><ChangeDefaultPassword /></ModeProvider></SessionProvider>);
    expect(await screen.findByText(/real-trading state unknown/)).toBeInTheDocument();
  });

  it("refuses mismatched passwords before calling the server", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      new Response(JSON.stringify({
        user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: true, createdAt: "", updatedAt: "" },
        permissions: [], via: "session", csrfToken: "c",
      }), { status: 200 }),
    );
    render(<SessionProvider><ChangeDefaultPassword /></SessionProvider>);
    fireEvent.change(screen.getByLabelText("Current password"), { target: { value: "admin" } });
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "a long new password" } });
    fireEvent.change(screen.getByLabelText("New password again"), { target: { value: "something else!!" } });
    const calls = fetchMock.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("do not match");
    expect(fetchMock.mock.calls.length).toBe(calls);
  });
});
