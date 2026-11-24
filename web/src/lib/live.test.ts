import { describe, expect, it } from "vitest";
import { liveUrl, upsertOrder } from "./live";
import type { Order } from "./api";
import { duration } from "../components/LiveCards";

const o = (requestId: string, state = "sent"): Order => ({
  requestId, mode: "demo", kind: "open", kindLabel: "Buy AAPL", direction: "long", instrumentId: 1, symbol: "AAPL", amountCents: 100,
  state, stateLabel: state, reason: "", sends: 1, createdAt: "", updatedAt: "",
});

describe("live helpers", () => {
  it("escapes the account id in the stream's address", () => {
    expect(liveUrl("a 1&x")).toBe("/api/v1/live?accountId=a%201%26x");
  });

  it("replaces a known order in place and puts a new one first, keeping the feed's size", () => {
    expect(upsertOrder([o("a"), o("b")], o("b", "filled")).map((x) => x.requestId + x.state)).toEqual(["asent", "bfilled"]);
    expect(upsertOrder([o("a"), o("b")], o("c"), 2).map((x) => x.requestId)).toEqual(["c", "a"]);
  });

  it.each([
    [0, "passed"], [-5, "passed"], [30, "in 1 min"], [600, "in 10 min"], [3900, "in 1 h 05 min"],
  ])("formats %i seconds as %s", (s, want) => {
    expect(duration(s)).toBe(want);
  });
});
