package domain

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"
	"time"
)

// Trading 212 execution (PLAN R45; owner's decisions D41–D44). Trading 212 trades practice
// (demo) accounts only, long only, in three order shapes: a market buy, a sell stop of a
// quantity cactusTrade holds and a market sell of a quantity cactusTrade holds (CLAUDE.md
// §2.3). Prices are in the instrument's currency (micros); money is in the account's minor
// units (Cents of the account currency, never converted, T212-f).

// T212FractionsVerified records whether Trading 212's fraction and stop rules (T8) have been
// verified on the owner's practice account (R45.13). Until a reviewed edit sets it, the
// fractional switch cannot be turned on and every quantity is whole shares (D43).
const T212FractionsVerified = false

// T212QuantityDecimals is the number of decimals of a fractional quantity, set together with
// T212FractionsVerified (0 = whole shares).
const T212QuantityDecimals = 0

// Trading 212 constants of PLAN R45.
const (
	// T212SizeBufferBp: a quantity is sized at the ask plus 1 %, and its reservation is the
	// quantity at the ask plus 1 % (T212-g), so a market buy that fills a little higher is
	// still paid for.
	T212SizeBufferBp BasisPoints = 100
	// T212FillToleranceBp: a fill more than 2 % from the eToro entry quote closes the
	// position at once and blocks the instrument (D42, T212-j).
	T212FillToleranceBp BasisPoints = 200
	// T212StampDutyBp: UK stamp duty on a London buy (T212-k).
	T212StampDutyBp BasisPoints = 50
	// T212PTMLevy is the UK PTM levy, counted on a London order over T212PTMThreshold
	// (account minor units).
	T212PTMLevy      Cents = 150
	T212PTMThreshold Cents = 1_000_000
	// T212MatchBefore and T212MatchAfter bound when an order Trading 212 lists may be ours:
	// created from 5 s before our send to 2 minutes after it (T212-d).
	T212MatchBefore = 5 * time.Second
	T212MatchAfter  = 2 * time.Minute
	// T212InitiatedFromAPI is Trading 212's initiatedFrom of an order sent through the API:
	// only such an order can be ours.
	T212InitiatedFromAPI = "API"
)

// wholeShare is one share in Micros.
const wholeShare Micros = 1_000_000

// T212QuantityStep is the smallest quantity step: one share, or 10^-T212QuantityDecimals of
// a share when fractional is asked for and fractions are verified.
func T212QuantityStep(fractional bool) Micros {
	if !fractional || !T212FractionsVerified {
		return wholeShare
	}
	step := wholeShare
	for range min(max(T212QuantityDecimals, 0), 6) {
		step /= 10
	}
	return step
}

// T212Side is the side of a Trading 212 order.
type T212Side string

// Sides.
const (
	T212Buy  T212Side = "buy"
	T212Sell T212Side = "sell"
)

// T212OrderType is the type of a Trading 212 order. cactusTrade sends market and stop orders
// only; other types it may read (limit, stop_limit) never match an order of ours.
type T212OrderType string

// Order types.
const (
	T212Market    T212OrderType = "market"
	T212StopOrder T212OrderType = "stop"
)

// T212Intent is what cactusTrade asks Trading 212 to do: one of exactly three shapes (a market
// buy, a sell stop, a market sell), each of a positive quantity. It carries no leverage, no
// value and no extended-hours flag: the adapter's bodies are constants (CLAUDE.md §2.3). It
// is built only by T212IntentFor.
type T212Intent struct {
	RequestID string // our persisted order row (Trading 212 never sees it)
	Ticker    string
	Quantity  Micros // > 0; the adapter negates it for a sell
	Side      T212Side
	Type      T212OrderType
	StopPrice Micros // the sell stop's price; 0 for a market order
	// Fractional: the quantity may be fractional (the account's switch is on and fractions
	// are verified). Otherwise the adapter refuses any fraction.
	Fractional bool
}

// t212Shape is the side and type of an order kind on Trading 212.
func t212Shape(k OrderKind) (T212Side, T212OrderType, bool) {
	switch k {
	case OrderOpen:
		return T212Buy, T212Market, true
	case OrderStop:
		return T212Sell, T212StopOrder, true
	case OrderClose:
		return T212Sell, T212Market, true
	}
	return "", "", false
}

// validateT212 checks a Trading 212 order: practice accounts only, long only; an open needs
// the matched instrument, ticker, quantity, amount, entry and a stop below the entry; a stop
// needs the position, ticker, quantity and stop price; a close needs the position, ticker and
// quantity; a cancel needs the order to cancel.
func (o Order) validateT212() error {
	switch {
	case o.Mode != ModeDemo:
		return fmt.Errorf("order %s: Trading 212 trades practice accounts only: %w", o.RequestID, ErrRealModeGated)
	case o.Direction == Short:
		return Invalid("direction", "long only on Trading 212 (no shorts)")
	case o.Kind != OrderCancel && o.Direction != Long:
		return Invalid("direction", "long only on Trading 212")
	case o.Quantity < 0:
		return Invalid("quantity", "must not be negative")
	}
	switch o.Kind {
	case OrderOpen:
		if o.Ticker == "" || o.Quantity <= 0 {
			return Invalid("quantity", "a Trading 212 buy needs a ticker and a quantity")
		}
		return validEntry(Long, o.Instrument, o.Amount, o.Entry, o.StopLoss, o.TakeProfit)
	case OrderStop:
		if o.PositionID <= 0 || o.Ticker == "" || o.Quantity <= 0 || o.StopLoss <= 0 {
			return Invalid("stopLoss", "a Trading 212 stop needs the position, ticker, quantity and stop price")
		}
	case OrderClose:
		if o.PositionID <= 0 || o.Ticker == "" || o.Quantity <= 0 {
			return Invalid("quantity", "a Trading 212 sell needs the position, ticker and quantity")
		}
	case OrderCancel:
		if o.TargetOrderID <= 0 {
			return Invalid("targetOrderId", "a cancel needs Trading 212's order id")
		}
	}
	return nil
}

// T212IntentFor builds the Trading 212 request for an order of ours. It is the only place a
// T212Intent is built. It refuses anything but a valid buy, stop or sell of a Trading 212
// practice account that is created or sent: a real-money account (ErrRealModeGated, D41 g),
// a short, a sell (stop or market) of more than held — the quantity cactusTrade holds in the
// ticker (D44) — and a fraction unless fractional is on and T212FractionsVerified (D43).
func T212IntentFor(o Order, held Micros, fractional bool) (T212Intent, error) {
	switch {
	case o.OnPlatform() != PlatformTrading212:
		return T212Intent{}, Invalid("platform", "only a Trading 212 order has a Trading 212 intent")
	case o.Mode != ModeDemo:
		return T212Intent{}, fmt.Errorf("order %s: Trading 212 trades practice accounts only: %w", o.RequestID, ErrRealModeGated)
	case o.Direction != Long:
		return T212Intent{}, Invalid("direction", "long only on Trading 212 (no shorts)")
	case o.State != OrderCreated && o.State != OrderSent:
		return T212Intent{}, fmt.Errorf("order %s is %s: %w", o.RequestID, o.State, ErrIllegalTransition)
	}
	if err := o.validate(); err != nil {
		return T212Intent{}, err
	}
	side, typ, ok := t212Shape(o.Kind)
	if !ok {
		return T212Intent{}, Invalid("kind", "only a buy, a stop or a sell has a Trading 212 intent")
	}
	frac := fractional && T212FractionsVerified
	if step := T212QuantityStep(frac); o.Quantity%step != 0 {
		return T212Intent{}, Invalid("quantity", "whole shares only (fractions stay off until they are verified and switched on)")
	}
	if side == T212Sell && o.Quantity > held {
		return T212Intent{}, Invalid("quantity", fmt.Sprintf("a sell of %s is more than the %s cactusTrade holds (no short)", o.Quantity, max(held, 0)))
	}
	in := T212Intent{RequestID: o.RequestID, Ticker: o.Ticker, Quantity: o.Quantity, Side: side, Type: typ, Fractional: frac}
	if typ == T212StopOrder {
		in.StopPrice = o.StopLoss
	}
	return in, nil
}

// T212PriceScale is how many account minor units one unit of the instrument's price currency
// is (T212-f): 100 for USD on a USD account and GBP on a GBP account, 1 for GBX (pence) on a
// GBP account. Trading 212 trades only in the account's primary currency, so anything else
// is refused (USD on a GBP account, any EUR account: nothing is converted).
func T212PriceScale(instrumentCurrency, accountCurrency string) (int64, error) {
	ic, ac := NormCurrency(instrumentCurrency), NormCurrency(accountCurrency)
	switch {
	case ac == "USD" && ic == "USD", ac == "GBP" && ic == "GBP":
		return 100, nil
	case ac == "GBP" && ic == "GBX":
		return 1, nil
	}
	return 0, Invalid("currency", fmt.Sprintf("an instrument priced in %q cannot be traded on a %q account (USD on USD, GBX or GBP on GBP)", ic, ac))
}

// T212MinorPerShare is the price of one share in millionths of the account's minor unit
// (e.g. $189.10 = 18 910 000 000 on a USD account; 250.5 GBX = 250 500 000 on a GBP account).
func T212MinorPerShare(price Micros, instrumentCurrency, accountCurrency string) (Micros, error) {
	scale, err := T212PriceScale(instrumentCurrency, accountCurrency)
	if err != nil {
		return 0, err
	}
	if price <= 0 {
		return 0, Invalid("price", "must be positive")
	}
	return Micros(mulDiv(int64(price), scale, 1)), nil
}

// bigRatio is num / den over big integers, rounded down or up, saturated at the int64 range
// (den must be positive; a negative result saturates at 0 when floor is asked for money).
func bigRatio(num, den *big.Int, up bool) int64 {
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if up && r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !up && r.Sign() < 0 {
		q.Sub(q, big.NewInt(1))
	}
	switch {
	case q.Cmp(big.NewInt(math.MaxInt64)) > 0:
		return math.MaxInt64
	case q.Cmp(big.NewInt(math.MinInt64)) < 0:
		return math.MinInt64
	}
	return q.Int64()
}

func bigProd(vs ...int64) *big.Int {
	p := big.NewInt(1)
	for _, v := range vs {
		p.Mul(p, big.NewInt(v))
	}
	return p
}

// T212Quantity is the quantity a size buys (T212-g): size ÷ (price per share × (1 + 1 %)),
// truncated (never rounded up) to whole shares, or to T212QuantityStep when fractional.
// perShare is T212MinorPerShare of the ask. 0 when nothing fits.
func T212Quantity(size Cents, perShare Micros, fractional bool) Micros {
	if size <= 0 || perShare <= 0 {
		return 0
	}
	// shares × 10^6 = size × 10^6 (micro-minor) × 10^6 (micro-share) × 10 000 / (perShare × 10 100)
	q := bigRatio(bigProd(int64(size), 1_000_000_000_000, 10_000), bigProd(int64(perShare), int64(10_000+T212SizeBufferBp)), false)
	if q == math.MaxInt64 {
		return 0 // beyond any real order: fail closed
	}
	step := T212QuantityStep(fractional)
	return Micros(q - q%int64(step))
}

// T212Reserve is the cash a buy of quantity at perShare reserves (T212-g): quantity × price ×
// (1 + 1 %), rounded up to the minor unit. For a quantity from T212Quantity it never exceeds
// the size it came from.
func T212Reserve(quantity, perShare Micros) Cents {
	if quantity <= 0 || perShare <= 0 {
		return 0
	}
	return Cents(bigRatio(bigProd(int64(quantity), int64(perShare), int64(10_000+T212SizeBufferBp)), bigProd(1_000_000_000_000, 10_000), true))
}

// t212Value is quantity × price in account minor units, rounded down or up.
func t212Value(quantity, price Micros, scale int64, up bool) Cents {
	return Cents(bigRatio(bigProd(int64(quantity), int64(price), scale), bigProd(1_000_000_000_000), up))
}

// T212CostInput is what the Trading 212 cost gate weighs (T212-k). Prices are eToro's quote
// of the matched instrument, in the instrument's currency.
type T212CostInput struct {
	Market             Market
	Quantity           Micros
	Bid, Ask           Micros
	TakeProfit         Micros
	InstrumentCurrency string
	AccountCurrency    string
}

// T212Cost is the cost gate's answer, in account minor units.
type T212Cost struct {
	Allowed bool
	Cost    Cents // stamp duty and PTM levy (London) plus 2 × the spread, rounded up
	Gain    Cents // the gross gain at the take-profit, rounded down
	Reason  string
}

// T212CostCheck refuses an entry whose costs eat its gain (T212-k): London stamp duty
// (50 bp of the buy, plus the PTM levy on an order over £10 000) and twice eToro's spread
// (ask − bid, the in and the out) must stay below the gross gain at the take-profit. There
// is no FX cost (T212-f). A missing take-profit, an unusable quote or a currency mismatch is
// a refusal (fail closed).
func T212CostCheck(in T212CostInput) T212Cost {
	deny := func(f string, a ...any) T212Cost { return T212Cost{Reason: fmt.Sprintf(f, a...)} }
	scale, err := T212PriceScale(in.InstrumentCurrency, in.AccountCurrency)
	switch {
	case err != nil:
		return deny("%v", err)
	case in.Quantity <= 0:
		return deny("no quantity")
	case in.Bid <= 0 || in.Ask <= 0 || in.Bid > in.Ask:
		return deny("the quote has no usable bid and ask")
	case in.TakeProfit <= in.Ask:
		return deny("no take-profit above the ask to weigh the costs against")
	}
	gain := t212Value(in.Quantity, in.TakeProfit-in.Ask, scale, false)
	cost := t212Value(in.Quantity, 2*(in.Ask-in.Bid), scale, true)
	if in.Market == MarketLSE {
		value := t212Value(in.Quantity, in.Ask, scale, true)
		cost += Cents(bigRatio(bigProd(int64(value), int64(T212StampDutyBp)), big.NewInt(10_000), true))
		if value > T212PTMThreshold {
			cost += T212PTMLevy
		}
	}
	if cost >= gain {
		return T212Cost{Cost: cost, Gain: gain, Reason: fmt.Sprintf("costs %s are not below the gain at the take-profit %s", cost, gain)}
	}
	return T212Cost{Allowed: true, Cost: cost, Gain: gain}
}

// T212FillConforms checks a Trading 212 fill against what was sent (T212-j, D42): something
// filled, no more than sent, at a price within T212FillToleranceBp of the eToro entry quote.
// A fill that does not conform is closed at once and its instrument blocked.
func T212FillConforms(sent, filled, fillPrice, entryQuote Micros) (bool, string) {
	switch {
	case filled <= 0:
		return false, "nothing filled"
	case filled > sent:
		return false, fmt.Sprintf("filled %s, more than the %s sent", filled, sent)
	case fillPrice <= 0 || entryQuote <= 0:
		return false, "no fill price or entry quote"
	}
	dev := int64(fillPrice - entryQuote)
	if dev < 0 {
		dev = -dev
	}
	if bigProd(dev, 10_000).Cmp(bigProd(int64(T212FillToleranceBp), int64(entryQuote))) > 0 {
		return false, fmt.Sprintf("filled at %s, more than %d bp from the eToro entry quote %s (wrong instrument?)", fillPrice, T212FillToleranceBp, entryQuote)
	}
	return true, ""
}

// T212Tax is one tax or fee Trading 212 charged on a fill (history's walletImpact.taxes).
type T212Tax struct {
	Name     string // e.g. STAMP_DUTY, PTM_LEVY
	Amount   Micros
	Currency string
}

// BrokerOrder is an order as Trading 212 reports it: the answer to a POST, an item of the
// open orders, or an order of the order history with its fill. Quantity and FilledQuantity
// are positive whatever the side.
type BrokerOrder struct {
	ID             int64
	Ticker         string
	ISIN           string
	Side           T212Side
	Type           T212OrderType
	Quantity       Micros
	FilledQuantity Micros
	FillPrice      Micros // average fill price, instrument currency (0 = none)
	StopPrice      Micros
	// Status is Trading 212's status as it came; State maps it (T212OrderState).
	Status        string
	State         OrderState
	Final         bool
	InitiatedFrom string // API, IOS, ANDROID, WEB, SYSTEM, …
	CreatedAt     time.Time
	FilledAt      time.Time
	Taxes         []T212Tax
}

// T212OrderState maps Trading 212's order status to ours, and whether it can still change.
// An unknown status (or a replaced order) is OrderUnknown: entries wait until it is resolved
// (fail closed).
func T212OrderState(status string) (OrderState, bool) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "FILLED":
		return OrderFilled, true
	case "PARTIALLY_FILLED":
		return OrderPartial, false
	case "CANCELLED":
		return OrderCancelled, true
	case "REJECTED":
		return OrderRejected, true
	case "LOCAL", "UNCONFIRMED", "CONFIRMED", "NEW", "CANCELLING":
		return OrderPending, false
	}
	return OrderUnknown, false
}

// T212MatchResult is how many orders Trading 212 lists could be one order of ours.
type T212MatchResult string

// Match results (T212-d): none (abandoned once neither list has it after T212MatchAfter),
// exactly one (adopted) or several (ambiguous: the owner resolves it).
const (
	T212MatchNone      T212MatchResult = "none"
	T212MatchOne       T212MatchResult = "one"
	T212MatchAmbiguous T212MatchResult = "ambiguous"
)

// MatchT212Order finds the order Trading 212 lists (open orders and history together, cands)
// that is our order o whose answer was lost (T212-d): the same ticker, side, type and
// quantity, initiated from the API, created within [o.SentAt − T212MatchBefore, o.SentAt +
// T212MatchAfter], and not claimed by another order of ours (claimed: Trading 212 ids
// already recorded on our orders). An order listed twice counts once. With exactly one
// candidate it is returned with T212MatchOne.
func MatchT212Order(o Order, cands []BrokerOrder, claimed []int64) (BrokerOrder, T212MatchResult) {
	side, typ, ok := t212Shape(o.Kind)
	if !ok || o.OnPlatform() != PlatformTrading212 || o.SentAt.IsZero() || o.Quantity <= 0 {
		return BrokerOrder{}, T212MatchNone
	}
	from, to := o.SentAt.Add(-T212MatchBefore), o.SentAt.Add(T212MatchAfter)
	seen := map[int64]bool{}
	var hits []BrokerOrder
	for _, c := range cands {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		switch {
		case c.ID <= 0, slices.Contains(claimed, c.ID), c.Ticker != o.Ticker, c.Side != side, c.Type != typ,
			c.Quantity != o.Quantity, c.InitiatedFrom != T212InitiatedFromAPI, c.CreatedAt.Before(from), c.CreatedAt.After(to):
			continue
		}
		hits = append(hits, c)
	}
	switch len(hits) {
	case 0:
		return BrokerOrder{}, T212MatchNone
	case 1:
		return hits[0], T212MatchOne
	}
	return BrokerOrder{}, T212MatchAmbiguous
}

// T212Position is one open position on a Trading 212 account. Prices are in the
// instrument's currency; Value, Cost and PnL in the account's.
type T212Position struct {
	Ticker   string
	ISIN     string
	Name     string
	Currency string
	Quantity Micros
	// QuantityAvailable is Trading 212's quantityAvailableForTrading: the most a sell may be.
	QuantityAvailable Micros
	AvgPrice          Micros
	CurrentPrice      Micros
	Value             Cents
	Cost              Cents
	PnL               Cents
	OpenedAt          time.Time
}

// T212Account is a Trading 212 account's cash and positions read at AsOf. Amounts are minor
// units of Currency, the account's primary currency.
type T212Account struct {
	Currency          string
	AvailableToTrade  Cents
	ReservedForOrders Cents
	Invested          Cents
	Equity            Cents // Trading 212's totalValue
	Positions         []T212Position
	AsOf              time.Time
}

// Position returns the account's position in ticker, if it holds one.
func (a T212Account) Position(ticker string) (T212Position, bool) {
	for _, p := range a.Positions {
		if p.Ticker == ticker {
			return p, true
		}
	}
	return T212Position{}, false
}

// T212HistoryPage is one page of Trading 212's order history; Next is the cursor of the next
// page ("" = the last page).
type T212HistoryPage struct {
	Orders []BrokerOrder
	Next   string
}

// T212Instrument is one instrument of Trading 212's catalogue (GET metadata/instruments).
type T212Instrument struct {
	Ticker    string // e.g. AAPL_US_EQ, BARCl_EQ
	ISIN      string
	Name      string
	ShortName string // e.g. AAPL, BARC
	Currency  string // GBX, USD, …
	Type      string // STOCK, ETF, …
	// MaxOpenQuantity is the most that may be held (0 = none may be bought).
	MaxOpenQuantity Micros
	FetchedAt       time.Time
}

// Trading 212 instrument types cactusTrade trades.
const (
	T212TypeStock = "STOCK"
	T212TypeETF   = "ETF"
)

// T212SuffixFor is the ticker suffix of a market's shares on Trading 212 (D42): "_US_EQ" for
// New York, "l_EQ" for London; ok is false for any other market.
func T212SuffixFor(m Market) (string, bool) {
	switch m {
	case MarketNYSE:
		return "_US_EQ", true
	case MarketLSE:
		return "l_EQ", true
	}
	return "", false
}

// t212TypeOf maps an eToro instrument type to Trading 212's ("" = not traded).
func t212TypeOf(etoroType string) string {
	switch strings.ToLower(strings.TrimSpace(etoroType)) {
	case "stocks", "stock":
		return T212TypeStock
	case "etf", "etfs":
		return T212TypeETF
	}
	return ""
}

// t212MatchCurrency is the currency both sides must agree on for market m: USD in New York,
// GBX (pence) in London.
func t212MatchCurrency(m Market) string {
	if m == MarketLSE {
		return "GBX"
	}
	return "USD"
}

// MatchInstrument finds the Trading 212 instrument of an eToro share (D42, T212-p): market
// and ticker suffix (_US_EQ for NYSE/NASDAQ, l_EQ for LSE), symbol (the eToro symbol without
// ".L" equals Trading 212's shortName), currency (USD, or GBX on both) and type (STOCK or
// ETF) must all agree, and exactly one instrument may fit. None: ErrNotFound; several:
// ErrConflict (ambiguous); another market or type: ErrInvalid. A match is only a candidate:
// the account owner confirms it (InstrumentMatch).
func MatchInstrument(in Instrument, cands []T212Instrument) (T212Instrument, error) {
	suffix, ok := T212SuffixFor(in.Market)
	if !ok {
		return T212Instrument{}, Invalid("market", "Trading 212 trading is for London and New York shares only")
	}
	typ := t212TypeOf(in.Type)
	if typ == "" {
		return T212Instrument{}, Invalid("type", "only shares and ETFs are traded")
	}
	cur := t212MatchCurrency(in.Market)
	if NormCurrency(in.Currency) != cur {
		return T212Instrument{}, Invalid("currency", fmt.Sprintf("%s is priced in %q on eToro, not %s", in.Symbol, in.Currency, cur))
	}
	sym := strings.ToUpper(strings.TrimSpace(in.Symbol))
	if in.Market == MarketLSE {
		sym = strings.TrimSuffix(sym, ".L")
	}
	var hits []T212Instrument
	for _, c := range cands {
		if strings.HasSuffix(c.Ticker, suffix) && strings.EqualFold(strings.TrimSpace(c.ShortName), sym) &&
			NormCurrency(c.Currency) == cur && strings.ToUpper(strings.TrimSpace(c.Type)) == typ {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 0:
		return T212Instrument{}, fmt.Errorf("no Trading 212 instrument matches %s: %w", in.Symbol, ErrNotFound)
	case 1:
		return hits[0], nil
	}
	return T212Instrument{}, fmt.Errorf("%d Trading 212 instruments match %s (ambiguous): %w", len(hits), in.Symbol, ErrConflict)
}

// Instrument match methods.
const (
	MatchMethodAuto = "auto" // MatchInstrument's candidate (D42)
)

// InstrumentMatch links an eToro share on an account's watchlist to a Trading 212 instrument
// (D42). It trades only once the account's owner has confirmed it.
type InstrumentMatch struct {
	AccountID   string
	Instrument  InstrumentID // the eToro instrument (signals and prices)
	Ticker      string       // the Trading 212 instrument (orders)
	ISIN        string       // Trading 212's ISIN, shown to the owner
	Method      string
	ConfirmedBy string // "" = not confirmed
	ConfirmedAt time.Time
	UpdatedAt   time.Time
}

// Confirmed reports whether the account's owner has confirmed the match.
func (m InstrumentMatch) Confirmed() bool { return m.ConfirmedBy != "" && !m.ConfirmedAt.IsZero() }

// T212Eligibility is the eligibility of eToro instrument id for a Trading 212 account, from
// its Trading 212 instrument t (T212-c): a long is allowed when t is a STOCK or ETF priced
// in a currency the account trades (T212PriceScale) and may be held (MaxOpenQuantity > 0).
// A short never is. CheckedAt is when the catalogue was fetched, so AssessEntry refuses an
// entry from a catalogue older than EligibilityMaxAge.
func T212Eligibility(id InstrumentID, symbol string, t T212Instrument, accountCurrency string) Eligibility {
	e := Eligibility{
		Instrument: id, Symbol: symbol, CheckedAt: t.FetchedAt,
		ShortReason: "(no shorts on Trading 212: long only)",
	}
	typ := strings.ToUpper(strings.TrimSpace(t.Type))
	_, curErr := T212PriceScale(t.Currency, accountCurrency)
	switch {
	case t.Ticker == "":
		e.Reason = "(no Trading 212 instrument)"
	case typ != T212TypeStock && typ != T212TypeETF:
		e.Reason = fmt.Sprintf("(Trading 212 type %q is not a share or ETF)", t.Type)
	case curErr != nil:
		e.Reason = "(" + curErr.Error() + ")"
	case t.MaxOpenQuantity <= 0:
		e.Reason = "(Trading 212 allows no position in it)"
	default:
		e.RealLong = true
	}
	return e
}
