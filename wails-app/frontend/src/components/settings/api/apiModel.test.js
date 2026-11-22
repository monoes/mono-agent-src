import { describe, it, expect } from 'vitest'
import { pickListener, servingListeners, listenerState, baseURL, modelsArgs, missingIsAboutJev, policyClass, relativeTime, formatDate } from './apiModel.js'
import { mainListener as listener, dedicatedListener as dedicated, statusOf as status, withoutScheme } from './__fixtures__/apiFixtures.js'

const down = { reachable: false, v1_answers: false }
const stale = { v1_answers: false }

describe('servingListeners', () => {
  it('is every listener that is meant to serve /v1, in the order the status lists them', () => {
    const main = listener(); const v1 = dedicated()
    expect(servingListeners(status([main, v1]))).toEqual([main, v1])
    expect(servingListeners(status([listener({ v1: false }), v1]))).toEqual([v1])
    expect(servingListeners(status([listener({ addr: '0.0.0.0:9322', loopback: false, v1: false })]))).toEqual([])
  })
  it('is empty with no listener, no status or a status without listeners', () => {
    expect(servingListeners(status([]))).toEqual([])
    expect(servingListeners({})).toEqual([])
    expect(servingListeners(null)).toEqual([])
    expect(servingListeners({ listeners: null })).toEqual([])
  })
})

describe('pickListener', () => {
  it('prefers a listener that answers /v1; of those that do, the first one listed (the main one)', () => {
    const main = listener(); const v1 = dedicated()
    expect(pickListener(status([main, v1]))).toBe(main)
    expect(pickListener(status([main]))).toBe(main)
    expect(pickListener(status([v1]))).toBe(v1)
  })
  it('does not let a failing listener hide one that serves: the main one down, the dedicated one answering', () => {
    const v1 = dedicated()
    expect(pickListener(status([listener(down), v1]))).toBe(v1)
  })
  it('prefers one that is reachable to one that is not, and /v1 answered to /health only', () => {
    const v1 = dedicated()
    expect(pickListener(status([listener(stale), v1]))).toBe(v1) // /health only, against serving
    const reachableOnly = dedicated(stale)
    const mainDown = listener(down)
    expect(pickListener(status([mainDown, reachableOnly]))).toBe(reachableOnly) // nothing, against /health only
  })
  it('takes the first listed when none answers', () => {
    const main = listener(down)
    expect(pickListener(status([main, dedicated(down)]))).toBe(main)
    expect(pickListener(status([dedicated(down), main])).name).toBe('v1') // by the order listed, not by name
  })
  it('takes the dedicated listener when the main one is off loopback or the daemon does not mount /v1 on it', () => {
    const v1 = dedicated()
    expect(pickListener(status([listener({ addr: '0.0.0.0:9322', loopback: false, v1: false }), v1]))).toBe(v1)
    expect(pickListener(status([listener({ v1: false }), v1]))).toBe(v1)
  })
  it('falls back to the main listener, to explain why nothing serves /v1', () => {
    const main = listener({ addr: '0.0.0.0:9322', loopback: false, v1: false })
    expect(pickListener(status([main]))).toBe(main)
    const other = dedicated({ v1: false })
    expect(pickListener(status([other, main]))).toBe(main) // wherever it is listed
    expect(pickListener(status([main, other]))).toBe(main)
    expect(pickListener(status([other]))).toBe(other) // and with no main one, the first listed
  })
  it('is null with no listener, no status or a status without listeners', () => {
    expect(pickListener(status([]))).toBeNull()
    expect(pickListener({})).toBeNull()
    expect(pickListener(null)).toBeNull()
    expect(pickListener(undefined)).toBeNull()
    expect(pickListener({ listeners: null })).toBeNull()
  })
})

describe('listenerState (listenerNote of api_status.go, case by case)', () => {
  it('is none without a listener', () => expect(listenerState(null)).toBe('none'))
  it('is down when /health does not answer, whatever else is true', () => {
    expect(listenerState(listener(down))).toBe('down')
    expect(listenerState(listener({ ...down, v1: false }))).toBe('down')
  })
  it('says the daemon does not serve /v1 on a reachable loopback listener that is not meant to', () => {
    expect(listenerState(listener({ v1: false, v1_answers: false }))).toBe('no-v1-daemon')
  })
  it('says an off-loopback main listener does not serve /v1', () => {
    expect(listenerState(listener({ addr: '0.0.0.0:9322', loopback: false, v1: false, v1_answers: false }))).toBe('no-v1-offloopback')
  })
  it('is serving when /v1 answers', () => expect(listenerState(listener())).toBe('serving'))
  it('is stale when /health answers but /v1 does not (a server that predates the API)', () => {
    expect(listenerState(listener(stale))).toBe('stale')
  })
  it('is down-daemon when nothing answers although the daemon is running: starting it is not the advice', () => {
    expect(listenerState(listener(down), true)).toBe('down-daemon')
    expect(listenerState(listener({ ...down, v1: false }), true)).toBe('down-daemon')
    expect(listenerState(listener(down), false)).toBe('down')
    expect(listenerState(listener(down))).toBe('down')
  })
  it('is what it is when the daemon runs and something answers, or nothing is listed', () => {
    expect(listenerState(listener(), true)).toBe('serving')
    expect(listenerState(listener(stale), true)).toBe('stale')
    expect(listenerState(listener({ v1: false }), true)).toBe('no-v1-daemon')
    expect(listenerState(null, true)).toBe('none')
  })
})

describe('baseURL: the scheme', () => {
  it('is the one the CLI says answered', () => {
    expect(baseURL(listener())).toEqual({ url: 'http://127.0.0.1:9322/v1', tls: false, wildcard: false })
    expect(baseURL(dedicated({ addr: 'api.example.com:9443' }))).toEqual({ url: 'https://api.example.com:9443/v1', tls: true, wildcard: false })
    // A dedicated loopback listener the server runs with a certificate speaks TLS: the CLI knows, the address does not say.
    expect(baseURL(dedicated({ loopback: true, addr: '127.0.0.1:9443', scheme: 'https' }))).toEqual({ url: 'https://127.0.0.1:9443/v1', tls: true, wildcard: false })
  })
  it('is the CLI\'s word even where the address would suggest otherwise', () => {
    expect(baseURL(dedicated({ addr: 'api.example.com:9443', scheme: 'http' })).url).toBe('http://api.example.com:9443/v1')
  })
  it('is derived only when the CLI sent none (an older CLI, or a listener that does not answer)', () => {
    expect(baseURL(withoutScheme(listener())).url).toBe('http://127.0.0.1:9322/v1')
    expect(baseURL(withoutScheme(dedicated({ addr: 'api.example.com:9443' }))).url).toBe('https://api.example.com:9443/v1')
    expect(baseURL(withoutScheme(dedicated({ loopback: true, addr: '127.0.0.1:9443' }))).url).toBe('http://127.0.0.1:9443/v1')
    expect(baseURL(listener({ ...down })).url).toBe('http://127.0.0.1:9322/v1') // not answering: where it will listen
  })
  it('is derived too when what the CLI sent is not http or https', () => {
    for (const scheme of ['ftp', 'javascript', 'HTTPS ', 'file', '']) {
      expect(baseURL(dedicated({ addr: 'api.example.com:9443', scheme })).url).toBe('https://api.example.com:9443/v1')
    }
  })
})

describe('baseURL: the address', () => {
  it('shows a wildcard host as localhost and says so', () => {
    for (const addr of ['0.0.0.0:9443', ':9443', '[::]:9443']) {
      expect(baseURL(dedicated({ addr }))).toEqual({ url: 'https://localhost:9443/v1', tls: true, wildcard: true })
    }
  })
  it('keeps the brackets of an IPv6 host', () => {
    expect(baseURL(listener({ addr: '[::1]:9322' })).url).toBe('http://[::1]:9322/v1')
    expect(baseURL(listener({ addr: '[2001:db8::1]:443' })).url).toBe('http://[2001:db8::1]:443/v1')
    expect(baseURL(listener({ addr: '[::ffff:127.0.0.1]:9322' })).url).toBe('http://[::ffff:127.0.0.1]:9322/v1')
    expect(baseURL(listener({ addr: '[1:2:3:4:5:6:7:8]:80' })).url).toBe('http://[1:2:3:4:5:6:7:8]:80/v1')
    expect(baseURL(listener({ addr: '[fe80::1]:80' })).url).toBe('http://[fe80::1]:80/v1')
  })
  it('accepts a host name, an IPv4 address and a port from 1 to 65535', () => {
    for (const [addr, url] of [
      ['localhost:9322', 'http://localhost:9322/v1'], ['my-host.example.com:80', 'http://my-host.example.com:80/v1'],
      ['10.0.0.5:1', 'http://10.0.0.5:1/v1'], ['127.0.0.1:65535', 'http://127.0.0.1:65535/v1'], ['127.0.0.1:09322', 'http://127.0.0.1:9322/v1'],
    ]) expect(baseURL(listener({ addr })).url).toBe(url)
  })
  it('holds a host name to the lengths of DNS: 63 characters a label, 253 in all', () => {
    const label = (n) => 'a'.repeat(n)
    expect(baseURL(listener({ addr: `${label(63)}.example:80` })).url).toBe(`http://${label(63)}.example:80/v1`)
    expect(baseURL(listener({ addr: `${label(64)}.example:80` }))).toBeNull()
    const four = (n) => [label(n), label(n), label(n), label(n)].join('.') // 3 dots more than 4n
    expect(baseURL(listener({ addr: `${four(62)}:80` })).url).toBe(`http://${four(62)}:80/v1`) // 251
    expect(baseURL(listener({ addr: `${four(63)}:80` }))).toBeNull() // 255
  })
  it('has no URL for an address that is not a host name or an IP literal and a port: what is copied is never another host', () => {
    for (const addr of [
      '127.0.0.1@evil.example:80', 'evil.example/x:80', 'host?q=1:80', 'host#frag:80', 'ho st:80', 'host:80/path', 'user:pw@host:80',
      '-bad.example:80', 'bad-.example:80', 'a..b:80', 'example.com.:80', 'ünï.example:80', 'host\n:80', 'host:80\n', ' host:80',
      '256.0.0.1:80', '999.1.1.1:80', '01.02.03.04:80', '1.2.3:80', '1.2.3.4.5:80', '12345:80',
      '[::1]x:80', '[not-ip]:80', '[]:80', '[::1:80', '[::1%eth0]:80', '[:::::::::::::::::::::::::::::::::::::::::::::::::]:80',
      '[1:2:3:4:5:6:7:8:9]:80', '[1:2:3:4:5:6:7]:80', '[1::2::3]:80', '[1:2:3:4:5:6:7:8::2::3]:80', '[12345::1]:80', '[::ffff:999.1.1.1]:80', '[1:2:3:4:5:6:7:8::]:80',
      'host:0', 'host:65536', 'host:99999', 'host:123456', 'host:', 'host:80x', 'host:-1', 'host', 'nonsense', '', '   ',
    ]) {
      expect(baseURL(listener({ addr })), JSON.stringify(addr)).toBeNull()
    }
    expect(baseURL(listener({ addr: undefined }))).toBeNull()
  })
  it('has no URL for a listener that is not meant to serve /v1', () => {
    expect(baseURL(listener({ v1: false }))).toBeNull()
    expect(baseURL(null)).toBeNull()
  })
})

describe('modelsArgs', () => {
  it('asks the CLI for its own defaults when no listener serves /v1', () => {
    expect(modelsArgs(null)).toEqual(['', '', '', ''])
    expect(modelsArgs(listener({ v1: false }))).toEqual(['', '', '', ''])
  })
  it('evaluates the listener the header describes, with the policy it reports', () => {
    expect(modelsArgs(listener())).toEqual(['loopback', 'any', 'chat-only', 'chat-only'])
    expect(modelsArgs(dedicated({ context_confinement: 'sandboxed', auto_confinement: 'sandboxed' }))).toEqual(['network', 'chat-only', 'sandboxed', 'sandboxed'])
  })
  it('drops what is missing (a CLI that predates --auto-confinement) or is not a class', () => {
    const l = listener({ confinement: 'none', context_confinement: '' })
    delete l.auto_confinement
    expect(modelsArgs(l)).toEqual(['loopback', '', '', ''])
  })
})

describe('policyClass', () => {
  it('spells a class as the policy that allows it: auto.confinement says unconfined where the listeners say any', () => {
    expect(policyClass('unconfined')).toBe('any')
    expect(policyClass('any')).toBe('any')
    expect(policyClass('sandboxed')).toBe('sandboxed')
    expect(policyClass('chat-only')).toBe('chat-only')
    expect(policyClass(undefined)).toBeUndefined()
  })
})

describe('missingIsAboutJev', () => {
  it('is true for what the Jev section fixes', () => {
    expect(missingIsAboutJev('the api_auto surface switched on for the profile (monoagentcli jev enable api_auto)')).toBe(true)
    expect(missingIsAboutJev('a Jev key for the profile (monoagentcli jev key set, or TYPESAFE_API_KEY)')).toBe(true)
    // With a decision provider other than TypeSafe on the api_auto surface.
    expect(missingIsAboutJev('the key of "router", the Jev decision provider of the api_auto surface ' +
      '(jev: provider "router" has no key: store it in the machine vault entry "decision-router")')).toBe(true)
    expect(missingIsAboutJev('a usable Jev decision provider for the api_auto surface: "gone" cannot be used ' +
      '(jev: unknown decision provider "gone")')).toBe(true)
  })
  it('is false for what only the server can fix, and for nothing', () => {
    expect(missingIsAboutJev("at least one model the listener's policy allows")).toBe(false)
    expect(missingIsAboutJev('a model within --auto-confinement (chat-only), which holds back all 3 the listener serves')).toBe(false)
    expect(missingIsAboutJev('')).toBe(false)
    expect(missingIsAboutJev(undefined)).toBe(false)
  })
})

describe('relativeTime', () => {
  const now = Date.parse('2026-10-02T12:00:00Z')
  const ago = (s) => new Date(now - s * 1000).toISOString()
  it('says now for a few seconds, then minutes, hours, days', () => {
    expect(relativeTime(ago(10), 'en', now)).toBe('now')
    expect(relativeTime(ago(3 * 60), 'en', now)).toBe('3 minutes ago')
    expect(relativeTime(ago(2 * 3600), 'en', now)).toBe('2 hours ago')
    expect(relativeTime(ago(24 * 3600), 'en', now)).toBe('yesterday')
    expect(relativeTime(ago(3 * 24 * 3600), 'en', now)).toBe('3 days ago')
    expect(relativeTime(ago(90 * 24 * 3600), 'en', now)).toBe('3 months ago')
  })
  it('speaks the chosen language', () => {
    expect(relativeTime(ago(3 * 60), 'es', now)).toBe('hace 3 minutos')
  })
  it('falls back to English for a language it does not know, and is empty for no date', () => {
    expect(relativeTime(ago(3 * 60), 'not a language!', now)).toBe('3 minutes ago')
    expect(relativeTime('', 'en', now)).toBe('')
    expect(relativeTime(null, 'en', now)).toBe('')
    expect(relativeTime('yesterday-ish', 'en', now)).toBe('')
  })
})

describe('formatDate', () => {
  it('gives a short date in the chosen language', () => {
    expect(formatDate('2026-10-01T12:00:00Z', 'en')).toMatch(/Oct 1, 2026/)
    expect(formatDate('2026-10-01T12:00:00Z', 'es')).toMatch(/1 oct\.? 2026/)
  })
  it('is empty for no or a bad date', () => {
    expect(formatDate('', 'en')).toBe('')
    expect(formatDate(undefined, 'en')).toBe('')
    expect(formatDate('not a date', 'en')).toBe('')
  })
})
