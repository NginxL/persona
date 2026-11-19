#!/usr/bin/env node
// Antigravity (agy) companion runtime for Persona (docs/integrations/runtimes.md "Antigravity CLI").
// Connects to ~/.persona/runtime.sock, exposes agy sessions & ACP runtime for both Agents tab and Persona Assistant.
//
// Updates (docs/integrations/runtimes.md "Auto-update"): run from a store (<store>/current/antigravity/companion.mjs,
// as launchd / systemd keep it), it installs the version the node carries as current and exits once idle; its
// supervisor starts it again on the new code (KeepAlive / Restart=always). --no-update: never.

import { parseArgs } from 'node:util'
import { selfPackage } from '../runtime-sdk/index.js'
import { createAgyRuntime } from './lib/runtime.js'

const { values } = parseArgs({
  options: {
    socket: { type: 'string' },
    'agy-dir': { type: 'string' },
    'agy-bin': { type: 'string' },
    instance: { type: 'string' },
    'parent-stdin': { type: 'boolean' },
  },
  strict: false,
})

const self = selfPackage(import.meta.url, 'persona-antigravity-runtime')
const log = (msg) => process.stderr.write(`persona-agy: ${msg}\n`)
let alive
const runtime = createAgyRuntime({
  socketPath: values.socket || undefined,
  agyDir: values['agy-dir'] || undefined,
  agyBin: values['agy-bin'] || undefined,
  instance: values.instance || undefined,
  log,
  update: self.store && !process.argv.includes('--no-update') ? {
    name: self.name,
    hash: self.hash,
    store: self.store,
    exit: (hash) => {
      log(`${hash.slice(0, 12)} installed; exiting for the supervisor to start it`)
      clearInterval(alive)
      runtime.stop().finally(() => process.exit(0))
    },
  } : undefined,
})

runtime.start()
alive = setInterval(() => {}, 1 << 30)
log('started Antigravity (agy) runtime for Persona')

let stopping = false
function stopCompanion() {
  if (stopping) return
  stopping = true
  clearInterval(alive)
  // Exit once agy is gone (killed after a short grace when it ignores SIGTERM), so it does not outlive the companion.
  runtime.stop().finally(() => process.exit(0))
}

for (const sig of ['SIGTERM', 'SIGINT']) {
  process.on(sig, stopCompanion)
}

if (values['parent-stdin'] && !process.stdin.isTTY) {
  process.stdin.on('end', stopCompanion)
  process.stdin.on('error', stopCompanion)
  process.stdin.resume()
}
