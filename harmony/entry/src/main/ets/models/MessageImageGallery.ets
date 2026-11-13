import type { ClientModel } from './ClientModel';
import { loadMessageImage } from './MessageImage';
import type { LoadedMessageImage, MessageImageGalleryWindow } from '../core/MessageImageGallery';
import { MAX_MESSAGE_IMAGE_GALLERY_BYTES, MAX_MESSAGE_IMAGE_GALLERY_COUNT } from '../core/MessageImageGallery';

export interface LoadedMessageImageGallery {
  entries: LoadedMessageImage[];
  selectedIndex: number;
  target: string;
}

/** Load only a bounded current-conversation window through its captured authenticated owner. */
export async function loadMessageImageGallery(model: ClientModel, window: MessageImageGalleryWindow,
  valid: () => boolean, selectedImage?: LoadedMessageImage): Promise<LoadedMessageImageGallery> {
  if (!valid() || !model.connected || !model.isChat()) throw new Error('request_updated');
  const repository = model.repo();
  const generation = model.connectionGeneration();
  const scope = model.scope();
  const target = model.target();
  const hub = model.profile.hubId || model.profile.id;
  const current = (): boolean => valid() && model.connected && model.isChat() && model.owns(generation, scope) &&
    model.scope() === scope && model.target() === target && (model.profile.hubId || model.profile.id) === hub &&
    model.repo() === repository;
  if (!window.entries.length || window.entries.length > MAX_MESSAGE_IMAGE_GALLERY_COUNT || window.selectedIndex < 0 ||
    window.selectedIndex >= window.entries.length || window.entries.some((entry) => entry.target !== target) || !current())
    throw new Error('request_updated');

  const images: LoadedMessageImage[] = [];
  let totalBytes = 0;
  for (let index = 0; index < window.entries.length; index++) {
    if (!current()) throw new Error('request_updated');
    const source = window.entries[index];
    let data: ArrayBuffer;
    let mime: string;
    if (index === window.selectedIndex && selectedImage && selectedImage.target === target && selectedImage.scope === scope &&
      selectedImage.generation === generation && selectedImage.hub === hub &&
      selectedImage.source.key === source.key && selectedImage.source.itemId === source.itemId &&
      selectedImage.source.remotePath === source.remotePath) {
      data = selectedImage.data;
      mime = selectedImage.mime;
    } else {
      const loaded = await loadMessageImage(model, source.itemId, source.remotePath);
      if (!current()) throw new Error('request_updated');
      data = loaded.data;
      mime = loaded.mime;
    }
    if (!data.byteLength || data.byteLength > MAX_MESSAGE_IMAGE_GALLERY_BYTES - totalBytes)
      throw new Error('preview_unavailable');
    totalBytes += data.byteLength;
    images.push({ source: source, target: target, scope: scope, generation: generation, hub: hub, data: data, mime: mime });
  }
  if (!current()) throw new Error('request_updated');
  return { entries: images, selectedIndex: window.selectedIndex, target: target };
}
