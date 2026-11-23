import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { Themes } from "./Themes";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "settings.write"], via: "session", csrfToken: "c",
};
const status = (account: unknown) => ({ version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "2026-11-23T09:00:00Z", now: "2026-11-23T09:00:00Z", account });
const acct = { id: "a1", name: "Main", ownerId: "1", ownerName: "admin", mode: "demo", envKeys: false, access: "owner", createdAt: "2026-11-23T09:00:00Z" };
const theme = {
  id: "t1", name: "A.I", keywords: ["ai"], industryId: 0, enabled: true, instruments: [1137, 4481], llmProfileId: "p2",
  shares: [{ instrumentId: 1137, symbol: "NVDA", name: "NVIDIA Corporation", market: "NYSE" }, { instrumentId: 4481, symbol: "TSM", name: "Taiwan Semiconductor", market: "NYSE" }],
  createdAt: "2026-11-23T09:00:00Z", updatedAt: "2026-11-23T09:00:00Z",
};
const llm = { settings: { enabled: true, activeProfileId: "p1" }, defaultAnthropicModel: "claude-opus-5-5", providers: [],
  profiles: [{ id: "p1", name: "Claude", provider: "anthropic", model: "", baseUrl: "", timeoutSeconds: 0, keySet: true },
    { id: "p2", name: "DeepSeek desk", provider: "deepseek", model: "deepseek-chat", baseUrl: "", timeoutSeconds: 0, keySet: true }] };

describe("Themes", () => {
  it("shows a theme's tickers and its reviewer, and removes a ticker", async () => {
    const puts: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json(status(acct));
      if (url.endsWith("/api/v1/themes/t1") && init?.method === "PUT") { puts.push(JSON.parse(String(init.body))); return json(theme); }
      if (url.endsWith("/api/v1/themes")) return json([theme]);
      if (url.endsWith("/api/v1/llm")) return json(llm);
      if (url.includes("/api/v1/reviews")) return json({ reviews: [], scannerEnabled: true });
      if (url.endsWith("/api/v1/industries")) return json([]);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><ModeProvider><Themes /></ModeProvider></SessionProvider>);
    expect(await screen.findByText("NVDA")).toBeInTheDocument();
    expect(screen.getByText("TSM")).toBeInTheDocument();
    expect(screen.getByText("LLM reviewer:").parentElement).toHaveTextContent("LLM reviewer: DeepSeek desk");
    fireEvent.click(screen.getByLabelText("Remove TSM"));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect((puts[0] as { instruments: number[] }).instruments).toEqual([1137]);
  });

  it("explains what to do without an eToro account", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json(status(undefined));
      return new Response("{}", { status: 404 });
    });
    render(<MemoryRouter><SessionProvider><ModeProvider><Themes /></ModeProvider></SessionProvider></MemoryRouter>);
    expect(await screen.findByText("No eToro account yet")).toBeInTheDocument();
  });
});
