import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { join } from 'node:path'

export async function fakeModel(t) {
  const requests = []
  const server = createServer(async (req, res) => {
    try {
      assert.equal(req.url, '/v1/chat/completions')
      let text = ''
      for await (const chunk of req) text += chunk
      const body = JSON.parse(text)
      requests.push(body)
      const lastUser = body.messages.findLastIndex((m) => m.role === 'user')
      const user = JSON.stringify(body.messages[lastUser]?.content ?? '')
      const alreadyCalled = body.messages.slice(lastUser + 1).some((m) => m.role === 'tool')
      const name = body.tools?.find((tool) => /probe$/.test(tool.function?.name))?.function.name
      // Native title/metadata requests may repeat the user text without any
      // tools. Only a main tool-enabled request can select probe; the outer
      // identity assertions still require every requested MCP call to happen.
      const call = /PROBE_IDENTITY/.test(user) && !alreadyCalled && Boolean(name)
      if (!body.stream) {
        res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ id: 'fake', object: 'chat.completion', model: 'test-model', choices: [{ index: 0, message: { role: 'assistant', content: 'Test conversation' }, finish_reason: 'stop' }], usage: { prompt_tokens: 10, completion_tokens: 2, total_tokens: 12 } }))
        return
      }
      res.writeHead(200, { 'Content-Type': 'text/event-stream' })
      const event = (delta, finish_reason = null) => res.write('data: ' + JSON.stringify({ id: 'fake', object: 'chat.completion.chunk', model: 'test-model', choices: [{ index: 0, delta, finish_reason }] }) + '\n\n')
      event({ role: 'assistant' })
      if (call) event({ tool_calls: [{ index: 0, id: 'probe-call', type: 'function', function: { name, arguments: '{}' } }] })
      else event({ content: 'contract complete' })
      event({}, call ? 'tool_calls' : 'stop')
      res.end('data: [DONE]\n\n')
    } catch (error) {
      requests.push({ error: error.message })
      res.writeHead(500).end(JSON.stringify({ error: { message: error.message } }))
    }
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  t.after(() => { server.closeAllConnections(); server.close() })
  return { url: `http://127.0.0.1:${server.address().port}/v1`, requests }
}

export function environment(home, modelUrl) {
  return {
    PATH: process.env.PATH, LANG: 'C.UTF-8', HOME: home, OPENCODE_TEST_HOME: home,
    XDG_CONFIG_HOME: join(home, 'config'), XDG_DATA_HOME: join(home, 'data'),
    XDG_STATE_HOME: join(home, 'state'), XDG_CACHE_HOME: join(home, 'cache'),
    OPENCODE_DISABLE_PROJECT_CONFIG: '1', OPENCODE_PURE: '1', OPENCODE_DISABLE_AUTOUPDATE: '1',
    OPENCODE_DISABLE_AUTOCOMPACT: '1', OPENCODE_DISABLE_MODELS_FETCH: '1', OPENCODE_AUTH_CONTENT: '{}',
    OPENCODE_CONFIG_CONTENT: JSON.stringify({
      model: 'test/test-model', small_model: 'test/test-model', formatter: false, lsp: false,
      enabled_providers: ['test'], share: 'disabled', permission: { '*': 'allow' },
      provider: { test: { name: 'Test', id: 'test', env: [], npm: '@ai-sdk/openai-compatible',
        options: { apiKey: 'fake-contract-key', baseURL: modelUrl },
        models: { 'test-model': { id: 'test-model', name: 'Test Model', attachment: false, reasoning: false,
          temperature: false, tool_call: true, limit: { context: 100_000, output: 10_000 }, cost: { input: 0, output: 0 } } },
      } },
    }),
  }
}
