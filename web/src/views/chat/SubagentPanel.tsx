// Full subagent detail panel (#14323, revising #5030): renders an embedded ChatView for any subagent
// (embedded `<parent>@<agent>`, same-machine folded child, or cross-machine A2A child) in the right side panel
// (full-screen overlay on narrow viewports) while leaving the parent chat column untouched.
import { useCallback, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { sessionAgent } from "../../lib/agent-kind";
import { errorText, pastHub, sessionPath } from "../../lib/api";
import { apiOp } from "../../lib/ops";
import { useBackgroundChat } from "../../lib/background-panel";
import { sessionCan, sessionTitle } from "../../lib/format";
import { openFromPage } from "../../lib/handoff";
import { stopMessageFeedback } from "../../lib/message-send";
import { useSidePanelBack } from "../../lib/panel";
import { getSession, useStore } from "../../lib/store";
import { splitSubagentLink } from "../../lib/subagent-link";
import { supervisorHeld } from "../../lib/supervisor-held";
import { buildTaskIndex, subagentCardKey, subagentTitle } from "../../lib/tasks";
import type { Item } from "../../lib/types";
import { useChildRow } from "../../lib/use-child-sessions";
import { mediaMatches, NARROW } from "../../lib/viewport";
import { AgentMark } from "../../ui/AgentMark";
import { Button, IconButton, Menu, toast, useMenu, type MenuItem } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { StatusMark } from "../../ui/StatusMark";
import { ChatView } from "./ChatView";
import { openExpanded } from "./expanded";
import {
  embeddedSubagentStatus,
  standaloneSubagentStatus,
  subagentProvenance,
  type OverviewSubagentStatus,
} from "./overview-subagents";
import { useSharedTranscript } from "./transcript-share";

const EMPTY_ITEMS: Item[] = [];

export function SubagentPanel({
  mid,
  sid,
  parentMid,
  parentSid,
  call,
  from,
  onClose,
}: {
  mid: string;
  sid: string;
  parentMid?: string;
  parentSid?: string;
  call?: string;
  from?: "overview" | "card" | "link";
  onClose: () => void;
}) {
  const parsed = splitSubagentLink(sid);
  const resolvedParentMid = parentMid ?? mid;
  const resolvedParentSid = parentSid ?? parsed?.parent;

  const held = useChildRow(mid, sid);
  const stored = useStore(() => getSession(mid, sid));
  const child = held ?? stored;
  const parent = useStore(() => (resolvedParentSid ? getSession(resolvedParentMid, resolvedParentSid) : undefined));
  const machines = useStore((s) => s.machines);
  const interactions = useStore((s) => s.interactions);
  const contextBack = useSidePanelBack();
  const more = useMenu();

  const embeddedMeta = parsed
    ? parent?.subagents?.find((a) => a.id === parsed.agent || a.session_id === sid)
    : undefined;
  const parentTr = useSharedTranscript(resolvedParentMid, resolvedParentSid ?? "");
  const parentItems = parentTr?.items ?? EMPTY_ITEMS;
  const parentTasks = useMemo(() => buildTaskIndex(parentItems), [parentItems]);

  const title = child
    ? sessionTitle(child)
    : embeddedMeta
      ? subagentTitle(embeddedMeta)
      : parsed?.agent || sid;

  const agent = child ? sessionAgent(child) : parent ? sessionAgent(parent) : { agent: "claude" };
  const provenance = parent ? subagentProvenance(parent, mid, agent, machines) : "";

  const status: OverviewSubagentStatus = child
    ? standaloneSubagentStatus(child, interactions)
    : embeddedMeta && resolvedParentSid
      ? embeddedSubagentStatus(mid, resolvedParentSid, embeddedMeta, interactions, parentItems, parentTasks)
      : interactions.some((ix) => !supervisorHeld(ix) && ix.machine_id === mid && (ix.session_id === sid || (parsed && ix.agent_id === parsed.agent)))
        ? "needs_you"
        : "ended";

  const active = status === "running" || status === "needs_you";
  const canStop = active && Boolean(child && sessionCan(child, "interrupt"));

  const [stopping, setStopping] = useState(false);
  const stopInFlight = useRef(false);
  const stopSubagent = useCallback(async () => {
    if (stopInFlight.current || !child || !sessionCan(child, "interrupt")) return;
    stopInFlight.current = true;
    setStopping(true);
    try {
      const r = await apiOp<{ state?: string }>(`${sessionPath(mid, sid)}/interrupt`, {
        method: "POST",
        timeout: pastHub(90_000),
      });
      if (r.state === "unknown") throw new Error(t("lib-new-agent.outcome_unknown"));
      toast(t("chat-chatview.interrupted"));
      stopMessageFeedback(mid, sid);
    } catch (e) {
      toast(t("chat-chatview.interrupt_failed", { error: errorText(e) }), "error");
    } finally {
      stopInFlight.current = false;
      setStopping(false);
    }
  }, [child, mid, sid]);

  const parentChat = useBackgroundChat(resolvedParentMid, resolvedParentSid ?? "");
  const resolvedCall = call ?? embeddedMeta?.tool_use_id;
  const canJumpToCall = Boolean(
    resolvedParentSid &&
    resolvedCall &&
    parentChat?.jumpToCall &&
    (parentChat.hasCall ? parentChat.hasCall(resolvedCall) : Boolean(embeddedMeta?.tool_use_id)),
  );

  const menuItems: MenuItem[] = [
    ...(canJumpToCall
      ? [{
          key: "show-in-chat",
          label: t("chat-sessionoverview.show_in_chat"),
          icon: "chat" as const,
          onSelect: () => {
            if (!resolvedParentSid || !resolvedCall) return;
            const scope = `${resolvedParentMid}/${resolvedParentSid}:`;
            openExpanded([subagentCardKey(scope, resolvedCall)]);
            parentChat?.jumpToCall?.(resolvedCall);
            if (mediaMatches(NARROW)) onClose();
          },
        }]
      : []),
    {
      key: "open-as-page",
      label: t("chat-sessionoverview.open_as_page"),
      icon: "external",
      onSelect: () => {
        openFromPage({ name: "session", mid, sid, standalone: true });
        onClose();
      },
    },
  ];

  return (
    <div className="flex min-h-0 flex-1 flex-col" data-subagent-panel={`${mid}/${sid}`}>
      <header className="flex shrink-0 flex-col gap-1.5 border-b border-line px-3 py-2">
        <div className="flex min-w-0 items-center gap-1.5">
          {from === "overview" ? (
            <button
              type="button"
              data-subagent-back
              onClick={onClose}
              className="touch-hit -ml-1.5 inline-flex shrink-0 items-center gap-0.5 rounded-control px-1.5 py-1 text-xs font-medium whitespace-nowrap text-ink-2 hover:bg-hover hover:text-ink"
            >
              <Icon name="chevronLeft" size={14} />
              <span>{t("chat-chatview.session_overview")}</span>
            </button>
          ) : contextBack ? (
            <IconButton icon="arrowLeft" label={t("sidepanel.back")} onClick={contextBack} />
          ) : null}
          <span className="min-w-0 flex-1" />
          {canStop && (
            <Button
              tone="danger"
              size="sm"
              icon="square"
              busy={stopping}
              onClick={() => void stopSubagent()}
              className="whitespace-nowrap"
            >
              {t("chat-sessionoverview.stop")}
            </Button>
          )}
          <IconButton
            icon="more"
            label={t("chat-chatview.more_actions")}
            onClick={more.open}
            active={Boolean(more.anchor)}
          />
          {more.anchor && <Menu anchor={more.anchor} onClose={more.close} align="right" items={menuItems} />}
          <IconButton icon="x" label={t("sidepanel.close")} onClick={onClose} />
        </div>
        <div className="flex min-w-0 items-center gap-2">
          <span className="inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-field text-ink-2">
            <AgentMark agent={agent.agent} label={agent.label} size={13} decorative />
          </span>
          <h2 className="min-w-0 flex-1 truncate text-sm font-semibold text-ink" title={title}>
            {title}
          </h2>
          {provenance && (
            <span
              data-subagent-provenance
              title={provenance}
              className="inline-flex max-w-36 shrink-0 items-center truncate rounded-full bg-field px-1.5 py-0.5 text-submeta whitespace-nowrap text-ink-3"
            >
              {provenance}
            </span>
          )}
          <span
            data-subagent-panel-status={status}
            className="flex shrink-0 items-center gap-1 text-xs whitespace-nowrap"
          >
            {status === "needs_you" ? (
              <>
                <StatusMark state="needs_you" size="sm" label={false} />
                <span className="font-medium text-attention">{t("chat-sessionoverview.status_needs_you")}</span>
              </>
            ) : status === "running" ? (
              <>
                <StatusMark state="working" size="sm" label={false} />
                <span className="text-ink-2">{t("chat-subagentcard.status_running")}</span>
              </>
            ) : status === "failed" ? (
              <span className="font-medium text-red">{t("chat-sessionoverview.status_failed")}</span>
            ) : (
              <span className="text-ink-3">{t("chat-sessionoverview.ended")}</span>
            )}
          </span>
        </div>
      </header>
      <div className="flex min-h-0 flex-1 flex-col">
        <ChatView key={`${mid}/${sid}`} mid={mid} sid={sid} embedded />
      </div>
    </div>
  );
}
