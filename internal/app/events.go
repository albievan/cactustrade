package app

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// eventRetention is how long the feed keeps events.
const eventRetention = 90 * 24 * time.Hour

func seenKey(userID string) string { return "events.seen." + userID }

// emit appends to the event feed. A failed write is logged, never fatal to the action.
func (s *Service) emit(ctx context.Context, actor, kind string, sev domain.EventSeverity, title, detail string) {
	if actor == "" {
		actor = "system"
	}
	e := domain.Event{ID: s.d.Random.ID(), At: s.d.Clock.Now().UTC(), Kind: kind, Severity: sev, Title: title, Detail: detail, Actor: actor}
	if err := s.d.Store.Events().Append(ctx, e); err != nil {
		s.d.Log.ErrorContext(ctx, "event write failed", "kind", kind, "err", err)
	}
}

// EventFeed is the newest events and how many the caller has not seen.
type EventFeed struct {
	Events []domain.Event
	Unread int
}

// Events returns the newest events (viewer) and the caller's unread count.
func (s *Service) Events(ctx context.Context, p Principal, limit int) (EventFeed, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return EventFeed{}, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	es, err := s.d.Store.Events().List(ctx, limit)
	if err != nil {
		return EventFeed{}, err
	}
	n, err := s.d.Store.Events().CountSince(ctx, s.seen(ctx, p.UserID))
	if err != nil {
		return EventFeed{}, err
	}
	return EventFeed{Events: es, Unread: n}, nil
}

func (s *Service) seen(ctx context.Context, userID string) time.Time {
	v, err := s.d.Store.Settings().Get(ctx, seenKey(userID))
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.d.Log.WarnContext(ctx, "reading the events mark failed", "err", err)
		}
		return time.Time{}
	}
	us, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMicro(us).UTC()
}

// MarkEventsSeen clears the caller's unread count (viewer).
func (s *Service) MarkEventsSeen(ctx context.Context, p Principal) error {
	if err := s.authorize(p, domain.PermView); err != nil {
		return err
	}
	now := s.d.Clock.Now().UTC()
	return s.d.Store.Settings().Set(ctx, seenKey(p.UserID), strconv.FormatInt(now.UnixMicro(), 10), now)
}

// PurgeOldEvents deletes events past the retention period (called periodically).
func (s *Service) PurgeOldEvents(ctx context.Context) (int64, error) {
	return s.d.Store.Events().DeleteBefore(ctx, s.d.Clock.Now().Add(-eventRetention))
}
