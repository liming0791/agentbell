package remoteconfig

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOwnedSidecarLockReleaseDoesNotRemoveSuccessor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.json.lock")
	if err := os.WriteFile(path, []byte("successor"), 0o600); err != nil {
		t.Fatal(err)
	}

	ownedSidecarLockRelease(path, "previous")()

	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "successor" {
		t.Fatalf("successor lock changed to %q", value)
	}
}

func TestRelayTransactionsInitializeAddRevokeAndDryRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.json")
	transactions := NewRelayTransactions(path)
	initial := RelayConfig{
		Version:        Version,
		MinCoreVersion: "0.3.0",
		Listener:       Listener{Enabled: false},
		Peers:          []Peer{},
	}
	initialized, err := transactions.Initialize(
		context.Background(),
		initial,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if initialized.Revision == "" || len(initialized.Config.Peers) != 0 {
		t.Fatalf("initialized = %#v", initialized)
	}

	peer := validPeer("peer-one", "origin-one")
	dryRun, err := transactions.Apply(context.Background(), PeerChange{
		Action:           PeerAdd,
		Peer:             peer,
		ExpectedRevision: initialized.Revision,
		DryRun:           true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || len(dryRun.After.Config.Peers) != 1 {
		t.Fatalf("dry-run = %#v", dryRun)
	}
	snapshot, err := transactions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Config.Peers) != 0 ||
		snapshot.Revision != initialized.Revision {
		t.Fatalf("dry-run mutated relay config: %#v", snapshot)
	}

	added, err := transactions.Apply(context.Background(), PeerChange{
		Action:           PeerAdd,
		Peer:             peer,
		ExpectedRevision: snapshot.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added.After.Config.Peers) != 1 ||
		added.After.Config.Peers[0].Revoked {
		t.Fatalf("added = %#v", added)
	}
	revoked, err := transactions.Apply(context.Background(), PeerChange{
		Action:           PeerRevoke,
		PeerID:           peer.ID,
		ExpectedRevision: added.After.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked.After.Config.Peers) != 1 ||
		!revoked.After.Config.Peers[0].Revoked {
		t.Fatalf("revoked peer was deleted or remained active: %#v", revoked)
	}
}

func TestRelayTransactionsRejectConflictsAndLostUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.json")
	transactions := NewRelayTransactions(path)
	initial, err := transactions.Initialize(context.Background(), RelayConfig{
		Version:        Version,
		MinCoreVersion: "0.3.0",
		Listener:       Listener{Enabled: false},
		Peers:          []Peer{},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	first := validPeer("peer-one", "origin-one")
	added, err := transactions.Apply(context.Background(), PeerChange{
		Action:           PeerAdd,
		Peer:             first,
		ExpectedRevision: initial.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transactions.Apply(context.Background(), PeerChange{
		Action:           PeerAdd,
		Peer:             validPeer("peer-two", "origin-two"),
		ExpectedRevision: initial.Revision,
	}); !errors.Is(err, ErrRelayChanged) {
		t.Fatalf("stale revision error = %v", err)
	}
	if _, err := transactions.Apply(context.Background(), PeerChange{
		Action: PeerAdd,
		Peer:   first,
	}); !errors.Is(err, ErrPeerExists) {
		t.Fatalf("duplicate peer error = %v", err)
	}
	if _, err := transactions.Apply(context.Background(), PeerChange{
		Action: PeerRevoke,
		PeerID: "missing",
	}); !errors.Is(err, ErrPeerNotFound) {
		t.Fatalf("missing peer error = %v", err)
	}
	if _, err := transactions.Apply(context.Background(), PeerChange{
		Action: PeerRevoke,
		PeerID: first.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := transactions.Apply(context.Background(), PeerChange{
		Action: PeerRevoke,
		PeerID: first.ID,
	}); !errors.Is(err, ErrPeerRevoked) {
		t.Fatalf("repeat revoke error = %v", err)
	}
	if added.After.Revision == initial.Revision {
		t.Fatal("revision did not change after peer add")
	}
}

func TestRelayTransactionsSerializeConcurrentAdds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.json")
	transactions := NewRelayTransactions(path)
	if _, err := transactions.Initialize(context.Background(), RelayConfig{
		Version:        Version,
		MinCoreVersion: "0.3.0",
		Listener:       Listener{Enabled: false},
		Peers:          []Peer{},
	}, false); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for _, peer := range []Peer{
		validPeer("peer-one", "origin-one"),
		validPeer("peer-two", "origin-two"),
	} {
		wait.Add(1)
		go func(peer Peer) {
			defer wait.Done()
			_, err := transactions.Apply(context.Background(), PeerChange{
				Action: PeerAdd,
				Peer:   peer,
			})
			errs <- err
		}(peer)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := transactions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Config.Peers) != 2 {
		t.Fatalf("concurrent peers = %#v", snapshot.Config.Peers)
	}
}

func TestRelayPeerRemoveIsRevisionGuardedCompensation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.json")
	transactions := NewRelayTransactions(path)
	ctx := context.Background()
	initial := RelayConfig{
		Version:        Version,
		MinCoreVersion: "0.3.0",
		Listener:       Listener{Enabled: false},
		Peers:          []Peer{validPeer("peer-one", "origin-one")},
	}
	before, err := transactions.Initialize(ctx, initial, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := transactions.Apply(ctx, PeerChange{
		Action:           PeerRemove,
		PeerID:           "peer-one",
		ExpectedRevision: before.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.After.Config.Peers) != 0 {
		t.Fatalf("peers = %#v", result.After.Config.Peers)
	}
	if _, err := transactions.Apply(ctx, PeerChange{
		Action:           PeerRemove,
		PeerID:           "peer-one",
		ExpectedRevision: before.Revision,
	}); !errors.Is(err, ErrRelayChanged) {
		t.Fatalf("stale remove error = %v", err)
	}
}

func validPeer(id, origin string) Peer {
	publicKey := sha256.Sum256([]byte(id))
	return Peer{
		ID:              id,
		TeamID:          "team-main",
		OriginID:        origin,
		PublicKey:       base64.RawURLEncoding.EncodeToString(publicKey[:]),
		Scopes:          []string{"ingest"},
		AllowedSources:  []string{"codex"},
		AllowedRuntimes: []string{"ssh"},
		Revoked:         false,
	}
}
