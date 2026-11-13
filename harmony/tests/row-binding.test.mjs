import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';

const source = readFileSync(new URL('../entry/src/main/ets/models/RowBinding.ets', import.meta.url), 'utf8');
const executable = stripTypeScriptTypes(source.replace(/@ObservedV2\s*/g, '').replace(/@Trace\s*/g, '').replace(/\bexport\s+/g, ''));
const { BoundRow, reconcileBoundRows } = new Function(executable + '\nreturn { BoundRow, reconcileBoundRows };')();

test('same owner and scope retain the wrapper while replacing its immutable row value', () => {
  const owner = {}, original = Object.freeze({ id: 'a', title: 'Old', state: 'working', raw: '{"status":"working"}' });
  const initial = reconcileBoundRows([], [original], row => row.id, undefined, owner, '', 'hub/a');
  const binding = initial[0];
  const replacement = Object.freeze({ id: 'a', title: 'New', state: 'needs_you', raw: '{"status":"needs_you"}' });
  const next = reconcileBoundRows(initial, [replacement], row => row.id, owner, owner, 'hub/a', 'hub/a');
  let opened;
  const onClick = () => { opened = next[0].value; };
  onClick();

  assert.equal(next[0], binding);
  assert.equal(next[0].key, binding.key);
  assert.equal(next[0].value, replacement);
  assert.equal(next[0].value.title, 'New');
  assert.equal(next[0].value.state, 'needs_you');
  assert.equal(next[0].value.raw, '{"status":"needs_you"}');
  assert.equal(opened, replacement);
  assert.deepEqual(original, { id: 'a', title: 'Old', state: 'working', raw: '{"status":"working"}' });
});

test('reorder, removal, and duplicate keys preserve distinct wrappers without stale values', () => {
  const owner = {};
  const original = [
    Object.freeze({ id: 'dup', title: 'first' }),
    Object.freeze({ id: 'other', title: 'other' }),
    Object.freeze({ id: 'dup', title: 'second' })
  ];
  const initial = reconcileBoundRows([], original, row => row.id, undefined, owner, '', 'scope');
  const replacements = [
    Object.freeze({ id: 'dup', title: 'first refreshed' }),
    Object.freeze({ id: 'dup', title: 'second refreshed' }),
    Object.freeze({ id: 'other', title: 'other refreshed' })
  ];
  const reordered = reconcileBoundRows(initial, replacements, row => row.id, owner, owner, 'scope', 'scope');

  assert.equal(new Set(initial).size, initial.length);
  assert.equal(new Set(initial.map(binding => binding.key)).size, initial.length);
  assert.equal(reordered[0], initial[0]);
  assert.equal(reordered[1], initial[2]);
  assert.equal(reordered[2], initial[1]);
  assert.deepEqual(reordered.map(binding => binding.key), [initial[0].key, initial[2].key, initial[1].key]);
  assert.deepEqual(reordered.map(binding => binding.value.title), ['first refreshed', 'second refreshed', 'other refreshed']);
  assert.notEqual(reordered[0], reordered[1]);

  const removed = reconcileBoundRows(reordered, [Object.freeze({ id: 'dup', title: 'remaining' })], row => row.id,
    owner, owner, 'scope', 'scope');
  assert.equal(removed.length, 1);
  assert.equal(removed[0], initial[0]);
  assert.equal(removed[0].value.title, 'remaining');
});

test('owner or scope changes create fresh wrappers', () => {
  const owner = {}, otherOwner = {};
  const initial = reconcileBoundRows([], [{ id: 'a' }], row => row.id, undefined, owner, '', 'scope/a');
  const newOwner = reconcileBoundRows(initial, [{ id: 'a' }], row => row.id, owner, otherOwner, 'scope/a', 'scope/a');
  const newScope = reconcileBoundRows(newOwner, [{ id: 'a' }], row => row.id, otherOwner, otherOwner, 'scope/a', 'scope/b');

  assert.notEqual(newOwner[0], initial[0]);
  assert.notEqual(newScope[0], newOwner[0]);
  assert.equal(newOwner[0].identity, initial[0].identity);
  assert.equal(newScope[0].identity, newOwner[0].identity);
  assert.notEqual(newOwner[0].key, initial[0].key);
  assert.notEqual(newScope[0].key, newOwner[0].key);
});

function pageMethods(file, names) {
  const source = readFileSync(new URL('../entry/src/main/ets/components/' + file + '.ets', import.meta.url), 'utf8');
  const methods = names.map(name => {
    const start = source.indexOf('  private ' + name + '('); assert.ok(start >= 0, name);
    const body = source.indexOf('{', start); let depth = 1, quote = '', escaped = false, end = body + 1;
    for (; end < source.length && depth; end++) {
      const char = source[end];
      if (quote) { if (escaped) escaped = false; else if (char === '\\') escaped = true; else if (char === quote) quote = ''; }
      else if (char === '"' || char === "'" || char === '`') quote = char;
      else if (char === '{') depth++; else if (char === '}') depth--;
    }
    assert.equal(depth, 0, name); return source.slice(start, end);
  }).join('\n');
  const code = stripTypeScriptTypes('class NativeRows {\n' + methods + '\n}');
  return new Function('reconcileBoundRows', 'Wire', code + '\nreturn new NativeRows();')(reconcileBoundRows,
    { text: (row, key) => typeof row[key] === 'string' ? row[key] : '', rows: (row, key) => row[key] || [] });
}
const model = () => ({ profile: { id: 'hub', manager: 'work' }, scope: () => 'hub/work', selected: { key: () => 'agent' } });

test('native settings retain current schedule and field bindings and retire them on a new target', () => {
  const page = pageMethods('SettingsPage', ['syncManagementRows', 'syncInputRows']);
  page.model = model(); page.page = 'schedules'; page.managementRowBindings = []; page.scheduleRunBindings = []; page.channelFeatureBindings = [];
  const names = ['extensionConfigFields', 'extensionSecretFields', 'authFields', 'connectorFields', 'channelFields'];
  page.management = { rows: [{ id: 'task', title: 'Old' }], scheduleRuns: [], selectedId: 'extension-a', featureData: [{ id: 'read', title: 'Read', on: false }], channelData: () => ({ features: page.management.featureData }) };
  for (const name of names) { page.management[name] = [{ key: 'value', value: 'old' }]; page[name + 'Bindings'] = []; }
  page.syncManagementRows(); page.syncInputRows();
  const feature = page.channelFeatureBindings[0], row = page.managementRowBindings[0], field = page.extensionConfigFieldsBindings[0], currentAction = () => row.value;
  page.management.rows = [Object.freeze({ id: 'task', title: 'New' })];
  page.management.extensionConfigFields = [Object.freeze({ key: 'value', value: 'new' })];
  page.management.featureData = [{ id: 'read', title: 'Read current', on: true }];
  page.syncManagementRows(); page.syncInputRows();
  assert.equal(page.managementRowBindings[0], row); assert.equal(currentAction().title, 'New');
  assert.equal(page.channelFeatureBindings[0], feature); assert.equal(feature.value.title, 'Read current'); assert.equal(feature.value.on, true);
  assert.equal(page.extensionConfigFieldsBindings[0], field); assert.equal(field.value.value, 'new');
  page.management.selectedId = 'extension-b'; page.syncInputRows(); assert.notEqual(page.extensionConfigFieldsBindings[0].key, field.key);
  page.model.profile.manager = 'other'; page.syncManagementRows(); assert.notEqual(page.managementRowBindings[0].key, row.key);
});

test('native workspace rows retain latest file, Git, stash and preview values across refresh', () => {
  const page = pageMethods('SessionTools', ['syncRowBindings']); page.model = model();
  page.workspace = { cwd: '/demo', currentPath: '/demo', entries: [Object.freeze({ path: '/demo/a', size: 1 })],
    gitFiles: [{ path: 'a', mark: 'M' }], gitStashes: [{ oid: 'oid', index: 0 }], previewServers: [{ name: 'app', listening: false }] };
  for (const name of ['fileRowBindings', 'gitRowBindings', 'stashRowBindings', 'previewRowBindings']) page[name] = [];
  page.syncRowBindings();
  const file = page.fileRowBindings[0], git = page.gitRowBindings[0], stash = page.stashRowBindings[0], preview = page.previewRowBindings[0], original = file.value;
  page.workspace.entries = [{ path: '/demo/a', size: 20 }]; page.workspace.gitFiles = [{ path: 'a', mark: 'A' }];
  page.workspace.gitStashes = [{ oid: 'oid', index: 2 }]; page.workspace.previewServers = [{ name: 'app', listening: true }]; page.syncRowBindings();
  assert.equal(page.fileRowBindings[0], file); assert.equal(file.value.size, 20); assert.equal(original.size, 1);
  assert.equal(page.gitRowBindings[0], git); assert.equal(git.value.mark, 'A');
  assert.equal(page.stashRowBindings[0], stash); assert.equal(stash.value.index, 2);
  assert.equal(page.previewRowBindings[0], preview); assert.equal(preview.value.listening, true);
  page.model.selected = { key: () => 'another-agent' }; page.syncRowBindings();
  assert.notEqual(page.fileRowBindings[0].key, file.key); assert.notEqual(page.gitRowBindings[0].key, git.key);
});

test('the generic native resource list keeps a retained row current through refresh and filtering', () => {
  const page = pageMethods('ResourcePage', ['syncRows', 'rows']); page.model = model(); page.kind = 'todo';
  page.rowBindings = []; page.search = ''; page.source = 0;
  page.model.resourceRows = [{ key: () => 'a', title: 'Old', detail: '' }]; page.syncRows(); const row = page.rowBindings[0];
  page.model.resourceRows = [{ key: () => 'a', title: 'Renamed', detail: 'Updated' }]; page.syncRows();
  assert.equal(page.rowBindings[0], row); assert.equal(row.value.detail, 'Updated');
  page.search = 'missing'; page.syncRows(); assert.deepEqual(page.rowBindings, []);
});
