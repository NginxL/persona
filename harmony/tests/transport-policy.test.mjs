import test from 'node:test';
import assert from 'node:assert/strict';
import * as transport from '../entry/src/main/ets/core/TransportPolicy.ts';
const {heldReceipt202, isAgentMessagePost, writeResultUnknown} = transport;

test('only a matching held Agent receipt can acknowledge a 202 session send', () => {
  const path = '/api/v1/sessions/my%20laptop/chat%2Fone/messages';
  const reply = {delivered:'later', undelivered:{id:'operation',state:'waiting',computer:'Desk'}};
  assert.ok(isAgentMessagePost('POST', path));
  assert.ok(!isAgentMessagePost('GET', path));
  assert.ok(!isAgentMessagePost('POST', path + '/retry'));
  assert.equal(heldReceipt202(JSON.stringify(reply),'operation'), 'held');
  for (const value of ['{}','bad',JSON.stringify({...reply,undelivered:{...reply.undelivered,id:'other'}}),JSON.stringify({state:'pending'}),
    JSON.stringify({...reply,undelivered:{...reply.undelivered,computer:''}})]) assert.equal(heldReceipt202(value,'operation'),'pending');
  assert.equal(heldReceipt202('{"state":"unknown"}','operation'),'unknown');
});
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
