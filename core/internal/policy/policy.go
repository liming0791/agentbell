// Package policy evaluates M2 notification routing, templates, and quiet hours.
package policy

import (
	"errors"
	"fmt"

	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/settings"
)

type Defaults struct {
	ChannelIDs []string
	TemplateID string
}

type Decision struct {
	Enabled    bool     `json:"enabled"`
	ChannelIDs []string `json:"channelIds"`
	TemplateID string   `json:"templateId"`
	Matched    bool     `json:"matched"`
	PolicyID   string   `json:"policyId,omitempty"`
}

// Evaluate applies the first matching policy. Empty selector slices are
// wildcards. An omitted action field inherits the caller-provided routing
// default or the explicit global event switch.
func Evaluate(
	settingsValue settings.Settings,
	notification event.Notification,
	defaults Defaults,
) (Decision, error) {
	if err := settingsValue.Validate(); err != nil {
		return Decision{}, fmt.Errorf("settings: %w", err)
	}
	if err := notification.Validate(); err != nil {
		return Decision{}, fmt.Errorf("notification: %w", err)
	}
	if err := validateDefaults(settingsValue, defaults); err != nil {
		return Decision{}, err
	}

	decision := Decision{
		Enabled:    settingsValue.Events[notification.Event],
		ChannelIDs: append([]string(nil), defaults.ChannelIDs...),
		TemplateID: defaults.TemplateID,
	}
	for _, candidate := range settingsValue.Policies {
		if !matches(candidate.Match, notification) {
			continue
		}
		decision.Matched = true
		decision.PolicyID = candidate.ID
		if candidate.Action.Enabled != nil {
			decision.Enabled = *candidate.Action.Enabled
		}
		if len(candidate.Action.ChannelIDs) > 0 {
			decision.ChannelIDs = append(
				[]string(nil),
				candidate.Action.ChannelIDs...,
			)
		}
		if candidate.Action.TemplateID != "" {
			decision.TemplateID = candidate.Action.TemplateID
		}
		break
	}
	return decision, nil
}

func validateDefaults(
	settingsValue settings.Settings,
	defaults Defaults,
) error {
	if len(defaults.ChannelIDs) == 0 {
		return errors.New("at least one default channel is required")
	}
	seen := make(map[string]bool, len(defaults.ChannelIDs))
	for _, channelID := range defaults.ChannelIDs {
		if channelID == "" {
			return errors.New("default channel cannot be empty")
		}
		if seen[channelID] {
			return fmt.Errorf("duplicate default channel %q", channelID)
		}
		seen[channelID] = true
	}
	if defaults.TemplateID == "" {
		return errors.New("default template is required")
	}
	for _, candidate := range settingsValue.Templates {
		if candidate.ID == defaults.TemplateID {
			return nil
		}
	}
	return fmt.Errorf(
		"default template %q does not exist",
		defaults.TemplateID,
	)
}

func matches(match settings.PolicyMatch, notification event.Notification) bool {
	return matchesString(match.Events, notification.Event) &&
		matchesString(match.Sources, notification.Source) &&
		matchesString(match.Surfaces, notification.Surface) &&
		matchesString(match.Runtimes, notification.Runtime) &&
		matchesString(match.Priorities, notification.Priority) &&
		matchesString(match.Projects, notification.Project)
}

func matchesString(candidates []string, value string) bool {
	if len(candidates) == 0 {
		return true
	}
	for _, candidate := range candidates {
		if candidate == value {
			return true
		}
	}
	return false
}
