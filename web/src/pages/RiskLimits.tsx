import { useEffect, useState, type FormEvent } from "react";
import { api, errorText, usd, type RiskLimits as Limits } from "../lib/api";
import { useSession } from "../lib/session";
import { useMode } from "../lib/mode";

// The owner's limits. The risk engine enforces them on every entry; they can be changed only
// while the agent is stopped, and every change is audited.
export function RiskLimits() {
  const { can, me } = useSession();
  const status = useMode();
  const [l, setL] = useState<Limits | null>(null);
  const [budget, setBudget] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    api.riskLimits().then((x) => { setL(x); setBudget((x.tradingBudgetCents / 100).toFixed(2)); }).catch((e) => setError(errorText(e)));
  }, []);
  const editable = can("settings.write") && me?.via === "session" && status?.agentState === "stopped";
  if (!l) return <><h1 className="h3">Risk limits</h1>{error && <div className="alert alert-danger">{error}</div>}</>;
  const num = (k: keyof Limits) => (e: React.ChangeEvent<HTMLInputElement>) => setL({ ...l, [k]: Number(e.target.value) });
  async function save(e: FormEvent) {
    e.preventDefault();
    if (!l) return;
    setError(""); setSaved(false);
    try {
      const next = await api.setRiskLimits({ ...l, tradingBudgetCents: Math.round(Number(budget) * 100) });
      setL(next); setSaved(true);
    } catch (err) {
      setError(errorText(err));
    }
  }
  const field = (k: keyof Limits & string, label: string, help: string, unit: string) => (
    <div className="col-md-6 col-xl-4">
      <label className="form-label fw-semibold" htmlFor={k}>{label}</label>
      <div className="input-group">
        <input id={k} type="number" className="form-control" value={l[k]} onChange={num(k)} disabled={!editable} required />
        <span className="input-group-text">{unit}</span>
      </div>
      <div className="form-text">{help}</div>
    </div>
  );
  return (
    <>
      <h1 className="h3">Risk limits</h1>
      <p className="text-secondary">
        Enforced by the risk engine on every order, in US dollars (the eToro account currency). Nothing can switch on
        leverage, short selling or CFDs: those are fixed in the code.
      </p>
      {!editable && can("settings.write") && <div className="alert alert-info">Stop the agent to change the limits (it is <strong>{status?.agentState}</strong>).</div>}
      {error && <div className="alert alert-danger" role="alert">{error}</div>}
      {saved && <div className="alert alert-success" role="status">Saved and audited.</div>}
      <form onSubmit={save} className="card"><div className="card-body row g-4">
        <div className="col-md-6 col-xl-4">
          <label className="form-label fw-semibold" htmlFor="budget">Trading budget</label>
          <div className="input-group">
            <span className="input-group-text">$</span>
            <input id="budget" type="number" step="0.01" min="0" className="form-control" value={budget} onChange={(e) => setBudget(e.target.value)} disabled={!editable} required />
          </div>
          <div className="form-text">The most the agent may have invested at once ({usd(l.tradingBudgetCents)} now). Never more than the account equity. 0 = no trading.</div>
        </div>
        {field("dailyLossBp", "Daily loss limit", "No new entries once equity is this far below the start of the day (100 bp = 1 %).", "bp")}
        {field("drawdownBp", "Drawdown kill switch", "Close everything and stop at this fall from the high-water mark; an admin must reset it.", "bp")}
        {field("maxPositionBp", "Largest position", "As a share of equity; 0 = no cap.", "bp")}
        {field("maxOpenPositions", "Open positions", "Positions plus pending orders (at most 15, so the kill switch can close them all within eToro's limit).", "max")}
        {field("noEntryBeforeCloseMinutes", "No entries before the close", "Minutes before the session ends.", "min")}
        {field("flattenBeforeCloseMinutes", "Close everything before the close", "Minutes before the session ends (never held overnight).", "min")}
        {field("maxPortfolioAgeSeconds", "Freshest cash figure", "The available cash must be at most this old when an order is sized.", "s")}
        {field("maxQuoteAgeSeconds", "Freshest price", "The entry price must be at most this old.", "s")}
        {editable && <div className="col-12"><button className="btn btn-success">Save limits</button></div>}
      </div></form>
    </>
  );
}
