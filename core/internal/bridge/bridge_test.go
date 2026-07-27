package bridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/installstate"
)

type runnerCall struct {
	Executable string
	Args       []string
	Input      []byte
}

type recordingRunner struct {
	Calls []runnerCall
	Err   error
}

func (runner *recordingRunner) Run(
	_ context.Context,
	executable string,
	args []string,
	input []byte,
	_ io.Writer,
	_ io.Writer,
) error {
	runner.Calls = append(runner.Calls, runnerCall{
		Executable: executable,
		Args:       append([]string(nil), args...),
		Input:      append([]byte(nil), input...),
	})
	return runner.Err
}

func bridgeFixture(t *testing.T) (*App, *recordingRunner, string, installstate.ActiveState) {
	t.Helper()
	dataRoot := t.TempDir()
	core := []byte("test AgentBell Core")
	state := installstate.ActiveState{
		SchemaVersion:   installstate.SchemaVersion,
		Generation:      11,
		ActiveVersion:   "0.3.0",
		PreviousVersion: "0.2.0",
		Target:          "linux-amd64",
		Checksum:        installstate.SHA256(core),
		BridgeChecksum:  strings.Repeat("b", 64),
		TransactionID:   "tx-bridge-test",
	}
	store := installstate.NewStore(installstate.OSFileSystem{})
	corePath, err := installstate.ManagedCorePath(dataRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(corePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corePath, core, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(dataRoot, state); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(
		dataRoot,
		"bin",
		"bridge",
		"v1",
		"agentbell-bridge",
	)
	runner := &recordingRunner{}
	app := &App{
		Store:  store,
		Runner: runner,
		ExecutablePath: func() (string, error) {
			return executable, nil
		},
		Target: "linux-amd64",
	}
	return app, runner, corePath, state
}

func hookArguments(adapter, surface string) []string {
	return []string{
		"hook-v1",
		"--adapter", adapter,
		"--surface", surface,
		"--runtime", "host",
		"--stdin",
		"--fail-open",
	}
}

func TestHookV1RunsActiveCoreWithArgumentArray(t *testing.T) {
	app, runner, corePath, state := bridgeFixture(t)
	input := []byte(`{"hook_event_name":"Stop"}`)
	exitCode := app.Run(
		context.Background(),
		hookArguments("codex", "cli"),
		bytes.NewReader(input),
		io.Discard,
		io.Discard,
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want fail-open success", exitCode)
	}
	if len(runner.Calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.Calls))
	}
	call := runner.Calls[0]
	if call.Executable != corePath {
		t.Fatalf("executable = %q, want %q", call.Executable, corePath)
	}
	expectedArgs := []string{
		"emit",
		"--adapter", "codex",
		"--surface", "cli",
		"--runtime", "host",
		"--stdin",
		"--fail-open",
		"--bridge-protocol", "1",
		"--activation-generation", strconv.FormatUint(state.Generation, 10),
	}
	if !reflect.DeepEqual(call.Args, expectedArgs) {
		t.Fatalf("Core args = %#v, want %#v", call.Args, expectedArgs)
	}
	if !bytes.Equal(call.Input, input) {
		t.Fatalf("Core input = %q, want %q", call.Input, input)
	}
}

func TestHookV1OmitsM2ProofFlagsForPreBridgeCore(t *testing.T) {
	app, runner, _, state := bridgeFixture(t)
	state.ActiveVersion = "0.2.0-rc.3"
	state.PreviousVersion = ""
	dataRoot, err := installstate.DataRootFromBridgePath(
		mustExecutablePath(t, app),
	)
	if err != nil {
		t.Fatal(err)
	}
	corePath, err := installstate.ManagedCorePath(dataRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(corePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corePath, []byte("test AgentBell Core"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := app.Store.Save(dataRoot, state); err != nil {
		t.Fatal(err)
	}

	input := []byte(`{"hook_event_name":"Stop"}`)
	if exitCode := app.Run(
		context.Background(),
		hookArguments("codex", "cli"),
		bytes.NewReader(input),
		io.Discard,
		io.Discard,
	); exitCode != 0 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if len(runner.Calls) != 1 {
		t.Fatalf("runner calls = %d", len(runner.Calls))
	}
	expected := []string{
		"emit",
		"--adapter", "codex",
		"--surface", "cli",
		"--runtime", "host",
		"--stdin",
		"--fail-open",
	}
	if runner.Calls[0].Executable != corePath ||
		!reflect.DeepEqual(runner.Calls[0].Args, expected) {
		t.Fatalf("pre-M2 Core call = %#v, want args %#v", runner.Calls[0], expected)
	}
}

func TestGenerationProofVersionBoundary(t *testing.T) {
	for version, expected := range map[string]bool{
		"0.2.0-rc.3": false,
		"0.3.0-rc.1": true,
		"0.3.0":      true,
		"1.0.0":      true,
		"invalid":    false,
	} {
		if actual := supportsGenerationProof(version); actual != expected {
			t.Fatalf(
				"supportsGenerationProof(%q) = %v, want %v",
				version,
				actual,
				expected,
			)
		}
	}
}

func mustExecutablePath(t *testing.T, app *App) string {
	t.Helper()
	value, err := app.ExecutablePath()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestHookV1ReadsOneJSONValueWithoutWaitingForEOF(t *testing.T) {
	app, runner, _, _ := bridgeFixture(t)
	reader, writer := io.Pipe()
	result := make(chan int, 1)
	go func() {
		result <- app.Run(
			context.Background(),
			hookArguments("claude-code", "desktop"),
			reader,
			io.Discard,
			io.Discard,
		)
	}()
	if _, err := writer.Write([]byte(`{"hook_event_name":"Stop"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case exitCode := <-result:
		if exitCode != 0 {
			t.Fatalf("exit code = %d", exitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge waited for stdin EOF after receiving a complete JSON value")
	}
	_ = writer.Close()
	_ = reader.Close()
	if len(runner.Calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.Calls))
	}
}

func TestHookV1IsFailOpenForEveryFailure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*App, *recordingRunner)
		args   []string
		input  []byte
	}{
		{
			name:  "invalid JSON",
			args:  hookArguments("codex", "cli"),
			input: []byte(`{"incomplete"`),
		},
		{
			name: "oversized JSON",
			args: hookArguments("codex", "cli"),
			input: append(
				append([]byte(`"`), bytes.Repeat([]byte("x"), MaxHookInput+1)...),
				'"',
			),
		},
		{
			name:  "unsupported adapter",
			args:  hookArguments("opencode", "cli"),
			input: []byte(`{}`),
		},
		{
			name:  "unsupported surface",
			args:  hookArguments("kimi-code", "desktop"),
			input: []byte(`{}`),
		},
		{
			name:  "extra argument",
			args:  append(hookArguments("codex", "cli"), "--unexpected"),
			input: []byte(`{}`),
		},
		{
			name: "active target mismatch",
			mutate: func(app *App, _ *recordingRunner) {
				app.Target = "darwin-arm64"
			},
			args:  hookArguments("codex", "cli"),
			input: []byte(`{}`),
		},
		{
			name: "runner failure",
			mutate: func(_ *App, runner *recordingRunner) {
				runner.Err = errors.New("spawn failed")
			},
			args:  hookArguments("codex", "cli"),
			input: []byte(`{}`),
		},
		{
			name: "executable lookup failure",
			mutate: func(app *App, _ *recordingRunner) {
				app.ExecutablePath = func() (string, error) {
					return "", errors.New("lookup failed")
				}
			},
			args:  hookArguments("codex", "cli"),
			input: []byte(`{}`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, runner, _, _ := bridgeFixture(t)
			if test.mutate != nil {
				test.mutate(app, runner)
			}
			if exitCode := app.Run(
				context.Background(),
				test.args,
				bytes.NewReader(test.input),
				io.Discard,
				io.Discard,
			); exitCode != 0 {
				t.Fatalf("hook failure returned %d, want 0", exitCode)
			}
		})
	}
}

func TestServiceV1RunsForegroundAndPropagatesFailure(t *testing.T) {
	app, runner, corePath, _ := bridgeFixture(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := app.Run(
		context.Background(),
		[]string{"service-v1"},
		strings.NewReader("ignored"),
		&stdout,
		&stderr,
	); exitCode != 0 {
		t.Fatalf("service exit code = %d", exitCode)
	}
	if len(runner.Calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.Calls))
	}
	call := runner.Calls[0]
	if call.Executable != corePath ||
		!reflect.DeepEqual(call.Args, []string{"service", "run", "--foreground"}) ||
		len(call.Input) != 0 {
		t.Fatalf("unexpected service call: %#v", call)
	}

	runner.Err = errors.New("service failed")
	if exitCode := app.Run(
		context.Background(),
		[]string{"service-v1"},
		bytes.NewReader(nil),
		io.Discard,
		io.Discard,
	); exitCode == 0 {
		t.Fatal("service runner failure was reported as success")
	}
}

func TestOnlyVersionedBridgeCommandsAreAccepted(t *testing.T) {
	app, runner, _, _ := bridgeFixture(t)
	for _, args := range [][]string{
		nil,
		{"hook"},
		{"service"},
		{"emit"},
		{"service-v1", "--unexpected"},
		{"hook-v2"},
	} {
		if exitCode := app.Run(
			context.Background(),
			args,
			bytes.NewReader([]byte(`{}`)),
			io.Discard,
			io.Discard,
		); exitCode == 0 {
			t.Fatalf("unsupported command returned success: %#v", args)
		}
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("unsupported commands reached runner: %#v", runner.Calls)
	}
}

func TestCurrentTarget(t *testing.T) {
	tests := map[string]string{
		"windows/amd64": "windows-amd64",
		"windows/arm64": "windows-arm64",
		"darwin/amd64":  "darwin-amd64",
		"darwin/arm64":  "darwin-arm64",
		"linux/amd64":   "linux-amd64",
		"linux/arm64":   "linux-arm64",
	}
	for input, expected := range tests {
		parts := strings.Split(input, "/")
		target, err := CurrentTarget(parts[0], parts[1])
		if err != nil || target != expected {
			t.Fatalf("target = %q, err = %v, want %q", target, err, expected)
		}
	}
	if _, err := CurrentTarget("plan9", "amd64"); err == nil {
		t.Fatal("unsupported runtime target accepted")
	}
}

func TestNewAndDefaultRunnerRemainFailOpen(t *testing.T) {
	created := New()
	if created.Runner == nil || created.ExecutablePath == nil || created.Target == "" {
		t.Fatalf("New returned incomplete bridge: %#v", created)
	}

	app, _, _, _ := bridgeFixture(t)
	app.Runner = nil
	if exitCode := app.Run(
		context.Background(),
		hookArguments("codex", "cli"),
		bytes.NewReader([]byte(`{}`)),
		io.Discard,
		io.Discard,
	); exitCode != 0 {
		t.Fatalf("default runner failure returned %d", exitCode)
	}
}
