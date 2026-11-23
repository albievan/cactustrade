import { useState, type FormEvent } from "react";
import { api, errorText } from "../lib/api";
import { useSession } from "../lib/session";

// Shown instead of the app while the account still has the default password (admin/admin).
// The server refuses everything else until the password is changed.
export function ChangeDefaultPassword() {
  const { me, refresh, signOut } = useSession();
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (next !== again) {
      setError("The new passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      await api.changePassword(cur, next);
      await refresh(); // the session ended: back to sign-in with the new password
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="card auth-card shadow-sm">
      <div className="card-body p-4">
        <h1 className="h4 mb-3"><i className="bi bi-shield-lock text-success me-2" />Choose a new password</h1>
        <p>
          <strong>{me?.user.username}</strong> still has the default password. Choose your own (at least 12
          characters) before using cactusTrade. You will then sign in again.
        </p>
        <form onSubmit={submit}>
          <div className="mb-3">
            <label className="form-label" htmlFor="current">Current password</label>
            <input id="current" type="password" className="form-control" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} required />
          </div>
          <div className="mb-3">
            <label className="form-label" htmlFor="new">New password</label>
            <input id="new" type="password" className="form-control" autoComplete="new-password" minLength={12} value={next} onChange={(e) => setNext(e.target.value)} required />
          </div>
          <div className="mb-3">
            <label className="form-label" htmlFor="again">New password again</label>
            <input id="again" type="password" className="form-control" autoComplete="new-password" minLength={12} value={again} onChange={(e) => setAgain(e.target.value)} required />
          </div>
          {error && <div className="alert alert-danger py-2" role="alert">{error}</div>}
          <button className="btn btn-success w-100" disabled={busy}>Change password</button>
          <button type="button" className="btn btn-link w-100 mt-2" onClick={() => void signOut()}>Sign out</button>
        </form>
      </div>
    </div>
  );
}
