import { decodeInteractionDecision, interactionHistoryResolution, interactionScreenOptions } from './InteractionDecision';
import type { InteractionChoice } from './InteractionDecision';
import type { ClientModel } from './ClientModel';
import type { HubRepository } from './HubRepository';
import { Decision, ResourceRow, Wire } from './Wire';

export class InteractionHistoryEntry {
  request: Decision = new Decision();
  outcome: Record<string, Object> = Wire.payload([], []);
  status: string = '';
  resolvedAt: string = '';
}

function historyEnvelope(source: Record<string, Object>, interaction: Record<string, Object>, resolution: Record<string, Object>): Record<string, Object> {
  return Wire.payload(['interaction', 'status', 'resolution', 'resolved_at'], [interaction, Wire.text(source, 'status'), resolution, Wire.text(source, 'resolved_at')]);
}

function sessionId(row: ResourceRow): string { return row.session || row.id; }

function redactedWireResolution(request: Decision, source: Record<string, Object>, outcome: Record<string, Object>): Record<string, Object> {
  const raw = Wire.object(source, 'resolution'), safe = Wire.payload([], []);
  if (request.kind === 'question') {
    const answers = Wire.payload([], []);
    Object.keys(outcome).forEach((key: string) => { if (typeof outcome[key] === 'string') answers[key] = outcome[key]; });
    if (Object.keys(answers).length) safe['answers'] = answers;
  } else if (request.kind === 'plan') {
    if (typeof outcome['approve'] === 'boolean') safe['approve'] = outcome['approve'];
    if (Wire.text(outcome, 'feedback')) safe['feedback'] = Wire.text(outcome, 'feedback');
  } else if (request.kind === 'permission') {
    if (typeof outcome['allow'] === 'boolean') safe['allow'] = outcome['allow'];
  } else if (request.kind === 'screen') {
    const option = Wire.text(raw, 'option');
    if (Wire.text(outcome, 'option_label') && interactionScreenOptions(request).some((choice: InteractionChoice) => choice.key === option)) safe['option'] = option;
    const options = Wire.strings(raw, 'options').filter((key: string) => interactionScreenOptions(request).some((choice: InteractionChoice) => choice.key === key));
    if (options.length) safe['options'] = options;
    if (Wire.text(outcome, 'text')) safe['text'] = Wire.text(outcome, 'text');
  } else if (request.kind === 'elicitation' && Wire.text(outcome, 'action')) {
    safe['action'] = Wire.text(outcome, 'action');
  }
  return safe;
}

/** A bounded, in-memory history for only the selected native session. */
@ObservedV2
export class InteractionHistoryModel {
  @Trace rows: InteractionHistoryEntry[] = [];
  @Trace loading: boolean = false;
  @Trace error: string = '';
  @Trace selectedId: string = '';

  private client: ClientModel | undefined;
  private repository: HubRepository | undefined;
  private generation: number = -1;
  private scope: string = '';
  private target: string = '';
  private selectedKey: string = '';
  private machine: string = '';
  private session: string = '';
  private owner: string = '';
  private identityKey: string = '';
  private run: number = 0;
  private loaded: boolean = false;
  private needsRefresh: boolean = true;
  private task: Promise<void> | undefined;
  private taskRun: number = -1;

  /** Includes the connection, manager, and selected row so a sheet cannot survive an owner/session change. */
  identity(model: ClientModel): string {
    const row = model.selected;
    const id = sessionId(row);
    if (!['session', 'conversation'].includes(row.kind) || !row.machine || !id) return '';
    return JSON.stringify([model.connectionGeneration(), model.scope(), model.target(row), row.key(), row.machine, id,
      row.manager || model.profile.manager]);
  }

  async open(model: ClientModel, active: boolean = true, force: boolean = false): Promise<void> {
    if (!active) { this.hide(); return; }
    const row = model.selected;
    const id = sessionId(row), owner = row.manager || model.profile.manager;
    const identity = this.identity(model);
    if (!identity || owner !== model.profile.manager) {
      this.hide(); this.clear(); return;
    }
    if (identity !== this.identityKey) {
      this.invalidate(); this.clear(); this.identityKey = identity;
      this.client = model; this.generation = model.connectionGeneration(); this.scope = model.scope();
      this.target = model.target(row); this.selectedKey = row.key();
      this.machine = row.machine; this.session = id; this.owner = owner;
      this.needsRefresh = true;
    } else {
      this.client = model;
    }
    if (!model.connected || model.demo) {
      this.invalidate(); this.needsRefresh = true; this.loading = false; this.error = '';
      this.repository = undefined;
      return;
    }
    let repository: HubRepository;
    try { repository = model.repo(); }
    catch (_) { this.invalidate(); this.needsRefresh = true; this.loading = false; this.error = ''; this.repository = undefined; return; }
    if (repository !== this.repository) {
      this.invalidate(); this.repository = repository; this.needsRefresh = true;
    }
    if (!force && this.loaded && !this.needsRefresh) return;
    if (this.task && this.taskRun === this.run) return this.task;
    const run = ++this.run;
    this.taskRun = run;
    const currentRepository = repository;
    this.loading = true; this.error = '';
    const task = this.load(model, currentRepository, run);
    this.task = task;
    try { await task; }
    finally {
      if (this.task === task) { this.task = undefined; this.taskRun = -1; }
    }
  }

  /** Cached destinations call hide while inactive; late replies are retired and the inspector is dismissed. */
  hide(clear: boolean = false): void {
    this.invalidate(); this.selectedId = ''; this.needsRefresh = true; this.loading = false; this.error = '';
    this.client = undefined; this.repository = undefined;
    if (clear) this.clear();
  }

  async refresh(): Promise<void> {
    const client = this.client;
    if (!client) return;
    await this.open(client, true, true);
  }

  private clear(): void {
    this.rows = []; this.loading = false; this.error = ''; this.selectedId = '';
    this.client = undefined; this.repository = undefined; this.identityKey = '';
    this.generation = -1; this.scope = ''; this.target = ''; this.selectedKey = '';
    this.machine = ''; this.session = ''; this.owner = ''; this.loaded = false; this.needsRefresh = true;
  }

  private invalidate(): void {
    this.run++; this.task = undefined; this.taskRun = -1;
  }

  private current(model: ClientModel, repository: HubRepository, run: number): boolean {
    if (run !== this.run || !model.connected || model.demo || !model.owns(this.generation, this.scope) ||
      model.connectionGeneration() !== this.generation || model.scope() !== this.scope || model.target() !== this.target ||
      model.selected.key() !== this.selectedKey || this.identity(model) !== this.identityKey || this.repository !== repository) return false;
    try { return model.repo() === repository; } catch (_) { return false; }
  }

  private async load(model: ClientModel, repository: HubRepository, run: number): Promise<void> {
    const path = '/api/v1/interactions/history?machine_id=' + encodeURIComponent(this.machine) +
      '&session_id=' + encodeURIComponent(this.session) + '&limit=100&manager=' + encodeURIComponent(this.owner);
    try {
      const data = await repository.call(path);
      if (!this.current(model, repository, run)) return;
      if (!Array.isArray(data['interactions'])) throw new Error('history_data_invalid');
      const rows: InteractionHistoryEntry[] = [];
      Wire.rows(data, 'interactions').slice(0, 100).forEach((record: Record<string, Object>) => {
        const source = Wire.object(record, 'interaction');
        const id = Wire.text(source, 'id');
        const machine = Wire.text(source, 'machine_id');
        const session = Wire.text(source, 'session_id');
        const manager = Wire.text(source, 'manager');
        if (!id || machine && machine !== this.machine || session && session !== this.session || manager && manager !== this.owner) return;
        if (!machine || !session) return;
        if (!manager) source['manager'] = this.owner;
        const initial = decodeInteractionDecision(historyEnvelope(record, source, Wire.object(record, 'resolution')));
        const outcome = interactionHistoryResolution(initial);
        const safe = redactedWireResolution(initial, record, outcome);
        const request = decodeInteractionDecision(historyEnvelope(record, source, safe));
        const entry = new InteractionHistoryEntry();
        entry.request = request; entry.outcome = outcome; entry.status = request.status; entry.resolvedAt = Wire.text(record, 'resolved_at');
        rows.push(entry);
      });
      this.rows = rows; this.loaded = true; this.needsRefresh = false; this.error = '';
    } catch (_) {
      if (this.current(model, repository, run)) { this.error = 'failed'; this.loaded = false; this.needsRefresh = true; }
    } finally {
      if (this.current(model, repository, run)) this.loading = false;
    }
  }
}
