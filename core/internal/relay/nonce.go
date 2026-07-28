package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	MinimumNonceRetention = 10 * time.Minute
	nonceRecordVersion    = 1
)

type NonceStore struct {
	root      string
	retention time.Duration
}

type nonceRecord struct {
	Version   int       `json:"version"`
	SeenAt    time.Time `json:"seenAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func OpenNonceStore(root string, retention time.Duration) (*NonceStore, error) {
	if root == "" {
		return nil, errors.New("nonce store root is required")
	}
	if retention < MinimumNonceRetention {
		retention = MinimumNonceRetention
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, fmt.Errorf("create nonce store: %w", err)
	}
	return &NonceStore{root: root, retention: retention}, nil
}

func (store *NonceStore) Retention() time.Duration {
	return store.retention
}

func (store *NonceStore) Accept(
	peerID string,
	nonce string,
	now time.Time,
) (bool, error) {
	if err := validateIdentifier("peer id", peerID); err != nil {
		return false, err
	}
	if err := validateNonce(nonce); err != nil {
		return false, err
	}
	if now.IsZero() {
		return false, errors.New("nonce acceptance time is required")
	}
	now = now.UTC()
	path := store.path(peerID, nonce)

	for {
		record := nonceRecord{
			Version:   nonceRecordVersion,
			SeenAt:    now,
			ExpiresAt: now.Add(store.retention),
		}
		err := writeNewPrivateJSON(path, store.root, record)
		if errors.Is(err, os.ErrExist) {
			expired, expiryErr := store.expired(path, now)
			if expiryErr != nil {
				return false, expiryErr
			}
			if !expired {
				return false, nil
			}
			if removeErr := os.Remove(path); removeErr != nil &&
				!errors.Is(removeErr, os.ErrNotExist) {
				return false, fmt.Errorf("remove expired nonce: %w", removeErr)
			}
			continue
		}
		if err != nil {
			return false, fmt.Errorf("reserve nonce: %w", err)
		}
		return true, nil
	}
}

func (store *NonceStore) Cleanup(now time.Time) (int, error) {
	if now.IsZero() {
		return 0, errors.New("nonce cleanup time is required")
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(store.root, entry.Name())
		expired, expiryErr := store.expired(path, now.UTC())
		if expiryErr != nil {
			return removed, expiryErr
		}
		if !expired {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func (store *NonceStore) expired(path string, now time.Time) (bool, error) {
	value, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read nonce record: %w", err)
	}
	var record nonceRecord
	if err := json.Unmarshal(value, &record); err == nil &&
		record.Version == nonceRecordVersion &&
		!record.ExpiresAt.IsZero() {
		return !record.ExpiresAt.After(now), nil
	}

	// A partial record still reserves the nonce. It becomes reclaimable only
	// after the full retention period, using its durable filesystem timestamp.
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat nonce record: %w", err)
	}
	return !info.ModTime().UTC().Add(store.retention).After(now), nil
}

func (store *NonceStore) path(peerID, nonce string) string {
	sum := sha256.Sum256([]byte(peerID + "\x00" + nonce))
	return filepath.Join(store.root, hex.EncodeToString(sum[:])+".json")
}
