import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LiveInvestChart, addPoint, pointOf, thin } from "./LiveInvestChart";
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

  it("thins a long series evenly, keeping the first and the last point", () => {
    const pts = Array.from({ length: 1001 }, (_, i) => i);
    const t = thin(pts, 11);
    expect(t).toEqual([0, 100, 200, 300, 400, 500, 600, 700, 800, 900, 1000]);
    expect(thin([1, 2, 3], 11)).toEqual([1, 2, 3]);
  });

  it("shows a week, month, year or max from the stored snapshots (R43)", async () => {
    const urls: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      urls.push(url);
      const days = Number(new URL(url, "https://x").searchParams.get("days"));
      const points = days === 1 ? [] : [
        { at: "2026-11-20T16:00:00Z", cash: 9_000_000, equity: 10_000_000 },
        { at: "2026-11-26T16:00:00Z", cash: 9_500_000, equity: 10_050_000 },
      ];
      return new Response(JSON.stringify([{ accountId: "a1", name: "A", platform: "etoro", mode: "demo", currency: "USD", points }]), { status: 200 });
    });
    render(<LiveInvestChart accountId="a1" snapshot={null} />);
    for (const label of ["Day", "Week", "Month", "Year", "Max"]) expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Week" }));
    expect(await screen.findByRole("img", { name: "Graph: Invested" })).toBeInTheDocument();
    expect(urls.some((u) => u.endsWith("/api/v1/overview/history?days=7"))).toBe(true);
    expect(screen.getByRole("button", { name: "Open P&L" })).toBeDisabled();
    expect(screen.getByText("20/11/2026 " + new Date("2026-11-20T16:00:00Z").toTimeString().slice(0, 5))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Max" }));
    fireEvent.click(screen.getByRole("button", { name: "Equity" }));
    expect(await screen.findByRole("img", { name: "Graph: Equity" })).toBeInTheDocument();
    expect(urls.some((u) => u.endsWith("days=400"))).toBe(true);
  });
});
