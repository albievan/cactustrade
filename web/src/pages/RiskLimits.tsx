import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, usd, type AgentCard, type RiskLimits as Limits } from "../lib/api";
import { useMode, useRefreshMode } from "../lib/mode";
import { NeedAccount } from "../components/NeedAccount";
import { InfoCard } from "../components/InfoCard";
import { ProblemAlert } from "../components/ProblemAlert";
import { AgentCardView, type AgentAction } from "../components/AgentCards";
import { ForceCloseDialog, forceCloseNote } from "../components/ForceCloseDialog";

// The owner's limits. The risk engine enforces them on every entry; they can be changed only
// while the agent is stopped, and every change is audited. Whether the caller may change them
// now comes from the server (canEdit, editBlockedReason); this page only shows it.
//
// Above the form, one card per eToro account (R23) stops, pauses and starts its agent without
// leaving the page; which buttons work is the server's decision (card.controls). The page
// remembers, in its own state only, which agents it stopped while they were running, and
// offers to start the current one again after its limits were saved.
export function RiskLimits() {
  const status = useMode();
  const refreshMode = useRefreshMode();
  const [l, setL] = useState<Limits | null>(null);
  const [edit, setEdit] = useState<{ canEdit: boolean; reason?: string }>({ canEdit: false });
  const [budget, setBudget] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  const [cards, setCards] = useState<AgentCard[] | null>(null);
  const [cardError, setCardError] = useState<unknown>(null);
  const [cardNote, setCardNote] = useState("");
  const [busy, setBusy] = useState("");
  const [stoppedHere, setStoppedHere] = useState<string[]>([]); // accounts whose running agent this page stopped
  const [forceClose, setForceClose] = useState<{ accountId?: string; all?: boolean } | null>(null);
  const accountId = status?.account?.id;
  const agentState = status?.agentState; // a change of state may change canEdit: fetch again
  useEffect(() => {
    if (!accountId) return;
    api.riskLimits().then(({ canEdit, editBlockedReason, ...x }) => {
      setL(x); setEdit({ canEdit, reason: editBlockedReason });
      setBudget((x.tradingBudgetCents / 100).toFixed(2));
    }).catch(setError);
  }, [accountId, agentState]);
  const loadCards = useCallback(async () => {
    try {
      setCards(await api.agents());
    } catch (e) {
      setCardError(e);
    }
  }, []);
  useEffect(() => { if (accountId) void loadCards(); }, [loadCards, accountId, agentState]);

  async function act(card: AgentCard, action: AgentAction) {
    const id = card.account.id;
    if (action === "forceClose") {
      setForceClose({ accountId: id });
      return;
    }
    setBusy(id); setCardError(null); setCardNote("");
    try {
      const run = { start: api.startAgent, pause: api.pauseAgent, stop: api.stopAgent }[action];
      const st = await run(id);
      if (action === "stop" && card.state === "running") setStoppedHere((s) => (s.includes(id) ? s : [...s, id]));
      if (action === "start") setStoppedHere((s) => s.filter((x) => x !== id));
      setCardNote(`${card.account.name}: the agent is ${st.state}.`);
    } catch (e) {
      setCardError(e);
    } finally {
      setBusy("");
      await loadCards();
      await refreshMode();
    }
  }
  async function switchTo(id: string) {
    setBusy(id); setCardError(null); setCardNote(""); setSaved(false);
    try {
      await api.setCurrentAccount(id);
      await refreshMode(); // the limits below follow the new current account
    } catch (e) {
      setCardError(e);
    } finally {
      setBusy("");
    }
  }

  if (status && !status.account) return <><h1 className="h3">Risk limits</h1><NeedAccount /></>;
  const current = cards?.find((c) => c.current);
  const canForceCloseAll = (cards ?? []).filter((c) => c.controls.forceClose).length > 1;
  const agents = (
    <section className="mb-4" aria-labelledby="agents-heading">
      <div className="d-flex align-items-center gap-2 mb-2">
        <h2 className="h5 mb-0" id="agents-heading">Agents</h2>
        {canForceCloseAll && (
          <button type="button" className="btn btn-sm btn-outline-danger ms-auto" disabled={!!busy}
            title="Force close every demo account you can trade (admin, password)" onClick={() => setForceClose({ all: true })}>
            <i className="bi bi-door-closed me-1" />Force close all
          </button>
        )}
      </div>
      <p className="small text-secondary">Stop an agent to change its limits, then start it again. Click an account's name to edit its limits.</p>
      <ProblemAlert error={cardError} className="py-2 small" />
      {cardNote && <div className="alert alert-success py-2 small" role="status">{cardNote}</div>}
      {cards && (
        <div className="row row-cols-1 row-cols-md-2 row-cols-xxl-3 g-3">
          {cards.map((c) => (
            <AgentCardView key={c.account.id} card={c} busy={busy === c.account.id}
              onAction={(a) => void act(c, a)} onSwitch={() => void switchTo(c.account.id)} />
          ))}
        </div>
      )}
      {forceClose && (
        <ForceCloseDialog accountId={forceClose.accountId} all={forceClose.all} onCancel={() => setForceClose(null)}
          onDone={async (reports) => {
            setForceClose(null); setCardError(null); setCardNote("Force close: " + forceCloseNote(reports));
            await loadCards();
            await refreshMode();
          }} />
      )}
    </section>
  );
  if (!l) return <><h1 className="h3">Risk limits</h1>{agents}<ProblemAlert error={error} /></>;
  const editable = edit.canEdit;
  const num = (k: keyof Limits) => (e: React.ChangeEvent<HTMLInputElement>) => setL({ ...l, [k]: Number(e.target.value) });
  async function save(e: FormEvent) {
    e.preventDefault();
    if (!l) return;
    setError(""); setSaved(false);
    try {
      const next = await api.setRiskLimits({ ...l, tradingBudgetCents: Math.round(Number(budget) * 100) });
      setL(next); setSaved(true);
    } catch (err) {
      setError(err);
    }
  }
  const startAgain = saved && current && accountId && stoppedHere.includes(accountId);
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
        Enforced by the risk engine on every order, in US dollars (the eToro account currency). Leverage is always 1;
        longs buy the real share, shorts are CFDs with a mandatory stop, and nothing is held overnight — fixed in the code.
      </p>
      {agents}
      <h2 className="h5">Limits</h2>
      {status?.account && <p className="small">Account: <strong>{status.account.name}</strong> ({status.account.mode}).</p>}
      <InfoCard id="risk-states" title="When the limits can change">
        <p>The limits are locked while the agent could act on them. They can be changed only while it is <strong>stopped</strong>.</p>
        <ul className="mb-0">
          <li><strong>Running</strong>: the agent looks for entries at every 5-minute bar close.</li>
          <li><strong>Paused</strong>: no new entries, but the agent is not stopped: the limits stay locked and it can be started
            again as it was. Positions due are still closed before the close.</li>
          <li><strong>Stopped</strong>: no new entries; positions due are still closed. The limits can be changed.</li>
          <li><strong>Killed</strong>: the kill switch is on (by hand, or because equity fell by the drawdown limit). No entries;
            cactusTrade closed its own positions. An admin must reset it on the dashboard — the agent then comes back stopped,
            and the limits can be changed.</li>
        </ul>
      </InfoCard>
      {!editable && edit.reason && <div className="alert alert-info" role="status"><i className="bi bi-lock-fill me-1" />{edit.reason}.</div>}
      <ProblemAlert error={error} />
      {saved && (
        <div className="alert alert-success d-flex flex-wrap align-items-center gap-2" role="status">
          <span className="flex-grow-1">Saved and audited.</span>
          {startAgain && current && (
            <button type="button" className="btn btn-sm btn-success" disabled={!!busy || !current.controls.start}
              title={current.controls.startBlockedReason ? "Cannot start: " + current.controls.startBlockedReason : "Start the agent with the new limits"}
              onClick={() => void act(current, "start")}>
              <i className="bi bi-play-fill me-1" />Start the agent again
            </button>
          )}
        </div>
      )}
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
        {field("maxShortBp", "Short selling", "All shorts together (CFDs at leverage 1, each with a stop above the entry) as a share of the trading budget. 0 = no shorts; at most 2500 bp = 25 %.", "bp")}
        {editable && <div className="col-12"><button className="btn btn-success">Save limits</button></div>}
      </div></form>
    </>
  );
}
