package binding

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	recordVersion      = 1
	minimumTTL         = 2 * time.Minute
	maximumTTL         = 30 * time.Minute
	bindingLockTimeout = 5 * time.Second
	bindingLockStale   = 2 * time.Minute
	bindingLockPoll    = 2 * time.Millisecond
	codeAlphabet       = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

var (
	ErrInvalidCode = errors.New("invalid binding code")
	ErrNotFound    = errors.New("binding code not found")
	ErrExpired     = errors.New("binding code expired")
	ErrClaimed     = errors.New("binding code is already claimed")
	ErrConsumed    = errors.New("binding code is already consumed")
	ErrCancelled   = errors.New("binding code is cancelled")
)

type Record struct {
	Version     int       `json:"version"`
	CodeHash    string    `json:"codeHash"`
	ChannelName string    `json:"channelName"`
	As          string    `json:"as"`
	LarkCLIPath string    `json:"larkCliPath,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	ClaimID     string    `json:"claimId,omitempty"`
	ClaimedAt   time.Time `json:"claimedAt,omitempty"`
	LeaseUntil  time.Time `json:"leaseUntil,omitempty"`
	ConsumedAt  time.Time `json:"consumedAt,omitempty"`
	CancelledAt time.Time `json:"cancelledAt,omitempty"`
}

type Status struct {
	State       string    `json:"state"`
	ChannelName string    `json:"channelName"`
	As          string    `json:"as"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	ConsumedAt  time.Time `json:"consumedAt,omitempty"`
	CancelledAt time.Time `json:"cancelledAt,omitempty"`
}

type Claim struct {
	CodeHash string
	ClaimID  string
	Record   Record
}

type Store struct {
	Root   string
	Random io.Reader
}

func NewStore(root string) *Store {
	return &Store{Root: root, Random: rand.Reader}
}

func (store *Store) Create(
	channelName string,
	as string,
	ttl time.Duration,
	now time.Time,
	larkCLIPath ...string,
) (string, Record, error) {
	channelName = strings.TrimSpace(channelName)
	if channelName == "" || len(channelName) > 120 {
		return "", Record{}, errors.New("channel name must contain 1 to 120 bytes")
	}
	if as != "bot" && as != "user" {
		return "", Record{}, errors.New("binding identity must be bot or user")
	}
	if ttl < minimumTTL || ttl > maximumTTL {
		return "", Record{}, fmt.Errorf(
			"binding TTL must be between %s and %s",
			minimumTTL,
			maximumTTL,
		)
	}
	if len(larkCLIPath) > 1 {
		return "", Record{}, errors.New("binding accepts one lark-cli path")
	}
	selectedLarkCLIPath := ""
	if len(larkCLIPath) == 1 {
		selectedLarkCLIPath = strings.TrimSpace(larkCLIPath[0])
		if !validLarkCLIPath(selectedLarkCLIPath) {
			return "", Record{}, errors.New(
				"binding lark-cli path must be an absolute executable path",
			)
		}
	}
	if err := store.ensureDirectories(); err != nil {
		return "", Record{}, err
	}

	for range 8 {
		code, err := store.newCode()
		if err != nil {
			return "", Record{}, err
		}
		codeHash, fileName := bindingHash(code)
		record := Record{
			Version:     recordVersion,
			CodeHash:    codeHash,
			ChannelName: channelName,
			As:          as,
			LarkCLIPath: selectedLarkCLIPath,
			CreatedAt:   now.UTC(),
			ExpiresAt:   now.UTC().Add(ttl),
		}
		path := filepath.Join(store.directory("pending"), fileName)
		if err := writeNewRecord(path, record); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", Record{}, err
		}
		return code, record, nil
	}
	return "", Record{}, errors.New("could not allocate a unique binding code")
}

func (store *Store) Load(code string, now time.Time) (Record, error) {
	_, fileName, err := canonicalHash(code)
	if err != nil {
		return Record{}, err
	}
	if err := store.ensureDirectories(); err != nil {
		return Record{}, err
	}
	if _, statErr := os.Stat(filepath.Join(store.directory("history"), fileName)); statErr == nil {
		return Record{}, ErrConsumed
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Record{}, statErr
	}
	if _, statErr := os.Stat(filepath.Join(store.directory("inflight"), fileName)); statErr == nil {
		return Record{}, ErrClaimed
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Record{}, statErr
	}
	record, err := readRecord(filepath.Join(store.directory("pending"), fileName))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	if !record.CancelledAt.IsZero() {
		return Record{}, ErrCancelled
	}
	if !now.UTC().Before(record.ExpiresAt) {
		return Record{}, ErrExpired
	}
	return record, nil
}

func (store *Store) Claim(
	code string,
	now time.Time,
	lease time.Duration,
) (Claim, error) {
	if lease <= 0 {
		return Claim{}, errors.New("claim lease must be positive")
	}
	codeHash, fileName, err := canonicalHash(code)
	if err != nil {
		return Claim{}, err
	}
	if err := store.ensureDirectories(); err != nil {
		return Claim{}, err
	}
	release, err := store.acquireRecordLock(fileName)
	if err != nil {
		return Claim{}, err
	}
	defer release()
	if _, err := store.recoverExpiredFile(fileName, now); err != nil {
		return Claim{}, err
	}

	pendingPath := filepath.Join(store.directory("pending"), fileName)
	inflightPath := filepath.Join(store.directory("inflight"), fileName)
	if err := os.Rename(pendingPath, inflightPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Claim{}, err
		}
		if _, historyErr := os.Stat(
			filepath.Join(store.directory("history"), fileName),
		); historyErr == nil {
			return Claim{}, ErrConsumed
		}
		if _, inflightErr := os.Stat(inflightPath); inflightErr == nil {
			return Claim{}, ErrClaimed
		}
		return Claim{}, ErrNotFound
	}

	record, err := readRecord(inflightPath)
	if err != nil {
		_ = os.Rename(inflightPath, pendingPath)
		return Claim{}, err
	}
	if record.CodeHash != codeHash {
		_ = os.Rename(inflightPath, pendingPath)
		return Claim{}, errors.New("binding record hash does not match its file")
	}
	if !record.CancelledAt.IsZero() {
		return Claim{}, ErrCancelled
	}
	if !now.UTC().Before(record.ExpiresAt) {
		_ = os.Rename(inflightPath, pendingPath)
		return Claim{}, ErrExpired
	}
	claimID, err := store.randomHex(16)
	if err != nil {
		_ = os.Rename(inflightPath, pendingPath)
		return Claim{}, err
	}
	record.ClaimID = claimID
	record.ClaimedAt = now.UTC()
	record.LeaseUntil = now.UTC().Add(lease)
	if err := writeRecordAtomic(inflightPath, record); err != nil {
		_ = os.Rename(inflightPath, pendingPath)
		return Claim{}, err
	}
	return Claim{CodeHash: codeHash, ClaimID: claimID, Record: record}, nil
}

func (store *Store) Commit(claim Claim, now time.Time) error {
	if claim.CodeHash == "" || claim.ClaimID == "" {
		return errors.New("binding claim is incomplete")
	}
	fileName, err := fileNameForHash(claim.CodeHash)
	if err != nil {
		return err
	}
	if err := store.ensureDirectories(); err != nil {
		return err
	}
	release, err := store.acquireRecordLock(fileName)
	if err != nil {
		return err
	}
	defer release()
	historyPath := filepath.Join(store.directory("history"), fileName)
	if _, err := os.Stat(historyPath); err == nil {
		return ErrConsumed
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	inflightPath := filepath.Join(store.directory("inflight"), fileName)
	record, err := readRecord(inflightPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, historyErr := os.Stat(historyPath); historyErr == nil {
			return ErrConsumed
		}
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if record.ClaimID != claim.ClaimID {
		return ErrClaimed
	}
	record.ConsumedAt = now.UTC()
	if err := writeNewRecord(historyPath, record); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrConsumed
		}
		return err
	}
	if err := os.Remove(inflightPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (store *Store) Release(claim Claim) error {
	if claim.CodeHash == "" || claim.ClaimID == "" {
		return errors.New("binding claim is incomplete")
	}
	fileName, err := fileNameForHash(claim.CodeHash)
	if err != nil {
		return err
	}
	if err := store.ensureDirectories(); err != nil {
		return err
	}
	release, err := store.acquireRecordLock(fileName)
	if err != nil {
		return err
	}
	defer release()
	inflightPath := filepath.Join(store.directory("inflight"), fileName)
	record, err := readRecord(inflightPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, historyErr := os.Stat(
			filepath.Join(store.directory("history"), fileName),
		); historyErr == nil {
			return ErrConsumed
		}
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if record.ClaimID != claim.ClaimID {
		return ErrClaimed
	}
	clearClaim(&record)
	if err := writeRecordAtomic(inflightPath, record); err != nil {
		return err
	}
	pendingPath := filepath.Join(store.directory("pending"), fileName)
	if err := os.Rename(inflightPath, pendingPath); err != nil {
		return err
	}
	return nil
}

func (store *Store) RecoverExpired(now time.Time) (int, error) {
	if err := store.ensureDirectories(); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(store.directory("inflight"))
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		release, err := store.acquireRecordLock(entry.Name())
		if err != nil {
			return recovered, err
		}
		didRecover, recoverErr := store.recoverExpiredFile(entry.Name(), now)
		release()
		if recoverErr != nil {
			return recovered, recoverErr
		}
		if didRecover {
			recovered++
		}
	}
	return recovered, nil
}

// recoverExpiredFile inspects and transitions one record. The caller must own
// that record's lock for the full read/write/move sequence.
func (store *Store) recoverExpiredFile(fileName string, now time.Time) (bool, error) {
	inflightPath := filepath.Join(store.directory("inflight"), fileName)
	record, err := readRecord(inflightPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if record.LeaseUntil.IsZero() || record.LeaseUntil.After(now.UTC()) {
		return false, nil
	}
	historyPath := filepath.Join(store.directory("history"), fileName)
	if _, err := os.Stat(historyPath); err == nil {
		if removeErr := os.Remove(inflightPath); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			return false, removeErr
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	clearClaim(&record)
	if err := writeRecordAtomic(inflightPath, record); err != nil {
		return false, err
	}
	pendingPath := filepath.Join(store.directory("pending"), fileName)
	if err := os.Rename(inflightPath, pendingPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			_ = os.Remove(inflightPath)
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (store *Store) Cancel(code string, now time.Time) error {
	if now.IsZero() {
		return errors.New("binding cancellation time is required")
	}
	_, fileName, err := canonicalHash(code)
	if err != nil {
		return err
	}
	if err := store.ensureDirectories(); err != nil {
		return err
	}
	release, err := store.acquireRecordLock(fileName)
	if err != nil {
		return err
	}
	defer release()
	historyPath := filepath.Join(store.directory("history"), fileName)
	if history, readErr := readRecord(historyPath); readErr == nil {
		if !history.CancelledAt.IsZero() {
			return ErrCancelled
		}
		return ErrConsumed
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if _, statErr := os.Stat(
		filepath.Join(store.directory("inflight"), fileName),
	); statErr == nil {
		return ErrClaimed
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	pendingPath := filepath.Join(store.directory("pending"), fileName)
	record, err := readRecord(pendingPath)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !record.CancelledAt.IsZero() {
		return ErrCancelled
	}
	clearClaim(&record)
	record.CancelledAt = now.UTC()
	if err := writeRecordAtomic(pendingPath, record); err != nil {
		return err
	}
	if err := os.Rename(pendingPath, historyPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrCancelled
		}
		return err
	}
	return nil
}

func (store *Store) List(now time.Time) ([]Status, error) {
	if now.IsZero() {
		return nil, errors.New("binding status time is required")
	}
	if err := store.ensureDirectories(); err != nil {
		return nil, err
	}
	result := make([]Status, 0)
	for _, directory := range []string{"pending", "inflight", "history"} {
		entries, err := os.ReadDir(store.directory(directory))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			record, err := readRecord(filepath.Join(
				store.directory(directory),
				entry.Name(),
			))
			if err != nil {
				return nil, err
			}
			state := directory
			switch {
			case !record.CancelledAt.IsZero():
				state = "cancelled"
			case !record.ConsumedAt.IsZero():
				state = "consumed"
			case !now.UTC().Before(record.ExpiresAt):
				state = "expired"
			}
			result = append(result, Status{
				State:       state,
				ChannelName: record.ChannelName,
				As:          record.As,
				CreatedAt:   record.CreatedAt,
				ExpiresAt:   record.ExpiresAt,
				ConsumedAt:  record.ConsumedAt,
				CancelledAt: record.CancelledAt,
			})
		}
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

func (store *Store) ensureDirectories() error {
	if strings.TrimSpace(store.Root) == "" {
		return errors.New("binding store root is required")
	}
	if err := os.MkdirAll(store.Root, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"pending", "inflight", "history", "tmp"} {
		if err := os.MkdirAll(store.directory(name), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) directory(name string) string {
	return filepath.Join(store.Root, name)
}

func (store *Store) acquireRecordLock(fileName string) (func(), error) {
	return acquireBindingLock(filepath.Join(
		store.directory("tmp"),
		fileName+".lock",
	), bindingLockTimeout, bindingLockPoll, bindingLockStale)
}

func (store *Store) newCode() (string, error) {
	value := make([]byte, 13)
	if _, err := io.ReadFull(store.random(), value); err != nil {
		return "", err
	}
	encoded := crockford(value)
	if len(encoded) < 20 {
		return "", errors.New("binding entropy encoding is incomplete")
	}
	encoded = encoded[:20]
	return "AGB-" + strings.Join([]string{
		encoded[0:5],
		encoded[5:10],
		encoded[10:15],
		encoded[15:20],
	}, "-"), nil
}

func (store *Store) randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(store.random(), value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (store *Store) random() io.Reader {
	if store.Random != nil {
		return store.Random
	}
	return rand.Reader
}

func crockford(value []byte) string {
	var builder strings.Builder
	var buffer uint32
	bits := 0
	for _, current := range value {
		buffer = (buffer << 8) | uint32(current)
		bits += 8
		for bits >= 5 {
			bits -= 5
			builder.WriteByte(codeAlphabet[(buffer>>bits)&31])
			if bits == 0 {
				buffer = 0
			} else {
				buffer &= (1 << bits) - 1
			}
		}
	}
	if bits > 0 {
		builder.WriteByte(codeAlphabet[(buffer<<(5-bits))&31])
	}
	return builder.String()
}

func canonicalHash(code string) (string, string, error) {
	canonical, err := canonicalCode(code)
	if err != nil {
		return "", "", err
	}
	codeHash, fileName := bindingHash(canonical)
	return codeHash, fileName, nil
}

func canonicalCode(code string) (string, error) {
	value := strings.ToUpper(strings.TrimSpace(code))
	parts := strings.Split(value, "-")
	if len(parts) != 5 || parts[0] != "AGB" {
		return "", ErrInvalidCode
	}
	for _, part := range parts[1:] {
		if len(part) != 5 {
			return "", ErrInvalidCode
		}
		for _, character := range part {
			if !strings.ContainsRune(codeAlphabet, character) {
				return "", ErrInvalidCode
			}
		}
	}
	return value, nil
}

func bindingHash(code string) (string, string) {
	sum := sha256.Sum256([]byte(code))
	encoded := hex.EncodeToString(sum[:])
	return "sha256:" + encoded, encoded + ".json"
}

func fileNameForHash(value string) (string, error) {
	if !strings.HasPrefix(value, "sha256:") {
		return "", errors.New("binding hash must use sha256")
	}
	encoded := strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("binding hash is invalid")
	}
	return encoded + ".json", nil
}

func clearClaim(record *Record) {
	record.ClaimID = ""
	record.ClaimedAt = time.Time{}
	record.LeaseUntil = time.Time{}
}

func readRecord(path string) (Record, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("parse binding record: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Record{}, err
	}
	if record.Version != recordVersion ||
		record.CodeHash == "" ||
		record.ChannelName == "" ||
		(record.As != "bot" && record.As != "user") ||
		record.CreatedAt.IsZero() ||
		record.ExpiresAt.IsZero() ||
		!record.CreatedAt.Before(record.ExpiresAt) ||
		(record.LarkCLIPath != "" && !validLarkCLIPath(record.LarkCLIPath)) ||
		(!record.ConsumedAt.IsZero() && !record.CancelledAt.IsZero()) {
		return Record{}, errors.New("binding record is invalid")
	}
	return record, nil
}

func validLarkCLIPath(value string) bool {
	return filepath.IsAbs(value) &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("binding record contains trailing JSON")
		}
		return fmt.Errorf("parse binding record trailer: %w", err)
	}
	return nil
}

func writeNewRecord(path string, record Record) error {
	value, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	value = append(value, '\n')
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, "binding-new-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(temporary)
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Link(temporary, path)
}

func writeRecordAtomic(path string, record Record) error {
	value, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	value = append(value, '\n')
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, "binding-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(value); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func acquireBindingLock(
	path string,
	timeout time.Duration,
	poll time.Duration,
	staleAfter time.Duration,
) (func(), error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	deadline := time.Now().Add(timeout)
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if chmodErr := setBindingLockPermissions(file); chmodErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, chmodErr
			}
			if _, writeErr := file.WriteString(token); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, writeErr
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, syncErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, closeErr
			}
			return ownedBindingLockRelease(path, token), nil
		}
		if !isBindingLockContention(err) {
			return nil, fmt.Errorf("acquire binding lock: %w", err)
		}

		info, statErr := os.Stat(path)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			continue
		case isBindingLockContention(statErr):
			// Windows may transiently deny inspection while another owner
			// publishes or removes the lock.
		case statErr != nil:
			return nil, fmt.Errorf("inspect binding lock: %w", statErr)
		case staleAfter > 0 && time.Since(info.ModTime()) > staleAfter:
			removeErr := os.Remove(path)
			if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
			if !isBindingLockContention(removeErr) {
				return nil, fmt.Errorf("remove stale binding lock: %w", removeErr)
			}
		}
		if timeout > 0 && !time.Now().Before(deadline) {
			return nil, errors.New("timed out waiting for binding lock")
		}
		time.Sleep(poll)
	}
}

func ownedBindingLockRelease(path, token string) func() {
	return func() {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != token {
			return
		}
		_ = os.Remove(path)
	}
}
