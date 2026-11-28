import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { EquityCharts } from "../components/EquityChart";
import { oneOf, usePref } from "../lib/prefs";
import { api, errorText, money, platformBadge, platformLabel, type AccountHoldings, type EquitySeries, type Overview as OverviewData } from "../lib/api";

// Every account the user can view, on every platform: totals per currency, one card per
// account, an equity chart and every open position. Read-only: nothing here places an order.

const RANGES: { label: string; days: number }[] = [
  { label: "1D", days: 1 }, { label: "1W", days: 7 }, { label: "1M", days: 30 }, { label: "3M", days: 90 }, { label: "All", days: 400 },
];
const isRange = oneOf(RANGES.map((r) => r.days));

export function Overview() {
  const [data, setData] = useState<OverviewData | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [days, setDays] = usePref<number>("overview.days", 30, isRange); // R50
  const [series, setSeries] = useState<EquitySeries[] | null>(null);
  const [chartError, setChartError] = useState("");
  const load = useCallback(() => {
    setLoading(true);
    setError("");
    api.overview().then(setData).catch((e) => setError(errorText(e))).finally(() => setLoading(false));
  }, []);
  useEffect(load, [load]);
  useEffect(() => {
    setChartError("");
    api.equityHistory(days).then(setSeries).catch((e) => setChartError(errorText(e)));
  }, [days]);

  const rows = (data?.accounts ?? []).flatMap((a) => (a.holdings?.positions ?? []).map((p, i) => ({ a, p, key: `${a.account.id}-${i}` })));
  return (
    <>
      <div className="d-flex align-items-center gap-2 mb-1">
        <h1 className="h3 mb-0">Overview</h1>
        <button className="btn btn-sm btn-outline-secondary ms-auto" onClick={load} disabled={loading}>
          <i className="bi bi-arrow-clockwise me-1" />{loading ? "Loading…" : "Refresh"}
        </button>
      </div>
      <p className="text-secondary">
        All your accounts on every platform, read now. Trading 212, Saxo and Tiger Brokers accounts are view-only: cactusTrade
        does not trade them. A position shown as <em>unknown</em> is one whose value the platform did not report in the
        account currency (cactusTrade never guesses or converts it).
      </p>
      {error && <div className="alert alert-danger" role="alert">{error}</div>}

      {data && data.totals.length > 0 && (
        <>
          <div className="row g-3 mb-1">
            {data.totals.map((t) => (
              <div className="col-md-6 col-xl-4" key={t.currency}>
                <div className="card h-100 stat"><div className="card-body">
                  <div className="text-secondary small">Total in {t.currency} · {t.accounts} account{t.accounts === 1 ? "" : "s"}</div>
                  <div className="value">{money(t.equity, t.currency)}</div>
                  <div className="small">Cash {money(t.cash, t.currency)} · P&amp;L <Pnl v={t.unrealisedPnl} cur={t.currency} /></div>
                </div></div>
              </div>
            ))}
          </div>
          <p className="small text-secondary mb-4">Amounts in different currencies are not added up.</p>
        </>
      )}

      {data && data.accounts.length === 0 && (
        <div className="alert alert-warning">You have no accounts yet. Add one on the <Link to="/accounts">Platform accounts</Link> page.</div>
      )}
      <div className="row g-3 mb-4">
        {data?.accounts.map((a) => <AccountCard key={a.account.id} a={a} />)}
      </div>

      <div className="card mb-4"><div className="card-body">
        <div className="d-flex flex-wrap align-items-center gap-2 mb-3">
          <h2 className="h5 mb-0">Equity over time</h2>
          <div className="btn-group btn-group-sm ms-auto" role="group" aria-label="Chart range">
            {RANGES.map((r) => (
              <button key={r.label} type="button" className={"btn " + (days === r.days ? "btn-success" : "btn-outline-secondary")}
                aria-pressed={days === r.days} onClick={() => setDays(r.days)}>{r.label}</button>
            ))}
          </div>
        </div>
        {chartError && <p className="text-secondary mb-0">{chartError}</p>}
        {!chartError && series && <EquityCharts series={series} days={days} />}
      </div></div>

      <div className="card"><div className="card-body">
        <h2 className="h5">Open positions</h2>
        {data && rows.length === 0 && <p className="text-secondary mb-0">No open positions.</p>}
        {rows.length > 0 && (
          <div className="table-responsive">
            <table className="table table-sm align-middle mb-0">
              <thead><tr>
                <th>Account</th><th>Platform</th><th>Symbol</th><th>Name</th><th className="text-end">Quantity</th>
                <th className="text-end">Avg price</th><th className="text-end">Price</th><th className="text-end">Value</th>
                <th className="text-end">P&amp;L</th><th>Opened</th>
              </tr></thead>
              <tbody>
                {rows.map(({ a, p, key }) => {
                  const cur = a.holdings?.currency ?? "";
                  return (
                    <tr key={key}>
                      <td>{a.account.name}</td>
                      <td><PlatformBadge platform={a.account.platform} /></td>
                      <td className="mono">{p.symbol}{!p.long && <span className="badge text-bg-warning ms-1">short</span>}</td>
                      <td>{p.name}</td>
                      <td className="text-end">{p.quantity}</td>
                      <td className="text-end">{p.avgPrice} <span className="text-secondary small">{p.currency}</span></td>
                      <td className="text-end">{p.currentPrice === "0" ? "—" : <>{p.currentPrice} <span className="text-secondary small">{p.currency}</span></>}</td>
                      <td className="text-end">{p.unknown ? <Unknown /> : money(p.value, cur)}</td>
                      <td className="text-end">{p.unknown ? <Unknown /> : <Pnl v={p.pnl} cur={cur} />}</td>
                      <td className="small text-secondary">{p.openedAt ? new Date(p.openedAt).toLocaleDateString() : "—"}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div></div>
    </>
  );
}

function Pnl({ v, cur }: { v: number; cur: string }) {
  return <span className={v > 0 ? "text-success" : v < 0 ? "text-danger" : ""}>{v > 0 ? "+" : ""}{money(v, cur)}</span>;
}

function Unknown() {
  return <span className="text-secondary fst-italic" title="The platform did not report this in the account currency">unknown</span>;
}

function PlatformBadge({ platform }: { platform: AccountHoldings["account"]["platform"] }) {
  return <span className={"badge " + platformBadge[platform]}>{platformLabel[platform]}</span>;
}

function AccountCard({ a }: { a: AccountHoldings }) {
  const h = a.holdings;
  const real = a.account.mode === "real";
  return (
    <div className="col-md-6 col-xl-4">
      <div className={"card h-100 " + (real ? "border-danger" : "")}><div className="card-body">
        <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
          <h2 className="h6 mb-0 me-auto">{a.account.name}</h2>
          <PlatformBadge platform={a.account.platform} />
          <span className={"badge " + (real ? "text-bg-danger" : "text-bg-info")}>{real ? "real" : "demo"}</span>
        </div>
        {h && (
          <dl className="row small mb-0">
            <dt className="col-6 fw-normal text-secondary">Equity</dt><dd className="col-6 text-end fw-semibold">{money(h.equity, h.currency)}</dd>
            <dt className="col-6 fw-normal text-secondary">Cash</dt><dd className="col-6 text-end">{money(h.cash, h.currency)}</dd>
            <dt className="col-6 fw-normal text-secondary">Unrealised P&amp;L</dt><dd className="col-6 text-end"><Pnl v={h.unrealisedPnl} cur={h.currency} /></dd>
            <dt className="col-6 fw-normal text-secondary">Positions</dt><dd className="col-6 text-end mb-0">{h.positions.length}</dd>
          </dl>
        )}
        {h && h.positions.some((p) => p.unknown) && (
          <p className="small text-secondary mt-2 mb-0">Some positions have no value in {h.currency} from the platform: shown as unknown.</p>
        )}
        {!h && a.noKeys && (
          <p className="small mb-0 text-secondary">No keys yet — add them on the <Link to="/accounts">Platform accounts</Link> page.</p>
        )}
        {!h && a.reconnect && (
          <p className="small mb-0 text-warning-emphasis"><i className="bi bi-box-arrow-in-right me-1" />{a.error || `Reconnect to ${platformLabel[a.account.platform]}`}.{" "}
            <Link to="/accounts">Connect again</Link></p>
        )}
        {!h && !a.noKeys && !a.reconnect && (
          <p className="small mb-0 text-danger"><i className="bi bi-exclamation-triangle me-1" />{a.error || "Could not be read."}{" "}
            <Link to="/accounts">Check the account</Link></p>
        )}
      </div></div>
    </div>
  );
}
