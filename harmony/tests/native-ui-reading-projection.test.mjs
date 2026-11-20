import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { stripTypeScriptTypes } from 'node:module';
import { projectToolTurn } from '../entry/src/main/ets/core/Domain.ts';
import { stepsToItems } from '../../integrations/antigravity/lib/map.js';

const source = readFileSync(new URL('../entry/src/main/ets/models/ClientModel.ets', import.meta.url), 'utf8');

function method(name) {
  const match = new RegExp('  private ' + name + '\\(').exec(source);
  assert.ok(match, name);
  const start = match.index, body = source.indexOf('{', start);
  let depth = 1, quote = '', escape = false, index = body + 1;
  for (; index < source.length && depth; index += 1) {
    const character = source[index];
    if (quote) {
      if (escape) escape = false;
      else if (character === '\\') escape = true;
      else if (character === quote) quote = '';
    } else if (character === '"' || character === "'" || character === '`') quote = character;
    else if (character === '{') depth += 1;
    else if (character === '}') depth -= 1;
  }
  return source.slice(start, index);
}

class ChatMessage {
  constructor(id, role, text = '') { Object.assign(this, { id, role, text }); }
}

const ProjectionHarness = new Function(
  'projectToolTurn', 'ChatMessage', 'messageDisplay', 'parseMessageContent',
  'return ' + stripTypeScriptTypes('class ProjectionHarness {' + method('projectMessages') + '}'),
)(
  projectToolTurn,
  ChatMessage,
  text => ({ text, sources: [] }),
  text => ({ text, images: [], files: [] }),
);

const configs = [
  { reading_case: true },
  { reading_case: true, reading_prepend: true },
  { reading_case: true, reading_prepend: true, reading_append: true },
];
const fixturePath = fileURLToPath(new URL('./native-ui-fixture.py', import.meta.url));
const python = [
  'import importlib.util, json, sys',
  "spec = importlib.util.spec_from_file_location('native_ui_fixture', sys.argv[1])",
  'fixture = importlib.util.module_from_spec(spec)',
  'spec.loader.exec_module(fixture)',
  'controls = json.loads(sys.argv[2])',
  'print(json.dumps([fixture.transcript(control=control)["items"] for control in controls]))',
].join('; ');
const transcripts = JSON.parse(execFileSync(
  'python3', ['-c', python, fixturePath, JSON.stringify(configs)], { encoding: 'utf8' },
));

function project(items) {
  const model = Object.assign(new ProjectionHarness(), {
    target: () => 'fixture-reading-target',
    itemTime: () => 0,
    workText: step => step.item?.text ?? '',
  });
  return model.projectMessages(items, false);
}

test('native transcript projection keeps a middle stable ID across prepend and append phases', () => {
  const middleId = 'visible-fixture-reading-50#0';
  const initial = project(transcripts[0]);
  assert.equal(initial.length, 20);
  assert.ok(initial.some(message => message.id === middleId));

  const prepended = project(transcripts[1]);
  assert.equal(prepended.length, 40);
  assert.equal(prepended[31].id, middleId);

  const appended = project(transcripts[2]);
  assert.equal(appended.length, 45);
  assert.equal(appended[31].id, middleId);
  assert.equal(appended[31].itemId, 'fixture-reading-51#0');
});

test('an Antigravity wake keeps the finished reply and its attachment in the conversation', () => {
  const raw = readFileSync(new URL('../../integrations/antigravity/test/fixtures/autonomous-wake.jsonl', import.meta.url), 'utf8');
  const steps = raw.trim().split('\n').map(line => JSON.parse(line));
  const prior = project(stepsToItems(steps.slice(0, 3)).items);
  const history = stepsToItems(steps).items;
  const after = project(history);
  const replies = after.filter(row => row.role === 'assistant');
  assert.deepEqual(replies.map(row => row.text), [steps[2].content, steps[5].content, steps[8].content]);
  assert.deepEqual(replies[0], prior.find(row => row.role === 'assistant'));
  assert.match(replies[0].text, /file: \/synthetic\/chart\.png/);
  assert.equal(after.filter(row => row.role === 'user').length, 2);
  assert.ok(after.every(row => !row.work?.includes(steps[2].content)));
});
