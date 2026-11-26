import { useState, useEffect, useCallback, useRef } from 'react'
import { createPortal } from 'react-dom'
import { Users, Search, RefreshCw, CheckCircle, ExternalLink, Plus, X, Tag, Check, Palette, Trash2 } from 'lucide-react'
import { api, subscribeEvent } from '../services/api.js'
import { useReloadOnActivate } from '../lib/useReloadOnActivate'
import { confirm } from '../components/ConfirmDialog.jsx'
import { useTranslation } from 'react-i18next'
import { loginRequiredInError } from '../lib/accountBindingWatch.js'
import { commsByPerson, isCompany, isOwnAccount, onCommsChanged } from '../lib/commsStatus.js'
import t from '../lib/i18nOr.js'

// ── Tag colour palette ────────────────────────────────────────
export const TAG_COLORS = [
  '#00b4d8', // Cyan
  '#10b981', // Emerald
  '#7c3aed', // Purple
  '#e1306c', // Rose
  '#f59e0b', // Amber
  '#f97316', // Orange
  '#d946ef', // Fuchsia
  '#00f5d4', // Mint
  '#3b82f6', // Blue
  '#6366f1', // Indigo
  '#14b8a6', // Teal
  '#ef4444', // Red
]

// ── Curated Suggested Tags by Purpose ─────────────────────────
export const TAG_SUGGESTION_GROUPS = [
  {
    get title() { return t('ui6PagesPeople.group_sales_title') },
    get description() { return t('ui6PagesPeople.group_sales_desc') },
    tags: [
      { name: 'Lead', color: '#00b4d8' },
      { name: 'Prospect', color: '#0284c7' },
      { name: 'Contacted', color: '#eab308' },
      { name: 'Qualified', color: '#8b5cf6' },
      { name: 'Proposal', color: '#a855f7' },
      { name: 'Negotiation', color: '#f59e0b' },
      { name: 'Won', color: '#10b981' },
      { name: 'Lost', color: '#64748b' },
      { name: 'Irrelevant', color: '#94a3b8' },
    ],
  },
  {
    get title() { return t('ui6PagesPeople.group_role_title') },
    get description() { return t('ui6PagesPeople.group_role_desc') },
    tags: [
      { name: 'Founder', color: '#7c3aed' },
      { name: 'Co-Founder', color: '#8b5cf6' },
      { name: 'C-Level', color: '#d946ef' },
      { name: 'Decision Maker', color: '#f59e0b' },
      { name: 'Investor', color: '#10b981' },
      { name: 'Advisor', color: '#06b6d4' },
      { name: 'Executive', color: '#3b82f6' },
      { name: 'Employee', color: '#64748b' },
    ],
  },
  {
    get title() { return t('ui6PagesPeople.group_advocacy_title') },
    get description() { return t('ui6PagesPeople.group_advocacy_desc') },
    tags: [
      { name: 'Champion', color: '#e1306c' },
      { name: 'Sales Agent', color: '#f97316' },
      { name: 'Influencer', color: '#ec4899' },
      { name: 'Advertiser', color: '#00f5d4' },
      { name: 'PR', color: '#6366f1' },
      { name: 'Partner', color: '#14b8a6' },
      { name: 'Customer', color: '#10b981' },
    ],
  },
]

const PLATFORM_PROFILE_URL = {
  INSTAGRAM: (u) => `https://www.instagram.com/${u}/`,
  LINKEDIN:  (u) => `https://www.linkedin.com/in/${u}/`,
  X:         (u) => `https://x.com/${u}`,
  TIKTOK:    (u) => `https://www.tiktok.com/@${u}`,
}

// ── Avatar ────────────────────────────────────────────────────
function Avatar({ username, imageUrl }) {
  const [imgFailed, setImgFailed] = useState(false)
  const initials = (username || '?').slice(0, 2).toUpperCase()
  const pairs = [
    ['#7c3aed','#00b4d8'], ['#00b4d8','#00f5d4'], ['#e1306c','#7c3aed'],
    ['#f97316','#eab308'], ['#10b981','#00b4d8'],
  ]
  const pair = pairs[(username?.charCodeAt(0) || 0) % pairs.length]

  if (imageUrl && !imgFailed) {
    return (
      <div className="person-avatar" style={{ padding: 0, overflow: 'hidden', background: 'var(--elevated)' }}>
        <img src={imageUrl} alt={username} onError={() => setImgFailed(true)}
          style={{ width: '100%', height: '100%', objectFit: 'cover', borderRadius: 'inherit' }} />
      </div>
    )
  }
  return (
    <div className="person-avatar" style={{ background: `linear-gradient(135deg, ${pair[0]}, ${pair[1]})` }}>
      {initials}
    </div>
  )
}

// kindBadge: a small outlined tag; the text always says what it is.
const kindBadge = (color) => ({
  padding: '1px 6px', borderRadius: 4, fontSize: 9, fontFamily: 'var(--font-mono)',
  color, border: `1px solid ${color}`, flexShrink: 0,
})

function PlatformBadge({ platform }) {
  return <span className={`badge badge-platform-${(platform || '').toLowerCase()}`}>{platform}</span>
}

// ── Single tag chip (read-only) ───────────────────────────────
function TagChip({ tag }) {
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center',
      padding: '2px 8px', borderRadius: 4,
      background: `${tag.color}1a`,
      border: `1px solid ${tag.color}55`,
      color: tag.color,
      fontSize: 10, fontFamily: 'var(--font-mono)',
      whiteSpace: 'nowrap', maxWidth: 90,
      overflow: 'hidden', textOverflow: 'ellipsis',
      lineHeight: 1.6,
    }}>
      {tag.name}
    </span>
  )
}

// ── Tag Editor Modal ──────────────────────────────────────────
function TagEditor({ personId, username, fullName, onClose }) {
  useTranslation()
  const [tags, setTags]               = useState([])
  const [allTags, setAllTags]         = useState([])
  const [input, setInput]             = useState('')
  const [selColor, setSelColor]       = useState(TAG_COLORS[0])
  const [loading, setLoading]         = useState(true)
  const [suggestions, setSuggestions] = useState([])
  const [editingColorTagId, setEditingColorTagId] = useState(null)
  const inputRef = useRef(null)
  const rootRef  = useRef(null)
  const [tagError, setTagError] = useState('')
  // A failed write must say so; the optimistic change is not applied then.
  const guarded = (fn) => async (...args) => {
    setTagError('')
    try { return await fn(...args) } catch (e) { setTagError(e?.message || String(e)) }
  }

  // Load person's current tags + all global tags
  useEffect(() => {
    Promise.all([
      api.getPersonTags(personId),
      api.getAllTags(),
    ]).then(([pt, at]) => {
      setTags(pt || [])
      setAllTags(at || [])
    }).catch(e => setTagError(e?.message || String(e)))
      .finally(() => setLoading(false))
  }, [personId])

  // Update suggestions when input changes
  useEffect(() => {
    const q = input.trim().toLowerCase()
    if (!q) { setSuggestions([]); return }
    const already = new Set(tags.map(t => t.id))
    setSuggestions(
      allTags.filter(t =>
        t.name.toLowerCase().includes(q) && !already.has(t.id)
      ).slice(0, 6)
    )
  }, [input, allTags, tags])

  // Close on Escape key
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  useEffect(() => { inputRef.current?.focus() }, [])

  const addExisting = guarded(async (tag) => {
    if (tags.length >= 10) return
    const result = await api.addPersonTag(personId, tag.name, tag.color)
    if (result) {
      setTags(prev => [...prev.filter(t => t.id !== result.id), result])
    }
    setInput('')
    setSuggestions([])
    inputRef.current?.focus()
  })

  const toggleSuggestedTag = guarded(async (sTag) => {
    const existing = tags.find(t => t.name.toLowerCase() === sTag.name.toLowerCase())
    if (existing) {
      await api.removePersonTag(personId, existing.id)
      setTags(prev => prev.filter(t => t.id !== existing.id))
      if (editingColorTagId === existing.id) setEditingColorTagId(null)
    } else {
      if (tags.length >= 10) return
      const known = allTags.find(t => t.name.toLowerCase() === sTag.name.toLowerCase())
      const colorToUse = known ? known.color : sTag.color
      const result = await api.addPersonTag(personId, sTag.name, colorToUse)
      if (result) {
        setTags(prev => [...prev.filter(t => t.id !== result.id), result])
        setAllTags(prev => prev.some(t => t.id === result.id) ? prev : [...prev, result])
      }
    }
  })

  const addNew = guarded(async () => {
    const name = input.trim()
    if (!name || tags.length >= 10) return
    const exact = allTags.find(t => t.name.toLowerCase() === name.toLowerCase())
    const color = exact ? exact.color : selColor
    const result = await api.addPersonTag(personId, name, color)
    if (result) {
      setTags(prev => [...prev.filter(t => t.id !== result.id), result])
      setAllTags(prev => prev.some(t => t.id === result.id) ? prev : [...prev, result])
    }
    setInput('')
    setSuggestions([])
    inputRef.current?.focus()
  })

  const remove = guarded(async (tagId) => {
    await api.removePersonTag(personId, tagId)
    setTags(prev => prev.filter(t => t.id !== tagId))
    if (editingColorTagId === tagId) setEditingColorTagId(null)
  })

  const changeTagColor = guarded(async (tagId, newColor) => {
    await api.updateTagColor(tagId, newColor)
    setTags(prev => prev.map(t => t.id === tagId ? { ...t, color: newColor } : t))
    setAllTags(prev => prev.map(t => t.id === tagId ? { ...t, color: newColor } : t))
    setEditingColorTagId(null)
  })

  const isNewTag = input.trim() && !allTags.some(t =>
    t.name.toLowerCase() === input.trim().toLowerCase()
  )

  const displayName = fullName || (username ? `@${username}` : t('ui6PagesPeople.contact'))

  return createPortal(
    <div
      className="modal-overlay"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 10000,
        background: 'rgba(3, 7, 18, 0.8)',
        backdropFilter: 'blur(8px)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: 20,
        animation: 'fade-in 120ms ease',
      }}
    >
      <div
        ref={rootRef}
        role="dialog"
        aria-modal="true"
        aria-label={t('ui6PagesPeople.manage_tags_for', { name: displayName })}
        style={{
          width: 620,
          maxWidth: '96vw',
          maxHeight: '90vh',
          display: 'flex',
          flexDirection: 'column',
          background: 'linear-gradient(165deg, #0d1624 0%, #070d16 100%)',
          border: '1.5px solid rgba(0,180,216,0.35)',
          borderRadius: 12,
          boxShadow: '0 24px 70px rgba(0,0,0,0.85), 0 0 0 1px rgba(0,180,216,0.1)',
          overflow: 'hidden',
          animation: 'slide-up 180ms ease',
        }}
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div style={{
          padding: '14px 20px',
          borderBottom: '1px solid rgba(0,180,216,0.16)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          background: 'rgba(0,180,216,0.06)',
          flexShrink: 0,
        }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <div style={{
              width: 34, height: 34, borderRadius: 8,
              background: 'rgba(0,180,216,0.15)',
              border: '1px solid rgba(0,180,216,0.3)',
              display: 'flex', alignItems: 'center', justifyContent: 'center',
            }}>
              <Tag size={17} color="#00b4d8" />
            </div>
            <div>
              <div style={{ fontSize: 14, fontWeight: 600, color: '#e2e8f0', display: 'flex', alignItems: 'center', gap: 6 }}>
                {t('ui6PagesPeople.manage_tags')}
                {username && (
                  <span style={{ fontSize: 12, fontWeight: 400, color: '#00b4d8', fontFamily: 'var(--font-mono)' }}>
                    @{username}
                  </span>
                )}
              </div>
              <div style={{ fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', marginTop: 2 }}>
                {fullName && fullName !== username ? `${fullName} · ` : ''}{t('ui6PagesPeople.tags_assigned', { n: tags.length })}
              </div>
            </div>
          </div>
          <button
            onClick={onClose}
            style={{
              background: 'none', border: 'none', cursor: 'pointer',
              color: 'var(--text-muted)', padding: 6, borderRadius: 6,
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              transition: 'color 120ms',
            }}
            onMouseEnter={e => e.currentTarget.style.color = '#fff'}
            onMouseLeave={e => e.currentTarget.style.color = 'var(--text-muted)'}
            title={t('ui6PagesPeople.close')}
          >
            <X size={16} />
          </button>
        </div>

        {/* Scrollable Body */}
        <div style={{ padding: '18px 22px', flex: 1, overflowY: 'auto' }}>
          {/* 1. Assigned Tags Section */}
          <div style={{ marginBottom: 18 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
              <span style={{
                fontSize: 10, fontFamily: 'var(--font-mono)', color: 'var(--text-muted)',
                textTransform: 'uppercase', letterSpacing: 1.5,
              }}>
                {t('ui6PagesPeople.assigned_tags', { n: tags.length })}
              </span>
              {tags.length > 0 && !editingColorTagId && (
                <span style={{ fontSize: 10, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)' }}>
                  {t('ui6PagesPeople.click_dot')}
                </span>
              )}
            </div>

            {tagError && <div role="alert" data-testid="tag-error" style={{ fontSize: 11, color: 'var(--red)', marginBottom: 6 }}>{tagError}</div>}
            {loading ? (
              <div style={{ height: 32, display: 'flex', alignItems: 'center', gap: 8, color: 'var(--text-muted)', fontSize: 12 }}>
                <div className="spinner" style={{ width: 14, height: 14 }} /> {t('ui6PagesPeople.loading_tags')}
              </div>
            ) : tags.length > 0 ? (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 7 }}>
                {tags.map(tag => (
                  <div
                    key={tag.id}
                    style={{
                      display: 'inline-flex', alignItems: 'center', gap: 6,
                      padding: '4px 10px', borderRadius: 6,
                      background: `${tag.color}1a`,
                      border: `1.5px solid ${tag.color}66`,
                      color: tag.color,
                      fontSize: 11, fontFamily: 'var(--font-mono)',
                    }}
                  >
                    {/* Color dot button: click to customize color */}
                    <button
                      type="button"
                      onClick={() => setEditingColorTagId(editingColorTagId === tag.id ? null : tag.id)}
                      title={t('ui6PagesPeople.customize_color')}
                      style={{
                        width: 12, height: 12, borderRadius: '50%',
                        background: tag.color,
                        border: '1.5px solid rgba(255,255,255,0.7)',
                        cursor: 'pointer', padding: 0,
                        boxShadow: `0 0 6px ${tag.color}`,
                        display: 'flex', alignItems: 'center', justifyContent: 'center',
                      }}
                    />
                    <span style={{ fontWeight: 500 }}>{tag.name}</span>
                    <button
                      type="button"
                      onClick={() => remove(tag.id)}
                      style={{
                        background: 'none', border: 'none', cursor: 'pointer',
                        color: `${tag.color}99`, padding: 0,
                        display: 'flex', alignItems: 'center', justifyContent: 'center',
                        transition: 'color 100ms',
                      }}
                      onMouseEnter={e => e.currentTarget.style.color = '#fff'}
                      onMouseLeave={e => e.currentTarget.style.color = `${tag.color}99`}
                      title={t('ui6PagesPeople.remove_tag_named', { name: tag.name })}
                    >
                      <X size={12} />
                    </button>
                  </div>
                ))}
              </div>
            ) : (
              <div style={{
                fontSize: 12, color: 'var(--text-muted)',
                padding: '12px 14px', background: 'rgba(255,255,255,0.02)',
                borderRadius: 6, border: '1px dashed rgba(255,255,255,0.1)',
              }}>
                {t('ui6PagesPeople.no_tags')}
              </div>
            )}

            {/* Inline color customization panel for selected tag */}
            {editingColorTagId && (
              <div style={{
                marginTop: 10,
                padding: '10px 14px',
                background: 'rgba(0,180,216,0.06)',
                borderRadius: 8,
                border: '1px solid rgba(0,180,216,0.25)',
                display: 'flex',
                alignItems: 'center',
                gap: 10,
                flexWrap: 'wrap',
                animation: 'fade-in 100ms ease',
              }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 11, color: '#e2e8f0', fontFamily: 'var(--font-mono)' }}>
                  <Palette size={13} color="#00b4d8" />
                  {t('ui6PagesPeople.color_for')} <strong>{tags.find(t => t.id === editingColorTagId)?.name}</strong>:
                </div>
                <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
                  {TAG_COLORS.map(c => (
                    <button
                      key={c}
                      type="button"
                      onClick={() => changeTagColor(editingColorTagId, c)}
                      title={t('ui6PagesPeople.set_color', { color: c })}
                      style={{
                        width: 20, height: 20, borderRadius: '50%',
                        background: c,
                        border: '2px solid rgba(255,255,255,0.4)',
                        cursor: 'pointer', padding: 0,
                        boxShadow: `0 0 6px ${c}66`,
                        transition: 'transform 100ms',
                      }}
                      onMouseEnter={e => e.currentTarget.style.transform = 'scale(1.2)'}
                      onMouseLeave={e => e.currentTarget.style.transform = 'scale(1)'}
                    />
                  ))}
                </div>
                <button
                  type="button"
                  onClick={() => setEditingColorTagId(null)}
                  style={{
                    marginLeft: 'auto', background: 'none', border: 'none',
                    color: 'var(--text-muted)', cursor: 'pointer', fontSize: 11,
                    fontFamily: 'var(--font-mono)',
                  }}
                >
                  {t('ui6PagesPeople.cancel')}
                </button>
              </div>
            )}
          </div>

          {/* 2. Curated Suggested Tag Groups */}
          <div style={{ marginBottom: 20 }}>
            <div style={{
              fontSize: 10, fontFamily: 'var(--font-mono)', color: 'var(--text-muted)',
              textTransform: 'uppercase', letterSpacing: 1.5, marginBottom: 10,
            }}>
              {t('ui6PagesPeople.suggested')}
            </div>

            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {TAG_SUGGESTION_GROUPS.map(group => (
                <div
                  key={group.title}
                  style={{
                    padding: '10px 14px',
                    background: 'rgba(255,255,255,0.02)',
                    borderRadius: 8,
                    border: '1px solid rgba(255,255,255,0.06)',
                  }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
                    <span style={{ fontSize: 11, fontWeight: 600, color: '#cbd5e1' }}>
                      {group.title}
                    </span>
                    <span style={{ fontSize: 10, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)' }}>
                      {group.description}
                    </span>
                  </div>

                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                    {group.tags.map(stag => {
                      const isAssigned = tags.some(t => t.name.toLowerCase() === stag.name.toLowerCase())
                      const assignedTag = isAssigned ? tags.find(t => t.name.toLowerCase() === stag.name.toLowerCase()) : null
                      const activeColor = assignedTag ? assignedTag.color : stag.color

                      return (
                        <button
                          key={stag.name}
                          type="button"
                          onClick={() => toggleSuggestedTag(stag)}
                          disabled={!isAssigned && tags.length >= 10}
                          title={isAssigned ? t('ui6PagesPeople.assigned_remove', { name: stag.name }) : t('ui6PagesPeople.click_assign', { name: stag.name })}
                          style={{
                            display: 'inline-flex',
                            alignItems: 'center',
                            gap: 5,
                            padding: '3px 9px',
                            borderRadius: 5,
                            background: isAssigned ? `${activeColor}2b` : `${stag.color}0d`,
                            border: isAssigned ? `1.5px solid ${activeColor}` : `1px solid ${stag.color}44`,
                            color: isAssigned ? '#fff' : stag.color,
                            fontSize: 11,
                            fontFamily: 'var(--font-mono)',
                            fontWeight: isAssigned ? 600 : 400,
                            cursor: (!isAssigned && tags.length >= 10) ? 'not-allowed' : 'pointer',
                            boxShadow: isAssigned ? `0 0 10px ${activeColor}40` : 'none',
                            opacity: (!isAssigned && tags.length >= 10) ? 0.45 : 1,
                            transition: 'all 120ms',
                          }}
                          onMouseEnter={e => {
                            if (!isAssigned && tags.length < 10) {
                              e.currentTarget.style.background = `${stag.color}24`
                              e.currentTarget.style.borderColor = `${stag.color}99`
                            }
                          }}
                          onMouseLeave={e => {
                            if (!isAssigned) {
                              e.currentTarget.style.background = `${stag.color}0d`
                              e.currentTarget.style.borderColor = `${stag.color}44`
                            }
                          }}
                        >
                          {isAssigned ? (
                            <Check size={11} strokeWidth={3} style={{ color: activeColor }} />
                          ) : (
                            <span style={{ width: 6, height: 6, borderRadius: '50%', background: stag.color }} />
                          )}
                          {stag.name}
                        </button>
                      )
                    })}
                  </div>
                </div>
              ))}
            </div>
          </div>

          {/* 3. Custom Tag Input & Color Selector */}
          {tags.length < 10 ? (
            <div style={{
              padding: '12px 14px',
              background: 'rgba(0,180,216,0.03)',
              borderRadius: 8,
              border: '1px solid rgba(0,180,216,0.15)',
              position: 'relative',
            }}>
              <div style={{
                fontSize: 10, fontFamily: 'var(--font-mono)', color: 'var(--text-muted)',
                textTransform: 'uppercase', letterSpacing: 1.5, marginBottom: 8,
              }}>
                {t('ui6PagesPeople.add_custom')}
              </div>

              {/* Input field */}
              <div style={{
                display: 'flex', gap: 8, alignItems: 'center',
                background: '#060a12',
                border: '1px solid rgba(0,180,216,0.3)',
                borderRadius: 6, padding: '7px 10px',
                boxShadow: 'inset 0 1px 3px rgba(0,0,0,0.5)',
              }}>
                <Tag size={13} style={{ color: 'var(--text-muted)', flexShrink: 0 }} />
                <input
                  ref={inputRef}
                  value={input}
                  onChange={e => setInput(e.target.value)}
                  onKeyDown={e => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      if (suggestions.length > 0 && input.trim().toLowerCase() === suggestions[0].name.toLowerCase()) {
                        addExisting(suggestions[0])
                      } else {
                        addNew()
                      }
                    }
                  }}
                  placeholder={t('ui6PagesPeople.custom_placeholder')}
                  style={{
                    flex: 1, background: 'none', border: 'none', outline: 'none',
                    color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 12,
                  }}
                />
                {input.trim() && (
                  <button
                    type="button"
                    onClick={addNew}
                    style={{
                      background: 'rgba(0,180,216,0.2)',
                      border: '1px solid rgba(0,180,216,0.45)',
                      borderRadius: 4, padding: '4px 10px',
                      color: '#00b4d8', fontFamily: 'var(--font-mono)',
                      fontSize: 11, fontWeight: 600, cursor: 'pointer',
                      whiteSpace: 'nowrap', transition: 'background 120ms',
                    }}
                    onMouseEnter={e => e.currentTarget.style.background = 'rgba(0,180,216,0.3)'}
                    onMouseLeave={e => e.currentTarget.style.background = 'rgba(0,180,216,0.2)'}
                  >
                    {isNewTag ? t('ui6PagesPeople.plus_create') : t('ui6PagesPeople.plus_add')}
                  </button>
                )}
              </div>

              {/* Suggestions dropdown */}
              {suggestions.length > 0 && (
                <div style={{
                  position: 'absolute', top: 58, left: 14, right: 14,
                  background: '#0a121e',
                  border: '1px solid rgba(0,180,216,0.3)',
                  borderRadius: 6,
                  zIndex: 20, overflow: 'hidden',
                  boxShadow: '0 8px 24px rgba(0,0,0,0.6)',
                }}>
                  <div style={{ padding: '4px 8px', fontSize: 9, fontFamily: 'var(--font-mono)', color: 'var(--text-muted)', textTransform: 'uppercase', background: 'rgba(255,255,255,0.02)', borderBottom: '1px solid rgba(255,255,255,0.06)' }}>
                    {t('ui6PagesPeople.existing_tags')}
                  </div>
                  {suggestions.map(s => (
                    <div
                      key={s.id}
                      onClick={() => addExisting(s)}
                      style={{
                        display: 'flex', alignItems: 'center', gap: 8,
                        padding: '7px 12px', cursor: 'pointer',
                        transition: 'background 100ms',
                      }}
                      onMouseEnter={e => e.currentTarget.style.background = 'rgba(0,180,216,0.1)'}
                      onMouseLeave={e => e.currentTarget.style.background = 'transparent'}
                    >
                      <div style={{
                        width: 8, height: 8, borderRadius: '50%',
                        background: s.color, boxShadow: `0 0 6px ${s.color}`,
                        flexShrink: 0,
                      }} />
                      <span style={{
                        fontFamily: 'var(--font-mono)', fontSize: 11,
                        color: '#e2e8f0', flex: 1,
                      }}>
                        {s.name}
                      </span>
                      <span style={{ fontSize: 10, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)' }}>
                        {t('ui6PagesPeople.select')}
                      </span>
                    </div>
                  ))}
                </div>
              )}

              {/* Color swatches & live preview */}
              <div style={{ marginTop: 12 }}>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
                  <span style={{ fontSize: 10, fontFamily: 'var(--font-mono)', color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1.5 }}>
                    {t('ui6PagesPeople.tag_color')}
                  </span>
                  <span style={{ fontSize: 10, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', display: 'flex', alignItems: 'center', gap: 6 }}>
                    {t('ui6PagesPeople.preview')}
                    <span style={{
                      padding: '2px 8px', borderRadius: 4,
                      background: `${selColor}20`,
                      border: `1px solid ${selColor}66`,
                      color: selColor,
                      fontSize: 10, fontWeight: 600,
                    }}>
                      {input.trim() || t('ui6PagesPeople.custom_tag')}
                    </span>
                  </span>
                </div>

                <div style={{ display: 'flex', gap: 7, alignItems: 'center', flexWrap: 'wrap' }}>
                  {TAG_COLORS.map(c => (
                    <button
                      key={c}
                      type="button"
                      onClick={() => setSelColor(c)}
                      title={c}
                      style={{
                        width: 22, height: 22, borderRadius: '50%',
                        background: c,
                        border: selColor === c ? '2px solid #fff' : '2px solid transparent',
                        boxShadow: selColor === c ? `0 0 0 2px ${c}, 0 0 10px ${c}` : `0 0 4px ${c}40`,
                        cursor: 'pointer',
                        padding: 0,
                        transform: selColor === c ? 'scale(1.15)' : 'scale(1)',
                        transition: 'all 120ms',
                      }}
                    />
                  ))}
                </div>
              </div>
            </div>
          ) : (
            <div style={{ fontSize: 11, color: '#f59e0b', fontFamily: 'var(--font-mono)', padding: '8px 12px', background: 'rgba(245,158,11,0.08)', borderRadius: 6, border: '1px solid rgba(245,158,11,0.2)' }}>
              {t('ui6PagesPeople.limit')}
            </div>
          )}
        </div>

        {/* Footer */}
        <div style={{
          padding: '12px 20px',
          borderTop: '1px solid rgba(0,180,216,0.12)',
          display: 'flex', alignItems: 'center', justifyContent: 'space-between',
          background: 'rgba(0,180,216,0.03)',
          flexShrink: 0,
        }}>
          <span style={{ fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)' }}>
            {t('ui6PagesPeople.esc_close')}
          </span>
          <button
            type="button"
            onClick={onClose}
            style={{
              padding: '6px 18px', borderRadius: 6,
              background: 'rgba(0,180,216,0.18)',
              border: '1px solid rgba(0,180,216,0.4)',
              color: '#00b4d8', fontSize: 12, fontWeight: 600,
              cursor: 'pointer', transition: 'all 120ms',
            }}
            onMouseEnter={e => e.currentTarget.style.background = 'rgba(0,180,216,0.3)'}
            onMouseLeave={e => e.currentTarget.style.background = 'rgba(0,180,216,0.18)'}
          >
            {t('ui6PagesPeople.done')}
          </button>
        </div>
      </div>
    </div>,
    document.body
  )
}

// ── Tags cell in the table ────────────────────────────────────
function TagsCell({ personId, username, fullName, initialTags }) {
  useTranslation()
  const [open, setOpen]       = useState(false)
  const [tags, setTags]       = useState(initialTags || [])

  // Keep local tags state in sync if initialTags prop changes
  useEffect(() => {
    if (initialTags) setTags(initialTags)
  }, [initialTags])

  // Reload tags when editor closes so the table row refreshes
  const handleClose = async () => {
    setOpen(false)
    const fresh = await api.getPersonTags(personId)
    if (fresh) setTags(fresh)
  }

  const visibleTags = tags.slice(0, 3)
  const extra       = tags.length - visibleTags.length

  return (
    <div style={{ display: 'inline-flex', alignItems: 'center', gap: 5, flexWrap: 'nowrap' }}>
      {visibleTags.map(tag => (
        <span
          key={tag.id}
          onClick={(e) => { e.stopPropagation(); setOpen(true) }}
          style={{ cursor: 'pointer' }}
          title={t('ui6PagesPeople.click_manage')}
        >
          <TagChip tag={tag} />
        </span>
      ))}
      {extra > 0 && (
        <button
          type="button"
          onClick={(e) => { e.stopPropagation(); setOpen(true) }}
          title={t('ui6PagesPeople.more_tags', { count: extra })}
          style={{
            background: 'none', border: 'none', padding: 0,
            fontSize: 10, fontFamily: 'var(--font-mono)',
            color: 'var(--text-muted)', whiteSpace: 'nowrap',
            cursor: 'pointer', textDecoration: 'underline',
          }}
        >
          +{extra}
        </button>
      )}
      <button
        type="button"
        onClick={(e) => { e.stopPropagation(); setOpen(o => !o) }}
        title={t('ui6PagesPeople.add_manage')}
        style={{
          width: 20, height: 20, borderRadius: 4,
          background: open ? 'rgba(0,180,216,0.2)' : 'rgba(0,180,216,0.08)',
          border: '1px solid rgba(0,180,216,0.25)',
          color: open ? '#00b4d8' : 'var(--text-secondary)',
          cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center',
          flexShrink: 0, transition: 'all 120ms', padding: 0,
        }}
        onMouseEnter={e => {
          e.currentTarget.style.background = 'rgba(0,180,216,0.2)'
          e.currentTarget.style.color = '#00b4d8'
        }}
        onMouseLeave={e => {
          if (!open) {
            e.currentTarget.style.background = 'rgba(0,180,216,0.08)'
            e.currentTarget.style.color = 'var(--text-secondary)'
          }
        }}
      >
        <Plus size={11} />
      </button>

      {open && (
        <TagEditor
          personId={personId}
          username={username}
          fullName={fullName}
          onClose={handleClose}
        />
      )}
    </div>
  )
}

// ── Main People page ──────────────────────────────────────────
export default function People({ onProfile, isActive = true, onNavigate }) {
  useTranslation()
  const [comms, setComms] = useState({})
  const [people, setPeople]   = useState([])
  const [tagsMap, setTagsMap] = useState({})
  const [count, setCount]     = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError]     = useState(null)
  const [platform, setPlatform] = useState('')
  const [search, setSearch]   = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const [offset, setOffset]   = useState(0)
  const [selected, setSelected] = useState(() => new Set())
  const LIMIT = 50

  const debounceRef = useRef(null)

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      setDebouncedSearch(search)
      setOffset(0)
    }, 300)
    return () => clearTimeout(debounceRef.current)
  }, [search])

  const loadSeq = useRef(0)
  const load = useCallback(async (opts) => {
    const seq = ++loadSeq.current // only the newest request may write
    if (opts?.silent !== true) setLoading(true)
    try {
      setError(null)
      const [data, total] = await Promise.all([
        api.getPeople(platform, debouncedSearch, LIMIT, offset),
        api.getPeopleCount(platform, debouncedSearch),
      ])
      if (seq !== loadSeq.current) return
      const rows = data || []
      setPeople(rows)
      setCount(total || 0)
      Promise.resolve().then(() => api.getAllPersonMessages(500)).then(m => setComms(commsByPerson(m))).catch(() => {})

      // Bulk-load tags for all visible people
      if (rows.length > 0) {
        const ids = rows.map(p => p.id)
        const tm  = await api.getPeopleTagsMap(ids)
        setTagsMap(tm || {})
      } else {
        setTagsMap({})
      }
    } catch (e) {
      // Signed out: the account gate (watching every binding) takes over.
      if (seq === loadSeq.current && !loginRequiredInError(e)) setError(e?.message || t('ui6PagesPeople.load_failed'))
    } finally {
      if (seq === loadSeq.current) setLoading(false)
    }
  }, [platform, debouncedSearch, offset])

  useEffect(() => { load() }, [load])
  useReloadOnActivate(isActive, () => load({ silent: true }))

  // Reload whenever a workflow finishes (may have saved new people).
  useEffect(() => {
    const off = subscribeEvent('workflow:complete', load)
    return off
  }, [load])

  useEffect(() => onCommsChanged(() => load({ silent: true })), [load])

  // A selection only makes sense for the page it was made on.
  useEffect(() => { setSelected(new Set()) }, [platform, debouncedSearch, offset])

  const toggle = (id) => setSelected(prev => {
    const next = new Set(prev)
    next.has(id) ? next.delete(id) : next.add(id)
    return next
  })

  const deleteSelected = async () => {
    const ids = [...selected]
    if (!(await confirm(t('ui6PagesPeople.confirm_delete', { count: ids.length }), { title: t('ui6PagesPeople.delete_title'), confirmLabel: t('ui6PagesPeople.delete') }))) return
    try {
      await api.deletePeople(ids)
      setSelected(new Set())
    } catch (e) {
      setError(e?.message || t('ui6PagesPeople.delete_failed'))
    }
    load({ silent: true })
  }

  const handlePlatformChange = (p) => {
    setPlatform(p)
    setOffset(0)
    setSearch('')
    setDebouncedSearch('')
  }

  // Filter options derive from the platforms actually present in the data;
  // keep the active filter selectable even if its page of rows is all one platform.
  const platformOptions = [...new Set(people.map(p => p.platform?.toUpperCase()).filter(Boolean))]
  if (platform && !platformOptions.includes(platform)) platformOptions.push(platform)
  platformOptions.sort()
  const showFollowers = people.some(p => p.follower_count)

  return (
    <>
      <div className="page-header">
        <div className="page-header-left">
          <div className="page-title">{t('ui6PagesPeople.title')}</div>
          <div className="page-subtitle">{t('ui6PagesPeople.subtitle')}</div>
        </div>
        <div className="page-header-right">
          {selected.size > 0 && (
            <button className="btn btn-ghost btn-sm" onClick={deleteSelected} style={{ gap: 5, color: 'var(--red)' }}>
              <Trash2 size={12} /> {t('ui6PagesPeople.delete_n', { n: selected.size })}
            </button>
          )}
          <button className="btn btn-ghost btn-sm" onClick={() => load()} style={{ gap: 5 }}>
            <RefreshCw size={12} /> {t('ui6PagesPeople.refresh')}
          </button>
        </div>
      </div>

      <div className="page-body">
        {error && <div style={{ padding: '12px 16px', background: 'rgba(239,68,68,.08)', border: '1px solid rgba(239,68,68,.2)', borderRadius: 'var(--radius)', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--red)', marginBottom: 12, display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }} role="alert">
          <span>{error}</span>
          <button className="btn btn-ghost btn-sm" onClick={() => load()}>{t('ui6PagesPeople.retry')}</button>
        </div>}
        <div className="filters-bar">
          <div style={{ position: 'relative', display: 'flex', alignItems: 'center' }}>
            <Search size={13} style={{ position: 'absolute', left: 10, color: 'var(--text-muted)', pointerEvents: 'none' }} />
            <input
              className="search-input"
              style={{ paddingLeft: 30 }}
              placeholder={t('ui6PagesPeople.search_placeholder')}
              value={search}
              onChange={e => setSearch(e.target.value)}
            />
          </div>
          <select className="filter-select" value={platform} onChange={e => handlePlatformChange(e.target.value)}>
            <option value="">{t('ui6PagesPeople.all_platforms')}</option>
            {platformOptions.map(p => <option key={p} value={p}>{p}</option>)}
          </select>
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', marginLeft: 'auto' }}>
            {t('ui6PagesPeople.profile_count', { count, n: count.toLocaleString() })}
          </span>
        </div>

        {loading ? (
          <div className="empty-state"><div className="spinner" /></div>
        ) : people.length === 0 ? (
          <div className="empty-state">
            <div className="empty-state-icon"><Users size={40} /></div>
            <div className="empty-state-title">{t('ui6PagesPeople.none_found')}</div>
            <div className="empty-state-desc">
              {debouncedSearch || platform
                ? t('ui6PagesPeople.adjust_filters')
                : t('ui6PagesPeople.import_hint')}
            </div>
          </div>
        ) : (
          <>
            <div style={{ overflowX: 'auto' }}>
              <table className="data-table">
                <thead>
                  <tr>
                    <th style={{ width: 28 }}>
                      <input
                        type="checkbox"
                        aria-label={t('ui6PagesPeople.select_all')}
                        checked={people.length > 0 && selected.size === people.length}
                        onChange={e => setSelected(e.target.checked ? new Set(people.map(p => p.id)) : new Set())}
                      />
                    </th>
                    <th>{t('ui6PagesPeople.col_profile')}</th>
                    <th>{t('ui6PagesPeople.col_platform')}</th>
                    <th>{t('ui6PagesPeople.col_full_name')}</th>
                    <th>{t('ui6PagesPeople.col_tags')}</th>
                    {showFollowers && <th>{t('ui6PagesPeople.col_followers')}</th>}
                    <th>{t('ui6PagesPeople.col_job')}</th>
                    <th>{t('ui6PagesPeople.col_verified')}</th>
                    <th>{t('ui6PagesPeople.col_added')}</th>
                  </tr>
                </thead>
                <tbody>
                  {people.map(p => {
                    const profileUrl = p.profile_url || PLATFORM_PROFILE_URL[p.platform?.toUpperCase()]?.(p.username)
                    return (
                      <tr key={p.id}>
                        <td>
                          <input
                            type="checkbox"
                            aria-label={t('ui6PagesPeople.select_user', { username: p.username })}
                            checked={selected.has(p.id)}
                            onChange={() => toggle(p.id)}
                          />
                        </td>
                        <td>
                          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                            <Avatar username={p.username} imageUrl={p.image_url} />
                            <button
                              className="mono profile-link"
                              onClick={() => onProfile?.(p.id)}
                              aria-label={t('ui6PagesPeople.view_profile', { username: p.username })}
                            >
                              @{p.username}
                            </button>
                            {isCompany(p) && <span data-testid="badge-company" style={kindBadge('#a78bfa')}>{t('comms.company')}</span>}
                            {isOwnAccount(p) && <span data-testid="badge-own" style={kindBadge('#22c55e')}>{t('comms.ownAccount')}</span>}
                            {comms[p.id]?.total > 0 && onNavigate && (
                              <button
                                type="button"
                                data-testid="people-comms-link"
                                onClick={() => onNavigate('communications', { person: p.id })}
                                style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', font: 'inherit', fontSize: 10, color: 'var(--cyan)', textDecoration: 'underline', whiteSpace: 'nowrap' }}
                              >
                                {t('comms.commsCount', { count: comms[p.id].total })}
                                {comms[p.id].pending > 0 && ` \u00B7 ${t('comms.commsPending', { count: comms[p.id].pending })}`}
                              </button>
                            )}
                            {profileUrl && (
                              <button
                                className="btn btn-ghost btn-icon"
                                onClick={() => api.openURL(profileUrl)}
                                title={t('ui6PagesPeople.open_on', { platform: p.platform })}
                                style={{ padding: 3, minWidth: 'unset', minHeight: 'unset', opacity: 0.4 }}
                              >
                                <ExternalLink size={11} />
                              </button>
                            )}
                          </div>
                        </td>
                        <td><PlatformBadge platform={p.platform} /></td>
                        <td style={{ color: 'var(--text)' }}>{p.full_name || '—'}</td>

                        {/* Tags column */}
                        <td style={{ minWidth: 140 }}>
                          <TagsCell
                            personId={p.id}
                            username={p.username}
                            fullName={p.full_name}
                            initialTags={tagsMap[p.id] || []}
                          />
                        </td>

                        {showFollowers && <td className="mono">{p.follower_count || '—'}</td>}
                        <td style={{ maxWidth: 160 }} className="truncate">{p.job_title || p.category || '—'}</td>
                        <td>
                          {p.is_verified && (
                            <CheckCircle size={13} style={{ color: 'var(--cyan)' }} />
                          )}
                        </td>
                        <td className="mono text-xs" style={{ color: 'var(--text-muted)' }}>
                          {p.created_at ? p.created_at.slice(0, 10) : '—'}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>

            {/* Pagination */}
            {count > LIMIT && (
              <div style={{ display: 'flex', justifyContent: 'center', gap: 8, marginTop: 16 }}>
                <button
                  className="btn btn-secondary btn-sm"
                  onClick={() => setOffset(Math.max(0, offset - LIMIT))}
                  disabled={offset === 0}
                >
                  {t('ui6PagesPeople.prev')}
                </button>
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', alignSelf: 'center' }}>
                  {t('ui6PagesPeople.range', { from: offset + 1, to: Math.min(offset + LIMIT, count), total: count })}
                </span>
                <button
                  className="btn btn-secondary btn-sm"
                  onClick={() => setOffset(offset + LIMIT)}
                  disabled={offset + LIMIT >= count}
                >
                  {t('ui6PagesPeople.next')}
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </>
  )
}
