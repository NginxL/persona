// Runtime packages (docs/integrations/runtimes.md "Auto-update"): the code of a runtime's plugin as files with one hash, the same as
// personad's (server/internal/runtimepkg). The loader (loader.js) hashes the copy it ships with, keeps the versions the
// node sends in a store and checks every one before it runs it.

import { addPackageAliases, wirePackageName } from './compat.js'
import { createHash, randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, readdirSync, readFileSync, readlinkSync, realpathSync, renameSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { basename, dirname, join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * The packages and the files each is made of, relative to the integrations directory (`*` only in the last segment).
 * Must stay the same as personad's runtimepkg.Packages: a file one side counts and the other does not makes every
 * hello look like another version.
 */
export const PACKAGES = addPackageAliases({
  'persona-dsh-runtime': ['dsh/package.json', 'dsh/lib/*.js', 'runtime-sdk/package.json', 'runtime-sdk/index.js', 'runtime-sdk/lib/*.js'],
  'persona-pi-runtime': ['pi/package.json', 'pi/index.js', 'pi/companion.mjs', 'pi/lib/*.js', 'runtime-sdk/package.json', 'runtime-sdk/index.js', 'runtime-sdk/lib/*.js'],
  'persona-antigravity-runtime': ['antigravity/package.json', 'antigravity/companion.mjs', 'antigravity/desktop.mjs', 'antigravity/mcp-proxy.mjs', 'antigravity/media-guard.mjs', 'antigravity/lib/*.js', 'runtime-sdk/package.json', 'runtime-sdk/index.js', 'runtime-sdk/lib/*.js'],
  'persona-opencode-runtime': ['opencode/package.json', 'opencode/index.js', 'opencode/companion.mjs', 'opencode/lib/*.js', 'runtime-sdk/package.json', 'runtime-sdk/index.js', 'runtime-sdk/lib/*.js'],
  'persona-paseo-runtime': ['paseo/package.json', 'paseo/bridge.mjs', 'paseo/lib/*.js', 'runtime-sdk/package.json', 'runtime-sdk/index.js', 'runtime-sdk/lib/*.js'],
})

/** A package name, as personad takes it. */
export const NAME_RE = /^[a-z0-9][a-z0-9._-]{0,63}$/

/** Bounds a package (all its files), as personad does. */
export const MAX_SIZE = 8 << 20

/** A file's path in a package: relative, slash-separated, no empty / . / .. segments. */
export function validPath(p) {
  if (typeof p !== 'string' || p === '' || p.startsWith('/') || p.includes('\\') || p.length > 256) return false
  return p.split('/').every((seg) => seg !== '' && seg !== '.' && seg !== '..')
}

/**
 * A package's hash: sha256 over its files sorted by path, each as "<path>\0<hex sha256 of the content>\n", in hex.
 * @param {{path: string, data: Buffer}[]} files
 */
export function packageHash(files) {
  const h = createHash('sha256')
  for (const f of [...files].sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0))) {
    h.update(`${f.path}\0${createHash('sha256').update(f.data).digest('hex')}\n`)
  }
  return h.digest('hex')
}

/**
 * The files of a package in an integrations directory, by its patterns (what the loader ships with).
 * @param {string} root - the integrations directory (where dsh/ and runtime-sdk/ are).
 * @param {string[]} patterns
 */
export function readShipped(root, patterns) {
  const files = []
  for (const pattern of patterns) {
    const at = pattern.lastIndexOf('/')
    const dir = at < 0 ? '' : pattern.slice(0, at)
    const base = pattern.slice(at + 1)
    if (!base.includes('*')) {
      const path = join(root, pattern)
      if (isFile(path)) files.push({ path: pattern, data: readFileSync(path) })
      continue
    }
    const [prefix, suffix] = base.split('*')
    let names = []
    try {
      names = readdirSync(join(root, dir))
    } catch {
      continue
    }
    for (const n of names.sort()) {
      if (!n.startsWith(prefix) || !n.endsWith(suffix) || n.length < prefix.length + suffix.length) continue
      const path = join(root, dir, n)
      if (isFile(path)) files.push({ path: dir ? `${dir}/${n}` : n, data: readFileSync(path) })
    }
  }
  return files
}

/** Every regular file under dir (a stored version), with slash-separated relative paths. */
export function readTree(dir) {
  const files = []
  const walk = (d) => {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const path = join(d, e.name)
      if (e.isDirectory()) walk(path)
      else if (e.isFile()) files.push({ path: relative(dir, path).split(sep).join('/'), data: readFileSync(path) })
    }
  }
  walk(dir)
  return files
}

function isFile(path) {
  try {
    return statSync(path).isFile()
  } catch {
    return false
  }
}

/**
 * The files of a package the node sent (runtime.package's answer), checked: name, paths, size, and that they hash to
 * what it says. Throws on anything off.
 * @param {{name: string, hash: string, files: {path: string, data: string}[]}} pkg
 * @param {string} name - the package asked for.
 */
export function verifyPackage(pkg, name) {
  if (!pkg || pkg.name !== name) throw new Error(`the node sent package ${pkg?.name}, not ${name}`)
  if (!Array.isArray(pkg.files) || pkg.files.length === 0) throw new Error(`package ${name} has no files`)
  const files = []
  const seen = new Set()
  let size = 0
  for (const f of pkg.files) {
    if (!validPath(f?.path) || seen.has(f.path) || typeof f.data !== 'string') throw new Error(`package ${name}: bad file ${JSON.stringify(f?.path)}`)
    seen.add(f.path)
    const data = Buffer.from(f.data, 'base64')
    if ((size += data.length) > MAX_SIZE) throw new Error(`package ${name} is over ${MAX_SIZE} bytes`)
    files.push({ path: f.path, data })
  }
  const hash = packageHash(files)
  if (hash !== pkg.hash) throw new Error(`package ${name}: its files hash to ${hash.slice(0, 12)}, not ${String(pkg.hash).slice(0, 12)}`)
  return { hash, files }
}

/**
 * Put a checked package in the store as <store>/<hash>/ (written aside, then renamed into place: never half a version).
 * @returns {string} its directory.
 */
export function storeVersion(store, hash, files) {
  const dir = join(store, hash)
  if (existsSync(dir)) {
    try {
      if (packageHash(readTree(dir)) === hash) return dir
    } catch {
      // unreadable: written again below
    }
    rmSync(dir, { recursive: true, force: true })
  }
  mkdirSync(store, { recursive: true, mode: 0o700 })
  const tmp = join(store, `.${hash}.${randomBytes(4).toString('hex')}.tmp`)
  try {
    for (const f of files) {
      const path = join(tmp, ...f.path.split('/'))
      mkdirSync(join(path, '..'), { recursive: true })
      writeFileSync(path, f.data, { mode: 0o644 })
    }
    try {
      renameSync(tmp, dir)
    } catch (err) {
      // Another process stored the same version meanwhile (pi-web's extension and the companion both install it).
      if (!existsSync(dir) || packageHash(readTree(dir)) !== hash) throw err
    }
  } finally {
    rmSync(tmp, { recursive: true, force: true })
  }
  return dir
}

const HASH_DIR = /^[0-9a-f]{64}$/

/** The version <store>/current points at (installCurrent), or ''. */
export function currentHash(store) {
  try {
    const target = readlinkSync(join(store, 'current'))
    return HASH_DIR.test(target) ? target : ''
  } catch {
    return ''
  }
}

/**
 * Store a checked package and point <store>/current at it (a symlink replaced by a rename: whoever reads `current`
 * sees the old version or the new one, never neither). For a runtime whose host loads it from a fixed path
 * (pi's settings name <store>/current/pi). Keeps the version current pointed at before, removes older ones.
 * @returns {string} the version's directory.
 */
export function installCurrent(store, hash, files) {
  const dir = storeVersion(store, hash, files)
  const before = currentHash(store)
  if (before !== hash) {
    const tmp = join(store, `.current.${randomBytes(4).toString('hex')}.tmp`)
    symlinkSync(hash, tmp)
    renameSync(tmp, join(store, 'current'))
  }
  for (const n of readdirSync(store)) {
    if ((HASH_DIR.test(n) && n !== hash && n !== before) || (n.startsWith('.') && n.endsWith('.tmp'))) {
      rmSync(join(store, n), { recursive: true, force: true })
    }
  }
  return dir
}

/**
 * The package a module of it runs from: the directory holding its dirs (dsh/, pi/, runtime-sdk/), its hash, and —
 * when that directory is a version in a store (<store>/<hash>/, reached through <store>/current or not) — the store,
 * so the runtime may install the node's version there.
 * @param {string} url - import.meta.url of a module at <root>/<dir>/<file> (depth: how many dirs below root).
 * @param {string} name - the package (PACKAGES).
 * @param {number} [depth] - 1 for <root>/pi/index.js, 2 for <root>/pi/lib/x.js.
 * @returns {{ name: string, root: string, hash: string, store?: string }}
 */
export function selfPackage(url, name, depth = 1) {
  name = wirePackageName(name)
  let root = dirname(fileURLToPath(url))
  for (let i = 0; i < depth; i++) root = dirname(root)
  try {
    root = realpathSync(root)
  } catch {
    // as given
  }
  const hash = packageHash(readShipped(root, PACKAGES[name] ?? []))
  const inStore = HASH_DIR.test(basename(root)) && basename(root) === hash
  return { name, root, hash, ...(inStore ? { store: dirname(root) } : {}) }
}

/** Write a small JSON file atomically. */
export function writeJSON(path, value) {
  const tmp = `${path}.${randomBytes(4).toString('hex')}.tmp`
  writeFileSync(tmp, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 })
  renameSync(tmp, path)
}
