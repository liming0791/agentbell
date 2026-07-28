package secretstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

const (
	keychainExecutable = "/usr/bin/security"
	keychainService    = "io.agentbell.remote-device-key"
	maximumBlobBytes   = 64 << 10
)

var (
	ErrNotFound         = errors.New("secret not found")
	ErrInvalidReference = errors.New("invalid secret reference")
	ErrInvalidSecret    = errors.New("invalid Ed25519 private key")
	ErrUnsafeStorage    = errors.New("unsafe secret storage")
	ErrUnavailable      = errors.New("secret store unavailable")
	ErrBackend          = errors.New("secret store operation failed")
)

var secretToolCandidates = []string{
	"/usr/bin/secret-tool",
	"/bin/secret-tool",
}

type commandRunner interface {
	Run(context.Context, string, []string, []byte) ([]byte, error)
}

type protector interface {
	Protect([]byte) ([]byte, error)
	Unprotect([]byte) ([]byte, error)
}

type options struct {
	goos        string
	managedRoot string
	runner      commandRunner
	protector   protector
	secretTool  string
}

// Store resolves a remote configuration's PrivateKeyRef using the native
// user-scoped secret backend, or an explicitly acknowledged private file.
type Store struct {
	goos        string
	managedRoot string
	runner      commandRunner
	protector   protector
	secretTool  string
}

// New creates the current platform's secret store. managedRoot owns encrypted
// DPAPI blobs on Windows and must always be an absolute, clean path.
func New(managedRoot string) (*Store, error) {
	return newStore(options{
		goos:        runtime.GOOS,
		managedRoot: managedRoot,
		runner:      execRunner{},
		protector:   defaultProtector(),
		secretTool:  findSecretTool(),
	})
}

func newStore(value options) (*Store, error) {
	if value.goos != "darwin" && value.goos != "linux" && value.goos != "windows" {
		return nil, ErrUnavailable
	}
	if value.managedRoot == "" ||
		!filepath.IsAbs(value.managedRoot) ||
		filepath.Clean(value.managedRoot) != value.managedRoot {
		return nil, ErrUnsafeStorage
	}
	if value.runner == nil {
		value.runner = execRunner{}
	}
	if value.protector == nil {
		value.protector = defaultProtector()
	}
	if value.secretTool != "" && !isFixedSecretTool(value.secretTool) {
		return nil, ErrUnsafeStorage
	}
	return &Store{
		goos:        value.goos,
		managedRoot: value.managedRoot,
		runner:      value.runner,
		protector:   value.protector,
		secretTool:  value.secretTool,
	}, nil
}

// Put validates and stores one Ed25519 private key.
func (store *Store) Put(
	ctx context.Context,
	reference remoteconfig.PrivateKeyRef,
	privateKey []byte,
) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := store.validateReference(reference); err != nil {
		return err
	}
	if err := validatePrivateKey(privateKey); err != nil {
		return err
	}
	value := append([]byte(nil), privateKey...)
	defer clear(value)

	switch reference.Store {
	case "keychain":
		return store.putCommandSecret(
			ctx,
			keychainExecutable,
			[]string{
				"add-generic-password",
				"-U",
				"-a", reference.ID,
				"-s", keychainService,
				"-w",
			},
			value,
			"keychain",
		)
	case "secret-service":
		if store.secretTool == "" {
			return ErrUnavailable
		}
		return store.putCommandSecret(
			ctx,
			store.secretTool,
			[]string{
				"store",
				"--label=AgentBell remote device key",
				"service", keychainService,
				"account", reference.ID,
			},
			value,
			"secret-service",
		)
	case "dpapi":
		ciphertext, err := store.protector.Protect(value)
		if err != nil || len(ciphertext) == 0 || len(ciphertext) > maximumBlobBytes {
			return fmt.Errorf("%w: dpapi put failed", ErrBackend)
		}
		defer clear(ciphertext)
		return writePrivateFile(store.dpapiPath(reference.ID), ciphertext, "windows")
	case "file":
		return writePrivateFile(reference.Path.Value, value, store.goos)
	default:
		return ErrInvalidReference
	}
}

// Get retrieves and validates one Ed25519 private key. The caller owns the
// returned buffer.
func (store *Store) Get(
	ctx context.Context,
	reference remoteconfig.PrivateKeyRef,
) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := store.validateReference(reference); err != nil {
		return nil, err
	}

	var value []byte
	var err error
	switch reference.Store {
	case "keychain":
		value, err = store.getCommandSecret(
			ctx,
			keychainExecutable,
			[]string{
				"find-generic-password",
				"-a", reference.ID,
				"-s", keychainService,
				"-w",
			},
			"keychain",
		)
	case "secret-service":
		if store.secretTool == "" {
			return nil, ErrUnavailable
		}
		value, err = store.getCommandSecret(
			ctx,
			store.secretTool,
			[]string{
				"lookup",
				"service", keychainService,
				"account", reference.ID,
			},
			"secret-service",
		)
	case "dpapi":
		var ciphertext []byte
		ciphertext, err = readPrivateFile(
			store.dpapiPath(reference.ID),
			"windows",
			maximumBlobBytes,
		)
		if err == nil {
			defer clear(ciphertext)
			value, err = store.protector.Unprotect(ciphertext)
			if err != nil {
				err = fmt.Errorf("%w: dpapi get failed", ErrBackend)
			}
		}
	case "file":
		value, err = readPrivateFile(
			reference.Path.Value,
			store.goos,
			ed25519.PrivateKeySize,
		)
	default:
		err = ErrInvalidReference
	}
	if err != nil {
		return nil, err
	}
	if err := validatePrivateKey(value); err != nil {
		clear(value)
		return nil, err
	}
	return value, nil
}

// Delete removes one referenced private key. It reports ErrNotFound when no
// value exists.
func (store *Store) Delete(
	ctx context.Context,
	reference remoteconfig.PrivateKeyRef,
) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := store.validateReference(reference); err != nil {
		return err
	}
	switch reference.Store {
	case "keychain":
		_, err := store.runner.Run(
			ctx,
			keychainExecutable,
			[]string{
				"delete-generic-password",
				"-a", reference.ID,
				"-s", keychainService,
			},
			nil,
		)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if isMissingCommandValue(err, "keychain") {
				return ErrNotFound
			}
			return fmt.Errorf("%w: keychain delete failed", ErrBackend)
		}
		return nil
	case "secret-service":
		if store.secretTool == "" {
			return ErrUnavailable
		}
		_, err := store.runner.Run(
			ctx,
			store.secretTool,
			[]string{
				"clear",
				"service", keychainService,
				"account", reference.ID,
			},
			nil,
		)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if isMissingCommandValue(err, "secret-service") {
				return ErrNotFound
			}
			return fmt.Errorf("%w: secret-service delete failed", ErrBackend)
		}
		return nil
	case "dpapi":
		return removePrivateFile(store.dpapiPath(reference.ID))
	case "file":
		return removePrivateFile(reference.Path.Value)
	default:
		return ErrInvalidReference
	}
}

func (store *Store) validateReference(reference remoteconfig.PrivateKeyRef) error {
	if store == nil {
		return ErrUnavailable
	}
	if err := reference.Validate(); err != nil {
		return ErrInvalidReference
	}
	switch store.goos {
	case "darwin":
		if reference.Store != "keychain" && reference.Store != "file" {
			return ErrInvalidReference
		}
	case "linux":
		if reference.Store != "secret-service" && reference.Store != "file" {
			return ErrInvalidReference
		}
	case "windows":
		if reference.Store != "dpapi" && reference.Store != "file" {
			return ErrInvalidReference
		}
	default:
		return ErrInvalidReference
	}
	if reference.Store == "file" && reference.Path.Platform != store.goos {
		return ErrInvalidReference
	}
	return nil
}

func validatePrivateKey(value []byte) error {
	if len(value) != ed25519.PrivateKeySize {
		return ErrInvalidSecret
	}
	derived := ed25519.NewKeyFromSeed(value[:ed25519.SeedSize])
	defer clear(derived)
	if !bytes.Equal(value, derived) {
		return ErrInvalidSecret
	}
	return nil
}

func (store *Store) putCommandSecret(
	ctx context.Context,
	executable string,
	args []string,
	value []byte,
	backend string,
) error {
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(value))+1)
	base64.StdEncoding.Encode(encoded[:len(encoded)-1], value)
	encoded[len(encoded)-1] = '\n'
	defer clear(encoded)
	if _, err := store.runner.Run(ctx, executable, args, encoded); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s put failed", ErrBackend, backend)
	}
	return nil
}

func (store *Store) getCommandSecret(
	ctx context.Context,
	executable string,
	args []string,
	backend string,
) ([]byte, error) {
	output, err := store.runner.Run(ctx, executable, args, nil)
	if err != nil {
		clear(output)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if isMissingCommandValue(err, backend) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: %s get failed", ErrBackend, backend)
	}
	defer clear(output)
	trimmed := bytes.TrimSpace(output)
	value := make([]byte, base64.StdEncoding.DecodedLen(len(trimmed)))
	count, err := base64.StdEncoding.Strict().Decode(value, trimmed)
	if err != nil || count > ed25519.PrivateKeySize {
		clear(value)
		return nil, ErrInvalidSecret
	}
	return value[:count], nil
}

func (store *Store) dpapiPath(id string) string {
	digest := sha256.Sum256([]byte(id))
	name := hex.EncodeToString(digest[:]) + ".blob"
	return filepath.Join(store.managedRoot, "secrets", "dpapi", name)
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidReference
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func isFixedSecretTool(path string) bool {
	for _, candidate := range secretToolCandidates {
		if path == candidate {
			return true
		}
	}
	return false
}

func findSecretTool() string {
	for _, candidate := range secretToolCandidates {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

func isMissingCommandValue(err error, backend string) bool {
	var failure commandExitError
	if !errors.As(err, &failure) {
		return false
	}
	return (backend == "keychain" && failure.code == 44) ||
		(backend == "secret-service" && failure.code == 1)
}
