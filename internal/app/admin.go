package app

import (
	"context"
	"fmt"

	"github.com/albievan/cactustrade/internal/domain"
)

// ReloadSettings re-reads the credentials provided by the environment or secret files, so a
// changed ETORO_DEMO_PUB/_PK file takes effect without a restart (admin, web session).
// Everything stored in the database is read live already.
func (s *Service) ReloadSettings(ctx context.Context, p Principal) error {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("only from the web UI: %w", domain.ErrForbidden)
	}
	if s.d.Reload == nil {
		return fmt.Errorf("reloading is not supported here: %w", domain.ErrUnavailable)
	}
	if err := s.d.Reload(ctx); err != nil {
		s.audit(ctx, p, "system.reload", "", "failed")
		return fmt.Errorf("reload: %w: %w", domain.ErrUnavailable, err)
	}
	s.audit(ctx, p, "system.reload", "", "ok")
	s.emit(ctx, p.Username, "system.reloaded", domain.SeverityInfo, "Settings reloaded", "credentials re-read from the environment and secret files")
	return nil
}

// RestartServer restarts cactusTrade gracefully (admin, web session). The agent is stopped
// first by the shutdown; open positions are left to the start-up reconciliation.
func (s *Service) RestartServer(ctx context.Context, p Principal) error {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("only from the web UI: %w", domain.ErrForbidden)
	}
	if s.d.Restart == nil {
		return fmt.Errorf("restarting is not supported here: %w", domain.ErrUnavailable)
	}
	s.audit(ctx, p, "system.restart", "", "")
	s.emit(ctx, p.Username, "system.restart", domain.SeverityWarning, "cactusTrade is restarting", "requested by "+p.Username)
	s.d.Restart()
	return nil
}
