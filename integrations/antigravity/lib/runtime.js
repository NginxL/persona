import { daemonBinary, mcpServers } from './compat.js'
import { contextWrapped, wirePackageName } from '../../runtime-sdk/lib/compat.js'
// Antigravity (agy) runtime for Persona (docs/integrations/runtimes.md).
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { basename, join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { createRuntime, installCurrent, interactionId, page, RpcError, snippet, summaryOf, verifyPackage } from '../../runtime-sdk/index.js'
import { isAssistantDir } from '../../runtime-sdk/lib/identity.js'
import { AgyAcpClient } from './acp.js'
import { discoverModels, mapModels } from './models.js'
import { nativeErrorStep } from './errors.js'
import {
  agyModeToPersona,
  cleanUserText,
  LABEL,
  mapToolCall,
  mapToolInput,
  mapToolName,
  nativeWorking,
  personaModeToAgy,
  PREFIX,
  readTranscript,
  RUNTIME,
  stepsToItems,
  transcriptStatus,
  toAgyId,
  toPersonaId,
  workspacePathOf,
} from './map.js'

// agy acp's answer to a model it does not offer, and agy models' answer while the CLI is signed out (#10368).
const UNKNOWN_MODEL = /\bunknown model\b|\bmodel_not_found\b/i
const SIGNED_OUT = /\b(?:sign(?:ed)?|log(?:ged)?)[ -]?in\b/i

export function createAgyRuntime(options = {}) {
  const home = options.home || homedir()
  const agyDir = options.agyDir || process.env.ANTIGRAVITY_CLI_CONFIG_DIR || join(home, '.gemini', 'antigravity-cli')
  const personadBin = daemonBinary(home, options)
  const agyBin = options.agyBin || join(home, '.local', 'bin', 'agy')
  const log = options.log || ((m) => console.error(m))
  const listDays = options.listDays ?? 14
  const maxSessions = options.maxSessions ?? 80
  const exportPollMs = options.exportPollMs ?? 400
  const exportSettleTimeoutMs = options.exportSettleTimeoutMs ?? 15_000

  const activeTurns = new Map() // personaId -> { streamingText, startedAt }
  const promptTurns = new Map() // the prompt executing, before any queued successor
  const unexportedTurns = new Map() // agyId -> { baseMaxIndex, seq, steps, hasReply, expiresAt } settled turns awaiting native transcript export
  const exportPolls = new Map() // agyId -> interval watching for a settled turn's native transcript flush
  const sentMsgIds = new Map() // `${agyId}:${stepIndex}` -> { text, clientMsgId }
  let nextTurnSeq = 1
  const createdAt = new Map() // owned sessions remain discoverable before their first transcript export
  const completedTurns = new Set() // owned ACP turns that ended before a native transcript was available
  const sessionTitles = new Map() // agyId -> title
  const sessionCwds = new Map() // agyId -> cwd
  const sessionModels = new Map() // agyId -> modelId
  const sessionModes = new Map() // agyId -> { mode, effort }
  const archived = new Set() // agyId
  const lastPushedSeq = new Map() // personaId -> number
  const lastPushedHistory = new Map()
  const watchedPolls = new Map()
  const rewinding = new Set()
  const nativeCuts = new Map()
  const cutChecks = new Map()
  const nativeTails = new Map()
  const promptFailures = new Map()
  const assistants = new Set() // personaId
  const checkTurns = new Map() // availability probes collect text without appearing in user conversations
  const permissionAsks = new Map() // agyId -> Set of AbortControllers, one per permission card still on the phone
  const cancelling = new Set() // agyId: interrupted, its turn has not ended yet

  const acp = new AgyAcpClient({
    bin: existsSync(agyBin) ? agyBin : 'agy',
    ...(Array.isArray(options.agyArgs) ? { args: options.agyArgs } : {}),
    log,
    onPermissionRequest: async (params) => handlePermissionRequest(params),
  })

  let catalog = []
  let catalogAt = 0
  let catalogFlight
  let catalogTimer
  // One agy acp process can keep the model list it had when the account's plan lapsed or the CLI signed out (#10368).
  let discoveredGeneration = -1 // the agy acp generation running when the latest successful discovery began
  let signedOut = false // the latest discovery failed because the CLI is signed out
  let staleThrough = 0 // agy acp generations up to this one were running before the CLI signed in again
  let freshRejection // a replacement process rejected this model too: { generation, model, listed }
  const catalogAbort = new AbortController()
  let stopped = false
  const update = options.update?.store && typeof options.update.name === 'string' && typeof options.update.hash === 'string'
    ? { ...options.update, name: wirePackageName(options.update.name) }
    : undefined
  const idleCheckMs = options.idleCheckMs ?? 5000
  let want = ''
  const bad = new Set()
  let installing = false
  let exiting = false
  let idleTimer
  let leases = 0 // runtime calls, turns and native reads that use agy acp, each held for its whole run (#10368)
  let recycling // while agy acp is being replaced: new users wait for it and start on the new process
  const launchers = [{ name: 'agy', agent: RUNTIME, models: [] }]

  function publishModels(entries = catalog) {
    catalog = mapModels(entries, readAgySettings().model)
    const selected = catalog.find((model) => model.default)
    rt.launchersChanged([{ name: 'agy', agent: RUNTIME, model: selected?.name || readAgySettings().model || '', models: catalog }])
  }

  async function models(params = {}) {
    if (!params.refresh && catalog.length && Date.now() - catalogAt < 300_000) {
      publishModels()
      return { agent: RUNTIME, models: catalog }
    }
    if (!catalogFlight) {
      const generation = acp.generation
      catalogFlight = discoverModels(acp.bin, { signal: catalogAbort.signal }).then((entries) => {
        if (!stopped) {
          catalogAt = Date.now()
          discoveredGeneration = generation
          if (signedOut) {
            // The CLI signed in again: an agy acp process from before keeps the signed-out account.
            signedOut = false
            staleThrough = generation
            void recycleStale(0)
          }
          publishModels(entries)
        }
        return { agent: RUNTIME, models: catalog }
      }, (err) => {
        if (SIGNED_OUT.test(err?.message || '')) signedOut = true
        throw err
      }).finally(() => { catalogFlight = undefined })
    }
    return catalogFlight
  }

  // A session's models come from its agy acp process, which can keep the list it had when the account changed
  // (#10368). A discovery that began while that process was already running has the newer list: keep it.
  function publishSessionModels(entries) {
    if (acp.generation > discoveredGeneration) publishModels(entries)
  }

  /** Run `fn` holding a lease on agy acp: it is never replaced under a lease, and a replacement in progress ends first. */
  async function withAcp(fn) {
    while (recycling) await recycling
    leases++
    try {
      return await fn()
    } finally {
      leases--
      if (want) void maybeExit()
    }
  }

  /** Whether anything besides the caller's own `own` leases uses agy acp: a lease, a request or a phone card. */
  function acpBusy(own) {
    return leases > own || acp.pending.size > 0 || rt.busy()
  }

  /**
   * Replace agy acp when nothing besides the caller's own `own` leases uses it. The check and the replacement run with no
   * await between them, and users arriving meanwhile wait for the new process. True when the next call gets a new one.
   */
  async function replaceAcp(own, reason) {
    if (recycling || acpBusy(own)) return false
    if (!acp.proc) return true
    recycling = acp.recycle(reason).finally(() => { recycling = undefined })
    await recycling
    return true
  }

  /** Replace an agy acp process that was running before the CLI signed in again, once nothing else uses it. */
  async function recycleStale(own) {
    if (acp.proc && acp.generation <= staleThrough) await replaceAcp(own, 'the Antigravity CLI signed in again')
  }

  async function setNativeModel(rawId, model, timeoutMs) {
    try {
      return await acp.setModel(rawId, model, timeoutMs)
    } catch (err) {
      if (UNKNOWN_MODEL.test(`${err?.message || ''} ${JSON.stringify(err?.data ?? '')}`)) err.unknownModel = model
      throw err
    }
  }

  /**
   * agy acp rejected `model` as unknown. When a fresh `agy models` lists it and nothing else uses agy acp, replace the
   * process, which can keep the model list from a lapsed plan. The listed model when it was replaced, otherwise nothing.
   */
  async function recycleForModel(model) {
    if (acpBusy(1)) return undefined
    let discovered
    try {
      discovered = (await models({ refresh: true })).models
    } catch (err) {
      log(`agy model discovery error: ${err.message}`)
      return undefined
    }
    const entry = discovered.find((candidate) => candidate.id === model || candidate.name === model)
    const listed = discovered.map((candidate) => candidate.id).join('\n')
    const last = freshRejection
    if (!entry || (last?.generation === acp.generation && last.model === entry.id && last.listed === listed)) return undefined
    // Checked again: a call or turn that began during the discovery holds a lease.
    if (!(await replaceAcp(1, `it does not know ${entry.id}, which agy models lists`))) return undefined
    return { model: entry.id, listed }
  }

  /** Run `attempt`; when agy acp rejects a model a fresh discovery lists, replace the process once and run it again. */
  async function retryStaleModels(attempt) {
    try {
      return await attempt()
    } catch (err) {
      const recycled = err?.unknownModel ? await recycleForModel(err.unknownModel) : undefined
      if (!recycled) throw err
      try {
        return await attempt()
      } catch (again) {
        // A fresh process rejects it too: report that, and do not replace this one for the same catalog again.
        if (again?.unknownModel) freshRejection = { generation: acp.generation, ...recycled }
        throw again
      }
    }
  }

  function sessionConfiguration(rawId, result) {
    if (!result) return
    if (result.models?.availableModels) publishSessionModels(result.models.availableModels)
    if (result.models?.currentModelId) sessionModels.set(rawId, result.models.currentModelId)
    if (result.modes?.currentModeId) {
      const cur = sessionModes.get(rawId) || {}
      const initialBypass = !cur.mode && configuredMode() === 'bypassPermissions'
      sessionModes.set(rawId, { ...cur, mode: initialBypass ? 'bypassPermissions' : agyModeToPersona(result.modes.currentModeId) })
    }
    const config = result.configOptions
    if (Array.isArray(config)) {
      const model = config.find((option) => option.category === 'model' || option.id === 'model')?.currentValue
      const effort = config.find((option) => option.id === 'thought_level')?.currentValue
      const mode = config.find((option) => option.id === 'mode')?.currentValue
      const cur = sessionModes.get(rawId) || {}
      sessionModes.set(rawId, { ...cur, ...(effort ? { effort } : {}), ...(mode && !(cur.mode === 'bypassPermissions' && ['accept-edits', 'default'].includes(mode)) ? { mode: agyModeToPersona(mode) } : {}) })
      if (model) {
        const variant = catalog.find((entry) => entry.id === `${model}-${effort}`) || catalog.find((entry) => entry.id === model)
        if (variant) sessionModels.set(rawId, variant.id)
      }
    }
    rt.sessionsChanged()
  }

  const rt = createRuntime(
    {
      listSessions,
      items,
      cursor,
      item,
      send,
      interrupt,
      start,
      rename,
      close,
      modes,
      models,
      check,
      rewind,
      watchChanged,
    },
    {
      runtime: RUNTIME,
      name: LABEL,
      agent: RUNTIME,
      version: '0.1.0',
      launcher: 'agy',
      modelSwitch: true,
      launchers,
      assistant: true,
      socketPath: options.socketPath,
      log,
      ...(update ? { package: { name: update.name, hash: update.hash }, onUpdate: (offer) => void takeUpdate(offer) } : {}),
      retryMs: options.retryMs,
    },
  )

  acp.on('update', (params) => {
    const rawId = params?.sessionId
    const upd = params?.update
    if (!rawId || !upd) return
    const pid = toPersonaId(rawId)
    switch (upd.sessionUpdate) {
      case 'config_option_update':
        sessionConfiguration(rawId, upd)
        break
      case 'session_info_update':
        if (typeof upd.title === 'string' && upd.title.trim()) {
          sessionTitles.set(rawId, upd.title.trim())
          rt.sessionsChanged()
        }
        break
      case 'current_mode_update':
        if (typeof upd.currentModeId === 'string') {
          const cur = sessionModes.get(rawId) || {}
          const mapped = agyModeToPersona(upd.currentModeId)
          if (cur.mode === 'bypassPermissions' && ['acceptEdits', 'default'].includes(mapped)) {
            break
          }
          sessionModes.set(rawId, { ...cur, mode: mapped })
          rt.sessionsChanged()
        }
        break
      case 'agent_thought_chunk': {
        const chunk = upd.content?.text
        const turn = promptTurns.get(pid)
        if (typeof chunk === 'string' && chunk && turn) {
          turn.liveThinking += chunk
          turn.liveRev++
          if (!turn.thoughtTimer) {
            turn.thoughtTimer = setTimeout(() => {
              turn.thoughtTimer = undefined
              if (promptTurns.get(pid) === turn) syncTranscriptAppend(rawId)
            }, 150)
            turn.thoughtTimer.unref?.()
          }
        }
        break
      }
      case 'agent_message_chunk': {
        const chunk = upd.content?.text
        if (typeof chunk === 'string' && chunk) {
          const probe = checkTurns.get(rawId)
          if (probe) probe.text += chunk
          const turn = promptTurns.get(pid)
          if (turn) {
            if (turn.newMessage) turn.streamingText = ''
            turn.newMessage = false
            turn.streamingText += chunk
            rt.stream(pid, turn.streamingText)
          }
        }
        break
      }
      case 'tool_call': {
        const turn = promptTurns.get(pid)
        if (turn) {
          turn.newMessage = true
          const id = upd.toolCallId || `live-${turn.liveTools.length}`
          const rawName = upd._meta?.toolName || upd.name || upd.title || 'tool'
          const args = upd.rawInput || upd.input || {}
          const existing = turn.liveTools.find((t) => t.id === id)
          if (existing) {
            existing.name = rawName
            existing.args = args
          } else {
            turn.liveTools.push({ id, name: rawName, args, done: false, output: '', isError: false })
          }
          turn.liveRev++
        }
        syncTranscriptAppend(rawId)
        break
      }
      case 'tool_call_update': {
        const turn = promptTurns.get(pid)
        if (turn && upd.toolCallId) {
          const existing = turn.liveTools.find((t) => t.id === upd.toolCallId)
          if (existing && ['completed', 'failed'].includes(upd.status)) {
            existing.done = true
            existing.isError = upd.status === 'failed'
            existing.output = Array.isArray(upd.content)
              ? upd.content.map((b) => (b?.type === 'content' ? b.content?.text || '' : b?.text || '')).filter(Boolean).join('\n')
              : typeof upd.rawOutput === 'string' ? upd.rawOutput : ''
            turn.liveRev++
          }
        }
        syncTranscriptAppend(rawId)
        break
      }
      default:
        break
    }
  })

  function readAgySettings() {
    const cfgFile = join(agyDir, 'settings.json')
    if (!existsSync(cfgFile)) return {}
    try {
      return JSON.parse(readFileSync(cfgFile, 'utf8')) || {}
    } catch {
      return {}
    }
  }

  function effectiveModeOf(rawId) {
    const cur = sessionModes.get(rawId)
    if (cur?.mode) return cur.mode
    return configuredMode()
  }

  function configuredModel() {
    const value = readAgySettings().model
    if (typeof value !== 'string' || !value.trim()) return undefined
    const model = value.trim()
    return catalog.find((entry) => entry.id === model || entry.name === model)?.id || model
  }

  function configuredMode() {
    const cfg = readAgySettings()
    if (cfg.toolPermission === 'always-proceed' || cfg.agentMode === 'bypassPermissions') {
      return 'bypassPermissions'
    }
    if (cfg.agentMode === 'auto') return 'auto'
    if (cfg.agentMode === 'accept-edits') return 'acceptEdits'
    if (cfg.agentMode === 'plan') return 'plan'
    return 'default'
  }

  function shouldBypassPermission(rawId) {
    const mode = effectiveModeOf(rawId)
    return mode === 'bypassPermissions'
  }

  async function handlePermissionRequest(params) {
    const rawId = params?.sessionId
    const pid = toPersonaId(rawId)
    const tc = params?.toolCall || {}
    const rawName = tc.title || tc.name || 'run_command'
    const { toolName, toolInput } = mapToolCall(rawName, tc.rawInput || tc.input || {})
    const opts = Array.isArray(params?.options) ? params.options : []
    const allowAlwaysOpt = opts.find((o) => o.kind === 'allow_always') || opts.find((o) => o.kind === 'allow_once') || opts[0]
    const allowOpt = opts.find((o) => o.kind === 'allow_once') || allowAlwaysOpt
    const rejectOpt = opts.find((o) => o.kind === 'reject_once') || opts.find((o) => o.kind === 'reject_always')

    // A request that crossed the interrupt on the wire: ACP wants every pending one answered 'cancelled'.
    if (cancelling.has(rawId) || archived.has(rawId)) return { outcome: { outcome: 'cancelled' } }
    if (shouldBypassPermission(rawId) && allowAlwaysOpt) {
      return { outcome: { outcome: 'selected', optionId: allowAlwaysOpt.optionId } }
    }

    // Interrupt and the end of the turn withdraw the card (the SDK takes it off the phone) and agy hears 'cancelled'.
    const ac = new AbortController()
    let asks = permissionAsks.get(rawId)
    if (!asks) permissionAsks.set(rawId, (asks = new Set()))
    asks.add(ac)
    let res
    try {
      res = await rt.ask({
        id: interactionId(pid, tc.toolCallId || String(Date.now())),
        kind: 'permission',
        session_id: pid,
        tool_name: toolName,
        tool_input: toolInput,
        summary: summaryOf(toolName, toolInput),
      }, { signal: ac.signal })
    } catch (err) {
      if (ac.signal.aborted) return { outcome: { outcome: 'cancelled' } }
      throw err
    } finally {
      asks.delete(ac)
      if (asks.size === 0 && permissionAsks.get(rawId) === asks) permissionAsks.delete(rawId)
      if (want) setImmediate(() => { void maybeExit() })
    }

    if (res?.allow && allowOpt) {
      return { outcome: { outcome: 'selected', optionId: allowOpt.optionId } }
    }
    if (rejectOpt) {
      return { outcome: { outcome: 'selected', optionId: rejectOpt.optionId } }
    }
    return { outcome: { outcome: 'cancelled' } }
  }

  /** Take the session's permission cards off the phone: each one answers agy 'cancelled'. */
  function withdrawPermissionAsks(rawId, why) {
    const asks = permissionAsks.get(rawId)
    if (!asks) return
    permissionAsks.delete(rawId)
    for (const ac of asks) ac.abort(new Error(why))
  }

  function readCacheMetadata() {
    const metaFile = join(agyDir, 'cache', 'conversation_metadata.json')
    const lastFile = join(agyDir, 'cache', 'last_conversations.json')
    let convs = {}
    const cwdByConv = {}
    if (existsSync(metaFile)) {
      try {
        convs = JSON.parse(readFileSync(metaFile, 'utf8'))?.conversations || {}
      } catch {}
    }
    if (existsSync(lastFile)) {
      try {
        const last = JSON.parse(readFileSync(lastFile, 'utf8')) || {}
        for (const [dir, cid] of Object.entries(last)) {
          if (typeof cid === 'string' && !cwdByConv[cid]) cwdByConv[cid] = dir
        }
      } catch {}
    }
    return { convs, cwdByConv }
  }

  function brainDirOf(rawId) {
    return join(agyDir, 'brain', rawId)
  }

  function historyOf(rawId) {
    // Refresh cached items emitted before native failures used the shared API-error format or MCP tool normalization.
    return `agy:${rawId}:full:api-errors:content-v2:step-ids-v2:mcp-tools`
  }

  function cutMatches(cut, transcript) {
    return cut?.branch === transcript.branch && cut.next === JSON.stringify(transcript.steps.find((step) => step.step_index >= cut.limit))
  }

  function rememberCut(rawId, transcript, limit) {
    nativeCuts.set(rawId, { branch: transcript.branch, limit,
      next: JSON.stringify(transcript.steps.find((step) => step.step_index >= limit)) })
  }

  function exportedTranscriptOf(rawId, native = readConversationStates().get(rawId)) {
    const transcript = readTranscript(brainDirOf(rawId), true)
    const latestStep = transcript.steps.reduce((latest, step) => Math.max(latest, Date.parse(step.created_at || '') || 0), 0)
    const summaryCurrent = (Date.parse(native?.last_modified_time || '') || 0) > latestStep
    const verified = nativeCuts.get(rawId)
    const limit = cutMatches(verified, transcript) ? verified.limit : summaryCurrent ? native?.step_count : undefined
    const steps = Number.isInteger(limit) && limit >= 0
      ? transcript.steps.filter((step, index) => (step.step_index ?? index) < limit)
      : transcript.steps
    const compact = transcript.compact ? ':compact' : ''
    const branch = transcript.branch ? `:branch:${transcript.branch}` : ''
    const revision = transcript.revision ? `:revision:${transcript.revision}` : ''
    const cut = steps.length < transcript.steps.length ? `:cut:${limit}` : ''
    return { steps, history: historyOf(rawId) + compact + branch + revision + cut }
  }

  function tailKey(transcript, native) {
    const last = transcript.steps.at(-1)
    return [transcript.history, last?.step_index, last?.created_at, native?.step_count, native?.last_modified_time].join('|')
  }

  function maxStepIndex(steps) {
    return steps.reduce((max, step, index) => Math.max(max, typeof step?.step_index === 'number' ? step.step_index : index), -1)
  }

  function exportedHasReply(steps, baseMaxIndex) {
    return steps.some((step, index) => {
      const idx = typeof step?.step_index === 'number' ? step.step_index : index
      if (idx <= baseMaxIndex) return false
      if (step?.type === 'ERROR_MESSAGE') return true
      return step?.type === 'PLANNER_RESPONSE' && typeof step.content === 'string' && step.content.trim().length > 0
    })
  }

  function pendingReplaced(pending, steps, curMax = maxStepIndex(steps)) {
    return curMax > pending.baseMaxIndex &&
      (!pending.hasReply || exportedHasReply(steps, pending.baseMaxIndex) || Date.now() >= pending.expiresAt)
  }

  function clearExportPoll(rawId) {
    clearInterval(exportPolls.get(rawId))
    exportPolls.delete(rawId)
  }

  function scheduleExportPoll(rawId, baseMaxIndex, expectReply) {
    clearExportPoll(rawId)
    const pid = toPersonaId(rawId)
    const deadline = Date.now() + exportSettleTimeoutMs
    const timer = setInterval(() => {
      if (stopped || archived.has(rawId) || activeTurns.has(pid)) {
        clearExportPoll(rawId)
        return
      }
      syncTranscriptAppend(rawId)
      const exported = exportedTranscriptOf(rawId)
      const settled = !unexportedTurns.has(rawId) &&
        (!expectReply || exportedHasReply(exported.steps, baseMaxIndex))
      if (settled || Date.now() >= deadline) {
        clearExportPoll(rawId)
      }
    }, exportPollMs)
    timer.unref?.()
    exportPolls.set(rawId, timer)
  }

  function clientMsgIdFor(rawId) {
    return (idx, text) => {
      const sent = sentMsgIds.get(`${rawId}:${idx}`)
      return sent && sent.text === text ? sent.clientMsgId : undefined
    }
  }

  function liveTurnSteps(turn) {
    const out = []
    let idx = turn.baseMaxIndex + 1
    if (turn.userText) {
      out.push({
        step_index: idx++,
        type: 'USER_INPUT',
        source: 'USER_EXPLICIT',
        status: 'DONE',
        created_at: turn.startedIso,
        content: turn.userText,
        client_msg_id: turn.clientMsgId,
      })
    }
    if (turn.liveThinking.trim()) {
      out.push({
        step_index: idx++,
        type: 'PLANNER_RESPONSE',
        source: 'MODEL',
        status: 'RUNNING',
        created_at: turn.startedIso,
        thinking: turn.liveThinking,
      })
    }
    for (const tc of turn.liveTools) {
      out.push({
        step_index: idx++,
        type: 'PLANNER_RESPONSE',
        source: 'MODEL',
        status: tc.done ? 'DONE' : 'RUNNING',
        created_at: turn.startedIso,
        tool_calls: [{ name: tc.name, args: tc.args }],
      })
      if (tc.done) {
        out.push({
          step_index: idx++,
          type: 'GENERIC',
          source: 'MODEL',
          status: tc.isError ? 'ERROR' : 'DONE',
          created_at: turn.startedIso,
          content: tc.output,
          ...(tc.isError ? { error: tc.output || 'Tool failed' } : {}),
        })
      }
    }
    return out
  }

  function transcriptOf(rawId, native = readConversationStates().get(rawId)) {
    const transcript = exportedTranscriptOf(rawId, native)
    const curMax = maxStepIndex(transcript.steps)
    const pid = toPersonaId(rawId)
    const turn = promptTurns.get(pid)
    if (turn && turn.exportedBaseMax !== undefined && curMax <= turn.exportedBaseMax) {
      const extra = [...turn.priorPending, ...liveTurnSteps(turn)]
      if (extra.length > 0) {
        return {
          ...transcript,
          steps: [...transcript.steps, ...extra],
          history: `${transcript.history}:live:${turn.seq}:${turn.liveRev}`,
        }
      }
    }
    const pending = unexportedTurns.get(rawId)
    if (pending) {
      if (pendingReplaced(pending, transcript.steps, curMax)) {
        unexportedTurns.delete(rawId)
      } else {
        return {
          ...transcript,
          steps: [...transcript.steps, ...pending.steps],
          history: `${transcript.history}:settled:${pending.seq}`,
        }
      }
    }
    const tail = nativeTails.get(rawId)
    if (tail?.key === tailKey(transcript, native) && tail.error) {
      return { ...transcript, steps: [...transcript.steps, tail.error] }
    }
    return transcript
  }

  function readNativeError(rawId, native = readConversationStates().get(rawId), transcript = exportedTranscriptOf(rawId, native)) {
    const next = (transcript.steps.at(-1)?.step_index ?? -1) + 1
    const count = native?.step_count
    if (!Number.isInteger(count) || count <= next || nativeWorking(native)) return
    const key = tailKey(transcript, native)
    const previous = nativeTails.get(rawId)
    if (previous?.key === key && (!previous.retryAt || Date.now() < previous.retryAt)) return previous.pending
    const entry = { key }
    nativeTails.set(rawId, entry)
    entry.pending = withAcp(async () => {
      try {
        const offset = Math.max(next, count - 8)
        const result = await acp.nativeRequest('GetCascadeTrajectorySteps', { cascadeId: rawId, stepOffset: offset, disableRehydration: true })
        const steps = result.steps || []
        const last = steps.findLastIndex((step) => /CORTEX_STEP_TYPE_(USER_INPUT|PLANNER_RESPONSE|GENERIC|ERROR_MESSAGE)$/.test(step?.type))
        const error = nativeErrorStep(steps[last], offset + last)
        if (nativeTails.get(rawId) !== entry || stopped) return
        entry.error = error
        if (error) {
          syncTranscriptAppend(rawId)
          rt.sessionsChanged()
        }
      } catch (error) {
        entry.retryAt = Date.now() + 30_000
        log(`agy native turn error: ${error.message}`)
      } finally {
        entry.pending = undefined
      }
    })
    return entry.pending
  }

  function inputKey(rawId) {
    const transcript = exportedTranscriptOf(rawId)
    const input = transcript.steps.findLast((step) => step.type === 'USER_INPUT' && (!step.source || step.source === 'USER_EXPLICIT'))
    return [transcript.history, input?.step_index, input?.created_at].join('|')
  }

  // Native rewind restores older summary timestamps. Verify a shorter summary against the native trajectory so
  // restarts keep the cut, while a stale idle summary cannot hide a newer external input.
  async function reconcileNativeCut(rawId, native = readConversationStates().get(rawId)) {
    const transcript = readTranscript(brainDirOf(rawId), true)
    const count = native?.step_count
    if (!Number.isInteger(count) || count < 0 || !transcript.steps.some((step) => step.step_index >= count) ||
        cutMatches(nativeCuts.get(rawId), transcript)) return
    if (cutChecks.has(rawId)) return cutChecks.get(rawId)
    const check = withAcp(async () => {
      try {
        await acp.ensureSessionLoaded(rawId, sessionCwds.get(rawId) || home, [])
        const snapshot = await acp.nativeRequest('GetCascadeTrajectory', { cascadeId: rawId, disableRehydration: true })
        if (snapshot.trajectory?.cascadeId === rawId && (snapshot.trajectory.steps || []).length === count) {
          rememberCut(rawId, transcript, count)
          syncTranscriptAppend(rawId)
          rt.sessionsChanged()
        }
      } catch (error) {
        log(`agy rewind history: ${error.message}`)
      } finally {
        cutChecks.delete(rawId)
      }
    })
    cutChecks.set(rawId, check)
    return check
  }

  function readConversationStates() {
    const path = join(agyDir, 'conversation_summaries.db')
    if (!existsSync(path)) return new Map()
    let db
    try {
      db = new DatabaseSync(path, { readOnly: true })
      db.exec('PRAGMA query_only = 1; PRAGMA busy_timeout = 50')
      const rows = db.prepare(`
        SELECT conversation_id, status, not_fully_idle, killed, step_count,
               title, workspace_uris, last_modified_time
        FROM conversation_summaries
      `).all()
      return new Map(rows.map((row) => {
        let workspaceUris = []
        try {
          const parsed = JSON.parse(row.workspace_uris)
          if (Array.isArray(parsed)) workspaceUris = parsed.filter((uri) => typeof uri === 'string' && uri)
        } catch {}
        return [row.conversation_id, { ...row, WorkspaceURIs: workspaceUris }]
      }))
    } catch {
      // A missing or unavailable native summary cannot establish that a text reply ended the turn.
      return new Map()
    } finally {
      db?.close()
    }
  }

  function syncTranscriptAppend(rawId) {
    const pid = toPersonaId(rawId)
    const bDir = brainDirOf(rawId)
    const { steps, history } = transcriptOf(rawId)
    const toSeq = steps.reduce((max, step, index) => {
      const at = typeof step?.step_index === 'number' ? step.step_index : index
      return Math.max(max, at + 1)
    }, 0)
    const prevSeq = lastPushedSeq.get(pid) ?? 0
    const rewritten = lastPushedHistory.has(pid) && lastPushedHistory.get(pid) !== history
    if (rewritten || toSeq > prevSeq) {
      const { items: allItems } = stepsToItems(steps, { brainDir: bDir, full: false, clientMsgIdFor: clientMsgIdFor(rawId) })
      const deltaItems = rewritten ? allItems : allItems.filter((it) => it.seq > prevSeq)
      lastPushedSeq.set(pid, toSeq)
      lastPushedHistory.set(pid, history)
      rt.append(pid, {
        items: deltaItems,
        fromSeq: rewritten ? 0 : prevSeq,
        toSeq,
        history,
      })
      rt.sessionsChanged()
    }
  }

  function watchChanged(pid, watching, options) {
    clearInterval(watchedPolls.get(pid))
    watchedPolls.delete(pid)
    if (!watching || options?.transcript) return
    const timer = setInterval(() => {
      // Owned ACP turns already poll their export. Native terminal turns need the same committed-content feed.
      if (!activeTurns.has(pid)) syncTranscriptAppend(toAgyId(pid))
    }, 400)
    timer.unref?.()
    watchedPolls.set(pid, timer)
  }

  async function listSessions() {
    const { convs, cwdByConv } = readCacheMetadata()
    const states = readConversationStates()
    const brainRoot = join(agyDir, 'brain')
    const seenIds = new Set([...Object.keys(convs), ...states.keys(), ...createdAt.keys()])
    if (existsSync(brainRoot)) {
      try {
        for (const name of readdirSync(brainRoot)) {
          if (/^[0-9a-fA-F-]{36}$/.test(name)) seenIds.add(name)
        }
      } catch {}
    }

    const candidates = []
    for (const rawId of seenIds) {
      if (archived.has(rawId)) continue
      const meta = convs[rawId]
      const summary = meta?.summary
      const native = states.get(rawId)
      const pid = toPersonaId(rawId)
      const running = activeTurns.has(pid) || nativeWorking(native)
      const bDir = brainDirOf(rawId)
      const fullTranscript = join(bDir, '.system_generated', 'logs', 'transcript_full.jsonl')
      const transcriptFile = existsSync(fullTranscript) ? fullTranscript : join(bDir, '.system_generated', 'logs', 'transcript.jsonl')
      const hasTranscript = existsSync(transcriptFile)
      if (!hasTranscript && !running && !rt.touched(pid)) continue
      if (meta?.is_internal && !summary && !rt.touched(pid) && !running) continue

      let mtimeMs = Math.max(
        Date.parse(summary?.UpdatedAt || meta?.last_modified_time || '') || 0,
        Date.parse(native?.last_modified_time || '') || 0,
      )
      if (hasTranscript) {
        try {
          const st = statSync(transcriptFile)
          if (st.mtimeMs > mtimeMs) mtimeMs = st.mtimeMs
        } catch {}
      }
      if (!mtimeMs) mtimeMs = createdAt.get(rawId) || Date.now()

      const cwd = sessionCwds.get(rawId) || workspacePathOf(native, workspacePathOf(summary, cwdByConv[rawId] || home))
      // The node removes its temporary probe directory when the check ends. Keep those transcripts out after restarts.
      if (basename(cwd).startsWith('persona-check-') && !existsSync(cwd)) continue
      if (cwd) sessionCwds.set(rawId, cwd)
      if (isAssistantDir(cwd)) assistants.add(pid)

      candidates.push({ rawId, pid, summary, native, cwd, mtimeMs, running, hasTranscript })
    }

    const chosen = rt.recent(candidates, {
      days: listDays,
      max: maxSessions,
      pinned: (c) => c.running || assistants.has(c.pid),
      id: (c) => c.pid,
      updatedAt: (c) => c.mtimeMs,
    })

    return chosen.map(({ rawId, pid, summary, native, cwd, mtimeMs, running, hasTranscript }) => {
      const transcript = transcriptOf(rawId, native || null)
      if (!running) void readNativeError(rawId, native, transcript)
      const { steps, history } = transcript
      const parsed = stepsToItems(steps, { brainDir: brainDirOf(rawId), full: false, clientMsgIdFor: clientMsgIdFor(rawId) })
      if (!lastPushedSeq.has(pid)) lastPushedSeq.set(pid, parsed.toSeq)
      if (!lastPushedHistory.has(pid)) lastPushedHistory.set(pid, history)
      let status = running ? 'working' : transcriptStatus(steps, native)
      if (!hasTranscript && !running) {
        if (completedTurns.has(pid)) status = 'done'
        else if (createdAt.has(rawId)) status = 'idle'
      }
      const title =
        sessionTitles.get(rawId) ||
        native?.title ||
        summary?.Title ||
        (cwd ? basename(cwd) : 'Antigravity')
      const modeInfo = sessionModes.get(rawId)
      const failure = promptFailures.get(rawId)
      if (failure && failure.input !== inputKey(rawId)) promptFailures.delete(rawId)
      const row = {
        session_id: pid,
        title,
        cwd: cwd || home,
        status,
        updated_at: new Date(mtimeMs).toISOString(),
        rev: Math.max(1, parsed.toSeq),
        last_text: (hasTranscript || steps.length > 0) ? parsed.lastText : snippet(summary?.Preview || ''),
        model: sessionModels.get(rawId),
        launcher: 'agy',
        agent_label: LABEL,
        ...(!running && (parsed.turnError || promptFailures.get(rawId)?.fields)),
      }
      if (cwd) {
        row.space_id = cwd
        row.space_name = basename(cwd)
      }
      const effMode = effectiveModeOf(rawId)
      if (effMode && effMode !== 'default') row.permission_mode = effMode
      if (modeInfo?.effort) row.effort = modeInfo.effort
      return row
    })
  }

  // The agy id of one of this runtime's sessions. Every id it hands out carries PREFIX; anything else is another
  // runtime's. The node asks every runtime about a session nobody listed and gives it to the first that answers, so an
  // empty page here would take a new Claude Code session (no transcript yet) for an Antigravity one.
  function ownedId(sessionId) {
    const id = String(sessionId ?? '').trim()
    if (!id.startsWith(PREFIX)) throw new RpcError(`Antigravity has no session ${id}`, 'not_found')
    return toAgyId(id)
  }

  async function items(params) {
    const rawId = ownedId(params.session_id)
    await reconcileNativeCut(rawId)
    await readNativeError(rawId)
    const bDir = brainDirOf(rawId)
    const { steps, history } = transcriptOf(rawId)
    const { items: allItems, toSeq } = stepsToItems(steps, { brainDir: bDir, full: false, clientMsgIdFor: clientMsgIdFor(rawId) })
    return page(allItems, params, { history, toSeq })
  }

  async function cursor(sessionId) {
    const rawId = ownedId(sessionId)
    await reconcileNativeCut(rawId)
    await readNativeError(rawId)
    const { steps, history } = transcriptOf(rawId)
    const { toSeq } = stepsToItems(steps, { brainDir: brainDirOf(rawId), full: false, clientMsgIdFor: clientMsgIdFor(rawId) })
    return { to_seq: toSeq, history }
  }

  async function item(params) {
    const rawId = ownedId(params.session_id)
    await reconcileNativeCut(rawId)
    await readNativeError(rawId)
    const bDir = brainDirOf(rawId)
    const { steps } = transcriptOf(rawId)
    const { items: fullItems } = stepsToItems(steps, { brainDir: bDir, full: true, clientMsgIdFor: clientMsgIdFor(rawId) })
    const found = fullItems.find((it) => it.id === params.item_id)
    if (!found) throw new Error(`item ${params.item_id} not found`)
    return found
  }

  function mcpServersFor(pid, isAsst) {
    if (!existsSync(personadBin)) return []
    return mcpServers(personadBin, pid, isAsst)
  }

  /** A runtime call that uses agy acp, under a lease. A process from before the CLI signed in again is replaced first. */
  function busyCall(fn) {
    return withAcp(async () => {
      await recycleStale(1)
      return fn()
    })
  }

  async function check(params) {
    if (!params.cwd || !params.prompt) throw new Error('Availability checks require a temporary workspace and prompt')
    return busyCall(async () => {
      const started = Date.now()
      const deadline = started + (options.checkTimeoutMs ?? 75_000)
      const remaining = () => Math.max(1, deadline - Date.now())
      return retryStaleModels(async () => {
        let rawId
        try {
          await acp.ensureStarted()
          const session = await acp.call('session/new', { cwd: params.cwd, mcpServers: [] }, remaining())
          rawId = session.sessionId
          if (!rawId) throw new Error('Antigravity returned no check session')
          // Probes have no tools or phone permission cards. The existing archived-session guard cancels their requests.
          archived.add(rawId)
          const probe = { text: '' }
          checkTurns.set(rawId, probe)
          if (session.models?.availableModels) publishSessionModels(session.models.availableModels)
          const model = configuredModel()
          if (model) await setNativeModel(rawId, model, remaining())
          await acp.call('session/set_mode', { sessionId: rawId, modeId: 'default' }, remaining())
          const result = await acp.call('session/prompt', { sessionId: rawId, prompt: [{ type: 'text', text: params.prompt }] }, remaining())
          if (result?.stopReason !== 'end_turn') throw new Error(`Antigravity availability check ended with ${result?.stopReason || 'no stop reason'}`)
          if (!probe.text.trim()) throw new Error('Antigravity availability check returned no answer')
          return { ok: true, agent: RUNTIME, model: model || session.models?.currentModelId, answer: probe.text.trim().slice(0, 400), millis: Date.now() - started }
        } finally {
          if (rawId) {
            await acp.cancel(rawId)
            await acp.closeSession(rawId)
            checkTurns.delete(rawId)
            sessionModels.delete(rawId)
            sessionModes.delete(rawId)
          }
        }
      })
    })
  }

  async function applySessionOptions(rawId, { model, permission_mode, effort }) {
    try {
      if (model) {
        await setNativeModel(rawId, model)
        sessionModels.set(rawId, model)
        const selected = catalog.find((entry) => entry.id === model)
        if (selected?.default_effort) {
          const cur = sessionModes.get(rawId) || {}
          sessionModes.set(rawId, { ...cur, effort: selected.default_effort })
        }
      }
      if (permission_mode) {
        await acp.setMode(rawId, personaModeToAgy(permission_mode))
        const cur = sessionModes.get(rawId) || {}
        sessionModes.set(rawId, { ...cur, mode: permission_mode })
      }
      if (effort) {
        const res = await acp.setConfigOption(rawId, 'thought_level', effort)
        const cur = sessionModes.get(rawId) || {}
        sessionModes.set(rawId, { ...cur, effort })
        sessionConfiguration(rawId, res)
      }
    } finally {
      // A multi-field change may partially succeed. Publish each acknowledged field even when a later one fails.
      rt.sessionsChanged()
    }
  }

  async function loadSession(rawId, cwd, servers) {
    sessionConfiguration(rawId, await acp.ensureSessionLoaded(rawId, cwd, servers))
  }

  /** Create a native session with the requested or configured options; a session that refuses them is closed. */
  async function openSession(params, cwd, isAsst) {
    const res = await acp.newSession({ cwd, mcpServers: [] })
    const rawId = res.sessionId
    createdAt.set(rawId, Date.now())
    sessionConfiguration(rawId, res)
    const pid = toPersonaId(rawId)
    sessionCwds.set(rawId, cwd)
    if (isAsst) assistants.add(pid)

    try {
      await loadSession(rawId, cwd, mcpServersFor(pid, isAsst))
      await applySessionOptions(rawId, {
        model: params.model || configuredModel(),
        permission_mode: params.permission_mode || configuredMode(),
        effort: params.effort,
      })
    } catch (err) {
      archived.add(rawId)
      await acp.closeSession(rawId)
      rt.sessionsChanged()
      throw err
    }
    return rawId
  }

  async function start(params) {
    return busyCall(async () => {
      const cwd = params.cwd || home
      const isAsst = Boolean(params.assistant) || isAssistantDir(cwd)
      // A process that rejected the model gets replaced and the session opened again on the new one (#10368).
      const rawId = await retryStaleModels(() => openSession(params, cwd, isAsst))
      const pid = toPersonaId(rawId)

      const session = {
        session_id: pid,
        title: basename(cwd) || LABEL,
        cwd,
        status: params.prompt ? 'working' : 'idle',
        updated_at: new Date().toISOString(),
        rev: 1,
        model: sessionModels.get(rawId),
        launcher: 'agy',
        effort: sessionModes.get(rawId)?.effort,
        agent_label: LABEL,
        space_id: cwd,
        space_name: basename(cwd),
        permission_mode: effectiveModeOf(rawId) !== 'default' ? effectiveModeOf(rawId) : undefined,
      }

      if (params.prompt) {
        await send({
          session_id: pid,
          text: params.prompt,
          client_msg_id: `start-${Date.now()}`,
          assistant: isAsst,
          mode: params.permission_mode,
          model: params.model,
          effort: params.effort,
        })
      }

      rt.sessionsChanged()
      return { session }
    })
  }

  function beginLiveTurn(rawId, pid, turn) {
    clearExportPoll(rawId)
    const base = exportedTranscriptOf(rawId)
    const curMax = maxStepIndex(base.steps)
    const pending = unexportedTurns.get(rawId)
    const priorPending = pending && !pendingReplaced(pending, base.steps, curMax) ? pending.steps : []
    const baseMax = maxStepIndex([...base.steps, ...priorPending])
    turn.priorPending = priorPending
    turn.exportedBaseMax = curMax
    turn.baseMaxIndex = baseMax
    turn.startedIso = new Date().toISOString()
    if (turn.clientMsgId && turn.userText) {
      sentMsgIds.set(`${rawId}:${baseMax + 1}`, { text: turn.userText, clientMsgId: turn.clientMsgId })
      while (sentMsgIds.size > 512) sentMsgIds.delete(sentMsgIds.keys().next().value)
    }
    promptFailures.delete(rawId)
    promptTurns.set(pid, turn)
    rt.streamEnd(pid)
    syncTranscriptAppend(rawId)
  }

  async function send(params) {
    return busyCall(async () => {
      const rawId = ownedId(params.session_id)
      const pid = toPersonaId(rawId)
      if (rewinding.has(pid)) throw new RpcError('The conversation is being rewound', 'rewind_busy')
      completedTurns.delete(pid)
      if (archived.has(rawId)) throw new Error('Antigravity session is closed')
      const cwd = sessionCwds.get(rawId) || home
      const isAsst = Boolean(params.assistant) || assistants.has(pid) || isAssistantDir(cwd)
      if (isAsst) assistants.add(pid)

      await retryStaleModels(async () => {
        await loadSession(rawId, cwd, mcpServersFor(pid, isAsst))
        await applySessionOptions(rawId, {
          model: params.model,
          permission_mode: params.mode,
          effort: params.effort,
        })
      })

      if (rewinding.has(pid)) throw new RpcError('The conversation is being rewound', 'rewind_busy')

      const blocks = []
      let fullText = params.text || ''
      if (typeof params.context === 'string' && params.context.trim()) {
        const ctx = params.context.trim()
        const wrapped =
          contextWrapped(ctx)
            ? ctx
            : `<persona-context>\n${ctx}\n</persona-context>`
        fullText = `${wrapped}\n\n${fullText}`
      }
      if (fullText) {
        blocks.push({ type: 'text', text: fullText })
      }
      if (Array.isArray(params.images)) {
        for (const img of params.images) {
          if (img?.data) {
            blocks.push({
              type: 'image',
              data: img.data,
              mimeType: img.media_type || img.mime || 'image/png',
            })
          }
        }
      }

      const prevTurn = activeTurns.get(pid)
      if (prevTurn?.pollTimer) clearInterval(prevTurn.pollTimer)
      const pollTimer = setInterval(() => syncTranscriptAppend(rawId), 400)
      const turnToken = Symbol('turn')
      const turn = {
        seq: nextTurnSeq++,
        userText: cleanUserText(params.text || ''),
        clientMsgId: typeof params.client_msg_id === 'string' && params.client_msg_id ? params.client_msg_id : undefined,
        streamingText: '',
        liveThinking: '',
        liveTools: [],
        liveRev: 0,
        thoughtTimer: undefined,
        priorPending: [],
        exportedBaseMax: undefined,
        baseMaxIndex: undefined,
        startedIso: '',
        failed: false,
        newMessage: false,
        startedAt: Date.now(),
        pollTimer,
        token: turnToken,
      }
      if (!prevTurn && !archived.has(rawId)) {
        beginLiveTurn(rawId, pid, turn)
      }
      const prevPromise = prevTurn?.promise || Promise.resolve()
      leases++ // the turn's own lease, until it ends
      const promise = prevPromise
        .catch(() => {})
        .then(() => {
          if (archived.has(rawId)) return
          if (promptTurns.get(pid) !== turn) beginLiveTurn(rawId, pid, turn)
          return acp.prompt(rawId, blocks)
        })
        .catch((err) => {
          turn.failed = true
          if (!stopped && !cancelling.has(rawId)) {
            promptFailures.set(rawId, { input: inputKey(rawId), fields: {
              turn_error: err.message, turn_error_after_work: Boolean(turn.streamingText || turn.newMessage),
            } })
          }
          log(`agy prompt error (${pid}): ${err.message}`)
        })
        .finally(async () => {
          try {
            clearTimeout(turn.thoughtTimer)
            turn.thoughtTimer = undefined
            if (!stopped) await readNativeError(rawId)
            // The turn is over: a card still waiting has nothing to answer for any more.
            const wasCancelled = cancelling.has(rawId)
            cancelling.delete(rawId)
            withdrawPermissionAsks(rawId, 'the turn ended')
            const expectReply = Boolean(turn.streamingText.trim())
            let awaitExport = false
            if (!stopped && !turn.failed && !wasCancelled && !archived.has(rawId) && turn.exportedBaseMax !== undefined) {
              const exported = exportedTranscriptOf(rawId)
              const curMax = maxStepIndex(exported.steps)
              const exportedComplete = curMax > turn.exportedBaseMax && (!expectReply || exportedHasReply(exported.steps, turn.exportedBaseMax))
              if (curMax <= turn.exportedBaseMax) {
                const settled = [...turn.priorPending, ...liveTurnSteps(turn)]
                if (expectReply) {
                  settled.push({
                    step_index: (settled.length ? maxStepIndex(settled) : turn.baseMaxIndex) + 1,
                    type: 'PLANNER_RESPONSE',
                    source: 'MODEL',
                    status: 'DONE',
                    created_at: new Date().toISOString(),
                    content: turn.streamingText.trim(),
                  })
                }
                if (settled.length > 0) {
                  unexportedTurns.set(rawId, {
                    baseMaxIndex: turn.exportedBaseMax,
                    seq: turn.seq,
                    steps: settled,
                    hasReply: expectReply,
                    expiresAt: Date.now() + exportSettleTimeoutMs,
                  })
                  awaitExport = true
                }
              } else {
                unexportedTurns.delete(rawId)
                awaitExport = !exportedComplete
              }
            }
            if (promptTurns.get(pid) === turn) {
              promptTurns.delete(pid)
              rt.streamEnd(pid)
            }
            if (activeTurns.get(pid)?.token === turnToken) {
              clearInterval(pollTimer)
              activeTurns.delete(pid)
              completedTurns.add(pid)
              syncTranscriptAppend(rawId)
              rt.sessionsChanged(0)
              if (awaitExport && turn.exportedBaseMax !== undefined) {
                scheduleExportPoll(rawId, turn.exportedBaseMax, expectReply)
              }
            }
          } finally {
            leases--
            if (want) void maybeExit()
          }
        })
      turn.promise = promise
      activeTurns.set(pid, turn)
      rt.sessionsChanged(0)
    })
  }

  async function interrupt(params) {
    const rawId = ownedId(params.session_id)
    return busyCall(async () => {
      if (activeTurns.has(toPersonaId(rawId))) cancelling.add(rawId)
      withdrawPermissionAsks(rawId, 'interrupted')
      await acp.cancel(rawId)
    })
  }

  async function rename(params) {
    const rawId = ownedId(params.session_id)
    if (typeof params.title === 'string' && params.title.trim()) {
      sessionTitles.set(rawId, params.title.trim())
      rt.sessionsChanged()
    }
  }

  async function close(params) {
    const rawId = ownedId(params.session_id)
    return busyCall(async () => {
      archived.add(rawId)
      clearExportPoll(rawId)
      unexportedTurns.delete(rawId)
      await interrupt(params)
      await acp.closeSession(rawId)
      rt.sessionsChanged()
    })
  }

  async function rewind(params) {
    return busyCall(() => rewindConversation(params))
  }

  async function rewindConversation(params) {
    const rawId = ownedId(params.session_id)
    const pid = toPersonaId(rawId)
    if (params.restore !== 'conversation' || assistants.has(pid) || isAssistantDir(sessionCwds.get(rawId) || '')) {
      throw new RpcError('This session supports conversation rewind', 'rewind_unsupported')
    }
    if (typeof params.prompt !== 'string' || !params.prompt.trim() || !Number.isInteger(params.occurrence) || params.occurrence < 1 ||
        (params.prompts_after !== undefined && (!Number.isInteger(params.prompts_after) || params.prompts_after < 0))) {
      throw new RpcError('A rewind needs the message and its current position', 'bad_params')
    }
    if (rewinding.has(pid) || activeTurns.has(pid) || permissionAsks.get(rawId)?.size || nativeWorking(readConversationStates().get(rawId))) {
      throw new RpcError('Wait for the current turn to finish before rewinding', 'rewind_busy')
    }
    if (archived.has(rawId)) throw new RpcError('The session is closed', 'rewind_unsupported')
    rewinding.add(pid)
    try {
      const cwd = sessionCwds.get(rawId) || home
      await loadSession(rawId, cwd, mcpServersFor(pid, false))
      const snapshot = await acp.nativeRequest('GetCascadeTrajectory', { cascadeId: rawId,
        verbosity: 'CLIENT_TRAJECTORY_VERBOSITY_PROD_UI', trajectoryVerbosity: 'CLIENT_TRAJECTORY_VERBOSITY_PROD_UI', disableRehydration: true })
      if (snapshot.trajectory?.cascadeId !== rawId) throw new RpcError('The native conversation changed', 'rewind_mismatch')
      if (snapshot.status !== 'CASCADE_RUN_STATUS_IDLE') throw new RpcError('The native conversation is busy', 'rewind_busy')
      const squash = (text) => text.replace(/\s+/g, '')
      const exported = readTranscript(brainDirOf(rawId), true).steps
      const inputs = (snapshot.trajectory.steps || []).flatMap((step, index) =>
        step.type === 'CORTEX_STEP_TYPE_USER_INPUT' && step.metadata?.source === 'CORTEX_STEP_SOURCE_USER_EXPLICIT'
          ? [{ index, text: cleanUserText((step.userInput?.items || []).map((item) => item.text || '').join('\n')),
              shown: cleanUserText(exported.find((entry) => entry.step_index === index)?.content) }] : [])
      const matches = inputs.filter((input) => {
        const shown = input.shown || input.text
        // Plan mode exports its /plan command, while the trajectory keeps the original words.
        return squash(shown) === squash(params.prompt) && squash(shown.replace(/^\/plan\s+/, '')) === squash(input.text)
      })
      const target = matches.at(-params.occurrence)
      if (!target || (params.prompts_after !== undefined && inputs.length - 1 - inputs.indexOf(target) !== params.prompts_after)) {
        throw new RpcError('The selected message moved. Reload the conversation before rewinding', 'rewind_mismatch')
      }
      const before = transcriptOf(rawId)
      clearExportPoll(rawId)
      unexportedTurns.delete(rawId)
      for (const k of sentMsgIds.keys()) if (k.startsWith(`${rawId}:`)) sentMsgIds.delete(k)
      lastPushedHistory.set(pid, before.history)
      lastPushedSeq.set(pid, stepsToItems(before.steps).toSeq)
      await acp.nativeRequest('RevertToCascadeStep', { cascadeId: rawId, stepIndex: target.index - 1,
        conversationOnly: true, suppressErrorStep: true })
      const after = await acp.nativeRequest('GetCascadeTrajectory', { cascadeId: rawId, disableRehydration: true })
      if (after.trajectory?.cascadeId !== rawId || (after.trajectory.steps || []).length !== target.index) {
        throw new RpcError('The native conversation changed during rewind. Reload its history', 'rewind_failed')
      }
      rememberCut(rawId, readTranscript(brainDirOf(rawId), true), target.index)
      // Native exports retain the old branch until replacement steps arrive. Its durable summary supplies the cut.
      const deadline = Date.now() + 5000
      while (readConversationStates().get(rawId)?.step_count !== target.index) {
        if (Date.now() >= deadline) throw new RpcError('The native rewind completed and its history is still synchronizing', 'rewind_failed')
        await new Promise((resolve) => setTimeout(resolve, 50))
      }
      rt.streamEnd(pid)
      syncTranscriptAppend(rawId)
      rt.sessionsChanged()
      return { prompt: target.text, restore: 'conversation', code_restored: false, input_cleared: true }
    } finally {
      rewinding.delete(pid)
      if (want) void maybeExit()
    }
  }

  async function modes(params) {
    return busyCall(async () => {
      const rawId = ownedId(params.session_id)
      if (archived.has(rawId)) throw new Error('Antigravity session is closed')
      if (params.model && (activeTurns.has(toPersonaId(rawId)) || nativeWorking(readConversationStates().get(rawId)))) {
        throw new Error('Antigravity session is busy')
      }
      const cwd = sessionCwds.get(rawId) || home
      await retryStaleModels(async () => {
        await loadSession(rawId, cwd, mcpServersFor(toPersonaId(rawId), assistants.has(toPersonaId(rawId))))
        await applySessionOptions(rawId, {
          model: params.model,
          permission_mode: params.permission_mode,
          effort: params.effort,
        })
      })
      rt.sessionsChanged()
    })
  }

  /** No lease, replacement, owned ACP turn, probe, rewind or phone card is in flight. */
  function idle() {
    return leases === 0 && !recycling && activeTurns.size === 0 && rewinding.size === 0 && checkTurns.size === 0 && !rt.busy()
  }

  async function maybeExit() {
    if (!want || !update?.exit || stopped || exiting || !idle()) return
    exiting = true
    const target = want
    clearInterval(idleTimer)
    await stop()
    update.exit(target)
  }

  async function takeUpdate(offer) {
    if (!update || installing || stopped || exiting || offer?.name !== update.name ||
        offer.hash === update.hash || offer.hash === want || bad.has(offer.hash)) return
    installing = true
    try {
      const { hash, files } = verifyPackage(await rt.fetchPackage(update.name), update.name)
      if (stopped || exiting || hash === update.hash || bad.has(hash)) return
      installCurrent(update.store, hash, files)
      log(`installed ${update.name} ${hash.slice(0, 12)} from the node (running ${update.hash.slice(0, 12)})`)
      if (!update.exit) return
      want = hash
      clearInterval(idleTimer)
      idleTimer = setInterval(() => { void maybeExit() }, idleCheckMs)
      idleTimer.unref?.()
      await maybeExit()
    } catch (err) {
      bad.add(offer.hash)
      log(`${update.name} update to ${String(offer?.hash || '').slice(0, 12)} failed: ${err.message}`)
    } finally {
      installing = false
    }
  }

  /** Resolves once agy is gone. */
  function stop() {
    stopped = true
    clearInterval(idleTimer)
    clearInterval(catalogTimer)
    catalogAbort.abort()
    rt.stop()
    // A turn's transcript poll would otherwise keep running; acp.stop() fails the prompts themselves.
    for (const turn of activeTurns.values()) {
      clearInterval(turn.pollTimer)
      clearTimeout(turn.thoughtTimer)
    }
    for (const timer of exportPolls.values()) clearInterval(timer)
    exportPolls.clear()
    activeTurns.clear()
    promptTurns.clear()
    unexportedTurns.clear()
    return acp.stop()
  }

  return {
    start: () => {
      void models().catch((err) => log(`agy model discovery error: ${err.message}`))
      catalogTimer = setInterval(() => { void models({ refresh: true }).catch((err) => log(`agy model discovery error: ${err.message}`)) }, 300_000)
      catalogTimer.unref?.()
      return rt.start()
    },
    stop,
    idle,
  }
}
