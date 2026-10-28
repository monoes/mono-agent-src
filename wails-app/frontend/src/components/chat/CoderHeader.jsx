import { useState } from 'react'
import { FolderOpen, Copy, Check, FolderRoot } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { copyToClipboard } from './toolCardUtils.js'
import { MissionToggle } from './MissionToggle.jsx'

// Coder conversation header (#203): the CODER badge, the folder the agent
// works in (full path), and Open folder / Copy path. With a `mission` (useMissionToggle)
// it also carries the Mission switch; before the chat has a folder that is all it shows.

const mono = 'var(--font-mono)'

export function CoderBadge({ small = false }) {
  return (
    <span data-testid="coder-badge" style={{
      fontFamily: mono, fontSize: small ? 7.5 : 8.5, fontWeight: 700, letterSpacing: 1,
      color: '#f59e0b', background: 'rgba(245,158,11,0.08)', border: '1px solid rgba(245,158,11,0.4)',
      borderRadius: 4, padding: small ? '0 4px' : '2px 5px', flexShrink: 0,
    }}>
      CODER
    </span>
  )
}

const iconButton = {
  display: 'flex', alignItems: 'center', gap: 4, flexShrink: 0,
  background: 'transparent', border: '1px solid rgba(0,180,216,0.2)', borderRadius: 5,
  padding: '2px 6px', cursor: 'pointer', color: '#00b4d8', fontFamily: mono, fontSize: 9,
}

export function CoderHeader({ cwd, mission }) {
  const [copied, setCopied] = useState(false)
  if (!cwd && !(mission && (mission.state || mission.error))) return null
  const openFolder = () => {
    Promise.resolve(api.openPathWithOS(cwd)).catch(e => notify('coder', `Could not open ${cwd}: ${e}`))
  }
  const copy = () => {
    copyToClipboard(cwd)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <div data-testid="coder-header" style={{
      padding: '6px 12px', borderBottom: '1px solid rgba(245,158,11,0.15)', background: 'rgba(245,158,11,0.03)',
      display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0, flexWrap: 'wrap',
    }}>
      {cwd && <CoderBadge />}
      {mission && <MissionToggle toggle={mission} />}
      {cwd && <><span title={cwd} data-testid="coder-cwd" style={{
        flex: 1, minWidth: 0, fontFamily: mono, fontSize: 10, color: '#e2e8f0',
        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', direction: 'rtl', textAlign: 'left',
      }}>
        {/* rtl keeps the folder name visible when the path is cut; the
            bdi stops it from reordering the path's own characters. */}
        <bdi>{cwd}</bdi>
      </span>
      <button type="button" onClick={openFolder} title="Open folder in the file manager" style={iconButton}>
        <FolderOpen size={10} /> Open folder
      </button>
      <button type="button" onClick={copy} title="Copy path" aria-label="Copy path" style={iconButton}>
        {copied ? <Check size={10} /> : <Copy size={10} />}
      </button></>}
    </div>
  )
}

// The init files worth naming per runtime; `monomind init` creates a few
// hundred more (skills, helpers, …), which stay behind a "show all".
const KEY_INIT_FILES = {
  claude: ['CLAUDE.md', '.claude/settings.json', '.mcp.json'],
  codex: ['AGENTS.md', '.codex/config.toml'],
  opencode: ['AGENTS.md', 'opencode.json'],
  kimicode: ['AGENTS.md', '.kimi-code/mcp.json'],
  antigravity: ['GEMINI.md', '.gemini/settings.json'],
  cline: ['.clinerules/monomind.md', 'AGENTS.md'],
  aider: ['CONVENTIONS.md', '.aider.conf.yml'],
}

// keyInitFiles is a runtime's key init files. Every runtime without its own
// list (pi, dsh, grok, copilot, qwen, crush, …) gets AGENTS.md alone —
// `monomind init --target agents`, or mono-agent's own fallback — never
// Claude's setup.
export function keyInitFiles(runtime) {
  return KEY_INIT_FILES[runtime] || ['AGENTS.md']
}

// initSummary names the key files created and counts the rest.
export function initSummary(created, runtime = 'claude') {
  const key = keyInitFiles(runtime).filter(f => created.includes(f))
  const shown = key.length ? key : created.slice(0, 3)
  const more = created.length - shown.length
  return shown.join(', ') + (more > 0 ? ` and ${more} more` : '')
}

// CoderInitNote reports the coder root a coder chat was started in,
// from `coder workspace root`'s JSON; runtime picks the files to name.
export function CoderInitNote({ workspace, runtime = 'claude' }) {
  const created = workspace?.init?.created || []
  return (
    <div data-testid="coder-init-note" style={{
      display: 'flex', alignItems: 'flex-start', gap: 6, margin: '0 0 8px', padding: '6px 8px', borderRadius: 6,
      background: 'rgba(0,180,216,0.05)', border: '1px solid rgba(0,180,216,0.15)',
      fontFamily: mono, fontSize: 9.5, color: 'var(--text-muted)', lineHeight: 1.5, wordBreak: 'break-all',
    }}>
      <FolderRoot size={11} color="#00b4d8" style={{ flexShrink: 0, marginTop: 1 }} />
      <span>
        {workspace?.created ? (workspace?.profile ? 'Created profile folder ' : 'Created coder root ') : 'Working in '}
        <span style={{ color: '#e2e8f0' }}>{workspace?.path}</span>
        {created.length > 0 && <> · created {initSummary(created, runtime)}</>}
        {workspace?.git && <> · git repository</>}
        {created.length > keyInitFiles(runtime).length && (
          <details style={{ marginTop: 3 }}>
            <summary style={{ cursor: 'pointer' }}>show all {created.length}</summary>
            <div data-testid="coder-init-all" style={{ maxHeight: 160, overflow: 'auto', whiteSpace: 'pre-wrap' }}>{created.join('\n')}</div>
          </details>
        )}
      </span>
    </div>
  )
}
