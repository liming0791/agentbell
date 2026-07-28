package pluginverify

import (
	"context"
	"errors"
)

const (
	ManifestVersion     = 1
	DefaultManifestPath = "plugin-manifest.json"

	SignatureTechnicalPreview = "technical-preview"
	SignatureSigned           = "sigstore-verified"

	maximumManifestSize       = 1 << 20
	maximumSigstoreBundleSize = 8 << 20
)

var (
	ErrInvalidManifest       = errors.New("invalid plugin manifest")
	ErrUnsafePath            = errors.New("unsafe plugin path")
	ErrFileSetMismatch       = errors.New("plugin file set does not match manifest")
	ErrSymlink               = errors.New("plugin bundle contains a symlink")
	ErrNonRegularFile        = errors.New("plugin bundle contains a non-regular file")
	ErrFileIntegrity         = errors.New("plugin file integrity check failed")
	ErrIncompatibleCore      = errors.New("plugin is incompatible with this core version")
	ErrSignatureRequired     = errors.New("signed plugin bundle is required")
	ErrSignatureVerification = errors.New("plugin signature verification failed")
)

type Manifest struct {
	Version         int    `json:"version"`
	PluginID        string `json:"pluginId"`
	PluginVersion   string `json:"pluginVersion"`
	MinCoreVersion  string `json:"minCoreVersion"`
	MaxCoreVersion  string `json:"maxCoreVersion"`
	SignatureStatus string `json:"signatureStatus"`
	Files           []File `json:"files"`
}

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Policy struct {
	CoreVersion      string
	RequireSigned    bool
	ExpectedPluginID string
	OIDCIssuer       string
	GitHubRepository string
	WorkflowIdentity string
}

type Request struct {
	Root               string
	ManifestPath       string
	SigstoreBundlePath string
	Policy             Policy
}

// Verifier is the cryptographic trust boundary. A production implementation
// must use the official Sigstore verifier to validate the certificate, chain,
// transparency-log inclusion and signature over Artifact exactly as supplied.
// The domain layer independently checks every evidence field and pinned
// identity before accepting the plugin.
type Verifier interface {
	Verify(context.Context, SigstoreInput) (SigstoreEvidence, error)
}

type SigstoreInput struct {
	// Artifact is the exact plugin-manifest.json byte sequence. Its signed
	// digest transitively binds every payload path, size and SHA-256 entry.
	Bundle         []byte
	Artifact       []byte
	ArtifactDigest string
}

type SigstoreEvidence struct {
	CertificateVerified    bool
	ChainVerified          bool
	RekorInclusionVerified bool
	ArtifactDigest         string
	OIDCIssuer             string
	GitHubRepository       string
	WorkflowIdentity       string
}

type Identity struct {
	OIDCIssuer       string `json:"oidcIssuer"`
	GitHubRepository string `json:"githubRepository"`
	WorkflowIdentity string `json:"workflowIdentity"`
}

type Report struct {
	Manifest        Manifest  `json:"manifest"`
	SignatureStatus string    `json:"signatureStatus"`
	ArtifactDigest  string    `json:"artifactDigest"`
	FilesVerified   int       `json:"filesVerified"`
	Signed          bool      `json:"signed"`
	Identity        *Identity `json:"identity,omitempty"`
}

type Validator struct {
	Verifier Verifier
}
