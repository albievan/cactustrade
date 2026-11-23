import { useState } from "react";
import { api, errorText, usd, type BrokerCheck } from "../lib/api";

// "Test connection": one read-only portfolio call to eToro with the current mode's keys.
export function BrokerTest() {
  const [res, setRes] = useState<BrokerCheck | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function run() {
    setBusy(true);
    setError("");
    try {
      setRes(await api.testBroker());
    } catch (e) {
      setRes(null);
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="card mb-3">
      <div className="card-body">
        <div className="d-flex flex-wrap align-items-center gap-2">
          <div className="me-auto">
            <div className="fw-semibold">Test the eToro connection</div>
            <div className="small text-secondary">One read-only call (portfolio) with the keys of the current mode. Nothing is traded.</div>
          </div>
          <button className="btn btn-outline-success" onClick={() => void run()} disabled={busy}>
            <i className="bi bi-plug me-1" />{busy ? "Testing…" : "Test connection"}
          </button>
        </div>
        {error && <div className="alert alert-danger mt-3 mb-0" role="alert">{error}</div>}
        {res && (res.ok ? (
          <div className="alert alert-success mt-3 mb-0" role="status">
            Connected to eToro <strong>{res.mode}</strong> in {res.latencyMs} ms. Available cash {usd(res.availableCashCents)},
            equity {usd(res.equityCents)}, {res.positions} open position(s).
            {!!res.nonConforming && <div className="text-danger mt-1">{res.nonConforming} position(s) are not real shares, long, leverage 1.</div>}
          </div>
        ) : (
          <div className="alert alert-warning mt-3 mb-0" role="alert">
            {res.configuredKeys ? <>eToro refused or did not answer: {res.error}</> : <>The eToro {res.mode} API key and user key are not both set.</>}
          </div>
        ))}
      </div>
    </div>
  );
}
