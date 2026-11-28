import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PrefsProvider, PrefsStore, RETRY_MS, SAVE_DELAY_MS, oneOf, usePref, usePrefsSaveFailed } from "./prefs";

// A fake server for GET/PUT /api/v1/me/preferences (a merge patch; null deletes).
function fakeServer(initial: Record<string, unknown> = {}) {
  const srv = {
    prefs: { ...initial } as Record<string, unknown>, gets: 0, puts: [] as Record<string, unknown>[], failPut: false, failGet: false, putStatus: 503,
    // putDelay holds each PUT this long; inFlight / maxInFlight count PUTs on their way at once.
    putDelay: 0, inFlight: 0, maxInFlight: 0, keepalive: [] as boolean[],
  };
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (!url.endsWith("/api/v1/me/preferences")) return new Response("{}", { status: 404 });
    if (init?.method === "PUT") {
      srv.inFlight++;
      srv.maxInFlight = Math.max(srv.maxInFlight, srv.inFlight);
      try {
        if (srv.putDelay > 0) await new Promise((r) => setTimeout(r, srv.putDelay));
      } finally {
        srv.inFlight--;
      }
      if (srv.failPut) return new Response(JSON.stringify({ title: "refused", status: srv.putStatus }), { status: srv.putStatus });
      const patch = JSON.parse(String(init.body)) as Record<string, unknown>;
      srv.puts.push(patch);
      srv.keepalive.push(!!init.keepalive);
      for (const [k, v] of Object.entries(patch)) {
        if (v === null) delete srv.prefs[k];
        else srv.prefs[k] = v;
      }
      return new Response(JSON.stringify(srv.prefs), { status: 200 });
    }
    srv.gets++;
    if (srv.failGet) return new Response(JSON.stringify({ title: "down", status: 503 }), { status: 503 });
    return new Response(JSON.stringify(srv.prefs), { status: 200 });
  });
  return srv;
}

const isRange = oneOf(["day", "week", "max"]);

function Range() {
  const [range, setRange] = usePref<string>("test.range", "day", isRange);
  const [open, setOpen] = usePref<boolean>("test.open", true);
  const failed = usePrefsSaveFailed();
  return (
    <div>
      <span data-testid="range">{range}</span>
      <span data-testid="open">{String(open)}</span>
      {failed && <span>not saved</span>}
      {["day", "week", "max"].map((r) => <button key={r} onClick={() => setRange(r)}>{r}</button>)}
      <button onClick={() => setOpen((o) => !o)}>toggle</button>
    </div>
  );
}

beforeEach(() => localStorage.clear());
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); });

describe("usePref and PrefsProvider (R50)", () => {
  it("loads once after sign-in and renders the pages only with the user's values", async () => {
    const srv = fakeServer({ "test.range": "max", "test.open": false });
    render(<PrefsProvider userId="u1"><Range /></PrefsProvider>);
    expect(screen.getByText("Loading…")).toBeInTheDocument();
    expect(screen.queryByTestId("range")).not.toBeInTheDocument(); // no flash of the default
    expect(await screen.findByTestId("range")).toHaveTextContent("max");
    expect(screen.getByTestId("open")).toHaveTextContent("false");
    expect(srv.gets).toBe(1);
  });

  it("saves the changes of one pause together, then survives a reload", async () => {
    const srv = fakeServer();
    const first = render(<PrefsProvider userId="u1"><Range /></PrefsProvider>);
    await screen.findByTestId("range");
    fireEvent.click(screen.getByText("week"));
    fireEvent.click(screen.getByText("max"));
    fireEvent.click(screen.getByText("toggle"));
    expect(screen.getByTestId("range")).toHaveTextContent("max"); // applied at once
    expect(srv.puts).toEqual([]); // not yet: debounced
    await waitFor(() => expect(srv.puts).toEqual([{ "test.range": "max", "test.open": false }]), { timeout: SAVE_DELAY_MS * 4 });
    first.unmount();

    // a fresh page (another browser: nothing cached) shows the server's values
    localStorage.clear();
    render(<PrefsProvider userId="u1"><Range /></PrefsProvider>);
    expect(await screen.findByTestId("range")).toHaveTextContent("max");
    expect(screen.getByTestId("open")).toHaveTextContent("false");
  });

  it("removes a key set back to its default and ignores a stored value that is not allowed", async () => {
    const srv = fakeServer({ "test.range": "fortnight", "test.open": false });
    render(<PrefsProvider userId="u1"><Range /></PrefsProvider>);
    expect(await screen.findByTestId("range")).toHaveTextContent("day"); // "fortnight" is not a range
    fireEvent.click(screen.getByText("toggle"));
    expect(screen.getByTestId("open")).toHaveTextContent("true");
    await waitFor(() => expect(srv.puts).toEqual([{ "test.open": null }]), { timeout: SAVE_DELAY_MS * 4 });
    expect(srv.prefs).toEqual({ "test.range": "fortnight" });
  });

  it("never blocks: a failed load uses this browser's copy, a failed save says so quietly and retries", async () => {
    localStorage.setItem("cactustrade.prefs.u1", JSON.stringify({ "test.range": "week" }));
    const srv = fakeServer({ "test.range": "max" });
    srv.failGet = true;
    srv.failPut = true;
    render(<PrefsProvider userId="u1"><Range /></PrefsProvider>);
    expect(await screen.findByTestId("range")).toHaveTextContent("week");
    expect(screen.getByText("not saved")).toBeInTheDocument();
    fireEvent.click(screen.getByText("day"));
    fireEvent.click(screen.getByText("max"));
    expect(screen.getByTestId("range")).toHaveTextContent("max"); // the UI works on
    expect(JSON.parse(localStorage.getItem("cactustrade.prefs.u1")!)).toEqual({ "test.range": "max" });
  });

  it("is plain component state outside a provider", () => {
    vi.spyOn(globalThis, "fetch");
    render(<Range />);
    fireEvent.click(screen.getByText("week"));
    expect(screen.getByTestId("range")).toHaveTextContent("week");
    expect(fetch).not.toHaveBeenCalled();
  });
});

describe("PrefsStore", () => {
  it("debounces, merges pending changes and retries a failed save", async () => {
    vi.useFakeTimers();
    const srv = fakeServer();
    const s = new PrefsStore("u1", {});
    s.set("a.one", 1);
    await vi.advanceTimersByTimeAsync(SAVE_DELAY_MS - 1);
    s.set("a.two", "x");
    await vi.advanceTimersByTimeAsync(SAVE_DELAY_MS - 1);
    expect(srv.puts).toEqual([]); // each change restarts the pause
    await vi.advanceTimersByTimeAsync(1);
    expect(srv.puts).toEqual([{ "a.one": 1, "a.two": "x" }]);

    srv.failPut = true;
    s.set("a.one", 2);
    await vi.advanceTimersByTimeAsync(SAVE_DELAY_MS);
    expect(s.saveFailed()).toBe(true);
    s.set("a.three", true); // a change while failed goes with the retry
    srv.failPut = false;
    await vi.advanceTimersByTimeAsync(SAVE_DELAY_MS);
    expect(srv.puts.at(-1)).toEqual({ "a.one": 2, "a.three": true });
    expect(s.saveFailed()).toBe(false);
    expect(srv.prefs).toEqual({ "a.one": 2, "a.two": "x", "a.three": true });

    // a failure with no further change is retried after RETRY_MS
    srv.failPut = true;
    s.set("a.two", null);
    await vi.advanceTimersByTimeAsync(SAVE_DELAY_MS);
    srv.failPut = false;
    await vi.advanceTimersByTimeAsync(RETRY_MS);
    expect(srv.prefs).toEqual({ "a.one": 2, "a.three": true });

    // a refusal (400: e.g. the size cap) is not retried; the value stays in this page
    srv.failPut = true;
    srv.putStatus = 400;
    s.set("a.four", "big");
    await vi.advanceTimersByTimeAsync(SAVE_DELAY_MS);
    const tries = srv.puts.length;
    srv.failPut = false;
    await vi.advanceTimersByTimeAsync(RETRY_MS * 2);
    expect(srv.puts).toHaveLength(tries);
    expect(s.saveFailed()).toBe(true);
    expect(s.get("a.four")).toBe("big");
    s.dispose();
  });

  it("flushes on demand and stops after dispose", async () => {
    vi.useFakeTimers();
    const srv = fakeServer();
    const s = new PrefsStore("u1", {});
    s.set("a.one", 1);
    await act(() => s.flush());
    expect(srv.puts).toEqual([{ "a.one": 1 }]);
    s.set("a.two", 2);
    s.dispose();
    await vi.advanceTimersByTimeAsync(RETRY_MS);
    expect(srv.puts).toHaveLength(1);
  });

  it("flush during a save in flight waits for it, then sends what is pending (review fix 2026-11-28)", async () => {
    vi.useFakeTimers();
    const srv = fakeServer();
    srv.putDelay = 300;
    const s = new PrefsStore("u1", {});
    s.set("a.one", 1);
    await vi.advanceTimersByTimeAsync(SAVE_DELAY_MS); // the first save is now on its way
    expect(srv.inFlight).toBe(1);
    s.set("a.two", 2); // changed while it is in flight
    let flushed = false;
    void s.flush().then(() => { flushed = true; });
    await vi.advanceTimersByTimeAsync(299);
    expect(flushed).toBe(false); // waits for the save in flight
    await vi.advanceTimersByTimeAsync(301 + 1);
    expect(flushed).toBe(true);
    expect(srv.puts).toEqual([{ "a.one": 1 }, { "a.two": 2 }]);
    expect(srv.maxInFlight).toBe(1);
    s.dispose();
  });

  it("the pagehide keepalive save never overlaps a save in flight", async () => {
    vi.useFakeTimers();
    const srv = fakeServer();
    srv.putDelay = 200;
    const s = new PrefsStore("u1", {});
    s.set("a.one", "old");
    await vi.advanceTimersByTimeAsync(SAVE_DELAY_MS); // in flight
    s.set("a.one", "new");
    void s.flush(true); // pagehide
    await vi.advanceTimersByTimeAsync(1000);
    expect(srv.maxInFlight).toBe(1);
    expect(srv.puts).toEqual([{ "a.one": "old" }, { "a.one": "new" }]);
    expect(srv.keepalive).toEqual([false, true]);
    expect(srv.prefs).toEqual({ "a.one": "new" }); // the newer value lands last
    s.dispose();
  });

  it("signOut sends everything pending and then clears this browser's copy", async () => {
    vi.useFakeTimers();
    const srv = fakeServer();
    srv.putDelay = 100;
    const s = new PrefsStore("u1", {});
    s.set("a.one", 1);
    expect(localStorage.getItem("cactustrade.prefs.u1")).not.toBeNull();
    await vi.advanceTimersByTimeAsync(SAVE_DELAY_MS); // in flight
    s.set("a.two", 2);
    let done = false;
    void s.signOut().then(() => { done = true; });
    await vi.advanceTimersByTimeAsync(500);
    expect(done).toBe(true);
    expect(srv.prefs).toEqual({ "a.one": 1, "a.two": 2 });
    expect(localStorage.getItem("cactustrade.prefs.u1")).toBeNull();
    // a failed final save still clears the copy (it never rejects)
    const t = new PrefsStore("u2", {});
    t.set("a.one", 1);
    srv.failPut = true;
    let ended = false;
    void t.signOut().then(() => { ended = true; });
    await vi.advanceTimersByTimeAsync(500);
    expect(ended).toBe(true);
    expect(localStorage.getItem("cactustrade.prefs.u2")).toBeNull();
  });
});
