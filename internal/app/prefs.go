package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/albievan/cactustrade/internal/domain"
)

// PrefsKeyPrefix starts the settings key of every user's view preferences (R50). A data
// reset (R49) keeps it: it is not one of the reset's wiped prefixes.
const PrefsKeyPrefix = "ui.prefs."

// prefsKey is the settings key of one user's view preferences.
func prefsKey(userID string) string { return PrefsKeyPrefix + userID }

// prefsCaller lets only a signed-in person in the web app at their own preferences. An API
// key is refused: preferences are the web app's view choices, not an automation surface.
func prefsCaller(p Principal) error {
	if p.UserID == "" {
		return domain.ErrUnauthenticated
	}
	if p.MustChangePassword {
		return domain.ErrPasswordChangeRequired
	}
	if p.Via != ViaSession {
		return fmt.Errorf("view preferences are for the web app only: %w", domain.ErrForbidden)
	}
	return nil
}

// MyPreferences returns the caller's own view preferences (R50): web session only, never
// another user's (the key is the caller's id, there is no user parameter). Stored
// preferences that cannot be read are logged and answered as none: they are view choices
// only and must never block the app.
func (s *Service) MyPreferences(ctx context.Context, p Principal) (domain.Preferences, error) {
	if err := prefsCaller(p); err != nil {
		return nil, err
	}
	return s.loadPrefs(ctx, p.UserID)
}

// SetMyPreferences applies a merge patch to the caller's own view preferences (R50): a
// value sets its key (last write wins), null deletes it. The patch is validated whole by
// domain.MergePreferences (key pattern, value shapes, 200 keys, 60 KiB); nothing is stored
// when anything is refused. Not audited: these are view choices with no effect on trading,
// risk or access (PLAN R50).
func (s *Service) SetMyPreferences(ctx context.Context, p Principal, patch map[string]json.RawMessage) (domain.Preferences, error) {
	if err := prefsCaller(p); err != nil {
		return nil, err
	}
	s.prefs.Lock()
	defer s.prefs.Unlock()
	cur, err := s.loadPrefs(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	next, err := domain.MergePreferences(cur, patch)
	if err != nil {
		return nil, err
	}
	enc, err := next.Encode()
	if err != nil {
		return nil, fmt.Errorf("app.SetMyPreferences: %w", err)
	}
	if err := s.d.Store.Settings().Set(ctx, prefsKey(p.UserID), enc, s.d.Clock.Now().UTC()); err != nil {
		return nil, fmt.Errorf("app.SetMyPreferences: %w", err)
	}
	return next, nil
}

// loadPrefs reads a user's stored preferences: none when never saved or unreadable.
func (s *Service) loadPrefs(ctx context.Context, userID string) (domain.Preferences, error) {
	v, err := s.d.Store.Settings().Get(ctx, prefsKey(userID))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Preferences{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("app.loadPrefs: %w", err)
	}
	p, err := domain.ParsePreferences(v)
	if err != nil {
		s.d.Log.WarnContext(ctx, "stored view preferences are unreadable; starting from none", "user", userID, "err", err)
		return domain.Preferences{}, nil
	}
	return p, nil
}
