import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorText, type Industry, type Instrument, type Theme, type ThemeInput } from "../lib/api";
import { useMode } from "../lib/mode";

const EMPTY: ThemeInput = { name: "", keywords: [], industryId: 0, enabled: true, instruments: [] };

// Themes: the account's own "industries" (Space, Semiconductors…). The scanner reviews the
// shares its search words, its eToro industry and its picked shares lead to.
export function Themes() {
  const status = useMode();
  const canEdit = status?.account?.access === "owner" || status?.account?.access === "trade";
  const [themes, setThemes] = useState<Theme[]>([]);
  const [industries, setIndustries] = useState<Industry[]>([]);
  const [scanner, setScanner] = useState<boolean | null>(null);
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    api.themes().then(setThemes).catch((e) => setError(errorText(e)));
    api.reviews(1).then((b) => setScanner(b.scannerEnabled)).catch(() => undefined);
  }, []);
  useEffect(() => {
    load();
    api.industries().then(setIndustries).catch(() => undefined);
  }, [load]);
  async function run(f: () => Promise<void>) {
    setBusy(true); setError(""); setMsg("");
    try { await f(); } catch (e) { setError(errorText(e)); } finally { setBusy(false); }
  }
  return (
    <>
      <h1 className="h3">Themes</h1>
      <p className="text-secondary">
        Tell the trader where to look. A theme such as <em>Space</em> or <em>Semiconductors</em> finds shares by search words
        (names or symbols), by one of eToro's stock industries, and by shares you pick. The scanner reviews them with the
        regime model and the playbooks every 15 minutes while a market is open; a share that passes every rule — and that
        eToro lets you trade that way — is added to the watchlist. See every review on the Dashboard.
      </p>
      {msg && <div className="alert alert-success" role="status">{msg}</div>}
      {error && <div className="alert alert-danger" role="alert">{error}</div>}
      {canEdit && (
        <div className="card mb-4"><div className="card-body d-flex flex-wrap align-items-center gap-3">
          <div className="form-check form-switch me-auto">
            <input className="form-check-input" type="checkbox" role="switch" id="scanner" checked={!!scanner} disabled={busy || scanner === null}
              onChange={(e) => void run(async () => { await api.setScanner(e.target.checked); setScanner(e.target.checked); })} />
            <label className="form-check-label fw-semibold" htmlFor="scanner">Scanner on for {status?.account?.name}</label>
          </div>
          <button className="btn btn-outline-success" disabled={busy} onClick={() => void run(async () => {
            const r = await api.runScanner();
            setMsg(`Reviewed ${r.reviewed} share(s)` + (r.added.length ? `; added ${r.added.join(", ")} to the watchlist.` : "; nothing new for the watchlist."));
          })}><i className="bi bi-radar me-1" />{busy ? "Scanning…" : "Scan now"}</button>
        </div></div>
      )}
      {canEdit && <ThemeForm industries={industries} onSaved={load} />}
      {themes.map((t) => <ThemeCard key={t.id} t={t} industries={industries} canEdit={canEdit} onChange={load} />)}
      {themes.length === 0 && <p className="text-secondary">No themes yet.</p>}
    </>
  );
}

function ThemeForm({ initial, id, industries, onSaved, onCancel }: { initial?: ThemeInput; id?: string; industries: Industry[]; onSaved: () => void; onCancel?: () => void }) {
  const [t, setT] = useState<ThemeInput>(initial ?? EMPTY);
  const [kw, setKw] = useState((initial?.keywords ?? []).join(", "));
  const [picked, setPicked] = useState<Instrument[]>([]);
  const [q, setQ] = useState("");
  const [found, setFound] = useState<Instrument[]>([]);
  const [error, setError] = useState("");
  async function search() {
    setError("");
    try { setFound((await api.searchInstruments(q)).filter((i) => i.market)); } catch (e) { setError(errorText(e)); }
  }
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    const body: ThemeInput = { ...t, keywords: kw.split(",").map((s) => s.trim()).filter(Boolean), instruments: [...t.instruments, ...picked.map((p) => p.instrumentId)] };
    try {
      if (id) await api.updateTheme(id, body); else await api.createTheme(body);
      if (!id) { setT(EMPTY); setKw(""); setPicked([]); }
      onSaved();
    } catch (err) {
      setError(errorText(err));
    }
  }
  return (
    <form className="card mb-4" onSubmit={submit}><div className="card-body row g-3">
      <div className="col-md-4">
        <label className="form-label fw-semibold" htmlFor={"tn" + (id ?? "")}>{id ? "Name" : "New theme"}</label>
        <input id={"tn" + (id ?? "")} className="form-control" placeholder="e.g. Space, Semiconductors" maxLength={60} value={t.name} onChange={(e) => setT({ ...t, name: e.target.value })} required />
      </div>
      <div className="col-md-5">
        <label className="form-label fw-semibold" htmlFor={"tk" + (id ?? "")}>Search words</label>
        <input id={"tk" + (id ?? "")} className="form-control" placeholder="comma separated, e.g. rocket, satellite, launch" value={kw} onChange={(e) => setKw(e.target.value)} />
      </div>
      <div className="col-md-3">
        <label className="form-label fw-semibold" htmlFor={"ti" + (id ?? "")}>eToro industry</label>
        <select id={"ti" + (id ?? "")} className="form-select" value={t.industryId} onChange={(e) => setT({ ...t, industryId: Number(e.target.value) })}>
          <option value={0}>none</option>
          {industries.map((i) => <option key={i.id} value={i.id}>{i.name}</option>)}
        </select>
      </div>
      <div className="col-12">
        <label className="form-label fw-semibold" htmlFor={"tq" + (id ?? "")}>Pick shares</label>
        <div className="d-flex gap-2">
          <input id={"tq" + (id ?? "")} className="form-control" placeholder="search eToro by name or symbol" value={q} onChange={(e) => setQ(e.target.value)} />
          <button type="button" className="btn btn-outline-secondary" onClick={() => void search()} disabled={!q.trim()}>Search</button>
        </div>
        <div className="mt-2 d-flex flex-wrap gap-1">
          {found.map((f) => (
            <button type="button" key={f.instrumentId} className="btn btn-sm btn-outline-success" onClick={() => { setPicked([...picked.filter((p) => p.instrumentId !== f.instrumentId), f]); setFound([]); setQ(""); }}>
              + {f.symbol} <span className="opacity-75">{f.name}</span>
            </button>
          ))}
          {picked.map((p) => (
            <span key={p.instrumentId} className="badge text-bg-light border">{p.symbol}
              <button type="button" className="btn-close btn-close-sm ms-1" style={{ fontSize: ".5rem" }} aria-label={"Remove " + p.symbol} onClick={() => setPicked(picked.filter((x) => x.instrumentId !== p.instrumentId))} />
            </span>
          ))}
          {t.instruments.length > 0 && <span className="small text-secondary align-self-center">{t.instruments.length} share(s) picked before</span>}
        </div>
      </div>
      {error && <div className="col-12"><div className="alert alert-danger mb-0">{error}</div></div>}
      <div className="col-12 d-flex gap-2">
        <button className="btn btn-success">{id ? "Save" : "Add theme"}</button>
        {onCancel && <button type="button" className="btn btn-outline-secondary" onClick={onCancel}>Cancel</button>}
      </div>
    </div></form>
  );
}

function ThemeCard({ t, industries, canEdit, onChange }: { t: Theme; industries: Industry[]; canEdit: boolean; onChange: () => void }) {
  const [editing, setEditing] = useState(false);
  const [error, setError] = useState("");
  const industry = industries.find((i) => i.id === t.industryId)?.name;
  const input: ThemeInput = { name: t.name, keywords: t.keywords, industryId: t.industryId, enabled: t.enabled, instruments: t.instruments };
  if (editing) return <ThemeForm id={t.id} initial={input} industries={industries} onSaved={() => { setEditing(false); onChange(); }} onCancel={() => setEditing(false)} />;
  async function act(f: () => Promise<unknown>) {
    setError("");
    try { await f(); onChange(); } catch (e) { setError(errorText(e)); }
  }
  return (
    <div className={"card mb-3" + (t.enabled ? "" : " opacity-75")}><div className="card-body">
      <div className="d-flex flex-wrap align-items-center gap-2">
        <h2 className="h5 mb-0 me-2">{t.name}</h2>
        {!t.enabled && <span className="badge text-bg-secondary">paused</span>}
        <span className="ms-auto" />
        {canEdit && <>
          <button className="btn btn-sm btn-outline-secondary" onClick={() => void act(() => api.updateTheme(t.id, { ...input, enabled: !t.enabled }))}>{t.enabled ? "Pause" : "Resume"}</button>
          <button className="btn btn-sm btn-outline-secondary" onClick={() => setEditing(true)}>Edit</button>
          <button className="btn btn-sm btn-outline-danger" onClick={() => { if (window.confirm(`Delete the theme ${t.name}?`)) void act(() => api.deleteTheme(t.id)); }}>Delete</button>
        </>}
      </div>
      <div className="small mt-2">
        {t.keywords.length > 0 && <div><span className="text-secondary">Search words:</span> {t.keywords.join(", ")}</div>}
        {industry && <div><span className="text-secondary">eToro industry:</span> {industry}</div>}
        {t.instruments.length > 0 && <div><span className="text-secondary">Picked shares:</span> {t.instruments.length}</div>}
      </div>
      {error && <div className="alert alert-danger mt-2 mb-0 py-2">{error}</div>}
    </div></div>
  );
}
