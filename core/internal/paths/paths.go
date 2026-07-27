package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Paths struct {
	ConfigFile string `json:"configFile"`
	DataDir    string `json:"dataDir"`
	StateDir   string `json:"stateDir"`
	LogDir     string `json:"logDir"`
}

func Resolve() (Paths, error) {
	return resolve(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

func resolve(
	goos string,
	getenv func(string) string,
	userHomeDir func() (string, error),
) (Paths, error) {
	if getenv == nil {
		return Paths{}, errors.New("environment lookup is required")
	}
	if userHomeDir == nil {
		return Paths{}, errors.New("user home lookup is required")
	}
	home, err := userHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user home: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return Paths{}, errors.New("resolve user home: home directory is empty")
	}
	return ResolveFor(goos, home, getenv), nil
}

func ResolveFor(goos, home string, getenv func(string) string) Paths {
	var result Paths

	switch goos {
	case "windows":
		appData := firstNonEmpty(getenv("APPDATA"), filepath.Join(home, "AppData", "Roaming"))
		localAppData := firstNonEmpty(getenv("LOCALAPPDATA"), filepath.Join(home, "AppData", "Local"))
		result.ConfigFile = filepath.Join(appData, "AgentBell", "config.json")
		result.DataDir = filepath.Join(localAppData, "AgentBell")
		result.StateDir = filepath.Join(localAppData, "AgentBell", "state")
		result.LogDir = filepath.Join(localAppData, "AgentBell", "logs")
	case "darwin":
		appSupport := filepath.Join(home, "Library", "Application Support", "AgentBell")
		result.ConfigFile = filepath.Join(appSupport, "config.json")
		result.DataDir = appSupport
		result.StateDir = filepath.Join(appSupport, "state")
		result.LogDir = filepath.Join(home, "Library", "Logs", "AgentBell")
	default:
		configHome := firstNonEmpty(getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
		dataHome := firstNonEmpty(getenv("XDG_DATA_HOME"), filepath.Join(home, ".local", "share"))
		stateHome := firstNonEmpty(getenv("XDG_STATE_HOME"), filepath.Join(home, ".local", "state"))
		result.ConfigFile = filepath.Join(configHome, "agentbell", "config.json")
		result.DataDir = filepath.Join(dataHome, "agentbell")
		result.StateDir = filepath.Join(stateHome, "agentbell")
		result.LogDir = filepath.Join(stateHome, "agentbell", "logs")
	}

	if override := getenv("AGENTBELL_CONFIG"); override != "" {
		result.ConfigFile = override
	}
	if override := getenv("AGENTBELL_DATA_DIR"); override != "" {
		result.DataDir = override
	}
	if override := getenv("AGENTBELL_STATE_DIR"); override != "" {
		result.StateDir = override
	}
	if override := getenv("AGENTBELL_LOG_DIR"); override != "" {
		result.LogDir = override
	}

	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
