import { fullItem, imageURL } from "./api";
import { imagePayload, MAX_IMAGE } from "./attachments";
import { cleanRawUserText, userImageItems, userTextItems, type FailedTurnCandidate, type ResolvePayloadDeps, type ResolvedPayload } from "./compact-resend";
import type { Item } from "./types";

async function defaultFetchImage(mid: string, sid: string, item: Item, signal?: AbortSignal): Promise<{ mime: string; data: string } | null> {
  try {
    const r = await fetch(imageURL(mid, sid, item.id), { signal });
    const blob = r.ok ? await r.blob() : null;
    if (!blob || !blob.type.startsWith("image/") || blob.size > MAX_IMAGE) return null;
    return await imagePayload(blob);
  } catch {
    return null;
  }
}

/**
 * Captures the complete original `OutgoingPayload` (raw text with contexts/files/attachments + full untruncated text +
 * all image bytes) before resending or compacting. A missing attachment or truncated text stops the retry.
 */
export async function resolvePayload(
  candidate: FailedTurnCandidate | null,
  mid: string,
  sid: string,
  deps: ResolvePayloadDeps = {},
  signal?: AbortSignal,
): Promise<ResolvedPayload> {
  if (!candidate) return { ok: false, reason: "empty" };
  if (candidate.captured) {
    const text = candidate.captured.text;
    const images = candidate.captured.images.map((img) => ({ mime: img.mime, data: img.data }));
    if (!text.trim() && !images.length) return { ok: false, reason: "empty" };
    return { ok: true, payload: { text, images } };
  }
  const fetchFull = deps.fetchFullItem ?? fullItem;
  const fetchImg = deps.fetchImage ?? ((m: string, s: string, item: Item) => defaultFetchImage(m, s, item, signal));
  const { user } = candidate;

  const imgItems = userImageItems(user);
  const images: { mime: string; data: string }[] = [];
  for (const img of imgItems) {
    if (img.image?.data) {
      images.push({ mime: img.image.mime || "image/png", data: img.image.data });
      continue;
    }
    if (!img.image?.lazy || !img.id) return { ok: false, reason: "images_unavailable" };
    const loaded = await fetchImg(mid, sid, img);
    if (!loaded?.data) return { ok: false, reason: "images_unavailable" };
    images.push(loaded);
  }

  const textItems = userTextItems(user);
  let text = "";
  if (textItems.length > 0) {
    const parts: string[] = [];
    for (const it of textItems) {
      let raw = it.text ?? "";
      if (it.full_size) {
        try {
          const whole = await fetchFull(mid, sid, it.id);
          if (typeof whole.text !== "string") return { ok: false, reason: "text_unavailable" };
          raw = whole.text;
        } catch {
          return { ok: false, reason: "text_unavailable" };
        }
      }
      const cleaned = cleanRawUserText(raw, images.length > 0);
      if (cleaned) parts.push(cleaned);
    }
    text = parts.join("\n");
  } else {
    text = user.text.trim();
  }

  if (!text.trim() && !images.length) return { ok: false, reason: "empty" };
  return { ok: true, payload: { text, images } };
}
