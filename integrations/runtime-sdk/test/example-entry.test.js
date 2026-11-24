import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { chmodSync, mkdtempSync, realpathSync, rmSync, symlinkSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { until } from '../testing/fake-node.js'

// #12848: Node loads the entry module by its real path while argv[1] keeps a symlink on the way (a symlinked home is
// common on Linux), so an entry check that compares the two as given skips the program and exits 0 without a word.
test('the echo runtime example runs when started through a symlinked directory', async (t) => {
  const base = realpathSync(mkdtempSync(join(tmpdir(), 'persona-echo-link-')))
  const examples = fileURLToPath(new URL('../examples', import.meta.url))
  const link = join(base, 'examples')
  symlinkSync(examples, link, process.platform === 'win32' ? 'junction' : 'dir')
  const child = spawn(process.execPath, [join(link, 'echo-runtime.mjs'), '--socket', join(base, 'missing', 'runtime.sock')],
    { stdio: ['ignore', 'ignore', 'pipe'] })
  const exited = once(child, 'exit')
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
    await exited
    chmodSync(base, 0o700)
    rmSync(base, { recursive: true, force: true })
  })
  let log = ''
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', (part) => { log += part })
  // The start line is the proof the program ran: without a node behind the socket it may exit afterwards.
  await until('echo runtime started or exited', () => log.includes('echo runtime: waiting for the personad node') || child.exitCode !== null, 5000)
  if (!log.includes('echo runtime: waiting for the personad node')) await exited
  assert.match(log, /echo runtime: waiting for the personad node/, `the example exited (${child.exitCode}) without running; stderr=${JSON.stringify(log)}`)
})
