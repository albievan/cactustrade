import { money, type AccountHoldings } from "../lib/api";
import { dateTime } from "../lib/format";

// AccountFigures is an account tile's figures (R44): balance, the shares held long and the
// shorts with their value and open P&L, and the account's P&L, as the server counted and
// summed them (Holdings.summary) in the account's own currency. Presentation only.
export function AccountFigures({ h }: { h?: AccountHoldings | null }) {
  if (h === undefined) return <div className="small text-secondary mb-2">Figures loading…</div>;
  if (h === null || h.noKeys) return null;
  if (!h.holdings) return <div className="small text-warning mb-2">Figures: {h.error ?? "not available"}</div>;
  const x = h.holdings, s = x.summary, c = x.currency;
  const pnl = (v: number) => <span className={v < 0 ? "text-danger" : v > 0 ? "text-success" : ""}>{money(v, c)}</span>;
  return (
    <table className="table table-sm small mb-2 account-figures" aria-label={`Figures of ${h.account.name}`}>
      <tbody>
        <tr><th scope="row">Balance</th><td>{money(x.cash, c)} cash</td><td>{money(x.equity, c)} equity</td></tr>
        <tr>
          <th scope="row">Shares held (long)</th>
          <td>{s.longs} {s.longs === 1 ? "position" : "positions"}</td>
          <td>{money(s.longValue, c)} · P&L {pnl(s.longPnl)}</td>
        </tr>
        <tr>
          <th scope="row">Shorts held</th>
          <td>{s.shorts} {s.shorts === 1 ? "position" : "positions"}</td>
          <td>{money(s.shortValue, c)} · P&L {pnl(s.shortPnl)}</td>
        </tr>
        <tr>
          <th scope="row">P&L</th>
          <td>open {pnl(x.unrealisedPnl)}</td>
          <td>{x.realisedPnl !== 0 && <>realised {pnl(x.realisedPnl)}</>}</td>
        </tr>
      </tbody>
      <caption className="pt-1 pb-0">
        As of {dateTime(x.asOf)}{s.unknownValues > 0 && ` · ${s.unknownValues} without a value reported by the platform (not in the sums)`}
      </caption>
    </table>
  );
}
