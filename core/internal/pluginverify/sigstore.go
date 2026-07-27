package pluginverify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	sigbundle "github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	sigroot "github.com/sigstore/sigstore-go/pkg/root"
	sigverify "github.com/sigstore/sigstore-go/pkg/verify"
)

// SigstoreGoVerifier is the production cryptographic boundary for signed
// plugins. The official verifier validates the signature, Fulcio chain,
// observer time, SCT, and Rekor inclusion proof before this type emits
// evidence for the domain-level identity pins.
type SigstoreGoVerifier struct {
	verifier        *sigverify.Verifier
	loadEntity      func([]byte) (sigverify.SignedEntity, error)
	extractIdentity func(*certificate.Summary) (string, string, error)
}

var fetchPublicGoodTrustedMaterial = func() (sigroot.TrustedMaterial, error) {
	return sigroot.FetchTrustedRoot()
}

// NewSigstoreGoVerifier constructs a verifier from a Sigstore trusted-root
// document. Production verification requires all of the offline guarantees
// recommended for public Sigstore bundles.
func NewSigstoreGoVerifier(trustedRootJSON []byte) (*SigstoreGoVerifier, error) {
	if len(trustedRootJSON) == 0 {
		return nil, errors.New("sigstore trusted root is empty")
	}
	trusted, err := sigroot.NewTrustedRootFromJSON(trustedRootJSON)
	if err != nil {
		return nil, fmt.Errorf("load sigstore trusted root: %w", err)
	}
	return newSigstoreGoVerifier(trusted, true)
}

// NewPublicGoodSigstoreGoVerifier obtains Sigstore's public-good trusted
// material through the official TUF client, then configures strict offline
// bundle verification. Verification itself does not call Fulcio or Rekor.
func NewPublicGoodSigstoreGoVerifier() (*SigstoreGoVerifier, error) {
	trusted, err := fetchPublicGoodTrustedMaterial()
	if err != nil {
		return nil, fmt.Errorf("fetch Sigstore trusted root through TUF: %w", err)
	}
	return newSigstoreGoVerifier(trusted, true)
}

func newSigstoreGoVerifier(
	trusted sigroot.TrustedMaterial,
	requireSCT bool,
) (*SigstoreGoVerifier, error) {
	if trusted == nil {
		return nil, errors.New("sigstore trusted material is nil")
	}
	options := []sigverify.VerifierOption{
		sigverify.WithTransparencyLog(1),
		sigverify.WithObserverTimestamps(1),
	}
	if requireSCT {
		options = append(options, sigverify.WithSignedCertificateTimestamps(1))
	}
	official, err := sigverify.NewVerifier(trusted, options...)
	if err != nil {
		return nil, fmt.Errorf("configure sigstore verifier: %w", err)
	}
	return &SigstoreGoVerifier{
		verifier:        official,
		loadEntity:      loadSigstoreEntity,
		extractIdentity: extractGitHubIdentity,
	}, nil
}

// Verify validates exact artifact bytes. It intentionally does not accept a
// digest-only policy: the caller-provided digest is checked independently, and
// the official verifier hashes the supplied artifact itself.
func (verifier *SigstoreGoVerifier) Verify(
	ctx context.Context,
	input SigstoreInput,
) (SigstoreEvidence, error) {
	if err := ctx.Err(); err != nil {
		return SigstoreEvidence{}, err
	}
	if verifier == nil ||
		verifier.verifier == nil ||
		verifier.loadEntity == nil ||
		verifier.extractIdentity == nil {
		return SigstoreEvidence{}, errors.New("sigstore verifier is not configured")
	}
	artifactDigest := digest(input.Artifact)
	if !validDigest(input.ArtifactDigest) || input.ArtifactDigest != artifactDigest {
		return SigstoreEvidence{}, errors.New("sigstore artifact digest does not match exact bytes")
	}
	entity, err := verifier.loadEntity(input.Bundle)
	if err != nil {
		return SigstoreEvidence{}, fmt.Errorf("load sigstore bundle: %w", err)
	}
	if entity == nil || !entity.HasInclusionProof() {
		return SigstoreEvidence{}, errors.New("sigstore bundle has no Rekor inclusion proof")
	}
	content, err := entity.VerificationContent()
	if err != nil || content == nil || content.Certificate() == nil {
		return SigstoreEvidence{}, errors.New("sigstore bundle has no signing certificate")
	}
	unverifiedSummary, err := certificate.SummarizeCertificate(content.Certificate())
	if err != nil {
		return SigstoreEvidence{}, fmt.Errorf("read sigstore certificate identity: %w", err)
	}
	exactIdentity, err := sigverify.NewShortCertificateIdentity(
		unverifiedSummary.Issuer,
		"",
		unverifiedSummary.SubjectAlternativeName,
		"",
	)
	if err != nil {
		return SigstoreEvidence{}, fmt.Errorf("construct sigstore identity policy: %w", err)
	}
	result, err := verifier.verifier.Verify(
		entity,
		sigverify.NewPolicy(
			sigverify.WithArtifact(bytes.NewReader(input.Artifact)),
			sigverify.WithCertificateIdentity(exactIdentity),
		),
	)
	if err != nil {
		return SigstoreEvidence{}, fmt.Errorf("verify sigstore bundle: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return SigstoreEvidence{}, err
	}
	if result == nil ||
		result.Signature == nil ||
		result.Signature.Certificate == nil ||
		result.VerifiedIdentity == nil {
		return SigstoreEvidence{}, errors.New("sigstore verification returned incomplete certificate evidence")
	}
	rekorVerified := false
	for _, timestamp := range result.VerifiedTimestamps {
		if timestamp.Type == "Tlog" {
			rekorVerified = true
			break
		}
	}
	if !rekorVerified {
		return SigstoreEvidence{}, errors.New("sigstore verification returned no Rekor timestamp")
	}
	repository, workflow, err := verifier.extractIdentity(result.Signature.Certificate)
	if err != nil {
		return SigstoreEvidence{}, fmt.Errorf("extract verified GitHub identity: %w", err)
	}
	return SigstoreEvidence{
		CertificateVerified:    true,
		ChainVerified:          true,
		RekorInclusionVerified: true,
		ArtifactDigest:         artifactDigest,
		OIDCIssuer:             result.Signature.Certificate.Issuer,
		GitHubRepository:       repository,
		WorkflowIdentity:       workflow,
	}, nil
}

func loadSigstoreEntity(value []byte) (sigverify.SignedEntity, error) {
	if len(value) == 0 {
		return nil, errors.New("sigstore bundle is empty")
	}
	entity := &sigbundle.Bundle{}
	if err := entity.UnmarshalJSON(value); err != nil {
		return nil, fmt.Errorf("decode sigstore bundle: %w", err)
	}
	return entity, nil
}

func extractGitHubIdentity(summary *certificate.Summary) (string, string, error) {
	if summary == nil || summary.Issuer == "" {
		return "", "", errors.New("verified certificate has no OIDC issuer")
	}
	var repository string
	var workflow string
	setRepository := func(candidate string) error {
		if candidate == "" {
			return nil
		}
		if !validGitHubRepository(candidate) {
			return errors.New("certificate contains an invalid GitHub repository")
		}
		if repository != "" && repository != candidate {
			return errors.New("certificate contains conflicting GitHub repositories")
		}
		repository = candidate
		return nil
	}
	setWorkflow := func(candidateRepository string, candidateWorkflow string) error {
		if candidateWorkflow == "" {
			return nil
		}
		if err := setRepository(candidateRepository); err != nil {
			return err
		}
		if workflow != "" && workflow != candidateWorkflow {
			return errors.New("certificate contains conflicting GitHub workflows")
		}
		workflow = candidateWorkflow
		return nil
	}

	if err := setRepository(summary.GithubWorkflowRepository); err != nil {
		return "", "", err
	}
	if summary.SourceRepositoryURI != "" {
		candidate, err := parseGitHubRepositoryURI(summary.SourceRepositoryURI)
		if err != nil {
			return "", "", err
		}
		if err := setRepository(candidate); err != nil {
			return "", "", err
		}
	}
	for _, candidate := range []string{
		summary.SubjectAlternativeName,
		summary.BuildSignerURI,
		summary.BuildConfigURI,
	} {
		if candidate == "" {
			continue
		}
		candidateRepository, candidateWorkflow, err := parseGitHubWorkflowURI(candidate)
		if err != nil {
			return "", "", err
		}
		if err := setWorkflow(candidateRepository, candidateWorkflow); err != nil {
			return "", "", err
		}
	}
	if repository == "" || workflow == "" {
		return "", "", errors.New("verified certificate has no exact GitHub workflow identity")
	}
	return repository, workflow, nil
}

func parseGitHubRepositoryURI(value string) (string, error) {
	parsed, err := strictGitHubURL(value)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return "", errors.New("certificate GitHub repository URI is not repository-scoped")
	}
	repository := strings.Join(parts, "/")
	if !validGitHubRepository(repository) {
		return "", errors.New("certificate GitHub repository URI is invalid")
	}
	return repository, nil
}

func parseGitHubWorkflowURI(value string) (string, string, error) {
	parsed, err := strictGitHubURL(value)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) < 6 {
		return "", "", errors.New("certificate GitHub workflow URI is incomplete")
	}
	repository := strings.Join(parts[:2], "/")
	workflow := strings.Join(parts[2:], "/")
	if !validGitHubRepository(repository) ||
		!strings.HasPrefix(workflow, ".github/workflows/") ||
		!strings.Contains(workflow, "@refs/") ||
		strings.HasPrefix(workflow, "/") ||
		strings.HasSuffix(workflow, "/") {
		return "", "", errors.New("certificate GitHub workflow URI is invalid")
	}
	for _, part := range strings.Split(workflow, "/") {
		if part == "" || part == "." || part == ".." {
			return "", "", errors.New("certificate GitHub workflow URI is unsafe")
		}
	}
	return repository, workflow, nil
}

func strictGitHubURL(value string) (*url.URL, error) {
	if strings.Contains(value, "%") || strings.ContainsAny(value, " \t\r\n") {
		return nil, errors.New("certificate GitHub URI contains escaped or whitespace characters")
	}
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Scheme != "https" ||
		parsed.Host != "github.com" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		parsed.RawPath != "" {
		return nil, errors.New("certificate GitHub URI is not an exact HTTPS github.com URL")
	}
	return parsed, nil
}

func validGitHubRepository(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
			return false
		}
		for _, character := range part {
			if (character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') ||
				character == '-' ||
				character == '_' ||
				character == '.' {
				continue
			}
			return false
		}
	}
	return true
}
