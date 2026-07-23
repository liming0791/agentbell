---
name: agentbell-setup
description: Install, bind, test, or diagnose AgentBell lifecycle notifications for Kimi Code CLI and Feishu.
---

# AgentBell Setup

Kimi Code is cataloged as Pilot, but its formal AgentBell Adapter is not implemented in M0.5.

1. Use `agentbell setup --plan` only for no-side-effect environment discovery.
2. Treat the bundled Node hook and plugin manifest as M0 protocol fixtures.
3. Do not install or claim verified lifecycle delivery from these fixtures.
4. Use the official `lark-cli` for Feishu authentication and never copy or print its credentials.
5. Refer to the M1 roadmap for the formal Kimi Hook dialect implementation.
