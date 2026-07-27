package binding

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type recordingCommandExecutor struct {
	name   string
	args   []string
	stdout string
	stderr string
	err    error
	wait   bool
}

func (executor *recordingCommandExecutor) Run(
	ctx context.Context,
	name string,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	executor.name = name
	executor.args = append([]string(nil), args...)
	if executor.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	if executor.stdout != "" {
		_, _ = io.WriteString(stdout, executor.stdout)
	}
	if executor.stderr != "" {
		_, _ = io.WriteString(stderr, executor.stderr)
	}
	return executor.err
}

func TestLarkCLIRunnerContractRecordsVerifiedBoundaries(t *testing.T) {
	contract := (LarkCLIRunner{}).Contract()
	if !contract.ServerKeywordSearch ||
		contract.ServerExactTextSearch ||
		!contract.ServerTimeWindow ||
		contract.ServerTimePrecision != time.Second ||
		contract.OutputTimePrecision != time.Minute ||
		contract.MaxSearchPages != 40 ||
		!contract.SendIdempotencyKey {
		t.Fatalf("unexpected lark-cli contract: %#v", contract)
	}
}

func TestLarkCLIRunnerSearchUsesVerifiedArgvAndFiltersExactMessage(t *testing.T) {
	location := time.FixedZone("SGT", 8*60*60)
	createdAt := time.Date(2026, 7, 26, 12, 0, 0, 0, location)
	expiresAt := createdAt.Add(10 * time.Minute)
	executor := &recordingCommandExecutor{stdout: `{
		"ok": true,
		"identity": "user",
		"data": {
			"messages": [
				{
					"message_id": "om_exact",
					"msg_type": "text",
					"content": "AGB-01234-56789-ABCDE-FGHJK",
					"sender": {"id":"ou_sender","id_type":"open_id","sender_type":"user","name":"User"},
					"create_time": "2026-07-26 12:01",
					"deleted": false,
					"updated": false,
					"chat_id": "oc_exact"
				},
				{
					"message_id": "om_keyword",
					"msg_type": "text",
					"content": "prefix AGB-01234-56789-ABCDE-FGHJK",
					"sender": {"id":"ou_sender","id_type":"open_id","sender_type":"user"},
					"create_time": "2026-07-26 12:02",
					"deleted": false,
					"updated": false,
					"chat_id": "oc_keyword"
				},
				{
					"message_id": "om_bot",
					"msg_type": "text",
					"content": "AGB-01234-56789-ABCDE-FGHJK",
					"sender": {"id":"cli_sender","id_type":"app_id","sender_type":"bot"},
					"create_time": "2026-07-26 12:03",
					"deleted": false,
					"updated": false,
					"chat_id": "oc_bot"
				},
				{
					"message_id": "om_post",
					"msg_type": "post",
					"content": "AGB-01234-56789-ABCDE-FGHJK",
					"sender": {"id":"ou_sender","id_type":"open_id","sender_type":"user"},
					"create_time": "2026-07-26 12:04",
					"deleted": false,
					"updated": false,
					"chat_id": "oc_post"
				}
			],
			"total": 4,
			"has_more": false,
			"page_token": ""
		}
	}`}
	runner := LarkCLIRunner{
		Command:        "/absolute/lark-cli",
		Executor:       executor,
		OutputLocation: location,
	}

	result, err := runner.SearchMessages(context.Background(), SearchRequest{
		ExactText: "AGB-01234-56789-ABCDE-FGHJK",
		Identity:  SearchIdentityUser,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantArgs := []string{
		"im", "+messages-search",
		"--as", "user",
		"--query", "AGB-01234-56789-ABCDE-FGHJK",
		"--sender-type", "user",
		"--start", "2026-07-26T12:00:00+08:00",
		"--end", "2026-07-26T12:09:59+08:00",
		"--page-size", "50",
		"--page-all",
		"--page-limit", "40",
		"--format", "json",
	}
	if executor.name != "/absolute/lark-cli" ||
		!reflect.DeepEqual(executor.args, wantArgs) {
		t.Fatalf("command = %q %#v, want %#v", executor.name, executor.args, wantArgs)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages = %#v", result)
	}
	message := result.Messages[0]
	if message.ChatID != "oc_exact" ||
		message.Identity != SearchIdentityUser ||
		!message.CreatedAt.Equal(time.Date(2026, 7, 26, 12, 1, 0, 0, location)) ||
		string(message.BodyContent) != `{"text":"AGB-01234-56789-ABCDE-FGHJK"}` {
		t.Fatalf("normalized message = %#v", message)
	}
}

func TestLarkCLIRunnerSearchRoundsWindowInwardAndClampsMinuteOutput(t *testing.T) {
	location := time.FixedZone("SGT", 8*60*60)
	createdAt := time.Date(2026, 7, 26, 12, 0, 0, 250_000_000, location)
	expiresAt := createdAt.Add(2 * time.Minute)
	executor := &recordingCommandExecutor{stdout: `{
		"ok":true,
		"identity":"user",
		"data":{
			"messages":[{
				"message_id":"om_exact",
				"msg_type":"text",
				"content":"code",
				"sender":{"id":"ou_sender","id_type":"open_id","sender_type":"user"},
				"create_time":"2026-07-26 12:00",
				"deleted":false,
				"updated":false,
				"chat_id":"oc_exact"
			}],
			"total":1,
			"has_more":false,
			"page_token":""
		}
	}`}
	runner := LarkCLIRunner{Executor: executor, OutputLocation: location}

	result, err := runner.SearchMessages(context.Background(), SearchRequest{
		ExactText: "code",
		Identity:  SearchIdentityUser,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := argumentValue(executor.args, "--start"); got != "2026-07-26T12:00:01+08:00" {
		t.Fatalf("--start = %q", got)
	}
	if got := argumentValue(executor.args, "--end"); got != "2026-07-26T12:01:59+08:00" {
		t.Fatalf("--end = %q", got)
	}
	if len(result.Messages) != 1 ||
		!result.Messages[0].CreatedAt.Equal(
			time.Date(2026, 7, 26, 12, 0, 1, 0, location),
		) {
		t.Fatalf("minute timestamp was not safely clamped: %#v", result)
	}
}

func TestLarkCLIRunnerSearchRejectsIncompleteOrMalformedOutput(t *testing.T) {
	location := time.FixedZone("SGT", 8*60*60)
	request := SearchRequest{
		ExactText: "code",
		Identity:  SearchIdentityUser,
		CreatedAt: time.Date(2026, 7, 26, 12, 0, 0, 0, location),
		ExpiresAt: time.Date(2026, 7, 26, 12, 2, 0, 0, location),
	}
	tests := []struct {
		name   string
		output string
	}{
		{
			"unknown field",
			`{"ok":true,"identity":"user","data":{"messages":[],"total":0,"has_more":false,"page_token":"","unexpected":true}}`,
		},
		{
			"JSON trailer",
			`{"ok":true,"identity":"user","data":{"messages":[],"total":0,"has_more":false,"page_token":""}} {}`,
		},
		{
			"wrong identity",
			`{"ok":true,"identity":"bot","data":{"messages":[],"total":0,"has_more":false,"page_token":""}}`,
		},
		{
			"not ok",
			`{"ok":false,"identity":"user","data":{"messages":[],"total":0,"has_more":false,"page_token":""}}`,
		},
		{
			"pagination incomplete",
			`{"ok":true,"identity":"user","data":{"messages":[],"total":0,"has_more":true,"page_token":"opaque"}}`,
		},
		{
			"mget fallback",
			`{"ok":true,"identity":"user","data":{"message_ids":["om_x"],"total":1,"has_more":false,"page_token":"","note":"fallback"}}`,
		},
		{
			"count mismatch",
			`{"ok":true,"identity":"user","data":{"messages":[],"total":1,"has_more":false,"page_token":""}}`,
		},
		{
			"unsafe exact destination",
			`{"ok":true,"identity":"user","data":{"messages":[{"message_id":"om_x","msg_type":"text","content":"code","sender":{"id":"ou_x","id_type":"open_id","sender_type":"user"},"create_time":"2026-07-26 12:01","deleted":false,"updated":false,"chat_id":"unsafe\nchat"}],"total":1,"has_more":false,"page_token":""}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := LarkCLIRunner{
				Executor:       &recordingCommandExecutor{stdout: test.output},
				OutputLocation: location,
			}
			if _, err := runner.SearchMessages(context.Background(), request); !errors.Is(err, ErrLarkCLIOutput) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLarkCLIRunnerSendVerificationUsesTargetIdentityAndIdempotency(t *testing.T) {
	executor := &recordingCommandExecutor{}
	runner := LarkCLIRunner{
		Command:  "/absolute/lark-cli",
		Executor: executor,
	}
	text := `verify "$(touch /tmp/never)" ; & |`
	err := runner.SendVerification(context.Background(), VerificationRequest{
		Destination:    newDestination("oc_target"),
		Identity:       "bot",
		Text:           text,
		IdempotencyKey: "sha256:verification",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"im", "+messages-send",
		"--as", "bot",
		"--chat-id", "oc_target",
		"--text", text,
		"--idempotency-key", "sha256:verification",
	}
	if executor.name != "/absolute/lark-cli" || !reflect.DeepEqual(executor.args, want) {
		t.Fatalf("command = %q %#v, want %#v", executor.name, executor.args, want)
	}
}

func TestLarkCLIRunnerBoundsOutputTimesOutAndDoesNotLeak(t *testing.T) {
	secret := "AGB-SECRET oc_private token_private"
	request := SearchRequest{
		ExactText: secret,
		Identity:  SearchIdentityUser,
		CreatedAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2026, 7, 26, 12, 2, 0, 0, time.UTC),
	}
	tests := []struct {
		name     string
		executor *recordingCommandExecutor
		timeout  time.Duration
	}{
		{
			name: "stdout limit",
			executor: &recordingCommandExecutor{
				stdout: strings.Repeat(secret, 20),
			},
		},
		{
			name: "stderr limit",
			executor: &recordingCommandExecutor{
				stderr: strings.Repeat(secret, 20),
				err:    errors.New(secret),
			},
		},
		{
			name:     "timeout",
			executor: &recordingCommandExecutor{wait: true},
			timeout:  time.Millisecond,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := LarkCLIRunner{
				Executor:       test.executor,
				Timeout:        test.timeout,
				MaxStdoutBytes: 32,
				MaxStderrBytes: 16,
			}
			_, err := runner.SearchMessages(context.Background(), request)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), secret) ||
				strings.Contains(err.Error(), "oc_private") ||
				strings.Contains(err.Error(), "token_private") {
				t.Fatalf("error leaked sensitive command data: %v", err)
			}
		})
	}
}

func TestLarkCLIRunnerRejectsUnsafeRequestsWithoutExecuting(t *testing.T) {
	executor := &recordingCommandExecutor{}
	runner := LarkCLIRunner{Executor: executor}
	_, err := runner.SearchMessages(context.Background(), SearchRequest{
		ExactText: "code",
		Identity:  "bot",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrInvalidLarkCLIRequest) {
		t.Fatalf("search error = %v", err)
	}
	err = runner.SendVerification(context.Background(), VerificationRequest{
		Destination:    newDestination("unsafe\nchat"),
		Identity:       "bot",
		Text:           "verify",
		IdempotencyKey: "key",
	})
	if !errors.Is(err, ErrInvalidLarkCLIRequest) {
		t.Fatalf("send error = %v", err)
	}
	if executor.name != "" || len(executor.args) != 0 {
		t.Fatalf("unsafe request executed: %q %#v", executor.name, executor.args)
	}
}

func TestExecCommandExecutorUsesArgv(t *testing.T) {
	t.Setenv("AGENTBELL_BINDING_EXEC_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr boundedBuffer
	stdout.limit = 128
	stderr.limit = 128
	err = (execCommandExecutor{}).Run(
		context.Background(),
		executable,
		[]string{"-test.run=TestBindingExecHelper", "--", `literal ; $(no-shell)`},
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("exec helper: %v stderr=%q", err, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), `literal ; $(no-shell)`) {
		t.Fatalf("argv was not preserved: %q", stdout.String())
	}
}

func TestBindingExecHelper(t *testing.T) {
	if os.Getenv("AGENTBELL_BINDING_EXEC_HELPER") != "1" {
		return
	}
	args := os.Args
	for index, value := range args {
		if value == "--" && index+1 < len(args) {
			_, _ = os.Stdout.WriteString(args[index+1])
			return
		}
	}
	os.Exit(2)
}

func argumentValue(args []string, flag string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	return ""
}
