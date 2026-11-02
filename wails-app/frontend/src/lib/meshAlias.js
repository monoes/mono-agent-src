// aliasFromTitle turns a chat's name into a mesh name the way the host does (coderhost.AliasFromTitle):
// spaces become dashes, what the hub would not take is dropped, repeats collapse, it starts with a
// letter or digit and is at most 64 characters. '' when nothing usable is left.
export function aliasFromTitle(title) {
  let out = ''
  let dash = false
  for (const ch of String(title || '')) {
    if (/[A-Za-z0-9._]/.test(ch)) { out += ch; dash = false }
    else if (ch === '-' || ch === ' ' || ch === '\t') {
      if (out && !dash) { out += '-'; dash = true }
    }
  }
  out = out.replace(/^[._-]+/, '').slice(0, 64)
  return out.replace(/-+$/, '')
}
