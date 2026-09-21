package schedule

import (
	"fmt"
	"strconv"
	"strings"
)

// Form is a schedule in the shapes a person picks from a list — "daily at
// 03:00", "every Sunday at 03:00" — instead of typing a cron expression.
// Expr renders a Form as the 5-field expression the scheduler runs;
// Describe recognises exactly those shapes again so the UI can show a
// saved expression as words. Anything else is FormCron and stays an
// expression: no guessing, no lossy paraphrase.
type Form struct {
	Kind    string `json:"kind"`              // daily, weekly, monthly, hourly, minutes, cron
	Hour    int    `json:"hour,omitempty"`    // daily, weekly, monthly: 0-23
	Minute  int    `json:"minute,omitempty"`  // daily, weekly, monthly, hourly: 0-59
	Weekday int    `json:"weekday,omitempty"` // weekly: 0 (Sunday) - 6
	Day     int    `json:"day,omitempty"`     // monthly: 1-28 (every month has them)
	Every   int    `json:"every,omitempty"`   // minutes: a divisor of 60
	Expr    string `json:"expr,omitempty"`    // cron: the expression as typed
}

// Form kinds.
const (
	FormDaily   = "daily"
	FormWeekly  = "weekly"
	FormMonthly = "monthly"
	FormHourly  = "hourly"
	FormMinutes = "minutes"
	FormCron    = "cron"
)

// Expression renders the form as a cron expression, or says what is out of
// range. FormCron passes its expression through untouched (validate it
// with Validate).
func (f Form) Expression() (string, error) {
	switch f.Kind {
	case FormDaily:
		if err := clock(f.Hour, f.Minute); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d * * *", f.Minute, f.Hour), nil
	case FormWeekly:
		if err := clock(f.Hour, f.Minute); err != nil {
			return "", err
		}
		if f.Weekday < 0 || f.Weekday > 6 {
			return "", fmt.Errorf("weekday %d out of range 0-6", f.Weekday)
		}
		return fmt.Sprintf("%d %d * * %d", f.Minute, f.Hour, f.Weekday), nil
	case FormMonthly:
		if err := clock(f.Hour, f.Minute); err != nil {
			return "", err
		}
		if f.Day < 1 || f.Day > 28 {
			return "", fmt.Errorf("day %d out of range 1-28", f.Day)
		}
		return fmt.Sprintf("%d %d %d * *", f.Minute, f.Hour, f.Day), nil
	case FormHourly:
		if f.Minute < 0 || f.Minute > 59 {
			return "", fmt.Errorf("minute %d out of range 0-59", f.Minute)
		}
		return fmt.Sprintf("%d * * * *", f.Minute), nil
	case FormMinutes:
		if f.Every < 1 || f.Every > 30 || 60%f.Every != 0 {
			return "", fmt.Errorf("every %d minutes is not a divisor of 60 (1, 2, 3, 4, 5, 6, 10, 12, 15, 20, 30)", f.Every)
		}
		return fmt.Sprintf("*/%d * * * *", f.Every), nil
	case FormCron:
		return strings.TrimSpace(f.Expr), nil
	}
	return "", fmt.Errorf("unknown form %q", f.Kind)
}

func clock(hour, minute int) error {
	if hour < 0 || hour > 23 {
		return fmt.Errorf("hour %d out of range 0-23", hour)
	}
	if minute < 0 || minute > 59 {
		return fmt.Errorf("minute %d out of range 0-59", minute)
	}
	return nil
}

// Describe turns an expression back into a Form when it has exactly one of
// the list shapes; every other expression — lists, ranges, names, a step in
// the hour field — comes back as FormCron with the expression kept.
func Describe(expr string) Form {
	expr = strings.TrimSpace(expr)
	cron := Form{Kind: FormCron, Expr: expr}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return cron
	}
	min, minOK := plain(f[0], 0, 59)
	hour, hourOK := plain(f[1], 0, 23)
	dom, domOK := plain(f[2], 1, 28)
	dow, dowOK := plain(f[4], 0, 7)
	star := func(s string) bool { return s == "*" }
	switch {
	case minOK && hourOK && star(f[2]) && star(f[3]) && star(f[4]):
		return Form{Kind: FormDaily, Hour: hour, Minute: min}
	case minOK && hourOK && star(f[2]) && star(f[3]) && dowOK:
		return Form{Kind: FormWeekly, Hour: hour, Minute: min, Weekday: dow % 7} // 7 is Sunday too
	case minOK && hourOK && domOK && star(f[3]) && star(f[4]):
		return Form{Kind: FormMonthly, Hour: hour, Minute: min, Day: dom}
	case minOK && star(f[1]) && star(f[2]) && star(f[3]) && star(f[4]):
		return Form{Kind: FormHourly, Minute: min}
	case strings.HasPrefix(f[0], "*/") && star(f[1]) && star(f[2]) && star(f[3]) && star(f[4]):
		if n, err := strconv.Atoi(f[0][2:]); err == nil && n >= 1 && n <= 30 && 60%n == 0 {
			return Form{Kind: FormMinutes, Every: n}
		}
	}
	return cron
}

// plain accepts a bare decimal number in range — no names, lists, ranges
// or steps, so that only expressions the form can reproduce are described.
func plain(s string, lo, hi int) (int, bool) {
	if s == "" || strings.ContainsAny(s, "*,-/") {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, false
	}
	return n, true
}
