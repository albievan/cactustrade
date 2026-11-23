import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorText, usd, type Account, type AccountKeys, type AccountShare, type BrokerCheck } from "../lib/api";
import { useSession } from "../lib/session";
import { useRefreshMode } from "../lib/mode";

// Each user's eToro accounts and the ones shared with them.
export function Accounts() {
  const { can } = useSession();
  const refreshMode = useRefreshMode();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    api.accounts().then(setAccounts).catch((e) => setError(errorText(e)));
    void refreshMode();
  }, [refreshMode]);
  useEffect(load, [load]);
  return (
    <>
      <h1 className="h3">eToro accounts</h1>
      <p className="text-secondary">
        Each account is one eToro key pair, <strong>demo</strong> or <strong>real</strong> (eToro binds every key to one
        environment). Its owner can share it with other users to <em>view</em> it or to <em>trade</em> with it. The
        watchlist, risk limits, agent and events belong to the account. In eToro: Settings → Trading → API Key
        Management → Create New Key; the <em>Public API Key</em> is the API key, the <em>User Key</em> the user key.
      </p>
      {error && <div className="alert alert-danger" role="alert">{error}</div>}
      {can("agent.control") && <CreateAccount onCreated={load} />}
      {accounts.length === 0 && <p className="text-secondary">No accounts yet.</p>}
      {accounts.map((a) => <AccountCard key={a.id} a={a} onChange={load} />)}
    </>
  );
}

function CreateAccount({ onCreated }: { onCreated: () => void }) {
  const [name, setName] = useState("");
  const [mode, setMode] = useState<"demo" | "real">("demo");
  const [error, setError] = useState("");
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      await api.createAccount(name, mode);
      setName("");
      onCreated();
    } catch (err) {
      setError(errorText(err));
    }
  }
  return (
    <form className="card mb-4" onSubmit={submit}><div className="card-body">
      <div className="row g-2 align-items-end">
        <div className="col-md-5">
          <label className="form-label fw-semibold" htmlFor="acct-name">New account</label>
          <input id="acct-name" className="form-control" placeholder="e.g. My demo, Family ISA" maxLength={60} value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="col-md-3">
          <label className="form-label fw-semibold" htmlFor="acct-mode">Environment</label>
          <select id="acct-mode" className="form-select" value={mode} onChange={(e) => setMode(e.target.value as "demo" | "real")}>
            <option value="demo">Demo (practice money)</option>
            <option value="real">Real money</option>
          </select>
        </div>
        <div className="col-md-2"><button className="btn btn-success w-100">Add</button></div>
      </div>
      {mode === "real" && <div className="form-text text-danger">A real account can be added and tested now; the agent trades real money only after the gated switch (phase 7).</div>}
      {error && <div className="alert alert-danger mt-3 mb-0">{error}</div>}
    </div></form>
  );
}

function AccountCard({ a, onChange }: { a: Account; onChange: () => void }) {
  const owner = a.access === "owner";
  const [keys, setKeys] = useState<AccountKeys | null>(null);
  const [apiKey, setApiKey] = useState("");
  const [userKey, setUserKey] = useState("");
  const [check, setCheck] = useState<BrokerCheck | null>(null);
  const [shares, setShares] = useState<AccountShare[]>([]);
  const [shareUser, setShareUser] = useState("");
  const [shareAccess, setShareAccess] = useState<"view" | "trade">("view");
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const loadDetails = useCallback(() => {
    api.accountKeys(a.id).then(setKeys).catch(() => undefined);
    if (owner) api.accountShares(a.id).then(setShares).catch(() => undefined);
  }, [a.id, owner]);
  useEffect(loadDetails, [loadDetails]);
  async function run(f: () => Promise<void>) {
    setBusy(true); setError(""); setMsg("");
    try { await f(); } catch (e) { setError(errorText(e)); } finally { setBusy(false); }
  }
  return (
    <div className={"card mb-3 " + (a.mode === "real" ? "border-danger" : "")}>
      <div className="card-body">
        <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
          <h2 className="h5 mb-0 me-2">{a.name}</h2>
          <span className={"badge " + (a.mode === "real" ? "text-bg-danger" : "text-bg-info")}>{a.mode === "real" ? "REAL MONEY" : "demo"}</span>
          {a.envKeys && <span className="badge text-bg-secondary" title="Keys from ETORO_DEMO_PUB / ETORO_DEMO_PK">environment keys</span>}
          <span className="text-secondary small ms-auto">{owner ? "yours" : `${a.ownerName}'s · you can ${a.access}`}</span>
        </div>
        <div className="small mb-3">
          Keys: {keys ? (keys.apiKeySet && keys.userKeySet
            ? <span className="text-success"><i className="bi bi-check-circle me-1" />set{keys.fromEnvironment && " by the environment"}</span>
            : <span className="text-danger">not both set</span>) : "…"}
        </div>
        {msg && <div className="alert alert-success py-2">{msg}</div>}
        {error && <div className="alert alert-danger py-2">{error}</div>}
        {owner && (
          <>
            {!keys?.fromEnvironment && (
              <form className="row g-2 mb-3" onSubmit={(e) => { e.preventDefault(); void run(async () => {
                await api.setAccountKeys(a.id, apiKey, userKey); setApiKey(""); setUserKey(""); loadDetails(); setMsg("Keys stored (encrypted, write-only).");
              }); }}>
                <div className="col-md-5"><input className="form-control form-control-sm" type="password" autoComplete="off" placeholder={keys?.apiKeySet ? "API key — set, type to replace" : "Public API Key (x-api-key)"} value={apiKey} onChange={(e) => setApiKey(e.target.value)} aria-label="API key" /></div>
                <div className="col-md-5"><input className="form-control form-control-sm" type="password" autoComplete="off" placeholder={keys?.userKeySet ? "User key — set, type to replace" : "User Key (x-user-key)"} value={userKey} onChange={(e) => setUserKey(e.target.value)} aria-label="User key" /></div>
                <div className="col-md-2"><button className="btn btn-sm btn-outline-success w-100" disabled={busy || (!apiKey && !userKey)}>Save keys</button></div>
              </form>
            )}
            <div className="d-flex flex-wrap gap-2 mb-3">
              <button className="btn btn-sm btn-outline-success" disabled={busy} onClick={() => void run(async () => setCheck(await api.testAccount(a.id)))}><i className="bi bi-plug me-1" />Test connection</button>
              <button className="btn btn-sm btn-outline-secondary" disabled={busy} onClick={() => { const n = window.prompt("New name", a.name); if (n) void run(async () => { await api.renameAccount(a.id, n); onChange(); }); }}>Rename</button>
              <button className="btn btn-sm btn-outline-danger" disabled={busy} onClick={() => { if (window.confirm(`Delete ${a.name}, its keys, shares and watchlist?`)) void run(async () => { await api.deleteAccount(a.id); onChange(); }); }}>Delete</button>
            </div>
            {check && (check.ok
              ? <div className="alert alert-success py-2">Connected ({check.mode}, {check.latencyMs} ms): cash {usd(check.availableCashCents)}, equity {usd(check.equityCents)}, {check.positions} position(s).</div>
              : <div className="alert alert-warning py-2">{check.configuredKeys ? <>eToro refused or did not answer: {check.error}</> : "The key pair is not complete."}</div>)}
            <div className="fw-semibold small mb-1">Shared with</div>
            {shares.length === 0 && <div className="small text-secondary mb-2">nobody</div>}
            <ul className="list-unstyled small mb-2">
              {shares.map((s) => (
                <li key={s.userId} className="d-flex align-items-center gap-2 mb-1">
                  <i className="bi bi-person" />{s.username} · {s.access}
                  <button className="btn btn-link btn-sm text-danger p-0 ms-2" onClick={() => void run(async () => { await api.unshareAccount(a.id, s.userId); loadDetails(); })}>remove</button>
                </li>
              ))}
            </ul>
            <form className="row g-2" onSubmit={(e) => { e.preventDefault(); void run(async () => { await api.shareAccount(a.id, shareUser, shareAccess); setShareUser(""); loadDetails(); setMsg("Shared."); }); }}>
              <div className="col-md-5"><input className="form-control form-control-sm" placeholder="username" value={shareUser} onChange={(e) => setShareUser(e.target.value)} aria-label="Share with user" required /></div>
              <div className="col-md-4"><select className="form-select form-select-sm" value={shareAccess} onChange={(e) => setShareAccess(e.target.value as "view" | "trade")} aria-label="Access">
                <option value="view">can view</option><option value="trade">can trade</option></select></div>
              <div className="col-md-3"><button className="btn btn-sm btn-outline-secondary w-100" disabled={busy}>Share</button></div>
            </form>
          </>
        )}
      </div>
    </div>
  );
}
