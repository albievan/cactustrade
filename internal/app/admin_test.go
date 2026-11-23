package app

import (
	"context"
	"errors"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

func TestReloadAndRestart(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if err := e.svc.ReloadSettings(ctx, e.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("reload without a hook")
	}
	if err := e.svc.RestartServer(ctx, e.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("restart without a hook")
	}
	reloads, restarts := 0, 0
	var reloadErr error
	e.svc.d.Reload = func(context.Context) error { reloads++; return reloadErr }
	e.svc.d.Restart = func() { restarts++ }
	op := e.user(t, "olivia", domain.RoleOperator)
	key := e.admin
	key.Via = ViaAPIKey
	for _, p := range []Principal{op, key} {
		if err := e.svc.ReloadSettings(ctx, p); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("reload by %+v: %v", p, err)
		}
		if err := e.svc.RestartServer(ctx, p); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("restart by %+v: %v", p, err)
		}
	}
	if err := e.svc.ReloadSettings(ctx, e.admin); err != nil || reloads != 1 {
		t.Fatalf("reload: %v (%d)", err, reloads)
	}
	reloadErr = errors.New("bad file")
	if err := e.svc.ReloadSettings(ctx, e.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("failed reload")
	}
	if err := e.svc.RestartServer(ctx, e.admin); err != nil || restarts != 1 {
		t.Fatalf("restart: %v (%d)", err, restarts)
	}
	f, _ := e.svc.Events(ctx, e.admin, 10)
	if f.Events[0].Kind != "system.restart" || f.Events[1].Kind != "system.reloaded" {
		t.Fatalf("events = %+v", f.Events)
	}
}
