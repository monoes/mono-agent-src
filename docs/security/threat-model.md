# Threat model

Mono Agent runs entirely on the user's machine but holds privileges most
local tools don't: it can decrypt browser cookies, drive logged-in Chrome
tabs, evaluate JavaScript in page context, and attach the Chrome DevTools
Protocol debugger. This document names the assets those privileges expose,
the boundaries between components, who's assumed capable of attacking them,
and what is deliberately out of scope. It should be revisited whenever a new
privileged integration is added (a new browser bridge command, a new secrets
backend, a new subprocess dependency).

## Assets

| Asset | Where it lives | Why it matters |
| --- | --- | --- |
| Live browser session cookies | Read via `chrome.cookies.getAll` through the extension bridge, transiently in Go process memory | Equivalent to account takeover for whatever site they belong to |
| Vault secrets (API keys, logins) | `internal/secrets`, AES-256-GCM at rest, DEK wrapped by an OS-keyring KEK | Direct credential exposure if the vault or keyring is compromised |
| Extension pairing/relay token | `~/.monoagent/extension.token`, mode 0600 | Whoever holds it can impersonate the extension connection (MA-01) or relay commands through a running daemon |
| Per-site browser automation authority | Chrome's `optional_host_permissions` grants (MA-05), tracked by the extension | Bounds which sites automated commands (cookies, eval, debugger, DOM) can touch |
| Workflow definitions & execution history | SQLite DB under `~/.monoagent/` | Can encode credentials-adjacent logic (e.g., which sites are automated, HTTP node bodies) |

## Trust boundaries

```
Go process (monoagentcli/daemon)
  │
  ├─ loopback WebSocket ──── Chrome Extension (background.js)
  │  (internal/extension)         │
  │  auth: extension.token        ├─ content script ── page DOM (per-site grant)
  │  (MA-01)                      ├─ chrome.cookies API (per-site grant, MA-05)
  │                               └─ chrome.debugger API (NOT permission-scoped by
  │                                  Chrome — gated by explicit allowlist check, MA-05)
  │
  ├─ npx subprocess ──────── Monomind MCP server (pinned exact version, MA-04)
  │
  └─ OS keyring / vault DB ── secrets at rest (internal/secrets)
```

Each `──` above is a boundary an attacker on the same machine, or controlling
one endpoint, might try to cross:

1. **Loopback bridge (Go ↔ extension).** Binds 127.0.0.1 only, validates
   WebSocket `Origin`, and — since MA-01 — requires the extension token as
   the first frame before a connection is treated as the real extension.
   Origin/loopback checks are defense-in-depth, not the primary control:
   they stop a hostile web page, not a hostile local process, which is why
   token auth exists on top of them.
   **Multiple browsers.** The bridge keeps one authenticated socket per
   browser profile, keyed by an instance id the extension reports. All of
   them authenticate with the same pairing token, so a token holder can
   already drive every paired browser; binding adds routing, not a new
   capability. `/monoagent/health` (unauthenticated) reports only a browser
   count; which profile and label each browser has is behind the token at
   `/monoagent/browsers` and `/monoagent/resolve`.
   A browser runs a set of profiles (at most 64, each id at most 128 bytes).
   A reported profile id that `profiledir.ValidProfileID` rejects, a
   duplicate, and an id beyond the cap are dropped, and ids of deleted
   profiles leave the set. A command for a profile is never sent to a
   browser whose set lacks that profile (except to an unbound default
   browser); a profile removed from a browser stops routing there before the
   extension answers.
   Known limits, stated plainly: a paired browser (or any holder of the
   bridge token) can claim any valid profile id, and with sets it can claim
   up to 64 at once and receive those profiles' commands and session data.
   That widens the blast radius of one mis-paired or compromised browser from
   one profile to many; it is not a new trust boundary. The real mitigation,
   binding a browser's token to an allowed set of profiles at pairing time,
   is not part of this change. The CLI's operator-only guards (`bind`,
   `unbind`, `assign`) stop an agent shell that does not hold the bridge
   token; a caller who can read the token can already send `set_binding`
   directly, as on master.
2. **Extension ↔ page.** Content-script and cookie/debugger access is scoped
   to explicitly granted origins (MA-05) rather than `<all_urls>` by
   default. `chrome.debugger` is called out separately because Chrome does
   not gate it by host permission the way it gates cookies/scripting — the
   extension enforces the allowlist itself before every debugger-backed
   command.
3. **Go ↔ Monomind subprocess.** Pinned to an exact, canonical package
   version (MA-04) rather than `npx -y <pkg>@latest`, so a future package
   publish cannot execute locally without a reviewed version bump.
4. **Vault ↔ OS keyring.** Data-encryption keys never touch disk unwrapped;
   the file-keyring fallback (see SECURITY.md) is opt-in and explicitly
   documented as weaker.
5. **CI/release pipeline.** GitHub Actions pinned to commit SHAs (MA-06),
   release artifacts carry SLSA build provenance (MA-13), and publication is
   gated behind a protected environment (MA-09) — see SECURITY.md for the
   exact repo settings this depends on.

## Attacker models considered

- **Hostile web page.** Can it reach the loopback bridge or read data it
  shouldn't? Mitigated by loopback binding + Origin checks + (now) token
  auth on the WebSocket itself.
- **Hostile local process (no special privilege).** Can it impersonate the
  extension, read the relay token, or ride along on an existing connection?
  Mitigated by the extension token (file mode 0600) and the MA-01 handshake,
  which never lets an unauthenticated socket replace an authenticated one.
- **Compromised npm dependency (Monomind or a transitive package).** Can it
  execute unreviewed code the next time the MCP server starts? Mitigated by
  exact-version pinning (MA-04) plus Dependabot/`govulncheck`/`npm audit` in
  CI (MA-06/MA-11).
- **Compromised or malicious GitHub Actions workflow / CI runner.** Can it
  publish a tampered release? Mitigated by SHA-pinned actions (MA-06),
  environment-gated publication (MA-09), and build provenance attestation
  (MA-13) so a downloaded artifact's origin is independently checkable.
- **Compromised Chrome extension itself** (e.g. a supply-chain attack on the
  unpacked extension files, or a bug that lets page script reach background
  state). Bounded, not eliminated, by per-site permission scoping (MA-05):
  the blast radius is the sites the user has explicitly authorized, not
  every site they've ever visited.

## Non-goals

- **Protecting against a fully compromised OS or an attacker with root.**
  If the attacker can read arbitrary process memory or the keyring
  unconditionally, no local-first tool defends against that.
- **Protecting against the user deliberately running untrusted workflows.**
  Importing/running a workflow is equivalent to executing code (see
  SECURITY.md's Resource limits section) — that's a capability, not a bug.
- **Defending the sites Mono Agent automates against their own
  vulnerabilities.** Out of scope per SECURITY.md.
- **A remote/multi-machine deployment threat model.** The extension bridge
  explicitly refuses non-loopback servers by default (an unsafe,
  session-only override exists for advanced use) — remote automation is not
  a supported configuration this document reasons about.

## Recommended user posture

Run Mono Agent's paired Chrome extension in a **dedicated automation browser
profile**, not your everyday personal/work profile — this bounds the blast
radius of a compromised extension or a workflow that automates the wrong
site to that profile's sessions, not your primary one. If you suspect the
extension pairing token has leaked, run `monoagentcli extension reset` and
re-pair.
