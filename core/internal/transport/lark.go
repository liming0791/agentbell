package transport

import (
	"bytes"
	"context"
	"encoding/json"
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
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, remaining: 1024 * 1024}
	command.Stderr = &limitedWriter{writer: &stderr, remaining: 4096}
	err = command.Run()
	if err != nil {
		if stderr.Len() > 0 {
			return stderr.Bytes(), fmt.Errorf("%w: %s", err, stderr.String())
		}
		return nil, err
	}
	return stdout.Bytes(), nil
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

type chatListItem struct {
	ID string `json:"chat_id"`
}

type chatListResponse struct {
	Data struct {
		Chats     []chatListItem `json:"chats"`
		Items     []chatListItem `json:"items"`
		HasMore   bool           `json:"has_more"`
		PageToken string         `json:"page_token"`
	} `json:"data"`
}

// VerifyUserReachability proves that the currently authorized Feishu user is
// a member of the configured chat. A successful bot send alone is insufficient:
// Feishu permits a bot-only group, which produces a false-positive test result.
func (sender LarkCLI) VerifyUserReachability(
	ctx context.Context,
	channel config.Channel,
) error {
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
	verifyContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pageToken := ""
	seenTokens := map[string]struct{}{}
	for {
		args := []string{
			"im",
			"+chat-list",
			"--as",
			"user",
			"--page-size",
			"100",
		}
		if pageToken != "" {
			args = append(args, "--page-token", pageToken)
		}
		args = append(args, "--format", "json")
		output, err := sender.runner().Run(verifyContext, command, args...)
		if errors.Is(verifyContext.Err(), context.DeadlineExceeded) {
			return errors.New("lark-cli recipient verification timed out")
		}
		if err != nil {
			return errors.New(
				"cannot verify the Feishu user recipient; update lark-cli if needed, run `lark-cli auth login --domain im`, then run `agentbell setup` again",
			)
		}
		var response chatListResponse
		if err := json.Unmarshal(output, &response); err != nil {
			return fmt.Errorf("cannot parse lark-cli chat list while verifying the recipient: %w", err)
		}
		chats := append(response.Data.Chats, response.Data.Items...)
		for _, chat := range chats {
			if chat.ID == channel.ChatID {
				return nil
			}
		}
		if !response.Data.HasMore {
			break
		}
		pageToken = strings.TrimSpace(response.Data.PageToken)
		if pageToken == "" {
			return errors.New("lark-cli chat list reported more pages without a page token")
		}
		if _, exists := seenTokens[pageToken]; exists {
			return errors.New("lark-cli chat list repeated a page token")
		}
		seenTokens[pageToken] = struct{}{}
	}
	return permanentError{err: errors.New(
		"the authorized Feishu user is not a member of the configured notification chat; run `agentbell setup` again and choose a chat shared by the user and bot",
	)}
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
