import test from 'node:test';
import assert from 'node:assert/strict';
import {writeResultUnknown} from '../entry/src/main/ets/core/TransportPolicy.ts';
test('a lost dispatched write stays unknown even after closing the connection', () => {
 for (const status of [0,202,302,408,429,500,502,503,504]) assert.equal(writeResultUnknown(status),true);
});
test('only explicit refusal evidence permits a new operation', () => {
 for (const status of [400,401,403,404,405,409,410,413,415,422]) assert.equal(writeResultUnknown(status),false);
 assert.equal(writeResultUnknown(503,'{"delivered":"none"}'),false);
});
test('unknown or pending receipt evidence takes precedence over HTTP status', () => {
 for (const status of [200,400,409,503]) {
  assert.equal(writeResultUnknown(status,'{"state":"unknown"}'),true);
  assert.equal(writeResultUnknown(status,'{"state":"pending","delivered":"none"}'),true);
  assert.equal(writeResultUnknown(status,'{"delivered":"unknown"}'),true);
  assert.equal(writeResultUnknown(status,'{"delivered":"yes"}'),true);
 }
});
