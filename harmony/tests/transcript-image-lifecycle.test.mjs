import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';

const source = readFileSync(new URL('../entry/src/main/ets/components/TranscriptImage.ets', import.meta.url), 'utf8');
const body = source.slice(source.indexOf('export struct TranscriptImage'), source.indexOf('  @Builder imageMenu'))
  .replace('export struct TranscriptImage', 'class TranscriptImage')
  .replace(/@(?:Require|Param|Local|Event)\s+/g, '').replace(/@Monitor\([^)]*\)\s*/g, '');
function deferred() { let resolve; const promise = new Promise(yes => { resolve = yes; }); return { promise, resolve }; }
function fixture() {
  const loads = [], released = [];
  const image = { createImageSource() { return { async getImageInfo() { return { size: {width: 100, height: 100} }; },
    async createPixelMap() { const pixel = { async release() { released.push(pixel); } }; return pixel; }, async release() {} }; } };
  const Component = new Function('image','loadMessageImage','messageImageGalleryWindow', stripTypeScriptTypes(body + '\n}') + '; return TranscriptImage;')(
    image, () => { const gate = deferred(); loads.push(gate); return gate.promise; },
    (entries, selectedIndex) => ({entries,selectedIndex,signature: JSON.stringify(entries.map(e=>e.key))}));
  const component = new Component();
  const client = { profile: {id:'hub',hubId:'hub'},connected:true,generation:1,
    target:()=> 'chat',scope:()=> 'scope',isChat:()=>true,connectionGeneration(){return this.generation;},owns(generation,scope){return this.connected && this.generation===generation && scope==='scope';} };
  component.model=client;component.itemId='message';component.remotePath='/image';
  return {component,client,loads,released};
}
const bytes = () => ({data:new Uint8Array([1,2,3]).buffer,mime:'image/jpeg'});
const settle = () => new Promise(resolve => setImmediate(resolve));

test('reconnect replaces a loaded thumbnail and enables the new generation lease',async()=>{
 const {component,client,loads,released}=fixture();component.aboutToAppear();loads[0].resolve(bytes());await settle();
 const previous=component.pixels;assert.equal(component.loaded.generation,1);
 client.generation++;client.profile={...client.profile};component.ownerChanged();assert.equal(component.pixels,undefined);assert.ok(released.includes(previous));
 loads[1].resolve(bytes());await settle();assert.equal(component.loaded.generation,2);assert.equal(component.loadedOwnerCurrent(),true);assert.equal(component.busy,false);
 component.aboutToDisappear();
});

test('reconnect during loading cannot leave a spinner or publish the old response',async()=>{
 const {component,client,loads}=fixture();component.aboutToAppear();client.generation++;component.ownerChanged();assert.equal(loads.length,2);
 loads[0].resolve(bytes());await settle();assert.equal(component.loaded,undefined);assert.equal(component.busy,true);
 loads[1].resolve(bytes());await settle();assert.equal(component.loaded.generation,2);assert.equal(component.busy,false);component.aboutToDisappear();
});

test('gallery changes and inactivity during loading admit a fresh request on return',async()=>{
 const {component,loads}=fixture();component.aboutToAppear();component.galleryEntries=[{key:'one',itemId:'message',remotePath:'/image',target:'chat'}];component.galleryIndex=0;component.galleryChanged();
 assert.equal(loads.length,2);component.active=false;component.activityChanged();assert.equal(component.busy,false);
 loads[0].resolve(bytes());loads[1].resolve(bytes());await settle();assert.equal(component.loaded,undefined);
 component.active=true;component.activityChanged();loads[2].resolve(bytes());await settle();assert.ok(component.pixels);assert.equal(component.busy,false);component.aboutToDisappear();
});

test('native gallery activity pause does not invalidate its presented source lease',async()=>{
 const {component,client,loads}=fixture();component.aboutToAppear();loads[0].resolve(bytes());await settle();
 const args=[client,component.revision,component.sourceVisit,1,'scope','chat','hub',component.galleryVisit,component.gallerySignature()];
 component.active=false;component.activityChanged();assert.equal(component.currentOwner(...args),true);
 component.active=true;component.activityChanged();assert.equal(loads.length,1);assert.equal(component.currentOwner(...args),true);component.aboutToDisappear();
});
