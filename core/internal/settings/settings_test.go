package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func validSettings() Settings {
	return Settings{
		Version:        Version,
		MinCoreVersion: "0.3.0",
		Events: EventSwitches{
			"task.completed":      true,
			"task.failed":         true,
			"agent.waiting":       true,
			"approval.required":   true,
			"session.interrupted": false,
			"subagent.completed":  false,
			"agent.info":          false,
		},
		DefaultTemplate: "standard",
		Templates: []Template{{
			ID: "standard",
			Body: "{{status}}\nAgentBell · {{sourceName}}\n" +
				"事件：{{event}}\n项目：{{project}}\n优先级：{{priority}}\n" +
				"时间：{{occurredAt}}\n运行位置：{{runtime}}/{{surface}}",
		}},
		QuietHours: QuietHours{
			Enabled:  true,
			Timezone: "Asia/Singapore",
			Action:   "defer",
			Intervals: []QuietInterval{{
				Days:  []string{"mon", "tue", "wed", "thu", "fri"},
				Start: "22:00",
				End:   "08:00",
			}},
			BypassEvents: []string{"approval.required"},
		},
		Policies: []Policy{
			{
				ID: "failures-first",
				Match: PolicyMatch{
					Events:     []string{"task.failed"},
					Sources:    []string{"codex"},
					Surfaces:   []string{"cli"},
					Runtimes:   []string{"host"},
					Priorities: []string{"high", "urgent"},
					Projects:   []string{"agentbell"},
				},
				Action: PolicyAction{
					Enabled:    boolPointer(true),
					ChannelIDs: []string{"team", "on-call"},
					TemplateID: "standard",
				},
			},
			{
				ID: "completion-default",
				Match: PolicyMatch{
					Events: []string{"task.completed"},
				},
				Action: PolicyAction{Enabled: boolPointer(false)},
			},
		},
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestLoadStrictJSONAndPreservesPolicyOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	value := validSettings()
	if err := Save(path, &value); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Policies) != 2 ||
		loaded.Policies[0].ID != "failures-first" ||
		loaded.Policies[1].ID != "completion-default" {
		t.Fatalf("policy order changed: %#v", loaded.Policies)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unknownTopLevel := strings.Replace(
		string(raw),
		`"version": 1,`,
		`"version": 1, "unexpected": true,`,
		1,
	)
	if err := os.WriteFile(path, []byte(unknownTopLevel), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown top-level field was accepted: %v", err)
	}

	unknownNested := strings.Replace(
		string(raw),
		`"id": "standard",`,
		`"id": "standard", "execute": "env",`,
		1,
	)
	if err := os.WriteFile(path, []byte(unknownNested), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown nested field was accepted: %v", err)
	}

	if err := os.WriteFile(path, append(raw, []byte(`{"version":1}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing JSON was accepted: %v", err)
	}

	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"version",
		"minCoreVersion",
		"events",
		"defaultTemplate",
		"templates",
		"quietHours",
		"policies",
	} {
		t.Run("missing_"+field, func(t *testing.T) {
			candidate := make(map[string]any, len(document))
			for name, value := range document {
				candidate[name] = value
			}
			delete(candidate, field)
			missingRequired, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, missingRequired, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil ||
				!strings.Contains(err.Error(), field) {
				t.Fatalf("missing required field %q was accepted: %v", field, err)
			}
		})
	}

	duplicateTopLevel := strings.Replace(
		string(raw),
		`"version": 1,`,
		`"version": 1, "version": 1,`,
		1,
	)
	if err := os.WriteFile(path, []byte(duplicateTopLevel), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "duplicate field") {
		t.Fatalf("duplicate top-level field was accepted: %v", err)
	}
}

func TestLoadUsesSchemaAlignedDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := `{
		"version":1,
		"minCoreVersion":"0.3.0",
		"events":{
			"task.completed":true,
			"task.failed":true,
			"agent.waiting":true,
			"approval.required":true,
			"session.interrupted":false,
			"subagent.completed":false,
			"agent.info":false
		},
		"defaultTemplate":"standard",
		"templates":[{"id":"standard","body":"{{event}}"}],
		"quietHours":{"timezone":"","action":""},
		"policies":[{
			"id":"all-events",
			"action":{"enabled":false,"templateId":""}
		}]
	}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.QuietHours.Enabled ||
		len(loaded.Policies) != 1 ||
		len(loaded.Policies[0].Match.Events) != 0 ||
		loaded.Policies[0].Action.Enabled == nil ||
		*loaded.Policies[0].Action.Enabled {
		t.Fatalf("default/optional semantics changed: %#v", loaded)
	}
}

func TestLoadMissingReturnsErrNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestLoadRejectsOversizedDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	value := validSettings()
	if err := Save(path, &value); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(strings.Repeat(" ", maximumSettingsBytes))...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("oversized settings were accepted: %v", err)
	}
}

func TestLoadRejectsWrongJSONShapesAndTypes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "root array", raw: `[]`, want: "must be an object"},
		{
			name: "events array",
			raw: `{
				"version":1,
				"minCoreVersion":"0.3.0",
				"events":[],
				"defaultTemplate":"standard",
				"templates":[{"id":"standard","body":"{{event}}"}],
				"quietHours":{"enabled":false},
				"policies":[]
			}`,
			want: "events must be an object",
		},
		{
			name: "event non-boolean",
			raw: `{
				"version":1,
				"minCoreVersion":"0.3.0",
				"events":{
					"task.completed":"yes",
					"task.failed":true,
					"agent.waiting":true,
					"approval.required":true,
					"session.interrupted":false,
					"subagent.completed":false,
					"agent.info":false
				},
				"defaultTemplate":"standard",
				"templates":[{"id":"standard","body":"{{event}}"}],
				"quietHours":{"enabled":false},
				"policies":[]
			}`,
			want: "must be boolean",
		},
		{
			name: "null quiet hours",
			raw: `{
				"version":1,
				"minCoreVersion":"0.3.0",
				"events":{
					"task.completed":true,
					"task.failed":true,
					"agent.waiting":true,
					"approval.required":true,
					"session.interrupted":false,
					"subagent.completed":false,
					"agent.info":false
				},
				"defaultTemplate":"standard",
				"templates":[{"id":"standard","body":"{{event}}"}],
				"quietHours":null,
				"policies":[]
			}`,
			want: "cannot be null",
		},
		{
			name: "null policies",
			raw: `{
				"version":1,
				"minCoreVersion":"0.3.0",
				"events":{
					"task.completed":true,
					"task.failed":true,
					"agent.waiting":true,
					"approval.required":true,
					"session.interrupted":false,
					"subagent.completed":false,
					"agent.info":false
				},
				"defaultTemplate":"standard",
				"templates":[{"id":"standard","body":"{{event}}"}],
				"quietHours":{"enabled":false},
				"policies":null
			}`,
			want: "cannot be null",
		},
		{
			name: "nested null",
			raw: `{
				"version":1,
				"minCoreVersion":"0.3.0",
				"events":{
					"task.completed":true,
					"task.failed":true,
					"agent.waiting":true,
					"approval.required":true,
					"session.interrupted":false,
					"subagent.completed":false,
					"agent.info":false
				},
				"defaultTemplate":"standard",
				"templates":[{"id":"standard","body":"{{event}}"}],
				"quietHours":{"enabled":false,"timezone":null},
				"policies":[]
			}`,
			want: "cannot be null",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEventSwitchesAreExplicitAndStrict(t *testing.T) {
	value := validSettings()
	for name := range value.Events {
		value.Events[name] = false
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("all events disabled must be valid: %v", err)
	}

	delete(value.Events, "agent.info")
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "missing event") {
		t.Fatalf("missing explicit event was accepted: %v", err)
	}

	value = validSettings()
	value.Events["future.event"] = true
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Fatalf("unknown event was accepted: %v", err)
	}

	path := filepath.Join(t.TempDir(), "settings.json")
	raw := `{
		"version":1,
		"minCoreVersion":"0.3.0",
		"events":{
			"task.completed":true,
			"task.completed":false,
			"task.failed":true,
			"agent.waiting":true,
			"approval.required":true,
			"session.interrupted":false,
			"subagent.completed":false,
			"agent.info":false
		},
		"defaultTemplate":"standard",
		"templates":[{"id":"standard","body":"{{event}}"}],
		"quietHours":{"enabled":false},
		"policies":[]
	}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "duplicate event") {
		t.Fatalf("duplicate event key was accepted: %v", err)
	}

	value = validSettings()
	value.QuietHours.BypassEvents = []string{"task.failed", "task.failed"}
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate event") {
		t.Fatalf("duplicate bypass event was accepted: %v", err)
	}
}

func TestTemplateValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "unknown placeholder", body: "{{environment}}", want: "unknown placeholder"},
		{name: "malformed placeholder", body: "{{event", want: "malformed placeholder"},
		{name: "empty body", body: "", want: "body is required"},
		{
			name: "too long",
			body: strings.Repeat("x", MaximumTemplateBytes+1),
			want: "exceeds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validSettings()
			value.Templates[0].Body = test.body
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("template validation: %v", err)
			}
		})
	}

	value := validSettings()
	value.Templates = append(value.Templates, value.Templates[0])
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate template") {
		t.Fatalf("duplicate template id was accepted: %v", err)
	}

	value = validSettings()
	value.DefaultTemplate = "missing"
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "default template") {
		t.Fatalf("missing default template was accepted: %v", err)
	}

	value = validSettings()
	value.Templates[0].Body = strings.Repeat("界", MaximumTemplateBytes/3)
	if err := value.Validate(); err != nil {
		t.Fatalf("UTF-8 template at byte limit must be valid: %v", err)
	}
	value.Templates[0].Body += "界"
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("UTF-8 template above byte limit was accepted: %v", err)
	}
}

func TestQuietHoursValidationAllowsCrossMidnight(t *testing.T) {
	value := validSettings()
	interval := value.QuietHours.Intervals[0]
	if interval.Start != "22:00" || interval.End != "08:00" {
		t.Fatalf("fixture is not cross-midnight: %#v", interval)
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("cross-midnight interval must be valid: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Settings)
		want   string
	}{
		{
			name: "non-IANA timezone",
			mutate: func(value *Settings) {
				value.QuietHours.Timezone = "PST"
			},
			want: "IANA timezone",
		},
		{
			name: "unknown timezone",
			mutate: func(value *Settings) {
				value.QuietHours.Timezone = "Mars/Olympus"
			},
			want: "timezone",
		},
		{
			name: "unknown day",
			mutate: func(value *Settings) {
				value.QuietHours.Intervals[0].Days = []string{"monday"}
			},
			want: "day",
		},
		{
			name: "duplicate day",
			mutate: func(value *Settings) {
				value.QuietHours.Intervals[0].Days = []string{"mon", "mon"}
			},
			want: "duplicate day",
		},
		{
			name: "invalid time",
			mutate: func(value *Settings) {
				value.QuietHours.Intervals[0].Start = "24:00"
			},
			want: "time",
		},
		{
			name: "zero interval",
			mutate: func(value *Settings) {
				value.QuietHours.Intervals[0].End = "22:00"
			},
			want: "must differ",
		},
		{
			name: "invalid action",
			mutate: func(value *Settings) {
				value.QuietHours.Action = "retry"
			},
			want: "action",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validSettings()
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("quiet-hours validation: %v", err)
			}
		})
	}
}

func TestDisabledQuietHoursStillValidateSuppliedFields(t *testing.T) {
	value := validSettings()
	value.QuietHours = QuietHours{Enabled: false}
	if err := value.Validate(); err != nil {
		t.Fatalf("disabled quiet hours with defaults must be valid: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*QuietHours)
		want   string
	}{
		{
			name: "valid optional fields",
			mutate: func(quiet *QuietHours) {
				quiet.Timezone = "UTC"
				quiet.Action = "drop"
				quiet.BypassEvents = []string{"task.failed"}
				quiet.Intervals = []QuietInterval{{
					Days: []string{"sat"}, Start: "09:00", End: "17:00",
				}}
			},
		},
		{
			name: "invalid timezone",
			mutate: func(quiet *QuietHours) {
				quiet.Timezone = "local"
			},
			want: "IANA timezone",
		},
		{
			name: "invalid action",
			mutate: func(quiet *QuietHours) {
				quiet.Action = "retry"
			},
			want: "action",
		},
		{
			name: "invalid bypass",
			mutate: func(quiet *QuietHours) {
				quiet.BypassEvents = []string{"future.event"}
			},
			want: "unknown event",
		},
		{
			name: "invalid interval",
			mutate: func(quiet *QuietHours) {
				quiet.Intervals = []QuietInterval{{
					Days: []string{"sun"}, Start: "bad", End: "08:00",
				}}
			},
			want: "start time",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validSettings()
			candidate.QuietHours = QuietHours{Enabled: false}
			test.mutate(&candidate.QuietHours)
			err := candidate.Validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("valid disabled settings failed: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPolicyValidation(t *testing.T) {
	value := validSettings()
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}

	value.Policies[1].ID = value.Policies[0].ID
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate policy") {
		t.Fatalf("duplicate policy was accepted: %v", err)
	}

	value = validSettings()
	value.Policies[0].Match.Events = []string{"task.failed", "task.failed"}
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate event") {
		t.Fatalf("duplicate policy event was accepted: %v", err)
	}

	value = validSettings()
	value.Policies[0].Action.TemplateID = "missing"
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("unknown policy template was accepted: %v", err)
	}

	value = validSettings()
	value.Policies[0].Action = PolicyAction{}
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "action") {
		t.Fatalf("empty policy action was accepted: %v", err)
	}
}

func TestPolicySelectorAndSettingsLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Settings)
		want   string
	}{
		{
			name: "invalid minimum version",
			mutate: func(value *Settings) {
				value.MinCoreVersion = "latest"
			},
			want: "semantic version",
		},
		{
			name: "no templates",
			mutate: func(value *Settings) {
				value.Templates = nil
			},
			want: "template",
		},
		{
			name: "invalid policy id",
			mutate: func(value *Settings) {
				value.Policies[0].ID = "Bad ID"
			},
			want: "policy id",
		},
		{
			name: "unknown policy event",
			mutate: func(value *Settings) {
				value.Policies[0].Match.Events = []string{"future.event"}
			},
			want: "unknown event",
		},
		{
			name: "unknown source",
			mutate: func(value *Settings) {
				value.Policies[0].Match.Sources = []string{"future"}
			},
			want: "unknown source",
		},
		{
			name: "duplicate source",
			mutate: func(value *Settings) {
				value.Policies[0].Match.Sources = []string{"codex", "codex"}
			},
			want: "duplicate source",
		},
		{
			name: "unknown surface",
			mutate: func(value *Settings) {
				value.Policies[0].Match.Surfaces = []string{"terminal"}
			},
			want: "unknown surface",
		},
		{
			name: "duplicate runtime",
			mutate: func(value *Settings) {
				value.Policies[0].Match.Runtimes = []string{"host", "host"}
			},
			want: "duplicate runtime",
		},
		{
			name: "channel id too long",
			mutate: func(value *Settings) {
				value.Policies[0].Action.ChannelIDs =
					[]string{strings.Repeat("x", maximumIdentifier+1)}
			},
			want: "channelId exceeds",
		},
		{
			name: "unicode channel id at character limit",
			mutate: func(value *Settings) {
				value.Policies[0].Action.ChannelIDs =
					[]string{strings.Repeat("群", maximumIdentifier)}
			},
		},
		{
			name: "duplicate channel id",
			mutate: func(value *Settings) {
				value.Policies[0].Action.ChannelIDs = []string{"team", "team"}
			},
			want: "duplicate channelId",
		},
		{
			name: "unknown priority",
			mutate: func(value *Settings) {
				value.Policies[0].Match.Priorities = []string{"critical"}
			},
			want: "unknown priority",
		},
		{
			name: "empty project",
			mutate: func(value *Settings) {
				value.Policies[0].Match.Projects = []string{""}
			},
			want: "project",
		},
		{
			name: "nil policies are not an array",
			mutate: func(value *Settings) {
				value.Policies = nil
			},
			want: "policies must be an array",
		},
		{
			name: "too many policies",
			mutate: func(value *Settings) {
				value.Policies = make([]Policy, maximumPolicies+1)
			},
			want: "policies exceed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validSettings()
			test.mutate(&value)
			err := value.Validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("Validate failed: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSavePermissionsRoundTripAndAtomicFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "settings.json")
	value := validSettings()
	if err := Save(path, &value); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.MinCoreVersion != value.MinCoreVersion {
		t.Fatalf("round-trip: %#v err=%v", loaded, err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("settings mode = %o, want 0600", info.Mode().Perm())
		}
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if parent.Mode().Perm() != 0o700 {
			t.Fatalf("settings parent mode = %o, want 0700", parent.Mode().Perm())
		}
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	invalid := validSettings()
	invalid.Version = 99
	if err := Save(path, &invalid); err == nil {
		t.Fatal("invalid settings save succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed save changed the existing settings")
	}

	updated := validSettings()
	updated.Events["task.completed"] = false
	if err := Save(path, &updated); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil || reloaded.Events["task.completed"] {
		t.Fatalf("atomic overwrite did not publish the update: %#v err=%v", reloaded, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temporary file survived save: %s", entry.Name())
		}
	}
}

func TestSaveRejectsNilAndOversizedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(path, nil); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("nil settings Save error = %v", err)
	}

	value := validSettings()
	for index := 0; index < 300; index++ {
		value.Templates = append(value.Templates, Template{
			ID:   "large" + strings.Repeat("x", index%50) + string(rune('a'+index%26)),
			Body: strings.Repeat("x", MaximumTemplateBytes),
		})
	}
	if err := Save(path, &value); err == nil || !strings.Contains(err.Error(), "settings exceed") {
		t.Fatalf("oversized settings Save error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized Save created a file: %v", err)
	}
}
