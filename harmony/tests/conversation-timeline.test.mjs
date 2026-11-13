import test from 'node:test';
import assert from 'node:assert/strict';
import {mergeConversationTimeline} from '../entry/src/main/ets/core/ConversationTimeline.ts';
const message=(id,role,at)=>({id,role,at});
test('plans follow the complete answer even when created during its streamed text',()=>{
 const entries=mergeConversationTimeline([message('ask','user',10),message('work','work',12),message('answer','assistant',20),message('next','user',30)],[{id:'plan',at:15}]);
 assert.deepEqual(entries.map(row=>row.id),['message:ask','message:work','message:answer','feed:plan','message:next']);
});
test('late receipts stay after the last exchange and input ties stay together',()=>{
 const entries=mergeConversationTimeline([message('text','user',10),message('image','user',10),message('answer','assistant',20)],[{id:'same-time',at:10},{id:'receipt',at:40}]);
 assert.deepEqual(entries.map(row=>row.id),['message:text','message:image','message:answer','feed:same-time','feed:receipt']);
});
test('overlapping feed pages upsert identities in stable time order',()=>{
 const entries=mergeConversationTimeline([message('next','user',30)],[{id:'b',at:20},{id:'a',at:20},{id:'b',at:20}]);
 assert.deepEqual(entries.map(row=>row.cardId).filter(Boolean),['a','b']);
});
test('a cached message with no clock does not split an exchange around a card',()=>{
 const entries=mergeConversationTimeline([message('cached','user',0),message('answer','assistant',0)],[{id:'receipt',at:10}]);
 assert.deepEqual(entries.map(row=>row.id),['message:cached','message:answer','feed:receipt']);
});
