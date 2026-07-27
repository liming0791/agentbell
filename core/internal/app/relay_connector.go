package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"

	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remote"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

type relayConnectorSummary struct {
	ID       string `json:"id"`
	TeamID   string `json:"teamId"`
	OriginID string `json:"originId"`
	Runtime  string `json:"runtime"`
	Type     string `json:"type"`
}

var pairHostConnector = func(
	ctx context.Context,
	target remoteconfig.HostConnector,
	code string,
	enroll relay.PairEnrollmentFunc,
) (remote.PairDecision, error) {
	return (remote.StdioPairer{Enroll: enroll}).PairConnector(
		ctx,
		target,
		code,
	)
}

func runRelayConnector(
	args []string,
	stdin io.Reader,
	resolved paths.Paths,
	stdout io.Writer,
) error {
	if len(args) == 0 {
		return relayConnectorUsageError()
	}
	transactions := remoteconfig.NewHostConnectorTransactions(filepath.Join(
		filepath.Dir(resolved.ConfigFile),
		"host-connectors.json",
	))
	switch args[0] {
	case "add":
		return runRelayConnectorAdd(
			args[1:],
			transactions,
			stdout,
		)
	case "list":
		return runRelayConnectorList(
			args[1:],
			transactions,
			stdout,
		)
	case "remove":
		return runRelayConnectorRemove(
			args[1:],
			transactions,
			stdout,
		)
	case "pair":
		return runRelayConnectorPair(
			args[1:],
			stdin,
			resolved,
			transactions,
			stdout,
		)
	default:
		return relayConnectorUsageError()
	}
}

func relayConnectorUsageError() error {
	return errors.New(
		"usage: agentbell relay connector <add|list|remove|pair> ...",
	)
}

func runRelayConnectorAdd(
	args []string,
	transactions *remoteconfig.HostConnectorTransactions,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("relay connector add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "local connector id")
	teamID := flags.String("team", "", "expected team id")
	originID := flags.String("origin", "", "expected remote origin id")
	runtimeName := flags.String("runtime", "", "wsl, ssh, or container")
	hostExecutable := flags.String("host-executable", "", "absolute host executable")
	remoteExecutable := flags.String(
		"remote-executable",
		"",
		"absolute remote AgentBell executable",
	)
	distribution := flags.String("distribution", "", "WSL distribution")
	host := flags.String("host", "", "SSH host")
	port := flags.Int("port", 22, "SSH port")
	user := flags.String("user", "", "SSH user")
	knownHosts := flags.String("known-hosts", "", "absolute known_hosts file")
	containerRuntime := flags.String(
		"container-runtime",
		"",
		"docker or podman",
	)
	containerID := flags.String("container-id", "", "container id or name")
	revision := flags.String("revision", "", "expected registry revision")
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return relayConnectorUsageError()
	}
	target := remoteconfig.HostConnector{
		ID:       *id,
		TeamID:   *teamID,
		OriginID: *originID,
		Runtime:  *runtimeName,
	}
	hostPath := remoteconfig.PathRef{
		Platform: goruntime.GOOS,
		Value:    *hostExecutable,
	}
	remotePath := remoteconfig.PathRef{
		Platform: "linux",
		Value:    *remoteExecutable,
	}
	switch *runtimeName {
	case "wsl":
		target.Connector = remoteconfig.Connector{
			Type: "wsl",
			WSL: &remoteconfig.WSLConnector{
				Distribution:     *distribution,
				HostExecutable:   hostPath,
				RemoteExecutable: remotePath,
			},
		}
	case "ssh":
		target.Connector = remoteconfig.Connector{
			Type: "ssh",
			SSH: &remoteconfig.SSHConnector{
				Host:           *host,
				Port:           *port,
				User:           *user,
				HostExecutable: hostPath,
				KnownHostsFile: remoteconfig.PathRef{
					Platform: goruntime.GOOS,
					Value:    *knownHosts,
				},
				RemoteExecutable: remotePath,
			},
		}
	case "container":
		target.Connector = remoteconfig.Connector{
			Type: "container",
			Container: &remoteconfig.ContainerConnector{
				Runtime:          *containerRuntime,
				HostExecutable:   hostPath,
				ContainerID:      *containerID,
				RemoteExecutable: remotePath,
			},
		}
	default:
		return errors.New("--runtime must be wsl, ssh, or container")
	}
	snapshot, err := transactions.Add(
		context.Background(),
		target,
		*revision,
		*dryRun,
	)
	if err != nil {
		return err
	}
	result := struct {
		Connector              relayConnectorSummary `json:"connector"`
		Revision               string                `json:"revision"`
		Count                  int                   `json:"count"`
		DryRun                 bool                  `json:"dryRun"`
		ServiceRestartRequired bool                  `json:"serviceRestartRequired"`
	}{
		Connector:              summarizeRelayConnector(target),
		Revision:               snapshot.Revision,
		Count:                  len(snapshot.Config.Connectors),
		DryRun:                 *dryRun,
		ServiceRestartRequired: !*dryRun,
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(
		stdout,
		"Host connector %s (%s) %s; registry count: %s. Restart the AgentBell service to apply.\n",
		result.Connector.ID,
		result.Connector.Runtime,
		map[bool]string{true: "validated", false: "added"}[*dryRun],
		strconv.Itoa(result.Count),
	)
	return nil
}

func runRelayConnectorList(
	args []string,
	transactions *remoteconfig.HostConnectorTransactions,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("relay connector list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return relayConnectorUsageError()
	}
	snapshot, err := transactions.List(context.Background())
	if err != nil {
		return err
	}
	summaries := make(
		[]relayConnectorSummary,
		0,
		len(snapshot.Config.Connectors),
	)
	for _, target := range snapshot.Config.Connectors {
		summaries = append(summaries, summarizeRelayConnector(target))
	}
	if *asJSON {
		return writeJSON(stdout, struct {
			Connectors []relayConnectorSummary `json:"connectors"`
			Revision   string                  `json:"revision"`
		}{Connectors: summaries, Revision: snapshot.Revision})
	}
	for _, summary := range summaries {
		fmt.Fprintf(
			stdout,
			"%s\t%s\t%s\t%s\n",
			summary.ID,
			summary.Runtime,
			summary.TeamID,
			summary.OriginID,
		)
	}
	return nil
}

func runRelayConnectorRemove(
	args []string,
	transactions *remoteconfig.HostConnectorTransactions,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("relay connector remove", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "local connector id")
	revision := flags.String("revision", "", "expected registry revision")
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *id == "" {
		return relayConnectorUsageError()
	}
	snapshot, err := transactions.Remove(
		context.Background(),
		*id,
		*revision,
		*dryRun,
	)
	if err != nil {
		return err
	}
	result := struct {
		ID                     string `json:"id"`
		Revision               string `json:"revision"`
		Count                  int    `json:"count"`
		DryRun                 bool   `json:"dryRun"`
		ServiceRestartRequired bool   `json:"serviceRestartRequired"`
	}{
		ID:                     *id,
		Revision:               snapshot.Revision,
		Count:                  len(snapshot.Config.Connectors),
		DryRun:                 *dryRun,
		ServiceRestartRequired: !*dryRun,
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(
		stdout,
		"Host connector %s removed. Restart the AgentBell service to apply.\n",
		result.ID,
	)
	return nil
}

func summarizeRelayConnector(
	target remoteconfig.HostConnector,
) relayConnectorSummary {
	return relayConnectorSummary{
		ID:       target.ID,
		TeamID:   target.TeamID,
		OriginID: target.OriginID,
		Runtime:  target.Runtime,
		Type:     target.Connector.Type,
	}
}

func runRelayConnectorPair(
	args []string,
	stdin io.Reader,
	resolved paths.Paths,
	transactions *remoteconfig.HostConnectorTransactions,
	stdout io.Writer,
) error {
	flags := flag.NewFlagSet("relay connector pair", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "local connector id")
	codeStdin := flags.Bool("code-stdin", false, "read pairing code from stdin")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *id == "" || !*codeStdin || stdin == nil {
		return errors.New(
			"usage: agentbell relay connector pair --id <id> --code-stdin [--json]",
		)
	}
	snapshot, err := transactions.List(context.Background())
	if err != nil {
		return err
	}
	var target *remoteconfig.HostConnector
	for index := range snapshot.Config.Connectors {
		if snapshot.Config.Connectors[index].ID == *id {
			value := snapshot.Config.Connectors[index]
			target = &value
			break
		}
	}
	if target == nil {
		return errors.New("host connector not found")
	}
	code, err := readRelayPairingCode(stdin)
	if err != nil {
		return err
	}
	pairings, err := relay.OpenPairingStore(filepath.Join(
		resolved.StateDir,
		"relay",
		"pairings",
	))
	if err != nil {
		return err
	}
	runtimeValue := &relayRuntime{
		pairings: pairings,
		transactions: remoteconfig.NewRelayTransactions(filepath.Join(
			filepath.Dir(resolved.ConfigFile),
			"relay.json",
		)),
		expectedTeamID:  target.TeamID,
		expectedRuntime: target.Runtime,
		peers:           map[string]relay.Peer{},
	}
	decision, err := pairHostConnector(
		context.Background(),
		*target,
		code,
		runtimeValue.enroll,
	)
	code = ""
	if err != nil {
		return err
	}
	result := struct {
		Accepted  bool                  `json:"accepted"`
		Connector relayConnectorSummary `json:"connector"`
	}{
		Accepted:  decision.Accepted,
		Connector: summarizeRelayConnector(*target),
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(
		stdout,
		"Host connector %s pairing accepted\n",
		result.Connector.ID,
	)
	return nil
}

func readRelayPairingCode(reader io.Reader) (string, error) {
	if reader == nil {
		return "", errors.New("pairing code input is required")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxBindingCodeInput+1))
	if err != nil {
		return "", err
	}
	defer wipeBytes(raw)
	if len(raw) == 0 || len(raw) > maxBindingCodeInput {
		return "", errors.New("pairing code input is invalid")
	}
	code := strings.TrimSpace(string(raw))
	if code == "" || strings.ContainsAny(code, "\x00\r\n\t ") {
		return "", errors.New("pairing code input is invalid")
	}
	return code, nil
}
