package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/liming0791/agentbell/core/internal/binding"
	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/paths"
)

const (
	defaultBindingTTL   = 10 * time.Minute
	bindingClaimLease   = 2 * time.Minute
	maxBindingCodeInput = 256
)

var (
	bindingNow       = time.Now
	newBindingRunner = func(command string) binding.Runner {
		return binding.LarkCLIRunner{Command: command}
	}
)

type bindCreateResult struct {
	Code        string    `json:"code"`
	ChannelName string    `json:"channelName"`
	Identity    string    `json:"identity"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type bindCompleteResult struct {
	ChannelID   string    `json:"channelId"`
	ChannelName string    `json:"channelName"`
	Identity    string    `json:"identity"`
	MatchedAt   time.Time `json:"matchedAt"`
	VerifiedAt  time.Time `json:"verifiedAt"`
}

func runBind(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	if len(args) == 0 {
		return errors.New("usage: agentbell bind <create|complete|status|cancel>")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	store := binding.NewStore(filepath.Join(resolved.StateDir, "bindings"))
	switch args[0] {
	case "create":
		return runBindCreate(resolved, store, args[1:], stdout)
	case "complete":
		return runBindComplete(resolved, store, args[1:], stdin, stdout)
	case "status":
		return runBindStatus(store, args[1:], stdout)
	case "cancel":
		return runBindCancel(store, args[1:], stdin, stdout)
	default:
		return fmt.Errorf("unsupported bind command %q", args[0])
	}
}

func runBindCreate(
	resolved paths.Paths,
	store *binding.Store,
	args []string,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("bind create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "channel display name")
	identity := flags.String("as", "bot", "bot or user")
	ttl := flags.Duration("ttl", defaultBindingTTL, "binding lifetime")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(
			"usage: agentbell bind create --name <channel> --as <bot|user> [--ttl 10m] [--json]",
		)
	}
	now := bindingNow().UTC()
	larkCLIPath := ""
	if loaded, loadErr := config.Load(resolved.ConfigFile); loadErr == nil {
		larkCLIPath = loaded.LarkCLIPath
	} else if !errors.Is(loadErr, config.ErrNotFound) {
		return loadErr
	}
	var code string
	var record binding.Record
	var err error
	if larkCLIPath == "" {
		code, record, err = store.Create(*name, *identity, *ttl, now)
	} else {
		code, record, err = store.Create(
			*name,
			*identity,
			*ttl,
			now,
			larkCLIPath,
		)
	}
	if err != nil {
		return err
	}
	result := bindCreateResult{
		Code:        code,
		ChannelName: record.ChannelName,
		Identity:    record.As,
		CreatedAt:   record.CreatedAt,
		ExpiresAt:   record.ExpiresAt,
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(
		stdout,
		"Send this one-time code as the entire message in the target Feishu chat:\n%s\nExpires: %s\n",
		code,
		record.ExpiresAt.Format(time.RFC3339),
	)
	return nil
}

func runBindComplete(
	resolved paths.Paths,
	store *binding.Store,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("bind complete", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	codeStdin := flags.Bool("code-stdin", false, "read binding code from stdin")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*codeStdin {
		return errors.New(
			"usage: agentbell bind complete --code-stdin [--json]",
		)
	}
	code, err := readBindingCode(stdin)
	if err != nil {
		return err
	}
	now := bindingNow().UTC()
	claim, err := store.Claim(code, now, bindingClaimLease)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = store.Release(claim)
		}
	}()
	loaded, loadErr := config.Load(resolved.ConfigFile)
	freshConfig := errors.Is(loadErr, config.ErrNotFound)
	if loadErr != nil && !freshConfig {
		return loadErr
	}
	larkCLIPath := loaded.LarkCLIPath
	if freshConfig {
		larkCLIPath = claim.Record.LarkCLIPath
	}
	if larkCLIPath == "" {
		return errors.New(
			"binding has no configured absolute lark-cli executable path",
		)
	}
	candidate, err := (binding.Discovery{
		Runner: newBindingRunner(larkCLIPath),
		Now:    bindingNow,
	}).DiscoverAndVerify(context.Background(), code, claim.Record)
	if err != nil {
		return err
	}
	channel := config.Channel{
		ID:     boundChannelID(candidate.Destination().ChatID()),
		Name:   candidate.ChannelName,
		Type:   "feishu",
		ChatID: candidate.Destination().ChatID(),
		As:     candidate.Identity,
	}
	transactions := config.NewChannelTransactions(resolved.ConfigFile)
	if freshConfig {
		_, err = transactions.Initialize(
			context.Background(),
			config.Config{
				DefaultChannel: channel.ID,
				LarkCLIPath:    larkCLIPath,
				Notifications: config.Notifications{
					Events: []string{
						event.EventTaskCompleted,
						event.EventTaskFailed,
						event.EventAgentWaiting,
						event.EventApprovalRequired,
					},
					PrivacyLevel: event.PrivacyMetadataOnly,
				},
				Channels: []config.Channel{channel},
			},
			false,
		)
		if errors.Is(err, config.ErrConfigChanged) {
			freshConfig = false
		}
	}
	if !freshConfig {
		_, err = transactions.Apply(context.Background(), config.ChannelChange{
			Action:  config.ChannelAdd,
			Channel: channel,
		})
	}
	if errors.Is(err, config.ErrChannelExists) {
		snapshot, listErr := transactions.List(context.Background())
		if listErr != nil {
			return listErr
		}
		existing, found := channelByID(snapshot.Config.Channels, channel.ID)
		if !found ||
			existing.Name != channel.Name ||
			existing.ChatID != channel.ChatID ||
			existing.As != channel.As ||
			existing.Type != channel.Type {
			return errors.New(
				"bound channel id already exists with different configuration",
			)
		}
		err = nil
	}
	if err != nil {
		return err
	}
	if err := store.Commit(claim, bindingNow().UTC()); err != nil {
		return err
	}
	committed = true
	result := bindCompleteResult{
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		Identity:    channel.As,
		MatchedAt:   candidate.MatchedAt,
		VerifiedAt:  candidate.VerifiedAt,
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(
		stdout,
		"Bound Feishu channel %s as %s (%s).\n",
		channel.Name,
		channel.As,
		channel.ID,
	)
	return nil
}

func runBindStatus(
	store *binding.Store,
	args []string,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("bind status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: agentbell bind status [--json]")
	}
	statuses, err := store.List(bindingNow().UTC())
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, statuses)
	}
	if len(statuses) == 0 {
		fmt.Fprintln(stdout, "No binding records.")
		return nil
	}
	for _, status := range statuses {
		fmt.Fprintf(
			stdout,
			"%s: %s as %s (expires %s)\n",
			status.State,
			status.ChannelName,
			status.As,
			status.ExpiresAt.Format(time.RFC3339),
		)
	}
	return nil
}

func runBindCancel(
	store *binding.Store,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("bind cancel", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	codeStdin := flags.Bool("code-stdin", false, "read binding code from stdin")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*codeStdin {
		return errors.New("usage: agentbell bind cancel --code-stdin [--json]")
	}
	code, err := readBindingCode(stdin)
	if err != nil {
		return err
	}
	cancelledAt := bindingNow().UTC()
	if err := store.Cancel(code, cancelledAt); err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, map[string]any{
			"cancelled":   true,
			"cancelledAt": cancelledAt,
		})
	}
	fmt.Fprintln(stdout, "Binding code cancelled.")
	return nil
}

func readBindingCode(reader io.Reader) (string, error) {
	value, err := io.ReadAll(io.LimitReader(reader, maxBindingCodeInput+1))
	if err != nil {
		return "", err
	}
	if len(value) > maxBindingCodeInput {
		return "", errors.New("binding code input is too large")
	}
	code := strings.TrimSpace(string(value))
	if code == "" || strings.ContainsAny(code, "\x00\r\n\t ") {
		return "", binding.ErrInvalidCode
	}
	return code, nil
}

func boundChannelID(chatID string) string {
	sum := sha256.Sum256([]byte("agentbell-bound-channel-v1\x00" + chatID))
	return "feishu-" + hex.EncodeToString(sum[:6])
}

func channelByID(
	channels []config.Channel,
	id string,
) (config.Channel, bool) {
	for _, channel := range channels {
		if channel.ID == id {
			return channel, true
		}
	}
	return config.Channel{}, false
}
