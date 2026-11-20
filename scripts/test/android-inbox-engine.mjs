// Keep one disposable Codex thread loaded for Persona's real node. The initial turn makes
// it discoverable. From then on only the Android app may send to it.
import { writeFile, rename } from 'node:fs/promises'
import { join } from 'node:path'
import { once } from 'node:events'

const [operation, endpoint, subject, model] = process.argv.slice(2)
if (new URL(endpoint).hostname !== '127.0.0.1') throw new Error('Expected a loopback app-server')
const socket = new WebSocket(endpoint)
await once(socket, 'open')
const pending = new Map()
let sequence = 0
let finishSeed
const completed = new Promise(resolve => { finishSeed = resolve })
socket.addEventListener('message', event => {
  const message = JSON.parse(event.data)
  const call = pending.get(message.id)
  if (call) {
    pending.delete(message.id)
    if (message.error) call.reject(new Error(message.error.message))
    else call.resolve(message.result)
  } else if (message.method === 'turn/completed') {
    finishSeed(message.params.turn)
  }
})
function call(method, params) {
  const id = ++sequence
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject })
    socket.send(JSON.stringify({ id, method, params }))
  })
}
await call('initialize', { clientInfo: { name: 'inbox_fixture', version: '1' }, capabilities: { experimentalApi: true } })
socket.send(JSON.stringify({ method: 'initialized' }))
if (operation === 'read') {
  const { thread } = await call('thread/read', { threadId: subject, includeTurns: true })
  // This is the engine's own transcript, independent of Persona's session-history projection.
  const turns = thread.turns.map(turn => ({
    id: turn.id, status: turn.status,
    inputs: turn.items.filter(i => i.type === 'userMessage').map(i => i.content.filter(c => c.type === 'text').map(c => c.text).join('')),
    answers: turn.items.filter(i => i.type === 'agentMessage').map(i => i.text),
  }))
  process.stdout.write(JSON.stringify(turns))
  socket.close()
} else if (operation === 'seed') {
  const directory = subject
  const { thread } = await call('thread/start', {
    cwd: directory, model, modelProvider: 'fixture', approvalPolicy: 'never', sandbox: 'read-only',
    baseInstructions: 'This is a text-only integration test. Answer in plain text. Do not call tools.',
  })
  const { turn } = await call('turn/start', {
    threadId: thread.id, input: [{ type: 'text', text: 'Reply with only READY.', text_elements: [] }],
  })
  const seed = await completed
  if (seed.id !== turn.id || seed.status !== 'completed') throw new Error(`Seed turn ${seed.status}: ${JSON.stringify(seed.error)}`)
  const temporary = join(directory, 'engine-ready.tmp')
  await writeFile(temporary, JSON.stringify({ thread: thread.id, seed: seed.id }), { mode: 0o600 })
  await rename(temporary, join(directory, 'engine-ready.json'))
  await once(socket, 'close')
} else {
  throw new Error('Expected seed or read')
}
