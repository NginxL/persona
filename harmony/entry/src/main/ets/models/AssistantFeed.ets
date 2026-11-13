import { Wire, ResourceRow } from './Wire';
import type { ClientModel } from './ClientModel';
import type { HubRepository } from './HubRepository';

function segment(value: string): string { return encodeURIComponent(value); }

function textField(value: Object | undefined, key: string, fallback: string = ''): string {
  return Wire.text(Wire.record(value), key, fallback);
}

function hasId(values: string[], id: string): boolean { return values.includes(id); }

function updated(values: string[], id: string, add: boolean): string[] {
  const next = values.filter((value: string) => value !== id);
  if (add && id) next.push(id);
  return next;
}

function timestamp(value: string): number {
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

function draftPlaceholder(value: string): string {
  const match = /【[^】\n]*】|\{\{[^}\n]*\}\}/.exec(value);
  return match ? match[0] : '';
}

/** Editable values kept separately from the server's immutable plan until the owner presses Start. */
@ObservedV2
export class PlanEditTask {
  @Trace title: string = '';
  @Trace prompt: string = '';
  @Trace machine: string = '';
  @Trace cwd: string = '';
  @Trace permission: string = '';
  @Trace readonly: boolean = false;

  constructor(source: Record<string, Object> = {}) {
    this.title = Wire.text(source, 'title');
    this.prompt = Wire.text(source, 'prompt');
    this.machine = Wire.text(source, 'machine');
    this.cwd = Wire.text(source, 'cwd');
    this.permission = Wire.text(source, 'permission', 'default');
    this.readonly = Wire.bool(source, 'readonly');
    if (this.readonly) this.permission = 'plan';
  }

  value(key: string): string {
    if (key === 'title') return this.title;
    if (key === 'prompt') return this.prompt;
    if (key === 'machine') return this.machine;
    if (key === 'cwd') return this.cwd;
    if (key === 'permission') return this.permission;
    return '';
  }
}

/** Scoped, in-memory projection of one assistant conversation's real Hub feed. */
@ObservedV2
export class AssistantFeedModel {
  @Trace loading: boolean = false;
  @Trace loadingMore: boolean = false;
  @Trace error: string = '';
  @Trace todoError: string = '';
  @Trace conversationId: string = '';
  @Trace owner: string = '';
  @Trace items: Record<string, Object>[] = [];
  @Trace plans: Record<string, Object> = {};
  @Trace taskCards: Record<string, Object>[] = [];
  @Trace todos: Record<string, Object>[] = [];
  @Trace views: Record<string, Object> = {};
  @Trace viewActions: Record<string, Object> = {};
  @Trace viewBlocks: string[] = [];
  @Trace nextCursor: string = '';
  @Trace hasLoaded: boolean = false;
  @Trace editing: boolean = false;
  @Trace editingPlanId: string = '';
  @Trace editTasks: PlanEditTask[] = [];
  @Trace rememberPlans: string[] = [];
  @Trace busyKeys: string[] = [];
  @Trace uncertainKeys: string[] = [];
  @Trace pendingMessageIds: string[] = [];
  @Trace messageDrafts: Record<string, Object> = {};
  private generation: number = 0;
  private clientGeneration: number = -1;
  private scopeKey: string = '';
  private targetKey: string = '';
  private client: ClientModel | undefined;
  private repository: HubRepository | undefined;
  private refreshPromise: Promise<void> | undefined;
  private refreshRun: number = -1;
  private offEvents: (() => void) | undefined;

  private current(run: number): boolean {
    const client = this.client;
    return !!client && run === this.generation && client.owns(this.clientGeneration, this.scopeKey) &&
      client.scope() === this.scopeKey && client.target() === this.targetKey;
  }

  private clearState(): void {
    this.loading = false; this.loadingMore = false; this.error = ''; this.todoError = '';
    this.conversationId = ''; this.owner = ''; this.items = []; this.plans = {}; this.taskCards = []; this.todos = [];
    this.views = {}; this.viewActions = {}; this.viewBlocks = []; this.nextCursor = ''; this.hasLoaded = false;
    this.editing = false; this.editingPlanId = ''; this.editTasks = []; this.rememberPlans = [];
    this.busyKeys = []; this.uncertainKeys = []; this.pendingMessageIds = []; this.messageDrafts = {};
  }

  /** Captures one conversation's generation, scope, selected identity and repository before starting any reads. */
  async open(model: ClientModel, row: ResourceRow): Promise<void> {
    this.close();
    const run = this.generation;
    if (row.kind !== 'conversation' || !row.id || model.target() !== model.target(row)) {
      this.error = 'request_updated';
      return;
    }
    const owner = row.manager || model.profile.manager;
    if (owner !== model.profile.manager) {
      this.error = 'request_updated';
      return;
    }
    let repository: HubRepository;
    try { repository = model.repo(); } catch (_) { this.error = 'offline'; return; }
    this.client = model;
    this.repository = repository;
    this.clientGeneration = model.connectionGeneration();
    this.scopeKey = model.scope();
    this.targetKey = model.target(row);
    this.conversationId = row.id;
    this.owner = owner;
    if (!this.current(run)) { this.error = 'request_updated'; return; }
    this.offEvents = model.observeEvents((raw: string): void => this.receiveEvent(raw, run));
    await this.refresh();
  }

  close(): void {
    this.offEvents?.(); this.offEvents = undefined;
    this.generation++;
    this.client = undefined; this.repository = undefined; this.refreshPromise = undefined; this.refreshRun = -1;
    this.clientGeneration = -1; this.scopeKey = ''; this.targetKey = '';
    this.clearState();
  }

  private mergeViews(views: Record<string, Object>): void {
    const next: Record<string, Object> = {};
    for (const key of Object.keys(this.views)) next[key] = this.views[key];
    for (const key of Object.keys(views)) {
      const incoming = Wire.record(views[key]), conversation = Wire.text(incoming, 'conversation'), version = incoming['version'];
      if (Wire.text(incoming, 'id') !== key || conversation && conversation !== this.conversationId ||
        typeof version !== 'number' || !Number.isSafeInteger(version) || version <= 0) continue;
      const previous = Wire.record(next[key]);
      if (!Object.keys(previous).length || Wire.number(previous, 'version') < version) next[key] = incoming;
    }
    this.views = next;
  }
  private mergeViewActions(actions: Record<string, Object>): void {
    const next: Record<string, Object> = {};
    for (const key of Object.keys(this.viewActions)) next[key] = this.viewActions[key];
    for (const key of Object.keys(actions)) {
      const incoming = Wire.record(actions[key]), previous = Wire.record(next[key]);
      if (Wire.text(incoming, 'key') !== key || !Wire.text(incoming, 'action') || !Wire.text(incoming, 'client_msg_id') || !Wire.text(incoming, 'status')) continue;
      if (Object.keys(previous).length) {
        const immutable = ['action', 'client_msg_id', 'page', 'version', 'text'];
        const previousValues = Wire.object(previous, 'values'), incomingValues = Wire.object(incoming, 'values');
        if (immutable.some((field: string) => JSON.stringify(previous[field] ?? '') !== JSON.stringify(incoming[field] ?? '')) ||
          (Wire.text(previous, 'kind') || 'action') !== (Wire.text(incoming, 'kind') || 'action') ||
          Object.keys(previousValues).length !== Object.keys(incomingValues).length || Object.keys(previousValues).some((name: string) => previousValues[name] !== incomingValues[name]) ||
          Wire.number(incoming, 'revision') <= Wire.number(previous, 'revision')) continue;
      }
      next[key] = incoming;
    }
    this.viewActions = next;
  }
  private mergeViewBlocks(keys: string[]): void {
    const next = this.viewBlocks.slice();
    for (const key of keys) if (key && key.length <= 512 && !/[\u0000-\u001f\u007f]/.test(key) && !next.includes(key)) next.push(key);
    this.viewBlocks = next;
  }
  private receiveEvent(raw: string, run: number): void {
    if (!this.current(run) || raw.length > 2 * 1024 * 1024) return;
    const frame = Wire.parse(raw);
    if (Wire.has(frame, 'manager') && Wire.text(frame, 'manager') !== this.owner || Wire.text(frame, 'conversation') !== this.conversationId) return;
    const type = Wire.text(frame, 'type');
    if (type === 'assistant_view') {
      const view = Wire.object(frame, 'view'), id = Wire.text(view, 'id'); if (id) this.mergeViews(Wire.payload([id], [view]));
    } else if (type === 'assistant_view_action') {
      const action = Wire.object(frame, 'action'), key = Wire.text(action, 'key'); if (key) this.mergeViewActions(Wire.payload([key], [action]));
    } else if (type === 'assistant_view_block') this.mergeViewBlocks([Wire.text(frame, 'key')]);
    else if (type === 'assistant_feed') {
      const item = Wire.object(frame, 'item');
      if (Wire.text(item, 'conversation', this.conversationId) !== this.conversationId || Wire.text(item, 'manager', this.owner) !== this.owner) return;
      this.mergeItems([item]);
      const view = Wire.object(Wire.object(item, 'body'), 'view'), id = Wire.text(view, 'id');
      if (id && Wire.text(item, 'ref') === id) this.mergeViews(Wire.payload([id], [view]));
      this.refresh().catch(() => undefined);
    }
  }

  private feedPath(cursor: string = '', since: string = ''): string {
    let path = '/api/v1/assistant/conversations/' + segment(this.conversationId) + '/feed?paginate=1';
    if (this.owner) path += '&manager=' + segment(this.owner);
    if (cursor) path += '&cursor=' + segment(cursor);
    else if (since) path += '&since=' + segment(since);
    return path;
  }

  private ownerPath(path: string): string { return this.owner ? path + '?manager=' + segment(this.owner) : path; }

  private mergeItems(incoming: Record<string, Object>[]): void {
    const byId: Record<string, Record<string, Object>> = {};
    this.items.forEach((item: Record<string, Object>) => {
      const id = Wire.text(item, 'id'); if (id) byId[id] = item;
    });
    incoming.forEach((item: Record<string, Object>) => {
      const id = Wire.text(item, 'id'); if (id) byId[id] = item;
    });
    this.items = Object.keys(byId).map((id: string) => byId[id]).sort((left: Record<string, Object>, right: Record<string, Object>) => {
      const order = timestamp(Wire.text(left, 'at')) - timestamp(Wire.text(right, 'at'));
      return order !== 0 ? order : Wire.text(left, 'id').localeCompare(Wire.text(right, 'id'));
    });
  }

  private mergePlans(incoming: Record<string, Object>): void {
    const next: Record<string, Object> = {};
    Object.keys(this.plans).forEach((id: string) => next[id] = this.plans[id]);
    Object.keys(incoming).forEach((id: string) => {
      const plan = Wire.record(incoming[id]);
      const conversation = Wire.text(plan, 'conversation');
      if (conversation && conversation !== this.conversationId) return;
      next[id] = plan;
    });
    this.plans = next;
  }

  private mergeTaskCards(incoming: Record<string, Object>[]): void {
    if (!incoming.length) return; // Empty/null projections are upsert pages, never a deletion snapshot.
    const byId: Record<string, Record<string, Object>> = {};
    this.taskCards.forEach((card: Record<string, Object>) => {
      const id = Wire.text(card, 'id'); if (id) byId[id] = card;
    });
    incoming.forEach((card: Record<string, Object>) => {
      const id = Wire.text(card, 'id');
      const planId = Wire.text(card, 'plan_id');
      if (planId) {
        const plan = Wire.record(this.plans[planId]);
        if (Wire.text(plan, 'conversation') && Wire.text(plan, 'conversation') !== this.conversationId) return;
      }
      if (id) byId[id] = card;
    });
    this.taskCards = Object.keys(byId).map((id: string) => byId[id]).sort((left: Record<string, Object>, right: Record<string, Object>) => {
      const order = timestamp(Wire.text(left, 'at')) - timestamp(Wire.text(right, 'at'));
      return order !== 0 ? order : Wire.text(left, 'id').localeCompare(Wire.text(right, 'id'));
    });
  }

  private mergePage(result: Record<string, Object>): void {
    const incomingItems = Wire.rows(result, 'items').filter((item: Record<string, Object>) => {
      const conversation = Wire.text(item, 'conversation');
      const manager = Wire.text(item, 'manager');
      return (!conversation || conversation === this.conversationId) && (!manager || manager === this.owner);
    });
    this.mergeItems(incomingItems);
    const plans = Wire.object(result, 'plans');
    const related: string[] = [];
    incomingItems.forEach((item: Record<string, Object>) => {
      const ref = Wire.text(item, 'ref');
      if (Wire.text(item, 'kind') === 'plan' && ref && !related.includes(ref)) related.push(ref);
    });
    const filteredPlans: Record<string, Object> = {};
    Object.keys(plans).forEach((id: string) => {
      if (hasId(related, id) || hasId(Object.keys(this.plans), id)) filteredPlans[id] = plans[id];
    });
    this.mergePlans(filteredPlans);
    if (Wire.has(result, 'task_cards') && Array.isArray(result['task_cards'])) this.mergeTaskCards(Wire.rows(result, 'task_cards'));
    this.mergeViews(Wire.object(result, 'views'));
    this.mergeViewActions(Wire.object(result, 'view_actions'));
    this.mergeViewBlocks(Wire.strings(result, 'view_blocks'));
    this.pendingMessageIds = this.pendingMessageIds.filter((id: string) => {
      const item = this.items.find((row: Record<string, Object>) => Wire.text(row, 'id') === id);
      const body = Wire.object(item || {}, 'body');
      return !(Wire.text(body, 'sent_at') || Wire.text(body, 'filed_at') || Wire.text(body, 'dismissed_at'));
    });
  }

  private async readPage(run: number, cursor: string = '', since: string = ''): Promise<Record<string, Object> | undefined> {
    if (!this.current(run) || !this.repository) return undefined;
    const path = this.feedPath(cursor, since);
    const result = await this.repository.call(path);
    if (!this.current(run)) return undefined;
    this.mergePage(result);
    return result;
  }

  private latestSeenAt(): string {
    let newest = '';
    let newestTime = 0;
    this.items.forEach((item: Record<string, Object>) => {
      const body = Wire.object(item, 'body');
      const candidates = [Wire.text(item, 'at'), Wire.text(body, 'sent_at'), Wire.text(body, 'filed_at'),
        Wire.text(body, 'dismissed_at'), Wire.text(body, 'dismissal_updated_at')];
      candidates.forEach((value: string) => {
        const at = timestamp(value);
        if (at > newestTime) { newestTime = at; newest = value; }
      });
    });
    return newest;
  }

  async refresh(): Promise<void> {
    const run = this.generation;
    if (!this.current(run)) return;
    if (this.refreshPromise && this.refreshRun === run) return this.refreshPromise;
    this.loading = true; this.error = '';
    const work = this.refreshCurrent(run);
    this.refreshPromise = work; this.refreshRun = run;
    try { await work; }
    finally {
      if (this.refreshPromise === work) this.refreshPromise = undefined;
      if (this.generation === run) this.loading = false;
    }
  }

  private async refreshCurrent(run: number): Promise<void> {
    try {
      const initial = !this.hasLoaded;
      const since = initial ? '' : this.latestSeenAt();
      let result = await this.readPage(run, '', since);
      if (!result || !this.current(run)) return;
      if (initial) this.nextCursor = Wire.text(result, 'next_cursor');
      this.hasLoaded = true;
      let cursor = Wire.text(result, 'next_cursor');
      // A refresh window can be large; bound it and continue from its newest timestamp on the next refresh.
      for (let page = 1; cursor && page < 4; page++) {
        const before = cursor;
        result = await this.readPage(run, cursor);
        if (!result || !this.current(run)) return;
        cursor = Wire.text(result, 'next_cursor');
        // The next cursor is the boundary after the newest page actually read.
        // Keep it only for the initial, full-history window: a later since
        // refresh has a separate cursor and must not replace the older-history
        // boundary exposed by Load more.
        if (initial) this.nextCursor = cursor;
        if (cursor === before) break;
      }
      if (!this.current(run)) return;
      await this.refreshTodos(run);
    } catch (error) {
      if (this.current(run)) this.error = this.errorKey(error as Error);
    }
  }

  async loadMore(): Promise<void> {
    const run = this.generation, cursor = this.nextCursor;
    if (!cursor || !this.current(run) || this.loadingMore) return;
    this.loadingMore = true; this.todoError = '';
    try {
      const result = await this.readPage(run, cursor);
      if (!result || !this.current(run)) return;
      const next = Wire.text(result, 'next_cursor');
      this.nextCursor = next === cursor ? '' : next;
      await this.refreshTodos(run);
    } catch (error) { if (this.current(run)) this.error = this.errorKey(error as Error); }
    finally { if (this.generation === run) this.loadingMore = false; }
  }

  private todoIds(): string[] {
    const ids: string[] = [];
    this.taskCards.forEach((card: Record<string, Object>) => { const id = Wire.text(card, 'todo_id'); if (id && !ids.includes(id)) ids.push(id); });
    Object.keys(this.plans).forEach((planId: string) => Wire.rows(Wire.record(this.plans[planId]), 'tasks').forEach((task: Record<string, Object>) => {
      const id = Wire.text(task, 'todo_id'); if (id && !ids.includes(id)) ids.push(id);
    }));
    this.items.forEach((item: Record<string, Object>) => {
      if (['task', 'receipt', 'report', 'review', 'answered', 'routed'].includes(Wire.text(item, 'kind'))) {
        const id = Wire.text(item, 'ref'); if (id && !ids.includes(id)) ids.push(id);
      }
    });
    return ids;
  }

  private async refreshTodos(run: number): Promise<void> {
    if (!this.current(run) || !this.repository) return;
    const ids = this.todoIds();
    if (!ids.length) { this.todos = []; this.todoError = ''; return; }
    let path = '/api/v1/todos';
    if (this.owner) path += '?manager=' + segment(this.owner);
    try {
      if (!this.current(run)) return;
      const result = await this.repository.call(path);
      if (!this.current(run)) return;
      this.todos = Wire.rows(result, 'todos').filter((todo: Record<string, Object>) => {
        const id = Wire.text(todo, 'id');
        const manager = Wire.text(todo, 'manager', this.owner);
        return ids.includes(id) && manager === this.owner && Wire.text(todo, 'origin') === this.conversationId;
      });
      this.todoError = '';
    } catch (error) { if (this.current(run)) this.todoError = this.errorKey(error as Error); }
  }

  private errorKey(error: Error): string {
    const data = error as Object as Record<string, Object>;
    if (data['resultUnknown'] === true) return 'result_unknown';
    const status = typeof data['status'] === 'number' ? data['status'] as number : 0;
    if (status === 401 || status === 403) return 'permission_denied';
    if (status === 404) return 'unsupported';
    if (status === 409 || status === 410) return 'request_updated';
    return error.message === 'offline' ? 'offline' : 'failed';
  }

  private plan(id: string): Record<string, Object> { return Wire.record(this.plans[id]); }
  private planActionPath(id: string, action: string): string {
    return '/api/v1/task-plans/' + segment(id) + '/' + action;
  }
  private blocked(path: string): boolean {
    if (hasId(this.uncertainKeys, path)) return true;
    return !!this.client && this.client.operationBlocked(path);
  }
  private markBusy(key: string, value: boolean): void { this.busyKeys = updated(this.busyKeys, key, value); }
  private markUncertain(path: string, value: boolean): void { this.uncertainKeys = updated(this.uncertainKeys, path, value); }
  private setPlan(id: string, plan: Record<string, Object>): void {
    if (!id || Wire.text(plan, 'id') !== id) return;
    const conversation = Wire.text(plan, 'conversation');
    if (conversation && conversation !== this.conversationId) return;
    const next: Record<string, Object> = {};
    Object.keys(this.plans).forEach((key: string) => next[key] = this.plans[key]);
    next[id] = plan; this.plans = next;
  }

  planState(id: string): string { return Wire.text(this.plan(id), 'state', 'unknown'); }
  planCanStart(id: string): boolean {
    const path = this.planActionPath(id, 'start');
    return this.current(this.generation) && this.planState(id) === 'open' && !hasId(this.busyKeys, 'plan:' + id) && !this.blocked(path);
  }
  planCanDismiss(id: string): boolean {
    const path = this.planActionPath(id, 'dismiss');
    return this.current(this.generation) && this.planState(id) === 'open' && !hasId(this.busyKeys, 'plan:' + id) && !this.blocked(path);
  }
  planBusy(id: string): boolean { return hasId(this.busyKeys, 'plan:' + id); }
  rememberPlan(id: string): boolean { return hasId(this.rememberPlans, id); }
  toggleRememberPlan(id: string): void { this.rememberPlans = updated(this.rememberPlans, id, !this.rememberPlan(id)); }

  beginEditPlan(id: string): boolean {
    if (this.planState(id) !== 'open' || !this.planCanStart(id)) return false;
    this.editingPlanId = id;
    this.editTasks = Wire.rows(this.plan(id), 'tasks').map((task: Record<string, Object>) => new PlanEditTask(task));
    this.editing = true;
    return true;
  }

  updateEditTask(index: number, key: string, value: string): void {
    if (!this.editing || index < 0 || index >= this.editTasks.length || !['title', 'prompt', 'machine', 'cwd', 'permission'].includes(key)) return;
    const current = this.editTasks[index];
    if (key === 'permission') {
      if (current.readonly) current.permission = 'plan';
      else if (value === 'bypassPermissions') current.permission = value;
      else return;
    } else if (key === 'title') current.title = value;
    else if (key === 'prompt') current.prompt = value;
    else if (key === 'machine') current.machine = value;
    else if (key === 'cwd') current.cwd = value;
    this.editTasks = this.editTasks.slice();
  }

  private editPayload(): Record<string, Object>[] {
    const original = Wire.rows(this.plan(this.editingPlanId), 'tasks');
    return this.editTasks.map((edited: PlanEditTask, index: number) => {
      const before = original[index] || {};
      const patch: Record<string, Object> = {};
      ['title', 'prompt', 'machine', 'cwd'].forEach((key: string) => {
        const value = edited.value(key).trim();
        if (value && value !== Wire.text(before, key)) patch[key] = value;
      });
      if (!edited.readonly && edited.permission === 'bypassPermissions' && edited.permission !== Wire.text(before, 'permission', 'default')) patch['permission'] = edited.permission;
      return patch;
    });
  }

  cancelPlanEdit(): void { this.editing = false; this.editingPlanId = ''; this.editTasks = []; }

  async startEditedPlan(): Promise<boolean> {
    const id = this.editingPlanId;
    if (!id || this.editTasks.some((task: PlanEditTask) => !task.title.trim() || !task.prompt.trim() || !task.machine.trim() || !task.cwd.trim())) {
      this.error = 'required'; return false;
    }
    if (this.editTasks.some((task: PlanEditTask) => !task.readonly && task.permission !== 'bypassPermissions')) {
      this.error = 'assistant_feed_permission_required'; return false;
    }
    const started = await this.startPlan(id, this.rememberPlan(id), this.editPayload());
    if (started) this.cancelPlanEdit();
    return started;
  }

  async startPlan(id: string, remember: boolean = false, edits?: Record<string, Object>[]): Promise<boolean> {
    const run = this.generation, path = this.planActionPath(id, 'start');
    const repository = this.repository;
    if (!repository || !this.current(run) || !this.planCanStart(id)) {
      if (this.blocked(path)) this.error = 'result_unknown';
      return false;
    }
    const body = Wire.payload(['remember'], [remember]);
    if (edits) body['tasks'] = edits;
    this.error = ''; this.markBusy('plan:' + id, true);
    try {
      if (!this.current(run)) return false;
      const response = await repository.call(path, 'POST', body);
      if (!this.current(run)) return false;
      const started = Wire.object(response, 'plan');
      if (Wire.text(started, 'state') !== 'started' || Wire.text(started, 'id') !== id) {
        this.markUncertain(path, true); this.error = 'result_unknown';
        await this.reconcilePlan(id, run);
        await this.refresh();
        return false;
      }
      this.setPlan(id, started); this.markUncertain(path, false);
      await this.refresh();
      if (!this.current(run)) return false;
      return true;
    } catch (error) {
      if (!this.current(run)) return false;
      this.error = this.errorKey(error as Error);
      if (this.error === 'result_unknown') {
        this.markUncertain(path, true);
        await this.reconcilePlan(id, run);
      } else if (this.error === 'request_updated' || this.error === 'unsupported') {
        await this.reconcilePlan(id, run);
      }
      if (this.current(run) && (this.error === 'result_unknown' || this.error === 'request_updated' || this.error === 'unsupported')) {
        // A competing device may have started the plan before this response was
        // lost or rejected. Read its linked Todos before presenting a retry path.
        await this.refreshTodos(run);
      }
      return false;
    } finally { if (this.generation === run) this.markBusy('plan:' + id, false); }
  }

  async dismissPlan(id: string): Promise<boolean> {
    const run = this.generation, path = this.planActionPath(id, 'dismiss'), repository = this.repository;
    if (!repository || !this.current(run) || !this.planCanDismiss(id)) {
      if (this.blocked(path)) this.error = 'result_unknown';
      return false;
    }
    this.error = ''; this.markBusy('plan:' + id, true);
    try {
      if (!this.current(run)) return false;
      const response = await repository.call(path, 'POST', Wire.payload([], []));
      if (!this.current(run)) return false;
      const dismissed = Wire.object(response, 'plan');
      if (Wire.text(dismissed, 'state') !== 'dismissed' || Wire.text(dismissed, 'id') !== id) {
        this.markUncertain(path, true); this.error = 'result_unknown';
        await this.reconcilePlan(id, run); return false;
      }
      this.setPlan(id, dismissed); this.markUncertain(path, false);
      await this.refresh(); return this.current(run);
    } catch (error) {
      if (!this.current(run)) return false;
      this.error = this.errorKey(error as Error);
      if (this.error === 'result_unknown') this.markUncertain(path, true);
      if (this.error === 'result_unknown' || this.error === 'request_updated' || this.error === 'unsupported') await this.reconcilePlan(id, run);
      return false;
    } finally { if (this.generation === run) this.markBusy('plan:' + id, false); }
  }

  private async reconcilePlan(id: string, run: number): Promise<void> {
    if (!this.current(run) || !this.repository) return;
    const path = '/api/v1/task-plans/' + segment(id);
    try {
      const result = await this.repository.call(path);
      if (!this.current(run)) return;
      const plan = Wire.object(result, 'plan');
      if (Wire.text(plan, 'id') !== id) return;
      this.setPlan(id, plan);
      const state = Wire.text(plan, 'state');
      const actionPath = this.planActionPath(id, 'start');
      const dismissPath = this.planActionPath(id, 'dismiss');
      if (state === 'started' || state === 'dismissed' || state === 'open' && !this.client?.operationBlocked(actionPath) && !this.client?.operationBlocked(dismissPath)) {
        this.markUncertain(actionPath, false); this.markUncertain(dismissPath, false);
      }
    } catch (_) { /* The retained plan and unknown marker remain visible until a later explicit refresh. */ }
  }

  setMessageDraft(id: string, value: string): void {
    const next: Record<string, Object> = {};
    Object.keys(this.messageDrafts).forEach((key: string) => next[key] = this.messageDrafts[key]);
    next[id] = value; this.messageDrafts = next;
  }
  messageDraft(item: Record<string, Object>): string {
    const id = Wire.text(item, 'id');
    const custom = this.messageDrafts[id];
    return typeof custom === 'string' ? custom as string : Wire.text(Wire.object(item, 'body'), 'draft');
  }
  messageState(item: Record<string, Object>): string {
    const body = Wire.object(item, 'body');
    if (Wire.text(body, 'sent_at')) return 'sent';
    if (Wire.text(body, 'filed_at')) return 'filed';
    if (Wire.text(body, 'dismissed_at')) return 'dismissed';
    if (hasId(this.pendingMessageIds, Wire.text(item, 'id'))) return 'pending';
    return 'open';
  }
  messageCanSend(item: Record<string, Object>): boolean {
    const id = Wire.text(item, 'id'), body = Wire.object(item, 'body'), ref = Wire.text(item, 'ref');
    const path = this.ownerPath('/api/v1/inbox/' + segment(ref) + '/reply');
    const draft = this.messageDraft(item);
    return this.current(this.generation) && Wire.text(item, 'kind') === 'message' && !!id && !!ref &&
      this.messageState(item) === 'open' && draft.trim().length > 0 && !draftPlaceholder(draft) && !this.blocked(path) && !hasId(this.busyKeys, 'message:' + id) &&
      Wire.has(body, 'draft');
  }
  async sendMessage(id: string, text: string): Promise<boolean> {
    const run = this.generation, item = this.items.find((value: Record<string, Object>) => Wire.text(value, 'id') === id);
    if (!item || !this.messageCanSend(item) || text !== this.messageDraft(item)) return false;
    const ref = Wire.text(item, 'ref');
    const path = '/api/v1/inbox/' + segment(ref) + '/reply' + (this.owner ? '?manager=' + segment(this.owner) : '');
    if (this.blocked(path)) { this.error = 'result_unknown'; return false; }
    const repository = this.repository;
    if (!repository || !this.current(run)) return false;
    this.markBusy('message:' + id, true); this.error = '';
    try {
      if (!this.current(run)) return false;
      await repository.call(path, 'POST', Wire.payload(['text', 'card'], [text, id]));
      if (!this.current(run)) return false;
      this.pendingMessageIds = updated(this.pendingMessageIds, id, true);
      await this.refresh();
      return this.current(run);
    } catch (error) {
      if (!this.current(run)) return false;
      this.error = this.errorKey(error as Error);
      if (this.error === 'result_unknown') this.pendingMessageIds = updated(this.pendingMessageIds, id, true);
      return false;
    } finally { if (this.generation === run) this.markBusy('message:' + id, false); }
  }

  messageCanDismiss(item: Record<string, Object>): boolean {
    const id = Wire.text(item, 'id'), body = Wire.object(item, 'body');
    const path = this.ownerPath('/api/v1/assistant/feed/' + segment(id) + '/dismiss');
    return this.current(this.generation) && Wire.text(item, 'kind') === 'message' && this.messageState(item) === 'open' &&
      !!Wire.text(body, 'draft') && !this.blocked(path) && !hasId(this.busyKeys, 'message:' + id);
  }
  async dismissMessage(id: string): Promise<boolean> {
    const run = this.generation, item = this.items.find((value: Record<string, Object>) => Wire.text(value, 'id') === id);
    if (!item || !this.messageCanDismiss(item) || !this.repository) return false;
    const path = '/api/v1/assistant/feed/' + segment(id) + '/dismiss' + (this.owner ? '?manager=' + segment(this.owner) : '');
    const draft = Wire.text(Wire.object(item, 'body'), 'draft');
    this.markBusy('message:' + id, true); this.error = '';
    try {
      if (!this.current(run)) return false;
      const response = await this.repository.call(path, 'POST', Wire.payload(['draft'], [draft]));
      if (!this.current(run)) return false;
      const updated = Wire.object(response, 'item');
      if (Wire.text(updated, 'id') !== id || Wire.text(updated, 'conversation') !== this.conversationId) { this.error = 'request_updated'; return false; }
      this.mergeItems([updated]); return true;
    } catch (error) {
      if (this.current(run)) {
        this.error = this.errorKey(error as Error);
        if (this.error === 'result_unknown') this.pendingMessageIds = updated(this.pendingMessageIds, id, true);
      }
      return false;
    }
    finally { if (this.generation === run) this.markBusy('message:' + id, false); }
  }
  messageCanUndo(item: Record<string, Object>): boolean {
    const id = Wire.text(item, 'id'), body = Wire.object(item, 'body'), token = Wire.text(body, 'dismissal_id');
    const path = this.ownerPath('/api/v1/assistant/feed/' + segment(id) + '/undo-dismiss');
    return this.current(this.generation) && Wire.text(item, 'kind') === 'message' && !!token && this.messageState(item) === 'dismissed' &&
      !this.blocked(path) && !hasId(this.busyKeys, 'message:' + id);
  }
  async undoDismissMessage(id: string): Promise<boolean> {
    const run = this.generation, item = this.items.find((value: Record<string, Object>) => Wire.text(value, 'id') === id);
    if (!item || !this.messageCanUndo(item) || !this.repository) return false;
    const token = Wire.text(Wire.object(item, 'body'), 'dismissal_id');
    const path = '/api/v1/assistant/feed/' + segment(id) + '/undo-dismiss' + (this.owner ? '?manager=' + segment(this.owner) : '');
    this.markBusy('message:' + id, true); this.error = '';
    try {
      if (!this.current(run)) return false;
      const response = await this.repository.call(path, 'POST', Wire.payload(['dismissal_id'], [token]));
      if (!this.current(run)) return false;
      const updated = Wire.object(response, 'item');
      if (Wire.text(updated, 'id') !== id || Wire.text(updated, 'conversation') !== this.conversationId) { this.error = 'request_updated'; return false; }
      this.mergeItems([updated]); return true;
    } catch (error) { if (this.current(run)) this.error = this.errorKey(error as Error); return false; }
    finally { if (this.generation === run) this.markBusy('message:' + id, false); }
  }

  todo(todoId: string): Record<string, Object> { return this.todos.find((value: Record<string, Object>) => Wire.text(value, 'id') === todoId) || {}; }
  taskCardsForConversation(): Record<string, Object>[] {
    const rows: Record<string, Object>[] = [];
    const seen: string[] = [];
    this.taskCards.forEach((card: Record<string, Object>) => {
      const id = Wire.text(card, 'todo_id');
      if (id && this.todo(id) && !seen.includes(id)) { rows.push(card); seen.push(id); }
    });
    // Legacy servers may omit task_cards; feed refs and started plan todo ids still identify real tasks.
    this.items.forEach((item: Record<string, Object>) => {
      const kind = Wire.text(item, 'kind');
      if (['task', 'receipt', 'report', 'review'].includes(kind)) {
        const id = Wire.text(item, 'ref');
        if (id && this.todo(id) && !seen.includes(id)) {
          rows.push(Wire.payload(['id', 'todo_id', 'state'], ['todo:' + id, id, 'unknown'])); seen.push(id);
        }
      }
    });
    Object.keys(this.plans).forEach((planId: string) => {
      const plan = Wire.record(this.plans[planId]);
      Wire.rows(plan, 'tasks').forEach((task: Record<string, Object>, index: number) => {
        const id = Wire.text(task, 'todo_id');
        if (id && this.todo(id) && !seen.includes(id)) {
          rows.push(Wire.payload(['id', 'plan_id', 'index', 'todo_id', 'at', 'updated_at', 'state'],
            ['plan:' + planId + ':' + String(index), planId, index, id, Wire.text(plan, 'created_at'), Wire.text(plan, 'updated_at'), 'unknown']));
          seen.push(id);
        }
      });
    });
    return rows.sort((left: Record<string, Object>, right: Record<string, Object>) => timestamp(Wire.text(left, 'at')) - timestamp(Wire.text(right, 'at')));
  }

  openTodo(id: string): ResourceRow | undefined {
    if (!this.current(this.generation)) return undefined;
    const todo = this.todo(id);
    if (!Object.keys(todo).length || Wire.text(todo, 'origin') !== this.conversationId || Wire.text(todo, 'manager', this.owner) !== this.owner) return undefined;
    const row = ResourceRow.from(todo, 'todo'); row.manager = this.owner; return row;
  }

  openAgent(todoId: string, agent: Record<string, Object>): ResourceRow | undefined {
    if (!this.current(this.generation) || Wire.text(this.todo(todoId), 'origin') !== this.conversationId) return undefined;
    const machine = Wire.text(agent, 'machine_id'), session = Wire.text(agent, 'session_id');
    if (!machine || !session) return undefined;
    const listed = this.client?.sessions.find((row: ResourceRow) => row.manager === this.owner && row.machine === machine && (row.session || row.id) === session);
    if (listed) return listed;
    const row = new ResourceRow(session, Wire.text(agent, 'title', Wire.text(agent, 'agent', session)), 'session');
    row.machine = machine; row.session = session; row.manager = this.owner; row.path = Wire.text(agent, 'cwd');
    row.raw = JSON.stringify(agent); return row;
  }
}
