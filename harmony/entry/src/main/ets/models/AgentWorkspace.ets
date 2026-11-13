import type { ClientModel } from './ClientModel';
import { ResourceRow, Wire } from './Wire';

interface AgentRepository {
  call(path: string, method?: string, body?: Record<string, Object>, operationId?: string, admit?: () => boolean): Promise<Record<string, Object>>;
}

interface AgentOwnerSnapshot {
  root: ClientModel;
  rootScope: string;
  rootGeneration: number;
  revision: number;
  client: ClientModel;
  repo: AgentRepository;
  generation: number;
  scope: string;
  viewScope: string;
  manager: string;
  rowKey: string;
  rowTarget: string;
}

@ObservedV2
export class AgentWorkspaceGroup {
  id: string = '';
  machineId: string = '';
  machineName: string = '';
  repositoryPath: string = '';
  repositoryName: string = '';
  worktreePath: string = '';
  worktreeName: string = '';
  branch: string = '';
  showMachine: boolean = false;
  showRepository: boolean = false;
  showWorktree: boolean = false;
  rows: ResourceRow[] = [];

  constructor(id: string, machineId: string, machineName: string, repositoryPath: string, repositoryName: string,
    worktreePath: string, worktreeName: string, branch: string, rows: ResourceRow[] = []) {
    this.id = id; this.machineId = machineId; this.machineName = machineName; this.repositoryPath = repositoryPath;
    this.repositoryName = repositoryName; this.worktreePath = worktreePath; this.worktreeName = worktreeName;
    this.branch = branch; this.rows = rows;
  }
}

function segment(value: string): string { return encodeURIComponent(value); }
function rowSession(row: ResourceRow): string { return row.session || Wire.text(row.data(), 'session_id') || row.id; }
function rowMachine(row: ResourceRow): string { return row.machine || Wire.text(row.data(), 'machine_id'); }
function rowKey(row: ResourceRow): string { return JSON.stringify(row.group ? [row.group, row.kind, row.manager, rowMachine(row), rowSession(row)] : [row.kind, row.manager, rowMachine(row), rowSession(row)]); }
function rowSpaceKey(row: ResourceRow): string {
  const name = Wire.text(row.data(), 'space_name');
  return name ? rowMachine(row) + '/' + name : '';
}
function pathName(path: string): string {
  const normalized = path.replace(/\\/g, '/').replace(/\/+$/g, '');
  return normalized ? normalized.slice(normalized.lastIndexOf('/') + 1) : '';
}
function cloneRow(row: ResourceRow): ResourceRow {
  const copy = new ResourceRow(row.id, row.title, row.kind);
  copy.group = row.group; copy.detail = row.detail; copy.state = row.state; copy.manager = row.manager; copy.machine = row.machine;
  copy.session = row.session; copy.path = row.path; copy.raw = row.raw;
  return copy;
}
function stringsFrom(value: Record<string, Object>, key: string, optional: boolean = false): string[] {
  if (!Wire.has(value, key) && optional) return [];
  const item = value[key];
  if (!Array.isArray(item)) throw new Error('invalid_preferences');
  const values = item as Object[];
  if (values.some((part: Object) => typeof part !== 'string')) throw new Error('invalid_preferences');
  return values.map((part: Object) => part as string);
}
function removeKey(values: string[], key: string): string[] { return values.filter((value: string) => value !== key); }
function toggleKey(values: string[], key: string): string[] {
  return values.includes(key) ? removeKey(values, key) : values.concat([key]);
}

class AgentPreferences {
  pinsSpaces: string[] = []; pinsSessions: string[] = [];
  mutesSpaces: string[] = []; mutesSessions: string[] = []; mutesAllowed: string[] = [];
}

/** Native Agents list state and the hub's pins, mutes, and read receipts. */
@ObservedV2
export class AgentWorkspace {
  @Trace groups: AgentWorkspaceGroup[] = [];
  @Trace quickFilter: string = 'all';
  @Trace groupingMode: string = 'flat';
  @Trace machineFilter: string = '';
  @Trace search: string = '';
  @Trace history: boolean = false;
  @Trace selectedKey: string = '';
  @Trace loading: boolean = false;
  @Trace busy: boolean = false;
  @Trace error: string = '';
  @Trace pinsSpaces: string[] = [];
  @Trace pinsSessions: string[] = [];
  @Trace mutesSpaces: string[] = [];
  @Trace mutesSessions: string[] = [];
  @Trace mutesAllowed: string[] = [];

  private client: ClientModel | undefined;
  private preferences: Map<string, AgentPreferences> = new Map();
  private owner: string = '';
  private scope: string = '';
  private viewScope: string = '';
  private revision: number = 0;
  private sourceRows: ResourceRow[] = [];
  private externalSelection: ResourceRow | undefined;
  private historyRows: ResourceRow[] = [];
  private historyLoaded: boolean = false;
  private frozenGroups: AgentWorkspaceGroup[] | undefined;
  private readingDetail: boolean = false;
  private openingView: boolean = false;
  private freezeSuppressed: boolean = false;
  private historyRequest: number = 0;
  private persistTimer: number = -1;

  historySupported(): boolean { return !!this.client?.directoryOwners().some((owner: ClientModel) => owner.features.includes('agent_session_archives')); }
  rowOnline(row: ResourceRow): boolean { return this.ownerFor(row)?.connected === true; }
  hasRows(): boolean { return this.groups.some((group: AgentWorkspaceGroup) => group.rows.length > 0); }
  visibleRows(): ResourceRow[] {
    const rows: ResourceRow[] = [];
    this.groups.forEach((group: AgentWorkspaceGroup) => group.rows.forEach((row: ResourceRow) => rows.push(row)));
    return rows;
  }
  isSelected(row: ResourceRow): boolean { return this.selectedKey === rowKey(row); }
  rowMetadata(row: ResourceRow): string[] {
    const data = row.data(), machineId = rowMachine(row);
    const path = row.path || Wire.text(data, 'worktree') || Wire.text(data, 'repo_root') || Wire.text(data, 'cwd');
    const owner = this.ownerFor(row);
    const machine = machineId ? owner?.machineName(machineId) || machineId : '';
    const identity = this.client ? [this.client.groupName(row), row.manager].filter((value: string) => !!value).join(' · ') : '';
    const shortPath = path && this.client ? this.client.shortPath(path) : path;
    const state = row.state || Wire.text(data, 'status');
    const content = Wire.text(data, 'last_text') || row.detail;
    return [identity, machine, shortPath, state, content];
  }

  listMetadata(row: ResourceRow): string[] {
    const metadata = this.rowMetadata(row);
    const machineId = rowMachine(row);
    return [this.client?.groupName(row) || '', metadata[1] === machineId ? '' : metadata[1], metadata[3], metadata[4]];
  }

  async open(client: ClientModel, readingDetail: boolean = false): Promise<void> {
    const revision = ++this.revision;
    this.client = client; this.scope = client.scope(); this.owner = client.profile.manager;
    this.viewScope = JSON.stringify([this.scope, this.owner]);
    this.openingView = true; this.loading = true; this.busy = false; this.error = '';
    this.externalSelection = undefined; this.groups = []; this.sourceRows = []; this.historyRows = []; this.historyLoaded = false; this.frozenGroups = undefined;
    this.quickFilter = 'all'; this.groupingMode = 'flat'; this.machineFilter = ''; this.search = ''; this.history = false; this.selectedKey = '';
    this.readingDetail = false; this.freezeSuppressed = false;
    this.preferences.clear(); this.pinsSpaces = []; this.pinsSessions = []; this.mutesSpaces = []; this.mutesSessions = []; this.mutesAllowed = [];
    try {
      const saved = Wire.parse(await client.loadView('agent-workspace'));
      if (!this.loadCurrent(client, revision)) return;
      this.quickFilter = ['all', 'needs_you', 'working', 'unread'].includes(Wire.text(saved, 'quick')) ? Wire.text(saved, 'quick') : 'all';
      this.groupingMode = ['flat', 'machine', 'repository', 'worktree'].includes(Wire.text(saved, 'grouping')) ? Wire.text(saved, 'grouping') : 'flat';
      this.machineFilter = Wire.text(saved, 'machine'); this.search = Wire.text(saved, 'search');
      this.history = Wire.bool(saved, 'history');
      if (this.history && client.connected && !this.historySupported()) this.history = false;
      this.selectedKey = Wire.text(saved, 'selected');
      this.sourceRows = this.history ? this.historyRows.slice() : this.rowsForClient(client);
      this.syncSelected(client);
      this.rebuild();
      this.setReadingDetail(readingDetail);
      if (client.connected) {
        await client.refreshDirectory();
        if (!this.loadCurrent(client, revision)) return;
        this.sourceRows = this.history ? this.historyRows.slice() : this.rowsForClient(client); this.rebuild();
        await this.refreshDirectoryPreferences(client, revision);
        if (!this.loadCurrent(client, revision)) return;
        if (this.history) await this.loadHistory(client, revision);
      }
    } catch (failure) {
      if (this.loadCurrent(client, revision)) this.fail(failure as Error);
    } finally {
      if (this.loadCurrent(client, revision)) { this.openingView = false; this.loading = false; this.rebuild(); }
    }
  }

  hide(): void {
    this.revision++; this.openingView = false; this.client = undefined; this.groups = []; this.sourceRows = []; this.historyRows = []; this.historyLoaded = false;
    this.frozenGroups = undefined; this.readingDetail = false;
    if (this.persistTimer !== -1) clearTimeout(this.persistTimer);
    this.persistTimer = -1;
  }

  update(client: ClientModel, readingDetail: boolean = this.readingDetail): void {
    const nextScope = JSON.stringify([client.scope(), client.profile.manager]);
    if (this.client !== client || this.viewScope !== nextScope) {
      this.open(client, readingDetail).catch(() => undefined);
      return;
    }
    if (this.history && client.connected && !this.historySupported()) {
      this.history = false; this.sourceRows = this.rowsForClient(client); this.saveViewLater();
    }
    this.syncSelected(client);
    this.setReadingDetail(readingDetail);
    this.sourceRows = this.history ? this.historyRows.slice() : this.rowsForClient(client);
    this.rebuild();
  }

  async refresh(client: ClientModel): Promise<void> {
    const nextScope = JSON.stringify([client.scope(), client.profile.manager]);
    if (this.client !== client || this.viewScope !== nextScope) { await this.open(client, this.readingDetail); return; }
    this.update(client, this.readingDetail);
    const snapshot = this.capture(client);
    if (!snapshot) return;
    try {
      await client.refreshDirectory();
      if (!this.ownerActive(snapshot)) return;
      this.sourceRows = this.history ? this.historyRows.slice() : this.rowsForClient(client); this.rebuild();
      await this.refreshDirectoryPreferences(client, this.revision);
      if (this.history) await this.loadHistory(client, this.revision);
    } catch (failure) { if (this.ownerActive(snapshot)) this.fail(failure as Error); }
  }

  setReadingDetail(value: boolean): void {
    if (value === this.readingDetail) return;
    if (value) {
      this.rebuild();
      this.readingDetail = true;
      this.freezeSuppressed = false;
      this.frozenGroups = this.sourceReady() ? this.copyGroups(this.groups) : undefined;
    } else {
      this.readingDetail = false;
      this.freezeSuppressed = false;
      this.frozenGroups = undefined;
      this.rebuild();
    }
  }

  setSearch(value: string): void {
    this.releaseFreeze(); this.search = value; this.rebuild(); this.refreezeAfterFilter(); this.saveViewLater();
  }

  setQuickFilter(value: string): void {
    if (!['all', 'needs_you', 'working', 'unread'].includes(value)) return;
    this.releaseFreeze(); this.quickFilter = value; this.rebuild(); this.refreezeAfterFilter(); this.saveViewLater();
  }

  setGroupingMode(value: string): void {
    if (!['flat', 'machine', 'repository', 'worktree'].includes(value) || value === this.groupingMode) return;
    this.releaseFreeze(); this.groupingMode = value; this.rebuild(); this.refreezeAfterFilter(); this.saveViewLater();
  }

  async setMachineFilter(value: string): Promise<void> {
    this.releaseFreeze(); this.machineFilter = value; this.rebuild(); this.saveViewLater();
    if (this.history) {
      const client = this.client;
      if (client) await this.loadHistory(client, this.revision, true);
      else this.refreezeAfterFilter();
    } else {
      this.refreezeAfterFilter();
    }
  }

  async setHistory(value: boolean): Promise<void> {
    if (value && !this.historySupported()) return;
    this.releaseFreeze();
    this.historyRequest++; this.loading = false;
    this.history = value;
    this.error = '';
    this.sourceRows = value ? [] : this.rowsForClient(this.client);
    this.rebuild(); this.saveViewLater();
    if (value && this.client) await this.loadHistory(this.client, this.revision, true);
    else this.refreezeAfterFilter();
  }

  actionTicket(row: ResourceRow): () => boolean { const snapshot = this.captureRow(row, this.sessionPath(row)); return (): boolean => !!snapshot && this.active(snapshot); }
  forgetHub(group: string): void {
    this.historyRequest++; this.loading = false;
    const keep = (row: ResourceRow): boolean => row.group !== group;
    this.sourceRows = this.sourceRows.filter(keep); this.historyRows = this.historyRows.filter(keep);
    if (this.externalSelection?.group === group) { this.externalSelection = undefined; this.selectedKey = ''; }
    if (this.frozenGroups) this.frozenGroups = this.frozenGroups.map((source: AgentWorkspaceGroup) => { const copy = this.copyGroup(source); copy.rows = copy.rows.filter(keep); return copy; }).filter((source: AgentWorkspaceGroup) => source.rows.length > 0);
    this.preferences.clear(); this.rebuild();
  }
  selectExternal(row: ResourceRow): void { this.externalSelection = row; if (this.client) this.syncSelected(this.client); }

  selectRow(row: ResourceRow): void {
    if (row.kind !== 'session' || !this.groups.some((group: AgentWorkspaceGroup) =>
      group.rows.some((item: ResourceRow) => rowKey(item) === rowKey(row)))) return;
    this.externalSelection = row; this.selectedKey = rowKey(row); this.saveViewLater();
  }

  isPinned(row: ResourceRow): boolean { return this.preferencesFor(row).pinsSessions.includes(this.sessionKey(row)); }
  isSpacePinned(row: ResourceRow): boolean { const key = rowSpaceKey(row); return !!key && this.preferencesFor(row).pinsSpaces.includes(key); }
  isSpaceMuted(row: ResourceRow): boolean { const key = rowSpaceKey(row); return !!key && this.preferencesFor(row).mutesSpaces.includes(key); }
  isMuted(row: ResourceRow): boolean {
    const key = this.sessionKey(row), space = rowSpaceKey(row);
    const preferences = this.preferencesFor(row);
    return preferences.mutesSessions.includes(key) || (!preferences.mutesAllowed.includes(key) && !!space && preferences.mutesSpaces.includes(space));
  }

  canRename(row: ResourceRow): boolean {
    if (!this.canMutateSession(row)) return false;
    const data = row.data(), source = Wire.text(data, 'source_kind');
    if (!source) return true;
    return Wire.bool(Wire.object(data, 'capabilities'), 'terminal');
  }

  canClose(row: ResourceRow): boolean {
    if (!this.canMutateSession(row)) return false;
    const data = row.data(), source = Wire.text(data, 'source_kind'), capabilities = Wire.object(data, 'capabilities');
    if (Wire.has(capabilities, 'close')) return Wire.bool(capabilities, 'close');
    return !source || Wire.bool(capabilities, 'terminal');
  }

  canDelete(row: ResourceRow): boolean {
    if (!this.canMutateSession(row) || this.history) return false;
    const data = row.data();
    return !Wire.bool(data, 'assistant') && !Wire.bool(data, 'starting') &&
      ['offline', 'done', 'idle', 'unknown', 'interrupted'].includes(Wire.text(data, 'status'));
  }

  canMarkSeen(row: ResourceRow): boolean { return this.rowBelongs(row) && row.state === 'unread'; }
  readOnly(row: ResourceRow): boolean {
    const data = row.data();
    return !!Wire.text(data, 'read_only_reason') || Wire.bool(data, 'read_only') || Wire.text(data, 'source_kind') === 'codex_history';
  }

  async togglePin(row: ResourceRow, space: boolean = false): Promise<boolean> {
    const key = space ? rowSpaceKey(row) : this.sessionKey(row);
    return key ? this.changePreference('pins', key, space, row) : false;
  }

  async toggleMute(row: ResourceRow, space: boolean = false): Promise<boolean> {
    const key = space ? rowSpaceKey(row) : this.sessionKey(row);
    if (!key) return false;
    const snapshot = this.captureRow(row, '/api/v1/mutes');
    if (!snapshot || this.blocked(snapshot, '/api/v1/mutes')) return false;
    if (space) return this.changePreference('mutes', key, true, row);
    if (this.busy) return false;
    this.busy = true; this.error = '';
    try {
      const current = await snapshot.repo.call('/api/v1/mutes');
      if (!this.active(snapshot) || !this.rowBelongs(row)) return false;
      const spaces = stringsFrom(current, 'spaces');
      const sessions = stringsFrom(current, 'sessions');
      const allowed = stringsFrom(current, 'allowed', true);
      const spaceKey = rowSpaceKey(row), inherited = !!spaceKey && spaces.includes(spaceKey);
      const muted = sessions.includes(key) || (!allowed.includes(key) && inherited);
      const nextSessions = muted ? removeKey(sessions, key) : toggleKey(sessions, key);
      const nextAllowed = muted && inherited ? togglePresent(allowed, key) : removeKey(allowed, key);
      const body = Wire.payload(['spaces', 'sessions', 'allowed'], [spaces, nextSessions, nextAllowed]);
      if (!this.active(snapshot) || this.blocked(snapshot, '/api/v1/mutes')) return false;
      await snapshot.repo.call('/api/v1/mutes', 'PUT', body, '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      this.applyMutes(spaces, nextSessions, nextAllowed, snapshot.client); return true;
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  async rename(row: ResourceRow, title: string, tabName: string): Promise<boolean> {
    if (!this.canRename(row)) return false;
    const data = row.data(), priorTitle = Wire.text(data, 'title'), priorTab = Wire.text(data, 'slot_name');
    const nextTitle = title.trim(), nextTab = tabName.trim();
    const keys: string[] = [], values: Object[] = [];
    if (nextTitle !== priorTitle && nextTitle) { keys.push('title'); values.push(nextTitle); }
    if (Wire.text(data, 'pane_id') && nextTab !== priorTab && nextTab) { keys.push('tab_name'); values.push(nextTab); }
    if (!keys.length) return false;
    const path = this.sessionPath(row) + '/rename', snapshot = this.captureRow(row, path);
    if (!snapshot || this.blocked(snapshot, path) || this.busy) return false;
    this.busy = true; this.error = '';
    try {
      await snapshot.repo.call(path, 'POST', Wire.payload(keys, values), '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      await this.refreshAfterMutation(snapshot);
      return this.ownerActive(snapshot);
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  async close(row: ResourceRow): Promise<boolean> {
    if (!this.canClose(row)) return false;
    const path = this.sessionPath(row) + '/close', snapshot = this.captureRow(row, path);
    if (!snapshot || this.blocked(snapshot, path) || this.busy) return false;
    this.busy = true; this.error = '';
    try {
      await snapshot.repo.call(path, 'POST', Wire.payload([], []), '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      await this.refreshAfterMutation(snapshot);
      return this.ownerActive(snapshot);
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  async delete(row: ResourceRow): Promise<boolean> {
    if (!this.canDelete(row)) return false;
    const path = this.sessionPath(row), snapshot = this.captureRow(row, path);
    if (!snapshot || this.blocked(snapshot, path) || this.busy) return false;
    this.busy = true; this.error = '';
    try {
      await snapshot.repo.call(path, 'DELETE', Wire.payload([], []), '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      await this.refreshAfterMutation(snapshot);
      return this.ownerActive(snapshot);
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  async markSeen(row: ResourceRow): Promise<boolean> {
    if (!this.canMarkSeen(row) || this.busy) return false;
    const path = this.sessionPath(row) + '/seen', snapshot = this.captureRow(row, path);
    if (!snapshot || this.blocked(snapshot, path)) return false;
    this.busy = true; this.error = '';
    try {
      await snapshot.repo.call(path, 'POST', Wire.payload([], []), '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      this.patchState(row, 'idle');
      return true;
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  machineIdentity(row: ResourceRow): string { return JSON.stringify([row.group || this.client?.profile.id || '', row.manager, row.kind === 'machine' ? row.id : rowMachine(row)]); }

  private sessionKey(row: ResourceRow): string {
    const machine = rowMachine(row), session = rowSession(row);
    return machine && session ? machine + '/' + session : '';
  }

  private sessionPath(row: ResourceRow): string { return '/api/v1/sessions/' + segment(rowMachine(row)) + '/' + segment(rowSession(row)); }

  private rowBelongs(row: ResourceRow): boolean {
    if (row.kind !== 'session' || !rowMachine(row) || !rowSession(row)) return false;
    return this.sourceRows.some((item: ResourceRow) => rowKey(item) === rowKey(row));
  }

  private canMutateSession(row: ResourceRow): boolean {
    if (!this.client || this.history || !this.rowBelongs(row) || this.readOnly(row)) return false;
    const owner = this.ownerFor(row); if (!owner || !owner.connected) return false;
    const machine = owner.machines.find((item: ResourceRow) => item.id === rowMachine(row) && item.manager === row.manager);
    if (!machine) return false;
    const trust = Wire.text(machine.data(), 'trust');
    return ['', 'confirmed', 'personal', 'work'].includes(trust);
  }

  private ownerFor(row: ResourceRow): ClientModel | undefined { return this.client?.directoryOwnerFor(row); }
  private capture(client: ClientModel): AgentOwnerSnapshot | undefined {
    const root = this.client;
    if (!root || !client.connected || root.scope() !== this.scope) return undefined;
    try {
      const repo = client.repo() as AgentRepository;
      return { root: root, rootScope: root.scope(), rootGeneration: root.connectionGeneration(), revision: this.revision,
        client: client, repo: repo, generation: client.connectionGeneration(), scope: client.scope(),
        viewScope: this.viewScope, manager: client.profile.manager, rowKey: '', rowTarget: '' };
    } catch (_) { return undefined; }
  }
  private captureRow(row: ResourceRow, path: string): AgentOwnerSnapshot | undefined {
    const client = this.ownerFor(row);
    if (!client || !this.rowBelongs(row)) return undefined;
    const snapshot = this.capture(client); if (!snapshot) return undefined;
    if (client.operationBlocked(path)) { this.error = 'pending_action'; return undefined; }
    snapshot.rowKey = rowKey(row); snapshot.rowTarget = client.target(row); return snapshot;
  }
  private active(snapshot: AgentOwnerSnapshot): boolean {
    if (!this.ownerActive(snapshot)) return false;
    if (snapshot.rowKey) {
      const row = this.findSourceRow(snapshot.rowKey);
      if (!row || snapshot.client.target(row) !== snapshot.rowTarget || this.ownerFor(row) !== snapshot.client) return false;
    }
    return true;
  }
  private ownerActive(snapshot: AgentOwnerSnapshot): boolean {
    if (this.client !== snapshot.root || this.revision !== snapshot.revision || this.viewScope !== snapshot.viewScope ||
      !snapshot.root.owns(snapshot.rootGeneration, snapshot.rootScope) || !snapshot.client.owns(snapshot.generation, snapshot.scope) ||
      !snapshot.client.connected || snapshot.client.profile.manager !== snapshot.manager) return false;
    try { return snapshot.client.repo() === snapshot.repo; } catch (_) { return false; }
  }
  private preferencesFor(row: ResourceRow): AgentPreferences {
    const owner = this.ownerFor(row), saved = owner ? this.preferences.get(owner.scope()) : undefined;
    if (saved) return saved;
    const preferences = new AgentPreferences();
    if (owner === this.client) { preferences.pinsSpaces = this.pinsSpaces; preferences.pinsSessions = this.pinsSessions; preferences.mutesSpaces = this.mutesSpaces; preferences.mutesSessions = this.mutesSessions; preferences.mutesAllowed = this.mutesAllowed; }
    return preferences;
  }
  private async refreshDirectoryPreferences(client: ClientModel, revision: number): Promise<void> {
    const owners = new Set<ClientModel>(client.directoryOwners());
    for (const owner of owners) {
      if (!this.loadCurrent(client, revision)) return;
      const snapshot = this.capture(owner); if (snapshot) { try { await this.refreshPreferences(snapshot); } catch (failure) { if (this.ownerActive(snapshot)) this.fail(failure as Error); } }
    }
  }
  private findSourceRow(key: string): ResourceRow | undefined { return this.sourceRows.find((row: ResourceRow) => rowKey(row) === key); }

  private blocked(snapshot: AgentOwnerSnapshot, path: string): boolean {
    if (!this.active(snapshot)) return true;
    if (snapshot.client.operationBlocked(path)) { this.error = 'pending_action'; return true; }
    return false;
  }

  private loadCurrent(client: ClientModel, revision: number): boolean {
    return this.client === client && this.revision === revision && this.scope === client.scope() && this.owner === client.profile.manager;
  }

  private async refreshPreferences(snapshot: AgentOwnerSnapshot): Promise<void> {
    const pins = await snapshot.repo.call('/api/v1/pins');
    if (!this.ownerActive(snapshot)) return;
    const mutes = await snapshot.repo.call('/api/v1/mutes');
    if (!this.ownerActive(snapshot)) return;
    this.applyPins(stringsFrom(pins, 'spaces'), stringsFrom(pins, 'sessions'), snapshot.client);
    this.applyMutes(stringsFrom(mutes, 'spaces'), stringsFrom(mutes, 'sessions'), stringsFrom(mutes, 'allowed', true), snapshot.client);
    this.rebuild();
  }

  private async changePreference(kind: string, key: string, space: boolean, row: ResourceRow): Promise<boolean> {
    if (!this.client || this.busy) return false;
    const path = '/api/v1/' + kind, snapshot = this.captureRow(row, path);
    if (!snapshot || this.blocked(snapshot, path)) return false;
    this.busy = true; this.error = '';
    try {
      const current = await snapshot.repo.call(path);
      if (!this.active(snapshot)) return false;
      const spaces = stringsFrom(current, 'spaces'), sessions = stringsFrom(current, 'sessions');
      if (kind === 'pins') {
        const nextSpaces = space ? toggleKey(spaces, key) : spaces;
        const nextSessions = space ? sessions : toggleKey(sessions, key);
        const body = Wire.payload(['spaces', 'sessions'], [nextSpaces, nextSessions]);
        if (!this.active(snapshot) || this.blocked(snapshot, path)) return false;
        await snapshot.repo.call(path, 'PUT', body, '', (): boolean => this.active(snapshot));
        if (!this.ownerActive(snapshot)) return false;
        this.applyPins(nextSpaces, nextSessions, snapshot.client); return true;
      }
      const allowed = stringsFrom(current, 'allowed', true);
      const nextSpaces = space ? toggleKey(spaces, key) : spaces;
      const nextSessions = space ? sessions : toggleKey(sessions, key);
      const nextAllowed = space ? allowed : removeKey(allowed, key);
      const body = Wire.payload(['spaces', 'sessions', 'allowed'], [nextSpaces, nextSessions, nextAllowed]);
      if (!this.active(snapshot) || this.blocked(snapshot, path)) return false;
      await snapshot.repo.call(path, 'PUT', body, '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      this.applyMutes(nextSpaces, nextSessions, nextAllowed, snapshot.client); return true;
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  private applyPins(spaces: string[], sessions: string[], owner: ClientModel): void {
    const preferences = this.preferences.get(owner.scope()) || new AgentPreferences(); preferences.pinsSpaces = spaces.slice(); preferences.pinsSessions = sessions.slice(); this.preferences.set(owner.scope(), preferences);
    if (owner === this.client) { this.pinsSpaces = spaces.slice(); this.pinsSessions = sessions.slice(); } this.rebuild();
  }
  private applyMutes(spaces: string[], sessions: string[], allowed: string[], owner: ClientModel): void {
    const preferences = this.preferences.get(owner.scope()) || new AgentPreferences(); preferences.mutesSpaces = spaces.slice(); preferences.mutesSessions = sessions.slice(); preferences.mutesAllowed = allowed.slice(); this.preferences.set(owner.scope(), preferences);
    if (owner === this.client) { this.mutesSpaces = spaces.slice(); this.mutesSessions = sessions.slice(); this.mutesAllowed = allowed.slice(); } this.rebuild();
  }

  private async refreshAfterMutation(snapshot: AgentOwnerSnapshot): Promise<void> {
    try { await snapshot.client.refresh(); } catch (_) { /* the acknowledged mutation stays authoritative; a later list event refreshes rows */ }
  }

  private patchState(row: ResourceRow, state: string): void {
    const key = rowKey(row);
    const patch = (items: ResourceRow[]): ResourceRow[] => items.map((item: ResourceRow) => {
      if (rowKey(item) !== key) return item;
      const next = cloneRow(item), data = next.data(); data['state'] = state; next.raw = JSON.stringify(data); next.state = state; return next;
    });
    this.sourceRows = patch(this.sourceRows); this.historyRows = patch(this.historyRows); this.rebuild();
  }

  private async loadHistory(client: ClientModel, revision: number, refreezeAfterLoad: boolean = false): Promise<void> {
    if (!this.history || !this.historySupported() || client.demo) { if (refreezeAfterLoad) this.refreezeAfterFilter(); return; }
    const request = ++this.historyRequest, machine = this.machineFilter;
    const owners = new Set<ClientModel>(client.directoryOwners());
    this.loading = true; this.error = ''; const rows: ResourceRow[] = [];
    try {
      for (const owner of owners) {
        if (!this.loadCurrent(client, revision) || request !== this.historyRequest || machine !== this.machineFilter) return;
        if (!owner.features.includes('agent_session_archives')) continue;
        const snapshot = this.capture(owner); if (!snapshot) continue;
        const filters = owner.profile.manager ? ['manager=' + segment(owner.profile.manager)] : [];
        if (machine) {
          if (machine.startsWith('[')) {
            const match = owner.machines.find((row: ResourceRow) => this.machineIdentity(row) === machine); if (!match) continue;
            filters.unshift('machine_id=' + segment(match.id));
          } else filters.unshift('machine_id=' + segment(machine));
        }
        const path = '/api/v1/sessions/archived' + (filters.length ? '?' + filters.join('&') : '');
        try {
          const data = await snapshot.repo.call(path);
          if (!this.loadCurrent(client, revision) || !this.ownerActive(snapshot) || request !== this.historyRequest || machine !== this.machineFilter) return;
          for (const item of Wire.rows(data, 'sessions')) {
            const row = ResourceRow.from(item, 'session'); if (!row.manager) row.manager = owner.profile.manager;
            row.group = owner.profile.id || ''; if (row.manager === owner.profile.manager && !Wire.bool(row.data(), 'assistant')) rows.push(row);
          }
        } catch (failure) { if (this.ownerActive(snapshot)) this.fail(failure as Error); }
      }
      if (!this.loadCurrent(client, revision) || request !== this.historyRequest || machine !== this.machineFilter) return;
      if ((refreezeAfterLoad || !this.historyLoaded) && this.readingDetail) { this.freezeSuppressed = false; this.frozenGroups = undefined; }
      this.historyRows = rows; this.historyLoaded = true; this.sourceRows = rows.slice(); this.rebuild();
    } finally { if (this.loadCurrent(client, revision) && request === this.historyRequest) { this.loading = false; if (refreezeAfterLoad && this.freezeSuppressed) this.refreezeAfterFilter(); } }
  }

  private rowsForClient(client: ClientModel | undefined): ResourceRow[] {
    if (!client) return [];
    return (client.directoryReady ? client.directorySessions : client.sessions).filter((row: ResourceRow) => row.kind === 'session' &&
      !Wire.bool(row.data(), 'archived') && !Wire.bool(row.data(), 'assistant'));
  }

  private syncSelected(client: ClientModel): void {
    const selected = this.externalSelection || client.selected;
    if (!selected.kind) { if (this.externalSelection) this.selectedKey = ''; return; }
    const next = selected.kind === 'session' && this.sourceRows.some((row: ResourceRow) => rowKey(row) === rowKey(selected)) ? rowKey(selected) : '';
    if (next === this.selectedKey) return;
    this.selectedKey = next; this.saveViewLater();
  }

  private releaseFreeze(): void {
    if (!this.readingDetail) return;
    this.freezeSuppressed = true; this.frozenGroups = undefined;
  }

  private sourceReady(): boolean { return !this.openingView && (this.history ? this.historyLoaded : !!this.client && (this.client.directoryReady || this.client.sessionsReady) && !this.client.directoryLoading); }

  private refreezeAfterFilter(): void {
    if (!this.readingDetail) return;
    this.freezeSuppressed = false; this.frozenGroups = this.sourceReady() ? this.copyGroups(this.groups) : undefined;
  }

  private rebuild(): void {
    if (this.readingDetail && !this.freezeSuppressed && this.frozenGroups) {
      const latest = new Map<string, ResourceRow>();
      this.sourceRows.forEach((row: ResourceRow) => latest.set(rowKey(row), row));
      this.groups = this.frozenGroups.map((group: AgentWorkspaceGroup) => {
        const copy = this.copyGroup(group);
        copy.rows = group.rows.map((row: ResourceRow) => latest.get(rowKey(row)) || row);
        return copy;
      });
      this.frozenGroups = this.copyGroups(this.groups);
      return;
    }
    const needle = this.search.trim().toLowerCase();
    const rows = this.sourceRows.filter((row: ResourceRow) => {
      if (this.machineFilter && rowMachine(row) !== this.machineFilter && this.machineIdentity(row) !== this.machineFilter) return false;
      if (!this.history && (row.state === 'offline' || Wire.text(row.data(), 'source_kind') === 'codex_history')) return false;
      if (this.quickFilter !== 'all' && row.state !== this.quickFilter) return false;
      if (!needle) return true;
      const data = row.data(), machine = this.ownerFor(row)?.machines.find((item: ResourceRow) => item.id === rowMachine(row));
      const text = [row.title, row.path, Wire.text(data, 'last_text'), Wire.text(data, 'agent'), Wire.text(data, 'agent_label'),
        Wire.text(data, 'space_name'), Wire.text(data, 'slot_name'), Wire.text(data, 'repo_root'), Wire.text(data, 'worktree'), machine?.title || rowMachine(row)]
        .join(' ').toLowerCase();
      return text.includes(needle);
    });
    rows.sort((left: ResourceRow, right: ResourceRow) => this.compareRows(left, right));
    const groups: AgentWorkspaceGroup[] = [];
    const lookup = new Map<string, AgentWorkspaceGroup>();
    rows.forEach((row: ResourceRow) => {
      const data = row.data(), machineId = rowMachine(row);
      const machine = this.ownerFor(row)?.machines.find((item: ResourceRow) => item.id === machineId && item.manager === row.manager);
      const repositoryPath = Wire.text(data, 'repo_root') || Wire.text(data, 'worktree') || '';
      const repositoryName = pathName(repositoryPath);
      const worktreePath = Wire.text(data, 'worktree') || row.path || repositoryPath;
      const branch = Wire.text(data, 'branch');
      const worktreeName = branch || pathName(worktreePath) || repositoryName;
      const repositoryKey = repositoryPath || repositoryName;
      const worktreeKey = worktreePath || worktreeName;
      const id = this.groupingMode === 'flat' ? 'flat' : this.groupingMode === 'machine' ?
        JSON.stringify([row.group, row.manager, machineId]) : this.groupingMode === 'repository' ?
        JSON.stringify([row.group, row.manager, machineId, repositoryKey]) : JSON.stringify([row.group, row.manager, machineId, repositoryKey, worktreeKey]);
      let group = lookup.get(id);
      if (!group) {
        group = new AgentWorkspaceGroup(id, machineId, machine?.title || machineId, repositoryPath, repositoryName,
          worktreePath, worktreeName, branch);
        lookup.set(id, group); groups.push(group);
      }
      group.rows.push(row);
    });
    groups.forEach((group: AgentWorkspaceGroup) => {
      group.showMachine = this.groupingMode === 'machine';
      group.showRepository = this.groupingMode === 'repository';
      group.showWorktree = this.groupingMode === 'worktree';
    });
    this.groups = groups;
    if (this.readingDetail && this.sourceReady() && !this.freezeSuppressed && !this.frozenGroups) this.frozenGroups = this.copyGroups(groups);
  }

  private compareRows(left: ResourceRow, right: ResourceRow): number {
    const leftData = left.data(), rightData = right.data();
    const leftRepository = Wire.text(leftData, 'repo_root') || Wire.text(leftData, 'worktree') || '';
    const rightRepository = Wire.text(rightData, 'repo_root') || Wire.text(rightData, 'worktree') || '';
    const leftWorktree = Wire.text(leftData, 'worktree') || left.path || leftRepository;
    const rightWorktree = Wire.text(rightData, 'worktree') || right.path || rightRepository;
    if (this.groupingMode !== 'flat') {
      const machineOrder = rowMachine(left).localeCompare(rowMachine(right));
      if (machineOrder) return machineOrder;
      if (this.groupingMode === 'repository' || this.groupingMode === 'worktree') {
        const repositoryOrder = leftRepository.localeCompare(rightRepository);
        if (repositoryOrder) return repositoryOrder;
      }
      if (this.groupingMode === 'worktree') {
        const worktreeOrder = leftWorktree.localeCompare(rightWorktree);
        if (worktreeOrder) return worktreeOrder;
      }
    }
    const leftPinned = this.preferencesFor(left).pinsSessions.indexOf(this.sessionKey(left)), rightPinned = this.preferencesFor(right).pinsSessions.indexOf(this.sessionKey(right));
    if (leftPinned >= 0 || rightPinned >= 0) {
      if (leftPinned < 0) return 1;
      if (rightPinned < 0) return -1;
      if (leftPinned !== rightPinned) return leftPinned - rightPinned;
    }
    const rank = (row: ResourceRow): number => {
      if (row.state === 'needs_you') return 0;
      if (row.state === 'unread') return 1;
      if (row.state === 'working') return 2;
      if (this.isMuted(row)) return 4;
      return 3;
    };
    const stateOrder = rank(left) - rank(right);
    if (stateOrder) return stateOrder;
    const leftDate = Date.parse(Wire.text(left.data(), 'updated_at')) || 0;
    const rightDate = Date.parse(Wire.text(right.data(), 'updated_at')) || 0;
    if (leftDate !== rightDate) return rightDate - leftDate;
    return left.id.localeCompare(right.id);
  }

  private copyGroup(group: AgentWorkspaceGroup): AgentWorkspaceGroup {
    const copy = new AgentWorkspaceGroup(group.id, group.machineId, group.machineName, group.repositoryPath, group.repositoryName,
      group.worktreePath, group.worktreeName, group.branch, group.rows.slice());
    copy.showMachine = group.showMachine; copy.showRepository = group.showRepository; copy.showWorktree = group.showWorktree; return copy;
  }

  private copyGroups(groups: AgentWorkspaceGroup[]): AgentWorkspaceGroup[] { return groups.map((group: AgentWorkspaceGroup) => this.copyGroup(group)); }

  private fail(failure: Error): void {
    const detail = failure as Error as Object as Record<string, Object>;
    this.error = Wire.bool(detail, 'resultPending') ? 'pending' : Wire.bool(detail, 'resultUnknown') ? 'result_unknown' :
      Wire.text(detail, 'message') === 'pending_action' ? 'pending_action' : Wire.text(detail, 'message') === 'invalid_preferences' ? 'failed' : 'failed';
  }

  private saveViewLater(): void {
    if (this.persistTimer !== -1) clearTimeout(this.persistTimer);
    const client = this.client, revision = this.revision, viewScope = this.viewScope;
    this.persistTimer = setTimeout(() => {
      this.persistTimer = -1;
      if (!client || !this.loadCurrent(client, revision) || this.viewScope !== viewScope) return;
      const values: Object[] = [this.quickFilter, this.groupingMode, this.machineFilter, this.search, this.history, this.selectedKey];
      client.saveView('agent-workspace', JSON.stringify(Wire.payload(['quick', 'grouping', 'machine', 'search', 'history', 'selected'], values)))
        .catch(() => undefined);
    }, 180);
  }
}

function togglePresent(values: string[], key: string): string[] { return values.includes(key) ? values : values.concat([key]); }
