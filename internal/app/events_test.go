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
	acct, _ := e.svc.CurrentAccount(ctx, e.admin)
	e.clock.T = e.clock.T.Add(time.Second)
	if err := e.svc.ShareAccount(ctx, e.admin, acct.ID, "vera", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	e.clock.T = e.clock.T.Add(time.Second)
	if _, err := e.svc.AddToWatchlist(ctx, e.admin, 1); err != nil {
		t.Fatal(err)
	}
	e.clock.T = e.clock.T.Add(time.Second)
	if err := e.svc.RemoveFromWatchlist(ctx, e.admin, 1); err != nil {
		t.Fatal(err)
	}
	f, err := e.svc.Events(ctx, viewer, 0)
	// account.created, account.shared, watchlist.added, watchlist.removed
	if err != nil || len(f.Events) != 4 || f.Unread != 4 || f.Events[0].Kind != "watchlist.removed" || f.Events[1].Title != "AAPL added to the watchlist of Main demo" || f.Events[1].Actor != "admin" || f.Events[1].AccountID != acct.ID {
		t.Fatalf("feed = %+v %v", f, err)
	}
	e.clock.T = e.clock.T.Add(time.Second)
	if err := e.svc.MarkEventsSeen(ctx, viewer); err != nil {
		t.Fatal(err)
	}
	if f, _ := e.svc.Events(ctx, viewer, 1); f.Unread != 0 || len(f.Events) != 1 {
		t.Fatalf("after seen = %+v", f)
	}
	if f, _ := e.svc.Events(ctx, e.admin, 10); f.Unread != 4 {
		t.Fatalf("the admin's own mark is separate: %+v", f)
	}
	stranger := e.user(t, "sam", domain.RoleViewer)
	if f, _ := e.svc.Events(ctx, stranger, 10); len(f.Events) != 0 {
		t.Fatalf("someone else's account events leaked: %+v", f.Events)
	}
	_ = e.store.Settings().Set(ctx, seenKey(e.admin.UserID), "garbage", e.clock.T)
	if f, _ := e.svc.Events(ctx, e.admin, 10); f.Unread != 4 {
		t.Fatal("a bad mark must count everything as unread")
	}
	if _, err := e.svc.Events(ctx, Principal{}, 10); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous feed")
	}
	if err := e.svc.MarkEventsSeen(ctx, Principal{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous mark")
	}
	e.clock.T = e.clock.T.Add(91 * 24 * time.Hour)
	if n, err := e.svc.PurgeOldEvents(ctx); err != nil || n != 4 {
		t.Fatalf("purge = %d %v", n, err)
	}
}
