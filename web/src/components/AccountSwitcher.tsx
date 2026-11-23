import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, viewOnly, type Account } from "../lib/api";
import { useMode } from "../lib/mode";

// The eToro account the user is working on; every page shows that account's data. Trading 212,
// Saxo and Tiger accounts are view-only and appear on the Overview page only.
export function AccountSwitcher() {
  const status = useMode();
  const [accounts, setAccounts] = useState<Account[]>([]);
  useEffect(() => {
    // only eToro accounts can be worked on; view-only ones are shown in the overview
    api.accounts().then((as) => setAccounts(as.filter((a) => !viewOnly(a.platform)))).catch(() => undefined);
  }, [status?.account?.id]);
  if (accounts.length === 0) {
    return <Link className="btn btn-sm btn-outline-secondary" to="/accounts"><i className="bi bi-plus-lg me-1" />Add an eToro account</Link>;
  }
  async function pick(id: string) {
    await api.setCurrentAccount(id);
    window.location.reload(); // every page reloads with the new account's data
  }
  return (
    <select className="form-select form-select-sm account-switch" aria-label="eToro account" value={status?.account?.id ?? ""}
      onChange={(e) => void pick(e.target.value)} title="The eToro account you are working on">
      {accounts.map((a) => (
        <option key={a.id} value={a.id}>
          {a.mode === "real" ? "● REAL · " : "○ demo · "}{a.name}{a.access !== "owner" ? ` (${a.ownerName}, ${a.access})` : ""}
        </option>
      ))}
    </select>
  );
}
