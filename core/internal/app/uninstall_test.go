package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/installstate"
	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
	"github.com/liming0791/agentbell/core/internal/service"
)

type recordingUninstallCredentialStore struct {
	references []remoteconfig.PrivateKeyRef
	err        error
}

func (store *recordingUninstallCredentialStore) Delete(
	_ context.Context,
	reference remoteconfig.PrivateKeyRef,
) error {
	store.references = append(store.references, reference)
	return store.err
}

func TestUninstallRemoteAssetPreflightIsRedactedAndComplete(t *testing.T) {
	resolved := uninstallTestPaths(t)
	remoteValue := validUninstallRemoteConfig(t, resolved)
	if err := remoteconfig.SaveRemote(
		filepath.Join(filepath.Dir(resolved.ConfigFile), "remote.json"),
		&remoteValue,
	); err != nil {
		t.Fatal(err)
	}
	hostValue := remoteconfig.HostConnectors{
		Version:        remoteconfig.Version,
		MinCoreVersion: "0.3.0",
		Connectors: []remoteconfig.HostConnector{
			validUninstallHostConnector(),
		},
	}
	if err := remoteconfig.SaveHostConnectors(
		filepath.Join(
			filepath.Dir(resolved.ConfigFile),
			"host-connectors.json",
		),
		&hostValue,
	); err != nil {
		t.Fatal(err)
	}
	relayValue := validUninstallRelay()
	if err := remoteconfig.SaveRelay(
		filepath.Join(filepath.Dir(resolved.ConfigFile), "relay.json"),
		&relayValue,
	); err != nil {
		t.Fatal(err)
	}
	activeValue := installstate.ActiveState{
		SchemaVersion:  installstate.SchemaVersion,
		Generation:     7,
		ActiveVersion:  "0.3.0-rc.1",
		Target:         runtime.GOOS + "-" + runtime.GOARCH,
		Checksum:       strings.Repeat("a", 64),
		BridgeChecksum: strings.Repeat("b", 64),
		TransactionID:  "uninstall-preflight",
	}
	if err := installstate.NewStore(nil).Save(
		resolved.DataDir,
		activeValue,
	); err != nil {
		t.Fatal(err)
	}

	actual := inspectUninstallRemoteAssets(resolved)
	if !actual.RemoteConfig.Healthy ||
		!actual.HostConnectors.Healthy ||
		!actual.Relay.Healthy ||
		!actual.ActiveInstall.Healthy ||
		actual.HostConnectorCount != 1 ||
		actual.RelayPeerCount != 1 ||
		actual.CredentialBackend != "file" ||
		actual.CredentialAction != "preserved" ||
		actual.ActiveVersion != activeValue.ActiveVersion ||
		actual.ActiveGeneration != activeValue.Generation {
		t.Fatalf("unexpected uninstall remote assets: %#v", actual)
	}
	encodedBytes, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(encodedBytes)
	for _, secret := range []string{
		"team-main",
		"origin-laptop",
		"peer-laptop",
		remoteValue.PrivateKeyRef.Path.Value,
	} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("uninstall preflight leaked %q: %s", secret, encoded)
		}
	}
}

func TestProductUninstallRequiresExplicitCredentialConfirmation(t *testing.T) {
	resolved := uninstallTestPaths(t)
	remoteValue := validUninstallRemoteConfig(t, resolved)
	if err := remoteconfig.SaveRemote(
		filepath.Join(filepath.Dir(resolved.ConfigFile), "remote.json"),
		&remoteValue,
	); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(
		[]string{"uninstall", "--delete-remote-credential", "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code == 0 ||
		!strings.Contains(
			stderr.String(),
			"requires --confirm-delete-remote-credential",
		) {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestProductUninstallDeletesCredentialOnlyAfterConfirmation(t *testing.T) {
	resolved := uninstallTestPaths(t)
	remoteValue := validUninstallRemoteConfig(t, resolved)
	if err := remoteconfig.SaveRemote(
		filepath.Join(filepath.Dir(resolved.ConfigFile), "remote.json"),
		&remoteValue,
	); err != nil {
		t.Fatal(err)
	}
	runner := &appServiceRunner{}
	manager := &service.Manager{
		GOOS:       "darwin",
		Executable: filepath.Join(t.TempDir(), "agentbell"),
		HomeDir:    t.TempDir(),
		LogDir:     t.TempDir(),
		UID:        "501",
		Runner:     runner,
	}
	store := &recordingUninstallCredentialStore{}
	originalManager := newServiceManager
	originalStore := newUninstallCredentialStore
	newServiceManager = func(string, string) (*service.Manager, error) {
		return manager, nil
	}
	newUninstallCredentialStore = func(
		string,
	) (uninstallCredentialStore, error) {
		return store, nil
	}
	t.Cleanup(func() {
		newServiceManager = originalManager
		newUninstallCredentialStore = originalStore
	})

	var dryRunOutput bytes.Buffer
	var dryRunError bytes.Buffer
	if code := Run(
		[]string{
			"uninstall",
			"--dry-run",
			"--delete-remote-credential",
			"--confirm-delete-remote-credential",
			"--json",
		},
		strings.NewReader(""),
		&dryRunOutput,
		&dryRunError,
	); code != 0 {
		t.Fatalf("uninstall dry-run failed: %s", dryRunError.String())
	}
	if len(store.references) != 0 {
		t.Fatalf("dry-run deleted credential: %#v", store.references)
	}
	var dryRunReport productUninstallReport
	if err := json.Unmarshal(dryRunOutput.Bytes(), &dryRunReport); err != nil {
		t.Fatal(err)
	}
	if dryRunReport.RemoteAssets.CredentialAction != "would-delete" {
		t.Fatalf("unexpected dry-run report: %#v", dryRunReport.RemoteAssets)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(
		[]string{
			"uninstall",
			"--delete-remote-credential",
			"--confirm-delete-remote-credential",
			"--json",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("uninstall failed: %s", stderr.String())
	}
	if len(store.references) != 1 ||
		store.references[0].Path == nil ||
		store.references[0].Path.Value != remoteValue.PrivateKeyRef.Path.Value {
		t.Fatalf("credential deletion references = %#v", store.references)
	}
	var report productUninstallReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.RemoteAssets.CredentialAction != "deleted" {
		t.Fatalf("unexpected report: %#v", report.RemoteAssets)
	}
	if _, err := os.Stat(
		filepath.Join(filepath.Dir(resolved.ConfigFile), "remote.json"),
	); err != nil {
		t.Fatalf("remote metadata was not preserved: %v", err)
	}
}

func uninstallTestPaths(t *testing.T) paths.Paths {
	t.Helper()
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	resolved := paths.Paths{
		ConfigFile: filepath.Join(configRoot, "config.json"),
		DataDir:    filepath.Join(root, "data"),
		StateDir:   filepath.Join(root, "state"),
		LogDir:     filepath.Join(root, "logs"),
	}
	t.Setenv("HOME", root)
	t.Setenv("AGENTBELL_CONFIG", resolved.ConfigFile)
	t.Setenv("AGENTBELL_DATA_DIR", resolved.DataDir)
	t.Setenv("AGENTBELL_STATE_DIR", resolved.StateDir)
	t.Setenv("AGENTBELL_LOG_DIR", resolved.LogDir)
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, ".claude"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(root, ".kimi-code"))
	t.Setenv("OPENCODE_CONFIG_DIR", filepath.Join(root, ".config", "opencode"))
	t.Setenv("QODER_CONFIG_DIR", filepath.Join(root, ".qoder"))
	t.Setenv("QODERWORK_CONFIG_DIR", filepath.Join(root, ".qoderwork"))
	t.Setenv("TRAE_CONFIG_DIR", filepath.Join(root, ".trae"))
	return resolved
}

func validUninstallRemoteConfig(
	t *testing.T,
	resolved paths.Paths,
) remoteconfig.RemoteConfig {
	t.Helper()
	return remoteconfig.RemoteConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: "0.3.0",
		TeamID:         "team-main",
		OriginID:       "origin-laptop",
		Runtime:        "ssh",
		Outbox: remoteconfig.Outbox{
			Path: remoteconfig.PathRef{
				Platform: "linux",
				Value:    "/tmp/agentbell-outbox",
			},
			MaxBytes: 64 << 20,
		},
		Connector: remoteconfig.Connector{
			Type: "https",
			HTTPS: &remoteconfig.HTTPSConnector{
				Endpoint: "https://relay.example.com/v1/events",
			},
		},
		PrivateKeyRef: remoteconfig.PrivateKeyRef{
			Store: "file",
			Path: &remoteconfig.PathRef{
				Platform: runtime.GOOS,
				Value: filepath.Join(
					resolved.DataDir,
					"private",
					"device.key",
				),
			},
			FileFallbackAcknowledged: true,
		},
	}
}

func validUninstallHostConnector() remoteconfig.HostConnector {
	hostPlatform := runtime.GOOS
	hostExecutable := "/usr/bin/ssh"
	knownHosts := "/tmp/known_hosts"
	if runtime.GOOS == "windows" {
		hostExecutable = `C:\Windows\System32\OpenSSH\ssh.exe`
		knownHosts = `C:\Users\Test\.ssh\known_hosts`
	}
	return remoteconfig.HostConnector{
		ID:       "primary",
		TeamID:   "team-main",
		OriginID: "origin-primary",
		Runtime:  "ssh",
		Connector: remoteconfig.Connector{
			Type: "ssh",
			SSH: &remoteconfig.SSHConnector{
				Host: "build.example.com",
				Port: 22,
				User: "agentbell",
				HostExecutable: remoteconfig.PathRef{
					Platform: hostPlatform,
					Value:    hostExecutable,
				},
				KnownHostsFile: remoteconfig.PathRef{
					Platform: hostPlatform,
					Value:    knownHosts,
				},
				RemoteExecutable: remoteconfig.PathRef{
					Platform: "linux",
					Value:    "/usr/local/bin/agentbell",
				},
			},
		},
	}
}

func validUninstallRelay() remoteconfig.RelayConfig {
	publicKey := make([]byte, ed25519.PublicKeySize)
	for index := range publicKey {
		publicKey[index] = byte(index + 1)
	}
	return remoteconfig.RelayConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: "0.3.0",
		Listener:       remoteconfig.Listener{},
		Peers: []remoteconfig.Peer{{
			ID:              "peer-laptop",
			TeamID:          "team-main",
			OriginID:        "origin-laptop",
			PublicKey:       base64.RawURLEncoding.EncodeToString(publicKey),
			Scopes:          []string{"ingest"},
			AllowedSources:  []string{"codex"},
			AllowedRuntimes: []string{"ssh"},
		}},
	}
}
