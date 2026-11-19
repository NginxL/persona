// Managed per-session supervision policy rule directories for Antigravity (agy).
//
// Each supervised session receives its own isolated directory under `<agyDir>/persona-supervision/<rawId>`
// passed to `agy acp` via `additionalDirectories`. Because it lives outside `~/.persona/assistant`, ordinary
// chats sharing `~/.persona/assistant` never discover or inherit another session's supervision policy, and the
// shared `~/.persona/assistant/AGENTS.md` is never rewritten. `agy` loads both `AGENTS.md` and `GEMINI.md`
// from `additionalDirectories` directly into `<user_rules>` on every model turn (surviving compaction and
// process restarts) with a 24,000-byte per-file cap; policies above 24,000 bytes (for example leaders with
// long direction notes) split cleanly across `AGENTS.md` and `GEMINI.md` up to 48,000 bytes, and fail closed
// otherwise.
import { randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
import { basename, dirname, join, resolve, sep } from 'node:path'
import { RpcError } from '../../runtime-sdk/index.js'

export const POLICY_DIR_NAME = 'persona-supervision'
export const SUPERVISION_ERROR_CODE = 'assistant_supervision_runtime'
export const AGY_RULE_MAX_BYTES = 24_000

const PRIMARY_RULE = 'AGENTS.md'
const SECONDARY_RULE = 'GEMINI.md'
const SAFE_SESSION_ID = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/

function failClosed(message) {
  return new RpcError(message, SUPERVISION_ERROR_CODE)
}

export function policyDirOf(policyRoot, rawId) {
  if (typeof rawId !== 'string' || !SAFE_SESSION_ID.test(rawId) || rawId.includes('..')) {
    throw failClosed(`Invalid session id for supervision policy: ${String(rawId)}`)
  }
  const root = resolve(policyRoot)
  const dir = resolve(root, rawId)
  if (dirname(dir) !== root) {
    throw failClosed(`Unsafe supervision policy path for session: ${rawId}`)
  }
  return dir
}

export function isPolicyDir(policyRoot, candidatePath) {
  if (!policyRoot || typeof candidatePath !== 'string' || !candidatePath) return false
  const root = resolve(policyRoot)
  const target = resolve(candidatePath)
  return target === root || target.startsWith(root + sep)
}

/** Find the largest UTF-8 prefix of `text` fitting within `maxBytes`, preferring a line boundary. */
function prefixWithinBytes(text, maxBytes) {
  let bytes = 0
  let lastNewline = -1
  let lastSafe = 0
  for (let i = 0; i < text.length; ) {
    const cp = text.codePointAt(i)
    const step = cp > 0xffff ? 2 : 1
    const len = cp <= 0x7f ? 1 : cp <= 0x7ff ? 2 : cp <= 0xffff ? 3 : 4
    if (bytes + len > maxBytes) break
    bytes += len
    i += step
    lastSafe = i
    if (cp === 0x0a) lastNewline = i
  }
  return lastNewline > 0 ? lastNewline : lastSafe
}

export function splitPolicy(policy, maxBytes = AGY_RULE_MAX_BYTES) {
  if (Buffer.byteLength(policy, 'utf8') <= maxBytes) {
    return { primary: policy, secondary: '' }
  }
  const cut = prefixWithinBytes(policy, maxBytes)
  const primary = policy.slice(0, cut)
  const secondary = policy.slice(cut)
  if (!primary || Buffer.byteLength(primary, 'utf8') > maxBytes || Buffer.byteLength(secondary, 'utf8') > maxBytes) {
    throw failClosed(`Supervision policy exceeds Antigravity rule limit (${maxBytes * 2} bytes)`)
  }
  return { primary, secondary }
}

function readOptionalFile(filePath) {
  try {
    return readFileSync(filePath, 'utf8')
  } catch (err) {
    if (err?.code === 'ENOENT') return ''
    throw err
  }
}

export function readPolicy(policyRoot, rawId) {
  let dir
  try {
    dir = policyDirOf(policyRoot, rawId)
  } catch {
    return ''
  }
  const primary = readOptionalFile(join(dir, PRIMARY_RULE))
  const secondary = readOptionalFile(join(dir, SECONDARY_RULE))
  return primary + secondary
}

function writeFileAtomic(filePath, content) {
  const tmp = join(dirname(filePath), `.${basename(filePath)}.tmp.${process.pid}.${randomBytes(4).toString('hex')}`)
  try {
    writeFileSync(tmp, content, { encoding: 'utf8', mode: 0o600 })
    renameSync(tmp, filePath)
  } catch (err) {
    try { rmSync(tmp, { force: true }) } catch {}
    throw err
  }
}

function syncFile(filePath, content) {
  let current
  try {
    current = readFileSync(filePath, 'utf8')
  } catch (err) {
    if (err?.code !== 'ENOENT') current = undefined
  }
  if (current === content) return
  writeFileAtomic(filePath, content)
}

/**
 * Atomically synchronize `rawId`'s supervision policy rule files on disk.
 * Returns the policy directory path when `policy` is nonempty, or `''` when cleared.
 * Throws `RpcError(..., 'assistant_supervision_runtime')` on any error (fail-closed).
 */
export function syncPolicy(policyRoot, rawId, policy, maxBytes = AGY_RULE_MAX_BYTES) {
  if (typeof policy !== 'string') {
    throw failClosed('Supervision policy must be a string')
  }
  const dir = policyDirOf(policyRoot, rawId)
  const primaryPath = join(dir, PRIMARY_RULE)
  const secondaryPath = join(dir, SECONDARY_RULE)
  if (policy === '') {
    try {
      rmSync(dir, { recursive: true, force: true })
    } catch (err) {
      throw failClosed(`Failed to clear supervision policy: ${err.message}`)
    }
    if (existsSync(primaryPath) || existsSync(secondaryPath)) {
      throw failClosed('Failed to clear supervision policy files')
    }
    return ''
  }
  const { primary, secondary } = splitPolicy(policy, maxBytes)
  try {
    mkdirSync(dir, { recursive: true, mode: 0o700 })
    syncFile(primaryPath, primary)
    if (secondary) {
      syncFile(secondaryPath, secondary)
    } else if (existsSync(secondaryPath)) {
      unlinkSync(secondaryPath)
    }
  } catch (err) {
    if (err instanceof RpcError) throw err
    throw failClosed(`Failed to write supervision policy: ${err.message}`)
  }
  return dir
}

export function removePolicy(policyRoot, rawId) {
  try {
    rmSync(policyDirOf(policyRoot, rawId), { recursive: true, force: true })
  } catch {}
}
