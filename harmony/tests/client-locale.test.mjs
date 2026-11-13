import test from 'node:test';
import assert from 'node:assert/strict';
import {clientLocale,initializeClientLanguage,setClientLanguage} from '../entry/src/main/ets/core/ClientLocale.ts';
test('wire locale follows the committed UI selection independently of the system and resolves system fallback',()=>{
 for(const [choice,expected] of [['en-US','en'],['zh-CN','zh-Hans'],['ja-JP','ja']]){setClientLanguage(choice);for(const system of ['zh-CN','en-US','ja-JP','ko-KR'])assert.equal(clientLocale(system),expected);}
 setClientLanguage('default');for(const [system,expected]of [['zh-Hant-TW','zh-Hans'],['ja-JP','ja'],['en-GB','en'],['ko-KR','en']])assert.equal(clientLocale(system),expected);
});
test('Ability recreation cannot apply a pending cold-start language to this launch',()=>{
 initializeClientLanguage('en-US');assert.equal(clientLocale('zh-CN'),'en');initializeClientLanguage('default');assert.equal(clientLocale('zh-CN'),'en');setClientLanguage('default');
});
