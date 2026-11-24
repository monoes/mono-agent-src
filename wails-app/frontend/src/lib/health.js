// System health: state shared by Settings › System health and the status-bar
// dot. Everything it knows comes from `monoagentcli doctor --json` (via the
// RunHealthCheck/RunHealthFix bindings); this module only stores, merges and
// re-requests it.
import { RunHealthCheck, RunHealthFix, InstallAgentRuntime, CancelHealthRun } from '../wailsjs/go/main/App'
import { subscribeEvent } from '../services/api.js'

export const BACKGROUND_INTERVAL_MS = 30 * 60 * 1000

// ── pure helpers (tested) ───────────────────────────────────────────────────

const PROBLEM = new Set(['warn', 'fail'])

// Groups in the order `monoagentcli doctor` registers them, so rows carried
// over from an earlier report (appended at the end) still show in place.
const GROUP_ORDER = ['core', 'monomind', 'runtimes', 'browser', 'automations', 'services', 'integrations', 'accounts']
const groupRank = g => { const i = GROUP_ORDER.indexOf(g); return i < 0 ? GROUP_ORDER.length : i }

/** Rows of a report nested under their parent row (report.results is flat). */
export function groupReport(report) {
  const groups = []
  const byGroup = new Map()
  const byId = new Map()
  for (const r of report?.results || []) {
    byId.set(r.id, { ...r, children: [] })
  }
  for (const r of report?.results || []) {
    const row = byId.get(r.id)
    if (r.parent && byId.has(r.parent)) {
      byId.get(r.parent).children.push(row)
      continue
    }
    if (!byGroup.has(r.group)) {
      const g = { group: r.group, rows: [] }
      byGroup.set(r.group, g)
      groups.push(g)
    }
    byGroup.get(r.group).rows.push(row)
  }
  for (const g of groups) {
    g.problems = countProblems(g.rows)
  }
  return groups.sort((a, b) => groupRank(a.group) - groupRank(b.group))
}

/**
 * The check a mode runs. background is the start-up and 30-minute check:
 * it leaves out the AI agent runtimes, whose scan runs every agent CLI and
 * writes their state outside mono-agent. The others are asked for.
 */
export function healthMode({ background = false, deep = false, projects = false } = {}) {
  if (background) return 'background'
  if (projects) return 'projects'
  if (deep) return 'deep'
  return 'local'
}

// What each mode's report already holds: a report never borrows rows from
// an older report of a mode it covers (saved = the runtime rows kept from
// the last check that scanned them).
const COVERS = {
  background: [],
  saved: [],
  local: ['background', 'saved'],
  deep: ['background', 'local', 'saved'],
  projects: ['background', 'local', 'saved'],
}

/**
 * One report from the latest report of each mode: the newest-started run
 * decides every row it has; rows only a richer or different mode produced
 * (monomind's own checks, network checks, project rows, the runtimes the
 * background check leaves out) are kept from older runs, marked carried.
 * runs: [{ mode, report, startedAt }].
 */
export function mergeReports(runs) {
  const sorted = runs.filter(r => r?.report).sort((a, b) => b.startedAt - a.startedAt)
  const base = sorted.find(r => r.mode !== 'saved') // saved rows alone are no report
  if (!base) return null
  const older = sorted.filter(r => r !== base)
  const results = [...(base.report.results || [])]
  const have = new Set(results.map(r => r.id))
  const carried = new Set()
  const covered = new Set([base.mode, ...(COVERS[base.mode] || [])])
  for (const run of older) {
    if (covered.has(run.mode) || run.report.profile_id !== base.report.profile_id) continue
    for (const r of run.report.results || []) {
      if (have.has(r.id)) continue
      // A newer run of the parent decides its children.
      if (r.parent && have.has(r.parent) && !carried.has(r.parent)) continue
      have.add(r.id)
      carried.add(r.id)
      results.push({ ...r, carried: true, checked_at: run.startedAt })
    }
    covered.add(run.mode)
    for (const m of COVERS[run.mode] || []) covered.add(m)
  }
  return { ...base.report, results }
}

/** The lock key of a fix: installing one runtime from the AI agents page
 * and from its health row share a key, so the two can't overlap. */
export function runKey(id) {
  const m = /^(agent\.install|runtimes\.install):(.+)$/.exec(id || '')
  return m ? `runtime:${m[2]}` : id
}

function countProblems(rows) {
  let n = 0
  for (const r of rows) {
    if (PROBLEM.has(r.status)) n++
    n += countProblems(r.children || [])
  }
  return n
}

/** Overall verdict for the banner and the status-bar dot. */
export function summarize(report) {
  const results = report?.results || []
  const required = results.filter(r => r.required && r.status === 'fail').length
  const fails = results.filter(r => r.status === 'fail').length
  const warns = results.filter(r => r.status === 'warn').length
  let level = 'ok'
  if (required > 0) level = 'broken'
  else if (fails + warns > 0) level = 'issues'
  return { level, required, fails, warns, issues: fails + warns, fixable: fixPlan(report).length }
}

/**
 * The fixes "Fix issues" applies, in report order, one per fix id:
 * auto and confirm ones that aren't optional. Manual and optional fixes are
 * only ever applied from their own row.
 */
export function fixPlan(report) {
  const seen = new Set()
  const plan = []
  for (const r of report?.results || []) {
    const f = r.fix
    if (!f || f.optional || f.safety === 'manual' || seen.has(f.id)) continue
    if (!PROBLEM.has(r.status)) continue
    seen.add(f.id)
    plan.push({ ...f, rowTitle: r.title })
  }
  return plan
}

/** True when two release builds differ (dev/git-describe builds never do). */
export function versionSkew(guiVersion, cliVersion) {
  const norm = v => String(v || '').trim().replace(/^v/, '')
  const a = norm(guiVersion)
  const b = norm(cliVersion)
  const dev = v => !v || v === 'dev' || /-g[0-9a-f]+/.test(v)
  if (dev(a) || dev(b)) return false
  return a !== b
}

// ── shared state ────────────────────────────────────────────────────────────

let state = { report: null, error: null, cliMissing: false, loading: false, lastRun: null, mode: null, checking: [] }
const listeners = new Set()

export function getHealth() { return state }

export function subscribeHealth(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

function set(patch) {
  state = { ...state, ...patch }
  for (const fn of listeners) fn(state)
}

const runs = {} // mode → { mode, report, startedAt, finishedAt }: the latest report of each
const active = new Map() // mode → { again, cancelled }: the check running now

// The runtime rows of the last check that scanned them, kept across app
// starts: the background check doesn't scan, and would otherwise show no
// runtimes until someone presses Check again.
const SAVED_RUNTIMES_KEY = 'monoagent:healthRuntimes:v1'

function loadSavedRuntimes() {
  try {
    const saved = JSON.parse(localStorage.getItem(SAVED_RUNTIMES_KEY) || 'null')
    if (saved?.results?.length) {
      runs.saved = { mode: 'saved', report: { v: 1, profile_id: saved.profile_id, results: saved.results }, startedAt: saved.at }
    }
  } catch { /* storage unavailable — nothing saved */ }
}
loadSavedRuntimes()

function saveRuntimes(report, at) {
  const results = (report.results || []).filter(r => r.group === 'runtimes')
  if (results.length === 0) return
  try {
    localStorage.setItem(SAVED_RUNTIMES_KEY, JSON.stringify({ at, profile_id: report.profile_id, results }))
  } catch { /* best effort */ }
}

function publish() {
  const report = mergeReports(Object.values(runs))
  const base = Object.values(runs).filter(r => r.mode !== 'saved').sort((a, b) => b.startedAt - a.startedAt)[0]
  set({ report, error: null, cliMissing: false, lastRun: base?.finishedAt || null, mode: base?.mode || null })
}

function syncLoading() {
  set({ loading: active.size > 0, checking: [...active.keys()] })
}

/**
 * Run the checks: background (start-up and every 30 minutes; no runtime
 * scan), or what the person asked for — deep adds network checks, projects
 * monomind's checks per project, neither is Check again. A mode's report
 * replaces only that mode's last one (see mergeReports). A request while
 * the same mode runs is run again after it, so the latest request wins
 * (a re-check after a fix never gets a report from before the fix).
 */
export function runHealth(opts = {}) {
  const mode = healthMode(opts)
  const cur = active.get(mode)
  if (cur) {
    if (!cur.again) {
      let resolve
      const promise = new Promise(r => { resolve = r })
      cur.again = { promise, resolve }
    }
    return cur.again.promise
  }
  return startCheck(mode)
}

let lastStarted = 0

function startCheck(mode) {
  const entry = { again: null, cancelled: false }
  active.set(mode, entry)
  // Strictly increasing, so two runs started in the same millisecond still
  // have an order (the later request wins).
  const startedAt = lastStarted = Math.max(Date.now(), lastStarted + 1)
  syncLoading()
  return RunHealthCheck(mode)
    .then(s => {
      const data = JSON.parse(s)
      if (data.cancelled || entry.cancelled) return
      if (data.error) { set({ error: data.error, cliMissing: !!data.cli_missing }); return }
      runs[mode] = { mode, report: data, startedAt, finishedAt: Date.now() }
      if (mode !== 'background') saveRuntimes(data, startedAt)
      publish()
    })
    .catch(e => set({ error: String(e) }))
    .finally(() => {
      active.delete(mode)
      const again = entry.again
      if (again) {
        if (entry.cancelled) again.resolve()
        else startCheck(mode).then(again.resolve)
      }
      syncLoading()
    })
}

/** Stop the running checks (the CLI gets SIGTERM, then a grace period).
 * The last report stays. */
export function cancelHealthCheck() {
  const calls = []
  for (const [mode, entry] of active) {
    entry.cancelled = true
    calls.push(CancelHealthRun(`check:${mode}`).catch(() => {}))
  }
  return Promise.all(calls)
}

const runningFixes = new Set() // run keys (see runKey)

/** True while a fix runs — lets the app hold back navigation it would
 * otherwise do (a fix like `monomind init` creates a sample org). */
export function isFixing() { return runningFixes.size > 0 }

/** True while this fix (or another install of the same runtime) runs. */
export function isFixRunning(fixId) { return runningFixes.has(runKey(fixId)) }

/** Stop a running fix or install; its run resolves { ok: false, cancelled: true }. */
export function cancelFix(fixId) {
  return CancelHealthRun(fixId).catch(() => {})
}

/**
 * Apply one fix; onLine gets each progress line. Resolves to
 * { ok: true } or { ok: false, message, cancelled }. A fix already running is not
 * started again (two daemons, two npm installs into one folder).
 */
export function runFix(fixId, onLine) {
  return runStreamed(fixId, () => RunHealthFix(fixId), onLine)
}

/**
 * Install (update: reinstall) an AI agent runtime via `monoagentcli agent
 * install`; same progress events and result shape as runFix. approveURL is
 * the vendor script the person was shown, for a script install.
 */
export function installRuntime(runtimeId, update, onLine, approveURL = '') {
  return runStreamed(`agent.install:${runtimeId}`, () => InstallAgentRuntime(runtimeId, !!update, approveURL), onLine)
}

// A streamed fix that stays silent this long is given up on (installs can be
// quiet for minutes, so this is generous).
export const FIX_IDLE_TIMEOUT_MS = 10 * 60 * 1000

// runStreamed starts a streamed CLI command and follows its
// health:fixProgress events (keyed by fix_id) to the final done/error. One
// run per key: a second start while one runs is refused.
function runStreamed(key, start, onLine) {
  const lock = runKey(key)
  if (runningFixes.has(lock)) return Promise.resolve({ ok: false, busy: true, message: 'already running' })
  runningFixes.add(lock)
  return new Promise(resolve => {
    // Watchdog: if the terminal event is lost (CLI killed, window event
    // dropped) no progress ever arrives; release the lock after a quiet spell.
    let watchdog = null
    const arm = () => {
      clearTimeout(watchdog)
      watchdog = setTimeout(() => {
        off()
        resolve({ ok: false, message: 'no progress from the command; gave up waiting' })
      }, FIX_IDLE_TIMEOUT_MS)
    }
    const off = subscribeEvent('health:fixProgress', ev => {
      if (!ev || ev.fix_id !== key) return
      if (ev.kind === 'line') { arm(); onLine?.(ev.message || ''); return }
      clearTimeout(watchdog)
      off()
      resolve(ev.kind === 'done' ? { ok: true, message: ev.message || '' } : { ok: false, cancelled: !!ev.cancelled, message: ev.message || 'failed' })
    })
    start()
      .then(s => {
        const r = JSON.parse(s)
        if (r.error) { clearTimeout(watchdog); off(); resolve({ ok: false, message: r.error }) }
      })
      .catch(e => { clearTimeout(watchdog); off(); resolve({ ok: false, message: String(e) }) })
    arm()
  }).finally(() => { runningFixes.delete(lock) })
}

let timer = null
let subscribers = 0

/** Start the start-up + every-30-minutes background check (idempotent). It
 * runs `doctor --skip-group runtimes`, which writes nothing outside
 * ~/.monoagent. */
export function startBackgroundHealth() {
  subscribers++
  if (!timer) {
    runHealth({ background: true })
    timer = setInterval(() => { runHealth({ background: true }) }, BACKGROUND_INTERVAL_MS)
  }
  let released = false
  return () => {
    if (released) return
    released = true
    if (--subscribers === 0) { clearInterval(timer); timer = null }
  }
}
