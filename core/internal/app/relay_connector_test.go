package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remote"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

func TestRelayConnectorAddListRemoveUsesHostRegistry(t *testing.T) {
	root := t.TempDir()
	resolved := paths.Paths{
		ConfigFile: filepath.Join(root, "config.json"),
		StateDir:   filepath.Join(root, "state"),
	}
	hostExecutable := "/usr/bin/ssh"
	if runtime.GOOS == "windows" {
		hostExecutable = `C:\Windows\System32\OpenSSH\ssh.exe`
	}
	knownHosts := filepath.Join(root, "known_hosts")
	var output bytes.Buffer
	err := runRelayConnector([]string{
		"add",
		"--id", "build-primary",
		"--team", "team-main",
		"--origin", "origin-build",
		"--runtime", "ssh",
		"--host-executable", hostExecutable,
		"--remote-executable", "/usr/local/bin/agentbell",
		"--host", "build.example.com",
		"--port", "22",
		"--user", "agentbell",
		"--known-hosts", knownHosts,
		"--json",
	}, strings.NewReader(""), resolved, &output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "build.example.com") ||
		strings.Contains(output.String(), hostExecutable) {
		t.Fatalf("add output leaked target details: %s", output.String())
	}
	var added struct {
		Revision string `json:"revision"`
		Count    int    `json:"count"`
	}
	if err := json.Unmarshal(output.Bytes(), &added); err != nil ||
		added.Revision == "" ||
		added.Count != 1 {
		t.Fatalf("add output=%s err=%v", output.String(), err)
	}

	output.Reset()
	if err := runRelayConnector(
		[]string{"list", "--json"},
		strings.NewReader(""),
		resolved,
		&output,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "build.example.com") ||
		strings.Contains(output.String(), knownHosts) {
		t.Fatalf("list output leaked target details: %s", output.String())
	}

	output.Reset()
	if err := runRelayConnector([]string{
		"remove",
		"--id", "build-primary",
		"--revision", added.Revision,
		"--json",
	}, strings.NewReader(""), resolved, &output); err != nil {
		t.Fatal(err)
	}
	var removed struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(output.Bytes(), &removed); err != nil ||
		removed.Count != 0 {
		t.Fatalf("remove output=%s err=%v", output.String(), err)
	}
}

func TestRelayConnectorAddDryRunDoesNotCreateRegistry(t *testing.T) {
	root := t.TempDir()
	resolved := paths.Paths{ConfigFile: filepath.Join(root, "config.json")}
	hostExecutable := "/usr/bin/docker"
	if runtime.GOOS == "windows" {
		hostExecutable = `C:\Program Files\Docker\docker.exe`
	}
	var output bytes.Buffer
	if err := runRelayConnector([]string{
		"add",
		"--id", "container-primary",
		"--team", "team-main",
		"--origin", "origin-container",
		"--runtime", "container",
		"--host-executable", hostExecutable,
		"--remote-executable", "/usr/local/bin/agentbell",
		"--container-runtime", "docker",
		"--container-id", "worker-01",
		"--dry-run",
		"--json",
	}, strings.NewReader(""), resolved, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := filepath.Glob(filepath.Join(root, "host-connectors.json")); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "host-connectors.json")); len(matches) != 0 {
		t.Fatal("dry-run created registry")
	}
}

func TestRelayConnectorPairUsesRegisteredExpectedIdentityAndStdin(t *testing.T) {
	root := t.TempDir()
	resolved := paths.Paths{
		ConfigFile: filepath.Join(root, "config.json"),
		StateDir:   filepath.Join(root, "state"),
	}
	registry := remoteconfig.HostConnectors{
		Version:        remoteconfig.Version,
		MinCoreVersion: "0.2.0",
		Connectors: []remoteconfig.HostConnector{{
			ID:       "build-primary",
			TeamID:   "team-main",
			OriginID: "origin-build",
			Runtime:  "ssh",
			Connector: remoteconfig.Connector{
				Type: "ssh",
				SSH: &remoteconfig.SSHConnector{
					Host: "build.example.com", Port: 22, User: "agentbell",
					HostExecutable: remoteconfig.PathRef{
						Platform: runtime.GOOS,
						Value:    map[bool]string{true: `C:\OpenSSH\ssh.exe`, false: "/usr/bin/ssh"}[runtime.GOOS == "windows"],
					},
					KnownHostsFile: remoteconfig.PathRef{
						Platform: runtime.GOOS,
						Value:    filepath.Join(root, "known_hosts"),
					},
					RemoteExecutable: remoteconfig.PathRef{
						Platform: "linux",
						Value:    "/usr/local/bin/agentbell",
					},
				},
			},
		}},
	}
	if err := remoteconfig.SaveHostConnectors(
		filepath.Join(root, "host-connectors.json"),
		&registry,
	); err != nil {
		t.Fatal(err)
	}
	oldPair := pairHostConnector
	defer func() { pairHostConnector = oldPair }()
	pairHostConnector = func(
		_ context.Context,
		target remoteconfig.HostConnector,
		code string,
		enroll relay.PairEnrollmentFunc,
	) (remote.PairDecision, error) {
		if target.TeamID != "team-main" ||
			target.OriginID != "origin-build" ||
			code != "AGBR-TEST" ||
			enroll == nil {
			t.Fatalf("target=%#v code=%q enroll=%v", target, code, enroll != nil)
		}
		return remote.PairDecision{Accepted: true}, nil
	}
	var output bytes.Buffer
	if err := runRelayConnector(
		[]string{"pair", "--id", "build-primary", "--code-stdin", "--json"},
		strings.NewReader("AGBR-TEST\n"),
		resolved,
		&output,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "AGBR-TEST") ||
		strings.Contains(output.String(), "build.example.com") {
		t.Fatalf("pair output leaked sensitive state: %s", output.String())
	}
}

func TestRelayConnectorPairRejectsUnsafeCodeBeforeStartingConnector(t *testing.T) {
	root := t.TempDir()
	resolved := paths.Paths{
		ConfigFile: filepath.Join(root, "config.json"),
		StateDir:   filepath.Join(root, "state"),
	}
	registry := remoteconfig.HostConnectors{
		Version:        remoteconfig.Version,
		MinCoreVersion: "0.2.0",
		Connectors: []remoteconfig.HostConnector{{
			ID:       "container-primary",
			TeamID:   "team-main",
			OriginID: "origin-container",
			Runtime:  "container",
			Connector: remoteconfig.Connector{
				Type: "container",
				Container: &remoteconfig.ContainerConnector{
					Runtime: "docker",
					HostExecutable: remoteconfig.PathRef{
						Platform: runtime.GOOS,
						Value:    map[bool]string{true: `C:\docker.exe`, false: "/usr/bin/docker"}[runtime.GOOS == "windows"],
					},
					ContainerID: "worker-01",
					RemoteExecutable: remoteconfig.PathRef{
						Platform: "linux",
						Value:    "/usr/local/bin/agentbell",
					},
				},
			},
		}},
	}
	if err := remoteconfig.SaveHostConnectors(
		filepath.Join(root, "host-connectors.json"),
		&registry,
	); err != nil {
		t.Fatal(err)
	}
	oldPair := pairHostConnector
	defer func() { pairHostConnector = oldPair }()
	called := false
	pairHostConnector = func(
		context.Context,
		remoteconfig.HostConnector,
		string,
		relay.PairEnrollmentFunc,
	) (remote.PairDecision, error) {
		called = true
		return remote.PairDecision{}, nil
	}
	inputs := []string{
		"AGBR-ONE\nAGBR-TWO\n",
		strings.Repeat("A", maxBindingCodeInput+1),
		"AGBR-\x00SECRET",
	}
	for _, input := range inputs {
		if err := runRelayConnector(
			[]string{
				"pair",
				"--id", "container-primary",
				"--code-stdin",
			},
			strings.NewReader(input),
			resolved,
			&bytes.Buffer{},
		); err == nil {
			t.Fatalf("unsafe pairing input was accepted: %q", input)
		}
	}
	if err := runRelayConnector(
		[]string{
			"pair",
			"--id", "container-primary",
			"--code-stdin",
			"extra",
		},
		strings.NewReader("AGBR-SAFE"),
		resolved,
		&bytes.Buffer{},
	); err == nil {
		t.Fatal("extra pair argument was accepted")
	}
	if called {
		t.Fatal("connector was started for invalid pairing input")
	}
}
