// Reads one file out of a zip on a machine without fetching the whole zip (#1345: the preview inside a .pages /
// .key / .numbers): the end of the file for the central directory, then just that entry, by HTTP Range. Pure apart
// from the `read` callback; tested in zip-range.test.ts.

export interface ZipEntry {
  name: string;
  method: number; // 0 stored, 8 deflate
  compSize: number;
  size: number;
  offset: number; // of its local header
  encrypted?: boolean;
}

/** Reads bytes [start, end) of the file. */
export type ReadRange = (start: number, end: number) => Promise<Uint8Array>;

const MAX_DIRECTORY = 64 << 20; // central-directory metadata is untrusted too
const TAIL = 66 * 1024; // the end-of-central-directory record is within the last 64 KiB (+22 bytes) of a zip

const u16 = (b: Uint8Array, i: number) => b[i] | (b[i + 1] << 8);
const u32 = (b: Uint8Array, i: number) => (b[i] | (b[i + 1] << 8) | (b[i + 2] << 16) | (b[i + 3] << 24)) >>> 0;

/** Where the central directory is, from the file's last bytes; null when this is no zip (or a zip64 one). */
export function findDirectory(tail: Uint8Array): { offset: number; size: number; count: number } | null {
  for (let i = tail.length - 22; i >= 0; i--) {
    if (u32(tail, i) !== 0x06054b50) continue;
    if (i + 22 + u16(tail, i + 20) !== tail.length) continue;
    const count = u16(tail, i + 10);
    const size = u32(tail, i + 12);
    const offset = u32(tail, i + 16);
    if (count === 0xffff || offset === 0xffffffff || size === 0xffffffff) return null; // zip64: not for previews
    return { offset, size, count };
  }
  return null;
}

/** The entries of a central directory. */
export function readDirectory(dir: Uint8Array, count: number): ZipEntry[] {
  const out: ZipEntry[] = [];
  const names = new TextDecoder();
  let i = 0;
  while (out.length < count && i + 46 <= dir.length && u32(dir, i) === 0x02014b50) {
    const nameLen = u16(dir, i + 28);
    const extraLen = u16(dir, i + 30);
    const commentLen = u16(dir, i + 32);
    const next = i + 46 + nameLen + extraLen + commentLen;
    if (next > dir.length) break;
    out.push({
      method: u16(dir, i + 10),
      compSize: u32(dir, i + 20),
      size: u32(dir, i + 24),
      offset: u32(dir, i + 42),
      name: names.decode(dir.subarray(i + 46, i + 46 + nameLen)),
      ...((u16(dir, i + 8) & 1) !== 0 ? { encrypted: true } : {}),
    });
    i = next;
  }
  return out;
}

async function inflate(data: Uint8Array, maxSize: number): Promise<Uint8Array | null> {
  const stream = new Blob([data as Uint8Array<ArrayBuffer>]).stream().pipeThrough(new DecompressionStream("deflate-raw"));
  const reader = stream.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > maxSize) {
        await reader.cancel();
        return null;
      }
      chunks.push(value);
    }
    const result = new Uint8Array(size);
    let offset = 0;
    for (const chunk of chunks) {
      result.set(chunk, offset);
      offset += chunk.length;
    }
    return result;
  } catch {
    await reader.cancel().catch(() => undefined);
    return null;
  } finally {
    reader.releaseLock();
  }
}

/**
 * The first of `wanted` (names, compared without case) that the zip holds, read with three ranges at most: its tail,
 * its central directory (when not in the tail), and the entry. null when it has none of them or is no zip.
 */
export async function readZipEntry(read: ReadRange, fileSize: number, wanted: string[], maxSize = 64 << 20): Promise<{ name: string; data: Uint8Array } | null> {
  const tailStart = Math.max(0, fileSize - TAIL);
  const tail = await read(tailStart, fileSize);
  const where = findDirectory(tail);
  if (!where || where.size > MAX_DIRECTORY || where.offset + where.size > fileSize) return null;
  const dir = where.offset >= tailStart ? tail.subarray(where.offset - tailStart, where.offset - tailStart + where.size) : await read(where.offset, where.offset + where.size);
  const entries = readDirectory(dir, where.count);
  const lower = entries.map((e) => e.name.toLowerCase());
  for (const name of wanted) {
    const i = lower.indexOf(name.toLowerCase());
    if (i < 0) continue;
    const e = entries[i];
    if (e.encrypted || e.compSize > maxSize || e.size > maxSize || (e.method !== 0 && e.method !== 8)) return null;
    // the local header's name and extra field may differ in length from the directory's: read some room for them
    const chunk = await read(e.offset, Math.min(fileSize, e.offset + 30 + 1024 + e.compSize));
    if (chunk.length < 30 || u32(chunk, 0) !== 0x04034b50 || (u16(chunk, 6) & 1) !== 0) return null;
    const start = 30 + u16(chunk, 26) + u16(chunk, 28);
    const raw = chunk.subarray(start, start + e.compSize);
    if (raw.length !== e.compSize) return null;
    const data = e.method === 8 ? await inflate(raw, maxSize) : raw;
    return data ? { name: e.name, data } : null;
  }
  return null;
}

/** Previews Apple's apps keep inside an iWork document, best first (a PDF of every page, else a picture of the first). */
export const IWORK_PREVIEWS = ["QuickLook/Preview.pdf", "preview.jpg", "preview-web.jpg", "QuickLook/Thumbnail.jpg", "QuickLook/Thumbnail.png", "preview-micro.jpg"];
