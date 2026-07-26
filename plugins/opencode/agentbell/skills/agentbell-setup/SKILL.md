---
name: agentbell-setup
description: Install, bind, test, or diagnose AgentBell lifecycle notifications for OpenCode and Feishu.
---

# AgentBell Setup

OpenCode is cataloged as Pilot. Its native Go Adapter is implemented in the current M1
development build for CLI, TUI and Desktop; it is not part of the published
v0.2.0-rc.3 Core.

1. Use `agentbell setup --plan` only for no-side-effect environment discovery.
2. Prefer `agentbell setup`; for the granular path use `agentbell adapter plan opencode`,
   `agentbell adapter install opencode --dry-run`, then install and verify.
3. The Adapter writes a global plugin to `~/.config/opencode/plugins/agentbell.js`
   (or `$OPENCODE_CONFIG_DIR/plugins/agentbell.js`). The plugin subscribes to
   `session.idle`, `session.error` and `permission.asked` events. CLI, TUI and
   Desktop share it.
4. Restart OpenCode after installation and complete a session, then require
   `agentbell adapter diagnose opencode` to report `runtimeVerified: true`.
5. Treat the bundled Node hook as an M0 protocol fixture, not the production data path.
6. Use the official `lark-cli` for Feishu authentication and never copy or print its credentials.
7. Use `agentbell adapter uninstall opencode` for precise removal, or
   `agentbell adapter uninstall all` to remove all supported product hooks.
8. Use `agentbell service install` and `agentbell service status` for the native macOS
   LaunchAgent, Windows current-user logon task, or Linux systemd-user/XDG login service.
9. Use top-level `agentbell uninstall --dry-run` then `agentbell uninstall` for product-level
   removal of the login service, every supported hook, and the bootstrap-managed Core version;
   config and queue are retained.
