import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createInterface } from 'node:readline'
import { describe, it } from 'node:test'
import { fileURLToPath } from 'node:url'
import { mcpServers } from '../lib/compat.js'

const PROXY = fileURLToPath(new URL('../mcp-proxy.mjs', import.meta.url))
const FAKE = fileURLToPath(new URL('./fake-mcp-server.mjs', import.meta.url))

/** The proxy in front of the fake server, with a call(method, params) that resolves to the response. */
function proxied(t, tools) {
  const args = []
  if (tools) {
    const dir = mkdtempSync(join(tmpdir(), 'persona-mcp-proxy-'))
    t.after(() => rmSync(dir, { recursive: true, force: true }))
    args.push(join(dir, 'tools.json'))
    writeFileSync(args[0], JSON.stringify(tools))
  }
  const proc = spawn(process.execPath, [PROXY, process.execPath, FAKE, ...args], { stdio: ['pipe', 'pipe', 'inherit'] })
  t.after(() => proc.kill())
  const waiting = new Map()
  createInterface({ input: proc.stdout }).on('line', (line) => {
    const msg = JSON.parse(line)
    const resolve = waiting.get(msg.id)
    waiting.delete(msg.id)
    resolve?.(msg)
  })
  let id = 0
  const call = (method, params, requestID = ++id) => new Promise((resolve) => {
    waiting.set(requestID, resolve)
    proc.stdin.write(JSON.stringify({ jsonrpc: '2.0', id: requestID, method, params }) + '\n')
  })
  return { proc, call }
}

describe('MCP proxy (#7333)', { timeout: 15_000 }, () => {
  it('adds persona_call to the server\'s tools', async (t) => {
    const big = { name: 'kb_search', description: 'Search the knowledge base. '.repeat(20), inputSchema: { type: 'object', properties: {} } }
    const { call } = proxied(t, [big, { name: 'agents_list', description: 'List the Agents', inputSchema: { type: 'object', properties: {} } }])
    const res = await call('tools/list', {})
    assert.deepEqual(res.result.tools.map((x) => x.name), ['kb_search', 'persona_call'])
  })

  it('weighs a tool by what agy declares of it, not its output schema or annotations (#10756)', async (t) => {
    const padded = {
      name: 'mail_accounts', description: 'List mail accounts', inputSchema: { type: 'object', properties: { id: { type: 'string' } }, required: ['id'] },
      outputSchema: { type: 'object', description: 'x'.repeat(2000) }, annotations: { title: 'y'.repeat(500) },
    }
    const { call } = proxied(t, [padded])
    const res = await call('tools/list', {})
    assert.deepEqual(res.result.tools.map((x) => x.name), ['persona_call'])
    const listed = await call('tools/call', { name: 'persona_call', arguments: {} })
    assert.match(listed.result.content[0].text, /- mail_accounts: List mail accounts Arguments: \{"type":"object","properties":\{"id":\{"type":"string"\}\},"required":\["id"\]\}/)
  })

  it('lists a left-out tool with its whole input schema, references and alternatives included (#10756)', async (t) => {
    const schema = {
      type: 'object',
      $defs: { when: { type: 'string', enum: ['now', 'later'] } },
      properties: { at: { $ref: '#/$defs/when' }, target: { anyOf: [{ type: 'string' }, { type: 'integer' }] } },
      additionalProperties: false,
    }
    const { call } = proxied(t, [{ name: 'cancel_followup', description: 'Cancel', inputSchema: schema }])
    await call('tools/list', {})
    const listed = await call('tools/call', { name: 'persona_call', arguments: {} })
    const line = listed.result.content[0].text.split('\n').find((l) => l.startsWith('- cancel_followup:'))
    assert.deepEqual(JSON.parse(line.slice(line.indexOf('Arguments: ') + 'Arguments: '.length)), schema)
  })

  it('declares no tool smaller than persona_call, so agy never defers it (#10756)', async (t) => {
    const small = Array.from({ length: 40 }, (_, i) => ({ name: `connector_read_${i}`, description: 'Read one thing', inputSchema: { type: 'object', properties: {} } }))
    const big = { name: 'todo_create', description: 'Add a to-do. '.repeat(40), inputSchema: { type: 'object', properties: { title: { type: 'string' } } } }
    const { call } = proxied(t, [...small, big])
    const res = await call('tools/list', {})
    const size = (tool) => Buffer.byteLength(JSON.stringify(tool))
    const declared = res.result.tools
    const dispatch = declared.find((x) => x.name === 'persona_call')
    assert.deepEqual(declared.map((x) => x.name), ['todo_create', 'persona_call'])
    assert.ok(declared.every((x) => x === dispatch || size(x) > size(dispatch)), 'persona_call is the smallest declared tool')
    // The tools left out are still listed, with their arguments, and callable through it.
    const listed = await call('tools/call', { name: 'persona_call', arguments: {} })
    assert.match(listed.result.content[0].text, /connector_read_0, .*connector_read_39, todo_create/)
    assert.match(listed.result.content[0].text, /- connector_read_7: Read one thing Arguments: \{"type":"object","properties":\{\}\}/)
    assert.doesNotMatch(listed.result.content[0].text, /- todo_create:/)
    const via = await call('tools/call', { name: 'persona_call', arguments: { name: 'connector_read_7', arguments: {} } })
    assert.deepEqual(JSON.parse(via.result.content[0].text), { name: 'connector_read_7', args: {} })
  })

  it('sends persona_call on as a call of the named tool, and other calls unchanged', async (t) => {
    const { call } = proxied(t)
    await call('tools/list', {})
    const via = await call('tools/call', { name: 'persona_call', arguments: { name: 'todo_create', arguments: { title: 'x' } } })
    assert.deepEqual(JSON.parse(via.result.content[0].text), { name: 'todo_create', args: { title: 'x' } })
    const bare = await call('tools/call', { name: 'agents_list', arguments: {} })
    assert.deepEqual(JSON.parse(bare.result.content[0].text), { name: 'agents_list', args: {} })
    const noArgs = await call('tools/call', { name: 'persona_call', arguments: { name: 'agents_list' } })
    assert.deepEqual(JSON.parse(noArgs.result.content[0].text), { name: 'agents_list', args: {} })
  })

  it('lists the tools without a name and leaves an unknown one to the server', async (t) => {
    const { call } = proxied(t)
    await call('tools/list', {})
    const list = await call('tools/call', { name: 'persona_call', arguments: {} })
    assert.match(list.result.content[0].text, /todo_create, agents_list/)
    const unknown = await call('tools/call', { name: 'persona_call', arguments: { name: 'nope' } })
    assert.match(unknown.error.message, /unknown tool nope/)
  })

  it('ends with its server when agy closes stdin', async (t) => {
    const { proc } = proxied(t)
    proc.stdin.end()
    const [code] = await once(proc, 'exit')
    assert.equal(code, 0)
  })
})

describe('command gate tools', { timeout: 15_000 }, () => {
  // What the node adds for a runtime's assistant chat (server/internal/node/assistant_runtime.go gateTools,
  // gateToolSchema): the assistant's instructions name it, so agy must see it declared, its schema as the node gives it.
  const gateSchema = (command) => ({
    type: 'object', required: ['args'],
    properties: { args: { type: 'array', items: { type: 'string' }, description: `The words after ${command}: METHOD PATH [JSON body] and its flags, one per item` } },
  })
  const chatAPI = {
    name: 'chat_api',
    description: 'Run the `ex` helper of the chat skill: `args` are the words after `ex`, e.g. ["GET", "/me"], ["GET", "/conversations?scope=group&limit=100"], ["POST", "/conversations/FEED_ID/messages", "{\\"text\\":\\"hi\\"}"]. Reads run at once; writes other people see are confirmed on the phone first, and a refusal comes back as the result. Returns what ex prints.',
    inputSchema: gateSchema('ex'),
  }

  it('declares it with its schema unchanged and passes its calls on by name', async (t) => {
    const { call } = proxied(t, [chatAPI, { name: 'agents_list', description: 'List the Agents', inputSchema: { type: 'object', properties: {} } }])
    const listed = await call('tools/list', {})
    assert.deepEqual(listed.result.tools.map((x) => x.name), ['chat_api', 'persona_call'])
    assert.deepEqual(listed.result.tools[0], chatAPI)
    const args = { args: ['GET', '/me'] }
    for (const params of [{ name: 'chat_api', arguments: args }, { name: 'persona_call', arguments: { name: 'chat_api', arguments: args } }]) {
      const res = await call('tools/call', params)
      assert.deepEqual(JSON.parse(res.result.content[0].text), { name: 'chat_api', args })
    }
  })

  it('keeps one with a short description reachable through persona_call, with its arguments', async (t) => {
    const short = { name: 'ex_api', description: 'Run ex.', inputSchema: gateSchema('ex') }
    const { call } = proxied(t, [short])
    const listed = await call('tools/list', {})
    assert.deepEqual(listed.result.tools.map((x) => x.name), ['persona_call'])
    const text = (await call('tools/call', { name: 'persona_call', arguments: {} })).result.content[0].text
    assert.ok(text.includes(`- ex_api: Run ex. Arguments: ${JSON.stringify(short.inputSchema)}`), text)
  })
})

describe('MCP declaration enums (#9944)', { timeout: 15_000 }, () => {
  // The template has language branches and is not JSON until the server renders it. Use its recorded MCP wire form.
  const rendered = JSON.parse(readFileSync(new URL('../../../server/internal/hub/testdata/prompts/mcp_tools/assistant/task_review.golden', import.meta.url), 'utf8'))
  const taskTool = { name: rendered.name, description: rendered.description, inputSchema: rendered.input_schema }
  const catalogTool = { name: 'tool_catalog', inputSchema: { type: 'object' } }

  it('projects the real task_review schema without its boolean enum and keeps boolean call arguments', async (t) => {
    assert.deepEqual(taskTool.inputSchema.properties.evidence.properties.ready.enum, [true])
    const { call } = proxied(t, [taskTool])
    const listed = await call('tools/list', {})
    const expected = structuredClone(taskTool)
    delete expected.inputSchema.properties.evidence.properties.ready.enum
    assert.deepEqual(listed.result.tools, [expected, listed.result.tools.at(-1)])
    assert.equal(listed.result.tools.at(-1).name, 'persona_call')
    for (const ready of [true, false]) {
      const args = { todo_id: 'todo_fixture', verdict: 'parked', evidence: { kind: 'merge_review', ready } }
      for (const params of [
        { name: 'task_review', arguments: args },
        { name: 'persona_call', arguments: { name: 'task_review', arguments: args } },
      ]) {
        const result = await call('tools/call', params)
        assert.deepEqual(JSON.parse(result.result.content[0].text), { name: 'task_review', args })
      }
    }
  })

  it('changes only enum keywords in schema positions, including properties named enum', async (t) => {
    const data = { enum: [true], properties: { enum: { enum: [1] } } }
    const schema = {
      type: 'object', required: ['enum'], default: data, examples: [data], const: data, 'x-metadata': data,
      properties: {
        enum: { type: 'boolean', enum: [true], default: true, examples: [true], const: true },
        numeric: { type: 'number', enum: [1, 2], minimum: 1 },
        mixed: { enum: ['one', true] }, empty: { enum: [] }, emptyString: { enum: ['valid', ''] },
        valid: { type: 'string', enum: ['accepted', 'false', ' '] },
        allowed: true, blocked: false, reference: { $ref: '#/$defs/enum' },
        tuple: { items: [{ enum: [0] }, { enum: ['ok'] }], additionalItems: { enum: [false] } },
        array: { items: { enum: [1] }, contains: { enum: [true] }, prefixItems: [{ enum: [false] }],
          unevaluatedItems: { enum: [0] } },
        union: { allOf: [{ enum: [true] }], anyOf: [{ enum: [1] }], oneOf: [{ enum: [false] }],
          not: { enum: [false] }, if: { enum: [true] }, then: { enum: [1] }, else: { enum: [0] } },
      },
      patternProperties: { '^enum$': { enum: [1] } }, additionalProperties: { enum: [true] },
      propertyNames: { enum: ['', 'name'] }, $defs: { enum: { enum: [1] } }, definitions: { enum: { enum: [true] } },
      dependencies: { enum: { enum: [true] }, requiredDependency: ['enum'] },
      dependentSchemas: { enum: { enum: [true] } }, unevaluatedProperties: { enum: [false] }, contentSchema: { enum: [1] },
    }
    const expected = structuredClone(schema)
    for (const name of ['enum', 'numeric', 'mixed', 'empty', 'emptyString']) delete expected.properties[name].enum
    delete expected.properties.tuple.items[0].enum
    delete expected.properties.tuple.additionalItems.enum
    for (const name of ['items', 'contains', 'unevaluatedItems']) delete expected.properties.array[name].enum
    delete expected.properties.array.prefixItems[0].enum
    for (const name of ['allOf', 'anyOf', 'oneOf']) delete expected.properties.union[name][0].enum
    for (const name of ['not', 'if', 'then', 'else']) delete expected.properties.union[name].enum
    delete expected.patternProperties['^enum$'].enum
    for (const name of ['additionalProperties', 'propertyNames', 'unevaluatedProperties', 'contentSchema']) delete expected[name].enum
    for (const name of ['$defs', 'definitions', 'dependencies', 'dependentSchemas']) delete expected[name].enum.enum
    const tool = { name: 'nested', inputSchema: schema, annotations: data, outputSchema: schema, _meta: data }
    const { call } = proxied(t, [tool])
    const result = await call('tools/list', {})
    assert.deepEqual(result.result.tools[0], { ...tool, inputSchema: expected })
  })

  it('correlates list responses by id and leaves tool results and ordinary messages unchanged', async (t) => {
    const tools = [taskTool, catalogTool]
    const { call } = proxied(t, tools)
    const pending = call('tools/list', { hold: true }, 'list')
    const catalog = await call('tools/call', { name: 'tool_catalog', arguments: {} }, 0)
    assert.deepEqual(catalog.result, { tools })
    const listed = await pending
    assert.equal(listed.result.tools[0].inputSchema.properties.evidence.properties.ready.enum, undefined)
    assert.deepEqual((await call('test/echo', { tools }, 'list')).result, { tools })
    assert.ok((await call('tools/list', { fail: true }, 0)).error)
    assert.deepEqual((await call('test/echo', { tools }, 0)).result, { tools })
  })
})

describe('mcpServers', () => {
  it('allows a regular agent to keep ordinary tools alongside a separate scoped A2A server', () => {
    const agent = mcpServers('/bin/personad', 'agy-1', false, 'test-ticket')
    const chat = mcpServers('/bin/personad', 'agy-2', true, 'test-ticket')
    assert.deepEqual(agent.map(({ name }) => name), ['persona', 'persona_a2a'])
    assert.ok(agent[0].env.every(({ name }) => name !== 'PERSONA_A2A_TOKEN'))
    assert.ok(agent[0].env.some(({ name, value }) => name === 'PERSONA_MCP_EXCLUDE_A2A' && value === '1'))
    assert.ok(agent[1].env.some(({ name, value }) => name === 'PERSONA_A2A_TOKEN' && value === 'test-ticket'))
    assert.ok(chat[0].env.every(({ name }) => name !== 'PERSONA_A2A_TOKEN'))
  })
  it('passes one persona server; an assistant chat\'s through the proxy', () => {
    const agent = mcpServers('/bin/personad', 'agy-1', false)
    assert.deepEqual(agent.map((s) => [s.name, s.command, s.args]), [['persona', '/bin/personad', ['mcp']]])
    const chat = mcpServers('/bin/personad', 'agy-1', true)
    assert.deepEqual(chat.map((s) => [s.name, s.command, s.args]), [['persona', process.execPath, [PROXY, '/bin/personad', 'mcp']]])
    assert.ok(chat[0].env.some((e) => e.name === 'PERSONA_ASSISTANT' && e.value === '1'))
    assert.ok(chat[0].env.some((e) => e.name === 'PERSONA_SESSION_ID' && e.value === 'agy-1'))
  })
})


describe('MCP proxy process cleanup', { timeout: 15_000 }, () => {
  async function fixture(t, mode = '') {
    const dir = mkdtempSync(join(tmpdir(), 'persona-mcp-lifecycle-'))
    const events = join(dir, 'events')
    const source = `
      const fs = require('node:fs');
      const [events, mode] = process.argv.slice(1);
      for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => {
        fs.appendFileSync(events, signal + '\\n');
        process.exit(0);
      });
      setInterval(() => {}, 1000);
      process.stdin.on('data', () => process.stdout.write(JSON.stringify({ result: 'response' }) + '\\n'));
      if (mode === 'close-input') {
        process.stdin.once('close', () => {
          fs.closeSync(0);
          process.stdout.write(JSON.stringify({ result: { pid: process.pid } }) + '\\n');
        });
        process.stdin.destroy();
      } else process.stdout.write(JSON.stringify({ result: { pid: process.pid } }) + '\\n');
    `
    const proc = spawn(process.execPath, [PROXY, process.execPath, '-e', source, events, mode], { stdio: ['pipe', 'pipe', 'pipe'] })
    let childPid
    let stderr = ''
    proc.stderr.on('data', (chunk) => { stderr += chunk })
    proc.stdin.on('error', () => {})
    t.after(() => {
      proc.kill('SIGKILL')
      if (childPid) { try { process.kill(childPid, 'SIGKILL') } catch {} }
      rmSync(dir, { recursive: true, force: true })
    })
    const [ready] = await once(proc.stdout, 'data')
    childPid = JSON.parse(String(ready)).result.pid
    return { proc, childPid, events, stderr: () => stderr }
  }

  function exited(proc) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('proxy did not exit')), 2000)
      proc.once('exit', (code, signal) => { clearTimeout(timer); resolve({ code, signal }) })
    })
  }

  function assertGone(pid) {
    assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' })
  }

  for (const signal of ['SIGTERM', 'SIGINT']) {
    it(`forwards ${signal} and waits for the child to exit`, async (t) => {
      const { proc, childPid, events } = await fixture(t)
      const done = exited(proc)
      proc.kill(signal)
      await done
      assert.equal(existsSync(events) && readFileSync(events, 'utf8'), signal + '\n')
      assertGone(childPid)
    })
  }

  it('ends a server that stays alive after stdin closes', async (t) => {
    const { proc, childPid } = await fixture(t)
    const done = exited(proc)
    proc.stdin.end()
    await done
    assertGone(childPid)
  })

  it('handles a broken child input without an uncaught EPIPE or orphan', async (t) => {
    const { proc, childPid, stderr } = await fixture(t, 'close-input')
    const done = exited(proc)
    proc.stdin.write('request\n')
    await done
    assert.doesNotMatch(stderr(), /Unhandled 'error' event|throw er;/)
    assertGone(childPid)
  })

  it('cleans up when its output consumer closes the pipe', async (t) => {
    const { proc, childPid, stderr } = await fixture(t)
    const done = exited(proc)
    proc.stdout.destroy()
    proc.stdin.write('request\n')
    await done
    assert.doesNotMatch(stderr(), /Unhandled 'error' event|throw er;/)
    assertGone(childPid)
  })
})
