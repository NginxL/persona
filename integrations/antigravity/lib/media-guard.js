// Keeps pictures the model service cannot decode out of agy conversations (docs/integrations/runtimes.md "Antigravity
// CLI"): agy's read_file keeps a picture's bytes in the history, and one truncated JPEG fails every later request.
import { createHash } from 'node:crypto'
import { closeSync, existsSync, fstatSync, mkdirSync, openSync, readFileSync, readSync, renameSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

export const GUARD_PLUGIN = 'persona-media-guard'
const PASS = { decision: 'ask' } // no opinion: agy's own permissions decide
const bad = (mime, scan) => ({ ok: false, mime, reason: 'truncated', ...(scan ? { scan } : {}) })

/** {ok: true} for a complete picture or no picture at all; else its mime and why (scan: JPEG image data began). */
export function inspectImage(buf) {
  const at = (i, s) => buf.subarray(i, i + s.length).toString('latin1') === s
  if (buf[0] === 0xff && buf[1] === 0xd8 && buf[2] === 0xff) {
    for (let i = 2; i + 4 <= buf.length;) {
      const m = buf[i + 1]
      if (buf[i] !== 0xff) return { ok: false, mime: 'image/jpeg', reason: 'corrupt' }
      if (m === 0xff || m === 0x01 || (m >= 0xd0 && m <= 0xd7)) { i += m === 0xff ? 1 : 2; continue }
      if (m === 0xd9) break
      const end = i + 2 + buf.readUInt16BE(i + 2)
      if (end > buf.length) return bad('image/jpeg')
      // Image data stuffs its 0xFF bytes: the first end marker after a scan ends the picture.
      if (m === 0xda) return buf.indexOf(Buffer.from([0xff, 0xd9]), end) < 0 ? bad('image/jpeg', end < buf.length) : { ok: true, mime: 'image/jpeg' }
      i = end
    }
    return buf.length >= 4 && buf.readUInt16BE(buf.length - 2) === 0xffd9 ? { ok: true, mime: 'image/jpeg' } : bad('image/jpeg')
  }
  if (at(0, '\x89PNG\r\n\x1a\n')) {
    for (let i = 8; i + 12 <= buf.length;) {
      const type = buf.subarray(i + 4, i + 8).toString('latin1')
      i += 12 + buf.readUInt32BE(i)
      if (i <= buf.length && type === 'IEND') return { ok: true, mime: 'image/png' }
    }
    return bad('image/png')
  }
  if (at(0, 'GIF87a') || at(0, 'GIF89a')) {
    let end = buf.length
    while (end > 0 && buf[end - 1] === 0) end--
    return buf[end - 1] === 0x3b ? { ok: true, mime: 'image/gif' } : bad('image/gif')
  }
  if (at(0, 'RIFF') && at(8, 'WEBP')) return 8 + buf.readUInt32LE(4) <= buf.length ? { ok: true, mime: 'image/webp' } : bad('image/webp')
  return { ok: true }
}

function readSmall(path) {
  const fd = openSync(path, 'r')
  try {
    const { size } = fstatSync(fd)
    if (!fstatSync(fd).isFile() || !size || size > 64 << 20) return undefined // bigger: agy sends no picture
    const buf = Buffer.alloc(size)
    return buf.subarray(0, readSync(fd, buf, 0, size, 0))
  } finally {
    closeSync(fd)
  }
}

/** The hook's answer to a PreToolUse payload; anything unexpected lets the call go on. */
export function decide(payload, state) {
  try {
    const args = payload?.toolCall?.args || {}
    const path = ['Path', 'AbsolutePath', 'path', 'file_path'].map((k) => args[k]).find((v) => typeof v === 'string' && v)
    const buf = path && readSmall(path)
    const found = buf ? inspectImage(buf) : { ok: true }
    if (found.ok) return PASS
    let reason = `${path} is a ${found.mime} picture that is ${found.reason === 'truncated' ? 'cut short (truncated)' : 'damaged'}. ` +
      'The model service rejects it, and in this conversation it would fail every later request, so it was not opened. ' +
      'The file is kept unchanged. Tell the user the picture is incomplete and go on without it.'
    if (found.scan && state) {
      // The part that arrived, terminated: the service accepts it. Named by the original's content, written once.
      const copy = join(state, `${createHash('sha256').update(buf).digest('hex').slice(0, 32)}.partial.jpg`)
      if (!existsSync(copy)) {
        mkdirSync(state, { recursive: true, mode: 0o700 })
        writeFileSync(`${copy}.tmp`, Buffer.concat([buf, Buffer.from([0xff, 0xd9])]), { mode: 0o600 })
        renameSync(`${copy}.tmp`, copy)
      }
      reason += ` The part that arrived is readable as a separate copy: ${copy}`
    }
    return { decision: 'deny', reason }
  } catch {
    return PASS
  }
}

/**
 * Writes the guard as an agy plugin under agy's global customization root (<agyDir>/../config/plugins). Its command
 * answers "ask" once the script is gone (the package moved on), so a stale plugin never blocks agy. '': not written.
 */
export function installMediaGuard({ agyDir, script, node = process.execPath, log = () => {} }) {
  try {
    const dir = join(dirname(agyDir), 'config', 'plugins', GUARD_PLUGIN)
    const q = (s) => `'${String(s).replace(/'/g, `'\\''`)}'`
    const run = `if [ -r ${q(script)} ] && [ -x ${q(node)} ]; then exec ${q(node)} ${q(script)} --state ${q(join(agyDir, GUARD_PLUGIN))}; ` +
      `else cat >/dev/null 2>&1; printf '%s\\n' '{"decision":"ask"}'; fi`
    const hooks = { PreToolUse: [{ matcher: 'read_file|view_file', hooks: [{ type: 'command', command: `/bin/sh -c ${q(run)}`, timeout: 10 }] }] }
    mkdirSync(dir, { recursive: true, mode: 0o700 })
    for (const [name, value] of [['plugin.json', { name: GUARD_PLUGIN }], ['hooks.json', { [GUARD_PLUGIN]: hooks }]]) {
      const file = join(dir, name)
      const text = JSON.stringify(value, null, 2) + '\n'
      if (existsSync(file) && readFileSync(file, 'utf8') === text) continue
      writeFileSync(`${file}.tmp`, text, { mode: 0o600 })
      renameSync(`${file}.tmp`, file)
    }
    return dir
  } catch (err) {
    log(`media guard: ${err.message}`)
    return ''
  }
}
