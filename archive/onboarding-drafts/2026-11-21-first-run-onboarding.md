# First-Run Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A full-window, 10-step onboarding inside the Wails desktop app that ends with a named profile, monomind on, one usable agent, a connected browser and one real run, while a night village grows with each finished step.

**Architecture:** A React screen (`wails-app/frontend/src/onboarding/`) mounted between the sign-in gate and `App`. Step status is derived in Go from real state plus a few stored marks in `~/.monoagent/gui-state.json`, served by one binding. The village is a framework-free module ported from the approved mockup and driven only by step statuses.

**Tech Stack:** Go (root module and the `wails-app` module), Wails bindings, React 19, vitest + Testing Library, i18next (`en.json`, `es.json`), plain CSS, inline SVG.

**Spec:** `docs/mastermind/specs/2026-11-21-first-run-onboarding-design.md`. Copy: `docs/mastermind/specs/onboarding-mockups/CONTENT.md`. Visual reference (port from it, do not redraw): `docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html`.

## Global Constraints

- Work on branch `feat/first-run-onboarding`, created from `master` in its own worktree. Never push or merge.
- Never touch or commit `wails-app/frontend/src/components/chat/OpenStartReview.jsx`.
- Tests and builds never touch the real `~/.monoagent`: use `t.TempDir()` / `t.Setenv("HOME", ...)`; keep the real GOMODCACHE and GOCACHE.
- Build the CLI only with `go build -o <scratch path> ./cmd/monoagentcli`.
- Go tests pass without Chrome. Frontend tests are vitest, co-located, named `*.render.test.jsx` or `*.test.js`.
- Every file under 500 lines. Conventional commits ending with `Co-Authored-By: Claude Fable 5.2 <noreply@anthropic.com>`.
- Brand is spelled "MonoAgent". Engine is spelled "monomind".
- Every user-visible string goes through i18n under the `onboarding.` prefix, with the key written out in full at the call site (the key scan needs it). `en.json` in the task that adds the string; `es.json` in Task 17.
- No network assets, no web fonts, no raster images.
- No fake progress: every progress line and every "done" comes from a real binding result or event.
- The profile id `default` stays valid in existing databases. Do not change `chrome-extension/` profile-selection UI or `internal/extension` request shapes.
- Step ids, used everywhere: `welcome`, `system`, `profile`, `about`, `monomind`, `agents`, `browser`, `permissions`, `firstRun`, `finish`. Statuses: `done`, `skipped`, `todo`.
- Not skippable: `welcome`, `profile`, `finish`.
- Go test commands: root module `go test ./internal/... ./data/...`; app module `cd wails-app && go test ./...`. Frontend: `cd wails-app/frontend && npx vitest run <path>`.

## Review Focus

1. **Session ends mid-flow.** A binding returns the login-required JSON instead of a result. Expected: the step shows the sign-in message and its buttons are usable again; nothing spins forever. Test in Task 7 (shared `callStep` helper) and Task 9.
2. **App closed while monomind init runs.** Expected on restart: the step shows "not turned on yet" with the button, not a stuck progress view; if init finished meanwhile it shows done. Test in Task 9.
3. **Old or damaged `gui-state.json`.** A file with only `selected_profile`, or unreadable JSON. Expected: selection still loads; onboarding state is treated as absent; writing either part never erases the other. Test in Task 2.
4. **Zero profiles after onboarding was completed** (the user deleted the only profile). Expected: onboarding opens at Create profile, not an empty app. Test in Task 3.
5. **Background service not running at the browser step** (system step skipped). Expected: pairing reports that the service is not running and offers Start in place; no endless "waiting". Test in Task 12.

---

## Phase 1: Foundations (Go)

### Task 1: No seeded profile on a new database

**Files:**
- Modify: `data/migrations/011_profiles.sql` (remove the two `INSERT OR IGNORE` lines for `profiles` and `settings.active_profile_id`; keep everything else, add a comment saying why)
- Modify: `internal/storage/reconcile.go` (only if a test below fails on an empty `profiles` table)
- Modify: `internal/osmenu/bundle.go` (the comment that says a Default profile always exists)
- Test: `internal/storage/no_seed_profile_migration_test.go`

Migrations are not checksummed (only filenames are listed in `data/migrations.txt`), so the seed is edited out in place; no new migration.

- [ ] **Step 1: Write the failing tests**

```go
func TestFreshDatabaseHasNoProfiles(t *testing.T)            // open a new DB in t.TempDir(), run all migrations: no error, SELECT COUNT(*) FROM profiles == 0, settings has no active_profile_id
func TestReconcileRunsOnEmptyProfiles(t *testing.T)          // on that fresh DB call every reconcile entry point migrate runs at startup: no error, no row created in profiles
func TestExistingDefaultProfileSurvives(t *testing.T)        // DB with profiles row ('default','Default') and one workflow with profile_id='default'; run migrations + reconcile again: both rows unchanged
```

Use the helpers the neighbouring `migration_*_test.go` files use to open a scratch database.

- [ ] **Step 2: Run** `go test ./internal/storage/ -run 'NoProfiles|EmptyProfiles|DefaultProfileSurvives' -v`. Expected: `TestFreshDatabaseHasNoProfiles` FAILS with count 1.
- [ ] **Step 3: Remove the seed lines from 011; fix any later migration or reconcile step that fails on an empty `profiles` table** (column `DEFAULT 'default'` clauses stay).
- [ ] **Step 4: Run** `go test ./internal/storage/ ./internal/profiledir/ ./internal/cli/... ./data/...`. Expected: PASS, except tests already listed as red on master in the project's known-failing list. Fix any test that relied on the seed by having it create its own profile.
- [ ] **Step 5: Commit** `feat(profiles): stop seeding a Default profile on new databases`

### Task 2: Onboarding marks in `gui-state.json`

**Files:**
- Create: `wails-app/onboarding_store.go`
- Modify: `wails-app/profile_selection.go` (`guiState`, `saveSelectedProfile`)
- Test: `wails-app/onboarding_store_test.go`

**Interfaces — Produces:**

```go
type onboardingMarks struct {
    Completed    bool                              `json:"completed"`
    Seen         map[string]bool                   `json:"seen,omitempty"`     // machine-wide marks: welcome, system, permissions, firstRun
    Skipped      []string                          `json:"skipped,omitempty"`  // machine-wide skipped step ids: system, agents, permissions, firstRun
    Profiles     map[string]onboardingProfileMarks `json:"profiles,omitempty"` // keyed by profile id
}
type onboardingProfileMarks struct {
    Skipped   []string `json:"skipped,omitempty"` // about, monomind, browser
    AboutDone bool     `json:"about_done,omitempty"`
}
// guiState gains: Onboarding *onboardingMarks `json:"onboarding,omitempty"`  (nil = never written)

func loadGuiState(path string) guiState                                  // damaged or missing file = zero value
func updateGuiState(path string, mutate func(*guiState)) error           // read, mutate, temp file + rename
```

`saveSelectedProfile` becomes a call to `updateGuiState` that sets only `SelectedProfile`. `loadSelectedProfile` and `persistedProfileForStop` keep their behaviour.

- [ ] **Step 1: Write the failing tests**

```go
func TestSavingSelectionKeepsOnboardingMarks(t *testing.T)   // write marks{Completed:true}, then saveSelectedProfile(path,"work"); reload: Completed still true, SelectedProfile=="work"
func TestSavingMarksKeepsSelection(t *testing.T)             // the reverse
func TestOldStateFileLoads(t *testing.T)                     // file `{"selected_profile":"work"}`: SelectedProfile=="work", Onboarding==nil
func TestDamagedStateFileIsEmpty(t *testing.T)               // file `{not json`: zero guiState, no panic; a following updateGuiState writes a valid file
```

- [ ] **Step 2: Run** `cd wails-app && go test . -run 'Selection|Marks|StateFile' -v`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd wails-app && go test . -run 'Selection|Marks|StateFile|Profile' -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(onboarding): keep onboarding marks beside the profile selection`

### Task 3: `GetOnboardingState` and its actions

**Files:**
- Create: `wails-app/app_onboarding.go`, `wails-app/onboarding_derive.go`
- Test: `wails-app/onboarding_derive_test.go`, `wails-app/app_onboarding_test.go`

**Interfaces — Consumes:** Task 2. **Produces (Wails bindings):**

```go
type OnboardingStep struct {
    ID     string `json:"id"`
    Status string `json:"status"` // done | skipped | todo
}
type OnboardingState struct {
    Completed   bool             `json:"completed"`
    CurrentStep string           `json:"currentStep"` // first step that is todo; "finish" when none
    ProfileID   string           `json:"profileId"`   // "" before the first profile
    Steps       []OnboardingStep `json:"steps"`       // always the 10 ids, in order
}
func (a *App) GetOnboardingState() (*OnboardingState, error)
func (a *App) MarkOnboardingStep(id string) error  // accepts welcome, system, about, permissions, firstRun; anything else is an error
func (a *App) SkipOnboardingStep(id string) error  // refuses welcome, profile, finish
func (a *App) CompleteOnboarding() error
func (a *App) RestartOnboarding() error            // Completed=false, clears Seen["welcome"] and all skipped marks; keeps real state
```

Pure core, testable without an App:

```go
type onboardingFacts struct {
    ProfileID       string // selected profile, "" if none
    ProfileCount    int
    MonomindOn      bool   // IsMonomindInitialized for ProfileID
    AgentUsable     bool   // the agent roster has at least one entry that passed validation
    BrowserAssigned bool   // extension paired and a browser assigned to ProfileID
}
func deriveOnboarding(m *onboardingMarks, f onboardingFacts) OnboardingState
```

Rules for `deriveOnboarding`: `profile` is done iff `ProfileCount > 0`; `monomind`, `agents`, `browser` are done iff their fact is true (a true fact beats a stored skip); `welcome`, `system`, `permissions`, `firstRun` are done iff `Seen[id]`; `about` is done iff `Profiles[ProfileID].AboutDone`; `finish` is done iff `Completed`. A step that is not done is `skipped` if marked, else `todo`. `m == nil` and `ProfileCount > 0` means an install that predates onboarding: return `Completed: true`, and `GetOnboardingState` writes that mark once. `Completed` is reported **false** whenever `ProfileCount == 0`, whatever is stored, with `CurrentStep` `welcome` if never seen, else `profile`.

- [ ] **Step 1: Write the failing tests** (table-driven over `deriveOnboarding`)

```go
func TestDerive_FreshInstallStartsAtWelcome(t *testing.T)        // m=nil, ProfileCount=0: Completed=false, CurrentStep=="welcome", 10 steps all todo
func TestDerive_ExistingInstallIsCompleted(t *testing.T)         // m=nil, ProfileCount=2: Completed=true
func TestDerive_ResumesAtFirstTodo(t *testing.T)                 // welcome+system seen, profile exists, about skipped: CurrentStep=="monomind", about=="skipped"
func TestDerive_RealStateBeatsSkip(t *testing.T)                 // monomind skipped but MonomindOn: status=="done"
func TestDerive_CompletedButNoProfilesReopensAtProfile(t *testing.T) // Completed stored true, welcome seen, ProfileCount=0: Completed=false, CurrentStep=="profile"
func TestDerive_PerProfileMarksFollowTheSelectedProfile(t *testing.T) // AboutDone for "a", ProfileID "b": about=="todo"
func TestSkip_RefusesUnskippable(t *testing.T)                   // welcome, profile, finish and "nope" all return an error and write nothing
```

- [ ] **Step 2: Run** `cd wails-app && go test . -run 'Derive|Skip_' -v`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.** `GetOnboardingState` gathers `onboardingFacts` from the same code the existing bindings use (`GetProfiles`, `IsMonomindInitialized`, `AgentRoster`, `GetBrowsers`/`GetExtensionStatus`); a fact that cannot be read (signed out, CLI missing) counts as false and is never an error for the caller.
- [ ] **Step 4: Run** `cd wails-app && go test . -run 'Derive|Skip_|Onboarding' -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(onboarding): derive step status from real state`

### Task 4: Health check and fixes without a profile

**Files:**
- Modify: `wails-app/app_health.go` (`healthArgs`, `healthFixArgs`, `RunHealthCheck`, `RunHealthFix`)
- Test: `wails-app/app_health_noprofile_test.go`

`InstallAgentRuntime` keeps needing a profile (step 6 comes after step 3).

- [ ] **Step 1: Write the failing tests**

```go
func TestHealthArgs_NoProfile(t *testing.T)     // healthArgs("", healthModeLocal) equals healthArgs("work", healthModeLocal) with the profile flag and its value removed; no element is "--profile" or starts with "--profile="
func TestHealthFixArgs_NoProfile(t *testing.T)  // healthFixArgs("", "monomind.install") ends with ["doctor","fix","--","monomind.install"], no --profile flag
```

- [ ] **Step 2: Run** `cd wails-app && go test . -run 'HealthArgs_NoProfile|HealthFixArgs_NoProfile' -v`. Expected: FAIL.
- [ ] **Step 3: Implement:** an empty profile id builds the unscoped command; `RunHealthCheck` and `RunHealthFix` pass `""` when `a.profileID()` returns `errNoProfile` and keep failing on any other error.
- [ ] **Step 4: Verify against the real CLI in a scratch HOME:** build with `go build -tags devaccount -o "$SCRATCH/monoagentcli" ./cmd/monoagentcli`, then `HOME="$SCRATCH/home" MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z "$SCRATCH/monoagentcli" doctor --json`. Expected: a JSON report, exit 0 or 1, no "profile" error. If it does refuse, make `doctor` and `doctor fix` profile-optional in `internal/cli` with a test there, and say so in the commit.
- [ ] **Step 5: Run** `cd wails-app && go test . -run Health -v`. Expected: PASS.
- [ ] **Step 6: Commit** `fix(health): run the health check and fixes before any profile exists`

### Task 5: Find and open Chromium-based browsers

**Files:**
- Create: `internal/chromium/browsers.go`, `internal/chromium/paths_darwin.go`, `internal/chromium/paths_windows.go`, `internal/chromium/paths_linux.go`
- Create: `wails-app/app_chromium.go`
- Test: `internal/chromium/browsers_test.go`, `wails-app/app_chromium_test.go`

**Interfaces — Produces:**

```go
// package chromium
type Browser struct {
    ID            string `json:"id"`            // chrome, edge, brave, arc, vivaldi, opera, chromium
    Name          string `json:"name"`
    Installed     bool   `json:"installed"`
    ExtensionsURL string `json:"extensionsUrl"` // chrome://extensions, edge://extensions, brave://extensions, arc://extensions, vivaldi://extensions, opera://extensions, chrome://extensions for chromium
    DevModeSide   string `json:"devModeSide"`   // "left" for edge, "right" for the others
}
func Known() []Browser                              // all seven, Installed=false
func Detect(exists func(path string) bool) []Browser // installed first, then the rest, each group in the order above
func LaunchCommand(id string) (name string, args []string, err error) // from the detected path only; unknown or not installed is an error

// wails-app bindings
func (a *App) DetectChromiumBrowsers() []chromium.Browser
func (a *App) OpenChromiumBrowser(id string) error
```

`OpenChromiumBrowser` takes an id, never a path: nothing from the frontend reaches `exec`.

- [ ] **Step 1: Write the failing tests**

```go
func TestDetect_InstalledFirst(t *testing.T)          // exists() true only for the brave and chrome candidates: first two are chrome, brave with Installed=true; len==7
func TestKnown_ExtensionsURLs(t *testing.T)           // the seven exact addresses above; DevModeSide "left" only for edge
func TestLaunchCommand_RefusesUnknownAndMissing(t *testing.T) // "nope", "../x" and a known-but-not-installed id all return an error
```

- [ ] **Step 2: Run** `go test ./internal/chromium/ -v`. Expected: FAIL (no package).
- [ ] **Step 3: Implement.** Candidate paths per OS: macOS `/Applications/<App>.app` and `~/Applications/<App>.app`; Windows under `%ProgramFiles%`, `%ProgramFiles(x86)%`, `%LocalAppData%`; Linux by `exec.LookPath` on the usual binary names.
- [ ] **Step 4: Run** `go test ./internal/chromium/ -v && cd wails-app && go test . -run Chromium -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(browser): find and open Chromium-based browsers`

### Task 6: Browser permission presets

**Files:**
- Modify: `internal/browserctl/permissions.go`, `wails-app/app_browser_permissions.go`
- Test: `internal/browserctl/permissions_preset_test.go`, `wails-app/app_browser_permissions_test.go`

**Interfaces — Produces:**

```go
// package browserctl
const (PresetAsk = "ask"; PresetFree = "free"; PresetOff = "off")
func PresetLevels(preset string) (map[string]string, error)
// wails-app
func (a *App) ApplyBrowserPermissionPreset(preset string) string // same JSON answer shape as SetBrowserPermission; all kinds written in one save
```

`ask`: every kind `LevelFull` (any site, a submit on a real site waits for approval). `free`: every kind `LevelFullNoApprovals`, except `KindApply` which is `LevelFull` (the existing rule forbids no-approvals for it). `off`: every kind `LevelOff`. Site rules are left as they are.

- [ ] **Step 1: Write the failing tests**

```go
func TestPresetLevels(t *testing.T)                 // the three maps above, each with exactly the five Kinds; every value passes the existing kind/level validation
func TestPresetLevels_Unknown(t *testing.T)         // "", "full", "ASK" return an error
func TestApplyPreset_WritesAllKindsOnce(t *testing.T) // after "off", BrowserPermissions() reports off for all five kinds and existing site rules unchanged
```

- [ ] **Step 2: Run** `go test ./internal/browserctl/ -run Preset -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/browserctl/ && cd wails-app && go test . -run BrowserPermission -v`. Expected: PASS.
- [ ] **Step 5: Regenerate the Wails JS bindings** for Tasks 3 to 6 (`cd wails-app && wails generate module`), check that `frontend/src/wailsjs/go/main/App.js` gained the eight new functions, commit with message `feat(browser): one-click permission presets`.

---

## Phase 2: Shell and steps 1 to 6

### Task 7: Onboarding shell, gate and state hook

**Files:**
- Create: `wails-app/frontend/src/onboarding/OnboardingGate.jsx`, `Onboarding.jsx`, `useOnboarding.js`, `steps.js`, `callStep.js`, `onboarding.css`
- Modify: `wails-app/frontend/src/AccountApp.jsx` (render `<OnboardingGate banners={banners} />` instead of `<App banners={banners} />`), `wails-app/frontend/src/App.jsx` (accept `initialNav`), `wails-app/frontend/src/services/api.js` (wrappers for the new bindings), `wails-app/frontend/src/locales/en.json`
- Test: `onboarding/Onboarding.render.test.jsx`, `onboarding/OnboardingGate.render.test.jsx`, `onboarding/callStep.test.js`

**Interfaces — Produces:**

```js
// steps.js
export const STEPS = [/* { id, titleKey, skippable } for the 10 ids, in order */]
export const PROFILE_STEPS = ['about', 'monomind', 'browser']

// useOnboarding.js
export function useOnboarding()  // { state, loading, reload(), mark(id), skip(id), complete(), restart() }; reload() calls GetOnboardingState

// callStep.js
export async function callStep(fn)  // -> { ok: true, value } | { ok: false, loginRequired: boolean, message: string }
// understands the three answer styles the bindings use: a thrown error, a JSON string with "error", and the login-required JSON

// OnboardingGate.jsx
export const OnboardingContext  // { openOnboarding(stepId?: string, mode?: 'full' | 'profile') }
// renders <Onboarding> when !state.completed or when opened by openOnboarding; otherwise <App banners initialNav>

// Onboarding.jsx
// props: { state, startStep, mode, onFinish(nav?: { page, opts }) }
// every step component receives: { state, profileId, reload, onDone(), setCanContinue(bool), goTo(stepId) }
```

`App` gets a prop `initialNav` (`{ page, opts }` or null) that it applies once on mount through its existing `navigate`.

Layout follows the mockup: scene area full-window behind (a static dark gradient placeholder `div.onb-backdrop` until Task 11), glass panel with the step, progress rail showing all 10 steps with done / skipped / current, Back, Continue, "Skip for now". Enter continues when allowed and the focus is not in a textarea. Visible focus rings.

- [ ] **Step 1: Write the failing tests**

```jsx
it('opens at the step the state names')                    // state.currentStep 'monomind' -> heading t('onboarding.monomind.title')
it('hides Skip on welcome, profile and finish')
it('keeps Continue disabled until the step allows it')     // stub step calls setCanContinue(true) -> enabled
it('Skip calls skip(id) and moves to the next step')
it('Back never goes before welcome')
it('Enter continues only when Continue is enabled')
it('shows skipped steps as skipped in the rail')           // data-status="skipped"
it('gate renders App when completed')                      // App mocked
it('gate renders Onboarding when not completed')
it('openOnboarding(stepId) shows Onboarding at that step over a completed state')
// callStep.test.js
it('maps the login-required JSON to loginRequired: true')
it('maps a JSON error string and a thrown error to ok: false with the message')
```

- [ ] **Step 2: Run** `cd wails-app/frontend && npx vitest run src/onboarding`. Expected: FAIL (files missing).
- [ ] **Step 3: Implement** with placeholder step components (heading only) registered in a `STEP_COMPONENTS` map in `Onboarding.jsx`; later tasks replace entries.
- [ ] **Step 4: Run** `npx vitest run src/onboarding src/AccountApp.signedOut.render.test.jsx src/doctrine.test.js`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(onboarding): shell, gate and state hook`

### Task 8: Steps Welcome, System check, Create profile, About you

**Files:**
- Create: `onboarding/steps/Welcome.jsx`, `SystemCheck.jsx`, `CreateProfile.jsx`, `AboutYou.jsx`, each with `<Name>.render.test.jsx`
- Modify: `onboarding/Onboarding.jsx` (register), `locales/en.json`

**Interfaces — Consumes:** Task 7 step props; `RunHealthCheck('local')`, `RunHealthFix(fixId)` with `health:fixProgress` events; `CreateProfile(name, rootDir, icon)`, `ChooseProfileFolder()`; `SetProfileInfo(key, value)`.

Decisions:
- **Welcome:** on "Get started" call `mark('welcome')`.
- **System check:** four rows read from the doctor report: CLI found, Node.js found, monomind installed (fix id `monomind.install`), background service running (fix id `services.daemon.start`). Reuse `components/settings/HealthRow.jsx` if its props fit, otherwise a local row. When all four pass, call `mark('system')` and `setCanContinue(true)`. The service row's copy: "Runs schedules and triggers, and lets the browser extension connect."
- **Create profile:** name field with chips Personal, Work, Client name; icon picker using the icon set `NewProfileModal.jsx` already offers; folder line with Change (`ChooseProfileFolder`); live preview card. The Go error is shown in the step; a name refused for being "default" shows `onboarding.profile.errorDefault` ("Pick a name that says what this profile is for."). On success `reload()` then advance.
- **About you:** fields map to `full_name` (also split on the last space into `first_name` / `last_name`), `job_title`, `employer`; the chosen chips are written to `about_me` as one line `Wants help with: Outreach, Research.` Only non-empty fields are written. Then `mark('about')`. Intro copy: check whether `internal/profilectx` puts Info into what agents receive; if yes use "Agents read this before they work for you, so they write and act as you would.", if no use "Used when agents fill forms and applications for you." State which in the commit message.

- [ ] **Step 1: Write the failing tests**

```jsx
// SystemCheck
it('shows a Fix button only on failing rows and enables Continue when all four pass')
it('a failed fix shows the error text and leaves the row failing')
it('a login-required answer shows the sign-in message and stops the spinner')
// CreateProfile
it('disables Create profile while the name is empty')
it('shows the Go error under the field and stays on the step')
it('creates with the chosen name, folder and icon, then reloads state')   // CreateProfile called with ('Work', '', '<icon id>')
// AboutYou
it('writes full_name, first_name, last_name, job_title, employer and about_me, skipping empty fields')
it('marks about done after saving')
// Welcome
it('lists the five things that will be set up and marks welcome on Get started')
```

- [ ] **Step 2: Run** `npx vitest run src/onboarding/steps`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/onboarding`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(onboarding): welcome, system check, profile and about-you steps`

### Task 9: Step Turn on monomind

**Files:**
- Create: `onboarding/steps/Monomind.jsx`, `Monomind.render.test.jsx`
- Modify: `onboarding/Onboarding.jsx`, `locales/en.json`

**Interfaces — Consumes:** `IsMonomindInitialized()`, `InitializeMonomindProfile()`, event `monomind:initProgress` via `subscribeEvent`.

Decisions: two explainers (documents, orgs) with the copy from CONTENT.md; button "Turn on for <profile name>". Progress lines are the event payloads as they arrive, nothing invented. On mount, ask `IsMonomindInitialized()`: true shows the success state. While running, Skip and Back stay usable and the subscription lives in `Onboarding.jsx` (not the step), so the result lands after the user moved on; on success call `reload()`. Pointer line: "You'll find this later in the Documents tab."

- [ ] **Step 1: Write the failing tests**

```jsx
it('shows the success state at once when already initialized')
it('shows each progress event as a line while running')
it('a login-required return value shows the sign-in message and re-enables the button')   // no event ever arrives
it('a failed init shows the error and re-enables the button')
it('remounting while nothing is running shows the button, not a progress view')            // app closed mid-init
it('success after the user left the step still reloads state')                             // event arrives with a different step mounted
```

- [ ] **Step 2: Run** `npx vitest run src/onboarding/steps/Monomind.render.test.jsx`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/onboarding`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(onboarding): turn on monomind step`

### Task 10: Step Your agents

**Files:**
- Create: `onboarding/steps/Agents.jsx`, `Agents.render.test.jsx`
- Modify: `onboarding/Onboarding.jsx`, `locales/en.json`

**Interfaces — Consumes:** `ScanAgentRuntimes()`, `GetAgentRuntimeModels(runtimeID, binary)`, `InstallAgentRuntime(runtimeID, false, '')` (progress on `health:fixProgress`, key `agent.install:<id>`), `StartAgentValidation([runtime], [model], false)`, `AgentRoster()`.

Decisions: tiles for each runtime the scan returns (installed / not installed / sign-in needed, with the scan's own hint). "Check again" rescans. For an installed runtime: pick a model (default: the first the runtime reports), then "Send a test message"; the copy says "This sends one short test message to <agent>." Validation is called with exactly one runtime and one model. Continue is enabled once `AgentRoster()` holds a passed entry. If the scan answers that monomind is missing, show that with a "Back to system check" button (`goTo('system')`). Confirm by reading `ScanAgentRuntimes` that it needs only the monomind binary, not a profile init; if it needs init, say so in the step instead. Pointer: "You'll find this later in Settings, under AI agents."

- [ ] **Step 1: Write the failing tests**

```jsx
it('shows a tile per scanned runtime with its state')
it('shows the empty state with a recommended install when none is installed')
it('validation is started with one runtime and one model')        // expect(StartAgentValidation).toHaveBeenCalledWith(['claude'], ['<model>'], false)
it('enables Continue only after the roster shows a passed entry')
it('Check again rescans')
it('monomind missing shows the way back to the system check')
it('a failed test message shows the reason and leaves Continue disabled')
```

- [ ] **Step 2: Run** `npx vitest run src/onboarding/steps/Agents.render.test.jsx`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/onboarding`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(onboarding): agents step with a one-message check`

---

## Phase 3: The scene

### Task 11: Port the village and wire it to real completion

**Files:**
- Create under `onboarding/scene/`: `index.js` (controller), `art-ground.js`, `art-buildings.js`, `art-people.js`, `art-robots.js` (SVG strings), `builds.js` (per-step build sequences), `inhabitants.js` (the 20 and their actions), `discoveries.js` (the 12 and the counter), `scene.css`, `scene-motion.css`
- Create: `onboarding/Scene.jsx` (React wrapper), `onboarding/scene/index.test.js`, `onboarding/Scene.render.test.jsx`
- Modify: `onboarding/Onboarding.jsx` (replace `div.onb-backdrop`)

**Interfaces — Produces:**

```js
// scene/index.js
export function createScene()  // -> { mount(el), setState(statuses, currentStep), playBuild(stepId): Promise<void>, destroy() }
// statuses: { [stepId]: 'done' | 'skipped' | 'todo' }
export const BUILD_FOR_STEP = { profile: 'house', monomind: 'windmill', agents: 'robots', browser: 'bridge', firstRun: 'campfire' }
```

Port from `style-c-cinematic.html`: same SVG art, same 20 inhabitants and actions, same 2.45 s build transitions with scaffolding and confetti, same 12 discoveries (9 ordinary, 3 rarer) with the "found N of 12" counter that appears after the first find, lighting that warms with progress, sunrise when `finish` is done. Drop the mockup's "Mockup" control and all simulated timers that stood in for real work.

Decisions: the root element is `aria-hidden="true"`, has no `tabindex` anywhere, and its click handlers never call `focus()` or `preventDefault()` on keyboard events. `Scene.jsx` calls `playBuild(step)` when a step's status changes to `done` between two `setState` calls (so a late monomind success builds the windmill then), never on Continue. A `skipped` step's plot renders as an outline (`data-status="skipped"`). Under `prefers-reduced-motion: reduce` the `scene-motion.css` rules do not apply, `playBuild` resolves at once with the built state, and a discovery click still counts.

- [ ] **Step 1: Write the failing tests**

```js
it('mount adds one aria-hidden root with no focusable element')      // querySelectorAll('[tabindex], a, button, input') is empty
it('setState marks each plot with its status')                       // [data-plot="house"][data-status="done"]
it('a skipped step leaves an outline')                               // data-status="skipped"
it('playBuild resolves and leaves the plot built')
it('under reduced motion playBuild resolves immediately and adds no animation class')
it('a discovery click raises the counter and shows it only after the first find')
it('clicking the scene does not move focus from an input')            // document.activeElement unchanged
it('destroy removes the root and every listener')                    // a click after destroy changes nothing
// Scene.render.test.jsx
it('plays the build when a step becomes done, not when it is merely current')
it('does not replay builds for steps that were already done on mount')
```

- [ ] **Step 2: Run** `npx vitest run src/onboarding/scene src/onboarding/Scene.render.test.jsx`. Expected: FAIL.
- [ ] **Step 3: Implement.** Split so every file stays under 500 lines.
- [ ] **Step 4: Run** `npx vitest run src/onboarding`; then `wc -l src/onboarding/scene/*`. Expected: PASS, every file under 500.
- [ ] **Step 5: Look at it.** Run the app against a scratch HOME (`cd wails-app && HOME="$SCRATCH/home" wails dev`), go through steps 1 to 6 and compare with the mockup open beside it: builds, inhabitants, discoveries, typing in the profile name while the scene animates. Note any difference from the mockup in the commit body.
- [ ] **Step 6: Commit** `feat(onboarding): the growing village`

---

## Phase 4: Steps 7 to 10 and the ways back in

### Task 12: Step Connect your browser

**Files:**
- Create: `onboarding/steps/Browser.jsx`, `onboarding/steps/BrowserGuide.jsx` (the per-browser illustration and instructions), `Browser.render.test.jsx`
- Modify: `onboarding/Onboarding.jsx`, `locales/en.json`

**Interfaces — Consumes:** `DetectChromiumBrowsers()`, `OpenChromiumBrowser(id)`, `InstallExtension()`, `PairExtension()`, `GetExtensionStatus()`, `GetBrowsers()`, `AssignBrowser(profileID, instance)`, `RunHealthFix('services.daemon.start')`; `usePairExtension` from `components/settings/PairBrowser.jsx` where it fits.

Sub-steps, in this order:
1. **Pick browser.** Lead: "Works with any Chromium-based browser." Detected browsers first, marked "Installed"; last choice "Other Chromium browser".
2. **Open the right browser profile.** "Open the <browser> profile whose logged-in accounts belong to <profile name>. Do the next steps in that window." Button "Open <browser>" (`OpenChromiumBrowser`); hidden for "Other".
3. **Install.** `InstallExtension()` on entry; show the extensions address with Copy, where Developer mode is (`devModeSide`), "Load unpacked", and the folder the binding returned with Copy. "Other": "Open your browser's extensions page (usually Menu, then Extensions)".
4. **Pair.** "Pair extension", live status. If the status says the background service is not running, show that with a Start button in place.
5. **Confirm.** The attached browser(s) from `GetBrowsers()`: one is shown for confirmation, several as a list. Confirm calls `AssignBrowser(profileId, instance)`. Copy: "Agents working in <profile name> will only use this browser profile."

Explainer and diagram from CONTENT.md (MonoAgent profile, browser profile, logged-in sites). Done means paired and assigned; then `reload()`. Pointer: "You'll find this later in Settings, under Browser Extension."

Named browsers: verify install, pairing and detection by hand in each of the seven that is installed on this machine; any that fails or cannot be tested is removed from `chromium.Known()` (it is then covered by "Other"). List what was verified in the commit body.

- [ ] **Step 1: Write the failing tests**

```jsx
it('lists installed browsers first and Other last')
it('shows the chosen browser’s extensions address and Developer mode side')   // edge -> 'edge://extensions', left
it('Other shows the generic instruction and no Open button')
it('service not running shows Start in place and pairing works after it starts')
it('one attached browser is confirmed with AssignBrowser(profileId, instance)')
it('several attached browsers are offered as a list')
it('Continue stays disabled until a browser is assigned')
it('Copy buttons write the address and the folder to the clipboard')
```

- [ ] **Step 2: Run** `npx vitest run src/onboarding/steps/Browser.render.test.jsx`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/onboarding`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(onboarding): connect any Chromium browser`

### Task 13: Step What agents may do in your browser

**Files:**
- Create: `onboarding/steps/Permissions.jsx`, `Permissions.render.test.jsx`
- Modify: `onboarding/Onboarding.jsx`, `locales/en.json`

**Interfaces — Consumes:** `ApplyBrowserPermissionPreset(preset)`, `BrowserPermissions()`.

Three choices: "Ask before anything is submitted" (`ask`, preselected, marked Recommended), "Let agents work freely" (`free`), "Keep agents out of my browser" (`off`). Each has one line saying what it means in the words of `browserctl.Catalog()`. Note line: "This applies to every profile on this machine." Human-in-Loop line from CONTENT.md. The detailed setting is not linked from inside the flow (leaving mid-flow would lose the user's place); a plain line names where it lives: "You can set this per kind of agent later in Settings, under Browser permissions." Continue applies the chosen preset, then `mark('permissions')`. If the current levels match no preset, nothing is preselected and a line says "You have custom settings. Continue keeps them." and Continue applies nothing.

- [ ] **Step 1: Write the failing tests**

```jsx
it('preselects ask on a machine with the default levels')
it('Continue applies the chosen preset and marks the step')      // ApplyBrowserPermissionPreset('off')
it('custom levels are kept when nothing is chosen')              // preset binding not called, step still marked
it('a failed save shows the error and does not mark the step')
```

- [ ] **Step 2: Run** `npx vitest run src/onboarding/steps/Permissions.render.test.jsx`. Expected: FAIL.
- [ ] **Step 3: Implement.** **Step 4: Run** `npx vitest run src/onboarding`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(onboarding): browser permission presets step`

### Task 14: Starter templates and step Your first run

**Files:**
- Create: `internal/workflow/templates/starter_page_headings.json`, `starter_summarise_page.json`, `starter_browser_read.json`
- Create: `onboarding/steps/FirstRun.jsx`, `onboarding/starters.js`, `FirstRun.render.test.jsx`
- Modify: `onboarding/Onboarding.jsx`, `locales/en.json`
- Test: `internal/workflow/templates_starter_test.go`

**Interfaces — Produces:**

```js
// starters.js
export const STARTERS = [
  { templateId: 'starter_page_headings',  needs: [] },
  { templateId: 'starter_summarise_page', needs: ['agents'] },
  { templateId: 'starter_browser_read',   needs: ['agents', 'browser'] },
]
```

Templates (find node types and config with `monoagentcli node list` and `monoagentcli node schema <type>`; follow `templates/hello.json` for the file shape, each with `summary` and no `order`):
- `starter_page_headings`: manual trigger, fetch `https://example.com`, extract its headings. No agent, no credentials.
- `starter_summarise_page`: manual trigger, fetch the same page, one agent step that summarises it in three sentences.
- `starter_browser_read`: manual trigger, one agent step that opens `https://example.com` in the connected browser and reports the page title and first paragraph.

Step behaviour: a card is disabled when any step in `needs` is not `done`, with the line "Finish '<step title>' first." and a button `goTo(thatStep)`. Running: `CreateWorkflowFromTemplate(templateId)` then `RunWorkflow(id)`; node progress and the result come from the execution events and bindings NodeRunner already uses (reuse its subscription helper; do not write a second one). A failed run shows the error text and "Try again"; Continue is enabled after any finished run, failed or not, and `mark('firstRun')` is called only after a successful one.

- [ ] **Step 1: Write the failing tests**

```go
func TestStarterTemplatesLoadAndValidate(t *testing.T) // the three ids are in ListTemplates(), each has a non-empty Summary, and each passes the same validation the existing template tests apply to hello
```

```jsx
it('disables a card whose prerequisite step is not done and names the step')
it('runs the chosen template for real')                       // CreateWorkflowFromTemplate('starter_page_headings') then RunWorkflow(id)
it('lights nodes from execution events and shows the result')
it('a failed run shows the error, allows Continue and does not mark the step')
```

- [ ] **Step 2: Run** `go test ./internal/workflow/ -run Starter -v` and `npx vitest run src/onboarding/steps/FirstRun.render.test.jsx`. Expected: FAIL.
- [ ] **Step 3: Implement.** Run all three once for real in a scratch HOME and paste the three results in the commit body.
- [ ] **Step 4: Run** `go test ./internal/workflow/ && npx vitest run src/onboarding`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(onboarding): starter templates and first run step`

### Task 15: Step You're set, Finish-setup card, Settings entry

**Files:**
- Create: `onboarding/steps/Finish.jsx`, `onboarding/catalogue.js`, `Finish.render.test.jsx`
- Create: `pages/dashboard/FinishSetupCard.jsx`, `FinishSetupCard.render.test.jsx`
- Delete: `pages/dashboard/GetStartedCard.jsx`, `GetStartedCard.render.test.jsx`, and `getStarted.js` with its test if nothing else imports it
- Modify: `pages/Dashboard.jsx`, `components/settings/ProfileSection.jsx` or the section that holds general app actions (add "Run setup again"), `locales/en.json` (remove unused `dashboard.getStarted.*` keys)

**Interfaces — Produces:**

```js
// catalogue.js: the 11 cards from CONTENT.md
export const CATALOGUE = [/* { id, icon, titleKey, lineKey, page, opts? } */]
```

Each `page` must be a real key of the `pages` map in `App.jsx`; read the map and use its ids. Export `PAGE_IDS` (the map's keys) from `App.jsx` for the test below. Cards with no page of their own (API and MCP, schedules) point at the Settings section or page that holds them.

Finish: summary of the nine earlier steps, done or skipped, with "Finish this" (`goTo(step)`) on skipped ones. A catalogue card calls `complete()` then `onFinish({ page, opts })`. "Open MonoAgent" calls `complete()` then `onFinish()`.

FinishSetupCard: shown on the dashboard when any step is `skipped` or `todo` and the card has not been dismissed (keep the dismissal storage `GetStartedCard` uses); each row calls `openOnboarding(stepId)`. "Run setup again" in Settings calls `restart()` then `openOnboarding()`.

- [ ] **Step 1: Write the failing tests**

```jsx
it('summary shows each step as done or skipped')
it('Finish this goes to the skipped step')
it('a catalogue card completes onboarding and opens its page')   // onFinish called with { page: 'orgs', ... }
it('every catalogue page exists in the app')                      // each CATALOGUE page is in PAGE_IDS
it('Open MonoAgent completes and opens the app')
// FinishSetupCard
it('lists only steps that are skipped or todo and opens onboarding at the clicked one')
it('is not rendered when every step is done')
it('stays dismissed')
```

- [ ] **Step 2: Run** `npx vitest run src/onboarding/steps/Finish.render.test.jsx src/pages/dashboard`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run`. Expected: PASS for the whole frontend suite (the key scan included).
- [ ] **Step 5: Commit** `feat(onboarding): finish step, finish-setup card and run-setup-again`

---

## Phase 5: Fixes, docs, Spanish, later profiles

### Task 16: Fixes on this path

**Files:** as named per item; each item is one commit with its own test.

- [ ] **Fix 2: stale monomind banners.** `pages/Documents.jsx` and `pages/Agents.jsx` re-check `IsMonomindInitialized()` when the selected profile changes and on each `monomind:initProgress` event that reports completion. Tests: banner disappears after the event; banner reflects the new profile after a switch.
- [ ] **Fix 3: stuck spinner when signed out.** Every caller of `InitializeMonomindProfile` (`MonomindInitPrompt.jsx`, Documents, Agents) reads the return value through `callStep` and stops its spinner on a login-required or error answer. Test per caller.
- [ ] **Fix 4: Documents banner text.** Read what the upload path does before init and make the banner say that. Put the finding in the commit body.
- [ ] **Fix 5: `MonomindInitPrompt` text.** It says init always sends a Claude prompt; say that only an older monomind version does.
- [ ] **Fix 6: Agents page.** Add `FixMonomindButton` to its "monomind missing" state. Test: the button renders in that state. No sidebar entry.
- [ ] **Fix 7: health text.** Replace "the chrome-extension folder" with `~/.monoagent/extension` where the health check says it (find it in `internal/` doctor checks). Update the test that pins the string.
- [ ] **Fix 9: Chrome-and-Edge-only wording.** In `components/settings/ExtensionSection.jsx`, `BrowserBindingsSection.jsx`, `PairBrowser.jsx` and their `en.json` strings, say "any Chromium-based browser".
- [ ] **Fix 10: brand spelling.** "MonoAgent" in every screen this plan touched; grep `en.json` for `Mono Agent|Monoagent|monoAgent` within the keys those screens use.
- [ ] **Cleanup:** in `internal/profiledir/list.go`, remove the `Default` field only if `monograph_impact` and a grep show no reader in the CLI, the GUI or the extension; otherwise leave it and fix only the comment.
- [ ] **Run** `go test ./internal/... && cd wails-app && go test ./... && cd frontend && npx vitest run`. Expected: PASS except the known-red list.

### Task 17: Docs and Spanish

**Files:** `README.md`, `docs/BROWSER_PROFILES.md`, `wails-app/frontend/src/locales/es.json`

- [ ] **Step 1:** In both docs replace "Settings → Browsers" with the section's real name as the app shows it, and describe the extension install for any Chromium-based browser with the per-browser addresses from Task 5. Add a short "First run" paragraph to the README: the app opens with a setup flow and no profile exists until the user makes one.
- [ ] **Step 2:** Add every `onboarding.*` key and every other key added in this plan to `es.json`. Write a test `src/onboarding/locales.test.js`: every key under `onboarding` in `en.json` exists in `es.json` and no value is empty.
- [ ] **Step 3: Run** `npx vitest run src/onboarding/locales.test.js src/doctrine.test.js`. Expected: PASS.
- [ ] **Step 4: Commit** `docs: first run and any-Chromium install; feat(i18n): Spanish onboarding`

### Task 18: Short flow for a later profile

**Files:**
- Modify: `components/NewProfileModal.jsx` (after a successful create, when at least one other profile existed), `onboarding/Onboarding.jsx` (mode `profile`)
- Test: `onboarding/Onboarding.profileMode.render.test.jsx`, `components/NewProfileModal.render.test.jsx`

**Interfaces — Consumes:** `OnboardingContext.openOnboarding(undefined, 'profile')`, `PROFILE_STEPS`.

Mode `profile`: only `about`, `monomind`, `browser`, in that order, every one skippable, over the finished village (all plots built, sunrise off). The rail shows three steps. The last step's button is "Done"; it closes onboarding without touching `completed`. Step status comes from the same state, which follows the newly selected profile.

- [ ] **Step 1: Write the failing tests**

```jsx
it('profile mode shows only about, monomind and browser')
it('Done closes onboarding and does not call complete()')
it('creating a second profile opens the short flow')
it('creating the very first profile does not open the short flow')   // the full flow is already showing
```

- [ ] **Step 2: Run** the two test files. Expected: FAIL.
- [ ] **Step 3: Implement.** **Step 4: Run** `npx vitest run`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(onboarding): short setup for a later profile`

### Task 19: End-to-end check on a clean machine state

- [ ] **Step 1:** Build the CLI and the app against a scratch HOME (Global Constraints). Start with an empty `$SCRATCH/home`.
- [ ] **Step 2:** Go through all ten steps for real: system fixes, profile "Work", about you, monomind on, one agent checked, one browser connected and assigned, preset "ask", the three starters, catalogue card "Orgs". Record for each step what happened.
- [ ] **Step 3:** Quit mid-flow at step 5 and reopen: it resumes at the right step. Skip steps 6 and 7 on a second scratch HOME: the village shows outlines, the Finish-setup card lists both, and each row reopens the right step.
- [ ] **Step 4:** Copy a state directory that has existing profiles (made with the previous release build) into a third scratch HOME and open the app: no onboarding, profiles intact, "Run setup again" works.
- [ ] **Step 5:** With the OS set to reduce motion, repeat steps 1 to 3 of the flow: no animation, everything usable.
- [ ] **Step 6:** Run every suite once more: `go vet ./... && gofmt -l . && go test ./internal/... ./data/... && cd wails-app && go test ./... && cd frontend && npx vitest run && npx vite build`. Expected: clean, except the known-red list. Report results plainly, including anything that could not be checked.
