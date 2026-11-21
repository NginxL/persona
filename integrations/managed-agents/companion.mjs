#!/usr/bin/env node
// Managed Agents sessions in Persona's Agents (docs/integrations/managed-agents.md). Runs next to the node, as the same
// user, and connects to its runtime.sock; with --listen it runs as a daemon the node dials (daemons.json).
//
//   node integrations/managed-agents/companion.mjs [--config <file>] [--socket <path>]
//   PERSONA_DAEMON_TOKEN=<token> node integrations/managed-agents/companion.mjs --listen <port> [--host <addr>]
//   node integrations/managed-agents/companion.mjs --check      (reads the config, lists each profile's sessions once)

import { parseArgs } from 'node:util'
import { loadConfig, DEFAULT_CONFIG } from './lib/config.js'
import { createClient } from './lib/client.js'
import { createSecrets } from './lib/secrets.js'
import { createManagedAgentsRuntime } from './lib/runtime.js'

const { values } = parseArgs({ options: {
  config: { type: 'string' }, socket: { type: 'string' }, listen: { type: 'string' }, host: { type: 'string' }, check: { type: 'boolean' },
} })
const log = (m) => process.stderr.write(`persona-managed-agents: ${m}\n`)
const configPath = values.config ?? DEFAULT_CONFIG

if (values.check) {
  // A read-only check of each profile: the key resolves and the server answers. Prints counts, never keys or content.
  const secrets = createSecrets({ log })
  const config = await loadConfig(configPath, { log })
  for (const p of config.problems) log(`config: ${p}`)
  let failed = config.problems.length > 0
  for (const p of config.profiles) {
    const client = createClient(p, secrets)
    try {
      const list = await client.listSessions({ limit: 5 })
      log(`${p.id} (${p.mode}, ${p.base}${p.prefix}): ok, ${list.length} recent session(s)`)
    } catch (err) {
      failed = true
      log(`${p.id} (${p.mode}, ${p.base}${p.prefix}): ${err.message}`)
    }
  }
  process.exit(failed ? 1 : 0)
}

const runtimeOptions = { log, answerFirst: 'phone' }
if (values.socket) runtimeOptions.socketPath = values.socket
if (values.listen) {
  const token = process.env.PERSONA_DAEMON_TOKEN
  if (!token) {
    log('set PERSONA_DAEMON_TOKEN (the token daemons.json gives for this daemon)')
    process.exit(2)
  }
  runtimeOptions.listen = { port: Number(values.listen), host: values.host ?? '127.0.0.1', token }
}
const ma = createManagedAgentsRuntime({ configPath, log, runtimeOptions })
const addr = await ma.start()
log(addr ? `daemon on ws://${addr.address}:${addr.port}/persona/runtime` : 'waiting for the personad node')
const alive = setInterval(() => {}, 1 << 30)
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => {
  clearInterval(alive)
  void ma.stop().finally(() => process.exit(0))
})
