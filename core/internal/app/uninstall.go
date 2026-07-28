package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/liming0791/agentbell/core/internal/adapter"
	"github.com/liming0791/agentbell/core/internal/installstate"
	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
	"github.com/liming0791/agentbell/core/internal/secretstore"
	"github.com/liming0791/agentbell/core/internal/service"
)

type uninstallAssetStatus struct {
	Configured bool   `json:"configured"`
	Healthy    bool   `json:"healthy"`
	Status     string `json:"status"`
	ErrorCode  string `json:"errorCode"`
}

type uninstallRemoteAssets struct {
	RemoteConfig         uninstallAssetStatus `json:"remoteConfig"`
	HostConnectors       uninstallAssetStatus `json:"hostConnectors"`
	Relay                uninstallAssetStatus `json:"relay"`
	ActiveInstall        uninstallAssetStatus `json:"activeInstall"`
	HostConnectorCount   int                  `json:"hostConnectorCount"`
	RelayPeerCount       int                  `json:"relayPeerCount"`
	CredentialConfigured bool                 `json:"credentialConfigured"`
	CredentialBackend    string               `json:"credentialBackend"`
	CredentialAction     string               `json:"credentialAction"`
	ActiveVersion        string               `json:"activeVersion"`
	ActiveGeneration     uint64               `json:"activeGeneration"`
	privateKeyReference  remoteconfig.PrivateKeyRef
}

type productUninstallReport struct {
	DryRun       bool                    `json:"dryRun"`
	Service      service.ManagerResult   `json:"service"`
	Adapters     []adapter.AdapterResult `json:"adapters"`
	RemoteAssets uninstallRemoteAssets   `json:"remoteAssets"`
	CoreCleanup  string                  `json:"coreCleanup"`
	Preserved    []string                `json:"preserved"`
}

type uninstallCredentialStore interface {
	Delete(context.Context, remoteconfig.PrivateKeyRef) error
}

var newUninstallCredentialStore = func(
	managedRoot string,
) (uninstallCredentialStore, error) {
	return secretstore.New(managedRoot)
}

func runProductUninstall(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "show changes without applying them")
	asJSON := flags.Bool("json", false, "print JSON")
	deleteCredential := flags.Bool(
		"delete-remote-credential",
		false,
		"delete the configured remote private key",
	)
	confirmDeleteCredential := flags.Bool(
		"confirm-delete-remote-credential",
		false,
		"confirm deletion of the configured remote private key",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return productUninstallUsageError()
	}
	if *confirmDeleteCredential && !*deleteCredential {
		return errors.New(
			"--confirm-delete-remote-credential requires --delete-remote-credential",
		)
	}
	if *deleteCredential && !*confirmDeleteCredential {
		return errors.New(
			"remote credential deletion requires --confirm-delete-remote-credential",
		)
	}

	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	remoteAssets := inspectUninstallRemoteAssets(resolved)
	if *deleteCredential {
		if !remoteAssets.RemoteConfig.Healthy {
			return errors.New(
				"cannot safely delete the remote credential while remote.json is invalid",
			)
		}
		if remoteAssets.CredentialConfigured {
			remoteAssets.CredentialAction = "would-delete"
		}
	}
	var credentialStore uninstallCredentialStore
	if !*dryRun && *deleteCredential && remoteAssets.CredentialConfigured {
		credentialStore, err = newUninstallCredentialStore(resolved.DataDir)
		if err != nil {
			return fmt.Errorf("open remote credential store: %w", err)
		}
	}

	manager, err := configuredServiceManager("", resolved)
	if err != nil {
		return err
	}
	selected, err := supportedAdapters(resolved)
	if err != nil {
		return err
	}
	serviceResult, err := manager.Uninstall(context.Background(), true)
	if err != nil {
		return fmt.Errorf("preflight service uninstall: %w", err)
	}
	adapterResults, err := uninstallAdapters(selected, true)
	if err != nil {
		return fmt.Errorf("preflight %w", err)
	}

	if !*dryRun {
		serviceResult, err = manager.Uninstall(context.Background(), false)
		if err != nil {
			return fmt.Errorf("uninstall service: %w", err)
		}
		adapterResults, err = uninstallAdapters(selected, false)
		if err != nil {
			return err
		}
		if *deleteCredential && remoteAssets.CredentialConfigured {
			deleteErr := credentialStore.Delete(
				context.Background(),
				remoteAssets.privateKeyReference,
			)
			switch {
			case deleteErr == nil:
				remoteAssets.CredentialAction = "deleted"
			case errors.Is(deleteErr, secretstore.ErrNotFound):
				remoteAssets.CredentialAction = "already-absent"
			default:
				return fmt.Errorf("delete remote credential: %w", deleteErr)
			}
		}
	}

	report := productUninstallReport{
		DryRun:       *dryRun,
		Service:      serviceResult,
		Adapters:     adapterResults,
		RemoteAssets: remoteAssets,
		CoreCleanup:  "npm bootstrap removes its managed Core version after this process exits; direct binary invocations retain the executable",
		Preserved: []string{
			resolved.ConfigFile,
			filepath.Join(filepath.Dir(resolved.ConfigFile), "remote.json"),
			filepath.Join(
				filepath.Dir(resolved.ConfigFile),
				"host-connectors.json",
			),
			filepath.Join(filepath.Dir(resolved.ConfigFile), "relay.json"),
			resolved.StateDir,
		},
	}
	if *asJSON {
		return writeJSON(stdout, report)
	}
	writeHumanProductUninstall(stdout, report)
	return nil
}

func productUninstallUsageError() error {
	return errors.New(
		"usage: agentbell uninstall [--dry-run] [--json] " +
			"[--delete-remote-credential --confirm-delete-remote-credential]",
	)
}

func inspectUninstallRemoteAssets(resolved paths.Paths) uninstallRemoteAssets {
	result := uninstallRemoteAssets{
		RemoteConfig:     missingUninstallAsset(),
		HostConnectors:   missingUninstallAsset(),
		Relay:            missingUninstallAsset(),
		ActiveInstall:    missingUninstallAsset(),
		CredentialAction: "not-configured",
	}
	configRoot := filepath.Dir(resolved.ConfigFile)

	remoteValue, err := remoteconfig.LoadRemote(
		filepath.Join(configRoot, "remote.json"),
	)
	switch {
	case err == nil:
		result.RemoteConfig = configuredUninstallAsset()
		result.CredentialConfigured = true
		result.CredentialBackend = remoteValue.PrivateKeyRef.Store
		result.CredentialAction = "preserved"
		result.privateKeyReference = remoteValue.PrivateKeyRef
	case errors.Is(err, remoteconfig.ErrNotFound):
	default:
		result.RemoteConfig = invalidUninstallAsset("remote_config_invalid")
	}

	hostValue, err := remoteconfig.LoadHostConnectors(
		filepath.Join(configRoot, "host-connectors.json"),
	)
	switch {
	case err == nil:
		result.HostConnectors = configuredUninstallAsset()
		result.HostConnectorCount = len(hostValue.Connectors)
	case errors.Is(err, remoteconfig.ErrNotFound):
	default:
		result.HostConnectors = invalidUninstallAsset(
			"host_connectors_invalid",
		)
	}

	relayValue, err := remoteconfig.LoadRelay(
		filepath.Join(configRoot, "relay.json"),
	)
	switch {
	case err == nil:
		result.Relay = configuredUninstallAsset()
		result.RelayPeerCount = len(relayValue.Peers)
	case errors.Is(err, remoteconfig.ErrNotFound):
	default:
		result.Relay = invalidUninstallAsset("relay_config_invalid")
	}

	activeValue, err := installstate.NewStore(nil).Load(resolved.DataDir)
	switch {
	case err == nil:
		result.ActiveInstall = configuredUninstallAsset()
		result.ActiveVersion = activeValue.ActiveVersion
		result.ActiveGeneration = activeValue.Generation
	case errors.Is(err, fs.ErrNotExist):
	default:
		result.ActiveInstall = invalidUninstallAsset(
			"active_install_invalid",
		)
	}
	return result
}

func missingUninstallAsset() uninstallAssetStatus {
	return uninstallAssetStatus{
		Healthy: true,
		Status:  "missing",
	}
}

func configuredUninstallAsset() uninstallAssetStatus {
	return uninstallAssetStatus{
		Configured: true,
		Healthy:    true,
		Status:     "ok",
	}
}

func invalidUninstallAsset(code string) uninstallAssetStatus {
	return uninstallAssetStatus{
		Configured: true,
		Status:     "invalid",
		ErrorCode:  code,
	}
}

func writeHumanProductUninstall(
	stdout io.Writer,
	report productUninstallReport,
) {
	if report.DryRun {
		fmt.Fprintln(stdout, "AgentBell product uninstall plan:")
	} else {
		fmt.Fprintln(
			stdout,
			"AgentBell login service and supported product hooks are uninstalled.",
		)
	}
	fmt.Fprintf(stdout, "Service: %s\n", report.Service.Message)
	for _, result := range report.Adapters {
		fmt.Fprintf(stdout, "%s: %s\n", result.Adapter, result.Message)
	}
	fmt.Fprintf(
		stdout,
		"Remote assets: connectors=%d peers=%d credential=%s activeVersion=%s\n",
		report.RemoteAssets.HostConnectorCount,
		report.RemoteAssets.RelayPeerCount,
		report.RemoteAssets.CredentialAction,
		report.RemoteAssets.ActiveVersion,
	)
	fmt.Fprintln(stdout, report.CoreCleanup)
	fmt.Fprintln(stdout, "Configuration, remote metadata, and queue data were preserved:")
	for _, path := range report.Preserved {
		fmt.Fprintf(stdout, "  %s\n", path)
	}
}
