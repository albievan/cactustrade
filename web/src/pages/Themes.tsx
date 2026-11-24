import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorText, type Industry, type LlmSetup, type Theme, type ThemeInput, type ThemeShare } from "../lib/api";
import { useMode } from "../lib/mode";
import { NeedAccount } from "../components/NeedAccount";
import { ProblemAlert } from "../components/ProblemAlert";
import { ConfirmDialog } from "../components/ConfirmDialog";

const EMPTY: ThemeInput = { name: "", keywords: [], industryId: 0, enabled: true, instruments: [], llmProfileId: "" };

// reviewerName says which LLM profile reviews a theme's ideas ("" = the default).
function reviewerName(llm: LlmSetup | null, id: string | undefined): string {
  if (!llm) return "";
  const def = llm.profiles.find((p) => p.id === llm.settings.activeProfileId);
  const own = id ? llm.profiles.find((p) => p.id === id) : undefined;
  if (own) return own.name;
  return def ? `default (${def.name})` : "default (none set up)";
}

// Themes: the account's own "industries" (Space, Semiconductors…). The scanner reviews the
// shares its search words, its eToro industry and its picked shares lead to.
export function Themes() {
  const status = useMode();
  const canEdit = status?.account?.access === "owner" || status?.account?.access === "trade";
  const [themes, setThemes] = useState<Theme[]>([]);
  const [industries, setIndustries] = useState<Industry[]>([]);
  const [llm, setLlm] = useState<LlmSetup | null>(null);
  const [scanner, setScanner] = useState<boolean | null>(null);
  const [msg, setMsg] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    api.themes().then(setThemes).catch(setError);
    api.reviews(1).then((b) => setScanner(b.scannerEnabled)).catch(() => undefined);
  }, []);
  const accountId = status?.account?.id;
  useEffect(() => {
    if (!accountId) return;
    load();
    api.industries().then(setIndustries).catch(() => undefined);
    api.llmSetup().then(setLlm).catch(() => undefined);
  }, [load, accountId]);
  if (status && !status.account) return <><h1 className="h3">Themes</h1><NeedAccount /></>;
  async function run(f: () => Promise<void>) {
    setBusy(true); setError(""); setMsg("");
    try { await f(); } catch (e) { setError(e); } finally { setBusy(false); }
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
      <ProblemAlert error={error} />
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
      {llm && !llm.settings.enabled && (
        <div className="alert alert-light border small">The LLM reviewer is off, so themes' reviewers are not asked. Switch it on under <a href="/llm">LLM reviewer</a>.</div>
      )}
      {canEdit && <ThemeForm industries={industries} llm={llm} onSaved={load} />}
      {themes.map((t) => <ThemeCard key={t.id} t={t} industries={industries} llm={llm} canEdit={canEdit} onChange={load} />)}
      {themes.length === 0 && <p className="text-secondary">No themes yet.</p>}
    </>
  );
}

function ThemeForm({ initial, initialShares, id, industries, llm, onSaved, onCancel }: {
  initial?: ThemeInput; initialShares?: ThemeShare[]; id?: string; industries: Industry[]; llm: LlmSetup | null;
  onSaved: () => void; onCancel?: () => void;
}) {
  const [t, setT] = useState<ThemeInput>(initial ?? EMPTY);
  const [kw, setKw] = useState((initial?.keywords ?? []).join(", "));
  const [shares, setShares] = useState<ThemeShare[]>(initialShares ?? []);
  const [q, setQ] = useState("");
  const [found, setFound] = useState<ThemeShare[]>([]);
  const [error, setError] = useState("");
  const sfx = id ?? "";
  async function search() {
    setError("");
    try {
      const res = (await api.searchInstruments(q)).filter((i) => i.market);
      setFound(res.map((i) => ({ instrumentId: i.instrumentId, symbol: i.symbol, name: i.name, market: i.market ?? "" })));
      if (res.length === 0) setError("No share on LSE, NYSE/NASDAQ or the ASX matches " + q);
    } catch (e) { setError(errorText(e)); }
  }
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    const body: ThemeInput = { ...t, keywords: kw.split(",").map((x) => x.trim()).filter(Boolean), instruments: shares.map((x) => x.instrumentId) };
    try {
      if (id) await api.updateTheme(id, body); else await api.createTheme(body);
      if (!id) { setT(EMPTY); setKw(""); setShares([]); }
      onSaved();
    } catch (err) {
      setError(errorText(err));
    }
  }
  return (
    <form className="card mb-4" onSubmit={submit}><div className="card-body row g-3">
      <div className="col-md-4">
        <label className="form-label fw-semibold" htmlFor={"tn" + sfx}>{id ? "Name" : "New theme"}</label>
        <input id={"tn" + sfx} className="form-control" placeholder="e.g. Space, Semiconductors" maxLength={60} value={t.name} onChange={(e) => setT({ ...t, name: e.target.value })} required />
      </div>
      <div className="col-md-5">
        <label className="form-label fw-semibold" htmlFor={"tk" + sfx}>Search words</label>
        <input id={"tk" + sfx} className="form-control" placeholder="comma separated, e.g. rocket, satellite, launch" value={kw} onChange={(e) => setKw(e.target.value)} />
      </div>
      <div className="col-md-3">
        <label className="form-label fw-semibold" htmlFor={"ti" + sfx}>eToro industry</label>
        <select id={"ti" + sfx} className="form-select" value={t.industryId} onChange={(e) => setT({ ...t, industryId: Number(e.target.value) })}>
          <option value={0}>none</option>
          {industries.map((i) => <option key={i.id} value={i.id}>{i.name}</option>)}
        </select>
      </div>
      <div className="col-md-8">
        <label className="form-label fw-semibold" htmlFor={"tq" + sfx}>Shares in this theme</label>
        <div className="d-flex gap-2">
          <input id={"tq" + sfx} className="form-control" placeholder="search eToro by name or symbol, e.g. grok, nvidia" value={q}
            onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); if (q.trim()) void search(); } }} />
          <button type="button" className="btn btn-outline-secondary" onClick={() => void search()} disabled={!q.trim()}>Search</button>
        </div>
        {found.length > 0 && (
          <div className="mt-2 d-flex flex-wrap gap-1">
            {found.map((f) => (
              <button type="button" key={f.instrumentId} className="btn btn-sm btn-outline-success"
                onClick={() => { setShares([...shares.filter((p) => p.instrumentId !== f.instrumentId), f]); setFound([]); setQ(""); }}>
                + {f.symbol} <span className="opacity-75">{f.name}</span>
              </button>
            ))}
          </div>
        )}
        <div className="mt-2 d-flex flex-wrap gap-1">
          {shares.map((p) => (
            <span key={p.instrumentId} className="ticker-chip" title={p.name}>
              <strong>{p.symbol}</strong> <span className="opacity-75 d-none d-md-inline">{p.name}</span>
              <button type="button" className="btn-close ms-1" aria-label={"Remove " + p.symbol}
                onClick={() => setShares(shares.filter((x) => x.instrumentId !== p.instrumentId))} />
            </span>
          ))}
          {shares.length === 0 && <span className="small text-secondary">No shares picked: the search words and industry find them.</span>}
        </div>
      </div>
      <div className="col-md-4">
        <label className="form-label fw-semibold" htmlFor={"tl" + sfx}>LLM reviewer</label>
        <select id={"tl" + sfx} className="form-select" value={t.llmProfileId ?? ""} onChange={(e) => setT({ ...t, llmProfileId: e.target.value })}>
          <option value="">{reviewerName(llm, "")}</option>
          {llm?.profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
        <div className="form-text">Asked before this theme's shares join the watchlist and before trading them. Set up profiles under LLM reviewer.</div>
      </div>
      {error && <div className="col-12"><div className="alert alert-danger mb-0">{error}</div></div>}
      <div className="col-12 d-flex gap-2">
        <button className="btn btn-success">{id ? "Save" : "Add theme"}</button>
        {onCancel && <button type="button" className="btn btn-outline-secondary" onClick={onCancel}>Cancel</button>}
      </div>
    </div></form>
  );
}

function ThemeCard({ t, industries, llm, canEdit, onChange }: { t: Theme; industries: Industry[]; llm: LlmSetup | null; canEdit: boolean; onChange: () => void }) {
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState("");
  const industry = industries.find((i) => i.id === t.industryId)?.name;
  const input: ThemeInput = { name: t.name, keywords: t.keywords, industryId: t.industryId, enabled: t.enabled, instruments: t.instruments, llmProfileId: t.llmProfileId ?? "" };
  if (editing) return <ThemeForm id={t.id} initial={input} initialShares={t.shares} industries={industries} llm={llm} onSaved={() => { setEditing(false); onChange(); }} onCancel={() => setEditing(false)} />;
  const removeShare = (id: number) => act(() => api.updateTheme(t.id, { ...input, instruments: t.instruments.filter((x) => x !== id) }));
  async function act(f: () => Promise<unknown>) {
    setError("");
    try { await f(); onChange(); } catch (e) { setError(errorText(e)); }
  }
  return (
    <div className={"card mb-3" + (t.enabled ? "" : " opacity-75")}><div className="card-body d-flex gap-3">
      {canEdit && (
        <div className="btn-group-vertical btn-group-sm align-self-start" role="group" aria-label={`Actions for ${t.name}`}>
          <button className="btn btn-outline-secondary" title={t.enabled ? "Pause" : "Resume"} aria-label={`${t.enabled ? "Pause" : "Resume"} ${t.name}`}
            onClick={() => void act(() => api.updateTheme(t.id, { ...input, enabled: !t.enabled }))}>
            <i className={"bi " + (t.enabled ? "bi-pause-fill" : "bi-play-fill")} /></button>
          <button className="btn btn-outline-secondary" title="Edit" aria-label={`Edit ${t.name}`} onClick={() => setEditing(true)}>
            <i className="bi bi-pencil" /></button>
          <button className="btn btn-outline-danger" title="Delete" aria-label={`Delete ${t.name}`} onClick={() => setDeleting(true)}>
            <i className="bi bi-trash" /></button>
        </div>
      )}
      <div className="flex-grow-1 min-w-0">
      <div className="d-flex flex-wrap align-items-center gap-2">
        <h2 className="h5 mb-0 me-2">{t.name}</h2>
        {!t.enabled && <span className="badge text-bg-secondary">paused</span>}
      </div>
      <div className="small mt-2">
        {t.keywords.length > 0 && <div><span className="text-secondary">Search words:</span> {t.keywords.join(", ")}</div>}
        {industry && <div><span className="text-secondary">eToro industry:</span> {industry}</div>}
        <div><span className="text-secondary">LLM reviewer:</span> {reviewerName(llm, t.llmProfileId)}</div>
      </div>
      {t.shares.length > 0 && (
        <div className="mt-2 d-flex flex-wrap gap-1" aria-label={`Shares in ${t.name}`}>
          {t.shares.map((p) => (
            <span key={p.instrumentId} className="ticker-chip" title={`${p.name}${p.market ? " · " + p.market : ""}`}>
              <strong>{p.symbol}</strong> <span className="opacity-75 d-none d-md-inline">{p.name}</span>
              {canEdit && <button type="button" className="btn-close ms-1" aria-label={"Remove " + p.symbol}
                onClick={() => void removeShare(p.instrumentId)} />}
            </span>
          ))}
        </div>
      )}
      {error && <div className="alert alert-danger mt-2 mb-0 py-2">{error}</div>}
      </div>
      {deleting && (
        <ConfirmDialog title={`Delete the theme ${t.name}?`} confirmLabel="Delete" busyLabel="Deleting…" icon="trash"
          onCancel={() => setDeleting(false)} onConfirm={async () => { await act(() => api.deleteTheme(t.id)); setDeleting(false); }}>
          <p className="mb-0">The scanner no longer looks for shares through it.</p>
        </ConfirmDialog>
      )}
    </div></div>
  );
}
