// The earlier stretches of a conversation that moved between Claude Code and Codex (#5054, lib/engine-switch.ts), as
// the chat around them sees them. This part is on the first screen and small: the live stretch's items (all the
// transcript's, unless the live session ran an earlier stretch too: A → B → A) and what the stretches' own module
// (EarlierStretches.tsx, loaded only for a conversation that switched) last said. That module works out the stretches,
// reads the other sessions, read only and lazily, draws the earlier stretches above the live one and says whether
// anything older is left and how to load it. Until it has, the live transcript's own pages load as in any chat.
import { useCallback, useMemo, useState } from "react";
import { liveStretch, stretchesKey } from "../../lib/engine-switch";
import type { EngineLeg, Item } from "../../lib/types";

/** What the chat's own transcript (useTranscript, the live session's) gives the stretches. */
export interface LiveTranscript {
  items: Item[];
  hasMore: boolean;
  loading: boolean;
  loadingEarlier: boolean;
  earlierPage?: symbol;
  loadEarlier: (options?: { waitForRetry: boolean }) => Promise<symbol | undefined>;
  expandFold: (fold: Item) => Promise<boolean | void>;
}

/** One earlier stretch as the chat draws it: its session's items in it, and how its folds open. */
export interface EarlierStretch {
  /** Unique in the conversation: the stretch's index and session. */
  key: string;
  index: number;
  sid: string;
  /** Its engine ("claude" / "codex"), as the row's history says. */
  agent?: string;
  items: Item[];
  expandFold: (fold: Item) => Promise<boolean | void>;
}

/** What the stretches' module tells the chat, each time it changes. */
export interface StretchesReport {
  hasMore: boolean;
  loading: boolean;
  loadEarlier: LiveTranscript["loadEarlier"];
  /** Identities of the live and previous-session pages accounted for in the rendered stretches. */
  projectedLivePage?: symbol;
  projectedOtherPage?: symbol;
  /** The earlier stretches drawn, oldest first: a new array whenever what they draw changes. */
  stretches: EarlierStretch[];
  /** The local calendar days (YYYY-MM-DD) the drawn stretches cover: the live transcript skips its own divider for
   *  them, so a day split across an engine switch gets one divider, not two. */
  dayKeys: string[];
  /** Moves each time a page that failed may be asked for again (its wait is over): the chat asks if its top is in view. */
  due: number;
}

/** The stretches' module's props (EarlierStretches.tsx). */
export interface StretchesHost {
  mid: string;
  sid: string;
  /** Identifies the conversation's stretches: a report for another is not used. */
  scope: string;
  /** The row's engine_history: the same array while it says the same. */
  history: readonly EngineLeg[];
  live: LiveTranscript;
  report: (scope: string, r: StretchesReport) => void;
}

export interface Stitched {
  /** The live stretch's items: what the chat draws itself, and all its rail, jumps and search cover. */
  liveItems: Item[];
  /** The live stretch has older items to load: the live session's own pages, until they reach its earlier stretch. A
   *  live session that ran an earlier stretch too (A → B → A) has its live stretch's items in a new array; otherwise
   *  they are the transcript's own. */
  liveHasMore: boolean;
  /** The earlier stretches drawn, oldest first. */
  stretches: EarlierStretch[];
  /** The calendar days the drawn stretches cover (StretchesReport.dayKeys). */
  stretchDays: readonly string[];
  /** Anything older left to load, the live session's own pages included. */
  hasMore: boolean;
  loadingEarlier: boolean;
  /** Loads what comes next going back: the live session's older page, or the next stretch's. */
  loadEarlier: LiveTranscript["loadEarlier"];
  projectedLivePage?: symbol;
  projectedOtherPage?: symbol;
  /** EarlierStretches' `due`: 0 for a conversation that never switched. */
  due: number;
  /** The stretches' module's props; null for a conversation that never switched. */
  host: StretchesHost | null;
}

const NO_STRETCHES: EarlierStretch[] = [];

export function useEarlierStretches(mid: string, sid: string, history: readonly EngineLeg[] | undefined, live: LiveTranscript): Stitched {
  const key = stretchesKey(history);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const legs = useMemo(() => history ?? [], [key]);
  const scope = `${mid}\u0000${sid}\u0000${key}`;
  const [said, setSaid] = useState<{ scope: string; r: StretchesReport } | null>(null);
  const report = said?.scope === scope ? said.r : undefined;
  const hear = useCallback((at: string, r: StretchesReport) => setSaid({ scope: at, r }), []);
  // the live stretch's items when the live session ran an earlier one too; else the transcript as it is
  const own = useMemo(() => liveStretch(live.items, legs, sid), [live.items, legs, sid]);
  const host = useMemo<StretchesHost | null>(
    () => (key ? { mid, sid, scope, history: legs, live, report: hear } : null),
    // `live` is read through: its items, pages and loads are what the module plans with
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [mid, sid, scope, key, legs, hear, live.items, live.hasMore, live.loading, live.loadEarlier, live.expandFold, live.earlierPage],
  );
  return {
    liveItems: own ?? live.items,
    // the live stretch reaches its start once an item of the live session's earlier stretch is loaded too
    liveHasMore: live.hasMore && (!own || own.length === live.items.length),
    stretches: report?.stretches ?? NO_STRETCHES,
    stretchDays: report?.dayKeys ?? [],
    // until the module has said (it loads with the chat): the live session's own pages, and something earlier is left
    hasMore: key ? (report?.hasMore ?? true) : live.hasMore,
    loadingEarlier: live.loadingEarlier || Boolean(report?.loading),
    loadEarlier: report?.loadEarlier ?? live.loadEarlier,
    projectedLivePage: key ? report?.projectedLivePage : live.earlierPage,
    projectedOtherPage: report?.projectedOtherPage,
    due: report?.due ?? 0,
    host,
  };
}
