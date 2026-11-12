import React from 'react'
import ReactDOM from 'react-dom/client'
import './i18n.js'
import './index.css'
import MissionStrip from './components/bubbles/mission/MissionStrip.jsx'
import { PlanCard } from './components/bubbles/mission/MissionCards.jsx'
ReactDOM.createRoot(document.getElementById('root')).render(<div style={{ background: 'var(--bg, #111)', padding: 8 }}>
  <PlanCard item={{ payload: { sessionId: 7, round: 1 } }} conversationId="c1" />
  <MissionStrip conversationId="c1" />
</div>)
