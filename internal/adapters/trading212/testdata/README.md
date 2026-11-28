# Trading 212 fixtures

Synthetic responses shaped like the Trading 212 Public API v0 schemas (docs.trading212.com,
verified 2026-11-23 and 2026-11-28). No real account data, keys or ids.

- `summary.json`, `positions.json`: the account reads (view-only, and the practice account).
- `order_market.json`, `order_stop.json`, `order_sell.json`: the answers to the three order
  shapes (PLAN R45.8); `orders.json`, `order.json`: the active orders and one order;
  `history_orders_p1.json` (with `nextPagePath`) and `history_orders_p2.json` (the last page):
  the order history with fills and taxes; `instruments.json`: the instrument metadata.

The order fixtures are replaced by sanitised recordings of the owner's practice run (R45.13).
