// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import '../../i18n.js'
import NotificationsSection from './NotificationsSection.jsx'
import { MuteToggle } from '../bubbles/PersonItemCard.jsx'
import { getNotifySound, setNotifySound } from '../../lib/personChime.js'

afterEach(() => { cleanup(); localStorage.clear(); setNotifySound(true) })

describe('the notifySound setting in Settings', () => {
  it('is on by default and turns the chime off and on', () => {
    render(<NotificationsSection />)
    const box = screen.getByRole('checkbox')
    expect(box).toBeChecked()
    fireEvent.click(box)
    expect(getNotifySound()).toBe(false)
    expect(box).not.toBeChecked()
  })

  it('follows the Mute button of the card, which is the same setting', () => {
    render(<><NotificationsSection /><MuteToggle /></>)
    fireEvent.click(screen.getByRole('button', { name: 'Mute the chime' }))
    expect(screen.getByRole('checkbox')).not.toBeChecked()
  })
})
