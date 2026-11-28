import { useEffect, useMemo, useState } from "react";
import { api, type Account } from "../lib/api";
import {
  ACTIVITY, FILTERS, clockSeconds, fromDecision, fromEvent, mergeActivity, shareLine, shows, showsSide,
  type ActivityEntry, type ActivityFilter, type Sides,
} from "../lib/activity";
import type { Live } from "../lib/live";
import { ProblemAlert } from "./ProblemAlert";
import { oneOf, usePref } from "../lib/prefs";

const isFilter = oneOf(FILTERS.map((f) => f.id));
// a stored pair of sides is used only when it shows at least one
const isSides = (v: unknown) => {
  const s = v as Sides | null;
  return !!s && typeof s === "object" && typeof s.long === "boolean" && typeof s.short === "boolean" && (s.long || s.short);
};

// ActivityLog is the dashboard's live log of what the agent does (R26), newest first. On load
// it reads the latest stored events and decisions of the account; the stream's events, ticks
// and decisions are added on top as they arrive. It only shows what the server sent: nothing
// here decides or changes anything.
export function ActivityLog({ account, live }: { account: Account; live: Live }) {
  const [history, setHistory] = useState<ActivityEntry[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<unknown>(null);
  // R50: the filter and the sides shown are the user's preferences
  const [filter, setFilter] = usePref<ActivityFilter>("dashboard.activity.filter", "all", isFilter);
  const [frozen, setFrozen] = useState<ActivityEntry[] | null>(null);
  const [sides, setSides] = usePref<Sides>("dashboard.activity.sides", { long: true, short: true }, isSides);

  // The history, again after a resync or every poll while the stream is down.
  useEffect(() => {
    let gone = false;
    Promise.allSettled([api.events(ACTIVITY.historyEvents), api.decisions({ limit: ACTIVITY.historyDecisions })]).then(([evs, ds]) => {
      if (gone) return;
      const lines: ActivityEntry[] = [];
      if (evs.status === "fulfilled") {
        // this account's events and system-wide ones: the stream sends the same
        for (const e of evs.value.events) if (!e.accountId || e.accountId === account.id) lines.push(fromEvent(e));
      }
      if (ds.status === "fulfilled") for (const d of ds.value) lines.push(fromDecision(d));
      setHistory(mergeActivity([], lines));
      setError(evs.status === "rejected" ? evs.reason : ds.status === "rejected" ? ds.reason : null);
      setLoaded(true);
    });
    return () => { gone = true; };
  }, [account.id, live.refresh]);

  const all = useMemo(() => mergeActivity(history, live.activity), [history, live.activity]);
  const paused = frozen !== null;
  const shown = (frozen ?? all).filter((e) => shows(e, filter) && showsSide(e, sides));
  // A side can be switched off only while the other is on: at least one is always shown.
  const toggleSide = (k: keyof Sides) => setSides((s) => (s[k] && !s[k === "long" ? "short" : "long"] ? s : { ...s, [k]: !s[k] }));
  const seen = useMemo(() => new Set((frozen ?? []).map((e) => e.key)), [frozen]);
  const waiting = paused ? all.filter((e) => !seen.has(e.key)).length : 0;
  const name = (id: string) => (id === "" ? "System" : id === account.id ? account.name : id);

  return (
    <div className="card mb-4 activity-log">
      <div className="card-body">
        <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
          <h2 className="h5 mb-0 me-auto"><i className="bi bi-activity me-2" aria-hidden="true" />Agent activity</h2>
          <div className="btn-group btn-group-sm" role="group" aria-label="Filter the activity">
            {FILTERS.map((f) => (
              <button key={f.id} type="button" className={"btn " + (filter === f.id ? "btn-success" : "btn-outline-success")}
                title={f.title} aria-label={f.title} aria-pressed={filter === f.id} onClick={() => setFilter(f.id)}>
                <i className={"bi me-1 " + f.icon} aria-hidden="true" />{f.label}
              </button>
            ))}
          </div>
          <div className="btn-group btn-group-sm" role="group" aria-label="Show long and/or short">
            {(["long", "short"] as const).map((k) => (
              <button key={k} type="button" className={"btn " + (sides[k] ? "btn-secondary" : "btn-outline-secondary")}
                aria-pressed={sides[k]} title={sides[k] ? `Hide ${k} ideas, orders and positions` : `Show ${k} ideas, orders and positions`}
                onClick={() => toggleSide(k)}>
                <i className={"bi me-1 " + (k === "long" ? "bi-arrow-up-right" : "bi-arrow-down-right")} aria-hidden="true" />{k === "long" ? "Long" : "Short"}
              </button>
            ))}
          </div>
          <button type="button" className={"btn btn-sm " + (paused ? "btn-warning" : "btn-outline-secondary")}
            title={paused ? `Resume: show the newest entries${waiting ? ` (${waiting} new)` : ""}` : "Pause: keep the list still while you read"}
            aria-label={paused ? "Resume the activity log" : "Pause the activity log"} aria-pressed={paused}
            onClick={() => setFrozen(paused ? null : all)}>
            <i className={"bi " + (paused ? "bi-play-fill" : "bi-pause-fill")} aria-hidden="true" />
            {paused && waiting > 0 && <span className="badge text-bg-light ms-1">{waiting} new</span>}
          </button>
        </div>
        <ProblemAlert error={error} variant="warning" className="py-2 small" />
        {shown.length === 0 ? (
          <p className="text-secondary small mb-0">
            {!loaded ? "Loading the latest activity…"
              : all.length === 0
                ? "Nothing yet. Entries appear here as the agent works: each round while it runs (every 5 minutes), every idea with the reviewer's and the risk engine's answer, orders, fills and closes, and changes such as a start, a pause or the kill switch."
                : "No entries match this filter."}
          </p>
        ) : (
          <ol className="list-unstyled small mb-0 activity-list" role="log" aria-label="Agent activity" aria-live={paused ? "off" : "polite"}>
            {shown.map((e) => (
              <li key={e.key} className={"activity-line tone-" + e.tone}>
                <span className="when text-nowrap">{clockSeconds(e.at)}</span>
                <i className={"bi icon " + e.icon} aria-hidden="true" />
                <span className="acct text-nowrap" title="account">{name(e.accountId)}</span>
                <span className="what">
                  {e.text}
                  {e.detail && <span className="text-secondary" title={e.detail}> · {e.detail}</span>}
                  {e.shares && (
                    <details className="shares">
                      <summary>shares</summary>
                      <ul className="list-unstyled mb-0">
                        {e.shares.map((s) => <li key={s.instrumentId}>{shareLine(s)}</li>)}
                      </ul>
                    </details>
                  )}
                </span>
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  );
}
