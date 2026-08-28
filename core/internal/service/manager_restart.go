package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	windowsTaskStateScript     = `$task = Get-ScheduledTask -TaskPath '\AgentBell\' -TaskName 'AgentBell'; [Console]::Out.Write($task.State)`
	windowsTaskWaitStateScript = `$deadline = [DateTime]::UtcNow.AddSeconds(5); do { $task = Get-ScheduledTask -TaskPath '\AgentBell\' -TaskName 'AgentBell'; $state = [string]$task.State; if ($state -eq 'Running') { break }; Start-Sleep -Milliseconds 100 } while ([DateTime]::UtcNow -lt $deadline); [Console]::Out.Write($state)`
	windowsTaskQuiesceScript   = `$deadline = [DateTime]::UtcNow.AddSeconds(5); do { $task = Get-ScheduledTask -TaskPath '\AgentBell\' -TaskName 'AgentBell'; $state = [string]$task.State; $lockOwnerAlive = $false; if (Test-Path -LiteralPath $LockPath) { try { $record = Get-Content -Raw -LiteralPath $LockPath | ConvertFrom-Json; $lockOwnerPid = [int]$record.pid; if ($lockOwnerPid -gt 0) { $lockOwnerAlive = $null -ne (Get-Process -Id $lockOwnerPid -ErrorAction SilentlyContinue) } } catch { $lockOwnerAlive = $true } }; if ($state -ne 'Running' -and -not $lockOwnerAlive) { break }; Start-Sleep -Milliseconds 100 } while ([DateTime]::UtcNow -lt $deadline); if ($state -eq 'Running' -or $lockOwnerAlive) { [Console]::Error.Write("task state=$state lockOwnerAlive=$lockOwnerAlive"); exit 1 }`
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
	if err := manager.waitWindowsTaskQuiesced(ctx); err != nil {
		return result, fmt.Errorf("wait for AgentBell Windows task to stop: %w", err)
	}
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

func (manager *Manager) waitWindowsTaskQuiesced(ctx context.Context) error {
	if manager.StateDir == "" || !absoluteFor("windows", manager.StateDir) {
		return errors.New("AgentBell Windows service state path is incomplete")
	}
	lockPath := filepath.Join(manager.StateDir, "queue", "service.lock")
	_, err := manager.runner().Run(
		ctx,
		"powershell.exe",
		windowsTaskQuiesceArgs(lockPath)...,
	)
	return err
}

func (manager *Manager) waitWindowsTaskRunning(ctx context.Context) (string, error) {
	output, err := manager.runner().Run(
		ctx,
		"powershell.exe",
		windowsTaskWaitStateArgs()...,
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func windowsTaskStateArgs() []string {
	return []string{
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		windowsTaskStateScript,
	}
}

func windowsTaskWaitStateArgs() []string {
	return []string{
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		windowsTaskWaitStateScript,
	}
}

func windowsTaskQuiesceArgs(lockPath string) []string {
	script := "$LockPath = " + powershellSingleQuoted(lockPath) + "; " +
		windowsTaskQuiesceScript
	return []string{
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		script,
	}
}

func powershellSingleQuoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func windowsStateCallKey() string {
	return "powershell.exe " + strings.Join(windowsTaskStateArgs(), " ")
}

func windowsWaitStateCallKey() string {
	return "powershell.exe " + strings.Join(windowsTaskWaitStateArgs(), " ")
}

func windowsQuiesceCallKey(lockPath string) string {
	return "powershell.exe " + strings.Join(windowsTaskQuiesceArgs(lockPath), " ")
}
