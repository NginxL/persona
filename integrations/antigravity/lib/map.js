import { MCP_SERVER_NAMES, SNAPSHOT_RE, SNAPSHOT_END_RE } from '../../runtime-sdk/lib/compat.js'
// Mapping between Antigravity CLI (agy) transcripts / ACP updates and Persona runtime items.
import { appendFileSync, constants, existsSync, lstatSync, readFileSync, statSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { basename, join } from 'node:path'
import { appToolName, clipInput, light, snippet } from '../../runtime-sdk/index.js'
import { isImageReference, localMediaPath } from './media.js'

export const RUNTIME = 'agy'
export const LABEL = 'Antigravity'
export const PREFIX = 'agy-'

// Kept off native JSON: an appended replacement branch is a new logical step even when its timestamp repeats.
const STEP_IDENTITY = Symbol('persona.agy.stepIdentity')
// Kept off native JSON: live ACP tool calls and results pair explicitly even when parallel calls complete out of order.
export const LIVE_TOOL_USE_ID = Symbol('persona.agy.liveToolUseId')

function completedReply(step) {
  return step.type === 'PLANNER_RESPONSE' && step.status === 'DONE' && typeof step.content === 'string' &&
    Boolean(step.content.trim()) && !step.tool_calls?.length
}

function stepIdentity(step, branch = '') {
  const birth = typeof step.created_at === 'string' ? step.created_at : ''
  return birth || branch ? createHash('sha256').update(birth).update('\n').update(branch).digest('hex').slice(0, 20) : ''
}

// A few CLI exports omit the scheduler record even when ACP and its completed native reply agree.
// Record only the two native step identities. The Go pane reader uses the same local format.
const WAKE_RECEIPTS = '.persona-observed-wakes'
const OBSERVED_WAKE = Symbol('persona.agy.observedWake')
const volatileWakes = new Map() // read-only brain directories retain matched wakes for this process

function modelWork(step) {
  return step?.type === 'PLANNER_RESPONSE' && (step.content?.trim?.() || step.thinking?.trim?.() || step.tool_calls?.length) ||
    step?.type === 'GENERIC' || step?.type === 'ERROR_MESSAGE'
}

function explicitOwner(step) {
  return step?.type === 'USER_INPUT' && (!step.source || step.source === 'USER_EXPLICIT')
}

function wakeStepKey(step) {
  const identity = step[STEP_IDENTITY] ?? stepIdentity(step)
  return createHash('sha256').update([step.step_index, step.type, identity, step.created_at ? '' : step.content || ''].join('\n')).digest('hex').slice(0, 20)
}

function wakeLink(previous, next) {
  return wakeStepKey(previous) + ' ' + wakeStepKey(next)
}

export function rememberObservedWake(brainDir, steps, first) {
  // A foreground send or a real native notification supplies its own boundary.
  if (steps.slice(first).some(explicitOwner)) return
  const before = steps.slice(0, first).findLastIndex(step => modelWork(step) || explicitOwner(step))
  if (before < 0 || !completedReply(steps[before])) return
  const resumed = steps.findIndex((step, index) => index >= first && modelWork(step))
  if (resumed < 0 || steps[resumed][OBSERVED_WAKE] || steps.slice(before + 1, resumed).some(step => step?.type === 'SYSTEM_MESSAGE')) return
  const link = wakeLink(steps[before], steps[resumed])
  try {
    if (lstatSync(brainDir).isDirectory()) {
      appendFileSync(join(brainDir, WAKE_RECEIPTS), link + '\n', {
        flag: constants.O_APPEND | constants.O_WRONLY | constants.O_CREAT | constants.O_NOFOLLOW, mode: 0o600,
      })
      return
    }
  } catch {}
  let links = volatileWakes.get(brainDir)
  if (!links) volatileWakes.set(brainDir, (links = new Set()))
  links.add(link)
}

function applyWakeReceipts(steps, links) {
  let previous
  const applied = []
  for (const step of steps) {
    if (explicitOwner(step)) previous = undefined
    if (!modelWork(step)) continue
    if (previous) {
      const link = wakeLink(previous, step)
      if (links.has(link)) {
        Object.defineProperty(step, OBSERVED_WAKE, { value: true })
        applied.push(link)
      }
    }
    previous = completedReply(step) ? step : undefined
  }
  return applied.length ? createHash('sha256').update(applied.join('\n')).digest('hex').slice(0, 20) : ''
}

const OWN_TOOLS = {
  run_command: 'Bash',
  view_file: 'Read',
  read_file: 'Read',
  write_to_file: 'Write',
  write_file: 'Write',
  replace_file_content: 'Edit',
  edit_file: 'Edit',
  grep_search: 'Grep',
  find_by_name: 'Glob',
  search_web: 'WebSearch',
  read_url_content: 'WebFetch',
  ask_question: 'AskUserQuestion',
  invoke_subagent: 'Agent',
}

export function toPersonaId(rawId) {
  const s = String(rawId ?? '').trim()
  if (!s) return ''
  return s.startsWith(PREFIX) ? s : `${PREFIX}${s}`
}

export function toAgyId(personaId) {
  const s = String(personaId ?? '').trim()
  return s.startsWith(PREFIX) ? s.slice(PREFIX.length) : s
}

const USER_REQ_RE = /<USER_REQUEST>\s*([\s\S]*?)\s*<\/USER_REQUEST>/

export function cleanUserText(raw) {
  if (typeof raw !== 'string') return ''
  const m = USER_REQ_RE.exec(raw)
  let body = m ? m[1] : raw
  let prev
  do {
    prev = body
    body = body.replace(SNAPSHOT_RE, '').replace(SNAPSHOT_END_RE, '')
  } while (body !== prev)
  return body.trim()
}

function attachedImagePaths(text) {
  const paths = new Set()
  for (const [, body] of text.matchAll(/<attached_images>([\s\S]*?)<\/attached_images>/g)) {
    for (const path of body.split('\n')) {
      if (path.trim()) paths.add(localMediaPath(path.trim()))
    }
  }
  return paths
}

const PERSONA_MCP_TOOL_RE = new RegExp(`^mcp_(?:${MCP_SERVER_NAMES.join('|')})(?:_[0-9a-fA-F]{8,16})?_([a-z][a-z0-9_]*)$`)
const BARE_TOOL_NAME_RE = /^[a-z][a-z0-9_]*$/

export function mapToolName(name) {
  if (typeof name === 'string') {
    const m = PERSONA_MCP_TOOL_RE.exec(name)
    if (m) return `mcp__persona__${m[1]}`
  }
  return appToolName(name, OWN_TOOLS)
}

export function mapToolCall(name, args) {
  const rawName = name || 'tool'
  const mapped = mapToolName(rawName)
  if (mapped === 'mcp__persona__persona_call' && args && typeof args === 'object' && !Array.isArray(args)) {
    const target = typeof args.name === 'string' ? args.name.trim() : ''
    if (BARE_TOOL_NAME_RE.test(target)) {
      const inner = args.arguments && typeof args.arguments === 'object' && !Array.isArray(args.arguments) ? args.arguments : {}
      return { toolName: `mcp__persona__${target}`, toolInput: clipInput(inner) }
    }
  }
  return { toolName: mapped, toolInput: mapToolInput(rawName, args) }
}

export function mapToolInput(name, args) {
  if (!args || typeof args !== 'object') return {}
  switch (name) {
    case 'run_command':
      return clipInput({
        command: args.CommandLine ?? args.command ?? '',
        description: args.toolSummary ?? args.Cwd ?? '',
      })
    case 'view_file':
    case 'read_file':
      return clipInput({
        file_path: args.AbsolutePath ?? args.Path ?? args.file_path ?? '',
      })
    case 'write_to_file':
    case 'write_file':
      return clipInput({
        file_path: args.TargetFile ?? args.Path ?? args.file_path ?? '',
        content: args.CodeContent ?? args.Content ?? args.content ?? '',
      })
    case 'replace_file_content':
    case 'edit_file':
      return clipInput({
        file_path: args.TargetFile ?? args.Path ?? args.file_path ?? '',
        old_string: args.TargetContent ?? args.Target ?? args.old_string ?? '',
        new_string: args.ReplacementContent ?? args.Replacement ?? args.new_string ?? '',
      })
    case 'grep_search':
      return clipInput({
        pattern: args.Query ?? args.Pattern ?? args.pattern ?? '',
        path: args.SearchPath ?? args.Path ?? args.path ?? '',
      })
    case 'find_by_name':
      return clipInput({
        pattern: args.Pattern ?? args.pattern ?? '',
        path: args.SearchDirectory ?? args.Path ?? args.path ?? '',
      })
    case 'search_web':
      return clipInput({
        query: args.query ?? args.Query ?? '',
      })
    case 'read_url_content':
      return clipInput({
        url: args.Url ?? args.url ?? '',
      })
    case 'invoke_subagent':
      return clipInput({
        description: args.toolSummary ?? 'subagent',
        prompt: typeof args.Subagents === 'string' ? args.Subagents : JSON.stringify(args.Subagents ?? ''),
      })
    default:
      return clipInput(args)
  }
}

export function cleanGenericOutput(content) {
  if (typeof content !== 'string') return ''
  // Strip run_command metadata header if followed by Output:
  const outIdx = content.indexOf('\nOutput:\n')
  if (outIdx >= 0 && content.startsWith('Created At:')) {
    return content.slice(outIdx + '\nOutput:\n'.length)
  }
  return content
}

/**
 * Parse a transcript.jsonl file (or array of parsed step objects) into Persona items.
 * @param {object[]} steps
 * @param {{ brainDir?: string, full?: boolean, includeMedia?: boolean, mediaItemId?: string, clientMsgIdFor?: (index: number, text: string, ordinal: number) => string | undefined }} [opts]
 */
export function stepsToItems(steps, opts = {}) {
  const items = []
  const pendingCalls = []
  let maxSeq = 0
  let lastText = ''
  let turnError
  let afterWork = false
  let media
  let userOrdinal = 0

  let delivered = ''
  let notified = false
  for (let i = 0; i < steps.length; i++) {
    const step = steps[i]
    if (!step || typeof step !== 'object') continue
    const idx = typeof step.step_index === 'number' ? step.step_index : i
    // Synthetic notices can move the projected sequence without moving the native record's files or message binding.
    const nativeIdx = typeof step.native_step_index === 'number' ? step.native_step_index : idx
    const seq = idx + 1
    if (seq > maxSeq) maxSeq = seq
    const ts = step.created_at || new Date().toISOString()
    const identity = step[STEP_IDENTITY] ?? stepIdentity(step)
    const line = identity ? `${seq}@${identity}` : String(seq)

    // A native system message can be ordinary housekeeping. Wait for actual model activity (or an
    // unsolicited ACP update), so checkpoints and cursors can pass it before its meaning is known.
    const autonomous = step.type === 'PERSONA_AUTONOMOUS_WAKE'
    if (autonomous || modelWork(step)) {
      if (delivered && (notified || autonomous || step[OBSERVED_WAKE])) {
        // The previous answer identifies its continuation on live and native reads. Block -1 precedes
        // the first resumed item without renumbering any existing text, tool, attachment or prompt.
        items.push({ id: `${delivered}#-1`, seq, ts, role: 'system', kind: 'notice', wake: true, text: '后台任务有更新' })
        lastText = ''
        afterWork = false
        turnError = undefined
      }
      delivered = completedReply(step) ? line : ''
      notified = false
    }

    // A call's result follows its planner step. One that never came (interrupted, dropped) must not stay queued to
    // take the next result, which would shift every later result onto the wrong call.
    if (step.type === 'PLANNER_RESPONSE' || step.type === 'USER_INPUT' || step.type === 'ERROR_MESSAGE') pendingCalls.length = 0

    switch (step.type) {
      case 'USER_INPUT': {
        if (step.source && step.source !== 'USER_EXPLICIT') break
        delivered = ''
        notified = false
        // A new prompt answers the previous reply. Only assistant words can ask the user for a response.
        lastText = ''
        turnError = undefined
        afterWork = false
        const text = cleanUserText(step.content)
        userOrdinal++
        const msgId = (typeof step.client_msg_id === 'string' && step.client_msg_id) || opts.clientMsgIdFor?.(nativeIdx, text, userOrdinal)
        const user = { seq, role: 'user', ts, ...(msgId ? { client_msg_id: msgId } : {}) }
        if (text) {
          const item = { id: `${line}#0`, kind: 'text', text, ...user }
          items.push(opts.full ? item : light(item))
        }
        if (opts.includeMedia === true && Array.isArray(step.media)) {
          const attached = attachedImagePaths(text)
          for (const [index, entry] of step.media.entries()) {
            if (!isImageReference(entry)) continue
            const path = entry.uri && localMediaPath(entry.uri)
            if (path && attached.has(path)) continue
            const id = `${line}#media${index}`
            items.push({ id, kind: 'image', ...user, image: { mime: entry.mime_type, data: '', lazy: true } })
            if (opts.mediaItemId === id) media = entry
          }
        }
        break
      }
      case 'CHECKPOINT':
        items.push({ id: `${line}#0`, seq, role: 'system', kind: 'notice', text: '上下文已压缩', ts })
        break
      case 'PLANNER_RESPONSE': {
        turnError = undefined
        let sub = 0
        if (typeof step.thinking === 'string' && step.thinking.trim()) {
          const item = { id: `${line}#${sub++}`, seq, role: 'assistant', kind: 'thinking', text: step.thinking.trim(), ts }
          items.push(opts.full ? item : light(item))
        }
        if (typeof step.content === 'string' && step.content.trim()) {
          const text = step.content.trim()
          lastText = snippet(text)
          afterWork = true
          const item = { id: `${line}#${sub++}`, seq, role: 'assistant', kind: 'text', text, ts }
          items.push(opts.full ? item : light(item))
        }
        const calls = Array.isArray(step.tool_calls) ? step.tool_calls : []
        if (calls.length) afterWork = true
        for (let k = 0; k < calls.length; k++) {
          const tc = calls[k]
          if (!tc || typeof tc !== 'object') continue
          const explicitCallId = typeof tc[LIVE_TOOL_USE_ID] === 'string' && tc[LIVE_TOOL_USE_ID] ? tc[LIVE_TOOL_USE_ID] : ''
          const callId = explicitCallId || `call_${line}_${k}`
          const { toolName, toolInput } = mapToolCall(tc.name || 'tool', tc.args)
          if (!explicitCallId) pendingCalls.push({ callId, idx })
          const item = {
            id: `${line}#${sub++}`,
            seq,
            role: 'assistant',
            kind: 'tool_use',
            tool_name: toolName,
            tool_input: toolInput,
            tool_use_id: callId,
            ts,
          }
          items.push(opts.full ? item : light(item))
        }
        break
      }
      case 'GENERIC': {
        afterWork = true
        const explicitCallId = typeof step[LIVE_TOOL_USE_ID] === 'string' && step[LIVE_TOOL_USE_ID] ? step[LIVE_TOOL_USE_ID] : ''
        const paired = explicitCallId ? undefined : pendingCalls.shift()
        const callId = explicitCallId || (paired ? paired.callId : `call_${line}_0`)
        let rawOut = step.error || step.content || ''
        if (opts.full && opts.brainDir && Array.isArray(step.truncated_fields) && step.truncated_fields.includes('content')) {
          const outFile = join(opts.brainDir, '.system_generated', 'steps', String(nativeIdx), 'output.txt')
          if (existsSync(outFile)) {
            try {
              rawOut = readFileSync(outFile, 'utf8')
            } catch {}
          }
        }
        const text = step.error ? String(step.error) : cleanGenericOutput(rawOut)
        const isError = Boolean(step.error) || step.status === 'ERROR'
        const resItem = {
          id: `${line}#0`,
          seq,
          role: 'user',
          kind: 'tool_result',
          tool_use_id: callId,
          text,
          is_error: isError,
          ts,
        }
        items.push(opts.full ? resItem : light(resItem))
        if (opts.includeMedia === true && Array.isArray(step.media)) {
          for (const [index, entry] of step.media.entries()) {
            if (!isImageReference(entry)) continue
            const id = `${line}#media${index}`
            items.push({ id, seq, role: 'assistant', kind: 'image', tool_use_id: callId, ts,
              image: { mime: entry.mime_type, data: '', lazy: true } })
            if (opts.mediaItemId === id) media = entry
          }
        }
        break
      }
      case 'SYSTEM_MESSAGE':
        notified = Boolean(delivered)
        break
      case 'PERSONA_MODE_NOTICE': {
        const item = { id: `${line}#0`, seq, role: 'system', kind: 'notice', text: String(step.content || ''), ts }
        items.push(opts.full ? item : light(item))
        break
      }
      case 'ERROR_MESSAGE': {
        const text = step.error || step.content || 'Error'
        turnError = { turn_error: String(text), ...step.turn_error, turn_error_after_work: afterWork }
        // Clients display this shared API-error format outside the collapsed work summary.
        const message = String(text).startsWith('API Error:') ? String(text) : `API Error: ${text}`
        items.push({ id: `${line}#0`, seq, role: 'assistant', kind: 'text', text: message, ts })
        break
      }
      default:
        break
    }
  }

  return { items, toSeq: maxSeq, lastText, turnError, ...(media ? { media } : {}) }
}

const transcriptCache = new Map()
const transcriptRevisions = new Map()
const MAX_CACHED_TRANSCRIPTS = 128
const MAX_TRANSCRIPT_REVISIONS = 256
const MAX_TRANSCRIPT_CACHE_BYTES = 64 * 1024 * 1024
let transcriptCacheBytes = 0

export function forkReplyPosition(itemId, steps) {
  const legacy = !itemId.includes('@')
  const item = stepsToItems(steps).items.find((entry) => entry.role === 'assistant' && entry.kind === 'text' &&
    (entry.id === itemId || (legacy && entry.id.replace(/@[^#]+/, '') === itemId)))
  return item ? steps.findIndex((entry, index) => entry?.type === 'PLANNER_RESPONSE' && (entry.step_index ?? index) + 1 === item.seq) : -1
}

export function forkStepIndex(index, trajectory) {
  if (trajectory[index]?.type !== 'CORTEX_STEP_TYPE_PLANNER_RESPONSE') return undefined
  const nextInput = trajectory.findIndex((entry, position) => position > index && entry?.type === 'CORTEX_STEP_TYPE_USER_INPUT' &&
    (!entry.metadata?.source || entry.metadata.source === 'CORTEX_STEP_SOURCE_USER_EXPLICIT'))
  return (nextInput < 0 ? trajectory.length : nextInput) - 1
}

export function readTranscriptSteps(brainDir, full = false) {
  return readTranscript(brainDir, full).steps
}

export function readTranscript(brainDir, full = false) {
  const file = join(brainDir, '.system_generated', 'logs', full ? 'transcript_full.jsonl' : 'transcript.jsonl')
  const fallback = full ? join(brainDir, '.system_generated', 'logs', 'transcript.jsonl') : null
  const target = existsSync(file) ? file : fallback && existsSync(fallback) ? fallback : null
  if (!target) return { steps: [], branch: '', revision: '', compact: false }
  const compact = Boolean(full && target !== file)
  try {
    const info = statSync(target, { bigint: true })
    const receiptFile = join(brainDir, WAKE_RECEIPTS)
    let receiptInfo
    try { receiptInfo = lstatSync(receiptFile, { bigint: true, throwIfNoEntry: false }) } catch {}
    const volatile = volatileWakes.get(brainDir)
    const version = [info.dev, info.ino, info.size, info.mtimeNs, info.ctimeNs, receiptInfo?.size, receiptInfo?.mtimeNs, volatile?.size].join(':')
    // Fast same-size native rewrites can share a filesystem timestamp tick.
    const settled = Math.max(Number(info.mtimeMs), Number(info.ctimeMs)) <= Date.now() - 1000
    const remembered = transcriptRevisions.get(target)
    if (remembered) {
      transcriptRevisions.delete(target)
      transcriptRevisions.set(target, remembered)
    }
    const cached = transcriptCache.get(target)
    if (cached) {
      transcriptCache.delete(target)
      transcriptCacheBytes -= cached.size
      if (settled && cached.version === version) {
        transcriptCache.set(target, cached)
        transcriptCacheBytes += cached.size
        return cached.transcript
      }
    }
    const raw = readFileSync(target, 'utf8')
    const out = []
    let branch = ''
    let revision = ''
    for (const line of raw.split('\n')) {
      const trimmed = line.trim()
      if (!trimmed) continue
      try {
        const step = JSON.parse(trimmed)
        const previous = out.at(-1)
        const index = step?.step_index
        let at = out.length
        if (Number.isInteger(index) && Number.isInteger(previous?.step_index) && index <= previous.step_index) {
          // A step whose index nothing retained has is a late arrival, not a rewind: parallel tool calls are exported
          // as they finish, so a call that fails at once lands before its sibling's result. It takes its place in
          // step order, keeps the branch (every item identity) and marks the content as changed.
          const floor = out.findLastIndex((s) => s.step_index <= index)
          const late = index < previous.step_index && out[floor]?.step_index !== index
          if (late) {
            at = floor + 1
            revision = createHash('sha256').update(trimmed).digest('hex').slice(0, 20)
          } else {
            // Native exports append replacement steps after a rewind. A status update keeps the step's identity.
            if (index < previous.step_index || step.type !== previous.type || step.created_at !== previous.created_at) {
              branch = createHash('sha256').update(branch).update('\n').update(trimmed).digest('hex').slice(0, 20)
              revision = ''
            } else if (JSON.stringify(previous) !== JSON.stringify(step)) {
              // A planner step can gain its completed response while keeping its index and branch.
              revision = createHash('sha256').update(JSON.stringify(step)).digest('hex').slice(0, 20)
            }
            while (out.length && out.at(-1).step_index >= index) out.pop()
            at = out.length
          }
        }
        // Compute once with the parsed record, rather than rehashing every step on each session-list pass.
        if (step && typeof step === 'object') Object.defineProperty(step, STEP_IDENTITY, { value: stepIdentity(step, branch) })
        out.splice(at, 0, step)
      } catch {}
    }
    const nativeRevision = revision
    const hash = createHash('sha256').update(raw).digest('hex')
    if (remembered && remembered.branch === branch) {
      const appended = raw.length >= remembered.length &&
        createHash('sha256').update(raw.slice(0, remembered.length)).digest('hex') === remembered.hash
      if (!appended) revision = hash.slice(0, 20)
      else if (nativeRevision === remembered.nativeRevision) revision = remembered.revision
    }
    // Keep small revision records independently of the parsed-body LRU, including fresh native rewrites.
    transcriptRevisions.set(target, { length: raw.length, hash, branch, nativeRevision, revision })
    while (transcriptRevisions.size > MAX_TRANSCRIPT_REVISIONS) transcriptRevisions.delete(transcriptRevisions.keys().next().value)
    const transcript = { steps: out, branch, revision, compact }
    if (volatile || receiptInfo?.isFile()) {
      const links = new Set(volatile)
      if (receiptInfo?.isFile()) {
        try { for (const link of readFileSync(receiptFile, 'utf8').split('\n')) if (link) links.add(link) } catch {}
      }
      const witness = applyWakeReceipts(out, links)
      if (witness) transcript.revision += ':wake:' + witness
    }
    const size = Number(info.size)
    if (settled && size <= MAX_TRANSCRIPT_CACHE_BYTES) {
      while (transcriptCache.size >= MAX_CACHED_TRANSCRIPTS || transcriptCacheBytes + size > MAX_TRANSCRIPT_CACHE_BYTES) {
        const oldest = transcriptCache.keys().next().value
        transcriptCacheBytes -= transcriptCache.get(oldest).size
        transcriptCache.delete(oldest)
      }
      transcriptCache.set(target, { version, size, transcript })
      transcriptCacheBytes += size
    }
    return transcript
  } catch {
    return full ? { ...readTranscript(brainDir, false), compact: true } : { steps: [], branch: '', revision: '', compact: false }
  }
}

export function nativeWorking(summary) {
  return !summary?.killed && (summary?.status === 'CASCADE_RUN_STATUS_RUNNING' || Boolean(summary?.not_fully_idle))
}

/** Working requires live execution evidence. A completed planner message can be progress narration. */
export function transcriptStatus(steps, summary) {
  if (summary?.killed) return 'idle'
  if (nativeWorking(summary)) return 'working'
  const last = steps.findLast((step) => ['USER_INPUT', 'PLANNER_RESPONSE', 'GENERIC', 'ERROR_MESSAGE'].includes(step?.type))
  if (!last) return 'unknown'
  if (last.type === 'ERROR_MESSAGE' || ['ERROR', 'CANCELLED', 'CANCELED'].includes(last.status)) return 'idle'
  // A summary from before a newly exported step cannot close that newer work.
  const idle = summary?.status === 'CASCADE_RUN_STATUS_IDLE' && Number.isInteger(last.step_index) && summary.step_count > last.step_index
  if (idle) return completedReply(last) ? 'done' : 'idle'
  return 'unknown'
}

export function workspacePathOf(summary, fallbackCwd = '', skip = null) {
  if (Array.isArray(summary?.WorkspaceURIs) && summary.WorkspaceURIs.length > 0) {
    for (const raw of summary.WorkspaceURIs) {
      const u = String(raw)
      let p = u
      if (u.startsWith('file://')) {
        try {
          p = decodeURIComponent(new URL(u).pathname)
        } catch {
          p = u.slice('file://'.length)
        }
      }
      if (skip && skip(p)) continue
      return p
    }
  }
  return fallbackCwd
}

export function personaModeToAgy(mode) {
  switch (mode) {
    case 'plan':
      return 'plan'
    case 'acceptEdits':
      return 'accept-edits'
    case 'auto':
      return 'auto'
    case 'bypassPermissions':
      return 'accept-edits'
    case 'default':
    default:
      return 'default'
  }
}

export function agyModeToPersona(mode) {
  switch (mode) {
    case 'auto':
      return 'auto'
    case 'plan':
      return 'plan'
    case 'accept-edits':
      return 'acceptEdits'
    default:
      return 'default'
  }
}
