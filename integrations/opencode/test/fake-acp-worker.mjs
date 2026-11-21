// A deliberately directory-scoped MCP backend: assistants must use different
// processes even when their cwd and MCP server name are identical.
import { appendFileSync, readFileSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { createInterface } from 'node:readline'
import { spawn } from 'node:child_process'
const record = (data) => appendFileSync(process.env.FAKE_OPENCODE_RECORD, JSON.stringify({ worker: process.pid, ...data }) + '\n')
const write = (data) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...data }) + '\n')
const sessions = new Map()
const directories = new Map()
const held = new Map()
const permissions = new Map()
let nextAsk = 500
const configOptions = [
  { id: 'mode', category: 'mode', currentValue: 'build', options: [{ value: 'build' }, { value: 'plan' }] },
  { id: 'model', category: 'model', currentValue: 'mock/one', options: [{ value: 'mock/one', name: 'One' }, { value: 'mock/two', name: 'Two' }] },
  { id: 'variant', category: 'reasoning', currentValue: 'low', options: [{ value: 'low' }, { value: 'high' }] },
]
const optionsOf = (sessionId) => configOptions.map((option) => ({ ...option, currentValue: sessions.get(sessionId)?.[option.id] || option.currentValue }))
const processInstructions = JSON.parse(process.env.OPENCODE_CONFIG_CONTENT || '{}').instructions || []
record({ started: true, args: process.argv.slice(2), instructions: processInstructions.map((file) => ({ file, text: readFileSync(file, 'utf8') })) })
if (process.env.FAKE_OPENCODE_CHILD === '1') {
  const descendant = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' })
  record({ descendant: descendant.pid })
}
const update = (sessionId, text) => write({ method: 'session/update', params: { sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } } } })
for await (const line of createInterface({ input: process.stdin })) {
  let request
  try { request = JSON.parse(line) } catch { continue }
  void (async () => {
    const { id, method, params = {} } = request
    if (!method && permissions.has(id)) { permissions.get(id)(request.result); permissions.delete(id); return }
    record({ method, params })
    let result = {}
    switch (method) {
      case 'initialize': result = { protocolVersion: 1, agentCapabilities: { sessionCapabilities: { resume: true, ...(process.env.FAKE_OPENCODE_SYSTEM_PROMPT === '1' ? { systemPrompt: {} } : {}) }, mcpCapabilities: { http: true } }, agentInfo: { name: 'fake-opencode', version: process.env.FAKE_OPENCODE_VERSION || 'test' } }; break
      case 'session/new': {
        const sessionId = 'ses_' + randomUUID()
        sessions.set(sessionId, { cwd: params.cwd })
        directories.set(params.cwd, params.mcpServers)
        result = { sessionId, configOptions: optionsOf(sessionId) }
        break
      }
      case 'session/resume':
      case 'session/load':
        sessions.set(params.sessionId, { cwd: params.cwd })
        directories.set(params.cwd, params.mcpServers)
        result = { configOptions: optionsOf(params.sessionId) }
        break
      case 'session/set_config_option':
        sessions.get(params.sessionId)[params.configId] = params.value
        result = { configOptions: optionsOf(params.sessionId) }; break
      case 'session/prompt': {
        const text = params.prompt.filter((block) => block.type === 'text').map((block) => block.text).join('\n')
        if (text === 'hold') { held.set(params.sessionId, id); return }
        if (text === 'ask') {
          const askId = nextAsk++
          const answer = new Promise((resolve) => permissions.set(askId, resolve))
          write({ id: askId, method: 'session/request_permission', params: { sessionId: params.sessionId, toolCall: { toolCallId: 'ask', kind: 'execute', title: 'bash', rawInput: { command: 'echo fake' } }, options: [{ optionId: 'yes', kind: 'allow_once' }, { optionId: 'no', kind: 'reject_once' }] } })
          await answer
        }
        if (text === 'probe') {
          const server = directories.get(sessions.get(params.sessionId).cwd)[0]
          const response = await fetch(server.url, { method: 'POST', headers: { 'Content-Type': 'application/json', ...Object.fromEntries(server.headers.map(({ name, value }) => [name, value])) }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'probe', arguments: {} } }) }).then((r) => r.json())
          record({ toolResult: response, sessionId: params.sessionId })
        }
        if (text.startsWith('foreign:')) update(text.slice('foreign:'.length), 'must not cross sessions')
        update(params.sessionId, 'finished')
        result = { stopReason: 'end_turn' }
        record({ ended: params.sessionId })
        break
      }
      case 'session/cancel': {
        const prompt = held.get(params.sessionId)
        if (prompt !== undefined) { held.delete(params.sessionId); write({ id: prompt, result: { stopReason: 'cancelled' } }) }
        return
      }
      case 'session/close': sessions.delete(params.sessionId); break
    }
    if (id !== undefined) write({ id, result })
  })().catch((error) => { if (request.id !== undefined) write({ id: request.id, error: { code: -32000, message: error.message } }) })
}
