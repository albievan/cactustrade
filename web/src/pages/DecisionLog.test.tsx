import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ModeProvider } from "../lib/mode";
import { DecisionLog, cost } from "./DecisionLog";

const account = { id: "a1", name: "Main demo", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "" };
const status = { version: "0.1.0", build: "1", mode: "demo", agentState: "running", database: "sqlite", startedAt: "", now: "", account };

const base = {
  accountId: "a1", instrumentId: 1001, symbol: "AAPL", direction: "long", playbook: "trend_breakout", signalReason: "close above the 20-bar high",
  entry: "100", stopLoss: "97", takeProfit: "106", suggestedSizeCents: 200000, regimeProbs: { trending: 0.78, choppy: 0.22 },
  sizeBp: 0, verdictReason: "", llmCallId: "", riskAllowed: false, riskSizeCents: 0, riskReason: "", orderRequestId: "", orderState: "",
};
const ordered = { ...base, id: "d2", at: "2026-11-24T15:05:05Z", verdict: "approve", sizeBp: 10000, verdictReason: "clean breakout", llmCallId: "c1",
  riskAllowed: true, riskSizeCents: 150000, riskReason: "allowed $1500.00 of $2000.00 requested", outcome: "ordered",
  orderRequestId: "11111111-1111-4111-8111-111111111111", orderState: "filled" };
const rejected = { ...base, id: "d1", at: "2026-11-24T15:00:05Z", symbol: "MSFT", verdict: "", outcome: "rejected_risk", riskReason: "the price is stale" };
const call = { id: "c1", purpose: "agent", profileId: "p1", provider: "anthropic", model: "claude-opus-5-5",
  prompt: '{"messages":[{"role":"user","content":"Proposed trade: LONG AAPL"}]}', response: '{"verdict":"approve"}',
  inputTokens: 1234, outputTokens: 56, latencyMs: 1500, costUsd: "0.004542", error: "", at: "2026-11-24T15:05:06Z" };

function serve(rows: unknown[]) {
  const urls: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    urls.push(url);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/api/v1/system/status")) return json(status);
    if (url.includes("/api/v1/decisions/d2")) return json({ decision: ordered, call });
    if (url.includes("/api/v1/decisions/d1")) return json({ decision: rejected });
    if (url.includes("/api/v1/decisions?")) {
      const outcome = new URL(url, "http://x").searchParams.get("outcome");
      return json(outcome ? (rows as { outcome: string }[]).filter((r) => r.outcome === outcome) : rows);
    }
    return new Response("{}", { status: 404 });
  });
  render(<ModeProvider><DecisionLog /></ModeProvider>);
  return urls;
}

afterEach(() => vi.restoreAllMocks());

describe("DecisionLog", () => {
  it("lists the decisions in a table with verdict, risk, outcome and order state", async () => {
    serve([ordered, rejected]);
    expect(await screen.findByText("AAPL")).toBeInTheDocument();
    const table = screen.getByRole("table");
    expect(within(table).getByText("approve")).toBeInTheDocument();
    expect(within(table).getByText("$1,500.00")).toBeInTheDocument();
    expect(within(table).getByText("ordered")).toBeInTheDocument();
    expect(within(table).getByText("filled")).toBeInTheDocument();
    expect(within(table).getByText("not reviewed")).toBeInTheDocument();
    expect(within(table).getByText("rejected: risk")).toBeInTheDocument();
    expect(within(table).getByText("refused")).toBeInTheDocument();
  });

  it("filters by outcome", async () => {
    const urls = serve([ordered, rejected]);
    await screen.findByText("AAPL");
    fireEvent.change(screen.getByLabelText("Outcome"), { target: { value: "rejected_risk" } });
    await waitFor(() => expect(screen.queryByText("AAPL")).not.toBeInTheDocument());
    expect(screen.getByText("MSFT")).toBeInTheDocument();
    expect(urls.some((u) => u.includes("outcome=rejected_risk"))).toBe(true);
  });

  it("opens a row: signal, regime, reasons, and the folded prompt and response with tokens, cost and latency", async () => {
    serve([ordered, rejected]);
    await screen.findByText("AAPL");
    const btn = screen.getByLabelText(/Show the details of AAPL/);
    expect(btn).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(btn);
    expect(await screen.findByText("Prompt as sent")).toBeInTheDocument();
    expect(screen.getByText("close above the 20-bar high")).toBeInTheDocument();
    expect(screen.getByText(/78 %/)).toBeInTheDocument();
    expect(screen.getByText(/clean breakout/)).toBeInTheDocument();
    expect(screen.getByText("allowed $1500.00 of $2000.00 requested")).toBeInTheDocument();
    expect(screen.getByText(/1,234 in \/ 56 out tokens/)).toBeInTheDocument();
    expect(screen.getByText(/cost \$0\.004542/)).toBeInTheDocument();
    expect(screen.getByText(/1,500 ms/)).toBeInTheDocument();
    // folded: inside closed <details>
    const prompt = screen.getByText(/Proposed trade: LONG AAPL/);
    expect(prompt.closest("details")).not.toHaveAttribute("open");
    expect(screen.getByText('{"verdict":"approve"}').closest("details")).not.toHaveAttribute("open");
    fireEvent.click(screen.getByLabelText(/Hide the details of AAPL/));
    expect(screen.queryByText("Prompt as sent")).not.toBeInTheDocument();
  });

  it("opens a row without a reviewer call", async () => {
    serve([rejected]);
    await screen.findByText("MSFT");
    fireEvent.click(screen.getByLabelText(/Show the details of MSFT/));
    expect(await screen.findByText("Not reviewed.")).toBeInTheDocument();
    expect(screen.queryByText("Prompt as sent")).not.toBeInTheDocument();
    expect(screen.getAllByText("the price is stale").length).toBeGreaterThan(0);
  });

  it("says when there are none, and shows an unknown cost as unknown", async () => {
    serve([]);
    expect(await screen.findByText("No decisions yet.")).toBeInTheDocument();
    expect(cost(undefined)).toBe("unknown");
    expect(cost("0")).toBe("$0");
  });
});
