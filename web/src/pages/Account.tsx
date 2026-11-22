import { useState, type FormEvent } from "react";
import { api, errorText } from "../lib/api";
import { useSession } from "../lib/session";

export function Account() {
  const { me, refresh } = useSession();
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [error, setError] = useState("");
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      await api.changePassword(cur, next);
      await refresh(); // the session ended: back to sign-in
    } catch (err) {
      setError(errorText(err));
    }
  }
  return (
    <>
      <h1 className="h3">Your account</h1>
      <p>Signed in as <strong>{me?.user.username}</strong> ({me?.user.role}).</p>
      <h2 className="h5 mt-4">Change password</h2>
      <form className="col-md-6" onSubmit={submit}>
        <input className="form-control mb-2" type="password" placeholder="current password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} required />
        <input className="form-control mb-2" type="password" placeholder="new password (12+)" minLength={12} autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} required />
        {error && <div className="alert alert-danger py-2">{error}</div>}
        <button className="btn btn-success">Change password</button>
        <div className="form-text">You will be signed out everywhere and sign in again.</div>
      </form>
    </>
  );
}
