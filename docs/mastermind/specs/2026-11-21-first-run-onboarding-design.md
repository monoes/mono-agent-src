# First-run onboarding: design

Date: 2026-11-21. Status: approved by the owner 2026-11-21.
Approved visual reference: `docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html`
(clickable; open it in a browser). Step script and copy:
`docs/mastermind/specs/onboarding-mockups/CONTENT.md`.

## Intent

A new user opens the MonoAgent desktop app and, in about five minutes, ends
with a working setup they understand: their own profile, monomind turned on,
one usable agent, their browser connected to the right browser profile, and
one successful run. Today these are scattered over Documents, a hidden Agents
page, Settings and a dashboard checklist, and a fresh install starts with a
meaningless profile called "Default".

The flow should also be a pleasure: a night village grows as each step is
finished, and it is full of small things to discover.

## Goals

1. A full-window, 10-step onboarding shown after sign-in and before the main
   app, matching the approved mockup in look, copy, order and behaviour.
2. No seeded "Default" profile on a new install. The user creates and names
   the first profile in the flow.
3. Every step does the real work inline through the bindings the app already
   has, and tells the user where to find the same thing later.
4. Every step except Welcome, Create profile and Finish can be skipped. The
   flow resumes where it was left, and skipped steps stay visible as "to
   finish" after onboarding.
5. Fix the defects on this path (list below).

## Non-goals

- No change to the sign-in gate, to how the CLI resolves profiles, or to the
  strict-explicit-profile program (`docs/mastermind/plans/2026-10-15-strict-explicit-profile.md`).
- No Chrome Web Store / Edge Add-ons listing. Install stays "Load unpacked".
- No change to the extension's own profile selector or to
  `internal/extension` request shapes (that plan's coordination note).
- The village does not live on in the main app after onboarding.
- No raster art. See "Calls made for the owner".

## Anti-goals

- An existing user is never forced through onboarding, and no existing
  profile or data is touched by removing the seed.
- Onboarding never blocks the app: a failed step can always be skipped, and
  closing the app mid-flow loses nothing.
- The scene never slows typing, steals keyboard focus, or runs motion under
  `prefers-reduced-motion`.
- The flow never claims something worked that did not (no fake progress in
  the real product; progress lines come from real events).

## Approaches

1. **Native React screen (chosen).** Onboarding is a React screen mounted in
   `AccountShell` between the sign-in gate and `App`. Steps call existing
   Wails bindings. The scene is ported from the mockup as a self-contained
   module (inline SVG, CSS animation, a small framework-free controller)
   that takes the step statuses as input. Translatable, testable, no
   duplicated logic.
2. **Embed the mockup HTML in an iframe** with a postMessage bridge. Fastest,
   but not translatable (the app ships en and es), hard to test, and every
   real action needs a bridge.
3. **Spotlight tour over the real tabs.** Rejected by the owner in favour of
   the inline wizard: a tour can be lost halfway and depends on page layout.

## When it shows

- New Go binding `GetOnboardingState()` returns, per step, one of
  `done | skipped | todo`, plus `completed` and `currentStep`.
- Status is **derived from real state** wherever it exists (a profile
  exists; `IsMonomindInitialized`; an agent is usable; extension connected
  and a browser bound to the profile). Only what cannot be derived is stored:
  `skipped` marks, "seen" for Welcome / permissions, first-run done,
  `completed`. Stored machine-wide in `~/.monoagent/gui-state.json`, with
  per-profile steps keyed by profile id.
- Shown when `completed` is false. Install with no profile: starts at
  Welcome. Mid-flow restart: resumes at the first step that is not done or
  skipped.
- **Existing installs** (at least one profile when this version first runs)
  are marked `completed` silently. They get a "Run setup again" entry in
  Settings and the "Finish setup" card (below) for anything not done.
- The dashboard `GetStartedCard` is replaced by a "Finish setup" card driven
  by the same state: lists steps that are skipped or todo, each opening the
  onboarding at that step. Dismissible as today.
- **Later profiles:** creating another profile from the sidebar opens a short
  version with only the per-profile steps (About you, Turn on monomind,
  browser connection), in the same panel over the finished
  village. This is the last phase and may ship after the main flow.

## The steps (real behaviour)

Copy and layout follow `CONTENT.md`; this section says what each step really
does. Bindings named here exist today unless marked NEW.

1. **Welcome.** Static.
2. **System check.** Runs the existing health checks for: CLI found, Node.js
   found, monomind installed (`monomind.install` fix, as `FixMonomindButton`
   does), background service running. Each failing row has a one-click fix
   using the existing doctor fix ids. `RunHealthCheck` and the fix bindings
   must work with **no profile** (today they need `a.profileID()`); the
   underlying `doctor` already does.
   The background service row starts the daemon, because the extension
   bridge, schedules and triggers need it; the copy says so.
3. **Create your profile.** `CreateProfile` (name, icon, folder via
   `ChooseProfileFolder`). Name rules come from the existing validator
   (`validateProfileName`, `internal/cli/profile/names.go`, which already
   refuses "default"); errors are shown in the step's words.
   The first profile is auto-selected (existing behaviour). Cannot be skipped.
4. **About you.** Writes to the profile's Info (`profileinfo`): name to
   `first_name`/`last_name`/`full_name`, role to `job_title`, company to
   `employer`. "What do you want help with?" is written as one plain line
   into `about_me` (no new schema). Today only form-fill and application-fill
   are known to read Info, so the mockup line "Agents read this before they
   work for you" may be untrue: the builder checks whether the profile
   summary given to agents (`profilectx`) includes Info, and the copy says
   only what is true (fallback: "Used when agents fill forms and
   applications for you").
5. **Turn on monomind.** `InitializeMonomindProfile` with its
   `monomind:initProgress` events as the visible progress. Success is
   `IsMonomindInitialized`. Long-running (timeout is 12 minutes): the user
   may continue to read, and Skip stays available while it runs; the step
   completes in the background and the village builds when it lands.
6. **Your agents.** `ScanAgentRuntimes`, install via the existing
   `agent install`, "Check again" rescans. Continue needs one agent that is
   installed and signed in. Proof is the existing validation, **scoped to
   the one runtime and model the user picks** (`--runtime`/`--model`): by
   default validation sends a real turn to every model of every runtime,
   which is too slow and costly here. The step says it sends one short test
   message. Passing puts the agent on the roster. The scan needs the
   monomind binary from step 2 (the builder confirms it does not need step
   5); if that was skipped the step says so and offers to go back.
7. **Connect your browser.**
   - NEW Go function lists Chromium-based browsers found on this machine
     (Chrome, Edge, Brave, Arc, Vivaldi, Opera, Chromium) per OS.
   - `InstallExtension` unpacks to `~/.monoagent/extension`; the step shows
     the extensions page address for the chosen browser with Copy, where
     Developer mode is, and "Load unpacked". A NEW "Open <browser>" button
     launches the browser (a `chrome://` address cannot be opened from
     outside, so the address is copied, not opened).
   - `PairExtension`, live status from `GetExtensionStatus`.
   - `GetBrowsers` lists only browsers already attached to the bridge, not
     every browser profile on the machine. So the order differs from the
     mockup: the user is told first to **open the browser profile whose
     logged-in accounts belong to this MonoAgent profile**, and to install
     and pair the extension in that window. That window then appears as the
     connected browser and the user confirms it. The mockup's pick-list of
     "detected browser profiles" becomes this confirm step (a list only when
     several are attached).
   - The tie is exclusive, `AssignBrowser` (not `BindBrowser`, which adds a
     profile to a browser that may serve several): the copy promises agents
     in this profile use only that browser profile.
   - Done means paired **and** assigned.
   - A browser is listed by name only if install, pairing and browser-profile
     detection are verified to work in it during implementation; otherwise it
     is covered by "Other Chromium browser" with generic instructions.
8. **What agents may do in your browser.** DECIDED by the owner 2026-11-21: presets over the existing
   setting; no new per-action model. The
   mockup's three per-action rows (read / click and type / submit, each
   Allow, Ask, Never) do not exist in the product. The real model
   (`internal/browserctl/permissions.go`) is one level per agent kind
   (coder, apply, org, chat, other) from full-no-approvals, full, dev, ask,
   off, and it is machine-wide, not per profile. The step offers
   three plain presets that set those existing levels for all kinds
   ("Ask before anything is submitted", recommended; "Let agents work
   freely"; "Keep agents out of my browser"), plus a link to the detailed
   setting. Being machine-wide, it is shown once and is not part of the
   later-profile flow. Explains Human-in-Loop.
9. **Your first run.** Three starter runs backed by real bundled workflow
   templates (use existing ones where they fit, add small new templates
   where not). A run executes for real in the new profile and shows real node
   progress and the real result. A failure shows the error plainly and still
   lets the user go on. No bundled template matches the mockup's three
   cards, so three small ones are added, graded by what they need: one that
   needs nothing (fetch a page and pull out its headings), one that needs an
   agent (summarise a page), one that needs an agent and the browser. A card
   whose prerequisite step was skipped is disabled and says which step to
   finish.
10. **You're set.** Summary from `GetOnboardingState`; catalogue cards that
    name the real page and, on click, finish onboarding and open that page.

## The scene

- Ported from the mockup, not redrawn: same village, 20 inhabitants and
  their actions, build transitions (about 2.5 s), 12 hidden discoveries with
  the counter, lighting that warms, sunrise at the end, skipped steps as
  outlines.
- One module with a narrow interface: `mount(el)`, `setState(stepStatuses,
  currentStep)`, `playBuild(step)`, `destroy()`. It knows nothing about
  bindings. React owns the panel; the scene is `aria-hidden` and never
  focusable.
- All scene files stay under the 500-line limit by splitting art (SVG
  pieces), animation CSS and the controller.
- On the last step only (owner, 2026-11-21; see CONTENT.md "Third round"):
  a windmill click brightens the house lights for about 3 seconds; a drone
  can be sent up; and a secret combination (windmill 7 times, campfire,
  drone, windmill while the drone is up) starts a robot rampage that ends in
  a ruin with a "Rebuild the village" control. The control sits outside the
  aria-hidden scene and exists only then. The rampage never blocks the
  panel, and "Open MonoAgent" works during and after it.
- Builds fire when a step becomes `done` for real (including late, as in
  step 5), not when Continue is pressed.
- Under reduced motion: built state, no animation; clicks may give a
  non-moving response.
- No network: no web fonts or remote assets are added for onboarding.

## Removing the "Default" profile

- New databases get no profile. The seed is in `data/migrations/011_profiles.sql`
  (`INSERT OR IGNORE INTO profiles ... ('default','Default')`).
- Existing databases are not changed: a profile whose id is `default`
  remains valid, as the strict-profile plan requires.
- 011 also seeds `active_profile_id='default'` (deleted again by migration
  101) and declares `profile_id ... DEFAULT 'default'` columns; the column
  defaults stay (inert: every insert names its profile).
  `storage/reconcile.go` (around lines 264 and 301-339) and the other
  migrations that mention 'default' must each be checked against an empty
  `profiles` table.
- The builder picks the mechanism (edit the seed out of 011 if migrations
  are not checksummed; otherwise an equivalent that only affects a database
  created in the same migrate run) and proves with tests that (a) a fresh
  database has zero profiles and every later migration still applies, and
  (b) an existing database keeps its profiles.
- Databases from before profiles existed (version 010 or lower, v0.7.0 and
  older) still run 011 when they are upgraded. Its `ADD COLUMN profile_id
  ... DEFAULT 'default'` lines (and later 013 and 098) give all their rows
  the owner `default`. So 011 keeps creating the `default` profile on
  exactly those databases: only when one of the tables it scopes (people,
  crawler_sessions, actions, social_lists, threads, workflows,
  workflow_executions, credentials, vault_images, hil_pending, tags), or
  posts, already holds a row. A new database has all of them empty and gets
  no profile. The `active_profile_id` setting is not recreated, because 101
  deletes it in the same upgrade. Tests start from the schema below 011 with
  and without data.
- With zero profiles: the CLI gate already refuses scoped commands with a
  clear message; the GUI already treats "no profile" as a normal state. The
  onboarding replaces the Dashboard's no-profile empty state as the way in.
- Clean up what contradicts this: the `osmenu/bundle.go` comment, and
  `profiledir.List` marking a `Default` only if nothing depends on it.

## Fixes on this path

1. Health check and fixes need a profile (blocks step 2).
2. Documents and Agents check monomind status only on mount; a profile
   switch or an init started elsewhere leaves a stale banner. Both should
   follow the `monomind:initProgress` events and the selected profile.
3. Signed-out `InitializeMonomindProfile` returns "login required" with no
   event, so callers' spinners stick. Callers handle the return value.
4. Documents banner says uploads are not searchable before init; make the
   text true to what the code does.
5. `MonomindInitPrompt` says init always sends a Claude prompt; only the
   old-version fallback does.
6. Agents page: no way in from the sidebar, and its "monomind missing" state
   has no Fix button. Add the Fix button; the catalogue and Finish-setup
   card link to it. (A sidebar entry is the owner's call; not added here.)
7. Health text says "the chrome-extension folder"; it is
   `~/.monoagent/extension`.
8. README and `docs/BROWSER_PROFILES.md` say "Settings → Browsers"; name the
   section as it is, and update both for any Chromium browser.
9. App UI names only Chrome and Edge for the extension; align with step 7.
10. Brand spelling: "MonoAgent" everywhere in the screens touched.
11. New onboarding strings go through i18n (`en.json`, `es.json`).

Not touched: the uncommitted local-debug change in `OpenStartReview.jsx`.

## Constraints

- Branch from `master` in its own worktree. Never push or merge; the owner
  does that (a push to master is a release).
- Go tests must pass without Chrome. Frontend tests in vitest. Tests and
  builds never touch the real `~/.monoagent` (scratch HOME).
- Files under 500 lines. Conventional commits.
- Works on macOS, Windows and Linux; browser detection is per-OS.

## Phases (each independently reviewable)

1. **Foundations (Go):** no seeded profile; onboarding state + binding;
   health without a profile; Chromium browser detection; "open browser".
2. **Shell + steps 1 to 6** with a static backdrop.
3. **Scene port** and wiring to real step completion.
4. **Steps 7 to 10**, starter templates, Finish-setup card, Settings entry.
5. **Fixes list, docs, es translation, later-profile short flow.**

## Calls made for the owner (check these)

- No codex-generated raster images. The owner first asked for generated
  pictures, then approved a mockup whose only art is the animated vector
  village. Photos or painted images would clash with it and cannot animate
  or be clicked, so the village is the art. Say so if generated images are
  still wanted somewhere (for example on the catalogue cards).
- Existing users are not shown onboarding; they get "Run setup again".
- The village is onboarding-only.
- No sidebar entry for Agents.
- A browser is promised by name only once verified.
- Later-profile short flow ships last.
- Agent check sends one short real test message to the chosen agent only.
- Browser tie is exclusive (one browser profile per MonoAgent profile).
- The browser step asks the user to open the right browser profile first,
  instead of picking from a detected list (the app cannot see browser
  profiles the extension is not in yet).
- Starter runs are three new small templates graded by what they need.

## Left to the builder

- Exact storage shape in `gui-state.json`; component and file layout.
- The seed-removal mechanism, within the rule above.
