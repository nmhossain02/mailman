package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nmhossain02/mailman/internal/core"
)

type Translator struct {
	Backend Backend
	Model   string
}

func (t Translator) Translate(ctx context.Context, text string, tc core.TranslationContext) (core.CommandDraft, error) {
	if HasUnsupportedDateLanguage(text) {
		return clarify("Use an exact date, today/tomorrow, next weekday, or in N days/weeks."), nil
	}
	task, err := BuiltinTask("translate_command", t.Model)
	if err != nil {
		return core.CommandDraft{}, err
	}
	resolvedDates := ResolveRelativeDates(text, tc.Now, tc.Timezone)
	input, err := json.Marshal(map[string]any{"command": text, "context": tc, "resolved_dates": resolvedDates})
	if err != nil {
		return core.CommandDraft{}, err
	}
	result, err := RunTask(ctx, t.Backend, task, input, "")
	if err != nil {
		return core.CommandDraft{}, err
	}
	command := core.CommandDraft(result.Output.(CommandResult))
	return ResolveCommandReferences(command, tc), nil
}

func ResolveCommandReferences(command core.CommandDraft, tc core.TranslationContext) core.CommandDraft {
	ref := strings.TrimSpace(command.Reference)
	if ref == "" {
		return command
	}
	if strings.EqualFold(ref, "this") || strings.EqualFold(ref, "selected") {
		if tc.SelectedID == "" {
			return clarify("No item is selected; choose an item first.")
		}
		command.Reference = tc.SelectedID
		if command.Target == "" {
			command.Target = tc.SelectedType
		}
		return command
	}
	candidates := append([]string{}, tc.AccountNames...)
	candidates = append(candidates, tc.LabelNames...)
	candidates = append(candidates, tc.QueueNames...)
	candidates = append(candidates, tc.RuleNames...)
	candidates = append(candidates, tc.ScheduleNames...)
	matches := make([]string, 0)
	for _, candidate := range candidates {
		if strings.EqualFold(candidate, ref) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		for _, candidate := range candidates {
			if strings.Contains(strings.ToLower(candidate), strings.ToLower(ref)) {
				matches = append(matches, candidate)
			}
		}
	}
	sort.Strings(matches)
	if len(matches) != 1 {
		return clarify(fmt.Sprintf("Reference %q matches %d items; choose one exact name.", ref, len(matches)))
	}
	command.Reference = matches[0]
	return command
}

func clarify(message string) core.CommandDraft {
	return core.CommandDraft{Intent: "clarify", Clarification: message}
}

var relativePattern = regexp.MustCompile(`(?i)\b(today|tomorrow|next\s+(?:monday|tuesday|wednesday|thursday|friday|saturday|sunday)|in\s+[0-9]+\s+(?:day|days|week|weeks)|[0-9]{4}-[0-9]{2}-[0-9]{2})\b`)
var unsupportedDatePattern = regexp.MustCompile(`(?i)\b(soon|later|tonight|this\s+(?:week|month|monday|tuesday|wednesday|thursday|friday|saturday|sunday)|next\s+(?:week|month|year)|end\s+of|beginning\s+of)\b`)

func HasUnsupportedDateLanguage(text string) bool { return unsupportedDatePattern.MatchString(text) }

func ResolveRelativeDates(text string, now time.Time, timezone string) map[string]string {
	location := now.Location()
	if timezone != "" {
		if parsed, err := time.LoadLocation(timezone); err == nil {
			location = parsed
		}
	}
	base := now.In(location)
	resolved := map[string]string{}
	for _, phrase := range relativePattern.FindAllString(text, -1) {
		if date, ok := ParseRelativeDate(phrase, base); ok {
			resolved[phrase] = date.Format("2006-01-02")
		}
	}
	return resolved
}

func ParseRelativeDate(phrase string, now time.Time) (time.Time, bool) {
	phrase = strings.ToLower(strings.TrimSpace(phrase))
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if parsed, err := time.ParseInLocation("2006-01-02", phrase, now.Location()); err == nil {
		return parsed, true
	}
	switch phrase {
	case "today":
		return date, true
	case "tomorrow":
		return date.AddDate(0, 0, 1), true
	}
	if strings.HasPrefix(phrase, "next ") {
		weekdays := map[string]time.Weekday{"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6}
		wanted, ok := weekdays[strings.TrimPrefix(phrase, "next ")]
		if !ok {
			return time.Time{}, false
		}
		days := (int(wanted) - int(date.Weekday()) + 7) % 7
		if days == 0 {
			days = 7
		}
		return date.AddDate(0, 0, days), true
	}
	parts := strings.Fields(phrase)
	if len(parts) == 3 && parts[0] == "in" {
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			return time.Time{}, false
		}
		if strings.HasPrefix(parts[2], "week") {
			n *= 7
		}
		return date.AddDate(0, 0, n), true
	}
	return time.Time{}, false
}
