import { TranscriptReducer, TranscriptSync, TranscriptItem, TranscriptBatch, TranscriptAction, TranscriptSnapshot } from '../core/Domain';
import { HubRepository, segment } from './HubRepository';
import { ResourceRow, Wire } from './Wire';
import { LocalStore } from '../platform/LocalStore';
import { boundedTranscriptCache } from '../core/TranscriptCache';

// A visible conversation owns its subscription, paging and continuity state.
export class TranscriptModel {
  private reducer: TranscriptReducer = new TranscriptReducer();
  private sync: TranscriptSync = new TranscriptSync(this.reducer);
  private active: boolean = true;
  private filling: number = -1;
  private refreshing: boolean = false;
  private refreshAgain: boolean = false;
  private readonly row: ResourceRow;
  private readonly repository: HubRepository;
  private readonly local: LocalStore;
  private readonly key: string;
  private readonly publish: (items: readonly TranscriptItem[], hasMore: boolean) => void;
  private readonly failure: (error: Error) => void;

  constructor(row: ResourceRow, repository: HubRepository, local: LocalStore, key: string,
    publish: (items: readonly TranscriptItem[], hasMore: boolean) => void, failure: (error: Error) => void) {
    this.row = row; this.repository = repository; this.local = local; this.key = key; this.publish = publish; this.failure = failure;
  }
  private path(query: string): string {
    return '/api/v1/sessions/' + segment(this.row.machine) + '/' + segment(this.row.session || this.row.id) + '/items?' + query;
  }
  private batch(data: Record<string, Object>): TranscriptBatch {
    // Presence matters: an older server omitted sequencing and has_more rather than returning zero/false.
    const canonical: Record<string, Object> = {};
    canonical['items'] = Wire.rows(data, 'items');
    if (Wire.has(data, 'history')) canonical['history'] = Wire.text(data, 'history');
    if (Wire.has(data, 'from_seq')) canonical['fromSeq'] = Wire.number(data, 'from_seq');
    if (Wire.has(data, 'to_seq')) canonical['toSeq'] = Wire.number(data, 'to_seq');
    if (Wire.has(data, 'backlog')) canonical['backlog'] = Wire.bool(data, 'backlog');
    if (Wire.has(data, 'has_more')) canonical['hasMore'] = Wire.bool(data, 'has_more');
    if (Wire.has(data, 'more_after')) canonical['moreAfter'] = Wire.bool(data, 'more_after');
    return JSON.parse(JSON.stringify(canonical)) as TranscriptBatch;
  }
  async open(): Promise<void> {
    const cached = await this.local.get('transcript:' + this.key);
    if (!this.active) return;
    if (cached) {
      try { this.reducer.restore(JSON.parse(cached) as TranscriptSnapshot); this.emit(); } catch (_) {}
    }
    await this.drive(this.sync.reconnect());
    await this.latest();
  }
  async latest(): Promise<void> {
    if (!this.active) return;
    if (this.refreshing) { this.refreshAgain = true; return; }
    this.refreshing = true;
    try {
      const data = await this.repository.call(this.path('lines=80&fold_work=1&lazy_images=1'));
      if (!this.active) return;
      await this.drive(this.sync.settle(this.reducer.latest(this.batch(data)))); this.emit();
    } catch (error) { if (this.active) this.failure(error as Error); }
    finally {
      this.refreshing = false;
      if (this.refreshAgain && this.active) { this.refreshAgain = false; this.latest(); }
    }
  }
  receive(data: Record<string, Object>): void {
    if (!this.active || Wire.text(data, 'machine_id') !== this.row.machine || Wire.text(data, 'session_id') !== (this.row.session || this.row.id)) return;
    const batch = this.batch(data);
    if (Wire.text(data, 'type') === 'items_replace') this.sync.reload(batch.items, batch.history || null, batch.toSeq || null, batch.hasMore === true);
    else this.drive(this.sync.settle(this.reducer.apply(batch))).catch((error: Error) => this.failure(error));
    this.emit();
  }
  async older(): Promise<void> { await this.drive(this.sync.loadOlder()); }
  async unfold(id: string): Promise<void> { await this.drive(this.sync.openFold(id)); }
  private async drive(actions: readonly TranscriptAction[]): Promise<void> {
    if (!this.active) return;
    for (const action of actions) {
      if (!this.active) return;
      if (action.kind === 'schedule_fill') {
        if (this.filling !== -1) clearTimeout(this.filling);
        this.filling = setTimeout(() => { this.filling = -1; this.drive(this.sync.fillTimer()).catch((error: Error) => this.failure(error)); }, action.delayMillis);
      } else if (action.kind === 'subscribe') {
        const payload: Record<string, Object> = Wire.payload(['type', 'machine_id', 'session_id', 'fold_work'], ['subscribe', this.row.machine, this.row.session || this.row.id, true]);
        if (action.sinceSeq !== undefined) payload['since_seq'] = action.sinceSeq;
        this.repository.transport.send(JSON.stringify(payload));
      } else if (action.kind === 'fetch_latest') {
        await this.latest();
      } else if (action.kind === 'fetch_after') {
        const data = await this.repository.call(this.path('after_seq=' + action.afterSeq + '&lazy_images=1'));
        if (!this.active) return;
        await this.drive(this.sync.after(this.batch(data)).actions); this.emit();
      } else if (action.kind === 'fetch_older') {
        try {
          const data = await this.repository.call(this.path('before_seq=' + action.beforeSeq + '&lines=80&fold_work=1&lazy_images=1'));
          if (!this.active) return;
          const batch = this.batch(data); this.sync.older(batch.items, batch.hasMore === true); this.emit();
        } catch (error) { this.sync.finishOlder(); if (error instanceof Error) throw error; throw new Error('failed'); }
      } else if (action.kind === 'fetch_range') {
        try {
          const data = await this.repository.call(this.path('from_seq=' + action.fromSeq + '&through_seq=' + action.throughSeq + '&lazy_images=1'));
          if (!this.active) return;
          const batch = this.batch(data); await this.drive(this.sync.range(batch.items, Wire.bool(data, 'more'))); this.emit();
        } catch (error) { this.sync.cancelFold(); if (error instanceof Error) throw error; throw new Error('failed'); }
      }
    }
  }
  private emit(): void {
    if (!this.active) return;
    this.publish(this.reducer.items, this.reducer.hasMore === true);
    this.local.put('transcript:' + this.key, boundedTranscriptCache(this.reducer.snapshot())).catch((error: Error) => { if (this.active) this.failure(error); });
  }
  close(): void {
    if (!this.active) return; this.active = false;
    if (this.filling !== -1) clearTimeout(this.filling);
    try { this.repository.transport.send(JSON.stringify(Wire.payload(['type', 'machine_id', 'session_id'], ['unsubscribe', this.row.machine, this.row.session || this.row.id]))); } catch (_) {}
  }
}
