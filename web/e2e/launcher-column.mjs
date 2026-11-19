// Synthetic acceptance for the real shared ModelPanel (#10249). No owner data or live model.
// node web/e2e/launcher-column.mjs --shots <directory> [--port 19381] [--baseline]
import { createServer } from "vite";
import { chromium } from "playwright-core";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";
const flag = (name) => process.argv[process.argv.indexOf(name) + 1];
const shots = process.argv.includes("--shots") ? resolve(flag("--shots")) : null;
const baseline = process.argv.includes("--baseline");
if (shots) mkdirSync(shots, { recursive: true });
const port = Number(process.argv.includes("--port") ? flag("--port") : 19381);
const web = fileURLToPath(new URL("../", import.meta.url));
const module = `import React from 'react';
import { createRoot } from 'react-dom/client';
import { initI18n } from '/src/i18n/index.ts';
import { setTheme } from '/src/views/theme.ts';
import '/src/styles.css';
await initI18n('en');
const { ModelPanel } = await import('/src/ui/model-panel/ModelPanel.tsx');
setTheme(new URLSearchParams(location.search).get('theme') || 'light');
document.documentElement.dataset.textSize = new URLSearchParams(location.search).get('size') || 'standard';
const { panelStoreKey } = await import('/src/ui/model-panel/store.ts');
localStorage.setItem(panelStoreKey('agent','recent'),JSON.stringify([{provider:'account-0',model:'sonnet'},{provider:'account-1',model:'opus',effort:'high'},{provider:'account-2',model:'sonnet'}]));
const root = createRoot(document.getElementById('root'));
const anchor = document.getElementById('anchor');
const models = [{id:'opus',name:'Opus 5.5',description:'Complex work',efforts:['low','medium','high'],defaultEffort:'medium',fast:true},
{id:'sonnet',name:'Sonnet 5',description:'Everyday work',efforts:['low','medium','high']},
{id:'claude-opus-4-8',name:'Opus 4.8',efforts:[]}];
window.fixture = {picks:[], closed:0, failed:false, current:2, count:7, pending:null};
window.fixture.render = (o={}) => {
 Object.assign(window.fixture,o);
 const f=window.fixture;
 const providers=Array.from({length:f.count},(_,i)=>({id:'account-'+i,name:'Account '+i,engine:i===5?'codex':'claude',models,typable:true,defaultModel:'Opus 5.5',isDefault:i===0,unavailable:i===6?'Signed out: sign in on this computer':undefined}));
 root.render(React.createElement(ModelPanel,{key:f.key||'panel',anchor,providers,state:{current:f.current===null?null:{provider:'account-'+f.current,model:'opus'},recommended:[]},scope:'agent',
 follow:{active:false,label:'Follow model configuration',detail:'Account 0 · Opus 5.5',onFollow:async()=>true},
 quotas:{'account-2':{quota:{gateway:'example',budget:[{label:'Weekly',used_ratio:0.4}]}}},
 onPick:async(c)=>{f.picks.push(c); if(f.pending) await new Promise(r=>f.resolve=r); return !f.failed;},onClose:()=>{f.closed++},onApplyCurrent:async()=>true}));
};
window.fixture.render();`;
const html = '<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Persona model selection fixture</title></head><body class="bg-page text-ink"><main style="padding:24px"><h1>Choose a model</h1><p>Synthetic launcher accounts</p><button id="anchor" style="position:fixed;bottom:28px;right:24px">Account 2 · Opus 5.5</button></main><div id="root"></div><script type="module" src="/__launcher-fixture.js"></script></body></html>';
const server = await createServer({ root: web, configFile: web + '/vite.config.ts', server: { host: '127.0.0.1', port, strictPort: true }, plugins: [{
 name:'launcher-column-fixture', resolveId(id){if(id==='/__launcher-fixture.js') return '\0launcher-fixture.js'}, load(id){if(id==='\0launcher-fixture.js') return module},
 configureServer(vite){vite.middlewares.use(async(req,res,next)=>{if(!req.url.startsWith('/__launcher-fixture?'))return next();res.setHeader('Content-Type','text/html');res.end(await vite.transformIndexHtml('/fixture.html',html));})}
}] });
let browser;
const results=[];
try {
 await server.listen();
 browser=await chromium.launch({channel:'chrome',headless:true});
 for(const width of [1280,390]) for(const theme of ['light','dark']) for(const size of ['standard','large']) {
  const context=await browser.newContext({viewport:{width,height:900},colorScheme:theme,hasTouch:width<600,reducedMotion:size==='large'?'reduce':'no-preference'});
  const page=await context.newPage(); const errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto(`http://127.0.0.1:${port}/__launcher-fixture?theme=${theme}&size=${size}`);
  const panel=page.locator('[data-model-panel]'); await panel.waitFor();
  await page.waitForFunction(()=>getComputedStyle(document.querySelector('[data-model-panel]')).visibility==='visible');
  await page.evaluate(async()=>{await document.fonts.ready;await Promise.all(document.getAnimations().map(a=>a.finished.catch(()=>{})));});
  await page.evaluate(async()=>{await new Promise(requestAnimationFrame);await new Promise(requestAnimationFrame);});
  await page.waitForFunction(()=>{const el=document.querySelector('[data-model-panel]');return getComputedStyle(el).clipPath==='none' && getComputedStyle(el.firstElementChild).opacity==='1';});
  const tag=`${width}-${theme}-${size}`;
  if(!baseline && size==='large') assert(await panel.evaluate(el=>getComputedStyle(el).animationName==='none'),`${tag}: reduced motion skips the clip`);
  if(size==='large') assert(await panel.evaluate(el=>parseFloat(getComputedStyle(el).animationDuration)<=0.002),`${tag}: reduced motion`);
  if(!baseline){assert.equal(await panel.locator('[data-panel-quick] [data-panel-row]').count(),3);}
  if(shots)await page.screenshot({animations:"disabled",path:resolve(shots,`${baseline?'before':'after'}-${tag}.png`)});
  const dimensions=await panel.evaluate(el=>({left:el.getBoundingClientRect().left,right:el.getBoundingClientRect().right,width:el.clientWidth,scroll:el.scrollWidth}));
  assert(dimensions.left>=0 && dimensions.right<=width+1 && dimensions.scroll<=dimensions.width+1,`${tag}: panel overflow`);
  if(baseline){
   const feedback=await page.locator('[data-rail-cell="account-4"]').evaluate(async el=>{const t=performance.now();el.click();await new Promise(requestAnimationFrame);return performance.now()-t});
   results.push({tag,dimensions,feedback});await context.close();continue;
  }
  assert.equal(await panel.locator('[data-panel-launcher]').count(),7);
  assert.deepEqual(await panel.locator('[data-panel-launcher][aria-expanded="true"]').evaluateAll(els=>els.map(e=>e.dataset.panelLauncher)),['account-2']);
  assert.match(await panel.textContent(),/Signed out/);
  assert.match(await panel.locator('[data-panel-launcher="account-2"]').textContent(),/60%/);
  const feedback=await panel.locator('[data-panel-launcher="account-4"]').evaluate(async el=>{const t=performance.now();el.click();await new Promise(requestAnimationFrame);return performance.now()-t});
  await page.waitForFunction(()=>document.querySelector('[data-panel-launcher="account-4"]').getAttribute('aria-expanded')==='true');
  await page.evaluate(()=>window.fixture.failed=true);
  const wanted=panel.locator('[data-panel-section="provider"][aria-label="Account 4"] [data-panel-row]').filter({hasText:'Sonnet 5'});
  await wanted.click({position:{x:48,y:12}});
  await page.waitForFunction(()=>window.fixture.picks.length===1);
  assert.equal(await panel.count(),1);
  assert.equal(await page.evaluate(()=>window.fixture.picks[0].provider),'account-4');
  const search=panel.locator('input'); await search.fill('Sonnet');
  assert.equal(await panel.locator('[data-panel-row]:not([data-follow-row])').count(),7);
  assert.equal(await panel.locator('[data-panel-section], [data-panel-family]').count(),0);
  for(const [i,text] of (await panel.locator('[data-panel-row]:not([data-follow-row])').allTextContents()).entries()) assert(text.includes(`Account ${i}`),`${tag}: missing launcher`);
  if(shots)await page.screenshot({animations:"disabled",path:resolve(shots,`search-${tag}.png`)});
  await search.fill('vendor/new-model');
  const typed=panel.locator('[data-panel-row]:not([data-follow-row])').filter({hasText:'Account 3'});await typed.click({position:{x:48,y:12}});
  assert.deepEqual(await page.evaluate(()=>window.fixture.picks.at(-1)),{provider:'account-3',model:'vendor/new-model'});
  await search.fill('Account 1');assert.equal(await panel.locator('[data-panel-launcher]').count(),1);
  await panel.locator('[data-panel-launcher="account-1"]').click();assert.equal(await search.inputValue(),'');
  await page.evaluate(()=>window.fixture.render({count:12,current:null,key:'many'}));
  await page.waitForFunction(()=>document.querySelectorAll('[data-panel-launcher]').length===12);
  assert.equal(await panel.locator('[data-panel-launcher][aria-expanded="true"]').count(),0);
  const scroll=await panel.locator('[data-panel-launcher="account-11"]').evaluate(el=>{let p=el.parentElement;while(p&&getComputedStyle(p).overflowY!=='auto')p=p.parentElement;return {height:p.clientHeight,scroll:p.scrollHeight,width:p.clientWidth,scrollWidth:p.scrollWidth}});
  assert(scroll.scroll>scroll.height && scroll.scrollWidth<=scroll.width+1,`${tag}: twelve-launcher scroll`);
  await panel.locator('[data-panel-launcher="account-11"]').scrollIntoViewIfNeeded();
  if(shots)await page.screenshot({animations:"disabled",path:resolve(shots,`many-${tag}.png`)});
  assert.deepEqual(errors,[],`${tag}: browser errors`);
  results.push({tag,dimensions,feedback,scroll}); console.log(`PASS ${tag}`);
  await context.close();
 }
 if(shots)writeFileSync(resolve(shots,'results.json'),JSON.stringify(results,null,2));
 console.log(JSON.stringify(results,null,2));
} finally {await browser?.close();await server.close();}
