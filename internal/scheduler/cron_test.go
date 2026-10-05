package scheduler

import (
	"strings"
	"testing"
	"time"
)

func TestParseCronExpression_Valid(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		verify     func(t *testing.T, c *ParsedCron)
	}{
		{
			name:       "wildcards",
			expression: "* * * * *",
			verify: func(t *testing.T, c *ParsedCron) {
				if !c.Minute.Unrestricted || !c.Hour.Unrestricted || !c.DayOfMonth.Unrestricted || !c.Month.Unrestricted || !c.DayOfWeek.Unrestricted {
					t.Fatalf("expected all fields unrestricted")
				}
				if !c.Minute.Has(0) || !c.Minute.Has(59) {
					t.Errorf("unrestricted minute should match any value")
				}
			},
		},
		{
			name:       "specific numbers",
			expression: "15 10 5 6 3",
			verify: func(t *testing.T, c *ParsedCron) {
				if c.Minute.Unrestricted || len(c.Minute.Values) != 1 || !c.Minute.Has(15) {
					t.Errorf("unexpected minute values: %v", c.Minute.Values)
				}
				if c.Hour.Unrestricted || len(c.Hour.Values) != 1 || !c.Hour.Has(10) {
					t.Errorf("unexpected hour values: %v", c.Hour.Values)
				}
				if c.DayOfMonth.Unrestricted || len(c.DayOfMonth.Values) != 1 || !c.DayOfMonth.Has(5) {
					t.Errorf("unexpected dayOfMonth values: %v", c.DayOfMonth.Values)
				}
				if c.Month.Unrestricted || len(c.Month.Values) != 1 || !c.Month.Has(6) {
					t.Errorf("unexpected month values: %v", c.Month.Values)
				}
				if c.DayOfWeek.Unrestricted || len(c.DayOfWeek.Values) != 1 || !c.DayOfWeek.Has(3) {
					t.Errorf("unexpected dayOfWeek values: %v", c.DayOfWeek.Values)
				}
			},
		},
		{
			name:       "lists, ranges and steps",
			expression: "*/15 0,12 1-5 * 1-5/2",
			verify: func(t *testing.T, c *ParsedCron) {
				// minute: 0, 15, 30, 45
				for _, m := range []int{0, 15, 30, 45} {
					if !c.Minute.Has(m) {
						t.Errorf("expected minute %d in */15", m)
					}
				}
				if c.Minute.Has(10) {
					t.Errorf("minute 10 should not be in */15")
				}

				// hour: 0, 12
				if !c.Hour.Has(0) || !c.Hour.Has(12) || c.Hour.Has(6) {
					t.Errorf("unexpected hour: %v", c.Hour.Values)
				}

				// dom: 1, 2, 3, 4, 5
				for d := 1; d <= 5; d++ {
					if !c.DayOfMonth.Has(d) {
						t.Errorf("expected dom %d", d)
					}
				}
				if c.DayOfMonth.Has(6) {
					t.Errorf("dom 6 should not be present")
				}

				// dow: 1-5/2 -> 1, 3, 5
				if !c.DayOfWeek.Has(1) || !c.DayOfWeek.Has(3) || !c.DayOfWeek.Has(5) {
					t.Errorf("expected dow 1, 3, 5")
				}
				if c.DayOfWeek.Has(2) || c.DayOfWeek.Has(4) {
					t.Errorf("dow 2 and 4 should not be present")
				}
			},
		},
		{
			name:       "sunday normalization 7 to 0",
			expression: "0 0 * * 7",
			verify: func(t *testing.T, c *ParsedCron) {
				if !c.DayOfWeek.Has(0) {
					t.Errorf("expected Sunday normalized to 0")
				}
				if _, ok := c.DayOfWeek.Values[7]; ok {
					t.Errorf("7 should have been normalized to 0, not present as 7")
				}
			},
		},
		{
			name:       "single number with step",
			expression: "10/20 * * * *",
			verify: func(t *testing.T, c *ParsedCron) {
				// 10 to 59 step 20 -> 10, 30, 50
				if !c.Minute.Has(10) || !c.Minute.Has(30) || !c.Minute.Has(50) {
					t.Errorf("expected 10, 30, 50 in 10/20")
				}
				if c.Minute.Has(20) || c.Minute.Has(0) {
					t.Errorf("unexpected minute in 10/20")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cron, err := ParseCronExpression(tt.expression)
			if err != nil {
				t.Fatalf("unexpected error parsing %q: %v", tt.expression, err)
			}
			tt.verify(t, cron)
		})
	}
}

func TestParseCronExpression_Errors(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		errSubstr  string
	}{
		{
			name:       "empty expression",
			expression: "",
			errSubstr:  "must be a five-field cron expression",
		},
		{
			name:       "four fields",
			expression: "* * * *",
			errSubstr:  "must be a five-field cron expression",
		},
		{
			name:       "six fields",
			expression: "* * * * * *",
			errSubstr:  "must be a five-field cron expression",
		},
		{
			name:       "empty item in list",
			expression: "1,,2 * * * *",
			errSubstr:  "field contains an empty item",
		},
		{
			name:       "trailing comma",
			expression: "1,2, * * * *",
			errSubstr:  "field contains an empty item",
		},
		{
			name:       "invalid step multiple slashes",
			expression: "*/5/2 * * * *",
			errSubstr:  "field has an invalid step",
		},
		{
			name:       "non-integer step",
			expression: "*/foo * * * *",
			errSubstr:  "step must be a positive integer in milliseconds",
		},
		{
			name:       "step of zero",
			expression: "*/0 * * * *",
			errSubstr:  "step must be a positive integer in milliseconds",
		},
		{
			name:       "invalid range start greater than end",
			expression: "50-10 * * * *",
			errSubstr:  "range start must be before range end",
		},
		{
			name:       "invalid range malformed",
			expression: "5-10-15 * * * *",
			errSubstr:  "field has an invalid range",
		},
		{
			name:       "non-integer minute",
			expression: "foo * * * *",
			errSubstr:  "minute value must be an integer",
		},
		{
			name:       "negative minute has invalid range",
			expression: "-1 * * * *",
			errSubstr:  "minute field has an invalid range",
		},
		{
			name:       "out of bounds minute over 59",
			expression: "60 * * * *",
			errSubstr:  "minute value must be between 0 and 59",
		},
		{
			name:       "out of bounds hour over 23",
			expression: "* 24 * * *",
			errSubstr:  "hour value must be between 0 and 23",
		},
		{
			name:       "out of bounds day of month 0",
			expression: "* * 0 * *",
			errSubstr:  "day of month value must be between 1 and 31",
		},
		{
			name:       "out of bounds day of month 32",
			expression: "* * 32 * *",
			errSubstr:  "day of month value must be between 1 and 31",
		},
		{
			name:       "out of bounds month 0",
			expression: "* * * 0 *",
			errSubstr:  "month value must be between 1 and 12",
		},
		{
			name:       "out of bounds month 13",
			expression: "* * * 13 *",
			errSubstr:  "month value must be between 1 and 12",
		},
		{
			name:       "out of bounds day of week 8",
			expression: "* * * * 8",
			errSubstr:  "day of week value must be between 0 and 7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCronExpression(tt.expression)
			if err == nil {
				t.Fatalf("expected error for expression %q, got nil", tt.expression)
			}
			if !strings.Contains(err.Error(), tt.errSubstr) {
				t.Errorf("error %q does not contain expected substring %q", err.Error(), tt.errSubstr)
			}
		})
	}
}

func TestValidateCronFeasibility(t *testing.T) {
	// Impossible: February 31
	cron, err := ParseCronExpression("0 0 31 2 *")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if err := ValidateCronFeasibility(cron); err == nil {
		t.Errorf("expected feasibility error for Feb 31, got nil")
	}

	// Impossible: April 31 (April has 30 days)
	cronApr, err := ParseCronExpression("0 0 31 4 *")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if err := ValidateCronFeasibility(cronApr); err == nil {
		t.Errorf("expected feasibility error for Apr 31, got nil")
	}

	// Feasible: February 29 (leap year allowed in feasibility table)
	cronLeap, err := ParseCronExpression("0 0 29 2 *")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if err := ValidateCronFeasibility(cronLeap); err != nil {
		t.Errorf("Feb 29 should be feasible: %v", err)
	}

	// Feasible: Unrestricted month
	cronUnrestrictedMonth, err := ParseCronExpression("0 0 31 * *")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if err := ValidateCronFeasibility(cronUnrestrictedMonth); err != nil {
		t.Errorf("unrestricted month should be feasible: %v", err)
	}
}

func TestCronMatchesDate_DayUnionIntersection(t *testing.T) {
	// Case 1: Both DOM and DOW unrestricted ("* * * * *")
	cronBothUnrestricted, _ := ParseCronExpression("* * * * *")
	date := time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC)
	if !CronMatchesDate(cronBothUnrestricted, date) {
		t.Errorf("expected match for both unrestricted")
	}

	// Case 2: Only DOM restricted -> must match DOM
	cronDOMOnly, _ := ParseCronExpression("0 0 5 * *")
	if !CronMatchesDate(cronDOMOnly, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("expected match on day 5")
	}
	if CronMatchesDate(cronDOMOnly, time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("should not match on day 6")
	}

	// Case 3: Only DOW restricted -> must match DOW
	// 2026-10-05 is Monday (DOW = 1)
	cronDOWOnly, _ := ParseCronExpression("0 0 * * 1")
	if !CronMatchesDate(cronDOWOnly, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("expected match on Monday")
	}
	if CronMatchesDate(cronDOWOnly, time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("should not match on Tuesday")
	}

	// Case 4: BOTH restricted -> UNION (POSIX rule: DOM || DOW)
	// Match if day is the 10th OR if day is Monday (1)
	cronUnion, _ := ParseCronExpression("0 0 10 * 1")
	// 2026-10-05 is Monday, but DOM is 5 (matches DOW=1)
	if !CronMatchesDate(cronUnion, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("expected match because Monday satisfies union rule")
	}
	// 2026-10-10 is Saturday (DOW=6), but DOM is 10 (matches DOM=10)
	if !CronMatchesDate(cronUnion, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("expected match because 10th satisfies union rule")
	}
	// 2026-10-11 is Sunday (DOW=0), DOM is 11 (matches neither)
	if CronMatchesDate(cronUnion, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("should not match when neither matches")
	}
}

func TestGetNextCronRunAt_Calculation(t *testing.T) {
	loc := time.UTC
	from := time.Date(2026, time.October, 5, 10, 30, 45, 0, loc)

	// Next minute
	next, err := GetNextCronRunAt("* * * * *", from)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := time.Date(2026, time.October, 5, 10, 31, 0, 0, loc)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, next)
	}

	// Next top of hour
	nextHour, err := GetNextCronRunAt("0 * * * *", from)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedHour := time.Date(2026, time.October, 5, 11, 0, 0, 0, loc)
	if !nextHour.Equal(expectedHour) {
		t.Errorf("expected %v, got %v", expectedHour, nextHour)
	}

	// Next specific day
	nextDay, err := GetNextCronRunAt("0 0 10 * *", from)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedDay := time.Date(2026, time.October, 10, 0, 0, 0, 0, loc)
	if !nextDay.Equal(expectedDay) {
		t.Errorf("expected %v, got %v", expectedDay, nextDay)
	}
}

func TestGetNextCronRunAt_LookaheadHorizon(t *testing.T) {
	loc := time.UTC
	// Current date: 2026-01-01
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, loc)

	// Schedule 40 days away (> 35 days): February 10
	// 35 days from Jan 1 is Feb 5. Feb 10 is outside the 35-day window.
	_, err := GetNextCronRunAt("0 0 10 2 *", from)
	if err == nil {
		t.Fatalf("expected error for schedule >35 days away")
	}
	if !strings.Contains(err.Error(), "within 50400 minutes") {
		t.Errorf("expected 50400 minutes error, got: %v", err)
	}

	// Zero date error
	_, err = GetNextCronRunAt("* * * * *", time.Time{})
	if err == nil {
		t.Fatalf("expected error for zero from date")
	}
	if !strings.Contains(err.Error(), "from must be a valid Date") {
		t.Errorf("expected valid Date error, got: %v", err)
	}
}
