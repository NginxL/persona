import test from 'node:test';
import assert from 'node:assert/strict';
import {registerHooks,stripTypeScriptTypes} from 'node:module';
import {readFileSync} from 'node:fs';
registerHooks({resolve(s,c,n){
 if(c.parentURL?.endsWith('/RemoteDocument.ets')){
  if(s==='@kit.ArkTS')return{url:'document-test:ark',shortCircuit:true};
  if(s==='./HubRepository')return{url:'document-test:repo',shortCircuit:true};
 }
 if(c.parentURL?.endsWith('.ets')&&s.startsWith('.'))return{url:new URL(s+'.ets',c.parentURL).href,shortCircuit:true};return n(s,c);
},load(u,c,n){if(u==='document-test:ark')return{format:'module',shortCircuit:true,source:"export const util={TextDecoder:{create:(encoding,options)=>({decodeToString:value=>new TextDecoder(encoding,options).decode(value)})}};"};
 if(u==='document-test:repo')return{format:'module',shortCircuit:true,source:'export const segment=encodeURIComponent;'};
 if(u.endsWith('.ets'))return{format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(u),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,''))};return n(u,c);}});
const {loadRemoteDocument}=await import('../entry/src/main/ets/models/RemoteDocument.ets');
const {ResourceRow}=await import('../entry/src/main/ets/models/Wire.ets');
function fixture(){const row=new ResourceRow('report','Report','file');row.machine='box';row.manager='owner';row.path='/private/report.pdf.md';let generation=1;const calls=[],repo={transport:{async requestBytes(path){calls.push(path);return{data:new TextEncoder().encode('# A health record').buffer,contentType:'text/markdown; charset=utf-8'};}}},model={profile:{manager:'owner'},repo:()=>repo,connectionGeneration:()=>generation,scope:()=> 'hub/owner',owns:g=>g===generation};return{row,model,repo,calls,bump:()=>generation++};}
test('report records use authenticated machine downloads and decode bounded UTF-8 content',async()=>{const {row,model,calls}=fixture(),doc=await loadRemoteDocument(model,row,()=>true);assert.equal(doc.text,'# A health record');assert.equal(doc.name,'report.pdf.md');assert.equal(calls[0],'/api/v1/machines/box/file/download?path=%2Fprivate%2Freport.pdf.md');});
test('a file response cannot cross an owner or hidden destination boundary',async()=>{for(const invalidate of ['generation','visibility']){const {row,model,repo,bump}=fixture();let visible=true;repo.transport.requestBytes=async()=>{if(invalidate==='generation')bump();else visible=false;return{data:new Uint8Array([1]).buffer,contentType:'application/pdf'};};await assert.rejects(loadRemoteDocument(model,row,()=>visible),/request_updated/);}});
test('a file from another manager or an oversized body cannot enter document preview',async()=>{const {row,model,repo,calls}=fixture();row.manager='other';await assert.rejects(loadRemoteDocument(model,row,()=>true),/request_updated/);assert.equal(calls.length,0);row.manager='owner';repo.transport.requestBytes=async()=>({data:new ArrayBuffer(32*1024*1024+1),contentType:'application/pdf'});await assert.rejects(loadRemoteDocument(model,row,()=>true),/file_too_large/);});
