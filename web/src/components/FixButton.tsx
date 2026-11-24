import { Link } from "react-router-dom";
import type { Missing } from "../lib/api";

// What each missing piece of setup (R22) leads to. The server decides what is missing and
// whether the caller may fix it; this only says where the fix is.
interface Fix {
  missing: string; // what is missing, in a few words
  icon: string;
  label: string; // short text on the button
  title: string; // the tooltip and the accessible name
  to?: (accountId?: string) => string; // the page with the fix
  help?: string; // nothing to press: an explanation only (a deployment setting)
}

export const fixes: Record<Missing, Fix> = {
  trading_budget: { missing: "No trading budget", icon: "bi-cash-coin", label: "Set budget", title: "Set the trading budget (Risk limits)", to: () => "/risk" },
  account_keys: {
    missing: "The eToro keys are not set", icon: "bi-key", label: "Add keys", title: "Add the account's keys (Platform accounts)",
    to: (id) => (id ? `/accounts?keys=${encodeURIComponent(id)}` : "/accounts"),
  },
  account: { missing: "No eToro account", icon: "bi-wallet2", label: "Add account", title: "Add an eToro account (Platform accounts)", to: () => "/accounts" },
  watchlist: { missing: "The watchlist is empty", icon: "bi-plus-lg", label: "Add shares", title: "Add shares to the watchlist: the agent has nothing to trade", to: () => "/watchlist" },
  reconcile: { missing: "Not reconciled with eToro", icon: "bi-arrow-repeat", label: "Reconcile", title: "Reconcile with eToro now (read-only)", to: () => "/" },
  kill_reset: { missing: "The kill switch is on", icon: "bi-arrow-counterclockwise", label: "Reset kill switch", title: "Reset the kill switch (admin)", to: () => "/" },
  llm_profile: { missing: "The LLM reviewer cannot review", icon: "bi-robot", label: "Fix reviewer", title: "Give the LLM reviewer a default profile with a key (LLM reviewer)", to: () => "/llm" },
  email_server: { missing: "No mail server", icon: "bi-envelope", label: "Set up e-mail", title: "Set up the mail server (E-mail)", to: () => "/email" },
  public_url: {
    missing: "CACTUSTRADE_PUBLIC_URL is not set", icon: "bi-question-circle", label: "Public URL", title: "What is CACTUSTRADE_PUBLIC_URL?",
    help: "CACTUSTRADE_PUBLIC_URL is the address users open cactusTrade at (e.g. https://trader.example:8443). " +
      "It is a deployment setting: set it in .env.local (or the container's environment) and restart cactusTrade.",
  },
};

// FixButton is a compact icon button next to a message saying something is missing: it opens
// the page with the fix, or runs onFix when the page has the fix itself (reconcile, the reset
// dialog, a form on the same page). public_url has nothing to press: it explains in a tooltip.
export function FixButton({ what, accountId, onFix, disabled }: { what: Missing; accountId?: string; onFix?: () => void; disabled?: boolean }) {
  const f = fixes[what];
  if (!f) return null;
  const body = <><i className={`bi ${f.icon} me-1`} aria-hidden="true" />{f.label}</>;
  const cls = "btn btn-sm btn-outline-primary py-0 text-nowrap fix-button";
  const name = `${f.label}: ${f.title}`; // the accessible name starts with the visible text
  if (f.help) {
    return (
      <span className="btn btn-sm btn-outline-secondary py-0 text-nowrap fix-button" tabIndex={0} role="note" title={f.help} aria-label={f.help}>
        {body}
      </span>
    );
  }
  if (onFix) {
    return <button type="button" className={cls} title={f.title} aria-label={name} disabled={disabled} onClick={onFix}>{body}</button>;
  }
  return <Link className={cls} to={f.to?.(accountId) ?? "/"} title={f.title} aria-label={name}>{body}</Link>;
}
