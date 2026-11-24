// The pictures a message may carry (#12973, docs/protocol/protocol.md "Pictures in a message"): the hub publishes them in
// hello's image_limits and refuses more than per_message; a hub without them takes the same numbers (its body limit is
// the same and it counts nothing). The scaling itself is lib/image-scale.ts, loaded when a picture is added.

export interface ImageLimits {
  /** The body of a message that carries pictures. */
  request_bytes: number;
  /** One picture's bytes a client aims for (the hub does not refuse a larger one). */
  image_bytes: number;
  /** The long side a picture is scaled down to. */
  max_side: number;
  /** The pictures one message may carry. */
  per_message: number;
}

export const DEFAULT_IMAGE_LIMITS: ImageLimits = { request_bytes: 32 * 1024 * 1024, image_bytes: 3_750_000, max_side: 1600, per_message: 20 };

/** Room a message keeps beside its pictures, for its words, quotes, contexts and other fields. */
const BODY_ROOM = 1024 * 1024;

/** An original bigger than this is not decoded in the page: it goes as a file. */
export const MAX_PICTURE_SOURCE = 64 * 1024 * 1024;

const FIELDS = ["request_bytes", "image_bytes", "max_side", "per_message"] as const;

/** The last hello's image_limits as it came (lib/store.ts sets it): undefined from a hub without them. */
let published: unknown;
export function setPublishedImageLimits(v: unknown) {
  published = v;
}

function limitsOf(v: unknown): ImageLimits | null {
  if (!v || typeof v !== "object") return null;
  const o = v as Record<string, unknown>;
  const out = { ...DEFAULT_IMAGE_LIMITS };
  for (const k of FIELDS) if (typeof o[k] === "number" && (o[k] as number) > 0) out[k] = o[k] as number;
  return out;
}

/** The limits a message to a session of `agent` is held to (an empty agent is Claude Code, as on the hub), from the hub's
 *  hello, else the defaults. */
export function imageLimitsFor(agent?: string): ImageLimits {
  const agents = published && typeof published === "object" ? (published as { agents?: Record<string, unknown> }).agents : undefined;
  return limitsOf(agents?.[agent || "claude"]) ?? limitsOf(published) ?? DEFAULT_IMAGE_LIMITS;
}

/** Whether one more picture of `size` base64 characters fits beside those held (their base64 lengths): `count` past
 *  per_message, `bytes` past what the body leaves for pictures. */
export function pictureRoom(held: readonly number[], size: number, l: ImageLimits): "ok" | "count" | "bytes" {
  if (held.length >= l.per_message) return "count";
  return held.reduce((a, b) => a + b, 0) + size > l.request_bytes - BODY_ROOM ? "bytes" : "ok";
}
