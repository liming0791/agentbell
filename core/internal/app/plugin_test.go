package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/pluginverify"
	"github.com/liming0791/agentbell/core/internal/version"
)

type appPluginVerifier struct {
	evidence pluginverify.SigstoreEvidence
	err      error
	calls    int
}

func (verifier *appPluginVerifier) Verify(
	_ context.Context,
	_ pluginverify.SigstoreInput,
) (pluginverify.SigstoreEvidence, error) {
	verifier.calls++
	return verifier.evidence, verifier.err
}

func TestPluginVerifyCLIRequiresSignedReleaseIdentity(t *testing.T) {
	root, manifestDigest := writeAppPluginBundle(
		t,
		"sigstore-verified",
		"0.2.0-rc.3",
		"0.2.0-rc.3",
	)
	verifier := &appPluginVerifier{evidence: validAppPluginEvidence(manifestDigest)}
	withAppPluginVerifier(t, verifier)
	withAppCoreVersion(t, "0.2.0-rc.3")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		[]string{"plugin", "verify", root, "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("plugin verify failed: %s", stderr.String())
	}
	var report pluginverify.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Signed ||
		report.Identity == nil ||
		report.Identity.OIDCIssuer != releaseOIDCIssuer ||
		report.Identity.GitHubRepository != releaseGitHubRepository ||
		report.Identity.WorkflowIdentity != releaseWorkflowIdentity("0.2.0-rc.3") ||
		verifier.calls != 1 {
		t.Fatalf("report=%#v calls=%d", report, verifier.calls)
	}
}

func TestPluginVerifyCLIFailsClosedForTamperWrongSignerAndDowngrade(t *testing.T) {
	withAppCoreVersion(t, "0.2.0-rc.3")

	t.Run("tampered payload", func(t *testing.T) {
		root, manifestDigest := writeAppPluginBundle(
			t,
			"sigstore-verified",
			"0.2.0-rc.3",
			"0.2.0-rc.3",
		)
		if err := os.WriteFile(
			filepath.Join(root, "plugin.json"),
			[]byte("tampered"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		verifier := &appPluginVerifier{
			evidence: validAppPluginEvidence(manifestDigest),
		}
		withAppPluginVerifier(t, verifier)
		assertPluginVerifyFails(t, root, pluginverify.ErrFileIntegrity)
		if verifier.calls != 0 {
			t.Fatal("signature verifier ran before file-integrity rejection")
		}
	})

	t.Run("wrong signer", func(t *testing.T) {
		root, manifestDigest := writeAppPluginBundle(
			t,
			"sigstore-verified",
			"0.2.0-rc.3",
			"0.2.0-rc.3",
		)
		evidence := validAppPluginEvidence(manifestDigest)
		evidence.GitHubRepository = "attacker/agentbell"
		withAppPluginVerifier(t, &appPluginVerifier{evidence: evidence})
		assertPluginVerifyFails(t, root, pluginverify.ErrSignatureVerification)
	})

	t.Run("technical preview downgrade", func(t *testing.T) {
		root, _ := writeAppPluginBundle(
			t,
			"technical-preview",
			"0.2.0-rc.3",
			"0.2.0-rc.3",
		)
		if err := os.Remove(filepath.Join(root, defaultPluginSigstoreBundle)); err != nil {
			t.Fatal(err)
		}
		withAppPluginVerifier(t, &appPluginVerifier{
			err: errors.New("must not make unsigned content trusted"),
		})
		assertPluginVerifyFails(t, root, pluginverify.ErrSignatureRequired)
	})
}

func TestPluginVerifyCLIEnforcesCoreCompatibilityRange(t *testing.T) {
	root, manifestDigest := writeAppPluginBundle(
		t,
		"sigstore-verified",
		"0.3.0",
		"0.4.0",
	)
	withAppPluginVerifier(t, &appPluginVerifier{
		evidence: validAppPluginEvidence(manifestDigest),
	})

	for _, test := range []struct {
		version string
		ok      bool
	}{
		{version: "0.2.9", ok: false},
		{version: "0.3.0", ok: true},
		{version: "0.3.7", ok: true},
		{version: "0.4.0", ok: true},
		{version: "0.4.1", ok: false},
	} {
		t.Run(test.version, func(t *testing.T) {
			withAppCoreVersion(t, test.version)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := Run(
				[]string{"plugin", "verify", root, "--json"},
				strings.NewReader(""),
				&stdout,
				&stderr,
			)
			if test.ok && code != 0 {
				t.Fatalf("compatible version failed: %s", stderr.String())
			}
			if !test.ok &&
				(code == 0 || !strings.Contains(stderr.String(), pluginverify.ErrIncompatibleCore.Error())) {
				t.Fatalf("incompatible result: code=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestPluginVerifyCLIUsageAndFactoryFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(
		[]string{"plugin", "verify"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code == 0 || !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("missing bundle result: code=%d stderr=%q", code, stderr.String())
	}

	root, _ := writeAppPluginBundle(
		t,
		"sigstore-verified",
		"0.2.0-rc.3",
		"0.2.0-rc.3",
	)
	withAppCoreVersion(t, "0.2.0-rc.3")
	original := newPluginVerifier
	newPluginVerifier = func() (pluginverify.Verifier, error) {
		return nil, errors.New("trusted root unavailable")
	}
	t.Cleanup(func() { newPluginVerifier = original })
	stdout.Reset()
	stderr.Reset()
	if code := Run(
		[]string{"plugin", "verify", root},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code == 0 || !strings.Contains(stderr.String(), "trusted root unavailable") {
		t.Fatalf("factory failure: code=%d stderr=%q", code, stderr.String())
	}
}

func writeAppPluginBundle(
	t *testing.T,
	signatureStatus,
	minimum,
	maximum string,
) (string, string) {
	t.Helper()
	root := t.TempDir()
	payload := []byte(`{"name":"agentbell"}`)
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	payloadSum := sha256.Sum256(payload)
	manifest := pluginverify.Manifest{
		Version:         pluginverify.ManifestVersion,
		PluginID:        "agentbell.codex",
		PluginVersion:   "0.2.0-rc.3",
		MinCoreVersion:  minimum,
		MaxCoreVersion:  maximum,
		SignatureStatus: signatureStatus,
		Files: []pluginverify.File{{
			Path:   "plugin.json",
			SHA256: "sha256:" + hex.EncodeToString(payloadSum[:]),
			Size:   int64(len(payload)),
		}},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := os.WriteFile(
		filepath.Join(root, pluginverify.DefaultManifestPath),
		manifestBytes,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, defaultPluginSigstoreBundle),
		[]byte(`{"fixture":true}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	manifestSum := sha256.Sum256(manifestBytes)
	return root, "sha256:" + hex.EncodeToString(manifestSum[:])
}

func validAppPluginEvidence(digest string) pluginverify.SigstoreEvidence {
	return pluginverify.SigstoreEvidence{
		CertificateVerified:    true,
		ChainVerified:          true,
		RekorInclusionVerified: true,
		ArtifactDigest:         digest,
		OIDCIssuer:             releaseOIDCIssuer,
		GitHubRepository:       releaseGitHubRepository,
		WorkflowIdentity:       releaseWorkflowIdentity("0.2.0-rc.3"),
	}
}

func withAppPluginVerifier(t *testing.T, verifier pluginverify.Verifier) {
	t.Helper()
	original := newPluginVerifier
	newPluginVerifier = func() (pluginverify.Verifier, error) {
		return verifier, nil
	}
	t.Cleanup(func() { newPluginVerifier = original })
}

func withAppCoreVersion(t *testing.T, value string) {
	t.Helper()
	original := version.Version
	version.Version = value
	t.Cleanup(func() { version.Version = original })
}

func assertPluginVerifyFails(t *testing.T, root string, target error) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		[]string{"plugin", "verify", root, "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code == 0 || !strings.Contains(stderr.String(), target.Error()) {
		t.Fatalf(
			"plugin verify result: code=%d stdout=%q stderr=%q",
			code,
			stdout.String(),
			stderr.String(),
		)
	}
}
