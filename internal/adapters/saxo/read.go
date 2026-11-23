package saxo

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Positions are read in pages of pageSize, at most maxPages of them.
const (
	pageSize = 100
	maxPages = 20
)

// balancesWire is GET /port/v1/balances/me. Currency and TotalValue are verified in Saxo's
// samples; CashBalance is not (docs/saxo-api.md), so every field is a pointer and a missing
// one is refused rather than read as 0.
type balancesWire struct {
	Currency    *string  `json:"Currency"`
	TotalValue  *float64 `json:"TotalValue"`
	CashBalance *float64 `json:"CashBalance"`
}

// positionsWire is one page of GET /port/v1/positions/me with
// FieldGroups=PositionBase,PositionView,DisplayAndFormat.
type positionsWire struct {
	Data *[]positionWire `json:"Data"`
}

// positionWire: names marked "verified" appear in Saxo's samples; the others are parsed
// defensively (pointers) and, when absent, make the position (or that column) unknown.
type positionWire struct {
	PositionBase *struct {
		Amount            *float64 `json:"Amount"`    // verified
		AssetType         string   `json:"AssetType"` // verified
		Uic               *int64   `json:"Uic"`
		OpenPrice         *float64 `json:"OpenPrice"`
		ExecutionTimeOpen string   `json:"ExecutionTimeOpen"`
	} `json:"PositionBase"`
	PositionView *struct {
		AverageOpenPrice                *float64 `json:"AverageOpenPrice"` // verified
		MarketValue                     *float64 `json:"MarketValue"`      // verified
		MarketValueInBaseCurrency       *float64 `json:"MarketValueInBaseCurrency"`
		CurrentPrice                    *float64 `json:"CurrentPrice"`
		ProfitLossOnTrade               *float64 `json:"ProfitLossOnTrade"`
		ProfitLossOnTradeInBaseCurrency *float64 `json:"ProfitLossOnTradeInBaseCurrency"`
	} `json:"PositionView"`
	DisplayAndFormat *struct {
		Symbol      string `json:"Symbol"`      // verified
		Description string `json:"Description"` // verified
		Currency    string `json:"Currency"`    // verified
	} `json:"DisplayAndFormat"`
}

// Holdings reads the balance and the open positions of the Saxo account in ctx. Amounts are
// minor units of the account currency (never converted). A balance without its currency,
// total value or cash is refused (fail closed); a position whose value or P&L in the
// account currency is not reported is marked Unknown, never guessed.
func (c *Client) Holdings(ctx context.Context) (domain.Holdings, error) {
	var bal balancesWire
	if err := c.get(ctx, OpBalances, nil, &bal); err != nil {
		return domain.Holdings{}, err
	}
	if bal.Currency == nil || len(strings.TrimSpace(*bal.Currency)) != 3 || bal.TotalValue == nil || bal.CashBalance == nil {
		return domain.Holdings{}, fmt.Errorf("saxo %s: no Currency, TotalValue or CashBalance in the answer: %w", OpBalances, domain.ErrUnavailable)
	}
	cur := strings.ToUpper(strings.TrimSpace(*bal.Currency))
	var all []positionWire
	for page := 0; ; page++ {
		if page == maxPages {
			return domain.Holdings{}, fmt.Errorf("saxo %s: more than %d positions: %w", OpPositions, maxPages*pageSize, domain.ErrUnavailable)
		}
		q := url.Values{
			"FieldGroups": {"PositionBase,PositionView,DisplayAndFormat"},
			"$top":        {strconv.Itoa(pageSize)}, "$skip": {strconv.Itoa(page * pageSize)},
		}
		var pw positionsWire
		if err := c.get(ctx, OpPositions, q, &pw); err != nil {
			return domain.Holdings{}, err
		}
		if pw.Data == nil {
			return domain.Holdings{}, fmt.Errorf("saxo %s: no Data in the answer: %w", OpPositions, domain.ErrUnavailable)
		}
		all = append(all, *pw.Data...)
		if len(*pw.Data) < pageSize {
			break
		}
	}
	var cv conv
	h := domain.Holdings{
		Platform: domain.PlatformSaxo, Currency: cur, Cash: cv.minor(*bal.CashBalance, cur),
		Equity: cv.minor(*bal.TotalValue, cur), AsOf: c.cfg.Now().UTC(),
	}
	for _, p := range all {
		x := holding(&cv, p, cur)
		if !x.Unknown {
			h.Invested += x.Cost
			h.UnrealisedPnL += x.PnL
		}
		h.Positions = append(h.Positions, x)
	}
	if cv.err != nil {
		return domain.Holdings{}, fmt.Errorf("saxo %s: %w: %w", OpPositions, cv.err, domain.ErrUnavailable)
	}
	return h, nil
}

// holding converts one position. Value and P&L come from the *InBaseCurrency fields (the
// account currency) when present, else from MarketValue / ProfitLossOnTrade when the
// instrument trades in the account currency; otherwise the position is Unknown.
func holding(cv *conv, p positionWire, cur string) domain.Holding {
	x := domain.Holding{Long: true}
	if d := p.DisplayAndFormat; d != nil {
		x.Symbol, x.Name, x.Currency = d.Symbol, d.Description, strings.ToUpper(d.Currency)
	}
	known := false
	if b := p.PositionBase; b != nil {
		if b.Amount != nil {
			known = true
			x.Quantity, x.Long = cv.micros(math.Abs(*b.Amount)), *b.Amount >= 0
		}
		x.CFD = strings.HasPrefix(b.AssetType, "Cfd")
		if x.Symbol == "" && b.Uic != nil {
			x.Symbol = "#" + strconv.FormatInt(*b.Uic, 10)
		}
		if b.OpenPrice != nil {
			x.AvgPrice = cv.micros(*b.OpenPrice)
		}
		if t, err := time.Parse(time.RFC3339, b.ExecutionTimeOpen); err == nil {
			x.OpenedAt = t.UTC()
		}
	}
	if x.Symbol == "" {
		x.Symbol = "?"
	}
	var value, pnl *float64
	if v := p.PositionView; v != nil {
		if v.AverageOpenPrice != nil {
			x.AvgPrice = cv.micros(*v.AverageOpenPrice)
		}
		if v.CurrentPrice != nil {
			x.CurrentPrice = cv.micros(*v.CurrentPrice)
		}
		value, pnl = v.MarketValueInBaseCurrency, v.ProfitLossOnTradeInBaseCurrency
		if x.Currency == cur {
			value, pnl = firstOf(value, v.MarketValue), firstOf(pnl, v.ProfitLossOnTrade)
		}
	}
	if !known || value == nil || pnl == nil {
		x.Unknown = true
		return x
	}
	x.Value, x.PnL = cv.minor(*value, cur), cv.minor(*pnl, cur)
	x.Cost = x.Value - x.PnL
	return x
}

func firstOf(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}
