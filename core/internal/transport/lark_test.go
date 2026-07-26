package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/config"
)

type fakeRunner struct {
	name string
	args []string
	err  error
	wait time.Duration
}

func (runner *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	runner.name = name
	runner.args = append([]string(nil), args...)
	if runner.wait > 0 {
		select {
		case <-time.After(runner.wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, runner.err
}

func TestLarkCLIUsesArgumentVector(t *testing.T) {
	runner := &fakeRunner{}
	sender := LarkCLI{Runner: runner, Command: "lark-cli-test"}
	err := sender.Send(context.Background(), config.Channel{
		ChatID: "oc_test",
		As:     "bot",
	}, `hello "quoted"`)
	if err != nil {
		t.Fatal(err)
	}
	if runner.name != "lark-cli-test" {
		t.Fatalf("unexpected command: %q", runner.name)
	}
	joined := strings.Join(runner.args, "\x00")
	if !strings.Contains(joined, `hello "quoted"`) || !strings.Contains(joined, "oc_test") {
		t.Fatalf("arguments were not preserved: %#v", runner.args)
	}
}

func TestLarkCLIErrorsAndTimeout(t *testing.T) {
	sender := LarkCLI{Runner: &fakeRunner{err: errors.New("exit 1")}}
	if err := sender.Send(context.Background(), config.Channel{ChatID: "oc"}, "hello"); err == nil {
		t.Fatal("expected runner error")
	}
	timeoutSender := LarkCLI{
		Runner:  &fakeRunner{wait: time.Second},
		Timeout: time.Millisecond,
	}
	if err := timeoutSender.Send(context.Background(), config.Channel{ChatID: "oc"}, "hello"); err == nil {
		t.Fatal("expected timeout")
	}
	if err := sender.Send(context.Background(), config.Channel{}, "hello"); err == nil {
		t.Fatal("expected missing chat id error")
	} else {
		var permanent interface {
			Permanent() bool
		}
		if !errors.As(err, &permanent) || !permanent.Permanent() ||
			!strings.Contains(err.Error(), "chatId") ||
			errors.Unwrap(err) == nil {
			t.Fatalf("missing chat id was not classified as permanent: %v", err)
		}
	}
}

func TestLimitedWriterAndDefaultRunner(t *testing.T) {
	var buffer bytes.Buffer
	writer := &limitedWriter{writer: &buffer, remaining: 4}
	if count, err := writer.Write([]byte("123456")); err != nil || count != 6 {
		t.Fatalf("write count=%d err=%v", count, err)
	}
	if buffer.String() != "1234" {
		t.Fatalf("unexpected limited output: %q", buffer.String())
	}
	if _, ok := (LarkCLI{}).runner().(ExecRunner); !ok {
		t.Fatal("default runner is not ExecRunner")
	}
}

func TestExecRunnerSuccessAndStderr(t *testing.T) {
	t.Setenv("AGENTBELL_EXEC_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := ExecRunner{}
	if _, err := runner.Run(
		context.Background(),
		executable,
		"-test.run=TestExecRunnerHelper",
		"--",
		"success",
	); err != nil {
		t.Fatalf("helper success: %v", err)
	}
	output, err := runner.Run(
		context.Background(),
		executable,
		"-test.run=TestExecRunnerHelper",
		"--",
		"stderr",
	)
	if err == nil || !strings.Contains(err.Error(), "helper failure") {
		t.Fatalf("helper stderr: output=%q err=%v", output, err)
	}
	if len(output) > 4096 {
		t.Fatalf("stderr was not capped: %d bytes", len(output))
	}
}

func TestExecRunnerNotFoundAndTimeout(t *testing.T) {
	runner := ExecRunner{}
	if _, err := runner.Run(context.Background(), "agentbell-command-does-not-exist"); err == nil {
		t.Fatal("expected command-not-found error")
	}
	t.Setenv("AGENTBELL_EXEC_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := runner.Run(
		ctx,
		executable,
		"-test.run=TestExecRunnerHelper",
		"--",
		"wait",
	); err == nil {
		t.Fatal("expected helper timeout")
	}
}

func TestExecutableCommandUsesPowerShellForWindowsNPMShims(t *testing.T) {
	arguments := []string{
		"im",
		"+messages-send",
		"--text",
		"quoted \" text & %PATH%",
	}
	name, got, err := executableCommand(
		"windows",
		`C:\Users\test\AppData\Roaming\npm\lark-cli.cmd`,
		arguments,
		func(path string) bool {
			return path == `C:\Users\test\AppData\Roaming\npm\lark-cli.ps1`
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if name != "powershell.exe" {
		t.Fatalf("command = %q, want powershell.exe", name)
	}
	if len(got) < len(arguments)+2 ||
		got[len(got)-1] != arguments[len(arguments)-1] ||
		got[len(got)-4] != arguments[0] {
		t.Fatalf("arguments were not preserved: %#v", got)
	}
	if got[5] != "-File" ||
		got[6] != `C:\Users\test\AppData\Roaming\npm\lark-cli.ps1` {
		t.Fatalf("PowerShell shim invocation is incomplete: %#v", got)
	}
}

func TestExecutableCommandRejectsOrLeavesOtherCommands(t *testing.T) {
	if _, _, err := executableCommand(
		"windows",
		`C:\npm\lark-cli.cmd`,
		nil,
		func(string) bool { return false },
	); err == nil || !strings.Contains(err.Error(), "PowerShell shim") {
		t.Fatalf("expected missing PowerShell shim error, got %v", err)
	} else {
		var permanent interface {
			Permanent() bool
		}
		if !errors.As(err, &permanent) || !permanent.Permanent() {
			t.Fatalf("missing PowerShell shim was not permanent: %v", err)
		}
	}
	name, args, err := executableCommand(
		"windows",
		`C:\AgentBell\lark-cli.exe`,
		[]string{"test"},
		func(string) bool { return false },
	)
	if err != nil || name != `C:\AgentBell\lark-cli.exe` || len(args) != 1 {
		t.Fatalf("unexpected executable resolution: name=%q args=%#v err=%v", name, args, err)
	}
	name, _, err = executableCommand(
		"linux",
		"/tmp/lark-cli.cmd",
		nil,
		func(string) bool { return true },
	)
	if err != nil || name != "/tmp/lark-cli.cmd" {
		t.Fatalf("non-Windows command was rewritten: name=%q err=%v", name, err)
	}
}

func TestExecRunnerHelper(t *testing.T) {
	if os.Getenv("AGENTBELL_EXEC_HELPER") != "1" {
		return
	}
	mode := ""
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			break
		}
	}
	switch mode {
	case "success":
		return
	case "stderr":
		_, _ = fmt.Fprint(os.Stderr, "helper failure: ", strings.Repeat("x", 8192))
		os.Exit(17)
	case "wait":
		time.Sleep(10 * time.Second)
	default:
		os.Exit(18)
	}
}
