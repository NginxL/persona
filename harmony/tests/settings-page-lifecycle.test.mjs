import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';

const source = readFileSync(new URL('../entry/src/main/ets/components/SettingsPage.ets', import.meta.url), 'utf8');
function method(name) {
  const start = source.indexOf('  private ' + name + '(');
  assert.ok(start >= 0, name);
  const body = source.indexOf('{', start); let depth = 1, quote = '', escape = false, end = body + 1;
  for (; end < source.length && depth; end++) {
    const ch = source[end];
    if (quote) { if (escape) escape = false; else if (ch === '\\') escape = true; else if (ch === quote) quote = ''; }
    else if (ch === '"' || ch === "'" || ch === '`') quote = ch;
    else if (ch === '{') depth++;
    else if (ch === '}') depth--;
  }
  assert.equal(depth, 0, name); return source.slice(start, end);
}
const NativeSettings = new Function('Wire', 'tr', 'return ' + stripTypeScriptTypes('class NativeSettings {' +
  ['confirmExtensionSave', 'confirmExtensionRemove', 'confirmConnectorLogout', 'confirmChannelUnbind'].map(method).join('\n') + '}'))(
  { text: (row, key, fallback = '') => row[key] || fallback }, value => value);
function fixture() {
  const page = new NativeSettings(); let generation = 1, dialog, writes = 0, backs = 0;
  page.machineRevision = 2; page.mounted = true; page.active = true;
  page.model = { scope: () => 'hub/personal', profile: { manager: 'personal' }, connectionGeneration: () => generation };
  page.management = { selectedId: 'target', extensionDetail: { name: 'Target' }, operation: {}, channelData: () => ({ name: 'Target' }),
    saveExtensionConfig: async () => { writes++; return true; }, removeExtension: async () => { writes++; return true; },
    connectorLogout: async () => { writes++; return true; }, channelUnbind: async () => { writes++; return true; } };
  page.selectedConnector = () => ({ id: 'target', title: 'Target' });
  page.getUIContext = () => ({ showAlertDialog: value => { dialog = value; } }); page.onBack = () => { backs++; };
  return { page, confirm: () => dialog.secondaryButton.action(), reconnect: () => generation++, writes: () => writes, backs: () => backs };
}
for (const name of ['confirmExtensionSave', 'confirmExtensionRemove', 'confirmConnectorLogout', 'confirmChannelUnbind']) {
  test(name + ' rejects a dialog from a previous connection or page lifetime', () => {
    for (const change of ['reconnect', 'hide-and-return', 'replace-owner']) {
      const f = fixture(); f.page[name]();
      if (change === 'reconnect') f.reconnect();
      if (change === 'hide-and-return') f.page.machineRevision++;
      if (change === 'replace-owner') f.page.model = { ...f.page.model };
      f.confirm(); assert.equal(f.writes(), 0, change);
    }
  });
  test(name + ' admits its unchanged visible confirmation', async () => {
    const f = fixture(); f.page[name](); f.confirm(); await Promise.resolve(); assert.equal(f.writes(), 1);
    assert.equal(f.backs(), name === 'confirmExtensionRemove' ? 1 : 0);
  });
}


test('a late removal ACK cannot pop a reentered native detail page', async () => {
  const f = fixture(); let finish;
  f.page.management.removeExtension = () => new Promise(resolve => { finish = resolve; });
  f.page.confirmExtensionRemove(); f.confirm(); f.page.machineRevision++;
  finish(true); await Promise.resolve(); assert.equal(f.backs(), 0);
});
