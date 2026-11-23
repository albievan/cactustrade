import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorText, platformLabel, usd, type Account, type AccountKeys, type AccountShare, type BrokerCheck, type Platform } from "../lib/api";
import { useSession } from "../lib/session";
import { useRefreshMode } from "../lib/mode";

// Each user's trading platform accounts (eToro, and view-only Trading 212) and the ones
// shared with them, as a grid of cards.

const keyLabels: Record<Platform, { key: string; secret: string }> = {
  etoro: { key: "Public API key", secret: "User key" },
  trading212: { key: "API key", secret: "API secret" },
};

function KeyHelp({ platform }: { platform: Platform }) {
  return platform === "trading212" ? (
    <div className="form-text">
      Trading 212 app → Settings → API (Beta) → Generate API key; give it only read permissions (account, portfolio,
      history); the IP restriction is optional. Use a demo key for a demo account, a live key for a real one.
      View-only: cactusTrade does not trade Trading 212 accounts yet.
    </div>
  ) : (
    <div className="form-text">
      eToro: Settings → Trading → API Key Management → Create New Key. The <em>Public API Key</em> is the API key
      (x-api-key), the <em>User Key</em> the user key (x-user-key).
    </div>
  );
}

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
      <h1 className="h3">Trading Platform Accounts</h1>
      <p className="text-secondary">
        Each account is one key pair, <strong>demo</strong> or <strong>real</strong> (every key works for one environment).
        Its owner can share it with other users to <em>view</em> it or to <em>trade</em> with it. Keys are write-only:
        they are stored encrypted and never shown again.
      </p>
      <ul className="text-secondary small">
        <li><strong>eToro</strong> — Public API Key + User Key (eToro: Settings → Trading → API Key Management → Create New
          Key). The watchlist, risk limits, agent and events belong to the account.</li>
        <li><strong>Trading 212</strong> — API key + API secret (Trading 212 app → Settings → API (Beta) → Generate API key,
          with read-only permissions). View-only in cactusTrade for now: shown on the Overview page, never traded.</li>
      </ul>
      {error && <div className="alert alert-danger" role="alert">{error}</div>}
      {can("agent.control") && <CreateAccount onCreated={load} />}
      {accounts.length === 0 && <p className="text-secondary">No accounts yet.</p>}
      <div className="row row-cols-1 row-cols-md-2 row-cols-xxl-3 g-3">
        {accounts.map((a) => <AccountCard key={a.id} a={a} onChange={load} />)}
      </div>
    </>
  );
}

function CreateAccount({ onCreated }: { onCreated: () => void }) {
  const [platform, setPlatform] = useState<Platform>("etoro");
  const [name, setName] = useState("");
  const [mode, setMode] = useState<"demo" | "real">("demo");
  const [apiKey, setApiKey] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState("");
  const [msg, setMsg] = useState("");
  const labels = keyLabels[platform];
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(""); setMsg("");
    try {
      const a = await api.createAccount(name, mode, platform);
      setName("");
      onCreated();
      if (apiKey || secret) {
        try {
          await api.setAccountKeys(a.id, apiKey, secret);
        } catch (err) {
          setError(`${a.name} was added, but its keys were not stored: ${errorText(err)}`);
          return;
        }
      }
      setApiKey(""); setSecret("");
      setMsg(`${a.name} added.`);
      onCreated();
    } catch (err) {
      setError(errorText(err));
    }
  }
  return (
    <form className="card mb-4" onSubmit={submit}><div className="card-body">
      <h2 className="h6">New account</h2>
      <div className="row g-2 align-items-end">
        <div className="col-md-3">
          <label className="form-label fw-semibold" htmlFor="acct-platform">Platform</label>
          <select id="acct-platform" className="form-select" value={platform} onChange={(e) => setPlatform(e.target.value as Platform)}>
            <option value="etoro">eToro</option>
            <option value="trading212">Trading 212 (view-only)</option>
          </select>
        </div>
        <div className="col-md-5">
          <label className="form-label fw-semibold" htmlFor="acct-name">Name</label>
          <input id="acct-name" className="form-control" placeholder="e.g. My demo, Family ISA" maxLength={60} value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="col-md-4">
          <label className="form-label fw-semibold" htmlFor="acct-mode">Environment</label>
          <select id="acct-mode" className="form-select" value={mode} onChange={(e) => setMode(e.target.value as "demo" | "real")}>
            <option value="demo">Demo (practice money)</option>
            <option value="real">Real money</option>
          </select>
        </div>
        <div className="col-md-5">
          <label className="form-label fw-semibold" htmlFor="acct-key">{labels.key} <span className="fw-normal text-secondary">(optional)</span></label>
          <input id="acct-key" className="form-control" type="password" autoComplete="off" value={apiKey} onChange={(e) => setApiKey(e.target.value)} />
        </div>
        <div className="col-md-5">
          <label className="form-label fw-semibold" htmlFor="acct-secret">{labels.secret} <span className="fw-normal text-secondary">(optional)</span></label>
          <input id="acct-secret" className="form-control" type="password" autoComplete="off" value={secret} onChange={(e) => setSecret(e.target.value)} />
        </div>
        <div className="col-md-2"><button className="btn btn-success w-100">Add</button></div>
      </div>
      <KeyHelp platform={platform} />
      <div className="form-text">Keys can also be set later; they are write-only.</div>
      {platform === "etoro" && mode === "real" && <div className="form-text text-danger">A real account can be added and tested now; the agent trades real money only after the gated switch (phase 7).</div>}
      {msg && <div className="alert alert-success mt-3 mb-0 py-2">{msg}</div>}
      {error && <div className="alert alert-danger mt-3 mb-0">{error}</div>}
    </div></form>
  );
}

type Panel = "" | "keys" | "share" | "rename";

function AccountCard({ a, onChange }: { a: Account; onChange: () => void }) {
  const owner = a.access === "owner";
  const t212 = a.platform === "trading212";
  const labels = keyLabels[a.platform];
  const [keys, setKeys] = useState<AccountKeys | null>(null);
  const [apiKey, setApiKey] = useState("");
  const [userKey, setUserKey] = useState("");
  const [newName, setNewName] = useState(a.name);
  const [check, setCheck] = useState<BrokerCheck | null>(null);
  const [shares, setShares] = useState<AccountShare[]>([]);
  const [shareUser, setShareUser] = useState("");
  const [shareAccess, setShareAccess] = useState<"view" | "trade">("view");
  const [panel, setPanel] = useState<Panel>("");
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
  const toggle = (p: Panel) => setPanel(panel === p ? "" : p);
  return (
    <div className="col">
      <div className={"card h-100 " + (a.mode === "real" ? "border-danger" : "")}><div className="card-body d-flex gap-3">
        {owner && (
          <div className="btn-group-vertical btn-group-sm align-self-start" role="group" aria-label={`Actions for ${a.name}`}>
            <button className="btn btn-outline-success" disabled={busy || t212}
              title={t212 ? "Test connection (not for view-only Trading 212 accounts; see the Overview)" : "Test connection"}
              aria-label={`Test connection of ${a.name}`} onClick={() => void run(async () => setCheck(await api.testAccount(a.id)))}>
              <i className="bi bi-plug" /></button>
            <button className={"btn btn-outline-secondary" + (panel === "rename" ? " active" : "")} disabled={busy} title="Rename"
              aria-label={`Rename ${a.name}`} aria-pressed={panel === "rename"} onClick={() => { setNewName(a.name); toggle("rename"); }}>
              <i className="bi bi-pencil" /></button>
            <button className={"btn btn-outline-secondary" + (panel === "keys" ? " active" : "")} disabled={busy || !!keys?.fromEnvironment}
              title={keys?.fromEnvironment ? "Keys come from the environment" : "Keys"} aria-label={`Keys of ${a.name}`}
              aria-pressed={panel === "keys"} onClick={() => toggle("keys")}>
              <i className="bi bi-key" /></button>
            <button className={"btn btn-outline-secondary" + (panel === "share" ? " active" : "")} disabled={busy} title="Share"
              aria-label={`Share ${a.name}`} aria-pressed={panel === "share"} onClick={() => toggle("share")}>
              <i className="bi bi-people" /></button>
            <button className="btn btn-outline-danger" disabled={busy} title="Delete" aria-label={`Delete ${a.name}`}
              onClick={() => { if (window.confirm(`Delete ${a.name}, its keys, shares${t212 ? "" : " and watchlist"}?`)) void run(async () => { await api.deleteAccount(a.id); onChange(); }); }}>
              <i className="bi bi-trash" /></button>
          </div>
        )}
        <div className="flex-grow-1 min-w-0">
          <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
            <strong className="text-break me-1">{a.name}</strong>
            <span className={"badge " + (t212 ? "text-bg-primary" : "text-bg-success")}>{platformLabel[a.platform]}</span>
            <span className={"badge " + (a.mode === "real" ? "text-bg-danger" : "text-bg-info")}>{a.mode === "real" ? "REAL MONEY" : "demo"}</span>
            {a.envKeys && <span className="badge text-bg-secondary" title="Keys from ETORO_DEMO_PUB / ETORO_DEMO_PK">environment keys</span>}
          </div>
          <div className="small text-secondary mb-1">{owner ? "yours" : `${a.ownerName}'s · you can ${t212 ? "view" : a.access}`}</div>
          <div className="small mb-1">
            Keys: {keys ? (keys.apiKeySet && keys.userKeySet
              ? <span className="text-success"><i className="bi bi-check-circle me-1" />set{keys.fromEnvironment && " by the environment"}</span>
              : <span className="text-danger">not both set</span>) : "…"}
          </div>
          {owner && (
            <div className="small mb-2">
              Shared with: {shares.length === 0 ? <span className="text-secondary">nobody</span> : shares.map((s) => `${s.username} (${s.access})`).join(", ")}
            </div>
          )}
          {t212 && <div className="small text-secondary mb-2">View-only: cactusTrade does not trade Trading 212 accounts yet.</div>}
          {msg && <div className="alert alert-success py-2 small">{msg}</div>}
          {error && <div className="alert alert-danger py-2 small">{error}</div>}
          {check && (check.ok
            ? <div className="alert alert-success py-2 small">Connected ({check.mode}, {check.latencyMs} ms): cash {usd(check.availableCashCents)}, equity {usd(check.equityCents)}, {check.positions} position(s).</div>
            : <div className="alert alert-warning py-2 small">{check.configuredKeys ? <>eToro refused or did not answer: {check.error}</> : "The key pair is not complete."}</div>)}

          {owner && panel === "rename" && (
            <form className="d-flex gap-2 mb-2" onSubmit={(e) => { e.preventDefault(); void run(async () => { await api.renameAccount(a.id, newName); setPanel(""); onChange(); }); }}>
              <input className="form-control form-control-sm" maxLength={60} value={newName} onChange={(e) => setNewName(e.target.value)} aria-label="New name" required />
              <button className="btn btn-sm btn-outline-success" disabled={busy}>Save</button>
            </form>
          )}
          {owner && panel === "keys" && !keys?.fromEnvironment && (
            <form className="mb-2" onSubmit={(e) => { e.preventDefault(); void run(async () => {
              await api.setAccountKeys(a.id, apiKey, userKey); setApiKey(""); setUserKey(""); loadDetails(); setPanel(""); setMsg("Keys stored (encrypted, write-only).");
            }); }}>
              <input className="form-control form-control-sm mb-2" type="password" autoComplete="off" value={apiKey} onChange={(e) => setApiKey(e.target.value)}
                placeholder={keys?.apiKeySet ? `${labels.key} — set, type to replace` : labels.key} aria-label={labels.key} />
              <input className="form-control form-control-sm mb-2" type="password" autoComplete="off" value={userKey} onChange={(e) => setUserKey(e.target.value)}
                placeholder={keys?.userKeySet ? `${labels.secret} — set, type to replace` : labels.secret} aria-label={labels.secret} />
              <button className="btn btn-sm btn-outline-success" disabled={busy || (!apiKey && !userKey)}>Save keys</button>
              <KeyHelp platform={a.platform} />
            </form>
          )}
          {owner && panel === "share" && (
            <div className="mb-2">
              <ul className="list-unstyled small mb-2">
                {shares.map((s) => (
                  <li key={s.userId} className="d-flex align-items-center gap-2 mb-1">
                    <i className="bi bi-person" />{s.username} · {s.access}
                    <button className="btn btn-link btn-sm text-danger p-0 ms-2" onClick={() => void run(async () => { await api.unshareAccount(a.id, s.userId); loadDetails(); })}>remove</button>
                  </li>
                ))}
              </ul>
              <form className="d-flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); void run(async () => { await api.shareAccount(a.id, shareUser, shareAccess); setShareUser(""); loadDetails(); setMsg("Shared."); }); }}>
                <input className="form-control form-control-sm w-auto flex-grow-1" placeholder="username" value={shareUser} onChange={(e) => setShareUser(e.target.value)} aria-label="Share with user" required />
                <select className="form-select form-select-sm w-auto" value={shareAccess} onChange={(e) => setShareAccess(e.target.value as "view" | "trade")} aria-label="Access">
                  <option value="view">can view</option>{!t212 && <option value="trade">can trade</option>}</select>
                <button className="btn btn-sm btn-outline-secondary" disabled={busy}>Share</button>
              </form>
            </div>
          )}
        </div>
      </div></div>
    </div>
  );
}
