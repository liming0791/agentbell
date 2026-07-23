---
name: agentbell-setup
description: Install, bind, test, or diagnose AgentBell lifecycle notifications for Claude Code and Feishu.
---

# AgentBell Setup

Claude Code is cataloged as Pilot, but its formal AgentBell Adapter is not implemented in M0.5.

1. Use `agentbell setup --plan` only for no-side-effect environment discovery.
2. Treat the bundled Node hook as an M0 protocol fixture, not a supported production install.
3. Do not modify Claude settings or claim lifecycle delivery is configured.
4. Use the official `lark-cli` for Feishu authentication and never copy or print its credentials.
5. Refer to the M1 roadmap for the structured install/verify/uninstall implementation.
