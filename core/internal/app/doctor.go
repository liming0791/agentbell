package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/installstate"
	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/queue"
	"github.com/liming0791/agentbell/core/internal/settings"
	"github.com/liming0791/agentbell/core/internal/version"
)

const doctorSchemaVersion = 1

type doctorReport struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Healthy       bool                        `json:"healthy"`
	Version       version.Info                `json:"version"`
	Platform      string                      `json:"platform"`
	Architecture  string                      `json:"architecture"`
	Config        doctorConfigStatus          `json:"config"`
	Settings      doctorSettingsStatus        `json:"settings"`
	LarkCLI       doctorComponentStatus       `json:"larkCli"`
	Install       doctorInstallStatus         `json:"install"`
	Plugin        doctorPluginCapability      `json:"plugin"`
	SecretStore   doctorSecretStoreCapability `json:"secretStore"`
	Queue         doctorQueueStatus           `json:"queue"`
	Adapters      []doctorAdapterStatus       `json:"adapters"`
	Relay         remoteDoctorSummary         `json:"relay"`
}

type doctorComponentStatus struct {
	Configured bool   `json:"configured"`
	Healthy    bool   `json:"healthy"`
	Status     string `json:"status"`
	ErrorCode  string `json:"errorCode"`
}

type doctorConfigStatus struct {
	Configured   bool   `json:"configured"`
	Healthy      bool   `json:"healthy"`
	Status       string `json:"status"`
	ChannelCount int    `json:"channelCount"`
	ErrorCode    string `json:"errorCode"`
}

type doctorSettingsStatus struct {
	Configured     bool   `json:"configured"`
	Healthy        bool   `json:"healthy"`
	Status         string `json:"status"`
	Version        int    `json:"version"`
	MinCoreVersion string `json:"minCoreVersion"`
	ErrorCode      string `json:"errorCode"`
}

type doctorInstallStatus struct {
	Configured         bool   `json:"configured"`
	Healthy            bool   `json:"healthy"`
	Mode               string `json:"mode"`
	ActiveVersion      string `json:"activeVersion"`
	PreviousVersion    string `json:"previousVersion"`
	Generation         uint64 `json:"generation"`
	Target             string `json:"target"`
	ChecksumStatus     string `json:"checksumStatus"`
	StableBridgeStatus string `json:"stableBridgeStatus"`
	SignatureStatus    string `json:"signatureStatus"`
	ErrorCode          string `json:"errorCode"`
}

type doctorPluginCapability struct {
	VerificationAvailable   bool   `json:"verificationAvailable"`
	Capability              string `json:"capability"`
	RequiredSignatureStatus string `json:"requiredSignatureStatus"`
}

type doctorSecretStoreCapability struct {
	Backend                         string `json:"backend"`
	Available                       bool   `json:"available"`
	Status                          string `json:"status"`
	FileFallbackAvailable           bool   `json:"fileFallbackAvailable"`
	RequiresExplicitAcknowledgement bool   `json:"requiresExplicitAcknowledgement"`
}

type doctorQueueStatus struct {
	Healthy   bool   `json:"healthy"`
	Pending   int    `json:"pending"`
	Inflight  int    `json:"inflight"`
	History   int    `json:"history"`
	Dead      int    `json:"dead"`
	ErrorCode string `json:"errorCode"`
}

type doctorAdapterStatus struct {
	Adapter         string `json:"adapter"`
	Detected        bool   `json:"detected"`
	Installed       bool   `json:"installed"`
	RuntimeVerified bool   `json:"runtimeVerified"`
	Status          string `json:"status"`
	LastSeen        string `json:"lastSeen"`
}

var inspectDoctorSecretStore = nativeSecretStoreCapability

func runUnifiedDoctor(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: agentbell doctor [--json]")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	report := buildDoctorReport(resolved)
	if *asJSON {
		return writeJSON(stdout, report)
	}
	writeHumanDoctor(stdout, report)
	return nil
}

func buildDoctorReport(resolved paths.Paths) doctorReport {
	configStatus, loadedConfig := inspectDoctorConfig(resolved.ConfigFile)
	settingsStatus := inspectDoctorSettings(filepath.Join(
		filepath.Dir(resolved.ConfigFile),
		"settings.json",
	))
	larkStatus := inspectDoctorLarkCLI(configStatus, loadedConfig)
	installStatus := inspectDoctorInstall(resolved)
	queueStatus := inspectDoctorQueue(resolved.StateDir)
	adapterStatuses := inspectDoctorAdapters(resolved)
	relayStatus := diagnoseRemoteWorkers(resolved)

	healthy := configStatus.Healthy &&
		settingsStatus.Healthy &&
		larkStatus.Healthy &&
		installStatus.Healthy &&
		queueStatus.Healthy &&
		relayStatus.Healthy
	return doctorReport{
		SchemaVersion: doctorSchemaVersion,
		Healthy:       healthy,
		Version:       version.Current(),
		Platform:      runtime.GOOS,
		Architecture:  runtime.GOARCH,
		Config:        configStatus,
		Settings:      settingsStatus,
		LarkCLI:       larkStatus,
		Install:       installStatus,
		Plugin: doctorPluginCapability{
			VerificationAvailable:   true,
			Capability:              "sigstore-keyless",
			RequiredSignatureStatus: "sigstore-verified",
		},
		SecretStore: inspectDoctorSecretStore(),
		Queue:       queueStatus,
		Adapters:    adapterStatuses,
		Relay:       relayStatus,
	}
}

func inspectDoctorConfig(path string) (doctorConfigStatus, config.Config) {
	value, err := config.Load(path)
	switch {
	case err == nil:
		return doctorConfigStatus{
			Configured:   true,
			Healthy:      true,
			Status:       "ok",
			ChannelCount: len(value.Channels),
		}, value
	case errors.Is(err, config.ErrNotFound):
		return doctorConfigStatus{
			Status:    "missing",
			ErrorCode: "config_missing",
		}, config.Config{}
	default:
		return doctorConfigStatus{
			Configured: true,
			Status:     "invalid",
			ErrorCode:  "config_invalid",
		}, config.Config{}
	}
}

func inspectDoctorSettings(path string) doctorSettingsStatus {
	value, err := settings.Load(path)
	switch {
	case err == nil:
		return doctorSettingsStatus{
			Configured:     true,
			Healthy:        true,
			Status:         "ok",
			Version:        value.Version,
			MinCoreVersion: value.MinCoreVersion,
		}
	case errors.Is(err, settings.ErrNotFound):
		return doctorSettingsStatus{
			Healthy: true,
			Status:  "legacy-compatible",
		}
	default:
		return doctorSettingsStatus{
			Configured: true,
			Status:     "invalid",
			ErrorCode:  "settings_invalid",
		}
	}
}

func inspectDoctorLarkCLI(
	configStatus doctorConfigStatus,
	loaded config.Config,
) doctorComponentStatus {
	configured := configStatus.Configured && loaded.LarkCLIPath != ""
	var err error
	if configured {
		_, err = os.Stat(loaded.LarkCLIPath)
	} else {
		_, err = exec.LookPath("lark-cli")
	}
	if err == nil {
		return doctorComponentStatus{
			Configured: configured,
			Healthy:    true,
			Status:     "available",
		}
	}
	return doctorComponentStatus{
		Configured: configured,
		Status:     "missing",
		ErrorCode:  "lark_cli_missing",
	}
}

func inspectDoctorInstall(resolved paths.Paths) doctorInstallStatus {
	result := doctorInstallStatus{
		Healthy:            true,
		Mode:               "legacy",
		ChecksumStatus:     "not-managed",
		StableBridgeStatus: "legacy",
		SignatureStatus:    "unmanaged",
	}
	store := installstate.NewStore(installstate.OSFileSystem{})
	active, err := store.Load(resolved.DataDir)
	if errors.Is(err, fs.ErrNotExist) {
		if _, bridgeErr := inspectBridgeRuntime(resolved); bridgeErr != nil {
			result.Healthy = false
			result.StableBridgeStatus = "unavailable"
			result.ErrorCode = "legacy_runtime_unavailable"
		}
		return result
	}
	if err != nil {
		result.Configured = true
		result.Healthy = false
		result.Mode = "invalid"
		result.ChecksumStatus = "invalid"
		result.StableBridgeStatus = "invalid"
		result.SignatureStatus = "unknown"
		result.ErrorCode = "active_state_invalid"
		return result
	}

	result.Configured = true
	result.Mode = "active"
	result.ActiveVersion = active.ActiveVersion
	result.PreviousVersion = active.PreviousVersion
	result.Generation = active.Generation
	result.Target = active.Target
	result.ChecksumStatus = "unverified"
	result.StableBridgeStatus = "unverified"
	result.SignatureStatus = "unknown"
	bridgeReport, bridgeErr := inspectBridgeRuntime(resolved)
	if bridgeErr != nil {
		result.Healthy = false
		result.ChecksumStatus = "invalid"
		result.StableBridgeStatus = "invalid"
		result.ErrorCode = "active_runtime_invalid"
		return result
	}
	result.ChecksumStatus = "verified"
	result.StableBridgeStatus = "verified"
	result.SignatureStatus = bridgeReport.SignatureStatus
	return result
}

func inspectDoctorQueue(stateDir string) doctorQueueStatus {
	value, err := queue.Open(filepath.Join(stateDir, "queue"))
	if err != nil {
		return doctorQueueStatus{
			ErrorCode: "queue_unavailable",
		}
	}
	stats, err := value.Stats()
	if err != nil {
		return doctorQueueStatus{
			ErrorCode: "queue_status_unavailable",
		}
	}
	return doctorQueueStatus{
		Healthy:  true,
		Pending:  stats.Pending,
		Inflight: stats.Inflight,
		History:  stats.History,
		Dead:     stats.Dead,
	}
}

func inspectDoctorAdapters(resolved paths.Paths) []doctorAdapterStatus {
	selected, err := supportedAdapters(resolved)
	if err != nil {
		result := make([]doctorAdapterStatus, 0, len(supportedAdapterIDs))
		for _, id := range supportedAdapterIDs {
			result = append(result, doctorAdapterStatus{
				Adapter: id,
				Status:  "runtime-unavailable",
			})
		}
		return result
	}
	result := make([]doctorAdapterStatus, 0, len(selected))
	for _, value := range selected {
		diagnosed := value.Diagnose()
		status := "not-detected"
		switch {
		case diagnosed.RuntimeVerified:
			status = "runtime-verified"
		case diagnosed.Installed:
			status = "installed-unverified"
		case diagnosed.Detected:
			status = "not-installed"
		}
		result = append(result, doctorAdapterStatus{
			Adapter:         diagnosed.Adapter,
			Detected:        diagnosed.Detected,
			Installed:       diagnosed.Installed,
			RuntimeVerified: diagnosed.RuntimeVerified,
			Status:          status,
			LastSeen:        diagnosed.LastSeen,
		})
	}
	return result
}

func nativeSecretStoreCapability() doctorSecretStoreCapability {
	result := doctorSecretStoreCapability{
		FileFallbackAvailable:           true,
		RequiresExplicitAcknowledgement: true,
	}
	switch runtime.GOOS {
	case "darwin":
		result.Backend = "keychain"
		result.Available = executableFile("/usr/bin/security")
	case "windows":
		result.Backend = "dpapi"
		result.Available = true
	case "linux":
		result.Backend = "secret-service"
		result.Available = executableFile("/usr/bin/secret-tool") ||
			executableFile("/bin/secret-tool")
	default:
		result.Backend = "unsupported"
	}
	if result.Available {
		result.Status = "available"
	} else {
		result.Status = "unavailable"
	}
	return result
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil &&
		info.Mode().IsRegular() &&
		info.Mode().Perm()&0o111 != 0
}

func writeHumanDoctor(writer io.Writer, report doctorReport) {
	fmt.Fprintf(
		writer,
		"AgentBell %s\nOverall: %s\nConfig: %s\nSettings: %s\nlark-cli: %s\n",
		report.Version.Version,
		statusForHealthy(report.Healthy),
		report.Config.Status,
		report.Settings.Status,
		report.LarkCLI.Status,
	)
	fmt.Fprintf(
		writer,
		"Runtime: %s (%s)\nSignature: %s\nPlugin verification: %s\nSecret store: %s (%s)\n",
		statusForHealthy(report.Install.Healthy),
		report.Install.Mode,
		report.Install.SignatureStatus,
		statusForCapability(report.Plugin.VerificationAvailable),
		report.SecretStore.Status,
		report.SecretStore.Backend,
	)
	fmt.Fprintf(
		writer,
		"Queue: %d pending, %d inflight, %d history, %d dead\n",
		report.Queue.Pending,
		report.Queue.Inflight,
		report.Queue.History,
		report.Queue.Dead,
	)
	relayConfiguration := "configured"
	if !report.Relay.Configured {
		relayConfiguration = "unconfigured"
	}
	fmt.Fprintf(
		writer,
		"Relay: %s (%d connectors, %s)\n",
		statusForHealthy(report.Relay.Healthy),
		report.Relay.ConnectorCount,
		relayConfiguration,
	)
	for _, adapterStatus := range report.Adapters {
		fmt.Fprintf(
			writer,
			"%s: %s\n",
			adapterStatus.Adapter,
			adapterStatus.Status,
		)
	}
}

func statusForCapability(available bool) string {
	if available {
		return "available"
	}
	return "unavailable"
}
