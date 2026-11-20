// In-conversation search (docs/web/web.md "Search in chat", Android ChatSearch.kt, parity N26): where a hit sits in the
// chat's lines, for going to it; paging back and fetching the folded work for the search. The matching itself is
// chat-search-match.ts, which only the search panel loads (off the first screen).
import type { Item } from "./types";
import { sortItems, type ChatLine, type Step } from "./turns";
import type { WorkNode } from "./work-groups";

export type SearchCategory = "text" | "quoted" | "thinking" | "tool_name" | "tool_input" | "tool_output" | "event";

/** One match: the item, what of it matched, the text around the first match. */
export interface ChatSearchHit {
  itemId: string;
  category: SearchCategory;
  snippet: string;
  /** [start, end) offsets of every match in the snippet (UTF-16, as the DOM counts). */
  ranges: [number, number][];
}

export interface ChatSearchResults {
  hits: ChatSearchHit[];
  /** More matched than SEARCH_LIMIT. */
  truncated: boolean;
}

/** The items a step draws (a tool call: its call, result and pictures). */
export function stepItemIds(step: Step): string[] {
  return step.kind === "tool" ? [step.use.id, ...(step.result ? [step.result.id] : []), ...step.images.map((i) => i.id)] : [step.item.id];
}

/** The items under work nodes, nested groups included. */
export function nodeItemIds(nodes: WorkNode[]): string[] {
  return nodes.flatMap((n) => (n.kind === "group" ? nodeItemIds(n.children) : stepItemIds(n.step)));
}

export function lineItemIds(line: ChatLine): string[] {
  switch (line.kind) {
    case "supervision":
      return line.lines.flatMap(lineItemIds);
    case "segment": {
      const s = line.seg;
      return [...(s.user ? [s.user.id, ...(s.user.rawInput ?? s.user.images).map((i) => i.id)] : []), ...s.steps.flatMap(stepItemIds), ...(s.answer ? [s.answer.id] : []), ...s.folds.map((f) => f.id)];
    }
    case "recaps":
      return line.items.map((i) => i.id);
    default:
      return [line.item.id];
  }
}

/** The line a hit is on: the one that draws it (the work a wake started is its own exchange's line, #9140), else the line
 *  of the nearest item before it that a line draws (another block of your message, a notice folded into a compaction). */
export function lineOfItem(lines: ChatLine[], items: Item[], itemId: string): string | undefined {
  const owner = new Map<string, string>();
  const claim = (l: ChatLine) => lineItemIds(l).forEach((id) => owner.has(id) || owner.set(id, l.id));
  lines.forEach(claim);
  const direct = owner.get(itemId);
  if (direct) return direct;
  let last: string | undefined;
  for (const it of sortItems(items)) {
    if (it.id === itemId) return last;
    last = owner.get(it.id) ?? last;
  }
  return undefined;
}

/** How many pages one "Load earlier and keep searching" loads (Android: 20). */
export const SEARCH_PAGES = 20;

/** How a run of loading earlier history ended: `start` reached the start of the chat; the others say why it stopped. */
export type SearchLoadOutcome = "start" | "stopped" | "busy" | "load_failed" | "no_progress" | "page_cap";

/** What of the transcript the run looks at between pages. */
export interface SearchLoadState {
  items: Item[];
  hasMore: boolean;
  loading: boolean;
  loadingEarlier: boolean;
  error: string;
}

/** Pages back through the history for the search (Android loadEarlierForSearch): one page at a time, `frame` after
 *  each so it lands, until the start, `stopped()`, a page that brought nothing or failed, or `pages` pages. */
export async function loadEarlierForSearch(o: {
  get: () => SearchLoadState;
  load: () => Promise<void>;
  frame: () => Promise<void>;
  stopped: () => boolean;
  onPage: (n: number) => void;
  pages?: number;
  joinEarlier?: boolean;
}): Promise<SearchLoadOutcome> {
  const pages = o.pages ?? SEARCH_PAGES;
  for (let page = 0; page < pages; page++) {
    if (o.stopped()) return "stopped";
    const now = o.get();
    if (!now.hasMore) return "start";
    if (now.loading || (now.loadingEarlier && !o.joinEarlier)) return "busy";
    const firstSeq = now.items[0]?.seq ?? Infinity;
    await o.load();
    await o.frame();
    await o.frame();
    const after = o.get();
    // nothing older came: failed if an error stands (the same text as an earlier failure counts), else just nothing
    if ((after.items[0]?.seq ?? Infinity) >= firstSeq) return after.error ? "load_failed" : "no_progress";
    o.onPage(page + 1);
    if (!after.hasMore) return "start";
  }
  return "page_cap";
}

/** Where something is while the rows around it open or close: its y in the scrolled content, and what was found. */
export interface Located<T> {
  y: number;
  value: T;
}

/** Calls `locate` every frame until what it finds holds its y for `stillFrames` frames (the rows above it have stopped
 *  opening or closing), or `giveUpMs` passes. Gives what was found last; `missing` if nothing ever was; `cancelled` once
 *  `cancelled()` (another go started). `locate` may move the view each frame (the way back keeps its line in place). */
export async function untilStill<T>(o: {
  locate: (waited: number) => Located<T> | null;
  frame: () => Promise<void>;
  now: () => number;
  cancelled: () => boolean;
  giveUpMs: number;
  stillFrames?: number;
}): Promise<{ value: T } | "missing" | "cancelled"> {
  const need = o.stillFrames ?? 3;
  const started = o.now();
  let last: Located<T> | null = null;
  let still = 0;
  for (;;) {
    await o.frame();
    if (o.cancelled()) return "cancelled";
    const waited = o.now() - started;
    const at = o.locate(waited);
    if (at) {
      still = last && Math.abs(at.y - last.y) < 0.5 ? still + 1 : 0;
      last = at;
      if (still >= need) return { value: at.value };
    }
    if (waited >= o.giveUpMs) return last ? { value: last.value } : "missing";
  }
}

/** At most this much of a chat's folded work is fetched for its search (text and tool input, in UTF-16 units). */
export const FOLD_SEARCH_BUDGET = 4 << 20;

/** The size a page of a fold's items counts against the budget. */
export function foldItemsSize(items: readonly Item[]): number {
  let n = 0;
  for (const i of items) n += (i.text?.length ?? 0) + (i.tool_input === undefined ? 0 : JSON.stringify(i.tool_input).length);
  return n;
}

/** How fetching a chat's folds for its search ended: every one in, some failed, the budget ran out, or it was stopped. */
export interface FoldFetchOutcome {
  failed: number;
  capped: boolean;
  stopped: boolean;
}

/** Fetches the steps of `folds` (the work the hub folded, #6281) for search in chat: the newest first, `concurrency` at
 *  a time, each through `load` (the pages a fold's own opening reads), until `budget` is spent or `stopped()`. Each fold
 *  that comes in goes to `onFold`; a fold that fails counts and the others go on. */
export async function fetchFoldsForSearch(o: {
  folds: readonly Item[];
  load: (fold: Item) => Promise<Item[]>;
  onFold: (fold: Item, items: Item[]) => void;
  stopped: () => boolean;
  budget?: number;
  concurrency?: number;
}): Promise<FoldFetchOutcome> {
  const queue = [...o.folds].filter((f) => f.kind === "fold" && f.fold).sort((a, b) => b.seq - a.seq);
  let left = o.budget ?? FOLD_SEARCH_BUDGET;
  let failed = 0;
  let capped = false;
  const worker = async () => {
    for (let f = queue.shift(); f; f = queue.shift()) {
      if (o.stopped()) return;
      if (left <= 0) {
        capped = true;
        return;
      }
      try {
        const items = await o.load(f);
        if (o.stopped()) return;
        left -= foldItemsSize(items);
        o.onFold(f, items);
      } catch {
        failed++;
      }
    }
  };
  await Promise.all(Array.from({ length: Math.max(1, o.concurrency ?? 2) }, worker));
  return { failed, capped: capped || (left <= 0 && queue.length > 0), stopped: o.stopped() };
}

/** The fold `itemId` came from, among the folds fetched for the search. */
export function foldHolding(fetched: ReadonlyMap<string, { fold: Item; items: Item[] }>, itemId: string): Item | undefined {
  for (const { fold, items } of fetched.values()) if (items.some((i) => i.id === itemId)) return fold;
  return undefined;
}
