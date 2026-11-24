// A picture for a new to-do (#13797). The manager keeps a to-do's PNG or JPEG at most 1 MiB and 4096 pixels on its long
// side (server/internal/assistant/todo_images.go, which still checks them): one within them goes as it is, byte for
// byte and without being decoded; a larger one, most often a screenshot, is scaled down and encoded again by fitPicture
// (lib/image-scale.ts) instead of being refused. Its header is read first, so a file too large to decode in the page,
// one with too many pixels (a decompression bomb among them) or one that is not a PNG or JPEG fails with a message
// before anything is decoded. Loaded when a picture is picked (views/Todos.tsx), not on the first screen.
// docs/assistant/todos.md "Web".
import { t } from "../i18n";
import { base64File } from "./attachments";
import { fitPicture, type FitStep } from "./image-scale";
import { MAX_PICTURE_SOURCE } from "./image-limits";

/** The manager's limits for one to-do image (maxPhotoBytes and maxPhotoSide, server/internal/assistant/todo_images.go). */
export const TODO_IMAGE_BYTES = 1024 * 1024;
export const TODO_IMAGE_SIDE = 4096;
/** More pixels than this (8K by 6K: decoding holds four bytes a pixel) are not decoded; nor are more than
 *  MAX_PICTURE_SOURCE bytes, as in a chat. */
export const TODO_IMAGE_SOURCE_PIXELS = 50_000_000;
/** The long sides a larger picture is tried at, no smaller than a chat's pictures go at… */
const SIDES = [TODO_IMAGE_SIDE, 2560, 1600];
/** …and no smaller than a third of its own, so a screenshot's words stay legible: one that would need more fails. */
const READABLE = 1 / 3;

type Head = { type: "image/png" | "image/jpeg"; width: number; height: number };

/** `file`, a PNG or JPEG picked for a new to-do, as it goes with it, or why it cannot. */
export async function prepareTodoImage(file: File): Promise<{ file: File } | { error: string }> {
  const name = file.name || "image";
  const tooLarge = { error: t("todos.image_source_too_big", { name, bytes: MAX_PICTURE_SOURCE / 1024 / 1024, pixels: TODO_IMAGE_SOURCE_PIXELS / 1e6 }) };
  if (file.size > MAX_PICTURE_SOURCE) return tooLarge;
  const head = await imageHead(file);
  if (!head) return { error: t("todos.image_unreadable", { name }) };
  if (head.width * head.height > TODO_IMAGE_SOURCE_PIXELS) return tooLarge;
  const long = Math.max(head.width, head.height);
  if (file.size <= TODO_IMAGE_BYTES && long <= TODO_IMAGE_SIDE) return { file: head.type === file.type ? file : new File([file], file.name, { type: head.type }) };
  const sides = [...new Set(SIDES.map((side) => Math.min(side, long)))].filter((side) => side >= long * READABLE);
  if (!sides.length) return { error: t("todos.image_too_long", { name, max: TODO_IMAGE_SIDE }) };
  // a PNG that only had to be scaled down stays one, transparency and all, when it then fits; anything else becomes a
  // JPEG on white at the largest side that fits
  const steps: FitStep[] = [
    ...(head.type === "image/png" && long > TODO_IMAGE_SIDE ? [{ side: sides[0], png: true as const }] : []),
    { side: sides[0], quality: 0.8 },
    ...sides.map((side) => ({ side, quality: 0.6 })),
  ];
  const fit = await fitPicture(file, { image_bytes: TODO_IMAGE_BYTES, max_side: TODO_IMAGE_SIDE }, steps);
  if (!fit || (fit.mime !== "image/png" && fit.mime !== "image/jpeg")) return { error: t("todos.image_not_prepared", { name }) };
  return { file: base64File(renamed(file.name, fit.mime), fit.mime, fit.data) };
}

/** `name` ending as `mime` does (a PNG screenshot made a JPEG is shot.jpg). */
function renamed(name: string, mime: string): string {
  if ((mime === "image/png" ? /\.png$/i : /\.jpe?g$/i).test(name)) return name;
  return (name.replace(/\.(png|jpe?g)$/i, "") || "image") + (mime === "image/png" ? ".png" : ".jpg");
}

const PNG = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];

/** The type and pixel size a PNG's or JPEG's header gives, read without decoding it; null when it is neither. */
export async function imageHead(file: Blob): Promise<Head | null> {
  const read = async (at: number, n: number) => new Uint8Array(await file.slice(at, at + n).arrayBuffer());
  const u16 = (b: Uint8Array, i: number) => (b[i] << 8) | b[i + 1];
  const sized = (type: Head["type"], width: number, height: number) => (width > 0 && height > 0 ? { type, width, height } : null);
  const b = await read(0, 24);
  // a PNG: the signature, then IHDR with the width and height
  if (b.length === 24 && PNG.every((v, i) => b[i] === v) && String.fromCharCode(...b.subarray(12, 16)) === "IHDR") {
    const view = new DataView(b.buffer);
    return sized("image/png", view.getUint32(16), view.getUint32(20));
  }
  if (b[0] !== 0xff || b[1] !== 0xd8) return null;
  // a JPEG: its segments up to the frame header (SOF0–SOF15 but DHT, JPG and DAC), a bounded number of them
  for (let at = 2, n = 0; n < 256; n++) {
    const s = await read(at, 9);
    if (s.length < 2 || s[0] !== 0xff) return null;
    const m = s[1];
    if (m === 0xff) at++;
    else if (m >= 0xc0 && m <= 0xcf && m !== 0xc4 && m !== 0xc8 && m !== 0xcc) return s.length === 9 ? sized("image/jpeg", u16(s, 7), u16(s, 5)) : null;
    else if (m === 0xd8 || m === 0xd9 || m === 0xda || s.length < 4 || u16(s, 2) < 2) return null;
    else at += 2 + u16(s, 2);
  }
  return null;
}
