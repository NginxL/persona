import test from 'node:test';
import assert from 'node:assert/strict';
import { boundedTranscriptCache, TRANSCRIPT_CACHE_BYTES, utf8Bytes } from '../entry/src/main/ets/core/TranscriptCache.ts';

const snapshot = items => ({items, covered: 600, history: 'test-history', hasMore: false, loadedOnce: true});
const item = (id, text) => ({id, kind: 'text', role: 'assistant', text});

test('ordinary cached history preserves every item and continuity field', () => {
  const original = snapshot([item('one', 'Hello'), item('two', '世界🌍')]);
  assert.deepEqual(JSON.parse(boundedTranscriptCache(original)), original);
});
test('large multilingual histories retain a bounded complete suffix with older paging available', () => {
  const items = Array.from({length: 500}, (_, i) => item(String(i), '世界🌍'.repeat(1000)));
  const encoded = boundedTranscriptCache(snapshot(items)), saved = JSON.parse(encoded);
  assert.ok(Buffer.byteLength(encoded) <= TRANSCRIPT_CACHE_BYTES);
  assert.ok(saved.items.length > 0 && saved.items.length < items.length);
  assert.deepEqual(saved.items, items.slice(-saved.items.length));
  assert.equal(saved.hasMore, true);
  assert.equal(saved.covered, 600);
});
test('an oversized latest answer is never replaced by an older answer or a truncated result', () => {
  const saved = JSON.parse(boundedTranscriptCache(snapshot([item('old', 'outdated'), item('latest', 'x'.repeat(TRANSCRIPT_CACHE_BYTES))])));
  assert.deepEqual(saved.items, []);
  assert.equal(saved.covered, 0);
  assert.equal(saved.loadedOnce, false);
  assert.equal(saved.hasMore, true);
});
test('cache bounds include escaped JSON and UTF-8 surrogate pairs', () => {
  for (const value of ['ASCII', '中文', '🌍', '\ud800', '"\\\n']) assert.equal(utf8Bytes(value), Buffer.byteLength(value));
  const encoded = boundedTranscriptCache(snapshot(Array.from({length: 400}, (_, i) => item(String(i), '\n"\\🌍'.repeat(200)))));
  assert.ok(Buffer.byteLength(encoded) <= TRANSCRIPT_CACHE_BYTES);
  assert.ok(JSON.parse(encoded).items.length <= 256);
});
