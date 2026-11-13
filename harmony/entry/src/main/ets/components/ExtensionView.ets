import { UIUtils } from '@kit.ArkUI';
import type { Binding } from '@kit.ArkUI';
import { reconcileJsonProjection } from '../core/JsonProjection';
import { image } from '@kit.ImageKit';
import { url } from '@kit.ArkTS';
import type { ExtensionPresentation } from '../core/ExtensionPresentation';
import { ExtensionViewRecord } from '../core/ExtensionViewTree';
import type { ExtensionChartRow, ExtensionDiffContextRange, ExtensionDiffDirection, ExtensionDiffExpandRequest } from '../core/ExtensionReviewPayload';
import { boundedImageSize, chartRows, diffContextRanges, diffContextValues, diffExpandRequests, ExtensionImageBudget,
  controlInputAllowed, ExtensionImageLease, ExtensionImageTicket, LatestControlValue, lineWindowOffset, multiChoiceValue,
  sliderShouldCommit, threadReplyValues, toggleMultiChoice } from '../core/ExtensionReviewPayload';
import { Wire } from '../models/Wire';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { MarkdownText } from './MarkdownText';

function records(value: Object | undefined): ExtensionViewRecord[] {
  if (!Array.isArray(value)) return [];
  return (value as Object[]).map((item: Object) => Wire.record(item));
}

function recordArrays(value: Object | undefined): Object[][] {
  if (!Array.isArray(value)) return [];
  return (value as Object[]).filter((item: Object) => Array.isArray(item)).map((item: Object) => item as Object[]);
}

function objectValues(value: Object | undefined): Object[] { return Array.isArray(value) ? value as Object[] : []; }

function commentSideChoices(): ExtensionViewRecord[] {
  const newer: ExtensionViewRecord = { value: 'new' };
  const older: ExtensionViewRecord = { value: 'old' };
  return [newer, older];
}

function codeLineAddress(item: ExtensionViewRecord): string {
  const start = Wire.number(item, 'start_line', 1), text = Wire.text(item, 'text');
  return Wire.text(item, 'line_url').replace('{start}', String(start)).replace('{end}', String(start + displayedLineCount(text) - 1));
}

function diffLineText(value: Object): string { return typeof value === 'string' ? value as string : ''; }

function safeHTTPS(value: string): boolean {
  if (!value || value.length > 4096 || !value.startsWith('https://') || /[\r\n\\]/.test(value)) return false;
  try {
    const parsed: url.URL = url.URL.parseURL(value);
    return parsed.protocol === 'https:' && !!parsed.hostname && !parsed.username && !parsed.password;
  } catch (_) { return false; }
}

function initials(value: string, fallback: string): string {
  const text: string = value || fallback;
  return Array.from(text).slice(0, 2).join('').toUpperCase();
}

function toneKey(value: string): string {
  if (value === 'positive') return 'app.color.persona_brand';
  if (value === 'danger') return 'app.color.persona_danger';
  return 'sys.color.font_secondary';
}

function displayedLineCount(value: string): number {
  const rows = value.split('\n').length - (value.endsWith('\n') ? 1 : 0);
  return Math.max(1, rows);
}

interface NativeActionButton {
  action: ExtensionViewRecord;
  key: string;
  title: string;
  event: string;
  role: string;
  summary: string;
  values: ExtensionViewRecord;
}

interface ExtensionControlFlight {
  queue: LatestControlValue;
  timer: number;
  submitted: string;
  previous: string;
  errorAtStart: string;
  observedBusy: boolean;
  authoritativeView: boolean;
  waitingForModel: boolean;
  awaitingUnlock: boolean;
}

interface ExtensionControlEntry {
  control: ExtensionViewRecord;
  key: string;
}

interface ExtensionLineRequest {
  root: string;
  key: string;
  path: string;
  side: string;
  from: number;
  to: number;
  revision: number;
}

interface ExtensionLineWindow {
  offset: number;
  limit: number;
}

class ExtensionLineState {
  sourceIdentity: string = "";
  result: ExtensionViewRecord = {};
  request: ExtensionLineRequest | undefined = undefined;
  revision: number = -1;
  offset: number = 0;
  limit: number = 500;
  history: ExtensionLineWindow[] = [];
}

/** A picture never receives a Hub token through a web view; extension paths use the authenticated native transport. */
@ComponentV2
struct ExtensionImage {
  @Require @Param ui: ExtensionPresentation;
  @Require @Param budget: ExtensionImageBudget;
  @Param source: ExtensionViewRecord = {};
  @Param active: boolean = true;
  @Param alt: string = '';
  @Param aspect: string = 'landscape';
  @Param fit: string = 'fill';
  @Param imageHeight: number = 180;
  @Local pixels: image.PixelMap | undefined = undefined;
  @Local loading: boolean = false;
  @Local failed: boolean = false;
  @Local remote: string = '';
  private visible: boolean = false;
  private generation: number = 0;
  private sourceKey: string = '';
  private ticket: ExtensionImageTicket | undefined = undefined;
  private lease: ExtensionImageLease | undefined = undefined;

  aboutToAppear(): void { this.sourceKey = this.sourceIdentity(); if (this.active && this.visible) this.load(); }
  aboutToDisappear(): void { this.visible = false; this.releaseImage(); }
  @Monitor('source', 'source.path', 'source.url') sourceChanged(): void { this.reset(); }
  @Monitor('active') activeChanged(): void { if (this.active) { if (this.visible) this.load(); } else this.releaseImage(); }

  private reset(): void {
    const next = this.sourceIdentity();
    if (next === this.sourceKey) return;
    this.sourceKey = next; this.releaseImage(); this.failed = false;
    if (this.active && this.visible) this.load();
  }

  private sourceIdentity(): string { return Wire.text(this.source, 'path') + '\u0000' + Wire.text(this.source, 'url'); }

  private releaseImage(): void {
    this.generation++;
    const ticket = this.ticket; this.ticket = undefined;
    if (ticket) this.budget.cancel(ticket);
    const lease = this.lease; this.lease = undefined; lease?.release();
    this.pixels?.release(); this.pixels = undefined;
    this.remote = ''; this.loading = false;
  }

  private async load(): Promise<void> {
    if (!this.active || !this.visible || this.loading || this.pixels || this.remote || this.failed) return;
    const path = Wire.text(this.source, 'path'), address = Wire.text(this.source, 'url');
    const generation = ++this.generation;
    this.loading = true;
    const ticket = this.budget.request(this.displayPixelBudget()); this.ticket = ticket;
    let lease: ExtensionImageLease | undefined = undefined;
    let retained = false;
    let stream: image.ImageSource | undefined;
    try {
      lease = await ticket.promise;
      if (!lease || !this.isCurrent(generation)) return;
      this.ticket = undefined;
      if (!path && safeHTTPS(address)) {
        this.lease = lease; lease.finishLoad(); this.remote = address; retained = true; return;
      }
      if (!path) { this.failed = true; return; }
      const data = await this.ui.imageBytes(path);
      if (!this.isCurrent(generation)) return;
      stream = image.createImageSource(data);
      const info = await stream.getImageInfo();
      if (info.size.width <= 0 || info.size.height <= 0 || info.size.width * info.size.height > 40000000) throw new Error('invalid_image');
      const size = boundedImageSize(info.size.width, info.size.height, this.displayPixelBudget());
      if (size.width <= 0 || size.height <= 0) throw new Error('invalid_image');
      const pixels = await stream.createPixelMap({ desiredSize: { width: size.width, height: size.height } });
      if (!this.isCurrent(generation)) { await pixels.release(); return; }
      this.lease = lease; lease.finishLoad(); this.pixels = pixels; retained = true;
    } catch (_) {
      if (this.isCurrent(generation)) this.failed = true;
    } finally {
      await stream?.release();
      if (!retained) lease?.release();
      if (generation === this.generation) this.loading = false;
    }
  }

  private isCurrent(generation: number): boolean { return this.active && this.visible && generation === this.generation; }

  private imageAspectRatio(): number {
    if (this.aspect === 'square') return 1;
    if (this.aspect === 'wide') return 16 / 9;
    if (this.aspect === 'portrait') return 3 / 4;
    return 4 / 3;
  }

  private displayPixelBudget(): number {
    const width = Math.max(1, Math.round(this.imageHeight * this.imageAspectRatio()));
    return boundedImageSize(width, this.imageHeight).pixels;
  }

  build() {
    Column() {
      if (this.pixels) Image(this.pixels).width('100%').height(this.imageHeight)
        .objectFit(this.fit === 'fit' ? ImageFit.Contain : ImageFit.Cover).accessibilityText(this.alt)
      else if (this.remote) Image(this.remote).width('100%').height(this.imageHeight)
        .objectFit(this.fit === 'fit' ? ImageFit.Contain : ImageFit.Cover).accessibilityText(this.alt)
      else if (this.loading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      else Text(this.alt || tr('extension_ui_image_unavailable')).fontSize(Tokens.meta)
        .fontColor($r('sys.color.font_secondary')).textAlign(TextAlign.Center).width('100%').height(this.imageHeight)
          .padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_tertiary'))
    }.width('100%').aspectRatio(this.imageAspectRatio())
      .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => {
        const next = visible && ratio > 0;
        if (this.visible === next) return;
        this.visible = next;
        if (next && this.active) this.load();
        else if (!next) this.releaseImage();
      })
  }
}

/** Native renderer for extension views. Unknown nodes stay visible as a safe plain-text notice. */
@ComponentV2
export struct ExtensionView {
  @Require @Param model: ExtensionPresentation;
  @Param compact: boolean = false;
  @Param active: boolean = true;
  @Param showTitle: boolean = true;
  @Event onLink: (address: string) => void = (_address: string): void => {};
  @Local presentedView: ExtensionViewRecord | undefined = undefined;
  @Local projectionError: boolean = false;
  @Local projectionGeneration: number = 0;
  private projectionOwner: ExtensionPresentation | undefined = undefined;
  private projectionScope: string = '';
  @Local searchDraft: string = '';
  @Local filterDraft: string = '';
  @Local tabDraft: string = '';
  @Local localValues: Record<string, string> = {};
  @Local lineResultsRevision: number = 0;
  @Local linePagingRevision: number = 0;
  private imageBudget: ExtensionImageBudget = new ExtensionImageBudget(2, 64, 4000000);
  private controlFlights: Map<string, ExtensionControlFlight> = new Map();
  private controlServerValues: Map<string, string> = new Map();
  private controlView: string = '';
  private lineStates: Map<string, ExtensionLineState> = new Map();
  private lineOwner: ExtensionPresentation | undefined = undefined;
  private lineRoot: string = '';
  private lineIdentityView: ExtensionViewRecord | undefined = undefined;
  private lineIdentityRevision: number = -1;
  private lineIdentities: Map<string, string> = new Map();
  private toolbarView: string = '';
  private searchIdentity: string = '';
  private filterIdentity: string = '';
  private tabIdentity: string = '';
  private searchDirty: boolean = false;
  private filterDirty: boolean = false;
  private tabDirty: boolean = false;
  private searchTimer: number = -1;

  aboutToAppear(): void { this.syncProjection(); this.lineResultsRevision = this.model.lineResultRevision; this.syncToolbar(); this.syncControls(); }
  aboutToDisappear(): void { this.clearSearchTimer(); this.cancelControlFlights(); }
  @Monitor('active') visibilityChanged(): void {
    if (this.active) { this.syncProjection(); this.syncToolbar(); this.syncControls(); this.resumeControlFlights(); }
    else { this.clearSearchTimer(); this.cancelControlFlights(); }
  }
  @Monitor('model', 'model.revision', 'model.viewVersion') viewChanged(): void { this.syncProjection(); this.syncToolbar(); this.syncControls(); this.settleControlFlights(); }
  @Monitor('model.busy', 'model.error') actionStateChanged(): void {
    if (this.model.busy) for (const state of this.controlFlights.values()) if (state.queue.inFlight()) state.observedBusy = true;
    this.settleControlFlights();
  }
  @Monitor('model.lineResultRevision') lineResultsChanged(): void { this.lineResultsRevision = this.model.lineResultRevision; }

  private renderView(): ExtensionViewRecord { return this.presentedView || this.model.view; }
  private syncProjection(): void {
    const scope = this.model.presentationKey || this.rootKey();
    if (!this.presentedView || this.projectionOwner !== this.model || this.projectionScope !== scope) {
      this.clearSearchTimer(); this.cancelControlFlights(); this.controlFlights.clear(); this.controlServerValues.clear();
      this.controlView = ''; this.localValues = {}; this.toolbarView = '';
      this.searchIdentity = ''; this.filterIdentity = ''; this.tabIdentity = '';
      this.searchDirty = false; this.filterDirty = false; this.tabDirty = false;
      this.lineStates.clear(); this.lineIdentities.clear(); this.lineIdentityView = undefined; this.lineIdentityRevision = -1;
      this.projectionOwner = this.model; this.projectionScope = scope; this.projectionGeneration++;
      this.presentedView = UIUtils.makeObserved<ExtensionViewRecord>({});
    }
    try { reconcileJsonProjection(this.presentedView, this.model.view); this.projectionError = false; }
    catch (_) { this.clearSearchTimer(); this.cancelControlFlights(); this.controlFlights.clear(); this.projectionError = true; }
  }

  private clearSearchTimer(): void { if (this.searchTimer !== -1) clearTimeout(this.searchTimer); this.searchTimer = -1; }
  private toolbarIdentity(name: string): string {
    const part = Wire.object(this.renderView(), name), choices = this.choices(part['choices'] || part['items']);
    return JSON.stringify([this.rootKey(), Wire.text(this.renderView(), 'title'), Wire.text(part, 'name'), Wire.text(part, 'key'),
      Wire.text(Wire.object(part, 'action'), 'id'), choices.map((choice: Record<string, Object>) => Wire.text(choice, 'value'))]);
  }
  private syncToolbar(): void {
    const encoded = JSON.stringify(this.renderView());
    if (encoded === this.toolbarView) return;
    this.toolbarView = encoded;
    const searchIdentity = this.toolbarIdentity('search'), filterIdentity = this.toolbarIdentity('filter'), tabIdentity = this.toolbarIdentity('tabs');
    const search = Wire.object(this.renderView(), 'search'), filter = Wire.object(this.renderView(), 'filter'), tabs = Wire.object(this.renderView(), 'tabs');
    const searchValue = Wire.text(search, 'value'), filterValue = Wire.text(filter, 'value'), tabValue = Wire.text(tabs, 'value');
    if (searchIdentity !== this.searchIdentity) { this.searchIdentity = searchIdentity; this.searchDraft = searchValue; this.searchDirty = false; }
    else if (this.searchDirty) { if (searchValue === this.searchDraft) this.searchDirty = false; }
    else this.searchDraft = searchValue;
    if (filterIdentity !== this.filterIdentity) { this.filterIdentity = filterIdentity; this.filterDraft = filterValue; this.filterDirty = false; }
    else if (this.filterDirty) { if (filterValue === this.filterDraft) this.filterDirty = false; }
    else this.filterDraft = filterValue;
    if (tabIdentity !== this.tabIdentity) { this.tabIdentity = tabIdentity; this.tabDraft = tabValue; this.tabDirty = false; }
    else if (this.tabDirty) { if (tabValue === this.tabDraft) this.tabDirty = false; }
    else this.tabDraft = tabValue;
  }
  private sections(): ExtensionViewRecord[] { return records(this.renderView()['sections']); }
  private itemKey(section: ExtensionViewRecord, sectionIndex: number, item: ExtensionViewRecord, itemIndex: number): string {
    return Wire.text(item, 'key', '@item:' + String(sectionIndex) + ':' + String(itemIndex));
  }
  private sectionKey(section: ExtensionViewRecord, sectionIndex: number): string {
    return Wire.text(section, 'key', '@section:' + String(sectionIndex));
  }
  private rootKey(): string { return this.model.getSnapshot()?.root_key || '@root'; }
  private sectionItems(section: ExtensionViewRecord): ExtensionViewRecord[] { return records(section['items']); }
  private link(address: string): void { if (safeHTTPS(address)) this.onLink(address); }
  private runAction(action: ExtensionViewRecord, key: string, event: string = 'press', values: ExtensionViewRecord = {}, role: string = '', title: string = ''): void {
    const id = Wire.text(action, 'id');
    if (!id || !this.model.canInteract()) return;
    const payload: ExtensionViewRecord = {};
    for (const name of Object.keys(values)) payload[name] = values[name];
    if (role) payload['__role'] = role;
    if (this.reuseLineResult(key, payload)) return;
    const lineRequest = this.rememberLineRequest(key, payload);
    void this.model.performAction(key, id, event, payload, title).catch((): void => {
      if (lineRequest) {
        const state = this.lineState(key, lineRequest.path);
        if (state.request === lineRequest) state.request = undefined;
      }
    });
  }
  private rememberLineRequest(key: string, values: ExtensionViewRecord): ExtensionLineRequest | undefined {
    const path = Wire.text(values, 'path');
    if (!path) return undefined;
    const rawFrom = values['from'], rawTo = values['to'];
    const from = typeof rawFrom === 'number' ? rawFrom : typeof rawFrom === 'string' ? Number(rawFrom) : 0;
    const to = typeof rawTo === 'number' ? rawTo : typeof rawTo === 'string' && rawTo !== 'end' ? Number(rawTo) : 0;
    const request: ExtensionLineRequest = {
      root: this.rootKey(), key: key, path: path, side: Wire.text(values, 'side'),
      from: Number.isSafeInteger(from) && from > 0 ? from : 0,
      to: Number.isSafeInteger(to) && to > 0 ? to : 0,
      revision: this.lineResultsRevision
    };
    this.lineState(key, path).request = request;
    return request;
  }
  private setLocal(name: string, value: string): void {
    const next: Record<string, string> = {};
    for (const key of Object.keys(this.localValues)) next[key] = this.localValues[key];
    next[name] = value; this.localValues = next;
  }
  private local(name: string, fallback: string): string { return Object.keys(this.localValues).includes(name) ? this.localValues[name] : fallback; }
  private choices(value: Object | undefined): Record<string, Object>[] { return records(value); }
  private choiceTitle(choice: Record<string, Object>): string { return Wire.text(choice, 'title', Wire.text(choice, 'value')); }
  private choiceOptions(choices: Record<string, Object>[]): SelectOption[] {
    return choices.map((choice: Record<string, Object>): SelectOption => ({ value: this.choiceTitle(choice) }));
  }
  private choiceIndex(choices: Record<string, Object>[], value: string): number {
    const found = choices.findIndex((choice: Record<string, Object>) => Wire.text(choice, 'value') === value);
    return found < 0 ? 0 : found;
  }
  private fieldName(item: ExtensionViewRecord): string { return Wire.text(item, 'name'); }
  private safeActionValues(name: string, value: string): ExtensionViewRecord {
    const result: ExtensionViewRecord = {}; if (name) result[name] = value; return result;
  }
  private controlIdentity(control: ExtensionViewRecord, key: string): string { return this.rootKey() + ':' + key + ':' + Wire.text(control, 'name'); }
  private serverControlValue(control: ExtensionViewRecord): string {
    const value = control['value']; return value === undefined ? String(Wire.number(control, 'value')) : String(value);
  }
  private controlCanEdit(control: ExtensionViewRecord, key: string): boolean {
    const state = this.controlFlights.get(this.controlIdentity(control, key));
    return controlInputAllowed(Wire.bool(control, 'disabled'), this.active, this.model.canInteract(), this.model.busy,
      !!state && state.queue.inFlight(), !!state && state.observedBusy, !state || this.model.error === state.errorAtStart);
  }
  private controlDraft(control: ExtensionViewRecord, key: string): string {
    const name = this.controlIdentity(control, key);
    return this.local(name, this.serverControlValue(control));
  }
  private controlDisplay(control: ExtensionViewRecord, key: string): string {
    const value = this.controlDraft(control, key);
    if (value === this.serverControlValue(control) && Wire.text(control, 'display')) return Wire.text(control, 'display');
    return value + Wire.text(control, 'unit');
  }
  private ensureControlFlight(identity: string, value: string): ExtensionControlFlight {
    const existing = this.controlFlights.get(identity); if (existing) return existing;
    const state: ExtensionControlFlight = { queue: new LatestControlValue(value), timer: -1, submitted: '', previous: value,
      errorAtStart: '', observedBusy: false, authoritativeView: false, waitingForModel: false, awaitingUnlock: false };
    this.controlFlights.set(identity, state); return state;
  }
  private controlsInView(): Map<string, ExtensionControlEntry> {
    const result = new Map<string, ExtensionControlEntry>();
    const add = (control: ExtensionViewRecord, key: string): void => {
      const identity = this.controlIdentity(control, key);
      if (Wire.text(control, 'name')) result.set(identity, { control: control, key: key });
    };
    const sections = this.sections();
    for (let sectionIndex = 0; sectionIndex < sections.length; sectionIndex++) {
      const section = sections[sectionIndex], items = this.sectionItems(section);
      for (let itemIndex = 0; itemIndex < items.length; itemIndex++) {
        const item = items[itemIndex], key = this.itemKey(section, sectionIndex, item, itemIndex);
        if (Wire.text(item, 'type') === 'row' && Wire.has(item, 'control')) add(Wire.object(item, 'control'), key);
        if (['grid', 'carousel'].includes(Wire.text(item, 'type'))) {
          const tiles = this.choices(item['tiles']);
          for (let tileIndex = 0; tileIndex < tiles.length; tileIndex++) {
            const tile = tiles[tileIndex]; if (Wire.has(tile, 'control')) add(Wire.object(tile, 'control'), this.tileKey(key, tile, tileIndex));
          }
        }
      }
    }
    return result;
  }
  private syncControls(): void {
    const encodedView = JSON.stringify(this.renderView()), viewChanged = encodedView !== this.controlView;
    this.controlView = encodedView;
    const current = this.controlsInView(), present = new Set<string>();
    for (const identity of current.keys()) {
      const entry = current.get(identity); if (!entry) continue;
      present.add(identity);
      const serverValue = this.serverControlValue(entry.control), previous = this.controlServerValues.get(identity);
      const changed = previous !== undefined && previous !== serverValue;
      this.controlServerValues.set(identity, serverValue);
      const state = this.ensureControlFlight(identity, serverValue);
      if (state.queue.inFlight()) { if (viewChanged) state.authoritativeView = true; continue; }
      if (changed || viewChanged && state.queue.hasQueued()) {
        if (state.timer !== -1) clearTimeout(state.timer);
        state.timer = -1; state.queue.discardPending(serverValue); this.setLocal(identity, serverValue); continue;
      }
      if (state.queue.hasQueued()) continue;
      state.queue.discardPending(serverValue); this.setLocal(identity, serverValue);
    }
    for (const identity of Array.from(this.controlFlights.keys())) {
      if (present.has(identity)) continue;
      const state = this.controlFlights.get(identity); if (!state) continue;
      if (state.timer !== -1) clearTimeout(state.timer);
      state.queue.fail(''); this.controlFlights.delete(identity); this.removeLocal(identity);
      this.controlServerValues.delete(identity);
    }
  }
  private removeLocal(name: string): void {
    if (!Object.keys(this.localValues).includes(name)) return;
    const next: Record<string, string> = {};
    for (const key of Object.keys(this.localValues)) if (key !== name) next[key] = this.localValues[key];
    this.localValues = next;
  }
  private queueControlValue(control: ExtensionViewRecord, key: string, value: string, delay: number): void {
    if (!this.controlCanEdit(control, key)) return;
    const identity = this.controlIdentity(control, key), state = this.ensureControlFlight(identity, this.serverControlValue(control));
    if (!state.queue.hasQueued() && (state.queue.inFlight() ? value === state.submitted : value === state.queue.value())) return;
    state.queue.queue(value, delay === 0); this.setLocal(identity, value);
    if (state.timer !== -1) clearTimeout(state.timer);
    state.timer = -1;
    if (delay > 0) {
      state.timer = setTimeout(() => { state.timer = -1; state.queue.markReady(); this.flushControl(control, key, state); }, delay);
    } else if (delay === 0) this.flushControl(control, key, state);
  }
  private releaseSlider(control: ExtensionViewRecord, key: string): void {
    const identity = this.controlIdentity(control, key), state = this.controlFlights.get(identity);
    if (state) { state.queue.markReady(); this.flushControl(control, key, state); }
  }
  private stepControl(control: ExtensionViewRecord, key: string, direction: number): void {
    const current = Number(this.controlDraft(control, key)), step = Wire.number(control, 'step', 1);
    const min = Wire.number(control, 'min', -9007199254740991), max = Wire.number(control, 'max', 9007199254740991);
    const next = Math.max(min, Math.min(max, current + step * direction));
    this.queueControlValue(control, key, String(next), 600);
  }
  private flushControl(control: ExtensionViewRecord, key: string, state: ExtensionControlFlight): void {
    if (!this.active || !this.model.canInteract() || state.queue.inFlight()) return;
    const value = state.queue.take(); if (value === undefined) return;
    this.sendControlValue(control, key, this.controlIdentity(control, key), state, value);
  }
  private sendControlValue(control: ExtensionViewRecord, key: string, identity: string, state: ExtensionControlFlight, value: string): void {
    const action = Wire.object(control, 'action'), actionId = Wire.text(action, 'id');
    if (!actionId || !this.model.canInteract() || !this.active) { state.queue.fail(this.serverControlValue(control)); this.setLocal(identity, this.serverControlValue(control)); return; }
    state.previous = state.queue.value(); state.submitted = value; state.errorAtStart = this.model.error;
    state.observedBusy = this.model.busy; state.authoritativeView = false; state.waitingForModel = false; state.awaitingUnlock = false;
    this.setLocal(identity, value);
    this.model.performAction(key, actionId, 'change', this.safeActionValues(Wire.text(control, 'name'), value))
      .then((): void => {
        if (this.controlFlights.get(identity) !== state || !state.queue.inFlight()) return;
        if (this.model.busy) { state.observedBusy = true; state.waitingForModel = true; return; }
        this.finishControl(identity, state);
      }).catch((): void => this.failControl(identity, state));
    if (this.model.busy) state.observedBusy = true;
  }
  private uncertainAction(error: string): boolean {
    return error.includes('uncertain') || error.includes('ambiguous') || error.includes('receipt_timeout') || error === 'manager_unavailable';
  }
  private failedAction(error: string): boolean {
    return ['request_updated', 'extension_ui_action_stale', 'extension_ui_action_rejected', 'extension_ui_value_invalid',
      'extension_ui_queue_limit'].includes(error);
  }
  private settleControlFlights(): void {
    if (!this.active) return;
    const controls = this.controlsInView();
    for (const identity of this.controlFlights.keys()) {
      const state = this.controlFlights.get(identity); if (!state) continue;
      const entry = controls.get(identity); if (!entry) continue;
      if (state.queue.inFlight() && !this.model.busy && state.awaitingUnlock && this.model.canInteract()) {
        state.awaitingUnlock = false; this.finishControl(identity, state); continue;
      }
      if (state.queue.inFlight() && !this.model.busy && state.waitingForModel && state.observedBusy) this.finishControl(identity, state);
      else if (!state.queue.inFlight() && state.queue.hasQueued() && state.timer === -1 && this.model.canInteract())
        this.flushControl(entry.control, entry.key, state);
    }
  }
  private finishControl(identity: string, state: ExtensionControlFlight): void {
    if (this.controlFlights.get(identity) !== state || !state.queue.inFlight()) return;
    const error = this.model.error;
    if (error && error !== state.errorAtStart) {
      if (this.uncertainAction(error) && this.active) { state.awaitingUnlock = true; state.waitingForModel = false; return; }
      if (this.failedAction(error)) { this.failControl(identity, state); return; }
    }
    const entry = this.controlsInView().get(identity), serverValue = entry ? this.serverControlValue(entry.control) : state.submitted;
    const accepted = state.authoritativeView || serverValue !== state.previous ? serverValue : state.submitted;
    const next = state.queue.complete(accepted);
    state.waitingForModel = false; state.awaitingUnlock = false; state.observedBusy = false; state.authoritativeView = false;
    if (next !== undefined && entry) { this.setLocal(identity, next); this.sendControlValue(entry.control, entry.key, identity, state, next); }
    else if (state.queue.hasQueued()) this.setLocal(identity, state.queue.queuedValue());
    else this.setLocal(identity, accepted);
  }
  private failControl(identity: string, state: ExtensionControlFlight): void {
    if (this.controlFlights.get(identity) !== state) return;
    const entry = this.controlsInView().get(identity), serverValue = entry ? this.serverControlValue(entry.control) : state.previous;
    state.queue.fail(serverValue); state.waitingForModel = false; state.awaitingUnlock = false; state.observedBusy = false; state.authoritativeView = false;
    if (state.timer !== -1) clearTimeout(state.timer);
    state.timer = -1; this.setLocal(identity, serverValue);
  }
  private cancelControlFlights(): void {
    const controls = this.controlsInView();
    for (const identity of this.controlFlights.keys()) {
      const state = this.controlFlights.get(identity); if (!state) continue;
      if (state.timer !== -1) clearTimeout(state.timer);
      state.timer = -1;
      const entry = controls.get(identity), serverValue = entry ? this.serverControlValue(entry.control) : state.previous;
      state.queue.fail(serverValue); state.waitingForModel = false; state.awaitingUnlock = false; state.observedBusy = false; state.authoritativeView = false;
      this.setLocal(identity, serverValue);
    }
  }
  private resumeControlFlights(): void { this.settleControlFlights(); }
  private mergedActionValues(base: ExtensionViewRecord, name: string, value: Object): ExtensionViewRecord {
    const result: ExtensionViewRecord = {};
    for (const field of Object.keys(base)) result[field] = base[field];
    if (name) result[name] = value;
    return result;
  }

  @Builder private badgeView(badge: ExtensionViewRecord) {
    if (Wire.text(badge, 'text')) Text(Wire.text(badge, 'text')).fontSize(Tokens.meta - 1)
      .fontColor($r(toneKey(Wire.text(badge, 'tone')))).padding({ left: Tokens.gap, right: Tokens.gap, top: Tokens.fine, bottom: Tokens.fine })
      .border({ width: Tokens.hairline, color: $r('sys.color.font_secondary'), radius: Tokens.touch })
  }

  @Builder private spark(value: ExtensionViewRecord) {
    Row({ space: Tokens.fine }) {
      ForEach(this.numberValues(value, 'values').slice(0, 16), (point: number, index: number) => {
        Column() {}.width(4).height(Math.max(3, Math.min(18, Math.abs(point) + 3)))
          .backgroundColor($r(toneKey(Wire.text(value, 'tone')))).borderRadius(Tokens.fine)
      }, (point: number, index: number) => String(index) + ':' + String(point))
    }.height(20).alignItems(VerticalAlign.Bottom)
  }
  private numberValues(value: ExtensionViewRecord, name: string): number[] {
    if (!Array.isArray(value[name])) return [];
    return (value[name] as Object[]).filter((item: Object) => typeof item === 'number' && Number.isFinite(item as number)).map((item: Object) => item as number);
  }

  @Builder private toolbarContent() {
    if (Wire.has(this.renderView(), 'search') || Wire.has(this.renderView(), 'filter')) {
      Row({ space: Tokens.gap }) {
        if (Wire.has(this.renderView(), 'search')) {
          Search({ value: this.searchDraft, placeholder: Wire.text(Wire.object(this.renderView(), 'search'), 'placeholder', tr('search')) })
            .onChange((value: string) => { this.searchDraft = value; this.searchDirty = true; this.clearSearchTimer(); this.searchTimer = setTimeout(() => {
              this.searchTimer = -1; this.runAction(Wire.object(Wire.object(this.renderView(), 'search'), 'action'), this.rootKey(), 'change', this.safeActionValues('q', value));
            }, 300); }).layoutWeight(1)
        }
        if (Wire.has(this.renderView(), 'filter')) {
          if (this.choices(Wire.object(this.renderView(), 'filter')['choices']).length) Select(this.choiceOptions(this.choices(Wire.object(this.renderView(), 'filter')['choices']))).value(this.filterTitle(this.choices(Wire.object(this.renderView(), 'filter')['choices']), this.filterDraft)).enabled(this.model.canInteract())
            .onSelect((index: number) => {
              const choice = this.choices(Wire.object(this.renderView(), 'filter')['choices'])[index]; if (!choice) return;
              this.filterDraft = Wire.text(choice, 'value'); this.filterDirty = true;
              const values = this.safeActionValues(Wire.text(Wire.object(this.renderView(), 'filter'), 'name'), this.filterDraft);
              if (Wire.has(this.renderView(), 'search')) values['q'] = this.searchDraft;
              this.runAction(Wire.object(Wire.object(this.renderView(), 'filter'), 'action'), this.rootKey(), 'change', values);
            }).layoutWeight(1)
        }
      }.width('100%').padding({ top: Tokens.gap, bottom: Tokens.gap })
    }
    if (Wire.has(this.renderView(), 'tabs')) {
      Scroll() {
        Row({ space: Tokens.fine }) {
          ForEach(this.choices(Wire.object(this.renderView(), 'tabs')['items']), (tab: Record<string, Object>, index: number) => {
            Button(this.choiceTitle(tab), { buttonStyle: Wire.text(tab, 'value') === this.tabDraft ? ButtonStyleMode.NORMAL : ButtonStyleMode.TEXTUAL })
              .enabled(this.model.canInteract()).onClick(() => {
                this.tabDraft = Wire.text(tab, 'value'); this.tabDirty = true;
                const values = this.safeActionValues(Wire.text(Wire.object(this.renderView(), 'tabs'), 'name'), this.tabDraft);
                if (Wire.has(this.renderView(), 'search')) values['q'] = this.searchDraft;
                this.runAction(Wire.object(Wire.object(this.renderView(), 'tabs'), 'action'), this.rootKey(), 'change', values);
              })
          }, (tab: Record<string, Object>, index: number) => Wire.text(tab, 'value', String(index)))
        }
      }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).width('100%')
    }
  }
  private filterTitle(choices: Record<string, Object>[], value: string): string {
    const found = choices.find((choice: Record<string, Object>) => Wire.text(choice, 'value') === value);
    return found ? this.choiceTitle(found) : Wire.text(Wire.object(this.renderView(), 'filter'), 'title', value);
  }

  @Builder private sectionView(section: ExtensionViewRecord, sectionIndex: Binding<number>) {
    Column({ space: Tokens.gap }) {
      if (Wire.text(section, 'title')) Text(Wire.text(section, 'title')).fontSize(Tokens.title)
        .fontWeight(FontWeight.Bold).width('100%')
      ForEach(this.sectionItems(section), (item: ExtensionViewRecord, itemIndex: number) => {
        this.componentView(item, this.itemKey(section, sectionIndex.value, item, itemIndex))
      }, (item: ExtensionViewRecord, itemIndex: number) => this.itemKey(section, sectionIndex.value, item, itemIndex))
      if (Wire.has(section, 'more')) this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(Wire.object(section, 'more'), 'action'), key: this.sectionKey(section, sectionIndex.value), title: Wire.text(Wire.object(section, 'more'), 'title', tr('more')), event: 'press', role: '', summary: '', values: {} })))
      if (Wire.text(section, 'footer')) Text(Wire.text(section, 'footer')).fontSize(Tokens.meta)
        .fontColor($r('sys.color.font_secondary')).width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start).padding(this.compact ? Tokens.inset : Tokens.page)
      .backgroundColor($r('sys.color.comp_background_tertiary'))
      .border({ width: Tokens.hairline, color: $r('sys.color.font_secondary'), radius: Tokens.gap })
  }

  @Builder private componentView(item: ExtensionViewRecord, key: string) {
    if (Wire.text(item, 'type') === 'text') this.textView(item)
    else if (Wire.text(item, 'type') === 'metric') this.metricView(item)
    else if (Wire.text(item, 'type') === 'progress') this.progressView(item)
    else if (Wire.text(item, 'type') === 'row') this.rowView(item, key)
    else if (Wire.text(item, 'type') === 'button') this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(item, 'action'), key: key, title: Wire.text(item, 'title'), event: 'press', role: Wire.text(item, 'role'), summary: '', values: {} })))
    else if (Wire.text(item, 'type') === 'link') this.linkView(item)
    else if (Wire.text(item, 'type') === 'field') this.fieldView(item, key)
    else if (Wire.text(item, 'type') === 'divider') Divider().strokeWidth(Tokens.hairline).color($r('sys.color.font_secondary')).width('100%')
    else if (Wire.text(item, 'type') === 'chart') this.chartView(item)
    else if (Wire.text(item, 'type') === 'stat_grid') this.statsView(item)
    else if (Wire.text(item, 'type') === 'segmented') this.segmentedView(item, key)
    else if (Wire.text(item, 'type') === 'markdown') MarkdownText({ text: Wire.text(item, 'text'), onLink: (address: string) => this.link(address) })
    else if (Wire.text(item, 'type') === 'table') this.tableView(item)
    else if (Wire.text(item, 'type') === 'empty') this.emptyView(item, key)
    else if (Wire.text(item, 'type') === 'loading') this.loadingView(item)
    else if (Wire.text(item, 'type') === 'html') this.htmlFallback(item)
    else if (Wire.text(item, 'type') === 'header') this.headerView(item)
    else if (Wire.text(item, 'type') === 'breadcrumb') this.breadcrumbView(item, key)
    else if (Wire.text(item, 'type') === 'file_tree') this.fileTreeView(item, key)
    else if (Wire.text(item, 'type') === 'code') this.codeView(item, key)
    else if (Wire.text(item, 'type') === 'diff') this.diffView(item, key)
    else if (Wire.text(item, 'type') === 'comment_thread') this.threadView(item, key)
    else if (Wire.text(item, 'type') === 'composer') this.composerView(item, key)
    else if (Wire.text(item, 'type') === 'image') this.imageView(item)
    else if (Wire.text(item, 'type') === 'grid') this.gridView(item, key)
    else if (Wire.text(item, 'type') === 'carousel') this.carouselView(item, key)
    else Column({ space: Tokens.fine }) {
      Text(tr('extension_ui_unknown_node')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (Wire.text(item, 'type')) Text(Wire.text(item, 'type')).fontSize(Tokens.meta).fontFamily('monospace')
      if (Wire.text(item, 'title', Wire.text(item, 'text', Wire.text(item, 'label', Wire.text(item, 'detail'))))) Text(Wire.text(item, 'title', Wire.text(item, 'text', Wire.text(item, 'label', Wire.text(item, 'detail'))))).fontSize(Tokens.body).width('100%').wordBreak(WordBreak.BREAK_WORD)
    }.width('100%').padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_tertiary'))
  }

  @Builder private textView(item: ExtensionViewRecord) {
    Text(Wire.text(item, 'text')).fontSize(Wire.text(item, 'style', 'body') === 'title' ? Tokens.title : Wire.text(item, 'style', 'body') === 'headline' ? Tokens.body + 1 : Wire.text(item, 'style', 'body') === 'caption' ? Tokens.meta - 1 : Tokens.body)
      .fontWeight(Wire.text(item, 'style', 'body') === 'title' || Wire.text(item, 'style', 'body') === 'headline' ? FontWeight.Bold : FontWeight.Normal)
      .fontFamily(Wire.text(item, 'style', 'body') === 'code' ? 'monospace' : 'sans-serif').fontColor($r(Wire.text(item, 'style', 'body') === 'secondary' || Wire.text(item, 'style', 'body') === 'caption' ? 'sys.color.font_secondary' : 'sys.color.font_primary'))
      .wordBreak(WordBreak.BREAK_WORD).copyOption(CopyOptions.LocalDevice).width('100%')
  }
  @Builder private metricView(item: ExtensionViewRecord) {
    Column({ space: Tokens.fine }) {
      Text(Wire.text(item, 'label')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      Row({ space: Tokens.gap }) {
        Text(Wire.text(item, 'value')).fontSize(Tokens.title).fontWeight(FontWeight.Bold).fontColor($r(toneKey(Wire.text(item, 'tone'))))
        if (Wire.has(item, 'spark')) this.spark(Wire.object(item, 'spark'))
      }.width('100%')
      if (Wire.text(item, 'detail')) Text(Wire.text(item, 'detail')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start)
  }
  @Builder private progressView(item: ExtensionViewRecord) {
    Column({ space: Tokens.fine }) {
      Row() { Text(Wire.text(item, 'label')).fontSize(Tokens.meta).layoutWeight(1); Text(String(Math.round(Wire.number(item, 'ratio') * 100)) + '%').fontSize(Tokens.meta) }.width('100%')
      Progress({ value: Math.max(0, Math.min(100, Wire.number(item, 'ratio') * 100)), total: 100, type: ProgressType.Linear }).width('100%')
      if (Wire.text(item, 'detail')) Text(Wire.text(item, 'detail')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
    }.width('100%')
  }
  @Builder private rowView(item: ExtensionViewRecord, key: string) {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        if (Wire.text(item, 'icon')) Text(Wire.text(item, 'icon')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width(Tokens.touch).textAlign(TextAlign.Center)
        if (Wire.has(item, 'avatar')) this.avatarView(Wire.object(item, 'avatar'), UIUtils.makeBinding((): string => Wire.text(item, 'title')))
        if (Wire.has(item, 'thumbnail')) this.thumbnailView(Wire.object(item, 'thumbnail'))
        Column({ space: Tokens.fine }) {
          Text(Wire.text(item, 'title')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).maxLines(2).textOverflow({ overflow: TextOverflow.Ellipsis }).width('100%')
          if (Wire.text(item, 'subtitle')) Text(Wire.text(item, 'subtitle')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).maxLines(2).width('100%')
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
        if (Wire.text(item, 'value')) Text(Wire.text(item, 'value')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).maxLines(1)
        if (Wire.has(item, 'badge')) this.badgeView(Wire.object(item, 'badge'))
        if (Wire.has(item, 'spark')) this.spark(Wire.object(item, 'spark'))
      }.width('100%').alignItems(VerticalAlign.Center)
        .onClick(() => { if (Wire.has(item, 'action')) this.runAction(Wire.object(item, 'action'), key, 'press', {}, '', Wire.text(item, 'title')); })
      if (Wire.has(item, 'control')) this.rowControl(Wire.object(item, 'control'), key, UIUtils.makeBinding((): string => Wire.text(item, 'title')))
      ForEach(this.choices(item['actions']), (candidate: Record<string, Object>, index: number) => {
        this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(candidate, 'action'), key: key, title: Wire.text(candidate, 'title'), event: 'press', role: Wire.text(candidate, 'role'), summary: '', values: {} })))
      }, (candidate: Record<string, Object>, index: number) => Wire.text(Wire.object(candidate, 'action'), 'id', String(index)))
    }.width('100%').padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_secondary'))
      .border({ width: Tokens.hairline, color: $r('sys.color.font_secondary'), radius: Tokens.gap })
  }
  @Builder private avatarView(avatar: ExtensionViewRecord, title: Binding<string>) {
    if (Wire.text(avatar, 'path') || Wire.text(avatar, 'url')) ExtensionImage({ ui: this.model, budget: this.imageBudget, active: this.active, source: avatar, alt: title.value, aspect: 'square', imageHeight: 36 })
      .width(36).height(36).borderRadius(Tokens.touch)
    else Text(initials(Wire.text(avatar, 'text'), title.value)).fontSize(Tokens.meta).fontWeight(FontWeight.Medium)
      .width(36).height(36).textAlign(TextAlign.Center).backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.touch)
  }
  @Builder private thumbnailView(thumbnail: ExtensionViewRecord) {
    if (Wire.text(thumbnail, 'path') || Wire.text(thumbnail, 'url')) ExtensionImage({ ui: this.model, budget: this.imageBudget, active: this.active, source: thumbnail, alt: '', aspect: Wire.text(thumbnail, 'aspect', 'square'), imageHeight: 56 }).width(72).height(56)
  }
  @Builder private actionButton(button: Binding<NativeActionButton>) {
    if (Wire.text(button.value.action, 'id')) Button(button.value.title || Wire.text(button.value.action, 'id'))
      .role(button.value.role === 'destructive' ? ButtonRole.ERROR : ButtonRole.NORMAL)
      .enabled(this.model.canInteract()).height(Tokens.touch).onClick(() => {
        const current = button.value;
        this.runAction(current.action, current.key, current.event, current.values, current.role, current.summary || current.title);
      })
  }

  @Builder private rowControl(control: ExtensionViewRecord, key: string, label: Binding<string>) {
    if (Wire.text(control, 'kind') === 'switch') {
      Toggle({ type: ToggleType.Switch, isOn: Wire.bool(control, 'on') }).enabled(!Wire.bool(control, 'disabled') && this.model.canInteract())
        .onChange((on: boolean) => this.runAction(Wire.object(control, 'action'), key, 'change', this.safeActionValues(Wire.text(control, 'name'), String(on))))
        .accessibilityText(label.value)
    } else if (Wire.text(control, 'kind') === 'slider') {
      Row({ space: Tokens.gap }) {
        Slider({ value: Number(this.controlDraft(control, key)), min: Wire.number(control, 'min', 0), max: Wire.number(control, 'max', 100), step: Wire.number(control, 'step', 1) })
          .enabled(this.controlCanEdit(control, key)).layoutWeight(1)
          .onChange((value: number, mode: SliderChangeMode) => {
            this.queueControlValue(control, key, String(value), -1);
            if (sliderShouldCommit(mode)) this.releaseSlider(control, key);
          })
        Text(this.controlDisplay(control, key)).fontSize(Tokens.meta)
      }.width('100%')
    } else if (Wire.text(control, 'kind') === 'stepper') {
      Row({ space: Tokens.gap }) {
        Button('−').enabled(this.controlCanEdit(control, key)).onClick(() => this.stepControl(control, key, -1))
        Text(this.controlDisplay(control, key)).fontSize(Tokens.body)
        Button('+').enabled(this.controlCanEdit(control, key)).onClick(() => this.stepControl(control, key, 1))
      }.width('100%')
    }
  }

  @Builder private linkView(item: ExtensionViewRecord) {
    if (safeHTTPS(Wire.text(item, 'url'))) Button(Wire.text(item, 'title')).buttonStyle(ButtonStyleMode.TEXTUAL).fontColor($r('sys.color.font_emphasize'))
      .height(Tokens.touch).onClick(() => this.link(Wire.text(item, 'url')))
    else Text(Wire.text(item, 'title') + ' · ' + Wire.text(item, 'url')).fontSize(Tokens.body).fontColor($r('sys.color.font_secondary'))
  }
  @Builder private fieldView(item: ExtensionViewRecord, key: string) {
    Column({ space: Tokens.fine }) {
      Text(Wire.text(item, 'label')).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).width('100%')
      if (Wire.text(item, 'kind') === 'toggle') Toggle({ type: ToggleType.Switch, isOn: this.model.fieldValue(key, Wire.text(item, 'kind') === 'secret' ? '' : Wire.text(item, 'value')) === 'true' })
        .onChange((checked: boolean) => this.model.setFieldValue(key, String(checked), false))
      else if (Wire.text(item, 'kind') === 'choice' && this.choices(item['choices']).length) {
        Select(this.choiceOptions(this.choices(item['choices'])))
          .value(this.fieldChoiceTitle(this.choices(item['choices']), this.model.fieldValue(key, Wire.text(item, 'kind') === 'secret' ? '' : Wire.text(item, 'value')))).onSelect((index: number) => {
          const choice = this.choices(item['choices'])[index]; if (choice) this.model.setFieldValue(key, Wire.text(choice, 'value'), false);
        })
      } else if (Wire.text(item, 'kind') === 'multichoice' && this.choices(item['choices']).length) this.multiChoiceView(item, key)
      else if (Wire.text(item, 'kind') === 'multiline') TextArea({ text: this.model.fieldValue(key, Wire.text(item, 'kind') === 'secret' ? '' : Wire.text(item, 'value')), placeholder: Wire.text(item, 'placeholder') }).height(Tokens.inputExpanded)
        .onChange((next: string) => this.model.setFieldValue(key, next, false))
      else TextInput({ text: this.model.fieldValue(key, Wire.text(item, 'kind') === 'secret' ? '' : Wire.text(item, 'value')), placeholder: Wire.text(item, 'placeholder') || Wire.text(item, 'label') })
        .type(Wire.text(item, 'kind') === 'secret' ? InputType.Password : Wire.text(item, 'kind') === 'number' ? InputType.Number : InputType.Normal)
        .showPasswordIcon(false).onChange((next: string) => this.model.setFieldValue(key, next, Wire.text(item, 'kind') === 'secret'))
      if (Wire.text(item, 'detail')) Text(Wire.text(item, 'detail')).fontSize(Tokens.meta - 1)
        .fontColor($r('sys.color.font_secondary')).width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start)
  }
  private fieldChoiceTitle(choices: Record<string, Object>[], value: string): string {
    const found = choices.find((choice: Record<string, Object>) => Wire.text(choice, 'value') === value);
    return found ? this.choiceTitle(found) : value;
  }
  private selectedValues(raw: string): string[] {
    try {
      const parsed: Object = JSON.parse(raw) as Object;
      if (Array.isArray(parsed)) return (parsed as Object[]).filter((item: Object) => typeof item === 'string').map((item: Object) => item as string);
    } catch (_) { }
    return [];
  }
  @Builder private multiChoiceView(item: ExtensionViewRecord, key: string) {
    Column({ space: Tokens.fine }) {
      ForEach(this.choices(item['choices']), (choice: Record<string, Object>, index: number) => {
        Row({ space: Tokens.gap }) {
          Toggle({ type: ToggleType.Checkbox, isOn: this.selectedValues(this.model.fieldValue(key, multiChoiceValue(item))).includes(Wire.text(choice, 'value')) })
            .onChange(() => this.model.setFieldValue(key,
              toggleMultiChoice(this.model.fieldValue(key, multiChoiceValue(item)), Wire.text(choice, 'value')), false))
          Text(this.choiceTitle(choice)).fontSize(Tokens.body).layoutWeight(1)
        }.width('100%')
      }, (choice: Record<string, Object>, index: number) => Wire.text(choice, 'value', String(index)))
    }.width('100%')
  }

  @Builder private segmentedView(item: ExtensionViewRecord, key: string) {
    Scroll() { Row({ space: Tokens.fine }) {
      ForEach(this.choices(item['choices']), (choice: Record<string, Object>, index: number) => {
        Button(this.choiceTitle(choice), { buttonStyle: Wire.text(choice, 'value') === this.local(key + ':' + Wire.text(item, 'name'), Wire.text(item, 'value')) ? ButtonStyleMode.NORMAL : ButtonStyleMode.TEXTUAL })
          .enabled(this.model.canInteract()).onClick(() => {
            const value = Wire.text(choice, 'value'); this.setLocal(key + ':' + Wire.text(item, 'name'), value);
            this.runAction(Wire.object(item, 'action'), key, 'change', this.safeActionValues(Wire.text(item, 'name'), value));
          })
      }, (choice: Record<string, Object>, index: number) => Wire.text(choice, 'value', String(index)))
    }}.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).width('100%')
  }
  @Builder private tableView(item: ExtensionViewRecord) {
    Column({ space: Tokens.gap }) {
      if (Wire.text(item, 'caption')) Text(Wire.text(item, 'caption')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      Scroll() {
        Column() {
          Row() { ForEach(this.choices(item['columns']), (column: Record<string, Object>, index: number) => Text(Wire.text(column, 'title'))
            .fontSize(Tokens.meta).fontWeight(FontWeight.Bold).width(160).padding(Tokens.gap), (column: Record<string, Object>, index: number) => String(index)) }
          ForEach(recordArrays(item['rows']), (row: Object[], rowIndex: number) => {
            Row() { ForEach(row, (cell: Object, cellIndex: number) => Text(typeof cell === 'string' ? cell as string : '')
              .fontSize(Tokens.meta).width(160).padding(Tokens.gap).border({ width: Tokens.hairline, color: $r('sys.color.font_secondary') }), (cell: Object, cellIndex: number) => String(cellIndex) + ':' + JSON.stringify(cell)) }
          }, (_row: Object[], rowIndex: number) => String(rowIndex))
        }
      }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Auto).width('100%')
    }.width('100%')
  }
  @Builder private statsView(item: ExtensionViewRecord) {
    Flex({ direction: FlexDirection.Row, wrap: FlexWrap.Wrap, justifyContent: FlexAlign.Start }) {
      ForEach(this.choices(item['stats']), (stat: Record<string, Object>, index: number) => Column({ space: Tokens.fine }) {
        Text(Wire.text(stat, 'label')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
        Text(Wire.text(stat, 'value')).fontSize(Tokens.title).fontWeight(FontWeight.Bold).fontColor($r(toneKey(Wire.text(stat, 'tone')))).width('100%')
        if (Wire.text(stat, 'detail')) Text(Wire.text(stat, 'detail')).fontSize(Tokens.meta - 1).width('100%')
        if (Wire.has(stat, 'spark')) this.spark(Wire.object(stat, 'spark'))
      }.width('48%').padding(Tokens.inset).alignItems(HorizontalAlign.Start)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.gap), (_stat: Record<string, Object>, index: number) => String(index))
    }.width('100%')
  }
  @Builder private chartView(item: ExtensionViewRecord) {
    Column({ space: Tokens.gap }) {
      if (Wire.text(item, 'title')) Text(Wire.text(item, 'title')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
      if (Wire.text(item, 'kind') === 'candlestick') {
        ForEach(chartRows(item), (point: ExtensionChartRow, pointIndex: number) => Column({ space: Tokens.fine }) {
          Text(this.chartPointLabel(point)).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).width('100%')
          Text('O ' + point.open + '   H ' + point.high + '   L ' + point.low + '   C ' + point.close + (point.volume ? '   V ' + point.volume : ''))
            .fontSize(Tokens.meta - 1).fontFamily('monospace').width('100%').wordBreak(WordBreak.BREAK_WORD)
        }.width('100%').padding(Tokens.gap).backgroundColor($r('sys.color.comp_background_secondary')),
          (point: ExtensionChartRow, pointIndex: number) => String(pointIndex) + ':' + JSON.stringify(point))
      } else {
        ForEach(chartRows(item), (point: ExtensionChartRow, pointIndex: number) => Row({ space: Tokens.gap }) {
          Text(this.chartPointLabel(point)).fontSize(Tokens.meta - 1).layoutWeight(1).wordBreak(WordBreak.BREAK_WORD)
          Text(point.value).fontSize(Tokens.meta - 1).textAlign(TextAlign.End).wordBreak(WordBreak.BREAK_WORD)
        }.width('100%').padding({ top: Tokens.fine, bottom: Tokens.fine }), (point: ExtensionChartRow, pointIndex: number) => String(pointIndex) + ':' + JSON.stringify(point))
      }
      if (Wire.text(item, 'detail')) Text(Wire.text(item, 'detail')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
    }.width('100%').padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.gap)
  }
  private chartPointLabel(point: ExtensionChartRow): string { return point.series ? point.series + ' · ' + point.label : point.label; }
  @Builder private emptyView(item: ExtensionViewRecord, key: string) {
    Column({ space: Tokens.gap }) {
      Text(Wire.text(item, 'icon', '○')).fontSize(Tokens.title).fontColor($r('sys.color.font_secondary'))
      Text(Wire.text(item, 'title')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%').textAlign(TextAlign.Center)
      if (Wire.text(item, 'detail')) Text(Wire.text(item, 'detail')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%').textAlign(TextAlign.Center)
      if (Wire.has(item, 'button')) this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(Wire.object(item, 'button'), 'action'), key: key, title: Wire.text(Wire.object(item, 'button'), 'title'), event: 'press', role: '', summary: '', values: {} })))
    }.width('100%').alignItems(HorizontalAlign.Center).padding(Tokens.page)
  }
  @Builder private loadingView(item: ExtensionViewRecord) {
    Row({ space: Tokens.gap }) { LoadingProgress().width(Tokens.touch).height(Tokens.touch); Text(Wire.text(item, 'title', tr('loading'))).fontSize(Tokens.meta) }
      .width('100%').padding(Tokens.inset)
  }
  @Builder private htmlFallback(item: ExtensionViewRecord) {
    Column({ space: Tokens.gap }) {
      Text(Wire.text(item, 'title', tr('extension_ui_html_unsupported'))).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      Text(Wire.text(item, 'html')).fontSize(Tokens.meta).fontFamily('monospace').wordBreak(WordBreak.BREAK_WORD)
        .copyOption(CopyOptions.LocalDevice).width('100%')
    }.width('100%').padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_tertiary'))
  }
  @Builder private headerView(item: ExtensionViewRecord) {
    Column({ space: Tokens.gap }) {
      if (Wire.has(item, 'avatar')) Row({ space: Tokens.gap }) {
        this.avatarView(Wire.object(item, 'avatar'), UIUtils.makeBinding((): string => Wire.text(item, 'title')))
        Column({ space: Tokens.fine }) { Text(Wire.text(item, 'title')).fontSize(Tokens.title).fontWeight(FontWeight.Bold)
          if (Wire.text(item, 'subtitle')) Text(Wire.text(item, 'subtitle')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
      }.width('100%')
      else {
        Text(Wire.text(item, 'title')).fontSize(Tokens.title).fontWeight(FontWeight.Bold).width('100%')
        if (Wire.text(item, 'subtitle')) Text(Wire.text(item, 'subtitle')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      }
      Flex({ direction: FlexDirection.Row, wrap: FlexWrap.Wrap }) {
        ForEach(this.choices(item['badges']), (badge: Record<string, Object>, index: number) => this.badgeView(badge), (_badge: Record<string, Object>, index: number) => String(index))
      }.width('100%')
      ForEach(this.choices(item['facts']), (fact: Record<string, Object>, index: number) => Row({ space: Tokens.gap }) {
        if (Wire.text(fact, 'icon')) Text(Wire.text(fact, 'icon')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width(32)
        if (!Wire.text(fact, 'icon')) Text(Wire.text(fact, 'label')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width(72)
        Text(Wire.text(fact, 'value')).fontSize(Tokens.meta).layoutWeight(1)
      }.width('100%'), (_fact: Record<string, Object>, index: number) => String(index))
    }.width('100%').padding(Tokens.inset)
  }
  @Builder private breadcrumbView(item: ExtensionViewRecord, key: string) {
    Scroll() { Row({ space: Tokens.fine }) {
      ForEach(this.choices(item['crumbs']), (crumb: Record<string, Object>, index: number) => {
        if (index > 0) Text('/').fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
        if (Wire.has(crumb, 'action') && index < this.choices(item['crumbs']).length - 1) Button(Wire.text(crumb, 'title'), { buttonStyle: ButtonStyleMode.TEXTUAL })
          .enabled(!this.model.busy).onClick(() => this.runAction(Wire.object(crumb, 'action'), key, 'press'))
        else Text(Wire.text(crumb, 'title')).fontSize(Tokens.meta).fontWeight(index === this.choices(item['crumbs']).length - 1 ? FontWeight.Bold : FontWeight.Normal)
      }, (_crumb: Record<string, Object>, index: number) => String(index))
    }}.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).width('100%')
  }
  @Builder private fileTreeView(item: ExtensionViewRecord, key: string) {
    Column({ space: Tokens.fine }) {
      ForEach(this.choices(item['entries']), (entry: Record<string, Object>, index: number) => {
        Row({ space: Tokens.gap }) {
          Text(Wire.text(entry, 'kind') === 'dir' ? '▸' : '·').fontSize(Tokens.meta).width(20).textAlign(TextAlign.Center)
          Button(Wire.text(entry, 'path'), { buttonStyle: ButtonStyleMode.TEXTUAL }).enabled(!this.model.busy)
            .layoutWeight(1).onClick(() => {
              const path = Wire.text(entry, 'path');
              const action = Wire.text(entry, 'kind') === 'dir' ? Wire.object(item, 'expand') : Wire.object(item, 'action');
              if (Object.keys(action).length) this.runAction(action, key, 'press', this.safeActionValues('path', path));
            })
          Text('+' + String(Wire.number(entry, 'additions')) + ' −' + String(Wire.number(entry, 'deletions'))).fontSize(Tokens.meta - 1)
            .fontColor($r('sys.color.font_secondary'))
        }.width('100%').padding({ left: Math.min(48, Wire.text(entry, 'path').split('/').length * Tokens.gap), right: Tokens.gap })
      }, (entry: Record<string, Object>, index: number) => Wire.text(entry, 'path', String(index)))
    }.width('100%').padding({ top: Tokens.fine, bottom: Tokens.fine })
  }
  @Builder private codeView(item: ExtensionViewRecord, key: string) {
    Column({ space: Tokens.gap }) {
      if (Wire.text(item, 'path')) Text(Wire.text(item, 'path')).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).width('100%')
      Scroll() {
        Column() {
          ForEach(Wire.text(item, 'text').split('\n'), (line: string, index: number) => Row() {
            Text(String(Wire.number(item, 'start_line', 1) + index)).fontSize(Tokens.meta - 1).fontFamily('monospace').fontColor($r('sys.color.font_secondary')).width(48).textAlign(TextAlign.End).padding({ right: Tokens.gap })
            Text(line || ' ').fontSize(Tokens.meta).fontFamily('monospace').fontColor($r('sys.color.font_primary'))
              .backgroundColor(Wire.number(item, 'start_line', 1) + index >= Wire.number(Wire.object(item, 'highlight'), 'start', -1) && Wire.number(item, 'start_line', 1) + index <= Wire.number(Wire.object(item, 'highlight'), 'end', -1) ? $r('sys.color.comp_background_tertiary') : Color.Transparent)
              .copyOption(CopyOptions.LocalDevice).layoutWeight(1)
          }.width('100%'), (line: string, index: number) => String(index) + ':' + line)
        }
      }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Auto).width('100%')
        .backgroundColor($r('sys.color.comp_background_secondary')).padding(Tokens.gap)
      if (Wire.bool(item, 'truncated') && Wire.has(item, 'load')) this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(item, 'load'), key: key, title: tr('extension_ui_load_more'), event: 'press', role: '', summary: '', values: { path: Wire.text(item, 'path'), from: String(Wire.number(item, 'start_line', 1) + displayedLineCount(Wire.text(item, 'text'))) } })))
      if (Wire.text(item, 'line_url')) {
        if (safeHTTPS(codeLineAddress(item))) Button(tr('extension_ui_open_lines'), { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => this.link(codeLineAddress(item)))
      }
      this.returnedLines(key, UIUtils.makeBinding((): string => Wire.text(item, 'path')))
    }.width('100%')
  }
  private lineComponentIdentity(key: string, path: string): string {
    if (this.lineIdentityView !== this.renderView() || this.lineIdentityRevision !== this.model.revision) {
      this.lineIdentities.clear();
      for (const section of this.sections()) for (const item of this.sectionItems(section)) {
        if (['code', 'diff'].includes(Wire.text(item, 'type'))) {
          this.lineIdentities.set(JSON.stringify([Wire.text(item, 'key'), Wire.text(item, 'path')]), JSON.stringify(item));
        }
      }
      this.lineIdentityView = this.renderView(); this.lineIdentityRevision = this.model.revision;
    }
    return this.lineIdentities.get(JSON.stringify([key, path])) || '';
  }
  private lineState(key: string, path: string): ExtensionLineState {
    const snapshot = this.model.getSnapshot();
    const root = JSON.stringify([this.rootKey(), snapshot?.session || '', snapshot?.epoch || '']);
    if (this.lineOwner !== this.model || this.lineRoot !== root) {
      this.lineStates.clear(); this.lineOwner = this.model; this.lineRoot = root;
    }
    const identity = JSON.stringify([key, path]);
    let state = this.lineStates.get(identity);
    const sourceIdentity = this.lineComponentIdentity(key, path);
    if (!state) {
      // Retain independent windows, bounded even for dynamically generated components.
      if (this.lineStates.size >= 64) for (const entry of this.lineStates.keys()) { this.lineStates.delete(entry); break; }
      state = new ExtensionLineState(); state.sourceIdentity = sourceIdentity; this.lineStates.set(identity, state);
    } else if (state.sourceIdentity !== sourceIdentity) {
      // A branch/commit/content change can keep the same key and path. Old FileLines are then invalid.
      state = new ExtensionLineState(); state.sourceIdentity = sourceIdentity;
      state.revision = this.lineResultsRevision; this.lineStates.set(identity, state);
    }
    return state;
  }
  private matchingLineResult(key: string, path: string): ExtensionViewRecord {
    const state = this.lineState(key, path);
    if (state.revision !== this.lineResultsRevision) {
      const result = this.model.lineResultFor(key);
      const side = Wire.text(result, 'side', 'new');
      const request = state.request;
      const resultMatches = Wire.text(result, 'path') === path && Array.isArray(result['lines']);
      const requestMatches = !!request && request.root === this.rootKey() && request.key === key && request.path === path;
      const correlated = resultMatches && (!requestMatches || !!request && (!request.side || request.side === side) &&
        request.revision + 1 === this.lineResultsRevision);
      if (correlated && result !== state.result) {
        state.result = result;
        const lines = objectValues(result['lines']), start = Wire.number(result, 'start', 1);
        const requestedStart = requestMatches && request && request.from > 0 ? request.from : start;
        state.offset = lineWindowOffset(requestedStart, start, lines.length);
        state.limit = requestMatches && request && request.from > 0 && request.to >= request.from
          ? Math.max(1, Math.min(500, request.to - request.from + 1)) : 500;
        state.history = []; state.request = undefined;
        // Each protocol result is bounded to 4 MiB. Keep at most four result bodies.
        let retained = 0;
        for (const entry of this.lineStates.values()) if (Object.keys(entry.result).length) retained++;
        for (const entry of this.lineStates.values()) {
          if (retained <= 4) break;
          if (entry !== state && Object.keys(entry.result).length) { entry.result = {}; retained--; }
        }
      }
      // An unrelated component's reply never erases this component's cache or window.
      state.revision = this.lineResultsRevision;
    }
    return state.result;
  }
  private reuseLineResult(key: string, values: ExtensionViewRecord): boolean {
    const path = Wire.text(values, 'path');
    if (!path || !Wire.has(values, 'from')) return false;
    const result = this.matchingLineResult(key, path), lines = objectValues(result['lines']);
    const start = Wire.number(result, 'start', 1), end = start + lines.length - 1;
    const from = Number(values['from']), rawTo = values['to'];
    const to = rawTo === 'end' ? Wire.number(result, 'total', -1) : rawTo === undefined ? end : Number(rawTo);
    if (!lines.length || !Number.isSafeInteger(from) || !Number.isSafeInteger(to) || from < start || to < from || to > end ||
      Wire.text(values, 'side', 'new') !== Wire.text(result, 'side', 'new')) return false;
    const state = this.lineState(key, path);
    state.offset = from - start; state.limit = Math.min(500, to - from + 1); state.history = [];
    state.request = undefined; this.linePagingRevision++;
    return true;
  }
  private lineResultValues(key: string, path: string): Object[] {
    const revision = this.linePagingRevision; // Native render dependency for plain per-component windows.
    const lines = objectValues(this.matchingLineResult(key, path)['lines']);
    const state = this.lineState(key, path);
    return lines.slice(state.offset, state.offset + state.limit);
  }
  private lineResultOffset(key: string, path: string): number {
    this.matchingLineResult(key, path);
    return this.lineState(key, path).offset;
  }
  private lineResultHeader(key: string, path: string): string {
    const result = this.matchingLineResult(key, path);
    const start = Wire.number(result, 'start', 1) + this.lineResultOffset(key, path);
    const visible = this.lineResultValues(key, path), end = visible.length ? start + visible.length - 1 : start;
    return Wire.text(result, 'path') + ' · ' + Wire.text(result, 'side', 'new') + ' · ' + String(start) + '–' + String(end);
  }
  private moveLineWindow(key: string, path: string, next: boolean): void {
    const result = this.matchingLineResult(key, path), lines = objectValues(result['lines']);
    if (!lines.length) return;
    const state = this.lineState(key, path);
    if (next) {
      const offset = state.offset + this.lineResultValues(key, path).length;
      if (offset >= lines.length) return;
      state.history.push({ offset: state.offset, limit: state.limit });
      state.offset = offset; state.limit = 500;
    } else if (state.history.length) {
      const last = state.history.pop();
      if (last) { state.offset = last.offset; state.limit = last.limit; }
    } else { state.offset = Math.max(0, state.offset - 500); state.limit = 500; }
    this.linePagingRevision++;
  }
  private hasEarlierLineWindow(key: string, path: string): boolean {
    return this.lineResultOffset(key, path) > 0;
  }
  private hasMoreLineWindows(key: string, path: string): boolean {
    const result = this.matchingLineResult(key, path), lines = objectValues(result['lines']);
    return this.lineResultOffset(key, path) + this.lineResultValues(key, path).length < lines.length;
  }
  @Builder private returnedLines(key: string, path: Binding<string>) {
    if (this.lineResultValues(key, path.value).length) {
      Text(this.lineResultHeader(key, path.value)).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).width('100%')
      ForEach(this.lineResultValues(key, path.value), (line: Object, index: number) => Text(String(Wire.number(this.matchingLineResult(key, path.value), 'start', 1) + this.lineResultOffset(key, path.value) + index) + '  ' + String(line))
        .fontSize(Tokens.meta).fontFamily('monospace').width('100%'), (line: Object, index: number) => String(index) + ':' + JSON.stringify(line))
      Row({ space: Tokens.gap }) {
        if (this.hasEarlierLineWindow(key, path.value)) Button(tr('back')).onClick(() => this.moveLineWindow(key, path.value, false))
        if (this.hasMoreLineWindows(key, path.value)) Button(tr('more')).onClick(() => this.moveLineWindow(key, path.value, true))
      }.width('100%')
    }
  }
  private contextRanges(item: ExtensionViewRecord): ExtensionDiffContextRange[] {
    return diffContextRanges(Wire.text(item, 'status'), this.choices(item['hunks']));
  }
  private contextRangesAt(item: ExtensionViewRecord, boundary: 'top' | 'between' | 'bottom', afterHunk: number = -1): ExtensionDiffContextRange[] {
    return this.contextRanges(item).filter((range: ExtensionDiffContextRange) => range.boundary === boundary &&
      (boundary !== 'between' || range.afterHunk === afterHunk));
  }
  private contextTitle(range: ExtensionDiffContextRange): string {
    return tr('extension_ui_load_context') + ' ' + range.side + ' ' + String(range.from) + '–' + String(range.to);
  }
  @Builder private diffContextButtons(item: ExtensionViewRecord, key: string, boundary: 'top' | 'between' | 'bottom', afterHunk: number = -1) {
    ForEach(this.contextRangesAt(item, boundary, afterHunk), (range: ExtensionDiffContextRange, index: number) =>
      this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(item, 'context'), key: key, title: this.contextTitle(range), event: 'press', role: '', summary: '', values: diffContextValues(Wire.text(item, 'path'), range) }))),
      (range: ExtensionDiffContextRange, index: number) => range.side + ':' + String(range.from) + ':' + String(range.to))
  }
  private expandRequests(item: ExtensionViewRecord): ExtensionDiffExpandRequest[] {
    return diffExpandRequests(Wire.text(item, 'path'), Wire.text(item, 'status'), this.choices(item['hunks']));
  }
  @Builder private diffExpandButtons(item: ExtensionViewRecord, key: string, hunk: number, direction: ExtensionDiffDirection) {
    ForEach(this.expandRequests(item).filter((request: ExtensionDiffExpandRequest) => request.hunk === hunk && request.direction === direction),
      (request: ExtensionDiffExpandRequest, index: number) => this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(item, 'expand'), key: key, title: tr('extension_ui_expand_diff') + ' ' + request.direction, event: 'press', role: '', summary: '', values: { path: request.path, hunk: String(request.hunk), direction: request.direction } }))),
      (request: ExtensionDiffExpandRequest, index: number) => JSON.stringify(request))
  }
  @Builder private diffView(item: ExtensionViewRecord, key: string) {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Column({ space: Tokens.fine }) {
          Text(Wire.text(item, 'path')).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).width('100%')
          if (Wire.text(item, 'old_path')) Text(Wire.text(item, 'old_path') + ' → ' + Wire.text(item, 'path')).fontSize(Tokens.meta - 1).fontColor($r('sys.color.font_secondary'))
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
        Text('+' + String(Wire.number(item, 'additions')) + ' −' + String(Wire.number(item, 'deletions'))).fontSize(Tokens.meta - 1)
      }.width('100%')
      if (Wire.bool(item, 'binary')) Text(tr('extension_ui_binary_diff')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      else if ((Wire.bool(item, 'collapsed') || !this.choices(item['hunks']).length) && Wire.has(item, 'load')) this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(item, 'load'), key: key, title: tr('extension_ui_load_diff'), event: 'press', role: '', summary: '', values: this.safeActionValues('path', Wire.text(item, 'path')) })))
      else {
        if (Wire.has(item, 'context')) this.diffContextButtons(item, key, 'top')
        ForEach(this.choices(item['hunks']), (hunk: Record<string, Object>, hunkIndex: number) => {
          if (Wire.has(item, 'context')) this.diffContextButtons(item, key, 'between', hunkIndex - 1)
          else if (Wire.has(item, 'expand')) this.diffExpandButtons(item, key, hunkIndex, 'up')
          Column({ space: Tokens.fine }) {
            if (Wire.text(hunk, 'header')) Text(Wire.text(hunk, 'header')).fontSize(Tokens.meta - 1).fontFamily('monospace').fontColor($r('sys.color.font_secondary')).width('100%')
            if (Array.isArray(hunk['lines'])) ForEach(objectValues(hunk['lines']), (line: Object, lineIndex: number) => {
              Text(diffLineText(line) || ' ').fontSize(Tokens.meta).fontFamily('monospace').wordBreak(WordBreak.BREAK_ALL).width('100%')
                .fontColor($r(diffLineText(line).charAt(0) === '+' ? 'app.color.persona_brand' : diffLineText(line).charAt(0) === '-' ? 'app.color.persona_danger' : 'sys.color.font_primary'))
            }, (line: Object, lineIndex: number) => String(lineIndex) + ':' + JSON.stringify(line))
          }.width('100%').padding(Tokens.gap).backgroundColor($r('sys.color.comp_background_secondary'))
          if (Wire.has(item, 'expand') && !Wire.has(item, 'context')) this.diffExpandButtons(item, key, hunkIndex, 'down')
        }, (_hunk: Record<string, Object>, hunkIndex: number) => String(hunkIndex))
        if (Wire.has(item, 'context')) this.diffContextButtons(item, key, 'bottom', this.choices(item['hunks']).length - 1)
      }
      if (Wire.has(item, 'mark_viewed')) Toggle({ type: ToggleType.Switch, isOn: Wire.bool(item, 'viewed') })
        .enabled(this.model.canInteract()).accessibilityText(tr('extension_ui_mark_viewed'))
        .onChange((viewed: boolean) => this.runAction(Wire.object(item, 'mark_viewed'), key, 'press',
          { path: Wire.text(item, 'path'), viewed: String(viewed) }))
      this.comments(UIUtils.makeBinding((): Object | undefined => item['threads']), key)
      if (Wire.has(item, 'comment') || Wire.has(item, 'draft')) this.commentComposer(item, key)
      this.returnedLines(key, UIUtils.makeBinding((): string => Wire.text(item, 'path')))
    }.width('100%').padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.gap)
  }
  @Builder private comments(threadsValue: Binding<Object | undefined>, key: string) {
    ForEach(records(threadsValue.value), (thread: Record<string, Object>, index: number) => {
      Column({ space: Tokens.gap }) {
        Text(tr('extension_ui_comment_line') + ' ' + String(Wire.number(thread, 'line'))).fontSize(Tokens.meta).fontWeight(FontWeight.Medium)
        ForEach(this.choices(thread['comments']), (comment: Record<string, Object>, commentIndex: number) => {
          Column({ space: Tokens.fine }) {
            Text(Wire.text(comment, 'author')).fontSize(Tokens.meta).fontWeight(FontWeight.Medium)
            MarkdownText({ text: Wire.text(comment, 'body'), onLink: (address: string) => this.link(address) })
          }.width('100%').padding(Tokens.gap).backgroundColor($r('sys.color.comp_background_secondary'))
        }, (comment: Record<string, Object>, commentIndex: number) => Wire.text(comment, 'id', String(commentIndex)))
        if (Wire.has(thread, 'reply')) {
          TextArea({ text: this.model.fieldValue(key + ':reply:' + String(index)), placeholder: tr('reply') }).height(Tokens.input)
            .onChange((value: string) => this.model.setFieldValue(key + ':reply:' + String(index), value))
          this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(thread, 'reply'), key: key, title: tr('reply'), event: 'submit', role: '', summary: Wire.text(thread, 'id'), values: threadReplyValues(Wire.text(thread, 'id'), this.model.fieldValue(key + ':reply:' + String(index))) })))
        }
        if (Wire.has(thread, 'resolve')) this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(thread, 'resolve'), key: key, title: Wire.bool(thread, 'resolved') ? tr('extension_ui_reopen_thread') : tr('extension_ui_resolve_thread'), event: 'press', role: '', summary: '', values: { thread: Wire.text(thread, 'id'), resolved: String(!Wire.bool(thread, 'resolved')) } })))
      }.width('100%')
    }, (_thread: Record<string, Object>, index: number) => String(index))
  }
  @Builder private commentComposer(item: ExtensionViewRecord, key: string) {
    Row({ space: Tokens.gap }) {
      Text(tr('extension_ui_comment_line')).fontSize(Tokens.meta)
      TextInput({ text: this.local(key + ':comment-line', '1'), placeholder: tr('extension_ui_comment_line') }).type(InputType.Number)
        .width(92).onChange((value: string) => this.setLocal(key + ':comment-line', value))
      Select(this.choiceOptions(commentSideChoices())).value(this.local(key + ':comment-side', 'new')).onSelect((index: number) => {
        const side = commentSideChoices()[index]; if (side) this.setLocal(key + ':comment-side', Wire.text(side, 'value'));
      }).layoutWeight(1)
    }.width('100%')
    TextArea({ text: this.model.fieldValue(key + ':comment'), placeholder: tr('extension_ui_comment_placeholder') }).height(Tokens.inputExpanded)
      .onChange((value: string) => this.model.setFieldValue(key + ':comment', value))
    this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.has(item, 'draft') ? Wire.object(item, 'draft') : Wire.object(item, 'comment'), key: key, title: tr('comment'), event: 'submit', role: '', summary: Wire.text(item, 'path'), values: {
      path: Wire.text(item, 'path'), side: this.local(key + ':comment-side', 'new'), line: this.local(key + ':comment-line', '1'), body: this.model.fieldValue(key + ':comment')
    } })))
  }
  @Builder private threadView(item: ExtensionViewRecord, key: string) {
    Column({ space: Tokens.gap }) {
      if (Wire.text(item, 'title')) Text(Wire.text(item, 'title')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
      ForEach(this.choices(item['comments']), (comment: Record<string, Object>, index: number) => {
        Column({ space: Tokens.fine }) {
          Text(Wire.text(comment, 'author')).fontSize(Tokens.meta).fontWeight(FontWeight.Medium)
          if (Wire.text(comment, 'body')) MarkdownText({ text: Wire.text(comment, 'body'), onLink: (address: string) => this.link(address) })
          if (Wire.has(comment, 'badge')) this.badgeView(Wire.object(comment, 'badge'))
        }.width('100%').padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_secondary'))
      }, (comment: Record<string, Object>, index: number) => Wire.text(comment, 'id', String(index)))
      if (Wire.has(item, 'reply')) {
        TextArea({ text: this.model.fieldValue(key + ':reply'), placeholder: tr('reply') }).height(Tokens.input)
          .onChange((value: string) => this.model.setFieldValue(key + ':reply', value))
        this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(item, 'reply'), key: key, title: tr('reply'), event: 'submit', role: '', summary: Wire.text(item, 'title'), values: threadReplyValues(Wire.text(item, 'id'), this.model.fieldValue(key + ':reply')) })))
      }
    }.width('100%').padding(Tokens.inset)
  }
  @Builder private composerView(item: ExtensionViewRecord, key: string) {
    Column({ space: Tokens.gap }) {
      TextArea({ text: this.model.fieldValue(key + ':' + Wire.text(item, 'name'), Wire.text(item, 'value')), placeholder: Wire.text(item, 'placeholder') })
        .height(Tokens.inputExpanded).onChange((value: string) => this.model.setFieldValue(key + ':' + Wire.text(item, 'name'), value))
      Flex({ direction: FlexDirection.Row, wrap: FlexWrap.Wrap }) {
        ForEach(this.choices(item['buttons']), (button: Record<string, Object>, index: number) => this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(button, 'action'), key: key, title: Wire.text(button, 'title'), event: 'submit', role: '', summary: Wire.text(item, 'title'), values: this.safeActionValues(Wire.text(item, 'name'), this.model.fieldValue(key + ':' + Wire.text(item, 'name'), Wire.text(item, 'value'))) }))),
          (button: Record<string, Object>, index: number) => Wire.text(Wire.object(button, 'action'), 'id', String(index)))
      }.width('100%')
    }.width('100%')
  }
  @Builder private imageView(item: ExtensionViewRecord) {
    ExtensionImage({ ui: this.model, budget: this.imageBudget, active: this.active, source: Wire.object(item, 'src'), alt: Wire.text(item, 'alt'),
      aspect: Wire.text(item, 'aspect', 'landscape'), fit: Wire.text(item, 'fit', 'fill'), imageHeight: this.compact ? 160 : 320 })
    if (Wire.text(item, 'caption')) Text(Wire.text(item, 'caption')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
  }
  @Builder private gridView(item: ExtensionViewRecord, parentKey: string) {
    Flex({ direction: FlexDirection.Row, wrap: FlexWrap.Wrap, justifyContent: FlexAlign.Start }) {
      ForEach(this.choices(item['tiles']).slice(0, this.compact ? 8 : 60), (tile: Record<string, Object>, index: number) => this.tileView(tile, this.tileKey(parentKey, tile, index)),
        (tile: Record<string, Object>, index: number) => this.tileKey(parentKey, tile, index))
    }.width('100%')
  }
  private tileKey(parentKey: string, tile: ExtensionViewRecord, index: number): string {
    return Wire.text(tile, 'key', parentKey + ':tile:' + String(index));
  }
  @Builder private carouselView(item: ExtensionViewRecord, parentKey: string) {
    if (Wire.text(item, 'title')) Text(Wire.text(item, 'title')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
    Scroll() {
      Row({ space: Tokens.gap }) {
        ForEach(this.choices(item['tiles']).slice(0, this.compact ? 6 : 20), (tile: Record<string, Object>, index: number) => this.tileView(tile, this.tileKey(parentKey, tile, index)),
          (tile: Record<string, Object>, index: number) => this.tileKey(parentKey, tile, index))
      }.alignItems(VerticalAlign.Top)
    }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Auto).width('100%')
  }
  @Builder private tileView(tile: ExtensionViewRecord, key: string) {
    Column({ space: Tokens.gap }) {
      if (Wire.has(tile, 'image')) this.thumbnailView(Wire.object(tile, 'image'))
      else if (Wire.text(tile, 'icon')) Text(Wire.text(tile, 'icon')).fontSize(Tokens.title).fontColor($r('sys.color.font_secondary'))
      Text(Wire.text(tile, 'title')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
      if (Wire.text(tile, 'subtitle')) Text(Wire.text(tile, 'subtitle')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      if (Wire.text(tile, 'value')) Text(Wire.text(tile, 'value')).fontSize(Tokens.title).width('100%')
      if (Wire.has(tile, 'badge')) this.badgeView(Wire.object(tile, 'badge'))
      if (Wire.has(tile, 'spark')) this.spark(Wire.object(tile, 'spark'))
      if (Wire.has(tile, 'control')) this.rowControl(Wire.object(tile, 'control'), key, UIUtils.makeBinding((): string => Wire.text(tile, 'title')))
      if (Wire.has(tile, 'action')) this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(tile, 'action'), key: key, title: Wire.text(tile, 'title'), event: 'press', role: '', summary: Wire.text(tile, 'title'), values: {} })))
      ForEach(this.choices(tile['actions']), (entry: Record<string, Object>, index: number) => this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(entry, 'action'), key: key, title: Wire.text(entry, 'title'), event: 'press', role: Wire.text(entry, 'role'), summary: '', values: {} }))), (entry: Record<string, Object>, index: number) => String(index))
    }.width(200).padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_tertiary'))
      .border({ width: Tokens.hairline, color: $r('sys.color.font_secondary'), radius: Tokens.gap })
  }

  @Builder private reviewBar() {
    if (Wire.has(this.renderView(), 'review')) {
      Column({ space: Tokens.gap }) {
        Row({ space: Tokens.gap }) {
          Text(Wire.text(Wire.object(this.renderView(), 'review'), 'name')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).layoutWeight(1)
          Text(String(Wire.number(Wire.object(this.renderView(), 'review'), 'count'))).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
        }.width('100%')
        TextArea({ text: this.model.fieldValue('@review'), placeholder: Wire.text(Wire.object(this.renderView(), 'review'), 'placeholder') }).height(Tokens.input)
          .onChange((value: string) => this.model.setFieldValue('@review', value))
        if (Wire.has(Wire.object(this.renderView(), 'review'), 'discard')) this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(Wire.object(this.renderView(), 'review'), 'discard'), key: this.rootKey(), title: tr('discard'), event: 'press', role: 'destructive', summary: Wire.text(Wire.object(this.renderView(), 'review'), 'name'), values: {} })))
        ForEach(this.choices(Wire.object(this.renderView(), 'review')['buttons']), (button: Record<string, Object>, index: number) => this.actionButton(UIUtils.makeBinding((): NativeActionButton => ({ action: Wire.object(button, 'action'), key: this.rootKey(), title: Wire.text(button, 'title'), event: 'submit', role: '', summary: Wire.text(Wire.object(this.renderView(), 'review'), 'name'), values: this.safeActionValues(Wire.text(Wire.object(this.renderView(), 'review'), 'name'), this.model.fieldValue('@review')) }))),
          (button: Record<string, Object>, index: number) => Wire.text(Wire.object(button, 'action'), 'id', String(index)))
      }.width('100%').padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_tertiary'))
        .border({ width: Tokens.hairline, color: $r('sys.color.font_secondary'), radius: Tokens.gap })
    }
  }

  build() {
    Column({ space: this.compact ? Tokens.gap : Tokens.page }) {
      ForEach([this.projectionGeneration], (_generation: number) => {
      if (this.projectionError) Text(tr('extension_ui_invalid_view')).fontSize(Tokens.meta).width('100%')
      else {
      this.toolbarContent()
      if (this.showTitle && Wire.text(this.renderView(), 'title')) Text(Wire.text(this.renderView(), 'title')).fontSize(Tokens.title).fontWeight(FontWeight.Bold).width('100%')
      ForEach(this.sections(), (section: ExtensionViewRecord, index: number) => this.sectionView(section, UIUtils.makeBinding((): number => this.sections().indexOf(section))),
        (section: ExtensionViewRecord, index: number) => this.projectionScope + ':' + this.sectionKey(section, index))
      if (Wire.has(this.renderView(), 'review')) this.reviewBar()
      }
      }, (generation: number) => String(generation))
    }.width('100%').padding(this.compact ? Tokens.inset : 0).alignItems(HorizontalAlign.Start)
  }
}
