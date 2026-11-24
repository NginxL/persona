import { useRef, useState } from "react";
import { t } from "../../i18n";
import { archiveSession } from "../../lib/agent-archives";
import { api, ApiError, errorText, isTransient, sessionPath } from "../../lib/api";
import { isBackgroundSupervisor } from "../../lib/background-supervisor";
import { NEW_MAIN_FEATURE } from "../../lib/compaction";
import { conversationPage } from "../../lib/conversation-page";
import { uuid } from "../../lib/format";
import { adoptConversation } from "../../lib/outbox";
import { apiOp } from "../../lib/ops";
import { navigate } from "../../lib/router";
import { hubHas, upsertSession, useStore } from "../../lib/store";
import type { Session } from "../../lib/types";
import { Button, IconButton, toast } from "../../ui/atoms";
import { lazyDialog } from "../LazyOverlay";

const NewMainDialog = lazyDialog(() => import("../assistant/NewMainDialog").then((m) => m.NewMainDialog));

export function ContextRestartNotice({ session, assistant, isMain }: { session: Session; assistant?: boolean; isMain: boolean }) {
  const supervisor = useStore((s) => isBackgroundSupervisor(s.assistant, session.session_id));
  const canNewMain = useStore((s) => hubHas(s.features, NEW_MAIN_FEATURE));
  const key = "context-restart-dismissed:" + JSON.stringify([session.manager, session.machine_id, session.session_id]);
  const [dismissed, setDismissed] = useState(() => {
    try { return localStorage.getItem(key) === "1"; } catch { return false; }
  });
  const [newMain, setNewMain] = useState(false);
  const [starting, setStarting] = useState(false);
  const operation = useRef<string | undefined>(undefined);
  if (dismissed || session.archived || session.is_subagent || session.read_only_reason ||
      session.space === "health" || supervisor ||
      session.compaction?.state === "running" || (isMain && !canNewMain)) return null;

  const dismiss = () => {
    setDismissed(true);
    try { localStorage.setItem(key, "1"); } catch { /* The notice stays dismissed while this chat is open. */ }
  };
  const start = async () => {
    if (isMain) {
      setNewMain(true);
      return;
    }
    if (starting) return;
    const sourcePage = location.href;
    setStarting(true);
    try {
      const path = `${sessionPath(session.machine_id, session.session_id)}/continue-in-new-chat`;
      const opId = operation.current ??= uuid();
      let result: { state?: string; session?: Session };
      do {
        result = await apiOp(path, { method: "POST", json: {}, opId, timeout: 150_000 });
      } while (result.state === "pending");
      const next = result.session;
      if (!next || result.state === "unknown") {
        toast(t("chat-contextrestart.unknown"), "error");
        return;
      }
      const manager = next.manager ?? session.manager;
      upsertSession({ ...next, manager });
      if (assistant) adoptConversation(manager, next.session_id, next.machine_id);
      if (location.href === sourcePage) {
        navigate(assistant ? conversationPage(next.session_id, next.machine_id, manager)
          : { name: "session", mid: next.machine_id, sid: next.session_id });
      }
      dismiss();
      toast(t("chat-contextrestart.started"), "info", {
        label: t("chat-contextrestart.archive"),
        sticky: true,
        run: () => {
          const write = assistant
            ? api(`/assistant/conversations/${encodeURIComponent(session.session_id)}/archive`, { method: "POST", json: {} })
            : archiveSession(session);
          void write.then(() => toast(t("chat-contextrestart.archived"), "ok"),
            (e) => toast(errorText(e), "error"));
        },
      });
    } catch (e) {
      if (e instanceof ApiError && e.status < 500 && !isTransient(e) && e.body.delivered !== "unknown") {
        operation.current = undefined;
      }
      toast(errorText(e), "error");
    } finally {
      setStarting(false);
    }
  };
  const action = isMain ? t("assistant-assistantchat.new_main_ellipsis")
    : starting ? t("chat-contextrestart.starting") : t("chat-contextrestart.continue");

  return (
    <>
      <div data-context-restart-notice role="status" className="flex flex-wrap items-center gap-2 rounded-control bg-orange-tint px-3 py-1.5 text-xs text-ink">
        <span className="min-w-32 flex-1">
          {isMain ? t("chat-contextrestart.main") : t("chat-contextrestart.regular")}
        </span>
        <Button tone="quiet" className="touch-hit shrink-0 whitespace-nowrap" onClick={start} disabled={starting}>{action}</Button>
        <IconButton icon="x" label={t("chat-contextrestart.dismiss")} onClick={dismiss} disabled={starting} />
      </div>
      {newMain && isMain && <NewMainDialog open onClose={() => setNewMain(false)} session={session} />}
    </>
  );
}
