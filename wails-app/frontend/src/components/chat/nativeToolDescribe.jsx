import {
  Terminal, FilePen, FilePlus, FileText, Search, Globe, Bot, ListChecks, Plug, Wrench, FileDiff,
} from 'lucide-react'
import { Tag, Section, ResultSection, DiffView, PatchDiffView, patchLines, preStyle, sectionLabel, mono, CYAN } from './nativeToolParts.jsx'
import { diffLines, diffStats } from './unifiedDiff.js'
import { monoagentCommand, cliTitle, vaultRelated, isSingleCommand } from './toolLinks.js'
import i18nOr from '../../lib/i18nOr.js'

// How one native tool call reads: by the normalized kind every coder
// runtime reports on tool.started (shell, edit, write, read, search, web,
// mcp, task, todo, patch, other), else by Claude Code's tool name for
// events from before kinds existed. Inputs follow the canonical keys per
// kind (see the agent-exec protocol); Claude's calls keep their native
// input, which uses the same keys plus a few of its own (MultiEdit's
// edits, Read's offset/limit, WebFetch's prompt).

const CLAUDE_KINDS = {
  Bash: 'shell', Edit: 'edit', MultiEdit: 'edit', Write: 'write', Read: 'read',
  Glob: 'search', Grep: 'search', WebFetch: 'web', WebSearch: 'web',
  Task: 'task', Agent: 'task', TodoWrite: 'todo',
}

// kindOf is the call's kind: the reported one, else Claude's name mapped.
export function kindOf(call) {
  if (call.kind) return call.kind
  const name = call.name || ''
  if (CLAUDE_KINDS[name]) return CLAUDE_KINDS[name]
  if (name.startsWith('mcp__')) return 'mcp'
  return 'other'
}

const EXISTING_KEYS = { overwrite: 'ui7Chat.file_overwrite', modified: 'ui7Chat.file_modified' }
// A patch's actions; only the displayed word is translated.
const PATCH_ACTION_KEYS = { add: 'ui7Chat.patch_add', delete: 'ui7Chat.patch_delete', update: 'ui7Chat.patch_update' }
const patchAction = a => (Object.hasOwn(PATCH_ACTION_KEYS, a) ? i18nOr(PATCH_ACTION_KEYS[a]) : a)

// fileTags labels a write/edit by whether its file existed when the call
// started (tool.started's fileExisted, absent when unknown): "new file",
// else existingLabel ("overwrite" for a write, "modified" for an edit).
function fileTags(call, existingLabel) {
  if (call.fileExisted === false) return [<Tag key="nf" testId="file-state" color="#86efac">{i18nOr('ui7Chat.new_file')}</Tag>]
  if (call.fileExisted === true) {
    return [<Tag key="ex" testId="file-state" color={existingLabel === 'overwrite' ? '#fbbf24' : '#94a3b8'}>{i18nOr(EXISTING_KEYS[existingLabel])}</Tag>]
  }
  return []
}

function statsTag(stats) {
  return <Tag key="st" color="#94a3b8">{`+${stats.added} −${stats.removed}`}</Tag>
}

function exitTag(call) {
  if (typeof call.exitCode !== 'number') return []
  return [<Tag key="exit" testId="exit-code" color={call.exitCode === 0 ? '#94a3b8' : '#ef4444'}>{i18nOr('ui7Chat.exit_code', { code: call.exitCode })}</Tag>]
}

const errorOnFail = (call) => call.ok === false && <ResultSection call={call} label={i18nOr('ui7Chat.error')} />

function shell(call, args, childCalls, renderChild, t) {
  const command = Array.isArray(args.command) ? args.command.join(' ') : (args.command ?? '')
  const cli = monoagentCommand(command)
  if (cli?.family === 'vault') return monoagentShell(call, args, command, cli, t)
  if (vaultRelated(command)) return vaultHidden(call, t)
  if (cli) return monoagentShell(call, args, command, cli, t)
  return {
    icon: Terminal, title: `$ ${command}`, titleMono: true, subtitle: args.description || (args.cwd ? i18nOr('ui7Chat.in_path', { path: args.cwd }) : undefined),
    tags: [...(args.run_in_background ? [<Tag key="bg" color="#fbbf24">{i18nOr('ui7Chat.background')}</Tag>] : []), ...exitTag(call)],
    body: <ResultSection call={call} />,
  }
}

// monoagentShell is a shell call that runs monoagentcli on one of the app's
// tabs (toolLinks.js): it reads as that tab with the command's subject, and
// links to it (link.page). A vault command, anywhere in the command line,
// shows the entry's name only: never the command line, which can carry a
// value, nor the output, except a lone list's (names and metadata).
function monoagentShell(call, args, command, cli, t) {
  const vault = cli.family === 'vault'
  return {
    icon: Terminal, title: cliTitle(cli, t), subtitle: vault ? undefined : args.description,
    tags: [...(args.run_in_background ? [<Tag key="bg" color="#fbbf24">{i18nOr('ui7Chat.background')}</Tag>] : []), ...exitTag(call)],
    link: { page: cli.page },
    body: vault ? (
      cli.sub === 'list' && isSingleCommand(command) ? <ResultSection call={call} /> : <VaultHidden t={t} />
    ) : (
      <>
        <Section label={t('toolLinks.command')} copyText={command}><pre style={preStyle}>{command}</pre></Section>
        <ResultSection call={call} />
      </>
    ),
  }
}

function VaultHidden({ t }) {
  return <span data-testid="vault-hidden" style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-muted)' }}>{t('toolLinks.vaultHidden')}</span>
}

// vaultHidden is a call that may run a vault command the card cannot read as
// one (behind bash -c, a variable or eval, or in arguments cut short): it
// says so, and shows nothing of its command line, description or output.
export function vaultHidden(call, t) {
  return { icon: Terminal, title: t('toolLinks.vaultRelated'), tags: exitTag(call), body: <VaultHidden t={t} /> }
}

function edit(call, args) {
  // aider's edits name only the file (its shim sees the result, not the
  // strings): no diff to show.
  const noStrings = !Array.isArray(args.edits) && args.old_string == null && args.new_string == null
  const pairs = Array.isArray(args.edits) ? args.edits : noStrings ? [] : [{ old_string: args.old_string, new_string: args.new_string }]
  const stats = pairs.reduce((acc, p) => {
    const s = diffStats(diffLines(p.old_string, p.new_string))
    return { added: acc.added + s.added, removed: acc.removed + s.removed }
  }, { added: 0, removed: 0 })
  return {
    icon: FilePen, title: args.file_path, titleMono: true, defaultOpen: true,
    tags: [...(pairs.length ? [statsTag(stats)] : []), ...fileTags(call, 'modified'), ...(args.replace_all ? [<Tag key="ra">{i18nOr('ui7Chat.replace_all')}</Tag>] : [])],
    body: <>{pairs.map((p, i) => <DiffView key={i} oldText={p.old_string} newText={p.new_string} />)}{errorOnFail(call)}</>,
  }
}

function write(call, args) {
  const content = args.content ?? ''
  // A trailing newline ends the last line; it doesn't start another.
  const lineCount = content ? content.replace(/\n$/, '').split('\n').length : 0
  return {
    icon: FilePlus, title: args.file_path, titleMono: true,
    tags: [<Tag key="lines" color="#94a3b8">{i18nOr('ui7Chat.lines', { count: lineCount })}</Tag>, ...fileTags(call, 'overwrite')],
    body: <><Section label={i18nOr('ui7Chat.content')} copyText={content}><pre style={preStyle}>{content}</pre></Section>{errorOnFail(call)}</>,
  }
}

function read(call, args) {
  const range = args.offset != null || args.limit != null
    ? ` ${i18nOr('ui7Chat.line_range', { range: `${args.offset ?? 1}${args.limit != null ? `–${(args.offset ?? 1) + args.limit - 1}` : '+'}` })}`
    : ''
  // cline reads several files in one call: file_path is the first, file_paths all.
  const paths = Array.isArray(args.file_paths) && args.file_paths.length > 1 ? args.file_paths : null
  if (paths) {
    return { icon: FileText, compact: true, title: i18nOr('ui7Chat.read_files', { n: paths.length }), subtitle: paths.join(', '), body: <ResultSection call={call} /> }
  }
  return { icon: FileText, compact: true, title: `${i18nOr('ui7Chat.read_path', { path: args.file_path ?? '' })}${range}`, body: <ResultSection call={call} /> }
}

function search(call, args) {
  const verb = CLAUDE_KINDS[call.name] === 'search' ? call.name : i18nOr('ui7Chat.search')
  return {
    icon: Search, compact: true,
    title: `${verb} ${args.pattern ?? args.query ?? ''}${args.path ? ` ${i18nOr('ui7Chat.in_path', { path: args.path })}` : ''}`,
    body: <ResultSection call={call} label={i18nOr('ui7Chat.matches')} />,
  }
}

function web(call, args) {
  // cline fetches several pages in one call: url is the first, urls all.
  const more = Array.isArray(args.urls) && args.urls.length > 1 ? args.urls.length - 1 : 0
  return {
    icon: Globe, title: args.url || args.query, titleMono: true,
    subtitle: more ? i18nOr('ui7Chat.and_more_urls', { n: more, urls: args.urls.slice(1).join(', ') }) : args.url ? args.prompt : undefined,
    tags: [<Tag key="ext" color="#fbbf24" testId="external-content">{i18nOr('ui7Chat.external_content')}</Tag>],
    body: <ResultSection call={call} label={i18nOr('ui7Chat.result')} />,
  }
}

const PATCH_ACTION_COLORS = { add: '#86efac', delete: '#fca5a5', update: '#94a3b8' }

function patch(call, args) {
  const files = Array.isArray(args.files) ? args.files : []
  const stats = files.reduce((acc, f) => {
    const s = diffStats(patchLines(f.diff))
    return { added: acc.added + s.added, removed: acc.removed + s.removed }
  }, { added: 0, removed: 0 })
  return {
    icon: FileDiff, titleMono: true, defaultOpen: true,
    title: files.length === 1 ? files[0].file_path : i18nOr('ui7Chat.n_files', { n: files.length }),
    tags: [statsTag(stats)],
    body: (
      <>
        {files.map((f, i) => (
          <div key={i} data-testid="patch-file" style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: mono, fontSize: 10, color: '#e2e8f0', wordBreak: 'break-all' }}>
              <Tag color={PATCH_ACTION_COLORS[f.action] || '#94a3b8'} testId="patch-action">{patchAction(f.action || 'update')}</Tag>
              {f.file_path}
            </div>
            {f.diff && <PatchDiffView diff={f.diff} />}
          </div>
        ))}
        {errorOnFail(call)}
      </>
    ),
  }
}

// mcp: canonical {server, tool, arguments}; a Claude call keeps its raw
// arguments and names the server and tool as mcp__server__tool.
function mcp(call, args) {
  const parts = (call.name || '').startsWith('mcp__') ? call.name.split('__') : null
  const canonical = typeof args.server === 'string' && typeof args.tool === 'string'
  const server = canonical ? args.server : parts?.[1] || ''
  const tool = canonical ? args.tool : parts ? parts.slice(2).join('__') : call.name
  const toolArgs = canonical ? (args.arguments ?? {}) : args
  const shown = typeof toolArgs === 'string' ? toolArgs : JSON.stringify(toolArgs, null, 2)
  return {
    icon: Plug, title: server ? `${server} · ${tool}` : tool, titleMono: true,
    body: (
      <>
        {shown && shown !== '{}' && <Section label={i18nOr('ui7Chat.args')} copyText={shown}><pre style={preStyle}>{shown}</pre></Section>}
        <ResultSection call={call} label={i18nOr('ui7Chat.result')} />
      </>
    ),
  }
}

function task(call, args, childCalls, renderChild) {
  return {
    icon: Bot, title: args.description || i18nOr('ui7Chat.subagent'), subtitle: args.subagent_type,
    defaultOpen: true,
    tags: [<Tag key="n" color="#94a3b8">{i18nOr('ui7Chat.calls', { count: childCalls.length })}</Tag>],
    body: (
      <>
        {args.prompt && (
          <details>
            <summary style={{ ...sectionLabel, cursor: 'pointer' }}>{i18nOr('ui7Chat.prompt')}</summary>
            <pre style={preStyle}>{args.prompt}</pre>
          </details>
        )}
        {childCalls.length > 0 && (
          <div data-testid="subagent-calls" style={{ borderLeft: '2px solid rgba(0,180,216,0.25)', paddingLeft: 8 }}>
            {childCalls.map(c => renderChild(c))}
          </div>
        )}
        <ResultSection call={call} label={i18nOr('ui7Chat.result')} />
      </>
    ),
  }
}

// todo: Claude's {todos: [{content, status}]}; other runtimes' items may
// carry {text, completed} instead.
function todo(call, args) {
  const items = (Array.isArray(args.todos) ? args.todos : Array.isArray(args.items) ? args.items : []).map(t => ({
    text: t.content ?? t.text ?? '',
    done: t.status === 'completed' || t.completed === true,
    active: t.status === 'in_progress',
  }))
  return {
    icon: ListChecks, compact: true, title: i18nOr('ui7Chat.todos', { done: items.filter(t => t.done).length, total: items.length }),
    body: (
      <ul style={{ margin: 0, paddingLeft: 16, fontFamily: mono, fontSize: 10, color: '#94a3b8' }}>
        {items.map((t, i) => (
          <li key={i} style={{ textDecoration: t.done ? 'line-through' : 'none', color: t.active ? CYAN : undefined }}>{t.text}</li>
        ))}
      </ul>
    ),
  }
}

function other(call, args) {
  return {
    icon: Wrench, title: call.name, titleMono: true,
    body: (
      <>
        {Object.keys(args).length > 0 && (
          <Section label={i18nOr('ui7Chat.args')} copyText={JSON.stringify(args, null, 2)}><pre style={preStyle}>{JSON.stringify(args, null, 2)}</pre></Section>
        )}
        <ResultSection call={call} label={i18nOr('ui7Chat.result')} />
      </>
    ),
  }
}

const BY_KIND = { shell, edit, write, read, search, web, patch, mcp, task, todo }

// describe returns how one call's card reads: header icon + title (+ an
// optional subtitle and tags), whether it starts open, its body, and the
// app tab it links to (link: {page}), if any. t is the card's translator.
export function describe(call, args, childCalls, renderChild, t = k => k) {
  return (BY_KIND[kindOf(call)] || other)(call, args, childCalls, renderChild, t)
}
