import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync, existsSync } from 'node:fs';
registerHooks({
 resolve(s,c,next){
  if(s==='@kit.ArkTS')return {url:'card-integration:arkts',shortCircuit:true};
  if(c.parentURL?.endsWith('.ets')&&s.startsWith('.')){const base=new URL(s,c.parentURL);return {url:base.href+(existsSync(new URL(base.href+'.ts'))?'.ts':'.ets'),shortCircuit:true};}
  return next(s,c);
 },
 load(u,c,next){
  if(u==='card-integration:arkts')return {format:'module',shortCircuit:true,source:'export const url={URL:{parseURL:value=>new URL(value)}};export const util={TextEncoder:class{encodeInto(value){return new TextEncoder().encode(value);}},generateRandomUUID:()=>"synthetic"};'};
  if(u.endsWith('.ets'))return {format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(u),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,''))};
  return next(u,c);
 }
});
const { assistantMessageParts } = await import('../entry/src/main/ets/core/AssistantCardText.ts');
const { parseAssistantViewBlocks } = await import('../entry/src/main/ets/models/AssistantView.ets');
const { assistantCardResource } = await import('../entry/src/main/ets/models/AssistantCardLinks.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');

test('trusted body cards replace only their outer fences and retain surrounding Markdown order',()=>{
 const raw=JSON.stringify({fallback:'Readable choice',sections:[{items:[{type:'button',title:'Go',action:{id:'go',reply:'Go'}}]}]});
 const text='Before\n\n```persona-view\n'+raw+'\n```\nAfter';
 const blocks=parseAssistantViewBlocks('source#2',text),parts=assistantMessageParts(text,blocks,'Unavailable');
 assert.equal(parts.length,3);assert.equal(parts[0].text,'Before\n\n');assert.equal(parts[1].cardKey,'source#2/0');assert.equal(parts[2].text,'After');
 assert.equal(parts.map(part=>part.text).join(''),'Before\n\nAfter');
});

test('incomplete and invalid body cards retain readable fallback without enabling a native card',()=>{
 const raw=JSON.stringify({fallback:'Readable fallback',sections:[]});
 for(const text of ['Before\n```persona-view\n'+raw,'Before\n```persona-view\n{invalid}\n```\nAfter']){
  const parts=assistantMessageParts(text,parseAssistantViewBlocks('source',text),'Unavailable');assert.equal(parts.some(part=>part.cardKey),false);assert.equal(parts.some(part=>part.text==='Readable fallback'||part.text==='Unavailable'),true);
 }
});

test('card resource links retain the captured Hub and manager and never reuse foreign metadata',()=>{
 const foreign=new ResourceRow('session','Foreign title','session');foreign.group='other';foreign.manager='owner';foreign.machine='machine';foreign.session='session';
 let row=assistantCardResource('persona://session/machine/session','hub','owner',[foreign]);assert.equal(row.group,'hub');assert.equal(row.manager,'owner');assert.equal(row.title,'');assert.equal(row.session,'session');
 const known=new ResourceRow('session','Own title','session');known.group='hub';known.manager='owner';known.machine='machine';known.session='session';known.raw='{"read_only":true}';
 row=assistantCardResource('persona://session/machine/session','hub','owner',[known]);assert.equal(row.title,'Own title');assert.equal(row.raw,known.raw);
 row=assistantCardResource('persona://kb/projects/project-id','hub','owner',[]);assert.equal(row.kind,'knowledge');assert.equal(row.id,'project-id');assert.equal(row.path,'projects');
 row=assistantCardResource('persona://task/todo-id','hub','owner',[]);assert.equal(row.kind,'todo');assert.equal(row.id,'todo-id');
 row=assistantCardResource('persona://file/machine/%2Ftmp%2Fdemo.txt','hub','owner',[]);assert.equal(row.kind,'file');assert.equal(row.path,'/tmp/demo.txt');assert.equal(row.title,'demo.txt');
 for(const address of ['persona://kb/projects/../private','persona://session/machine/session?manager=another','https://example.test','persona://file/machine/../../private'])assert.equal(assistantCardResource(address,'hub','owner',[]),undefined);
});


test('assistant links share the legacy scheme boundary and reject malformed or non-absolute targets',()=>{
 const legacy=assistantCardResource('jarvis://task/todo-id','hub','owner',[]);assert.equal(legacy.kind,'todo');assert.equal(legacy.id,'todo-id');
 for(const value of ['https://example.test/%zz','https://example.test/%0Aheader','persona://file/machine/relative','persona://file/machine/relative/path']) assert.equal(assistantCardResource(value,'hub','owner',[]),undefined);
});
