package cron

import (
	"fmt"
	"strconv"
	"strings"
)

// ValidationError describes a problem with a specific cron field.
type ValidationError struct {
	Field   string `json:"field"`
	Value   string `json:"value"`
	Message string `json:"message"`
}

// fieldDef defines the name and valid range for a cron field.
type fieldDef struct {
	Name string
	Min  int
	Max  int
}

var fields = []fieldDef{
	{"minute", 0, 59},
	{"hour", 0, 23},
	{"day", 1, 31},
	{"month", 1, 12},
	{"weekday", 0, 7}, // 0 and 7 both mean Sunday
}

// Validate performs full validation of a 5-field cron expression.
// Returns a list of field-level errors and whether the expression is valid.
func Validate(expr string) ([]ValidationError, bool) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return []ValidationError{{
			Field:   "expression",
			Value:   expr,
			Message: fmt.Sprintf("Expected 5 fields (minute hour day month weekday), got %d", len(parts)),
		}}, false
	}

	var errs []ValidationError
	for i, fd := range fields {
		errs = append(errs, validateField(parts[i], fd)...)
	}
	return errs, len(errs) == 0
}

func validateField(expr string, fd fieldDef) []ValidationError {
	var errs []ValidationError
	tokens := strings.Split(expr, ",")
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			errs = append(errs, ValidationError{fd.Name, tok, "empty token"})
			continue
		}
		if tok == "*" {
			continue
		}
		// */step
		if strings.HasPrefix(tok, "*/") {
			stepStr := tok[2:]
			if err := checkStep(stepStr, fd); err != nil {
				errs = append(errs, ValidationError{fd.Name, tok, err.Error()})
			}
			continue
		}
		// range with optional step: a-b or a-b/step
		if strings.Contains(tok, "-") {
			if err := checkRange(tok, fd); err != nil {
				errs = append(errs, ValidationError{fd.Name, tok, err.Error()})
			}
			continue
		}
		// plain step: n/step (less common but valid)
		if strings.Contains(tok, "/") {
			parts := strings.SplitN(tok, "/", 2)
			if err := checkValue(parts[0], fd); err != nil {
				errs = append(errs, ValidationError{fd.Name, tok, err.Error()})
				continue
			}
			if err := checkStep(parts[1], fd); err != nil {
				errs = append(errs, ValidationError{fd.Name, tok, err.Error()})
			}
			continue
		}
		// single value
		if err := checkValue(tok, fd); err != nil {
			errs = append(errs, ValidationError{fd.Name, tok, err.Error()})
		}
	}
	return errs
}

func checkValue(s string, fd fieldDef) error {
	// Allow weekday names
	if fd.Name == "weekday" {
		if _, ok := weekdayAlias(s); ok {
			return nil
		}
	}
	if fd.Name == "month" {
		if _, ok := monthAlias(s); ok {
			return nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("'%s' is not a valid number", s)
	}
	if v < fd.Min || v > fd.Max {
		return fmt.Errorf("%d is out of range %d-%d for %s", v, fd.Min, fd.Max, fd.Name)
	}
	return nil
}

func checkStep(s string, fd fieldDef) error {
	v, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("step '%s' is not a valid number", s)
	}
	if v < 1 {
		return fmt.Errorf("step must be >= 1, got %d", v)
	}
	if v > (fd.Max - fd.Min + 1) {
		return fmt.Errorf("step %d exceeds range %d-%d for %s", v, fd.Min, fd.Max, fd.Name)
	}
	return nil
}

func checkRange(tok string, fd fieldDef) error {
	// a-b or a-b/step
	main, stepStr := tok, ""
	if idx := strings.Index(tok, "/"); idx != -1 {
		main = tok[:idx]
		stepStr = tok[idx+1:]
	}
	parts := strings.SplitN(main, "-", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid range '%s'", tok)
	}
	a, errA := resolveValue(parts[0], fd)
	if errA != nil {
		return fmt.Errorf("range start: %w", errA)
	}
	b, errB := resolveValue(parts[1], fd)
	if errB != nil {
		return fmt.Errorf("range end: %w", errB)
	}
	if a > b {
		return fmt.Errorf("range start %d > end %d", a, b)
	}
	if stepStr != "" {
		if err := checkStep(stepStr, fd); err != nil {
			return err
		}
	}
	return nil
}

func resolveValue(s string, fd fieldDef) (int, error) {
	if fd.Name == "weekday" {
		if v, ok := weekdayAlias(s); ok {
			return v, nil
		}
	}
	if fd.Name == "month" {
		if v, ok := monthAlias(s); ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("'%s' is not a valid number", s)
	}
	if v < fd.Min || v > fd.Max {
		return 0, fmt.Errorf("%d out of range %d-%d", v, fd.Min, fd.Max)
	}
	return v, nil
}

func weekdayAlias(s string) (int, bool) {
	aliases := map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}
	v, ok := aliases[strings.ToLower(s)]
	return v, ok
}

func monthAlias(s string) (int, bool) {
	aliases := map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}
	v, ok := aliases[strings.ToLower(s)]
	return v, ok
}
