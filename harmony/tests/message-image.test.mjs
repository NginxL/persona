import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {registerHooks,stripTypeScriptTypes} from 'node:module';
const files=new Map(),dirs=new Set();let counter=0,canPreview=async()=>true;const opened=[];
globalThis.canIUse=()=>true;
registerHooks({resolve(s,c,n){if(c.parentURL?.endsWith('/MessageImage.ets')&&s==='./HubRepository')return{url:'image-boundary:repo',shortCircuit:true};if(c.parentURL?.endsWith('/NativePreview.ets')){if(s==='../core/AttachmentTypes'||s==='../core/MessageImageGallery')return{url:new URL(s+'.ts',c.parentURL).href,shortCircuit:true};return{url:'image-boundary:'+s,shortCircuit:true};}return n(s,c);},load(u,c,n){if(u.startsWith('image-boundary:'))return{format:'module',shortCircuit:true,source:`export const segment=encodeURIComponent;export const tr=k=>k;export const common={};export class BusinessError extends Error{};export const util={generateRandomUUID:()=>globalThis.imageBoundary.uuid()};export const fileIo=globalThis.imageBoundary.io;export const fileUri={getUriFromPath:p=>'file://'+p};export const filePreview=globalThis.imageBoundary.preview;`};if(u.endsWith('.ets')||u.endsWith('.ts'))return{format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(u),'utf8'))};return n(u,c);}});
globalThis.imageBoundary={uuid:()=>`11111111-1111-4111-8111-${String(++counter).padStart(12,'0')}`,io:{OpenMode:{CREATE:1,WRITE_ONLY:2,TRUNC:4},statSync(p){if(!dirs.has(p))throw new Error('missing');return{};},mkdirSync:p=>dirs.add(p),listFileSync:p=>{if(!dirs.has(p))throw Object.assign(new Error('missing'),{code:13900002});return [...files.keys()].filter(k=>k.startsWith(p+'/')).map(k=>k.slice(p.length+1));},openSync:p=>({fd:p}),writeSync:(p,data)=>{files.set(p,data);return data.byteLength;},closeSync(){},unlinkSync:p=>files.delete(p),rmdirSync:p=>dirs.delete(p)},preview:{canPreview:()=>canPreview(),openPreview:async(c,i)=>opened.push(i),closePreview:async()=>opened.push('closed')}};
const {loadMessageImage}=await import('../entry/src/main/ets/models/MessageImage.ets');
const {previewMessageImage,clearImagePreviews}=await import('../entry/src/main/ets/platform/NativePreview.ets');
function fixture(){const paths=[],response={data:new Uint8Array([1,2,3]).buffer,contentType:'image/jpeg; charset=binary'},repository={transport:{async requestBytes(p){paths.push(p);return response;}}};let current=repository,gen=1;const model={selected:{machine:'computer/1',session:'chat/2',id:'chat/2',path:'/work/project'},repo:()=>current,connectionGeneration:()=>gen,scope:()=> 'hub/owner',target:()=> 'target',owns:g=>g===gen};return{model,paths,response,replace:()=>current={},bump:()=>gen++};}
test('image notes use authenticated machine download while lazy images keep their session item route',async()=>{
 const {model,paths}=fixture();await loadMessageImage(model,'','/tmp/photo image.jpg');await loadMessageImage(model,'item#1','');
 assert.equal(paths[0],'/api/v1/machines/computer%2F1/file/download?path=%2Ftmp%2Fphoto%20image.jpg&cwd=%2Fwork%2Fproject');assert.equal(paths[1],'/api/v1/sessions/computer%2F1/chat%2F2/items/item%231/image');
});
test('image bytes from a replaced owner transport are never published and active document formats are refused',async()=>{
 const f=fixture();f.model.repo().transport.requestBytes=async()=>{f.replace();return f.response;};await assert.rejects(loadMessageImage(f.model,'image',''),/request_updated/);
 const other=fixture();other.response.contentType='image/svg+xml';await assert.rejects(loadMessageImage(other.model,'image',''),/preview_unavailable/);
});
test('native image previews retain typed bounded Hub caches and forgetting one Hub preserves another',async()=>{
 files.clear();dirs.clear();opened.length=0;const c={cacheDir:'/cache'},data=new Uint8Array([1,2,3]).buffer;canPreview=async()=>true;
 await previewMessageImage(c,'hub/one',data,'image/jpeg',()=>true);await previewMessageImage(c,'hub-two',data,'image/png',()=>true);assert.equal(opened.length,2);assert.match(opened[0].uri,/message-images\/hub%2Fone\/.*\.jpg$/);assert.equal(opened[0].title,'image');
 clearImagePreviews(c,'hub/one');assert.equal([...files.keys()].some(p=>p.includes('hub%2Fone')),false);assert.equal([...files.keys()].some(p=>p.includes('hub-two')),true);
});
test('a native preview cannot open private bytes after its scope is invalidated during SDK admission',async()=>{
 files.clear();dirs.clear();opened.length=0;let valid=true;canPreview=async()=>{valid=false;return true;};await assert.rejects(previewMessageImage({cacheDir:'/cache'},'forgotten',new Uint8Array([1]).buffer,'image/jpeg',()=>valid),/preview_unavailable/);assert.equal(opened.length,0);assert.equal(files.size,0);canPreview=async()=>true;
});
