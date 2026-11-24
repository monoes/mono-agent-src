// Help content: plain English, no React. Every string lives under a full
// literal key in HELP_KEYS (key -> English) so the es parity test can see it.
// Keys are built by one helper, so the explicit list is exported: a Help
// Keys test should iterate HELP_KEYS, not scan this file.
//
// Every command quoted here was checked against `monoagentcli ... --help`.
// Permission wording follows the permission map of the 2026-11-22 review (r28).

export const PAGE_IDS = [
  'dashboard', 'noderunner', 'orgs', 'people', 'publication', 'communications',
  'vault', 'secretsVault', 'applications', 'documents', 'info', 'tasks',
  'connections', 'ai', 'logs', 'settings',
]

const go = (target, params) => ({ kind: 'goto', target, ...(params ? { params } : {}) })
const coder = (prompt) => ({ kind: 'coder', prompt })
const step = (text, action) => (action ? { text, action } : { text })

export const TOPICS = [
  {
    id: 'firstSteps',
    emoji: '👋',
    title: 'First steps',
    blurb: 'Sign in, make a profile, run something harmless.',
    quip: 'Everyone starts with zero profiles. It is a very relaxed starting line.',
    steps: [
      step('Sign in to monoes.me first: MonoAgent needs an account before anything runs. In a terminal, run monoagentcli account login.', go('settings')),
      step('Create a profile: the dashboard shows what happens inside one. Run monoagentcli profile create <name>, or use the dashboard.', go('dashboard')),
      step('Start from a template and press Run. The hello template needs no credentials: monoagentcli workflow templates run hello --profile <name>.', go('noderunner', { openTemplates: true })),
      step('Hid the get-started checklist and want it back? This brings it back on the dashboard.', go('dashboard', { showGetStarted: true })),
    ],
  },
  {
    id: 'profiles',
    emoji: '🗂️',
    title: 'Profiles',
    blurb: 'Everything you own lives in a profile, and every call names it.',
    quip: 'There is no default profile. Yes, you will type --profile a lot. It keeps your accounts from meeting each other at parties.',
    steps: [
      step('A profile holds its own sessions, credentials, workflows, vault and people. Commands that act on one take --profile <id|name>.', go('dashboard')),
      step('List yours with monoagentcli profile list, and see what one holds with monoagentcli profile context --profile <name>.', go('dashboard')),
      step('Machine-wide commands (version, update, config, account) do not take a profile.', go('settings')),
    ],
  },
  {
    id: 'connections',
    emoji: '🔌',
    title: 'Connecting accounts',
    blurb: 'Link a platform once; workflows reuse it.',
    quip: 'Credentials go in the vault, not in a sticky note. The vault appreciates the compliment.',
    steps: [
      step('Open Connections to see which platforms you have saved and how each one connects.', go('connections')),
      step('From a terminal, monoagentcli connect list --all shows every supported platform, and monoagentcli connect show <platform> shows its methods and fields.', go('connections')),
      step('Test a saved connection with monoagentcli connect test <platform> to re-validate its credentials. Web automations run in your own browser through the extension, so log in there too.', go('connections', { tab: 'session' })),
    ],
  },
  {
    id: 'workflows',
    emoji: '🧩',
    title: 'Workflows and templates',
    blurb: 'Build it once, run it whenever.',
    quip: 'A workflow is a recipe. A template is a recipe someone else already burnt the first batch of.',
    steps: [
      step('Pick a template and press Run. Browse them in the workflow editor.', go('noderunner', { openTemplates: true })),
      step('From a terminal, monoagentcli workflow templates list shows the bundled templates and monoagentcli workflow templates show <id> shows inputs and the exact run command.', go('noderunner')),
      step('Schedules and webhooks need the background service: monoagentcli daemon keeps them alive in the foreground.', go('noderunner')),
      step('Ask a coder agent to build one for you, then validate it before the first run.', coder('Using the monoagent-build skill, create a workflow in my profile that runs every weekday at 09:00 and saves a short summary of my unread messages to the task board. Look up the node types and their schemas first, validate the workflow, run it once and show me each node\'s output before enabling the schedule.')),
    ],
  },
  {
    id: 'tasks',
    emoji: '📋',
    title: 'The task board',
    blurb: 'A kanban board for you and your AI agents.',
    quip: 'Moving a card to Ready is how you tell an agent "go on, then". Moving it back is how you change your mind.',
    steps: [
      step('The board has five columns. New tasks land in the Inbox, and you approve them to Ready, where an AI agent may take them.', go('tasks')),
      step('Add one from a terminal: monoagentcli task add "<title>" --profile <name> (add --ready to skip the Inbox).', go('tasks')),
      step('Agents hand work back for review. You verify it or send it back; nothing is done until you say so.', go('tasks')),
    ],
  },
  {
    id: 'hil',
    emoji: '✋',
    title: 'Approvals (human in the loop)',
    blurb: 'Anything an agent wants to send waits for your yes.',
    quip: 'The Approve button is deliberately boring. Boring buttons are safe buttons.',
    steps: [
      step('Pending approvals show up in Communications. Read the card, then approve or reject.', go('communications')),
      step('From a terminal, monoagentcli hil list shows what is waiting, and monoagentcli hil approve or hil reject answers one.', go('communications')),
      step('A HIL step with no timeout waits forever (the default timeout is 0, which means unlimited), so a run and its context sit parked until someone answers. Set timeout_minutes if that matters to you.', go('noderunner')),
      step('auto_decide lets Jev approve items it is confident about on your behalf. It needs Jev enabled for approvals; check its settings before relying on it.', go('settings', { section: 'jev' })),
    ],
    danger: { level: 3, quip: 'Auto-approve means a polite wrong guess goes out as you. Review what it decided at least once.' },
  },
  {
    id: 'orgs',
    emoji: '🏢',
    title: 'Orgs',
    blurb: 'Teams of agents with roles, budgets and a boss.',
    quip: 'You are the board of directors. The agents are very keen interns.',
    steps: [
      step('Orgs and agents need monomind initialized in this profile\'s folder first.', go('orgs')),
      step('Open an org to see its chart, spend and pending approvals.', go('orgs', { tab: 'overview' })),
      step('Ask a coder agent to design one for a specific job.', coder('Create an org in my profile named "inbox-triage" with a boss role and two worker roles: one that reads new messages and drafts replies, one that files tasks on my task board. Keep autonomy on manual so every send waits for my approval, set a small budget, validate the org and show me the role chart before running it.')),
    ],
    danger: { level: 4, quip: 'A role with full access is an employee with your house key who never clocks out, limited only by budgets. Keep roles scoped.' },
  },
  {
    id: 'coderModes',
    emoji: '🧑‍💻',
    title: 'Coder chats and their modes',
    blurb: 'Off, mission or open-ended: pick before the first message.',
    quip: 'The mode is chosen before you say hello and cannot change afterwards. Choose like you are picking a haircut.',
    steps: [
      step('Coder mode is off until you turn it on in Settings. From a terminal, monoagentcli coder status shows where it stands.', go('settings')),
      step('Chat modes: Off is a plain chat. Mission means the lead plans the work and goes on until you close it. Open-ended runs until you stop it.', go('tasks')),
      step('Open-ended is offered only while the coder binding is on and can start on this machine; the chat will tell you why if it cannot.', go('settings')),
      step('Raise a chat\'s access level one step at a time: scoped, coding, workspace, full. Only you can raise it.', go('ai')),
      step('Not sure where to start? Ask a coder chat to walk the settings with you.', coder('Run monoagentcli coder status and monoagentcli doctor, then explain in plain language what coder mode would be allowed to do on this machine and which default access level new chats would get. Do not enable anything; just report and recommend settings.')),
    ],
    danger: { level: 5, quip: 'Coder mode on means the agent can run any command and change any file as you. Start at the lowest level that does the job.' },
  },
  {
    id: 'extension',
    emoji: '🧭',
    title: 'The browser extension',
    blurb: 'Lets MonoAgent drive your own browser.',
    quip: 'It works inside your real browser, so your logins come along. That is the feature and the warning in one sentence.',
    steps: [
      step('Install: monoagentcli extension install unpacks the bridge extension for your browser; load it as an unpacked extension.', go('settings')),
      step('Pair: monoagentcli extension pair prints the token to paste into the extension side panel.', go('settings')),
      step('Check: monoagentcli extension status says whether a bridge is running and the extension is attached.', go('settings', { section: 'health' })),
      step('Limit what agents may do in it with monoagentcli browser permissions set <kind|all> <level>. Levels, mildest first: off, ask, dev, full, full-no-approvals.', go('settings')),
    ],
    danger: { level: 4, quip: 'A paired browser is not restricted to particular profiles by default. Restrict it if more than one person or agent can reach the token.' },
  },
  {
    id: 'vault',
    emoji: '🔐',
    title: 'Vault and secrets',
    blurb: 'Passwords and logins, encrypted, referenced by name.',
    quip: 'Agents get to use a secret without seeing it. That is not distrust, it is good manners.',
    steps: [
      step('Add secrets and logins in the Secrets vault; list shows metadata only, never values.', go('secretsVault')),
      step('Agent access defaults to strict. use lets an agent use secrets without retrieving plaintext; reveal lets it read them. Set it with monoagentcli secret agent-access strict|use|reveal.', go('secretsVault')),
      step('Every agent read is audited by name, never by value: monoagentcli secret agent-access audit.', go('secretsVault')),
    ],
    danger: { level: 4, quip: 'Reveal lets an agent read your passwords in plaintext. Scope it by secret, host and expiry, or leave it on strict.' },
  },
  {
    id: 'apiGateway',
    emoji: '🚪',
    title: 'API gateway',
    blurb: 'An OpenAI-compatible endpoint with keys you control.',
    quip: 'It listens on your machine only, unless you say otherwise. Say otherwise carefully.',
    steps: [
      step('Check where it listens and whether it answers: monoagentcli api status --profile <name>.', go('settings')),
      step('Create a key (shown once): monoagentcli api key create --profile <name>. Revoke it any time with api key revoke.', go('settings')),
      step('List the models the gateway would serve: monoagentcli api models.', go('settings')),
    ],
  },
  {
    id: 'images',
    emoji: '🖼️',
    title: 'Images and the media vault',
    blurb: 'Generate pictures and keep them in one place.',
    quip: 'The media vault does not judge your 47 versions of the same wizard.',
    steps: [
      step('Generated and added images, audio and video live in the media vault.', go('vault')),
      step('Log in to Gemini once with monoagentcli login gemini, then run the single-image template: monoagentcli workflow templates run gemimg --input \'{"prompt":"a red bicycle"}\' --profile <name>.', go('noderunner', { openTemplates: true })),
      step('Browse from a terminal with monoagentcli media list --profile <name>.', go('vault')),
    ],
  },
  {
    id: 'troubleshooting',
    emoji: '🩺',
    title: 'Troubleshooting and doctor',
    blurb: 'Something is off? Ask the doctor first.',
    quip: 'The doctor does not say "have you tried turning it off and on again" until it has actually checked.',
    steps: [
      step('Run monoagentcli doctor to check everything MonoAgent needs on this machine, and monoagentcli doctor --fix to apply the fixes.', go('settings', { section: 'health' })),
      step('Read the logs for what actually happened.', go('logs')),
      step('monoagentcli status gives an overview, and monoagentcli summary lists what needs your attention.', go('dashboard')),
      step('Still stuck? Hand the diagnosis to a coder agent.', coder('Run monoagentcli doctor and monoagentcli status, read the most recent errors in the logs, and tell me what is wrong and the smallest fix. Apply only fixes that doctor marks as automatic, and ask me before anything else.')),
    ],
  },
  {
    id: 'updates',
    emoji: '⬆️',
    title: 'Updates',
    blurb: 'Stay current without surprises.',
    quip: 'New versions arrive often. Checking costs nothing; installing is your call.',
    steps: [
      step('See what you run and whether a newer release exists in Settings.', go('settings', { section: 'version' })),
      step('From a terminal, monoagentcli update --check only reports; monoagentcli update installs.', go('settings', { section: 'version' })),
    ],
  },
]

// The Permission Pit: each stage digs deeper. Consequences follow the
// permission map; none of the humor replaces the warning.
export const PIT_STAGES = [
  {
    level: 1,
    permission: 'Ask first (browser level ask, vault strict)',
    label: 'The Welcome Mat',
    consequence: 'Agents ask before touching a new site, and they use no secret they cannot name. The worst outcome is a few extra clicks from you.',
    mascotMood: 'happy',
  },
  {
    level: 2,
    permission: 'Chat access: scoped, then coding',
    label: 'The Garden Gate',
    consequence: 'Each step up hands the chat more of your computer. Only you can raise it, but once raised it keeps the power until you lower it.',
    mascotMood: 'curious',
  },
  {
    level: 3,
    permission: 'Browser level full, vault use',
    label: 'The Back Door',
    consequence: 'An agent can work any site you are logged into, and a form submit on a real site still waits for your approval. Secrets can be used without being read.',
    mascotMood: 'nervous',
  },
  {
    level: 4,
    permission: 'Browser full-no-approvals, vault reveal, org role full access',
    label: 'The Spare Key',
    consequence: 'Forms go through without asking and page scripts can read a site\'s cookies, so an agent can post, buy or send as you. Reveal means it can read your passwords in plaintext.',
    mascotMood: 'sweating',
  },
  {
    level: 5,
    permission: 'Coder mode on, full access, HIL no timeout, auto-approve',
    label: 'The Whole House',
    consequence: 'The agent can run any command and change any file as you, with no asking, so it can delete your home folder as politely as it can fix a typo. Approvals with no timeout wait forever, and auto-approve lets an AI click Approve on your behalf.',
    mascotMood: 'panicking',
  },
]

export const EASTER_EGGS = [
  { trigger: 'konami', line: 'Up, up, down, down, left, right, left, right, B, A. You have unlocked: nothing, but you did it with style.' },
  { trigger: 'mascot-click-7', line: 'Seven pokes. The mascot would like you to know that it is a very good listener and a very poor punching bag.' },
  { trigger: 'type:sudo', line: 'Nice try. Permissions here are raised by you, in Settings or with --yes-i-understand, and not by magic words.' },
  { trigger: 'type:rm -rf', line: 'Typing it is free; running it is not. Agents with full access can really do this, which is why the pit exists.' },
  { trigger: 'type:please', line: 'Politeness noted. Agents still ask before sending anything as you, whatever your manners.' },
  { trigger: 'type:hello', line: 'Hello. The hello template says hi back, and it needs no credentials.' },
  { trigger: 'type:xyzzy', line: 'Nothing happens. (Some adventures never end.)' },
  { trigger: 'type:42', line: 'The answer, but you will want the question: which profile is this for?' },
  { trigger: 'type:vim', line: 'You can leave. The help page has no modes, only a polite exit button.' },
  { trigger: 'type:exit', line: 'There is no shell in here, but the sidebar is on the left.' },
  { trigger: 'type:matrix', line: 'There is no spoon, only workflows, and they are all visible in the editor.' },
  { trigger: 'type:coffee', line: 'Brewing. Agents work for tokens, not caffeine, but you go ahead.' },
  { trigger: 'type:bug', line: 'Found one? Run doctor first, then tell us. Both help.' },
  { trigger: 'idle-60s', line: 'Still there? Take your time. Nothing here runs while you read.' },
  { trigger: 'pit-bottom-reached', line: 'You reached the bottom. Every stage above it is cheaper, and almost always enough.' },
  { trigger: 'all-topics-read', line: 'You have read every topic. That makes you the most prepared person who has ever pressed Run.' },
]

const KEY_BASE = 'help'
export const HELP_KEYS = (() => {
  const out = {}
  for (const t of TOPICS) {
    const p = `${KEY_BASE}.topics.${t.id}`
    out[`${p}.title`] = t.title
    out[`${p}.blurb`] = t.blurb
    out[`${p}.quip`] = t.quip
    t.steps.forEach((s, i) => {
      out[`${p}.steps.${i}.text`] = s.text
      if (s.action?.kind === 'coder') out[`${p}.steps.${i}.prompt`] = s.action.prompt
    })
    if (t.danger) out[`${p}.danger.quip`] = t.danger.quip
  }
  for (const s of PIT_STAGES) {
    const p = `${KEY_BASE}.pit.${s.level}`
    out[`${p}.permission`] = s.permission
    out[`${p}.label`] = s.label
    out[`${p}.consequence`] = s.consequence
  }
  EASTER_EGGS.forEach((e, i) => { out[`${KEY_BASE}.eggs.${i}.line`] = e.line })
  return out
})()
