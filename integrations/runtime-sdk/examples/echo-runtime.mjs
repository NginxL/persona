// The smallest complete Persona runtime (docs/integrations/runtimes.md "Minimal example"): sessions in memory, every message answered with
// an echo after a short "thinking" stream, and a permission card before each echo when the text says so. Start it
// next to a personad node to see a runtime in the apps without any agent, or copy it as the skeleton of a new runtime:
//
//   node integrations/runtime-sdk/examples/echo-runtime.mjs [--socket <path>]
//
// or as a daemon the node dials (docs/integrations/daemons.md), the token in PERSONA_DAEMON_TOKEN:
//
//   PERSONA_DAEMON_TOKEN=<token> node integrations/runtime-sdk/examples/echo-runtime.mjs --listen <port>
//
// What a real runtime replaces: `sessions` with its own session store, `reply` with its agent, and the item mapping
// with its own (see integrations/dsh/lib/map.js).

import { randomUUID } from 'node:crypto'
import { realpathSync } from 'node:fs'
import { basename } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRuntime, interactionId, light, page, RpcError } from '../index.js'

/** An echo runtime; options are createRuntime's (socketPath, answerFirst…). */
export function createEchoRuntime(options = {}) {
  const sessions = new Map() // id -> { id, cwd, title, items: [], busy, updatedAt }
  let rt

  const now = () => new Date().toISOString()
  const toSeq = (s) => s.items.length // seq = position: item n has seq n (1-based)

  function add(s, item) {
    const seq = s.items.length + 1
    const it = { id: `${seq}`, seq, ts: now(), ...item }
    s.items.push(it)
    s.updatedAt = Date.now()
    rt.append(s.id, { items: [light(it)], fromSeq: seq - 1, toSeq: seq, history: `echo:${s.id}` })
    return it
  }

  async function reply(s, text, clientMsgId) {
    s.busy = true
    rt.sessionsChanged(0)
    add(s, { role: 'user', kind: 'text', text })
    const answer = `echo: ${text}`
    if (/\bask\b/i.test(text)) {
      const res = await rt.ask({ id: interactionId('echo', s.id, 'permission', clientMsgId ?? randomUUID()), kind: 'permission',
        session_id: s.id, tool_name: 'Echo', tool_input: { text }, summary: `echo ${text}` })
      if (res.allow === false) {
        add(s, { role: 'system', kind: 'notice', text: '已拒绝' })
        s.busy = false
        rt.sessionsChanged(0)
        return
      }
    }
    // Slower than the SDK's 120 ms between live texts: some of it goes out before the end (only the first would at 20).
    for (let i = 1; i <= answer.length; i += 4) {
      rt.stream(s.id, answer.slice(0, i))
      await new Promise((r) => setTimeout(r, 50))
    }
    rt.streamEnd(s.id)
    add(s, { role: 'assistant', kind: 'text', text: answer })
    s.busy = false
    rt.sessionsChanged(0)
  }

  // Paging is the SDK's (page): a runtime only says what its items are, in seq order.
  const pageOf = (s, params) => page(s.items, params, { history: `echo:${s.id}`, toSeq: toSeq(s) })

  const get = (id) => {
    const s = sessions.get(id)
    if (!s) throw new RpcError(`no session ${id}`, 'not_found')
    return s
  }

  rt = createRuntime({
    async listSessions() {
      return [...sessions.values()].map((s) => ({
        session_id: s.id, title: s.title, cwd: s.cwd, status: s.busy ? 'working' : 'done',
        updated_at: new Date(s.updatedAt).toISOString(), rev: toSeq(s), space_id: s.cwd, space_name: basename(s.cwd),
        last_text: s.items.findLast((i) => i.role === 'assistant')?.text ?? '',
      }))
    },
    async items(params) {
      return pageOf(get(params.session_id), params)
    },
    async cursor(id) {
      return toSeq(get(id))
    },
    async item(params) {
      const it = get(params.session_id).items.find((i) => i.id === params.item_id)
      if (!it) throw new RpcError(`no item ${params.item_id}`, 'not_found')
      return it
    },
    async send(params) {
      const s = get(params.session_id)
      void reply(s, params.text ?? '', params.client_msg_id)
    },
    async interrupt(params) {
      get(params.session_id).busy = false
      rt.sessionsChanged(0)
    },
    async start(params) {
      const id = `echo-${randomUUID().slice(0, 8)}`
      const s = { id, cwd: params.cwd, title: basename(params.cwd) || 'echo', items: [], busy: false, updatedAt: Date.now() }
      sessions.set(id, s)
      if (params.prompt) void reply(s, params.prompt)
      return { session: { session_id: id, title: s.title, cwd: s.cwd, status: 'idle', updated_at: now() } }
    },
    async rename(params) {
      get(params.session_id).title = params.title
    },
  }, { runtime: 'echo', name: 'Echo', version: '1', launcher: 'echo', launcherModel: 'echo', ...options })

  return { runtime: rt, sessions }
}

// Whether this file is the program Node runs. Node loads the entry by its real path, so compare real paths: a symlinked
// directory on the way would otherwise skip the runtime and exit 0 (#12848).
function isEntry() {
  try {
    return !!process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))
  } catch {
    return false // no file behind argv[1] (node -e, a REPL import)
  }
}

// Run it: node examples/echo-runtime.mjs [--socket <path> | --listen <port>]
if (isEntry()) {
  const i = process.argv.indexOf('--socket')
  const l = process.argv.indexOf('--listen')
  const opts = { answerFirst: 'phone', log: (m) => console.error(m) }
  if (i > 0) opts.socketPath = process.argv[i + 1]
  if (l > 0) {
    const token = process.env.PERSONA_DAEMON_TOKEN
    if (!token) {
      console.error('set PERSONA_DAEMON_TOKEN (the token daemons.json gives for this daemon)')
      process.exit(2)
    }
    opts.listen = { port: Number(process.argv[l + 1]), token }
  }
  const { runtime } = createEchoRuntime(opts)
  const addr = await runtime.start()
  console.error(addr ? `echo daemon: ws://${addr.address}:${addr.port}/persona/runtime; Ctrl-C to stop` : 'echo runtime: waiting for the personad node; Ctrl-C to stop')
}
