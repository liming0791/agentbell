// Package installstate owns the small, version-independent state used by the
// AgentBell bridge to locate the currently active Core.
package installstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	SchemaVersion   = 1
	ActiveStateFile = "active.json"

	maxActiveStateSize = 64 * 1024
)

var (
	baseVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	identifierPattern  = regexp.MustCompile(`^[0-9A-Za-z-]+$`)
	checksumPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	transactionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	allowedTargets     = map[string]bool{
		"windows-amd64": true,
		"windows-arm64": true,
		"darwin-amd64":  true,
		"darwin-arm64":  true,
		"linux-amd64":   true,
		"linux-arm64":   true,
	}
)

type ActiveState struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Generation      uint64 `json:"generation"`
	ActiveVersion   string `json:"activeVersion"`
	PreviousVersion string `json:"previousVersion,omitempty"`
	ServiceVersion  string `json:"serviceVersion,omitempty"`
	Target          string `json:"target"`
	Checksum        string `json:"checksum"`
	ServiceChecksum string `json:"serviceChecksum,omitempty"`
	BridgeChecksum  string `json:"bridgeChecksum"`
	TransactionID   string `json:"transactionId"`
}

func (state ActiveState) Validate() error {
	if state.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported active-state schema version %d", state.SchemaVersion)
	}
	if state.Generation == 0 {
		return errors.New("active-state generation must be greater than zero")
	}
	if !validVersion(state.ActiveVersion) {
		return errors.New("activeVersion must be a valid AgentBell semantic version")
	}
	if state.PreviousVersion != "" {
		if !validVersion(state.PreviousVersion) {
			return errors.New("previousVersion must be a valid AgentBell semantic version")
		}
		if state.PreviousVersion == state.ActiveVersion {
			return errors.New("previousVersion must differ from activeVersion")
		}
	}
	if (state.ServiceVersion == "") != (state.ServiceChecksum == "") {
		return errors.New(
			"serviceVersion and serviceChecksum must be set together",
		)
	}
	if state.ServiceVersion != "" {
		if !validVersion(state.ServiceVersion) {
			return errors.New(
				"serviceVersion must be a valid AgentBell semantic version",
			)
		}
		if !checksumPattern.MatchString(state.ServiceChecksum) {
			return errors.New(
				"serviceChecksum must be a lowercase SHA-256 digest",
			)
		}
	}
	if !allowedTargets[state.Target] {
		return fmt.Errorf("unsupported active-state target %q", state.Target)
	}
	if !checksumPattern.MatchString(state.Checksum) {
		return errors.New("active-state checksum must be a lowercase SHA-256 digest")
	}
	if !checksumPattern.MatchString(state.BridgeChecksum) {
		return errors.New("active-state bridgeChecksum must be a lowercase SHA-256 digest")
	}
	if !transactionPattern.MatchString(state.TransactionID) {
		return errors.New("active-state transactionId contains unsupported characters")
	}
	return nil
}

type AtomicFile interface {
	io.Writer
	Chmod(fs.FileMode) error
	Sync() error
	Close() error
	Name() string
}

// FileSystem is deliberately narrow so transaction and bridge tests can
// inject I/O faults without modifying process-global filesystem state.
type FileSystem interface {
	ReadFile(string) ([]byte, error)
	Lstat(string) (fs.FileInfo, error)
	MkdirAll(string, fs.FileMode) error
	CreateTemp(string, string) (AtomicFile, error)
	Rename(string, string) error
	Remove(string) error
	SyncDir(string) error
}

type OSFileSystem struct{}

func (OSFileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (OSFileSystem) Lstat(path string) (fs.FileInfo, error) {
	return os.Lstat(path)
}

func (OSFileSystem) MkdirAll(path string, mode fs.FileMode) error {
	return os.MkdirAll(path, mode)
}

func (OSFileSystem) CreateTemp(directory, pattern string) (AtomicFile, error) {
	return os.CreateTemp(directory, pattern)
}

func (OSFileSystem) Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func (OSFileSystem) Remove(path string) error {
	return os.Remove(path)
}

func (OSFileSystem) SyncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type Store struct {
	FS FileSystem
}

func NewStore(fileSystem FileSystem) Store {
	return Store{FS: fileSystem}
}

func (store Store) fileSystem() FileSystem {
	if store.FS != nil {
		return store.FS
	}
	return OSFileSystem{}
}

func (store Store) Load(dataRoot string) (ActiveState, error) {
	path, err := ActiveStatePath(dataRoot)
	if err != nil {
		return ActiveState{}, err
	}
	fileSystem := store.fileSystem()
	if err := ensureManagedPath(fileSystem, dataRoot, path); err != nil {
		return ActiveState{}, fmt.Errorf("validate active-state path: %w", err)
	}
	value, err := fileSystem.ReadFile(path)
	if err != nil {
		return ActiveState{}, err
	}
	if len(value) > maxActiveStateSize {
		return ActiveState{}, errors.New("active state exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var state ActiveState
	if err := decoder.Decode(&state); err != nil {
		return ActiveState{}, fmt.Errorf("parse active state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ActiveState{}, errors.New("active state contains multiple JSON values")
		}
		return ActiveState{}, fmt.Errorf("parse active-state trailing data: %w", err)
	}
	if err := state.Validate(); err != nil {
		return ActiveState{}, err
	}
	return state, nil
}

func (store Store) Save(dataRoot string, state ActiveState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	path, err := ActiveStatePath(dataRoot)
	if err != nil {
		return err
	}
	fileSystem := store.fileSystem()
	directory := filepath.Dir(path)
	root := filepath.Clean(dataRoot)
	if info, statErr := fileSystem.Lstat(root); statErr == nil {
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("AgentBell data root is a symlink: %s", root)
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	if err := fileSystem.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := ensureManagedPath(fileSystem, dataRoot, directory); err != nil {
		return fmt.Errorf("validate active-state directory: %w", err)
	}
	if _, err := fileSystem.Lstat(path); err == nil {
		if err := ensureManagedPath(fileSystem, dataRoot, path); err != nil {
			return fmt.Errorf("validate active-state path: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary, err := fileSystem.CreateTemp(directory, ".agentbell-active-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer fileSystem.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := fileSystem.Rename(temporaryPath, path); err != nil {
		return err
	}
	return fileSystem.SyncDir(directory)
}

func ActiveStatePath(dataRoot string) (string, error) {
	root, err := cleanAbsoluteRoot(dataRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "bin", ActiveStateFile), nil
}

func ManagedCorePath(dataRoot string, state ActiveState) (string, error) {
	if err := state.Validate(); err != nil {
		return "", err
	}
	root, err := cleanAbsoluteRoot(dataRoot)
	if err != nil {
		return "", err
	}
	executable := "agentbell"
	if strings.HasPrefix(state.Target, "windows-") {
		executable += ".exe"
	}
	versionsRoot := filepath.Join(root, "bin")
	candidate := filepath.Join(versionsRoot, state.ActiveVersion, executable)
	relative, err := filepath.Rel(versionsRoot, candidate)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return "", errors.New("managed Core path escapes the versions root")
	}
	return candidate, nil
}

func (store Store) ResolveManagedCore(dataRoot string, state ActiveState) (string, error) {
	path, err := ManagedCorePath(dataRoot, state)
	if err != nil {
		return "", err
	}
	fileSystem := store.fileSystem()
	if err := ensureManagedPath(fileSystem, dataRoot, path); err != nil {
		return "", fmt.Errorf("validate managed Core path: %w", err)
	}
	info, err := fileSystem.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("managed Core is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("managed Core is not executable")
	}
	value, err := fileSystem.ReadFile(path)
	if err != nil {
		return "", err
	}
	if SHA256(value) != state.Checksum {
		return "", errors.New("managed Core checksum does not match active state")
	}
	return path, nil
}

func DataRootFromBridgePath(executable string) (string, error) {
	if !filepath.IsAbs(executable) {
		return "", errors.New("bridge executable path must be absolute")
	}
	clean := filepath.Clean(executable)
	name := filepath.Base(clean)
	if name != "agentbell-bridge" && name != "agentbell-bridge.exe" {
		return "", errors.New("bridge executable has an unexpected name")
	}
	versionDirectory := filepath.Dir(clean)
	if filepath.Base(versionDirectory) != "v1" {
		return "", errors.New("bridge executable is not in bridge/v1")
	}
	bridgeDirectory := filepath.Dir(versionDirectory)
	if filepath.Base(bridgeDirectory) != "bridge" {
		return "", errors.New("bridge executable is not in bin/bridge/v1")
	}
	binDirectory := filepath.Dir(bridgeDirectory)
	if filepath.Base(binDirectory) != "bin" {
		return "", errors.New("bridge executable is not in a managed bin directory")
	}
	dataRoot := filepath.Dir(binDirectory)
	if dataRoot == binDirectory || filepath.Dir(dataRoot) == dataRoot || dataRoot == "." {
		return "", errors.New("bridge executable has no managed data root")
	}
	return dataRoot, nil
}

func SHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func validVersion(value string) bool {
	base, prerelease, hasPrerelease := strings.Cut(value, "-")
	if !baseVersionPattern.MatchString(base) {
		return false
	}
	if !hasPrerelease {
		return true
	}
	if prerelease == "" {
		return false
	}
	for _, identifier := range strings.Split(prerelease, ".") {
		if !identifierPattern.MatchString(identifier) {
			return false
		}
		numeric := true
		for _, character := range identifier {
			if character < '0' || character > '9' {
				numeric = false
				break
			}
		}
		if numeric && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func cleanAbsoluteRoot(dataRoot string) (string, error) {
	if !filepath.IsAbs(dataRoot) {
		return "", errors.New("AgentBell data root must be absolute")
	}
	root := filepath.Clean(dataRoot)
	if root == string(filepath.Separator) {
		return "", errors.New("AgentBell data root cannot be the filesystem root")
	}
	return root, nil
}

func ensureManagedPath(
	fileSystem FileSystem,
	dataRoot string,
	target string,
) error {
	root, err := cleanAbsoluteRoot(dataRoot)
	if err != nil {
		return err
	}
	target = filepath.Clean(target)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return errors.New("path escapes the AgentBell data root")
	}
	rootInfo, err := fileSystem.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("managed path contains symlink %s", root)
	}
	if relative == "." {
		return nil
	}
	components := strings.Split(relative, string(filepath.Separator))
	current := root
	for _, component := range components {
		current = filepath.Join(current, component)
		info, statErr := fileSystem.Lstat(current)
		if statErr != nil {
			return statErr
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("managed path contains symlink %s", current)
		}
	}
	return nil
}
