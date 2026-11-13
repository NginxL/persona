import test from 'node:test';
import assert from 'node:assert/strict';
import {resolveManagerScope} from '../entry/src/main/ets/core/ScopePolicy.ts';
test('implicit and explicit default picks use one cache and write owner',()=>{
 assert.equal(resolveManagerScope('', 'personal', ['personal','work']), 'personal');
 assert.equal(resolveManagerScope('personal', 'personal', ['personal','work']), 'personal');
});
test('a changed server default does not redirect an explicit assistant',()=>{
 assert.equal(resolveManagerScope('personal','work',['personal','work']),'personal');
 assert.equal(resolveManagerScope('removed','work',['personal','work']),'work');
});
test('an incomplete roster cannot admit manager-owned work',()=>{
 assert.throws(()=>resolveManagerScope('', '', ['personal']));
 assert.throws(()=>resolveManagerScope('work', 'missing', ['work']));
});
