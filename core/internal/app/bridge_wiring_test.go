package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/adapter"
	"github.com/liming0791/agentbell/core/internal/bridge"
	"github.com/liming0791/agentbell/core/internal/installstate"
	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/service"
)

func activeRuntimeFixture(
	t *testing.T,
) (paths.Paths, string, string, installstate.ActiveState) {
	t.Helper()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	stateDir := filepath.Join(root, "state")
	t.Setenv("AGENTBELL_DATA_DIR", dataRoot)
	t.Setenv("AGENTBELL_STATE_DIR", stateDir)
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	t.Setenv("AGENTBELL_LOG_DIR", filepath.Join(root, "logs"))
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, ".claude"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(root, ".kimi-code"))

	target, err := bridge.CurrentTarget(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	coreBytes := []byte("active AgentBell Core")
	bridgeBytes := []byte("stable bridge")
	state := installstate.ActiveState{
		SchemaVersion:  installstate.SchemaVersion,
		Generation:     17,
		ActiveVersion:  "0.3.0",
		Target:         target,
		Checksum:       installstate.SHA256(coreBytes),
		BridgeChecksum: installstate.SHA256(bridgeBytes),
		TransactionID:  "tx-app-wiring",
	}
	corePath, err := installstate.ManagedCorePath(dataRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(corePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corePath, coreBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installstate.NewStore(installstate.OSFileSystem{}).Save(
		dataRoot,
		state,
	); err != nil {
		t.Fatal(err)
	}

	bridgeName := "agentbell-bridge"
	if runtime.GOOS == "windows" {
		bridgeName += ".exe"
	}
	bridgePath := filepath.Join(
		dataRoot,
		"bin",
		"bridge",
		"v1",
		bridgeName,
	)
	if err := os.MkdirAll(filepath.Dir(bridgePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bridgePath, bridgeBytes, 0o700); err != nil {
		t.Fatal(err)
	}

	resolved, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	return resolved, corePath, bridgePath, state
}

func TestAdapterFactoryUsesActiveCoreAndStableBridge(t *testing.T) {
	resolved, corePath, bridgePath, state := activeRuntimeFixture(t)

	tests := []struct {
		id    string
		check func(t *testing.T, value cliAdapter)
	}{
		{
			id: "codex",
			check: func(t *testing.T, value cliAdapter) {
				selected := value.(*adapter.CodexAdapter)
				assertActiveAdapter(
					t,
					selected.Executable,
					selected.BridgeExecutable,
					selected.ActiveGeneration,
					corePath,
					bridgePath,
					state.Generation,
				)
			},
		},
		{
			id: "claude-code",
			check: func(t *testing.T, value cliAdapter) {
				selected := value.(*adapter.ClaudeAdapter)
				assertActiveAdapter(
					t,
					selected.Executable,
					selected.BridgeExecutable,
					selected.ActiveGeneration,
					corePath,
					bridgePath,
					state.Generation,
				)
			},
		},
		{
			id: "kimi-code",
			check: func(t *testing.T, value cliAdapter) {
				selected := value.(*adapter.KimiAdapter)
				assertActiveAdapter(
					t,
					selected.Executable,
					selected.BridgeExecutable,
					selected.ActiveGeneration,
					corePath,
					bridgePath,
					state.Generation,
				)
			},
		},
		{
			id: "opencode",
			check: func(t *testing.T, value cliAdapter) {
				selected := value.(*adapter.OpenCodeAdapter)
				if selected.Executable != corePath {
					t.Fatalf(
						"non-bridge adapter executable = %q, want active Core %q",
						selected.Executable,
						corePath,
					)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			selected, err := adapterForID(test.id, resolved)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, selected)
		})
	}
}

func TestAdapterFactoryRejectsBrokenActiveRuntime(t *testing.T) {
	resolved, _, bridgePath, _ := activeRuntimeFixture(t)
	if err := os.Remove(bridgePath); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterForID("codex", resolved); err == nil {
		t.Fatal("missing stable bridge silently fell back to a direct Core hook")
	}
}

func TestServiceRestartUsesStableBridgeRuntime(t *testing.T) {
	_, _, bridgePath, _ := activeRuntimeFixture(t)
	root := t.TempDir()
	runner := &appServiceRunner{}
	manager := &service.Manager{
		GOOS:       "darwin",
		Executable: filepath.Join(root, "legacy-agentbell"),
		HomeDir:    filepath.Join(root, "home"),
		LogDir:     filepath.Join(root, "logs"),
		UID:        "501",
		Runner:     runner,
	}
	definitionPath := filepath.Join(
		manager.HomeDir,
		"Library",
		"LaunchAgents",
		"com.agentbell.service.plist",
	)
	if err := os.MkdirAll(filepath.Dir(definitionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definitionPath, []byte("installed"), 0o600); err != nil {
		t.Fatal(err)
	}

	originalNewServiceManager := newServiceManager
	newServiceManager = func(string, string) (*service.Manager, error) {
		return manager, nil
	}
	t.Cleanup(func() {
		newServiceManager = originalNewServiceManager
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		[]string{"service", "restart", "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("service restart failed: %s", stderr.String())
	}
	var result service.ManagerResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if manager.ServiceMode != service.ServiceModeBridge ||
		manager.BridgeExecutable != bridgePath {
		t.Fatalf(
			"service runtime = (%q, %q), want bridge %q",
			manager.ServiceMode,
			manager.BridgeExecutable,
			bridgePath,
		)
	}
	if !result.Running || runner.calls != 2 {
		t.Fatalf("restart was not verified: %#v, calls=%d", result, runner.calls)
	}
}

func TestBridgeDoctorReportsValidatedActiveRuntime(t *testing.T) {
	resolved, corePath, bridgePath, state := activeRuntimeFixture(t)
	serviceBytes := []byte("pinned service AgentBell Core")
	state.ServiceVersion = "0.3.1"
	state.ServiceChecksum = installstate.SHA256(serviceBytes)
	serviceState := state
	serviceState.ActiveVersion = state.ServiceVersion
	serviceState.PreviousVersion = ""
	serviceState.Checksum = state.ServiceChecksum
	serviceState.ServiceVersion = ""
	serviceState.ServiceChecksum = ""
	serviceCorePath, err := installstate.ManagedCorePath(
		resolved.DataDir,
		serviceState,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(serviceCorePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serviceCorePath, serviceBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installstate.NewStore(installstate.OSFileSystem{}).Save(
		resolved.DataDir,
		state,
	); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(
		filepath.Dir(corePath),
		"install.json",
	)
	metadata := `{
	  "schemaVersion": 1,
	  "version": "0.3.0",
	  "target": "` + state.Target + `",
	  "checksum": "` + state.Checksum + `",
	  "bridgeChecksum": "` + installstate.SHA256([]byte("stable bridge")) + `",
	  "signatureStatus": "technical-preview",
	  "transactionId": "tx-app-wiring"
	}`
	if err := os.WriteFile(metadataPath, []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	serviceMetadata := `{
	  "schemaVersion": 1,
	  "version": "0.3.1",
	  "target": "` + state.Target + `",
	  "checksum": "` + state.ServiceChecksum + `",
	  "bridgeChecksum": "` + state.BridgeChecksum + `",
	  "signatureStatus": "technical-preview",
	  "transactionId": "tx-app-wiring"
	}`
	if err := os.WriteFile(
		filepath.Join(filepath.Dir(serviceCorePath), "install.json"),
		[]byte(serviceMetadata),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		[]string{"bridge", "doctor", "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("bridge doctor failed: %s", stderr.String())
	}
	var result bridgeDoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Healthy ||
		result.Mode != "active" ||
		result.DataRoot != resolved.DataDir ||
		result.CorePath != corePath ||
		result.ServiceCorePath != serviceCorePath ||
		result.BridgePath != bridgePath ||
		result.Version != state.ActiveVersion ||
		result.ServiceVersion != state.ServiceVersion ||
		result.Generation != state.Generation ||
		result.SignatureStatus != "technical-preview" {
		t.Fatalf("unexpected bridge doctor report: %#v", result)
	}
}

func assertActiveAdapter(
	t *testing.T,
	executable,
	bridgeExecutable string,
	generation uint64,
	wantCore,
	wantBridge string,
	wantGeneration uint64,
) {
	t.Helper()
	if executable != wantCore ||
		bridgeExecutable != wantBridge ||
		generation != wantGeneration {
		t.Fatalf(
			"adapter runtime = (%q, %q, %d), want (%q, %q, %d)",
			executable,
			bridgeExecutable,
			generation,
			wantCore,
			wantBridge,
			wantGeneration,
		)
	}
}
