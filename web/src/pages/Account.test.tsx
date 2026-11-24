import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { Account } from "./Account";

const me = {
  user: { id: "1", username: "vera", role: "operator", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view"], via: "session", csrfToken: "c",
};
const profile = { email: "vera@example.com", notify: { trades: true, watchlist: false, alerts: true } };

// serve answers the profile endpoints; a PUT answers refusal when given, else the body back.
function serve(refusal?: { status: number; body: unknown }) {
  const puts: Record<string, unknown>[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status });
    if (url.endsWith("/api/v1/auth/me")) return json(me);
    if (url.endsWith("/api/v1/me/profile") && init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>;
      puts.push(body);
      if (refusal) return json(refusal.body, refusal.status);
      return json({ email: body.email, notify: body.notify });
    }
    if (url.endsWith("/api/v1/me/profile")) return json(profile);
    return json({}, 404);
  });
  render(<SessionProvider><Account /></SessionProvider>);
  return puts;
}

afterEach(() => vi.restoreAllMocks());

describe("Account e-mail (PLAN 7.1.11)", () => {
  it("asks for the current password only when the address changes, and sends it", async () => {
    const puts = serve();
    const email = await screen.findByLabelText("Your e-mail address");
    expect(screen.queryByLabelText("Current password (to change the address)")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save e-mail settings" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).not.toHaveProperty("currentPassword");

    fireEvent.change(email, { target: { value: "new@example.com" } });
    const pw = screen.getByLabelText("Current password (to change the address)");
    expect(pw).toBeRequired();
    fireEvent.change(pw, { target: { value: "vera password!" } });
    fireEvent.click(screen.getByRole("button", { name: "Save e-mail settings" }));
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1]).toMatchObject({ email: "new@example.com", currentPassword: "vera password!" });
    expect(await screen.findByRole("status")).toHaveTextContent("Saved.");
    expect(screen.queryByLabelText("Current password (to change the address)")).not.toBeInTheDocument();
  });

  it("shows the server's refusal as a problem alert", async () => {
    serve({ status: 400, body: { title: "Invalid input", status: 400, detail: "currentPassword: is not correct", field: "currentPassword" } });
    fireEvent.change(await screen.findByLabelText("Your e-mail address"), { target: { value: "new@example.com" } });
    fireEvent.change(screen.getByLabelText("Current password (to change the address)"), { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: "Save e-mail settings" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("is not correct");
  });
});
