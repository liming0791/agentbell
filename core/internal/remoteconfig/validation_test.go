package remoteconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validSSHConnector() SSHConnector {
	return SSHConnector{
		Host:             "build.example.com",
		Port:             22,
		User:             "agentbell",
		HostExecutable:   pathRef("linux", "/usr/bin/ssh"),
		KnownHostsFile:   pathRef("linux", "/home/agentbell/.ssh/known_hosts"),
		RemoteExecutable: pathRef("linux", "/usr/local/bin/agentbell"),
	}
}

func validContainerConnector() ContainerConnector {
	return ContainerConnector{
		Runtime:          "docker",
		HostExecutable:   pathRef("linux", "/usr/bin/docker"),
		ContainerID:      "agentbell-worker-1",
		RemoteExecutable: pathRef("linux", "/usr/local/bin/agentbell"),
	}
}

func TestRemoteHeaderOutboxAndConnectorDiscriminatorValidation(t *testing.T) {
	tests := map[string]func(*RemoteConfig){
		"version": func(value *RemoteConfig) {
			value.Version = Version + 1
		},
		"minimum core version": func(value *RemoteConfig) {
			value.MinCoreVersion = "latest"
		},
		"team identity": func(value *RemoteConfig) {
			value.TeamID = "Team Main"
		},
		"origin identity": func(value *RemoteConfig) {
			value.OriginID = "../origin"
		},
		"host runtime": func(value *RemoteConfig) {
			value.Runtime = "host"
		},
		"unknown runtime": func(value *RemoteConfig) {
			value.Runtime = "browser"
		},
		"outbox too small": func(value *RemoteConfig) {
			value.Outbox.MaxBytes = minimumOutboxBytes - 1
		},
		"outbox too large": func(value *RemoteConfig) {
			value.Outbox.MaxBytes = maximumOutboxBytes + 1
		},
		"no connector arm": func(value *RemoteConfig) {
			value.Connector = Connector{Type: "wsl"}
		},
		"unknown connector type": func(value *RemoteConfig) {
			value.Connector.Type = "shell"
		},
		"type arm mismatch": func(value *RemoteConfig) {
			value.Connector.Type = "ssh"
		},
		"WSL runtime mismatch": func(value *RemoteConfig) {
			value.Runtime = "ssh"
		},
		"SSH runtime mismatch": func(value *RemoteConfig) {
			value.Runtime = "wsl"
			value.Connector = Connector{Type: "ssh", SSH: pointerSSH(validSSHConnector())}
		},
		"container runtime mismatch": func(value *RemoteConfig) {
			value.Connector = Connector{
				Type:      "container",
				Container: pointerContainer(validContainerConnector()),
			}
		},
		"HTTPS forbidden for WSL": func(value *RemoteConfig) {
			value.Connector = Connector{
				Type:  "https",
				HTTPS: &HTTPSConnector{Endpoint: "https://relay.example.com/events"},
			}
		},
		"vendor cloud type mismatch": func(value *RemoteConfig) {
			value.Runtime = "vendor-cloud"
			value.Connector = Connector{
				Type: "vendor-cloud",
				WSL:  validWSLConnector().WSL,
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

func TestConnectorSpecificValidation(t *testing.T) {
	t.Run("WSL", func(t *testing.T) {
		tests := map[string]func(*WSLConnector){
			"distribution": func(value *WSLConnector) {
				value.Distribution = "Ubuntu; shutdown"
			},
			"host platform": func(value *WSLConnector) {
				value.HostExecutable.Platform = "linux"
			},
			"host basename": func(value *WSLConnector) {
				value.HostExecutable.Value = `C:\Windows\System32\cmd.exe`
			},
			"remote platform": func(value *WSLConnector) {
				value.RemoteExecutable.Platform = "windows"
				value.RemoteExecutable.Value = `C:\AgentBell\agentbell.exe`
			},
			"remote basename": func(value *WSLConnector) {
				value.RemoteExecutable.Value = "/bin/sh"
			},
		}
		for name, mutate := range tests {
			t.Run(name, func(t *testing.T) {
				value := *validWSLConnector().WSL
				mutate(&value)
				if err := value.Validate(); err == nil {
					t.Fatal("expected validation error")
				}
			})
		}
	})

	t.Run("SSH", func(t *testing.T) {
		tests := map[string]func(*SSHConnector){
			"host": func(value *SSHConnector) {
				value.Host = "host|command"
			},
			"port": func(value *SSHConnector) {
				value.Port = 65536
			},
			"user": func(value *SSHConnector) {
				value.User = "user name"
			},
			"host executable": func(value *SSHConnector) {
				value.HostExecutable.Value = "/usr/bin/bash"
			},
			"known hosts path": func(value *SSHConnector) {
				value.KnownHostsFile.Value = "relative"
			},
			"known hosts platform": func(value *SSHConnector) {
				value.KnownHostsFile = pathRef(
					"darwin",
					"/Users/agentbell/.ssh/known_hosts",
				)
			},
			"remote executable": func(value *SSHConnector) {
				value.RemoteExecutable.Value = "/usr/bin/sh"
			},
		}
		for name, mutate := range tests {
			t.Run(name, func(t *testing.T) {
				value := validSSHConnector()
				mutate(&value)
				if err := value.Validate(); err == nil {
					t.Fatal("expected validation error")
				}
			})
		}
	})

	t.Run("container", func(t *testing.T) {
		tests := map[string]func(*ContainerConnector){
			"runtime": func(value *ContainerConnector) {
				value.Runtime = "containerd"
			},
			"container id": func(value *ContainerConnector) {
				value.ContainerID = "worker; command"
			},
			"host executable": func(value *ContainerConnector) {
				value.HostExecutable.Value = "/usr/bin/podman"
			},
			"remote executable": func(value *ContainerConnector) {
				value.RemoteExecutable.Platform = "darwin"
			},
		}
		for name, mutate := range tests {
			t.Run(name, func(t *testing.T) {
				value := validContainerConnector()
				mutate(&value)
				if err := value.Validate(); err == nil {
					t.Fatal("expected validation error")
				}
			})
		}
	})

	t.Run("HTTPS", func(t *testing.T) {
		invalidEndpoints := []string{
			"://bad",
			"http://relay.example.com/events",
			"https://user:password@relay.example.com/events",
			"https://relay.example.com/events#fragment",
			"https://host;command/events",
			"https://relay.example.com:99999/events",
		}
		for _, endpoint := range invalidEndpoints {
			t.Run(endpoint, func(t *testing.T) {
				value := HTTPSConnector{Endpoint: endpoint}
				if err := value.Validate(); err == nil {
					t.Fatal("expected validation error")
				}
			})
		}
		value := HTTPSConnector{
			Endpoint:   "https://relay.example.com/events",
			PinnedSPKI: strings.Repeat("A", 64),
		}
		if err := value.Validate(); err == nil {
			t.Fatal("uppercase SPKI pin was accepted")
		}
	})
}

func TestPrivateKeyReferenceValidation(t *testing.T) {
	for _, store := range []string{"keychain", "dpapi", "secret-service"} {
		t.Run(store, func(t *testing.T) {
			value := PrivateKeyRef{Store: store, ID: "agentbell/device:key"}
			if err := value.Validate(); err != nil {
				t.Fatalf("valid secret reference rejected: %v", err)
			}
		})
	}
	tests := map[string]PrivateKeyRef{
		"unsafe id": {
			Store: "keychain",
			ID:    "secret value",
		},
		"non-file path": {
			Store: "dpapi",
			ID:    "agentbell/device",
			Path:  pointerPath(pathRef("windows", `C:\AgentBell\key`)),
		},
		"non-file acknowledgement": {
			Store:                    "secret-service",
			ID:                       "agentbell/device",
			FileFallbackAcknowledged: true,
		},
		"file with id": {
			Store:                    "file",
			ID:                       "device",
			Path:                     pointerPath(pathRef("linux", "/etc/agentbell/key")),
			FileFallbackAcknowledged: true,
		},
		"file without path": {
			Store:                    "file",
			FileFallbackAcknowledged: true,
		},
		"file invalid path": {
			Store:                    "file",
			Path:                     pointerPath(pathRef("linux", "relative/key")),
			FileFallbackAcknowledged: true,
		},
		"unknown store": {
			Store: "plaintext",
			ID:    "device",
		},
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPathReferenceValidation(t *testing.T) {
	valid := []PathRef{
		pathRef("darwin", "/Users/test/Library/Application Support/AgentBell/key"),
		pathRef("linux", "/var/lib/agentbell/key"),
		pathRef("windows", `C:\Users\test\AppData\Local\AgentBell\key`),
		pathRef("windows", `\\server\share\AgentBell\key`),
	}
	for _, value := range valid {
		if err := value.Validate(); err != nil {
			t.Errorf("valid path %#v rejected: %v", value, err)
		}
	}
	invalid := []PathRef{
		pathRef("freebsd", "/var/lib/agentbell"),
		pathRef("linux", ""),
		pathRef("linux", "/var/lib/\nkey"),
		pathRef("linux", "var/lib/agentbell"),
		pathRef("linux", "/var//lib/agentbell"),
		pathRef("windows", `C:relative\key`),
		pathRef("windows", `\\server`),
		pathRef("windows", `C:\AgentBell\..\key`),
		pathRef("windows", `C:\AgentBell\.\key`),
	}
	for _, value := range invalid {
		if err := value.Validate(); err == nil {
			t.Errorf("invalid path %#v was accepted", value)
		}
	}
}

func TestRelayListenerTLSAndDuplicatePeerValidation(t *testing.T) {
	disabled := validRelay()
	disabled.Listener = Listener{}
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled listener rejected: %v", err)
	}
	for name, listener := range map[string]Listener{
		"disabled with address": {
			Address: "127.0.0.1:18892",
		},
		"missing port": {
			Enabled: true,
			Address: "localhost",
		},
		"zero port": {
			Enabled: true,
			Address: "localhost:0",
		},
		"unsafe host": {
			Enabled: true,
			Address: "host|command:18892",
		},
		"TLS invalid cert path": {
			Enabled: true,
			Address: "0.0.0.0:18892",
			TLS: &ListenerTLS{
				CertFile: pathRef("linux", "relative/cert.pem"),
				KeyFile:  pathRef("linux", "/etc/agentbell/key.pem"),
			},
		},
		"TLS invalid key path": {
			Enabled: true,
			Address: "0.0.0.0:18892",
			TLS: &ListenerTLS{
				CertFile: pathRef("linux", "/etc/agentbell/cert.pem"),
				KeyFile:  pathRef("linux", "relative/key.pem"),
			},
		},
		"TLS platform mismatch": {
			Enabled: true,
			Address: "0.0.0.0:18892",
			TLS: &ListenerTLS{
				CertFile: pathRef("linux", "/etc/agentbell/cert.pem"),
				KeyFile:  pathRef("darwin", "/etc/agentbell/key.pem"),
			},
		},
		"TLS same file": {
			Enabled: true,
			Address: "0.0.0.0:18892",
			TLS: &ListenerTLS{
				CertFile: pathRef("linux", "/etc/agentbell/tls.pem"),
				KeyFile:  pathRef("linux", "/etc/agentbell/tls.pem"),
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := validRelay()
			value.Listener = listener
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	t.Run("nil peers", func(t *testing.T) {
		value := validRelay()
		value.Peers = nil
		if err := value.Validate(); err == nil {
			t.Fatal("nil peers accepted")
		}
	})
	t.Run("duplicate public key", func(t *testing.T) {
		value := relayWithSecondPeer()
		value.Peers[1].PublicKey = value.Peers[0].PublicKey
		if err := value.Validate(); err == nil {
			t.Fatal("duplicate public key accepted")
		}
	})
	t.Run("duplicate team and origin", func(t *testing.T) {
		value := relayWithSecondPeer()
		value.Peers[1].TeamID = value.Peers[0].TeamID
		value.Peers[1].OriginID = value.Peers[0].OriginID
		if err := value.Validate(); err == nil {
			t.Fatal("duplicate team/origin accepted")
		}
	})
}

func TestPeerIdentityAndListValidation(t *testing.T) {
	tests := map[string]func(*Peer){
		"id": func(value *Peer) {
			value.ID = "Peer One"
		},
		"team": func(value *Peer) {
			value.TeamID = "../team"
		},
		"origin": func(value *Peer) {
			value.OriginID = "Origin"
		},
		"empty scopes": func(value *Peer) {
			value.Scopes = nil
		},
		"duplicate scopes": func(value *Peer) {
			value.Scopes = []string{"ingest", "ingest"}
		},
		"empty sources": func(value *Peer) {
			value.AllowedSources = nil
		},
		"duplicate sources": func(value *Peer) {
			value.AllowedSources = []string{"codex", "codex"}
		},
		"empty runtimes": func(value *Peer) {
			value.AllowedRuntimes = nil
		},
		"duplicate runtimes": func(value *Peer) {
			value.AllowedRuntimes = []string{"ssh", "ssh"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validRelay().Peers[0]
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestStrictLoaderRejectsMalformedShapesDuplicatesAndTrailingData(t *testing.T) {
	root := t.TempDir()
	remotePath := filepath.Join(root, "remote.json")
	remote := validRemote(validWSLConnector())
	raw, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	validRaw := string(raw)
	remoteCases := map[string]string{
		"root array":       `[]`,
		"missing field":    strings.Replace(validRaw, `"teamId":"team-main",`, "", 1),
		"null field":       strings.Replace(validRaw, `"teamId":"team-main"`, `"teamId":null`, 1),
		"duplicate field":  strings.Replace(validRaw, `"version":1`, `"version":1,"version":1`, 1),
		"nested duplicate": strings.Replace(validRaw, `"maxBytes":67108864`, `"maxBytes":67108864,"maxBytes":67108864`, 1),
		"unknown nested":   strings.Replace(validRaw, `"maxBytes":67108864`, `"maxBytes":67108864,"command":"run"`, 1),
		"trailing value":   validRaw + ` {}`,
	}
	for name, document := range remoteCases {
		t.Run("remote "+name, func(t *testing.T) {
			if err := os.WriteFile(remotePath, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadRemote(remotePath); err == nil {
				t.Fatal("malformed remote sidecar accepted")
			}
		})
	}

	relayPath := filepath.Join(root, "relay.json")
	relayRaw, err := json.Marshal(validRelay())
	if err != nil {
		t.Fatal(err)
	}
	validRelayRaw := string(relayRaw)
	relayCases := map[string]string{
		"listener null": strings.Replace(
			validRelayRaw,
			`"listener":{"enabled":true,"address":"127.0.0.1:18892"}`,
			`"listener":null`,
			1,
		),
		"listener array": strings.Replace(
			validRelayRaw,
			`"listener":{"enabled":true,"address":"127.0.0.1:18892"}`,
			`"listener":[]`,
			1,
		),
		"listener missing enabled": strings.Replace(
			validRelayRaw,
			`"enabled":true,`,
			"",
			1,
		),
		"peers null":   strings.Replace(validRelayRaw, `"peers":[`, `"peers":null,"ignored":[`, 1),
		"peers object": strings.Replace(validRelayRaw, `"peers":[`, `"peers":{"peer":`, 1),
		"peer null":    strings.Replace(validRelayRaw, `"peers":[{`, `"peers":[null,{`, 1),
		"peer missing id": strings.Replace(
			validRelayRaw,
			`"id":"peer-laptop",`,
			"",
			1,
		),
	}
	for name, document := range relayCases {
		t.Run("relay "+name, func(t *testing.T) {
			if err := os.WriteFile(relayPath, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadRelay(relayPath); err == nil {
				t.Fatal("malformed relay sidecar accepted")
			}
		})
	}
}

func TestStoreRejectsNilEmptyAndOversizedInputs(t *testing.T) {
	if err := SaveRemote("unused", nil); err == nil {
		t.Fatal("nil remote accepted")
	}
	if err := SaveRelay("unused", nil); err == nil {
		t.Fatal("nil relay accepted")
	}
	remote := validRemote(validWSLConnector())
	if err := SaveRemote("", &remote); err == nil {
		t.Fatal("empty path accepted")
	}
	relay := validRelay()
	relay.Version++
	if err := SaveRelay("unused", &relay); err == nil {
		t.Fatal("invalid relay accepted")
	}

	path := filepath.Join(t.TempDir(), "oversized.json")
	document := strings.Repeat(" ", maximumSidecarBytes+1)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRemote(path); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized sidecar error = %v", err)
	}

	directoryPath := t.TempDir()
	if _, err := LoadRemote(directoryPath); err == nil {
		t.Fatal("directory read was accepted as a sidecar")
	}
}

func TestVendorCloudCapabilityGateUsesSentinel(t *testing.T) {
	connector := Connector{
		Type: "vendor-cloud",
		VendorCloud: &VendorCloudConnector{
			Provider:   "provider",
			Capability: "outbound-hook-v1",
			Endpoint:   "https://cloud.example.com/events",
		},
	}
	if err := connector.Validate("vendor-cloud"); !errors.Is(
		err,
		ErrVendorCloudUnsupported,
	) {
		t.Fatalf("vendor cloud gate = %v", err)
	}
}

func relayWithSecondPeer() RelayConfig {
	value := validRelay()
	second := value.Peers[0]
	second.ID = "peer-server"
	second.OriginID = "origin-server"
	second.PublicKey = strings.Repeat("A", 43)
	value.Peers = append(value.Peers, second)
	return value
}

func pointerSSH(value SSHConnector) *SSHConnector {
	return &value
}

func pointerContainer(value ContainerConnector) *ContainerConnector {
	return &value
}
