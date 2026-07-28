---
name: agentbell-setup
description: Install, bind, test, or diagnose AgentBell lifecycle notifications for Qoder and Feishu.
---

# AgentBell Setup

Qoder is cataloged as Pilot. Its native Go Adapter is implemented in the current M1
development build for CLI, IDE and JetBrains plugin; it is not part of the published
v0.2.0-rc.3 Core.

1. Use `agentbell setup --plan` only for no-side-effect environment discovery.
2. Prefer `agentbell setup`; for the granular path use `agentbell adapter plan qoder`,
   `agentbell adapter install qoder --dry-run`, then install and verify.
3. The Adapter structurally merges exec-form hooks into the user-level
   `$QODER_CONFIG_DIR/settings.json` (default `~/.qoder/settings.json`) for Stop and
   PostToolUseFailure. CLI, IDE and JetBrains plugin share it.
4. Complete a new session and require `agentbell adapter diagnose qoder` to report
   `runtimeVerified: true`. Restart the session only if Qoder's settings watcher missed the edit.
5. Treat the bundled Node hook as an M0 protocol fixture, not the production data path.
6. Use the official `lark-cli` for Feishu authentication and never copy or print its credentials.
7. Use `agentbell adapter uninstall qoder` for precise removal, or
   `agentbell adapter uninstall all` to remove all supported product hooks.
8. Use `agentbell service install` and `agentbell service status` for the native macOS
   LaunchAgent, Windows current-user logon task, or Linux systemd-user/XDG login service.
9. Use top-level `agentbell uninstall --dry-run` then `agentbell uninstall` for product-level
   removal of the login service, every supported hook, and the bootstrap-managed Core version;
   config and queue are retained.
