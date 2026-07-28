package remote

import (
	"errors"
	"reflect"
	"testing"

	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

func TestBuildPullCommandUsesExactArgumentFixtures(t *testing.T) {
	tests := []struct {
		name       string
		config     remoteconfig.RemoteConfig
		executable string
		arguments  []string
	}{
		{
			name:       "windows wsl host pull",
			config:     validRemoteConfig("wsl"),
			executable: `C:\Windows\System32\wsl.exe`,
			arguments: []string{
				"-d", "Ubuntu-24.04",
				"--exec", "/usr/local/bin/agentbell",
				"remote", "drain", "--stdio",
			},
		},
		{
			name:       "darwin openssh",
			config:     validRemoteConfig("ssh"),
			executable: "/usr/bin/ssh",
			arguments: []string{
				"-T",
				"-o", "BatchMode=yes",
				"-o", "StrictHostKeyChecking=yes",
				"-o", "UserKnownHostsFile=/Users/test/.ssh/known_hosts",
				"-p", "2222",
				"--", "agentbell@build.example.com",
				"/usr/local/bin/agentbell", "remote", "drain", "--stdio",
			},
		},
		{
			name:       "linux container",
			config:     validRemoteConfig("container"),
			executable: "/usr/bin/docker",
			arguments: []string{
				"exec", "-i", "--", "worker-01",
				"/usr/local/bin/agentbell", "remote", "drain", "--stdio",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, err := BuildPullCommand(test.config)
			if err != nil {
				t.Fatal(err)
			}
			if spec.Executable != test.executable {
				t.Fatalf("executable = %q", spec.Executable)
			}
			if !reflect.DeepEqual(spec.Arguments, test.arguments) {
				t.Fatalf("arguments = %#v", spec.Arguments)
			}
			if spec.String() == "" ||
				containsAny(spec.String(), test.executable, "build.example.com", "worker-01") {
				t.Fatalf("command String leaked configuration: %q", spec.String())
			}
		})
	}
}

func TestBuildPullCommandRejectsHTTPSAndVendorCloud(t *testing.T) {
	if _, err := BuildPullCommand(validRemoteConfig("https")); !errors.Is(
		err,
		ErrNotPullConnector,
	) {
		t.Fatalf("HTTPS pull error = %v", err)
	}
	config := validRemoteConfig("wsl")
	config.Runtime = "vendor-cloud"
	config.Connector = remoteconfig.Connector{
		Type: "vendor-cloud",
		VendorCloud: &remoteconfig.VendorCloudConnector{
			Provider:   "kimi",
			Capability: "unverified",
			Endpoint:   "https://cloud.example.com/hook",
		},
	}
	if _, err := BuildPullCommand(config); !errors.Is(
		err,
		ErrVendorCloudUnsupported,
	) {
		t.Fatalf("vendor-cloud error = %v", err)
	}
}

func TestBuildPullCommandRejectsInvalidConfigurationWithoutLeakingIt(t *testing.T) {
	config := validRemoteConfig("ssh")
	config.Connector.SSH.Host = "secret.example.com;cat /private/key"
	_, err := BuildPullCommand(config)
	if !errors.Is(err, ErrInvalidRemoteConfig) {
		t.Fatalf("error = %v", err)
	}
	if containsAny(err.Error(), "secret.example.com", "/private/key") {
		t.Fatalf("configuration leaked in error: %v", err)
	}

	config = validRemoteConfig("ssh")
	config.Connector.SSH.RemoteExecutable.Value =
		"/usr/local/$(touch /tmp/pwned)/agentbell"
	if _, err := BuildPullCommand(config); !errors.Is(
		err,
		ErrInvalidRemoteConfig,
	) {
		t.Fatalf("unsafe remote command error = %v", err)
	}
	if containsAny(err.Error(), "touch", "/tmp/pwned") {
		t.Fatalf("remote command leaked in error: %v", err)
	}
}

func validRemoteConfig(kind string) remoteconfig.RemoteConfig {
	config := remoteconfig.RemoteConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: "2.0.0",
		TeamID:         "team-main",
		OriginID:       "origin-main",
		Runtime:        kind,
		Outbox: remoteconfig.Outbox{
			Path: remoteconfig.PathRef{
				Platform: "linux",
				Value:    "/var/lib/agentbell/outbox",
			},
			MaxBytes: 64 << 20,
		},
		PrivateKeyRef: remoteconfig.PrivateKeyRef{
			Store: "secret-service",
			ID:    "agentbell/origin-main",
		},
	}
	switch kind {
	case "wsl":
		config.Connector = remoteconfig.Connector{
			Type: "wsl",
			WSL: &remoteconfig.WSLConnector{
				Distribution: "Ubuntu-24.04",
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
	case "ssh":
		config.Connector = remoteconfig.Connector{
			Type: "ssh",
			SSH: &remoteconfig.SSHConnector{
				Host: "build.example.com",
				Port: 2222,
				User: "agentbell",
				HostExecutable: remoteconfig.PathRef{
					Platform: "darwin",
					Value:    "/usr/bin/ssh",
				},
				KnownHostsFile: remoteconfig.PathRef{
					Platform: "darwin",
					Value:    "/Users/test/.ssh/known_hosts",
				},
				RemoteExecutable: remoteconfig.PathRef{
					Platform: "linux",
					Value:    "/usr/local/bin/agentbell",
				},
			},
		}
	case "container":
		config.Connector = remoteconfig.Connector{
			Type: "container",
			Container: &remoteconfig.ContainerConnector{
				Runtime: "docker",
				HostExecutable: remoteconfig.PathRef{
					Platform: "linux",
					Value:    "/usr/bin/docker",
				},
				ContainerID: "worker-01",
				RemoteExecutable: remoteconfig.PathRef{
					Platform: "linux",
					Value:    "/usr/local/bin/agentbell",
				},
			},
		}
	case "https":
		config.Runtime = "ssh"
		config.Connector = remoteconfig.Connector{
			Type: "https",
			HTTPS: &remoteconfig.HTTPSConnector{
				Endpoint: "https://relay.example.com/v1/events",
			},
		}
	}
	return config
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && len(value) >= len(needle) {
			for index := 0; index+len(needle) <= len(value); index++ {
				if value[index:index+len(needle)] == needle {
					return true
				}
			}
		}
	}
	return false
}
