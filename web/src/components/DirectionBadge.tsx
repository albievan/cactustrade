// DirectionBadge shows an order's or position's direction: long (the share itself, a buy) or
// short (a CFD sold short). A close or cancel carries the direction of what it acts on.
export function DirectionBadge({ direction }: { direction: string }) {
  if (direction === "long") return <span className="badge text-bg-success" title="long: the share itself (buy)">Long</span>;
  if (direction === "short") return <span className="badge text-bg-danger" title="short: a CFD sold short">Short</span>;
  return <span>—</span>;
}
