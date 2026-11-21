// Mandatory Assistant shell gates.
// Match declared command names and script paths.
export function shellWordsRe(words) {
  const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const alts = (Array.isArray(words) ? words : [])
    .filter((w) => typeof w === 'string' && w !== '')
    .map((w) => (w.includes('/') ? `[^\\s;&|()\`"'<>]*${esc(w)}` : `(?:[^\\s;&|()\`"'<>]*/)?${esc(w)}`))
  return alts.length ? new RegExp(`(^|[\\s;&|(\`$"'><])(${alts.join('|')})([\\s;&|)\`"'<>]|$)`) : null
}

const SHELL_BINARIES = new Set(['sh', 'bash', 'zsh', 'dash', 'ksh', 'ash'])

// Parse shell words/operators and retain nested command substitutions.
function tokenizeShell(command) {
  const tokens = []
  const embedded = []
  let cur = ''
  let hasToken = false
  let i = 0
  const n = command.length
  const flush = () => {
    if (hasToken) {
      tokens.push(cur)
      cur = ''
      hasToken = false
    }
  }
  while (i < n) {
    const ch = command[i]
    // Token boundary.
    if (/[\s;&|()<>]/.test(ch)) {
      flush()
      i++
      continue
    }
    if (ch === '`') {
      flush()
      i++
      const start = i
      while (i < n && command[i] !== '`') {
        if (command[i] === '\\' && i + 1 < n) i += 2
        else i++
      }
      embedded.push(command.slice(start, i))
      if (i < n) i++
      continue
    }
    if (ch === '$' && command[i + 1] === '(') {
      flush()
      i += 2
      const start = i
      let depth = 1
      while (i < n && depth > 0) {
        if (command[i] === '\\' && i + 1 < n) { i += 2; continue }
        if (command[i] === '(') depth++
        else if (command[i] === ')') depth--
        if (depth > 0) i++
      }
      embedded.push(command.slice(start, i))
      if (i < n) i++
      continue
    }
    if (ch === "'") {
      hasToken = true
      i++
      while (i < n && command[i] !== "'") {
        cur += command[i++]
      }
      if (i < n) i++
      continue
    }
    if (ch === '"') {
      hasToken = true
      i++
      while (i < n && command[i] !== '"') {
        if (command[i] === '\\' && i + 1 < n) {
          const next = command[i + 1]
          cur += (next === '"' || next === '\\' || next === '$' || next === '`') ? next : `\\${next}`
          i += 2
          continue
        }
        if (command[i] === '$' && command[i + 1] === '(') {
          i += 2
          const start = i
          let depth = 1
          while (i < n && depth > 0) {
            if (command[i] === '\\' && i + 1 < n) { i += 2; continue }
            if (command[i] === '(') depth++
            else if (command[i] === ')') depth--
            if (depth > 0) i++
          }
          embedded.push(command.slice(start, i))
          if (i < n) i++
          continue
        }
        if (command[i] === '`') {
          i++
          const start = i
          while (i < n && command[i] !== '`') {
            if (command[i] === '\\' && i + 1 < n) i += 2
            else i++
          }
          embedded.push(command.slice(start, i))
          if (i < n) i++
          continue
        }
        cur += command[i++]
      }
      if (i < n) i++
      continue
    }
    if (ch === '\\' && i + 1 < n) {
      hasToken = true
      cur += command[i + 1]
      i += 2
      continue
    }
    hasToken = true
    cur += ch
    i++
  }
  flush()
  return { tokens, embedded }
}

function tokenMatchesWord(token, word) {
  if (!token || !word) return false
  const normTok = token.replace(/\\/g, '/')
  const normWord = word.replace(/\\/g, '/')
  if (normTok === normWord) return true
  return normTok.endsWith(`/${normWord}`)
}

function basenameOf(token) {
  const norm = token.replace(/\\/g, '/')
  const idx = norm.lastIndexOf('/')
  return idx >= 0 ? norm.slice(idx + 1) : norm
}

// Build command gates from the current tool inventory.
export function extractShellGates(tools) {
  const out = []
  for (const t of Array.isArray(tools) ? tools : []) {
    if (!Array.isArray(t?.shell) || t.shell.length === 0) continue
    const words = t.shell.filter((w) => typeof w === 'string' && w !== '')
    const re = shellWordsRe(words)
    if (re) out.push({ name: t.name, command: words[0], words, re })
  }
  return out
}

// Find a gate hit, including nested commands.
export function matchShellGate(command, gates, depth = 0) {
  if (depth > 4 || typeof command !== 'string' || !command.trim() || !Array.isArray(gates) || gates.length === 0) {
    return undefined
  }
  const { tokens, embedded } = tokenizeShell(command)
  for (const tok of tokens) {
    for (const g of gates) {
      const words = Array.isArray(g.words) ? g.words : (g.command ? [g.command] : [])
      if (words.some((w) => tokenMatchesWord(tok, w))) return g
    }
  }
  // Check eval, nested shells and substitutions.
  for (let i = 0; i < tokens.length; i++) {
    const base = basenameOf(tokens[i])
    if (base === 'eval' && i + 1 < tokens.length) {
      const sub = matchShellGate(tokens.slice(i + 1).join(' '), gates, depth + 1)
      if (sub) return sub
    } else if (SHELL_BINARIES.has(base)) {
      for (let j = i + 1; j < tokens.length; j++) {
        const arg = tokens[j]
        if (arg === '--') break
        if (/^-[A-Za-z]*c[A-Za-z]*$/.test(arg) && j + 1 < tokens.length) {
          const sub = matchShellGate(tokens[j + 1], gates, depth + 1)
          if (sub) return sub
          break
        }
        if (!arg.startsWith('-')) break
      }
    }
  }
  for (const inner of embedded) {
    const sub = matchShellGate(inner, gates, depth + 1)
    if (sub) return sub
  }
  return gates.find((g) => g.re?.test(command))
}
