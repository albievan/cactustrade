import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ReviewBoard } from "../components/ReviewBoard";
import { AgentPanel } from "../components/AgentPanel";
import { ActivityLog } from "../components/ActivityLog";
import { InfoCard } from "../components/InfoCard";
import { LimitBars, LiveEnded, LivePositions, LiveStats, LiveStatus, OrderFeed, RegimeList } from "../components/LiveCards";
import { api, type Account, type MarketSession, type SystemStatus } from "../lib/api";
import { useLive } from "../lib/live";

// The dashboard of the current account. Its figures, positions, orders and regimes come from
// the live stream (GET /api/v1/live, PLAN 6.11); the agent's controls are the agent panel's
// own REST calls, so the kill switch works while the stream is down.
export function Dashboard() {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [markets, setMarkets] = useState<MarketSession[]>([]);
  useEffect(() => {
    api.status().then(setStatus).catch(() => undefined);
    api.marketStatus().then(setMarkets).catch(() => undefined);
  }, []);
  const noAccount = status !== null && !status.account;
  return (
    <>
      <h1 className="h3 mb-4">Dashboard</h1>
      {noAccount && (
        <div className="alert alert-warning">
          You have no eToro account yet. Add one under <Link to="/accounts">eToro accounts</Link>, or start cactusTrade
          with <code>ETORO_DEMO_PUB</code> and <code>ETORO_DEMO_PK</code> (the first admin then gets "Demo (environment)").
        </div>
      )}
      {status?.account && <LiveDashboard key={status.account.id} account={status.account} />}
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
    </>
  );
}

// LiveDashboard follows one account's stream (keyed by the account: a switch starts afresh).
function LiveDashboard({ account }: { account: Account }) {
  const live = useLive(account.id);
  const s = live.snapshot;
  return (
    <>
      <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
        <span className="small text-secondary">Live view of <strong>{account.name}</strong></span>
        <LiveStatus live={live} />
      </div>
      <LiveEnded reason={live.endedReason} />
      <LiveStats snapshot={s} />
      <ActivityLog account={account} live={live} />
      <AgentPanel live={live} />
      <div className="row g-3 mb-4">
        <div className="col-xl-6"><LimitBars figures={s?.figures} accountId={account.id} /></div>
        <div className="col-xl-6"><OrderFeed orders={live.orders} /></div>
      </div>
      <LivePositions snapshot={s} />
      <InfoCard id="dashboard-live" title="How the live view works">
        <ul className="small mb-0">
          <li>The figures arrive every 5 seconds from cactusTrade's latest read of the account, and at once when an
            order or the agent changes. Nothing here places or changes an order.</li>
          <li><strong>stale</strong> means no update for 30 seconds, or a portfolio older than 30 seconds: the figures
            may be out of date.</li>
          <li>When the stream fails three times the page refreshes every 15 seconds instead and keeps retrying. The
            kill switch and the agent's buttons never depend on the stream.</li>
          <li>A tab hidden for a minute closes the stream; it reopens when you come back.</li>
          <li><strong>Agent activity</strong> lists the newest first: on opening, the latest stored events and
            decisions; then each round of the agent (every 5 minutes while it runs; rounds are shown live only and
            are not kept), each idea with the reviewer's and the risk engine's answer, orders, fills and closes.
            Pause keeps the list still while you read; at most 300 entries are kept on the page.</li>
        </ul>
      </InfoCard>
      <RegimeList regimes={s?.regimes ?? []} legend={s?.regimeLegend ?? []} />
    </>
  );
}

function time(iso: string) {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}
