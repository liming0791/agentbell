package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/policy"
	"github.com/liming0791/agentbell/core/internal/service"
	"github.com/liming0791/agentbell/core/internal/settings"
)

const (
	m2MinimumCoreVersion = "0.3.0"
	settingsLockTimeout  = 2 * time.Second
	settingsLockStale    = 30 * time.Second
)

type effectiveSettings struct {
	Source   string            `json:"source"`
	Path     string            `json:"path"`
	Settings settings.Settings `json:"settings"`
}

func runSettings(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(
			"usage: agentbell settings <show|channel|event|template|quiet-hours>",
		)
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	switch args[0] {
	case "show":
		return runSettingsShow(resolved, args[1:], stdout)
	case "channel":
		return runSettingsChannel(resolved, args[1:], stdout)
	case "event":
		return runSettingsEvent(resolved, args[1:], stdout)
	case "template":
		return runSettingsTemplate(resolved, args[1:], stdout)
	case "quiet-hours":
		return runSettingsQuietHours(resolved, args[1:], stdout)
	default:
		return fmt.Errorf("unsupported settings command %q", args[0])
	}
}

func runSettingsShow(
	resolved paths.Paths,
	args []string,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("settings show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	effective := flags.Bool("effective", false, "include legacy fallback")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: agentbell settings show [--effective] [--json]")
	}
	path := settingsPath(resolved)
	value, err := settings.Load(path)
	source := "settings-sidecar"
	if errors.Is(err, settings.ErrNotFound) && *effective {
		legacy, loadErr := config.Load(resolved.ConfigFile)
		if loadErr != nil {
			return loadErr
		}
		value = defaultSettings(legacy)
		source = "legacy-config"
		err = nil
	}
	if err != nil {
		if errors.Is(err, settings.ErrNotFound) {
			return errors.New(
				"settings sidecar not found; use --effective or mutate a setting",
			)
		}
		return err
	}
	result := effectiveSettings{Source: source, Path: path, Settings: value}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(
		stdout,
		"Settings: %s\nDefault template: %s\nPolicies: %d\n",
		source,
		value.DefaultTemplate,
		len(value.Policies),
	)
	return nil
}

func runSettingsChannel(
	resolved paths.Paths,
	args []string,
	stdout io.Writer,
) error {
	if len(args) == 0 {
		return errors.New(
			"usage: agentbell settings channel <list|add|rename|remove|default> ...",
		)
	}
	action := args[0]
	transactions := config.NewChannelTransactions(resolved.ConfigFile)
	if action == "list" {
		flags := flag.NewFlagSet("settings channel list", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		asJSON := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: agentbell settings channel list [--json]")
		}
		snapshot, err := transactions.List(context.Background())
		if err != nil {
			return err
		}
		if *asJSON {
			return writeJSON(stdout, snapshot)
		}
		for _, channel := range snapshot.Config.Channels {
			marker := " "
			if channel.ID == snapshot.Config.DefaultChannel {
				marker = "*"
			}
			fmt.Fprintf(
				stdout,
				"%s %s\t%s\t%s\n",
				marker,
				channel.ID,
				channel.Name,
				channel.As,
			)
		}
		return nil
	}
	if len(args) < 2 {
		return fmt.Errorf(
			"usage: agentbell settings channel %s <id> ...",
			action,
		)
	}
	channelID := args[1]
	flags := flag.NewFlagSet("settings channel "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	name := flags.String("name", "", "channel display name")
	chatID := flags.String("chat-id", "", "Feishu chat id")
	identity := flags.String("as", "bot", "bot or user")
	setDefault := flags.Bool("default", false, "make this the default channel")
	replacement := flags.String(
		"replacement-default",
		"",
		"replacement when removing the default channel",
	)
	expectedRevision := flags.String(
		"expected-revision",
		"",
		"compare-and-swap config revision",
	)
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected settings channel arguments")
	}
	if action != "add" &&
		action != "rename" &&
		action != "remove" &&
		action != "default" {
		return fmt.Errorf("unsupported settings channel command %q", action)
	}
	if action == "rename" && strings.TrimSpace(*name) == "" {
		return errors.New("channel rename requires --name")
	}
	if action == "add" &&
		(strings.TrimSpace(*name) == "" || strings.TrimSpace(*chatID) == "") {
		return errors.New("channel add requires --name and --chat-id")
	}
	change := config.ChannelChange{
		ChannelID:          channelID,
		Name:               strings.TrimSpace(*name),
		ReplacementDefault: *replacement,
		ExpectedRevision:   *expectedRevision,
		DryRun:             *dryRun,
	}
	switch action {
	case "add":
		change.Action = config.ChannelAdd
		change.Channel = config.Channel{
			ID:     channelID,
			Name:   strings.TrimSpace(*name),
			Type:   "feishu",
			ChatID: strings.TrimSpace(*chatID),
			As:     *identity,
		}
		change.SetDefault = *setDefault
	case "rename":
		change.Action = config.ChannelRename
	case "remove":
		change.Action = config.ChannelRemove
	case "default":
		change.Action = config.ChannelSetDefault
	}
	result, err := transactions.Apply(context.Background(), change)
	if err != nil {
		if errors.Is(err, config.ErrChannelNotFound) {
			return fmt.Errorf("channel %q not found", channelID)
		}
		return err
	}
	return writeJSON(stdout, result)
}

func runSettingsEvent(
	resolved paths.Paths,
	args []string,
	stdout io.Writer,
) error {
	if len(args) < 2 {
		return errors.New(
			"usage: agentbell settings event <enable|disable> <event> [--dry-run]",
		)
	}
	action, eventName := args[0], args[1]
	if action != "enable" && action != "disable" {
		return fmt.Errorf("unsupported settings event command %q", action)
	}
	if !event.IsKnownEvent(eventName) {
		return fmt.Errorf("unknown event %q", eventName)
	}
	flags := flag.NewFlagSet("settings event "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected settings event arguments")
	}
	value, err := updateSettings(resolved, *dryRun, func(value *settings.Settings) error {
		value.Events[eventName] = action == "enable"
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{
		"dryRun":  *dryRun,
		"event":   eventName,
		"enabled": value.Events[eventName],
	})
}

func runSettingsTemplate(
	resolved paths.Paths,
	args []string,
	stdout io.Writer,
) error {
	if len(args) == 0 {
		return errors.New(
			"usage: agentbell settings template <list|set|remove|preview> ...",
		)
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("settings template list", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		asJSON := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		value, _, err := loadEffectiveSettings(resolved)
		if err != nil {
			return err
		}
		if *asJSON {
			return writeJSON(stdout, value.Templates)
		}
		for _, template := range value.Templates {
			fmt.Fprintln(stdout, template.ID)
		}
		return nil
	case "set":
		return runTemplateSet(resolved, args[1:], stdout)
	case "remove":
		return runTemplateRemove(resolved, args[1:], stdout)
	case "preview":
		return runTemplatePreview(resolved, args[1:], stdout)
	default:
		return fmt.Errorf("unsupported settings template command %q", args[0])
	}
}

func runTemplateSet(
	resolved paths.Paths,
	args []string,
	stdout io.Writer,
) error {
	if len(args) < 1 {
		return errors.New(
			"usage: agentbell settings template set <id> --body <template> [--dry-run]",
		)
	}
	id := args[0]
	flags := flag.NewFlagSet("settings template set", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	body := flags.String("body", "", "template body")
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *body == "" {
		return errors.New("template set requires --body")
	}
	value, err := updateSettings(resolved, *dryRun, func(value *settings.Settings) error {
		for index := range value.Templates {
			if value.Templates[index].ID == id {
				value.Templates[index].Body = *body
				return nil
			}
		}
		value.Templates = append(value.Templates, settings.Template{ID: id, Body: *body})
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{
		"dryRun": *dryRun,
		"id":     id,
		"count":  len(value.Templates),
	})
}

func runTemplateRemove(
	resolved paths.Paths,
	args []string,
	stdout io.Writer,
) error {
	if len(args) < 1 {
		return errors.New(
			"usage: agentbell settings template remove <id> [--dry-run]",
		)
	}
	id := args[0]
	flags := flag.NewFlagSet("settings template remove", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	value, err := updateSettings(resolved, *dryRun, func(value *settings.Settings) error {
		if value.DefaultTemplate == id {
			return fmt.Errorf("cannot remove default template %q", id)
		}
		for _, candidate := range value.Policies {
			if candidate.Action.TemplateID == id {
				return fmt.Errorf(
					"cannot remove template %q used by policy %q",
					id,
					candidate.ID,
				)
			}
		}
		index := templateIndex(value.Templates, id)
		if index < 0 {
			return fmt.Errorf("template %q not found", id)
		}
		value.Templates = append(
			value.Templates[:index:index],
			value.Templates[index+1:]...,
		)
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{
		"dryRun": *dryRun,
		"id":     id,
		"count":  len(value.Templates),
	})
}

func runTemplatePreview(
	resolved paths.Paths,
	args []string,
	stdout io.Writer,
) error {
	if len(args) < 1 {
		return errors.New(
			"usage: agentbell settings template preview <id> --event <event> --source <source>",
		)
	}
	id := args[0]
	flags := flag.NewFlagSet("settings template preview", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	eventName := flags.String("event", event.EventTaskCompleted, "event name")
	source := flags.String("source", "codex", "event source")
	project := flags.String("project", "", "project display name")
	surface := flags.String("surface", "cli", "event surface")
	runtimeName := flags.String("runtime", "host", "runtime location")
	priority := flags.String("priority", event.PriorityNormal, "event priority")
	at := flags.String("at", "", "RFC3339 occurrence time")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	value, _, err := loadEffectiveSettings(resolved)
	if err != nil {
		return err
	}
	index := templateIndex(value.Templates, id)
	if index < 0 {
		return fmt.Errorf("template %q not found", id)
	}
	occurredAt, err := parseOptionalTime(*at)
	if err != nil {
		return err
	}
	input := policy.TemplateInput{
		SourceName: sourceDisplayName(*source),
		Event:      *eventName,
		Status:     statusForEvent(*eventName),
		Project:    *project,
		Priority:   *priority,
		OccurredAt: occurredAt,
		Runtime:    *runtimeName,
		Surface:    *surface,
	}
	rendered, err := policy.Render(value.Templates[index], input)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, rendered)
	return nil
}

func runSettingsQuietHours(
	resolved paths.Paths,
	args []string,
	stdout io.Writer,
) error {
	if len(args) == 0 {
		return errors.New(
			"usage: agentbell settings quiet-hours <set|disable> ...",
		)
	}
	switch args[0] {
	case "set":
		flags := flag.NewFlagSet("settings quiet-hours set", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		timezone := flags.String("timezone", "", "IANA timezone")
		days := flags.String("days", "", "comma-separated weekdays")
		start := flags.String("start", "", "HH:MM start")
		end := flags.String("end", "", "HH:MM end")
		action := flags.String("action", policy.ActionDefer, "defer or drop")
		bypass := flags.String("bypass-events", "", "comma-separated events")
		dryRun := flags.Bool("dry-run", false, "validate without writing")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		_, err := updateSettings(resolved, *dryRun, func(value *settings.Settings) error {
			value.QuietHours = settings.QuietHours{
				Enabled:  true,
				Timezone: *timezone,
				Action:   *action,
				Intervals: []settings.QuietInterval{{
					Days:  splitList(*days),
					Start: *start,
					End:   *end,
				}},
				BypassEvents: splitList(*bypass),
			}
			return nil
		})
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{
			"dryRun":  *dryRun,
			"enabled": true,
		})
	case "disable":
		flags := flag.NewFlagSet("settings quiet-hours disable", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		dryRun := flags.Bool("dry-run", false, "validate without writing")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		_, err := updateSettings(resolved, *dryRun, func(value *settings.Settings) error {
			value.QuietHours.Enabled = false
			return nil
		})
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{
			"dryRun":  *dryRun,
			"enabled": false,
		})
	default:
		return fmt.Errorf("unsupported quiet-hours command %q", args[0])
	}
}

func runPolicy(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: agentbell policy <status|explain>")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		flags := flag.NewFlagSet("policy status", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		asJSON := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		value, source, err := loadEffectiveSettings(resolved)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(value.Policies))
		for _, candidate := range value.Policies {
			ids = append(ids, candidate.ID)
		}
		result := map[string]any{
			"source":      source,
			"policyCount": len(ids),
			"policyIds":   ids,
		}
		if *asJSON {
			return writeJSON(stdout, result)
		}
		fmt.Fprintf(stdout, "Policies: %d (%s)\n", len(ids), source)
		return nil
	case "explain":
		return runPolicyExplain(resolved, args[1:], stdout)
	default:
		return fmt.Errorf("unsupported policy command %q", args[0])
	}
}

func runPolicyExplain(
	resolved paths.Paths,
	args []string,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("policy explain", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	eventName := flags.String("event", "", "event name")
	source := flags.String("source", "", "event source")
	surface := flags.String("surface", "cli", "event surface")
	runtimeName := flags.String("runtime", "host", "runtime location")
	priority := flags.String("priority", event.PriorityNormal, "event priority")
	project := flags.String("project", "", "project display name")
	at := flags.String("at", "", "RFC3339 evaluation time")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *eventName == "" || *source == "" {
		return errors.New("policy explain requires --event and --source")
	}
	occurredAt, err := parseOptionalTime(*at)
	if err != nil {
		return err
	}
	notification := event.Notification{
		Version:        event.Version,
		Source:         *source,
		Surface:        *surface,
		Runtime:        *runtimeName,
		Event:          *eventName,
		Status:         statusForEvent(*eventName),
		OccurredAt:     occurredAt,
		IdempotencyKey: "policy-explain",
		Priority:       *priority,
		PrivacyLevel:   event.PrivacyMetadataOnly,
		Project:        *project,
	}
	value, sourceName, err := loadEffectiveSettings(resolved)
	if err != nil {
		return err
	}
	legacy, err := config.Load(resolved.ConfigFile)
	if err != nil {
		return err
	}
	decision, err := policy.Evaluate(
		value,
		notification,
		policy.Defaults{
			ChannelIDs: []string{legacy.DefaultChannel},
			TemplateID: value.DefaultTemplate,
		},
	)
	if err != nil {
		return err
	}
	quiet, err := (policy.QuietEvaluator{
		Now: func() time.Time { return occurredAt },
	}).Evaluate(value.QuietHours, notification, policy.QuietOptions{})
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{
		"source":   sourceName,
		"decision": decision,
		"quiet":    quiet,
	})
}

func loadEffectiveSettings(
	resolved paths.Paths,
) (settings.Settings, string, error) {
	value, err := settings.Load(settingsPath(resolved))
	if err == nil {
		return value, "settings-sidecar", nil
	}
	if !errors.Is(err, settings.ErrNotFound) {
		return settings.Settings{}, "", err
	}
	legacy, err := config.Load(resolved.ConfigFile)
	if err != nil {
		return settings.Settings{}, "", err
	}
	return defaultSettings(legacy), "legacy-config", nil
}

func updateSettings(
	resolved paths.Paths,
	dryRun bool,
	mutate func(*settings.Settings) error,
) (settings.Settings, error) {
	path := settingsPath(resolved)
	var result settings.Settings
	operation := func() error {
		value, _, err := loadEffectiveSettings(resolved)
		if err != nil {
			return err
		}
		if err := mutate(&value); err != nil {
			return err
		}
		if err := value.Validate(); err != nil {
			return err
		}
		result = value
		if dryRun {
			return nil
		}
		return settings.Save(path, &value)
	}
	if dryRun {
		err := operation()
		return result, err
	}
	err := withSettingsLock(path, operation)
	return result, err
}

func updateConfig(
	path string,
	dryRun bool,
	mutate func(*config.Config) error,
) (config.Config, error) {
	var result config.Config
	operation := func() error {
		value, err := config.Load(path)
		if err != nil {
			return err
		}
		if err := mutate(&value); err != nil {
			return err
		}
		if err := value.Validate(); err != nil {
			return err
		}
		result = value
		if dryRun {
			return nil
		}
		return config.Save(path, &value)
	}
	if dryRun {
		err := operation()
		return result, err
	}
	err := withSettingsLock(path, operation)
	return result, err
}

func withSettingsLock(path string, operation func() error) error {
	lockPath := path + ".agentbell.lock"
	deadline := time.Now().Add(settingsLockTimeout)
	for {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			defer os.Remove(lockPath)
			return operation()
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("acquire settings lock: %w", err)
		}
		info, statErr := os.Stat(lockPath)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("inspect settings lock: %w", statErr)
		}
		if time.Since(info.ModTime()) > settingsLockStale {
			if removeErr := os.Remove(lockPath); removeErr != nil &&
				!errors.Is(removeErr, os.ErrNotExist) {
				return fmt.Errorf("remove stale settings lock: %w", removeErr)
			}
			continue
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for settings lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func defaultSettings(legacy config.Config) settings.Settings {
	events := make(settings.EventSwitches)
	for _, eventName := range event.KnownEvents() {
		events[eventName] = legacy.EventEnabled(eventName)
	}
	return settings.Settings{
		Version:         settings.Version,
		MinCoreVersion:  m2MinimumCoreVersion,
		Events:          events,
		DefaultTemplate: "default",
		Templates: []settings.Template{{
			ID:   "default",
			Body: "[{{sourceName}}] {{event}} · {{project}}",
		}},
		QuietHours: settings.QuietHours{Enabled: false},
		Policies:   []settings.Policy{},
	}
}

func settingsPath(resolved paths.Paths) string {
	return filepath.Join(filepath.Dir(resolved.ConfigFile), "settings.json")
}

func channelIndex(channels []config.Channel, id string) int {
	for index := range channels {
		if channels[index].ID == id {
			return index
		}
	}
	return -1
}

func templateIndex(templates []settings.Template, id string) int {
	for index := range templates {
		if templates[index].ID == id {
			return index
		}
	}
	return -1
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		result = append(result, strings.TrimSpace(part))
	}
	return result
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Now().UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("--at must be RFC3339: %w", err)
	}
	return parsed.UTC(), nil
}

func statusForEvent(name string) string {
	switch name {
	case event.EventTaskCompleted, event.EventSubagentCompleted:
		return event.StatusCompleted
	case event.EventTaskFailed, event.EventSessionInterrupted:
		return event.StatusFailed
	case event.EventAgentWaiting, event.EventApprovalRequired:
		return event.StatusAttention
	default:
		return event.StatusInfo
	}
}

func sourceDisplayName(source string) string {
	switch source {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude Code"
	case "kimi":
		return "Kimi Code"
	case "opencode":
		return "OpenCode"
	case "qoder":
		return "Qoder"
	case "qoder-work":
		return "QoderWork"
	case "trae":
		return "TRAE"
	default:
		return source
	}
}

func newM2Processor(resolved paths.Paths) *service.Processor {
	return &service.Processor{
		LoadSettings: func() (settings.Settings, error) {
			return settings.Load(settingsPath(resolved))
		},
		SourceName: func(notification event.Notification) string {
			return sourceDisplayName(notification.Source)
		},
	}
}
