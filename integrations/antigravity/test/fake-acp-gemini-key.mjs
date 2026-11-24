// A fake `agy` for the Gemini API key provider: records how it was started and what ACP asked of it.
import { appendFileSync } from 'node:fs'
import { createInterface } from 'node:readline'

const directory = process.argv[2]
const record = (value) => appendFileSync(`${directory}/calls.jsonl`, `${JSON.stringify(value)}\n`)
record({ argv: process.argv.slice(3), env_key_set: Boolean(process.env.GEMINI_API_KEY) })
if (process.argv.includes('models')) {
  // Only with the provider setting does agy list anything without a sign-in; it prints its whole built-in catalog.
  if (!process.argv.join(' ').includes('"modelProvider":"gemini"')) {
    console.error('Please sign in to view available models.')
    process.exit(1)
  }
  console.error('Fetching available models...')
  for (const id of ['gemini-4-neon-high', 'gemini-4-neon-low', 'gemini-3.8-flash-medium', 'built-in-only-high']) console.log(`${id}\t${id.toUpperCase()}`)
  process.exit(0)
}
const send = (value) => process.stdout.write(`${JSON.stringify(value)}\n`)
createInterface({ input: process.stdin }).on('line', (line) => {
  const msg = JSON.parse(line)
  record({ method: msg.method, params: msg.params })
  if (msg.method === 'initialize') {
    send({ jsonrpc: '2.0', id: msg.id, result: { protocolVersion: 1, agentInfo: { name: 'fake', version: '1.0.0' }, authMethods: [{ id: 'oauth-personal' }, { id: 'gemini-api-key' }] } })
  } else if (msg.method === 'authenticate') {
    send({ jsonrpc: '2.0', id: msg.id, result: {} })
  } else if (msg.id !== undefined) {
    send({ jsonrpc: '2.0', id: msg.id, result: {} })
  }
})
