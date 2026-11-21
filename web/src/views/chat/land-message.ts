// Deep links and forwarded originals page back only when someone asks to open a message.
import { t } from "../../i18n";
import { ApiError, fullItem } from "../../lib/api";
import type { Item } from "../../lib/types";
import { insideFold } from "../../lib/turns";
import { toast } from "../../ui/atoms";
import { openFoldRange } from "./expanded";
import type { Transcript } from "./useTranscript";

const LANDING_WAIT_MS = 5000;
const nextFrame = () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));

/** Guarded links keep the identity even after reloading or opening in another tab. */
export function readStarTarget(target: string) {
  const match = /^star:([a-f0-9]{32}):(line[0-9]+#[0-9]+)$/.exec(target);
  return match ? { fingerprint: match[1]!, item: match[2]! } : undefined;
}

export interface LandingProof {
  selector: string;
  matches: (items: readonly Item[]) => boolean;
  unavailable: () => void;
}

interface Landing {
  target: string;
  mid: string;
  sid: string;
  scope: string;
  fromLink: boolean;
  current: () => boolean;
  transcript: () => Transcript;
  seek: (item: string, quiet?: boolean, proof?: LandingProof) => void;
  loadEarlier: (stop: () => boolean) => Promise<unknown>;
}

export async function landMessage({ target, mid, sid, scope, fromLink, current, transcript, seek, loadEarlier }: Landing) {
  if (!current()) return;
  const saved = readStarTarget(target);
  const item = saved?.item ?? target;
  const proof: LandingProof | undefined = saved ? {
    selector: `[data-source-fingerprint="${saved.fingerprint}"]`,
    matches: (items) => items.some((entry) => entry.id === item && entry.source_fingerprint === saved.fingerprint),
    unavailable: () => toast(t("starred.source_unavailable")),
  } : undefined;
  const open = (quiet = false) => requestAnimationFrame(() => {
    if (current()) seek(item, quiet, proof);
  });
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
