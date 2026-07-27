package installstate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func validState() ActiveState {
	return ActiveState{
		SchemaVersion:   SchemaVersion,
		Generation:      7,
		ActiveVersion:   "0.3.0-rc.1",
		PreviousVersion: "0.2.0",
		Target:          "darwin-arm64",
		Checksum:        strings.Repeat("a", 64),
		BridgeChecksum:  strings.Repeat("b", 64),
		TransactionID:   "upgrade-20260726-001",
	}
}

func TestActiveStateValidate(t *testing.T) {
	if err := validState().Validate(); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ActiveState)
	}{
		{"schema", func(state *ActiveState) { state.SchemaVersion = 2 }},
		{"generation", func(state *ActiveState) { state.Generation = 0 }},
		{"active version", func(state *ActiveState) { state.ActiveVersion = "../escape" }},
		{"empty prerelease identifier", func(state *ActiveState) { state.ActiveVersion = "0.3.0-rc..1" }},
		{"numeric prerelease leading zero", func(state *ActiveState) { state.ActiveVersion = "0.3.0-01" }},
		{"previous version", func(state *ActiveState) { state.PreviousVersion = "bad/version" }},
		{"same versions", func(state *ActiveState) { state.PreviousVersion = state.ActiveVersion }},
		{"target", func(state *ActiveState) { state.Target = "plan9-amd64" }},
		{"checksum", func(state *ActiveState) { state.Checksum = strings.Repeat("A", 64) }},
		{"bridge checksum", func(state *ActiveState) { state.BridgeChecksum = "" }},
		{"transaction", func(state *ActiveState) { state.TransactionID = "../transaction" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := validState()
			test.mutate(&state)
			if err := state.Validate(); err == nil {
				t.Fatal("invalid state accepted")
			}
		})
	}
}

func TestStoreSaveLoadAndRejectUnknownFields(t *testing.T) {
	dataRoot := t.TempDir()
	store := NewStore(OSFileSystem{})
	state := validState()

	if err := store.Save(dataRoot, state); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := store.Load(dataRoot)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded != state {
		t.Fatalf("state changed across save/load: %#v", loaded)
	}

	info, err := os.Stat(filepath.Join(dataRoot, "bin", ActiveStateFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("active state mode = %o, want 600", info.Mode().Perm())
	}

	unknown := `{
		"schemaVersion":1,
		"generation":1,
		"activeVersion":"0.3.0",
		"target":"linux-amd64",
		"checksum":"` + strings.Repeat("b", 64) + `",
		"transactionId":"tx-1",
		"unexpected":true
	}`
	if err := os.WriteFile(
		filepath.Join(dataRoot, "bin", ActiveStateFile),
		[]byte(unknown),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(dataRoot); err == nil {
		t.Fatal("unknown active-state field accepted")
	}
}

func TestLoadRejectsOversizedMultipleAndInvalidStates(t *testing.T) {
	dataRoot := t.TempDir()
	store := Store{}
	state := validState()
	if err := store.Save(dataRoot, state); err != nil {
		t.Fatal(err)
	}
	activePath := filepath.Join(dataRoot, "bin", ActiveStateFile)
	validJSON, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range [][]byte{
		append(append([]byte(nil), validJSON...), validJSON...),
		bytes.Repeat([]byte(" "), maxActiveStateSize+1),
		[]byte(`{"schemaVersion":1}`),
	} {
		if err := os.WriteFile(activePath, value, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(dataRoot); err == nil {
			t.Fatalf("invalid active state accepted: %q", value[:min(len(value), 80)])
		}
	}
}

type renameFailureFS struct {
	FileSystem
}

func (renameFailureFS) Rename(string, string) error {
	return errors.New("injected rename failure")
}

func TestStoreSaveIsAtomicWhenRenameFails(t *testing.T) {
	dataRoot := t.TempDir()
	base := NewStore(OSFileSystem{})
	original := validState()
	if err := base.Save(dataRoot, original); err != nil {
		t.Fatal(err)
	}

	replacement := original
	replacement.Generation++
	replacement.ActiveVersion = "0.4.0"
	replacement.PreviousVersion = original.ActiveVersion
	replacement.Checksum = strings.Repeat("c", 64)

	failing := NewStore(renameFailureFS{FileSystem: OSFileSystem{}})
	if err := failing.Save(dataRoot, replacement); err == nil {
		t.Fatal("injected rename failure was ignored")
	}
	loaded, err := base.Load(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != original {
		t.Fatalf("failed save changed active state: %#v", loaded)
	}
}

func TestManagedCorePathAndDataRootResolution(t *testing.T) {
	dataRoot := t.TempDir()
	state := validState()
	path, err := ManagedCorePath(dataRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(dataRoot, "bin", state.ActiveVersion, "agentbell")
	if path != expected {
		t.Fatalf("core path = %q, want %q", path, expected)
	}

	state.Target = "windows-amd64"
	path, err = ManagedCorePath(dataRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "agentbell.exe") {
		t.Fatalf("Windows Core path missing extension: %s", path)
	}

	bridgePath := filepath.Join(
		dataRoot,
		"bin",
		"bridge",
		"v1",
		"agentbell-bridge",
	)
	resolved, err := DataRootFromBridgePath(bridgePath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != dataRoot {
		t.Fatalf("data root = %q, want %q", resolved, dataRoot)
	}
	for _, invalid := range []string{
		filepath.Join(dataRoot, "bin", "bridge", "v2", "agentbell-bridge"),
		filepath.Join(dataRoot, "bin", "agentbell-bridge"),
		filepath.Join(dataRoot, "bin", "bridge", "v1", "unexpected"),
		"relative/bin/bridge/v1/agentbell-bridge",
	} {
		if _, err := DataRootFromBridgePath(invalid); err == nil {
			t.Fatalf("invalid bridge path accepted: %s", invalid)
		}
	}
	if _, err := ActiveStatePath("relative"); err == nil {
		t.Fatal("relative data root accepted")
	}
	if _, err := ActiveStatePath(string(filepath.Separator)); err == nil {
		t.Fatal("filesystem root accepted as data root")
	}
}

func TestResolveManagedCoreRejectsSymlinkAndChecksumMismatch(t *testing.T) {
	dataRoot := t.TempDir()
	store := NewStore(OSFileSystem{})
	state := validState()
	state.Target = "linux-amd64"
	content := []byte("verified-core")
	state.Checksum = SHA256(content)
	path, err := ManagedCorePath(dataRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveManagedCore(dataRoot, state)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved != path {
		t.Fatalf("resolved path = %q, want %q", resolved, path)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResolveManagedCore(dataRoot, state); err == nil {
			t.Fatal("non-executable POSIX Core accepted")
		}
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	state.Checksum = strings.Repeat("d", 64)
	if _, err := store.ResolveManagedCore(dataRoot, state); err == nil {
		t.Fatal("checksum mismatch accepted")
	}

	state.Checksum = SHA256(content)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dataRoot, "real-core")
	if err := os.WriteFile(target, content, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveManagedCore(dataRoot, state); err == nil {
		t.Fatal("symlinked Core accepted")
	}
}

func TestLoadRejectsSymlinkedActiveState(t *testing.T) {
	dataRoot := t.TempDir()
	store := NewStore(OSFileSystem{})
	if err := store.Save(dataRoot, validState()); err != nil {
		t.Fatal(err)
	}
	activePath := filepath.Join(dataRoot, "bin", ActiveStateFile)
	targetPath := filepath.Join(dataRoot, "active-target.json")
	value, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, value, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(activePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetPath, activePath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(dataRoot); err == nil {
		t.Fatal("symlinked active state accepted")
	}
}

func TestSaveRejectsSymlinkedDataRootBeforeWriting(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(parent, "linked-root")
	if err := os.Symlink(target, dataRoot); err != nil {
		t.Fatal(err)
	}
	store := NewStore(OSFileSystem{})
	if err := store.Save(dataRoot, validState()); err == nil {
		t.Fatal("symlinked data root accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Save wrote through symlinked data root: %v", err)
	}
}
