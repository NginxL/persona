import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const modes=['default','acceptEdits','plan','auto','bypassPermissions'];
const locales={en:['base','en_US'],'zh-Hans':['zh_CN'],ja:['ja_JP']};
// Only permission-specific standalone names. Generic IDs also name effort options, and generic
// "Use default" describes unset behavior; a planned task's actual default mode is still checked.
const keys={
  default:['assistant_feed_permission_default'],
  acceptEdits:['assistant_feed_permission_acceptEdits'],
  plan:['assistant_feed_permission_plan'],
  auto:['assistant_feed_permission_auto'],
  bypassPermissions:['assistant_feed_permission_bypassPermissions'],
};

function json(url){return JSON.parse(readFileSync(url,'utf8'));}

test('HarmonyOS standalone permission mode names match the shared table (#10363)',()=>{
  const fixture=json(new URL('../../spec/fixtures/permission-modes/names.json',import.meta.url));
  assert.deepEqual(Object.keys(fixture.names).sort(),[...modes].sort());
  const settings=readFileSync(new URL('../entry/src/main/ets/components/SettingsPage.ets',import.meta.url),'utf8');
  assert.ok(settings.includes('assistant_feed_permission_'),
    'the session permission picker must use permission-specific labels, not generic effort IDs');
  assert.ok(settings.includes('secondaryText: this.currentModeName(field)'),
    'the current permission summary must use the localized mode name');
  assert.ok(settings.includes('+ this.modeName(field, value),'),
    'the save confirmation must name the draft mode, not its raw ID');
  assert.ok(settings.includes('return value ? this.modeName(field, value) : tr(\'automatic\')'),
    'an unset value keeps its previous wording');
  assert.ok(settings.includes(': value;'), 'an unknown permission ID stays raw');
  for(const [language,directories] of Object.entries(locales)){
    for(const directory of directories){
      const catalog=json(new URL(`../entry/src/main/resources/${directory}/element/string.json`,import.meta.url));
      const strings=new Map(catalog.string.map(item=>[item.name,item.value]));
      for(const mode of modes){
        for(const key of keys[mode]){
          assert.ok(strings.has(key),`${directory} ${key} is missing`);
          assert.equal(strings.get(key),fixture.names[mode][language],`${directory} ${key}`);
        }
      }
    }
  }
});
