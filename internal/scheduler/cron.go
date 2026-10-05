package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// CronFieldCount defines the standard 5 fields in a cron expression:
	// minute, hour, day-of-month, month, day-of-week.
	CronFieldCount = 5

	// CronLookaheadMinutes defines the maximum forward search horizon for a scheduled run.
	// 35 days * 24 hours/day * 60 minutes/hour = 50,400 minutes.
	CronLookaheadMinutes = 35 * 24 * 60
)

// maxDaysInMonth mirrors MAX_DAYS_IN_MONTH in runner.js:
// [31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
// Note: February allows 29 for potential leap years.
var maxDaysInMonth = [12]int{31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// cronFieldDef describes constraints and normalization for one field of a cron expression.
type cronFieldDef struct {
	key       string
	label     string
	min       int
	max       int
	normalize func(int) int
}

var cronFieldDefs = []cronFieldDef{
	{key: "minute", label: "minute", min: 0, max: 59},
	{key: "hour", label: "hour", min: 0, max: 23},
	{key: "dayOfMonth", label: "day of month", min: 1, max: 31},
	{key: "month", label: "month", min: 1, max: 12},
	{
		key:   "dayOfWeek",
		label: "day of week",
		min:   0,
		max:   7,
		normalize: func(v int) int {
			if v == 7 {
				return 0 // Sunday can be 0 or 7; normalize 7 -> 0
			}
			return v
		},
	},
}

// CronField represents the parsed values for one field of a cron expression.
type CronField struct {
	Values       map[int]struct{}
	Unrestricted bool
}

// Has checks if value is present in this field.
func (f *CronField) Has(value int) bool {
	if f.Unrestricted {
		return true
	}
	_, ok := f.Values[value]
	return ok
}

// ParsedCron holds a parsed and validated 5-field cron expression.
type ParsedCron struct {
	Expression string
	Minute     CronField
	Hour       CronField
	DayOfMonth CronField
	Month      CronField
	DayOfWeek  CronField
}

// parseCronRawNumber validates that text is a positive non-negative integer within field bounds.
func parseCronRawNumber(text string, def cronFieldDef, envName string) (int, error) {
	// Must match /^\d+$/
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("%s %s value must be an integer", envName, def.label)
		}
	}
	if text == "" {
		return 0, fmt.Errorf("%s %s value must be an integer", envName, def.label)
	}

	val, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%s %s value must be an integer", envName, def.label)
	}

	if val < def.min || val > def.max {
		return 0, fmt.Errorf("%s %s value must be between %d and %d", envName, def.label, def.min, def.max)
	}

	return val, nil
}

// parsePositiveStep parses step text requiring a positive integer > 0.
func parsePositiveStep(text, envName, label string) (int, error) {
	text = strings.TrimSpace(text)
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("%s %s step must be a positive integer in milliseconds", envName, label)
		}
	}
	if text == "" {
		return 0, fmt.Errorf("%s %s step must be a positive integer in milliseconds", envName, label)
	}

	val, err := strconv.Atoi(text)
	if err != nil || val <= 0 {
		return 0, fmt.Errorf("%s %s step must be a positive integer in milliseconds", envName, label)
	}

	return val, nil
}

// parseCronField parses a single field of a cron expression (e.g. "*", "1,2", "0-30/5").
func parseCronField(text string, def cronFieldDef, envName string) (CronField, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return CronField{}, fmt.Errorf("%s %s field cannot be empty", envName, def.label)
	}

	values := make(map[int]struct{})
	unrestricted := text == "*"

	parts := strings.Split(text, ",")
	for _, rawPart := range parts {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			return CronField{}, fmt.Errorf("%s %s field contains an empty item", envName, def.label)
		}

		pieces := strings.Split(part, "/")
		if len(pieces) > 2 {
			return CronField{}, fmt.Errorf("%s %s field has an invalid step", envName, def.label)
		}

		rangeText := pieces[0]
		step := 1
		var hasStep bool
		if len(pieces) == 2 {
			hasStep = true
			var err error
			step, err = parsePositiveStep(pieces[1], envName, def.label)
			if err != nil {
				return CronField{}, err
			}
		}

		var start, end int
		if rangeText == "*" {
			start = def.min
			end = def.max
		} else if strings.Contains(rangeText, "-") {
			rangeParts := strings.Split(rangeText, "-")
			if len(rangeParts) != 2 || rangeParts[0] == "" || rangeParts[1] == "" {
				return CronField{}, fmt.Errorf("%s %s field has an invalid range", envName, def.label)
			}
			var err error
			start, err = parseCronRawNumber(rangeParts[0], def, envName)
			if err != nil {
				return CronField{}, err
			}
			end, err = parseCronRawNumber(rangeParts[1], def, envName)
			if err != nil {
				return CronField{}, err
			}
			if start > end {
				return CronField{}, fmt.Errorf("%s %s range start must be before range end", envName, def.label)
			}
		} else {
			var err error
			start, err = parseCronRawNumber(rangeText, def, envName)
			if err != nil {
				return CronField{}, err
			}
			if hasStep {
				end = def.max
			} else {
				end = start
			}
		}

		for v := start; v <= end; v += step {
			normalized := v
			if def.normalize != nil {
				normalized = def.normalize(v)
			}
			values[normalized] = struct{}{}
		}
	}

	return CronField{
		Values:       values,
		Unrestricted: unrestricted,
	}, nil
}

// ParseCronExpression parses a 5-field cron expression string into a ParsedCron.
// envName specifies the context/identifier for error messages (default "cron").
func ParseCronExpression(expression string, envNameOpt ...string) (*ParsedCron, error) {
	envName := "cron"
	if len(envNameOpt) > 0 && envNameOpt[0] != "" {
		envName = envNameOpt[0]
	}

	text := strings.TrimSpace(expression)
	parts := strings.Fields(text)
	if len(parts) != CronFieldCount {
		return nil, fmt.Errorf("%s must be a five-field cron expression", envName)
	}

	parsed := &ParsedCron{
		Expression: strings.Join(parts, " "),
	}

	for i, def := range cronFieldDefs {
		field, err := parseCronField(parts[i], def, envName)
		if err != nil {
			return nil, err
		}
		switch def.key {
		case "minute":
			parsed.Minute = field
		case "hour":
			parsed.Hour = field
		case "dayOfMonth":
			parsed.DayOfMonth = field
		case "month":
			parsed.Month = field
		case "dayOfWeek":
			parsed.DayOfWeek = field
		}
	}

	return parsed, nil
}

// ValidateCronFeasibility checks whether the cron schedule can ever match a real calendar date.
// If both dayOfMonth and month are restricted, it verifies that at least one (month, day) pair is valid.
func ValidateCronFeasibility(cron *ParsedCron) error {
	if cron.DayOfMonth.Unrestricted || cron.Month.Unrestricted {
		return nil
	}

	for month := range cron.Month.Values {
		if month < 1 || month > 12 {
			continue
		}
		maxDay := maxDaysInMonth[month-1]
		for day := range cron.DayOfMonth.Values {
			if day <= maxDay {
				return nil
			}
		}
	}

	return fmt.Errorf("Cron expression %q cannot match any calendar date", cron.Expression)
}

// CronMatchesDate checks if a given time.Time matches the parsed cron expression.
// Follows standard POSIX / Vixie cron rules:
// If both dayOfMonth and dayOfWeek are restricted (not "*"), matching either satisfies the day constraint (UNION).
// If either or both is unrestricted ("*"), both must match (INTERSECTION).
func CronMatchesDate(cron *ParsedCron, date time.Time) bool {
	dom := date.Day()
	dow := int(date.Weekday()) // 0 = Sunday, matches normalized 0

	_, domMatches := cron.DayOfMonth.Values[dom]
	_, dowMatches := cron.DayOfWeek.Values[dow]

	var dayMatches bool
	if !cron.DayOfMonth.Unrestricted && !cron.DayOfWeek.Unrestricted {
		dayMatches = domMatches || dowMatches
	} else {
		// When unrestricted, CronField.Has returns true
		dayMatches = cron.DayOfMonth.Has(dom) && cron.DayOfWeek.Has(dow)
	}

	if !dayMatches {
		return false
	}

	minuteMatches := cron.Minute.Has(date.Minute())
	hourMatches := cron.Hour.Has(date.Hour())
	monthMatches := cron.Month.Has(int(date.Month()))

	return minuteMatches && hourMatches && monthMatches
}

// GetNextCronRunAt calculates the next execution time for a cron expression starting strictly after 'from'.
// It searches minute-by-minute up to CronLookaheadMinutes (35 days).
// Returns an error if the expression is invalid, infeasible, or has no run within 35 days.
func GetNextCronRunAt(expressionOrCron any, from time.Time) (time.Time, error) {
	if from.IsZero() {
		return time.Time{}, errors.New("from must be a valid Date")
	}

	var cron *ParsedCron
	switch v := expressionOrCron.(type) {
	case string:
		var err error
		cron, err = ParseCronExpression(v)
		if err != nil {
			return time.Time{}, err
		}
	case *ParsedCron:
		cron = v
	default:
		return time.Time{}, errors.New("expressionOrCron must be a string or *ParsedCron")
	}

	if err := ValidateCronFeasibility(cron); err != nil {
		return time.Time{}, err
	}

	// Start at next whole minute: candidate.setSeconds(0, 0); candidate.setMinutes(+1)
	loc := from.Location()
	candidate := time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), from.Minute(), 0, 0, loc).Add(time.Minute)

	for i := 0; i < CronLookaheadMinutes; i++ {
		if CronMatchesDate(cron, candidate) {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}

	return time.Time{}, fmt.Errorf("No future run found for cron expression %q within %d minutes", cron.Expression, CronLookaheadMinutes)
}
