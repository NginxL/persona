#!/usr/bin/env node
// The one-time switch to self-updating (docs/integrations/runtimes.md "Antigravity CLI"): installs this checkout's
// Antigravity package into the store beside the node's socket as its current version, and prints the supervised path
// (<store>/current/antigravity/companion.mjs) so node updates take effect automatically from then on.
//
//   node integrations/antigravity/install.mjs [--socket <runtime.sock>]

import { runtimeStorePath } from '../runtime-sdk/lib/compat.js'
import { realpathSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { prepareSocketDirectory } from '../runtime-sdk/lib/identity.js'
import { defaultSocketPath, installCurrent, PACKAGES, packageHash, readShipped } from '../runtime-sdk/index.js'

export const PACKAGE_NAME = 'persona-antigravity-runtime'

export function installAntigravityPackage({ socketPath, root = join(dirname(fileURLToPath(import.meta.url)), '..') } = {}) {
  const socket = socketPath || defaultSocketPath()
  const store = runtimeStorePath(prepareSocketDirectory(socket), PACKAGE_NAME)
  const files = readShipped(root, PACKAGES[PACKAGE_NAME])
  const hash = packageHash(files)
  installCurrent(store, hash, files)
  return { store, current: join(store, 'current'), hash }
}

// Node loads the entry module by its real path; compare real paths so a symlinked directory still runs it (#12848).
function isEntry() {
  try {
    return !!process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))
  } catch {
    return false // no file behind argv[1]
  }
}

if (isEntry()) {
  const i = process.argv.indexOf('--socket')
  const socketPath = (i > 0 && process.argv[i + 1]) || undefined
  const { current, hash } = installAntigravityPackage({ socketPath })
  console.log(`installed ${PACKAGE_NAME} ${hash.slice(0, 12)} as ${current}

next:
  node ${join(current, 'antigravity', 'companion.mjs')}   # under launchd / systemd (KeepAlive / Restart=always)`)
}
