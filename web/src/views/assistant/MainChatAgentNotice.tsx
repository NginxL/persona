import { useEffect, useRef } from "react";
import { t } from "../../i18n";
import { ASSISTANT_MODEL_PICK_FEATURE, mainChatAgentMismatch } from "../../lib/assistant-models";
import { launcherTitle } from "../../lib/provider-names";
import { assistantLaunchers, readAssistantLaunchers, useModelLists } from "../../lib/session-models";
import { hubHas, useStore } from "../../lib/store";
import type { Session } from "../../lib/types";
import { Button } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

/** The Main chat setting names another agent kind than this conversation runs: say what runs now and offer the existing
 *  fresh-start path. A same-kind default, an unknown list or a conversation's pick shows nothing. */
export function MainChatAgentNotice({ session, isMain, onNewMain }: { session: Session; isMain: boolean; onNewMain?: () => void }) {
  const assistant = useStore((s) => s.assistant);
  const kept = useStore((s) => hubHas(s.features, ASSISTANT_MODEL_PICK_FEATURE));
  const mid = session.machine_id;
  useModelLists();
  const { info, read: renderedRead } = assistantLaunchers(mid);
  useEffect(() => {
    if (isMain && mid) void readAssistantLaunchers(mid, { again: true });
  }, [isMain, mid]);
  // A hub-side default change can arrive while this page stays open. Follow the pill: a pushed session revision
  // re-reads the launchers, and useModelLists redraws this notice with the answer so a stale mismatch cannot linger.
  const lastRev = useRef<number | undefined>(session.rev);
  useEffect(() => {
    if (!isMain || !mid || session.rev === undefined || session.rev === lastRev.current) return;
    lastRev.current = session.rev;
    const read = readAssistantLaunchers(mid, { again: true });
    // Only the exact request seen at render can have preceded this revision. If it already settled, this call returned
    // a new promise and needs no trailing read; a request another effect starts for this revision also needs none.
    if (!renderedRead || read !== renderedRead) return;
    let active = true;
    void read.then(() => {
      if (active) void readAssistantLaunchers(mid, { again: true });
    });
    return () => { active = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only a new revision needs this render's request identity.
  }, [isMain, mid, session.rev]);
  const mismatch = mainChatAgentMismatch({ session, isMain, kept, config: assistant?.launcher }, info);
  if (!mismatch) return null;
  const named = (launcher: string) => launcherTitle(launcher, info?.launchers);
  return (
    <div data-main-chat-agent-notice role="status" className="flex min-h-9 items-center gap-2 rounded-[12px] bg-surface px-3 py-1.5 text-xs text-ink-2 shadow-card">
      <Icon name="info" size={13} className="shrink-0 text-ink-3" />
      <span className="min-w-0 flex-1 break-words">
        {t("assistant-mainagentnotice.main_other_kind", { configured: named(mismatch.configuredLauncher), actual: named(mismatch.actualLauncher) })}
      </span>
      {onNewMain && <Button tone="quiet" className="touch-hit shrink-0" onClick={onNewMain}>{t("assistant-assistantchat.new_main_ellipsis")}</Button>}
    </div>
  );
}
