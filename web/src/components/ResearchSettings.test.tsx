import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ResearchSettings } from "./ResearchSettings";
import type { LlmSetup } from "../lib/api";

const setup: LlmSetup = {
  settings: { enabled: true, activeProfileId: "gpt" },
  defaultAnthropicModel: "claude-opus-5-5",
  providers: [
    { id: "anthropic", label: "Anthropic (Claude)", defaultBaseUrl: "", keyRequired: true, note: "", webSearch: true },
    { id: "openai", label: "OpenAI", defaultBaseUrl: "", keyRequired: true, note: "", webSearch: false },
    { id: "openrouter", label: "OpenRouter", defaultBaseUrl: "", keyRequired: true, note: "", webSearch: true },
  ],
  profiles: [
    { id: "gpt", name: "GPT", provider: "openai", model: "gpt", baseUrl: "", timeoutSeconds: 0, keySet: true, inputPricePerMTok: "", outputPricePerMTok: "" },
    { id: "cl", name: "Claude", provider: "anthropic", model: "", baseUrl: "", timeoutSeconds: 0, keySet: true, inputPricePerMTok: "", outputPricePerMTok: "" },
  ],
};

describe("ResearchSettings (R29)", () => {
  afterEach(() => vi.restoreAllMocks());

  it("offers only profiles that can search the web, explains why research is off, and saves", async () => {
    const puts: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (String(input).endsWith("/api/v1/llm/research")) {
        if (init?.method === "PUT") {
          const body = JSON.parse(String(init.body));
          puts.push(body);
          return json({ config: body, profileId: "cl", profileName: "Claude", missing: [] });
        }
        return json({
          config: { profileId: "", maxShares: 10, runsPerDay: 3 }, missing: [{ what: "llm_profile", canFix: true }],
          note: "GPT uses OpenAI, which has no built-in web search here (research not supported)",
        });
      }
      return new Response("{}", { status: 404 });
    });
    render(<MemoryRouter><ResearchSettings setup={setup} admin /></MemoryRouter>);
    expect(await screen.findByText(/research not supported\)/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /^Fix reviewer/ })).toBeInTheDocument();
    const select = screen.getByLabelText("Research profile");
    const names = within(select).getAllByRole("option").map((o) => o.textContent);
    expect(names).toEqual(["Default reviewer profile", "Claude"]);
    fireEvent.change(select, { target: { value: "cl" } });
    fireEvent.change(screen.getByLabelText("Shares per run"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Runs per day"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(puts).toEqual([{ profileId: "cl", maxShares: 5, runsPerDay: 1 }]));
    expect(await screen.findByText("Saved and audited.")).toBeInTheDocument();
  });

  it("is read-only for someone who may not change settings", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      new Response(JSON.stringify({ config: { profileId: "", maxShares: 10, runsPerDay: 3 }, profileId: "cl", profileName: "Claude", missing: [] }), { status: 200 }));
    render(<MemoryRouter><ResearchSettings setup={setup} admin={false} /></MemoryRouter>);
    expect(await screen.findByLabelText("Research profile")).toBeDisabled();
    expect(screen.queryByRole("button", { name: /Save/ })).not.toBeInTheDocument();
  });
});
