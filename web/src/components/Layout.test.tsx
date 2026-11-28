import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { PrefsProvider, usePref } from "../lib/prefs";
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
    expect(links.indexOf("Orders")).toBeGreaterThan(links.indexOf("Dashboard")); // under Trading
    expect(links.indexOf("Decision log")).toBe(links.indexOf("Orders") + 1);
    // R31: Analysis under Trading, right after History, with the graph-up-arrow icon.
    expect(links.indexOf("Analysis")).toBe(links.indexOf("History") + 1);
    const analysis = screen.getAllByRole("link").find((l) => l.textContent === "Analysis");
    expect(analysis).toHaveAttribute("href", "/analysis");
    expect(analysis?.querySelector("i.bi-graph-up-arrow")).not.toBeNull();
    // R48: Backups under Setup, right after E-mail, for admins.
    expect(links.indexOf("Backups")).toBe(links.indexOf("E-mail") + 1);
    const backups = screen.getAllByRole("link").find((l) => l.textContent === "Backups");
    expect(backups).toHaveAttribute("href", "/backups");
    expect(backups?.querySelector("i.bi-archive")).not.toBeNull();
  });

  it("shows Backups only to admins (settings.write)", async () => {
    const operator = { ...me, user: { ...me.user, role: "operator" }, permissions: ["view", "agent.control"] };
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(operator);
      if (url.endsWith("/api/v1/system/status")) return json(status);
      if (url.endsWith("/api/v1/accounts")) return json([]);
      if (url.includes("/api/v1/events")) return json({ events: [], unseen: 0 });
      return new Response("{}", { status: 404 });
    });
    render(<MemoryRouter><SessionProvider><ModeProvider><Layout /></ModeProvider></SessionProvider></MemoryRouter>);
    expect(await screen.findByText("v0.1.0 · build 15")).toBeInTheDocument();
    const links = screen.getAllByRole("link").map((l) => l.textContent);
    expect(links).toContain("Dashboard");
    expect(links).not.toContain("Backups");
    expect(links).not.toContain("E-mail");
  });

  // R50 review fix (2026-11-28): a choice made just before signing out reaches the server
  // before the sign-out, even while an earlier save is still on its way; then this browser's
  // copy is removed.
  it("signing out right after a change saves it first and clears the local copy", async () => {
    const calls: string[] = [];
    const prefs: Record<string, unknown> = {};
    let putsInFlight = 0;
    let maxInFlight = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/me/preferences") && init?.method === "PUT") {
        putsInFlight++;
        maxInFlight = Math.max(maxInFlight, putsInFlight);
        await new Promise((r) => setTimeout(r, 50)); // a slow server
        putsInFlight--;
        Object.assign(prefs, JSON.parse(String(init.body)));
        calls.push("put " + String(init.body));
        return json(prefs);
      }
      if (url.endsWith("/api/v1/me/preferences")) return json(prefs);
      if (url.endsWith("/api/v1/auth/logout")) {
        calls.push("logout");
        return new Response(null, { status: 204 });
      }
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json(status);
      if (url.endsWith("/api/v1/accounts")) return json([]);
      if (url.includes("/api/v1/events")) return json({ events: [], unseen: 0 });
      return new Response("{}", { status: 404 });
    });
    function RangePicker() {
      const [range, setRange] = usePref<string>("dashboard.liveInvest.range", "day");
      return <div><span data-testid="range">{range}</span><button onClick={() => setRange("week")}>Week</button><button onClick={() => setRange("max")}>Max</button></div>;
    }
    render(
      <MemoryRouter><SessionProvider><ModeProvider><PrefsProvider userId="1">
        <Routes><Route element={<Layout />}><Route index element={<RangePicker />} /></Route></Routes>
      </PrefsProvider></ModeProvider></SessionProvider></MemoryRouter>,
    );
    fireEvent.click(await screen.findByText("Week"));
    // let the first save start (the 500 ms pause), then change again and sign out at once
    await waitFor(() => expect(putsInFlight).toBe(1), { timeout: 2000 });
    fireEvent.click(screen.getByText("Max"));
    expect(screen.getByTestId("range")).toHaveTextContent("max");
    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(calls).toContain("logout"), { timeout: 2000 });
    expect(calls).toEqual([
      `put {"dashboard.liveInvest.range":"week"}`,
      `put {"dashboard.liveInvest.range":"max"}`,
      "logout",
    ]);
    expect(prefs["dashboard.liveInvest.range"]).toBe("max");
    expect(maxInFlight).toBe(1);
    expect(localStorage.getItem("cactustrade.prefs.1")).toBeNull();
  });
});
