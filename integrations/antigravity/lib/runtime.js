import { daemonBinary, mcpServers } from './compat.js'
import { contextWrapped, wirePackageName } from '../../runtime-sdk/lib/compat.js'
// Antigravity (agy) runtime for Persona (docs/integrations/runtimes.md).
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { basename, join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { setTimeout as delay } from 'node:timers/promises'
import { pathToFileURL } from 'node:url'
import { createRuntime, installCurrent, interactionId, page, RpcError, snippet, summaryOf, verifyPackage } from '../../runtime-sdk/index.js'
import { isAssistantDir } from '../../runtime-sdk/lib/identity.js'
import { AgyAcpClient } from './acp.js'
import { readMediaImage } from './media.js'
import { discoverModels, mapModels, SIGNIN_TIMEOUT } from './models.js'
import { nativeErrorStep } from './errors.js'
import { isPolicyDir, POLICY_DIR_NAME, policyDirOf, readPolicy, removePolicy, splitPolicy, SUPERVISION_ERROR_CODE, syncPolicy } from './policy.js'
import {
  agyModeToPersona,
  cleanUserText,
  forkReplyPosition,
  forkStepIndex,
  LABEL,
  LIVE_TOOL_USE_ID,
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
// The hub localizes this exact runtime notice (transcript.RuntimeAgyAutoRefusalNotice).
const AUTO_REFUSAL_NOTICE = 'agy 拒绝了 Auto 模式，已改由 Persona 自动批准权限请求'
const MODE_NOTICE_STEP = 'PERSONA_MODE_NOTICE'
const CONVERSATION_ID = /^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/i

export function createAgyRuntime(options = {}) {
  const home = options.home || homedir()
  const agyDir = options.agyDir || process.env.ANTIGRAVITY_CLI_CONFIG_DIR || join(home, '.gemini', 'antigravity-cli')
  const policyRoot = options.policyRoot || join(agyDir, POLICY_DIR_NAME)
  const skipPolicyDir = (p) => isPolicyDir(policyRoot, p)
  const personadBin = daemonBinary(home, options)
  const agyBin = options.agyBin || join(home, '.local', 'bin', 'agy')
  const log = options.log || ((m) => console.error(m))
  const listDays = options.listDays ?? 14
  const maxSessions = options.maxSessions ?? 80
  const exportPollMs = options.exportPollMs ?? 400
  const exportSettleTimeoutMs = options.exportSettleTimeoutMs ?? 15_000
  const autonomousIdleMs = options.autonomousIdleMs ?? 30_000
  const transient401RetryDelayMs = options.transient401RetryDelayMs ?? 1500

  const activeTurns = new Map() // personaId -> { streamingText, startedAt }
  const promptTurns = new Map() // the prompt executing, before any queued successor
  const autonomousTurns = new Map()
  const missedWakes = new Map()
  const unexportedTurns = new Map() // agyId -> { baseMaxIndex, seq, steps, hasReply, base } settled turns awaiting native transcript export
  const exportPolls = new Map() // agyId -> interval watching for a settled turn's native transcript flush
  const sentMsgIds = new Map() // `${agyId}:${stepIndex}` -> { text, clientMsgId }
  let nextTurnSeq = 1
  const createdAt = new Map() // owned sessions remain discoverable before their first transcript export
  const forkSeeds = new Map()
  const completedTurns = new Set() // owned ACP turns that ended before a native transcript was available
  const sessionTitles = new Map() // agyId -> title
  const sessionCwds = new Map() // agyId -> cwd
  const sessionModels = new Map() // agyId -> modelId
  const sessionModes = new Map() // agyId -> { mode, requestedMode, modeSource, effort, autoRequested, autoRefused, applyingAuto }
  const autoFallbacks = new Map() // agyId -> the pending change from refused auto to Persona-side automatic approval
  const modeNotices = new Map() // agyId -> { at, created_at }, the one refusal notice woven into reloadable items
  const archived = new Set() // agyId
  const lastPushedSeq = new Map() // personaId -> number
  const lastPushedHistory = new Map()
  const dirtyLiveSeqs = new Map() // personaId -> Set<number> of already-pushed live seqs modified in place
  const watchedPolls = new Map()
  const rewinding = new Set()
  const forking = new Set()
  const nativeCuts = new Map()
  const cutChecks = new Map()
  const nativeTails = new Map()
  const promptFailures = new Map()
  const promptRetries = new Map()
  const assistants = new Set() // personaId
  const checkTurns = new Map() // availability probes collect text without appearing in user conversations
  const permissionAsks = new Map() // agyId -> Set of AbortControllers, one per permission card still on the phone
  const cancelling = new Set() // agyId: interrupted, its turn has not ended yet
  // agyId -> native step count at cancel (#11108) or wake timeout. Ignore a stale running summary until new work.
  const staleWorkingAt = new Map()
  const interruptSettleMs = options.interruptSettleMs ?? 10_000

  const acp = new AgyAcpClient({
    bin: existsSync(agyBin) ? agyBin : 'agy',
    ...(Array.isArray(options.agyArgs) ? { args: options.agyArgs } : {}),
    log,
    promptInactivityMs: options.promptInactivityMs,
    onPermissionRequest: async (params) => handlePermissionRequest(params),
  })

  let catalog = []
  let catalogAt = 0
  let catalogFlight
  let catalogTimer
  // Discovery runs every catalogEveryMs; each discovery in a row that times out on an expired sign-in doubles the wait,
  // up to catalogMaxMs, instead of holding a hung agy for 25 s every 5 minutes (#11106).
  const catalogEveryMs = options.catalogEveryMs ?? 300_000
  const catalogMaxMs = options.catalogMaxMs ?? 3_600_000
  let catalogTimeouts = 0
  // One agy acp process can keep the model list it had when the account's plan lapsed or the CLI signed out (#10368).
  let discoveredGeneration = -1 // the agy acp generation running when the latest successful discovery began
  let signedOut = false // the latest discovery failed because the CLI is signed out
  let staleThrough = 0 // agy acp generations up to this one were running before the CLI signed in again
  let stalledGeneration = 0
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
      catalogFlight = discoverModels(acp.bin, { signal: catalogAbort.signal, timeout: options.modelsTimeoutMs ?? 25_000 }).then((entries) => {
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
        if (err?.code === SIGNIN_TIMEOUT || SIGNED_OUT.test(err?.message || '')) signedOut = true
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

  /** Whether anything besides the caller's own `own` leases uses agy acp. */
  function acpBusy(own) {
    return leases > own || acp.pending.size > 0 || autonomousTurns.size > 0 || rt.busy()
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
    if (!acp.proc) return
    if (acp.generation === stalledGeneration) await replaceAcp(own, 'an Antigravity prompt stalled')
    else if (acp.generation <= staleThrough) await replaceAcp(own, 'the Antigravity CLI signed in again')
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

  function recordNativeMode(rawId, nativeMode, modeRequest = false) {
    if (typeof nativeMode !== 'string' || !nativeMode) return
    const cur = sessionModes.get(rawId) || {}
    const mapped = agyModeToPersona(nativeMode)
    // Model and config-option updates are not correlated with this request. Only its direct result or ACP's
    // mode-specific current_mode_update can acknowledge or refuse auto.
    if (cur.autoRequested && !modeRequest) return
    if (cur.autoRequested && mapped === 'default') {
      sessionModes.set(rawId, { ...cur, mode: 'default', autoRefused: true })
      if (!cur.applyingAuto) {
        void fallbackRefusedAuto(rawId).catch((error) => log(`agy auto fallback: ${error.message}`))
      }
      return
    }
    const initialBypass = !cur.mode && !cur.autoRequested && configuredMode() === 'bypassPermissions'
    const keepBypass = cur.mode === 'bypassPermissions' && ['acceptEdits', 'default'].includes(mapped)
    sessionModes.set(rawId, {
      ...cur,
      mode: initialBypass || keepBypass ? 'bypassPermissions' : mapped,
      autoRequested: mapped === 'auto' ? cur.autoRequested : false,
      autoRefused: mapped === 'auto' ? false : cur.autoRefused,
    })
  }

  function sessionConfiguration(rawId, result, modeRequest = false) {
    if (!result) return
    if (result.models?.availableModels) publishSessionModels(result.models.availableModels)
    if (result.models?.currentModelId) sessionModels.set(rawId, result.models.currentModelId)
    recordNativeMode(rawId, result.currentModeId, modeRequest)
    recordNativeMode(rawId, result.modes?.currentModeId, modeRequest)
    const config = result.configOptions
    if (Array.isArray(config)) {
      const model = config.find((option) => option.category === 'model' || option.id === 'model')?.currentValue
      const effort = config.find((option) => option.id === 'thought_level')?.currentValue
      const mode = config.find((option) => option.id === 'mode')?.currentValue
      const cur = sessionModes.get(rawId) || {}
      sessionModes.set(rawId, { ...cur, ...(effort ? { effort } : {}) })
      recordNativeMode(rawId, mode, modeRequest)
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
      image,
      send,
      interrupt,
      start,
      rename,
      close,
      modes,
      models,
      check,
      rewind,
      fork,
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
      readonly: true,
      supervisionPolicy: true,
      socketPath: options.socketPath,
      instance: options.instance,
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
          recordNativeMode(rawId, upd.currentModeId, true)
          rt.sessionsChanged()
        }
        break
      case 'agent_thought_chunk': {
        const chunk = upd.content?.text
        const turn = typeof chunk === 'string' && chunk && liveTurnForUpdate(rawId, pid, Boolean(chunk.trim()))
        if (turn) {
          let changed = false
          if (turn.activeThoughtStep) {
            const before = turn.activeThoughtStep.thinking.trim()
            turn.activeThoughtStep.thinking += chunk
            if (turn.activeThoughtStep.thinking.trim() !== before) {
              markDirtySeq(pid, turn.activeThoughtStep.step_index + 1)
              changed = true
            }
          } else {
            turn.pendingThought += chunk
            if (turn.pendingThought.trim()) {
              const step = {
                step_index: turn.nextStepIndex++,
                type: 'PLANNER_RESPONSE',
                source: 'MODEL',
                status: 'RUNNING',
                created_at: turn.startedIso,
                thinking: turn.pendingThought,
              }
              turn.liveSteps.push(step)
              turn.activeThoughtStep = step
              turn.pendingThought = ''
              changed = true
            }
          }
          if (changed && !turn.thoughtTimer) {
            turn.thoughtTimer = setTimeout(() => {
              turn.thoughtTimer = undefined
              if (liveTurnOf(pid) === turn) syncTranscriptAppend(rawId)
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
          if (probe) {
            probe.text += chunk
            break
          }
          const turn = liveTurnForUpdate(rawId, pid, Boolean(chunk.trim()))
          if (turn) {
            if (turn.newMessage) turn.streamingText = ''
            turn.newMessage = false
            turn.streamingText += chunk
            if (turn.streamingText.trim()) {
              turn.activeThoughtStep = null
              turn.pendingThought = ''
            }
            rt.stream(pid, turn.streamingText)
          }
        }
        break
      }
      case 'tool_call': {
        const turn = liveTurnForUpdate(rawId, pid)
        if (!turn) break
        recordLiveTool(pid, turn, upd)
        syncTranscriptAppend(rawId)
        break
      }
      case 'tool_call_update': {
        const turn = upd.toolCallId && liveTurnForUpdate(rawId, pid, true, upd.toolCallId)
        if (turn) {
          turn.toolActivity = true
          const existing = turn.liveToolsById.get(upd.toolCallId) ||
            (autonomousTurns.get(pid) === turn && recordLiveTool(pid, turn, upd))
          if (existing && (upd.rawInput || upd.input)) {
            const nextArgs = upd.rawInput || upd.input
            const cur = existing.step.tool_calls?.[0]
            const nextName = upd._meta?.toolName || upd.name || cur?.name || 'tool'
            if (cur?.name !== nextName || JSON.stringify(cur?.args) !== JSON.stringify(nextArgs)) {
              existing.step.tool_calls = [{ [LIVE_TOOL_USE_ID]: existing.callId, name: nextName, args: nextArgs }]
              markDirtySeq(pid, existing.step.step_index + 1)
            }
          }
          if (existing && ['completed', 'failed'].includes(upd.status)) {
            const isError = upd.status === 'failed'
            const output = Array.isArray(upd.content)
              ? upd.content.map((b) => (b?.type === 'content' ? b.content?.text || '' : b?.text || '')).filter(Boolean).join('\n')
              : typeof upd.rawOutput === 'string' ? upd.rawOutput : ''
            existing.step.status = 'DONE'
            if (existing.resultStep) {
              existing.resultStep.status = isError ? 'ERROR' : 'DONE'
              existing.resultStep.content = output
              if (isError) existing.resultStep.error = output || 'Tool failed'
              else delete existing.resultStep.error
              markDirtySeq(pid, existing.resultStep.step_index + 1)
            } else {
              const resultStep = {
                [LIVE_TOOL_USE_ID]: existing.callId,
                step_index: turn.nextStepIndex++,
                type: 'GENERIC',
                source: 'MODEL',
                status: isError ? 'ERROR' : 'DONE',
                created_at: turn.startedIso,
                content: output,
                ...(isError ? { error: output || 'Tool failed' } : {}),
              }
              turn.liveSteps.push(resultStep)
              existing.resultStep = resultStep
            }
          }
          syncTranscriptAppend(rawId)
        }
        break
      }
      default:
        break
    }
  })

  function recordLiveTool(pid, turn, upd) {
    turn.toolActivity = true
    if (turn.streamingText.trim()) {
      turn.liveSteps.push({ step_index: turn.nextStepIndex++, type: 'PLANNER_RESPONSE', source: 'MODEL',
        status: 'DONE', created_at: turn.startedIso, content: turn.streamingText })
      turn.streamingText = ''
      rt.streamEnd(pid)
    }
    turn.newMessage = true
    turn.activeThoughtStep = null
    turn.pendingThought = ''
    const id = upd.toolCallId || `live-${turn.nextToolNum}`
    const existing = turn.liveToolsById.get(id)
    const name = upd._meta?.toolName || upd.name || upd.title || existing?.step.tool_calls[0].name || 'tool'
    const args = upd.rawInput || upd.input || existing?.step.tool_calls[0].args || {}
    if (existing) {
      existing.step.tool_calls = [{ [LIVE_TOOL_USE_ID]: existing.callId, name, args }]
      markDirtySeq(pid, existing.step.step_index + 1)
      return existing
    }
    const callId = `live_call_${turn.seq}_${turn.nextToolNum++}`
    const step = {
      step_index: turn.nextStepIndex++,
      type: 'PLANNER_RESPONSE',
      source: 'MODEL',
      status: 'RUNNING',
      created_at: turn.startedIso,
      tool_calls: [{ [LIVE_TOOL_USE_ID]: callId, name, args }],
    }
    const tool = { callId, step, resultStep: null }
    turn.liveSteps.push(step)
    turn.liveToolsById.set(id, tool)
    return tool
  }

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
    if (cur?.modeSource === 'settings') {
      const configured = configuredMode()
      if (configured === 'auto' && cur.autoRefused) return cur.mode || 'default'
      return configured
    }
    return cur?.requestedMode || cur?.mode || configuredMode()
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
    const turn = promptTurns.get(pid)
    if (turn) turn.toolActivity = true
    const tc = params?.toolCall || {}
    const rawName = tc.title || tc.name || 'run_command'
    const { toolName, toolInput } = mapToolCall(rawName, tc.rawInput || tc.input || {})
    const opts = Array.isArray(params?.options) ? params.options : []
    const allowAlwaysOpt = opts.find((o) => o.kind === 'allow_always') || opts.find((o) => o.kind === 'allow_once') || opts[0]
    const allowOpt = opts.find((o) => o.kind === 'allow_once') || allowAlwaysOpt
    const rejectOpt = opts.find((o) => o.kind === 'reject_once') || opts.find((o) => o.kind === 'reject_always')

    // A request that crossed the interrupt on the wire: ACP wants every pending one answered 'cancelled'.
    if (cancelling.has(rawId) || archived.has(rawId)) return { outcome: { outcome: 'cancelled' } }
    const fallback = autoFallbacks.get(rawId)
    if (fallback) {
      try { await fallback } catch { /* a failed fallback leaves the session asking */ }
    }
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
        id: interactionId(RUNTIME, pid, 'permission', tc.toolCallId || String(Date.now())),
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
      const wake = autonomousTurns.get(pid)
      if (wake) wake.lastUpdate = Date.now()
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
    // Refresh cached transcripts that did not include lazy tool images.
    return `agy:${rawId}:full:api-errors:content-v2:step-ids-v2:mcp-tools:lazy-tool-media`
  }

  function cutMatches(cut, transcript) {
    return cut?.branch === transcript.branch && cut.next === JSON.stringify(transcript.steps.find((step) => step.step_index >= cut.limit))
  }

  function rememberCut(rawId, transcript, limit) {
    nativeCuts.set(rawId, { branch: transcript.branch, limit,
      next: JSON.stringify(transcript.steps.find((step) => step.step_index >= limit)) })
  }

  function exportedTranscriptOf(rawId) {
    const transcript = readTranscript(brainDirOf(rawId), true)
    const seed = forkSeeds.get(rawId)
    if (seed) {
      if (!transcript.steps.length) return { steps: seed, history: `${historyOf(rawId)}:fork` }
      forkSeeds.delete(rawId)
    }
    const verified = nativeCuts.get(rawId)
    // Summary timestamps can lead their step counts. Only a verified native rewind may hide exported steps.
    const limit = cutMatches(verified, transcript) ? verified.limit : undefined
    const steps = Number.isInteger(limit) && limit >= 0
      ? transcript.steps.filter((step, index) => (step.step_index ?? index) < limit)
      : transcript.steps
    const compact = transcript.compact ? ':compact' : ''
    const branch = transcript.branch ? `:branch:${transcript.branch}` : ''
    const revision = transcript.revision ? `:revision:${transcript.revision}` : ''
    const cut = steps.length < transcript.steps.length ? `:cut:${limit}` : ''
    return { steps: withoutRetriedInputs(rawId, steps), history: historyOf(rawId) + compact + branch + revision + cut }
  }

  function tailKey(transcript, native) {
    const last = transcript.steps.at(-1)
    return [transcript.history, last?.step_index, last?.created_at, native?.step_count, native?.last_modified_time].join('|')
  }

  function maxStepIndex(steps) {
    return steps.reduce((max, step, index) => Math.max(max, typeof step?.step_index === 'number' ? step.step_index : index), -1)
  }

  function explicitInput(step) {
    return step.type === 'USER_INPUT' && (!step.source || step.source === 'USER_EXPLICIT')
  }

  function explicitInputs(steps) {
    return steps.filter(explicitInput).length
  }

  function exportedHasReply(steps, baseMaxIndex, replyText, inputCount = 0, allowTruncated = false) {
    let inputs = 0
    let last
    for (const [index, step] of steps.entries()) {
      if (explicitInput(step)) inputs++
      if (inputCount > 0 && inputs > inputCount) break
      if (inputCount > 0 && inputs !== inputCount) continue
      if ((step.step_index ?? index) > baseMaxIndex &&
          ['USER_INPUT', 'PLANNER_RESPONSE', 'GENERIC', 'ERROR_MESSAGE'].includes(step.type)) last = step
    }
    const fragments = allowTruncated && last?.truncated_fields?.includes('content') && typeof last.content === 'string'
      ? last.content.split(/<truncated \d+ bytes>/) : []
    const truncatedMatch = fragments.length === 2 && replyText?.trim().startsWith(fragments[0].trimEnd()) &&
      replyText.trim().endsWith(fragments[1].trimStart())
    return last?.type === 'ERROR_MESSAGE' || (last?.type === 'PLANNER_RESPONSE' &&
      !last.tool_calls?.length && last.status !== 'RUNNING' && typeof last.content === 'string' && Boolean(last.content.trim()) &&
      (!replyText || last.content.trim() === replyText.trim() || truncatedMatch))
  }

  function pendingReplaced(pending, steps, curMax = maxStepIndex(steps)) {
    if (pending.inputCount > 0 && explicitInputs(steps) > pending.inputCount) return true
    // Only an explicitly truncated reply may replace mismatching live text after the export grace period.
    return curMax > pending.baseMaxIndex &&
      (!pending.hasReply || exportedHasReply(steps, pending.baseMaxIndex, pending.replyText, pending.inputCount, Date.now() >= pending.expiresAt))
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

  function markDirtySeq(pid, seq) {
    let set = dirtyLiveSeqs.get(pid)
    if (!set) dirtyLiveSeqs.set(pid, (set = new Set()))
    set.add(seq)
  }

  function transcriptOf(rawId, native = readConversationStates().get(rawId)) {
    const transcript = exportedTranscriptOf(rawId)
    const curMax = maxStepIndex(transcript.steps)
    const pid = toPersonaId(rawId)
    const turn = liveTurnOf(pid)
    if (turn?.liveSteps.length) {
      return {
        ...transcript,
        steps: [...turn.base.steps, ...turn.priorPending, ...turn.liveSteps],
        // Live and settled prompt steps share a history epoch until native export flushes to disk.
        history: `${turn.base.history}:settled:${turn.historySeq ?? turn.seq}`,
        synthetic: true,
      }
    }
    const pending = unexportedTurns.get(rawId)
    if (pending) {
      if (pendingReplaced(pending, transcript.steps, curMax)) {
        unexportedTurns.delete(rawId)
      } else {
        return {
          ...transcript,
          steps: [...pending.base.steps, ...pending.steps],
          history: `${pending.base.history}:settled:${pending.seq}`,
          synthetic: true,
        }
      }
    }
    const tail = nativeTails.get(rawId)
    if (tail?.key === tailKey(transcript, native) && tail.error) {
      return { ...transcript, steps: [...transcript.steps, tail.error] }
    }
    return transcript
  }

  // Keep the refusal notice at its original position when later native steps arrive. Their projected indices move
  // behind it, so refetches and append batches see the same notice instead of losing it or reusing its sequence.
  function withModeNotice(rawId, transcript) {
    const notice = modeNotices.get(rawId)
    if (!notice) return transcript
    const steps = []
    let inserted = false
    for (let i = 0; i < transcript.steps.length; i++) {
      const step = transcript.steps[i]
      const index = typeof step?.step_index === 'number' ? step.step_index : i
      if (!inserted && index >= notice.at) {
        steps.push({ step_index: notice.at, type: MODE_NOTICE_STEP, status: 'DONE', created_at: notice.created_at, content: AUTO_REFUSAL_NOTICE })
        inserted = true
      }
      steps.push(index >= notice.at ? { ...step, step_index: index + 1, native_step_index: step.native_step_index ?? index } : step)
    }
    if (!inserted) steps.push({ step_index: notice.at, type: MODE_NOTICE_STEP, status: 'DONE', created_at: notice.created_at, content: AUTO_REFUSAL_NOTICE })
    return { ...transcript, steps, history: `${transcript.history}:auto-refusal:${notice.at}` }
  }

  function rememberAutoRefusalNotice(rawId) {
    if (modeNotices.has(rawId)) return
    modeNotices.set(rawId, { at: maxStepIndex(transcriptOf(rawId).steps) + 1, created_at: new Date().toISOString() })
    syncTranscriptAppend(rawId)
  }

  function withoutStaleWorking(rawId, native) {
    const mark = staleWorkingAt.get(rawId)
    if (mark === undefined || !native) return native
    if (Number.isInteger(native.step_count) && native.step_count > mark) {
      staleWorkingAt.delete(rawId)
      return native
    }
    return nativeWorking(native) ? { ...native, status: 'CASCADE_RUN_STATUS_IDLE', not_fully_idle: false } : native
  }

  function readNativeError(rawId, native = readConversationStates().get(rawId), transcript = exportedTranscriptOf(rawId)) {
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
        await acp.ensureSessionLoaded(rawId, sessionCwds.get(rawId) || home, [], extraDirsFor(rawId))
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
    const { steps, history, synthetic } = withModeNotice(rawId, transcriptOf(rawId))
    const toSeq = steps.reduce((max, step, index) => {
      const at = typeof step?.step_index === 'number' ? step.step_index : index
      return Math.max(max, at + 1)
    }, 0)
    const prevSeq = lastPushedSeq.get(pid) ?? 0
    const rewritten = lastPushedHistory.has(pid) && lastPushedHistory.get(pid) !== history
    const dirty = synthetic ? dirtyLiveSeqs.get(pid) : undefined
    if (!synthetic) dirtyLiveSeqs.delete(pid)
    const hasDirty = Boolean(dirty && dirty.size > 0)
    if (rewritten || toSeq > prevSeq || hasDirty) {
      const { items: allItems } = stepsToItems(steps, { brainDir: bDir, full: false, includeMedia: true, clientMsgIdFor: clientMsgIdFor(rawId) })
      const deltaItems = rewritten
        ? allItems
        : allItems.filter((it) => it.seq > prevSeq || Boolean(dirty?.has(it.seq)))
      dirtyLiveSeqs.delete(pid)
      if (!rewritten && toSeq <= prevSeq && deltaItems.length === 0) return
      const update = !rewritten && deltaItems.some((it) => it.seq <= prevSeq)
      lastPushedSeq.set(pid, toSeq)
      lastPushedHistory.set(pid, history)
      rt.append(pid, {
        items: deltaItems,
        fromSeq: rewritten ? 0 : prevSeq,
        toSeq,
        history,
        ...(update ? { update: true } : {}),
      })
      if (rewritten || toSeq > prevSeq) rt.sessionsChanged()
    }
  }

  function watchChanged(pid, watching, options) {
    clearInterval(watchedPolls.get(pid))
    watchedPolls.delete(pid)
    if (!watching || options?.transcript) return
    const rawId = ownedId(pid)
    const timer = setInterval(() => {
      // Tracked ACP turns already poll their export. Native terminal turns need the same committed-content feed.
      if (!hasLiveWork(pid)) syncTranscriptAppend(rawId)
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
          if (CONVERSATION_ID.test(name)) seenIds.add(name)
        }
      } catch {}
    }

    const candidates = []
    for (const rawId of seenIds) {
      if (!CONVERSATION_ID.test(rawId) || archived.has(rawId)) continue
      const meta = convs[rawId]
      const summary = meta?.summary
      const native = withoutStaleWorking(rawId, states.get(rawId))
      const pid = toPersonaId(rawId)
      const running = hasLiveWork(pid) || nativeWorking(native)
      const bDir = brainDirOf(rawId)
      const fullTranscript = join(bDir, '.system_generated', 'logs', 'transcript_full.jsonl')
      const transcriptFile = existsSync(fullTranscript) ? fullTranscript : join(bDir, '.system_generated', 'logs', 'transcript.jsonl')
      const hasTranscript = existsSync(transcriptFile)
      if (!hasTranscript && !running && !createdAt.has(rawId) && !rt.touched(pid)) continue
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

      const cwd = sessionCwds.get(rawId) || workspacePathOf(native, workspacePathOf(summary, cwdByConv[rawId] || home, skipPolicyDir), skipPolicyDir)
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
      const nativeTranscript = transcriptOf(rawId, native || null)
      const transcript = withModeNotice(rawId, nativeTranscript)
      const { steps, history } = transcript
      const parsed = stepsToItems(steps, { brainDir: brainDirOf(rawId), full: false, clientMsgIdFor: clientMsgIdFor(rawId) })
      if (!lastPushedSeq.has(pid)) lastPushedSeq.set(pid, parsed.toSeq)
      if (!lastPushedHistory.has(pid)) lastPushedHistory.set(pid, history)
      let status = running ? 'working' : transcriptStatus(nativeTranscript.steps, native)
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
    const id = typeof sessionId === 'string' ? sessionId.trim() : ''
    const rawId = toAgyId(id)
    if (!id.startsWith(PREFIX) || !CONVERSATION_ID.test(rawId)) throw new RpcError('Antigravity session not found', 'not_found')
    return rawId
  }

  async function items(params) {
    const rawId = ownedId(params.session_id)
    await reconcileNativeCut(rawId)
    await readNativeError(rawId)
    const bDir = brainDirOf(rawId)
    const { steps, history } = withModeNotice(rawId, transcriptOf(rawId))
    const { items: allItems, toSeq } = stepsToItems(steps, { brainDir: bDir, full: false, includeMedia: true, clientMsgIdFor: clientMsgIdFor(rawId) })
    return page(allItems, params, { history, toSeq })
  }

  async function cursor(sessionId) {
    const rawId = ownedId(sessionId)
    await reconcileNativeCut(rawId)
    await readNativeError(rawId)
    const { steps, history } = withModeNotice(rawId, transcriptOf(rawId))
    const { toSeq } = stepsToItems(steps, { brainDir: brainDirOf(rawId), full: false, clientMsgIdFor: clientMsgIdFor(rawId) })
    return { to_seq: toSeq, history }
  }

  async function item(params) {
    const rawId = ownedId(params.session_id)
    await reconcileNativeCut(rawId)
    await readNativeError(rawId)
    const bDir = brainDirOf(rawId)
    const { steps } = withModeNotice(rawId, transcriptOf(rawId))
    const { items: fullItems } = stepsToItems(steps, { brainDir: bDir, full: true, includeMedia: true, clientMsgIdFor: clientMsgIdFor(rawId) })
    const found = fullItems.find((it) => it.id === params.item_id)
    if (!found) throw new Error(`item ${params.item_id} not found`)
    return found
  }

  async function image(params) {
    const rawId = ownedId(params.session_id)
    if (typeof params.item_id === 'string') {
      await reconcileNativeCut(rawId)
      const { steps } = withModeNotice(rawId, transcriptOf(rawId))
      const { media } = stepsToItems(steps, { includeMedia: true, mediaItemId: params.item_id })
      const found = media && await readMediaImage(media, brainDirOf(rawId))
      if (found) return found
    }
    throw new RpcError('Antigravity image not found', 'not_found')
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

  function fallbackRefusedAuto(rawId) {
    const existing = autoFallbacks.get(rawId)
    if (existing) return existing
    const work = (async () => {
      try {
        const res = await acp.setMode(rawId, personaModeToAgy('bypassPermissions'))
        sessionConfiguration(rawId, res)
        const cur = sessionModes.get(rawId) || {}
        sessionModes.set(rawId, {
          ...cur,
          mode: 'bypassPermissions',
          requestedMode: 'bypassPermissions',
          autoRequested: false,
          autoRefused: true,
          applyingAuto: false,
        })
        rememberAutoRefusalNotice(rawId)
      } catch (error) {
        const cur = sessionModes.get(rawId) || {}
        sessionModes.set(rawId, { ...cur, mode: 'default', requestedMode: 'default', autoRequested: false, autoRefused: true, applyingAuto: false })
        throw error
      } finally {
        autoFallbacks.delete(rawId)
        rt.sessionsChanged()
      }
    })()
    autoFallbacks.set(rawId, work)
    return work
  }

  async function applySessionOptions(rawId, { model, permission_mode, effort, settings_mode = false }) {
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
        // A newer explicit choice goes after the older fallback, never behind its native accept-edits write.
        const pendingFallback = autoFallbacks.get(rawId)
        if (pendingFallback) {
          try { await pendingFallback } catch { /* the new mode still gets its own chance */ }
        }
        const previous = sessionModes.get(rawId) || {}
        const modeSource = settings_mode ? 'settings' : 'explicit'
        sessionModes.set(rawId, {
          ...previous,
          ...(permission_mode === 'auto' ? { mode: undefined } : {}),
          modeSource,
          autoRequested: permission_mode === 'auto',
          autoRefused: permission_mode === 'auto' ? false : previous.autoRefused,
          applyingAuto: permission_mode === 'auto',
        })
        try {
          const res = await acp.setMode(rawId, personaModeToAgy(permission_mode))
          sessionConfiguration(rawId, res, true)
          const cur = sessionModes.get(rawId) || {}
          sessionModes.set(rawId, { ...cur, applyingAuto: false })
          if (permission_mode === 'auto') {
            if (cur.autoRefused) {
              await fallbackRefusedAuto(rawId)
            } else {
              const mode = cur.mode || 'auto'
              sessionModes.set(rawId, { ...sessionModes.get(rawId), mode, requestedMode: mode })
            }
          } else {
            sessionModes.set(rawId, {
              ...sessionModes.get(rawId),
              mode: permission_mode,
              requestedMode: permission_mode,
              autoRequested: false,
              autoRefused: false,
            })
          }
        } catch (error) {
          const cur = sessionModes.get(rawId) || {}
          sessionModes.set(rawId, {
            ...cur,
            mode: cur.mode || previous.mode,
            ...(cur.autoRefused ? {} : { modeSource: previous.modeSource, requestedMode: previous.requestedMode, autoRefused: previous.autoRefused }),
            autoRequested: false,
            applyingAuto: false,
          })
          throw error
        }
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

  function extraDirsFor(rawId) {
    try {
      return readPolicy(policyRoot, rawId) ? [policyDirOf(policyRoot, rawId)] : []
    } catch {
      return []
    }
  }

  function requireAdditionalDirectories() {
    if (!acp.supportsAdditionalDirectories) {
      throw new RpcError('Antigravity CLI does not support separate supervision policy directories', SUPERVISION_ERROR_CODE)
    }
  }

  function validatePolicyParam(params) {
    if (params?.supervision_instructions === undefined) return { hasPolicy: false, policy: '' }
    if (typeof params.supervision_instructions !== 'string') {
      throw new RpcError('Supervision policy must be a string', SUPERVISION_ERROR_CODE)
    }
    return { hasPolicy: true, policy: params.supervision_instructions }
  }

  async function loadSession(rawId, cwd, servers, additionalDirectories = []) {
    sessionConfiguration(rawId, await acp.ensureSessionLoaded(rawId, cwd, servers, additionalDirectories))
  }

  /** Create a native session with the requested or configured options; a session that refuses them is closed. */
  async function openSession(params, cwd, isAsst) {
    const { policy } = validatePolicyParam(params)
    await acp.ensureStarted()
    if (policy) {
      requireAdditionalDirectories()
      splitPolicy(policy)
    }
    const res = await acp.newSession({ cwd, mcpServers: [] })
    const rawId = res.sessionId
    createdAt.set(rawId, Date.now())
    sessionConfiguration(rawId, res)
    const pid = toPersonaId(rawId)
    sessionCwds.set(rawId, cwd)
    if (isAsst) assistants.add(pid)

    try {
      let extraDirs = []
      if (policy) {
        const dir = syncPolicy(policyRoot, rawId, policy)
        if (dir) extraDirs = [dir]
      }
      await loadSession(rawId, cwd, mcpServersFor(pid, isAsst), extraDirs)
      await applySessionOptions(rawId, {
        model: params.model || configuredModel(),
        permission_mode: params.permission_mode || configuredMode(),
        effort: params.effort,
        settings_mode: !params.permission_mode,
      })
    } catch (err) {
      removePolicy(policyRoot, rawId)
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
          ...(params.supervision_instructions !== undefined ? { supervision_instructions: params.supervision_instructions } : {}),
        })
      }

      rt.sessionsChanged()
      return { session }
    })
  }

  function hasLiveWork(pid) {
    return activeTurns.has(pid) || autonomousTurns.has(pid)
  }

  function liveTurnOf(pid) {
    return promptTurns.get(pid) || autonomousTurns.get(pid)
  }

  function missedWake(rawId, now) {
    const missed = missedWakes.get(rawId)
    if (missed?.expiresAt > now && missed.skip) return missed
    if (missed) missedWakes.delete(rawId)
  }

  function liveTurnForUpdate(rawId, pid, start = true, toolCallId) {
    if (stopped || archived.has(rawId) || checkTurns.has(rawId)) return undefined
    const prompt = promptTurns.get(pid)
    if (prompt) return prompt
    if (activeTurns.has(pid) || rewinding.has(pid) || forking.has(pid) || cancelling.has(rawId) || !sessionCwds.has(rawId)) return undefined
    const now = Date.now()
    let turn = autonomousTurns.get(pid)
    const missed = missedWake(rawId, now)
    if (toolCallId && missed?.toolIds.includes(toolCallId)) {
      missed.skip--
      missed.toolIds = []
      delete missed.observed
    }
    if (!turn) {
      if (!start) return undefined
      turn = { seq: nextTurnSeq++, streamingText: '' }
      prepareLiveTurn(rawId, turn)
      turn.historySeq = turn.seq
      const { base, steps, ...pending } = unexportedTurns.get(rawId) ?? {}
      turn.wakeCursor = { after: turn.exportedBaseMax, skip: 0, pending: turn.priorPending.length ? pending : undefined }
      autonomousTurns.set(pid, turn)
      if (!lastPushedSeq.has(pid)) lastPushedSeq.set(pid, 0)
      staleWorkingAt.delete(rawId)
      promptFailures.delete(rawId)
      rt.sessionsChanged(0)
      turn.pollTimer = setInterval(() => pollAutonomousTurn(rawId, turn), exportPollMs)
      turn.pollTimer.unref?.()
    }
    turn.lastUpdate = now
    return turn
  }

  function pollAutonomousTurn(rawId, turn) {
    if (permissionAsks.get(rawId)?.size) return
    const now = Date.now()
    const missed = missedWake(rawId, now)
    if (!missed && turn.observedMissed?.skip && turn.observedMissed.observed) {
      const { after } = turn.observedMissed.observed
      turn.observedMissed = { after, skip: 0, observed: { after, remaining: 0 } }
    }
    const cursor = missed ?? (turn.observedMissed?.observed ? turn.observedMissed : turn.wakeCursor)
    const native = readConversationStates().get(rawId)
    if (native && !nativeWorking(native) && native.step_count > turn.exportedBaseMax) {
      const { steps } = exportedTranscriptOf(rawId)
      const first = steps.findIndex((step, index) => (step.step_index ?? index) > turn.exportedBaseMax)
      if (first >= 0 && native.step_count > maxStepIndex(steps)) {
        const { after, skip, pending } = cursor
        let remaining = skip
        let prior = pending
        const wake = steps.findIndex((step, index) => {
          if (step.type !== 'SYSTEM_MESSAGE' || (step.step_index ?? index) <= after ||
              (prior && !pendingReplaced(prior, steps.slice(0, index)))) return false
          if (prior && prior.seq === unexportedTurns.get(rawId)?.seq) unexportedTurns.delete(rawId)
          prior = undefined
          if (remaining-- === 0) return true
          cursor.observed = { after: step.step_index ?? index, remaining }
          turn.observedMissed = cursor
          return false
        })
        if ((wake >= 0 || (!skip && !pending && !cursor.observed)) && autonomousExported(turn, steps.slice(Math.max(first, wake + 1)))) {
          missedWakes.delete(rawId)
          retireAutonomousTurn(rawId)
          return
        }
      }
    }
    if (now - turn.lastUpdate >= autonomousIdleMs) {
      if (!nativeWorking(native)) {
        const next = missed ?? turn.wakeCursor
        const { observed } = cursor
        missedWakes.set(rawId, { after: observed?.after ?? next.after, pending: observed ? undefined : next.pending,
          skip: (missed ? observed?.remaining ?? missed.skip : 0) + 1, expiresAt: now + exportSettleTimeoutMs,
          toolIds: [...turn.liveToolsById.keys()] })
      }
      retireAutonomousTurn(rawId, native)
    }
  }

  function autonomousExported(turn, steps) {
    if (steps.at(-1)?.type === 'ERROR_MESSAGE') return true
    const reply = turn.streamingText.trim()
    if (reply && !exportedHasReply(steps, -1, reply)) return false
    if (turn.liveToolsById.size > 0) {
      const calls = steps.reduce((count, step) => count + (step.tool_calls?.length || 0), 0)
      const results = steps.filter((step) => step.type === 'GENERIC').length
      return calls >= turn.liveToolsById.size && results >= turn.liveSteps.filter((step) => step.type === 'GENERIC').length
    }
    const thought = turn.liveSteps.findLast((step) => step.thinking)?.thinking.trim()
    return Boolean(reply || (thought && steps.some((step) => typeof step.thinking === 'string' && step.thinking.trim() === thought)))
  }

  function retireAutonomousTurn(rawId, native) {
    const pid = toPersonaId(rawId)
    const turn = autonomousTurns.get(pid)
    if (!turn) return false
    autonomousTurns.delete(pid)
    clearInterval(turn.pollTimer)
    clearTimeout(turn.thoughtTimer)
    if (nativeWorking(native)) staleWorkingAt.set(rawId, Number.isInteger(native.step_count) ? native.step_count : -1)
    rt.streamEnd(pid)
    if (!stopped && !archived.has(rawId)) {
      withdrawPermissionAsks(rawId, 'the turn ended')
      syncTranscriptAppend(rawId)
      rt.sessionsChanged(0)
    }
    if (want) setImmediate(() => { void maybeExit() })
    return true
  }

  function prepareLiveTurn(rawId, turn) {
    const base = exportedTranscriptOf(rawId)
    const curMax = maxStepIndex(base.steps)
    const pending = unexportedTurns.get(rawId)
    const priorPending = pending && !pendingReplaced(pending, base.steps, curMax) ? pending.steps : []
    const baseMax = maxStepIndex([...base.steps, ...priorPending])
    turn.base = priorPending.length ? pending.base : base
    turn.inputCount = explicitInputs([...turn.base.steps, ...priorPending]) + 1
    turn.priorPending = priorPending
    turn.historySeq = priorPending.length > 0 ? pending.seq : turn.seq
    turn.exportedBaseMax = curMax
    turn.baseMaxIndex = baseMax
    turn.nextStepIndex = baseMax + 1
    turn.startedIso = new Date().toISOString()
    turn.liveSteps = []
    turn.activeThoughtStep = null
    turn.pendingThought = ''
    turn.liveToolsById = new Map()
    turn.nextToolNum = 0
  }

  function beginLiveTurn(rawId, pid, turn) {
    missedWakes.delete(rawId)
    clearExportPoll(rawId)
    prepareLiveTurn(rawId, turn)
    if (turn.userText) {
      const idx = turn.nextStepIndex++
      turn.liveSteps.push({
        step_index: idx,
        type: 'USER_INPUT',
        source: 'USER_EXPLICIT',
        status: 'DONE',
        created_at: turn.startedIso,
        content: turn.userText,
        client_msg_id: turn.clientMsgId,
      })
      if (turn.clientMsgId) {
        sentMsgIds.set(`${rawId}:${idx}`, { text: turn.userText, clientMsgId: turn.clientMsgId })
        while (sentMsgIds.size > 512) sentMsgIds.delete(sentMsgIds.keys().next().value)
      }
    }
    promptFailures.delete(rawId)
    promptTurns.set(pid, turn)
    rt.streamEnd(pid)
    syncTranscriptAppend(rawId)
  }

  function promptWork(turn, exportedSteps) {
    return Boolean(turn.streamingText || turn.toolActivity || exportedSteps.some((step) =>
      step.type === 'GENERIC' || step.tool_calls?.length ||
      (step.type === 'PLANNER_RESPONSE' && typeof step.content === 'string' && step.content.trim())))
  }

  function transient401(error) {
    if (!error) return false
    const text = typeof error === 'string' ? error :
      `${error.message ?? error.error ?? ''} ${error.code ?? ''} ${error.turn_error ?? ''} ${error.content ?? ''} ${JSON.stringify(error.data ?? '')}`
    return /\b401\b/.test(text) && /\bUNAUTHENTICATED\b/i.test(text) &&
      /missing (?:the )?(?:required )?(?:authentication |auth )?credentials?\b/i.test(text) &&
      !SIGNED_OUT.test(text) && !/invalid.grant|login.required|log.?out|signed.out|reauthenticat|refresh.token.*(?:expired|revoked|invalid)|(?:expired|revoked|invalid).*refresh.token|revoked.*(?:login|credential)/i.test(text)
  }

  function withoutRetriedInputs(rawId, steps) {
    const retries = promptRetries.get(rawId)
    if (!retries) return steps
    const hidden = new Set()
    for (const retry of retries) {
      let firstInput = false
      for (const step of steps) {
        if (step.step_index <= retry.base) continue
        const at = Date.parse(step.created_at)
        if (!(at >= retry.startedAt && at <= retry.endedAt)) continue
        if (explicitInput(step)) {
          if (cleanUserText(step.content) !== retry.text) break
          if (at >= retry.retriedAt) {
            if (firstInput) hidden.add(step)
            break
          }
          if (firstInput) break
          firstInput = true
        } else if (step.type === 'ERROR_MESSAGE' && at < retry.retriedAt && transient401(step)) {
          hidden.add(step)
        }
      }
    }
    return hidden.size ? steps.filter((step) => !hidden.has(step)) : steps
  }

  async function retryPrompt401(rawId, blocks, turn, { failure, result, cancelled }) {
    if (cancelled() || result?.stopReason === 'cancelled' || promptWork(turn, [])) return undefined
    if (failure && !transient401(failure)) return undefined
    const exportedSteps = () => exportedTranscriptOf(rawId).steps.filter((step) => step.step_index > turn.exportedBaseMax)
    const steps = exportedSteps()
    if (!failure) {
      const last = steps.findLast((step) => ['USER_INPUT', 'PLANNER_RESPONSE', 'GENERIC', 'ERROR_MESSAGE'].includes(step.type))
      if (last?.type !== 'ERROR_MESSAGE' || Date.parse(last.created_at) < Date.parse(turn.startedIso) || !transient401(last)) return undefined
    }
    if (promptWork(turn, steps)) return undefined

    const retry = new AbortController()
    turn.retry = retry
    try {
      await delay(transient401RetryDelayMs, undefined, { signal: retry.signal })
    } catch (err) {
      if (!retry.signal.aborted) throw err
    } finally {
      turn.retry = undefined
    }
    if (cancelled() || promptWork(turn, exportedSteps())) return undefined

    const marker = { base: turn.exportedBaseMax, text: turn.userText, startedAt: Date.parse(turn.startedIso),
      retriedAt: Date.now(), endedAt: Infinity }
    const retries = promptRetries.get(rawId) ?? []
    retries.push(marker)
    promptRetries.set(rawId, retries)
    try {
      return { result: await acp.prompt(rawId, blocks) }
    } catch (err) {
      return { failure: err }
    } finally {
      marker.endedAt = Date.now()
    }
  }

  async function send(params) {
    const rawId = ownedId(params.session_id)
    const pid = toPersonaId(rawId)
    if (rewinding.has(pid)) throw new RpcError('The conversation is being rewound', 'rewind_busy')
    if (forking.has(pid)) throw new RpcError('The conversation is being forked', 'fork_turn_running')
    if (archived.has(rawId)) throw new Error('Antigravity session is closed')
    const cwd = sessionCwds.get(rawId) || home
    const isAsst = Boolean(params.assistant) || assistants.has(pid) || isAssistantDir(cwd)
    if (isAsst) assistants.add(pid)

    const { hasPolicy, policy: requestedPolicy } = validatePolicyParam(params)
    let prevPolicy = ''
    try {
      prevPolicy = readPolicy(policyRoot, rawId)
    } catch (err) {
      if (hasPolicy) throw new RpcError(`Failed to read supervision policy: ${err.message}`, SUPERVISION_ERROR_CODE)
    }
    let nextPolicy = hasPolicy ? requestedPolicy : prevPolicy
    if (hasPolicy && nextPolicy !== prevPolicy && (hasLiveWork(pid) || nativeWorking(readConversationStates().get(rawId)))) {
      throw new RpcError('Wait for the current turn to finish before changing supervision policy', 'busy')
    }

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
    retireAutonomousTurn(rawId)
    const turnToken = Symbol('turn')
    const turn = {
      seq: nextTurnSeq++,
      historySeq: undefined,
      userText: cleanUserText(params.text || ''),
      clientMsgId: typeof params.client_msg_id === 'string' && params.client_msg_id ? params.client_msg_id : undefined,
      streamingText: '',
      liveSteps: [],
      activeThoughtStep: null,
      pendingThought: '',
      liveToolsById: new Map(),
      nextToolNum: 0,
      thoughtTimer: undefined,
      priorPending: [],
      exportedBaseMax: undefined,
      baseMaxIndex: undefined,
      nextStepIndex: undefined,
      startedIso: '',
      failed: false,
      newMessage: false,
      startedAt: Date.now(),
      pollTimer: undefined,
      previous: prevTurn,
      cancelled: false,
      token: turnToken,
    }
    let prepared = false
    let readyResolve, readyReject
    const ready = prevTurn ? undefined : new Promise((resolve, reject) => { readyResolve = resolve; readyReject = reject })
    const prevPromise = prevTurn?.promise || Promise.resolve()
    const promise = prevPromise
      .catch(() => {})
      .then(() => busyCall(async () => {
        turn.previous = undefined
        if (stopped || turn.cancelled || archived.has(rawId)) { readyResolve?.(); return }
        if (prevTurn && !hasPolicy) {
          try { nextPolicy = readPolicy(policyRoot, rawId) } catch {}
        }
        const cancelled = () => turn.cancelled || stopped || archived.has(rawId)
        let retried401 = false
        for (let attempt = 0; ; attempt++) {
          try {
            await retryStaleModels(async () => {
              await acp.ensureStarted()
              if (nextPolicy) requireAdditionalDirectories()
              let extraDirs = []
              if (nextPolicy || prevPolicy) {
                const dir = syncPolicy(policyRoot, rawId, nextPolicy)
                if (dir) extraDirs = [dir]
              }
              await loadSession(rawId, cwd, mcpServersFor(pid, isAsst), extraDirs)
              if (!turn.cancelled && !stopped && !archived.has(rawId)) {
                const modeInfo = sessionModes.get(rawId)
                const settingsMode = !params.mode && modeInfo?.modeSource === 'settings'
                const explicitMode = !params.mode && modeInfo?.modeSource === 'explicit' && modeInfo.requestedMode && modeInfo.requestedMode !== modeInfo.mode
                  ? modeInfo.requestedMode
                  : undefined
                await applySessionOptions(rawId, {
                  model: params.model,
                  permission_mode: params.mode || (settingsMode ? configuredMode() : explicitMode),
                  effort: params.effort,
                  settings_mode: settingsMode,
                })
              }
            })
            if (cancelled()) { readyResolve?.(); return }
            if (!prepared) {
              cancelling.delete(rawId)
              staleWorkingAt.delete(rawId)
              completedTurns.delete(pid)
              beginLiveTurn(rawId, pid, turn)
              turn.pollTimer = setInterval(() => syncTranscriptAppend(rawId), 400)
              prepared = true
            }
            readyResolve?.()
            let result, failure
            try {
              result = await acp.prompt(rawId, blocks)
            } catch (err) {
              failure = err
            }
            const retry = attempt === 0 ? await retryPrompt401(rawId, blocks, turn, { failure, result, cancelled }) : undefined
            if (cancelled()) return
            if (retry) {
              retried401 = true
              if (retry.failure) throw retry.failure
              return retry.result
            }
            if (failure) throw failure
            return result
          } catch (err) {
            if (!err.invalidSession || attempt > 0 || retried401 || cancelled()) throw err
          }
        }
      }))
      .catch((err) => {
        readyReject?.(err)
        if (!prepared && hasPolicy && nextPolicy !== prevPolicy) {
          try { syncPolicy(policyRoot, rawId, prevPolicy) } catch {}
        }
        turn.failed = true
        turn.inactive = err.promptInactive === true
        if (turn.inactive) stalledGeneration = acp.generation
        if ((prepared || prevTurn) && !stopped && !turn.cancelled && !cancelling.has(rawId)) {
          promptFailures.set(rawId, { input: inputKey(rawId), fields: {
            turn_error: err.message, turn_error_after_work: promptWork(turn, []),
          } })
        }
        log(`agy prompt error (${pid}): ${err.message}`)
      })
      .finally(() => {
        try {
          clearTimeout(turn.thoughtTimer)
          turn.thoughtTimer = undefined
          clearInterval(turn.pollTimer)
          // The turn is over: a card still waiting has nothing to answer for any more.
          const wasCancelled = turn.cancelled || cancelling.has(rawId)
          if (promptTurns.get(pid) === turn) cancelling.delete(rawId)
          if ((wasCancelled || turn.inactive) && turn.exportedBaseMax !== undefined) {
            const count = readConversationStates().get(rawId)?.step_count
            staleWorkingAt.set(rawId, Number.isInteger(count) ? count : -1)
          }
          withdrawPermissionAsks(rawId, 'the turn ended')
          const expectReply = Boolean(turn.streamingText.trim())
          let awaitExport = false
          if (!stopped && !turn.failed && !wasCancelled && !archived.has(rawId) && turn.exportedBaseMax !== undefined) {
            const exported = exportedTranscriptOf(rawId)
            const curMax = maxStepIndex(exported.steps)
            const exportedComplete = curMax > turn.exportedBaseMax && (!expectReply || exportedHasReply(exported.steps, turn.exportedBaseMax, turn.streamingText, turn.inputCount))
            if (!exportedComplete) {
              const settled = [...turn.priorPending, ...turn.liveSteps]
              if (expectReply) {
                settled.push({
                  step_index: Math.max(curMax, settled.length ? maxStepIndex(settled) : turn.baseMaxIndex) + 1,
                  type: 'PLANNER_RESPONSE',
                  source: 'MODEL',
                  status: 'DONE',
                  created_at: new Date().toISOString(),
                  content: turn.streamingText.trim(),
                })
              }
              if (settled.length > 0) {
                unexportedTurns.set(rawId, {
                  base: turn.base,
                  baseMaxIndex: turn.exportedBaseMax,
                  seq: turn.historySeq ?? turn.seq,
                  steps: settled,
                  hasReply: expectReply,
                  replyText: turn.streamingText,
                  inputCount: turn.inputCount,
                  expiresAt: Date.now() + exportSettleTimeoutMs,
                })
                awaitExport = true
              }
            } else {
              unexportedTurns.delete(rawId)
            }
          }
          if (promptTurns.get(pid) === turn) {
            promptTurns.delete(pid)
            rt.streamEnd(pid)
          }
          if (activeTurns.get(pid)?.token === turnToken) {
            activeTurns.delete(pid)
            cancelling.delete(rawId)
            if (prepared || prevTurn) completedTurns.add(pid)
            syncTranscriptAppend(rawId)
            rt.sessionsChanged(0)
            if (awaitExport && turn.exportedBaseMax !== undefined) {
              scheduleExportPoll(rawId, turn.exportedBaseMax, expectReply)
            }
          }
        } finally {
          if (!stopped && !turn.inactive && turn.exportedBaseMax !== undefined) {
            setImmediate(() => { if (!stopped && !activeTurns.has(pid)) void readNativeError(rawId) })
          }
          if (want) void maybeExit()
        }
      })
    turn.promise = promise
    activeTurns.set(pid, turn)
    rt.sessionsChanged(0)
    return prevTurn ? { queued: true } : ready
  }

  /** Ask agy to end the session's running turn (session/cancel) and take its permission cards off the phone. */
  function cancelTurn(rawId) {
    const pid = toPersonaId(rawId)
    const autonomous = autonomousTurns.has(pid) && retireAutonomousTurn(rawId, readConversationStates().get(rawId))
    if (autonomous) cancelling.add(rawId)
    return busyCall(async () => {
      const tail = activeTurns.get(pid)
      if (tail) cancelling.add(rawId)
      for (let turn = tail; turn; turn = turn.previous) {
        turn.cancelled = true
        turn.retry?.abort()
      }
      withdrawPermissionAsks(rawId, 'interrupted')
      await acp.cancel(rawId)
      return tail
    }).finally(() => {
      if (autonomous && !activeTurns.has(pid)) cancelling.delete(rawId)
    })
  }

  /**
   * Stop the running turn and queued follow-ups. Resolves to {settled: true} once that queue drains, or to nothing
   * when it has not within interruptSettleMs (docs/integrations/runtimes.md, #8097, #11108).
   */
  async function interrupt(params) {
    const rawId = ownedId(params.session_id)
    const pid = toPersonaId(rawId)
    const running = await cancelTurn(rawId)
    if (!running?.promise) return undefined
    let timer
    const ended = await Promise.race([
      running.promise.then(() => true, () => true),
      new Promise((resolve) => { timer = setTimeout(() => resolve(false), interruptSettleMs) }),
    ])
    clearTimeout(timer)
    return ended && !activeTurns.has(pid) ? { settled: true } : undefined
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
      if (forking.has(toPersonaId(rawId))) throw new RpcError('The conversation is being forked', 'fork_turn_running')
      archived.add(rawId)
      missedWakes.delete(rawId)
      forkSeeds.delete(rawId)
      clearExportPoll(rawId)
      unexportedTurns.delete(rawId)
      promptRetries.delete(rawId)
      dirtyLiveSeqs.delete(toPersonaId(rawId))
      removePolicy(policyRoot, rawId)
      await cancelTurn(rawId)
      await acp.closeSession(rawId)
      rt.sessionsChanged()
    })
  }

  async function rewind(params) {
    return busyCall(() => rewindConversation(params))
  }

  async function fork({ session_id, at_item_id = '', assistant }) {
    const rawId = ownedId(session_id)
    const pid = toPersonaId(rawId)
    const missing = () => new RpcError('Antigravity conversation not found', 'not_found')
    const busy = () => new RpcError('Wait for the current turn to finish before branching', 'fork_turn_running')
    const mismatch = () => new RpcError('The selected reply changed. Reload the conversation before branching', 'fork_mismatch')
    const native = readConversationStates().get(rawId)
    const pending = unexportedTurns.get(rawId)
    if (archived.has(rawId) || !/^[\da-f-]{36}$/i.test(rawId) ||
        (!native && !createdAt.has(rawId) && !existsSync(brainDirOf(rawId)))) throw missing()
    if (forking.has(pid) || rewinding.has(pid) || hasLiveWork(pid) || cancelling.has(rawId) ||
        permissionAsks.get(rawId)?.size || (!pending && nativeWorking(native))) throw busy()
    if (typeof at_item_id !== 'string') throw mismatch()

    const deadline = Date.now() + exportSettleTimeoutMs
    const { convs, cwdByConv } = readCacheMetadata()
    const summary = convs[rawId]?.summary
    const cwd = sessionCwds.get(rawId) || workspacePathOf(native, workspacePathOf(summary, cwdByConv[rawId] || home, skipPolicyDir), skipPolicyDir)
    const isAsst = assistant === true || assistants.has(pid) || isAssistantDir(cwd)
    if (isAsst) assistants.add(pid)
    const projected = pending && at_item_id && withModeNotice(rawId, { steps: [...pending.base.steps, ...pending.steps] }).steps

    forking.add(pid)
    try {
      return await busyCall(async () => {
        await loadSession(rawId, cwd, mcpServersFor(pid, isAsst), extraDirsFor(rawId))
        let transcript = exportedTranscriptOf(rawId)
        while (pending && !pendingReplaced(pending, transcript.steps)) {
          const remaining = deadline - Date.now()
          if (stopped || remaining <= 0) throw busy()
          await new Promise((resolve) => setTimeout(resolve, Math.min(exportPollMs, remaining)))
          transcript = exportedTranscriptOf(rawId)
        }
        const snapshot = await acp.nativeRequest('GetCascadeTrajectory', { cascadeId: rawId,
          trajectoryVerbosity: 'CLIENT_TRAJECTORY_VERBOSITY_PROD_UI', disableRehydration: true })
        if (snapshot.trajectory?.cascadeId !== rawId) throw missing()
        if (snapshot.status !== 'CASCADE_RUN_STATUS_IDLE') throw busy()
        const trajectory = snapshot.trajectory.steps || []
        let index = -1
        if (at_item_id) {
          index = forkStepIndex(forkReplyIndex(rawId, at_item_id, transcript, projected, pending), trajectory)
          if (index === undefined) throw mismatch()
        }
        const result = await acp.nativeRequest('ForkConversation', { sourceCascadeId: rawId, forkAtStepIndex: index,
          targetForkWorkspace: 'FORK_TARGET_CURRENT_WORKSPACE', sourceWorkspaceUris: [pathToFileURL(cwd).href] })
        const newId = result.newCascadeId
        if (typeof newId !== 'string' || !/^[\da-f-]{36}$/i.test(newId) || newId === rawId) {
          throw new RpcError('Antigravity returned an invalid branch', 'fork_failed')
        }
        const sessionId = toPersonaId(newId)
        const now = Date.now()
        createdAt.set(newId, now)
        sessionCwds.set(newId, cwd)
        const last = result.forkedAtStepIndex ?? (index < 0 ? trajectory.length - 1 : index)
        const prefix = transcript.steps.filter((step, position) => (step.step_index ?? position) <= last)
        if (prefix.length) {
          forkSeeds.set(newId, prefix)
          if (forkSeeds.size > maxSessions) forkSeeds.delete(forkSeeds.keys().next().value)
        }
        if (isAsst) assistants.add(sessionId)
        const title = readConversationStates().get(newId)?.title || sessionTitles.get(rawId) || native?.title || summary?.Title || basename(cwd)
        sessionTitles.set(newId, title)
        rt.sessionsChanged(0)
        return { session_id: sessionId, session: { session_id: sessionId, cwd, title, status: 'idle',
          launcher: 'agy', updated_at: new Date(now).toISOString() } }
      })
    } catch (error) {
      if (error.code === 'not_found') throw missing()
      throw error
    } finally {
      forking.delete(pid)
      if (want) void maybeExit()
    }
  }

  function forkReplyIndex(rawId, itemId, transcript, projected, pending) {
    if (projected) {
      const position = forkReplyPosition(itemId, projected)
      const reply = projected[position]
      if (reply && (reply.native_step_index ?? reply.step_index ?? position) > maxStepIndex(pending.base.steps)) {
        return exportedReplyIndex(projected.slice(0, position), reply.content.trim(), transcript.steps)
      }
    }
    const steps = withModeNotice(rawId, transcript).steps
    const position = forkReplyPosition(itemId, steps)
    const step = steps[position]
    return step ? step.native_step_index ?? step.step_index ?? position : undefined
  }

  function exportedReplyIndex(beforeReply, replyText, exported) {
    const turn = explicitInputs(beforeReply)
    const prompt = beforeReply.findLast(explicitInput)
    if (!prompt) return undefined
    const promptText = cleanUserText(prompt.content)
    let inputs = 0
    let index
    for (const [offset, step] of exported.entries()) {
      if (explicitInput(step) && ++inputs === turn) {
        const shown = cleanUserText(step.content)
        if (shown !== promptText && shown.replace(/^\/plan\s+/, '') !== promptText) return undefined
      }
      if (inputs > turn) break
      if (inputs === turn && step.type === 'PLANNER_RESPONSE' && typeof step.content === 'string' && step.content.trim() === replyText) {
        if (index !== undefined) return undefined
        index = step.step_index ?? offset
      }
    }
    return index
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
    if (rewinding.has(pid) || forking.has(pid) || hasLiveWork(pid) || permissionAsks.get(rawId)?.size || nativeWorking(readConversationStates().get(rawId))) {
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
      dirtyLiveSeqs.delete(pid)
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
      missedWakes.delete(rawId)
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

  async function modes({ session_id, model, permission_mode, effort, assistant }) {
    return busyCall(async () => {
      const rawId = ownedId(session_id)
      const pid = toPersonaId(rawId)
      if (archived.has(rawId)) throw new Error('Antigravity session is closed')
      if (forking.has(pid) || (model && (hasLiveWork(pid) || nativeWorking(readConversationStates().get(rawId))))) {
        throw new Error('Antigravity session is busy')
      }
      const cwd = sessionCwds.get(rawId) || home
      const isAsst = assistant === true || assistants.has(pid) || isAssistantDir(cwd)
      if (isAsst) assistants.add(pid)
      await retryStaleModels(async () => {
        await loadSession(rawId, cwd, mcpServersFor(pid, isAsst), extraDirsFor(rawId))
        await applySessionOptions(rawId, { model, permission_mode, effort })
      })
      rt.sessionsChanged()
    })
  }

  /** No lease, replacement, live turn, probe, rewind or phone card is in flight. */
  function idle() {
    return leases === 0 && !recycling && activeTurns.size === 0 && autonomousTurns.size === 0 &&
      rewinding.size === 0 && forking.size === 0 && checkTurns.size === 0 && !rt.busy()
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

  /** The next periodic discovery: catalogEveryMs, doubled for each discovery in a row that timed out (#11106). */
  function scheduleCatalog() {
    clearTimeout(catalogTimer)
    if (stopped) return
    const wait = Math.min(catalogMaxMs, catalogEveryMs * 2 ** catalogTimeouts)
    catalogTimer = setTimeout(() => {
      void models({ refresh: true })
        .then(() => { catalogTimeouts = 0 }, (err) => {
          if (err?.code === SIGNIN_TIMEOUT) catalogTimeouts = Math.min(catalogTimeouts + 1, 16)
          else catalogTimeouts = 0
          log(`agy model discovery error: ${err.message}`)
        })
        .finally(scheduleCatalog)
    }, wait)
    catalogTimer.unref?.()
  }

  /** Resolves once agy is gone. */
  function stop() {
    stopped = true
    clearInterval(idleTimer)
    clearTimeout(catalogTimer)
    catalogAbort.abort()
    for (const pid of autonomousTurns.keys()) retireAutonomousTurn(toAgyId(pid))
    missedWakes.clear()
    rt.stop()
    // A turn's transcript poll would otherwise keep running; acp.stop() fails the prompts themselves.
    for (const tail of activeTurns.values()) {
      for (let turn = tail; turn; turn = turn.previous) {
        clearInterval(turn.pollTimer)
        clearTimeout(turn.thoughtTimer)
        turn.retry?.abort()
      }
    }
    for (const timer of exportPolls.values()) clearInterval(timer)
    exportPolls.clear()
    activeTurns.clear()
    promptTurns.clear()
    unexportedTurns.clear()
    promptRetries.clear()
    dirtyLiveSeqs.clear()
    return acp.stop()
  }

  return {
    start: () => {
      void models().catch((err) => log(`agy model discovery error: ${err.message}`))
      scheduleCatalog()
      return rt.start()
    },
    stop,
    idle,
  }
}
