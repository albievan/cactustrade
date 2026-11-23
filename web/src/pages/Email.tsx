import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { api, errorText, type EmailSetup, type EmailSetupInput, type OutboxMail } from "../lib/api";
import { useSession } from "../lib/session";

const P = ({ children }: { children: ReactNode }) => <span className="fw-semibold">{children}</span>;

// Step by step, per provider: what to do there, then here. Help text only — the server
// tests and stores whatever is entered.
const guide: Record<string, ReactNode[]> = {
  gmail: [
    <>Turn on 2-Step Verification for the Google account: <P>Google Account → Security → 2-Step Verification</P>. Google offers app passwords only to accounts that have it.</>,
    <>Create an app password: <P>Google Account → Security → 2-Step Verification → App passwords</P>. Call it “cactusTrade” and copy the 16-letter password Google shows once.</>,
    <>Here, choose <P>Gmail</P>: host <P>smtp.gmail.com</P>, port <P>587</P> and <P>STARTTLS</P> are filled in.</>,
    <>User name: your full Gmail address. Password: the app password without spaces — not your normal password.</>,
    <>Send as: your Gmail address. To send as another address, first add and verify it in Gmail under <P>Settings → Accounts → Send mail as</P>; otherwise Gmail sends as your Gmail address (the page warns you).</>,
    <>Tick <P>Use this server for all e-mail</P>, press <P>Save</P>, then <P>Send test e-mail</P> and check the inbox.</>,
  ],
  microsoft: [
    <>The mailbox must be allowed to send through SMTP. An administrator turns it on in the <P>Microsoft 365 admin center → Users → Active users → the user → Mail → Manage email apps → Authenticated SMTP</P>.</>,
    <>Here, choose <P>Outlook / Microsoft 365</P>: host <P>smtp.office365.com</P>, port <P>587</P> and <P>STARTTLS</P> are filled in.</>,
    <>User name: the mailbox's full address. Password: its password (or an app password if your organisation uses them).</>,
    <>Send as: the mailbox address, or one it has “Send As” permission for; otherwise Microsoft 365 may refuse (the page warns you).</>,
    <>Tick <P>Use this server for all e-mail</P>, press <P>Save</P>, then <P>Send test e-mail</P>.</>,
  ],
  custom: [
    <>Look up your provider's SMTP settings (“outgoing mail server”): host, port and security.</>,
    <>Enter them. Port <P>587</P> goes with <P>STARTTLS</P>, port <P>465</P> with <P>TLS</P>. <P>None</P> only for a test server on this machine: it sends everything unencrypted.</>,
    <>User name and password as the provider gives them — often the address and its password; some services use a fixed user name and an API key.</>,
    <>Send as: an address on a domain the provider lets you send from (with the SPF and DKIM records it asks for, so mail is not marked as spam).</>,
    <>Tick <P>Use this server for all e-mail</P>, press <P>Save</P>, then <P>Send test e-mail</P>.</>,
  ],
};

export function Email() {
  const { me } = useSession();
  const session = me?.via === "session";
  const [setup, setSetup] = useState<EmailSetup | null>(null);
  const [form, setForm] = useState<EmailSetupInput | null>(null);
  const [preset, setPreset] = useState("gmail");
  const [password, setPassword] = useState("");
  const [to, setTo] = useState("");
  const [outbox, setOutbox] = useState<OutboxMail[]>([]);
  const [showGuide, setShowGuide] = useState(false);
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const apply = (s: EmailSetup) => {
    setSetup(s);
    setForm({ enabled: s.enabled, host: s.host, port: s.port, username: s.username, from: s.from, tls: s.tls as EmailSetupInput["tls"] });
    setPreset(s.presets.find((p) => p.host !== "" && p.host === s.host)?.id ?? "custom");
  };
  const loadOutbox = () => api.outbox().then(setOutbox).catch(() => undefined);
  useEffect(() => {
    api.emailSetup().then(apply).catch((e) => setError(errorText(e)));
    void loadOutbox();
    api.myProfile().then((p) => setTo(p.email)).catch(() => undefined);
  }, []);
  async function run(label: string, f: () => Promise<void>) {
    setBusy(label); setError(""); setMsg("");
    try { await f(); } catch (e) { setError(errorText(e)); } finally { setBusy(""); }
  }
  if (!setup || !form) return <><h1 className="h3">E-mail</h1>{error && <div className="alert alert-danger">{error}</div>}</>;
  const set = (patch: Partial<EmailSetupInput>) => setForm({ ...form, ...patch });
  const choosePreset = (id: string) => {
    setPreset(id);
    const p = setup.presets.find((x) => x.id === id);
    if (p && id !== "custom") set({ host: p.host, port: p.port, tls: p.tls as EmailSetupInput["tls"] });
  };
  const save = (e: FormEvent) => {
    e.preventDefault();
    void run("save", async () => {
      apply(await api.setEmailSetup({ ...form, ...(password ? { password } : {}) }));
      setPassword("");
      setMsg("Saved and audited.");
    });
  };
  return (
    <>
      <h1 className="h3">E-mail</h1>
      <p className="text-secondary">
        cactusTrade e-mails trades, watchlist changes and alerts to the users who ask for them (each user chooses under
        <em> Your account</em>), and password-reset links. Mail waits in the outbox and is retried until the server accepts it;
        a message the server accepted is never sent twice.
      </p>
      {msg && <div className="alert alert-success" role="status">{msg}</div>}
      {error && <div className="alert alert-danger" role="alert">{error}</div>}
      {!form.enabled && (
        <div className="alert alert-light border small">
          {setup.fallback
            ? <>This server is off: mail goes through the deployment's server <code>{setup.fallback}</code> (on Docker the test mailbox, Mailpit).</>
            : <>No server is on: mail waits in the outbox until one is set up here.</>}
        </div>
      )}
      <form className="card mb-4" onSubmit={save}><div className="card-body row g-3">
        <div className="col-md-4">
          <label className="form-label fw-semibold" htmlFor="preset">Provider</label>
          <select id="preset" className="form-select" value={preset} onChange={(e) => choosePreset(e.target.value)}>
            {setup.presets.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
          </select>
          <button type="button" className="btn btn-link btn-sm px-0" onClick={() => setShowGuide(!showGuide)}>
            <i className="bi bi-question-circle me-1" />{showGuide ? "Hide the setup guide" : "Setup guide"}</button>
        </div>
        <div className="col-md-4">
          <label className="form-label fw-semibold" htmlFor="host">Host</label>
          <input id="host" className="form-control" value={form.host} onChange={(e) => { set({ host: e.target.value }); setPreset("custom"); }} placeholder="smtp.example.com" />
        </div>
        <div className="col-md-2">
          <label className="form-label fw-semibold" htmlFor="port">Port</label>
          <input id="port" type="number" min={0} max={65535} className="form-control" value={form.port} onChange={(e) => set({ port: Number(e.target.value) })} />
        </div>
        <div className="col-md-2">
          <label className="form-label fw-semibold" htmlFor="tls">Security</label>
          <select id="tls" className="form-select" value={form.tls} onChange={(e) => set({ tls: e.target.value as EmailSetupInput["tls"] })}>
            <option value="starttls">STARTTLS</option>
            <option value="tls">SSL/TLS</option>
            <option value="none">None</option>
          </select>
        </div>
        {showGuide && (
          <div className="col-12"><div className="card bg-body-tertiary"><div className="card-body">
            <h2 className="h6">{setup.presets.find((p) => p.id === preset)?.label}: step by step</h2>
            <ol className="mb-0 small">{(guide[preset] ?? guide.custom).map((s, i) => <li key={i} className="mb-1">{s}</li>)}</ol>
          </div></div></div>
        )}
        <div className="col-md-4">
          <label className="form-label fw-semibold" htmlFor="user">User name</label>
          <input id="user" className="form-control" autoComplete="off" value={form.username} onChange={(e) => set({ username: e.target.value })} placeholder="you@gmail.com (empty = no sign-in)" />
        </div>
        <div className="col-md-4">
          <label className="form-label fw-semibold" htmlFor="smtppw">Password</label>
          <input id="smtppw" type="password" autoComplete="new-password" className="form-control" value={password} onChange={(e) => setPassword(e.target.value)}
            placeholder={setup.passwordSet ? "set — type to replace" : preset === "gmail" ? "the 16-letter app password" : "password"} />
          <div className="form-text">Encrypted and write-only. A changed host or user name needs it again.</div>
        </div>
        <div className="col-md-4">
          <label className="form-label fw-semibold" htmlFor="from">Send as</label>
          <input id="from" className="form-control" value={form.from} onChange={(e) => set({ from: e.target.value })} placeholder="cactusTrade <you@gmail.com> (empty = the user name)" />
        </div>
        {setup.warnings.map((w) => <div key={w} className="col-12"><div className="alert alert-warning py-2 mb-0 small">{w}</div></div>)}
        <div className="col-12 d-flex flex-wrap align-items-center gap-3">
          <div className="form-check form-switch me-auto">
            <input className="form-check-input" type="checkbox" role="switch" id="enabled" checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
            <label className="form-check-label fw-semibold" htmlFor="enabled">Use this server for all e-mail</label>
          </div>
          <button className="btn btn-success" disabled={!!busy || !session}>{busy === "save" ? "Saving…" : "Save"}</button>
        </div>
      </div></form>

      <div className="card mb-4"><div className="card-body">
        <h2 className="h5">Send a test e-mail</h2>
        <p className="small text-secondary">Sent straight away through the saved server (save first), not through the outbox.</p>
        <div className="d-flex flex-wrap gap-2">
          <input type="email" className="form-control" style={{ maxWidth: "22rem" }} placeholder="to@example.com" value={to} onChange={(e) => setTo(e.target.value)} aria-label="Test address" />
          <button className="btn btn-outline-success" disabled={!!busy || !to || !session}
            onClick={() => void run("test", async () => { await api.sendTestEmail(to); setMsg(`The server accepted the test e-mail to ${to}: check the inbox (and the spam folder).`); void loadOutbox(); })}>
            <i className="bi bi-send me-1" />{busy === "test" ? "Sending…" : "Send test e-mail"}</button>
        </div>
      </div></div>

      <div className="d-flex align-items-center mb-2">
        <h2 className="h5 mb-0 flex-grow-1">Outbox</h2>
        <button className="btn btn-sm btn-outline-secondary" onClick={() => void loadOutbox()}><i className="bi bi-arrow-clockwise" /> Refresh</button>
      </div>
      {outbox.length === 0 ? <p className="text-secondary">No e-mail yet.</p> : (
        <div className="table-responsive"><table className="table table-sm align-middle">
          <thead><tr><th>Queued</th><th>To</th><th>Subject</th><th>Status</th></tr></thead>
          <tbody>{outbox.map((m) => (
            <tr key={m.id}>
              <td className="text-nowrap small">{new Date(m.createdAt).toLocaleString()}</td>
              <td className="small">{m.to}</td>
              <td className="small">{m.subject}</td>
              <td className="small">{m.sentAt ? <span className="badge text-bg-success">sent</span>
                : m.attempts >= 10 ? <span className="badge text-bg-danger" title={m.lastError}>given up</span>
                : m.attempts > 0 ? <span className="badge text-bg-warning" title={m.lastError}>retrying ({m.attempts})</span>
                : <span className="badge text-bg-secondary">waiting</span>}
                {!m.sentAt && m.lastError && <div className="text-secondary text-truncate" style={{ maxWidth: "24rem" }}>{m.lastError}</div>}
              </td>
            </tr>
          ))}</tbody>
        </table></div>
      )}
    </>
  );
}
