import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createRuntime, HISTORY_LIMIT, HISTORY_MAX, historyPage } from '../index.js'
import { startFakeNode } from '../testing/fake-node.js'

const day = 24 * 60 * 60 * 1000
const base = Date.parse('2026-11-19T00:00:00Z')
const iso = (ms) => new Date(ms).toISOString()
// 450 sessions, one every 4 hours back from base: about 75 days, so a 90-day window holds them all.
const rows = Array.from({ length: 450 }, (_, i) => ({ session_id: `s${String(i).padStart(3, '0')}`, status: i % 3 ? 'offline' : 'done', updated_at: iso(base - i * 4 * 60 * 60 * 1000), archived: i % 7 === 0 }))

function all(list, params) {
  const out = []
  let cursor = ''
  for (let i = 0; i < 100; i++) {
    const page = historyPage(list, { ...params, cursor })
    out.push(...page.rows)
    if (!page.next_cursor) return { rows: out, pages: i + 1, complete: page.complete }
    cursor = page.next_cursor
  }
  throw new Error('no last page')
}

test('historyPage pages a window newest first until every row in it is listed once', () => {
  const since = iso(base - 90 * day)
  const got = all(rows, { since, until: iso(base + 1), limit: 100 })
  assert.equal(got.pages, 5)
  assert.equal(got.rows.length, 450)
  assert.equal(new Set(got.rows.map((r) => r.session_id)).size, 450, 'no row twice')
  assert.deepEqual(got.rows.map((r) => r.session_id), rows.map((r) => r.session_id), 'newest first')
  assert.equal(got.complete, true)
  // [since, until): the session at until is out, the one at since in.
  const w = historyPage(rows, { since: rows[10].updated_at, until: rows[2].updated_at, limit: 500 })
  assert.deepEqual(w.rows.map((r) => r.session_id), ['s003', 's004', 's005', 's006', 's007', 's008', 's009', 's010'])
  assert.equal(w.next_cursor, '')
})

test('historyPage breaks ties by session id, takes started_at when updated_at is missing and clamps the limit', () => {
  const t = iso(base - day)
  const tied = ['c', 'a', 'b', 'e', 'd'].map((id) => ({ session_id: id, updated_at: t }))
  const first = historyPage(tied, { since: iso(base - 2 * day), until: iso(base), limit: 2 })
  assert.deepEqual(first.rows.map((r) => r.session_id), ['a', 'b'])
  const second = historyPage(tied, { since: iso(base - 2 * day), until: iso(base), limit: 2, cursor: first.next_cursor })
  assert.deepEqual(second.rows.map((r) => r.session_id), ['c', 'd'])
  const third = historyPage(tied, { since: iso(base - 2 * day), until: iso(base), limit: 2, cursor: second.next_cursor })
  assert.deepEqual(third.rows.map((r) => r.session_id), ['e'])
  assert.equal(third.next_cursor, '')

  const started = historyPage([{ session_id: 'x', started_at: t }, { session_id: 'y' }], { since: iso(base - 2 * day), until: iso(base) })
  assert.deepEqual(started.rows.map((r) => r.session_id), ['x'], 'a row with no time is in no window')

  assert.equal(historyPage(rows, { since: iso(base - 90 * day), until: iso(base + 1) }).rows.length, HISTORY_LIMIT)
  assert.equal(historyPage(rows, { since: iso(base - 90 * day), until: iso(base + 1), limit: 10_000 }).rows.length, HISTORY_MAX - 50, 'all 450')
  assert.equal(historyPage(rows, { since: iso(base - 90 * day), until: iso(base + 1) }, { complete: false }).complete, false)
  assert.throws(() => historyPage(rows, {}), /since/)
  assert.throws(() => historyPage(rows, { since: 'yesterday' }), /since/)
  assert.throws(() => historyPage(rows, { since: t, cursor: 'nope' }), /cursor/)
})

test('a driver with listHistory declares caps.history and answers sessions.history through rt.history', async (t) => {
  const n = await startFakeNode(t)
  let seen
  let rt
  const driver = {
    listSessions: async () => rt.recent(rows),
    items: async () => ({ items: [], to_seq: 0 }),
    listHistory: async (params) => {
      seen = params
      return rt.history(rows, params)
    },
  }
  rt = createRuntime(driver, { runtime: 'toy', socketPath: n.path, retryMs: 20, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  assert.equal(n.hello.caps.history, true)

  const since = iso(base - 90 * day)
  const got = []
  let cursor = ''
  let pages = 0
  do {
    const page = await n.peer.call('sessions.history', { since, until: iso(base + 1), cursor, limit: 200 })
    got.push(...page.rows)
    assert.equal(page.complete, true)
    cursor = page.next_cursor
    pages++
  } while (cursor)
  assert.equal(pages, 3)
  assert.equal(got.length, 450, 'every session in 90 days, far past rt.recent\'s 7 days / 80')
  assert.ok(got.some((r) => r.archived), 'archived sessions too')

  // until left out: the driver gets now, and the limit is clamped to 500.
  await n.peer.call('sessions.history', { since, limit: 9999 })
  assert.equal(seen.limit, 500)
  assert.ok(Math.abs(Date.parse(seen.until) - Date.now()) < 60_000)
  await assert.rejects(n.peer.call('sessions.history', { until: iso(base) }), /since/)
})

test('a driver without listHistory does not declare caps.history and refuses sessions.history as unsupported', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime({ listSessions: async () => [], items: async () => ({ items: [], to_seq: 0 }) }, { runtime: 'toy', socketPath: n.path, retryMs: 20, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  assert.equal(n.hello.caps.history, undefined)
  await assert.rejects(n.peer.call('sessions.history', { since: iso(base - day) }), /unsupported/)
})

test('a driver answering by hand is normalized: rows without an id dropped, complete only when it says so', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime({
    listSessions: async () => [],
    items: async () => ({ items: [], to_seq: 0 }),
    listHistory: async () => ({ rows: [{ session_id: 'a' }, { title: 'no id' }, null] }),
  }, { runtime: 'toy', socketPath: n.path, retryMs: 20, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  assert.deepEqual(await n.peer.call('sessions.history', { since: iso(base - day) }), { rows: [{ session_id: 'a' }], next_cursor: '', complete: false })
})

test('historyPage windows and orders by active_at before updated_at, and leaves deleted sessions out', () => {
  const since = iso(base - 10 * day)
  const until = iso(base)
  const list = [
    // Archived today, last active 20 days ago: archiving moved its updated_at, not when it was used.
    { session_id: 'archived-old', archived: true, updated_at: iso(base - day), active_at: iso(base - 20 * day) },
    // Archived today, last active 5 days ago: in the window, and older than the next one.
    { session_id: 'archived-in', archived: true, updated_at: iso(base - day), active_at: iso(base - 5 * day), origin: 'automation', user_messages: 0 },
    { session_id: 'plain', updated_at: iso(base - 2 * day), origin: 'user', user_messages: 3 },
    { session_id: 'gone', status: 'deleted', updated_at: iso(base - 2 * day) },
    { session_id: 'gone-too', deleted: true, active_at: iso(base - 3 * day) },
  ]
  const page = historyPage(list, { since, until })
  assert.deepEqual(page.rows.map((r) => r.session_id), ['plain', 'archived-in'])
  assert.equal(page.rows[1].archived, true, 'archived ones are listed, as archived')
  assert.deepEqual([page.rows[1].origin, page.rows[1].user_messages, page.rows[0].active_at], ['automation', 0, undefined], 'rows go out as given')
})
