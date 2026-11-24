import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import {
  api, errorText, platformBadge, platformLabel, usd, viewOnly,
  type Account, type AccountKeys, type AccountShare, type BrokerCheck, type CredentialSpec, type Platform, type PlatformInfo,
} from "../lib/api";
import { useSession } from "../lib/session";
import { useRefreshMode } from "../lib/mode";
import { ProblemAlert } from "../components/ProblemAlert";
import { ConfirmDialog } from "../components/ConfirmDialog";

// Each user's trading platform accounts (eToro, and the view-only Trading 212, Saxo and Tiger
// Brokers) and the ones shared with them, as a grid of cards. Every credential field comes
// from the platform's credential list (GET /api/v1/platforms); values are write-only.

type Specs = Partial<Record<Platform, PlatformInfo>>;

// typed: the credentials the owner types in (not the ones a sign-in sets).
const typed = (p?: PlatformInfo): CredentialSpec[] => (p?.credentials ?? []).filter((c) => !c.setByFlow);

function PlatformHelp({ platform, redirectUri }: { platform: Platform; redirectUri?: string }) {
  switch (platform) {
    case "trading212":
      return (
        <div className="form-text">
          Trading 212 app → Settings → API (Beta) → Generate API key; give it only read permissions (account, portfolio,
          history); the IP restriction is optional. Use a demo key for a demo account, a live key for a real one.
          View-only: cactusTrade does not trade Trading 212 accounts.
        </div>
      );
    case "saxo":
      return (
        <div className="form-text">
          developer.saxo → Application management → create a <em>Code Flow</em> app (a SIM app for a demo account, a Live
          app for a real one; Live apps need Saxo&apos;s approval) and register this redirect address:{" "}
          {redirectUri ? <code>{redirectUri}</code> : <em>set CACTUSTRADE_PUBLIC_URL first; the address is that URL + /api/v1/oauth/saxo/callback</em>}.
          Enter the app key and secret, then press <strong>Connect to Saxo</strong> and sign in at Saxo. For a demo (SIM)
          account you can instead paste a 24-hour developer token. View-only: cactusTrade does not trade Saxo accounts.
        </div>
      );
    case "tiger":
      return (
        <div className="form-text">
          Tiger developer console (Open API): note your <em>tiger_id</em> and account number, generate an RSA key pair,
          upload the public key in the console and paste the private key here. Tiger paper accounts have no Australian
          market. View-only: cactusTrade does not trade Tiger accounts.
        </div>
      );
  }
  return (
    <div className="form-text">
      eToro: Settings → Trading → API Key Management → Create New Key. The <em>Public API Key</em> is the API key
      (x-api-key), the <em>User Key</em> the user key (x-user-key).
    </div>
  );
}

// CredentialFields renders one input per typed credential: password inputs, a textarea for a
// multi-line key (PEM). Nothing typed is ever shown back.
function CredentialFields({ specs, values, onChange, idPrefix, setKeys, small, optionalLabels }: {
  specs: CredentialSpec[]; values: Record<string, string>; onChange: (key: string, v: string) => void; idPrefix: string;
  setKeys?: Set<string>; small?: boolean; optionalLabels?: boolean;
}) {
  return (
    <>
      {specs.map((c) => {
        const id = `${idPrefix}-${c.key}`;
        const label = c.label + (optionalLabels || c.optional ? " (optional)" : "");
        const placeholder = setKeys?.has(c.key) ? "set — type to replace" : "";
        const cls = "form-control" + (small ? " form-control-sm" : "");
        return (
          <div className={small ? "mb-2" : "col-md-6"} key={c.key}>
            <label className={"form-label " + (small ? "small mb-1" : "fw-semibold")} htmlFor={id}>{label}</label>
            {c.multiline
              ? <textarea id={id} className={cls + " mono"} rows={4} autoComplete="off" spellCheck={false} value={values[c.key] ?? ""}
                placeholder={placeholder} onChange={(e) => onChange(c.key, e.target.value)} />
              : <input id={id} className={cls} type="password" autoComplete="off" value={values[c.key] ?? ""} placeholder={placeholder}
                onChange={(e) => onChange(c.key, e.target.value)} />}
            <div className="form-text">{c.help}</div>
          </div>
        );
      })}
    </>
  );
}

function filled(values: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(values).filter(([, v]) => v !== ""));
}

export function Accounts() {
  const { can } = useSession();
  const refreshMode = useRefreshMode();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [specs, setSpecs] = useState<Specs>({});
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState<{ ok: boolean; text: string } | null>(null);
  // ?keys=<id>: a fix button elsewhere (R22) asks to open that account's keys
  const [openKeys] = useState(() => new URLSearchParams(window.location.search).get("keys") ?? "");
  const load = useCallback(() => {
    api.accounts().then(setAccounts).catch(setError);
    void refreshMode();
  }, [refreshMode]);
  useEffect(load, [load]);
  useEffect(() => {
    api.platforms().then((ps) => setSpecs(Object.fromEntries(ps.map((p) => [p.platform, p])))).catch(setError);
  }, []);
  useEffect(() => {
    // the Saxo sign-in comes back here with ?connected=<id> or ?connect_error=<reason>
    const q = new URLSearchParams(window.location.search);
    const reasons: Record<string, string> = {
      expired: "The Saxo sign-in expired or was already used: press Connect to Saxo again.",
      refused: "Saxo refused the sign-in (or it was cancelled). Check the app key, secret and redirect address, then connect again.",
      failed: "Connecting to Saxo failed. Try again in a minute.",
    };
    if (q.has("connected")) setNotice({ ok: true, text: "Connected to Saxo. The account appears on the Overview page." });
    else if (q.has("connect_error")) setNotice({ ok: false, text: reasons[q.get("connect_error") ?? ""] ?? reasons.failed });
    if (q.has("connected") || q.has("connect_error") || q.has("keys")) window.history.replaceState(null, "", window.location.pathname);
  }, []);
  return (
    <>
      <h1 className="h3">Trading Platform Accounts</h1>
      <p className="text-secondary">
        Each account is one set of keys, <strong>demo</strong> or <strong>real</strong> (every key works for one environment).
        Its owner can share it with other users to <em>view</em> it or to <em>trade</em> with it. Keys are write-only:
        they are stored encrypted and never shown again.
      </p>
      <ul className="text-secondary small">
        <li><strong>eToro</strong> — Public API Key + User Key (eToro: Settings → Trading → API Key Management → Create New
          Key). The watchlist, risk limits, agent and events belong to the account. The only platform cactusTrade trades.</li>
        <li><strong>Trading 212</strong> — API key + API secret (Trading 212 app → Settings → API (Beta) → Generate API key,
          with read-only permissions). View-only: shown on the Overview page, never traded.</li>
        <li><strong>Saxo</strong> — app key + app secret of a Code Flow app, then <em>Connect to Saxo</em> (or a 24-hour
          developer token for SIM). View-only.</li>
        <li><strong>Tiger Brokers</strong> — Tiger ID, account number and an RSA private key whose public key is uploaded in
          the Tiger developer console. View-only; Tiger paper accounts have no Australian market.</li>
      </ul>
      <ProblemAlert error={error} />
      {notice && <div className={"alert " + (notice.ok ? "alert-success" : "alert-warning")} role="status">{notice.text}</div>}
      {can("agent.control") && <CreateAccount specs={specs} onCreated={load} />}
      {accounts.length === 0 && <p className="text-secondary">No accounts yet.</p>}
      <div className="row row-cols-1 row-cols-md-2 row-cols-xxl-3 g-3">
        {accounts.map((a) => <AccountCard key={a.id} a={a} spec={specs[a.platform]} onChange={load} openKeys={a.id === openKeys} />)}
      </div>
    </>
  );
}

function CreateAccount({ specs, onCreated }: { specs: Specs; onCreated: () => void }) {
  const [platform, setPlatform] = useState<Platform>("etoro");
  const [name, setName] = useState("");
  const [mode, setMode] = useState<"demo" | "real">("demo");
  const [values, setValues] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [msg, setMsg] = useState("");
  const fields = typed(specs[platform]);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(""); setMsg("");
    try {
      const a = await api.createAccount(name, mode, platform);
      setName("");
      onCreated();
      const v = filled(values);
      if (Object.keys(v).length > 0) {
        try {
          await api.setAccountCredentials(a.id, v);
        } catch (err) {
          setError(`${a.name} was added, but its keys were not stored: ${errorText(err)}`);
          return;
        }
      }
      setValues({});
      setMsg(`${a.name} added.` + (platform === "saxo" ? " Now press Connect to Saxo on its card." : ""));
      onCreated();
    } catch (err) {
      setError(errorText(err));
    }
  }
  const choices: Platform[] = ["etoro", "trading212", "saxo", "tiger"];
  return (
    <form className="card mb-4" onSubmit={submit}><div className="card-body">
      <h2 className="h6">New account</h2>
      <div className="row g-2 align-items-end">
        <div className="col-md-3">
          <label className="form-label fw-semibold" htmlFor="acct-platform">Platform</label>
          <select id="acct-platform" className="form-select" value={platform} onChange={(e) => { setPlatform(e.target.value as Platform); setValues({}); }}>
            {choices.map((p) => <option key={p} value={p}>{platformLabel[p]}{viewOnly(p) ? " (view-only)" : ""}</option>)}
          </select>
        </div>
        <div className="col-md-5">
          <label className="form-label fw-semibold" htmlFor="acct-name">Name</label>
          <input id="acct-name" className="form-control" placeholder="e.g. My demo, Family ISA" maxLength={60} value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="col-md-4">
          <label className="form-label fw-semibold" htmlFor="acct-mode">Environment</label>
          <select id="acct-mode" className="form-select" value={mode} onChange={(e) => setMode(e.target.value as "demo" | "real")}>
            <option value="demo">{platform === "saxo" ? "Demo (Saxo SIM)" : platform === "tiger" ? "Demo (paper account)" : "Demo (practice money)"}</option>
            <option value="real">Real money</option>
          </select>
        </div>
      </div>
      <div className="row g-2 mt-1">
        <CredentialFields specs={fields} values={values} onChange={(k, v) => setValues({ ...values, [k]: v })} idPrefix="acct-new" optionalLabels />
      </div>
      <div className="mt-2"><button className="btn btn-success">Add</button></div>
      <PlatformHelp platform={platform} />
      <div className="form-text">Keys can also be set later; they are write-only.</div>
      {platform === "etoro" && mode === "real" && <div className="form-text text-danger">A real account can be added and tested now; the agent trades real money only after the gated switch (phase 7).</div>}
      {msg && <div className="alert alert-success mt-3 mb-0 py-2">{msg}</div>}
      {error && <div className="alert alert-danger mt-3 mb-0">{error}</div>}
    </div></form>
  );
}

type Panel = "" | "keys" | "share" | "rename";

// age says how long ago an ISO time was, e.g. "3 h ago".
function age(iso: string): string {
  const h = Math.floor((Date.now() - new Date(iso).getTime()) / 3_600_000);
  if (h < 1) return "less than an hour ago";
  return h < 48 ? `${h} h ago` : `${Math.floor(h / 24)} days ago`;
}

function KeyStatus({ keys, platform }: { keys: AccountKeys | null; platform: Platform }) {
  if (!keys) return <>…</>;
  const dev = keys.credentials.find((c) => c.key === "dev_token" && c.set);
  const refresh = keys.credentials.find((c) => c.key === "refresh_token");
  return (
    <>
      {keys.complete
        ? <span className="text-success"><i className="bi bi-check-circle me-1" />set{keys.fromEnvironment && " by the environment"}</span>
        : <span className="text-danger">{platform === "saxo" ? "not connected yet" : "not all set"}</span>}
      {platform === "saxo" && refresh?.set && <span className="text-secondary"> · connected{refresh.updatedAt ? `, renewed ${age(refresh.updatedAt)}` : ""}</span>}
      {dev?.updatedAt && (
        <span className="text-secondary"> · 24-hour token stored {age(dev.updatedAt)}
          {Date.now() - new Date(dev.updatedAt).getTime() >= 24 * 3_600_000 && <span className="text-danger"> (expired: paste a new one)</span>}
        </span>
      )}
    </>
  );
}

function AccountCard({ a, spec, onChange, openKeys }: { a: Account; spec?: PlatformInfo; onChange: () => void; openKeys?: boolean }) {
  const owner = a.access === "owner";
  const vo = viewOnly(a.platform);
  const saxo = a.platform === "saxo";
  const [keys, setKeys] = useState<AccountKeys | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const [newName, setNewName] = useState(a.name);
  const [check, setCheck] = useState<BrokerCheck | null>(null);
  const [shares, setShares] = useState<AccountShare[]>([]);
  const [shareUser, setShareUser] = useState("");
  const [shareAccess, setShareAccess] = useState<"view" | "trade">("view");
  const [panel, setPanel] = useState<Panel>(openKeys && a.access === "owner" ? "keys" : "");
  const [msg, setMsg] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const cardRef = useRef<HTMLDivElement>(null);
  useEffect(() => { if (openKeys) cardRef.current?.scrollIntoView?.({ block: "center" }); }, [openKeys]);
  const loadDetails = useCallback(() => {
    api.accountKeys(a.id).then(setKeys).catch(() => undefined);
    if (owner) api.accountShares(a.id).then(setShares).catch(() => undefined);
  }, [a.id, owner]);
  useEffect(loadDetails, [loadDetails]);
  async function run(f: () => Promise<void>) {
    setBusy(true); setError(null); setMsg("");
    try { await f(); } catch (e) { setError(e); } finally { setBusy(false); }
  }
  const toggle = (p: Panel) => setPanel(panel === p ? "" : p);
  const setKeySet = new Set((keys?.credentials ?? []).filter((c) => c.set).map((c) => c.key));
  const connect = () => void run(async () => {
    const cs = await api.connectSaxo(a.id);
    window.location.assign(cs.authorizeUrl);
  });
  return (
    <div className="col" ref={cardRef}>
      <div className={"card h-100 " + (a.mode === "real" ? "border-danger" : "")}><div className="card-body d-flex gap-3">
        {owner && (
          <div className="btn-group-vertical btn-group-sm align-self-start" role="group" aria-label={`Actions for ${a.name}`}>
            <button className="btn btn-outline-success" disabled={busy || vo}
              title={vo ? `Test connection (not for view-only ${platformLabel[a.platform]} accounts; see the Overview)` : "Test connection"}
              aria-label={`Test connection of ${a.name}`} onClick={() => void run(async () => setCheck(await api.testAccount(a.id)))}>
              <i className="bi bi-plug" /></button>
            {saxo && (
              <button className="btn btn-outline-primary" disabled={busy} title="Connect to Saxo (sign in at Saxo)"
                aria-label={`Connect ${a.name} to Saxo`} onClick={connect}>
                <i className="bi bi-box-arrow-in-right" /></button>
            )}
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
              onClick={() => setDeleting(true)}>
              <i className="bi bi-trash" /></button>
          </div>
        )}
        <div className="flex-grow-1 min-w-0">
          <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
            <strong className="text-break me-1">{a.name}</strong>
            <span className={"badge " + platformBadge[a.platform]}>{platformLabel[a.platform]}</span>
            <span className={"badge " + (a.mode === "real" ? "text-bg-danger" : "text-bg-info")}>{a.mode === "real" ? "REAL MONEY" : "demo"}</span>
            {a.envKeys && <span className="badge text-bg-secondary" title="Keys from ETORO_DEMO_PUB / ETORO_DEMO_PK">environment keys</span>}
          </div>
          <div className="small text-secondary mb-1">{owner ? "yours" : `${a.ownerName}'s · you can ${vo ? "view" : a.access}`}</div>
          <div className="small mb-1">Keys: <KeyStatus keys={keys} platform={a.platform} /></div>
          {owner && (
            <div className="small mb-2">
              Shared with: {shares.length === 0 ? <span className="text-secondary">nobody</span> : shares.map((s) => `${s.username} (${s.access})`).join(", ")}
            </div>
          )}
          {vo && <div className="small text-secondary mb-2">View-only: cactusTrade does not trade {platformLabel[a.platform]} accounts.</div>}
          {owner && saxo && (
            <div className="small mb-2">
              <button className="btn btn-sm btn-primary me-2" disabled={busy} onClick={connect}>Connect to Saxo</button>
              {keys?.connectRedirectUri
                ? <>Redirect address to register: <code className="text-break">{keys.connectRedirectUri}</code></>
                : <span className="text-secondary">Set CACTUSTRADE_PUBLIC_URL to connect (the redirect address is that URL + /api/v1/oauth/saxo/callback).</span>}
            </div>
          )}
          {msg && <div className="alert alert-success py-2 small">{msg}</div>}
          <ProblemAlert error={error} accountId={a.id} fixes={owner ? { account_keys: () => setPanel("keys") } : undefined} className="py-2 small" />
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
              await api.setAccountCredentials(a.id, filled(values)); setValues({}); loadDetails(); setPanel(""); setMsg("Keys stored (encrypted, write-only).");
            }); }}>
              <CredentialFields specs={typed(spec)} values={values} onChange={(k, v) => setValues({ ...values, [k]: v })} idPrefix={`acct-${a.id}`} setKeys={setKeySet} small />
              <button className="btn btn-sm btn-outline-success" disabled={busy || Object.keys(filled(values)).length === 0}>Save keys</button>
              <PlatformHelp platform={a.platform} redirectUri={keys?.connectRedirectUri} />
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
                  <option value="view">can view</option>{!vo && <option value="trade">can trade</option>}</select>
                <button className="btn btn-sm btn-outline-secondary" disabled={busy}>Share</button>
              </form>
            </div>
          )}
        </div>
      </div></div>
      {deleting && (
        <ConfirmDialog title={`Delete ${a.name}?`} confirmLabel="Delete" busyLabel="Deleting…" icon="trash" busy={busy}
          onCancel={() => setDeleting(false)}
          onConfirm={async () => { await run(async () => { await api.deleteAccount(a.id); onChange(); }); setDeleting(false); }}>
          <p className="mb-0">Its keys, shares{vo ? "" : " and watchlist"} are deleted with it. This cannot be undone.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}
