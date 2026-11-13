import type { ClientModel } from './ClientModel';
import type { HubRepository } from './HubRepository';
import { ResourceRow, Wire } from './Wire';

const HEALTH_SPACE: string = 'health';
const MEMORY_INDEX: string = 'MEMORY.md';
const MAX_HEALTH_REPORT_BYTES: number = 32 * 1024 * 1024;

class HealthOwner {
  root: ClientModel;
  rootGeneration: number;
  rootScope: string;
  rootManager: string;
  client: ClientModel;
  repository: HubRepository;
  generation: number;
  scope: string;
  manager: string;
  target: string;
  machine: string = '';

  constructor(root: ClientModel, rootGeneration: number, rootScope: string, rootManager: string, client: ClientModel,
    repository: HubRepository, generation: number, scope: string, manager: string, target: string) {
    this.root = root; this.rootGeneration = rootGeneration; this.rootScope = rootScope; this.rootManager = rootManager;
    this.client = client; this.repository = repository; this.generation = generation; this.scope = scope;
    this.manager = manager; this.target = target;
  }
}

export class HealthMemoryFile {
  name: string = '';
  content: string = '';
  updatedAt: string = '';
  constructor(name: string = '', content: string = '', updatedAt: string = '') { this.name = name; this.content = content; this.updatedAt = updatedAt; }
}

export class HealthReport {
  row: ResourceRow = new ResourceRow();
  name: string = '';
  title: string = '';
  path: string = '';
  recordPath: string = '';
  size: number = 0;
  modifiedAt: string = '';
}

function segment(value: string): string { return encodeURIComponent(value); }

function operationId(): string {
  return 'health-' + Date.now().toString(36) + '-' + Math.floor(Math.random() * 0x7fffffff).toString(36);
}

function payload(keys: string[], values: Object[]): Record<string, Object> { return Wire.payload(keys, values); }

/** The native, manager-owned Health space. It never changes the selected global manager. */
@ObservedV2
export class HealthWorkspace {
  @Trace conversations: ResourceRow[] = [];
  @Trace memories: HealthMemoryFile[] = [];
  @Trace reports: HealthReport[] = [];
  @Trace machine: string = '';
  @Trace draft: string = '';
  @Trace selectedMemory: HealthMemoryFile | undefined;
  @Trace memoryDraft: string = '';
  @Trace loading: boolean = false;
  @Trace busy: boolean = false;
  @Trace error: string = '';
  @Trace memoryError: string = '';
  @Trace writeState: string = '';
  @Trace conversationWriteState: string = '';
  @Trace memoryWriteState: string = '';
  @Trace reportUploadState: string = '';
  @Trace reportUploadAllowed: boolean = false;

  private client: ClientModel | undefined;
  private healthClient: ClientModel | undefined;
  private owner: string = '';
  private viewScope: string = '';
  private rootGeneration: number = 0;
  private rootScope: string = '';
  private rootManager: string = '';
  private revision: number = 0;
  private draftWrite: Promise<void> = Promise.resolve();
  private memoryWrite: Promise<void> = Promise.resolve();
  private draftFailure: boolean = false;
  private memoryDraftFailure: boolean = false;
  private memoryDraftFailureTarget: string = '';

  async open(client: ClientModel): Promise<void> {
    const revision = ++this.revision;
    this.client = client; this.healthClient = undefined; this.owner = '';
    this.rootGeneration = client.connectionGeneration(); this.rootScope = client.scope(); this.rootManager = client.profile.manager;
    this.viewScope = JSON.stringify([this.rootScope, this.rootManager]);
    this.conversations = []; this.memories = []; this.reports = []; this.selectedMemory = undefined;
    this.machine = ''; this.error = ''; this.memoryError = ''; this.writeState = ''; this.conversationWriteState = ''; this.memoryWriteState = '';
    this.reportUploadState = ''; this.reportUploadAllowed = false;
    this.draft = ''; this.memoryDraft = '';
    this.loading = true; this.busy = false;
    try {
      const owner = await this.resolveDefault(client, revision);
      if (!this.current(owner, revision)) return;
      this.healthClient = owner.client; this.owner = owner.manager;
      const saved = Wire.parse(await owner.client.loadView('health-workspace'));
      if (!this.current(owner, revision)) return;
      this.draft = Wire.text(saved, 'draft');
      this.conversationWriteState = ['pending', 'unknown'].includes(Wire.text(saved, 'write_state')) ? Wire.text(saved, 'write_state') : '';
      await this.load(owner, revision);
    } catch (failure) {
      if (this.revision === revision) this.error = this.errorCode(failure as Error);
    } finally { if (this.revision === revision) this.loading = false; }
  }

  hide(): void { this.revision++; this.client = undefined; this.healthClient = undefined; this.conversations = []; this.memories = []; this.reports = []; }

  async refresh(): Promise<void> {
    const client = this.client;
    if (!client) return;
    if (client.scope() !== this.rootScope || client.profile.manager !== this.rootManager) {
      await this.open(client);
      return;
    }
    this.rootGeneration = client.connectionGeneration();
    const revision = this.revision; this.loading = true;
    try {
      let owner = await this.resolveDefault(client, revision);
      if (!this.rootCurrent(client, revision, owner)) return;
      if (this.owner !== owner.manager || this.healthClient !== owner.client) {
        this.owner = owner.manager; this.healthClient = owner.client;
        this.selectedMemory = undefined; this.memoryDraft = '';
        this.conversations = []; this.memories = []; this.reports = []; this.machine = '';
        this.writeState = ''; this.memoryWriteState = ''; this.reportUploadState = ''; this.reportUploadAllowed = false;
        const saved = Wire.parse(await owner.client.loadView('health-workspace'));
        if (!this.rootCurrent(client, revision, owner)) return;
        this.draft = Wire.text(saved, 'draft');
        this.conversationWriteState = ['pending', 'unknown'].includes(Wire.text(saved, 'write_state')) ? Wire.text(saved, 'write_state') : '';
        owner.target = this.healthTarget(owner.manager);
      }
      if (!this.current(owner, revision, owner.target)) return;
      await this.load(owner, revision);
    } catch (failure) {
      if (this.revision === revision) {
        this.error = this.errorCode(failure as Error);
        if (['health_default_manager_unavailable', 'request_updated'].includes(this.error)) this.clearOwnerState();
      }
    }
    finally { if (this.revision === revision) this.loading = false; }
  }

  setDraft(value: string): void {
    this.draft = value;
    this.draftFailure = false;
    const client = this.healthClient, revision = this.revision;
    if (!client) return;
    const generation = client.connectionGeneration(), scope = client.scope(), manager = client.profile.manager;
    const durableWrite = ['pending', 'unknown'].includes(this.conversationWriteState) ? this.conversationWriteState :
      this.conversationWriteState === 'sending' ? 'unknown' : '';
    const raw = JSON.stringify(payload(['draft', 'write_state'], [value, durableWrite])), epoch = client.viewRevision('health-workspace');
    this.draftWrite = this.draftWrite.catch(() => {}).then(async (): Promise<void> => {
      if (client.scope() !== scope || client.profile.manager !== manager) throw new Error('health_storage_unavailable');
      await client.saveView('health-workspace', raw, epoch);
      if (client.scope() !== scope || client.profile.manager !== manager) throw new Error('health_storage_unavailable');
      if (this.draft === value && this.revision === revision && this.healthClient === client) this.draftFailure = false;
    }).catch((failure: Error) => {
      if (this.revision === revision && this.healthClient === client && this.draft === value) {
        this.draftFailure = true; this.error = this.errorCode(failure);
      }
    });
  }

  appendReportPath(path: string, prompt: string): boolean {
    if (!this.healthClient || !this.validReportPath(path)) return false;
    const current = this.draft.trim(), base = current || prompt.trim();
    if (!base) return false;
    if (base.includes(path)) return true;
    this.setDraft(base + '\n' + path);
    return true;
  }

  async startConversation(text: string = this.draft): Promise<ResourceRow | undefined> {
    const client = this.healthClient, root = this.client;
    const prompt = text.trim();
    if (!root || !client || !this.machine || !prompt || this.busy || ['pending', 'unknown'].includes(this.conversationWriteState)) return undefined;
    if (!this.reportUploadAllowed) { this.error = 'health_default_manager_unavailable'; return undefined; }
    const sendingDraft = text === this.draft;
    const revision = this.revision, path = '/api/v1/assistant/conversations';
    this.busy = true; this.conversationWriteState = 'sending'; this.error = '';
    let owner: HealthOwner | undefined;
    try {
      await this.draftWrite;
      if (this.revision !== revision || this.client !== root || this.healthClient !== client) return undefined;
      if (sendingDraft && this.draftFailure) throw new Error('health_storage_unavailable');
      owner = await this.ownerSnapshot(root, client, path, revision);
      if (!this.current(owner, revision, path) || this.machine !== owner.machine) return undefined;
      if (owner.client.operationBlocked(path)) {
        this.conversationWriteState = 'pending'; this.error = 'pending_action';
        await this.saveWriteState(owner, revision, 'pending'); return undefined;
      }
      const machineList = await owner.repository.call('/api/v1/machines');
      if (!this.current(owner, revision, path) || this.machine !== owner.machine) return undefined;
      const expectedMachine = owner.machine;
      const machine = Wire.rows(machineList, 'machines').find((item: Record<string, Object>) => Wire.text(item, 'id') === expectedMachine);
      if (!machine || !this.machineUsable(machine, owner.manager)) {
        this.reportUploadAllowed = false; this.conversationWriteState = 'failed';
        this.error = 'health_default_manager_unavailable'; return undefined;
      }
      this.reportUploadAllowed = true;
      if (!await this.saveWriteState(owner, revision, 'sending') || !this.current(owner, revision, path)) return undefined;
      const captured = owner;
      const result = await captured.repository.call(path, 'POST', payload(['prompt', 'space'], [prompt, HEALTH_SPACE]), operationId(),
        (): boolean => this.current(captured, revision, path) && this.owner === captured.manager &&
          this.healthClient === captured.client && this.machine === captured.machine && this.reportUploadAllowed, true);
      if (!this.current(owner, revision, path)) return undefined;
      const session = Wire.object(result, 'session');
      const row = ResourceRow.from(session, 'conversation');
      if (!row.id) {
        // The hub acknowledged the write, so a malformed follow-up body must not make the draft sendable again.
        this.conversationWriteState = 'unknown'; this.error = 'result_unknown';
        await this.saveWriteState(owner, revision, 'unknown');
        return undefined;
      }
      row.manager = owner.manager; row.machine = Wire.text(session, 'machine_id', owner.machine);
      row.session = Wire.text(session, 'session_id', row.id); row.path = Wire.text(session, 'cwd');
      const data = row.data(); data['space'] = HEALTH_SPACE; row.raw = JSON.stringify(data);
      this.conversationWriteState = 'done';
      await this.saveWriteState(owner, revision, '');
      if (!this.current(owner, revision, path)) return undefined;
      if (this.draft === text) { this.draft = ''; this.setDraft(''); }
      try { await this.load(owner, revision); }
      catch (failure) { if (this.current(owner, revision)) this.error = this.errorCode(failure as Error); }
      return row;
    } catch (failure) {
      if (!owner || !this.current(owner, revision, path)) {
        if (!owner && this.revision === revision && this.client === root && this.healthClient === client) {
          this.conversationWriteState = 'failed'; this.error = this.errorCode(failure as Error);
        }
        return undefined;
      }
      this.conversationWriteState = this.unknownWrite(failure as Error); this.error = this.errorCode(failure as Error);
      await this.saveWriteState(owner, revision, this.conversationWriteState);
      return undefined;
    } finally {
      if (this.revision === revision && this.healthClient === client && this.client === root) this.busy = false;
    }
  }

  async archive(row: ResourceRow): Promise<boolean> {
    const client = this.healthClient, root = this.client;
    if (!root || !client || !this.canArchiveConversation(row) || this.busy) return false;
    const revision = this.revision, target = row.key(), path = '/api/v1/assistant/conversations/' + segment(row.id) + '/archive';
    this.busy = true; this.writeState = 'sending'; this.error = '';
    let owner: HealthOwner | undefined;
    try {
      owner = await this.ownerSnapshot(root, client, target, revision);
      if (!this.current(owner, revision, target) || !this.canArchiveConversation(row)) return false;
      if (owner.client.operationBlocked(path)) { this.writeState = 'pending'; this.error = 'pending_action'; return false; }
      const captured = owner;
      await captured.repository.call(path, 'POST', {}, operationId(),
        (): boolean => this.current(captured, revision, target) && this.canArchiveConversation(row), true);
      if (!this.current(owner, revision, target) || !this.canArchiveConversation(row)) return false;
      this.writeState = 'done'; this.conversations = this.conversations.filter((item: ResourceRow) => item.key() !== target); return true;
    } catch (failure) {
      if (!owner || !this.current(owner, revision, target)) return false;
      this.writeState = this.unknownWrite(failure as Error); this.error = this.errorCode(failure as Error); return false;
    } finally { if (this.revision === revision && this.healthClient === client && this.client === root) this.busy = false; }
  }

  openMemory(file: HealthMemoryFile): void {
    this.selectedMemory = file; this.memoryDraft = file.content; this.memoryError = ''; this.memoryWriteState = '';
    this.memoryDraftFailure = false; this.memoryDraftFailureTarget = file.name;
    if (file.name !== MEMORY_INDEX) this.loadMemoryDraft(file).catch(() => undefined);
  }

  memoryEntries(): HealthMemoryFile[] { return this.memories.filter((file: HealthMemoryFile) => file.name !== MEMORY_INDEX); }

  indexMemory(): HealthMemoryFile | undefined { return this.memories.find((file: HealthMemoryFile) => file.name === MEMORY_INDEX); }

  canEditMemory(): boolean { return !!this.selectedMemory && this.selectedMemory.name !== MEMORY_INDEX; }

  canSaveMemory(): boolean {
    const file = this.selectedMemory;
    return !!file && this.canEditMemory() && file.content !== this.memoryDraft && !this.busy &&
      !['pending', 'unknown'].includes(this.memoryWriteState);
  }

  setMemoryDraft(value: string): void {
    const file = this.selectedMemory, client = this.healthClient, revision = this.revision;
    this.memoryDraft = value;
    if (!file || !client || file.name === MEMORY_INDEX) return;
    this.memoryDraftFailure = false; this.memoryDraftFailureTarget = file.name;
    const generation = client.connectionGeneration(), scope = client.scope(), manager = client.profile.manager;
    const key = this.memoryDraftKey(file.name), epoch = client.viewRevision(key), raw = JSON.stringify(payload(['content'], [value]));
    this.memoryWrite = this.memoryWrite.catch(() => {}).then(async (): Promise<void> => {
      if (client.scope() !== scope || client.profile.manager !== manager)
        throw new Error('health_storage_unavailable');
      await client.saveView(key, raw, epoch);
      if (client.scope() !== scope || client.profile.manager !== manager)
        throw new Error('health_storage_unavailable');
      if (this.memoryDraft === value && this.selectedMemory?.name === file.name) this.memoryDraftFailure = false;
    }).catch((failure: Error) => {
      if (this.revision === revision && this.healthClient === client && this.selectedMemory?.name === file.name && this.memoryDraft === value) {
        this.memoryDraftFailure = true;
        this.memoryDraftFailureTarget = file.name;
        this.memoryError = this.errorCode(failure);
      }
    });
  }

  async saveMemory(): Promise<boolean> {
    const file = this.selectedMemory, client = this.healthClient, root = this.client;
    if (!file || !client || !root || !this.canSaveMemory() || ['pending', 'unknown'].includes(this.memoryWriteState)) return false;
    const revision = this.revision, target = file.name, path = '/api/v1/assistant/memory/' + segment(file.name) + '?space=' + HEALTH_SPACE;
    const content = this.memoryDraft;
    this.busy = true; this.memoryWriteState = 'sending'; this.memoryError = '';
    let owner: HealthOwner | undefined;
    try {
      await this.memoryWrite;
      if (this.revision !== revision || this.selectedMemory?.name !== target || this.memoryDraft !== content) return false;
      if (this.memoryDraftFailure && this.memoryDraftFailureTarget === target) throw new Error('health_storage_unavailable');
      owner = await this.ownerSnapshot(root, client, target, revision);
      if (!this.current(owner, revision, target) || this.selectedMemory?.name !== target || this.memoryDraft !== content) return false;
      if (owner.client.operationBlocked(path)) { this.memoryWriteState = 'pending'; this.memoryError = 'pending_action'; return false; }
      const captured = owner;
      await captured.repository.call(path, 'PUT', payload(['content'], [content]), operationId(),
        (): boolean => this.current(captured, revision, target) && this.selectedMemory?.name === target && this.memoryDraft === content, true);
      if (!this.current(owner, revision, target) || this.selectedMemory?.name !== target) return false;
      this.memoryWriteState = 'done';
      file.content = content;
      try { await this.load(owner, revision); }
      catch (failure) { if (this.current(owner, revision)) this.memoryError = this.errorCode(failure as Error); }
      this.clearMemoryDraft(owner, file);
      return true;
    } catch (failure) {
      if (!owner || !this.current(owner, revision, target)) { if (!owner && this.revision === revision) this.memoryWriteState = 'failed'; return false; }
      this.memoryWriteState = this.unknownWrite(failure as Error); this.memoryError = this.errorCode(failure as Error); return false;
    } finally { if (this.revision === revision && this.healthClient === client && this.client === root) this.busy = false; }
  }

  async deleteMemory(): Promise<boolean> {
    const file = this.selectedMemory, client = this.healthClient, root = this.client;
    if (!file || !client || !root || !this.canEditMemory() || this.busy || ['pending', 'unknown', 'deleted'].includes(this.memoryWriteState)) return false;
    const revision = this.revision, target = file.name, path = '/api/v1/assistant/memory/' + segment(file.name) + '?space=' + HEALTH_SPACE;
    this.busy = true; this.memoryWriteState = 'sending'; this.memoryError = '';
    let owner: HealthOwner | undefined;
    try {
      await this.memoryWrite;
      if (this.revision !== revision || this.selectedMemory?.name !== target) return false;
      owner = await this.ownerSnapshot(root, client, target, revision);
      if (!this.current(owner, revision, target) || this.selectedMemory?.name !== target) return false;
      if (owner.client.operationBlocked(path)) { this.memoryWriteState = 'pending'; this.memoryError = 'pending_action'; return false; }
      const captured = owner;
      await captured.repository.call(path, 'DELETE', {}, operationId(),
        (): boolean => this.current(captured, revision, target) && this.selectedMemory?.name === target, true);
      if (!this.current(owner, revision, target) || this.selectedMemory?.name !== target) return false;
      this.memoryWriteState = 'deleted'; this.memories = this.memories.filter((item: HealthMemoryFile) => item.name !== target);
      this.selectedMemory = undefined; this.memoryDraft = '';
      this.clearMemoryDraft(owner, file);
      return true;
    } catch (failure) {
      if (!owner || !this.current(owner, revision, target)) { if (!owner && this.revision === revision) this.memoryWriteState = 'failed'; return false; }
      this.memoryWriteState = this.unknownWrite(failure as Error); this.memoryError = this.errorCode(failure as Error); return false;
    } finally { if (this.revision === revision && this.healthClient === client && this.client === root) this.busy = false; }
  }

  async deleteReport(report: HealthReport): Promise<boolean> {
    const client = this.healthClient, root = this.client;
    if (!root || !client || !this.machine || this.busy || !report.name || report.row.manager !== this.owner ||
      report.row.machine !== this.machine || this.reports.every((item: HealthReport) => item.row.key() !== report.row.key())) return false;
    const revision = this.revision, target = report.row.key();
    const path = '/api/v1/machines/' + segment(this.machine) + '/health/reports/' + segment(report.name);
    this.busy = true; this.writeState = 'sending'; this.error = '';
    let owner: HealthOwner | undefined;
    try {
      owner = await this.ownerSnapshot(root, client, target, revision);
      if (!this.current(owner, revision, target) || this.machine !== owner.machine || this.reports.every((item: HealthReport) => item.row.key() !== target)) return false;
      if (owner.client.operationBlocked(path)) { this.writeState = 'pending'; this.error = 'pending_action'; return false; }
      const captured = owner;
      await captured.repository.call(path, 'DELETE', {}, operationId(),
        (): boolean => this.current(captured, revision, target) && this.machine === captured.machine &&
          this.reports.some((item: HealthReport) => item.row.key() === target), true);
      if (!this.current(owner, revision, target) || report.row.manager !== owner.manager || report.row.machine !== this.machine) return false;
      this.writeState = 'done'; this.reports = this.reports.filter((item: HealthReport) => item.name !== report.name); return true;
    } catch (failure) {
      if (!owner || !this.current(owner, revision, target)) { if (!owner && this.revision === revision) this.writeState = 'failed'; return false; }
      this.writeState = this.unknownWrite(failure as Error); this.error = this.errorCode(failure as Error); return false;
    } finally { if (this.revision === revision && this.healthClient === client && this.client === root) this.busy = false; }
  }

  canUploadReports(): boolean {
    return !!this.healthClient && !!this.machine && this.reportUploadAllowed && !this.busy;
  }

  canStartConversation(): boolean {
    return !!this.healthClient && !!this.machine && this.reportUploadAllowed && !this.busy &&
      !['pending', 'unknown'].includes(this.conversationWriteState);
  }

  /** Upload one confirmed PDF/photo into this manager's Health reports directory. */
  async uploadReport(name: string, bytes: ArrayBuffer): Promise<string | undefined> {
    const client = this.healthClient, root = this.client;
    const cleanName = name.trim();
    const extension = cleanName.toLowerCase().split('.').pop() || '';
    if (!root || !client) return undefined;
    if (!this.canUploadReports()) {
      this.reportUploadState = 'failed'; this.error = 'health_default_manager_unavailable'; return undefined;
    }
    if (!this.validReportName(cleanName) || !['pdf', 'jpg', 'jpeg', 'png', 'webp', 'gif', 'bmp', 'heic', 'heif', 'avif'].includes(extension) ||
      bytes.byteLength <= 0 || bytes.byteLength > MAX_HEALTH_REPORT_BYTES) {
      this.reportUploadState = 'failed';
      this.error = bytes.byteLength > MAX_HEALTH_REPORT_BYTES ? 'health_report_file_too_large' : 'health_report_file_unsupported';
      return undefined;
    }
    const revision = this.revision;
    const path = '/api/v1/machines/' + segment(this.machine) + '/files?name=' + segment(cleanName) + '&dest=health';
    this.busy = true; this.reportUploadState = 'sending'; this.error = '';
    let owner: HealthOwner | undefined;
    try {
      owner = await this.ownerSnapshot(root, client, path, revision);
      if (!this.current(owner, revision, path)) return undefined;
      if (owner.client.operationBlocked(path)) {
        this.reportUploadState = 'unknown'; this.error = 'result_unknown'; return undefined;
      }
      const machineList = await owner.repository.call('/api/v1/machines');
      if (!this.current(owner, revision, path) || this.machine !== owner.machine) return undefined;
      const expectedMachine = owner.machine;
      const machine = Wire.rows(machineList, 'machines').find((item: Record<string, Object>) => Wire.text(item, 'id') === expectedMachine);
      if (!machine || !this.machineUsable(machine, owner.manager)) {
        this.reportUploadState = 'failed'; this.error = 'health_default_manager_unavailable'; return undefined;
      }
      this.reportUploadAllowed = true;
      const captured = owner;
      const receipt = await captured.repository.upload(path, bytes, (): boolean =>
        this.current(captured, revision, path) && this.owner === captured.manager &&
        this.healthClient === captured.client && this.machine === captured.machine && this.reportUploadAllowed);
      if (!this.current(owner, revision, path)) return undefined;
      const remotePath = Wire.text(receipt, 'path');
      if (!this.validReportPath(remotePath)) {
        // The upload endpoint acknowledged a report but gave no usable location; do not offer a retry.
        this.reportUploadState = 'unknown'; this.error = 'result_unknown'; return undefined;
      }
      this.reportUploadState = 'done';
      try { await this.load(owner, revision); }
      catch (failure) { if (this.current(owner, revision, path)) this.error = this.errorCode(failure as Error); }
      if (!this.current(owner, revision, path) || this.machine !== owner.machine) return undefined;
      return remotePath;
    } catch (failure) {
      if (!owner || !this.current(owner, revision, path)) {
        if (!owner && this.revision === revision && this.healthClient === client) {
          this.reportUploadState = 'failed'; this.error = this.errorCode(failure as Error);
        }
        return undefined;
      }
      this.reportUploadState = this.unknownWrite(failure as Error);
      this.error = this.errorCode(failure as Error);
      return undefined;
    } finally { if (this.revision === revision && this.healthClient === client && this.client === root) this.busy = false; }
  }

  isHealthConversation(row: ResourceRow): boolean {
    const data = row.data();
    return row.kind === 'conversation' && row.id.length > 0 && row.manager === this.owner && row.machine === this.machine &&
      Wire.text(data, 'space') === HEALTH_SPACE;
  }

  canArchiveConversation(row: ResourceRow): boolean {
    return this.isHealthConversation(row) && this.conversations.some((item: ResourceRow) => item.key() === row.key());
  }

  openReport(report: HealthReport, record: boolean): ResourceRow | undefined {
    const path = record ? report.recordPath : report.path;
    if (!path || report.row.manager !== this.owner || report.row.machine !== this.machine ||
      this.reports.every((item: HealthReport) => item.row.key() !== report.row.key())) return undefined;
    const row = new ResourceRow(report.name, report.title, 'file');
    row.manager = this.owner; row.machine = this.machine; row.path = path; row.detail = report.name;
    return row;
  }

  private async resolveDefault(client: ClientModel, revision: number): Promise<HealthOwner> {
    const rootGeneration = client.connectionGeneration(), rootScope = client.scope(), rootManager = client.profile.manager;
    if (!client.connected || client.demo || !client.profile.id) throw new Error('offline');
    const rootRepo = client.repo();
    const roster = await rootRepo.call('/api/v1/managers');
    if (!client.owns(rootGeneration, rootScope) || !client.connected || this.revision !== revision) throw new Error('request_updated');
    const defaultManager = Wire.text(roster, 'default');
    if (!defaultManager) throw new Error('health_default_manager_unavailable');
    const info = Wire.rows(roster, 'managers').find((row: Record<string, Object>) => Wire.text(row, 'id') === defaultManager);
    if (!info || Wire.bool(info, 'installing') || !Wire.bool(info, 'online')) throw new Error('health_default_manager_unavailable');
    const reference = ResourceRow.from(info, 'manager'); reference.manager = defaultManager;
    const view = await client.viewFor(reference);
    if (!client.owns(rootGeneration, rootScope) || this.revision !== revision || view.profile.manager !== defaultManager || !view.connected) {
      throw new Error('request_updated');
    }
    const generation = view.connectionGeneration(), scope = view.scope(), repository = view.repo();
    const target = this.healthTarget(defaultManager);
    const owner = new HealthOwner(client, rootGeneration, rootScope, rootManager, view, repository, generation, scope, defaultManager, target);
    return owner;
  }

  private async ownerSnapshot(root: ClientModel, client: ClientModel, target: string, revision: number): Promise<HealthOwner> {
    const latest = await this.resolveDefault(root, revision);
    if (!this.rootCurrent(root, revision, latest) || !this.current(latest, revision, latest.target) || latest.manager !== this.owner || latest.client !== client)
      throw new Error('request_updated');
    const owner = new HealthOwner(root, latest.rootGeneration, latest.rootScope, latest.rootManager, latest.client, latest.repository,
      latest.generation, latest.scope, latest.manager, target);
    owner.machine = this.machine;
    return owner;
  }

  private healthTarget(manager: string): string { return JSON.stringify([manager, HEALTH_SPACE]); }

  private rootCurrent(root: ClientModel, revision: number, owner: HealthOwner): boolean {
    return this.revision === revision && this.client === root && owner.rootGeneration === this.rootGeneration &&
      owner.rootScope === this.rootScope && owner.rootManager === this.rootManager && root.connected &&
      root.owns(owner.rootGeneration, owner.rootScope) && root.profile.manager === owner.rootManager;
  }

  private current(owner: HealthOwner, revision: number, target: string = owner.target): boolean {
    if (owner.target !== target || !this.rootCurrent(owner.root, revision, owner) ||
      this.healthClient !== undefined && this.healthClient !== owner.client ||
      !owner.root.connected || !owner.root.owns(owner.rootGeneration, owner.rootScope) || owner.root.profile.manager !== owner.rootManager ||
      !owner.client.connected || !owner.client.owns(owner.generation, owner.scope) || owner.client.profile.manager !== owner.manager) return false;
    try { return owner.client.repo() === owner.repository; } catch (_) { return false; }
  }

  private storageCurrent(client: ClientModel, generation: number, scope: string, manager: string, revision: number): boolean {
    return this.revision === revision && this.healthClient === client && client.connected && client.owns(generation, scope) &&
      client.scope() === scope && client.profile.manager === manager && manager === this.owner;
  }

  private clearOwnerState(): void {
    this.healthClient = undefined; this.owner = ''; this.machine = '';
    this.conversations = []; this.memories = []; this.reports = []; this.selectedMemory = undefined; this.memoryDraft = '';
    this.writeState = ''; this.conversationWriteState = ''; this.memoryWriteState = ''; this.reportUploadState = ''; this.reportUploadAllowed = false;
  }

  private async load(owner: HealthOwner, revision: number): Promise<void> {
    // The health list itself supplies the manager's configured assistant machine and health cwd.
    const assistant = await owner.repository.call('/api/v1/assistant?space=' + HEALTH_SPACE);
    if (!this.current(owner, revision, owner.target)) return;
    owner.machine = Wire.text(assistant, 'machine_id'); this.machine = owner.machine;
    if (!owner.machine) throw new Error('health_default_manager_unavailable');
    try {
      const machineList = await owner.repository.call('/api/v1/machines');
      if (!this.current(owner, revision, owner.target)) return;
      const expectedMachine = owner.machine;
      const machine = Wire.rows(machineList, 'machines').find((item: Record<string, Object>) => Wire.text(item, 'id') === expectedMachine);
      this.reportUploadAllowed = !!machine && this.machineUsable(machine, owner.manager);
    } catch (_) { this.reportUploadAllowed = false; }
    const chats = Wire.rows(assistant, 'conversations').filter((data: Record<string, Object>) =>
      !Wire.text(data, 'space') || Wire.text(data, 'space') === HEALTH_SPACE).map((data: Record<string, Object>) => {
      const row = ResourceRow.from(data, 'conversation'); row.manager = owner.manager;
      row.machine = Wire.text(data, 'machine_id', owner.machine); row.session = Wire.text(data, 'session_id', row.id);
      row.path = Wire.text(data, 'cwd');
      const stamp = row.data(); stamp['space'] = HEALTH_SPACE; row.raw = JSON.stringify(stamp);
      return row;
    }).filter((row: ResourceRow) => !row.isDone());
    this.conversations = chats;
    this.error = this.reportUploadAllowed ? '' : 'health_default_manager_unavailable';

    let memoryFailure: Error | undefined;
    try {
      const memory = await owner.repository.call('/api/v1/assistant/memory?space=' + HEALTH_SPACE);
      if (!this.current(owner, revision, owner.target)) return;
      this.memories = Wire.rows(memory, 'files').map((data: Record<string, Object>) => new HealthMemoryFile(
        Wire.text(data, 'name'), Wire.text(data, 'content'), Wire.text(data, 'updated_at'))).filter((file: HealthMemoryFile) => !!file.name);
      this.memoryError = '';
    } catch (failure) { memoryFailure = failure as Error; if (this.current(owner, revision)) this.memoryError = this.errorCode(failure as Error); }

    try {
      const reports = await owner.repository.call('/api/v1/machines/' + segment(owner.machine) + '/health/reports');
      if (!this.current(owner, revision, owner.target)) return;
      this.reports = Wire.rows(reports, 'reports').map((data: Record<string, Object>) => {
        const report = new HealthReport(); report.name = Wire.text(data, 'name'); report.title = Wire.text(data, 'title', report.name);
        report.path = Wire.text(data, 'path'); report.recordPath = Wire.text(data, 'record_path');
        report.size = Wire.number(data, 'size'); report.modifiedAt = Wire.text(data, 'modified_at');
        report.row = new ResourceRow(report.name, report.title, 'health_report'); report.row.manager = owner.manager;
        report.row.machine = owner.machine; report.row.path = report.recordPath || report.path; report.row.detail = report.name;
        return report;
      }).filter((report: HealthReport) => !!report.name);
      if (!memoryFailure && this.reportUploadAllowed) this.error = '';
    } catch (failure) { if (this.current(owner, revision)) this.error = this.errorCode(failure as Error); }
  }

  private async loadMemoryDraft(file: HealthMemoryFile): Promise<void> {
    const client = this.healthClient, revision = this.revision;
    if (!client) return;
    const generation = client.connectionGeneration(), scope = client.scope(), manager = client.profile.manager;
    try {
      const raw = await client.loadView(this.memoryDraftKey(file.name));
      if (!this.storageCurrent(client, generation, scope, manager, revision) || this.selectedMemory?.name !== file.name || !raw) return;
      this.memoryDraft = Wire.text(Wire.parse(raw), 'content', file.content);
    } catch (_) { if (this.revision === revision) this.memoryError = 'health_storage_unavailable'; }
  }

  private memoryDraftKey(name: string): string { return 'health-memory-draft:' + JSON.stringify([this.owner, name]); }

  private clearMemoryDraft(owner: HealthOwner, file: HealthMemoryFile): void {
    if (!this.current(owner, this.revision, owner.target)) return;
    owner.client.saveView(this.memoryDraftKey(file.name), '').catch(() => undefined);
  }

  private async saveWriteState(owner: HealthOwner, revision: number, state: string): Promise<boolean> {
    const client = owner.client, generation = owner.generation, scope = owner.scope, manager = owner.manager;
    const durableState = ['pending', 'unknown'].includes(state) ? state : state === 'sending' ? 'unknown' : '';
    const raw = JSON.stringify(payload(['draft', 'write_state'], [this.draft, durableState]));
    this.draftWrite = this.draftWrite.catch(() => {}).then(async (): Promise<void> => {
      if (!this.storageCurrent(client, generation, scope, manager, revision)) throw new Error('health_storage_unavailable');
      await client.saveView('health-workspace', raw);
      if (!this.storageCurrent(client, generation, scope, manager, revision)) throw new Error('health_storage_unavailable');
    });
    try { await this.draftWrite; return true; }
    catch (failure) {
      if (this.revision === revision && this.healthClient === client) this.error = this.errorCode(failure as Error);
      return false;
    }
  }

  private unknownWrite(error: Error): string {
    const data = error as Object as Record<string, Object>;
    return Wire.bool(data, 'resultPending') ? 'pending' : Wire.bool(data, 'resultUnknown') ? 'unknown' : 'failed';
  }

  private errorCode(error: Error): string {
    const known = ['offline', 'request_updated', 'permission_denied', 'pending_action', 'result_unknown',
      'health_default_manager_unavailable', 'storage_unavailable', 'health_storage_unavailable',
      'health_report_file_too_large', 'health_report_file_unsupported', 'health_report_node_outdated'];
    const data = error as Object as Record<string, Object>;
    const status = Wire.number(data, 'status');
    return error.message === 'node_outdated' ? 'health_report_node_outdated' :
      known.includes(error.message) ? error.message : status === 401 || status === 403 ? 'permission_denied' :
      Wire.bool(data, 'resultPending') ? 'pending' : Wire.bool(data, 'resultUnknown') ? 'result_unknown' : 'health_request_failed';
  }

  private machineUsable(machine: Record<string, Object>, manager: string): boolean {
    return Wire.text(machine, 'id').length > 0 &&
      (!Wire.text(machine, 'manager') || Wire.text(machine, 'manager') === manager) &&
      Wire.bool(machine, 'online') && ['personal', 'work'].includes(Wire.text(machine, 'trust')) &&
      !Wire.bool(machine, 'pending') && !Wire.bool(machine, 'pending_confirm');
  }

  private validReportName(name: string): boolean {
    return !!name && name.length <= 255 && name === name.trim() && name !== '.' && name !== '..' &&
      !/[\/\\\u0000-\u001f]/.test(name);
  }

  private validReportPath(path: string): boolean {
    return (/^\//.test(path) || /^[A-Za-z]:[\\/]/.test(path)) && /[\\/]reports[\\/]/.test(path);
  }
}
