import { useEffect, useState, type FormEvent } from "react";
import { api, backupFileUrl, type BackupSchedule, type BackupStatus } from "../lib/api";
import { useSession } from "../lib/session";
import { ProblemAlert } from "../components/ProblemAlert";
import { AGE_KEYGEN_COMMAND, generateAgeKeyPair, x25519Supported, type AgeKeyPair } from "../lib/agekey";

// Backups from the app (R48, admins). Every rule comes from the server's answers: the folder
// is fixed at deploy time, a schedule needs a recipient (OD29), downloads need a web session
// (OD40). A key pair made here keeps its private half in this browser: only the public key
// is ever sent.

function size(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / 1024 / 1024).toFixed(1)} MiB`;
}

function when(iso: string): string {
  return new Date(iso).toLocaleString();
}

const kinds: { id: BackupSchedule["kind"]; label: string }[] = [
  { id: "off", label: "Off" },
  { id: "hourly", label: "Every hour" },
  { id: "every", label: "Every N hours" },
  { id: "daily", label: "Daily at a time" },
];

export function Backups() {
  const { can } = useSession();
  const admin = can("settings.write");
  const [st, setSt] = useState<BackupStatus | null>(null);
  const [sched, setSched] = useState<BackupSchedule>({ kind: "off", keep: 0 });
  const [recipients, setRecipients] = useState("");
  const [pair, setPair] = useState<AgeKeyPair | null>(null);
  const [saved, setSaved] = useState(false);
  const [noX25519, setNoX25519] = useState(false);
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState("");
  const [error, setError] = useState<unknown>(null);

  const apply = (s: BackupStatus) => {
    setSt(s);
    setSched({ ...s.schedule });
    setRecipients(s.recipients.filter((r) => r.source === "ui").map((r) => r.key).join("\n"));
  };
  useEffect(() => {
    if (admin) api.backups().then(apply).catch(setError);
  }, [admin]);

  async function run(label: string, f: () => Promise<void>) {
    setBusy(label); setError(null); setMsg("");
    try { await f(); } catch (e) { setError(e); } finally { setBusy(""); }
  }

  if (!admin) return <><h1 className="h3">Backups</h1><p className="text-secondary">Only administrators manage backups.</p></>;
  if (!st) return <><h1 className="h3">Backups</h1><ProblemAlert error={error} /></>;
  if (!st.available) {
    return (
      <>
        <h1 className="h3">Backups</h1>
        <div className="alert alert-light border">This deployment has no backup folder for the app (<code>CACTUSTRADE_BACKUP_DIR</code>); backups from the app are unavailable.</div>
      </>
    );
  }

  const saveSchedule = (e: FormEvent) => {
    e.preventDefault();
    void run("schedule", async () => {
      apply(await api.setBackupSchedule(sched));
      setMsg("Schedule saved and audited.");
    });
  };
  const lines = (t: string) => t.split("\n").map((l) => l.trim()).filter((l) => l !== "");
  const saveRecipients = (e: FormEvent) => {
    e.preventDefault();
    void run("recipients", async () => {
      apply(await api.setBackupRecipients(lines(recipients)));
      setMsg("Recipients saved; every administrator was e-mailed.");
    });
  };
  const makePair = () => void run("keypair", async () => {
    if (!(await x25519Supported())) { setNoX25519(true); return; }
    setPair(await generateAgeKeyPair());
    setSaved(false);
  });
  // Only the public key goes to the server; the private key is dropped from the page.
  const usePair = () => {
    if (!pair) return;
    const recipient = pair.recipient;
    void run("recipients", async () => {
      apply(await api.setBackupRecipients([...lines(recipients), recipient]));
      setPair(null);
      setSaved(false);
      setMsg("Public key added; the private key stays only where you saved it.");
    });
  };
  const downloadIdentity = () => {
    if (!pair || typeof URL.createObjectURL !== "function") return;
    const url = URL.createObjectURL(new Blob([`# cactusTrade backup identity\n# public key: ${pair.recipient}\n${pair.identity}\n`], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url; a.download = "cactustrade-backup.agekey";
    a.click();
    URL.revokeObjectURL(url);
  };
  const backupNow = () => void run("now", async () => {
    const f = await api.runBackup();
    setMsg(`Backup ${f.name} written (${size(f.size)}).`);
    apply(await api.backups());
  });
  const envOnly = st.recipientSource === "env";

  return (
    <>
      <h1 className="h3">Backups</h1>
      <p className="text-secondary">
        cactusTrade backs up its database into the folder below, always encrypted with age to the public keys listed here:
        only the holder of a matching private key can read a backup. Trading is not affected by a failed backup.
      </p>
      {msg && <div className="alert alert-success" role="status">{msg}</div>}
      <ProblemAlert error={error} />

      <div className="row g-3 mb-4">
        <div className="col-lg-6"><div className="card stat h-100"><div className="card-body">
          <div className="text-secondary small">Folder (set at deploy time)</div>
          <div className="value fs-6"><code data-testid="backup-folder">{st.folder}</code></div>
          <div className="small text-secondary">
            {st.driver === "mariadb" ? <>Files <code>.sql.gz.age</code>; restore with <code>deploy.sh --restore</code>.</>
              : <>Files <code>.db.age</code>; restore with <code>cactustrade restore</code>.</>}
          </div>
        </div></div></div>
        <div className="col-lg-6"><div className="card stat h-100"><div className="card-body">
          <div className="text-secondary small">Next scheduled backup</div>
          <div className="value fs-6" data-testid="backup-next">
            {st.nextRun ? <>{st.nextRunLocal} <span className="text-secondary small">({new Date(st.nextRun).toISOString().slice(0, 16).replace("T", " ")} UTC)</span></> : "none"}
          </div>
          <div className="small text-secondary" data-testid="backup-last">
            Last: {st.last ? <>{when(st.last.at)} ({st.last.trigger}) — {st.last.ok ? <span className="text-success">OK, {st.last.file}</span> : <span className="text-danger">failed: {st.last.error}</span>}</> : "never"}
            {st.running && <span className="ms-2 badge text-bg-warning">running</span>}
          </div>
        </div></div></div>
      </div>

      <form className="card mb-3" onSubmit={saveSchedule}><div className="card-body row g-3 align-items-end">
        <div className="col-md-3">
          <label className="form-label fw-semibold" htmlFor="kind">Schedule</label>
          <select id="kind" className="form-select" value={sched.kind} onChange={(e) => setSched({ ...sched, kind: e.target.value as BackupSchedule["kind"] })}>
            {kinds.map((k) => <option key={k.id} value={k.id}>{k.label}</option>)}
          </select>
        </div>
        {sched.kind === "every" && (
          <div className="col-md-2">
            <label className="form-label fw-semibold" htmlFor="every">Hours</label>
            <input id="every" type="number" className="form-control" value={sched.everyHours ?? ""} onChange={(e) => setSched({ ...sched, everyHours: Number(e.target.value) })} />
          </div>
        )}
        {sched.kind === "daily" && (
          <div className="col-md-2">
            <label className="form-label fw-semibold" htmlFor="at">At</label>
            <input id="at" type="time" className="form-control" value={sched.at ?? ""} onChange={(e) => setSched({ ...sched, at: e.target.value })} />
          </div>
        )}
        <div className="col-md-3">
          <label className="form-label fw-semibold" htmlFor="zone">Time zone</label>
          <input id="zone" className="form-control" value={sched.zone ?? ""} placeholder="Europe/London" onChange={(e) => setSched({ ...sched, zone: e.target.value })} />
        </div>
        <div className="col-md-2">
          <label className="form-label fw-semibold" htmlFor="keep">Keep newest</label>
          <input id="keep" type="number" className="form-control" value={sched.keep} onChange={(e) => setSched({ ...sched, keep: Number(e.target.value) })} />
          <div className="form-text">0 = keep all</div>
        </div>
        <div className="col-md-2">
          <button className="btn btn-success w-100" disabled={busy !== ""}><i className="bi bi-save me-1" />Save schedule</button>
        </div>
      </div></form>

      <form className="card mb-3" onSubmit={saveRecipients}><div className="card-body">
        <label className="form-label fw-semibold" htmlFor="recipients">Recipients (age public keys, one per line)</label>
        {envOnly && (
          <div className="small text-secondary mb-2">
            In force now: the deployment's <code>CACTUSTRADE_BACKUP_RECIPIENT</code> ({st.recipients.map((r) => r.fingerprint).join(", ")}). Keys entered here take its place.
          </div>
        )}
        <textarea id="recipients" className="form-control font-monospace" rows={3} value={recipients} placeholder="age1…" onChange={(e) => setRecipients(e.target.value)} />
        <div className="d-flex flex-wrap gap-2 mt-2">
          <button className="btn btn-outline-success" disabled={busy !== ""}><i className="bi bi-save me-1" />Save recipients</button>
          <button type="button" className="btn btn-outline-secondary" onClick={makePair} disabled={busy !== "" || pair !== null}>
            <i className="bi bi-key me-1" />Generate a key pair
          </button>
        </div>
        {noX25519 && (
          <div className="alert alert-light border small mt-3 mb-0">
            This browser cannot make an X25519 key. On your own computer run <code>{AGE_KEYGEN_COMMAND}</code>, keep that file safe and paste the
            printed <code>age1…</code> public key above.
          </div>
        )}
        {pair && (
          <div className="alert alert-warning mt-3 mb-0" data-testid="keypair">
            <div className="fw-semibold mb-1">Save this private key now — it is shown once and never sent to cactusTrade</div>
            <code className="d-block text-break mb-2" data-testid="identity">{pair.identity}</code>
            <div className="d-flex flex-wrap gap-2 mb-2">
              <button type="button" className="btn btn-sm btn-outline-dark" onClick={() => void navigator.clipboard?.writeText(pair.identity)}><i className="bi bi-clipboard me-1" />Copy</button>
              <button type="button" className="btn btn-sm btn-outline-dark" onClick={downloadIdentity}><i className="bi bi-download me-1" />Download</button>
            </div>
            <div className="small mb-2">Public key: <code>{pair.recipient}</code></div>
            <div className="form-check mb-2">
              <input id="saved" type="checkbox" className="form-check-input" checked={saved} onChange={(e) => setSaved(e.target.checked)} />
              <label className="form-check-label" htmlFor="saved">I have saved the private key somewhere off this machine</label>
            </div>
            <button type="button" className="btn btn-sm btn-success" disabled={!saved || busy !== ""} onClick={usePair}>Add the public key</button>
            <button type="button" className="btn btn-sm btn-link" onClick={() => { setPair(null); setSaved(false); }}>Discard</button>
          </div>
        )}
      </div></form>

      <div className="card"><div className="card-body">
        <div className="d-flex flex-wrap align-items-center gap-2 mb-3">
          <div className="fw-semibold me-auto">Backup files</div>
          <button className="btn btn-outline-success" onClick={backupNow} disabled={busy !== ""}>
            <i className="bi bi-archive me-1" />{busy === "now" ? "Backing up…" : "Back up now"}
          </button>
        </div>
        {st.files.length === 0 ? <p className="text-secondary mb-0">No backup files yet.</p> : (
          <table className="table table-sm mb-0">
            <thead><tr><th>File</th><th>Taken</th><th className="text-end">Size</th><th /></tr></thead>
            <tbody>
              {st.files.map((f) => (
                <tr key={f.name}>
                  <td><code>{f.name}</code></td>
                  <td>{when(f.at)}</td>
                  <td className="text-end">{size(f.size)}</td>
                  <td className="text-end"><a className="btn btn-sm btn-outline-secondary" href={backupFileUrl(f.name)} download={f.name}><i className="bi bi-download me-1" />Download</a></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div></div>
    </>
  );
}
