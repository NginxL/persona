import assert from 'node:assert/strict'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { toAgyId, workspacePathOf } from '../lib/map.js'
import { AGY_RULE_MAX_BYTES, isPolicyDir, POLICY_DIR_NAME, policyDirOf, readPolicy, SUPERVISION_ERROR_CODE, syncPolicy } from '../lib/policy.js'
import { createAgyRuntime } from '../lib/runtime.js'
import { AgyAcpClient } from '../lib/acp.js'

const FAKE = fileURLToPath(new URL('./fake-acp-policy.mjs', import.meta.url))
const SLOW = 15_000

function received(record) {
  return readFileSync(record, 'utf8').split('\n').filter(Boolean).map((line) => JSON.parse(line))
}

function setupAgy(t, opts = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'agy-policy-'))
  const record = join(dir, 'record.jsonl')
  writeFileSync(record, '')
  t.after(() => {
    for (const m of received(record)) {
      if (m.started) {
        try { process.kill(m.started, 'SIGKILL') } catch {}
      }
    }
    rmSync(dir, { recursive: true, force: true })
  })

  const extraFlags = (opts.acpFlags || []).map((f) => `'${f}'`).join(' ')
  const agyBin = join(dir, 'agy')
  writeFileSync(agyBin, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' --record '${record}' ${extraFlags} "$@"\n`)
  chmodSync(agyBin, 0o755)

  const agyDir = join(dir, 'agy-config')
  mkdirSync(join(agyDir, 'brain'), { recursive: true })
  const assistantDir = join(dir, '.persona', 'assistant')
  mkdirSync(assistantDir, { recursive: true })
  const baseAgentsPath = join(assistantDir, 'AGENTS.md')
  const baseAgentsContent = '# Base Persona Assistant Instructions\nNever overwrite me.\n'
  writeFileSync(baseAgentsPath, baseAgentsContent)

  const policyRoot = opts.policyRoot || join(agyDir, POLICY_DIR_NAME)
  return { dir, record, agyBin, agyDir, assistantDir, baseAgentsPath, baseAgentsContent, policyRoot }
}

async function startRuntime(t, env, runtimeOpts = {}) {
  const n = await startFakeNode(t)
  const rt = createAgyRuntime({
    agyBin: env.agyBin,
    agyDir: env.agyDir,
    home: env.dir,
    policyRoot: env.policyRoot,
    socketPath: n.path,
    log: () => {},
    ...runtimeOpts,
  })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  return { n, rt }
}

test('hello advertises assistant and supervision_policy capabilities (#11173)', async (t) => {
  const env = setupAgy(t)
  const { n } = await startRuntime(t, env)
  assert.equal(n.hello.caps.assistant, true)
  assert.equal(n.hello.caps.supervision_policy, true)
})

test('supervised and ordinary chats sharing ~/.persona/assistant stay isolated and keep base AGENTS.md untouched', async (t) => {
  const env = setupAgy(t)
  const { n } = await startRuntime(t, env)

  const policy = 'SUPERVISION_ROLE_MARKER\nReview worker reports before acting.'
  const supStart = await n.peer.call('session.start', {
    cwd: env.assistantDir,
    assistant: true,
    supervision_instructions: policy,
  })
  const supPid = supStart.session.session_id
  const supRaw = toAgyId(supPid)
  const supPolicyDir = policyDirOf(env.policyRoot, supRaw)

  const ordStart = await n.peer.call('session.start', {
    cwd: env.assistantDir,
    assistant: true,
  })
  const ordPid = ordStart.session.session_id
  const ordRaw = toAgyId(ordPid)

  // Verify on-disk isolation and base AGENTS.md preservation.
  assert.equal(readFileSync(env.baseAgentsPath, 'utf8'), env.baseAgentsContent, 'shared AGENTS.md is never modified')
  assert.equal(readFileSync(join(supPolicyDir, 'AGENTS.md'), 'utf8'), policy)
  assert.equal(existsSync(policyDirOf(env.policyRoot, ordRaw)), false, 'ordinary chat gets no supervision policy dir')
  assert.equal(isPolicyDir(env.policyRoot, supPolicyDir), true)
  assert.equal(isPolicyDir(env.policyRoot, env.assistantDir), false)

  // Verify ACP additionalDirectories per session.
  const resumes = received(env.record).filter((m) => m.method === 'session/resume')
  const supResume = resumes.find((m) => m.params.sessionId === supRaw)
  const ordResume = resumes.find((m) => m.params.sessionId === ordRaw)
  assert.deepEqual(supResume?.params.additionalDirectories, [supPolicyDir])
  assert.equal(ordResume?.params.additionalDirectories, undefined, 'ordinary chat never passes additionalDirectories')

  // Send messages to both; verify user prompt text never contains supervision policy.
  await n.peer.call('session.send', {
    session_id: supPid,
    text: 'Check worker status',
    assistant: true,
    supervision_instructions: policy,
    client_msg_id: 'm-sup',
  })
  await n.peer.call('session.send', {
    session_id: ordPid,
    text: 'Hello ordinary chat',
    assistant: true,
    supervision_instructions: '',
    client_msg_id: 'm-ord',
  })
  await until('both prompts completed', () => received(env.record).filter((m) => m.ended).length >= 2, SLOW)

  const prompts = received(env.record).filter((m) => m.method === 'session/prompt')
  const supPromptText = prompts.find((m) => m.params.sessionId === supRaw)?.params.prompt?.[0]?.text || ''
  const ordPromptText = prompts.find((m) => m.params.sessionId === ordRaw)?.params.prompt?.[0]?.text || ''
  assert.equal(supPromptText.includes('SUPERVISION_ROLE_MARKER'), false, 'policy must never be concatenated into user prompt text')
  assert.equal(supPromptText.includes('Check worker status'), true)
  assert.equal(ordPromptText.includes('SUPERVISION_ROLE_MARKER'), false)
  assert.equal(readFileSync(env.baseAgentsPath, 'utf8'), env.baseAgentsContent)

  // workspacePathOf skips managed policy directories so they are never mistaken for session cwd.
  const fakeSummary = { WorkspaceURIs: [`file://${supPolicyDir}`, `file://${env.assistantDir}`] }
  assert.equal(workspacePathOf(fakeSummary, env.dir, (p) => isPolicyDir(env.policyRoot, p)), env.assistantDir)
  assert.equal(workspacePathOf({ WorkspaceURIs: [`file://${supPolicyDir}`] }, env.dir, (p) => isPolicyDir(env.policyRoot, p)), env.dir)
})

test('dynamic promotion, in-place update, >24KB split, demotion, session.modes preservation, and close cleanup', async (t) => {
  const env = setupAgy(t)
  const { n } = await startRuntime(t, env)

  // Start as an ordinary chat (no policy).
  const { session } = await n.peer.call('session.start', { cwd: env.assistantDir, assistant: true })
  const pid = session.session_id
  const raw = toAgyId(pid)
  const policyDir = policyDirOf(env.policyRoot, raw)
  assert.equal(existsSync(policyDir), false)

  // Promote dynamically on session.send.
  const policyV1 = 'POLICY_V1\nAct as supervisor.'
  await n.peer.call('session.send', { session_id: pid, text: 'turn 1', assistant: true, supervision_instructions: policyV1, client_msg_id: 't1' })
  await until('turn 1 done', () => received(env.record).filter((m) => m.ended === raw).length >= 1, SLOW)
  assert.equal(readPolicy(env.policyRoot, raw), policyV1)
  const afterPromote = received(env.record).filter((m) => m.method === 'session/resume' && m.params.sessionId === raw)
  assert.deepEqual(afterPromote.at(-1)?.params.additionalDirectories, [policyDir])

  // session.modes keeps active additionalDirectories intact.
  await n.peer.call('session.modes', { session_id: pid, permission_mode: 'default' })
  const afterModes = received(env.record).filter((m) => m.method === 'session/resume' && m.params.sessionId === raw)
  assert.deepEqual(afterModes.at(-1)?.params.additionalDirectories, [policyDir])

  // Update with a >24,000-byte CJK + ASCII policy (splits cleanly across AGENTS.md + GEMINI.md).
  const line = '规则行：请先检查任务验收标准再决定下一步。\n'
  const bigPolicy = line.repeat(Math.ceil((AGY_RULE_MAX_BYTES + 4000) / Buffer.byteLength(line, 'utf8')))
  assert.ok(Buffer.byteLength(bigPolicy, 'utf8') > AGY_RULE_MAX_BYTES)
  assert.ok(Buffer.byteLength(bigPolicy, 'utf8') <= AGY_RULE_MAX_BYTES * 2)
  await n.peer.call('session.send', { session_id: pid, text: 'turn 2', assistant: true, supervision_instructions: bigPolicy, client_msg_id: 't2' })
  await until('turn 2 done', () => received(env.record).filter((m) => m.ended === raw).length >= 2, SLOW)
  const primaryBytes = Buffer.byteLength(readFileSync(join(policyDir, 'AGENTS.md'), 'utf8'), 'utf8')
  const secondaryBytes = Buffer.byteLength(readFileSync(join(policyDir, 'GEMINI.md'), 'utf8'), 'utf8')
  assert.ok(primaryBytes > 0 && primaryBytes <= AGY_RULE_MAX_BYTES)
  assert.ok(secondaryBytes > 0 && secondaryBytes <= AGY_RULE_MAX_BYTES)
  assert.equal(readPolicy(env.policyRoot, raw), bigPolicy, 'split policy round-trips byte-for-byte')

  // Shrinking back under 24KB removes secondary GEMINI.md.
  const policyV2 = 'POLICY_V2\nShort again.'
  await n.peer.call('session.send', { session_id: pid, text: 'turn 3', assistant: true, supervision_instructions: policyV2, client_msg_id: 't3' })
  await until('turn 3 done', () => received(env.record).filter((m) => m.ended === raw).length >= 3, SLOW)
  assert.equal(readFileSync(join(policyDir, 'AGENTS.md'), 'utf8'), policyV2)
  assert.equal(existsSync(join(policyDir, 'GEMINI.md')), false, 'secondary file removed once policy fits in primary')

  // Demote back to ordinary chat via empty string: files removed, session reloaded without additionalDirectories.
  const resumesBeforeDemote = received(env.record).filter((m) => m.method === 'session/resume' && m.params.sessionId === raw).length
  await n.peer.call('session.send', { session_id: pid, text: 'turn 4', assistant: true, supervision_instructions: '', client_msg_id: 't4' })
  await until('turn 4 done', () => received(env.record).filter((m) => m.ended === raw).length >= 4, SLOW)
  assert.equal(existsSync(policyDir), false, 'demotion removes the session policy directory')
  const resumesAfterDemote = received(env.record).filter((m) => m.method === 'session/resume' && m.params.sessionId === raw)
  assert.ok(resumesAfterDemote.length > resumesBeforeDemote, 'demotion reloads the session')
  assert.equal(resumesAfterDemote.at(-1)?.params.additionalDirectories, undefined, 'demoted session drops additionalDirectories')

  // Re-promote and verify session.close cleans up the directory.
  await n.peer.call('session.send', { session_id: pid, text: 'turn 5', assistant: true, supervision_instructions: policyV1, client_msg_id: 't5' })
  await until('turn 5 done', () => received(env.record).filter((m) => m.ended === raw).length >= 5, SLOW)
  assert.equal(existsSync(policyDir), true)
  await n.peer.call('session.close', { session_id: pid })
  assert.equal(existsSync(policyDir), false, 'session.close removes the session policy directory')
})

test('busy role change is refused without altering disk policy; same policy while busy still queues', async (t) => {
  const env = setupAgy(t)
  const { n } = await startRuntime(t, env)

  const policyV1 = 'BUSY_POLICY_V1'
  const { session } = await n.peer.call('session.start', {
    cwd: env.assistantDir,
    assistant: true,
    supervision_instructions: policyV1,
  })
  const pid = session.session_id
  const raw = toAgyId(pid)

  // Start a long-running prompt ('hold').
  await n.peer.call('session.send', {
    session_id: pid,
    text: 'hold this turn',
    assistant: true,
    supervision_instructions: policyV1,
    client_msg_id: 'busy-1',
  })
  await until('prompt running', () => received(env.record).some((m) => m.method === 'session/prompt'), SLOW)

  // Attempting to change supervision_instructions while busy is refused with code 'busy'.
  await assert.rejects(
    n.peer.call('session.send', {
      session_id: pid,
      text: 'change policy now',
      assistant: true,
      supervision_instructions: 'BUSY_POLICY_V2',
      client_msg_id: 'busy-change',
    }),
    (err) => err.code === 'busy',
  )
  assert.equal(readPolicy(env.policyRoot, raw), policyV1, 'failed busy change must leave on-disk policy untouched')

  // Sending with the SAME policy while busy queues cleanly without error.
  await n.peer.call('session.send', {
    session_id: pid,
    text: 'follow-up same policy',
    assistant: true,
    supervision_instructions: policyV1,
    client_msg_id: 'busy-same',
  })

  // Interrupt cancels both the held turn and its queued follow-up.
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('session stopped', () => n.last('runtime.sessions')?.sessions.find((row) => row.session_id === pid)?.status !== 'working', SLOW)
  assert.equal(received(env.record).filter((m) => m.ended === raw).length, 1)
  assert.equal(received(env.record).filter((m) => m.method === 'session/prompt').length, 1)
  assert.equal(readPolicy(env.policyRoot, raw), policyV1)
})

test('fail closed when agy acp does not advertise additionalDirectories; ordinary chats still work', async (t) => {
  const env = setupAgy(t, { acpFlags: ['--no-additional-dirs'] })
  const { n } = await startRuntime(t, env)

  // Supervised session.start fails closed with assistant_supervision_runtime and creates no ACP session.
  await assert.rejects(
    n.peer.call('session.start', {
      cwd: env.assistantDir,
      assistant: true,
      supervision_instructions: 'SUPERVISION_ONLY',
    }),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(received(env.record).filter((m) => m.method === 'session/new').length, 0)

  // Ordinary session.start and session.send without policy succeed.
  const { session } = await n.peer.call('session.start', { cwd: env.assistantDir, assistant: true })
  const pid = session.session_id
  const raw = toAgyId(pid)
  await n.peer.call('session.send', {
    session_id: pid,
    text: 'ordinary message',
    assistant: true,
    supervision_instructions: '',
    client_msg_id: 'ord-ok',
  })
  await until('ordinary turn done', () => received(env.record).filter((m) => m.ended === raw).length >= 1, SLOW)

  // Promoting the ordinary session to supervised fails closed without sending session/prompt.
  const promptsBefore = received(env.record).filter((m) => m.method === 'session/prompt').length
  await assert.rejects(
    n.peer.call('session.send', {
      session_id: pid,
      text: 'supervision message',
      assistant: true,
      supervision_instructions: 'SUPERVISION_ONLY',
      client_msg_id: 'sup-fail',
    }),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(received(env.record).filter((m) => m.method === 'session/prompt').length, promptsBefore, 'prompt must never run when policy cannot be applied')
  assert.equal(existsSync(policyDirOf(env.policyRoot, raw)), false)
})

test('fail closed on unwritable policyRoot, oversized policy (>48KB), and roll back disk policy on resume error', async (t) => {
  // 1. Unwritable policyRoot (a regular file instead of a directory).
  const badEnv = setupAgy(t)
  const fileAsRoot = join(badEnv.dir, 'not-a-dir')
  writeFileSync(fileAsRoot, 'blocker')
  badEnv.policyRoot = fileAsRoot
  const { n: badNode } = await startRuntime(t, badEnv)
  await assert.rejects(
    badNode.peer.call('session.start', {
      cwd: badEnv.assistantDir,
      assistant: true,
      supervision_instructions: 'SUPERVISION_POLICY',
    }),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(received(badEnv.record).filter((m) => m.method === 'session/prompt').length, 0)

  // 2. Oversized policy (> 48,000 bytes) fails closed and leaves prior policy intact.
  const sizeEnv = setupAgy(t)
  const { n: sizeNode } = await startRuntime(t, sizeEnv)
  const { session } = await sizeNode.peer.call('session.start', {
    cwd: sizeEnv.assistantDir,
    assistant: true,
    supervision_instructions: 'INITIAL_OK',
  })
  const raw = toAgyId(session.session_id)
  const tooBig = 'x'.repeat(AGY_RULE_MAX_BYTES * 2 + 10)
  await assert.rejects(
    sizeNode.peer.call('session.send', {
      session_id: session.session_id,
      text: 'oversized',
      assistant: true,
      supervision_instructions: tooBig,
      client_msg_id: 'too-big',
    }),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(readPolicy(sizeEnv.policyRoot, raw), 'INITIAL_OK', 'oversized policy leaves previous valid policy intact')
  assert.equal(received(sizeEnv.record).filter((m) => m.method === 'session/prompt').length, 0)

  // 3. Direct syncPolicy rejects path traversal session ids.
  assert.throws(() => syncPolicy(sizeEnv.policyRoot, '../escape', 'bad'), (err) => err.code === SUPERVISION_ERROR_CODE)

  // 4. Failed session/resume during policy change rolls back disk policy and never sends prompt.
  const failEnv = setupAgy(t, { acpFlags: ['--fail-resume'] })
  const { n: failNode } = await startRuntime(t, failEnv)
  await assert.rejects(
    failNode.peer.call('session.start', {
      cwd: failEnv.assistantDir,
      assistant: true,
      supervision_instructions: 'SHOULD_ROLL_BACK',
    }),
    /refused additionalDirectories/,
  )
  const created = received(failEnv.record).find((m) => m.method === 'session/new')
  assert.ok(created)
  assert.equal(received(failEnv.record).filter((m) => m.method === 'session/prompt').length, 0)
  assert.equal(existsSync(join(failEnv.policyRoot, '11111111-2222-4333-8444-000000000001')), false, 'failed start cleans up policy dir')
})


test('queued messages inherit a policy applied while the preceding send prepares', async (t) => {
  const env = setupAgy(t)
  const { n } = await startRuntime(t, env)
  const { session } = await n.peer.call('session.start', { cwd: env.assistantDir, assistant: true })
  const pid = session.session_id
  let release, entered = false, hold = true
  const gate = new Promise((resolve) => { release = resolve })
  const original = AgyAcpClient.prototype.ensureStarted
  const mocked = t.mock.method(AgyAcpClient.prototype, 'ensureStarted', async function () {
    if (hold) { hold = false; entered = true; await gate }
    return original.call(this)
  })
  const first = n.peer.call('session.send', { session_id: pid, text: 'First.', supervision_instructions: 'Review before acting.' })
  try {
    await until('first send preparing', () => entered, SLOW)
    assert.equal((await n.peer.call('session.send', { session_id: pid, text: 'Second.' })).queued, true)
  } finally {
    release()
    mocked.mock.restore()
  }
  await first
  await until('both prompts started', () => received(env.record).filter((call) => call.method === 'session/prompt').length === 2, SLOW)
  const resumes = received(env.record).filter((call) => call.method === 'session/resume')
  assert.deepEqual(resumes.at(-1).params.additionalDirectories, [policyDirOf(env.policyRoot, toAgyId(pid))])
})


test('rejected supervision preparation preserves an idle session without publishing a prompt', async (t) => {
  const env = setupAgy(t, { acpFlags: ['--no-additional-dirs'] })
  const { n } = await startRuntime(t, env)
  const { session } = await n.peer.call('session.start', { cwd: env.assistantDir, assistant: true })
  const pid = session.session_id
  await n.peer.call('session.watch', { session_id: pid })
  const before = await n.peer.call('session.items', { session_id: pid })
  const batches = n.all('session.items').length
  await assert.rejects(n.peer.call('session.send', {
    session_id: pid, text: 'Not accepted.', supervision_instructions: 'Review before acting.',
  }), (err) => err.code === SUPERVISION_ERROR_CODE)
  assert.deepEqual(await n.peer.call('session.items', { session_id: pid }), before)
  const inventories = n.all('runtime.sessions').length
  await n.peer.call('session.rename', { session_id: pid, title: 'Checked' })
  await until('inventory refreshed', () => n.all('runtime.sessions').length > inventories, SLOW)
  const row = n.last('runtime.sessions').sessions.find((row) => row.session_id === pid)
  assert.equal(row.status, 'idle')
  assert.equal(row.turn_error, undefined)
  assert.equal(n.all('session.items').slice(batches).flatMap((batch) => batch.items).some((item) => item.text === 'Not accepted.'), false)
})
