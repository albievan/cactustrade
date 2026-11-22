import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, type Credential, type SystemStatus } from "../lib/api";

export function Dashboard() {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [creds, setCreds] = useState<Credential[]>([]);
  useEffect(() => {
    api.status().then(setStatus).catch(() => undefined);
    api.credentials().then(setCreds).catch(() => undefined);
  }, []);
  const demoReady = creds.filter((c) => c.name.startsWith("etoro_demo_") && c.set).length === 2;
  return (
    <>
      <h1 className="h3 mb-4">Dashboard</h1>
      <div className="row g-3 mb-4">
        <Stat label="Agent" value={status?.agentState ?? "…"} icon="bi-robot" capitalize />
        <Stat label="Mode" value={status?.mode ?? "…"} icon="bi-shield-check" capitalize />
        <Stat label="Database" value={status?.database ?? "…"} icon="bi-database" />
        <Stat label="Version" value={status ? `${status.version} (${status.build})` : "…"} icon="bi-tag" />
      </div>
      {!demoReady && (
        <div className="alert alert-warning">
          The eToro <strong>demo</strong> keys are not set yet. Add them on the <Link to="/credentials">Credentials</Link> page,
          or start cactusTrade with <code>ETORO_DEMO_PUB</code> and <code>ETORO_DEMO_PK</code>.
        </div>
      )}
      <div className="card">
        <div className="card-body">
          <h2 className="h5">Coming next</h2>
          <p className="text-secondary mb-0">
            Live equity, cash, trading budget, positions, orders and fills, market regimes and the distance to every risk
            limit arrive in later phases (see <code>docs/PLAN.md</code>). The agent cannot trade yet: phase 1 has no broker
            client, by design.
          </p>
        </div>
      </div>
    </>
  );
}

function Stat({ label, value, icon, capitalize }: { label: string; value: string; icon: string; capitalize?: boolean }) {
  return (
    <div className="col-6 col-lg-3">
      <div className="card stat h-100">
        <div className="card-body">
          <div className="text-secondary small"><i className={"bi me-1 " + icon} />{label}</div>
          <div className={"value" + (capitalize ? " text-capitalize" : "")}>{value}</div>
        </div>
      </div>
    </div>
  );
}
