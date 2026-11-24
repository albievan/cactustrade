import { useEffect, useState } from "react";
import { api, type ForceClosePlan, type ForceCloseReport } from "../lib/api";
import { ConfirmDialog } from "./ConfirmDialog";
import { ProblemAlert } from "./ProblemAlert";

// ForceCloseDialog asks before a force close (R24): the server says what it would close
// (its preview), and the admin types their password, which the server checks like a sign-in.
// accountId names one account; all force-closes every demo account the admin can trade. A
// refusal (wrong password, real account, too many attempts) stays in the dialog.
export function ForceCloseDialog({ accountId, all = false, onCancel, onDone }: {
  accountId?: string;
  all?: boolean;
  onCancel: () => void;
  onDone: (reports: ForceCloseReport[]) => void | Promise<void>;
}) {
  const [plans, setPlans] = useState<ForceClosePlan[] | null>(null);
  const [password, setPassword] = useState("");
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    const load = all ? api.forceCloseAllPreview() : api.forceClosePreview(accountId).then((p) => [p]);
    load.then(setPlans).catch(setError);
  }, [accountId, all]);
  async function confirm() {
    setError(null);
    try {
      const reports = all ? await api.forceCloseAll(password) : [await api.forceClose(password, accountId)];
      await onDone(reports);
    } catch (e) {
      setError(e);
      setPassword("");
    }
  }
  return (
    <ConfirmDialog title={all ? "Force close every demo account?" : "Force close?"} confirmLabel="Force close" busyLabel="Closing…"
      icon="door-closed" confirmDisabled={!password || plans === null} onCancel={onCancel} onConfirm={confirm}>
      {plans === null && !error && <p className="text-secondary">Reading what is open…</p>}
      {plans !== null && (
        <ul aria-label="What the force close does">
          {plans.map((p) => <li key={p.accountId}>{p.summary.charAt(0).toUpperCase() + p.summary.slice(1)}.</li>)}
        </ul>
      )}
      <p>cactusTrade cancels its pending orders and closes every position <strong>it</strong> opened, now. Positions opened
        outside cactusTrade are left alone. The agent is <strong>stopped</strong>: unlike the kill switch, no admin reset is
        needed before it can start again.</p>
      <ProblemAlert error={error} className="py-2 small" />
      <label className="form-label" htmlFor="force-close-password">Your password</label>
      <input id="force-close-password" type="password" className="form-control" autoComplete="current-password" maxLength={256}
        value={password} onChange={(e) => setPassword(e.target.value)} />
    </ConfirmDialog>
  );
}

// forceCloseNote says what a force close sent, for the page's success message.
export function forceCloseNote(reports: ForceCloseReport[]): string {
  const n = (k: number, one: string) => `${k} ${one}${k === 1 ? "" : "s"}`;
  return reports.map((r) => {
    const failed = r.errors.length > 0 ? ` (${n(r.errors.length, "failure")}: ${r.errors.join("; ")})` : "";
    return `${r.name}: ${n(r.closing, "close")} and ${n(r.cancelled, "cancel")} sent; the agent is ${r.state}${failed}.`;
  }).join(" ");
}
