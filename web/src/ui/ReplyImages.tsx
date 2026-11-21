// A run of a reply's pictures (#9475): reply-images-plugin.ts lifts it out of its paragraph, lib/reply-images.ts has
// the rules (iOS's ReplyPictureRun draws the same). One picture is a fixed height and its shape's width, never larger
// than itself; two or more are squares, the sixth counting those not drawn. The box is its final size before the
// picture arrives (only a lone one's width changes), a picture that did not load is a small tile that retries, and a
// click opens the image viewer on every picture of the reply. Pictures load through the hub's remote image route at
// the size drawn (`remote_images`); what other people wrote waits for a click, and so does every picture of a hub
// without the route (as #8162 had them, loaded straight from their host with no Referer once asked).
// Its own chunk: loaded with a reply's first picture. Sizes are inline styles, not new utility classes (those would
// land in every page's stylesheet).
import { createContext, useContext, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import type { Element } from "hast";
import { t } from "../i18n";
import { fileDownloadURL, getToken } from "../lib/api";
import { fileName, isMachineRef, resolveRef } from "../lib/files";
import {
  grid,
  hostOf,
  MOST_SHOWN,
  PICTURE_FAILED,
  PICTURE_GAP,
  PICTURE_H,
  PICTURE_H_NARROW,
  PICTURES_W,
  picturesIn,
  PLACEHOLDER,
  remoteSize,
  single,
  type ReplyPicture,
  type Size,
} from "../lib/reply-image-layout";
import { isWebImage, remoteImageURL, REMOTE_IMAGES_FEATURE, VIEWER_SIZE } from "../lib/reply-images";
import { hubHas, useStore } from "../lib/store";
import type { ViewerImage } from "../views/chat/ImageViewer";
import { ImageViewer } from "../views/chat/LazyImageViewer";
import { Icon } from "./icons";
import { NARROW, useMedia } from "./media-query";
import { ReplyPicturesContext } from "./reply-pictures-context";

export { rehypeReplyImages } from "./reply-images-plugin";

/** Set by Export image (#9469, views/chat/ExportImage.tsx): a run draws all its pictures, through the hub only, for a
 *  picture that cannot be tapped (no "+N", no viewer, no waiting for a tap). Here, not with the first screen's context. */
export const ReplyPicturesExport = createContext(false);

const RADIUS = 10; // the turn's viewed images' tiles (TurnImages.tsx)
const tileClass = "group relative shrink-0 overflow-hidden bg-field shadow-hairline outline-none focus-visible:ring-2 focus-visible:ring-accent";

/** Remembered local-image failures so remounting/scrolling never auto-retries until the user clicks Retry. */
const localFailureCache = new Map<string, { attempt: number; reason: string }>();
const MAX_FAILURE_CACHE = 128;

export function clearLocalImageFailures(): void {
  localFailureCache.clear();
}

function recordLocalFailure(key: string, attempt: number, reason: string): void {
  localFailureCache.delete(key);
  localFailureCache.set(key, { attempt, reason });
  if (localFailureCache.size > MAX_FAILURE_CACHE) {
    const oldest = localFailureCache.keys().next().value;
    if (oldest !== undefined) localFailureCache.delete(oldest);
  }
}

/** One-shot diagnostic fetch after an <img> error to surface a clear reason (e.g. 404/403/JSON error). */
export async function probeLocalImageError(url: string, signal?: AbortSignal): Promise<string> {
  const fallback = t("ui-replyimages.local_load_failed");
  if (typeof fetch !== "function") return fallback;
  try {
    const res = await fetch(url, { signal });
    if (res.ok) return fallback;
    const ct = res.headers.get("content-type") ?? "";
    if (ct.includes("application/json")) {
      const body = (await res.json().catch(() => null)) as { error?: unknown; message?: unknown } | null;
      const msg = typeof body?.error === "string" ? body.error.trim() : typeof body?.message === "string" ? body.message.trim() : "";
      if (msg) return msg;
    } else {
      const text = (await res.text().catch(() => "")).trim();
      if (text && text.length <= 160 && !text.startsWith("<")) return text;
    }
    return res.status ? `${fallback} (HTTP ${res.status})` : fallback;
  } catch {
    return fallback;
  }
}

/** Build the authenticated download URL for a local image, with a retry query on manual retry. */
function localImageSrc(mid: string, resolved: string, cwd: string | undefined, attempt: number): string {
  const base = fileDownloadURL(mid, resolved, cwd, { inline: true });
  return attempt > 0 ? `${base}${base.includes("?") ? "&" : "?"}retry=${attempt}` : base;
}

/** The pictures a run's element holds. */
function picturesOf(node: Element | undefined, allowLocal: boolean): ReplyPicture[] {
  return (node?.children ?? []).flatMap((c) => {
    if (c.type !== "element" || c.tagName !== "img") return [];
    const src = typeof c.properties.src === "string" ? c.properties.src : "";
    const alt = typeof c.properties.alt === "string" ? c.properties.alt : "";
    if (isWebImage(src)) return [{ src, alt }];
    if (allowLocal && isMachineRef(src)) return [{ src, alt, local: true }];
    return [];
  });
}

export function ReplyImages({ node }: { node?: Element }) {
  const { text, ask, mid, cwd } = useContext(ReplyPicturesContext);
  const allowLocal = Boolean(mid);
  const pictures = picturesOf(node, allowLocal);
  const all = useMemo(() => picturesIn(text, { local: allowLocal }), [text, allowLocal]);
  const through = useStore((s) => hubHas(s.features, REMOTE_IMAGES_FEATURE));
  const narrow = useMedia(NARROW);
  const [asked, setAsked] = useState(false);
  const [open, setOpen] = useState<number | null>(null);
  const exporting = useContext(ReplyPicturesExport);
  if (!pictures.length) return null;
  const allLocal = pictures.every((p) => p.local);
  const resolveLocal = (p: ReplyPicture) => resolveRef(cwd ?? "", p.src) || p.src;
  const localKey = (p: ReplyPicture) => `${mid ?? ""}\0${cwd ?? ""}\0${resolveLocal(p)}`;
  if (exporting) return <ExportPictures pictures={pictures} through={through} mid={mid} cwd={cwd} />;
  const height = narrow ? PICTURE_H_NARROW : PICTURE_H;
  const ratio = typeof devicePixelRatio === "number" ? devicePixelRatio : 1;
  const srcFor = (p: ReplyPicture, size: number, attempt = 0) =>
    p.local && mid ? localImageSrc(mid, resolveLocal(p), cwd, attempt) : through ? remoteImageURL(p.src, size, getToken()) : p.src;
  // the viewer pages through the whole reply's pictures, from the one clicked
  const gallery = pictures.every((p) => all.some((a) => a.src === p.src)) ? all : pictures;
  const at = (p: ReplyPicture) => Math.max(0, gallery.findIndex((g) => g.src === p.src));
  const viewer: ViewerImage[] = gallery.map((g, i) => {
    if (g.local && mid) {
      const resolved = resolveLocal(g);
      const name = fileName(resolved);
      return { id: `${i}:${g.src}`, src: srcFor(g, VIEWER_SIZE), label: g.alt || name, name };
    }
    return { id: `${i}:${g.src}`, src: srcFor(g, VIEWER_SIZE), label: [g.alt, hostOf(g.src)].filter(Boolean).join(" · ") };
  });
  const waiting = !allLocal && (ask || !through) && !asked;
  return (
    <div data-reply-images="" data-reply-images-kind={allLocal ? "local" : "web"} style={{ margin: "8px 0" }}>
      {waiting ? (
        <Asked pictures={pictures} height={height} onAsk={() => setAsked(true)} />
      ) : pictures.length === 1 ? (
        <Single
          picture={pictures[0]}
          height={height}
          src={(attempt) => srcFor(pictures[0], remoteSize(height * PLACEHOLDER, ratio), attempt)}
          failureKey={pictures[0].local ? localKey(pictures[0]) : undefined}
          onOpen={() => setOpen(at(pictures[0]))}
        />
      ) : (
        <Squares
          pictures={pictures}
          src={(p, attempt) => srcFor(p, remoteSize(gridFor(pictures.length).tile * PLACEHOLDER, ratio), attempt)}
          failureKey={(p) => (p.local ? localKey(p) : undefined)}
          onOpen={(p) => setOpen(at(p))}
        />
      )}
      {open !== null && <ImageViewer images={viewer} start={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

const gridFor = (count: number) => grid(count, PICTURES_W);

/** The squares' container: `columns` to a row, as wide as the column allows up to PICTURES_W (four: two of the
 *  three-to-a-row squares). */
function squaresStyle(count: number): CSSProperties {
  const g = gridFor(count);
  const width = count === 4 ? `calc((min(100%, ${PICTURES_W}px) - ${2 * PICTURE_GAP}px) * 2 / 3 + ${PICTURE_GAP}px)` : `min(100%, ${PICTURES_W}px)`;
  return { display: "grid", gridTemplateColumns: `repeat(${g.columns}, minmax(0, 1fr))`, gap: PICTURE_GAP, width };
}

function Single({
  picture,
  height,
  src,
  failureKey,
  onOpen,
}: {
  picture: ReplyPicture;
  height: number;
  src: (attempt: number) => string;
  failureKey?: string;
  onOpen: () => void;
}) {
  const cached = failureKey ? localFailureCache.get(failureKey) : undefined;
  const [pixels, setPixels] = useState<Size | null>(null);
  const [failed, setFailed] = useState(Boolean(cached));
  // the column is CSS's (min(100%, …)): wider than it, a picture is cut around its centre at the same height
  const box = failed ? { w: PICTURE_FAILED, h: PICTURE_FAILED } : single(pixels, Number.POSITIVE_INFINITY, height);
  return (
    <Tile
      picture={picture}
      src={src}
      failureKey={failureKey}
      onOpen={onOpen}
      style={{ width: `min(100%, ${box.w}px)`, height: box.h, transition: "width 200ms ease-out" }}
      onPixels={setPixels}
      onFailed={setFailed}
    />
  );
}

function Squares({
  pictures,
  src,
  failureKey,
  onOpen,
}: {
  pictures: ReplyPicture[];
  src: (p: ReplyPicture, attempt: number) => string;
  failureKey: (p: ReplyPicture) => string | undefined;
  onOpen: (p: ReplyPicture) => void;
}) {
  const g = gridFor(pictures.length);
  const shown = pictures.slice(0, Math.min(g.shown, MOST_SHOWN));
  return (
    <div style={squaresStyle(pictures.length)}>
      {shown.map((p, i) => (
        <Tile
          key={`${i}:${p.src}`}
          picture={p}
          src={(attempt) => src(p, attempt)}
          failureKey={failureKey(p)}
          onOpen={() => onOpen(p)}
          style={{ width: "100%", aspectRatio: "1 / 1" }}
          more={i === shown.length - 1 ? g.more : 0}
        />
      ))}
    </div>
  );
}

/** One picture filling its box: the field's color while it comes, then the picture (cut to the box); one that did not
 *  load shows the picture mark and retries on a click. Local pictures also display a clear failure reason + Retry button. */
function Tile({
  picture,
  src,
  failureKey,
  onOpen,
  style,
  more = 0,
  onPixels,
  onFailed,
}: {
  picture: ReplyPicture;
  src: (attempt: number) => string;
  failureKey?: string;
  onOpen: () => void;
  style: CSSProperties;
  more?: number;
  onPixels?: (s: Size) => void;
  onFailed?: (failed: boolean) => void;
}) {
  const cached = failureKey ? localFailureCache.get(failureKey) : undefined;
  const [state, setState] = useState<"loading" | "loaded" | "failed">(cached ? "failed" : "loading");
  const [attempt, setAttempt] = useState(cached?.attempt ?? 0);
  const [reason, setReason] = useState(cached?.reason ?? "");
  const probeRef = useRef<AbortController | null>(null);
  useEffect(() => () => probeRef.current?.abort(), []);
  const url = src(attempt);
  const host = picture.local ? fileName(picture.src) : hostOf(picture.src);
  const failed = state === "failed";
  const retry = () => {
    probeRef.current?.abort();
    const next = attempt + 1;
    if (failureKey) localFailureCache.delete(failureKey);
    setReason("");
    setState("loading");
    onFailed?.(false);
    setAttempt(next);
  };
  return (
    <div className="flex flex-col items-start gap-1.5">
      <button
        type="button"
        onClick={() => {
          if (!failed) return onOpen();
          retry();
        }}
        title={failed ? `${reason || t("ui-replyimages.retry")} · ${host}` : [picture.alt, host].filter(Boolean).join(" · ")}
        aria-label={failed ? t("ui-replyimages.retry") : picture.alt || t("ui-replyimages.picture")}
        data-reply-picture={failed ? "failed" : state}
        data-local-image={picture.local ? "" : undefined}
        className={tileClass}
        style={{ borderRadius: RADIUS, padding: 0, ...style }}
      >
        {failed ? (
          <span className="flex size-full items-center justify-center text-ink-3">
            <Icon name="image" size={18} />
          </span>
        ) : (
          <img
            key={attempt}
            src={url}
            alt=""
            decoding="async"
            loading="lazy"
            referrerPolicy={picture.local ? undefined : "no-referrer"}
            onLoad={(e) => {
              if (failureKey) localFailureCache.delete(failureKey);
              setState("loaded");
              onPixels?.({ w: e.currentTarget.naturalWidth, h: e.currentTarget.naturalHeight });
            }}
            onError={() => {
              setState("failed");
              onFailed?.(true);
              if (picture.local) {
                const fallback = t("ui-replyimages.local_load_failed");
                setReason(fallback);
                if (failureKey) recordLocalFailure(failureKey, attempt, fallback);
                probeRef.current?.abort();
                const ac = new AbortController();
                probeRef.current = ac;
                void probeLocalImageError(url, ac.signal).then((msg) => {
                  if (ac.signal.aborted) return;
                  setReason(msg);
                  if (failureKey) recordLocalFailure(failureKey, attempt, msg);
                });
              }
            }}
            style={{ display: "block", width: "100%", height: "100%", maxWidth: "none", objectFit: "cover", borderRadius: 0, opacity: state === "loaded" ? 1 : 0, transition: "opacity 200ms ease-out" }}
          />
        )}
        {more > 0 && !failed && (
          <span className="absolute inset-0 flex items-center justify-center text-xl font-semibold text-white" style={{ background: "rgba(0, 0, 0, 0.45)" }} aria-label={t("ui-replyimages.more", { n: more })}>
            +{more}
          </span>
        )}
      </button>
      {failed && picture.local && (
        <div className="flex flex-wrap items-center gap-2 text-submeta text-ink-2" data-local-image-error="">
          <span>{reason || t("ui-replyimages.local_load_failed")}</span>
          <button
            type="button"
            onClick={retry}
            data-local-image-retry=""
            className="rounded-chip bg-surface px-2 py-0.5 text-submeta font-medium text-ink shadow-btn hover:bg-hover"
          >
            {t("ui-replyimages.retry_button")}
          </button>
        </div>
      )}
    </div>
  );
}

/** A run not loaded yet: its boxes in the field's color, the site and Load over them. */
function Asked({ pictures, height, onAsk }: { pictures: ReplyPicture[]; height: number; onAsk: () => void }) {
  const one = pictures.length === 1;
  const g = gridFor(pictures.length);
  const box = single(null, Number.POSITIVE_INFINITY, height);
  return (
    <button
      type="button"
      onClick={onAsk}
      data-reply-picture="asked"
      className="relative block text-left outline-none focus-visible:ring-2 focus-visible:ring-accent"
      style={{ borderRadius: RADIUS, padding: 0, ...(one ? { width: `min(100%, ${box.w}px)`, height: box.h } : squaresStyle(pictures.length)) }}
    >
      {one ? (
        <span className="block size-full bg-field shadow-hairline" style={{ borderRadius: RADIUS }} />
      ) : (
        pictures.slice(0, g.shown).map((p, i) => <span key={`${i}:${p.src}`} className="block bg-field shadow-hairline" style={{ borderRadius: RADIUS, aspectRatio: "1 / 1" }} />)
      )}
      <span className="absolute inset-0 flex flex-col items-center justify-center gap-0.5 p-2 text-center">
        <span className="inline-flex items-center gap-1.5 text-meta text-ink-2">
          <Icon name="image" size={14} />
          <span className="whitespace-nowrap">{one ? t("ui-replyimages.load_image") : t("ui-replyimages.load_images", { n: pictures.length })}</span>
        </span>
        <span className="max-w-full truncate text-submeta text-ink-3">{hostOf(pictures[0].src)}</span>
      </span>
    </button>
  );
}


/** A run in an exported picture (#9469): one picture at most PICTURE_H_NARROW high and never larger than itself; several
 *  as squares, every one of them; each through the hub's route, or, when the hub has none, a tile naming the site:
 *  nothing is read from another site. */
function ExportPictures({ pictures, through, mid, cwd }: { pictures: ReplyPicture[]; through: boolean; mid?: string; cwd?: string }) {
  const size = remoteSize(PICTURE_H_NARROW * PLACEHOLDER, 3);
  const src = (p: ReplyPicture) =>
    p.local && mid ? fileDownloadURL(mid, resolveRef(cwd ?? "", p.src) || p.src, cwd, { inline: true }) : remoteImageURL(p.src, size, getToken());
  const tile = (p: ReplyPicture, style: CSSProperties) =>
    p.local || through ? (
      <img key={p.src} src={src(p)} alt={p.alt} style={{ ...style, objectFit: "cover", borderRadius: RADIUS, display: "block" }} data-export-picture="" />
    ) : (
      <div key={p.src} style={{ ...style, borderRadius: RADIUS }} className="flex flex-col items-center justify-center gap-1 bg-field text-ink-3" data-export-picture="away">
        <Icon name="image" size={18} />
        <span className="max-w-full truncate px-2 text-submeta">{hostOf(p.src)}</span>
      </div>
    );
  if (pictures.length === 1) {
    const p = pictures[0];
    return <div style={{ margin: "8px 0" }}>{p.local || through ? tile(p, { maxHeight: PICTURE_H_NARROW, maxWidth: "100%" }) : tile(p, { height: PICTURE_H_NARROW * 0.6, width: PICTURE_H_NARROW })}</div>;
  }
  const g = grid(pictures.length, PICTURES_W);
  return (
    <div style={{ margin: "8px 0", display: "grid", gridTemplateColumns: `repeat(${g.columns}, minmax(0, 1fr))`, gap: PICTURE_GAP, width: `min(100%, ${PICTURES_W}px)` }}>
      {pictures.map((p) => tile(p, { width: "100%", aspectRatio: "1 / 1" }))}
    </div>
  );
}

/** A single local-path image that stayed as an <img> element (e.g. inside a blockquote, list, or table): uses the same
 *  authenticated fileDownloadURL, full-screen ImageViewer, and clear failure reason + manual Retry. */
export function LocalImage({ mid, cwd, src, alt = "" }: { mid: string; cwd?: string; src: string; alt?: string }) {
  const narrow = useMedia(NARROW);
  const [open, setOpen] = useState(false);
  const exporting = useContext(ReplyPicturesExport);
  const resolved = resolveRef(cwd ?? "", src) || src;
  const name = fileName(resolved);
  const picture: ReplyPicture = { src, alt, local: true };
  if (exporting) {
    return (
      <img
        src={fileDownloadURL(mid, resolved, cwd, { inline: true })}
        alt={alt}
        style={{ maxHeight: PICTURE_H_NARROW, maxWidth: "100%", objectFit: "cover", borderRadius: RADIUS, display: "block" }}
        data-export-picture=""
      />
    );
  }
  const height = narrow ? PICTURE_H_NARROW : PICTURE_H;
  const viewer: ViewerImage[] = [{ id: `local:${resolved}`, src: fileDownloadURL(mid, resolved, cwd, { inline: true }), label: alt || name, name }];
  return (
    <span className="inline-block align-middle" data-reply-images="" data-reply-images-kind="local">
      <Single
        picture={picture}
        height={height}
        src={(attempt) => localImageSrc(mid, resolved, cwd, attempt)}
        failureKey={`${mid}\0${cwd ?? ""}\0${resolved}`}
        onOpen={() => setOpen(true)}
      />
      {open && <ImageViewer images={viewer} start={0} onClose={() => setOpen(false)} />}
    </span>
  );
}
