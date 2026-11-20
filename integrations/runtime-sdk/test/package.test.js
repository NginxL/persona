import { test } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { currentHash, installCurrent, packageHash, PACKAGES, readShipped, selfPackage } from '../lib/package.js'

const version = (text) => {
  const files = [{ path: 'pi/index.js', data: Buffer.from(text) }, { path: 'runtime-sdk/index.js', data: Buffer.from('export {}') },
    { path: 'pi/package.json', data: Buffer.from('{"type":"module"}') }]
  return { hash: packageHash(files), files }
}

test('installCurrent points current at the new version, keeps the one before, drops older ones, and is idempotent', (t) => {
  const store = mkdtempSync(join(tmpdir(), 'jrt-pkg-'))
  t.after(() => rmSync(store, { recursive: true, force: true }))
  const [v1, v2, v3] = [version('1'), version('2'), version('3')]
  installCurrent(store, v1.hash, v1.files)
  assert.equal(currentHash(store), v1.hash)
  installCurrent(store, v2.hash, v2.files)
  assert.equal(currentHash(store), v2.hash)
  assert.ok(existsSync(join(store, v1.hash)), 'the version current pointed at before stays (a process may still run it)')
  installCurrent(store, v3.hash, v3.files)
  assert.equal(currentHash(store), v3.hash)
  assert.deepEqual(readdirSync(store).filter((n) => n !== 'current').sort(), [v2.hash, v3.hash].sort())
  installCurrent(store, v3.hash, v3.files) // the companion and an extension both install it
  assert.equal(currentHash(store), v3.hash)
  assert.ok(existsSync(join(store, 'current', 'pi', 'index.js')))
})

test('selfPackage: a module run from a store version (through current) knows its store; from a checkout it has none', (t) => {
  const store = mkdtempSync(join(tmpdir(), 'jrt-pkg-'))
  const checkout = mkdtempSync(join(tmpdir(), 'jrt-co-'))
  t.after(() => {
    rmSync(store, { recursive: true, force: true })
    rmSync(checkout, { recursive: true, force: true })
  })
  for (const f of ['pi/package.json', 'pi/index.js', 'pi/companion.mjs', 'pi/lib/pi.js', 'runtime-sdk/package.json', 'runtime-sdk/index.js', 'runtime-sdk/lib/runtime.js']) {
    mkdirSync(join(checkout, f, '..'), { recursive: true })
    writeFileSync(join(checkout, f), f)
  }
  const files = readShipped(checkout, PACKAGES['persona-pi-runtime'])
  const hash = packageHash(files)
  const fromCheckout = selfPackage(pathToFileURL(join(checkout, 'pi', 'index.js')).href, 'persona-pi-runtime')
  assert.equal(fromCheckout.hash, hash)
  assert.equal(fromCheckout.store, undefined)
  installCurrent(store, hash, files)
  const fromStore = selfPackage(pathToFileURL(join(store, 'current', 'pi', 'index.js')).href, 'persona-pi-runtime')
  assert.equal(fromStore.hash, hash)
  assert.equal(fromStore.store, realpathSync(store)) // through current, to the version's real directory
})

test('persona-antigravity-runtime hashes companion, mcp-proxy, lib and runtime-sdk identically across checkout and store', (t) => {
  const store = mkdtempSync(join(tmpdir(), 'jrt-pkg-'))
  const checkout = mkdtempSync(join(tmpdir(), 'jrt-co-'))
  t.after(() => {
    rmSync(store, { recursive: true, force: true })
    rmSync(checkout, { recursive: true, force: true })
  })
  for (const f of [
    'antigravity/package.json',
    'antigravity/companion.mjs',
    'antigravity/desktop.mjs',
    'antigravity/mcp-proxy.mjs',
    'antigravity/lib/runtime.js',
    'antigravity/install.mjs',
    'runtime-sdk/package.json',
    'runtime-sdk/index.js',
    'runtime-sdk/lib/runtime.js',
  ]) {
    mkdirSync(join(checkout, f, '..'), { recursive: true })
    writeFileSync(join(checkout, f), f)
  }
  const files = readShipped(checkout, PACKAGES['persona-antigravity-runtime'])
  assert.deepEqual(files.map((f) => f.path), [
    'antigravity/package.json',
    'antigravity/companion.mjs',
    'antigravity/desktop.mjs',
    'antigravity/mcp-proxy.mjs',
    'antigravity/lib/runtime.js',
    'runtime-sdk/package.json',
    'runtime-sdk/index.js',
    'runtime-sdk/lib/runtime.js',
  ])
  const hash = packageHash(files)
  const fromCheckout = selfPackage(pathToFileURL(join(checkout, 'antigravity', 'companion.mjs')).href, 'persona-antigravity-runtime')
  assert.equal(fromCheckout.name, 'persona-antigravity-runtime')
  assert.equal(fromCheckout.hash, hash)
  assert.equal(fromCheckout.store, undefined)
  installCurrent(store, hash, files)
  const fromStore = selfPackage(pathToFileURL(join(store, 'current', 'antigravity', 'companion.mjs')).href, 'persona-antigravity-runtime')
  assert.equal(fromStore.name, 'persona-antigravity-runtime')
  assert.equal(fromStore.hash, hash)
  assert.equal(fromStore.store, realpathSync(store))
})
