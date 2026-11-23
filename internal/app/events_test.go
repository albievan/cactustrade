package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

func TestEventFeed(t *testing.T) {
	ctx := context.Background()
	e, _ := withBroker(t)
	viewer := e.user(t, "vera", domain.RoleViewer)
	if _, err := e.svc.AddToWatchlist(ctx, e.admin, 1); err != nil {
		t.Fatal(err)
	}
	e.clock.T = e.clock.T.Add(time.Second)
	if err := e.svc.RemoveFromWatchlist(ctx, e.admin, 1); err != nil {
		t.Fatal(err)
	}
	f, err := e.svc.Events(ctx, viewer, 0)
	if err != nil || len(f.Events) != 2 || f.Unread != 2 || f.Events[0].Kind != "watchlist.removed" || f.Events[1].Title != "AAPL added to the watchlist" || f.Events[1].Actor != "admin" {
		t.Fatalf("feed = %+v %v", f, err)
	}
	e.clock.T = e.clock.T.Add(time.Second)
	if err := e.svc.MarkEventsSeen(ctx, viewer); err != nil {
		t.Fatal(err)
	}
	if f, _ := e.svc.Events(ctx, viewer, 1); f.Unread != 0 || len(f.Events) != 1 {
		t.Fatalf("after seen = %+v", f)
	}
	if f, _ := e.svc.Events(ctx, e.admin, 10); f.Unread != 2 {
		t.Fatalf("the admin's own mark is separate: %+v", f)
	}
	_ = e.store.Settings().Set(ctx, seenKey(e.admin.UserID), "garbage", e.clock.T)
	if f, _ := e.svc.Events(ctx, e.admin, 10); f.Unread != 2 {
		t.Fatal("a bad mark must count everything as unread")
	}
	if _, err := e.svc.Events(ctx, Principal{}, 10); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous feed")
	}
	if err := e.svc.MarkEventsSeen(ctx, Principal{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous mark")
	}
	e.clock.T = e.clock.T.Add(91 * 24 * time.Hour)
	if n, err := e.svc.PurgeOldEvents(ctx); err != nil || n != 2 {
		t.Fatalf("purge = %d %v", n, err)
	}
}
