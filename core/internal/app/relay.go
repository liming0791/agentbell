package app

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
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
	"sync"
	"syscall"
	"time"

	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/queue"
	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

const (
	defaultRelayPairingTTL = 10 * time.Minute
	relayNonceCleanupEvery = 5 * time.Minute
)

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("list value cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

type relayRuntime struct {
	ingress         relay.Ingress
	nonces          *relay.NonceStore
	receipts        *relay.ReceiptStore
	pairings        *relay.PairingStore
	transactions    *remoteconfig.RelayTransactions
	expectedTeamID  string
	expectedRuntime string
	peersMutex      sync.RWMutex
	peers           map[string]relay.Peer
	server          relay.HTTPServer
}

type relayHTTPRoutes struct {
	events  http.Handler
	pairing http.Handler
}

func (routes relayHTTPRoutes) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	if request != nil && request.URL != nil &&
		request.URL.Path == "/v1/pair" {
		routes.pairing.ServeHTTP(response, request)
		return
	}
	routes.events.ServeHTTP(response, request)
}

func runRelay(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return relayUsageError()
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	switch args[0] {
	case "run":
		return runRelayForeground(args[1:], resolved)
	case "configure":
		return runRelayConfigure(args[1:], resolved, stdout)
	case "bind":
		return runRelayBind(args[1:], resolved, stdout)
	case "peers":
		return runRelayPeers(args[1:], resolved, stdout)
	case "receipts":
		return runRelayReceipts(args[1:], resolved, stdout)
	case "connector":
		return runRelayConnector(args[1:], stdin, resolved, stdout)
	default:
		return relayUsageError()
	}
}

func relayUsageError() error {
	return errors.New(
		"usage: agentbell relay <configure|run|bind create|peers list|peers revoke|receipts list|connector add|connector list|connector remove|connector pair> ...",
	)
}

func runRelayConfigure(
	args []string,
	resolved paths.Paths,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("relay configure", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listen := flags.String("listen", "", "listener address")
	certFile := flags.String("tls-cert", "", "TLS certificate")
	keyFile := flags.String("tls-key", "", "TLS private key")
	sshTunnel := flags.Bool(
		"ssh-tunnel",
		false,
		"listener is reachable only through an explicit SSH tunnel",
	)
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(
			"usage: agentbell relay configure [--listen <host:port>] [--tls-cert <path> --tls-key <path>] [--ssh-tunnel] [--dry-run] [--json]",
		)
	}
	listener := remoteconfig.Listener{}
	if *listen != "" {
		listener.Enabled = true
		listener.Address = *listen
		listener.SSHTunnel = *sshTunnel
		if *certFile != "" || *keyFile != "" {
			listener.TLS = &remoteconfig.ListenerTLS{
				CertFile: remoteconfig.PathRef{
					Platform: runtime.GOOS,
					Value:    *certFile,
				},
				KeyFile: remoteconfig.PathRef{
					Platform: runtime.GOOS,
					Value:    *keyFile,
				},
			}
		}
	} else if *certFile != "" || *keyFile != "" || *sshTunnel {
		return errors.New(
			"--tls-cert, --tls-key and --ssh-tunnel require --listen",
		)
	}
	value := remoteconfig.RelayConfig{
		Version:        remoteconfig.Version,
		MinCoreVersion: minimumM2CoreVersion,
		Listener:       listener,
		Peers:          []remoteconfig.Peer{},
	}
	transactions := remoteconfig.NewRelayTransactions(filepath.Join(
		filepath.Dir(resolved.ConfigFile),
		"relay.json",
	))
	snapshot, err := transactions.Initialize(
		context.Background(),
		value,
		*dryRun,
	)
	if err != nil {
		return err
	}
	result := struct {
		Path     string                     `json:"path"`
		Snapshot remoteconfig.RelaySnapshot `json:"snapshot"`
		DryRun   bool                       `json:"dryRun"`
	}{
		Path: transactions.Path, Snapshot: snapshot, DryRun: *dryRun,
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	action := "created"
	if *dryRun {
		action = "would create"
	}
	fmt.Fprintf(stdout, "Relay config %s: %s\n", action, transactions.Path)
	return nil
}

func runRelayBind(
	args []string,
	resolved paths.Paths,
	stdout io.Writer,
) error {
	if len(args) == 0 || args[0] != "create" {
		return errors.New(
			"usage: agentbell relay bind create --team <id> --source <source> --runtime <runtime> [--ttl 10m] [--json]",
		)
	}
	flags := flag.NewFlagSet("relay bind create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	teamID := flags.String("team", "", "relay team id")
	ttl := flags.Duration("ttl", defaultRelayPairingTTL, "pairing lifetime")
	asJSON := flags.Bool("json", false, "print JSON")
	var sources stringListFlag
	var runtimes stringListFlag
	flags.Var(&sources, "source", "allowed event source; repeatable")
	flags.Var(&runtimes, "runtime", "allowed remote runtime; repeatable")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 ||
		strings.TrimSpace(*teamID) == "" ||
		len(sources) == 0 ||
		len(runtimes) == 0 {
		return errors.New(
			"relay bind create requires --team and at least one --source and --runtime",
		)
	}
	store, err := relay.OpenPairingStore(filepath.Join(
		resolved.StateDir,
		"relay",
		"pairings",
	))
	if err != nil {
		return err
	}
	code, record, err := store.Create(relay.PairingPolicy{
		TeamID:          strings.TrimSpace(*teamID),
		AllowedSources:  append([]string(nil), sources...),
		AllowedRuntimes: append([]string(nil), runtimes...),
	}, *ttl, time.Now().UTC())
	if err != nil {
		return err
	}
	result := struct {
		Code      string    `json:"code"`
		CodeHash  string    `json:"codeHash"`
		ExpiresAt time.Time `json:"expiresAt"`
	}{
		Code:      code,
		CodeHash:  record.CodeHash,
		ExpiresAt: record.ExpiresAt,
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(
		stdout,
		"Relay pairing code: %s\nExpires at: %s\n",
		result.Code,
		result.ExpiresAt.Format(time.RFC3339),
	)
	return nil
}

func runRelayPeers(
	args []string,
	resolved paths.Paths,
	stdout io.Writer,
) error {
	if len(args) == 0 {
		return errors.New(
			"usage: agentbell relay peers <list|revoke> ...",
		)
	}
	transactions := remoteconfig.NewRelayTransactions(
		filepath.Join(filepath.Dir(resolved.ConfigFile), "relay.json"),
	)
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("relay peers list", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		asJSON := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: agentbell relay peers list [--json]")
		}
		snapshot, err := transactions.List(context.Background())
		if err != nil {
			return err
		}
		peers := snapshot.Config.Peers
		if *asJSON {
			return writeJSON(stdout, peers)
		}
		for _, peer := range peers {
			status := "active"
			if peer.Revoked {
				status = "revoked"
			}
			fmt.Fprintf(
				stdout,
				"%s\t%s\t%s\t%s\n",
				peer.ID,
				peer.OriginID,
				peer.TeamID,
				status,
			)
		}
		return nil
	case "revoke":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			return errors.New(
				"usage: agentbell relay peers revoke <peer-id> [--dry-run] [--json]",
			)
		}
		peerID := args[1]
		flags := flag.NewFlagSet("relay peers revoke", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		dryRun := flags.Bool("dry-run", false, "show changes only")
		asJSON := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New(
				"usage: agentbell relay peers revoke <peer-id> [--dry-run] [--json]",
			)
		}
		result, err := transactions.Apply(
			context.Background(),
			remoteconfig.PeerChange{
				Action: remoteconfig.PeerRevoke,
				PeerID: peerID,
				DryRun: *dryRun,
			},
		)
		if err != nil {
			return err
		}
		if *asJSON {
			return writeJSON(stdout, result)
		}
		action := "revoked"
		if *dryRun {
			action = "would revoke"
		}
		fmt.Fprintf(stdout, "Relay peer %s %s\n", peerID, action)
		return nil
	default:
		return errors.New(
			"usage: agentbell relay peers <list|revoke> ...",
		)
	}
}

func runRelayReceipts(
	args []string,
	resolved paths.Paths,
	stdout io.Writer,
) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: agentbell relay receipts list [--json]")
	}
	flags := flag.NewFlagSet("relay receipts list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: agentbell relay receipts list [--json]")
	}
	store, err := relay.OpenReceiptStore(filepath.Join(
		resolved.StateDir,
		"relay",
		"receipts",
	))
	if err != nil {
		return err
	}
	receipts, err := store.ListCommitted()
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, receipts)
	}
	for _, receipt := range receipts {
		fmt.Fprintf(
			stdout,
			"%s\t%s\t%s\t%s\n",
			receipt.ID,
			receipt.OriginID,
			receipt.LocalQueueID,
			receipt.CommittedAt.Format(time.RFC3339),
		)
	}
	return nil
}

func runRelayForeground(args []string, resolved paths.Paths) error {
	flags := flag.NewFlagSet("relay run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	foreground := flags.Bool("foreground", false, "run in foreground")
	listen := flags.String("listen", "", "override listener address")
	certFile := flags.String("tls-cert", "", "TLS certificate")
	keyFile := flags.String("tls-key", "", "TLS private key")
	sshTunnel := flags.Bool(
		"ssh-tunnel",
		false,
		"allow plaintext only behind an explicit SSH tunnel",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*foreground {
		return errors.New(
			"usage: agentbell relay run --foreground [--listen <host:port>] [--tls-cert <path> --tls-key <path>] [--ssh-tunnel]",
		)
	}
	configPath := filepath.Join(filepath.Dir(resolved.ConfigFile), "relay.json")
	value, err := remoteconfig.LoadRelay(configPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		value.Listener.Enabled = true
		value.Listener.Address = *listen
		value.Listener.TLS = nil
		value.Listener.SSHTunnel = *sshTunnel
		if *certFile != "" || *keyFile != "" {
			value.Listener.TLS = &remoteconfig.ListenerTLS{
				CertFile: remoteconfig.PathRef{
					Platform: runtime.GOOS,
					Value:    *certFile,
				},
				KeyFile: remoteconfig.PathRef{
					Platform: runtime.GOOS,
					Value:    *keyFile,
				},
			}
		}
		if err := value.Validate(); err != nil {
			return err
		}
	} else if *certFile != "" || *keyFile != "" || *sshTunnel {
		return errors.New(
			"--tls-cert, --tls-key and --ssh-tunnel require --listen",
		)
	}
	if !value.Listener.Enabled {
		return errors.New("relay listener is disabled")
	}
	runtimeValue, err := newRelayRuntime(resolved, value)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	return runtimeValue.run(ctx)
}

func newRelayRuntime(
	resolved paths.Paths,
	value remoteconfig.RelayConfig,
) (*relayRuntime, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	if !value.Listener.Enabled {
		return nil, errors.New("relay listener is disabled")
	}
	peers := make(map[string]relay.Peer, len(value.Peers))
	for _, configured := range value.Peers {
		peer, err := configuredRelayPeer(configured)
		if err != nil {
			return nil, err
		}
		peers[peer.ID] = peer
	}
	queueValue, err := queue.Open(filepath.Join(resolved.StateDir, "queue"))
	if err != nil {
		return nil, err
	}
	nonces, err := relay.OpenNonceStore(
		filepath.Join(resolved.StateDir, "relay", "nonces"),
		relay.MinimumNonceRetention,
	)
	if err != nil {
		return nil, err
	}
	receipts, err := relay.OpenReceiptStore(filepath.Join(
		resolved.StateDir,
		"relay",
		"receipts",
	))
	if err != nil {
		return nil, err
	}
	pairings, err := relay.OpenPairingStore(filepath.Join(
		resolved.StateDir,
		"relay",
		"pairings",
	))
	if err != nil {
		return nil, err
	}
	runtimeValue := &relayRuntime{
		nonces:   nonces,
		receipts: receipts,
		pairings: pairings,
		transactions: remoteconfig.NewRelayTransactions(filepath.Join(
			filepath.Dir(resolved.ConfigFile),
			"relay.json",
		)),
		peers: peers,
	}
	ingress := relay.Ingress{
		Peer: func(keyID string) (relay.Peer, bool) {
			runtimeValue.peersMutex.RLock()
			defer runtimeValue.peersMutex.RUnlock()
			peer, found := runtimeValue.peers[keyID]
			return peer, found
		},
		Nonces:   nonces,
		Receipts: receipts,
		Queue:    queueValue,
		MaxSkew:  relay.DefaultMaxSkew,
	}
	runtimeValue.ingress = ingress
	eventHandler := relay.NewHTTPHandler(ingress)
	pairingHandler := relay.NewPairingHTTPHandler(runtimeValue.enroll)
	server := relay.HTTPServer{
		Address:   value.Listener.Address,
		SSHTunnel: value.Listener.SSHTunnel,
		Handler: relayHTTPRoutes{
			events:  eventHandler,
			pairing: pairingHandler,
		},
	}
	if value.Listener.TLS != nil {
		if value.Listener.TLS.CertFile.Platform != runtime.GOOS ||
			value.Listener.TLS.KeyFile.Platform != runtime.GOOS {
			return nil, errors.New(
				"relay TLS file platform does not match the current host",
			)
		}
		server.CertFile = value.Listener.TLS.CertFile.Value
		server.KeyFile = value.Listener.TLS.KeyFile.Value
	}
	runtimeValue.server = server
	return runtimeValue, nil
}

func configuredRelayPeer(configured remoteconfig.Peer) (relay.Peer, error) {
	publicKey, err := base64.RawURLEncoding.DecodeString(configured.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return relay.Peer{}, errors.New("relay peer public key is invalid")
	}
	scopes := make([]string, 0, len(configured.Scopes))
	for _, scope := range configured.Scopes {
		if scope != "ingest" {
			return relay.Peer{}, fmt.Errorf(
				"unsupported relay peer scope %q",
				scope,
			)
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
		return relay.Peer{}, err
	}
	return peer, nil
}

func (runtimeValue *relayRuntime) enroll(
	ctx context.Context,
	request relay.PairEnrollmentRequest,
) (relay.PairEnrollmentResult, error) {
	if runtimeValue == nil ||
		runtimeValue.pairings == nil ||
		runtimeValue.transactions == nil ||
		request.PeerID != relayPeerID(request.PublicKey) {
		return relay.PairEnrollmentResult{}, errors.New(
			"relay enrollment is unavailable",
		)
	}
	now := time.Now().UTC()
	claim, err := runtimeValue.pairings.Claim(
		request.Code,
		now,
		2*time.Minute,
	)
	if err != nil {
		return relay.PairEnrollmentResult{}, err
	}
	releaseClaim := func() {
		_ = runtimeValue.pairings.Release(claim, time.Now().UTC())
	}
	if (runtimeValue.expectedTeamID != "" &&
		claim.Record.Policy.TeamID != runtimeValue.expectedTeamID) ||
		(runtimeValue.expectedRuntime != "" &&
			!containsString(
				claim.Record.Policy.AllowedRuntimes,
				runtimeValue.expectedRuntime,
			)) {
		releaseClaim()
		return relay.PairEnrollmentResult{}, errors.New(
			"pairing policy does not match the host connector",
		)
	}
	configured := remoteconfig.Peer{
		ID:              request.PeerID,
		EnrollmentID:    claim.Record.ID,
		TeamID:          claim.Record.Policy.TeamID,
		OriginID:        request.OriginID,
		PublicKey:       base64.RawURLEncoding.EncodeToString(request.PublicKey),
		Scopes:          []string{"ingest"},
		AllowedSources:  append([]string(nil), claim.Record.Policy.AllowedSources...),
		AllowedRuntimes: append([]string(nil), claim.Record.Policy.AllowedRuntimes...),
	}
	added := false
	var addResult remoteconfig.PeerResult
	addResult, err = runtimeValue.transactions.Apply(
		ctx,
		remoteconfig.PeerChange{
			Action: remoteconfig.PeerAdd,
			Peer:   configured,
		},
	)
	if errors.Is(err, remoteconfig.ErrPeerExists) {
		snapshot, listErr := runtimeValue.transactions.List(ctx)
		if listErr != nil ||
			!containsExactEnrollmentPeer(snapshot.Config.Peers, configured) {
			releaseClaim()
			if listErr != nil {
				return relay.PairEnrollmentResult{}, listErr
			}
			return relay.PairEnrollmentResult{}, err
		}
		err = nil
	} else if err == nil {
		added = true
	}
	if err != nil {
		releaseClaim()
		return relay.PairEnrollmentResult{}, err
	}
	if err := runtimeValue.pairings.Commit(
		claim,
		request.PeerID,
		time.Now().UTC(),
	); err != nil {
		if added {
			_, _ = runtimeValue.transactions.Apply(
				context.Background(),
				remoteconfig.PeerChange{
					Action:           remoteconfig.PeerRemove,
					PeerID:           configured.ID,
					ExpectedRevision: addResult.After.Revision,
				},
			)
		}
		releaseClaim()
		return relay.PairEnrollmentResult{}, err
	}
	peer, err := configuredRelayPeer(configured)
	if err != nil {
		return relay.PairEnrollmentResult{}, err
	}
	runtimeValue.peersMutex.Lock()
	runtimeValue.peers[peer.ID] = peer
	runtimeValue.peersMutex.Unlock()
	return relay.PairEnrollmentResult{
		PeerID:          configured.ID,
		TeamID:          configured.TeamID,
		AllowedSources:  append([]string(nil), configured.AllowedSources...),
		AllowedRuntimes: append([]string(nil), configured.AllowedRuntimes...),
	}, nil
}

func containsExactEnrollmentPeer(
	peers []remoteconfig.Peer,
	expected remoteconfig.Peer,
) bool {
	for _, candidate := range peers {
		if candidate.ID != expected.ID {
			continue
		}
		return candidate.EnrollmentID == expected.EnrollmentID &&
			candidate.TeamID == expected.TeamID &&
			candidate.OriginID == expected.OriginID &&
			candidate.PublicKey == expected.PublicKey &&
			!candidate.Revoked &&
			stringSlicesEqual(candidate.Scopes, expected.Scopes) &&
			stringSlicesEqual(
				candidate.AllowedSources,
				expected.AllowedSources,
			) &&
			stringSlicesEqual(
				candidate.AllowedRuntimes,
				expected.AllowedRuntimes,
			)
	}
	return false
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (runtimeValue *relayRuntime) run(ctx context.Context) error {
	if runtimeValue == nil ||
		runtimeValue.nonces == nil ||
		runtimeValue.receipts == nil {
		return errors.New("relay runtime is incomplete")
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		ticker := time.NewTicker(relayNonceCleanupEvery)
		defer ticker.Stop()
		for {
			select {
			case <-runContext.Done():
				return
			case now := <-ticker.C:
				_, _ = runtimeValue.nonces.Cleanup(now.UTC())
			}
		}
	}()
	err := runtimeValue.server.Run(runContext)
	cancel()
	<-cleanupDone
	return err
}
