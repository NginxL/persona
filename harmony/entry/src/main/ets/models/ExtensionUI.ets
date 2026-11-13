import type { ExtensionUIStreamSnapshot, ExtensionViewRecord } from '../core/ExtensionViewTree';
import { url, util } from '@kit.ArkTS';
import { ClientModel } from './ClientModel';
import { segment } from './HubRepository';
import { Wire } from './Wire';
import { tr } from '../design/Strings';
import type { ExtensionPresentation } from '../core/ExtensionPresentation';
import {
  actionForStreamEvent, extensionUIActionSlotsForView, extensionUIFieldValues, parseExtensionUIFrame, parseExtensionUIEvent,
  reduceExtensionUIStream, streamRequestText, validStreamInteractionValue
} from '../core/ExtensionViewTree';

export interface ExtensionUINavigationEffect {
  kind: string;
  extensionId: string;
  uiId: string;
  state: string;
  title: string;
  context: ExtensionViewRecord;
}

const STREAM_FEATURE: string = 'extension_ui_stream_v1';
const MAX_STACK: number = 8;
const MAX_PENDING_REQUESTS: number = 64;
const MAX_PENDING_EVENTS: number = 32;
const MAX_PRESENTED_RECEIPTS: number = 64;
const STREAM_WAIT_MS: number = 45000;

/** Host confirmation compares saved actions structurally, independent of JSON member order. */
function equalActionValue(left: Object | undefined, right: Object | undefined, depth: number = 0, budget: number[] = [0]): boolean {
  if (++budget[0] > 4096 || depth > 16) return false;
  if (left === right) return true;
  if (left === null || right === null || typeof left !== 'object' || typeof right !== 'object' || Array.isArray(left) !== Array.isArray(right)) return false;
  if (Array.isArray(left)) {
    const a = left as Object[], b = right as Object[];
    return a.length === b.length && a.every((value: Object, index: number): boolean => equalActionValue(value, b[index], depth + 1, budget));
  }
  const a = Wire.record(left), b = Wire.record(right), keys = Object.keys(a), names = new Set<string>(Object.keys(b));
  return keys.length === names.size && keys.every((key: string): boolean => names.has(key) && equalActionValue(a[key], b[key], depth + 1, budget));
}

class ExtensionUIPageState {
  key: string = '';
  ui: string = '';
  state: string = '';
  title: string = '';
  view: ExtensionViewRecord = { sections: [] };
  snapshot: ExtensionUIStreamSnapshot | undefined = undefined;
  retiredEpochs: Set<string> = new Set();
  openRequest: ExtensionViewRecord | undefined = undefined;
  stream: boolean = false;
  preferLegacy: boolean = false;
  hasView: boolean = false;
  dirtyRevision: number = 0;
  dirty: boolean = false;
  refreshDeadline: number = 0;
  presentationGeneration: number = 0;
  constructor(ui: string, state: string = '', title: string = '') {
    this.key = util.generateRandomUUID(); this.ui = ui; this.state = state; this.title = title;
  }
}

class ExtensionUIPendingRequest {
  kind: string = '';
  pageKey: string = '';
  eventId: string = '';
  constructor(kind: string, pageKey: string, eventId: string = '') { this.kind = kind; this.pageKey = pageKey; this.eventId = eventId; }
}

class ExtensionUIEvent {
  interaction: ExtensionViewRecord = {};
  action: ExtensionViewRecord = {};
  values: Record<string, Object> = {};
  pageKey: string = '';
  requestId: string = '';
  status: string = 'pending';
  confirmed: boolean = false;
  timeout: number = -1;
  constructor(interaction: ExtensionViewRecord, action: ExtensionViewRecord, values: Record<string, Object>, pageKey: string, requestId: string) {
    this.interaction = interaction; this.action = action; this.values = values; this.pageKey = pageKey; this.requestId = requestId;
  }
}

class ExtensionUIConfirmation {
  confirmed: boolean = false;
  token: string = '';
  pageKey: string = '';
  generation: number = 0;
  scope: string = '';
  revision: number = 0;
  version: number = 0;
  ui: string = '';
  actionJSON: string = '';
  contextJSON: string = '';
  key: string = '';
  actionId: string = '';
  event: string = '';
  values: Record<string, Object> = {};
  title: string = '';
  impact: string = '';
}

class ExtensionUIDetachedOpen {
  client: ClientModel;
  requestId: string;
  generation: number;
  scope: string;
  managerId: string;
  off: (() => void) | undefined = undefined;
  timer: number = -1;
  constructor(client: ClientModel, requestId: string, generation: number, scope: string, managerId: string) {
    this.client = client; this.requestId = requestId; this.generation = generation; this.scope = scope; this.managerId = managerId;
  }
}

function cloneRecord(value: ExtensionViewRecord): ExtensionViewRecord { return JSON.parse(JSON.stringify(value)) as ExtensionViewRecord; }
function actionPush(action: ExtensionViewRecord): ExtensionViewRecord { return Wire.object(action, 'push'); }
function secureWebURL(value: string): boolean {
  if (!value || value.length > 4096 || !value.startsWith('https://') || /[\r\n\\]/.test(value)) return false;
  try {
    const parsed: url.URL = url.URL.parseURL(value);
    return parsed.protocol === 'https:' && !!parsed.hostname && !parsed.username && !parsed.password;
  } catch (_) { return false; }
}
function requestBody(context: ExtensionViewRecord, state: string): ExtensionViewRecord {
  const body: ExtensionViewRecord = {};
  body['context'] = cloneRecord(context); body['state'] = state;
  return body;
}
function saveViewFromResponse(value: ExtensionViewRecord): ExtensionViewRecord { return Wire.object(value, 'view'); }

@ObservedV2
export class ExtensionUIModel implements ExtensionPresentation {
  @Trace view: ExtensionViewRecord = { sections: [] };
  @Trace pageTitle: string = '';
  @Trace loading: boolean = false;
  @Trace busy: boolean = false;
  @Trace error: string = '';
  @Trace notice: string = '';
  @Trace phase: string = 'cold';
  @Trace revision: number = 0;
  @Trace viewVersion: number = 0;
  @Trace confirmationToken: string = '';
  @Trace confirmationText: string = '';
  @Trace confirmationTitle: string = '';
  @Trace uncertainEventId: string = '';
  @Trace streamStackDepth: number = 1;
  @Trace lineResultRevision: number = 0;

  private client: ClientModel | undefined;
  private extensionId: string = '';
  private rootUI: string = '';
  private compact: boolean = false;
  private baseContext: ExtensionViewRecord = {};
  private initialRouteState: string = '';
  private legacyRefreshTimer: number = -1;
  private callbackLink: (address: string) => void = (_address: string): void => {};
  private callbackNavigate: ((effect: ExtensionUINavigationEffect) => void) | undefined = undefined;
  private stack: ExtensionUIPageState[] = [];
  private active: boolean = false;
  private disposed: boolean = false;
  private generation: number = 0;
  private scope: string = '';
  private profileId: string = '';
  private managerId: string = '';
  private eventsOff: (() => void) | undefined;
  private openRead: boolean = false;
  private recoveryRead: boolean = false;
  private requestPages: Map<string, ExtensionUIPendingRequest> = new Map();
  private events: Map<string, ExtensionUIEvent> = new Map();
  private uncertainEvents: Map<string, ExtensionUIEvent> = new Map();
  private presentations: Map<string, ExtensionViewRecord> = new Map();
  private presented: Set<string> = new Set();
  private drafts: Map<string, string> = new Map();
  private secrets: Map<string, string> = new Map();
  private answeredHostConfirmations: Map<string, number> = new Map();
  private pendingConfirmation: ExtensionUIConfirmation | undefined;
  private confirmationResult: ((accepted: boolean) => void) | undefined;
  private detachedOpens: Map<string, ExtensionUIDetachedOpen> = new Map();
  private operationSequence: number = 0;
  private lineResultJSON: string = '';
  private lineResultKey: string = '';
  private lineResultPageKey: string = '';
  private lineResultPath: string = '';

  bind(client: ClientModel, extensionId: string, uiId: string, context: ExtensionViewRecord = {}, initialView?: ExtensionViewRecord,
    compact: boolean = false, onLink: (address: string) => void = (_address: string): void => {}, visible: boolean = true, initialState: string = ''): void {
    if (this.client === client && this.extensionId === extensionId && this.rootUI === uiId && this.profileId === client.profile.id && this.managerId === client.profile.manager) {
      this.callbackLink = onLink;
      const nextContext = cloneRecord(context);
      if (JSON.stringify(nextContext) !== JSON.stringify(this.baseContext) || this.initialRouteState !== initialState) {
        this.detachPendingOpen(this.currentPage());
        for (const previous of this.stack) {
          if (previous.snapshot && client.connected) {
            const close: ExtensionViewRecord = { request: 'close', protocol: 1, request_id: util.generateRandomUUID(), session: previous.snapshot.session, epoch: previous.snapshot.epoch };
            try { client.repo().transport.send(streamRequestText(close, this.managerEnvelopeValue())); } catch (_) { }
          }
        }
        this.requestPages.clear(); this.clearAllEventTimeouts(); this.events.clear(); this.uncertainEvents.clear(); this.presentations.clear(); this.presented.clear();
        this.baseContext = nextContext; this.initialRouteState = initialState; this.stack = [new ExtensionUIPageState(uiId, initialState)]; this.active = visible;
        this.cancelConfirmation(); this.clearFields(); this.lineResultJSON = ''; this.lineResultKey = ''; this.lineResultPageKey = ''; this.lineResultPath = '';
        this.busy = false; this.uncertainEventId = ''; this.phase = 'cold'; this.view = { sections: [] }; this.revision++;
        if (this.active) this.open();
        return;
      }
      if (initialView && this.revision === 0) this.setView(initialView);
      return;
    }
    this.dispose();
    this.client = client; this.extensionId = extensionId; this.rootUI = uiId; this.compact = compact;
    this.baseContext = cloneRecord(context); this.initialRouteState = initialState; this.callbackLink = onLink; this.profileId = client.profile.id; this.managerId = client.profile.manager;
    this.generation = client.connectionGeneration(); this.scope = client.scope(); this.active = visible; this.disposed = false;
    this.stack = [new ExtensionUIPageState(uiId, initialState)]; this.eventsOff = client.observeEvents((raw: string): void => this.receive(raw));
    this.streamStackDepth = 1;
    if (initialView) this.setView(initialView);
    if (this.active && this.currentPage() && !initialView) this.openCurrent();
  }

  setLinkHandler(onLink: (address: string) => void): void { this.callbackLink = onLink; }
  setNavigationHandler(onNavigate?: (effect: ExtensionUINavigationEffect) => void): void { this.callbackNavigate = onNavigate; }
  private currentPage(): ExtensionUIPageState | undefined { return this.stack.length ? this.stack[this.stack.length - 1] : undefined; }
  get presentationKey(): string {
    const snapshot = this.currentPage()?.snapshot;
    return JSON.stringify([this.profileId, this.managerId, this.extensionId, this.currentPage()?.key || '', snapshot?.session || '', snapshot?.epoch || '', this.currentPage()?.presentationGeneration || 0]);
  }
  currentUI(): string { return this.currentPage()?.ui || this.rootUI; }
  getSnapshot(): ExtensionUIStreamSnapshot | undefined { return this.currentPage()?.snapshot; }
  isStream(): boolean { return !!this.currentPage()?.stream; }
  isReady(): boolean { return this.phase === 'ready'; }
  pageState(): string { return this.currentPage()?.state || ''; }
  canInteract(): boolean {
    return this.active && this.phase === 'ready' && !this.busy && !this.uncertainEventId && !this.pendingConfirmation && this.ownerCurrent() && !!this.client?.connected;
  }

  private ownerCurrent(): boolean {
    const client = this.client;
    return !!client && !this.disposed && client.profile.id === this.profileId && client.profile.manager === this.managerId && client.owns(this.generation, this.scope);
  }
  private pageByKey(key: string): ExtensionUIPageState | undefined { return this.stack.find((page: ExtensionUIPageState) => page.key === key); }
  private contextForStream(): ExtensionViewRecord {
    const context = cloneRecord(this.baseContext); context['ui_version'] = 7;
    const stream: ExtensionViewRecord = { protocol: 1, components: ExtensionUIModel.streamComponents() };
    context['stream'] = stream;
    return context;
  }
  static streamComponents(): string[] {
    return ['text', 'metric', 'progress', 'row', 'button', 'link', 'field', 'divider', 'chart', 'stat_grid', 'segmented',
      'markdown', 'table', 'empty', 'loading', 'header', 'breadcrumb', 'file_tree', 'code', 'diff', 'comment_thread', 'composer',
      'image', 'grid', 'carousel'];
  }
  private contextForLegacy(): ExtensionViewRecord {
    const context: ExtensionViewRecord = {};
    for (const key of Object.keys(this.baseContext)) if (key !== 'stream') context[key] = this.baseContext[key];
    context['ui_version'] = 6;
    return context;
  }
  private apiPath(ui: string): string { return '/api/v1/extensions/' + segment(this.extensionId) + '/-/ui/' + segment(ui); }
  private async renderPage(page: ExtensionUIPageState, keep: boolean = false): Promise<void> {
    const client = this.client, generation = this.generation, scope = this.scope;
    if (!client || !this.active || !this.ownerCurrent() || !client.connected) { this.error = 'extension_ui_offline'; return; }
    this.clearLegacyRefresh();
    const dirtyRevision = page.dirtyRevision;
    const turn = ++this.operationSequence; this.loading = true; this.error = ''; this.phase = keep ? 'recovering' : 'opening';
    const context = this.contextForLegacy();
    const body: ExtensionViewRecord = requestBody(context, page.state);
    try {
      const raw = await client.repo().transport.request(this.apiPath(page.ui), 'POST', JSON.stringify(body));
      if (turn !== this.operationSequence || !this.ownerCurrent() || generation !== client.connectionGeneration() || scope !== client.scope() || this.currentPage() !== page) return;
      const envelope = Wire.parse(raw), received = saveViewFromResponse(envelope);
      if (!Object.keys(received).length) throw new Error('extension_ui_invalid_view');
      const currentState = Wire.text(received, 'state'); page.state = currentState;
      page.view = received; page.stream = false; page.snapshot = undefined; page.hasView = true;
      page.dirty = page.dirtyRevision !== dirtyRevision;
      const refreshAfter = Wire.number(received, 'refresh_after');
      page.refreshDeadline = refreshAfter > 0 ? Date.now() + Math.max(30, Math.min(86400, refreshAfter)) * 1000 : 0;
      this.publishPage(page, keep); this.phase = 'ready';
    } catch (error) {
      if (turn === this.operationSequence && this.ownerCurrent() && this.currentPage() === page) { this.error = this.safeError(error as Error); this.phase = 'failed'; page.refreshDeadline = 0; page.dirty = false; }
    } finally { if (turn === this.operationSequence && this.currentPage() === page) { this.loading = false; this.scheduleLegacyRefresh(page); } }
  }
  private clearLegacyRefresh(): void {
    if (this.legacyRefreshTimer !== -1) clearTimeout(this.legacyRefreshTimer);
    this.legacyRefreshTimer = -1;
  }
  private scheduleLegacyRefresh(page: ExtensionUIPageState): void {
    this.clearLegacyRefresh();
    if (!this.active || this.disposed || page.stream || this.currentPage() !== page || !this.ownerCurrent() || !this.client?.connected) return;
    if (!page.dirty && !page.refreshDeadline) return;
    const delay = page.dirty ? 0 : Math.max(0, page.refreshDeadline - Date.now());
    this.legacyRefreshTimer = setTimeout(() => {
      this.legacyRefreshTimer = -1;
      if (!this.active || this.currentPage() !== page || !this.ownerCurrent()) return;
      if (this.busy || this.loading) { page.dirty = true; return; }
      this.renderPage(page, true).catch(() => undefined);
    }, delay);
  }
  private receiveInvalidation(frame: ExtensionViewRecord): void {
    if (this.disposed || !this.ownerCurrent() || Wire.text(frame, 'extension') !== this.extensionId || !Wire.bool(frame, 'invalidate') ||
      Wire.has(frame, 'manager') && Wire.text(frame, 'manager') !== this.managerId) return;
    const contribution = Wire.text(frame, 'id');
    for (const page of this.stack) if (!page.stream && (!contribution || page.ui === contribution)) {
      page.dirty = true; page.dirtyRevision++;
      if (this.currentPage() === page && this.active) this.scheduleLegacyRefresh(page);
    }
  }
  private publishPage(page: ExtensionUIPageState, keep: boolean = false): void {
    if (this.currentPage() !== page) return;
    if (!keep) this.drafts.clear();
    this.view = cloneRecord(page.view);
    for (const key of Array.from(this.secrets.keys())) if (!this.isSecretField(key)) this.secrets.delete(key);
    this.pageTitle = Wire.text(page.view, 'title', page.title || this.extensionId);
    this.viewVersion = page.snapshot?.version || 0; this.revision++;
    if (!page.stream) { const seconds = Wire.number(page.view, 'refresh_after'); page.refreshDeadline = seconds > 0 ? Date.now() + Math.max(30, Math.min(86400, seconds)) * 1000 : 0; this.scheduleLegacyRefresh(page); }
  }
  private async openCurrent(): Promise<void> {
    const page = this.currentPage(), client = this.client;
    if (!page || !client || this.disposed || !this.active) return;
    if (!client.connected) { this.phase = 'disconnected'; this.error = 'extension_ui_offline'; return; }
    if (!this.ownerCurrent()) { this.error = 'request_updated'; return; }
    if (this.compact || page.preferLegacy || !client.features.includes(STREAM_FEATURE)) {
      page.stream = false; this.phase = 'opening'; await this.renderPage(page, page.dirty); return;
    }
    page.stream = true; this.phase = 'opening'; this.loading = true; this.error = '';
    if (!page.openRequest) page.openRequest = this.makeOpenRequest(page);
    const sent = this.sendLifecycle(page.openRequest, 'open', page.key);
    this.openRead = true;
    if (!sent) { this.loading = false; this.phase = 'disconnected'; this.error = this.error || 'extension_ui_offline'; }
  }
  private makeOpenRequest(page: ExtensionUIPageState): ExtensionViewRecord {
    return { request: 'open', protocol: 1, request_id: util.generateRandomUUID(), extension: this.extensionId, contribution: page.ui,
      context: this.contextForStream(), state: page.state };
  }
  private managerEnvelopeValue(): string { return this.managerId; }
  private sendLifecycle(request: ExtensionViewRecord, kind: string, pageKey: string, eventId: string = ''): boolean {
    const client = this.client;
    if (!client || !this.ownerCurrent() || !client.connected) return false;
    const requestId = Wire.text(request, 'request_id');
    if (this.requestPages.size >= MAX_PENDING_REQUESTS) {
      const oldIds = Array.from(this.requestPages.keys());
      for (const oldId of oldIds) {
        const pending = this.requestPages.get(oldId);
        if (!pending) continue;
        if (pending.kind !== 'event' && pending.kind !== 'open') this.requestPages.delete(oldId);
        if (this.requestPages.size < MAX_PENDING_REQUESTS) break;
      }
    }
    if (this.requestPages.size >= MAX_PENDING_REQUESTS) { this.error = 'extension_ui_queue_limit'; return false; }
    this.requestPages.set(requestId, new ExtensionUIPendingRequest(kind, pageKey, eventId));
    try {
      const sent = client.repo().transport.send(streamRequestText(request, this.managerEnvelopeValue()));
      if (!sent) { this.requestPages.delete(requestId); return false; }
      return true;
    } catch (_) { this.requestPages.delete(requestId); return false; }
  }
  private makeLifecycle(kind: string, page: ExtensionUIPageState): ExtensionViewRecord | undefined {
    const snapshot = page.snapshot;
    if (!snapshot) return undefined;
    const request: ExtensionViewRecord = { request: kind, protocol: 1, request_id: util.generateRandomUUID(), session: snapshot.session, epoch: snapshot.epoch };
    if (kind === 'resume' || kind === 'snapshot') request['version'] = snapshot.version;
    return request;
  }
  private requestRecovery(page: ExtensionUIPageState, reason: string): void {
    if (!this.active || !this.ownerCurrent() || !this.client?.connected) { this.phase = this.active ? 'disconnected' : 'suspended'; return; }
    if (this.currentPage() !== page) return;
    this.phase = 'recovering'; this.error = reason;
    if (this.recoveryRead) return;
    this.recoveryRead = true;
    if (page.snapshot) {
      const request = this.makeLifecycle('snapshot', page);
      if (request && !this.sendLifecycle(request, 'snapshot', page.key)) this.recoveryRead = false;
    } else if (page.openRequest) {
      this.sendLifecycle(page.openRequest, 'open', page.key); this.openRead = true;
    }
  }
  retryRead(): void {
    const page = this.currentPage(); if (!page || !this.active || !this.ownerCurrent() || !this.client?.connected) return;
    this.recoveryRead = false;
    if (page.snapshot) {
      const request = this.makeLifecycle('resume', page);
      this.phase = 'recovering'; this.loading = true; this.error = '';
      if (request && !this.sendLifecycle(request, 'resume', page.key)) { this.phase = 'disconnected'; this.loading = false; }
    } else if (page.openRequest) { this.phase = 'opening'; this.loading = true; this.sendLifecycle(page.openRequest, 'open', page.key); }
    else this.refresh();
  }

  /** Called by the authenticated ClientModel event fan-out; all writes stay on its existing socket. */
  receive(raw: string): void {
    let frame: ExtensionViewRecord;
    try { frame = parseExtensionUIEvent(raw); } catch (_) { this.receiveHostInteractions(raw); return; }
    if (Wire.text(frame, 'type') === 'extension_ui') { this.receiveInvalidation(frame); return; }
    if (this.disposed || !this.ownerCurrent()) return;
    if (Wire.has(frame, 'manager') && Wire.text(frame, 'manager') !== this.managerId) return;
    const kind = Wire.text(frame, 'kind'), requestId = Wire.text(frame, 'request_id');
    if (kind === 'snapshot') { this.receiveSnapshot(frame, requestId); return; }
    if (kind === 'delta') { this.receiveDelta(frame); return; }
    if (kind === 'receipt') { this.receiveReceipt(frame, requestId); return; }
    if (kind === 'error') { this.receiveError(frame, requestId); return; }
    if (kind === 'suspended') { this.ackLifecycle(frame, requestId, 'suspend'); return; }
    if (kind === 'closed') {
      if (requestId) this.ackLifecycle(frame, requestId, 'close');
      else this.receiveRetirement(frame);
    }
  }
  private receiveRetirement(frame: ExtensionViewRecord): void {
    const session = Wire.text(frame, 'session');
    const page = this.stack.find((candidate: ExtensionUIPageState): boolean => !!session && candidate.snapshot?.session === session);
    if (!page) return;
    this.retirePageView(page);
  }
  invalidateAuthorization(): void {
    this.operationSequence++;
    for (const page of this.stack) {
      if (page.snapshot) {
        const request = this.makeLifecycle('close', page);
        if (request) this.sendLifecycle(request, 'close', page.key);
      } else this.detachPendingOpen(page);
      this.retirePageView(page);
    }
  }
  private retirePageView(page: ExtensionUIPageState): void {
    page.presentationGeneration++;
    if (this.lineResultPageKey === page.key) {
      this.lineResultJSON = ''; this.lineResultKey = ''; this.lineResultPageKey = ''; this.lineResultPath = ''; this.lineResultRevision++;
    }
    page.snapshot = undefined; page.openRequest = undefined; page.hasView = false; page.dirty = true; page.dirtyRevision++;
    for (const requestId of Array.from(this.requestPages.keys())) {
      const pending = this.requestPages.get(requestId);
      if (pending?.pageKey === page.key && pending.kind !== 'event') this.requestPages.delete(requestId);
    }
    for (const event of this.events.values()) if (event.pageKey === page.key && event.status === 'pending') {
      this.clearEventTimeout(event); this.markUncertain(event, 'extension_ui_action_uncertain');
    }
    if (this.currentPage() === page) {
      this.clearLegacyRefresh(); this.cancelConfirmation(); this.clearSecrets();
      this.openRead = false; this.recoveryRead = false; this.loading = false; this.busy = false;
      this.view = { sections: [] }; this.viewVersion = 0; this.revision++;
      this.phase = 'failed'; this.error = 'application_unavailable';
    }
  }
  private receiveHostInteractions(raw: string): void {
    if (raw.length > 1024 * 1024 || !this.events.size || !this.ownerCurrent() || !this.active || this.phase !== 'ready' || !this.client?.connected) return;
    let envelope: ExtensionViewRecord;
    try { envelope = Wire.parse(raw); } catch (_) { return; }
    if (Wire.has(envelope, 'manager') && Wire.text(envelope, 'manager') !== this.managerId) return;
    const type = Wire.text(envelope, 'type');
    if (type === 'interaction') this.receiveHostConfirmation(Wire.object(envelope, 'interaction'));
    else if (type === 'interactions') for (const value of Wire.rows(envelope, 'interactions')) this.receiveHostConfirmation(value);
  }
  private confirmedHostEvent(interaction: ExtensionViewRecord): ExtensionUIEvent | undefined {
    const page = this.currentPage(), snapshot = page?.snapshot;
    const expiry = Date.parse(Wire.text(interaction, 'expires_at'));
    if (!page || !snapshot || !page.stream || !this.ownerCurrent() || !this.active || this.phase !== 'ready' || !this.client?.connected ||
      Wire.text(interaction, 'manager') !== this.managerId || Wire.text(interaction, 'kind') !== 'permission' ||
      Wire.text(interaction, 'tool_name') !== 'extension.ui' || Wire.text(interaction, 'machine_id') !== '' ||
      Wire.text(interaction, 'session_id') !== snapshot.session || !Number.isFinite(expiry) || expiry <= Date.now() ||
      Wire.bool(interaction, 'secret') || Wire.bool(interaction, 'requires_input') || Wire.rows(interaction, 'fields').length ||
      Wire.rows(interaction, 'questions').length || Wire.has(interaction, 'elicitation')) return undefined;
    const matches: ExtensionUIEvent[] = [];
    for (const event of this.events.values()) {
      const input = event.interaction;
      if (!event.confirmed || event.status !== 'pending' || event.pageKey !== page.key ||
        Wire.text(input, 'session') !== snapshot.session || Wire.text(input, 'epoch') !== snapshot.epoch || Wire.number(input, 'version') !== snapshot.version ||
        !Wire.text(event.action, 'confirm') || Wire.text(interaction, 'summary') !== Wire.text(event.action, 'confirm') ||
        !equalActionValue(event.action, Wire.object(interaction, 'tool_input'))) continue;
      const saved = this.actionFor(Wire.text(input, 'key'), Wire.text(input, 'action'), Wire.text(input, 'event'));
      if (saved && equalActionValue(saved, event.action)) matches.push(event);
    }
    return matches.length === 1 ? matches[0] : undefined;
  }
  private receiveHostConfirmation(interaction: ExtensionViewRecord): void {
    const now = Date.now();
    this.answeredHostConfirmations.forEach((expiry: number, id: string): void => { if (expiry <= now) this.answeredHostConfirmations.delete(id); });
    const id = Wire.text(interaction, 'id'), event = this.confirmedHostEvent(interaction), client = this.client;
    if (!id || id.length > 256 || !event || !client || this.answeredHostConfirmations.has(id) || this.answeredHostConfirmations.size >= 64) return;
    this.answeredHostConfirmations.set(id, Date.parse(Wire.text(interaction, 'expires_at')));
    const admit = (): boolean => this.confirmedHostEvent(interaction) === event;
    // This completes only the exact page action the user already confirmed. The Hub rechecks its source and grants.
    const resolution: ExtensionViewRecord = { allow: true };
    client.repo().call('/api/v1/interactions/' + segment(id) + '/resolve', 'POST', resolution, util.generateRandomUUID(), admit)
      .catch((error: Error): void => { if (this.ownerCurrent() && this.active && this.events.get(Wire.text(event.interaction, 'event_id')) === event) this.error = this.safeError(error); });
  }
  private receiveSnapshot(frame: ExtensionViewRecord, requestId: string): void {
    const envelopeSnapshot = Wire.object(frame, 'snapshot'), request = requestId ? this.requestPages.get(requestId) : undefined;
    const snapshotSession = Wire.text(envelopeSnapshot, 'session');
    let page = request ? this.pageByKey(request.pageKey) : undefined;
    if (!page) page = this.stack.find((candidate: ExtensionUIPageState) => candidate.snapshot?.session === snapshotSession);
    if (!page && request?.kind === 'open') {
      this.requestPages.delete(requestId);
      const closed = reduceExtensionUIStream(undefined, 'snapshot', envelopeSnapshot, true);
      if (!closed.recover && closed.snapshot) this.closeLateOpen(closed.snapshot.session, closed.snapshot.epoch);
      return;
    }
    if (!page || this.disposed || !page.stream || !snapshotSession) return;
    if (request && !['open', 'resume', 'snapshot'].includes(request.kind)) return;
    if (!request && page.snapshot?.session !== snapshotSession) return;
    const previous = page.snapshot;
    if (previous && previous.session !== snapshotSession) return;
    const isCurrent = this.currentPage() === page;
    const allowEpochChange = request?.kind === 'resume' || request?.kind === 'snapshot' ||
      isCurrent && ['recovering', 'disconnected', 'suspended'].includes(this.phase);
    const incomingEpoch = Wire.text(envelopeSnapshot, 'epoch');
    if (page.retiredEpochs.has(incomingEpoch)) return;
    const reduced = reduceExtensionUIStream(previous, 'snapshot', envelopeSnapshot, allowEpochChange);
    if (reduced.recover || !reduced.snapshot) { this.requestRecovery(page, reduced.reason); return; }
    if (!this.supportedComponents(reduced.snapshot.view)) { this.requestRecovery(page, 'extension_ui_unsupported_component'); return; }
    if (previous && previous.epoch !== reduced.snapshot.epoch) {
      if (page.retiredEpochs.size >= 64) { this.error = 'extension_ui_epoch_limit'; return; }
      this.retireEpochEvents(page, previous.epoch);
      page.retiredEpochs.add(previous.epoch);
    }
    page.snapshot = reduced.snapshot; page.view = reduced.snapshot.view; page.state = Wire.text(reduced.snapshot.view, 'state', page.state);
    page.stream = true;
    if (requestId) this.requestPages.delete(requestId);
    if (isCurrent && this.active) {
      this.openRead = false; this.recoveryRead = false; this.phase = 'ready';
      this.error = this.uncertainEventId ? 'extension_ui_action_uncertain' : '';
      this.loading = false; this.publishPage(page, true);
    } else {
      const request = this.makeLifecycle('suspend', page);
      if (request) this.sendLifecycle(request, 'suspend', page.key);
      if (isCurrent && !this.active) { this.phase = 'suspended'; this.loading = false; }
    }
    this.applyReadyReceipts(page);
  }
  private closeLateOpen(session: string, epoch: string): void {
    const client = this.client;
    if (!client || !session || !epoch || !client.connected || !this.ownerCurrent()) return;
    const request: ExtensionViewRecord = { request: 'close', protocol: 1, request_id: util.generateRandomUUID(), session: session, epoch: epoch };
    try { client.repo().transport.send(streamRequestText(request, this.managerEnvelopeValue())); } catch (_) { }
  }
  private receiveDelta(frame: ExtensionViewRecord): void {
    const incoming = Wire.object(frame, 'delta'), session = Wire.text(incoming, 'session');
    const page = this.stack.find((candidate: ExtensionUIPageState) => candidate.snapshot?.session === session);
    if (!page || !page.snapshot || !page.stream) return;
    const reduced = reduceExtensionUIStream(page.snapshot, 'delta', incoming);
    if (reduced.recover || !reduced.snapshot) { this.requestRecovery(page, reduced.reason); return; }
    if (!this.supportedComponents(reduced.snapshot.view)) { this.requestRecovery(page, 'extension_ui_unsupported_component'); return; }
    page.snapshot = reduced.snapshot; page.view = reduced.snapshot.view; page.state = Wire.text(reduced.snapshot.view, 'state', page.state);
    if (this.currentPage() === page) { this.phase = 'ready'; this.error = ''; this.publishPage(page, true); }
    this.applyReadyReceipts(page);
  }
  private receiveReceipt(frame: ExtensionViewRecord, requestId: string): void {
    const request = this.requestPages.get(requestId), receipt = Wire.object(frame, 'receipt');
    if (!request || request.kind !== 'event' || !request.eventId) return;
    const pending = this.events.get(request.eventId);
    if (!pending) return;
    const interaction = pending.interaction;
    if (Wire.text(receipt, 'session') !== Wire.text(interaction, 'session') || Wire.text(receipt, 'epoch') !== Wire.text(interaction, 'epoch') ||
      Wire.text(receipt, 'event_id') !== request.eventId) return;
    const status = Wire.text(receipt, 'status'), version = Wire.number(receipt, 'version');
    if (!['accepted', 'ambiguous', 'rejected'].includes(status) || status !== 'rejected' && version !== Wire.number(interaction, 'version') + 1) {
      const page = this.pageByKey(pending.pageKey); if (page) this.requestRecovery(page, 'extension_ui_receipt_invalid'); return;
    }
    if (status !== 'accepted' && Wire.has(receipt, 'result')) {
      const page = this.pageByKey(pending.pageKey); if (page) this.requestRecovery(page, 'extension_ui_receipt_invalid'); return;
    }
    if (status === 'rejected' && version !== Wire.number(interaction, 'version')) {
      const page = this.pageByKey(pending.pageKey); if (page) this.requestRecovery(page, 'extension_ui_receipt_invalid'); return;
    }
    this.requestPages.delete(requestId); pending.status = status;
    this.clearEventTimeout(pending);
    const requestIds = Array.from(this.requestPages.keys());
    for (const oldRequestId of requestIds) {
      const oldPending = this.requestPages.get(oldRequestId);
      if (!oldPending) continue;
      if (oldPending.kind === 'event' && oldPending.eventId === request.eventId) this.requestPages.delete(oldRequestId);
    }
    const page = this.pageByKey(pending.pageKey);
    if (page && page.snapshot?.session !== Wire.text(interaction, 'session')) {
      // A late receipt can settle the old write, but cannot revive its page or execute navigation on a new one.
      if (status === 'ambiguous') {
        pending.status = 'uncertain'; this.uncertainEvents.set(request.eventId, pending);
        this.uncertainEventId = request.eventId;
      } else {
        this.events.delete(request.eventId); this.uncertainEvents.delete(request.eventId);
        if (this.uncertainEventId === request.eventId) this.uncertainEventId = '';
        if (this.currentPage() === page) {
          if (status === 'accepted') this.notice = tr('extension_ui_action_completed');
          else this.error = 'extension_ui_action_rejected';
        }
      }
      return;
    }
    const presentationKey = JSON.stringify([Wire.text(interaction, 'session'), Wire.text(interaction, 'epoch'), request.eventId]);
    this.presentations.set(presentationKey, receipt);
    this.applyReadyReceipts(this.pageByKey(pending.pageKey));
  }
  private receiveError(frame: ExtensionViewRecord, requestId: string): void {
    const pending = this.requestPages.get(requestId), failure = Wire.object(frame, 'error');
    if (!pending) return;
    const code = Wire.text(failure, 'code', 'extension_ui_failed');
    if (pending) this.requestPages.delete(requestId);
    const opening = pending.kind === 'open' ? this.pageByKey(pending.pageKey) : undefined;
    if (opening) { opening.openRequest = undefined; this.openRead = false; }
    if (opening && this.currentPage() === opening && !opening.snapshot && !opening.preferLegacy &&
      this.events.size === 0 && this.uncertainEvents.size === 0 && ['negotiation', 'rejected'].includes(code)) {
      // A legacy render page has no component mount. Only an initial read may change transport.
      opening.preferLegacy = true; opening.stream = false; opening.openRequest = undefined; this.openRead = false;
      this.renderPage(opening, opening.hasView).catch(() => undefined); return;
    }
    if (pending && pending.kind === 'event') {
      const event = this.events.get(pending.eventId);
      if (event && event.requestId !== requestId) return;
      if (event) this.markUncertain(event, code);
      const page = pending ? this.pageByKey(pending.pageKey) : undefined;
      if (page) this.requestRecovery(page, code);
      return;
    }
    if (pending && ['open', 'snapshot', 'resume'].includes(pending.kind) && code === 'expired') {
      const page = this.pageByKey(pending.pageKey); if (page) this.reopenExpired(page); return;
    }
    if (pending?.kind === 'snapshot' || pending?.kind === 'resume') this.recoveryRead = false;
    this.error = code; this.phase = code === 'inactive' ? 'recovering' : 'failed'; this.loading = false;
  }
  private ackLifecycle(frame: ExtensionViewRecord, requestId: string, expected: string): void {
    const pending = this.requestPages.get(requestId);
    if (!pending || pending.kind !== expected || Wire.text(frame, 'session') !== this.pageByKey(pending.pageKey)?.snapshot?.session) return;
    this.requestPages.delete(requestId);
    if (expected === 'suspend' && !this.active && this.currentPage()?.key === pending.pageKey) this.phase = 'suspended';
  }
  private applyReadyReceipts(page: ExtensionUIPageState | undefined): void {
    if (!page?.snapshot || !this.active || this.currentPage() !== page) return;
    for (const identity of Array.from(this.presentations.keys())) {
      const receipt = this.presentations.get(identity);
      if (!receipt) continue;
      const session = Wire.text(receipt, 'session'), epoch = Wire.text(receipt, 'epoch'), version = Wire.number(receipt, 'version');
      if (session !== page.snapshot.session || epoch !== page.snapshot.epoch || page.snapshot.version < version) continue;
      const alreadyPresented = this.presented.has(identity);
      if (!alreadyPresented) this.rememberPresented(identity);
      this.presentations.delete(identity);
      const event = this.findEventByIdentity(session, epoch, Wire.text(receipt, 'event_id'));
      const result = Wire.object(receipt, 'result');
      const eventId = Wire.text(receipt, 'event_id');
      if (Wire.text(receipt, 'status') === 'accepted') {
        this.notice = tr('extension_ui_action_completed');
        if (!alreadyPresented && Object.keys(result).length && event) this.applyActionResult(page, result, event);
      }
      const status = Wire.text(receipt, 'status');
      if (status === 'ambiguous') {
        this.error = 'extension_ui_action_ambiguous';
        if (event) { event.status = 'uncertain'; this.uncertainEvents.set(eventId, event); }
        this.uncertainEventId = eventId;
      } else if (status === 'rejected') this.error = 'extension_ui_action_rejected';
      this.busy = false; this.events.delete(eventId);
      if (status === 'accepted' || status === 'rejected') {
        this.uncertainEvents.delete(eventId);
        if (this.uncertainEventId === eventId) this.uncertainEventId = '';
      }
    }
    if (!this.uncertainEventId && this.phase === 'ready' && this.error === 'extension_ui_action_uncertain') this.error = '';
  }
  private markUncertain(event: ExtensionUIEvent, error: string): void {
    this.clearEventTimeout(event);
    const eventId = Wire.text(event.interaction, 'event_id');
    event.status = 'uncertain'; this.uncertainEvents.set(eventId, event); this.uncertainEventId = eventId;
    this.busy = false; this.error = error || 'extension_ui_action_uncertain';
  }
  private clearEventTimeout(event: ExtensionUIEvent): void {
    if (event.timeout !== -1) clearTimeout(event.timeout);
    event.timeout = -1;
  }
  private clearAllEventTimeouts(): void {
    for (const event of this.events.values()) this.clearEventTimeout(event);
    for (const event of this.uncertainEvents.values()) this.clearEventTimeout(event);
  }
  private rememberPresented(identity: string): void {
    this.presented.add(identity);
    if (this.presented.size > MAX_PRESENTED_RECEIPTS) {
      for (const oldIdentity of this.presented) { this.presented.delete(oldIdentity); break; }
    }
  }
  private retireEpochEvents(page: ExtensionUIPageState, epoch: string): void {
    for (const eventId of Array.from(this.events.keys())) {
      const event = this.events.get(eventId);
      if (!event) continue;
      if (event.pageKey !== page.key || Wire.text(event.interaction, 'epoch') !== epoch) continue;
      this.clearEventTimeout(event);
      this.events.delete(eventId); this.uncertainEvents.delete(eventId);
      const requestIds = Array.from(this.requestPages.keys());
      for (const requestId of requestIds) {
        const pending = this.requestPages.get(requestId);
        if (pending && pending.kind === 'event' && pending.eventId === eventId) this.requestPages.delete(requestId);
      }
    }
    for (const eventId of Array.from(this.uncertainEvents.keys())) {
      const event = this.uncertainEvents.get(eventId);
      if (!event || event.pageKey !== page.key || Wire.text(event.interaction, 'epoch') !== epoch) continue;
      this.clearEventTimeout(event); this.uncertainEvents.delete(eventId); this.events.delete(eventId);
      const requestIds = Array.from(this.requestPages.keys());
      for (const requestId of requestIds) {
        const pending = this.requestPages.get(requestId);
        if (pending && pending.kind === 'event' && pending.eventId === eventId) this.requestPages.delete(requestId);
      }
      if (this.uncertainEventId === eventId) this.uncertainEventId = '';
    }
    for (const identity of Array.from(this.presentations.keys())) {
      const receipt = this.presentations.get(identity);
      if (!receipt) continue;
      if (Wire.text(receipt, 'session') === page.snapshot?.session && Wire.text(receipt, 'epoch') === epoch) this.presentations.delete(identity);
    }
    if (this.currentPage() === page) { this.busy = false; this.uncertainEventId = ''; }
  }
  private findEventByIdentity(session: string, epoch: string, eventId: string): ExtensionUIEvent | undefined {
    const event = this.events.get(eventId);
    if (!event || Wire.text(event.interaction, 'session') !== session || Wire.text(event.interaction, 'epoch') !== epoch) return undefined;
    return event;
  }
  private applyActionResult(page: ExtensionUIPageState, result: ExtensionViewRecord, event: ExtensionUIEvent): void {
    const toast = Wire.text(result, 'toast'); if (toast) this.notice = toast;
    const address = Wire.text(result, 'open_url'); if (address && secureWebURL(address)) this.callbackLink(address);
    const push = Wire.object(result, 'push');
    if (Object.keys(push).length) this.pushPage(Wire.text(push, 'ui', page.ui), Wire.text(push, 'state'), Wire.text(push, 'title'));
    else if (Wire.bool(result, 'pop')) this.popPage(true);
    this.storeLineResult(page, Wire.text(event.interaction, 'key'), Wire.text(event.values, 'path'), Wire.text(event.values, 'side'), Wire.object(result, 'lines'), true);
  }
  private storeLineResult(page: ExtensionUIPageState, key: string, expectedPath: string, expectedSide: string, lines: ExtensionViewRecord, streamed: boolean = false): void {
    if (this.currentPage() !== page || !Object.keys(lines).length) return;
    const path = Wire.text(lines, 'path'), side = Wire.text(lines, 'side', expectedSide || 'new'), start = Wire.number(lines, 'start');
    if (!Array.isArray(lines['lines'])) return;
    const values = lines['lines'] as Object[];
    const valid = values.length <= 20000 && values.every((line: Object) => typeof line === 'string' && Array.from(line as string).length <= 20000);
    if (!key || path !== expectedPath || expectedSide && side !== expectedSide || !Number.isSafeInteger(start) || start <= 0 || !valid || util.TextEncoder.create('utf-8').encodeInto(JSON.stringify(lines)).byteLength > (streamed ? 64 * 1024 : 4 * 1024 * 1024)) return;
    const saved = cloneRecord(lines); if (expectedSide) saved['side'] = side;
    this.lineResultJSON = JSON.stringify(saved); this.lineResultKey = key; this.lineResultPageKey = page.key; this.lineResultPath = path; this.lineResultRevision++;
  }
  lineResultFor(key: string): ExtensionViewRecord {
    if (key !== this.lineResultKey || !this.lineResultJSON || this.currentPage()?.key !== this.lineResultPageKey) return {};
    const sections = Array.isArray(this.view['sections']) ? this.view['sections'] as Object[] : [];
    for (const sectionValue of sections) {
      const items = Array.isArray(Wire.record(sectionValue)['items']) ? Wire.record(sectionValue)['items'] as Object[] : [];
      for (const itemValue of items) {
        const item = Wire.record(itemValue);
        if (Wire.text(item, 'key') === key && ['code', 'diff'].includes(Wire.text(item, 'type')) && Wire.text(item, 'path') === this.lineResultPath) return Wire.parse(this.lineResultJSON);
      }
    }
    return {};
  }
  private supportedComponents(view: ExtensionViewRecord): boolean {
    const sections = Array.isArray(view['sections']) ? view['sections'] as Object[] : [];
    for (const sectionValue of sections) {
      const items = Array.isArray(Wire.record(sectionValue)['items']) ? Wire.record(sectionValue)['items'] as Object[] : [];
      for (const itemValue of items) if (Wire.text(Wire.record(itemValue), 'type') === 'html') return false;
    }
    return true;
  }
  private reopenExpired(page: ExtensionUIPageState): void {
    page.snapshot = undefined; page.openRequest = undefined; page.state = Wire.text(page.view, 'state', page.state);
    this.clearAllEventTimeouts(); this.events.clear(); this.uncertainEvents.clear(); this.presentations.clear(); this.pendingConfirmation = undefined;
    this.busy = false; this.uncertainEventId = ''; this.error = '';
    this.requestPages.clear(); this.openRead = false; this.recoveryRead = false;
    this.confirmationToken = ''; this.phase = 'cold'; this.openCurrent().catch(() => {});
  }
  private detachPendingOpen(page: ExtensionUIPageState | undefined): void {
    const client = this.client, requestId = page?.openRequest ? Wire.text(page.openRequest, 'request_id') : '';
    if (!client || !requestId || page?.snapshot || this.detachedOpens.has(requestId) || this.detachedOpens.size >= 32) return;
    const detached = new ExtensionUIDetachedOpen(client, requestId, this.generation, this.scope, this.managerId);
    this.detachedOpens.set(requestId, detached);
    detached.off = client.observeEvents((raw: string): void => this.receiveDetachedOpen(raw, requestId));
    detached.timer = setTimeout(() => this.clearDetachedOpen(requestId), STREAM_WAIT_MS);
  }
  private receiveDetachedOpen(raw: string, requestId: string): void {
    const detached = this.detachedOpens.get(requestId); if (!detached) return;
    let frame: ExtensionViewRecord;
    try { frame = parseExtensionUIFrame(raw); } catch (_) { return; }
    if (Wire.text(frame, 'request_id') !== requestId) return;
    if (Wire.has(frame, 'manager') && Wire.text(frame, 'manager') !== detached.managerId) return;
    if (Wire.text(frame, 'kind') === 'snapshot') {
      const snapshot = Wire.object(frame, 'snapshot'), session = Wire.text(snapshot, 'session'), epoch = Wire.text(snapshot, 'epoch');
      if (session && epoch && detached.client.connected && detached.client.profile.manager === detached.managerId &&
        detached.client.owns(detached.generation, detached.scope)) {
        const request: ExtensionViewRecord = { request: 'close', protocol: 1, request_id: util.generateRandomUUID(), session: session, epoch: epoch };
        try { detached.client.repo().transport.send(streamRequestText(request, detached.managerId)); } catch (_) { }
      }
      this.clearDetachedOpen(requestId);
    } else if (Wire.text(frame, 'kind') === 'error') this.clearDetachedOpen(requestId);
  }
  private clearDetachedOpen(requestId: string): void {
    const detached = this.detachedOpens.get(requestId); if (!detached) return;
    if (detached.timer !== -1) clearTimeout(detached.timer);
    detached.off?.(); this.detachedOpens.delete(requestId);
  }

  private setView(value: ExtensionViewRecord): void {
    const page = this.currentPage(); if (!page) return;
    page.hasView = true; page.view = cloneRecord(value); page.state = Wire.text(value, 'state', page.state); this.publishPage(page); this.phase = 'ready';
  }
  private safeError(error: Error): string {
    const code = error && error.message ? error.message : 'extension_ui_failed';
    return /^[a-z0-9_]{1,64}$/.test(code) ? code : 'extension_ui_failed';
  }
  open(): void { this.openCurrent().catch((error: Error) => this.error = this.safeError(error)); }
  refresh(): void {
    const page = this.currentPage(); if (!page || !this.ownerCurrent() || !this.active) return;
    if (this.busy && !page.stream) { page.dirty = true; page.dirtyRevision++; return; }
    if (page.stream && page.snapshot) {
      const request = this.makeLifecycle('snapshot', page);
      this.phase = 'recovering'; this.loading = true; this.error = '';
      if (request && !this.sendLifecycle(request, 'snapshot', page.key)) { this.phase = 'disconnected'; this.loading = false; }
    } else if (!this.compact && !page.preferLegacy && this.client?.features.includes(STREAM_FEATURE)) {
      // A restored route can open before hello advertises stream support. Retire that older HTTP read.
      this.operationSequence++; this.clearLegacyRefresh(); this.open();
    } else this.renderPage(page, true).catch((error: Error) => this.error = this.safeError(error));
  }
  setVisible(value: boolean): void {
    if (this.disposed || this.active === value) return;
    this.active = value;
    if (!value) {
      this.clearLegacyRefresh(); this.clearSecrets(); this.cancelConfirmation();
      for (const page of this.stack) {
        if (page.stream && page.snapshot && this.client?.connected) {
          const request = this.makeLifecycle('suspend', page);
          if (request) this.sendLifecycle(request, 'suspend', page.key);
        }
      }
      this.phase = 'suspended';
      return;
    }
    if (!this.ownerCurrent()) { this.error = 'request_updated'; return; }
    const page = this.currentPage(); if (!page) return;
    if (page.stream && page.snapshot) {
      const request = this.makeLifecycle('resume', page);
      this.phase = 'recovering'; this.loading = true; this.error = '';
      if (request && !this.sendLifecycle(request, 'resume', page.key)) { this.phase = 'disconnected'; this.loading = false; }
      this.applyReadyReceipts(page);
    } else if (page.hasView && !page.dirty && (!page.refreshDeadline || page.refreshDeadline > Date.now())) { this.phase = 'ready'; this.scheduleLegacyRefresh(page); }
    else if (page.hasView) this.renderPage(page, true).catch(() => undefined);
    else this.open();
  }
  connectionChanged(): void {
    if (this.disposed || !this.client) return;
    const client = this.client;
    if (!client.connected) this.clearLegacyRefresh();
    if (client.profile.id !== this.profileId || client.profile.manager !== this.managerId || client.scope() !== this.scope) {
      this.cancelConfirmation(); this.clearSecrets(); this.phase = 'disconnected'; this.error = 'request_updated'; return;
    }
    if (client.connected && client.connectionGeneration() !== this.generation) {
      // Resume the saved server session, while retiring callbacks and confirmations from the old transport.
      this.generation = client.connectionGeneration(); this.operationSequence++; this.revision++;
      this.cancelConfirmation(); this.clearSecrets(); this.requestPages.clear(); this.recoveryRead = false;
      for (const event of this.events.values()) if (event.status === 'pending') this.markUncertain(event, 'extension_ui_action_uncertain');
      this.loading = false;
    }
    if (!this.ownerCurrent()) { this.cancelConfirmation(); this.clearSecrets(); this.phase = 'disconnected'; this.error = 'request_updated'; return; }
    const page = this.currentPage(); if (!page || !page.stream) { if (this.active && this.client.connected) this.refresh(); return; }
    if (!this.client.connected) {
      this.phase = 'disconnected';
      for (const event of this.events.values()) if (event.status === 'pending') this.markUncertain(event, 'extension_ui_action_uncertain');
      return;
    }
    if (!this.active) return;
    if (page.snapshot) {
      const request = this.makeLifecycle('resume', page);
      this.phase = 'recovering'; this.loading = true; this.error = '';
      if (request && !this.sendLifecycle(request, 'resume', page.key)) { this.phase = 'disconnected'; this.loading = false; }
    } else if (page.openRequest) this.sendLifecycle(page.openRequest, 'open', page.key);
  }

  private isSecretField(key: string): boolean {
    const sections = Wire.rows(this.view, 'sections');
    for (let sectionIndex = 0; sectionIndex < sections.length; sectionIndex++) {
      const items = Wire.rows(sections[sectionIndex], 'items');
      for (let itemIndex = 0; itemIndex < items.length; itemIndex++) {
        const item = items[itemIndex], fieldKey = Wire.text(item, 'key', '@item:' + String(sectionIndex) + ':' + String(itemIndex));
        if (fieldKey === key && Wire.text(item, 'type') === 'field' && Wire.text(item, 'kind') === 'secret') return true;
      }
    }
    return false;
  }
  private initialFieldValue(key: string): string {
    const sections = Wire.rows(this.view, 'sections');
    for (let sectionIndex = 0; sectionIndex < sections.length; sectionIndex++) {
      const items = Wire.rows(sections[sectionIndex], 'items');
      for (let itemIndex = 0; itemIndex < items.length; itemIndex++) {
        const item = items[itemIndex], fieldKey = Wire.text(item, 'key', '@item:' + String(sectionIndex) + ':' + String(itemIndex));
        if (fieldKey === key) return Array.isArray(item['value']) ? JSON.stringify(item['value']) : Wire.text(item, 'value');
      }
    }
    return '';
  }
  fieldValue(key: string, fallback: string = ''): string {
    if (this.drafts.has(key)) return this.drafts.get(key) || '';
    if (this.isSecretField(key) && this.secrets.has(key)) return this.secrets.get(key) || '';
    return fallback;
  }
  setFieldValue(key: string, value: string, secret: boolean = false): void {
    if (!key) return;
    if (secret) { this.drafts.delete(key); this.secrets.set(key, value); } else { this.secrets.delete(key); this.drafts.set(key, value); }
    this.revision++;
  }
  private clearSecrets(): void { this.secrets.clear(); }
  clearFields(): void { this.drafts.clear(); this.secrets.clear(); this.revision++; }
  toggleValue(key: string, choice: string): string {
    const current = this.drafts.has(key) ? this.drafts.get(key) || '[]' : this.initialFieldValue(key) || '[]';
    let values: string[] = [];
    try { const parsed = JSON.parse(current) as Object; if (Array.isArray(parsed)) values = (parsed as Object[]).filter((value: Object) => typeof value === 'string').map((value: Object) => value as string); } catch (_) { }
    values = values.includes(choice) ? values.filter((value: string) => value !== choice) : values.concat([choice]);
    this.drafts.set(key, JSON.stringify(values)); this.revision++; return this.drafts.get(key) || '[]';
  }
  fieldValues(includeSecrets: boolean = false): Record<string, Object> {
    const values = extensionUIFieldValues(this.view, this.drafts);
    if (includeSecrets) {
      const sections = Array.isArray(this.view['sections']) ? this.view['sections'] as Object[] : [];
      for (let sectionIndex = 0; sectionIndex < sections.length; sectionIndex++) {
        const items = Array.isArray(Wire.record(sections[sectionIndex])['items']) ? Wire.record(sections[sectionIndex])['items'] as Object[] : [];
        for (let itemIndex = 0; itemIndex < items.length; itemIndex++) {
          const itemValue = items[itemIndex];
          const item = Wire.record(itemValue), key = Wire.text(item, 'key'), name = Wire.text(item, 'name');
          const fieldKey = key || '@item:' + String(sectionIndex) + ':' + String(itemIndex);
          if (Wire.text(item, 'type') === 'field' && Wire.text(item, 'kind') === 'secret' && name && this.secrets.has(fieldKey)) values[name] = this.secrets.get(fieldKey) || '';
        }
      }
    }
    return values;
  }
  private actionFor(key: string, actionId: string, event: string): ExtensionViewRecord | undefined {
    const page = this.currentPage(); if (!page) return undefined;
    if (page.snapshot) return actionForStreamEvent(page.snapshot, key, actionId, event);
    const slots = extensionUIActionSlotsForView(page.view, '@root');
    for (const slot of slots) if (slot.key === key && slot.event === event && Wire.text(slot.action, 'id') === actionId) return cloneRecord(slot.action);
    return undefined;
  }
  private pageActionValues(action: ExtensionViewRecord, ownValues: ExtensionViewRecord, stream: boolean): Record<string, Object> {
    const values: Record<string, Object> = {};
    if (Wire.bool(action, 'submit')) {
      const fields = this.fieldValues(!stream);
      for (const key of Object.keys(fields)) values[key] = fields[key];
    }
    for (const key of Object.keys(ownValues)) {
      if (key === '__role' || key === 'role') continue;
      const value = ownValues[key];
      if (typeof value === 'string' || typeof value === 'boolean' || typeof value === 'number') values[key] = String(value);
    }
    const actionValues = Wire.object(action, 'values');
    for (const key of Object.keys(actionValues)) values[key] = String(actionValues[key]);
    return values;
  }
  async performAction(key: string, actionId: string, event: string = 'press', ownValues: ExtensionViewRecord = {}, title: string = ''): Promise<void> {
    if (this.busy || this.uncertainEventId || this.pendingConfirmation || this.phase !== 'ready') return;
    if (!this.ownerCurrent() || !this.active || !this.client?.connected) { this.error = 'request_updated'; return; }
    const page = this.currentPage(), saved = this.actionFor(key, actionId, event);
    if (!page || !saved || Wire.text(saved, 'id') !== actionId) { this.error = 'extension_ui_action_stale'; return; }
    const role = Wire.text(ownValues, '__role', Wire.text(ownValues, 'role'));
    if (role === 'destructive' && !Wire.text(saved, 'confirm')) { this.error = 'extension_ui_confirmation_required'; return; }
    const values = this.pageActionValues(saved, ownValues, page.stream);
    if (page.stream && Wire.bool(saved, 'submit') && this.hasSecretDraft()) { this.clearSecrets(); this.error = 'extension_ui_secret_stream'; return; }
    const capture = this.freezeAction(page, key, actionId, event, values, saved, title);
    if (Wire.text(saved, 'confirm')) {
      const accepted = await new Promise<boolean>((resolve) => { this.confirmationResult = resolve; this.showConfirmation(capture, Wire.text(saved, 'confirm')); });
      if (!accepted) throw new Error(this.error || 'cancelled');
      return;
    }
    await this.executeCaptured(capture);
  }
  private hasSecretDraft(): boolean { for (const value of this.secrets.values()) if (value) return true; return false; }
  private freezeAction(page: ExtensionUIPageState, key: string, actionId: string, event: string, values: Record<string, Object>, saved: ExtensionViewRecord, title: string): ExtensionUIConfirmation {
    const capture = new ExtensionUIConfirmation();
    capture.token = util.generateRandomUUID(); capture.pageKey = page.key; capture.generation = this.generation; capture.scope = this.scope;
    capture.revision = this.revision; capture.version = page.snapshot?.version || 0; capture.ui = page.ui;
    capture.actionJSON = JSON.stringify(saved); capture.contextJSON = JSON.stringify(this.baseContext);
    capture.key = key; capture.actionId = actionId; capture.event = event; capture.values = cloneRecord(values);
    capture.title = title || this.pageTitle || this.extensionId; capture.impact = this.extensionId + ' / ' + page.ui + '\n' + actionId;
    return capture;
  }
  private showConfirmation(capture: ExtensionUIConfirmation, textValue: string): void {
    this.pendingConfirmation = capture; this.confirmationToken = capture.token;
    this.confirmationTitle = tr('extension_ui_confirm_action');
    this.confirmationText = textValue + '\n' + capture.title + '\n' + capture.impact;
  }
  confirmAction(token: string, allow: boolean): void {
    const capture = this.pendingConfirmation;
    if (!capture || capture.token !== token) return;
    const complete = this.confirmationResult; this.confirmationResult = undefined;
    this.cancelConfirmation();
    if (!allow) { complete?.(false); return; }
    if (!this.captureCurrent(capture)) { this.error = 'request_updated'; complete?.(false); return; }
    capture.confirmed = true;
    this.executeCaptured(capture).then(() => complete?.(true)).catch((error: Error) => {
      this.error = this.safeError(error); complete?.(false);
    });
  }
  private cancelConfirmation(): void {
    const complete = this.confirmationResult; this.confirmationResult = undefined;
    this.pendingConfirmation = undefined; this.confirmationToken = ''; this.confirmationText = ''; this.confirmationTitle = '';
    complete?.(false);
  }
  private captureCurrent(capture: ExtensionUIConfirmation): boolean {
    if (!this.ownerCurrent() || !this.active || capture.generation !== this.generation || capture.scope !== this.scope || capture.revision !== this.revision ||
      JSON.stringify(this.baseContext) !== capture.contextJSON || this.currentPage()?.key !== capture.pageKey || this.currentPage()?.ui !== capture.ui ||
      (this.currentPage()?.snapshot?.version || 0) !== capture.version) return false;
    const saved = this.actionFor(capture.key, capture.actionId, capture.event);
    return !!saved && JSON.stringify(saved) === capture.actionJSON;
  }
  private async executeCaptured(capture: ExtensionUIConfirmation): Promise<void> {
    if (!this.captureCurrent(capture) || !this.client) { this.error = 'request_updated'; return; }
    const page = this.currentPage(); if (!page) return;
    const action = this.actionFor(capture.key, capture.actionId, capture.event); if (!action) { this.error = 'extension_ui_action_stale'; return; }
    const push = actionPush(action);
    const submit = Wire.bool(action, 'submit');
    if (Object.keys(push).length) { if (submit) this.clearSecrets(); this.pushPage(Wire.text(push, 'ui', page.ui), Wire.text(push, 'state'), Wire.text(push, 'title')); return; }
    if (page.stream) { if (submit) this.clearSecrets(); this.sendStreamEvent(page, capture, action); return; }
    await this.sendLegacyAction(page, capture, action);
  }
  private async sendLegacyAction(page: ExtensionUIPageState, capture: ExtensionUIConfirmation, action: ExtensionViewRecord): Promise<void> {
    const client = this.client; if (!client || !this.captureCurrent(capture)) { this.error = 'request_updated'; return; }
    const body: ExtensionViewRecord = { context: this.contextForLegacy(), action: Wire.text(action, 'id'), values: capture.values };
    if (page.state) body['state'] = page.state;
    const requestRevision = this.revision; this.busy = true; this.error = ''; this.notice = '';
    if (Wire.bool(action, 'submit')) this.clearSecrets();
    const admit = (): boolean => this.captureCurrent(capture) && this.revision === requestRevision && this.currentPage() === page;
    try {
      let result: ExtensionViewRecord;
      try { result = await client.repo().call(this.apiPath(page.ui), 'POST', body, util.generateRandomUUID(), admit); }
      catch (error) { if (admit()) this.error = this.safeError(error as Error); throw error as Error; }
      if (!this.ownerCurrent() || this.currentPage() !== page) return;
      if (!admit()) {
        this.notice = tr('extension_ui_action_completed');
        await this.renderPage(page, true);
        return;
      }
      this.notice = tr('extension_ui_action_completed');
      const submit = Wire.bool(action, 'submit');
      if (submit) this.clearFields();
      if (Object.keys(saveViewFromResponse(result)).length) {
        page.view = saveViewFromResponse(result); page.state = Wire.text(page.view, 'state', page.state); this.publishPage(page, !submit);
      }
      this.handleLegacyResult(page, result, capture.key, capture.values);
      if (!Wire.has(result, 'view') && !Wire.has(result, 'items') && !Wire.has(result, 'lines')) {
        await this.renderPage(page, !submit);
      }
    } finally { this.busy = false; this.scheduleLegacyRefresh(page); }
  }
  private handleLegacyResult(page: ExtensionUIPageState, result: ExtensionViewRecord, actionKey: string, values: ExtensionViewRecord): void {
    this.storeLineResult(page, actionKey, Wire.text(values, 'path'), Wire.text(values, 'side'), Wire.object(result, 'lines'));
    const toast = Wire.text(result, 'toast'); if (toast) this.notice = toast;
    const address = Wire.text(result, 'open_url'); if (address && secureWebURL(address)) this.callbackLink(address);
    const push = Wire.object(result, 'push');
    if (Object.keys(push).length) this.pushPage(Wire.text(push, 'ui', page.ui), Wire.text(push, 'state'), Wire.text(push, 'title'));
    else if (Wire.bool(result, 'pop')) this.popPage(true);
    const replacements = Array.isArray(result['items']) ? result['items'] as Object[] : [];
    if (replacements.length) {
      const itemsByKey = new Map<string, ExtensionViewRecord>();
      for (const value of replacements) { const item = Wire.record(value); if (Wire.text(item, 'key')) itemsByKey.set(Wire.text(item, 'key'), item); }
      const next = cloneRecord(page.view), sections = Array.isArray(next['sections']) ? next['sections'] as Object[] : [];
      for (let si = 0; si < sections.length; si++) {
        const section = cloneRecord(Wire.record(sections[si])), items = Array.isArray(section['items']) ? section['items'] as Object[] : [];
        for (let ii = 0; ii < items.length; ii++) {
          const key = Wire.text(Wire.record(items[ii]), 'key'); if (itemsByKey.has(key)) items[ii] = itemsByKey.get(key) as ExtensionViewRecord;
        }
        section['items'] = items; sections[si] = section;
      }
      next['sections'] = sections; page.view = next; this.publishPage(page, true);
    }
  }
  private sendStreamEvent(page: ExtensionUIPageState, capture: ExtensionUIConfirmation, saved: ExtensionViewRecord): void {
    const snapshot = page.snapshot, client = this.client;
    if (!snapshot || !client || !this.captureCurrent(capture) || this.phase !== 'ready') { this.error = 'request_updated'; return; }
    if (this.events.size >= MAX_PENDING_EVENTS) { this.error = 'extension_ui_queue_limit'; return; }
    const value: Object = capture.values;
    if (!validStreamInteractionValue(value)) { this.error = 'extension_ui_value_invalid'; return; }
    const eventId = util.generateRandomUUID(), requestId = util.generateRandomUUID();
    const interaction: ExtensionViewRecord = { protocol: 1, session: snapshot.session, epoch: snapshot.epoch, version: snapshot.version,
      event_id: eventId, key: capture.key, action: Wire.text(saved, 'id'), event: capture.event, value: cloneRecord(capture.values) };
    const event = new ExtensionUIEvent(interaction, cloneRecord(saved), cloneRecord(capture.values), page.key, requestId);
    event.confirmed = capture.confirmed;
    this.events.set(eventId, event); this.busy = true; this.error = ''; this.notice = '';
    const request: ExtensionViewRecord = { request: 'event', protocol: 1, request_id: requestId, interaction: interaction };
    if (!this.sendLifecycle(request, 'event', page.key, eventId)) {
      this.markUncertain(event, 'extension_ui_send_uncertain'); this.requestRecovery(page, 'extension_ui_send_uncertain');
    } else this.scheduleEventTimeout(event);
    if (Wire.bool(saved, 'submit')) this.clearFields();
  }
  private scheduleEventTimeout(event: ExtensionUIEvent): void {
    const eventId = Wire.text(event.interaction, 'event_id'), page = this.pageByKey(event.pageKey);
    this.clearEventTimeout(event);
    event.timeout = setTimeout(() => {
      event.timeout = -1;
      if (event.status === 'pending' && this.events.has(eventId)) {
        this.markUncertain(event, 'extension_ui_receipt_timeout');
        if (page) this.requestRecovery(page, 'extension_ui_receipt_timeout');
      }
    }, STREAM_WAIT_MS);
  }
  retryUncertainEvent(eventId: string): void {
    const event = this.uncertainEvents.get(eventId), page = event ? this.pageByKey(event.pageKey) : undefined;
    if (!event || !page || !page.snapshot || this.currentPage() !== page || this.uncertainEventId !== eventId || this.busy ||
      !this.active || this.phase !== 'ready' || !this.ownerCurrent() || !this.client?.connected) return;
    const interaction = cloneRecord(event.interaction), requestId = util.generateRandomUUID();
    const request: ExtensionViewRecord = { request: 'event', protocol: 1, request_id: requestId, interaction: interaction };
    event.requestId = requestId; event.status = 'pending'; this.events.set(eventId, event); this.busy = true;
    if (!this.sendLifecycle(request, 'event', page.key, eventId)) {
      this.markUncertain(event, 'extension_ui_send_uncertain');
    } else this.scheduleEventTimeout(event);
  }

  canGoBack(): boolean { return this.stack.length > 1; }
  lineRevision(): number { return this.lineResultRevision; }
  async imageBytes(path: string): Promise<ArrayBuffer> {
    const client = this.client, prefix = '/api/v1/extensions/' + segment(this.extensionId) + '/-/image/';
    if (!client || !this.ownerCurrent() || !path.startsWith(prefix) || path.length > 2048 || /[?#\\\r\n]/.test(path)) throw new Error('extension_ui_image_invalid');
    const response = await client.repo().transport.requestBytes(path, 'GET');
    if (!this.ownerCurrent()) throw new Error('request_updated');
    const rasterTypes = ['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/heic', 'image/heif'];
    if (response.data.byteLength === 0 || response.data.byteLength > 8 * 1024 * 1024 || !rasterTypes.includes(response.contentType.split(';')[0].trim().toLowerCase())) {
      throw new Error('extension_ui_image_invalid');
    }
    return response.data;
  }

  private pushPage(ui: string, state: string, title: string): void {
    if (!ui || ui.length > 64 || util.TextEncoder.create('utf-8').encodeInto(state).byteLength > 16 * 1024 || !this.active || !this.ownerCurrent()) return;
    if (this.callbackNavigate) {
      this.callbackNavigate({ kind: 'push', extensionId: this.extensionId, uiId: ui, state: state, title: title,
        context: cloneRecord(this.baseContext) });
      return;
    }
    if (this.stack.length >= MAX_STACK) { this.error = 'extension_ui_stack_limit'; return; }
    const current = this.currentPage();
    if (current?.stream && current.snapshot) {
      const request = this.makeLifecycle('suspend', current); if (request) this.sendLifecycle(request, 'suspend', current.key);
    }
    const page = new ExtensionUIPageState(ui, state, title); page.stream = !!this.client?.features.includes(STREAM_FEATURE) && !this.compact;
    this.stack.push(page); this.streamStackDepth = this.stack.length; this.pageTitle = title || ui;
    this.view = { sections: [] }; this.viewVersion = 0; this.revision++; this.loading = true;
    if (page.stream) this.openCurrent().catch((error: Error) => this.error = this.safeError(error)); else this.renderPage(page).catch((error: Error) => this.error = this.safeError(error));
  }
  popPage(fromActionResult: boolean = false): void {
    if (fromActionResult && this.callbackNavigate) {
      const page = this.currentPage(); if (!page) return;
      this.callbackNavigate({ kind: 'pop', extensionId: this.extensionId, uiId: page.ui, state: page.state,
        title: page.title, context: cloneRecord(this.baseContext) });
      return;
    }
    if (this.stack.length <= 1 || !fromActionResult && (this.busy || this.uncertainEventId)) return;
    const top = this.currentPage();
    if (top?.snapshot) {
      const request: ExtensionViewRecord = { request: 'close', protocol: 1, request_id: util.generateRandomUUID(), session: top.snapshot.session, epoch: top.snapshot.epoch };
      this.sendLifecycle(request, 'close', top.key);
    }
    this.stack.pop(); this.streamStackDepth = this.stack.length;
    const page = this.currentPage(); if (!page) return;
    this.publishPage(page, true); this.loading = false;
    this.applyReadyReceipts(page);
    if (page.stream && page.snapshot && this.active && this.client?.connected) {
      const request = this.makeLifecycle('resume', page); this.phase = 'recovering'; this.loading = true; this.error = '';
      if (request && !this.sendLifecycle(request, 'resume', page.key)) { this.phase = 'disconnected'; this.loading = false; }
    } else if (page.stream && page.openRequest === undefined) this.open();
    else if (this.active && this.client?.connected) this.renderPage(page, true).catch((error: Error) => this.error = this.safeError(error));
  }

  dispose(): void {
    if (this.disposed) return;
    for (const page of this.stack) if (!page.snapshot) this.detachPendingOpen(page);
    this.disposed = true; this.active = false; this.clearLegacyRefresh(); this.clearSecrets(); this.cancelConfirmation();
    for (const page of this.stack) if (page.snapshot && this.client?.connected) {
      const request: ExtensionViewRecord = { request: 'close', protocol: 1, request_id: util.generateRandomUUID(), session: page.snapshot.session, epoch: page.snapshot.epoch };
      try { this.client.repo().transport.send(streamRequestText(request, this.managerEnvelopeValue())); } catch (_) { }
    }
    this.clearFields(); this.clearAllEventTimeouts(); this.events.clear(); this.uncertainEvents.clear(); this.presentations.clear(); this.requestPages.clear(); this.answeredHostConfirmations.clear();
    this.uncertainEventId = '';
    this.eventsOff?.(); this.eventsOff = undefined;
    this.phase = 'closed'; this.loading = false; this.busy = false; this.view = { sections: [] }; this.pageTitle = '';
  }
}
