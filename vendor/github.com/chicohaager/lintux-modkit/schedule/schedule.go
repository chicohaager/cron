package schedule

import (
	"strconv"
	"strings"
	"time"
)

// searchWindow bounds the forward search in Next. A valid 5-field expression
// always fires within a year, except for calendar dates that a given year
// does not contain (Feb 29); those return the zero time and the caller treats
// the task as unschedulable rather than spinning forever.
const searchWindow = 366 * 24 * time.Hour

// Next returns the first minute strictly after from at which expr fires,
// or the zero time if the expression is malformed or nothing matches within
// searchWindow. It implements the POSIX day-matching rule: when both the
// day-of-month and the day-of-week field are restricted, a day matches if
// EITHER does; when only one is restricted, that one decides.
func Next(expr string, from time.Time) time.Time {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return time.Time{}
	}
	minute, ok1 := parseField(f[0], fields[0])
	hour, ok2 := parseField(f[1], fields[1])
	dom, ok3 := parseField(f[2], fields[2])
	month, ok4 := parseField(f[3], fields[3])
	dow, ok5 := parseField(f[4], fields[4])
	if !(ok1 && ok2 && ok3 && ok4 && ok5) {
		return time.Time{}
	}

	d := from.Truncate(time.Minute).Add(time.Minute)
	deadline := d.Add(searchWindow)
	for d.Before(deadline) {
		if month.has(int(d.Month())) && dayMatches(d, dom, dow) &&
			hour.has(d.Hour()) && minute.has(d.Minute()) {
			return d
		}
		d = d.Add(time.Minute)
	}
	return time.Time{}
}

// dayMatches applies the POSIX union rule for the two day fields.
func dayMatches(d time.Time, dom, dow field) bool {
	domOk := dom.has(d.Day())
	dowOk := dow.has(int(d.Weekday())) // Sunday is stored as 0; 7 is folded to 0 while parsing
	switch {
	case dom.unrestricted && dow.unrestricted:
		return true
	case dom.unrestricted:
		return dowOk
	case dow.unrestricted:
		return domOk
	default:
		return domOk || dowOk
	}
}

// field is the parsed value set of one cron field.
type field struct {
	set          map[int]bool
	unrestricted bool // the field was a bare "*"; "*/n" is a restriction
}

func (f field) has(v int) bool { return f.set[v] }

// parseField turns one field of a validated expression into its value set.
// It shares the alias tables with Validate so that "the validator accepts
// it" and "the scheduler can run it" are the same statement. Malformed input
// yields ok=false; Validate reports the human-readable reason.
func parseField(expr string, fd fieldDef) (field, bool) {
	f := field{set: map[int]bool{}}
	for _, tok := range strings.Split(strings.TrimSpace(expr), ",") {
		tok = strings.TrimSpace(tok)
		if tok == "*" {
			f.unrestricted = true
			f.add(fd.Min, fd.Max, 1, fd)
			continue
		}
		rangePart, step := tok, 1
		if i := strings.Index(tok, "/"); i != -1 {
			s, err := strconv.Atoi(tok[i+1:])
			if err != nil || s < 1 {
				return f, false
			}
			rangePart, step = tok[:i], s
		}
		lo, hi, ok := parseRange(rangePart, fd)
		if !ok {
			return f, false
		}
		if step > 1 && !strings.ContainsAny(rangePart, "*-") {
			hi = fd.Max // Vixie: "a/n" means a..max stepped by n
		}
		f.add(lo, hi, step, fd)
	}
	return f, true
}

// parseRange resolves "*", "a", or "a-b" into inclusive bounds. Names
// (mon, jan) are accepted wherever numbers are.
func parseRange(s string, fd fieldDef) (lo, hi int, ok bool) {
	if s == "*" {
		return fd.Min, fd.Max, true
	}
	a, b, isRange := strings.Cut(s, "-")
	lo, err := resolveValue(a, fd)
	if err != nil {
		return 0, 0, false
	}
	if !isRange {
		return lo, lo, true
	}
	hi, err = resolveValue(b, fd)
	if err != nil || hi < lo {
		return 0, 0, false
	}
	return lo, hi, true
}

// add marks lo..hi (stepped) as matching. For the weekday field 7 is an
// alias of Sunday and is folded onto 0.
func (f *field) add(lo, hi, step int, fd fieldDef) {
	for v := lo; v <= hi; v += step {
		if fd.Name == "weekday" && v == 7 {
			f.set[0] = true
			continue
		}
		f.set[v] = true
	}
}
