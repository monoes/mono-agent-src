// Step 10, You're set: what was done and what was left for later (with a way
// back to each), then the catalogue of what MonoAgent does. Continue is
// "Open MonoAgent": it completes the onboarding and the shell opens the app.
// A catalogue card completes it too, then opens the app at its page.
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, GitBranch, Building2, MessageSquare, FileText, Users, Share2, Clock, KanbanSquare, KeyRound, Plug, Library } from 'lucide-react'
import { STEPS, statusesOf, railLabel } from '../steps.js'
import { CATALOGUE } from '../catalogue.js'
import './Finish.css'

const ICONS = { workflow: GitBranch, org: Building2, chat: MessageSquare, doc: FileText, people: Users, share: Share2, clock: Clock, board: KanbanSquare, key: KeyRound, plug: Plug, library: Library }

export const SUMMARY_STEPS = STEPS.filter(s => s.id !== 'finish').map(s => s.id)

export default function Finish({ state, complete, onFinish, goTo, setCanContinue, setOnContinue, setContinueLabel }) {
  const { t } = useTranslation()
  const statuses = statusesOf(state)
  const left = SUMMARY_STEPS.filter(id => statuses[id] !== 'done').length
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // a sign-in refusal says so in the shell's words
  const signInFirst = (r) => (r && r.ok === false && r.loginRequired ? { ...r, message: t('onboarding.shell.signIn') } : r)

  useEffect(() => { setContinueLabel(t('onboarding.finish.open')) }, [setContinueLabel, t])
  useEffect(() => { setCanContinue(!busy) }, [busy, setCanContinue])
  // The shell shows a failed callStep result and stays; otherwise it finishes.
  useEffect(() => { setOnContinue(async () => signInFirst(await complete())) })

  const openCard = async (card) => {
    if (busy) return
    setBusy(true)
    setError('')
    const r = signInFirst(await complete())
    if (r && r.ok === false) {
      setError(r.message)
      setBusy(false)
      return
    }
    onFinish({ page: card.page, opts: card.opts })
  }

  return (
    <>
      <p className="onb-lead">{left ? t('onboarding.finish.left', { count: left }) : t('onboarding.finish.allDone')}</p>
      <ul className="onb-fin-sum" aria-label={t('onboarding.finish.summary')}>
        {SUMMARY_STEPS.map(id => {
          const st = statuses[id]
          return (
            <li key={id} className={st === 'done' ? 'done' : 'skipped'}>
              <span className="onb-fin-s" aria-hidden="true">{st === 'done' ? <Check /> : null}</span>
              <span className="onb-fin-n">{railLabel(t, id)}</span>
              {st === 'done'
                ? <span className="onb-sr">{t('onboarding.finish.done')}</span>
                : (
                  <>
                    <em>{st === 'skipped' ? t('onboarding.finish.skipped') : t('onboarding.finish.notDone')}</em>
                    <button type="button" className="onb-fin-link" disabled={busy} onClick={() => goTo(id)}
                      aria-label={t('onboarding.finish.finishThisStep', { name: railLabel(t, id) })}>
                      {t('onboarding.finish.finishThis')}
                    </button>
                  </>
                )}
            </li>
          )
        })}
      </ul>
      <p className="onb-h3">{t('onboarding.finish.catalogue')}</p>
      <div className="onb-fin-cat">
        {CATALOGUE.map(card => {
          const Icon = ICONS[card.icon] || GitBranch
          return (
            <button key={card.id} type="button" disabled={busy} onClick={() => openCard(card)}>
              <Icon aria-hidden="true" />
              <span className="onb-fin-txt">
                <b>{t(card.titleKey)}</b>
                <span>{t(card.lineKey)}</span>
                <small>{t(card.whereKey)}</small>
              </span>
            </button>
          )
        })}
      </div>
      {error && <p className="onb-error" role="alert">{error}</p>}
    </>
  )
}
