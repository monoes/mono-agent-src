import { useState, useEffect } from 'react'
import { ArrowLeft, Heart, MessageCircle, ExternalLink } from 'lucide-react'
import { api } from '../services/api.js'

export default function PostDetail({ id, onBack, onOpenURL }) {
  const [post, setPost]         = useState(null)
  const [comments, setComments] = useState([])
  const [loading, setLoading]   = useState(true)
  const [loadError, setLoadError] = useState('')
  const [attempt, setAttempt]     = useState(0)

  useEffect(() => {
    let cancelled = false // a slow answer for an earlier id must not land
    setLoading(true)
    setPost(null)
    setComments([])
    setLoadError('')
    if (!id) { setLoading(false); return }
    Promise.all([
      api.getPostDetail(id),
      api.getPostComments(id),
    ]).then(([p, c]) => {
      if (cancelled) return
      setPost(p)
      setComments(c || [])
      setLoading(false)
    }).catch(e => {
      if (cancelled) return
      setLoadError(e?.message || String(e))
      setLoading(false)
    })
    return () => { cancelled = true }
  }, [id, attempt])

  if (loading) {
    return (
      <div className="page-body" style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: 200 }}>
        <div className="spinner" />
      </div>
    )
  }

  if (!post) {
    return (
      <div className="page-body">
        <button className="btn btn-ghost btn-sm" onClick={onBack} style={{ gap: 5, marginBottom: 16 }}>
          <ArrowLeft size={13} /> Back
        </button>
        <div className="empty-state" role={loadError ? 'alert' : undefined}>
          {loadError ? `Could not load the post: ${loadError}` : 'Post not found.'}
          {loadError && <button className="btn btn-ghost btn-sm" onClick={() => setAttempt(n => n + 1)} style={{ marginLeft: 8 }}>Retry</button>}
        </div>
      </div>
    )
  }

  return (
    <div className="page-scroll">
      <div className="page-header">
        <div className="page-header-left" style={{ gap: 10 }}>
          <button className="btn btn-ghost btn-sm" onClick={onBack} style={{ gap: 5 }}>
            <ArrowLeft size={13} /> Back
          </button>
          <div className="page-title" style={{ fontFamily: 'var(--font-mono)', fontSize: 16 }}>
            {post.shortcode}
          </div>
        </div>
        <div className="page-header-right">
          <button
            className="btn btn-ghost btn-sm"
            onClick={() => onOpenURL(post.url)}
            style={{ gap: 5 }}
          >
            <ExternalLink size={12} /> Open Post
          </button>
        </div>
      </div>

      <div className="page-body">
        {/* Meta row */}
        <div style={{
          display: 'flex', alignItems: 'center', gap: 12,
          padding: '10px 0 16px',
          borderBottom: '1px solid var(--border)',
          marginBottom: 16,
          flexWrap: 'wrap',
        }}>
          <span style={{
            display: 'flex', alignItems: 'center', gap: 4,
            fontFamily: 'var(--font-mono)', fontSize: 12,
            color: 'var(--text-muted)',
          }}>
            <Heart size={12} /> {post.like_count ?? '—'} likes
          </span>
          <span style={{
            display: 'flex', alignItems: 'center', gap: 4,
            fontFamily: 'var(--font-mono)', fontSize: 12,
            color: 'var(--text-muted)',
          }}>
            <MessageCircle size={12} /> {post.comment_count ?? '—'} comments
          </span>
          {post.scraped_at && (
            <span style={{
              fontFamily: 'var(--font-mono)', fontSize: 10,
              color: 'var(--text-dim)',
              marginLeft: 'auto',
            }}>
              scraped {post.scraped_at.slice(0, 10)}
            </span>
          )}
        </div>

        {/* Caption */}
        {post.caption && (
          <div style={{
            fontFamily: 'var(--font-mono)', fontSize: 12,
            color: 'var(--text-secondary)',
            padding: '0 0 16px',
            borderBottom: '1px solid var(--border)',
            marginBottom: 16,
            lineHeight: 1.6,
          }}>
            {post.caption}
          </div>
        )}

        {/* Comments section */}
        <div className="profile-section-title" style={{ marginBottom: 12 }}>
          Comments
          <span style={{ color: 'var(--text-muted)', fontWeight: 400, marginLeft: 8, fontSize: 11 }}>
            {comments.length}
          </span>
        </div>

        {comments.length === 0 ? (
          <div style={{
            padding: '24px 0', textAlign: 'center',
            color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', fontSize: 12,
          }}>
            No comments scraped yet — run list_post_comments to populate
          </div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
            {comments.map((c, i) => (
              <div key={c.id || i} style={{
                display: 'grid',
                gridTemplateColumns: '130px 1fr auto',
                gap: 10,
                padding: '8px 10px',
                borderRadius: 5,
                background: i % 2 === 0 ? 'var(--elevated)' : 'transparent',
                alignItems: 'start',
              }}>
                {/* Author */}
                <span style={{
                  fontFamily: 'var(--font-mono)', fontSize: 11,
                  color: '#00b4d8', flexShrink: 0,
                  overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                }}>
                  @{c.author}
                </span>

                {/* Text */}
                <span style={{
                  fontSize: 12, color: 'var(--text)',
                  lineHeight: 1.5, wordBreak: 'break-word',
                }}>
                  {c.text || <span style={{ color: 'var(--text-muted)', fontStyle: 'italic' }}>—</span>}
                </span>

                {/* Right side: timestamp + likes */}
                <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 3, flexShrink: 0 }}>
                  {c.timestamp && (
                    <span style={{
                      fontFamily: 'var(--font-mono)', fontSize: 9,
                      color: 'var(--text-dim)', whiteSpace: 'nowrap',
                    }}>
                      {c.timestamp.slice(0, 10)}
                    </span>
                  )}
                  {c.likes_count > 0 && (
                    <span style={{
                      display: 'flex', alignItems: 'center', gap: 2,
                      fontFamily: 'var(--font-mono)', fontSize: 9,
                      color: 'var(--text-muted)',
                    }}>
                      <Heart size={9} /> {c.likes_count}
                    </span>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
