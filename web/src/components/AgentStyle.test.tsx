import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import type { AgentStyle, RegimeText, ReviewBoard as Board, RiskPresetList } from "../lib/api";
import type { LiveRegime } from "../lib/live";
import { RiskPresets } from "./RiskPresets";
import { AgentStylePanel } from "./AgentStylePanel";
import { RegimeList } from "./LiveCards";
import { ReviewBoard } from "./ReviewBoard";
import presetsSource from "./RiskPresets.tsx?raw";
import styleSource from "./AgentStylePanel.tsx?raw";

// R28: risk presets, the agent's aggressiveness and autonomy, the market mood card and the
// scanner's proposals. Every word and every decision comes from the server.

interface Call { method: string; url: string; body?: unknown }

function serve(routes: Record<string, (body: unknown) => unknown>): Call[] {
  const calls: Call[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url, body });
    const h = routes[method + " " + url];
    if (!h) return new Response("{}", { status: 404 });
    return new Response(JSON.stringify(h(body)), { status: 200 });
  });
  return calls;
}

afterEach(() => vi.restoreAllMocks());

const limits = { tradingBudgetCents: 500000, dailyLossBp: 50, drawdownBp: 300, maxPositionBp: 500, maxOpenPositions: 3,
  noEntryBeforeCloseMinutes: 30, flattenBeforeCloseMinutes: 10, maxPortfolioAgeSeconds: 5, maxQuoteAgeSeconds: 30, maxShortBp: 0 };

const presets: RiskPresetList = {
  current: "medium", clamped: false,
  presets: [
    { id: "low", label: "Low", summary: "stops buying after a 0.5 % loss in a day, no shorts", dailyLossBp: 50, drawdownBp: 300, maxPositionBp: 500, maxOpenPositions: 3, maxShortBp: 0 },
    { id: "low_medium", label: "Low–medium", summary: "s2", dailyLossBp: 100, drawdownBp: 500, maxPositionBp: 1000, maxOpenPositions: 5, maxShortBp: 0 },
    { id: "medium", label: "Medium", summary: "stops buying after a 2 % loss in a day", dailyLossBp: 200, drawdownBp: 1000, maxPositionBp: 2000, maxOpenPositions: 8, maxShortBp: 1000 },
    { id: "medium_high", label: "Medium–high", summary: "s4", dailyLossBp: 300, drawdownBp: 1500, maxPositionBp: 2500, maxOpenPositions: 12, maxShortBp: 2000 },
    { id: "high", label: "High", summary: "s5", dailyLossBp: 400, drawdownBp: 2000, maxPositionBp: 3000, maxOpenPositions: 15, maxShortBp: 2500 },
  ],
};

describe("RiskPresets", () => {
  it("shows each preset's numbers, marks the matching one and applies a preset with {preset} only", async () => {
    const calls = serve({
      "GET /api/v1/risk/presets": () => presets,
      "PUT /api/v1/risk/limits": () => ({ ...limits, canEdit: true, preset: "low" }),
    });
    const applied = vi.fn();
    render(<RiskPresets canEdit edited={false} reloadKey="a1" onApplied={applied} />);
    const group = await screen.findByRole("group", { name: "Risk presets" });
    const buttons = within(group).getAllByRole("button");
    expect(buttons.map((b) => b.querySelector(".fw-semibold")?.textContent)).toEqual(["Low", "Low–medium", "Medium", "Medium–high", "High", "Custom"]);
    expect(screen.getByRole("button", { name: /^Medium preset/ })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: /^Low preset/ })).toHaveTextContent("day loss 0.5 % · kill at −3 % · ≤ 5 % a share · ≤ 3 positions · no shorts");
    expect(screen.getByText(/stops buying after a 2 % loss in a day/)).toBeInTheDocument();
    for (const b of buttons) expect(b).toHaveAttribute("title");
    fireEvent.click(screen.getByRole("button", { name: /^Low preset/ }));
    await waitFor(() => expect(applied).toHaveBeenCalledWith({ ...limits, canEdit: true, preset: "low" }));
    expect(calls.filter((c) => c.method === "PUT")).toEqual([{ method: "PUT", url: "/api/v1/risk/limits", body: { preset: "low" } }]);
    expect(screen.getByRole("button", { name: /^Low preset/ })).toHaveAttribute("aria-pressed", "true");
  });

  it("shows Custom while the form is edited by hand, and is locked when the server says so", async () => {
    serve({ "GET /api/v1/risk/presets": () => presets });
    const { rerender } = render(<RiskPresets canEdit edited reloadKey="a1" onApplied={() => undefined} />);
    expect(await screen.findByRole("button", { name: /^Custom/ })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Custom: the limits below were set by hand.", { exact: false })).toBeInTheDocument();
    rerender(<RiskPresets canEdit={false} edited={false} reloadKey="a1" onApplied={() => undefined} />);
    for (const b of within(screen.getByRole("group", { name: "Risk presets" })).getAllByRole("button")) expect(b).toBeDisabled();
  });
});

const style: AgentStyle = {
  aggressiveness: "high", effectiveAggressiveness: "medium", autonomy: "medium", canEdit: true,
  policy: { researchMode: "auto_add", scanEveryMinutes: 15, ownerSharesOnly: false, autoAssignThemes: false, pruneStale: false, autoStart: false, adaptAggressiveness: false },
  aggressivenessLevels: ["low", "medium", "high", "full"].map((id) => ({ id: id as AgentStyle["aggressiveness"], label: id[0].toUpperCase() + id.slice(1), summary: `aggr ${id}` })),
  autonomyLevels: ["low", "medium", "high", "full"].map((id) => ({ id: id as AgentStyle["autonomy"], label: id[0].toUpperCase() + id.slice(1), summary: `auto ${id}` })),
};

describe("AgentStylePanel", () => {
  it("shows both levels in plain words and sends one change", async () => {
    const calls = serve({
      "GET /api/v1/agent/style": () => style,
      "PUT /api/v1/agent/style": (b) => ({ ...style, ...(b as object) }),
    });
    render(<AgentStylePanel reloadKey="a1" />);
    const aggr = await screen.findByRole("group", { name: "Aggressiveness" });
    const auto = screen.getByRole("group", { name: "Autonomy" });
    expect(within(aggr).getByRole("button", { name: /^Aggressiveness High/ })).toHaveAttribute("aria-pressed", "true");
    expect(within(auto).getByRole("button", { name: /^Autonomy Medium/ })).toHaveAttribute("aria-pressed", "true");
    const para = (text: string) => (_: string, el: Element | null) => el?.tagName === "P" && el.textContent === text;
    expect(screen.getByText(para("High: aggr high"))).toBeInTheDocument();
    expect(screen.getByText(para("Medium: auto medium"))).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(/uses medium for now/);
    fireEvent.click(within(auto).getByRole("button", { name: /^Autonomy Full/ }));
    expect(await screen.findByText("Autonomy is now full. Saved and audited.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "PUT")).toEqual([{ method: "PUT", url: "/api/v1/agent/style", body: { autonomy: "full" } }]);
  });

  it("is locked when the server says so", async () => {
    serve({ "GET /api/v1/agent/style": () => ({ ...style, canEdit: false, editBlockedReason: "Stop the agent" }) });
    render(<AgentStylePanel reloadKey="a1" />);
    const aggr = await screen.findByRole("group", { name: "Aggressiveness" });
    for (const b of within(aggr).getAllByRole("button")) expect(b).toBeDisabled();
  });

  it("has no role, access or state check of its own", () => {
    for (const src of [presetsSource, styleSource]) {
      for (const banned of [".role", ".access", ".via", "agentState", "permissions"]) expect(src).not.toContain(banned);
    }
  });
});

const legend: RegimeText[] = [
  { regime: "trending", label: "Trending", description: "moving steadily one way", agentMayDo: "the agent may buy breakouts or short breakdowns" },
  { regime: "choppy", label: "Choppy", description: "going sideways", agentMayDo: "the agent may buy dips back to the average" },
  { regime: "high_volatility", label: "Volatile", description: "big swings", agentMayDo: "the agent stays out" },
];

describe("RegimeList (Market mood)", () => {
  it("names the mood in plain words, with a legend, the chances, the time and what the agent may do", () => {
    const regimes: LiveRegime[] = [
      { instrumentId: 1, symbol: "AAPL", known: true, regime: "trending", probabilities: { trending: 0.65, choppy: 0.25, high_volatility: 0.1 },
        at: "2026-11-24T20:43:00Z", mood: legend[0], stance: "trend", mayDo: "May buy a breakout or short a breakdown" },
      { instrumentId: 2, symbol: "MSFT", known: false, stance: "unknown", mayDo: "Not reviewed yet" },
    ];
    render(<MemoryRouter><RegimeList regimes={regimes} legend={legend} /></MemoryRouter>);
    expect(screen.getByRole("heading", { name: "Market mood" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "What the agent may do" })).toBeInTheDocument();
    const key = screen.getByRole("list", { name: "Market mood legend" });
    expect(within(key).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "Trending — moving steadily one way (the agent may buy breakouts or short breakdowns)",
      "Choppy — going sideways (the agent may buy dips back to the average)",
      "Volatile — big swings (the agent stays out)",
    ]);
    const bar = screen.getByRole("img", { name: "Chances: trending 65%, choppy 25%, volatile 10%" });
    expect(bar.children).toHaveLength(3);
    expect(screen.getByText(/updated \d\d:\d\d/)).toBeInTheDocument();
    expect(screen.getByText("May buy a breakout or short a breakdown")).toBeInTheDocument();
    expect(screen.getByText("not reviewed yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /What is the market mood/ })).toBeInTheDocument();
  });
});

describe("ReviewBoard proposals (low autonomy)", () => {
  const board = (canAccept: boolean): Board => ({
    scannerEnabled: true, canAccept,
    reviews: [{ instrumentId: 7, symbol: "RKLB", theme: "Space", at: "2026-11-24T15:00:00Z", outcome: "proposed", checks: [], summary: "trend_breakout: up", market: "NYSE", exchange: "New York (NYSE/NASDAQ)" }],
  });

  it("adds a proposed share to the watchlist when the owner accepts it", async () => {
    const calls = serve({ "GET /api/v1/reviews?limit=200": () => board(true), "POST /api/v1/watchlist": () => ({}) });
    render(<MemoryRouter><ReviewBoard /></MemoryRouter>);
    expect(await screen.findByText("proposed for the watchlist")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add RKLB to the watchlist" }));
    expect(await screen.findByText("added")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toEqual([{ method: "POST", url: "/api/v1/watchlist", body: { instrumentId: 7 } }]);
  });

  it("offers no button without trade access", async () => {
    serve({ "GET /api/v1/reviews?limit=200": () => board(false) });
    render(<MemoryRouter><ReviewBoard /></MemoryRouter>);
    expect(await screen.findByText("proposed for the watchlist")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /to the watchlist/ })).not.toBeInTheDocument();
  });
});
