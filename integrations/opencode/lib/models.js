// Model catalog discovery, normalization, and resolution for opencode runtimes (plugin and ACP companion).
// Supports:
//  - opencode v2 plugin `ctx.model.list()` (`ModelV2Info[]` or `{ data: ModelV2Info[] }`)
//  - opencode CLI `opencode models --verbose` (and plain `opencode models`)
//  - ACP `session/new` / `config_option_update` `configOptions`
//  - opencode config (`opencode.json` / `opencode.jsonc`) default `model` detection.

import { execFile } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { RpcError } from '../../runtime-sdk/index.js'

// Persona valid effort levels for non-Codex agents, weakest first (server/internal/proto & node/modes.go).
export const PERSONA_EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max']
const MAX_MODELS = 200
const DEFAULT_CLI_TIMEOUT_MS = 25_000

/** Check whether the next non-whitespace, non-comment character in `text` from `pos` is `}` or `]`. */
function closesContainerNext(text, pos) {
  let j = pos
  while (j < text.length) {
    const c = text[j]
    const n = text[j + 1]
    if (c === ' ' || c === '\t' || c === '\r' || c === '\n') {
      j++
      continue
    }
    if (c === '/' && n === '/') {
      j += 2
      while (j < text.length && text[j] !== '\n') j++
      continue
    }
    if (c === '/' && n === '*') {
      j += 2
      while (j < text.length && !(text[j] === '*' && text[j + 1] === '/')) j++
      j += 2
      continue
    }
    return c === '}' || c === ']'
  }
  return false
}

/** Strip JSONC comments (`//`, `/* ... *\/`) and trailing commas outside string literals. */
export function stripJsonc(text) {
  let out = ''
  let inString = false
  let escaped = false
  let i = 0
  while (i < text.length) {
    const ch = text[i]
    const next = text[i + 1]
    if (inString) {
      out += ch
      if (escaped) escaped = false
      else if (ch === '\\') escaped = true
      else if (ch === '"') inString = false
      i++
      continue
    }
    if (ch === '"') {
      inString = true
      out += ch
      i++
      continue
    }
    if (ch === '/' && next === '/') {
      i += 2
      while (i < text.length && text[i] !== '\n') i++
      continue
    }
    if (ch === '/' && next === '*') {
      i += 2
      while (i < text.length && !(text[i] === '*' && text[i + 1] === '/')) i++
      i += 2
      continue
    }
    if (ch === ',' && closesContainerNext(text, i + 1)) {
      i++
      continue
    }
    out += ch
    i++
  }
  return out
}

/** Read the configured default model from `<configDir>/opencode.jsonc` or `opencode.json` ("" if absent/unreadable). */
export function readConfiguredModel({ configDir, env = process.env, home = homedir() } = {}) {
  const dir = configDir || join(env.XDG_CONFIG_HOME || join(home, '.config'), 'opencode')
  for (const file of ['opencode.jsonc', 'opencode.json', 'config.json']) {
    const p = join(dir, file)
    if (!existsSync(p)) continue
    try {
      const parsed = JSON.parse(stripJsonc(readFileSync(p, 'utf8')))
      if (typeof parsed?.model === 'string' && parsed.model.trim()) return parsed.model.trim()
    } catch {
      // ignore malformed user config
    }
  }
  return ''
}

/** Normalize raw provider+model into canonical qualified id (`provider/model`). */
export function qualifyModelId(providerID, id) {
  const cleanId = typeof id === 'string' ? id.trim() : ''
  const cleanProvider = typeof providerID === 'string' ? providerID.trim() : ''
  if (!cleanId) return ''
  if (!cleanProvider) return cleanId
  if (cleanId.startsWith(`${cleanProvider}/`)) return cleanId
  return `${cleanProvider}/${cleanId}`
}

/** Split a qualified model string (`provider/model` or `provider:model`) into `{ providerID, modelID }`. */
export function splitModelId(raw) {
  const s = typeof raw === 'string' ? raw.trim() : ''
  if (!s) return { providerID: '', modelID: '' }
  const slash = s.indexOf('/')
  const colon = s.indexOf(':')
  const validSlash = slash > 0 && slash < s.length - 1
  const validColon = colon > 0 && colon < s.length - 1
  const sep = validSlash && validColon ? Math.min(slash, colon) : validSlash ? slash : validColon ? colon : -1
  if (sep > 0) {
    return { providerID: s.slice(0, sep), modelID: s.slice(sep + 1) }
  }
  return { providerID: '', modelID: s }
}

/** Extract Persona-compatible effort levels from opencode v1 object variants or v2 array variants. */
export function extractEfforts(variants) {
  const raw = []
  if (Array.isArray(variants)) {
    for (const v of variants) {
      if (typeof v === 'string') raw.push(v.toLowerCase())
      else if (typeof v?.id === 'string') raw.push(v.id.toLowerCase())
    }
  } else if (variants && typeof variants === 'object') {
    for (const key of Object.keys(variants)) raw.push(key.toLowerCase())
  }
  const set = new Set(raw)
  return PERSONA_EFFORTS.filter((e) => set.has(e))
}

/**
 * Parse output of `opencode models --verbose` (or plain `opencode models`).
 * Verbose output alternates `<provider>/<model>` header lines and JSON objects.
 */
export function parseCliModels(stdout) {
  const lines = String(stdout || '').split(/\r?\n/)
  const out = []
  let i = 0
  while (i < lines.length) {
    const line = lines[i].trim()
    i++
    if (!line || line.startsWith('{') || line.startsWith('}') || !line.includes('/')) continue
    // Peek for a verbose JSON object starting on the next non-empty line.
    let j = i
    while (j < lines.length && !lines[j].trim()) j++
    if (j < lines.length && lines[j].trim().startsWith('{')) {
      let depth = 0
      let inStr = false
      let esc = false
      const chunk = []
      while (j < lines.length) {
        const l = lines[j]
        chunk.push(l)
        for (let k = 0; k < l.length; k++) {
          const c = l[k]
          if (inStr) {
            if (esc) esc = false
            else if (c === '\\') esc = true
            else if (c === '"') inStr = false
          } else if (c === '"') inStr = true
          else if (c === '{') depth++
          else if (c === '}') depth--
        }
        j++
        if (depth <= 0) break
      }
      i = j
      try {
        const obj = JSON.parse(chunk.join('\n'))
        const { providerID, modelID } = splitModelId(line)
        out.push({
          providerID: obj.providerID || providerID,
          id: obj.id || modelID,
          name: obj.name || modelID,
          status: obj.status,
          enabled: obj.enabled,
          limit: obj.limit,
          variants: obj.variants,
        })
        continue
      } catch {
        // fall through to plain header entry
      }
    }
    const { providerID, modelID } = splitModelId(line)
    if (providerID && modelID) out.push({ providerID, id: modelID, name: modelID })
  }
  return out
}

/** Parse ACP `configOptions` array into normalized raw model entries plus the current default model. */
export function parseAcpModelOptions(configOptions) {
  if (!Array.isArray(configOptions)) return { models: [], currentModel: '' }
  const opt = configOptions.find((c) => c && (c.id === 'model' || c.category === 'model'))
  if (!opt) return { models: [], currentModel: '' }
  const currentModel = typeof opt.currentValue === 'string' ? opt.currentValue.trim() : ''
  const models = []
  for (const item of Array.isArray(opt.options) ? opt.options : []) {
    const value = typeof item?.value === 'string' ? item.value.trim() : ''
    if (!value) continue
    const { providerID, modelID } = splitModelId(value)
    models.push({
      providerID,
      id: modelID || value,
      qualifiedId: value,
      name: typeof item?.name === 'string' && item.name.trim() ? item.name.trim() : value,
    })
  }
  return { models, currentModel }
}

/** Parse ACP `configOptions` array for a reasoning-effort / variant option. */
export function parseAcpEffortOptions(configOptions) {
  if (!Array.isArray(configOptions)) return { configId: '', currentEffort: '', efforts: [] }
  const opt = configOptions.find((c) => c && (
    c.id === 'variant' ||
    c.id === 'reasoning_effort' ||
    c.id === 'thought_level' ||
    c.category === 'reasoning' ||
    c.category === 'effort'
  ))
  if (!opt) return { configId: '', currentEffort: '', efforts: [] }
  const rawCurrent = typeof opt.currentValue === 'string' ? opt.currentValue.trim().toLowerCase() : ''
  const currentEffort = PERSONA_EFFORTS.includes(rawCurrent) ? rawCurrent : ''
  const rawValues = (Array.isArray(opt.options) ? opt.options : [])
    .map((item) => (typeof item?.value === 'string' ? item.value.trim().toLowerCase() : ''))
    .filter(Boolean)
  const set = new Set(rawValues)
  const efforts = PERSONA_EFFORTS.filter((e) => set.has(e))
  return { configId: typeof opt.id === 'string' ? opt.id : '', currentEffort, efforts }
}

/**
 * Normalize raw opencode model records (from `ctx.model.list()`, CLI, or ACP) into:
 *  - `models`: Persona `AgentModel[]` (at most 200, default marked)
 *  - `windows`: Map of `provider\0model`, `provider/model`, and `model` -> context window
 *  - `defaultModel`: the selected default model id (or "")
 */
export function normalizeModels(rawList, defaultModel = '') {
  const list = Array.isArray(rawList) ? rawList : Array.isArray(rawList?.data) ? rawList.data : []
  const wantDefault = typeof defaultModel === 'string' ? defaultModel.trim() : ''
  const windows = new Map()
  const entries = []
  const seen = new Set()

  for (const m of list) {
    if (!m || typeof m !== 'object') continue
    if (m.enabled === false) continue
    const rawId = typeof m.id === 'string' ? m.id.trim() : ''
    const providerID = typeof m.providerID === 'string' ? m.providerID.trim() : ''
    const qualified = typeof m.qualifiedId === 'string' && m.qualifiedId.trim()
      ? m.qualifiedId.trim()
      : qualifyModelId(providerID, rawId)
    if (!qualified || seen.has(qualified)) continue
    seen.add(qualified)

    const ctxLimit = m.limit?.context
    if (typeof ctxLimit === 'number' && ctxLimit > 0) {
      if (providerID && rawId) windows.set(`${providerID}\0${rawId}`, ctxLimit)
      windows.set(qualified, ctxLimit)
      if (rawId) windows.set(rawId, ctxLimit)
    }

    const efforts = extractEfforts(m.variants)
    const displayName = typeof m.name === 'string' && m.name.trim()
      ? (providerID && !m.name.includes('/') ? `${providerID}/${m.name.trim()}` : m.name.trim())
      : qualified
    const isDefault = wantDefault !== '' && (
      qualified === wantDefault ||
      rawId === wantDefault ||
      (providerID && `${providerID}:${rawId}` === wantDefault)
    )
    entries.push({
      id: qualified,
      name: displayName,
      ...(isDefault ? { default: true } : {}),
      ...(efforts.length ? { efforts } : {}),
      _deprecated: m.status === 'deprecated',
    })
  }

  // Sort default first, non-deprecated before deprecated, preserving original relative order otherwise.
  entries.sort((a, b) => Number(Boolean(b.default)) - Number(Boolean(a.default)) || Number(a._deprecated) - Number(b._deprecated))
  const models = entries.slice(0, MAX_MODELS).map(({ _deprecated, ...rest }) => rest)
  const explicitDefault = models.find((m) => m.default)?.id || wantDefault || ''
  const resolvedDefault = explicitDefault || models[0]?.id || ''
  return { models, windows, defaultModel: resolvedDefault, explicitDefault }
}

/**
 * Resolve a caller-requested model string against the normalized catalog.
 * Returns `{ id, providerID, modelID }` or `null` when input is empty.
 * Throws `RpcError(..., 'bad_request')` when a bare (unqualified) model name is not found or is ambiguous
 * across multiple providers in the catalog.
 */
export function resolveModel(input, catalog = []) {
  const raw = typeof input === 'string' ? input.trim() : ''
  if (!raw) return null
  const list = Array.isArray(catalog) ? catalog : []

  // 1. Exact match on qualified id or display name.
  const exact = list.find((m) => m.id === raw || m.name === raw)
  if (exact) {
    const { providerID, modelID } = splitModelId(exact.id)
    return { id: exact.id, providerID, modelID: modelID || exact.id }
  }

  // 2. Explicit `provider/model` or `provider:model` when matching the catalog or a known provider.
  const knownProviders = new Set(list.map((m) => splitModelId(m.id).providerID).filter(Boolean))
  const split = splitModelId(raw)
  if (split.providerID && split.modelID) {
    const canonical = `${split.providerID}/${split.modelID}`
    const match = list.find((m) => m.id === canonical)
    if (match) {
      const s = splitModelId(match.id)
      return { id: match.id, providerID: s.providerID, modelID: s.modelID }
    }
    if (list.length === 0 || knownProviders.has(split.providerID)) {
      return { id: canonical, providerID: split.providerID, modelID: split.modelID }
    }
  }

  // 3. Bare model ID matched against catalog entries (`<provider>/<bare>`), including bare IDs containing `:` or `/`.
  const matches = list.filter((m) => {
    const { modelID } = splitModelId(m.id)
    return modelID === raw
  })
  if (matches.length === 1) {
    const picked = matches[0]
    const s = splitModelId(picked.id)
    return { id: picked.id, providerID: s.providerID, modelID: s.modelID || picked.id }
  }
  if (matches.length > 1) {
    const alts = matches.map((m) => m.id).join(', ')
    throw new RpcError(`模型 "${raw}" 存在多个 provider 匹配（${alts}），请使用完整的 provider/model 格式`, 'bad_request')
  }

  // 4. Explicit `provider/model` (with `/`) for a provider not yet in the cached catalog.
  if (raw.includes('/') && split.providerID && split.modelID) {
    const canonical = `${split.providerID}/${split.modelID}`
    return { id: canonical, providerID: split.providerID, modelID: split.modelID }
  }

  throw new RpcError(`未知的 opencode 模型 "${raw}"（请使用 provider/model 格式，如 opencode-go/claude-haiku-5-5）`, 'bad_request')
}

/** Build the runtime launcher entry for `runtime.hello` / `runtime.launchers`. */
export function buildLauncher({ name = 'opencode', displayName = 'opencode', models = [], defaultModel = '' } = {}) {
  const selected = models.find((m) => m.default)
  const model = selected?.id || defaultModel || ''
  return {
    name,
    agent: 'opencode',
    display_name: displayName || name,
    ...(model ? { model } : {}),
    models,
  }
}

/** Discover available models by invoking `opencode models --verbose` (no prompt, no token spend). */
export function discoverCliModels({ bin = 'opencode', env = process.env, timeoutMs = DEFAULT_CLI_TIMEOUT_MS, signal } = {}) {
  const run = (args) => new Promise((resolve, reject) => {
    execFile(bin, args, { env, timeout: timeoutMs, maxBuffer: 16 * 1024 * 1024, signal }, (err, stdout, stderr) => {
      if (err) {
        const msg = String(stderr || err.message || err).trim()
        reject(new Error(msg || `${bin} ${args.join(' ')} failed`))
        return
      }
      resolve(stdout)
    })
  })
  return run(['models', '--verbose']).then((out) => {
    const parsed = parseCliModels(out)
    if (parsed.length > 0) return parsed
    return run(['models']).then(parseCliModels)
  }, async (err) => {
    if (signal?.aborted) throw err
    const out = await run(['models'])
    return parseCliModels(out)
  })
}
