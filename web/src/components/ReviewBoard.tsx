import { Fragment, useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, errorText, type Review, type ReviewBoard as Board } from "../lib/api";

const OUTCOME: Record<Review["outcome"], { label: string; cls: string }> = {
  added: { label: "added to watchlist", cls: "text-bg-success" },
  candidate: { label: "signal (on watchlist)", cls: "text-bg-primary" },
  skipped: { label: "signal, not added", cls: "text-bg-warning" },
  no_signal: { label: "no signal", cls: "text-bg-secondary" },
  error: { label: "no data", cls: "text-bg-danger" },
};

// "Under review": every stock the scanner looked at for this account, when, the market
// regime, and each rule it was checked against with the result.
export function ReviewBoard() {
  const [board, setBoard] = useState<Board | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    api.reviews(100).then(setBoard).catch((e) => setError(errorText(e)));
  }, []);
  useEffect(() => {
    load();
    const t = window.setInterval(load, 60_000);
    return () => window.clearInterval(t);
  }, [load]);
  return (
    <div className="card mb-4">
      <div className="card-body">
        <div className="d-flex align-items-center gap-2 mb-2">
          <h2 className="h5 mb-0 me-auto">Under review</h2>
          {board && <span className={"badge " + (board.scannerEnabled ? "text-bg-success" : "text-bg-secondary")}>scanner {board.scannerEnabled ? "on" : "off"}</span>}
          <Link className="btn btn-sm btn-outline-secondary" to="/themes">Themes</Link>
        </div>
        {error && <p className="text-secondary mb-0">{error}</p>}
        {board && board.reviews.length === 0 && (
          <p className="text-secondary mb-0">Nothing reviewed yet. Add a theme (e.g. Space, Semiconductors) under <Link to="/themes">Themes</Link>; the scanner runs every 15 minutes while a market is open, or press "Scan now" there.</p>
        )}
        {board && board.reviews.length > 0 && (
          <div className="table-responsive">
            <table className="table table-sm align-middle mb-0">
              <thead><tr><th>Stock</th><th>Theme</th><th>Reviewed</th><th>Regime</th><th>Outcome</th><th>Rules</th></tr></thead>
              <tbody>
                {board.reviews.map((r) => {
                  const p = r.probs ?? {};
                  const pass = r.checks.filter((c) => c.pass).length;
                  return (
                    <Fragment key={r.instrumentId}>
                      <tr onClick={() => setOpen(open === r.instrumentId ? null : r.instrumentId)} style={{ cursor: "pointer" }}>
                        <td className="fw-semibold">{r.symbol}{r.direction && <span className={"badge ms-1 " + (r.direction === "short" ? "text-bg-danger" : "text-bg-success")}>{r.direction}</span>}</td>
                        <td className="small">{r.theme}</td>
                        <td className="small">{new Date(r.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</td>
                        <td title={`trending ${pct(p.trending)} · choppy ${pct(p.choppy)} · high volatility ${pct(p.high_volatility)}`}>
                          <div className="probbar">
                            <div className="t" style={{ width: pct(p.trending) }} /><div className="c" style={{ width: pct(p.choppy) }} /><div className="h" style={{ width: pct(p.high_volatility) }} />
                          </div>
                        </td>
                        <td><span className={"badge " + OUTCOME[r.outcome].cls}>{OUTCOME[r.outcome].label}</span></td>
                        <td className="small">
                          {r.checks.map((c, i) => (
                            <i key={i} className={"bi me-1 " + (c.pass ? "bi-check-circle-fill rule-pass" : "bi-x-circle rule-fail")} title={c.rule + (c.detail ? ": " + c.detail : "")} />
                          ))}
                          <span className="text-secondary">{pass}/{r.checks.length}</span>
                        </td>
                      </tr>
                      {open === r.instrumentId && (
                        <tr>
                          <td colSpan={6} className="bg-body-tertiary small">
                            <div className="mb-1">{r.summary}</div>
                            <ul className="list-unstyled mb-0">
                              {r.checks.map((c, i) => (
                                <li key={i}><i className={"bi me-1 " + (c.pass ? "bi-check-circle-fill rule-pass" : "bi-x-circle rule-fail")} />{c.rule}{c.detail && <span className="text-secondary"> — {c.detail}</span>}</li>
                              ))}
                            </ul>
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <p className="small text-secondary mt-2 mb-0">Click a row for every rule and its value. Reviews only read prices; nothing is traded here.</p>
      </div>
    </div>
  );
}

function pct(v: number | undefined): string {
  return `${Math.round((v ?? 0) * 100)}%`;
}
