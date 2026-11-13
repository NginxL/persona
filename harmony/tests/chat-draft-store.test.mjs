import test from 'node:test';
import assert from 'node:assert/strict';
import {registerHooks,stripTypeScriptTypes} from 'node:module';
import {readFileSync} from 'node:fs';
registerHooks({resolve(s,c,n){if(c.parentURL?.endsWith('/ChatDraftStore.ets')&&s==='./Wire')return{url:new URL('./Wire.ets',c.parentURL).href,shortCircuit:true};return n(s,c);},load(u,c,n){if(u.endsWith('.ets'))return{format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(u),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,''))};return n(u,c);}});
const {ChatDraftStore,ChatDraft,ChatOutboxEntry}=await import('../entry/src/main/ets/models/ChatDraftStore.ets');
function fixture(){const data=new Map();return{data,local:{revision(){return 0;},accepts(){return true;},async get(k){return data.get(k)||'';},async put(k,v){data.set(k,v);},async remove(k){data.delete(k);}}};}
function draft(text,revision){const d=new ChatDraft();d.text=text;d.revision=revision;return d;}
function entry(id,target='target'){const e=new ChatOutboxEntry();e.id=id;e.target=target;e.revision='draft:'+id;e.text='Synthetic message';e.at=Date.now();return e;}
test('acknowledgement clears its captured draft after navigating away but preserves a newer identical draft',async()=>{
 const {local}=fixture(),a=new ChatDraftStore(local),b=new ChatDraftStore(local);await a.save('old',draft('Same words','first'));await a.save('other',draft('Other chat','other'));
 assert.equal(await b.acknowledge('old','first'),true);assert.equal((await a.read('old')).text,'');assert.equal((await b.read('other')).text,'Other chat');
 await a.save('old',draft('Same words','second'));assert.equal(await b.acknowledge('old','first'),false);assert.equal((await a.read('old')).revision,'second');
});
test('the draft transaction serializes ACK with a newer edit across detail contexts',async()=>{
 const {local}=fixture(),a=new ChatDraftStore(local),b=new ChatDraftStore(local);await a.save('target',draft('first','r1'));
 const ack=a.acknowledge('target','r1'),edit=b.save('target',draft('next','r2'));await Promise.all([ack,edit]);assert.equal((await a.read('target')).text,'next');
});
test('pending outbox identities survive re-open and isolate targets without permitting silent replacement',async()=>{
 const {local}=fixture(),a=new ChatDraftStore(local);await a.stage('owner-a',entry('op1'));await assert.rejects(a.stage('owner-a',entry('op2')),/pending_action/);await a.stage('owner-a',entry('op3','other-target'));await a.stage('owner-b',entry('op4'));
 const restored=new ChatDraftStore(local);assert.deepEqual((await restored.list('owner-a')).map(x=>x.id),['op1','op3']);assert.deepEqual((await restored.list('owner-b')).map(x=>x.id),['op4']);
});
test('known receipts cannot be downgraded by later refresh failure and exact echoes retire one local message',async()=>{
 const {local}=fixture(),a=new ChatDraftStore(local);await a.stage('scope',entry('one'));await a.settle('scope','one','done');await a.settle('scope','one','unknown');await a.stage('scope',entry('two'));await a.discardEchoed('scope',['one']);
 assert.deepEqual((await a.list('scope')).map(x=>[x.id,x.state]),[['two','pending']]);
});

test('outbox payloads use separate bounded rows instead of one growing RDB cursor record',async()=>{
 const {local,data}=fixture(),a=new ChatDraftStore(local);const first=entry('one','a');first.text='x'.repeat(100000);const second=entry('two','b');second.text='y'.repeat(100000);await a.stage('scope',first);await a.stage('scope',second);
 assert.ok(data.get('outbox:scope').length<100);assert.equal(JSON.parse(data.get('outbox:scope:one')).text.length,100000);assert.equal(JSON.parse(data.get('outbox:scope:two')).text.length,100000);assert.equal((await new ChatDraftStore(local).list('scope')).length,2);
});

test('a queued draft or outbox write cannot return after its Hub is forgotten and immediately paired again',async()=>{
 const {local,data}=fixture();let epoch=0,release,entered;const gate=new Promise(r=>release=r),started=new Promise(r=>entered=r),get=local.get;
 local.revision=()=>epoch;local.accepts=(key,captured)=>captured===epoch;local.get=async key=>{if(key==='outbox:scope'){entered();await gate;}return get(key);};
 local.put=async(key,value,captured)=>{if(captured!==epoch)throw new Error('scope_retired');data.set(key,value);};
 const a=new ChatDraftStore(local),pending=a.stage('scope',entry('old'));await started;epoch++;data.clear();release();await assert.rejects(pending,/scope_retired/);assert.equal(data.size,0);
 await a.stage('scope',entry('new'));assert.deepEqual((await a.list('scope')).map(x=>x.id),['new']);
});


test('terminal retention cannot evict a known delivery whose attachment cleanup is still outstanding',async()=>{
 const {local}=fixture(),store=new ChatDraftStore(local),scope='hub/owner';
 const photo=new ChatOutboxEntry();photo.id='photo';photo.target='chat-photo';photo.attachmentSnapshot='captured-revisions';await store.stage(scope,photo);await store.settle(scope,photo.id,'done');
 for(let i=0;i<40;i++){const entry=new ChatOutboxEntry();entry.id='other-'+i;entry.target='other';await store.stage(scope,entry);await store.settle(scope,entry.id,'done');}
 assert.equal((await store.list(scope)).find(entry=>entry.id==='photo').attachmentSnapshot,'captured-revisions');
 await store.attachmentsCommitted(scope,'photo');assert.equal((await store.list(scope)).find(entry=>entry.id==='photo').attachmentSnapshot,'');
});
