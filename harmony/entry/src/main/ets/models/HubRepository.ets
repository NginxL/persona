import { interactionResolution } from './InteractionDecision';
import { util } from '@kit.ArkTS';
import { HubTransport, EndpointConfig, HttpFailure } from '../platform/HubTransport';
import { ResourceRow, Wire, ChatMessage, Decision, DecisionField } from './Wire';

import { LocalStore } from '../platform/LocalStore';
import { MutationStore, MutationRecord } from './MutationStore';

export function segment(value: string): string { return encodeURIComponent(value); }

export class HubRepository {
  readonly transport: HubTransport;
  private owner: string;
  private defaultOwner: string;
  readonly mutations: MutationStore | undefined;
  private changed: () => void;
  constructor(config: EndpointConfig, local?: LocalStore, changed: () => void = () => {}, defaultOwner: string = config.managerId,
    transport?: HubTransport) {
    this.transport = transport || new HubTransport(config); this.owner = config.managerId; this.changed = changed; this.defaultOwner = defaultOwner;
    if (local) this.mutations = MutationStore.shared(local, JSON.stringify([config.hubId, config.managerId]));
  }
  async call(path: string, method: string = 'GET', body: Record<string, Object> = {}, op: string = '', admit?: () => boolean, completedAsAck: boolean = false): Promise<Record<string, Object>> {
    if (method === 'GET' || !this.mutations) { if (admit && !admit()) throw new HttpFailure(0, 'request_updated', false); return Wire.parse(await this.transport.request(path, method, method === 'GET' ? '' : JSON.stringify(body), op)); }
    const payload = JSON.stringify(body), record = await this.mutations.begin(path, method, payload, op); this.changed();
    let pendingReceipt = false;
    try {
      if (admit && !admit()) { await this.mutations.finish(record, 'failed'); throw new HttpFailure(0, 'request_updated', false); }
      const result = Wire.parse(await this.transport.request(path, method, payload, record.id));
      const state = Wire.text(result, 'state');
      if (state === 'pending' || state === 'unknown' || Wire.text(result, 'delivered') === 'unknown') {
        pendingReceipt = state === 'pending'; await this.mutations.finish(record, pendingReceipt ? 'pending' : 'unknown'); throw new HttpFailure(202, 'result_unknown', true, pendingReceipt);
      }
      await this.mutations.finish(record, 'done'); return result;
    } catch (error) {
      if (completedAsAck && record.state === 'done') return Wire.payload(['state'], ['done']);
      if (record.state === 'pending' && !pendingReceipt) await this.mutations.finish(record, error instanceof HttpFailure && error.resultPending ? 'pending' : error instanceof HttpFailure && !error.resultUnknown ? 'failed' : 'unknown');
      if (completedAsAck && record.state === 'done') return Wire.payload(['state'], ['done']);
      if (error instanceof HttpFailure) throw error;
      throw new HttpFailure(0, 'result_unknown', true);
    } finally { this.changed(); }
  }
  async upload(path: string, bytes: ArrayBuffer, admit?: () => boolean): Promise<Record<string, Object>> {
    if (!this.mutations) throw new Error('storage_unavailable');
    const record = await this.mutations.begin(path, 'POST', ''); this.changed();
    try {
      if (admit && !admit()) { await this.mutations.finish(record, 'failed'); throw new HttpFailure(0, 'request_updated', false); }
      const result = await this.transport.requestBytes(path, 'POST', bytes, 'application/octet-stream', record.id);
      const receipt = Wire.parse(util.TextDecoder.create('utf-8', { fatal: true }).decodeToString(new Uint8Array(result.data)));
      if (!Wire.text(receipt, 'path') && !Wire.text(receipt, 'name')) throw new HttpFailure(0, 'result_unknown', true);
      await this.mutations.finish(record, 'done'); return receipt;
    } catch (error) {
      if (record.state === 'pending') await this.mutations.finish(record, error instanceof HttpFailure && error.resultPending ? 'pending' : error instanceof HttpFailure && !error.resultUnknown ? 'failed' : 'unknown');
      if (error instanceof HttpFailure) throw error;
      throw new HttpFailure(0, 'result_unknown', true);
    } finally { this.changed(); }
  }
  async list(path: string, envelope: string, kind: string, merged: boolean = false): Promise<ResourceRow[]> {
    const result = await this.call(path);
    return Wire.rows(result, envelope).map((item: Record<string, Object>) => { const row = ResourceRow.from(item, kind); if (!Wire.has(item, 'manager')) row.manager = merged ? this.defaultOwner : this.owner; return row; }).filter((row: ResourceRow) => !merged || row.manager === this.owner);
  }
  async sessions(): Promise<ResourceRow[]> { return this.list('/api/v1/sessions', 'sessions', 'session', true); }
  async machines(): Promise<ResourceRow[]> { return this.list('/api/v1/machines', 'machines', 'machine', true); }
  async todos(): Promise<ResourceRow[]> { return this.list('/api/v1/todos', 'todos', 'todo'); }
  async inbox(): Promise<ResourceRow[]> { return this.list('/api/v1/inbox?sources=all', 'items', 'inbox', true); }
  async knowledge(kind: string = 'projects', query: string = ''): Promise<ResourceRow[]> {
    if (!['projects', 'chats', 'people', 'docs', 'meetings', 'events', 'recordings'].includes(kind)) throw new Error('unsupported');
    if (query) {
      const data = await this.call('/api/v1/kb-search?q=' + segment(query) + '&messages=0');
      return Wire.rows(data, 'hits').map((item: Record<string, Object>) => {
        const row = new ResourceRow(Wire.text(item, 'path'), Wire.text(item, 'line'), 'knowledge_hit'); row.detail = Wire.text(item, 'path'); row.manager = this.owner; return row;
      });
    }
    const rows = await this.list('/api/v1/kb/' + kind, 'items', 'knowledge');
    rows.forEach((row: ResourceRow) => row.path = kind); return rows;
  }
  async conversations(): Promise<ResourceRow[]> { return this.list('/api/v1/assistant', 'conversations', 'conversation'); }
  async newConversation(main: boolean, op: string): Promise<ResourceRow> {
    const data = await this.call('/api/v1/assistant/conversations', 'POST', Wire.payload(['main', 'prompt'], [main, '']), op);
    const row = ResourceRow.from(Wire.object(data, 'session'), 'conversation'); row.session = row.id; row.manager = this.owner; return row;
  }
  async resource(row: ResourceRow): Promise<ResourceRow> {
    if (row.kind === 'knowledge_hit') return row;
    const root = row.kind === 'todo' ? '/todos/' : row.kind === 'inbox' ? '/inbox/' : '/kb/' + segment(row.path) + '/';
    const result = await this.call('/api/v1' + root + segment(row.id) + '?manager=' + segment(row.manager));
    const data = Wire.object(result, row.kind === 'todo' ? 'todo' : row.kind === 'knowledge' ? 'detail' : 'item');
    const next = ResourceRow.from(Object.keys(data).length ? data : result, row.kind); next.path = row.path; next.manager = row.manager;
    if (next.id && next.id !== row.id) throw new Error('identity_changed');
    next.id = row.id; return next;
  }
  async save(row: ResourceRow, title: string, content: string, done?: boolean): Promise<void> {
    if (row.kind !== 'todo') throw new Error('unsupported');
    const body = Wire.payload(['title'], [title]);
    body[row.kind === 'todo' ? 'notes' : 'content'] = content;
    if (done !== undefined) body['done'] = done;
    const root = row.kind === 'todo' ? '/todos' : '/knowledge/items';
    await this.call('/api/v1' + root + (row.id ? '/' + segment(row.id) : '') + '?manager=' + segment(row.manager), row.id ? 'PATCH' : 'POST', body);
  }
  async remove(row: ResourceRow): Promise<void> {
    if (row.kind !== 'todo') throw new Error('unsupported');
    const root = '/todos/';
    await this.call('/api/v1' + root + segment(row.id) + '?manager=' + segment(row.manager), 'DELETE');
  }
  async feed(row: ResourceRow): Promise<Record<string, Object>> {
    return this.call('/api/v1/sessions/' + segment(row.machine) + '/' + segment(row.session || row.id) + '/items?lines=80&fold_work=1&lazy_images=1');
  }
  async cards(row: ResourceRow): Promise<Record<string, Object>> { return this.call('/api/v1/assistant/conversations/' + segment(row.id) + '/feed'); }
  async send(row: ResourceRow, text: string, op: string, context: string, images: Object[] = [], admit?: () => boolean): Promise<void> {
    const body = Wire.payload(['text', 'client_msg_id'], [text, op]); if (images.length) body['images'] = images;
    await this.call('/api/v1/sessions/' + segment(row.machine) + '/' + segment(row.session || row.id) + '/messages', 'POST', body, op, admit, true);
  }
  async stop(row: ResourceRow): Promise<void> { await this.call('/api/v1/sessions/' + segment(row.machine) + '/' + segment(row.session || row.id) + '/interrupt', 'POST'); }
  async decisions(): Promise<Record<string, Object>[]> {
    const result = await this.call('/api/v1/interactions'); return Wire.rows(result, 'interactions');
  }
  async resolve(request: Decision, option: string, values: Record<string, Object>, op: string): Promise<Record<string, Object>> {
    const body = interactionResolution(request, option, values);
    return this.call('/api/v1/interactions/' + segment(request.id) + '/resolve', 'POST', body, op);
  }
  async saveKnowledge(row: ResourceRow, tags: string[], importance: string): Promise<void> {
    if (row.kind !== 'knowledge' || !row.id || !['projects','chats','people','docs','meetings','events','recordings'].includes(row.path)) throw new Error('unsupported');
    const body = Wire.payload(['tags'], [tags]); if (importance) body['importance'] = importance;
    await this.call('/api/v1/kb/' + segment(row.path) + '/' + segment(row.id) + '?manager=' + segment(row.manager), 'PUT', body);
  }
  async reply(row: ResourceRow, text: string, op: string): Promise<ResourceRow> {
    const target = Wire.object(row.data(), 'reply_target');
    const body = Wire.payload(['text', 'client_id'], [text, op]);
    if (Object.keys(target).length || Wire.has(row.data(), 'mail')) {
      const uid = Wire.number(target, 'uid'), version = Wire.text(target, 'version');
      if (!Number.isInteger(uid) || uid <= 0 || uid > 4294967295 || !version) throw new Error('request_updated');
      body['expected_uid'] = uid; body['expected_version'] = version;
    }
    const result = await this.call('/api/v1/inbox/' + segment(row.id) + '/reply?manager=' + segment(row.manager), 'POST', body, op);
    const item = Wire.object(result, 'item'); const next = ResourceRow.from(Object.keys(item).length ? item : result, 'inbox'); next.manager = row.manager; return next;
  }
  async processInbox(row: ResourceRow): Promise<ResourceRow> {
    const result = await this.call('/api/v1/inbox/' + segment(row.id) + '/done?manager=' + segment(row.manager), 'POST', Wire.payload(['done'], [!row.isDone()]));
    const item = Wire.object(result, 'item'); const next = ResourceRow.from(Object.keys(item).length ? item : result, 'inbox'); next.manager = row.manager; return next;
  }
  async createAgent(prompt: string, machine: ResourceRow, cwd: string, launcher: string, op: string, todoId: string = '', admit?: () => boolean): Promise<ResourceRow> {
    if (!machine.id || machine.manager && machine.manager !== this.owner || Wire.text(machine.data(), 'trust') === 'pending' ||
      !Wire.bool(machine.data(), 'online', true) || Wire.bool(machine.data(), 'pending_confirm') || Wire.bool(machine.data(), 'pending')) throw new Error('permission_denied');
    const body = Wire.payload(['machine_id', 'cwd', 'launcher', 'prompt'], [machine.id, cwd, launcher, prompt]);
    if (todoId) body['todo_id'] = todoId;
    const result = await this.call('/api/v1/sessions', 'POST', body, op, admit);
    const row = ResourceRow.from(Wire.object(result, 'session'), 'session'); row.manager = this.owner; row.machine = row.machine || machine.id; row.session = row.session || row.id; return row;
  }
  close(): void { this.transport.close(); }
}
