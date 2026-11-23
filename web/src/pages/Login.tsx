import { useEffect, useState, type FormEvent } from "react";
import { api, errorText } from "../lib/api";
import { useSession } from "../lib/session";
import { AuthLayout } from "../components/AuthLayout";

export function Login() {
  const { refresh } = useSession();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [show, setShow] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [version, setVersion] = useState("");
  const [canReset, setCanReset] = useState(false);
  const [forgot, setForgot] = useState(false);
  const [sent, setSent] = useState(false);
  useEffect(() => {
    api.publicStatus().then((s) => { setVersion(s.version); setCanReset(s.passwordResetAvailable); }).catch(() => undefined);
  }, []);
  async function requestReset(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.requestPasswordReset(username);
      setSent(true);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  if (forgot) {
    return (
      <AuthLayout version={version}>
        <h2>Forgot password</h2>
        {sent ? (
          <div className="alert alert-success" role="status">
            If <strong>{username}</strong> has an e-mail address in cactusTrade, a link to choose a new password is on its way.
            It works once, for 30 minutes.
          </div>
        ) : (
          <form onSubmit={requestReset}>
            <div className="mb-3">
              <label className="form-label" htmlFor="fusername">Username</label>
              <input id="fusername" className="form-control form-control-lg" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required autoFocus />
              <div className="form-text">The link goes to the e-mail address saved under Your account.</div>
            </div>
            {error && <div className="alert alert-danger py-2" role="alert">{error}</div>}
            <button className="btn btn-success btn-lg w-100" disabled={busy}>E-mail me a link</button>
          </form>
        )}
        <button type="button" className="btn btn-link px-0 mt-3" onClick={() => { setForgot(false); setSent(false); setError(""); }}>Back to sign in</button>
      </AuthLayout>
    );
  }
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
    <AuthLayout version={version}>
      <h2>Sign in</h2>
      <form onSubmit={submit}>
        <div className="mb-3">
          <label className="form-label" htmlFor="username">Username</label>
          <input id="username" className="form-control form-control-lg" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required autoFocus />
        </div>
        <div className="mb-3">
          <label className="form-label" htmlFor="password">Password</label>
          <div className="pw">
            <input id="password" type={show ? "text" : "password"} className="form-control form-control-lg" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
            <button type="button" className="eye" onClick={() => setShow(!show)} aria-label={show ? "Hide the password" : "Show the password"}><i className={"bi " + (show ? "bi-eye-slash" : "bi-eye")} /></button>
          </div>
        </div>
        {error && <div className="alert alert-danger py-2" role="alert">{error}</div>}
        <button className="btn btn-success btn-lg w-100" disabled={busy}>Sign in</button>
      </form>
      {canReset && <button type="button" className="btn btn-link px-0 mt-2" onClick={() => { setForgot(true); setError(""); }}>Forgot password?</button>}
      <p className="form-text mt-3">
        First start: sign in as <code>admin</code> / <code>admin</code>; you will be asked to choose a new password.
      </p>
    </AuthLayout>
  );
}
