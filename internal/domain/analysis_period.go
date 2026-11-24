package domain

import (
	"strings"
	"time"
)

// The analysis page (R31) looks at a chosen period of whole risk days (22:00 UTC to 22:00
// UTC, OD4), like a trading statement (R30), but over any range: today, this week, this
// month, last month, a custom range or everything kept. The figures are grouped by date in
// buckets whose size follows the length of the period.

// AnalysisPreset names how the period was chosen.
type AnalysisPreset string

// Analysis presets.
const (
	AnalysisPresetToday     AnalysisPreset = "today"      // today's risk day
	AnalysisPresetWeek      AnalysisPreset = "week"       // the Monday to Friday risk days of this ISO week (as R30's weekly statement)
	AnalysisPresetMonth     AnalysisPreset = "month"      // this calendar month's risk days
	AnalysisPresetLastMonth AnalysisPreset = "last_month" // the previous calendar month's risk days
	AnalysisPresetCustom    AnalysisPreset = "custom"     // from and to, both risk days, inclusive
	AnalysisPresetAll       AnalysisPreset = "all"        // the last AnalysisMaxDays risk days (everything equity is kept for)
)

// AnalysisPresets lists the presets in display order.
var AnalysisPresets = []AnalysisPreset{AnalysisPresetToday, AnalysisPresetWeek, AnalysisPresetMonth, AnalysisPresetLastMonth, AnalysisPresetCustom, AnalysisPresetAll}

// AnalysisMaxDays is the longest period an analysis covers: the days equity snapshots and
// decisions are kept (EquityRetentionDays).
const AnalysisMaxDays = EquityRetentionDays

// Bucket-size rule (R31-b): one bar per risk day up to AnalysisDailyMaxDays days, one per
// ISO week up to AnalysisWeeklyMaxDays (about six months), one per calendar month beyond.
const (
	AnalysisDailyMaxDays  = 31
	AnalysisWeeklyMaxDays = 186
)

// AnalysisBucket is the size of the date groups.
type AnalysisBucket string

// Bucket sizes.
const (
	BucketDay   AnalysisBucket = "day"
	BucketWeek  AnalysisBucket = "week"
	BucketMonth AnalysisBucket = "month"
)

// BucketFor is the bucket size for a period of days risk days (R31-b).
func BucketFor(days int) AnalysisBucket {
	switch {
	case days <= AnalysisDailyMaxDays:
		return BucketDay
	case days <= AnalysisWeeklyMaxDays:
		return BucketWeek
	}
	return BucketMonth
}

// noun names one bucket ("day", "week", "month").
func (b AnalysisBucket) noun() string { return string(b) }

// AnalysisWindow is the period of an analysis: the risk days FirstDay to LastDay, which run
// from From (inclusive) to To (exclusive). To may be in the future while today's risk day
// runs.
type AnalysisWindow struct {
	Preset            AnalysisPreset
	From, To          time.Time
	FirstDay, LastDay RiskDate
	Days              int
	Bucket            AnalysisBucket
}

// ParseAnalysisPreset validates a preset name ("" = this month, or custom when a range is
// given; the caller decides).
func ParseAnalysisPreset(s string) (AnalysisPreset, error) {
	for _, p := range AnalysisPresets {
		if string(p) == s {
			return p, nil
		}
	}
	return "", Invalid("preset", "today, week, month, last_month, custom or all")
}

// AnalysisWindowFor resolves a preset at now. A custom range needs from and to (risk days,
// YYYY-MM-DD, inclusive): from ≤ to, to not after today, at most AnalysisMaxDays days and
// none older than that. The other presets ignore from and to.
func AnalysisWindowFor(p AnalysisPreset, from, to string, now time.Time) (AnalysisWindow, error) {
	today := RiskDay(now).date()
	var first, last time.Time
	switch p {
	case AnalysisPresetToday:
		first, last = today, today
	case AnalysisPresetWeek:
		w := windowOf(PeriodWeekly, today)
		first, last = w.FirstDay.date(), w.LastDay.date()
	case AnalysisPresetMonth:
		w := windowOf(PeriodMonthly, today)
		first, last = w.FirstDay.date(), w.LastDay.date()
	case AnalysisPresetLastMonth:
		w := windowOf(PeriodMonthly, today).Previous()
		first, last = w.FirstDay.date(), w.LastDay.date()
	case AnalysisPresetAll:
		first, last = today.AddDate(0, 0, -(AnalysisMaxDays-1)), today
	case AnalysisPresetCustom:
		f, err := parseDay("from", from)
		if err != nil {
			return AnalysisWindow{}, err
		}
		t, err := parseDay("to", to)
		if err != nil {
			return AnalysisWindow{}, err
		}
		switch {
		case t.Before(f):
			return AnalysisWindow{}, Invalid("to", "must not be before from")
		case t.After(today):
			return AnalysisWindow{}, Invalid("to", "must not be after today ("+string(riskDate(today))+")")
		case f.Before(today.AddDate(0, 0, -(AnalysisMaxDays - 1))):
			return AnalysisWindow{}, Invalid("from", "at most 400 days back")
		}
		first, last = f, t
	default:
		return AnalysisWindow{}, Invalid("preset", "today, week, month, last_month, custom or all")
	}
	days := int(last.Sub(first)/(24*time.Hour)) + 1
	return AnalysisWindow{
		Preset: p, From: dayStart(first), To: dayStart(last).Add(24 * time.Hour),
		FirstDay: riskDate(first), LastDay: riskDate(last), Days: days, Bucket: BucketFor(days),
	}, nil
}

func parseDay(field, s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, Invalid(field, "is required for a custom range (YYYY-MM-DD)")
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, Invalid(field, "a date such as 2026-11-24")
	}
	return d, nil
}

// Contains reports whether t falls in the window.
func (w AnalysisWindow) Contains(t time.Time) bool { return !t.Before(w.From) && t.Before(w.To) }

// Title names the period for people: "Tuesday 24 November 2026", "23–27 November 2026",
// "November 2026", "28 October 2025 – 24 November 2026".
func (w AnalysisWindow) Title() string {
	first, last := w.FirstDay.date(), w.LastDay.date()
	switch {
	case first.Equal(last):
		return first.Format("Monday 2 January 2006")
	case first.Day() == 1 && last.AddDate(0, 0, 1).Day() == 1 && first.Month() == last.Month() && first.Year() == last.Year():
		return first.Format("January 2006")
	case first.Year() != last.Year():
		return first.Format("2 January 2006") + " – " + last.Format("2 January 2006")
	case first.Month() != last.Month():
		return first.Format("2 January") + " – " + last.Format("2 January 2006")
	}
	return first.Format("2") + "–" + last.Format("2 January 2006")
}

// Label names the preset for people ("This week").
func (p AnalysisPreset) Label() string {
	switch p {
	case AnalysisPresetToday:
		return "Today"
	case AnalysisPresetWeek:
		return "This week"
	case AnalysisPresetMonth:
		return "This month"
	case AnalysisPresetLastMonth:
		return "Last month"
	case AnalysisPresetAll:
		return "Last 400 days"
	}
	return "Custom range"
}

// bucketOf is the key and label of the date bucket holding the risk day d (a UTC date).
func (b AnalysisBucket) bucketOf(d time.Time) (key, label string) {
	switch b {
	case BucketWeek:
		w := windowOf(PeriodWeekly, d)
		return w.Key, "w/c " + w.FirstDay.date().Format("2 Jan")
	case BucketMonth:
		w := windowOf(PeriodMonthly, d)
		return w.Key, w.FirstDay.date().Format("Jan 2006")
	}
	return d.Format("2006-01-02"), d.Format("Mon 2 Jan")
}

// buckets lists the date buckets of the window in time order. Daily buckets leave out
// Saturday and Sunday risk days (no supported market trades on them; R30-b) unless a trade
// closed on one (has).
func (w AnalysisWindow) buckets(has map[string]bool) []AnalysisGroup {
	var out []AnalysisGroup
	seen := map[string]bool{}
	for d := w.FirstDay.date(); !d.After(w.LastDay.date()); d = d.AddDate(0, 0, 1) {
		key, label := w.Bucket.bucketOf(d)
		if seen[key] {
			continue
		}
		if wd := d.Weekday(); w.Bucket == BucketDay && (wd == time.Saturday || wd == time.Sunday) && !has[key] {
			continue
		}
		seen[key] = true
		out = append(out, AnalysisGroup{Key: key, Label: label})
	}
	return out
}

// AnalysisFileName is the PDF's file name: cactusTrade-analysis-<first day>-<last day>.pdf
// (ASCII letters, digits, '.' and '-' only, so no header needs quoting).
func AnalysisFileName(w AnalysisWindow) string {
	return "cactusTrade-analysis-" + strings.ToLower(string(w.FirstDay)) + "-to-" + strings.ToLower(string(w.LastDay)) + ".pdf"
}
