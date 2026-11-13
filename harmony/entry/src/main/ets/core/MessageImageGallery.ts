export const MAX_MESSAGE_IMAGE_GALLERY_COUNT: number = 16;
export const MAX_MESSAGE_IMAGE_GALLERY_BYTES: number = 64 * 1024 * 1024;

/** An authenticated image source in the current conversation transcript. */
export interface MessageImageGalleryEntry {
  key: string;
  itemId: string;
  remotePath: string;
  target: string;
}

/** A loaded image keeps its exact transcript source and authenticated chat target. */
export interface LoadedMessageImage {
  source: MessageImageGalleryEntry;
  target: string;
  scope: string;
  generation: number;
  hub: string;
  data: ArrayBuffer;
  mime: string;
}

export interface MessageImageGalleryWindow {
  entries: MessageImageGalleryEntry[];
  selectedIndex: number;
  signature: string;
}

/** Keep the selected source in a bounded native gallery, centered where possible. */
export function messageImageGalleryWindow(entries: MessageImageGalleryEntry[], selectedIndex: number): MessageImageGalleryWindow | undefined {
  if (!Number.isSafeInteger(selectedIndex) || selectedIndex < 0 || selectedIndex >= entries.length) return undefined;
  const count = Math.min(entries.length, MAX_MESSAGE_IMAGE_GALLERY_COUNT);
  const start = Math.max(0, Math.min(selectedIndex - Math.floor(count / 2), entries.length - count));
  const selected: MessageImageGalleryEntry[] = [];
  const keys = new Set<string>();
  for (let index = start; index < start + count; index++) {
    const entry = entries[index];
    if (!entry || !entry.key || (!entry.itemId && !entry.remotePath) || !entry.target || keys.has(entry.key)) return undefined;
    keys.add(entry.key);
    selected.push({ key: entry.key, itemId: entry.itemId, remotePath: entry.remotePath, target: entry.target });
  }
  const localIndex = selectedIndex - start;
  const signature = JSON.stringify([selected.map((entry: MessageImageGalleryEntry): Object =>
    [entry.key, entry.itemId, entry.remotePath, entry.target]), localIndex]);
  return { entries: selected, selectedIndex: localIndex, signature: signature };
}

export function sameMessageImageSource(left: MessageImageGalleryEntry, right: MessageImageGalleryEntry): boolean {
  return left.key === right.key && left.itemId === right.itemId && left.remotePath === right.remotePath && left.target === right.target;
}

export interface ConversationImageMessage {
  id: string;
  ownerTarget: string;
  imageId: string;
  imagePaths: string[];
}
export function conversationImageKey(target: string, messageId: string, kind: string, source: string, index: number = 0): string {
  return JSON.stringify([target, messageId, kind, source, index]);
}
/** Preserve transcript order while excluding messages belonging to a retired chat owner. */
export function conversationImageEntries(messages: ConversationImageMessage[], target: string): MessageImageGalleryEntry[] {
  const entries: MessageImageGalleryEntry[] = [];
  for (const message of messages) {
    if (!target || message.ownerTarget !== target) continue;
    for (let index = 0; index < message.imagePaths.length; index++) {
      const path = message.imagePaths[index];
      if (path) entries.push({key: conversationImageKey(target, message.id, 'path', path, index), itemId: '', remotePath: path, target: target});
    }
    if (message.imageId) entries.push({key: conversationImageKey(target, message.id, 'item', message.imageId), itemId: message.imageId, remotePath: '', target: target});
  }
  return entries;
}
