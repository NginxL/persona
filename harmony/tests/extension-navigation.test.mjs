import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/ExtensionNavigation.ets') && specifier === './ClientModel') return {url:'mock:client',shortCircuit:true};
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      const base = new URL(specifier, context.parentURL);
      return {url:base.href+(existsSync(new URL(base.href+'.ts'))?'.ts':'.ets'),shortCircuit:true};
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url==='mock:client') return {format:'module',source:'export class ClientModel {}',shortCircuit:true};
    if (url.endsWith('.ets')) return {format:'module',source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,'')),shortCircuit:true};
    return next(url,context);
  }
});
const { ExtensionNavigation } = await import('../entry/src/main/ets/models/ExtensionNavigation.ets');
const entry = (id='page') => ({extension:'example',id,title:id,placement:'nav_page',icon:'chart'});
function fixture() {
  let generation=1, manager='assistant'; const handlers=new Set(), calls=[];
  const f={result:{ui:[entry()]},error:false,reader:undefined};
  f.client={profile:{id:'hub',manager},connected:true,features:['extensions','extension_ui_slots'],scope:()=>`hub:${manager}`,
    connectionGeneration:()=>generation,owns:(g,s)=>g===generation&&s===`hub:${manager}`,
    repo:()=>({call:async path=>{calls.push(path);if(f.reader)return f.reader();if(f.error)throw Error('offline');return f.result;}}),
    observeEvents:handler=>{handlers.add(handler);return()=>handlers.delete(handler);}};
  f.model=new ExtensionNavigation();f.calls=calls;
  f.switch=()=>{manager='other';generation++;f.client.profile={id:'hub',manager};};
  f.reconnect=()=>{generation++;f.client.features=[];f.client.connected=false;};
  f.event=data=>{for(const handler of handlers)handler(JSON.stringify(data));};
  return f;
}
test('main navigation lists every valid contribution without a small page cap',async()=>{
  const f=fixture();f.result={ui:[...Array.from({length:24},(_,i)=>entry(`p${i}`)),entry('p1'),{...entry('wrong'),placement:'widget'}]};
  f.model.bind(f.client);await f.model.refresh();
  assert.equal(f.model.entries.length,24);assert.equal(f.model.visible,true);assert.equal(f.model.ready,true);
  assert.ok(f.model.accepts(f.model.entries[23]));assert.ok(f.calls.every(path=>path==='/api/v1/extensions/-/ui?placement=nav_page'));
  f.model.dispose();
});
test('an old Hub is not queried and legacy slot support uses its compatible path',async()=>{
  const f=fixture();f.client.features=[];f.model.bind(f.client);await f.model.refresh();assert.equal(f.calls.length,0);assert.equal(f.model.visible,false);
  f.client.features=['plugin_ui_slots'];f.result={ui:[{plugin:'example',id:'old',title:'Old',placement:'nav_page'}]};await f.model.refresh();
  assert.equal(f.calls.at(-1),'/api/v1/plugins/-/ui?placement=nav_page');assert.equal(f.model.entries[0].extensionId,'example');f.model.dispose();
});
test('failed discovery offers retry while a successful empty directory removes the entry',async()=>{
  const f=fixture();f.error=true;f.model.bind(f.client);await f.model.refresh();assert.equal(f.model.visible,true);assert.equal(f.model.ready,false);assert.ok(f.model.error);
  f.error=false;f.result={ui:[]};await f.model.refresh();assert.equal(f.model.visible,false);assert.equal(f.model.error,'');f.model.dispose();
});
test('late directory reads and retained rows cannot authorize another manager',async()=>{
  const f=fixture();f.model.bind(f.client);await f.model.refresh();const stale=f.model.entries[0];
  let resolve;f.reader=()=>new Promise(r=>resolve=r);const pending=f.model.refresh();assert.equal(f.model.accepts(stale),false);
  f.switch();f.reader=undefined;f.result={ui:[entry('other')]};f.model.bind(f.client);await f.model.refresh();resolve({ui:[entry('old')]});await pending;
  assert.equal(f.model.entries[0].uiId,'other');assert.equal(f.model.accepts(stale),false);f.model.dispose();
});
test('extension invalidation immediately revokes old navigation until a fresh directory succeeds',async()=>{
  const f=fixture();f.model.bind(f.client);await f.model.refresh();const stale=f.model.entries[0];
  f.event({type:'extension_operation',operation:{state:'running'}});assert.equal(f.model.accepts(stale),false);
  f.result={ui:[]};await f.model.refresh();assert.equal(f.model.accepts(stale),false);
  f.client.connected=false;f.model.connectionChanged();assert.equal(f.model.ready,false);f.model.dispose();
});
test('stream frames and badge updates retain the open page while a lifecycle change revokes it',async()=>{
  const f=fixture();f.model.bind(f.client);await f.model.refresh();const row=f.model.entries[0],calls=f.calls.length;
  f.event({type:'extension_ui_stream',kind:'delta',extension:'example'});
  f.event({type:'extension_ui',extension:'example',id:'page',badge:{text:'3'}});
  assert.equal(f.model.accepts(row),true);assert.equal(row.badge,'3');assert.equal(f.calls.length,calls);
  f.event({type:'extension_operation',operation:{state:'done'}});assert.equal(f.model.accepts(row),false);f.model.dispose();
});
test('an omitted event manager never updates another named manager directory',async()=>{
  const f=fixture();f.switch();f.model.bind(f.client);await f.model.refresh();const row=f.model.entries[0];
  f.event({type:'extension_ui',extension:'example',id:'page',badge:{text:'wrong'}});
  f.event({type:'extension_operation',operation:{state:'done'}});
  assert.equal(row.badge,'');assert.equal(f.model.accepts(row),true);
  f.event({type:'extension_ui',manager:'other',extension:'example',id:'page',badge:{text:'2'}});assert.equal(row.badge,'2');f.model.dispose();
});
test('server-initiated closure immediately revokes a no-auth application and reloads its directory',async()=>{
  const f=fixture();f.model.bind(f.client);await f.model.refresh();const row=f.model.entries[0];
  f.event({type:'extension_ui_stream',kind:'closed',session:'page-session',request_id:''});
  assert.equal(f.model.accepts(row),false);f.result={ui:[]};await f.model.refresh();assert.equal(f.model.visible,false);f.model.dispose();
});
test('capability negotiation keeps cached rows disabled until a confirmed supported or unsupported hello',async()=>{
  const f=fixture();f.model.bind(f.client);await f.model.refresh();const row=f.model.entries[0];
  f.reconnect();f.model.connectionChanged();assert.equal(f.model.visible,true);assert.equal(f.model.entries[0],row);assert.equal(f.model.accepts(row),false);
  f.client.connected=true;await f.model.refresh();assert.equal(f.model.visible,true);assert.equal(f.model.entries[0],row);assert.equal(f.model.ready,false);
  f.event({type:'hello',features:[]});await f.model.refresh();assert.equal(f.model.visible,false);assert.equal(f.model.entries.length,0);f.model.dispose();
});
test('a newly installed application is discovered without a lifecycle event',async(t)=>{
  t.mock.timers.enable({apis:['setTimeout']});
  const f=fixture();f.result={ui:[]};f.model.bind(f.client);await f.model.refresh();assert.equal(f.model.visible,false);
  f.result={ui:[entry('new')]};t.mock.timers.tick(5000);await new Promise(resolve=>setImmediate(resolve));
  assert.equal(f.model.entries[0]?.uiId,'new');assert.equal(f.model.visible,true);f.model.dispose();
});
