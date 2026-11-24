import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { Layout } from "./Layout";
import appSource from "../App.tsx?raw";

// Every route of the signed-in app (App.tsx), checked below against its source so a new page
// cannot miss the banner.
const paths = ["/", "/risk", "/llm", "/email", "/system", "/overview", "/watchlist", "/orders", "/decisions", "/history",
  "/accounts", "/real-trading", "/themes", "/users", "/api-keys", "/audit", "/account", "/credentials"];

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "settings.write", "users.manage", "audit.view"], via: "session", csrfToken: "c",
};

function serve(realArmed: { id: string; name: string }[], mode = "demo") {
  const status = { version: "0.1.0", build: "15", mode, agentState: "stopped", database: "sqlite", startedAt: "", now: "", realArmed };
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/auth/me")) return json(me);
    if (url.endsWith("/api/v1/system/status")) return json(status);
    if (url.endsWith("/api/v1/accounts")) return json([]);
    if (url.includes("/api/v1/events")) return json({ events: [], unseen: 0 });
    return new Response("{}", { status: 404 });
  });
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <SessionProvider><ModeProvider>
        <Routes><Route element={<Layout />}><Route path="*" element={<p>page {path}</p>} /></Route></Routes>
      </ModeProvider></SessionProvider>
    </MemoryRouter>,
  );
}

afterEach(() => vi.restoreAllMocks());

describe("RealBanner in the Layout (OD27)", () => {
  it("covers every route of the app", () => {
    const declared = [...appSource.matchAll(/<Route path="([^"*]+)"/g)].map((m) => "/" + m[1]);
    for (const p of declared) expect(paths).toContain(p);
  });

  it.each(paths)("is on %s while real trading is armed", async (path) => {
    serve([{ id: "r1", name: "Live" }, { id: "r2", name: "ISA live" }]);
    const { unmount } = renderAt(path);
    const banner = await screen.findByText(/REAL MONEY — real trading armed on: Live, ISA live/);
    expect(banner).toBeInTheDocument();
    expect(banner.closest("[role=alert]")).not.toBeNull();
    expect(screen.queryByRole("button", { name: /close|dismiss/i })).not.toBeInTheDocument();
    unmount();
  });

  it.each(paths)("is absent on %s when nothing is armed", async (path) => {
    serve([], "real"); // a real current account alone shows R3's frame, not the banner
    const { unmount } = renderAt(path);
    expect(await screen.findByText("page " + path)).toBeInTheDocument();
    await screen.findByText("v0.1.0 · build 15"); // the status has arrived
    expect(screen.queryByText(/real trading armed on/)).not.toBeInTheDocument();
    unmount();
  });
});
