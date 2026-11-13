import type { LocalStore } from '../platform/LocalStore';
import { Wire } from './Wire';

export class ChatDraft {
  text: string = '';
  context: string = '';
  revision: string = '';
}
export class ChatOutboxEntry {
  id: string = '';
  target: string = '';
  revision: string = '';
  text: string = '';
  context: string = '';
  imageCount: number = 0;
  attachmentSnapshot: string = '';
  // Rebound from the local store on read; never trust a persisted process epoch.
  localEpoch: number = 0;
  at: number = 0;
  state: string = 'pending';
  confirmedPending: boolean = false;
  draft: string = '';
}

/** Serialize complete read/modify/write transactions across all detail contexts sharing a database handle. */
export class ChatDraftStore {
  private static queues: Map<LocalStore, Map<string, Promise<void>>> = new Map();
  private local: LocalStore;
  constructor(local: LocalStore) { this.local = local; }
  private async transaction<T>(key: string, action: (epoch: number) => Promise<T>): Promise<T> {
    let queues = ChatDraftStore.queues.get(this.local);
    if (!queues) { queues = new Map(); ChatDraftStore.queues.set(this.local, queues); }
    const epoch = this.local.revision(key), previous = queues.get(key) || Promise.resolve();
    const work = previous.catch(() => {}).then(async (): Promise<T> => {
      if (!this.local.accepts(key, epoch)) throw new Error('scope_retired');
      const result = await action(epoch); if (!this.local.accepts(key, epoch)) throw new Error('scope_retired'); return result;
    }), tail = work.then(() => {}, () => {});
    queues.set(key, tail);
    try { return await work; } finally { if (queues.get(key) === tail) queues.delete(key); if (!queues.size) ChatDraftStore.queues.delete(this.local); }
  }
  private decodeDraft(raw: string): ChatDraft {
    const data = raw ? Wire.record(JSON.parse(raw) as Object) : Wire.payload([], []), draft = new ChatDraft();
    draft.text = Wire.text(data, 'text'); draft.context = Wire.text(data, 'context'); draft.revision = Wire.text(data, 'revision'); return draft;
  }
  async read(target: string): Promise<ChatDraft> {
    const key = 'draft:' + target; return this.transaction(key, async (): Promise<ChatDraft> => this.decodeDraft(await this.local.get(key)));
  }
  async save(target: string, draft: ChatDraft): Promise<void> {
    const key = 'draft:' + target, raw = JSON.stringify(draft); return this.transaction(key, async (epoch: number): Promise<void> => this.local.put(key, raw, epoch));
  }
  async acknowledge(target: string, revision: string): Promise<boolean> {
    if (!revision) return false;
    const key = 'draft:' + target;
    return this.transaction(key, async (epoch: number): Promise<boolean> => {
      const current = this.decodeDraft(await this.local.get(key));
      if (current.revision !== revision) return false;
      await this.local.put(key, JSON.stringify(new ChatDraft()), epoch); return true;
    });
  }
  private decodeEntry(data: Record<string, Object>): ChatOutboxEntry {
    const entry = new ChatOutboxEntry(); entry.id = Wire.text(data, 'id'); entry.target = Wire.text(data, 'target');
    entry.revision = Wire.text(data, 'revision'); entry.text = Wire.text(data, 'text'); entry.context = Wire.text(data, 'context');
    entry.draft = Wire.text(data, 'draft', entry.text); entry.confirmedPending = Wire.bool(data, 'confirmedPending');
    entry.attachmentSnapshot = Wire.text(data, 'attachmentSnapshot'); entry.imageCount = Wire.number(data, 'imageCount'); entry.at = Wire.number(data, 'at'); entry.state = Wire.text(data, 'state', 'unknown'); return entry;
  }
  private async entries(key: string): Promise<ChatOutboxEntry[]> {
    const raw = await this.local.get(key), data = raw ? Wire.record(JSON.parse(raw) as Object) : Wire.payload([], []), entries: ChatOutboxEntry[] = [];
    for (const id of Wire.strings(data, 'ids')) {
      const value = await this.local.get(key + ':' + id);
      if (!value) throw new Error('storage_unavailable');
      entries.push(this.decodeEntry(Wire.record(JSON.parse(value) as Object)));
    }
    return entries;
  }
  private async index(key: string, entries: ChatOutboxEntry[], epoch: number): Promise<void> {
    await this.local.put(key, JSON.stringify(Wire.payload(['ids'], [entries.map((entry: ChatOutboxEntry) => entry.id)])), epoch);
  }
  async list(scope: string): Promise<ChatOutboxEntry[]> {
    const key = 'outbox:' + scope; return this.transaction(key, async (epoch: number): Promise<ChatOutboxEntry[]> => { const entries = await this.entries(key); entries.forEach((entry: ChatOutboxEntry) => entry.localEpoch = epoch); return entries; });
  }
  async stage(scope: string, entry: ChatOutboxEntry): Promise<void> {
    const key = 'outbox:' + scope, captured = this.decodeEntry(Wire.record(JSON.parse(JSON.stringify(entry)) as Object));
    return this.transaction(key, async (epoch: number): Promise<void> => {
      let entries = await this.entries(key);
      const existing = entries.find((old: ChatOutboxEntry) => old.id === captured.id);
      if (existing) { if (existing.target !== captured.target || existing.revision !== captured.revision || existing.text !== captured.text || existing.context !== captured.context || existing.imageCount !== captured.imageCount || existing.attachmentSnapshot !== captured.attachmentSnapshot) throw new Error('request_updated'); return; }
      if (entries.some((old: ChatOutboxEntry) => old.target === captured.target && ['pending', 'unknown'].includes(old.state))) throw new Error('pending_action');
      const active = entries.filter((old: ChatOutboxEntry) => ['pending', 'unknown'].includes(old.state));
      if (active.length >= 128) throw new Error('pending_action');
      const repairs = entries.filter((old: ChatOutboxEntry) => old.state === 'done' && !!old.attachmentSnapshot), terminal = entries.filter((old: ChatOutboxEntry) => !['pending', 'unknown'].includes(old.state) && !(old.state === 'done' && !!old.attachmentSnapshot)), removed = terminal.slice(0, Math.max(0, terminal.length - 32));
      entries = active.concat(repairs, terminal.slice(-32)); entries.push(captured);
      await this.local.put(key + ':' + captured.id, JSON.stringify(captured), epoch); await this.index(key, entries, epoch);
      for (const old of removed) { try { await this.local.remove(key + ':' + old.id, epoch); } catch (_) {} }
    });
  }
  async settle(scope: string, id: string, state: string): Promise<void> {
    if (!['pending', 'unknown', 'failed', 'done', 'checked'].includes(state)) throw new Error('unsupported');
    const key = 'outbox:' + scope;
    return this.transaction(key, async (epoch: number): Promise<void> => {
      const entries = await this.entries(key), entry = entries.find((value: ChatOutboxEntry) => value.id === id);
      if (!entry) return;
      // An acknowledged message cannot become retryable because a later read or cleanup failed.
      if (entry.state === 'done' && state !== 'done') return;
      entry.state = state === 'checked' ? 'failed' : state; entry.confirmedPending = state === 'pending';
      await this.local.put(key + ':' + entry.id, JSON.stringify(entry), epoch);
    });
  }
  async attachmentsCommitted(scope: string, id: string): Promise<void> {
    const key = 'outbox:' + scope;
    return this.transaction(key, async (epoch: number): Promise<void> => {
      const entries = await this.entries(key), entry = entries.find((value: ChatOutboxEntry) => value.id === id);
      if (!entry || entry.state !== 'done') return;
      entry.attachmentSnapshot = ''; await this.local.put(key + ':' + id, JSON.stringify(entry), epoch);
    });
  }
  async discardEchoed(scope: string, ids: string[]): Promise<void> {
    if (!ids.length) return; const key = 'outbox:' + scope;
    return this.transaction(key, async (epoch: number): Promise<void> => {
      const entries = await this.entries(key), retained = entries.filter((entry: ChatOutboxEntry) => !ids.includes(entry.id));
      if (retained.length !== entries.length) { await this.index(key, retained, epoch); for (const entry of entries) if (ids.includes(entry.id)) { try { await this.local.remove(key + ':' + entry.id, epoch); } catch (_) {} } }
    });
  }
}
