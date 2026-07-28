package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

func TestRelayBindCreateStoresOnlyHash(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	var stdout bytes.Buffer
	if err := runRelay([]string{
		"bind", "create",
		"--team", "team-main",
		"--source", "codex",
		"--runtime", "ssh",
		"--ttl", "10m",
		"--json",
	}, strings.NewReader(""), &stdout); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Code      string `json:"code"`
		CodeHash  string `json:"codeHash"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Code, "AGBR-") ||
		!strings.HasPrefix(result.CodeHash, "sha256:") ||
		result.ExpiresAt == "" {
		t.Fatalf("result = %#v", result)
	}
	files, err := filepath.Glob(
		filepath.Join(root, "state", "relay", "pairings", "pending", "*.json"),
	)
	if err != nil || len(files) != 1 {
		t.Fatalf("pairing files=%v err=%v", files, err)
	}
	persisted, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(result.Code)) {
		t.Fatal("pairing code was persisted in plaintext")
	}
}

func TestRelayCommandIsExposedThroughRootDispatch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{
		"relay", "bind", "create",
		"--team", "team-main",
		"--source", "codex",
		"--runtime", "ssh",
		"--json",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"code": "AGBR-`) {
		t.Fatalf(
			"code=%d stdout=%s stderr=%s",
			code,
			stdout.String(),
			stderr.String(),
		)
	}
}

func TestRelayPeerListAndRevokeUseAtomicTransactions(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "relay.json")
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	publicKey := make([]byte, ed25519.PublicKeySize)
	for index := range publicKey {
		publicKey[index] = byte(index + 1)
	}
	value := remoteconfig.RelayConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: "2.0.0",
		Listener:       remoteconfig.Listener{},
		Peers: []remoteconfig.Peer{{
			ID:              "peer-one",
			TeamID:          "team-main",
			OriginID:        "origin-one",
			PublicKey:       base64.RawURLEncoding.EncodeToString(publicKey),
			Scopes:          []string{"ingest"},
			AllowedSources:  []string{"codex"},
			AllowedRuntimes: []string{"ssh"},
		}},
	}
	if err := remoteconfig.SaveRelay(configPath, &value); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := runRelay(
		[]string{"peers", "list", "--json"},
		strings.NewReader(""),
		&stdout,
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"id": "peer-one"`) {
		t.Fatalf("list output = %s", stdout.String())
	}
	stdout.Reset()
	if err := runRelay([]string{
		"peers", "revoke", "peer-one", "--dry-run", "--json",
	}, strings.NewReader(""), &stdout); err != nil {
		t.Fatal(err)
	}
	loaded, err := remoteconfig.LoadRelay(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Peers[0].Revoked {
		t.Fatal("dry-run revoked the peer")
	}
	stdout.Reset()
	if err := runRelay([]string{
		"peers", "revoke", "peer-one", "--json",
	}, strings.NewReader(""), &stdout); err != nil {
		t.Fatal(err)
	}
	loaded, err = remoteconfig.LoadRelay(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Peers[0].Revoked {
		t.Fatal("peer was not revoked")
	}
}

func TestRelayConfigureInitializesWithoutOverwriting(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	arguments := []string{
		"configure",
		"--listen", "127.0.0.1:18892",
		"--json",
	}
	var stdout bytes.Buffer
	if err := runRelay(
		append(append([]string(nil), arguments...), "--dry-run"),
		strings.NewReader(""),
		&stdout,
	); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "relay.json")
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote relay.json: %v", err)
	}
	stdout.Reset()
	if err := runRelay(arguments, strings.NewReader(""), &stdout); err != nil {
		t.Fatal(err)
	}
	value, err := remoteconfig.LoadRelay(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !value.Listener.Enabled ||
		value.Listener.Address != "127.0.0.1:18892" ||
		len(value.Peers) != 0 {
		t.Fatalf("relay config = %#v", value)
	}
	if err := runRelay(arguments, strings.NewReader(""), &bytes.Buffer{}); !errors.Is(
		err,
		remoteconfig.ErrRelayExists,
	) {
		t.Fatalf("overwrite error = %v", err)
	}
}

func TestRelayRuntimeWiresStrictPeerIngressAndReceiptList(t *testing.T) {
	root := t.TempDir()
	resolved := paths.Paths{
		ConfigFile: filepath.Join(root, "config.json"),
		StateDir:   filepath.Join(root, "state"),
	}
	publicKey := make([]byte, ed25519.PublicKeySize)
	value := remoteconfig.RelayConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: "2.0.0",
		Listener: remoteconfig.Listener{
			Enabled: true,
			Address: "127.0.0.1:18892",
		},
		Peers: []remoteconfig.Peer{{
			ID:              "peer-one",
			TeamID:          "team-main",
			OriginID:        "origin-one",
			PublicKey:       base64.RawURLEncoding.EncodeToString(publicKey),
			Scopes:          []string{"ingest"},
			AllowedSources:  []string{"codex"},
			AllowedRuntimes: []string{"ssh"},
		}},
	}
	runtimeValue, err := newRelayRuntime(resolved, value)
	if err != nil {
		t.Fatal(err)
	}
	peer, found := runtimeValue.ingress.Peer("peer-one")
	if !found || peer.Scopes[0] != relay.ScopeIngest {
		t.Fatalf("peer=%#v found=%v", peer, found)
	}
	if _, found := runtimeValue.ingress.Peer("missing"); found {
		t.Fatal("unknown peer was accepted")
	}

	t.Setenv("AGENTBELL_CONFIG", resolved.ConfigFile)
	t.Setenv("AGENTBELL_STATE_DIR", resolved.StateDir)
	var stdout bytes.Buffer
	if err := runRelay(
		[]string{"receipts", "list", "--json"},
		strings.NewReader(""),
		&stdout,
	); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != "[]" {
		t.Fatalf("receipt output = %s", stdout.String())
	}
}

func TestRelayCommandRejectsUnsafeOrIncompleteInvocation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	tests := [][]string{
		nil,
		{"run"},
		{"bind", "create", "--team", "team-main", "--source", "codex"},
		{"peers", "revoke"},
		{"peers", "list", "extra"},
		{"receipts", "remove"},
	}
	for _, arguments := range tests {
		if err := runRelay(
			arguments,
			strings.NewReader(""),
			&bytes.Buffer{},
		); err == nil {
			t.Fatalf("arguments %#v were accepted", arguments)
		}
	}
}

func TestRelayRuntimeStopsCleanupWhenServerFails(t *testing.T) {
	runtimeValue := &relayRuntime{
		nonces:   mustRelayNonceStore(t),
		receipts: mustRelayReceiptStore(t),
		server:   relay.HTTPServer{},
	}
	result := make(chan error, 1)
	go func() {
		result <- runtimeValue.run(context.Background())
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("invalid relay server unexpectedly ran")
		}
	case <-time.After(time.Second):
		t.Fatal("relay runtime leaked its nonce cleanup goroutine")
	}
}

func TestRelayHostEnrollmentReleasesMismatchedPolicy(t *testing.T) {
	root := t.TempDir()
	relayPath := filepath.Join(root, "relay.json")
	configValue := remoteconfig.RelayConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: "0.2.0",
		Listener:       remoteconfig.Listener{},
		Peers:          []remoteconfig.Peer{},
	}
	if err := remoteconfig.SaveRelay(relayPath, &configValue); err != nil {
		t.Fatal(err)
	}
	pairings, err := relay.OpenPairingStore(filepath.Join(root, "pairings"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	code, _, err := pairings.Create(relay.PairingPolicy{
		TeamID:          "team-other",
		AllowedSources:  []string{"codex"},
		AllowedRuntimes: []string{"ssh"},
	}, 3*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	runtimeValue := &relayRuntime{
		pairings:        pairings,
		transactions:    remoteconfig.NewRelayTransactions(relayPath),
		expectedTeamID:  "team-main",
		expectedRuntime: "ssh",
		peers:           map[string]relay.Peer{},
	}
	publicKey := make([]byte, ed25519.PublicKeySize)
	if _, err := runtimeValue.enroll(
		context.Background(),
		relay.PairEnrollmentRequest{
			Code:      code,
			PeerID:    relayPeerID(publicKey),
			OriginID:  "origin-build",
			PublicKey: publicKey,
		},
	); err == nil {
		t.Fatal("mismatched host pairing policy was accepted")
	}
	claim, err := pairings.Claim(code, now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatalf("mismatched policy consumed pairing code: %v", err)
	}
	_ = pairings.Release(claim, now.Add(2*time.Second))
}

func TestRelayPairingEndpointEnrollsPeerAndConsumesCode(t *testing.T) {
	root := t.TempDir()
	resolved := paths.Paths{
		ConfigFile: filepath.Join(root, "config.json"),
		StateDir:   filepath.Join(root, "state"),
	}
	configPath := filepath.Join(root, "relay.json")
	configValue := remoteconfig.RelayConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: "2.0.0",
		Listener: remoteconfig.Listener{
			Enabled: true,
			Address: "127.0.0.1:18892",
		},
		Peers: []remoteconfig.Peer{},
	}
	if err := remoteconfig.SaveRelay(configPath, &configValue); err != nil {
		t.Fatal(err)
	}
	pairings, err := relay.OpenPairingStore(filepath.Join(
		resolved.StateDir,
		"relay",
		"pairings",
	))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	code, _, err := pairings.Create(relay.PairingPolicy{
		TeamID:          "team-main",
		AllowedSources:  []string{"codex"},
		AllowedRuntimes: []string{"ssh"},
	}, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerID := relayPeerID(publicKey)
	body, err := json.Marshal(map[string]string{
		"code":      code,
		"peerId":    peerID,
		"originId":  "origin-one",
		"publicKey": base64.RawURLEncoding.EncodeToString(publicKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeValue, err := newRelayRuntime(resolved, configValue)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1/v1/pair",
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	runtimeValue.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	loaded, err := remoteconfig.LoadRelay(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Peers) != 1 ||
		loaded.Peers[0].ID != peerID ||
		loaded.Peers[0].OriginID != "origin-one" {
		t.Fatalf("peers = %#v", loaded.Peers)
	}
	if _, found := runtimeValue.ingress.Peer(peerID); !found {
		t.Fatal("new peer was not activated in the running relay")
	}
	if _, err := pairings.Claim(
		code,
		time.Now().UTC(),
		time.Minute,
	); !errors.Is(err, relay.ErrPairingConsumed) {
		t.Fatalf("pairing code was not consumed: %v", err)
	}

	replay := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1/v1/pair",
		bytes.NewReader(body),
	)
	replay.Header.Set("Content-Type", "application/json")
	replayResponse := httptest.NewRecorder()
	runtimeValue.server.Handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code == http.StatusCreated {
		t.Fatal("consumed pairing code was accepted again")
	}
}

func mustRelayNonceStore(t *testing.T) *relay.NonceStore {
	t.Helper()
	store, err := relay.OpenNonceStore(
		filepath.Join(t.TempDir(), "nonces"),
		relay.MinimumNonceRetention,
	)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mustRelayReceiptStore(t *testing.T) *relay.ReceiptStore {
	t.Helper()
	store, err := relay.OpenReceiptStore(filepath.Join(t.TempDir(), "receipts"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
