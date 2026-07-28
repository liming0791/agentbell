package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	ChannelAdd        ChannelAction = "add"
	ChannelRename     ChannelAction = "rename"
	ChannelRemove     ChannelAction = "remove"
	ChannelSetDefault ChannelAction = "default"

	defaultChannelLockTimeout = 5 * time.Second
	defaultChannelLockPoll    = 5 * time.Millisecond
	defaultChannelLockStale   = 2 * time.Minute
	maximumConfigSize         = 1 << 20
)

var (
	ErrChannelExists              = errors.New("channel already exists")
	ErrChannelNotFound            = errors.New("channel not found")
	ErrLastChannel                = errors.New("cannot remove the last channel")
	ErrReplacementDefaultRequired = errors.New("replacement default channel is required")
	ErrUnsupportedChannelAction   = errors.New("unsupported channel action")
	ErrInvalidChannelChange       = errors.New("invalid channel change")
	ErrConfigChanged              = errors.New("config changed before write")
	ErrConfigLockTimeout          = errors.New("timed out waiting for config lock")
)

type ChannelAction string

// ChannelChange describes one atomic channel mutation. ExpectedRevision is
// optional; when present it provides caller-visible compare-and-swap semantics.
// DryRun returns the fully validated prospective result without creating a lock
// file or writing config.json.
type ChannelChange struct {
	Action             ChannelAction
	Channel            Channel
	ChannelID          string
	Name               string
	ReplacementDefault string
	SetDefault         bool
	ExpectedRevision   string
	DryRun             bool
}

// ChannelSnapshot is an immutable config view and a content-addressed revision.
// Revision is safe to display and can be supplied as ExpectedRevision.
type ChannelSnapshot struct {
	Config   Config `json:"config"`
	Revision string `json:"revision"`
}

type ChannelResult struct {
	Before ChannelSnapshot `json:"before"`
	After  ChannelSnapshot `json:"after"`
	DryRun bool            `json:"dryRun"`
}

// ChannelTransactions owns cross-process channel updates for one config.json.
// The zero value is invalid; construct it with NewChannelTransactions.
type ChannelTransactions struct {
	Path string

	lockTimeout time.Duration
	lockPoll    time.Duration
	lockStale   time.Duration

	beforeCompare func()
}

func NewChannelTransactions(path string) *ChannelTransactions {
	return &ChannelTransactions{
		Path:        path,
		lockTimeout: defaultChannelLockTimeout,
		lockPoll:    defaultChannelLockPoll,
		lockStale:   defaultChannelLockStale,
	}
}

func (transactions *ChannelTransactions) List(ctx context.Context) (ChannelSnapshot, error) {
	if transactions == nil || strings.TrimSpace(transactions.Path) == "" {
		return ChannelSnapshot{}, errors.New("config path is required")
	}
	if err := ctx.Err(); err != nil {
		return ChannelSnapshot{}, err
	}
	return loadChannelSnapshot(transactions.Path)
}

func (transactions *ChannelTransactions) Apply(
	ctx context.Context,
	change ChannelChange,
) (ChannelResult, error) {
	if transactions == nil || strings.TrimSpace(transactions.Path) == "" {
		return ChannelResult{}, errors.New("config path is required")
	}
	if change.DryRun {
		before, err := transactions.List(ctx)
		if err != nil {
			return ChannelResult{}, err
		}
		return transactions.applyToSnapshot(before, change)
	}

	lock, err := acquireChannelLock(
		ctx,
		transactions.Path,
		transactions.timeout(),
		transactions.poll(),
		transactions.stale(),
	)
	if err != nil {
		return ChannelResult{}, err
	}
	defer lock.release()

	before, err := loadChannelSnapshot(transactions.Path)
	if err != nil {
		return ChannelResult{}, err
	}
	result, err := transactions.applyToSnapshot(before, change)
	if err != nil {
		return ChannelResult{}, err
	}
	if transactions.beforeCompare != nil {
		transactions.beforeCompare()
	}
	current, err := loadChannelSnapshot(transactions.Path)
	if err != nil {
		return ChannelResult{}, err
	}
	if current.Revision != before.Revision {
		return ChannelResult{}, ErrConfigChanged
	}
	if err := Save(transactions.Path, &result.After.Config); err != nil {
		return ChannelResult{}, err
	}
	committed, err := loadChannelSnapshot(transactions.Path)
	if err != nil {
		return ChannelResult{}, err
	}
	result.After = committed
	return result, nil
}

// Initialize publishes the first valid config.json without overwriting a
// concurrently-created config. The same lock as Apply is used for cooperating
// writers, while an exclusive hard-link publication provides filesystem-level
// compare-before-write semantics against non-cooperating writers.
func (transactions *ChannelTransactions) Initialize(
	ctx context.Context,
	value Config,
	dryRun bool,
) (ChannelSnapshot, error) {
	if transactions == nil || strings.TrimSpace(transactions.Path) == "" {
		return ChannelSnapshot{}, errors.New("config path is required")
	}
	if err := ctx.Err(); err != nil {
		return ChannelSnapshot{}, err
	}
	if err := value.Validate(); err != nil {
		return ChannelSnapshot{}, err
	}
	encoded, err := encodeConfig(value)
	if err != nil {
		return ChannelSnapshot{}, err
	}
	planned := ChannelSnapshot{
		Config:   cloneConfig(value),
		Revision: configRevision(encoded),
	}
	if dryRun {
		return planned, nil
	}
	lock, err := acquireChannelLock(
		ctx,
		transactions.Path,
		transactions.timeout(),
		transactions.poll(),
		transactions.stale(),
	)
	if err != nil {
		return ChannelSnapshot{}, err
	}
	defer lock.release()
	if _, err := os.Lstat(transactions.Path); err == nil {
		return ChannelSnapshot{}, ErrConfigChanged
	} else if !errors.Is(err, os.ErrNotExist) {
		return ChannelSnapshot{}, err
	}
	if err := saveNewConfig(transactions.Path, encoded); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ChannelSnapshot{}, ErrConfigChanged
		}
		return ChannelSnapshot{}, err
	}
	return loadChannelSnapshot(transactions.Path)
}

func (transactions *ChannelTransactions) applyToSnapshot(
	before ChannelSnapshot,
	change ChannelChange,
) (ChannelResult, error) {
	if change.ExpectedRevision != "" && change.ExpectedRevision != before.Revision {
		return ChannelResult{}, ErrConfigChanged
	}
	after := cloneConfig(before.Config)
	if err := applyChannelChange(&after, change); err != nil {
		return ChannelResult{}, err
	}
	if err := after.Validate(); err != nil {
		return ChannelResult{}, fmt.Errorf("%w: %v", ErrInvalidChannelChange, err)
	}
	encoded, err := encodeConfig(after)
	if err != nil {
		return ChannelResult{}, err
	}
	return ChannelResult{
		Before: before,
		After: ChannelSnapshot{
			Config:   after,
			Revision: configRevision(encoded),
		},
		DryRun: change.DryRun,
	}, nil
}

func (transactions *ChannelTransactions) timeout() time.Duration {
	if transactions.lockTimeout > 0 {
		return transactions.lockTimeout
	}
	return defaultChannelLockTimeout
}

func (transactions *ChannelTransactions) poll() time.Duration {
	if transactions.lockPoll > 0 {
		return transactions.lockPoll
	}
	return defaultChannelLockPoll
}

func (transactions *ChannelTransactions) stale() time.Duration {
	if transactions.lockStale > 0 {
		return transactions.lockStale
	}
	return defaultChannelLockStale
}

func applyChannelChange(value *Config, change ChannelChange) error {
	switch change.Action {
	case ChannelAdd:
		if channelIndex(value.Channels, change.Channel.ID) >= 0 {
			return ErrChannelExists
		}
		value.Channels = append(value.Channels, change.Channel)
		if change.SetDefault {
			value.DefaultChannel = change.Channel.ID
		}
	case ChannelRename:
		index := channelIndex(value.Channels, change.ChannelID)
		if index < 0 {
			return ErrChannelNotFound
		}
		name := strings.TrimSpace(change.Name)
		if name == "" {
			return ErrInvalidChannelChange
		}
		value.Channels[index].Name = name
	case ChannelRemove:
		index := channelIndex(value.Channels, change.ChannelID)
		if index < 0 {
			return ErrChannelNotFound
		}
		if len(value.Channels) == 1 {
			return ErrLastChannel
		}
		if value.DefaultChannel == change.ChannelID {
			if change.ReplacementDefault == "" {
				return ErrReplacementDefaultRequired
			}
			replacement := channelIndex(value.Channels, change.ReplacementDefault)
			if replacement < 0 || replacement == index {
				return ErrChannelNotFound
			}
			value.DefaultChannel = change.ReplacementDefault
		} else if change.ReplacementDefault != "" {
			return ErrInvalidChannelChange
		}
		value.Channels = append(value.Channels[:index], value.Channels[index+1:]...)
	case ChannelSetDefault:
		if channelIndex(value.Channels, change.ChannelID) < 0 {
			return ErrChannelNotFound
		}
		value.DefaultChannel = change.ChannelID
	default:
		return ErrUnsupportedChannelAction
	}
	return nil
}

func channelIndex(channels []Channel, id string) int {
	for index := range channels {
		if channels[index].ID == id {
			return index
		}
	}
	return -1
}

func cloneConfig(value Config) Config {
	result := value
	result.Channels = append([]Channel(nil), value.Channels...)
	result.Notifications.Events = append([]string(nil), value.Notifications.Events...)
	return result
}

func loadChannelSnapshot(path string) (ChannelSnapshot, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return ChannelSnapshot{}, ErrNotFound
	}
	if err != nil {
		return ChannelSnapshot{}, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maximumConfigSize+1))
	if err != nil {
		return ChannelSnapshot{}, err
	}
	if len(data) > maximumConfigSize {
		return ChannelSnapshot{}, errors.New("config exceeds 1 MiB")
	}
	value, err := decodeConfig(data)
	if err != nil {
		return ChannelSnapshot{}, err
	}
	return ChannelSnapshot{Config: value, Revision: configRevision(data)}, nil
}

func decodeConfig(data []byte) (Config, error) {
	if err := rejectDuplicateConfigKeys(data); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value Config
	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("parse config: trailing JSON value")
		}
		return Config{}, fmt.Errorf("parse config trailer: %w", err)
	}
	migrateLegacyConfig(&value)
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func rejectDuplicateConfigKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	return scanConfigJSONValue(decoder)
}

func scanConfigJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := token.(string)
			if !ok {
				return errors.New("object key must be a string")
			}
			if seen[name] {
				return fmt.Errorf("duplicate field %q", name)
			}
			seen[name] = true
			if err := scanConfigJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanConfigJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func encodeConfig(value Config) ([]byte, error) {
	data, err := json.MarshalIndent(&value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func saveNewConfig(path string, encoded []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".agentbell-config-new-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func configRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type channelLock struct {
	path  string
	token string
}

func channelLockPath(configPath string) string {
	return configPath + ".config.lock"
}

func acquireChannelLock(
	ctx context.Context,
	configPath string,
	timeout time.Duration,
	poll time.Duration,
	staleAfter time.Duration,
) (*channelLock, error) {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return nil, err
	}
	token, err := lockToken()
	if err != nil {
		return nil, err
	}
	path := channelLockPath(configPath)
	deadline := time.Now().Add(timeout)
	for {
		file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr == nil {
			if _, err := file.WriteString(token); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, err
			}
			if err := file.Sync(); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, err
			}
			if err := file.Close(); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return &channelLock{path: path, token: token}, nil
		}
		if !isConfigLockContention(openErr) {
			return nil, fmt.Errorf("acquire config lock: %w", openErr)
		}
		if staleAfter > 0 {
			info, statErr := os.Stat(path)
			if statErr == nil && time.Since(info.ModTime()) > staleAfter {
				if removeErr := os.Remove(path); removeErr == nil ||
					errors.Is(removeErr, os.ErrNotExist) {
					continue
				} else if !isConfigLockContention(removeErr) {
					return nil, fmt.Errorf("remove stale config lock: %w", removeErr)
				}
			} else if errors.Is(statErr, os.ErrNotExist) {
				continue
			} else if isConfigLockContention(statErr) {
				// Windows may transiently deny inspection while another
				// owner publishes or removes the lock.
			} else if statErr != nil {
				return nil, fmt.Errorf("inspect config lock: %w", statErr)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if timeout > 0 && time.Now().After(deadline) {
			return nil, ErrConfigLockTimeout
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (lock *channelLock) release() {
	if lock == nil {
		return
	}
	data, err := os.ReadFile(lock.path)
	if err != nil || string(data) != lock.token {
		return
	}
	_ = os.Remove(lock.path)
}

func lockToken() (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
