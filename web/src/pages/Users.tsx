import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorText, type Role, type User } from "../lib/api";
import { useSession } from "../lib/session";
import { ConfirmDialog } from "../components/ConfirmDialog";

const roles: Role[] = ["viewer", "operator", "admin"];

export function Users() {
  const { me } = useSession();
  const [users, setUsers] = useState<User[]>([]);
  const [error, setError] = useState("");
  const [form, setForm] = useState({ username: "", password: "", role: "viewer" as Role });
  const [dialog, setDialog] = useState<{ kind: "delete" | "password"; user: User } | null>(null);
  const [newPassword, setNewPassword] = useState("");
  const load = useCallback(() => {
    api.users().then(setUsers).catch((e) => setError(errorText(e)));
  }, []);
  useEffect(load, [load]);
  async function act(f: () => Promise<unknown>) {
    setError("");
    try {
      await f();
      load();
    } catch (e) {
      setError(errorText(e));
    }
  }
  const ask = (kind: "delete" | "password", user: User) => { setNewPassword(""); setDialog({ kind, user }); };
  async function confirmed(f: () => Promise<unknown>) {
    await act(f);
    setDialog(null);
    setNewPassword("");
  }
  async function create(e: FormEvent) {
    e.preventDefault();
    await act(async () => {
      await api.createUser(form.username, form.password, form.role);
      setForm({ username: "", password: "", role: "viewer" });
    });
  }
  return (
    <>
      <h1 className="h3">Users</h1>
      <p className="text-secondary">Viewers read; operators can also start, stop and pull the kill switch; admins manage everything.</p>
      {error && <div className="alert alert-danger">{error}</div>}
      <table className="table align-middle">
        <thead><tr><th><span className="visually-hidden">Actions</span></th><th>Username</th><th>Role</th><th>Status</th></tr></thead>
        <tbody>
          {users.map((u) => (
            <tr key={u.id}>
              <td className="text-nowrap" style={{ width: "1%" }}>
                <div className="btn-group btn-group-sm" role="group" aria-label={`Actions for ${u.username}`}>
                  <button className="btn btn-outline-secondary" title={u.disabled ? "Enable" : "Disable"} aria-label={`${u.disabled ? "Enable" : "Disable"} ${u.username}`}
                    onClick={() => void act(() => api.updateUser(u.id, { disabled: !u.disabled }))}>
                    <i className={"bi " + (u.disabled ? "bi-person-check" : "bi-person-slash")} /></button>
                  <button className="btn btn-outline-secondary" title="Reset password" aria-label={`Reset the password of ${u.username}`} onClick={() => ask("password", u)}>
                    <i className="bi bi-key" /></button>
                  {u.id !== me?.user.id && (
                    <button className="btn btn-outline-danger" title="Delete" aria-label={`Delete ${u.username}`} onClick={() => ask("delete", u)}>
                      <i className="bi bi-trash" /></button>
                  )}
                </div>
              </td>
              <td>{u.username}{u.id === me?.user.id && <span className="badge text-bg-secondary ms-2">you</span>}</td>
              <td>
                <select className="form-select form-select-sm w-auto" value={u.role} onChange={(e) => void act(() => api.updateUser(u.id, { role: e.target.value as Role }))}>
                  {roles.map((r) => <option key={r}>{r}</option>)}
                </select>
              </td>
              <td>{u.disabled ? <span className="badge text-bg-warning">disabled</span> : <span className="badge text-bg-success">active</span>}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {dialog?.kind === "delete" && (
        <ConfirmDialog title={`Delete ${dialog.user.username}?`} confirmLabel="Delete" busyLabel="Deleting…" icon="trash"
          onCancel={() => setDialog(null)} onConfirm={() => confirmed(() => api.deleteUser(dialog.user.id))}>
          <p className="mb-0">Their sessions and API keys stop working. This cannot be undone.</p>
        </ConfirmDialog>
      )}
      {dialog?.kind === "password" && (
        <ConfirmDialog title={`New password for ${dialog.user.username}`} confirmLabel="Set password" busyLabel="Setting…" icon="key" variant="primary"
          confirmDisabled={newPassword === ""} onCancel={() => setDialog(null)}
          onConfirm={() => confirmed(() => api.updateUser(dialog.user.id, { password: newPassword }))}>
          <label className="form-label" htmlFor="new-password">New password (12+ characters)</label>
          <input id="new-password" className="form-control" type="password" autoComplete="new-password" autoFocus
            value={newPassword} onChange={(e) => setNewPassword(e.target.value)} />
        </ConfirmDialog>
      )}
      <h2 className="h5 mt-4">Add a user</h2>
      <form className="row g-2" onSubmit={create}>
        <div className="col-md-4"><input className="form-control" placeholder="username" value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} required /></div>
        <div className="col-md-4"><input className="form-control" type="password" placeholder="password (12+)" minLength={12} autoComplete="new-password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} required /></div>
        <div className="col-md-2"><select className="form-select" value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value as Role })}>{roles.map((r) => <option key={r}>{r}</option>)}</select></div>
        <div className="col-md-2"><button className="btn btn-success w-100">Add</button></div>
      </form>
    </>
  );
}
