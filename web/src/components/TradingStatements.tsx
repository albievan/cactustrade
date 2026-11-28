import { useCallback, useEffect, useState, type FormEvent, type KeyboardEvent } from "react";
import {
  api, platformLabel, statementPreviewUrl,
  type StatementAccountRef, type StatementPeriod, type StatementSchedule, type StatementScheduleInput, type StatementScope,
  type StatementsPage,
} from "../lib/api";
import { useSession } from "../lib/session";
import { ConfirmDialog } from "./ConfirmDialog";
import { FixButton } from "./FixButton";
import { InfoCard } from "./InfoCard";
import { oneOf, usePref } from "../lib/prefs";
import { ProblemAlert } from "./ProblemAlert";

// Trading statements (R30): schedules that e-mail daily, weekly or monthly PDF statements,
// a preview download and "send now". The server decides who may do what and what a
// statement holds; this section only shows and edits.

const MAX_RECIPIENTS = 10;

const periodLabel: Record<StatementPeriod, string> = { daily: "Daily", weekly: "Weekly", monthly: "Monthly" };
const periodWhen: Record<StatementPeriod, string> = {
  daily: "each trading day (Monday to Friday) at 22:30 UTC",
  weekly: "every Saturday at 06:00 UTC, for Monday to Friday",
  monthly: "on the 1st at 06:00 UTC, for the month before",
};
const exchanges = ["LSE", "NYSE", "ASX", "Other"] as const;
const isPeriod = (v: unknown) => typeof v === "string" && v in periodLabel;
const isScope = oneOf<StatementScope>(["combined", "account", "exchange"]);
const isExchange = oneOf<string>(exchanges);

function when(iso?: string): string {
  return iso ? new Date(iso).toLocaleString() : "";
}

function blank(): StatementScheduleInput {
  return { period: "weekly", accountIds: [], recipients: [], includeCombined: true, includeAccounts: false, includeExchanges: false, enabled: true };
}

// scheduleName names a schedule for buttons and dialogs.
function scheduleName(s: StatementSchedule): string {
  return `${periodLabel[s.period].toLowerCase()} statements to ${s.recipients.join(", ")}`;
}

export function TradingStatements() {
  const { me } = useSession();
  const [page, setPage] = useState<StatementsPage | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [msg, setMsg] = useState("");
  const [editing, setEditing] = useState<StatementSchedule | "new" | null>(null);
  const [deleting, setDeleting] = useState<StatementSchedule | null>(null);
  const [busy, setBusy] = useState("");
  const load = useCallback(async () => {
    try {
      setPage(await api.statementSchedules());
    } catch (e) {
      setError(e);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  async function sendNow(s: StatementSchedule) {
    setBusy("send" + s.id); setError(null); setMsg("");
    try {
      const r = await api.sendStatementNow(s.id);
      setMsg(`${periodLabel[s.period]} statement for ${r.title} queued for ${r.recipients} recipient${r.recipients === 1 ? "" : "s"} ` +
        `(${r.files.length} PDF${r.files.length === 1 ? "" : "s"}).`);
      await load();
    } catch (e) {
      setError(e);
    } finally {
      setBusy("");
    }
  }

  async function remove(s: StatementSchedule) {
    setError(null); setMsg("");
    try {
      await api.deleteStatementSchedule(s.id);
      setDeleting(null);
      setMsg("Schedule deleted.");
      await load();
    } catch (e) {
      setDeleting(null);
      setError(e);
    }
  }

  return (
    <section className="mt-5" aria-labelledby="statements-heading">
      <h2 className="h4" id="statements-heading">Trading statements</h2>
      <InfoCard id="statements" title="What is sent and when">
        <p>
          A statement is a PDF of what cactusTrade did over a day, a week or a month: equity per currency (never added across
          currencies), the closed trades of positions it opened with realised P&amp;L and fees (unknown fees shown as unknown),
          the trades per exchange and per account, positions still open at the end, drawdown, kill switch and daily-limit events,
          the agent&apos;s decisions and orders, and LLM reviewer use. It opens with a short summary written from those figures
          (no LLM). It reports past records only: software, not financial advice — trading can lose money.
        </p>
        <ul className="mb-2">
          <li><strong>Daily</strong> {periodWhen.daily}; <strong>weekly</strong> {periodWhen.weekly}; <strong>monthly</strong> {periodWhen.monthly}. Periods are risk days, from 22:00 UTC to 22:00 UTC.</li>
          <li>Each recipient gets one e-mail with the PDFs you choose: the combined statement of the schedule&apos;s accounts and exchanges, one per account, and one per exchange with trades or positions.</li>
          <li>Each period is sent once, also across restarts. A new or re-enabled schedule starts with the next period: use <em>Send now</em> for the last one.</li>
          <li>You can put the accounts you own on a schedule (an administrator: the accounts they can see). Trading 212, Saxo and Tiger accounts add equity and holdings only.</li>
        </ul>
        <p className="mb-0 small text-secondary">E-mail goes through the outbox (retries; mail the server accepted is never sent twice). <em>Preview</em> downloads the PDF without e-mailing it.</p>
      </InfoCard>
      {page && !page.mailServer && (
        <div className="alert alert-warning d-flex flex-wrap align-items-center gap-2" role="alert">
          <span className="flex-grow-1">No mail server is set up: scheduled statements wait in the outbox until one is{page.canFixMail ? "." : " (ask an administrator)."}</span>
          {page.canFixMail && <FixButton what="email_server" />}
        </div>
      )}
      <ProblemAlert error={error} />
      {msg && <div className="alert alert-success" role="status">{msg}</div>}
      {!page ? (!error && <p className="text-secondary">Loading…</p>) : (
        <>
          <div className="d-flex flex-wrap gap-2 align-items-start mb-3">
            <button type="button" className="btn btn-success" disabled={editing !== null || page.accounts.length === 0}
              title={page.accounts.length === 0 ? "You own no account to make statements of" : "Add a statement schedule"}
              onClick={() => { setMsg(""); setEditing("new"); }}>
              <i className="bi bi-plus-lg me-1" />Add schedule
            </button>
            <PreviewForm accounts={page.accounts} />
          </div>
          {editing === "new" && (
            <ScheduleForm accounts={page.accounts} initial={blank()} onCancel={() => setEditing(null)}
              onSaved={async () => { setEditing(null); setMsg("Schedule added: the first e-mail goes out after the next period closes."); await load(); }} />
          )}
          {page.schedules.length === 0 && editing === null && (
            <p className="text-secondary">No statement schedules yet.</p>
          )}
          <div className="row row-cols-1 row-cols-lg-2 row-cols-xxl-3 g-3">
            {page.schedules.map((s) => editing !== "new" && editing?.id === s.id ? (
              <div key={s.id} className="col-12 w-100">
                <ScheduleForm accounts={page.accounts} initial={s} id={s.id} onCancel={() => setEditing(null)}
                  onSaved={async () => { setEditing(null); setMsg("Schedule saved."); await load(); }} />
              </div>
            ) : (
              <div key={s.id} className="col">
                <div className={`card h-100 statement-card${s.enabled ? "" : " opacity-75"}`}><div className="card-body d-flex gap-3">
                  <div className="btn-group-vertical btn-group-sm align-self-start" role="group" aria-label={`Actions for ${scheduleName(s)}`}>
                    <button type="button" className="btn btn-outline-success" disabled={!!busy} title="Send now (the last complete period)"
                      aria-label={`Send now: ${scheduleName(s)}`} onClick={() => void sendNow(s)}>
                      {busy === "send" + s.id ? <span className="spinner-border spinner-border-sm" /> : <i className="bi bi-send" />}</button>
                    <a className="btn btn-outline-secondary" href={statementPreviewUrl({ period: s.period, scheduleId: s.id })}
                      title="Preview: download the last complete period's combined PDF" aria-label={`Preview: ${scheduleName(s)}`}>
                      <i className="bi bi-file-earmark-pdf" /></a>
                    <button type="button" className="btn btn-outline-secondary" title="Edit" aria-label={`Edit: ${scheduleName(s)}`}
                      onClick={() => { setMsg(""); setEditing(s); }}><i className="bi bi-pencil" /></button>
                    <button type="button" className="btn btn-outline-danger" title="Delete" aria-label={`Delete: ${scheduleName(s)}`}
                      onClick={() => setDeleting(s)}><i className="bi bi-trash" /></button>
                  </div>
                  <ScheduleCard s={s} mine={s.ownerId === me?.user.id} />
                </div></div>
              </div>
            ))}
          </div>
        </>
      )}
      {deleting && (
        <ConfirmDialog title="Delete this statement schedule?" confirmLabel="Delete" icon="trash" onCancel={() => setDeleting(null)}
          onConfirm={() => remove(deleting)}>
          <p className="mb-0">No more {scheduleName(deleting)}. E-mails already in the outbox are still delivered.</p>
        </ConfirmDialog>
      )}
    </section>
  );
}

function ScheduleCard({ s, mine }: { s: StatementSchedule; mine: boolean }) {
  const includes = [s.includeCombined && "Combined", s.includeAccounts && "Per account", s.includeExchanges && "Per exchange"].filter(Boolean) as string[];
  return (
    <div className="flex-grow-1 min-w-0 small">
      <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
        <strong className="fs-6">{periodLabel[s.period]}</strong>
        {s.enabled ? <span className="badge text-bg-success">on</span> : <span className="badge text-bg-secondary">off</span>}
        {!mine && <span className="badge text-bg-light border" title="Owner"><i className="bi bi-person me-1" />{s.ownerName}</span>}
      </div>
      <div className="mb-1"><span className="text-secondary">Accounts: </span>
        {s.accountIds.length === 0 ? <>all {s.accounts.length === 1 ? "(1)" : `(${s.accounts.length})`}: </> : null}
        {s.accounts.length === 0 ? <span className="text-warning">none any more</span> : s.accounts.map((a) => a.name).join(", ")}
      </div>
      <div className="mb-1 d-flex flex-wrap gap-1 align-items-center"><span className="text-secondary">To: </span>
        {s.recipients.map((r) => <span key={r} className="badge text-bg-light border text-break">{r}</span>)}
      </div>
      <div className="mb-1 d-flex flex-wrap gap-1 align-items-center"><span className="text-secondary">PDFs: </span>
        {includes.map((i) => <span key={i} className="badge text-bg-light border">{i}</span>)}
      </div>
      <div className="mb-1"><span className="text-secondary">Last sent: </span>{s.lastSentAt ? `${when(s.lastSentAt)} (${s.lastPeriod})` : "never"}</div>
      <div><span className="text-secondary">Next send: </span>{s.nextSendAt ? when(s.nextSendAt) : "switched off"}</div>
      {s.lastError && <div className="alert alert-warning py-1 px-2 mt-2 mb-0">Last send failed: {s.lastError}</div>}
    </div>
  );
}

// ScheduleForm adds or edits a schedule: period, accounts (none ticked = all), recipient
// chips, the PDFs to attach and whether it is on.
function ScheduleForm({ accounts, initial, id, onCancel, onSaved }: {
  accounts: StatementAccountRef[];
  initial: StatementScheduleInput;
  id?: string;
  onCancel: () => void;
  onSaved: () => Promise<void>;
}) {
  const [x, setX] = useState<StatementScheduleInput>({
    period: initial.period, accountIds: [...initial.accountIds], recipients: [...initial.recipients], includeCombined: initial.includeCombined,
    includeAccounts: initial.includeAccounts, includeExchanges: initial.includeExchanges, enabled: initial.enabled,
  });
  const [addr, setAddr] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const full = x.recipients.length >= MAX_RECIPIENTS;
  function addRecipient() {
    const a = addr.trim().toLowerCase();
    if (!a || full) return;
    if (!x.recipients.includes(a)) setX({ ...x, recipients: [...x.recipients, a] });
    setAddr("");
  }
  function onKey(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Enter" || e.key === ",") { e.preventDefault(); addRecipient(); }
  }
  function toggleAccount(accountId: string) {
    const has = x.accountIds.includes(accountId);
    setX({ ...x, accountIds: has ? x.accountIds.filter((a) => a !== accountId) : [...x.accountIds, accountId] });
  }
  async function submit(e: FormEvent) {
    e.preventDefault();
    const pending = addr.trim().toLowerCase();
    const body = pending && !full && !x.recipients.includes(pending) ? { ...x, recipients: [...x.recipients, pending] } : x;
    setSaving(true); setError(null);
    try {
      if (id) await api.updateStatementSchedule(id, body);
      else await api.createStatementSchedule(body);
      await onSaved();
    } catch (err) {
      setError(err);
    } finally {
      setSaving(false);
    }
  }
  const title = id ? "Edit statement schedule" : "New statement schedule";
  return (
    <form className="card mb-3" onSubmit={(e) => void submit(e)} aria-label={title}>
      <div className="card-body">
        <h3 className="h6">{title}</h3>
        <div className="row g-3">
          <div className="col-md-3">
            <label className="form-label" htmlFor="stmt-period">Period</label>
            <select id="stmt-period" className="form-select" value={x.period} onChange={(e) => setX({ ...x, period: e.target.value as StatementPeriod })}>
              {(Object.keys(periodLabel) as StatementPeriod[]).map((p) => <option key={p} value={p}>{periodLabel[p]}</option>)}
            </select>
            <div className="form-text">Sent {periodWhen[x.period]}.</div>
          </div>
          <fieldset className="col-md-4">
            <legend className="form-label fs-6">Accounts</legend>
            <div className="form-text mt-0 mb-1">{x.accountIds.length === 0 ? "None ticked: all your accounts, also ones added later." : `${x.accountIds.length} chosen.`}</div>
            {accounts.map((a) => (
              <div className="form-check" key={a.id}>
                <input className="form-check-input" type="checkbox" id={`stmt-acct-${a.id}`} checked={x.accountIds.includes(a.id)} onChange={() => toggleAccount(a.id)} />
                <label className="form-check-label" htmlFor={`stmt-acct-${a.id}`}>{a.name} <span className="text-secondary small">({platformLabel[a.platform]}, {a.mode})</span></label>
              </div>
            ))}
          </fieldset>
          <div className="col-md-5">
            <label className="form-label" htmlFor="stmt-recipient">Recipients ({x.recipients.length}/{MAX_RECIPIENTS})</label>
            <div className="input-group">
              <input id="stmt-recipient" type="email" className="form-control" placeholder="name@example.com" value={addr} disabled={full}
                onChange={(e) => setAddr(e.target.value)} onKeyDown={onKey} autoComplete="email" />
              <button type="button" className="btn btn-outline-secondary" onClick={addRecipient} disabled={full || !addr.trim()} aria-label="Add recipient">
                <i className="bi bi-plus-lg" /></button>
            </div>
            <div className="d-flex flex-wrap gap-1 mt-2" aria-label="Recipient list">
              {x.recipients.map((r) => (
                <span key={r} className="badge text-bg-light border d-inline-flex align-items-center gap-1 ticker-chip">
                  {r}
                  <button type="button" className="btn-close" aria-label={`Remove ${r}`}
                    onClick={() => setX({ ...x, recipients: x.recipients.filter((y) => y !== r) })} />
                </span>
              ))}
            </div>
            <div className="form-text">Any address: the statements go to whoever you enter here.</div>
          </div>
          <fieldset className="col-md-7">
            <legend className="form-label fs-6">PDFs in each e-mail</legend>
            {([["includeCombined", "Combined: every account and exchange"], ["includeAccounts", "One per account"],
              ["includeExchanges", "One per exchange (with trades or positions)"]] as const).map(([k, label]) => (
              <div className="form-check form-check-inline" key={k}>
                <input className="form-check-input" type="checkbox" id={`stmt-${k}`} checked={x[k]} onChange={(e) => setX({ ...x, [k]: e.target.checked })} />
                <label className="form-check-label" htmlFor={`stmt-${k}`}>{label}</label>
              </div>
            ))}
          </fieldset>
          <div className="col-md-5">
            <div className="form-check form-switch mt-md-4">
              <input className="form-check-input" type="checkbox" role="switch" id="stmt-enabled" checked={x.enabled} onChange={(e) => setX({ ...x, enabled: e.target.checked })} />
              <label className="form-check-label" htmlFor="stmt-enabled">On</label>
            </div>
          </div>
        </div>
        <ProblemAlert error={error} className="mt-3 mb-0" />
        <div className="d-flex gap-2 mt-3">
          <button type="submit" className="btn btn-success" disabled={saving}>{saving ? "Saving…" : "Save"}</button>
          <button type="button" className="btn btn-outline-secondary" onClick={onCancel}>Cancel</button>
        </div>
      </div>
    </form>
  );
}

// PreviewForm downloads any one statement as a PDF (nothing is e-mailed).
function PreviewForm({ accounts }: { accounts: StatementAccountRef[] }) {
  // R50: the period, kind and exchange are the user's preferences (the day and account are not)
  const [period, setPeriod] = usePref<StatementPeriod>("statements.preview.period", "weekly", isPeriod);
  const [from, setFrom] = useState("");
  const [scope, setScope] = usePref<StatementScope>("statements.preview.scope", "combined", isScope);
  const [accountId, setAccountId] = useState("");
  const [exchange, setExchange] = usePref<string>("statements.preview.exchange", "LSE", isExchange);
  const acct = accountId || accounts[0]?.id || "";
  const href = statementPreviewUrl({
    period, from: from || undefined, scope,
    accountId: scope === "account" ? acct : undefined, exchange: scope === "exchange" ? exchange : undefined,
  });
  return (
    <div className="card flex-grow-1"><div className="card-body py-2 d-flex flex-wrap gap-2 align-items-end" role="group" aria-label="Preview a statement">
      <div>
        <label className="form-label small mb-0" htmlFor="pv-period">Period</label>
        <select id="pv-period" className="form-select form-select-sm" value={period} onChange={(e) => setPeriod(e.target.value as StatementPeriod)}>
          {(Object.keys(periodLabel) as StatementPeriod[]).map((p) => <option key={p} value={p}>{periodLabel[p]}</option>)}
        </select>
      </div>
      <div>
        <label className="form-label small mb-0" htmlFor="pv-from">Containing the day</label>
        <input id="pv-from" type="date" className="form-control form-control-sm" value={from} onChange={(e) => setFrom(e.target.value)} />
      </div>
      <div>
        <label className="form-label small mb-0" htmlFor="pv-scope">Statement</label>
        <select id="pv-scope" className="form-select form-select-sm" value={scope} onChange={(e) => setScope(e.target.value as StatementScope)}>
          <option value="combined">Combined</option>
          <option value="account" disabled={accounts.length === 0}>One account</option>
          <option value="exchange">One exchange</option>
        </select>
      </div>
      {scope === "account" && (
        <div>
          <label className="form-label small mb-0" htmlFor="pv-account">Account</label>
          <select id="pv-account" className="form-select form-select-sm" value={acct} onChange={(e) => setAccountId(e.target.value)}>
            {accounts.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
          </select>
        </div>
      )}
      {scope === "exchange" && (
        <div>
          <label className="form-label small mb-0" htmlFor="pv-exchange">Exchange</label>
          <select id="pv-exchange" className="form-select form-select-sm" value={exchange} onChange={(e) => setExchange(e.target.value)}>
            {exchanges.map((x) => <option key={x} value={x}>{x}</option>)}
          </select>
        </div>
      )}
      <a className="btn btn-sm btn-outline-primary" href={href} title="Download the PDF (not e-mailed)">
        <i className="bi bi-file-earmark-pdf me-1" />Preview PDF</a>
      <span className="small text-secondary">Without a day: the last complete period.</span>
    </div></div>
  );
}
