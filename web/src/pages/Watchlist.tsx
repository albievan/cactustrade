import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorText, type Instrument, type Quote, type WatchItem } from "../lib/api";
import { useSession } from "../lib/session";

const QUOTE_REFRESH_MS = 15_000; // 4 calls a minute: well inside eToro's market-data quota

// The instruments the agent may trade. Adding asks eToro whether the share can be bought as a
// real share, long, at leverage 1; anything else is refused with eToro's reason.
export function Watchlist() {
  const { can } = useSession();
  const admin = can("settings.write");
  const [items, setItems] = useState<WatchItem[]>([]);
  const [quotes, setQuotes] = useState<Record<number, Quote>>({});
  const [error, setError] = useState("");
  const load = useCallback(() => {
    api.watchlist().then(setItems).catch((e) => setError(errorText(e)));
  }, []);
  useEffect(load, [load]);
  useEffect(() => {
    if (items.length === 0) return;
    const tick = () =>
      api.watchlistQuotes()
        .then((qs) => setQuotes(Object.fromEntries(qs.map((q) => [q.instrumentId, q]))))
        .catch(() => undefined);
    tick();
    const t = window.setInterval(tick, QUOTE_REFRESH_MS);
    return () => window.clearInterval(t);
  }, [items]);
  async function remove(id: number) {
    setError("");
    try {
      await api.removeFromWatchlist(id);
      load();
    } catch (e) {
      setError(errorText(e));
    }
  }
  return (
    <>
      <h1 className="h3">Watchlist</h1>
      <p className="text-secondary">
        Shares the agent may trade: London (LSE) and New York (NYSE / NASDAQ) only, bought as the real share at leverage 1.
      </p>
      {error && <div className="alert alert-danger" role="alert">{error}</div>}
      {admin && <AddInstrument onAdded={load} />}
      {items.length === 0 ? (
        <p className="text-secondary">The watchlist is empty.</p>
      ) : (
        <div className="table-responsive">
          <table className="table align-middle">
            <thead><tr><th>Symbol</th><th>Name</th><th>Market</th><th className="text-end">Bid</th><th className="text-end">Ask</th><th>Eligibility</th>{admin && <th />}</tr></thead>
            <tbody>
              {items.map((w) => {
                const q = quotes[w.instrument.instrumentId];
                return (
                  <tr key={w.instrument.instrumentId}>
                    <td className="fw-semibold">{w.instrument.symbol}</td>
                    <td>{w.instrument.name}</td>
                    <td>{w.instrument.market}</td>
                    <td className="text-end">{q?.bid ?? "—"}</td>
                    <td className="text-end">{q?.ask ?? "—"}{q?.delayed && <span className="badge text-bg-secondary ms-1">delayed</span>}</td>
                    <td className="small">
                      {w.eligibility.realLong ? <span className="text-success"><i className="bi bi-check-circle me-1" />real share x1</span> : <span className="text-danger">{w.eligibility.reason}</span>}
                      {w.eligibility.checkedAt && <div className="text-secondary">checked {new Date(w.eligibility.checkedAt).toLocaleDateString()}</div>}
                    </td>
                    {admin && <td className="text-end"><button className="btn btn-sm btn-outline-danger" onClick={() => void remove(w.instrument.instrumentId)}>Remove</button></td>}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function AddInstrument({ onAdded }: { onAdded: () => void }) {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<Instrument[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function search(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      setResults(await api.searchInstruments(q));
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  async function add(id: number) {
    setError("");
    try {
      await api.addToWatchlist(id);
      setResults([]);
      setQ("");
      onAdded();
    } catch (err) {
      setError(errorText(err));
    }
  }
  return (
    <div className="card mb-4">
      <div className="card-body">
        <form className="d-flex gap-2" onSubmit={search}>
          <input className="form-control" placeholder="Search eToro by name or symbol (e.g. Apple, BARC.L)" aria-label="Search instruments" value={q} maxLength={100} onChange={(e) => setQ(e.target.value)} required />
          <button className="btn btn-outline-primary" disabled={busy}><i className="bi bi-search me-1" />Search</button>
        </form>
        {error && <div className="alert alert-danger mt-3 mb-0" role="alert">{error}</div>}
        {results.length > 0 && (
          <ul className="list-group mt-3">
            {results.map((r) => (
              <li className="list-group-item d-flex align-items-center gap-2" key={r.instrumentId}>
                <span className="fw-semibold">{r.symbol}</span>
                <span className="me-auto text-secondary">{r.name} · {r.type}</span>
                {r.market ? (
                  <button className="btn btn-sm btn-success" onClick={() => void add(r.instrumentId)}>Add ({r.market})</button>
                ) : (
                  <span className="small text-secondary">not tradable here</span>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
