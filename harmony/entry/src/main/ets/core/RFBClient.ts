import { RFBARDReply, RFBAuth, RFBCredentials } from './RFBAuth';
import { RFBFramebuffer } from './RFBFramebuffer';
import { RFBZRLE } from './RFBZRLE';
import { util } from '@kit.ArkTS';

const MAX_READ_BYTES: number = 72 * 1024 * 1024;
const MAX_QUEUED_BYTES: number = 8 * 1024 * 1024;
const MAX_INFLATED_BYTES: number = 72 * 1024 * 1024;
const MAX_RECTS: number = 4096;
const MAX_TEXT_BYTES: number = 1024 * 1024;
const MAX_REASON_BYTES: number = 4096;
const MAX_SCREENS: number = 64;

export interface RFBTransport {
  sendBinary(bytes: ArrayBuffer): boolean;
  close(): void;
}

export interface RFBInflater {
  inflate(compressed: Uint8Array, outputLimit: number): Promise<Uint8Array>;
  close(): void;
}

export class RFBScreenInfo {
  id: number = 0;
  x: number = 0;
  y: number = 0;
  width: number = 0;
  height: number = 0;
  constructor(id: number, x: number, y: number, width: number, height: number) {
    this.id = id; this.x = x; this.y = y; this.width = width; this.height = height;
  }
}

export class RFBCursor {
  width: number = 0;
  height: number = 0;
  hotX: number = 0;
  hotY: number = 0;
  pixels: Uint8Array = new Uint8Array(0);
  constructor(width: number, height: number, hotX: number, hotY: number, pixels: Uint8Array) {
    this.width = width; this.height = height; this.hotX = hotX; this.hotY = hotY; this.pixels = pixels;
  }
}

class RFBIssue extends Error {
  code: string = '';
  detail: string = '';
  constructor(code: string, detail: string = '') { super(code); this.code = code; this.detail = detail; }
}

/** Buffers WebSocket messages as one ordered TCP byte stream, consuming large RFB payloads incrementally. */
class RFBReader {
  private chunks: Uint8Array[] = [];
  private firstOffset: number = 0;
  private queuedBytes: number = 0;
  private waiter: (() => void) | undefined;
  private closed: boolean = false;

  push(input: ArrayBuffer): void {
    if (this.closed) return;
    if (input.byteLength === 0) return;
    if (input.byteLength > 1024 * 1024 || this.queuedBytes + input.byteLength > MAX_QUEUED_BYTES) throw new RFBIssue('rfb_input_limit');
    const copy = new Uint8Array(input.byteLength);
    copy.set(new Uint8Array(input));
    this.chunks.push(copy);
    this.queuedBytes += copy.length;
    const wake: (() => void) | undefined = this.waiter;
    this.waiter = undefined;
    wake?.();
  }

  close(): void {
    this.closed = true;
    const wake: (() => void) | undefined = this.waiter;
    this.waiter = undefined;
    wake?.();
  }

  async bytes(length: number): Promise<Uint8Array> {
    if (!Number.isInteger(length) || length < 0 || length > MAX_READ_BYTES) throw new RFBIssue('rfb_input_limit');
    const output = new Uint8Array(length);
    let written: number = 0;
    while (written < length) {
      while (this.chunks.length === 0) {
        if (this.closed) throw new RFBIssue('rfb_connection_lost');
        await new Promise<void>((resolve) => { this.waiter = resolve; });
      }
      const chunk: Uint8Array = this.chunks[0];
      const available: number = chunk.length - this.firstOffset;
      const take: number = Math.min(available, length - written);
      output.set(chunk.subarray(this.firstOffset, this.firstOffset + take), written);
      written += take;
      this.firstOffset += take;
      this.queuedBytes -= take;
      if (this.firstOffset === chunk.length) { this.chunks.shift(); this.firstOffset = 0; }
    }
    return output;
  }

  async u8(): Promise<number> { return (await this.bytes(1))[0]; }
  async u16(): Promise<number> { const bytes: Uint8Array = await this.bytes(2); return bytes[0] * 256 + bytes[1]; }
  async u32(): Promise<number> {
    const bytes: Uint8Array = await this.bytes(4);
    return bytes[0] * 16777216 + bytes[1] * 65536 + bytes[2] * 256 + bytes[3];
  }
  async s32(): Promise<number> { const value: number = await this.u32(); return value >= 2147483648 ? value - 4294967296 : value; }
}

function arrayBuffer(bytes: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(bytes.length);
  copy.set(bytes);
  return copy.buffer;
}

function append(target: Uint8Array, extra: Uint8Array): Uint8Array {
  const result = new Uint8Array(target.length + extra.length);
  result.set(target, 0); result.set(extra, target.length);
  return result;
}

function numberBytes(value: number, count: number): Uint8Array {
  const result = new Uint8Array(count);
  let remainder: number = value;
  for (let index: number = count - 1; index >= 0; index--) {
    result[index] = remainder & 255;
    remainder = Math.floor(remainder / 256);
  }
  return result;
}

function latin1(bytes: Uint8Array): string {
  let result: string = '';
  for (let at: number = 0; at < bytes.length; at += 1024) {
    const end: number = Math.min(bytes.length, at + 1024);
    let part: string = '';
    for (let index: number = at; index < end; index++) part += String.fromCharCode(bytes[index]);
    result += part;
  }
  return result;
}

/** RFB 3.8 client: Apple Remote Desktop auth, raw/CopyRect/ZRLE and native pointer/key input. */
export class RFBClient {
  readonly framebuffer: RFBFramebuffer = new RFBFramebuffer(1, 1);
  readonly transport: RFBTransport;
  private readonly reader: RFBReader = new RFBReader();
  private readonly credentials: () => Promise<RFBCredentials | undefined>;
  private readonly canControl: () => boolean;
  private readonly zrle: RFBInflater;
  private active: boolean = false;
  private started: boolean = false;
  private live: boolean = false;
  private work: Promise<void> | undefined;
  private pointerX: number = 0;
  private pointerY: number = 0;
  private pointerButtons: number = 0;

  onState: (phase: string, code: string, detail: string) => void = (_phase: string, _code: string, _detail: string) => {};
  onFrame: (framebuffer: RFBFramebuffer) => void = (_framebuffer: RFBFramebuffer) => {};
  onClipboard: (text: string) => void = (_text: string) => {};
  onResize: (width: number, height: number) => void = (_width: number, _height: number) => {};
  onScreens: (screens: RFBScreenInfo[]) => void = (_screens: RFBScreenInfo[]) => {};
  onCursor: (cursor: RFBCursor) => void = (_cursor: RFBCursor) => {};
  serverName: string = '';

  constructor(transport: RFBTransport, credentials: () => Promise<RFBCredentials | undefined>, canControl: () => boolean,
    inflater: RFBInflater = new RFBZRLE()) {
    this.transport = transport;
    this.credentials = credentials;
    this.canControl = canControl;
    this.zrle = inflater;
  }

  start(): Promise<void> {
    if (this.started) return this.work || Promise.resolve();
    this.started = true;
    this.active = true;
    this.work = this.run();
    return this.work;
  }

  receive(bytes: ArrayBuffer): void {
    if (!this.active) return;
    try { this.reader.push(bytes); }
    catch (error) { this.fail(error as Error); }
  }

  close(): void {
    if (!this.active && this.started) return;
    this.active = false;
    this.live = false;
    this.reader.close();
    this.transport.close();
    this.onState('closed', '', '');
  }

  refresh(): boolean {
    if (!this.active || !this.live) return false;
    return this.sendRequest(false);
  }

  pointer(x: number, y: number, buttons: number): boolean {
    if (!this.inputAllowed() || !Number.isInteger(x) || !Number.isInteger(y)) return false;
    const safeX: number = Math.min(this.framebuffer.width - 1, Math.max(0, x));
    const safeY: number = Math.min(this.framebuffer.height - 1, Math.max(0, y));
    this.pointerX = safeX; this.pointerY = safeY;
    this.pointerButtons = buttons & 31;
    const packet = new Uint8Array([5, this.pointerButtons, (safeX >>> 8) & 255, safeX & 255, (safeY >>> 8) & 255, safeY & 255]);
    return this.send(packet);
  }

  releasePointer(): boolean {
    if (!this.inputAllowed() || this.pointerButtons === 0) return false;
    this.pointerButtons = 0;
    return this.send(new Uint8Array([5, 0, (this.pointerX >>> 8) & 255, this.pointerX & 255,
      (this.pointerY >>> 8) & 255, this.pointerY & 255]));
  }

  key(keysym: number, down: boolean): boolean {
    if (!this.inputAllowed() || !Number.isInteger(keysym) || keysym < 0 || keysym > 4294967295) return false;
    return this.send(new Uint8Array([4, down ? 1 : 0, 0, 0, ...RFBAuth.be32(keysym)]));
  }

  pressKey(keysym: number): boolean {
    if (!this.inputAllowed() || !Number.isInteger(keysym) || keysym < 0 || keysym > 4294967295) return false;
    const down = new Uint8Array([4, 1, 0, 0, ...RFBAuth.be32(keysym)]);
    const up = new Uint8Array([4, 0, 0, 0, ...RFBAuth.be32(keysym)]);
    return this.send(append(down, up));
  }

  pressWithModifiers(keysym: number, modifiers: number[]): boolean {
    if (!this.inputAllowed() || !Number.isInteger(keysym) || keysym < 0 || keysym > 4294967295 || modifiers.length > 8) return false;
    for (const modifier of modifiers) if (!Number.isInteger(modifier) || modifier < 0 || modifier > 4294967295) return false;
    const packets: number[] = [];
    for (const modifier of modifiers) packets.push(4, 1, 0, 0, ...RFBAuth.be32(modifier));
    packets.push(4, 1, 0, 0, ...RFBAuth.be32(keysym), 4, 0, 0, 0, ...RFBAuth.be32(keysym));
    for (let index: number = modifiers.length - 1; index >= 0; index--) packets.push(4, 0, 0, 0, ...RFBAuth.be32(modifiers[index]));
    return this.send(new Uint8Array(packets));
  }

  typeText(text: string): boolean {
    if (!this.inputAllowed() || text.length === 0 || text.length > 256) return false;
    const packets: number[] = [];
    let at: number = 0;
    let characters: number = 0;
    while (at < text.length) {
      let codepoint: number = text.charCodeAt(at);
      at++;
      if (codepoint >= 55296 && codepoint <= 56319 && at < text.length) {
        const low: number = text.charCodeAt(at);
        if (low >= 56320 && low <= 57343) { codepoint = 65536 + ((codepoint - 55296) * 1024) + low - 56320; at++; }
      }
      let keysym: number = codepoint;
      if (codepoint === 10 || codepoint === 13) keysym = 65293;
      else if (codepoint === 9) keysym = 65289;
      else if (codepoint < 32 || codepoint === 127) continue;
      else if (codepoint > 255) return false;
      packets.push(4, 1, 0, 0, ...RFBAuth.be32(keysym), 4, 0, 0, 0, ...RFBAuth.be32(keysym));
      characters++;
      if (characters > 256) return false;
    }
    return packets.length === 0 || this.send(new Uint8Array(packets));
  }

  private async run(): Promise<void> {
    this.onState('connecting', '', '');
    try {
      await this.handshake();
      while (this.active) {
        const message: number = await this.reader.u8();
        if (message === 0) await this.update();
        else if (message === 1) await this.colorMap();
        else if (message === 2) { /* bell */ }
        else if (message === 3) await this.serverClipboard();
        else throw new RFBIssue('unsupported', 'rfb_unknown_message');
      }
    } catch (error) {
      if (this.active) this.fail(error as Error);
    } finally {
      this.active = false;
      this.live = false;
      this.reader.close();
      this.zrle.close();
      this.transport.close();
    }
  }

  private async handshake(): Promise<void> {
    const versionBytes: Uint8Array = await this.reader.bytes(12);
    let version: string = '';
    for (let index: number = 0; index < versionBytes.length; index++) version += String.fromCharCode(versionBytes[index]);
    if (!/^RFB 003\.(008|889)\n$/.test(version)) throw new RFBIssue('unsupported', 'rfb_version');
    if (!this.send(new Uint8Array([82, 70, 66, 32, 48, 48, 51, 46, 48, 48, 56, 10]))) throw new RFBIssue('connection_lost');
    const count: number = await this.reader.u8();
    if (count === 0) throw new RFBIssue('auth_failed', await this.reason());
    if (count > 32) throw new RFBIssue('unsupported', 'rfb_security_limit');
    const offered: Uint8Array = await this.reader.bytes(count);
    let selected: number = 0;
    for (const candidate of RFBAuth.preferred) if (offered.includes(candidate)) { selected = candidate; break; }
    if (selected === 0) throw new RFBIssue('unsupported', 'rfb_security');
    if (!this.send(new Uint8Array([selected]))) throw new RFBIssue('connection_lost');
    if (selected === RFBAuth.securityARD) await this.ard();
    else if (selected === RFBAuth.veNCrypt) await this.veNCrypt();
    const result: number = await this.reader.u32();
    if (result !== 0) throw new RFBIssue('auth_failed', await this.reason());
    if (!this.send(new Uint8Array([1]))) throw new RFBIssue('connection_lost');
    const width: number = await this.reader.u16();
    const height: number = await this.reader.u16();
    if (!RFBFramebuffer.validSize(width, height)) throw new RFBIssue('unsupported', 'rfb_size_limit');
    await this.reader.bytes(16);
    const nameLength: number = await this.reader.u32();
    if (nameLength > MAX_REASON_BYTES) throw new RFBIssue('unsupported', 'rfb_name_limit');
    this.serverName = util.TextDecoder.create('utf-8').decodeToString(await this.reader.bytes(nameLength));
    this.framebuffer.resize(width, height);
    const pixelFormat = new Uint8Array([0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0]);
    if (!this.send(pixelFormat)) throw new RFBIssue('connection_lost');
    const encodings: number[] = [16, 1, 0, -223, -308, -224];
    const setEncodings: Uint8Array = new Uint8Array(4 + encodings.length * 4);
    setEncodings[0] = 2; setEncodings[1] = 0;
    setEncodings[2] = 0; setEncodings[3] = encodings.length;
    for (let index: number = 0; index < encodings.length; index++) setEncodings.set(numberBytes(encodings[index] >>> 0, 4), 4 + index * 4);
    if (!this.send(setEncodings)) throw new RFBIssue('connection_lost');
    this.live = true;
    this.onState('live', '', '');
    if (!this.sendRequest(false)) throw new RFBIssue('connection_lost');
  }

  private async ard(): Promise<void> {
    const generator: Uint8Array = await this.reader.bytes(2);
    const length: number = await this.reader.u16();
    if (length < 1 || length > RFBAuth.maxPrimeBytes) throw new RFBIssue('unsupported', 'rfb_prime_limit');
    const prime: Uint8Array = await this.reader.bytes(length);
    const serverKey: Uint8Array = await this.reader.bytes(length);
    const credentials: RFBCredentials = await this.askForCredentials();
    try {
      const reply: RFBARDReply = await RFBAuth.ard(generator, prime, serverKey, credentials.account, credentials.password);
      if (!this.send(append(reply.sealed, reply.publicKey))) throw new RFBIssue('connection_lost');
    }
    finally { credentials.account = ''; credentials.password = ''; }
  }

  private async veNCrypt(): Promise<void> {
    const version: Uint8Array = await this.reader.bytes(2);
    if (version[0] !== 0 || version[1] !== 2) throw new RFBIssue('unsupported', 'rfb_vencrypt_version');
    if (!this.send(new Uint8Array([0, 2]))) throw new RFBIssue('connection_lost');
    if (await this.reader.u8() !== 0) throw new RFBIssue('auth_failed', 'rfb_vencrypt_refused');
    const count: number = await this.reader.u8();
    if (count < 1 || count > 64) throw new RFBIssue('unsupported', 'rfb_vencrypt_limit');
    let plainAvailable: boolean = false;
    for (let index: number = 0; index < count; index++) if (await this.reader.u32() === RFBAuth.plain) plainAvailable = true;
    if (!plainAvailable) throw new RFBIssue('unsupported', 'rfb_vencrypt_plain');
    if (!this.send(RFBAuth.be32(RFBAuth.plain))) throw new RFBIssue('connection_lost');
    const credentials: RFBCredentials = await this.askForCredentials();
    const encoder = util.TextEncoder.create('utf-8');
    const accountBytes: Uint8Array = encoder.encodeInto(credentials.account);
    const passwordBytes: Uint8Array = encoder.encodeInto(credentials.password);
    if (accountBytes.length > 4096 || passwordBytes.length > 4096) {
      accountBytes.fill(0); passwordBytes.fill(0); credentials.account = ''; credentials.password = '';
      throw new RFBIssue('unsupported', 'rfb_credential_limit');
    }
    const payload: Uint8Array = RFBAuth.plainCredentials(credentials.account, credentials.password);
    accountBytes.fill(0); passwordBytes.fill(0); credentials.account = ''; credentials.password = '';
    const sent: boolean = this.send(payload);
    payload.fill(0);
    if (!sent) throw new RFBIssue('connection_lost');
  }

  private async askForCredentials(): Promise<RFBCredentials> {
    this.onState('credentials', '', '');
    const value: RFBCredentials | undefined = await this.credentials();
    if (!this.active || !value) throw new RFBIssue('auth_failed', 'rfb_credentials_cancelled');
    return value;
  }

  private async reason(): Promise<string> {
    const length: number = await this.reader.u32();
    if (length > MAX_REASON_BYTES) throw new RFBIssue('unsupported', 'rfb_reason_limit');
    return util.TextDecoder.create('utf-8').decodeToString(await this.reader.bytes(length));
  }

  private request(incremental: boolean): boolean {
    if (this.framebuffer.width < 1 || this.framebuffer.height < 1) return false;
    const width: number = this.framebuffer.width, height: number = this.framebuffer.height;
    return this.send(new Uint8Array([3, incremental ? 1 : 0, 0, 0, 0, 0, (width >>> 8) & 255, width & 255, (height >>> 8) & 255, height & 255]));
  }

  private sendRequest(incremental: boolean): boolean { return this.request(incremental); }

  private async update(): Promise<void> {
    await this.reader.bytes(1);
    const count: number = await this.reader.u16();
    if (count !== 65535 && count > MAX_RECTS) throw new RFBIssue('unsupported', 'rfb_rect_limit');
    let changed: boolean = false;
    for (let index: number = 0; index < count; index++) {
      const x: number = await this.reader.u16();
      const y: number = await this.reader.u16();
      const width: number = await this.reader.u16();
      const height: number = await this.reader.u16();
      const encoding: number = await this.reader.s32();
      if (encoding === -224) break;
      if (index >= MAX_RECTS) throw new RFBIssue('unsupported', 'rfb_rect_limit');
      if (encoding === -223) {
        if (!RFBFramebuffer.validSize(width, height)) throw new RFBIssue('unsupported', 'rfb_size_limit');
        this.framebuffer.resize(width, height);
        this.onResize(width, height);
        changed = true;
        continue;
      }
      if (encoding === -308) {
        const screens: RFBScreenInfo[] = await this.extendedScreens(x, y);
        if (y === 0) {
          if (!RFBFramebuffer.validSize(width, height)) throw new RFBIssue('unsupported', 'rfb_size_limit');
          if (width !== this.framebuffer.width || height !== this.framebuffer.height) {
            this.framebuffer.resize(width, height);
            this.onResize(width, height);
            changed = true;
          }
          this.onScreens(screens);
        }
        continue;
      }
      if (encoding === -239) {
        await this.readCursor(x, y, width, height);
        continue;
      }
      this.framebuffer.requireRect(x, y, width, height);
      if (encoding === 0) {
        const size: number = width * height * 4;
        if (size > RFBFramebuffer.maxBytes) throw new RFBIssue('unsupported', 'rfb_rect_limit');
        this.framebuffer.raw(x, y, width, height, await this.reader.bytes(size));
      } else if (encoding === 1) {
        const sourceX: number = await this.reader.u16();
        const sourceY: number = await this.reader.u16();
        this.framebuffer.copy(x, y, width, height, sourceX, sourceY);
      } else if (encoding === 16) {
        const compressedLength: number = await this.reader.u32();
        if (compressedLength < 1 || compressedLength > 1024 * 1024) throw new RFBIssue('unsupported', 'rfb_rect_limit');
        const pixels: number = width * height;
        const tiles: number = Math.ceil(width / 64) * Math.ceil(height / 64);
        const outputLimit: number = Math.min(MAX_INFLATED_BYTES, pixels * 4 + tiles * 384 + 1024);
        const inflated: Uint8Array = await this.zrle.inflate(await this.reader.bytes(compressedLength), outputLimit);
        this.framebuffer.drawZRLE(inflated, x, y, width, height);
      } else throw new RFBIssue('unsupported', 'rfb_encoding');
      changed = true;
    }
    if (changed) this.onFrame(this.framebuffer);
    if (this.active && !this.sendRequest(true)) throw new RFBIssue('connection_lost');
  }

  private async extendedScreens(_reason: number, _status: number): Promise<RFBScreenInfo[]> {
    const count: number = await this.reader.u8();
    await this.reader.bytes(3);
    if (count > MAX_SCREENS) throw new RFBIssue('unsupported', 'rfb_screen_limit');
    const screens: RFBScreenInfo[] = [];
    for (let index: number = 0; index < count; index++) {
      const id: number = await this.reader.u32();
      const x: number = await this.reader.u16();
      const y: number = await this.reader.u16();
      const width: number = await this.reader.u16();
      const height: number = await this.reader.u16();
      await this.reader.u32();
      if (!RFBFramebuffer.validSize(width, height)) continue;
      screens.push(new RFBScreenInfo(id, x, y, width, height));
    }
    return screens;
  }

  private async readCursor(hotX: number, hotY: number, width: number, height: number): Promise<void> {
    const count: number = width * height;
    if (count > 262144) throw new RFBIssue('unsupported', 'rfb_cursor_limit');
    const pixels: Uint8Array = await this.reader.bytes(count * 4);
    const mask: Uint8Array = await this.reader.bytes(Math.ceil(width / 8) * height);
    const maskStride: number = Math.ceil(width / 8);
    for (let y: number = 0; y < height; y++) {
      for (let x: number = 0; x < width; x++) {
        const at: number = (y * width + x) * 4 + 3;
        pixels[at] = (mask[y * maskStride + Math.floor(x / 8)] & (128 >>> (x % 8))) !== 0 ? 255 : 0;
      }
    }
    this.onCursor(new RFBCursor(width, height, hotX, hotY, pixels));
  }

  private async colorMap(): Promise<void> {
    await this.reader.bytes(1);
    await this.reader.u16();
    const count: number = await this.reader.u16();
    if (count > 4096) throw new RFBIssue('unsupported', 'rfb_colormap_limit');
    await this.reader.bytes(count * 6);
  }

  private async serverClipboard(): Promise<void> {
    await this.reader.bytes(3);
    const length: number = await this.reader.u32();
    if (length > MAX_TEXT_BYTES) throw new RFBIssue('unsupported', 'rfb_clipboard_limit');
    this.onClipboard(latin1(await this.reader.bytes(length)));
  }

  private inputAllowed(): boolean { return this.active && this.started && this.canControl(); }

  private send(bytes: Uint8Array): boolean {
    if (!this.active) return false;
    return this.transport.sendBinary(arrayBuffer(bytes));
  }

  private fail(error: Error): void {
    if (!this.active) return;
    const issue: RFBIssue | undefined = error instanceof RFBIssue ? error : undefined;
    const code: string = issue ? issue.code : error.message === 'rfb_connection_lost' ? 'connection_lost' :
      error.message.startsWith('rfb_') ? 'unsupported' : 'connection_failed';
    const detail: string = issue ? issue.detail : error.message;
    this.active = false;
    this.live = false;
    this.reader.close();
    this.onState(code === 'connection_lost' ? 'closed' : 'failed', code, detail);
    this.transport.close();
  }
}
