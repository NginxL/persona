// A reply's runs of pictures (#9475), loaded the first time a reply names a web picture (lazy-plugin.ts): one chunk
// with the step that lifts block pictures into runs (reply-images-plugin.ts) and the runs' component (ReplyImages.tsx),
// so the run is drawn as soon as the step has made it. Replies without pictures never carry either.
import { lazy } from "react";
import { lazyPlugin } from "./stream/lazy-plugin";

const chunk = lazyPlugin(() => import("./ReplyImages"));

/** The chunk once loaded, when `wanted` (asking for it then): its `rehypeReplyImages` is the step. */
export const useReplyImages = chunk.use;
/** Loads it (once): tests, and anything that wants it before the page is idle. */
export const loadReplyImages = chunk.load;
/** A run, drawn by the chunk. */
export const ReplyImagesRun = lazy(() => chunk.load().then((m) => ({ default: m.ReplyImages })));
/** A single local image inside a quote, list, or table, drawn by the same chunk. */
export const LocalImageRun = lazy(() => chunk.load().then((m) => ({ default: m.LocalImage })));
