# Agent Context Bundle + Auto-Capture Implementation Plan

> For agentic workers: use mastermind-execute to implement task by task. Status: proposal, nothing built.

**Goal:** Every chat, on every runtime, knows the app (what exists, where each kind of asset belongs, how to use the tools) and anything it works with is saved to the right place, tagged `draft` until the user confirms.

**Architecture:** One CLI-owned *context bundle* resolved at chat start = layered instructions (the CLAUDE.md equivalent) + MCP registry + a generated, locked *App map*. A post-turn *capture pass* files what the chat produced into the right domain as drafts. The GUI only renders and shells out to the CLI.

**Tech stack:** Go, SQLite, React, Wails. Owning repo: mono-agent (bundle, registry, capture, GUI). monomind only if the runner needs a new pass-through for system prompt or MCP config.

## Global constraints

- Every call names its profile; the bundle is per profile, never an "active" profile.
- Secret values never enter the bundle, snapshot, transcript store or logs. Names and counts only.
- Synced mail, captures and web text are untrusted. The capture pass treats the transcript as untrusted; drafts exist so an injected "save this" cannot land as final data.
- Media is **media vault** (image, audio, video). The media vault itself is built in another session; this plan only targets its save API and must not touch the Vault/Media page.
- Tests never run `connect save` and use a scratch HOME (`~/scratch`).

## 1. Context bundle (the CLAUDE.md equivalent)

Resolved by the CLI (`chat`, coder sessions, org runs all call the same resolver), passed to the runtime through `SystemPrompt` and its MCP config.

Layers, later wins on conflict:
1. App map (generated, locked, always present; user cannot disable it)
2. Global instructions `~/.monoagent/instructions.md`
3. Profile instructions
4. Chat-kind addendum (default / coder / org)
5. Coder only: the repo's own `AGENTS.md`/`CLAUDE.md`, read, never overwritten

CLI: `monoagentcli instructions get|set|edit [--profile]`, `monoagentcli context show --profile P [--kind K]` (prints the resolved bundle, token count per layer; the GUI "context in use" popover calls it).

## 2. App map (generated, always on)

Built from the command catalog (`internal/cli/ref/catalog`) plus a per-domain descriptor, so it cannot drift. Per domain: what it is, what belongs there, the exact save/read command and MCP tool, the draft rule, and what must never go there.

| Domain | Belongs here | Notes |
|---|---|---|
| People | any person/handle/profile URL met in work; contact facts | `profile_details` for extras, bios to `about`; `category` is review state |
| Communication | messages, mail accounts, templates, drafts | drafts exist already; untrusted fences stay |
| Applications | the Applications pane (left sidebar): discover, evaluate and apply functions and their records | `internal/cli/application`; page `Applications.jsx` |
| Publications | anything posted/published, with platform, url, workflow/agent | `publication register` |
| Tasks | work items the user must do or review, and issues with no repo of their own | task board; `inbox` is the draft state. Issue routing below |
| Documents | profile folder documents and captures; always indexed and searchable | inventory is always in the bundle |
| Media vault | image, audio, video the chat generates or downloads | save, then link to the person/task/publication it belongs to |
| Vault (secrets) | passwords, tokens, keys | never in chat, files or the bundle; access level per profile |
| Profile info | facts about the user or their business (the Info tab) | `profile info set`; section 5 |
| Workflows / Orgs | automations and agent teams | validate before run |

Plus a **state snapshot** per profile: counts and the N most recent names per domain, so a chat sees "Ana already exists" and updates instead of duplicating. Bounded size, names only.

## 3. Capture: how "100% awareness" is enforced, not hoped for

Prompting alone fails (chats already skip saves). Four independent mechanisms:

1. **Save tools in the registry**, one per domain, upsert with dedupe (handle, URL, content hash): `person_save`, `media_save`, `vault_store`, `publication_save`, `task_save`, `profile_info_propose`, `document_save`. Each writes `draft` + provenance (`source=chat`, chat id, turn, runtime).
2. **Post-turn capture pass** (same pattern as `internal/cli/capturehooks`): deterministic extraction over transcript and tool outputs. Finds new handles/profile URLs, produced or downloaded files, secret-shaped strings, mentioned posts, action items. Anything not already saved is saved as a draft. Idempotent via the same dedupe keys.
3. **Review card** "Saved from this chat": per item confirm, move to another domain, or undo. Confirm clears `draft`.
4. **Conformance suite**: a scripted-chat fixture set (stub runtime) with one scenario per domain asserting the asset lands in the right place, tagged draft, once. CI fails if a domain in the catalog has no App-map row or no scenario.

Draft state per domain (reuse what exists, add only where missing):
- People: `category` review state. Tasks: `inbox`. Messages: existing drafts.
- Media, vault entries, publications, documents, profile info: new `draft` flag + provenance columns (migration, numbered after the current latest; check for a duplicate number on master first).
- A draft vault secret is stored but is **not usable by agents** until confirmed. The capture pass never writes a secret value on its own: it prompts "store in vault?" and redacts the value from the saved transcript.

## 4. MCP registry

Global + profile scope. Entry: name, command or URL, args, env as vault references (never inline), allow/deny tool list, enabled per chat kind, usage note (feeds the App map "Connected tools" block). monoagent's own server is built in and locked. Mutating tools go through HIL. CLI: `monoagentcli mcp add|list|remove|enable|disable|test`; `test` is never run by automated tests that could reach a real service.

## 5. Profile info (user / business)

Profile info already lives in the Info tab (`internal/profileinfo`, `profile info get|set|rm`, `Info.jsx`; entries carry a `source` of `user` or `ai`, and an AI fill exists). Rare case: while working, the agent learns something about the user or the profile's business (name, role, company, offer, audience, tone). It calls `profile_info_propose`, which writes through `profileinfo.Set` with source `ai`, tagged draft. It may fill empty fields only; an entry with source `user` is never overwritten, and a change to one is a proposal the user approves in the Info tab. The profile's field catalog (`GetProfileInfoFields`) goes into the App map so the agent knows which fields exist and which are empty. Confirmed entries are included in the bundle; secret-class fields stay masked as in `Get(reveal=false)`.

## Issue and to-do routing (Tasks domain rule)

Where a chat files an issue or follow-up depends on what it is working in:
1. Work on mono-agent itself: issues go to `monoes/mono-agent`.
2. A coder chat in a folder that is a GitHub repo: issues go to that repo's own tracker (origin remote), never to mono-agent.
3. Anything else, or no repo: a task on the task board (`task_save`, `inbox`).

The coder bundle resolves the folder's origin remote and states the chosen target in the App map, so the chat does not guess. Filing to GitHub is an outward action: the chat proposes it and HIL/the user confirms; the task-board fallback needs no approval.

## 6. Documents

Always present: the bundle carries the profile's document inventory (titles, tags, counts) and the instruction to search before answering questions about the user's business, using the existing profile knowledge search. Excerpts keep the existing untrusted-content fencing. Chat-produced documents are saved with `document_save` as drafts.

## Phases and tasks

**Phase 1: instructions + App map (read-only, ships alone)**
- [ ] 1. Instruction layers store + `instructions` and `context show` commands (new package under `internal/cli/`, tests for layer order, profile scoping, size caps).
- [ ] 2. Domain descriptors + App-map generator from the catalog; snapshot builder (counts/names, no secrets); test that every catalog domain has a row.
- [ ] 3. Wire the resolver into `chat`, coder session start, org runs; coder reads the repo's file but never writes it; test per runtime.
- [ ] 4. GUI: Settings "Agent context" page (Instructions tab) + chat "context in use" popover, CLI-backed; browser test via `wails dev`.

**Phase 2: MCP registry + save tools**
- [ ] 5. Registry storage + `mcp` commands + runtime config pass-through (extend `internal/cli/integrations/mcpregister.go`); vault-reference env; HIL on mutations.
- [ ] 6. Save tools per domain with upsert/dedupe/draft/provenance; migration for the draft flag; profile-info storage decision.
- [ ] 7. GUI Connections tab; "Connected tools" block in the App map.

**Phase 3: capture pass + review** (all chat kinds: default, coder, org)
- [ ] 8. Post-turn extractor (people, media, secrets-as-prompt, publications, tasks, documents), idempotent, untrusted-input safe.
- [ ] 9. "Saved from this chat" review card (confirm / move / undo) in chat and per-domain draft filters.
- [ ] 10. Conformance suite (one scenario per domain, plus an injection scenario that must produce only drafts and no secret values).

**Phase 4: docs**
- [ ] 11. AGENTS.md sections (context bundle, MCP registry, capture), `ref` entries, SECURITY.md note on secrets and untrusted text.

## Validation

Focused Go tests per package, the conformance suite, frontend tests and build, a `wails dev` browser pass over the Agent context page and review card, and one live check in a scratch HOME with a stub runtime. Unverified until run: behaviour on each real runtime (claude, codex, kilo, pi, grok), since each takes system prompt and MCP config differently.

## Open questions

- Resolved 2026-10-28: Applications = the left-pane Applications functions; profile info = the Info tab; the capture pass runs for orgs too (default, coder, org); issues for this work go in `monoes/mono-agent` (epic plus one child per phase), runner work, if any, in `monoes/monomind`.
