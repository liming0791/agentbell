package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/remote"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

func TestConfiguredRemoteWorkerPreservesServiceWithoutSidecar(t *testing.T) {
	root := t.TempDir()
	resolved := paths.Paths{
		ConfigFile: filepath.Join(root, "config.json"),
		StateDir:   filepath.Join(root, "state"),
	}
	workers, err := configuredRemoteWorkers(resolved)
	if err != nil || len(workers) != 0 {
		t.Fatalf("workers=%#v err=%v", workers, err)
	}
	if _, err := os.Stat(resolved.StateDir); !os.IsNotExist(err) {
		t.Fatalf("missing sidecar created service state: %v", err)
	}
}

func TestConfiguredRemoteWorkersIncludesHTTPS(t *testing.T) {
	for _, contents := range []string{`{}`, `{"connector":{"type":"https"}}`} {
		t.Run(contents, func(t *testing.T) {
			root := t.TempDir()
			resolved := paths.Paths{
				ConfigFile: filepath.Join(root, "config.json"),
				StateDir:   filepath.Join(root, "state"),
			}
			if err := os.WriteFile(
				filepath.Join(root, "remote.json"),
				[]byte(contents),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			workers, err := configuredRemoteWorkers(resolved)
			if err != nil {
				t.Fatal(err)
			}
			if len(workers) != 1 {
				t.Fatalf("workers=%#v", workers)
			}
			scheduler, ok := workers[0].(remote.HostScheduler)
			if !ok ||
				scheduler.RemoteConfigPath != filepath.Join(root, "remote.json") ||
				scheduler.Target != nil {
				t.Fatalf("worker=%#v", workers[0])
			}
		})
	}
}

func TestConfiguredRemoteWorkerRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "remote.json")); err != nil {
		t.Fatal(err)
	}
	workers, err := configuredRemoteWorkers(paths.Paths{
		ConfigFile: filepath.Join(root, "config.json"),
		StateDir:   filepath.Join(root, "state"),
	})
	if err == nil || workers != nil {
		t.Fatalf("workers=%#v err=%v", workers, err)
	}
}

func TestConfiguredRemoteWorkersEnumeratesHostRegistryOnly(t *testing.T) {
	root := t.TempDir()
	registry := remoteconfig.HostConnectors{
		Version:        remoteconfig.Version,
		MinCoreVersion: "0.2.0",
		Connectors: []remoteconfig.HostConnector{{
			ID:       "build-primary",
			TeamID:   "team-main",
			OriginID: "origin-build",
			Runtime:  "ssh",
			Connector: remoteconfig.Connector{
				Type: "ssh",
				SSH: &remoteconfig.SSHConnector{
					Host: "build.example.com", Port: 22, User: "agentbell",
					HostExecutable: remoteconfig.PathRef{
						Platform: "darwin", Value: "/usr/bin/ssh",
					},
					KnownHostsFile: remoteconfig.PathRef{
						Platform: "darwin", Value: "/Users/test/.ssh/known_hosts",
					},
					RemoteExecutable: remoteconfig.PathRef{
						Platform: "linux", Value: "/usr/local/bin/agentbell",
					},
				},
			},
		}},
	}
	if err := remoteconfig.SaveHostConnectors(
		filepath.Join(root, "host-connectors.json"),
		&registry,
	); err != nil {
		t.Fatal(err)
	}
	workers, err := configuredRemoteWorkers(paths.Paths{
		ConfigFile: filepath.Join(root, "config.json"),
		StateDir:   filepath.Join(root, "state"),
	})
	if err != nil || len(workers) != 1 {
		t.Fatalf("workers=%#v err=%v", workers, err)
	}
	scheduler, ok := workers[0].(remote.HostScheduler)
	if !ok || scheduler.Target == nil ||
		scheduler.Target.ID != "build-primary" ||
		scheduler.RemoteConfigPath != "" {
		t.Fatalf("scheduler=%#v", scheduler)
	}
}

func TestConfiguredRemoteWorkersDoesNotTreatRemoteHostConfigAsRegistry(t *testing.T) {
	root := t.TempDir()
	value := remoteconfig.RemoteConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: "0.2.0",
		TeamID:         "team-main",
		OriginID:       "origin-wsl",
		Runtime:        "wsl",
		Outbox: remoteconfig.Outbox{
			Path: remoteconfig.PathRef{
				Platform: "linux",
				Value:    "/var/lib/agentbell/outbox",
			},
			MaxBytes: 64 << 20,
		},
		Connector: remoteconfig.Connector{
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
		},
		PrivateKeyRef: remoteconfig.PrivateKeyRef{
			Store: "secret-service",
			ID:    "agentbell/origin-wsl",
		},
	}
	if err := remoteconfig.SaveRemote(filepath.Join(root, "remote.json"), &value); err != nil {
		t.Fatal(err)
	}
	workers, err := configuredRemoteWorkers(paths.Paths{
		ConfigFile: filepath.Join(root, "config.json"),
		StateDir:   filepath.Join(root, "state"),
	})
	if err != nil || len(workers) != 0 {
		t.Fatalf("remote-owned config scheduled host pull: %#v err=%v", workers, err)
	}
}

func TestDiagnoseRemoteWorkersIsReadOnlyAndRedacted(t *testing.T) {
	root := t.TempDir()
	registry := remoteconfig.HostConnectors{
		Version:        remoteconfig.Version,
		MinCoreVersion: "0.2.0",
		Connectors: []remoteconfig.HostConnector{{
			ID:       "secret-connector-id",
			TeamID:   "team-main",
			OriginID: "origin-secret",
			Runtime:  "ssh",
			Connector: remoteconfig.Connector{
				Type: "ssh",
				SSH: &remoteconfig.SSHConnector{
					Host: "secret.example.com", Port: 22, User: "agentbell",
					HostExecutable: remoteconfig.PathRef{
						Platform: "darwin", Value: "/private/ssh",
					},
					KnownHostsFile: remoteconfig.PathRef{
						Platform: "darwin", Value: "/private/known_hosts",
					},
					RemoteExecutable: remoteconfig.PathRef{
						Platform: "linux", Value: "/private/agentbell",
					},
				},
			},
		}},
	}
	if err := remoteconfig.SaveHostConnectors(
		filepath.Join(root, "host-connectors.json"),
		&registry,
	); err != nil {
		t.Fatal(err)
	}
	resolved := paths.Paths{
		ConfigFile: filepath.Join(root, "config.json"),
		StateDir:   filepath.Join(root, "state"),
	}
	report := diagnoseRemoteWorkers(resolved)
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"secret-connector-id",
		"origin-secret",
		"secret.example.com",
		"/private/",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("doctor leaked %q: %s", secret, raw)
		}
	}
	if !report.Configured || report.ConnectorCounts["ssh"] != 1 {
		t.Fatalf("report=%#v", report)
	}
	if _, err := os.Stat(resolved.StateDir); !os.IsNotExist(err) {
		t.Fatalf("doctor mutated state: %v", err)
	}
}
