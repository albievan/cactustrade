package app

import (
	"context"
	"errors"

	"github.com/albievan/cactustrade/internal/domain"
)

// researchModeKey is the setting holding an account's research mode (PLAN R29).
func researchModeKey(accountID string) string { return "research.mode." + accountID }

// ResearchMode says what web research may do for an account: off, propose or auto_add.
//
// TEMPORARY (PLAN R29-c): the autonomy levels being added in the main tree define this
// function; this stand-in reads the setting research.mode.<account> and is replaced at the
// merge. Nothing stored = auto_add, matching the owner's default (medium) autonomy; an
// unreadable or unknown value = off (fail closed).
func (s *Service) ResearchMode(ctx context.Context, accountID string) domain.ResearchMode {
	v, err := s.d.Store.Settings().Get(ctx, researchModeKey(accountID))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ResearchAutoAdd
	}
	if m := domain.ResearchMode(v); err == nil && m.Valid() {
		return m
	}
	return domain.ResearchOff
}
