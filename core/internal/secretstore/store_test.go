package secretstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

type commandCall struct {
	name  string
	args  []string
	stdin []byte
}

type fakeRunner struct {
	calls   []commandCall
	outputs [][]byte
	errs    []error
}

func (runner *fakeRunner) Run(
	_ context.Context,
	name string,
	args []string,
	stdin []byte,
) ([]byte, error) {
	runner.calls = append(runner.calls, commandCall{
		name:  name,
		args:  append([]string(nil), args...),
		stdin: append([]byte(nil), stdin...),
	})
	index := len(runner.calls) - 1
	var output []byte
	if index < len(runner.outputs) {
		output = runner.outputs[index]
	}
	if index < len(runner.errs) {
		return output, runner.errs[index]
	}
	return output, nil
}

type fakeProtector struct {
	protectCalls   int
	unprotectCalls int
	plain          []byte
	cipher         []byte
	err            error
}

func (protector *fakeProtector) Protect(value []byte) ([]byte, error) {
	protector.protectCalls++
	protector.plain = append([]byte(nil), value...)
	if protector.err != nil {
		return nil, protector.err
	}
	return append([]byte(nil), protector.cipher...), nil
}

func (protector *fakeProtector) Unprotect(value []byte) ([]byte, error) {
	protector.unprotectCalls++
	if protector.err != nil {
		return nil, protector.err
	}
	if !bytes.Equal(value, protector.cipher) {
		return nil, errors.New("unexpected ciphertext")
	}
	return append([]byte(nil), protector.plain...), nil
}

func testPrivateKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey
}

func fileReference(path string) remoteconfig.PrivateKeyRef {
	return remoteconfig.PrivateKeyRef{
		Store: "file",
		Path: &remoteconfig.PathRef{
			Platform: runtime.GOOS,
			Value:    path,
		},
		FileFallbackAcknowledged: true,
	}
}

func TestFileFallbackPutGetDeleteIsPrivateAndAtomic(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "device.key")
	store, err := newStore(options{goos: runtime.GOOS, managedRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	reference := fileReference(path)
	first := testPrivateKey(t)
	second := testPrivateKey(t)

	if err := store.Put(ctx, reference, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, reference, second); err != nil {
		t.Fatalf("atomic replacement failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("directory mode = %o, want private", directoryInfo.Mode().Perm())
	}
	got, err := store.Get(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, second) {
		t.Fatal("retrieved private key differs")
	}
	got[0] ^= 0xff
	again, err := store.Get(ctx, reference)
	if err != nil || bytes.Equal(got, again) {
		t.Fatal("Get did not return an independent copy")
	}
	if err := store.Delete(ctx, reference); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, reference); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, reference); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete error = %v, want ErrNotFound", err)
	}
}

func TestFileFallbackRejectsUnsafeReferenceAndStorage(t *testing.T) {
	root := t.TempDir()
	store, err := newStore(options{goos: runtime.GOOS, managedRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	key := testPrivateKey(t)
	ctx := context.Background()

	unacknowledged := fileReference(filepath.Join(root, "unacknowledged.key"))
	unacknowledged.FileFallbackAcknowledged = false
	if err := store.Put(ctx, unacknowledged, key); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("unacknowledged error = %v", err)
	}
	mismatch := fileReference(filepath.Join(root, "wrong-platform.key"))
	if runtime.GOOS == "windows" {
		mismatch.Path.Platform = "linux"
	} else {
		mismatch.Path.Platform = "windows"
		mismatch.Path.Value = `C:\AgentBell\device.key`
	}
	if err := store.Put(ctx, mismatch, key); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("platform mismatch error = %v", err)
	}

	target := filepath.Join(root, "target.key")
	if err := os.WriteFile(target, key, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.key")
	if err := os.Symlink(target, link); err == nil {
		if err := store.Put(ctx, fileReference(link), key); !errors.Is(err, ErrUnsafeStorage) {
			t.Fatalf("symlink error = %v", err)
		}
	}
	if err := os.Chmod(target, 0o644); err == nil && runtime.GOOS != "windows" {
		if _, err := store.Get(ctx, fileReference(target)); !errors.Is(err, ErrUnsafeStorage) {
			t.Fatalf("permissive file error = %v", err)
		}
	}
}

func TestPutAndGetRejectInvalidPrivateKeys(t *testing.T) {
	root := t.TempDir()
	store, err := newStore(options{goos: runtime.GOOS, managedRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	reference := fileReference(filepath.Join(root, "device.key"))
	for _, value := range [][]byte{nil, {}, make([]byte, ed25519.PrivateKeySize-1), make([]byte, ed25519.PrivateKeySize+1), make([]byte, ed25519.PrivateKeySize)} {
		if err := store.Put(context.Background(), reference, value); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("Put(%d bytes) error = %v", len(value), err)
		}
	}

	if err := os.WriteFile(reference.Path.Value, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), reference); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Get malformed key error = %v", err)
	}
}

func TestKeychainUsesFixedBinaryAndSecretOnlyOnStdin(t *testing.T) {
	key := testPrivateKey(t)
	encoded := base64.StdEncoding.EncodeToString(key)
	runner := &fakeRunner{outputs: [][]byte{nil, []byte(encoded + "\n")}}
	store, err := newStore(options{
		goos:        "darwin",
		managedRoot: t.TempDir(),
		runner:      runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	reference := remoteconfig.PrivateKeyRef{Store: "keychain", ID: "agentbell/device:mac"}
	ctx := context.Background()
	if err := store.Put(ctx, reference, key); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("keychain round trip differs")
	}
	if err := store.Delete(ctx, reference); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("command calls = %d, want 3", len(runner.calls))
	}
	for _, call := range runner.calls {
		if call.name != "/usr/bin/security" {
			t.Fatalf("binary = %q", call.name)
		}
		joined := strings.Join(call.args, " ")
		if strings.Contains(joined, encoded) || strings.Contains(joined, string(key)) {
			t.Fatal("private key leaked into argv")
		}
	}
	if string(runner.calls[0].stdin) != encoded+"\n" {
		t.Fatal("keychain Put did not pass the encoded secret through stdin")
	}
	if len(runner.calls[1].stdin) != 0 || len(runner.calls[2].stdin) != 0 {
		t.Fatal("keychain Get/Delete unexpectedly used stdin")
	}
}

func TestSecretServiceUsesFixedBinaryAndSecretOnlyOnStdin(t *testing.T) {
	key := testPrivateKey(t)
	encoded := base64.StdEncoding.EncodeToString(key)
	runner := &fakeRunner{outputs: [][]byte{nil, []byte(encoded + "\n")}}
	store, err := newStore(options{
		goos:        "linux",
		managedRoot: t.TempDir(),
		runner:      runner,
		secretTool:  "/usr/bin/secret-tool",
	})
	if err != nil {
		t.Fatal(err)
	}
	reference := remoteconfig.PrivateKeyRef{Store: "secret-service", ID: "agentbell/device:linux"}
	ctx := context.Background()
	if err := store.Put(ctx, reference, key); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("secret-service round trip differs")
	}
	if err := store.Delete(ctx, reference); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if call.name != "/usr/bin/secret-tool" {
			t.Fatalf("binary = %q", call.name)
		}
		if strings.Contains(strings.Join(call.args, " "), encoded) {
			t.Fatal("private key leaked into argv")
		}
	}
	if string(runner.calls[0].stdin) != encoded+"\n" {
		t.Fatal("secret-tool Put did not pass the encoded secret through stdin")
	}
}

func TestDPAPIBlobStaysInManagedRootAndContainsNoPlaintext(t *testing.T) {
	root := t.TempDir()
	key := testPrivateKey(t)
	protector := &fakeProtector{
		plain:  append([]byte(nil), key...),
		cipher: []byte("opaque-dpapi-ciphertext"),
	}
	store, err := newStore(options{
		goos:        "windows",
		managedRoot: root,
		protector:   protector,
	})
	if err != nil {
		t.Fatal(err)
	}
	reference := remoteconfig.PrivateKeyRef{Store: "dpapi", ID: "agentbell/device:windows"}
	ctx := context.Background()
	if err := store.Put(ctx, reference, key); err != nil {
		t.Fatal(err)
	}
	path := store.dpapiPath(reference.ID)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("DPAPI path escaped managed root: %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, key) || !bytes.Equal(raw, protector.cipher) {
		t.Fatal("DPAPI blob contains plaintext or unexpected data")
	}
	got, err := store.Get(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("DPAPI round trip differs")
	}
	if protector.protectCalls != 1 || protector.unprotectCalls != 1 {
		t.Fatalf("protector calls = %d/%d", protector.protectCalls, protector.unprotectCalls)
	}
	if err := store.Delete(ctx, reference); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("DPAPI blob remains: %v", err)
	}
}

func TestReferenceStoreMustMatchPlatform(t *testing.T) {
	key := testPrivateKey(t)
	tests := []struct {
		goos string
		ref  remoteconfig.PrivateKeyRef
	}{
		{"darwin", remoteconfig.PrivateKeyRef{Store: "secret-service", ID: "safe"}},
		{"darwin", remoteconfig.PrivateKeyRef{Store: "dpapi", ID: "safe"}},
		{"linux", remoteconfig.PrivateKeyRef{Store: "keychain", ID: "safe"}},
		{"windows", remoteconfig.PrivateKeyRef{Store: "keychain", ID: "safe"}},
	}
	for _, test := range tests {
		store, err := newStore(options{goos: test.goos, managedRoot: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Put(context.Background(), test.ref, key); !errors.Is(err, ErrInvalidReference) {
			t.Fatalf("%s/%s error = %v", test.goos, test.ref.Store, err)
		}
	}
}

func TestBackendErrorsAreRedacted(t *testing.T) {
	key := testPrivateKey(t)
	secretText := base64.StdEncoding.EncodeToString(key)
	runner := &fakeRunner{errs: []error{errors.New("backend echoed " + secretText)}}
	store, err := newStore(options{
		goos:        "darwin",
		managedRoot: t.TempDir(),
		runner:      runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = store.Put(
		context.Background(),
		remoteconfig.PrivateKeyRef{Store: "keychain", ID: "safe"},
		key,
	)
	if !errors.Is(err, ErrBackend) {
		t.Fatalf("error = %v, want ErrBackend", err)
	}
	if strings.Contains(err.Error(), secretText) || strings.Contains(err.Error(), string(key)) {
		t.Fatal("backend error leaked the private key")
	}
}

func TestNewValidatesManagedRootAndContext(t *testing.T) {
	if _, err := New("relative/root"); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("relative root error = %v", err)
	}
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ref := fileReference(filepath.Join(t.TempDir(), "cancelled.key"))
	if err := store.Put(cancelled, ref, testPrivateKey(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Put error = %v", err)
	}
	if _, err := store.Get(nil, ref); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("nil context Get error = %v", err)
	}
	if err := store.Delete(cancelled, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Delete error = %v", err)
	}
}

func TestCommandRunner(t *testing.T) {
	output, err := (execRunner{}).Run(
		context.Background(),
		"/usr/bin/printf",
		[]string{"agentbell"},
		nil,
	)
	if err != nil || string(output) != "agentbell" {
		t.Fatalf("exec runner output=%q err=%v", output, err)
	}
	if _, err := (execRunner{}).Run(
		context.Background(),
		"/definitely/not/an/executable",
		nil,
		nil,
	); !errors.Is(err, ErrBackend) {
		t.Fatalf("missing executable error = %v", err)
	}
	if _, err := (execRunner{}).Run(
		context.Background(),
		"/usr/bin/printf",
		[]string{strings.Repeat("x", maximumCommandOutputBytes+1)},
		nil,
	); !errors.Is(err, ErrBackend) {
		t.Fatalf("oversized output error = %v", err)
	}
	if _, err := (execRunner{}).Run(
		context.Background(),
		"/usr/bin/false",
		nil,
		nil,
	); err == nil {
		t.Fatal("non-zero exit succeeded")
	}
}

func TestFileHelpersRejectUnsafeShapesAndSizes(t *testing.T) {
	root := t.TempDir()
	if err := writePrivateFile("relative.key", []byte("value"), runtime.GOOS); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("relative write error = %v", err)
	}
	if _, err := readPrivateFile("relative.key", runtime.GOOS, 10); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("relative read error = %v", err)
	}

	directoryTarget := filepath.Join(root, "directory-target")
	if err := os.Mkdir(directoryTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(directoryTarget, []byte("value"), runtime.GOOS); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("directory target write error = %v", err)
	}
	if _, err := readPrivateFile(directoryTarget, runtime.GOOS, 10); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("directory target read error = %v", err)
	}
	if err := removePrivateFile(directoryTarget); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("directory target remove error = %v", err)
	}

	oversized := filepath.Join(root, "oversized.key")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte{1}, 11), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(oversized, runtime.GOOS, 10); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("oversized read error = %v", err)
	}
	if err := removePrivateFile(oversized); err != nil {
		t.Fatal(err)
	}
	if err := removePrivateFile(oversized); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing remove error = %v", err)
	}

	if runtime.GOOS != "windows" {
		publicDirectory := filepath.Join(root, "public")
		if err := os.Mkdir(publicDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writePrivateFile(
			filepath.Join(publicDirectory, "key"),
			[]byte("value"),
			runtime.GOOS,
		); !errors.Is(err, ErrUnsafeStorage) {
			t.Fatalf("public directory error = %v", err)
		}
	}
}

func TestBackendUnavailableMalformedAndFailureBranches(t *testing.T) {
	key := testPrivateKey(t)
	ctx := context.Background()
	linux, err := newStore(options{
		goos:        "linux",
		managedRoot: t.TempDir(),
		runner:      &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := remoteconfig.PrivateKeyRef{Store: "secret-service", ID: "safe"}
	if err := linux.Put(ctx, ref, key); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable Put error = %v", err)
	}
	if _, err := linux.Get(ctx, ref); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable Get error = %v", err)
	}
	if err := linux.Delete(ctx, ref); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable Delete error = %v", err)
	}

	malformedRunner := &fakeRunner{outputs: [][]byte{[]byte("not-base64")}}
	mac, err := newStore(options{
		goos:        "darwin",
		managedRoot: t.TempDir(),
		runner:      malformedRunner,
	})
	if err != nil {
		t.Fatal(err)
	}
	macRef := remoteconfig.PrivateKeyRef{Store: "keychain", ID: "safe"}
	if _, err := mac.Get(ctx, macRef); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("malformed backend value error = %v", err)
	}

	failingRunner := &fakeRunner{errs: []error{
		errors.New("failed"),
		errors.New("failed"),
	}}
	mac.runner = failingRunner
	if _, err := mac.Get(ctx, macRef); !errors.Is(err, ErrBackend) {
		t.Fatalf("backend Get error = %v", err)
	}
	if err := mac.Delete(ctx, macRef); !errors.Is(err, ErrBackend) {
		t.Fatalf("backend Delete error = %v", err)
	}
}

func TestDPAPIFailureAndMalformedBlobAreRedacted(t *testing.T) {
	root := t.TempDir()
	ref := remoteconfig.PrivateKeyRef{Store: "dpapi", ID: "safe"}
	failing := &fakeProtector{err: errors.New("contains sensitive backend detail")}
	store, err := newStore(options{
		goos:        "windows",
		managedRoot: root,
		protector:   failing,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), ref, testPrivateKey(t)); !errors.Is(err, ErrBackend) {
		t.Fatalf("DPAPI Put error = %v", err)
	}
	path := store.dpapiPath(ref.ID)
	if err := writePrivateFile(path, []byte("cipher"), "windows"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), ref); !errors.Is(err, ErrBackend) {
		t.Fatalf("DPAPI Get error = %v", err)
	}
}

func TestStoreConstructorRejectsInvalidInputs(t *testing.T) {
	if _, err := newStore(options{goos: "plan9", managedRoot: t.TempDir()}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unsupported platform error = %v", err)
	}
	if _, err := newStore(options{
		goos:        "linux",
		managedRoot: t.TempDir(),
		secretTool:  "/tmp/secret-tool",
	}); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("untrusted secret-tool error = %v", err)
	}
	if isFixedSecretTool("/tmp/secret-tool") {
		t.Fatal("untrusted secret-tool was accepted")
	}
}

func TestCommandNotFoundClassification(t *testing.T) {
	if !isMissingCommandValue(commandExitError{code: 44}, "keychain") {
		t.Fatal("keychain not-found exit was not classified")
	}
	if !isMissingCommandValue(commandExitError{code: 1}, "secret-service") {
		t.Fatal("secret-service not-found exit was not classified")
	}
	if isMissingCommandValue(commandExitError{code: 2}, "keychain") ||
		isMissingCommandValue(errors.New("opaque"), "keychain") {
		t.Fatal("backend failure was misclassified as not found")
	}

	runner := &fakeRunner{errs: []error{
		commandExitError{code: 44},
		commandExitError{code: 44},
	}}
	store, err := newStore(options{
		goos:        "darwin",
		managedRoot: t.TempDir(),
		runner:      runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := remoteconfig.PrivateKeyRef{Store: "keychain", ID: "safe"}
	if _, err := store.Get(context.Background(), ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Get error = %v", err)
	}
	if err := store.Delete(context.Background(), ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Delete error = %v", err)
	}
}
