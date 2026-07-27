package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/installstate"
	"github.com/liming0791/agentbell/core/internal/settings"
)

func doctorTestEnvironment(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("PATH", "")
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config", "config.json"))
	t.Setenv("AGENTBELL_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("AGENTBELL_LOG_DIR", filepath.Join(root, "logs"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(root, "kimi"))
	t.Setenv("OPENCODE_CONFIG_DIR", filepath.Join(root, "opencode"))
	return root
}

func runDoctorForTest(t *testing.T, asJSON bool) string {
	t.Helper()
	arguments := []string{"doctor"}
	if asJSON {
		arguments = append(arguments, "--json")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(
		arguments,
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("doctor failed: code=%d stderr=%s", code, stderr.String())
	}
	return stdout.String()
}

func TestDoctorMissingSidecarsReturnsStableJSONContract(t *testing.T) {
	doctorTestEnvironment(t)
	raw := runDoctorForTest(t, true)

	var report map[string]any
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	if report["schemaVersion"] != float64(1) {
		t.Fatalf("schemaVersion = %#v", report["schemaVersion"])
	}
	for _, name := range []string{
		"healthy",
		"version",
		"platform",
		"architecture",
		"config",
		"settings",
		"larkCli",
		"install",
		"plugin",
		"secretStore",
		"queue",
		"adapters",
		"relay",
	} {
		if _, exists := report[name]; !exists {
			t.Fatalf("doctor contract omitted %q: %s", name, raw)
		}
	}
	configStatus := report["config"].(map[string]any)
	settingsStatus := report["settings"].(map[string]any)
	installStatus := report["install"].(map[string]any)
	if configStatus["status"] != "missing" ||
		settingsStatus["status"] != "legacy-compatible" ||
		installStatus["mode"] != "legacy" {
		t.Fatalf("missing sidecar status is unstable: %s", raw)
	}

	actualShape := doctorJSONShape(report)
	goldenPath := filepath.Join("testdata", "doctor-contract.golden.json")
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var expectedShape any
	if err := json.Unmarshal(golden, &expectedShape); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualShape, expectedShape) {
		actual, _ := json.MarshalIndent(actualShape, "", "  ")
		t.Fatalf(
			"doctor JSON contract changed\nactual:\n%s\nexpected:\n%s",
			actual,
			golden,
		)
	}
}

func TestDoctorReportsValidatedActiveRuntimeWithoutPathsOrChecksums(
	t *testing.T,
) {
	resolved, corePath, bridgePath, state := activeRuntimeFixture(t)
	state.PreviousVersion = "0.2.9"
	if err := installstate.NewStore(installstate.OSFileSystem{}).Save(
		resolved.DataDir,
		state,
	); err != nil {
		t.Fatal(err)
	}
	metadata := installMetadata{
		SchemaVersion:   1,
		Version:         state.ActiveVersion,
		Target:          state.Target,
		Checksum:        state.Checksum,
		BridgeChecksum:  strings.Repeat("c", 64),
		SignatureStatus: "technical-preview",
		TransactionID:   "install-tx-differs-from-activation",
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(filepath.Dir(corePath), "install.json"),
		encoded,
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	raw := runDoctorForTest(t, true)
	var report struct {
		Install map[string]any `json:"install"`
	}
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	if report.Install["activeVersion"] != "0.3.0" ||
		report.Install["previousVersion"] != "0.2.9" ||
		report.Install["generation"] != float64(17) ||
		report.Install["checksumStatus"] != "verified" ||
		report.Install["stableBridgeStatus"] != "verified" ||
		report.Install["signatureStatus"] != "technical-preview" {
		t.Fatalf("active install summary = %s", raw)
	}
	for _, forbidden := range []string{
		resolved.DataDir,
		resolved.StateDir,
		corePath,
		bridgePath,
		state.Checksum,
		state.TransactionID,
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("doctor leaked install detail %q: %s", forbidden, raw)
		}
	}
}

func TestDoctorInvalidSidecarsReturnStableCodesWithoutFailing(t *testing.T) {
	root := doctorTestEnvironment(t)
	configDirectory := filepath.Dir(os.Getenv("AGENTBELL_CONFIG"))
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(configDirectory, "settings.json"),
		[]byte(`{"prompt":"settings-secret"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	activePath, err := installstate.ActiveStatePath(os.Getenv("AGENTBELL_DATA_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(activePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		activePath,
		[]byte(`{"token":"active-secret"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	raw := runDoctorForTest(t, true)
	var report struct {
		Settings map[string]any   `json:"settings"`
		Install  map[string]any   `json:"install"`
		Adapters []map[string]any `json:"adapters"`
	}
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	if report.Settings["errorCode"] != "settings_invalid" ||
		report.Install["errorCode"] != "active_state_invalid" {
		t.Fatalf("invalid sidecars were not summarized: %s", raw)
	}
	if len(report.Adapters) != len(supportedAdapterIDs) ||
		report.Adapters[0]["status"] != "runtime-unavailable" {
		t.Fatalf("adapter diagnostics were not fail-safe: %s", raw)
	}
	for _, forbidden := range []string{root, "settings-secret", "active-secret"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("invalid sidecar leaked %q: %s", forbidden, raw)
		}
	}
}

func TestDoctorStrictlyRedactsConfigurationAndRemoteSecrets(t *testing.T) {
	root := doctorTestEnvironment(t)
	configPath := os.Getenv("AGENTBELL_CONFIG")
	larkPath := filepath.Join(root, "token-secret", "lark-cli")
	if err := os.MkdirAll(filepath.Dir(larkPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(larkPath, []byte("secret executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	value := config.Config{
		DefaultChannel: "primary",
		LarkCLIPath:    larkPath,
		Notifications: config.Notifications{
			Events:       []string{"task.completed"},
			PrivacyLevel: "metadata-only",
		},
		Channels: []config.Channel{{
			ID: "primary", Name: "private", Type: "feishu",
			ChatID: "oc_private_chat_secret", As: "bot",
		}},
	}
	if err := config.Save(configPath, &value); err != nil {
		t.Fatal(err)
	}
	settingsValue := defaultSettings(value)
	settingsValue.Templates[0].Body = "prompt-secret-body {{event}}"
	if err := settings.Save(
		filepath.Join(filepath.Dir(configPath), "settings.json"),
		&settingsValue,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(filepath.Dir(configPath), "remote.json"),
		[]byte(`{
		  "endpoint":"https://token-secret@example.invalid/v1/events",
		  "privateKey":"PRIVATE KEY SECRET",
		  "body":"raw-hook-secret"
		}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(filepath.Dir(configPath), "host-connectors.json"),
		[]byte(`{"host":"private-host.example","prompt":"do not expose"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	raw := runDoctorForTest(t, true)
	for _, forbidden := range []string{
		root,
		larkPath,
		"oc_private_chat_secret",
		"prompt-secret-body",
		"token-secret@example.invalid",
		"PRIVATE KEY SECRET",
		"raw-hook-secret",
		"private-host.example",
		"do not expose",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("doctor leaked %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, `"errorCode": "connector_registry_invalid"`) {
		t.Fatalf("doctor did not replace remote details with a stable code: %s", raw)
	}
}

func TestDoctorHumanOutputIncludesRelayHealthSummary(t *testing.T) {
	root := doctorTestEnvironment(t)
	output := runDoctorForTest(t, false)
	if !strings.Contains(output, "Relay: ok (0 connectors, unconfigured)") {
		t.Fatalf("human doctor omitted relay health: %s", output)
	}
	if strings.Contains(output, root) {
		t.Fatalf("human doctor exposed a full path: %s", output)
	}
}

func doctorJSONShape(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for name, child := range typed {
			result[name] = doctorJSONShape(child)
		}
		return result
	case []any:
		result := map[string]any{
			"type":   "array",
			"length": float64(len(typed)),
		}
		if len(typed) != 0 {
			result["item"] = doctorJSONShape(typed[0])
		}
		return result
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}
