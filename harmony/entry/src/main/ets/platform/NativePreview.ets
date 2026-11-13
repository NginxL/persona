import { tr } from '../design/Strings';
import { BusinessError } from '@kit.BasicServicesKit';
import { common } from '@kit.AbilityKit';
import { util } from '@kit.ArkTS';
import { fileIo, fileUri } from '@kit.CoreFileKit';
import { filePreview } from '@kit.PreviewKit';
import { attachmentExtension, isAttachmentCopyName } from '../core/AttachmentTypes';
import type { LoadedMessageImage } from '../core/MessageImageGallery';
import { MAX_MESSAGE_IMAGE_GALLERY_BYTES, MAX_MESSAGE_IMAGE_GALLERY_COUNT } from '../core/MessageImageGallery';

/** Hand only this app's selected attachment copies to the system preview service. */
export async function previewAttachment(context: common.UIAbilityContext, path: string, name: string, mime: string): Promise<void> {
  const directory = context.filesDir + '/chat-attachments/';
  if (!path.startsWith(directory) || !isAttachmentCopyName(path.substring(directory.length))) throw new Error('preview_unavailable');
  if (!canIUse('SystemCapability.FileManagement.FilePreview.Core')) throw new Error('preview_unavailable');
  const uri = fileUri.getUriFromPath(path);
  if (!await filePreview.canPreview(context, uri)) throw new Error('preview_unavailable');
  const info: filePreview.PreviewInfo = { title: name, uri: uri, mimeType: mime };
  await filePreview.openPreview(context, info);
}

function messagePreviewDirectory(context: common.UIAbilityContext, hub: string): string {
  if (!hub || hub.length > 256) throw new Error('preview_unavailable');
  return context.cacheDir + '/message-images/' + encodeURIComponent(hub);
}
function messageImageExtension(mime: string): string {
  return mime === 'image/png' ? 'png' : mime === 'image/webp' ? 'webp' : mime === 'image/gif' ? 'gif' :
    mime === 'image/heif' || mime === 'image/heic' ? 'heic' : mime === 'image/jpeg' ? 'jpg' : '';
}
export function clearImagePreviews(context: common.UIAbilityContext, hub: string): void {
  const directory = messagePreviewDirectory(context, hub);
  let names: string[];
  try { names = fileIo.listFileSync(directory); } catch (error) {
    if ((error as BusinessError).code === 13900002) return; throw error as Error;
  }
  for (const name of names) if (isAttachmentCopyName(name)) fileIo.unlinkSync(directory + '/' + name);
  fileIo.rmdirSync(directory);
}
/** System preview supplies its own image viewing and zoom gestures. Cache copies are scoped to the paired Hub. */
export async function previewMessageImage(context: common.UIAbilityContext, hub: string, data: ArrayBuffer, mime: string, valid: () => boolean): Promise<void> {
  if (!valid() || !data.byteLength || data.byteLength > 32 * 1024 * 1024 || !canIUse('SystemCapability.FileManagement.FilePreview.Core')) throw new Error('preview_unavailable');
  const extension = mime === 'image/png' ? 'png' : mime === 'image/webp' ? 'webp' : mime === 'image/gif' ? 'gif' : mime === 'image/heif' || mime === 'image/heic' ? 'heic' : mime === 'image/jpeg' ? 'jpg' : '';
  if (!extension) throw new Error('preview_unavailable');
  const directory = messagePreviewDirectory(context, hub); try { fileIo.statSync(directory); } catch (_) { fileIo.mkdirSync(directory, true); }
  const files = fileIo.listFileSync(directory).filter((name: string) => isAttachmentCopyName(name));
  while (files.length >= 16) fileIo.unlinkSync(directory + '/' + files.shift());
  const path = directory + '/' + util.generateRandomUUID() + '.' + extension;
  const file = fileIo.openSync(path, fileIo.OpenMode.CREATE | fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.TRUNC);
  try { if (fileIo.writeSync(file.fd, data) !== data.byteLength) throw new Error('preview_unavailable'); } finally { fileIo.closeSync(file.fd); }
  const uri = fileUri.getUriFromPath(path); let opened = false;
  try {
    if (!await filePreview.canPreview(context, uri) || !valid()) throw new Error('preview_unavailable');
    await filePreview.openPreview(context, { title: tr('image'), uri: uri, mimeType: mime }); opened = true;
    if (!valid()) { await filePreview.closePreview(context); throw new Error('request_updated'); }
  } catch (error) {
    if (!opened || !valid()) { try { fileIo.unlinkSync(path); } catch (_) {} }
    throw error as Error;
  }
}

/** Open a bounded authenticated transcript window; the presented lease tolerates the system preview modal pause. */
export async function previewMessageImageGallery(context: common.UIAbilityContext, hub: string,
  images: LoadedMessageImage[], selectedIndex: number, valid: () => boolean,
  validWhilePresented: () => boolean = valid): Promise<void> {
  if (!valid() || !images.length || images.length > MAX_MESSAGE_IMAGE_GALLERY_COUNT ||
    !Number.isSafeInteger(selectedIndex) || selectedIndex < 0 || selectedIndex >= images.length ||
    !canIUse('SystemCapability.FileManagement.FilePreview.Core')) throw new Error('preview_unavailable');
  let totalBytes = 0;
  const owner = images[0];
  for (const image of images) {
    if (image.target !== owner.target || image.scope !== owner.scope || image.generation !== owner.generation ||
      image.hub !== owner.hub || image.source.target !== image.target || !image.data.byteLength ||
      image.data.byteLength > MAX_MESSAGE_IMAGE_GALLERY_BYTES - totalBytes || !messageImageExtension(image.mime))
      throw new Error('preview_unavailable');
    totalBytes += image.data.byteLength;
  }

  const directory = messagePreviewDirectory(context, hub);
  try { fileIo.statSync(directory); } catch (_) { fileIo.mkdirSync(directory, true); }
  const existing = fileIo.listFileSync(directory).filter((name: string) => isAttachmentCopyName(name));
  while (existing.length + images.length > MAX_MESSAGE_IMAGE_GALLERY_COUNT) {
    const oldest = existing.shift();
    if (!oldest) break;
    fileIo.unlinkSync(directory + '/' + oldest);
  }

  const paths: string[] = [];
  const previews: filePreview.PreviewInfo[] = [];
  let opened = false;
  try {
    for (const image of images) {
      if (!valid()) throw new Error('request_updated');
      const path = directory + '/' + util.generateRandomUUID() + '.' + messageImageExtension(image.mime);
      paths.push(path);
      const file = fileIo.openSync(path, fileIo.OpenMode.CREATE | fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.TRUNC);
      try { if (fileIo.writeSync(file.fd, image.data) !== image.data.byteLength) throw new Error('preview_unavailable'); }
      finally { fileIo.closeSync(file.fd); }
      const uri = fileUri.getUriFromPath(path);
      if (!await filePreview.canPreview(context, uri) || !valid()) throw new Error('preview_unavailable');
      previews.push({ title: tr('image'), uri: uri, mimeType: image.mime });
    }
    if (!valid()) throw new Error('request_updated');
    await filePreview.openPreview(context, previews, selectedIndex);
    opened = true;
    if (!validWhilePresented()) {
      try { await filePreview.closePreview(context); } catch (_) {}
      opened = false;
      throw new Error('request_updated');
    }
  } catch (error) {
    if (opened && !validWhilePresented()) { try { await filePreview.closePreview(context); } catch (_) {} }
    for (const path of paths) { try { fileIo.unlinkSync(path); } catch (_) {} }
    throw error as Error;
  }
}


/** Supported Office, PDF, media and HTML files use the system document preview extension. */
export async function previewDocument(context: common.UIAbilityContext, hub: string, data: ArrayBuffer, name: string, mime: string, valid: () => boolean): Promise<void> {
  if (!valid() || !data.byteLength || data.byteLength > 32 * 1024 * 1024 || !canIUse('SystemCapability.FileManagement.FilePreview.Core')) throw new Error('preview_unavailable');
  const directory = messagePreviewDirectory(context, hub); try { fileIo.statSync(directory); } catch (_) { fileIo.mkdirSync(directory, true); }
  const files = fileIo.listFileSync(directory).filter((value: string) => isAttachmentCopyName(value));
  while (files.length >= 16) fileIo.unlinkSync(directory + '/' + files.shift());
  const path = directory + '/' + util.generateRandomUUID() + '.' + attachmentExtension(name);
  const file = fileIo.openSync(path, fileIo.OpenMode.CREATE | fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.TRUNC);
  try { if (fileIo.writeSync(file.fd, data) !== data.byteLength) throw new Error('preview_unavailable'); } finally { fileIo.closeSync(file.fd); }
  const uri = fileUri.getUriFromPath(path);
  try {
    if (!await filePreview.canPreview(context, uri) || !valid()) throw new Error('preview_unavailable');
    await filePreview.openPreview(context, { title: name, uri: uri, mimeType: mime });
    if (!valid()) { await filePreview.closePreview(context); throw new Error('request_updated'); }
  } catch (error) { try { fileIo.unlinkSync(path); } catch (_) {} throw error as Error; }
}
