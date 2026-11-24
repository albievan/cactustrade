import { useEffect, useState, type FormEvent } from "react";
import { api, errorText, type Profile } from "../lib/api";
import { useSession } from "../lib/session";
import { ProblemAlert } from "../components/ProblemAlert";

const kinds: { key: keyof Profile["notify"]; label: string; help: string }[] = [
  { key: "trades", label: "Trades", help: "buying, selling and shorting: orders, fills and closes" },
  { key: "watchlist", label: "Watchlist", help: "shares added to or removed from a watchlist (also by the scanner)" },
  { key: "alerts", label: "Alerts", help: "warnings and errors: kill switch, limits reached, failures" },
];

export function Account() {
  const { me, refresh } = useSession();
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [error, setError] = useState("");
  const [profile, setProfile] = useState<Profile | null>(null);
  // saved is the address on the server: a different one needs the current password (7.1.11).
  const [saved, setSaved] = useState("");
  const [ppw, setPpw] = useState("");
  const [pmsg, setPmsg] = useState("");
  const [perr, setPerr] = useState<unknown>(null);
  const loaded = (p: Profile) => { setProfile(p); setSaved(p.email); setPpw(""); };
  useEffect(() => { api.myProfile().then(loaded).catch(setPerr); }, []);
  const emailChanged = !!profile && profile.email.trim() !== saved;
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
  async function saveProfile(e: FormEvent) {
    e.preventDefault();
    if (!profile) return;
    setPerr(null); setPmsg("");
    try {
      loaded(await api.setMyProfile(emailChanged ? { ...profile, currentPassword: ppw } : profile));
      setPmsg("Saved.");
    } catch (err) { setPerr(err); }
  }
  return (
    <>
      <h1 className="h3">Your account</h1>
      <p>Signed in as <strong>{me?.user.username}</strong> ({me?.user.role}).</p>

      <h2 className="h5 mt-4">E-mail</h2>
      {profile && (
        <form className="col-lg-8" onSubmit={saveProfile}>
          <label className="form-label fw-semibold" htmlFor="email">Your e-mail address</label>
          <input id="email" type="email" className="form-control mb-1" placeholder="you@example.com" maxLength={254} value={profile.email}
            onChange={(e) => setProfile({ ...profile, email: e.target.value })} />
          <div className="form-text mb-3">Notifications and password-reset links go here. Empty = no e-mail.</div>
          {emailChanged && (
            <>
              <label className="form-label fw-semibold" htmlFor="email-pw">Current password (to change the address)</label>
              <input id="email-pw" type="password" className="form-control mb-3" autoComplete="current-password" required
                value={ppw} onChange={(e) => setPpw(e.target.value)} />
            </>
          )}
          <div className="fw-semibold mb-1">E-mail me about</div>
          {kinds.map((k) => (
            <div className="form-check" key={k.key}>
              <input className="form-check-input" type="checkbox" id={"n-" + k.key} checked={profile.notify[k.key]}
                onChange={(e) => setProfile({ ...profile, notify: { ...profile.notify, [k.key]: e.target.checked } })} />
              <label className="form-check-label" htmlFor={"n-" + k.key}>{k.label} <span className="text-secondary small">— {k.help}</span></label>
            </div>
          ))}
          <div className="form-text">For the eToro accounts you own or that are shared with you. An administrator sets up the mail server under E-mail.</div>
          <ProblemAlert error={perr} className="py-2 mt-2" />
          {pmsg && <div className="alert alert-success py-2 mt-2" role="status">{pmsg}</div>}
          <button className="btn btn-success mt-2">Save e-mail settings</button>
        </form>
      )}
      {!profile && <ProblemAlert error={perr} className="py-2" />}

      <h2 className="h5 mt-5">Change password</h2>
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
