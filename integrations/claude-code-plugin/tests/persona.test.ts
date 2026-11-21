import type { On } from 'claude-code'
import { describe, expect, mock, test } from 'claude-code/testing'
import { applyStepOverride, homeDir, mergeOverride, nextBackoff, readPoll, socketPath, verdict } from '../hooks/bridge'

const SESSION = '11111111-2222-3333-4444-555555555555'

type Answer = { status: number; body: unknown } | { fail: true }

// The engine beneath the plugin: a session, its manifest, and a node that answers polls in the order given (then
// none: a node that went away), recording every request and every prompt the plugin submits.
function world(on: On, polls: Answer[], env: Record<string, string> = { HOME: '/home/me' }) {
  const requests: { url: string; socketPath?: string; body: Record<string, unknown> }[] = []
  const submitted: { text: string; origin: unknown }[] = []
  const aborted: string[] = []
  const steps: { model: string; effort?: string | number; agentId?: string }[] = []
  let holdSubmit = false
  let abortError: string | undefined
  let sessionModel = 'claude-sonnet-5-5'
  mock.env(on, env)
  const clock = mock.clock(on, { now: 1_000 })
  on('session.start', (_$, e) => ({ cwd: e.cwd }))
  on('session.id', () => ({ value: SESSION }))
  on('session.model', () => ({ value: sessionModel }))
  on('session.version', () => ({ value: { version: '2.1.326', base: '2.1.326' } }))
  on('fs.read', () => ({ value: '{"name":"persona","version":"0.2.0"}' }))
  on('ui.log', () => ({ value: undefined }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: 'done' }))
  on('turn.abort', (_$, e) => {
    if (abortError !== undefined) throw new Error(abortError)
    aborted.push(e.turnId)
    return { value: undefined }
  })
  on('prompt.submit', (_$, e) => {
    submitted.push({ text: e.text, origin: e.origin })
    if (holdSubmit) return new Promise(() => {})
    return { text: e.text }
  })
  on('turn.step', async function* (_$, e) {
    steps.push({ model: e.model, ...(e.effort !== undefined ? { effort: e.effort } : {}), ...(e.agentId !== undefined ? { agentId: e.agentId } : {}) })
    return { turnId: e.turnId, index: e.index, answer: 'ok', toolUses: [], stopReason: 'end_turn' as const, usage: null }
  })
  on('http.fetch', (_$, e) => {
    const rawBody = typeof e.init?.body === 'string' ? e.init.body : '{}'
    requests.push({ url: e.url, socketPath: e.init?.socketPath, body: JSON.parse(rawBody) })
    if (e.url.endsWith('/ack')) return { value: { status: 200, ok: true, headers: {}, text: '{"ok":true}' } }
    const next = polls.shift() ?? { fail: true }
    if ('fail' in next) throw new Error('connection refused')
    return { value: { status: next.status, ok: next.status === 200, headers: {}, text: JSON.stringify(next.body) } }
  })
  return {
    requests,
    submitted,
    aborted,
    steps,
    clock,
    setHoldSubmit(v: boolean) {
      holdSubmit = v
    },
    setAbortError(err: string | undefined) {
      abortError = err
    },
    setSessionModel(m: string) {
      sessionModel = m
    },
  }
}

const start = { cwd: '/work', surface: 'desktop', isInteractive: true } as const

describe('persona plugin', () => {
  test('submits the node’s message as the person’s words and says so', async ($, on) => {
    const w = world(on, [{ status: 200, body: { messages: [{ id: 'm1', text: 'go ahead and merge', deadline_ms: 9_000 }] } }])
    await $.session.start(start)
    await w.clock.settle()

    expect(w.submitted).toEqual([{ text: 'go ahead and merge', origin: { kind: 'plugin', name: 'persona', asUser: true } }])
    const [poll, ack] = w.requests
    expect(poll?.url).toBe('http://persona/claude-plugin/v1/poll')
    expect(poll?.socketPath).toBe('/home/me/.persona/node.sock')
    expect(poll?.body).toEqual({
      session_id: SESSION,
      plugin_version: '0.2.0',
      engine_version: '2.1.326',
      capabilities: ['busy_input', 'interrupt', 'settings'],
      wait_ms: 25_000,
    })
    expect(ack?.url).toBe('http://persona/claude-plugin/v1/ack')
    expect(ack?.body).toEqual({ session_id: SESSION, id: 'm1', submitted: true })
  })

  test('queues a busy message without blocking the poll loop and aborts the active main-loop turn on interrupt', async ($, on) => {
    const w = world(on, [
      { status: 200, body: { messages: [{ id: 'm-busy', text: 'also check edge cases', deadline_ms: 9_000 }] } },
      { status: 200, body: { messages: [], interrupt: { id: 'int-1', deadline_ms: 9_000 } } },
      { status: 200, body: { messages: [], interrupt: { id: 'int-2', deadline_ms: 9_000 } } },
    ])
    w.setHoldSubmit(true)
    await $.turn.start({ text: 'initial task', turnId: 'turn-main-1' })
    await $.turn.start({ text: 'subagent task', turnId: 'turn-sub-1', agentId: 'sub-1' })
    await $.session.start(start)
    await w.clock.settle()

    // While prompt.submit waits behind the running turn, advancing the settle window acks the queued message and polls on.
    await w.clock.advance(250)
    await w.clock.settle()

    expect(w.submitted).toEqual([{ text: 'also check edge cases', origin: { kind: 'plugin', name: 'persona', asUser: true } }])
    expect(w.aborted).toEqual(['turn-main-1'])
    expect(w.requests.filter(r => r.url.endsWith('/ack')).map(r => r.body)).toEqual([
      { session_id: SESSION, id: 'm-busy', submitted: true },
      { session_id: SESSION, id: 'int-1', submitted: true },
      { session_id: SESSION, id: 'int-2', submitted: false, reason: 'no_turn' },
    ])
  })

  test('applies session-only model and effort switches on main-loop turn.step and yields when desktop changes model', async ($, on) => {
    const w = world(on, [
      { status: 200, body: { messages: [], settings: { id: 's1', model: 'gateway-opus-5-5[1m]', effort: 'xhigh' } } },
    ])
    await $.session.start(start)
    await w.clock.settle()

    expect(w.requests.filter(r => r.url.endsWith('/ack')).map(r => r.body)).toEqual([
      { session_id: SESSION, id: 's1', submitted: true },
    ])

    const runStep = async (input: Parameters<typeof $.turn.step>[0]) => {
      const gen = $.turn.step(input)
      while (!(await gen.next()).done) {
        // drain chunks
      }
    }

    // Main loop step gets the picked model and effort; subagent step keeps its own model.
    await runStep({ turnId: 't1', index: 0, model: 'claude-sonnet-5-5', effort: 'medium', messageCount: 1 })
    await runStep({ turnId: 't1', index: 0, model: 'claude-haiku-5-5', messageCount: 1, agentId: 'sub-1' })
    expect(w.steps).toEqual([
      { model: 'gateway-opus-5-5[1m]', effort: 'xhigh' },
      { model: 'claude-haiku-5-5', agentId: 'sub-1' },
    ])

    // Changing model in Claude Desktop UI supersedes Persona's earlier pick.
    w.setSessionModel('claude-opus-5-5')
    await runStep({ turnId: 't2', index: 0, model: 'claude-opus-5-5', effort: 'high', messageCount: 2 })
    expect(w.steps.at(-1)).toEqual({ model: 'claude-opus-5-5', effort: 'high' })
  })

  test('submits nothing late, empty or a slash command, and tells the node why', async ($, on) => {
    const w = world(on, [{
      status: 200,
      body: {
        messages: [
          { id: 'late', text: 'too late', deadline_ms: 500 },
          { id: 'cmd', text: ' /exit', deadline_ms: 9_000 },
          { id: 'empty', text: '  ', deadline_ms: 9_000 },
          { id: 'odd', text: 42, deadline_ms: 9_000 },
          { id: 'bad id!', text: 'no id', deadline_ms: 9_000 },
        ],
      },
    }])
    await $.session.start(start)
    await w.clock.settle()

    expect(w.submitted).toEqual([])
    expect(w.requests.filter(r => r.url.endsWith('/ack')).map(r => r.body)).toEqual([
      { session_id: SESSION, id: 'late', submitted: false, reason: 'late' },
      { session_id: SESSION, id: 'cmd', submitted: false, reason: 'command' },
      { session_id: SESSION, id: 'empty', submitted: false, reason: 'invalid' },
      { session_id: SESSION, id: 'odd', submitted: false, reason: 'invalid' },
    ])
  })

  test('backs off quietly while no node answers, and polls again once one does', async ($, on) => {
    const w = world(on, [{ fail: true }, { status: 404, body: {} }, { status: 200, body: { messages: [] } }])
    await $.session.start(start)
    await w.clock.settle()
    expect(w.requests.length).toBe(1)
    await w.clock.advance(2_000) // the first backoff: an old node answers 404
    expect(w.requests.length).toBe(2)
    await w.clock.advance(3_999) // the second is twice the first
    expect(w.requests.length).toBe(2)
    await w.clock.advance(1)
    expect(w.requests.length).toBe(4) // answered empty: the next poll goes at once (and finds no node)
    expect(w.submitted).toEqual([])
  })

  test('waits as long as the node asks when it does not serve this session', async ($, on) => {
    const w = world(on, [{ status: 403, body: { error: 'not_this_session', retry_ms: 300_000 } }])
    await $.session.start(start)
    await w.clock.settle()
    await w.clock.advance(299_999)
    expect(w.requests.length).toBe(1)
    await w.clock.advance(1)
    expect(w.requests.length).toBe(2)
  })

  test('finds the node where the node itself looks', async ($, on) => {
    const w = world(on, [], { USERPROFILE: 'C:\\Users\\me', HOME: '/c/Users/me', PERSONA_HOME: 'persona-data' })
    await $.session.start(start)
    await w.clock.settle()
    expect(w.requests[0]?.socketPath).toBe('C:\\Users\\me\\persona-data\\node.sock')
  })
})

describe('protocol helpers', () => {
  test('socket path', () => {
    expect(socketPath('/home/me', undefined)).toBe('/home/me/.persona/node.sock')
    expect(socketPath('/home/me', '~')).toBe('/home/me/node.sock')
    expect(socketPath('/home/me', '~/state')).toBe('/home/me/state/node.sock')
    expect(socketPath('/home/me', '/srv/persona')).toBe('/srv/persona/node.sock')
    expect(socketPath('C:\\Users\\me', undefined)).toBe('C:\\Users\\me\\.persona\\node.sock')
    expect(socketPath('C:\\Users\\me', 'D:\\persona')).toBe('D:\\persona\\node.sock')
    expect(socketPath(undefined, '/srv/persona')).toBe(undefined)
    expect(homeDir('C:\\Users\\me', '/c/Users/me')).toBe('C:\\Users\\me')
    expect(homeDir(undefined, '/Users/me')).toBe('/Users/me')
  })

  test('poll answers and override helpers', () => {
    expect(readPoll(200, '{"messages":[{"id":"a","text":"hi","deadline_ms":5}],"interrupt":{"id":"i1","deadline_ms":9},"settings":{"id":"s1","model":"opus","effort":"high"},"retry_ms":10}')).toEqual({
      kind: 'messages',
      messages: [{ id: 'a', text: 'hi', deadlineMs: 5 }],
      interrupt: { id: 'i1', deadlineMs: 9 },
      settings: { id: 's1', model: 'opus', effort: 'high' },
      retryMs: 10,
    })
    expect(readPoll(200, '{"messages":[],"settings":{"id":"s2","model":"-bad"}}')).toEqual({
      kind: 'messages',
      messages: [],
      retryMs: 0,
    })
    const ov = mergeOverride(undefined, SESSION, { id: 's1', model: 'opus', effort: 'auto' }, 'claude-sonnet-5-5')
    expect(applyStepOverride({ model: 'claude-sonnet-5-5', effort: 'medium' }, ov, SESSION, 'claude-sonnet-5-5').step).toEqual({
      model: 'opus',
    })
    expect(readPoll(200, 'not json')).toEqual({ kind: 'unavailable' })
    expect(readPoll(404, '')).toEqual({ kind: 'unavailable' })
    expect(readPoll(403, '{}')).toEqual({ kind: 'refused', retryMs: 60_000 })
    expect(verdict({ id: 'a', text: 'x'.repeat(200_001), deadlineMs: 10 }, 0)).toBe('invalid')
    expect(nextBackoff(0)).toBe(2_000)
    expect(nextBackoff(40_000)).toBe(60_000)
  })
})
