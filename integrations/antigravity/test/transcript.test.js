import assert from 'node:assert/strict'
import { appendFileSync, closeSync, mkdirSync, mkdtempSync, openSync, renameSync, rmSync, writeFileSync, writeSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { startFakeNode } from '../../runtime-sdk/testing/fake-node.js'
import { readTranscript, readTranscriptSteps, rememberObservedWake, stepsToItems } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

function logs(t) {
  const brain = mkdtempSync(join(tmpdir(), 'agy-transcript-'))
  const directory = join(brain, '.system_generated/logs')
  mkdirSync(directory, { recursive: true })
  t.after(() => rmSync(brain, { recursive: true, force: true }))
  return { brain, directory }
}

const record = (content) => JSON.stringify({ step_index: 0, type: 'PLANNER_RESPONSE', content }) + '\n'

test('the complete transcript is authoritative while the compact export is ahead', (t) => {
  const { brain, directory } = logs(t)
  writeFileSync(join(directory, 'transcript.jsonl'), record('compact') + JSON.stringify({ step_index: 1, content: 'next' }) + '\n')
  writeFileSync(join(directory, 'transcript_full.jsonl'), record('complete') + '{"step_index":')
  assert.deepEqual(readTranscriptSteps(brain, true).map((step) => step.content), ['complete'])
  assert.equal(readTranscriptSteps(brain, false).length, 2)
})

test('complete reads fall back to compact-only histories and unavailable full files', (t) => {
  const { brain, directory } = logs(t)
  writeFileSync(join(directory, 'transcript.jsonl'), record('compact-only history'))
  assert.equal(readTranscriptSteps(brain, true)[0].content, 'compact-only history')
  mkdirSync(join(directory, 'transcript_full.jsonl'))
  assert.equal(readTranscriptSteps(brain, true)[0].content, 'compact-only history')
})

test('fresh rewrites bypass the cache and settled records refresh after atomic replacement', async (t) => {
  const { brain, directory } = logs(t)
  const file = join(directory, 'transcript_full.jsonl')
  writeFileSync(file, record('first'))
  assert.equal(readTranscriptSteps(brain, true)[0].content, 'first')
  assert.equal(readTranscriptSteps(brain, true)[0].content, 'first')
  writeFileSync(file, record('other'))
  assert.equal(readTranscriptSteps(brain, true)[0].content, 'other')
  await new Promise((resolve) => setTimeout(resolve, 1100))
  const cached = readTranscriptSteps(brain, true)
  assert.equal(readTranscriptSteps(brain, true), cached, 'settled files reuse parsed records')
  writeFileSync(file + '.new', record('final'))
  renameSync(file + '.new', file)
  assert.equal(readTranscriptSteps(brain, true)[0].content, 'final')
})


test('native replacement records discard the abandoned tail and keep a stable branch identity', (t) => {
  const { brain, directory } = logs(t)
  const file = join(directory, 'transcript_full.jsonl')
  const step = (index, text, at) => JSON.stringify({ step_index: index, type: 'PLANNER_RESPONSE', content: text, created_at: at }) + '\n'
  writeFileSync(file, step(0, 'retained', '2026-01-01') + step(1, 'abandoned', '2026-01-01') + step(2, 'abandoned tail', '2026-01-01'))
  assert.equal(readTranscript(brain, true).branch, '')
  appendFileSync(file, step(1, 'replacement', '2026-01-02'))
  const rewound = readTranscript(brain, true)
  assert.deepEqual(rewound.steps.map((step) => step.content), ['retained', 'replacement'])
  assert.ok(rewound.branch)
  appendFileSync(file, step(1, 'replacement updated', '2026-01-02') + step(2, 'continued', '2026-01-02'))
  const next = readTranscript(brain, true)
  assert.equal(next.branch, rewound.branch, 'updating the same step and continuing keep the branch')
  assert.ok(next.revision, 'a changed native step has a content revision')
  assert.deepEqual(next.steps.map((step) => step.content), ['retained', 'replacement updated', 'continued'])
  appendFileSync(file, step(3, 'ordinary append', '2026-01-02'))
  assert.equal(readTranscript(brain, true).revision, next.revision, 'ordinary appends keep the content revision')
})

test('same-sequence file rewrites update content revisions while ordinary appends keep them', async (t) => {
  const { brain, directory } = logs(t)
  const file = join(directory, 'transcript_full.jsonl')
  writeFileSync(file, record('Pending'))
  const pending = readTranscript(brain, true)
  writeFileSync(file, record('Complete'))
  const complete = readTranscript(brain, true)
  assert.notEqual(complete.revision, pending.revision)
  assert.equal(readTranscript(brain, true).revision, complete.revision)
  for (let i = 0; i < 20; i++) {
    const other = join(directory, 'other-' + i, '.system_generated/logs')
    mkdirSync(other, { recursive: true })
    writeFileSync(join(other, 'transcript_full.jsonl'), record('Other conversation'))
  }
  await new Promise((resolve) => setTimeout(resolve, 1100))
  readTranscript(brain, true)
  for (let i = 0; i < 20; i++) readTranscript(join(directory, 'other-' + i), true)
  assert.equal(readTranscript(brain, true).revision, complete.revision, 'body cache pressure preserves the revision')
  appendFileSync(file, JSON.stringify({ step_index: 1, type: 'PLANNER_RESPONSE', content: 'Next' }) + '\n')
  assert.equal(readTranscript(brain, true).revision, complete.revision)
})

// A parallel call that fails at once (unknown tool, bad arguments) is exported before its sibling's result, so the
// lines arrive as 3 then 2. That is a late arrival, not a rewind: both results belong to the same planner step.
test('parallel results exported out of order are kept in step order and pair with their own calls', (t) => {
  const { brain, directory } = logs(t)
  const file = join(directory, 'transcript.jsonl')
  const line = (step) => JSON.stringify({ source: 'MODEL', status: 'DONE', created_at: '2026-01-01T00:00:00Z', ...step }) + '\n'
  const calls = [{ name: 'run_command', args: { CommandLine: 'tail log' } }, { name: 'manage_task', args: { Action: 'status' } }]
  writeFileSync(file, line({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', content: 'go' }) +
    line({ step_index: 1, type: 'PLANNER_RESPONSE', tool_calls: calls }) +
    line({ step_index: 3, type: 'GENERIC', status: 'INVALID', content: 'Error invalid tool call', error: 'invalid arguments' }))
  const before = readTranscript(brain, false)
  appendFileSync(file, line({ step_index: 2, type: 'GENERIC', content: 'tail output' }) +
    line({ step_index: 4, type: 'PLANNER_RESPONSE', tool_calls: [{ name: 'view_file', args: { AbsolutePath: '/tmp/a' } }] }) +
    line({ step_index: 5, type: 'GENERIC', content: 'file body' }))
  const after = readTranscript(brain, false)
  assert.deepEqual(after.steps.map((step) => step.step_index), [0, 1, 2, 3, 4, 5], 'the late step keeps its place and nothing is dropped')
  assert.equal(after.branch, before.branch, 'a late arrival is not a rewind: item identities stay')
  assert.notEqual(after.revision, before.revision, 'watchers still learn that earlier items changed')
  const { items } = stepsToItems(after.steps)
  const uses = items.filter((item) => item.kind === 'tool_use')
  const results = new Map(items.filter((item) => item.kind === 'tool_result').map((item) => [item.tool_use_id, item]))
  assert.equal(uses.length, 3)
  assert.deepEqual(uses.map((use) => results.get(use.tool_use_id)?.text), ['tail output', 'invalid arguments', 'file body'])
  assert.equal(results.get(uses[1].tool_use_id).is_error, true)
})

test('parsed transcript cache retains a full inventory of 80 sessions', async (t) => {
  const entries = Array.from({ length: 80 }, () => logs(t))
  for (const { directory } of entries) writeFileSync(join(directory, 'transcript_full.jsonl'), record('Cached reply.'))
  await new Promise((resolve) => setTimeout(resolve, 1100))
  const cached = entries.map(({ brain }) => readTranscript(brain, true))
  for (const [index, { brain }] of entries.entries()) assert.equal(readTranscript(brain, true), cached[index])
})

test('incremental append handles large files, partial trailing lines, and preserves prior snapshots', (t) => {
  const { brain, directory } = logs(t)
  const file = join(directory, 'transcript_full.jsonl')
  const pad = 'p'.repeat(600)
  const makeStep = (i, content) => JSON.stringify({
    step_index: i,
    source: i % 2 === 0 ? 'USER_EXPLICIT' : 'MODEL',
    type: i % 2 === 0 ? 'USER_INPUT' : 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: `2026-11-20T10:00:${String(i % 60).padStart(2, '0')}Z`,
    content,
  }) + '\n'

  let initial = ''
  for (let i = 0; i < 12; i++) initial += makeStep(i, `step-${i}-${pad}`)
  // Trailing partial JSON line without newline
  writeFileSync(file, initial + '{"step_index":12,"type":"PLANNER_')
  const first = readTranscript(brain, true)
  assert.equal(first.steps.length, 12)
  // Unsettled unchanged read reuses cached transcript reference via SHA-256 verification
  assert.equal(readTranscript(brain, true), first)

  // Complete the partial line and append step 13 (without trailing newline)
  const step13NoNl = makeStep(13, 'step-13-no-nl').trimEnd()
  appendFileSync(file, 'RESPONSE","status":"DONE","created_at":"2026-11-20T10:01:00Z","content":"step-12-done"}\n' + step13NoNl)
  const second = readTranscript(brain, true)
  assert.equal(first.steps.length, 12, 'prior snapshot array must not be mutated')
  assert.equal(second.steps.length, 14)
  assert.equal(second.steps[12].content, 'step-12-done')
  assert.equal(second.steps[13].content, 'step-13-no-nl')
  assert.equal(second.revision, first.revision)
  assert.equal(second.steps[0], first.steps[0], 'incremental append reuses parsed prefix step objects')

  // Complete newline for step 13 and append step 14
  appendFileSync(file, '\n' + makeStep(14, 'step-14-done'))
  const third = readTranscript(brain, true)
  assert.equal(third.steps.length, 15)
  assert.equal(third.steps[13].content, 'step-13-no-nl')
  assert.equal(third.steps[14].content, 'step-14-done')
  assert.equal(third.revision, first.revision)
})

test('same-size fast rewrite and truncation are always detected even on large unsettled files', (t) => {
  const { brain, directory } = logs(t)
  const file = join(directory, 'transcript_full.jsonl')
  const pad = 'a'.repeat(500)
  const line = (i, tag) => JSON.stringify({
    step_index: i,
    source: 'MODEL',
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-20T10:00:00Z',
    content: `${tag}-${pad}`,
  }) + '\n'

  const rowsA = Array.from({ length: 16 }, (_, i) => line(i, 'ORIG'))
  writeFileSync(file, rowsA.join(''))
  const before = readTranscript(brain, true)
  assert.equal(before.steps[8].content.startsWith('ORIG-'), true)

  // Same-size fast rewrite in the middle of a >8KB file within the same mtime tick
  const rowsB = rowsA.slice()
  rowsB[8] = line(8, 'EDIT')
  assert.equal(Buffer.byteLength(rowsB.join('')), Buffer.byteLength(rowsA.join('')))
  writeFileSync(file, rowsB.join(''))
  const rewritten = readTranscript(brain, true)
  assert.equal(rewritten.steps[8].content.startsWith('EDIT-'), true, 'middle same-size rewrite must not be missed')
  assert.notEqual(rewritten.revision, before.revision)

  // Prefix rewrite + growth (tail changes because middle line grew)
  const rowsC = rowsB.slice()
  rowsC[1] = line(1, 'LONGER_PREFIX_REWRITE')
  rowsC.push(line(16, 'TAIL'))
  writeFileSync(file, rowsC.join(''))
  const grownRewrite = readTranscript(brain, true)
  assert.equal(grownRewrite.steps.length, 17)
  assert.equal(grownRewrite.steps[1].content.startsWith('LONGER_PREFIX_REWRITE-'), true)
  assert.notEqual(grownRewrite.revision, rewritten.revision)

  // Truncation to fewer steps
  writeFileSync(file, rowsA.slice(0, 2).join(''))
  const truncated = readTranscript(brain, true)
  assert.equal(truncated.steps.length, 2)
  assert.notEqual(truncated.revision, grownRewrite.revision)
})

test('incremental append handles rewind replacement and out-of-order late arrival on large files', (t) => {
  const { brain, directory } = logs(t)
  const file = join(directory, 'transcript_full.jsonl')
  const pad = 'z'.repeat(600)
  const line = (step) => JSON.stringify({ source: 'MODEL', status: 'DONE', created_at: '2026-11-20T10:00:00Z', ...step }) + '\n'

  let initial = ''
  for (let i = 0; i < 10; i++) initial += line({ step_index: i, type: 'PLANNER_RESPONSE', content: `base-${i}-${pad}` })
  initial += line({ step_index: 10, type: 'PLANNER_RESPONSE', tool_calls: [{ name: 'run_command', args: { CommandLine: 'a' } }, { name: 'run_command', args: { CommandLine: 'b' } }] })
  initial += line({ step_index: 12, type: 'GENERIC', content: 'second-finished-first' })
  writeFileSync(file, initial)
  const before = readTranscript(brain, true)
  assert.deepEqual(before.steps.slice(-2).map((s) => s.step_index), [10, 12])

  // Out-of-order late arrival of step 11 appended incrementally
  appendFileSync(file, line({ step_index: 11, type: 'GENERIC', content: 'first-arrived-late' }))
  const late = readTranscript(brain, true)
  assert.deepEqual(late.steps.slice(-3).map((s) => s.step_index), [10, 11, 12])
  assert.equal(late.branch, before.branch)
  assert.notEqual(late.revision, before.revision)

  // Rewind replacement appended incrementally
  appendFileSync(file, line({ step_index: 9, type: 'PLANNER_RESPONSE', content: 'rewound-9', created_at: '2026-11-20T10:05:00Z' }))
  const rewound = readTranscript(brain, true)
  assert.equal(rewound.steps.length, 10)
  assert.equal(rewound.steps[9].content, 'rewound-9')
  assert.notEqual(rewound.branch, late.branch)
})

test('wake receipts update cached transcripts without mutating prior snapshots and full/compact flag stays accurate', (t) => {
  const { brain, directory } = logs(t)
  const compactFile = join(directory, 'transcript.jsonl')
  const fullFile = join(directory, 'transcript_full.jsonl')
  const line = (step) => JSON.stringify({ source: 'MODEL', status: 'DONE', ...step }) + '\n'
  writeFileSync(compactFile,
    line({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', created_at: '2026-11-20T10:00:00Z', content: 'go' }) +
    line({ step_index: 1, type: 'PLANNER_RESPONSE', created_at: '2026-11-20T10:00:01Z', content: 'Initial reply.' }) +
    line({ step_index: 2, type: 'PLANNER_RESPONSE', created_at: '2026-11-20T10:05:00Z', content: 'Background follow-up.' }))

  // Reading compact first must not poison compact=true when full=true falls back to transcript.jsonl
  const asCompact = readTranscript(brain, false)
  assert.equal(asCompact.compact, false)
  const asFallbackFull = readTranscript(brain, true)
  assert.equal(asFallbackFull.compact, true)

  // Record an observed wake receipt and verify cached transcript refreshes cleanly
  rememberObservedWake(brain, asFallbackFull.steps, 2)
  const withWake = readTranscript(brain, true)
  assert.match(withWake.revision, /:wake:/)
  assert.equal(stepsToItems(asFallbackFull.steps).items.some((item) => item.wake), false, 'prior snapshot stays unmutated')
  assert.equal(stepsToItems(withWake.steps).items.some((item) => item.wake), true)

  // Switching to transcript_full.jsonl once it appears clears compact
  writeFileSync(fullFile,
    line({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', created_at: '2026-11-20T10:00:00Z', content: 'go' }) +
    line({ step_index: 1, type: 'PLANNER_RESPONSE', created_at: '2026-11-20T10:00:01Z', content: 'Full reply.' }))
  const switched = readTranscript(brain, true)
  assert.equal(switched.compact, false)
  assert.equal(switched.steps[1].content, 'Full reply.')
})

test('stepsToItems caches exact hits, reuses immutable prefixes on append, and invalidates on option or live mutations', (t) => {
  const { brain, directory } = logs(t)
  const file = join(directory, 'transcript_full.jsonl')
  const line = (step) => JSON.stringify({ status: 'DONE', created_at: '2026-11-20T10:00:00Z', ...step }) + '\n'
  writeFileSync(file,
    line({ step_index: 0, source: 'USER_EXPLICIT', type: 'USER_INPUT', content: 'hello' }) +
    line({ step_index: 1, source: 'MODEL', type: 'PLANNER_RESPONSE', content: 'world' }))

  const tr1 = readTranscript(brain, true)
  const key = `test:${brain}:base`
  const m1 = stepsToItems(tr1.steps, { brainDir: brain, full: false, includeMedia: true, cacheKey: key })
  const m1Repeat = stepsToItems(tr1.steps, { brainDir: brain, full: false, includeMedia: true, cacheKey: key })
  assert.equal(m1Repeat, m1, 'exact cacheKey hit returns cached result')

  // Direct WeakMap cache on immutable readTranscript steps
  const direct1 = stepsToItems(tr1.steps, { full: false })
  const direct2 = stepsToItems(tr1.steps, { full: false })
  assert.equal(direct2, direct1)
  const directFull = stepsToItems(tr1.steps, { full: true })
  assert.notEqual(directFull, direct1)

  // Incremental append reuses prefix item objects
  appendFileSync(file, line({ step_index: 2, source: 'MODEL', type: 'PLANNER_RESPONSE', content: 'more' }))
  const tr2 = readTranscript(brain, true)
  const m2 = stepsToItems(tr2.steps, { brainDir: brain, full: false, includeMedia: true, cacheKey: key })
  assert.equal(m2.items.length, 3)
  assert.equal(m2.items[0], m1.items[0], 'prefix items are reused on append')
  assert.equal(m2.items[1], m1.items[1])
  assert.equal(m2.items[2].text, 'more')

  // Live trailing step mutated in place is never served stale when immutablePrefix bounds the snapshot
  const liveStep = { step_index: 3, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'RUNNING', created_at: '2026-11-20T10:01:00Z', thinking: 'part 1' }
  const liveKey = `test:${brain}:live`
  const live1 = stepsToItems([...tr2.steps, liveStep], { brainDir: brain, full: false, includeMedia: true, cacheKey: liveKey, immutablePrefix: tr2.steps.length })
  assert.equal(live1.items.at(-1).text, 'part 1')
  liveStep.thinking += ' + part 2'
  const live2 = stepsToItems([...tr2.steps, liveStep], { brainDir: brain, full: false, includeMedia: true, cacheKey: liveKey, immutablePrefix: tr2.steps.length })
  assert.equal(live2.items.at(-1).text, 'part 1 + part 2')

  // Different cacheKey for clientMsgId version invalidates cleanly
  const withClientId = stepsToItems(tr2.steps, {
    brainDir: brain,
    full: false,
    includeMedia: true,
    cacheKey: `${key}:m1`,
    clientMsgIdFor: (_idx, _text, ordinal) => (ordinal === 1 ? 'cmsg-1' : undefined),
  })
  assert.equal(withClientId.items[0].client_msg_id, 'cmsg-1')
})

test('concurrent session.items and session.cursor calls coalesce and return consistent paginated results', async (t) => {
  const rawId = '22222222-3333-4444-8555-666666666666'
  const sessionId = `agy-${rawId}`
  const dir = mkdtempSync(join(tmpdir(), 'agy-conc-'))
  const logsDir = join(dir, 'config', 'brain', rawId, '.system_generated', 'logs')
  mkdirSync(logsDir, { recursive: true })
  const file = join(logsDir, 'transcript_full.jsonl')
  const lines = []
  for (let i = 0; i < 40; i++) {
    lines.push(JSON.stringify({
      step_index: i,
      source: i % 2 === 0 ? 'USER_EXPLICIT' : 'MODEL',
      type: i % 2 === 0 ? 'USER_INPUT' : 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: `2026-11-20T10:00:${String(i).padStart(2, '0')}Z`,
      content: `msg-${i}`,
    }))
  }
  writeFileSync(file, lines.join('\n') + '\n')

  const sockOpts = process.platform === 'win32'
    ? { path: `\\\\.\\pipe\\agy-conc-${process.pid}-${Date.now()}` }
    : {}
  const node = await startFakeNode(t, sockOpts)
  const runtime = createAgyRuntime({
    agyBin: join(dir, 'missing-agy'),
    agyDir: join(dir, 'config'),
    home: dir,
    socketPath: node.path,
    log: () => {},
  })
  t.after(async () => {
    await runtime.stop()
    rmSync(dir, { recursive: true, force: true })
  })
  runtime.start()
  await node.ready

  // Fire concurrent session.items (with distinct pagination params) and session.watch (cursor) calls
  const [p1, p2, p3, p4, w1, w2] = await Promise.all([
    node.peer.call('session.items', { session_id: sessionId, limit: 10 }),
    node.peer.call('session.items', { session_id: sessionId, turns: 2 }),
    node.peer.call('session.items', { session_id: sessionId, after_seq: 35 }),
    node.peer.call('session.items', { session_id: sessionId, limit: 50 }),
    node.peer.call('session.watch', { session_id: sessionId }),
    node.peer.call('session.watch', { session_id: sessionId }),
  ])
  assert.equal(p1.items.length, 10)
  assert.equal(p1.to_seq, 40)
  assert.equal(p2.items.length, 4)
  assert.equal(p3.items.length, 5)
  assert.equal(p4.items.length, 40)
  assert.equal(w1.ok, true)
  assert.equal(w2.ok, true)

  // Append a new step and verify subsequent concurrent reads reflect the append immediately
  appendFileSync(file, JSON.stringify({
    step_index: 40,
    source: 'MODEL',
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-20T10:01:00Z',
    content: 'msg-40',
  }) + '\n')
  const [afterPage, fullItem] = await Promise.all([
    node.peer.call('session.items', { session_id: sessionId, after_seq: 39 }),
    node.peer.call('session.item', { session_id: sessionId, item_id: p4.items[0].id }),
  ])
  assert.deepEqual(afterPage.items.map((it) => it.text), ['msg-39', 'msg-40'])
  assert.equal(fullItem.text, 'msg-0')
  await node.peer.call('session.unwatch', { session_id: sessionId })
})

test('10MB transcript incremental append and cached stepsToItems stay in millisecond range', (t) => {
  const { brain, directory } = logs(t)
  const file = join(directory, 'transcript_full.jsonl')
  const payload = 'x'.repeat(2700)
  const lines = []
  for (let i = 0; i < 3500; i++) {
    const type = i % 10 === 0 ? 'USER_INPUT' : i % 2 === 1 ? 'PLANNER_RESPONSE' : 'GENERIC'
    const step = type === 'USER_INPUT'
      ? { step_index: i, source: 'USER_EXPLICIT', type, status: 'DONE', created_at: '2026-11-20T10:00:00Z', content: `<USER_REQUEST>\nPrompt ${i}\n${payload.slice(0, 400)}\n</USER_REQUEST>` }
      : type === 'PLANNER_RESPONSE'
        ? { step_index: i, source: 'MODEL', type, status: 'DONE', created_at: '2026-11-20T10:00:01Z', thinking: payload, content: `Reply ${i} ${payload.slice(0, 500)}`, tool_calls: [{ name: 'run_command', args: { CommandLine: `echo ${i}`, toolSummary: `Echo ${i}` } }] }
        : { step_index: i, source: 'MODEL', type, status: 'DONE', created_at: '2026-11-20T10:00:02Z', content: `Created At: 2026-11-20T10:00:02Z\nOutput:\n${payload}` }
    lines.push(JSON.stringify(step))
  }
  const raw = lines.join('\n') + '\n'
  assert.ok(Buffer.byteLength(raw) >= 9 * 1024 * 1024, 'fixture should be ~10MB')
  writeFileSync(file, raw)

  const key = `bench:${brain}`
  const cold = readTranscript(brain, true)
  assert.equal(cold.steps.length, 3500)
  const mappedCold = stepsToItems(cold.steps, { brainDir: brain, full: false, includeMedia: true, cacheKey: key })
  assert.equal(mappedCold.items.length, 7000)

  // Unsettled unchanged read + cached stepsToItems
  const tUnchanged = performance.now()
  const unchanged = readTranscript(brain, true)
  const mappedUnchanged = stepsToItems(unchanged.steps, { brainDir: brain, full: false, includeMedia: true, cacheKey: key })
  const unchangedMs = performance.now() - tUnchanged
  assert.equal(unchanged, cold)
  assert.equal(mappedUnchanged, mappedCold)

  // Incremental append (+1 step) + incremental stepsToItems
  appendFileSync(file, JSON.stringify({
    step_index: 3500,
    source: 'MODEL',
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-20T10:00:03Z',
    content: 'Incremental reply 3500',
  }) + '\n')
  const tAppend = performance.now()
  const appended = readTranscript(brain, true)
  const mappedAppended = stepsToItems(appended.steps, { brainDir: brain, full: false, includeMedia: true, cacheKey: key })
  const appendMs = performance.now() - tAppend
  assert.equal(appended.steps.length, 3501)
  assert.equal(mappedAppended.items.length, 7001)
  assert.equal(mappedAppended.items[0], mappedCold.items[0])
  assert.equal(mappedAppended.items.at(-1).text, 'Incremental reply 3500')
  assert.ok(unchangedMs < 500, `unsettled unchanged read took ${unchangedMs.toFixed(1)}ms`)
  assert.ok(appendMs < 500, `incremental append took ${appendMs.toFixed(1)}ms`)
})

test('itemsCache replaces older history keys in the same session slot and enforces the 64MB byte budget', () => {
  const step = (i, content) => ({
    step_index: i,
    source: 'MODEL',
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-20T10:00:00Z',
    content,
  })

  // 1. Same-slot replacement when history/revision/wake changes
  const slot = 'slot-test-session|0|1'
  const s1 = [step(0, 'first version')]
  const s2 = [step(0, 'second version after revision or wake')]
  const r1 = stepsToItems(s1, { full: false, includeMedia: true, cacheSlot: slot, cacheKey: `${slot}|h1` })
  assert.equal(stepsToItems(s1, { full: false, includeMedia: true, cacheSlot: slot, cacheKey: `${slot}|h1` }), r1, 'exact hit before slot replacement')

  const r2 = stepsToItems(s2, { full: false, includeMedia: true, cacheSlot: slot, cacheKey: `${slot}|h2` })
  assert.equal(stepsToItems(s2, { full: false, includeMedia: true, cacheSlot: slot, cacheKey: `${slot}|h2` }), r2, 'new key is cached')
  const r1Reload = stepsToItems(s1, { full: false, includeMedia: true, cacheSlot: slot, cacheKey: `${slot}|h1` })
  assert.notEqual(r1Reload, r1, 'old history key in the same slot must be evicted immediately')

  // 2. Byte-budget eviction (each entry ~36MB via step thinking UTF-16 weight; two exceed 64MB budget)
  const bigStep = (ch, len) => ({
    step_index: 0,
    source: 'MODEL',
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-20T10:00:00Z',
    thinking: ch.repeat(len),
  })
  const bigA = [bigStep('a', 18 * 1024 * 1024)]
  const bigB = [bigStep('b', 18 * 1024 * 1024)]
  const resA = stepsToItems(bigA, { full: false, cacheSlot: 'big-slot-a|0|0', cacheKey: 'big-slot-a|0|0|h1' })
  assert.equal(stepsToItems(bigA, { full: false, cacheSlot: 'big-slot-a|0|0', cacheKey: 'big-slot-a|0|0|h1' }), resA, '36MB entry fits under 64MB budget')

  const resB = stepsToItems(bigB, { full: false, cacheSlot: 'big-slot-b|0|0', cacheKey: 'big-slot-b|0|0|h1' })
  assert.equal(stepsToItems(bigB, { full: false, cacheSlot: 'big-slot-b|0|0', cacheKey: 'big-slot-b|0|0|h1' }), resB, 'newer 36MB entry is retained')
  const resAReload = stepsToItems(bigA, { full: false, cacheSlot: 'big-slot-a|0|0', cacheKey: 'big-slot-a|0|0|h1' })
  assert.notEqual(resAReload, resA, 'older 36MB entry must be evicted when total exceeds 64MB budget')

  // 3. Single entry exceeding 64MB budget (~66MB) is never retained in itemsCache
  const oversize = [bigStep('c', 33 * 1024 * 1024)]
  const resOver1 = stepsToItems(oversize, { full: false, cacheSlot: 'big-slot-c|0|0', cacheKey: 'big-slot-c|0|0|h1' })
  const resOver2 = stepsToItems(oversize, { full: false, cacheSlot: 'big-slot-c|0|0', cacheKey: 'big-slot-c|0|0|h1' })
  assert.notEqual(resOver2, resOver1, 'oversize entry above 64MB budget must not be cached')
})

test('same-inode growth detects an in-place same-length prefix edit anywhere in the file before append', (t) => {
  const { brain, directory } = logs(t)
  const file = join(directory, 'transcript_full.jsonl')
  const line = (i, tag) => JSON.stringify({
    step_index: i,
    source: 'MODEL',
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-20T10:00:00Z',
    content: `${tag}-${String(i).padStart(3, '0')}`,
  }) + '\n'

  const rows = Array.from({ length: 200 }, (_, i) => line(i, 'ORIG'))
  const initial = rows.join('')
  assert.ok(Buffer.byteLength(initial) > 20 * 1024, 'fixture should exceed 20KB')
  writeFileSync(file, initial)

  const before = readTranscript(brain, true)
  assert.equal(before.steps.length, 200)
  assert.equal(before.steps[60].content, 'ORIG-060')

  // In-place same-length overwrite of step 60 (outside head/mid/tail windows) followed by appending step 200
  const offset60 = Buffer.byteLength(rows.slice(0, 60).join(''))
  const edited60 = Buffer.from(line(60, 'EDIT'))
  assert.equal(edited60.length, Buffer.byteLength(rows[60]))
  const fd = openSync(file, 'r+')
  try {
    writeSync(fd, edited60, 0, edited60.length, offset60)
  } finally {
    closeSync(fd)
  }
  appendFileSync(file, line(200, 'TAIL'))

  const after = readTranscript(brain, true)
  assert.equal(after.steps.length, 201)
  assert.equal(after.steps[60].content, 'EDIT-060', 'in-place edit at step 60 must not be hidden by growth path')
  assert.equal(after.steps[200].content, 'TAIL-200')
  assert.notEqual(after.revision, before.revision)
})
