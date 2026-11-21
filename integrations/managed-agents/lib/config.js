// The companion's configuration (docs/integrations/managed-agents.md "Configuration"): one or more profiles, each a
// Managed Agents endpoint with the user's own key. `mode: "cloud"` is a hosted endpoint (Ark by default); `mode: "self"`
// is one the user runs (a private deployment, a gateway, a local server), with its own address. Keys are references
// (secrets.js), never values.

import { readFile, stat } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { parseRef } from './secrets.js'

export const DEFAULT_CONFIG = join(homedir(), '.persona', 'managed-agents.json')
export const RUNTIME_ID = 'managed-agents'

// What each API family needs besides the key. `ark` is Volcengine Ark's Managed Agents, `claude` the API it follows
// (Claude Managed Agents); a self-hosted server speaks one of the two.
const FLAVORS = {
  ark: {
    base: (p) => `https://ark.${p.region || 'cn-beijing'}.volces.com`,
    prefix: '/api/v3',
    auth: 'bearer',
    headers: (p) => ({ 'X-Ark-Beta': p.beta || 'agentic-2026-06-01' }),
    modelHeaderPrefix: 'X-User-Model-Header-',
  },
  claude: {
    base: () => 'https://api.anthropic.com',
    prefix: '/v1',
    auth: 'x-api-key',
    headers: (p) => ({ 'anthropic-version': '2023-06-01', 'anthropic-beta': p.beta || 'managed-agents-2026-04-01' }),
    modelHeaderPrefix: '',
  },
}

const PROFILE_ID = /^[a-z0-9][a-z0-9-]{0,23}$/
const MA_ID = /^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$/
const HEADER = /^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$/
const SECRET_HEADER = /^(authorization|x-api-key|api-key|proxy-authorization|cookie)$/i

export class ConfigError extends Error {}

/** Reads and checks the file; an absent file is no profiles (the companion waits and rereads). */
export async function loadConfig(path = DEFAULT_CONFIG, { log = () => {} } = {}) {
  let text
  try {
    text = await readFile(path, 'utf8')
  } catch (err) {
    if (err.code === 'ENOENT') return { profiles: [], problems: [`no config at ${path}`] }
    throw new ConfigError(`cannot read ${path}: ${err.code ?? err.message}`)
  }
  try {
    const st = await stat(path)
    if ((st.mode & 0o077) !== 0) log(`${path} can be read by other users; chmod 600 it (it holds no keys, but names where they are)`)
  } catch {}
  let raw
  try {
    raw = JSON.parse(text)
  } catch (err) {
    throw new ConfigError(`${path} is not JSON: ${err.message}`)
  }
  return parseConfig(raw)
}

/** Checks a parsed config: valid profiles are kept, each bad one is a problem (its text never holds a value). */
export function parseConfig(raw) {
  if (!raw || typeof raw !== 'object' || !Array.isArray(raw.profiles)) throw new ConfigError('the config needs a "profiles" array')
  const problems = []
  const profiles = []
  const seen = new Set()
  for (const [i, p] of raw.profiles.entries()) {
    try {
      const profile = parseProfile(p)
      if (seen.has(profile.id)) throw new ConfigError(`profile id ${profile.id} appears twice`)
      seen.add(profile.id)
      if (!profile.disabled) profiles.push(profile)
    } catch (err) {
      problems.push(`profiles[${i}]: ${err.message}`)
    }
  }
  return {
    runtime: typeof raw.runtime === 'string' && /^[a-z0-9][a-z0-9_.-]{0,31}$/.test(raw.runtime) ? raw.runtime : RUNTIME_ID,
    name: typeof raw.name === 'string' && raw.name.trim() ? raw.name.trim().slice(0, 40) : 'Managed Agents',
    profiles,
    problems,
  }
}

function parseProfile(p) {
  if (!p || typeof p !== 'object') throw new ConfigError('not an object')
  if (!PROFILE_ID.test(p.id ?? '')) throw new ConfigError('id: lowercase letters, digits and -, at most 24')
  if (p.mode !== 'cloud' && p.mode !== 'self') throw new ConfigError('mode: "cloud" or "self"')
  const flavorName = p.api ?? 'ark'
  const flavor = FLAVORS[flavorName]
  if (!flavor) throw new ConfigError('api: "ark" or "claude"')
  // An inline key is refused outright: "api_key" must be a reference. The text is not echoed back.
  if (p.api_key === undefined) throw new ConfigError('api_key: a reference to your key is required (env:, file:, keychain: or extension:)')
  parseRef(p.api_key, 'api_key')
  if (p.mode === 'self' && !p.base_url) throw new ConfigError('base_url: a self-hosted profile needs the address of your server')
  const base = String(p.base_url || flavor.base(p)).replace(/\/+$/, '')
  let url
  try {
    url = new URL(base)
  } catch {
    throw new ConfigError('base_url: not a URL')
  }
  if (url.username || url.password) throw new ConfigError('base_url: no user name or password in the address; use api_key')
  if (url.search || url.hash) throw new ConfigError('base_url: no query or fragment')
  const loopback = ['localhost', '127.0.0.1', '[::1]', '::1'].includes(url.hostname)
  if (url.protocol === 'http:' && !loopback && p.allow_http !== true) {
    throw new ConfigError('base_url: http:// sends your key in the clear; use https://, a loopback address, or set allow_http: true')
  }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') throw new ConfigError('base_url: http(s) only')
  const prefix = normalizePrefix(p.api_prefix ?? flavor.prefix)
  const auth = p.auth ?? flavor.auth
  if (auth !== 'bearer' && auth !== 'x-api-key') throw new ConfigError('auth: "bearer" or "x-api-key"')
  const headers = { ...flavor.headers(p) }
  for (const [k, v] of Object.entries(p.headers ?? {})) {
    if (!HEADER.test(k) || typeof v !== 'string' || /[\r\n]/.test(v)) throw new ConfigError(`headers.${k}: not a valid header`)
    if (SECRET_HEADER.test(k)) throw new ConfigError(`headers.${k}: credentials go in api_key or model_headers, never in headers`)
    headers[k] = v
  }
  const modelHeaders = {}
  for (const [k, ref] of Object.entries(p.model_headers ?? {})) {
    if (!HEADER.test(k)) throw new ConfigError(`model_headers.${k}: not a header name`)
    parseRef(ref, `model_headers.${k}`)
    modelHeaders[k] = ref
  }
  const list = p.list ?? {}
  const start = p.start ?? undefined
  if (start !== undefined) {
    if (!MA_ID.test(start.agent ?? '')) throw new ConfigError('start.agent: the id of the agent new sessions run')
    if (start.environment_id !== undefined && !MA_ID.test(start.environment_id)) throw new ConfigError('start.environment_id: not an id')
  }
  const ids = (a, what) => {
    if (a === undefined) return []
    if (!Array.isArray(a) || a.some((x) => !MA_ID.test(x))) throw new ConfigError(`${what}: a list of ids`)
    return a
  }
  return {
    id: p.id,
    name: typeof p.name === 'string' && p.name.trim() ? p.name.trim().slice(0, 40) : p.id,
    mode: p.mode,
    api: flavorName,
    base,
    prefix,
    auth,
    apiKey: p.api_key,
    headers,
    modelHeaders,
    modelHeaderPrefix: typeof p.model_header_prefix === 'string' ? p.model_header_prefix : flavor.modelHeaderPrefix,
    project: typeof p.project === 'string' ? p.project : undefined,
    listPath: normalizePrefix(list.path ?? '/sessions'),
    listEnabled: list.enabled !== false,
    listLimit: clampInt(list.limit, 1, 100, 50),
    agents: ids(p.agents, 'agents'),
    sessions: ids(p.sessions, 'sessions'),
    start: start && { agent: start.agent, environmentId: start.environment_id, launcher: launcherName(start.launcher, p.id), label: typeof start.label === 'string' ? start.label.slice(0, 40) : undefined, body: plainObject(start.body) },
    questionTools: strings(p.question_tools, ['AskQuestion', 'AskUserQuestion']),
    planTools: strings(p.plan_tools, ['ExitPlanMode']),
    images: p.images === 'none' ? 'none' : 'base64',
    stream: p.stream !== false,
    pollMs: clampInt(p.poll_seconds, 1, 60, 3) * 1000,
    recentMinutes: clampInt(p.recent_minutes, 0, 24 * 60, 30),
    disabled: p.disabled === true,
  }
}

function normalizePrefix(s) {
  if (typeof s !== 'string' || /[?#\s]/.test(s)) throw new ConfigError('a path prefix like /api/v3')
  const t = s.replace(/\/+$/, '')
  return t === '' ? '' : t.startsWith('/') ? t : `/${t}`
}

function launcherName(name, id) {
  const n = name ?? `ma-${id}`
  if (!/^[a-z0-9][a-z0-9_-]{0,39}$/.test(n)) throw new ConfigError('start.launcher: lowercase letters, digits, - and _')
  return n
}

function clampInt(v, lo, hi, dflt) {
  return Number.isInteger(v) ? Math.min(hi, Math.max(lo, v)) : dflt
}

function strings(v, dflt) {
  return Array.isArray(v) && v.every((x) => typeof x === 'string') ? v : dflt
}

function plainObject(v) {
  return v && typeof v === 'object' && !Array.isArray(v) ? v : undefined
}
