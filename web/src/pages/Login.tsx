import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api, errorText } from "../lib/api";
import { useSession } from "../lib/session";

export function Login() {
  const { refresh } = useSession();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [setupRequired, setSetupRequired] = useState(false);
  useEffect(() => {
    api.publicStatus().then((s) => setSetupRequired(s.setupRequired)).catch(() => undefined);
  }, []);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.login(username, password);
      await refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="card auth-card shadow-sm">
      <div className="card-body p-4">
        <h1 className="h4 mb-3"><i className="bi bi-flower1 text-success me-2" />cactusTrade</h1>
        {setupRequired && (
          <div className="alert alert-info">
            First run: <Link to="/setup">create the administrator</Link>.
          </div>
        )}
        <form onSubmit={submit}>
          <div className="mb-3">
            <label className="form-label" htmlFor="username">Username</label>
            <input id="username" className="form-control" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
          </div>
          <div className="mb-3">
            <label className="form-label" htmlFor="password">Password</label>
            <input id="password" type="password" className="form-control" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          </div>
          {error && <div className="alert alert-danger py-2" role="alert">{error}</div>}
          <button className="btn btn-success w-100" disabled={busy}>Sign in</button>
        </form>
      </div>
    </div>
  );
}
