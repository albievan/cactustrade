import { useEffect, useState } from "react";
import { api, type AgentStyle, type StyleLevel } from "../lib/api";
import { ProblemAlert } from "./ProblemAlert";

// The agent's aggressiveness and autonomy (R28). The server says what each level does, which
// one is chosen, which one the strategy uses now and whether the caller may change them (the
// rules of the risk limits); this only shows it and sends the choice.

type Kind = "aggressiveness" | "autonomy";

const heading: Record<Kind, { title: string; help: string }> = {
  aggressiveness: {
    title: "Aggressiveness",
    help: "How boldly the strategy proposes trades. It never changes a risk limit: the risk engine still sizes and refuses every entry.",
  },
  autonomy: {
    title: "Autonomy",
    help: "How much cactusTrade does by itself: finding shares, keeping the watchlist and starting the agent.",
  },
};

export function AgentStylePanel({ reloadKey }: { reloadKey: string }) {
  const [style, setStyle] = useState<AgentStyle | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState("");
  useEffect(() => {
    api.agentStyle().then(setStyle).catch(() => setStyle(null));
  }, [reloadKey]);
  if (!style) return null;
  async function choose(kind: Kind, level: StyleLevel) {
    setBusy(true); setError(null); setSaved("");
    try {
      const next = await api.setAgentStyle(kind === "aggressiveness" ? { aggressiveness: level } : { autonomy: level });
      setStyle(next);
      setSaved(`${heading[kind].title} is now ${level}. Saved and audited.`);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  const group = (kind: Kind) => {
    const levels = kind === "aggressiveness" ? style.aggressivenessLevels : style.autonomyLevels;
    const chosen = kind === "aggressiveness" ? style.aggressiveness : style.autonomy;
    const active = levels.find((l) => l.id === chosen);
    return (
      <div className="col-lg-6">
        <h3 className="h6 mb-1" id={`${kind}-heading`}>{heading[kind].title}</h3>
        <p className="small text-secondary mb-2">{heading[kind].help}</p>
        <div className="btn-group btn-group-sm mb-2" role="group" aria-labelledby={`${kind}-heading`}>
          {levels.map((l) => (
            <button key={l.id} type="button" disabled={!style.canEdit || busy} aria-pressed={chosen === l.id}
              className={"btn " + (chosen === l.id ? "btn-success" : "btn-outline-secondary")}
              title={l.summary} aria-label={`${heading[kind].title} ${l.label}: ${l.summary}`} onClick={() => void choose(kind, l.id)}>
              {l.label}
            </button>
          ))}
        </div>
        {active && <p className="small mb-1"><strong>{active.label}</strong>: {active.summary}</p>}
        {kind === "aggressiveness" && style.effectiveAggressiveness !== style.aggressiveness && (
          <p className="small text-warning-emphasis mb-1" role="status">
            <i className="bi bi-arrow-down-circle me-1" aria-hidden="true" />
            The strategy uses <strong>{style.effectiveAggressiveness}</strong> for now: full autonomy lowered it after a day that
            reached the daily loss limit, and raises it again after clean trading days.
          </p>
        )}
      </div>
    );
  };
  return (
    <section className="card mb-4" aria-labelledby="style-heading">
      <div className="card-body">
        <h2 className="h5" id="style-heading">How the agent trades</h2>
        <p className="small text-secondary">
          Changed like the limits: by the account's owner, while the agent is stopped. Neither choice can change leverage, the
          cash and budget checks, the short cap, the stops or the close before the end of the session.
        </p>
        <div className="row g-4">
          {group("aggressiveness")}
          {group("autonomy")}
        </div>
        {saved && <div className="alert alert-success py-2 small mt-3 mb-0" role="status">{saved}</div>}
        <ProblemAlert error={error} className="py-2 small mt-3" />
      </div>
    </section>
  );
}
