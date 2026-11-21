// Persona runtime for opencode (docs/integrations/opencode.md): an opencode plugin that puts the sessions of this opencode server
// process into the Persona apps through the persona node on this machine (~/.persona/runtime.sock, docs/integrations/runtimes.md).
// It does nothing visible until a node listens. opencode loads a plugin once per project directory in the server
// process (the shared `opencode serve` every TUI attaches to), so the host is one per process and each location only
// hands it its context.
//
// Configuration (optional): <opencode config dir>/persona.json — { "answerFirst": "auto", "listDays": 7,
// "maxSessions": 80, "socket": "<runtime.sock>", "launcher": "opencode", "assistant": true, "model": "provider/model" }.
// PERSONA_OPENCODE_DB points at opencode.db when it is not in the default place ($XDG_DATA_HOME/opencode).
//
// Installed by a one-line file in the opencode config dir's plugins/ that re-exports this one (install.mjs).
// For opencode 1.x CLI setups without a resident v2 server plugin, run `node integrations/opencode/companion.mjs` instead.

import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { envValue } from '../runtime-sdk/lib/identity.js'
import { defaultDbPath, openReadOnly } from './lib/db.js'
import { readConfiguredModel } from './lib/models.js'
import { createOpencodeHost } from './lib/opencode.js'
import { registerA2A } from './lib/a2a.js'

function configDir(env = process.env) {
  return join(env.XDG_CONFIG_HOME || join(homedir(), '.config'), 'opencode')
}

function config() {
  try {
    return JSON.parse(readFileSync(join(configDir(), 'persona.json'), 'utf8'))
  } catch {
    return {}
  }
}

/** One host per server process: every location's plugin registers with it. */
function host() {
  const slot = (globalThis.__personaOpencode ??= {})
  if (slot.host) return slot.host // it connects again by itself when a location registers after all went away
  const c = config()
  const configuredModel = typeof c.model === 'string' && c.model.trim()
    ? c.model.trim()
    : readConfiguredModel({ configDir: configDir() })
  slot.host = createOpencodeHost({
    openDb: () => openReadOnly(defaultDbPath()),
    log: (m) => { if (envValue('PERSONA_OPENCODE_DEBUG')) console.error(m) },
  }, {
    socketPath: typeof c.socket === 'string' && c.socket ? c.socket : undefined,
    answerFirst: ['auto', 'phone', 'local'].includes(c.answerFirst) ? c.answerFirst : 'auto',
    listDays: Number.isFinite(c.listDays) ? c.listDays : undefined,
    maxSessions: Number.isFinite(c.maxSessions) ? c.maxSessions : undefined,
    launcher: typeof c.launcher === 'string' ? c.launcher : undefined,
    displayName: typeof c.displayName === 'string' ? c.displayName : undefined,
    assistant: typeof c.assistant === 'boolean' ? c.assistant : true,
    model: configuredModel || undefined,
    instance: `opencode-${process.pid}`,
  })
  return slot.host
}

export default {
  id: 'persona',
  // opencode 1.x requires a server() hook on default-exported plugin objects; keep it a no-op so installing the shim
  // alongside the standalone ACP companion (`companion.mjs`) does not emit plugin-load errors in opencode 1.x.
  server() {
    return {}
  },
  async setup(ctx) {
    // opencode 1.x loads definitions in a registration-only host without sessions or events: nothing to do there.
    if (!ctx?.session || !ctx.event?.subscribe) return undefined
    // One-shot runs (`opencode run …`) have nobody to drive from the phone.
    if (process.argv[2] === 'run') return undefined
    const runtime = host()
    const dispose = runtime.register(ctx)
    try {
      await registerA2A(ctx, runtime.runtime)
    } catch (err) {
      dispose()
      throw err
    }
    return dispose
  },
}
