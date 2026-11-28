package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Scheduled backups from the web app (R48). The schedule, the file names and the pruning
// rule are pure: the clock and the time zone are passed in.

// BackupKind is how often a scheduled backup runs.
type BackupKind string

// The backup schedule kinds.
const (
	BackupOff    BackupKind = "off"    // no scheduled backup ("Back up now" still works)
	BackupHourly BackupKind = "hourly" // at every full local hour
	BackupEvery  BackupKind = "every"  // every N hours, on the hour, N-hour steps from local midnight
	BackupDaily  BackupKind = "daily"  // once a day at HH:MM local time
)

// Backup schedule bounds.
const (
	MaxBackupKeep       = 1000
	MaxBackupEveryHours = 24
)

// Backup file formats: the part of the name between the time and ".age".
const (
	BackupExtSQLite  = "db"     // VACUUM INTO, then age (restore: cactustrade restore)
	BackupExtMariaDB = "sql.gz" // a logical dump, gzip, then age (restore: deploy.sh --restore)
)

// Settings keys of the backup scheduler.
const (
	SettingBackupSchedule   = "backup.schedule"
	SettingBackupRecipients = "backup.recipients"
	SettingBackupLast       = "backup.last"
)

// BackupSchedule is when the app backs its database up by itself (R48).
type BackupSchedule struct {
	Kind BackupKind `json:"kind"`
	// EveryHours is N for BackupEvery (1–24): the backups run at the local hours h with
	// h % N == 0, counted from local midnight.
	EveryHours int `json:"everyHours,omitempty"`
	// At is the local time of a daily backup, "HH:MM" (24-hour clock).
	At string `json:"at,omitempty"`
	// Zone is the IANA time zone the schedule is read in ("" = the deployment's).
	Zone string `json:"zone,omitempty"`
	// Keep is how many of the newest backup files are kept after a successful backup
	// (0 = all; at most MaxBackupKeep).
	Keep int `json:"keep"`
}

// Validate checks the schedule's fields for its kind. The zone is checked by name only; the
// caller loads it (that needs the time zone database).
func (s BackupSchedule) Validate() error {
	switch s.Kind {
	case BackupOff, BackupHourly:
	case BackupEvery:
		if s.EveryHours < 1 || s.EveryHours > MaxBackupEveryHours {
			return Invalid("everyHours", "a whole number of hours from 1 to 24")
		}
	case BackupDaily:
		if _, _, ok := parseHHMM(s.At); !ok {
			return Invalid("at", "a time of day as HH:MM, 00:00 to 23:59")
		}
	default:
		return Invalid("kind", "off, hourly, every or daily")
	}
	if s.Keep < 0 || s.Keep > MaxBackupKeep {
		return Invalid("keep", "from 0 (keep every file) to 1000")
	}
	if len(s.Zone) > 64 || strings.ContainsAny(s.Zone, " \t\r\n\\") || strings.Contains(s.Zone, "..") {
		return Invalid("zone", "an IANA time zone such as Europe/London")
	}
	return nil
}

// Enabled reports whether the schedule runs backups by itself.
func (s BackupSchedule) Enabled() bool { return s.Kind != "" && s.Kind != BackupOff }

// String is the schedule in words, for the audit log.
func (s BackupSchedule) String() string {
	var w string
	switch s.Kind {
	case BackupHourly:
		w = "hourly"
	case BackupEvery:
		w = fmt.Sprintf("every %d h", s.EveryHours)
	case BackupDaily:
		w = "daily at " + s.At
	default:
		return "off"
	}
	if s.Zone != "" {
		w += " " + s.Zone
	}
	return w + fmt.Sprintf(", keep %d", s.Keep)
}

// parseHHMM reads "HH:MM" (two digits each).
func parseHHMM(v string) (h, m int, ok bool) {
	if len(v) != 5 || v[2] != ':' {
		return 0, 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if v[i] < '0' || v[i] > '9' {
			return 0, 0, false
		}
	}
	h, _ = strconv.Atoi(v[:2])
	m, _ = strconv.Atoi(v[3:])
	if h > 23 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// slots are the local wall-clock times (hour, minute) of one day's backups.
func (s BackupSchedule) slots() [][2]int {
	switch s.Kind {
	case BackupHourly:
		out := make([][2]int, 0, 24)
		for h := range 24 {
			out = append(out, [2]int{h, 0})
		}
		return out
	case BackupEvery:
		n := s.EveryHours
		if n < 1 || n > MaxBackupEveryHours {
			return nil
		}
		var out [][2]int
		for h := 0; h < 24; h += n {
			out = append(out, [2]int{h, 0})
		}
		return out
	case BackupDaily:
		h, m, ok := parseHHMM(s.At)
		if !ok {
			return nil
		}
		return [][2]int{{h, m}}
	default:
		return nil
	}
}

// Next returns the first backup time strictly after after, read in loc (nil = UTC). It is
// the zero time when the schedule is off or invalid. A local time that daylight saving
// skips runs at the first valid instant after it (the moment the clocks jump); a local time
// that occurs twice runs once.
func (s BackupSchedule) Next(after time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	slots := s.slots()
	if len(slots) == 0 {
		return time.Time{}
	}
	local := after.In(loc)
	y, mo, d := local.Date()
	var best time.Time
	for day := -1; day <= 2; day++ {
		for _, sl := range slots {
			t := resolveLocal(y, mo, d+day, sl[0], sl[1], loc)
			if t.After(after) && (best.IsZero() || t.Before(best)) {
				best = t
			}
		}
	}
	return best
}

// resolveLocal is the instant of the local wall-clock time in loc. A wall-clock time that
// does not exist (skipped by a daylight-saving jump) resolves to the instant of the jump,
// the first valid instant after it.
func resolveLocal(y int, mo time.Month, d, h, mi int, loc *time.Location) time.Time {
	t := time.Date(y, mo, d, h, mi, 0, 0, loc)
	want := time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	got := wall(t.In(loc))
	switch {
	case got.Equal(want):
		// A wall-clock time that occurs twice (the clocks went back): the first one.
		if start, _ := t.ZoneBounds(); !start.IsZero() {
			_, prevOff := start.Add(-time.Nanosecond).Zone()
			if _, off := t.Zone(); prevOff > off {
				if alt := t.Add(-time.Duration(prevOff-off) * time.Second); alt.Before(start) && wall(alt.In(loc)).Equal(want) {
					return alt
				}
			}
		}
		return t
	case got.Before(want): // Go chose the zone before the jump: the jump ends this zone
		_, end := t.ZoneBounds()
		if !end.IsZero() {
			return end
		}
	default: // Go chose the zone after the jump: the jump starts it
		start, _ := t.ZoneBounds()
		if !start.IsZero() {
			return start
		}
	}
	return t
}

// wall is t's wall-clock date and time (to the minute) as a UTC time, for comparing.
func wall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
}

// Due reports whether a backup is due at now: a slot lies after last (the previous run, or
// when the schedule was set) and at or before now. However many slots were missed, one run
// covers them all (no burst after a downtime).
func (s BackupSchedule) Due(last, now time.Time, loc *time.Location) bool {
	n := s.Next(last, loc)
	return !n.IsZero() && !n.After(now)
}

// BackupFile is one encrypted backup file in the backup folder.
type BackupFile struct {
	Name string    `json:"name"`
	Size int64     `json:"size"`
	At   time.Time `json:"at"` // when it was taken (from its name, UTC)
}

// BackupRun is the outcome of the latest backup run (the setting backup.last).
type BackupRun struct {
	At      time.Time `json:"at"`
	Trigger string    `json:"trigger"` // schedule | manual | reset
	OK      bool      `json:"ok"`
	File    string    `json:"file,omitempty"`
	Bytes   int64     `json:"bytes,omitempty"`
	Ms      int64     `json:"ms,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Backup run triggers.
const (
	BackupTriggerSchedule = "schedule"
	BackupTriggerManual   = "manual"
	BackupTriggerReset    = "reset"
)

const backupTimeLayout = "20060102T150405Z"

var backupNameRE = regexp.MustCompile(`^cactustrade-(\d{8}T\d{6}Z)(-[0-9a-f]{4})?\.(db|sql\.gz)\.age$`)

// BackupName is the file name of a backup taken at at: cactustrade-<UTC>[-suffix].<ext>.age.
// ext is BackupExtSQLite or BackupExtMariaDB; suffix is "" or four lower-case hex digits
// (added when the plain name is taken).
func BackupName(at time.Time, ext, suffix string) (string, error) {
	if ext != BackupExtSQLite && ext != BackupExtMariaDB {
		return "", Invalid("ext", "db or sql.gz")
	}
	name := "cactustrade-" + at.UTC().Format(backupTimeLayout)
	if suffix != "" {
		name += "-" + suffix
	}
	name += "." + ext + ".age"
	if _, ok := ParseBackupName(name); !ok {
		return "", Invalid("suffix", "four lower-case hex digits")
	}
	return name, nil
}

// ParsedBackupName is what a backup file name says.
type ParsedBackupName struct {
	At     time.Time
	Suffix string
	Ext    string
}

// ParseBackupName reads a backup file name; ok is false for every other name (a path, a
// partial file, a file someone else put in the folder).
func ParseBackupName(name string) (ParsedBackupName, bool) {
	m := backupNameRE.FindStringSubmatch(name)
	if m == nil {
		return ParsedBackupName{}, false
	}
	at, err := time.Parse(backupTimeLayout, m[1])
	if err != nil {
		return ParsedBackupName{}, false
	}
	return ParsedBackupName{At: at.UTC(), Suffix: strings.TrimPrefix(m[2], "-"), Ext: m[3]}, true
}

// PruneSelect returns the backup files to delete so that the newest keep remain: the
// oldest first. keep 0 keeps all. Names that do not parse are never returned, nor is
// protect (the file just written, "" = none): it always counts as one of the keep, even when
// an older file's name carries a later time (a clock set back), so a backup never prunes
// itself.
func PruneSelect(names []string, keep int, protect string) []string {
	if keep <= 0 {
		return nil
	}
	type named struct {
		name string
		at   time.Time
	}
	var ok []named
	seen := map[string]bool{}
	if _, good := ParseBackupName(protect); good {
		seen[protect] = true
		keep--
	}
	for _, n := range names {
		if p, good := ParseBackupName(n); good && !seen[n] {
			seen[n] = true
			ok = append(ok, named{n, p.At})
		}
	}
	if len(ok) <= keep {
		return nil
	}
	sort.Slice(ok, func(i, j int) bool {
		if !ok[i].at.Equal(ok[j].at) {
			return ok[i].at.Before(ok[j].at)
		}
		return ok[i].name < ok[j].name
	})
	out := make([]string, 0, len(ok)-keep)
	for _, n := range ok[:len(ok)-keep] {
		out = append(out, n.name)
	}
	return out
}
