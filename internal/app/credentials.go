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

// Credentials lists every credential and whether it is set (viewers may see the list).
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

// SetCredential stores a credential, encrypted (admin, signed-in only: an API key cannot
// change the broker keys). The value is never logged, audited or returned.
func (s *Service) SetCredential(ctx context.Context, p Principal, name, value string) error {
	if err := s.authorize(p, domain.PermManageSecrets); err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("credentials are set in the web UI only: %w", domain.ErrForbidden)
	}
	n, err := domain.ParseCredentialName(name)
	if err != nil {
		return err
	}
	v, err := domain.NormalizeCredential(value)
	if err != nil {
		return err
	}
	if err := s.d.Secrets.Put(ctx, n, v); err != nil {
		return fmt.Errorf("app.SetCredential: %w", err)
	}
	s.audit(ctx, p, "credential.set", string(n), "")
	return nil
}

// DeleteCredential removes a credential (admin, signed-in only).
func (s *Service) DeleteCredential(ctx context.Context, p Principal, name string) error {
	if err := s.authorize(p, domain.PermManageSecrets); err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("credentials are changed in the web UI only: %w", domain.ErrForbidden)
	}
	n, err := domain.ParseCredentialName(name)
	if err != nil {
		return err
	}
	if err := s.d.Secrets.Delete(ctx, n); err != nil {
		return fmt.Errorf("app.DeleteCredential: %w", err)
	}
	s.audit(ctx, p, "credential.delete", string(n), "")
	return nil
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
