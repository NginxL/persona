// Synthetic scheduled prompt flow; no real hub, task or model is contacted.
import assert from 'node:assert/strict';
import { readFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { chromium } from 'playwright-core';
const args = process.argv.slice(2),
  flag = name => args[args.indexOf(name) + 1],
  base = args[0],
  shots = flag('--shots');
assert(['localhost', '127.0.0.1'].includes(new URL(base).hostname));
mkdirSync(shots, {
  recursive: true
});
const watchdog = setTimeout(() => {
  console.error('TIMEOUT task input verification');
  process.exit(124);
}, 180000);
const browser = await chromium.launch({
  executablePath: flag('--chromium'),
  headless: true,
  args: ['--no-sandbox']
});
try {
  for (const width of [1280, 390]) for (const theme of ['light', 'dark']) {
    const context = await browser.newContext({
      viewport: {
        width,
        height: 900
      },
      colorScheme: theme,
      reducedMotion: 'reduce'
    });
    await context.addInitScript({
      content: readFileSync(new URL('./task-input-fixture.js', import.meta.url), 'utf8')
    });
    await context.addInitScript(({
      theme
    }) => localStorage.setItem('persona.theme', theme), {
      theme
    });
    const page = await context.newPage(),
      errors = [];
    page.on('pageerror', e => errors.push(String(e)));
    page.setDefaultTimeout(12000);
    await page.goto(`${base}/routines`);
    await page.getByRole('button', {
      name: /Review project files/
    }).first().getByText('Assistant · claude · main-model · high', {
      exact: true
    }).waitFor();
    await page.getByRole('button', {
      name: 'New',
      exact: true
    }).click();
    await page.getByRole('menuitem', {
      name: 'Blank',
      exact: true
    }).click();
    const creating = page.getByRole('dialog');
    await creating.getByRole('textbox', {
      name: 'Title',
      exact: true
    }).fill('Weekly notes');
    const newPrompt = creating.getByRole('textbox', {
      name: 'Prompt',
      exact: true
    });
    await newPrompt.fill('/Project');
    await creating.getByRole('option').filter({
      hasText: 'Project review'
    }).waitFor();
    const detail = creating.locator('[data-picker-detail]');
    await detail.waitFor();
    const withinDialog = await detail.evaluate(element => {
      const preview = element.getBoundingClientRect();
      const bounds = element.closest('[role=dialog]').getBoundingClientRect();
      return preview.left >= bounds.left && preview.right <= bounds.right;
    });
    assert(withinDialog, 'Skill details must fit inside the task editor.');
    await page.screenshot({ path: join(shots, `task-menu-${width}-${theme}.png`) });
    await newPrompt.press('Enter');
    await creating.getByLabel('Context sent with the message').getByText(/Project review/).waitFor();
    await newPrompt.fill('Read the notes.');
    await creating.getByRole('button', {
      name: 'Add',
      exact: true
    }).click();
    await creating.waitFor({
      state: 'hidden'
    });
    const created = await page.evaluate(() => window.__taskInputFixture.created);
    assert(created.prompt.includes('ref="project-review"'));
    assert.equal(created.launcher, undefined);
    await page.getByRole('button', {
      name: /Weekly notes/
    }).first().click();
    assert.equal(await newPrompt.inputValue(), 'Read the notes.');
    await creating.getByLabel('Context sent with the message').getByText(/Project review/).waitFor();
    await creating.getByRole('button', {
      name: 'Cancel',
      exact: true
    }).click();
    await creating.waitFor({
      state: 'hidden'
    });
    await page.getByRole('button', {
      name: /Review project files/
    }).first().click();
    const dialog = page.getByRole('dialog');
    const disclosure = dialog.locator('summary').filter({
      hasText: 'Execution options'
    });
    const input = dialog.getByRole('textbox', {
      name: 'Prompt',
      exact: true
    });
    await dialog.getByRole('radio', {
      name: 'Cron',
      exact: true
    }).waitFor();
    await disclosure.getByText('Assistant · claude · main-model · high', {
      exact: true
    }).waitFor();
    assert(await disclosure.evaluate(el => !!(el.compareDocumentPosition(document.querySelector('textarea')) & Node.DOCUMENT_POSITION_FOLLOWING)));
    assert.equal(await page.evaluate(() => window.__taskInputFixture.reads.some(r => r.path.includes('/models'))), false);
    await dialog.getByText('Execution options', {
      exact: true
    }).click();
    await dialog.getByRole('combobox', {
      name: 'Computer',
      exact: true
    }).selectOption('remote');
    await dialog.getByRole('combobox', {
      name: 'Agent',
      exact: true
    }).selectOption('codex');
    await dialog.getByRole('button').filter({
      hasText: 'Agent default'
    }).click();
    await dialog.getByRole('option', {
      name: /Example model/
    }).click();
    await dialog.getByRole('combobox', {
      name: 'Reasoning effort',
      exact: true
    }).selectOption('high');
    await dialog.getByRole('textbox', {
      name: /Working directory/
    }).fill('/work/project');
    await input.fill('@notes');
    await dialog.getByRole('option').filter({
      hasText: 'notes.txt'
    }).click();
    await dialog.getByLabel('Context sent with the message').getByText('notes.txt', {
      exact: true
    }).waitFor();
    await input.fill('@Project');
    await dialog.getByRole('option').filter({
      hasText: 'Project review'
    }).click();
    await dialog.getByLabel('Context sent with the message').getByText(/Project review/).waitFor();
    await input.fill('/Review');
    await page.getByRole('option').filter({
      hasText: 'Review'
    }).filter({
      hasText: 'Example'
    }).click();
    assert.equal(await input.inputValue(), 'Review the selected files.');
    assert.equal(await dialog.getByTestId('composer-send').count(), 0);
    for (let i = 0; i < 10 && (await page.locator('[aria-live="polite"] button[aria-label="Close"]').count()); i++) await page.locator('[aria-live="polite"] button[aria-label="Close"]').first().click();
    await dialog.getByRole('button', {
      name: 'Save',
      exact: true
    }).click();
    await dialog.waitFor({
      state: 'hidden'
    });
    const writes = await page.evaluate(() => window.__taskInputFixture.writes);
    assert.equal(writes.length, 1);
    const saved = writes[0];
    assert.equal(saved.prompt, undefined);
    assert.equal(saved.execution_machine_id, 'remote');
    assert.equal(saved.model, 'test-model');
    assert.equal(saved.launcher, 'codex');
    assert.equal(saved.effort, 'high');
    for (const bit of ['kind="file"', 'kind="skill"', 'path="notes.txt"', 'machine="remote"', 'cwd="/work/project"', 'launcher="codex"', 'ref="project-review"']) assert(saved.structured_prompt.includes(bit), bit);
    await page.getByRole('button', {
      name: /Review project files/
    }).first().getByText('agents · codex · test-model · high', {
      exact: true
    }).waitFor();
    await page.getByRole('button', {
      name: /Review project files/
    }).first().click();
    await input.waitFor();
    assert.equal(await input.inputValue(), 'Review the selected files.');
    assert((await dialog.innerText()).includes('Project review'));
    assert((await dialog.innerText()).includes('notes.txt'));
    await disclosure.getByText('agents · codex · test-model · high', {
      exact: true
    }).waitFor();
    assert(await disclosure.evaluate(el => {
      const [label, detail] = el.querySelectorAll('span'),
        a = label.getBoundingClientRect(),
        b = detail.getBoundingClientRect();
      return Math.abs((a.top + a.bottom - b.top - b.bottom) / 2) < 1 && el.scrollWidth <= el.clientWidth;
    }), 'execution title and summary share one line');
    await dialog.getByText('Checking the task’s computer and working directory…', {
      exact: true
    }).waitFor({
      state: 'hidden'
    });
    // Frame the whole shared composer card, including chips and its toolbar.
    await input.locator('..').locator('..').scrollIntoViewIfNeeded();
    for (let i = 0; i < 10 && (await page.locator('[aria-live="polite"] button[aria-label="Close"]').count()); i++) await page.locator('[aria-live="polite"] button[aria-label="Close"]').first().click();
    await page.screenshot({
      path: join(shots, `task-input-${width}-${theme}.png`)
    });
    // Changing the execution directory invalidates the selected file without discarding it.
    await disclosure.click();
    const directory = dialog.getByRole('textbox', {
      name: /Working directory/
    });
    await directory.fill('/work/other');
    const changed = dialog.getByText('This file belongs to a different computer, folder or agent. Remove it and select it again.', {
      exact: true
    });
    await changed.waitFor();
    assert((await dialog.innerText()).includes('notes.txt'));
    await directory.fill('/work/project');
    await changed.waitFor({
      state: 'hidden'
    });
    await disclosure.click();
    // Refused saves preserve text and both reference chips.
    await page.evaluate(() => window.__taskInputFixture.refuse = true);
    await input.fill('Keep this draft');
    await dialog.getByRole('button', {
      name: 'Save',
      exact: true
    }).click();
    assert.equal(await input.inputValue(), 'Keep this draft');
    assert((await dialog.innerText()).includes('Project review'));
    await page.getByText('Selected reference is unavailable', {
      exact: true
    }).waitFor();
    for (let i = 0; i < 10 && (await page.locator('[aria-live="polite"] button[aria-label="Close"]').count()); i++) await page.locator('[aria-live="polite"] button[aria-label="Close"]').first().click();
    await page.evaluate(() => document.documentElement.style.setProperty('--text-scale', '2'));
    await disclosure.click();
    await page.waitForFunction(el => {
      const css = getComputedStyle(el);
      return el.clientHeight >= parseFloat(css.lineHeight) + parseFloat(css.paddingTop) + parseFloat(css.paddingBottom);
    }, await input.elementHandle());
    await input.scrollIntoViewIfNeeded();
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false);
    assert.deepEqual(await dialog.evaluate(el => {
      const b = el.getBoundingClientRect();
      return [...el.querySelectorAll('textarea,input,select,fieldset')].filter(n => {
        const r = n.getBoundingClientRect();
        return r.width > 0 && (r.left < b.left - 1 || r.right > b.right + 1);
      }).map(n => n.tagName);
    }), []);
    await page.screenshot({
      path: join(shots, `task-input-${width}-${theme}-large.png`)
    });
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
    await dialog.waitFor({ state: 'hidden' });
    await page.evaluate(() => {
      document.documentElement.style.setProperty('--text-scale', '1');
      window.__taskInputFixture.taskInput = false;
    });
    await page.getByRole('button', { name: 'New', exact: true }).click();
    await page.getByRole('menuitem', { name: 'Blank', exact: true }).click();
    await dialog.getByText('Update Persona on this computer to select files and skills.').waitFor();
    assert(await dialog.locator('[data-composer-plus]').isDisabled());
    await dialog.getByRole('textbox', { name: 'Title', exact: true }).fill('Legacy computer');
    await input.fill('/Review');
    await dialog.getByRole('option', { name: 'Review Example', exact: true }).click();
    assert.equal(await input.inputValue(), 'Review the selected files.');
    await dialog.getByRole('button', { name: 'Add', exact: true }).click();
    await dialog.waitFor({ state: 'hidden' });
    const plain = await page.evaluate(() => window.__taskInputFixture.created);
    assert.equal(plain.prompt, 'Review the selected files.');
    assert.equal(plain.prompt_selections, false);
    assert.deepEqual(errors, []);
    console.log(`PASS ${width} ${theme}: inherited summary before prompt, Cron visible, execution, @file, @skill, /prompt, save, reopen, changed directory, refused save, 2x text, plain prompt on an older computer`);
    await context.close();
  }
} finally {
  await browser.close();
  clearTimeout(watchdog);
}
