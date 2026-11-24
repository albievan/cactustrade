package app

import (
	"context"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// CredentialStatus says whether a credential is set — never its value.
type CredentialStatus struct {
	Name      domain.CredentialName
	Label     string
	Set       bool
	Source    string // "ui", "env" or "" when not set
	EnvVar    string // the environment variable that may provide it, if any
	UpdatedAt *time.Time
}

// Credentials lists every deployment credential and whether it is set (viewers may see the
// list). It is read-only: eToro keys are set per account (SetAccountCredentials, owner, web
// session, only while the account is quiescent), and the environment account's demo pair
// comes from ETORO_DEMO_PUB / ETORO_DEMO_PK (PLAN 7.1.3, D27).
func (s *Service) Credentials(ctx context.Context, p Principal) ([]CredentialStatus, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return nil, err
	}
	st, err := s.d.Secrets.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("app.Credentials: %w", err)
	}
	out := make([]CredentialStatus, 0, len(domain.Credentials))
	for _, c := range domain.Credentials {
		cs := CredentialStatus{Name: c.Name, Label: c.Label, EnvVar: domain.CredentialEnv[c.Name]}
		if m, ok := st[c.Name]; ok {
			cs.Set, cs.Source = true, m.Source
			if !m.UpdatedAt.IsZero() {
				at := m.UpdatedAt
				cs.UpdatedAt = &at
			}
		}
		out = append(out, cs)
	}
	return out, nil
}

// AuditLog lists the newest audit entries (admin). limit is clamped to 1-500.
func (s *Service) AuditLog(ctx context.Context, p Principal, limit int) ([]domain.AuditEntry, error) {
	if err := s.authorize(p, domain.PermViewAudit); err != nil {
		return nil, err
	}
	limit = max(1, min(limit, 500))
	es, err := s.d.Store.Audit().List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("app.AuditLog: %w", err)
	}
	return es, nil
}
