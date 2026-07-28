package relay

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPairingStorePersistsOnlyHashAndConsumesOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pairings")
	store, err := OpenPairingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x42}, 128))
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	code, record, err := store.Create(PairingPolicy{
		TeamID:          "team-main",
		AllowedSources:  []string{"codex"},
		AllowedRuntimes: []string{"ssh"},
	}, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code, "AGBR-") ||
		!strings.HasPrefix(record.CodeHash, "sha256:") {
		t.Fatalf("code=%q record=%#v", code, record)
	}
	value, err := os.ReadFile(filepath.Join(root, "pending", record.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(value, []byte(code)) {
		t.Fatal("relay pairing code was persisted in plaintext")
	}
	claim, err := store.Claim(code, now.Add(time.Minute), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(
		code,
		now.Add(time.Minute),
		2*time.Minute,
	); !errors.Is(err, ErrPairingClaimed) {
		t.Fatalf("second claim error = %v", err)
	}
	if err := store.Commit(claim, "peer-one", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(
		code,
		now.Add(2*time.Minute),
		2*time.Minute,
	); !errors.Is(err, ErrPairingConsumed) {
		t.Fatalf("consumed claim error = %v", err)
	}
}

func TestPairingStoreReleaseExpiryAndValidation(t *testing.T) {
	store, err := OpenPairingStore(filepath.Join(t.TempDir(), "pairings"))
	if err != nil {
		t.Fatal(err)
	}
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x31}, 256))
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	for _, ttl := range []time.Duration{time.Minute, 31 * time.Minute} {
		if _, _, err := store.Create(PairingPolicy{
			TeamID:          "team-main",
			AllowedSources:  []string{"codex"},
			AllowedRuntimes: []string{"ssh"},
		}, ttl, now); err == nil {
			t.Fatalf("invalid TTL %s was accepted", ttl)
		}
	}
	if _, _, err := store.Create(PairingPolicy{}, 10*time.Minute, now); err == nil {
		t.Fatal("empty pairing policy was accepted")
	}
	code, _, err := store.Create(PairingPolicy{
		TeamID:          "team-main",
		AllowedSources:  []string{"codex"},
		AllowedRuntimes: []string{"ssh"},
	}, 2*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(code, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Release(claim, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(
		code,
		now.Add(3*time.Minute),
		time.Minute,
	); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("expired claim error = %v", err)
	}
	if _, err := store.Claim("not-a-code", now, time.Minute); !errors.Is(
		err,
		ErrInvalidPairingCode,
	) {
		t.Fatalf("invalid code error = %v", err)
	}
}

func TestPairingStoreConcurrentClaimHasOneWinner(t *testing.T) {
	store, err := OpenPairingStore(filepath.Join(t.TempDir(), "pairings"))
	if err != nil {
		t.Fatal(err)
	}
	store.Random = bytes.NewReader(bytes.Repeat([]byte{0x55}, 256))
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	code, _, err := store.Create(PairingPolicy{
		TeamID:          "team-main",
		AllowedSources:  []string{"codex"},
		AllowedRuntimes: []string{"ssh"},
	}, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.Claim(code, now, time.Minute)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrPairingClaimed) {
			t.Fatalf("claim error = %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners = %d", winners)
	}
}

func TestPairingStoreClaimCommitReleaseInputEdges(t *testing.T) {
	store, err := OpenPairingStore(filepath.Join(t.TempDir(), "pairings"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	if _, err := store.Claim(
		"AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ",
		time.Time{},
		time.Minute,
	); err == nil {
		t.Fatal("zero claim time was accepted")
	}
	for _, lease := range []time.Duration{0, MaximumPairingTTL + time.Second} {
		if _, err := store.Claim(
			"AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ",
			now,
			lease,
		); err == nil {
			t.Fatalf("invalid lease %s was accepted", lease)
		}
	}
	if err := store.Release(PairingClaim{}, time.Time{}); err == nil {
		t.Fatal("zero release time was accepted")
	}
	if err := store.Release(PairingClaim{}, now); err == nil {
		t.Fatal("invalid release claim was accepted")
	}
	if err := store.Commit(PairingClaim{}, "peer-one", time.Time{}); err == nil {
		t.Fatal("zero commit time was accepted")
	}
	if err := store.Commit(PairingClaim{}, "../peer", now); err == nil {
		t.Fatal("invalid commit peer was accepted")
	}
	if err := store.Commit(PairingClaim{}, "peer-one", now); err == nil {
		t.Fatal("invalid commit claim was accepted")
	}
}
