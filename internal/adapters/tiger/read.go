package tiger

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/albievan/cactustrade/internal/domain"
)

// securities is the prime_assets segment category of stocks ("S"; "C" is commodities).
const securities = "S"

// Field names below were read from the Go SDK's model package (model/trade.go PrimeAsset,
// PrimeAssetSegment, Asset; model/position.go Position). Everything is a pointer: a missing
// field is refused (account totals) or makes a position unknown, never 0.

type primeAssetsWire struct {
	Segments []struct {
		Category       string   `json:"category"`
		Currency       string   `json:"currency"`
		CashBalance    *float64 `json:"cashBalance"`
		NetLiquidation *float64 `json:"netLiquidation"`
		UnrealizedPL   *float64 `json:"unrealizedPL"`
		RealizedPL     *float64 `json:"realizedPL"`
	} `json:"segments"`
}

type assetsWire struct {
	Items []struct {
		Account        string   `json:"account"`
		Currency       string   `json:"currency"`
		CashValue      *float64 `json:"cashValue"`
		NetLiquidation *float64 `json:"netLiquidation"`
		UnrealizedPnL  *float64 `json:"unrealizedPnL"`
		RealizedPnL    *float64 `json:"realizedPnL"`
	} `json:"items"`
}

type positionsWire struct {
	Items *[]positionWire `json:"items"`
}

type positionWire struct {
	Symbol        string   `json:"symbol"`
	Name          string   `json:"name"`
	Market        string   `json:"market"`
	Currency      string   `json:"currency"`
	SecType       string   `json:"secType"`
	PositionQty   *float64 `json:"positionQty"`
	Position      *int64   `json:"position"`
	PositionScale int      `json:"positionScale"`
	AverageCost   *float64 `json:"averageCost"`
	LatestPrice   *float64 `json:"latestPrice"`
	MarketValue   *float64 `json:"marketValue"`
	UnrealizedPnl *float64 `json:"unrealizedPnl"`
}

// totals is the account's currency and cash, net liquidation and P&L figures.
type totals struct {
	currency                string
	cash, equity, unr, real *float64
}

// Holdings reads the assets and open positions of the Tiger account in ctx: prime_assets
// (its securities segment), or the older assets method when the account has no prime assets,
// then positions. Amounts are minor units of the account currency (never converted). A
// position in another currency has no account-currency value from Tiger and is Unknown.
func (c *Client) Holdings(ctx context.Context) (domain.Holdings, error) {
	t, err := c.totals(ctx)
	if err != nil {
		return domain.Holdings{}, err
	}
	var pw positionsWire
	if err := c.call(ctx, MethodPositions, nil, &pw); err != nil {
		return domain.Holdings{}, err
	}
	if pw.Items == nil {
		return domain.Holdings{}, fmt.Errorf("tiger %s: no items in the answer: %w", MethodPositions, domain.ErrUnavailable)
	}
	var cv conv
	cur := t.currency
	h := domain.Holdings{
		Platform: domain.PlatformTiger, Currency: cur, Cash: cv.minor(*t.cash, cur), Equity: cv.minor(*t.equity, cur),
		AsOf: c.cfg.Now().UTC(),
	}
	if t.real != nil {
		h.RealisedPnL = cv.minor(*t.real, cur)
	}
	var sum domain.Cents
	for _, p := range *pw.Items {
		x := holding(&cv, p, cur)
		if !x.Unknown {
			h.Invested += x.Cost
			sum += x.PnL
		}
		h.Positions = append(h.Positions, x)
	}
	h.UnrealisedPnL = sum
	if t.unr != nil {
		h.UnrealisedPnL = cv.minor(*t.unr, cur) // Tiger's own figure covers every position
	}
	if cv.err != nil {
		return domain.Holdings{}, fmt.Errorf("tiger %s: %w: %w", MethodPositions, cv.err, domain.ErrUnavailable)
	}
	return h, nil
}

// totals reads prime_assets; when Tiger refuses it for this account (a business error that
// is not a rate limit or a permission problem) or it has no securities segment, it reads
// assets instead. Without a currency, cash and net liquidation it fails closed.
func (c *Client) totals(ctx context.Context) (totals, error) {
	var pa primeAssetsWire
	err := c.call(ctx, MethodPrimeAssets, nil, &pa)
	if err == nil {
		for _, s := range pa.Segments {
			if s.Category == securities {
				return checked(totals{s.Currency, s.CashBalance, s.NetLiquidation, s.UnrealizedPL, s.RealizedPL})
			}
		}
	} else if be, ok := errors.AsType[*BusinessError](err); !ok || be.Code == 5 || be.Code < 1010 || be.Code >= 2400 {
		return totals{}, err
	}
	var as assetsWire
	if err := c.call(ctx, MethodAssets, map[string]any{"segment": true}, &as); err != nil {
		return totals{}, err
	}
	if len(as.Items) == 0 {
		return totals{}, fmt.Errorf("tiger %s: no assets in the answer: %w", MethodAssets, domain.ErrUnavailable)
	}
	a := as.Items[0]
	return checked(totals{a.Currency, a.CashValue, a.NetLiquidation, a.UnrealizedPnL, a.RealizedPnL})
}

func checked(t totals) (totals, error) {
	t.currency = strings.ToUpper(strings.TrimSpace(t.currency))
	if len(t.currency) != 3 || t.cash == nil || t.equity == nil {
		return totals{}, fmt.Errorf("tiger: no currency, cash or net liquidation in the answer: %w", domain.ErrUnavailable)
	}
	return t, nil
}

// holding converts one position. Quantity is positionQty, else position / 10^positionScale;
// value and P&L count only when the position is in the account currency.
func holding(cv *conv, p positionWire, cur string) domain.Holding {
	x := domain.Holding{Symbol: p.Symbol, Name: p.Name, Currency: strings.ToUpper(p.Currency), Long: true}
	if x.Symbol == "" {
		x.Symbol = "?"
	}
	if x.Name == "" {
		x.Name = p.Market
	}
	var qty *float64
	switch {
	case p.PositionQty != nil:
		qty = p.PositionQty
	case p.Position != nil && p.PositionScale >= 0 && p.PositionScale <= 12:
		q := float64(*p.Position) / math.Pow10(p.PositionScale)
		qty = &q
	}
	if qty != nil {
		x.Quantity, x.Long = cv.micros(math.Abs(*qty)), *qty >= 0
	}
	if p.AverageCost != nil {
		x.AvgPrice = cv.micros(*p.AverageCost)
	}
	if p.LatestPrice != nil {
		x.CurrentPrice = cv.micros(*p.LatestPrice)
	}
	if qty == nil || p.MarketValue == nil || p.UnrealizedPnl == nil || x.Currency != cur {
		x.Unknown = true
		return x
	}
	x.Value, x.PnL = cv.minor(*p.MarketValue, cur), cv.minor(*p.UnrealizedPnl, cur)
	x.Cost = x.Value - x.PnL
	return x
}
