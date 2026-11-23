import { useCallback, useEffect, useState } from "react";
import { api, type AppEvent } from "../lib/api";

const POLL_MS = 20_000;

// The bell at the top right: unread count, and a panel with the newest events (watchlist
// changes, scanner suggestions, orders, buys and sells, kill switch, limits, restarts).
export function EventsBell() {
  const [events, setEvents] = useState<AppEvent[]>([]);
  const [unread, setUnread] = useState(0);
  const [open, setOpen] = useState(false);
  const load = useCallback(() => {
    api.events(50).then((f) => { setEvents(f.events); setUnread(f.unread); }).catch(() => undefined);
  }, []);
  useEffect(() => {
    load();
    const t = window.setInterval(load, POLL_MS);
    return () => window.clearInterval(t);
  }, [load]);
  function toggle() {
    const next = !open;
    setOpen(next);
    if (next && unread > 0) {
      api.eventsSeen().then(() => setUnread(0)).catch(() => undefined);
    }
  }
  return (
    <>
      <button className="btn btn-sm btn-outline-secondary bell" onClick={toggle} title="Events: watchlist changes, suggestions, orders, buys and sells, kill switch, limits" aria-label={`Events, ${unread} unread`} aria-expanded={open}>
        <i className="bi bi-bell" />
        {unread > 0 && <span className="count">{unread > 99 ? "99+" : unread}</span>}
      </button>
      {open && (
        <aside className="events-panel" aria-label="Events">
          <div className="d-flex align-items-center justify-content-between p-3 border-bottom">
            <h5 className="mb-0"><i className="bi bi-bell me-2" />Events</h5>
            <button className="btn-close" onClick={() => setOpen(false)} aria-label="Close" />
          </div>
          <div className="list">
            {events.length === 0 && <p className="text-secondary p-2">Nothing has happened yet.</p>}
            {events.map((e) => (
              <div key={e.id} className={"ev " + e.severity}>
                <div className="fw-semibold">{e.title}</div>
                {e.detail && <div className="small">{e.detail}</div>}
                <div className="when">{new Date(e.at).toLocaleString()} · {e.actor}</div>
              </div>
            ))}
          </div>
        </aside>
      )}
    </>
  );
}
