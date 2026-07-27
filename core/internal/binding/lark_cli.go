package binding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultLarkCLITimeout     = 15 * time.Second
	defaultLarkCLIStdoutLimit = int64(4 << 20)
	defaultLarkCLIStderrLimit = int64(4 << 10)
	larkCLIMaxSearchPages     = 40
	larkCLIMaxSearchPageSize  = 50
)

var (
	ErrInvalidLarkCLIRequest = errors.New("invalid lark-cli binding request")
	ErrLarkCLIExecution      = errors.New("lark-cli binding command failed")
	ErrLarkCLITimeout        = errors.New("lark-cli binding command timed out")
	ErrLarkCLIOutput         = errors.New("invalid lark-cli binding output")
)

// LarkCLIContract records the relevant, verified contract of the official
// lark-cli `im +messages-search` and `im +messages-send` shortcuts.
//
// Version 1.0.30 performs keyword, sender-type and time-range filtering on the
// server. It does not provide an exact-text flag, so LarkCLIRunner must filter
// the normalized text again. Search output formats create_time only to a local
// minute, even though the server filter accepts second-precision timestamps.
type LarkCLIContract struct {
	ServerKeywordSearch   bool
	ServerExactTextSearch bool
	ServerTimeWindow      bool
	ServerTimePrecision   time.Duration
	OutputTimePrecision   time.Duration
	MaxSearchPages        int
	SendIdempotencyKey    bool
}

// CommandExecutor is the argv-only process boundary used by LarkCLIRunner.
// Implementations must not invoke a command shell.
type CommandExecutor interface {
	Run(
		context.Context,
		string,
		[]string,
		io.Writer,
		io.Writer,
	) error
}

// LarkCLIRunner is the production discovery.Runner implementation backed by
// the official lark-cli. Command should be the absolute configured larkCliPath
// in production.
type LarkCLIRunner struct {
	Command        string
	Timeout        time.Duration
	MaxStdoutBytes int64
	MaxStderrBytes int64
	OutputLocation *time.Location
	Executor       CommandExecutor
}

var _ Runner = LarkCLIRunner{}

func (LarkCLIRunner) Contract() LarkCLIContract {
	return LarkCLIContract{
		ServerKeywordSearch:   true,
		ServerExactTextSearch: false,
		ServerTimeWindow:      true,
		ServerTimePrecision:   time.Second,
		OutputTimePrecision:   time.Minute,
		MaxSearchPages:        larkCLIMaxSearchPages,
		SendIdempotencyKey:    true,
	}
}

func (runner LarkCLIRunner) SearchMessages(
	ctx context.Context,
	request SearchRequest,
) (SearchResult, error) {
	if !validSearchRequest(request) {
		return SearchResult{}, ErrInvalidLarkCLIRequest
	}

	start, end, ok := strictSecondWindow(request.CreatedAt, request.ExpiresAt)
	if !ok {
		return SearchResult{}, ErrInvalidLarkCLIRequest
	}
	args := []string{
		"im",
		"+messages-search",
		"--as",
		SearchIdentityUser,
		"--query",
		request.ExactText,
		"--sender-type",
		SearchIdentityUser,
		"--start",
		start.Format(time.RFC3339),
		"--end",
		end.Format(time.RFC3339),
		"--page-size",
		fmt.Sprintf("%d", larkCLIMaxSearchPageSize),
		"--page-all",
		"--page-limit",
		fmt.Sprintf("%d", larkCLIMaxSearchPages),
		"--format",
		"json",
	}

	output, err := runner.run(ctx, args)
	if err != nil {
		return SearchResult{}, err
	}
	envelope, err := decodeSearchEnvelope(output)
	if err != nil {
		return SearchResult{}, ErrLarkCLIOutput
	}
	if envelope.OK == nil ||
		!*envelope.OK ||
		envelope.Identity != SearchIdentityUser ||
		envelope.Data == nil ||
		envelope.Data.Messages == nil ||
		envelope.Data.Total == nil ||
		envelope.Data.HasMore == nil ||
		envelope.Data.PageToken == nil ||
		*envelope.Data.Total != len(*envelope.Data.Messages) ||
		*envelope.Data.HasMore {
		return SearchResult{}, ErrLarkCLIOutput
	}

	location := runner.OutputLocation
	if location == nil {
		location = time.Local
	}
	result := SearchResult{
		Messages: make([]SearchMessage, 0, len(*envelope.Data.Messages)),
	}
	for _, raw := range *envelope.Data.Messages {
		message, matches, normalizeErr := normalizeSearchMessage(
			raw,
			request,
			location,
		)
		if normalizeErr != nil {
			return SearchResult{}, ErrLarkCLIOutput
		}
		if matches {
			result.Messages = append(result.Messages, message)
		}
	}
	return result, nil
}

func (runner LarkCLIRunner) SendVerification(
	ctx context.Context,
	request VerificationRequest,
) error {
	if !validVerificationRequest(request) {
		return ErrInvalidLarkCLIRequest
	}
	_, err := runner.run(ctx, []string{
		"im",
		"+messages-send",
		"--as",
		request.Identity,
		"--chat-id",
		request.Destination.ChatID(),
		"--text",
		request.Text,
		"--idempotency-key",
		request.IdempotencyKey,
	})
	return err
}

func (runner LarkCLIRunner) run(
	ctx context.Context,
	args []string,
) ([]byte, error) {
	timeout := runner.Timeout
	if timeout <= 0 {
		timeout = defaultLarkCLITimeout
	}
	stdoutLimit := runner.MaxStdoutBytes
	if stdoutLimit <= 0 {
		stdoutLimit = defaultLarkCLIStdoutLimit
	}
	stderrLimit := runner.MaxStderrBytes
	if stderrLimit <= 0 {
		stderrLimit = defaultLarkCLIStderrLimit
	}
	command := runner.Command
	if command == "" {
		command = "lark-cli"
	}
	executor := runner.Executor
	if executor == nil {
		executor = execCommandExecutor{}
	}

	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout := boundedBuffer{limit: stdoutLimit}
	stderr := boundedBuffer{limit: stderrLimit}
	err := executor.Run(
		commandContext,
		command,
		append([]string(nil), args...),
		&stdout,
		&stderr,
	)
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		return nil, ErrLarkCLITimeout
	}
	if errors.Is(commandContext.Err(), context.Canceled) {
		return nil, context.Canceled
	}
	if stdout.overflow {
		return nil, ErrLarkCLIOutput
	}
	if err != nil {
		// stdout, stderr and the executor error may contain a binding code,
		// chat ID, user ID, token or authorization URL. Deliberately do not
		// include or wrap any of them in the returned error.
		return nil, ErrLarkCLIExecution
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func validSearchRequest(request SearchRequest) bool {
	return request.Identity == SearchIdentityUser &&
		validCommandText(request.ExactText, 1024, true) &&
		!request.CreatedAt.IsZero() &&
		!request.ExpiresAt.IsZero() &&
		request.CreatedAt.Before(request.ExpiresAt)
}

func validVerificationRequest(request VerificationRequest) bool {
	return validChatID(request.Destination.ChatID()) &&
		(request.Identity == "bot" || request.Identity == "user") &&
		validCommandText(request.Text, 8192, true) &&
		validCommandText(request.IdempotencyKey, 512, false)
}

func validCommandText(value string, maximum int, allowWhitespace bool) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	if !allowWhitespace && strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 ||
			(!allowWhitespace && (character <= ' ' || character == '\u007f')) {
			return false
		}
	}
	return true
}

// strictSecondWindow rounds inward because lark-cli 1.0.30 parses ISO time
// values to Unix seconds and the API's end boundary is not documented as
// exclusive. This loses at most one second at each edge but cannot admit a
// message outside Discovery's [CreatedAt, ExpiresAt) validity window.
func strictSecondWindow(createdAt, expiresAt time.Time) (time.Time, time.Time, bool) {
	start := createdAt.Truncate(time.Second)
	if !start.Equal(createdAt) {
		start = start.Add(time.Second)
	}
	end := expiresAt.Truncate(time.Second).Add(-time.Second)
	return start, end, !end.Before(start)
}

type cliSearchEnvelope struct {
	OK                 *bool           `json:"ok"`
	Identity           string          `json:"identity"`
	Data               *cliSearchData  `json:"data"`
	Meta               *cliOutputMeta  `json:"meta,omitempty"`
	ContentSafetyAlert json.RawMessage `json:"_content_safety_alert,omitempty"`
	Notice             json.RawMessage `json:"_notice,omitempty"`
}

type cliOutputMeta struct {
	Count    int    `json:"count,omitempty"`
	Rollback string `json:"rollback,omitempty"`
}

type cliSearchData struct {
	Messages  *[]cliSearchMessage `json:"messages"`
	Total     *int                `json:"total"`
	HasMore   *bool               `json:"has_more"`
	PageToken *string             `json:"page_token"`
}

type cliSearchMessage struct {
	MessageID             *string           `json:"message_id"`
	MessageType           *string           `json:"msg_type"`
	Content               *string           `json:"content"`
	Sender                *cliMessageSender `json:"sender"`
	CreateTime            *string           `json:"create_time"`
	Deleted               *bool             `json:"deleted"`
	Updated               *bool             `json:"updated"`
	ThreadID              string            `json:"thread_id,omitempty"`
	ReplyTo               string            `json:"reply_to,omitempty"`
	ChatID                *string           `json:"chat_id"`
	MessagePosition       json.RawMessage   `json:"message_position,omitempty"`
	ThreadMessagePosition json.RawMessage   `json:"thread_message_position,omitempty"`
	MessageAppLink        string            `json:"message_app_link,omitempty"`
	Mentions              []cliMention      `json:"mentions,omitempty"`
	ChatType              string            `json:"chat_type,omitempty"`
	ChatPartner           *cliChatPartner   `json:"chat_partner,omitempty"`
	ChatName              string            `json:"chat_name,omitempty"`
}

type cliMessageSender struct {
	ID         string `json:"id"`
	IDType     string `json:"id_type,omitempty"`
	SenderType string `json:"sender_type"`
	TenantKey  string `json:"tenant_key,omitempty"`
	Name       string `json:"name,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
}

type cliMention struct {
	Key  string `json:"key"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cliChatPartner struct {
	OpenID string `json:"open_id"`
}

func decodeSearchEnvelope(output []byte) (cliSearchEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var envelope cliSearchEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return cliSearchEnvelope{}, err
	}
	var trailer any
	if err := decoder.Decode(&trailer); !errors.Is(err, io.EOF) {
		return cliSearchEnvelope{}, errors.New("unexpected JSON trailer")
	}
	return envelope, nil
}

func normalizeSearchMessage(
	raw cliSearchMessage,
	request SearchRequest,
	location *time.Location,
) (SearchMessage, bool, error) {
	if raw.MessageID == nil ||
		raw.MessageType == nil ||
		raw.Content == nil ||
		raw.Sender == nil ||
		raw.CreateTime == nil ||
		raw.Deleted == nil ||
		raw.Updated == nil ||
		raw.ChatID == nil {
		return SearchMessage{}, false, errors.New("incomplete message")
	}
	createdAt, err := time.ParseInLocation(
		"2006-01-02 15:04",
		*raw.CreateTime,
		location,
	)
	if err != nil {
		return SearchMessage{}, false, errors.New("invalid message time")
	}
	minuteEnd := createdAt.Add(time.Minute)
	if !minuteEnd.After(request.CreatedAt) ||
		!createdAt.Before(request.ExpiresAt) {
		return SearchMessage{}, false, nil
	}
	if createdAt.Before(request.CreatedAt) {
		strictStart, _, ok := strictSecondWindow(
			request.CreatedAt,
			request.ExpiresAt,
		)
		if !ok {
			return SearchMessage{}, false, errors.New("invalid message window")
		}
		createdAt = strictStart
	}

	if *raw.MessageType != "text" ||
		*raw.Content != request.ExactText ||
		raw.Sender.SenderType != SearchIdentityUser ||
		*raw.Deleted {
		return SearchMessage{}, false, nil
	}
	if !validChatID(*raw.ChatID) {
		return SearchMessage{}, false, errors.New("invalid chat destination")
	}
	bodyContent, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: *raw.Content})
	if err != nil {
		return SearchMessage{}, false, errors.New("could not normalize message")
	}
	return SearchMessage{
		ChatID:      *raw.ChatID,
		Identity:    SearchIdentityUser,
		CreatedAt:   createdAt,
		BodyContent: bodyContent,
	}, true, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int64
	overflow bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := buffer.limit - int64(buffer.Len())
	if remaining <= 0 {
		if originalLength > 0 {
			buffer.overflow = true
		}
		return originalLength, nil
	}
	if int64(len(value)) > remaining {
		value = value[:remaining]
		buffer.overflow = true
	}
	_, _ = buffer.Buffer.Write(value)
	return originalLength, nil
}

type execCommandExecutor struct{}

func (execCommandExecutor) Run(
	ctx context.Context,
	name string,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	executable, commandArgs, err := executableArgv(
		runtime.GOOS,
		name,
		args,
	)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, executable, commandArgs...)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func executableArgv(
	goos string,
	name string,
	args []string,
) (string, []string, error) {
	if goos != "windows" {
		return name, append([]string(nil), args...), nil
	}
	extension := strings.ToLower(filepath.Ext(name))
	if extension != ".cmd" && extension != ".bat" {
		return name, append([]string(nil), args...), nil
	}
	powerShellShim := strings.TrimSuffix(name, filepath.Ext(name)) + ".ps1"
	info, err := os.Stat(powerShellShim)
	if err != nil || info.IsDir() {
		return "", nil, errors.New("lark-cli PowerShell shim is unavailable")
	}
	commandArgs := []string{
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-File",
		powerShellShim,
	}
	commandArgs = append(commandArgs, args...)
	return "powershell.exe", commandArgs, nil
}
