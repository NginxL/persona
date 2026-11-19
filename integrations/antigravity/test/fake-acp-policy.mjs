// A fake `agy acp` for supervision-policy.test.js.
//
// Flags:
//   --record <path>            append one JSON line per inbound request/notification
//   --no-additional-dirs       omit agentCapabilities.sessionCapabilities.additionalDirectories
//   --fail-resume              reject session/resume requests with an ACP error
//
// Prompt behavior:
//   text containing 'hold'     keeps session/prompt open until session/cancel arrives
//   anything else              immediately emits a short agent message and ends with end_turn
import { appendFileSync } from 'node:fs'
import { createInterface } from 'node:readline'

if (process.argv.includes('models')) {
  console.log('test-model\tTest Model')
  process.exit(0)
}

const recordFile = process.argv[process.argv.indexOf('--record') + 1]
const noAdditionalDirs = process.argv.includes('--no-additional-dirs')
const failResume = process.argv.includes('--fail-resume')
const record = (o) => appendFileSync(recordFile, `${JSON.stringify(o)}\n`)
const send = (o) => process.stdout.write(`${JSON.stringify(o)}\n`)

let nextSession = 1
const turns = new Map() // sessionId -> { id }

function sessionUUID(n) {
  return `11111111-2222-4333-8444-${String(n).padStart(12, '0')}`
}

function onRequest(msg) {
  const { id, method, params } = msg
  const answer = (result) => send({ jsonrpc: '2.0', id, result })
  switch (method) {
    case 'initialize':
      return answer({
        protocolVersion: 1,
        agentInfo: { name: 'fake-agy', version: '1.5.12' },
        agentCapabilities: noAdditionalDirs
          ? { sessionCapabilities: {} }
          : { sessionCapabilities: { additionalDirectories: {} } },
      })
    case 'session/new': {
      const sessionId = sessionUUID(nextSession++)
      return answer({
        sessionId,
        models: { currentModelId: 'test-model', availableModels: [{ modelId: 'test-model', name: 'Test Model' }] },
      })
    }
    case 'session/resume':
      if (failResume) {
        return send({ jsonrpc: '2.0', id, error: { code: -32000, message: 'session/resume refused additionalDirectories' } })
      }
      return answer({})
    case 'session/set_model':
    case 'session/set_mode':
    case 'session/set_config_option':
    case 'session/close':
      return answer({})
    case 'session/prompt': {
      const sessionId = params.sessionId
      const text = params.prompt?.find((b) => b.type === 'text')?.text || ''
      if (text.includes('hold')) {
        turns.set(sessionId, { id })
        return
      }
      send({
        jsonrpc: '2.0',
        method: 'session/update',
        params: {
          sessionId,
          update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'ok' } },
        },
      })
      record({ ended: sessionId })
      return answer({ stopReason: 'end_turn' })
    }
    default:
      return send({ jsonrpc: '2.0', id, error: { code: -32601, message: `Method not found: ${method}` } })
  }
}

function onNotification(msg) {
  if (msg.method !== 'session/cancel') return
  const sessionId = msg.params?.sessionId
  const turn = turns.get(sessionId)
  if (!turn) return
  turns.delete(sessionId)
  record({ ended: sessionId })
  send({ jsonrpc: '2.0', id: turn.id, result: { stopReason: 'cancelled' } })
}

record({ started: process.pid })
const rl = createInterface({ input: process.stdin, crlfDelay: Infinity })
rl.on('line', (line) => {
  if (!line.trim()) return
  const msg = JSON.parse(line)
  record(msg)
  if (msg.method && msg.id !== undefined) onRequest(msg)
  else if (msg.method) onNotification(msg)
})
rl.on('close', () => process.exit(0))
