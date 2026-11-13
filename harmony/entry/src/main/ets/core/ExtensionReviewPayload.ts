export type ExtensionDiffSide = 'old' | 'new';
export type ExtensionDiffDirection = 'up' | 'down';

export interface ExtensionDiffContextRange {
  side: ExtensionDiffSide;
  from: number;
  to: number | 'end';
  direction: ExtensionDiffDirection;
  afterHunk: number;
  boundary: 'top' | 'between' | 'bottom';
}

export interface ExtensionDiffExpandRequest {
  path: string;
  hunk: number;
  direction: ExtensionDiffDirection;
}

export interface ExtensionChartRow {
  series: string;
  label: string;
  value: string;
  open: string;
  high: string;
  low: string;
  close: string;
  volume: string;
}

function record(value: Object | undefined): Record<string, Object> {
  if (value === undefined || value === null || typeof value !== 'object' || Array.isArray(value)) return {};
  return value as Record<string, Object>;
}

function array(value: Object | undefined): Object[] { return Array.isArray(value) ? value as Object[] : []; }
function text(value: Record<string, Object>, name: string): string { return typeof value[name] === 'string' ? value[name] as string : ''; }
function number(value: Record<string, Object>, name: string): number { return typeof value[name] === 'number' ? value[name] as number : 0; }
function hasNumber(value: Record<string, Object>, name: string): boolean { return typeof value[name] === 'number'; }

function hunkCounts(hunkValue: Object): { oldCount: number; newCount: number; oldStart: number; newStart: number } {
  const hunk = record(hunkValue);
  let oldCount = 0, newCount = 0;
  for (const line of array(hunk['lines'])) {
    if (typeof line !== 'string') continue;
    const value = line as string;
    if (value.startsWith('\\')) continue;
    if (value.startsWith('-')) oldCount++;
    else if (value.startsWith('+')) newCount++;
    else { oldCount++; newCount++; }
  }
  return { oldCount, newCount, oldStart: Math.max(0, number(hunk, 'old_start')), newStart: Math.max(0, number(hunk, 'new_start')) };
}

function hunkEnd(start: number, count: number): number {
  if (count > 0) return start + count - 1;
  return Math.max(0, start - 1);
}

function pushRange(ranges: ExtensionDiffContextRange[], side: ExtensionDiffSide, from: number, to: number | 'end',
  direction: ExtensionDiffDirection, afterHunk: number, boundary: 'top' | 'between' | 'bottom'): void {
  if (from < 1 || typeof to === 'number' && from > to) return;
  ranges.push({ side, from, to, direction, afterHunk, boundary });
}

/** Exact hidden file-line intervals for UI v5 context requests, never the already displayed hunk. */
export function diffContextRanges(pathStatus: string, hunkValues: Object[]): ExtensionDiffContextRange[] {
  const hunks = hunkValues.map((hunk: Object) => hunkCounts(hunk));
  if (!hunks.length) return [];
  const ranges: ExtensionDiffContextRange[] = [];
  const includeOld = pathStatus !== 'added';
  const includeNew = pathStatus !== 'deleted';
  const sides: ExtensionDiffSide[] = [];
  if (includeOld) sides.push('old');
  if (includeNew) sides.push('new');

  for (const side of sides) {
    const startKey = side === 'old' ? 'oldStart' : 'newStart';
    const countKey = side === 'old' ? 'oldCount' : 'newCount';
    const firstStart = hunks[0][startKey];
    if (firstStart > 1) pushRange(ranges, side, 1, firstStart - 1, 'up', 0, 'top');
    for (let index = 1; index < hunks.length; index++) {
      const previous = hunks[index - 1], current = hunks[index];
      const from = Math.max(1, hunkEnd(previous[startKey], previous[countKey]) + 1);
      const to = current[startKey] - 1;
      if (from <= to) pushRange(ranges, side, from, to, 'down', index - 1, 'between');
    }
    const last = hunks[hunks.length - 1];
    const from = Math.max(1, hunkEnd(last[startKey], last[countKey]) + 1);
    pushRange(ranges, side, from, 'end', 'down', hunks.length - 1, 'bottom');
  }
  return ranges;
}

/** UI v4 expand requests use only the documented `up` and `down` directions. */
export function diffExpandRequests(path: string, status: string, hunkValues: Object[]): ExtensionDiffExpandRequest[] {
  const ranges = diffContextRanges(status, hunkValues), result: ExtensionDiffExpandRequest[] = [];
  const seen = new Set<string>();
  const add = (hunk: number, direction: ExtensionDiffDirection): void => {
    if (hunk < 0 || hunk >= hunkValues.length) return;
    const id = hunk + ':' + direction;
    if (seen.has(id)) return;
    seen.add(id); result.push({ path, hunk, direction });
  };
  for (const range of ranges) {
    if (range.boundary === 'top') add(0, 'up');
    else if (range.boundary === 'between') { add(range.afterHunk, 'down'); add(range.afterHunk + 1, 'up'); }
    else add(range.afterHunk, 'down');
  }
  return result;
}

export function diffContextValues(path: string, range: ExtensionDiffContextRange): Record<string, Object> {
  return { path, side: range.side, from: String(range.from), to: String(range.to) };
}

export function threadReplyValues(threadId: string, body: string): Record<string, Object> {
  const values: Record<string, Object> = { body };
  if (threadId) values['thread'] = threadId;
  return values;
}

export function multiChoiceValue(item: Record<string, Object>): string {
  const value = item['value'];
  if (Array.isArray(value)) return JSON.stringify(value);
  return typeof value === 'string' ? value as string : '';
}

export function toggleMultiChoice(raw: string, choice: string): string {
  let values: string[] = [];
  try {
    const parsed: Object = JSON.parse(raw || '[]') as Object;
    if (Array.isArray(parsed)) values = (parsed as Object[]).filter((value: Object) => typeof value === 'string').map((value: Object) => value as string);
  } catch (_) { }
  values = values.includes(choice) ? values.filter((value: string) => value !== choice) : values.concat([choice]);
  return JSON.stringify(values);
}

/** Harmony SliderChangeMode values: Begin=0, Moving=1, End=2, Click=3. */
export function sliderShouldCommit(mode: number): boolean { return mode === 2 || mode === 3; }

/** Permit a local edit during only the control's own live request; other busy states remain read-only. */
export function controlInputAllowed(disabled: boolean, active: boolean, canInteract: boolean, busy: boolean,
  ownFlight: boolean, observedBusy: boolean, errorUnchanged: boolean): boolean {
  if (disabled || !active) return false;
  if (canInteract) return true;
  return busy && ownFlight && observedBusy && errorUnchanged;
}

/** Show the requested portion of a cached file first when a server returns the whole file. */
export function lineWindowOffset(requestedStart: number, resultStart: number, lineCount: number): number {
  if (!Number.isSafeInteger(requestedStart) || !Number.isSafeInteger(resultStart) || requestedStart < 1 || resultStart < 1 || lineCount <= 0) return 0;
  const offset = requestedStart - resultStart;
  return offset >= 0 && offset < lineCount ? offset : 0;
}

export function chartRows(chartValue: Record<string, Object>): ExtensionChartRow[] {
  const kind = text(chartValue, 'kind'), unit = text(chartValue, 'unit');
  const rows: ExtensionChartRow[] = [];
  const allSeries = array(chartValue['series']);
  for (let seriesIndex = 0; seriesIndex < allSeries.length; seriesIndex++) {
    const series = record(allSeries[seriesIndex]), seriesName = text(series, 'name'), points = array(series['points']);
    for (let pointIndex = 0; pointIndex < points.length; pointIndex++) {
      const point = record(points[pointIndex]), rawValue = String(number(point, 'value')) + unit;
      const display = text(point, 'display');
      const row: ExtensionChartRow = { series: seriesName, label: text(point, 'label'), value: display ? display + ' · ' + rawValue : rawValue,
        open: '', high: '', low: '', close: '', volume: '' };
      if (kind === 'candlestick') {
        row.open = hasNumber(point, 'open') ? String(number(point, 'open')) + unit : '';
        row.high = hasNumber(point, 'high') ? String(number(point, 'high')) + unit : '';
        row.low = hasNumber(point, 'low') ? String(number(point, 'low')) + unit : '';
        row.close = hasNumber(point, 'close') ? String(number(point, 'close')) + unit : rawValue;
        row.volume = hasNumber(point, 'volume') ? String(number(point, 'volume')) : '';
      }
      rows.push(row);
    }
  }
  return rows;
}

export interface BoundedImageSize { width: number; height: number; pixels: number; }

export function boundedImageSize(width: number, height: number, maxPixels: number = 1000000, maxSide: number = 1600): BoundedImageSize {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return { width: 0, height: 0, pixels: 0 };
  const pixelScale = Math.sqrt(maxPixels / (width * height));
  const sideScale = maxSide / Math.max(width, height);
  const scale = Math.min(1, pixelScale, sideScale);
  let targetWidth = Math.max(1, Math.floor(width * scale));
  let targetHeight = Math.max(1, Math.floor(height * scale));
  while (targetWidth * targetHeight > maxPixels) {
    if (targetWidth >= targetHeight) targetWidth--;
    else targetHeight--;
  }
  return { width: targetWidth, height: targetHeight, pixels: targetWidth * targetHeight };
}

export interface ExtensionImageLease { finishLoad(): void; release(): void; }

export class ExtensionImageTicket {
  promise: Promise<ExtensionImageLease | undefined>;
  cancelled: boolean = false;
  lease: ExtensionImageLease | undefined = undefined;
  pixels: number;
  private complete: ((lease: ExtensionImageLease | undefined) => void) | undefined;
  constructor(pixels: number = 1) { this.pixels = pixels; this.promise = new Promise<ExtensionImageLease | undefined>((resolve) => { this.complete = resolve; }); }
  resolve(lease: ExtensionImageLease | undefined): void { if (this.complete) { const complete = this.complete; this.complete = undefined; complete(lease); } }
}

/** Bounds concurrent work, decoded pixels and retained image metadata per ExtensionView page. */
export class ExtensionImageBudget {
  private waiting: ExtensionImageTicket[] = [];
  private activeLoads: number = 0;
  private retainedImages: number = 0;
  private retainedPixels: number = 0;
  private maxConcurrent: number;
  private maxRetained: number;
  private maxPixels: number;
  constructor(maxConcurrent: number = 2, maxRetained: number = 64, maxPixels: number = 4000000) {
    this.maxConcurrent = Math.max(1, Math.floor(maxConcurrent)); this.maxRetained = Math.max(1, Math.floor(maxRetained));
    this.maxPixels = Math.max(1, Math.floor(maxPixels));
  }
  request(pixels: number = 1): ExtensionImageTicket {
    const requested = Number.isFinite(pixels) ? Math.max(1, Math.ceil(pixels)) : this.maxPixels + 1;
    const ticket = new ExtensionImageTicket(requested);
    let reservedPixels = this.retainedPixels;
    let liveTickets = this.retainedImages;
    for (const queued of this.waiting) if (!queued.cancelled) { reservedPixels += queued.pixels; liveTickets++; }
    if (requested > this.maxPixels || reservedPixels + requested > this.maxPixels || liveTickets >= this.maxRetained) {
      ticket.cancelled = true; ticket.resolve(undefined); return ticket;
    }
    this.waiting.push(ticket); this.pump(); return ticket;
  }
  cancel(ticket: ExtensionImageTicket): void {
    if (ticket.cancelled) return;
    ticket.cancelled = true;
    if (ticket.lease) ticket.lease.release();
    else ticket.resolve(undefined);
    this.pump();
  }
  private pump(): void {
    this.waiting = this.waiting.filter((ticket: ExtensionImageTicket) => !ticket.cancelled);
    while (this.activeLoads < this.maxConcurrent && this.retainedImages < this.maxRetained && this.waiting.length) {
      let index = -1;
      for (let waitingIndex = 0; waitingIndex < this.waiting.length; waitingIndex++) {
        const candidate = this.waiting[waitingIndex];
        if (candidate && this.retainedPixels + candidate.pixels <= this.maxPixels) { index = waitingIndex; break; }
      }
      if (index < 0) return;
      const ticket = this.waiting.splice(index, 1)[0];
      if (!ticket) return;
      if (ticket.cancelled) { ticket.resolve(undefined); continue; }
      this.activeLoads++; this.retainedImages++; this.retainedPixels += ticket.pixels;
      let released = false, finished = false;
      const lease: ExtensionImageLease = {
        finishLoad: (): void => {
          if (released || finished) return;
          finished = true; this.activeLoads--; this.pump();
        },
        release: (): void => {
          if (released) return;
          released = true;
          if (!finished) this.activeLoads--;
          this.retainedImages--; this.retainedPixels = Math.max(0, this.retainedPixels - ticket.pixels); this.pump();
        }
      };
      ticket.lease = lease; ticket.resolve(lease);
    }
  }
  counts(): { activeLoads: number; retainedImages: number; retainedPixels: number; waiting: number } {
    return { activeLoads: this.activeLoads, retainedImages: this.retainedImages, retainedPixels: this.retainedPixels,
      waiting: this.waiting.filter((ticket: ExtensionImageTicket) => !ticket.cancelled).length };
  }
}

/** At most one request per control; interactions replace the queued value until the flight settles. */
export class LatestControlValue {
  private current: string;
  private flight: boolean = false;
  private pending: string = '';
  private hasPending: boolean = false;
  private ready: boolean = false;
  constructor(value: string) { this.current = value; }
  value(): string { return this.current; }
  inFlight(): boolean { return this.flight; }
  hasQueued(): boolean { return this.hasPending; }
  queuedValue(): string { return this.pending; }
  queue(value: string, ready: boolean): void { this.pending = value; this.hasPending = true; this.ready = ready; }
  markReady(): void { if (this.hasPending) this.ready = true; }
  take(): string | undefined {
    if (this.flight || !this.hasPending || !this.ready) return undefined;
    const value = this.pending; this.pending = ''; this.hasPending = false; this.ready = false; this.flight = true; return value;
  }
  complete(value: string): string | undefined {
    this.flight = false; this.current = value;
    return this.take();
  }
  fail(serverValue: string): void { this.flight = false; this.current = serverValue; this.pending = ''; this.hasPending = false; this.ready = false; }
  discardPending(serverValue: string): void { this.current = serverValue; this.pending = ''; this.hasPending = false; this.ready = false; }
}
