import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { ChangeDefaultPassword } from "./ChangeDefaultPassword";

describe("ChangeDefaultPassword", () => {
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
