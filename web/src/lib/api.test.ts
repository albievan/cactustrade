import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, request, setCsrf } from "./api";

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
    await request("PUT", "/api/v1/credentials/llm_api_key", { value: "x" });
    const headers = g.mock.calls.at(-1)![1]!.headers as Record<string, string>;
    expect(headers["X-CSRF-Token"]).toBe("c1");
    expect(f).toHaveBeenCalled();
    mockFetch(200, []);
    await request("GET", "/api/v1/users");
    const getHeaders = vi.mocked(globalThis.fetch).mock.calls.at(-1)![1]!.headers as Record<string, string>;
    expect(getHeaders["X-CSRF-Token"]).toBeUndefined();
  });

  it("turns problem+json into ApiError", async () => {
    mockFetch(409, { title: "Conflict", status: 409, detail: "you cannot delete yourself" });
    await expect(api.deleteUser("me")).rejects.toEqual(new ApiError(409, { title: "Conflict", status: 409, detail: "you cannot delete yourself" }));
  });
});
