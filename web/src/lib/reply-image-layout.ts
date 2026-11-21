// The rules for pictures in replies (#9475), the same numbers as iOS's PersonaKit ReplyImageLayout: where they go and
// what size to ask for. Only the runs' own chunk (ui/ReplyImages.tsx) reads them; lib/reply-images.ts has what the
// first screen needs.

/** The sizes the hub draws (`GET /api/v1/remote-image`). */
export const REMOTE_IMAGE_SIZES = [256, 512, 768, 1024, 1536, 2048];
/** A lone picture's height on a wide page, and in a narrow window (the iPhone's). */
export const PICTURE_H = 300;
export const PICTURE_H_NARROW = 240;
/** The widest a run gets: two squares of the phone's height side by side, as on iOS. */
export const PICTURES_W = 480;
/** Between the squares of a run. */
export const PICTURE_GAP = 4;
/** A picture that did not load: a tile this many pixels square. */
export const PICTURE_FAILED = 80;
/** The narrowest a lone picture is drawn, as a share of its height (3:4), and its box before it arrives (4:3). */
export const NARROWEST = 3 / 4;
export const PLACEHOLDER = 4 / 3;
/** At most this many squares are drawn; the last of them says how many more there are. */
export const MOST_SHOWN = 6;

export interface Size {
  w: number;
  h: number;
}

/** The hub's size for a picture drawn `css` pixels on its long side at `ratio` device pixels a CSS pixel. */
export function remoteSize(css: number, ratio = 1): number {
  const px = css * Math.max(1, ratio);
  return REMOTE_IMAGE_SIZES.find((s) => s >= px) ?? REMOTE_IMAGE_SIZES[REMOTE_IMAGE_SIZES.length - 1];
}

/** A lone picture's box in a column `column` wide: `height` tall and its shape's width (3:4 up to the column) once its
 *  pixels are known, the 4:3 placeholder before; a picture smaller than that box keeps its own size. */
export function single(pixels: Size | null, column: number, height: number): Size {
  const col = Math.max(1, column);
  if (!pixels || pixels.w <= 0 || pixels.h <= 0) return { w: Math.min(col, height * PLACEHOLDER), h: height };
  if (pixels.h <= height && pixels.w <= col) return pixels;
  return { w: Math.min(col, Math.max(height * NARROWEST, (height * pixels.w) / pixels.h)), h: height };
}

export interface Grid {
  columns: number;
  tile: number;
  /** How many pictures are drawn (the rest are counted on the last). */
  shown: number;
  /** How many are not drawn (0: all are). */
  more: number;
  rows: number;
}

/** The squares of a run of `count` pictures (2 or more) in a column `column` wide, at most `widest`. */
export function grid(count: number, column: number, widest = PICTURES_W, gap = PICTURE_GAP): Grid {
  const width = Math.max(1, Math.min(column, widest));
  const make = (columns: number, tile: number, shown: number): Grid => ({ columns, tile, shown, more: count - shown, rows: Math.ceil(shown / columns) });
  if (count <= 2) return make(2, Math.max(1, (width - gap) / 2), Math.max(0, count));
  const tile = Math.max(1, (width - 2 * gap) / 3);
  if (count === 4) return make(2, tile, 4);
  return make(3, tile, Math.min(count, MOST_SHOWN));
}

import { isMachineRef } from "./files";
import { isWebImage } from "./reply-images";

export interface ReplyPicture {
  src: string;
  alt: string;
  /** True when `src` points to a file on the session's machine rather than an http(s) URL. */
  local?: boolean;
}

const PICTURE = /!\[([^\]\n]*)\]\(\s*(?:<([^>\n]+)>|([^\s)]+))(?:\s+["'][^"'\n]*["'])?\s*\)/g;

/** Every supported picture the text names outside code, in order (block and inline): what the viewer pages through. */
export function picturesIn(md: string, opts?: { local?: boolean }): ReplyPicture[] {
  if (!md.includes("![")) return [];
  const out: ReplyPicture[] = [];
  let fence: string | null = null;
  for (const line of md.split("\n")) {
    const t = line.trim();
    const open = /^(`{3,}|~{3,})/.exec(t)?.[1];
    if (fence) {
      if (open && open[0] === fence[0] && open.length >= fence.length && t.slice(open.length).trim() === "") fence = null;
      continue;
    }
    if (open && !(open[0] === "`" && t.slice(open.length).includes("`"))) {
      fence = open;
      continue;
    }
    if (/^ {4}|^\t/.test(line) || t.startsWith(">")) continue;
    const noCode = line.replace(/`[^`]*`/g, "");
    for (const m of noCode.matchAll(PICTURE)) {
      const dest = (m[2] ?? m[3] ?? "").trim();
      if (isWebImage(dest)) out.push({ src: dest, alt: m[1].trim() });
      else if (opts?.local && isMachineRef(dest)) out.push({ src: dest, alt: m[1].trim(), local: true });
    }
  }
  return out;
}

/** The site a picture comes from. */
export function hostOf(src: string): string {
  try {
    return new URL(src).host;
  } catch {
    return "";
  }
}
