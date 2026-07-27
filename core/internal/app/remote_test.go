package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remote"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
	"github.com/liming0791/agentbell/core/internal/secretstore"
)

func TestRemoteDrainEmptyOutboxUsesConfiguredCurrentPlatformPath(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	t.Setenv("AGENTBELL_CONFIG", configPath)
	value := remoteDrainConfig(filepath.Join(root, "outbox"))
	if err := remoteconfig.SaveRemote(
		filepath.Join(root, "remote.json"),
		&value,
	); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		[]string{"remote", "drain", "--stdio"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf(
			"code=%d stdout=%q stderr=%q",
			code,
			stdout.String(),
			stderr.String(),
		)
	}
}

func TestRemoteDrainRejectsWrongPlatformAndUnsafeArguments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	value := remoteDrainConfig(filepath.Join(root, "outbox"))
	if runtime.GOOS == "windows" {
		value.Outbox.Path = remoteconfig.PathRef{
			Platform: "linux",
			Value:    "/tmp/agentbell-outbox",
		}
	} else {
		value.Outbox.Path = remoteconfig.PathRef{
			Platform: "windows",
			Value:    `C:\AgentBell\outbox`,
		}
	}
	if err := remoteconfig.SaveRemote(
		filepath.Join(root, "remote.json"),
		&value,
	); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		{"remote"},
		{"remote", "drain"},
		{"remote", "drain", "--stdio", "extra"},
	} {
		var stderr bytes.Buffer
		if code := Run(
			arguments,
			strings.NewReader(""),
			&bytes.Buffer{},
			&stderr,
		); code == 0 {
			t.Fatalf("arguments %#v were accepted", arguments)
		}
	}
}

func TestRemoteEmitNormalizesSignsAndSpoolsMetadataOnly(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	statePath := filepath.Join(root, "state")
	outboxPath := filepath.Join(root, "outbox")
	keyPath := filepath.Join(root, "secrets", "device.key")
	t.Setenv("AGENTBELL_CONFIG", configPath)
	t.Setenv("AGENTBELL_STATE_DIR", statePath)
	value := remoteDrainConfig(outboxPath)
	value.PrivateKeyRef = remoteconfig.PrivateKeyRef{
		Store: "file",
		Path: &remoteconfig.PathRef{
			Platform: runtime.GOOS,
			Value:    keyPath,
		},
		FileFallbackAcknowledged: true,
	}
	if err := remoteconfig.SaveRemote(
		filepath.Join(root, "remote.json"),
		&value,
	); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyStore, err := secretstore.New(filepath.Join(statePath, "relay"))
	if err != nil {
		t.Fatal(err)
	}
	if err := keyStore.Put(
		context.Background(),
		value.PrivateKeyRef,
		privateKey,
	); err != nil {
		t.Fatal(err)
	}
	raw := `{
		"hook_event_name":"Stop",
		"session_id":"private-session",
		"turn_id":"private-turn",
		"summary":"private-summary"
	}`
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{
		"remote", "emit",
		"--adapter", "codex",
		"--surface", "cli",
		"--runtime", value.Runtime,
		"--stdin",
	}, strings.NewReader(raw), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{
		"remote", "emit",
		"--adapter", "codex",
		"--surface", "cli",
		"--runtime", value.Runtime,
		"--stdin",
	}, strings.NewReader(raw), &stdout, &stderr)
	if code != 0 {
		t.Fatalf(
			"duplicate code=%d stderr=%s",
			code,
			stderr.String(),
		)
	}
	outbox, err := relay.OpenOutbox(outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := outbox.Stats()
	if err != nil || stats.Total != 1 {
		t.Fatalf("duplicate outbox stats=%#v err=%v", stats, err)
	}
	item, err := outbox.Claim(
		testClock(),
		0,
	)
	if err != nil || item == nil {
		t.Fatalf("claim item=%#v err=%v", item, err)
	}
	envelope, err := relay.Decode(item.ExactBody)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Event.PrivacyLevel != event.PrivacyMetadataOnly ||
		envelope.Event.Runtime != value.Runtime ||
		envelope.Event.Summary != "" ||
		envelope.Event.CWD != "" {
		t.Fatalf("event = %#v", envelope.Event)
	}
	persisted, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"private-session",
		"private-turn",
		"private-summary",
		string(privateKey),
	} {
		if bytes.Contains(persisted, []byte(secret)) {
			t.Fatalf("outbox leaked %q", secret)
		}
	}
}

func TestRemoteEmitFailOpenDoesNotBlockAgent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "missing.json"))
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	var stderr bytes.Buffer
	code := Run([]string{
		"remote", "emit",
		"--adapter", "codex",
		"--surface", "cli",
		"--runtime", "ssh",
		"--stdin",
		"--fail-open",
	}, strings.NewReader(`{"hook_event_name":"Stop"}`), &bytes.Buffer{}, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "state", "relay")); !os.IsNotExist(err) {
		t.Fatalf("fail-open mutated relay state: %v", err)
	}
}

func TestRemoteTestQueuesFreshMetadataOnlyProbe(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	statePath := filepath.Join(root, "state")
	outboxPath := filepath.Join(root, "outbox")
	keyPath := filepath.Join(root, "secrets", "device.key")
	t.Setenv("AGENTBELL_CONFIG", configPath)
	t.Setenv("AGENTBELL_STATE_DIR", statePath)
	value := remoteDrainConfig(outboxPath)
	value.PrivateKeyRef = remoteconfig.PrivateKeyRef{
		Store: "file",
		Path: &remoteconfig.PathRef{
			Platform: runtime.GOOS,
			Value:    keyPath,
		},
		FileFallbackAcknowledged: true,
	}
	if err := remoteconfig.SaveRemote(
		filepath.Join(root, "remote.json"),
		&value,
	); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyStore, err := secretstore.New(filepath.Join(statePath, "relay"))
	if err != nil {
		t.Fatal(err)
	}
	if err := keyStore.Put(
		context.Background(),
		value.PrivateKeyRef,
		privateKey,
	); err != nil {
		t.Fatal(err)
	}

	var first bytes.Buffer
	var stderr bytes.Buffer
	for index := 0; index < 2; index++ {
		output := &bytes.Buffer{}
		if index == 0 {
			output = &first
		}
		code := Run([]string{
			"remote", "test",
			"--adapter", "codex",
			"--surface", "cli",
			"--json",
		}, strings.NewReader(""), output, &stderr)
		if code != 0 {
			t.Fatalf(
				"iteration=%d code=%d stderr=%s",
				index,
				code,
				stderr.String(),
			)
		}
	}
	var result struct {
		Queued   bool   `json:"queued"`
		OutboxID string `json:"outboxId"`
	}
	if err := json.Unmarshal(first.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Queued || result.OutboxID == "" {
		t.Fatalf("result = %#v", result)
	}
	outbox, err := relay.OpenOutbox(outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		item, err := outbox.Claim(testClock(), 0)
		if err != nil || item == nil {
			t.Fatalf("index=%d item=%#v err=%v", index, item, err)
		}
		envelope, err := relay.Decode(item.ExactBody)
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Event.Event != event.EventAgentInfo ||
			envelope.Event.Status != event.StatusInfo ||
			envelope.Event.PrivacyLevel != event.PrivacyMetadataOnly ||
			envelope.Event.Runtime != value.Runtime ||
			envelope.Event.Summary != "" ||
			envelope.Event.CWD != "" {
			t.Fatalf("event = %#v", envelope.Event)
		}
	}
}

func TestRemoteTestRejectsUnsafeArgumentsBeforeMutation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "missing.json"))
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	for _, arguments := range [][]string{
		{"remote", "test"},
		{"remote", "test", "--adapter", "codex"},
		{
			"remote", "test",
			"--adapter", "codex",
			"--surface", "cli",
			"extra",
		},
		{
			"remote", "test",
			"--adapter", "codex",
			"--surface", "cli",
			"--wait", "-1s",
		},
	} {
		if code := Run(
			arguments,
			strings.NewReader(""),
			&bytes.Buffer{},
			&bytes.Buffer{},
		); code == 0 {
			t.Fatalf("arguments %#v were accepted", arguments)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatalf("invalid test mutated state: %v", err)
	}
}

func TestWaitRemoteOutboxDistinguishesAckTimeoutAndDead(t *testing.T) {
	now := time.Now().UTC()
	outbox, err := relay.OpenOutbox(filepath.Join(t.TempDir(), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	body, signature := remoteSignedOutboxFixture(t, now)
	id, _, err := outbox.Enqueue(body, signature, now)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(10 * time.Millisecond)
		item, claimErr := outbox.Claim(time.Now().UTC(), time.Minute)
		if claimErr == nil && item != nil {
			_ = outbox.Ack(item, time.Now().UTC())
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	state, err := waitRemoteOutbox(ctx, outbox, id, time.Millisecond)
	if err != nil || state != relay.OutboxHistory {
		t.Fatalf("ack state=%q err=%v", state, err)
	}

	body, signature = remoteSignedOutboxFixture(
		t,
		now.Add(time.Second),
	)
	pendingID, _, err := outbox.Enqueue(
		body,
		signature,
		now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	timeoutContext, stopTimeout := context.WithTimeout(
		context.Background(),
		time.Millisecond,
	)
	defer stopTimeout()
	if _, err := waitRemoteOutbox(
		timeoutContext,
		outbox,
		pendingID,
		time.Millisecond,
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}

	item, err := outbox.Claim(now.Add(2*time.Second), time.Minute)
	if err != nil || item == nil {
		t.Fatalf("dead claim item=%#v err=%v", item, err)
	}
	if _, err := outbox.Nack(
		item,
		errors.New("offline"),
		now.Add(2*time.Second),
		[]time.Duration{time.Second},
	); err != nil {
		t.Fatal(err)
	}
	if state, err := waitRemoteOutbox(
		context.Background(),
		outbox,
		pendingID,
		time.Millisecond,
	); err == nil || state != relay.OutboxDead {
		t.Fatalf("dead state=%q err=%v", state, err)
	}
}

func TestRemoteConfigureHTTPSIsValidatedAtomicAndDryRunnable(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	t.Setenv("AGENTBELL_CONFIG", configPath)
	outboxPath := filepath.Join(root, "outbox")
	keyPath := filepath.Join(root, "secrets", "device.key")
	arguments := []string{
		"remote", "configure",
		"--team", "team-main",
		"--origin", "origin-main",
		"--runtime", "ssh",
		"--outbox", outboxPath,
		"--connector", "https",
		"--endpoint", "https://relay.example.com/v1/events",
		"--key-file", keyPath,
		"--acknowledge-file-fallback",
		"--json",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	dryRunArguments := append(
		append([]string(nil), arguments...),
		"--dry-run",
	)
	code := Run(
		dryRunArguments,
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("dry-run code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "remote.json")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote remote.json: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(arguments, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	value, err := remoteconfig.LoadRemote(filepath.Join(root, "remote.json"))
	if err != nil {
		t.Fatal(err)
	}
	if value.TeamID != "team-main" ||
		value.OriginID != "origin-main" ||
		value.Connector.Type != "https" ||
		value.PrivateKeyRef.Store != "file" {
		t.Fatalf("remote config = %#v", value)
	}
	if code := Run(
		arguments,
		strings.NewReader(""),
		&bytes.Buffer{},
		&stderr,
	); code == 0 {
		t.Fatal("configure overwrote an existing remote.json")
	}
}

func TestRemotePairStoresKeyAfterValidatedEnrollment(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	statePath := filepath.Join(root, "state")
	keyPath := filepath.Join(root, "secrets", "device.key")
	t.Setenv("AGENTBELL_CONFIG", configPath)
	t.Setenv("AGENTBELL_STATE_DIR", statePath)
	value := remoteDrainConfig(filepath.Join(root, "outbox"))
	value.TeamID = "team-main"
	value.Runtime = "ssh"
	value.Connector = remoteconfig.Connector{
		Type: "https",
		HTTPS: &remoteconfig.HTTPSConnector{
			Endpoint: "https://relay.invalid/v1/events",
		},
	}
	value.PrivateKeyRef = remoteconfig.PrivateKeyRef{
		Store: "file",
		Path: &remoteconfig.PathRef{
			Platform: runtime.GOOS,
			Value:    keyPath,
		},
		FileFallbackAcknowledged: true,
	}
	if err := remoteconfig.SaveRemote(
		filepath.Join(root, "remote.json"),
		&value,
	); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.NewPairingHTTPHandler(func(
		_ context.Context,
		request relay.PairEnrollmentRequest,
	) (relay.PairEnrollmentResult, error) {
		return relay.PairEnrollmentResult{
			PeerID:          request.PeerID,
			TeamID:          "team-main",
			AllowedSources:  []string{"codex"},
			AllowedRuntimes: []string{"ssh"},
		}, nil
	}))
	defer server.Close()
	codeValue := "AGBR-00000000-00000000-00000000-00000000"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{
		"remote", "pair",
		"--code-stdin",
		"--endpoint", server.URL + "/v1/pair",
		"--ssh-tunnel",
		"--json",
	}, strings.NewReader(codeValue+"\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), codeValue) {
		t.Fatal("pair output leaked the pairing code")
	}
	keyStore, err := secretstore.New(filepath.Join(statePath, "relay"))
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := keyStore.Get(
		context.Background(),
		value.PrivateKeyRef,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer wipeBytes(privateKey)
	publicKey := ed25519.PrivateKey(privateKey).Public().(ed25519.PublicKey)
	if !strings.Contains(stdout.String(), relayPeerID(publicKey)) {
		t.Fatalf("pair output = %s", stdout.String())
	}
}

func TestRemotePairStdioCompletesWithoutNetworkListener(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(map[bool]string{true: "accepted", false: "rejected"}[accepted], func(
			t *testing.T,
		) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config.json")
			statePath := filepath.Join(root, "state")
			keyPath := filepath.Join(root, "secrets", "device.key")
			t.Setenv("AGENTBELL_CONFIG", configPath)
			t.Setenv("AGENTBELL_STATE_DIR", statePath)
			value := remoteDrainConfig(filepath.Join(root, "outbox"))
			value.PrivateKeyRef = remoteconfig.PrivateKeyRef{
				Store: "file",
				Path: &remoteconfig.PathRef{
					Platform: runtime.GOOS,
					Value:    keyPath,
				},
				FileFallbackAcknowledged: true,
			}
			if err := remoteconfig.SaveRemote(
				filepath.Join(root, "remote.json"),
				&value,
			); err != nil {
				t.Fatal(err)
			}

			hostToChildReader, hostToChildWriter := io.Pipe()
			childToHostReader, childToHostWriter := io.Pipe()
			done := make(chan error, 1)
			go func() {
				err := runRemote(
					[]string{"pair", "--stdio"},
					hostToChildReader,
					childToHostWriter,
				)
				_ = childToHostWriter.Close()
				done <- err
			}()
			hello, err := remote.ReadPairHello(childToHostReader)
			if err != nil {
				t.Fatal(err)
			}
			if hello.TeamID != value.TeamID ||
				hello.OriginID != value.OriginID ||
				hello.Runtime != value.Runtime ||
				hello.PeerID != relayPeerID(hello.PublicKey) {
				t.Fatalf("hello = %#v", hello)
			}
			decision := remote.PairDecision{
				ErrorCode: remote.PairErrorEnrollmentFailed,
			}
			if accepted {
				decision = remote.PairDecision{
					Accepted:        true,
					PeerID:          hello.PeerID,
					TeamID:          value.TeamID,
					AllowedSources:  []string{"codex"},
					AllowedRuntimes: []string{value.Runtime},
				}
			}
			if err := remote.WritePairDecision(
				hostToChildWriter,
				decision,
			); err != nil {
				t.Fatal(err)
			}
			_ = hostToChildWriter.Close()
			childErr := <-done
			keyStore, err := secretstore.New(filepath.Join(
				statePath,
				"relay",
			))
			if err != nil {
				t.Fatal(err)
			}
			privateKey, keyErr := keyStore.Get(
				context.Background(),
				value.PrivateKeyRef,
			)
			wipeBytes(privateKey)
			if accepted {
				if childErr != nil || keyErr != nil {
					t.Fatalf(
						"childErr=%v keyErr=%v",
						childErr,
						keyErr,
					)
				}
			} else {
				if childErr == nil ||
					!errors.Is(keyErr, secretstore.ErrNotFound) {
					t.Fatalf(
						"childErr=%v keyErr=%v",
						childErr,
						keyErr,
					)
				}
			}
		})
	}
}

func testClock() time.Time {
	return time.Now().UTC().Add(time.Minute)
}

func remoteSignedOutboxFixture(
	t *testing.T,
	now time.Time,
) ([]byte, relay.SignatureMetadata) {
	t.Helper()
	producerKey := "remote-test-" + now.Format(time.RFC3339Nano)
	deliveryKey, err := relay.DeriveDeliveryKey(
		"team-main",
		"origin-main",
		producerKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	notification := event.Notification{
		Version:        event.Version,
		Source:         "codex",
		Surface:        "cli",
		Runtime:        "ssh",
		Event:          event.EventAgentInfo,
		Status:         event.StatusInfo,
		OccurredAt:     now,
		IdempotencyKey: producerKey,
		Priority:       event.PriorityNormal,
		PrivacyLevel:   event.PrivacyMetadataOnly,
	}
	envelope := relay.Envelope{
		ProtocolVersion: relay.ProtocolVersion,
		TeamID:          "team-main",
		Origin: relay.Origin{
			ID:      "origin-main",
			Runtime: "ssh",
		},
		Delivery: relay.Delivery{
			Key:         deliveryKey,
			ProducerKey: notification.IdempotencyKey,
		},
		SentAt: now,
		Nonce:  strings.Repeat("a", 32),
		Event:  notification,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := relay.Sign(
		privateKey,
		"POST",
		"/v1/events",
		now,
		envelope.Nonce,
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	return body, relay.SignatureMetadata{
		KeyID:     relayPeerID(publicKey),
		Method:    "POST",
		Target:    "/v1/events",
		SentAt:    now,
		Nonce:     envelope.Nonce,
		Signature: signature,
	}
}

func remoteDrainConfig(outboxPath string) remoteconfig.RemoteConfig {
	platform := runtime.GOOS
	runtimeName := "ssh"
	if platform == "windows" {
		runtimeName = "wsl"
	}
	value := remoteconfig.RemoteConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: "2.0.0",
		TeamID:         "team-main",
		OriginID:       "origin-main",
		Runtime:        runtimeName,
		Outbox: remoteconfig.Outbox{
			Path: remoteconfig.PathRef{
				Platform: platform,
				Value:    outboxPath,
			},
			MaxBytes: 64 << 20,
		},
		PrivateKeyRef: remoteconfig.PrivateKeyRef{
			Store: "file",
			Path: &remoteconfig.PathRef{
				Platform: platform,
				Value:    filepath.Join(filepath.Dir(outboxPath), "key"),
			},
			FileFallbackAcknowledged: true,
		},
	}
	switch runtimeName {
	case "wsl":
		value.Connector = remoteconfig.Connector{
			Type: "wsl",
			WSL: &remoteconfig.WSLConnector{
				Distribution: "Ubuntu",
				HostExecutable: remoteconfig.PathRef{
					Platform: "windows",
					Value:    `C:\Windows\System32\wsl.exe`,
				},
				RemoteExecutable: remoteconfig.PathRef{
					Platform: "linux",
					Value:    "/usr/local/bin/agentbell",
				},
			},
		}
	default:
		hostExecutable := "/usr/bin/ssh"
		knownHosts := "/tmp/known_hosts"
		if platform == "darwin" {
			knownHosts = "/Users/test/.ssh/known_hosts"
		}
		value.Connector = remoteconfig.Connector{
			Type: "ssh",
			SSH: &remoteconfig.SSHConnector{
				Host: "relay.example.com",
				Port: 22,
				User: "agentbell",
				HostExecutable: remoteconfig.PathRef{
					Platform: platform,
					Value:    hostExecutable,
				},
				KnownHostsFile: remoteconfig.PathRef{
					Platform: platform,
					Value:    knownHosts,
				},
				RemoteExecutable: remoteconfig.PathRef{
					Platform: platform,
					Value:    "/usr/local/bin/agentbell",
				},
			},
		}
	}
	return value
}
