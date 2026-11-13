import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync, existsSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.ArkTS') return {url:'client-boundary:'+specifier,shortCircuit:true};
    if (context.parentURL?.endsWith('/ClientModel.ets')) {
      if (['./Wire', './AssistantView', './DemoRepository', './NavigationContext', './ChatDraftStore', './HubRepository', './MutationStore', './Compatibility', './InteractionDecision', '../core/ClientLocale', '../core/ClientLocale', '../core/Domain', '../core/ScopePolicy', '../core/StorageOwnership', '../core/MessageContent', '../core/TranscriptCache'].includes(specifier)) {
        const base = new URL(specifier, context.parentURL);
        return {url: base.href + (existsSync(new URL(base.href + '.ts')) ? '.ts' : '.ets'), shortCircuit: true};
      }
      return {url: 'client-boundary:' + specifier, shortCircuit: true};
    }
    if (context.parentURL?.endsWith('/AssistantView.ets')) {
      if (specifier === '@kit.ArkTS') return {url:'client-boundary:'+specifier,shortCircuit:true};
      if (specifier.startsWith('.')) { const base=new URL(specifier,context.parentURL); return {url:base.href+(existsSync(new URL(base.href+'.ts'))?'.ts':'.ets'),shortCircuit:true}; }
    }
    if (context.parentURL?.endsWith('/DemoRepository.ets') || context.parentURL?.endsWith('/DemoTransport.ets') || context.parentURL?.endsWith('/HubRepository.ets') || context.parentURL?.endsWith('/MutationStore.ets')) {
      if (['./Wire','./HubRepository','./InteractionDecision','./MutationStore','./DemoTransport'].includes(specifier)) return {url:new URL(specifier+'.ets',context.parentURL).href,shortCircuit:true};
      if (specifier==='../core/StorageOwnership') return {url:new URL(specifier+'.ts',context.parentURL).href,shortCircuit:true};
      return {url:'client-boundary:'+specifier,shortCircuit:true};
    }
    if (context.parentURL?.endsWith('/InboxWorkspace.ets') || context.parentURL?.endsWith('/TodoWorkspace.ets') ||
      context.parentURL?.endsWith('/MessageImage.ets') || context.parentURL?.endsWith('/RemoteDocument.ets')) {
      if (specifier === '@kit.ArkTS' || specifier === '../platform/HubTransport') return {url:'client-boundary:'+specifier,shortCircuit:true};
      if (specifier.startsWith('.')) return {url:new URL(specifier.endsWith('.ets') || specifier.endsWith('.ts') ? specifier : specifier + '.ets',context.parentURL).href,shortCircuit:true};
    }
    if((context.parentURL?.endsWith('/NavigationContext.ets')||context.parentURL?.endsWith('/ChatDraftStore.ets')||context.parentURL?.endsWith('/Compatibility.ets'))&&specifier==='./Wire')return {url:new URL('./Wire.ets',context.parentURL).href,shortCircuit:true};
    if(context.parentURL?.endsWith('/InteractionDecision.ets')&&specifier.startsWith('.'))return {url:new URL(specifier+'.ets',context.parentURL).href,shortCircuit:true};
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.startsWith('client-boundary:')) return {format: 'module', shortCircuit: true, source: `
      export class HttpFailure extends Error { constructor(status=0,codeOrUnknown=false,unknownOrPending=false,pending=false) { super('synthetic failure'); this.status=status; this.resultUnknown=typeof codeOrUnknown==='string'?unknownOrPending:codeOrUnknown; this.resultPending=typeof codeOrUnknown==='string'?pending:unknownOrPending; } }
      export class HubTransport {async request(...args){return globalThis.clientTestRequest(...args);} close(){} } export class EndpointConfig {constructor(baseUrl='',hubId='',token='',pin='',managerId='',alternateUrls=[]){Object.assign(this,{baseUrl,hubId,token,pin,managerId,alternateUrls});}} export class CredentialVault {}
      export class LocalStore { data=new Map(); revision(){return 0;} accepts(){return true;} async put(k,v) {this.data.set(k,v);} async get(k) { return this.data.get(k)||''; } async remove(k){this.data.delete(k);} allowHub() {} async purgeHub(hub) {for(const key of this.data.keys())if(globalThis.clientIsHubStateKey(key,hub))this.data.delete(key);} }
      export class HubRepository {} export class TranscriptModel {} export class SessionWorkspace {}
      export class SshTunnel { initialize(){} stop(){} async bind(client){this.client=client;} async acquire(){if(globalThis.clientSshAcquire)return globalThis.clientSshAcquire(this.client);return undefined;} async forget(){} }
      export class KnowledgeRecording { static async forgetProfile(){} }
      export const deviceLook = {async forgetHub(){}};
      export class Pairing {static async reachable(primary,...args){return globalThis.clientPairingAddress ? globalThis.clientPairingAddress(primary,...args) : primary;}} export class PairingTarget {} export class MutationRecord {} export class MutationStore { static forgetHub() {} }
      export class ChatAttachments { static allowHub() {} static async forgetHub() {} static suppressStoredSnapshot(...args) {globalThis.clientAttachmentSuppressed?.push(args);} static async commitStoredSnapshot(...args) {globalThis.clientAttachmentCommits?.push(args);if(globalThis.clientAttachmentCommitGate)await globalThis.clientAttachmentCommitGate();} }
      export function clearImagePreviews() {}
      export const common = {};
      export const url = {URL:{parseURL:value=>new URL(value)}};
      export const util = {TextEncoder:class {static create(){return new this();}encodeInto(value){return new TextEncoder().encode(value);}},generateRandomUUID:()=> 'synthetic-operation-'+(++globalThis.clientTestUUID)};
      export const tr = key => key; export const Strings = {refresh(){}}; export const i18n = {getSystemLanguage(){return 'zh-CN';},System:{setAppPreferredLanguage(value){globalThis.clientLanguageChanges?.push(value);}}};
      export const segment = encodeURIComponent;
      export const withSourceContext = text => text;
      export const messageDisplay = text => text;
      export const nativeNotifications = {async clear(){},features(){return ['elicitation'];}};
    `};
    if (url.endsWith('.ets')) return {format:'module', shortCircuit:true, source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,''))};
    return next(url, context);
  }
});
const {isHubStateKey}=await import('../entry/src/main/ets/core/StorageOwnership.ts');globalThis.clientIsHubStateKey=isHubStateKey;
const contextReference=JSON.stringify({kind:'todo',id:'source',title:'Source'});
globalThis.clientTestUUID=0;
const { ClientModel } = await import('../entry/src/main/ets/models/ClientModel.ets');
const { HubProfile, ResourceRow, Decision } = await import('../entry/src/main/ets/models/Wire.ets');
const { HttpFailure } = await import('client-boundary:../platform/HubTransport');
const { InboxWorkspace } = await import('../entry/src/main/ets/models/InboxWorkspace.ets');
const { TodoWorkspace } = await import('../entry/src/main/ets/models/TodoWorkspace.ets');

const {clientLocale,setClientLanguage}=await import('../entry/src/main/ets/core/ClientLocale.ts');
function fixture() {
  setClientLanguage('default');
  const model = new ClientModel();
  model.profile = new HubProfile('synthetic-hub'); model.profile.manager='personal';
  model.selected = new ResourceRow('chat-one','Chat','session'); model.selected.machine='machine-one'; model.selected.session='chat-one';
  model.decisionReadyTarget = model.target();
  model.connected = true; model.canSend=()=>true; model.canApprove=()=>true; model.canInterrupt=()=>true;
  model.draft='exact draft '; model.sourceContext=contextReference;
  const writes=[];
  model.repository={async send(...args) { writes.push(args); }, async resolve() { return {}; }, async stop() {}};
  model.loadChat=async()=>{};
  return {model,writes};
}
function decision(model,id='request-one') { const value=new Decision(); value.id=id; value.kind='permission'; value.raw=JSON.stringify({id}); model.decision=value; return value; }
function deferred() { let resolve,reject; const promise=new Promise((yes,no)=>{resolve=yes;reject=no;}); return {promise,resolve,reject}; }

test('an acknowledged message stays done when refreshing its transcript fails',async()=>{
  const {model,writes}=fixture(); model.loadChat=async()=>{throw new Error('read failed');};
  assert.equal(await model.send(),true); assert.equal(writes.length,1);
  assert.equal(model.operationState,'done'); assert.equal(model.draft,''); assert.equal(model.sourceContext,'');
});
test('a failed dispatched send preserves the exact draft and records an uncertain result',async()=>{
  const {model}=fixture(); model.repository.send=async()=>{throw new HttpFailure(0,true);};
  assert.equal(await model.send(),false); assert.equal(model.operationState,'unknown');
  assert.equal(model.draft,'exact draft '); assert.equal(model.sourceContext,contextReference);
});
test('a late acknowledged send cannot clear a newly selected conversation draft',async()=>{
  const {model}=fixture(), wait=deferred(), admitted=deferred(); let dispatched;
  model.repository.send=async row=>{dispatched=row;admitted.resolve();await wait.promise;};
  const sending=model.send(); await admitted.promise; const original=model.selected;
  model.selected=new ResourceRow('chat-two','Other','session'); model.draft='new conversation draft'; model.sourceContext='new source'; model.operationState='';
  wait.resolve(); assert.equal(await sending,true); assert.equal(dispatched,original);
  assert.equal(model.draft,'new conversation draft'); assert.equal(model.sourceContext,'new source'); assert.equal(model.operationState,'');
});
test('an acknowledged approval cannot reopen after a follow-up read fails',async()=>{
  const {model}=fixture(), request=decision(model); let count=0;
  model.repository.resolve=async()=>{count++;return {};}; model.loadChat=async()=>{throw new Error('read failed');};
  await model.answer('approve',{},request); await model.answer('approve',{},request);
  assert.equal(count,1); assert.equal(request.status,'resolved'); assert.equal(model.operationState,'done');
});
test('an accepted pending approval stays disabled until a receipt is known',async()=>{
  const {model}=fixture(), request=decision(model); let count=0;
  model.repository.resolve=async()=>{count++;throw new HttpFailure(202,true,true);};
  await model.answer('approve',{},request); await model.answer('approve',{},request);
  assert.equal(count,1); assert.equal(request.status,'pending'); assert.equal(model.operationState,'pending');
});
test('a stale form cannot submit values to a replacement request',async()=>{
  const {model}=fixture(), previous=decision(model); decision(model,'request-two'); let count=0;
  model.repository.resolve=async()=>{count++;return {};};
  await model.answer('approve',{secret:'synthetic'},previous);
  assert.equal(count,0); assert.equal(model.error,'request_updated'); assert.equal(model.decision.status,'open');
});
test('a late approval or interrupt failure cannot overwrite another conversation state',async()=>{
  for (const action of ['answer','stop']) {
    const {model}=fixture(), request=decision(model), wait=deferred();
    model.repository.resolve=()=>wait.promise; model.repository.stop=()=>wait.promise;
    const result=action==='answer'?model.answer('approve',{},request):model.stop();
    model.selected=new ResourceRow('chat-two','Other','session'); model.error=''; model.operationState='';
    wait.reject(new HttpFailure(503,true)); await result;
    assert.equal(model.error,''); assert.equal(model.operationState,'');
  }
});
test('a delayed main-chat refresh cannot create a conversation on a newly selected owner',async()=>{
 const {model}=fixture(),wait=deferred();let count=0;model.refresh=()=>wait.promise;model.repository.newConversation=async()=>{count++;return new ResourceRow('new','','conversation');};
 const opening=model.openMain();model.profile=new HubProfile('different-hub');wait.resolve();
 await assert.rejects(opening,/request_updated/);assert.equal(count,0);
});
test('a newly created conversation cannot select itself after its owner is changed',async()=>{
 const {model}=fixture(),wait=deferred();let selected=0;model.repository.newConversation=()=>wait.promise;model.select=async()=>{selected++;};
 const opening=model.newConversation();model.profile=new HubProfile('different-hub');wait.resolve(new ResourceRow('new','','conversation'));
 await assert.rejects(opening,/request_updated/);assert.equal(selected,0);
});
test('forgetting the active Hub clears content and retires writes before removing its credential',async()=>{
 const {model}=fixture(),profile=model.profile,events=[],wait=deferred();model.profiles=[profile];model.messages=[{text:'synthetic private message'}];model.sessions=[model.selected];
 model.repository.close=()=>events.push('closed');model.local.purgeHub=async id=>{events.push('purge:'+id);await wait.promise;};model.vault={async remove(id){events.push('credential:'+id);}};
 const forgetting=model.forgetProfile(profile.id);
 assert.equal(model.profile.id,'');assert.equal(model.draft,'');assert.equal(model.messages.length,0);assert.equal(model.sessions.length,0);await new Promise(resolve=>setImmediate(resolve));assert.ok(events.includes('purge:'+profile.id));
 await assert.rejects(model.selectProfile(profile),/pending_action/);assert.ok(!events.some(event=>event.startsWith('credential:')));
 wait.resolve();await forgetting;assert.ok(events.includes('credential:'+profile.id));assert.deepEqual(model.profiles,[]);
});
test('an approval submits an open immutable request snapshot while its visible form stays pending',async()=>{
 const {model}=fixture(),request=decision(model),wait=deferred();let sent;
 model.repository.resolve=async captured=>{sent=captured;assert.equal(request.status,'pending');assert.equal(captured.status,'open');await wait.promise;return {};};
 const approving=model.answer('approve',{},request);assert.notEqual(sent,request);assert.equal(sent.raw,request.raw);wait.resolve();await approving;assert.equal(request.status,'resolved');
});
test('background closes its captured transport even if saving the draft fails',async()=>{
 const {model}=fixture();let closed=0;model.repository.close=()=>closed++;model.persistDraft=async()=>{throw new Error('storage failed');};
 await assert.rejects(model.background(),/storage failed/);assert.equal(closed,1);
});
test('a delayed background draft save cannot close a replacement foreground transport',async()=>{
 const {model}=fixture(),wait=deferred();let oldClosed=0,newClosed=0;model.repository.close=()=>oldClosed++;model.persistDraft=()=>wait.promise;
 const background=model.background();model.repository={close:()=>newClosed++};wait.resolve();await background;
 assert.equal(oldClosed,1);assert.equal(newClosed,0);
});
test('an earlier credential admission cannot restore a Hub after a later forget',async()=>{
 const {model}=fixture(),profile=model.profile,wait=deferred(),events=[];model.profiles=[profile];model.repository.close=()=>{};
 model.vault={async get(){return null;},async put(){events.push('put');await wait.promise;},async remove(){events.push('remove');}};
 const pairing=model.useEndpoint({hubId:profile.id,baseUrl:'https://example.test',token:'synthetic',pin:'test',managerId:'personal'},'Synthetic');
 for(let i=0;i<20&&!events.length;i++)await new Promise(resolve=>setTimeout(resolve,0));
 const forgetting=model.forgetProfile(profile.id);wait.resolve();await assert.rejects(pairing,/request_updated/);await forgetting;
 assert.deepEqual(events,['put','remove']);assert.equal(model.profile.id,'');assert.deepEqual(model.profiles,[]);
});
test('a redeem response returning after forget cannot write credentials or restore the profile',async()=>{
 const {model}=fixture(),profile=model.profile,wait=deferred();model.profiles=[profile];model.repository.close=()=>{};let admitted=0;model.vault={async get(){return null;},async remove(){},async put(){admitted++;}};
 const pairing=model.pair({redeem:()=>wait.promise},{hubId:profile.id,name:'Synthetic'});await model.forgetProfile(profile.id);
 wait.resolve({hubId:profile.id,baseUrl:'https://example.test',token:'synthetic',pin:'test',managerId:'personal'});await assert.rejects(pairing,/request_updated/);
 assert.equal(admitted,0);assert.equal(model.profile.id,'');assert.deepEqual(model.profiles,[]);
});
test('a warm notification tap remains queued while reconnecting and opens only after the catalog refresh',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.connected=false;const row=model.selected;model.sessions=[row];let attempts=0;
 model.selectProfile=async()=>{attempts++;};model.refresh=async()=>{};
 await model.openNotification({profile:profile.id,manager:'personal',machine:row.machine,session:row.session});assert.equal(attempts,1);assert.equal(model.notificationTarget,'');
 model.connected=true;await model.foregrounded();assert.equal(JSON.parse(model.notificationTarget).id,row.id);
});
test('a notification tap queued in background is invalidated when its Hub is forgotten',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.repository.close=()=>{};model.refresh=async()=>{};model.selectProfile=async()=>{};
 await model.background();await model.openNotification({profile:profile.id,manager:'personal',machine:'machine-one',session:'chat-one'});await model.forgetProfile(profile.id);
 model.connected=true;await model.foregrounded();assert.equal(model.notificationTarget,'');
});
test('notification cleanup failures cannot skip credential removal or profile deletion',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.repository.close=()=>{};let removed=0;model.vault={async remove(){removed++;}};
 const {nativeNotifications}=await import('client-boundary:../platform/NativeNotifications');const clear=nativeNotifications.clear;nativeNotifications.clear=async()=>{throw new Error('service unavailable');};
 try {await assert.rejects(model.forgetProfile(profile.id),/cleanup_incomplete/);assert.equal(removed,1);assert.deepEqual(model.profiles,[]);assert.equal(model.error,'cleanup_incomplete');}
 finally {nativeNotifications.clear=clear;}
});

test('failed erasure retains a durable cleanup identity that retries after restart without reconnecting',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.repository.close=()=>{};let attempts=0;model.vault={async remove(){attempts++;throw Error('asset unavailable');}};
 await assert.rejects(model.forgetProfile(profile.id),/cleanup_incomplete/);assert.deepEqual(model.profiles,[]);assert.equal(model.pendingCleanupCount,1);assert.deepEqual(JSON.parse(await model.local.get('hub-cleanup')),[[profile.id,profile.id]]);
 const restarted=new ClientModel();restarted.local=model.local;let erased=0;restarted.vault={async remove(id){assert.equal(id,profile.id);erased++;}};await restarted.restoreCleanup();
 await assert.rejects(restarted.selectProfile(profile),/pending_action/);await assert.rejects(restarted.useEndpoint({hubId:profile.id},'again'),/pending_action/);await restarted.retryCleanup();assert.equal(erased,1);assert.equal(attempts,1);assert.equal(restarted.pendingCleanupCount,0);assert.deepEqual(JSON.parse(await restarted.local.get('hub-cleanup')),[]);
});

test('a cleanup journal failure retires the active context before any physical erasure',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.repository.close=()=>{};let deleted=0,purged=0;model.vault={async remove(){deleted++;}};model.local.purgeHub=async()=>{purged++;};const put=model.local.put.bind(model.local);model.local.put=async(key,value)=>{if(key==='hub-cleanup')throw Error('disk full');return put(key,value);};
 await assert.rejects(model.forgetProfile(profile.id),/cleanup_incomplete/);assert.equal(model.connected,false);assert.equal(model.profile.id,'');assert.equal(deleted,0);assert.equal(purged,0);assert.equal(model.profiles[0].id,profile.id);await assert.rejects(model.selectProfile(profile),/pending_action/);
 model.local.put=put;await model.retryCleanup();assert.equal(deleted,1);assert.equal(purged,1);assert.equal(model.pendingCleanupCount,0);
});

test('retrying another Hub cleanup does not stop the active Hub SSH lease or connection',async()=>{
 const {model}=fixture(),active=model.profile;model.profiles=[active];let stopped=0,closed=0;model.ssh.stop=()=>{stopped++;};model.repository.close=()=>{closed++;};model.vault={async remove(){}};model.cleanupProfiles.set('forgotten-hub','forgotten-identity');
 await model.retryCleanup();assert.equal(stopped,0);assert.equal(closed,0);assert.equal(model.profile,active);assert.equal(model.connected,true);assert.equal(model.pendingCleanupCount,0);
});

test('language commits reannounce capabilities on all live owners and a failed commit does not',async()=>{
 const {model}=fixture(),child=new ClientModel(),hello=[];model.views.set('child',child);child.connected=true;child.detached=true;model.repository.transport={hello:features=>hello.push(['root',features])};child.repository={transport:{hello:features=>hello.push(['child',features])}};
 await model.setLanguage('ja-JP');assert.deepEqual(hello.map(row=>row[0]),['root','child']);assert.equal(child.language,'ja-JP');hello.length=0;model.local.put=async()=>{throw Error('disk full');};await model.setLanguage('en-US');assert.deepEqual(hello,[]);assert.equal(child.language,'ja-JP');
});

test('follow-system retains this launch language and rollback restores it until cold start',async()=>{
 const {model}=fixture();globalThis.clientLanguageChanges=[];await model.setLanguage('en-US');await model.setLanguage('default');assert.equal(model.language,'default');assert.equal(clientLocale('zh-CN'),'en');
 model.local.put=async()=>{throw Error('disk full');};await model.setLanguage('ja-JP');assert.equal(model.language,'default');assert.equal(clientLocale('zh-CN'),'en');assert.deepEqual(globalThis.clientLanguageChanges,['en-US','default','ja-JP','en-US','default']);delete globalThis.clientLanguageChanges;
 setClientLanguage('default');assert.equal(clientLocale('zh-CN'),'zh-Hans','a new cold launch can apply the saved system preference');
});
test('prepared attachment sends use their captured payload exactly once and clear only the matching draft',async()=>{
 const {model,writes}=fixture(),images=[{mime:'image/png',data:'synthetic-base64'}],prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'prepared context and file path',images};
 assert.equal(await model.sendPrepared(prepared,model.draft,model.sourceContext),true);assert.equal(writes.length,1);assert.equal(writes[0][1],prepared.text);assert.deepEqual(writes[0][4],images);assert.equal(model.draft,'');
});
test('a prepared send is refused after owner, transport, or source draft changes',async()=>{
 for(const change of ['owner','repository','draft']){
  const {model,writes}=fixture(),draft=model.draft,context=model.sourceContext,prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'prepared',images:[]};
  if(change==='owner')model.profile=new HubProfile('another-hub');else if(change==='repository')model.repository={send:async()=>writes.push('wrong')};else model.draft='changed';
  assert.equal(await model.sendPrepared(prepared,draft,context),false);assert.equal(writes.length,0);
 }
});
test('a Persona notification keeps its conversation destination when the same runtime session is also in the fleet',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.sessions=[model.selected];const conversation=new ResourceRow(model.selected.id,'Persona','conversation');conversation.machine=model.selected.machine;conversation.session=model.selected.session;model.conversations=[conversation];model.refresh=async()=>{};
 await model.openNotification({profile:profile.id,manager:'personal',machine:conversation.machine,session:conversation.session});assert.equal(JSON.parse(model.notificationTarget).kind,'conversation');
});
test('an owner detail context uses isolated transports and drafts without persisting the workspace manager',async()=>{
 const {model}=fixture(),row=new ResourceRow('owned-chat','Other manager','conversation');row.manager='other';let registryWrites=0;model.local.put=async key=>{if(key==='profiles')registryWrites++;};
 const select=ClientModel.prototype.selectProfile;
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;this.connected=true;await this.saveProfiles();};
 try {
  const view=await model.viewFor(row);assert.notEqual(view,model);assert.equal(view.profile.manager,'other');assert.equal(model.profile.manager,'personal');assert.equal(view.local,model.local);assert.equal(view.detached,true);assert.equal(registryWrites,0);
  view.draft='other-manager draft';assert.equal(model.draft,'exact draft ');assert.equal(await model.viewFor(row),view);
 } finally {ClientModel.prototype.selectProfile=select;}
});
test('a pending owner context cannot be returned after the workspace changes',async()=>{
 const {model}=fixture(),row=new ResourceRow('owned-chat','','conversation'),wait=deferred();row.manager='other';const select=ClientModel.prototype.selectProfile;
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;await wait.promise;this.connected=true;};
 try {const opening=model.viewFor(row);model.profile=new HubProfile('different');wait.resolve();await assert.rejects(opening,/request_updated/);}
 finally {ClientModel.prototype.selectProfile=select;model.retireViews();}
});
test('forgetting a Hub retires its detail contexts before purging their data',async()=>{
 const {model}=fixture(),row=new ResourceRow('owned-chat','','conversation');row.manager='other';const select=ClientModel.prototype.selectProfile;model.profiles=[model.profile];model.repository.close=()=>{};
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;this.connected=true;};
 try {const view=await model.viewFor(row);let closed=0;view.repository={close(){closed++;}};const old=view.connectionGeneration(),scope=view.scope();await model.forgetProfile(model.profile.id);assert.equal(view.owns(old,scope),false);assert.equal(closed,1);assert.equal(model.views.size,0);ClientModel.prototype.selectProfile=select;await assert.rejects(view.selectProfile(view.profile),/pending_action/);}
 finally {ClientModel.prototype.selectProfile=select;}
});
test('resource handoff keeps its owner context and does not change the workspace manager',async()=>{
 const {model}=fixture(),row=new ResourceRow('todo-other','Other todo','todo');row.manager='other';const view=new ClientModel(),chat=new ResourceRow('owner-main','Owner main','conversation');view.profile=new HubProfile('synthetic-hub');view.profile.manager='other';view.selected=chat;view.openMain=async()=>chat;view.persistDraft=async()=>{};model.viewFor=async()=>view;
 assert.equal(await model.handoff(row),chat);assert.equal(model.profile.manager,'personal');assert.equal(model.sourceContext,contextReference);assert.equal(JSON.parse(view.sourceContext).contexts[0].manager,'other');
});
test('main chat selection that is cancelled by an owner switch cannot complete navigation',async()=>{
 const {model}=fixture(),wait=deferred(),main=new ResourceRow('main','Main','conversation');main.manager='personal';model.mainId=main.id;model.conversations=[main];model.refresh=async()=>{};model.select=async()=>{await wait.promise;};
 const opening=model.openMain();model.profile=new HubProfile('new-owner');wait.resolve();await assert.rejects(opening,/request_updated/);
});
test('handoff cannot append source context after its main selection is replaced',async()=>{
 for(const change of ['scope','target']){
  const {model}=fixture(),wait=deferred(),main=new ResourceRow('main','Main','conversation'),source=new ResourceRow('todo','Todo','todo');source.manager='personal';main.manager='personal';model.selected=main;model.openMain=()=>wait.promise;let persisted=0;model.persistDraft=async()=>{persisted++;};
  const handing=model.handoff(source);await new Promise(resolve=>setImmediate(resolve));
  if(change==='scope')model.profile=new HubProfile('other-owner');else model.selected=new ResourceRow('another-chat','','conversation');model.sourceContext='new source';model.draft='new draft';wait.resolve(main);
  await assert.rejects(handing,/request_updated/);assert.equal(model.sourceContext,'new source');assert.equal(model.draft,'new draft');assert.equal(persisted,0);
 }
});
test('overlapping foreground restores do not reuse a manager connection task from an earlier root generation',async()=>{
 const {model}=fixture(),target=new ResourceRow('owned','','conversation');target.manager='other';const gates=[deferred(),deferred()];let calls=0;const select=ClientModel.prototype.selectProfile;
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;await gates[calls++].promise;this.connected=true;};
 try {
  const previous=model.viewFor(target);model.generation++;const latest=model.viewFor(target);assert.equal(calls,2);
  gates[0].resolve();await assert.rejects(previous,/request_updated/);gates[1].resolve();const view=await latest;assert.equal(view.profile.manager,'other');assert.equal(view.connected,true);assert.equal(model.profile.manager,'personal');
 } finally {ClientModel.prototype.selectProfile=select;model.retireViews();}
});
test('an ACK clears its persisted draft even after leaving the destination, while another draft stays intact',async()=>{
 const {model}=fixture(),gate=deferred(),admitted=deferred(),original=model.selected;model.repository.send=async()=>{admitted.resolve();await gate.promise;};const target=model.target();
 const sending=model.send();await admitted.promise;model.selected=new ResourceRow('other','Other','session');model.selected.manager='personal';model.updateDraft('Other draft');await model.persistDraft();gate.resolve();assert.equal(await sending,true);
 assert.equal(model.draft,'Other draft');assert.equal(JSON.parse(await model.local.get('draft:'+target)).text,'');await model.select(original);assert.equal(model.draft,'');
});
test('deleting and retyping identical words while a send is pending creates a new draft lineage',async()=>{
 const {model}=fixture(),gate=deferred(),admitted=deferred(),text=model.draft;model.repository.send=async()=>{admitted.resolve();await gate.promise;};const sending=model.send();await admitted.promise;const sentRevision=model.draftRevision;
 model.updateDraft('');model.updateDraft(text);const newer=model.draftRevision;assert.notEqual(newer,sentRevision);await model.persistDraft();gate.resolve();assert.equal(await sending,true);
 assert.equal(model.draft,text);assert.equal(model.draftRevision,newer);assert.equal(JSON.parse(await model.local.get('draft:'+model.target())).revision,newer);model.cancelDraftTimer();
});
test('an uncertain message blocks only its own destination and a different chat can send',async()=>{
 const {model}=fixture(),original=model.selected;model.repository.send=async()=>{throw new HttpFailure(202,true,true);};assert.equal(await model.send(),false);assert.equal(model.operationState,'pending');
 const other=new ResourceRow('other','Other','session');other.manager='personal';other.machine='machine-one';other.session=other.id;await model.select(other);assert.equal(model.operationState,'');model.updateDraft('Other message');model.repository.send=async()=>{};assert.equal(await model.send(),true);
 await model.select(original);assert.equal(model.operationState,'pending');assert.equal(await model.send(),false);
});
test('optimistic user content is visible before ACK and only the exact server echo replaces it',async()=>{
 const {model}=fixture(),gate=deferred(),admitted=deferred();let op,text;model.repository.send=async(row,body,id)=>{text=body;op=id;admitted.resolve();await gate.promise;};
 const sending=model.send();await admitted.promise;assert.ok(model.messages.some(m=>m.id==='local:'+op&&m.deliveryState==='pending'));gate.resolve();assert.equal(await sending,true);
 model.publishTranscript([{id:'unrelated',role:'user',kind:'text',text,seq:1,client_msg_id:'another-op'}],false);assert.ok(model.messages.some(m=>m.id==='local:'+op));
 model.publishTranscript([{id:'server-echo',role:'user',kind:'text',text,seq:2,client_msg_id:op}],false);assert.equal(model.messages.filter(m=>m.role==='user').length,1);assert.equal(model.messages[0].id,'server-echo');assert.equal(model.messages.some(m=>m.id==='local:'+op),false);
 await new Promise(resolve=>setImmediate(resolve));
});
test('changing destination during local outbox admission prevents dispatch to either destination',async()=>{
 const {model,writes}=fixture(),gate=deferred(),reading=deferred(),get=model.local.get.bind(model.local);model.local.get=async key=>{if(key.startsWith('outbox:')){reading.resolve();await gate.promise;}return get(key);};
 const sending=model.send();await reading.promise;model.selected=new ResourceRow('new','New','session');model.updateDraft('New draft');gate.resolve();assert.equal(await sending,false);assert.equal(writes.length,0);assert.equal(model.draft,'New draft');model.cancelDraftTimer();
});
test('a crash between local staging and transport admission restores an explicit uncertain result without replay',async()=>{
 const {model,writes}=fixture(),scope=model.scope(),target=model.target(),{ChatDraftStore,ChatOutboxEntry}=await import('../entry/src/main/ets/models/ChatDraftStore.ets'),store=new ChatDraftStore(model.local),entry=new ChatOutboxEntry();entry.id='crash-before-admission';entry.target=target;entry.text='Unsent synthetic text';entry.draft=entry.text;entry.revision='crash-draft';await store.stage(scope,entry);
 await model.select(model.selected);assert.equal(model.operationState,'unknown');assert.equal(writes.length,0);assert.equal(model.canCheckLocalMessage(entry.id),true);await model.acknowledgeWrite(entry.id);assert.equal(model.operationState,'failed');
 model.updateDraft('Another draft');await model.restoreMessageDraft(entry.id);assert.equal(model.draft,entry.draft);assert.equal(writes.length,0);model.cancelDraftTimer();
});
test('a persisted outbox receipt can clear its original draft even when no operation journal entry survived',async()=>{
 const {model,writes}=fixture(),scope=model.scope(),target=model.target(),{ChatDraftStore,ChatOutboxEntry,ChatDraft}=await import('../entry/src/main/ets/models/ChatDraftStore.ets'),store=new ChatDraftStore(model.local),entry=new ChatOutboxEntry(),draft=new ChatDraft();entry.id='receipt-only';entry.target=target;entry.revision='sent-version';entry.text='Sent synthetic text';entry.draft=entry.text;draft.text=entry.text;draft.revision=entry.revision;await store.save(target,draft);await store.stage(scope,entry);
 model.features=['client_ops'];model.repository.call=async path=>{assert.equal(path,'/api/v1/ops/'+entry.id);return{state:'done'};};const restoredRow=model.selected;model.selected=new ResourceRow();model.draft='';model.sourceContext='';await model.select(restoredRow);assert.equal(model.operationState,'unknown');await model.reconcileWrites();assert.equal(model.operationState,'done');assert.equal(model.draft,'');assert.equal(writes.length,0);assert.equal((await store.read(target)).text,'');
});
test('plain text and attachment composers enforce the same long-message bound before persistence or dispatch',async()=>{
 const {model,writes}=fixture(),old=model.draft;model.updateDraft('x'.repeat(100001));assert.equal(model.draft,old);assert.equal(model.error,'chat_attachment_text_too_large');model.draft='x'.repeat(100001);assert.equal(await model.send(),false);assert.equal(writes.length,0);
});
test('an exact photo-message echo before HTTP timeout proves delivery and returns a successful attachment ACK',async()=>{
 const {model}=fixture(),gate=deferred(),admitted=deferred();model.draft='';model.sourceContext='';let op;model.repository.send=async(row,text,id)=>{op=id;admitted.resolve();await gate.promise;};
 const prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'',images:[{mime:'image/jpeg',data:'synthetic'}]};const sending=model.sendPrepared(prepared,'','');await admitted.promise;
 model.publishTranscript([{id:'photo-echo',role:'user',kind:'text',text:'<attached_images>\n/tmp/photo.jpg\n</attached_images>',seq:1,client_msg_id:op}],false);gate.reject(new HttpFailure(0,true));assert.equal(await sending,true);assert.equal(model.draft,'');assert.ok(!['unknown','pending'].includes(model.operationState));assert.equal(model.messages.some(m=>m.id==='local:'+op),false);
});
test('a photo-only message stays visible locally after ACK when transcript refresh fails',async()=>{
 const {model}=fixture();model.draft='';model.sourceContext='';model.loadChat=async()=>{throw new Error('read failed');};
 const prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'',images:[{mime:'image/jpeg',data:'one'},{mime:'image/png',data:'two'}]};assert.equal(await model.sendPrepared(prepared,'',''),true);assert.equal(model.messages.length,1);assert.equal(model.messages[0].localImageCount,2);assert.equal(model.messages[0].deliveryState,'done');
});
test('read-only metadata received while the local message is being staged revokes dispatch',async()=>{
 const {model,writes}=fixture(),gate=deferred(),entered=deferred(),get=model.local.get.bind(model.local);model.canSend=()=>ClientModel.prototype.canSend.call(model);model.selected.raw=JSON.stringify({source_kind:'live',capabilities:{read:true,send:true}});assert.equal(model.canSend(),true);
 model.local.get=async key=>{if(key.startsWith('outbox:')){entered.resolve();await gate.promise;}return get(key);};const sending=model.send();await entered.promise;model.selected.raw=JSON.stringify({source_kind:'live',read_only:true,capabilities:{read:true,send:false}});gate.resolve();assert.equal(await sending,false);assert.equal(writes.length,0);assert.equal(model.operationState,'failed');model.selected.raw=JSON.stringify({source_kind:'live',capabilities:{read:true,send:true}});assert.equal(await model.send(),true);assert.equal(writes.length,1);
});
test('composer limits count UTF-8 Chinese and emoji plus source-context bytes',async()=>{
 for(const text of ['界'.repeat(40000),'🌍'.repeat(30000)]){const {model,writes}=fixture();model.draft=text;assert.equal(await model.send(),false);assert.equal(writes.length,0);}
 const {model,writes}=fixture();model.draft='界'.repeat(33300);model.sourceContext=JSON.stringify({kind:'todo',title:'Source',id:'x'.repeat(400)});assert.equal(await model.send(),false);assert.equal(model.error,'chat_attachment_request_too_large');assert.equal(writes.length,0);
});

test('an exact echo after leaving and reopening the chat settles the original repository call despite its late timeout',async()=>{
 const {model}=fixture(),{HubRepository}=await import('../entry/src/main/ets/models/HubRepository.ets'),repo=new HubRepository({managerId:'personal',hubId:model.profile.id},model.local),gate=deferred(),entered=deferred(),original=model.selected;model.repository=repo;model.draft='';model.sourceContext='';let op;
 globalThis.clientTestRequest=async(path,method,body,id)=>{op=id;entered.resolve();return gate.promise;};
 const prepared={repository:repo,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:original,text:'',images:[{mime:'image/jpeg',data:'synthetic'}]},sending=model.sendPrepared(prepared,'','');await entered.promise;
 const other=new ResourceRow('other','Other','session');other.manager='personal';await model.select(other);await model.select(original);
 model.publishTranscript([{id:'navigated-photo-echo',role:'user',kind:'text',text:'<attached_images>\n/tmp/photo.jpg\n</attached_images>',seq:1,client_msg_id:op}],false);
 await new Promise(resolve=>setImmediate(resolve));gate.reject(new HttpFailure(0,true));assert.equal(await sending,true);assert.ok(!['unknown','pending'].includes(model.operationState));assert.equal(repo.mutations.blocked().length,0);assert.equal(model.messages.some(m=>m.id==='local:'+op),false);
});
test('Checked obtains a fresh server receipt and cannot release a completion lost from an older local unknown row',async()=>{
 const {model}=fixture(),{HubRepository}=await import('../entry/src/main/ets/models/HubRepository.ets'),repo=new HubRepository({managerId:'personal',hubId:model.profile.id},model.local);model.repository=repo;const record=await repo.mutations.begin('/messages','POST','body');await repo.mutations.finish(record,'unknown');model.unresolvedWrites=repo.mutations.blocked();model.features=['client_ops'];let lookups=0;
 globalThis.clientTestRequest=async path=>{assert.equal(path,'/api/v1/ops/'+record.id);lookups++;return '{"state":"done"}';};await model.acknowledgeWrite(record.id);assert.equal(lookups,1);assert.equal(record.state,'done');assert.equal(repo.mutations.blocked().length,0);
});
test('a queued echo completion is observed after the journal barrier before a late transport error propagates',async()=>{
 const {model}=fixture(),{HubRepository}=await import('../entry/src/main/ets/models/HubRepository.ets'),repo=new HubRepository({managerId:'personal',hubId:model.profile.id},model.local),network=deferred(),requested=deferred(),held=deferred(),queued=deferred(),original=model.selected;model.repository=repo;model.draft='';model.sourceContext='';let op;
 globalThis.clientTestRequest=async(path,method,body,id)=>{op=id;requested.resolve();return network.promise;};const prepared={repository:repo,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:original,text:'',images:[{mime:'image/png',data:'synthetic'}]},sending=model.sendPrepared(prepared,'','');await requested.promise;
 const other=new ResourceRow('other','Other','session');other.manager='personal';await model.select(other);await model.select(original);
 const put=model.local.put.bind(model.local),scope=model.scope();model.local.put=async(key,value,...rest)=>{if(key==='mutations:'+scope&&JSON.parse(value).ids.length>1){queued.resolve();await held.promise;}return put(key,value,...rest);};
 const occupying=repo.mutations.begin('/unrelated','POST','');await queued.promise;
 model.publishTranscript([{id:'queued-photo-echo',role:'user',kind:'text',text:'<attached_images>\n/tmp/photo.png\n</attached_images>',seq:1,client_msg_id:op}],false);network.reject(new HttpFailure(0,true));held.resolve();await occupying;assert.equal(await sending,true);assert.ok(!['unknown','pending'].includes(model.operationState));assert.equal(repo.mutations.blocked().some(r=>r.id===op),false);
});
test('client_ops missing receipt can release only a user-checked local-only staged message without replay',async()=>{
 const {model,writes}=fixture(),scope=model.scope(),{ChatDraftStore,ChatOutboxEntry}=await import('../entry/src/main/ets/models/ChatDraftStore.ets'),entry=new ChatOutboxEntry();entry.id='local-only-404';entry.target=model.target();entry.text='Local draft';entry.draft=entry.text;entry.revision='local-only-version';await new ChatDraftStore(model.local).stage(scope,entry);model.features=['client_ops'];model.repository.call=async()=>{throw new HttpFailure(404,'not_found',false);};await model.select(model.selected);assert.equal(model.canCheckLocalMessage(entry.id),true);await model.acknowledgeWrite(entry.id);assert.equal(model.operationState,'failed');assert.equal(writes.length,0);await model.restoreMessageDraft(entry.id);assert.equal(model.draft,'Local draft');assert.equal(writes.length,0);
});


test('an exact echo after a timed-out photo send clears the captured attachment selection after reopening',async()=>{
 const {model}=fixture();model.context={filesDir:'/app/files'};globalThis.clientAttachmentCommits=[];
 model.repository.send=async()=>{throw new HttpFailure(0,true);};model.draft='';model.sourceContext='';
 const snapshot={target:JSON.stringify(['synthetic-hub','personal','machine-one','chat-one']),selectionRevision:1,attachments:[{id:'image-one',revision:1}]};
 const prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'',images:[{mime:'image/png',data:'synthetic'}],snapshot};
 assert.equal(await model.sendPrepared(prepared,'',''),false);assert.equal(globalThis.clientAttachmentCommits.length,0);
 const entry=model.localMessages[0];model.localMessages=[];await model.restoreLocalDelivery();
 model.projectMessages=()=>[];model.publishTranscript([{id:'server-user',role:'user',client_msg_id:entry.id}],false);
 for(let i=0;i<20;i++)await new Promise(resolve=>setImmediate(resolve));
 assert.equal(globalThis.clientAttachmentCommits.length,1);assert.equal(globalThis.clientAttachmentCommits[0][1],JSON.stringify(snapshot));
 assert.equal((await model.local.get('outbox:'+model.scope())).includes(entry.id),false);assert.equal(model.draft,'');
 delete globalThis.clientAttachmentCommits;
});


test('confirmed photo delivery suppresses its selection before failed draft cleanup or preview-store initialization',async()=>{
 const {model}=fixture();model.context={filesDir:'/app/files'};globalThis.clientAttachmentSuppressed=[];
 model.draft='';model.sourceContext='';const snapshot={target:JSON.stringify(['synthetic-hub','personal','machine-one','chat-one']),selectionRevision:1,attachments:[{id:'image-one',revision:1}]};
 const prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'',images:[{mime:'image/png',data:'synthetic'}],snapshot};
 const oldPut=model.local.put.bind(model.local);let admitted=false;
 model.local.put=async(k,v)=>{if(admitted&&k.startsWith('draft:'))throw Error('disk full');return oldPut(k,v);};model.repository.send=async()=>{admitted=true;};
 assert.equal(await model.sendPrepared(prepared,'',''),true);assert.equal(globalThis.clientAttachmentSuppressed.length,1);assert.equal(globalThis.clientAttachmentSuppressed[0][0],JSON.stringify(snapshot));assert.equal(model.error,'cache_unavailable');delete globalThis.clientAttachmentSuppressed;
});


test('restoring multiple delivered selections cannot enter a replacement pairing after the first awaits cleanup',async()=>{
 const {model}=fixture(),scope=model.scope(),{ChatDraftStore,ChatOutboxEntry}=await import('../entry/src/main/ets/models/ChatDraftStore.ets'),store=new ChatDraftStore(model.local),entered=deferred(),release=deferred();let epoch=0;
 model.context={filesDir:'/app/files'};model.local.revision=()=>epoch;model.local.accepts=(key,captured)=>captured===epoch;globalThis.clientAttachmentSuppressed=[];globalThis.clientAttachmentCommits=[];
 for(const id of ['first','second']){const entry=new ChatOutboxEntry();entry.id=id;entry.target=model.target();entry.attachmentSnapshot=JSON.stringify({target:JSON.stringify(['synthetic-hub','personal','machine-one','chat-one']),attachments:[{id,revision:1}]});await store.stage(scope,entry);await store.settle(scope,id,'done');}
 globalThis.clientAttachmentCommitGate=async()=>{entered.resolve();await release.promise;};
 const restoring=model.restoreLocalDelivery();await entered.promise;epoch++;model.local.data.clear();model.repository={async send(){}};model.draft='Fresh pairing draft';model.draftRevision='fresh';model.operationState='fresh';release.resolve();await restoring;
 assert.equal(globalThis.clientAttachmentSuppressed.length,1);assert.equal(globalThis.clientAttachmentCommits.length,1);assert.equal(model.draft,'Fresh pairing draft');assert.equal(model.operationState,'fresh');
 delete globalThis.clientAttachmentSuppressed;delete globalThis.clientAttachmentCommits;delete globalThis.clientAttachmentCommitGate;
});


test('dedicated resource destinations do not issue a generic knowledge or unsupported todo detail read',async()=>{
 for(const kind of ['todo','knowledge','inbox','file','todo_source','todo_thread']){
  const {model}=fixture();let calls=0;model.repository.resource=async()=>{calls++;throw Error('wrong endpoint');};
  const row=new ResourceRow('resource','Resource',kind);row.manager=model.profile.manager;await model.select(row);assert.equal(calls,0);assert.equal(model.error,'');assert.equal(model.selected.kind,kind);
 }
});

test('identical manager and session ids in two paired Hubs retain independent detail contexts',async()=>{
 const {model}=fixture(),other=new HubProfile('second-hub','Second Hub','https://second.invalid');other.manager='personal';model.profiles=[model.profile,other];
 const row=new ResourceRow('same-chat','','session');row.group=other.id;row.manager='personal';row.machine='same-machine';
 const select=ClientModel.prototype.selectProfile;
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;this.connected=true;};
 try {const view=await model.viewFor(row);assert.notEqual(view,model);assert.equal(view.profile.id,'second-hub');assert.equal(view.profile.baseUrl,other.baseUrl);assert.equal(model.profile.id,'synthetic-hub');assert.equal(model.directoryOwnerFor(row),view);
  const own=new ResourceRow(row.id,'','session');own.group=model.profile.id;own.manager=row.manager;own.machine=row.machine;assert.notEqual(row.key(),own.key());
  await assert.rejects(model.select(row),/request_updated/);
 }finally{ClientModel.prototype.selectProfile=select;model.retireViews();}
});
test('directory hydration visits every paired Hub and manager without changing the chosen workspace',async()=>{
 const {model}=fixture(),chosen=model.profile,other=new HubProfile('second-hub','Second Hub');other.manager='personal';model.profiles=[chosen,other];model.foreground=true;
 const calls=[],make=(profile,manager)=>{const view=new ClientModel();view.profile=new HubProfile(profile.id,profile.name);view.profile.manager=manager;view.connected=true;view.managers=manager==='personal'?[new ResourceRow('personal'),new ResourceRow('work')]:[];view.refresh=async()=>{};return view;};
 const ownManagers=[new ResourceRow('personal'),new ResourceRow('work')];model.managers=ownManagers;model.refresh=async()=>{};
 model.viewFor=async row=>{calls.push([row.group,row.manager]);return row.group===chosen.id&&row.manager==='personal'?model:make(model.profiles.find(p=>p.id===row.group),row.manager);};
 await model.refreshDirectory();assert.deepEqual(calls,[[chosen.id,'personal'],[chosen.id,'work'],[other.id,'personal'],[other.id,'work']]);assert.equal(model.profile,chosen);assert.equal(model.directoryReady,true);assert.equal(model.directoryLoading,false);
});
test('a superseded directory pass cannot publish completion into a new workspace',async()=>{
 const {model}=fixture(),gate=deferred();model.profiles=[model.profile];model.viewFor=()=>gate.promise;
 const old=model.refreshDirectory();model.generation++;model.directoryReady=false;gate.resolve(model);await old;assert.equal(model.directoryReady,false);assert.equal(model.directoryTask,undefined);
});

test('aggregate serialization retains needs-you, working and unread session states',()=>{const {model}=fixture();model.profiles=[model.profile];model.sessions=['needs_you','working','unread'].map(state=>{const row=new ResourceRow(state,state,'session');row.state=state;row.manager=model.profile.manager;row.machine='same-machine';return row;});model.publishDirectory();assert.deepEqual(model.directorySessions.map(row=>row.state),['needs_you','working','unread']);assert.ok(model.directorySessions.every(row=>row.group===model.profile.id));});

test('the local experience uses one mutable repository across chat, todo and Agent flows without transport',async()=>{const model=new ClientModel();model.ready=true;model.foreground=true;let network=0;globalThis.clientTestRequest=async()=>{network++;throw Error('demo must stay local');};model.useDemo();assert.equal(model.profile.manager,'demo');assert.equal(model.repo(),model.demoRepository);const main=await model.openMain();assert.equal(model.canSend(),true);model.updateDraft('Synthetic local message');assert.equal(await model.send(),true);assert.ok(model.messages.some(message=>message.role==='user'&&message.text==='Synthetic local message'));const other=await model.newConversation();assert.notEqual(other.id,main.id);assert.equal(model.messages.some(message=>message.text==='Synthetic local message'),false);await model.select(main);assert.ok(model.messages.some(message=>message.text==='Synthetic local message'));const todo=await model.repo().call('/api/v1/todos','POST',{title:'Local task',notes:'Synthetic'});await model.listResources('todo');assert.ok(model.resourceRows.some(row=>row.id===todo.todo.id));const agent=await model.createAgent('Local Agent',model.machines[0],'/demo/ReleaseDesk','',todo.todo.id);assert.ok(model.sessions.some(row=>row.id===agent.id));assert.equal(agent.group,'demo');assert.equal(network,0);model.cancelDraftTimer();});

test('application language uses the native API and serializes device preferences with appearance changes',async()=>{const {model}=fixture(),gate=deferred(),entered=deferred();globalThis.clientLanguageChanges=[];const put=model.local.put.bind(model.local);let first=true;model.local.put=async(key,value)=>{if(key==='preferences'&&first){first=false;entered.resolve();await gate.promise;}return put(key,value);};const appearance=model.setAppearance(2,'brief',false);await entered.promise;const language=model.setLanguage('ja-JP');assert.deepEqual(globalThis.clientLanguageChanges,[]);gate.resolve();await appearance;await language;const saved=JSON.parse(await model.local.get('preferences'));assert.equal(saved.appearance,2);assert.equal(saved.style,'brief');assert.equal(saved.alerts,false);assert.equal(saved.language,'ja-JP');assert.equal(model.language,'ja-JP');assert.deepEqual(globalThis.clientLanguageChanges,['ja-JP']);assert.equal(model.languageChanging,false);await model.setLanguage('not-supported');assert.deepEqual(globalThis.clientLanguageChanges,['ja-JP']);delete globalThis.clientLanguageChanges;});

test('queued appearance, style and notification edits merge only their changed fields',async()=>{const {model}=fixture(),gate=deferred(),entered=deferred(),put=model.local.put.bind(model.local);let first=true;model.local.put=async(key,value)=>{if(key==='preferences'&&first){first=false;entered.resolve();await gate.promise;}return put(key,value);};const mode=model.setAppearance(2);await entered.promise;const style=model.setStyle('brief'),alerts=model.setAlerts(false);gate.resolve();await Promise.all([mode,style,alerts]);const saved=JSON.parse(await model.local.get('preferences'));assert.equal(saved.appearance,2);assert.equal(saved.style,'brief');assert.equal(saved.alerts,false);});
test('a fallback profiles write returning after owner change or background cannot send a bearer probe',async()=>{
 for(const cancel of ['owner','background']){const model=new ClientModel(),profile=new HubProfile('fallback-hub','','https://primary.invalid/');profile.hubId=profile.id;profile.pin='A'.repeat(43);profile.manager='personal';profile.addresses=['https://secondary.invalid/'];model.profile=profile;model.profiles=[profile];model.foreground=true;model.vault={async get(){return'synthetic-device-token';}};const gate=deferred(),entered=deferred(),put=model.local.put.bind(model.local);let writes=0,requests=0;model.local.put=async(key,value)=>{if(key==='profiles'&&++writes===2){entered.resolve();await gate.promise;}return put(key,value);};globalThis.clientPairingAddress=async()=> 'https://secondary.invalid/';globalThis.clientTestRequest=async()=>{requests++;return'{}';};const connecting=model.selectProfile(profile,true);await entered.promise;if(cancel==='owner'){model.generation++;model.profile=new HubProfile('new-context');}else model.foreground=false;gate.resolve();await connecting;assert.equal(requests,0);delete globalThis.clientPairingAddress;}
});
test('pairing redemption cannot commit credentials after its initiating context is replaced',async()=>{const {model}=fixture(),gate=deferred(),entered=deferred();model.foreground=true;let puts=0,selected=0;model.vault={async get(){return null;},async put(){puts++;}};model.useEndpoint=async()=>{selected++;};const target={hubId:'paired-a',name:'A'},pairing={async redeem(_target,_name,admit){entered.resolve();await gate.promise;return{hubId:'paired-a',token:'synthetic-issued'};}};const old=model.pair(pairing,target);await entered.promise;model.generation++;model.profile=new HubProfile('chosen-c');gate.resolve();await assert.rejects(old,/request_updated/);assert.equal(puts,0);assert.equal(selected,0);assert.equal(model.profile.id,'chosen-c');});
test('cancelled credential admission restores the earlier token without selecting the new Hub',async()=>{const {model}=fixture(),gate=deferred(),entered=deferred();let stored='previous-token',allowed=true,selected=0;model.vault={async get(){return stored;},async put(_id,value){stored=value;if(value==='new-token'){entered.resolve();await gate.promise;}},async remove(){stored=null;}};model.selectProfile=async()=>{selected++;};const saving=model.useEndpoint({hubId:'target-hub',token:'new-token',baseUrl:'https://target.invalid/',pin:'',managerId:''},'Target',()=>allowed);await entered.promise;allowed=false;gate.resolve();await assert.rejects(saving,/request_updated/);assert.equal(stored,'previous-token');assert.equal(selected,0);});

test('demo answer and stop acknowledgements refresh the selected roster and retain its draft',async()=>{
 const model=new ClientModel();model.useDemo();const review=model.sessions.find(row=>row.id==='demo-agent-review');assert.ok(review);await model.select(review);
 assert.equal(model.decision?.id,'demo-plan-review');model.draft='Keep this draft';model.sourceContext='Keep this source';
 model.loadChat=async()=>{throw new Error('follow-up read failed');};await model.answer('approve',{},model.decision);
 assert.equal(model.operationState,'done');assert.equal(model.decision.status,'resolved');assert.equal(model.sessions.find(row=>row.id===review.id).state,'working');
 assert.equal(model.selected.state,'working');assert.equal(model.draft,'Keep this draft');assert.equal(model.sourceContext,'Keep this source');
 await model.stop();assert.equal(model.sessions.find(row=>row.id===review.id).state,'idle');assert.equal(model.selected.state,'idle');
 assert.equal(model.sessions.find(row=>row.id===review.id).data().status,'interrupted');assert.equal(model.draft,'Keep this draft');assert.equal(model.sourceContext,'Keep this source');
});

test('the demo todo workspace projects its waiting agent and opens the linked plan',async()=>{
 const model=new ClientModel();model.useDemo();const workspace=new TodoWorkspace();await workspace.open(model);
 const waiting=workspace.items.find(todo=>todo.id==='demo-todo-release');assert.ok(waiting);assert.equal(workspace.state(waiting),'needs_you');
 await workspace.openRow(waiting);
 const agent=waiting.activeAgents()[0];assert.ok(agent);assert.equal(agent.sessionId,'demo-agent-review');
 const session=workspace.agentSession(agent);assert.equal(session.id,'demo-agent-review');assert.equal(session.machine,'demo-mac');
 await model.select(session);assert.equal(model.decision?.id,'demo-plan-review');assert.equal(model.decision?.status,'open');
});

test('InboxWorkspace sends a reply into the opened demo thread with its validated root',async()=>{
 const model=new ClientModel();model.useDemo();const workspace=new InboxWorkspace();await workspace.open(model);
 const row=workspace.items.find(item=>item.id==='demo-inbox-lark');assert.ok(row);await workspace.openRow(row);
 assert.equal(workspace.isGeneric(),true);const root=workspace.feedMessages.find(message=>message.id==='demo-lark-message-1');assert.ok(root);
 await workspace.openThread(root);assert.equal(workspace.threadRoot,root.id);assert.equal(workspace.threadReplyTo,root.id);
 assert.equal(await workspace.sendConversation('I reviewed the sample plan.',root.id,true),true);
 const reply=workspace.feedMessages.find(message=>message.text==='I reviewed the sample plan.');assert.ok(reply);
 assert.equal(reply.data().thread_root,root.id);assert.equal(reply.data().reply_to,root.id);assert.equal(workspace.threadRoot,root.id);
});


test('assistant cards capture the actual Client machine identity and forget before durable cleanup',async()=>{
 const {AssistantViewModel}=await import('../entry/src/main/ets/models/AssistantView.ets');
 const {model}=fixture(),profile=model.profile,wait=deferred();model.selected.kind='conversation';model.selected.group=profile.id;model.selected.manager='personal';model.features=['assistant_views'];model.profiles=[profile];model.repository.close=()=>{};model.local.purgeHub=async()=>wait.promise;model.vault={async remove(){}};
 const key='view_real_client_owner',envelope={id:key,conversation:model.selected.id,version:1,view:{fallback:'Synthetic private card',sections:[{items:[{type:'button',key:'choose',title:'Choose',action:{id:'choose',reply:'Choose'}}]}]}};
 const feed={owner:'personal',conversationId:model.selected.id,views:{[key]:envelope},viewActions:{},viewBlocks:[]};
 const card=AssistantViewModel.acquire(model,feed,{id:'position',kind:'view',ref:key},key,true);
 assert.equal(card.canInteract(),true);assert.equal(card.fallbackText,'Synthetic private card');card.hide();
 const forgetting=model.forgetProfile(profile.id);assert.equal(card.active,false);assert.equal(card.fallbackText,'');assert.deepEqual(card.view,{});assert.equal(card.canRetryOriginal,false);
 wait.resolve();await forgetting;
});

test('SSH metadata follows the paired Hub across canonical manager selection and stays separate from manager views',async()=>{
 const {model}=fixture();await model.saveView('ssh-tunnel','{"enabled":true}');await model.saveView('filters','personal');
 model.profile.manager='work';assert.equal(await model.loadView('ssh-tunnel'),'{"enabled":true}');assert.equal(await model.loadView('filters'),'');
 model.profile=new HubProfile('different-hub');assert.equal(await model.loadView('ssh-tunnel'),'');
});

test('detached-owner reconnect reconstructs its profile while retaining the shared pairing lease',async()=>{
 const {model}=fixture(),row=new ResourceRow('secondary','','conversation');row.manager='work';
 const select=ClientModel.prototype.selectProfile;
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;this.connected=true;};
 try {
  const first=await model.viewFor(row),profile=first.profile,revision=first.pairingRevision();first.connected=false;
  const again=await model.viewFor(row);assert.equal(first,again);assert.notEqual(again.profile,profile);assert.equal(again.pairingRevision(),revision);
  model.retirePairing(model.profile.id);assert.notEqual(again.pairingRevision(),revision);
 }finally{ClientModel.prototype.selectProfile=select;model.retireViews();}
});

test('enabled SSH startup failures stop Hub discovery before any direct probe or authenticated request',async()=>{
 const model=new ClientModel(),profile=new HubProfile('ssh-fail-hub','','https://target.invalid/');profile.hubId=profile.id;profile.pin='A'.repeat(43);profile.manager='work';
 model.profile=profile;model.profiles=[profile];model.foreground=true;model.vault={async get(){return 'synthetic';}};
 let probes=0,requests=0;globalThis.clientSshAcquire=async()=>{throw new Error('ssh_host_key_mismatch');};globalThis.clientPairingAddress=async()=>{probes++;return profile.baseUrl;};globalThis.clientTestRequest=async()=>{requests++;return '{}';};
 try {await model.selectProfile(profile,true);assert.equal(probes,0);assert.equal(requests,0);assert.equal(model.connected,false);}
 finally{delete globalThis.clientSshAcquire;delete globalThis.clientPairingAddress;model.stopReconnect();}
});

test('Hub connection retirement closes the active owner and every retained manager while preserving other Hubs',async()=>{
 const {model}=fixture(),row=new ResourceRow('secondary','','conversation');row.manager='work';model.profiles=[model.profile];let rootClosed=0,secondaryClosed=0,foreignClosed=0;model.repository={close(){rootClosed++;}};
 const select=ClientModel.prototype.selectProfile;ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;this.connected=true;};
 try{
  const secondary=await model.viewFor(row);secondary.repository={close(){secondaryClosed++;}};
  const foreign=new ClientModel();foreign.profile=new HubProfile('foreign');foreign.connected=true;foreign.repository={close(){foreignClosed++;}};model.views.set('foreign',foreign);
  const before=model.connectionGeneration();model.retireHubConnections(model.profile.id);assert.equal(rootClosed,1);assert.equal(secondaryClosed,1);assert.equal(foreignClosed,0);assert.equal(model.connected,false);assert.equal(secondary.retired,true);assert.equal(model.connectionGeneration()>before,true);assert.equal(model.views.has('foreign'),true);
 }finally{ClientModel.prototype.selectProfile=select;model.stopReconnect();model.retireViews();}
});

test('committed network settings replace an old reconnect timer even when no settings view remains',async()=>{
 const {model}=fixture();model.repository={close(){model.scheduleReconnect();}};model.connected=false;let attempts=0;
 model.selectProfile=async()=>{attempts++;model.connected=true;};model.scheduleReconnect();
 model.retireHubConnections(model.profile.id);
 try {await new Promise(resolve=>setTimeout(resolve,1200));assert.equal(attempts,1);assert.equal(model.connected,true);}
 finally{model.stopReconnect();}
});

test('slow credential admission keeps its generation while SSH offline callbacks request reconnection',async()=>{
 const {model}=fixture(),entered=deferred(),wait=deferred(),profile=model.profile;let selected=0,reads=0;
 model.foreground=true;model.ssh.stop=()=>model.scheduleReconnect();model.repository={close(){model.scheduleReconnect();}};
 model.vault={async get(){if(++reads===1){entered.resolve();await wait.promise;}return 'previous';},async put(){},async remove(){}};
 model.selectProfile=async function(next){selected++;this.profile=next;this.generation++;this.connected=true;};
 const pairing={async redeem(){return{hubId:profile.id,baseUrl:'https://replacement.invalid/',token:'new-synthetic',pin:'A'.repeat(43),managerId:profile.manager};}};
 const task=model.pair(pairing,{hubId:profile.id,name:'Replacement'});await entered.promise;const admitted=model.connectionGeneration();
 try {model.scheduleReconnect();await new Promise(resolve=>setTimeout(resolve,1200));assert.equal(model.connectionGeneration(),admitted);assert.equal(selected,0);wait.resolve();await task;assert.equal(selected,1);assert.equal(model.profile.baseUrl,'https://replacement.invalid/');}
 finally{wait.resolve();await task.catch(()=>{});model.stopReconnect();}
});

test('language publication waits for storage and a failed save restores the previous native preference',async()=>{
 const {model}=fixture();model.language='en-US';globalThis.clientLanguageChanges=[];const entered=deferred(),gate=deferred();model.local.put=async()=>{entered.resolve();await gate.promise;throw Error('storage unavailable');};
 const changing=model.setLanguage('ja-JP');await entered.promise;assert.equal(model.language,'en-US');gate.resolve();await changing;
 assert.equal(model.language,'en-US');assert.deepEqual(globalThis.clientLanguageChanges,['ja-JP','en-US']);assert.equal(model.error,'cache_unavailable');delete globalThis.clientLanguageChanges;
});

test('captured outcome storage keeps its original scope and is retired by that scope purge',async()=>{
  const {model}=fixture();const retired=new Set();model.local.accepts=key=>!retired.has(key);
  const old=model.captureView('synthetic-outcome');await old.write('pending');const oldKey=[...model.local.data.keys()][0];
  model.profile=new HubProfile('other-synthetic-hub');model.profile.manager='personal';
  const current=model.captureView('synthetic-outcome');await old.write('acknowledged');
  assert.equal(await old.read(),'acknowledged');assert.equal(await current.read(),'');
  retired.add(oldKey);model.local.data.delete(oldKey);assert.equal(old.valid(),false);
  await assert.rejects(old.write('late-result'),/request_updated/);await assert.rejects(old.read(),/request_updated/);
  await current.write('current-scope');assert.equal(await current.read(),'current-scope');assert.equal(model.local.data.has(oldKey),false);
});

test('a machine-directory notification requests fresh scoped facts instead of applying foreign payload rows',async(t)=>{
  t.mock.timers.enable({apis:['setTimeout']});
  const {model}=fixture(),scopes=[];
  model.machines=[new ResourceRow('owned','Original','machine')];
  model.refresh=async()=>{scopes.push(model.scope());};
  const owner=model.scope();
  model.event(JSON.stringify({type:'machines',manager:'another-owner',machines:[{id:'foreign',name:'Foreign'}]}));
  assert.equal(model.machines[0].id,'owned');
  t.mock.timers.tick(100);await Promise.resolve();
  assert.deepEqual(scopes,[owner]);assert.equal(model.machines[0].id,'owned');
});
