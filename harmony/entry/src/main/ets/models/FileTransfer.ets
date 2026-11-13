import { ClientModel } from './ClientModel';
import type { ClientViewLease } from './ClientModel';
import { ResourceRow, Wire } from './Wire';
import { HubTransport, HttpFailure } from '../platform/HubTransport';
import { util } from '@kit.ArkTS';

const JOURNAL_KEY: string = 'computer-file-transfers-v1';
const TRANSFER_COLLECTION: string = '/api/v1/files/transfer';
const ACTIVE_STATES: string[] = ['creating', 'running'];
const TERMINAL_STATES: string[] = ['done', 'failed', 'cancelled'];

interface FileTransferOwner {
  client: ClientModel;
  view: ClientViewLease;
  generation: number;
  scope: string;
  target: string;
  rowKey: string;
  manager: string;
  sourceMachine: string;
  profileId: string;
  hubId: string;
  journalEpoch: number;
  journal: FileTransferJournal;
}

class FileTransferSourceEndpoint {
  machine: string = '';
  path: string = '';
  cwd: string | undefined = undefined;
}

class FileTransferTargetEndpoint {
  machine: string = '';
  dest: string = 'inbox';
}

class FileTransferSettingUpdate {
  enabled: boolean = false;
}

class FileTransferCreatePayload {
  from: FileTransferSourceEndpoint = new FileTransferSourceEndpoint();
  to: FileTransferTargetEndpoint = new FileTransferTargetEndpoint();
}

class FileTransferJournalRecordData {
  key: string = '';
  id: string = '';
  manager: string = '';
  sourceMachine: string = '';
  sourceName: string = '';
  sourcePath: string = '';
  sourceCwd: string = '';
  targetMachine: string = '';
  targetName: string = '';
  targetInbox: string = '';
  state: string = '';
  bytes: number = 0;
  total: number = 0;
  resultPath: string = '';
  code: string = '';
  error: string = '';
  updatedAt: string = '';
  acknowledged: boolean = false;
  cancelUnknown: boolean = false;
}

class FileTransferJournalData {
  version: number = 1;
  records: FileTransferJournalRecordData[] = [];
}

interface FileTransferJournal {
  key: string;
  epoch: number;
  records: FileTransferRecord[];
  tail: Promise<void>;
  loading: Promise<void> | undefined;
  loaded: boolean;
  error: string;
  subscribers: FileTransfer[];
  settingWrites: Map<string, Promise<boolean>>;
  settingVersions: Map<string, number>;
}

@ObservedV2
export class FileTransferMachine {
  @Trace id: string = '';
  @Trace title: string = '';
  @Trace online: boolean = false;
  @Trace trust: string = '';
}

@ObservedV2
export class FileTransferSetting {
  @Trace machineId: string = '';
  @Trace enabled: boolean = false;
  @Trace inbox: string = '';
  @Trace state: string = 'unknown';
  @Trace error: string = '';
}

@ObservedV2
export class FileTransferRecord {
  @Trace key: string = '';
  @Trace id: string = '';
  @Trace manager: string = '';
  @Trace sourceMachine: string = '';
  @Trace sourceName: string = '';
  @Trace sourcePath: string = '';
  @Trace sourceCwd: string = '';
  @Trace targetMachine: string = '';
  @Trace targetName: string = '';
  @Trace targetInbox: string = '';
  @Trace state: string = 'unknown';
  @Trace bytes: number = 0;
  @Trace total: number = 0;
  @Trace resultPath: string = '';
  @Trace code: string = '';
  @Trace error: string = '';
  @Trace updatedAt: string = '';
  @Trace acknowledged: boolean = false;
  @Trace cancelUnknown: boolean = false;
  @Trace cancelBusy: boolean = false;
}

export function transferMachineTrustedOnline(model: ClientModel | undefined, machineId: string, manager: string): boolean {
  if (!model || !model.connected || model.demo || !machineId) return false;
  const machine = model.machines.find((row: ResourceRow) => row.id === machineId && (!manager || row.manager === manager));
  if (!machine) return false;
  const data = machine.data();
  return Wire.bool(data, 'online') && Wire.text(data, 'trust') !== 'pending' &&
    ['', 'confirmed', 'personal', 'work'].includes(Wire.text(data, 'trust')) &&
    !Wire.bool(data, 'pending_confirm') && !Wire.bool(data, 'pending');
}

function copyRecord(source: Record<string, Object>): FileTransferRecord {
  const record = new FileTransferRecord();
  record.key = Wire.text(source, 'key');
  record.id = Wire.text(source, 'id');
  record.manager = Wire.text(source, 'manager');
  record.sourceMachine = Wire.text(source, 'sourceMachine');
  record.sourceName = Wire.text(source, 'sourceName');
  record.sourcePath = Wire.text(source, 'sourcePath');
  record.sourceCwd = Wire.text(source, 'sourceCwd');
  record.targetMachine = Wire.text(source, 'targetMachine');
  record.targetName = Wire.text(source, 'targetName');
  record.targetInbox = Wire.text(source, 'targetInbox');
  record.state = Wire.text(source, 'state', 'unknown');
  if (!['creating', 'running', 'done', 'failed', 'cancelled', 'unknown'].includes(record.state)) record.state = 'unknown';
  if (record.state === 'creating') { record.state = 'unknown'; record.code = 'create_interrupted'; }
  record.bytes = Wire.number(source, 'bytes');
  record.total = Wire.number(source, 'total');
  record.resultPath = Wire.text(source, 'resultPath');
  record.code = Wire.text(source, 'code') || record.code;
  record.error = Wire.text(source, 'error');
  record.updatedAt = Wire.text(source, 'updatedAt');
  record.acknowledged = Wire.bool(source, 'acknowledged');
  record.cancelUnknown = Wire.bool(source, 'cancelUnknown');
  return record;
}

function transferState(raw: Record<string, Object>): string {
  const state = Wire.text(raw, 'state');
  return ['running', 'done', 'failed', 'cancelled'].includes(state) ? state : '';
}

function timeValue(value: string): number {
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

@ObservedV2
export class FileTransfer {
  private static journals: Map<string, FileTransferJournal> = new Map<string, FileTransferJournal>();

  @Trace targets: FileTransferMachine[] = [];
  @Trace records: FileTransferRecord[] = [];
  @Trace sourceSetting: FileTransferSetting = new FileTransferSetting();
  @Trace targetSetting: FileTransferSetting = new FileTransferSetting();
  @Trace targetId: string = '';
  @Trace journalReady: boolean = false;
  @Trace journalError: string = '';
  @Trace createBusy: boolean = false;

  private model: ClientModel | undefined;
  private viewLease: ClientViewLease | undefined;
  private sharedJournal: FileTransferJournal | undefined;
  private ownerProfileId: string = '';
  private ownerHubId: string = '';
  private ownerJournalEpoch: number = 0;
  private managerId: string = '';
  private sourceMachineId: string = '';
  private sourceName: string = '';
  private sourcePath: string = '';
  private sourceCwd: string = '';
  private ownerScope: string = '';
  private ownerTarget: string = '';
  private ownerRowKey: string = '';
  private ownerGeneration: number = 0;
  private visible: boolean = false;
  private connected: boolean = false;
  private eventRelease: (() => void) | undefined = undefined;
  private pollTimer: number = -1;
  private sourceSettingSeq: number = 0;
  private targetSettingSeq: number = 0;
  private createRecordKey: string = '';
  private visibilityRevision: number = 0;

  bind(model: ClientModel, sourceMachineId: string, managerId: string): void {
    const nextTarget = model.target(), nextScope = model.scope(), nextRowKey = model.selected.key();
    const nextGeneration = model.connectionGeneration();
    const nextProfileId = model.profile.id, nextHubId = model.profile.hubId;
    const nextViewLease = model.captureView(JOURNAL_KEY);
    const nextJournalEpoch = nextViewLease.epoch;
    if (this.model === model && this.ownerTarget === nextTarget && this.ownerScope === nextScope &&
      this.ownerGeneration === nextGeneration && this.ownerProfileId === nextProfileId && this.ownerHubId === nextHubId &&
      this.ownerJournalEpoch === nextJournalEpoch && this.sourceMachineId === sourceMachineId && this.managerId === managerId) return;
    this.hide();
    this.unsubscribeJournal();
    this.model = model;
    this.ownerScope = nextScope;
    this.ownerTarget = nextTarget;
    this.ownerRowKey = nextRowKey;
    this.ownerGeneration = nextGeneration;
    this.ownerProfileId = nextProfileId;
    this.ownerHubId = nextHubId;
    this.ownerJournalEpoch = nextJournalEpoch;
    this.viewLease = nextViewLease;
    this.sourceMachineId = sourceMachineId;
    this.managerId = managerId;
    const journalKey = JSON.stringify([nextProfileId, nextHubId, nextScope, managerId, nextJournalEpoch]);
    this.sharedJournal = FileTransfer.shared(journalKey, nextJournalEpoch);
    this.sourcePath = '';
    this.sourceName = '';
    this.sourceCwd = '';
    this.targetId = '';
    this.targets = [];
    this.records = this.sharedJournal.records.slice();
    this.sourceSetting = new FileTransferSetting();
    this.sourceSetting.machineId = sourceMachineId;
    this.targetSetting = new FileTransferSetting();
    this.journalReady = this.sharedJournal.loaded;
    this.journalError = this.sharedJournal.error;
    this.createBusy = false;
    this.createRecordKey = '';
    this.connected = model.connected;
    this.sourceSettingSeq++;
    this.targetSettingSeq++;
  }

  private static shared(key: string, epoch: number): FileTransferJournal {
    const current = FileTransfer.journals.get(key);
    if (current) return current;
    const journal: FileTransferJournal = { key: key, epoch: epoch, records: [], tail: Promise.resolve(),
      loading: undefined, loaded: false, error: '', subscribers: [], settingWrites: new Map(), settingVersions: new Map() };
    FileTransfer.journals.set(key, journal);
    return journal;
  }

  private owner(): FileTransferOwner | undefined {
    const client = this.model;
    const journal = this.sharedJournal;
    const view = this.viewLease;
    if (!client || !journal || !view) return undefined;
    return { client: client, view: view, generation: this.ownerGeneration, scope: this.ownerScope, target: this.ownerTarget,
      rowKey: this.ownerRowKey, manager: this.managerId, sourceMachine: this.sourceMachineId,
      profileId: this.ownerProfileId, hubId: this.ownerHubId, journalEpoch: this.ownerJournalEpoch, journal: journal };
  }

  private owns(owner: FileTransferOwner, needConnection: boolean = false): boolean {
    const client = this.model;
    return client === owner.client && client.owns(owner.generation, owner.scope) && client.target() === owner.target &&
      client.selected.key() === owner.rowKey && this.managerId === owner.manager && this.sourceMachineId === owner.sourceMachine &&
      (!needConnection || client.connected && this.connected);
  }

  private subscribeJournal(journal: FileTransferJournal): void {
    if (!journal.subscribers.includes(this)) journal.subscribers.push(this);
    this.records = journal.records.slice();
    this.journalReady = journal.loaded;
    this.journalError = journal.error;
  }

  private unsubscribeJournal(): void {
    const journal = this.sharedJournal;
    if (journal) journal.subscribers = journal.subscribers.filter((item: FileTransfer) => item !== this);
  }

  private static publish(journal: FileTransferJournal): void {
    for (const subscriber of journal.subscribers) {
      if (subscriber.sharedJournal !== journal) continue;
      subscriber.records = journal.records.slice();
      subscriber.journalReady = journal.loaded;
      subscriber.journalError = journal.error;
      subscriber.schedulePoll();
    }
  }

  private journalRecords(): FileTransferRecord[] {
    // Observe the published mirror so ArkUI tracks additions as well as record field updates.
    const published = this.records;
    return this.sharedJournal?.records || published;
  }

  private journalFailure(owner: FileTransferOwner): void {
    if (this.sharedJournal === owner.journal) this.journalError = 'journal_unavailable';
  }

  isAvailable(): boolean {
    const owner = this.owner();
    return !!owner && this.owns(owner) && !owner.client.demo && owner.client.features.includes('file_transfer');
  }

  private eligibleMachine(machineId: string): boolean {
    const model = this.model;
    if (!model || !machineId || model.demo || !model.connected || !this.connected) return false;
    const machine = model.machines.find((row: ResourceRow) => row.id === machineId && row.manager === this.managerId);
    return !!machine && transferMachineTrustedOnline(model, machineId, this.managerId);
  }

  refreshTargets(): void {
    const owner = this.owner();
    const model = this.model;
    if (!owner || !model || !this.owns(owner) || !model.features.includes('file_transfer')) { this.targets = []; return; }
    const retained: Map<string, FileTransferMachine> = new Map();
    for (const target of this.targets) retained.set(target.id, target);
    this.targets = model.machines.filter((row: ResourceRow) => row.manager === this.managerId &&
      transferMachineTrustedOnline(model, row.id, this.managerId)).map((row: ResourceRow) => {
        const target = retained.get(row.id) || new FileTransferMachine();
        target.id = row.id;
        target.title = model.machineName(row.id) || row.title || row.id;
        target.online = Wire.bool(row.data(), 'online');
        target.trust = Wire.text(row.data(), 'trust');
        return target;
      });
  }

  async openSource(path: string, name: string, cwd: string): Promise<void> {
    if (!this.isAvailable() || !path || !name || !this.eligibleMachine(this.sourceMachineId)) throw new Error('transfer_unavailable');
    const owner = this.owner();
    if (!owner) throw new Error('transfer_unavailable');
    await Promise.all(Array.from(owner.journal.settingWrites.values()));
    if (!this.owns(owner, true)) throw new Error('request_updated');
    this.sourcePath = path;
    this.sourceName = name;
    this.sourceCwd = cwd;
    this.targetId = '';
    this.targetSetting = new FileTransferSetting();
    this.refreshTargets();
    this.sourceSetting.machineId = this.sourceMachineId;
    await this.loadSetting(this.sourceMachineId, this.sourceSetting);
  }

  async selectTarget(machineId: string): Promise<void> {
    if (this.targetSelectionFrozen()) return;
    if (!this.targets.some((target: FileTransferMachine) => target.id === machineId) || !this.eligibleMachine(machineId)) return;
    this.targetId = machineId;
    this.targetSettingSeq++;
    if (machineId === this.sourceMachineId) {
      this.targetSetting = new FileTransferSetting();
      this.targetSetting.machineId = machineId;
      return;
    }
    this.targetSetting = new FileTransferSetting();
    this.targetSetting.machineId = machineId;
    await this.loadSetting(machineId, this.targetSetting);
  }

  settingForTarget(): FileTransferSetting {
    return this.targetId === this.sourceMachineId ? this.sourceSetting : this.targetSetting;
  }

  sourceMachine(): string { return this.sourceMachineId; }
  sourceFileName(): string { return this.sourceName; }
  sourceFilePath(): string { return this.sourcePath; }
  sourceMachineName(): string { return this.model?.machineName(this.sourceMachineId) || this.sourceMachineId; }
  targetMachineName(): string { return this.targets.find((machine: FileTransferMachine) => machine.id === this.targetId)?.title || this.targetId; }

  latestForSource(): FileTransferRecord | undefined {
    const active = this.activeForSource();
    if (active) return active;
    return this.journalRecords().find((record: FileTransferRecord) => record.manager === this.managerId &&
      record.sourceMachine === this.sourceMachineId && record.sourcePath === this.sourcePath);
  }

  unknownForSource(): FileTransferRecord[] {
    return this.journalRecords().filter((record: FileTransferRecord) => record.manager === this.managerId &&
      record.sourceMachine === this.sourceMachineId && record.sourcePath === this.sourcePath && record.state === 'unknown');
  }

  private hasPendingCreate(machineId: string): boolean {
    return this.journalRecords().some((record: FileTransferRecord) => record.manager === this.managerId &&
      record.sourceMachine === this.sourceMachineId && record.sourcePath === this.sourcePath &&
      record.targetMachine === machineId && record.state === 'creating');
  }

  private activeForSource(): FileTransferRecord | undefined {
    return this.journalRecords().find((record: FileTransferRecord) => record.manager === this.managerId &&
      record.sourceMachine === this.sourceMachineId && record.sourcePath === this.sourcePath &&
      (record.state === 'creating' || record.state === 'running'));
  }

  targetSelectionFrozen(): boolean {
    return !!this.activeForSource() || this.sourceSetting.state === 'loading' || this.sourceSetting.state === 'saving' ||
      this.targetSetting.state === 'loading' || this.targetSetting.state === 'saving';
  }

  progress(record: FileTransferRecord): number {
    if (record.total <= 0) return 0;
    return Math.max(0, Math.min(100, Math.floor(record.bytes * 100 / record.total)));
  }

  private async transportRequest(path: string, method: string, body: string, owner: FileTransferOwner,
    captureResult: boolean = false): Promise<string> {
    if (!this.owns(owner, true)) throw new HttpFailure(0, 'request_updated');
    const transport: HubTransport = owner.client.repo().transport;
    if (!this.owns(owner, true)) throw new HttpFailure(0, 'request_updated');
    const result = await transport.request(path, method, body);
    if (!captureResult && !this.owns(owner)) throw new HttpFailure(0, 'request_updated');
    return result;
  }

  private async loadSetting(machineId: string, setting: FileTransferSetting,
    restoreEnabledOnFailure: boolean = false, previousEnabled: boolean = false, duringWrite: boolean = false): Promise<void> {
    const owner = this.owner();
    const pending = owner?.journal.settingWrites.get(machineId);
    if (owner && pending && !duringWrite) {
      setting.state = 'saving';
      await pending;
      if (!this.owns(owner)) return;
    }
    if (!owner || !this.isAvailable() || !this.eligibleMachine(machineId)) {
      setting.state = 'unknown';
      setting.error = 'transfer_unavailable';
      return;
    }
    const sequence = machineId === this.sourceMachineId ? ++this.sourceSettingSeq : ++this.targetSettingSeq;
    setting.machineId = machineId;
    setting.state = 'loading';
    setting.error = '';
    try {
      const raw = await this.transportRequest('/api/v1/machines/' + encodeURIComponent(machineId) + '/file-transfer/settings', 'GET', '', owner);
      if (!this.owns(owner) || !this.settingCurrent(machineId, setting, sequence)) return;
      const data = Wire.parse(raw);
      if (typeof data['enabled'] !== 'boolean') throw new HttpFailure(0, 'settings_unavailable');
      setting.enabled = Wire.bool(data, 'enabled');
      setting.inbox = Wire.text(data, 'inbox');
      setting.state = 'ready';
      setting.error = '';
    } catch (error) {
      if (!this.owns(owner) || !this.settingCurrent(machineId, setting, sequence)) return;
      if (restoreEnabledOnFailure) setting.enabled = previousEnabled;
      setting.state = 'unknown';
      setting.error = this.errorCode(error as Error);
    }
  }

  private settingCurrent(machineId: string, setting: FileTransferSetting, sequence: number): boolean {
    if (machineId === this.sourceMachineId) return setting === this.sourceSetting && sequence === this.sourceSettingSeq;
    return machineId === this.targetId && setting === this.targetSetting && sequence === this.targetSettingSeq;
  }

  async setEnabled(machineId: string, next: boolean): Promise<boolean> {
    const owner = this.owner();
    if (!owner || owner.journal.settingWrites.has(machineId)) return false;
    const setting = machineId === this.sourceMachineId ? this.sourceSetting : machineId === this.targetId ? this.targetSetting : undefined;
    if (!setting || setting.machineId !== machineId || setting.state !== 'ready' || !this.isAvailable() || !this.eligibleMachine(machineId)) return false;
    if (setting.enabled === next) return true;
    owner.journal.settingVersions.set(machineId, (owner.journal.settingVersions.get(machineId) || 0) + 1);
    const operation = this.writeEnabled(machineId, next);
    owner.journal.settingWrites.set(machineId, operation);
    for (const subscriber of owner.journal.subscribers) {
      if (subscriber === this) continue;
      if (subscriber.sourceMachineId === machineId) {
        subscriber.sourceSettingSeq++;
        subscriber.sourceSetting.state = 'unknown';
      }
      if (subscriber.targetId === machineId) {
        subscriber.targetSettingSeq++;
        subscriber.targetSetting.state = 'unknown';
      }
    }
    FileTransfer.publish(owner.journal);
    try { return await operation; }
    finally {
      if (owner.journal.settingWrites.get(machineId) === operation) owner.journal.settingWrites.delete(machineId);
      FileTransfer.publish(owner.journal);
    }
  }

  private async writeEnabled(machineId: string, next: boolean): Promise<boolean> {
    const owner = this.owner();
    const setting = machineId === this.sourceMachineId ? this.sourceSetting :
      machineId === this.targetId ? this.targetSetting : undefined;
    if (!owner || !setting || setting.machineId !== machineId || setting.state !== 'ready' ||
      !this.isAvailable() || !this.eligibleMachine(machineId)) return false;
    const previous = setting.enabled;
    if (previous === next) return true;
    const sequence = machineId === this.sourceMachineId ? ++this.sourceSettingSeq : ++this.targetSettingSeq;
    const update = new FileTransferSettingUpdate();
    update.enabled = next;
    setting.enabled = next;
    setting.state = 'saving';
    setting.error = '';
    try {
      const result = await this.transportRequest('/api/v1/machines/' + encodeURIComponent(machineId) + '/file-transfer/settings',
        'PUT', JSON.stringify(update), owner);
      if (!this.owns(owner) || !this.settingCurrent(machineId, setting, sequence)) return false;
      let data: Record<string, Object> = {};
      try { data = Wire.parse(result); }
      catch (_) {
        setting.state = 'unknown';
        setting.error = 'settings_unavailable';
        await this.loadSetting(machineId, setting, true, previous, true);
        return false;
      }
      if (typeof data['enabled'] !== 'boolean' || Wire.bool(data, 'enabled') !== next) {
        setting.state = 'unknown';
        setting.error = 'settings_unavailable';
        await this.loadSetting(machineId, setting, true, previous, true);
        return false;
      }
      setting.enabled = next;
      setting.inbox = Wire.text(data, 'inbox');
      setting.state = 'ready';
      setting.error = '';
      return true;
    } catch (error) {
      if (!this.owns(owner) || !this.settingCurrent(machineId, setting, sequence)) return false;
      const failure = error as Error;
      if (failure instanceof HttpFailure && (failure.resultUnknown || failure.resultPending)) {
        setting.state = 'unknown';
        setting.error = this.errorCode(failure as Error);
        await this.loadSetting(machineId, setting, true, previous, true);
      } else {
        setting.enabled = previous;
        setting.state = 'ready';
        setting.error = this.errorCode(failure);
      }
      return false;
    }
  }

  canStart(machineId: string = this.targetId): boolean {
    const active = this.activeForSource();
    const owner = this.owner();
    if (!owner || !this.owns(owner, true) || !this.isAvailable() || !this.journalReady || this.journalError ||
      owner.journal.settingWrites.has(this.sourceMachineId) || owner.journal.settingWrites.has(machineId) ||
      this.createBusy || active || this.hasPendingCreate(machineId) || !this.sourcePath || !this.sourceName || !this.eligibleMachine(this.sourceMachineId) ||
      !this.targets.some((target: FileTransferMachine) => target.id === machineId) || !this.eligibleMachine(machineId)) return false;
    const source = this.sourceSetting;
    const target = machineId === this.sourceMachineId ? source : this.targetSetting;
    return source.machineId === this.sourceMachineId && source.state === 'ready' && source.enabled &&
      source.inbox.length > 0 && target.machineId === machineId && target.state === 'ready' && target.enabled && target.inbox.length > 0;
  }

  requiresDuplicateConfirmation(machineId: string = this.targetId): boolean {
    return this.journalRecords().some((record: FileTransferRecord) => record.manager === this.managerId &&
      record.sourceMachine === this.sourceMachineId && record.sourcePath === this.sourcePath &&
      record.targetMachine === machineId && record.state === 'unknown' && record.acknowledged);
  }

  private hasUnacknowledgedUnknown(machineId: string): boolean {
    return this.journalRecords().some((record: FileTransferRecord) => record.manager === this.managerId &&
      record.sourceMachine === this.sourceMachineId && record.sourcePath === this.sourcePath &&
      record.targetMachine === machineId && record.state === 'unknown' && !record.acknowledged);
  }

  private creationCurrent(owner: FileTransferOwner, record: FileTransferRecord, sourceSequence: number, targetSequence: number,
    sourceVersion: number, targetVersion: number): boolean {
    const source = this.sourceSetting;
    const target = record.targetMachine === record.sourceMachine ? source : this.targetSetting;
    return this.owns(owner, true) && this.isAvailable() && this.sourceSettingSeq === sourceSequence &&
      this.targetSettingSeq === targetSequence && this.eligibleMachine(record.sourceMachine) && this.eligibleMachine(record.targetMachine) &&
      (owner.journal.settingVersions.get(record.sourceMachine) || 0) === sourceVersion &&
      (owner.journal.settingVersions.get(record.targetMachine) || 0) === targetVersion &&
      !owner.journal.settingWrites.has(record.sourceMachine) && !owner.journal.settingWrites.has(record.targetMachine) &&
      source.machineId === record.sourceMachine && source.state === 'ready' && source.enabled && !!source.inbox &&
      target.machineId === record.targetMachine && target.state === 'ready' && target.enabled && target.inbox === record.targetInbox;
  }

  async start(machineId: string, allowDuplicateRisk: boolean = false): Promise<FileTransferRecord> {
    if (this.hasPendingCreate(machineId)) throw new Error('transfer_create_pending');
    if (this.activeForSource()) throw new Error('transfer_active');
    if (!this.canStart(machineId)) throw new Error('transfer_unavailable');
    if (this.hasUnacknowledgedUnknown(machineId)) throw new Error('transfer_unknown_requires_acknowledgement');
    if (this.requiresDuplicateConfirmation(machineId) && !allowDuplicateRisk) throw new Error('duplicate_confirmation_required');
    const owner = this.owner();
    if (!owner) throw new Error('scope_changed');
    const target = this.targets.find((machine: FileTransferMachine) => machine.id === machineId);
    if (!target) throw new Error('target_unavailable');
    const record = new FileTransferRecord();
    record.key = 'local-' + util.generateRandomUUID();
    record.manager = this.managerId;
    record.sourceMachine = this.sourceMachineId;
    record.sourceName = this.sourceName;
    record.sourcePath = this.sourcePath;
    record.sourceCwd = this.sourceCwd;
    record.targetMachine = machineId;
    record.targetName = target.title;
    record.targetInbox = (machineId === this.sourceMachineId ? this.sourceSetting : this.targetSetting).inbox;
    const sourceSequence = this.sourceSettingSeq, targetSequence = this.targetSettingSeq;
    const sourceVersion = owner.journal.settingVersions.get(record.sourceMachine) || 0;
    const targetVersion = owner.journal.settingVersions.get(record.targetMachine) || 0;
    record.state = 'creating';
    owner.journal.records = [record, ...owner.journal.records];
    this.createBusy = true;
    this.createRecordKey = record.key;
    this.journalError = '';
    owner.journal.error = '';
    FileTransfer.publish(owner.journal);
    try {
      await this.persistJournal(owner);
    } catch (_) {
      owner.journal.records = owner.journal.records.filter((item: FileTransferRecord) => item.key !== record.key);
      if (owner.view.valid()) {
        owner.journal.error = 'journal_unavailable';
        FileTransfer.publish(owner.journal);
        if (this.sharedJournal === owner.journal) this.journalError = 'journal_unavailable';
      }
      if (this.createRecordKey === record.key) { this.createBusy = false; this.createRecordKey = ''; }
      throw new Error('journal_unavailable');
    }
    if (!this.creationCurrent(owner, record, sourceSequence, targetSequence, sourceVersion, targetVersion)) {
      if (owner.view.valid()) {
        record.state = 'failed';
        record.code = 'request_updated';
        try { await this.persistJournal(owner); } catch (_) { this.journalFailure(owner); }
      }
      if (this.createRecordKey === record.key) { this.createBusy = false; this.createRecordKey = ''; }
      return record;
    }
    try {
      const payload = new FileTransferCreatePayload();
      payload.from.machine = record.sourceMachine;
      payload.from.path = record.sourcePath;
      payload.from.cwd = record.sourceCwd || undefined;
      payload.to.machine = record.targetMachine;
      const body = JSON.stringify(payload);
      const transport: HubTransport = owner.client.repo().transport;
      if (!this.creationCurrent(owner, record, sourceSequence, targetSequence, sourceVersion, targetVersion)) {
        record.state = 'failed'; record.code = 'request_updated';
        try { await this.persistJournal(owner); } catch (_) { this.journalFailure(owner); }
        return record;
      }
      const raw = await transport.requestAccepted(TRANSFER_COLLECTION, 'POST', body);
      if (!owner.view.valid()) return record;
      record.code = 'create_response_unknown';
      const accepted = Wire.parse(raw);
      const id = Wire.text(accepted, 'id');
      if (!id || id.length > 256 || Wire.text(accepted, 'from') !== record.sourceMachine || Wire.text(accepted, 'to') !== record.targetMachine) {
        record.state = 'unknown';
        record.error = '';
        await this.persistJournal(owner);
        return record;
      }
      record.id = id;
      record.state = 'running';
      record.bytes = Wire.number(accepted, 'bytes');
      record.total = Wire.number(accepted, 'total');
      record.updatedAt = Wire.text(accepted, 'updated_at');
      record.code = '';
      record.error = '';
      try { await this.persistJournal(owner); }
      catch (_) { this.journalFailure(owner); }
      if (this.visible && this.connected && this.owns(owner)) await this.refresh(record.key);
      if (this.visible && this.owns(owner)) this.schedulePoll();
      return record;
    } catch (error) {
      if (!owner.view.valid()) return record;
      const failure = error as Error;
      if (record.code === 'create_response_unknown') {
        record.state = 'unknown';
        record.id = '';
      } else if (failure instanceof HttpFailure && (failure.resultUnknown || failure.resultPending)) {
        record.state = 'unknown';
      } else record.state = 'failed';
      if (record.state !== 'unknown' || record.code !== 'create_response_unknown') record.code = this.errorCode(failure);
      record.error = record.code === 'create_response_unknown' ? '' : failure.message.slice(0, 256);
      try { await this.persistJournal(owner); } catch (_) { this.journalFailure(owner); }
      return record;
    } finally {
      if (this.createRecordKey === record.key) { this.createBusy = false; this.createRecordKey = ''; }
    }
  }

  async acknowledgeUnknown(key: string): Promise<boolean> {
    const owner = this.owner();
    const record = this.journalRecords().find((item: FileTransferRecord) => item.key === key && item.state === 'unknown');
    if (!owner || !record || !this.owns(owner)) return false;
    record.acknowledged = true;
    try { await this.persistJournal(owner); return this.owns(owner); }
    catch (_) { this.journalFailure(owner); return false; }
  }

  async cancel(key: string): Promise<boolean> {
    const owner = this.owner();
    const record = this.journalRecords().find((item: FileTransferRecord) => item.key === key);
    if (!owner || !record || !record.id || record.state !== 'running' || record.cancelBusy || !this.owns(owner, true)) return false;
    record.cancelBusy = true;
    record.cancelUnknown = false;
    const path = TRANSFER_COLLECTION + '/' + encodeURIComponent(record.id);
    let responseReceived: boolean = false;
    try {
      const raw = await this.transportRequest(path, 'DELETE', '', owner, true);
      if (!owner.view.valid()) { record.cancelBusy = false; return false; }
      responseReceived = true;
      const result = Wire.parse(raw);
      if (!this.applyServerRecord(record, result)) {
        record.cancelBusy = false;
        record.cancelUnknown = true;
        record.code = 'transfer_response_invalid';
        try { await this.persistJournal(owner); } catch (_) { this.journalFailure(owner); }
        if (this.owns(owner, true)) await this.refresh(key);
        return false;
      }
      record.cancelBusy = false;
      await this.persistJournal(owner);
      if (this.visible && this.owns(owner)) this.schedulePoll();
      return this.isCancelled(record);
    } catch (error) {
      if (!owner.view.valid()) { record.cancelBusy = false; return false; }
      const failure = error as Error;
      record.cancelBusy = false;
      record.cancelUnknown = responseReceived || (failure instanceof HttpFailure &&
        (failure.resultUnknown || failure.resultPending || failure.status === 404));
      record.code = this.errorCode(failure);
      try { await this.persistJournal(owner); } catch (_) { this.journalFailure(owner); }
      if (record.cancelUnknown) {
        if (this.owns(owner, true)) await this.refresh(key);
      }
      return false;
    }
  }

  private isCancelled(record: FileTransferRecord): boolean { return record.state === 'cancelled'; }

  async refresh(key: string): Promise<boolean> {
    const owner = this.owner();
    const record = this.journalRecords().find((item: FileTransferRecord) => item.key === key && !!item.id);
    if (!owner || !record || !this.owns(owner, true)) return false;
    try {
      const raw = await this.transportRequest(TRANSFER_COLLECTION + '/' + encodeURIComponent(record.id), 'GET', '', owner);
      if (!this.owns(owner)) return false;
      const data = Wire.parse(raw);
      if (!this.applyServerRecord(record, data)) { record.code = 'transfer_response_invalid'; return false; }
      if (TERMINAL_STATES.includes(record.state)) record.cancelUnknown = false;
      await this.persistJournal(owner);
      return true;
    } catch (error) {
      if (!this.owns(owner)) return false;
      if (TERMINAL_STATES.includes(record.state)) return false;
      const failure = error as Error;
      record.code = this.errorCode(failure);
      if (failure instanceof HttpFailure && failure.status === 404) {
        record.state = 'unknown';
        record.acknowledged = false;
      }
      try { await this.persistJournal(owner); } catch (_) { this.journalFailure(owner); }
      return false;
    }
  }

  private applyServerRecord(record: FileTransferRecord, server: Record<string, Object>): boolean {
    if (Wire.text(server, 'id') !== record.id || Wire.text(server, 'from') !== record.sourceMachine ||
      Wire.text(server, 'to') !== record.targetMachine) return false;
    const nextState = transferState(server);
    if (!nextState) return false;
    const nextUpdated = Wire.text(server, 'updated_at');
    if (TERMINAL_STATES.includes(record.state) && !TERMINAL_STATES.includes(nextState)) return true;
    if (TERMINAL_STATES.includes(record.state) && TERMINAL_STATES.includes(nextState) &&
      !nextUpdated && record.state !== nextState) return true;
    if (timeValue(nextUpdated) > 0 && timeValue(record.updatedAt) > timeValue(nextUpdated)) return true;
    const nextBytes = Wire.number(server, 'bytes');
    if (nextState === 'running' && record.state === 'running' &&
      (!nextUpdated || timeValue(nextUpdated) === timeValue(record.updatedAt)) && nextBytes < record.bytes) return true;
    record.state = nextState;
    record.bytes = nextBytes;
    record.total = Wire.number(server, 'total', record.total);
    record.resultPath = Wire.text(server, 'result_path', record.resultPath);
    record.code = Wire.text(server, 'code');
    record.error = Wire.text(server, 'error');
    record.updatedAt = nextUpdated || record.updatedAt;
    if (TERMINAL_STATES.includes(nextState)) record.cancelUnknown = false;
    return true;
  }

  private async persistJournal(owner: FileTransferOwner): Promise<void> {
    const journal = owner.journal;
    const pending = journal.tail.catch(() => {}).then(async (): Promise<void> => {
      if (!owner.view.valid() || !journal.loaded || journal.epoch !== owner.view.epoch) throw new Error('scope_changed');
      const snapshot = new FileTransferJournalData();
      snapshot.records = journal.records.map((record: FileTransferRecord): FileTransferJournalRecordData => {
        const saved = new FileTransferJournalRecordData();
        saved.key = record.key; saved.id = record.id; saved.manager = record.manager;
        saved.sourceMachine = record.sourceMachine; saved.sourceName = record.sourceName;
        saved.sourcePath = record.sourcePath; saved.sourceCwd = record.sourceCwd;
        saved.targetMachine = record.targetMachine; saved.targetName = record.targetName;
        saved.targetInbox = record.targetInbox; saved.state = record.state;
        saved.bytes = record.bytes; saved.total = record.total; saved.resultPath = record.resultPath;
        saved.code = record.code; saved.error = record.error; saved.updatedAt = record.updatedAt;
        saved.acknowledged = record.acknowledged; saved.cancelUnknown = record.cancelUnknown;
        return saved;
      });
      await owner.view.write(JSON.stringify(snapshot));
      if (!owner.view.valid()) throw new Error('scope_changed');
      journal.error = '';
      FileTransfer.publish(journal);
    });
    journal.tail = pending.then(() => {}, () => {});
    try { await pending; }
    catch (_) {
      if (owner.view.valid()) {
        journal.error = 'journal_unavailable';
        FileTransfer.publish(journal);
      }
      throw new Error('journal_unavailable');
    }
  }

  private async loadJournal(owner: FileTransferOwner, visibility: number): Promise<void> {
    if (!this.visibleOwner(owner, visibility)) return;
    const journal = owner.journal;
    this.subscribeJournal(journal);
    if (journal.loaded) return;
    if (!journal.loading) journal.loading = this.readJournal(owner).finally(() => { journal.loading = undefined; });
    await journal.loading;
    if (this.visibleOwner(owner, visibility) && this.sharedJournal === journal) this.subscribeJournal(journal);
  }

  private async readJournal(fallback: FileTransferOwner): Promise<void> {
    const journal = fallback.journal;
    try {
      await journal.tail.catch(() => {});
      if (!fallback.view.valid() || journal.epoch !== fallback.view.epoch) return;
      const raw = await fallback.view.read();
      if (!fallback.view.valid()) return;
      const data = Wire.parse(raw);
      journal.records = Wire.rows(data, 'records').map((item: Record<string, Object>) => copyRecord(item))
        .filter((item: FileTransferRecord) => item.key && item.manager === fallback.manager && item.sourceMachine && item.targetMachine);
      journal.loaded = true;
      journal.error = '';
      FileTransfer.publish(journal);
    } catch (_) {
      if (fallback.view.valid()) {
        journal.loaded = false;
        journal.error = 'journal_unavailable';
        FileTransfer.publish(journal);
      }
    }
  }

  async show(): Promise<void> {
    const owner = this.owner();
    this.visible = true;
    const visibility = ++this.visibilityRevision;
    this.connected = this.model?.connected === true;
    if (!owner || !this.owns(owner)) return;
    this.refreshTargets();
    this.attachEvents(owner);
    await this.loadJournal(owner, visibility);
    if (!this.visibleOwner(owner, visibility)) return;
    if (this.connected && this.sourcePath) await this.refreshSettings();
    if (!this.visibleOwner(owner, visibility)) return;
    if (this.connected) await this.refreshKnown();
  }

  private visibleOwner(owner: FileTransferOwner, visibility: number): boolean {
    return this.visible && this.visibilityRevision === visibility && this.owns(owner) && owner.view.valid();
  }

  hide(): void {
    this.visible = false;
    this.visibilityRevision++;
    this.releaseEvents();
    this.stopPoll();
    this.unsubscribeJournal();
  }

  onConnected(): void {
    const owner = this.owner();
    if (!owner || !this.visible || !this.owns(owner)) return;
    this.connected = true;
    this.refreshTargets();
    this.attachEvents(owner);
    this.refreshSettings().catch(() => {});
    this.refreshKnown().catch(() => {});
  }

  private async refreshSettings(): Promise<void> {
    const owner = this.owner();
    if (!owner || !this.sourcePath || !this.owns(owner, true) || !this.isAvailable()) return;
    await this.loadSetting(this.sourceMachineId, this.sourceSetting);
    if (!this.owns(owner, true) || !this.targetId || this.targetId === this.sourceMachineId) return;
    await this.loadSetting(this.targetId, this.targetSetting);
  }

  onDisconnected(): void {
    this.connected = false;
    this.stopPoll();
    this.releaseEvents();
    if (['ready', 'loading', 'saving'].includes(this.sourceSetting.state)) this.sourceSetting.state = 'unknown';
    if (['ready', 'loading', 'saving'].includes(this.targetSetting.state)) this.targetSetting.state = 'unknown';
  }

  private attachEvents(owner: FileTransferOwner): void {
    if (this.eventRelease || !this.visible || !this.owns(owner)) return;
    this.eventRelease = owner.client.observeEvents((payload: string): void => this.receiveEvent(payload, owner));
  }

  private releaseEvents(): void {
    const release = this.eventRelease;
    this.eventRelease = undefined;
    release?.();
  }

  private receiveEvent(payload: string, owner: FileTransferOwner): void {
    if (!this.visible || !this.owns(owner)) return;
    try {
      const event = Wire.parse(payload);
      if (Wire.text(event, 'type') !== 'file_transfer') return;
      const eventManager = Wire.text(event, 'manager');
      const eventOwner = eventManager || 'assistant';
      const capturedOwner = owner.manager || 'assistant';
      if (eventOwner !== capturedOwner) return;
      const server = Wire.object(event, 'transfer');
      const id = Wire.text(server, 'id');
      if (!id) return;
      const record = this.journalRecords().find((item: FileTransferRecord) => item.id === id && item.manager === owner.manager &&
        item.sourceMachine === Wire.text(server, 'from') && item.targetMachine === Wire.text(server, 'to'));
      if (!record || !this.applyServerRecord(record, server)) return;
      this.persistJournal(owner).catch(() => { if (this.owns(owner)) this.journalError = 'journal_unavailable'; });
      this.schedulePoll();
    } catch (_) { }
  }

  private async refreshKnown(): Promise<void> {
    const owner = this.owner();
    if (!owner || !this.owns(owner, true) || !this.journalReady) return;
    const keys = this.journalRecords().filter((record: FileTransferRecord) => !!record.id && ACTIVE_STATES.includes(record.state))
      .map((record: FileTransferRecord) => record.key);
    for (const key of keys) {
      if (!this.owns(owner, true)) return;
      await this.refresh(key);
    }
    this.schedulePoll();
  }

  private schedulePoll(): void {
    this.stopPoll();
    if (!this.visible || !this.connected || !this.journalReady || !this.journalRecords().some((record: FileTransferRecord) =>
      !!record.id && record.state === 'running')) return;
    this.pollTimer = setTimeout(() => {
      this.pollTimer = -1;
      this.refreshKnown().catch(() => {});
    }, 1000);
  }

  private stopPoll(): void {
    if (this.pollTimer !== -1) clearTimeout(this.pollTimer);
    this.pollTimer = -1;
  }

  private errorCode(error: Error): string {
    if (error instanceof HttpFailure) return error.code;
    return error.message || 'failed';
  }

}
