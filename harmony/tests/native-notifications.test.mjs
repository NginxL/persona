import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

const state={granted:false,requests:0,published:[],wants:[],cancelled:0,wait:null,publishGate:null,cancelledIds:[],cancelError:null,queryError:null};
globalThis.__personaNotificationTest=state;
registerHooks({
 resolve(specifier,context,next){
  if(specifier.endsWith('/core/StorageOwnership'))return {url:new URL(specifier+'.ts',context.parentURL).href,shortCircuit:true};
  if(specifier==='@kit.NotificationKit')return {url:'notify-test:notifications',shortCircuit:true};
  if(specifier==='@kit.AbilityKit')return {url:'notify-test:ability',shortCircuit:true};
  if(specifier.endsWith('/design/Strings'))return {url:'notify-test:strings',shortCircuit:true};
  if(context.parentURL?.endsWith('.ets')&&specifier.startsWith('.'))return {url:new URL(specifier+'.ets',context.parentURL).href,shortCircuit:true};
  return next(specifier,context);
 },
 load(url,context,next){
  if(url==='notify-test:notifications')return {format:'module',shortCircuit:true,source:`const s=globalThis.__personaNotificationTest;export const notificationManager={isNotificationEnabled:async()=>s.granted,requestEnableNotification:async()=>{s.requests++;},cancelAll:async()=>{s.cancelled++;s.published=[];},cancel:async id=>{s.cancelledIds.push(id);if(s.cancelError)throw Object.assign(new Error('synthetic'),{code:s.cancelError});s.published=s.published.filter(item=>item.id!==id);},getActiveNotifications:async()=>{if(s.queryError)throw Error('service unavailable');return s.published;},publish:async request=>{if(s.publishGate)await s.publishGate;s.published.push(request);},SlotType:{SOCIAL_COMMUNICATION:1},ContentType:{NOTIFICATION_CONTENT_BASIC_TEXT:0}};`};
  if(url==='notify-test:ability')return {format:'module',shortCircuit:true,source:`const s=globalThis.__personaNotificationTest;export const common={};export const bundleManager={BundleFlag:{GET_BUNDLE_INFO_DEFAULT:0},getBundleInfoForSelfSync:()=>({name:'synthetic.bundle'})};export const wantAgent={OperationType:{START_ABILITY:0},WantAgentFlags:{UPDATE_PRESENT_FLAG:0},getWantAgent:async info=>{s.wants.push(info);if(s.wait)await s.wait;return info;}};`};
  if(url==='notify-test:strings')return {format:'module',shortCircuit:true,source:'export const tr=key=>key;'};
  if(url.endsWith('.ets'))return {format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,''))};
  return next(url,context);
 }
});
const {NativeNotifications}=await import('../entry/src/main/ets/platform/NativeNotifications.ets');
const {ResourceRow}=await import('../entry/src/main/ets/models/Wire.ets');
function setup(){
 Object.assign(state,{granted:true,requests:0,published:[],wants:[],cancelled:0,wait:null,publishGate:null,cancelledIds:[],cancelError:null,queryError:null});
 const row=new ResourceRow('session','Agent','session');row.machine='machine';row.session='session';
 const client={alerts:true,demo:false,connected:true,profile:{id:'hub',manager:'personal'},sessions:[row],conversations:[],appearance:0,style:'glass',scope:()=>client.owner,owner:'hub/personal',connectionGeneration:()=>1,owns:(generation,scope)=>generation===1&&scope===client.owner,saved:[],setAlerts:async(enabled)=>{client.saved.push(enabled);client.alerts=enabled;},repo:()=>({transport:{hello:features=>client.features=features}})};
 return {client,notifications:new NativeNotifications()};
}
const frame={type:'notify',kind:'interaction',interaction_id:'synthetic-request',machine_id:'machine',session_id:'session',title:'Synthetic approval',body:'Needs a decision'};
test('a successful permission request cannot turn a denied system grant into an enabled preference',async()=>{
 const {client,notifications}=setup();state.granted=false;await notifications.initialize({});
 await notifications.setEnabled(client,true);
 assert.equal(state.requests,1);assert.deepEqual(client.saved,[]);assert.equal(notifications.granted,false);assert.equal(notifications.error,'notifications_denied');assert.ok(!notifications.features(true).includes('ws_notify'));
});
test('native quiet notifications reduce all reminders and open only the catalog-owned destination',async()=>{
 const {client,notifications}=setup();await notifications.initialize({});await notifications.receive(client,{...frame,quiet:true});
 assert.equal(state.published.length,1);assert.deepEqual(state.published[0].notificationFlags,{soundEnabled:2,vibrationEnabled:2,bannerEnabled:2});
 assert.deepEqual(state.wants[0].wants[0].parameters,{profile:'hub',manager:'personal',machine:'machine',session:'session'});
});
test('simultaneous copies of a notification publish once and scope changes cancel pending native work',async()=>{
 const {client,notifications}=setup();await notifications.initialize({});let release;state.wait=new Promise(resolve=>release=resolve);
 const first=notifications.receive(client,frame),duplicate=notifications.receive(client,frame);await duplicate;assert.equal(state.wants.length,1);
 client.owner='hub/work';release();await first;assert.deepEqual(state.published,[]);
});
test('notification frames cannot invent a manager owner or navigate an uncatalogued session',async()=>{
 const {client,notifications}=setup();await notifications.initialize({});
 await notifications.receive(client,{...frame,manager:'another-manager'});assert.equal(state.wants.length,0);
 await notifications.receive(client,{...frame,session_id:'unverified-session'});assert.equal(state.wants.length,1);assert.equal(state.wants[0].wants[0].parameters.session,'');
});
test('disabling notifications withdraws opt-in and cancels native cards',async()=>{
 const {client,notifications}=setup();await notifications.initialize({});await notifications.setEnabled(client,false);
 assert.deepEqual(client.saved,[false]);assert.ok(!client.features.includes('ws_notify'));assert.equal(state.cancelled,1);
});
test('forgetting one Hub preserves another Hub notification and dedup state',async()=>{
 const {client,notifications}=setup();await notifications.initialize({});await notifications.receive(client,frame);const firstId=state.published[0].id;
 client.profile.id='other-hub';client.owner='other-hub/personal';await notifications.receive(client,frame);const otherId=state.published[1].id;
 await notifications.clear('hub');assert.deepEqual(state.published.map(card=>card.id),[otherId]);assert.ok(state.cancelledIds.includes(firstId));assert.equal(state.cancelled,0);
 await notifications.receive(client,frame);assert.equal(state.published.length,1);
});
test('a publication finishing after its Hub is forgotten is cancelled without removing other Hub cards',async()=>{
 const {client,notifications}=setup();await notifications.initialize({});let release;state.publishGate=new Promise(resolve=>release=resolve);
 const posting=notifications.receive(client,frame);for(let i=0;i<20&&!state.wants.length;i++)await new Promise(resolve=>setTimeout(resolve,0));await new Promise(resolve=>setTimeout(resolve,0));
 await notifications.clear('hub');release();await posting;assert.deepEqual(state.published,[]);assert.equal(state.cancelled,0);assert.ok(state.cancelledIds.length>=1);
});
test('forgetting a Hub tolerates cards that the user has already dismissed',async()=>{
 const {client,notifications}=setup();await notifications.initialize({});await notifications.receive(client,frame);state.published=[];state.cancelError=1600007;
 await notifications.clear('hub');assert.equal(state.cancelledIds.length,1);assert.equal(state.cancelled,0);
});
test('a notification service failure still attempts remembered cancellations and reports incomplete cleanup',async()=>{
 const {client,notifications}=setup();await notifications.initialize({});await notifications.receive(client,frame);state.queryError=true;state.cancelError=1600001;
 await assert.rejects(notifications.clear('hub'),/notifications_unavailable/);assert.equal(state.cancelledIds.length,1);assert.equal(state.cancelled,0);
});


test('stream UI capability stays advertised when notifications are disabled or denied',()=>{
 const notifications = new NativeNotifications();
 for(const enabled of [false,true])for(const granted of [false,true]){notifications.granted=granted;const features=notifications.features(enabled);assert.ok(features.includes('extension_ui_stream_v1'));assert.equal(features.includes('ws_notify'),enabled&&granted);}
});
