#!/usr/bin/env node
// Standalone ACP companion runtime for opencode (docs/integrations/opencode.md).
// Connects opencode to ~/.persona/runtime.sock without requiring a resident `opencode serve` process,
// supporting both new Agent sessions and Persona Assistant conversations via `opencode acp`.
//
//   node integrations/opencode/companion.mjs [--socket <runtime.sock>] [--bin <path>]
//     [--db <opencode.db>] [--config-dir <dir>] [--launcher <name>] [--name <label>]
//     [--model <provider/model>] [--instance <id>] [--no-assistant] [--no-update] [--parent-stdin]
//     [--assistant-workers <1..64>] [--assistant-idle-ms <milliseconds>]

import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { parseArgs } from 'node:util'
import { createOpencodeCompanion } from './lib/companion.js'

export function parseCompanionArgs(argv = process.argv.slice(2), env = process.env) {
  const { values } = parseArgs({
    args: argv,
    options: {
      socket: { type: 'string' },
      bin: { type: 'string' },
      'opencode-bin': { type: 'string' },
      db: { type: 'string' },
      'config-dir': { type: 'string' },
      launcher: { type: 'string' },
      name: { type: 'string' },
      'display-name': { type: 'string' },
      model: { type: 'string' },
      runtime: { type: 'string' },
      instance: { type: 'string' },
      'no-assistant': { type: 'boolean' },
      'no-update': { type: 'boolean' },
      'assistant-workers': { type: 'string' },
      'assistant-idle-ms': { type: 'string' },
      'parent-stdin': { type: 'boolean' },
    },
    strict: true,
    allowPositionals: false,
  })
  const bin = values.bin || values['opencode-bin'] || env.PERSONA_OPENCODE_BIN || env.OPENCODE_BIN || 'opencode'
  const label = values.name || values['display-name'] || undefined
  const displayName = values['display-name'] || values.name || undefined
  const maxAssistantWorkers = values['assistant-workers'] === undefined ? undefined : Number(values['assistant-workers'])
  const assistantIdleMs = values['assistant-idle-ms'] === undefined ? undefined : Number(values['assistant-idle-ms'])
  if (maxAssistantWorkers !== undefined && (!Number.isInteger(maxAssistantWorkers) || maxAssistantWorkers < 1 || maxAssistantWorkers > 64)) throw new Error('--assistant-workers must be an integer between 1 and 64')
  if (assistantIdleMs !== undefined && (!Number.isFinite(assistantIdleMs) || assistantIdleMs < 1)) throw new Error('--assistant-idle-ms must be positive')
  return {
    ...(maxAssistantWorkers === undefined ? {} : { maxAssistantWorkers }),
    ...(assistantIdleMs === undefined ? {} : { assistantIdleMs }),
    socketPath: values.socket || undefined,
    bin,
    dbPath: values.db || undefined,
    configDir: values['config-dir'] || undefined,
    launcher: values.launcher,
    name: label,
    displayName,
    model: values.model,
    runtime: values.runtime,
    instance: values.instance,
    assistant: values['no-assistant'] ? false : true,
    noUpdate: values['no-update'] === true,
    parentStdin: values['parent-stdin'] === true,
  }
}

function main() {
  const opts = parseCompanionArgs()
  const log = (msg) => process.stderr.write(`persona-opencode: ${msg}\n`)
  const companion = createOpencodeCompanion({
    ...opts,
    log,
  })

  void companion.start()
  const alive = setInterval(() => {}, 1 << 30)
  log('started opencode ACP companion for Persona')

  let stopping = false
  function shutdown() {
    if (stopping) return
    stopping = true
    clearInterval(alive)
    void companion.stop().finally(() => process.exit(0))
  }

  for (const sig of ['SIGTERM', 'SIGINT']) {
    process.on(sig, shutdown)
  }

  if (opts.parentStdin && !process.stdin.isTTY) {
    process.stdin.on('end', shutdown)
    process.stdin.on('close', shutdown)
    process.stdin.on('error', shutdown)
    process.stdin.resume()
    if (process.stdin.readableEnded || process.stdin.destroyed) {
      shutdown()
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main()
}
