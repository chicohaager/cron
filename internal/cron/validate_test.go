package cron

import "testing"

func TestValidExpressions(t *testing.T) {
	valid := []string{
		"* * * * *",
		"*/5 * * * *",
		"0 3 * * *",
		"0 0 1 1 *",
		"30 4 1-15 * 1-5",
		"0 */2 * * *",
		"0 0 * * mon",
		"0 0 * jan *",
		"5,10,15 * * * *",
		"0 0 * * 0",
		"0 0 * * 7",
		"1-5/2 * * * *",
	}
	for _, expr := range valid {
		errs, ok := Validate(expr)
		if !ok {
			t.Errorf("expected valid: %q, got errors: %v", expr, errs)
		}
	}
}

func TestInvalidFieldCount(t *testing.T) {
	errs, ok := Validate("* * *")
	if ok {
		t.Fatal("expected invalid for 3 fields")
	}
	if len(errs) != 1 || errs[0].Field != "expression" {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestOutOfRange(t *testing.T) {
	cases := []struct {
		expr  string
		field string
	}{
		{"60 * * * *", "minute"},
		{"* 24 * * *", "hour"},
		{"* * 32 * *", "day"},
		{"* * * 13 *", "month"},
		{"* * * * 8", "weekday"},
	}
	for _, c := range cases {
		errs, ok := Validate(c.expr)
		if ok {
			t.Errorf("expected invalid: %q", c.expr)
			continue
		}
		found := false
		for _, e := range errs {
			if e.Field == c.field {
				found = true
			}
		}
		if !found {
			t.Errorf("expected error on field %s for %q, got: %v", c.field, c.expr, errs)
		}
	}
}

func TestInvalidStep(t *testing.T) {
	errs, ok := Validate("*/0 * * * *")
	if ok {
		t.Fatal("expected invalid for step 0")
	}
	if len(errs) == 0 {
		t.Fatal("expected errors")
	}
}

func TestInvalidRange(t *testing.T) {
	errs, ok := Validate("10-5 * * * *")
	if ok {
		t.Fatal("expected invalid for reversed range")
	}
	if len(errs) == 0 {
		t.Fatal("expected errors")
	}
}

func TestWeekdayAliases(t *testing.T) {
	errs, ok := Validate("0 0 * * mon-fri")
	if !ok {
		t.Errorf("expected valid weekday aliases, got: %v", errs)
	}
}

func TestMonthAliases(t *testing.T) {
	errs, ok := Validate("0 0 1 jan-dec *")
	if !ok {
		t.Errorf("expected valid month aliases, got: %v", errs)
	}
}

func TestInvalidToken(t *testing.T) {
	errs, ok := Validate("abc * * * *")
	if ok {
		t.Fatal("expected invalid for non-numeric")
	}
	if len(errs) == 0 {
		t.Fatal("expected errors")
	}
}
