import { useEffect, useState, type FormEvent } from "react";
import { api, errorText, type LlmProfile, type LlmProfileInput, type LlmSetup, type LlmTestResult } from "../lib/api";
import { useSession } from "../lib/session";

const blank: LlmProfileInput = { name: "", provider: "anthropic", model: "", baseUrl: "", timeoutSeconds: 0 };

// The LLM reviewer: what it is, several provider/model profiles (each with its own key), the
// default one, and a test per profile. Themes can pick their own profile.
export function LlmReviewer() {
  const { can, me } = useSession();
  const admin = can("settings.write") && me?.via === "session";
  const [setup, setSetup] = useState<LlmSetup | null>(null);
  const [editing, setEditing] = useState<LlmProfile | "new" | null>(null);
  const [tests, setTests] = useState<Record<string, LlmTestResult>>({});
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const load = () => api.llmSetup().then(setSetup).catch((e) => setError(errorText(e)));
  useEffect(() => { void load(); }, []);
  async function run(label: string, f: () => Promise<void>) {
    setBusy(label); setError(""); setMsg("");
    try { await f(); } catch (e) { setError(errorText(e)); } finally { setBusy(""); }
  }
  if (!setup) return <><h1 className="h3">LLM reviewer</h1>{error && <div className="alert alert-danger">{error}</div>}</>;
  const s = setup.settings;
  const label = (id: string) => setup.providers.find((p) => p.id === id)?.label ?? id;
  const setSettings = (enabled: boolean, activeProfileId: string) =>
    run("settings", async () => { setSetup(await api.setLlmSettings({ enabled, activeProfileId })); setMsg("Saved and audited."); });
  return (
    <>
      <h1 className="h3">LLM reviewer</h1>
      <div className="card mb-4"><div className="card-body">
        <h2 className="h5">What it does</h2>
        <p className="mb-2">
          Before a share is added to the watchlist by the scanner, and before an order is sent, the idea — instrument, reason,
          entry, stop, target, size, the market regime and the recent prices — is shown to a large language model acting as a
          second pair of eyes. It answers <strong>approve</strong>, <strong>reduce</strong> (a smaller size) or
          <strong> reject</strong>, with a short reason you see on the Dashboard and in the decision log.
        </p>
        <ul className="mb-0 small">
          <li>Set up as many <strong>profiles</strong> (provider + model + key) as you like. The <strong>default</strong> one reviews everything; a theme can pick its own profile instead (Themes page).</li>
          <li>It can never enlarge a trade, propose another one, or place an order: it has no tools, no eToro access and no keys but its own.</li>
          <li>It fails closed: no answer, a timeout, a quota error, an unreadable answer or a refusal all count as <strong>reject</strong>.</li>
          <li>The deterministic risk engine still has the final say (budget, cash, loss limits, kill switch).</li>
          <li>Each review is one request to the provider, paid with that profile's key. A <em>429 / quota</em> error comes from the provider: check your plan there.</li>
        </ul>
      </div></div>
      {msg && <div className="alert alert-success" role="status">{msg}</div>}
      {error && <div className="alert alert-danger" role="alert">{error}</div>}

      <div className="card mb-4"><div className="card-body row g-3 align-items-end">
        <div className="col-md-6">
          <div className="form-check form-switch">
            <input className="form-check-input" type="checkbox" role="switch" id="enabled" checked={s.enabled}
              disabled={!admin || !!busy} onChange={(e) => void setSettings(e.target.checked, s.activeProfileId)} />
            <label className="form-check-label fw-semibold" htmlFor="enabled">Review with an LLM</label>
          </div>
          <div className="form-text">Off: ideas go from the model straight to the risk engine.</div>
        </div>
        <div className="col-md-6">
          <label className="form-label fw-semibold" htmlFor="default">Default profile</label>
          <select id="default" className="form-select" value={s.activeProfileId} disabled={!admin || !!busy}
            onChange={(e) => void setSettings(s.enabled && e.target.value !== "", e.target.value)}>
            <option value="">— none —</option>
            {setup.profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
        </div>
      </div></div>

      <div className="d-flex align-items-center mb-2">
        <h2 className="h5 mb-0 flex-grow-1">Profiles</h2>
        {admin && editing === null && <button className="btn btn-success btn-sm" onClick={() => setEditing("new")}><i className="bi bi-plus-lg me-1" />Add profile</button>}
      </div>
      {editing === "new" && (
        <ProfileForm setup={setup} initial={blank} onCancel={() => setEditing(null)}
          onSaved={async (p) => { setEditing(null); setMsg(`Profile ${p.name} added.`); await load(); }} />
      )}
      {setup.profiles.length === 0 && editing === null && (
        <p className="text-secondary">No profiles yet. Add one: choose a provider, paste its API key, load and pick a model.</p>
      )}
      <div className="vstack gap-3">
        {setup.profiles.map((p) => editing !== "new" && editing?.id === p.id ? (
          <ProfileForm key={p.id} setup={setup} initial={p} id={p.id} keySet={p.keySet} onCancel={() => setEditing(null)}
            onSaved={async (x) => { setEditing(null); setMsg(`Profile ${x.name} saved.`); await load(); }} />
        ) : (
          <div key={p.id} className="card"><div className="card-body">
            <div className="d-flex flex-wrap align-items-center gap-2">
              <strong className="me-1">{p.name}</strong>
              {p.id === s.activeProfileId && <span className="badge text-bg-success">default</span>}
              <span className="badge text-bg-light border">{label(p.provider)}</span>
              <code className="small">{p.model || (p.provider === "anthropic" ? setup.defaultAnthropicModel : "")}</code>
              {p.keySet ? <span className="badge text-bg-light border"><i className="bi bi-key me-1" />key set</span>
                : <span className="badge text-bg-warning">no key</span>}
              <span className="flex-grow-1" />
              {admin && <>
                <button className="btn btn-sm btn-outline-success" disabled={!!busy}
                  onClick={() => void run("test" + p.id, async () => setTests({ ...tests, [p.id]: await api.testLlmProfile(p.id) }))}>
                  <i className="bi bi-chat-square-text me-1" />{busy === "test" + p.id ? "Asking…" : "Test"}</button>
                {p.id !== s.activeProfileId && <button className="btn btn-sm btn-outline-secondary" disabled={!!busy}
                  onClick={() => void setSettings(s.enabled, p.id)}>Make default</button>}
                <button className="btn btn-sm btn-outline-secondary" onClick={() => setEditing(p)}><i className="bi bi-pencil" /> Edit</button>
                <button className="btn btn-sm btn-outline-danger" disabled={!!busy} aria-label={`Delete ${p.name}`}
                  onClick={() => void run("del", async () => { await api.deleteLlmProfile(p.id); setMsg(`Profile ${p.name} deleted.`); await load(); })}>
                  <i className="bi bi-trash" /></button>
              </>}
            </div>
            {tests[p.id] && <TestResult r={tests[p.id]} />}
          </div></div>
        ))}
      </div>
    </>
  );
}

function TestResult({ r }: { r: LlmTestResult }) {
  return r.ok ? (
    <div className="alert alert-success mt-3 mb-0" role="status">
      Answered <strong>{r.verdict}</strong>{r.verdict === "reduce" && <> to {Math.round((r.sizeMultiplier ?? 0) * 100)} %</>} in {r.latencyMs} ms: “{r.reason}”
    </div>
  ) : (
    <div className="alert alert-warning mt-3 mb-0" role="alert">No usable answer ({r.latencyMs} ms): {r.error}. A real idea would be rejected.</div>
  );
}

function ProfileForm({ setup, initial, id, keySet, onCancel, onSaved }: {
  setup: LlmSetup; initial: LlmProfileInput; id?: string; keySet?: boolean;
  onCancel: () => void; onSaved: (p: LlmProfile) => Promise<void>;
}) {
  const [p, setP] = useState<LlmProfileInput>({ name: initial.name, provider: initial.provider, model: initial.model, baseUrl: initial.baseUrl, timeoutSeconds: initial.timeoutSeconds });
  const [key, setKey] = useState("");
  const [models, setModels] = useState<string[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [note, setNote] = useState("");
  const provider = setup.providers.find((x) => x.id === p.provider);
  const set = (patch: Partial<LlmProfileInput>) => setP({ ...p, ...patch });
  const providerChanged = !!id && (p.provider !== initial.provider || p.baseUrl !== initial.baseUrl);
  async function loadModels() {
    setBusy("models"); setError(""); setNote("");
    try {
      const m = await api.llmModels({ provider: p.provider, baseUrl: p.baseUrl, ...(key ? { apiKey: key } : id && !providerChanged ? { profileId: id } : {}) });
      setModels(m);
      setNote(`${m.length} models: pick one from the list or type it.`);
      if (!p.model && m.length > 0) set({ model: m[0] });
    } catch (e) { setError(errorText(e)); } finally { setBusy(""); }
  }
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy("save"); setError("");
    try {
      const body: LlmProfileInput = { ...p, ...(key ? { apiKey: key } : {}) };
      await onSaved(id ? await api.updateLlmProfile(id, body) : await api.createLlmProfile(body));
    } catch (e) { setError(errorText(e)); } finally { setBusy(""); }
  }
  return (
    <form className="card border-success mb-3" onSubmit={save}><div className="card-body row g-3">
      <div className="col-md-4">
        <label className="form-label fw-semibold" htmlFor="pname">Name</label>
        <input id="pname" className="form-control" maxLength={80} value={p.name} onChange={(e) => set({ name: e.target.value })} placeholder="e.g. Claude for space stocks (empty = provider · model)" />
      </div>
      <div className="col-md-4">
        <label className="form-label fw-semibold" htmlFor="provider">Provider</label>
        <select id="provider" className="form-select" value={p.provider}
          onChange={(e) => { set({ provider: e.target.value, model: "", baseUrl: "" }); setModels([]); setNote(""); }}>
          {setup.providers.map((x) => <option key={x.id} value={x.id}>{x.label}</option>)}
        </select>
        {provider?.note && <div className="form-text">{provider.note}</div>}
      </div>
      <div className="col-md-4">
        <label className="form-label fw-semibold" htmlFor="key">API key</label>
        <input id="key" type="password" autoComplete="off" className="form-control" value={key} onChange={(e) => setKey(e.target.value.trim())}
          placeholder={keySet && !providerChanged ? "set — type to replace" : provider?.keyRequired ? "required" : "optional"} />
        <div className="form-text">Encrypted, write-only.{providerChanged && keySet ? " Changing provider or address needs the key again." : ""}</div>
      </div>
      <div className="col-md-6">
        <label className="form-label fw-semibold" htmlFor="model">Model</label>
        <div className="input-group">
          {models.length > 0 ? (
            <select id="model" className="form-select" value={p.model} onChange={(e) => set({ model: e.target.value })}>
              {!models.includes(p.model) && <option value={p.model}>{p.model || "— choose —"}</option>}
              {models.map((m) => <option key={m} value={m}>{m}</option>)}
            </select>
          ) : (
            <input id="model" className="form-control" value={p.model} onChange={(e) => set({ model: e.target.value.trim() })}
              placeholder={p.provider === "anthropic" ? setup.defaultAnthropicModel + " (default)" : "type a model id or load the list"} />
          )}
          <button type="button" className="btn btn-outline-secondary" disabled={!!busy} onClick={() => void loadModels()}>
            {busy === "models" ? "Loading…" : "Load models"}</button>
        </div>
        <div className="form-text">{note || "Uses the key typed here, or the saved one."}</div>
      </div>
      <div className="col-md-4">
        <label className="form-label fw-semibold" htmlFor="baseUrl">Address</label>
        <input id="baseUrl" className="form-control" value={p.baseUrl} onChange={(e) => set({ baseUrl: e.target.value.trim() })}
          placeholder={provider?.defaultBaseUrl || (p.provider === "anthropic" ? "https://api.anthropic.com (default)" : "required, e.g. http://gpu-box:11434/v1")} />
      </div>
      <div className="col-md-2">
        <label className="form-label fw-semibold" htmlFor="timeout">Time limit</label>
        <div className="input-group">
          <input id="timeout" type="number" min={0} max={600} className="form-control" value={p.timeoutSeconds} onChange={(e) => set({ timeoutSeconds: Number(e.target.value) })} />
          <span className="input-group-text">s</span>
        </div>
      </div>
      {error && <div className="col-12"><div className="alert alert-danger mb-0" role="alert">{error}</div></div>}
      <div className="col-12 d-flex gap-2">
        <button className="btn btn-success" disabled={!!busy}>{busy === "save" ? "Saving…" : id ? "Save profile" : "Add profile"}</button>
        <button type="button" className="btn btn-outline-secondary" onClick={onCancel}>Cancel</button>
      </div>
    </div></form>
  );
}
