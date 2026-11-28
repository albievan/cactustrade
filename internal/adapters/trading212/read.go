package trading212

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

type summaryWire struct {
	Cash *struct {
		AvailableToTrade  float64 `json:"availableToTrade"`
		InPies            float64 `json:"inPies"`
		ReservedForOrders float64 `json:"reservedForOrders"`
	} `json:"cash"`
	Currency    string `json:"currency"`
	ID          int64  `json:"id"`
	Investments *struct {
		CurrentValue         float64 `json:"currentValue"`
		RealizedProfitLoss   float64 `json:"realizedProfitLoss"`
		TotalCost            float64 `json:"totalCost"`
		UnrealizedProfitLoss float64 `json:"unrealizedProfitLoss"`
	} `json:"investments"`
	TotalValue *float64 `json:"totalValue"`
}

type positionWire struct {
	AveragePricePaid float64   `json:"averagePricePaid"`
	CreatedAt        time.Time `json:"createdAt"`
	CurrentPrice     float64   `json:"currentPrice"`
	Instrument       struct {
		Currency string `json:"currency"`
		ISIN     string `json:"isin"`
		Name     string `json:"name"`
		Ticker   string `json:"ticker"`
	} `json:"instrument"`
	Quantity                    float64 `json:"quantity"`
	QuantityAvailableForTrading float64 `json:"quantityAvailableForTrading"`
	QuantityInPies              float64 `json:"quantityInPies"`
	WalletImpact                *struct {
		Currency             string   `json:"currency"`
		CurrentValue         float64  `json:"currentValue"`
		FxImpact             *float64 `json:"fxImpact"`
		TotalCost            float64  `json:"totalCost"`
		UnrealizedProfitLoss float64  `json:"unrealizedProfitLoss"`
	} `json:"walletImpact"`
}

// summary is the account summary in minor units.
type summary struct {
	domain.Holdings
	reserved domain.Cents
}

// Holdings reads the account summary and the open positions of the account in ctx. Amounts
// are in the account's own currency (often GBP or EUR, never converted). A summary without
// its currency or total value is refused (fail closed).
func (c *Client) Holdings(ctx context.Context) (domain.Holdings, error) {
	h, pos, err := c.holdings(ctx)
	if err != nil {
		return domain.Holdings{}, err
	}
	var cv conv
	for _, p := range pos {
		x := domain.Holding{
			Symbol: p.Instrument.Ticker, Name: p.Instrument.Name, Currency: p.Instrument.Currency,
			Quantity: cv.micros(p.Quantity), AvgPrice: cv.micros(p.AveragePricePaid), CurrentPrice: cv.micros(p.CurrentPrice),
			OpenedAt: p.CreatedAt.UTC(), Long: true, // Trading 212 Invest holds real shares, long only
		}
		if w := p.WalletImpact; w != nil {
			x.Value, x.Cost, x.PnL = cv.cents(w.CurrentValue), cv.cents(w.TotalCost), cv.cents(w.UnrealizedProfitLoss)
		}
		h.Positions = append(h.Positions, x)
	}
	if cv.err != nil {
		return domain.Holdings{}, fmt.Errorf("trading212 %s: %w: %w", OpPositions, cv.err, domain.ErrUnavailable)
	}
	return h.Holdings, nil
}

// holdings reads the summary and the raw positions.
func (c *Client) holdings(ctx context.Context) (summary, []positionWire, error) {
	// The figures are as old as the cash read (availableToTrade), not the later positions
	// read, which may wait for its rate limit (review B4): AsOf is taken before the summary.
	asOf := c.cfg.Now().UTC()
	var sum summaryWire
	if err := c.get(ctx, OpAccountSummary, nil, &sum); err != nil {
		return summary{}, nil, err
	}
	cur := strings.ToUpper(strings.TrimSpace(sum.Currency))
	if len(cur) != 3 || sum.TotalValue == nil || sum.Cash == nil {
		return summary{}, nil, fmt.Errorf("trading212 %s: no currency, cash or total value in the response: %w", OpAccountSummary, domain.ErrUnavailable)
	}
	var pos []positionWire
	if err := c.get(ctx, OpPositions, nil, &pos); err != nil {
		return summary{}, nil, err
	}
	var cv conv
	h := summary{Holdings: domain.Holdings{
		Platform: domain.PlatformTrading212, Currency: cur, Cash: cv.cents(sum.Cash.AvailableToTrade),
		Equity: cv.cents(*sum.TotalValue), AsOf: asOf,
	}, reserved: cv.cents(sum.Cash.ReservedForOrders)}
	if inv := sum.Investments; inv != nil {
		h.Invested, h.UnrealisedPnL, h.RealisedPnL = cv.cents(inv.TotalCost), cv.cents(inv.UnrealizedProfitLoss), cv.cents(inv.RealizedProfitLoss)
	}
	if cv.err != nil {
		return summary{}, nil, fmt.Errorf("trading212 %s: %w: %w", OpAccountSummary, cv.err, domain.ErrUnavailable)
	}
	return h, pos, nil
}
