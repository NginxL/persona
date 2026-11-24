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

const ANSI_C_ESCAPES = { a: '\x07', b: '\b', e: '\x1b', E: '\x1b', f: '\f', n: '\n', r: '\r', t: '\t', v: '\v', '\\': '\\', "'": "'", '"': '"', '?': '?' }

function readAnsiC(command, start) {
  const n = command.length
  let i = start + 2
  let text = ''
  while (i < n && command[i] !== "'") {
    if (command[i] === '\\' && i + 1 < n) {
      const next = command[i + 1]
      if (next === 'x') {
        const hex = /^[0-9a-fA-F]{1,2}/.exec(command.slice(i + 2))
        if (hex) {
          text += String.fromCharCode(parseInt(hex[0], 16))
          i += 2 + hex[0].length
          continue
        }
      }
      const oct = /^[0-7]{1,3}/.exec(command.slice(i + 1))
      if (oct) {
        text += String.fromCharCode(parseInt(oct[0], 8))
        i += 1 + oct[0].length
        continue
      }
      if (next === 'u' || next === 'U') {
        const uni = (next === 'u' ? /^[0-9a-fA-F]{1,4}/ : /^[0-9a-fA-F]{1,8}/).exec(command.slice(i + 2))
        if (uni) {
          const cp = parseInt(uni[0], 16)
          if (cp <= 0x10ffff) text += String.fromCodePoint(cp)
          i += 2 + uni[0].length
          continue
        }
      }
      text += ANSI_C_ESCAPES[next] ?? next
      i += 2
      continue
    }
    text += command[i++]
  }
  if (i < n) i++
  return { text, end: i }
}

function readCommandSub(command, start) {
  const n = command.length
  let i = start
  let depth = 1
  while (i < n && depth > 0) {
    const ch = command[i]
    if (ch === '\\' && i + 1 < n) {
      i += 2
      continue
    }
    if (ch === '$' && command[i + 1] === "'") {
      i = readAnsiC(command, i).end
      continue
    }
    if (ch === "'") {
      i++
      while (i < n && command[i] !== "'") i++
      if (i < n) i++
      continue
    }
    if (ch === '"') {
      i++
      while (i < n && command[i] !== '"') {
        if (command[i] === '\\' && i + 1 < n) i += 2
        else if (command[i] === '$' && command[i + 1] === '(') i = readCommandSub(command, i + 2).end
        else if (command[i] === '`') {
          i++
          while (i < n && command[i] !== '`') i += command[i] === '\\' && i + 1 < n ? 2 : 1
          if (i < n) i++
        } else i++
      }
      if (i < n) i++
      continue
    }
    if (ch === '`') {
      i++
      while (i < n && command[i] !== '`') i += command[i] === '\\' && i + 1 < n ? 2 : 1
      if (i < n) i++
      continue
    }
    if (ch === '(') depth++
    else if (ch === ')') {
      depth--
      if (depth === 0) return { body: command.slice(start, i), end: i + 1 }
    }
    i++
  }
  return { body: command.slice(start, i), end: i }
}

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
      const sub = readCommandSub(command, i + 2)
      embedded.push(sub.body)
      i = sub.end
      continue
    }
    if (ch === '$' && command[i + 1] === "'") {
      hasToken = true
      const ansi = readAnsiC(command, i)
      cur += ansi.text
      i = ansi.end
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
          const sub = readCommandSub(command, i + 2)
          embedded.push(sub.body)
          i = sub.end
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
