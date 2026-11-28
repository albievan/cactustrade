import { useCallback, useEffect, useState } from "react";
import { api, type Account, type T212Catalogue } from "../lib/api";
import { ProblemAlert } from "./ProblemAlert";

// T212Setup is a Trading 212 practice account's trading setup on its card (R45.11): the eToro
// demo account whose prices and signals feed it (D44: eToro data, at the owner's risk) and the
// age of Trading 212's instrument list, with a refresh. The server decides everything (owner,
// web session, agent stopped, at most one refresh per 50 s); this only shows and asks.
export function T212Setup({ account: a, accounts, owner, onChanged }: {
  account: Account; accounts: Account[]; owner: boolean; onChanged: () => void;
}) {
  const [cat, setCat] = useState<T212Catalogue | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");
  const loadCatalogue = useCallback(() => {
    api.t212Catalogue(a.id).then(setCat).catch(() => setCat(null));
  }, [a.id]);
  useEffect(loadCatalogue, [loadCatalogue]);
  // An eToro demo account of the same owner (the server checks it has keys).
  const sources = accounts.filter((s) => s.platform === "etoro" && s.mode === "demo" && s.ownerId === a.ownerId);
  const current = accounts.find((s) => s.id === a.priceSourceAccountId);
  async function run(f: () => Promise<void>) {
    setBusy(true); setError(null); setNote("");
    try { await f(); } catch (e) { setError(e); } finally { setBusy(false); }
  }
  return (
    <div className="small border rounded p-2 mb-2" aria-label={`Trading 212 setup of ${a.name}`}>
      <div className="fw-semibold mb-1">Trading 212 practice trading</div>
      <div className="mb-1">
        <label className="me-1" htmlFor={`ps-${a.id}`}>Prices and signals from</label>
        {owner ? (
          <select id={`ps-${a.id}`} className="form-select form-select-sm d-inline-block w-auto" value={a.priceSourceAccountId ?? ""} disabled={busy}
            onChange={(e) => void run(async () => { await api.setPriceSource(a.id, e.target.value); setNote("Price source saved."); onChanged(); })}>
            <option value="">— none: nothing is bought —</option>
            {sources.map((s) => <option key={s.id} value={s.id}>{s.name} (eToro demo)</option>)}
          </select>
        ) : (
          <strong id={`ps-${a.id}`}>{current ? current.name : "none"}</strong>
        )}
        <div className="text-secondary">Trading 212 publishes no prices: the agent reads eToro&apos;s for the same share. Orders go to Trading 212.</div>
      </div>
      <div className="d-flex flex-wrap align-items-center gap-2">
        <span>Instrument list:</span>
        {cat === null ? <span className="text-secondary">unknown</span> : cat.fetchedAt
          ? <span className={cat.stale ? "text-danger" : ""}>{cat.stale ? "out of date" : "fresh"} · fetched {new Date(cat.fetchedAt).toLocaleString()}</span>
          : <span className="text-danger">never fetched</span>}
        {owner && (
          <button type="button" className="btn btn-sm btn-outline-secondary py-0" disabled={busy}
            onClick={() => void run(async () => {
              const c = await api.refreshT212Catalogue(a.id);
              setCat(c); setNote(`${c.count} instruments stored.`);
            })}>Refresh</button>
        )}
      </div>
      {note && <div className="text-success mt-1" role="status">{note}</div>}
      <ProblemAlert error={error} className="py-1 small mt-1 mb-0" />
    </div>
  );
}
