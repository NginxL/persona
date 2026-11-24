#!/usr/bin/env node
// Installs the Persona plugin into an opencode config directory (docs/integrations/opencode.md "Installation"): one file in plugins/ that
// re-exports this checkout's plugin, so updating the checkout updates the plugin. opencode loads plugins/*.js of its
// config directory by itself; the shared `opencode serve` has to be restarted once to pick it up.
//
//   node integrations/opencode/install.mjs [--config-dir <dir>] [--uninstall] [--force]
//
// The default directory is $XDG_CONFIG_HOME/opencode (~/.config/opencode). A file of that name that this script did not
// write is left alone unless --force.

import { existsSync, mkdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

export const MARK = '// persona-opencode-runtime: written by integrations/opencode/install.mjs'
export const SHIM = 'persona.js'

/** The shim's text: re-exports the plugin at entry. */
export function shimText(entry) {
  return `${MARK}\nexport { default } from ${JSON.stringify(pathToFileURL(entry).href)}\n`
}

/**
 * Installs (or removes) the shim; returns what happened: 'installed', 'updated', 'unchanged', 'removed', 'absent', or
 * 'foreign' when a file of that name is not ours and force is off.
 */
export function install({ configDir, entry, uninstall = false, force = false }) {
  const file = join(configDir, 'plugins', SHIM)
  const current = existsSync(file) ? readFileSync(file, 'utf8') : undefined
  const ours = current?.startsWith(MARK)
  if (uninstall) {
    if (current === undefined) return 'absent'
    if (!ours && !force) return 'foreign'
    rmSync(file)
    return 'removed'
  }
  if (current !== undefined && !ours && !force) return 'foreign'
  const text = shimText(entry)
  if (current === text) return 'unchanged'
  mkdirSync(dirname(file), { recursive: true })
  writeFileSync(file, text)
  return current === undefined ? 'installed' : 'updated'
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
  const arg = (n) => {
    const i = process.argv.indexOf(`--${n}`)
    return i > 0 ? process.argv[i + 1] : undefined
  }
  const configDir = arg('config-dir') ?? join(process.env.XDG_CONFIG_HOME || join(homedir(), '.config'), 'opencode')
  const entry = join(dirname(fileURLToPath(import.meta.url)), 'index.js')
  const result = install({ configDir, entry, uninstall: process.argv.includes('--uninstall'), force: process.argv.includes('--force') })
  const file = join(configDir, 'plugins', SHIM)
  switch (result) {
    case 'foreign':
      console.error(`${file} exists and is not the Persona plugin's; pass --force to replace it`)
      process.exit(1)
      break
    case 'absent':
      console.log(`nothing to remove: ${file}`)
      break
    case 'removed':
      console.log(`removed ${file}; restart opencode serve`)
      break
    default:
      console.log(`${result} ${file} -> ${entry}\nnext: restart the opencode server (opencode serve) once; its sessions then show in Persona`)
  }
}
