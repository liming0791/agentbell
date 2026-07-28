package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remote"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
	"github.com/liming0791/agentbell/core/internal/secretstore"
)

const (
	defaultRemoteOutboxBytes = 64 << 20
	minimumM2CoreVersion     = "0.3.0"
	maximumRemoteTestWait    = 10 * time.Minute
	remoteTestPollInterval   = 100 * time.Millisecond
)

type noCloseWriter struct {
	io.Writer
}

func (noCloseWriter) Close() error { return nil }

func runRemote(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	if len(args) == 0 {
		return errors.New(
			"usage: agentbell remote <configure|pair|test|emit|drain> ...",
		)
	}
	switch args[0] {
	case "drain":
		return runRemoteDrain(args[1:], stdin, stdout)
	case "emit":
		return runRemoteEmit(args[1:], stdin, stdout)
	case "test":
		return runRemoteTest(args[1:], stdout)
	case "configure":
		return runRemoteConfigure(args[1:], stdout)
	case "pair":
		return runRemotePair(args[1:], stdin, stdout)
	default:
		return errors.New(
			"usage: agentbell remote <configure|pair|test|emit|drain> ...",
		)
	}
}

func runRemotePair(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("remote pair", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	codeStdin := flags.Bool(
		"code-stdin",
		false,
		"read the pairing code from stdin",
	)
	useStdio := flags.Bool(
		"stdio",
		false,
		"use the bounded no-listener host pairing protocol",
	)
	endpoint := flags.String("endpoint", "", "relay /v1/pair endpoint")
	pinnedSPKI := flags.String("pinned-spki", "", "optional SPKI SHA-256")
	sshTunnel := flags.Bool(
		"ssh-tunnel",
		false,
		"allow a loopback HTTP endpoint behind an explicit SSH tunnel",
	)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *codeStdin == *useStdio {
		return errors.New(
			"usage: agentbell remote pair (--code-stdin [--endpoint <url>] [--pinned-spki <sha256>] [--ssh-tunnel] [--json] | --stdio)",
		)
	}
	if *useStdio {
		if *endpoint != "" || *pinnedSPKI != "" || *sshTunnel || *asJSON {
			return errors.New(
				"remote pair --stdio does not accept network or presentation flags",
			)
		}
		return runRemotePairStdio(stdin, stdout)
	}
	code, err := readBindingCode(stdin)
	if err != nil {
		return err
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	configValue, err := remoteconfig.LoadRemote(filepath.Join(
		filepath.Dir(resolved.ConfigFile),
		"remote.json",
	))
	if err != nil {
		return err
	}
	pairEndpoint := *endpoint
	pin := *pinnedSPKI
	if pairEndpoint == "" &&
		configValue.Connector.Type == "https" &&
		configValue.Connector.HTTPS != nil {
		pairEndpoint = strings.TrimSuffix(
			configValue.Connector.HTTPS.Endpoint,
			"/v1/events",
		) + "/v1/pair"
		if pin == "" {
			pin = configValue.Connector.HTTPS.PinnedSPKI
		}
	}
	if pairEndpoint == "" {
		return errors.New(
			"remote pair requires --endpoint for non-HTTPS connectors",
		)
	}
	keyStore, err := secretstore.New(filepath.Join(
		resolved.StateDir,
		"relay",
	))
	if err != nil {
		return err
	}
	if existing, getErr := keyStore.Get(
		context.Background(),
		configValue.PrivateKeyRef,
	); getErr == nil {
		wipeBytes(existing)
		return errors.New(
			"remote device key already exists; pairing will not overwrite it",
		)
	} else if !errors.Is(getErr, secretstore.ErrNotFound) {
		return getErr
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("generate remote Ed25519 key")
	}
	defer wipeBytes(privateKey)
	if err := keyStore.Put(
		context.Background(),
		configValue.PrivateKeyRef,
		privateKey,
	); err != nil {
		return err
	}
	cleanupKey := func() {
		_ = keyStore.Delete(
			context.Background(),
			configValue.PrivateKeyRef,
		)
	}
	client, err := remote.NewPairingClient(
		remote.PairingClientConfig{
			Endpoint:   pairEndpoint,
			PinnedSPKI: pin,
			SSHTunnel:  *sshTunnel,
		},
		remote.PairingClientOptions{},
	)
	if err != nil {
		cleanupKey()
		return err
	}
	defer client.Close()
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	result, err := client.Pair(ctx, remote.PairingRequest{
		Code:      code,
		PeerID:    relayPeerID(publicKey),
		OriginID:  configValue.OriginID,
		PublicKey: publicKey,
	})
	code = ""
	if err != nil {
		cleanupKey()
		return err
	}
	if result.TeamID != configValue.TeamID ||
		!containsString(result.AllowedRuntimes, configValue.Runtime) {
		return errors.New(
			"relay pairing succeeded but its policy does not match remote.json; the device key was retained for recovery",
		)
	}
	output := struct {
		PeerID          string   `json:"peerId"`
		TeamID          string   `json:"teamId"`
		AllowedSources  []string `json:"allowedSources"`
		AllowedRuntimes []string `json:"allowedRuntimes"`
		KeyStore        string   `json:"keyStore"`
	}{
		PeerID:          result.PeerID,
		TeamID:          result.TeamID,
		AllowedSources:  result.AllowedSources,
		AllowedRuntimes: result.AllowedRuntimes,
		KeyStore:        configValue.PrivateKeyRef.Store,
	}
	if *asJSON {
		return writeJSON(stdout, output)
	}
	fmt.Fprintf(
		stdout,
		"Remote paired as %s for team %s (%s)\n",
		output.PeerID,
		output.TeamID,
		output.KeyStore,
	)
	return nil
}

func runRemotePairStdio(
	stdin io.Reader,
	stdout io.Writer,
) error {
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	configValue, err := remoteconfig.LoadRemote(filepath.Join(
		filepath.Dir(resolved.ConfigFile),
		"remote.json",
	))
	if err != nil {
		return err
	}
	if _, err := remote.BuildPairCommand(configValue); err != nil {
		return err
	}
	keyStore, err := secretstore.New(filepath.Join(
		resolved.StateDir,
		"relay",
	))
	if err != nil {
		return err
	}
	if existing, getErr := keyStore.Get(
		context.Background(),
		configValue.PrivateKeyRef,
	); getErr == nil {
		wipeBytes(existing)
		return errors.New(
			"remote device key already exists; pairing will not overwrite it",
		)
	} else if !errors.Is(getErr, secretstore.ErrNotFound) {
		return getErr
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("generate remote Ed25519 key")
	}
	defer wipeBytes(privateKey)
	if err := keyStore.Put(
		context.Background(),
		configValue.PrivateKeyRef,
		privateKey,
	); err != nil {
		return err
	}
	keepKey := false
	defer func() {
		if !keepKey {
			_ = keyStore.Delete(
				context.Background(),
				configValue.PrivateKeyRef,
			)
		}
	}()
	peerID := relayPeerID(publicKey)
	if err := remote.WritePairHello(stdout, remote.PairHello{
		ProtocolVersion: remote.PairProtocolVersion,
		TeamID:          configValue.TeamID,
		OriginID:        configValue.OriginID,
		Runtime:         configValue.Runtime,
		PeerID:          peerID,
		PublicKey:       publicKey,
	}); err != nil {
		return err
	}
	decision, err := remote.ReadPairDecision(stdin)
	if err != nil {
		return err
	}
	if !decision.Accepted {
		return remote.ErrPairEnrollment
	}
	if decision.PeerID != peerID ||
		decision.TeamID != configValue.TeamID ||
		!containsString(
			decision.AllowedRuntimes,
			configValue.Runtime,
		) {
		return remote.ErrPairProtocol
	}
	keepKey = true
	return nil
}

func runRemoteConfigure(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("remote configure", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	teamID := flags.String("team", "", "relay team id")
	originID := flags.String("origin", "", "remote origin id")
	runtimeName := flags.String("runtime", "", "remote runtime")
	outboxPath := flags.String("outbox", "", "durable outbox path")
	maxBytes := flags.Int64(
		"max-bytes",
		defaultRemoteOutboxBytes,
		"maximum retry spool bytes",
	)
	connectorType := flags.String("connector", "", "connector type")
	endpoint := flags.String("endpoint", "", "HTTPS event endpoint")
	pinnedSPKI := flags.String("pinned-spki", "", "optional SPKI SHA-256")
	distribution := flags.String("distribution", "", "WSL distribution")
	host := flags.String("host", "", "SSH host")
	port := flags.Int("port", 22, "SSH port")
	user := flags.String("user", "", "SSH user")
	hostExecutable := flags.String(
		"host-executable",
		"",
		"absolute host connector executable",
	)
	knownHosts := flags.String("known-hosts", "", "SSH known_hosts path")
	remoteExecutable := flags.String(
		"remote-executable",
		"",
		"absolute remote AgentBell executable",
	)
	remotePlatform := flags.String(
		"remote-platform",
		"linux",
		"remote executable platform",
	)
	containerRuntime := flags.String(
		"container-runtime",
		"",
		"docker or podman",
	)
	containerID := flags.String("container-id", "", "container id")
	keyFile := flags.String("key-file", "", "private key fallback file")
	acknowledgeFile := flags.Bool(
		"acknowledge-file-fallback",
		false,
		"explicitly allow a private 0600 key file",
	)
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 ||
		*teamID == "" ||
		*originID == "" ||
		*runtimeName == "" ||
		*outboxPath == "" ||
		*connectorType == "" {
		return errors.New(
			"remote configure requires --team, --origin, --runtime, --outbox and --connector",
		)
	}
	connector, err := configuredRemoteConnector(
		*connectorType,
		remoteConnectorFlags{
			endpoint:         *endpoint,
			pinnedSPKI:       *pinnedSPKI,
			distribution:     *distribution,
			host:             *host,
			port:             *port,
			user:             *user,
			hostExecutable:   *hostExecutable,
			knownHosts:       *knownHosts,
			remoteExecutable: *remoteExecutable,
			remotePlatform:   *remotePlatform,
			containerRuntime: *containerRuntime,
			containerID:      *containerID,
			currentPlatform:  runtime.GOOS,
		},
	)
	if err != nil {
		return err
	}
	keyReference, err := configuredPrivateKeyReference(
		*originID,
		*keyFile,
		*acknowledgeFile,
	)
	if err != nil {
		return err
	}
	value := remoteconfig.RemoteConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: minimumM2CoreVersion,
		TeamID:         *teamID,
		OriginID:       *originID,
		Runtime:        *runtimeName,
		Outbox: remoteconfig.Outbox{
			Path: remoteconfig.PathRef{
				Platform: runtime.GOOS,
				Value:    *outboxPath,
			},
			MaxBytes: *maxBytes,
		},
		Connector:     connector,
		PrivateKeyRef: keyReference,
	}
	if err := value.Validate(); err != nil {
		return err
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	path := filepath.Join(filepath.Dir(resolved.ConfigFile), "remote.json")
	if err := remoteconfig.CreateRemote(
		context.Background(),
		path,
		value,
		*dryRun,
	); err != nil {
		return err
	}
	result := struct {
		Path   string                    `json:"path"`
		Config remoteconfig.RemoteConfig `json:"config"`
		DryRun bool                      `json:"dryRun"`
	}{
		Path: path, Config: value, DryRun: *dryRun,
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	action := "created"
	if *dryRun {
		action = "would create"
	}
	fmt.Fprintf(stdout, "Remote config %s: %s\n", action, path)
	if keyReference.Store == "file" {
		fmt.Fprintln(
			stdout,
			"Warning: the device key will use the acknowledged private-file fallback.",
		)
	}
	return nil
}

type remoteConnectorFlags struct {
	endpoint         string
	pinnedSPKI       string
	distribution     string
	host             string
	port             int
	user             string
	hostExecutable   string
	knownHosts       string
	remoteExecutable string
	remotePlatform   string
	containerRuntime string
	containerID      string
	currentPlatform  string
}

func configuredRemoteConnector(
	kind string,
	value remoteConnectorFlags,
) (remoteconfig.Connector, error) {
	switch kind {
	case "https":
		return remoteconfig.Connector{
			Type: "https",
			HTTPS: &remoteconfig.HTTPSConnector{
				Endpoint:   value.endpoint,
				PinnedSPKI: value.pinnedSPKI,
			},
		}, nil
	case "wsl":
		return remoteconfig.Connector{
			Type: "wsl",
			WSL: &remoteconfig.WSLConnector{
				Distribution: value.distribution,
				HostExecutable: remoteconfig.PathRef{
					Platform: "windows",
					Value:    value.hostExecutable,
				},
				RemoteExecutable: remoteconfig.PathRef{
					Platform: "linux",
					Value:    value.remoteExecutable,
				},
			},
		}, nil
	case "ssh":
		return remoteconfig.Connector{
			Type: "ssh",
			SSH: &remoteconfig.SSHConnector{
				Host: value.host,
				Port: value.port,
				User: value.user,
				HostExecutable: remoteconfig.PathRef{
					Platform: value.currentPlatform,
					Value:    value.hostExecutable,
				},
				KnownHostsFile: remoteconfig.PathRef{
					Platform: value.currentPlatform,
					Value:    value.knownHosts,
				},
				RemoteExecutable: remoteconfig.PathRef{
					Platform: value.remotePlatform,
					Value:    value.remoteExecutable,
				},
			},
		}, nil
	case "container":
		return remoteconfig.Connector{
			Type: "container",
			Container: &remoteconfig.ContainerConnector{
				Runtime: value.containerRuntime,
				HostExecutable: remoteconfig.PathRef{
					Platform: value.currentPlatform,
					Value:    value.hostExecutable,
				},
				ContainerID: value.containerID,
				RemoteExecutable: remoteconfig.PathRef{
					Platform: value.remotePlatform,
					Value:    value.remoteExecutable,
				},
			},
		}, nil
	default:
		return remoteconfig.Connector{}, fmt.Errorf(
			"unsupported remote connector %q",
			kind,
		)
	}
}

func configuredPrivateKeyReference(
	originID string,
	keyFile string,
	acknowledged bool,
) (remoteconfig.PrivateKeyRef, error) {
	if keyFile != "" {
		if !acknowledged {
			return remoteconfig.PrivateKeyRef{}, errors.New(
				"--key-file requires --acknowledge-file-fallback",
			)
		}
		return remoteconfig.PrivateKeyRef{
			Store: "file",
			Path: &remoteconfig.PathRef{
				Platform: runtime.GOOS,
				Value:    keyFile,
			},
			FileFallbackAcknowledged: true,
		}, nil
	}
	if acknowledged {
		return remoteconfig.PrivateKeyRef{}, errors.New(
			"--acknowledge-file-fallback requires --key-file",
		)
	}
	store := map[string]string{
		"darwin":  "keychain",
		"linux":   "secret-service",
		"windows": "dpapi",
	}[runtime.GOOS]
	if store == "" {
		return remoteconfig.PrivateKeyRef{}, secretstore.ErrUnavailable
	}
	return remoteconfig.PrivateKeyRef{
		Store: store,
		ID:    "agentbell/device/" + originID,
	}, nil
}

func runRemoteEmit(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("remote emit", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	adapterID := flags.String("adapter", "", "adapter id")
	surface := flags.String("surface", "", "agent surface")
	runtimeName := flags.String("runtime", "", "remote runtime")
	useStdin := flags.Bool("stdin", false, "read hook JSON from stdin")
	failOpen := flags.Bool("fail-open", false, "do not block the agent")
	if err := flags.Parse(args); err != nil {
		return failOpenError(*failOpen, err)
	}
	if flags.NArg() != 0 ||
		*adapterID == "" ||
		*surface == "" ||
		*runtimeName == "" ||
		!*useStdin {
		return failOpenError(*failOpen, errors.New(
			"remote emit requires --adapter, --surface, --runtime and --stdin",
		))
	}
	raw, err := readLimited(stdin, maxInputSize)
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	now := time.Now().UTC()
	notification, err := event.Normalize(
		*adapterID,
		*surface,
		*runtimeName,
		raw,
		now,
	)
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	shouldNotify, err := event.ShouldNotify(*adapterID, raw)
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	if !shouldNotify {
		return nil
	}
	notification.PrivacyLevel = event.PrivacyMetadataOnly
	notification.CWD = ""
	notification.Summary = ""
	result, err := enqueueRemoteNotification(notification)
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	if os.Getenv("AGENTBELL_DEBUG") == "1" {
		return writeJSON(stdout, result)
	}
	return nil
}

func runRemoteTest(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("remote test", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	adapterID := flags.String("adapter", "", "adapter id")
	surface := flags.String("surface", "", "agent surface")
	wait := flags.Duration(
		"wait",
		0,
		"wait for the durable outbox acknowledgement",
	)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 ||
		*adapterID == "" ||
		*surface == "" ||
		*wait < 0 ||
		*wait > maximumRemoteTestWait {
		return errors.New(
			"usage: agentbell remote test --adapter <id> --surface <surface> [--wait <duration>] [--json]",
		)
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	configValue, err := remoteconfig.LoadRemote(filepath.Join(
		filepath.Dir(resolved.ConfigFile),
		"remote.json",
	))
	if err != nil {
		return err
	}
	probeID, err := randomRelayNonce()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]string{
		"hook_event_name": "Notification",
		"source_id":       probeID,
	})
	if err != nil {
		return err
	}
	notification, err := event.Normalize(
		*adapterID,
		*surface,
		configValue.Runtime,
		raw,
		time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	result, err := enqueueRemoteNotification(notification)
	if err != nil {
		return err
	}
	outbox, err := relay.OpenOutbox(result.OutboxPath)
	if err != nil {
		return err
	}
	state, err := outbox.Status(result.ID)
	if err != nil {
		return err
	}
	if *wait > 0 && state != relay.OutboxHistory {
		ctx, cancel := context.WithTimeout(context.Background(), *wait)
		defer cancel()
		state, err = waitRemoteOutbox(
			ctx,
			outbox,
			result.ID,
			remoteTestPollInterval,
		)
		if err != nil {
			return fmt.Errorf(
				"remote test remained durable but was not acknowledged: %w",
				err,
			)
		}
	}
	output := struct {
		Queued       bool              `json:"queued"`
		Acknowledged bool              `json:"acknowledged"`
		OutboxID     string            `json:"outboxId"`
		State        relay.OutboxState `json:"state"`
		Duplicate    bool              `json:"duplicate"`
		Runtime      string            `json:"runtime"`
		Connector    string            `json:"connector"`
	}{
		Queued:       true,
		Acknowledged: state == relay.OutboxHistory,
		OutboxID:     result.ID,
		State:        state,
		Duplicate:    result.Duplicate,
		Runtime:      result.Runtime,
		Connector:    result.Connector,
	}
	if *asJSON {
		return writeJSON(stdout, output)
	}
	fmt.Fprintf(
		stdout,
		"Remote test %s as %s for %s/%s\n",
		output.State,
		output.OutboxID,
		output.Runtime,
		output.Connector,
	)
	return nil
}

type remoteEnqueueResult struct {
	ID         string `json:"id"`
	Duplicate  bool   `json:"duplicate"`
	Runtime    string `json:"-"`
	Connector  string `json:"-"`
	OutboxPath string `json:"-"`
}

func enqueueRemoteNotification(
	notification event.Notification,
) (remoteEnqueueResult, error) {
	notification.PrivacyLevel = event.PrivacyMetadataOnly
	notification.CWD = ""
	notification.Summary = ""
	if err := notification.Validate(); err != nil {
		return remoteEnqueueResult{}, err
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return remoteEnqueueResult{}, err
	}
	configValue, err := remoteconfig.LoadRemote(filepath.Join(
		filepath.Dir(resolved.ConfigFile),
		"remote.json",
	))
	if err != nil {
		return remoteEnqueueResult{}, err
	}
	if configValue.Runtime != notification.Runtime ||
		configValue.Outbox.Path.Platform != runtime.GOOS {
		return remoteEnqueueResult{}, errors.New(
			"remote event runtime or outbox platform does not match remote.json",
		)
	}
	deliveryKey, err := relay.DeriveDeliveryKey(
		configValue.TeamID,
		configValue.OriginID,
		notification.IdempotencyKey,
	)
	if err != nil {
		return remoteEnqueueResult{}, err
	}
	outbox, err := relay.OpenOutbox(configValue.Outbox.Path.Value)
	if err != nil {
		return remoteEnqueueResult{}, err
	}
	if id, _, found, err := outbox.LookupDelivery(
		configValue.TeamID,
		configValue.OriginID,
		deliveryKey,
	); err != nil {
		return remoteEnqueueResult{}, err
	} else if found {
		return remoteEnqueueResult{
			ID:         id,
			Duplicate:  true,
			Runtime:    configValue.Runtime,
			Connector:  configValue.Connector.Type,
			OutboxPath: configValue.Outbox.Path.Value,
		}, nil
	}
	keyStore, err := secretstore.New(filepath.Join(
		resolved.StateDir,
		"relay",
	))
	if err != nil {
		return remoteEnqueueResult{}, err
	}
	privateKey, err := keyStore.Get(
		context.Background(),
		configValue.PrivateKeyRef,
	)
	if err != nil {
		return remoteEnqueueResult{}, err
	}
	defer wipeBytes(privateKey)
	publicKey, ok := ed25519.PrivateKey(privateKey).Public().(ed25519.PublicKey)
	if !ok {
		return remoteEnqueueResult{}, errors.New(
			"remote private key is not Ed25519",
		)
	}
	now := time.Now().UTC()
	nonce, err := randomRelayNonce()
	if err != nil {
		return remoteEnqueueResult{}, err
	}
	envelope := relay.Envelope{
		ProtocolVersion: relay.ProtocolVersion,
		TeamID:          configValue.TeamID,
		Origin: relay.Origin{
			ID:      configValue.OriginID,
			Runtime: configValue.Runtime,
		},
		Delivery: relay.Delivery{
			Key:         deliveryKey,
			ProducerKey: notification.IdempotencyKey,
		},
		SentAt: now,
		Nonce:  nonce,
		Event:  notification,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return remoteEnqueueResult{}, err
	}
	signature, err := relay.Sign(
		ed25519.PrivateKey(privateKey),
		http.MethodPost,
		"/v1/events",
		now,
		nonce,
		body,
	)
	if err != nil {
		return remoteEnqueueResult{}, err
	}
	id, duplicate, err := outbox.EnqueueBounded(
		body,
		relay.SignatureMetadata{
			KeyID:     relayPeerID(publicKey),
			Method:    http.MethodPost,
			Target:    "/v1/events",
			SentAt:    now,
			Nonce:     nonce,
			Signature: signature,
		},
		now,
		configValue.Outbox.MaxBytes,
	)
	if err != nil {
		if errors.Is(err, relay.ErrOutboxConflict) {
			existingID, _, found, lookupErr := outbox.LookupDelivery(
				configValue.TeamID,
				configValue.OriginID,
				deliveryKey,
			)
			if lookupErr != nil {
				return remoteEnqueueResult{}, lookupErr
			}
			if found {
				return remoteEnqueueResult{
					ID:         existingID,
					Duplicate:  true,
					Runtime:    configValue.Runtime,
					Connector:  configValue.Connector.Type,
					OutboxPath: configValue.Outbox.Path.Value,
				}, nil
			}
		}
		return remoteEnqueueResult{}, err
	}
	return remoteEnqueueResult{
		ID:         id,
		Duplicate:  duplicate,
		Runtime:    configValue.Runtime,
		Connector:  configValue.Connector.Type,
		OutboxPath: configValue.Outbox.Path.Value,
	}, nil
}

func waitRemoteOutbox(
	ctx context.Context,
	outbox *relay.Outbox,
	id string,
	pollInterval time.Duration,
) (relay.OutboxState, error) {
	if ctx == nil {
		return "", context.Canceled
	}
	if outbox == nil || pollInterval <= 0 {
		return "", errors.New("invalid remote test wait configuration")
	}
	for {
		state, err := outbox.Status(id)
		if err != nil {
			return "", err
		}
		switch state {
		case relay.OutboxHistory:
			return state, nil
		case relay.OutboxDead:
			return state, errors.New(
				"remote test entered the durable dead-letter state",
			)
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return state, ctx.Err()
		case <-timer.C:
		}
	}
}

func runRemoteDrain(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("remote drain", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	useStdio := flags.Bool("stdio", false, "use bounded frame protocol")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*useStdio {
		return errors.New("usage: agentbell remote drain --stdio")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	value, err := remoteconfig.LoadRemote(filepath.Join(
		filepath.Dir(resolved.ConfigFile),
		"remote.json",
	))
	if err != nil {
		return err
	}
	if value.Outbox.Path.Platform != runtime.GOOS {
		return errors.New(
			"remote outbox path platform does not match the current host",
		)
	}
	outbox, err := relay.OpenOutbox(value.Outbox.Path.Value)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	_, err = remote.DrainOutbox(
		ctx,
		outbox,
		io.NopCloser(stdin),
		noCloseWriter{Writer: stdout},
		remote.DrainOptions{},
	)
	return err
}

func randomRelayNonce() (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", fmt.Errorf("generate relay nonce: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func relayPeerID(publicKey ed25519.PublicKey) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("agentbell-relay-peer-v1"))
	_, _ = digest.Write(publicKey)
	return "peer_" + hex.EncodeToString(digest.Sum(nil)[:16])
}

func wipeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
