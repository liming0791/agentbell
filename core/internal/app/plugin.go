package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/liming0791/agentbell/core/internal/pluginverify"
	"github.com/liming0791/agentbell/core/internal/version"
)

const (
	defaultPluginSigstoreBundle = "plugin.sigstore.json"
	releaseOIDCIssuer           = "https://token.actions.githubusercontent.com"
	releaseGitHubRepository     = "liming0791/agentbell"
	releaseWorkflowPath         = ".github/workflows/release.yml"
)

var newPluginVerifier = func() (pluginverify.Verifier, error) {
	return pluginverify.NewPublicGoodSigstoreGoVerifier()
}

func runPlugin(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "verify" {
		return errors.New("usage: agentbell plugin verify <bundle> [--json]")
	}
	asJSON := false
	bundleRoot := ""
	for _, argument := range args[1:] {
		switch {
		case argument == "--json":
			asJSON = true
		case strings.HasPrefix(argument, "-"):
			return fmt.Errorf("unsupported plugin verify option %q", argument)
		case bundleRoot == "":
			bundleRoot = argument
		default:
			return errors.New("usage: agentbell plugin verify <bundle> [--json]")
		}
	}
	if bundleRoot == "" {
		return errors.New("usage: agentbell plugin verify <bundle> [--json]")
	}

	currentVersion := version.Current().Version
	_, _, _, manifest, err := pluginverify.ReadManifest(bundleRoot, "")
	if err != nil {
		return fmt.Errorf("read plugin manifest: %w", err)
	}
	if manifest.SignatureStatus != pluginverify.SignatureSigned {
		return fmt.Errorf("verify plugin bundle: %w", pluginverify.ErrSignatureRequired)
	}
	verifier, err := newPluginVerifier()
	if err != nil {
		return fmt.Errorf("initialize plugin signature verifier: %w", err)
	}
	report, err := (pluginverify.Validator{Verifier: verifier}).Verify(
		context.Background(),
		pluginverify.Request{
			Root:               bundleRoot,
			SigstoreBundlePath: defaultPluginSigstoreBundle,
			Policy: pluginverify.Policy{
				CoreVersion:      currentVersion,
				RequireSigned:    true,
				OIDCIssuer:       releaseOIDCIssuer,
				GitHubRepository: releaseGitHubRepository,
				WorkflowIdentity: releaseWorkflowIdentity(manifest.PluginVersion),
			},
		},
	)
	if err != nil {
		return fmt.Errorf("verify plugin bundle: %w", err)
	}
	if asJSON {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(
		stdout,
		"Plugin %s verified: %d files, %s, signer %s via %s\n",
		report.Manifest.PluginID,
		report.FilesVerified,
		report.SignatureStatus,
		report.Identity.GitHubRepository,
		report.Identity.WorkflowIdentity,
	)
	return nil
}

func releaseWorkflowIdentity(pluginVersion string) string {
	return releaseWorkflowPath + "@refs/tags/v" + pluginVersion
}
