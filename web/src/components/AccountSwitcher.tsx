import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { agentTradable, api, type Account } from "../lib/api";
import { useMode } from "../lib/mode";

// The account the user is working on; every page shows that account's data. It lists the
// accounts the agent can trade (the server says which: eToro accounts and Trading 212 practice
// accounts, R45.11). Real Trading 212, Saxo and Tiger accounts are view-only and appear on the
// Overview page only.
export function AccountSwitcher() {
  const status = useMode();
  const [accounts, setAccounts] = useState<Account[]>([]);
  useEffect(() => {
    api.accounts().then((as) => setAccounts(as.filter(agentTradable))).catch(() => undefined);
  }, [status?.account?.id]);
  if (accounts.length === 0) {
    return <Link className="btn btn-sm btn-outline-secondary" to="/accounts"><i className="bi bi-plus-lg me-1" />Add an eToro account</Link>;
  }
  async function pick(id: string) {
    await api.setCurrentAccount(id);
    window.location.reload(); // every page reloads with the new account's data
  }
  return (
    <select className="form-select form-select-sm account-switch" aria-label="Trading account" value={status?.account?.id ?? ""}
      onChange={(e) => void pick(e.target.value)} title="The account you are working on">
      {accounts.map((a) => (
        <option key={a.id} value={a.id}>
          {a.mode === "real" ? "● REAL · " : "○ demo · "}{a.platform === "trading212" ? "Trading 212 · " : ""}{a.name}
          {a.access !== "owner" ? ` (${a.ownerName}, ${a.access})` : ""}
        </option>
      ))}
    </select>
  );
}
