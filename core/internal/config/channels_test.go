package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChannelTransactionsListAndMutations(t *testing.T) {
	path := writeTransactionConfig(t)
	transactions := NewChannelTransactions(path)
	before, err := transactions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision == "" || before.Config.DefaultChannel != "primary" {
		t.Fatalf("snapshot = %#v", before)
	}

	added, err := transactions.Apply(context.Background(), ChannelChange{
		Action: ChannelAdd,
		Channel: Channel{
			ID:     "secondary",
			Name:   "Secondary",
			Type:   "feishu",
			ChatID: "oc_secondary",
			As:     "user",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.Before.Revision != before.Revision ||
		added.After.Revision == before.Revision ||
		len(added.After.Config.Channels) != 2 {
		t.Fatalf("add result = %#v", added)
	}

	renamed, err := transactions.Apply(context.Background(), ChannelChange{
		Action:    ChannelRename,
		ChannelID: "secondary",
		Name:      "On-call",
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.After.Config.Channels[1].Name != "On-call" {
		t.Fatalf("rename result = %#v", renamed)
	}

	defaulted, err := transactions.Apply(context.Background(), ChannelChange{
		Action:    ChannelSetDefault,
		ChannelID: "secondary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if defaulted.After.Config.DefaultChannel != "secondary" {
		t.Fatalf("default result = %#v", defaulted)
	}

	removed, err := transactions.Apply(context.Background(), ChannelChange{
		Action:    ChannelRemove,
		ChannelID: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.After.Config.Channels) != 1 ||
		removed.After.Config.DefaultChannel != "secondary" {
		t.Fatalf("remove result = %#v", removed)
	}
}

func TestChannelTransactionsNeverLeaveDanglingDefault(t *testing.T) {
	path := writeTransactionConfig(t)
	transactions := NewChannelTransactions(path)
	if _, err := transactions.Apply(context.Background(), ChannelChange{
		Action:    ChannelRemove,
		ChannelID: "primary",
	}); !errors.Is(err, ErrLastChannel) {
		t.Fatalf("last-channel error = %v", err)
	}

	if _, err := transactions.Apply(context.Background(), ChannelChange{
		Action: ChannelAdd,
		Channel: Channel{
			ID:     "secondary",
			Name:   "Secondary",
			Type:   "feishu",
			ChatID: "oc_secondary",
			As:     "bot",
		},
		SetDefault: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := transactions.Apply(context.Background(), ChannelChange{
		Action:             ChannelRemove,
		ChannelID:          "secondary",
		ReplacementDefault: "missing",
	}); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("unknown replacement error = %v", err)
	}
	if _, err := transactions.Apply(context.Background(), ChannelChange{
		Action:    ChannelRemove,
		ChannelID: "secondary",
	}); !errors.Is(err, ErrReplacementDefaultRequired) {
		t.Fatalf("missing replacement error = %v", err)
	}
	result, err := transactions.Apply(context.Background(), ChannelChange{
		Action:             ChannelRemove,
		ChannelID:          "secondary",
		ReplacementDefault: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.After.Config.DefaultChannel != "primary" ||
		len(result.After.Config.Channels) != 1 {
		t.Fatalf("replacement result = %#v", result)
	}
}

func TestChannelTransactionsDryRunDoesNotWrite(t *testing.T) {
	path := writeTransactionConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	transactions := NewChannelTransactions(path)
	result, err := transactions.Apply(context.Background(), ChannelChange{
		Action: ChannelAdd,
		Channel: Channel{
			ID:     "dry",
			Name:   "Dry run",
			Type:   "feishu",
			ChatID: "oc_dry",
			As:     "bot",
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || len(result.After.Config.Channels) != 2 ||
		result.Before.Revision == result.After.Revision {
		t.Fatalf("dry-run result = %#v", result)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || !afterInfo.ModTime().Equal(info.ModTime()) {
		t.Fatal("dry-run changed config.json")
	}
	if _, err := os.Stat(channelLockPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created a lock file: %v", err)
	}
}

func TestChannelTransactionsInitializeIsExclusiveAndDryRunnable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	value := transactionConfig()
	transactions := NewChannelTransactions(path)

	planned, err := transactions.Initialize(
		context.Background(),
		value,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Revision == "" || planned.Config.DefaultChannel != "primary" {
		t.Fatalf("initialize plan = %#v", planned)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initialize dry-run wrote config: %v", err)
	}

	created, err := transactions.Initialize(
		context.Background(),
		value,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision == "" {
		t.Fatal("initialize did not return a committed revision")
	}
	if _, err := transactions.Initialize(
		context.Background(),
		value,
		false,
	); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("repeat initialize error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultChannel != "primary" {
		t.Fatalf("initialized config = %#v", loaded)
	}
}

func TestChannelTransactionsCompareAndWrite(t *testing.T) {
	path := writeTransactionConfig(t)
	transactions := NewChannelTransactions(path)
	snapshot, err := transactions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transactions.Apply(context.Background(), ChannelChange{
		Action:           ChannelRename,
		ChannelID:        "primary",
		Name:             "First",
		ExpectedRevision: snapshot.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := transactions.Apply(context.Background(), ChannelChange{
		Action:           ChannelRename,
		ChannelID:        "primary",
		Name:             "Stale",
		ExpectedRevision: snapshot.Revision,
	}); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("stale revision error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Channels[0].Name != "First" {
		t.Fatalf("stale writer overwrote config: %#v", loaded)
	}
}

func TestChannelTransactionsDetectExternalWriteBeforeCommit(t *testing.T) {
	path := writeTransactionConfig(t)
	transactions := NewChannelTransactions(path)
	transactions.beforeCompare = func() {
		external, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		external.Channels[0].Name = "External"
		if err := Save(path, &external); err != nil {
			t.Fatal(err)
		}
	}
	_, err := transactions.Apply(context.Background(), ChannelChange{
		Action:    ChannelRename,
		ChannelID: "primary",
		Name:      "Transaction",
	})
	if !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("external-write error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Channels[0].Name != "External" {
		t.Fatalf("external write was lost: %#v", loaded)
	}
}

func TestChannelTransactionsConcurrentAddsDoNotLoseUpdates(t *testing.T) {
	path := writeTransactionConfig(t)
	transactions := NewChannelTransactions(path)
	const writers = 16
	var wait sync.WaitGroup
	wait.Add(writers)
	errorsChannel := make(chan error, writers)
	for index := range writers {
		go func(index int) {
			defer wait.Done()
			id := "channel-" + string(rune('a'+index))
			_, err := transactions.Apply(context.Background(), ChannelChange{
				Action: ChannelAdd,
				Channel: Channel{
					ID:     id,
					Name:   id,
					Type:   "feishu",
					ChatID: "oc_" + id,
					As:     "bot",
				},
			})
			errorsChannel <- err
		}(index)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Channels) != writers+1 {
		t.Fatalf("channels = %d, want %d", len(loaded.Channels), writers+1)
	}
}

func TestChannelTransactionsLockHonorsContext(t *testing.T) {
	path := writeTransactionConfig(t)
	lock, err := acquireChannelLock(
		context.Background(),
		path,
		time.Second,
		time.Millisecond,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = NewChannelTransactions(path).Apply(ctx, ChannelChange{
		Action:    ChannelRename,
		ChannelID: "primary",
		Name:      "Blocked",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait error = %v", err)
	}
}

func TestChannelTransactionsLockTimeoutAndStaleRecovery(t *testing.T) {
	path := writeTransactionConfig(t)
	lock, err := acquireChannelLock(
		context.Background(),
		path,
		time.Second,
		time.Millisecond,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	transactions := NewChannelTransactions(path)
	transactions.lockTimeout = 10 * time.Millisecond
	transactions.lockPoll = time.Millisecond
	_, err = transactions.Apply(context.Background(), ChannelChange{
		Action:    ChannelRename,
		ChannelID: "primary",
		Name:      "Blocked",
	})
	if !errors.Is(err, ErrConfigLockTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
	lock.release()

	lockPath := channelLockPath(path)
	if err := os.WriteFile(lockPath, []byte("abandoned"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	transactions.lockStale = time.Millisecond
	result, err := transactions.Apply(context.Background(), ChannelChange{
		Action:    ChannelRename,
		ChannelID: "primary",
		Name:      "Recovered",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.After.Config.Channels[0].Name != "Recovered" {
		t.Fatalf("stale recovery result = %#v", result)
	}
}

func TestChannelLockReleaseDoesNotRemoveAnotherOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	lock, err := acquireChannelLock(
		context.Background(),
		path,
		time.Second,
		time.Millisecond,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock.path, []byte("replacement-owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock.release()
	if _, err := os.Stat(lock.path); err != nil {
		t.Fatalf("another owner's lock was removed: %v", err)
	}
	if err := os.Remove(lock.path); err != nil {
		t.Fatal(err)
	}
	var nilLock *channelLock
	nilLock.release()
}

func TestChannelTransactionsReadAndDependencyErrors(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	path := writeTransactionConfig(t)
	if _, err := NewChannelTransactions(path).List(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled list error = %v", err)
	}
	if _, err := NewChannelTransactions(
		filepath.Join(t.TempDir(), "missing.json"),
	).List(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing config error = %v", err)
	}
	if _, err := (*ChannelTransactions)(nil).List(context.Background()); err == nil {
		t.Fatal("nil transaction list succeeded")
	}
	if _, err := NewChannelTransactions("").Apply(
		context.Background(),
		ChannelChange{},
	); err == nil {
		t.Fatal("empty transaction path succeeded")
	}
}

func TestChannelTransactionsPreserveStrictSchemaAndSizeLimit(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name  string
		value string
	}{
		{
			"unknown field",
			`{"defaultChannel":"primary","notifications":{},"channels":[],"unknown":true}`,
		},
		{
			"trailing JSON",
			`{"defaultChannel":"primary","notifications":{},"channels":[]} {}`,
		},
		{"oversized", strings.Repeat(" ", maximumConfigSize+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(test.value), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewChannelTransactions(path).List(context.Background()); err == nil {
				t.Fatal("invalid config was accepted")
			}
		})
	}
}

func TestChannelTransactionsValidationErrors(t *testing.T) {
	path := writeTransactionConfig(t)
	transactions := NewChannelTransactions(path)
	tests := []struct {
		name   string
		change ChannelChange
		want   error
	}{
		{"unknown action", ChannelChange{Action: "copy"}, ErrUnsupportedChannelAction},
		{
			"duplicate",
			ChannelChange{Action: ChannelAdd, Channel: Channel{
				ID: "primary", Type: "feishu", ChatID: "oc_other", As: "bot",
			}},
			ErrChannelExists,
		},
		{
			"missing rename",
			ChannelChange{Action: ChannelRename, ChannelID: "missing", Name: "Missing"},
			ErrChannelNotFound,
		},
		{
			"empty name",
			ChannelChange{Action: ChannelRename, ChannelID: "primary", Name: " "},
			ErrInvalidChannelChange,
		},
		{
			"missing default",
			ChannelChange{Action: ChannelSetDefault, ChannelID: "missing"},
			ErrChannelNotFound,
		},
		{
			"replacement on non-default",
			ChannelChange{
				Action:             ChannelRemove,
				ChannelID:          "secondary",
				ReplacementDefault: "primary",
			},
			ErrChannelNotFound,
		},
		{
			"invalid add",
			ChannelChange{Action: ChannelAdd, Channel: Channel{ID: "bad"}},
			ErrInvalidChannelChange,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := transactions.Apply(
				context.Background(),
				test.change,
			); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func writeTransactionConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	value := &Config{
		DefaultChannel: "primary",
		LarkCLIPath:    filepath.Join(t.TempDir(), "bin", "lark-cli"),
		Notifications: Notifications{
			Events:       []string{"task.completed"},
			PrivacyLevel: "metadata-only",
		},
		Channels: []Channel{{
			ID:     "primary",
			Name:   "Primary",
			Type:   "feishu",
			ChatID: "oc_primary",
			As:     "bot",
		}},
	}
	if err := Save(path, value); err != nil {
		t.Fatal(err)
	}
	return path
}

func transactionConfig() Config {
	return Config{
		DefaultChannel: "primary",
		LarkCLIPath:    filepath.Join(string(filepath.Separator), "bin", "lark-cli"),
		Notifications: Notifications{
			Events:       []string{"task.completed"},
			PrivacyLevel: "metadata-only",
		},
		Channels: []Channel{{
			ID: "primary", Name: "Primary", Type: "feishu",
			ChatID: "oc_primary", As: "bot",
		}},
	}
}
