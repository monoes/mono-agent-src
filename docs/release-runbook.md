# Release runbook: signed update manifest

Every push to master still publishes a GitHub release (see `.github/workflows/release.yml`
and SECURITY.md, "Release governance"). Alongside it, CI builds, checks and signs what the
updater verifies from the public releases-only repo (`monoes/mono-agent`, the workflow-level
`RELEASES_REPO`; the code builds in the private `monoes/mono-agent-src` once the owner has
run `docs/release-repo-split-runbook.md`).
Releases are hands-free apart from the `release` environment approval.

## What is published

- the release assets and `SHA256SUMS`
- `manifest.json` (schema 1: version, release time, notes URL, and per-platform assets with
  `os`, `arch`, `name`, `url`, `sha256`, `size`, `kind` of `cli` or `app`). The browser
  extension zip, `monoagent-chrome-extension.zip`, is an asset too: `kind` `extension`, with no
  `os` or `arch`. Installers that pick by os/arch/kind never select it.
  `monoagentcli extension install` checks the zip against the signed manifest of the CLI's
  release. It uses that release's `SHA256SUMS.txt` only when the manifest has no extension
  entry (releases made before the extension was listed), and it says so in its output.
- `manifest.json.sig`: one line, `<key id> <base64 Ed25519 signature>`, made over the exact
  bytes of `manifest.json`. The key id is the first 8 bytes of the SHA-256 of the public key,
  hex encoded.

Assets, `manifest.json` and `manifest.json.sig` are uploaded together, so an unsigned
manifest never sits alone in the public release. Downloads are anonymous. The client pins
only the public key(s) and trusts nothing else.

## How CI signs

The signing key is the **environment secret `RELEASE_SIGNING_KEY`** of the `release`
GitHub environment. It exists nowhere else in CI.

1. `release-manifest` builds `manifest.json` and `SHA256SUMS` from the build artifacts
   (unsigned; it never sees the key).
2. `sign-manifest` runs only for a push to master, with `environment: release` (so it waits
   for the reviewer approval and the branch rule below) and `permissions: contents: read`.
   Steps run in this order, and only the last signing one has the key:
   - it reads the latest release in the **releases repo** (`gh release list --repo
     "$RELEASES_REPO"`, the current tag excluded). The release job has already published the
     tag in the releases repo, so the earlier releases there give the version floor. An API failure
     fails the job; an empty list is accepted only for the very first release;
   - it compiles `cmd/release-manifest` into `$RUNNER_TEMP` and flattens the artifacts (a
     duplicate file name fails the job instead of one file silently replacing the other);
   - the signing step runs only that binary, `release-manifest sign -key-env RELEASE_SIGNING_KEY
     -assets-dir ... -url-prefix https://github.com/$RELEASES_REPO/releases/download/$TAG/`. The
     secret is in the env of this one step. The tool refuses to sign if:
     - the manifest has an unknown field, or `expires_at` is present and is not RFC3339 or not
       in the future;
     - any asset url is not exactly the url prefix plus the asset name, or `notes_url` is not
       https under `github.com/$RELEASES_REPO/` or on `monoes.me`;
     - an asset name is listed twice, is missing, or its sha256 or size differs from the built file;
     - a built per-platform file is not listed;
     - the manifest version is not exactly the tag CI is releasing;
     - the version is lower than the latest release in the releases repo.
   - it runs `monoagentcli release verify` as a self-check against the pinned keys. If the
     signing key is **not** pinned in `internal/release/keys.go` the step **fails**, unless
     the repository variable `RELEASE_SIGNING_BOOTSTRAP` is `true` (see "Bootstrap" below).
   `manifest.json`, `manifest.json.sig` and the derived public key (`signing-pubkey.txt`, public)
   go up as the `signed-manifest` artifact.
3. `publish-releases-repo` (also `environment: release`) downloads the artifacts again and
   therefore **re-verifies everything before uploading**: `manifest.json.sig` against the pinned
   key (or, in bootstrap, the derived one); every manifest-listed file's sha256 and size
   against the signed `manifest.json` (`release-manifest check -strict`); and it uploads only
   the files the manifest lists, plus the manifest, the signature and a `SHA256SUMS`
   regenerated from those verified files (the unsigned `SHA256SUMS` is never uploaded). Any
   mismatch aborts. It writes to `RELEASES_REPO` with the secret `RELEASES_REPO_TOKEN` and fails
   rather than upload without the signature. Both this job and `sign-manifest` are no-ops when
   the secret `RELEASES_REPO_TOKEN` does not exist.

What these checks do and do not prove: the pre-sign check proves the manifest **matches the
build artifacts** that the workflow produced. It does not prove the build itself was not
compromised: a malicious change to the workflow or a build step produces artifacts and a
matching manifest that pass every check. The real controls are the `release` environment
approval and branch protection on master with code-owner review (see "Code owners" below).
Read the diff of `.github/`, `cmd/release-manifest/` and `internal/release/` before approving.

CI never overwrites `manifest.json` once `manifest.json.sig` exists on the release: the
upload step fails with a message instead. A new version always makes a new release, so this
only matters for a re-run of the same tag.

The `-key-env NAME` flag reads the base64 private key (64-byte key or 32-byte seed) from the
named environment variable. It is never accepted as a flag value, never printed, never written
to disk, and an empty variable is refused.

Optional expiry policy: the manifest job can pass `-expires-days N` to
`release-manifest manifest`, which adds a signed `expires_at` (RFC3339). Clients reject a
manifest whose `expires_at` is in the past. The default (0) omits the field.

Until `manifest.json.sig` exists, clients treat the manifest as unavailable and use the
GitHub API path (while `AllowLegacyGitHubUpdates` is true).

## One-time setup (owner)

1. Create the key straight into the secret. The private key never appears on screen or in
   shell history (keygen refuses to print it to a terminal); the public line is printed to
   stderr:

   ```bash
   monoagentcli release keygen --stdout-private | gh secret set RELEASE_SIGNING_KEY --env release --repo OWNER/REPO
   ```

   Create the environment first (step 3) if it does not exist, since `gh secret set --env`
   needs it. Nothing is stored in your keyring in this mode.
2. Copy the printed public line (`<keyid> <base64pub>`) into `pinnedReleaseKeys` in
   `internal/release/keys.go`, and its base64 half into `RELEASE_PUBKEY` in `install.sh`, and
   ship it in a release (see "Bootstrap"). Until a key is pinned no manifest is trusted by
   clients, and the CI self-check fails.
3. Protect the `release` environment. Find your numeric user id, then set the required
   reviewer and limit deployments to master:

   ```bash
   OWNER=your-github-login; REPO=monoes/mono-agent-src   # the repository the workflow RUNS in (before the rename of the split: monoes/mono-agent)
   ID=$(gh api users/$OWNER --jq .id)
   # required reviewer = the owner (self review stays allowed: you are the only maintainer)
   gh api -X PUT repos/$REPO/environments/release --input - <<JSON
   {"reviewers":[{"type":"User","id":$ID}],"prevent_self_review":false,
    "deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}
   JSON
   # deployment branches: master only
   gh api -X POST repos/$REPO/environments/release/deployment-branch-policies -f name=master -f type=branch
   ```

   `PUT` replaces the environment's settings, so run it once and then check
   Settings -> Environments -> release. Also keep branch protection on master (SECURITY.md).

## Bootstrap: the first release that ships the pin

Clients trust only keys pinned in their own build, and the CI self-check requires the signing
key to be pinned in the commit being released. For the very first signed release the key
exists (secret) before any build pins it, so that release is signed with the new key but
cannot self-verify against a pin. The owner opens that one gap by hand:

1. Set the repository variable (not a secret): `gh variable set RELEASE_SIGNING_BOOTSTRAP
   --body true --repo OWNER/REPO`.
2. Push the commit that pins the key. If the pin is already in that commit, the self-check
   passes on the pin and the variable is not needed; if the key is not pinned yet, the
   self-check and the publish job verify against the public key CI derived from the secret and
   print a warning.
3. Approve the `release` environment, let the release ship, then **unset the variable**:
   `gh variable delete RELEASE_SIGNING_BOOTSTRAP --repo OWNER/REPO`.

While the variable is true, an unpinned key passes the self-check, so leave it set only for
that one release. Once the key is pinned, a run with the variable still true fails with a message
telling you to delete it. Known limit: in bootstrap mode the publish job verifies against the
`signing-pubkey.txt` produced by sign-manifest and does not recompute the key from the secret,
because the secret is referenced in exactly one step by design; the environment approval and
branch rule are what protect that one release. Any later release with an unpinned key fails the job.

## Rotation

Clients only trust manifests signed by a key pinned in their own build, so the release that
introduces the new key must still be signed by the old one. Stage the new key first; do not
overwrite the secret yet.

Rotation is **not** done in the workflow: the staged key never enters CI before step 3, so
there is no workflow step to clean up. The staged key is a file on the owner's machine, the one
place a private key touches disk. Keep it in a directory only you can read, and delete it with
a trap so it goes away even if a step fails. Never commit it, attach it to an artifact or paste
it anywhere. (If rotation is ever automated in CI, the staged-key file must live in
`$RUNNER_TEMP`, be created under `umask 077`, be removed by an `if: always()` cleanup step and
never be uploaded.)

1. Generate the new key into a private file and note its public line (printed to stderr):

   ```bash
   umask 077
   keydir=$(mktemp -d "$HOME/scratch/release-key.XXXXXX")   # mode 0700
   trap 'rm -f "$keydir/next"; rmdir "$keydir"' EXIT        # delete on any exit
   monoagentcli release keygen --stdout-private > "$keydir/next"
   ```

   `keygen` warns on stderr that the redirect leaves the key on disk. Because the trap only
   lasts for this shell, run steps 1 and 3 in one session, or repeat the `umask`/`mktemp`
   in a new shell and delete the directory by hand at step 4.

2. Ship a release that pins BOTH keys (old and new) in `pinnedReleaseKeys`. CI signs it with
   the old key (still in the secret), which clients already trust.
3. Once that release is out, promote the new key and make it the signer:

   ```bash
   gh secret set RELEASE_SIGNING_KEY --env release --repo OWNER/REPO < "$keydir/next"
   ```

   `install.sh` pins one key (`RELEASE_PUBKEY`), and each release publishes it to the releases
   repository. Set it to the new key's base64 in the first release CI signs with the new key,
   or the installer refuses that release's manifest.

4. Delete the staged key: `rm -f "$keydir/next"; rmdir "$keydir"` (the trap does this on exit).
5. Later, when clients have moved past builds that only know the old key, ship a release
   (signed by the new key) that removes the old line from `pinnedReleaseKeys`.

## Incident: the secret may have leaked

A leaked key can sign manifests that every client pinning it accepts, and only a client update
revokes it. Move fast, in the same order as a rotation but pinning only the new key:

1. Stage a new key (step 1 of Rotation) and note its public line.
2. Ship a release that pins the new key and **removes the leaked key** from
   `pinnedReleaseKeys`. CI signs it with the leaked key (still in the secret), because that is
   the only signature existing clients accept. Approve it yourself, after checking
   `.github/workflows/release.yml` on master and the environment's deployment history for
   anything you did not do.
3. Immediately set the secret to the new key (Rotation step 3) and delete the staged file.
4. Clients that do not update to that release keep trusting the leaked key and need a manual
   install (installer / releases page). If the leak is also a workflow or reviewer
   compromise, fix that first: revoke the offending access, re-check the environment's
   required reviewers and deployment branches.

## Code owners

`.github/CODEOWNERS` assigns the release path (`/.github/`, `/cmd/release-manifest/`,
`/internal/release/`, `/cmd/monoagentcli/release*.go`, `/internal/secrets/release_key.go`, `/scripts/check-release-tags*.sh`, `/scripts/release-flatten.sh`,
`/docs/release-runbook.md`) to the owner. **It only takes effect when branch protection on
master requires code-owner review**; without that rule it is a label. Enable it (owner runs
this, it replaces the rule's review settings, so check Settings -> Branches afterwards):

```bash
gh api -X PATCH repos/OWNER/REPO/branches/master/protection/required_pull_request_reviews \
  -F require_code_owner_reviews=true -F required_approving_review_count=1
```

(`PATCH` needs the branch protection rule to already exist; create it first in Settings ->
Branches if it does not. CODEOWNERS entries must name a user or team: an organization name
alone is not a valid owner.)

GitHub does not count the author's own approval toward code-owner review. With a single owner,
a PR the owner opened cannot satisfy the rule alone. Choose one deliberately: (a) **recommended
until a second maintainer exists: use admin bypass** (leave "Do not allow bypassing the above
settings" off and merge as repository admin, which is an explicit, logged act), or (b) add a
second reviewer who is a code owner. Do not weaken the rule to avoid the choice.

## Break-glass: sign locally

The local path still works and is the fallback if CI signing is unavailable or untrusted:

```bash
# once: create a key in the OS keyring (pin the printed public key)
monoagentcli release keygen

gh release download vX.Y.Z --repo monoes/mono-agent --pattern manifest.json
go run ./cmd/release-manifest sign -manifest manifest.json -keyring     # or -key-file path/to/key
gh release upload vX.Y.Z manifest.json.sig --repo monoes/mono-agent
monoagentcli release verify manifest.json manifest.json.sig   # add --pubkey "<id> <b64>" to try another key
```

`-key-file` is refused unless its mode is 0600 or tighter (a symlink is judged by its target;
the check is skipped on Windows, which has no POSIX mode bits). `-keyring` reads the entry written by keygen (service `monoagent-release-signing`, account
`ed25519-v1`, value the base64 of the 64-byte private key; a bare 32-byte seed is accepted
too). Add `-assets-dir DIR -expect-version vX.Y.Z -url-prefix https://github.com/OWNER/REPO/releases/download/vX.Y.Z/
-min-version vA.B.C` to get the same asset checks as CI. Check `manifest.json` against the run's checksums before signing.

## Making this repo private

`AllowLegacyGitHubUpdates` must be set to false in the same release that makes this repo
private. Otherwise clients keep calling the GitHub API path against a repo they can no
longer read, and the signed manifest is bypassed as a fallback.
