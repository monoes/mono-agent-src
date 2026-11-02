import { useEffect, useState } from 'react'
import { getNotifySound, setNotifySound, subscribeNotifySound } from '../../lib/personChime.js'

// useNotifySound is [on, set] for the operator's notifySound setting; every toggle in the app
// (Settings, the card's mute button, the inbox's) follows the same value.
export function useNotifySound() {
  const [on, setOn] = useState(getNotifySound)
  useEffect(() => subscribeNotifySound(setOn), [])
  return [on, setNotifySound]
}
