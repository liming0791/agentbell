package binding

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCreateStoresOnlyHashAndExpires(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x42}, 64))
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	code, record, err := store.Create("AgentBell Team", "bot", 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code, "AGB-") || len(code) != len("AGB-XXXXX-XXXXX-XXXXX-XXXXX") {
		t.Fatalf("unexpected code format: %q", code)
	}
	if strings.Contains(record.CodeHash, code) || !strings.HasPrefix(record.CodeHash, "sha256:") {
		t.Fatalf("record did not contain an opaque hash: %#v", record)
	}
	files, err := os.ReadDir(filepath.Join(root, "pending"))
	if err != nil || len(files) != 1 {
		t.Fatalf("pending files=%d err=%v", len(files), err)
	}
	value, err := os.ReadFile(filepath.Join(root, "pending", files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(value, []byte(code)) {
		t.Fatal("binding code was persisted in plaintext")
	}
	info, err := os.Lstat(filepath.Join(root, "pending", files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateBindingRecord(t, info)

	if _, err := store.Load(code, now.Add(11*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired load error = %v", err)
	}
}

func assertPrivateBindingRecord(t *testing.T, info os.FileInfo) {
	t.Helper()
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("binding record is not a regular file: %v", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCreatePersistsOnlyAValidatedAbsoluteLarkCLIPath(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if store.random() == nil {
		t.Fatal("default cryptographic random source is nil")
	}
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x24}, 128))
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	larkCLIPath := filepath.Join(root, "bin", "lark-cli")

	code, record, err := store.Create(
		"AgentBell Team",
		"user",
		10*time.Minute,
		now,
		larkCLIPath,
	)
	if err != nil {
		t.Fatal(err)
	}
	if record.LarkCLIPath != larkCLIPath {
		t.Fatalf("lark-cli path = %q", record.LarkCLIPath)
	}
	loaded, err := store.Load(code, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LarkCLIPath != larkCLIPath {
		t.Fatalf("persisted lark-cli path = %q", loaded.LarkCLIPath)
	}

	for _, invalid := range []string{
		"relative/lark-cli",
		larkCLIPath + "\n",
	} {
		if _, _, err := store.Create(
			"AgentBell Team",
			"user",
			10*time.Minute,
			now,
			invalid,
		); err == nil {
			t.Fatalf("invalid lark-cli path %q was accepted", invalid)
		}
	}
	if _, _, err := store.Create(
		"AgentBell Team",
		"user",
		10*time.Minute,
		now,
		larkCLIPath,
		larkCLIPath,
	); err == nil {
		t.Fatal("multiple lark-cli paths were accepted")
	}
}

func TestCreateValidatesInputs(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	for _, test := range []struct {
		name string
		as   string
		ttl  time.Duration
	}{
		{"", "bot", 10 * time.Minute},
		{"team", "admin", 10 * time.Minute},
		{"team", "bot", time.Minute},
		{"team", "bot", 31 * time.Minute},
	} {
		if _, _, err := store.Create(test.name, test.as, test.ttl, now); err == nil {
			t.Fatalf("Create(%q, %q, %v) succeeded", test.name, test.as, test.ttl)
		}
	}
	if _, err := store.Load("not-a-code", now); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("invalid code error = %v", err)
	}
}

func TestClaimCommitIsSingleUse(t *testing.T) {
	store := NewStore(t.TempDir())
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x25}, 128))
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	code, _, err := store.Create("Team", "user", 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}

	claim, err := store.Claim(code, now.Add(time.Minute), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(code, now.Add(time.Minute), 2*time.Minute); !errors.Is(err, ErrClaimed) {
		t.Fatalf("second claim error = %v", err)
	}
	if err := store.Commit(claim, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(code, now.Add(2*time.Minute)); !errors.Is(err, ErrConsumed) {
		t.Fatalf("consumed load error = %v", err)
	}
	if err := store.Commit(claim, now.Add(2*time.Minute)); !errors.Is(err, ErrConsumed) {
		t.Fatalf("duplicate commit error = %v", err)
	}
	if err := store.Cancel(code, now.Add(2*time.Minute)); !errors.Is(err, ErrConsumed) {
		t.Fatalf("consumed cancel error = %v", err)
	}
	if err := store.Cancel(
		"AGB-00000-00000-00000-00000",
		now.Add(2*time.Minute),
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing cancel error = %v", err)
	}
}

func TestReleaseAndRecoverExpiredClaim(t *testing.T) {
	store := NewStore(t.TempDir())
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x31}, 256))
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	code, _, err := store.Create("Team", "bot", 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(code, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Release(claim); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(code, now); err != nil {
		t.Fatalf("released binding is unavailable: %v", err)
	}

	claim, err = store.Claim(code, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.RecoverExpired(now.Add(2 * time.Minute)); err != nil || recovered != 1 {
		t.Fatalf("recover=%d err=%v", recovered, err)
	}
	if _, err := store.Load(code, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("recovered binding is unavailable: %v", err)
	}
}

func TestConcurrentClaimHasOneWinner(t *testing.T) {
	store := NewStore(t.TempDir())
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x5a}, 512))
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	code, _, err := store.Create("Team", "bot", 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	wait.Add(2)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			defer wait.Done()
			_, claimErr := store.Claim(code, now, time.Minute)
			results <- claimErr
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	claimed := 0
	for result := range results {
		if result == nil {
			successes++
		} else if errors.Is(result, ErrClaimed) {
			claimed++
		} else {
			t.Fatalf("unexpected claim error: %v", result)
		}
	}
	if successes != 1 || claimed != 1 {
		t.Fatalf("successes=%d claimed=%d", successes, claimed)
	}
}

func TestClaimAndCommitRejectInvalidOrStaleClaims(t *testing.T) {
	store := NewStore(t.TempDir())
	entropy := append(
		bytes.Repeat([]byte{0x63}, 13),
		bytes.Repeat([]byte{0x64}, 13)...,
	)
	entropy = append(entropy, bytes.Repeat([]byte{0x65}, 128)...)
	store.Random = bytes.NewReader(entropy)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	if _, err := store.Claim("bad", now, time.Minute); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("invalid claim code error = %v", err)
	}
	if _, err := store.Claim("AGB-00000-00000-00000-00000", now, 0); err == nil {
		t.Fatal("zero claim lease succeeded")
	}
	if err := store.Commit(Claim{}, now); err == nil {
		t.Fatal("empty commit succeeded")
	}
	if err := store.Release(Claim{}); err == nil {
		t.Fatal("empty release succeeded")
	}
	if err := store.Commit(Claim{CodeHash: "bad", ClaimID: "claim"}, now); err == nil {
		t.Fatal("invalid hash commit succeeded")
	}

	expiredCode, _, err := store.Create("Expired", "bot", 2*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(expiredCode, now.Add(3*time.Minute), time.Minute); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired claim error = %v", err)
	}

	code, _, err := store.Create("Team", "bot", 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(code, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	wrong := claim
	wrong.ClaimID = "wrong"
	if err := store.Commit(wrong, now); !errors.Is(err, ErrClaimed) {
		t.Fatalf("wrong commit error = %v", err)
	}
	if err := store.Release(wrong); !errors.Is(err, ErrClaimed) {
		t.Fatalf("wrong release error = %v", err)
	}
	if err := store.Release(claim); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(claim, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("released commit error = %v", err)
	}
}

func TestStoreRejectsCorruptRecordsAndEntropyFailure(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x44}, 13))
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	code, _, err := store.Create("Team", "user", 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create("Team", "user", 10*time.Minute, now); err == nil {
		t.Fatal("exhausted entropy source succeeded")
	}

	_, fileName, err := canonicalHash(code)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pending", fileName)
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value = append(value, []byte("{}\n")...)
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(code, now); err == nil ||
		!strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("trailing JSON error = %v", err)
	}

	if _, err := fileNameForHash("sha256:nope"); err == nil {
		t.Fatal("invalid sha256 hash succeeded")
	}
	if err := NewStore("").ensureDirectories(); err == nil {
		t.Fatal("empty store root succeeded")
	}
}

func TestRecoverExpiredRemovesCommittedInflightCopy(t *testing.T) {
	store := NewStore(t.TempDir())
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x71}, 128))
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	code, _, err := store.Create("Team", "bot", 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(code, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fileName, err := fileNameForHash(claim.CodeHash)
	if err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(store.Root, "history", fileName)
	committed := claim.Record
	committed.ConsumedAt = now.Add(30 * time.Second)
	if err := writeNewRecord(historyPath, committed); err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.RecoverExpired(now.Add(2 * time.Minute)); err != nil || recovered != 0 {
		t.Fatalf("recover=%d err=%v", recovered, err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "inflight", fileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inflight copy remains: %v", err)
	}
}

func TestStateSpecificErrorsAndCollisionExhaustion(t *testing.T) {
	store := NewStore(t.TempDir())
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x19}, 256))
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	code, record, err := store.Create("Team", "bot", 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create("Team", "bot", 10*time.Minute, now); err == nil ||
		!strings.Contains(err.Error(), "unique") {
		t.Fatalf("collision exhaustion error = %v", err)
	}
	if _, err := store.Load("AGB-00000-00000-00000-00000", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing load error = %v", err)
	}
	claim, err := store.Claim(code, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(code, now); !errors.Is(err, ErrClaimed) {
		t.Fatalf("claimed load error = %v", err)
	}
	if err := store.Commit(claim, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(code, now, time.Minute); !errors.Is(err, ErrConsumed) {
		t.Fatalf("consumed claim error = %v", err)
	}
	if err := store.Release(claim); !errors.Is(err, ErrConsumed) {
		t.Fatalf("consumed release error = %v", err)
	}

	fileName, err := fileNameForHash(record.CodeHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewRecord(filepath.Join(store.Root, "history", fileName), record); !errors.Is(err, os.ErrExist) {
		t.Fatalf("exclusive history write error = %v", err)
	}
}

func TestMalformedCodesAndFilesystemErrors(t *testing.T) {
	for _, code := range []string{
		"AGB-0000-00000-00000-00000",
		"AGB-00000-00000-00000-0000I",
		"XYZ-00000-00000-00000-00000",
	} {
		if _, err := canonicalCode(code); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("canonicalCode(%q) error = %v", code, err)
		}
	}

	root := t.TempDir()
	fileRoot := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(fileRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(fileRoot).ensureDirectories(); err == nil {
		t.Fatal("file-backed store root succeeded")
	}
	if NewStore(root).random() == nil {
		t.Fatal("default entropy source is nil")
	}
	emptyEntropy := NewStore(root)
	emptyEntropy.Random = bytes.NewReader(nil)
	if _, err := emptyEntropy.randomHex(16); err == nil {
		t.Fatal("short claim entropy succeeded")
	}
	if err := writeNewRecord(filepath.Join(root, "missing", "record.json"), Record{}); err == nil {
		t.Fatal("exclusive write into missing directory succeeded")
	}
	if err := writeRecordAtomic(filepath.Join(root, "missing", "record.json"), Record{}); err == nil {
		t.Fatal("atomic write into missing directory succeeded")
	}
	invalidRecordPath := filepath.Join(root, "invalid.json")
	if err := os.WriteFile(invalidRecordPath, []byte("{\"version\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRecord(invalidRecordPath); err == nil {
		t.Fatal("incomplete binding record succeeded")
	}
}

func TestBindingStoreListsAndCancelsWithoutExposingCode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bindings")
	store := NewStore(root)
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x35}, 128))
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	code, record, err := store.Create("Cancel me", "user", 2*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := store.List(now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 ||
		statuses[0].State != "pending" ||
		statuses[0].ChannelName != record.ChannelName {
		t.Fatalf("pending statuses = %#v", statuses)
	}
	encoded, err := json.Marshal(statuses)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), code) ||
		strings.Contains(string(encoded), record.CodeHash) {
		t.Fatalf("binding status leaked a secret: %s", encoded)
	}

	cancelledAt := now.Add(90 * time.Second)
	if err := store.Cancel(code, cancelledAt); err != nil {
		t.Fatal(err)
	}
	statuses, err = store.List(cancelledAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 ||
		statuses[0].State != "cancelled" ||
		!statuses[0].CancelledAt.Equal(cancelledAt) {
		t.Fatalf("cancelled statuses = %#v", statuses)
	}
	if err := store.Cancel(code, cancelledAt); !errors.Is(err, ErrCancelled) {
		t.Fatalf("repeat cancel error = %v", err)
	}
	if _, err := store.Load(code, cancelledAt); !errors.Is(err, ErrConsumed) {
		t.Fatalf("cancelled code remained loadable: %v", err)
	}
}

func TestBindingStoreCancelAndListEdges(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "bindings"))
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x51}, 256))
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	if _, err := store.List(time.Time{}); err == nil {
		t.Fatal("zero status time was accepted")
	}
	if err := store.Cancel("bad", now); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("bad cancel code error = %v", err)
	}
	if err := store.Cancel(testBindingCode, time.Time{}); err == nil {
		t.Fatal("zero cancellation time was accepted")
	}

	code, _, err := store.Create("Claimed", "bot", 2*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(code, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Cancel(code, now); !errors.Is(err, ErrClaimed) {
		t.Fatalf("claimed cancel error = %v", err)
	}
	if err := store.Release(claim); err != nil {
		t.Fatal(err)
	}

	statuses, err := store.List(now.Add(3 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].State != "expired" {
		t.Fatalf("expired statuses = %#v", statuses)
	}
}
