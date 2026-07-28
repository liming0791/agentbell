package adapter

import (
	"errors"
	"path/filepath"
	"strings"
)

const (
	stableBridgeProtocol = 1
	bridgeReceiptVersion = 2
)

type hookInvocation struct {
	Executable           string
	Args                 []string
	BridgeProtocol       int
	ActivationGeneration uint64
}

func resolveHookInvocation(
	coreExecutable,
	bridgeExecutable string,
	activeGeneration uint64,
	adapterID,
	surface,
	runtimeName string,
) (hookInvocation, error) {
	executable := coreExecutable
	command := "emit"
	protocol := 0
	generation := uint64(0)
	if bridgeExecutable != "" {
		executable = bridgeExecutable
		command = "hook-v1"
		protocol = stableBridgeProtocol
		generation = activeGeneration
		if activeGeneration == 0 {
			return hookInvocation{}, errors.New(
				"stable AgentBell bridge requires an active generation",
			)
		}
		if !filepath.IsAbs(bridgeExecutable) {
			return hookInvocation{}, errors.New(
				"stable AgentBell bridge path must be absolute",
			)
		}
	}
	if executable == "" || strings.ContainsAny(executable, "\x00\r\n\"") {
		return hookInvocation{}, errors.New(
			"AgentBell hook executable path contains unsupported characters",
		)
	}
	return hookInvocation{
		Executable: executable,
		Args: []string{
			command,
			"--adapter", adapterID,
			"--surface", surface,
			"--runtime", runtimeName,
			"--stdin",
			"--fail-open",
		},
		BridgeProtocol:       protocol,
		ActivationGeneration: generation,
	}, nil
}

func (invocation hookInvocation) shellCommand(windows bool) string {
	arguments := " " + strings.Join(invocation.Args, " ")
	if windows {
		return `"` + invocation.Executable + `"` + arguments
	}
	return shellQuote(invocation.Executable) + arguments
}

func receiptVersion(invocation hookInvocation) int {
	if invocation.BridgeProtocol == stableBridgeProtocol {
		return bridgeReceiptVersion
	}
	return 1
}

func validateReceiptBridge(
	version,
	bridgeProtocol int,
	activationGeneration uint64,
) error {
	switch version {
	case 1:
		if bridgeProtocol != 0 || activationGeneration != 0 {
			return errors.New("legacy adapter receipt contains bridge state")
		}
	case bridgeReceiptVersion:
		if bridgeProtocol != stableBridgeProtocol || activationGeneration == 0 {
			return errors.New("stable bridge adapter receipt is incomplete")
		}
	default:
		return errors.New("unsupported adapter receipt version")
	}
	return nil
}

func bridgeReceiptActive(
	version,
	bridgeProtocol int,
	activationGeneration uint64,
) bool {
	return version == bridgeReceiptVersion &&
		bridgeProtocol == stableBridgeProtocol &&
		activationGeneration != 0
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func plannedHookExecutable(coreExecutable, bridgeExecutable string) string {
	if bridgeExecutable != "" {
		return bridgeExecutable
	}
	return coreExecutable
}
