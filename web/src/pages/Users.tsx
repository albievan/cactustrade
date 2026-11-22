import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorText, type Role, type User } from "../lib/api";
import { useSession } from "../lib/session";

const roles: Role[] = ["viewer", "operator", "admin"];

export function Users() {
  const { me } = useSession();
  const [users, setUsers] = useState<User[]>([]);
  const [error, setError] = useState("");
  const [form, setForm] = useState({ username: "", password: "", role: "viewer" as Role });
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
        <thead><tr><th>Username</th><th>Role</th><th>Status</th><th /></tr></thead>
        <tbody>
          {users.map((u) => (
            <tr key={u.id}>
              <td>{u.username}{u.id === me?.user.id && <span className="badge text-bg-secondary ms-2">you</span>}</td>
              <td>
                <select className="form-select form-select-sm w-auto" value={u.role} onChange={(e) => void act(() => api.updateUser(u.id, { role: e.target.value as Role }))}>
                  {roles.map((r) => <option key={r}>{r}</option>)}
                </select>
              </td>
              <td>{u.disabled ? <span className="badge text-bg-warning">disabled</span> : <span className="badge text-bg-success">active</span>}</td>
              <td className="text-end">
                <button className="btn btn-sm btn-outline-secondary me-2" onClick={() => void act(() => api.updateUser(u.id, { disabled: !u.disabled }))}>{u.disabled ? "Enable" : "Disable"}</button>
                <button className="btn btn-sm btn-outline-secondary me-2" onClick={() => { const p = window.prompt(`New password for ${u.username} (12+ characters)`); if (p) void act(() => api.updateUser(u.id, { password: p })); }}>Reset password</button>
                {u.id !== me?.user.id && <button className="btn btn-sm btn-outline-danger" onClick={() => { if (window.confirm(`Delete ${u.username}?`)) void act(() => api.deleteUser(u.id)); }}>Delete</button>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
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
