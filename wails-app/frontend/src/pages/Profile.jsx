import { useState, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ArrowLeft, ExternalLink, CheckCircle, Globe, Mail, MapPin,
  Users, FileText, Heart, MessageSquare, Send, Eye,
  UserPlus, UserMinus, Search, RefreshCw, Zap,
  MessageCircle, ChevronDown, ChevronRight,
  ArrowDownLeft, ArrowUpRight
} from 'lucide-react'
import { isUnread, UnreadDot } from '../lib/unread.jsx'
import { api, PLATFORM_COLORS, STATE_COLORS } from '../services/api.js'
import { confirm } from '../components/ConfirmDialog.jsx'
import MessageDetailModal from '../components/MessageDetailModal.jsx'
import StatusHistoryModal from '../components/StatusHistoryModal.jsx'
import { ExperienceSection, EducationSection, ProfileDetailsSection } from '../components/ProfileDetails.jsx'
import { execStatusWord } from './dashboard/format.js'

// Map action types to icons + labels
const ACTION_META = {
  like_posts:            { icon: Heart },
  comment_on_posts:      { icon: MessageSquare },
  like_comments_on_posts:{ icon: Heart },
  send_dms:              { icon: Send },
  auto_reply_dms:        { icon: Send },
  follow_users:          { icon: UserPlus },
  unfollow_users:        { icon: UserMinus },
  watch_stories:         { icon: Eye },
  scrape_profile_info:   { icon: Search },
  export_followers:      { icon: Users },
  engage_with_posts:     { icon: Zap },
  engage_user_posts:     { icon: Zap },
  find_by_keyword:       { icon: Search },
  extract_post_data:     { icon: FileText },
  publish_post:          { icon: Send },
}

// Label of an action type; types without a translation show the raw name.
const actionLabel = (t, type) => (ACTION_META[type] ? t(`profileBody.action_${type}`) : type)

const PLATFORM_PROFILE_URL = {
  INSTAGRAM: (u) => `https://www.instagram.com/${u}/`,
  LINKEDIN:  (u) => `https://www.linkedin.com/in/${u}/`,
  X:         (u) => `https://x.com/${u}`,
  TIKTOK:    (u) => `https://www.tiktok.com/@${u}`,
}

function StatPill({ label, value, icon: Icon }) {
  if (!value && value !== 0) return null
  return (
    <div className="profile-stat">
      {Icon && <Icon size={14} style={{ color: 'var(--cyan-dim)', flexShrink: 0 }} />}
      <span className="profile-stat-value">{value || '—'}</span>
      <span className="profile-stat-label">{label}</span>
    </div>
  )
}

// node_type is "<platform>.<actionType>" (e.g. "instagram.like_posts") —
// ACTION_META is keyed by the bare actionType suffix, so strip the prefix.
function actionTypeSuffix(nodeType) {
  if (!nodeType) return nodeType
  const i = nodeType.lastIndexOf('.')
  return i === -1 ? nodeType : nodeType.slice(i + 1)
}

function InteractionRow({ item }) {
  const { t } = useTranslation()
  const suffix = actionTypeSuffix(item.node_type)
  const Icon = (ACTION_META[suffix] || { icon: Zap }).icon
  const stateColor = STATE_COLORS[item.status] || '#94a3b8'
  const ts = item.last_interacted_at || item.created_at
  const date = ts ? ts.slice(0, 10) : '—'
  const time = ts && ts.length > 10 ? ts.slice(11, 16) : ''

  return (
    <div className="interaction-row">
      <div className="interaction-icon" style={{ color: 'var(--cyan-dim)' }}>
        <Icon size={13} />
      </div>
      <div className="interaction-body">
        <div className="interaction-top">
          <span className="interaction-type">{actionLabel(t, suffix)}</span>
          {item.node_name && (
            <span className="interaction-action-title">{t('profileBody.via', { name: item.node_name })}</span>
          )}
          <span
            className="interaction-status"
            style={{ color: stateColor, borderColor: stateColor + '40', background: stateColor + '12' }}
          >
            {execStatusWord(item.status)}
          </span>
        </div>
        {item.comment_text && (
          <div className="interaction-comment">"{item.comment_text}"</div>
        )}
        {item.link && (
          <div className="interaction-link">{item.link}</div>
        )}
      </div>
      <div className="interaction-time">
        <span>{date}</span>
        {time && <span style={{ color: 'var(--text-dim)', fontSize: 10 }}>{time}</span>}
      </div>
    </div>
  )
}

function Avatar({ username, imageUrl, size = 72 }) {
  const [failed, setFailed] = useState(false)
  const initials = (username || '?').slice(0, 2).toUpperCase()
  const colors = [
    ['#7c3aed','#00b4d8'], ['#00b4d8','#00f5d4'], ['#e1306c','#7c3aed'],
    ['#f97316','#eab308'], ['#10b981','#00b4d8'],
  ]
  const pair = colors[(username?.charCodeAt(0) || 0) % colors.length]
  const style = {
    width: size, height: size, borderRadius: '50%', flexShrink: 0,
    display: 'flex', alignItems: 'center', justifyContent: 'center',
    fontFamily: 'var(--font-mono)', fontWeight: 700,
    fontSize: size * 0.26, color: 'white', overflow: 'hidden',
    border: '2px solid var(--border-bright)',
  }

  if (imageUrl && !failed) {
    return (
      <div style={{ ...style, background: 'var(--elevated)', padding: 0 }}>
        <img src={imageUrl} alt={username} onError={() => setFailed(true)}
          style={{ width: '100%', height: '100%', objectFit: 'cover' }} />
      </div>
    )
  }
  return (
    <div style={{ ...style, background: `linear-gradient(135deg, ${pair[0]}, ${pair[1]})` }}>
      {initials}
    </div>
  )
}

function PostsSection({ personId, onOpenPost, onOpenURL }) {
  const [posts, setPosts]       = useState([])
  const [open, setOpen]         = useState(false)
  const [loading, setLoading]   = useState(true)
  const [loadError, setLoadError] = useState('')
  const { t } = useTranslation()

  useEffect(() => {
    api.getPersonPosts(personId).then(data => {
      const rows = data || []
      setPosts(rows)
      if (rows.length > 0) setOpen(true)
    }).catch(e => setLoadError(e?.message || String(e))).finally(() => setLoading(false))
  }, [personId])

  return (
    <div className="profile-section">
      {/* Header row — always visible */}
      <button
        onClick={() => setOpen(o => !o)}
        style={{
          display: 'flex', alignItems: 'center', gap: 6,
          background: 'none', border: 'none', cursor: 'pointer',
          width: '100%', padding: 0, textAlign: 'left',
        }}
      >
        {open
          ? <ChevronDown size={13} style={{ color: 'var(--text-muted)' }} />
          : <ChevronRight size={13} style={{ color: 'var(--text-muted)' }} />
        }
        <span className="profile-section-title" style={{ margin: 0 }}>
          {t('profileBody.posts')}
          <span style={{ color: 'var(--text-muted)', fontWeight: 400, marginLeft: 8, fontSize: 11 }}>
            {loading ? '…' : posts.length}
          </span>
        </span>
      </button>

      {/* Collapsible body */}
      {open && (
        <div style={{ marginTop: 10 }}>
          {loading ? (
            <div style={{ padding: '12px 0', textAlign: 'center' }}>
              <div className="spinner" style={{ width: 14, height: 14, margin: '0 auto' }} />
            </div>
          ) : loadError ? (
            <div role="alert" style={{ padding: '12px 0', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--red)' }}>
              {t('profilePage.loadFailed', { error: loadError })}
            </div>
          ) : posts.length === 0 ? (
            <div style={{
              padding: '16px 0', textAlign: 'center',
              color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', fontSize: 11,
            }}>
              {t('profileBody.noPosts')}
            </div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {posts.map(post => (
                <div key={post.id} style={{
                  display: 'flex', alignItems: 'center', gap: 10,
                  padding: '6px 8px', borderRadius: 6,
                  background: 'var(--elevated)',
                  border: '1px solid var(--border)',
                }}>
                  {/* Shortcode link → PostDetail */}
                  <button
                    onClick={() => onOpenPost(post.id)}
                    style={{
                      fontFamily: 'var(--font-mono)', fontSize: 11,
                      color: 'var(--cyan)', background: 'none', border: 'none',
                      cursor: 'pointer', padding: 0, flexShrink: 0,
                    }}
                  >
                    {post.shortcode}
                  </button>

                  {/* External link */}
                  <button
                    onClick={() => onOpenURL(post.url)}
                    style={{
                      background: 'none', border: 'none', cursor: 'pointer',
                      color: 'var(--text-muted)', padding: 0, display: 'flex',
                      opacity: 0.5, flexShrink: 0,
                    }}
                  >
                    <ExternalLink size={10} />
                  </button>

                  {/* Stats */}
                  <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginLeft: 4 }}>
                    <span style={{
                      display: 'flex', alignItems: 'center', gap: 3,
                      fontFamily: 'var(--font-mono)', fontSize: 10,
                      color: 'var(--text-muted)',
                    }}>
                      <Heart size={10} /> {post.like_count ?? '—'}
                    </span>
                    <span style={{
                      display: 'flex', alignItems: 'center', gap: 3,
                      fontFamily: 'var(--font-mono)', fontSize: 10,
                      color: 'var(--text-muted)',
                    }}>
                      <MessageCircle size={10} /> {post.comment_count ?? '—'}
                    </span>
                  </div>

                  {/* We interacted badges */}
                  {post.we_liked && (
                    <span style={{
                      padding: '1px 6px', borderRadius: 4,
                      background: 'rgba(0,180,216,0.12)',
                      border: '1px solid rgba(0,180,216,0.3)',
                      color: '#00b4d8', fontSize: 9,
                      fontFamily: 'var(--font-mono)',
                    }}>
                      {t('profileBody.liked')}
                    </span>
                  )}
                  {post.we_commented && (
                    <span style={{
                      padding: '1px 6px', borderRadius: 4,
                      background: 'rgba(124,58,237,0.12)',
                      border: '1px solid rgba(124,58,237,0.3)',
                      color: '#a855f7', fontSize: 9,
                      fontFamily: 'var(--font-mono)',
                    }}>
                      {t('profileBody.commented')}
                    </span>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function MessagesSection({ personId, personLabel, personPlatform }) {
  const [messages, setMessages] = useState([])
  const [open, setOpen]         = useState(false)
  const [loading, setLoading]   = useState(true)
  const [openMessage, setOpenMessage] = useState(null)

  const [composeOpen, setComposeOpen] = useState(false)
  const [connections, setConnections] = useState([])
  const [connectionId, setConnectionId] = useState('')
  const [subject, setSubject] = useState('')
  const [body, setBody] = useState('')
  const [sending, setSending] = useState(null) // 'send' | 'draft'
  const [composeError, setComposeError] = useState(null)
  const [draftError, setDraftError] = useState(null) // Send/Discard fail with the compose panel closed
  const [loadError, setLoadError] = useState('')
  const { t } = useTranslation()
  const [draftActionId, setDraftActionId] = useState(null) // message id being sent/rejected
  const [directionFilter, setDirectionFilter] = useState('all') // 'all' | 'inbound' | 'outbound'

  const canCompose = personPlatform === 'email'
  const visibleMessages = directionFilter === 'all' ? messages : messages.filter(m => m.direction === directionFilter)
  const sentCount = messages.filter(m => m.direction === 'outbound').length

  const reload = () => api.getPersonMessages(personId).then(data => { setMessages(data || []); return data || [] })

  // Opening a person's conversation marks it read. The list keeps the dots
  // it loaded with, so what was new stays visible for this visit.
  useEffect(() => {
    reload()
      .then(list => { if (list.some(isUnread)) api.markPersonMessagesRead(personId) })
      .catch(e => setLoadError(e?.message || String(e)))
      .finally(() => setLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [personId])

  useEffect(() => {
    if (!canCompose) return
    api.listConnections('outlook').then(rows => {
      const list = rows || []
      setConnections(list)
      if (list.length === 1) setConnectionId(list[0].id)
    }).catch(() => {})
  }, [canCompose])

  const handleCompose = async (asDraft) => {
    if (!connectionId || !body.trim()) return
    if (!asDraft && !(await confirm(t('profilePage.confirmSend'), { confirmLabel: t('profilePage.send') }))) return
    setSending(asDraft ? 'draft' : 'send')
    setComposeError(null)
    try {
      await api.composePersonMessage(personId, connectionId, subject, body, asDraft)
      setSubject('')
      setBody('')
      setComposeOpen(false)
      await reload()
      setOpen(true)
    } catch (e) {
      setComposeError(e?.message || String(e))
    } finally {
      setSending(null)
    }
  }

  const handleSendDraft = async (id) => {
    // Sending is a real email: the drafted text goes out as it stands.
    if (!(await confirm(t('profilePage.confirmSend'), { confirmLabel: t('profilePage.send') }))) return
    setDraftActionId(id)
    setDraftError(null)
    try {
      await api.sendDraftPersonMessage(id)
      await reload()
    } catch (e) {
      setDraftError(e?.message || String(e))
    } finally {
      setDraftActionId(null)
    }
  }

  const handleRejectDraft = async (id) => {
    setDraftActionId(id)
    setDraftError(null)
    try {
      await api.rejectDraftPersonMessage(id)
      await reload()
    } catch (e) {
      setDraftError(e?.message || String(e))
    } finally {
      setDraftActionId(null)
    }
  }

  return (
    <div className="profile-section">
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <button
          onClick={() => setOpen(o => !o)}
          style={{
            display: 'flex', alignItems: 'center', gap: 6,
            background: 'none', border: 'none', cursor: 'pointer',
            flex: 1, padding: 0, textAlign: 'left',
          }}
        >
          {open
            ? <ChevronDown size={13} style={{ color: 'var(--text-muted)' }} />
            : <ChevronRight size={13} style={{ color: 'var(--text-muted)' }} />
          }
          <span className="profile-section-title" style={{ margin: 0 }}>
            {t('profileBody.messages')}
            <span style={{ color: 'var(--text-muted)', fontWeight: 400, marginLeft: 8, fontSize: 11 }}>
              {loading ? '…' : messages.length}
            </span>
          </span>
        </button>
        {canCompose && (
          <button
            onClick={() => { setOpen(true); setComposeOpen(o => !o) }}
            style={{
              fontFamily: 'var(--font-mono)', fontSize: 10,
              padding: '3px 10px', borderRadius: 5, cursor: 'pointer',
              background: composeOpen ? 'rgba(0,180,216,0.18)' : 'rgba(0,180,216,0.08)',
              border: '1px solid rgba(0,180,216,0.3)', color: '#00b4d8',
            }}
          >
            {composeOpen ? t('profileBody.cancel') : t('profileBody.compose')}
          </button>
        )}
      </div>

      {composeOpen && (
        <div style={{
          marginTop: 10, padding: 10, borderRadius: 6,
          background: 'var(--elevated)', border: '1px solid var(--border)',
          display: 'flex', flexDirection: 'column', gap: 8,
        }}>
          {connections.length === 0 ? (
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>
              {t('profileBody.noOutlook')}
            </div>
          ) : (
            <>
              {connections.length > 1 && (
                <select
                  value={connectionId}
                  onChange={e => setConnectionId(e.target.value)}
                  className="select-compact"
                  style={{ fontSize: 11 }}
                >
                  <option value="">{t('profileBody.selectAccount')}</option>
                  {connections.map(c => <option key={c.id} value={c.id}>{c.label || c.account_id || c.id}</option>)}
                </select>
              )}
              <input
                placeholder={t('profileBody.subject')}
                value={subject}
                onChange={e => setSubject(e.target.value)}
                style={{ fontFamily: 'var(--font-mono)', fontSize: 11, padding: '6px 8px', borderRadius: 4, background: 'var(--bg)', border: '1px solid var(--border)', color: 'var(--text)' }}
              />
              <textarea
                placeholder={t('profileBody.writeMessage')}
                value={body}
                onChange={e => setBody(e.target.value)}
                rows={4}
                style={{ fontFamily: 'var(--font-mono)', fontSize: 11, padding: '6px 8px', borderRadius: 4, background: 'var(--bg)', border: '1px solid var(--border)', color: 'var(--text)', resize: 'vertical' }}
              />
              {composeError && (
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: '#ef4444' }}>{composeError}</div>
              )}
              <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
                <button
                  onClick={() => handleCompose(true)}
                  disabled={!!sending || !connectionId || !body.trim()}
                  style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, padding: '5px 12px', borderRadius: 5, cursor: 'pointer', background: 'rgba(255,255,255,0.06)', border: '1px solid var(--border)', color: 'var(--text-secondary)', opacity: sending && sending !== 'draft' ? 0.5 : 1 }}
                >{sending === 'draft' ? t('profileBody.saving') : t('profileBody.saveDraft')}</button>
                <button
                  onClick={() => handleCompose(false)}
                  disabled={!!sending || !connectionId || !body.trim()}
                  style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, padding: '5px 14px', borderRadius: 5, cursor: 'pointer', background: 'rgba(16,185,129,0.15)', border: '1px solid rgba(16,185,129,0.4)', color: '#10b981', opacity: sending && sending !== 'send' ? 0.5 : 1 }}
                >{sending === 'send' ? t('profileBody.sending') : t('profilePage.send')}</button>
              </div>
            </>
          )}
        </div>
      )}

      {open && !loading && messages.length > 0 && (
        <div style={{ display: 'flex', gap: 4, marginTop: 10 }}>
          {[
            { key: 'all', label: t('profileBody.filterAll', { count: messages.length }) },
            { key: 'inbound', label: t('profileBody.filterReceived', { count: messages.length - sentCount }) },
            { key: 'outbound', label: t('profileBody.filterSent', { count: sentCount }) },
          ].map(f => (
            <button
              key={f.key}
              onClick={() => setDirectionFilter(f.key)}
              style={{
                fontFamily: 'var(--font-mono)', fontSize: 10,
                padding: '3px 9px', borderRadius: 5, cursor: 'pointer',
                background: directionFilter === f.key ? 'rgba(0,180,216,0.15)' : 'transparent',
                border: '1px solid ' + (directionFilter === f.key ? 'rgba(0,180,216,0.3)' : 'var(--border)'),
                color: directionFilter === f.key ? '#00b4d8' : 'var(--text-muted)',
              }}
            >{f.label}</button>
          ))}
        </div>
      )}

      {open && (
        <div style={{ marginTop: 10 }}>
          {loading ? (
            <div style={{ padding: '12px 0', textAlign: 'center' }}>
              <div className="spinner" style={{ width: 14, height: 14, margin: '0 auto' }} />
            </div>
          ) : loadError ? (
            <div role="alert" style={{ padding: '12px 0', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--red)' }}>
              {t('profilePage.loadFailed', { error: loadError })}
            </div>
          ) : visibleMessages.length === 0 ? (
            <div style={{
              padding: '16px 0', textAlign: 'center',
              color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', fontSize: 11,
            }}>
              {messages.length === 0 ? t('profileBody.noMessagesYet') : t('profileBody.noMessagesView')}
            </div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {visibleMessages.map(msg => (
                <div
                  key={msg.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => setOpenMessage(msg)}
                  onKeyDown={e => { if (e.key === 'Enter') setOpenMessage(msg) }}
                  style={{
                  display: 'flex', flexDirection: 'column', gap: 4,
                  padding: '8px 10px', borderRadius: 6, cursor: 'pointer',
                  background: 'var(--elevated)',
                  border: '1px solid var(--border)',
                }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    {isUnread(msg) && <UnreadDot title={t('profileBody.new')} />}
                    {msg.direction === 'outbound'
                      ? <ArrowUpRight size={11} style={{ color: '#10b981', flexShrink: 0 }} title={t('profileBody.sentTitle')} />
                      : <ArrowDownLeft size={11} style={{ color: 'var(--text-muted)', flexShrink: 0 }} title={t('profileBody.receivedTitle')} />
                    }
                    <span style={{
                      padding: '1px 6px', borderRadius: 4,
                      background: 'rgba(0,180,216,0.12)',
                      border: '1px solid rgba(0,180,216,0.3)',
                      color: '#00b4d8', fontSize: 9,
                      fontFamily: 'var(--font-mono)', flexShrink: 0,
                    }}>
                      {msg.source}
                    </span>
                    {msg.status === 'draft' && (
                      <span style={{
                        padding: '1px 6px', borderRadius: 4,
                        background: 'rgba(245,158,11,0.12)',
                        border: '1px solid rgba(245,158,11,0.35)',
                        color: '#f59e0b', fontSize: 9,
                        fontFamily: 'var(--font-mono)', flexShrink: 0,
                      }}>
                        {t('profileBody.draftBadge')}
                      </span>
                    )}
                    <span style={{
                      fontFamily: 'var(--font-mono)', fontSize: 11,
                      color: 'var(--text)', fontWeight: 500,
                      overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }}>
                      {msg.subject || t('profileBody.noSubject')}
                    </span>
                    <span style={{
                      marginLeft: 'auto', flexShrink: 0,
                      fontFamily: 'var(--font-mono)', fontSize: 10,
                      color: 'var(--text-muted)',
                    }}>
                      {msg.sent_at ? msg.sent_at.slice(0, 16).replace('T', ' ') : ''}
                    </span>
                  </div>
                  {msg.body && (
                    <div style={{
                      fontFamily: 'var(--font-mono)', fontSize: 10.5,
                      color: 'var(--text-muted)',
                      overflow: 'hidden', textOverflow: 'ellipsis',
                      display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical',
                    }}>
                      {stripHTML(msg.body)}
                    </div>
                  )}
                  {msg.status === 'draft' && (
                    <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
                      <button
                        onClick={e => { e.stopPropagation(); handleRejectDraft(msg.id) }}
                        disabled={draftActionId === msg.id}
                        style={{ fontFamily: 'var(--font-mono)', fontSize: 10, padding: '3px 10px', borderRadius: 5, cursor: 'pointer', background: 'rgba(239,68,68,0.1)', border: '1px solid rgba(239,68,68,0.3)', color: '#ef4444' }}
                      >{draftActionId === msg.id ? '…' : t('profilePage.discard')}</button>
                      <button
                        onClick={e => { e.stopPropagation(); handleSendDraft(msg.id) }}
                        disabled={draftActionId === msg.id}
                        style={{ fontFamily: 'var(--font-mono)', fontSize: 10, padding: '3px 10px', borderRadius: 5, cursor: 'pointer', background: 'rgba(16,185,129,0.15)', border: '1px solid rgba(16,185,129,0.4)', color: '#10b981' }}
                      >{draftActionId === msg.id ? '…' : t('profilePage.send')}</button>
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
          {draftError && <div role="alert" data-testid="draft-error" style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: '#ef4444', marginTop: 6 }}>{draftError}</div>}
        </div>
      )}

      {openMessage && (
        <MessageDetailModal
          message={openMessage}
          personLabel={personLabel}
          onClose={() => setOpenMessage(null)}
        />
      )}
    </div>
  )
}

// Quote-box showing a person's latest manually-written status update (e.g.
// "Just closed the Q1 deal"), with an inline form to post a new one. The
// "History" link is wired up by StatusHistoryModal in a later change.
function StatusSection({ personId, platformColor }) {
  const [latest, setLatest]   = useState(null)
  const [loading, setLoading] = useState(true)
  const [text, setText]       = useState('')
  const [posting, setPosting] = useState(false)
  const [postError, setPostError] = useState('')
  const { t } = useTranslation()
  const [historyOpen, setHistoryOpen] = useState(false)

  const reload = () => api.getLatestPersonStatus(personId).then(setLatest)

  useEffect(() => {
    setLoading(true)
    reload().catch(() => {}).finally(() => setLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [personId])

  const post = async () => {
    const value = text.trim()
    if (!value || posting) return
    setPosting(true)
    setPostError('')
    try {
      const created = await api.addPersonStatus(personId, value)
      if (created) {
        setLatest(created)
        setText('')
      } else {
        setPostError(t('profilePage.statusNotSaved'))
      }
    } catch (e) {
      setPostError(e?.message || String(e))
    } finally {
      setPosting(false)
    }
  }

  if (loading) return null

  return (
    <div className="profile-status-box" style={{ '--platform-color': platformColor }}>
      {latest ? (
        <p className="profile-status-quote">&ldquo;{latest.text}&rdquo;</p>
      ) : (
        <p className="profile-status-empty">{t('profileBody.noStatus')}</p>
      )}
      <div className="profile-status-meta">
        {latest && <span>{latest.created_at.slice(0, 16).replace('T', ' ')}</span>}
        <button className="profile-status-history-link" onClick={() => setHistoryOpen(true)}>
          {t('profileBody.history')}
        </button>
      </div>
      <div className="profile-status-form">
        <input
          className="profile-status-input"
          placeholder={t('profileBody.statusPlaceholder')}
          value={text}
          onChange={e => setText(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') post() }}
          disabled={posting}
        />
        <button className="btn btn-secondary btn-sm" onClick={post} disabled={posting || !text.trim()}>
          {t('profileBody.post')}
        </button>
      </div>
      {postError && <div role="alert" style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: '#ef4444', marginTop: 6 }}>{postError}</div>}

      {historyOpen && (
        <StatusHistoryModal personId={personId} onClose={() => setHistoryOpen(false)} />
      )}
    </div>
  )
}

// Strips HTML tags for the plain-text preview snippet; full HTML is
// rendered properly in MessageDetailModal via a sandboxed iframe.
function stripHTML(body) {
  return body.replace(/<style[\s\S]*?<\/style>/gi, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim()
}

export default function Profile({ id, onBack, onOpenURL, onOpenPost }) {
  const { t } = useTranslation()
  const [person, setPerson] = useState(null)
  const [interactions, setInteractions] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [filterType, setFilterType] = useState('')

  const loadSeq = useRef(0)
  const load = async () => {
    if (!id) return
    const seq = ++loadSeq.current
    // Refresh keeps the page (and its open sections and drafts) on screen.
    if (!person || person.id !== id) setLoading(true)
    try {
      setError(null)
      const [p, i] = await Promise.all([
        api.getPersonDetail(id),
        api.getPersonInteractions(id),
      ])
      if (seq !== loadSeq.current) return
      setPerson(p)
      setInteractions(i || [])
    } catch (e) {
      if (seq === loadSeq.current) setError(e?.message || t('profileBody.loadProfileFailed'))
    } finally {
      if (seq === loadSeq.current) setLoading(false)
    }
  }

  useEffect(() => { load() }, [id])

  if (loading) {
    return (
      <div className="empty-state" style={{ height: '100%' }}>
        <div className="spinner" />
      </div>
    )
  }

  if (error) {
    return (
      <div className="empty-state" style={{ height: '100%' }}>
        <div style={{ padding: '12px 16px', background: 'rgba(239,68,68,.08)', border: '1px solid rgba(239,68,68,.2)', borderRadius: 'var(--radius)', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--red)', marginBottom: 12 }}>{error}</div>
        <button className="btn btn-secondary btn-sm" onClick={onBack} style={{ marginTop: 12 }}>
          <ArrowLeft size={13} /> {t('profileBody.back')}
        </button>
      </div>
    )
  }

  if (!person) {
    return (
      <div className="empty-state" style={{ height: '100%' }}>
        <div className="empty-state-title">{t('profileBody.notFound')}</div>
        <button className="btn btn-secondary btn-sm" onClick={onBack} style={{ marginTop: 12 }}>
          <ArrowLeft size={13} /> {t('profileBody.back')}
        </button>
      </div>
    )
  }

  const platformColor = PLATFORM_COLORS[person.platform?.toUpperCase()] || 'var(--cyan)'
  const profileUrl = person.profile_url || PLATFORM_PROFILE_URL[person.platform?.toUpperCase()]?.(person.username)

  const actionTypes = [...new Set(interactions.map(i => i.node_type).filter(Boolean))]
  const filtered = filterType ? interactions.filter(i => i.node_type === filterType) : interactions

  // Summarise interaction counts
  const summary = {}
  interactions.forEach(i => {
    if (i.node_type) summary[i.node_type] = (summary[i.node_type] || 0) + 1
  })

  return (
    <div className="profile-page">
      {/* ── Header bar ── */}
      <div className="page-header">
        <div className="page-header-left">
          <button className="btn btn-ghost btn-sm" onClick={onBack} style={{ gap: 6 }}>
            <ArrowLeft size={13} /> {t('profileBody.people')}
          </button>
          <div className="page-subtitle" style={{ margin: '0 4px', color: 'var(--text-dim)' }}>/</div>
          {profileUrl ? (
            <button className="page-subtitle btn btn-ghost btn-sm" style={{ padding: '0 4px' }} onClick={() => onOpenURL(profileUrl)}>
              @{person.username} <ExternalLink size={11} style={{ opacity: 0.5 }} />
            </button>
          ) : (
            <div className="page-subtitle">@{person.username}</div>
          )}
        </div>
        <div className="page-header-right">
          <button className="btn btn-ghost btn-sm" onClick={() => load()} style={{ gap: 5 }}>
            <RefreshCw size={12} /> {t('profileBody.refresh')}
          </button>
          {profileUrl && (
            <button className="btn btn-secondary btn-sm" onClick={() => onOpenURL(profileUrl)} style={{ gap: 6 }}>
              <ExternalLink size={12} /> {t('profileBody.openOn', { platform: person.platform })}
            </button>
          )}
        </div>
      </div>

      <div className="page-body profile-body">
        {/* ── Hero card ── */}
        <div className="profile-hero" style={{ '--platform-color': platformColor }}>
          {/* Background blurred photo */}
          {person.image_url && (
            <div className="profile-hero-bg" style={{ backgroundImage: `url(${person.image_url})` }} />
          )}
          <div className="profile-hero-content">
            <Avatar username={person.username} imageUrl={person.image_url} size={80} />
            <div className="profile-hero-info">
              <div className="profile-name-row">
                <h1 className="profile-name">{person.full_name || person.username}</h1>
                {person.is_verified && (
                  <CheckCircle size={18} style={{ color: 'var(--cyan)', flexShrink: 0 }} />
                )}
                <span
                  className="badge"
                  style={{ background: platformColor + '20', color: platformColor, borderColor: platformColor + '40' }}
                >
                  {person.platform}
                </span>
              </div>
              {profileUrl ? (
                <button className="profile-username profile-username-link" onClick={() => onOpenURL(profileUrl)}>
                  @{person.username}
                </button>
              ) : (
                <div className="profile-username">@{person.username}</div>
              )}
              {(person.headline || person.job_title || person.category) && (
                <div className="profile-role">{person.headline || person.job_title || person.category}</div>
              )}
              {/* about is the profile's own bio; introduction is the drafted
                  outreach message, shown as the bio only when there is none. */}
              {(person.about || person.introduction) && (
                <p className="profile-bio">{person.about || person.introduction}</p>
              )}
              {person.about && person.introduction && (
                <div className="profile-intro">
                  <span className="profile-entry-label">{t('personProfile.introduction')}:</span> {person.introduction}
                </div>
              )}
              <StatusSection personId={id} platformColor={platformColor} />
              <div className="profile-links">
                {person.location && (
                  <span className="profile-meta-link">
                    <MapPin size={12} /> {person.location}
                  </span>
                )}
                {person.website && (
                  <button className="profile-meta-link" onClick={() => onOpenURL(person.website)}>
                    <Globe size={12} /> {person.website.replace(/^https?:\/\//, '')}
                  </button>
                )}
                {person.contact_details && (
                  <span className="profile-meta-link">
                    <Mail size={12} /> {person.contact_details}
                  </span>
                )}
              </div>
            </div>
          </div>

          {/* Stats row */}
          <div className="profile-stats">
            <StatPill label={t('profileBody.followers')}  value={person.follower_count}  icon={Users} />
            <StatPill label={t('profileBody.following')}  value={person.following_count || null} icon={Users} />
            <StatPill label={t('profileBody.statPosts')}      value={person.content_count || null}   icon={FileText} />
            <StatPill label={t('profileBody.interactions')} value={interactions.length || null}  icon={Zap} />
          </div>
        </div>

        {/* ── Interaction summary chips ── */}
        {Object.keys(summary).length > 0 && (
          <div className="profile-summary-chips">
            <button
              className={`summary-chip ${!filterType ? 'active' : ''}`}
              onClick={() => setFilterType('')}
            >
              {t('profileBody.all')} <span>{interactions.length}</span>
            </button>
            {Object.entries(summary).sort((a,b) => b[1]-a[1]).map(([type, count]) => {
              const Icon = (ACTION_META[type] || { icon: Zap }).icon
              return (
                <button
                  key={type}
                  className={`summary-chip ${filterType === type ? 'active' : ''}`}
                  onClick={() => setFilterType(filterType === type ? '' : type)}
                >
                  <Icon size={11} />
                  {actionLabel(t, type)} <span>{count}</span>
                </button>
              )
            })}
          </div>
        )}

        {/* ── Experience / education (profile reads) ── */}
        <ProfileDetailsSection details={person.profile_details} onOpenURL={onOpenURL} />
        <ExperienceSection experience={person.experience} onOpenURL={onOpenURL} />
        <EducationSection education={person.education} onOpenURL={onOpenURL} />

        {/* ── Posts section ── */}
        <PostsSection
          personId={id}
          onOpenPost={onOpenPost}
          onOpenURL={onOpenURL}
        />

        {/* ── Messages section ── */}
        <MessagesSection personId={id} personLabel={person.full_name || person.username} personPlatform={person.platform} />

        {/* ── Interaction history ── */}
        <div className="profile-section">
          <div className="profile-section-title">
            {t('profileBody.interactionHistory')}
            <span style={{ color: 'var(--text-muted)', fontWeight: 400, marginLeft: 8, fontSize: 11 }}>
              {t('profileBody.events', { count: filtered.length })}
            </span>
          </div>

          {filtered.length === 0 ? (
            <div style={{ padding: '24px 0', textAlign: 'center', color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', fontSize: 12 }}>
              {t('profileBody.noInteractions')}
            </div>
          ) : (
            <div className="interaction-list">
              {filtered.map((item, i) => (
                <InteractionRow key={`${item.execution_id}-${i}`} item={item} />
              ))}
            </div>
          )}
        </div>

        {/* ── Meta footer ── */}
        <div style={{ display: 'flex', gap: 20, padding: '4px 0 8px', fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-dim)' }}>
          <span>{t('profileBody.added', { date: person.created_at?.slice(0, 10) || '—' })}</span>
          {person.updated_at && person.updated_at !== person.created_at && (
            <span>{t('profileBody.updated', { date: person.updated_at.slice(0, 10) })}</span>
          )}
          <span style={{ marginLeft: 'auto' }}>{t('profileBody.idLabel', { id: person.id })}</span>
        </div>
      </div>
    </div>
  )
}
