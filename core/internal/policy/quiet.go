package policy

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/settings"
)

const (
	ActionDefer = "defer"
	ActionDrop  = "drop"

	ReasonDisabled     = "disabled"
	ReasonNotQuiet     = "not-quiet"
	ReasonUrgentBypass = "urgent-bypass"
	ReasonEventBypass  = "event-bypass"
	ReasonQuietHours   = "quiet-hours"
)

type QuietOptions struct {
	// UrgentBypass defaults to true when nil.
	UrgentBypass *bool
}

type QuietDecision struct {
	Active     bool      `json:"active"`
	Action     string    `json:"action,omitempty"`
	DeferUntil time.Time `json:"deferUntil,omitempty"`
	Reason     string    `json:"reason"`
}

type QuietEvaluator struct {
	Now func() time.Time
}

func (evaluator QuietEvaluator) Evaluate(
	quiet settings.QuietHours,
	notification event.Notification,
	options QuietOptions,
) (QuietDecision, error) {
	if err := quiet.Validate(); err != nil {
		return QuietDecision{}, fmt.Errorf("quiet hours: %w", err)
	}
	if err := notification.Validate(); err != nil {
		return QuietDecision{}, fmt.Errorf("notification: %w", err)
	}
	if !quiet.Enabled {
		return QuietDecision{Reason: ReasonDisabled}, nil
	}
	urgentBypass := true
	if options.UrgentBypass != nil {
		urgentBypass = *options.UrgentBypass
	}
	if urgentBypass && notification.Priority == "urgent" {
		return QuietDecision{Reason: ReasonUrgentBypass}, nil
	}
	for _, eventName := range quiet.BypassEvents {
		if eventName == notification.Event {
			return QuietDecision{Reason: ReasonEventBypass}, nil
		}
	}

	location, err := time.LoadLocation(quiet.Timezone)
	if err != nil {
		return QuietDecision{}, fmt.Errorf(
			"load quiet-hours timezone: %w",
			err,
		)
	}
	now := evaluator.now().In(location)
	var latestEnd time.Time
	for _, interval := range quiet.Intervals {
		start, end, active, err := matchingWindow(now, interval, location)
		if err != nil {
			return QuietDecision{}, err
		}
		if !active || now.Before(start) || !now.Before(end) {
			continue
		}
		if latestEnd.IsZero() || end.After(latestEnd) {
			latestEnd = end
		}
	}
	if latestEnd.IsZero() {
		return QuietDecision{Reason: ReasonNotQuiet}, nil
	}
	decision := QuietDecision{
		Active: true,
		Action: quiet.Action,
		Reason: ReasonQuietHours,
	}
	switch quiet.Action {
	case ActionDefer:
		decision.DeferUntil = latestEnd.UTC()
	case ActionDrop:
		// Drop is only reachable when settings explicitly contain "drop".
	default:
		return QuietDecision{}, errors.New("quiet-hours action is not explicit")
	}
	return decision, nil
}

func (evaluator QuietEvaluator) now() time.Time {
	if evaluator.Now != nil {
		return evaluator.Now()
	}
	return time.Now()
}

func matchingWindow(
	now time.Time,
	interval settings.QuietInterval,
	location *time.Location,
) (time.Time, time.Time, bool, error) {
	startHour, startMinute, err := parseTime(interval.Start)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	endHour, endMinute, err := parseTime(interval.End)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	startClock := startHour*60 + startMinute
	endClock := endHour*60 + endMinute
	nowClock := now.Hour()*60 + now.Minute()

	if startClock < endClock {
		if !containsWeekday(interval.Days, now.Weekday()) {
			return time.Time{}, time.Time{}, false, nil
		}
		start := civilTime(now, 0, startHour, startMinute, location)
		end := civilTime(now, 0, endHour, endMinute, location)
		return start, end, !now.Before(start) && now.Before(end), nil
	}

	if nowClock >= startClock &&
		containsWeekday(interval.Days, now.Weekday()) {
		start := civilTime(now, 0, startHour, startMinute, location)
		end := civilTime(now, 1, endHour, endMinute, location)
		return start, end, !now.Before(start) && now.Before(end), nil
	}
	previous := now.AddDate(0, 0, -1)
	if nowClock < endClock &&
		containsWeekday(interval.Days, previous.Weekday()) {
		start := civilTime(now, -1, startHour, startMinute, location)
		end := civilTime(now, 0, endHour, endMinute, location)
		return start, end, !now.Before(start) && now.Before(end), nil
	}
	return time.Time{}, time.Time{}, false, nil
}

func civilTime(
	base time.Time,
	dayOffset, hour, minute int,
	location *time.Location,
) time.Time {
	day := base.AddDate(0, 0, dayOffset)
	return time.Date(
		day.Year(),
		day.Month(),
		day.Day(),
		hour,
		minute,
		0,
		0,
		location,
	)
}

func parseTime(value string) (int, int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid quiet-hours time %q", value)
	}
	hour, hourErr := strconv.Atoi(parts[0])
	minute, minuteErr := strconv.Atoi(parts[1])
	if hourErr != nil || minuteErr != nil ||
		hour < 0 || hour > 23 ||
		minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid quiet-hours time %q", value)
	}
	return hour, minute, nil
}

func containsWeekday(days []string, weekday time.Weekday) bool {
	name := [...]string{
		"sun", "mon", "tue", "wed", "thu", "fri", "sat",
	}[weekday]
	for _, day := range days {
		if day == name {
			return true
		}
	}
	return false
}
