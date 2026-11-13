import { ClientModel } from './ClientModel';
import { ResourceRow, Wire } from './Wire';
import { HubRepository, segment } from './HubRepository';
import { HubTransport, HttpFailure } from '../platform/HubTransport';
import { RFBClient, RFBScreenInfo, RFBCursor } from '../core/RFBClient';
import type { RFBTransport } from '../core/RFBClient';
import { RFBFramebuffer } from '../core/RFBFramebuffer';
import { RFBCredentials } from '../core/RFBAuth';
import { RFBOwnerLease } from '../core/RFBOwnerLease';

const FRAME_EDGE: number = 1920;
const MAX_TYPED_TEXT: number = 256;
const MAX_PRELUDE_BYTES: number = 2 * 1024 * 1024;

export class ComputerScreenFrame {
  width: number = 0;
  height: number = 0;
  pixels: Uint8Array = new Uint8Array(0);
  constructor(width: number = 0, height: number = 0, pixels: Uint8Array = new Uint8Array(0)) {
    this.width = width;
    this.height = height;
    this.pixels = pixels;
  }
}

class ComputerScreenRelay implements RFBTransport {
  private readonly stream: HubTransport;
  constructor(stream: HubTransport) { this.stream = stream; }
  sendBinary(bytes: ArrayBuffer): boolean { return this.stream.sendBinary(bytes); }
  close(): void { this.stream.close(); }
}

/** One visible computer screen, bound to its selected machine owner and connection generation. */
@ObservedV2
export class ComputerScreen {
  @Trace phase: string = 'idle';
  @Trace messageKey: string = 'computer_screen_waiting';
  @Trace credentialsRequested: boolean = false;
  @Trace frameRevision: number = 0;
  @Trace remoteWidth: number = 0;
  @Trace remoteHeight: number = 0;
  @Trace screenList: RFBScreenInfo[] = [];
  @Trace cursor: RFBCursor | undefined = undefined;
  @Trace clipboard: string = '';
  @Trace controlRevision: number = 0;

  private parent: ClientModel | undefined;
  private owner: ClientModel | undefined;
  private row: ResourceRow | undefined;
  private rowKey: string = '';
  private repository: HubRepository | undefined;
  private stream: HubTransport | undefined;
  private client: RFBClient | undefined;
  private lease: RFBOwnerLease | undefined;
  private parentGeneration: number = 0;
  private parentScope: string = '';
  private ownerGeneration: number = 0;
  private ownerScope: string = '';
  private attempt: number = 0;
  private frameTimer: number = -1;
  private visible: boolean = false;
  private socketReady: boolean = false;
  private credentialAnswer: ((value: RFBCredentials | undefined) => void) | undefined;

  constructor() {}

  async open(parent: ClientModel, row: ResourceRow): Promise<void> {
    this.close();
    this.parent = parent;
    this.row = row;
    this.rowKey = row.key();
    this.parentGeneration = parent.connectionGeneration();
    this.parentScope = parent.scope();
    this.visible = true;
    this.phase = 'checking';
    this.messageKey = 'computer_screen_waiting';
    this.credentialsRequested = false;
    this.attempt++;
    const attempt: number = this.attempt;

    if (!row.id || row.kind !== 'machine' || parent.demo || !parent.connected || !parent.owns(this.parentGeneration, this.parentScope)) {
      this.fail(attempt, 'computer_screen_screen_unavailable');
      return;
    }

    try {
      const owner: ClientModel = await parent.viewFor(row);
      if (!this.parentCurrent(attempt) || !owner.connected || owner.demo ||
        (row.manager && owner.profile.manager !== row.manager)) {
        this.fail(attempt, 'computer_screen_screen_unavailable');
        return;
      }
      const machine: ResourceRow | undefined = owner.machines.find((candidate: ResourceRow) => candidate.id === row.id);
      if (!machine || row.manager && machine.manager !== row.manager || !this.isMac(machine) || !this.machineCanConnect(machine)) {
        this.fail(attempt, this.isMac(machine || row) ? 'computer_screen_screen_unavailable' : 'computer_screen_not_mac');
        return;
      }

      this.owner = owner;
      this.ownerGeneration = owner.connectionGeneration();
      this.ownerScope = owner.scope();
      const repository: HubRepository = owner.repo();
      this.repository = repository;
      const lease: RFBOwnerLease = new RFBOwnerLease((): boolean => this.isCurrent(attempt));
      this.lease = lease;

      const response: Record<string, Object> = await repository.call('/api/v1/machines/' + segment(row.id) + '/computer');
      if (!this.isCurrent(attempt)) return;
      if (!Wire.bool(response, 'screen_sharing')) {
        this.fail(attempt, 'computer_screen_sharing_off');
        return;
      }

      const streamPath: string = '/api/v1/machines/' + segment(row.id) + '/ports/5900/connect';
      const stream: HubTransport = repository.transport.stream(streamPath);
      this.stream = stream;
      const pendingRFB: ArrayBuffer[] = [];
      let pendingBytes: number = 0;
      let clientStarted: boolean = false;
      const client: RFBClient = new RFBClient(
        new ComputerScreenRelay(stream),
        (): Promise<RFBCredentials | undefined> => this.askForCredentials(attempt),
        (): boolean => this.lease?.allowsInput() === true && this.isCurrent(attempt)
      );
      this.client = client;
      client.onState = (phase: string, code: string, _detail: string): void => this.protocolState(attempt, phase, code);
      client.onFrame = (_framebuffer: RFBFramebuffer): void => this.frameArrived(attempt);
      client.onResize = (width: number, height: number): void => {
        if (!this.isCurrent(attempt)) return;
        this.remoteWidth = width;
        this.remoteHeight = height;
      };
      client.onScreens = (screens: RFBScreenInfo[]): void => {
        if (this.isCurrent(attempt)) this.screenList = screens.slice();
      };
      client.onCursor = (next: RFBCursor): void => {
        if (this.isCurrent(attempt)) this.cursor = next;
      };
      client.onClipboard = (text: string): void => {
        if (this.isCurrent(attempt)) this.clipboard = text;
      };

      await stream.connect((_message: string): void => {
        if (this.isCurrent(attempt)) this.fail(attempt, 'computer_screen_unsupported');
      }, (state: string): void => {
        if ((state === 'offline' || state === 'closed') && this.socketReady && this.visible &&
          !['failed', 'auth_failed', 'sharing_off', 'closed'].includes(this.phase)) {
          if (this.isCurrent(attempt)) this.fail(attempt, 'computer_screen_connection_lost');
          else if (attempt === this.attempt) this.close();
        }
      }, (bytes: ArrayBuffer): void => {
        if (!this.isCurrent(attempt)) { if (attempt === this.attempt) this.close(); return; }
        if (clientStarted) { client.receive(bytes); return; }
        if (pendingBytes + bytes.byteLength > MAX_PRELUDE_BYTES) { this.fail(attempt, 'computer_screen_unsupported'); return; }
        const copy: ArrayBuffer = new ArrayBuffer(bytes.byteLength);
        new Uint8Array(copy).set(new Uint8Array(bytes));
        pendingRFB.push(copy);
        pendingBytes += copy.byteLength;
      });
      if (!this.isCurrent(attempt)) { if (attempt === this.attempt) this.close(); return; }
      this.socketReady = true;
      const work: Promise<void> = client.start();
      clientStarted = true;
      pendingRFB.forEach((bytes: ArrayBuffer) => client.receive(bytes));
      pendingRFB.length = 0;
      pendingBytes = 0;
      await work;
    } catch (error) {
      if (!this.parentCurrent(attempt)) { if (attempt === this.attempt) this.close(); return; }
      const failure: Error = error as Error;
      if (failure instanceof HttpFailure && failure.status === 403) this.fail(attempt, 'computer_screen_read_only_device');
      else if (failure instanceof HttpFailure && failure.status === 404) this.fail(attempt, 'computer_screen_screen_unavailable');
      else if (failure.message === 'connection_closed' || failure.message === 'client_closed') this.fail(attempt, 'computer_screen_connection_lost');
      else this.fail(attempt, 'computer_screen_connection_failed');
    }
  }

  retry(): void {
    const parent: ClientModel | undefined = this.parent;
    const row: ResourceRow | undefined = this.row;
    if (!parent || !row || !this.visible) return;
    this.open(parent, row).catch((): void => this.fail(this.attempt, 'computer_screen_connection_failed'));
  }

  close(): void {
    this.visible = false;
    this.attempt++;
    this.socketReady = false;
    if (this.frameTimer !== -1) { clearTimeout(this.frameTimer); this.frameTimer = -1; }
    const answer: ((value: RFBCredentials | undefined) => void) | undefined = this.credentialAnswer;
    this.credentialAnswer = undefined;
    this.credentialsRequested = false;
    answer?.(undefined);
    this.lease?.revoke();
    this.lease = undefined;
    this.client?.close();
    this.client = undefined;
    this.stream?.close();
    this.stream = undefined;
    this.repository = undefined;
    this.owner = undefined;
    this.parent = undefined;
    this.row = undefined;
    this.rowKey = '';
    this.remoteWidth = 0;
    this.remoteHeight = 0;
    this.screenList = [];
    this.cursor = undefined;
    this.clipboard = '';
    this.phase = 'closed';
    this.frameRevision++;
    this.controlRevision++;
  }

  hide(): void { this.close(); }

  isLive(): boolean { return this.phase === 'live' && this.isCurrent(this.attempt); }

  validateOwner(): boolean {
    if (!this.visible) return false;
    if (!this.owner) {
      if (this.parentCurrent(this.attempt)) return true;
      this.close();
      return false;
    }
    if (this.isCurrent(this.attempt)) return true;
    this.close();
    return false;
  }

  controlAllowed(): boolean { return this.isCurrent(this.attempt) && this.lease?.allowsInput() === true; }

  connectionRevision(): number { return this.attempt; }

  grantControl(): boolean {
    if (!this.isLive() || !this.lease?.grant()) return false;
    this.controlRevision++;
    return true;
  }

  releaseControl(): void {
    if (this.controlAllowed()) this.client?.releasePointer();
    this.lease?.disable();
    this.controlRevision++;
  }

  refresh(): void {
    if (this.isLive()) this.client?.refresh();
    else this.retry();
  }

  pointerFromView(x: number, y: number, viewWidth: number, viewHeight: number, buttons: number): boolean {
    if (!this.controlAllowed() || this.remoteWidth <= 0 || this.remoteHeight <= 0 || viewWidth <= 0 || viewHeight <= 0) return false;
    const scale: number = Math.min(viewWidth / this.remoteWidth, viewHeight / this.remoteHeight);
    if (!Number.isFinite(scale) || scale <= 0) return false;
    const contentWidth: number = this.remoteWidth * scale;
    const contentHeight: number = this.remoteHeight * scale;
    const offsetX: number = (viewWidth - contentWidth) / 2;
    const offsetY: number = (viewHeight - contentHeight) / 2;
    const localX: number = x - offsetX;
    const localY: number = y - offsetY;
    if (localX < 0 || localY < 0 || localX >= contentWidth || localY >= contentHeight) return false;
    const pixelX: number = Math.min(this.remoteWidth - 1, Math.floor(localX / scale));
    const pixelY: number = Math.min(this.remoteHeight - 1, Math.floor(localY / scale));
    return this.client?.pointer(pixelX, pixelY, buttons) || false;
  }

  releasePointer(): void { if (this.controlAllowed()) this.client?.releasePointer(); }

  pressKey(keysym: number): boolean { return this.controlAllowed() && (this.client?.pressKey(keysym) || false); }

  async sendText(text: string): Promise<boolean> {
    if (!text || text.length > MAX_TYPED_TEXT || !this.controlAllowed()) return false;
    const attempt: number = this.attempt;
    const client: RFBClient | undefined = this.client;
    const lease: RFBOwnerLease | undefined = this.lease;
    const repository: HubRepository | undefined = this.repository;
    const row: ResourceRow | undefined = this.row;
    if (!client || !lease || !repository || !row) return false;
    let needsClipboard: boolean = false;
    for (let at: number = 0; at < text.length; at++) {
      const codepoint: number = text.codePointAt(at) || 0;
      if (codepoint > 65535) at++;
      if (codepoint > 255) needsClipboard = true;
    }
    if (!needsClipboard) return client.typeText(text);
    try {
      await repository.transport.request('/api/v1/machines/' + segment(row.id) + '/computer/clipboard', 'POST', JSON.stringify(Wire.payload(['text'], [text])));
    } catch (_) { return false; }
    if (!this.isCurrent(attempt) || this.client !== client || this.lease !== lease || !lease.allowsInput()) return false;
    return client.pressWithModifiers(0x76, [0xffeb]);
  }

  frameSnapshot(maxEdge: number = FRAME_EDGE): ComputerScreenFrame {
    const client: RFBClient | undefined = this.client;
    if (!client || !this.isLive() || maxEdge < 1) return new ComputerScreenFrame();
    const framebuffer: RFBFramebuffer = client.framebuffer;
    const source: Uint8Array = framebuffer.pixels;
    const scale: number = Math.min(1, maxEdge / Math.max(framebuffer.width, framebuffer.height));
    const width: number = Math.max(1, Math.floor(framebuffer.width * scale));
    const height: number = Math.max(1, Math.floor(framebuffer.height * scale));
    const pixels: Uint8Array = new Uint8Array(width * height * 4);
    for (let y: number = 0; y < height; y++) {
      const sourceY: number = Math.min(framebuffer.height - 1, Math.floor(y / scale));
      for (let x: number = 0; x < width; x++) {
        const sourceX: number = Math.min(framebuffer.width - 1, Math.floor(x / scale));
        const sourceAt: number = (sourceY * framebuffer.width + sourceX) * 4;
        const targetAt: number = (y * width + x) * 4;
        pixels[targetAt] = source[sourceAt];
        pixels[targetAt + 1] = source[sourceAt + 1];
        pixels[targetAt + 2] = source[sourceAt + 2];
        pixels[targetAt + 3] = source[sourceAt + 3];
      }
    }
    return new ComputerScreenFrame(width, height, pixels);
  }

  private askForCredentials(attempt: number): Promise<RFBCredentials | undefined> {
    if (!this.isCurrent(attempt) || this.credentialAnswer) return Promise.resolve(undefined);
    this.phase = 'credentials';
    this.messageKey = 'computer_screen_enter_credentials';
    this.credentialsRequested = true;
    return new Promise<RFBCredentials | undefined>((resolve) => { this.credentialAnswer = resolve; });
  }

  submitCredentials(account: string, password: string): boolean {
    if (!this.credentialsRequested || !this.isCurrent(this.attempt)) return false;
    const cleanAccount: string = account.trim();
    if (!cleanAccount || cleanAccount.length > 256 || password.length === 0 || password.length > 256) return false;
    const answer: ((value: RFBCredentials | undefined) => void) | undefined = this.credentialAnswer;
    this.credentialAnswer = undefined;
    this.credentialsRequested = false;
    this.phase = 'connecting';
    this.messageKey = 'computer_screen_waiting';
    answer?.(new RFBCredentials(cleanAccount, password));
    return answer !== undefined;
  }

  cancelCredentials(): void {
    const answer: ((value: RFBCredentials | undefined) => void) | undefined = this.credentialAnswer;
    this.credentialAnswer = undefined;
    this.credentialsRequested = false;
    this.fail(this.attempt, 'computer_screen_auth_failed');
    answer?.(undefined);
  }

  private protocolState(attempt: number, phase: string, code: string): void {
    if (!this.isCurrent(attempt)) { if (attempt === this.attempt) this.close(); return; }
    if (this.phase === 'failed' || this.phase === 'auth_failed' || this.phase === 'sharing_off' || this.phase === 'closed') return;
    if (phase === 'live') {
      this.phase = 'live';
      this.messageKey = 'computer_screen_read_only';
      this.remoteWidth = this.client?.framebuffer.width || 0;
      this.remoteHeight = this.client?.framebuffer.height || 0;
      return;
    }
    if (phase === 'credentials') {
      this.phase = 'credentials';
      this.messageKey = 'computer_screen_enter_credentials';
      this.credentialsRequested = true;
      return;
    }
    if (phase === 'connecting') {
      this.phase = 'connecting';
      this.messageKey = 'computer_screen_waiting';
      return;
    }
    if (phase === 'closed' || phase === 'failed') {
      if (code === 'auth_failed') this.fail(attempt, 'computer_screen_auth_failed');
      else if (code === 'unsupported') this.fail(attempt, 'computer_screen_unsupported');
      else if (code === 'connection_lost') this.fail(attempt, 'computer_screen_connection_lost');
      else this.fail(attempt, 'computer_screen_connection_failed');
    }
  }

  private frameArrived(attempt: number): void {
    if (!this.isCurrent(attempt)) { if (attempt === this.attempt) this.close(); return; }
    if (this.frameTimer !== -1) return;
    this.frameTimer = setTimeout((): void => {
      this.frameTimer = -1;
      if (this.isCurrent(attempt)) this.frameRevision++;
    }, 100);
  }

  private fail(attempt: number, messageKey: string): void {
    if (attempt !== this.attempt) return;
    this.attempt++;
    this.phase = messageKey === 'computer_screen_sharing_off' ? 'sharing_off' : messageKey === 'computer_screen_auth_failed' ? 'auth_failed' : 'failed';
    this.messageKey = messageKey;
    this.credentialsRequested = false;
    this.socketReady = false;
    this.lease?.revoke();
    if (this.frameTimer !== -1) { clearTimeout(this.frameTimer); this.frameTimer = -1; }
    this.remoteWidth = 0;
    this.remoteHeight = 0;
    this.screenList = [];
    this.cursor = undefined;
    this.clipboard = '';
    this.frameRevision++;
    const answer: ((value: RFBCredentials | undefined) => void) | undefined = this.credentialAnswer;
    this.credentialAnswer = undefined;
    answer?.(undefined);
    const client: RFBClient | undefined = this.client;
    this.client = undefined;
    client?.close();
    const stream: HubTransport | undefined = this.stream;
    this.stream = undefined;
    stream?.close();
    this.controlRevision++;
  }

  private parentCurrent(attempt: number): boolean {
    const parent: ClientModel | undefined = this.parent;
    const row: ResourceRow | undefined = this.row;
    return attempt === this.attempt && this.visible && !!parent && !!row && row.key() === this.rowKey &&
      parent.connected && !parent.demo && parent.owns(this.parentGeneration, this.parentScope);
  }

  private isCurrent(attempt: number): boolean {
    if (!this.parentCurrent(attempt)) return false;
    const owner: ClientModel | undefined = this.owner;
    const row: ResourceRow | undefined = this.row;
    const repository: HubRepository | undefined = this.repository;
    if (!owner || !row || !repository || !owner.connected || owner.demo || !owner.owns(this.ownerGeneration, this.ownerScope)) return false;
    try {
      if (owner.repo() !== repository) return false;
    } catch (_) { return false; }
    const machine: ResourceRow | undefined = owner.machines.find((candidate: ResourceRow) => candidate.id === row.id);
    return !!machine && (!row.manager || machine.manager === row.manager) && this.machineCanConnect(machine);
  }

  private machineCanConnect(machine: ResourceRow): boolean {
    const data: Record<string, Object> = machine.data();
    const trust: string = Wire.text(data, 'trust');
    return Wire.bool(data, 'online') && !Wire.bool(data, 'pending') && !Wire.bool(data, 'pending_confirm') &&
      ['', 'confirmed', 'personal', 'work'].includes(trust);
  }

  private isMac(machine: ResourceRow): boolean {
    const data: Record<string, Object> = machine.data();
    const system: Record<string, Object> = Wire.object(data, 'system');
    const os: string = Wire.text(data, 'os', Wire.text(system, 'os'));
    return !os || os === 'mac' || os === 'darwin';
  }
}
