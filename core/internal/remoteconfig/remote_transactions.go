package remoteconfig

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

var ErrRemoteExists = errors.New("remote config already exists")

// CreateRemote validates and creates remote.json under the same cross-process
// lock discipline used for relay peer transactions. It never overwrites an
// existing sidecar.
func CreateRemote(
	ctx context.Context,
	path string,
	value RemoteConfig,
	dryRun bool,
) error {
	if ctx == nil {
		return context.Canceled
	}
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return errors.New("absolute remote config path is required")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	transactions := NewRelayTransactions(path)
	release, err := transactions.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if _, err := LoadRemote(path); err == nil {
		return ErrRemoteExists
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return SaveRemote(path, &value)
}
