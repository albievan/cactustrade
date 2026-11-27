package domain

import (
	"fmt"
	"strings"
	"time"
)

// FXRates are eToro's own rates for converting cost lines to USD (the account currency):
// the USD value of one unit of each currency, in micros (AUD 0.65 = 650 000). A currency
// that is not in the map cannot be converted, and a cost line in it is a refusal.
type FXRates map[string]Micros

// FXPair names the eToro instrument (by symbol) whose rate converts currency cur to USD,
// and the number of units of cur in one unit of that pair's base currency: GBX (pence) is
// GBP/USD divided by 100. ok is false for USD itself and for any currency cactusTrade does
// not convert (a cost line in it is then a refusal, failing closed).
func FXPair(cur string) (symbol string, units int64, ok bool) {
	switch NormCurrency(cur) {
	case "AUD":
		return "AUDUSD", 1, true
	case "GBP":
		return "GBPUSD", 1, true
	case "GBX":
		return "GBPUSD", 100, true
	}
	return "", 0, false
}

// NormCurrency is a currency code as cost lines are compared: trimmed, upper case, with
// eToro's "GBp" (pence) spelled GBX. "" stays "" (a line with no currency is USD).
func NormCurrency(cur string) string {
	c := strings.TrimSpace(cur)
	if c == "GBp" {
		return "GBX"
	}
	return strings.ToUpper(c)
}

// FXRateFromQuote turns eToro's quote q of a currency pair into the USD value of one unit
// of a currency that has units of them in the pair's base currency (see FXPair). It uses
// the ask, the higher side, so a converted cost is never understated. A delayed, stale
// (older than maxAge at now), future or empty quote is refused.
func FXRateFromQuote(q Quote, units int64, now time.Time, maxAge time.Duration) (Micros, error) {
	switch {
	case units <= 0:
		return 0, fmt.Errorf("no unit count")
	case q.Delayed:
		return 0, fmt.Errorf("the rate is delayed")
	case q.At.IsZero() || now.Sub(q.At) > maxAge || q.At.After(now.Add(time.Second)):
		return 0, fmt.Errorf("the rate is stale (%s)", q.At.UTC().Format(time.RFC3339))
	case q.Ask <= 0 || q.Bid <= 0 || q.Bid > q.Ask:
		return 0, fmt.Errorf("the rate has no usable bid/ask")
	}
	r := (int64(q.Ask) + units - 1) / units // rounded up: never understate a cost
	return Micros(r), nil
}
