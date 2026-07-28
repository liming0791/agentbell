package remoteconfig

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func pathRef(platform, value string) PathRef {
	return PathRef{Platform: platform, Value: value}
}

func validRemote(connector Connector) RemoteConfig {
	return RemoteConfig{
		Version:        Version,
		MinCoreVersion: "2.0.0",
		TeamID:         "team-main",
		OriginID:       "origin-laptop",
		Runtime:        "wsl",
		Outbox: Outbox{
			Path:     pathRef("linux", "/home/test/.local/state/agentbell/outbox"),
			MaxBytes: 64 << 20,
		},
		Connector: connector,
		PrivateKeyRef: PrivateKeyRef{
			Store: "secret-service",
			ID:    "agentbell/device/origin-laptop",
		},
	}
}

func validWSLConnector() Connector {
	return Connector{
		Type: "wsl",
		WSL: &WSLConnector{
			Distribution:     "Ubuntu-24.04",
			HostExecutable:   pathRef("windows", `C:\Windows\System32\wsl.exe`),
			RemoteExecutable: pathRef("linux", "/usr/local/bin/agentbell"),
		},
	}
}

func validRelay() RelayConfig {
	publicKey := make([]byte, ed25519.PublicKeySize)
	for index := range publicKey {
		publicKey[index] = byte(index + 1)
	}
	return RelayConfig{
		Version:        Version,
		MinCoreVersion: "2.0.0",
		Listener: Listener{
			Enabled: true,
			Address: "127.0.0.1:18892",
		},
		Peers: []Peer{{
			ID:              "peer-laptop",
			TeamID:          "team-main",
			OriginID:        "origin-laptop",
			PublicKey:       base64.RawURLEncoding.EncodeToString(publicKey),
			Scopes:          []string{"ingest"},
			AllowedSources:  []string{"codex", "claude"},
			AllowedRuntimes: []string{"wsl", "ssh"},
			Revoked:         false,
		}},
	}
}

func TestMissingSidecarsAreCompatible(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := LoadRemote(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remote missing error = %v", err)
	}
	if _, err := LoadRelay(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("relay missing error = %v", err)
	}
}

func TestRemoteConnectorModelsRoundTripAtomically(t *testing.T) {
	tests := map[string]struct {
		runtime   string
		connector Connector
	}{
		"wsl": {
			runtime:   "wsl",
			connector: validWSLConnector(),
		},
		"ssh": {
			runtime: "ssh",
			connector: Connector{
				Type: "ssh",
				SSH: &SSHConnector{
					Host:             "build.example.com",
					Port:             22,
					User:             "agentbell",
					HostExecutable:   pathRef("darwin", "/usr/bin/ssh"),
					KnownHostsFile:   pathRef("darwin", "/Users/test/.ssh/known_hosts"),
					RemoteExecutable: pathRef("linux", "/usr/local/bin/agentbell"),
				},
			},
		},
		"container": {
			runtime: "container",
			connector: Connector{
				Type: "container",
				Container: &ContainerConnector{
					Runtime:          "docker",
					HostExecutable:   pathRef("linux", "/usr/bin/docker"),
					ContainerID:      "agentbell-worker-1",
					RemoteExecutable: pathRef("linux", "/usr/local/bin/agentbell"),
				},
			},
		},
		"https": {
			runtime: "ssh",
			connector: Connector{
				Type: "https",
				HTTPS: &HTTPSConnector{
					Endpoint:   "https://relay.example.com/v1/events",
					PinnedSPKI: strings.Repeat("a", 64),
				},
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "nested", "remote.json")
			value := validRemote(test.connector)
			value.Runtime = test.runtime
			if test.runtime != "wsl" {
				value.Outbox.Path = pathRef("windows", `C:\Users\test\AppData\Local\AgentBell\outbox`)
				value.PrivateKeyRef = PrivateKeyRef{
					Store:                    "file",
					Path:                     pointerPath(pathRef("windows", `C:\Users\test\.agentbell\device.key`)),
					FileFallbackAcknowledged: true,
				}
			}
			if err := SaveRemote(path, &value); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadRemote(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Connector.Type != test.connector.Type ||
				loaded.TeamID != value.TeamID ||
				loaded.PrivateKeyRef.Store != value.PrivateKeyRef.Store {
				t.Fatalf("remote round trip changed data: %#v", loaded)
			}
			if err := SaveRemote(path, &loaded); err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(first) != string(second) {
				t.Fatalf("remote serialization is unstable:\n%s\n%s", first, second)
			}
			assertPrivateModes(t, filepath.Dir(path), path)
		})
	}
}

func TestRemoteRejectsUnknownSecretsCommandsAndUnsupportedCloud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote.json")
	rawCases := []string{
		`{"version":1,"minCoreVersion":"2.0.0","teamId":"team-main","originId":"origin-main","runtime":"wsl","outbox":{"path":{"platform":"linux","value":"/tmp/outbox"},"maxBytes":1048576},"connector":{"type":"wsl","wsl":{"distribution":"Ubuntu","hostExecutable":{"platform":"windows","value":"C:\\Windows\\wsl.exe"},"remoteExecutable":{"platform":"linux","value":"/bin/agentbell"},"command":"rm -rf /"}},"privateKeyRef":{"store":"secret-service","id":"agentbell/key"}}`,
		`{"version":1,"minCoreVersion":"2.0.0","teamId":"team-main","originId":"origin-main","runtime":"wsl","outbox":{"path":{"platform":"linux","value":"/tmp/outbox"},"maxBytes":1048576},"connector":{"type":"wsl","wsl":{"distribution":"Ubuntu","hostExecutable":{"platform":"windows","value":"C:\\Windows\\wsl.exe"},"remoteExecutable":{"platform":"linux","value":"/bin/agentbell"}}},"privateKeyRef":{"store":"secret-service","id":"agentbell/key","privateKey":"secret"}}`,
	}
	for _, raw := range rawCases {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRemote(path); err == nil {
			t.Fatalf("dangerous remote document was accepted: %s", raw)
		}
	}

	value := validRemote(Connector{
		Type: "vendor-cloud",
		VendorCloud: &VendorCloudConnector{
			Provider:   "kimi-work",
			Capability: "signed-outbound-webhook-v1",
			Endpoint:   "https://cloud.example.com/events",
		},
	})
	value.Runtime = "vendor-cloud"
	if err := SaveRemote(path, &value); !errors.Is(err, ErrVendorCloudUnsupported) {
		t.Fatalf("vendor cloud gate = %v", err)
	}
}

func TestRemoteValidationRejectsDangerousPathsHostsAndUnion(t *testing.T) {
	tests := map[string]func(*RemoteConfig){
		"path traversal": func(value *RemoteConfig) {
			value.Outbox.Path = pathRef("linux", "/var/lib/../secret")
		},
		"host injection": func(value *RemoteConfig) {
			value.Runtime = "ssh"
			value.Connector = Connector{
				Type: "ssh",
				SSH: &SSHConnector{
					Host:             "host;shutdown",
					Port:             22,
					User:             "agent",
					HostExecutable:   pathRef("linux", "/usr/bin/ssh"),
					KnownHostsFile:   pathRef("linux", "/home/a/.ssh/known_hosts"),
					RemoteExecutable: pathRef("linux", "/usr/bin/agentbell"),
				},
			}
		},
		"multiple union arms": func(value *RemoteConfig) {
			value.Connector.SSH = &SSHConnector{}
		},
		"unacknowledged file key": func(value *RemoteConfig) {
			value.PrivateKeyRef = PrivateKeyRef{
				Store: "file",
				Path:  pointerPath(pathRef("linux", "/home/a/device.key")),
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validRemote(validWSLConnector())
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRelayListenerSecurityAndPeerValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.json")
	value := validRelay()
	if err := SaveRelay(path, &value); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRelay(path)
	if err != nil || len(loaded.Peers) != 1 {
		t.Fatalf("relay round trip: %#v err=%v", loaded, err)
	}
	assertPrivateModes(t, filepath.Dir(path), path)

	nonLoopback := validRelay()
	nonLoopback.Listener.Address = "0.0.0.0:18892"
	if err := nonLoopback.Validate(); err == nil {
		t.Fatal("non-loopback listener without TLS/tunnel was accepted")
	}
	nonLoopback.Listener.TLS = &ListenerTLS{
		CertFile: pathRef("linux", "/etc/agentbell/tls/cert.pem"),
		KeyFile:  pathRef("linux", "/etc/agentbell/tls/key.pem"),
	}
	if err := nonLoopback.Validate(); err != nil {
		t.Fatalf("TLS listener rejected: %v", err)
	}
	nonLoopback.Listener.TLS = nil
	nonLoopback.Listener.SSHTunnel = true
	if err := nonLoopback.Validate(); err != nil {
		t.Fatalf("explicit SSH tunnel rejected: %v", err)
	}
}

func TestRelayRejectsDuplicatePeersInvalidKeysScopesAndUnknownFields(t *testing.T) {
	tests := map[string]func(*RelayConfig){
		"duplicate id": func(value *RelayConfig) {
			value.Peers = append(value.Peers, value.Peers[0])
		},
		"invalid public key": func(value *RelayConfig) {
			value.Peers[0].PublicKey = "not-ed25519"
		},
		"invalid scope": func(value *RelayConfig) {
			value.Peers[0].Scopes = []string{"admin"}
		},
		"invalid source": func(value *RelayConfig) {
			value.Peers[0].AllowedSources = []string{"unknown-agent"}
		},
		"invalid runtime": func(value *RelayConfig) {
			value.Peers[0].AllowedRuntimes = []string{"public-internet"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validRelay()
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	path := filepath.Join(t.TempDir(), "relay.json")
	raw, err := os.ReadFile(writeRelayFixture(t, path, validRelay()))
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.Replace(string(raw), `"version": 1`, `"version": 1, "command": "listen"`, 1)
	if err := os.WriteFile(path, []byte(withUnknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRelay(path); err == nil {
		t.Fatal("unknown relay field was accepted")
	}

	if err := SaveRelay(path, pointerRelay(validRelay())); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	peers := document["peers"].([]any)
	delete(peers[0].(map[string]any), "revoked")
	missingRevoked, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, missingRevoked, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRelay(path); err == nil ||
		!strings.Contains(err.Error(), "revoked") {
		t.Fatalf("missing explicit revoked field was accepted: %v", err)
	}
}

func TestInvalidSaveDoesNotReplaceExistingSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote.json")
	valid := validRemote(validWSLConnector())
	if err := SaveRemote(path, &valid); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Version = 99
	if err := SaveRemote(path, &invalid); err == nil {
		t.Fatal("invalid save succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("invalid save replaced the existing sidecar")
	}

	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := SaveRemote(path, &valid); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if _, err := LoadRemote(path); err != nil {
		t.Fatalf("concurrent atomic saves corrupted the sidecar: %v", err)
	}
}

func TestCreateRemoteNeverOverwritesAcrossConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote.json")
	value := validRemote(validWSLConnector())
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- CreateRemote(
				context.Background(),
				path,
				value,
				false,
			)
		}()
	}
	wait.Wait()
	close(results)
	created := 0
	for err := range results {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrRemoteExists):
		default:
			t.Fatalf("create error = %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("successful creates = %d", created)
	}
	if _, err := LoadRemote(path); err != nil {
		t.Fatal(err)
	}
}

func pointerPath(value PathRef) *PathRef {
	return &value
}

func pointerRelay(value RelayConfig) *RelayConfig {
	return &value
}

func assertPrivateModes(t *testing.T, directory, path string) {
	t.Helper()
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !directoryInfo.IsDir() || !fileInfo.Mode().IsRegular() {
		t.Fatalf(
			"sidecar paths are not regular: directory=%v file=%v",
			directoryInfo.Mode(),
			fileInfo.Mode(),
		)
	}
	if runtime.GOOS == "windows" {
		directoryLink, directoryErr := os.Lstat(directory)
		fileLink, fileErr := os.Lstat(path)
		if directoryErr != nil ||
			fileErr != nil ||
			directoryLink.Mode()&os.ModeSymlink != 0 ||
			fileLink.Mode()&os.ModeSymlink != 0 {
			t.Fatalf(
				"Windows sidecar path is not an inherited-DACL regular path",
			)
		}
		return
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %o, want 700", directoryInfo.Mode().Perm())
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", fileInfo.Mode().Perm())
	}
}

func writeRelayFixture(t *testing.T, path string, value RelayConfig) string {
	t.Helper()
	if err := SaveRelay(path, &value); err != nil {
		t.Fatal(err)
	}
	return path
}
