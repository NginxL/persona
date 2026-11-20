import assert from 'node:assert/strict'
import { appendFileSync, mkdirSync, mkdtempSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { readTranscript, readTranscriptSteps, stepsToItems } from '../lib/map.js'

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
