---
name: agentbell-setup
description: Install, bind, test, or diagnose AgentBell lifecycle notifications for Codex and Feishu.
---

# AgentBell Setup

Use the AgentBell CLI for setup and diagnostics. Keep installation explicit and fail open.

1. Verify Node.js 20 or newer.
2. Run `agentbell doctor`.
3. Preview changes with `agentbell setup --plan`.
4. Before installing missing software or changing Hook configuration, show the planned commands and obtain the user's confirmation.
5. Use the official `lark-cli` for Feishu configuration, authentication, and message delivery.
6. Never copy or print stored Feishu credentials.
7. After configuration, send a test notification and report which CLI events are enabled.

