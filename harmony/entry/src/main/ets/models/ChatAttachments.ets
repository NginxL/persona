import { common, wantConstant } from '@kit.AbilityKit';
import { util } from '@kit.ArkTS';
import { dataSharePredicates } from '@kit.ArkData';
import { fileIo, fileUri, picker } from '@kit.CoreFileKit';
import { image } from '@kit.ImageKit';
import { photoAccessHelper } from '@kit.MediaLibraryKit';
import type { ClientModel } from './ClientModel';
import type { HubRepository } from './HubRepository';
import { LocalStore } from '../platform/LocalStore';
import { isHubStateKey } from '../core/StorageOwnership';
import { attachmentExtension, attachmentMimeType, isAttachmentCopyName } from '../core/AttachmentTypes';
import { ResourceRow, Wire } from './Wire';

const MAX_ATTACHMENTS: number = 8;
const MAX_IMAGES: number = 4;
const MAX_FILE_BYTES: number = 32 * 1024 * 1024;
const MAX_IMAGE_BYTES: number = 64 * 1024 * 1024;
const MAX_IMAGE_PIXELS: number = 40000000;
const MAX_IMAGE_EDGE: number = 1600;
const REQUEST_LIMIT: number = 4 * 1024 * 1024;
const TEXT_LIMIT: number = 100000;
const COPY_CHUNK_BYTES: number = 256 * 1024;
const STORE_NAME: string = 'chat-attachments.db';

interface CopiedFile {
  size: number;
}

interface DecodedAttachmentImage {
  mime: string;
  width: number;
  height: number;
}

interface EncodedAttachmentImage {
  mime: string;
  data: ArrayBuffer;
}

interface AttachmentImageSize {
  width: number;
  height: number;
}

interface UploadOutcomeFlags {
  resultUnknown?: boolean;
  resultPending?: boolean;
}

@ObservedV2
export class ChatAttachment {
  @Trace id: string = '';
  @Trace kind: string = 'file';
  @Trace name: string = '';
  @Trace localPath: string = '';
  @Trace previewUri: string = '';
  @Trace mime: string = '';
  @Trace size: number = 0;
  @Trace width: number = 0;
  @Trace height: number = 0;
  @Trace status: string = 'selected';
  @Trace remotePath: string = '';
  @Trace remoteName: string = '';
  @Trace revision: number = 0;
}

export class ChatImagePayload {
  mime: string = '';
  data: string = '';
  constructor(mime: string = '', data: string = '') { this.mime = mime; this.data = data; }
}

export class AttachmentRevision {
  id: string = '';
  revision: number = 0;
  constructor(id: string = '', revision: number = 0) { this.id = id; this.revision = revision; }
}

/** The exact selected attachment revisions represented by one prepared message. */
export class AttachmentSendSnapshot {
  target: string = '';
  selectionRevision: number = 0;
  attachments: AttachmentRevision[] = [];
}

/** Captured data the caller sends to the captured repository and target. */
export class PreparedAttachmentSend {
  target: string = '';
  modelTarget: string = '';
  scope: string = '';
  generation: number = 0;
  row: ResourceRow = new ResourceRow();
  repository: HubRepository | undefined;
  text: string = '';
  images: ChatImagePayload[] = [];
  snapshot: AttachmentSendSnapshot = new AttachmentSendSnapshot();
}

class AttachmentDraft {
  target: string = '';
  selectionRevision: number = 0;
  attachments: ChatAttachment[] = [];
}

class AttachmentOperation {
  model: ClientModel;
  context: common.UIAbilityContext;
  row: ResourceRow;
  modelTarget: string;
  scope: string;
  generation: number;
  target: string;
  openRevision: number;
  constructor(model: ClientModel, context: common.UIAbilityContext, row: ResourceRow,
    modelTarget: string, scope: string, generation: number, target: string, openRevision: number) {
    this.model = model; this.context = context; this.row = row; this.modelTarget = modelTarget;
    this.scope = scope; this.generation = generation; this.target = target; this.openRevision = openRevision;
  }
}

@ObservedV2
export class ChatAttachments {
  private static transactions: Map<string, Promise<void>> = new Map();
  private static instances: Set<ChatAttachments> = new Set();
  static async forgetHub(context: common.UIAbilityContext, hub: string): Promise<void> {
    const storage = new LocalStore(STORE_NAME), paths: string[] = [];
    const directory = context.filesDir + '/chat-attachments/';
    try {
      await storage.open(context);
      for (const target of ChatAttachments.transactions.keys()) if (isHubStateKey('attachments:' + target, hub)) await ChatAttachments.transactions.get(target);
      for (const key of await storage.keys()) if (isHubStateKey(key, hub)) {
        const data = Wire.parse(await storage.get(key));
        for (const item of Wire.rows(data, 'attachments')) {
          const path = Wire.text(item, 'localPath');
          if (path.startsWith(directory) && isAttachmentCopyName(path.substring(directory.length))) paths.push(path);
        }
      }
      await storage.purgeHub(hub);
      for (const path of paths) { try { fileIo.unlinkSync(path); } catch (_) {} }
    } finally { await storage.close(); }
  }
  static allowHub(hub: string): void { new LocalStore(STORE_NAME).allowHub(hub); }
  /** Repair an acknowledged selection even after its conversation component has been destroyed. */
  static suppressStoredSnapshot(raw: string, target: string): AttachmentSendSnapshot {
    const snapshot = ChatAttachments.storedSnapshot(raw, target);
    for (const instance of ChatAttachments.instances) if (instance.opened && instance.target === snapshot.target) {
      instance.attachments = instance.attachments.filter((item: ChatAttachment) => !snapshot.attachments.some((captured: AttachmentRevision) => captured.id === item.id && captured.revision === item.revision));
      instance.busy = true;
    }
    return snapshot;
  }
  private static storedSnapshot(raw: string, target: string): AttachmentSendSnapshot {
    if (raw.length > 4096) throw new Error('chat_attachment_selection_changed');
    const data = Wire.parse(raw), snapshot = new AttachmentSendSnapshot();
    snapshot.target = Wire.text(data, 'target'); snapshot.selectionRevision = Wire.number(data, 'selectionRevision');
    const owner = JSON.parse(target) as string[], scope = JSON.parse(owner[0]) as string[], parts = JSON.parse(snapshot.target) as string[];
    if (parts.length !== 4 || parts[0] !== scope[0] || parts[1] !== scope[1] || parts[2] !== owner[2] || parts[3] !== owner[3]) throw new Error('chat_attachment_selection_changed');
    for (const row of Wire.rows(data, 'attachments')) {
      const id = Wire.text(row, 'id'), revision = Wire.number(row, 'revision');
      if (!/^[a-zA-Z0-9-]{1,96}$/.test(id) || !Number.isSafeInteger(revision) || revision < 1) throw new Error('chat_attachment_selection_changed');
      snapshot.attachments.push(new AttachmentRevision(id, revision));
    }
    if (snapshot.attachments.length > MAX_ATTACHMENTS) throw new Error('chat_attachment_selection_changed');
    return snapshot;
  }
  static async commitStoredSnapshot(context: common.UIAbilityContext, raw: string, target: string, valid: () => boolean = (): boolean => true): Promise<void> {
    if (!raw || !valid()) return;
    const snapshot = ChatAttachments.suppressStoredSnapshot(raw, target);
    const holder = new ChatAttachments(); holder.context = context; holder.storageDirectory = holder.ownedDirectory(context);
    try { await holder.openStorage(context); if (!valid()) throw new Error('scope_changed'); if (!await holder.committed(snapshot, valid)) throw new Error('chat_attachment_storage_unavailable'); }
    catch (error) { for (const instance of ChatAttachments.instances) if (valid() && instance.opened && instance.target === snapshot.target) instance.errorKey = 'chat_attachment_storage_unavailable'; throw error as Error; }
    finally { await holder.storage?.close(); }
  }

  @Trace attachments: ChatAttachment[] = [];
  @Trace busy: boolean = false;
  @Trace picking: boolean = false;
  @Trace errorKey: string = '';
  private model: ClientModel | undefined;
  private context: common.UIAbilityContext | undefined;
  private row: ResourceRow | undefined;
  private scope: string = '';
  private target: string = '';
  private modelTarget: string = '';
  private openRevision: number = 0;
  private openRequestRevision: number = 0;
  private selectionRevision: number = 0;
  private opened: boolean = false;
  private restoring: boolean = false;
  private storageDirectory: string = '';
  private storage: LocalStore | undefined;
  private storageOpening: Promise<LocalStore> | undefined;
  private pendingStorageWrites: Map<string, Promise<void>> = new Map();

  private key(target: string): string { return 'attachments:' + target; }
  private ownedDirectory(context: common.UIAbilityContext): string { return context.filesDir + '/chat-attachments'; }

  private async openStorage(context: common.UIAbilityContext): Promise<LocalStore> {
    if (this.storage) return this.storage;
    if (this.storageOpening) return this.storageOpening;
    const storage = new LocalStore(STORE_NAME);
    this.storageOpening = storage.open(context).then(() => {
      this.storage = storage;
      this.storageOpening = undefined;
      return storage;
    }).catch((error: Error) => {
      this.storageOpening = undefined;
      throw error;
    });
    return this.storageOpening;
  }

  /** Open or restore the draft owned by one immutable Hub + manager + machine + session target. */
  async open(model: ClientModel, context: common.UIAbilityContext, row: ResourceRow): Promise<void> {
    const requestRevision = ++this.openRequestRevision;
    await this.detachCurrent();
    if (requestRevision !== this.openRequestRevision) return;
    const openRevision = ++this.openRevision;
    this.attachments = [];
    this.selectionRevision = 0;
    this.target = '';
    this.modelTarget = '';
    this.scope = '';
    this.errorKey = '';
    const modelTarget = model.target();
    if (!row.machine || !(row.session || row.id) || model.target(row) !== modelTarget || !model.profile.id) {
      this.errorKey = 'chat_attachment_target_unavailable';
      this.attachments = [];
      return;
    }
    this.model = model;
    this.context = context;
    this.row = row;
    this.scope = model.scope();
    this.modelTarget = modelTarget;
    const hubId = model.profile.hubId || model.profile.id;
    const managerId = row.manager || model.profile.manager;
    if (!hubId) {
      this.errorKey = 'chat_attachment_target_unavailable';
      this.attachments = [];
      return;
    }
    this.target = JSON.stringify([hubId, managerId, row.machine, row.session || row.id]);
    this.storageDirectory = this.ownedDirectory(context);
    this.opened = true;
    ChatAttachments.instances.add(this);
    this.restoring = true;
    this.busy = true;
    this.errorKey = '';
    const operation = this.capture();
    if (!operation) {
      this.opened = false;
      this.restoring = false;
      this.busy = false;
      this.errorKey = 'chat_attachment_target_unavailable';
      return;
    }
    try {
      const storage = await this.openStorage(context);
      if (!this.current(operation)) return;
      this.ensureStorageDirectory();
      await this.waitForPendingWrite(operation.target);
      await ChatAttachments.transactions.get(operation.target);
      if (!this.current(operation)) return;
      const saved = await storage.get(this.key(operation.target));
      if (!this.current(operation)) return;
      const draft = this.decodeDraft(saved, operation.target);
      this.selectionRevision = draft.selectionRevision;
      const recovered = draft.attachments.map((item: ChatAttachment) => {
        if (item.status === 'uploading' || (item.kind === 'file' && item.status === 'ready' && !item.remotePath)) item.status = 'unknown';
        return item;
      });
      this.attachments = recovered;
      if (recovered.some((item: ChatAttachment) => item.status === 'unknown')) {
        const saved = await this.changeDraft(operation.target, storage, (latest: AttachmentDraft): void => {
          latest.attachments.forEach((item: ChatAttachment) => { if (item.status === 'uploading' || item.kind === 'file' && item.status === 'ready' && !item.remotePath) item.status = 'unknown'; });
        });
        if (this.current(operation)) this.attachments = saved.attachments;
      }
      if (!this.current(operation)) return;
      this.restoring = false;
      this.busy = false;
    } catch (_) {
      if (this.current(operation)) {
        this.errorKey = 'chat_attachment_storage_unavailable';
        this.restoring = false;
        this.busy = false;
      }
    }
  }

  /** Detach immediately; every visible mutation is already persisted before publication. */
  async close(): Promise<void> {
    this.openRequestRevision++;
    await this.detachCurrent();
  }

  private async detachCurrent(): Promise<void> {
    // Invalidate every picker/upload operation before yielding to storage I/O.
    this.opened = false;
    ChatAttachments.instances.delete(this);
    this.restoring = false;
    this.openRevision++;
    this.busy = false;
    this.picking = false;
    this.attachments = [];
    this.errorKey = '';
    this.model = undefined;
    this.context = undefined;
    this.row = undefined;
    this.scope = '';
    this.target = '';
    this.modelTarget = '';
    this.selectionRevision = 0;

  }

  private async waitForPendingWrite(target: string): Promise<void> {
    const pending = this.pendingStorageWrites.get(target);
    if (pending) await pending;
  }

  private async queueDraftWrite(target: string, raw: string, storage: LocalStore, epoch: number,
    admit: () => boolean = (): boolean => true): Promise<void> {
    const previous = this.pendingStorageWrites.get(target);
    const write: Promise<void> = (previous ? previous.catch(() => {}) : Promise.resolve()).then(async () => {
      if (!admit() || !storage.accepts(this.key(target), epoch)) throw new Error('scope_changed');
      if (raw) await storage.put(this.key(target), raw, epoch);
      else await storage.remove(this.key(target), epoch);
    });
    this.pendingStorageWrites.set(target, write);
    try { await write; } finally {
      if (this.pendingStorageWrites.get(target) === write) this.pendingStorageWrites.delete(target);
    }
  }

  /** Open the native image picker, copy selected media into the app sandbox, then persist the metadata. */
  async choosePhotos(): Promise<void> {
    const operation = this.capture();
    if (!operation || this.busy) return;
    const currentImages = this.attachments.filter((item: ChatAttachment) => item.kind === 'image').length;
    if (currentImages >= MAX_IMAGES || this.attachments.length >= MAX_ATTACHMENTS) {
      this.errorKey = 'chat_attachment_limit_reached';
      return;
    }
    this.busy = true;
    this.errorKey = '';
    let stagedPath: string = '';
    let helper: photoAccessHelper.PhotoAccessHelper | undefined;
    try {
      const options = new photoAccessHelper.PhotoSelectOptions();
      options.MIMEType = photoAccessHelper.PhotoViewMIMETypes.IMAGE_TYPE;
      options.maxSelectNumber = Math.min(MAX_IMAGES - currentImages, MAX_ATTACHMENTS - this.attachments.length);
      this.picking = true;
      const selected = await new photoAccessHelper.PhotoViewPicker().select(options);
      if (this.current(operation)) this.picking = false;
      if (!this.current(operation) || selected.photoUris.length === 0) return;
      helper = photoAccessHelper.getPhotoAccessHelper(operation.context);
      for (const photoUri of selected.photoUris) {
        if (!this.current(operation)) return;
        const predicates = new dataSharePredicates.DataSharePredicates().equalTo('uri', photoUri);
        const result = await helper.getAssets({ fetchColumns: ['uri', 'display_name', 'media_type'], predicates: predicates });
        let asset: photoAccessHelper.PhotoAsset | undefined;
        try {
          if (!this.current(operation)) return;
          if (result.getCount() !== 1) throw new Error('chat_attachment_image_unsupported');
          asset = await result.getFirstObject();
        } finally { result.close(); }
        if (!this.current(operation)) return;
        if (!asset || asset.photoType !== photoAccessHelper.PhotoType.IMAGE || asset.uri !== photoUri) throw new Error('chat_attachment_image_unsupported');
        if (this.attachments.filter((item: ChatAttachment) => item.kind === 'image').length >= MAX_IMAGES ||
          this.attachments.length >= MAX_ATTACHMENTS) throw new Error('chat_attachment_limit_reached');
        const item = new ChatAttachment();
        item.id = util.generateRandomUUID(); item.kind = 'image'; item.name = this.cleanName(asset.displayName || 'image');
        item.localPath = this.pathFor(item.id, item.name); stagedPath = item.localPath;
        const copied = this.copyIntoSandbox(asset.uri, item.localPath, MAX_IMAGE_BYTES);
        item.size = copied.size;
        item.previewUri = fileUri.getUriFromPath(item.localPath);
        const info = await this.inspectImage(item.localPath);
        if (!this.current(operation)) { this.removeOwnedCopy(item.localPath); stagedPath = ''; return; }
        item.mime = info.mime; item.width = info.width; item.height = info.height;
        item.revision = ++this.selectionRevision;
        const next = this.attachments.concat([item]);
        const storage = await this.openStorage(operation.context);
        if (!this.current(operation)) { this.removeOwnedCopy(item.localPath); stagedPath = ''; return; }
        const saved = await this.changeDraft(operation.target, storage, (draft: AttachmentDraft): void => {
          if (draft.attachments.length >= MAX_ATTACHMENTS || item.kind === 'image' && draft.attachments.filter((entry: ChatAttachment) => entry.kind === 'image').length >= MAX_IMAGES) throw new Error('chat_attachment_limit_reached');
          item.revision = Math.max(draft.selectionRevision + 1, item.revision); draft.selectionRevision = item.revision;
          draft.attachments = draft.attachments.concat([item]);
        });
        stagedPath = '';
        if (!this.current(operation)) return;
        this.selectionRevision = saved.selectionRevision; this.attachments = saved.attachments;
      }
    } catch (error) {
      if (stagedPath) this.removeOwnedCopy(stagedPath);
      if (this.current(operation)) this.errorKey = this.errorKeyFor(error, 'chat_attachment_pick_failed');
    } finally { try { await helper?.release(); } catch (_) {} if (this.current(operation)) { this.busy = false; this.picking = false; } }
  }

  /** The system photo editor receives a read grant; its result is a separate selected attachment. */
  async editPhoto(id: string): Promise<void> {
    const operation = this.capture(), original = this.attachments.find((item: ChatAttachment) => item.id === id);
    if (!operation || this.busy || !original || original.kind !== 'image') return;
    if (this.attachments.length >= MAX_ATTACHMENTS || this.attachments.filter((item: ChatAttachment) => item.kind === 'image').length >= MAX_IMAGES) { this.errorKey = 'chat_attachment_limit_reached'; return; }
    this.busy = true; this.picking = true; this.errorKey = ''; let staged = '';
    try {
      const uri = await new Promise<string>((resolve, reject): void => {
        const callback: common.AbilityStartCallback = {
          onError: (): void => reject(new Error('preview_unavailable')),
          onResult: (result): void => resolve(result.resultCode === 0 ? result.want?.uri || '' : '')
        };
        operation.context.startAbilityByType('photoEditor', Wire.payload(['ability.params.stream', 'ability.want.params.uriPermissionFlag'],
          [[original.previewUri], wantConstant.Flags.FLAG_AUTH_READ_URI_PERMISSION]), callback).catch(() => reject(new Error('preview_unavailable')));
      });
      if (!uri || !this.current(operation)) return;
      const item = new ChatAttachment(); item.id = util.generateRandomUUID(); item.kind = 'image'; item.name = original.name;
      item.localPath = this.pathFor(item.id, item.name); staged = item.localPath;
      item.size = this.copyIntoSandbox(uri, staged, MAX_IMAGE_BYTES).size; item.previewUri = fileUri.getUriFromPath(staged);
      const info = await this.inspectImage(staged); if (!this.current(operation)) return;
      item.mime = info.mime; item.width = info.width; item.height = info.height;
      const storage = await this.openStorage(operation.context); if (!this.current(operation)) return;
      const saved = await this.changeDraft(operation.target, storage, (draft: AttachmentDraft): void => {
        if (draft.attachments.length >= MAX_ATTACHMENTS || draft.attachments.filter((entry: ChatAttachment) => entry.kind === 'image').length >= MAX_IMAGES) throw new Error('chat_attachment_limit_reached');
        item.revision = draft.selectionRevision + 1; draft.selectionRevision = item.revision; draft.attachments.push(item);
      });
      staged = ''; if (this.current(operation)) { this.selectionRevision = saved.selectionRevision; this.attachments = saved.attachments; }
    } catch (error) { if (this.current(operation)) this.errorKey = this.errorKeyFor(error, 'preview_unavailable'); }
    finally { if (staged) this.removeOwnedCopy(staged); if (this.current(operation)) { this.busy = false; this.picking = false; } }
  }

  /** Edit a received image into a new draft attachment bound to its captured chat owner. */
  async editReceivedImage(data: ArrayBuffer, mime: string, expectedTarget: string, expectedScope: string,
    expectedGeneration: number, expectedHub: string): Promise<void> {
    const operation = this.capture();
    if (!operation || this.busy) return;
    const model = operation.model;
    const profile = model.profile;
    const pairingRevision = model.pairingRevision(), profileId = profile.id, profileHub = profile.hubId;
    const profilePin = profile.pin;
    let storage: LocalStore | undefined = this.storage;
    let localEpoch: number | undefined;
    let acceptedGeneration = expectedGeneration;
    let editorPending = false;
    const key = this.key(operation.target);
    const localStoreCurrent = (): boolean => !!storage && localEpoch !== undefined && storage.accepts(key, localEpoch);
    const stableOwnerCurrent = (): boolean => this.current(operation) && operation.modelTarget === expectedTarget &&
      operation.scope === expectedScope && model.scope() === expectedScope && model.target() === expectedTarget &&
      model.pairingRevision() === pairingRevision && model.profile.id === profileId && model.profile.hubId === profileHub &&
      model.profile.pin === profilePin &&
      (model.profile.hubId || model.profile.id) === expectedHub && localStoreCurrent();
    const ownerCurrent = (): boolean => stableOwnerCurrent() && model.connectionGeneration() === acceptedGeneration &&
      model.owns(acceptedGeneration, expectedScope);
    const initialOwnerCurrent = (): boolean => this.receivedOwnerCurrent(operation, expectedTarget, expectedScope,
      expectedGeneration, expectedHub) && stableOwnerCurrent();
    const editorOwnerCurrent = (): boolean => {
      if (!stableOwnerCurrent()) return false;
      const generation = model.connectionGeneration();
      if (generation === acceptedGeneration) return model.owns(acceptedGeneration, expectedScope);
      return editorPending && this.picking && model.owns(generation, expectedScope);
    };
    if (!this.receivedOwnerCurrent(operation, expectedTarget, expectedScope, expectedGeneration, expectedHub)) return;
    if (!data.byteLength || data.byteLength > MAX_IMAGE_BYTES) { this.errorKey = 'chat_attachment_image_too_large'; return; }
    if (mime !== 'image/jpeg' && mime !== 'image/png') { this.errorKey = 'chat_attachment_image_unsupported'; return; }
    if (this.attachments.length >= MAX_ATTACHMENTS || this.attachments.filter((item: ChatAttachment) => item.kind === 'image').length >= MAX_IMAGES) {
      this.errorKey = 'chat_attachment_limit_reached'; return;
    }

    this.busy = true;
    this.picking = true;
    this.errorKey = '';
    let stagedSource = '';
    let stagedResult = '';
    let stagedAttachment = '';
    try {
      if (!storage) storage = await this.openStorage(operation.context);
      if (!storage) return;
      if (!this.receivedOwnerCurrent(operation, expectedTarget, expectedScope, expectedGeneration, expectedHub) ||
        model.pairingRevision() !== pairingRevision) return;
      localEpoch = storage.revision(key);
      if (!initialOwnerCurrent()) return;
      const sourceID = util.generateRandomUUID();
      const sourceName = mime === 'image/png' ? 'received.png' : 'received.jpg';
      stagedSource = this.pathFor(sourceID, sourceName);
      this.writeOwnedBytes(data, stagedSource, MAX_IMAGE_BYTES);
      if (!initialOwnerCurrent()) return;
      const sourceInfo = await this.inspectImage(stagedSource);
      if (!initialOwnerCurrent()) return;
      if (sourceInfo.mime !== mime) throw new Error('chat_attachment_image_unsupported');
      const sourceUri = fileUri.getUriFromPath(stagedSource);
      editorPending = true;
      const editedUri = await new Promise<string>((resolve, reject): void => {
        const callback: common.AbilityStartCallback = {
          onError: (): void => reject(new Error('preview_unavailable')),
          onResult: (result): void => resolve(result.resultCode === 0 ? result.want?.uri || '' : '')
        };
        operation.context.startAbilityByType('photoEditor', Wire.payload(['ability.params.stream', 'ability.want.params.uriPermissionFlag'],
          [[sourceUri], wantConstant.Flags.FLAG_AUTH_READ_URI_PERMISSION]), callback)
          .catch(() => reject(new Error('preview_unavailable')));
      });
      if (!editedUri || !editorOwnerCurrent()) return;
      acceptedGeneration = model.connectionGeneration();
      editorPending = false;
      if (!ownerCurrent()) return;

      const resultID = util.generateRandomUUID();
      stagedResult = this.pathFor(resultID, 'received.bin');
      this.copyIntoSandbox(editedUri, stagedResult, MAX_IMAGE_BYTES);
      if (!ownerCurrent()) return;
      const resultInfo = await this.inspectImage(stagedResult);
      if (!ownerCurrent()) return;
      const item = new ChatAttachment();
      item.id = util.generateRandomUUID();
      item.kind = 'image';
      item.name = resultInfo.mime === 'image/png' ? 'image.png' : 'image.jpg';
      item.localPath = this.pathFor(item.id, item.name);
      stagedAttachment = item.localPath;
      item.size = this.copyIntoSandbox(stagedResult, stagedAttachment, MAX_IMAGE_BYTES).size;
      item.previewUri = fileUri.getUriFromPath(item.localPath);
      item.mime = resultInfo.mime;
      item.width = resultInfo.width;
      item.height = resultInfo.height;
      if (!ownerCurrent()) return;
      const saved = await this.changeDraft(operation.target, storage, (draft: AttachmentDraft): void => {
        if (draft.attachments.length >= MAX_ATTACHMENTS ||
          draft.attachments.filter((entry: ChatAttachment) => entry.kind === 'image').length >= MAX_IMAGES)
          throw new Error('chat_attachment_limit_reached');
        item.revision = draft.selectionRevision + 1;
        draft.selectionRevision = item.revision;
        draft.attachments = draft.attachments.concat([item]);
      }, ownerCurrent);
      stagedAttachment = '';
      if (!ownerCurrent()) return;
      this.selectionRevision = saved.selectionRevision;
      this.attachments = saved.attachments;
    } catch (error) {
      if (ownerCurrent()) this.errorKey = this.errorKeyFor(error as Object, 'preview_unavailable');
    } finally {
      editorPending = false;
      if (stagedSource) this.removeOwnedCopy(stagedSource);
      if (stagedResult) this.removeOwnedCopy(stagedResult);
      if (stagedAttachment) this.removeOwnedCopy(stagedAttachment);
      if (this.current(operation)) { this.busy = false; this.picking = false; }
    }
  }

  /** Open the native document picker. Files are copied locally now and uploaded only by prepareSend. */
  async chooseFiles(): Promise<void> {
    const operation = this.capture();
    if (!operation || this.busy) return;
    if (this.attachments.length >= MAX_ATTACHMENTS) {
      this.errorKey = 'chat_attachment_limit_reached';
      return;
    }
    this.busy = true;
    this.errorKey = '';
    let stagedPath: string = '';
    try {
      const options = new picker.DocumentSelectOptions();
      options.maxSelectNumber = MAX_ATTACHMENTS - this.attachments.length;
      this.picking = true;
      const uris = await new picker.DocumentViewPicker(operation.context).select(options);
      if (this.current(operation)) this.picking = false;
      if (!this.current(operation)) return;
      for (const uri of uris) {
        if (!this.current(operation)) return;
        if (this.attachments.length >= MAX_ATTACHMENTS) throw new Error('chat_attachment_limit_reached');
        const info = fileIo.statSync(uri);
        if (info.size <= 0) throw new Error('chat_attachment_file_read_failed');
        if (info.size > MAX_FILE_BYTES) throw new Error('chat_attachment_file_too_large');
        const item = new ChatAttachment();
        item.id = util.generateRandomUUID(); item.kind = 'file'; item.name = this.pickedName(uri);
        item.mime = attachmentMimeType(item.name);
        item.localPath = this.pathFor(item.id, item.name); stagedPath = item.localPath;
        const copied = this.copyIntoSandbox(uri, item.localPath, MAX_FILE_BYTES);
        item.size = copied.size;
        item.previewUri = '';
        item.status = 'selected';
        item.revision = ++this.selectionRevision;
        const next = this.attachments.concat([item]);
        const storage = await this.openStorage(operation.context);
        if (!this.current(operation)) { this.removeOwnedCopy(item.localPath); stagedPath = ''; return; }
        const saved = await this.changeDraft(operation.target, storage, (draft: AttachmentDraft): void => {
          if (draft.attachments.length >= MAX_ATTACHMENTS || item.kind === 'image' && draft.attachments.filter((entry: ChatAttachment) => entry.kind === 'image').length >= MAX_IMAGES) throw new Error('chat_attachment_limit_reached');
          item.revision = Math.max(draft.selectionRevision + 1, item.revision); draft.selectionRevision = item.revision;
          draft.attachments = draft.attachments.concat([item]);
        });
        stagedPath = '';
        if (!this.current(operation)) return;
        this.selectionRevision = saved.selectionRevision; this.attachments = saved.attachments;
      }
    } catch (error) {
      if (stagedPath) this.removeOwnedCopy(stagedPath);
      if (this.current(operation)) this.errorKey = this.errorKeyFor(error, 'chat_attachment_pick_failed');
    } finally { if (this.current(operation)) { this.busy = false; this.picking = false; } }
  }

  /** Remove one selected item and only its copy inside this model's own attachment directory. */
  async remove(id: string): Promise<void> {
    const operation = this.capture();
    if (!operation || this.busy) return;
    const removed = this.attachments.find((item: ChatAttachment) => item.id === id);
    if (!removed) return;
    this.busy = true;
    try {
      const storage = await this.openStorage(operation.context);
      const saved = await this.changeDraft(operation.target, storage, (draft: AttachmentDraft): void => {
        draft.attachments = draft.attachments.filter((item: ChatAttachment) => item.id !== removed.id || item.revision !== removed.revision);
      });
      if (this.current(operation)) this.attachments = saved.attachments;
      this.removeOwnedCopy(removed.localPath);
      if (this.current(operation)) this.errorKey = '';
    } catch (_) { if (this.current(operation)) this.errorKey = 'chat_attachment_storage_unavailable'; }
    finally { if (this.current(operation)) this.busy = false; }
  }

  /** Prepare images and upload files for this exact target. Uploads are explicit and never auto-retried if uncertain. */
  async prepareSend(text: string): Promise<PreparedAttachmentSend> {
    const operation = this.capture();
    if (!operation || this.busy) throw new Error('chat_attachment_target_unavailable');
    if (!operation.model.canSend()) throw new Error('chat_attachment_offline');
    const repository = operation.model.repo();
    this.busy = true;
    this.errorKey = '';
    const selected = this.attachments.map((item: ChatAttachment) => new AttachmentRevision(item.id, item.revision));
    try {
    const storage = await this.openStorage(operation.context);
    const latest = await this.changeDraft(operation.target, storage, (_draft: AttachmentDraft): void => {});
    if (!this.connected(operation, repository) || selected.length !== latest.attachments.length ||
      !selected.every((item: AttachmentRevision) => latest.attachments.some((saved: ChatAttachment) => saved.id === item.id && saved.revision === item.revision))) {
      if (this.current(operation)) this.busy = false;
      throw new Error('chat_attachment_selection_changed');
    }
    const captured = this.attachments.map((item: ChatAttachment) => this.copyAttachment(item));
    const snapshot = new AttachmentSendSnapshot();
    snapshot.target = operation.target; snapshot.selectionRevision = this.selectionRevision;
    snapshot.attachments = captured.map((item: ChatAttachment) => new AttachmentRevision(item.id, item.revision));
      const uploaded: ChatAttachment[] = [];
      for (const item of captured) {
        if (item.kind !== 'file') { uploaded.push(item); continue; }
        const current = this.attachments.find((candidate: ChatAttachment) => candidate.id === item.id && candidate.revision === item.revision);
        if (!current) throw new Error('chat_attachment_selection_changed');
        if (current.status === 'unknown' || current.status === 'uploading' || (current.status === 'ready' && !current.remotePath))
          throw new Error('chat_attachment_upload_unknown');
        if (current.status === 'ready' && current.remotePath) { uploaded.push(this.copyAttachment(current)); continue; }
        if (!this.connected(operation, repository)) throw new Error('chat_attachment_offline');
        const bytes = this.readOwnedFile(current.localPath, MAX_FILE_BYTES);
        const uploading = this.copyAttachment(current); uploading.status = 'uploading';
        await this.replaceItemAndSave(operation, uploading);
        if (!this.connected(operation, repository)) {
          if (this.current(operation)) await this.replaceItemAndSave(operation, current);
          throw new Error('chat_attachment_offline');
        }
        let receipt: Record<string, Object>;
        try {
          const path = '/api/v1/machines/' + encodeURIComponent(operation.row.machine) + '/files?name=' +
            encodeURIComponent(current.name) + '&dest=' + (Wire.text(operation.row.data(), 'space') === 'health' ? 'health' : 'terminal');
          receipt = await repository.upload(path, bytes, (): boolean => this.connected(operation, repository));
        } catch (error) {
          if (!this.current(operation)) throw new Error('scope_changed');
          const failed = this.copyAttachment(current);
          failed.status = this.uncertain(error) ? 'unknown' : 'failed';
          await this.replaceItemAndSave(operation, failed);
          if (failed.status === 'unknown') throw new Error('chat_attachment_upload_unknown');
          throw this.asError(error, 'chat_attachment_upload_failed');
        }
        if (!this.connected(operation, repository)) throw new Error('scope_changed');
        const remotePath = Wire.text(receipt, 'path');
        const remoteName = Wire.text(receipt, 'name');
        if (!this.absoluteRemotePath(remotePath) || !remoteName) {
          const unknown = this.copyAttachment(current); unknown.status = 'unknown';
          await this.replaceItemAndSave(operation, unknown);
          throw new Error('chat_attachment_upload_unknown');
        }
        const ready = this.copyAttachment(current); ready.status = 'ready'; ready.remotePath = remotePath; ready.remoteName = remoteName;
        await this.replaceItemAndSave(operation, ready);
        uploaded.push(ready);
      }
      if (!this.connected(operation, repository)) throw new Error('scope_changed');
      const filePaths = uploaded.filter((item: ChatAttachment) => item.kind === 'file').map((item: ChatAttachment) => item.remotePath);
      const messageText = this.appendPaths(text, filePaths);
      const selectedImages = uploaded.filter((item: ChatAttachment) => item.kind === 'image');
      const imagePayloads = await this.encodeImages(selectedImages, messageText, operation);
      if (!this.connected(operation, repository)) throw new Error('scope_changed');
      if (snapshot.attachments.length !== this.attachments.length || !snapshot.attachments.every((item: AttachmentRevision) =>
        this.attachments.some((selected: ChatAttachment) => selected.id === item.id && selected.revision === item.revision))) throw new Error('chat_attachment_selection_changed');
      const prepared = new PreparedAttachmentSend();
      prepared.target = operation.target; prepared.modelTarget = operation.modelTarget; prepared.scope = operation.scope;
      prepared.generation = operation.generation; prepared.row = operation.row; prepared.repository = repository;
      prepared.text = messageText; prepared.images = imagePayloads; prepared.snapshot = snapshot;
      return prepared;
    } catch (error) {
      if (this.current(operation)) this.errorKey = this.errorKeyFor(error, 'chat_attachment_send_failed');
      throw this.asError(error, 'chat_attachment_send_failed');
    } finally { if (this.current(operation)) this.busy = false; }
  }

  /** Clear only revisions captured by a successfully sent payload. */
  async committed(snapshot: AttachmentSendSnapshot, valid: () => boolean = (): boolean => true): Promise<boolean> {
    if (!valid()) return false;
    const storage = this.storage;
    if (!snapshot || !snapshot.target || snapshot.attachments.length === 0) return true;
    if (!storage) return false;
    for (const instance of ChatAttachments.instances) if (instance.opened && instance.target === snapshot.target) {
      instance.attachments = instance.attachments.filter((item: ChatAttachment) => !snapshot.attachments.some((captured: AttachmentRevision) => captured.id === item.id && captured.revision === item.revision));
      instance.busy = true;
    }
    try {
      const removed: ChatAttachment[] = [];
      const saved = await this.changeDraft(snapshot.target, storage, (draft: AttachmentDraft): void => {
        draft.attachments = draft.attachments.filter((item: ChatAttachment) => {
          const exact = snapshot.attachments.some((captured: AttachmentRevision) => captured.id === item.id && captured.revision === item.revision);
          if (exact) removed.push(item);
          return !exact;
        });
      }, valid);
      if (!valid()) return false;
      const keep = saved.attachments;
      const currentTarget = this.opened && this.target === snapshot.target;
      if (currentTarget) this.attachments = keep;
      for (const item of removed) this.removeOwnedCopy(item.localPath);
      for (const instance of ChatAttachments.instances) if (instance.opened && instance.target === snapshot.target) { instance.errorKey = ''; instance.busy = false; }
      return true;
    } catch (_) {
      if (!valid()) return false;
      for (const instance of ChatAttachments.instances) if (instance.opened && instance.target === snapshot.target) { instance.errorKey = 'chat_attachment_storage_unavailable'; instance.busy = true; }
      return false;
    }
  }

  private capture(): AttachmentOperation | undefined {
    if (!this.opened || !this.model || !this.context || !this.row || !this.target) return undefined;
    return new AttachmentOperation(this.model, this.context, this.row, this.modelTarget, this.scope,
      this.model.connectionGeneration(), this.target, this.openRevision);
  }

  private current(operation: AttachmentOperation): boolean {
    return this.opened && this.openRevision === operation.openRevision && this.model === operation.model &&
      this.target === operation.target && operation.model.scope() === operation.scope &&
      operation.model.target() === operation.modelTarget && operation.model.target(operation.row) === operation.modelTarget;
  }

  private receivedOwnerCurrent(operation: AttachmentOperation, expectedTarget: string, expectedScope: string,
    expectedGeneration: number, expectedHub: string): boolean {
    const model = operation.model;
    return this.current(operation) && operation.modelTarget === expectedTarget && operation.scope === expectedScope &&
      operation.generation === expectedGeneration && model.connectionGeneration() === expectedGeneration &&
      model.scope() === expectedScope && model.target() === expectedTarget && model.owns(expectedGeneration, expectedScope) &&
      (model.profile.hubId || model.profile.id) === expectedHub;
  }

  private connected(operation: AttachmentOperation, repository: HubRepository): boolean {
    if (!this.current(operation) || !operation.model.owns(operation.generation, operation.scope) || !operation.model.canSend()) return false;
    try { return operation.model.repo() === repository; } catch (_) { return false; }
  }

  private ensureStorageDirectory(): void {
    try { fileIo.statSync(this.storageDirectory); }
    catch (_) { fileIo.mkdirSync(this.storageDirectory, true); }
  }

  private pathFor(id: string, name: string): string { return this.storageDirectory + '/' + id + '.' + attachmentExtension(name); }

  private ownedPath(path: string): boolean {
    return !!this.storageDirectory && path.startsWith(this.storageDirectory + '/') && isAttachmentCopyName(path.substring(this.storageDirectory.length + 1));
  }

  private removeOwnedCopy(path: string): void {
    if (!this.ownedPath(path)) return;
    try { fileIo.unlinkSync(path); } catch (_) { /* An already removed attachment copy is safe. */ }
  }

  private writeOwnedBytes(data: ArrayBuffer, destination: string, limit: number): number {
    if (!this.ownedPath(destination) || !data.byteLength) throw new Error('chat_attachment_file_read_failed');
    if (data.byteLength > limit) throw new Error('chat_attachment_image_too_large');
    this.ensureStorageDirectory();
    let outputFd = -1;
    try {
      const output = fileIo.openSync(destination, fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.CREATE | fileIo.OpenMode.TRUNC);
      outputFd = output.fd;
      let offset = 0;
      while (offset < data.byteLength) {
        const length = Math.min(COPY_CHUNK_BYTES, data.byteLength - offset);
        const chunk = data.slice(offset, offset + length);
        const written = fileIo.writeSync(outputFd, chunk, { length: length });
        if (written !== length) throw new Error('chat_attachment_file_read_failed');
        offset += length;
      }
      return offset;
    } catch (error) {
      this.removeOwnedCopy(destination);
      throw this.asError(error as Object, 'chat_attachment_file_read_failed');
    } finally { if (outputFd >= 0) fileIo.closeSync(outputFd); }
  }

  private copyIntoSandbox(source: string, destination: string, limit: number): CopiedFile {
    const info = fileIo.statSync(source);
    if (!Number.isSafeInteger(info.size) || info.size <= 0) throw new Error('chat_attachment_file_read_failed');
    if (info.size > limit) throw new Error(limit === MAX_FILE_BYTES ? 'chat_attachment_file_too_large' : 'chat_attachment_image_too_large');
    this.ensureStorageDirectory();
    const input = fileIo.openSync(source, fileIo.OpenMode.READ_ONLY);
    let outputFd: number = -1;
    try {
      const output = fileIo.openSync(destination, fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.CREATE | fileIo.OpenMode.TRUNC);
      outputFd = output.fd;
      let copied = 0;
      while (copied < info.size) {
        const length = Math.min(COPY_CHUNK_BYTES, info.size - copied);
        const chunk = new ArrayBuffer(length);
        const read = fileIo.readSync(input.fd, chunk, { length: length });
        if (read <= 0 || read > length) throw new Error('chat_attachment_file_read_failed');
        const data = read === length ? chunk : chunk.slice(0, read);
        const written = fileIo.writeSync(outputFd, data, { length: read });
        if (written !== read) throw new Error('chat_attachment_file_read_failed');
        copied += read;
      }
      if (copied !== info.size) throw new Error('chat_attachment_file_read_failed');
      return { size: copied };
    } catch (error) {
      this.removeOwnedCopy(destination);
      throw this.asError(error, 'chat_attachment_file_read_failed');
    } finally {
      if (outputFd >= 0) fileIo.closeSync(outputFd);
      fileIo.closeSync(input.fd);
    }
  }

  private readOwnedFile(path: string, limit: number): ArrayBuffer {
    if (!this.ownedPath(path)) throw new Error('chat_attachment_file_read_failed');
    const stat = fileIo.statSync(path);
    if (!Number.isSafeInteger(stat.size) || stat.size <= 0) throw new Error('chat_attachment_file_read_failed');
    if (stat.size > limit) throw new Error('chat_attachment_file_too_large');
    const file = fileIo.openSync(path, fileIo.OpenMode.READ_ONLY);
    try {
      const data = new ArrayBuffer(stat.size);
      let offset = 0;
      while (offset < stat.size) {
        const chunk = new ArrayBuffer(Math.min(COPY_CHUNK_BYTES, stat.size - offset));
        const read = fileIo.readSync(file.fd, chunk, { length: chunk.byteLength });
        if (read <= 0) throw new Error('chat_attachment_file_read_failed');
        new Uint8Array(data, offset, read).set(new Uint8Array(chunk, 0, read));
        offset += read;
      }
      if (offset !== stat.size) throw new Error('chat_attachment_file_read_failed');
      return data;
    } finally { fileIo.closeSync(file.fd); }
  }

  private async inspectImage(path: string): Promise<DecodedAttachmentImage> {
    let source: image.ImageSource | undefined;
    try {
      source = image.createImageSource(path);
      const info = await source.getImageInfo();
      if (info.mimeType !== 'image/jpeg' && info.mimeType !== 'image/png') throw new Error('chat_attachment_image_unsupported');
      if (!Number.isSafeInteger(info.size.width) || !Number.isSafeInteger(info.size.height) || info.size.width <= 0 || info.size.height <= 0 ||
        info.size.width * info.size.height > MAX_IMAGE_PIXELS) throw new Error('chat_attachment_image_too_large');
      return { mime: info.mimeType, width: info.size.width, height: info.size.height };
    } catch (error) {
      if (error instanceof Error && error.message.startsWith('chat_attachment_')) throw error;
      throw new Error('chat_attachment_image_unsupported');
    } finally { await source?.release(); }
  }

  private async encodeImages(items: ChatAttachment[], text: string, operation: AttachmentOperation): Promise<ChatImagePayload[]> {
    if (this.byteLength(text) > TEXT_LIMIT) throw new Error('chat_attachment_request_too_large');
    if (items.length === 0) {
      if (this.requestBytes(text, []) >= REQUEST_LIMIT) throw new Error('chat_attachment_request_too_large');
      return [];
    }
    const mimeList = items.map((item: ChatAttachment) => item.mime);
    const empty = mimeList.map((mime: string) => new ChatImagePayload(mime, ''));
    const availableBase64Chars = REQUEST_LIMIT - 1 - this.requestBytes(text, empty);
    const totalRawBudget = Math.floor(Math.max(0, availableBase64Chars - items.length * 4) * 3 / 4);
    if (totalRawBudget <= 0) throw new Error('chat_attachment_request_too_large');
    const result: ChatImagePayload[] = [];
    let remaining = totalRawBudget;
    for (let index = 0; index < items.length; index++) {
      if (!this.current(operation)) throw new Error('scope_changed');
      const item = items[index];
      const itemBudget = Math.floor(remaining / (items.length - index));
      const encoded = await this.encodeImage(item, itemBudget);
      if (!this.current(operation)) throw new Error('scope_changed');
      const base64 = new util.Base64Helper().encodeToStringSync(new Uint8Array(encoded.data));
      const payload = new ChatImagePayload(encoded.mime, base64);
      result.push(payload);
      remaining -= encoded.data.byteLength;
      if (remaining < 0) throw new Error('chat_attachment_request_too_large');
    }
    if (this.requestBytes(text, result) >= REQUEST_LIMIT) throw new Error('chat_attachment_request_too_large');
    return result;
  }

  private async encodeImage(item: ChatAttachment, byteBudget: number): Promise<EncodedAttachmentImage> {
    let source: image.ImageSource | undefined;
    let packer: image.ImagePacker | undefined;
    let pixels: image.PixelMap | undefined;
    try {
      source = image.createImageSource(item.localPath);
      const info = await source.getImageInfo();
      if (info.mimeType !== 'image/jpeg' && info.mimeType !== 'image/png') throw new Error('chat_attachment_image_unsupported');
      if (info.mimeType !== item.mime || info.size.width <= 0 || info.size.height <= 0 || info.size.width * info.size.height > MAX_IMAGE_PIXELS)
        throw new Error('chat_attachment_image_unsupported');
      const maxDimension = Math.max(info.size.width, info.size.height);
      let edge = Math.min(MAX_IMAGE_EDGE, maxDimension);
      let quality = 75;
      packer = image.createImagePacker();
      for (let attempt = 0; attempt < 12; attempt++) {
        const scale = Math.min(1, edge / maxDimension);
        const desired: AttachmentImageSize = {
          width: Math.max(1, Math.round(info.size.width * scale)),
          height: Math.max(1, Math.round(info.size.height * scale))
        };
        pixels = await source.createPixelMap({ desiredSize: desired });
        const data = await packer.packToData(pixels, { format: info.mimeType, quality: quality });
        await pixels.release(); pixels = undefined;
        if (data.byteLength > 0 && data.byteLength <= byteBudget) return { mime: info.mimeType, data: data };
        if (info.mimeType === 'image/jpeg' && quality > 45) quality -= 10;
        else { edge = Math.max(96, Math.floor(edge * 0.75)); quality = 75; }
      }
      throw new Error('chat_attachment_request_too_large');
    } catch (error) {
      if (error instanceof Error && error.message.startsWith('chat_attachment_')) throw error;
      throw new Error('chat_attachment_image_unsupported');
    } finally {
      await pixels?.release();
      await packer?.release();
      await source?.release();
    }
  }

  private byteLength(text: string): number { return util.TextEncoder.create('utf-8').encodeInto(text).byteLength; }

  private requestBytes(text: string, images: ChatImagePayload[]): number {
    const body = Wire.payload(['text', 'client_msg_id'], [text, '00000000-0000-0000-0000-000000000000']);
    if (images.length > 0) body['images'] = images.map((item: ChatImagePayload): Object =>
      Wire.payload(['mime', 'data'], [item.mime, item.data]));
    return this.byteLength(JSON.stringify(body));
  }

  private appendPaths(text: string, paths: string[]): string {
    if (paths.length === 0) return text;
    const refs = paths.map((path: string) => this.quotePath(path)).join(' ');
    const base = text.trimEnd();
    return base ? base + ' ' + refs : refs;
  }

  private quotePath(path: string): string {
    if (!/[\s'\"]/.test(path)) return path;
    return "'" + path.replace(/'/g, "'\\''") + "'";
  }

  private absoluteRemotePath(path: string): boolean { return path.startsWith('/') || /^[A-Za-z]:[\\/]/.test(path); }

  private uncertain(error: Object): boolean {
    const value = error as Record<string, Object>;
    return value['resultUnknown'] === true || value['resultPending'] === true;
  }

  private errorKeyFor(error: Object, fallback: string): string {
    if (!(error instanceof Error)) return fallback;
    const known = [
      'chat_attachment_image_unsupported', 'chat_attachment_image_too_large', 'chat_attachment_file_too_large',
      'chat_attachment_file_read_failed', 'chat_attachment_upload_unknown', 'chat_attachment_storage_unavailable',
      'chat_attachment_request_too_large', 'chat_attachment_limit_reached', 'chat_attachment_target_unavailable',
      'chat_attachment_offline', 'chat_attachment_selection_changed', 'scope_changed'
    ];
    if (known.includes(error.message)) return error.message === 'scope_changed' ? 'chat_attachment_target_unavailable' : error.message;
    const value = error as UploadOutcomeFlags;
    if (value.resultUnknown === true || value.resultPending === true) return 'chat_attachment_upload_unknown';
    return fallback;
  }

  private asError(value: Object, fallback: string): Error {
    if (value instanceof Error) return value;
    return new Error(fallback);
  }

  private cleanName(value: string): string {
    const cleaned = value.replace(/[\\/\u0000-\u001f\u007f]/g, '_').trim().slice(0, 255);
    return cleaned || 'upload';
  }

  private pickedName(uri: string): string {
    try { return this.cleanName(new fileUri.FileUri(uri).name); }
    catch (_) {
      const leaf = uri.substring(uri.lastIndexOf('/') + 1).split('?')[0];
      try { return this.cleanName(decodeURIComponent(leaf)); } catch (_) { return this.cleanName(leaf); }
    }
  }

  private copyAttachment(item: ChatAttachment): ChatAttachment {
    const copy = new ChatAttachment();
    copy.id = item.id; copy.kind = item.kind; copy.name = item.name; copy.localPath = item.localPath;
    copy.previewUri = item.previewUri; copy.mime = item.mime; copy.size = item.size; copy.width = item.width;
    copy.height = item.height; copy.status = item.status; copy.remotePath = item.remotePath;
    copy.remoteName = item.remoteName; copy.revision = item.revision;
    return copy;
  }

  private async replaceItemAndSave(operation: AttachmentOperation, replacement: ChatAttachment): Promise<void> {
    if (!this.current(operation)) throw new Error('scope_changed');
    const storage = await this.openStorage(operation.context);
    if (!this.current(operation)) throw new Error('scope_changed');
    const saved = await this.changeDraft(operation.target, storage, (draft: AttachmentDraft): void => {
      const found = draft.attachments.some((item: ChatAttachment) => item.id === replacement.id && item.revision === replacement.revision);
      if (!found) throw new Error('chat_attachment_selection_changed');
      draft.attachments = draft.attachments.map((item: ChatAttachment) => item.id === replacement.id && item.revision === replacement.revision ? replacement : item);
    });
    if (!this.current(operation)) throw new Error('scope_changed');
    this.attachments = saved.attachments;
  }

  private async changeDraft(target: string, storage: LocalStore, change: (draft: AttachmentDraft) => void, admit: () => boolean = (): boolean => true): Promise<AttachmentDraft> {
    const epoch = storage.revision(this.key(target));
    const valid = (): boolean => admit() && storage.accepts(this.key(target), epoch);
    let result = new AttachmentDraft();
    const previous = ChatAttachments.transactions.get(target) || Promise.resolve();
    const work = previous.catch(() => {}).then(async (): Promise<void> => {
      if (!valid()) throw new Error('scope_changed');
      await this.waitForPendingWrite(target);
      result = this.decodeDraft(await storage.get(this.key(target)), target);
      if (!valid()) throw new Error('scope_changed');
      change(result);
      await this.saveDraft(target, result.selectionRevision, result.attachments, storage, epoch, admit);
      if (!storage.accepts(this.key(target), epoch)) throw new Error('scope_changed');
      if (admit()) {
        for (const instance of ChatAttachments.instances) {
          const owner = instance.capture();
          if (instance.target === target && owner && instance.current(owner)) {
            instance.selectionRevision = result.selectionRevision;
            instance.attachments = result.attachments.map((item: ChatAttachment) => instance.copyAttachment(item));
          }
        }
      }
    });
    const tail = work.catch(() => {});
    ChatAttachments.transactions.set(target, tail);
    try { await work; } finally { if (ChatAttachments.transactions.get(target) === tail) ChatAttachments.transactions.delete(target); }
    return result;
  }

  private async saveDraft(target: string, revision: number, attachments: ChatAttachment[], storage: LocalStore, epoch: number,
    admit: () => boolean = (): boolean => true): Promise<void> {
    let raw = '';
    if (attachments.length > 0) {
      const rows = attachments.map((item: ChatAttachment): Object => Wire.payload(
        ['id', 'kind', 'name', 'localPath', 'previewUri', 'mime', 'size', 'width', 'height', 'status', 'remotePath', 'remoteName', 'revision'],
        [item.id, item.kind, item.name, item.localPath, item.previewUri, item.mime, item.size, item.width, item.height,
          item.status, item.remotePath, item.remoteName, item.revision]));
      const record = Wire.payload(['target', 'selectionRevision', 'attachments'], [target, revision, rows]);
      raw = JSON.stringify(record);
    }
    await this.queueDraftWrite(target, raw, storage, epoch, admit);
  }

  private decodeDraft(raw: string, target: string): AttachmentDraft {
    const draft = new AttachmentDraft(); draft.target = target;
    if (!raw) return draft;
    const data = Wire.parse(raw);
    if (Wire.text(data, 'target') !== target) throw new Error('chat_attachment_storage_unavailable');
    draft.selectionRevision = Wire.number(data, 'selectionRevision');
    draft.attachments = Wire.rows(data, 'attachments').map((value: Record<string, Object>) => {
      const item = new ChatAttachment();
      item.id = Wire.text(value, 'id'); item.kind = Wire.text(value, 'kind'); item.name = Wire.text(value, 'name');
      item.localPath = Wire.text(value, 'localPath'); item.previewUri = Wire.text(value, 'previewUri');
      item.mime = Wire.text(value, 'mime'); item.size = Wire.number(value, 'size'); item.width = Wire.number(value, 'width');
      item.height = Wire.number(value, 'height'); item.status = Wire.text(value, 'status', 'selected');
      item.remotePath = Wire.text(value, 'remotePath'); item.remoteName = Wire.text(value, 'remoteName'); item.revision = Wire.number(value, 'revision');
      if (!item.id || !['image', 'file'].includes(item.kind) || !this.ownedPath(item.localPath) || !item.name || item.revision <= 0)
        throw new Error('chat_attachment_storage_unavailable');
      if (item.kind === 'image' && !['image/jpeg', 'image/png'].includes(item.mime)) throw new Error('chat_attachment_storage_unavailable');
      if (!['selected', 'uploading', 'ready', 'unknown', 'failed'].includes(item.status)) item.status = 'unknown';
      return item;
    });
    if (draft.attachments.length > MAX_ATTACHMENTS || draft.attachments.filter((item: ChatAttachment) => item.kind === 'image').length > MAX_IMAGES)
      throw new Error('chat_attachment_storage_unavailable');
    return draft;
  }
}
