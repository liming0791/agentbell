package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/remote"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
	"github.com/liming0791/agentbell/core/internal/service"
)

type remoteDoctorSummary struct {
	Configured      bool                      `json:"configured"`
	Healthy         bool                      `json:"healthy"`
	ConnectorCount  int                       `json:"connectorCount"`
	ConnectorCounts map[string]int            `json:"connectorCounts"`
	RuntimeProofs   []remote.HostDoctorReport `json:"runtimeProofs"`
	ErrorCode       string                    `json:"errorCode"`
}

// configuredRemoteWorkers keeps ownership explicit: remote.json is consulted
// only for this machine's HTTPS push outbox, while host-connectors.json owns
// zero or more WSL/SSH/container pull targets.
func configuredRemoteWorkers(
	resolved paths.Paths,
) ([]service.BackgroundWorker, error) {
	var workers []service.BackgroundWorker
	remotePath := filepath.Join(filepath.Dir(resolved.ConfigFile), "remote.json")
	info, err := os.Lstat(remotePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
		return nil, errors.New("remote sidecar must be a regular file")
	default:
		value, loadErr := remoteconfig.LoadRemote(remotePath)
		if loadErr != nil || value.Connector.Type == "https" {
			workers = append(workers, remote.HostScheduler{
				RemoteConfigPath: remotePath,
				StateDir:         resolved.StateDir,
			})
		}
	}

	registryPath := filepath.Join(
		filepath.Dir(resolved.ConfigFile),
		"host-connectors.json",
	)
	registryInfo, err := os.Lstat(registryPath)
	if errors.Is(err, os.ErrNotExist) {
		return workers, nil
	}
	if err != nil {
		return nil, err
	}
	if registryInfo.Mode()&os.ModeSymlink != 0 ||
		!registryInfo.Mode().IsRegular() {
		return nil, errors.New("host connector registry must be a regular file")
	}
	registry, err := remoteconfig.LoadHostConnectors(registryPath)
	if err != nil {
		return nil, err
	}
	relayPath := filepath.Join(filepath.Dir(resolved.ConfigFile), "relay.json")
	for index := range registry.Connectors {
		target := registry.Connectors[index]
		workers = append(workers, remote.HostScheduler{
			RelayConfigPath: relayPath,
			StateDir:        resolved.StateDir,
			Target:          &target,
		})
	}
	return workers, nil
}

func diagnoseRemoteWorkers(resolved paths.Paths) remoteDoctorSummary {
	result := remoteDoctorSummary{
		Healthy:         true,
		ConnectorCounts: map[string]int{},
		RuntimeProofs:   []remote.HostDoctorReport{},
	}
	workers, err := configuredRemoteWorkers(resolved)
	if err != nil {
		result.Healthy = false
		result.ErrorCode = "connector_registry_invalid"
		return result
	}
	result.Configured = len(workers) > 0
	result.ConnectorCount = len(workers)
	for _, worker := range workers {
		scheduler, ok := worker.(remote.HostScheduler)
		if !ok {
			result.Healthy = false
			result.ErrorCode = "connector_runtime_invalid"
			continue
		}
		report, doctorErr := scheduler.Doctor(context.Background())
		if doctorErr != nil {
			result.Healthy = false
			result.ErrorCode = "connector_status_unavailable"
			continue
		}
		result.RuntimeProofs = append(result.RuntimeProofs, report)
		result.ConnectorCounts[report.Connector]++
		if !report.Healthy {
			result.Healthy = false
		}
	}
	return result
}
