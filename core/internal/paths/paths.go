package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

type Paths struct {
	ConfigFile string `json:"configFile"`
	StateDir   string `json:"stateDir"`
	LogDir     string `json:"logDir"`
}

func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return ResolveFor(runtime.GOOS, home, os.Getenv), nil
}

func ResolveFor(goos, home string, getenv func(string) string) Paths {
	var result Paths

	switch goos {
	case "windows":
		appData := firstNonEmpty(getenv("APPDATA"), filepath.Join(home, "AppData", "Roaming"))
		localAppData := firstNonEmpty(getenv("LOCALAPPDATA"), filepath.Join(home, "AppData", "Local"))
		result.ConfigFile = filepath.Join(appData, "AgentBell", "config.json")
		result.StateDir = filepath.Join(localAppData, "AgentBell", "state")
		result.LogDir = filepath.Join(localAppData, "AgentBell", "logs")
	case "darwin":
		appSupport := filepath.Join(home, "Library", "Application Support", "AgentBell")
		result.ConfigFile = filepath.Join(appSupport, "config.json")
		result.StateDir = filepath.Join(appSupport, "state")
		result.LogDir = filepath.Join(home, "Library", "Logs", "AgentBell")
	default:
		configHome := firstNonEmpty(getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
		stateHome := firstNonEmpty(getenv("XDG_STATE_HOME"), filepath.Join(home, ".local", "state"))
		result.ConfigFile = filepath.Join(configHome, "agentbell", "config.json")
		result.StateDir = filepath.Join(stateHome, "agentbell")
		result.LogDir = filepath.Join(stateHome, "agentbell", "logs")
	}

	if override := getenv("AGENTBELL_CONFIG"); override != "" {
		result.ConfigFile = override
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
