// Opt-in pinned native-engine contract. The CI wrapper verifies the binary;
// all model traffic goes to this test's local deterministic HTTP server.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { mkdtempSync, mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { performance } from 'node:perf_hooks'
import { OpencodeAcpClient } from '../lib/acp.js'
import { createMcpProxy } from '../lib/mcp.js'
import { createOpencodeCompanion } from '../lib/companion.js'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { fakeModel, environment } from './real-acp-fixture.mjs'

const bin = process.env.OPENCODE_CONTRACT_BIN
const probe = { name: 'probe', description: 'Return the test conversation identity', input_schema: { type: 'object', properties: {} } }

test('pinned OpenCode reproduces shared MCP replacement and isolates same-directory assistant identities', { skip: !bin, timeout: 210_000 }, async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'opencode-real-contract-'))
  const cwd = join(home, 'assistant')
  mkdirSync(cwd)
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const model = await fakeModel(t)
  const env = environment(home, model.url)
  const logs = []
  const sharedCalls = []
  const proxy = createMcpProxy({ tools: () => [probe], call: (binding) => {
    sharedCalls.push(binding.sessionId)
    return { content: [{ type: 'text', text: 'fake identity' }] }
  } })
  const shared = new OpencodeAcpClient({ bin, cwd: home, env, processGroup: true, log: (line) => logs.push(line),
    onPermissionRequest: ({ options }) => ({ outcome: { outcome: 'selected', optionId: options.find((option) => option.kind === 'allow_once').optionId } }),
  })
  t.after(async () => { await shared.stop(); proxy.stop() })
  try {
    const routeA = await proxy.allocate({ sessionId: 'a', assistant: true })
    const routeB = await proxy.allocate({ sessionId: 'b', assistant: true })
    const initialized = await shared.ensureStarted()
    assert.equal(initialized.agentInfo?.version, '1.19.6')
    const first = await shared.newSession({ cwd, mcpServers: [routeA.acpServer] })
    await shared.newSession({ cwd, mcpServers: [routeB.acpServer] })
    await shared.prompt(first.sessionId, [{ type: 'text', text: 'PROBE_IDENTITY shared' }], 30_000)
    assert.deepEqual(sharedCalls, ['b'], 'pinned engine replaces a same-name MCP route within a directory')
    await shared.stop()

    const node = await startFakeNode(t, { tools: [probe], features: ['assistant_pids', 'prefer_running'] })
    node.resolve = () => ({ allow: true })
    const privateEnv = { ...env, OPENCODE_CONFIG_CONTENT: JSON.stringify({ ...JSON.parse(env.OPENCODE_CONFIG_CONTENT), server: { port: Number(new URL(model.url).port), hostname: '0.0.0.0', mdns: true } }) }
    const firstTokens = new Map()
    const companion = createOpencodeCompanion({ home, bin, env: privateEnv, socketPath: node.path, retryMs: 20, pushDelayMs: 5,
      assistantAcpFactory: (options) => {
        const client = new OpencodeAcpClient(options)
        client.on('update', (params) => {
          if (params.update?.sessionUpdate === 'agent_message_chunk' && firstTokens.get(params.sessionId) === null) {
            firstTokens.set(params.sessionId, performance.now())
          }
        })
        return client
      },
      log: (line) => logs.push(line), openDb: async () => ({ all: () => [], close() {} }),
      discoverModels: async () => [{ providerID: 'test', id: 'test-model', name: 'Test Model' }],
    })
    t.after(() => companion.stop())
    await companion.start()
    await node.ready
    const start = async () => (await node.peer.call('session.start', { cwd, assistant: true })).session.session_id
    const a = await start(), b = await start()
    const pids = node.last('runtime.assistant_pids').pids
    assert.notEqual(pids[a], pids[b])
    assert.equal(node.hello.caps.supervision_policy, true)
    const prompt = async (id, text) => {
      await node.peer.call('session.send', { session_id: id, text })
      await until('real worker turn settled', () => !companion.live.get(id).running && !companion.live.get(id).queued, 30_000)
      assert.equal(companion.live.get(id).turnError, '')
    }
    await Promise.all([prompt(a, 'PROBE_IDENTITY isolated A'), prompt(b, 'PROBE_IDENTITY isolated B')])
    assert.deepEqual(node.toolCalls.map((call) => call.session_id).sort(), [a, b].sort())

    // T12 observations never set a performance threshold. Five process-cold
    // samples and five warm turns; OS page-cache state is uncontrolled.
    const cold = [], warm = [], rss = []
    const firstToken = async (id, began) => {
      firstTokens.set(id, null)
      await prompt(id, 'PERFORMANCE_TEXT')
      const timestamp = firstTokens.get(id)
      assert.equal(typeof timestamp, 'number', 'the engine must produce a first token')
      firstTokens.delete(id)
      return timestamp - began
    }
    for (let sample = 0; sample < 5; sample++) {
      const id = await start()
      await companion.assistantWorkers.discard(companion.live.get(id).worker)
      // This send starts a new process, initializes, then session/resume's the
      // existing native conversation. It does not time session/new instead.
      cold.push(await firstToken(id, performance.now()))
      warm.push(await firstToken(id, performance.now()))
      const pid = companion.live.get(id).worker.acp.pid
      rss.push(Number(readFileSync(`/proc/${pid}/status`, 'utf8').match(/^VmRSS:\s+(\d+)/m)?.[1]))
      assert.ok(Number.isFinite(rss.at(-1)) && rss.at(-1) > 0, 'RSS must be a valid observation')
      await node.peer.call('session.close', { session_id: id })
    }
    const quantile = (list, q) => [...list].sort((a, b) => a - b)[Math.ceil(list.length * q) - 1]
    t.diagnostic(JSON.stringify({ version: '1.19.6', samples: 5, cache: 'uncontrolled OS cache; fresh worker versus existing worker',
      cold_ms: { p50: quantile(cold, .5), p95: quantile(cold, .95) }, warm_ms: { p50: quantile(warm, .5), p95: quantile(warm, .95) }, rss_kib: rss }))
    assert.deepEqual(model.requests.filter((r) => r.error), [], 'fake model accepts all native request shapes')
  } catch (error) {
    t.diagnostic(logs.slice(-20).join('\n'))
    throw error
  }
})
