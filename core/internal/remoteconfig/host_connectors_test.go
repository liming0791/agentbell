package remoteconfig

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func validHostConnector(id string) HostConnector {
	return HostConnector{
		ID:       id,
		TeamID:   "team-main",
		OriginID: "origin-" + id,
		Runtime:  "ssh",
		Connector: Connector{
			Type: "ssh",
			SSH: &SSHConnector{
				Host:             "build.example.com",
				Port:             22,
				User:             "agentbell",
				HostExecutable:   pathRef("darwin", "/usr/bin/ssh"),
				KnownHostsFile:   pathRef("darwin", "/Users/test/.ssh/known_hosts"),
				RemoteExecutable: pathRef("linux", "/usr/local/bin/agentbell"),
			},
		},
	}
}

func TestHostConnectorRegistryRoundTripAndCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-connectors.json")
	transactions := NewHostConnectorTransactions(path)
	initial, err := transactions.Initialize(context.Background(), HostConnectors{
		Version:        Version,
		MinCoreVersion: "0.2.0",
		Connectors:     []HostConnector{},
	}, false)
	if err != nil || initial.Revision == "" {
		t.Fatalf("initial=%#v err=%v", initial, err)
	}
	added, err := transactions.Add(
		context.Background(),
		validHostConnector("primary"),
		initial.Revision,
		false,
	)
	if err != nil || len(added.Config.Connectors) != 1 {
		t.Fatalf("added=%#v err=%v", added, err)
	}
	if _, err := transactions.Add(
		context.Background(),
		validHostConnector("stale"),
		initial.Revision,
		false,
	); err == nil {
		t.Fatal("stale revision was accepted")
	}
	removed, err := transactions.Remove(
		context.Background(),
		"primary",
		added.Revision,
		true,
	)
	if err != nil || len(removed.Config.Connectors) != 0 {
		t.Fatalf("removed=%#v err=%v", removed, err)
	}
	current, err := transactions.List(context.Background())
	if err != nil || len(current.Config.Connectors) != 1 {
		t.Fatalf("dry-run changed registry: %#v err=%v", current, err)
	}
}

func TestHostConnectorRegistryRejectsRemoteOwnedFieldsAndUnknownShape(t *testing.T) {
	value := HostConnectors{
		Version:        Version,
		MinCoreVersion: "0.2.0",
		Connectors:     []HostConnector{validHostConnector("primary")},
	}
	value.Connectors[0].Connector.HTTPS = &HTTPSConnector{
		Endpoint: "https://relay.example.com/v1/events",
	}
	if err := value.Validate(); err == nil {
		t.Fatal("registry accepted HTTPS arm")
	}
	value.Connectors[0] = validHostConnector("primary")
	value.Connectors = append(value.Connectors, validHostConnector("primary"))
	if err := value.Validate(); err == nil {
		t.Fatal("registry accepted duplicate id")
	}

	path := filepath.Join(t.TempDir(), "host-connectors.json")
	raw := map[string]any{
		"version":        Version,
		"minCoreVersion": "0.2.0",
		"connectors":     []any{},
		"outbox":         map[string]any{"path": "/secret"},
	}
	encoded, _ := json.Marshal(raw)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHostConnectors(path); err == nil {
		t.Fatal("registry accepted remote-owned outbox field")
	}
}

func TestHostConnectorRegistryContextAndMissingMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-connectors.json")
	transactions := NewHostConnectorTransactions(path)
	if _, err := transactions.List(nil); err == nil {
		t.Fatal("nil context passed")
	}
	if _, err := transactions.Initialize(context.Background(), HostConnectors{}, true); err == nil {
		t.Fatal("invalid initial registry passed")
	}
	added, err := transactions.Add(
		context.Background(),
		validHostConnector("primary"),
		"",
		false,
	)
	if err != nil || len(added.Config.Connectors) != 1 {
		t.Fatalf("add did not atomically create registry: %#v err=%v", added, err)
	}
}
