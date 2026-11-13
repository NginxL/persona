import { common } from '@kit.AbilityKit';
import { BusinessError } from '@kit.BasicServicesKit';
import { fileIo, fileUri } from '@kit.CoreFileKit';
import { image } from '@kit.ImageKit';
import { HttpFailure } from '../platform/HubTransport';
import { LocalStore } from '../platform/LocalStore';
import type { HubRepository } from './HubRepository';
import { Wire } from './Wire';
import type { ClientModel } from './ClientModel';
import { chooseDeviceLookAvatarFile, deviceLookPackageJSON, deviceLookPalette, parseDeviceLookPackage, validAccentId,
  validDeviceLookFile, validDeviceLookOwner, validDeviceLookPackageId, validDeviceLookRev,
  DEVICE_LOOK_MAX_AVATAR_BYTES, DEVICE_LOOK_MAX_PACKAGE_BYTES,
  type DeviceLookPackage, type DeviceLookPackageColors, type DeviceLookPalette } from '../core/DeviceLook';

const STORAGE_KEY: string = 'device-look';
const BUILTIN_ID: string = 'persona';
const BUILTIN_KEY: string = 'builtin:persona';
const MAX_AVATAR_BYTES: number = DEVICE_LOOK_MAX_AVATAR_BYTES;
const MAX_THUMBNAIL_BYTES: number = 8 * 1024 * 1024;
const MAX_REMOTE_PACKAGES: number = 32;
const AVATAR_PIXELS: number = 128;

/** A selectable real avatar: the bundled Mochi or a validated Hub package image. */
@ObservedV2
export class DeviceLookAvatar {
  id: string = '';
  @Trace name: string = '';
  @Trace subtitle: string = '';
  @Trace imagePath: string = '';
  rev: string = '';
  profileId: string = '';
  hubId: string = '';
  file: string = '';
  ownerName: string = '';
  infoJSON: string = '';
  packageInfo: DeviceLookPackage | undefined = undefined;
  constructor(id: string, name: string, subtitle: string = '', imagePath: string = '', rev: string = '',
    profileId: string = '', hubId: string = '', file: string = '', infoJSON: string = '', packageInfo?: DeviceLookPackage,
    ownerName: string = '') {
    this.id = id; this.name = name; this.subtitle = subtitle; this.imagePath = imagePath;
    this.rev = rev; this.profileId = profileId; this.hubId = hubId; this.file = file;
    this.infoJSON = infoJSON; this.packageInfo = packageInfo; this.ownerName = ownerName;
  }
  isBuiltin(): boolean { return this.id === BUILTIN_ID; }
  imageUri(): string { return this.imagePath ? fileUri.getUriFromPath(this.imagePath) : ''; }
  optionKey(): string { return this.isBuiltin() ? BUILTIN_KEY : JSON.stringify([this.profileId, this.hubId, this.id, this.rev]); }
}

/** Device-wide Avatar and Accent preferences with authenticated Hub package thumbnails in private app storage. */
@ObservedV2
export class DeviceLook {
  @Trace selectedAvatar: DeviceLookAvatar = new DeviceLookAvatar(BUILTIN_ID, 'Mochi');
  @Trace accentId: string = 'style';
  @Trace avatars: DeviceLookAvatar[] = [new DeviceLookAvatar(BUILTIN_ID, 'Mochi')];
  @Trace busy: boolean = false;
  @Trace error: string = '';
  @Trace pictures: number = 0;
  @Trace ready: boolean = false;

  private storage: LocalStore | undefined = undefined;
  private cacheRoot: string = '';
  private boundClient: ClientModel | undefined = undefined;
  private bindRevision: number = 0;
  private boundPairing: number = 0;
  private commitTail: Promise<void> = Promise.resolve();
  private forgetEpochs: Map<string, number> = new Map<string, number>();
  private fileSequence: number = 0;
  private changeHandler: (() => void) | undefined = undefined;
  private initializing: Promise<void> | undefined;

  get avatarId(): string { return this.selectedAvatar.id; }
  get avatarKey(): string { return this.selectedAvatar.optionKey(); }
  get avatarName(): string { return this.selectedAvatar.name; }
  get avatarPath(): string { return this.selectedAvatar.imagePath; }
  get avatarUri(): string { return this.selectedAvatar.imageUri(); }
  get avatarColors(): DeviceLookPackageColors | undefined { return this.selectedAvatar.packageInfo?.colors; }

  /** Root calls this once with the Ability context; settings live in a non-Hub-owned LocalStore key. */
  async initialize(context: common.UIAbilityContext): Promise<void> {
    if (this.initializing) return this.initializing;
    this.ready = false;
    const task = this.initializeStore(context); this.initializing = task;
    try { await task; this.ready = true; }
    catch (error) { await this.storage?.close().catch(() => {}); this.storage = undefined; this.initializing = undefined; throw error as Error; }
  }

  private async initializeStore(context: common.UIAbilityContext): Promise<void> {
    if (!context.filesDir) throw new Error('storage_unavailable');
    this.cacheRoot = context.filesDir + '/device-look/avatars';
    const store = new LocalStore();
    await store.open(context);
    this.storage = store;
    try { fileIo.statSync(this.cacheRoot); } catch (_) { fileIo.mkdirSync(this.cacheRoot, true); }
    await this.restore();
  }

  /** Binds to the current Hub. An older Hub uses its documented `/personas` list after `/looks` returns 404. */
  async bind(client: ClientModel | undefined): Promise<void> {
    const revision = ++this.bindRevision;
    this.boundClient = client;
    this.boundPairing = client?.pairingRevision() || 0;
    this.error = ''; this.busy = false;
    if (!client || !client.connected || client.demo) {
      this.avatars = this.offlineOptions();
      this.changed();
      return;
    }
    let repository: HubRepository;
    try { repository = client.repo(); }
    catch (_) { this.avatars = this.offlineOptions(); this.changed(); return; }
    const generation = client.connectionGeneration(), scope = client.scope();
    const admitted = (): boolean => this.admits(client, repository, generation, scope, revision);
    this.busy = true;
    try {
      await this.commitTail;
      if (!admitted()) return;
      this.pruneCache(this.cacheRoot, this.selectedAvatar.imagePath);
      let response: Record<string, Object>;
      try {
        response = Wire.parse(await repository.transport.request('/api/v1/looks'));
      } catch (error) {
        if (!(error instanceof HttpFailure && error.status === 404)) throw error as Error;
        if (!admitted()) return;
        response = Wire.parse(await repository.transport.request('/api/v1/personas'));
      }
      if (!admitted()) return;
      let rows = Wire.rows(response, 'looks');
      if (!rows.length) rows = Wire.rows(response, 'personas');
      const profileId = client.profile.id, hubId = client.profile.hubId;
      if (!validDeviceLookOwner(profileId) || !validDeviceLookOwner(hubId)) {
        this.error = 'device_look_owner_unavailable';
        this.avatars = this.offlineOptions();
        this.changed();
        return;
      }
      const packages: DeviceLookPackage[] = [];
      const seen: Set<string> = new Set<string>();
      const infoJSONs: Map<string, string> = new Map<string, string>();
      for (const item of rows.slice(0, MAX_REMOTE_PACKAGES)) {
        const nested = Wire.object(item, 'pack');
        const raw = Object.keys(nested).length ? nested : item;
        const parsed = parseDeviceLookPackage(raw);
        if (!parsed) continue;
        const key = JSON.stringify([profileId, hubId, parsed.id, parsed.rev]);
        if (seen.has(key)) continue;
        seen.add(key); packages.push(parsed); infoJSONs.set(key, deviceLookPackageJSON(parsed));
      }
      const options: DeviceLookAvatar[] = [new DeviceLookAvatar(BUILTIN_ID, 'Mochi')];
      let thumbnailBytes = 0;
      for (const pack of packages) {
        if (!admitted()) return;
        const image = chooseDeviceLookAvatarFile(pack.avatarFiles, 64);
        if (!image || !image.file.toLowerCase().endsWith('.png')) continue;
        const path = this.cachePath(profileId, hubId, pack.id, pack.rev, image.file);
        if (!path) continue;
        let size = this.cachedSize(path);
        const fetched = size < 0;
        if (fetched) {
          if (thumbnailBytes >= MAX_THUMBNAIL_BYTES) break;
          try { size = await this.fetchImage(client, repository, generation, scope, revision, pack, image.file, path); }
          catch (error) {
            if (!admitted()) return;
            if (error instanceof Error && error.message === 'request_updated') return;
            continue;
          }
        }
        if (size < 0 || size > MAX_AVATAR_BYTES) continue;
        if (thumbnailBytes + size > MAX_THUMBNAIL_BYTES) {
          if (fetched) { try { fileIo.unlinkSync(path); } catch (_) { } }
          break;
        }
        thumbnailBytes += size;
        const key = JSON.stringify([profileId, hubId, pack.id, pack.rev]);
        const infoJSON = infoJSONs.get(key) || '';
        if (!infoJSON) continue;
        const subtitle = pack.about || pack.subtitle;
        options.push(new DeviceLookAvatar(pack.id, pack.name, subtitle, path, pack.rev, profileId, hubId,
          image.file, infoJSON, pack, client.profile.name));
      }
      if (!admitted()) return;
      this.avatars = this.withSelected(options);
      if (packages.length && options.length === 1) this.error = 'device_look_avatar_unavailable';
      this.pictures++;
      this.changed();
    } catch (error) {
      if (!admitted()) return;
      this.error = error instanceof HttpFailure && error.status === 404 ? 'device_look_hub_unsupported' : 'device_look_list_failed';
      this.avatars = this.offlineOptions();
      this.changed();
    } finally { if (revision === this.bindRevision) { this.busy = false; this.changed(); } }
  }

  /** Sets a device-wide avatar choice after its real image is already cached or safely downloaded. */
  async setAvatar(id: string): Promise<boolean> {
    if (!this.ready || this.busy) return false;
    if (id === BUILTIN_KEY) {
      return this.commitSelection(new DeviceLookAvatar(BUILTIN_ID, 'Mochi'), undefined);
    }
    const option = this.avatars.find((item: DeviceLookAvatar) => item.optionKey() === id && !item.isBuiltin());
    if (!option || !option.packageInfo || !option.file || !option.profileId || !option.hubId) return false;
    let path = option.imagePath;
    if (this.cachedSize(path) < 0) {
      const client = this.boundClient;
      if (!client || !client.connected || client.demo || client.profile.id !== option.profileId || client.profile.hubId !== option.hubId) {
        this.error = 'device_look_connect_to_hub'; this.changed(); return false;
      }
      let repository: HubRepository;
      try { repository = client.repo(); } catch (_) { this.error = 'device_look_connect_to_hub'; this.changed(); return false; }
      this.busy = true;
      const revision = this.bindRevision, generation = client.connectionGeneration(), scope = client.scope();
      try {
        path = this.cachePath(option.profileId, option.hubId, option.id, option.rev, option.file);
        if (!path) throw new Error('device_look_avatar_unavailable');
        await this.fetchImage(client, repository, generation, scope, revision, option.packageInfo, option.file, path);
        if (!this.admits(client, repository, generation, scope, revision)) throw new Error('request_updated');
      } catch (error) {
        if (revision === this.bindRevision) {
          this.error = error instanceof Error && error.message === 'request_updated' ? '' : 'device_look_download_failed';
          this.busy = false; this.changed();
        }
        return false;
      }
      this.busy = false;
    }
    if (this.cachedSize(path) < 0) { this.error = 'device_look_download_failed'; this.changed(); return false; }
    const selected = new DeviceLookAvatar(option.id, option.name, option.subtitle, path, option.rev,
      option.profileId, option.hubId, option.file, option.infoJSON, option.packageInfo, option.ownerName);
    return this.commitSelection(selected, undefined);
  }

  /** Accent selection is independent of the current avatar and interface style. */
  async setAccent(id: string): Promise<boolean> {
    if (!this.ready || !validAccentId(id) || id === 'avatar' && !this.avatarColors && this.accentId !== 'avatar') return false;
    return this.commitSelection(undefined, id);
  }

  accentOptions(): string[] {
    const options: string[] = ['style'];
    if (this.avatarColors || this.accentId === 'avatar') options.push('avatar');
    options.push('blue', 'coral', 'lavender', 'teal', 'lark');
    return options;
  }

  /** Root maps the light/dark colors into ArkUI WithTheme and native bubble tokens. */
  palette(style: string): DeviceLookPalette {
    return deviceLookPalette(this.accentId, style, this.avatarColors);
  }

  setChangeHandler(handler: (() => void) | undefined): void { this.changeHandler = handler; }

  /** Purges this forgotten Hub's unselected image copies while retaining the device-wide selected avatar. */
  async forgetHub(profileId: string, hubId: string): Promise<void> {
    if (!this.cacheRoot || !validDeviceLookOwner(profileId) || !validDeviceLookOwner(hubId)) return;
    const ownerRoot = this.cacheRoot + '/' + profileId + '/' + hubId;
    const ownerKey = JSON.stringify([profileId, hubId]);
    this.forgetEpochs.set(ownerKey, (this.forgetEpochs.get(ownerKey) || 0) + 1);
    const disconnect = this.boundClient?.profile.id === profileId && this.boundClient.profile.hubId === hubId;
    if (disconnect) { this.bindRevision++; this.boundClient = undefined; }
    await this.commitTail;
    const selected = this.selectedAvatar;
    const keepPath = !selected.isBuiltin() && selected.profileId === profileId && selected.hubId === hubId &&
      selected.imagePath.startsWith(ownerRoot + '/') && this.cachedSize(selected.imagePath) > 0 ? selected.imagePath : '';
    try { this.pruneCache(ownerRoot, keepPath); }
    catch (_) { this.error = 'device_look_cache_cleanup_failed'; this.changed(); throw new Error('device_look_cache_cleanup_failed'); }
    this.avatars = disconnect ? this.offlineOptions() : this.avatars.filter((avatar: DeviceLookAvatar) =>
      avatar.isBuiltin() || avatar.profileId !== profileId || avatar.hubId !== hubId || avatar.optionKey() === this.avatarKey);
    this.changed();
  }

  private changed(): void { this.changeHandler?.(); }

  private offlineOptions(): DeviceLookAvatar[] {
    const result: DeviceLookAvatar[] = [new DeviceLookAvatar(BUILTIN_ID, 'Mochi')];
    if (!this.selectedAvatar.isBuiltin() && this.selectedAvatar.imagePath && this.cachedSize(this.selectedAvatar.imagePath) >= 0) result.push(this.selectedAvatar);
    return result;
  }

  private withSelected(values: DeviceLookAvatar[]): DeviceLookAvatar[] {
    const result = values.slice();
    if (!this.selectedAvatar.isBuiltin() && !result.some((item: DeviceLookAvatar) => item.optionKey() === this.selectedAvatar.optionKey())) result.push(this.selectedAvatar);
    return result;
  }

  private admits(client: ClientModel, repository: HubRepository, generation: number, scope: string, revision: number): boolean {
    if (revision !== this.bindRevision || client !== this.boundClient || !client.connected || client.demo ||
      this.boundPairing !== client.pairingRevision() || !client.owns(generation, scope)) return false;
    try { return client.repo() === repository; } catch (_) { return false; }
  }

  private cachePath(profileId: string, hubId: string, id: string, rev: string, file: string): string {
    if (!this.cacheRoot || !validDeviceLookOwner(profileId) || !validDeviceLookOwner(hubId) ||
      !validDeviceLookPackageId(id) || !validDeviceLookRev(rev) || !validDeviceLookFile(file) || !file.toLowerCase().endsWith('.png')) return '';
    return this.cacheRoot + '/' + profileId + '/' + hubId + '/' + id + '/' + rev + '/' + file;
  }

  private cachedSize(path: string): number {
    if (!path || !this.cacheRoot || !path.startsWith(this.cacheRoot + '/')) return -1;
    try {
      const size = fileIo.statSync(path).size;
      return typeof size === 'number' && size > 0 && size <= MAX_AVATAR_BYTES ? size : -1;
    } catch (_) { return -1; }
  }

  private pruneCache(path: string, keepPath: string): void {
    if (path === keepPath) return;
    try {
      if (!fileIo.statSync(path).isDirectory()) { fileIo.unlinkSync(path); return; }
    } catch (error) { if ((error as BusinessError).code === 13900002) return; throw error as Error; }
    for (const name of fileIo.listFileSync(path)) {
      if (!name || name === '.' || name === '..' || name.includes('/') || name.includes('\\')) throw new Error('device_look_cache_cleanup_failed');
      const child = path + '/' + name;
      if (keepPath && (keepPath === child || keepPath.startsWith(child + '/'))) this.pruneCache(child, keepPath);
      else this.pruneCache(child, '');
    }
    if (!keepPath || !keepPath.startsWith(path + '/')) fileIo.rmdirSync(path);
  }

  private async fetchImage(client: ClientModel, repository: HubRepository, generation: number,
    scope: string, revision: number, pack: DeviceLookPackage, file: string, destination: string): Promise<number> {
    if (!validDeviceLookFile(file) || !file.toLowerCase().endsWith('.png') || pack.bytes > DEVICE_LOOK_MAX_PACKAGE_BYTES) throw new Error('device_look_avatar_unavailable');
    if (!this.admits(client, repository, generation, scope, revision)) throw new Error('request_updated');
    const encodedFile = file.split('/').map((part: string) => encodeURIComponent(part)).join('/');
    const path = '/api/v1/personas/' + encodeURIComponent(pack.id) + '/' + encodedFile + '?rev=' + encodeURIComponent(pack.rev);
    const response = await repository.transport.requestBytes(path);
    if (!this.admits(client, repository, generation, scope, revision)) throw new Error('request_updated');
    const bytes = new Uint8Array(response.data);
    if (!response.contentType.toLowerCase().startsWith('image/png') || bytes.length < 24 || bytes.length > MAX_AVATAR_BYTES ||
      bytes[0] !== 137 || bytes[1] !== 80 || bytes[2] !== 78 || bytes[3] !== 71 || bytes[4] !== 13 || bytes[5] !== 10 ||
      bytes[6] !== 26 || bytes[7] !== 10 || bytes[12] !== 73 || bytes[13] !== 72 || bytes[14] !== 68 || bytes[15] !== 82) {
      throw new Error('device_look_avatar_invalid');
    }
    const width = bytes[16] * 16777216 + bytes[17] * 65536 + bytes[18] * 256 + bytes[19];
    const height = bytes[20] * 16777216 + bytes[21] * 65536 + bytes[22] * 256 + bytes[23];
    if (!width || !height || width > 4096 || height > 4096 || width * height > 16000000) throw new Error('device_look_avatar_invalid');
    let source: image.ImageSource | undefined, pixels: image.PixelMap | undefined;
    try {
      source = image.createImageSource(response.data);
      const info = await source.getImageInfo();
      if (!this.admits(client, repository, generation, scope, revision)) throw new Error('request_updated');
      if (info.size.width !== width || info.size.height !== height) throw new Error('device_look_avatar_invalid');
      const scale = Math.min(1, 256 / Math.max(width, height));
      pixels = await source.createPixelMap({ desiredSize: { width: Math.max(1, Math.round(width * scale)), height: Math.max(1, Math.round(height * scale)) } });
      if (!this.admits(client, repository, generation, scope, revision)) throw new Error('request_updated');
    } finally { await pixels?.release().catch(() => {}); await source?.release().catch(() => {}); }
    const slash = destination.lastIndexOf('/');
    if (slash <= 0 || !destination.startsWith(this.cacheRoot + '/')) throw new Error('device_look_avatar_unavailable');
    const directory = destination.slice(0, slash);
    try { fileIo.statSync(directory); } catch (_) { fileIo.mkdirSync(directory, true); }
    const temporary = destination + '.part-' + String(++this.fileSequence);
    const output = fileIo.openSync(temporary, fileIo.OpenMode.CREATE | fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.TRUNC);
    let open = true;
    try {
      if (fileIo.writeSync(output.fd, response.data) !== bytes.length) throw new Error('device_look_cache_failed');
      fileIo.fsyncSync(output.fd); fileIo.closeSync(output.fd); open = false;
      if (!this.admits(client, repository, generation, scope, revision)) throw new Error('request_updated');
      fileIo.renameSync(temporary, destination);
    } catch (error) {
      if (open) { try { fileIo.closeSync(output.fd); } catch (_) { } }
      try { fileIo.unlinkSync(temporary); } catch (_) { }
      throw error as Error;
    }
    this.pictures++;
    return bytes.length;
  }

  private async restore(): Promise<void> {
    if (!this.storage) return;
    const raw = await this.storage.get(STORAGE_KEY);
    if (!raw || raw.length > 256 * 1024) return;
    try {
      const saved = Wire.record(JSON.parse(raw) as Object);
      const accent = Wire.text(saved, 'accentId', 'style');
      this.accentId = validAccentId(accent) ? accent : 'style';
      const id = Wire.text(saved, 'avatarId', BUILTIN_ID);
      if (id === BUILTIN_ID) return;
      const profileId = Wire.text(saved, 'profileId'), hubId = Wire.text(saved, 'hubId'), rev = Wire.text(saved, 'rev');
      const file = Wire.text(saved, 'file'), infoJSON = Wire.text(saved, 'infoJSON');
      if (!infoJSON || infoJSON.length > 128 * 1024) return;
      const pkg = parseDeviceLookPackage(JSON.parse(infoJSON) as Object);
      if (!pkg || pkg.id !== id || pkg.rev !== rev) return;
      const path = this.cachePath(profileId, hubId, id, rev, file);
      if (!path || this.cachedSize(path) < 0) return;
      this.selectedAvatar = new DeviceLookAvatar(id, pkg.name, pkg.about || pkg.subtitle, path, rev,
        profileId, hubId, file, infoJSON, pkg, Wire.text(saved, 'ownerName'));
      this.avatars = this.offlineOptions();
    } catch (_) { this.selectedAvatar = new DeviceLookAvatar(BUILTIN_ID, 'Mochi'); }
  }

  private selectionJSON(selected: DeviceLookAvatar, accent: string): string {
    return JSON.stringify(Wire.payload(['version', 'avatarId', 'accentId', 'profileId', 'hubId', 'rev', 'file', 'infoJSON', 'ownerName'],
      [1, selected.id, accent, selected.profileId, selected.hubId, selected.rev, selected.file, selected.infoJSON, selected.ownerName]));
  }

  private async commitSelection(avatar: DeviceLookAvatar | undefined, accent: string | undefined): Promise<boolean> {
    if (!this.ready) return false;
    const ownerKey = avatar && !avatar.isBuiltin() ? JSON.stringify([avatar.profileId, avatar.hubId]) : '';
    const epoch = this.forgetEpochs.get(ownerKey) || 0;
    let committed = false;
    const task = this.commitTail.then(async (): Promise<void> => {
      const store = this.storage;
      if (!store) { this.error = 'device_look_storage_failed'; this.changed(); return; }
      const next = avatar || this.selectedAvatar, color = accent === undefined ? this.accentId : accent;
      const current = (): boolean => !avatar || avatar.isBuiltin() || (this.forgetEpochs.get(ownerKey) || 0) === epoch && this.cachedSize(avatar.imagePath) > 0;
      if (!current()) return;
      if (next.optionKey() === this.avatarKey && color === this.accentId) { committed = true; return; }
      if (color === 'avatar' && !next.packageInfo?.colors && this.accentId !== 'avatar') return;
      const previous = this.selectionJSON(this.selectedAvatar, this.accentId);
      try {
        await store.put(STORAGE_KEY, this.selectionJSON(next, color));
        if (!current()) { await store.put(STORAGE_KEY, previous); return; }
        this.selectedAvatar = next; this.accentId = color; this.error = ''; committed = true;
        this.avatars = this.withSelected(this.avatars); this.changed();
      } catch (_) { this.error = 'device_look_storage_failed'; this.changed(); }
    });
    this.commitTail = task.catch(() => {}); await task; return committed;
  }
}

export const deviceLook: DeviceLook = new DeviceLook();
