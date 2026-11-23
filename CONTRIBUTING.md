# Contributing to Mono Agent

Thanks for your interest in improving Mono Agent.

The source is private and this project does not accept source contributions;
the public repository publishes releases only. Bug reports and suggestions are
welcome in its Issues and Discussions. The rest of this file is the maintainers'
build and test guide.

## Prerequisites

- **Go 1.26** or newer (the version pinned in [`go.mod`](go.mod))

## Build & Test

```bash
# Build the CLI (produces ./monoagentcli)
go build ./cmd/monoagentcli

# Run the test suite
go test ./...

# Vet
go vet ./...
```

A new database migration must also be listed, as the last line, in
`data/migrations.txt` in the same change; `internal/storage`'s
`TestMigrationManifestListsEveryMigration` fails when the file and
`data/migrations` disagree.

### Social-platform nodes (built by default; `nosocial` opts out)

Social-platform browser bots and their action templates are compiled into
the default binary (files tagged `//go:build !nosocial`). The `nosocial` tag
builds without them — keep that build working too:

```bash
go build -tags nosocial ./...
go vet -tags nosocial ./...
go test -tags nosocial ./...
```

CI runs both modes — make sure your change builds and tests green in **both**
default (social included) and `-tags nosocial` modes.

### The monoes.me account gate

The account gate (`internal/account`) is live: a monoes.me sign-in has been
required since 2026-10-11 (UTC) and the production signing key is pinned.
`monoagentcli ref account` explains the behavior. A
`-tags devaccount` build trusts a development signing key and honors
`MONOAGENT_DEV_ENFORCE_FROM` (an RFC 3339 time) to move the enforcement date,
so the gate can be exercised against the fake monoes.me in
`internal/library/libraryfake`. Unit tests need no tag: `accounttest.Install` and
`account.SetTrustedKeysForTest` swap the keys and the clock, and panic outside a
test binary. To run the real binary, build it with the tag:

```bash
go build -tags devaccount -o /tmp/monoagentcli ./cmd/monoagentcli
go test -race -tags devaccount ./internal/account/...
go test -tags devaccount ./internal/accountsmoke/ -count=1 -timeout 25m   # real-binary smoke
```

Never build a release with that tag: CI's `release-guard` job
(`scripts/check-release-tags.sh`) fails the release if any shipped Go binary
lists it. Set `MONOAGENT_DEV_ENFORCE_FROM` for the process under test only: a
future date such as `2999-01-01T00:00:00Z` forces the warn period (nothing locks),
a past date forces enforcement, and it never relaxes anything. A default build
ignores it. Scripts and CI jobs that run a gated command without testing the gate
should build with the tag and set a date that never comes.

## Code Style

- Format with `gofmt` (or `go fmt ./...`) before submitting — CI and review
  expect canonical formatting.
- Match the style of the package you are touching; keep files under ~500 lines.
- No new dependencies without discussion — prefer the standard library.

## Pull Requests

- **Small diffs.** One logical change per PR; split unrelated work into
  separate PRs.
- **Tests required** for new CLI commands, node types, and bug fixes
  (regression test first where practical). Tests must not require Chrome or
  network access.
- Describe what changed and why; link any related issues.
- Verify before opening: `gofmt` clean, `go build ./...`,
  `go test ./...`, and the same with `-tags nosocial`.

## Contribution terms

Mono Agent is proprietary software (see `LICENSE`) and does not accept source
code contributions. Bug reports, suggestions and other feedback are welcome;
by sending them you let Nokhodian UG (haftungsbeschränkt) use them without
restriction and without payment (section 6 of `LICENSE`).

## Security

Found a security issue? Please do not open a public issue — see
[SECURITY.md](SECURITY.md) for how to report it responsibly.

## Adding a locale

There's a pilot i18n pipeline for GUI and CLI help text (issue #26) — see
[docs/i18n.md](docs/i18n.md) for the architecture and current (bounded)
coverage. Extending an existing translated string, or adding a whole new
language, is mechanical:

### CLI (`internal/i18n`)

1. Copy `internal/i18n/locales/en.json` to `internal/i18n/locales/<lang>.json`
   (use the language's ISO 639-1 code, e.g. `fr`, `de`, `ja`) and translate
   every value — keep the keys identical.
2. To translate a *new* string that isn't wrapped yet: replace the literal
   in the relevant `cmd/monoagentcli/*.go` file with `i18n.T("your.key")`,
   add `"your.key"` to `locales/en.json` with the English text, then add the
   same key to every other locale file (or leave it out — missing keys
   silently fall back to English via `T()`, they just won't be translated
   yet).
3. `go test ./internal/i18n/...` — the `TestLocaleKeys_CoverCodeReferences`
   test walks `cmd/monoagentcli/*.go` for `i18n.T("...")` calls and fails if
   any referenced key is missing from `en.json`. This catches typos, not
   full coverage: an unused key in a locale file is fine, an `i18n.T()` call
   referencing a key that doesn't exist in English is what it flags.
4. Try it: `go build ./cmd/monoagentcli && ./monoagentcli --lang <lang> --help`
   (or `MONOAGENT_LANG=<lang>`).

### GUI (`wails-app/frontend`)

1. Copy `wails-app/frontend/src/locales/en.json` to
   `wails-app/frontend/src/locales/<lang>.json` and translate every value —
   keep the nested key structure identical.
2. Register it in `wails-app/frontend/src/i18n.js`: import the new JSON
   file and add it to the `resources` object (e.g.
   `fr: { translation: fr }`), and add an `<option>` for it in the language
   `<select>` in `wails-app/frontend/src/pages/Settings.jsx`.
3. To translate a *new* string that isn't wrapped yet: in the component,
   call `const { t } = useTranslation()` and replace the literal with
   `t('your.namespace.key')`; add the key (nested under its
   component/page namespace) to `locales/en.json` and every other locale
   file you're maintaining.
4. `npm run build` and `npm test -- --run` in `wails-app/frontend` — both
   must pass. There's no automated locale-completeness check on the GUI
   side yet (unlike the CLI's `TestLocaleKeys_CoverCodeReferences`); a
   missing key falls back to `i18next`'s `fallbackLng` (English) at
   runtime rather than failing a build.

## Sharing a workflow template

The workflow marketplace's distribution plumbing isn't built yet, but its
curation policy is — read
[docs/plans/workflow-marketplace-curation-policy.md](docs/plans/workflow-marketplace-curation-policy.md)
before preparing a template for submission. In short: every template needs
a manifest declaring its node types, external calls, required credential
kinds, and an explicit license (MIT recommended for broad reuse); templates
using `core.code`, `system.execute_command`, `http.ssh`, or any `db.*` node
get a mandatory security review of the exact code before publication.
