// Package remote executes AgentBell remote relay connectors.
//
// WSL, SSH and container connectors use an argv-only host-pull process and the
// relay package's bounded stdio frame protocol. HTTPS uses the same signed
// envelope through a TLS client with optional SPKI pinning. The package never
// logs or renders envelope bodies, signatures, peer keys or connector targets.
package remote
