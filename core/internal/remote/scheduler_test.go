package remote

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

func writeSchedulerRemote(t *testing.T, path string, value remoteconfig.RemoteConfig) {
	t.Helper()
	if err := remoteconfig.SaveRemote(path, &value); err != nil {
		t.Fatal(err)
	}
}

func schedulerTarget(kind string) remoteconfig.HostConnector {
	value := validRemoteConfig(kind)
	return remoteconfig.HostConnector{
		ID:        kind + "-primary",
		TeamID:    value.TeamID,
		OriginID:  value.OriginID,
		Runtime:   value.Runtime,
		Connector: value.Connector,
	}
}

func TestSchedulerRunsPullWithoutReadingRemoteOutboxPath(t *testing.T) {
	root := t.TempDir()
	target := schedulerTarget("ssh")
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	var attempts atomic.Int32
	scheduler := HostScheduler{
		Target:         &target,
		StateDir:       root,
		Platform:       "darwin",
		Now:            func() time.Time { return now },
		CheckHostFiles: func(remoteconfig.HostConnector, string) error { return nil },
		PullAttempt: func(context.Context, remoteconfig.HostConnector) (int, error) {
			attempts.Add(1)
			return 3, nil
		},
	}
	status, err := scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 1 ||
		status.State != HostStateHealthy ||
		status.Connector != "ssh" ||
		status.Forwarded != 3 ||
		status.CommandDigest == "" ||
		status.CommandArgCount == 0 {
		t.Fatalf("status = %#v attempts=%d", status, attempts.Load())
	}
	raw, err := os.ReadFile(scheduler.StatusPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"build.example.com",
		"/usr/bin/ssh",
		"origin-main",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("status leaked %q: %s", secret, raw)
		}
	}
}

func TestSchedulerRunsHTTPSDurableOutboxAsPush(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "remote.json")
	value := validRemoteConfig("https")
	value.Outbox.Path = remoteconfig.PathRef{
		Platform: runtime.GOOS,
		Value:    filepath.Join(root, "outbox"),
	}
	writeSchedulerRemote(t, path, value)
	var pushes atomic.Int32
	scheduler := HostScheduler{
		RemoteConfigPath: path,
		StateDir:         root,
		Platform:         runtime.GOOS,
		PushAttempt: func(context.Context, remoteconfig.RemoteConfig) (int, error) {
			pushes.Add(1)
			return 2, nil
		},
	}
	status, err := scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pushes.Load() != 1 ||
		status.Connector != "https" ||
		status.State != HostStateHealthy ||
		status.Forwarded != 2 ||
		status.CommandDigest != "" ||
		status.CommandArgCount != 0 {
		t.Fatalf("status = %#v pushes=%d", status, pushes.Load())
	}
}

func TestSchedulerPersistsOnlyStableFailureCode(t *testing.T) {
	root := t.TempDir()
	target := schedulerTarget("ssh")
	scheduler := HostScheduler{
		Target:   &target,
		StateDir: root,
		Platform: "darwin",
		CheckHostFiles: func(remoteconfig.HostConnector, string) error {
			return errors.New("secret path /Users/alice/.ssh/known_hosts")
		},
	}
	status, err := scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != HostStateBackoff ||
		status.ErrorCode != HostErrorExecutableUnavailable {
		t.Fatalf("status = %#v", status)
	}
	raw, err := os.ReadFile(scheduler.StatusPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "alice") ||
		strings.Contains(string(raw), "known_hosts") {
		t.Fatalf("status leaked failure: %s", raw)
	}
}

func TestSchedulerRejectsSecondOwnerAndRecoversStaleLock(t *testing.T) {
	root := t.TempDir()
	scheduler := HostScheduler{StateDir: root}
	lock, err := scheduler.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.acquireLock(); !errors.Is(err, ErrHostSchedulerRunning) {
		t.Fatalf("second lock error = %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(scheduler.LockPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scheduler.LockPath(), []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * defaultHostLockStale)
	if err := os.Chtimes(scheduler.LockPath(), old, old); err != nil {
		t.Fatal(err)
	}
	recovered, err := scheduler.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.release()
}

func TestSchedulerStatusIsStrictAndDoctorIsRedacted(t *testing.T) {
	root := t.TempDir()
	target := schedulerTarget("ssh")
	scheduler := HostScheduler{
		Target:         &target,
		StateDir:       root,
		Platform:       "darwin",
		CheckHostFiles: func(remoteconfig.HostConnector, string) error { return nil },
		PullAttempt: func(context.Context, remoteconfig.HostConnector) (int, error) {
			return 0, nil
		},
	}
	if _, err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := scheduler.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Configured || !report.RuntimeProof || !report.Healthy {
		t.Fatalf("doctor = %#v", report)
	}
	if strings.Contains(string(encoded), "build.example.com") ||
		strings.Contains(string(encoded), "/usr/bin/ssh") {
		t.Fatalf("doctor leaked config: %s", encoded)
	}
	raw, err := os.ReadFile(scheduler.StatusPath())
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-2], []byte(`,"unknown":true}`)...)
	if err := os.WriteFile(scheduler.StatusPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Status(context.Background()); err == nil {
		t.Fatal("status accepted unknown field")
	}
}

func TestSchedulerRunRetriesAndStopsCleanly(t *testing.T) {
	root := t.TempDir()
	target := schedulerTarget("ssh")
	ctx, cancel := context.WithCancel(context.Background())
	var attempts atomic.Int32
	var waits atomic.Int32
	scheduler := HostScheduler{
		Target:         &target,
		StateDir:       root,
		Platform:       "darwin",
		PollInterval:   time.Millisecond,
		Backoff:        []time.Duration{time.Millisecond},
		CheckHostFiles: func(remoteconfig.HostConnector, string) error { return nil },
		PullAttempt: func(context.Context, remoteconfig.HostConnector) (int, error) {
			attempts.Add(1)
			return 0, errors.New("offline secret endpoint")
		},
		Wait: func(ctx context.Context, delay time.Duration) error {
			if delay != time.Millisecond {
				t.Fatalf("delay = %v", delay)
			}
			if waits.Add(1) == 1 {
				cancel()
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	if err := scheduler.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() == 0 || waits.Load() == 0 {
		t.Fatalf("attempts=%d waits=%d", attempts.Load(), waits.Load())
	}
	status, err := scheduler.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != HostStateStopped ||
		status.ErrorCode != HostErrorPullFailed {
		t.Fatalf("status = %#v", status)
	}
}

func TestSchedulerInvalidAndMissingConfigurations(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  []byte
		code string
	}{
		{name: "missing", code: HostErrorConfigMissing},
		{name: "invalid", raw: []byte(`{"endpoint":"secret.example"}`), code: HostErrorConfigInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "remote.json")
			if test.raw != nil {
				if err := os.WriteFile(path, test.raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			status, err := (HostScheduler{
				RemoteConfigPath: path,
				StateDir:         root,
			}).RunOnce(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if status.State != HostStateInvalid || status.ErrorCode != test.code {
				t.Fatalf("status = %#v", status)
			}
		})
	}
}

func TestSchedulerPlatformMismatchAndHTTPSPushFailure(t *testing.T) {
	t.Run("pull platform", func(t *testing.T) {
		root := t.TempDir()
		target := schedulerTarget("wsl")
		status, err := (HostScheduler{
			Target:   &target,
			StateDir: root,
			Platform: "linux",
		}).RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.ErrorCode != HostErrorPlatformMismatch {
			t.Fatalf("status = %#v", status)
		}
	})
	t.Run("https failure", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "remote.json")
		value := validRemoteConfig("https")
		value.Outbox.Path = remoteconfig.PathRef{
			Platform: runtime.GOOS,
			Value:    filepath.Join(root, "outbox"),
		}
		writeSchedulerRemote(t, path, value)
		status, err := (HostScheduler{
			RemoteConfigPath: path,
			StateDir:         root,
			Platform:         runtime.GOOS,
			PushAttempt: func(context.Context, remoteconfig.RemoteConfig) (int, error) {
				return 0, errors.New("TLS secret")
			},
		}).RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.ErrorCode != HostErrorPushFailed {
			t.Fatalf("status = %#v", status)
		}
	})
}

func TestSchedulerBuildsIngressWithDisabledListener(t *testing.T) {
	root := t.TempDir()
	publicKey := make([]byte, ed25519.PublicKeySize)
	for index := range publicKey {
		publicKey[index] = byte(index + 1)
	}
	relayPath := filepath.Join(root, "relay.json")
	configValue := remoteconfig.RelayConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: "2.0.0",
		Listener:       remoteconfig.Listener{Enabled: false},
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
	if err := remoteconfig.SaveRelay(relayPath, &configValue); err != nil {
		t.Fatal(err)
	}
	scheduler := HostScheduler{RelayConfigPath: relayPath, StateDir: root}
	ingress, err := scheduler.newIngress()
	if err != nil || ingress == nil {
		t.Fatalf("ingress=%#v err=%v", ingress, err)
	}
	if !scheduler.relayReady() {
		t.Fatal("disabled listener was incorrectly required for host pull")
	}
	configValue.Peers[0].PublicKey = "invalid"
	if err := remoteconfig.SaveRelay(relayPath, &configValue); err == nil {
		t.Fatal("invalid relay config was saved")
	}
}

func TestDefaultHostFileChecksAndSchedulerValidation(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "ssh")
	knownHosts := filepath.Join(root, "known_hosts")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHosts, []byte("host key"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := schedulerTarget("ssh")
	target.Connector.SSH.HostExecutable = remoteconfig.PathRef{
		Platform: runtime.GOOS,
		Value:    executable,
	}
	target.Connector.SSH.KnownHostsFile = remoteconfig.PathRef{
		Platform: runtime.GOOS,
		Value:    knownHosts,
	}
	if err := defaultCheckHostFiles(target, runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	if err := defaultCheckHostFiles(target, "other"); !errors.Is(
		err,
		errHostPlatformMismatch,
	) {
		t.Fatalf("platform error = %v", err)
	}
	if err := checkRegularFile(filepath.Join(root, "missing"), false); err == nil {
		t.Fatal("missing file passed")
	}
	for _, scheduler := range []HostScheduler{
		{},
		{RemoteConfigPath: "x", StateDir: "y", PollInterval: -1},
		{RemoteConfigPath: "x", StateDir: "y", Backoff: []time.Duration{0}},
		{
			StateDir: "y",
			Target: &remoteconfig.HostConnector{
				ID: "../../escape",
			},
		},
	} {
		if err := scheduler.validate(); err == nil {
			t.Fatalf("scheduler passed validation: %#v", scheduler)
		}
	}
}

func TestHostLockHeartbeatDetectsLostOwnership(t *testing.T) {
	root := t.TempDir()
	scheduler := HostScheduler{StateDir: root}
	lock, err := scheduler.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock.path, []byte(`{"token":"replaced"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lock.heartbeat(ctx, time.Millisecond); !errors.Is(
		err,
		ErrHostSchedulerLockLost,
	) {
		t.Fatalf("heartbeat error = %v", err)
	}
}

func TestSchedulerStatusContextAndDoctorWithoutProof(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "remote.json")
	value := validRemoteConfig("https")
	value.Outbox.Path.Platform = runtime.GOOS
	value.Outbox.Path.Value = filepath.Join(root, "outbox")
	writeSchedulerRemote(t, path, value)
	scheduler := HostScheduler{
		RemoteConfigPath: path,
		StateDir:         root,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scheduler.Status(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("status error = %v", err)
	}
	report, err := scheduler.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Configured || report.RuntimeProof || !report.RelayReady {
		t.Fatalf("doctor = %#v", report)
	}
}
