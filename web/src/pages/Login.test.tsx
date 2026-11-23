import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { Login } from "./Login";

describe("Login", () => {
  it("shows the sign-in form, the default-admin hint and the version", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url.endsWith("/api/v1/public/status")) return new Response(JSON.stringify({ version: "0.1.0" }), { status: 200 });
      return new Response(JSON.stringify({ title: "Unauthorized", status: 401 }), { status: 401 });
    });
    render(<MemoryRouter><SessionProvider><Login /></SessionProvider></MemoryRouter>);
    expect(await screen.findByText("v0.1.0")).toBeInTheDocument();
    expect(screen.getByText("Sign in", { selector: "h2" })).toBeInTheDocument();
    expect(screen.getByLabelText("Username")).toBeInTheDocument();
    expect(screen.getByText(/you will be asked to choose a new password/)).toBeInTheDocument();
  });
});
