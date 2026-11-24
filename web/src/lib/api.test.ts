import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, currencyDecimals, money, request, setCsrf, viewOnly } from "./api";

afterEach(() => {
  vi.restoreAllMocks();
  setCsrf(undefined);
});

function mockFetch(status: number, body?: unknown) {
  return vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(body === undefined ? null : JSON.stringify(body), { status }),
  );
}

describe("api client", () => {
  it("sends the CSRF token on state-changing requests only", async () => {
    const f = mockFetch(200, { user: { id: "1" }, permissions: [], via: "session", csrfToken: "c1" });
    await api.login("albie", "a long password");
    mockFetch(204);
    const g = vi.mocked(globalThis.fetch);
    await request("PUT", "/api/v1/me/profile", { email: "", notify: {} });
    const headers = g.mock.calls.at(-1)![1]!.headers as Record<string, string>;
    expect(headers["X-CSRF-Token"]).toBe("c1");
    expect(f).toHaveBeenCalled();
    mockFetch(200, []);
    await request("GET", "/api/v1/users");
    const getHeaders = vi.mocked(globalThis.fetch).mock.calls.at(-1)![1]!.headers as Record<string, string>;
    expect(getHeaders["X-CSRF-Token"]).toBeUndefined();
  });

  it("formats minor units per currency (JPY and KRW have no decimals)", () => {
    expect(money(123456, "GBP")).toBe("£1,234.56");
    expect(money(150000, "JPY")).toBe("JP¥150,000");
    expect(money(5000, "KRW")).toBe("₩5,000");
    expect(money(-12, "AUD")).toBe("-A$0.12");
    expect(money(undefined, "USD")).toBe("—");
    expect(money(150, "XX")).toBe("1.50 XX");
    expect(currencyDecimals(" jpy ")).toBe(0);
    expect(viewOnly("etoro")).toBe(false);
    expect(viewOnly("saxo") && viewOnly("tiger") && viewOnly("trading212")).toBe(true);
  });

  it("sends credentials by key", async () => {
    const f = mockFetch(204);
    await api.setAccountCredentials("a1", { tiger_id: "x" });
    expect(f.mock.calls[0][0]).toBe("/api/v1/accounts/a1/keys");
    expect(JSON.parse(String(f.mock.calls[0][1]!.body))).toEqual({ values: { tiger_id: "x" } });
  });

  it("turns problem+json into ApiError", async () => {
    mockFetch(409, { title: "Conflict", status: 409, detail: "you cannot delete yourself" });
    await expect(api.deleteUser("me")).rejects.toEqual(new ApiError(409, { title: "Conflict", status: 409, detail: "you cannot delete yourself" }));
  });
});
