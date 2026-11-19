// Synthetic settings acceptance: no real bot, group or account is contacted.
// node web/e2e/feishu-topic-policy.mjs <web-origin> <shots-directory>
import assert from 'node:assert/strict';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { chromium } from 'playwright-core';
const [base, shots] = process.argv.slice(2);
if (!base || !shots) throw new Error('web origin and screenshots directory required');
mkdirSync(shots, { recursive: true });
const browser = await chromium.launch({ channel: 'chrome', headless: true });
try {
  for (const width of [1280, 390]) for (const theme of ['light', 'dark']) for (const textSize of ['standard', 'large']) {
    const context = await browser.newContext({ viewport: { width, height: 1000 }, colorScheme: theme, reducedMotion: 'reduce' });
    let status = { enabled:true,app_id:'cli_example',domain:'https://open.feishu.cn',has_secret:true,from_config:false,connected:true,bot_name:'Example bot',owner:{name:'Example owner',paired_at:1},features:{main:true,cards:true,all_cards:false,pushes:false,sync:false,agents:false,reports:true},topic_chats:[{chat_id:'oc_example',name:'Example group',require_owner_mention:true}] };
    let failNext = false;
    const edits = [];
    await context.addInitScript(({theme,textSize}) => {
      localStorage.setItem('persona.token','synthetic-demo-token');
      localStorage.setItem('persona.language','en');
      localStorage.setItem('persona.theme',theme);
      localStorage.setItem('persona.text-size',textSize);
    }, {theme,textSize});
    await context.route('**/api/v1/**', async (route) => {
      const path = new URL(route.request().url()).pathname.replace(/^\/api\/v1/,'');
      let body = {};
      if (path === '/bots/feishu') {
        if (route.request().method()==='PUT') {
          const edit=route.request().postDataJSON(); edits.push(edit);
          if (failNext) { failNext=false; return route.fulfill({status:500,contentType:'application/json',body:JSON.stringify({error:'Synthetic save failure'})}); }
          status={...status,...edit};
        }
        body=status;
      } else if(path==='/machines') body={machines:[]};
      else if(path==='/sessions') body={sessions:[]};
      else if(path==='/interactions') body={interactions:[]};
      else if(path==='/assistant') body={conversations:[],tasks:[],memory:[]};
      else if(path==='/managers') body={managers:[]};
      else if(path==='/connectors') body={connectors:[]};
      else if((path==='/mail/accounts' || path==='/lark/accounts')) body={accounts:[]};
      else if(path==='/extensions') body={extensions:[]};
      else if(path==='/extensions/ui') body={slots:[]};
      await route.fulfill({contentType:'application/json',body:JSON.stringify(body)});
    });
    await context.routeWebSocket('**/api/v1/ws**',ws=>{
      ws.send(JSON.stringify({type:'hello',server_version:'synthetic',hub_name:'Example hub',features:[]}));
      ws.onMessage(raw=>{if(JSON.parse(String(raw)).type==='app_ping')ws.send(JSON.stringify({type:'pong'}));});
    });
    const page=await context.newPage();
    const settle = () => page.evaluate(async () => {
      await document.fonts.ready;
      await Promise.all(document.getAnimations().filter((a) => a.effect?.getTiming().iterations !== Infinity).map((a) => a.finished.catch(() => {})));
      await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    });
    const errors=[];page.on('pageerror',e=>errors.push(String(e)));page.on('console',m=>{if(m.type()==='error')console.error('browser console:',m.text());});page.setDefaultTimeout(15000);
    await page.goto(`${base}/settings?tab=connectors&connector=feishu-bot`);
    const group=page.locator('[data-settings-group="topic-groups"]');await group.waitFor().catch(async error=>{console.error(await page.locator("body").innerText());throw error;});await group.scrollIntoViewIfNeeded();
    const toggle=page.getByRole('switch',{name:'Only respond when I mention the bot — Example group',exact:true});
    assert.equal(await toggle.getAttribute('aria-checked'),'true');
    assert.equal(await page.getByRole('combobox',{name:'Home topic group',exact:true}).inputValue(),'');
    failNext=true;await toggle.click();await page.getByRole('alert').filter({hasText:'Synthetic save failure'}).waitFor();
    assert.equal(await toggle.getAttribute('aria-checked'),'true');
    await toggle.click();await page.waitForFunction(()=>document.querySelector('[role="switch"][aria-label$="— Example group"]')?.getAttribute('aria-checked')==='false');
    await toggle.click();await page.waitForFunction(()=>document.querySelector('[role="switch"][aria-label$="— Example group"]')?.getAttribute('aria-checked')==='true');
    await page.reload();await group.waitFor();await group.scrollIntoViewIfNeeded();
    assert.equal(await toggle.getAttribute('aria-checked'),'true');
    assert.equal(await page.getByRole('combobox',{name:'Home topic group',exact:true}).inputValue(),'');
    assert.deepEqual(errors,[]);
    assert.equal(await group.evaluate(el=>el.scrollWidth>el.clientWidth),false);
    await settle();
    await page.screenshot({path:join(shots,`${width}-${theme}-${textSize}.png`)});
    await page.getByRole('combobox',{name:'Home topic group',exact:true}).selectOption('oc_example');
    await page.waitForFunction(()=>document.querySelector('select[aria-label="Home topic group"]')?.value==='oc_example');
    assert.equal(edits.at(-1).topic_chats[0].home,true);
    await page.getByRole('combobox',{name:'Home topic group',exact:true}).selectOption('');
    await page.waitForFunction(()=>document.querySelector('select[aria-label="Home topic group"]')?.value==='');
    assert.equal(edits.at(-1).topic_chats[0].home,false);
    if (textSize === 'large') {
      await page.getByRole('button', { name: 'Add group', exact: true }).click();
      await page.getByLabel('Group ID', { exact: true }).fill('oc_isolated');
      await page.getByLabel('Group name (optional)', { exact: true }).fill('Another example group');
      const nextPolicy = page.getByRole('switch', { name: 'Only respond when I mention the bot', exact: true });
      assert.equal(await nextPolicy.getAttribute('aria-checked'), 'false');
      await nextPolicy.click();
      await group.scrollIntoViewIfNeeded();
      assert.equal(await group.evaluate(el => el.scrollWidth > el.clientWidth), false);
      await settle();
    await page.screenshot({ path: join(shots, `${width}-${theme}-enroll.png`) });
      await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    }
    console.log(`PASS ${width} ${theme} ${textSize}: failure retained policy, reverse, reload, separate home`);
    await context.close();
  }
} finally { await browser.close(); }
