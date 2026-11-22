import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ArrowRight, MessageSquare, AlertTriangle } from 'lucide-react'

export default function HelpTopicCard({ topic, open, onToggle, onAction }) {
  const { t } = useTranslation()
  const bodyId = useId()
  const level = topic.danger?.level || 0
  return (
    <article className={`card help-topic${open ? ' open' : ''}${level ? ` danger-${level}` : ''}`} data-testid={`help-topic-${topic.id}`}>
      <button type="button" className="help-topic-head" aria-expanded={open} aria-controls={bodyId} onClick={() => onToggle(topic.id)}>
        <span className="help-topic-emoji" aria-hidden="true">{topic.emoji}</span>
        <span className="help-topic-text">
          <span className="help-topic-title">{topic.title}</span>
          <span className="help-topic-blurb">{topic.blurb}</span>
        </span>
        {level > 0 && (
          <span className="help-danger" title={topic.danger.quip}>
            <AlertTriangle size={12} aria-hidden="true" />
            <span className="help-danger-pips" aria-hidden="true">
              {[1, 2, 3, 4, 5].map(i => <i key={i} className={i <= level ? 'on' : ''} />)}
            </span>
            <span className="sr-only">{t('help.topic.dangerLevel', { n: level })}</span>
          </span>
        )}
        <ChevronDown size={16} className="help-chevron" aria-hidden="true" />
      </button>
      {open && (
        <div id={bodyId} className="help-topic-body">
          {topic.quip && <p className="help-quip">{topic.quip}</p>}
          {level > 0 && topic.danger.quip && <p className="help-danger-quip">{topic.danger.quip}</p>}
          <ol className="help-steps">
            {topic.steps.map((s, i) => (
              <li key={i} className="help-step" style={{ '--i': i }}>
                <span className="help-step-n" aria-hidden="true">{i + 1}</span>
                <span className="help-step-text">{s.text}</span>
                {s.action && (
                  <button type="button" className="btn btn-sm btn-secondary" onClick={() => onAction(s.action)}>
                    {s.action.kind === 'coder' ? <MessageSquare size={13} aria-hidden="true" /> : <ArrowRight size={13} aria-hidden="true" />}
                    {s.action.kind === 'coder' ? t('help.topic.askCoder') : t('help.topic.takeMe')}
                  </button>
                )}
              </li>
            ))}
          </ol>
        </div>
      )}
    </article>
  )
}
