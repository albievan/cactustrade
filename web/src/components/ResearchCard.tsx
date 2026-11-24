import { useCallback, useEffect, useState } from "react";
import { api, type ResearchProposal, type ResearchRun, type ResearchView } from "../lib/api";
import { FixButton } from "./FixButton";
import { InfoCard } from "./InfoCard";
import { ProblemAlert } from "./ProblemAlert";

const MODES: Record<string, string> = { off: "off", propose: "proposes, you accept", auto_add: "adds eligible shares" };

// ResearchCard is the Watchlist page's web research (R29). The server decides everything: the
// mode, whether research can run, who may run it or accept proposals, and what is missing.
// Every text the model wrote (name, reason, sources) is untrusted and shown as plain text:
// sources are never links, so nothing on this page opens or fetches them.
export function ResearchCard({ accountId, onChanged }: { accountId?: string; onChanged: () => void }) {
  const [view, setView] = useState<ResearchView | null>(null);
  const [result, setResult] = useState<ResearchRun | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState<unknown>(null);
  const load = useCallback(() => { api.research().then(setView).catch(setError); }, []);
  useEffect(() => { if (accountId) { setResult(null); load(); } }, [load, accountId]);
  async function act(label: string, f: () => Promise<void>) {
    setBusy(label);
    setError(null);
    try { await f(); } catch (e) { setError(e); } finally { setBusy(""); load(); }
  }
  if (!view) return error ? <ProblemAlert error={error} /> : null;
  const limitReached = view.config.runsPerDay > 0 && view.runsUsed >= view.config.runsPerDay;
  return (
    <div className="card mb-3" aria-label="Web research">
      <div className="card-body">
        <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
          <h2 className="h6 mb-0"><i className="bi bi-globe2 me-1" aria-hidden="true" />Web research</h2>
          <span className="badge text-bg-light border" title="What research may do for this account">{MODES[view.mode] ?? view.mode}</span>
          {view.profileName && <span className="badge text-bg-light border" title="The LLM profile research uses"><i className="bi bi-robot me-1" />{view.profileName}</span>}
          <span className="small text-secondary" title="Runs in the last 24 hours">
            {view.runsUsed} / {view.config.runsPerDay} runs today
            {view.lastRunAt && <> · last {new Date(view.lastRunAt).toLocaleString()}</>}
          </span>
          {view.canRun && (
            <button type="button" className="btn btn-sm btn-outline-primary ms-auto" disabled={!!busy || !view.ready || limitReached}
              title={!view.ready ? view.note ?? "Research cannot run" : limitReached ? "The runs for today are used up" : "Ask the research profile for shares now"}
              aria-label="Research now" onClick={() => void act("run", async () => {
                const r = await api.runResearch();
                setResult(r);
                if (r.added.length > 0) onChanged();
              })}>
              {busy === "run" ? <span className="spinner-border spinner-border-sm me-1" /> : <i className="bi bi-search me-1" />}Research now
            </button>
          )}
        </div>
        <InfoCard id="research" title="What web research does">
          <p className="mb-2 small">
            Before the first market opens each day (and when you press <em>Research now</em>), an LLM searches the web for shares that fit
            this account's themes and are in the news. It uses only its provider's built-in web search: it has no other tool, no eToro
            access and no key but its own.
          </p>
          <ul className="mb-0 small">
            <li>Every share it names goes through the same check as a share you add by hand: eToro must list it on LSE, NYSE / NASDAQ or the ASX and allow it at leverage 1.</li>
            <li>The watchlist only says what the agent looks at. <strong>Nothing is traded because of research:</strong> a trade still needs the statistical model and its playbooks, the risk engine and the reviewer (which can only shrink or reject).</li>
            <li>Web pages can contain anything. The reasons and links below are shown as plain text and are never opened by cactusTrade.</li>
            <li>Each run is one paid request to the LLM provider (plus its web search fees); the runs per day and shares per run are set on the LLM page.</li>
          </ul>
        </InfoCard>
        {!view.ready && view.note && (
          <div className="alert alert-secondary d-flex flex-wrap align-items-center gap-2 py-2 small" role="status">
            <span className="flex-grow-1">{view.note}</span>
            {view.missing.filter((m) => m.canFix).map((m) => <FixButton key={m.what} what={m.what} accountId={accountId} />)}
          </div>
        )}
        <ProblemAlert error={error} accountId={accountId} />
        {result && <RunResult run={result} onClose={() => setResult(null)} />}
        {view.proposals.length > 0 && (
          <ul className="list-group" aria-label="Research proposals">
            {view.proposals.map((p) => (
              <ProposalRow key={p.id} p={p} canDecide={view.canDecide} busy={busy}
                onAccept={() => void act("accept" + p.id, async () => { await api.acceptResearchProposal(p.id); onChanged(); })}
                onDismiss={() => void act("dismiss" + p.id, async () => { await api.dismissResearchProposal(p.id); })} />
            ))}
          </ul>
        )}
        {view.mode === "propose" && view.proposals.length === 0 && <p className="small text-secondary mb-0">No proposals waiting.</p>}
      </div>
    </div>
  );
}

function ProposalRow({ p, canDecide, busy, onAccept, onDismiss }: {
  p: ResearchProposal; canDecide: boolean; busy: string; onAccept: () => void; onDismiss: () => void;
}) {
  return (
    <li className="list-group-item d-flex gap-2 align-items-start">
      {canDecide && (
        <div className="btn-group-vertical btn-group-sm" role="group" aria-label={`Decide on ${p.symbol}`}>
          <button type="button" className="btn btn-outline-success" disabled={!!busy} title={`Accept ${p.symbol}: add it to the watchlist after eToro's check`}
            aria-label={`Accept ${p.symbol}`} onClick={onAccept}><i className="bi bi-check-lg" /></button>
          <button type="button" className="btn btn-outline-secondary" disabled={!!busy} title={`Dismiss ${p.symbol}`}
            aria-label={`Dismiss ${p.symbol}`} onClick={onDismiss}><i className="bi bi-x-lg" /></button>
        </div>
      )}
      <div className="flex-grow-1 min-w-0 small">
        <div className="d-flex flex-wrap align-items-center gap-2">
          <strong>{p.symbol}</strong>
          <span className="badge text-bg-light border">{p.exchange}</span>
          <span className="text-secondary text-break">{p.name}</span>
          {p.theme && <span className="badge text-bg-info" title="The theme research named">{p.theme}</span>}
        </div>
        <div className="text-break">{p.reason}</div>
        <div className="text-secondary" aria-label={`Sources for ${p.symbol}`}>
          {p.sources.map((s) => <span key={s} className="d-block text-break font-monospace">{s}</span>)}
        </div>
      </div>
    </li>
  );
}

function RunResult({ run: r, onClose }: { run: ResearchRun; onClose: () => void }) {
  const variant = r.error ? "warning" : "success";
  return (
    <div className={`alert alert-${variant} d-flex gap-2 small`} role="status" aria-label="Research result">
      <div className="flex-grow-1">
        {r.error ? <><strong>Research failed, nothing was added:</strong> {r.error}</> : (
          <>
            <strong>Research done:</strong>{" "}
            {r.mode === "propose" ? `${r.proposed.length} proposed` : `${r.added.length} added`}, {r.refused.length} refused
            {r.costUsd && <> · cost ${r.costUsd}</>}.
            <ul className="mb-0">
              {r.added.map((a) => <li key={"a" + a.instrumentId}>Added <strong>{a.symbol}</strong>{a.theme && <> (theme {a.theme})</>}: {a.reason}</li>)}
              {r.refused.map((x, i) => <li key={"r" + i + x.symbol}>Not added: <strong>{x.symbol || "?"}</strong> — {x.reason}</li>)}
            </ul>
          </>
        )}
      </div>
      <button type="button" className="btn-close" aria-label="Close the research result" onClick={onClose} />
    </div>
  );
}
