package storetest

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// runStatements covers R30 (migration 00013): the outbox's attachments, the statement
// schedules and LLM usage per account.
func runStatements(t *testing.T, s ports.Store, t0 time.Time) {
	t.Helper()
	ctx := context.Background()
	owner := domain.User{ID: "u-stmt", Username: "stella", Role: domain.RoleOperator, PasswordHash: "h", CreatedAt: t0, UpdatedAt: t0}
	if err := s.Users().Create(ctx, owner); err != nil {
		t.Fatal(err)
	}

	t.Run("outbox_attachments", func(t *testing.T) {
		o := s.Outbox()
		bin := make([]byte, 3*256) // every byte value, so nothing is changed on the way
		for i := range bin {
			bin[i] = byte(i)
		}
		m := domain.OutboxMail{Mail: domain.Mail{
			ID: "att-1", To: "a@example.com", Subject: "Statement", Body: "see attached",
			Attachments: []domain.Attachment{
				{Filename: "a.pdf", ContentType: "application/pdf", Data: bin},
				{Filename: "b.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4\n")},
			},
		}, CreatedAt: t0, NextAt: t0}
		if err := o.Enqueue(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := o.Enqueue(ctx, m); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate: %v", err)
		}
		bad := m
		bad.ID, bad.Attachments = "att-bad", []domain.Attachment{{Filename: "x.exe", ContentType: "application/pdf", Data: []byte("x")}}
		if err := o.Enqueue(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("unsafe attachment stored: %v", err)
		}
		due, err := o.Due(ctx, t0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var got *domain.OutboxMail
		for i := range due {
			if due[i].ID == "att-1" {
				got = &due[i]
			}
			if due[i].ID == "att-bad" {
				t.Fatal("a refused mail was stored")
			}
		}
		if got == nil || len(got.Attachments) != 2 || got.Attachments[0].Filename != "a.pdf" || !bytes.Equal(got.Attachments[0].Data, bin) ||
			got.Attachments[1].ContentType != "application/pdf" || string(got.Attachments[1].Data) != "%PDF-1.4\n" {
			t.Fatalf("Due = %+v", got)
		}
		// The purge takes the files with their mail: the same id stored again has only its
		// new files.
		if _, err := o.DeleteBefore(ctx, t0.Add(time.Microsecond)); err != nil {
			t.Fatal(err)
		}
		m.Attachments = m.Attachments[1:]
		if err := o.Enqueue(ctx, m); err != nil {
			t.Fatal(err)
		}
		due, _ = o.Due(ctx, t0, 100)
		for _, d := range due {
			if d.ID == "att-1" && len(d.Attachments) != 1 {
				t.Fatalf("files outlived their mail: %+v", d.Attachments)
			}
		}
		if _, err := o.DeleteBefore(ctx, t0.Add(time.Microsecond)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("llm_usage_for", func(t *testing.T) {
		calls := []domain.LLMCall{
			{ID: "su-1", AccountID: "acc-su-1", Purpose: domain.LLMPurposeAgent, InputTokens: 10, OutputTokens: 2, CostMicros: 1_000, At: t0},
			{ID: "su-2", AccountID: "acc-su-2", Purpose: domain.LLMPurposeScanner, InputTokens: 5, CostMicros: domain.CostUnknown, Error: "timeout", At: t0.Add(time.Hour)},
			{ID: "su-3", AccountID: "acc-su-1", Purpose: domain.LLMPurposeAgent, CostMicros: 7, At: t0.Add(24 * time.Hour)}, // at to: out
			{ID: "su-4", AccountID: "acc-su-3", Purpose: domain.LLMPurposeAgent, CostMicros: 99, At: t0},                    // other account
		}
		for _, c := range calls {
			if err := s.LLMCalls().Add(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		u, err := s.LLMCalls().UsageFor(ctx, []string{"acc-su-1", "acc-su-2", "acc-su-1"}, t0, t0.Add(24*time.Hour))
		want := domain.LLMUsage{Calls: 2, Failed: 1, InputTokens: 15, OutputTokens: 2, Cost: 1_000, CostUnknownCalls: 1}
		if err != nil || u != want {
			t.Fatalf("UsageFor = %+v %v, want %+v", u, err, want)
		}
		if u, err := s.LLMCalls().UsageFor(ctx, nil, t0, t0.Add(time.Hour)); err != nil || u != (domain.LLMUsage{}) {
			t.Fatalf("UsageFor no ids = %+v %v", u, err)
		}
		if u, err := s.LLMCalls().UsageFor(ctx, []string{"acc-none"}, t0, t0.Add(time.Hour)); err != nil || u != (domain.LLMUsage{}) {
			t.Fatalf("UsageFor nothing = %+v %v", u, err)
		}
	})

	t.Run("statement_schedules", func(t *testing.T) {
		r := s.StatementSchedules()
		a := domain.StatementSchedule{
			ID: "sch-1", OwnerID: owner.ID, Period: domain.PeriodWeekly, AccountIDs: []string{"acc-1", "acc-2"},
			Recipients: []string{"me@example.com", "Ünïcode@example.com"}, IncludeCombined: true, IncludeExchanges: true, Enabled: true,
			LastPeriod: "2026-W47", CreatedAt: t0, UpdatedAt: t0,
		}
		b := domain.StatementSchedule{
			ID: "sch-2", OwnerID: owner.ID, Period: domain.PeriodDaily, AccountIDs: []string{},
			Recipients: []string{"x@example.com"}, IncludeAccounts: true, CreatedAt: t0.Add(time.Second), UpdatedAt: t0.Add(time.Second),
		}
		for _, x := range []domain.StatementSchedule{a, b} {
			if err := r.Create(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Create(ctx, a); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate: %v", err)
		}
		if err := r.Create(ctx, domain.StatementSchedule{ID: "sch-x", OwnerID: "nobody", Period: domain.PeriodDaily, CreatedAt: t0, UpdatedAt: t0}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown owner: %v", err)
		}
		got, err := r.Get(ctx, a.ID)
		if err != nil || !reflect.DeepEqual(got, a) {
			t.Fatalf("Get = %+v %v\nwant %+v", got, err, a)
		}
		if _, err := r.Get(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Get unknown: %v", err)
		}
		if l, err := r.List(ctx, owner.ID); err != nil || len(l) != 2 || l[0].ID != a.ID || l[1].ID != b.ID || l[1].AccountIDs == nil {
			t.Fatalf("List = %+v %v", l, err)
		}
		if l, _ := r.List(ctx, "u-admin"); len(l) != 0 {
			t.Fatalf("List other owner = %+v", l)
		}
		if l, _ := r.List(ctx, ""); len(l) < 2 {
			t.Fatalf("List all = %+v", l)
		}

		a.Period, a.Recipients, a.AccountIDs, a.Enabled, a.IncludeAccounts, a.LastPeriod = domain.PeriodMonthly, []string{"new@example.com"}, nil, false, true, "2026-10"
		a.UpdatedAt = t0.Add(time.Minute)
		a.LastError = "ignored by Update"
		if err := r.Update(ctx, a); err != nil {
			t.Fatal(err)
		}
		got, _ = r.Get(ctx, a.ID)
		if got.Period != domain.PeriodMonthly || got.Enabled || !got.IncludeAccounts || len(got.AccountIDs) != 0 || got.Recipients[0] != "new@example.com" ||
			got.LastPeriod != "2026-10" || !got.UpdatedAt.Equal(a.UpdatedAt) || got.LastError != "" {
			t.Fatalf("after Update %+v", got)
		}
		if err := r.Update(ctx, domain.StatementSchedule{ID: "nope", Period: domain.PeriodDaily}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Update unknown: %v", err)
		}

		// MarkPeriod is a compare-and-set on the last period.
		at := t0.Add(time.Hour)
		if err := r.MarkPeriod(ctx, a.ID, "2026-10", "2026-11", at, strings.Repeat("e", 700)); err != nil {
			t.Fatal(err)
		}
		if err := r.MarkPeriod(ctx, a.ID, "2026-10", "2026-11", at, ""); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("stale MarkPeriod: %v", err)
		}
		if err := r.MarkPeriod(ctx, "nope", "", "2026-11", at, ""); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("MarkPeriod unknown: %v", err)
		}
		got, _ = r.Get(ctx, a.ID)
		if got.LastPeriod != "2026-11" || !got.LastSentAt.Equal(at) || len(got.LastError) != 500 {
			t.Fatalf("after MarkPeriod %+v", got)
		}
		if err := r.MarkManual(ctx, a.ID, time.Time{}, at); err != nil {
			t.Fatal(err)
		}
		if err := r.MarkManual(ctx, a.ID, time.Time{}, at.Add(time.Second)); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("stale MarkManual: %v", err)
		}
		if err := r.MarkManual(ctx, a.ID, at, at.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := r.MarkManual(ctx, "nope", time.Time{}, at); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("MarkManual unknown: %v", err)
		}
		if got, _ = r.Get(ctx, a.ID); !got.ManualSentAt.Equal(at.Add(time.Minute)) {
			t.Fatalf("after MarkManual %+v", got)
		}

		if err := r.Delete(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
		if err := r.Delete(ctx, b.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Delete twice: %v", err)
		}
		// Schedules go with their owner.
		if err := s.Users().Delete(ctx, owner.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Get(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("schedule survived its owner: %v", err)
		}
	})
}
