import { describe, it, expect } from 'vitest'
import { isNetworkOrDevicePath, classifyUrl, disguisedHost, findPathTokens, codeLooksLikePath, splitLine } from './chatLinks.js'

describe('classifyUrl', () => {
  it('opens http and https only', () => {
    expect(classifyUrl('https://example.com/a?b=1#c')).toMatchObject({ ok: true, host: 'example.com' })
    expect(classifyUrl('http://example.com:8080/')).toMatchObject({ ok: true, host: 'example.com:8080' })
  })
  it.each([
    'javascript:alert(1)', 'JaVaScRiPt:alert(1)', ' javascript:alert(1)', 'data:text/html,<script>1</script>',
    'file:///etc/passwd', 'vbscript:x', 'mailto:a@b.com', 'ftp://example.com', 'myapp://open', 'tel:123',
    '//example.com/x', '/etc/passwd', 'example.com', '', 'https://', 'https:///x',
  ])('refuses %s', (h) => expect(classifyUrl(h).ok).toBe(false))
  it('refuses credentials in the userinfo, which hide the real host', () => {
    expect(classifyUrl('https://bank.com@evil.test/').ok).toBe(false)
    expect(classifyUrl('https://user:pw@example.com/').ok).toBe(false)
    expect(classifyUrl('https://:pw@example.com/').ok).toBe(false)
  })
  it('refuses non-strings', () => {
    expect(classifyUrl(null).ok).toBe(false)
    expect(classifyUrl({}).ok).toBe(false)
  })
})

describe('disguisedHost', () => {
  it('flags link text that names a different host than the target', () => {
    expect(disguisedHost('https://bank.com/login', 'https://evil.test/x')).toBe('bank.com')
    expect(disguisedHost('bank.com', 'https://evil.test/')).toBe('bank.com')
  })
  it('accepts matching hosts, subdomains and ordinary words', () => {
    expect(disguisedHost('https://example.com/a', 'https://example.com/b')).toBe('')
    expect(disguisedHost('example.com', 'https://www.example.com/')).toBe('')
    expect(disguisedHost('the report', 'https://evil.test/')).toBe('')
  })
})

describe('findPathTokens', () => {
  const toks = (s) => findPathTokens(s).map((t) => t.token)
  it('finds absolute, home, relative and windows paths with a line suffix', () => {
    expect(toks('see /home/u/proj/main.go:123 now')).toEqual(['/home/u/proj/main.go:123'])
    expect(toks('edit ~/notes/todo.md.')).toEqual(['~/notes/todo.md'])
    expect(toks('run ./scripts/build.sh and ../x/y.txt')).toEqual(['./scripts/build.sh', '../x/y.txt'])
    expect(toks('in wails-app/app_open.go:10:5, ok')).toEqual(['wails-app/app_open.go:10:5'])
    expect(toks('C:\\Users\\me\\doc.md is there')).toEqual(['C:\\Users\\me\\doc.md'])
  })
  it('leaves prose, urls and extensionless words alone', () => {
    expect(toks('and/or maybe 1/2 of it')).toEqual([])
    expect(toks('https://example.com/docs/a.html')).toEqual([])
    expect(toks('the file README without a path')).toEqual([])
    expect(toks('version 1.2.3 released')).toEqual([])
  })
  it('reports offsets', () => {
    const [h] = findPathTokens('x ./a.go y')
    expect('x ./a.go y'.slice(h.start, h.end)).toBe('./a.go')
  })
})

describe('codeLooksLikePath / splitLine', () => {
  it('accepts one-token paths and filenames', () => {
    for (const c of ['/etc/hosts', '~/projects/x', './a/b', '../c', 'main.go:12', 'a/b/c.md', 'C:\\x\\y.md']) expect(codeLooksLikePath(c)).toBe(true)
  })
  it('refuses commands, options and non-paths', () => {
    for (const c of ['ls -la /', '-rf', 'foo', '$HOME/x', 'a | b', '/', '//host/x', 'x'.repeat(400), '']) expect(codeLooksLikePath(c)).toBe(false)
  })
  it('splits a line suffix', () => {
    expect(splitLine('a.go:12:3')).toEqual({ path: 'a.go', line: 12 })
    expect(splitLine('C:\\a.md')).toEqual({ path: 'C:\\a.md', line: 0 })
  })
})

describe('network and device paths', () => {
  const bad = ['/\\h\\s\\x.md', '\\/h/s/x.md', '//h/s/x.md', '\\\\h\\s\\x.md', '\\\\?\\C:\\x.md', '\\\\.\\pipe\\x', '\\??\\C:\\x.md', 'C:foo.md', 'c:foo.md:3', '//h/s/x.md:7']
  it.each(bad)('%s is never looked up', (tok) => {
    expect(isNetworkOrDevicePath(tok)).toBe(true)
    expect(codeLooksLikePath(tok)).toBe(false)
    expect(findPathTokens(`see ${tok} now`).map((h) => h.token)).not.toContain(tok)
  })
  it('keeps the ordinary drive and posix forms', () => {
    for (const ok of ['C:\\ok\\x.md', 'C:/ok/x.md', '/abs/x.md', 'docs/plan.md']) {
      expect(isNetworkOrDevicePath(ok)).toBe(false)
      expect(codeLooksLikePath(ok)).toBe(true)
    }
  })
})
