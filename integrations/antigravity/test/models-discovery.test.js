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
    console.log('gemini-4-neon-xhigh\\tGemini 4 Neon (XHigh)')
    console.log('gemini-4-neon-medium\\tGemini 4 Neon (Medium)')
    console.log('gemini-4-hydrogen-max\\tGemini 4 Hydrogen (Max)')
    console.log('gemini-4-hydrogen-xhigh\\tGemini 4 Hydrogen (XHigh)')
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
      { id: 'gemini-4-neon-xhigh', name: 'Gemini 4 Neon (XHigh)', default: false, efforts: ['medium', 'xhigh'], default_effort: 'xhigh' },
      { id: 'gemini-4-neon-medium', name: 'Gemini 4 Neon (Medium)', default: false, efforts: ['medium', 'xhigh'], default_effort: 'medium' },
      { id: 'gemini-4-hydrogen-max', name: 'Gemini 4 Hydrogen (Max)', default: false, efforts: ['xhigh', 'max'], default_effort: 'max' },
      { id: 'gemini-4-hydrogen-xhigh', name: 'Gemini 4 Hydrogen (XHigh)', default: false, efforts: ['xhigh', 'max'], default_effort: 'xhigh' },
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
