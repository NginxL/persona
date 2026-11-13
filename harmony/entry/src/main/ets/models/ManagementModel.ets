import { ClientModel } from './ClientModel';
import { ResourceRow, Wire } from './Wire';
import { HttpFailure } from '../platform/HubTransport';
import { findScheduleTemplate } from '../core/ScheduleTemplates';

function segment(value: string): string { return encodeURIComponent(value); }

export class PurposeDraft {
  provider: string = '';
  model: string = '';
  effort: string = '';
  constructor(provider: string = '', model: string = '', effort: string = '') {
    this.provider = provider; this.model = model; this.effort = effort;
  }
}

function jsonNull(): Object { return Wire.parse('{"value":null}')['value']; }

export class ManagementRow {
  id: string = '';
  title: string = '';
  detail: string = '';
  raw: Record<string, Object> = {};
  group: string = '';
  manager: string = '';
  forgetEpoch: number = -1;
  scope: string = '';
  connectionGeneration: number = -1;
  constructor(id: string = '', title: string = '', detail: string = '', raw: Record<string, Object> = {}) {
    this.id = id; this.title = title; this.detail = detail; this.raw = raw;
  }
}

interface ScheduleOwnerLease {
  group: string;
  manager: string;
  forgetEpoch: number;
  scope: string;
  connectionGeneration: number;
}

@ObservedV2
export class ScheduleDraft {
  @Trace id: string = '';
  @Trace title: string = '';
  @Trace prompt: string = '';
  @Trace repeat: string = 'once';
  @Trace at: string = '';
  @Trace time: string = '';
  @Trace weekdays: string = '';
  @Trace everyMinutes: string = '60';
  @Trace enabled: boolean = true;
  @Trace sessionId: string = '';
  @Trace permissionMode: string = '';
  @Trace push: string = '';
  constructor(row: Record<string, Object> = {}) {
    this.id = Wire.text(row, 'id'); this.title = Wire.text(row, 'title'); this.prompt = Wire.text(row, 'prompt');
    this.repeat = Wire.text(row, 'repeat', 'once'); this.at = Wire.text(row, 'at'); this.time = Wire.text(row, 'time');
    const weekdays = row['weekdays'];
    this.weekdays = Array.isArray(weekdays) ? (weekdays as Object[]).map((value: Object) => String(value)).join(',') : '';
    this.everyMinutes = String(Wire.number(row, 'every_minutes', 60)); this.enabled = Wire.bool(row, 'enabled', true);
    this.sessionId = Wire.text(row, 'session_id');
    this.permissionMode = Wire.text(row, 'permission_mode'); this.push = Wire.text(row, 'push');
  }
}

@ObservedV2
export class ManagementInput {
  @Trace key: string = '';
  @Trace label: string = '';
  @Trace placeholder: string = '';
  @Trace value: string = '';
  @Trace secret: boolean = false;
  @Trace kind: string = 'text';
  @Trace options: string[] = [];
  constructor(key: string = '', label: string = '', placeholder: string = '', secret: boolean = false, value: string = '') {
    this.key = key; this.label = label; this.placeholder = placeholder; this.secret = secret; this.value = value;
  }
}

@ObservedV2
export class ManagementModel {
  @Trace page: string = '';
  @Trace loading: boolean = false;
  @Trace saving: boolean = false;
  @Trace error: string = '';
  @Trace rows: ManagementRow[] = [];
  @Trace data: Record<string, Object> = {};
  @Trace selectedId: string = '';
  @Trace purpose: Record<string, Object> = {};
  @Trace providers: Record<string, Object>[] = [];
  @Trace catalogPurposes: Record<string, Object>[] = [];
  @Trace purposeDraft: PurposeDraft = new PurposeDraft();
  @Trace providerModels: Record<string, Object>[] = [];
  @Trace pairing: Record<string, Object> = {};
  @Trace catalogRows: ManagementRow[] = [];
  @Trace catalogMarkets: Record<string, Object>[] = [];
  @Trace catalogMarket: string = '';
  @Trace catalogQuery: string = '';
  @Trace catalogSelectedId: string = '';
  @Trace catalogSelectedMarket: string = '';
  @Trace preview: Record<string, Object> = {};
  @Trace operation: Record<string, Object> = {};
  @Trace extensionDetail: Record<string, Object> = {};
  @Trace extensionDetailMissing: boolean = false;
  @Trace extensionConfigFields: ManagementInput[] = [];
  @Trace extensionSecretFields: ManagementInput[] = [];
  @Trace extensionSchemaSupported: boolean = true;
  @Trace auth: Record<string, Object> = {};
  @Trace authPrompt: Record<string, Object> = {};
  @Trace authFields: ManagementInput[] = [];
  @Trace connectorPrompt: Record<string, Object> = {};
  @Trace connectorFields: ManagementInput[] = [];
  @Trace channelDetail: Record<string, Object> = {};
  @Trace channelPrompt: Record<string, Object> = {};
  @Trace channelFields: ManagementInput[] = [];
  @Trace scheduleDraft: ScheduleDraft = new ScheduleDraft();
  @Trace scheduleRuns: ManagementRow[] = [];
  @Trace lastRunReceipt: Record<string, Object> = {};
  private generation: number = 0;
  private clientGeneration: number = -1;
  private modelsRequest: number = 0;
  private scope: string = '';
  private selectedTarget: string = '';
  private owner: string = '';
  private group: string = '';
  private ownerForgetEpoch: number = 0;
  private receiptGroup: string = '';
  private receiptManager: string = '';
  private receiptForgetEpoch: number = -1;
  private receiptTitle: string = '';
  private scheduleDraftLease: ScheduleOwnerLease | undefined;

  private admission: (() => boolean) | undefined;
  setAdmission(admit: () => boolean): void { this.admission = admit; }
  private active(model: ClientModel, generation: number, scope: string, target: string = ''): boolean {
    return (!this.admission || this.admission()) && model.owns(this.clientGeneration, scope) && this.generation === generation && this.scope === scope &&
      (!target || model.target() === target);
  }

  private scheduleForgetEpoch(model: ClientModel, group: string): number {
    return model.forgottenProfile === group ? model.forgottenEpoch : 0;
  }

  private captureScheduleLease(): ScheduleOwnerLease | undefined {
    if (this.page !== 'schedules' || !this.group || !this.scope) return undefined;
    return { group: this.group, manager: this.owner, forgetEpoch: this.ownerForgetEpoch, scope: this.scope,
      connectionGeneration: this.clientGeneration };
  }

  private scheduleDraftLeaseMatchesLoaded(): boolean {
    const lease = this.scheduleDraftLease;
    return !!lease && this.page === 'schedules' && lease.group === this.group && lease.manager === this.owner &&
      lease.forgetEpoch === this.ownerForgetEpoch && lease.scope === this.scope && lease.connectionGeneration === this.clientGeneration;
  }

  private scheduleDraftCurrent(model: ClientModel): boolean {
    const lease = this.scheduleDraftLease;
    return this.scheduleDraftLeaseMatchesLoaded() && !!lease && this.active(model, this.generation, this.scope) &&
      lease.group === model.profile.id && lease.manager === model.profile.manager &&
      lease.forgetEpoch === this.scheduleForgetEpoch(model, lease.group) && lease.scope === model.scope() &&
      lease.connectionGeneration === model.connectionGeneration();
  }

  private scheduleDraftTaskIdentityCurrent(): boolean {
    const lease = this.scheduleDraftLease, id = this.scheduleDraft.id;
    if (!id) return true;
    return !!lease && this.scheduleDraftLeaseMatchesLoaded() && this.rows.some((row: ManagementRow) => row.id === id &&
      row.group === lease.group && row.manager === lease.manager && row.forgetEpoch === lease.forgetEpoch &&
      row.scope === lease.scope && row.connectionGeneration === lease.connectionGeneration);
  }

  private scheduleTaskRowCurrent(model: ClientModel, row: ManagementRow): boolean {
    return this.page === 'schedules' && this.rows.includes(row) && row.group === this.group && row.manager === this.owner &&
      row.forgetEpoch === this.ownerForgetEpoch && row.scope === this.scope && row.connectionGeneration === this.clientGeneration &&
      row.connectionGeneration === model.connectionGeneration() && this.scope === model.scope() &&
      this.group === model.profile.id && this.owner === model.profile.manager &&
      this.ownerForgetEpoch === this.scheduleForgetEpoch(model, this.group) && this.active(model, this.generation, this.scope);
  }

  /** Keep the native page lease through the durable journal's final dispatch boundary. */
  private async call(model: ClientModel, path: string, method: string = 'GET', body: Record<string, Object> = {},
    operation: string = '', admit?: () => boolean, completedAsAck: boolean = false): Promise<Record<string, Object>> {
    if (method === 'GET') return model.repo().call(path, method, body, operation, admit, completedAsAck);
    const generation = this.generation, scope = this.scope, owner = this.owner, page = this.page,
      selected = this.selectedId, target = this.selectedTarget, connection = model.connectionGeneration();
    const captured = Wire.parse(JSON.stringify(body));
    return model.repo().call(path, method, captured, operation, (): boolean =>
      this.active(model, generation, scope) && this.page === page && this.owner === owner && model.profile.manager === owner &&
      this.selectedId === selected && this.selectedTarget === target && model.connectionGeneration() === connection && (!admit || admit()), completedAsAck);
  }

  private async reloadConnectors(model: ClientModel): Promise<void> {
    const id = this.selectedId, scope = this.scope, owner = this.owner, generation = this.generation;
    await this.load('connectors', model);
    if (!id || this.generation !== generation + 1 || !this.active(model, this.generation, scope) || this.owner !== owner) return;
    const row = this.rows.find((item: ManagementRow) => item.id === id);
    if (row) this.openConnector(row);
    else if (!this.error) this.error = 'not_found';
  }

  private async reloadExtensions(model: ClientModel): Promise<void> {
    const id = this.selectedId, scope = this.scope, owner = this.owner, generation = this.generation;
    await this.load('extensions', model);
    if (!id || this.generation !== generation + 1 || !this.active(model, this.generation, scope) || this.owner !== owner) return;
    const row = this.rows.find((item: ManagementRow) => item.id === id);
    if (row) await this.openExtension(model, row);
    else if (!this.error) this.extensionDetailMissing = true;
  }

  private async read(path: string, model: ClientModel): Promise<Record<string, Object>> {
    return this.call(model, path);
  }

  private fail(error: Error): void {
    if (error instanceof HttpFailure) {
      this.error = error.resultPending ? 'pending' : error.resultUnknown ? 'result_unknown' : error.status === 404 ? 'unsupported' :
        error.status === 409 || error.status === 410 ? 'request_updated' : 'failed';
    } else this.error = 'failed';
  }

  async load(page: string, model: ClientModel): Promise<void> {
    const group = model.profile.id, owner = model.profile.manager, forgetEpoch = this.scheduleForgetEpoch(model, model.profile.id),
      scope = model.scope(), draftLease = this.scheduleDraftLease;
    const keepScheduleReceipt = page === 'schedules' && this.page === 'schedules' && this.group === group &&
      this.owner === owner && this.ownerForgetEpoch === forgetEpoch && this.scope === scope;
    const keepScheduleDraft = page === 'schedules' && this.page === 'schedules' && this.group === group &&
      this.owner === owner && this.ownerForgetEpoch === forgetEpoch && this.scope === scope && !!draftLease &&
      draftLease.group === group && draftLease.manager === owner && draftLease.forgetEpoch === forgetEpoch &&
      draftLease.scope === scope && draftLease.connectionGeneration === model.connectionGeneration();
    if (!keepScheduleReceipt) this.clearScheduleReceipt();
    if (!keepScheduleDraft) { this.scheduleDraft = new ScheduleDraft(); this.scheduleDraftLease = undefined; }
    const generation = ++this.generation;
    this.clientGeneration = model.connectionGeneration();
    this.page = page; this.scope = scope; this.group = group; this.owner = owner; this.ownerForgetEpoch = forgetEpoch;
    this.selectedTarget = page === 'session_permissions' || page === 'session_effort' ? model.target() : '';
    this.error = ''; this.loading = true; this.saving = false; this.rows = []; this.data = {}; this.purpose = {}; this.providers = [];
    this.catalogPurposes = []; this.providerModels = []; this.selectedId = page === 'translation' ? 'kb' : '';
    this.catalogRows = []; this.catalogMarkets = []; this.preview = {}; this.operation = {}; this.extensionDetail = {}; this.extensionDetailMissing = false;
    this.extensionConfigFields = []; this.extensionSecretFields = []; this.auth = {}; this.authPrompt = {}; this.authFields = [];
    this.extensionSchemaSupported = true;
    this.connectorPrompt = {}; this.connectorFields = []; this.channelDetail = {}; this.channelPrompt = {}; this.channelFields = [];
    this.scheduleRuns = [];
    try {
      if (page === 'appearance' || page === 'notifications' || page === 'privacy' || page === 'diagnostics' || page === 'phone') return;
      if (page === 'models' || page === 'translation') {
        const catalog = await this.read('/api/v1/model-config', model);
        if (!this.active(model, generation, scope)) return;
        this.data = catalog;
        this.catalogPurposes = Wire.rows(catalog, 'purposes');
        const preferred = page === 'translation' ? 'kb' : 'assistant';
        const selected = this.catalogPurposes.find((row: Record<string, Object>) => Wire.text(row, 'id') === (this.selectedId || preferred)) || this.catalogPurposes[0];
        if (selected) await this.loadPurpose(model, selected, generation, scope);
      } else if (page === 'session_permissions' || page === 'session_effort') {
        this.data = model.selected.data();
        this.selectedId = model.selected.id;
      } else if (page === 'machines') {
        const result = await this.read('/api/v1/machines', model);
        if (!this.active(model, generation, scope)) return;
        this.data = result; this.rows = Wire.rows(result, 'machines').map((item: Record<string, Object>) =>
          new ManagementRow(Wire.text(item, 'id'), Wire.text(item, 'name', Wire.text(item, 'id')),
            Wire.text(item, 'error', Wire.text(item, 'state')), item));
        if (!this.rows.some((row: ManagementRow) => row.id === this.selectedId)) this.selectedId = this.rows[0]?.id || '';
      } else if (page === 'schedules') {
        const result = await this.read('/api/v1/assistant/tasks', model);
        if (!this.active(model, generation, scope) || this.scheduleForgetEpoch(model, group) !== forgetEpoch) return;
        this.data = result; this.rows = Wire.rows(result, 'tasks').map((item: Record<string, Object>) => {
          const row = new ManagementRow(Wire.text(item, 'id'), Wire.text(item, 'title', Wire.text(item, 'id')),
            Wire.text(item, 'next_run'), item);
          row.group = group; row.manager = owner; row.forgetEpoch = forgetEpoch; row.scope = scope;
          row.connectionGeneration = this.clientGeneration;
          return row;
        });
        if (this.scheduleDraft.id && !this.scheduleDraftTaskIdentityCurrent()) {
          this.scheduleDraft = new ScheduleDraft(); this.scheduleDraftLease = undefined; this.selectedId = '';
        }
        try {
          const runs = await this.read('/api/v1/assistant/runs', model);
          if (!this.active(model, generation, scope) || this.scheduleForgetEpoch(model, group) !== forgetEpoch) return;
          this.scheduleRuns = Wire.rows(runs, 'runs').map((item: Record<string, Object>) => {
            const row = new ManagementRow(Wire.text(item, 'id'), Wire.text(item, 'title', Wire.text(item, 'kind')),
              Wire.text(item, 'status', Wire.text(item, 'error')), item);
            row.group = group; row.manager = owner; row.forgetEpoch = forgetEpoch; row.scope = scope;
            return row;
          });
        } catch (_) { if (this.active(model, generation, scope)) this.scheduleRuns = []; }
      } else if (page === 'triage') {
        const result = await this.read('/api/v1/agents/triage', model);
        if (!this.active(model, generation, scope)) return; this.data = result;
      } else if (page === 'extensions') {
        const result = await this.read('/api/v1/extensions', model);
        if (!this.active(model, generation, scope)) return;
        this.data = result; this.rows = Wire.rows(result, 'extensions').map((item: Record<string, Object>) =>
          new ManagementRow(Wire.text(item, 'id'), Wire.text(item, 'name', Wire.text(item, 'id')),
            Wire.text(item, 'status'), item));
        if (model.features.includes('extension_operations')) {
          try {
            const operations = await this.read('/api/v1/extensions/-/operations', model);
            if (!this.active(model, generation, scope)) return;
            const latest = Wire.rows(operations, 'operations')[0];
            if (latest) { this.operation = latest; if (Wire.text(latest, 'state') === 'needs_consent') await this.refreshExtensionOperation(model, Wire.text(latest, 'id')); }
          } catch (_) { if (this.active(model, generation, scope)) this.operation = {}; }
        }
      } else if (page === 'connectors') {
        const result = await this.read('/api/v1/connectors?all=1', model);
        if (!this.active(model, generation, scope)) return;
        this.data = result; this.rows = Wire.rows(result, 'connectors').map((item: Record<string, Object>) =>
          new ManagementRow(Wire.text(item, 'id'), Wire.text(item, 'name', Wire.text(item, 'id')),
            Wire.text(item, 'status'), item));
      } else if (page === 'channels') {
        const result = await this.read('/api/v1/channels', model);
        if (!this.active(model, generation, scope)) return;
        this.data = result; this.rows = Wire.rows(result, 'channels').map((item: Record<string, Object>) =>
          new ManagementRow(Wire.text(item, 'id'), Wire.text(item, 'name', Wire.text(item, 'id')),
            Wire.text(item, 'status'), item));
      } else if (page === 'ledger') {
        const result = await this.read('/api/v1/connectors/sent?limit=50', model);
        if (!this.active(model, generation, scope)) return;
        this.data = result; this.rows = Wire.rows(result, 'items').map((item: Record<string, Object>) => {
          const target = Wire.object(item, 'target');
          return new ManagementRow(Wire.identifier(item, 'id'), Wire.text(item, 'connector'),
            Wire.text(target, 'to', Wire.text(target, 'subject', Wire.text(item, 'summary'))), item);
        });
      }
    } catch (error) { if (this.active(model, generation, scope)) this.fail(error as Error); }
    finally { if (this.active(model, generation, scope)) this.loading = false; }
  }

  private async loadPurpose(model: ClientModel, selected: Record<string, Object>, generation: number, scope: string): Promise<void> {
    const id = Wire.text(selected, 'id'); if (!id) return;
    const detail = await this.call(model, '/api/v1/model-config/purposes/' + segment(id));
    if (!this.active(model, generation, scope)) return;
    const purpose = Wire.object(detail, 'purpose');
    this.selectedId = id; this.purpose = purpose; this.providers = Wire.rows(detail, 'providers');
    this.purposeDraft = new PurposeDraft(Wire.text(Wire.object(purpose, 'override'), 'provider'),
      Wire.text(Wire.object(purpose, 'override'), 'model'), Wire.text(Wire.object(purpose, 'override'), 'effort'));
    await this.loadPurposeModels(model, generation, scope);
  }

  async selectPurpose(model: ClientModel, id: string): Promise<void> {
    if (!this.active(model, this.generation, this.scope) || this.saving) return;
    const selected = this.catalogPurposes.find((item: Record<string, Object>) => Wire.text(item, 'id') === id);
    if (!selected) return;
    const generation = ++this.generation, scope = model.scope(); this.scope = scope;
    this.error = ''; this.loading = true; this.selectedId = id;
    try { await this.loadPurpose(model, selected, generation, scope); }
    catch (error) { if (this.active(model, generation, scope)) this.fail(error as Error); }
    finally { if (this.active(model, generation, scope)) this.loading = false; }
  }

  private supports(field: string): boolean {
    const fields = this.purpose['fields'];
    return Array.isArray(fields) ? (fields as Object[]).includes(field) : field === 'provider' || field === 'model';
  }

  draftValue(field: string): string {
    if (field === 'provider') return this.purposeDraft.provider;
    if (field === 'model') return this.purposeDraft.model;
    if (field === 'effort') return this.purposeDraft.effort;
    return '';
  }

  purposeChanged(): boolean {
    const override = Wire.object(this.purpose, 'override');
    return ['provider', 'model', 'effort'].some((field: string) => Wire.text(override, field) !== this.draftValue(field));
  }

  async loadPurposeModels(model: ClientModel, generation: number = this.generation, scope: string = this.scope): Promise<void> {
    if (!this.active(model, this.generation, this.scope)) return;
    const request = ++this.modelsRequest, purpose = this.selectedId;
    const providerId = this.purposeDraft.provider || Wire.text(Wire.object(this.purpose, 'effective'), 'provider');
    const provider = this.providers.find((item: Record<string, Object>) => Wire.text(item, 'id') === providerId);
    if (!provider || Wire.text(provider, 'kind', 'launcher') !== 'launcher' || !this.supports('model')) {
      this.providerModels = []; return;
    }
    const machine = Wire.text(this.purpose, 'machine_id');
    const path = '/api/v1/model-config/providers/' + segment(providerId) + '/models' + (machine ? '?machine=' + segment(machine) : '');
    try {
      const result = await this.read(path, model);
      if (!this.purposeModelsOwner(model, generation, scope, request, purpose, providerId, machine)) return;
      this.providerModels = Wire.rows(result, 'models');
    } catch (error) { if (this.purposeModelsOwner(model, generation, scope, request, purpose, providerId, machine)) { this.providerModels = []; this.fail(error as Error); } }
  }

  private purposeModelsOwner(model: ClientModel, generation: number, scope: string, request: number, purpose: string, provider: string, machine: string): boolean {
    return this.active(model, generation, scope) && this.modelsRequest === request && this.selectedId === purpose &&
      (this.purposeDraft.provider || Wire.text(Wire.object(this.purpose, 'effective'), 'provider')) === provider && Wire.text(this.purpose, 'machine_id') === machine;
  }

  changePurposeDraft(key: string, value: string): void {
    if (!['provider', 'model', 'effort'].includes(key) || !this.supports(key)) return;
    const next = new PurposeDraft(this.purposeDraft.provider, this.purposeDraft.model, this.purposeDraft.effort);
    if (key === 'provider' && value !== next.provider) { next.model = ''; next.effort = ''; }
    if (key === 'provider') next.provider = value;
    if (key === 'model') next.model = value;
    if (key === 'effort') next.effort = value;
    this.purposeDraft = next;
    if (key === 'provider') { this.modelsRequest++; this.providerModels = []; }
  }

  async savePurpose(model: ClientModel): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    if (!this.selectedId || !Object.keys(this.purpose).length || this.saving) return false;
    const generation = this.generation, scope = this.scope, id = this.selectedId, purpose = this.purpose;
    const patchKeys: string[] = ['revision'];
    const patchValues: Object[] = [purpose['revision']];
    for (const key of ['provider', 'model', 'effort']) {
      if (!this.supports(key)) continue;
      patchKeys.push(key);
      const value = this.draftValue(key); patchValues.push(value ? value : jsonNull());
    }
    const body = Wire.payload(patchKeys, patchValues);
    this.saving = true; this.error = '';
    try {
      await this.call(model, '/api/v1/model-config/purposes/' + segment(id), 'PATCH', body);
      if (!this.active(model, generation, scope)) return false;
      this.saving = false;
      await this.selectPurpose(model, id);
      return this.active(model, this.generation, scope) && !this.error;
    } catch (error) { if (this.active(model, generation, scope)) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope)) this.saving = false; }
  }

  async saveMode(model: ClientModel, field: string, value: string): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const row = model.selected, generation = this.generation, scope = model.scope(), target = model.target(row);
    const capabilities = Wire.object(row.data(), 'capabilities');
    if (row.kind !== 'session' || !Wire.bool(capabilities, 'modes') || !['permission_mode', 'effort'].includes(field)) return false;
    if (field === 'effort' && !Wire.strings(row.data(), 'efforts').includes(value)) return false;
    const allowedModes = ['default', 'acceptEdits', 'plan', 'auto'];
    if (field === 'permission_mode' && !(allowedModes.includes(value) || value === 'bypassPermissions' && Wire.bool(row.data(), 'bypass_available'))) return false;
    this.saving = true; this.error = '';
    try {
      const body = Wire.payload([field], [value]);
      await this.call(model, '/api/v1/sessions/' + segment(row.machine) + '/' + segment(row.session) + '/modes', 'POST', body);
      if (!this.active(model, generation, scope, target)) return false;
      await model.refresh();
      return true;
    } catch (error) { if (this.active(model, generation, scope, target)) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope, target)) this.saving = false; }
  }

  async setTriage(model: ClientModel, switches: Record<string, Object>): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    if (this.saving || this.page !== 'triage') return false;
    const generation = this.generation, scope = this.scope; this.saving = true; this.error = '';
    try {
      const result = await this.call(model, '/api/v1/agents/triage', 'PUT', switches);
      if (!this.active(model, generation, scope)) return false;
      this.data = result; return true;
    } catch (error) { if (this.active(model, generation, scope)) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope)) this.saving = false; }
  }

  async setScheduleEnabled(model: ClientModel, row: ManagementRow, enabled: boolean): Promise<boolean> {
    if (!this.scheduleTaskRowCurrent(model, row)) return false;
    const generation = this.generation, scope = this.scope, owner = model.profile.manager, id = row.id;
    if (!this.taskId(id) || this.saving) return false;
    const draft = new ScheduleDraft(row.raw); draft.enabled = enabled;
    const weekdays = this.scheduleWeekdays(draft.weekdays);
    if (weekdays === undefined) return false;
    const keys = ['id', 'title', 'prompt', 'repeat', 'time', 'weekdays', 'every_minutes', 'enabled', 'session_id', 'permission_mode'];
    const values: Object[] = [id, draft.title, draft.prompt, draft.repeat, draft.repeat === 'daily' ? draft.time : '', weekdays,
      draft.repeat === 'interval' ? Wire.number(row.raw, 'every_minutes') : 0, enabled, draft.sessionId, draft.permissionMode];
    if (this.modelHasTaskPush(model) && draft.push) { keys.push('push'); values.push(draft.push); }
    if (draft.repeat === 'once' && draft.at) { keys.push('at'); values.push(draft.at); }
    this.saving = true; this.error = '';
    try {
      await this.call(model, '/api/v1/assistant/tasks/' + segment(id), 'PUT', Wire.payload(keys, values));
      if (!this.active(model, generation, scope) || model.profile.manager !== owner || !this.scheduleTaskRowCurrent(model, row)) return false;
      this.saving = false;
      await this.load('schedules', model); return this.error === '';
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.saving = false; }
  }

  beginSchedule(row?: ManagementRow): void {
    this.error = '';
    const lease = this.captureScheduleLease();
    const currentRow = !row || this.rows.includes(row) && row.group === this.group && row.manager === this.owner &&
      row.forgetEpoch === this.ownerForgetEpoch && row.scope === this.scope && row.connectionGeneration === this.clientGeneration;
    if (!lease || !currentRow) {
      this.selectedId = ''; this.scheduleDraft = new ScheduleDraft(); this.scheduleDraftLease = undefined;
      return;
    }
    this.selectedId = row?.id || '';
    this.scheduleDraft = new ScheduleDraft(row?.raw || {});
    this.scheduleDraftLease = lease;
  }

  /** Apply a native template to the editable draft only; saving remains an explicit user action. */
  applyScheduleTemplate(id: string, title: string, prompt: string): boolean {
    if (!this.scheduleDraftLeaseMatchesLoaded() || this.saving || this.scheduleDraft.id) return false;
    const template = findScheduleTemplate(id);
    if (!template || !title.trim() || !prompt.trim()) return false;
    const draft = new ScheduleDraft();
    draft.title = title; draft.prompt = prompt; draft.repeat = template.repeat; draft.at = '';
    draft.time = template.time; draft.weekdays = template.weekdays.join(','); draft.everyMinutes = String(template.everyMinutes);
    this.scheduleDraft = draft; this.selectedId = ''; this.error = '';
    return true;
  }

  /** Discard the unsubmitted native schedule draft without contacting the hub. */
  cancelSchedule(): boolean {
    if (this.page !== 'schedules' || this.saving) return false;
    this.scheduleDraft = new ScheduleDraft(); this.scheduleDraftLease = undefined; this.selectedId = ''; this.error = '';
    return true;
  }

  changeSchedule(field: string, value: string | boolean): void {
    const draft = this.scheduleDraft;
    if (field === 'title' && typeof value === 'string') draft.title = value;
    else if (field === 'prompt' && typeof value === 'string') draft.prompt = value;
    else if (field === 'repeat' && typeof value === 'string' && ['once', 'daily', 'interval'].includes(value)) draft.repeat = value;
    else if (field === 'at' && typeof value === 'string') draft.at = value;
    else if (field === 'time' && typeof value === 'string') draft.time = value;
    else if (field === 'weekdays' && typeof value === 'string') draft.weekdays = value;
    else if (field === 'every_minutes' && typeof value === 'string') draft.everyMinutes = value;
    else if (field === 'enabled' && typeof value === 'boolean') draft.enabled = value;
  }

  private taskId(id: string): boolean { return !!id && !['.', '..'].includes(id) && !id.includes('/') && !id.includes('\\'); }

  private scheduleWeekdays(value: string): number[] | undefined {
    if (!value.trim()) return [];
    const parts = value.split(',').map((part: string) => part.trim());
    if (parts.some((part: string) => !/^[1-7]$/.test(part))) return undefined;
    const days = parts.map((part: string) => Number(part));
    return new Set(days).size === days.length ? days : undefined;
  }

  scheduleValid(): boolean {
    const draft = this.scheduleDraft;
    if (!draft.title.trim() || !draft.prompt.trim() || !['once', 'daily', 'interval'].includes(draft.repeat)) return false;
    if (draft.id && !this.taskId(draft.id)) return false;
    if (draft.repeat === 'once' && (!draft.at.trim() || !Number.isFinite(Date.parse(draft.at)))) return false;
    if (draft.repeat === 'daily' && !/^([01]\d|2[0-3]):[0-5]\d$/.test(draft.time)) return false;
    if (this.scheduleWeekdays(draft.weekdays) === undefined) return false;
    const minutes = Number(draft.everyMinutes);
    return draft.repeat !== 'interval' || Number.isInteger(minutes) && minutes >= 1 && minutes <= 10080;
  }

  async saveSchedule(model: ClientModel): Promise<boolean> {
    if (!this.scheduleDraftCurrent(model)) return false;
    const generation = this.generation, scope = this.scope, lease = this.scheduleDraftLease;
    if (!lease) return false;
    const owner = lease.manager;
    const draft = this.scheduleDraft, id = draft.id;
    if (this.page !== 'schedules' || this.scope !== scope || this.owner !== owner || this.saving || !this.scheduleValid() ||
      !this.scheduleDraftTaskIdentityCurrent() ||
      id && !this.taskId(id)) return false;
    if (draft.repeat === 'once' && (!draft.at.trim() || !Number.isFinite(Date.parse(draft.at)))) return false;
    if (draft.repeat === 'daily' && !/^([01]\d|2[0-3]):[0-5]\d$/.test(draft.time)) return false;
    const weekdays = this.scheduleWeekdays(draft.weekdays);
    if (weekdays === undefined) return false;
    const minutes = Number(draft.everyMinutes);
    if (draft.repeat === 'interval' && (!Number.isInteger(minutes) || minutes < 1 || minutes > 10080)) return false;
    const keys = ['title', 'prompt', 'repeat', 'time', 'weekdays', 'every_minutes', 'enabled', 'session_id', 'permission_mode'];
    const values: Object[] = [draft.title.trim(), draft.prompt, draft.repeat, draft.repeat === 'daily' ? draft.time : '', weekdays,
      draft.repeat === 'interval' ? minutes : 0, draft.enabled, draft.sessionId, draft.permissionMode];
    if (id) { keys.push('id'); values.push(id); }
    if (this.modelHasTaskPush(model) && draft.push) { keys.push('push'); values.push(draft.push); }
    if (draft.repeat === 'once') { keys.push('at'); values.push(draft.at.trim()); }
    const body = Wire.payload(keys, values);
    const path = '/api/v1/assistant/tasks' + (id ? '/' + segment(id) : '');
    this.saving = true; this.error = '';
    try {
      await this.call(model, path, id ? 'PUT' : 'POST', body);
      if (!this.active(model, generation, scope) || !this.scheduleDraftCurrent(model) || model.profile.manager !== owner) return false;
      this.saving = false;
      await this.load('schedules', model);
      return this.error === '';
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.saving = false; }
  }

  private modelHasTaskPush(model: ClientModel): boolean { return model.features.includes('task_push'); }

  async deleteSchedule(model: ClientModel, row: ManagementRow): Promise<boolean> {
    if (!this.scheduleTaskRowCurrent(model, row)) return false;
    const generation = this.generation, scope = this.scope, owner = model.profile.manager, id = row.id;
    if (!this.taskId(id) || this.saving) return false;
    this.saving = true; this.error = '';
    try {
      await this.call(model, '/api/v1/assistant/tasks/' + segment(id), 'DELETE');
      if (!this.active(model, generation, scope) || model.profile.manager !== owner || !this.scheduleTaskRowCurrent(model, row)) return false;
      this.saving = false;
      await this.load('schedules', model); return this.error === '';
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.saving = false; }
  }

  async runSchedule(model: ClientModel, row: ManagementRow): Promise<boolean> {
    if (!this.scheduleTaskRowCurrent(model, row)) return false;
    const generation = this.generation, scope = this.scope, owner = model.profile.manager, group = model.profile.id,
      forgetEpoch = this.scheduleForgetEpoch(model, group), id = row.id;
    if (!this.taskId(id) || this.saving) return false;
    this.clearScheduleReceipt(); this.saving = true; this.error = '';
    try {
      const receipt = await this.call(model, '/api/v1/assistant/tasks/' + segment(id) + '/run', 'POST', Wire.payload([], []));
      if (!this.active(model, generation, scope) || model.profile.id !== group || model.profile.manager !== owner ||
        this.scheduleForgetEpoch(model, group) !== forgetEpoch || !this.scheduleTaskRowCurrent(model, row)) return false;
      this.lastRunReceipt = receipt; this.receiptGroup = group; this.receiptManager = owner;
      this.receiptForgetEpoch = forgetEpoch; this.receiptTitle = row.title; this.saving = false;
      await this.load('schedules', model); return this.error === '';
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.saving = false; }
  }

  /** Clear an acknowledged run target when its owning hub is forgotten. */
  clearScheduleReceipt(): void {
    this.lastRunReceipt = {}; this.receiptGroup = ''; this.receiptManager = '';
    this.receiptForgetEpoch = -1; this.receiptTitle = '';
  }

  /** Open only the exact conversation returned by the acknowledged Run Now response. */
  scheduleReceiptChat(model: ClientModel): ResourceRow | undefined {
    if (!this.receiptGroup || this.page !== 'schedules' || this.receiptGroup !== model.profile.id ||
      this.receiptManager !== model.profile.manager || this.receiptForgetEpoch !== this.scheduleForgetEpoch(model, model.profile.id)) {
      this.clearScheduleReceipt(); return undefined;
    }
    if (!this.active(model, this.generation, this.scope)) return undefined;
    return this.scheduleResultChat(this.lastRunReceipt, this.receiptGroup, this.receiptManager,
      this.receiptForgetEpoch, this.receiptTitle);
  }

  /** Open only a history entry that belongs to the currently loaded schedule owner. */
  scheduleRunChat(model: ClientModel, run: ManagementRow): ResourceRow | undefined {
    if (this.page !== 'schedules' || !this.scheduleRuns.includes(run) || !this.active(model, this.generation, this.scope) ||
      run.group !== model.profile.id || run.manager !== model.profile.manager ||
      run.forgetEpoch !== this.scheduleForgetEpoch(model, model.profile.id) || run.scope !== this.scope) return undefined;
    return this.scheduleResultChat(run.raw, run.group, run.manager, run.forgetEpoch, run.title);
  }

  private scheduleResultChat(data: Record<string, Object>, group: string, manager: string, forgetEpoch: number,
    title: string): ResourceRow | undefined {
    if (!group || forgetEpoch !== this.ownerForgetEpoch) return undefined;
    // These are the run API's documented destination fields; history IDs and task IDs are not chat IDs.
    const machine = Wire.text(data, 'machine_id'), session = Wire.text(data, 'session_id');
    if (!machine || !session) return undefined;
    const row = new ResourceRow(session, title, 'session');
    row.group = group; row.manager = manager; row.machine = machine; row.session = session;
    row.raw = JSON.stringify(Wire.payload(['id', 'session_id', 'machine_id', 'title'], [session, session, machine, title]));
    return row;
  }

  async createPairing(model: ClientModel, name: string, os: string): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = this.scope;
    if (!model.features.includes('machines_add') || !name.trim() || !['mac', 'linux', 'windows'].includes(os)) return false;
    this.saving = true; this.error = '';
    try {
      const add = Wire.payload(['name', 'os', 'manager'], [name.trim(), os, model.profile.manager]);
      const body = Wire.payload(['add'], [add]);
      const result = await this.call(model, '/api/v1/auth/pair-codes', 'POST', body, '', (): boolean => this.active(model, generation, scope));
      if (!this.active(model, generation, scope)) return false;
      this.pairing = result; this.saving = false; const clientGeneration = this.clientGeneration; await this.load('machines', model);
      return model.owns(clientGeneration, scope) && this.scope === scope && (!this.admission || this.admission());
    } catch (error) { if (this.active(model, generation, scope)) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope)) this.saving = false; }
  }

  async renewPairing(model: ClientModel): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const pairing = this.pairing, id = Wire.text(pairing, 'machine');
    if (!this.canRenewPairing() || this.saving) return false;
    const generation = this.generation, scope = this.scope; this.saving = true; this.error = '';
    try {
      const latest = await this.call(model, '/api/v1/machines');
      if (!this.active(model, generation, scope)) return false;
      const machine = Wire.rows(latest, 'machines').find((item: Record<string, Object>) => Wire.text(item, 'id') === id);
      if (!machine || !this.neverConnected(machine)) { this.error = 'request_updated'; return false; }
      const body = Wire.payload(['machine'], [id]);
      const result = await this.call(model, '/api/v1/auth/pair-codes', 'POST', body, '', (): boolean => this.active(model, generation, scope));
      if (!this.active(model, generation, scope)) return false;
      this.pairing = result; return true;
    } catch (error) { if (this.active(model, generation, scope)) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope)) this.saving = false; }
  }

  private neverConnected(machine: Record<string, Object>): boolean {
    return Wire.text(machine, 'trust') === 'pending' && !Wire.bool(machine, 'online') &&
      ['', '0001-01-01T00:00:00Z'].includes(Wire.text(machine, 'last_seen')) && (!Wire.has(machine, 'link') || machine['link'] === null);
  }
  canRenewPairing(): boolean {
    const id = Wire.text(this.pairing, 'machine'), expires = Date.parse(Wire.text(this.pairing, 'expires_at'));
    const machine = this.rows.find((row: ManagementRow) => row.id === id);
    return !!machine && this.neverConnected(machine.raw) && Number.isFinite(expires) && expires <= Date.now();
  }

  async trustMachine(model: ClientModel, row: ManagementRow, trust: boolean): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = this.scope, owner = model.profile.manager, id = row.id;
    if (Wire.text(row.raw, 'trust') !== 'pending') return false;
    this.saving = true; this.error = '';
    try {
      await this.call(model, '/api/v1/machines/' + segment(id) + (trust ? '/confirm' : '/reject'), 'POST', Wire.payload([], []), '', (): boolean => this.active(model, generation, scope) && model.profile.manager === owner);
      if (!this.active(model, generation, scope) || model.profile.manager !== owner || !this.rows.some((item: ManagementRow) => item.id === id)) return false;
      this.saving = false;
      await this.load('machines', model); return this.error === '';
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.saving = false; }
  }

  async setConnectorEnabled(model: ClientModel, row: ManagementRow, enabled: boolean): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = this.scope, id = row.id;
    this.saving = true; this.error = '';
    try {
      const body = Wire.payload(['enabled'], [enabled]);
      await this.call(model, '/api/v1/connectors/' + segment(id) + '/settings', 'PUT', body);
      if (!this.active(model, generation, scope)) return false;
      this.saving = false;
      await this.reloadConnectors(model); return true;
    } catch (error) { if (this.active(model, generation, scope)) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope)) this.saving = false; }
  }

  async setConnectorSetting(model: ClientModel, row: ManagementRow, key: string, value: boolean): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager, id = row.id;
    if (!['enabled', 'archive', 'inbox'].includes(key) || !Wire.has(row.raw, key) || this.page !== 'connectors' ||
      this.scope !== scope || this.owner !== owner || this.saving || !this.rows.some((item: ManagementRow) => item.id === id)) return false;
    this.saving = true; this.error = '';
    try {
      await this.call(model, '/api/v1/connectors/' + segment(id) + '/settings', 'PUT', Wire.payload([key], [value]));
      if (!this.active(model, generation, scope) || model.profile.manager !== owner) return false;
      this.saving = false; await this.reloadConnectors(model); return true;
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.saving = false; }
  }

  openConnector(row: ManagementRow): void {
    this.selectedId = row.id; this.selectedTarget = 'connector:' + row.id;
    this.connectorPrompt = {}; this.connectorFields = []; this.error = '';
  }

  async connectorLogin(model: ClientModel, row: ManagementRow, start: boolean): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager, id = row.id;
    if (this.page !== 'connectors' || this.selectedId !== id || this.owner !== owner || this.scope !== scope || this.saving ||
      !Wire.strings(row.raw, 'capabilities').includes('login')) return false;
    const prompt = this.connectorPrompt, step = start ? '' : Wire.text(prompt, 'step');
    const values = start || Wire.text(prompt, 'kind') ? Wire.payload([], []) : this.promptValues(this.connectorFields);
    const body = Wire.payload(['step', 'values'], [step, values]);
    this.saving = true; this.error = '';
    try {
      const result = await this.call(model, '/api/v1/connectors/' + segment(id) + '/login', 'POST', body);
      if (!this.detailOwner(model, generation, scope, owner, 'connectors', id)) return false;
      this.connectorPrompt = result; this.connectorFields = this.promptInputs(result);
      if (Wire.bool(result, 'done')) { this.saving = false; await this.reloadConnectors(model); }
      return true;
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'connectors', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'connectors', id)) this.saving = false; }
  }

  async connectorLogout(model: ClientModel, row: ManagementRow): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager, id = row.id;
    if (!this.detailOwner(model, generation, scope, owner, 'connectors', id) || this.saving) return false;
    this.saving = true; this.error = '';
    try {
      await this.call(model, '/api/v1/connectors/' + segment(id) + '/logout', 'POST', Wire.payload([], []));
      if (!this.detailOwner(model, generation, scope, owner, 'connectors', id)) return false;
      this.connectorPrompt = {}; this.connectorFields = []; this.saving = false;
      await this.reloadConnectors(model); return true;
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'connectors', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'connectors', id)) this.saving = false; }
  }

  async setChannelEnabled(model: ClientModel, row: ManagementRow, enabled: boolean): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager, id = row.id;
    if (this.page === 'channels' && this.selectedId === id) {
      if (Wire.bool(this.channelData(), 'read_only')) return false;
      this.saving = true; this.error = '';
      try {
        const received = await this.call(model, '/api/v1/channels/' + segment(id), 'PUT', Wire.payload(['enabled'], [enabled])); if (!this.detailOwner(model, generation, scope, owner, 'channels', id)) return false; this.channelDetail = received;
        return this.detailOwner(model, generation, scope, owner, 'channels', id);
      } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.fail(error as Error); return false; }
      finally { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.saving = false; }
    }
    this.saving = true; this.error = '';
    try {
      const body = Wire.payload(['enabled'], [enabled]);
      await this.call(model, '/api/v1/channels/' + segment(id), 'PUT', body);
      if (!this.active(model, generation, scope) || model.profile.manager !== owner) return false;
      this.saving = false;
      await this.load('channels', model); return this.error === '';
    } catch (error) { if (this.active(model, generation, scope)) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope)) this.saving = false; }
  }

  async openChannel(model: ClientModel, row: ManagementRow): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager, id = row.id;
    if (this.page !== 'channels' || !/^[a-z][a-z0-9_]{0,63}$/.test(id)) return false;
    this.saving = false; this.selectedId = id; this.selectedTarget = 'channel:' + id; this.channelDetail = {}; this.error = ''; this.loading = true;
    try {
      const detail = await this.call(model, '/api/v1/channels/' + segment(id));
      if (!this.detailOwner(model, generation, scope, owner, 'channels', id)) return false;
      this.channelDetail = detail; this.channelPrompt = {}; this.channelFields = []; return true;
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.loading = false; }
  }

  channelData(): Record<string, Object> {
    const nested = Wire.object(this.channelDetail, 'channel');
    return Object.keys(nested).length ? nested : this.channelDetail;
  }

  async setChannelFeature(model: ClientModel, id: string, feature: string, enabled: boolean): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    const channel = this.channelData();
    if (!this.detailOwner(model, generation, scope, owner, 'channels', id) || Wire.bool(channel, 'read_only') ||
      !Wire.rows(channel, 'features').some((item: Record<string, Object>) => Wire.text(item, 'id') === feature) || this.saving) return false;
    this.saving = true; this.error = '';
    try {
      const changes = Wire.payload([feature], [enabled]);
      const body = Wire.payload(['features'], [changes]);
      const received = await this.call(model, '/api/v1/channels/' + segment(id), 'PUT', body); if (!this.detailOwner(model, generation, scope, owner, 'channels', id)) return false; this.channelDetail = received;
      return this.detailOwner(model, generation, scope, owner, 'channels', id);
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.saving = false; }
  }

  async setChannelTarget(model: ClientModel, id: string, kind: string, machine: string = '', session: string = ''): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    const channel = this.channelData();
    if (!this.detailOwner(model, generation, scope, owner, 'channels', id) || Wire.bool(channel, 'read_only') ||
      !['main', 'new', 'conversation'].includes(kind) || kind === 'conversation' && (!machine || !session) || this.saving) return false;
    const keys = ['kind'], values: Object[] = [kind];
    if (kind === 'conversation') { keys.push('machine_id'); values.push(machine); keys.push('session_id'); values.push(session); }
    this.saving = true; this.error = '';
    try {
      const target = Wire.payload(keys, values), body = Wire.payload(['target'], [target]);
      const received = await this.call(model, '/api/v1/channels/' + segment(id), 'PUT', body); if (!this.detailOwner(model, generation, scope, owner, 'channels', id)) return false; this.channelDetail = received;
      return this.detailOwner(model, generation, scope, owner, 'channels', id);
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.saving = false; }
  }

  async channelBind(model: ClientModel, id: string, start: boolean): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    const channel = this.channelData();
    if (!this.detailOwner(model, generation, scope, owner, 'channels', id) || Wire.bool(channel, 'read_only') || Wire.text(channel, 'bind', 'none') === 'none' || this.saving) return false;
    const prompt = this.channelPrompt, step = start ? '' : Wire.text(prompt, 'step');
    const values = start || Wire.text(prompt, 'kind') ? Wire.payload([], []) : this.promptValues(this.channelFields);
    const body = start ? Wire.payload([], []) : Wire.payload(['step', 'values'], [step, values]);
    this.saving = true; this.error = '';
    try {
      const result = await this.call(model, '/api/v1/channels/' + segment(id) + '/bind', 'POST', body);
      if (!this.detailOwner(model, generation, scope, owner, 'channels', id)) return false;
      this.channelPrompt = result; this.channelFields = this.promptInputs(result);
      if (Wire.bool(result, 'done')) { const received = await this.call(model, '/api/v1/channels/' + segment(id)); if (!this.detailOwner(model, generation, scope, owner, 'channels', id)) return false; this.channelDetail = received; this.channelPrompt = {}; this.channelFields = []; }
      return true;
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.saving = false; }
  }

  async channelUnbind(model: ClientModel, id: string): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!this.detailOwner(model, generation, scope, owner, 'channels', id) || this.saving) return false;
    this.saving = true; this.error = '';
    try {
      const received = await this.call(model, '/api/v1/channels/' + segment(id) + '/binding', 'DELETE'); if (!this.detailOwner(model, generation, scope, owner, 'channels', id)) return false; this.channelDetail = received;
      if (this.detailOwner(model, generation, scope, owner, 'channels', id)) { this.channelPrompt = {}; this.channelFields = []; }
      return true;
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.saving = false; }
  }

  async channelTest(model: ClientModel, id: string): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!this.detailOwner(model, generation, scope, owner, 'channels', id) || this.saving) return false;
    this.saving = true; this.error = '';
    try {
      await this.call(model, '/api/v1/channels/' + segment(id) + '/test', 'POST', Wire.payload([], []));
      if (!this.detailOwner(model, generation, scope, owner, 'channels', id)) return false;
      const received = await this.call(model, '/api/v1/channels/' + segment(id)); if (!this.detailOwner(model, generation, scope, owner, 'channels', id)) return false; this.channelDetail = received; return true;
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'channels', id)) this.saving = false; }
  }

  async setExtensionEnabled(model: ClientModel, row: ManagementRow, enabled: boolean): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = this.scope, id = row.id;
    this.saving = true; this.error = '';
    try {
      const body = Wire.payload(['enabled'], [enabled]);
      await this.call(model, '/api/v1/extensions/' + segment(id) + '/-/enabled', 'PUT', body);
      if (!this.active(model, generation, scope)) return false;
      this.saving = false;
      await this.load('extensions', model); return this.error === '';
    } catch (error) { if (this.active(model, generation, scope)) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope)) this.saving = false; }
  }

  private valueText(value: Object | undefined): string {
    if (value === undefined || value === null) return '';
    if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return String(value);
    return '';
  }

  private configAt(root: Record<string, Object>, path: string): Object | undefined {
    const parts = path.split('.'); let current = root;
    for (let index = 0; index < parts.length; index++) {
      if (!Wire.has(current, parts[index])) return undefined;
      const value = current[parts[index]];
      if (index === parts.length - 1) return value;
      current = Wire.record(value);
    }
    return undefined;
  }

  private schemaInputs(fields: Record<string, Object>[], config: Record<string, Object>, prefix: string = ''): ManagementInput[] {
    const result: ManagementInput[] = [];
    fields.forEach((schema: Record<string, Object>) => {
      const name = Wire.text(schema, 'name');
      if (!name || !/^[A-Za-z0-9_-]+$/.test(name)) { this.extensionSchemaSupported = false; return; }
      const path = prefix ? prefix + '.' + name : name;
      const type = Wire.text(schema, 'type', 'string'), format = Wire.text(schema, 'format');
      if (type === 'object' && Array.isArray(schema['fields'])) {
        result.push(...this.schemaInputs(Wire.rows(schema, 'fields'), config, path)); return;
      }
      const enumRows = Wire.rows(schema, 'enum');
      const options: string[] = [];
      let supported = ['string', 'boolean', 'integer', 'number'].includes(type) && !['machine', 'launcher'].includes(format);
      if (enumRows.length > 0) {
        enumRows.forEach((choice: Record<string, Object>) => {
          const value = choice['value'];
          if (typeof value !== 'string') supported = false;
          else options.push(value as string);
        });
        supported = supported && type === 'string';
      }
      if (!supported) { this.extensionSchemaSupported = false; return; }
      const input = new ManagementInput(path, Wire.text(schema, 'title', name), '', false,
        this.valueText(this.configAt(config, path) ?? schema['default']));
      input.kind = enumRows.length > 0 ? 'enum' : type;
      input.options = options;
      result.push(input);
    });
    return result;
  }

  private promptInputs(prompt: Record<string, Object>): ManagementInput[] {
    return Wire.rows(prompt, 'fields').filter((field: Record<string, Object>) => !!Wire.text(field, 'name'))
      .map((field: Record<string, Object>) => new ManagementInput(Wire.text(field, 'name'),
        Wire.text(field, 'label', Wire.text(field, 'name')), Wire.text(field, 'placeholder'), Wire.bool(field, 'secret')));
  }

  private promptValues(fields: ManagementInput[]): Record<string, Object> {
    const keys: string[] = [], values: Object[] = [];
    fields.forEach((field: ManagementInput) => { keys.push(field.key); values.push(field.value); });
    return Wire.payload(keys, values);
  }

  private detailOwner(model: ClientModel, generation: number, scope: string, owner: string, page: string, id: string): boolean {
    return this.active(model, generation, scope) && this.page === page && this.selectedId === id &&
      this.owner === owner && model.profile.manager === owner;
  }

  async openExtension(model: ClientModel, row: ManagementRow): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager, id = row.id;
    if (this.page !== 'extensions' || !id) return false;
    this.saving = false; this.selectedId = id; this.selectedTarget = 'extension:' + id; this.error = ''; this.loading = true;
    try {
      const detail = await this.call(model, '/api/v1/extensions/' + segment(id) + '/-/');
      if (!this.detailOwner(model, generation, scope, owner, 'extensions', id)) return false;
      this.extensionDetail = detail; this.extensionSchemaSupported = true;
      this.extensionConfigFields = this.schemaInputs(Wire.rows(Wire.object(detail, 'form'), 'fields'), Wire.object(detail, 'config'));
      this.extensionSecretFields = Wire.rows(detail, 'secrets').map((field: Record<string, Object>) =>
        new ManagementInput(Wire.text(field, 'name'), Wire.text(field, 'title', Wire.text(field, 'name')), '', true));
      const authEnabled = model.features.includes('extension_auth');
      if (authEnabled) await this.loadExtensionAuth(model, id);
      return true;
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.loading = false; }
  }

  changeInput(fields: ManagementInput[], key: string, value: string): void {
    const field = fields.find((item: ManagementInput) => item.key === key);
    if (field) field.value = value;
  }

  extensionConfigChanged(): boolean {
    const config = Wire.object(this.extensionDetail, 'config');
    return this.extensionConfigFields.some((field: ManagementInput) => this.valueText(this.configAt(config, field.key)) !== field.value);
  }

  private assignConfig(root: Record<string, Object>, path: string, value: Object): void {
    const parts = path.split('.'); let current = root;
    for (let index = 0; index < parts.length - 1; index++) {
      let next = Wire.record(current[parts[index]]);
      if (!Object.keys(next).length) { next = Wire.payload([], []); current[parts[index]] = next; }
      current = next;
    }
    current[parts[parts.length - 1]] = value;
  }

  async saveExtensionConfig(model: ClientModel): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const id = this.selectedId, generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!this.extensionSchemaSupported || !this.extensionConfigFields.length || Wire.bool(this.extensionDetail, 'read_only') ||
      !this.detailOwner(model, generation, scope, owner, 'extensions', id) || this.saving) return false;
    const config = Wire.parse(JSON.stringify(Wire.object(this.extensionDetail, 'config')));
    for (const field of this.extensionConfigFields) {
      if (!field.value.trim() && field.kind !== 'string' && field.kind !== 'enum' && field.kind !== 'boolean') return false;
      if (!field.value.trim() && field.kind !== 'string' && field.kind !== 'enum') return false;
      let value: Object = field.value;
      if (field.kind === 'boolean') { if (!['true', 'false'].includes(field.value)) return false; value = field.value === 'true'; }
      else if (field.kind === 'integer') { const number = Number(field.value); if (!Number.isInteger(number)) return false; value = number; }
      else if (field.kind === 'number') { const number = Number(field.value); if (!Number.isFinite(number)) return false; value = number; }
      else if (field.kind === 'enum' && !field.options.includes(field.value)) return false;
      this.assignConfig(config, field.key, value);
    }
    this.saving = true; this.error = '';
    try {
      const body = Wire.payload(['config'], [config]);
      await this.call(model, '/api/v1/extensions/' + segment(id) + '/-/config', 'PUT', body);
      if (!this.detailOwner(model, generation, scope, owner, 'extensions', id)) return false;
      return await this.openExtension(model, new ManagementRow(id));
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.saving = false; }
  }

  async saveExtensionSecret(model: ClientModel, field: ManagementInput): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const id = this.selectedId, generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!field.key || !field.value || Wire.bool(this.extensionDetail, 'read_only') || !this.detailOwner(model, generation, scope, owner, 'extensions', id) || this.saving) return false;
    this.saving = true; this.error = '';
    try {
      const body = Wire.payload(['value'], [field.value]);
      await this.call(model, '/api/v1/extensions/' + segment(id) + '/-/secrets/' + segment(field.key), 'PUT', body);
      if (!this.detailOwner(model, generation, scope, owner, 'extensions', id)) return false;
      field.value = ''; return await this.openExtension(model, new ManagementRow(id));
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.saving = false; }
  }

  async clearExtensionSecret(model: ClientModel, field: ManagementInput): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const id = this.selectedId, generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!field.key || Wire.bool(this.extensionDetail, 'read_only') || !this.detailOwner(model, generation, scope, owner, 'extensions', id) || this.saving) return false;
    this.saving = true; this.error = '';
    try {
      await this.call(model, '/api/v1/extensions/' + segment(id) + '/-/secrets/' + segment(field.key), 'DELETE');
      if (!this.detailOwner(model, generation, scope, owner, 'extensions', id)) return false;
      return await this.openExtension(model, new ManagementRow(id));
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.saving = false; }
  }

  async loadExtensionAuth(model: ClientModel, id: string): Promise<void> {
    if (!this.active(model, this.generation, this.scope)) return;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!model.features.includes('extension_auth') || !id || !this.detailOwner(model, generation, scope, owner, 'extensions', id)) return;
    try {
      const received = await this.call(model, '/api/v1/extensions/' + segment(id) + '/-/auth'); if (!this.detailOwner(model, generation, scope, owner, 'extensions', id)) return; this.auth = received;
      if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) { this.authPrompt = {}; this.authFields = []; }
    } catch (error) {
      if (!this.detailOwner(model, generation, scope, owner, 'extensions', id)) return;
      if (error instanceof HttpFailure && (error.status === 404 || error.status === 405)) {
        this.auth = {}; this.authPrompt = {}; this.authFields = [];
      } else this.fail(error as Error);
    }
  }

  async extensionAuthStep(model: ClientModel, id: string, start: boolean): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!model.features.includes('extension_auth') || !this.detailOwner(model, generation, scope, owner, 'extensions', id) || this.saving) return false;
    const prompt = this.authPrompt;
    const step = start ? '' : Wire.text(prompt, 'step');
    const values = start || Wire.text(prompt, 'kind') !== '' ? Wire.payload([], []) : this.promptValues(this.authFields);
    const body = Wire.payload(['step', 'values'], [step, values]);
    this.saving = true; this.error = '';
    try {
      const result = await this.call(model, '/api/v1/extensions/' + segment(id) + '/-/auth/step', 'POST', body);
      if (!this.detailOwner(model, generation, scope, owner, 'extensions', id)) return false;
      this.authPrompt = result; this.authFields = this.promptInputs(result);
      if (Wire.bool(result, 'done')) await this.loadExtensionAuth(model, id);
      return true;
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.saving = false; }
  }

  async extensionLogout(model: ClientModel, id: string): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!this.detailOwner(model, generation, scope, owner, 'extensions', id) || this.saving) return false;
    this.saving = true; this.error = '';
    try {
      const received = await this.call(model, '/api/v1/extensions/' + segment(id) + '/-/auth/logout', 'POST', Wire.payload([], [])); if (!this.detailOwner(model, generation, scope, owner, 'extensions', id)) return false; this.auth = received;
      if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) { this.authPrompt = {}; this.authFields = []; }
      return true;
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.saving = false; }
  }

  async loadExtensionCatalog(model: ClientModel, query: string = this.catalogQuery, market: string = this.catalogMarket): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (this.page !== 'extensions' || !Wire.bool(this.data, 'markets') || this.owner !== owner) return false;
    this.catalogQuery = query; this.catalogMarket = market; this.error = ''; this.loading = true;
    const params: string[] = [];
    if (query.trim()) params.push('q=' + segment(query.trim()));
    if (market) params.push('market=' + segment(market));
    try {
      const result = await this.read('/api/v1/extensions/-/catalog' + (params.length ? '?' + params.join('&') : ''), model);
      if (!this.active(model, generation, scope) || this.page !== 'extensions' || model.profile.manager !== owner) return false;
      this.catalogMarkets = Wire.rows(result, 'markets');
      this.catalogRows = Wire.rows(result, 'extensions').map((item: Record<string, Object>) =>
        new ManagementRow(Wire.text(item, 'id'), Wire.text(item, 'name', Wire.text(item, 'id')),
          Wire.text(item, 'problem', Wire.text(item, 'version')), item));
      return true;
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.loading = false; }
  }

  async previewExtension(model: ClientModel, row: ManagementRow): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    const market = Wire.text(row.raw, 'market'), id = row.id;
    if (this.page !== 'extensions' || this.owner !== owner || !this.catalogRows.some((item: ManagementRow) => item.id === id && Wire.text(item.raw, 'market') === market) ||
      !Wire.bool(this.data, 'can_install') || !market || !id || this.saving) return false;
    this.catalogSelectedId = id; this.catalogSelectedMarket = market; this.selectedTarget = 'catalog:' + market + ':' + id;
    this.error = ''; this.preview = {}; this.operation = {}; this.saving = true;
    try {
      const stepped = model.features.includes('extension_operations');
      const keys = ['market', 'extension']; const values: Object[] = [market, id];
      if (stepped) { keys.push('async'); values.push(true); }
      const result = await this.call(model, '/api/v1/extensions/-/catalog/preview', 'POST', Wire.payload(keys, values));
      if (!this.active(model, generation, scope) || model.profile.manager !== owner || this.selectedTarget !== 'catalog:' + market + ':' + id) return false;
      if (stepped) {
        this.operation = Wire.object(result, 'operation');
        const opId = Wire.text(this.operation, 'id');
        if (!opId) throw new Error('protocol');
        if (Wire.text(this.operation, 'state') === 'needs_consent') await this.refreshExtensionOperation(model, opId);
      } else this.preview = result;
      return true;
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.saving = false; }
  }

  previewReady(): boolean {
    if (!Wire.text(this.preview, 'preview_id') || !Wire.text(this.preview, 'permissions_digest') || Wire.strings(this.preview, 'unresolved').length > 0) return false;
    const expiry = Date.parse(Wire.text(this.preview, 'expires_at'));
    if (Number.isFinite(expiry) && expiry <= Date.now()) return false;
    const bundle = Wire.rows(this.preview, 'bundle');
    if (bundle.some((item: Record<string, Object>) => !Wire.text(item, 'preview_id') || !Wire.text(item, 'permissions_digest') ||
      Wire.strings(item, 'unresolved').length > 0 || Date.parse(Wire.text(item, 'expires_at')) <= Date.now())) return false;
    const bundled = bundle.map((item: Record<string, Object>) => Wire.text(Wire.object(item, 'manifest'), 'id'));
    return Wire.rows(this.preview, 'depends').every((item: Record<string, Object>) => {
      const state = Wire.text(item, 'state');
      return state === 'ok' || ['missing', 'old'].includes(state) && bundled.includes(Wire.text(item, 'id'));
    });
  }

  async confirmExtensionPreview(model: ClientModel): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    const preview = this.preview, id = this.catalogSelectedId, market = this.catalogSelectedMarket;
    const target = 'catalog:' + market + ':' + id;
    if (this.page !== 'extensions' || !id || !market || this.selectedTarget !== target || this.owner !== owner ||
      !this.active(model, generation, scope) || !this.previewReady() || this.saving) return false;
    const upgrade = Wire.bool(preview, 'upgrade');
    const bodyKeys = ['preview_id', 'permissions_digest'], bodyValues: Object[] = [Wire.text(preview, 'preview_id'), Wire.text(preview, 'permissions_digest')];
    bodyKeys.push('bundle'); bodyValues.push(Wire.rows(preview, 'bundle').map((item: Record<string, Object>) =>
      Wire.payload(['preview_id', 'permissions_digest'], [Wire.text(item, 'preview_id'), Wire.text(item, 'permissions_digest')])));
    const stepped = model.features.includes('extension_operations');
    if (stepped) { bodyKeys.push('async'); bodyValues.push(true); }
    this.saving = true; this.error = '';
    try {
      const path = upgrade ? '/api/v1/extensions/' + segment(id) + '/-/upgrade' : '/api/v1/extensions/-/install';
      const result = await this.call(model, path, 'POST', Wire.payload(bodyKeys, bodyValues));
      if (!this.active(model, generation, scope) || model.profile.manager !== owner || this.selectedTarget !== target) return false;
      if (stepped) this.operation = Wire.object(result, 'operation');
      else { this.preview = {}; await this.load('extensions', model); await this.loadExtensionCatalog(model, this.catalogQuery, market); }
      this.preview = {};
      return true;
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.saving = false; }
  }

  async refreshExtensionOperation(model: ClientModel, id: string = Wire.text(this.operation, 'id')): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!model.features.includes('extension_operations') || this.page !== 'extensions' || !id || this.owner !== owner) return false;
    try {
      const result = await this.read('/api/v1/extensions/-/operations/' + segment(id), model);
      if (!this.active(model, generation, scope) || model.profile.manager !== owner || Wire.text(this.operation, 'id') && Wire.text(this.operation, 'id') !== id) return false;
      this.operation = result;
      const state = Wire.text(result, 'state');
      if (state === 'needs_consent') {
        const preview = Wire.object(result, 'preview');
        if (Object.keys(preview).length) {
          this.preview = preview; this.catalogSelectedId = Wire.text(result, 'extension'); this.catalogSelectedMarket = Wire.text(result, 'market');
          this.selectedTarget = 'catalog:' + this.catalogSelectedMarket + ':' + this.catalogSelectedId;
        }
      } else if (state === 'succeeded') {
        this.preview = {}; this.operation = {}; await this.reloadExtensions(model);
      } else if (['failed', 'canceled'].includes(state)) this.preview = {};
      return true;
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
  }

  async cancelExtensionOperation(model: ClientModel): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const id = Wire.text(this.operation, 'id'), generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!id || !this.detailOrCatalogOwner(model, generation, scope, owner) || this.saving) return false;
    this.saving = true; this.error = '';
    try {
      await this.call(model, '/api/v1/extensions/-/operations/' + segment(id), 'DELETE');
      if (!this.active(model, generation, scope) || model.profile.manager !== owner) return false;
      const detail = !!this.selectedId;
      this.operation = {}; this.preview = {}; this.saving = false;
      if (detail) await this.reloadExtensions(model);
      else { await this.load('extensions', model); await this.loadExtensionCatalog(model, this.catalogQuery, this.catalogMarket); }
      return true;
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.saving = false; }
  }

  async retryExtensionOperation(model: ClientModel): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const id = Wire.text(this.operation, 'id'), generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!id || !Wire.bool(this.operation, 'retryable') || !this.detailOrCatalogOwner(model, generation, scope, owner) || this.saving) return false;
    this.saving = true; this.error = '';
    try {
      const result = await this.call(model, '/api/v1/extensions/-/operations/' + segment(id) + '/retry', 'POST', Wire.payload([], []));
      if (!this.active(model, generation, scope) || model.profile.manager !== owner) return false;
      this.operation = Wire.object(result, 'operation'); return true;
    } catch (error) { if (this.active(model, generation, scope) && model.profile.manager === owner) this.fail(error as Error); return false; }
    finally { if (this.active(model, generation, scope) && model.profile.manager === owner) this.saving = false; }
  }

  private detailOrCatalogOwner(model: ClientModel, generation: number, scope: string, owner: string): boolean {
    return this.active(model, generation, scope) && this.page === 'extensions' && this.owner === owner && model.profile.manager === owner;
  }

  async removeExtension(model: ClientModel): Promise<boolean> {
    if (!this.active(model, this.generation, this.scope)) return false;
    const id = this.selectedId, generation = this.generation, scope = model.scope(), owner = model.profile.manager;
    if (!id || Wire.bool(this.extensionDetail, 'read_only') || !this.detailOwner(model, generation, scope, owner, 'extensions', id) || this.saving || !Wire.text(this.extensionDetail, 'origin').startsWith('market:')) return false;
    this.saving = true; this.error = '';
    try {
      const stepped = model.features.includes('extension_operations');
      const path = '/api/v1/extensions/' + segment(id) + '/-/?keep_data=1' + (stepped ? '&async=1' : '');
      const result = await this.call(model, path, 'DELETE');
      if (!this.detailOwner(model, generation, scope, owner, 'extensions', id)) return false;
      if (stepped) this.operation = Wire.object(result, 'operation');
      else { this.extensionDetail = {}; this.selectedId = ''; this.saving = false; await this.load('extensions', model); }
      return true;
    } catch (error) { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.fail(error as Error); return false; }
    finally { if (this.detailOwner(model, generation, scope, owner, 'extensions', id)) this.saving = false; }
  }

  async loadMoreLedger(model: ClientModel): Promise<void> {
    if (!this.active(model, this.generation, this.scope)) return;
    const next = Wire.identifier(this.data, 'next'); if (!next || this.loading) return;
    const generation = this.generation, scope = this.scope; this.loading = true; this.error = '';
    try {
      const result = await this.read('/api/v1/connectors/sent?limit=50&before=' + segment(next), model);
      if (!this.active(model, generation, scope)) return;
      this.data = result;
      const prior = this.rows;
      this.rows = prior.concat(Wire.rows(result, 'items').map((item: Record<string, Object>) => {
        const target = Wire.object(item, 'target');
        return new ManagementRow(Wire.identifier(item, 'id'), Wire.text(item, 'connector'), Wire.text(target, 'to', Wire.text(target, 'subject', Wire.text(item, 'summary'))), item);
      }));
    } catch (error) { if (this.active(model, generation, scope)) this.fail(error as Error); }
    finally { if (this.active(model, generation, scope)) this.loading = false; }
  }
}
