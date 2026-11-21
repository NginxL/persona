// Pure mapping from opencode's session messages to Persona items (docs/integrations/opencode.md "Mapping"). No I/O: the host
// (opencode.js) hands in the messages (rows of opencode.db's session_message, or what the plugin API's
// session.context returns: the same fields) and the tests feed recorded shapes.
//
// seq: a message's `seq` in opencode's event log. It is stable, unique and growing within a session (it has gaps:
// the log holds other events too), so it is the item's seq as is. A message is rewritten in place while it runs (its
// tool calls finish one by one), so the host pushes it again until the turn is over; ids therefore come from the
// position of a part in the message and never move when a result arrives: part i is `<seq>#<2i>`, its result
// `<seq>#<2i+1>`.

import { appToolName, clipInput, snippet } from '../../runtime-sdk/index.js'
import { contextWrapped, SNAPSHOT_RE } from '../../runtime-sdk/lib/compat.js'

// opencode's tools under the names the apps draw (the SDK maps read / write / edit / grep / glob / bash itself).
const OWN_TOOLS = { shell: 'Bash', subagent: 'Agent', task: 'Agent', websearch: 'WebSearch', webfetch: 'WebFetch', skill: 'Skill', question: 'AskUserQuestion', todowrite: 'TodoWrite' }

/** Strip attribute-less `<persona-context>` snapshots from displayed user text while preserving attributed blocks. */
export function visibleUserText(text) {
  if (typeof text !== 'string') return ''
  SNAPSHOT_RE.lastIndex = 0
  if (!SNAPSHOT_RE.test(text)) return text
  SNAPSHOT_RE.lastIndex = 0
  return text.replace(SNAPSHOT_RE, '').trim()
}

/** Prepend a live snapshot context block before user text for the model. */
export function promptWithContext(text, context) {
  const body = typeof text === 'string' ? text : ''
  const ctx = typeof context === 'string' ? context.trim() : ''
  if (!ctx) return body
  const block = contextWrapped(ctx) ? ctx : `<persona-context>\n${ctx}\n</persona-context>`
  return body ? `${block}\n\n${body}` : block
}

/** Tool name the apps render. */
export const toolName = (name) => appToolName(name, OWN_TOOLS)

const dropUndefined = (o) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined && v !== null))

/** An opencode tool call's input in the shape the apps read for that tool (Claude's argument names). */
export function toolInput(name, input) {
  const a = input && typeof input === 'object' ? input : {}
  switch (name) {
    case 'shell':
    case 'bash':
      return clipInput(dropUndefined({ command: a.command, description: a.description, timeout: a.timeout }))
    case 'read':
      return clipInput(dropUndefined({ file_path: a.path ?? a.filePath ?? a.file_path, offset: a.offset, limit: a.limit }))
    case 'write':
      return clipInput(dropUndefined({ file_path: a.path ?? a.filePath ?? a.file_path, content: a.content }))
    case 'edit':
      return clipInput(dropUndefined({ file_path: a.path ?? a.filePath ?? a.file_path, old_string: a.oldString ?? a.old_string, new_string: a.newString ?? a.new_string, replace_all: a.replaceAll ?? a.replace_all }))
    case 'grep':
    case 'glob':
      return clipInput(dropUndefined({ pattern: a.pattern, path: a.path, glob: a.include ?? a.glob }))
    case 'webfetch':
      return clipInput(dropUndefined({ url: a.url, format: a.format }))
    case 'websearch':
      return clipInput(dropUndefined({ query: a.query }))
    case 'subagent':
    case 'task':
      return clipInput(dropUndefined({ description: a.description, prompt: a.prompt, subagent_type: a.agent ?? a.subagent_type }))
    default:
      return clipInput(a)
  }
}

// The finish reasons that stop a turn short without an error, in the words DSH uses for the same (dsh/lib/map.js).
const FINISH_NOTES = { length: '输出达到上限，这一轮停了', 'content-filter': '这一轮被拦下了' }

const ts = (ms) => new Date(typeof ms === 'number' && ms > 0 ? ms : Date.now()).toISOString()

/** The text of a tool's result: its text parts, files named, or the error. */
export function resultText(state) {
  if (state?.status === 'error' || (state?.error && state.status !== 'completed')) {
    const e = state.error
    const text = typeof e === 'string' ? e : e?.message ?? (e ? JSON.stringify(e) : '')
    const out = textParts(state.content)
    return [out, text].filter(Boolean).join('\n') || '出错了'
  }
  return textParts(state?.content)
}

function textParts(content) {
  const out = []
  for (const c of Array.isArray(content) ? content : []) {
    if (c?.type === 'text' && typeof c.text === 'string') out.push(c.text)
    else if (c?.type === 'file') out.push(`［文件 ${c.name ?? c.uri ?? ''}］`)
  }
  return out.join('\n')
}

const errorText = (e) => (typeof e === 'string' ? e : e?.message ?? '')

/**
 * The items of one message at seq. Images are placeholders (lazy): their data comes from `image`. Messages that are
 * not for people (the model's instructions, injected reminders, model / agent switches) give none.
 * @param {object} msg - { id, type, time, ...fields }
 * @param {number} seq
 */
export function messageItems(msg, seq, { clientMsgIdFor, userOrdinal } = {}) {
  const out = []
  const at = ts(msg?.time?.created)
  const add = (n, it) => out.push({ id: `${seq}#${n}`, seq, ts: at, ...it })
  switch (msg?.type) {
    case 'user': {
      const shown = visibleUserText(msg.text)
      if (shown) {
        const cm = typeof userOrdinal === 'number' ? clientMsgIdFor?.(userOrdinal, shown) : undefined
        add(0, { role: 'user', kind: 'text', text: shown, ...(cm ? { client_msg_id: cm } : {}) })
      }
      ;(msg.files ?? []).forEach((f, i) => {
        if (typeof f?.mime === 'string' && f.mime.startsWith('image/')) add(1 + i, { role: 'user', kind: 'image', image: { mime: f.mime, data: '', lazy: true } }) // bytes: session.image
        else add(1 + i, { role: 'user', kind: 'text', text: `［附件 ${f?.name ?? f?.mime ?? ''}］` })
      })
      break
    }
    case 'assistant': {
      const parts = msg.content ?? []
      parts.forEach((p, i) => {
        if (p?.type === 'reasoning' && p.text) add(2 * i, { role: 'assistant', kind: 'thinking', text: p.text })
        else if (p?.type === 'text' && p.text) add(2 * i, { role: 'assistant', kind: 'text', text: p.text })
        else if (p?.type === 'tool') {
          const state = p.state ?? {}
          add(2 * i, { role: 'assistant', kind: 'tool_use', tool_name: toolName(p.name), tool_use_id: p.id, tool_input: toolInput(p.name, state.input) })
          if (state.status === 'completed' || state.status === 'error') {
            add(2 * i + 1, { role: 'user', kind: 'tool_result', tool_use_id: p.id, is_error: state.status === 'error', text: resultText(state) })
          }
        }
      })
      if (msg.finish === 'error' || msg.error) {
        if (msg.error?.type === 'aborted') add(2 * parts.length, { role: 'system', kind: 'notice', text: '已中断' })
        else add(2 * parts.length, { role: 'system', kind: 'notice', text: errorText(msg.error) ? `出错了：${snippet(errorText(msg.error))}` : '出错了' })
      } else if (FINISH_NOTES[msg.finish]) {
        // A turn the model did not end itself: say why, or the phone shows half a sentence (or nothing) as the answer.
        add(2 * parts.length, { role: 'system', kind: 'notice', text: FINISH_NOTES[msg.finish] })
      }
      break
    }
    case 'compaction':
      if (msg.status !== 'pending') add(0, { role: 'system', kind: 'notice', text: '已压缩上下文' })
      break
    case 'shell': {
      // A command the user ran with ! in opencode: a Bash call and its output.
      const id = `shell-${msg.id ?? seq}`
      add(0, { role: 'assistant', kind: 'tool_use', tool_name: 'Bash', tool_use_id: id, tool_input: { command: msg.command } })
      if (msg.status && msg.status !== 'running') {
        add(1, { role: 'user', kind: 'tool_result', tool_use_id: id, is_error: typeof msg.exit === 'number' && msg.exit !== 0, text: msg.output?.output ?? '' })
      }
      break
    }
    case 'idle':
      // The end of a turn; only a turn that did not finish says so (an assistant message with an error says it too).
      if (msg.outcome === 'interrupted') add(0, { role: 'system', kind: 'notice', text: '已中断' })
      break
    // system, synthetic, model-switched, agent-switched, location-switched: not in the chat
  }
  return out
}

/** The image the item at index of a user message stands for ({ mime, data }), undefined for others. */
export function imageOf(msg, index) {
  if (msg?.type !== 'user') return undefined
  const f = (msg.files ?? [])[index - 1]
  if (!f || typeof f.mime !== 'string' || !f.mime.startsWith('image/') || typeof f.data !== 'string') return undefined
  return { mime: f.mime, data: f.data }
}

const MEDIA_REF = /^media:([0-9a-f]{64})$/
// Base64 takes 4/3 the raw size; leave 1 KiB of the runtime protocol's 64 MiB line for the JSON envelope.
const MAX_BLOB_BYTES = (64 * 1024 * 1024 - 1024) * 3 / 4

/**
 * Load an opencode 2.x blob-backed FileRef image (`media:<sha256>`) via `blob_ref` + `blob`,
 * enforcing session/message/seq scoping and the raw-size limit before base64 encoding.
 */
export async function loadBlobImage(queryRows, { sessionId, msg, seq, idx }) {
  const f = msg?.type === 'user' ? msg.files?.[idx - 1] : undefined
  const ref = typeof f?.mime === 'string' && f.mime.startsWith('image/') && typeof f.uri === 'string'
    ? MEDIA_REF.exec(f.uri)
    : null
  if (!ref) return undefined
  const [r] = await queryRows(
    `select b.data from blob b join blob_ref r on r.hash = b.hash
     where b.hash = ? and r.session_id = ? and r.message_id = ? and r.seq = ?`,
    ref[1], sessionId, msg.id, seq,
  )
  const data = r?.data
  if (!(data instanceof Uint8Array) || data.byteLength > MAX_BLOB_BYTES) return undefined
  const bytes = Buffer.isBuffer(data) ? data : Buffer.from(data.buffer, data.byteOffset, data.byteLength)
  return { mime: f.mime, data: bytes.toString('base64') }
}

/** Parse a session_message row (data is JSON) into the message shape messageItems reads. */
export function fromRow(row) {
  let data
  try {
    data = JSON.parse(row.data)
  } catch {
    return { id: row.id, type: row.type }
  }
  return { ...data, id: row.id, type: row.type }
}

/** The last thing the assistant said in the messages (oldest first), one line (the list's last_text). */
export function lastText(messages) {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i]
    if (m?.type !== 'assistant') continue
    for (let j = (m.content ?? []).length - 1; j >= 0; j--) {
      const p = m.content[j]
      if (p?.type === 'text' && p.text) return snippet(p.text)
    }
  }
  return ''
}

/** What the model saw in an assistant message: input and cached tokens (opencode counts reasoning apart). */
export function contextTokens(msg) {
  const t = msg?.tokens
  if (!t) return 0
  return (t.input ?? 0) + (t.cache?.read ?? 0) + (t.cache?.write ?? 0)
}

/** The live text of an assistant message being generated: its text parts so far, in order. */
export function liveText(texts) {
  return [...texts.entries()].sort((a, b) => a[0] - b[0]).map(([, t]) => t).join('')
}

/** A permission request as the apps draw it: the Claude-style tool name, input and one line. */
export function permissionCard(req) {
  const action = String(req?.action ?? '')
  const resources = Array.isArray(req?.resources) ? req.resources.map(String) : []
  const first = resources[0] ?? ''
  let input
  switch (action) {
    case 'shell':
    case 'bash':
      input = { command: first }
      break
    case 'read':
    case 'write':
    case 'edit':
      input = { file_path: first }
      break
    case 'webfetch':
      input = { url: first }
      break
    default:
      input = resources.length ? { resources } : {}
  }
  return { tool_name: toolName(action), tool_input: input, summary: snippet(first || action) }
}

/** Map an ACP `session/request_permission` payload into a Persona permission card. */
export function acpPermissionCard(params) {
  const tc = params?.toolCall ?? {}
  const rawInput = tc.rawInput && typeof tc.rawInput === 'object' ? tc.rawInput : {}
  const kind = String(tc.kind || '').toLowerCase()
  const title = typeof tc.title === 'string' ? tc.title.trim() : ''
  let rawName = typeof rawInput.tool === 'string' ? rawInput.tool : ''
  if (!rawName) {
    if (kind === 'execute' || typeof rawInput.command === 'string') rawName = 'bash'
    else if (kind === 'edit') rawName = 'edit'
    else if (kind === 'read') rawName = 'read'
    else if (kind === 'search') rawName = 'grep'
    else if (kind === 'fetch') rawName = 'webfetch'
    else rawName = title.split(/\s+/)[0] || kind || 'tool'
  }
  const input = toolInput(rawName, rawInput)
  const summary = snippet(
    rawInput.command || rawInput.file_path || rawInput.filePath || rawInput.path || rawInput.url || title || rawName,
  )
  const options = Array.isArray(params?.options) ? params.options : []
  const hasAlways = options.some((o) => o?.kind === 'allow_always')
  return {
    raw_name: rawName,
    tool_name: toolName(rawName),
    tool_input: input,
    summary,
    ...(hasAlways ? { always_label: `总是允许 ${toolName(rawName)}` } : {}),
  }
}

const DATA_URI_RE = /^data:([^;,]+);base64,([A-Za-z0-9+/=\s]+)$/

/** Decode a data-URI image from an opencode 1.x `file` part (`{ mime, data }`). */
export function v1ImageOf(parts, index) {
  const p = Array.isArray(parts) ? parts[index] : undefined
  if (!p || p.type !== 'file') return undefined
  const mime = typeof p.mime === 'string' ? p.mime : ''
  if (!mime.startsWith('image/')) return undefined
  if (typeof p.data === 'string' && p.data) return { mime, data: p.data }
  if (typeof p.url === 'string') {
    const m = DATA_URI_RE.exec(p.url)
    if (m) return { mime: m[1] || mime, data: m[2].replace(/\s+/g, '') }
  }
  return undefined
}

function v1ToolResultText(state) {
  if (!state || typeof state !== 'object') return ''
  if (state.status === 'error' || (state.error && state.status !== 'completed')) {
    const e = state.error
    const errText = typeof e === 'string' ? e : e?.message ?? (e ? JSON.stringify(e) : '')
    const out = typeof state.output === 'string' ? state.output : textParts(state.content)
    return [out, errText].filter(Boolean).join('\n') || '出错了'
  }
  if (typeof state.output === 'string') return state.output
  return textParts(state.content)
}

/**
 * Convert opencode 1.x `message` + `part` rows (ordered oldest-first) into Persona items and derived summary fields.
 * Each message receives a 1-based monotonic seq (`1..messages.length`).
 */
export function v1SessionItems(messages, partsByMessage, { clientMsgIdFor } = {}) {
  const items = []
  let last = ''
  let model = ''
  let provider = ''
  let tokens = 0
  let hasAssistant = false
  let userOrdinal = 0

  for (let mIdx = 0; mIdx < messages.length; mIdx++) {
    const row = messages[mIdx]
    const seq = mIdx + 1
    let msg
    try {
      msg = typeof row.data === 'string' ? JSON.parse(row.data) : (row.data ?? {})
    } catch {
      msg = {}
    }
    const role = msg.role
    const at = ts(msg.time?.created ?? row.time_created)
    const parts = (partsByMessage.get(row.id) ?? []).map((pr) => {
      try {
        return typeof pr.data === 'string' ? JSON.parse(pr.data) : (pr.data ?? {})
      } catch {
        return {}
      }
    })
    const add = (n, it) => items.push({ id: `${seq}#${n}`, seq, ts: at, ...it })

    if (role === 'user') {
      userOrdinal++
      let emittedText = false
      parts.forEach((p, i) => {
        if (p?.type === 'text' && typeof p.text === 'string') {
          const shown = visibleUserText(p.text)
          if (shown) {
            const cm = !emittedText ? clientMsgIdFor?.(userOrdinal, shown) : undefined
            emittedText = true
            add(i, { role: 'user', kind: 'text', text: shown, ...(cm ? { client_msg_id: cm } : {}) })
          }
        } else if (p?.type === 'file') {
          if (typeof p.mime === 'string' && p.mime.startsWith('image/')) {
            add(i, { role: 'user', kind: 'image', image: { mime: p.mime, data: '', lazy: true } })
          } else {
            add(i, { role: 'user', kind: 'text', text: `［附件 ${p.filename ?? p.name ?? p.mime ?? ''}］` })
          }
        }
      })
    } else if (role === 'assistant') {
      hasAssistant = true
      if (msg.modelID) model = msg.modelID
      if (msg.providerID) provider = msg.providerID
      const t = contextTokens(msg)
      if (t > 0) tokens = t
      parts.forEach((p, i) => {
        if (p?.type === 'reasoning' && p.text) {
          add(2 * i, { role: 'assistant', kind: 'thinking', text: p.text })
        } else if (p?.type === 'text' && p.text) {
          add(2 * i, { role: 'assistant', kind: 'text', text: p.text })
          last = snippet(p.text)
        } else if (p?.type === 'tool') {
          const state = p.state ?? {}
          const rawName = p.tool || p.name || 'tool'
          const callId = p.callID || p.id || `call_${seq}_${i}`
          add(2 * i, { role: 'assistant', kind: 'tool_use', tool_name: toolName(rawName), tool_use_id: callId, tool_input: toolInput(rawName, state.input) })
          if (state.status === 'completed' || state.status === 'error') {
            add(2 * i + 1, { role: 'user', kind: 'tool_result', tool_use_id: callId, is_error: state.status === 'error', text: v1ToolResultText(state) })
          }
        } else if (p?.type === 'compaction') {
          add(2 * i, { role: 'system', kind: 'notice', text: '已压缩上下文' })
        }
      })
      if (msg.finish === 'error' || msg.error) {
        if (msg.error?.name === 'MessageAbortedError' || msg.error?.type === 'aborted') {
          add(2 * parts.length, { role: 'system', kind: 'notice', text: '已中断' })
        } else {
          const errStr = errorText(msg.error?.data ?? msg.error)
          add(2 * parts.length, { role: 'system', kind: 'notice', text: errStr ? `出错了：${snippet(errStr)}` : '出错了' })
        }
      } else if (FINISH_NOTES[msg.finish]) {
        add(2 * parts.length, { role: 'system', kind: 'notice', text: FINISH_NOTES[msg.finish] })
      }
    }
  }

  return { items, toSeq: messages.length, lastText: last, model, provider, tokens, hasAssistant, userCount: userOrdinal }
}
