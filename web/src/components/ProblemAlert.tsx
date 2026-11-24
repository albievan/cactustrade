import { errorText, problemOf, type Missing } from "../lib/api";
import { FixButton, fixes } from "./FixButton";

// ProblemAlert shows an error (a refused request, an Error or a message) as an alert. When the
// server says setup is missing and the caller may fix it (problem.missing, problem.canFix;
// R22), the fix button sits next to the message. fixes replaces the default link of a code
// with an action of the page (e.g. reconcile in the agent panel).
export function ProblemAlert({ error, accountId, fixes: actions, className = "", variant = "danger" }: {
  error: unknown;
  accountId?: string;
  fixes?: Partial<Record<Missing, () => void>>;
  className?: string;
  variant?: "danger" | "warning";
}) {
  if (!error) return null;
  const text = typeof error === "string" ? error : errorText(error);
  const p = problemOf(error);
  const what = p?.missing;
  const show = !!what && (p?.canFix === true || !!fixes[what]?.help);
  return (
    <div className={`alert alert-${variant} d-flex flex-wrap align-items-center gap-2 ${className}`} role="alert">
      <span className="flex-grow-1">{text}</span>
      {show && what && <FixButton what={what} accountId={accountId} onFix={actions?.[what]} />}
    </div>
  );
}
