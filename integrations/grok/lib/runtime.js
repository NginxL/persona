import { createHash, randomUUID } from 'node:crypto'
import { mkdir, readFile, readdir, rename, rm, rmdir, stat, unlink, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { basename, dirname, join, resolve, sep } from 'node:path'
import { createRuntime, interactionId, page, RpcError, summaryOf } from '../../runtime-sdk/index.js'
import { isAssistantDir, stateDir } from '../../runtime-sdk/lib/identity.js'
import { contextWrapped } from '../../runtime-sdk/lib/compat.js'
import { GrokAcp } from './acp.js'
import { createMcpProxy } from './mcp.js'
import { addItem, blankItems, modelOptions, parseTranscript, toolName, updateItems, visibleText } from './map.js'

const PREFIX = 'grok-chat-'
const HOOKS = { pre_tool_use: [{ matcher: '.*', hookCallbackIds: ['persona-tools'], timeoutS: 600 }] }
const READ_TOOLS = new Set(['read_file', 'list_directory', 'search_files', 'grep', 'glob', 'web_search', 'web_fetch', 'search_tool'])
const WRITE_TOOLS = new Set(['write_file', 'edit_file', 'apply_patch'])
const MODES = new Set(['default', 'acceptEdits', 'auto', 'plan', 'bypassPermissions'])
const validRaw = (raw) => /^[a-f0-9-]{36}$/i.test(raw)
// Conversations started in a terminal are listed for this long after their last update, like the SDK's recent window.
const TERMINAL_RECENT_MS = 7 * 24 * 60 * 60 * 1000

// Match the node's assistant/health directories, including manager-scoped conversations.
export function isConversationDir(cwd, home, env) {
  return /^(assistant|health)(-m-[a-f0-9]+)?$/.test(basename(cwd)) && isAssistantDir(join(dirname(cwd), 'assistant'), home, env)
}

export function createGrokRuntime(options = {}) {
  const home = options.home || homedir()
  const grokHome = options.grokHome || process.env.GROK_HOME || join(home, '.grok')
  const launcher = options.launcher || 'grok-chat'
  // What the apps call it, and the Agent launcher (a Paseo provider running this Grok CLI and account) it runs the
  // assistant for: only what the person setting it up declares, never guessed from names.
  const displayName = typeof options.displayName === 'string' && options.displayName.trim() ? options.displayName.trim() : 'Grok'
  const assistantFor = typeof options.assistantFor === 'string' ? options.assistantFor.trim() : ''
  if (assistantFor && (!/^[A-Za-z_][A-Za-z0-9_-]*$/.test(assistantFor) || assistantFor === launcher)) throw new Error('Unsupported Grok assistant-for launcher')
  const launcherEntry = (model, models) => ({ name: launcher, agent: 'grok', display_name: displayName, ...(assistantFor ? { assistant_for: assistantFor } : {}),
    ...(model ? { model } : {}), ...(models ? { models } : {}) })
  const defaultModel = options.model || ''
  const defaultEffort = options.effort || ''
  const defaultMode = options.mode || 'default'
  if (!MODES.has(defaultMode)) throw new Error('Unsupported Grok permission mode')
  const log = options.log || (() => {})
  const sessions = new Map()
  const probes = new Map()
  let catalog = []
  let selectedModel = defaultModel
  let scanning
  let rescanning
  const acp = options.acp || new GrokAcp({ bin: options.bin, args: options.args,
    env: { ...process.env, GROK_HOME: grokHome }, log, request: handleRequest })
  acp.request = handleRequest
  const mcp = createMcpProxy({
    tools: async (s) => { s.personaTools = await rt.tools({ assistant: s.assistant }); return s.personaTools },
    call: async (s, name, args) => {
      if (!s.raw || !sessions.has(s.id)) throw new Error('Conversation is not available')
      if (s.mode === 'plan') return { content: [{ type: 'text', text: 'Persona tools are unavailable in read-only mode.' }], is_error: true }
      return rt.callTool({ name, args, session_id: s.id, assistant: s.assistant }, { signal: s.abort?.signal })
    },
  })

  const rt = createRuntime({ listSessions, items, cursor, item, image, start, send, interrupt, rename: renameSession, close, models, modes, check }, {
    runtime: 'grok-chat', name: 'Grok', agent: 'grok', launcher, launcherModel: defaultModel, launchers: [launcherEntry(defaultModel)],
    assistant: true, answerFirst: 'phone', modelSwitch: true, readonly: true,
    socketPath: options.socketPath, connect: options.connect, log, refreshMs: options.refreshMs || 10_000,
    pushDelayMs: options.pushDelayMs ?? 50, retryMs: options.retryMs,
  })
  acp.on('notification', (method, params) => {
    if (method !== 'session/update') return
    const probe = probes.get(params.sessionId)
    if (probe) {
      if (params.update?.sessionUpdate === 'agent_message_chunk') probe.text += params.update.content?.text || ''
      return
    }
    const s = sessions.get(PREFIX + params.sessionId)
    if (!s || s.loading) return
    const u = params.update
    if (u?.sessionUpdate === 'config_option_update') configuration(s, u)
    updateItems(s, u)
    s.updated = new Date().toISOString()
    if (u?.sessionUpdate === 'agent_message_chunk') rt.stream(s.id, s.segment?.kind === 'text' ? s.segment.text : '')
    if (u?.sessionUpdate === 'tool_call') rt.streamEnd(s.id)
    push(s)
  })

  function createState(raw, cwd, restored = false) {
      // A rediscovered conversation belongs to the assistant only in its directories (or by its metadata, readMetadata).
    const s = { ...blankItems(), raw, id: PREFIX + raw, cwd, assistant: !restored || isConversationDir(cwd, home, options.env || process.env), title: 'Grok', mode: restored ? 'default' : defaultMode, modeKnown: !restored,
      model: '', effort: '', updated: new Date().toISOString(), history: `grok:${raw}`,
      restored, loaded: 0, running: false, tail: Promise.resolve(), queued: 0, epoch: 0, seen: new Set(), asks: new Set(), approved: new Set() }
    sessions.set(s.id, s)
    rt.sessionsChanged()
    return s
  }

  function requireSession(id) {
    const s = sessions.get(id)
    if (!s) throw new RpcError('Grok conversation is unavailable', 'not_found')
    return s
  }

  // Grok CLI records the conversations its running processes hold in active_sessions.json. One open in a terminal
  // is shown read-only: a second writer through ACP would interleave with the terminal's own turns.
  let terminalHeld = new Set()
  async function readTerminalHeld() {
    const held = new Set()
    try {
      const list = JSON.parse(await readFile(join(grokHome, 'active_sessions.json'), 'utf8'))
      for (const entry of Array.isArray(list) ? list : []) {
        if (!validRaw(entry?.session_id) || !Number.isInteger(entry.pid) || entry.pid <= 0 || entry.pid === acp.child?.pid) continue
        try { process.kill(entry.pid, 0) } catch (error) { if (error.code !== 'EPERM') continue }
        held.add(entry.session_id)
      }
    } catch (error) { if (error.code !== 'ENOENT' && !(error instanceof SyntaxError)) log(`Grok active sessions read: ${error.code || error.name}`) }
    terminalHeld = held
  }
  const inTerminal = (s) => terminalHeld.has(s.raw) && s.loaded !== acp.generation
  async function requireWritable(id) {
    await readTerminalHeld()
    const s = requireSession(id)
    if (inTerminal(s)) throw new RpcError('This Grok conversation is open in a terminal. Continue it there, or exit Grok in that terminal first.', 'terminal_open')
    return s
  }

  function configuration(s, value) {
    const offered = value?.models?.availableModels || value?._meta?.modelState?.availableModels
    if (offered) catalog = offered
    s.model = value?.models?.currentModelId || s.model
    for (const option of value?.configOptions || []) {
      if (option.id === 'model') s.model = option.currentValue
      if (option.id === 'reasoning_effort') s.effort = option.currentValue
    }
    selectedModel ||= s.model
    rt.launchersChanged([launcherEntry(defaultModel || selectedModel, modelOptions(catalog, defaultModel || selectedModel))])
  }

  const sessionDir = (s) => s.file ? dirname(s.file) : join(grokHome, 'sessions', encodeURIComponent(s.cwd), s.raw)
  const inputKey = (s) => createHash('sha256').update(JSON.stringify(s.items.filter((i) => i.role === 'user' &&
    (i.kind === 'image' || (i.kind === 'text' && i.text))).map((i) => [i.kind, i.text, i.image?.data]))).digest('hex')

  async function saveMetadata(s) {
    if (s.probe) return
    // Provider-owned metadata shares the native session's lifecycle. Nothing is written to Persona's server state directory.
    const data = JSON.stringify({ version: 1, session_id: s.raw, assistant: s.assistant,
      ...(s.modeKnown ? { mode: s.mode } : {}), ...(s.failure ? { failure: { message: s.failure.slice(0, 8192), input: s.failureKey } } : {}) })
    s.metadataLoaded = true
    s.saving = (s.saving || Promise.resolve()).catch(() => {}).then(async () => {
      const dir = sessionDir(s)
      await mkdir(dir, { recursive: true, mode: 0o700 })
      const file = join(dir, 'persona-runtime.json')
      const temporary = `${file}.${randomUUID()}.tmp`
      try { await writeFile(temporary, data, { mode: 0o600 }); await rename(temporary, file) }
      finally { await unlink(temporary).catch(() => {}) }
    })
    return s.saving
  }

  async function readMetadata(s) {
    if (s.metadataLoaded) return
    try {
      const data = await readFile(join(sessionDir(s), 'persona-runtime.json'), 'utf8')
      if (data.length > 32 * 1024) throw new Error('Oversized Grok runtime metadata')
      const value = JSON.parse(data)
      if (value.version !== 1 || value.session_id !== s.raw) throw new Error('Invalid Grok runtime metadata')
      if (MODES.has(value.mode)) { s.mode = value.mode; s.modeKnown = true }
      s.assistant = value.assistant === true
      if (typeof value.failure?.message === 'string' && value.failure.input === inputKey(s)) {
        s.failure = value.failure.message
        s.failureKey = value.failure.input
        recordFailure(s)
      }
      s.metadataLoaded = true
    } catch (error) {
      if (error.code !== 'ENOENT') log(`Grok runtime metadata read: ${error.code || error.name}`)
      // A conversation created before metadata was supported has no verified permission setting. Keep default approval.
    }
  }

  async function scan() {
    // A caller arriving mid-scan waits for one more scan, which sees what changed after the running one began.
    if (scanning) return (rescanning ||= scanning.then(() => { rescanning = null; return scan() }))
    scanning = (async () => {
      await readTerminalHeld()
      let dirs = []
      try { dirs = await readdir(join(grokHome, 'sessions'), { withFileTypes: true }) } catch (error) { if (error.code !== 'ENOENT') throw error }
      for (const dir of dirs) {
        if (!dir.isDirectory()) continue
        let cwd
        try { cwd = decodeURIComponent(dir.name) } catch { continue }
        // Every working directory: the assistant's, and the ones where Grok was started in a terminal.
        const conversations = isConversationDir(cwd, home, options.env || process.env)
        const base = join(grokHome, 'sessions', dir.name)
        let entries
        try { entries = await readdir(base, { withFileTypes: true }) } catch (error) { if (error.code === 'ENOENT') continue; throw error }
        for (const entry of entries) {
          if (!entry.isDirectory() || !validRaw(entry.name) || probes.has(entry.name)) continue
          try {
            const summary = JSON.parse(await readFile(join(base, entry.name, 'summary.json'), 'utf8'))
            if (summary.info?.cwd !== cwd || summary.info?.id !== entry.name) continue
            // Background CLI runs and child agents are not interactive terminal conversations.
            if (!conversations && (summary.session_kind === 'headless' || summary.session_kind === 'subagent')) continue
            // Older terminal conversations are left unread until they are updated again.
            if (!conversations && !sessions.has(PREFIX + entry.name) && !terminalHeld.has(entry.name) &&
              !(Date.now() - Date.parse(summary.updated_at) < TERMINAL_RECENT_MS)) continue
            const s = sessions.get(PREFIX + entry.name) || createState(entry.name, cwd, true)
            s.file = join(base, entry.name, 'updates.jsonl')
            if (!s.running) {
              s.title = summary.generated_title || summary.title || summary.session_summary || s.title
              s.model = summary.current_model_id || s.model
              s.effort = summary.reasoning_effort || s.effort
              s.updated = summary.updated_at || s.updated
              // A terminal's turns reach a watching phone as the native transcript grows.
              if (await readTranscript(s) && rt.watching(s.id)) rt.append(s.id, { items: s.items.map(placeholder), fromSeq: 0, toSeq: s.items.length, history: s.history })
              await readMetadata(s)
            }
          } catch (error) { if (error.code !== 'ENOENT' && !(error instanceof SyntaxError)) log(`Grok transcript read: ${error.code || error.name}`) }
        }
      }
      // Native cleanup can remove a whole cwd group between scans. Forget only rediscovered, unowned conversations;
      // a new chat may not have flushed its summary yet, and the assistant keeps its own conversation lifecycle.
      for (const [id, s] of sessions) {
        if (!s.restored || s.assistant) continue
        try { await stat(join(sessionDir(s), 'summary.json')) }
        catch (error) {
          if (error.code !== 'ENOENT') { log(`Grok summary stat: ${error.code || error.name}`); continue }
          // Check after the filesystem await: a turn may have started or queued while the summary was checked.
          if (s.running || s.queued || s.loading || terminalHeld.has(s.raw)) continue
          sessions.delete(id)
          rt.sessionsChanged()
        }
      }
    })().finally(() => { scanning = null })
    return scanning
  }

  async function readTranscript(s) {
    const file = s.file || join(grokHome, 'sessions', encodeURIComponent(s.cwd), s.raw, 'updates.jsonl')
    try {
      const info = await stat(file)
      const stamp = `${info.mtimeMs}:${info.size}`
      if (stamp === s.stamp) return false
      const data = await readFile(file, 'utf8')
      const parsed = parseTranscript(data)
      // A newly-created chat can be listed before Grok flushes its first response.
      if (!parsed.items.length && s.items.length) return false
      const echoes = new Map()
      for (const i of s.items) if (i.client_msg_id) echoes.set(i.text, [...(echoes.get(i.text) || []), i.client_msg_id])
      for (const i of parsed.items) if (i.role === 'user' && i.kind === 'text' && echoes.get(i.text)?.length) i.client_msg_id = echoes.get(i.text).shift()
      Object.assign(s, parsed, { stamp, file, history: `grok:${s.raw}:${createHash('sha256').update(data).digest('hex').slice(0, 16)}` })
      if (s.failure && s.failureKey !== inputKey(s)) { s.failure = null; s.failureKey = null }
      if (s.failure) recordFailure(s)
      return true
    } catch (error) { if (error.code !== 'ENOENT') throw error }
    return false
  }

  function row(s) {
    return { session_id: s.id, cwd: s.cwd, title: s.title, status: s.running || s.queued ? 'working' : s.items.length ? 'done' : 'idle',
      updated_at: s.updated, rev: s.rev, model: s.model, effort: s.effort, ...(s.modeKnown ? { permission_mode: s.mode } : {}),
      last_text: s.lastText, launcher, agent_label: 'Grok', space_id: s.cwd, space_name: basename(s.cwd),
      ...(s.failure ? { turn_error: `API Error: ${s.failure}` } : {}),
      efforts: modelOptions(catalog, s.model).find((m) => m.id === s.model)?.efforts }
  }

  // A conversation a terminal holds stays listed however long ago it was last updated.
  async function listSessions() { await scan(); return rt.recent([...sessions.values()].filter((s) => !s.closed).map(row), { pinned: (r) => r.status === 'working' || terminalHeld.has(sessions.get(r.session_id)?.raw) }) }
  const placeholder = (i) => i.image ? { ...i, image: { mime: i.image.mime, data: '', lazy: true, size: Buffer.from(i.image.data, 'base64').length } } : i
  function push(s) { rt.append(s.id, { items: s.items.map(placeholder), fromSeq: 0, toSeq: s.items.length, history: s.history }); rt.sessionsChanged() }
  async function items(params) { await scan(); const s = requireSession(params.session_id); return page(s.items.map(placeholder), params, { history: s.history, toSeq: s.items.length }) }
  async function cursor(id) { const s = requireSession(id); return { to_seq: s.items.length, history: s.history } }
  async function item({ session_id, item_id }) { const s = requireSession(session_id); const value = s.items.find((i) => i.id === item_id); if (!value) throw new RpcError('Item not found', 'not_found'); return value }
  async function image(params) { const value = await item(params); if (!value.image) throw new RpcError('Image not found', 'not_found'); return value.image }

  function recordFailure(s) {
    s.lastText = `API Error: ${s.failure}`
    if (s.items.at(-1)?.text !== s.lastText) addItem(s, { role: 'assistant', kind: 'text', text: s.lastText })
    if (!s.history.endsWith(':error')) s.history += ':error'
  }

  async function models() {
    const info = await acp.ready()
    catalog = info?._meta?.modelState?.availableModels || catalog
    selectedModel ||= info?._meta?.modelState?.currentModelId || ''
    return { models: modelOptions(catalog, defaultModel || selectedModel) }
  }

  async function apply(s, { model, effort, mode } = {}, remaining) {
    if (mode && !MODES.has(mode)) throw new RpcError('Unsupported Grok permission mode', 'invalid_params')
    if (model && model !== s.model) {
      configuration(s, await acp.call('session/set_config_option', { sessionId: s.raw, configId: 'model', value: model }, remaining?.()))
      if (s.model !== model) throw new Error('Grok did not apply the selected model')
    }
    if (effort) {
      const offered = modelOptions(catalog, s.model).find((m) => m.id === s.model)?.efforts || []
      if (!offered.includes(effort)) throw new Error(`The Grok model does not offer reasoning effort ${effort}`)
      if (effort !== s.effort) configuration(s, await acp.call('session/set_config_option', { sessionId: s.raw, configId: 'reasoning_effort', value: effort }, remaining?.()))
      if (s.effort !== effort) throw new Error('Grok did not apply the selected reasoning effort')
    }
    if (mode) { s.mode = mode; s.modeKnown = true }
    await saveMetadata(s)
  }

  async function check(params) {
    if (!params.cwd || !params.prompt) throw new Error('Grok checks require a temporary directory and prompt')
    const started = Date.now()
    const deadline = started + (options.checkTimeoutMs ?? 75_000)
    const remaining = () => {
      if (Date.now() >= deadline) throw new Error('Grok availability check timed out')
      return deadline - Date.now()
    }
    const cwd = resolve(params.cwd)
    let s
    try {
      await acp.ready()
      const value = await acp.call('session/new', { cwd, mcpServers: [], _meta: { 'x.ai/hooks': HOOKS, yoloMode: false } }, remaining())
      if (!validRaw(value.sessionId)) throw new Error('Grok returned an invalid check session ID')
      s = { raw: value.sessionId, cwd, probe: true, model: '', effort: '', text: '' }
      probes.set(s.raw, s)
      configuration(s, value)
      await apply(s, { model: defaultModel, effort: defaultEffort, mode: 'plan' }, remaining)
      const result = await acp.call('session/prompt', { sessionId: s.raw, prompt: [{ type: 'text', text: params.prompt }] }, remaining())
      if (result?.stopReason !== 'end_turn') throw new Error(`Grok availability check ended with ${result?.stopReason || 'no stop reason'}`)
      if (!s.text.trim()) throw new Error('Grok availability check returned no answer')
      return { ok: true, agent: 'grok', model: s.model, answer: s.text.trim().slice(0, 400), millis: Date.now() - started }
    } catch (error) {
      return { ok: false, agent: 'grok', model: s?.model, error: error.message, millis: Date.now() - started }
    } finally {
      if (s) {
        acp.notify('session/cancel', { sessionId: s.raw })
        let closed = false
        try { await acp.call('session/close', { sessionId: s.raw }, 5000); closed = true } catch { log('Grok check session could not be closed') }
        probes.delete(s.raw)
        // Delete only this successfully closed probe's own native record. Other chats in the directory are untouched.
        if (closed) {
          const dir = sessionDir(s)
          await rm(dir, { recursive: true, force: true })
          await rmdir(dirname(dir)).catch(() => {})
        }
      }
    }
  }

  async function loaded(s) {
    await acp.ready()
    if (s.loaded === acp.generation) return
    s.loading = true
    try {
      configuration(s, await acp.call('session/load', { sessionId: s.raw, cwd: s.cwd, mcpServers: await mcp.forSession(s), _meta: { 'x.ai/hooks': HOOKS, yoloMode: true } }))
      s.loaded = acp.generation
      await readTranscript(s)
    } finally { s.loading = false }
  }

  async function start(params) {
    const cwd = resolve(params.cwd || join(stateDir(home), 'assistant'))
    const value = await acp.call('session/new', { cwd, mcpServers: [], _meta: { 'x.ai/hooks': HOOKS, yoloMode: true } })
    if (!validRaw(value.sessionId)) throw new Error('Grok returned an invalid session ID')
    const s = createState(value.sessionId, cwd)
    s.assistant = params.assistant === true || isConversationDir(cwd, home, options.env || process.env)
    configuration(s, value)
    await loaded(s)
    await apply(s, { model: params.model || defaultModel, effort: params.effort || defaultEffort, mode: params.permission_mode })
    rt.sessionsChanged()
    if (params.prompt) await send({ ...params, session_id: s.id, text: params.prompt })
    return { session: row(s) }
  }

  async function send(params) {
    const s = await requireWritable(params.session_id)
    if (s.closed) throw new RpcError('Grok conversation is closed', 'closed')
    if (params.client_msg_id && s.seen.has(params.client_msg_id)) return { queued: s.running || s.queued > 0 }
    await loaded(s)
    const wasBusy = s.running || s.queued > 0
    if (params.client_msg_id) s.seen.add(params.client_msg_id)
    while (s.seen.size > 1000) s.seen.delete(s.seen.values().next().value)
    const epoch = s.epoch
    s.queued++
    s.tail = s.tail.catch(() => {}).then(async () => {
      s.queued--
      if (epoch !== s.epoch) { if (params.client_msg_id) s.seen.delete(params.client_msg_id); return }
      s.running = true
      s.abort = new AbortController()
      s.memoryDir = params.memory_dir
      s.segment = null
      s.lastText = ''
      s.echo = { text: params.text || '', id: params.client_msg_id }
      s.failure = null
      s.failureKey = null
      addItem(s, { role: 'user', kind: 'text', text: params.text || '', ...(params.client_msg_id ? { client_msg_id: params.client_msg_id } : {}) })
      for (const i of params.images || []) if (i.data) addItem(s, { role: 'user', kind: 'image', image: { mime: i.media_type || i.mime || 'image/png', data: i.data } })
      rt.streamEnd(s.id)
      push(s)
      try {
        await apply(s, { model: params.model, effort: params.effort, mode: params.mode || params.permission_mode })
        s.personaTools = await rt.tools({ assistant: s.assistant })
        const context = params.context ? (contextWrapped(params.context) ? params.context + '\n\n' : `<persona-context>\n${params.context}\n</persona-context>\n\n`) : ''
        const prompt = [{ type: 'text', text: context + (params.text || '') }, ...(params.images || []).filter((i) => i.data).map((i) => ({ type: 'image', data: i.data, mimeType: i.media_type || i.mime || 'image/png' }))]
        await acp.call('session/prompt', { sessionId: s.raw, prompt }, 0)
      } catch (error) {
        if (!s.abort.signal.aborted) { s.failure = error.message; s.failureKey = inputKey(s) }
      } finally {
        s.running = false
        s.segment = null
        s.echo = null
        s.abort.abort()
        for (const abort of s.asks) abort.abort()
        s.approved.clear()
        s.updated = new Date().toISOString()
        await readTranscript(s)
        if (s.failure) recordFailure(s)
        await saveMetadata(s)
        rt.streamEnd(s.id)
        push(s)
      }
    })
    // A turn's error is part of its transcript, and cannot become an unhandled rejection in the companion.
    s.tail.catch((error) => { log(`Grok turn cleanup: ${error.code || error.name}`); s.running = false; rt.sessionsChanged() })
    rt.sessionsChanged()
    return wasBusy ? { queued: true } : {}
  }

  async function interrupt({ session_id }) {
    const s = requireSession(session_id)
    s.epoch++
    s.abort?.abort()
    for (const abort of s.asks) abort.abort()
    if (!s.running && !s.queued) return { settled: true }
    acp.notify('session/cancel', { sessionId: s.raw })
    let timer
    try { await Promise.race([s.tail, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('Grok has not stopped yet')), 15_000); timer.unref() })]) } finally { clearTimeout(timer) }
    return { settled: true }
  }
  async function renameSession({ session_id, title }) {
    const name = typeof title === 'string' ? title.trim() : ''
    if (!name) throw new RpcError('A conversation title is required', 'invalid_params')
    await scan()
    const s = await requireWritable(session_id)
    // The native operation persists a manual title, so refreshes and later turns keep the user's name.
    const result = await acp.call('_x.ai/session/rename', { sessionId: s.raw, title: name, cwd: s.cwd })
    if (result?.success !== true) throw new Error('Grok did not rename the conversation')
    s.title = name
    rt.sessionsChanged()
  }
  async function close(params) {
    await requireWritable(params.session_id)
    await interrupt(params)
    const s = requireSession(params.session_id)
    await acp.call('session/close', { sessionId: s.raw })
    s.closed = true
    s.loaded = 0
    rt.sessionsChanged()
  }
  async function modes({ session_id, permission_mode, model, effort }) { const s = await requireWritable(session_id); await loaded(s); await apply(s, { mode: permission_mode, model, effort }); rt.sessionsChanged() }

  async function ask(s, name, input, key) {
    const abort = new AbortController()
    const timer = setTimeout(() => abort.abort(), 540_000)
    s.asks.add(abort)
    try {
      const answer = await rt.ask({ id: interactionId('grok', s.id, 'permission', key), kind: 'permission', session_id: s.id,
        tool_name: toolName(name), tool_input: input, summary: summaryOf(name, input) }, { signal: abort.signal, timeoutSeconds: 530 })
      return answer?.allow === true
    } catch { return false } finally { clearTimeout(timer); s.asks.delete(abort) }
  }

  async function handleRequest(method, params) {
    const s = sessions.get(PREFIX + params?.sessionId)
    const deny = { decision: 'deny', reason: 'Persona could not authorize this tool.' }
    if (method === '_x.ai/hooks/run' || method === 'x.ai/hooks/run') {
      if (!s || s.abort?.signal.aborted) return deny
      const name = params.toolName || ''
      const input = params.toolInput || {}
      const command = input.command || ''
      for (const t of s.personaTools || []) {
        if (command && t.shell?.some((word) => word && command.includes(word))) return { decision: 'deny', reason: `Use the Persona tool ${t.name} for this command.` }
      }
      const details = s.tools.get(params.toolUseId)?._meta?.['x.ai/tool']
      const readOnly = details?.read_only === true || READ_TOOLS.has(name)
      if (s.mode === 'plan' && !readOnly) return { decision: 'deny', reason: 'This conversation is read-only.' }
      const target = input.file_path || input.path || input.target_file
      const memoryWrite = WRITE_TOOLS.has(name) && s.memoryDir && typeof target === 'string' && resolve(s.cwd, target).startsWith(resolve(s.memoryDir) + sep)
      const ownTool = name.startsWith('persona__') || name.startsWith('mcp__persona__')
      const allowed = readOnly || ownTool || memoryWrite || s.mode === 'bypassPermissions' ||
        (s.mode === 'acceptEdits' && WRITE_TOOLS.has(name)) || await ask(s, name, input, params.toolUseId || name)
      if (allowed) { s.approved.add(params.toolUseId); return {} }
      return deny
    }
    if (method === 'session/request_permission') {
      if (!s || s.abort?.signal.aborted) return { outcome: { outcome: 'cancelled' } }
      const call = params.toolCall || {}
      const name = call._meta?.['x.ai/tool']?.name || call.title || ''
      const details = call._meta?.['x.ai/tool'] || s.tools.get(call.toolCallId)?._meta?.['x.ai/tool']
      // The native permission request is a second boundary: a missing or late hook must not make plan mode writable.
      if (s.mode === 'plan' && details?.read_only !== true && !READ_TOOLS.has(name)) return { outcome: { outcome: 'cancelled' } }
      const options = params.options || []
      const allowing = options.filter((o) => o.kind?.startsWith('allow'))
      if (new Set(allowing.map((o) => o.kind)).size < allowing.length) {
        // ACP also carries questions as several options of the same allow kind. Automatic permissions never choose an answer.
        const question = call.title || 'Grok needs your choice'
        const abort = new AbortController()
        const timer = setTimeout(() => abort.abort(), 540_000)
        s.asks.add(abort)
        try {
          const answer = await rt.ask({ id: interactionId('grok', s.id, 'question', call.toolCallId || question), kind: 'question', session_id: s.id,
            tool_name: 'AskUserQuestion', questions: [{ question, options: allowing.map((o) => ({ label: o.name || o.optionId, description: '' })), multi_select: false }] },
          { signal: abort.signal, timeoutSeconds: 530 })
          const selected = allowing.find((o) => (o.name || o.optionId) === answer?.answers?.[question])
          return selected ? { outcome: { outcome: 'selected', optionId: selected.optionId } } : { outcome: { outcome: 'cancelled' } }
        } catch { return { outcome: { outcome: 'cancelled' } } } finally { clearTimeout(timer); s.asks.delete(abort) }
      }
      const allow = options.find((o) => o.kind === 'allow_once')
      const accepted = s.approved.has(call.toolCallId) || (s.mode === 'bypassPermissions') || await ask(s, call.title || 'Tool', call.rawInput || {}, call.toolCallId || 'permission')
      return accepted && allow ? { outcome: { outcome: 'selected', optionId: allow.optionId } } : { outcome: { outcome: 'cancelled' } }
    }
    throw new Error(`Unsupported Grok request: ${method}`)
  }

  return { start: () => rt.start(), stop: async () => { for (const s of sessions.values()) for (const abort of s.asks) abort.abort(); rt.stop(); await acp.stop(); mcp.stop() },
    runtime: rt, driver: { start, send, interrupt, rename: renameSession, models, modes, items, listSessions, check }, sessions }
}
