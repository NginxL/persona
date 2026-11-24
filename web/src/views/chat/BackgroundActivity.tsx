import { useEffect, useMemo } from "react";
import { buildTaskIndex } from "../../lib/tasks";
import { chatLines } from "../../lib/turns";
import { loadFeed, loadedFrom, useFeed, type FeedItem } from "../../lib/feed";
import { splitBackgroundActivity } from "../../lib/quiet-activity";
import { NO_WORKFLOW_CALLS } from "../../lib/workflows";
import { t } from "../../i18n";
import { Button, Dialog, Loading } from "../../ui/atoms";
import { CardConversation } from "../../ui/AssistantCard";
import { FeedLineView } from "../assistant/Tasks";
import { ChatContext, type ChatContextValue } from "./chat-context";
import { ChatLineView } from "./Lines";
import { useTranscript } from "./useTranscript";

const NO_FEED: FeedItem[] = [];
const NO_SUBAGENTS: never[] = [];

/** The same read-only full quiet-round renderer from either entry. Nothing here calls viewSession or marks seen. */
export default function BackgroundActivity({ open, onClose, mid, sid, updatedAt, rev, live = false }: {
  open: boolean;
  onClose: () => void;
  mid: string;
  sid: string;
  updatedAt?: string;
  rev?: number;
  live?: boolean;
}) {
  return (
    <Dialog open={open} onClose={onClose} title={t("chat-chatview.background_activity")} width={760}>
      {open && <ActivityBody mid={mid} sid={sid} updatedAt={updatedAt} rev={rev} live={live} />}
    </Dialog>
  );
}

function ActivityBody({ mid, sid, updatedAt, rev, live }: { mid: string; sid: string; updatedAt?: string; rev?: number; live: boolean }) {
  const tr = useTranscript(mid, sid, { assistant: true, updatedAt, rev, readOnly: true });
  const feed = useFeed((s) => s.feeds[sid] ?? NO_FEED);
  useEffect(() => { void loadFeed(sid); }, [sid]);
  const activity = useMemo(
    () => splitBackgroundActivity(chatLines(tr.items, { background: true, main: true, live }), feed, tr.hasMore ? loadedFrom(tr.items) : undefined).activity,
    [tr.items, tr.hasMore, feed, live],
  );
  const tasks = useMemo(() => buildTaskIndex(tr.items), [tr.items]);
  const ctx = useMemo<ChatContextValue>(() => ({ tasks, subagents: NO_SUBAGENTS, scope: `${mid}/${sid}:background:`, workflowCalls: NO_WORKFLOW_CALLS }), [tasks, mid, sid]);
  return (
    <div className="flex flex-col gap-4" data-background-activity>
      {tr.loading && !tr.items.length && <div className="flex justify-center py-10"><Loading /></div>}
      {tr.error && !tr.items.length && <div className="flex flex-col items-center gap-3 py-8 text-center"><p className="text-meta text-ink-2">{tr.error}</p><Button icon="retry" onClick={tr.reload}>{t("chat-chatview.retry")}</Button></div>}
      {!tr.loading && !activity.length && !tr.error && <p className="py-8 text-center text-meta text-ink-3">{t("chat-chatview.background_empty")}</p>}
      <ChatContext.Provider value={ctx}>
        <CardConversation.Provider value={sid}>
          {activity.map(({ line, feed }) => (
            <section key={line.id} className="flex min-w-0 flex-col gap-3" data-background-round={line.id}>
              <ChatLineView line={line} mid={mid} sid={sid} expandFold={tr.expandFold} assistant />
              {feed.map((item) => <div key={item.id} data-activity-feed={item.kind}><FeedLineView item={item} simplified /></div>)}
            </section>
          ))}
        </CardConversation.Provider>
      </ChatContext.Provider>
      {tr.hasMore && <Button tone="quiet" disabled={tr.loadingEarlier} onClick={() => void tr.loadEarlier({ waitForRetry: true })}>{t(tr.loadingEarlier ? "chat-chatview.loading_earlier" : "chat-chatview.background_load_earlier")}</Button>}
    </div>
  );
}
