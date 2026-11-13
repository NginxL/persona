import test from 'node:test';
import assert from 'node:assert/strict';
import { reconcileJsonProjection } from '../entry/src/main/ets/core/JsonProjection.ts';

test('same-key updates retain section, field and nested control objects without mutating the source', () => {
  const displayed = {};
  const first = { sections: [{ key: 'main', items: [
    { key: 'note', type: 'field', value: '' },
    { key: 'toggle', type: 'row', control: { kind: 'switch', on: true } },
    { key: 'receipt', type: 'text', text: '0' }
  ] }] };
  reconcileJsonProjection(displayed, first);
  const section = displayed.sections[0], field = section.items[0], control = section.items[1].control, receipt = section.items[2];
  const next = structuredClone(first); next.sections[0].items[1].control.on = false; next.sections[0].items[2].text = '1';
  reconcileJsonProjection(displayed, next);
  assert.equal(displayed.sections[0], section); assert.equal(section.items[0], field);
  assert.equal(section.items[1].control, control); assert.equal(control.on, false);
  assert.equal(section.items[2], receipt); assert.equal(receipt.text, '1');
  assert.equal(first.sections[0].items[2].text, '0'); assert.equal(next.sections[0].items[2].text, '1');
  assert.notEqual(receipt, next.sections[0].items[2]);
});

test('keyed reorder, deletion and insertion preserve only the matching native identities', () => {
  const displayed = { items: [{ key: 'a', title: 'A' }, { key: 'b', title: 'B' }] };
  const a = displayed.items[0], b = displayed.items[1], array = displayed.items;
  reconcileJsonProjection(displayed, { items: [{ key: 'b', title: 'Updated B' }, { key: 'c', title: 'C' }, { key: 'a', title: 'A' }] });
  assert.equal(displayed.items, array); assert.equal(array[0], b); assert.equal(array[2], a); assert.equal(b.title, 'Updated B');
  reconcileJsonProjection(displayed, { items: [{ key: 'a', title: 'A' }] });
  assert.equal(array.length, 1); assert.equal(array[0], a);
});

test('removed properties and nested table values reflect the latest snapshot', () => {
  const displayed = { action: { id: 'old', confirm: 'obsolete' }, rows: [['before', 1]], type: 'text' };
  const action = displayed.action, row = displayed.rows[0];
  reconcileJsonProjection(displayed, { action: { id: 'new' }, rows: [['after', 2]], type: 'field' });
  assert.equal(displayed.action, action); assert.equal('confirm' in action, false);
  assert.equal(displayed.rows[0], row); assert.deepEqual(row, ['after', 2]); assert.equal(displayed.type, 'field');
});

test('duplicate keys do not alias records and prototype names cannot escape the projection', () => {
  const displayed = { items: [{ key: 'same', text: 'a' }, { key: 'same', text: 'b' }] };
  reconcileJsonProjection(displayed, { items: [{ key: 'same', text: 'first' }, { key: 'same', text: 'second' }] });
  assert.notEqual(displayed.items[0], displayed.items[1]); assert.equal(displayed.items[0].text, 'first');
  const input = JSON.parse('{"__proto__":{"polluted":true},"constructor":"bad","nested":{"prototype":1,"safe":2}}');
  reconcileJsonProjection(displayed, input);
  assert.equal(Object.getPrototypeOf(displayed), Object.prototype); assert.equal({}.polluted, undefined);
  assert.deepEqual(Object.keys(displayed), ['nested']); assert.deepEqual(displayed.nested, { safe: 2 });
});

test('unkeyed display values keep index identity while choices and file entries follow their native keys', () => {
  const displayed = { sections: [{ items: [{ type: 'row', value: 'before', choices: [
    { value: 'a', label: 'A' }, { value: 'b', label: 'B' }
  ], entries: [{ path: 'a.txt' }, { path: 'b.txt' }] }] }] };
  const row = displayed.sections[0].items[0], a = row.choices[0], b = row.choices[1], file = row.entries[1];
  reconcileJsonProjection(displayed, { sections: [{ items: [{ type: 'row', value: 'after', choices: [
    { value: 'b', label: 'New B' }, { value: 'a', label: 'A' }
  ], entries: [{ path: 'b.txt', additions: 2 }] }] }] });
  assert.equal(displayed.sections[0].items[0], row); assert.equal(row.value, 'after');
  assert.equal(row.choices[0], b); assert.equal(row.choices[1], a); assert.equal(b.label, 'New B');
  assert.equal(row.entries[0], file); assert.equal(file.additions, 2);
});

test('oversized or excessively nested trees are refused before altering the presented tree', () => {
  const displayed = { sections: [] }; let nested = {};
  for (let index = 0; index < 70; index++) nested = { next: nested };
  assert.throws(() => reconcileJsonProjection(displayed, nested), /projection_limit/);
  assert.deepEqual(displayed, { sections: [] });
  assert.throws(() => reconcileJsonProjection(displayed, { values: Array(50001).fill(0) }), /projection_limit/);
  assert.deepEqual(displayed, { sections: [] });
});
