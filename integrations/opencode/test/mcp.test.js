import { test } from 'node:test'
import assert from 'node:assert/strict'
import { request } from 'node:http'
import { createMcpProxy, extractShellGates, matchShellGate } from '../lib/mcp.js'

async function postJson(url, headers, body) {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...headers },
    body: JSON.stringify(body),
  })
  const text = await res.text()
  return { status: res.status, json: text ? JSON.parse(text) : null }
}

test('revoking a worker binding while its HTTP body is pending prevents later tool execution', async (t) => {
  let calls = 0
  const proxy = createMcpProxy({ tools: () => [], call: () => { calls++; return { content: [] } } })
  t.after(() => proxy.stop())
  const route = await proxy.allocate({ assistant: true, sessionId: 'retired' })
  const body = JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'probe', arguments: {} } })
  const status = await new Promise((resolve, reject) => {
    const req = request(route.url, { method: 'POST', timeout: 2000, headers: { ...route.headersObject, 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body), Expect: '100-continue' } })
    t.after(() => req.destroy())
    req.on('error', reject)
    req.on('timeout', () => req.destroy(new Error('pending request timed out')))
    req.on('continue', () => { route.release(); req.end(body) })
    req.on('response', (res) => { res.resume(); res.on('end', () => resolve(res.statusCode)) })
    req.flushHeaders()
  })
  assert.equal(status, 404)
  assert.equal(calls, 0)
})

test('createMcpProxy enforces Bearer auth, method checks, route isolation, allocate+bind, and tool forwarding', async (t) => {
  const seenTools = []
  const seenCalls = []
  const proxy = createMcpProxy({
    tools: async (binding) => {
      seenTools.push({ assistant: binding.assistant, sessionId: binding.sessionId })
      return binding.assistant
        ? [{ name: 'todo_list', description: 'List todos', input_schema: { type: 'object', properties: {} }, shell: ['persona-todo'] }]
        : [{ name: 'report_to_supervisor', description: 'Report', input_schema: { type: 'object', properties: {} } }]
    },
    call: async (binding, name, args) => {
      seenCalls.push({ assistant: binding.assistant, sessionId: binding.sessionId, name, args })
      return {
        content: [
          { type: 'text', text: `ran ${name}` },
          { type: 'image', data: 'AAAA', mime_type: 'image/png' },
        ],
        is_error: false,
      }
    },
  })
  t.after(() => proxy.stop())

  const route = await proxy.allocate({ assistant: true })
  assert.match(route.url, /^http:\/\/127\.0\.0\.1:\d+\/mcp\//)
  assert.equal(route.acpServer.type, 'http')
  assert.equal(route.acpServer.name, 'persona')
  assert.equal(route.remoteConfig.type, 'remote')

  // Unauthorized requests are rejected with 401.
  const noAuth = await fetch(route.url, { method: 'POST', body: '{}' })
  assert.equal(noAuth.status, 401)
  const badAuth = await fetch(route.url, { method: 'POST', headers: { Authorization: 'Bearer wrong' }, body: '{}' })
  assert.equal(badAuth.status, 401)

  // Unknown route returns 404; GET returns 405; DELETE returns 204.
  const unknown = await fetch(route.url.replace(/\/mcp\/.*$/, '/mcp/nope'), { method: 'POST', headers: route.headersObject, body: '{}' })
  assert.equal(unknown.status, 404)
  const getRes = await fetch(route.url, { method: 'GET', headers: route.headersObject })
  assert.equal(getRes.status, 405)
  const delRes = await fetch(route.url, { method: 'DELETE', headers: route.headersObject })
  assert.equal(delRes.status, 204)

  // initialize, ping, notification (202), tools/list before bind.
  const init = await postJson(route.url, route.headersObject, { jsonrpc: '2.0', id: 1, method: 'initialize', params: {} })
  assert.equal(init.status, 200)
  assert.equal(init.json.result.serverInfo.name, 'persona')

  const notif = await postJson(route.url, route.headersObject, { jsonrpc: '2.0', method: 'notifications/initialized' })
  assert.equal(notif.status, 202)

  const list = await postJson(route.url, route.headersObject, { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} })
  assert.equal(list.status, 200)
  assert.deepEqual(list.json.result.tools.map((x) => x.name), ['todo_list'])
  assert.equal(seenTools[0].assistant, true)

  // Bind session and call tool.
  route.bind({ id: 'ses_asst_1', assistant: true })
  const callRes = await postJson(route.url, route.headersObject, {
    jsonrpc: '2.0',
    id: 3,
    method: 'tools/call',
    params: { name: 'todo_list', arguments: { status: 'open' } },
  })
  assert.equal(callRes.status, 200)
  assert.deepEqual(callRes.json.result, {
    content: [
      { type: 'text', text: 'ran todo_list' },
      { type: 'image', data: 'AAAA', mimeType: 'image/png' },
    ],
    isError: false,
  })
  assert.deepEqual(seenCalls[0], { assistant: true, sessionId: 'ses_asst_1', name: 'todo_list', args: { status: 'open' } })

  // Non-assistant session route gets only ordinary agent tools.
  const agentRoute = await proxy.forSession({ id: 'ses_agent_1', assistant: false })
  const agentList = await postJson(agentRoute.url, agentRoute.headersObject, { jsonrpc: '2.0', id: 4, method: 'tools/list', params: {} })
  assert.deepEqual(agentList.json.result.tools.map((x) => x.name), ['report_to_supervisor'])

  // Releasing a route removes it.
  route.release()
  const gone = await postJson(route.url, route.headersObject, { jsonrpc: '2.0', id: 5, method: 'ping' })
  assert.equal(gone.status, 404)
})

test('extractShellGates and matchShellGate detect gated commands, shell syntax, quoting, paths, and long prefixes', () => {
  const gates = extractShellGates([
    { name: 'mail_send', shell: ['send-mail', 'scripts/mail.py'] },
    { name: 'plain_tool' },
  ])
  assert.equal(gates.length, 1)
  assert.equal(matchShellGate('send-mail --to alice', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('send-mail; echo ok', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('send-mail&&id', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('send-mail||true', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('send-mail|cat', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('(send-mail)', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('"send-mail" --to alice', gates)?.name, 'mail_send')
  assert.equal(matchShellGate("'send-mail' --to alice", gates)?.name, 'mail_send')
  assert.equal(matchShellGate('send""-mail --to alice', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('\\send-mail --to alice', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('/usr/local/bin/send-mail --to alice', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('sh -c "send-mail; echo ok"', gates)?.name, 'mail_send')
  assert.equal(matchShellGate("bash -lc 'send-mail&&id'", gates)?.name, 'mail_send')
  assert.equal(matchShellGate('eval "send-mail --draft"', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('echo $(send-mail --to a)', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('echo `send-mail --to a`', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('echo $(printf ")"; send""-mail --to a)', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('echo "$(printf \')\'; \\send-mail --to a)"', gates)?.name, 'mail_send')
  assert.equal(matchShellGate("send$'-'mail --to alice", gates)?.name, 'mail_send')
  assert.equal(matchShellGate("$'send\\x2dmail' --to alice", gates)?.name, 'mail_send')
  assert.equal(matchShellGate(`${'x'.repeat(16_000)}; send-mail --to alice`, gates)?.name, 'mail_send')
  assert.equal(matchShellGate('echo ok && python3 /opt/scripts/mail.py --draft', gates)?.name, 'mail_send')
  assert.equal(matchShellGate('echo my-send-mail-helper', gates), undefined)
  assert.equal(matchShellGate('ls -la', gates), undefined)
  assert.equal(matchShellGate('', gates), undefined)
})
