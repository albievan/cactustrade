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
	// RealArmedUnknown: the gate of at least one real account the caller can see could not
	// be read; the banner says the real-trading state is unknown (never "not armed").
	RealArmedUnknown bool
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
//
// A user who must still change the default password gets only the banner's facts (the
// change-password page shows the banner too, review minor 1): no version, build, start time
// or account; every other use case stays refused for them.
func (s *Service) Status(ctx context.Context, p Principal) (SystemStatus, error) {
	if p.MustChangePassword && p.UserID != "" {
		return s.bannerStatus(ctx, p)
	}
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
	st.RealArmed, st.RealArmedUnknown = s.realArmed(ctx, vs)
	return st, nil
}

// bannerStatus is Status for a user who must still change the default password: the real
// banner's facts and nothing else.
func (s *Service) bannerStatus(ctx context.Context, p Principal) (SystemStatus, error) {
	st := SystemStatus{Mode: domain.ModeDemo, AgentState: domain.AgentStopped, Database: s.d.Store.Driver(), Now: s.d.Clock.Now().UTC()}
	vs, err := s.d.Store.Accounts().ForUser(ctx, p.UserID)
	if err != nil {
		return SystemStatus{}, fmt.Errorf("app.Status: %w", err)
	}
	st.RealArmed, st.RealArmedUnknown = s.realArmed(ctx, vs)
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
