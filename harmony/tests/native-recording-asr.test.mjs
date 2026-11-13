import test from 'node:test';
import assert from 'node:assert/strict';
import {registerHooks,stripTypeScriptTypes} from 'node:module';
import {readFileSync} from 'node:fs';
let sdk;
globalThis.__nativeAsr=()=>sdk;
registerHooks({
 resolve(specifier,context,next){if(specifier.startsWith('@kit.'))return{url:'native-asr:'+specifier,shortCircuit:true};return next(specifier,context);},
 load(url,context,next){if(url.startsWith('native-asr:')){
  const source={
   '@kit.AbilityKit':'export const abilityAccessCtrl={};export const common={};export const Permissions={};',
   '@kit.AudioKit':'export const audio={};',
   '@kit.CoreSpeechKit':'export const speechRecognizer={createEngine: (...args)=>globalThis.__nativeAsr().createEngine(...args)};',
   '@kit.CoreFileKit':'export const fileIo={OpenMode:{READ_ONLY:0},statSync:(...args)=>globalThis.__nativeAsr().stat(...args),mkdirSync(){},openSync:()=>({fd:1}),readSync:(_fd,_bytes,options)=>options.length,closeSync:()=>globalThis.__nativeAsr().closed++};',
   '@kit.ArkTS':'export const util={generateRandomUUID:()=>"00000000-0000-4000-8000-000000000001"};'
  }[url.slice('native-asr:'.length)];return{format:'module',source,shortCircuit:true};}
  if(url.endsWith('NativeRecording.ets'))return{format:'module',source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8')),shortCircuit:true};return next(url,context);
 }
});
const {NativeRecording}=await import('../entry/src/main/ets/platform/NativeRecording.ets');
const id='00000000-0000-4000-8000-000000000001';
async function fixture(mode,size=19200){
 const state={writes:0,closed:0,started:0,cancelled:0,shutdown:0,session:'',listener:undefined,
  stat(path){return{size:path.endsWith('.pcm')?size:0};},
  async createEngine(){if(this.creation)await this.creation;return{
   setListener(listener){state.listener=listener;},startListening(options){state.started++;state.session=options.sessionId;state.options=options;if(mode!=='wait_start')queueMicrotask(()=>state.listener.onStart(state.session,''));},
   writeAudio(_session,audio){state.writes++;state.fedMs=(state.fedMs||0)+audio.byteLength/32;if(mode==='limit'&&state.fedMs>=state.options.extraParams.maxAudioDuration)state.listener.onError(state.session,1,'audio duration limit reached');if(mode==='error'&&state.writes===1)state.listener.onError(state.session,1,'sdk detail');if(mode==='early_complete'&&state.writes===1)state.listener.onComplete(state.session,'');},
   finish(){state.listener.onResult('different-session',{isFinal:true,result:'foreign'});state.listener.onError('different-session',1,'');state.listener.onResult(state.session,{isFinal:true,result:'你好。'});state.listener.onResult(state.session,{isFinal:true,result:'你好。'});state.listener.onComplete(state.session,'');},
   cancel(){state.cancelled++;state.listener.onError(state.session,1,'cancel');},shutdown(){state.shutdown++;}
  };}};
 sdk=state;const platform=new NativeRecording();await platform.initialize({filesDir:'/private'});return{platform,state};
}
async function waitFor(predicate){for(let i=0;i<100;i++){if(predicate())return;await new Promise(r=>setTimeout(r,1));}assert.fail('SDK boundary not reached');}

test('cancel during the paced native feed settles without rejection leaks or extra writes',async()=>{
 const {platform,state}=await fixture('cancel',115200000),pending=platform.transcribe(id,'zh-CN');await waitFor(()=>state.writes===1);
 const rejected=assert.rejects(pending,/transcription_cancelled/);await platform.cancelTranscription();await rejected;assert.equal(state.writes,1);assert.equal(state.closed,1);assert.equal(state.shutdown,1);
});
test('an early SDK terminal error stops a long file before a second write',async()=>{
 const {platform,state}=await fixture('error',115200000);await assert.rejects(platform.transcribe(id,'zh-CN'),/knowledge_recording_transcribe_failed/);assert.equal(state.writes,1);assert.equal(state.shutdown,1);assert.equal(state.closed,1);assert.deepEqual(state.options.extraParams,{recognitionMode:1,maxAudioDuration:3600040});
});
test('completion before all PCM frames cannot publish a truncated recording',async()=>{
 const {platform,state}=await fixture('early_complete',115200000);await assert.rejects(platform.transcribe(id,'zh-CN'),/knowledge_recording_transcribe_failed/);assert.equal(state.writes,1);assert.equal(state.shutdown,1);
});
test('two identical final clauses remain in order and foreign session callbacks are ignored',async()=>{
 const {platform,state}=await fixture('repeat');assert.equal(await platform.transcribe(id,'zh-CN'),'你好。你好。');assert.equal(state.writes,15);assert.equal(state.closed,1);assert.equal(state.shutdown,1);
});
test('cancellation while the native engine is being created cannot start listening later',async()=>{
 const {platform,state}=await fixture('cancel');let release;state.creation=new Promise(r=>{release=r;});const pending=platform.transcribe(id,'zh-CN');const rejected=assert.rejects(pending,/transcription_cancelled/);await platform.cancelTranscription();assert.equal(state.started,0);release();await rejected;assert.equal(state.started,0);assert.equal(state.writes,0);assert.equal(state.shutdown,1);
});

test('PCM waits for the matching native onStart callback',async()=>{
 const {platform,state}=await fixture('wait_start'),pending=platform.transcribe(id,'zh-CN');await waitFor(()=>state.started===1);
 assert.equal(state.writes,0);state.listener.onStart('foreign-session','');await new Promise(r=>setTimeout(r,5));assert.equal(state.writes,0);
 state.listener.onStart(state.session,'');assert.equal(await pending,'你好。你好。');assert.equal(state.writes,15);
});
test('cancel before native onStart settles immediately without opening or feeding PCM',async()=>{
 const {platform,state}=await fixture('wait_start'),pending=platform.transcribe(id,'zh-CN');await waitFor(()=>state.started===1);
 const rejected=assert.rejects(pending,/transcription_cancelled/);await platform.cancelTranscription();await rejected;state.listener.onStart(state.session,'late');assert.equal(state.writes,0);assert.equal(state.closed,0);assert.equal(state.shutdown,1);
});

test('a long file and its padded last frame finish before the native audio limit',async()=>{
 const timer=globalThis.setTimeout;globalThis.setTimeout=(callback,delay,...args)=>timer(callback,delay===20||delay===40?0:delay,...args);
 try{for(const size of [2240000,2240320]){const {platform,state}=await fixture('limit',size);assert.equal(await platform.transcribe(id,'zh-CN'),'你好。你好。');assert.ok(state.fedMs<state.options.extraParams.maxAudioDuration);assert.equal(state.writes,Math.ceil(size/1280));assert.equal(state.shutdown,1);}}
 finally{globalThis.setTimeout=timer;}
});

test('retained listeners from a retired engine cannot settle or fail the next transcription',async()=>{
 const {platform,state}=await fixture('error');await assert.rejects(platform.transcribe(id,'zh-CN'),/knowledge_recording_transcribe_failed/);
 const old=state.listener,oldSession=state.session,pending=platform.transcribe(id,'zh-CN');await waitFor(()=>state.writes===2);
 old.onComplete(oldSession,'late completion');old.onError(oldSession,1,'late error');old.onResult(oldSession,{isFinal:true,result:'late text'});
 assert.equal(await pending,'你好。你好。');assert.equal(state.writes,16);assert.equal(state.shutdown,2);
});
