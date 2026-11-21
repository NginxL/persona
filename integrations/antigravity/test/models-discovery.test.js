import assert from 'node:assert/strict'
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { discoverModels } from '../lib/models.js'

function fixture(t) {
  const directory = mkdtempSync(join(tmpdir(), 'agy-discovery-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const received = join(directory, 'received.json')
  const fake = join(directory, 'models.mjs')
  writeFileSync(fake, `
    import { writeFileSync } from 'node:fs'
    writeFileSync(${JSON.stringify(received)}, JSON.stringify({
      args: process.argv.slice(2),
      autoUpdate: process.env.AGY_CLI_DISABLE_AUTO_UPDATE,
      marker: process.env.DISCOVERY_TEST_MARKER,
    }))
    console.log('Fetching available models...')
    console.log('gemini-future-medium\\tGemini Future (medium)')
    console.log('gemini-future-high\\tGemini Future (high)')
  `)
  const bin = join(directory, 'agy')
  writeFileSync(bin, `#!/bin/sh\nexec '${process.execPath}' '${fake}' "$@"\n`)
  chmodSync(bin, 0o755)
  return { bin, received: () => JSON.parse(readFileSync(received, 'utf8')) }
}

for (const value of [undefined, '1', 'false']) {
  test(`discovery disables child updates without changing caller env (${value ?? 'unset'})`, async (t) => {
    const f = fixture(t)
    const env = { ...process.env, DISCOVERY_TEST_MARKER: 'inherited' }
    delete env.AGY_CLI_DISABLE_AUTO_UPDATE
    if (value !== undefined) env.AGY_CLI_DISABLE_AUTO_UPDATE = value
    const before = { ...env }
    Object.freeze(env)

    const models = await discoverModels(f.bin, { env })

    assert.deepEqual(f.received(), { args: ['models'], autoUpdate: 'true', marker: 'inherited' })
    assert.deepEqual(models, [
      { id: 'gemini-future-medium', name: 'Gemini Future (medium)', default: false, efforts: ['medium', 'high'], default_effort: 'medium' },
      { id: 'gemini-future-high', name: 'Gemini Future (high)', default: false, efforts: ['medium', 'high'], default_effort: 'high' },
    ])
    assert.deepEqual(env, before)
  })
}

test('discovery leaves process.env unchanged when no env is supplied', async (t) => {
  const f = fixture(t)
  const original = process.env.AGY_CLI_DISABLE_AUTO_UPDATE
  t.after(() => {
    if (original === undefined) delete process.env.AGY_CLI_DISABLE_AUTO_UPDATE
    else process.env.AGY_CLI_DISABLE_AUTO_UPDATE = original
  })
  process.env.AGY_CLI_DISABLE_AUTO_UPDATE = '1'
  const before = { ...process.env }

  await discoverModels(f.bin)

  assert.equal(f.received().autoUpdate, 'true')
  assert.deepEqual({ ...process.env }, before)
})
