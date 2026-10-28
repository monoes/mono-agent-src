// The chime that says a person item is waiting: two soft tones made with WebAudio (no
// asset), and the operator's `notifySound` setting (on by default; the Mute buttons in the
// card and the inbox flip the same setting). Playing never throws: a webview that blocks audio
// until a click, or has none, gets silence and the visual signals still show.

const KEY = 'monoagent:notifySound'
const listeners = new Set()

const memory = { off: false } // used when localStorage is unavailable

export function getNotifySound() {
  try { return localStorage.getItem(KEY) !== 'off' } catch { return !memory.off }
}

export function setNotifySound(on) {
  memory.off = !on
  try { localStorage.setItem(KEY, on ? 'on' : 'off') } catch { /* the choice then lasts until reload */ }
  listeners.forEach(fn => { try { fn(on) } catch { /* a listener must not stop the others */ } })
}

export function subscribeNotifySound(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

// The two tones: a rising fourth, short and quiet.
export const TONES = [
  { freq: 659.25, at: 0, len: 0.16 },
  { freq: 880, at: 0.17, len: 0.26 },
]
const PEAK = 0.12

let shared = null
function defaultContext() {
  if (shared) return shared
  const Ctor = globalThis.AudioContext || globalThis.webkitAudioContext
  if (!Ctor) return null
  shared = new Ctor()
  return shared
}

// playChime plays the two tones and returns true when it got as far as scheduling them.
// make() returns an AudioContext (or null); it is a parameter so tests can fake one.
export function playChime(make = defaultContext) {
  try {
    const ctx = make()
    if (!ctx) return false
    if (ctx.state === 'suspended') {
      // A blocked context stays suspended until a gesture: resume() is a request, not a promise to sound.
      Promise.resolve(ctx.resume?.()).catch(() => {})
    }
    const t0 = ctx.currentTime
    for (const tone of TONES) {
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.type = 'sine'
      osc.frequency.value = tone.freq
      gain.gain.setValueAtTime(0.0001, t0 + tone.at)
      gain.gain.exponentialRampToValueAtTime(PEAK, t0 + tone.at + 0.02)
      gain.gain.exponentialRampToValueAtTime(0.0001, t0 + tone.at + tone.len)
      osc.connect(gain)
      gain.connect(ctx.destination)
      osc.start(t0 + tone.at)
      osc.stop(t0 + tone.at + tone.len + 0.02)
    }
    return true
  } catch {
    return false
  }
}
