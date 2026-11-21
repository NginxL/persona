// Protocol 1 between this plugin and the local Persona node (README.md): where the node's socket is, what a poll's
// answer holds, which messages or turn interrupts may go in as the person's own, and session-scoped model / effort
// overrides. Pure functions; register.ts does the I/O.

/** The base of the node's endpoints; the host name is not used (the request goes over the socket). */
export const BASE = 'http://persona/claude-plugin/v1'

/** Optional protocol capabilities this plugin version serves on /poll. */
export const CAPABILITIES = ['busy_input', 'interrupt', 'settings'] as const

/** How long deliver waits for $.prompt.submit to reject or drop before treating a mid-turn call as queued. */
export const SUBMIT_QUEUE_SETTLE_MS = 250

/** How long the node may hold a poll: under the 30 s after which Claude Code's fetch gives up. */
export const POLL_WAIT_MS = 25_000

/** Backoff while no node answers: from the first to the most, doubling. */
export const BACKOFF_FIRST_MS = 2_000
export const BACKOFF_MOST_MS = 60_000

/** The longest text submitted, in UTF-16 units; the node never sends more (its inbox line holds 1 MiB). */
export const TEXT_MOST = 200_000

const ID = /^[A-Za-z0-9_-]{1,128}$/
const MODEL = /^[A-Za-z0-9._:/[\]-]{1,200}$/

export type EffortChoice = 'low' | 'medium' | 'high' | 'xhigh' | 'max' | 'auto'

const EFFORTS: ReadonlySet<string> = new Set<EffortChoice>(['low', 'medium', 'high', 'xhigh', 'max', 'auto'])

export function validModel(model: unknown): model is string {
  return typeof model === 'string' && MODEL.test(model) && !model.startsWith('-')
}

export function isEffortChoice(effort: unknown): effort is EffortChoice {
  return typeof effort === 'string' && EFFORTS.has(effort)
}

/**
 * The node's socket, as the node resolves it: `$PERSONA_HOME/node.sock` when that is set (`~`, `~/…` and a relative
 * path are taken from the home directory), else `<home>/.persona/node.sock`. `undefined` without a home directory.
 */
export function socketPath(home: string | undefined, personaHome: string | undefined): string | undefined {
  if (!home) return undefined
  const sep = /^[A-Za-z]:[\\/]/.test(home) || home.includes('\\') ? '\\' : '/'
  const join = (a: string, b: string) => (a.endsWith('/') || a.endsWith('\\') ? a + b : a + sep + b)
  let root = join(home, '.persona')
  const p = personaHome?.trim()
  if (p) {
    if (p === '~') root = home
    else if (p.startsWith('~/') || p.startsWith('~\\')) root = join(home, p.slice(2))
    else if (p.startsWith('/') || /^[A-Za-z]:[\\/]/.test(p) || p.startsWith('\\\\')) root = p
    else root = join(home, p)
  }
  return join(root, 'node.sock')
}

/** The home directory: `USERPROFILE` on Windows (what the node's Go reads there), else `HOME`. */
export function homeDir(userProfile: string | undefined, home: string | undefined): string | undefined {
  if (userProfile && /^[A-Za-z]:[\\/]/.test(userProfile)) return userProfile
  return home || undefined
}

/** One message the node handed over: its id, the text, and the time after which it must not be submitted. */
export type Message = { id: string; text: unknown; deadlineMs: number }

/** An owner Stop handed over by the node to cancel the running main-loop turn via $.turn.abort. */
export type InterruptRequest = { id: string; deadlineMs: number }

/** The running main-loop turn tracked from turn.start / turn.step until turn.complete. */
export type ActiveTurn = { session: string; turnId: string }

/** A session-only model / effort switch handed over by the node. */
export type SettingsUpdate = { id: string; model?: string; effort?: EffortChoice }

/** In-memory session-only model / effort override applied on main-loop `turn.step` calls. */
export type SessionOverride = {
  session: string
  model?: string
  baseModel?: string
  effort?: EffortChoice
}

/** What a poll came to. */
export type PollAnswer =
  | { kind: 'messages'; messages: Message[]; interrupt?: InterruptRequest; settings?: SettingsUpdate; retryMs: number }
  | { kind: 'refused'; retryMs: number } // the node does not take this process for this session
  | { kind: 'unavailable' } // no node, an old node, or no answer

function readInterrupt(raw: unknown): InterruptRequest | undefined {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return undefined
  const { id, deadline_ms: deadline } = raw as Record<string, unknown>
  if (typeof id !== 'string' || !ID.test(id) || typeof deadline !== 'number') return undefined
  return { id, deadlineMs: deadline }
}

function readSettings(raw: unknown): SettingsUpdate | undefined {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return undefined
  const { id, model, effort } = raw as Record<string, unknown>
  if (typeof id !== 'string' || !ID.test(id)) return undefined
  if (model !== undefined && model !== '' && !validModel(model)) return undefined
  if (effort !== undefined && effort !== '' && !isEffortChoice(effort)) return undefined
  const m = typeof model === 'string' && model !== '' ? model : undefined
  const ef = isEffortChoice(effort) ? effort : undefined
  if (m === undefined && ef === undefined) return undefined
  return { id, ...(m !== undefined ? { model: m } : {}), ...(ef !== undefined ? { effort: ef } : {})
}

/** Reads a poll's answer: status and body as `$.http.fetch` resolved them. */
export function readPoll(status: number, body: string): PollAnswer {
  if (status === 403) return { kind: 'refused', retryMs: retryOf(parse(body), BACKOFF_MOST_MS) }
  if (status !== 200) return { kind: 'unavailable' }
  const answer = parse(body)
  if (answer === undefined) return { kind: 'unavailable' }
  const list = Array.isArray(answer.messages) ? answer.messages : []
  const messages: Message[] = []
  for (const m of list) {
    if (typeof m !== 'object' || m === null) continue
    const { id, text, deadline_ms: deadline } = m as Record<string, unknown>
    if (typeof id !== 'string' || !ID.test(id) || typeof deadline !== 'number') continue
    messages.push({ id, text, deadlineMs: deadline })
  }
  const interrupt = readInterrupt(answer.interrupt)
  const settings = readSettings(answer.settings)
  return {
    kind: 'messages',
    messages,
    ...(interrupt !== undefined ? { interrupt } : {}),
    ...(settings !== undefined ? { settings } : {}),
    retryMs: retryOf(answer, 0),
  }
}

/** Merges a new settings update into the active session override. */
export function mergeOverride(
  prev: SessionOverride | undefined,
  session: string,
  update: SettingsUpdate,
  sessionModel: string | undefined,
): SessionOverride {
  const base = prev?.session === session ? prev : undefined
  const next: SessionOverride = { session }
  if (update.model !== undefined) {
    next.model = update.model
    if (sessionModel !== undefined) next.baseModel = sessionModel
  } else if (base?.model !== undefined) {
    next.model = base.model
    if (base.baseModel !== undefined) next.baseModel = base.baseModel
  }
  if (update.effort !== undefined) next.effort = update.effort
  else if (base?.effort !== undefined) next.effort = base.effort
  return next
}

/** Applies the active session override to a main-loop `turn.step` input, clearing it if the computer changed models. */
export function applyStepOverride<
  E extends { model: string; effort?: 'low' | 'medium' | 'high' | 'xhigh' | 'max' | number; agentId?: string },
>(
  step: E,
  override: SessionOverride | undefined,
  session: string,
  sessionModel: string | undefined,
): { step: E; nextOverride: SessionOverride | undefined } {
  if (override === undefined || step.agentId !== undefined) return { step, nextOverride: override }
  if (override.session !== session) return { step, nextOverride: undefined }
  if (
    override.model !== undefined &&
    override.baseModel !== undefined &&
    sessionModel !== undefined &&
    sessionModel !== override.baseModel
  ) {
    // The user picked a different model in the desktop / terminal UI after Persona's switch: let the newer pick win.
    return { step, nextOverride: undefined }
  }
  const nextStep: E = { ...step }
  if (override.model !== undefined) nextStep.model = override.model
  if (override.effort === 'auto') delete nextStep.effort
  else if (override.effort !== undefined) nextStep.effort = override.effort
  return { step: nextStep, nextOverride: override }
}

/** Why a message is not submitted, or `ok`. */
export type Verdict = 'ok' | 'invalid' | 'late' | 'command'

/**
 * Whether the message may go in as the person's words now (`now`, epoch ms): text that is a string with something in
 * it and not too long, before its deadline, and no slash command (the node sends none; a command would run, not be
 * read).
 */
export function verdict(m: Message, now: number): Verdict {
  if (typeof m.text !== 'string' || m.text.trim() === '' || m.text.length > TEXT_MOST) return 'invalid'
  if (now > m.deadlineMs) return 'late'
  if (m.text.trimStart().startsWith('/')) return 'command'
  return 'ok'
}

/** The wait after a failed poll: the first backoff, then twice the last, up to the most. */
export function nextBackoff(last: number): number {
  return last <= 0 ? BACKOFF_FIRST_MS : Math.min(last * 2, BACKOFF_MOST_MS)
}

function parse(body: string): Record<string, unknown> | undefined {
  try {
    const v: unknown = JSON.parse(body)
    return typeof v === 'object' && v !== null && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined
  } catch {
    return undefined
  }
}

function retryOf(answer: Record<string, unknown> | undefined, fallback: number): number {
  const r = answer?.retry_ms
  return typeof r === 'number' && r >= 0 && r <= 3_600_000 ? r : fallback
}
