package pluginverify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fakeVerifier struct {
	input    SigstoreInput
	evidence SigstoreEvidence
	err      error
	calls    int
}

func (verifier *fakeVerifier) Verify(
	_ context.Context,
	input SigstoreInput,
) (SigstoreEvidence, error) {
	verifier.calls++
	verifier.input = input
	return verifier.evidence, verifier.err
}

func TestVerifyTechnicalPreviewManifestAndFiles(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "plugin.json", []byte(`{"name":"agentbell"}`))
	writePayload(t, root, "skills/setup/SKILL.md", []byte("# Setup\n"))
	manifest := validManifest(root, SignatureTechnicalPreview, []string{
		"plugin.json",
		"skills/setup/SKILL.md",
	})
	writeManifest(t, root, manifest)

	report, err := (Validator{}).Verify(context.Background(), Request{
		Root: root,
		Policy: Policy{
			CoreVersion:      "2.1.0",
			ExpectedPluginID: "agentbell.codex",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Manifest.PluginID != "agentbell.codex" ||
		report.SignatureStatus != SignatureTechnicalPreview ||
		report.FilesVerified != 2 ||
		report.ArtifactDigest == "" ||
		report.Signed {
		t.Fatalf("report = %#v", report)
	}
}

func TestVerifySignedBundleUsesExactManifestAndPinnedIdentity(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "plugin.json", []byte(`{"name":"agentbell"}`))
	manifest := validManifest(root, SignatureSigned, []string{"plugin.json"})
	manifestBytes := writeManifest(t, root, manifest)
	bundleBytes := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle+json;version=0.3"}`)
	writePayload(t, root, "plugin.sigstore.json", bundleBytes)
	digest := digestBytes(manifestBytes)
	verifier := &fakeVerifier{evidence: SigstoreEvidence{
		CertificateVerified:    true,
		ChainVerified:          true,
		RekorInclusionVerified: true,
		ArtifactDigest:         digest,
		OIDCIssuer:             "https://token.actions.githubusercontent.com",
		GitHubRepository:       "liming0791/agentbell",
		WorkflowIdentity:       ".github/workflows/release.yml@refs/tags/v2.1.0",
	}}
	policy := signedPolicy()

	report, err := (Validator{Verifier: verifier}).Verify(context.Background(), Request{
		Root:               root,
		SigstoreBundlePath: "plugin.sigstore.json",
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if verifier.calls != 1 ||
		string(verifier.input.Artifact) != string(manifestBytes) ||
		string(verifier.input.Bundle) != string(bundleBytes) ||
		verifier.input.ArtifactDigest != digest {
		t.Fatalf("verifier input = %#v", verifier.input)
	}
	if !report.Signed || report.Identity == nil ||
		report.Identity.GitHubRepository != policy.GitHubRepository {
		t.Fatalf("report = %#v", report)
	}
}

func TestSignedVerificationFailsClosedWithoutDowngrade(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "plugin.json", []byte("payload"))
	manifest := validManifest(root, SignatureSigned, []string{"plugin.json"})
	manifestBytes := writeManifest(t, root, manifest)
	writePayload(t, root, "bundle.json", []byte(`{"bundle":true}`))
	digest := digestBytes(manifestBytes)
	validEvidence := SigstoreEvidence{
		CertificateVerified:    true,
		ChainVerified:          true,
		RekorInclusionVerified: true,
		ArtifactDigest:         digest,
		OIDCIssuer:             signedPolicy().OIDCIssuer,
		GitHubRepository:       signedPolicy().GitHubRepository,
		WorkflowIdentity:       signedPolicy().WorkflowIdentity,
	}

	tests := []struct {
		name     string
		verifier Verifier
		policy   Policy
		bundle   string
	}{
		{"missing verifier", nil, signedPolicy(), "bundle.json"},
		{
			"verifier error",
			&fakeVerifier{err: errors.New("invalid signature")},
			signedPolicy(),
			"bundle.json",
		},
		{
			"certificate false",
			&fakeVerifier{evidence: mutateEvidence(validEvidence, func(value *SigstoreEvidence) {
				value.CertificateVerified = false
			})},
			signedPolicy(),
			"bundle.json",
		},
		{
			"chain false",
			&fakeVerifier{evidence: mutateEvidence(validEvidence, func(value *SigstoreEvidence) {
				value.ChainVerified = false
			})},
			signedPolicy(),
			"bundle.json",
		},
		{
			"rekor false",
			&fakeVerifier{evidence: mutateEvidence(validEvidence, func(value *SigstoreEvidence) {
				value.RekorInclusionVerified = false
			})},
			signedPolicy(),
			"bundle.json",
		},
		{
			"digest mismatch",
			&fakeVerifier{evidence: mutateEvidence(validEvidence, func(value *SigstoreEvidence) {
				value.ArtifactDigest = digestBytes([]byte("other"))
			})},
			signedPolicy(),
			"bundle.json",
		},
		{
			"issuer mismatch",
			&fakeVerifier{evidence: mutateEvidence(validEvidence, func(value *SigstoreEvidence) {
				value.OIDCIssuer = "https://issuer.example"
			})},
			signedPolicy(),
			"bundle.json",
		},
		{
			"repository mismatch",
			&fakeVerifier{evidence: mutateEvidence(validEvidence, func(value *SigstoreEvidence) {
				value.GitHubRepository = "other/repository"
			})},
			signedPolicy(),
			"bundle.json",
		},
		{
			"workflow mismatch",
			&fakeVerifier{evidence: mutateEvidence(validEvidence, func(value *SigstoreEvidence) {
				value.WorkflowIdentity = ".github/workflows/other.yml@refs/heads/main"
			})},
			signedPolicy(),
			"bundle.json",
		},
		{
			"missing pinned policy",
			&fakeVerifier{evidence: validEvidence},
			Policy{CoreVersion: "2.1.0", RequireSigned: true},
			"bundle.json",
		},
		{
			"missing bundle",
			&fakeVerifier{evidence: validEvidence},
			signedPolicy(),
			"",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := (Validator{Verifier: test.verifier}).Verify(
				context.Background(),
				Request{
					Root:               root,
					SigstoreBundlePath: test.bundle,
					Policy:             test.policy,
				},
			)
			if !errors.Is(err, ErrSignatureVerification) {
				t.Fatalf("error = %v", err)
			}
			if report.Signed ||
				report.SignatureStatus == SignatureTechnicalPreview {
				t.Fatalf("verification downgraded: %#v", report)
			}
		})
	}
}

func TestUnsignedRequiresExplicitTechnicalPreviewAndPolicyPermission(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "plugin.json", []byte("payload"))
	manifest := validManifest(root, SignatureTechnicalPreview, []string{"plugin.json"})
	writeManifest(t, root, manifest)

	if _, err := (Validator{}).Verify(context.Background(), Request{
		Root: root,
		Policy: Policy{
			CoreVersion:   "2.1.0",
			RequireSigned: true,
		},
	}); !errors.Is(err, ErrSignatureRequired) {
		t.Fatalf("RequireSigned error = %v", err)
	}

	manifest.SignatureStatus = ""
	writeManifest(t, root, manifest)
	if _, err := (Validator{}).Verify(context.Background(), Request{
		Root: root, Policy: Policy{CoreVersion: "2.1.0"},
	}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("implicit unsigned error = %v", err)
	}

	manifest.SignatureStatus = SignatureTechnicalPreview
	writeManifest(t, root, manifest)
	writePayload(t, root, "bundle.json", []byte("{}"))
	if _, err := (Validator{}).Verify(context.Background(), Request{
		Root:               root,
		SigstoreBundlePath: "bundle.json",
		Policy:             Policy{CoreVersion: "2.1.0"},
	}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("preview with signature bundle error = %v", err)
	}
}

func TestManifestRejectsUnsafeAndDuplicatePaths(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
	}{
		{"absolute", []string{"/etc/passwd"}},
		{"parent", []string{"../secret"}},
		{"embedded parent", []string{"skills/../secret"}},
		{"backslash", []string{`skills\\setup.md`}},
		{"current segment", []string{"./plugin.json"}},
		{"duplicate slash", []string{"skills//setup.md"}},
		{"trailing slash", []string{"skills/"}},
		{"windows drive", []string{"C:/plugin.json"}},
		{"windows alternate data stream", []string{"plugin.json:secret"}},
		{"windows reserved", []string{"CON"}},
		{"windows reserved with extension", []string{"aux.txt"}},
		{"trailing dot", []string{"plugin."}},
		{"trailing space", []string{"plugin "}},
		{"duplicate", []string{"plugin.json", "plugin.json"}},
		{"case collision", []string{"Plugin.json", "plugin.json"}},
		{"manifest collision", []string{DefaultManifestPath}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			manifest := Manifest{
				Version:         ManifestVersion,
				PluginID:        "agentbell.codex",
				PluginVersion:   "2.1.0",
				MinCoreVersion:  "2.0.0",
				MaxCoreVersion:  "3.0.0",
				SignatureStatus: SignatureTechnicalPreview,
			}
			for _, candidate := range test.paths {
				manifest.Files = append(manifest.Files, File{
					Path:   candidate,
					SHA256: digestBytes(nil),
					Size:   0,
				})
			}
			writeManifest(t, root, manifest)
			if _, err := (Validator{}).Verify(context.Background(), Request{
				Root: root, Policy: Policy{CoreVersion: "2.1.0"},
			}); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestVerifyRejectsMissingExtraSymlinkAndNonRegularFiles(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		root := t.TempDir()
		manifest := manifestForFiles(SignatureTechnicalPreview, []File{{
			Path: "missing.txt", SHA256: digestBytes(nil), Size: 0,
		}})
		writeManifest(t, root, manifest)
		assertErrorIs(t, root, ErrFileSetMismatch)
	})
	t.Run("extra", func(t *testing.T) {
		root := t.TempDir()
		writePayload(t, root, "declared.txt", []byte("declared"))
		writePayload(t, root, "extra.txt", []byte("extra"))
		writeManifest(t, root, validManifest(root, SignatureTechnicalPreview, []string{"declared.txt"}))
		assertErrorIs(t, root, ErrFileSetMismatch)
	})
	t.Run("unexpected directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeManifest(t, root, manifestForFiles(SignatureTechnicalPreview, nil))
		assertErrorIs(t, root, ErrFileSetMismatch)
	})
	t.Run("declared directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "payload"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeManifest(t, root, manifestForFiles(SignatureTechnicalPreview, []File{{
			Path: "payload", SHA256: digestBytes(nil), Size: 0,
		}}))
		assertErrorIs(t, root, ErrNonRegularFile)
	})
	if runtime.GOOS != "windows" {
		t.Run("symlink", func(t *testing.T) {
			root := t.TempDir()
			writePayload(t, root, "target.txt", []byte("target"))
			if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
				t.Fatal(err)
			}
			writeManifest(t, root, validManifest(root, SignatureTechnicalPreview, []string{"target.txt"}))
			assertErrorIs(t, root, ErrSymlink)
		})
		t.Run("root symlink", func(t *testing.T) {
			realRoot := t.TempDir()
			writeManifest(t, realRoot, manifestForFiles(SignatureTechnicalPreview, nil))
			linkRoot := filepath.Join(t.TempDir(), "bundle")
			if err := os.Symlink(realRoot, linkRoot); err != nil {
				t.Fatal(err)
			}
			if _, err := (Validator{}).Verify(context.Background(), Request{
				Root: linkRoot, Policy: Policy{CoreVersion: "2.1.0"},
			}); !errors.Is(err, ErrSymlink) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestVerifyRejectsSizeAndDigestMismatch(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "payload.txt", []byte("payload"))
	manifest := validManifest(root, SignatureTechnicalPreview, []string{"payload.txt"})
	manifest.Files[0].Size++
	writeManifest(t, root, manifest)
	assertErrorIs(t, root, ErrFileIntegrity)

	manifest = validManifest(root, SignatureTechnicalPreview, []string{"payload.txt"})
	manifest.Files[0].SHA256 = digestBytes([]byte("different"))
	writeManifest(t, root, manifest)
	assertErrorIs(t, root, ErrFileIntegrity)
}

func TestManifestRejectsInvalidFileMetadata(t *testing.T) {
	tests := []File{
		{Path: "payload", SHA256: "md5:0000", Size: 0},
		{Path: "payload", SHA256: "sha256:ABCDEF", Size: 0},
		{Path: "payload", SHA256: "sha256:" + strings.Repeat("z", 64), Size: 0},
		{Path: "payload", SHA256: digestBytes(nil), Size: -1},
	}
	for index, file := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			root := t.TempDir()
			writeManifest(t, root, manifestForFiles(SignatureTechnicalPreview, []File{file}))
			if _, err := (Validator{}).Verify(context.Background(), Request{
				Root: root, Policy: Policy{CoreVersion: "2.1.0"},
			}); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestManifestAndCoreVersionValidation(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Manifest)
		core     string
		pluginID string
		want     error
	}{
		{"manifest version", func(value *Manifest) { value.Version = 2 }, "2.1.0", "", ErrInvalidManifest},
		{"missing plugin id", func(value *Manifest) { value.PluginID = "" }, "2.1.0", "", ErrInvalidManifest},
		{"invalid plugin id", func(value *Manifest) { value.PluginID = "../plugin" }, "2.1.0", "", ErrInvalidManifest},
		{"trailing plugin separator", func(value *Manifest) { value.PluginID = "agentbell." }, "2.1.0", "", ErrInvalidManifest},
		{"adjacent plugin separators", func(value *Manifest) { value.PluginID = "agentbell.-codex" }, "2.1.0", "", ErrInvalidManifest},
		{"missing plugin version", func(value *Manifest) { value.PluginVersion = "" }, "2.1.0", "", ErrInvalidManifest},
		{"invalid plugin version", func(value *Manifest) { value.PluginVersion = "v2.1" }, "2.1.0", "", ErrInvalidManifest},
		{"invalid min", func(value *Manifest) { value.MinCoreVersion = "v2" }, "2.1.0", "", ErrInvalidManifest},
		{"invalid max", func(value *Manifest) { value.MaxCoreVersion = "latest" }, "2.1.0", "", ErrInvalidManifest},
		{"backwards range", func(value *Manifest) { value.MinCoreVersion = "4.0.0" }, "2.1.0", "", ErrInvalidManifest},
		{"core too old", func(*Manifest) {}, "1.9.9", "", ErrIncompatibleCore},
		{"core too new", func(*Manifest) {}, "3.0.1", "", ErrIncompatibleCore},
		{"invalid core", func(*Manifest) {}, "dev", "", ErrIncompatibleCore},
		{"plugin substitution", func(*Manifest) {}, "2.1.0", "agentbell.claude", ErrInvalidManifest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			manifest := manifestForFiles(SignatureTechnicalPreview, nil)
			test.mutate(&manifest)
			writeManifest(t, root, manifest)
			if _, err := (Validator{}).Verify(context.Background(), Request{
				Root: root,
				Policy: Policy{
					CoreVersion:      test.core,
					ExpectedPluginID: test.pluginID,
				},
			}); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestManifestStrictJSONAndContext(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
	}{
		{"unknown", `{"version":1,"unknown":true}`},
		{"trailing", `{"version":1} {}`},
		{"oversized", strings.Repeat(" ", maximumManifestSize+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writePayload(t, root, DefaultManifestPath, []byte(test.value))
			if _, err := (Validator{}).Verify(context.Background(), Request{
				Root: root, Policy: Policy{CoreVersion: "2.1.0"},
			}); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	root := t.TempDir()
	writeManifest(t, root, manifestForFiles(SignatureTechnicalPreview, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Validator{}).Verify(ctx, Request{
		Root: root, Policy: Policy{CoreVersion: "2.1.0"},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}

func TestVerifyRejectsUnsafeRootAndControlPaths(t *testing.T) {
	if _, err := (Validator{}).Verify(context.Background(), Request{
		Root: "", Policy: Policy{CoreVersion: "2.1.0"},
	}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("empty root error = %v", err)
	}
	regularFile := filepath.Join(t.TempDir(), "bundle")
	writePayload(t, filepath.Dir(regularFile), filepath.Base(regularFile), []byte("file"))
	if _, err := (Validator{}).Verify(context.Background(), Request{
		Root: regularFile, Policy: Policy{CoreVersion: "2.1.0"},
	}); !errors.Is(err, ErrNonRegularFile) {
		t.Fatalf("regular root error = %v", err)
	}
	root := t.TempDir()
	writeManifest(t, root, manifestForFiles(SignatureTechnicalPreview, nil))
	if _, err := (Validator{}).Verify(context.Background(), Request{
		Root: root, ManifestPath: "../manifest.json", Policy: Policy{CoreVersion: "2.1.0"},
	}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("unsafe manifest path error = %v", err)
	}

	signed := manifestForFiles(SignatureSigned, nil)
	writeManifest(t, root, signed)
	if _, err := (Validator{}).Verify(context.Background(), Request{
		Root:               root,
		SigstoreBundlePath: DefaultManifestPath,
		Policy:             signedPolicy(),
	}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("control collision error = %v", err)
	}
}

func validManifest(root string, status string, paths []string) Manifest {
	files := make([]File, 0, len(paths))
	for _, relative := range paths {
		value, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			panic(err)
		}
		files = append(files, File{
			Path:   relative,
			SHA256: digestBytes(value),
			Size:   int64(len(value)),
		})
	}
	return manifestForFiles(status, files)
}

func manifestForFiles(status string, files []File) Manifest {
	return Manifest{
		Version:         ManifestVersion,
		PluginID:        "agentbell.codex",
		PluginVersion:   "2.1.0",
		MinCoreVersion:  "2.0.0",
		MaxCoreVersion:  "3.0.0",
		SignatureStatus: status,
		Files:           files,
	}
}

func writeManifest(t *testing.T, root string, manifest Manifest) []byte {
	t.Helper()
	value, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	value = append(value, '\n')
	writePayload(t, root, DefaultManifestPath, value)
	return value
}

func writePayload(t *testing.T, root string, relative string, value []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func signedPolicy() Policy {
	return Policy{
		CoreVersion:      "2.1.0",
		RequireSigned:    true,
		ExpectedPluginID: "agentbell.codex",
		OIDCIssuer:       "https://token.actions.githubusercontent.com",
		GitHubRepository: "liming0791/agentbell",
		WorkflowIdentity: ".github/workflows/release.yml@refs/tags/v2.1.0",
	}
}

func mutateEvidence(
	value SigstoreEvidence,
	mutate func(*SigstoreEvidence),
) SigstoreEvidence {
	mutate(&value)
	return value
}

func assertErrorIs(t *testing.T, root string, want error) {
	t.Helper()
	if _, err := (Validator{}).Verify(context.Background(), Request{
		Root: root, Policy: Policy{CoreVersion: "2.1.0"},
	}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
