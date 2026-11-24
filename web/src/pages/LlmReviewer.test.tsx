import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { SessionProvider } from "../lib/session";
import { LlmReviewer } from "./LlmReviewer";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "settings.write"], via: "session", csrfToken: "c",
};
const llm = { settings: { enabled: true, activeProfileId: "p1" }, defaultAnthropicModel: "claude-opus-5-5", providers: [],
  profiles: [{ id: "p1", name: "Claude", provider: "anthropic", model: "", baseUrl: "", timeoutSeconds: 0, keySet: true },
    { id: "p2", name: "DeepSeek desk", provider: "deepseek", model: "deepseek-chat", baseUrl: "", timeoutSeconds: 0, keySet: false }] };

describe("LlmReviewer", () => {
  it("shows profiles as a grid of cards with icon buttons and makes one the default", async () => {
    const puts: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/llm") && init?.method === "PUT") {
        puts.push(JSON.parse(String(init.body)));
        return json({ ...llm, settings: { enabled: true, activeProfileId: "p2" } });
      }
      if (url.endsWith("/api/v1/llm")) return json(llm);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><LlmReviewer /></SessionProvider>);
    expect(await screen.findByRole("group", { name: "Actions for DeepSeek desk" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Actions for Claude" })).toBeInTheDocument();
    expect(screen.getByLabelText("Make Claude the default")).toBeDisabled();
    expect(screen.getByText("no key")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "anthropic logo" })).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Make DeepSeek desk the default"));
    await waitFor(() => expect(puts).toEqual([{ enabled: true, activeProfileId: "p2" }]));
  });
});

describe("InfoCard", () => {
  it("folds the explanation away and back", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url.endsWith("/api/v1/auth/me")) return new Response(JSON.stringify(me), { status: 200 });
      if (url.endsWith("/api/v1/llm")) return new Response(JSON.stringify(llm), { status: 200 });
      return new Response("{}", { status: 404 });
    });
    localStorage.removeItem("cactustrade.info.llm");
    render(<SessionProvider><LlmReviewer /></SessionProvider>);
    const toggle = await screen.findByRole("button", { name: /What it does/ });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(/second pair of eyes/)).toBeInTheDocument();
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText(/second pair of eyes/)).not.toBeInTheDocument();
    expect(localStorage.getItem("cactustrade.info.llm")).toBe("closed");
  });
});

describe("LlmReviewer prices and usage", () => {
  const priced = { ...llm, profiles: [{ ...llm.profiles[0], inputPricePerMTok: "3", outputPricePerMTok: "15" },
    { ...llm.profiles[1], inputPricePerMTok: "", outputPricePerMTok: "" }] };
  const usage = { days: 30, since: "2026-10-25T00:00:00Z",
    rows: [
      { profileId: "p1", profileName: "Claude", purpose: "agent", calls: 4, failed: 1, inputTokens: 4000, outputTokens: 400, knownCostUsd: "0.018", costUnknownCalls: 0, costUsd: "0.018" },
      { profileId: "gone", profileName: "", purpose: "test", calls: 1, failed: 0, inputTokens: 10, outputTokens: 2, knownCostUsd: "0", costUnknownCalls: 1 },
    ],
    total: { profileId: "", profileName: "", purpose: "", calls: 5, failed: 1, inputTokens: 4010, outputTokens: 402, knownCostUsd: "0.018", costUnknownCalls: 1 } };

  function serve() {
    const saved: unknown[] = [];
    const usageUrls: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.includes("/api/v1/llm/usage")) { usageUrls.push(url); return json(usage); }
      if (url.endsWith("/api/v1/llm/profiles/p1") && init?.method === "PUT") {
        saved.push(JSON.parse(String(init.body)));
        return json(priced.profiles[0]);
      }
      if (url.endsWith("/api/v1/llm/profiles/p1/test")) {
        return json({ ok: true, verdict: "approve", reason: "fine", latencyMs: 900, model: "claude-sonnet-fallback", inputTokens: 812, outputTokens: 40, costUsd: "0.003036", callId: "c9" });
      }
      if (url.endsWith("/api/v1/llm")) return json(priced);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><LlmReviewer /></SessionProvider>);
    return { saved, usageUrls };
  }

  it("shows the prices and keeps them when a profile is saved", async () => {
    const { saved } = serve();
    expect(await screen.findByText("$3 in / $15 out per M tokens")).toBeInTheDocument();
    expect(screen.getByText("prices unknown")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Edit Claude"));
    expect(screen.getByLabelText("Input price")).toHaveValue("3");
    expect(screen.getByLabelText("Output price")).toHaveValue("15");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Claude 2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save profile" }));
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0]).toMatchObject({ name: "Claude 2", inputPricePerMTok: "3", outputPricePerMTok: "15" });
  });

  it("sends a typed price", async () => {
    const { saved } = serve();
    fireEvent.click(await screen.findByLabelText("Edit Claude"));
    fireEvent.change(screen.getByLabelText("Output price"), { target: { value: "0.25" } });
    fireEvent.click(screen.getByRole("button", { name: "Save profile" }));
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0]).toMatchObject({ inputPricePerMTok: "3", outputPricePerMTok: "0.25" });
  });

  it("shows the usage card with unknown costs as unknown and changes the period", async () => {
    const { usageUrls } = serve();
    expect(await screen.findByRole("heading", { name: "Usage" })).toBeInTheDocument();
    expect(await screen.findByText("$0.018")).toBeInTheDocument();
    expect(screen.getByText("deleted profile")).toBeInTheDocument();
    expect(screen.getByText("unknown")).toBeInTheDocument();
    expect(screen.getByTestId("usage-total-cost")).toHaveTextContent("at least $0.018 (1 unpriced)");
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "7" } });
    await waitFor(() => expect(usageUrls.some((u) => u.endsWith("days=7"))).toBe(true));
  });

  it("shows the served model, the tokens and the cost of a test", async () => {
    serve();
    fireEvent.click(await screen.findByLabelText("Test Claude"));
    expect(await screen.findByText("claude-sonnet-fallback")).toBeInTheDocument();
    expect(screen.getByText(/812 in \/ 40 out tokens · cost \$0\.003036/)).toBeInTheDocument();
  });
});

describe("LlmReviewer missing key (R22)", () => {
  it("offers to open the profile's editor when its test needs a key", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/llm/profiles/p2/test") && init?.method === "POST") {
        return json({ title: "Invalid input", status: 400, field: "apiKey", detail: "DeepSeek desk has no API key yet", missing: "llm_profile", canFix: true }, 400);
      }
      if (url.endsWith("/api/v1/llm")) return json(llm);
      return new Response("{}", { status: 404 });
    });
    render(<MemoryRouter><SessionProvider><LlmReviewer /></SessionProvider></MemoryRouter>);
    fireEvent.click(await screen.findByLabelText("Test DeepSeek desk"));
    expect((await screen.findByText("DeepSeek desk has no API key yet")).closest(".alert")).toHaveClass("alert-danger");
    fireEvent.click(screen.getByRole("button", { name: /^Fix reviewer/ }));
    expect(screen.queryByText("DeepSeek desk has no API key yet")).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Actions for DeepSeek desk" })).not.toBeInTheDocument(); // its card became the form
  });
});
