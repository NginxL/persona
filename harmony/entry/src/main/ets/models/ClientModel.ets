import { AssistantViewModel } from './AssistantView';
import { SshTunnel } from './SshTunnel';
import type { SshProxyLease } from './SshTunnel';
import { KnowledgeRecording } from './KnowledgeRecording';
import { deviceLook } from './DeviceLook';
import { clientLocale, initializeClientLanguage, setClientLanguage } from '../core/ClientLocale';
import { i18n } from '@kit.LocalizationKit';
import { DemoRepository } from './DemoRepository';
import { NavigationContext } from './NavigationContext';
import { utf8Bytes } from '../core/TranscriptCache';
import { ChatDraftStore, ChatDraft, ChatOutboxEntry } from './ChatDraftStore';
import { clearImagePreviews } from '../platform/NativePreview';
import { parseMessageContent } from '../core/MessageContent';
import { decodeInteractionDecision, firstBlockingDecision, isPersonaAskUserQuestion } from './InteractionDecision';
import { ChatAttachments } from './ChatAttachments';
import type { PreparedAttachmentSend } from './ChatAttachments';
import { nativeNotifications } from '../platform/NativeNotifications';
import { resolveManagerScope } from '../core/ScopePolicy';
import { SessionWorkspace } from './SessionWorkspace';
import { common } from '@kit.AbilityKit';
import { util } from '@kit.ArkTS';
import { HubTransport, EndpointConfig, HttpFailure } from '../platform/HubTransport';
import { CredentialVault } from '../platform/CredentialVault';
import { LocalStore } from '../platform/LocalStore';
import { HubRepository, segment } from './HubRepository';
import { Wire, ResourceRow, ChatMessage, Decision, HubProfile } from './Wire';
import { tr, Strings } from '../design/Strings';
import { withSourceContext, messageDisplay } from './Compatibility';
import { MutationRecord, MutationStore } from './MutationStore';
import { TranscriptModel } from './TranscriptModel';
import { Pairing, PairingTarget } from '../platform/Pairing';
import { scopeKey, sessionCapabilities, projectToolTurn, TranscriptReducer } from '../core/Domain';
import type { SessionCapabilityInput, EffectiveSessionCapabilities, TranscriptItem, TranscriptSnapshot, ToolTurnProjection, WorkInspectionStep } from '../core/Domain';




interface DevicePreferenceChange { appearance?: number; style?: string; alerts?: boolean; }

export interface ClientViewLease {
  readonly epoch: number;
  valid: () => boolean;
  read: () => Promise<string>;
  write: (value: string) => Promise<void>;
}

@ObservedV2
export class ClientModel {
  readonly ssh: SshTunnel = new SshTunnel();
  private static pairingEpochs: Map<string, number> = new Map();
  pairingRevision(): number { return ClientModel.pairingEpochs.get(this.profile.id) || 0; }
  private retirePairing(id: string): void { ClientModel.pairingEpochs.set(id, (ClientModel.pairingEpochs.get(id) || 0) + 1); }
  retireHubConnections(id: string): void {
    const root = this.directoryParent || this;
    if (root.profile.id === id) {
      root.stopReconnect(); root.reconnectAttempt = 0; root.generation++; root.selectionGeneration++; root.connected = false;
      root.clearPendingDecisions();
      root.ssh.stop(); root.repository?.close(); root.repository = undefined;
      root.releaseTerminal();
      root.scheduleReconnect();
    }
    root.retireViews(id);
    root.publishDirectory();
  }
  @Trace ready: boolean = false;
  @Trace pairingLink: string = '';
  @Trace notificationTarget: string = '';
  @Trace forgottenProfile: string = '';
  @Trace forgottenEpoch: number = 0;
  @Trace busy: boolean = false;
  @Trace error: string = '';
  @Trace diagnosticCode: string = '';
  @Trace connected: boolean = false;
  @Trace demo: boolean = false;
  @Trace profiles: HubProfile[] = [];
  @Trace profile: HubProfile = new HubProfile();
  @Trace managers: ResourceRow[] = [];
  @Trace features: string[] = [];
  @Trace section: number = 0;
  @Trace sessions: ResourceRow[] = [];
  @Trace sessionsReady: boolean = false;
  @Trace conversations: ResourceRow[] = [];
  @Trace machines: ResourceRow[] = [];
  @Trace directorySessions: ResourceRow[] = [];
  @Trace directoryMachines: ResourceRow[] = [];
  @Trace directoryLoading: boolean = false;
  @Trace directoryUnavailable: number = 0;
  @Trace directoryReady: boolean = false;
  private directoryTask: Promise<void> | undefined;
  private directoryOwner: string = '';
  private directoryParent: ClientModel | undefined;
  @Trace todos: ResourceRow[] = [];
  @Trace knowledge: ResourceRow[] = [];
  @Trace knowledgeKind: string = 'projects';
  @Trace inbox: ResourceRow[] = [];
  @Trace selected: ResourceRow = new ResourceRow();
  @Trace messages: ChatMessage[] = [];
  @Trace cards: ResourceRow[] = [];
  @Trace decision: Decision | undefined;
  @Trace pendingDecisions: Decision[] = [];
  @Trace personaQuestions: Decision[] = [];
  private pendingDecisionTarget: string = '';
  @Trace private decisionReadyTarget: string = '';
  private decisionRefreshSequence: number = 0;
  @Trace draft: string = '';
  @Trace draftRevision: string = '';
  private localMessages: ChatOutboxEntry[] = [];
  private activeMessages: Set<string> = new Set();
  private renderedMessages: ChatMessage[] = [];
  private echoedMessages: Set<string> = new Set();
  @Trace draftLoading: boolean = false;
  @Trace sourceContext: string = '';
  @Trace reply: string = '';
  @Trace operationState: string = '';
  @Trace mainId: string = '';
  private preferenceTask: Promise<void> = Promise.resolve();
  private async changePreferences(action: () => Promise<void>): Promise<void> { const task = this.preferenceTask.then(action, action); this.preferenceTask = task.catch(() => {}); await task; }
  @Trace language: string = 'default';
  @Trace languageChanging: boolean = false;
  @Trace appearance: number = 0;
  @Trace style: string = 'glass';
  @Trace alerts: boolean = true;
  @Trace terminalText: string = '';
  @Trace terminalControl: boolean = false;
  @Trace resourceRows: ResourceRow[] = [];
  @Trace detail: string = '';
  @Trace hasMoreMessages: boolean = false;
  private deviceEvent: ((payload: string) => void) | undefined;
  private eventListeners: Set<(payload: string) => void> = new Set();
  observeEvents(listener: (payload: string) => void): () => void { this.eventListeners.add(listener); return (): void => { this.eventListeners.delete(listener); }; }
  setDeviceEvents(handler: ((payload: string) => void) | undefined): void { this.deviceEvent = handler; }
  private workspace: SessionWorkspace | undefined;
  setWorkspace(workspace: SessionWorkspace | undefined): void { if (this.workspace && this.workspace !== workspace) this.workspace.hide(); this.workspace = workspace; }
  private transcript: TranscriptModel | undefined;
  private transcriptKey: string = '';
  private revisionItems: TranscriptItem[] = [];
  transcriptItems(): readonly TranscriptItem[] { return this.revisionItems.slice(); }
  private repository: HubRepository | undefined;
  private demoRepository: DemoRepository | undefined;
  private detached: boolean = false;
  private retired: boolean = false;
  private views: Map<string, ClientModel> = new Map();
  private viewTasks: Map<string, Promise<ClientModel>> = new Map();

  // A detail context retains its paired Hub and manager without changing the workspace selection.
  async viewFor(row: ResourceRow): Promise<ClientModel> {
    const group = row.group || this.profile.id;
    if (group === this.profile.id && (!row.manager || row.manager === this.profile.manager)) return this;
    if (this.retired || !this.profile.id || this.demo || this.forgettingProfiles.has(group) || this.cleanupProfiles.has(group)) throw new Error('request_updated');
    const source = group === this.profile.id ? this.profile : this.profiles.find((profile: HubProfile) => profile.id === group);
    if (!source) throw new Error('request_updated');
    const manager = row.manager || source.manager, key = JSON.stringify([group, manager]), existing = this.views.get(key);
    if (existing && !existing.retired && existing.connected) return existing;
    const taskKey = JSON.stringify([this.generation, key]), pending = this.viewTasks.get(taskKey); if (pending) return pending;
    const generation = this.generation, scope = this.scope();
    const task = this.connectView(manager, key, generation, scope, source);
    this.viewTasks.set(taskKey, task);
    try { return await task; } finally { if (this.viewTasks.get(taskKey) === task) this.viewTasks.delete(taskKey); }
  }
  private async connectView(manager: string, key: string, generation: number, scope: string, source: HubProfile = this.profile): Promise<ClientModel> {
    let view = this.views.get(key);
    if (!view || view.retired) {
      view = new ClientModel(); view.detached = true; view.local = this.local; view.vault = this.vault; view.context = this.context;
      if (this.context) view.ssh.initialize(this.context);
      view.ready = this.ready; view.foreground = this.foreground; view.appearance = this.appearance; view.style = this.style; view.alerts = false;
      view.profiles = this.profiles.slice(); view.directoryParent = this; this.views.set(key, view);
    }
    const profile = new HubProfile(source.id, source.name, source.baseUrl);
    profile.addresses = source.addresses.slice(); profile.hubId = source.hubId; profile.pin = source.pin; profile.manager = manager;
    await view.selectProfile(profile, true);
    this.publishDirectory();
    if (!this.owns(generation, scope) || this.forgettingProfiles.has(source.id) || view.retired || !view.connected) throw new Error(view.error || 'request_updated');
    return view;
  }
  directoryOwnerFor(row: ResourceRow): ClientModel | undefined {
    const group = row.group || this.profile.id;
    if (group === this.profile.id && row.manager === this.profile.manager) return this;
    return Array.from(this.views.values()).find((view: ClientModel) => !view.retired && view.profile.id === group && view.profile.manager === row.manager);
  }
  directoryOwners(): ClientModel[] { const owners: ClientModel[] = [this]; for (const owner of this.views.values()) if (!owner.retired && !this.forgettingProfiles.has(owner.profile.id)) owners.push(owner); return owners; }
  groupName(row: ResourceRow): string { return this.profiles.find((profile: HubProfile) => profile.id === (row.group || this.profile.id))?.name || this.profile.name; }
  async refreshDirectory(): Promise<void> {
    if (this.detached || this.demo) { this.publishDirectory(); return; }
    const generation = this.generation, scope = this.scope(), owner = JSON.stringify([generation, scope]);
    if (this.directoryTask && this.directoryOwner === owner) return this.directoryTask;
    const task = this.loadDirectory(generation, scope); this.directoryTask = task; this.directoryOwner = owner;
    try { await task; } finally { if (this.directoryTask === task) this.directoryTask = undefined; }
  }
  private async loadDirectory(generation: number, scope: string): Promise<void> {
    this.directoryLoading = true; let unavailable = 0;
    const profiles = this.profiles.slice();
    try {
      for (const profile of profiles) {
        if (!this.owns(generation, scope)) return;
        if (this.forgettingProfiles.has(profile.id)) continue;
        const target = new ResourceRow(); target.group = profile.id; target.manager = profile.manager;
        let view: ClientModel;
        try { view = await this.viewFor(target); if (view.connected) await view.refresh(); else unavailable++; }
        catch (_) { unavailable++; continue; }
        if (!this.owns(generation, scope)) return;
        const managers = view.managers.slice();
        for (const manager of managers) {
          if (!this.owns(generation, scope)) return;
          if (manager.id === view.profile.manager) continue;
          const scoped = new ResourceRow(); scoped.group = profile.id; scoped.manager = manager.id;
          try { const child = await this.viewFor(scoped); if (child.connected) await child.refresh(); else unavailable++; } catch (_) { unavailable++; }
        }
      }
    } finally {
      if (this.owns(generation, scope)) { this.directoryLoading = false; this.directoryUnavailable = unavailable; this.directoryReady = true; this.publishDirectory(); }
    }
  }
  private directoryRow(row: ResourceRow, view: ClientModel): ResourceRow {
    const copy = NavigationContext.rowFrom(JSON.stringify(row)); copy.group = view.profile.id;
    if (!view.connected) copy.state = 'offline'; return copy;
  }
  private publishDirectory(): void {
    if (this.directoryParent) { this.directoryParent.publishDirectory(); return; }
    const sessions = new Map<string, ResourceRow>(), machines = new Map<string, ResourceRow>();
    const profiles = new Set<string>(this.profiles.map((profile: HubProfile) => profile.id));
    if (this.demo) profiles.add(this.profile.id);
    const views: ClientModel[] = [this]; for (const view of this.views.values()) views.push(view);
    for (const view of views) {
      if (view.retired || !profiles.has(view.profile.id) || this.forgettingProfiles.has(view.profile.id)) continue;
      for (const row of view.sessions) { const next = this.directoryRow(row, view); if (!Wire.bool(next.data(), 'assistant')) sessions.set(next.key(), next); }
      for (const row of view.machines) { const next = this.directoryRow(row, view); machines.set(next.key(), next); }
    }
    this.directorySessions = Array.from(sessions.values()); this.directoryMachines = Array.from(machines.values());
  }
  private retireViews(id: string = ''): void {
    for (const key of this.views.keys()) {
      const view = this.views.get(key); if (!view || id && view.profile.id !== id) continue;
      view.retired = true; view.generation++; view.selectionGeneration++;
      view.background().catch(() => {}); this.views.delete(key);
    }
    this.viewTasks.clear();
  }
  private refreshTask: Promise<void> | undefined;
  private refreshOwner: string = '';
  private vault: CredentialVault | undefined;
  private local: LocalStore = new LocalStore();
  private context: common.UIAbilityContext | undefined;
  private generation: number = 0;
  private selectionGeneration: number = 0;
  @Trace unresolvedWrites: MutationRecord[] = [];

  private forgettingProfiles: Set<string> = new Set();
  private cleanupProfiles: Map<string, string> = new Map();
  @Trace pendingCleanupCount: number = 0;
  private async saveCleanup(): Promise<void> {
    await this.changePreferences(async (): Promise<void> => {
      await this.local.put('hub-cleanup', JSON.stringify(Array.from(this.cleanupProfiles.entries())));
    });
    this.pendingCleanupCount = this.cleanupProfiles.size;
  }
  private async restoreCleanup(): Promise<void> {
    const pendingCleanup = await this.local.get('hub-cleanup');
    if (pendingCleanup) {
      if (pendingCleanup.length > 65536) throw new Error('cleanup_incomplete');
      const pending = JSON.parse(pendingCleanup) as string[][];
      if (!Array.isArray(pending) || pending.length > 128) throw new Error('cleanup_incomplete');
      for (const pair of pending) {
        if (!Array.isArray(pair) || pair.length !== 2 || pair.some((value: string): boolean => typeof value !== 'string' || !value || value.length > 128 || /[\u0000-\u001f\u007f]/.test(value))) throw new Error('cleanup_incomplete');
        this.cleanupProfiles.set(pair[0], pair[1]);
      }
      this.pendingCleanupCount = this.cleanupProfiles.size;
    }
  }
  async retryCleanup(): Promise<void> {
    for (const id of Array.from(this.cleanupProfiles.keys())) { try { await this.forgetProfile(id); } catch (_) {} }
  }
  private pairingProfiles: Set<string> = new Set();
  private forgetEpochs: Map<string, number> = new Map();
  private credentialWrites: Map<string, Promise<void>> = new Map();
  private pendingNotification: Record<string, Object> | undefined;
  private notificationEpoch: number = 0;
  private async credentialChange(id: string, action: () => Promise<void>): Promise<void> {
    const previous = this.credentialWrites.get(id) || Promise.resolve();
    const work = previous.catch(() => {}).then(action), tail = work.catch(() => {});
    this.credentialWrites.set(id, tail);
    try { await work; } finally { if (this.credentialWrites.get(id) === tail) this.credentialWrites.delete(id); }
  }
  private feedTimer: number = -1;
  private draftTimer: number = -1;
  private foreground: boolean = true;
  private reconnectTimer: number = -1;
  private reconnectAttempt: number = 0;
  private stopReconnect(): void { if (this.reconnectTimer !== -1) clearTimeout(this.reconnectTimer); this.reconnectTimer = -1; }
  private scheduleReconnect(): void {
    if (!this.foreground || this.demo || !this.profile.id || this.cleanupProfiles.has(this.profile.id) || this.reconnectTimer !== -1 || this.pairingProfiles.size > 0) return;
    const generation = this.generation, scope = this.scope(), delay = Math.min(30000, 1000 * Math.pow(2, Math.min(5, this.reconnectAttempt++)));
    this.reconnectTimer = setTimeout(() => { this.reconnectTimer = -1;
      if (this.foreground && this.owns(generation, scope) && !this.connected) this.selectProfile(this.profile, true).catch((error: Error) => this.failure(error));
    }, delay);
  }
  private chatVisible: boolean = false;
  private decisionsVisible: boolean = false;
  watchDecisions(visible: boolean): void { this.decisionsVisible = visible; if (visible && this.connected && this.isChat()) this.refreshDecisions().catch((error: Error) => this.failure(error)); }
  private clearPendingDecisions(): void {
    this.decisionRefreshSequence++;
    this.pendingDecisionTarget = '';
    this.decisionReadyTarget = '';
    this.pendingDecisions = [];
    this.personaQuestions = [];
    this.decision = undefined;
  }
  private replacePendingDecisions(interactions: Record<string, Object>[], row: ResourceRow, target: string,
    generation: number, scope: string, sequence: number): void {
    if (!this.owns(generation, scope) || this.target() !== target || sequence !== this.decisionRefreshSequence) return;
    const next: Decision[] = [], seen = new Set<string>(), previous = this.pendingDecisions;
    const session = row.session || row.id;
    for (const data of interactions) {
      const nested = Wire.object(data, 'interaction');
      const machine = Wire.text(data, 'machine_id', Wire.text(nested, 'machine_id'));
      const sessionId = Wire.text(data, 'session_id', Wire.text(nested, 'session_id'));
      const manager = Wire.text(data, 'manager', Wire.text(data, 'manager_id'));
      if (machine !== row.machine || sessionId !== session || manager && manager !== this.profile.manager) continue;
      let request = this.decodeDecision(data);
      request.machine = machine; request.session = sessionId;
      if (!request.id || seen.has(request.id)) continue;
      seen.add(request.id);
      const old = previous.find((item: Decision): boolean => item.id === request.id);
      if (old && old.revision === request.revision && old.raw === request.raw) {
        if (old.status === 'resolved' && request.status === 'open' || old.status === 'pending' && request.status === 'open') {
          request = old;
        } else {
          old.status = request.status;
          request = old;
        }
      }
      next.push(request);
    }
    this.pendingDecisionTarget = target;
    this.pendingDecisions = next;
    this.personaQuestions = next.filter((request: Decision): boolean => isPersonaAskUserQuestion(request, this.selected.kind));
    this.decision = firstBlockingDecision(next, this.selected.kind);
    this.decisionReadyTarget = target;
  }
  async refreshDecisions(): Promise<void> {
    const generation = this.generation, scope = this.scope(), target = this.target(), row = this.selected;
    const sequence = ++this.decisionRefreshSequence;
    try {
      const interactions = await this.repo().decisions();
      this.replacePendingDecisions(interactions, row, target, generation, scope, sequence);
    } catch (error) {
      if (this.owns(generation, scope) && this.target() === target && sequence === this.decisionRefreshSequence) this.failure(error as Error);
      throw error as Error;
    }
  }

  async initialize(context: common.UIAbilityContext): Promise<void> {
    this.context = context;
    this.ssh.initialize(context);
    this.vault = new CredentialVault(context);
    await this.local.open(context);
    const preferences = Wire.parse(await this.local.get('preferences'));
    this.appearance = Wire.number(preferences, 'appearance'); this.style = Wire.text(preferences, 'style', 'glass');
    this.alerts = Wire.bool(preferences, 'alerts', true); this.language = Wire.text(preferences, 'language', 'default');
    if (!['default', 'en-US', 'zh-CN', 'ja-JP'].includes(this.language)) this.language = 'default';
    i18n.System.setAppPreferredLanguage(this.language); initializeClientLanguage(this.language);
    await nativeNotifications.initialize(context);
    const saved = Wire.parse(await this.local.get('profiles'));
    await this.restoreCleanup();
    this.profiles = Wire.rows(saved, 'profiles').map((item: Record<string, Object>) => {
      const profile = new HubProfile(Wire.text(item, 'id'), Wire.text(item, 'name'), Wire.text(item, 'baseUrl'));
      profile.addresses = Wire.strings(item, 'addresses'); profile.hubId = Wire.text(item, 'hubId'); profile.pin = Wire.text(item, 'pin'); profile.manager = Wire.text(item, 'manager');
      return profile;
    });
    this.ready = true;
    await this.retryCleanup();
    const active = Wire.text(saved, 'active');
    const profile = this.profiles.find((item: HubProfile) => item.id === active);
    if (profile) { if (this.foreground) await this.selectProfile(profile); else this.profile = profile; }
  }
  scope(): string { return scopeKey({ groupId: this.profile.id, managerId: this.profile.manager }); }
  target(row: ResourceRow = this.selected): string { return JSON.stringify([this.scope(), row.kind, row.machine, row.id]); }
  repo(): HubRepository {
    if (!this.repository || !this.connected) throw new Error('offline');
    return this.repository;
  }
  connectionGeneration(): number { return this.generation; }
  owns(generation: number, scope: string): boolean { return !this.retired && generation === this.generation && scope === this.scope(); }
  isChat(): boolean { return this.selected.kind === 'conversation' || this.selected.kind === 'session'; }
  reportError(error: Error): void { this.failure(error); }
  operationBlocked(path: string): boolean { return this.unresolvedWrites.some((record: MutationRecord) => record.path === path); }
  private failure(error: Error): void {
    if (error instanceof HttpFailure) {
      if (error.status === 401 || error.status === 403) this.stopReconnect();
      this.error = error.resultPending ? 'pending' : error.resultUnknown ? 'result_unknown' : error.status === 401 ? 'welcome' :
        error.status === 403 ? 'permission_denied' : error.status === 409 || error.status === 410 ? 'request_updated' : 'failed';
    } else this.error = ['offline', 'pending_action'].includes(error.message) ? error.message : 'failed';
  }
  private async saveProfiles(): Promise<void> {
    if (this.detached) return;
    await this.local.put('profiles', JSON.stringify({ profiles: this.profiles, active: this.profile.id }));
  }
  private async applyAppearance(change: DevicePreferenceChange): Promise<void> {
    await this.changePreferences(async (): Promise<void> => {
      const mode = change.appearance === undefined ? this.appearance : change.appearance;
      const requested = change.style === undefined ? this.style : change.style;
      const style = ['glass', 'brief', 'board'].includes(requested) ? requested : 'glass';
      const alerts = change.alerts === undefined ? this.alerts : change.alerts;
      try { await this.local.put('preferences', JSON.stringify({ appearance: mode, style: style, alerts: alerts, language: this.language })); }
      catch (_) { this.error = 'cache_unavailable'; return; }
      this.appearance = mode; this.style = style; this.alerts = alerts;
      for (const view of this.views.values()) { view.appearance = mode; view.style = style; }
    });
  }
  async setAppearance(mode: number, style?: string, alerts?: boolean): Promise<void> { await this.applyAppearance({ appearance: mode, style: style, alerts: alerts }); }
  async setStyle(style: string): Promise<void> { await this.applyAppearance({ style: style }); }
  async setAlerts(alerts: boolean): Promise<void> { await this.applyAppearance({ alerts: alerts }); }
  async setLanguage(language: string): Promise<void> {
    if (this.languageChanging || !['default', 'en-US', 'zh-CN', 'ja-JP'].includes(language)) return;
    this.languageChanging = true; let applied = false;
    try {
      await this.changePreferences(async (): Promise<void> => {
        const previous = this.language;
        const effective = clientLocale(i18n.getSystemLanguage());
        const previousNative = previous === 'default' ? (effective === 'zh-Hans' ? 'zh-CN' : effective === 'ja' ? 'ja-JP' : 'en-US') : previous;
        i18n.System.setAppPreferredLanguage(language); applied = true;
        try { await this.local.put('preferences', JSON.stringify({ appearance: this.appearance, style: this.style, alerts: this.alerts, language: language })); }
        catch (error) {
          i18n.System.setAppPreferredLanguage(previousNative);
          if (previous === 'default') i18n.System.setAppPreferredLanguage('default');
          setClientLanguage(previousNative); Strings.refresh(); throw error as Error;
        }
        this.language = language;
        // Native 'default' changes resources only after a cold start. Keep this
        // launch's effective wire language until the UI actually follows it.
        if (language !== 'default') setClientLanguage(language);
        Strings.refresh();
        for (const view of this.views.values()) view.language = language;
        this.announceLanguage();
      });
    } catch (_) { this.error = applied ? 'cache_unavailable' : 'language_change_failed'; }
    finally { this.languageChanging = false; }
  }
  announceLanguage(): void {
    for (const owner of this.directoryOwners()) {
      if (owner.connected && !owner.demo) {
        try { owner.repo().transport.hello(nativeNotifications.features(!owner.detached && owner.alerts)); } catch (_) {}
      }
    }
  }
  async useEndpoint(config: EndpointConfig, name: string, admit?: () => boolean): Promise<void> {
    if (this.forgettingProfiles.has(config.hubId) || this.cleanupProfiles.has(config.hubId)) throw new Error('pending_action');
    if (!this.vault) throw new Error('storage_unavailable');
    if (this.pairingProfiles.size > 0) throw new Error('pending_action');
    if (admit && !admit()) throw new Error('request_updated');
    this.pairingProfiles.add(config.hubId); this.stopReconnect();
    try {
    this.retirePairing(config.hubId);
    this.retireViews(config.hubId);
    if (this.profile.id === config.hubId) this.ssh.stop();
    const epoch = this.forgetEpochs.get(config.hubId) || 0, vault = this.vault;
    const current = (): boolean => epoch === (this.forgetEpochs.get(config.hubId) || 0) && !this.forgettingProfiles.has(config.hubId) && (!admit || admit());
    let previous: string | null = null, written = false;
    const rollback = async (): Promise<void> => {
      if (await vault.get(config.hubId) !== config.token) return;
      if (previous && epoch === (this.forgetEpochs.get(config.hubId) || 0) && !this.forgettingProfiles.has(config.hubId)) await vault.put(config.hubId, previous);
      else await vault.remove(config.hubId);
    };
    await this.credentialChange(config.hubId, async (): Promise<void> => {
      if (!current()) throw new Error('request_updated');
      previous = await vault.get(config.hubId); if (!current()) throw new Error('request_updated');
      await vault.put(config.hubId, config.token); written = true;
      if (!current()) { await rollback(); written = false; throw new Error('request_updated'); }
    });
    if (!current()) { if (written) await this.credentialChange(config.hubId, rollback); throw new Error('request_updated'); }
    this.retireHubConnections(config.hubId);
    const profile = new HubProfile(config.hubId, name, config.baseUrl);
    profile.addresses = config.alternateUrls?.slice() || []; profile.hubId = config.hubId; profile.pin = config.pin; profile.manager = config.managerId;
    this.profiles = this.profiles.filter((item: HubProfile) => item.id !== profile.id).concat([profile]);
    await this.selectProfile(profile);
    } finally {
      this.pairingProfiles.delete(config.hubId);
      if (!this.connected) this.scheduleReconnect();
    }
  }
  async selectProfile(profile: HubProfile, preserve: boolean = false, notification: boolean = false): Promise<void> {
    if (this.retired || this.forgettingProfiles.has(profile.id) || this.cleanupProfiles.has(profile.id)) throw new Error('pending_action');
    if (!this.detached && this.profile.id && (this.profile.id !== profile.id || this.profile.manager !== profile.manager)) this.retireViews();
    if (!notification && this.pendingNotification && (Wire.text(this.pendingNotification, 'profile') !== profile.id || Wire.text(this.pendingNotification, 'manager') !== profile.manager)) {
      this.pendingNotification = undefined; this.notificationEpoch++;
    }
    this.local.allowHub(profile.id); ChatAttachments.allowHub(profile.hubId || profile.id);
    this.ssh.stop(); this.stopReconnect(); this.cancelDraftTimer(); const keep = preserve && profile.id === this.profile.id && profile.manager === this.profile.manager;
    const saved = this.persistDraft(); this.closeTranscript(); this.releaseTerminal(); this.repository?.close(); this.repository = undefined;
    const generation = ++this.generation; this.selectionGeneration++; const selection = this.selected;
    this.profile = profile; const scope = this.scope(); this.connected = false;
    this.clearPendingDecisions();
    this.unresolvedWrites = []; this.operationState = ''; this.mainId = ''; this.features = []; this.managers = [];
    this.demo = false; this.demoRepository = undefined; this.error = '';
    if (!keep) { this.sessionsReady = false; this.sessions = []; this.machines = []; this.conversations = []; this.todos = []; this.inbox = []; this.knowledge = []; this.messages = []; } this.selected = keep ? selection : new ResourceRow(); this.draftLoading = false;
    if (!keep && !this.detached) { this.directoryReady = false; this.directoryLoading = false; this.directoryUnavailable = 0; this.publishDirectory(); }
    if (!keep) { this.draft = ''; this.sourceContext = ''; this.draftRevision = ''; this.localMessages = []; this.renderedMessages = []; this.echoedMessages.clear(); }
    await saved; if (!this.owns(generation, scope)) return;
    await this.saveProfiles(); if (!this.owns(generation, scope) || !this.foreground) return;
    const token = await this.vault?.get(profile.id);
    if (!this.owns(generation, scope)) return;
    if (!token) { this.error = 'welcome'; return; }
    if (profile.manager) await this.restoreSnapshot(generation, scope);
    let proxy: SshProxyLease | undefined;
    try {
      await this.ssh.bind(this, (): boolean => this.foreground && !this.retired && !this.forgettingProfiles.has(profile.id));
      if (!this.owns(generation, scope) || !this.foreground) return;
      proxy = await this.ssh.acquire();
      const address = proxy ? profile.baseUrl : await Pairing.reachable(profile.baseUrl, profile.addresses, profile.hubId, profile.pin, (): boolean => this.owns(generation, scope) && this.foreground);
      if (!this.owns(generation, scope) || !this.foreground) return;
      if (address !== profile.baseUrl) { profile.addresses = Array.from(new Set<string>([profile.baseUrl].concat(profile.addresses))).filter((value: string) => value !== address && value.startsWith('https://')); profile.baseUrl = address; await this.saveProfiles(); }
    } catch (error) { if (this.owns(generation, scope)) { this.failure(error as Error); this.scheduleReconnect(); } return; }
    if (!this.owns(generation, scope) || !this.foreground) return;
    let defaultOwner = '';
    const probe = new HubTransport(new EndpointConfig(profile.baseUrl, profile.hubId, token, profile.pin, '', [], proxy));
    try {
      const roster = Wire.parse(await probe.request('/api/v1/managers'));
      if (!this.owns(generation, scope)) return;
      defaultOwner = Wire.text(roster, 'default');
      const managerId = resolveManagerScope(profile.manager, defaultOwner, Wire.rows(roster, 'managers').map((item: Record<string, Object>) => Wire.text(item, 'id')));
      if (managerId !== profile.manager) {
        const canonical = new HubProfile(profile.id, profile.name, profile.baseUrl);
        canonical.addresses = profile.addresses.slice(); canonical.hubId = profile.hubId; canonical.pin = profile.pin; canonical.manager = managerId;
        this.profiles = this.profiles.map((item: HubProfile) => item.id === canonical.id ? canonical : item);
        await this.selectProfile(canonical); return;
      }
    } catch (error) {
      if (!this.owns(generation, scope)) return;
      // A hub without manager support has one legacy scope. Never downgrade a named manager.
      if (!(error instanceof HttpFailure && error.status === 404 && !profile.manager)) { this.failure(error as Error); this.scheduleReconnect(); return; }
    } finally { probe.close(); }
    if (!profile.manager) await this.restoreSnapshot(generation, scope);
    if (!this.owns(generation, scope) || !this.foreground) return;
    const config = new EndpointConfig(profile.baseUrl, profile.hubId, token, profile.pin, profile.manager, [], proxy);
    this.repository = new HubRepository(config, this.local, () => { if (this.owns(generation, scope)) this.unresolvedWrites = this.repository?.mutations?.blocked() || []; }, defaultOwner);
    await this.repository.mutations?.load(); if (!this.owns(generation, scope) || !this.foreground) return; this.unresolvedWrites = this.repository.mutations?.blocked() || [];
    await this.repository.transport.connect((payload: string) => {
      if (this.owns(generation, scope)) this.event(payload);
    }, (state: string) => {
      if (!this.owns(generation, scope)) return;
      this.connected = state === 'ready' || state === 'online';
      if (this.connected) { this.reconnectAttempt = 0; this.stopReconnect(); }
      if (state === 'offline' || state === 'closed') { this.clearPendingDecisions(); this.releaseTerminal(); this.error = 'offline'; this.scheduleReconnect(); }
      else if (this.connected && this.isChat() && (this.chatVisible || this.decisionsVisible)) {
        const row = this.selected;
        if (this.chatVisible) this.loadChat(row).catch((error: Error) => this.failure(error));
        else this.refreshDecisions().catch((error: Error) => this.failure(error));
      }
      this.publishDirectory();
    }).catch((error: Error) => { if (this.owns(generation, scope)) this.failure(error); });
    if (this.owns(generation, scope)) await this.flushNotification();
  }
  private async restoreSnapshot(generation: number, scope: string): Promise<void> {
    try {
      const cached = await this.local.get('snapshot:' + scope);
      if (!this.owns(generation, scope) || !cached) return;
      const snapshot = Wire.record(JSON.parse(cached) as Object);
      this.sessionsReady = Wire.has(snapshot, 'sessions');
      this.sessions = Wire.rows(snapshot, 'sessions').map((data: Record<string, Object>) => this.cachedRow(data, 'session'));
      this.conversations = Wire.rows(snapshot, 'conversations').map((data: Record<string, Object>) => this.cachedRow(data, 'conversation')); this.publishDirectory();
    } catch (_) { if (this.owns(generation, scope)) this.error = 'cache_unavailable'; }
  }
  async selectManager(id: string): Promise<void> {
    if (this.demo && id === this.profile.manager) return;
    const profile = new HubProfile(this.profile.id, this.profile.name, this.profile.baseUrl);
    profile.addresses = this.profile.addresses.slice(); profile.hubId = this.profile.hubId; profile.pin = this.profile.pin; profile.manager = id;
    this.profiles = this.profiles.map((item: HubProfile) => item.id === profile.id ? profile : item);
    await this.selectProfile(profile);
  }
  async forgetProfile(id: string): Promise<void> {
    if (this.directoryParent) return this.directoryParent.forgetProfile(id);
    let profile = this.profiles.find((item: HubProfile) => item.id === id);
    if (!profile && this.cleanupProfiles.has(id)) { profile = new HubProfile(id); profile.hubId = this.cleanupProfiles.get(id) || id; }
    if (!profile || this.forgettingProfiles.has(id)) return;
    this.cleanupProfiles.set(id, profile.hubId || id); this.pendingCleanupCount = this.cleanupProfiles.size;
    this.forgettingProfiles.add(id); AssistantViewModel.forgetProfile(id); this.retireViews(id);
    this.retirePairing(id);
    this.forgetEpochs.set(id, (this.forgetEpochs.get(id) || 0) + 1);
    if (this.pendingNotification && Wire.text(this.pendingNotification, 'profile') === id) { this.pendingNotification = undefined; this.notificationEpoch++; }
    try {
    if (this.profile.id === id) {
      this.ssh.stop();
      this.stopReconnect(); this.cancelDraftTimer(); this.closeTranscript(); this.releaseTerminal();
      this.repository?.close(); this.repository = undefined; this.generation++; this.selectionGeneration++;
      this.profile = new HubProfile(); this.connected = false; this.selected = new ResourceRow();
      this.sessionsReady = false; this.sessions = []; this.conversations = []; this.machines = []; this.managers = []; this.todos = []; this.knowledge = []; this.inbox = [];
      this.clearPendingDecisions();
      this.messages = []; this.draft = ''; this.sourceContext = ''; this.draftRevision = ''; this.localMessages = []; this.renderedMessages = []; this.echoedMessages.clear(); this.detail = ''; this.reply = ''; this.mainId = '';
      this.unresolvedWrites = []; this.operationState = ''; this.notificationTarget = ''; this.draftLoading = false; this.features = []; this.error = '';
    }
    this.publishDirectory();
    try { await this.saveCleanup(); } catch (_) { this.error = 'cleanup_incomplete'; throw new Error('cleanup_incomplete'); }
    const forgettingSsh = this.ssh.forget(id);
    const forgettingRecordings = this.context ? KnowledgeRecording.forgetProfile(id, this.context) : Promise.resolve();
    const forgettingLook = deviceLook.forgetHub(id, profile.hubId || id);
    // Retire writes before awaiting credential removal so late callbacks cannot recreate cached content.
    const purging = this.local.purgeHub(id);
    this.forgottenProfile = id; this.forgottenEpoch++;
    MutationStore.forgetHub(this.local, id);
    const cleanup = await Promise.allSettled([forgettingSsh, forgettingRecordings, forgettingLook, purging]);
    let cleanupFailed = cleanup.some((result: PromiseSettledResult<void>): boolean => result.status === 'rejected');
    if (this.context) { try { await ChatAttachments.forgetHub(this.context, profile.hubId || id); } catch (_) { cleanupFailed = true; } }
    if (this.context) { try { clearImagePreviews(this.context, profile.hubId || id); } catch (_) { cleanupFailed = true; } }
    try { await nativeNotifications.clear(id); } catch (_) { cleanupFailed = true; }
    try { await this.credentialChange(id, async (): Promise<void> => { await this.vault?.remove(id); }); } catch (_) { cleanupFailed = true; }
    this.profiles = this.profiles.filter((item: HubProfile) => item.id !== id); this.publishDirectory();
    try { await this.saveProfiles(); } catch (_) { cleanupFailed = true; }
    if (cleanupFailed) { this.error = 'cleanup_incomplete'; throw new Error('cleanup_incomplete'); }
    this.cleanupProfiles.delete(id);
    try { await this.saveCleanup(); }
    catch (_) { this.cleanupProfiles.set(id, profile.hubId || id); this.pendingCleanupCount = this.cleanupProfiles.size; this.error = 'cleanup_incomplete'; throw new Error('cleanup_incomplete'); }
    if (this.cleanupProfiles.size === 0 && this.error === 'cleanup_incomplete') this.error = '';
    } finally { this.forgettingProfiles.delete(id); }
  }
  private event(payload: string): void {
    try {
      const data = Wire.parse(payload), type = Wire.text(data, 'type');
      if (type === 'hello') { const hubId = Wire.text(data, 'hub_id'); if (hubId && hubId !== this.profile.hubId) { this.repository?.close(); this.error = 'identity_changed'; return; } }
      this.workspace?.onEvent(data); this.deviceEvent?.(payload);
      for (const listener of this.eventListeners) { try { listener(payload); } catch (_) {} }
      if (type === 'hello') {
        this.features = Wire.strings(data, 'features'); this.connected = true; this.error = '';
        this.repository?.transport.hello(nativeNotifications.features(!this.detached && this.alerts));
        const generation = this.generation, scope = this.scope();
        this.reconcileWrites().then(async (): Promise<void> => { if (this.owns(generation, scope)) await this.refresh(); }).then(async (): Promise<void> => { if (this.owns(generation, scope)) await this.flushNotification(); }).catch((error: Error) => { if (this.owns(generation, scope)) this.failure(error); });
      } else if (type === 'notify') { if (!this.detached) nativeNotifications.receive(this, data); }
      else if (type === 'items' || type === 'items_replace') { this.transcript?.receive(data); } else if (type === 'terminal') {
        if (Wire.text(data, 'machine_id') === this.selected.machine && Wire.text(data, 'session_id') === this.selected.session)
          this.terminalText = (this.terminalText + Wire.text(data, 'data')).slice(-200000);
      } else if (['interaction', 'interactions', 'interaction_resolved', 'sessions', 'sessions_delta', 'item', 'items', 'session_delta', 'todo', 'inbox', 'task_plan', 'assistant_feed', 'managers', 'machines'].includes(type)) {
        if (this.feedTimer !== -1) clearTimeout(this.feedTimer);
        this.feedTimer = setTimeout(() => { this.feedTimer = -1; this.refresh().catch((error: Error) => this.failure(error)); }, 100);
      }
    } catch (_) { this.error = 'failed'; }
  }
  async refresh(): Promise<void> {
    const owner = JSON.stringify([this.generation, this.scope()]);
    if (this.refreshTask && this.refreshOwner === owner) return this.refreshTask;
    const task = this.refreshCurrent(); this.refreshTask = task; this.refreshOwner = owner;
    try { await task; } finally { if (this.refreshTask === task) this.refreshTask = undefined; }
  }
  private async refreshCurrent(): Promise<void> {
    if (this.demo && this.demoRepository) { this.refreshDemo(); if (this.isChat() && this.chatVisible) await this.loadChat(this.selected); return; }
    if (!this.connected || !this.repository) return;
    const generation = this.generation, scope = this.scope(), repo = this.repository;
    this.busy = true;
    try {
      const sessions = await repo.sessions(), assistant = await repo.call('/api/v1/assistant');
      const machines = await repo.machines();
      const managers = this.features.includes('managers') ? await repo.list('/api/v1/managers', 'managers', 'manager') : [];
      if (!this.owns(generation, scope)) return;
      sessions.forEach((row: ResourceRow) => row.group = this.profile.id); machines.forEach((row: ResourceRow) => row.group = this.profile.id);
      this.sessions = sessions; this.sessionsReady = true; this.machines = machines; this.managers = managers; this.publishDirectory();
      if (this.selected.kind === 'session') {
        const selected = sessions.find((item: ResourceRow) => item.id === this.selected.id && item.machine === this.selected.machine);
        if (selected) this.selected = selected;
        else { this.selected.raw = JSON.stringify(Wire.payload(['read_only'], [true])); this.selected.state = 'unknown'; this.clearPendingDecisions(); }
      }
      this.mainId = Wire.text(assistant, 'main');
      this.conversations = Wire.rows(assistant, 'conversations').map((data: Record<string, Object>) => {
        const row = ResourceRow.from(data, 'conversation'); row.manager = this.profile.manager;
        if (!row.machine) row.machine = Wire.text(assistant, 'machine_id'); if (!row.session) row.session = row.id;
        return row;
      });
      try { await this.local.put('snapshot:' + scope, JSON.stringify({ sessions: sessions, conversations: this.conversations })); } catch (_) { this.error = 'cache_unavailable'; }
      if (this.isChat() && this.chatVisible) await this.loadChat(this.selected);
      else if (this.isChat() && this.decisionsVisible) await this.refreshDecisions();
      if (this.section === 2) { const inbox = await repo.inbox(); if (this.owns(generation, scope)) this.inbox = inbox; }
    } catch (error) { if (this.owns(generation, scope)) this.failure(error as Error); }
    finally { if (this.owns(generation, scope)) { this.busy = false; this.publishDirectory(); } }
  }
  async openMain(): Promise<ResourceRow> {
    const generation = this.generation, scope = this.scope(), repository = this.repository;
    await this.refresh();
    if (!this.owns(generation, scope) || this.repository !== repository) throw new Error('request_updated');
    let row = this.conversations.find((item: ResourceRow) => item.id === this.mainId);
    if (!row) {
      row = await this.repo().newConversation(true, util.generateRandomUUID());
      if (!this.owns(generation, scope) || this.repository !== repository) throw new Error('request_updated');
      await this.refresh();
    }
    if (!this.owns(generation, scope) || this.repository !== repository) throw new Error('request_updated');
    if (!row.title) row.title = tr('main_chat'); await this.select(row);
    if (!this.owns(generation, scope) || this.selected.key() !== row.key()) throw new Error('request_updated'); return row;
  }
  async newConversation(): Promise<ResourceRow> {
    const generation = this.generation, scope = this.scope(), repository = this.repo();
    const row = await repository.newConversation(false, util.generateRandomUUID());
    if (!this.owns(generation, scope) || this.repository !== repository) throw new Error('request_updated');
    if (!row.title) row.title = tr('new_chat'); row.group = this.profile.id; await this.refresh();
    if (!this.owns(generation, scope) || this.repository !== repository) throw new Error('request_updated');
    await this.select(row); if (!this.owns(generation, scope) || this.selected.key() !== row.key()) throw new Error('request_updated'); return row;
  }
  async openNotification(target: Record<string, Object>): Promise<void> {
    const id = Wire.text(target, 'profile'), manager = Wire.text(target, 'manager'), machine = Wire.text(target, 'machine'), session = Wire.text(target, 'session');
    if ([id, manager, machine, session].some((value: string) => value.length > 256) || !id || !machine || !session) return;
    const saved = this.profiles.find((item: HubProfile) => item.id === id); if (!saved) return;
    this.pendingNotification = Wire.payload(['profile', 'manager', 'machine', 'session'], [id, manager, machine, session]);
    this.notificationEpoch++;
    if (!this.foreground) return;
    if (this.profile.id !== id || this.profile.manager !== manager || !this.connected) {
      const profile = new HubProfile(saved.id, saved.name, saved.baseUrl); profile.addresses = saved.addresses.slice(); profile.hubId = saved.hubId; profile.pin = saved.pin; profile.manager = manager;
      await this.selectProfile(profile, this.profile.id === id && this.profile.manager === manager, true);
    }
    await this.flushNotification();
  }
  private async flushNotification(): Promise<void> {
    const target = this.pendingNotification, epoch = this.notificationEpoch;
    if (!target || !this.foreground || !this.connected) return;
    const id = Wire.text(target, 'profile'), manager = Wire.text(target, 'manager'), machine = Wire.text(target, 'machine'), session = Wire.text(target, 'session');
    if (this.profile.id !== id || this.profile.manager !== manager || this.forgettingProfiles.has(id)) return;
    const generation = this.generation, scope = this.scope(); this.error = '';
    await this.refresh();
    if (epoch !== this.notificationEpoch || target !== this.pendingNotification || !this.owns(generation, scope) || !this.foreground || !this.connected || this.error) return;
    const row = this.conversations.concat(this.sessions).find((item: ResourceRow) => item.machine === machine && (item.session || item.id) === session);
    this.pendingNotification = undefined;
    if (row) this.notificationTarget = JSON.stringify(row); else this.error = 'request_updated';
  }
  private cachedRow(data: Record<string, Object>, kind: string): ResourceRow {
    const row = ResourceRow.from(data, kind); row.group = this.profile.id; row.raw = Wire.text(data, 'raw', '{}'); row.session = Wire.text(data, 'session', row.id); return row;
  }
  private cancelDraftTimer(): void { if (this.draftTimer !== -1) clearTimeout(this.draftTimer); this.draftTimer = -1; }
  async select(row: ResourceRow): Promise<void> {
    if (row.group && row.group !== this.profile.id || row.manager && row.manager !== this.profile.manager) throw new Error('request_updated');
    const generation = this.generation, scope = this.scope(), selection = ++this.selectionGeneration;
    this.cancelDraftTimer(); await this.persistDraft(); if (!this.owns(generation, scope) || selection !== this.selectionGeneration) return;
    this.closeTranscript(); this.releaseTerminal(); this.selected = row; this.messages = []; this.clearPendingDecisions(); this.error = '';
    this.draft = ''; this.sourceContext = ''; this.draftRevision = ''; this.draftLoading = true;
    this.localMessages = []; this.renderedMessages = []; this.echoedMessages.clear(); this.operationState = '';
    try {
      const saved = await new ChatDraftStore(this.local).read(this.target());
      if (!this.owns(generation, scope) || selection !== this.selectionGeneration) return;
      this.draft = saved.text; this.sourceContext = saved.context; this.draftRevision = saved.revision;
      await this.restoreOutbox(generation, scope);
    } finally { if (this.owns(generation, scope) && selection === this.selectionGeneration) this.draftLoading = false; }
    if (!this.owns(generation, scope) || selection !== this.selectionGeneration) return;
    this.chatVisible = this.isChat();
    if (this.chatVisible) await this.loadChat(row);
    // Dedicated resource workspaces own their reads and endpoint-specific loading/error state.
  }
  updateDraft(text: string): void {
    if (this.draftLoading) return;
    if (utf8Bytes(text) > 100000) { this.error = 'chat_attachment_text_too_large'; return; }
    if (text !== this.draft) this.draftRevision = util.generateRandomUUID();
    this.draft = text;
    if (this.draftTimer !== -1) clearTimeout(this.draftTimer);
    const generation = this.generation, scope = this.scope(), target = this.target();
    this.draftTimer = setTimeout(() => { this.draftTimer = -1; this.persistDraft().catch(() => { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }); }, 200);
  }
  async persistDraft(): Promise<void> {
    if (!this.profile.id || !this.selected.id) return;
    if (!this.draftRevision && (this.draft || this.sourceContext)) this.draftRevision = util.generateRandomUUID();
    const draft = new ChatDraft(); draft.text = this.draft; draft.context = this.sourceContext; draft.revision = this.draftRevision;
    await new ChatDraftStore(this.local).save(this.target(), draft);
  }
  async restoreLocalDelivery(): Promise<void> { await this.restoreOutbox(this.generation, this.scope()); }
  private async restoreOutbox(generation: number, scope: string): Promise<void> {
    const epoch = this.local.revision('outbox:' + scope), target = this.target(), repository = this.repository, entries = await new ChatDraftStore(this.local).list(scope);
    if (!this.owns(generation, scope) || target !== this.target() || !this.local.accepts('outbox:' + scope, epoch)) return;
    this.localMessages = entries;
    for (const entry of entries) if (this.local.accepts('outbox:' + scope, epoch) && entry.state === 'pending' && !entry.confirmedPending && !this.activeMessages.has(entry.id) && !this.unresolvedWrites.some((record: MutationRecord) => record.id === entry.id)) { entry.state = 'unknown'; await new ChatDraftStore(this.local).settle(scope, entry.id, 'unknown'); }
    for (const entry of entries) if (entry.target === target && entry.state === 'done') await this.completeLocalDelivery(entry, scope, repository);
    if (!this.owns(generation, scope) || target !== this.target() || !this.local.accepts('outbox:' + scope, epoch)) return;
    this.syncOutboxState(); this.publishMessages();
  }
  private syncOutboxState(): void {
    const entries = this.localMessages.filter((entry: ChatOutboxEntry) => entry.target === this.target() && !this.echoedMessages.has(entry.id));
    const blocking = entries.find((entry: ChatOutboxEntry) => ['pending', 'unknown'].includes(entry.state));
    this.operationState = blocking?.state || (entries.length ? entries[entries.length - 1].state : '');
  }
  private publishMessages(): void {
    const messages = this.localMessages.filter((entry: ChatOutboxEntry) => entry.target === this.target() && !this.echoedMessages.has(entry.id)).map((entry: ChatOutboxEntry): ChatMessage => {
      const display = messageDisplay(entry.text), content = parseMessageContent(display.text), message = new ChatMessage('local:' + entry.id, 'user', content.text);
      message.at = entry.at; message.source = display.sources; message.imagePaths = content.images; message.filePaths = content.files;
      message.ownerTarget = entry.target; message.localImageCount = entry.imageCount; message.localOperation = entry.id; message.deliveryState = entry.state; message.failed = entry.state === 'failed'; return message;
    });
    this.messages = this.renderedMessages.concat(messages);
  }
  private publishTranscript(items: readonly TranscriptItem[], running: boolean): void {
    this.revisionItems = items.slice(); this.renderedMessages = this.projectMessages(items, running);
    this.echoedMessages = new Set(items.filter((item: TranscriptItem) => item.role === 'user').map((item: TranscriptItem) => Wire.text(Wire.record(item as Object), 'client_msg_id')).filter((id: string) => !!id));
    const matched = this.localMessages.filter((entry: ChatOutboxEntry) => this.echoedMessages.has(entry.id)), scope = this.scope(), generation = this.generation, target = this.target();
    this.publishMessages();
    if (matched.length) {
      Promise.all(matched.map((entry: ChatOutboxEntry): Promise<void> => this.completeLocalDelivery(entry, scope))).then(async (): Promise<void> => {
        await new ChatDraftStore(this.local).discardEchoed(scope, matched.map((entry: ChatOutboxEntry) => entry.id));
        if (this.owns(generation, scope) && this.target() === target) { this.localMessages = this.localMessages.filter((entry: ChatOutboxEntry) => !matched.some((done: ChatOutboxEntry) => done.id === entry.id)); this.syncOutboxState(); this.publishMessages(); }
      }).catch(() => { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; });
    }
  }
  canCheckLocalMessage(id: string): boolean {
    const entry = this.localMessages.find((value: ChatOutboxEntry) => value.id === id && value.target === this.target());
    return this.connected && !!entry && entry.state === 'unknown' && !this.activeMessages.has(id) && !this.unresolvedWrites.some((record: MutationRecord) => record.id === id);
  }
  canRestoreMessage(id: string): boolean {
    const entry = this.localMessages.find((value: ChatOutboxEntry) => value.id === id && value.target === this.target());
    return !!entry && entry.state === 'failed' && (this.draft !== entry.draft || this.sourceContext !== entry.context);
  }
  async restoreMessageDraft(id: string): Promise<void> {
    const entry = this.localMessages.find((value: ChatOutboxEntry) => value.id === id && value.target === this.target());
    if (!entry || entry.state !== 'failed') return;
    const generation = this.generation, scope = this.scope(), target = this.target();
    this.cancelDraftTimer(); this.draft = entry.draft; this.sourceContext = entry.context; this.draftRevision = util.generateRandomUUID();
    try { await this.persistDraft(); } catch (_) { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }
  }
  private async completeLocalDelivery(entry: ChatOutboxEntry, scope: string, repository: HubRepository | undefined = this.repository): Promise<void> {
    const valid = (): boolean => !repository?.mutations?.scopeRetired && this.local.accepts('outbox:' + scope, entry.localEpoch);
    if (!valid()) return;
    if (entry.attachmentSnapshot) ChatAttachments.suppressStoredSnapshot(entry.attachmentSnapshot, entry.target);
    entry.state = 'done';
    if (this.target() === entry.target) {
      this.operationState = 'done';
      if (this.draftRevision === entry.revision) { this.cancelDraftTimer(); this.draft = ''; this.sourceContext = ''; this.draftRevision = ''; }
      this.publishMessages();
    }
    const store = new ChatDraftStore(this.local);
    const journal = repository?.mutations?.blocked().find((record: MutationRecord) => record.id === entry.id);
    if (journal) await repository?.mutations?.finish(journal, 'done');
    await store.settle(scope, entry.id, 'done'); await store.acknowledge(entry.target, entry.revision);
    if (entry.attachmentSnapshot) {
      if (!this.context) throw new Error('chat_attachment_storage_unavailable');
      await ChatAttachments.commitStoredSnapshot(this.context, entry.attachmentSnapshot, entry.target, valid);
      if (!valid()) return;
      await store.attachmentsCommitted(scope, entry.id); entry.attachmentSnapshot = '';
    }
  }
  private async cachedChat(row: ResourceRow): Promise<void> {
    const generation = this.generation, scope = this.scope(), target = this.target(row);
    const saved = await this.local.get('transcript:' + target);
    if (!this.owns(generation, scope) || target !== this.target()) return;
    if (saved) { const reducer = new TranscriptReducer(); reducer.restore(JSON.parse(saved) as TranscriptSnapshot); this.publishTranscript(reducer.items, false); this.hasMoreMessages = false; }
    this.clearPendingDecisions(); this.error = 'offline';
  }
  async loadChat(row: ResourceRow): Promise<void> {
    if (this.demo && this.demoRepository) { this.publishTranscript(this.demoRepository.historyItems(row) as Object as TranscriptItem[], false); this.hasMoreMessages = false; await this.refreshDecisions(); return; }
    const generation = this.generation, scope = this.scope(), target = this.target(row);
    if (!this.connected) { await this.cachedChat(row); return; }
    const sequence = ++this.decisionRefreshSequence;
    let interactions: Record<string, Object>[] = [], decisionsRead = false;
    try { interactions = await this.repo().decisions(); decisionsRead = true; }
    catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); }
    if (!this.owns(generation, scope) || this.target() !== target) return;
    if (decisionsRead) this.replacePendingDecisions(interactions, row, target, generation, scope, sequence);
    if (!this.transcript || this.transcriptKey !== target) {
      this.closeTranscript(); this.transcriptKey = target;
      this.transcript = new TranscriptModel(row, this.repo(), this.local, target, (items: readonly TranscriptItem[], hasMore: boolean) => {
        if (!this.owns(generation, scope) || this.target() !== target) return;
        this.hasMoreMessages = hasMore; this.publishTranscript(items, row.state === 'working' || Wire.bool(row.data(), 'working'));
      }, (error: Error) => { if (this.owns(generation, scope) && this.target() === target) this.failure(error); });
      await this.transcript.open();
    } else await this.transcript.latest();
  }
  private projectMessages(items: readonly TranscriptItem[], running: boolean): ChatMessage[] {
    const turns: TranscriptItem[][] = []; let turn: TranscriptItem[] = [];
    items.forEach((item: TranscriptItem) => {
      if (item.role === 'user' && item.kind === 'text' && turn.length > 0) { turns.push(turn); turn = []; }
      turn.push(item);
    });
    if (turn.length) turns.push(turn);
    const messages: ChatMessage[] = [];
    turns.forEach((group: TranscriptItem[], index: number) => {
      const projection = projectToolTurn(group[0].id, group, running && index === turns.length - 1);
      projection.userMessages.forEach((item: TranscriptItem) => { const display = messageDisplay(item.text || ''); const message = new ChatMessage(item.id, 'user', display.text); message.ownerTarget = this.target(); message.itemId = item.id; message.source = display.sources; const content = parseMessageContent(display.text); message.text = content.text; message.imagePaths = content.images; message.filePaths = content.files; message.at = this.itemTime(item); messages.push(message); });
      projection.userImages.forEach((item: TranscriptItem) => { const message = new ChatMessage(item.id, 'user'); message.imageId = item.id; message.ownerTarget = this.target(); message.itemId = item.id; message.at = this.itemTime(item); messages.push(message); });
      if (projection.summaryVisible) {
        const summary = new ChatMessage(projection.workKey, 'work');
        summary.work = projection.inspectionSteps.map((step: WorkInspectionStep) => this.workText(step)).filter((value: string) => !!value).join('\n\n');
        summary.raw = JSON.stringify(group); messages.push(summary);
      }
      if (projection.visibleAssistantText) { const message = new ChatMessage('visible-' + group[0].id, 'assistant', projection.visibleAssistantText.text || ''); message.ownerTarget = this.target(); message.itemId = projection.visibleAssistantText.id; messages.push(message); }
    });
    return messages;
  }
  private itemTime(item: TranscriptItem): number {
    const value = item['ts']; const parsed = typeof value === 'string' ? Date.parse(value) : 0;
    return Number.isFinite(parsed) ? parsed : 0;
  }
  private workText(step: WorkInspectionStep): string {
    if (step.kind === 'narration') return step.item.text || '';
    if (step.kind === 'fold') return tr('work_summary') + ' · ' + (step.toolCount + step.thoughtCount);
    const use = Wire.record(JSON.parse(JSON.stringify(step.use)) as Object);
    const name = Wire.text(use, 'tool', Wire.text(use, 'name', tr('work_summary')));
    const input = Wire.object(use, 'input');
    return [name, Object.keys(input).length ? JSON.stringify(input, null, 2) : step.use.text || '',
      step.results.map((item: TranscriptItem) => item.text || '').join('\n')].filter((value: string) => !!value).join('\n');
  }
  private closeTranscript(): void { this.transcript?.close(); this.transcript = undefined; this.transcriptKey = ''; this.revisionItems = []; }
  showConversation(row: ResourceRow): void { this.chatVisible = true; this.loadChat(row).catch((error: Error) => this.failure(error)); }
  hideConversation(releaseTools: boolean = true): void { this.chatVisible = false; this.closeTranscript(); this.clearPendingDecisions(); if (releaseTools) this.releaseTerminal(); this.persistDraft().catch(() => this.error = 'failed'); }
  async loadOlder(): Promise<void> { try { await this.transcript?.older(); } catch (error) { this.failure(error as Error); } }
  async expandWork(message: ChatMessage): Promise<void> {
    try {
      const items: TranscriptItem[] = JSON.parse(message.raw) as TranscriptItem[];
      for (const item of items) if (item.kind === 'fold') await this.transcript?.unfold(item.id);
    } catch (error) { this.failure(error as Error); }
  }
  private decodeDecision(data: Record<string, Object>): Decision {
    const decision = decodeInteractionDecision(data);
    const nested = Wire.object(data, 'interaction');
    if (!decision.revision) decision.revision = Wire.text(data, 'revision', Wire.text(nested, 'revision'));
    decision.machine = Wire.text(data, 'machine_id', decision.machine);
    decision.session = Wire.text(data, 'session_id', decision.session);
    if (this.operationBlocked('/api/v1/interactions/' + segment(decision.id) + '/resolve')) decision.status = 'unknown';
    return decision;
  }
  prepareMessageText(text: string = this.draft): string { return withSourceContext(text, this.sourceContext, this.features); }
  async send(): Promise<boolean> {
    const text = this.draft;
    if (utf8Bytes(text) > 100000) { this.error = 'chat_attachment_text_too_large'; return false; }
    if (this.draftLoading || this.decision || !this.canSend() || !text.trim() || ['pending', 'unknown'].includes(this.operationState)) return false;
    if (this.demo && this.demoRepository) {
      const prepared = this.prepareMessageText(text); if (utf8Bytes(prepared) > 100000) { this.error = 'chat_attachment_request_too_large'; return false; }
      try { this.demoRepository.sendLocal(this.selected, prepared); this.sourceContext = ''; this.updateDraft(''); await this.loadChat(this.selected); return true; }
      catch (error) { this.failure(error as Error); return false; }
    }
    const preparedText = this.prepareMessageText(text); if (utf8Bytes(preparedText) > 100000) { this.error = 'chat_attachment_request_too_large'; return false; }
    return this.dispatchMessage(this.repo(), this.selected, preparedText, [], this.generation, this.scope(), this.target(), text, this.sourceContext);
  }
  async sendPrepared(prepared: PreparedAttachmentSend, draft: string, context: string): Promise<boolean> {
    if (!prepared.repository || this.draftLoading || this.decision || !this.canSend() || ['pending', 'unknown'].includes(this.operationState) ||
      !this.owns(prepared.generation, prepared.scope) || this.target() !== prepared.modelTarget || this.repository !== prepared.repository ||
      this.target(prepared.row) !== prepared.modelTarget || (!prepared.text.trim() && !prepared.images.length)) return false;
    if (this.draft !== draft || this.sourceContext !== context) { this.error = 'chat_attachment_selection_changed'; return false; }
    return this.dispatchMessage(prepared.repository, prepared.row, prepared.text, prepared.images, prepared.generation, prepared.scope, prepared.modelTarget, draft, context, JSON.stringify(prepared.snapshot));
  }
  private async dispatchMessage(repo: HubRepository, row: ResourceRow, text: string, images: Object[], generation: number, scope: string, target: string, draft: string, context: string, attachmentSnapshot: string = ''): Promise<boolean> {
    const store = new ChatDraftStore(this.local), entry = new ChatOutboxEntry();
    if (!this.draftRevision) this.draftRevision = util.generateRandomUUID();
    entry.id = util.generateRandomUUID(); entry.target = target; entry.revision = this.draftRevision; entry.text = text; entry.context = context; entry.draft = draft; entry.imageCount = images.length; entry.attachmentSnapshot = attachmentSnapshot; entry.at = Date.now(); entry.localEpoch = this.local.revision('outbox:' + scope);
    this.activeMessages.add(entry.id); this.cancelDraftTimer(); this.operationState = 'pending'; this.error = ''; let dispatched = false, staged = false;
    try {
      const captured = new ChatDraft(); captured.text = draft; captured.context = context; captured.revision = entry.revision;
      await store.save(target, captured);
      if (!this.owns(generation, scope) || this.target() !== target || this.repository !== repo) { this.activeMessages.delete(entry.id); if (this.owns(generation, scope) && this.target() === target) { this.operationState = 'failed'; this.error = 'request_updated'; } return false; }
      await store.stage(scope, entry); staged = true;
      if (!this.owns(generation, scope) || this.target() !== target || this.repository !== repo || this.draftRevision !== entry.revision || this.decision || !this.canSend()) { await store.settle(scope, entry.id, 'failed'); this.activeMessages.delete(entry.id); entry.state = 'failed'; if (this.owns(generation, scope) && this.target() === target) { this.operationState = 'failed'; this.error = 'request_updated'; this.publishMessages(); } return false; }
      this.localMessages = this.localMessages.concat([entry]); this.publishMessages();
      dispatched = true; await repo.send(row, text, entry.id, '', images, (): boolean => this.owns(generation, scope) && this.target() === target && this.repository === repo && this.draftRevision === entry.revision && !this.decision && this.canSend());
    } catch (error) {
      if (entry.state === 'done') {
        this.activeMessages.delete(entry.id);
        try { await this.completeLocalDelivery(entry, scope, repo); } catch (_) { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }
        return true;
      }
      const pending = error instanceof HttpFailure && error.resultPending, unknown = error instanceof HttpFailure ? error.resultUnknown : dispatched;
      this.activeMessages.delete(entry.id); entry.state = pending ? 'pending' : unknown ? 'unknown' : 'failed'; entry.confirmedPending = pending;
      if (staged) { try { await store.settle(scope, entry.id, entry.state); } catch (_) {} }
      if (this.owns(generation, scope) && this.target() === target) {
        this.operationState = entry.state; this.failure(error as Error); if (unknown) this.error = pending ? 'pending' : 'result_unknown'; this.publishMessages();
      }
      return false;
    }
    this.activeMessages.delete(entry.id);
    // The acknowledgement belongs to the captured draft even if its destination is no longer on screen.
    try { await this.completeLocalDelivery(entry, scope, repo); } catch (_) { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }
    if (this.owns(generation, scope) && this.target() === target) {
      try { await this.loadChat(row); } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); }
    }
    return true;
  }
  async useMessageContext(message: ChatMessage): Promise<void> {
    if (!message.text || message.ownerTarget !== this.target() || !this.messages.some((item: ChatMessage) => item.id === message.id && item.text === message.text)) return;
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (utf8Bytes(message.text) > 100000) { this.error = 'chat_attachment_text_too_large'; return; }
    this.sourceContext = JSON.stringify(Wire.payload(['kind', 'title', 'item_id', 'machine_id', 'session_id', 'manager', 'text'],
      ['message', this.selected.title || tr('message'), message.itemId || message.id, this.selected.machine, this.selected.session || this.selected.id, this.profile.manager, message.text]));
    this.draftRevision = util.generateRandomUUID();
    try { await this.persistDraft(); } catch (_) { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }
  }
  async stop(): Promise<void> {
    if (!this.canInterrupt()) return;
    const generation = this.generation, scope = this.scope(), target = this.target(), row = this.selected, repo = this.repo();
    try { await repo.stop(row); } catch (error) {
      if (this.owns(generation, scope) && this.target() === target && this.repository === repo) this.failure(error as Error);
      return;
    }
    if (!this.owns(generation, scope) || this.target() !== target || this.repository !== repo) return;
    if (this.demo && this.demoRepository === repo) this.refreshDemo();
  }
  async answer(option: string, values: Record<string, Object>, expected?: Decision): Promise<void> {
    if (!this.canApprove()) return;
    const row = this.selected, target = this.target();
    let decision = expected ? this.pendingDecisions.find((item: Decision): boolean => item.id === expected.id && item.raw === expected.raw) : this.decision;
    if (!decision && expected && !this.pendingDecisionTarget && this.pendingDecisions.length === 0 && this.decision === expected) decision = expected;
    if (!decision || expected && (expected.id !== decision.id || expected.raw !== decision.raw || expected.machine !== decision.machine || expected.session !== decision.session)) { this.error = 'request_updated'; return; }
    const session = row.session || row.id;
    if (this.pendingDecisionTarget && (this.pendingDecisionTarget !== target || !this.pendingDecisions.some((item: Decision): boolean => item === decision) ||
      decision.machine !== row.machine || decision.session !== session || row.manager && row.manager !== this.profile.manager)) {
      this.error = 'request_updated'; return;
    }
    if (decision.status !== 'open') return;
    const personaQuestion = isPersonaAskUserQuestion(decision, row.kind);
    const request = new Decision();
    request.id = decision.id; request.raw = decision.raw; request.kind = decision.kind; request.revision = decision.revision;
    request.machine = decision.machine; request.session = decision.session; request.prompt = decision.prompt;
    request.options = decision.options.slice(); request.fields = decision.fields.slice(); request.status = decision.status;
    const generation = this.generation, scope = this.scope(), repo = this.repo();
    const current = (): boolean => this.owns(generation, scope) && this.target() === target && this.repository === repo &&
      (this.pendingDecisionTarget === target ? this.pendingDecisions.some((item: Decision): boolean => item === decision) :
        !this.pendingDecisionTarget && this.pendingDecisions.length === 0 && this.decision === decision);
    decision.status = 'pending';
    if (!personaQuestion) this.operationState = 'pending';
    try {
      await repo.resolve(request, option, values, util.generateRandomUUID());
    } catch (error) {
      if (!current()) return;
      decision.status = error instanceof HttpFailure && error.resultPending ? 'pending' : error instanceof HttpFailure && error.resultUnknown ? 'unknown' : 'open';
      if (!personaQuestion) this.operationState = ['pending', 'unknown'].includes(decision.status) ? decision.status : 'failed';
      this.failure(error as Error);
      if (error instanceof HttpFailure && (error.status === 409 || error.status === 410)) await this.loadChat(row);
      return;
    }
    if (!current()) return;
    decision.status = 'resolved';
    if (!personaQuestion) this.operationState = 'done';
    if (this.demo && this.demoRepository === repo) this.refreshDemo();
    // A refresh failure cannot turn an acknowledged approval into another sendable decision.
    try { await this.loadChat(row); } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); }
  }
  async listResources(kind: string): Promise<void> {
    this.error = ''; this.busy = true; const generation = this.generation, scope = this.scope();
    try {
      const rows = kind === 'todo' ? await this.repo().todos() : kind === 'knowledge' ? await this.repo().knowledge(this.knowledgeKind) : await this.repo().inbox();
      if (!this.owns(generation, scope)) return;
      this.resourceRows = rows;
      if (kind === 'todo') this.todos = rows; else if (kind === 'knowledge') this.knowledge = rows; else this.inbox = rows;
    } catch (error) { if (this.owns(generation, scope)) this.failure(error as Error); }
    finally { if (this.owns(generation, scope)) this.busy = false; }
  }
  async saveResource(row: ResourceRow, title: string, content: string, done?: boolean): Promise<boolean> {
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (row.manager !== this.profile.manager) return false;
    try {
      const repo = this.repo(); await repo.save(row, title, content, done);
      if (!this.owns(generation, scope)) return false;
      await this.listResources(row.kind);
      if (!this.owns(generation, scope) || this.target() !== target) return false;
      const refreshed = this.resourceRows.find((item: ResourceRow) => item.key() === row.key());
      if (refreshed && this.selected.key() === row.key()) this.selected = refreshed;
      return true;
    } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); return false; }
  }
  async deleteResource(row: ResourceRow): Promise<boolean> {
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (row.manager !== this.profile.manager) return false;
    try {
      const repo = this.repo(); await repo.remove(row);
      if (!this.owns(generation, scope)) return false;
      await this.listResources(row.kind); return this.owns(generation, scope) && this.target() === target;
    } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); return false; }
  }
  async handoff(row: ResourceRow): Promise<ResourceRow> {
    const owner = row.manager, generation = this.generation, scope = this.scope();
    const view = await this.viewFor(row);
    if (!this.owns(generation, scope)) throw new Error('request_updated');
    if (view !== this) { const target = await view.handoff(row); if (!this.owns(generation, scope)) throw new Error('request_updated'); return target; }
    const target = await this.openMain();
    if (!this.owns(generation, scope) || this.selected.key() !== target.key()) throw new Error('request_updated');
    const contexts = this.sourceContext ? Wire.rows(Wire.parse(this.sourceContext), 'contexts') : [];
    const ref = Wire.payload(['kind', 'id', 'title', 'manager'], [row.kind, row.id, row.title, owner]);
    if (!contexts.some((item: Record<string, Object>) => Wire.text(item, 'id') === row.id && Wire.text(item, 'kind') === row.kind)) contexts.push(ref);
    this.sourceContext = JSON.stringify(Wire.payload(['contexts'], [contexts])); this.draftRevision = util.generateRandomUUID();
    await this.persistDraft();
    if (!this.owns(generation, scope) || this.selected.key() !== target.key()) throw new Error('request_updated'); return target;
  }
  async pair(pairing: Pairing, target: PairingTarget, admit?: () => boolean): Promise<void> {
    if (!this.vault) throw new Error('storage_unavailable');
    const epoch = this.forgetEpochs.get(target.hubId) || 0, generation = this.generation, scope = this.scope();
    const current = (): boolean => this.owns(generation, scope) && this.foreground && epoch === (this.forgetEpochs.get(target.hubId) || 0) && !this.forgettingProfiles.has(target.hubId) && (!admit || admit());
    if (!current()) throw new Error('request_updated');
    const config = await pairing.redeem(target, tr('phone'), current);
    if (!current()) throw new Error('request_updated');
    await this.useEndpoint(config, target.name, current);
  }
  private capabilities(): EffectiveSessionCapabilities {
    const source = this.sessions.find((item: ResourceRow) => item.machine === this.selected.machine && (item.session || item.id) === (this.selected.session || this.selected.id));
    const data = (source || this.selected).data(), caps = Wire.object(data, 'capabilities');
    const input: SessionCapabilityInput = { sourceKind: Wire.text(data, 'source_kind'),
      readOnly: Wire.bool(data, 'read_only', Wire.bool(data, 'readonly')), readOnlyReason: Wire.text(data, 'read_only_reason'),
      capabilities: { read: Wire.bool(caps, 'read'), send: Wire.bool(caps, 'send'), interrupt: Wire.bool(caps, 'interrupt'),
        approve: Wire.bool(caps, 'approve'), terminal: Wire.bool(caps, 'terminal'), attach: Wire.bool(caps, 'attach') } };
    return sessionCapabilities(input);
  }
  canAttach(): boolean { return this.connected && this.capabilities().canAttach; }
  async attach(): Promise<void> {
    if (!this.canAttach()) return; const generation = this.generation, scope = this.scope(), row = this.selected;
    try { await this.repo().call('/api/v1/sessions/' + segment(row.machine) + '/' + segment(row.session || row.id) + '/attach', 'POST');
      await this.refresh(); if (this.owns(generation, scope) && this.target(row) === this.target()) { const updated = this.sessions.find((item: ResourceRow) => item.key() === row.key()); if (updated) await this.select(updated); }
    } catch (error) { if (this.owns(generation, scope)) this.failure(error as Error); }
  }
  private decisionsReady(): boolean {
    if (!this.isChat() || this.decisionReadyTarget === this.target()) return true;
    // Preserve compatibility with callers that still provide only the blocking decision.
    return !this.pendingDecisionTarget && this.pendingDecisions.length === 0 && this.decision !== undefined;
  }
  canSend(): boolean { return this.connected && this.capabilities().canSend && this.decisionsReady(); }
  canCompose(): boolean { return this.isChat() && this.capabilities().canSend; }
  canApprove(): boolean { return this.connected && this.capabilities().canApprove && this.decisionsReady(); }
  canInterrupt(): boolean { return this.connected && this.capabilities().canInterrupt; }
  contextTitle(): string {
    if (!this.sourceContext) return ''; const data = Wire.parse(this.sourceContext);
    return Wire.rows(data, 'contexts').map((item: Record<string, Object>) => Wire.text(item, 'title')).join(' · ') || Wire.text(data, 'title');
  }
  shortPath(path: string): string { return path.replace(/\\/g, '/').split('/').filter((part: string) => !!part).slice(-2).join('/'); }
  machineName(id: string): string { return this.machines.find((row: ResourceRow) => row.id === id)?.title || id; }
  async reconcileWrites(): Promise<void> {
    const repo = this.repo(), generation = this.generation, scope = this.scope();
    if (!this.features.includes('client_ops')) return;
    const records = (repo.mutations?.blocked() || []).slice();
    for (const entry of await new ChatDraftStore(this.local).list(scope)) if (['pending', 'unknown'].includes(entry.state) && !records.some((record: MutationRecord) => record.id === entry.id) && !this.activeMessages.has(entry.id)) { const record = new MutationRecord(); record.id = entry.id; record.state = entry.state; records.push(record); }
    for (const record of records) {
      try {
        const result = await repo.call('/api/v1/ops/' + segment(record.id));
        if (!this.owns(generation, scope)) return;
        const state = Wire.text(result, 'state');
        if (['done', 'failed', 'unknown', 'pending'].includes(state)) {
          if (repo.mutations?.blocked().some((item: MutationRecord) => item.id === record.id)) await repo.mutations?.finish(record, state);
          const entry = (await new ChatDraftStore(this.local).list(scope)).find((item: ChatOutboxEntry) => item.id === record.id);
          if (entry) { if (state === 'done') await this.completeLocalDelivery(entry, scope); else await new ChatDraftStore(this.local).settle(scope, entry.id, state); }
        }
      } catch (_) { /* A missing receipt never proves that a write did not execute. */ }
    }
    if (this.owns(generation, scope)) { this.unresolvedWrites = repo.mutations?.blocked() || [];
      await this.restoreOutbox(generation, scope); }
  }
  async acknowledgeWrite(id: string): Promise<void> {
    const repo = this.repo(), generation = this.generation, scope = this.scope();
    if (this.features.includes('client_ops')) {
      try {
        const result = await repo.call('/api/v1/ops/' + segment(id)); if (!this.owns(generation, scope)) return;
        const state = Wire.text(result, 'state'), record = repo.mutations?.blocked().find((item: MutationRecord) => item.id === id);
        if (state !== 'unknown') {
          if (record && ['done', 'failed', 'pending'].includes(state)) await repo.mutations?.finish(record, state);
          const entry = (await new ChatDraftStore(this.local).list(scope)).find((item: ChatOutboxEntry) => item.id === id);
          if (entry && state === 'done') await this.completeLocalDelivery(entry, scope, repo);
          else if (entry && ['failed', 'pending'].includes(state)) await new ChatDraftStore(this.local).settle(scope, id, state);
          if (this.owns(generation, scope)) { this.unresolvedWrites = repo.mutations?.blocked() || []; await this.restoreOutbox(generation, scope); } return;
        }
      } catch (error) {
        const localOnly = !(repo.mutations?.blocked().some((record: MutationRecord) => record.id === id));
        if (!(error instanceof HttpFailure && error.status === 404 && localOnly)) { if (this.owns(generation, scope)) this.failure(error as Error); return; }
        if (!this.owns(generation, scope) || !this.canCheckLocalMessage(id)) return;
      }
    }
    await repo.mutations?.acknowledge(id);
    if (repo.mutations?.blocked().some((record: MutationRecord) => record.id === id)) return;
    await new ChatDraftStore(this.local).settle(scope, id, 'checked');
    if (this.owns(generation, scope)) { this.unresolvedWrites = repo.mutations?.blocked() || []; await this.restoreOutbox(generation, scope); }
  }
  private viewKey(key: string): string { return 'view:' + (key === 'ssh-tunnel' ? scopeKey({ groupId: this.profile.id, managerId: '' }) : this.scope()) + ':' + key; }
  viewRevision(key: string): number { return this.local.revision(this.viewKey(key)); }
  /** Outcome bookkeeping stays on the admitted scope, even after navigation; Forget retires the captured epoch. */
  captureView(key: string): ClientViewLease {
    const path = this.viewKey(key), local = this.local, epoch = local.revision(path);
    const valid = (): boolean => local.accepts(path, epoch);
    return { epoch: epoch, valid: valid,
      read: async (): Promise<string> => {
        if (!valid()) throw new Error('request_updated');
        const result = await local.get(path);
        if (!valid()) throw new Error('request_updated');
        return result;
      },
      write: async (value: string): Promise<void> => {
        if (!valid()) throw new Error('request_updated');
        await local.put(path, value, epoch);
        if (!valid()) throw new Error('request_updated');
      } };
  }
  async saveView(key: string, value: string, expectedEpoch: number = this.viewRevision(key)): Promise<void> { await this.local.put(this.viewKey(key), value, expectedEpoch); }
  async loadView(key: string): Promise<string> { return this.local.get(this.viewKey(key)); }
  removeContext(): void { this.sourceContext = ''; this.draftRevision = util.generateRandomUUID(); this.persistDraft().catch(() => this.error = 'failed'); }
  async createAgent(prompt: string, machine: ResourceRow, directory: string, launcher: string, todoId: string = '', admit?: () => boolean): Promise<ResourceRow> {
    if (machine.manager !== this.profile.manager) throw new Error('request_updated');
    const generation = this.generation, scope = this.scope(), repo = this.repo();
    const row = await repo.createAgent(prompt, machine, directory, launcher, util.generateRandomUUID(), todoId, (): boolean => this.owns(generation, scope) && (!admit || admit()));
    if (!this.owns(generation, scope)) throw new Error('request_updated');
    try { await this.refresh(); } catch (_) { if (this.owns(generation, scope)) this.error = 'failed'; }
    if (!this.owns(generation, scope)) throw new Error('request_updated'); row.group = this.profile.id; return row;
  }
  async saveKnowledge(row: ResourceRow, tags: string, importance: string): Promise<boolean> {
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (row.manager !== this.profile.manager) return false;
    try {
      const repo = this.repo(); await repo.saveKnowledge(row, tags.split(',').map((value: string) => value.trim()).filter((value: string) => !!value), importance);
      if (!this.owns(generation, scope)) return false;
      await this.listResources('knowledge'); return this.owns(generation, scope) && this.target() === target;
    } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); return false; }
  }
  async searchKnowledge(query: string): Promise<void> {
    const generation = this.generation, scope = this.scope(), kind = this.knowledgeKind;
    try { const repo = this.repo(), rows = await repo.knowledge(kind, query); if (this.owns(generation, scope) && this.knowledgeKind === kind) this.resourceRows = rows; }
    catch (error) { if (this.owns(generation, scope) && this.knowledgeKind === kind) this.failure(error as Error); }
  }
  async replyTo(row: ResourceRow, text: string): Promise<boolean> {
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (row.manager !== this.profile.manager) return false;
    try {
      const repo = this.repo(), next = await repo.reply(row, text, util.generateRandomUUID());
      if (!this.owns(generation, scope)) return false;
      if (this.target() === target && this.selected.key() === row.key()) this.selected = next;
      this.inbox = this.inbox.map((item: ResourceRow) => item.key() === row.key() ? next : item); return true;
    } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); return false; }
  }
  async processInbox(row: ResourceRow): Promise<void> {
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (row.manager !== this.profile.manager) return;
    try {
      const repo = this.repo(), next = await repo.processInbox(row);
      if (!this.owns(generation, scope)) return;
      if (this.target() === target && this.selected.key() === row.key()) this.selected = next;
      this.inbox = this.inbox.map((item: ResourceRow) => item.key() === row.key() ? next : item);
    } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); }
  }
  private refreshDemo(): void {
    const demo = this.demoRepository; if (!this.demo || !demo) return;
    const roster = demo.roster(); this.features = roster.features; this.mainId = roster.mainId;
    this.sessions = roster.sessions; this.sessionsReady = true; this.machines = roster.machines; this.conversations = roster.conversations;
    this.todos = roster.todos; this.knowledge = roster.knowledge; this.inbox = roster.inbox;
    this.managers = [new ResourceRow('demo', tr('local_demo'), 'manager')];
    if (this.selected.kind === 'session') { const current = this.sessions.find((row: ResourceRow) => row.id === this.selected.id && row.machine === this.selected.machine); if (current) this.selected = current; }
    this.directoryReady = true; this.directoryLoading = false; this.directoryUnavailable = 0; this.publishDirectory();
  }
  useDemo(): void {
    this.ssh.stop(); this.stopReconnect(); this.cancelDraftTimer(); this.closeTranscript(); this.releaseTerminal(); this.retireViews(); this.repository?.close(); this.generation++; this.selectionGeneration++;
    this.demo = true; this.connected = true; this.profile = new HubProfile('demo', tr('local_demo')); this.profile.hubId = 'demo'; this.profile.manager = 'demo';
    this.demoRepository = new DemoRepository(this.local); this.repository = this.demoRepository;
    this.selected = new ResourceRow(); this.messages = []; this.revisionItems = []; this.clearPendingDecisions(); this.draft = ''; this.sourceContext = ''; this.draftRevision = ''; this.draftLoading = false;
    this.localMessages = []; this.renderedMessages = []; this.echoedMessages.clear(); this.unresolvedWrites = []; this.operationState = ''; this.resourceRows = []; this.detail = ''; this.reply = '';
    this.knowledgeKind = 'projects'; this.refreshDemo(); this.error = ''; this.section = 0;
  }
  releaseTerminal(): void { this.workspace?.hide(); this.terminalControl = false; }
  async background(): Promise<void> {
    this.foreground = false; this.ssh.stop(); this.stopReconnect(); this.cancelDraftTimer(); this.closeTranscript(); this.clearPendingDecisions(); this.releaseTerminal();
    const repository = this.repository; repository?.close();
    const children = Array.from(this.views.values()).map((view: ClientModel): Promise<void> => view.background());
    await this.persistDraft(); await Promise.all(children);
  }
  activateForeground(): void { this.foreground = true; for (const view of this.views.values()) view.activateForeground(); }
  async foregrounded(): Promise<void> { if (this.retired) return; this.activateForeground(); await this.retryCleanup(); if (this.retired || !this.foreground) return; if (this.profile.id && !this.demo) await this.selectProfile(this.profile, true); await this.flushNotification(); }
  async shutdown(): Promise<void> { await this.background(); this.retireViews(); if (!this.detached) await this.local.close(); }
}

export const client = new ClientModel();
