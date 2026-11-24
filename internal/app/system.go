package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// SystemStatus is what the dashboard header shows.
type SystemStatus struct {
	Version    string
	Build      string
	Mode       domain.TradingMode
	AgentState domain.AgentState
	Database   string
	StartedAt  time.Time
	Now        time.Time
	Account    *domain.AccountView // the caller's current account; nil when there is none
	// RealArmed lists the accounts the caller can see whose real trading is armed (the
	// banner on every page, OD27). Always empty in this build.
	RealArmed []RealArmedAccount
}

// Now is the service's clock, for adapters that must measure against the same time the use
// cases do (the session cookie's lifetime).
func (s *Service) Now() time.Time { return s.d.Clock.Now() }

// accountMode is the mode an account trades in: real only when the account says so (an
// unknown value is demo: never real by accident).
func accountMode(a domain.Account) domain.TradingMode {
	if a.Mode == domain.ModeReal {
		return domain.ModeReal
	}
	return domain.ModeDemo
}

// Status reports versions and the caller's current account: its mode and agent state
// (viewer). Without an account the mode reads demo and the agent stopped.
func (s *Service) Status(ctx context.Context, p Principal) (SystemStatus, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return SystemStatus{}, err
	}
	st := SystemStatus{
		Version: s.cfg.Version, Build: s.cfg.Build, Mode: domain.ModeDemo, AgentState: domain.AgentStopped,
		Database: s.d.Store.Driver(), StartedAt: s.startedAt, Now: s.d.Clock.Now().UTC(),
	}
	v, err := s.account(ctx, p, "", domain.AccessView)
	switch {
	case err == nil:
		st.Mode, st.AgentState, st.Account = accountMode(v.Account), s.agentState(ctx, v.ID), &v
	case !errors.Is(err, domain.ErrNotFound):
		return SystemStatus{}, err
	}
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return SystemStatus{}, err
	}
	st.RealArmed = s.realArmed(ctx, vs)
	return st, nil
}

// Ready reports whether the service can take requests (database reachable).
func (s *Service) Ready(ctx context.Context) error {
	if err := s.d.Store.Ping(ctx); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	return nil
}

// Version is the running release.
func (s *Service) Version() string { return s.cfg.Version }
