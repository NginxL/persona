// Persona's model providers in pi (docs/integrations/pi.md, #8194): in-scope providers from Settings › Model
// providers asked of node.sock (`launcher.pi` with launch.key) and registered via pi.registerProvider() in memory.
// User configuration wins: provider ids in models.json, default models in settings, or CLI model flags stay theirs.

import { readFileSync } from 'node:fs'
import { createConnection } from 'node:net'
import { homedir } from 'node:os'
import { dirname, join } from 'node:path'
import { socketDir } from '../../runtime-sdk/lib/identity.js'

/** The node's local socket and the launch key beside it. */
export function nodeSocketPath(env = process.env) {
  return join(socketDir(homedir(), env), 'node.sock')
}

function readLaunchKey(socket) {
  try {
    const k = readFileSync(join(dirname(socket), 'launch.key'), 'utf8').trim()
    return /^[0-9a-f]{64}$/i.test(k) ? k : ''
  } catch {
    return ''
  }
}

/**
 * Ask the node for the providers (one request, one JSON line back). null when there is nothing to ask (no node, no
 * launch key this process can read: a sandbox that hides the data directory) or the node refused; never throws.
 */
export function askPersonaProviders({ socket = nodeSocketPath(), timeoutMs = 30000, log = () => {} } = {}) {
  const key = readLaunchKey(socket)
  if (!key) return Promise.resolve(null)
  return new Promise((resolve) => {
    let buf = ''
    let done = false
    const finish = (v) => {
      if (done) return
      done = true
      clearTimeout(timer)
      conn.destroy()
      resolve(v)
    }
    const conn = createConnection(socket)
    conn.setEncoding('utf8')
    const timer = setTimeout(() => { log('persona providers: the node did not answer in time'); finish(null) }, timeoutMs)
    conn.on('error', () => finish(null))
    conn.on('close', () => finish(null))
    conn.on('connect', () => conn.write(JSON.stringify({ kind: 'launcher.pi', launch_key: key }) + '\n'))
    conn.on('data', (chunk) => {
      buf += chunk
      const nl = buf.indexOf('\n')
      if (nl < 0) return
      let answer = null
      try { answer = JSON.parse(buf.slice(0, nl)) } catch {}
      if (answer?.error) { log(`persona providers: ${answer.error}`); answer = null }
      else if (!Array.isArray(answer?.providers)) answer = null
      finish(answer)
    })
  })
}

function readJSON(path) {
  try { return JSON.parse(readFileSync(path, 'utf8')) } catch { return undefined }
}

/** pi's agent directory (PI_CODING_AGENT_DIR, else ~/.pi/agent). */
export function piAgentDir(env = process.env) {
  return env.PI_CODING_AGENT_DIR || join(homedir(), '.pi', 'agent')
}

/**
 * What the user configured in pi themselves: the provider ids of their models.json (only the ids are read) and
 * whether their settings (global or the project's) name a default model or provider.
 */
export function userPiConfig({ agentDir = piAgentDir(), cwd = process.cwd() } = {}) {
  const models = readJSON(join(agentDir, 'models.json'))
  const providers = new Set(models && typeof models.providers === 'object' && models.providers ? Object.keys(models.providers) : [])
  const names = (s) => !!(s && typeof s === 'object' && (s.defaultProvider || s.defaultModel))
  const defaultModel = names(readJSON(join(agentDir, 'settings.json'))) || names(readJSON(join(cwd, '.pi', 'settings.json')))
  return { providers, defaultModel }
}

/** The command line picks the model itself. */
export function argvPicksModel(argv = process.argv) {
  for (const a of argv) {
    if (a === '--') return false
    if (['-m', '--model', '--provider', '--models'].includes(a) || /^(?:-m|--(?:model|provider|models))=/.test(a)) return true
  }
  return false
}

/** One provider of the node's answer as pi's registerProvider takes it. */
export function providerConfig(p) {
  return {
    name: p.name,
    baseUrl: p.base_url,
    apiKey: p.api_key,
    api: p.api,
    ...(p.auth_header ? { authHeader: true } : {}),
    ...(p.headers && Object.keys(p.headers).length ? { headers: p.headers } : {}),
    models: (p.models || []).map((m) => ({
      id: m.id,
      name: m.name || m.id,
      reasoning: !!m.reasoning,
      input: m.input?.length ? m.input : ['text'],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      contextWindow: m.context_window || 200000,
      maxTokens: m.max_tokens || 32000,
    })),
  }
}

const EFFORTS = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max']

/**
 * Register what answer has in pi, the user's own providers left alone. Returns the ids registered.
 */
export function registerPersonaProviders(pi, answer, user, log = () => {}) {
  const ids = []
  for (const p of answer?.providers || []) {
    if (typeof p?.id !== 'string' || !p.id || !p.api_key || !p.models?.length) continue
    if (user.providers.has(p.id)) {
      log(`persona providers: ${p.id} is in your models.json; yours is used`)
      continue
    }
    pi.registerProvider(p.id, providerConfig(p))
    ids.push(p.id)
  }
  for (const s of answer?.skipped || []) log(`persona providers: ${s.launcher} not offered: ${s.error}`)
  return ids
}

/** The session already has a conversation (resumed, forked): its model is its own. */
function sessionHasHistory(ctx) {
  try {
    return (ctx.sessionManager?.getEntries?.() || []).some((e) => e.type === 'message' || e.type === 'model_change')
  } catch {
    return false
  }
}

/**
 * At a new session's start: Persona's Agents model when the user picked none (settings, command line) and pi has no
 * usable model of the user's own selected. Returns whether it switched.
 */
export async function applyPersonaDefault(pi, ctx, answer, registered, { user, argv = process.argv } = {}) {
  const def = answer?.default
  if (!def || !registered.includes(def.provider) || user.defaultModel || argvPicksModel(argv) || sessionHasHistory(ctx)) return false
  const current = ctx.model
  if (current && !String(current.provider || '').startsWith('persona-')) {
    let own = false
    try { own = !!ctx.modelRegistry?.hasConfiguredAuth?.(current) } catch {}
    if (own) return false // the user's own provider, signed in: theirs
  }
  if (current?.provider === def.provider && current?.id === def.model) return false
  const model = ctx.modelRegistry?.find?.(def.provider, def.model)
  if (!model) return false
  const ok = await pi.setModel(model)
  if (ok && EFFORTS.includes(def.effort)) {
    try { pi.setThinkingLevel(def.effort) } catch {}
  }
  return !!ok
}

/**
 * The whole of it for one pi extension load: ask, register, and pick the default when a session starts.
 */
export async function usePersonaProviders(pi, { ask = askPersonaProviders, user = userPiConfig(), argv = process.argv, log = () => {} } = {}) {
  let answer = null
  try { answer = await ask({ log }) } catch {}
  if (!answer) return []
  const registered = registerPersonaProviders(pi, answer, user, log)
  if (registered.length && answer.default) {
    pi.on('session_start', async (event, ctx) => {
      if (event?.reason && !['startup', 'new'].includes(event.reason)) return
      try { await applyPersonaDefault(pi, ctx, answer, registered, { user, argv }) } catch (e) { log(`persona providers: ${e?.message || e}`) }
    })
  }
  return registered
}
