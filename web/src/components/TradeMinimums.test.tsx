import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TradeMinimums } from "./TradeMinimums";

const limits = { tradingBudgetCents: 10000, dailyLossBp: 200, drawdownBp: 1000, maxPositionBp: 2000, maxOpenPositions: 8,
  noEntryBeforeCloseMinutes: 30, flattenBeforeCloseMinutes: 10, maxPortfolioAgeSeconds: 5, maxQuoteAgeSeconds: 30, maxShortBp: 500 };
const review = (over: object = {}) => ({
  id: "rv1", at: "2026-11-27T16:00:00Z", platform: "etoro", longCents: 1000, shortCents: 1000, longLowestCents: 1000, shortLowestCents: 1000,
  instruments: 12, longKnown: 12, shortKnown: 10, equityCents: 10000000, current: limits, proposed: { ...limits, maxShortBp: 1000 },
  longRoomCents: 10000, shortRoomCents: 500, changes: ["Short allowance 5.00 % → 10.00 % of the budget, so a minimum short ($10.00) fits"],
  problems: [], status: "pending", ...over,
});

function serve(view: object, decided?: object) {
  const calls: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    calls.push(`${init?.method ?? "GET"} ${url}`);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    if (url.endsWith("/decision")) return json(decided);
    return json(view);
  });
  return calls;
}

afterEach(() => vi.restoreAllMocks());

describe("TradeMinimums", () => {
  it("shows the minimums and the proposal, and approves only on a click", async () => {
    const calls = serve({ review: review(), canDecide: true },
      { review: review({ status: "approved", decidedBy: "admin", decidedAt: "2026-11-27T16:05:00Z" }), canDecide: false });
    const approved = vi.fn();
    render(<TradeMinimums reloadKey="a" onApproved={approved} />);
    expect(await screen.findByText("waiting for your approval")).toBeInTheDocument();
    expect(screen.getByText(/Short allowance 5.00 % → 10.00 %/)).toBeInTheDocument();
    expect(calls.some((c) => c.includes("/decision"))).toBe(false); // nothing changes on its own
    fireEvent.click(screen.getByRole("button", { name: /Approve the new limits/ }));
    await waitFor(() => expect(approved).toHaveBeenCalled());
    expect(calls).toContain("POST /api/v1/risk/minimums/rv1/decision");
    expect(await screen.findByText(/approved by admin/)).toBeInTheDocument();
  });

  it("cannot approve while the server says no", async () => {
    serve({ review: review(), canDecide: false, decideHint: "stop the agent first" });
    render(<TradeMinimums reloadKey="a" onApproved={() => {}} />);
    const btn = await screen.findByRole("button", { name: /Approve the new limits/ });
    expect(btn).toBeDisabled();
    expect(screen.getByText("stop the agent first.")).toBeInTheDocument();
  });

  it("says when nothing was checked yet", async () => {
    serve({ canDecide: false });
    render(<TradeMinimums reloadKey="a" onApproved={() => {}} />);
    expect(await screen.findByText("Not checked yet.")).toBeInTheDocument();
  });
});
