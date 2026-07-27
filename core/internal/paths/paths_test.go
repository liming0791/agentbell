package paths

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverrides(t *testing.T) {
	values := map[string]string{
		"AGENTBELL_CONFIG":    "custom-config.json",
		"AGENTBELL_DATA_DIR":  "custom-data",
		"AGENTBELL_STATE_DIR": "custom-state",
		"AGENTBELL_LOG_DIR":   "custom-logs",
	}
	result := ResolveFor("linux", "/home/test", func(key string) string {
		return values[key]
	})
	if result.ConfigFile != "custom-config.json" ||
		result.DataDir != "custom-data" ||
		result.StateDir != "custom-state" ||
		result.LogDir != "custom-logs" {
		t.Fatalf("overrides not applied: %#v", result)
	}
}

func TestDataDirPlatformDefaults(t *testing.T) {
	tests := []struct {
		goos string
		home string
		want string
	}{
		{
			"windows",
			filepath.Join("C:", "Users", "test"),
			filepath.Join("C:", "Users", "test", "AppData", "Local", "AgentBell"),
		},
		{
			"darwin",
			filepath.Join("", "Users", "test"),
			filepath.Join("", "Users", "test", "Library", "Application Support", "AgentBell"),
		},
		{
			"linux",
			filepath.Join("", "home", "test"),
			filepath.Join("", "home", "test", ".local", "share", "agentbell"),
		},
	}

	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			result := ResolveFor(test.goos, test.home, func(string) string { return "" })
			if result.DataDir != test.want {
				t.Fatalf("DataDir = %q, want %q", result.DataDir, test.want)
			}
		})
	}
}

func TestDataDirUsesPlatformEnvironment(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		key    string
		value  string
		suffix string
	}{
		{
			"windows local app data",
			"windows",
			"LOCALAPPDATA",
			filepath.Join("D:", "Profiles", "Local"),
			"AgentBell",
		},
		{
			"linux XDG data home",
			"linux",
			"XDG_DATA_HOME",
			filepath.Join("", "mnt", "data"),
			"agentbell",
		},
		{
			"other unix XDG data home",
			"freebsd",
			"XDG_DATA_HOME",
			filepath.Join("", "var", "user-data"),
			"agentbell",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ResolveFor(test.goos, filepath.Join("", "home", "test"), func(key string) string {
				if key == test.key {
					return test.value
				}
				return ""
			})
			want := filepath.Join(test.value, test.suffix)
			if result.DataDir != want {
				t.Fatalf("DataDir = %q, want %q", result.DataDir, want)
			}
		})
	}
}

func TestExistingPathDefaultsRemainCompatible(t *testing.T) {
	tests := []struct {
		goos       string
		configPart string
		statePart  string
		logPart    string
	}{
		{
			"windows",
			filepath.Join("AgentBell", "config.json"),
			filepath.Join("AgentBell", "state"),
			filepath.Join("AgentBell", "logs"),
		},
		{
			"darwin",
			filepath.Join("AgentBell", "config.json"),
			filepath.Join("AgentBell", "state"),
			filepath.Join("Logs", "AgentBell"),
		},
		{
			"linux",
			filepath.Join("agentbell", "config.json"),
			"agentbell",
			filepath.Join("agentbell", "logs"),
		},
	}

	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			result := ResolveFor(test.goos, filepath.Join("root", "home"), func(string) string { return "" })
			if !strings.Contains(result.ConfigFile, test.configPart) ||
				!strings.Contains(result.StateDir, test.statePart) ||
				!strings.Contains(result.LogDir, test.logPart) {
				t.Fatalf("unexpected paths: %#v", result)
			}
		})
	}
}

func TestResolveReturnsHomeLookupError(t *testing.T) {
	homeErr := errors.New("home unavailable")
	result, err := resolve("linux", func(string) string { return "" }, func() (string, error) {
		return "", homeErr
	})
	if !errors.Is(err, homeErr) {
		t.Fatalf("error = %v", err)
	}
	if result != (Paths{}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestResolveRejectsMissingHome(t *testing.T) {
	result, err := resolve("linux", func(string) string { return "" }, func() (string, error) {
		return "", nil
	})
	if err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("error = %v", err)
	}
	if result != (Paths{}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestResolveRejectsMissingDependencies(t *testing.T) {
	if _, err := resolve("linux", nil, func() (string, error) {
		return "/home/test", nil
	}); err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("missing environment lookup error = %v", err)
	}
	if _, err := resolve("linux", func(string) string {
		return ""
	}, nil); err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("missing home lookup error = %v", err)
	}
}

func TestResolveCurrentPlatform(t *testing.T) {
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("AGENTBELL_DATA_DIR", t.TempDir())
	t.Setenv("AGENTBELL_STATE_DIR", t.TempDir())
	t.Setenv("AGENTBELL_LOG_DIR", t.TempDir())
	result, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if result.ConfigFile == "" ||
		result.DataDir == "" ||
		result.StateDir == "" ||
		result.LogDir == "" {
		t.Fatalf("incomplete paths: %#v", result)
	}
}
