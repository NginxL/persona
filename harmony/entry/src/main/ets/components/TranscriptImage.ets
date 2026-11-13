import { common } from '@kit.AbilityKit';
import { image } from '@kit.ImageKit';
import type { LoadedMessageImage, MessageImageGalleryEntry, MessageImageGalleryWindow } from '../core/MessageImageGallery';
import { messageImageGalleryWindow, sameMessageImageSource } from '../core/MessageImageGallery';
import { loadMessageImageGallery } from '../models/MessageImageGallery';
import { loadMessageImage } from '../models/MessageImage';
import { previewMessageImageGallery } from '../platform/NativePreview';
import type { ClientModel } from '../models/ClientModel';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

@ComponentV2
export struct TranscriptImage {
  @Require @Param model: ClientModel;
  @Param active: boolean = true;
  @Param itemId: string = '';
  @Param remotePath: string = '';
  @Param sourceKey: string = '';
  @Param galleryEntries: MessageImageGalleryEntry[] = [];
  @Param galleryIndex: number = -1;
  @Event onEdit: (image: LoadedMessageImage) => void = (_image: LoadedMessageImage): void => {};
  private loaded: LoadedMessageImage | undefined;
  @Local pixels: image.PixelMap | undefined;
  @Local busy: boolean = false;
  @Local failed: boolean = false;
  @Local ratio: number = 1;
  private mounted: boolean = false;
  private revision: number = 0;
  private activityVisit: number = 0;
  private sourceVisit: number = 0;
  private galleryVisit: number = 0;
  private observedGallery: string = '';

  aboutToAppear(): void {
    this.mounted = true;
    this.activityVisit++;
    this.sourceVisit++;
    this.observedGallery = this.gallerySignature();
    this.load();
  }

  @Monitor('active') activityChanged(): void {
    this.activityVisit++;
    this.busy = false;
    if (this.loaded && !this.loadedOwnerCurrent()) this.resetThumbnail();
    if (this.active && this.mounted) this.load();
  }

  @Monitor('model.profile', 'model.connected') ownerChanged(): void {
    this.activityVisit++;
    this.sourceVisit++;
    this.resetThumbnail();
    if (this.model.connected && this.active && this.mounted) this.load();
  }

  @Monitor('itemId', 'remotePath', 'sourceKey') imageChanged(): void {
    this.revision++;
    this.activityVisit++;
    this.sourceVisit++;
    this.resetThumbnail();
    this.load();
  }

  @Monitor('galleryEntries', 'galleryIndex') galleryChanged(): void {
    const signature = this.gallerySignature();
    if (signature === this.observedGallery) return;
    this.observedGallery = signature;
    this.galleryVisit++;
    this.activityVisit++;
    this.sourceVisit++;
    this.busy = false;
    if (this.active && this.mounted) this.load();
  }

  private loadedOwnerCurrent(): boolean {
    const loaded = this.loaded;
    return !!loaded && loaded.target === this.model.target() && loaded.scope === this.model.scope() &&
      loaded.generation === this.model.connectionGeneration() && loaded.hub === (this.model.profile.hubId || this.model.profile.id) &&
      this.model.connected && this.model.owns(loaded.generation, loaded.scope);
  }

  private resetThumbnail(): void {
    this.pixels?.release(); this.pixels = undefined; this.loaded = undefined;
    this.busy = false; this.failed = false;
  }

  private sourceEntry(): MessageImageGalleryEntry {
    const key = this.sourceKey || JSON.stringify([this.itemId, this.remotePath]);
    return { key: key, itemId: this.itemId, remotePath: this.remotePath, target: this.model.target() };
  }

  private galleryWindow(): MessageImageGalleryWindow | undefined {
    if (this.galleryEntries.length === 0) return messageImageGalleryWindow([this.sourceEntry()], 0);
    return messageImageGalleryWindow(this.galleryEntries, this.galleryIndex);
  }

  private gallerySignature(): string { return this.galleryWindow()?.signature || ''; }

  private current(model: ClientModel, revision: number, visit: number, generation: number,
    scope: string, target: string, hub: string, galleryVisit: number, gallerySignature: string): boolean {
    return this.active && this.activityVisit === visit &&
      this.currentOwner(model, revision, this.sourceVisit, generation, scope, target, hub, galleryVisit, gallerySignature);
  }

  private currentOwner(model: ClientModel, revision: number, sourceVisit: number, generation: number,
    scope: string, target: string, hub: string, galleryVisit: number, gallerySignature: string): boolean {
    return this.mounted && this.model === model && this.revision === revision && this.sourceVisit === sourceVisit &&
      this.galleryVisit === galleryVisit && model.connected && model.connectionGeneration() === generation &&
      model.scope() === scope && model.target() === target && model.owns(generation, scope) &&
      (model.profile.hubId || model.profile.id) === hub && (!gallerySignature || this.gallerySignature() === gallerySignature);
  }

  private async load(): Promise<void> {
    if (this.pixels || this.busy || !this.active || !this.mounted || !this.model.connected || !this.model.isChat()) return;
    const model = this.model;
    const revision = this.revision;
    const visit = this.activityVisit;
    const target = model.target();
    const generation = model.connectionGeneration();
    const scope = model.scope();
    const hub = model.profile.hubId || model.profile.id;
    const galleryVisit = this.galleryVisit;
    const valid = (): boolean => this.current(model, revision, visit, generation, scope, target, hub, galleryVisit, '');
    this.busy = true;
    this.failed = false;
    let source: image.ImageSource | undefined;
    try {
      const response = await loadMessageImage(model, this.itemId, this.remotePath);
      if (!valid()) return;
      source = image.createImageSource(response.data);
      const info = await source.getImageInfo();
      if (!valid()) return;
      if (info.size.width <= 0 || info.size.height <= 0 || info.size.width * info.size.height > 40000000)
        throw new Error('response_too_large');
      const scale = Math.min(1, 1600 / Math.max(info.size.width, info.size.height));
      const pixels = await source.createPixelMap({ desiredSize: { width: Math.max(1, Math.round(info.size.width * scale)), height: Math.max(1, Math.round(info.size.height * scale)) } });
      if (!valid()) { await pixels.release(); return; }
      this.ratio = info.size.width / info.size.height;
      this.pixels = pixels;
      const entry = this.sourceEntry();
      this.loaded = { source: entry, target: target, scope: scope, generation: generation, hub: hub,
        data: response.data, mime: response.mime };
    } catch (_) {
      if (valid()) this.failed = true;
    } finally {
      await source?.release();
      if (revision === this.revision && visit === this.activityVisit) this.busy = false;
    }
  }

  aboutToDisappear(): void {
    this.mounted = false;
    this.activityVisit++;
    this.sourceVisit++;
    this.revision++;
    this.busy = false;
    this.pixels?.release();
    this.pixels = undefined;
    this.loaded = undefined;
  }

  private async preview(): Promise<void> {
    const loaded = this.loaded;
    const window = this.galleryWindow();
    if (!loaded || !window || !sameMessageImageSource(window.entries[window.selectedIndex], loaded.source)) return;
    const model = this.model;
    const revision = this.revision;
    const visit = this.activityVisit;
    const sourceVisit = this.sourceVisit;
    const galleryVisit = this.galleryVisit;
    const target = model.target();
    const generation = model.connectionGeneration();
    const scope = model.scope();
    const hub = model.profile.hubId || model.profile.id;
    if (loaded.target !== target || loaded.scope !== scope || loaded.generation !== generation || loaded.hub !== hub) return;
    const validBeforeOpen = (): boolean => this.current(model, revision, visit, generation, scope, target, hub, galleryVisit, window.signature);
    const validWhilePresented = (): boolean => this.currentOwner(model, revision, sourceVisit, generation, scope, target,
      hub, galleryVisit, window.signature);
    if (!validBeforeOpen()) return;
    const host = this.getUIContext().getHostContext();
    if (!host) return;
    try {
      const gallery = await loadMessageImageGallery(model, window, validBeforeOpen, loaded);
      if (!validBeforeOpen() || gallery.target !== target) return;
      await previewMessageImageGallery(host as common.UIAbilityContext, hub, gallery.entries, gallery.selectedIndex,
        validBeforeOpen, validWhilePresented);
    } catch (_) {
      if (validBeforeOpen()) this.failed = true;
    }
  }

  private editLoaded(): void {
    const loaded = this.loaded;
    const window = this.galleryWindow();
    if (!this.active || !this.mounted || !loaded || !window || !sameMessageImageSource(window.entries[window.selectedIndex], loaded.source) ||
      loaded.target !== this.model.target() || loaded.scope !== this.model.scope() ||
      loaded.generation !== this.model.connectionGeneration() || loaded.hub !== (this.model.profile.hubId || this.model.profile.id) ||
      !this.model.owns(loaded.generation, loaded.scope)) return;
    this.onEdit(loaded);
  }

  @Builder imageMenu() {
    Menu() {
      MenuItem({ content: tr('edit') }).enabled(!!this.loaded && !this.busy &&
        ['image/jpeg', 'image/png'].includes(this.loaded.mime)).onClick(() => this.editLoaded())
    }
  }

  build() {
    Column() {
      if (this.pixels) Image(this.pixels).width('100%').aspectRatio(this.ratio).objectFit(ImageFit.Contain)
        .onClick(() => this.preview()).bindContextMenu(this.imageMenu, ResponseType.LongPress)
        .accessibilityText(tr('image'))
      else if (this.busy) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      else Button(tr(this.failed ? 'retry' : 'image')).onClick(() => this.load())
    }.width('100%').onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => { if (visible && ratio > 0 && !this.failed) this.load(); })
  }
}
