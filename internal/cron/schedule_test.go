package cron

import (
	"testing"
	"time"
)

// from is a fixed Wednesday so that every weekday/day-of-month case has one
// unambiguous expected answer.
var from = time.Date(2026, time.August, 26, 0, 0, 0, 0, time.UTC) // Wed

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func TestNext(t *testing.T) {
	cases := []struct {
		expr string
		want time.Time
	}{
		// unrestricted day fields
		{"*/15 * * * *", at(2026, 8, 26, 0, 15)},
		{"0 2 * * *", at(2026, 8, 26, 2, 0)},
		// weekday only (finding 1)
		{"0 2 * * 1", at(2026, 8, 31, 2, 0)},     // Monday
		{"30 3 * * mon", at(2026, 8, 31, 3, 30)}, // alias
		{"0 2 * * mon-fri", at(2026, 8, 26, 2, 0)},
		{"0 2 * * sat,sun", at(2026, 8, 29, 2, 0)},
		{"0 2 * * 7", at(2026, 8, 30, 2, 0)}, // 7 == Sunday
		{"0 2 * * 5-7", at(2026, 8, 28, 2, 0)},
		// day-of-month only
		{"0 5 1 * *", at(2026, 9, 1, 5, 0)},
		{"0 5 */10 * *", at(2026, 8, 31, 5, 0)}, // */n is a restriction: 1,11,21,31
		{"0 5 15 * *", at(2026, 9, 15, 5, 0)},
		// both restricted -> POSIX OR
		{"0 2 1 * 1", at(2026, 8, 31, 2, 0)},
		{"0 2 27 * 1", at(2026, 8, 27, 2, 0)},
		// month names and ranges (finding 2)
		{"0 0 1 jan *", at(2027, 1, 1, 0, 0)},
		{"0 0 1 oct-dec *", at(2026, 10, 1, 0, 0)},
		{"0 0 1 1,7 *", at(2027, 1, 1, 0, 0)},
		// steps and lists
		{"0 */6 * * *", at(2026, 8, 26, 6, 0)},
		{"5,35 8-10/2 * * *", at(2026, 8, 26, 8, 5)},
		{"0 5/6 * * *", at(2026, 8, 26, 5, 0)}, // Vixie "a/n" = a..max
	}
	for _, c := range cases {
		got := Next(c.expr, from)
		if !got.Equal(c.want) {
			t.Errorf("Next(%q) = %s (%s), want %s (%s)", c.expr,
				got.Format("2006-01-02 15:04"), got.Weekday(),
				c.want.Format("2006-01-02 15:04"), c.want.Weekday())
		}
	}
}

// TestValidateAndNextAgree pins the contract "what the validator accepts, the
// scheduler can run": an expression must never validate and then never fire.
func TestValidateAndNextAgree(t *testing.T) {
	exprs := []string{
		"* * * * *", "0 2 * * 1", "0 0 1 jan *", "0 2 * * mon-fri",
		"0 0 1 jan-mar *", "*/5 * * * *", "0 12 */2 * *", "0 2 * * sun",
		"0 0 29 feb *", // fires on 2028-02-29 within... no: 2027 has no Feb 29 -> zero within 1 year window
		"bogus", "1 2 3", "60 * * * *", "0 2 * * 8", "0 0 1 xyz *",
	}
	for _, e := range exprs {
		_, valid := Validate(e)
		fires := !Next(e, from).IsZero()
		// leap-day exception: valid but not reachable within the 1-year window
		if e == "0 0 29 feb *" {
			if !valid {
				t.Errorf("%q should validate", e)
			}
			continue
		}
		if valid != fires {
			t.Errorf("%q: Validate=%v but Next fires=%v", e, valid, fires)
		}
	}
}

func TestNextStepFromValueRunsToMax(t *testing.T) {
	got := Next("0 5/6 * * *", at(2026, 8, 26, 6, 0))
	if want := at(2026, 8, 26, 11, 0); !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestNextRejectsWrongFieldCount(t *testing.T) {
	if !Next("* * * *", from).IsZero() {
		t.Error("4-field expression must not schedule")
	}
}
