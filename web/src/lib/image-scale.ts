// A picture made ready for a message (#12973), as the iPhone makes it (MessagePhotos.payload): its long side scaled down
// to max_side, JPEG at 0.75; again at 0.6 and then at 1280 px when it is still over image_bytes. One already within
// max_side and image_bytes goes as it is, so a PNG screenshot stays sharp and a GIF keeps moving. Loaded only when a
// picture is added (it is not on the first screen). A new to-do's pictures (lib/todo-image.ts, #13797) go through the
// same steps of their own.
import { imagePayload } from "./attachments";
import type { ImageLimits } from "./image-limits";

/** Types a message carries as they are. */
const AS_IS = new Set(["image/jpeg", "image/png", "image/webp", "image/gif"]);

/** One try at bringing a picture under image_bytes: its long side at most `side`, as a JPEG at `quality` on white, or
 *  as a PNG (`png`), its transparency kept. */
export type FitStep = { side: number; quality: number; png?: never } | { side: number; png: true; quality?: never };

/** A message's steps: max_side at 0.75, then at 0.6, then 1280 px at 0.6. */
const messageSteps = (l: Pick<ImageLimits, "max_side">): FitStep[] => [
  { side: l.max_side, quality: 0.75 },
  { side: l.max_side, quality: 0.6 },
  { side: Math.min(l.max_side, 1280), quality: 0.6 },
];

/** `file` as a message's picture, or null when the page cannot read it or bring it under image_bytes (it goes as a file).
 *  `steps` are the tries after the as-is one, a message's unless given. */
export async function fitPicture(file: Blob, l: Pick<ImageLimits, "image_bytes" | "max_side">, steps: readonly FitStep[] = messageSteps(l)): Promise<{ mime: string; data: string } | null> {
  let bitmap: ImageBitmap;
  try {
    bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
  } catch {
    return null;
  }
  try {
    if (Math.max(bitmap.width, bitmap.height) <= l.max_side && file.size <= l.image_bytes && AS_IS.has(file.type)) return await imagePayload(file);
    for (const step of steps) {
      const blob = await draw(bitmap, step);
      if (blob && blob.size <= l.image_bytes) return await imagePayload(blob);
    }
    return null;
  } finally {
    bitmap.close();
  }
}

/** `bitmap` with its long side at most `side`: a JPEG on white (a transparent PNG is not black in a JPEG), or a PNG. */
async function draw(bitmap: ImageBitmap, step: FitStep): Promise<Blob | null> {
  const scale = Math.min(1, step.side / Math.max(bitmap.width, bitmap.height));
  const canvas = document.createElement("canvas");
  canvas.width = Math.max(1, Math.round(bitmap.width * scale));
  canvas.height = Math.max(1, Math.round(bitmap.height * scale));
  const g = canvas.getContext("2d");
  if (!g) return null;
  if (!step.png) {
    g.fillStyle = "white";
    g.fillRect(0, 0, canvas.width, canvas.height);
  }
  g.imageSmoothingQuality = "high";
  g.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
  return new Promise((ok) => (step.png ? canvas.toBlob(ok, "image/png") : canvas.toBlob(ok, "image/jpeg", step.quality)));
}
