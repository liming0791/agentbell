package remoteconfig

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	pathpkg "path"
	"regexp"
	"strconv"
	"strings"

	"github.com/liming0791/agentbell/core/internal/event"
)

const (
	minimumOutboxBytes = 1 << 20
	maximumOutboxBytes = 16 << 30
)

var (
	semanticVersionPattern = regexp.MustCompile(
		`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`,
	)
	identifierPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	hostLabelPattern    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	userPattern         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9._-]{0,63}$`)
	tokenPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)
	containerPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	distributionPattern = regexp.MustCompile(
		`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`,
	)
	hexSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (config RemoteConfig) Validate() error {
	if err := validateHeader(config.Version, config.MinCoreVersion); err != nil {
		return err
	}
	if err := validateIdentity("teamId", config.TeamID); err != nil {
		return err
	}
	if err := validateIdentity("originId", config.OriginID); err != nil {
		return err
	}
	if !event.IsKnownRuntime(config.Runtime) || config.Runtime == "host" {
		return fmt.Errorf("unsupported remote runtime %q", config.Runtime)
	}
	if err := config.Outbox.Validate(); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	if err := config.Connector.Validate(config.Runtime); err != nil {
		return fmt.Errorf("connector: %w", err)
	}
	if err := config.PrivateKeyRef.Validate(); err != nil {
		return fmt.Errorf("privateKeyRef: %w", err)
	}
	return nil
}

func (outbox Outbox) Validate() error {
	if err := outbox.Path.Validate(); err != nil {
		return err
	}
	if outbox.MaxBytes < minimumOutboxBytes ||
		outbox.MaxBytes > maximumOutboxBytes {
		return fmt.Errorf(
			"maxBytes must be between %d and %d",
			minimumOutboxBytes,
			maximumOutboxBytes,
		)
	}
	return nil
}

func (connector Connector) Validate(runtimeName string) error {
	arms := 0
	for _, present := range []bool{
		connector.WSL != nil,
		connector.SSH != nil,
		connector.Container != nil,
		connector.HTTPS != nil,
		connector.VendorCloud != nil,
	} {
		if present {
			arms++
		}
	}
	if arms != 1 {
		return errors.New("exactly one connector configuration is required")
	}
	switch connector.Type {
	case "wsl":
		if connector.WSL == nil || arms != 1 {
			return errors.New("type wsl requires only the wsl connector")
		}
		if runtimeName != "wsl" {
			return errors.New("wsl connector requires runtime wsl")
		}
		return connector.WSL.Validate()
	case "ssh":
		if connector.SSH == nil || arms != 1 {
			return errors.New("type ssh requires only the ssh connector")
		}
		if runtimeName != "ssh" {
			return errors.New("ssh connector requires runtime ssh")
		}
		return connector.SSH.Validate()
	case "container":
		if connector.Container == nil || arms != 1 {
			return errors.New("type container requires only the container connector")
		}
		if runtimeName != "container" {
			return errors.New("container connector requires runtime container")
		}
		return connector.Container.Validate()
	case "https":
		if connector.HTTPS == nil || arms != 1 {
			return errors.New("type https requires only the https connector")
		}
		if runtimeName == "wsl" {
			return errors.New("WSL must use the host-pull connector, not HTTPS")
		}
		return connector.HTTPS.Validate()
	case "vendor-cloud":
		if connector.VendorCloud == nil || arms != 1 {
			return errors.New(
				"type vendor-cloud requires only the vendorCloud connector",
			)
		}
		return ErrVendorCloudUnsupported
	default:
		return fmt.Errorf("unsupported connector type %q", connector.Type)
	}
}

func (connector WSLConnector) Validate() error {
	if !distributionPattern.MatchString(connector.Distribution) {
		return errors.New("distribution contains unsafe characters")
	}
	if err := validateExecutable(
		connector.HostExecutable,
		[]string{"wsl.exe"},
		"windows",
	); err != nil {
		return fmt.Errorf("hostExecutable: %w", err)
	}
	if err := validateExecutable(
		connector.RemoteExecutable,
		[]string{"agentbell"},
		"linux",
	); err != nil {
		return fmt.Errorf("remoteExecutable: %w", err)
	}
	return nil
}

func (connector SSHConnector) Validate() error {
	if err := validateHost(connector.Host); err != nil {
		return err
	}
	if connector.Port < 1 || connector.Port > 65535 {
		return errors.New("SSH port must be between 1 and 65535")
	}
	if !userPattern.MatchString(connector.User) {
		return errors.New("SSH user contains unsafe characters")
	}
	if err := validateExecutable(
		connector.HostExecutable,
		[]string{"ssh", "ssh.exe"},
		"",
	); err != nil {
		return fmt.Errorf("hostExecutable: %w", err)
	}
	if err := connector.KnownHostsFile.Validate(); err != nil {
		return fmt.Errorf("knownHostsFile: %w", err)
	}
	if connector.KnownHostsFile.Platform != connector.HostExecutable.Platform {
		return errors.New("knownHostsFile platform must match hostExecutable")
	}
	if err := validateExecutable(
		connector.RemoteExecutable,
		[]string{"agentbell"},
		"",
	); err != nil {
		return fmt.Errorf("remoteExecutable: %w", err)
	}
	return nil
}

func (connector ContainerConnector) Validate() error {
	if connector.Runtime != "docker" && connector.Runtime != "podman" {
		return fmt.Errorf("unsupported container runtime %q", connector.Runtime)
	}
	if !containerPattern.MatchString(connector.ContainerID) {
		return errors.New("containerId contains unsafe characters")
	}
	hostNames := []string{connector.Runtime, connector.Runtime + ".exe"}
	if err := validateExecutable(connector.HostExecutable, hostNames, ""); err != nil {
		return fmt.Errorf("hostExecutable: %w", err)
	}
	if err := validateExecutable(
		connector.RemoteExecutable,
		[]string{"agentbell"},
		"linux",
	); err != nil {
		return fmt.Errorf("remoteExecutable: %w", err)
	}
	return nil
}

func (connector HTTPSConnector) Validate() error {
	parsed, err := url.Parse(connector.Endpoint)
	if err != nil {
		return fmt.Errorf("parse HTTPS endpoint: %w", err)
	}
	if parsed.Scheme != "https" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Fragment != "" {
		return errors.New(
			"HTTPS endpoint requires https, a host, and no credentials or fragment",
		)
	}
	host := parsed.Hostname()
	if err := validateHost(host); err != nil {
		return fmt.Errorf("HTTPS endpoint: %w", err)
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return errors.New("HTTPS endpoint port is invalid")
		}
	}
	if connector.PinnedSPKI != "" &&
		!hexSHA256Pattern.MatchString(connector.PinnedSPKI) {
		return errors.New("pinnedSpki must be a lowercase SHA-256 hex digest")
	}
	return nil
}

func (reference PrivateKeyRef) Validate() error {
	switch reference.Store {
	case "keychain", "dpapi", "secret-service":
		if !tokenPattern.MatchString(reference.ID) {
			return errors.New("secret store id contains unsafe characters")
		}
		if reference.Path != nil || reference.FileFallbackAcknowledged {
			return errors.New("non-file secret reference cannot contain a file path")
		}
	case "file":
		if reference.ID != "" || reference.Path == nil {
			return errors.New("file key reference requires only path")
		}
		if err := reference.Path.Validate(); err != nil {
			return err
		}
		if !reference.FileFallbackAcknowledged {
			return errors.New("file key fallback requires explicit acknowledgement")
		}
	default:
		return fmt.Errorf("unsupported private key store %q", reference.Store)
	}
	return nil
}

func (config RelayConfig) Validate() error {
	if err := validateHeader(config.Version, config.MinCoreVersion); err != nil {
		return err
	}
	if err := config.Listener.Validate(); err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	if config.Peers == nil {
		return errors.New("peers must be an array")
	}
	ids := make(map[string]bool, len(config.Peers))
	keys := make(map[string]bool, len(config.Peers))
	origins := make(map[string]bool, len(config.Peers))
	for index, peer := range config.Peers {
		if err := peer.Validate(); err != nil {
			return fmt.Errorf("peers[%d]: %w", index, err)
		}
		if ids[peer.ID] {
			return fmt.Errorf("duplicate peer id %q", peer.ID)
		}
		if keys[peer.PublicKey] {
			return errors.New("duplicate peer public key")
		}
		originKey := peer.TeamID + "\x00" + peer.OriginID
		if origins[originKey] {
			return errors.New("duplicate peer team/origin")
		}
		ids[peer.ID] = true
		keys[peer.PublicKey] = true
		origins[originKey] = true
	}
	return nil
}

func (listener Listener) Validate() error {
	if !listener.Enabled {
		if listener.Address != "" || listener.TLS != nil || listener.SSHTunnel {
			return errors.New("disabled listener cannot contain network settings")
		}
		return nil
	}
	host, portText, err := net.SplitHostPort(listener.Address)
	if err != nil || host == "" {
		return errors.New("enabled listener address must use host:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("listener port must be between 1 and 65535")
	}
	if err := validateHost(host); err != nil {
		return err
	}
	if listener.TLS != nil {
		if err := listener.TLS.Validate(); err != nil {
			return err
		}
	}
	if !isLoopbackHost(host) && listener.TLS == nil && !listener.SSHTunnel {
		return errors.New(
			"non-loopback listener requires TLS or explicit sshTunnel",
		)
	}
	return nil
}

func (tls ListenerTLS) Validate() error {
	if err := tls.CertFile.Validate(); err != nil {
		return fmt.Errorf("certFile: %w", err)
	}
	if err := tls.KeyFile.Validate(); err != nil {
		return fmt.Errorf("keyFile: %w", err)
	}
	if tls.CertFile.Platform != tls.KeyFile.Platform {
		return errors.New("TLS certificate and key platforms must match")
	}
	if tls.CertFile.Value == tls.KeyFile.Value {
		return errors.New("TLS certificate and key paths must differ")
	}
	return nil
}

func (peer Peer) Validate() error {
	if err := validateIdentity("peer id", peer.ID); err != nil {
		return err
	}
	if peer.EnrollmentID != "" &&
		!hexSHA256Pattern.MatchString(peer.EnrollmentID) {
		return errors.New(
			"enrollmentId must be a lowercase SHA-256 hex digest",
		)
	}
	if err := validateIdentity("teamId", peer.TeamID); err != nil {
		return err
	}
	if err := validateIdentity("originId", peer.OriginID); err != nil {
		return err
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(peer.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("publicKey must be a base64url Ed25519 public key")
	}
	if err := validateUniqueStrings("scopes", peer.Scopes, func(value string) bool {
		return value == "ingest"
	}); err != nil {
		return err
	}
	if err := validateUniqueStrings(
		"allowedSources",
		peer.AllowedSources,
		event.IsKnownSource,
	); err != nil {
		return err
	}
	if err := validateUniqueStrings(
		"allowedRuntimes",
		peer.AllowedRuntimes,
		event.IsKnownRuntime,
	); err != nil {
		return err
	}
	return nil
}

func (reference PathRef) Validate() error {
	if reference.Platform != "darwin" &&
		reference.Platform != "linux" &&
		reference.Platform != "windows" {
		return fmt.Errorf("unsupported path platform %q", reference.Platform)
	}
	if reference.Value == "" || strings.ContainsAny(reference.Value, "\x00\r\n") {
		return errors.New("path value is empty or contains control characters")
	}
	if reference.Platform == "windows" {
		if !windowsAbsolute(reference.Value) {
			return errors.New("Windows path must be drive-absolute or UNC")
		}
	} else {
		if !strings.HasPrefix(reference.Value, "/") {
			return errors.New("POSIX path must be absolute")
		}
		if pathpkg.Clean(reference.Value) != reference.Value {
			return errors.New("POSIX path must be clean")
		}
	}
	for _, component := range splitPath(reference.Value) {
		if component == ".." || component == "." {
			return errors.New("path traversal components are forbidden")
		}
	}
	return nil
}

func validateHeader(version int, minimumCoreVersion string) error {
	if version != Version {
		return fmt.Errorf("unsupported sidecar version %d", version)
	}
	if !semanticVersionPattern.MatchString(minimumCoreVersion) {
		return errors.New("minCoreVersion must be a semantic version")
	}
	return nil
}

func validateIdentity(label, value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s contains unsafe characters", label)
	}
	return nil
}

func validateHost(host string) error {
	if host == "" || strings.ContainsAny(host, "\x00\r\n \t/@?#;|&") {
		return errors.New("host contains unsafe characters")
	}
	if net.ParseIP(host) != nil || hostLabelPattern.MatchString(host) {
		return nil
	}
	return errors.New("host is not a valid IP address or DNS name")
}

func validateExecutable(reference PathRef, names []string, platform string) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	if platform != "" && reference.Platform != platform {
		return fmt.Errorf("executable platform must be %s", platform)
	}
	base := pathBase(reference.Value)
	for _, name := range names {
		if strings.EqualFold(base, name) {
			return nil
		}
	}
	return fmt.Errorf("executable basename %q is not allowed", base)
}

func validateUniqueStrings(
	label string,
	values []string,
	allowed func(string) bool,
) error {
	if len(values) == 0 {
		return fmt.Errorf("%s must contain at least one value", label)
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !allowed(value) {
			return fmt.Errorf("%s contains unsupported value %q", label, value)
		}
		if seen[value] {
			return fmt.Errorf("%s contains duplicate value %q", label, value)
		}
		seen[value] = true
	}
	return nil
}

func windowsAbsolute(value string) bool {
	if strings.HasPrefix(value, `\\`) {
		parts := splitPath(value)
		return len(parts) >= 2
	}
	return len(value) >= 3 &&
		((value[0] >= 'A' && value[0] <= 'Z') ||
			(value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' &&
		(value[2] == '\\' || value[2] == '/')
}

func splitPath(value string) []string {
	return strings.FieldsFunc(value, func(character rune) bool {
		return character == '/' || character == '\\'
	})
}

func pathBase(value string) string {
	parts := splitPath(value)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
