import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorText, type ApiKey, type Role } from "../lib/api";
import { useSession } from "../lib/session";
import { ConfirmDialog } from "../components/ConfirmDialog";

const order: Role[] = ["viewer", "operator", "admin"];

export function ApiKeys() {
  const { me } = useSession();
  const [keys, setKeys] = useState<ApiKey[]>([]);
  const [error, setError] = useState("");
  const [secret, setSecret] = useState("");
  const [revoking, setRevoking] = useState<ApiKey | null>(null);
  const [form, setForm] = useState({ name: "", role: "viewer" as Role, days: 90 });
  const load = useCallback(() => {
    api.apiKeys().then(setKeys).catch((e) => setError(errorText(e)));
  }, []);
  useEffect(load, [load]);
  const allowed = order.slice(0, order.indexOf(me?.user.role ?? "viewer") + 1);
  async function create(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      const k = await api.createApiKey(form.name, form.role, form.days);
      setSecret(k.secret);
      setForm({ ...form, name: "" });
      load();
    } catch (err) {
      setError(errorText(err));
    }
  }
  async function revoke(id: string) {
    setError("");
    try {
      await api.revokeApiKey(id);
      load();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setRevoking(null);
    }
  }
  return (
    <>
      <h1 className="h3">API keys</h1>
      <p className="text-secondary">For programs: send <code>Authorization: Bearer &lt;key&gt;</code>. A key's role can never exceed yours. API keys cannot change credentials or create other keys.</p>
      {error && <div className="alert alert-danger">{error}</div>}
      {secret && (
        <div className="alert alert-success">
          <div className="fw-semibold mb-1">Copy the key now — it will not be shown again:</div>
          <div className="mono">{secret}</div>
          <button className="btn btn-sm btn-outline-success mt-2" onClick={() => setSecret("")}>Done</button>
        </div>
      )}
      <table className="table align-middle">
        <thead><tr><th><span className="visually-hidden">Actions</span></th><th>Name</th><th>Role</th><th>Created</th><th>Last used</th><th>Expires</th></tr></thead>
        <tbody>
          {keys.map((k) => (
            <tr key={k.id}>
              <td className="text-nowrap" style={{ width: "1%" }}>
                <button className="btn btn-sm btn-outline-danger" title="Revoke" aria-label={`Revoke ${k.name}`} onClick={() => setRevoking(k)}>
                  <i className="bi bi-x-circle" /></button>
              </td>
              <td>{k.name}{k.ownerId !== me?.user.id && <span className="badge text-bg-secondary ms-2">other user</span>}</td>
              <td>{k.role}</td>
              <td>{new Date(k.createdAt).toLocaleDateString()}</td>
              <td>{k.lastUsedAt ? new Date(k.lastUsedAt).toLocaleString() : "never"}</td>
              <td>{k.expiresAt ? new Date(k.expiresAt).toLocaleDateString() : "never"}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {revoking && (
        <ConfirmDialog title={`Revoke the key ${revoking.name}?`} confirmLabel="Revoke" busyLabel="Revoking…" icon="x-circle"
          onCancel={() => setRevoking(null)} onConfirm={() => revoke(revoking.id)}>
          <p className="mb-0">Programs using it stop working at once. This cannot be undone.</p>
        </ConfirmDialog>
      )}
      {me?.via === "session" && (
        <form className="row g-2" onSubmit={create}>
          <div className="col-md-5"><input className="form-control" placeholder="name, e.g. grafana" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required /></div>
          <div className="col-md-3"><select className="form-select" value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value as Role })}>{allowed.map((r) => <option key={r}>{r}</option>)}</select></div>
          <div className="col-md-2"><select className="form-select" value={form.days} onChange={(e) => setForm({ ...form, days: Number(e.target.value) })}>
            <option value={30}>30 days</option><option value={90}>90 days</option><option value={365}>1 year</option><option value={0}>never</option>
          </select></div>
          <div className="col-md-2"><button className="btn btn-success w-100">Create</button></div>
        </form>
      )}
    </>
  );
}
