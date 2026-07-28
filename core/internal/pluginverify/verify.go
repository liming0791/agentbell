package pluginverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func (validator Validator) Verify(ctx context.Context, request Request) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	root, manifestPath, manifestBytes, manifest, err := ReadManifest(
		request.Root,
		request.ManifestPath,
	)
	if err != nil {
		return Report{}, err
	}
	if err := validateManifest(manifest, request.Policy); err != nil {
		return Report{}, err
	}

	bundlePath := request.SigstoreBundlePath
	if bundlePath != "" {
		bundlePath, err = normalizedRelativePath(bundlePath)
		if err != nil {
			return Report{}, err
		}
	}
	if manifest.SignatureStatus == SignatureSigned && bundlePath == "" {
		return Report{}, ErrSignatureVerification
	}
	declared, allowedDirectories, err := declaredPaths(
		manifest,
		manifestPath,
		bundlePath,
	)
	if err != nil {
		return Report{}, err
	}
	if err := verifyFileSet(ctx, root, declared, allowedDirectories); err != nil {
		return Report{}, err
	}
	for _, file := range manifest.Files {
		if err := verifyPayload(ctx, root, file); err != nil {
			return Report{}, err
		}
	}

	artifactDigest := digest(manifestBytes)
	report := Report{
		Manifest:        manifest,
		SignatureStatus: manifest.SignatureStatus,
		ArtifactDigest:  artifactDigest,
		FilesVerified:   len(manifest.Files),
	}
	switch manifest.SignatureStatus {
	case SignatureTechnicalPreview:
		if request.Policy.RequireSigned {
			return Report{}, ErrSignatureRequired
		}
		if bundlePath != "" {
			return Report{}, fmt.Errorf(
				"%w: technical-preview cannot include a signature bundle",
				ErrInvalidManifest,
			)
		}
		return report, nil
	case SignatureSigned:
		return validator.verifySignature(
			ctx,
			root,
			bundlePath,
			manifestBytes,
			artifactDigest,
			request.Policy,
			report,
		)
	default:
		return Report{}, ErrInvalidManifest
	}
}

// ReadManifest safely loads and strictly parses the unsigned metadata needed
// to select an exact verification policy. It does not verify or trust the
// manifest, its files, or its signature.
func ReadManifest(
	requestRoot string,
	requestManifestPath string,
) (string, string, []byte, Manifest, error) {
	root, err := safeRoot(requestRoot)
	if err != nil {
		return "", "", nil, Manifest{}, err
	}
	manifestPath := requestManifestPath
	if manifestPath == "" {
		manifestPath = DefaultManifestPath
	}
	manifestPath, err = normalizedRelativePath(manifestPath)
	if err != nil {
		return "", "", nil, Manifest{}, err
	}
	manifestBytes, err := readRegularFile(root, manifestPath, maximumManifestSize)
	if err != nil {
		if errors.Is(err, ErrSymlink) || errors.Is(err, ErrNonRegularFile) {
			return "", "", nil, Manifest{}, err
		}
		return "", "", nil, Manifest{}, fmt.Errorf(
			"%w: manifest cannot be read",
			ErrInvalidManifest,
		)
	}
	manifest, err := parseManifest(manifestBytes)
	if err != nil {
		return "", "", nil, Manifest{}, err
	}
	return root, manifestPath, manifestBytes, manifest, nil
}

func (validator Validator) verifySignature(
	ctx context.Context,
	root string,
	bundlePath string,
	artifact []byte,
	artifactDigest string,
	policy Policy,
	report Report,
) (Report, error) {
	if validator.Verifier == nil ||
		bundlePath == "" ||
		policy.OIDCIssuer == "" ||
		policy.GitHubRepository == "" ||
		policy.WorkflowIdentity == "" {
		return Report{}, ErrSignatureVerification
	}
	bundle, err := readRegularFile(root, bundlePath, maximumSigstoreBundleSize)
	if err != nil {
		return Report{}, ErrSignatureVerification
	}
	evidence, err := validator.Verifier.Verify(ctx, SigstoreInput{
		Bundle:         bundle,
		Artifact:       artifact,
		ArtifactDigest: artifactDigest,
	})
	if err != nil ||
		!evidence.CertificateVerified ||
		!evidence.ChainVerified ||
		!evidence.RekorInclusionVerified ||
		evidence.ArtifactDigest != artifactDigest ||
		evidence.OIDCIssuer != policy.OIDCIssuer ||
		evidence.GitHubRepository != policy.GitHubRepository ||
		evidence.WorkflowIdentity != policy.WorkflowIdentity {
		return Report{}, ErrSignatureVerification
	}
	report.Signed = true
	report.Identity = &Identity{
		OIDCIssuer:       evidence.OIDCIssuer,
		GitHubRepository: evidence.GitHubRepository,
		WorkflowIdentity: evidence.WorkflowIdentity,
	}
	return report, nil
}

func safeRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", ErrUnsafePath
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", ErrUnsafePath
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", ErrSymlink
	}
	if !info.IsDir() {
		return "", ErrNonRegularFile
	}
	return absolute, nil
}

func parseManifest(value []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: invalid JSON", ErrInvalidManifest)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("%w: trailing JSON", ErrInvalidManifest)
	}
	return manifest, nil
}

func validateManifest(manifest Manifest, policy Policy) error {
	if manifest.Version != ManifestVersion ||
		!validPluginID(manifest.PluginID) ||
		!validPluginVersion(manifest.PluginVersion) ||
		(manifest.SignatureStatus != SignatureTechnicalPreview &&
			manifest.SignatureStatus != SignatureSigned) {
		return ErrInvalidManifest
	}
	if policy.ExpectedPluginID != "" && manifest.PluginID != policy.ExpectedPluginID {
		return ErrInvalidManifest
	}
	minimum, minimumOK := parseSemanticVersion(manifest.MinCoreVersion)
	maximum, maximumOK := parseSemanticVersion(manifest.MaxCoreVersion)
	if !minimumOK || !maximumOK || compareSemanticVersions(minimum, maximum) > 0 {
		return ErrInvalidManifest
	}
	current, currentOK := parseSemanticVersion(policy.CoreVersion)
	if !currentOK ||
		compareSemanticVersions(current, minimum) < 0 ||
		compareSemanticVersions(current, maximum) > 0 {
		return ErrIncompatibleCore
	}
	return nil
}

func validPluginVersion(value string) bool {
	_, ok := parseSemanticVersion(value)
	return ok
}

func validPluginID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	separator := false
	for index, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') {
			separator = false
			continue
		}
		if index > 0 &&
			!separator &&
			(character == '.' || character == '_' || character == '-') {
			separator = true
			continue
		}
		return false
	}
	return !separator
}

func declaredPaths(
	manifest Manifest,
	manifestPath string,
	bundlePath string,
) (map[string]File, map[string]bool, error) {
	declared := make(map[string]File, len(manifest.Files)+2)
	caseFolded := make(map[string]string, len(manifest.Files)+2)
	directories := map[string]bool{".": true}
	addControl := func(candidate string) error {
		folded := strings.ToLower(candidate)
		if _, exists := caseFolded[folded]; exists {
			return ErrUnsafePath
		}
		caseFolded[folded] = candidate
		declared[candidate] = File{Path: candidate}
		addParentDirectories(directories, candidate)
		return nil
	}
	if err := addControl(manifestPath); err != nil {
		return nil, nil, err
	}
	if bundlePath != "" {
		if err := addControl(bundlePath); err != nil {
			return nil, nil, err
		}
	}
	for _, file := range manifest.Files {
		normalized, err := normalizedRelativePath(file.Path)
		if err != nil || normalized != file.Path {
			return nil, nil, ErrUnsafePath
		}
		if file.Size < 0 || !validDigest(file.SHA256) {
			return nil, nil, ErrInvalidManifest
		}
		folded := strings.ToLower(normalized)
		if _, exists := caseFolded[folded]; exists {
			return nil, nil, ErrUnsafePath
		}
		caseFolded[folded] = normalized
		declared[normalized] = file
		addParentDirectories(directories, normalized)
	}
	return declared, directories, nil
}

func addParentDirectories(directories map[string]bool, relative string) {
	current := path.Dir(relative)
	for {
		directories[current] = true
		if current == "." {
			return
		}
		current = path.Dir(current)
	}
}

func normalizedRelativePath(value string) (string, error) {
	if value == "" ||
		strings.Contains(value, "\\") ||
		strings.ContainsRune(value, '\x00') ||
		path.IsAbs(value) ||
		filepath.IsAbs(value) ||
		path.Clean(value) != value {
		return "", ErrUnsafePath
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", ErrUnsafePath
		}
		if strings.Contains(part, ":") ||
			strings.HasSuffix(part, ".") ||
			strings.HasSuffix(part, " ") ||
			windowsReservedName(part) {
			return "", ErrUnsafePath
		}
		for _, character := range part {
			if character < ' ' || character == '\u007f' {
				return "", ErrUnsafePath
			}
		}
	}
	return value, nil
}

func windowsReservedName(value string) bool {
	base := strings.ToUpper(strings.SplitN(value, ".", 2)[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 &&
		(base[:3] == "COM" || base[:3] == "LPT") &&
		base[3] >= '1' &&
		base[3] <= '9' {
		return true
	}
	return false
}

func verifyFileSet(
	ctx context.Context,
	root string,
	declared map[string]File,
	allowedDirectories map[string]bool,
) error {
	found := make(map[string]bool, len(declared))
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == root {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return ErrUnsafePath
		}
		relative = filepath.ToSlash(relative)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
		if info.IsDir() {
			if _, expectedFile := declared[relative]; expectedFile {
				return ErrNonRegularFile
			}
			if !allowedDirectories[relative] {
				return ErrFileSetMismatch
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return ErrNonRegularFile
		}
		if _, ok := declared[relative]; !ok {
			return ErrFileSetMismatch
		}
		found[relative] = true
		return nil
	})
	if err != nil {
		return err
	}
	for relative := range declared {
		if !found[relative] {
			return ErrFileSetMismatch
		}
	}
	return nil
}

func verifyPayload(ctx context.Context, root string, expected File) error {
	pathInfo, fullPath, err := regularPathInfo(root, expected.Path)
	if err != nil {
		if errors.Is(err, ErrSymlink) || errors.Is(err, ErrNonRegularFile) {
			return err
		}
		return ErrFileIntegrity
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return ErrFileIntegrity
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(pathInfo, before) {
		return ErrNonRegularFile
	}
	if before.Size() != expected.Size {
		return ErrFileIntegrity
	}
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return ErrFileIntegrity
		}
	}
	after, err := file.Stat()
	finalPathInfo, _, pathErr := regularPathInfo(root, expected.Path)
	if err != nil ||
		pathErr != nil ||
		!os.SameFile(before, after) ||
		!os.SameFile(after, finalPathInfo) ||
		before.Size() != after.Size() ||
		before.ModTime() != after.ModTime() {
		return ErrFileIntegrity
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != expected.SHA256 {
		return ErrFileIntegrity
	}
	return nil
}

func readRegularFile(root string, relative string, maximum int64) ([]byte, error) {
	pathInfo, current, err := regularPathInfo(root, relative)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(current)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, opened) {
		return nil, ErrNonRegularFile
	}
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > maximum {
		return nil, errors.New("file exceeds verification size limit")
	}
	finalPathInfo, _, err := regularPathInfo(root, relative)
	if err != nil || !os.SameFile(opened, finalPathInfo) {
		return nil, ErrNonRegularFile
	}
	return value, nil
}

func regularPathInfo(root string, relative string) (os.FileInfo, string, error) {
	current := root
	parts := strings.Split(relative, "/")
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", ErrSymlink
	}
	if !rootInfo.IsDir() {
		return nil, "", ErrNonRegularFile
	}
	for index, part := range parts {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if err != nil {
			return nil, "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, "", ErrSymlink
		}
		if index < len(parts)-1 {
			if !info.IsDir() {
				return nil, "", ErrNonRegularFile
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, "", ErrNonRegularFile
		}
		return info, current, nil
	}
	return nil, "", ErrUnsafePath
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	encoded := strings.TrimPrefix(value, "sha256:")
	if encoded != strings.ToLower(encoded) || len(encoded) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == sha256.Size
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
