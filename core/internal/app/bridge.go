package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/liming0791/agentbell/core/internal/installstate"
	"github.com/liming0791/agentbell/core/internal/paths"
)

const maxInstallMetadataSize = 64 * 1024

type bridgeDoctorReport struct {
	Healthy         bool   `json:"healthy"`
	Mode            string `json:"mode"`
	DataRoot        string `json:"dataRoot"`
	ActiveStatePath string `json:"activeStatePath"`
	CorePath        string `json:"corePath"`
	BridgePath      string `json:"bridgePath,omitempty"`
	Version         string `json:"version,omitempty"`
	Generation      uint64 `json:"generation,omitempty"`
	Target          string `json:"target,omitempty"`
	SignatureStatus string `json:"signatureStatus"`
}

type installMetadata struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Version         string `json:"version"`
	Target          string `json:"target"`
	FileName        string `json:"fileName,omitempty"`
	Checksum        string `json:"checksum"`
	BridgeFileName  string `json:"bridgeFileName,omitempty"`
	BridgeChecksum  string `json:"bridgeChecksum"`
	InstalledAt     string `json:"installedAt,omitempty"`
	SignatureStatus string `json:"signatureStatus"`
	TransactionID   string `json:"transactionId"`
}

func runBridge(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "doctor" {
		return errors.New("usage: agentbell bridge doctor [--json]")
	}
	flags := flag.NewFlagSet("bridge doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: agentbell bridge doctor [--json]")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	report, err := inspectBridgeRuntime(resolved)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(
		stdout,
		"AgentBell bridge: %s\nMode: %s\nCore: %s\nSignature: %s\n",
		statusForHealthy(report.Healthy),
		report.Mode,
		report.CorePath,
		report.SignatureStatus,
	)
	if report.BridgePath != "" {
		fmt.Fprintf(stdout, "Bridge: %s\n", report.BridgePath)
	}
	return nil
}

func inspectBridgeRuntime(resolved paths.Paths) (bridgeDoctorReport, error) {
	activePath, err := installstate.ActiveStatePath(resolved.DataDir)
	if err != nil {
		return bridgeDoctorReport{}, err
	}
	store := installstate.NewStore(installstate.OSFileSystem{})
	active, err := store.Load(resolved.DataDir)
	if errors.Is(err, fs.ErrNotExist) {
		selected, resolveErr := resolveAdapterRuntime(resolved)
		if resolveErr != nil {
			return bridgeDoctorReport{}, resolveErr
		}
		return bridgeDoctorReport{
			Healthy:         true,
			Mode:            "legacy",
			DataRoot:        resolved.DataDir,
			ActiveStatePath: activePath,
			CorePath:        selected.CoreExecutable,
			SignatureStatus: "unmanaged",
		}, nil
	}
	if err != nil {
		return bridgeDoctorReport{}, fmt.Errorf("load active AgentBell runtime: %w", err)
	}
	selected, err := resolveAdapterRuntime(resolved)
	if err != nil {
		return bridgeDoctorReport{}, err
	}
	metadata, err := loadInstallMetadata(filepath.Join(
		filepath.Dir(selected.CoreExecutable),
		"install.json",
	))
	if err != nil {
		return bridgeDoctorReport{}, fmt.Errorf("load active install metadata: %w", err)
	}
	if (metadata.SchemaVersion != 0 && metadata.SchemaVersion != 1) ||
		metadata.Version != active.ActiveVersion ||
		metadata.Target != active.Target ||
		metadata.Checksum != active.Checksum {
		return bridgeDoctorReport{}, errors.New(
			"active install metadata does not match active state",
		)
	}
	if metadata.SignatureStatus != "technical-preview" &&
		metadata.SignatureStatus != "sigstore-verified" {
		return bridgeDoctorReport{}, fmt.Errorf(
			"unsupported active signature status %q",
			metadata.SignatureStatus,
		)
	}
	bridgeBytes, err := os.ReadFile(selected.BridgeExecutable)
	if err != nil {
		return bridgeDoctorReport{}, err
	}
	if installstate.SHA256(bridgeBytes) != active.BridgeChecksum {
		return bridgeDoctorReport{}, errors.New(
			"stable AgentBell bridge checksum does not match active state",
		)
	}
	return bridgeDoctorReport{
		Healthy:         true,
		Mode:            "active",
		DataRoot:        resolved.DataDir,
		ActiveStatePath: activePath,
		CorePath:        selected.CoreExecutable,
		BridgePath:      selected.BridgeExecutable,
		Version:         active.ActiveVersion,
		Generation:      active.Generation,
		Target:          active.Target,
		SignatureStatus: metadata.SignatureStatus,
	}, nil
}

func loadInstallMetadata(path string) (installMetadata, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return installMetadata{}, err
	}
	if len(value) > maxInstallMetadataSize {
		return installMetadata{}, errors.New("install metadata exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var metadata installMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return installMetadata{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return installMetadata{}, errors.New(
				"install metadata contains multiple JSON values",
			)
		}
		return installMetadata{}, err
	}
	return metadata, nil
}

func statusForHealthy(healthy bool) string {
	if healthy {
		return "ok"
	}
	return "error"
}
