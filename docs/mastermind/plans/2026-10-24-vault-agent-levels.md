# Vault Agent Levels Implementation Plan

> For agentic workers: use mastermind-execute to implement task by task.

**Goal:** Provide Strict, Use without revealing, and Reveal in CLI and UI per profile.

**Architecture:** Extend the existing profile_agent_secret_access grant with a level column, defaulting existing rows to use. Read policy at each access. Keep the operator gate for mutations. CLI is the desktop backend.

**Tech Stack:** Go, SQLite, React, Wails.

## Global Constraints

Preserve unrelated workspace edits. Strict by default. Existing grants remain use. No plaintext persistence. No changes to authentication-token restrictions.

- [x] Extend internal/secrets/agent_access.go and migration 125 with grant level, validation, reveal authorization and name-only audit; test revocation, expiry, scope, profiles and reserved entries.
- [x] Extend internal/cli/secret/agent_access.go with strict/use/reveal and retain on/off compatibility. Gate reveal in internal/cli/secret/secret.go using profile policy; test CLI permission matrix and operator-only changes.
- [x] Apply policy to standalone steps and vault.secret_get in internal/workflow/approvals.go, internal/cli/node/run.go, internal/mcp/node_run.go and internal/nodes/vault/secret_get.go; test runtime permission matrix.
- [x] Add CLI-backed Wails methods in wails-app/app_vault.go, generated bindings and a VaultAgentAccess component in the Vault page; test loading, selection, confirmation, failure and saving.
- [x] Update offline documentation and SECURITY.md. Run focused Go tests and frontend tests/build, inspect diff and report limitations.

Validation: secrets, CLI secret/node, vault nodes, MCP, profile context and command catalog Go tests; 20 focused frontend tests; targeted Wails binding tests; frontend production build. Workflow suite rerun after updating its explicit migration fixture.
