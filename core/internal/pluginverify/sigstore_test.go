package pluginverify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	sigroot "github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/testing/data"
	sigverify "github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	testOIDCIssuer = "https://token.actions.githubusercontent.com"
	testRepository = "liming0791/agentbell"
	testWorkflow   = ".github/workflows/release.yml@refs/tags/v2.1.0"
)

func TestSigstoreGoVerifierUsesOfficialOfflineTrustAndExactArtifact(t *testing.T) {
	artifact := []byte("exact plugin manifest bytes\n")
	virtual, entity := signedOfficialEntity(t, artifact, true)
	verifier, err := newSigstoreGoVerifier(virtual, false)
	if err != nil {
		t.Fatal(err)
	}
	verifier.loadEntity = func([]byte) (sigverify.SignedEntity, error) {
		return entity, nil
	}
	verifier.extractIdentity = func(*certificate.Summary) (string, string, error) {
		return testRepository, testWorkflow, nil
	}

	evidence, err := verifier.Verify(context.Background(), SigstoreInput{
		Bundle:         []byte(`{"offline":"official fixture"}`),
		Artifact:       artifact,
		ArtifactDigest: digest(artifact),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.CertificateVerified ||
		!evidence.ChainVerified ||
		!evidence.RekorInclusionVerified ||
		evidence.ArtifactDigest != digest(artifact) ||
		evidence.OIDCIssuer != testOIDCIssuer ||
		evidence.GitHubRepository != testRepository ||
		evidence.WorkflowIdentity != testWorkflow {
		t.Fatalf("evidence = %#v", evidence)
	}
}

func TestSigstoreGoVerifierRejectsTamperWrongTrustAndNoInclusion(t *testing.T) {
	artifact := []byte("exact plugin manifest bytes\n")
	trusted, entity := signedOfficialEntity(t, artifact, true)
	verifier, err := newSigstoreGoVerifier(trusted, false)
	if err != nil {
		t.Fatal(err)
	}
	verifier.extractIdentity = func(*certificate.Summary) (string, string, error) {
		return testRepository, testWorkflow, nil
	}

	t.Run("artifact tamper", func(t *testing.T) {
		verifier.loadEntity = func([]byte) (sigverify.SignedEntity, error) {
			return entity, nil
		}
		tampered := []byte("tampered plugin manifest bytes\n")
		if _, err := verifier.Verify(context.Background(), SigstoreInput{
			Bundle:         []byte("{}"),
			Artifact:       tampered,
			ArtifactDigest: digest(tampered),
		}); err == nil {
			t.Fatal("tampered artifact verified")
		}
	})

	t.Run("digest disagreement", func(t *testing.T) {
		if _, err := verifier.Verify(context.Background(), SigstoreInput{
			Bundle:         []byte("{}"),
			Artifact:       artifact,
			ArtifactDigest: digest([]byte("other")),
		}); err == nil {
			t.Fatal("mismatched caller digest verified")
		}
	})

	t.Run("wrong Fulcio and Rekor trust", func(t *testing.T) {
		_, untrustedEntity := signedOfficialEntity(t, artifact, true)
		verifier.loadEntity = func([]byte) (sigverify.SignedEntity, error) {
			return untrustedEntity, nil
		}
		if _, err := verifier.Verify(context.Background(), SigstoreInput{
			Bundle:         []byte("{}"),
			Artifact:       artifact,
			ArtifactDigest: digest(artifact),
		}); err == nil {
			t.Fatal("entity from an untrusted Sigstore verified")
		}
	})

	t.Run("missing Rekor inclusion proof", func(t *testing.T) {
		_, noProof := signedOfficialEntity(t, artifact, false)
		verifier.loadEntity = func([]byte) (sigverify.SignedEntity, error) {
			return noProof, nil
		}
		if _, err := verifier.Verify(context.Background(), SigstoreInput{
			Bundle:         []byte("{}"),
			Artifact:       artifact,
			ArtifactDigest: digest(artifact),
		}); err == nil {
			t.Fatal("entity without inclusion proof verified")
		}
	})
}

func TestSigstoreGoVerifierFeedsExistingExactIdentityPins(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "plugin.json", []byte("payload"))
	manifest := validManifest(root, SignatureSigned, []string{"plugin.json"})
	manifestBytes := writeManifest(t, root, manifest)
	writePayload(t, root, "bundle.json", []byte("{}"))

	virtual, entity := signedOfficialEntity(t, manifestBytes, true)
	cryptoVerifier, err := newSigstoreGoVerifier(virtual, false)
	if err != nil {
		t.Fatal(err)
	}
	cryptoVerifier.loadEntity = func([]byte) (sigverify.SignedEntity, error) {
		return entity, nil
	}
	cryptoVerifier.extractIdentity = func(*certificate.Summary) (string, string, error) {
		return testRepository, testWorkflow, nil
	}
	base := Policy{
		CoreVersion:      "2.1.0",
		RequireSigned:    true,
		ExpectedPluginID: "agentbell.codex",
		OIDCIssuer:       testOIDCIssuer,
		GitHubRepository: testRepository,
		WorkflowIdentity: testWorkflow,
	}
	if _, err := (Validator{Verifier: cryptoVerifier}).Verify(
		context.Background(),
		Request{
			Root:               root,
			SigstoreBundlePath: "bundle.json",
			Policy:             base,
		},
	); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*Policy)
	}{
		{"issuer", func(value *Policy) { value.OIDCIssuer = "https://issuer.example" }},
		{"repository", func(value *Policy) { value.GitHubRepository = "other/repository" }},
		{"workflow", func(value *Policy) {
			value.WorkflowIdentity = ".github/workflows/other.yml@refs/heads/main"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := base
			test.mutate(&policy)
			if _, err := (Validator{Verifier: cryptoVerifier}).Verify(
				context.Background(),
				Request{
					Root:               root,
					SigstoreBundlePath: "bundle.json",
					Policy:             policy,
				},
			); !errors.Is(err, ErrSignatureVerification) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSigstoreGoVerifierLoadsOfficialBundleAndTrustedRootFormats(t *testing.T) {
	officialRoot := data.TrustedRoot(t, "public-good.json")
	rootJSON, err := officialRoot.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSigstoreGoVerifier(rootJSON); err != nil {
		t.Fatalf("official trusted root rejected: %v", err)
	}
	if _, err := NewSigstoreGoVerifier([]byte(`{"not":"a trusted root"}`)); err == nil {
		t.Fatal("invalid trusted root accepted")
	}

	officialBundle := data.Bundle(t, "sigstore.js@2.0.0-provenance.sigstore.json")
	bundleJSON, err := officialBundle.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	entity, err := loadSigstoreEntity(bundleJSON)
	if err != nil {
		t.Fatalf("official bundle rejected: %v", err)
	}
	content, err := entity.VerificationContent()
	if err != nil {
		t.Fatal(err)
	}
	summary, err := certificate.SummarizeCertificate(content.Certificate())
	if err != nil {
		t.Fatal(err)
	}
	repository, workflow, err := extractGitHubIdentity(&summary)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Issuer != testOIDCIssuer ||
		repository != "sigstore/sigstore-js" ||
		workflow != ".github/workflows/release.yml@refs/heads/main" {
		t.Fatalf("issuer=%q repository=%q workflow=%q", summary.Issuer, repository, workflow)
	}
}

func TestPublicGoodVerifierUsesOfficialTrustedMaterialFetcher(t *testing.T) {
	original := fetchPublicGoodTrustedMaterial
	t.Cleanup(func() { fetchPublicGoodTrustedMaterial = original })
	fetchPublicGoodTrustedMaterial = func() (sigroot.TrustedMaterial, error) {
		return data.TrustedRoot(t, "public-good.json"), nil
	}
	if _, err := NewPublicGoodSigstoreGoVerifier(); err != nil {
		t.Fatalf("official offline trusted material rejected: %v", err)
	}

	fetchPublicGoodTrustedMaterial = func() (sigroot.TrustedMaterial, error) {
		return nil, errors.New("offline")
	}
	if _, err := NewPublicGoodSigstoreGoVerifier(); err == nil ||
		!strings.Contains(err.Error(), "through TUF") {
		t.Fatalf("fetch failure = %v", err)
	}
}

func TestSigstoreGoVerifierRejectsMalformedInputAndCancellation(t *testing.T) {
	virtual, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newSigstoreGoVerifier(virtual, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), SigstoreInput{
		Bundle:         []byte(`{"invalid":true}`),
		Artifact:       []byte("artifact"),
		ArtifactDigest: digest([]byte("artifact")),
	}); err == nil {
		t.Fatal("malformed bundle verified")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := verifier.Verify(ctx, SigstoreInput{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func signedOfficialEntity(
	t *testing.T,
	artifact []byte,
	inclusionProof bool,
) (*ca.VirtualSigstore, sigverify.SignedEntity) {
	t.Helper()
	virtual, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifact)
	statement, err := json.Marshal(map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []any{map[string]any{
			"name": "plugin-manifest.json",
			"digest": map[string]string{
				"sha256": hex.EncodeToString(sum[:]),
			},
		}},
		"predicateType": "https://agentbell.dev/plugin-manifest/v1",
		"predicate":     map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	entity, err := virtual.AttestAtTime(
		"signer@example.com",
		testOIDCIssuer,
		statement,
		time.Now().Add(5*time.Minute),
		inclusionProof,
	)
	if err != nil {
		t.Fatal(err)
	}
	return virtual, entity
}
