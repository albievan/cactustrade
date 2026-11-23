import { useEffect, useState, type FormEvent } from "react";
import { api, errorText, type LlmConfig, type LlmSetup, type LlmTestResult } from "../lib/api";
import { useSession } from "../lib/session";

// The LLM reviewer: what it is, which provider and model it uses, its key, and a test.
export function LlmReviewer() {
  const { can, me } = useSession();
  const admin = can("settings.write") && me?.via === "session";
  const [setup, setSetup] = useState<LlmSetup | null>(null);
  const [cfg, setCfg] = useState<LlmConfig | null>(null);
  const [key, setKey] = useState("");
  const [models, setModels] = useState<string[]>([]);
  const [test, setTest] = useState<LlmTestResult | null>(null);
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const load = () => api.llmSetup().then((s) => { setSetup(s); setCfg(s.config); }).catch((e) => setError(errorText(e)));
  useEffect(() => { void load(); }, []);
  if (!setup || !cfg) return <><h1 className="h3">LLM reviewer</h1>{error && <div className="alert alert-danger">{error}</div>}</>;
  const provider = setup.providers.find((p) => p.id === cfg.provider);
  const set = (patch: Partial<LlmConfig>) => setCfg({ ...cfg, ...patch });
  async function run(label: string, f: () => Promise<void>) {
    setBusy(label); setError(""); setMsg("");
    try { await f(); } catch (e) { setError(errorText(e)); } finally { setBusy(""); }
  }
  const save = (e: FormEvent) => { e.preventDefault(); void run("save", async () => {
    if (key) { await api.setCredential("llm_api_key", key); setKey(""); }
    setCfg(await api.setLlm(cfg)); await load(); setMsg("Saved and audited.");
  }); };
  return (
    <>
      <h1 className="h3">LLM reviewer</h1>
      <div className="card mb-4"><div className="card-body">
        <h2 className="h5">What it does</h2>
        <p className="mb-2">
          Before an order is sent, the trade the model proposed — instrument, reason, entry, stop, target, size, the market
          regime and the recent prices — is shown to a large language model acting as a second pair of eyes. It answers in
          one of three ways: <strong>approve</strong> the trade, <strong>reduce</strong> its size (to a fraction of it), or
          <strong> reject</strong> it, with a short reason that goes into the decision log.
        </p>
        <ul className="mb-0 small">
          <li>It can never enlarge a trade, propose another one, or place an order: it has no tools, no eToro access and no keys but its own.</li>
          <li>It fails closed: no answer, a timeout, an unreadable answer or a refusal all <strong>reject</strong> the trade.</li>
          <li>The deterministic risk engine still has the final say after it (budget, cash, loss limits, kill switch).</li>
          <li>Each review is one request to the provider you choose below, paid with your key.</li>
        </ul>
      </div></div>
      {msg && <div className="alert alert-success" role="status">{msg}</div>}
      {error && <div className="alert alert-danger" role="alert">{error}</div>}
      <form className="card" onSubmit={save}><div className="card-body row g-4">
        <div className="col-12">
          <div className="form-check form-switch">
            <input className="form-check-input" type="checkbox" role="switch" id="enabled" checked={cfg.enabled} onChange={(e) => set({ enabled: e.target.checked })} disabled={!admin} />
            <label className="form-check-label fw-semibold" htmlFor="enabled">Review every proposed trade</label>
          </div>
          <div className="form-text">Off: trades go from the model straight to the risk engine.</div>
        </div>
        <div className="col-md-6">
          <label className="form-label fw-semibold" htmlFor="provider">Provider</label>
          <select id="provider" className="form-select" value={cfg.provider} disabled={!admin}
            onChange={(e) => { set({ provider: e.target.value, model: "", baseUrl: "" }); setModels([]); setTest(null); }}>
            {setup.providers.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
          </select>
          {provider?.note && <div className="form-text">{provider.note}</div>}
        </div>
        <div className="col-md-6">
          <label className="form-label fw-semibold" htmlFor="model">Model</label>
          <div className="input-group">
            <input id="model" className="form-control" list="models" value={cfg.model} onChange={(e) => set({ model: e.target.value.trim() })} disabled={!admin}
              placeholder={cfg.provider === "anthropic" ? setup.defaultAnthropicModel + " (default)" : "choose or type a model id"} />
            {admin && <button type="button" className="btn btn-outline-secondary" disabled={!!busy}
              onClick={() => void run("models", async () => { const m = await api.llmModels(cfg); setModels(m); setMsg(`${m.length} models available.`); })}>
              {busy === "models" ? "Loading…" : "Load models"}</button>}
          </div>
          <datalist id="models">{models.map((m) => <option key={m} value={m} />)}</datalist>
          <div className="form-text">"Load models" asks the provider with the saved key.</div>
        </div>
        <div className="col-md-6">
          <label className="form-label fw-semibold" htmlFor="baseUrl">Address</label>
          <input id="baseUrl" className="form-control" value={cfg.baseUrl} onChange={(e) => set({ baseUrl: e.target.value.trim() })} disabled={!admin}
            placeholder={provider?.defaultBaseUrl || (cfg.provider === "anthropic" ? "https://api.anthropic.com (default)" : "required, e.g. http://gpu-box:11434/v1")} />
          <div className="form-text">Leave empty for the provider's own address.</div>
        </div>
        <div className="col-md-3">
          <label className="form-label fw-semibold" htmlFor="timeout">Time limit</label>
          <div className="input-group">
            <input id="timeout" type="number" min={0} max={600} className="form-control" value={cfg.timeoutSeconds} onChange={(e) => set({ timeoutSeconds: Number(e.target.value) })} disabled={!admin} />
            <span className="input-group-text">s</span>
          </div>
          <div className="form-text">0 = 60 s. Too slow = rejected.</div>
        </div>
        <div className="col-md-6">
          <label className="form-label fw-semibold" htmlFor="key">API key</label>
          <input id="key" type="password" autoComplete="off" className="form-control" value={key} onChange={(e) => setKey(e.target.value)} disabled={!admin}
            placeholder={setup.keySet ? "set — type to replace" : provider?.keyRequired ? "required" : "optional"} />
          <div className="form-text">Stored encrypted and write-only: never shown again.</div>
        </div>
        {admin && (
          <div className="col-12 d-flex flex-wrap gap-2">
            <button className="btn btn-success" disabled={!!busy}>{busy === "save" ? "Saving…" : "Save"}</button>
            <button type="button" className="btn btn-outline-success" disabled={!!busy}
              onClick={() => void run("test", async () => setTest(await api.testLlm()))}>
              <i className="bi bi-chat-square-text me-1" />{busy === "test" ? "Asking…" : "Test with a sample trade"}</button>
          </div>
        )}
        {test && (
          <div className="col-12">
            {test.ok ? (
              <div className="alert alert-success mb-0" role="status">
                The reviewer answered <strong>{test.verdict}</strong>{test.verdict === "reduce" && <> to {Math.round((test.sizeMultiplier ?? 0) * 100)} %</>} in {test.latencyMs} ms: “{test.reason}”
              </div>
            ) : (
              <div className="alert alert-warning mb-0" role="alert">No usable answer ({test.latencyMs} ms): {test.error}. A real trade would be rejected.</div>
            )}
          </div>
        )}
      </div></form>
    </>
  );
}
