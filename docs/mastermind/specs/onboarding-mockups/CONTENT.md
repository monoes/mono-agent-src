# Onboarding mockups: shared content

Three HTML mockups (`style-a-operator.html`, `style-b-storybook.html`,
`style-c-cinematic.html`) show the SAME first-run flow in three visual styles.
This file is the single source for the steps, copy and simulated behaviour.
The mockups are throwaway: nothing here is product code.

## Product in one paragraph

MonoAgent is a desktop app (Wails window, about 1280x800, resizable) that runs
workflow automations and teams of AI agents on the user's own machine. The
logo is a monkey. Onboarding runs full-window the first time the app opens,
after sign-in, before the main app is shown.

## Rules for every mockup

- One self-contained `.html` file: inline CSS and JS, no network requests of
  any kind (no CDN, no web fonts, no remote images). System font stacks only.
- Every illustration is inline SVG drawn for that step, in the style's own
  art direction. Each step has one main illustration that is clearly about
  that step. (Final art will be generated later; these stand in for it.)
- Works at 1280x800 and stays usable down to 900px wide.
- Persistent: progress indicator showing all 10 steps and the current one;
  Back; primary Continue; "Skip for now" on every step except Welcome,
  Create profile and Finish. A skipped step shows as skipped (not done) in
  the progress indicator and in the final summary.
- Continue is disabled until the step's requirement is met (stated per step).
- All "work" is simulated with timers and in-memory state. Simulated progress
  takes 2 to 4 seconds and shows what is happening in plain words.
- Keyboard: Enter continues when allowed; visible focus rings; respects
  `prefers-reduced-motion`.
- Copy: plain, short sentences. No marketing fluff. Use the copy below as the
  base; tone may be adapted to the style but facts may not change.
- A small "Mockup" control (corner) lets the reviewer jump to any step and
  reset the flow.

## Steps

### 1. Welcome
- Title: "Welcome to MonoAgent"
- Body: "Automations and AI agents that run on your own machine. Setup takes
  about five minutes. You can skip any step and come back to it later."
- Shows the list of what will be set up: your profile, the engine (monomind),
  an AI agent, your browser, and a first run.
- Button: "Get started".

### 2. System check
- Explains: "MonoAgent needs a few things on this machine."
- Runs automatically, one row at a time:
  - MonoAgent command line: found
  - Node.js: found
  - monomind (the engine behind every AI step): NOT installed, with a
    "Install" button. Clicking it simulates the install and turns the row green.
  - Background service: not running, with a "Start" button. Explains: "Runs
    schedules and triggers, and lets the browser extension connect."
- Continue enabled when all rows are green.

### 3. Create your profile (cannot be skipped)
- Explains what a profile is: "A profile is a separate workspace. It has its
  own folder, documents, secrets, agents' memory, orgs and browser. Make one
  for each context you keep apart, for example Personal, Work, or a client."
- There is no ready-made profile; the user makes the first one.
- Fields: name (required; suggestions as chips: Personal, Work, Client name),
  avatar/icon picker (8 choices), folder location (default
  `~/.monoagent/profiles/<name>`, with a "Change" button that does nothing).
- A live preview card shows the profile as it will appear in the sidebar.
- Validation: empty name, and the name "default" is refused with "Pick a name
  that says what this profile is for."
- Continue: "Create profile".

### 4. About you (skippable)
- Explains: "Agents read this before they work for you, so they write and act
  as you would."
- Fields: your name, role, company or project, and "What do you want help
  with?" (multi-select chips: Outreach, Content, Research, Coding, Inbox,
  Operations).
- Pointer: "You can edit this later in the Info tab."

### 5. Turn on monomind
- Explains what monomind is: "monomind is the engine behind every AI step in
  MonoAgent."
- Two-part explainer with its own illustration each:
  - For documents: "Files you add to this profile are indexed into its Second
    Brain. Agents search it, and a knowledge graph links what belongs
    together."
  - For orgs: "An org is a team of agents with roles, goals and a boss. Orgs
    run on monomind and keep their memory in this profile."
- Button: "Turn on for <profile name>". Simulated progress in three lines:
  "Preparing the profile folder", "Setting up memory and knowledge graph",
  "Registering with your agents". Then a success state.
- Pointer: "You'll find this later in the Documents tab."
- Continue enabled after success.

### 6. Your agents
- Explains: "Agents are the AI coding tools installed on this machine.
  MonoAgent drives them. You need at least one."
- Simulated scan, then tiles: Claude Code (installed, signed in), Codex
  (installed, "Sign in needed" with a hint), OpenCode (not installed, Install
  button), GitHub Copilot CLI (not installed, Install button).
- Clicking Install simulates it and then shows the sign-in hint.
- A reviewer toggle "Simulate: none installed" shows the empty state: "No
  agents found. Install one to continue." with a recommended pick.
- Pointer: "You'll find this later in Settings, under AI agents."
- Continue enabled when at least one agent is installed and signed in.

### 7. Connect your browser
- Explains: "The extension lets agents work in your real browser. They use
  the accounts you are already signed in to there, so nothing needs your
  passwords."
- Sub-steps, shown as a small stepper inside the step:
  1. Pick browser. Lead line: "Works with any Chromium-based browser."
     Choices: Chrome, Edge, Brave, Arc, Vivaldi, Opera, and "Other Chromium
     browser". Browsers found on this machine are marked "Installed" and
     listed first (simulate Chrome, Edge and Brave as installed).
  2. Install, with instructions that change with the chosen browser:
     - the extensions page address to open, with a Copy button:
       Chrome `chrome://extensions`, Edge `edge://extensions`, Brave
       `brave://extensions`, Arc `arc://extensions`, Vivaldi
       `vivaldi://extensions`, Opera `opera://extensions`, Other: "Open your
       browser's extensions page (usually Menu, then Extensions)".
     - where the Developer mode switch is: top right in Chrome, Brave, Arc,
       Vivaldi and Opera; left sidebar in Edge.
     - the button name: "Load unpacked" everywhere.
     - then "choose this folder: `~/.monoagent/extension`" with a Copy button.
     The illustration shows where each control is for the chosen browser.
  3. Pair: "Pair extension" button; simulated wait, then "Connected".
  4. Choose the browser profile for this MonoAgent profile: a list of
     detected browser profiles (for example "Morteza (Work)", "Personal",
     "Client X"). Explains: "Pick the browser profile whose logged-in
     accounts belong to <profile name>. Agents working in <profile name>
     will only use that browser profile."
- Diagram: MonoAgent profile <-> browser profile <-> logged-in sites.
- Pointer: "You'll find this later in Settings, under Browser Extension."
- Continue enabled after a browser profile is chosen.

## Chosen style: C, with a growing world (owner's direction)

- No monkey mascot anywhere in the scene or the illustrations.
- The animated scene is the star: every finished step visibly BUILDS
  something into the world, with a short build animation (pieces rise, grow
  or assemble), and it stays for the rest of the flow. A skipped step leaves
  its plot as an unbuilt outline that fills in if the step is done later.
- By the last step the world is a living village: several houses, a
  windmill with turning sails, a campfire with flickering flame and sparks,
  lit windows and lanterns/string lights, trees, flowers, and humans and
  robots walking around among them (walk cycles along paths, not sliding
  static figures).
- Each step's build should relate to the step (for example: Create profile
  builds the first house; monomind builds the windmill, the engine; Agents
  brings the robots; Connect your browser adds a bridge or lit path to a
  second plot; first run lights the fire and the lights).

### Second round (owner's direction)

- More inhabitants: clearly more humans and robots, arriving in waves as
  steps finish, so the final village feels busy.
- They DO things, not only walk: for example a robot hammering or carrying
  planks to the plot being built, a human watering flowers, two chatting, a
  robot handing a human a letter, someone fishing off the bridge, a human
  and robot sitting by the fire, a robot waving at the viewer.
- Between steps: a real transition moment, not just a fade. On Continue,
  inhabitants react and work on the build (run to the plot, scaffolding
  goes up, pieces are carried in, a small celebration when it is done),
  and the camera or lighting moves with it. Keep it under about 2.5
  seconds and never block the next step's form from being usable.
- Hidden things to discover: the scene is clickable. Clicking things makes
  them react (windmill spins fast, a house's window lights and someone
  waves, the fire flares with sparks, a tree drops leaves or a bird flies
  out, a robot does a little dance, a human waves back, flowers bloom, a
  shooting star when the sky is clicked). Add a few rarer secrets that are
  not obvious (for example a cat that appears only after several clicks on
  a roof, a robot that follows the pointer for a while, a hidden firefly
  jar). No hints in the UI beyond a pointer cursor on hover; a small
  "found 3 of 12" counter appears only after the first discovery.
- None of this may steal focus from the form, capture keyboard input, or
  slow typing. All of it is off under reduced motion (clicks may still
  give a non-moving response).

### 8. What agents may do in your browser
- Explains: "You decide how far agents go on their own."
- Three rows, each with a choice of "Allow", "Ask me first", "Never":
  - Read pages: default Allow
  - Click and type: default Allow
  - Submit forms, post, upload: default Ask me first
- Explains the approval queue: "When an agent needs your OK, it waits in
  Human-in-Loop until you approve or reject."
- Continue always enabled.

### 9. Your first run
- Explains: "Pick one to see MonoAgent work."
- Three cards: "Summarise a web page", "Draft a post from a link", "Research
  a company". Choosing one simulates a short run with 3 to 4 visible nodes
  lighting up in order, then shows a short fake result.
- Continue enabled after a run finishes (or skip).

### 10. You're set
- Summary of every step: done or skipped, with "Finish this" links for
  skipped ones (links do nothing).
- Catalogue: "What you can do with MonoAgent", as a grid of cards, each with
  an icon, one line, and where it lives:
  - Workflows: build automations on a canvas from 100+ nodes and templates.
  - Orgs: run a team of agents with roles and goals.
  - Chat and coder: talk to an agent or have it write code.
  - Documents: your Second Brain; add files and ask questions.
  - People and Communications: contacts, synced email and messages.
  - Social automations: Instagram, LinkedIn, X, TikTok, Hacker News, Product Hunt.
  - Schedules and triggers: run things on a timer or an event.
  - Tasks: a board agents can pick work from.
  - Secrets vault: keys and logins, kept per profile.
  - API and MCP: an OpenAI-compatible API and an MCP server for other tools.
  - Library: install automations and templates from monoes.me.
- Button: "Open MonoAgent".
