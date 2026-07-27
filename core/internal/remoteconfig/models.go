package remoteconfig

import "errors"

const Version = 1

var (
	ErrNotFound               = errors.New("AgentBell remote sidecar not found")
	ErrVendorCloudUnsupported = errors.New(
		"vendor-cloud connector is unsupported without a verified outbound Hook capability",
	)
)

type PathRef struct {
	Platform string `json:"platform"`
	Value    string `json:"value"`
}

type RemoteConfig struct {
	Version        int           `json:"version"`
	MinCoreVersion string        `json:"minCoreVersion"`
	TeamID         string        `json:"teamId"`
	OriginID       string        `json:"originId"`
	Runtime        string        `json:"runtime"`
	Outbox         Outbox        `json:"outbox"`
	Connector      Connector     `json:"connector"`
	PrivateKeyRef  PrivateKeyRef `json:"privateKeyRef"`
}

type Outbox struct {
	Path     PathRef `json:"path"`
	MaxBytes int64   `json:"maxBytes"`
}

type Connector struct {
	Type        string                `json:"type"`
	WSL         *WSLConnector         `json:"wsl,omitempty"`
	SSH         *SSHConnector         `json:"ssh,omitempty"`
	Container   *ContainerConnector   `json:"container,omitempty"`
	HTTPS       *HTTPSConnector       `json:"https,omitempty"`
	VendorCloud *VendorCloudConnector `json:"vendorCloud,omitempty"`
}

type WSLConnector struct {
	Distribution     string  `json:"distribution"`
	HostExecutable   PathRef `json:"hostExecutable"`
	RemoteExecutable PathRef `json:"remoteExecutable"`
}

type SSHConnector struct {
	Host             string  `json:"host"`
	Port             int     `json:"port"`
	User             string  `json:"user"`
	HostExecutable   PathRef `json:"hostExecutable"`
	KnownHostsFile   PathRef `json:"knownHostsFile"`
	RemoteExecutable PathRef `json:"remoteExecutable"`
}

type ContainerConnector struct {
	Runtime          string  `json:"runtime"`
	HostExecutable   PathRef `json:"hostExecutable"`
	ContainerID      string  `json:"containerId"`
	RemoteExecutable PathRef `json:"remoteExecutable"`
}

type HTTPSConnector struct {
	Endpoint   string `json:"endpoint"`
	PinnedSPKI string `json:"pinnedSpki,omitempty"`
}

type VendorCloudConnector struct {
	Provider   string `json:"provider"`
	Capability string `json:"capability"`
	Endpoint   string `json:"endpoint"`
}

type PrivateKeyRef struct {
	Store                    string   `json:"store"`
	ID                       string   `json:"id,omitempty"`
	Path                     *PathRef `json:"path,omitempty"`
	FileFallbackAcknowledged bool     `json:"fileFallbackAcknowledged,omitempty"`
}

type RelayConfig struct {
	Version        int      `json:"version"`
	MinCoreVersion string   `json:"minCoreVersion"`
	Listener       Listener `json:"listener"`
	Peers          []Peer   `json:"peers"`
}

type Listener struct {
	Enabled   bool         `json:"enabled"`
	Address   string       `json:"address,omitempty"`
	TLS       *ListenerTLS `json:"tls,omitempty"`
	SSHTunnel bool         `json:"sshTunnel,omitempty"`
}

type ListenerTLS struct {
	CertFile PathRef `json:"certFile"`
	KeyFile  PathRef `json:"keyFile"`
}

type Peer struct {
	ID              string   `json:"id"`
	EnrollmentID    string   `json:"enrollmentId,omitempty"`
	TeamID          string   `json:"teamId"`
	OriginID        string   `json:"originId"`
	PublicKey       string   `json:"publicKey"`
	Scopes          []string `json:"scopes"`
	AllowedSources  []string `json:"allowedSources"`
	AllowedRuntimes []string `json:"allowedRuntimes"`
	Revoked         bool     `json:"revoked"`
}
