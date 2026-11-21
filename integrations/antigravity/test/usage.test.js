import assert from 'node:assert/strict'
import { test } from 'node:test'
import { usageAccountFromQuota } from '../lib/usage.js'

const sampledAt = new Date('2026-11-20T23:00:00Z')

test('native quota buckets become sorted 5h and weekly usage windows', () => {
  const account = usageAccountFromQuota({ response: {
    buckets: [{ bucketId: 'gemini-weekly', window: '604800s', remainingFraction: 0.25, resetTime: '2026-11-27T23:00:00Z' }],
    groups: [{ displayName: 'Third party', buckets: [
      { bucketId: '3p-5h', window: '18000s', remainingFraction: 0.75, resetTime: '2026-11-21T04:00:00Z' },
      { bucketId: '3p-flash-5h', window: '18000s', modelId: 'gemini-flash', remainingFraction: 0.5, resetTime: '2026-11-21T04:00:00Z' },
    ] }],
  } }, sampledAt)
  assert.deepEqual(account, {
    provider: 'agy', name: 'Antigravity', sampled_at: '2026-11-20T23:00:00.000Z',
    windows: [
      { label: '5h', window_minutes: 300, used_percent: 25, resets_at: '2026-11-21T04:00:00.000Z', model: 'Third party' },
      { label: '5h', window_minutes: 300, used_percent: 50, resets_at: '2026-11-21T04:00:00.000Z', model: 'gemini-flash' },
      { label: '7d', window_minutes: 10080, used_percent: 75, resets_at: '2026-11-27T23:00:00.000Z' },
    ],
  })
})

test('bucket ids supply legacy window lengths and fractions are clamped', () => {
  const account = usageAccountFromQuota({ response: { buckets: [
    { bucketId: '3p-5h', remainingFraction: 1.2 },
    { bucketId: 'gemini-weekly', remainingFraction: -0.2 },
  ] } }, sampledAt)
  assert.deepEqual(account.windows, [
    { label: '5h', window_minutes: 300, used_percent: 0 },
    { label: '7d', window_minutes: 10080, used_percent: 100 },
  ])
})

test('empty, disabled, expired, amount-only and unknown buckets produce no window', () => {
  for (const response of [
    {},
    { buckets: [{ bucketId: '3p-5h', remainingFraction: 0.5, disabled: true }] },
    { buckets: [{ bucketId: '3p-5h', remainingAmount: 12 }] },
    { buckets: [{ bucketId: 'credits', remainingFraction: 0.5 }] },
    { buckets: [{ bucketId: '3p-5h', remainingFraction: 0.5, resetTime: '2026-11-20T22:59:59Z' }] },
  ]) {
    assert.deepEqual(usageAccountFromQuota({ response }, sampledAt).windows, [])
  }
  assert.throws(() => usageAccountFromQuota({}, sampledAt), TypeError)
})

test('malformed native bucket collections are rejected', () => {
  for (const response of [
    { buckets: null },
    { buckets: 'bad' },
    { groups: null },
    { groups: {} },
    { groups: [{ buckets: null }] },
    { groups: [{ buckets: 'bad' }] },
  ]) {
    assert.throws(() => usageAccountFromQuota({ response }, sampledAt), TypeError)
  }
})
