import { useEffect, useState, type FormEvent } from "react";
import { api, errorText, type LlmProfile, type LlmProfileInput, type LlmSetup, type LlmTestResult, type LlmUsage, type LlmUsageRow } from "../lib/api";
import { useSession } from "../lib/session";
import { InfoCard } from "../components/InfoCard";
import { ProblemAlert } from "../components/ProblemAlert";

const blank: LlmProfileInput = { name: "", provider: "anthropic", model: "", baseUrl: "", timeoutSeconds: 0, inputPricePerMTok: "", outputPricePerMTok: "" };

// prices says what a profile costs per million tokens, or that it is unknown (OD13).
export function prices(p: { inputPricePerMTok: string; outputPricePerMTok: string }): string {
  if (!p.inputPricePerMTok || !p.outputPricePerMTok) return "prices unknown";
  return `$${p.inputPricePerMTok} in / $${p.outputPricePerMTok} out per M tokens`;
}

// The LLM reviewer: what it is, several provider/model profiles (each with its own key), the
// default one, and a test per profile. Themes can pick their own profile.
export function LlmReviewer() {
  const { can, me } = useSession();
  const admin = can("settings.write") && me?.via === "session";
  const seesUsage = can("settings.write");
  const [setup, setSetup] = useState<LlmSetup | null>(null);
  const [editing, setEditing] = useState<LlmProfile | "new" | null>(null);
  const [tests, setTests] = useState<Record<string, LlmTestResult>>({});
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState("");
  const [error, setError] = useState<unknown>(null);
  // subject: the profile the last action was about, so "no API key yet" can open its editor
  const [subject, setSubject] = useState<LlmProfile | null>(null);
  const load = () => api.llmSetup().then(setSetup).catch(setError);
  useEffect(() => { void load(); }, []);
  async function run(label: string, f: () => Promise<void>) {
    setBusy(label); setError(null); setMsg("");
    try { await f(); } catch (e) { setError(e); } finally { setBusy(""); }
  }
  if (!setup) return <><h1 className="h3">LLM reviewer</h1><ProblemAlert error={error} /></>;
  const s = setup.settings;
  const label = (id: string) => setup.providers.find((p) => p.id === id)?.label ?? id;
  const setSettings = (enabled: boolean, activeProfileId: string) => {
    setSubject(setup.profiles.find((p) => p.id === activeProfileId) ?? null);
    return run("settings", async () => { setSetup(await api.setLlmSettings({ enabled, activeProfileId })); setMsg("Saved and audited."); });
  };
  return (
    <>
      <h1 className="h3">LLM reviewer</h1>
      <InfoCard id="llm" title="What it does">
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
      </InfoCard>
      {msg && <div className="alert alert-success" role="status">{msg}</div>}
      <ProblemAlert error={error} fixes={subject && admin ? { llm_profile: () => { setError(null); setEditing(subject); } } : undefined} />

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
      <div className="row row-cols-1 row-cols-md-2 row-cols-xxl-3 g-3">
        {setup.profiles.map((p) => editing !== "new" && editing?.id === p.id ? (
          <div key={p.id} className="col-12 w-100">
            <ProfileForm setup={setup} initial={p} id={p.id} keySet={p.keySet} onCancel={() => setEditing(null)}
              onSaved={async (x) => { setEditing(null); setMsg(`Profile ${x.name} saved.`); await load(); }} />
          </div>
        ) : (
          <div key={p.id} className="col">
            <div className={`card h-100 profile-card${p.id === s.activeProfileId ? " border-success" : ""}`}><div className="card-body d-flex gap-3">
              {admin && (
                <div className="btn-group-vertical btn-group-sm align-self-start" role="group" aria-label={`Actions for ${p.name}`}>
                  <button className="btn btn-outline-success" disabled={!!busy} title="Test" aria-label={`Test ${p.name}`}
                    onClick={() => { setSubject(p); void run("test" + p.id, async () => setTests({ ...tests, [p.id]: await api.testLlmProfile(p.id) })); }}>
                    {busy === "test" + p.id ? <span className="spinner-border spinner-border-sm" /> : <i className="bi bi-chat-square-text" />}</button>
                  <button className="btn btn-outline-secondary" disabled={!!busy || p.id === s.activeProfileId}
                    title={p.id === s.activeProfileId ? "This is the default" : "Make default"} aria-label={`Make ${p.name} the default`}
                    onClick={() => void setSettings(s.enabled, p.id)}>
                    <i className={p.id === s.activeProfileId ? "bi bi-star-fill" : "bi bi-star"} /></button>
                  <button className="btn btn-outline-secondary" title="Edit" aria-label={`Edit ${p.name}`} onClick={() => setEditing(p)}>
                    <i className="bi bi-pencil" /></button>
                  <button className="btn btn-outline-danger" disabled={!!busy} title="Delete" aria-label={`Delete ${p.name}`}
                    onClick={() => void run("del", async () => { await api.deleteLlmProfile(p.id); setMsg(`Profile ${p.name} deleted.`); await load(); })}>
                    <i className="bi bi-trash" /></button>
                </div>
              )}
              <ProviderLogo provider={p.provider} label={label(p.provider)} />
              <div className="flex-grow-1 min-w-0">
                <div className="d-flex align-items-start gap-2 mb-2">
                  <strong className="text-break">{p.name}</strong>
                  {p.id === s.activeProfileId && <span className="badge text-bg-success">default</span>}
                </div>
                <div className="d-flex flex-wrap align-items-center gap-2 small">
                  <span className="badge text-bg-light border">{label(p.provider)}</span>
                  {p.keySet ? <span className="badge text-bg-light border"><i className="bi bi-key me-1" />key set</span>
                    : <span className="badge text-bg-warning">no key</span>}
                  <span className="badge text-bg-light border"><i className="bi bi-currency-dollar me-1" />{prices(p)}</span>
                </div>
                <code className="small d-block text-break mt-2">{p.model || (p.provider === "anthropic" ? setup.defaultAnthropicModel : "")}</code>
                {tests[p.id] && <TestResult r={tests[p.id]} />}
              </div>
            </div></div>
          </div>
        ))}
      </div>
      {seesUsage && <UsageCard setup={setup} />}
    </>
  );
}

const periods = [7, 30, 90, 400];

// usageCost shows a usage row's cost: exact when every call had a price, else what is known
// and how many calls are unpriced (OD13: unknown is shown as unknown, never as 0).
export function usageCost(r: LlmUsageRow): string {
  if (r.costUsd !== undefined) return "$" + r.costUsd;
  if (r.costUnknownCalls === r.calls) return "unknown";
  return `at least $${r.knownCostUsd} (${r.costUnknownCalls} unpriced)`;
}

// UsageCard: the LLM calls of the last days per profile and purpose (admin).
function UsageCard({ setup }: { setup: LlmSetup }) {
  const [days, setDays] = useState(30);
  const [usage, setUsage] = useState<LlmUsage | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.llmUsage(days).then((u) => { setUsage(u); setError(""); }).catch((e) => setError(errorText(e)));
  }, [days]);
  const name = (r: LlmUsageRow) => r.profileName || (setup.profiles.find((p) => p.id === r.profileId)?.name ?? "deleted profile");
  return (
    <div className="card mt-4">
      <div className="card-header d-flex align-items-center gap-2">
        <h2 className="h5 mb-0 flex-grow-1">Usage</h2>
        <label className="visually-hidden" htmlFor="usageDays">Period</label>
        <select id="usageDays" className="form-select form-select-sm w-auto" value={days} onChange={(e) => setDays(Number(e.target.value))}>
          {periods.map((d) => <option key={d} value={d}>last {d} days</option>)}
        </select>
      </div>
      <div className="card-body">
        {error && <div className="alert alert-danger" role="alert">{error}</div>}
        {usage && usage.rows.length === 0 && <p className="text-secondary mb-0">No LLM calls in the last {usage.days} days.</p>}
        {usage && usage.rows.length > 0 && (
          <div className="table-responsive">
            <table className="table table-sm align-middle mb-2">
              <thead>
                <tr>
                  <th>Profile</th><th>Purpose</th><th className="text-end">Calls</th><th className="text-end">Failed</th>
                  <th className="text-end">Input tokens</th><th className="text-end">Output tokens</th><th className="text-end">Cost</th>
                </tr>
              </thead>
              <tbody>
                {usage.rows.map((r) => (
                  <tr key={r.profileId + "/" + r.purpose}>
                    <td>{name(r)}</td><td>{r.purpose}</td><td className="text-end">{r.calls}</td><td className="text-end">{r.failed}</td>
                    <td className="text-end">{r.inputTokens.toLocaleString()}</td><td className="text-end">{r.outputTokens.toLocaleString()}</td>
                    <td className="text-end">{usageCost(r)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr className="fw-semibold">
                  <td colSpan={2}>Total</td><td className="text-end">{usage.total.calls}</td><td className="text-end">{usage.total.failed}</td>
                  <td className="text-end">{usage.total.inputTokens.toLocaleString()}</td><td className="text-end">{usage.total.outputTokens.toLocaleString()}</td>
                  <td className="text-end" data-testid="usage-total-cost">{usageCost(usage.total)}</td>
                </tr>
              </tfoot>
            </table>
          </div>
        )}
        <div className="form-text">Agent reviews, scanner checks and tests from this page. A cost is known only for profiles with prices.</div>
      </div>
    </div>
  );
}

// The provider's mark: the brand glyph where Bootstrap Icons has one, otherwise a coloured
// monogram (we draw no logos of our own).
const logos: Record<string, { icon?: string; text?: string; bg: string; fg?: string }> = {
  anthropic: { icon: "bi-claude", bg: "#d97757" },
  openai: { icon: "bi-openai", bg: "#0d0d0d" },
  gemini: { icon: "bi-google", bg: "#1a73e8" },
  deepseek: { text: "DS", bg: "#4d6bfe" },
  openrouter: { text: "OR", bg: "#6467f2" },
  kimi: { text: "K", bg: "#16191e" },
  grok: { text: "xAI", bg: "#000000" },
  ollama: { text: "Ol", bg: "#5a5a5a" },
  ollama_local: { icon: "bi-pc-display", bg: "#5a5a5a" },
};

export function ProviderLogo({ provider, label }: { provider: string; label: string }) {
  const l = logos[provider] ?? { icon: "bi-robot", bg: "#6c757d" };
  return (
    <div className="provider-logo flex-shrink-0" style={{ background: l.bg, color: l.fg ?? "#fff" }} role="img" aria-label={`${label} logo`} title={label}>
      {l.icon ? <i className={`bi ${l.icon}`} /> : <span>{l.text}</span>}
    </div>
  );
}

function TestResult({ r }: { r: LlmTestResult }) {
  return r.ok ? (
    <div className="alert alert-success small py-2 mt-3 mb-0" role="status">
      Answered <strong>{r.verdict}</strong>{r.verdict === "reduce" && <> to {Math.round((r.sizeMultiplier ?? 0) * 100)} %</>} in {r.latencyMs} ms: “{r.reason}”
      <TestUsage r={r} />
    </div>
  ) : (
    <div className="alert alert-warning small py-2 mt-3 mb-0 text-break test-error" role="alert">
      No usable answer ({r.latencyMs} ms): {r.error}. A real idea would be rejected.
      <TestUsage r={r} />
    </div>
  );
}

// TestUsage: the model that answered (a fallback may serve) and what the call used.
function TestUsage({ r }: { r: LlmTestResult }) {
  if (!r.model && r.inputTokens === undefined) return null;
  return (
    <div className="mt-1 text-secondary">
      {r.model && <>Model <code>{r.model}</code> · </>}
      {(r.inputTokens ?? 0).toLocaleString()} in / {(r.outputTokens ?? 0).toLocaleString()} out tokens · cost {r.costUsd !== undefined ? "$" + r.costUsd : "unknown"}
    </div>
  );
}

function ProfileForm({ setup, initial, id, keySet, onCancel, onSaved }: {
  setup: LlmSetup; initial: LlmProfileInput; id?: string; keySet?: boolean;
  onCancel: () => void; onSaved: (p: LlmProfile) => Promise<void>;
}) {
  // The prices travel with every save, so a save never resets them (they are required).
  const [p, setP] = useState<LlmProfileInput>({
    name: initial.name, provider: initial.provider, model: initial.model, baseUrl: initial.baseUrl, timeoutSeconds: initial.timeoutSeconds,
    inputPricePerMTok: initial.inputPricePerMTok ?? "", outputPricePerMTok: initial.outputPricePerMTok ?? "",
  });
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
      <div className="col-md-3">
        <label className="form-label fw-semibold" htmlFor="inPrice">Input price</label>
        <div className="input-group">
          <span className="input-group-text">$</span>
          <input id="inPrice" inputMode="decimal" className="form-control" value={p.inputPricePerMTok} placeholder="unknown"
            onChange={(e) => set({ inputPricePerMTok: e.target.value.trim() })} />
          <span className="input-group-text">/ M tokens</span>
        </div>
      </div>
      <div className="col-md-3">
        <label className="form-label fw-semibold" htmlFor="outPrice">Output price</label>
        <div className="input-group">
          <span className="input-group-text">$</span>
          <input id="outPrice" inputMode="decimal" className="form-control" value={p.outputPricePerMTok} placeholder="unknown"
            onChange={(e) => set({ outputPricePerMTok: e.target.value.trim() })} />
          <span className="input-group-text">/ M tokens</span>
        </div>
      </div>
      <div className="col-md-6 align-self-center">
        <div className="form-text">What the provider charges, from its price list (up to 6 decimals). Empty = unknown: every call&apos;s
          cost is then shown as unknown. Used only to show what the reviews cost.</div>
      </div>
      {error && <div className="col-12"><div className="alert alert-danger mb-0" role="alert">{error}</div></div>}
      <div className="col-12 d-flex gap-2">
        <button className="btn btn-success" disabled={!!busy}>{busy === "save" ? "Saving…" : id ? "Save profile" : "Add profile"}</button>
        <button type="button" className="btn btn-outline-secondary" onClick={onCancel}>Cancel</button>
      </div>
    </div></form>
  );
}
