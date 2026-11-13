import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('.mjs') && specifier.endsWith('.ts'))
      return { url: new URL(specifier, context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.endsWith('.ts')) {
      return { format: 'module', source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8')), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { ConversationRevisionLease, restoreAcknowledgedSessionDraft } =
  await import('../entry/src/main/ets/core/ConversationRevisionLease.ts');

function context(overrides = {}) {
  return {
    model: {}, active: true, connected: true, demo: false, generation: 7, scope: 'hub:worker',
    target: '["hub:worker","session","m1","s1"]', visibilityRevision: 12,
    itemId: 'entry-1#0', fingerprint: 'raw-fingerprint-1',
    messageOwnerTarget: '["hub:worker","session","m1","s1"]', currentSession: true,
    ...overrides
  };
}

function leaseFor(value) {
  return new ConversationRevisionLease(value.model, value.generation, value.scope, value.target, value.visibilityRevision,
    value.itemId, value.fingerprint, value.messageOwnerTarget);
}

function matches(lease, value) {
  return lease.matches(value.model, value.active, value.connected, value.demo, value.generation, value.scope, value.target,
    value.visibilityRevision, value.itemId, value.fingerprint, value.messageOwnerTarget, value.currentSession);
}

test('menu-open lease is frozen and rejects transcript edits and messages owned by another target', () => {
  const opened = context(), lease = leaseFor(opened);
  assert.equal(Object.isFrozen(lease), true);
  assert.throws(() => { lease.generation = 99; }, TypeError);
  assert.equal(matches(lease, opened), true);
  assert.equal(matches(lease, ({ ...opened,  fingerprint: 'raw-fingerprint-2'  })), false);
  assert.equal(matches(lease, ({ ...opened,  messageOwnerTarget: '["hub:worker","session","m2","s2"]'  })), false);
  assert.equal(matches(lease, ({ ...opened,  target: '["hub:worker","session","m2","s2"]', messageOwnerTarget: '["hub:worker","session","m2","s2"]'  })), false);
  assert.equal(matches(lease, ({ ...opened,  model: {}  })), false);
});

test('an old confirmation stays invalid after reconnect and hide then show of the same target', () => {
  const opened = context(), lease = leaseFor(opened);
  assert.equal(matches(lease, ({ ...opened,  generation: opened.generation + 1  })), false);
  assert.equal(matches(lease, ({ ...opened,  active: false, visibilityRevision: opened.visibilityRevision + 1  })), false);
  assert.equal(matches(lease, ({ ...opened,  active: true, visibilityRevision: opened.visibilityRevision + 2  })), false);
});

test('a late fork acknowledgement cannot navigate after the owning conversation reopened', async () => {
  const opened = context(), lease = leaseFor(opened), ack = Promise.resolve({ id: 'forked-session' });
  const reopened = ({ ...opened,  generation: opened.generation + 1, visibilityRevision: opened.visibilityRevision + 2  });
  const navigated = await ack.then((row) => matches(lease, reopened) ? row : undefined);
  assert.equal(navigated, undefined);
});

test('draft text and revision are both rechecked immediately before restore', async () => {
  let writes = 0, updates = 0;
  const model = {
    draft: 'newer user edit', draftRevision: 'draft-rev-2',
    updateDraft(text) { updates++; this.draft = text; this.draftRevision = 'restored-rev'; },
    async persistDraft() { writes++; }
  };
  const outcome = { inputCleared: true, prompt: 'rewound prompt', capturedDraft: '', capturedDraftRevision: 'draft-rev-1' };
  const restored = await restoreAcknowledgedSessionDraft(model, outcome, () => true, () => {});
  assert.equal(restored, false);
  assert.equal(updates, 0);
  assert.equal(writes, 0);
  assert.equal(model.draft, 'newer user edit');
});

test('an acknowledged rewind restores and immediately persists the captured draft', async () => {
  let writes = 0;
  const model = {
    draft: '', draftRevision: 'draft-rev-1',
    updateDraft(text) { this.draft = text; this.draftRevision = 'restored-rev'; },
    async persistDraft() { writes++; }
  };
  const outcome = { inputCleared: true, prompt: 'rewound prompt', capturedDraft: '', capturedDraftRevision: 'draft-rev-1' };
  const restored = await restoreAcknowledgedSessionDraft(model, outcome, () => true, () => {});
  assert.equal(restored, true);
  assert.equal(model.draft, 'rewound prompt');
  assert.equal(writes, 1);
});

test('a rejected draft update is not reported as a successful restore or persisted', async () => {
  let writes = 0;
  const model = {
    draft: '', draftRevision: 'draft-rev-1',
    updateDraft() {},
    async persistDraft() { writes++; }
  };
  const outcome = { inputCleared: true, prompt: 'rewound prompt', capturedDraft: '', capturedDraftRevision: 'draft-rev-1' };
  const restored = await restoreAcknowledgedSessionDraft(model, outcome, () => true, () => {});
  assert.equal(restored, false);
  assert.equal(writes, 0);
});

test('draft-cache failure is reported separately while the rewind acknowledgement remains done', async () => {
  const model = {
    draft: '', draftRevision: 'draft-rev-1',
    updateDraft(text) { this.draft = text; this.draftRevision = 'restored-rev'; },
    async persistDraft() { throw new Error('storage failed'); }
  };
  const outcome = { state: 'done', inputCleared: true, prompt: 'rewound prompt', capturedDraft: '', capturedDraftRevision: 'draft-rev-1' };
  let cacheError = '';
  const restored = await restoreAcknowledgedSessionDraft(model, outcome, () => true, () => { cacheError = 'cache_unavailable'; });
  assert.equal(restored, false);
  assert.equal(outcome.state, 'done');
  assert.equal(cacheError, 'cache_unavailable');
});


const nativeSource = readFileSync(new URL('../entry/src/main/ets/components/Conversation.ets', import.meta.url), 'utf8');
function nativeMethod(name) {
 const marker = nativeSource.includes('  private async '+name+'(')?'  private async '+name+'(':'  private '+name+'(';
 const start=nativeSource.indexOf(marker);assert.ok(start>=0,name);const body=nativeSource.indexOf('{',start);let depth=1,quote='',escape=false,end=body+1;
 for(;end<nativeSource.length&&depth;end++){const ch=nativeSource[end];if(quote){if(escape)escape=false;else if(ch==='\\')escape=true;else if(ch===quote)quote='';}else if(ch==='"'||ch==="'"||ch==='`')quote=ch;else if(ch==='{')depth++;else if(ch==='}')depth--;}
 assert.equal(depth,0);return nativeSource.slice(start,end);
}
const NativeConversation = new Function('restoreAcknowledgedSessionDraft','return '+stripTypeScriptTypes('class NativeConversation {'+['revisionOwnerCurrent','rewindFinished','restoreRevisionDraft','finishRewindDraftConfirmation'].map(nativeMethod).join('\n')+'}'))(restoreAcknowledgedSessionDraft);
for(const change of ['hydrated-target','rewound-transcript'])test('native rewind ACK restores durably after '+change+' arrives first',async()=>{
 const opened=context(),model=opened.model;let saves=0,loads=0;
 Object.assign(model,{connected:true,demo:false,draft:'',draftRevision:'draft-rev-1',selected:{},connectionGeneration:()=>opened.generation,scope:()=>opened.scope,target:()=>opened.target,owns:(g,s)=>g===opened.generation&&s===opened.scope,updateDraft(text){this.draft=text;this.draftRevision='restored';},async persistDraft(){saves++;},async loadChat(){loads++;}});
 const page=new NativeConversation();page.model=model;page.active=true;page.revisionVisibilityRevision=opened.visibilityRevision;page.revisions={isCurrent:()=>true,fingerprint:()=>change==='hydrated-target'?'new-hydrated-fingerprint':''};
 page.revisionLeaseCurrent=()=>false;
 const lease=leaseFor(opened),message={itemId:opened.itemId,ownerTarget:opened.messageOwnerTarget};
 await page.rewindFinished({state:'done',inputCleared:true,canFillDraft:true,prompt:'Rewound prompt',capturedDraft:'',capturedDraftRevision:'draft-rev-1'},lease,message);
 assert.equal(page.revisionResult,'rewind_chat_done');assert.equal(model.draft,'Rewound prompt');assert.equal(saves,1);assert.equal(loads,1);
});
