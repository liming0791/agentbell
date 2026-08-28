package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	windowsTaskStateScript              = `$task = Get-ScheduledTask -TaskPath '\AgentBell\' -TaskName 'AgentBell'; [Console]::Out.Write($task.State)`
	defaultWindowsTaskStartAttempts     = 51
	defaultWindowsTaskStartPollInterval = 100 * time.Millisecond
)

func (manager *Manager) restartLaunchAgent(
	ctx context.Context,
) (ManagerResult, error) {
	result := manager.launchdResult()
	if _, err := os.Stat(result.DefinitionPath); errors.Is(err, os.ErrNotExist) {
		result.Message = "AgentBell LaunchAgent is not installed"
		return result, errors.New("cannot restart an uninstalled AgentBell LaunchAgent")
	} else if err != nil {
		return result, err
	}
	result.Installed = true
	target := "gui/" + manager.UID + "/" + launchAgentLabel
	if _, err := manager.runner().Run(
		ctx,
		"launchctl",
		"kickstart",
		"-k",
		target,
	); err != nil {
		return result, fmt.Errorf("restart AgentBell LaunchAgent: %w", err)
	}
	verified, err := manager.statusLaunchAgent(ctx)
	if err != nil {
		return result, fmt.Errorf("verify AgentBell LaunchAgent restart: %w", err)
	}
	if !verified.Loaded || !verified.Running {
		return verified, errors.New(
			"verify AgentBell LaunchAgent restart: service is not running",
		)
	}
	verified.Changed = true
	verified.Message = "AgentBell LaunchAgent was safely restarted and verified"
	return verified, nil
}

func (manager *Manager) restartLinux(ctx context.Context) (ManagerResult, error) {
	backend := manager.linuxBackend(ctx, true)
	result := manager.linuxResult(backend)
	if _, err := os.Stat(result.DefinitionPath); errors.Is(err, os.ErrNotExist) {
		result.Message = "AgentBell Linux login service is not installed"
		return result, errors.New("cannot restart an uninstalled AgentBell Linux service")
	} else if err != nil {
		return result, err
	}
	result.Installed = true
	if backend == backendXDG {
		result.Loaded = true
		result.Message = "AgentBell XDG autostart entry can only start at the next desktop login"
		return result, fmt.Errorf(
			"%w: XDG autostart has no service manager or verifiable process identity",
			ErrRestartUnsupported,
		)
	}
	if _, err := manager.runner().Run(
		ctx,
		"systemctl",
		"--user",
		"restart",
		systemdUnitName,
	); err != nil {
		return result, fmt.Errorf("restart AgentBell systemd user service: %w", err)
	}
	verified, err := manager.statusLinux(ctx)
	if err != nil {
		return result, fmt.Errorf("verify AgentBell systemd restart: %w", err)
	}
	if !verified.Running {
		return verified, errors.New(
			"verify AgentBell systemd restart: service is not active",
		)
	}
	verified.Changed = true
	verified.Message = "AgentBell systemd user service was safely restarted and verified"
	return verified, nil
}

func (manager *Manager) restartWindowsTask(
	ctx context.Context,
) (ManagerResult, error) {
	result := manager.windowsResult()
	if _, err := manager.runner().Run(
		ctx,
		"schtasks.exe",
		"/Query",
		"/TN",
		windowsTaskName,
	); err != nil {
		result.Message = "AgentBell Windows logon task is not registered"
		return result, fmt.Errorf("cannot restart an unregistered AgentBell task: %w", err)
	}
	result.Installed = true
	result.Loaded = true
	// Task Scheduler owns the process identity. /End is intentionally best
	// effort because it also fails when the registered task is already idle.
	_, _ = manager.runner().Run(
		ctx,
		"schtasks.exe",
		"/End",
		"/TN",
		windowsTaskName,
	)
	if _, err := manager.runner().Run(
		ctx,
		"schtasks.exe",
		"/Run",
		"/TN",
		windowsTaskName,
	); err != nil {
		return result, fmt.Errorf("restart AgentBell Windows logon task: %w", err)
	}
	state, err := manager.waitWindowsTaskRunning(ctx)
	if err != nil {
		return result, fmt.Errorf("verify AgentBell Windows task restart: %w", err)
	}
	if !strings.EqualFold(state, "Running") {
		return result, fmt.Errorf(
			"verify AgentBell Windows task restart: state is %q, not Running",
			state,
		)
	}
	result.Running = true
	result.Changed = true
	result.Message = "AgentBell Windows logon task was safely restarted and verified"
	return result, nil
}

func (manager *Manager) waitWindowsTaskRunning(ctx context.Context) (string, error) {
	attempts := manager.windowsTaskStartAttempts
	if attempts <= 0 {
		attempts = defaultWindowsTaskStartAttempts
	}
	interval := manager.windowsTaskStartPollInterval
	if interval <= 0 && manager.windowsTaskStartAttempts <= 0 {
		interval = defaultWindowsTaskStartPollInterval
	}

	state := ""
	for attempt := 0; attempt < attempts; attempt++ {
		output, err := manager.runner().Run(
			ctx,
			"powershell.exe",
			windowsTaskStateArgs()...,
		)
		if err != nil {
			return state, err
		}
		state = strings.TrimSpace(string(output))
		if strings.EqualFold(state, "Running") {
			return state, nil
		}
		if attempt+1 == attempts || interval <= 0 {
			continue
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return state, ctx.Err()
		case <-timer.C:
		}
	}
	return state, nil
}

func windowsTaskStateArgs() []string {
	return []string{
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		windowsTaskStateScript,
	}
}

func windowsStateCallKey() string {
	return "powershell.exe " + strings.Join(windowsTaskStateArgs(), " ")
}
