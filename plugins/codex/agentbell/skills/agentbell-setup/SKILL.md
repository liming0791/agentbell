---
name: agentbell-setup
description: Install, bind, test, or diagnose AgentBell lifecycle notifications for Codex and Feishu.
---

# AgentBell Setup

AgentBell uses the native Go Core and its structured Codex Adapter. The bundled Node hook is a
migration fixture and is not the production data path. In the current M1 development build,
`agentbell setup` is the preferred interactive path (macOS validated): it detects agents, guides
lark-cli install/auth, creates or picks the Feishu chat, writes the platform-dir config, offers the
Codex hook install, and can install the platform login service. These M1 commands are not part of
the published v0.2.0-rc.3 Core.

1. Verify Node.js 20 or newer.
2. Install and checksum-verify Core with `agentbell install-core`.
3. Prefer `agentbell setup` (use `--dry-run` first to preview the plan).
4. For the granular path, run `agentbell adapter plan codex`, then
   `agentbell adapter install codex --dry-run`.
5. Before changing Hook configuration, show the plan and obtain the user's confirmation.
6. Install with `agentbell adapter install codex` and verify with
   `agentbell adapter verify codex`.
7. Review and trust a new or changed non-managed Stop hook in Codex `/hooks`, start a new task,
   complete one turn,
   then require `agentbell adapter diagnose codex` to report `runtimeVerified: true`.
8. Use the official `lark-cli` for Feishu authentication and delivery; never copy or print its
   stored credentials.
9. Use `agentbell service install` and `agentbell service status` for the native macOS
   LaunchAgent, Windows current-user logon task, or Linux systemd-user/XDG login service. Use
   `service run --foreground` only for temporary debugging. Send a probe with `agentbell test`
   and use `agentbell doctor --json` for diagnostics.
10. Use top-level `agentbell uninstall --dry-run` then `agentbell uninstall` for product-level
    removal of the login service, every supported hook, and the bootstrap-managed Core version;
    config and queue are retained.
