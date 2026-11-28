# ADR-008: Trading 212 execution (practice accounts only)

**Status:** accepted 2026-11-28 (PLAN R45; the owner's decisions D41–D44). Real Trading 212
accounts stay view-only.

**Context.** The owner asked to trade Trading 212 accounts (R45). Trading 212's public API (v0,
beta) cannot meet the guardrails of `CLAUDE.md` §2 the way eToro's does: an order has no client
request id and "the same request sent again may duplicate the order"; there are no shorts or
CFDs; a market order carries no stop-loss; the API publishes no market data; it documents no
minimum order value. The owner agreed the rules a–h in writing (D41), chose how instruments are
matched without an ISIN on the eToro side (D42), the CLAUDE.md wording with fractions behind a
switch (D43) and the defaults (D44). This ADR records how cactusTrade trades under those rules.

**Decision (T212-a … T212-q).**
- **a – Its own port.** `ports.T212Broker` (account, catalogue, open orders, one order,
  history, `Place`, `Cancel`), not `ports.Broker`: Trading 212 works by ticker and share
  quantity, takes the stop as its own order and has no position id or request id.
- **b – Its own app path.** `t212_executor.go`, `t212_agent.go`, `t212_supervisor.go`,
  `t212_reconcile.go`; the eToro executor, agent and supervisor are unchanged (real money runs
  on them). Shared: the risk engine, strategy, review, sessions, decision log, kill state and
  the orders and positions repositories.
- **c – Who trades.** `Account.T212Trading()` (Trading 212 and demo) and `Agentable()`
  (eToro, or a Trading 212 practice account); `agentAccount` resolves the use cases that serve
  both. Scanner, web research, the theme LLM, the watchlist import, trade minimums, autonomy
  auto-start, the real gate and the trade-history sync stay eToro-only.
- **d – Idempotency without a request id.** Our request id is stored (created, then sent)
  before every request, as on eToro, but Trading 212 never sees it. A buy, a stop and a sale are
  sent **once** and never again; a cancel up to three times (it cannot add exposure). A lost
  answer (transport error, timeout, 408, 429, 5xx, an unreadable answer) leaves the order
  *unknown*: entries on the account wait until it is matched in the open orders and the history
  (ticker, side, type, quantity, created within [send − 5 s, send + 2 min], initiated from the
  API, not claimed by another order of ours). One candidate is adopted, none after 2 minutes is
  abandoned for a buy (a stop or a sale stays unknown for the owner, T212-s), two or more wait
  for the owner (`POST /orders/{id}/resolve`, audited).
- **e – Quantities.** Whole shares unless the account's fractional switch is on; the switch is
  refused while `domain.T212FractionsVerified` is false (a reviewed code change after the
  practice run, R45.13). Quantities are truncated, never rounded up.
- **f – Currency.** Only instruments in the account's currency (or GBX on a GBP account) are
  traded; nothing is converted. Budget, limits, minimum order, reservations, events and the UI
  are in the account's currency.
- **g – Sizing.** quantity = truncate(size ÷ (ask × 1.02)); reservation = ceil(quantity × ask
  × 1.02) ≤ min(fresh `availableToTrade` − reserved, remaining budget). The 2 % buffer equals the
  fill tolerance (j), so any fill that is kept stays within the reservation (review d).
- **h – Stop.** A GOOD_TILL_CANCEL sell stop for the unsold quantity right after each fill,
  confirmed in the open orders within 30 s; refused, unconfirmed or missing later → sold at
  market (in session) and the instrument blocked. Entries wait while any position of ours has no
  confirmed stop. When Trading 212 lists a fresh fill late, the stop waits (bounded by
  `FillVisibleAfter`) instead of being refused (review B2).
- **i – Close; only what cactusTrade bought is sold (D44).** Cancel the stop, confirm it is gone,
  read the position again, read the positions fresh, sell at market
  `T212Sellable(bought, sold, quantityAvailableForTrading, our resting sells)`. *Sold so far* is
  the sum of the filled quantity of the position's own stops and sales, stored with each order's
  state in one write (`orders.filled_micros`, migration 00017), so a sale after a partly filled
  one never reaches the owner's own shares in the ticker. A stop that filled only in part is not
  "fired": its fill counts and the rest is sold (review B1). One exit at a time per account
  (`t212ExitLock`, review MAJOR); a sale stores only its request id on the position, so a stale
  copy cannot reopen a closed position (review B3). Entries are refused in a ticker the account
  already holds (also on the fresh figures just before the buy, review c).
- **j – Fill check.** Something filled, no more than sent, within 200 bp of the eToro entry
  quote; otherwise sold at once and the instrument blocked.
- **k – Cost gate.** LSE stamp duty (50 bp; the PTM levy over £10 000) plus twice the eToro
  spread must stay below the gross gain at the take-profit.
- **l – Prices.** An eToro **demo** account of the same owner, chosen by the owner, feeds
  signals, entry and stop prices (D44: at the owner's risk). Missing or invalid → no entries;
  exits never need it.
- **m – Minimum order value.** Required on Trading 212 (> 0, ≤ budget; 0 on eToro); the short
  cap is 0 (long only).
- **n – Real Trading 212 refused everywhere.** The domain (`T212Trading`, `T212IntentFor`,
  `NewOrder`), the adapter (before keys or I/O, whatever permit the context carries), the
  executor, the agent, the supervisor, reconciliation, start, the views and the probe.
- **o – Market orders only in session.** A market order sent while the market is closed would
  be queued; it is never sent then. An exit due out of session keeps (or places) the stop and
  raises an alert.
- **p – Matching (D42).** Market and ticker suffix (`_US_EQ`, `l_EQ`), symbol = `shortName`,
  currency and type STOCK/ETF agree, exactly one candidate; the owner confirms each match once
  on the watchlist, seeing both names and Trading 212's ISIN.
- **q – Version 1 scope.** No scanner, auto-add or auto-start; a person adds shares and starts
  the agent. Realised P&L history is a follow-up.
- **A stuck sale (review a).** A sale still working after 60 s is cancelled (at most three
  cancels, one in flight) and, once it ended unfilled, sent again — a new order with its own
  request id, sized again by the rule of i, never a resend of the same one. When no cancel is
  left, nothing more is sent for it and a danger event ("order.sale_stuck") asks a person to act
  before the close.

- **s – Listed nowhere is not gone (re-review 2026-11-28).** Trading 212 may list a finished
  order or a fresh fill late (T4, T5b unverified). A stop or a sale of ours becomes final only on
  an answer of Trading 212's that gives its filled quantity; one it lists neither as active nor
  in its history stays as it is (a cancelled-looking stop may have fired: a sale then would sell
  the owner's shares), the sale waits, at most three cancels are sent for one stop, and a stop or
  a sale whose lost answer matches nothing after 2 min stays unknown (only a buy is abandoned
  then). A position of ours Trading 212 does not list 2 min after its fill is never settled as
  gone: only a recorded stop or sale fill (its Sold), or its owner, closes it. Meanwhile the
  account is not reconciled (no entries; exits run), and a danger event names it
  ("order.unlisted" after 2 min, "order.unmatched", "position.unlisted"). The owner settles such
  a position on the orders page (`POST /orders/{requestId}/settle` on its buy: web session,
  audited; refused while a stop or sale of ours still works at Trading 212 or waits to be
  resolved, or while Trading 212 lists the position and every order of ours on it): closed in
  cactusTrade's records only, its unlisted stop or sale abandoned, nothing sent, nothing of it
  ever sold afterwards. "Not placed" for an unknown order is refused while Trading 212 lists any
  order that could be it. `t212Vanished`, the supervisor's stop placement and `ResolveT212Order`
  take the account's exit lock. A fresh fill a kill or force close cannot sell yet (not listed)
  gets its stop first.

**Consequences.**
- A position Trading 212 never lists again (sold by hand outside cactusTrade, or a fill whose
  listing never comes) blocks entries on the account until its owner settles it (T212-s).
- The adapter's allowlist holds exactly nine method + path pairs (`TestAllowlistExact`); no
  money, pies, limit or stop-limit path exists.
- A lost answer can block entries until it is matched or resolved by the owner; exits continue.
- Between a fill and its confirmed stop (about 30 s) a position has no stop, and a triggered
  stop is a market order (slippage).
- A wrong match despite D42 is caught only by the owner's confirmation and the 200 bp fill check.
- The practice run (R45.13) settles what the documentation leaves open (T2b, T4, T5b, T6b, T7,
  T8, T11–T13); until then the code fails closed on each.
