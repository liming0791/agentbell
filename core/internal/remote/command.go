package remote

import (
	"errors"
	"strconv"
	"strings"

	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

var (
	ErrInvalidRemoteConfig    = errors.New("invalid remote connector configuration")
	ErrNotPullConnector       = errors.New("remote connector does not use host-pull")
	ErrVendorCloudUnsupported = remoteconfig.ErrVendorCloudUnsupported
)

// CommandSpec is an argv-only process invocation. It deliberately has no
// command string, environment or shell field.
type CommandSpec struct {
	Executable string
	Arguments  []string
}

func (spec CommandSpec) String() string {
	return "remote.CommandSpec{Executable:<redacted>, Arguments:<redacted>}"
}

func (spec CommandSpec) GoString() string {
	return spec.String()
}

func BuildPullCommand(config remoteconfig.RemoteConfig) (CommandSpec, error) {
	if err := config.Validate(); err != nil {
		if errors.Is(err, remoteconfig.ErrVendorCloudUnsupported) {
			return CommandSpec{}, ErrVendorCloudUnsupported
		}
		return CommandSpec{}, ErrInvalidRemoteConfig
	}
	return BuildPullCommandForConnector(config.Runtime, config.Connector)
}

// BuildPullCommandForConnector accepts the host-owned connector view. It does
// not require or inspect the remote machine's outbox or private-key metadata.
func BuildPullCommandForConnector(
	runtimeName string,
	connectorValue remoteconfig.Connector,
) (CommandSpec, error) {
	if err := connectorValue.Validate(runtimeName); err != nil {
		return CommandSpec{}, ErrInvalidRemoteConfig
	}
	switch connectorValue.Type {
	case "wsl":
		connector := connectorValue.WSL
		return CommandSpec{
			Executable: connector.HostExecutable.Value,
			Arguments: []string{
				"-d", connector.Distribution,
				"--exec", connector.RemoteExecutable.Value,
				"remote", "drain", "--stdio",
			},
		}, nil
	case "ssh":
		connector := connectorValue.SSH
		// OpenSSH's exec channel is implemented by the server through the
		// account shell. OpenSSH joins command arguments with spaces, so only a
		// deliberately narrow absolute executable token is safe to transmit.
		if !safeSSHRemoteExecutable(connector.RemoteExecutable.Value) {
			return CommandSpec{}, ErrInvalidRemoteConfig
		}
		return CommandSpec{
			Executable: connector.HostExecutable.Value,
			Arguments: []string{
				"-T",
				"-o", "BatchMode=yes",
				"-o", "StrictHostKeyChecking=yes",
				"-o", "UserKnownHostsFile=" + connector.KnownHostsFile.Value,
				"-p", strconv.Itoa(connector.Port),
				"--", connector.User + "@" + connector.Host,
				connector.RemoteExecutable.Value,
				"remote", "drain", "--stdio",
			},
		}, nil
	case "container":
		connector := connectorValue.Container
		return CommandSpec{
			Executable: connector.HostExecutable.Value,
			Arguments: []string{
				"exec", "-i", "--", connector.ContainerID,
				connector.RemoteExecutable.Value,
				"remote", "drain", "--stdio",
			},
		}, nil
	case "https":
		return CommandSpec{}, ErrNotPullConnector
	default:
		return CommandSpec{}, ErrInvalidRemoteConfig
	}
}

func safeSSHRemoteExecutable(value string) bool {
	if !strings.HasPrefix(value, "/") {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case strings.ContainsRune("_./-", character):
		default:
			return false
		}
	}
	return true
}
