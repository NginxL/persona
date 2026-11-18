// Synthetic real-app check for #10544; no live hub or model.
// node web/e2e/assistant-identity.mjs <loopback URL> <shots directory> [before]
import assert from 'node:assert/strict';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { chromium } from 'playwright-core';
const [base, shots, mode] = process.argv.slice(2);
assert.match(base ?? '', /^http:\/\/127\.0\.0\.1:\d+$/);
mkdirSync(shots, { recursive: true });
const before = mode === 'before';
const browser = await chromium.launch({ channel: 'chrome' });
const AT = '2026-11-01T10:00:00Z';
const main = { machine_id: 'demo', session_id: 'demo-main', title: 'Persona', cwd: '/tmp/example', assistant: true, started_by_persona: true, state: 'idle', status: 'idle', updated_at: AT, capabilities: { send: true, read: true } };
const side = { ...main, session_id: 'demo-side', title: 'Weekend plans' };
try {
 for (const scheme of ['light', 'dark']) for (const width of [1280,390]) for (const size of ['standard','large']) {
  if (before && size === 'large') continue;
  const context = await browser.newContext({ viewport: { width, height: 900 }, colorScheme: scheme, reducedMotion: 'reduce', locale: 'en-US' });
  await context.addInitScript(({ scheme, size }) => {
   localStorage.setItem('persona.token','synthetic-demo-token'); localStorage.setItem('persona.language','en');
   localStorage.setItem('persona.theme',scheme); localStorage.setItem('persona.text-size',size);
  }, { scheme, size });
  await context.route('**/api/v1/**', async route => {
   const path = new URL(route.request().url()).pathname.replace(/^\/api\/v1/,''); let body = {};
   if (path === '/machines') body = { machines: [{ id:'demo',name:'Example computer',online:true,os:'linux' }] };
   else if (path === '/sessions') body = { sessions:[main,side] };
   else if (path === '/assistant') body = { machine_id:'demo', main:main.session_id, conversations:[main,side],tasks:[],memory:[] };
   else if (path === '/managers') body = { default:'assistant',managers:[{ id:'assistant',name:'Example assistant',online:true,default:true,builtin:true }] };
   else if (path === '/todos') body = { todos:[] };
   else if (path === '/interactions') body = { interactions:[] };
   else if (path.startsWith('/sessions/demo/')) body = { session:main,items:[],has_more:false };
   else if (path.endsWith('/feed')) body = { items:[] };
   else if (path === '/extensions/ui') body = { slots:[] };
   else if (path === '/extensions') body = { extensions:[] };
   await route.fulfill({ contentType:'application/json',body:JSON.stringify(body) });
  });
  await context.routeWebSocket('**/api/v1/ws**', ws => {
   ws.send(JSON.stringify({ type:'hello',server_version:'synthetic',hub_name:'Example hub',features:[] }));
   ws.onMessage(raw => { if (JSON.parse(String(raw)).type === 'app_ping') ws.send(JSON.stringify({ type:'pong' })); });
  });
  const page = await context.newPage(); const errors=[]; page.on('pageerror', e => errors.push(String(e))); page.setDefaultTimeout(15000);
  const tag = `${before?'before':'after'}-${width}-${size}-${scheme}`;
  const shot = name => page.screenshot({ path:join(shots,`${tag}-${name}.png`) });
  await page.goto(base); await page.locator('[data-chat-title]').waitFor({ state:'attached' });
  await page.getByRole('button',{ name:'Example assistant',exact:false }).first().waitFor({ state:'attached' });
  if (width === 390) await page.locator('header').getByRole('button',{ name:/sidebar|navigation/i }).first().click();
  const row = page.locator('[data-sidebar-column] a[title]').filter({ hasText:before?'Example assistant':'Main chat' }).first(); await row.waitFor({ state:'visible' });
  if (!before) {
   assert.equal(await row.locator('path[d^="M3 10.5 12 3"]').count(),1); assert.equal(await row.locator('canvas,img').count(),0);
   assert.equal((await row.innerText()).split('Main chat').length-1,1);
   assert.equal(await page.locator('[data-assistant-name-menu]').innerText(),'Example assistant');
   if (width === 1280) assert.equal(await page.locator('[data-chat-face]').count(),0);
  }
  await shot('sidebar');
  if (!before && width === 1280) {
   await page.locator('[data-assistant-name-menu]').focus(); await page.keyboard.press('Enter'); await page.getByRole('menu').waitFor();
   await page.getByRole('menuitem',{ name:'Details',exact:true }).waitFor(); await page.getByRole('menuitem',{ name:'Knowledge',exact:true }).waitFor();
   await shot('menu'); await page.keyboard.press('Escape');
   await page.getByRole('button',{ name:/Collapse sidebar/ }).click(); await page.locator('[data-chat-face]').waitFor({ state:'visible' }); await shot('rail');
   await page.getByRole('button',{ name:/Expand sidebar/ }).click(); await page.waitForFunction(() => !document.querySelector('[data-chat-face]'));
   await page.keyboard.press('ControlOrMeta+Shift+Enter'); await page.locator('[data-chat-face]').waitFor({ state:'visible' }); await shot('hidden');
   await page.keyboard.press('ControlOrMeta+Shift+Enter'); await page.waitForFunction(() => !document.querySelector('[data-chat-face]'));
  }
  if (!before) {
   if (width === 390) await row.click();
   await page.keyboard.press('ControlOrMeta+k'); const box=page.getByRole('combobox'); await box.fill('Main chat');
   const result=page.getByRole('option').filter({ hasText:'Main chat' }).first(); await result.waitFor();
   assert.equal(await result.locator('path[d^="M3 10.5 12 3"]').count(),1); await shot('search'); await page.keyboard.press('Escape');
   assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),false); assert.deepEqual(errors,[]);
   await page.goto(`${base}/history`);
   const search = page.locator('input').first(); await search.fill('Main chat');
   const historyRow = page.locator('main a').filter({ hasText:'Main chat' }).first(); await historyRow.waitFor();
   assert.equal(await historyRow.locator('path[d^="M3 10.5 12 3"]').count(),1);
   await shot('history'); assert.deepEqual(errors,[]);
  }
  console.log(`PASS ${tag}`); await context.close();
 }
} finally { await browser.close(); }
