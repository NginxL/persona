import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE_POLICY = fileURLToPath(new URL('./fake-acp-policy.mjs', import.meta.url))
const COMPANION = fileURLToPath(new URL('../companion.mjs', import.meta.url))

function makeFakeWrapper(home, recordPath, envLogPath) {
  const wrapper = join(home, 'wrapped-agy')
  const logger = join(home, 'log-env.mjs')
  writeFileSync(logger, `
import { appendFileSync } from 'node:fs'
appendFileSync(${JSON.stringify(envLogPath)}, JSON.stringify({
  args: process.argv.slice(2),
  configDir: process.env.ANTIGRAVITY_CLI_CONFIG_DIR || '',
  extraEnv: process.env.EXTRA_EXT_VAR || '',
}) + '\\n')
`)
  writeFileSync(wrapper, `#!/bin/sh
'${process.execPath}' '${logger}' "$@"
if [ "$1" = "models" ]; then
  printf 'gemini-4-neon-high\\tGemini 4 Neon (High)\\ngemini-3.8-flash\\tGemini 3.8 Flash\\n'
  exit 0
fi
exec '${process.execPath}' '${FAKE_POLICY}' --record '${recordPath}' "$@"
`)
  chmodSync(wrapper, 0o700)
  return wrapper
}

test('createAgyRuntime validates custom runtime, launcher, prefix, and assistantFor options', () => {
  assert.throws(() => createAgyRuntime({ runtime: 'bad id' }), /Unsupported Antigravity runtime id/)
  assert.throws(() => createAgyRuntime({ launcher: 'bad launcher' }), /Unsupported Antigravity launcher name/)
  assert.throws(() => createAgyRuntime({ prefix: 'bad/prefix' }), /Unsupported Antigravity session prefix/)
  assert.throws(() => createAgyRuntime({ runtime: 'agy-wrap', assistantFor: 'agy-wrap' }), /Unsupported Antigravity assistant-for launcher/)
  assert.throws(() => createAgyRuntime({ runtime: 'agy-wrap', assistantFor: '1bad' }), /Unsupported Antigravity assistant-for launcher/)
})

test('custom runtime instance scopes hello, launcher, session prefix, history, and ANTIGRAVITY_CLI_CONFIG_DIR', async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-multi-'))
  const agyDir = join(home, 'custom-agy-dir')
  const assistantDir = join(home, '.persona', 'assistant')
  mkdirSync(agyDir, { recursive: true })
  mkdirSync(assistantDir, { recursive: true })
  const recordPath = join(home, 'requests.jsonl')
  const envLogPath = join(home, 'env.jsonl')
  writeFileSync(recordPath, '')
  writeFileSync(envLogPath, '')
  const wrapper = makeFakeWrapper(home, recordPath, envLogPath)

  const node = await startFakeNode(t, { features: ['assistant_pids'] })
  const runtime = createAgyRuntime({
    home,
    agyDir,
    agyBin: wrapper,
    personadBin: wrapper,
    socketPath: node.path,
    runtime: 'agy-wrap',
    launcher: 'agy-corp',
    displayName: 'Antigravity Wrapped',
    prefix: 'agywrap-',
    assistantFor: 'agy-paseo',
    log: () => {},
  })
  t.after(async () => {
    await runtime.stop()
    rmSync(home, { recursive: true, force: true })
  })

  runtime.start()
  await node.ready

  assert.equal(node.hello.runtime, 'agy-wrap')
  assert.equal(node.hello.name, 'Antigravity Wrapped')
  assert.equal(node.hello.agent, 'agy')
  assert.equal(node.hello.launcher, 'agy-corp')
  assert.equal(node.hello.caps.assistant, true)
  assert.equal(node.hello.caps.supervision_policy, true)

  await until('custom launcher models published', () => node.last('runtime.launchers')?.launchers?.[0]?.models?.length === 2)
  const published = node.last('runtime.launchers').launchers[0]
  assert.equal(published.name, 'agy-corp')
  assert.equal(published.agent, 'agy')
  assert.equal(published.display_name, 'Antigravity Wrapped')
  assert.equal(published.assistant_for, 'agy-paseo')

  const started = await node.peer.call('session.start', {
    cwd: assistantDir,
    assistant: true,
    env: { EXTRA_EXT_VAR: 'from-node' },
  })
  const sid = started.session.session_id
  assert.ok(sid.startsWith('agywrap-'), `expected custom prefix, got ${sid}`)
  assert.equal(started.session.launcher, 'agy-corp')
  assert.equal(started.session.agent_label, 'Antigravity Wrapped')

  // Rejects default agy- prefix for the same raw UUID.
  const rawId = sid.slice('agywrap-'.length)
  await assert.rejects(node.peer.call('session.items', { session_id: `agy-${rawId}` }), /not found/i)

  const pageRes = await node.peer.call('session.items', { session_id: sid })
  assert.ok(pageRes.history.startsWith(`agy-wrap:${rawId}:`), `expected runtime-scoped history, got ${pageRes.history}`)

  await until('listed session row reflects custom launcher and label', () =>
    node.last('runtime.sessions')?.sessions?.some((s) => s.session_id === sid && s.launcher === 'agy-corp' && s.agent_label === 'Antigravity Wrapped'))

  const envLogs = readFileSync(envLogPath, 'utf8').trim().split('\n').map(JSON.parse)
  const modelsCall = envLogs.find((e) => e.args[0] === 'models')
  assert.equal(modelsCall?.configDir, agyDir)
  const acpCall = envLogs.find((e) => e.args[0] === 'acp')
  assert.equal(acpCall?.configDir, agyDir)
  assert.equal(acpCall?.extraEnv, 'from-node')
})

test('companion.mjs forwards multi-instance CLI flags', async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-cli-multi-'))
  const agyDir = join(home, 'custom-dir')
  mkdirSync(agyDir, { recursive: true })
  const recordPath = join(home, 'requests.jsonl')
  const envLogPath = join(home, 'env.jsonl')
  writeFileSync(recordPath, '')
  writeFileSync(envLogPath, '')
  const wrapper = makeFakeWrapper(home, recordPath, envLogPath)

  const node = await startFakeNode(t)
  const stderr = []
  const child = spawn(process.execPath, [
    COMPANION,
    '--socket', node.path,
    '--agy-bin', wrapper,
    '--agy-dir', agyDir,
    '--runtime', 'agy-alt',
    '--launcher', 'agy-alt',
    '--display-name', 'Alt Agy',
    '--prefix', 'alt-',
    '--no-media-guard',
    '--no-update',
  ], { env: { ...process.env, HOME: home }, stdio: ['ignore', 'pipe', 'pipe'] })
  child.stderr.on('data', (chunk) => stderr.push(String(chunk)))
  t.after(async () => {
    child.kill('SIGTERM')
    await new Promise((resolve) => child.once('exit', resolve))
    rmSync(home, { recursive: true, force: true })
  })

  await node.ready
  assert.equal(node.hello.runtime, 'agy-alt')
  assert.equal(node.hello.name, 'Alt Agy')
  assert.equal(node.hello.agent, 'agy')
  assert.equal(node.hello.launcher, 'agy-alt')
  await until('startup log line', () => stderr.join('').includes('persona-agy-alt: started Alt Agy (agy-alt) runtime for Persona'))
})
