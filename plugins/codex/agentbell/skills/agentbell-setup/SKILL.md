---
name: agentbell-setup
description: Install, bind, test, or diagnose AgentBell lifecycle notifications for Codex and Feishu.
---

# AgentBell Setup

M0.5 uses the native AgentBell Core and its structured Codex Adapter. The bundled Node hook is a
migration fixture and is not the production data path.

1. Verify Node.js 20 or newer.
2. Install and checksum-verify Core with `agentbell install-core`.
3. Run `agentbell adapter plan codex`, then `agentbell adapter install codex --dry-run`.
4. Before changing Hook configuration, show the plan and obtain the user's confirmation.
5. Install with `agentbell adapter install codex` and verify with
   `agentbell adapter verify codex`.
6. Use the official `lark-cli` for Feishu authentication and delivery; never copy or print its
   stored credentials.
7. Run `agentbell service run --foreground` and use `agentbell doctor --json` for diagnostics.
