package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	launchAgentLabel = "com.agentbell.service"
	systemdUnitName  = "agentbell.service"
	xdgAutostartName = "com.agentbell.service.desktop"
	windowsTaskName  = `\AgentBell\AgentBell`
	backendLaunchd   = "launchd"
	backendSystemd   = "systemd-user"
	backendXDG       = "xdg-autostart"
	backendTask      = "windows-task-scheduler"
)

var ErrRestartUnsupported = errors.New(
	"service backend does not support a safe verified restart",
)

type ServiceMode string

const (
	ServiceModeLegacy ServiceMode = "legacy"
	ServiceModeBridge ServiceMode = "bridge"
)

type ManagerRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type managerExecRunner struct{}

func (managerExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if len(output) > 4096 {
		output = output[:4096]
	}
	if err != nil {
		return output, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

type Manager struct {
	GOOS             string
	Executable       string
	BridgeExecutable string
	ServiceMode      ServiceMode
	HomeDir          string
	ConfigDir        string
	LogDir           string
	StateDir         string
	LarkCLIPath      string
	UID              string
	Runner           ManagerRunner
	LookPath         func(string) (string, error)
}

type ManagerResult struct {
	Label          string `json:"label"`
	Platform       string `json:"platform"`
	Backend        string `json:"backend"`
	Installed      bool   `json:"installed"`
	Loaded         bool   `json:"loaded"`
	Running        bool   `json:"running"`
	Changed        bool   `json:"changed"`
	DefinitionPath string `json:"definitionPath,omitempty"`
	PlistPath      string `json:"plistPath,omitempty"`
	StdoutPath     string `json:"stdoutPath,omitempty"`
	StderrPath     string `json:"stderrPath,omitempty"`
	Message        string `json:"message,omitempty"`
}

func NewManager(larkCLIPath, logDir string) (*Manager, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	current, err := user.Current()
	if err != nil {
		return nil, err
	}
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		configDir = filepath.Join(home, ".config")
	}
	return &Manager{
		GOOS:        runtime.GOOS,
		Executable:  executable,
		ServiceMode: ServiceModeLegacy,
		HomeDir:     home,
		ConfigDir:   configDir,
		LogDir:      logDir,
		LarkCLIPath: larkCLIPath,
		UID:         current.Uid,
		Runner:      managerExecRunner{},
		LookPath:    exec.LookPath,
	}, nil
}

func (manager *Manager) Install(ctx context.Context, dryRun bool) (ManagerResult, error) {
	if err := manager.validate(); err != nil {
		return manager.emptyResult(), err
	}
	switch manager.GOOS {
	case "darwin":
		return manager.installLaunchAgent(ctx, dryRun)
	case "linux":
		return manager.installLinux(ctx, dryRun)
	case "windows":
		return manager.installWindowsTask(ctx, dryRun)
	default:
		return manager.emptyResult(), fmt.Errorf(
			"service management is not implemented for %s",
			manager.GOOS,
		)
	}
}

func (manager *Manager) Status(ctx context.Context) (ManagerResult, error) {
	if err := manager.validate(); err != nil {
		return manager.emptyResult(), err
	}
	switch manager.GOOS {
	case "darwin":
		return manager.statusLaunchAgent(ctx)
	case "linux":
		return manager.statusLinux(ctx)
	case "windows":
		return manager.statusWindowsTask(ctx)
	default:
		return manager.emptyResult(), fmt.Errorf(
			"service management is not implemented for %s",
			manager.GOOS,
		)
	}
}

func (manager *Manager) Uninstall(ctx context.Context, dryRun bool) (ManagerResult, error) {
	if err := manager.validate(); err != nil {
		return manager.emptyResult(), err
	}
	switch manager.GOOS {
	case "darwin":
		return manager.uninstallLaunchAgent(ctx, dryRun)
	case "linux":
		return manager.uninstallLinux(ctx, dryRun)
	case "windows":
		return manager.uninstallWindowsTask(ctx, dryRun)
	default:
		return manager.emptyResult(), fmt.Errorf(
			"service management is not implemented for %s",
			manager.GOOS,
		)
	}
}

func (manager *Manager) Restart(ctx context.Context) (ManagerResult, error) {
	if err := manager.validate(); err != nil {
		return manager.emptyResult(), err
	}
	switch manager.GOOS {
	case "darwin":
		return manager.restartLaunchAgent(ctx)
	case "linux":
		return manager.restartLinux(ctx)
	case "windows":
		return manager.restartWindowsTask(ctx)
	default:
		return manager.emptyResult(), fmt.Errorf(
			"service management is not implemented for %s",
			manager.GOOS,
		)
	}
}

func (manager *Manager) installLaunchAgent(
	ctx context.Context,
	dryRun bool,
) (ManagerResult, error) {
	result := manager.launchdResult()
	plist, err := manager.plist()
	if err != nil {
		return result, err
	}
	existing, readErr := os.ReadFile(result.PlistPath)
	switch {
	case readErr == nil:
		result.Installed = true
		result.Changed = !bytes.Equal(existing, plist)
	case errors.Is(readErr, os.ErrNotExist):
		result.Changed = true
	default:
		return result, readErr
	}
	if dryRun {
		result.Message = "LaunchAgent would be installed and started"
		return result, nil
	}
	if err := manager.ensureDefinitionDirectories(result); err != nil {
		return result, err
	}
	if result.Changed {
		if err := writeManagerFileAtomic(result.PlistPath, plist, 0o600); err != nil {
			return result, err
		}
	}

	domain := "gui/" + manager.UID
	target := domain + "/" + launchAgentLabel
	_, _ = manager.runner().Run(ctx, "launchctl", "bootout", target)
	if _, err := manager.runner().Run(
		ctx,
		"launchctl",
		"bootstrap",
		domain,
		result.PlistPath,
	); err != nil {
		return result, fmt.Errorf("load AgentBell LaunchAgent: %w", err)
	}
	if _, err := manager.runner().Run(ctx, "launchctl", "kickstart", "-k", target); err != nil {
		return result, fmt.Errorf("start AgentBell LaunchAgent: %w", err)
	}
	result.Installed = true
	result.Loaded = true
	result.Running = true
	result.Message = "AgentBell LaunchAgent is installed and started"
	return result, nil
}

func (manager *Manager) statusLaunchAgent(ctx context.Context) (ManagerResult, error) {
	result := manager.launchdResult()
	if _, err := os.Stat(result.PlistPath); err == nil {
		result.Installed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	target := "gui/" + manager.UID + "/" + launchAgentLabel
	if _, err := manager.runner().Run(ctx, "launchctl", "print", target); err == nil {
		result.Loaded = true
		result.Running = true
	}
	switch {
	case result.Running:
		result.Message = "AgentBell LaunchAgent is loaded"
	case result.Installed:
		result.Message = "AgentBell LaunchAgent is installed but not loaded"
	default:
		result.Message = "AgentBell LaunchAgent is not installed"
	}
	return result, nil
}

func (manager *Manager) uninstallLaunchAgent(
	ctx context.Context,
	dryRun bool,
) (ManagerResult, error) {
	result := manager.launchdResult()
	if _, err := os.Stat(result.PlistPath); err == nil {
		result.Installed = true
		result.Changed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if dryRun {
		if result.Changed {
			result.Message = "AgentBell LaunchAgent would be unloaded and removed"
		} else {
			result.Message = "AgentBell LaunchAgent is not installed"
		}
		return result, nil
	}
	target := "gui/" + manager.UID + "/" + launchAgentLabel
	_, _ = manager.runner().Run(ctx, "launchctl", "bootout", target)
	if err := os.Remove(result.PlistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	result.Installed = false
	result.Loaded = false
	result.Running = false
	result.Message = "AgentBell LaunchAgent is uninstalled"
	return result, nil
}

func (manager *Manager) installLinux(
	ctx context.Context,
	dryRun bool,
) (ManagerResult, error) {
	backend := manager.linuxBackend(ctx, true)
	result := manager.linuxResult(backend)
	var content []byte
	var err error
	if backend == backendSystemd {
		content, err = manager.systemdUnit()
	} else {
		content, err = manager.xdgDesktopEntry()
	}
	if err != nil {
		return result, err
	}
	existing, readErr := os.ReadFile(result.DefinitionPath)
	switch {
	case readErr == nil:
		result.Installed = true
		result.Changed = !bytes.Equal(existing, content)
	case errors.Is(readErr, os.ErrNotExist):
		result.Changed = true
	default:
		return result, readErr
	}
	if dryRun {
		result.Message = "Linux user service would be installed and enabled"
		return result, nil
	}
	if err := manager.ensureDefinitionDirectories(result); err != nil {
		return result, err
	}
	if result.Changed {
		if err := writeManagerFileAtomic(result.DefinitionPath, content, 0o600); err != nil {
			return result, err
		}
	}
	result.Installed = true
	if backend == backendSystemd {
		if _, err := manager.runner().Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return result, fmt.Errorf("reload systemd user manager: %w", err)
		}
		if _, err := manager.runner().Run(
			ctx,
			"systemctl",
			"--user",
			"enable",
			"--now",
			systemdUnitName,
		); err != nil {
			return result, fmt.Errorf("enable AgentBell systemd user service: %w", err)
		}
		result.Loaded = true
		result.Running = true
		result.Message = "AgentBell systemd user service is installed, enabled and started"
	} else {
		result.Loaded = true
		result.Message = "AgentBell XDG autostart entry is installed; it starts at the next desktop login"
	}
	return result, nil
}

func (manager *Manager) statusLinux(ctx context.Context) (ManagerResult, error) {
	backend := manager.linuxBackend(ctx, true)
	result := manager.linuxResult(backend)
	if _, err := os.Stat(result.DefinitionPath); err == nil {
		result.Installed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if backend == backendSystemd && result.Installed {
		if _, err := manager.runner().Run(
			ctx,
			"systemctl",
			"--user",
			"is-enabled",
			systemdUnitName,
		); err == nil {
			result.Loaded = true
		}
		if _, err := manager.runner().Run(
			ctx,
			"systemctl",
			"--user",
			"is-active",
			systemdUnitName,
		); err == nil {
			result.Running = true
		}
	} else if backend == backendXDG && result.Installed {
		result.Loaded = true
	}
	switch {
	case result.Running:
		result.Message = "AgentBell systemd user service is active"
	case result.Loaded:
		result.Message = "AgentBell login service is installed and enabled"
	case result.Installed:
		result.Message = "AgentBell login service is installed but not enabled"
	default:
		result.Message = "AgentBell Linux login service is not installed"
	}
	return result, nil
}

func (manager *Manager) uninstallLinux(
	ctx context.Context,
	dryRun bool,
) (ManagerResult, error) {
	unitPath := filepath.Join(manager.ConfigDir, "systemd", "user", systemdUnitName)
	autostartPath := filepath.Join(manager.ConfigDir, "autostart", xdgAutostartName)
	unitExists, err := managerPathExists(unitPath)
	if err != nil {
		return manager.emptyResult(), err
	}
	autostartExists, err := managerPathExists(autostartPath)
	if err != nil {
		return manager.emptyResult(), err
	}
	backend := manager.linuxBackend(ctx, true)
	if unitExists {
		backend = backendSystemd
	} else if autostartExists {
		backend = backendXDG
	}
	result := manager.linuxResult(backend)
	if unitExists || autostartExists {
		result.Installed = true
		result.Changed = true
	}
	if dryRun {
		if result.Changed {
			result.Message = "AgentBell Linux login service would be disabled and removed"
		} else {
			result.Message = "AgentBell Linux login service is not installed"
		}
		return result, nil
	}
	if unitExists {
		_, _ = manager.runner().Run(
			ctx,
			"systemctl",
			"--user",
			"disable",
			"--now",
			systemdUnitName,
		)
	}
	for _, path := range []string{unitPath, autostartPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
	}
	if unitExists {
		_, _ = manager.runner().Run(ctx, "systemctl", "--user", "daemon-reload")
		_, _ = manager.runner().Run(ctx, "systemctl", "--user", "reset-failed")
	}
	result.Installed = false
	result.Loaded = false
	result.Running = false
	result.Message = "AgentBell Linux login service is uninstalled"
	return result, nil
}

func managerPathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (manager *Manager) installWindowsTask(
	ctx context.Context,
	dryRun bool,
) (ManagerResult, error) {
	result := manager.windowsResult()
	if _, err := manager.runner().Run(
		ctx,
		"schtasks.exe",
		"/Query",
		"/TN",
		windowsTaskName,
	); err == nil {
		result.Installed = true
	}
	result.Changed = !result.Installed
	if dryRun {
		result.Message = "Windows logon task would be registered and started"
		return result, nil
	}
	action, err := manager.windowsTaskAction()
	if err != nil {
		return result, err
	}
	if _, err := manager.runner().Run(
		ctx,
		"schtasks.exe",
		"/Create",
		"/TN",
		windowsTaskName,
		"/SC",
		"ONLOGON",
		"/TR",
		action,
		"/RL",
		"LIMITED",
		"/F",
	); err != nil {
		return result, fmt.Errorf("register AgentBell logon task: %w", err)
	}
	if _, err := manager.runner().Run(
		ctx,
		"schtasks.exe",
		"/Run",
		"/TN",
		windowsTaskName,
	); err != nil {
		return result, fmt.Errorf("start AgentBell logon task: %w", err)
	}
	state, err := manager.waitWindowsTaskRunning(ctx)
	if err != nil {
		return result, fmt.Errorf("verify AgentBell Windows task start: %w", err)
	}
	if !strings.EqualFold(state, "Running") {
		return result, fmt.Errorf(
			"verify AgentBell Windows task start: state is %q, not Running",
			state,
		)
	}
	result.Installed = true
	result.Loaded = true
	result.Running = true
	result.Message = "AgentBell Windows logon task is registered and started"
	return result, nil
}

func (manager *Manager) statusWindowsTask(ctx context.Context) (ManagerResult, error) {
	result := manager.windowsResult()
	if _, err := manager.runner().Run(
		ctx,
		"schtasks.exe",
		"/Query",
		"/TN",
		windowsTaskName,
	); err == nil {
		result.Installed = true
		result.Loaded = true
		output, stateErr := manager.runner().Run(
			ctx,
			"powershell.exe",
			windowsTaskStateArgs()...,
		)
		if stateErr != nil {
			return result, fmt.Errorf("query AgentBell Windows task state: %w", stateErr)
		}
		result.Running = strings.EqualFold(strings.TrimSpace(string(output)), "Running")
		if result.Running {
			result.Message = "AgentBell Windows logon task is registered and running"
		} else {
			result.Message = fmt.Sprintf(
				"AgentBell Windows logon task is registered but %s",
				strings.ToLower(strings.TrimSpace(string(output))),
			)
		}
	} else {
		result.Message = "AgentBell Windows logon task is not registered"
	}
	return result, nil
}

func (manager *Manager) uninstallWindowsTask(
	ctx context.Context,
	dryRun bool,
) (ManagerResult, error) {
	result := manager.windowsResult()
	if _, err := manager.runner().Run(
		ctx,
		"schtasks.exe",
		"/Query",
		"/TN",
		windowsTaskName,
	); err == nil {
		result.Installed = true
		result.Changed = true
	}
	if dryRun {
		if result.Changed {
			result.Message = "AgentBell Windows logon task would be stopped and removed"
		} else {
			result.Message = "AgentBell Windows logon task is not registered"
		}
		return result, nil
	}
	_, _ = manager.runner().Run(ctx, "schtasks.exe", "/End", "/TN", windowsTaskName)
	if result.Installed {
		if _, err := manager.runner().Run(
			ctx,
			"schtasks.exe",
			"/Delete",
			"/TN",
			windowsTaskName,
			"/F",
		); err != nil {
			return result, fmt.Errorf("delete AgentBell logon task: %w", err)
		}
	}
	result.Installed = false
	result.Loaded = false
	result.Running = false
	result.Message = "AgentBell Windows logon task is uninstalled"
	return result, nil
}

func (manager *Manager) validate() error {
	if manager.GOOS != "darwin" && manager.GOOS != "linux" && manager.GOOS != "windows" {
		return fmt.Errorf("service management is not implemented for %s", manager.GOOS)
	}
	if manager.HomeDir == "" || manager.LogDir == "" {
		return errors.New("service manager paths are incomplete")
	}
	if manager.GOOS == "linux" && manager.ConfigDir == "" {
		return errors.New("Linux service manager config path is incomplete")
	}
	if manager.GOOS == "darwin" && manager.UID == "" {
		return errors.New("LaunchAgent user id is incomplete")
	}
	command, _, err := manager.serviceCommand()
	if err != nil {
		return err
	}
	if !absoluteFor(manager.GOOS, command) {
		if manager.effectiveServiceMode() == ServiceModeBridge {
			return errors.New("AgentBell bridge path must be absolute")
		}
		return errors.New("service manager paths must be absolute")
	}
	for _, value := range []string{manager.HomeDir, manager.LogDir} {
		if !absoluteFor(manager.GOOS, value) {
			return errors.New("service manager paths must be absolute")
		}
	}
	if manager.GOOS == "linux" && !absoluteFor(manager.GOOS, manager.ConfigDir) {
		return errors.New("Linux service manager config path must be absolute")
	}
	if manager.LarkCLIPath != "" && !absoluteFor(manager.GOOS, manager.LarkCLIPath) {
		return errors.New("lark-cli path must be absolute")
	}
	if strings.ContainsAny(command, "\x00\n\r\"") {
		if manager.effectiveServiceMode() == ServiceModeBridge {
			return errors.New("AgentBell bridge path contains unsupported characters")
		}
		return errors.New("AgentBell executable path contains unsupported characters")
	}
	return nil
}

func (manager *Manager) effectiveServiceMode() ServiceMode {
	if manager.ServiceMode == "" {
		return ServiceModeLegacy
	}
	return manager.ServiceMode
}

func (manager *Manager) serviceCommand() (string, []string, error) {
	switch manager.effectiveServiceMode() {
	case ServiceModeLegacy:
		if manager.Executable == "" {
			return "", nil, errors.New("AgentBell executable path is required")
		}
		return manager.Executable, []string{"service", "run", "--foreground"}, nil
	case ServiceModeBridge:
		if manager.BridgeExecutable == "" {
			return "", nil, errors.New("AgentBell bridge path is required")
		}
		return manager.BridgeExecutable, []string{"service-v1"}, nil
	default:
		return "", nil, fmt.Errorf(
			"unsupported AgentBell service mode %q",
			manager.ServiceMode,
		)
	}
}

func (manager *Manager) emptyResult() ManagerResult {
	return ManagerResult{Platform: manager.GOOS}
}

func (manager *Manager) launchdResult() ManagerResult {
	path := filepath.Join(
		manager.HomeDir,
		"Library",
		"LaunchAgents",
		launchAgentLabel+".plist",
	)
	return ManagerResult{
		Label:          launchAgentLabel,
		Platform:       manager.GOOS,
		Backend:        backendLaunchd,
		DefinitionPath: path,
		PlistPath:      path,
		StdoutPath:     filepath.Join(manager.LogDir, "service.stdout.log"),
		StderrPath:     filepath.Join(manager.LogDir, "service.stderr.log"),
	}
}

func (manager *Manager) linuxResult(backend string) ManagerResult {
	path := filepath.Join(manager.ConfigDir, "systemd", "user", systemdUnitName)
	label := systemdUnitName
	if backend == backendXDG {
		path = filepath.Join(manager.ConfigDir, "autostart", xdgAutostartName)
		label = xdgAutostartName
	}
	return ManagerResult{
		Label:          label,
		Platform:       manager.GOOS,
		Backend:        backend,
		DefinitionPath: path,
		StdoutPath:     filepath.Join(manager.LogDir, "service.stdout.log"),
		StderrPath:     filepath.Join(manager.LogDir, "service.stderr.log"),
	}
}

func (manager *Manager) windowsResult() ManagerResult {
	return ManagerResult{
		Label:    windowsTaskName,
		Platform: manager.GOOS,
		Backend:  backendTask,
	}
}

func (manager *Manager) linuxBackend(ctx context.Context, probe bool) string {
	unitPath := filepath.Join(manager.ConfigDir, "systemd", "user", systemdUnitName)
	if _, err := os.Stat(unitPath); err == nil {
		return backendSystemd
	}
	autostartPath := filepath.Join(manager.ConfigDir, "autostart", xdgAutostartName)
	if _, err := os.Stat(autostartPath); err == nil {
		return backendXDG
	}
	if _, err := manager.lookPath()("systemctl"); err != nil {
		return backendXDG
	}
	if probe {
		if _, err := manager.runner().Run(ctx, "systemctl", "--user", "show-environment"); err != nil {
			return backendXDG
		}
	}
	return backendSystemd
}

func (manager *Manager) ensureDefinitionDirectories(result ManagerResult) error {
	if result.DefinitionPath != "" {
		if err := os.MkdirAll(filepath.Dir(result.DefinitionPath), 0o700); err != nil {
			return err
		}
	}
	if result.StdoutPath != "" || result.StderrPath != "" {
		if err := os.MkdirAll(manager.LogDir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) plist() ([]byte, error) {
	result := manager.launchdResult()
	command, arguments, err := manager.serviceCommand()
	if err != nil {
		return nil, err
	}
	pathValue := manager.pathValue([]string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	})
	values := []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`,
		`<plist version="1.0"><dict>`,
		`<key>Label</key><string>` + xmlText(launchAgentLabel) + `</string>`,
		`<key>ProgramArguments</key><array>`,
		`<string>` + xmlText(command) + `</string>`,
	}
	for _, argument := range arguments {
		values = append(values, `<string>`+xmlText(argument)+`</string>`)
	}
	values = append(values,
		`</array>`,
		`<key>EnvironmentVariables</key><dict>`,
		`<key>HOME</key><string>`+xmlText(manager.HomeDir)+`</string>`,
		`<key>PATH</key><string>`+xmlText(pathValue)+`</string>`,
		`</dict>`,
		`<key>RunAtLoad</key><true/>`,
		`<key>KeepAlive</key><true/>`,
		`<key>ProcessType</key><string>Background</string>`,
		`<key>ThrottleInterval</key><integer>5</integer>`,
		`<key>StandardOutPath</key><string>`+xmlText(result.StdoutPath)+`</string>`,
		`<key>StandardErrorPath</key><string>`+xmlText(result.StderrPath)+`</string>`,
		`</dict></plist>`,
	)
	return []byte(strings.Join(values, "\n") + "\n"), nil
}

func (manager *Manager) systemdUnit() ([]byte, error) {
	result := manager.linuxResult(backendSystemd)
	command, arguments, err := manager.serviceCommand()
	if err != nil {
		return nil, err
	}
	pathValue := manager.pathValue([]string{
		filepath.Join(manager.HomeDir, ".local", "bin"),
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/snap/bin",
	})
	values := []string{
		"[Unit]",
		"Description=AgentBell notification delivery service",
		"",
		"[Service]",
		"Type=simple",
		"ExecStart=" + systemdCommand(command, arguments),
		"Environment=" + systemdQuote("PATH="+pathValue),
		"Restart=always",
		"RestartSec=5",
		"StandardOutput=" + systemdQuote("append:"+result.StdoutPath),
		"StandardError=" + systemdQuote("append:"+result.StderrPath),
		"",
		"[Install]",
		"WantedBy=default.target",
	}
	return []byte(strings.Join(values, "\n") + "\n"), nil
}

func (manager *Manager) xdgDesktopEntry() ([]byte, error) {
	command, arguments, err := manager.serviceCommand()
	if err != nil {
		return nil, err
	}
	values := []string{
		"[Desktop Entry]",
		"Type=Application",
		"Version=1.0",
		"Name=AgentBell",
		"Comment=AgentBell notification delivery service",
		"Exec=" + desktopCommand(command, arguments),
		"Terminal=false",
		"X-GNOME-Autostart-enabled=true",
	}
	return []byte(strings.Join(values, "\n") + "\n"), nil
}

func (manager *Manager) windowsTaskAction() (string, error) {
	command, arguments, err := manager.serviceCommand()
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(command, "\x00\n\r\"") {
		if manager.effectiveServiceMode() == ServiceModeBridge {
			return "", errors.New("AgentBell bridge path contains unsupported characters")
		}
		return "", errors.New("AgentBell executable path contains unsupported characters")
	}
	return `"` + command + `" ` + strings.Join(arguments, " "), nil
}

func (manager *Manager) pathValue(defaults []string) string {
	pathEntries := make([]string, 0, len(defaults)+1)
	if manager.LarkCLIPath != "" {
		pathEntries = append(pathEntries, dirFor(manager.GOOS, manager.LarkCLIPath))
	}
	pathEntries = append(pathEntries, defaults...)
	return strings.Join(uniqueStrings(pathEntries), string(os.PathListSeparator))
}

func (manager *Manager) runner() ManagerRunner {
	if manager.Runner != nil {
		return manager.Runner
	}
	return managerExecRunner{}
}

func (manager *Manager) lookPath() func(string) (string, error) {
	if manager.LookPath != nil {
		return manager.LookPath
	}
	return exec.LookPath
}

func absoluteFor(goos, value string) bool {
	if goos == "windows" {
		if strings.HasPrefix(value, `\\`) {
			return true
		}
		return len(value) >= 3 &&
			((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
			value[1] == ':' &&
			(value[2] == '\\' || value[2] == '/')
	}
	return filepath.IsAbs(value)
}

func dirFor(goos, value string) string {
	if goos != "windows" {
		return filepath.Dir(value)
	}
	index := strings.LastIndexAny(value, `\/`)
	if index < 0 {
		return "."
	}
	return value[:index]
}

func systemdQuote(value string) string {
	return strconv.Quote(strings.ReplaceAll(value, "%", "%%"))
}

func systemdCommand(command string, arguments []string) string {
	return systemdQuote(command) + " " + strings.Join(arguments, " ")
}

func desktopExecArg(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"`", "\\`",
		"$", `\$`,
		"%", "%%",
	)
	return `"` + replacer.Replace(value) + `"`
}

func desktopCommand(command string, arguments []string) string {
	return desktopExecArg(command) + " " + strings.Join(arguments, " ")
}

func xmlText(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func writeManagerFileAtomic(path string, value []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".agentbell-service-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(value); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
