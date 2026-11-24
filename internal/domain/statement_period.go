package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Trading statements (R30): the periods a statement covers and when a scheduled statement is
// sent. Every period is made of whole risk days (22:00 UTC to 22:00 UTC, OD4), so the
// boundaries are in UTC and never move with daylight saving time.

// StatementPeriod is how long a statement covers.
type StatementPeriod string

// Statement periods.
const (
	// PeriodDaily is one risk day. Scheduled daily statements are sent for Monday to Friday
	// risk days only: no supported market trades on a Saturday or Sunday risk day.
	PeriodDaily StatementPeriod = "daily"
	// PeriodWeekly is the Monday to Friday risk days of one ISO week.
	PeriodWeekly StatementPeriod = "weekly"
	// PeriodMonthly is the risk days of one calendar month.
	PeriodMonthly StatementPeriod = "monthly"
)

// StatementPeriods lists the periods in display order.
var StatementPeriods = []StatementPeriod{PeriodDaily, PeriodWeekly, PeriodMonthly}

// ParseStatementPeriod validates a period name.
func ParseStatementPeriod(s string) (StatementPeriod, error) {
	p := StatementPeriod(s)
	if !p.Valid() {
		return "", Invalid("period", "daily, weekly or monthly")
	}
	return p, nil
}

// Valid reports whether p is a known period.
func (p StatementPeriod) Valid() bool {
	return p == PeriodDaily || p == PeriodWeekly || p == PeriodMonthly
}

// Label names the period for people ("Daily").
func (p StatementPeriod) Label() string {
	switch p {
	case PeriodWeekly:
		return "Weekly"
	case PeriodMonthly:
		return "Monthly"
	}
	return "Daily"
}

// noun is the period as a noun ("day", "week", "month").
func (p StatementPeriod) noun() string {
	switch p {
	case PeriodWeekly:
		return "week"
	case PeriodMonthly:
		return "month"
	}
	return "day"
}

// Send times after a period closes (UTC): a daily statement 30 minutes after the 22:00 UTC
// risk-day boundary; a weekly one on Saturday at 06:00; a monthly one on the 1st at 06:00.
const (
	dailySendDelay   = 30 * time.Minute
	weeklySendDelay  = 8 * time.Hour // Friday 22:00 + 8 h = Saturday 06:00
	monthlySendDelay = 8 * time.Hour // last day 22:00 + 8 h = the 1st 06:00
)

// StatementWindow is one statement period: the risk days FirstDay to LastDay, which run from
// From (inclusive) to To (exclusive). Key names the period uniquely within its kind
// ("2026-11-24", "2026-W48", "2026-11"); keys of one kind compare in time order as strings.
type StatementWindow struct {
	Period            StatementPeriod
	From, To          time.Time
	FirstDay, LastDay RiskDate
	Key               string
}

// dayStart is the start of the risk day named by the UTC date d (00:00): 22:00 UTC the day
// before.
func dayStart(d time.Time) time.Time { return d.Add(-(24 - RiskDayStartHourUTC) * time.Hour) }

// riskDate names the risk day of the UTC date d.
func riskDate(d time.Time) RiskDate { return RiskDate(d.Format("2006-01-02")) }

// ParseRiskDate reads "YYYY-MM-DD".
func ParseRiskDate(s string) (RiskDate, error) {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return "", Invalid("from", "a date such as 2026-11-24")
	}
	return riskDate(d), nil
}

// date is the risk day's UTC date at 00:00 (RiskDate values are always well formed here).
func (d RiskDate) date() time.Time {
	t, err := time.Parse("2006-01-02", string(d))
	if err != nil {
		return time.Time{}
	}
	return t
}

// WindowFor returns the period of kind p that holds the risk day day: that day (daily), the
// Monday to Friday of its ISO week (weekly) or its calendar month (monthly).
func WindowFor(p StatementPeriod, day RiskDate) (StatementWindow, error) {
	if !p.Valid() {
		return StatementWindow{}, Invalid("period", "daily, weekly or monthly")
	}
	d := day.date()
	if d.IsZero() {
		return StatementWindow{}, Invalid("from", "a date such as 2026-11-24")
	}
	return windowOf(p, d), nil
}

func windowOf(p StatementPeriod, d time.Time) StatementWindow {
	first, last := d, d
	key := d.Format("2006-01-02")
	switch p {
	case PeriodWeekly:
		first = d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)) // Monday
		last = first.AddDate(0, 0, 4)                          // Friday
		y, w := first.ISOWeek()
		key = fmt.Sprintf("%04d-W%02d", y, w)
	case PeriodMonthly:
		first = time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
		last = first.AddDate(0, 1, -1)
		key = first.Format("2006-01")
	}
	return StatementWindow{
		Period: p, From: dayStart(first), To: dayStart(last).Add(24 * time.Hour),
		FirstDay: riskDate(first), LastDay: riskDate(last), Key: key,
	}
}

// SendAt is when a scheduled statement for w is sent (after the period has closed).
func (w StatementWindow) SendAt() time.Time {
	switch w.Period {
	case PeriodWeekly:
		return w.To.Add(weeklySendDelay)
	case PeriodMonthly:
		return w.To.Add(monthlySendDelay)
	}
	return w.To.Add(dailySendDelay)
}

// Previous is the period of the same kind just before w.
func (w StatementWindow) Previous() StatementWindow {
	first := w.FirstDay.date()
	switch w.Period {
	case PeriodWeekly:
		return windowOf(w.Period, first.AddDate(0, 0, -7))
	case PeriodMonthly:
		return windowOf(w.Period, first.AddDate(0, -1, 0))
	}
	return windowOf(w.Period, first.AddDate(0, 0, -1))
}

// Next is the period of the same kind just after w.
func (w StatementWindow) Next() StatementWindow {
	first := w.FirstDay.date()
	switch w.Period {
	case PeriodWeekly:
		return windowOf(w.Period, first.AddDate(0, 0, 7))
	case PeriodMonthly:
		return windowOf(w.Period, first.AddDate(0, 1, 0))
	}
	return windowOf(w.Period, first.AddDate(0, 0, 1))
}

// scheduled reports whether a scheduled statement is ever sent for w: every week and month,
// and Monday to Friday risk days.
func (w StatementWindow) scheduled() bool {
	if w.Period != PeriodDaily {
		return true
	}
	wd := w.FirstDay.date().Weekday()
	return wd != time.Saturday && wd != time.Sunday
}

// LastCompleteWindow is the newest period of kind p whose scheduled send time is at or before
// now: the statement a schedule owes at now, and the one "Send now" sends.
func LastCompleteWindow(p StatementPeriod, now time.Time) (StatementWindow, error) {
	w, err := WindowFor(p, RiskDay(now))
	if err != nil {
		return StatementWindow{}, err
	}
	for i := 0; i < 32 && (w.SendAt().After(now) || !w.scheduled()); i++ {
		w = w.Previous()
	}
	return w, nil
}

// NextSendAt is when the next scheduled statement of kind p after now is sent.
func NextSendAt(p StatementPeriod, now time.Time) (time.Time, error) {
	w, err := LastCompleteWindow(p, now)
	if err != nil {
		return time.Time{}, err
	}
	w = w.Next()
	for i := 0; i < 32 && !w.scheduled(); i++ {
		w = w.Next()
	}
	return w.SendAt(), nil
}

// Title names the period for people: "Tuesday 24 November 2026", "23–27 November 2026",
// "November 2026".
func (w StatementWindow) Title() string {
	first, last := w.FirstDay.date(), w.LastDay.date()
	switch w.Period {
	case PeriodMonthly:
		return first.Format("January 2006")
	case PeriodWeekly:
		switch {
		case first.Year() != last.Year():
			return first.Format("2 January 2006") + " – " + last.Format("2 January 2006")
		case first.Month() != last.Month():
			return first.Format("2 January") + " – " + last.Format("2 January 2006")
		}
		return first.Format("2") + "–" + last.Format("2 January 2006")
	}
	return first.Format("Monday 2 January 2006")
}

// Contains reports whether t falls in the window.
func (w StatementWindow) Contains(t time.Time) bool { return !t.Before(w.From) && t.Before(w.To) }

// ---- formatting -------------------------------------------------------------------------

// currencySymbols are the symbols statements print; other currencies print their ISO code.
var currencySymbols = map[string]string{"USD": "$", "GBP": "£", "EUR": "€", "AUD": "A$", "JPY": "¥"}

// FormatMoney writes an amount of minor units of currency for people, with the currency's
// symbol and thousands separators: "$1,234.56", "-£12.30", "A$5.00", "¥1,200", "CHF 3.10".
func FormatMoney(currency string, c Cents) string {
	code := strings.ToUpper(strings.TrimSpace(currency))
	sym, ok := currencySymbols[code]
	if !ok {
		sym = code + " "
		if code == "" {
			sym = ""
		}
	}
	// Work on the decimal digits of the amount: exact for every int64, MinInt64 included.
	digits := strconv.FormatInt(int64(c), 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	dec := CurrencyDecimals(code)
	if len(digits) <= dec {
		digits = strings.Repeat("0", dec-len(digits)+1) + digits
	}
	whole := groupThousands(digits[:len(digits)-dec])
	if dec == 0 {
		return sign + sym + whole
	}
	return sign + sym + whole + "." + digits[len(digits)-dec:]
}

func groupThousands(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	head := len(s) % 3
	if head > 0 {
		b.WriteString(s[:head])
	}
	for i := head; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// FormatPercent writes basis points as a percentage with two decimals: 5833 → "58.33 %".
func FormatPercent(bp BasisPoints) string {
	v := int64(bp)
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d %%", sign, v/100, v%100)
}
