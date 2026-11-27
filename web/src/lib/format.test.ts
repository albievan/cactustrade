import { describe, expect, it } from "vitest";
import { dateTime, hourMinute } from "./format";

describe("format", () => {
  it("writes the full local date and time", () => {
    const iso = new Date(2026, 10, 26, 20, 5).toISOString();
    expect(dateTime(iso)).toBe("26/11/2026 20:05");
    expect(hourMinute(iso)).toBe("20:05");
    expect(dateTime("not a time")).toBe("—");
  });
});
