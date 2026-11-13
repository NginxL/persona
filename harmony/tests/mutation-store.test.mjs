import test from 'node:test';
import assert from 'node:assert/strict';
import {registerHooks, stripTypeScriptTypes} from 'node:module';
import {readFileSync} from 'node:fs';
import {randomUUID} from 'node:crypto';

globalThis.__personaMutationTestUUID = randomUUID;
registerHooks({
  resolve(specifier, context, next) {
    if (specifier.endsWith('/core/StorageOwnership')) return {url:new URL(specifier+'.ts',context.parentURL).href,shortCircuit:true};
    if (specifier.endsWith('/core/ScheduleTemplates')) return {url:new URL(specifier+'.ts',context.parentURL).href,shortCircuit:true};
    if (specifier === './ClientModel' && context.parentURL?.endsWith('/ManagementModel.ets')) return {url:'persona-test:client',shortCircuit:true};
    if (specifier === '@kit.ArkTS') return {url:'persona-test:arkts',shortCircuit:true};
    if (specifier.endsWith('/platform/LocalStore')) return {url:'persona-test:local',shortCircuit:true};
    if (specifier.endsWith('/platform/HubTransport')) return {url:'persona-test:transport',shortCircuit:true};
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) return {url:new URL(specifier+'.ets',context.parentURL).href,shortCircuit:true};
    return next(specifier,context);
  },
  load(url,context,next) {
    if (url === 'persona-test:client') return {format:'module',source:'export class ClientModel {}',shortCircuit:true};
    if (url === 'persona-test:arkts') return {format:'module',source:'export const util={generateRandomUUID:globalThis.__personaMutationTestUUID};',shortCircuit:true};
    if (url === 'persona-test:local') return {format:'module',source:'export class LocalStore {}',shortCircuit:true};
    if (url === 'persona-test:transport') return {format:'module',source:'export class EndpointConfig {} export class HubTransport {async request(...args){return globalThis.__personaRequest(...args)} async requestBytes(...args){return globalThis.__personaRequestBytes(...args)}} export class HttpFailure extends Error {constructor(status,code,resultUnknown=false,resultPending=false){super(code);this.status=status;this.code=code;this.resultUnknown=resultUnknown;this.resultPending=resultPending;}}',shortCircuit:true};
    if (url.endsWith('/core/ScheduleTemplates.ts')) return {format:'module',source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8')),shortCircuit:true};
    if (url.endsWith('.ets')) {
      const source=readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,'');
      return {format:'module',source:stripTypeScriptTypes(source),shortCircuit:true};
    }
    return next(url,context);
  }
});
const {MutationStore} = await import('../entry/src/main/ets/models/MutationStore.ets');
class MemoryStore {
  data=new Map();
  failNext=false;
  delay;
  async get(key){return this.data.get(key)||'';}
  async put(key,value){if(this.delay)await this.delay(key,value);if(this.failNext){this.failNext=false;throw Error('disk full');}this.data.set(key,value);}
  async remove(key){this.data.delete(key);}
}
test('a confirmed completion remains done when its first canonical save fails',async()=>{
 const local=new MemoryStore(),queue=new MutationStore(local,'test'),record=await queue.begin('/api/v1/sessions/m/s/messages','POST','{"text":"hello"}');
 local.failNext=true;await queue.finish(record,'done');assert.equal(record.state,'done');assert.equal(queue.blocked().length,0);
 const restored=new MutationStore(local,'test');await restored.load();assert.equal(restored.blocked().length,0);
 local.failNext=true;await queue.finish(record,'done');assert.equal(local.failNext,true);assert.equal(record.state,'done');
});
test('completed message payloads do not prevent restoring after large cumulative sends',async()=>{
  const local=new MemoryStore(),queue=new MutationStore(local,'test');
  for(let i=0;i<3;i++){const record=await queue.begin('/api/v1/sessions/m/s/messages','POST',JSON.stringify({text:'x'.repeat(2_100_000)}));await queue.finish(record,'done');}
  const restored=new MutationStore(local,'test');await restored.load();assert.deepEqual(restored.blocked(),[]);
  assert.ok([...local.data.values()].every(value=>value.length<4096));
});
test('a completion and a different concurrent write both survive restart',async()=>{
  const local=new MemoryStore(),queue=MutationStore.shared(local,'test');
  assert.equal(MutationStore.shared(local,'test'),queue);
  const first=await queue.begin('/first/messages','POST','first');
  let unlock;const hold=new Promise(resolve=>unlock=resolve);let entered;
  const waiting=new Promise(resolve=>entered=resolve);
  local.delay=async(key,value)=>{if(key==='mutations:test'){entered();await hold;}};
  const finished=queue.finish(first,'done');await waiting;
  const secondPromise=MutationStore.shared(local,'test').begin('/second/messages','POST','second');
  unlock();await finished;const second=await secondPromise;local.delay=undefined;
  const restored=new MutationStore(local,'test');await restored.load();assert.deepEqual(restored.blocked().map(item=>item.id),[second.id]);
});
test('manual confirmation releases only an unknown operation',async()=>{
  const local=new MemoryStore(),queue=new MutationStore(local,'test');
  const record=await queue.begin('/messages','POST','payload');
  await queue.acknowledge(record.id);assert.equal(queue.blocked().length,1);
  await queue.finish(record,'unknown');await queue.acknowledge(record.id);assert.equal(queue.blocked().length,0);
});

test('history cleanup failure does not turn a confirmed result into an unindexed unknown',async()=>{
 const local=new MemoryStore(),queue=new MutationStore(local,'test');
 const old=await queue.begin('/old/messages','POST','old');await queue.finish(old,'unknown');
 for(let i=0;i<100;i++){const record=await queue.begin('/new/'+i,'POST','');await queue.finish(record,'done');}
 local.remove=async()=>{throw Error('cleanup failed');};
 await queue.finish(old,'done');assert.equal(queue.blocked().length,0);
 const restored=new MutationStore(local,'test');await restored.load();assert.equal(restored.blocked().length,0);
});

test('a local admission failure never becomes an unresolved dispatched write',async()=>{
 const local=new MemoryStore(),queue=new MutationStore(local,'test');
 await queue.load();local.failNext=true;
 await assert.rejects(queue.begin('/messages','POST','not dispatched'),error=>error.resultUnknown===false);
 assert.equal(queue.blocked().length,0);
 const record=await queue.begin('/messages','POST','new attempt');assert.ok(record.id);
});

const {HubRepository} = await import('../entry/src/main/ets/models/HubRepository.ets');
test('a dispatched write with an unreadable reply remains unknown to its caller',async()=>{
 const local=new MemoryStore(),repo=new HubRepository({managerId:'personal',hubId:'test'},local);
 globalThis.__personaRequest=async()=>'{invalid';
 await assert.rejects(repo.call('/messages','POST',{text:'hello'}),e=>e.resultUnknown===true);
 assert.equal(repo.mutations.blocked()[0].state,'unknown');
});
test('a server pending receipt cannot be manually released after reload',async()=>{
 const local=new MemoryStore(),repo=new HubRepository({managerId:'personal',hubId:'test'},local);
 globalThis.__personaRequest=async()=>' {"state":"pending"}';
 await assert.rejects(repo.call('/messages','POST',{text:'hello'}),e=>e.resultUnknown===true);
 const restored=new MutationStore(local,JSON.stringify(['test','personal']));await restored.load();
 const record=restored.blocked()[0];assert.equal(record.state,'pending');
 await restored.acknowledge(record.id);assert.equal(restored.blocked().length,1);
});
test('merged lists keep same IDs from other assistants out of the current scope',async()=>{
 const repo=new HubRepository({managerId:'work',hubId:'test'},undefined,()=>{},'personal');
 globalThis.__personaRequest=async()=>JSON.stringify({sessions:[{id:'same',manager:'personal'},{id:'same',manager:'work'},{id:'legacy'}]});
 assert.deepEqual((await repo.sessions()).map(row=>[row.id,row.manager]),[['same','work']]);
});
test('an uncertain file upload cannot be submitted again after restart',async()=>{
 const local=new MemoryStore(),repo=new HubRepository({managerId:'work',hubId:'test'},local);
 let calls=0;globalThis.__personaRequestBytes=async()=>{calls++;throw Error('lost reply')};
 await assert.rejects(repo.upload('/files?name=test',new ArrayBuffer(2)),e=>e.resultUnknown===true);
 const restored=new HubRepository({managerId:'work',hubId:'test'},local);
 await assert.rejects(restored.upload('/files?name=test',new ArrayBuffer(2)),e=>e.resultUnknown===true);
 assert.equal(calls,1);
});

const {ManagementModel,ManagementRow}=await import('../entry/src/main/ets/models/ManagementModel.ets');
const {HttpFailure}=await import('persona-test:transport');
function managementClient(call){return {epoch:42,forgottenEpoch:0,forgottenProfile:'',profile:{id:'hub-profile',manager:'personal'},features:[],scope(){return 'hub:'+this.profile.manager;},connectionGeneration(){return this.epoch},owns(g,s){return g===this.epoch&&s===this.scope()},repo:()=>({call}),target:()=> 'session'};}
test('settings can be reopened repeatedly regardless of connection generation',async()=>{
 const model=managementClient(async()=>({items:[{id:'one',connector:'mail'}]})),page=new ManagementModel();
 for(let i=0;i<3;i++){await page.load('ledger',model);assert.equal(page.loading,false);assert.equal(page.rows[0].id,'one');}
});
test('a previous connection cannot update the current settings page',async()=>{
 let finish;const model=managementClient(()=>new Promise(resolve=>finish=resolve)),page=new ManagementModel();
 const loading=page.load('ledger',model);model.epoch++;finish({items:[{id:'stale'}]});await loading;
 assert.deepEqual(page.rows,[]);
});
test('a late channel save does not overwrite another selected channel',async()=>{
 let finish;
 const model=managementClient(async(path,method)=>{
  if(method==='PUT')return new Promise(resolve=>finish=resolve);
  if(path.endsWith('/alpha'))return {channel:{id:'alpha',features:[{id:'voice'}]}};
  if(path.endsWith('/beta'))return {channel:{id:'beta'}};
  return {channels:[{id:'alpha'},{id:'beta'}]};
 }),page=new ManagementModel();
 await page.load('channels',model);await page.openChannel(model,new ManagementRow('alpha'));
 const save=page.setChannelFeature(model,'alpha','voice',true);
 await page.openChannel(model,new ManagementRow('beta'));
 finish({channel:{id:'alpha',features:[]}});assert.equal(await save,false);
 assert.equal(page.channelData().id,'beta');assert.equal(page.saving,false);
});
test('a stale page cannot send its old values through a replacement connection',async()=>{
 let calls=0;const model=managementClient(async()=>{calls++;return {}}),page=new ManagementModel();
 await page.load('triage',model);const before=calls;model.epoch++;
 assert.equal(await page.setTriage(model,{enabled:true}),false);assert.equal(calls,before);
});

test('mail replies send the exact recipient version shown to the user',async()=>{
 const repo=new HubRepository({managerId:'personal',hubId:'test'});let submitted;
 globalThis.__personaRequest=async(path,method,raw)=>{submitted=JSON.parse(raw);return '{"item":{"id":"mail"}}'};
 const {ResourceRow}=await import('../entry/src/main/ets/models/Wire.ets');
 const row=ResourceRow.from({id:'mail',mail:{},reply_target:{uid:32,version:'target-version'}},'inbox');
 await repo.reply(row,'Reply text','reply-id');
 assert.equal(submitted.expected_uid,32);assert.equal(submitted.expected_version,'target-version');
 row.raw=JSON.stringify({id:'mail',mail:{}});
 await assert.rejects(repo.reply(row,'Reply text','reply-id'));
});
test('a connected pending computer never has its credentials renewed',async()=>{
 let calls=0;const model=managementClient(async()=>{calls++;return {machines:[{id:'pending',trust:'pending',online:true}]}}),page=new ManagementModel();
 await page.load('machines',model);page.pairing={machine:'pending',expires_at:'2000-01-01T00:00:00Z'};
 assert.equal(page.canRenewPairing(),false);const before=calls;
 assert.equal(await page.renewPairing(model),false);assert.equal(calls,before);
});
test('renewal rechecks whether a pending computer connected while its code expired',async()=>{
 let writes=0,connected=false;
 const model=managementClient(async(path,method)=>{if(method==='POST'){writes++;return {}}return {machines:[{id:'pending',trust:'pending',online:connected,last_seen:'0001-01-01T00:00:00Z',link:null}]}}),page=new ManagementModel();
 await page.load('machines',model);page.pairing={machine:'pending',expires_at:'2000-01-01T00:00:00Z'};assert.equal(page.canRenewPairing(),true);
 connected=true;assert.equal(await page.renewPairing(model),false);assert.equal(writes,0);
});
test('numeric ledger IDs and cursors retain distinct entries across pages',async()=>{
 const model=managementClient(async(path)=>path.includes('before=9')?{items:[{id:8,connector:'mail'}],next:0}:{items:[{id:10,connector:'mail'},{id:9,connector:'mail'}],next:9}),page=new ManagementModel();
 await page.load('ledger',model);await page.loadMoreLedger(model);
 assert.deepEqual(page.rows.map(row=>row.id),['10','9','8']);assert.equal(page.loading,false);
});
test('a bundled extension confirms every reviewed member in server order',async()=>{
 let body;
 const model=managementClient(async(path,method,payload)=>{if(method==='POST'){body=payload;return {operation:{id:'install',state:'running'}}}if(path.endsWith('/operations'))return {operations:[]};return {extensions:[],can_install:true}}),page=new ManagementModel();
 model.features=['extension_operations'];await page.load('extensions',model);
 page.catalogSelectedId='pack';page.catalogSelectedMarket='catalog';page.selectedTarget='catalog:catalog:pack';
 page.preview={preview_id:'main-preview',permissions_digest:'main-digest',bundle:[{preview_id:'child-preview',permissions_digest:'child-digest',manifest:{id:'child'}}],depends:[{id:'child',state:'missing'}]};
 assert.equal(page.previewReady(),true);assert.equal(await page.confirmExtensionPreview(model),true);
 assert.deepEqual(body.bundle,[{preview_id:'child-preview',permissions_digest:'child-digest'}]);
});
test('a recovered consent operation restores the exact catalog target',async()=>{
 const model=managementClient(async(path)=>path.endsWith('/operations')?{operations:[{id:'op',state:'needs_consent'}]}:path.endsWith('/operations/op')?{id:'op',state:'needs_consent',extension:'pack',market:'catalog',preview:{preview_id:'main',permissions_digest:'digest'}}:{extensions:[]}),page=new ManagementModel();
 model.features=['extension_operations'];await page.load('extensions',model);
 assert.equal(page.catalogSelectedId,'pack');assert.equal(page.catalogSelectedMarket,'catalog');assert.equal(page.selectedTarget,'catalog:catalog:pack');
});
test('a second triage toggle cannot overwrite a pending full configuration save',async()=>{
 let finish,calls=0;
 const model=managementClient(async(path,method)=>{if(method==='PUT'){calls++;return new Promise(resolve=>finish=resolve)}return {enabled:true}}),page=new ManagementModel();
 await page.load('triage',model);const first=page.setTriage(model,{enabled:true,wake:true});
 assert.equal(await page.setTriage(model,{enabled:true,decide:true}),false);finish({enabled:true,wake:true});await first;assert.equal(calls,1);
});
test('late provider models never appear under a newer provider',async()=>{
 let finishA,finishB;
 const model=managementClient(async(path)=>path.includes('/providers/a/')?new Promise(resolve=>finishA=resolve):path.includes('/providers/b/')?new Promise(resolve=>finishB=resolve):path.endsWith('/purposes/assistant')?{purpose:{id:'assistant',fields:['provider','model'],effective:{provider:'none'}},providers:[{id:'a',kind:'launcher'},{id:'b',kind:'launcher'}]}:{purposes:[{id:'assistant'}]}),page=new ManagementModel();
 await page.load('models',model);page.changePurposeDraft('provider','a');const a=page.loadPurposeModels(model);
 page.changePurposeDraft('provider','b');const b=page.loadPurposeModels(model);
 finishB({models:[{id:'model-b'}]});await b;finishA({models:[{id:'model-a'}]});await a;
 assert.equal(page.providerModels[0].id,'model-b');
});

test('a purpose cannot change while its configuration is being saved',async()=>{
 let finish;
 const model=managementClient(async(path,method)=>method==='PATCH'?new Promise(resolve=>finish=resolve):path.includes('/purposes/')?{purpose:{id:path.split('/').pop(),fields:['provider','model'],override:{},effective:{}},providers:[]}:{purposes:[{id:'assistant'},{id:'kb'}]}),page=new ManagementModel();
 await page.load('models',model);const saving=page.savePurpose(model);
 await page.selectPurpose(model,'kb');assert.equal(page.selectedId,'assistant');
 finish({});await saving;assert.equal(page.saving,false);assert.equal(page.selectedId,'assistant');
});

test('a pending server receipt stays nonreleasable when its save fails',async()=>{
 const local=new MemoryStore(),queue=new MutationStore(local,'pending-failure');
 const record=await queue.begin('/messages','POST','payload');local.failNext=true;
 await assert.rejects(queue.finish(record,'pending'),e=>e.resultUnknown===true);
 assert.equal(record.state,'pending');await queue.acknowledge(record.id);assert.equal(queue.blocked().length,1);
 const restored=new MutationStore(local,'pending-failure');await restored.load();
 assert.equal(restored.blocked()[0].state,'pending');await restored.acknowledge(record.id);assert.equal(restored.blocked().length,1);
});
test('a total storage failure cannot erase the durable nonrelease gate for pending writes',async()=>{
 const local=new MemoryStore(),queue=new MutationStore(local,'disk-failure');
 const record=await queue.begin('/messages','POST','payload');const put=local.put.bind(local);
 local.put=async()=>{throw Error('disk unavailable')};
 await assert.rejects(queue.finish(record,'pending'));local.put=put;
 const restored=new MutationStore(local,'disk-failure');await restored.load();
 assert.equal(restored.blocked()[0].requiresReceipt,true);await restored.acknowledge(record.id);assert.equal(restored.blocked().length,1);
});

test('new agents cannot be sent to an untrusted, offline or differently owned computer',async()=>{
 const {ResourceRow}=await import('../entry/src/main/ets/models/Wire.ets');
 const repo=new HubRepository({managerId:'personal',hubId:'test'});let calls=0;
 globalThis.__personaRequest=async()=>{calls++;return '{}'};
 for(const data of [{id:'pending',manager:'personal',trust:'pending',online:true},{id:'offline',manager:'personal',online:false},{id:'other',manager:'work',online:true}]){
  await assert.rejects(repo.createAgent('task',ResourceRow.from(data,'machine'),'test','codex','op'));
 }
 assert.equal(calls,0);
});
test('the real done_at contract supports processing and reopening the same inbox item',async()=>{
 const {ResourceRow}=await import('../entry/src/main/ets/models/Wire.ets');
 const repo=new HubRepository({managerId:'personal',hubId:'test'});const seen=[];
 globalThis.__personaRequest=async(path,method,body)=>{seen.push(JSON.parse(body).done);return JSON.stringify({id:'item'})};
 await repo.processInbox(ResourceRow.from({id:'item',done_at:null},'inbox'));
 await repo.processInbox(ResourceRow.from({id:'item',done_at:'2026-01-01T00:00:00Z'},'inbox'));
 assert.deepEqual(seen,[true,false]);
});
test('a forgotten mutation instance cannot finish an old record after the same Hub is paired again',async()=>{
 const local=new MemoryStore(),scope=JSON.stringify(['hub-a','personal']),old=MutationStore.shared(local,scope);
 const record=await old.begin('/api/v1/sessions/m/s/messages','POST','{"text":"old private draft"}');
 const finishing=old.finish(record,'done');MutationStore.forgetHub(local,'hub-a');local.data.clear();
 const fresh=MutationStore.shared(local,scope),next=await fresh.begin('/api/v1/sessions/m/s/messages','POST','{"text":"new draft"}');
 await assert.rejects(finishing,/scope_retired/);await assert.rejects(old.begin(record.path,'POST','{}'),/scope_retired/);
 assert.equal(record.payload,'');assert.notEqual(next.id,record.id);assert.ok([...local.data.values()].every(value=>!value.includes(record.id)));
});
test('a delayed restore cannot recreate forgotten records over a new pairing',async()=>{
 const local=new MemoryStore(),scope=JSON.stringify(['hub-a','personal']);let release;const gate=new Promise(resolve=>release=resolve);let delay=true;
 local.get=async function(key){if(delay){delay=false;await gate;}return this.data.get(key)||'';};
 const old=MutationStore.shared(local,scope),loading=old.load();MutationStore.forgetHub(local,'hub-a');local.data.clear();
 const fresh=MutationStore.shared(local,scope),record=await fresh.begin('/api/v1/sessions/m/s/messages','POST','{"text":"new"}');release();
 await assert.rejects(loading,/scope_retired/);const reopened=new MutationStore(local,scope);await reopened.load();assert.equal(reopened.blocked()[0].id,record.id);
});
test('a durable successful receipt survives a subsequent operation-index write failure',async()=>{
 const local=new MemoryStore(),scope=JSON.stringify(['ack-index-hub','personal']),repo=new HubRepository({managerId:'personal',hubId:'ack-index-hub'},local);let failing=false;
 local.delay=async key=>{if(failing&&key==='mutations:'+scope)throw new Error('index unavailable');};globalThis.__personaRequest=async()=>{failing=true;return '{"ok":true}';};
 assert.equal((await repo.call('/messages','POST',{text:'Once'})).ok,true);assert.equal(repo.mutations.blocked().length,0);
 const ids=JSON.parse(local.data.get('mutations:'+scope)).ids;assert.equal(JSON.parse(local.data.get('mutations:'+scope+':'+ids[0])).state,'done');
 failing=false;const restored=new MutationStore(local,scope);await restored.load();assert.equal(restored.blocked().length,0);
});
test('completion proved by an echo cannot be downgraded by a later pending or unknown transport outcome',async()=>{
 const local=new MemoryStore(),queue=new MutationStore(local,'echo-monotonic'),record=await queue.begin('/messages','POST','body');await queue.finish(record,'done');await queue.finish(record,'unknown');await queue.finish(record,'pending');
 assert.equal(record.state,'done');assert.equal(queue.blocked().length,0);const restored=new MutationStore(local,'echo-monotonic');await restored.load();assert.equal(restored.blocked().length,0);
});
test('a near-limit photo request journals identity without persisting its Base64 or user content',async()=>{
 const local=new MemoryStore(),queue=new MutationStore(local,'photo-metadata'),body=JSON.stringify({text:'Private source',images:[{mime:'image/jpeg',data:'A'.repeat(3*1024*1024)}]});
 const record=await queue.begin('/api/v1/sessions/m/s/messages','POST',body);await queue.finish(record,'unknown');assert.equal(record.payload,'');assert.ok([...local.data.values()].every(value=>value.length<4096&&!value.includes('Private source')));
 const restored=new MutationStore(local,'photo-metadata');await restored.load();assert.equal(restored.blocked()[0].id,record.id);
});
test('permission revoked during mutation admission prevents the actual transport request',async()=>{
 const local=new MemoryStore(),repo=new HubRepository({managerId:'personal',hubId:'admission-hub'},local),scope=JSON.stringify(['admission-hub','personal']);let release,started,valid=true,requests=0;
 const gate=new Promise(r=>release=r),entered=new Promise(r=>started=r);local.delay=async(key,value)=>{if(key==='mutations:'+scope&&JSON.parse(value).ids.length){started();await gate;}};globalThis.__personaRequest=async()=>{requests++;return '{}';};
 const {ResourceRow}=await import('../entry/src/main/ets/models/Wire.ets'),row=new ResourceRow('session','','session');row.machine='machine';row.manager='personal';const sending=repo.send(row,'Once','admitted-op','',[],()=>valid);await entered;valid=false;release();await assert.rejects(sending,error=>error.resultUnknown===false);assert.equal(requests,0);assert.equal(repo.mutations.blocked().length,0);
});

test('a fresh done receipt cannot become manually releasable when its former state was unknown',async()=>{
 const local=new MemoryStore(),queue=new MutationStore(local,'unknown-to-done'),record=await queue.begin('/messages','POST','body');await queue.finish(record,'unknown');assert.equal(record.requiresReceipt,false);local.failNext=true;await queue.finish(record,'done');await queue.acknowledge(record.id);
 assert.equal(record.state,'done');const restored=new MutationStore(local,'unknown-to-done');await restored.load();assert.equal(restored.blocked().length,0);
});
test('a stale file-upload target is refused after durable admission and before binary transport',async()=>{
 const local=new MemoryStore(),repo=new HubRepository({managerId:'personal',hubId:'upload-admission'},local),scope=JSON.stringify(['upload-admission','personal']);let release,started,valid=true,requests=0;const gate=new Promise(r=>release=r),entered=new Promise(r=>started=r);
 local.delay=async(key,value)=>{if(key==='mutations:'+scope&&JSON.parse(value).ids.length){started();await gate;}};globalThis.__personaRequestBytes=async()=>{requests++;throw new Error('must not dispatch');};
 const uploading=repo.upload('/files',new Uint8Array([1,2,3]).buffer,()=>valid);await entered;valid=false;release();await assert.rejects(uploading,e=>e.resultUnknown===false);assert.equal(requests,0);assert.equal(repo.mutations.blocked().length,0);
});

test('machine trust, pairing creation and renewal revoke transport dispatch at final journal admission',async()=>{
 for(const action of ['trust','create','renew']){
  const local=new MemoryStore(),hub='machine-admit-'+action,scope=JSON.stringify([hub,'personal']),repo=new HubRepository({managerId:'personal',hubId:hub},local);
  let release,started,valid=true,writes=0;const gate=new Promise(r=>release=r),entered=new Promise(r=>started=r);
  globalThis.__personaRequest=async(path,method='GET')=>{if(method!=='GET'){writes++;return '{}';}return JSON.stringify({machines:[{id:'pending',trust:'pending',online:false,last_seen:'0001-01-01T00:00:00Z',link:null}]});};
  const model=managementClient((...args)=>repo.call(...args));model.features=['machines_add'];const page=new ManagementModel();page.setAdmission(()=>valid);await page.load('machines',model);page.pairing={machine:'pending',expires_at:'2000-01-01T00:00:00Z'};
  local.delay=async(key,value)=>{if(key==='mutations:'+scope&&JSON.parse(value).ids.length){started();await gate;}};
  const work=action==='trust'?page.trustMachine(model,page.rows[0],true):action==='create'?page.createPairing(model,'Synthetic Mac','mac'):page.renewPairing(model);
  await entered;valid=false;release();assert.equal(await work,false);assert.equal(writes,0);
 }
});

test('a created pair code remains acknowledged and usable when its follow-up machine list fails',async()=>{let reads=0,writes=0;const model=managementClient(async(path,method='GET')=>{if(method==='POST'){writes++;return{machine:'new-mac',pair_url:'synthetic-pair-code'};}if(++reads>1)throw Error('read unavailable');return{machines:[]};});model.features=['machines_add'];const page=new ManagementModel();await page.load('machines',model);assert.equal(await page.createPairing(model,'Synthetic Mac','mac'),true);assert.equal(writes,1);assert.equal(page.pairing.pair_url,'synthetic-pair-code');});


test('native settings mutation admission is revoked during durable journal persistence', async () => {
 for (const action of ['triage','connector']) {
  const local=new MemoryStore(),hub='settings-final-admit-'+action,scope=JSON.stringify([hub,'personal']),repo=new HubRepository({managerId:'personal',hubId:hub},local);
  let release,started,valid=true,writes=0;const gate=new Promise(r=>release=r),entered=new Promise(r=>started=r);
  globalThis.__personaRequest=async(path,method='GET')=>{if(method!=='GET'){writes++;return '{}';}return JSON.stringify(action==='connector'?{connectors:[{id:'mail',enabled:true}]}:{enabled:true});};
  const model=managementClient((...args)=>repo.call(...args)),page=new ManagementModel();page.setAdmission(()=>valid);await page.load(action==='connector'?'connectors':'triage',model);
  if(action==='connector')page.openConnector(page.rows[0]);
  local.delay=async(key,value)=>{if(key==='mutations:'+scope&&JSON.parse(value).ids.length){started();await gate;}};
  const work=action==='connector'?page.setConnectorSetting(model,page.rows[0],'enabled',false):page.setTriage(model,{enabled:false});
  await entered;valid=false;release();assert.equal(await work,false);assert.equal(writes,0);assert.equal(repo.mutations.blocked().length,0);
 }
});

test('successful connector settings, login and logout retain their native detail target', async()=>{
 for(const action of ['setting','login','logout']){
  let writes=0;const row={id:'mail',name:'Mail',enabled:true,capabilities:['login']};
  const model=managementClient(async(path,method='GET')=>{if(method!=='GET'){writes++;return {done:true};}return {connectors:[row]};}),page=new ManagementModel();
  await page.load('connectors',model);page.openConnector(page.rows[0]);
  const result=action==='setting'?await page.setConnectorSetting(model,page.rows[0],'enabled',false):action==='login'?await page.connectorLogin(model,page.rows[0],true):await page.connectorLogout(model,page.rows[0]);
  assert.equal(result,true);assert.equal(writes,1);assert.equal(page.selectedId,'mail');assert.equal(page.selectedTarget,'connector:mail');assert.equal(page.rows[0].id,'mail');
 }
});

test('extension operation completion reopens an existing detail and signals removed routes',async()=>{
 for(const removed of [false,true]){
  const model=managementClient(async(path)=>path.endsWith('/operations/op')?{id:'op',state:'succeeded'}:path.endsWith('/operations')?{operations:[]}:path==='/api/v1/extensions'?{extensions:removed?[]:[{id:'pack',name:'Pack'}]}:{id:'pack',config:{},form:{fields:[]}}),page=new ManagementModel();
  model.features=['extension_operations'];await page.load('extensions',model);page.selectedId='pack';page.selectedTarget='extension:pack';page.operation={id:'op',state:'running'};
  assert.equal(await page.refreshExtensionOperation(model,'op'),true);
  assert.equal(page.extensionDetailMissing,removed);
  if(!removed){assert.equal(page.selectedId,'pack');assert.equal(page.extensionDetail.id,'pack');}
 }
});


test('canceling an extension removal retains the originating native detail',async()=>{
 let catalogs=0;const model=managementClient(async(path,method='GET')=>{if(method==='DELETE')return {};if(path.includes('/catalog'))catalogs++;if(path.endsWith('/operations'))return {operations:[]};return path==='/api/v1/extensions'?{extensions:[{id:'pack',name:'Pack'}]}:{id:'pack',config:{},form:{fields:[]}};}),page=new ManagementModel();
 model.features=['extension_operations'];await page.load('extensions',model);await page.openExtension(model,new ManagementRow('pack'));page.operation={id:'op',state:'waiting'};
 assert.equal(await page.cancelExtensionOperation(model),true);assert.equal(page.selectedId,'pack');assert.equal(page.extensionDetail.id,'pack');assert.equal(catalogs,0);
});

test('schedule templates and cancel only edit the local draft',async()=>{
 let writes=0;const model=managementClient(async(path,method='GET')=>{if(method!=='GET')writes++;return path.endsWith('/tasks')?{tasks:[]}:{runs:[]};}),page=new ManagementModel();
 await page.load('schedules',model);page.beginSchedule();
 assert.equal(page.applyScheduleTemplate('weekly_report','Weekly report','Draft this week\'s report.'),true);
 assert.deepEqual([page.scheduleDraft.title,page.scheduleDraft.prompt,page.scheduleDraft.repeat,page.scheduleDraft.time,page.scheduleDraft.weekdays],
  ['Weekly report','Draft this week\'s report.','daily','17:00','5']);
 assert.equal(page.scheduleValid(),true);assert.equal(writes,0);
 const before=JSON.stringify(page.scheduleDraft);assert.equal(page.applyScheduleTemplate('missing','Missing','Prompt'),false);
 assert.equal(JSON.stringify(page.scheduleDraft),before);assert.equal(page.cancelSchedule(),true);
 assert.equal(page.scheduleDraft.title,'');assert.equal(page.scheduleDraft.prompt,'');assert.equal(writes,0);
});

test('an acknowledged schedule run survives its follow-up and same-owner refresh and routes by the response target',async()=>{
 const receipt={machine_id:'run-machine',session_id:'run-session'};
 const run={id:'history-1',title:'Weekly report',status:'ok',machine_id:'history-machine',session_id:'history-session'};
 const model=managementClient(async(path,method='GET')=>{
  if(path==='/api/v1/assistant/tasks')return {tasks:[{id:'task-1',title:'Weekly report',prompt:'prompt',repeat:'daily',time:'17:00',weekdays:[5]}]};
  if(path==='/api/v1/assistant/runs')return {runs:[run]};
  if(path.endsWith('/run')&&method==='POST')return receipt;
  return {};
 }),page=new ManagementModel();
 await page.load('schedules',model);assert.equal(await page.runSchedule(model,page.rows[0]),true);
 assert.deepEqual(page.lastRunReceipt,receipt);
 await page.load('schedules',model);assert.deepEqual(page.lastRunReceipt,receipt);
 model.forgottenProfile='other-hub';model.forgottenEpoch++;
 await page.load('schedules',model);assert.deepEqual(page.lastRunReceipt,receipt);
 const result=page.scheduleReceiptChat(model);
 assert.deepEqual(result&&[result.id,result.group,result.manager,result.machine,result.session],
  ['run-session','hub-profile','personal','run-machine','run-session']);
 const history=page.scheduleRuns.find(row=>row.id==='history-1');
 const historyResult=history&&page.scheduleRunChat(model,history);
 assert.deepEqual(historyResult&&[historyResult.id,historyResult.group,historyResult.manager,historyResult.machine,historyResult.session],
  ['history-session','hub-profile','personal','history-machine','history-session']);
 const noTarget=new ManagementRow('history-2','Skipped','skipped',{id:'history-2',status:'skipped'});
 noTarget.group='hub-profile';noTarget.manager='personal';noTarget.forgetEpoch=0;noTarget.scope=model.scope();page.scheduleRuns.push(noTarget);
 assert.equal(page.scheduleRunChat(model,noTarget),undefined);
});

test('unknown run targets never infer a chat and receipts clear on owner, page and forget changes',async()=>{
 let runReply={session_id:'session-without-machine'};
 const model=managementClient(async(path,method='GET')=>{
  if(path==='/api/v1/assistant/tasks')return {tasks:[{id:'task-1',title:'Task',prompt:'prompt',machine_id:'wrong-machine',session_id:'wrong-session'}]};
  if(path==='/api/v1/assistant/runs')return {runs:[{id:'history',title:'Task',status:'failed'}]};
  if(path.endsWith('/run')&&method==='POST')return runReply;
  if(path==='/api/v1/channels')return {channels:[]};
  return {};
 }),page=new ManagementModel();
 await page.load('schedules',model);assert.equal(await page.runSchedule(model,page.rows[0]),true);
 assert.equal(page.scheduleReceiptChat(model),undefined);assert.deepEqual(page.lastRunReceipt,runReply);
 const oldRun=page.scheduleRuns[0];model.profile.manager='other';
 assert.equal(page.scheduleReceiptChat(model),undefined);assert.deepEqual(page.lastRunReceipt,{});
 assert.equal(oldRun&&page.scheduleRunChat(model,oldRun),undefined);
 await page.load('schedules',model);assert.deepEqual(page.lastRunReceipt,{});
 runReply={machine_id:'new-machine',session_id:'new-session'};
 assert.equal(await page.runSchedule(model,page.rows[0]),true);assert.ok(page.scheduleReceiptChat(model));
 await page.load('channels',model);assert.deepEqual(page.lastRunReceipt,{});
 await page.load('schedules',model);assert.deepEqual(page.lastRunReceipt,{});
 assert.equal(await page.runSchedule(model,page.rows[0]),true);assert.ok(page.scheduleReceiptChat(model));
 model.forgottenProfile=model.profile.id;model.forgottenEpoch++;
 assert.equal(page.scheduleReceiptChat(model),undefined);assert.deepEqual(page.lastRunReceipt,{});
});

test('a foreign-profile forget before Run Now does not discard the current schedule acknowledgement',async()=>{
 const receipt={machine_id:'run-machine',session_id:'run-session'};
 const model=managementClient(async(path,method='GET')=>{
  if(path==='/api/v1/assistant/tasks')return {tasks:[{id:'task-1',title:'Task'}]};
  if(path==='/api/v1/assistant/runs')return {runs:[]};
  if(path.endsWith('/run')&&method==='POST')return receipt;
  return {};
 });
 model.forgottenProfile='other-hub';model.forgottenEpoch++;
 const page=new ManagementModel();await page.load('schedules',model);
 assert.equal(await page.runSchedule(model,page.rows[0]),true);
 const target=page.scheduleReceiptChat(model);
 assert.deepEqual(target&&[target.group,target.manager,target.machine,target.session],['hub-profile','personal','run-machine','run-session']);
});

test('schedule result targets keep a valid empty legacy manager',async()=>{
 const receipt={machine_id:'legacy-machine',session_id:'legacy-session'};
 const history={id:'legacy-history',title:'Legacy task',status:'ok',machine_id:'history-machine',session_id:'history-session'};
 const model=managementClient(async(path,method='GET')=>{
  if(path==='/api/v1/assistant/tasks')return {tasks:[{id:'task-1',title:'Legacy task'}]};
  if(path==='/api/v1/assistant/runs')return {runs:[history]};
  if(path.endsWith('/run')&&method==='POST')return receipt;
  return {};
 });
 model.profile.manager='';
 const page=new ManagementModel();await page.load('schedules',model);
 assert.equal(await page.runSchedule(model,page.rows[0]),true);
 const target=page.scheduleReceiptChat(model);
 assert.deepEqual(target&&[target.group,target.manager,target.machine,target.session],['hub-profile','','legacy-machine','legacy-session']);
 const result=page.scheduleRuns[0]&&page.scheduleRunChat(model,page.scheduleRuns[0]);
 assert.deepEqual(result&&[result.group,result.manager,result.machine,result.session],['hub-profile','','history-machine','history-session']);
});

test('a failed or unknown next run cannot open the previous acknowledged run conversation',async()=>{
 let runs=0;const model=managementClient(async(path,method='GET')=>{
  if(path==='/api/v1/assistant/tasks')return{tasks:[{id:'task-1',title:'Task',prompt:'prompt',repeat:'daily',time:'17:00',weekdays:[5]}]};
  if(path==='/api/v1/assistant/runs')return{runs:[]};
  if(path.endsWith('/run')&&method==='POST'){if(++runs===1)return{machine_id:'first-machine',session_id:'first-session'};throw new HttpFailure(0,'unknown',true);}
  return{};
 }),page=new ManagementModel();await page.load('schedules',model);assert.equal(await page.runSchedule(model,page.rows[0]),true);assert.ok(page.scheduleReceiptChat(model));
 assert.equal(await page.runSchedule(model,page.rows[0]),false);assert.equal(page.scheduleReceiptChat(model),undefined);assert.deepEqual(page.lastRunReceipt,{});
});

test('schedule drafts stay with their starting owner while same-owner refreshes preserve them',async()=>{
 for(const kind of ['edit','template']){
  let writes=0;const task={id:'task-1',title:'Task',prompt:'original',repeat:'daily',time:'09:00',weekdays:[1,2,3,4,5]};
  const model=managementClient(async(path,method='GET')=>{
   if(path==='/api/v1/assistant/tasks')return {tasks:[task]};
   if(path==='/api/v1/assistant/runs')return {runs:[]};
   if(method!=='GET'){writes++;return {};}
   return {};
  }),page=new ManagementModel();
  await page.load('schedules',model);
  if(kind==='edit'){page.beginSchedule(page.rows[0]);page.changeSchedule('prompt','edited for owner A');}
  else {page.beginSchedule();assert.equal(page.applyScheduleTemplate('weekly_report','Weekly report','weekly report prompt'),true);}
  const expectedTitle=page.scheduleDraft.title,expectedPrompt=page.scheduleDraft.prompt;
  await page.load('schedules',model);
  assert.equal(page.scheduleDraft.title,expectedTitle);assert.equal(page.scheduleDraft.prompt,expectedPrompt);
  model.profile.manager='owner-b';await page.load('schedules',model);
  assert.equal(page.scheduleDraft.title,'');assert.equal(page.scheduleDraft.prompt,'');
  assert.equal(await page.saveSchedule(model),false);assert.equal(writes,0);
 }
});

test('schedule task mutations reject stale owner rows even when IDs collide',async()=>{
 for(const action of ['enabled','run','delete']){
  let writes=0;const task={id:'shared-task-id',title:'Task',prompt:'prompt',repeat:'daily',time:'09:00',weekdays:[1,2,3,4,5]};
  const model=managementClient(async(path,method='GET')=>{
   if(path==='/api/v1/assistant/tasks')return {tasks:[task]};
   if(path==='/api/v1/assistant/runs')return {runs:[]};
   if(method!=='GET'){writes++;return path.endsWith('/run')?{machine_id:'machine',session_id:'session'}:{ };}
   return {};
  }),page=new ManagementModel();
  await page.load('schedules',model);const stale=page.rows[0];
  model.profile.manager='owner-b';await page.load('schedules',model);const current=page.rows[0];
  const invoke=(row)=>action==='enabled'?page.setScheduleEnabled(model,row,false):action==='run'?page.runSchedule(model,row):page.deleteSchedule(model,row);
  assert.equal(await invoke(stale),false);assert.equal(writes,0);
  assert.equal(await invoke(current),true);assert.equal(writes,1);
 }
});

test('same-owner refreshed schedule rows remain editable while pre-refresh row identities expire',async()=>{
 let writes=0;const task={id:'task-1',title:'Task',prompt:'original',repeat:'daily',time:'09:00',weekdays:[1,2,3,4,5]};
 const model=managementClient(async(path,method='GET')=>{
  if(path==='/api/v1/assistant/tasks')return {tasks:[task]};
  if(path==='/api/v1/assistant/runs')return {runs:[]};
  if(method!=='GET'){writes++;return {};}
  return {};
 }),page=new ManagementModel();
 await page.load('schedules',model);const beforeRefresh=page.rows[0];
 page.beginSchedule(beforeRefresh);page.changeSchedule('prompt','edited safely');
 await page.load('schedules',model);const refreshed=page.rows[0];
 assert.equal(page.scheduleDraft.prompt,'edited safely');assert.notEqual(refreshed,beforeRefresh);
 assert.equal(await page.setScheduleEnabled(model,beforeRefresh,false),false);assert.equal(writes,0);
 assert.equal(await page.setScheduleEnabled(model,refreshed,false),true);assert.equal(writes,1);
 assert.equal(await page.saveSchedule(model),true);assert.equal(writes,2);
});

test('edited schedule identity and connection generation must remain current',async()=>{
 let writes=0,task={id:'task-1',title:'Task',prompt:'original',repeat:'daily',time:'09:00',weekdays:[1,2,3,4,5]};
 const model=managementClient(async(path,method='GET')=>{
  if(path==='/api/v1/assistant/tasks')return {tasks:task?[task]:[]};
  if(path==='/api/v1/assistant/runs')return {runs:[]};
  if(method!=='GET'){writes++;return {};}
  return {};
 }),page=new ManagementModel();
 await page.load('schedules',model);const removedTask=page.rows[0];
 page.beginSchedule(removedTask);page.changeSchedule('prompt','edited before removal');task=undefined;
 await page.load('schedules',model);
 assert.equal(page.scheduleDraft.title,'');assert.equal(await page.saveSchedule(model),false);
 assert.equal(await page.setScheduleEnabled(model,removedTask,false),false);assert.equal(writes,0);

 task={id:'task-1',title:'Task',prompt:'original',repeat:'daily',time:'09:00',weekdays:[1,2,3,4,5]};
 await page.load('schedules',model);const disconnectedRow=page.rows[0];
 page.beginSchedule(disconnectedRow);page.changeSchedule('prompt','edited before reconnect');model.epoch++;
 assert.equal(await page.saveSchedule(model),false);assert.equal(await page.setScheduleEnabled(model,disconnectedRow,false),false);
 await page.load('schedules',model);
 assert.equal(page.scheduleDraft.title,'');assert.equal(await page.setScheduleEnabled(model,disconnectedRow,false),false);assert.equal(writes,0);
});


test('an extension without sign-in omits auth state without reporting a detail failure', async () => {
 for (const status of [404, 405, 403, 500]) {
  const model=managementClient(async path=>{
   if (path.endsWith('/auth')) throw new HttpFailure(status, 'auth_unavailable');
   if (path.endsWith('/-/')) return {id:'native-check',ui:[{id:'controls',title:'Controls'}]};
   return {extensions:[{id:'native-check'}]};
  });
  model.features=['extension_manager','extension_auth'];const page=new ManagementModel();
  await page.load('extensions',model);await page.openExtension(model,new ManagementRow('native-check'));
  assert.deepEqual(page.auth,{});assert.equal(page.extensionDetail.id,'native-check');
  assert.equal(!!page.error,status!==404&&status!==405);
 }
});
