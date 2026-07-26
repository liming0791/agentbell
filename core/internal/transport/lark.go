package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/liming0791/agentbell/core/internal/config"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

type permanentError struct {
	err error
}

func (value permanentError) Error() string {
	return value.err.Error()
}

func (value permanentError) Unwrap() error {
	return value.err
}

func (permanentError) Permanent() bool {
	return true
}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	name, args, err := executableCommand(runtime.GOOS, name, args, fileExists)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	command.Stdout = nil
	command.Stderr = &limitedWriter{writer: &stderr, remaining: 4096}
	err = command.Run()
	if err != nil {
		if stderr.Len() > 0 {
			return stderr.Bytes(), fmt.Errorf("%w: %s", err, stderr.String())
		}
		return nil, err
	}
	return nil, nil
}

func executableCommand(
	goos string,
	name string,
	args []string,
	exists func(string) bool,
) (string, []string, error) {
	if goos != "windows" {
		return name, args, nil
	}
	extension := strings.ToLower(filepath.Ext(name))
	if extension != ".cmd" && extension != ".bat" {
		return name, args, nil
	}
	powershellShim := strings.TrimSuffix(name, filepath.Ext(name)) + ".ps1"
	if !exists(powershellShim) {
		return "", nil, permanentError{err: fmt.Errorf(
			"Windows command shim %q requires adjacent PowerShell shim %q",
			name,
			powershellShim,
		)}
	}
	powershellArgs := []string{
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-File",
		powershellShim,
	}
	powershellArgs = append(powershellArgs, args...)
	return "powershell.exe", powershellArgs, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

type LarkCLI struct {
	Runner  Runner
	Timeout time.Duration
	Command string
}

func (sender LarkCLI) Send(ctx context.Context, channel config.Channel, text string) error {
	if channel.ChatID == "" {
		return permanentError{err: errors.New("selected Feishu channel has no chatId")}
	}
	timeout := sender.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	command := sender.Command
	if command == "" {
		command = "lark-cli"
	}
	as := channel.As
	if as != "user" {
		as = "bot"
	}
	sendContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := sender.runner().Run(
		sendContext,
		command,
		"im",
		"+messages-send",
		"--chat-id",
		channel.ChatID,
		"--text",
		text,
		"--as",
		as,
	)
	if errors.Is(sendContext.Err(), context.DeadlineExceeded) {
		return errors.New("lark-cli notification timed out")
	}
	if err != nil {
		return fmt.Errorf("lark-cli notification failed: %w", err)
	}
	return nil
}

func (sender LarkCLI) runner() Runner {
	if sender.Runner != nil {
		return sender.Runner
	}
	return ExecRunner{}
}

type limitedWriter struct {
	writer    *bytes.Buffer
	remaining int
}

func (writer *limitedWriter) Write(value []byte) (int, error) {
	originalLength := len(value)
	if writer.remaining <= 0 {
		return originalLength, nil
	}
	if len(value) > writer.remaining {
		value = value[:writer.remaining]
	}
	_, _ = writer.writer.Write(value)
	writer.remaining -= len(value)
	return originalLength, nil
}
