package remoteconfig

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	PeerAdd    PeerAction = "add"
	PeerRevoke PeerAction = "revoke"
	PeerRemove PeerAction = "remove"

	defaultRelayLockTimeout = 5 * time.Second
	defaultRelayLockPoll    = 5 * time.Millisecond
	defaultRelayLockStale   = 2 * time.Minute
)

var (
	ErrRelayExists  = errors.New("relay config already exists")
	ErrRelayChanged = errors.New(
		"relay config changed before write",
	)
	ErrPeerExists   = errors.New("relay peer already exists")
	ErrPeerNotFound = errors.New("relay peer not found")
	ErrPeerRevoked  = errors.New("relay peer is already revoked")
)

type PeerAction string

type PeerChange struct {
	Action           PeerAction
	Peer             Peer
	PeerID           string
	ExpectedRevision string
	DryRun           bool
}

type RelaySnapshot struct {
	Config   RelayConfig `json:"config"`
	Revision string      `json:"revision"`
}

type PeerResult struct {
	Before RelaySnapshot `json:"before"`
	After  RelaySnapshot `json:"after"`
	DryRun bool          `json:"dryRun"`
}

type RelayTransactions struct {
	Path string

	lockTimeout time.Duration
	lockPoll    time.Duration
	lockStale   time.Duration
}

func NewRelayTransactions(path string) *RelayTransactions {
	return &RelayTransactions{
		Path:        path,
		lockTimeout: defaultRelayLockTimeout,
		lockPoll:    defaultRelayLockPoll,
		lockStale:   defaultRelayLockStale,
	}
}

func (transactions *RelayTransactions) List(
	ctx context.Context,
) (RelaySnapshot, error) {
	if err := transactions.validate(); err != nil {
		return RelaySnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return RelaySnapshot{}, err
	}
	value, err := LoadRelay(transactions.Path)
	if err != nil {
		return RelaySnapshot{}, err
	}
	return relaySnapshot(value)
}

func (transactions *RelayTransactions) Initialize(
	ctx context.Context,
	value RelayConfig,
	dryRun bool,
) (RelaySnapshot, error) {
	if err := transactions.validate(); err != nil {
		return RelaySnapshot{}, err
	}
	if err := value.Validate(); err != nil {
		return RelaySnapshot{}, err
	}
	prospective, err := relaySnapshot(value)
	if err != nil {
		return RelaySnapshot{}, err
	}
	if dryRun {
		return prospective, nil
	}
	release, err := transactions.acquire(ctx)
	if err != nil {
		return RelaySnapshot{}, err
	}
	defer release()
	if _, err := LoadRelay(transactions.Path); err == nil {
		return RelaySnapshot{}, ErrRelayExists
	} else if !errors.Is(err, ErrNotFound) {
		return RelaySnapshot{}, err
	}
	if err := SaveRelay(transactions.Path, &value); err != nil {
		return RelaySnapshot{}, err
	}
	return transactions.List(ctx)
}

func (transactions *RelayTransactions) Apply(
	ctx context.Context,
	change PeerChange,
) (PeerResult, error) {
	if err := transactions.validate(); err != nil {
		return PeerResult{}, err
	}
	if change.DryRun {
		before, err := transactions.List(ctx)
		if err != nil {
			return PeerResult{}, err
		}
		return applyPeerChange(before, change)
	}
	release, err := transactions.acquire(ctx)
	if err != nil {
		return PeerResult{}, err
	}
	defer release()
	before, err := transactions.List(ctx)
	if err != nil {
		return PeerResult{}, err
	}
	result, err := applyPeerChange(before, change)
	if err != nil {
		return PeerResult{}, err
	}
	value := result.After.Config
	if err := SaveRelay(transactions.Path, &value); err != nil {
		return PeerResult{}, err
	}
	after, err := transactions.List(ctx)
	if err != nil {
		return PeerResult{}, err
	}
	result.After = after
	return result, nil
}

func applyPeerChange(
	before RelaySnapshot,
	change PeerChange,
) (PeerResult, error) {
	if change.ExpectedRevision != "" &&
		change.ExpectedRevision != before.Revision {
		return PeerResult{}, ErrRelayChanged
	}
	value, err := cloneRelayConfig(before.Config)
	if err != nil {
		return PeerResult{}, err
	}
	switch change.Action {
	case PeerAdd:
		if err := change.Peer.Validate(); err != nil {
			return PeerResult{}, err
		}
		for _, existing := range value.Peers {
			if existing.ID == change.Peer.ID {
				return PeerResult{}, ErrPeerExists
			}
		}
		value.Peers = append(value.Peers, change.Peer)
	case PeerRevoke:
		peerID := strings.TrimSpace(change.PeerID)
		if peerID == "" {
			return PeerResult{}, errors.New("relay peer id is required")
		}
		found := false
		for index := range value.Peers {
			if value.Peers[index].ID != peerID {
				continue
			}
			found = true
			if value.Peers[index].Revoked {
				return PeerResult{}, ErrPeerRevoked
			}
			value.Peers[index].Revoked = true
			break
		}
		if !found {
			return PeerResult{}, ErrPeerNotFound
		}
	case PeerRemove:
		if change.ExpectedRevision == "" {
			return PeerResult{}, errors.New(
				"relay peer removal requires an expected revision",
			)
		}
		peerID := strings.TrimSpace(change.PeerID)
		if peerID == "" {
			return PeerResult{}, errors.New("relay peer id is required")
		}
		found := false
		peers := make([]Peer, 0, len(value.Peers))
		for _, existing := range value.Peers {
			if existing.ID == peerID {
				found = true
				continue
			}
			peers = append(peers, existing)
		}
		if !found {
			return PeerResult{}, ErrPeerNotFound
		}
		value.Peers = peers
	default:
		return PeerResult{}, fmt.Errorf(
			"unsupported relay peer action %q",
			change.Action,
		)
	}
	if err := value.Validate(); err != nil {
		return PeerResult{}, err
	}
	after, err := relaySnapshot(value)
	if err != nil {
		return PeerResult{}, err
	}
	return PeerResult{
		Before: before,
		After:  after,
		DryRun: change.DryRun,
	}, nil
}

func relaySnapshot(value RelayConfig) (RelaySnapshot, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return RelaySnapshot{}, err
	}
	sum := sha256.Sum256(encoded)
	return RelaySnapshot{
		Config:   value,
		Revision: "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

func cloneRelayConfig(value RelayConfig) (RelayConfig, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return RelayConfig{}, err
	}
	var cloned RelayConfig
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return RelayConfig{}, err
	}
	return cloned, nil
}

func (transactions *RelayTransactions) validate() error {
	if transactions == nil ||
		strings.TrimSpace(transactions.Path) == "" ||
		!filepath.IsAbs(transactions.Path) {
		return errors.New("absolute relay config path is required")
	}
	return nil
}

func (transactions *RelayTransactions) acquire(
	ctx context.Context,
) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(transactions.Path), 0o700); err != nil {
		return nil, err
	}
	if err := setSidecarPermissions(
		filepath.Dir(transactions.Path),
		0o700,
	); err != nil {
		return nil, err
	}
	lockPath := transactions.Path + ".lock"
	deadline := time.Now().Add(transactions.lockTimeout)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(
			lockPath,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if err == nil {
			if permissionErr := setSidecarFilePermissions(
				file,
				0o600,
			); permissionErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, permissionErr
			}
			if _, writeErr := file.WriteString(token); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, writeErr
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, syncErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, closeErr
			}
			return ownedSidecarLockRelease(lockPath, token), nil
		}
		if !isSidecarLockContention(err) {
			return nil, err
		}
		info, statErr := os.Stat(lockPath)
		if statErr == nil &&
			time.Since(info.ModTime()) > transactions.lockStale {
			if removeErr := os.Remove(lockPath); removeErr == nil ||
				errors.Is(removeErr, os.ErrNotExist) {
				continue
			} else if !isSidecarLockContention(removeErr) {
				return nil, removeErr
			}
		} else if isSidecarLockContention(statErr) {
			// Windows may transiently report ACCESS_DENIED while another
			// writer publishes or deletes the lock file.
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
		if !time.Now().Before(deadline) {
			return nil, errors.New("timed out waiting for relay config lock")
		}
		timer := time.NewTimer(transactions.lockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func ownedSidecarLockRelease(path, token string) func() {
	return func() {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != token {
			return
		}
		_ = os.Remove(path)
	}
}
