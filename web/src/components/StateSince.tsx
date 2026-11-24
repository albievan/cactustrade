import { useEffect, useState } from "react";

// durationText formats a length of time for people: "45 s", "12 min", "2 h 5 min",
// "3 d 4 h". Formatting only; the time it measures from comes from the server.
export function durationText(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} h ${m % 60} min`;
  return `${Math.floor(h / 24)} d ${h % 24} h`;
}

// StateSince shows how long the agent has been in its current state ("running for 2 h 5 min"),
// ticking every 15 s; nothing when the server does not know since when.
export function StateSince({ state, since }: { state: string; since?: string }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!since) return;
    const t = window.setInterval(() => setNow(Date.now()), 15_000);
    return () => window.clearInterval(t);
  }, [since]);
  if (!since) return null;
  const at = new Date(since);
  if (Number.isNaN(at.getTime())) return null;
  return (
    <span className="small text-secondary" title={`${state} since ${at.toLocaleString()}`}>
      {state} for {durationText(now - at.getTime())}
    </span>
  );
}
