import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { EquityCharts } from "./EquityChart";
import type { EquitySeries } from "../lib/api";

function series(values: number[]): EquitySeries {
  return {
    accountId: "a1", name: "Main demo", platform: "etoro", mode: "demo", currency: "USD",
    points: values.map((equity, i) => ({ at: new Date(Date.UTC(2026, 10, 20, 15, i * 15)).toISOString(), equity, cash: 0 })),
  };
}

describe("EquityCharts drawdown shading", () => {
  it("shades the fall below the highest equity so far", () => {
    render(<EquityCharts series={[series([100000, 120000, 90000, 110000])]} days={30} drawdown />);
    expect(screen.getByTestId("drawdown")).toBeInTheDocument();
  });

  it("shades nothing for a series that never falls", () => {
    render(<EquityCharts series={[series([100000, 100000, 120000])]} days={30} drawdown />);
    expect(screen.queryByTestId("drawdown")).not.toBeInTheDocument();
  });

  it("shades nothing unless asked (the Overview)", () => {
    render(<EquityCharts series={[series([100000, 120000, 90000])]} days={30} />);
    expect(screen.queryByTestId("drawdown")).not.toBeInTheDocument();
  });
});
