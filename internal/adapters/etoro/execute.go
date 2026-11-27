package etoro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// executable are the account-changing operations this version performs: on demo accounts,
// and on real-money accounts with a permit (PLAN 7.20). Setting or moving stops
// (OpSetStopLossTake) stays disabled on both: nothing needs it, and deepening a stop can move
// balance into margin.
var executable = map[Op]bool{OpOpenOrder: true, OpCancelOrder: true, OpClosePosition: true}

// realPermitted reports whether the permit in ctx allows op on the real-money account
// accountID at now (PLAN 7.20): opening needs an entry permit valid now; closing a position or
// cancelling an order accepts an exit or an entry permit for the account. Anything else (no
// permit, another account's, an expired one, any other operation) is refused.
func realPermitted(ctx context.Context, op Op, accountID string, now time.Time) bool {
	p, ok := ports.PermitFrom(ctx)
	if !ok {
		return false
	}
	switch op {
	case OpOpenOrder:
		return p.AllowsEntry(accountID, now)
	case OpClosePosition, OpCancelOrder:
		return p.AllowsExit(accountID)
	}
	return false
}

// isUUID reports whether s is a UUID in its canonical 36-character form (eToro requires a
// GUID x-request-id; Parse-style leniency would let other strings through).
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHex(c) {
				return false
			}
		}
	}
	return true
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// mutate performs one account-changing operation: refused before any I/O unless the
// operation is enabled, the account is an eToro demo account, or an eToro real-money account
// with its own keys (never the deployment's demo keys) and a permit in ctx for this account
// and operation (realPermitted; S3, PLAN 7.20), and requestID is a UUID; then exactly one
// request with x-request-id = requestID, to the mode's allowlisted path with the mode's keys
// and the same body as on demo. It never retries: a network error, a
// timeout, a 5xx or an unreadable answer is domain.ErrOutcomeUnknown (the request may have
// been processed); a 4xx is a *domain.NotAcceptedError (a 429 also pauses the quota group).
func (c *Client) mutate(ctx context.Context, op Op, path map[string]string, requestID string, body, out any) error {
	ep, ok := Allowlist[op]
	if !ok || !ep.Mutates || !executable[op] {
		return fmt.Errorf("etoro %s: %w", op, ErrNotEnabled)
	}
	acct, mode, err := account(ctx)
	if err != nil {
		return err
	}
	switch {
	case mode == domain.ModeDemo && acct.Mode == domain.ModeDemo:
	case mode == domain.ModeReal && acct.Mode == domain.ModeReal && !acct.EnvKeys && realPermitted(ctx, op, acct.ID, c.cfg.Now()):
		c.cfg.Log.WarnContext(ctx, "eToro REAL-MONEY account change", "op", string(op), "request_id", requestID)
	default:
		return fmt.Errorf("etoro %s for %s: %w", op, acct.Name, domain.ErrNoRealPermit)
	}
	if !isUUID(requestID) {
		return fmt.Errorf("etoro %s: %w", op, domain.Invalid("requestId", "must be a UUID"))
	}
	p, err := expand(ep.Path(mode), path)
	if err != nil {
		return fmt.Errorf("etoro %s: %w", op, err)
	}
	apiKey, userKey, err := c.keys(ctx, acct, mode)
	if err != nil {
		return err
	}
	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("etoro %s: %w", op, err)
		}
	}
	u := *c.base
	u.Path, u.RawPath, u.RawQuery = p, "", ""
	lim := c.limiter(acct.ID)
	if err := lim.Wait(ctx, ep.Quota); err != nil {
		return fmt.Errorf("etoro %s: not sent: %w", op, err)
	}
	tr := c.roundTrip(ctx, op, ep, mode, u.String(), payload, apiKey, userKey, requestID)
	switch tr.kind {
	case tripNotSent:
		return fmt.Errorf("etoro %s: not sent: %w", op, tr.err)
	case tripLost:
		c.cfg.Log.WarnContext(ctx, "eToro account change: outcome unknown", "op", string(op), "mode", string(mode), "request_id", requestID, "err", tr.err)
		return fmt.Errorf("etoro %s (request %s): %w: %w", op, requestID, domain.ErrOutcomeUnknown, tr.err)
	}
	c.cfg.Log.InfoContext(ctx, "eToro account change", "op", string(op), "mode", string(mode), "request_id", requestID, "status", tr.status)
	if tr.status < 200 || tr.status > 299 {
		apiErr := parseError(op, tr.status, tr.data)
		reason := strings.TrimSpace(strings.Join([]string{apiErr.Code, apiErr.Message}, " "))
		switch {
		case tr.status == http.StatusTooManyRequests:
			wait := retryAfter(tr.header.Get("Retry-After"))
			lim.Block(ep.Quota, c.cfg.Now().Add(wait))
			c.cfg.Log.WarnContext(ctx, "eToro rate limit hit", "op", string(op), "quota", string(ep.Quota), "retry_after_s", wait.Seconds())
			return &domain.NotAcceptedError{RateLimited: true, RetryAfter: wait, Reason: reason, Err: apiErr}
		case tr.status >= 400 && tr.status < 500:
			return &domain.NotAcceptedError{Reason: reason, Err: apiErr}
		}
		// 5xx, or a status eToro does not document for this call: it may have been processed.
		return fmt.Errorf("etoro %s (request %s): %w: %w", op, requestID, domain.ErrOutcomeUnknown, apiErr)
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(tr.data)) == 0 {
		return fmt.Errorf("etoro %s (request %s): empty answer: %w", op, requestID, domain.ErrOutcomeUnknown)
	}
	if err := json.Unmarshal(tr.data, out); err != nil {
		return fmt.Errorf("etoro %s (request %s): unreadable answer: %w: %w", op, requestID, domain.ErrOutcomeUnknown, err)
	}
	return nil
}

// openRequest is the v2 create-order body. It has exactly these keys and every one but the
// intent's instrument, amount and rates is a guardrail constant (CLAUDE.md §2.3): there is no
// field through which leverage, settlement or direction could be chosen freely.
type openRequest struct {
	Action         string  `json:"action"`
	Transaction    string  `json:"transaction"`
	InstrumentID   int64   `json:"instrumentId"`
	SettlementType string  `json:"settlementType"`
	OrderType      string  `json:"orderType"`
	Leverage       int     `json:"leverage"`
	Amount         float64 `json:"amount"`
	OrderCurrency  string  `json:"orderCurrency"`
	StopLossRate   float64 `json:"stopLossRate"`
	// TakeProfitRate is left out when the intent has none (0).
	TakeProfitRate float64 `json:"takeProfitRate,omitempty"`
	StopLossType   string  `json:"stopLossType"`
}

// openBody builds the body for an intent from the two constant order shapes. An intent
// without a stop, with an unknown direction or without an instrument or amount is refused.
func openBody(in domain.OrderIntent) (openRequest, error) {
	tr, st, ok := orderShape(in.Direction)
	switch {
	case !ok:
		return openRequest{}, domain.Invalid("direction", "long or short")
	case in.Instrument <= 0:
		return openRequest{}, domain.Invalid("instrumentId", "required")
	case in.Amount <= 0:
		return openRequest{}, domain.Invalid("amount", "must be positive")
	case in.StopLossRate <= 0:
		return openRequest{}, domain.Invalid("stopLossRate", "every order carries a stop-loss")
	case in.TakeProfitRate < 0:
		return openRequest{}, domain.Invalid("takeProfitRate", "must not be negative")
	}
	return openRequest{
		Action: OrderAction, Transaction: tr, InstrumentID: int64(in.Instrument), SettlementType: st,
		OrderType: OrderType, Leverage: OrderLeverage, Amount: float64(in.Amount) / 100, OrderCurrency: OrderCurrency,
		StopLossRate: float64(in.StopLossRate) / 1e6, TakeProfitRate: float64(in.TakeProfitRate) / 1e6, StopLossType: StopLossType,
	}, nil
}

// Open sends a market order for a demo account, or a real-money one with an entry permit. eToro's 200 means accepted, not filled: the
// answer is pending with eToro's order id; poll Lookup. An answer whose referenceId is not
// our request id (or that has no order id) is an unknown outcome.
func (c *Client) Open(ctx context.Context, in domain.OrderIntent) (domain.OrderStatus, error) {
	body, err := openBody(in)
	if err != nil {
		return domain.OrderStatus{}, fmt.Errorf("etoro %s: %w", OpOpenOrder, err)
	}
	var resp struct {
		Token       string `json:"token"`
		OrderID     int64  `json:"orderId"`
		ReferenceID string `json:"referenceId"`
	}
	if err := c.mutate(ctx, OpOpenOrder, nil, in.RequestID, body, &resp); err != nil {
		return domain.OrderStatus{}, err
	}
	if !strings.EqualFold(resp.ReferenceID, in.RequestID) || resp.OrderID <= 0 {
		return domain.OrderStatus{}, fmt.Errorf("etoro %s (request %s): answer for reference %q, order %d: %w",
			OpOpenOrder, in.RequestID, resp.ReferenceID, resp.OrderID, domain.ErrOutcomeUnknown)
	}
	return domain.OrderStatus{RequestID: in.RequestID, OrderID: resp.OrderID, State: domain.OrderPending, Instrument: in.Instrument}, nil
}

// closeRequest is the close-position body: the instrument only. UnitsToDeduct is never sent,
// so a close is always of the whole position.
type closeRequest struct {
	InstrumentID int64 `json:"InstrumentID"`
}

// Close sends a market order closing the whole position of a demo account, or of a
// real-money one with an exit or entry permit. The answer
// carries eToro's close order id; poll CloseOrder.
func (c *Client) Close(ctx context.Context, positionID int64, instrument domain.InstrumentID, requestID string) (domain.OrderStatus, error) {
	if positionID <= 0 || instrument <= 0 {
		return domain.OrderStatus{}, fmt.Errorf("etoro %s: %w", OpClosePosition, domain.Invalid("positionId", "a close needs the position and its instrument"))
	}
	var resp struct {
		OrderForClose *struct {
			PositionID   int64 `json:"positionID"`
			InstrumentID int64 `json:"instrumentID"`
			OrderID      int64 `json:"orderID"`
			StatusID     int   `json:"statusID"`
		} `json:"orderForClose"`
	}
	path := map[string]string{"positionId": strconv.FormatInt(positionID, 10)}
	if err := c.mutate(ctx, OpClosePosition, path, requestID, closeRequest{InstrumentID: int64(instrument)}, &resp); err != nil {
		return domain.OrderStatus{}, err
	}
	o := resp.OrderForClose
	if o == nil || o.OrderID <= 0 || (o.PositionID != 0 && o.PositionID != positionID) {
		return domain.OrderStatus{}, fmt.Errorf("etoro %s (request %s): no close order for position %d in the answer: %w",
			OpClosePosition, requestID, positionID, domain.ErrOutcomeUnknown)
	}
	st := domain.OrderStatus{RequestID: requestID, OrderID: o.OrderID, State: domain.OrderPending, Instrument: instrument}
	if s, ok := orderStates[o.StatusID]; ok {
		st.State, st.Final = s, finalStatus[o.StatusID]
	}
	return st, nil
}

// Cancel asks eToro to cancel a pending open order of a demo account, or of a real-money one
// with an exit or entry permit. 200 means the request
// was accepted, not that the order was cancelled: confirm with Lookup.
func (c *Client) Cancel(ctx context.Context, orderID int64, requestID string) error {
	if orderID <= 0 {
		return fmt.Errorf("etoro %s: %w", OpCancelOrder, domain.Invalid("orderId", "required"))
	}
	return c.mutate(ctx, OpCancelOrder, map[string]string{"orderId": strconv.FormatInt(orderID, 10)}, requestID, nil, nil)
}

// CloseOrder reads a close order's status and the positions it closed (read-only; retried
// like every read).
func (c *Client) CloseOrder(ctx context.Context, orderID int64) (domain.OrderStatus, error) {
	if orderID <= 0 {
		return domain.OrderStatus{}, domain.Invalid("orderId", "required")
	}
	var resp struct {
		OrderID      int64   `json:"orderID"`
		StatusID     int     `json:"statusID"`
		ReferenceID  *string `json:"referenceID"`
		ErrorMessage *string `json:"errorMessage"`
		InstrumentID int64   `json:"instrumentID"`
		Positions    []struct {
			PositionID int64    `json:"positionID"`
			Rate       *float64 `json:"rate"`
			Units      *float64 `json:"units"`
			Amount     *float64 `json:"amount"`
		} `json:"positions"`
	}
	if err := c.call(ctx, OpCloseOrderInfo, map[string]string{"orderId": strconv.FormatInt(orderID, 10)}, nil, nil, &resp); err != nil {
		return domain.OrderStatus{OrderID: orderID, State: domain.OrderUnknown}, err
	}
	st := domain.OrderStatus{OrderID: resp.OrderID, State: domain.OrderUnknown, Instrument: domain.InstrumentID(resp.InstrumentID)}
	if s, ok := orderStates[resp.StatusID]; ok {
		st.State, st.Final = s, finalStatus[resp.StatusID]
	}
	if resp.ReferenceID != nil {
		st.RequestID = *resp.ReferenceID
	}
	if resp.ErrorMessage != nil {
		st.Reason = *resp.ErrorMessage
	}
	for _, p := range resp.Positions {
		e := domain.PositionExecution{PositionID: p.PositionID}
		if p.Units != nil {
			e.Units = toMicros(*p.Units)
		}
		if p.Amount != nil {
			e.Amount = toCents(*p.Amount)
		}
		if p.Rate != nil {
			e.CloseRate = toMicros(*p.Rate)
		}
		st.Executions = append(st.Executions, e)
	}
	return st, nil
}
