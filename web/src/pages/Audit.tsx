import { useEffect, useState } from "react";
import { api, errorText, type AuditEntry } from "../lib/api";

export function Audit() {
  const [rows, setRows] = useState<AuditEntry[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    api.audit(200).then(setRows).catch((e) => setError(errorText(e)));
  }, []);
  return (
    <>
      <h1 className="h3">Audit log</h1>
      {error && <div className="alert alert-danger">{error}</div>}
      <table className="table table-sm">
        <thead><tr><th>When</th><th>Who</th><th>Action</th><th>Target</th><th>Details</th><th>From</th></tr></thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.id}>
              <td className="text-nowrap">{new Date(r.at).toLocaleString()}</td>
              <td>{r.actor}</td>
              <td><code>{r.action}</code></td>
              <td className="mono small">{r.target}</td>
              <td className="small">{r.details}</td>
              <td className="small text-secondary">{r.remoteIp}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
