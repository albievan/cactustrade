package domain

import (
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("time zone %s: %v", name, err)
	}
	return loc
}

func TestBackupScheduleValidate(t *testing.T) {
	tests := []struct {
		name  string
		s     BackupSchedule
		field string // "" = valid
	}{
		{"off", BackupSchedule{Kind: BackupOff}, ""},
		{"hourly", BackupSchedule{Kind: BackupHourly, Keep: 24}, ""},
		{"every 1", BackupSchedule{Kind: BackupEvery, EveryHours: 1}, ""},
		{"every 24", BackupSchedule{Kind: BackupEvery, EveryHours: 24}, ""},
		{"every 0", BackupSchedule{Kind: BackupEvery, EveryHours: 0}, "everyHours"},
		{"every 25", BackupSchedule{Kind: BackupEvery, EveryHours: 25}, "everyHours"},
		{"daily", BackupSchedule{Kind: BackupDaily, At: "02:30", Zone: "Europe/London"}, ""},
		{"daily 23:59", BackupSchedule{Kind: BackupDaily, At: "23:59"}, ""},
		{"daily 24:00", BackupSchedule{Kind: BackupDaily, At: "24:00"}, "at"},
		{"daily 2:30", BackupSchedule{Kind: BackupDaily, At: "2:30"}, "at"},
		{"daily +1:30", BackupSchedule{Kind: BackupDaily, At: "+1:30"}, "at"},
		{"daily 12:60", BackupSchedule{Kind: BackupDaily, At: "12:60"}, "at"},
		{"daily empty", BackupSchedule{Kind: BackupDaily}, "at"},
		{"unknown kind", BackupSchedule{Kind: "weekly"}, "kind"},
		{"empty kind", BackupSchedule{}, "kind"},
		{"keep -1", BackupSchedule{Kind: BackupHourly, Keep: -1}, "keep"},
		{"keep 1000", BackupSchedule{Kind: BackupHourly, Keep: 1000}, ""},
		{"keep 1001", BackupSchedule{Kind: BackupHourly, Keep: 1001}, "keep"},
		{"zone path", BackupSchedule{Kind: BackupHourly, Zone: "../etc/passwd"}, "zone"},
		{"zone space", BackupSchedule{Kind: BackupHourly, Zone: "Europe/ London"}, "zone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Validate()
			if tt.field == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tt.field || !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want a ValidationError on %s", err, tt.field)
			}
		})
	}
}

func TestBackupScheduleEnabledAndString(t *testing.T) {
	tests := []struct {
		s    BackupSchedule
		on   bool
		text string
	}{
		{BackupSchedule{Kind: BackupOff}, false, "off"},
		{BackupSchedule{}, false, "off"},
		{BackupSchedule{Kind: BackupHourly, Keep: 3}, true, "hourly, keep 3"},
		{BackupSchedule{Kind: BackupEvery, EveryHours: 5, Zone: "UTC"}, true, "every 5 h UTC, keep 0"},
		{BackupSchedule{Kind: BackupDaily, At: "02:30", Zone: "Europe/London", Keep: 7}, true, "daily at 02:30 Europe/London, keep 7"},
	}
	for _, tt := range tests {
		if tt.s.Enabled() != tt.on || tt.s.String() != tt.text {
			t.Errorf("%+v: Enabled %v String %q, want %v %q", tt.s, tt.s.Enabled(), tt.s.String(), tt.on, tt.text)
		}
	}
}

func TestBackupScheduleNext(t *testing.T) {
	london := mustLoc(t, "Europe/London")
	ny := mustLoc(t, "America/New_York")
	tests := []struct {
		name  string
		s     BackupSchedule
		loc   *time.Location
		after time.Time
		want  time.Time
	}{
		{"off", BackupSchedule{Kind: BackupOff}, time.UTC, utc(2027, 1, 1, 0, 0), time.Time{}},
		{"invalid every", BackupSchedule{Kind: BackupEvery}, time.UTC, utc(2027, 1, 1, 0, 0), time.Time{}},
		{"hourly mid-hour", BackupSchedule{Kind: BackupHourly}, time.UTC, utc(2027, 1, 1, 10, 17), utc(2027, 1, 1, 11, 0)},
		{"hourly on the hour is strictly after", BackupSchedule{Kind: BackupHourly}, time.UTC, utc(2027, 1, 1, 11, 0), utc(2027, 1, 1, 12, 0)},
		{"nil location is UTC", BackupSchedule{Kind: BackupHourly}, nil, utc(2027, 1, 1, 23, 30), utc(2027, 1, 2, 0, 0)},
		{"every 5 at 00:00", BackupSchedule{Kind: BackupEvery, EveryHours: 5}, time.UTC, utc(2027, 1, 1, 0, 0), utc(2027, 1, 1, 5, 0)},
		{"every 5 at 16:00", BackupSchedule{Kind: BackupEvery, EveryHours: 5}, time.UTC, utc(2027, 1, 1, 16, 0), utc(2027, 1, 1, 20, 0)},
		{"every 5 after 20:00 wraps to midnight", BackupSchedule{Kind: BackupEvery, EveryHours: 5}, time.UTC, utc(2027, 1, 1, 20, 0), utc(2027, 1, 2, 0, 0)},
		{"every 24 is midnight", BackupSchedule{Kind: BackupEvery, EveryHours: 24}, time.UTC, utc(2027, 1, 1, 0, 1), utc(2027, 1, 2, 0, 0)},
		{"daily later today", BackupSchedule{Kind: BackupDaily, At: "02:30"}, time.UTC, utc(2027, 1, 1, 1, 0), utc(2027, 1, 1, 2, 30)},
		{"daily tomorrow", BackupSchedule{Kind: BackupDaily, At: "02:30"}, time.UTC, utc(2027, 1, 1, 2, 30), utc(2027, 1, 2, 2, 30)},
		{"daily London winter", BackupSchedule{Kind: BackupDaily, At: "02:30"}, london, utc(2027, 1, 10, 0, 0), utc(2027, 1, 10, 2, 30)},
		{"daily London summer", BackupSchedule{Kind: BackupDaily, At: "02:30"}, london, utc(2027, 7, 10, 0, 0), utc(2027, 7, 10, 1, 30)},
		// 2027-03-28: London's clocks jump from 01:00 GMT to 02:00 BST (01:00 UTC); 01:30 does
		// not exist and runs at the jump.
		{"London 01:30 skipped", BackupSchedule{Kind: BackupDaily, At: "01:30"}, london, utc(2027, 3, 27, 12, 0), utc(2027, 3, 28, 1, 0)},
		{"London day after the jump", BackupSchedule{Kind: BackupDaily, At: "01:30"}, london, utc(2027, 3, 28, 1, 0), utc(2027, 3, 29, 0, 30)},
		// 2027-10-31: 01:00–02:00 BST then 01:00–02:00 GMT; 01:30 runs once.
		{"London 01:30 repeated", BackupSchedule{Kind: BackupDaily, At: "01:30"}, london, utc(2027, 10, 30, 12, 0), utc(2027, 10, 31, 0, 30)},
		{"London 01:30 repeated, after the first", BackupSchedule{Kind: BackupDaily, At: "01:30"}, london, utc(2027, 10, 31, 0, 30), utc(2027, 11, 1, 1, 30)},
		// 2027-03-14: New York jumps from 02:00 EST to 03:00 EDT (07:00 UTC).
		{"New York 02:30 skipped", BackupSchedule{Kind: BackupDaily, At: "02:30"}, ny, utc(2027, 3, 13, 12, 0), utc(2027, 3, 14, 7, 0)},
		{"hourly over London's jump", BackupSchedule{Kind: BackupHourly}, london, utc(2027, 3, 28, 0, 0), utc(2027, 3, 28, 1, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.s.Next(tt.after, tt.loc)
			if !got.Equal(tt.want) {
				t.Fatalf("Next(%s) = %s, want %s", tt.after.UTC(), got.UTC(), tt.want.UTC())
			}
		})
	}
}

func TestBackupEveryFiveHoursSlots(t *testing.T) {
	s := BackupSchedule{Kind: BackupEvery, EveryHours: 5}
	at := utc(2027, 1, 1, 0, 0).Add(-time.Second)
	var hours []int
	for range 6 {
		at = s.Next(at, time.UTC)
		hours = append(hours, at.Hour())
	}
	if want := []int{0, 5, 10, 15, 20, 0}; !slices.Equal(hours, want) {
		t.Fatalf("every 5 h runs at %v, want %v", hours, want)
	}
}

func TestBackupDue(t *testing.T) {
	london := mustLoc(t, "Europe/London")
	daily := BackupSchedule{Kind: BackupDaily, At: "02:30", Zone: "Europe/London"}
	tests := []struct {
		name      string
		s         BackupSchedule
		last, now time.Time
		want      bool
	}{
		{"before the slot", daily, utc(2027, 1, 9, 3, 0), utc(2027, 1, 10, 2, 29), false},
		{"at the slot", daily, utc(2027, 1, 9, 3, 0), utc(2027, 1, 10, 2, 30), true},
		{"after the slot", daily, utc(2027, 1, 9, 3, 0), utc(2027, 1, 10, 9, 0), true},
		{"already ran", daily, utc(2027, 1, 10, 2, 30), utc(2027, 1, 10, 9, 0), false},
		{"three missed slots run once", daily, utc(2027, 1, 6, 3, 0), utc(2027, 1, 10, 9, 0), true},
		{"off", BackupSchedule{Kind: BackupOff}, utc(2027, 1, 1, 0, 0), utc(2027, 2, 1, 0, 0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.Due(tt.last, tt.now, london); got != tt.want {
				t.Fatalf("Due = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBackupNextProperties: for random schedules, zones and instants, Next is strictly
// later and never more than one period plus an hour (a daylight-saving jump) away.
func TestBackupNextProperties(t *testing.T) {
	zones := []*time.Location{
		time.UTC, mustLoc(t, "Europe/London"), mustLoc(t, "America/New_York"),
		mustLoc(t, "Australia/Lord_Howe"), mustLoc(t, "America/Santiago"), mustLoc(t, "Asia/Kolkata"),
	}
	r := rand.New(rand.NewPCG(48, 1))
	base := utc(2026, 1, 1, 0, 0)
	for i := range 10000 {
		var s BackupSchedule
		var period time.Duration
		switch r.IntN(3) {
		case 0:
			s, period = BackupSchedule{Kind: BackupHourly}, time.Hour
		case 1:
			n := 1 + r.IntN(24)
			s, period = BackupSchedule{Kind: BackupEvery, EveryHours: n}, time.Duration(n)*time.Hour
		default:
			s = BackupSchedule{Kind: BackupDaily, At: time.Date(2000, 1, 1, r.IntN(24), r.IntN(60), 0, 0, time.UTC).Format("15:04")}
			period = 24 * time.Hour
		}
		loc := zones[r.IntN(len(zones))]
		at := base.Add(time.Duration(r.Int64N(int64(3 * 365 * 24 * time.Hour))))
		got := s.Next(at, loc)
		if !got.After(at) || got.After(at.Add(period+time.Hour)) {
			t.Fatalf("case %d: %s in %s: Next(%s) = %s", i, s, loc, at, got)
		}
	}
}

// TestBackupDueNoSlotTwice simulates a week of one-minute ticks across a daylight-saving
// change: every slot runs exactly once, and a run never comes before its slot.
func TestBackupDueNoSlotTwice(t *testing.T) {
	london := mustLoc(t, "Europe/London")
	for _, s := range []BackupSchedule{
		{Kind: BackupHourly},
		{Kind: BackupEvery, EveryHours: 5},
		{Kind: BackupDaily, At: "01:30"},
	} {
		for _, start := range []time.Time{utc(2027, 3, 25, 0, 0), utc(2027, 10, 28, 0, 0)} {
			last := start
			ran := map[time.Time]bool{}
			for now := start; now.Before(start.Add(7 * 24 * time.Hour)); now = now.Add(time.Minute) {
				if !s.Due(last, now, london) {
					continue
				}
				slot := s.Next(last, london)
				if ran[slot] || slot.After(now) {
					t.Fatalf("%s from %s: slot %s ran twice or early (now %s)", s, start, slot, now)
				}
				ran[slot] = true
				last = now
			}
			if len(ran) == 0 {
				t.Fatalf("%s: nothing ran in a week", s)
			}
		}
	}
}

func TestBackupNameRoundTrip(t *testing.T) {
	at := time.Date(2027, 3, 28, 1, 2, 3, 999, time.FixedZone("x", 3600))
	tests := []struct {
		ext, suffix, want string
	}{
		{BackupExtSQLite, "", "cactustrade-20270328T000203Z.db.age"},
		{BackupExtMariaDB, "", "cactustrade-20270328T000203Z.sql.gz.age"},
		{BackupExtSQLite, "0a9f", "cactustrade-20270328T000203Z-0a9f.db.age"},
	}
	for _, tt := range tests {
		name, err := BackupName(at, tt.ext, tt.suffix)
		if err != nil || name != tt.want {
			t.Fatalf("BackupName(%s, %s) = %q %v, want %q", tt.ext, tt.suffix, name, err, tt.want)
		}
		p, ok := ParseBackupName(name)
		if !ok || !p.At.Equal(at.Truncate(time.Second)) || p.Ext != tt.ext || p.Suffix != tt.suffix {
			t.Fatalf("ParseBackupName(%q) = %+v %v", name, p, ok)
		}
	}
	for _, bad := range [][2]string{{"sql", ""}, {"db", "ABCD"}, {"db", "12345"}, {"db", "../x"}} {
		if n, err := BackupName(at, bad[0], bad[1]); err == nil {
			t.Errorf("BackupName(%q, %q) = %q, want an error", bad[0], bad[1], n)
		}
	}
}

func TestParseBackupNameRefuses(t *testing.T) {
	for _, n := range []string{
		"", "cactustrade-20270328T000203Z.db", "cactustrade-20270328T000203Z.sql.gz",
		"cactustrade-20270328T000203Z.db.age.partial", ".partial-abcd", "../cactustrade-20270328T000203Z.db.age",
		"/backups/cactustrade-20270328T000203Z.db.age", "backups/cactustrade-20270328T000203Z.db.age",
		"cactustrade-20270328T000203Z-ABCD.db.age", "cactustrade-20270328T000203Z-abc.db.age",
		"cactustrade-20271328T000203Z.db.age", "cactustrade-20270328T250203Z.db.age",
		"cactustrade-2027-03-28.sql.gz.age", "cactustrade-20270328T000203Z.sql.age", "Cactustrade-20270328T000203Z.db.age",
		"cactustrade-20270328T000203Z.db.age\n",
	} {
		if p, ok := ParseBackupName(n); ok {
			t.Errorf("ParseBackupName(%q) = %+v, want refused", n, p)
		}
	}
}

func TestPruneSelect(t *testing.T) {
	n := func(day int) string {
		s, _ := BackupName(utc(2027, 1, day, 3, 0), BackupExtSQLite, "")
		return s
	}
	tests := []struct {
		name  string
		names []string
		keep  int
		want  []string
	}{
		{"keep 0 keeps all", []string{n(1), n(2), n(3)}, 0, nil},
		{"keep more than there are", []string{n(1), n(2)}, 5, nil},
		{"oldest first", []string{n(3), n(1), n(4), n(2)}, 2, []string{n(1), n(2)}},
		{"strangers ignored", []string{"notes.txt", n(2), ".partial-1234", n(1), "cactustrade-x.db.age"}, 1, []string{n(1)}},
		{"duplicates once", []string{n(1), n(1), n(2)}, 1, []string{n(1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PruneSelect(tt.names, tt.keep); !slices.Equal(got, tt.want) {
				t.Fatalf("PruneSelect = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPruneSelectProperties: random mixes of names never yield an unparsed name, never more
// than len(parsed) − keep, and keep 0 yields none.
func TestPruneSelectProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(48, 2))
	for i := range 10000 {
		var names []string
		parsed := map[string]bool{}
		for range r.IntN(30) {
			if r.IntN(4) == 0 {
				names = append(names, []string{"x.db.age", "cactustrade-.db.age", "../a", ".partial-0000"}[r.IntN(4)])
				continue
			}
			ext := []string{BackupExtSQLite, BackupExtMariaDB}[r.IntN(2)]
			s, err := BackupName(utc(2027, 1, 1, 0, 0).Add(time.Duration(r.IntN(100000))*time.Minute), ext, "")
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, s)
			parsed[s] = true
		}
		keep := r.IntN(10)
		got := PruneSelect(names, keep)
		if keep == 0 && len(got) != 0 {
			t.Fatalf("case %d: keep 0 pruned %v", i, got)
		}
		if keep > 0 && len(got) > max(0, len(parsed)-keep) {
			t.Fatalf("case %d: pruned %d of %d with keep %d", i, len(got), len(parsed), keep)
		}
		if keep > 0 && len(parsed) > keep && len(got) != len(parsed)-keep {
			t.Fatalf("case %d: pruned %d of %d with keep %d", i, len(got), len(parsed), keep)
		}
		for _, g := range got {
			if !parsed[g] {
				t.Fatalf("case %d: pruned unparsed %q", i, g)
			}
		}
	}
}
