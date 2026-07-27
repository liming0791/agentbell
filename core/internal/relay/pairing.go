package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

const (
	pairingVersion    = 1
	MinimumPairingTTL = 2 * time.Minute
	MaximumPairingTTL = 30 * time.Minute
)

var (
	ErrInvalidPairingCode = errors.New("invalid relay pairing code")
	ErrPairingNotFound    = errors.New("relay pairing code was not found")
	ErrPairingClaimed     = errors.New("relay pairing code is already claimed")
	ErrPairingExpired     = errors.New("relay pairing code has expired")
	ErrPairingConsumed    = errors.New("relay pairing code was already consumed")

	pairingCodePattern = regexp.MustCompile(
		`^AGBR-[0123456789ABCDEFGHJKMNPQRSTVWXYZ]{8}` +
			`(?:-[0123456789ABCDEFGHJKMNPQRSTVWXYZ]{8}){3}$`,
	)
	pairingEncoding = base32.NewEncoding(
		"0123456789ABCDEFGHJKMNPQRSTVWXYZ",
	).WithPadding(base32.NoPadding)
)

type PairingPolicy struct {
	TeamID          string   `json:"teamId"`
	AllowedSources  []string `json:"allowedSources"`
	AllowedRuntimes []string `json:"allowedRuntimes"`
}

type PairingRecord struct {
	Version    int           `json:"version"`
	ID         string        `json:"id"`
	CodeHash   string        `json:"codeHash"`
	Policy     PairingPolicy `json:"policy"`
	CreatedAt  time.Time     `json:"createdAt"`
	ExpiresAt  time.Time     `json:"expiresAt"`
	ClaimID    string        `json:"claimId,omitempty"`
	LeaseUntil time.Time     `json:"leaseUntil,omitempty"`
	PeerID     string        `json:"peerId,omitempty"`
	ConsumedAt time.Time     `json:"consumedAt,omitempty"`
}

type PairingClaim struct {
	Record  PairingRecord
	ClaimID string
}

type PairingStore struct {
	root     string
	pending  string
	inflight string
	history  string
	tmp      string
	Random   io.Reader
}

func OpenPairingStore(root string) (*PairingStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("relay pairing store root is required")
	}
	store := &PairingStore{
		root:     root,
		pending:  filepath.Join(root, "pending"),
		inflight: filepath.Join(root, "inflight"),
		history:  filepath.Join(root, "history"),
		tmp:      filepath.Join(root, "tmp"),
		Random:   rand.Reader,
	}
	for _, directory := range []string{
		store.root,
		store.pending,
		store.inflight,
		store.history,
		store.tmp,
	} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return nil, fmt.Errorf("create relay pairing store: %w", err)
		}
	}
	return store, nil
}

func (store *PairingStore) Create(
	policy PairingPolicy,
	ttl time.Duration,
	now time.Time,
) (string, PairingRecord, error) {
	if err := policy.Validate(); err != nil {
		return "", PairingRecord{}, err
	}
	if ttl < MinimumPairingTTL || ttl > MaximumPairingTTL {
		return "", PairingRecord{}, fmt.Errorf(
			"relay pairing TTL must be between %s and %s",
			MinimumPairingTTL,
			MaximumPairingTTL,
		)
	}
	if now.IsZero() {
		return "", PairingRecord{}, errors.New(
			"relay pairing creation time is required",
		)
	}
	for range 8 {
		code, err := store.newCode()
		if err != nil {
			return "", PairingRecord{}, err
		}
		codeHash, id, err := pairingIdentity(code)
		if err != nil {
			return "", PairingRecord{}, err
		}
		record := PairingRecord{
			Version:   pairingVersion,
			ID:        id,
			CodeHash:  codeHash,
			Policy:    policy,
			CreatedAt: now.UTC(),
			ExpiresAt: now.UTC().Add(ttl),
		}
		err = writeNewPrivateJSON(
			store.pendingPath(id),
			store.tmp,
			record,
		)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", PairingRecord{}, err
		}
		return code, record, nil
	}
	return "", PairingRecord{}, errors.New(
		"could not generate a unique relay pairing code",
	)
}

func (store *PairingStore) Claim(
	code string,
	now time.Time,
	lease time.Duration,
) (PairingClaim, error) {
	if now.IsZero() {
		return PairingClaim{}, errors.New("relay pairing claim time is required")
	}
	if lease <= 0 || lease > MaximumPairingTTL {
		return PairingClaim{}, errors.New("relay pairing claim lease is invalid")
	}
	codeHash, id, err := pairingIdentity(code)
	if err != nil {
		return PairingClaim{}, err
	}
	release, err := acquireStorageLock(filepath.Join(store.tmp, id+".lock"))
	if err != nil {
		return PairingClaim{}, err
	}
	defer release()
	if _, err := readPairing(store.historyPath(id)); err == nil {
		return PairingClaim{}, ErrPairingConsumed
	} else if !errors.Is(err, os.ErrNotExist) {
		return PairingClaim{}, err
	}
	record, err := readPairing(store.inflightPath(id))
	if err == nil {
		if record.CodeHash != codeHash {
			return PairingClaim{}, ErrPairingNotFound
		}
		if record.LeaseUntil.After(now.UTC()) {
			return PairingClaim{}, ErrPairingClaimed
		}
	} else if errors.Is(err, os.ErrNotExist) {
		record, err = readPairing(store.pendingPath(id))
		if errors.Is(err, os.ErrNotExist) {
			return PairingClaim{}, ErrPairingNotFound
		}
		if err != nil {
			return PairingClaim{}, err
		}
		if record.CodeHash != codeHash {
			return PairingClaim{}, ErrPairingNotFound
		}
	} else {
		return PairingClaim{}, err
	}
	if !record.ExpiresAt.After(now.UTC()) {
		return PairingClaim{}, ErrPairingExpired
	}
	claimID, err := store.randomHex(16)
	if err != nil {
		return PairingClaim{}, err
	}
	record.ClaimID = claimID
	record.LeaseUntil = now.UTC().Add(lease)
	source := store.pendingPath(id)
	if _, err := os.Stat(store.inflightPath(id)); err == nil {
		source = store.inflightPath(id)
	}
	if err := writePairingAtomic(source, record); err != nil {
		return PairingClaim{}, err
	}
	if source != store.inflightPath(id) {
		if err := os.Rename(source, store.inflightPath(id)); err != nil {
			return PairingClaim{}, err
		}
		if err := syncPairingMove(source, store.inflightPath(id)); err != nil {
			return PairingClaim{}, err
		}
	}
	return PairingClaim{Record: record, ClaimID: claimID}, nil
}

func (store *PairingStore) Release(
	claim PairingClaim,
	now time.Time,
) error {
	if now.IsZero() {
		return errors.New("relay pairing release time is required")
	}
	record, release, err := store.claimedRecord(claim)
	if err != nil {
		return err
	}
	defer release()
	record.ClaimID = ""
	record.LeaseUntil = time.Time{}
	if err := writePairingAtomic(store.inflightPath(record.ID), record); err != nil {
		return err
	}
	if err := os.Rename(
		store.inflightPath(record.ID),
		store.pendingPath(record.ID),
	); err != nil {
		return err
	}
	return syncPairingMove(
		store.inflightPath(record.ID),
		store.pendingPath(record.ID),
	)
}

func (store *PairingStore) Commit(
	claim PairingClaim,
	peerID string,
	now time.Time,
) error {
	if now.IsZero() {
		return errors.New("relay pairing commit time is required")
	}
	if err := validateIdentifier("peer id", peerID); err != nil {
		return err
	}
	record, release, err := store.claimedRecord(claim)
	if err != nil {
		return err
	}
	defer release()
	if !record.ExpiresAt.After(now.UTC()) {
		return ErrPairingExpired
	}
	record.ClaimID = ""
	record.LeaseUntil = time.Time{}
	record.PeerID = peerID
	record.ConsumedAt = now.UTC()
	if err := writePairingAtomic(store.inflightPath(record.ID), record); err != nil {
		return err
	}
	if err := os.Rename(
		store.inflightPath(record.ID),
		store.historyPath(record.ID),
	); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrPairingConsumed
		}
		return err
	}
	return syncPairingMove(
		store.inflightPath(record.ID),
		store.historyPath(record.ID),
	)
}

func (store *PairingStore) claimedRecord(
	claim PairingClaim,
) (PairingRecord, func(), error) {
	if claim.Record.ID == "" || claim.ClaimID == "" {
		return PairingRecord{}, nil, errors.New("relay pairing claim is invalid")
	}
	release, err := acquireStorageLock(
		filepath.Join(store.tmp, claim.Record.ID+".lock"),
	)
	if err != nil {
		return PairingRecord{}, nil, err
	}
	record, err := readPairing(store.inflightPath(claim.Record.ID))
	if errors.Is(err, os.ErrNotExist) {
		if _, historyErr := readPairing(
			store.historyPath(claim.Record.ID),
		); historyErr == nil {
			release()
			return PairingRecord{}, nil, ErrPairingConsumed
		} else if !errors.Is(historyErr, os.ErrNotExist) {
			release()
			return PairingRecord{}, nil, historyErr
		}
	}
	if err != nil {
		release()
		if errors.Is(err, os.ErrNotExist) {
			return PairingRecord{}, nil, ErrPairingNotFound
		}
		return PairingRecord{}, nil, err
	}
	if record.ClaimID != claim.ClaimID {
		release()
		return PairingRecord{}, nil, ErrPairingClaimed
	}
	return record, release, nil
}

func (policy PairingPolicy) Validate() error {
	if err := validateIdentifier("teamId", policy.TeamID); err != nil {
		return err
	}
	if err := validatePairingValues(
		"allowedSources",
		policy.AllowedSources,
		event.IsKnownSource,
	); err != nil {
		return err
	}
	return validatePairingValues(
		"allowedRuntimes",
		policy.AllowedRuntimes,
		func(value string) bool {
			return event.IsKnownRuntime(value) && value != "host"
		},
	)
}

func validatePairingValues(
	label string,
	values []string,
	allowed func(string) bool,
) error {
	if len(values) == 0 {
		return fmt.Errorf("%s must contain at least one value", label)
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !allowed(value) {
			return fmt.Errorf("%s contains unsupported value %q", label, value)
		}
		if seen[value] {
			return fmt.Errorf("%s contains duplicate value %q", label, value)
		}
		seen[value] = true
	}
	return nil
}

func (store *PairingStore) newCode() (string, error) {
	value := make([]byte, 20)
	if _, err := io.ReadFull(store.random(), value); err != nil {
		return "", err
	}
	encoded := pairingEncoding.EncodeToString(value)
	return "AGBR-" + strings.Join([]string{
		encoded[0:8],
		encoded[8:16],
		encoded[16:24],
		encoded[24:32],
	}, "-"), nil
}

func (store *PairingStore) randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(store.random(), value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (store *PairingStore) random() io.Reader {
	if store.Random != nil {
		return store.Random
	}
	return rand.Reader
}

func pairingIdentity(code string) (string, string, error) {
	canonical := strings.ToUpper(strings.TrimSpace(code))
	if !pairingCodePattern.MatchString(canonical) {
		return "", "", ErrInvalidPairingCode
	}
	sum := sha256.Sum256([]byte(canonical))
	id := hex.EncodeToString(sum[:])
	return "sha256:" + id, id, nil
}

func readPairing(path string) (PairingRecord, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return PairingRecord{}, err
	}
	var record PairingRecord
	if err := strictJSON(value, &record); err != nil {
		return PairingRecord{}, fmt.Errorf("decode relay pairing record: %w", err)
	}
	if record.Version != pairingVersion ||
		len(record.ID) != sha256.Size*2 ||
		record.CodeHash != "sha256:"+record.ID ||
		record.CreatedAt.IsZero() ||
		record.ExpiresAt.IsZero() ||
		!record.CreatedAt.Before(record.ExpiresAt) ||
		(record.ClaimID == "") != record.LeaseUntil.IsZero() ||
		(record.PeerID == "") != record.ConsumedAt.IsZero() {
		return PairingRecord{}, errors.New("invalid persisted relay pairing record")
	}
	if err := record.Policy.Validate(); err != nil {
		return PairingRecord{}, err
	}
	return record, nil
}

func writePairingAtomic(path string, record PairingRecord) error {
	value, err := strictPairingJSON(record)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".pairing-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := temporary.Write(value); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return syncDirectory(directory)
}

func strictPairingJSON(record PairingRecord) ([]byte, error) {
	// writeNewPrivateJSON already establishes the canonical record shape on
	// first creation; updates use the same standard-library encoder.
	value, err := jsonMarshal(record)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func jsonMarshal(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func syncPairingMove(source, destination string) error {
	sourceDirectory := filepath.Dir(source)
	destinationDirectory := filepath.Dir(destination)
	if err := syncDirectory(sourceDirectory); err != nil {
		return err
	}
	if destinationDirectory == sourceDirectory {
		return nil
	}
	return syncDirectory(destinationDirectory)
}

func (store *PairingStore) pendingPath(id string) string {
	return filepath.Join(store.pending, id+".json")
}

func (store *PairingStore) inflightPath(id string) string {
	return filepath.Join(store.inflight, id+".json")
}

func (store *PairingStore) historyPath(id string) string {
	return filepath.Join(store.history, id+".json")
}
