package fakes

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

type outboxRepo struct{ s *Store }

func (r outboxRepo) Enqueue(_ context.Context, m domain.OutboxMail) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if slices.ContainsFunc(r.s.outbox, func(x domain.OutboxMail) bool { return x.ID == m.ID }) {
		return domain.ErrConflict
	}
	m.SentAt = time.Time{}
	r.s.outbox = append(r.s.outbox, m)
	return nil
}

func (r outboxRepo) Due(_ context.Context, now time.Time, limit int) ([]domain.OutboxMail, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []domain.OutboxMail
	for _, m := range r.s.outbox {
		if m.SentAt.IsZero() && !m.NextAt.After(now) && m.Attempts < domain.MaxMailAttempts {
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out, func(a, b domain.OutboxMail) int { return a.NextAt.Compare(b.NextAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r outboxRepo) update(id string, f func(*domain.OutboxMail) bool) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for i := range r.s.outbox {
		if r.s.outbox[i].ID == id && f(&r.s.outbox[i]) {
			return nil
		}
	}
	return domain.ErrNotFound
}

func (r outboxRepo) MarkSent(_ context.Context, id string, at time.Time) error {
	return r.update(id, func(m *domain.OutboxMail) bool {
		m.SentAt, m.LastError = at, ""
		m.Attempts++
		return true
	})
}

func (r outboxRepo) MarkFailed(_ context.Context, id string, attempts int, next time.Time, lastErr string) error {
	return r.update(id, func(m *domain.OutboxMail) bool {
		if !m.SentAt.IsZero() {
			return false
		}
		m.Attempts, m.NextAt, m.LastError = attempts, next, lastErr
		return true
	})
}

func (r outboxRepo) List(_ context.Context, limit int) ([]domain.OutboxMail, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := slices.Clone(r.s.outbox)
	slices.Reverse(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r outboxRepo) DeleteBefore(_ context.Context, before time.Time) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	n := len(r.s.outbox)
	r.s.outbox = slices.DeleteFunc(r.s.outbox, func(m domain.OutboxMail) bool { return m.CreatedAt.Before(before) })
	return int64(n - len(r.s.outbox)), nil
}

type resetRepo struct{ s *Store }

func (r resetRepo) Create(_ context.Context, p domain.PasswordReset) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.resets[p.TokenHash]; ok {
		return domain.ErrConflict
	}
	p.UsedAt = time.Time{}
	r.s.resets[p.TokenHash] = p
	return nil
}

func (r resetRepo) Get(_ context.Context, h string) (domain.PasswordReset, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	p, ok := r.s.resets[h]
	if !ok {
		return domain.PasswordReset{}, domain.ErrNotFound
	}
	return p, nil
}

func (r resetRepo) Use(_ context.Context, h string, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	p, ok := r.s.resets[h]
	if !ok || !p.UsedAt.IsZero() {
		return domain.ErrNotFound
	}
	p.UsedAt = at
	r.s.resets[h] = p
	return nil
}

func (r resetRepo) CountSince(_ context.Context, userID string, since time.Time) (int, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	n := 0
	for _, p := range r.s.resets {
		if p.UserID == userID && !p.CreatedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (r resetRepo) DeleteForUser(_ context.Context, userID string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for h, p := range r.s.resets {
		if p.UserID == userID {
			delete(r.s.resets, h)
		}
	}
	return nil
}

func (r resetRepo) DeleteBefore(_ context.Context, before time.Time) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var n int64
	for h, p := range r.s.resets {
		if p.ExpiresAt.Before(before) {
			delete(r.s.resets, h)
			n++
		}
	}
	return n, nil
}

// Mailer records the mail it is asked to send; Err (when set) is returned instead.
type Mailer struct {
	mu   sync.Mutex
	Sent []domain.Mail
	Err  error
	// Server is the last server settings used (to check which server was chosen).
	Server domain.SMTPSettings
}

// Send records m.
func (f *Mailer) Send(_ context.Context, server domain.SMTPSettings, _ string, m domain.Mail) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Server = server
	if f.Err != nil {
		return f.Err
	}
	f.Sent = append(f.Sent, m)
	return nil
}

// Count returns how many messages were sent.
func (f *Mailer) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Sent)
}
