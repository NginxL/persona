#!/usr/bin/env node
// Usage: node mcp-proxy.mjs <command> [args...]
//
// Stdio MCP proxy for an assistant chat's `personad mcp` (lib/compat.js mcpServers). agy defers the MCP tools over its
// declaration budget, largest schemas first, and for servers passed through ACP it never declares `call_mcp_tool`, so
// a deferred Persona tool cannot be called at all (#7333). This adds one small tool, persona_call, that calls any
// Persona tool by name, and declares only the tools larger than it: the smallest declared, it is never the one deferred,
// however many small tools connectors add (#10756); the rest stay callable through it. Tool declarations also omit
// enums agy cannot represent.
// shortcut: works around agy 1.5.8; drop it once agy declares call_mcp_tool for ACP servers.
import { spawn } from 'node:child_process'
import { createInterface } from 'node:readline'
import { agyInputSchema } from './lib/mcp-schema.js'

const DISPATCH = 'persona_call'
const DISPATCH_TOOL = {
  name: DISPATCH,
  description: 'Call any Persona tool not in your list, by name; for a deferred one, read its schema file for the ' +
    'arguments. With no name it lists the tools, and the arguments of those that have no schema file.',
  inputSchema: {
    type: 'object',
    properties: {
      name: { type: 'string', description: 'e.g. todo_create' },
      arguments: { type: 'object', description: 'That tool\'s own arguments', additionalProperties: true },
    },
  },
}
// What agy declares of a tool (its name, description and parameters), in bytes: what its largest-first deferral weighs.
const size = (tool) => Buffer.byteLength(JSON.stringify({ name: tool.name, description: tool.description, inputSchema: tool.inputSchema }))
const DISPATCH_SIZE = size(DISPATCH_TOOL)

const [command, ...args] = process.argv.slice(2)
const child = spawn(command, args, { stdio: ['pipe', 'pipe', 'inherit'] })
child.on('error', (err) => {
  process.stderr.write(`persona mcp proxy: ${err.message}\n`)
  process.exit(1)
})
child.on('exit', (code, signal) => process.exit(code ?? (signal ? 1 : 0)))
process.stdin.on('end', () => child.stdin.end())

let names = null // the server's tools, from its last tools/list
let hidden = [] // those not declared to agy (smaller than persona_call), with their arguments, for the no-name listing
const pendingLists = new Set()
const send = (msg) => process.stdout.write(JSON.stringify(msg) + '\n')

createInterface({ input: process.stdin, crlfDelay: Infinity }).on('line', (line) => {
  const msg = parse(line)
  if (msg?.method && Object.hasOwn(msg, 'id')) {
    if (msg.method === 'tools/list') pendingLists.add(msg.id)
    else pendingLists.delete(msg.id)
  }
  if (msg?.method === 'tools/call' && msg.params?.name === DISPATCH) {
    const { name, arguments: input } = msg.params.arguments || {}
    if (!name) { // list them; the server itself rejects a name it does not have
      let text = `The Persona tools: ${(names || []).join(', ')}.`
      if (hidden.length) text += `\n\nNot in your tool list (call them through ${DISPATCH}):\n` + hidden.join('\n')
      send({ jsonrpc: '2.0', id: msg.id, result: { content: [{ type: 'text', text }] } })
      return
    }
    line = JSON.stringify({ ...msg, params: { ...msg.params, name, arguments: input || {} } })
  }
  child.stdin.write(line + '\n')
})

createInterface({ input: child.stdout, crlfDelay: Infinity }).on('line', (line) => {
  const msg = parse(line)
  if (msg && !Object.hasOwn(msg, 'method') && Object.hasOwn(msg, 'id')
    && (Object.hasOwn(msg, 'result') || Object.hasOwn(msg, 'error')) && pendingLists.delete(msg.id)) {
    const tools = msg.result?.tools
    if (Array.isArray(tools)) {
      names = tools.map((t) => t.name)
      const all = tools.map((tool) => ({ ...tool, inputSchema: agyInputSchema(tool.inputSchema) }))
      const declarations = all.filter((tool) => size(tool) > DISPATCH_SIZE) // persona_call stays the smallest (#10756)
      // Each with its whole input schema as the server gave it ($defs, $ref, anyOf, enums agy cannot represent): this
      // listing is the only place agy reads their arguments from.
      hidden = tools.filter((_, i) => size(all[i]) <= DISPATCH_SIZE)
        .map((tool) => `- ${tool.name}: ${tool.description || ''} Arguments: ${JSON.stringify(tool.inputSchema ?? {})}`)
      line = JSON.stringify({ ...msg, result: { ...msg.result, tools: [...declarations, DISPATCH_TOOL] } })
    }
  }
  process.stdout.write(line + '\n')
})

function parse(line) {
  try {
    return JSON.parse(line)
  } catch {
    return null
  }
}
