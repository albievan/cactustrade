import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ReviewBoard } from "../components/ReviewBoard";
import { api, errorText, usd, type MarketSession, type Portfolio, type SystemStatus } from "../lib/api";

export function Dashboard() {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [markets, setMarkets] = useState<MarketSession[]>([]);
  const [portfolio, setPortfolio] = useState<Portfolio | null>(null);
  const [pfError, setPfError] = useState("");
  useEffect(() => {
    api.status().then(setStatus).catch(() => undefined);
    api.marketStatus().then(setMarkets).catch(() => undefined);
    api.portfolio().then(setPortfolio).catch((e) => setPfError(errorText(e)));
  }, []);
  const noAccount = status !== null && !status.account;
  return (
    <>
      <h1 className="h3 mb-4">Dashboard</h1>
      <div className="row g-3 mb-4">
        <Stat label="Agent" value={status?.agentState ?? "…"} icon="bi-robot" capitalize />
        <Stat label="Mode" value={status?.mode ?? "…"} icon="bi-shield-check" capitalize />
        <Stat label="Available cash" value={portfolio ? usd(portfolio.availableCashCents) : "—"} icon="bi-cash-coin" />
        <Stat label="Equity" value={portfolio ? usd(portfolio.equityCents) : "—"} icon="bi-graph-up" />
      </div>
      {noAccount && (
        <div className="alert alert-warning">
          You have no eToro account yet. Add one under <Link to="/accounts">eToro accounts</Link>, or start cactusTrade
          with <code>ETORO_DEMO_PUB</code> and <code>ETORO_DEMO_PK</code> (the first admin then gets "Demo (environment)").
        </div>
      )}
      <div className="row g-3 mb-4">
        {markets.map((m) => (
          <div className="col-md-6" key={m.market}>
            <div className="card h-100">
              <div className="card-body">
                <div className="d-flex justify-content-between">
                  <span className="fw-semibold">{m.market === "LSE" ? "London (LSE)" : m.market === "ASX" ? "Sydney (ASX)" : "New York (NYSE / NASDAQ)"}</span>
                  <span className={"badge " + (m.open ? "text-bg-success" : "text-bg-secondary")}>{m.open ? "open" : "closed"}</span>
                </div>
                <div className="small text-secondary mt-1">
                  {m.trading && m.opens && m.closes
                    ? <>Today {time(m.opens)}–{time(m.closes)} (your time)</>
                    : m.note ? m.note : "Not trading today"}
                </div>
              </div>
            </div>
          </div>
        ))}
      </div>
      <ReviewBoard />
      <div className="card">
        <div className="card-body">
          <h2 className="h5">Open positions</h2>
          {pfError && <p className="text-secondary mb-0">{pfError}</p>}
          {portfolio && portfolio.positions.length === 0 && <p className="text-secondary mb-0">No open positions.</p>}
          {portfolio && portfolio.positions.length > 0 && (
            <div className="table-responsive">
              <table className="table table-sm align-middle mb-0">
                <thead><tr><th>Position</th><th>Instrument</th><th>Opened</th><th className="text-end">Rate</th><th className="text-end">Units</th><th className="text-end">Invested</th><th className="text-end">P&amp;L</th><th /></tr></thead>
                <tbody>
                  {portfolio.positions.map((p) => (
                    <tr key={p.positionId}>
                      <td>{p.positionId}</td>
                      <td>{p.instrumentId}</td>
                      <td>{new Date(p.openedAt).toLocaleString()}</td>
                      <td className="text-end">{p.openRate}</td>
                      <td className="text-end">{p.units}</td>
                      <td className="text-end">{usd(p.amountCents)}</td>
                      <td className={"text-end " + (p.unrealisedPnlCents < 0 ? "text-danger" : "text-success")}>{usd(p.unrealisedPnlCents)}</td>
                      <td>{!(p.realAsset && p.long && p.leverage === 1) && <span className="badge text-bg-danger">not a real long x1</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <p className="small text-secondary mt-3 mb-0">
            The agent cannot trade yet: orders arrive in phase 4 (demo first). This page only reads the account.
          </p>
        </div>
      </div>
    </>
  );
}

function time(iso: string) {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
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
