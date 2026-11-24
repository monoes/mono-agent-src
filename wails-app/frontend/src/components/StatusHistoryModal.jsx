import { useEffect, useState, useRef } from 'react'
import { X } from 'lucide-react'
import { api } from '../services/api.js'
import { useEscapeClose } from '../lib/escapeStack.js'

// Read-only chronological log of every status update ever posted for a
// person — no edit/delete affordance, matching the append-only data model.
export default function StatusHistoryModal({ personId, onClose }) {
  const overlayRef = useRef(null)
  const [entries, setEntries] = useState([])
  const [loading, setLoading] = useState(true)

  useEscapeClose(onClose)

  useEffect(() => {
    api.getPersonStatusHistory(personId).then(data => setEntries(data || [])).finally(() => setLoading(false))
  }, [personId])

  return (
    <div
      ref={overlayRef}
      onClick={(e) => { if (e.target === overlayRef.current) onClose() }}
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,0.75)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
      }}
    >
      <div role="dialog" aria-modal="true" aria-label="Status history" style={{
        background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 12,
        width: 480, maxWidth: '92vw', maxHeight: '80vh',
        display: 'flex', flexDirection: 'column',
        boxShadow: '0 20px 60px rgba(0,0,0,0.6)',
        overflow: 'hidden',
      }}>
        {/* Header */}
        <div style={{
          display: 'flex', justifyContent: 'space-between', alignItems: 'center',
          padding: '14px 18px', borderBottom: '1px solid #1e3a4f',
        }}>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 13, color: 'var(--text)', fontWeight: 600 }}>
            Status History
          </div>
          <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#475569', padding: 2 }}>
            <X size={18} />
          </button>
        </div>

        {/* Body */}
        <div style={{ flex: 1, overflow: 'auto', padding: '12px 18px' }}>
          {loading ? (
            <div style={{ padding: '20px 0', textAlign: 'center' }}>
              <div className="spinner" style={{ width: 16, height: 16, margin: '0 auto' }} />
            </div>
          ) : entries.length === 0 ? (
            <div style={{ padding: '20px 0', textAlign: 'center', color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', fontSize: 12 }}>
              No status updates yet
            </div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {entries.map(e => (
                <div key={e.id} style={{
                  padding: '8px 10px', borderRadius: 6,
                  background: 'var(--elevated)', border: '1px solid var(--border)',
                }}>
                  <div style={{ fontSize: 12, color: 'var(--text)', lineHeight: 1.5 }}>{e.text}</div>
                  <div style={{ marginTop: 4, fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-dim)' }}>
                    {e.created_at.slice(0, 16).replace('T', ' ')}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
