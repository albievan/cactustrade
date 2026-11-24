// useLive follows one account's live view (GET /api/v1/live, server-sent events; PLAN 6.11).
// It is a view only: nothing is decided on it, and the kill switch and every other control
// keep working through their own REST calls when the stream is down.
//
// Messages (the contract's description of /api/v1/live): `snapshot` (LiveSnapshot), `event`
// (LiveEvent), `order` (LiveOrder), `tick` (LiveTick), `decision` (LiveDecision), `resync` and
// `bye` (LiveNotice). Events, ticks and decisions become lines of the activity log (R26). The
// browser sends the session cookie with the same-origin EventSource.
//
// Connection rules:
//   - an error or a bye other than signed_out / no_access reconnects with backoff 1 s, 2 s,
//     4 s … 30 s; a bye "lifetime" (the 30-minute limit) reconnects at once;
//   - after 3 failures in a row the page falls back to polling: `refresh` ticks every 15 s
//     (the agent panel refetches) and the orders feed is read over REST, while the stream
//     keeps being retried; the first snapshot ends the fallback;
//   - a tab hidden for 60 s closes the stream (and stops polling); showing it reconnects;
//   - `stale` is true when no snapshot arrived for 30 s, or the server marked the last one
//     stale (its portfolio is missing or older than 30 s).
import { useEffect, useRef, useState } from "react";
import { api, type Order } from "./api";
import { ACTIVITY, fromDecision, fromEvent, fromTick, mergeActivity, type ActivityEntry, type LiveDecision, type LiveTick } from "./activity";
import type { components } from "./schema";

export type LiveSnapshot = components["schemas"]["LiveSnapshot"];
export type LiveFigures = components["schemas"]["LiveFigures"];
export type LivePosition = components["schemas"]["LivePosition"];
export type LiveRegime = components["schemas"]["LiveRegime"];
export type LiveMarketClock = components["schemas"]["LiveMarketClock"];
export type LiveEvent = components["schemas"]["LiveEvent"];
export type LiveOrder = components["schemas"]["LiveOrder"];
export type LiveNotice = components["schemas"]["LiveNotice"];

// LIVE holds the client's timings (exported for the tests and the page's explanations).
export const LIVE = {
  backoffFirstMs: 1_000,
  backoffMaxMs: 30_000,
  failuresBeforePolling: 3,
  pollMs: 15_000,
  hiddenCloseMs: 60_000,
  staleMs: 30_000,
  tickMs: 1_000,
  feedSize: 20,
};

// The stream's state as the page shows it.
export type LiveConnection = "connecting" | "live" | "reconnecting" | "polling" | "paused" | "ended";

export interface Live {
  snapshot: LiveSnapshot | null;
  orders: Order[]; // the orders feed: the snapshot's latest orders, updated by `order` messages
  activity: ActivityEntry[]; // the activity log's lines received on the stream, newest first (at most ACTIVITY.cap)
  connection: LiveConnection;
  endedReason: string; // the bye reason when connection is "ended" (signed_out or no_access)
  stale: boolean;
  staleReason: string;
  refresh: number; // ticks when what a page shows should be fetched again (resync; every 15 s while polling)
}

// liveUrl is the stream's address for an account.
export function liveUrl(accountId: string): string {
  return `/api/v1/live?accountId=${encodeURIComponent(accountId)}`;
}

function parse<T>(m: Event): T | null {
  try {
    return JSON.parse(String((m as MessageEvent).data)) as T;
  } catch {
    return null;
  }
}

// upsertOrder puts an order into the feed: replaced in place when known, else first.
export function upsertOrder(feed: Order[], o: Order, size = LIVE.feedSize): Order[] {
  const i = feed.findIndex((x) => x.requestId === o.requestId);
  if (i >= 0) return feed.map((x, j) => (j === i ? o : x));
  return [o, ...feed].slice(0, size);
}

export function useLive(accountId: string | undefined): Live {
  const [snapshot, setSnapshot] = useState<LiveSnapshot | null>(null);
  const [orders, setOrders] = useState<Order[]>([]);
  const [activity, setActivity] = useState<ActivityEntry[]>([]);
  const [connection, setConnection] = useState<LiveConnection>("connecting");
  const [endedReason, setEndedReason] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [lastAt, setLastAt] = useState<number | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const startedAt = useRef(Date.now());

  useEffect(() => {
    if (!accountId) return;
    let es: EventSource | null = null;
    let failures = 0;
    let retry: number | undefined;
    let poll: number | undefined;
    let hide: number | undefined;
    let paused = false;
    let ended = false;
    startedAt.current = Date.now();

    const fetchOrders = () => {
      api.orders(LIVE.feedSize).then(setOrders).catch(() => undefined);
    };
    const close = () => {
      if (es) { es.close(); es = null; }
    };
    const stopRetry = () => {
      if (retry !== undefined) { window.clearTimeout(retry); retry = undefined; }
    };
    const startPolling = () => {
      setConnection("polling");
      if (poll !== undefined) return;
      setRefresh((n) => n + 1);
      fetchOrders();
      poll = window.setInterval(() => { setRefresh((n) => n + 1); fetchOrders(); }, LIVE.pollMs);
    };
    const stopPolling = () => {
      if (poll !== undefined) { window.clearInterval(poll); poll = undefined; }
    };
    const fail = () => {
      close();
      if (ended || paused) return;
      failures++;
      if (failures >= LIVE.failuresBeforePolling) startPolling();
      else setConnection("reconnecting");
      const delay = Math.min(LIVE.backoffFirstMs * 2 ** (failures - 1), LIVE.backoffMaxMs);
      stopRetry();
      retry = window.setTimeout(open, delay);
    };
    const open = () => {
      retry = undefined;
      if (ended || paused) return;
      close();
      const src = new EventSource(liveUrl(accountId));
      es = src;
      const mine = (f: (m: Event) => void) => (m: Event) => { if (es === src) f(m); };
      src.addEventListener("snapshot", mine((m) => {
        const s = parse<LiveSnapshot>(m);
        if (!s) return;
        failures = 0;
        stopPolling();
        setConnection("live");
        setSnapshot(s);
        setOrders(s.recentOrders);
        setLastAt(Date.now());
      }));
      src.addEventListener("order", mine((m) => {
        const o = parse<LiveOrder>(m);
        if (o) setOrders((feed) => upsertOrder(feed, o.order));
      }));
      const line = (e: ActivityEntry) => setActivity((log) => mergeActivity(log, [e], ACTIVITY.cap));
      src.addEventListener("event", mine((m) => {
        const e = parse<LiveEvent>(m);
        if (e) line(fromEvent(e.event, e.accountId));
      }));
      src.addEventListener("tick", mine((m) => {
        const t = parse<LiveTick>(m);
        if (t) line(fromTick(t));
      }));
      src.addEventListener("decision", mine((m) => {
        const d = parse<LiveDecision>(m);
        if (d) line(fromDecision(d.decision));
      }));
      src.addEventListener("resync", mine(() => {
        // Messages may have been missed: fetch again; a fresh snapshot follows.
        setRefresh((n) => n + 1);
        fetchOrders();
      }));
      src.addEventListener("bye", mine((m) => {
        const reason = parse<LiveNotice>(m)?.reason ?? "";
        if (reason === "signed_out" || reason === "no_access") {
          ended = true;
          close();
          stopRetry();
          stopPolling();
          setEndedReason(reason);
          setConnection("ended");
          return;
        }
        if (reason === "lifetime") { close(); open(); return; }
        fail();
      }));
      src.onerror = () => { if (es === src) fail(); };
    };

    const onVisibility = () => {
      if (document.hidden) {
        if (hide === undefined && !paused) {
          hide = window.setTimeout(() => {
            hide = undefined;
            paused = true;
            close();
            stopRetry();
            stopPolling();
            setConnection("paused");
          }, LIVE.hiddenCloseMs);
        }
        return;
      }
      if (hide !== undefined) { window.clearTimeout(hide); hide = undefined; }
      if (paused && !ended) {
        paused = false;
        failures = 0;
        startedAt.current = Date.now();
        setLastAt(null);
        setConnection("connecting");
        setRefresh((n) => n + 1);
        open();
      }
    };
    document.addEventListener("visibilitychange", onVisibility);
    open();
    if (document.hidden) onVisibility();
    const tick = window.setInterval(() => setNow(Date.now()), LIVE.tickMs);
    return () => {
      ended = true;
      close();
      stopRetry();
      stopPolling();
      if (hide !== undefined) window.clearTimeout(hide);
      window.clearInterval(tick);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [accountId]);

  const quiet = now - (lastAt ?? startedAt.current) >= LIVE.staleMs;
  const watching = connection !== "ended" && connection !== "paused";
  let staleReason = "";
  if (watching && quiet) staleReason = "no live update for 30 seconds";
  else if (snapshot?.stale) staleReason = snapshot.portfolioKnown ? "the portfolio is older than 30 seconds" : "no portfolio read yet";
  return { snapshot, orders, activity, connection, endedReason, stale: staleReason !== "", staleReason, refresh };
}
