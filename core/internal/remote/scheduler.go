package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/liming0791/agentbell/core/internal/queue"
	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

const (
	HostStatusVersion = 1

	HostStateHealthy = "healthy"
	HostStateBackoff = "backoff"
	HostStateStopped = "stopped"
	HostStateInvalid = "invalid"

	HostErrorConfigMissing         = "config_missing"
	HostErrorConfigInvalid         = "config_invalid"
	HostErrorPlatformMismatch      = "platform_mismatch"
	HostErrorExecutableUnavailable = "executable_unavailable"
	HostErrorRelayUnavailable      = "relay_unavailable"
	HostErrorPullFailed            = "pull_failed"
	HostErrorPushFailed            = "push_failed"
)

const (
	defaultHostPollInterval = 5 * time.Second
	defaultHostLockStale    = 5 * time.Minute
	defaultHostHeartbeat    = 30 * time.Second
	maxHostStatusBytes      = 64 * 1024
)

var (
	ErrInvalidHostScheduler  = errors.New("invalid remote scheduler")
	ErrHostSchedulerRunning  = errors.New("remote scheduler is already running")
	ErrHostSchedulerLockLost = errors.New(
		"remote scheduler ownership was lost",
	)
)

// HostStatus is persistent runtime proof. It deliberately contains no
// endpoint, filesystem path, host, peer identifier, event body or raw error.
type HostStatus struct {
	Version             int       `json:"version"`
	State               string    `json:"state"`
	Connector           string    `json:"connector,omitempty"`
	Runtime             string    `json:"runtime,omitempty"`
	ConfigDigest        string    `json:"configDigest,omitempty"`
	CommandDigest       string    `json:"commandDigest,omitempty"`
	CommandArgCount     int       `json:"commandArgCount"`
	Attempts            uint64    `json:"attempts"`
	Forwarded           uint64    `json:"forwarded"`
	ConsecutiveFailures int       `json:"consecutiveFailures"`
	StartedAt           time.Time `json:"startedAt,omitempty"`
	UpdatedAt           time.Time `json:"updatedAt"`
	LastAttemptAt       time.Time `json:"lastAttemptAt,omitempty"`
	LastSuccessAt       time.Time `json:"lastSuccessAt,omitempty"`
	NextAttemptAt       time.Time `json:"nextAttemptAt,omitempty"`
	ErrorCode           string    `json:"errorCode,omitempty"`
}

type HostDoctorReport struct {
	Configured      bool   `json:"configured"`
	Connector       string `json:"connector,omitempty"`
	Runtime         string `json:"runtime,omitempty"`
	ConfigDigest    string `json:"configDigest,omitempty"`
	CommandDigest   string `json:"commandDigest,omitempty"`
	CommandArgCount int    `json:"commandArgCount"`
	ExecutableReady bool   `json:"executableReady"`
	RelayReady      bool   `json:"relayReady"`
	RuntimeProof    bool   `json:"runtimeProof"`
	Running         bool   `json:"running"`
	Healthy         bool   `json:"healthy"`
	State           string `json:"state,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
}

// HostScheduler owns both outbound modes:
//   - host-pull for wsl/ssh/container, using exact argv and local ingress;
//   - HTTPS push, draining the current machine's configured durable outbox.
//
// Injected attempt functions are test seams. Production callers leave them nil.
type HostScheduler struct {
	RemoteConfigPath string
	RelayConfigPath  string
	StateDir         string
	Target           *remoteconfig.HostConnector
	Platform         string
	Runner           Runner
	Ingress          Ingress
	Now              func() time.Time
	PollInterval     time.Duration
	AttemptTimeout   time.Duration
	Backoff          []time.Duration
	Wait             func(context.Context, time.Duration) error
	CheckHostFiles   func(remoteconfig.HostConnector, string) error
	PullAttempt      func(context.Context, remoteconfig.HostConnector) (int, error)
	PushAttempt      func(context.Context, remoteconfig.RemoteConfig) (int, error)
}

func (scheduler HostScheduler) StatusPath() string {
	name := "https"
	if scheduler.Target != nil {
		name = scheduler.Target.ID
	}
	return filepath.Join(
		scheduler.StateDir,
		"remote",
		"connectors",
		name,
		"status.json",
	)
}

func (scheduler HostScheduler) LockPath() string {
	name := "https"
	if scheduler.Target != nil {
		name = scheduler.Target.ID
	}
	return filepath.Join(
		scheduler.StateDir,
		"remote",
		"connectors",
		name,
		"scheduler.lock",
	)
}

func (scheduler HostScheduler) RunOnce(
	ctx context.Context,
) (HostStatus, error) {
	if err := scheduler.validate(); err != nil {
		return HostStatus{}, err
	}
	lock, err := scheduler.acquireLock()
	if err != nil {
		return HostStatus{}, err
	}
	defer lock.release()
	previous, _ := scheduler.Status(ctx)
	return scheduler.attempt(ctx, previous)
}

func (scheduler HostScheduler) Run(ctx context.Context) error {
	if err := scheduler.validate(); err != nil {
		return err
	}
	if ctx == nil {
		return context.Canceled
	}
	lock, err := scheduler.acquireLock()
	if err != nil {
		return err
	}
	defer lock.release()
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	lockLost := make(chan error, 1)
	go func() {
		heartbeatErr := lock.heartbeat(
			runContext,
			defaultHostHeartbeat,
		)
		if heartbeatErr != nil {
			cancel()
		}
		lockLost <- heartbeatErr
	}()

	status, _ := scheduler.Status(ctx)
	for {
		status, _ = scheduler.attempt(runContext, status)
		delay := scheduler.pollInterval()
		if status.State == HostStateBackoff {
			delay = scheduler.failureDelay(status.ConsecutiveFailures)
			status.NextAttemptAt = scheduler.now().Add(delay)
			status.UpdatedAt = scheduler.now()
			_ = scheduler.writeStatus(status)
		}
		select {
		case <-ctx.Done():
			cancel()
			status.State = HostStateStopped
			status.UpdatedAt = scheduler.now()
			status.NextAttemptAt = time.Time{}
			_ = scheduler.writeStatus(status)
			<-lockLost
			return nil
		case heartbeatErr := <-lockLost:
			if heartbeatErr != nil {
				return ErrHostSchedulerLockLost
			}
			return ErrHostSchedulerLockLost
		default:
		}
		if err := scheduler.wait()(runContext, delay); err != nil {
			if ctx.Err() != nil {
				cancel()
				status.State = HostStateStopped
				status.UpdatedAt = scheduler.now()
				status.NextAttemptAt = time.Time{}
				_ = scheduler.writeStatus(status)
				<-lockLost
				return nil
			}
		}
	}
}

func (scheduler HostScheduler) attempt(
	ctx context.Context,
	previous HostStatus,
) (HostStatus, error) {
	now := scheduler.now()
	status := previous
	if status.Version == 0 {
		status.Version = HostStatusVersion
		status.StartedAt = now
	}
	status.UpdatedAt = now
	status.LastAttemptAt = now
	status.Attempts++
	status.NextAttemptAt = time.Time{}

	if scheduler.Target != nil {
		return scheduler.attemptPull(ctx, status, *scheduler.Target)
	}
	configValue, err := remoteconfig.LoadRemote(scheduler.RemoteConfigPath)
	if err != nil {
		status.State = HostStateInvalid
		status.ErrorCode = HostErrorConfigInvalid
		if errors.Is(err, remoteconfig.ErrNotFound) {
			status.ErrorCode = HostErrorConfigMissing
		}
		status.ConsecutiveFailures++
		_ = scheduler.writeStatus(status)
		return status, nil
	}
	status.Connector = configValue.Connector.Type
	status.Runtime = configValue.Runtime
	status.ConfigDigest = digestJSON(configValue)
	status.CommandDigest = ""
	status.CommandArgCount = 0

	if configValue.Connector.Type != "https" {
		return scheduler.failed(status, HostErrorConfigInvalid), nil
	}
	if configValue.Outbox.Path.Platform != scheduler.platform() {
		return scheduler.failed(status, HostErrorPlatformMismatch), nil
	}
	count, err := scheduler.pushAttempt(ctx, configValue)
	if err != nil {
		return scheduler.failed(status, HostErrorPushFailed), nil
	}
	status.State = HostStateHealthy
	status.ErrorCode = ""
	status.ConsecutiveFailures = 0
	status.LastSuccessAt = scheduler.now()
	status.UpdatedAt = status.LastSuccessAt
	status.Forwarded += uint64(count)
	if err := scheduler.writeStatus(status); err != nil {
		return status, err
	}
	return status, nil
}

func (scheduler HostScheduler) attemptPull(
	ctx context.Context,
	status HostStatus,
	target remoteconfig.HostConnector,
) (HostStatus, error) {
	if err := target.Validate(); err != nil {
		return scheduler.failed(status, HostErrorConfigInvalid), nil
	}
	status.Connector = target.Connector.Type
	status.Runtime = target.Runtime
	status.ConfigDigest = digestJSON(target)
	status.CommandDigest = ""
	status.CommandArgCount = 0
	spec, err := BuildPullCommandForConnector(
		target.Runtime,
		target.Connector,
	)
	if err != nil {
		return scheduler.failed(status, HostErrorConfigInvalid), nil
	}
	status.CommandDigest = digestCommand(spec)
	status.CommandArgCount = len(spec.Arguments)
	if checkErr := scheduler.checkHostFiles()(
		target,
		scheduler.platform(),
	); checkErr != nil {
		code := HostErrorExecutableUnavailable
		if errors.Is(checkErr, errHostPlatformMismatch) {
			code = HostErrorPlatformMismatch
		}
		return scheduler.failed(status, code), nil
	}
	count, err := scheduler.pullAttempt(ctx, target)
	if err != nil {
		return scheduler.failed(status, HostErrorPullFailed), nil
	}
	status.State = HostStateHealthy
	status.ErrorCode = ""
	status.ConsecutiveFailures = 0
	status.LastSuccessAt = scheduler.now()
	status.UpdatedAt = status.LastSuccessAt
	status.Forwarded += uint64(count)
	if err := scheduler.writeStatus(status); err != nil {
		return status, err
	}
	return status, nil
}

func (scheduler HostScheduler) failed(
	status HostStatus,
	code string,
) HostStatus {
	status.State = HostStateBackoff
	status.ErrorCode = code
	status.ConsecutiveFailures++
	status.UpdatedAt = scheduler.now()
	_ = scheduler.writeStatus(status)
	return status
}

func (scheduler HostScheduler) Status(
	ctx context.Context,
) (HostStatus, error) {
	if ctx == nil {
		return HostStatus{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return HostStatus{}, err
	}
	info, err := os.Lstat(scheduler.StatusPath())
	if err != nil {
		return HostStatus{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() <= 0 || info.Size() > maxHostStatusBytes {
		return HostStatus{}, errors.New("invalid remote scheduler status")
	}
	file, err := os.Open(scheduler.StatusPath())
	if err != nil {
		return HostStatus{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxHostStatusBytes+1))
	if err != nil || len(raw) > maxHostStatusBytes {
		return HostStatus{}, errors.New("invalid remote scheduler status")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var status HostStatus
	if err := decoder.Decode(&status); err != nil {
		return HostStatus{}, errors.New("invalid remote scheduler status")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return HostStatus{}, errors.New("invalid remote scheduler status")
	}
	if err := validateHostStatus(status); err != nil {
		return HostStatus{}, err
	}
	return status, nil
}

func (scheduler HostScheduler) Doctor(
	ctx context.Context,
) (HostDoctorReport, error) {
	if ctx == nil {
		return HostDoctorReport{}, context.Canceled
	}
	if scheduler.Target != nil {
		return scheduler.doctorTarget(ctx, *scheduler.Target), nil
	}
	report := HostDoctorReport{}
	configValue, err := remoteconfig.LoadRemote(scheduler.RemoteConfigPath)
	if err != nil {
		report.ErrorCode = HostErrorConfigInvalid
		if errors.Is(err, remoteconfig.ErrNotFound) {
			report.ErrorCode = HostErrorConfigMissing
		}
		return report, nil
	}
	report.Configured = true
	report.Connector = configValue.Connector.Type
	report.Runtime = configValue.Runtime
	report.ConfigDigest = digestJSON(configValue)
	switch configValue.Connector.Type {
	case "https":
		report.ExecutableReady =
			configValue.Outbox.Path.Platform == scheduler.platform()
		report.RelayReady = true
	}
	status, statusErr := scheduler.Status(ctx)
	if statusErr == nil {
		report.RuntimeProof = true
		report.State = status.State
		report.ErrorCode = status.ErrorCode
		report.Healthy = status.State == HostStateHealthy &&
			status.ConfigDigest == report.ConfigDigest
	}
	report.Running = scheduler.lockFresh()
	return report, nil
}

func (scheduler HostScheduler) doctorTarget(
	ctx context.Context,
	target remoteconfig.HostConnector,
) HostDoctorReport {
	report := HostDoctorReport{
		Configured:   true,
		Connector:    target.Connector.Type,
		Runtime:      target.Runtime,
		ConfigDigest: digestJSON(target),
		RelayReady:   scheduler.relayReady(),
	}
	spec, err := BuildPullCommandForConnector(
		target.Runtime,
		target.Connector,
	)
	if err == nil {
		report.CommandDigest = digestCommand(spec)
		report.CommandArgCount = len(spec.Arguments)
		report.ExecutableReady = scheduler.checkHostFiles()(
			target,
			scheduler.platform(),
		) == nil
	}
	status, statusErr := scheduler.Status(ctx)
	if statusErr == nil {
		report.RuntimeProof = true
		report.State = status.State
		report.ErrorCode = status.ErrorCode
		report.Healthy = status.State == HostStateHealthy &&
			status.ConfigDigest == report.ConfigDigest
	}
	report.Running = scheduler.lockFresh()
	return report
}

func (scheduler HostScheduler) pullAttempt(
	ctx context.Context,
	target remoteconfig.HostConnector,
) (int, error) {
	if scheduler.PullAttempt != nil {
		return scheduler.PullAttempt(ctx, target)
	}
	ingress := scheduler.Ingress
	if ingress == nil {
		value, err := scheduler.newIngress()
		if err != nil {
			return 0, ErrInvalidPuller
		}
		ingress = value
	}
	return (Puller{
		Runner:  scheduler.Runner,
		Ingress: ingress,
		Now:     scheduler.Now,
		Timeout: scheduler.AttemptTimeout,
	}).PullConnector(ctx, target)
}

func (scheduler HostScheduler) pushAttempt(
	ctx context.Context,
	configValue remoteconfig.RemoteConfig,
) (int, error) {
	if scheduler.PushAttempt != nil {
		return scheduler.PushAttempt(ctx, configValue)
	}
	outbox, err := relay.OpenOutbox(configValue.Outbox.Path.Value)
	if err != nil {
		return 0, ErrInvalidDrain
	}
	if _, err := outbox.Recover(scheduler.now()); err != nil {
		return 0, ErrInvalidDrain
	}
	transport, err := NewHTTPSTransport(configValue, HTTPSOptions{
		Now:     scheduler.Now,
		Timeout: scheduler.AttemptTimeout,
	})
	if err != nil {
		return 0, err
	}
	defer transport.Close()
	forwarder := relay.Forwarder{
		Outbox:    outbox,
		Transport: transport,
		Now:       scheduler.Now,
		Lease:     defaultDrainLease,
		Backoff:   append([]time.Duration(nil), scheduler.Backoff...),
	}
	count := 0
	for count < defaultMaxItems {
		forwarded, forwardErr := forwarder.ForwardOne(ctx)
		if forwardErr != nil {
			return count + boolCount(forwarded), forwardErr
		}
		if !forwarded {
			return count, nil
		}
		count++
	}
	return count, ErrDrainLimit
}

func (scheduler HostScheduler) newIngress() (Ingress, error) {
	configValue, err := remoteconfig.LoadRelay(scheduler.RelayConfigPath)
	if err != nil {
		return nil, err
	}
	peers := make(map[string]relay.Peer, len(configValue.Peers))
	for _, configured := range configValue.Peers {
		publicKey, decodeErr := base64.RawURLEncoding.DecodeString(
			configured.PublicKey,
		)
		if decodeErr != nil || len(publicKey) != ed25519.PublicKeySize {
			return nil, errors.New("invalid relay peer")
		}
		scopes := make([]string, 0, len(configured.Scopes))
		for _, scope := range configured.Scopes {
			if scope != "ingest" {
				return nil, errors.New("invalid relay peer")
			}
			scopes = append(scopes, relay.ScopeIngest)
		}
		peer := relay.Peer{
			ID:              configured.ID,
			TeamID:          configured.TeamID,
			OriginID:        configured.OriginID,
			PublicKey:       ed25519.PublicKey(publicKey),
			Scopes:          scopes,
			AllowedSources:  append([]string(nil), configured.AllowedSources...),
			AllowedRuntimes: append([]string(nil), configured.AllowedRuntimes...),
			Revoked:         configured.Revoked,
		}
		if err := peer.Validate(); err != nil {
			return nil, errors.New("invalid relay peer")
		}
		peers[peer.ID] = peer
	}
	queueValue, err := queue.Open(filepath.Join(scheduler.StateDir, "queue"))
	if err != nil {
		return nil, err
	}
	nonces, err := relay.OpenNonceStore(
		filepath.Join(scheduler.StateDir, "relay", "nonces"),
		relay.MinimumNonceRetention,
	)
	if err != nil {
		return nil, err
	}
	receipts, err := relay.OpenReceiptStore(filepath.Join(
		scheduler.StateDir,
		"relay",
		"receipts",
	))
	if err != nil {
		return nil, err
	}
	return relay.Ingress{
		Peer: func(keyID string) (relay.Peer, bool) {
			peer, found := peers[keyID]
			return peer, found
		},
		Nonces:   nonces,
		Receipts: receipts,
		Queue:    queueValue,
		Now:      scheduler.Now,
		MaxSkew:  relay.DefaultMaxSkew,
	}, nil
}

func (scheduler HostScheduler) relayReady() bool {
	if scheduler.Ingress != nil || scheduler.PullAttempt != nil {
		return true
	}
	_, err := remoteconfig.LoadRelay(scheduler.RelayConfigPath)
	return err == nil
}

var errHostPlatformMismatch = errors.New("host platform mismatch")

func defaultCheckHostFiles(
	target remoteconfig.HostConnector,
	platform string,
) error {
	var executable remoteconfig.PathRef
	var knownHosts *remoteconfig.PathRef
	switch target.Connector.Type {
	case "wsl":
		if platform != "windows" {
			return errHostPlatformMismatch
		}
		executable = target.Connector.WSL.HostExecutable
	case "ssh":
		executable = target.Connector.SSH.HostExecutable
		knownHosts = &target.Connector.SSH.KnownHostsFile
	case "container":
		executable = target.Connector.Container.HostExecutable
	default:
		return errors.New("not a host connector")
	}
	if executable.Platform != platform {
		return errHostPlatformMismatch
	}
	if err := checkRegularFile(executable.Value, platform != "windows"); err != nil {
		return err
	}
	if knownHosts != nil {
		if knownHosts.Platform != platform {
			return errHostPlatformMismatch
		}
		if err := checkRegularFile(knownHosts.Value, false); err != nil {
			return err
		}
	}
	return nil
}

func checkRegularFile(path string, executable bool) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("file unavailable")
	}
	if executable && info.Mode().Perm()&0o111 == 0 {
		return errors.New("file is not executable")
	}
	return nil
}

func (scheduler HostScheduler) checkHostFiles() func(
	remoteconfig.HostConnector,
	string,
) error {
	if scheduler.CheckHostFiles != nil {
		return scheduler.CheckHostFiles
	}
	return defaultCheckHostFiles
}

func (scheduler HostScheduler) validate() error {
	if scheduler.StateDir == "" ||
		(scheduler.Target == nil && scheduler.RemoteConfigPath == "") ||
		scheduler.PollInterval < 0 ||
		scheduler.AttemptTimeout < 0 {
		return ErrInvalidHostScheduler
	}
	if scheduler.Target != nil && scheduler.Target.Validate() != nil {
		return ErrInvalidHostScheduler
	}
	for _, delay := range scheduler.Backoff {
		if delay <= 0 {
			return ErrInvalidHostScheduler
		}
	}
	return nil
}

func (scheduler HostScheduler) platform() string {
	if scheduler.Platform != "" {
		return scheduler.Platform
	}
	return runtime.GOOS
}

func (scheduler HostScheduler) now() time.Time {
	if scheduler.Now != nil {
		return scheduler.Now().UTC()
	}
	return time.Now().UTC()
}

func (scheduler HostScheduler) pollInterval() time.Duration {
	if scheduler.PollInterval > 0 {
		return scheduler.PollInterval
	}
	return defaultHostPollInterval
}

func (scheduler HostScheduler) failureDelay(failures int) time.Duration {
	backoff := scheduler.Backoff
	if len(backoff) == 0 {
		backoff = defaultConnectorBackoff
	}
	index := failures - 1
	if index < 0 {
		index = 0
	}
	if index >= len(backoff) {
		index = len(backoff) - 1
	}
	return backoff[index]
}

func (scheduler HostScheduler) wait() func(context.Context, time.Duration) error {
	if scheduler.Wait != nil {
		return scheduler.Wait
	}
	return waitContext
}

func digestJSON(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func digestCommand(spec CommandSpec) string {
	hash := sha256.New()
	writeDigestPart(hash, spec.Executable)
	for _, argument := range spec.Arguments {
		writeDigestPart(hash, argument)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeDigestPart(writer io.Writer, value string) {
	_, _ = fmt.Fprintf(writer, "%d:", len(value))
	_, _ = io.WriteString(writer, value)
}

func validateHostStatus(status HostStatus) error {
	validState := status.State == HostStateHealthy ||
		status.State == HostStateBackoff ||
		status.State == HostStateStopped ||
		status.State == HostStateInvalid
	if status.Version != HostStatusVersion ||
		!validState ||
		status.UpdatedAt.IsZero() ||
		status.CommandArgCount < 0 ||
		(status.ConfigDigest != "" && len(status.ConfigDigest) != 64) ||
		(status.CommandDigest != "" && len(status.CommandDigest) != 64) {
		return errors.New("invalid remote scheduler status")
	}
	return nil
}

func (scheduler HostScheduler) writeStatus(status HostStatus) error {
	if err := validateHostStatus(status); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	directory := filepath.Dir(scheduler.StatusPath())
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".status-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
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
	if err := os.Rename(name, scheduler.StatusPath()); err != nil {
		return err
	}
	return os.Chmod(scheduler.StatusPath(), 0o600)
}

type hostSchedulerLock struct {
	path  string
	token string
}

func (scheduler HostScheduler) acquireLock() (*hostSchedulerLock, error) {
	path := scheduler.LockPath()
	if path == "" {
		return nil, ErrInvalidHostScheduler
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := randomLockToken()
		if err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			encoded, _ := json.Marshal(struct {
				Token string `json:"token"`
			}{Token: token})
			if _, writeErr := file.Write(encoded); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, writeErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, closeErr
			}
			return &hostSchedulerLock{path: path, token: token}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 ||
			time.Since(info.ModTime()) <= defaultHostLockStale {
			return nil, ErrHostSchedulerRunning
		}
		stale := path + ".stale-" + token
		if renameErr := os.Rename(path, stale); renameErr != nil {
			continue
		}
		_ = os.Remove(stale)
	}
	return nil, ErrHostSchedulerRunning
}

func randomLockToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (lock *hostSchedulerLock) heartbeat(
	ctx context.Context,
	interval time.Duration,
) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if !lock.owned() {
				return ErrHostSchedulerLockLost
			}
			now := time.Now()
			if err := os.Chtimes(lock.path, now, now); err != nil {
				return ErrHostSchedulerLockLost
			}
		}
	}
}

func (lock *hostSchedulerLock) release() error {
	if lock == nil || !lock.owned() {
		return nil
	}
	return os.Remove(lock.path)
}

func (lock *hostSchedulerLock) owned() bool {
	if lock == nil {
		return false
	}
	raw, err := os.ReadFile(lock.path)
	if err != nil || len(raw) > 1024 {
		return false
	}
	var value struct {
		Token string `json:"token"`
	}
	return json.Unmarshal(raw, &value) == nil && value.Token == lock.token
}

func (scheduler HostScheduler) lockFresh() bool {
	info, err := os.Lstat(scheduler.LockPath())
	return err == nil &&
		info.Mode().IsRegular() &&
		time.Since(info.ModTime()) <= defaultHostLockStale
}
