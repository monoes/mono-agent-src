import { Children, isValidElement } from 'react'
import { useTranslation } from 'react-i18next'
import ErrorBoundary from '../../ErrorBoundary.jsx'
import { BLOCKS } from './registry.js'
import { parseBlock, infoFromClassName } from './parse.js'
import { useBlockEnv } from './context.js'
import { blockKey } from './answers.js'

function Skeleton({ info }) {
  const { t } = useTranslation()
  return (
    <div data-testid="chat-block-skeleton" role="status" aria-busy="true" aria-label={t('chatBlocks.loading', { type: info })}
      className="cb-skeleton" style={{ height: 64, margin: '0.35em 0', borderRadius: 6, border: '1px solid #1e3a4f', background: '#0d1a28' }} />
  )
}

// ChatBlock: the safe boundary. Renders the validated block, a skeleton while
// the JSON is still streaming in, or `fallback` (the ordinary code block) for
// anything unknown, invalid or that throws while rendering.
export function ChatBlock({ info, body, offset, fallback, blocks = BLOCKS }) {
  const env = useBlockEnv()
  const validators = {}
  for (const k of Object.keys(blocks)) validators[k] = blocks[k].validate
  const r = parseBlock(info, body, validators)
  if (r.status === 'none' || r.status === 'invalid') return fallback
  if (r.status === 'incomplete') return env.live ? <Skeleton info={info} /> : fallback
  const { Component } = blocks[r.type]
  return (
    <ErrorBoundary key={body.length} fallback={() => fallback}>
      <div data-testid={`chat-block-${r.type}`} style={{ margin: '0.35em 0', maxWidth: '100%' }}>
        <Component data={r.data} blockKey={blockKey(env.conversationId, env.scope, offset)} />
      </div>
    </ErrorBoundary>
  )
}

// chatBlockFor: the hook ChatMarkdown's `pre` override calls. The element
// that stands in for the fenced block, or null when `children` is not a
// fenced code element at all.
export function chatBlockFor(children, node, fallback) {
  const code = Children.toArray(children).find(isValidElement)
  if (!code) return null
  const info = infoFromClassName(code.props.className)
  if (!info || typeof code.props.children !== 'string') return null
  // Not a block type: leave the fence to the next hook (html-view, code block).
  if (!Object.prototype.hasOwnProperty.call(BLOCKS, info)) return null
  const offset = node?.position?.start?.offset ?? 0
  return <ChatBlock info={info} body={code.props.children} offset={offset} fallback={fallback} />
}
