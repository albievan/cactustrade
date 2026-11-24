import { useEffect, useState } from "react";
import { api, type RiskLimitsView, type RiskPresetList } from "../lib/api";
import { ProblemAlert } from "./ProblemAlert";

// The risk presets (R28): one compact button per preset with its numbers, plus "Custom" when
// the limits match none (or are being edited by hand). The server builds every preset, says
// which one matches and applies every rule of a change of limits; this only shows and asks.

function pct(bp: number): string {
  return `${bp / 100} %`;
}

export function RiskPresets({ canEdit, edited, current: stored, reloadKey, onApplied }: {
  canEdit: boolean;
  // edited: the form below was changed by hand and not saved yet (shown as Custom).
  edited: boolean;
  // current: the preset the stored limits match, as the server last said (after a save).
  current?: RiskPresetList["current"];
  reloadKey: string;
  onApplied: (v: RiskLimitsView) => void;
}) {
  const [list, setList] = useState<RiskPresetList | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api.riskPresets().then(setList).catch(() => setList(null));
  }, [reloadKey]);
  if (!list) return null;
  const current = edited ? "custom" : (stored ?? list.current);
  async function apply(id: RiskPresetList["presets"][number]["id"]) {
    setBusy(true); setError(null);
    try {
      const v = await api.applyRiskPreset(id);
      setList((l) => (l ? { ...l, current: v.preset } : l));
      onApplied(v);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  const active = list.presets.find((p) => p.id === current);
  return (
    <section className="mb-3" aria-labelledby="presets-heading">
      <h3 className="h6 mb-2" id="presets-heading">Presets</h3>
      <div className="d-flex flex-wrap gap-2" role="group" aria-label="Risk presets">
        {list.presets.map((p) => (
          <button key={p.id} type="button" disabled={!canEdit || busy} aria-pressed={current === p.id}
            className={"btn btn-sm text-start preset-btn " + (current === p.id ? "btn-success" : "btn-outline-secondary")}
            title={`${p.label}: ${p.summary}`} aria-label={`${p.label} preset: ${p.summary}`} onClick={() => void apply(p.id)}>
            <span className="fw-semibold d-block">{p.label}</span>
            <span className="small d-block">
              day loss {pct(p.dailyLossBp)} · kill at −{pct(p.drawdownBp)} · ≤ {pct(p.maxPositionBp)} a share · ≤ {p.maxOpenPositions} positions · {p.maxShortBp > 0 ? `shorts ≤ ${pct(p.maxShortBp)}` : "no shorts"}
            </span>
          </button>
        ))}
        <button type="button" disabled aria-pressed={current === "custom"}
          className={"btn btn-sm text-start preset-btn " + (current === "custom" ? "btn-secondary" : "btn-outline-secondary")}
          title="Limits set by hand that match no preset" aria-label="Custom: limits set by hand">
          <span className="fw-semibold d-block">Custom</span>
          <span className="small d-block">set by hand below</span>
        </button>
      </div>
      <p className="small text-secondary mt-2 mb-0" aria-live="polite">
        {active ? <><strong>{active.label}</strong>: {active.summary}.</> : "Custom: the limits below were set by hand."}
        {" "}A preset keeps your trading budget and never shortens the time before the close.
        {list.clamped && " On this real-money account every preset is capped at the real-money ceilings."}
      </p>
      <ProblemAlert error={error} className="py-2 small mt-2" />
    </section>
  );
}
