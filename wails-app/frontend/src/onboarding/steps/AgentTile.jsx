// One runtime's tile in the Your agents step: its state from the scan and the
// roster, the install (npm recipes only), the model picker and the one-message
// test. Every value shown comes from a binding answer or event; Agents.jsx
// holds the state.
import { useTranslation } from 'react-i18next'
import { installRecipe, recipeCommand } from '../../lib/agentRuntimes.js'
import { loginHintFor } from '../../lib/agentRoster.js'

// Product names; a runtime not listed here shows its scan id.
const NAMES = {
  claude: 'Claude Code',
  codex: 'Codex',
  opencode: 'OpenCode',
  copilot: 'GitHub Copilot CLI',
  gemini: 'Gemini CLI',
  antigravity: 'Antigravity',
  cursor: 'Cursor CLI',
}
export const runtimeName = (id) => NAMES[id] || id

const initials = (name) => {
  const w = String(name).split(/[\s-]+/).filter(Boolean)
  return (w.length > 1 ? w[0][0] + w[1][0] : String(name).slice(0, 2)).toUpperCase()
}

export const rosterReady = (rr) => (rr?.models || []).some(m => m.state === 'ready')
const signInFailed = (rr) => (rr?.models || []).some(m => m.status === 'auth')

/** What the tile's tag says: { key, tone } (tone: ok | warn | off). */
function tagFor(agent, rr, install) {
  if (install?.running) return { key: 'installing', tone: 'warn' }
  if (!agent.installed) return { key: 'notInstalled', tone: 'off' }
  if (agent.execution_supported === false) return { key: 'unsupported', tone: 'off' }
  if (rosterReady(rr)) return { key: 'ready', tone: 'ok' }
  if (signInFailed(rr)) return { key: 'signIn', tone: 'warn' }
  return { key: 'installed', tone: 'neutral' }
}

function Tag({ tag }) {
  const { t } = useTranslation()
  const text = {
    installing: t('onboarding.agents.tag.installing'),
    notInstalled: t('onboarding.agents.tag.notInstalled'),
    unsupported: t('onboarding.agents.tag.unsupported'),
    ready: t('onboarding.agents.tag.ready'),
    signIn: t('onboarding.agents.tag.signIn'),
    installed: t('onboarding.agents.tag.installed'),
  }[tag.key]
  return <span className={`onb-tag ${tag.tone}`}>{text}</span>
}

function NotInstalled({ agent, name, install, onInstall }) {
  const { t } = useTranslation()
  const recipe = installRecipe(agent)
  if (install?.running) {
    return (
      <>
        <span className="onb-ag-busy" role="status"><span className="onb-spinner" />{t('onboarding.agents.installingName', { name })}</span>
        {install.lines.length > 0 && <ol className="onb-log" aria-label={t('onboarding.agents.installProgress')}>{install.lines.map((l, i) => <li key={i}>{l}</li>)}</ol>}
      </>
    )
  }
  if (recipe.kind !== 'npm') {
    return agent.install_hint
      ? <p className="onb-hint">{t('onboarding.agents.manualInstall')} <code>{agent.install_hint}</code></p>
      : null
  }
  return (
    <>
      <p className="onb-hint">{t('onboarding.agents.runs')} <code>{recipeCommand(recipe, agent)}</code></p>
      <div>
        <button type="button" className="onb-btn sm" onClick={() => onInstall(agent)} aria-label={t('onboarding.agents.installName', { name })}>
          {t('onboarding.agents.install')}
        </button>
      </div>
      {install?.error && (
        <p className="onb-error" role="alert">
          {install.loginRequired ? t('onboarding.shell.signIn') : install.busy ? t('onboarding.agents.installBusy', { name }) : t('onboarding.agents.installFailed', { message: install.error })}
        </p>
      )}
    </>
  )
}

function TestArea({ agent, name, rr, models, picked, onPick, test, testBusy, onTest }) {
  const { t } = useTranslation()
  const mine = test && test.runtime === agent.id ? test : null
  const hint = loginHintFor(rr) || agent.login_hint || ''
  let picker
  if (!models || models.loading) {
    picker = <span className="onb-ag-busy"><span className="onb-spinner" />{t('onboarding.agents.readingModels')}</span>
  } else if (models.error) {
    picker = <p className="onb-error" role="alert">{models.loginRequired ? t('onboarding.shell.signIn') : t('onboarding.agents.modelsFailed', { message: models.error })}</p>
  } else if (models.list.length === 0) {
    picker = <p className="onb-hint">{t('onboarding.agents.noModels', { name })}</p>
  } else {
    picker = (
      <>
        <select className="onb-input" value={picked || ''} onChange={e => onPick(agent.id, e.target.value)}
          aria-label={t('onboarding.agents.modelFor', { name })} disabled={testBusy}>
          {models.list.map(m => <option key={m.id} value={m.id}>{m.label || m.id}</option>)}
        </select>
        <p className="onb-hint">{t('onboarding.agents.testNote', { name })}</p>
        {mine?.phase === 'running' ? (
          <span className="onb-ag-busy" role="status"><span className="onb-spinner" />{t('onboarding.agents.testing')}</span>
        ) : (
          <div>
            <button type="button" className="onb-btn sm primary" disabled={testBusy || !picked} onClick={() => onTest(agent)}
              aria-label={t('onboarding.agents.testName', { name })}>
              {t('onboarding.agents.test')}
            </button>
          </div>
        )}
      </>
    )
  }
  return (
    <>
      {signInFailed(rr) && hint && (
        <p className="onb-hint">{t('onboarding.agents.signInRun')} <code>{hint}</code> {t('onboarding.agents.signInThen')}</p>
      )}
      {picker}
      {mine?.phase === 'passed' && <p className="onb-ag-ok" role="status">{t('onboarding.agents.readyNote', { name })}</p>}
      {mine?.phase === 'failed' && (
        <p className="onb-error" role="alert">
          {mine.loginRequired ? t('onboarding.shell.signIn') : t('onboarding.agents.testFailed', { message: mine.message || t('onboarding.agents.noResult') })}
        </p>
      )}
    </>
  )
}

export default function AgentTile(props) {
  const { t } = useTranslation()
  const { agent, rr, install, onInstall } = props
  const name = runtimeName(agent.id)
  const tag = tagFor(agent, rr, install)
  let body = null
  if (!agent.installed || install?.running) {
    body = <NotInstalled agent={agent} name={name} install={install} onInstall={onInstall} />
  } else if (agent.execution_supported === false) {
    body = agent.execution_unsupported_reason ? <p className="onb-hint">{agent.execution_unsupported_reason}</p> : null
  } else if (tag.key === 'ready') {
    body = <p className="onb-ag-ok">{t('onboarding.agents.readyNote', { name })}</p>
  } else {
    body = <TestArea {...props} name={name} />
  }
  return (
    <div className={`onb-card onb-ag-tile${tag.key === 'ready' ? ' ready' : ''}`} data-runtime={agent.id}>
      <header>
        <span className="onb-ag-lg" aria-hidden="true">{initials(name)}</span>
        <b>{name}</b>
        {agent.installed && agent.version && <span className="onb-ag-ver">{agent.version}</span>}
      </header>
      <div><Tag tag={tag} /></div>
      {body}
    </div>
  )
}
