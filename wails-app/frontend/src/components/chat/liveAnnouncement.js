import { computeStatusLabel } from './TurnStatus.jsx'
import i18nOr from '../../lib/i18nOr.js'

// composeLiveAnnouncement builds the one text fired into the panel's
// persistent aria-live region when a turn finalizes. TurnStatus's own
// per-turn "role=status" region only exists while {streaming && ...} is
// mounted — at the exact moment a turn finishes, that block unmounts and a
// *new* TurnStatus instance mounts inside the finalized message with the
// terminal label already baked in. Most screen readers only announce a
// mutation inside an already-present live region, not a freshly-inserted
// node whose content is already set, so "Completed"/"Failed" would
// otherwise go unannounced despite mid-stream status changes working fine.
// Folds in any notices and tool failures already present at finalize time
// (e.g. historySaved:false) — those otherwise have no live-region coverage
// at all (NoticeBanner is plain, non-live DOM). Exported and pure/
// deterministic like TurnStatus's own computeStatusLabel, for the same
// direct-unit-testability reason.
//
// alreadyAnnouncedCount (default 0, so every existing caller/test is
// unaffected) excludes the first N of turnState.notices from this summary —
// the live-turn effect below announces mid-turn notices individually the
// moment they arrive rather than waiting for finalize, and passes its own
// running count here so a notice already spoken once is never repeated in
// this finalize-time summary too.
export function composeLiveAnnouncement(turnState, alreadyAnnouncedCount = 0) {
  const { label } = computeStatusLabel(turnState, Date.now())
  const parts = [i18nOr('ui7Chat.announce_response', { status: label.toLowerCase() })]
  const failedCount = Object.values(turnState.calls || {}).filter(c => c.status === 'completed' && c.ok === false).length
  if (failedCount > 0) parts.push(i18nOr('ui7Chat.announce_tools_failed', { count: failedCount }))
  for (const notice of (turnState.notices || []).slice(alreadyAnnouncedCount)) parts.push(notice.message)
  return parts.join(' ')
}
