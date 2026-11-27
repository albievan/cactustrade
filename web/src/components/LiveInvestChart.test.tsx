import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LiveInvestChart, addPoint, pointOf } from "./LiveInvestChart";
import type { LiveSnapshot } from "../lib/live";

const pos = (long: boolean, amountCents: number, unrealisedPnlCents: number) => ({ long, amountCents, unrealisedPnlCents });
const snap = (at: string, positions: ReturnType<typeof pos>[], equityCents = 10_000_000) =>
  ({ figures: { asOf: at, equityCents }, positions } as unknown as LiveSnapshot);

afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

describe("LiveInvestChart", () => {
  it("adds up long and short worth and open P&L from a snapshot", () => {
    expect(pointOf(snap("2026-11-27T16:00:00Z", [pos(true, 40000, 120), pos(true, 80000, -500), pos(false, 20000, 300)])))
      .toEqual({ at: "2026-11-27T16:00:00Z", long: 119620, short: 20300, pnl: -80, equity: 10_000_000 });
    expect(pointOf(null)).toBeNull();
  });

  it("keeps one point per time, oldest first, capped", () => {
    const p = (at: string) => ({ at, long: 1, short: 0, pnl: 0, equity: 1 });
    let l = addPoint([], p("2026-11-27T16:00:10Z"));
    l = addPoint(l, p("2026-11-27T16:00:05Z"));
    l = addPoint(l, p("2026-11-27T16:00:10Z"));
    expect(l.map((x) => x.at)).toEqual(["2026-11-27T16:00:05Z", "2026-11-27T16:00:10Z"]);
    expect(addPoint(l, p("2026-11-27T16:00:15Z"), 2).map((x) => x.at)).toEqual(["2026-11-27T16:00:10Z", "2026-11-27T16:00:15Z"]);
  });

  it("draws the graph as snapshots arrive", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("[]", { status: 200 }));
    const { rerender } = render(<LiveInvestChart accountId="a1" snapshot={snap("2026-11-27T16:00:00Z", [pos(true, 40000, 0)])} />);
    expect(screen.getByText("The graph fills in as the live figures arrive.")).toBeInTheDocument();
    rerender(<LiveInvestChart accountId="a1" snapshot={snap("2026-11-27T16:00:05Z", [pos(true, 40000, 250), pos(false, 10000, 0)])} />);
    expect(await screen.findByRole("img", { name: "Live graph: Long, Short" })).toBeInTheDocument();
    expect(screen.getByText(/Long \$402\.50/)).toBeInTheDocument();
  });
});
