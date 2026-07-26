---
name: agentbell-setup
description: Install, bind, test, or diagnose AgentBell lifecycle notifications for Kimi Code CLI and Feishu.
---

# AgentBell Setup

Kimi Code is cataloged as Pilot. Its Go Core Adapter is implemented and macOS-CLI validated
(2026-07-25) in the current M1 development build; it is not part of the published v0.2.0-rc.3
Core. Windows/Linux real-machine validation is pending.

1. Use `agentbell setup --plan` only for no-side-effect environment discovery.
2. `agentbell setup` (M1 slice 1) binds the Feishu channel, writes the shared config, and can
   install the Kimi Code hooks; the granular path is `agentbell adapter plan kimi-code`,
   `agentbell adapter install kimi-code --dry-run`, then `agentbell adapter install kimi-code`
   and `agentbell adapter verify kimi-code`.
3. The Adapter appends a marker-delimited `[[hooks]]` region to `$KIMI_CODE_HOME/config.toml`
   (default `~/.kimi-code/config.toml`) for Stop, StopFailure and PermissionRequest; entries carry
   only event/command/timeout. Hooks load at session start — close the old session and start a new
   Kimi session after install.
4. Treat the bundled Node hook and plugin manifest as M0 protocol fixtures.
5. Use the official `lark-cli` for Feishu authentication and never copy or print its credentials.
6. Complete a turn in the new session and require `agentbell adapter diagnose kimi-code` to report
   `runtimeVerified: true`. Use `agentbell service install` and `agentbell service status` for the
   native macOS LaunchAgent, Windows current-user logon task, or Linux systemd-user/XDG login
   service.
7. Use top-level `agentbell uninstall --dry-run` then `agentbell uninstall` for product-level
   removal of the login service, every supported hook, and the bootstrap-managed Core version;
   config and queue are retained.
