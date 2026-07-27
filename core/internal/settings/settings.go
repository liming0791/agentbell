// Package settings defines the versioned M2 notification settings sidecar.
//
// Transport credentials and Feishu channel addresses remain in config.json.
// This package owns only notification behavior that older Core versions do not
// understand: explicit event switches, templates, quiet hours, and ordered
// local policies.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/liming0791/agentbell/core/internal/event"
)

const (
	Version               = 1
	MaximumTemplateBytes  = 4 * 1024
	MaximumTemplateLength = MaximumTemplateBytes
	maximumIdentifier     = 64
	maximumPolicies       = 256
	maximumPolicyProjects = 256
)

var (
	knownEvents = event.KnownEvents()
	knownDays   = stringSet([]string{
		"mon", "tue", "wed", "thu", "fri", "sat", "sun",
	})
	knownPlaceholders = stringSet([]string{
		"sourceName", "event", "status", "project", "priority",
		"occurredAt", "runtime", "surface",
	})

	identifierPattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	semverPattern      = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	placeholderPattern = regexp.MustCompile(`\{\{\s*([A-Za-z][A-Za-z0-9]*)\s*\}\}`)
	timePattern        = regexp.MustCompile(`^([0-9]{2}):([0-9]{2})$`)
)

type Settings struct {
	Version         int           `json:"version"`
	MinCoreVersion  string        `json:"minCoreVersion"`
	Events          EventSwitches `json:"events"`
	DefaultTemplate string        `json:"defaultTemplate"`
	Templates       []Template    `json:"templates"`
	QuietHours      QuietHours    `json:"quietHours"`
	Policies        []Policy      `json:"policies"`
}

// EventSwitches intentionally uses a map so false is different from absence.
// Every event known to settings version 1 must be present.
type EventSwitches map[string]bool

type Template struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

type QuietHours struct {
	Enabled      bool            `json:"enabled"`
	Timezone     string          `json:"timezone,omitempty"`
	Action       string          `json:"action,omitempty"`
	Intervals    []QuietInterval `json:"intervals,omitempty"`
	BypassEvents []string        `json:"bypassEvents,omitempty"`
}

type QuietInterval struct {
	Days  []string `json:"days"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

// Policy order is significant: evaluators apply entries in slice order.
type Policy struct {
	ID     string       `json:"id"`
	Match  PolicyMatch  `json:"match"`
	Action PolicyAction `json:"action"`
}

type PolicyMatch struct {
	Events     []string `json:"events,omitempty"`
	Sources    []string `json:"sources,omitempty"`
	Surfaces   []string `json:"surfaces,omitempty"`
	Runtimes   []string `json:"runtimes,omitempty"`
	Priorities []string `json:"priorities,omitempty"`
	Projects   []string `json:"projects,omitempty"`
}

type PolicyAction struct {
	Enabled    *bool    `json:"enabled,omitempty"`
	ChannelIDs []string `json:"channelIds,omitempty"`
	TemplateID string   `json:"templateId,omitempty"`
}

func (settings Settings) Validate() error {
	if settings.Version != Version {
		return fmt.Errorf(
			"unsupported settings version %d; expected version %d",
			settings.Version,
			Version,
		)
	}
	if !semverPattern.MatchString(settings.MinCoreVersion) {
		return errors.New("minCoreVersion must be a semantic version")
	}
	if err := settings.Events.Validate(); err != nil {
		return err
	}

	templateIDs := make(map[string]bool, len(settings.Templates))
	if len(settings.Templates) == 0 {
		return errors.New("at least one template is required")
	}
	for index, template := range settings.Templates {
		if err := template.Validate(); err != nil {
			return fmt.Errorf("templates[%d]: %w", index, err)
		}
		if templateIDs[template.ID] {
			return fmt.Errorf("duplicate template id %q", template.ID)
		}
		templateIDs[template.ID] = true
	}
	if !templateIDs[settings.DefaultTemplate] {
		return fmt.Errorf(
			"default template %q does not exist",
			settings.DefaultTemplate,
		)
	}
	if err := settings.QuietHours.Validate(); err != nil {
		return fmt.Errorf("quietHours: %w", err)
	}
	if settings.Policies == nil {
		return errors.New("policies must be an array")
	}
	if len(settings.Policies) > maximumPolicies {
		return fmt.Errorf("policies exceed maximum of %d", maximumPolicies)
	}
	policyIDs := make(map[string]bool, len(settings.Policies))
	for index, policy := range settings.Policies {
		if err := policy.Validate(templateIDs); err != nil {
			return fmt.Errorf("policies[%d]: %w", index, err)
		}
		if policyIDs[policy.ID] {
			return fmt.Errorf("duplicate policy id %q", policy.ID)
		}
		policyIDs[policy.ID] = true
	}
	return nil
}

func (events EventSwitches) Validate() error {
	for name := range events {
		if !event.IsKnownEvent(name) {
			return fmt.Errorf("unknown event %q", name)
		}
	}
	for _, name := range knownEvents {
		if _, exists := events[name]; !exists {
			return fmt.Errorf("missing event switch %q", name)
		}
	}
	return nil
}

// UnmarshalJSON rejects duplicate object keys. encoding/json otherwise keeps
// only the last value, which could silently reverse an explicit event switch.
func (events *EventSwitches) UnmarshalJSON(raw []byte) error {
	if events == nil {
		return errors.New("event switches target is nil")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return errors.New("events must be an object")
	}
	result := make(EventSwitches)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("event name must be a string")
		}
		if _, exists := result[name]; exists {
			return fmt.Errorf("duplicate event %q", name)
		}
		var enabled bool
		if err := decoder.Decode(&enabled); err != nil {
			return fmt.Errorf("event %q must be boolean: %w", name, err)
		}
		result[name] = enabled
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data after events")
	}
	*events = result
	return nil
}

func (template Template) Validate() error {
	if !identifierPattern.MatchString(template.ID) {
		return fmt.Errorf(
			"template id must match %s",
			identifierPattern.String(),
		)
	}
	if template.Body == "" {
		return errors.New("template body is required")
	}
	if len(template.Body) > MaximumTemplateBytes {
		return fmt.Errorf(
			"template body exceeds %d bytes",
			MaximumTemplateBytes,
		)
	}
	var placeholderErr error
	remainder := placeholderPattern.ReplaceAllStringFunc(
		template.Body,
		func(match string) string {
			parts := placeholderPattern.FindStringSubmatch(match)
			if len(parts) != 2 || !knownPlaceholders[parts[1]] {
				name := ""
				if len(parts) == 2 {
					name = parts[1]
				}
				placeholderErr = fmt.Errorf("unknown placeholder %q", name)
			}
			return ""
		},
	)
	if placeholderErr != nil {
		return placeholderErr
	}
	if strings.Contains(remainder, "{{") || strings.Contains(remainder, "}}") {
		return errors.New("malformed placeholder")
	}
	return nil
}

func (quiet QuietHours) Validate() error {
	if !quiet.Enabled {
		if quiet.Timezone != "" {
			if err := validateTimezone(quiet.Timezone); err != nil {
				return err
			}
		}
		if quiet.Action != "" && quiet.Action != "defer" && quiet.Action != "drop" {
			return fmt.Errorf("unsupported action %q", quiet.Action)
		}
		if err := validateEventList("bypass", quiet.BypassEvents); err != nil {
			return err
		}
		for index, interval := range quiet.Intervals {
			if err := interval.Validate(); err != nil {
				return fmt.Errorf("intervals[%d]: %w", index, err)
			}
		}
		return nil
	}
	if err := validateTimezone(quiet.Timezone); err != nil {
		return err
	}
	if quiet.Action != "defer" && quiet.Action != "drop" {
		return fmt.Errorf("unsupported action %q", quiet.Action)
	}
	if len(quiet.Intervals) == 0 {
		return errors.New("at least one interval is required when enabled")
	}
	for index, interval := range quiet.Intervals {
		if err := interval.Validate(); err != nil {
			return fmt.Errorf("intervals[%d]: %w", index, err)
		}
	}
	return validateEventList("bypass", quiet.BypassEvents)
}

func (interval QuietInterval) Validate() error {
	if len(interval.Days) == 0 {
		return errors.New("at least one day is required")
	}
	seen := make(map[string]bool, len(interval.Days))
	for _, day := range interval.Days {
		if !knownDays[day] {
			return fmt.Errorf("unsupported day %q", day)
		}
		if seen[day] {
			return fmt.Errorf("duplicate day %q", day)
		}
		seen[day] = true
	}
	if err := validateClockTime(interval.Start); err != nil {
		return fmt.Errorf("start time: %w", err)
	}
	if err := validateClockTime(interval.End); err != nil {
		return fmt.Errorf("end time: %w", err)
	}
	if interval.Start == interval.End {
		return errors.New("start and end must differ")
	}
	return nil
}

func (policy Policy) Validate(templateIDs map[string]bool) error {
	if !identifierPattern.MatchString(policy.ID) {
		return fmt.Errorf(
			"policy id must match %s",
			identifierPattern.String(),
		)
	}
	if err := validateEventList("policy", policy.Match.Events); err != nil {
		return err
	}
	if err := validateSelector(
		"source",
		policy.Match.Sources,
		event.IsKnownSource,
	); err != nil {
		return err
	}
	if err := validateSelector(
		"surface",
		policy.Match.Surfaces,
		event.IsKnownSurface,
	); err != nil {
		return err
	}
	if err := validateSelector(
		"runtime",
		policy.Match.Runtimes,
		event.IsKnownRuntime,
	); err != nil {
		return err
	}
	if err := validateSelector(
		"priority",
		policy.Match.Priorities,
		event.IsKnownPriority,
	); err != nil {
		return err
	}
	if err := validateProjects(policy.Match.Projects); err != nil {
		return err
	}
	if policy.Action.Enabled == nil &&
		len(policy.Action.ChannelIDs) == 0 &&
		policy.Action.TemplateID == "" {
		return errors.New(
			"policy action must set enabled, channelIds, or templateId",
		)
	}
	if policy.Action.ChannelIDs != nil && len(policy.Action.ChannelIDs) == 0 {
		return errors.New("policy channelIds cannot be an empty array")
	}
	seenChannels := make(map[string]bool, len(policy.Action.ChannelIDs))
	for _, channelID := range policy.Action.ChannelIDs {
		if channelID == "" {
			return errors.New("policy channelId cannot be empty")
		}
		if utf8.RuneCountInString(channelID) > maximumIdentifier {
			return fmt.Errorf(
				"policy channelId exceeds %d characters",
				maximumIdentifier,
			)
		}
		if seenChannels[channelID] {
			return fmt.Errorf("policy contains duplicate channelId %q", channelID)
		}
		seenChannels[channelID] = true
	}
	if policy.Action.TemplateID != "" && !templateIDs[policy.Action.TemplateID] {
		return fmt.Errorf(
			"policy template %q does not exist",
			policy.Action.TemplateID,
		)
	}
	return nil
}

func validateTimezone(name string) error {
	if name == "" {
		return errors.New("IANA timezone is required")
	}
	if name != "UTC" && !strings.Contains(name, "/") {
		return fmt.Errorf("timezone %q must be an IANA timezone", name)
	}
	if _, err := time.LoadLocation(name); err != nil {
		return fmt.Errorf("load timezone %q: %w", name, err)
	}
	return nil
}

func validateClockTime(value string) error {
	match := timePattern.FindStringSubmatch(value)
	if len(match) != 3 {
		return fmt.Errorf("%q must use HH:MM", value)
	}
	hour, _ := strconv.Atoi(match[1])
	minute, _ := strconv.Atoi(match[2])
	if hour > 23 || minute > 59 {
		return fmt.Errorf("%q is outside 00:00-23:59", value)
	}
	return nil
}

func validateEventList(label string, values []string) error {
	seen := make(map[string]bool, len(values))
	for _, name := range values {
		if !event.IsKnownEvent(name) {
			return fmt.Errorf("%s contains unknown event %q", label, name)
		}
		if seen[name] {
			return fmt.Errorf("%s contains duplicate event %q", label, name)
		}
		seen[name] = true
	}
	return nil
}

func validateSelector(
	label string,
	values []string,
	allowed func(string) bool,
) error {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !allowed(value) {
			return fmt.Errorf("unknown %s %q", label, value)
		}
		if seen[value] {
			return fmt.Errorf("duplicate %s %q", label, value)
		}
		seen[value] = true
	}
	return nil
}

func validateProjects(projects []string) error {
	if len(projects) > maximumPolicyProjects {
		return fmt.Errorf(
			"policy projects exceed maximum of %d",
			maximumPolicyProjects,
		)
	}
	seen := make(map[string]bool, len(projects))
	for _, project := range projects {
		if strings.TrimSpace(project) == "" {
			return errors.New("policy project cannot be empty")
		}
		if utf8.RuneCountInString(project) > 120 {
			return errors.New("policy project exceeds 120 characters")
		}
		if seen[project] {
			return fmt.Errorf("policy contains duplicate project %q", project)
		}
		seen[project] = true
	}
	return nil
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
