import { useCallback, useEffect, useState } from "react";
import { api, errorText, type Credential } from "../lib/api";
import { useSession } from "../lib/session";
import { BrokerTest } from "../components/BrokerTest";

// Credentials are write-only: the page shows whether each is set, never its value.
export function Credentials() {
  const { can, me } = useSession();
  const [creds, setCreds] = useState<Credential[]>([]);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    api.credentials().then(setCreds).catch((e) => setError(errorText(e)));
  }, []);
  useEffect(load, [load]);
  const editable = can("credentials.write") && me?.via === "session";
  return (
    <>
      <h1 className="h3">Credentials</h1>
      <p className="text-secondary">
        Keys are stored encrypted (AES-256-GCM) and are <strong>write-only</strong>: once saved they are never shown
        again, logged or returned by the API. eToro binds each key pair to one environment, so demo and real pairs are
        separate. In eToro: Settings → Trading → API Key Management. The <em>Public API Key</em> goes in
        “API key”, the <em>User Key</em> in “user key”.
      </p>
      {error && <div className="alert alert-danger">{error}</div>}
      {can("credentials.write") && <BrokerTest />}
      <div className="list-group">
        {creds.map((c) => (
          <CredentialRow key={c.name} cred={c} editable={editable} onChange={load} />
        ))}
      </div>
    </>
  );
}

function CredentialRow({ cred, editable, onChange }: { cred: Credential; editable: boolean; onChange: () => void }) {
  const [value, setValue] = useState("");
  const [editing, setEditing] = useState(false);
  const [error, setError] = useState("");
  const fromEnv = cred.source === "env";
  async function save() {
    setError("");
    try {
      await api.setCredential(cred.name, value);
      setValue("");
      setEditing(false);
      onChange();
    } catch (e) {
      setError(errorText(e));
    }
  }
  async function remove() {
    if (!window.confirm(`Delete ${cred.label}?`)) return;
    try {
      await api.deleteCredential(cred.name);
      onChange();
    } catch (e) {
      setError(errorText(e));
    }
  }
  return (
    <div className="list-group-item">
      <div className="d-flex flex-wrap align-items-center gap-2">
        <div className="me-auto">
          <div className="fw-semibold">{cred.label}</div>
          <div className="small text-secondary">
            {cred.set ? (
              fromEnv ? (
                <span><i className="bi bi-terminal me-1" />set by the environment variable <code>{cred.envVar}</code> (change it there)</span>
              ) : (
                <span><i className="bi bi-check-circle text-success me-1" />set{cred.updatedAt && ` · ${new Date(cred.updatedAt).toLocaleString()}`}</span>
              )
            ) : (
              <span><i className="bi bi-dash-circle me-1" />not set{cred.envVar && <> · or set <code>{cred.envVar}</code></>}</span>
            )}
          </div>
        </div>
        {editable && !fromEnv && !editing && (
          <>
            <button className="btn btn-sm btn-outline-primary" onClick={() => setEditing(true)}>{cred.set ? "Replace" : "Set"}</button>
            {cred.set && <button className="btn btn-sm btn-outline-danger" onClick={() => void remove()}>Delete</button>}
          </>
        )}
      </div>
      {editing && (
        <div className="input-group mt-2">
          <input type="password" className="form-control mono" autoComplete="off" placeholder="Paste the key" value={value} onChange={(e) => setValue(e.target.value)} />
          <button className="btn btn-success" disabled={!value.trim()} onClick={() => void save()}>Save</button>
          <button className="btn btn-outline-secondary" onClick={() => { setEditing(false); setValue(""); }}>Cancel</button>
        </div>
      )}
      {error && <div className="text-danger small mt-1">{error}</div>}
    </div>
  );
}
