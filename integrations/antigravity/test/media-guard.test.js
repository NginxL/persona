import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { decide, inspectImage, installMediaGuard } from '../lib/media-guard.js'

const SCRIPT = fileURLToPath(new URL('../media-guard.mjs', import.meta.url))
// A baseline JPEG: SOI, a quantization table, a frame header, SOS, two bytes of image data, EOI.
const segment = (marker, body) => Buffer.concat([Buffer.from([0xff, marker]), Buffer.from([0, body.length + 2]), body])
const jpeg = Buffer.concat([Buffer.from([0xff, 0xd8]), segment(0xdb, Buffer.alloc(65)), segment(0xc0, Buffer.alloc(9)),
  segment(0xda, Buffer.alloc(8)), Buffer.from([0x12, 0xff, 0x00, 0x34]), Buffer.from([0xff, 0xd9])])
const chunk = (type, data = Buffer.alloc(0)) => Buffer.concat([Buffer.from([0, 0, 0, data.length]), Buffer.from(type), data, Buffer.alloc(4)])
const png = Buffer.concat([Buffer.from('89504e470d0a1a0a', 'hex'), chunk('IHDR', Buffer.alloc(13)), chunk('IDAT', Buffer.alloc(6)), chunk('IEND')])

test('complete pictures and other files pass; cut or damaged pictures are named', () => {
  assert.deepEqual(inspectImage(jpeg), { ok: true, mime: 'image/jpeg' })
  assert.equal(inspectImage(Buffer.concat([jpeg, Buffer.from('vendor trailer')])).ok, true)
  assert.deepEqual(inspectImage(jpeg.subarray(0, jpeg.length - 3)), { ok: false, mime: 'image/jpeg', reason: 'truncated', scan: true })
  assert.deepEqual(inspectImage(jpeg.subarray(0, 40)), { ok: false, mime: 'image/jpeg', reason: 'truncated' })
  assert.deepEqual(inspectImage(png), { ok: true, mime: 'image/png' })
  assert.deepEqual(inspectImage(png.subarray(0, png.length - 12)), { ok: false, mime: 'image/png', reason: 'truncated' })
  assert.equal(inspectImage(Buffer.from('GIF89a......;')).ok, true)
  assert.equal(inspectImage(Buffer.from('GIF89a......')).ok, false)
  const webp = Buffer.concat([Buffer.from('RIFF'), Buffer.from([12, 0, 0, 0]), Buffer.from('WEBPVP8 '), Buffer.alloc(4)])
  assert.equal(inspectImage(webp).ok, true)
  assert.equal(inspectImage(webp.subarray(0, 14)).ok, false)
  assert.deepEqual(inspectImage(Buffer.from('plain text')), { ok: true })
})

test('read_file of a truncated JPEG is refused with a reason and a readable copy; the file is unchanged', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-media-guard-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const cut = jpeg.subarray(0, jpeg.length - 2)
  const file = join(dir, 'shot.jpg')
  writeFileSync(file, cut)
  const state = join(dir, 'state')
  const answer = decide({ toolCall: { name: 'read_file', args: { Path: file } } }, state)
  assert.equal(answer.decision, 'deny')
  assert.match(answer.reason, /cut short \(truncated\)/)
  assert.match(answer.reason, /kept unchanged/)
  const copy = /readable as a separate copy: (\S+)$/.exec(answer.reason)?.[1]
  assert.ok(copy && copy.startsWith(state))
  assert.deepEqual(readFileSync(copy), Buffer.concat([cut, Buffer.from([0xff, 0xd9])]))
  assert.deepEqual(inspectImage(readFileSync(copy)), { ok: true, mime: 'image/jpeg' })
  assert.deepEqual(decide({ toolCall: { name: 'read_file', args: { Path: copy } } }, state), { decision: 'ask' })
  assert.deepEqual(readFileSync(file), cut)
  // A cut header has no image data to keep: refused without a copy.
  writeFileSync(file, jpeg.subarray(0, 40))
  const header = decide({ toolCall: { args: { Path: file } } }, state)
  assert.equal(header.decision, 'deny')
  assert.doesNotMatch(header.reason, /separate copy/)
})

test('everything else gets no opinion, and the script never fails closed', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-media-guard-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  writeFileSync(join(dir, 'ok.jpg'), jpeg)
  for (const payload of [undefined, {}, { toolCall: { args: {} } }, { toolCall: { args: { Path: join(dir, 'missing.jpg') } } },
    { toolCall: { args: { Path: join(dir, 'ok.jpg') } } }, { toolCall: { args: { Path: dir } } }]) {
    assert.deepEqual(decide(payload, join(dir, 'state')), { decision: 'ask' })
  }
  const run = (input) => JSON.parse(execFileSync(process.execPath, [SCRIPT, '--state', join(dir, 'state')], { input }).toString())
  assert.deepEqual(run('not json'), { decision: 'ask' })
  writeFileSync(join(dir, 'cut.png'), png.subarray(0, 20))
  assert.equal(run(JSON.stringify({ toolCall: { name: 'read_file', args: { Path: join(dir, 'cut.png') } } })).decision, 'deny')
})

test('the plugin is written idempotently and answers "ask" once its script is gone', (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-media-guard-'))
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const agyDir = join(home, 'antigravity-cli')
  mkdirSync(agyDir)
  const dir = installMediaGuard({ agyDir, script: SCRIPT })
  assert.equal(dir, join(home, 'config', 'plugins', 'persona-media-guard'))
  const hooks = JSON.parse(readFileSync(join(dir, 'hooks.json'), 'utf8'))['persona-media-guard'].PreToolUse[0]
  assert.equal(hooks.matcher, 'read_file|view_file')
  const sh = (command, input) => JSON.parse(execFileSync('/bin/sh', ['-c', command], { input }).toString())
  writeFileSync(join(home, 'cut.png'), png.subarray(0, 20))
  const payload = JSON.stringify({ toolCall: { name: 'read_file', args: { Path: join(home, 'cut.png') } } })
  assert.equal(sh(hooks.hooks[0].command, payload).decision, 'deny')
  assert.equal(installMediaGuard({ agyDir, script: SCRIPT }), dir)
  const gone = installMediaGuard({ agyDir, script: join(home, 'moved', 'media-guard.mjs') })
  const stale = JSON.parse(readFileSync(join(gone, 'hooks.json'), 'utf8'))['persona-media-guard'].PreToolUse[0].hooks[0].command
  assert.deepEqual(sh(stale, payload), { decision: 'ask' })
  assert.ok(existsSync(join(dir, 'plugin.json')))
})
