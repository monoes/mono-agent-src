import { useTranslation } from 'react-i18next'
import { AlertTriangle } from 'lucide-react'
import { budgetLevel } from '../../lib/taskSessionModel.js'

// Near budget (90 percent) and Budget reached say so in words and with an
// icon beside the orange spend, so the colour is never the only signal.
export default function BudgetFlag({ session }) {
  const { t } = useTranslation()
  const level = budgetLevel(session)
  if (!level) return null
  const text = t(`tasks.session.budget_${level}`)
  return (
    <span className="ts-budget-flag" data-testid="ts-budget-flag" data-level={level} role="img" aria-label={text} title={text}>
      <AlertTriangle size={11} aria-hidden="true" /> {text}
    </span>
  )
}
