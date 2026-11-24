import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { StateSince, durationText } from "./StateSince";

describe("durationText", () => {
  it("formats seconds, minutes, hours and days", () => {
    expect(durationText(45_000)).toBe("45 s");
    expect(durationText(12 * 60_000)).toBe("12 min");
    expect(durationText((2 * 60 + 5) * 60_000)).toBe("2 h 5 min");
    expect(durationText((3 * 24 + 4) * 3_600_000)).toBe("3 d 4 h");
    expect(durationText(-5)).toBe("0 s");
  });
});

describe("StateSince", () => {
  it("shows how long the agent has been running", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-11-24T16:50:00Z"));
    render(<StateSince state="running" since="2026-11-24T14:45:00Z" />);
    expect(screen.getByText("running for 2 h 5 min")).toBeInTheDocument();
    vi.useRealTimers();
  });
  it("shows nothing when the start is unknown", () => {
    const { container } = render(<StateSince state="running" />);
    expect(container).toBeEmptyDOMElement();
  });
});
