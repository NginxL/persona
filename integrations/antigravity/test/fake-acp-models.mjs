import { appendFileSync, readFileSync } from 'node:fs'
import { createInterface } from 'node:readline'

const directory = process.argv[2]
const settings = () => JSON.parse(readFileSync(`${directory}/catalog.json`, 'utf8'))
if (process.argv.includes('models')) {
  const catalog = settings()
  appendFileSync(`${directory}/models-calls.jsonl`, `${JSON.stringify({ at: Date.now() })}\n`)
  // An expired sign-in: agy models never answers (#11106).
  if (catalog.hang) setInterval(() => {}, 1 << 30)
  else {
  if (catalog.error) { console.error(catalog.error); process.exit(1) }
  console.error('Fetching available models...')
  for (const m of catalog.models) console.log(`${m.modelId}\t${m.name}`)
  process.exit(0)
  }
}
const id = '11111111-2222-3333-4444-555555555555'
let model = 'gemini-future-medium'
let effort = 'medium'
let mode = 'default'
const configuration = () => [
  { id: 'model', category: 'model', currentValue: model.replace(/-(low|medium|high)$/, '') },
  { id: 'thought_level', currentValue: effort },
  { id: 'mode', currentValue: mode },
]
let pendingPrompt
const send = (value) => process.stdout.write(`${JSON.stringify(value)}\n`)
createInterface({ input: process.stdin }).on('line', (line) => {
  const request = JSON.parse(line)
  appendFileSync(`${directory}/calls.jsonl`, `${line}\n`)
  const { method, params } = request
  if (request.id === 'check-permission' && !method) {
    send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'OK' } } } })
    send({ jsonrpc: '2.0', id: pendingPrompt, result: { stopReason: 'end_turn' } })
    return
  }
  if (!method) return
  let result = {}
  let error
  if (method === 'initialize') result = { protocolVersion: 1, agentInfo: { version: 'test' } }
  if (method === 'session/new' || method === 'session/resume') {
    result = { sessionId: id, models: { availableModels: settings().models, currentModelId: model }, modes: { currentModeId: mode }, configOptions: configuration() }
  }
  if (method === 'session/prompt') {
    if (settings().holdPrompt) return
    if (settings().promptError) error = { code: -32000, message: settings().promptError }
    else if (settings().askPermission) {
      pendingPrompt = request.id
      send({ jsonrpc: '2.0', id: 'check-permission', method: 'session/request_permission', params: { sessionId: id, toolCall: { title: 'run_command', input: { command: 'pwd' } }, options: [{ kind: 'allow_once', optionId: 'allow' }] } })
      return
    } else {
      send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'OK' } } } })
      result = { stopReason: 'end_turn' }
    }
  }
  if (method === 'session/set_model') {
    if (!settings().models.some((entry) => entry.modelId === params.modelId)) error = { code: -32602, message: 'Model unavailable' }
    else { model = params.modelId; effort = model.split('-').at(-1) }
  }
  if (method === 'session/set_mode') mode = params.modeId
  if (method === 'session/set_config_option') {
    if (!['low', 'medium', 'high'].includes(params.value)) error = { code: -32602, message: 'Thinking level unavailable' }
    else { effort = params.value; model = model.replace(/-(low|medium|high)$/, `-${effort}`); result = { configOptions: configuration() } }
  }
  if (request.id !== undefined) process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id: request.id, ...(error ? { error } : { result }) })}\n`)
  if (['session/set_model', 'session/set_mode'].includes(method) && !error) process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'config_option_update', configOptions: configuration() } } })}\n`)
})
