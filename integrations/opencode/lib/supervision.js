// Authority is supplied through an isolated worker's configuration or ACP
// systemPrompt, never through user text or shared workspace instructions.
import { execFile } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { mkdir, mkdtemp, writeFile, readFile, realpath, rm, readdir, lstat, rename } from 'node:fs/promises'
import { join, relative, resolve, isAbsolute, dirname, basename } from 'node:path'
import { promisify } from 'node:util'
import { RpcError } from '../../runtime-sdk/index.js'
import { OpencodeAcpClient, privateAcpArgs } from './acp.js'
import { stripJsonc } from './models.js'

export const VERIFIED_POLICY_VERSIONS = new Set(['1.19.6'])
const runFile = promisify(execFile)
const MAX_POLICY_BYTES = 128 * 1024
const OWNER_KIND = 'persona-opencode-policy'
const refusal = () => new RpcError('OpenCode could not apply isolated follow-up instructions. Nothing was sent.', 'assistant_supervision_runtime')
const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value)
const contained = (parent, path) => { const rel = relative(parent, path); return rel === '' || !rel.startsWith('..') && !isAbsolute(rel) }

export function supportsSystemPrompt(capabilities) {
  // ACP: {} advertises append; mode only lists additional delivery modes.
  return isObject(capabilities?.sessionCapabilities?.systemPrompt)
}

export function policyEnvironment(env, file, route) {
  const content = env.OPENCODE_CONFIG_CONTENT
  const config = content ? JSON.parse(stripJsonc(content)) : {}
  if (!isObject(config) || config.instructions !== undefined && (!Array.isArray(config.instructions) || config.instructions.some((p) => typeof p !== 'string'))) throw refusal()
  if (file) config.instructions = [...new Set([...(config.instructions ?? []), file])]
  if (route) {
    if (config.mcp !== undefined && !isObject(config.mcp)) throw refusal()
    config.mcp = { ...config.mcp, persona: route.remoteConfig }
  }
  return { ...env, OPENCODE_CONFIG_CONTENT: JSON.stringify(config) }
}

export function createSupervisionStore({ home, bin, env, directory, run = runFile, acpFactory = (options) => new OpencodeAcpClient({ ...options, args: privateAcpArgs() }) }) {
  const root = resolve(directory || join(home, '.local', 'state', 'persona', 'opencode-supervision'))
  let ready = null
  let rootReady
  const owned = new Set()
  async function ensureRoot() {
    rootReady ??= (async () => {
      const persona = resolve(home, '.persona')
      if (contained(persona, root)) throw refusal()
      const actualPersona = await realpath(persona).catch(() => persona)
      // Resolve existing ancestors before creating anything: a symlink from
      // the configured root must not create a directory in shared Persona data.
      let ancestor = root
      const missing = []
      for (;;) {
        try {
          const actual = await realpath(ancestor)
          if (contained(actualPersona, resolve(actual, ...missing))) throw refusal()
          break
        } catch (error) {
          if (error.code !== 'ENOENT') throw error
          missing.unshift(basename(ancestor)); ancestor = dirname(ancestor)
        }
      }
      await mkdir(root, { recursive: true, mode: 0o700 })
      const actualRoot = await realpath(root)
      if (contained(actualPersona, actualRoot)) throw refusal()
    })()
    return rootReady
  }
  async function create(text) {
    if (typeof text !== 'string' || Buffer.byteLength(text) > MAX_POLICY_BYTES) throw refusal()
    if (!text) return null
    await ensureRoot()
    const dir = await mkdtemp(join(root, 'session-'))
    owned.add(dir)
    const file = join(dir, 'policy.md')
    try {
      // Cleanup metadata never supplies tool identity or execution authority.
      await writeFile(join(dir, 'owner.json'), JSON.stringify({ kind: OWNER_KIND, version: 1, pid: process.pid, sessionId: '' }), { mode: 0o600, flag: 'wx' })
      await writeFile(file, text, { mode: 0o600, flag: 'wx' })
      return { dir, file, text }
    } catch (error) { await remove({ dir }); throw error }
  }
  async function remove(record) {
    if (!record || !owned.has(record.dir)) return
    await rm(record.dir, { recursive: true, force: true })
    owned.delete(record.dir)
  }
  async function bind(record, sessionId) {
    if (!record) return
    if (!owned.has(record.dir) || typeof sessionId !== 'string' || !sessionId || sessionId.length > 1024) throw refusal()
    const pending = join(record.dir, 'owner.next')
    await writeFile(pending, JSON.stringify({ kind: OWNER_KIND, version: 1, pid: process.pid, sessionId }), { mode: 0o600, flag: 'wx' })
    await rename(pending, join(record.dir, 'owner.json'))
  }
  async function records() {
    // Do not create a policy directory merely to close an ordinary disk chat.
    let entries
    try {
      const persona = await realpath(join(home, '.persona')).catch(() => resolve(home, '.persona'))
      if (contained(resolve(home, '.persona'), root) || contained(persona, await realpath(root))) throw refusal()
      entries = await readdir(root, { withFileTypes: true })
    } catch (error) { if (error.code === 'ENOENT') return []; throw error }
    const result = []
    for (const entry of entries) {
      if (!entry.isDirectory() || !/^session-[A-Za-z0-9]{6}$/.test(entry.name)) continue
      const dir = join(root, entry.name)
      try {
        const marker = join(dir, 'owner.json'), info = await lstat(marker)
        if (!info.isFile() || info.isSymbolicLink() || info.size > 4096) continue
        const owner = JSON.parse(await readFile(marker, 'utf8'))
        if (isObject(owner) && owner.kind === OWNER_KIND && owner.version === 1 && Number.isInteger(owner.pid) && owner.pid > 0 && owner.pid <= 2147483647 && typeof owner.sessionId === 'string' && owner.sessionId.length <= 1024) result.push({ dir, owner })
      } catch (error) { if (error.code !== 'ENOENT' && !(error instanceof SyntaxError)) throw error }
    }
    return result
  }
  async function removeForSession(sessionId, keep) {
    for (const record of await records()) {
      if (record.owner.sessionId !== sessionId || record.dir === keep?.dir) continue
      await rm(record.dir, { recursive: true, force: true })
      owned.delete(record.dir)
    }
  }
  async function collectStale() {
    for (const record of await records()) {
      if (owned.has(record.dir)) continue
      let alive = true
      try { process.kill(record.owner.pid, 0) } catch (error) { alive = error.code !== 'ESRCH' }
      if (!alive) {
        await rm(record.dir, { recursive: true, force: true })
      }
    }
  }
  async function verify(record, route) {
    if (!record || await readFile(record.file, 'utf8') !== record.text) throw refusal()
    const workerEnv = policyEnvironment(env, record.file, route)
    const { stdout } = await run(bin, ['debug', 'config'], { env: workerEnv, cwd: home, timeout: 15_000, maxBuffer: 4 * 1024 * 1024 })
    const config = JSON.parse(stdout)
    if (!Array.isArray(config.instructions) || !config.instructions.includes(record.file)) throw refusal()
    const prior = env.OPENCODE_CONFIG_CONTENT ? JSON.parse(stripJsonc(env.OPENCODE_CONFIG_CONTENT)).instructions ?? [] : []
    if (prior.some((file) => !config.instructions.includes(file))) throw refusal()
    if (route && (config.mcp?.persona?.url !== route.remoteConfig.url)) throw refusal()
    return workerEnv
  }
  return {
    get ready() { return ready },
    create,
    bind,
    remove,
    removeForSession,
    collectStale,
    verify,
    async check(record) {
      if (record && await readFile(record.file, 'utf8') !== record.text) throw refusal()
    },
    async probe() {
      ready = null
      let record, client
      try {
        await collectStale()
        const { stdout } = await run(bin, ['--version'], { env, cwd: home, timeout: 10_000, maxBuffer: 4096 })
        const version = stdout.trim()
        if (!VERIFIED_POLICY_VERSIONS.has(version)) return null
        record = await create(`Persona isolated instruction probe ${randomUUID()}`)
        const probeEnv = await verify(record)
        client = acpFactory({ bin, cwd: home, env: probeEnv, processGroup: true })
        const initialized = await client.ensureStarted(15_000)
        if (initialized.agentInfo?.version !== version) throw refusal()
        ready = { version, channel: supportsSystemPrompt(initialized.capabilities) ? 'append' : 'config' }
      } catch {
        // Never log config stdout/stderr: it can contain user provider settings.
        ready = null
      } finally {
        try { await client?.stop() } catch { ready = null }
        try { await remove(record) } catch { ready = null }
      }
      return ready
    },
    async stop() {
      ready = null
      await Promise.all([...owned].map((dir) => remove({ dir })))
    },
  }
}
