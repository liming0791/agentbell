package relay

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNonceStoreAcceptsOnceAcrossConcurrentInstances(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nonces")
	now := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	const workers = 48
	var accepted atomic.Int32
	var failures atomic.Int32
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			store, err := OpenNonceStore(root, time.Minute)
			if err != nil {
				failures.Add(1)
				return
			}
			ok, err := store.Accept(
				"peer-one",
				"abcdefabcdefabcdefabcdefabcdefab",
				now,
			)
			if err != nil {
				failures.Add(1)
				return
			}
			if ok {
				accepted.Add(1)
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 || accepted.Load() != 1 {
		t.Fatalf(
			"nonce acceptance: accepted=%d failures=%d",
			accepted.Load(),
			failures.Load(),
		)
	}
	store, err := OpenNonceStore(root, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if store.Retention() != MinimumNonceRetention {
		t.Fatalf("retention was not clamped: %s", store.Retention())
	}
}

func TestNonceStoreCleanupAllowsReuseAfterExpiry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nonces")
	store, err := OpenNonceStore(root, MinimumNonceRetention)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	nonce := "0123456789abcdef0123456789abcdef"
	if accepted, err := store.Accept("peer-one", nonce, now); err != nil || !accepted {
		t.Fatalf("first accept: accepted=%v err=%v", accepted, err)
	}
	if accepted, err := store.Accept("peer-one", nonce, now.Add(time.Minute)); err != nil || accepted {
		t.Fatalf("replay accept: accepted=%v err=%v", accepted, err)
	}
	removed, err := store.Cleanup(now.Add(MinimumNonceRetention + time.Second))
	if err != nil || removed != 1 {
		t.Fatalf("cleanup: removed=%d err=%v", removed, err)
	}
	if accepted, err := store.Accept(
		"peer-one",
		nonce,
		now.Add(MinimumNonceRetention+time.Second),
	); err != nil || !accepted {
		t.Fatalf("accept after expiry: accepted=%v err=%v", accepted, err)
	}
}

func TestNonceStorePermissionsAndValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nonces")
	store, err := OpenNonceStore(root, MinimumNonceRetention)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := store.Accept("", "0123456789abcdef0123456789abcdef", now); err == nil {
		t.Fatal("invalid peer must fail")
	}
	if _, err := store.Accept("peer-one", "invalid", now); err == nil {
		t.Fatal("invalid nonce must fail")
	}
	if _, err := store.Accept(
		"peer-one",
		"0123456789abcdef0123456789abcdef",
		time.Time{},
	); err == nil {
		t.Fatal("zero timestamp must fail")
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("nonce root mode: %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid nonce calls wrote state: %#v", entries)
	}
}
