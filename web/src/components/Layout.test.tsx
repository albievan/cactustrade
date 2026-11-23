import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { Layout } from "./Layout";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "settings.write"], via: "session", csrfToken: "c",
};
const status = { version: "0.1.0", build: "15", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "2026-11-23T09:00:00Z", now: "2026-11-23T09:00:00Z" };

describe("Layout", () => {
  it("shows the version under the brand and Overview first under Trading", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json(status);
      if (url.endsWith("/api/v1/accounts")) return json([]);
      if (url.includes("/api/v1/events")) return json({ events: [], unseen: 0 });
      return new Response("{}", { status: 404 });
    });
    render(<MemoryRouter><SessionProvider><ModeProvider><Layout /></ModeProvider></SessionProvider></MemoryRouter>);
    expect(await screen.findByText("v0.1.0 · build 15")).toBeInTheDocument();
    const links = screen.getAllByRole("link").map((l) => l.textContent);
    expect(links.indexOf("Overview")).toBeLessThan(links.indexOf("Dashboard"));
    expect(links).toContain("Platform accounts");
  });
});
