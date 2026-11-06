// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
vi.mock('../wailsjs/go/main/App', () => ({ DoomsdayStop: vi.fn().mockResolvedValue('{"ok":true,"stopped":true}') }))
import { controlRoomApi, CONTROL_ROOM_CHANGED } from './controlRoom.js'

describe('Stop', () => {
  it('tells the nav to look again, so it does not keep showing a run that is gone', async () => {
    const seen = vi.fn()
    window.addEventListener(CONTROL_ROOM_CHANGED, seen)
    expect((await controlRoomApi.stop()).stopped).toBe(true)
    expect(seen).toHaveBeenCalledTimes(1)
    window.removeEventListener(CONTROL_ROOM_CHANGED, seen)
  })
})
