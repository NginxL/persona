import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';
const state={ready:true,foregrounded:0,background:0,activated:0,recordingStops:0,lookUnbinds:0,localeAnnouncements:0,language:'default',notifications:[],query:Promise.resolve(),init:Promise.resolve()};
globalThis.__personaAbilityTest=state;
registerHooks({
 resolve(specifier,context,next){
  if(context.parentURL?.endsWith('/EntryAbility.ets')){
   if(specifier==='../core/WireCompatibility')return {url:new URL(specifier+'.ts',context.parentURL).href,shortCircuit:true};
   if(specifier==='../models/Wire')return {url:new URL(specifier+'.ets',context.parentURL).href,shortCircuit:true};
   return {url:'ability-test:'+specifier,shortCircuit:true};
  }
  return next(specifier,context);
 },
 load(url,context,next){
  if(url.startsWith('ability-test:'))return {format:'module',shortCircuit:true,source:`const s=globalThis.__personaAbilityTest;
   export const AbilityConstant={},ConfigurationConstant={ColorMode:{COLOR_MODE_LIGHT:1,COLOR_MODE_DARK:2,COLOR_MODE_NOT_SET:0}},Want={};
   export class UIAbility {context={resourceManager:{},getApplicationContext:()=>({setColorMode(){}})}}
   export const window={};export const Strings={bind(){},refresh(){}};
   export const NOTIFICATION_ACTION='persona.notification.OPEN';export const nativeNotifications={refresh:()=>s.query};
   export const deviceFeatures={async background(){},async foreground(){}};
   export const KnowledgeRecording={async backgroundAll(){s.recordingStops++;}};
   export const deviceLook={async initialize(){},async bind(owner){if(owner===undefined)s.lookUnbinds++;}};
   export const nativeEnvironment={update(){}};
   export const client={get ready(){return s.ready;},set ready(value){s.ready=value;},appearance:0,get language(){return s.language;},announceLanguage(){s.localeAnnouncements++;},error:'',initialize:()=>s.init,activateForeground(){s.activated++;},async foregrounded(){s.foregrounded++;},async background(){s.background++;},async shutdown(){s.background++;},async openNotification(target){s.notifications.push(target);}};
  `};
  if(url.endsWith('.ets')||url.endsWith('.ts'))return {format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,''))};
  return next(url,context);
 }
});
const {default:EntryAbility}=await import('../entry/src/main/ets/entryability/EntryAbility.ets');
const turn=()=>new Promise(resolve=>setTimeout(resolve,0));
function reset(){Object.assign(state,{ready:true,foregrounded:0,background:0,activated:0,recordingStops:0,lookUnbinds:0,localeAnnouncements:0,language:'default',notifications:[],query:Promise.resolve(),init:Promise.resolve()});}
test('a foreground permission query cannot reopen a connection after background or destroy',async()=>{
 for(const stop of ['onBackground','onDestroy']){
  reset();let release;state.query=new Promise(resolve=>release=resolve);const ability=new EntryAbility();ability.onForeground();ability[stop]();release();await turn();
  assert.equal(state.foregrounded,0);assert.equal(state.background,1);
  assert.equal(state.recordingStops,1);assert.equal(state.lookUnbinds,1);
 }
});
test('a notification arriving during cold initialization is retained and dispatched once ready',async()=>{
 reset();state.ready=false;let release;state.init=new Promise(resolve=>release=resolve);const ability=new EntryAbility();ability.onCreate({},{});
 const target={profile:'hub',manager:'personal',machine:'machine',session:'session'};ability.onNewWant({action:'persona.notification.OPEN',parameters:target},{});assert.equal(state.notifications.length,0);
 state.ready=true;release();await turn();assert.deepEqual(state.notifications,[target]);
});

test('matching system language reannounces live Hub locale after a configuration change',()=>{reset();const ability=new EntryAbility();ability.onConfigurationUpdate({});assert.equal(state.localeAnnouncements,1);state.language='ja-JP';ability.onConfigurationUpdate({});assert.equal(state.localeAnnouncements,1);});
