import { useState } from "react";
import { api, errorText } from "../lib/api";
import { useMode } from "../lib/mode";
import { ConfirmDialog } from "../components/ConfirmDialog";

// Admin: version, database, and the reload / restart actions.
export function System() {
  const status = useMode();
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [asking, setAsking] = useState(false);
  async function reload() {
    setError(""); setMsg("");
    try {
      await api.reloadSettings();
      setMsg("Settings reloaded: credentials from the environment and secret files were read again.");
    } catch (e) {
      setError(errorText(e));
    }
  }
  async function restart() {
    setError(""); setBusy(true);
    try {
      await api.restart();
      setMsg("Restarting…");
      const start = Date.now();
      await new Promise((r) => setTimeout(r, 2000));
      for (;;) {
        try {
          const res = await fetch("/healthz", { cache: "no-store" });
          if (res.ok) break;
        } catch { /* still down */ }
        if (Date.now() - start > 120_000) throw new Error("cactusTrade did not come back within two minutes; check its logs.");
        await new Promise((r) => setTimeout(r, 1500));
      }
      window.location.reload();
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  }
  return (
    <>
      <h1 className="h3">System</h1>
      <div className="row g-3 mb-4">
        {[["Version", status ? `${status.version} (build ${status.build})` : "…"], ["Database", status?.database ?? "…"],
          ["Mode", status?.mode ?? "…"], ["Running since", status ? new Date(status.startedAt).toLocaleString() : "…"]].map(([k, v]) => (
          <div className="col-6 col-lg-3" key={k}><div className="card stat h-100"><div className="card-body">
            <div className="text-secondary small">{k}</div><div className="value fs-6">{v}</div>
          </div></div></div>
        ))}
      </div>
      {msg && <div className="alert alert-success" role="status">{msg}</div>}
      {error && <div className="alert alert-danger" role="alert">{error}</div>}
      <div className="card mb-3"><div className="card-body d-flex flex-wrap gap-3 align-items-center">
        <div className="me-auto">
          <div className="fw-semibold">Reload settings</div>
          <div className="small text-secondary">Reads the eToro keys from the environment and secret files again (e.g. after <code>deploy.sh</code> updated them). Settings stored in the app take effect immediately anyway.</div>
        </div>
        <button className="btn btn-outline-success" onClick={() => void reload()} disabled={busy}><i className="bi bi-arrow-repeat me-1" />Reload settings</button>
      </div></div>
      <div className="card"><div className="card-body d-flex flex-wrap gap-3 align-items-center">
        <div className="me-auto">
          <div className="fw-semibold">Restart cactusTrade</div>
          <div className="small text-secondary">A graceful restart: requests finish, the agent stops, and the server starts again (in containers the orchestrator starts it). On start-up it reconciles with eToro before anything else.</div>
        </div>
        <button className="btn btn-outline-danger" onClick={() => setAsking(true)} disabled={busy}><i className="bi bi-power me-1" />{busy ? "Restarting…" : "Restart"}</button>
      </div></div>
      {asking && (
        <ConfirmDialog title="Restart cactusTrade now?" confirmLabel="Restart" icon="power" onCancel={() => setAsking(false)}
          onConfirm={() => { setAsking(false); void restart(); }}>
          <p className="mb-0">The agent stops and the page reconnects when cactusTrade is back. On start-up it reconciles with eToro before anything else.</p>
        </ConfirmDialog>
      )}
    </>
  );
}
