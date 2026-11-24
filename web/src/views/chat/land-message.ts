// Search hits, deep links and forwarded originals share the final seek after any paging or fold expansion.
import { t } from "../../i18n";
import { untilStill } from "../../lib/chat-search";
import { ApiError, fullItem } from "../../lib/api";
import type { Item } from "../../lib/types";
import { insideFold } from "../../lib/turns";
import { toast } from "../../ui/atoms";
import { revealItem } from "./search-reveal";
import { openFoldRange } from "./expanded";
import type { Transcript } from "./useTranscript";

const LANDING_WAIT_MS = 5000;
const FIND_ROW_MS = 400;
const SETTLE_ROW_MS = 2000;
const nextFrame = () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));

/** Guarded links keep the identity even after reloading or opening in another tab. */
export function readStarTarget(target: string) {
  const match = /^star:([a-f0-9]{32}):(line[0-9]+#[0-9]+)$/.exec(target);
  return match ? { fingerprint: match[1]!, item: match[2]! } : undefined;
}

interface LandingProof {
  selector: string;
  matches: (items: readonly Item[]) => boolean;
  unavailable: () => void;
}

export interface MessageView {
  scope: string;
  transcript: () => Transcript;
  lineFor: (id: string) => string | undefined;
  beginSeek: () => HTMLElement | null;
  land: (row: HTMLElement, scroller: HTMLElement) => void;
}

async function seekItem(view: MessageView, itemId: string, current: () => boolean, quiet = false, proof?: LandingProof) {
  if (!current()) return;
  const sourceMatches = () => !proof || proof.matches(view.transcript().items);
  if (!sourceMatches()) return proof?.unavailable();
  const sc = view.beginSeek();
  if (!sc) return;
  const finishReveal = revealItem(view.scope, itemId);
  const result = await untilStill<HTMLElement>({
    frame: nextFrame, now: () => performance.now(), cancelled: () => !current(), giveUpMs: SETTLE_ROW_MS,
    // Give opening rows a few frames before falling back to the line that owns the item.
    locate: (waited) => {
      if (!sourceMatches()) return null;
      const exact = sc.querySelector<HTMLElement>(`[data-items~="${CSS.escape(itemId)}"]${proof?.selector ?? ""}`);
      const lineId = exact || proof || waited <= FIND_ROW_MS ? undefined : view.lineFor(itemId);
      const el = exact ?? (lineId ? sc.querySelector(`[data-line="${CSS.escape(lineId)}"]`)?.firstElementChild : null);
      return el instanceof HTMLElement ? { y: el.getBoundingClientRect().top - sc.getBoundingClientRect().top + sc.scrollTop, value: el } : null;
    },
  });
  finishReveal();
  if (!current() || result === "cancelled") return;
  if (proof && (result === "missing" || !sourceMatches() || !result.value.matches(proof.selector))) return proof.unavailable();
  if (result === "missing") {
    if (!quiet) toast(t("chat-chatview.search_not_found"));
    return;
  }
  view.land(result.value, sc);
}

export async function landSearchHit(item: string, fold: Item | undefined, view: MessageView, current: () => boolean) {
  if (!current()) return;
  if (fold?.fold) {
    openFoldRange(view.scope, fold.seq, fold.fold.through_seq);
    try {
      await view.transcript().expandFold(fold);
      await nextFrame();
    } catch {
      if (current()) toast(t("chat-chatview.search_not_found"));
      return;
    }
  }
  await seekItem(view, item, current);
}

interface Landing {
  target: string;
  mid: string;
  sid: string;
  view: MessageView;
  fromLink: boolean;
  current: () => boolean;
  loadEarlier: (stop: () => boolean) => Promise<unknown>;
}

export async function landMessage({ target, mid, sid, view, fromLink, current, loadEarlier }: Landing) {
  const { transcript, scope } = view;
  if (!current()) return;
  const saved = readStarTarget(target);
  const item = saved?.item ?? target;
  const proof: LandingProof | undefined = saved ? {
    selector: `[data-source-fingerprint="${saved.fingerprint}"]`,
    matches: (items) => items.some((entry) => entry.id === item && entry.source_fingerprint === saved.fingerprint),
    unavailable: () => toast(t("starred.source_unavailable")),
  } : undefined;
  const open = (quiet = false) => requestAnimationFrame(() => void seekItem(view, item, current, quiet, proof));
  const holds = () => transcript().items.some((i) => i.id === item);
  let verified: Item | undefined;
  if (saved) {
    // A page can still hold the old item after an equal-length rewrite. Check the raw line even for an item
    // already on screen, including when the user opened this link directly.
    try {
      verified = await fullItem(mid, sid, item);
      if (!current()) return;
      if (verified.id !== item || verified.source_fingerprint !== saved.fingerprint) return proof?.unavailable();
    } catch (error) {
      if (current()) toast(t(error instanceof ApiError && error.status === 404 ? "starred.source_unavailable" : "starred.source_failed"));
      return;
    }
  }
  if (holds()) return void open(!saved);
  if (!transcript().hasMore) {
    try {
      // A folded reply is not on screen. Open the fold that holds the original.
      const whole = verified ?? await fullItem(mid, sid, item);
      if (!current()) return;
      const fold = transcript().items.find((f) => insideFold(whole, f));
      if (!fold) {
        if (fromLink) toast(t("chat-chatview.message_further_up"));
        return;
      }
      openFoldRange(scope, fold.seq, fold.fold!.through_seq);
      await transcript().expandFold(fold);
      open();
    } catch (error) {
      if (!fromLink || !current()) return;
      const missing = error instanceof ApiError && error.status === 404;
      if (saved) toast(t(missing ? "starred.source_unavailable" : "starred.source_failed"));
      else if (missing) toast(t("chat-chatview.search_not_found"));
    }
    return;
  }
  // Older pages may already be loading to fill the viewport. Wait, then give the target a bounded search back.
  try {
    for (let page = 0, until = performance.now() + LANDING_WAIT_MS; page < 3 && current() && !holds(); page++) {
      while ((transcript().loading || transcript().loadingEarlier) && performance.now() < until && current()) await nextFrame();
      if (current()) await loadEarlier(() => !current() || holds());
    }
    if (!current()) return;
    if (holds()) open();
    else toast(t("chat-chatview.message_further_up"));
  } catch {
    if (saved && current()) toast(t("starred.source_failed"));
  }
}
