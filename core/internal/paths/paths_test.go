package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOverrides(t *testing.T) {
	values := map[string]string{
		"AGENTBELL_CONFIG":    "custom-config.json",
		"AGENTBELL_STATE_DIR": "custom-state",
		"AGENTBELL_LOG_DIR":   "custom-logs",
	}
	result := ResolveFor("linux", "/home/test", func(key string) string {
		return values[key]
	})
	if result.ConfigFile != "custom-config.json" ||
		result.StateDir != "custom-state" ||
		result.LogDir != "custom-logs" {
		t.Fatalf("overrides not applied: %#v", result)
	}
}

func TestPlatformDefaults(t *testing.T) {
	tests := []struct {
		goos       string
		configPart string
		statePart  string
	}{
		{"windows", filepath.Join("AgentBell", "config.json"), filepath.Join("AgentBell", "state")},
		{"darwin", filepath.Join("AgentBell", "config.json"), filepath.Join("AgentBell", "state")},
		{"linux", filepath.Join("agentbell", "config.json"), "agentbell"},
	}

	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			result := ResolveFor(test.goos, filepath.Join("root", "home"), func(string) string { return "" })
			if !strings.Contains(result.ConfigFile, test.configPart) ||
				!strings.Contains(result.StateDir, test.statePart) {
				t.Fatalf("unexpected paths: %#v", result)
			}
		})
	}
}

func TestResolveCurrentPlatform(t *testing.T) {
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("AGENTBELL_STATE_DIR", t.TempDir())
	t.Setenv("AGENTBELL_LOG_DIR", t.TempDir())
	result, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if result.ConfigFile == "" || result.StateDir == "" || result.LogDir == "" {
		t.Fatalf("incomplete paths: %#v", result)
	}
}
