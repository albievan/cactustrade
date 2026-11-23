package etoro

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

var _ ports.Research = (*Client)(nil)

// Industries lists eToro's stock industries. The spec documents the list but not the item
// fields, so an item is read as its first integer field whose name contains "id" and its
// first string field whose name contains "name"; items without both are skipped.
func (c *Client) Industries(ctx context.Context) ([]domain.Industry, error) {
	var resp struct {
		StocksIndustries []map[string]json.RawMessage `json:"stocksIndustries"`
	}
	if err := c.call(ctx, OpIndustries, nil, nil, nil, &resp); err != nil {
		return nil, err
	}
	var out []domain.Industry
	for _, item := range resp.StocksIndustries {
		keys := make([]string, 0, len(item))
		for k := range item {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var ind domain.Industry
		for _, k := range keys {
			lk := strings.ToLower(k)
			if ind.ID == 0 && strings.Contains(lk, "id") {
				var n int64
				if json.Unmarshal(item[k], &n) == nil {
					ind.ID = n
				}
			}
			if ind.Name == "" && strings.Contains(lk, "name") {
				var s string
				if json.Unmarshal(item[k], &s) == nil {
					ind.Name = strings.TrimSpace(s)
				}
			}
		}
		if ind.ID > 0 && ind.Name != "" {
			out = append(out, ind)
		}
	}
	slices.SortFunc(out, func(a, b domain.Industry) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// screenFields are the instrument fields the screen asks for.
const screenFields = "instrumentId,internalSymbolFull,displayname,internalAssetClassName,internalExchangeId,isCurrentlyTradable,isBuyEnabled,industryNameId,popularityUniques7Day"

// Screen lists currently tradable shares (in one industry when industryID > 0), most popular
// first. Only shares on supported markets come back with a Market.
func (c *Client) Screen(ctx context.Context, industryID int64, limit int) ([]domain.Instrument, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	q := url.Values{
		"fields": {screenFields}, "sort": {"-popularityUniques7Day"}, "pageSize": {strconv.Itoa(limit)}, "pageNumber": {"1"},
		"isCurrentlyTradable": {"true"},
	}
	if industryID > 0 {
		q.Set("industryNameId", strconv.FormatInt(industryID, 10))
	}
	var resp struct {
		Items []struct {
			InstrumentID int64  `json:"instrumentId"`
			Symbol       string `json:"internalSymbolFull"`
			Name         string `json:"displayname"`
			AssetClass   string `json:"internalAssetClassName"`
			ExchangeID   int64  `json:"internalExchangeId"`
			Tradable     *bool  `json:"isCurrentlyTradable"`
			IndustryID   int64  `json:"industryNameId"`
		} `json:"items"`
	}
	if err := c.call(ctx, OpScreen, nil, q, nil, &resp); err != nil {
		return nil, err
	}
	var out []domain.Instrument
	for _, it := range resp.Items {
		if it.InstrumentID <= 0 || (it.Tradable != nil && !*it.Tradable) {
			continue
		}
		if industryID > 0 && it.IndustryID != 0 && it.IndustryID != industryID {
			continue // the filter was ignored upstream: never return another industry
		}
		w := instrumentWire{InstrumentID: it.InstrumentID, DisplayName: it.Name, Type: it.AssetClass, Symbol: it.Symbol, ExchangeID: it.ExchangeID}
		out = append(out, w.domain())
	}
	return out, nil
}
