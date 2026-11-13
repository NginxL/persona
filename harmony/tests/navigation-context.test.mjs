import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {registerHooks,stripTypeScriptTypes} from 'node:module';
registerHooks({resolve(s,c,n){if(c.parentURL?.endsWith('NavigationContext.ets')&&s==='./Wire')return {url:new URL('./Wire.ets',c.parentURL).href,shortCircuit:true};return n(s,c);},load(u,c,n){if(u.endsWith('.ets'))return{format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(u),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,''))};return n(u,c);}});
const {NavigationContext}=await import('../entry/src/main/ets/models/NavigationContext.ets');
const {ResourceRow}=await import('../entry/src/main/ets/models/Wire.ets');
// Load the real shell's navigation methods, excluding only ArkUI builders and platform lifecycle surfaces.
const source=readFileSync(new URL('../entry/src/main/ets/pages/Index.ets',import.meta.url),'utf8');
function method(name){
 const start=source.indexOf('  private ',source.indexOf(`  private ${name}(`)>=0?source.indexOf(`  private ${name}(`):source.indexOf(`  private async ${name}(`));
 assert.ok(start>=0,name);const body=source.indexOf('{',start);let depth=1,quote='',escape=false,i=body+1;
 for(;i<source.length&&depth;i++){const c=source[i];if(quote){if(escape)escape=false;else if(c==='\\')escape=true;else if(c===quote)quote='';}else if(c==='"'||c==="'"||c==='`')quote=c;else if(c==='{')depth++;else if(c==='}')depth--;}
 assert.equal(depth,0,name);return source.slice(start,i);
}
const names=['redeem','childKey','routeRow','openSettings','settingsKind','settingsModel','targetReady','openLinkedResource','openFrom','openComputer','handoffResource','restoreTarget','routeShown'];
const NativeNavigation=new Function('NavigationContext','return '+stripTypeScriptTypes('class NativeNavigation {'+names.map(method).join('\n')+'}'))(NavigationContext);
function row(id='source',manager='owner-b',kind='conversation'){const r=new ResourceRow(id,id,kind);r.manager=manager;r.machine='machine';r.session=id;return r;}
function deferred(){let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};}
function fixture(){const shell=new NativeNavigation(),pushed=[];let gen=1;
 const primary={profile:{id:'hub'},selected:row('primary','owner-a'),connected:true,error:'',connectionGeneration:()=>gen,scope:()=> 'hub/owner-a',owns:(g,s)=>g===gen&&s==='hub/owner-a',reportError(e){this.error=e.message;},hideConversation(){},async listResources(){}};
 const detail={selected:row('previous'),connected:true,async select(r){this.selected=r;},hideConversation(){},showConversation(){},watchDecisions(){}};
 primary.viewFor=async()=>detail;shell.model=primary;shell.detailModel=primary;shell.activeChild='';shell.routeRevision=0;shell.chatReady=false;shell.detailRoute='';shell.stack={pushPath:r=>pushed.push(r),getAllPathName:()=>pushed.map(r=>r.name),getParamByIndex:i=>pushed[i]?.param};shell.savePath=()=>{};
 return {shell,primary,detail,pushed,bump:()=>gen++};
}
test('global settings remain workspace-owned and chat settings retain the captured target',()=>{
 const {shell,detail,primary,pushed}=fixture();shell.detailModel=detail;shell.openSettings('models');shell.openSettings('models',true);
 assert.equal(pushed[0].param,'models');assert.equal(shell.settingsModel(pushed[0].param),primary);assert.equal(shell.settingsModel(pushed[1].param),detail);
 const route=NavigationContext.from('settings',pushed[1].param);assert.equal(route.page,'models');assert.equal(route.row.key(),detail.selected.key());
});
test('tools and history restore their serialized owner and session before becoming interactive',async()=>{
 for(const name of ['tools','history','resource','inbox']){const {shell,detail}=fixture(),target=row(name,'owner-b',name==='resource'?'todo':'conversation'),param=JSON.stringify(target);shell.activeChild=shell.childKey(name,param);await shell.restoreTarget(name,param);assert.equal(detail.selected.key(),target.key());assert.equal(shell.detailModel,detail);assert.equal(shell.targetReady(name,param),true);}
});
test('linked inbox and todo cards retain dedicated destination kinds and owner data',async()=>{
 for(const kind of ['inbox','todo','knowledge']){const {shell,pushed,primary}=fixture(),target=row(kind,'owner-b',kind);primary.viewFor=async()=>{throw new Error('must not select a chat for resource navigation');};await shell.openLinkedResource(target);assert.equal(pushed[0].name,kind);assert.equal(NavigationContext.from(kind,pushed[0].param).row.manager,'owner-b');assert.equal(primary.error,'');}
});
test('a delayed owner reconnect is rebound after the primary connection generation changes',async()=>{
 const {shell,primary,detail,bump}=fixture(),target=row(),param=JSON.stringify(target),wait=deferred();shell.activeChild=shell.childKey('conversation',param);primary.viewFor=()=>wait.promise;
 const old=shell.restoreTarget('conversation',param);bump();wait.resolve(detail);await old;assert.equal(shell.chatReady,false);
 await shell.restoreTarget('conversation',param);assert.equal(shell.targetReady('conversation',param),true);assert.equal(detail.selected.id,target.id);
});
test('late route restoration and handoff cannot navigate or expose a replaced tab',async()=>{
 const {shell,primary,detail,pushed}=fixture(),target=row(),param=JSON.stringify(target),wait=deferred();shell.activeChild=shell.childKey('tools',param);primary.viewFor=()=>wait.promise;
 const restoring=shell.restoreTarget('tools',param);shell.activeChild='different-tab';wait.resolve(detail);await restoring;assert.equal(shell.chatReady,false);assert.notEqual(detail.selected.id,target.id);
 const handoff=deferred();primary.handoff=()=>handoff.promise;const sending=shell.handoffResource(row('todo','owner-b','todo'));shell.activeChild='new-route';handoff.resolve(target);await sending;assert.equal(pushed.length,0);
});
test('returning from a native picker to the same resolved destination preserves its mounted content',async()=>{
 const {shell,primary,detail}=fixture(),target=detail.selected,param=JSON.stringify(target),gate=deferred();shell.activeChild=shell.childKey('conversation',param);shell.detailRoute=shell.activeChild;shell.chatReady=true;shell.detailModel=detail;primary.viewFor=()=>gate.promise;
 const returning=shell.restoreTarget('conversation',param);assert.equal(shell.chatReady,true);assert.equal(shell.detailRoute,shell.activeChild);gate.resolve(detail);await returning;assert.equal(shell.targetReady('conversation',param),true);
});

test('callbacks from a hidden workspace cannot open a destination in the new route', async()=>{
 const {shell,pushed}=fixture(),param='todo',target=row('linked','owner-b','todo');
 shell.activeChild=shell.childKey('resources',param);shell.openFrom('resources',param,target);
 await new Promise(resolve=>setImmediate(resolve));assert.equal(pushed.length,1);
 shell.activeChild='another-tab';shell.openFrom('resources',param,target);
 await new Promise(resolve=>setImmediate(resolve));assert.equal(pushed.length,1);
});
test('computer routes capture the machine owner and restore independently of chat selection',async()=>{
 const {shell,detail,pushed}=fixture(),machine=row('mac','owner-b','machine');
 shell.openComputer(machine);shell.openComputer(machine);assert.equal(pushed.length,1);assert.equal(pushed[0].name,'computer');
 assert.equal(NavigationContext.from('computer',pushed[0].param).row.manager,'owner-b');
 shell.activeChild=shell.childKey('computer',pushed[0].param);
 await shell.restoreTarget('computer',pushed[0].param);
 assert.equal(detail.selected.kind,'machine');assert.equal(shell.targetReady('computer',pushed[0].param),true);
});

test('a late pairing UI completion does not clear a newly opened connection form or its navigation',async()=>{const {shell,primary}=fixture(),gate=deferred();shell.pairingRevision=1;shell.address='first-link';shell.pairCode='first-code';shell.showSheet=true;primary.pair=()=>gate.promise;const completing=shell.redeem({}, {hubId:'paired-a'},()=>false);shell.pairingRevision=2;shell.address='new-link';shell.pairCode='new-code';primary.profile.id='chosen-c';gate.resolve();await completing;assert.equal(shell.address,'new-link');assert.equal(shell.pairCode,'new-code');assert.equal(shell.showSheet,true);assert.equal(primary.profile.id,'chosen-c');});

test('pairing completion cannot dismiss a later non-connect sheet in the same selected Hub',async()=>{const {shell,primary}=fixture(),gate=deferred();shell.pairingRevision=1;shell.sheetKind='connect';shell.address='confirmed-link';shell.pairCode='confirmed-code';shell.showSheet=false;primary.profile.id='paired-a';primary.pair=()=>gate.promise;const old=shell.redeem({}, {hubId:'paired-a'},()=>true);shell.sheetKind='hub';shell.showSheet=true;gate.resolve();await old;assert.equal(shell.sheetKind,'hub');assert.equal(shell.showSheet,true);assert.equal(shell.address,'confirmed-link');});


test('native extension pushes have distinct route identities and an eight-page limit',()=>{
 const NativeExtensions = new Function('ResourceRow','Wire','util','tr','return '+stripTypeScriptTypes('class NativeExtensions {'+method('openExtension')+'}'))(ResourceRow,{payload:(keys,values)=>Object.fromEntries(keys.map((key,i)=>[key,values[i]]))},{generateRandomUUID:(()=>{let id=0;return()=>String(++id);})()},value=>value);
 const shell=new NativeExtensions(),pushed=[],owner={profile:{id:'hub',manager:'owner'}};let warnings=0;
 shell.stack={getAllPathName:()=>pushed.map(row=>row.name),pushPath:row=>pushed.push(row)};shell.savePath=()=>{};shell.getUIContext=()=>({getPromptAction:()=>({showToast:()=>warnings++})});
 for(let i=0;i<10;i++)shell.openExtension(owner,'extension','same-page','Title');
 assert.equal(pushed.length,8);assert.equal(warnings,2);assert.equal(new Set(pushed.map(row=>row.param)).size,8);
 for(const route of pushed){const row=NavigationContext.rowFrom(route.param);assert.equal(row.group,'hub');assert.equal(row.manager,'owner');assert.equal(row.data().ui,'same-page');}
});
