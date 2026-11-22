import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, errorText } from "../lib/api";

export function Setup() {
  const nav = useNavigate();
  const [token, setToken] = useState("");
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (password !== confirm) {
      setError("The passwords do not match.");
      return;
    }
    try {
      await api.setup(token, username, password);
      nav("/", { replace: true });
    } catch (err) {
      setError(errorText(err));
    }
  }
  return (
    <div className="card auth-card shadow-sm">
      <div className="card-body p-4">
        <h1 className="h4">First-run setup</h1>
        <p className="text-secondary small">
          Create the administrator. The setup token was printed by <code>deploy.sh</code>, or is in
          the <code>setup_token</code> file in the data folder of a standalone install.
        </p>
        <form onSubmit={submit}>
          <div className="mb-3">
            <label className="form-label" htmlFor="token">Setup token</label>
            <input id="token" className="form-control mono" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} required />
          </div>
          <div className="mb-3">
            <label className="form-label" htmlFor="username">Administrator username</label>
            <input id="username" className="form-control" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
          </div>
          <div className="mb-3">
            <label className="form-label" htmlFor="password">Password (12+ characters)</label>
            <input id="password" type="password" minLength={12} className="form-control" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          </div>
          <div className="mb-3">
            <label className="form-label" htmlFor="confirm">Repeat password</label>
            <input id="confirm" type="password" className="form-control" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
          </div>
          {error && <div className="alert alert-danger py-2" role="alert">{error}</div>}
          <button className="btn btn-success w-100">Create administrator</button>
        </form>
        <div className="mt-3 small"><Link to="/">Back to sign-in</Link></div>
      </div>
    </div>
  );
}
