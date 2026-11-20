// Production adapter vs the real Rust core. Host evidence only: never a NAPI, HAP or physical-device claim.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import './agent-sort-loader.mjs';
import { HostCore, helperPath } from './host.mjs';

const {CoreAgentListAdapter, coreAgentSortInput} = await import('../../entry/src/main/ets/core/AgentListAdapter.ts');
const {ResourceRow} = await import('../../entry/src/main/ets/models/Wire.ets');
const fixtureDir = new URL('../../../spec/fixtures/sort/', import.meta.url);
const documents = readdirSync(fixtureDir).filter((f) => f.endsWith('.json')).sort().map((file) => ({
  file, ...JSON.parse(readFileSync(new URL(file, fixtureDir), 'utf8')),
}));
const cases = documents.filter((doc) => doc.operation === 'agents_list').flatMap((doc) => doc.cases);
assert.equal(cases.length, 61, 'the exact checked-in projection corpus');
assert.equal(new Set(cases.map((c) => c.name)).size, 61, 'unique case names');
const prefs = {pinsSpaces: [], pinsSessions: [], mutesSpaces: [], mutesSessions: [], mutesAllowed: []};

function nativeInput(c) {
  const input = c.input;
  const rows = input.sessions.map((raw) => ({id: raw.session_id, raw: structuredClone(raw), source: ResourceRow.from(raw, 'session')}));
  return {
    rows,
    history: false, reading: false, machines: input.machines,
    grouping: input.settings.grouping,
    quick: input.settings.quick,
    machineFilter: input.settings.machine_filter,
    query: input.settings.query,
    kept: input.settings.kept,
    coreSettings: {...input.settings},
    preferencesFor: () => ({...prefs, pinsSpaces: input.pins.spaces, pinsSessions: input.pins.sessions,
      mutesSpaces: input.mutes.spaces, mutesSessions: input.mutes.sessions, mutesAllowed: input.mutes.allowed}),
    machineIdentity: (r) => r.raw.machine_id,
    machineName: (r) => input.machines.find((m) => m.id === r.raw.machine_id && (m.group ?? '') === (r.raw.app_hub_group ?? ''))?.name ?? r.raw.machine_id,
  };
}

test('the production core adapter matches every fixture through real NAPI-shaped JSON', async () => {
  const host = new HostCore(helperPath);
  const adapter = new CoreAgentListAdapter();
  try {
    for (const c of cases) {
      const input = nativeInput(c);
      const coreInput = coreAgentSortInput(input);
      const core = await host.sort(coreInput);
      globalThis.coreProjectSort = (raw) => {
        assert.deepEqual(JSON.parse(raw), coreInput, `${c.name}: exact production input`);
        return JSON.stringify(core);
      };
      assert.deepEqual(core, c.expected, `${c.name}: real core remains pinned to the unchanged fixture`);
      assert.deepEqual(adapter.project(input), core, `${c.name}: production adapter returns the core projection`);
    }
    console.log(`  production core adapter: ${cases.length}/${cases.length} equal`);
  } finally { await host.end(); }
});

test('production adapter handles invalid input and has no release fallback', async () => {
  const host = new HostCore(helperPath);
  try {
    const adapter = new CoreAgentListAdapter();
    globalThis.coreProjectSort = () => '';
    await assert.rejects(() => host.sort({private: 'value must not be repeated'}), /sort/);
    assert.throws(() => adapter.project(nativeInput(cases[0])), /invalid_core_projection|Unexpected/);
  } finally { await host.end(); }
});
