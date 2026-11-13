import test from 'node:test';
import assert from 'node:assert/strict';
import {registerHooks, stripTypeScriptTypes} from 'node:module';
import {readFileSync} from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      return {url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true};
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8')
        .replace(/@ObservedV2\s*/g, '')
        .replace(/@Trace\s+/g, '');
      return {format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true};
    }
    return next(url, context);
  }
});

const {AssistantFeedModel} = await import('../entry/src/main/ets/models/AssistantFeed.ets');
const {ResourceRow} = await import('../entry/src/main/ets/models/Wire.ets');

const conversationId = 'conversation-1';
const managerId = 'work';
const timestamp = '2026-11-11T10:00:00.000Z';

function conversation(id = conversationId, manager = managerId) {
  const row = new ResourceRow(id, 'Conversation', 'conversation');
  row.manager = manager;
  return row;
}

function feedItem(id, kind, options = {}) {
  return {
    id,
    kind,
    ref: options.ref || '',
    conversation: options.conversation || conversationId,
    manager: options.manager || managerId,
    at: options.at || timestamp,
    body: options.body || {}
  };
}

function fixture(handler, options = {}) {
  const selected = options.row || conversation();
  const state = {
    epoch: 7,
    manager: options.manager || managerId,
    selected,
    blocked: new Set(),
    repository: undefined, handlers: []
  };
  const repository = {
    owner: state.manager,
    calls: [],
    async call(path, method = 'GET', body = {}, op = '') {
      const call = {path, method, body, op, repository: this};
      this.calls.push(call);
      return handler(call, state);
    }
  };
  state.repository = repository;
  const model = {
    profile: {manager: state.manager},
    sessions: options.sessions || [],
    machines: [],
    scope() { return 'hub:' + state.manager; },
    connectionGeneration() { return state.epoch; },
    owns(generation, scope) { return generation === state.epoch && scope === this.scope(); },
    target(row = state.selected) { return JSON.stringify([this.scope(), row.kind, row.machine, row.id]); },
    repo() { return state.repository; },
    operationBlocked(path) { return state.blocked.has(path); },
    machineName(id) { return id; },
    observeEvents(handler) { state.handlers.push(handler); return () => { state.handlers = state.handlers.filter(value => value !== handler); }; }
  };
  return {model, repository, state, row: selected};
}

function plan(id = 'plan-1', options = {}) {
  return {
    id,
    conversation: conversationId,
    state: options.state || 'open',
    created_at: timestamp,
    updated_at: timestamp,
    tasks: options.tasks || [{title: 'Read source', prompt: 'Inspect files', machine: 'machine-1', cwd: '/repo', permission: 'plan'}]
  };
}

function feedPage(items = [], plans = {}, extra = {}) {
  return {items, plans, task_cards: [], views: {}, view_actions: {}, view_blocks: [], next_cursor: '', ...extra};
}

async function opened(handler, options = {}) {
  const f = fixture(handler, options);
  const feed = new AssistantFeedModel();
  await feed.open(f.model, f.row);
  return {...f, feed};
}

test('the feed and plan start retain the conversation owner and captured repository', async () => {
  let completeStart;
  const repoA = fixture((call) => {
    if (call.path.includes('/feed')) return feedPage([feedItem('feed-plan', 'plan', {ref: 'plan-1'})], {'plan-1': plan()});
    if (call.path.includes('/start')) return new Promise((resolve) => { completeStart = resolve; });
    return {};
  });
  const repoB = fixture(async () => ({}));
  const state = repoA.state;
  state.repository = repoA.repository;
  const feed = new AssistantFeedModel();
  await feed.open(repoA.model, repoA.row);
  assert.match(repoA.repository.calls[0].path, /feed\?paginate=1&manager=work$/);
  const started = feed.startPlan('plan-1');
  const startCall = repoA.repository.calls.find((call) => call.path.endsWith('/task-plans/plan-1/start'));
  assert.ok(startCall);
  assert.equal(startCall.repository.owner, 'work');

  state.epoch++;
  state.selected = conversation('other-conversation');
  state.repository = repoB.repository;
  completeStart({plan: {...plan(), state: 'started', tasks: [{...plan().tasks[0], todo_id: 'todo-1'}]}});
  assert.equal(await started, false);
  assert.equal(feed.planState('plan-1'), 'open', 'a reply for the old target is ignored');
  assert.equal(repoB.repository.calls.length, 0, 'a replacement repository is never used for the old write');
});

test('feed items and todos from another manager or conversation never enter the scoped projection', async () => {
  const {feed} = await opened(async (call) => {
    if (call.path.includes('/feed')) return feedPage([
      feedItem('wrong-manager', 'task', {ref: 'todo-1', manager: 'personal'}),
      feedItem('owner-task', 'task', {ref: 'todo-2'}),
      feedItem('wrong-conversation', 'plan', {ref: 'plan-foreign', conversation: 'other-conversation'})
    ], {'plan-foreign': {...plan('plan-foreign'), conversation: 'other-conversation'}});
    if (call.path.startsWith('/api/v1/todos')) return {todos: [
      {id: 'todo-1', manager: 'personal', origin: conversationId, title: 'Foreign manager'},
      {id: 'todo-2', manager: managerId, origin: 'other-conversation', title: 'Foreign conversation'}
    ]};
    return {};
  });

  assert.deepEqual(feed.items.map((item) => item.id), ['owner-task']);
  assert.deepEqual(Object.keys(feed.plans), []);
  assert.deepEqual(feed.todos, []);
  assert.equal(feed.openTodo('todo-1'), undefined);
  assert.equal(feed.openTodo('todo-2'), undefined);
});

test('an unknown message send stays pending through refresh and cannot send again', async () => {
  const message = feedItem('draft-card', 'message', {ref: 'inbox-item', body: {
    todo_id: 'todo-1', source: 'mail', sender: 'sender@example.test', chat: 'Subject', text: 'Question', draft: 'Approved reply'
  }});
  let replyCalls = 0;
  const {feed} = await opened(async (call) => {
    if (call.path.includes('/feed')) return feedPage([message]);
    if (call.path.includes('/inbox/inbox-item/reply')) {
      replyCalls++;
      const error = new Error('result_unknown');
      error.status = 502;
      error.resultUnknown = true;
      throw error;
    }
    return {};
  });

  assert.equal(feed.messageCanSend(feed.items[0]), true);
  assert.equal(await feed.sendMessage('draft-card', 'Approved reply'), false);
  assert.equal(feed.messageState(feed.items[0]), 'pending');
  await feed.refresh();
  assert.equal(feed.messageState(feed.items[0]), 'pending');
  assert.equal(feed.messageCanSend(feed.items[0]), false);
  assert.equal(await feed.sendMessage('draft-card', 'Approved reply'), false);
  assert.equal(replyCalls, 1, 'verification never causes an automatic resend');
});

test('an unknown plan start is reconciled and blocked from a second start', async () => {
  const openPlan = plan('plan-unknown');
  let startCalls = 0;
  const {feed} = await opened(async (call, state) => {
    if (call.path.includes('/feed')) return feedPage([feedItem('feed-plan', 'plan', {ref: openPlan.id})], {[openPlan.id]: openPlan});
    if (call.path === '/api/v1/task-plans/plan-unknown/start') {
      startCalls++;
      state.blocked.add(call.path);
      const error = new Error('result_unknown');
      error.status = 502;
      error.resultUnknown = true;
      throw error;
    }
    if (call.path === '/api/v1/task-plans/plan-unknown') return {plan: openPlan};
    return {};
  });

  assert.equal(await feed.startPlan(openPlan.id), false);
  assert.equal(feed.planState(openPlan.id), 'open', 'the unknown response is not presented as success');
  assert.equal(feed.error, 'result_unknown');
  assert.equal(feed.planCanStart(openPlan.id), false);
  assert.equal(await feed.startPlan(openPlan.id), false);
  assert.equal(startCalls, 1, 'an unknown start is never submitted again automatically');
});

test('an already started plan has no Start action', async () => {
  const startedPlan = plan('plan-started', {state: 'started', tasks: [{
    title: 'Read source', prompt: 'Inspect files', machine: 'machine-1', cwd: '/repo', permission: 'plan', todo_id: 'todo-1'
  }]});
  const {feed} = await opened(async (call) => {
    if (call.path.includes('/feed')) return feedPage([feedItem('feed-plan', 'plan', {ref: startedPlan.id})], {[startedPlan.id]: startedPlan});
    if (call.path.startsWith('/api/v1/todos')) return {todos: [{id: 'todo-1', manager: managerId, origin: conversationId, title: 'Read source'}]};
    return {};
  });

  assert.equal(feed.planState(startedPlan.id), 'started');
  assert.equal(feed.planCanStart(startedPlan.id), false);
  assert.equal(feed.planCanDismiss(startedPlan.id), false);
});

test('editing a read-only task keeps plan permission and explicit Start sends no escalation', async () => {
  const readonlyPlan = plan('plan-readonly', {tasks: [{
    title: 'Read source', prompt: 'Inspect files', machine: 'machine-1', cwd: '/repo', permission: 'plan', readonly: true
  }]});
  let startBody, state = 'open';
  const {feed} = await opened(async (call) => {
    if (call.path.includes('/feed')) return feedPage([feedItem('feed-plan', 'plan', {ref: readonlyPlan.id})], {
      [readonlyPlan.id]: {...readonlyPlan, state}
    });
    if (call.path.endsWith('/task-plans/plan-readonly/start')) {
      startBody = call.body;
      state = 'started';
      return {plan: {...readonlyPlan, state}};
    }
    return {};
  });

  assert.equal(feed.beginEditPlan(readonlyPlan.id), true);
  feed.updateEditTask(0, 'permission', 'bypassPermissions');
  assert.equal(feed.editTasks[0].permission, 'plan');
  assert.equal(await feed.startEditedPlan(), true);
  assert.ok(Array.isArray(startBody.tasks));
  assert.equal(Object.hasOwn(startBody.tasks[0], 'permission'), false);
  assert.equal(feed.planState(readonlyPlan.id), 'started');
});

test('writable plan edits can submit only the server-required permission mode', async () => {
  const writablePlan = plan('plan-writable', {tasks: [{
    title: 'Change source', prompt: 'Update implementation', machine: 'machine-1', cwd: '/repo', permission: 'default', readonly: false
  }]});
  let startBody, state = 'open';
  const {feed} = await opened(async (call) => {
    if (call.path.includes('/feed')) return feedPage([feedItem('feed-plan', 'plan', {ref: writablePlan.id})], {
      [writablePlan.id]: {...writablePlan, state}
    });
    if (call.path.endsWith('/task-plans/plan-writable/start')) {
      startBody = call.body;
      state = 'started';
      return {plan: {...writablePlan, state}};
    }
    return {};
  });

  assert.equal(feed.beginEditPlan(writablePlan.id), true);
  feed.updateEditTask(0, 'permission', 'auto');
  assert.equal(feed.editTasks[0].permission, 'default');
  assert.equal(await feed.startEditedPlan(), false);
  assert.equal(startBody, undefined);
  feed.updateEditTask(0, 'permission', 'bypassPermissions');
  assert.equal(await feed.startEditedPlan(), true);
  assert.equal(startBody.tasks[0].permission, 'bypassPermissions');
});

test('paged refresh advances the older cursor while merging each feed identity once', async () => {
  const cursors = new Map([
    ['cursor-1', feedPage([
      feedItem('feed-1', 'notice', {body: {text: 'Updated card'}}),
      feedItem('feed-2', 'notice')
    ], {}, {next_cursor: 'cursor-2'})],
    ['cursor-2', feedPage([feedItem('feed-3', 'notice')], {}, {next_cursor: 'cursor-3'})],
    ['cursor-3', feedPage([feedItem('feed-4', 'notice')], {}, {next_cursor: 'cursor-4'})],
    ['cursor-4', feedPage([feedItem('feed-5', 'notice')])]
  ]);
  const {feed, repository} = await opened(async (call) => {
    if (!call.path.includes('/feed')) return {};
    if (call.path.includes('cursor=')) return cursors.get(new URL('https://hub.test' + call.path).searchParams.get('cursor'));
    return feedPage([feedItem('feed-1', 'notice', {body: {text: 'Original card'}})], {}, {next_cursor: 'cursor-1'});
  });

  assert.deepEqual(feed.items.map((item) => item.id), ['feed-1', 'feed-2', 'feed-3', 'feed-4']);
  assert.equal(feed.items.find((item) => item.id === 'feed-1').body.text, 'Updated card');
  assert.equal(feed.nextCursor, 'cursor-4');
  await feed.loadMore();
  assert.deepEqual(feed.items.map((item) => item.id), ['feed-1', 'feed-2', 'feed-3', 'feed-4', 'feed-5']);
  assert.equal(feed.nextCursor, '');
  assert.ok(repository.calls.some((call) => call.path.includes('cursor=cursor-4')));
});



test('live assistant card events update without transcript messages and cannot be downgraded by a raced feed',async()=>{
 const f=fixture(async()=>feedPage()),feed=new AssistantFeedModel();await feed.open(f.model,f.row);
 const emit=frame=>f.state.handlers.forEach(handler=>handler(JSON.stringify({manager:managerId,conversation:conversationId,...frame})));
 emit({type:'assistant_view',view:{id:'view_live',conversation:conversationId,version:2,view:{title:'Current',sections:[]}}});
 emit({type:'assistant_view_block',key:'item#0/0'});
 emit({type:'assistant_view_action',action:{key:'view_live',action:'choice',client_msg_id:'reply',status:'sent',revision:2,version:2,values:{a:'b'}}});
 await feed.refresh();assert.equal(feed.views.view_live.version,2);assert.equal(feed.viewActions.view_live.status,'sent');assert.ok(feed.viewBlocks.includes('item#0/0'));
 emit({type:'assistant_view',view:{id:'view_live',conversation:conversationId,version:1,view:{title:'Old',sections:[]}}});
 emit({type:'assistant_view_action',action:{key:'view_live',action:'choice',client_msg_id:'reply',status:'pending',revision:1,version:2,values:{a:'b'}}});
 emit({type:'assistant_view_action',action:{key:'view_live',action:'another-choice',client_msg_id:'other',status:'sent',revision:9,version:2,values:{a:'b'}}});
 assert.equal(feed.views.view_live.version,2);assert.equal(feed.viewActions.view_live.status,'sent');assert.equal(feed.viewActions.view_live.client_msg_id,'reply');
 const callback=f.state.handlers[0];feed.close();assert.equal(f.state.handlers.length,0);callback(JSON.stringify({type:'assistant_view_block',manager:managerId,conversation:conversationId,key:'late/0'}));assert.deepEqual(feed.viewBlocks,[]);
});

test('assistant card events retain their exact manager and conversation scope',async()=>{
 const f=fixture(async()=>feedPage()),feed=new AssistantFeedModel();await feed.open(f.model,f.row);
 for(const frame of [{manager:'another',conversation:conversationId},{manager:managerId,conversation:'another'}]) f.state.handlers[0](JSON.stringify({type:'assistant_view_block',key:'foreign/0',...frame}));
 assert.deepEqual(feed.viewBlocks,[]);feed.close();
});
