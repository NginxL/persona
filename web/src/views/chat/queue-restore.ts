// Restoring a queued message's draft and photos is needed only by the intent-loaded QueuePanel and ForwardDialog.
// Keep it out of the eager QueueStrip wrapper so this code and its chat-queuestrip words stay off the first screen.
import { fileDownloadURL } from "../../lib/api";
import { imagePayload, MAX_IMAGE, quotePath } from "../../lib/attachments";
import { restoreFiles } from "../../lib/composer-files";
import { sendDraft, type DraftImage } from "../../lib/drafts";
import { addContext, newContext, type ContextRef } from "../../lib/links";
import { queuedDraft, type QueuedParts } from "../../lib/queue";
import type { Session } from "../../lib/types";
import { toast } from "../../ui/atoms";
import { t } from "../../i18n";

/** A photo of a queued message, read back from the session's machine for the composer's tray; null when it can't be. */
export async function readPhoto(mid: string, path: string, cwd?: string): Promise<DraftImage | null> {
  try {
    const r = await fetch(fileDownloadURL(mid, path, cwd, { inline: true }));
    const blob = r.ok ? await r.blob() : null;
    if (!blob || !blob.type.startsWith("image/") || blob.size > MAX_IMAGE) return null;
    const name = path.split("/").pop() || "image.png";
    return { name, ...(await imagePayload(new File([blob], name, { type: blob.type }))) };
  } catch {
    return null;
  }
}

/** A photo a Persona chat's queue gave back with its message (#4342), as the composer's tray holds it. */
export interface QueuedImage {
  mime: string;
  data: string; // base64
}

/** Puts a pulled-back message in the composer of `draftKey`, read apart. Photos the machine no longer gives back go in
 *  as their paths, so nothing that was sent is lost; `images` are photos that came back with it (a Persona chat's). */
export async function restoreQueued(draftKey: string, session: Session, p: QueuedParts, images: QueuedImage[] = []) {
  // its files back to their tiles over the box (#8361); only ones past the limit go in as paths
  const text = queuedDraft({ text: p.text, files: restoreFiles(draftKey, p.files) });
  if (text) sendDraft(draftKey, { type: "append", text });
  // a kind this composer has no chip of its own for is shown as a quote and sent back as it came
  for (const c of p.contexts) addContext(draftKey, { ...newContext(c.kind as ContextRef["kind"], c.title, c.url, c.text), ...(c.attrs ? { attrs: c.attrs } : {}) });
  images.forEach((img, i) => sendDraft(draftKey, { type: "image", image: { name: `photo-${i + 1}.${img.mime.split("/")[1] || "png"}`, ...img } }));
  if (!p.photos.length) return;
  const read = await Promise.all(p.photos.map((path) => readPhoto(session.machine_id, path, session.cwd)));
  const lost: string[] = [];
  read.forEach((img, i) => {
    if (!img || !sendDraft(draftKey, { type: "image", image: img })) lost.push(p.photos[i]);
  });
  if (lost.length) {
    sendDraft(draftKey, { type: "append", text: lost.map(quotePath).join(" ") });
    toast(t("chat-queuestrip.photos_as_paths", { count: lost.length }), "error");
  }
}
