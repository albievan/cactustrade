import { useEffect, useState, type FormEvent } from "react";
import { api, type LlmSetup, type ResearchConfig, type ResearchSetup } from "../lib/api";
import { FixButton } from "./FixButton";
import { ProblemAlert } from "./ProblemAlert";

// ResearchSettings is the web research section of the LLM page (R29): which profile searches
// the web, how many shares per run and how many runs a day. Only providers with a built-in web
// search can be chosen (the server checks it again); the default is the default reviewer
// profile when its provider can search.
export function ResearchSettings({ setup, admin }: { setup: LlmSetup; admin: boolean }) {
  const [rs, setRs] = useState<ResearchSetup | null>(null);
  const [form, setForm] = useState<ResearchConfig | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    api.researchSettings().then((x) => { setRs(x); setForm(x.config); }).catch(setError);
  }, [setup]);
  if (!rs || !form) return <ProblemAlert error={error} />;
  const searches = (provider: string) => setup.providers.find((p) => p.id === provider)?.webSearch === true;
  const usable = setup.profiles.filter((p) => searches(p.provider));
  async function save(e: FormEvent) {
    e.preventDefault();
    if (!form) return;
    setBusy(true); setError(null); setMsg("");
    try {
      const x = await api.setResearchSettings(form);
      setRs(x); setForm(x.config); setMsg("Saved and audited.");
    } catch (err) { setError(err); } finally { setBusy(false); }
  }
  const supported = setup.providers.filter((p) => p.webSearch).map((p) => p.label).join(" and ");
  return (
    <div className="card mb-4" aria-label="Web research settings">
      <div className="card-body">
        <h2 className="h5">Web research</h2>
        <p className="small text-secondary">
          Once a day before the first market opens (and on <em>Research now</em> on the Watchlist page), the research profile searches the
          web for shares that fit each account's themes. It only proposes or adds watchlist shares after eToro's eligibility check; it never
          trades. Research needs a provider with a built-in web search: {supported}; every other provider is <em>research not supported</em>.
        </p>
        {rs.note && (
          <div className="alert alert-secondary d-flex flex-wrap align-items-center gap-2 py-2 small" role="status">
            <span className="flex-grow-1">{rs.note}</span>
            {rs.missing.filter((m) => m.canFix).map((m) => <FixButton key={m.what} what={m.what} />)}
          </div>
        )}
        {msg && <div className="alert alert-success py-2" role="status">{msg}</div>}
        <ProblemAlert error={error} />
        <form className="row g-3 align-items-end" onSubmit={(e) => void save(e)}>
          <div className="col-md-6">
            <label className="form-label fw-semibold" htmlFor="research-profile">Research profile</label>
            <select id="research-profile" className="form-select" value={form.profileId} disabled={!admin || busy}
              onChange={(e) => setForm({ ...form, profileId: e.target.value })}>
              <option value="">Default reviewer profile{rs.profileName && !form.profileId ? ` (${rs.profileName})` : ""}</option>
              {usable.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
            <div className="form-text">Only profiles whose provider can search the web are listed.</div>
          </div>
          <div className="col-6 col-md-2">
            <label className="form-label fw-semibold" htmlFor="research-shares">Shares per run</label>
            <input id="research-shares" type="number" className="form-control" min={1} max={20} required value={form.maxShares} disabled={!admin || busy}
              onChange={(e) => setForm({ ...form, maxShares: Number(e.target.value) })} />
          </div>
          <div className="col-6 col-md-2">
            <label className="form-label fw-semibold" htmlFor="research-runs">Runs per day</label>
            <input id="research-runs" type="number" className="form-control" min={0} max={24} required value={form.runsPerDay} disabled={!admin || busy}
              onChange={(e) => setForm({ ...form, runsPerDay: Number(e.target.value) })} />
            <div className="form-text">0 = off</div>
          </div>
          {admin && (
            <div className="col-md-2">
              <button className="btn btn-primary w-100" disabled={busy}><i className="bi bi-save me-1" />Save</button>
            </div>
          )}
        </form>
      </div>
    </div>
  );
}
