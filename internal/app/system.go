package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Setting keys.
const settingMode = "trading.mode"

// SystemStatus is what the dashboard header shows.
type SystemStatus struct {
	Version    string
	Build      string
	Mode       domain.TradingMode
	AgentState domain.AgentState
	Database   string
	StartedAt  time.Time
	Now        time.Time
}

// Mode returns the trading mode stored server-side; demo unless the gate set real. An
// unreadable or unknown value is demo (fail closed: never real by accident).
func (s *Service) Mode(ctx context.Context) domain.TradingMode {
	v, err := s.d.Store.Settings().Get(ctx, settingMode)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.d.Log.WarnContext(ctx, "reading trading mode failed; using demo", "err", err)
		}
		return domain.ModeDemo
	}
	if domain.TradingMode(v) == domain.ModeReal {
		return domain.ModeReal
	}
	return domain.ModeDemo
}

// Status reports versions, mode and agent state (viewer).
func (s *Service) Status(ctx context.Context, p Principal) (SystemStatus, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return SystemStatus{}, err
	}
	return SystemStatus{
		Version: s.cfg.Version, Build: s.cfg.Build, Mode: s.Mode(ctx),
		AgentState: s.AgentState(ctx), Database: s.d.Store.Driver(),
		StartedAt: s.startedAt, Now: s.d.Clock.Now().UTC(),
	}, nil
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
