import { useEffect, useState, type FormEvent } from "react";
import { api, errorText } from "../lib/api";
import { AuthLayout } from "../components/AuthLayout";

// ResetPassword is the page a reset e-mail links to: /reset-password#token=… (the token is in
// the fragment, so it never reaches server logs or Referer headers).
export function ResetPassword() {
  const [token] = useState(() => new URLSearchParams(window.location.hash.slice(1)).get("token") ?? "");
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  const [version, setVersion] = useState("");
  useEffect(() => {
    api.publicStatus().then((s) => setVersion(s.version)).catch(() => undefined);
    // drop the token from the address bar and history
    window.history.replaceState(null, "", "/reset-password");
  }, []);
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (password !== again) { setError("The two passwords differ."); return; }
    setBusy(true); setError("");
    try { await api.confirmPasswordReset(token, password); setDone(true); } catch (err) { setError(errorText(err)); } finally { setBusy(false); }
  }
  return (
    <AuthLayout version={version}>
      <h2>Choose a new password</h2>
      {done ? (
        <div className="alert alert-success" role="status">Your password is changed. You were signed out everywhere.</div>
      ) : !token ? (
        <div className="alert alert-warning">This link is incomplete. Ask for a new one on the sign-in page.</div>
      ) : (
        <form onSubmit={submit}>
          <input className="form-control form-control-lg mb-2" type="password" placeholder="new password (12+)" minLength={12} autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required autoFocus />
          <input className="form-control form-control-lg mb-3" type="password" placeholder="the same again" minLength={12} autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} required />
          {error && <div className="alert alert-danger py-2" role="alert">{error}</div>}
          <button className="btn btn-success btn-lg w-100" disabled={busy}>Set password</button>
        </form>
      )}
      <a className="btn btn-link px-0 mt-3" href="/">Go to sign in</a>
    </AuthLayout>
  );
}
