// One conversation: an agent in herdr on one of your computers, or a Persona assistant conversation.
import { useAssistantIdentity } from "../../lib/assistant-identity";
import { useAssistantTitle, useManagerOffline } from "../ManagerParts";
import { LinkManager, useShownManager } from "../../lib/manager-choice";
import { alsoOpenText } from "../../lib/engines";
import { lazy, memo, Suspense, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, errorText, fullItem, pastHub, sessionPath } from "../../lib/api";
import { attentionReason, externalLink, failureReason, isStoppedAgent, resumeCommand, sessionCan, sessionCanRename, sessionDisplayTab, sessionMenuActions, sourceSubtitle, sessionTitle, sessionTitleDetail, shortPath, stateOf, tabLabel, uuid } from "../../lib/format";
import { useHerdrTabName } from "../../lib/agents-display";
import { conversationPage } from "../../lib/conversation-page";
import { navigate, type Route, type SessionView } from "../../lib/router";
import { canShowTerminal, composerSlot, otherView, terminalJump, terminalWaits, useSessionView, viewOnlyRow } from "../../lib/session-view";
import { invalidateOverviewPanel } from "../../lib/use-shortcuts";
import { resumeInTerminal, terminalResume } from "../../lib/agent-resume";
import { openLink } from "../../lib/links";
import { getPanel, openPanel, togglePanel, usePanel } from "../../lib/panel";
import { backgroundPanelOf, isWorkflowsCommand, latestWorkflowRun, openBackgroundPanel, provideBackgroundChat, WORKFLOWS_COMMAND } from "../../lib/background-panel";
import { machineWithHub } from "../../lib/mesh";
import { sidebarRail, sidebarShown, useLayout } from "../../lib/layout";
import { hasModes, isMuted, listedModesOf, toggleMute } from "../../lib/session-actions";
import { flipPin } from "../../lib/agent-actions";
import { isPinned } from "../../lib/pins";
import { acknowledgeLaunch } from "../../lib/new-agent";
import { useRuntimeSession } from "../../lib/runtime-session";
import { ArchivedNotice, SessionDetailNotice } from "../../ui/SessionDetailNotice";
import { REVIEW_FEATURE } from "../../lib/session-review";
import { isBackgroundSupervisor } from "../../lib/background-supervisor";
import { getSession, getState, hubHas, listenSession, refresh, refreshTodos, saveMutes, upsertSession, useStore, viewSession } from "../../lib/store";
import { codexThreadOf, waitsForFirstMessage } from "../../lib/codex-waiting";
import { foldRows } from "../../lib/folded-rows";
import { PREVIEW_FEATURE } from "../../lib/preview";
import { buildTaskIndex, loopCadence, loopNext, stableSubagents, STOP_LOOP_MESSAGE, subagentCall, subagentCardKey, terminalCalls, type TaskIndex } from "../../lib/tasks";
import { runStep, sameStep, turnDone, type RunStep } from "../../lib/run-step";
import { stabilizeLines, stableValue } from "../../lib/stable-lines";
import { planProgress, workLineStyle } from "../../lib/work-groups";
import { echoed, taggedSeen } from "../../lib/echo";
import { AGENT_MESSAGE_HOLD_FEATURE, assertDelivered, isKept, keptBubble, showKept, shownUndelivered, type KeptReply } from "../../lib/undelivered";
import { lazyPage } from "../../lib/lazy-page";
import { acceptOutgoing, forgetOutgoing, inSendOrder, keepFailed, keepOutgoing, noteWritten, outboxKey, placeFailed, registerOutboxRecheck, relayNotice, restoreOutbox, retryOutbox, SENT_GRACE_MS, settleOutbox, type DeliveryReply } from "../../lib/outbox";
import { apiOp } from "../../lib/ops";
import { beginMessageSend, observeMessageItems, settleMessageEcho, stopMessageFeedback, useMessageFeedback } from "../../lib/message-send";
import { conversationKey, pickedLauncher, pickedModel, usePickedLauncher } from "../../lib/assistant-chat";
import { splitSkillDraft } from "../../lib/skill-mentions";
import { unlinkAgentFromTodo } from "../../lib/todos";
import { t as tr } from "../../i18n";
import { lastEditablePrompt, promptsOf, rewindBlock, rewindDone, rewindSession, rewindTarget, type RewindRestore } from "../../lib/rewind";
import { EDIT_SEND_SETTLE_MS } from "../../lib/edit-last-key";
import { sendDraft } from "../../lib/drafts";
import { chatLines, freshWords, insideFold, openRecap, parseUserText, segments, type ChatLine, type UserMessage } from "../../lib/turns";
import { foldsBackgroundRounds } from "../../lib/background-fold";
import type { Interaction, Item, Session, Subagent } from "../../lib/types";
import { anchoredScroll, followsAgain, isScrollbarPress, jumpLabel, leavesOnKey, leavesOnScrollbarDrag, leavesOnTouch, leavesOnWheel, followsAfterUnfold, UNFOLD_PAUSE, type ScrollAnchor } from "../../lib/disclosure";
import { Button, copyText, cx, Dialog, IconButton, Loading, Menu, toast, useMenu, useNow, type MenuItem } from "../../ui/atoms";
import { StatusMark } from "../../ui/StatusMark";
import { Icon } from "../../ui/icons";
import { StoppedAgentLine } from "./StoppedAgentLine";
const TakeoverAction = lazy(() => import("./TakeoverAction"));
import { scrollBehavior } from "../../ui/stream/motion";
import { segmentChanges, shiftAbove } from "./offscreen";
import { glide } from "./glide";
import { AGENT_COMMANDS, Composer, type Outgoing } from "./Composer";
import { agentCommands } from "../../lib/composer-commands";
import { CLEAR_MAIN_FEATURE, clearMainContext, isClearCommand, mainCommands } from "../../lib/main-clear";
import { PendingCards, TurnCards } from "./Interactions";
import { splitTurnAsks } from "../../lib/turn-asks";
import { focusedCard, focusedFirst } from "../../lib/card-focus";
import { COMPOSER_INPUT, lostWith } from "../../lib/dock-focus";
import { FailureNote, showsFailureNote } from "./FailureNote";
import { LazyCloseoutCard } from "./closeout-chunks";
import { shownCloseout } from "../../lib/closeout";
import { AWAIT_MS, awaitingTurn, DOCK_MAX, turnStart } from "./dock";
import { HEADER_SLOT, HEADER_SLOT_END, HEADER_SLOT_START } from "./header";
import { AgentModelPill, AssistantModelPill, useAssistantTurnFix } from "./ModelPill";
import { switchRefused } from "../../lib/session-models";
import { mediaMatches, NARROW, NO_HOVER, PHONE, SHORT, useFinePointer, useMedia } from "../../lib/viewport";
import { UserBubble } from "./SegmentView";
import { LatestMessageTime } from "./latest-message-time";
import { scrollsAFold } from "./fold-scroll";
import { CommentMarks } from "./CommentMarks";
import { isWorker, recordTeam, shownWorker, teamPanelOf, teamWorkers, useTeamReveal, workerCount } from "../../lib/team";
import { hubSides, isSide, sidesAmong, SESSIONS_SIDE_FOLD_FEATURE } from "../../lib/side-fold";
import { doneSideAsk, shownSide, sidesPanelOf, toggleSides, useSideAsk } from "../../lib/side-panel";
import { RunningStatusBar } from "./RunningStatusBar";
import { latestPlan, usePlanClosed, usePlanOutOfView } from "./latest-plan";
import { ChatContext, LiveClockContext, type ChatContextValue } from "./chat-context";
import { addFreshWords, resetFreshWords } from "./fresh-words";
import { backgroundCount, backgroundLine, backgroundWaitLine, workflowAgentFor, workflowCalls, workflowCallsKey } from "../../lib/workflows";
import { backgroundTurn, waitingOnSubagents } from "../../lib/background-work";
import { doneSubagentLink, useSubagentLink } from "../../lib/subagent-link";
import { childCall, childCardKey, childOfCall, childPlaces, exchangeStarts, placeChildren, type ChildPlace } from "../../lib/child-cards";
import { useChildSessions } from "../../lib/use-child-sessions";
import { openExpanded, openFoldRange } from "./expanded";
import { seekCall } from "./jump";
import { navRailTicks, type NavRailSource, type NavRailTick } from "../../lib/nav-rail";
import { containsInput, indexedNavTicks, userInputRevision, windowUserInputs, USER_INPUT_PAGE_SIZE } from "../../lib/user-input-index";
import { useRevealedInputSeq, useUserInputs } from "./useUserInputs";
import { useChatScrollbar } from "./useChatScrollbar";
import { ChatLineView, closedRecap, closeRecap, dismissedError, dismissError, ErrorBanner, errorKey, OpenRecapLine } from "./Lines";
import { useTranscript } from "./useTranscript";
import { publishTranscript, unpublishTranscript } from "./transcript-share";
import { useEarlierStretches } from "./useEarlierStretches";
import { dayRowKey, withDayDividers } from "./day-dividers";
import type { ChatRowWithModes, ModeChanges } from "./mode-changes";
import { useFoldSearch } from "./useFoldSearch";
import { DeleteSessionDialog, preloadSessionDialogs, RenameDialog, RenameSpaceDialog, renameTitle, SessionInfoDialog } from "./SessionDialogs";
import { PermissionPicker, usePlanMode } from "./PermissionPicker";
import { permissionsCommand } from "../../lib/permissions-command";
import { archiveCloseItems, closeItems } from "../AgentMenus";
import { ChatSidebarButton } from "../ShellSwitch";
import { appArchiveDeps, offerSessionDelete, restoreAgent, useArchived } from "../../lib/agent-archives";
import { UltracodeLostNote } from "./Ultracode";
import { isUltracodeQueued, ultracodeQueueDone } from "../../lib/ultracode";
import { ChatActionsContext, liveValue, type ChatActions } from "./chat-actions";
import { QueueStrip } from "./QueueStrip";
import type { LoadOutcome } from "./ChatSearch";
import { forgetReveals, revealItem, undoReveals, clearReveal } from "./search-reveal";
import { jumpWhenOpen, openForward, peekJump, takeJump, type ForwardChat, type ForwardPick } from "../../lib/forward";
import { pickParts } from "./picking-parts";
import type { MessagePick } from "../../lib/message-pick";
import { foldHolding, lineOfItem, loadEarlierForSearch, untilStill, type ChatSearchHit, type SearchLoadOutcome } from "../../lib/chat-search";
import { chatFiles } from "../../lib/mentions";
import { pairPhotoOnly, sameMessage } from "../../lib/queue";
import { OutboxBubbles } from "./OutboxBubbles";
import { MainRuntimeSlot } from "../assistant/MainRuntimeSlot";
import { AssistantFace, CompactionStrip, useAssistantMenu } from "../assistant/AssistantChat";
import { PageBoundary } from "../PageBoundary";
import { ViewLayer } from "../session/ViewLayer";
import { conversationTitle } from "../assistant/conversations";
import { useDirectionName } from "../../lib/direction-names";
import { chatPlaceIn, dropChatPlace, firstLineInView, handoffFrom, leaveChat, type ChatPlace, type HandoffFrom } from "../../lib/handoff";
import { externalOpening, openerOf } from "../../lib/conversation-origins";
import { usePersona } from "../assistant/Persona";
import { voiceCalls, useVoiceCall, useVoiceState } from "../../lib/voice/calls";
import { agentLabel } from "../../lib/mentions";
import { useVoiceEntry, VoiceBar, VoiceEnded, VoiceTail } from "./voice/VoiceParts";
import { ChatOnlyNote, FailureCard, ReadinessCard, useReadinessGate, type ReadinessGate } from "../assistant/Readiness";
import { codedFailure, knownAction, refreshReadiness, type CodedFailure } from "../../lib/readiness";
import { FALLBACK_UNAVAILABLE, recoveryExhausted } from "../../lib/model-fallback";
import { BASE } from "../../lib/base";
import { usageLimitLabel } from "../../lib/usage-limit";
import { displayLines, getFeedState, loadedFrom, loadFeed, loadToday, sendRouted, startOfDay, useFeed, type FeedItem } from "../../lib/feed";
import { CardConversation } from "../../ui/AssistantCard";
import { SessionMark, sessionMarkName } from "../../ui/AgentMark";
import { sessionAgentName } from "../../lib/agent-kind";
import { AssistantActionBar, DayLine, FeedLineView, QuickResultCard, RouteChip, TaskQuestions } from "../assistant/Tasks";
import { t } from "../../i18n";
import { lazyDialog, lazyOverlay } from "../LazyOverlay";

// The terminal view (TerminalPanel and its stream / cell parsing) loads on demand: most visits only read the chat, so it
// stays out of the main chunk. A page that can switch to it fetches it in the background (loadTerminal below), so the
// first switch does not slide in an empty layer.
const loadTerminal = () => import("../session/TerminalPanel");
// Sharing settings load on their first open, outside the first screen (docs/web/web.md "First screen").
const ChatSearch = lazyDialog(() => import("./ChatSearch").then((m) => m.ChatSearch));
const LinkTodoDialog = lazyDialog(() => import("../LinkTodoDialog").then((m) => m.LinkTodoDialog));
const SharingDialog = lazyOverlay(() => import("../../sharing/SharingDialog").then((m) => m.default));
const TerminalPanel = lazy(() => loadTerminal().then((m) => ({ default: m.TerminalPanel })));
const GoalBar = lazy(() => import("./GoalBar").then((m) => ({ default: m.GoalBar })));
const SessionOverview = lazy(() => import("./SessionOverview").then((m) => ({ default: m.SessionOverview })));
// Full pages opened from a conversation's hand-off card or belonging to an Inbox item load their way-back strip on demand.
const AgentStatusLine = lazy(() => import("./AgentStatusLine").then((m) => ({ default: m.AgentStatusLine })));
const HandoffBack = lazy(() => import("./HandoffBack").then((m) => ({ default: m.HandoffBack })));
const InboxBack = lazy(() => import("./InboxBack").then((m) => ({ default: m.InboxBack })));
// the plan's pill and its flyout load once a plan wants it shown, off the first screen
const PlanPill = lazy(() => import("./PlanCard").then((m) => ({ default: m.PlanPill })));
// "Wrap up: <item>" over the main conversation's composer (#5932): with the hand-off card's chunk, not the first screen's
const LazyWrapUpChips = lazy(() => import("../assistant/HandoffCard").then((m) => ({ default: m.WrapUpChips })));
// a chat that is nowhere (a stale or mistyped link): rare, so its notice and words are a chunk of their own
const LazyNotFoundNotice = lazy(() => import("./NotFoundNotice").then((m) => ({ default: m.NotFoundNotice })));
// a start stopped on its tab's shell question, answered from here (#10365): only then
const ShellQuestion = lazy(() => import("../ShellQuestionCard"));
// only in a conversation that moved between Claude Code and Codex (#5054): its earlier sessions, read above the live one
const EarlierStretches = lazy(() => import("./EarlierStretches"));
const NavRail = lazy(() => import("./NavRail").then((m) => ({ default: m.NavRail })));
// Recovery cards are rare: their rendering and words load only when the session actually carries a fallback record.
const ModelFallbackCard = lazy(() => import("./ModelFallbackCard").then((m) => ({ default: m.ModelFallbackCard })));
// A message the assistant keeps after it could not go in (#10194) is rare: its status line, Retry and Delete load only in
// a conversation that holds one (#10357). Until they are in it shows as the owner's bubble alone, so it never blinks out.
const SessionReviewMenuItem = lazyPage(() => import("./SessionReviewMenuItem").then((m) => m.SessionReviewMenuItem));
const UndeliveredBubble = lazyPage(() => import("./UndeliveredBubbles").then((m) => m.UndeliveredBubble));

const NO_FEED: FeedItem[] = [];
// A worker's card as its own conversation shows it (#5180): without the fold that makes it "From worker" on its lead's page.
// One copy per card object, so a push that leaves the card alone leaves its copy alone too.
const ownCopies = new WeakMap<Interaction, Interaction>();
function asOwnCard(ix: Interaction): Interaction {
  let c = ownCopies.get(ix);
  if (!c) {
    c = { ...ix, folded_under: undefined, folded_as: undefined, folded_side: undefined };
    ownCopies.set(ix, c);
  }
  return c;
}
/** In this chat's own waiting cards (not the Assistant tray's): one of them, and its text fields (styles.css lets the
 *  dock grow for those while the keyboard is up). */
const OWN_CARD = "[data-chat-cards] [data-pending-card]";
const inOwnCard = (el: EventTarget | null) => el instanceof Element && Boolean(el.closest(OWN_CARD));
const isCardField = (el: EventTarget | null) => el instanceof Element && el.matches(`${OWN_CARD} :is(input, textarea)`);
/** How many earlier pages a link to a child loads looking for the exchange its card is in. */
const CHILD_LINK_PAGES = 10;
// no child sessions: no cards, and the exchanges are not walked for where they would go
const NO_PLACES = new Map<string, ChildPlace[]>();
// the assistant has no subagents: one array, so the chat context does not change with every render
const NO_SUBAGENTS: Subagent[] = [];
// no fine pointer: the rail draws nothing, so its ticks are not read
const NO_RAIL_TICKS: NavRailTick[] = [];
/** Going to a search hit: how long the rows that hold it get to open before the line it is on stands in, when to give
 *  up, and how long it stays lit (Android: 3 s). */
const FIND_MS = 400;
const GIVE_UP_MS = 2000;
/** How long landing on a message waits for the chat to stop loading before it pages back itself. */
const LANDING_WAIT_MS = 5000;
const HIT_MS = 3000;
const SEARCH_NOTICE: Record<SearchLoadOutcome, string | null> = {
  start: null,
  stopped: "chat-chatview.search_stopped",
  busy: "chat-chatview.search_busy",
  load_failed: "chat-chatview.search_load_failed",
  no_progress: "chat-chatview.search_no_progress",
  page_cap: "chat-chatview.search_page_cap",
};
const nextFrame = () => new Promise<void>((r) => requestAnimationFrame(() => r()));

// An earlier A page in A → B → A does not change the live stretch. Read the rendered stretch
// rather than the raw session or the parent's last report, which can trail the child DOM.
function visibleHistoryFront(scroller: HTMLElement, live: number) {
  const oldest = scroller.querySelector<HTMLElement>("[data-stretch-index]");
  return {
    live,
    stretch: oldest ? Number(oldest.dataset.stretchIndex) : Infinity,
    stretchSeq: Number(oldest?.dataset.stretchFirst ?? Infinity),
  };
}

interface Pending {
  /** The client_msg_id it went with. */
  id: string;
  msg: UserMessage;
  at: number;
  acceptedAt?: number;
  /** The node held it back until an ultracode change waiting for the turn is through (#3428): it goes again then. */
  waits?: boolean;
  /** Its POST has not been answered yet (it may be behind earlier ones): the bubble does not time out. */
  posting?: boolean;
}

/** How many times a message held back for an ultracode change goes again before it is taken as not sent. */
const ULTRACODE_SEND_TRIES = 4;
/** How long after the hub took a stop the button stays blocked if the agent keeps working: it may have ignored it. */
const STOP_SETTLE_MS = 15_000;

/** "Start a new session" (#2137, #5314): the node copies the conversation up to this reply's turn into a new one, which opens here. */
async function forkAt(mid: string, sid: string, itemId: string, assistant: boolean, space?: string, manager?: string) {
  try {
    const r = await api<{ session_id: string }>(`${sessionPath(mid, sid)}/fork`, { method: "POST", json: { at_item_id: itemId } });
    await refresh();
    // a health conversation's branch runs in the health space too (docs/assistant/health.md)
    // a Persona conversation's branch is on its computer and its manager's (lib/conversation-page.ts)
    navigate(!assistant ? { name: "session", mid, sid: r.session_id } : space === "health" ? { name: "health", sid: r.session_id } : conversationPage(r.session_id, mid, manager));
    toast(t("chat-chatview.forked"), "ok");
  } catch (e) {
    toast(t("chat-chatview.fork_failed", { error: errorText(e) }), "error");
  }
}

/** "Rewind to here" (#2436): the node drives Claude's /rewind to before the message; the message then goes back into the composer.
 *  `declared`: the session's node declares rewind (a runtime's session or a Codex thread, #6294), which goes back by itself:
 *  there is no Claude input box on the computer to have kept the message. */
async function rewindTo(mid: string, sid: string, draftKey: string, items: Item[], messageId: string, text: string, restore: RewindRestore, declared: boolean) {
  const target = rewindTarget(items, messageId);
  if (!target) throw new Error(tr("rewind.no_text"));
  const res = await rewindSession(mid, sid, target, restore);
  const done = rewindDone(res, restore);
  toast(done.text, done.tone);
  if (res.input_cleared) sendDraft(draftKey, { type: "replace", text });
  else if (!declared) toast(tr("rewind.input_kept"), "info");
}

/** In a side panel (TaskPanel, AgentPanel; #3333, iOS #3255; opened while Agents open in the side panel, #5424): the
 *  chat's one-line header is the panel's only one. It closes the panel (× at its end), may stand for the task (its title,
 *  with `sub` in the title's tooltip), carries the panel's own rows at the top of ⋯ and, beside the title, what the panel
 *  puts there (a task's ⌄ to switch between its agents); the same space's other agents are left out. With `page`, a chat
 *  bubble before × opens the Agent shown as its own page (`/s/…`, as from the Agents list) and the panel closes. */
export interface PanelChrome {
  onClose: () => void;
  /** The page of the Agent the panel shows: the header's "Open Agent page" button. */
  page?: Route;
  title?: string;
  sub?: string;
  items?: MenuItem[];
  beside?: React.ReactNode;
}

const DeletedConversation = lazy(() => import("../DeletedConversation").then((m) => ({ default: m.DeletedConversation })));

/** A deleted chat has no composer or retained transcript, wherever its old link is opened. */
export function ChatView(props: Parameters<typeof ActiveChatView>[0]) {
  const deleted = useStore(() => getSession(props.mid, props.sid)?.deleted === true);
  if (deleted) return <Suspense fallback={<div className="flex h-full items-center justify-center"><Loading /></div>}>
    <DeletedConversation onToggleSidebar={props.onToggleSidebar} />
  </Suspense>;
  return <ActiveChatView {...props} />;
}

function ActiveChatView({ mid, sid, assistant, starting: launching, onToggleSidebar, topCenter, inPage, embedded, panel: chrome, terminalAvailable, banner, offline, mainCache, refreshError, onRefresh, headerActions, landAt, placeholder: askPlaceholder }: {
  mid: string;
  sid: string;
  assistant?: boolean;
  starting?: boolean;
  onToggleSidebar?: () => void;
  /** On a narrow screen, in the header's middle in place of the title (the shell's "Assistant" | "Agents" switch). */
  topCenter?: React.ReactNode;
  /** The agent's own page (`/s/…`, `/t/…`): its terminal is a view of the middle, switched in place with the header's
   *  "Terminal" | "Chat" and ⌘J (docs/web/web.md "Terminal ⇄ chat"). Elsewhere the chat is all there is. */
  inPage?: boolean;
  /** A worker's conversation in its lead's Team panel (#5180, views/chat/TeamPanel.tsx): no header of its own (the panel's
   *  strip says whose it is), a phone's dock, and Esc there closes the panel rather than stopping the worker. */
  embedded?: boolean;
  /** A task's or an Agent's side panel (TaskPanel, AgentPanel): the header is the panel's (PanelChrome). */
  panel?: PanelChrome;
  /** The work browser has verified that this session has an interactive pane. Standalone chats use their existing policy. */
  terminalAvailable?: boolean;
  /** Under the header: the assistant page's manager switcher (docs/assistant/managers.md "Clients"). */
  banner?: React.ReactNode;
  /** The conversation's manager is offline: what the box says instead, and nothing can be sent. */
  offline?: string;
  /** Only the main conversation keeps its latest page across browser loads. */
  mainCache?: { scope: string; manager?: string };
  refreshError?: string;
  onRefresh?: () => void;
  /** The page's own buttons at the header's end, before the permission mode and ⋯ (Health's New and its past conversations, #4329). */
  headerActions?: React.ReactNode;
  /** A message to open the chat at (`?at=`, a search hit's, #6283): landed on once it is in, paging back for it. */
  landAt?: string;
  /** What an idle Persona chat's box asks, in place of the main chat's: a space's own (Health's, docs/assistant/health.md).
   *  The box's other states (offline, queued, unavailable…) say theirs as before. */
  placeholder?: string;
}) {
  const { session, detail, retry: retryDetail, known } = useRuntimeSession(mid, sid);
  // The main conversation is always Persona (#1313): not its first message, the node's title for it.
  const mainConversation = useStore((s) => s.assistant?.main);
  const isMain = Boolean(assistant && (mainCache || sid === mainConversation));
  // Full Agent pages and non-main Persona conversations may offer a way back; side panels, Team panels and the main chat do not.
  const backPage = Boolean(!embedded && !chrome && (inPage || (assistant && !isMain)));
  // A conversation's hand-off strip wins over the Inbox item's when both are present; null keeps it hidden after ✕.
  const [cameFrom, setCameFrom] = useState<HandoffFrom | null | undefined>(() => (backPage ? handoffFrom() : undefined));
  // the main conversation, with a hub that clears its context in place: /clear in its command list (#5095)
  const clears = useStore((s) => Boolean(assistant) && s.assistant?.main === sid && hubHas(s.features, CLEAR_MAIN_FEATURE));
  // an archived agent (lib/agent-archives.ts) is not in the lists: its chat says so and offers Restore
  const archivedHere = useArchived(mid, sid);
  // its manager offline (several managers): nothing can be sent, wherever it was opened from; the page's own word first
  const ownerOffline = useManagerOffline(session?.manager);
  // the main conversation is titled with the current assistant's name once there are several
  const mainTitle = useAssistantTitle();
  const offlineLine = offline ?? ownerOffline;
  const listed = useStore((s) => (s.sessions[mid] ?? []).find((x) => x.session_id === sid)?.subagents);
  // the same array while nothing a card shows changed (#5602): the context, and every exchange reading it, stay put
  const lastSubagents = useRef<Subagent[] | undefined>(undefined);
  const subagents = stableSubagents(lastSubagents.current, listed);
  lastSubagents.current = subagents;
  const machineName = useStore((s) => machineWithHub(mid, s.machines));
  // An Agent's computer away, on a hub that holds what is sent meanwhile (#10661): the box stays open and says the
  // messages go once it is back, in words from the held bubbles' chunk, off the first screen (UndeliveredBubbles).
  const computerAway = useStore((s) => !assistant && hubHas(s.features, AGENT_MESSAGE_HOLD_FEATURE) && s.machines.find((m) => m.id === mid)?.online === false);
  const [awayLine, setAwayLine] = useState("");
  useEffect(() => {
    if (computerAway) void import("./UndeliveredBubbles").then((m) => setAwayLine(m.heldPlaceholder(machineName)), () => {});
  }, [computerAway, machineName]);
  // A Codex opened with no first message (lib/codex-waiting.ts): once its thread comes (the message went in from here
  // or its terminal) the row goes and the thread's comes in the same pane, and this page follows it there.
  const waitsFirst = waitsForFirstMessage(session);
  const [waitingRow, setWaitingRow] = useState<Session>();
  useEffect(() => {
    if (waitsFirst && session) setWaitingRow(session);
  }, [waitsFirst, session]);
  const becameThread = useStore((s) => (waitingRow && waitingRow.session_id === sid ? codexThreadOf(s.sessions[mid] ?? [], waitingRow)?.session_id : undefined));
  useEffect(() => {
    if (becameThread && inPage) navigate({ name: "session", mid, sid: becameThread }, true);
  }, [becameThread, inPage, mid]);
  // a lead's Team panel (#5180, lib/team.ts): opened and closed by itself with cindy's timing; never in a worker's own
  // conversation (embedded), a task's or an Agent's side panel (whose place the Team panel would take) or a Persona chat
  const inPanel = Boolean(chrome);
  const leads = !embedded && !inPanel && !assistant;
  useTeamReveal(mid, sid, session, leads);
  const panel = usePanel();
  // drawn: ⌘⇧↵ (the middle alone) keeps a wide screen's panel open but not drawn, so it shows nothing there
  const maximized = useLayout().maximized;
  const narrowPage = useMedia(NARROW);
  const panelDrawn = !maximized || narrowPage;
  const teamOpen = Boolean(leads && panelDrawn && teamPanelOf(panel, mid, sid));
  // its side sessions (#7944, lib/side-fold.ts): in its Side sessions panel, never rows or cards of their own; the panel
  // opens by hand or for a link to one of them (lib/side-panel.ts), never by itself
  const sideFold = useStore((s) => s.features.includes(SESSIONS_SIDE_FOLD_FEATURE));
  const sidesHere = leads && sideFold;
  const sidesOpen = Boolean(sidesHere && panelDrawn && sidesPanelOf(panel, mid, sid));
  const sideAsk = useSideAsk(mid, sid);
  useEffect(() => {
    // after the page's other effects, as the Team panel's link does: App may bring back this session's own panel
    if (sideAsk && sidesHere) queueMicrotask(() => doneSideAsk(mid, sid, sideAsk));
  }, [sideAsk, sidesHere, mid, sid]);
  // a side session's own chat (in its parent's panel): its own children fold under that parent, naming it (folded_side)
  const sideParent = session && isSide(session) ? session.folded_under : undefined;
  // the space's other agents, its child sessions folded into their parents (#5120: a child is never one of them)
  const siblings = useStore((s) => (session?.space_id ? foldRows(s.sessions[mid] ?? []).filter((x) => x.space_id === session.space_id && stateOf(x) !== "offline") : []));
  // its own requests and those of the child sessions folded under it (#5120): a child's are answered here, in the order
  // they came, each saying which child asks; a side session's chat (#7944) takes the cards of the rows that belong to it
  const ownCard = (ix: Interaction) => ix.machine_id === mid && (ix.session_id === sid || ix.folded_under === sid || ix.folded_side === sid);
  const interactions = useStore((s) => s.interactions.filter(ownCard));
  const answered = useStore((s) => Object.values(s.answered).filter((a) => ownCard(a.ix)));
  const delegated = useStore((s) => Object.values(s.delegated).filter((d) => ownCard(d.ix)));
  // the list as pushed: its updated_at and rev are what tell the page it fell behind (useTranscript)
  const tr = useTranscript(mid, sid, { starting: Boolean(launching), updatedAt: session?.updated_at, rev: session?.rev, cache: mainCache, assistant });
  const transcriptOwner = useRef<object>({});
  useEffect(() => {
    if (!embedded) publishTranscript(transcriptOwner.current, mid, sid, tr);
  }, [embedded, mid, sid, tr]);
  useEffect(() => {
    if (embedded) return;
    return () => unpublishTranscript(transcriptOwner.current, mid, sid);
  }, [embedded, mid, sid]);
  // a conversation that moved between Claude Code and Codex (#5054): its earlier sessions read above the live one, as
  // one conversation; without engine_history this is the transcript as it is
  const earlier = useEarlierStretches(mid, sid, assistant ? undefined : session?.engine_history, tr);
  // what the rail's jumps and the search page back through: the live stretch, whose start ends it (#5054)
  const trNow = useRef(tr);
  trNow.current = { ...tr, hasMore: earlier.liveHasMore };
  const railChat = useRef(`${mid}/${sid}`);
  railChat.current = `${mid}/${sid}`;
  // The mounted conversation owns late Check refreshes, including a Check started before leaving and returning.
  const recheckTranscript = useCallback(() => {
    if (railChat.current === `${mid}/${sid}`) trNow.current.reload();
  }, [mid, sid]);
  useEffect(() => registerOutboxRecheck(mid, sid, recheckTranscript), [mid, sid, recheckTranscript]);
  const railRun = useRef(0);
  useEffect(() => () => { railRun.current++; }, [mid, sid]);
  const fine = useFinePointer();
  // a Persona conversation's send the hub refused: a card above the composer, not a toast (docs/assistant/onboarding.md 4.4),
  // while its failed bubble (`entry`) is there
  const [sendFailure, setSendFailure] = useState<{ chat: string; failure: CodedFailure; entry: string } | null>(null);
  // the main conversation's setup gate, for deliver (defined before the gate is)
  const blockedRef = useRef<ReadinessGate["blocked"]>(undefined);
  const [pending, setPending] = useState<Pending[]>(() => {
    const launch = Object.values(getState().launches).find((l) => l.phase === "ready" && l.machineId === mid && l.session?.session_id === sid);
    const out = launch?.outgoing;
    if (!launch || !out || (!out.text && !out.images.length)) return [];
    const id = launch.messageId;
    return [{ id, at: Date.now(), msg: { id: `pending:${id}`, ...parseUserText(out.text), images: out.images.map((image, i) => ({ id: `pending:${id}#${i}`, seq: 0, role: "user", kind: "image", image })) } }];
  });
  const [draftRefusal, setDraftRefusal] = useState<{ id: string; draft: string; clearable?: string } | null>(null);
  // what other pages sent here ("Ask Persona", "Hand to AI"…): shown like this page's own sending bubbles (lib/outbox.ts)
  const outbox = useStore((s) => s.outbox[outboxKey(mid, sid)] ?? []);
  const online = useStore((s) => s.conn === "online");
  const sendFeedback = useMessageFeedback(mid, sid);
  // a message Claude queued shows in the queue strip, not as a sending bubble too (ChatView.swift chatScroll)
  const queued = session?.queued ?? [];
  // a photo-only one by its photos, as no words match it (#10871)
  const photoOnly = useMemo(() => pairPhotoOnly(queued, pending.map((p) => ({ text: p.msg.text, images: p.msg.images?.length ?? 0,
    carries: Boolean(p.msg.files?.length || p.msg.contexts?.length) }))), [queued, pending]);
  const pairedPending = useMemo(() => new Set(photoOnly.values()), [photoOnly]);
  const shownPending = queued.length ? pending.filter((p, i) => !pairedPending.has(i) && !queued.some((q) => sameMessage(q, p.msg.text))) : pending;
  // the owner's messages the assistant keeps after they could not go in, from the row: on every device (#10194)
  // (not one this page still draws as sending: the row can come before the send's answer)
  const keptUndelivered = shownUndelivered(session, tr.items, pending.map((p) => p.id));
  // a queued message taken back or deleted (QueueStrip) is not on its way any more: its sending bubble goes with it,
  // rather than showing again until the turn ends (#4342)
  // the photos a Persona chat's queued message went with, when it was sent from here (its queue holds only the words),
  // and local preview data URLs for any queued message sent from this page (#10871).
  // Both stay the same while `pending` does: the queue is drawn in the memoized composer.
  const queuedPhotos = useCallback((raw: string, index: number) => {
    const paired = photoOnly.get(index);
    return (raw === "" && paired !== undefined ? pending[paired] : pending.find((p) => sameMessage(raw, p.msg.text)))?.msg.images?.length ?? 0;
  }, [pending, photoOnly]);
  const queuedPhotoPreviews = useCallback((raw: string, index: number): string[] => {
    const paired = photoOnly.get(index);
    const match = paired !== undefined ? pending[paired] : pending.find((p) => sameMessage(raw, p.msg.text));
    return (match?.msg.images ?? [])
      .map((item) => (item.image?.data ? `data:${item.image.mime};base64,${item.image.data}` : ""))
      .filter(Boolean);
  }, [pending, photoOnly]);
  const withdrawn = useCallback((text: string, index: number) => {
    const paired = queued[index] === text ? photoOnly.get(index) : undefined;
    const gone = pending.find((p) => !p.posting && sameMessage(text, p.msg.text)) ?? (paired !== undefined && !pending[paired]?.posting ? pending[paired] : undefined);
    if (!gone) return;
    forgetOutgoing(outboxKey(mid, sid), gone.id);
    setPending((ps) => ps.filter((p) => p.id !== gone.id));
  }, [pending, photoOnly, queued, mid, sid]);
  const outgoing = shownPending.length + outbox.filter((e) => e.state !== "failed").length;
  const persona = usePersona();
  const state = session ? stateOf(session) : "unknown";
  // Voice chat (docs/web/web.md "Voice"): the call belongs to this page (the same chat may be open twice, a page and a
  // side panel), and ends as the page leaves the conversation. ChatView learns only whether a call is on here.
  const voiceId = useId();
  const voiceOwner = useMemo(() => ({ mid, sid, owner: voiceId }), [mid, sid, voiceId]);
  useEffect(() => () => voiceCalls.leave(voiceOwner), [voiceOwner]);
  const voiceState = useVoiceState(voiceOwner);
  const voiceCall = useVoiceCall(voiceOwner);
  const voiceLabel = assistant ? persona.name : session ? agentLabel(session) : "Claude";
  const itemsNow = useRef(tr.items);
  itemsNow.current = tr.items;
  const readItems = useCallback(() => itemsNow.current, []);
  const voiceEntry = useVoiceEntry({ ...voiceOwner, session, items: readItems, label: voiceLabel, off: Boolean(offlineLine) || !sessionCan(session, "send") });
  // the message is in, the hub has not said working yet (#3654): the live row covers that wait too
  const segs = useMemo(() => segments(tr.items), [tr.items]);
  const lastSeg = segs[segs.length - 1];
  const [awaitNow, setAwaitNow] = useState(() => Date.now());
  const turnFailure = session?.failure_reason || session?.turn_error || lastSeg?.cut ? { since: session?.state_since } : undefined;
  // a message whose wait the hub took a stop for (#2450): it is no longer worked on, so Working… and Interrupt go at
  // once instead of after AWAIT_MS; a turn the hub reports later still shows through `state`
  const [stoppedTurn, setStoppedTurn] = useState<string>();
  const awaitedTurn = awaitingTurn(lastSeg, state, awaitNow, turnFailure, session?.state_since) ? lastSeg?.user?.id : undefined;
  const awaiting = awaitedTurn !== undefined && awaitedTurn !== stoppedTurn && !tr.streamEnded;
  const awaitingNow = useRef<string | undefined>(undefined);
  awaitingNow.current = awaiting ? awaitedTurn : undefined;
  const waitStart = awaitedTurn ? lastSeg?.user?.ts : undefined;
  useEffect(() => {
    // draw again when the wait runs out, so a message nobody picks up stops looking worked on; once something answers
    // it there is nothing to wait for (and no clock to reset on every streamed item)
    const at = waitStart ? Date.parse(waitStart) : NaN;
    if (Number.isNaN(at)) return;
    setAwaitNow(Date.now());
    const left = at + AWAIT_MS - Date.now();
    if (left <= 0) return;
    const timer = window.setTimeout(() => setAwaitNow(Date.now()), left + 10);
    return () => clearTimeout(timer);
  }, [waitStart]);
  const stopped = Boolean(stoppedTurn && stoppedTurn === lastSeg?.user?.id);
  const canShowPreview = state !== "offline" && state !== "needs_you" && !stopped && !turnFailure;
  const streaming = Boolean(tr.streaming) && !tr.streamEnded && (state === "working" || canShowPreview);
  const working = state === "working" || streaming || outgoing > 0 || awaiting;
  // The user's time stays in its row until the finished reply can show its own. An item that lands
  // while the turn is still running otherwise removes a line above the live work and moves it up.
  const latestTime = (!working && lastSeg?.answer?.ts) || lastSeg?.user?.ts;
  // still starting only until the conversation has anything in it: the flag the caller passes may outlive the start
  const starting = Boolean(launching) && !tr.items.length && !tr.streaming && state !== "working";
  // a new conversation with its first message on the way and no transcript yet (#1697): the lines the transcript opens
  // with ("Chat started", today) are there already and no loader goes above the message, so it doesn't move when they come
  const opening = Boolean(assistant) && !tr.items.length && outgoing > 0;

  // ── the middle of an agent's page: its conversation, or its terminal in place (lib/session-view.ts) ──
  const switchable = Boolean(inPage && terminalAvailable !== false && !assistant && canShowTerminal(session));
  const openFiles = () => session && togglePanel({ kind: "files", mid, sid, cwd: session.cwd });
  const [view, setView] = useSessionView(mid, sid, switchable);
  // The terminal connects when first shown and stays connected while the page is on screen (the chat is hidden under it,
  // not gone: coming back is instant). In the background, or when the agent stops being reachable, it lets the pane go.
  const [termKept, setTermKept] = useState(false);
  if (switchable && view === "term" && !termKept) setTermKept(true);
  if (!switchable && termKept) setTermKept(false);
  useEffect(() => {
    // a failed prefetch is not an error yet: the lazy load retries it on the switch, under PageBoundary
    if (switchable) loadTerminal().catch(() => {});
  }, [switchable]);
  const viewNow = useRef(view);
  viewNow.current = view;
  useEffect(() => {
    const hide = () => document.visibilityState === "hidden" && viewNow.current !== "term" && setTermKept(false);
    document.addEventListener("visibilitychange", hide);
    return () => document.removeEventListener("visibilitychange", hide);
  }, []);


  // what the messages' menus, the image viewer and the @ menu read from this chat (chat-actions.ts)
  const items = useRef(tr.items);
  items.current = tr.items;
  const draftKey = `${mid}/${sid}`;
  const canCompose = sessionCan(session, "send");
  // a stopped Agent (#4754): a read-only line with Terminal and Resume where the composer goes
  const stoppedLine = composerSlot(session, { assistant: Boolean(assistant), offline: Boolean(offlineLine) }) === "stopped";
  // one function while this chat stays put: the header takes it, and must not draw again for every pushed item
  const openTerminal = useCallback(
    () => (switchable ? setView("term") : navigate({ name: "session", mid, sid, view: "term" })),
    [switchable, setView, mid, sid],
  );
  // No row for this chat yet is not "you may not write": the box takes typing and says it is connecting, or, when the hub
  // answered that the session is gone, says that instead. Only the main conversation may already send; every other chat
  // waits for its row, because the node would start an Agent for an id nobody has. A lookup that found nothing leaves
  // `session` a stand-in (useRuntimeSession), which is no row either. Retries are refused along with sends.
  const noRow = !session || detail.missing;
  // A runtime's list row can be here before its full detail: without `capabilities.send` yet it says nothing about sending, so
  // the box keeps taking words and Send waits until the detail answers (#11237), as it does with no row at all
  const hydrating = Boolean(session?.source_kind && session.capabilities?.send === undefined && !detail.ready && !detail.legacy && !detail.missing);
  // the hub could not say what this chat is (an older hub without the cached row, or a session it no longer has): the
  // notice above the chat says so, and an opening line under it would claim the chat started
  const noDetails = Boolean(detail.error) && (detail.missing || !session);
  const gone = detail.missing && !isMain;
  const refusedNoRow = (noRow && !isMain) || hydrating;
  const unknownGate = useRef({ isMain, gone: detail.missing });
  unknownGate.current = { isMain, gone: detail.missing };
  // A gone chat is read again a few times by itself (runtime-session.ts), and the notice above it keeps a Retry for after
  const noRowBlock = useMemo(() => (refusedNoRow ? { label: t(gone ? "chat-chatview.session_gone" : "chat-chatview.connecting") } : undefined), [refusedNoRow, gone]);
  // A Persona conversation without a title yet is "New chat", as All chats and the sidebar call it, not its folder's name (#1697)
  const named = session ?? archivedHere;
  // the hub has no such chat and nothing of it is here (no row, no cached lines, no message kept in this browser):
  // "Conversation not found" with the way back, never its id, nor cached content said to be readable
  const notFound = gone && !known && !archivedHere && !tr.loading && tr.items.length === 0 && outbox.length === 0;
  // a leader conversation is its direction, as its sidebar row says, not the hub's "MMDD | kind | prompt" title
  const directionName = useDirectionName(sid);
  const title = isMain ? mainTitle : notFound ? t("chat-chatview.not_found_title") : named ? (directionName ?? (assistant ? conversationTitle(named) : sessionTitle(named))) : assistant ? t("chat-chatview.new_chat") : t("chat-chatview.session");
  const canFork = Boolean(session?.can_fork);
  // "Rewind to here" (#2436): offered on your messages of a session in a terminal; off with a reason while it cannot. Live
  // (chat-actions.ts): it changes with every turn, and in `actions` it would render every message's actions again.
  const rewindOff = rewindBlock(session, working);
  const [rewindLive] = useState(() => liveValue(rewindOff));
  useLayoutEffect(() => rewindLive.set(rewindOff), [rewindLive, rewindOff]);
  const canRewind = canCompose && !assistant;
  // Legacy terminals retain both choices. Native rewind needs explicit file-restoration support.
  const rewindCode = session?.capabilities?.rewind_code ?? !session?.capabilities?.rewind;
  const rewindDeclared = session?.capabilities?.rewind === true;
  // Edit last message (#2945) and ↑ in an empty box (#4503), Slack's way: ↑, Esc twice or the safeguards card's button put
  // the last prompt in the box with an "Editing" bar, and the conversation-only rewind to before it runs when the edit is
  // sent (commitEdit), so starting or cancelling an edit changes nothing on the computer. One edit, of this conversation:
  // another one shown is not editing. `editingLast`: that rewind is running (the composer read-only).
  const [editingLast, setEditingLast] = useState(false);
  const editLastBusy = useRef(false);
  const [editTarget, setEditTarget] = useState<{ key: string; id: string } | null>(null);
  const editing = editTarget?.key === draftKey ? editTarget : null;
  const editNow = useRef(editing);
  editNow.current = editing;
  const editLastMessage = useCallback(() => {
    if (editLastBusy.current || !canRewind || rewindLive.get() || noRow || tr.loading || tr.error) return;
    const last = lastEditablePrompt(items.current);
    if (!last) return;
    setEditTarget({ key: draftKey, id: last.id });
    sendDraft(draftKey, { type: "replace", text: last.raw, edit: true });
  }, [canRewind, rewindLive, noRow, tr.loading, tr.error, draftKey]);
  const commitEdit = useCallback(async (): Promise<boolean | { clearDraft: string }> => {
    const target = editNow.current;
    if (!target) return true;
    if (editLastBusy.current) return false;
    // another message came after it: rewinding to before the old one would take that one too
    if (promptsOf(items.current).at(-1)?.id !== target.id) {
      setEditTarget(null);
      toast(t("rewind.edit_ended"), "info");
      return false;
    }
    const block = rewindLive.get();
    if (block) {
      toast(t("rewind.edit_wait", { reason: block }), "error");
      return false;
    }
    const at = rewindTarget(items.current, target.id);
    if (!at) {
      setEditTarget(null);
      toast(t("rewind.no_text"), "error");
      return false;
    }
    editLastBusy.current = true;
    setEditingLast(true);
    try {
      const res = await rewindSession(mid, sid, at, "conversation");
      setEditTarget((cur) => (cur === target ? null : cur));
      // Claude puts the old prompt back into its box a moment after the rewind answers, even when the node saw it empty
      // (#4605): wait for that, then the send clears exactly that prompt before typing (refused otherwise, kept in the box)
      await new Promise((r) => setTimeout(r, EDIT_SEND_SETTLE_MS));
      return { clearDraft: res.prompt || at.prompt };
    } catch (e) {
      toast(errorText(e), "error");
      return false;
    } finally {
      editLastBusy.current = false;
      setEditingLast(false);
    }
  }, [mid, sid, rewindLive]);
  // The last message changed while editing (one from the phone, a rewind from the menu): the words stay as a new message.
  const lastPromptId = useMemo(() => promptsOf(tr.items).at(-1)?.id, [tr.items]);
  useEffect(() => {
    if (!editing || editingLast || tr.loading || !tr.items.length || lastPromptId === editing.id) return;
    setEditTarget(null);
    toast(t("rewind.edit_ended"), "info");
  }, [editing, editingLast, tr.loading, tr.items.length, lastPromptId]);
  const editProp = useMemo(() => (editing ? { commit: commitEdit, cancel: () => setEditTarget(null) } : undefined), [editing, commitEdit]);
  const editableLast = useMemo(() => lastEditablePrompt(tr.items), [tr.items]);
  const onEditLastMessage = canRewind && !rewindOff && !noRow && !tr.loading && !tr.error && !editingLast && editableLast ? editLastMessage : undefined;
  // the chat a forward comes from, read when Forward is picked (lib/forward.ts): a push does not draw every message again
  const forwardChat = useRef<{ session?: Session; title: string }>({ title });
  forwardChat.current = { session: session ?? archivedHere ?? undefined, title };
  const forwardFrom = useCallback(
    (): ForwardChat => ({ mid, sid, assistant: Boolean(assistant), title: forwardChat.current.title, items: items.current, session: getSession(mid, sid) ?? forwardChat.current.session, machine: getState().machines.find((m) => m.id === mid)?.name }),
    [mid, sid, assistant],
  );
  // Forward to's Select messages… (#3838) hands the chat its picks (lib/message-pick.ts): while it holds them, each
  // message with a Forward has a checkbox and the bar takes the composer's place (Picking.tsx, which comes with Forward
  // to); they are dropped on Cancel or once they went (every host keys a ChatView by its chat: a new chat starts afresh)
  const [picker, setPicker] = useState<MessagePick>();
  const endPick = useCallback(() => setPicker(undefined), []);
  const actions = useMemo<ChatActions>(
    () => ({
      draftKey,
      title,
      canCompose,
      // `now`: the message menu's Select messages (#9469), Forward to's chunk starts picking with it, showing nothing
      forward: (p: ForwardPick, now?: boolean) => openForward({ from: { chat: forwardFrom(), picks: [p] }, select: { first: p, start: setPicker }, now }),
      pick: picker,
      speaker: assistant ? persona.name : "Agent",
      images: () => items.current.filter((i) => i.kind === "image"),
      fork: canFork ? (itemId: string) => void forkAt(mid, sid, itemId, Boolean(assistant), session?.space, session?.manager) : undefined,
      assistant: Boolean(assistant),
      rewind: canRewind ? { block: rewindLive, code: rewindCode, run: (messageId, text, restore) => rewindTo(mid, sid, draftKey, items.current, messageId, text, restore, rewindDeclared) } : undefined,
    }),
    [draftKey, title, canCompose, canFork, canRewind, rewindCode, rewindDeclared, rewindLive, mid, sid, assistant, persona.name, forwardFrom, picker],
  );
  // the assistant works on any computer, so its @ menu offers workspaces too
  const shownManager = useShownManager();
  const skillLauncher = usePickedLauncher(conversationKey(mid, sid));
  const assistantLauncher = useStore((s) => assistant ? s.assistant?.launcher : undefined);
  const skillManager = useStore((s) => session?.manager ?? s.machines.find((m) => m.id === mid)?.manager) ?? shownManager;
  const skillEnvironment = JSON.stringify([session?.launcher, session?.model_pick, session?.pending_switch, session?.source_kind, assistantLauncher]);
  const mention = useMemo(() => ({ cwd: session?.cwd, self: draftKey, files: () => chatFiles(items.current), workspaces: Boolean(assistant),
    skills: { manager: skillManager, session: sid, launcher: assistant ? skillLauncher : undefined, environment: skillEnvironment, agent: !assistant },
  }), [session?.cwd, skillManager, draftKey, assistant, sid, skillLauncher, skillEnvironment]);

  // a sent message stays as a "sending" bubble until the transcript has it (lib/echo.ts): an item the hub tied to it
  // (any kind, a notice too), or else a latest user message with its words; one that answered one answers no other
  const claimed = useRef(new Set<string>());
  useEffect(() => {
    // what a reload interrupted, once the hub's first page can tell what went in (a cached page cannot),
    // and not before this chat is known: a restored bubble's Retry posts without passing through deliver
    if (tr.firstRead && !refusedNoRow) restoreOutbox(mid, sid, pending.map((p) => p.id));
    observeMessageItems(mid, sid, tr.items);
    const users = [...segs.slice(-4).flatMap((seg) => (seg.user && !seg.user.injected ? [seg.user] : [])), ...taggedSeen(tr.items)];
    users.forEach((user) => acknowledgeLaunch(mid, sid, user));
    settleOutbox(mid, sid, users);
    if (!pending.length) return;
    const shown = echoed(pending, users, (p) => ({ id: p.id, text: p.msg.text, images: p.msg.images, at: p.at }), claimed.current);
    const drop = new Set([...shown].map((p) => p.id));
    const now = Date.now();
    for (const p of pending) {
      if (drop.has(p.id) || (!p.waits && !p.posting && state !== "working" && now - (p.acceptedAt ?? p.at) >= SENT_GRACE_MS)) {
        forgetOutgoing(outboxKey(mid, sid), p.id);
        if (drop.has(p.id)) settleMessageEcho(mid, sid, p.id);
      }
    }
    setPending((ps) => {
      // a sent one waits for its copy while the agent works and SENT_GRACE_MS after, as the outbox and the apps do: the
      // transcript can write it late (a long context compacted first, #1119); one held back for an ultracode change
      // waits as long as the turn does
      return ps.filter((p) => !drop.has(p.id) && (p.waits || p.posting || state === "working" || now - (p.acceptedAt ?? p.at) < SENT_GRACE_MS));
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [segs, tr.firstRead, refusedNoRow]);

  // A quiet transcript must not leave accepted bubbles forever. Reloads use the same stored deadline; local
  // composer bubbles retain their existing grace while the agent works. Never expire a POST still in flight.
  useEffect(() => {
    const expiring = state === "working" ? [] : pending.filter((p) => !p.posting && !p.waits);
    const deadlines = [...expiring, ...outbox.filter((e) => e.state === "sent")].map((e) => (e.acceptedAt ?? e.at) + SENT_GRACE_MS);
    if (!deadlines.length) return;
    const timer = setTimeout(() => {
      settleOutbox(mid, sid, []);
      const expired = new Set(expiring.filter((p) => Date.now() - (p.acceptedAt ?? p.at) >= SENT_GRACE_MS).map((p) => p.id));
      for (const id of expired) forgetOutgoing(outboxKey(mid, sid), id);
      if (expired.size) setPending((ps) => ps.filter((p) => !expired.has(p.id)));
    }, Math.max(0, Math.min(...deadlines) - Date.now()));
    return () => clearTimeout(timer);
  }, [mid, sid, pending, outbox, state]);

  // seen: on open, when the page comes back to the front, and whenever a turn ends while it is on screen (lib/store.ts
  // viewSession)
  useEffect(() => viewSession(mid, sid), [mid, sid]);

  // what came in over the WebSocket after the chat subscribed: the agent's words among it fade in (contract §3); the
  // first page, older pages and unfolded ranges come over HTTP and do not. Outside React (fresh-words.ts): a new reply
  // renders its own words, not every exchange; added before the transcript renders it, so its words mount fading.
  useEffect(() => {
    resetFreshWords(mid, sid);
    const off = listenSession(mid, sid, { items: (e) => addFreshWords(mid, sid, freshWords(items.current, e.items ?? [])) });
    return () => {
      off();
      resetFreshWords(mid, sid);
    };
  }, [mid, sid]);

  // ── scrolling (lib/disclosure.ts "following the end"): the end stays in view until the reader scrolls away from it;
  // older pages landing above keep the view still ──
  const scroller = useRef<HTMLDivElement>(null);
  // the element itself, for the effects that listen on it: once the session arrives the page's frame can change (a
  // terminal to switch to) and React then draws the chat in another element, so listeners left on the first one stop
  // following the end (an agent's page opened by its address did not keep its live text in view, #1309)
  const [scrollEl, setScrollEl] = useState<HTMLDivElement | null>(null);
  useChatScrollbar(scrollEl);
  const scrollRef = useCallback((el: HTMLDivElement | null) => {
    scroller.current = el;
    if (el) setScrollEl(el);
  }, []);
  const atEnd = useRef(true);
  // the jump's smooth scroll running: its first frames, a few px off the end, do not start following again
  const jumping = useRef(0);
  const [away, setAway] = useState(false);
  // A row a jump brought into view (the rail, a search hit, a comment): it stays `at` px under the view's top while what
  // is around it settles (pictures coming in, exchanges drawn for the first time). Held while the view moves and a
  // moment after; the reader scrolling, or the view going to the end, lets it go.
  const landing = useRef<{ row: Element | Range; at: number; quiet: number } | null>(null);
  const holdLanding = useCallback((ms: number) => {
    const l = landing.current;
    if (!l) return;
    clearTimeout(l.quiet);
    l.quiet = window.setTimeout(() => (landing.current = null), ms);
  }, []);
  const land = useCallback((row: Element | Range, at: number) => {
    clearTimeout(landing.current?.quiet);
    landing.current = { row, at, quiet: 0 };
    holdLanding(1200);
  }, [holdLanding]);
  // a smooth jump on its way (glide.ts): it reads where to go each frame, and the ResizeObserver leaves the view to it
  const gliding = useRef<(() => void) | null>(null);
  const stopGlide = useCallback(() => {
    gliding.current?.();
    gliding.current = null;
  }, []);
  useEffect(() => stopGlide, [stopGlide]);
  const glideTo = useCallback((aim: () => number) => {
    const sc = scroller.current;
    stopGlide();
    if (!sc) return;
    if (scrollBehavior() === "auto") sc.scrollTop = aim();
    else gliding.current = glide(sc, aim, () => (gliding.current = null));
  }, [stopGlide]);
  // something came in since the reader left the end: the pill says "New messages"
  const [newSince, setNewSince] = useState(false);
  const follow = useCallback((on: boolean) => {
    atEnd.current = on;
    setAway(!on);
    if (on) {
      setNewSince(false);
      landing.current = null;
    }
  }, []);
  const lastTop = useRef(0);
  const dragging = useRef(false);
  const firstLiveSeq = earlier.liveItems[0]?.seq ?? Infinity;
  const liveFront = useRef(firstLiveSeq);
  liveFront.current = firstLiveSeq;
  const historyScope = `${mid}/${sid}/${tr.history ?? ""}`;
  const historyScopeNow = useRef(historyScope);
  historyScopeNow.current = historyScope;
  type HistoryScrollAnchor = {
    el: HTMLDivElement; scope: string; front: ReturnType<typeof visibleHistoryFront>;
    h: number; top: number; readers: number; pages: Set<symbol>;
  };
  const before = useRef<HistoryScrollAnchor | null>(null);
  const projectedPages = useRef<(symbol | undefined)[]>([]);
  const loadWithScrollAnchor = useCallback(async (load: () => Promise<symbol | undefined>) => {
    const el = scroller.current;
    if (!el) { await load(); return; }
    // Scroll, search and the rail may all wait for the same page. A join can establish the anchor
    // if the read had none, and a caller returning early must not clear it for the others.
    if (before.current?.el !== el || before.current.scope !== historyScopeNow.current) {
      before.current = {
        el, scope: historyScopeNow.current, front: visibleHistoryFront(el, liveFront.current),
        h: el.scrollHeight, top: el.scrollTop, readers: 0, pages: new Set(),
      };
    }
    const saved = before.current;
    saved.readers++;
    try {
      const page = await load();
      if (page && !projectedPages.current.includes(page)) saved.pages.add(page);
    } finally {
      saved.readers--;
      // A successful read can finish before its rows are visible. Only a no-op can release the anchor here.
      if (!saved.readers && !saved.pages.size && before.current === saved) before.current = null;
    }
  }, []);
  const first = useRef(true);
  // Back from an Agent's page this conversation handed work to (#5096): where the chat was, kept on its history entry
  // (lib/handoff.ts); it opens there instead of at the end, once, and goes to the end if that line does not come in time.
  // `back`: this chat opened at a kept place (then the place handling, not the opening below, sets following).
  const kept = useRef<{ key: string; place?: ChatPlace; back?: boolean }>({ key: "" });
  if (kept.current.key !== `${mid}/${sid}`) {
    const place = assistant ? chatPlaceIn() : undefined;
    const back = Boolean(place && !place.end && place.line);
    kept.current = { key: `${mid}/${sid}`, place: back ? place : undefined, back };
  }
  useEffect(() => {
    if (assistant) dropChatPlace();
  }, [assistant, mid, sid]);

  useEffect(() => {
    lastTop.current = 0;
    before.current = null;
    if (kept.current.back) {
      if (kept.current.place) follow(false); // still waiting for its line: not pulled to the end meanwhile
      return;
    }
    first.current = true;
    follow(true);
  }, [mid, sid, follow]);

  useEffect(() => {
    if (!kept.current.place) return;
    const late = window.setTimeout(() => {
      if (!kept.current.place) return;
      kept.current.place = undefined;
      follow(true);
      const el = scroller.current;
      if (el) el.scrollTop = el.scrollHeight;
    }, GIVE_UP_MS * 2);
    return () => clearTimeout(late);
  }, [mid, sid, follow]);

  const lastId = tr.items[tr.items.length - 1]?.id;
  useEffect(() => {
    if (lastId && !atEnd.current) setNewSince(true);
  }, [lastId]);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const { projectedLivePage, projectedOtherPage } = earlier;
    projectedPages.current = [projectedLivePage, projectedOtherPage];
    let saved = before.current;
    if (saved && (saved.el !== el || saved.scope !== historyScope)) {
      before.current = saved = null;
    }
    if (saved) {
      const front = visibleHistoryFront(el, firstLiveSeq);
      if (front.live < saved.front.live || front.stretch < saved.front.stretch ||
        (front.stretch === saved.front.stretch && front.stretchSeq < saved.front.stretchSeq)) {
        el.scrollTop = saved.top + (el.scrollHeight - saved.h);
        before.current = null;
        return;
      }
      const livePageCommitted = projectedLivePage !== undefined && saved.pages.delete(projectedLivePage);
      const otherPageCommitted = projectedOtherPage !== undefined && saved.pages.delete(projectedOtherPage);
      // The committed page can be wholly outside the rendered stretch. In that case there is no
      // prepend to offset, but the page still settles the anchor.
      if ((livePageCommitted || otherPageCommitted) && !saved.readers && !saved.pages.size) before.current = saved = null;
    }
    const place = kept.current.place;
    // the line it was at, `offset` under the view's top again; until that line comes in, the chat opens at its end as
    // it always does (and goes to the line once it is there)
    const row = place && el.querySelector(`[data-line="${CSS.escape(place.line!)}"]`)?.firstElementChild;
    if (place && row) {
      before.current = null;
      kept.current.place = undefined;
      el.scrollTop += row.getBoundingClientRect().top - el.getBoundingClientRect().top - place.offset;
      first.current = false;
      follow(false);
      land(row, place.offset);
      return;
    }
    if (atEnd.current || first.current) {
      el.scrollTop = el.scrollHeight;
      if (tr.items.length) first.current = false;
    }
    // Live updates below the viewport can change the height while a history page is held.
    // Record that change without spending the anchor needed for the eventual prepend.
    if (saved) {
      saved.h = el.scrollHeight;
      saved.top = el.scrollTop;
    }
  }, [tr.items, earlier.stretches, earlier.projectedLivePage, earlier.projectedOtherPage, pending, outbox, tr.streaming, interactions.length, historyScope, firstLiveSeq]);

  // A card that unfolds (#1124): following holds off for UNFOLD_PAUSE while the clicked row stays where it was on
  // screen (anchoredScroll), then picks up again if the end is still near; a long card opened at the end stays put to
  // be read, and the pill brings the end back. Held until the sizes stop changing for a moment, or the reader scrolls.
  const anchor = useRef<(ScrollAnchor & { el: Element; quiet: number }) | null>(null);
  useEffect(() => {
    const sc = scroller.current;
    if (!sc) return;
    let resume = 0;
    // the reader scrolled (wheel, finger, keys) while an unfold held following off: the pause does not pull them back
    let moved = false;
    const drop = () => {
      if (anchor.current) clearTimeout(anchor.current.quiet);
      anchor.current = null;
    };
    const readerMoves = () => {
      moved = true;
      drop();
      stopGlide();
      landing.current = null;
    };
    const onClick = (e: MouseEvent) => {
      const el = e.target instanceof Element ? e.target.closest("button[aria-expanded]") : null;
      if (!el || !sc.contains(el)) return;
      drop();
      clearTimeout(resume);
      const was = atEnd.current;
      atEnd.current = false;
      moved = false;
      // A disclosure below its contents anchors the card, not the button that moves down when it opens.
      const row = el.closest("[data-disclosure-anchor]") ?? el;
      anchor.current = { el: row, edge: "top", at: row.getBoundingClientRect().top, follow: false, quiet: window.setTimeout(drop, 1000) };
      resume = window.setTimeout(() => {
        if (!was) return;
        if (!followsAfterUnfold({ wasAtEnd: was, readerMoved: moved, gap: sc.scrollHeight - sc.scrollTop - sc.clientHeight })) return follow(false);
        drop();
        follow(true);
        sc.scrollTop = sc.scrollHeight;
      }, UNFOLD_PAUSE);
    };
    sc.addEventListener("click", onClick, true);
    for (const ev of ["wheel", "touchstart", "keydown", "pointerdown"]) sc.addEventListener(ev, readerMoves, { passive: true });
    return () => {
      drop();
      clearTimeout(resume);
      sc.removeEventListener("click", onClick, true);
      for (const ev of ["wheel", "touchstart", "keydown", "pointerdown"]) sc.removeEventListener(ev, readerMoves);
    };
  }, [follow, scrollEl, stopGlide]);

  // content that grows by itself (images loading, a live group's steps) keeps the end in view; a held row stays put.
  // A ResizeObserver runs after layout and before paint, so the offset moves in the same frame as the height. The view
  // itself is watched too: what sits under it (the composer growing, the task dock, a question card) makes it shorter
  // without the content changing, and at the end the newest line stays in view. So are the exchanges that skip drawing
  // off screen (watchSegment): one drawn for the first time above the view swaps its guessed height for its own, and
  // the view moves by as much, so what is on screen stays (offscreen.ts).
  const heights = useRef(new WeakMap<Element, number>());
  const resized = useRef<ResizeObserver | null>(null);
  const sizes = useCallback(() => (resized.current ??= new ResizeObserver((entries) => {
      const sc = scroller.current;
      if (!sc) return;
      const changes = segmentChanges(entries, heights.current);
      if (gliding.current) return;
      const viewTop = sc.getBoundingClientRect().top;
      const l = landing.current;
      if (l && (l.row instanceof Range ? l.row.startContainer : l.row).isConnected) {
        const off = l.row.getBoundingClientRect().top - viewTop - l.at;
        if (Math.abs(off) >= 0.5) {
          sc.scrollTop += off;
          holdLanding(1200);
        }
        return;
      }
      const a = anchor.current;
      if (a && a.el.isConnected) {
        const r = a.el.getBoundingClientRect();
        const next = anchoredScroll(a, {
          scrollTop: sc.scrollTop, scrollHeight: sc.scrollHeight, clientHeight: sc.clientHeight,
          viewTop: sc.getBoundingClientRect().top, edgeAt: r.top, elTop: r.top,
        });
        if (Math.abs(next.scrollTop - sc.scrollTop) >= 0.5) sc.scrollTop = next.scrollTop;
        clearTimeout(a.quiet);
        a.quiet = window.setTimeout(() => (anchor.current = null), 600);
        return;
      }
      if (atEnd.current) sc.scrollTop = sc.scrollHeight;
      else {
        const shift = shiftAbove(changes, viewTop);
        if (shift) sc.scrollTop += shift;
      }
    })), [holdLanding]);
  useEffect(() => {
    const view = scroller.current;
    const el = view?.firstElementChild;
    if (!view || !el) return;
    const ro = sizes();
    ro.observe(el);
    ro.observe(view);
    return () => {
      ro.unobserve(el);
      ro.unobserve(view);
    };
  }, [scrollEl, sizes]);
  // Its height as this commit's layout effects see it is where its changes count from: an earlier page landing keeps
  // the view still by that layout (the guessed heights), and the browser may draw the new exchanges later in the same
  // frame, before the observer's first look at them. Read in the layout effects, where the chat reads its own height
  // anyway; nothing in between writes to the page, so it is one layout for all of them.
  const watchSegment = useCallback((section: HTMLElement) => {
    const ro = sizes();
    const h = section.getBoundingClientRect().height;
    if (h > 0) heights.current.set(section, h);
    ro.observe(section);
    return () => ro.unobserve(section);
  }, [sizes]);

  // the reader leaving the end: the wheel or a trackpad going up, a finger pulling the list down, PageUp / ↑ / Home,
  // the scroll bar dragged up (onScroll)
  useEffect(() => {
    const sc = scroller.current;
    if (!sc) return;
    const size = () => ({ scrollHeight: sc.scrollHeight, clientHeight: sc.clientHeight });
    const leave = () => follow(false);
    // (checked before sizes are read: no layout read per wheel or touch event while away from the end)
    // a wheel or a finger scrolling a folded message (fold-scroll.tsx) stays in it: the chat does not move
    const wheel = (e: WheelEvent) => atEnd.current && !scrollsAFold(e.target, sc, e.deltaY) && leavesOnWheel({ deltaX: e.deltaX, deltaY: e.deltaY, ...size() }) && leave();
    let startY: number | null = null;
    const touchStart = (e: TouchEvent) => (startY = e.touches[0]?.clientY ?? null);
    const touchMove = (e: TouchEvent) => {
      const y = e.touches[0]?.clientY;
      if (atEnd.current && startY !== null && y !== undefined && !scrollsAFold(e.target, sc, startY - y) && leavesOnTouch({ startY, y, ...size() })) leave();
    };
    const key = (e: KeyboardEvent) => {
      const t = e.target instanceof Element ? e.target : null;
      if (t?.closest("input, textarea, [contenteditable=true]")) return;
      if (atEnd.current && leavesOnKey({ key: e.key, ...size() })) leave();
    };
    const down = (e: PointerEvent) => (dragging.current = isScrollbarPress({ targetIsView: e.target === sc }));
    const up = () => (dragging.current = false);
    sc.addEventListener("wheel", wheel, { passive: true });
    sc.addEventListener("touchstart", touchStart, { passive: true });
    sc.addEventListener("touchmove", touchMove, { passive: true });
    sc.addEventListener("pointerdown", down);
    window.addEventListener("pointerup", up);
    window.addEventListener("keydown", key);
    return () => {
      sc.removeEventListener("wheel", wheel);
      sc.removeEventListener("touchstart", touchStart);
      sc.removeEventListener("touchmove", touchMove);
      sc.removeEventListener("pointerdown", down);
      window.removeEventListener("pointerup", up);
      window.removeEventListener("keydown", key);
    };
  }, [follow, scrollEl]);

  const loadEarlierNearTop = () => {
    const el = scroller.current;
    if (!el || el.scrollTop >= 300 || !earlier.hasMore || earlier.loadingEarlier || first.current) return;
    void loadWithScrollAnchor(earlier.loadEarlier);
  };

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    const saved = before.current;
    if (saved?.el === el && saved.scope === historyScope) saved.top = el.scrollTop;
    const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
    const delta = el.scrollTop - lastTop.current;
    lastTop.current = el.scrollTop;
    holdLanding(600);
    if (atEnd.current) {
      if (leavesOnScrollbarDrag({ dragging: dragging.current, delta })) follow(false);
    } else if (!anchor.current && !jumping.current && followsAgain({ gap, delta })) follow(true);
    if (!first.current && !atEnd.current && el.scrollTop < 300) revealLoadedInputs();
    loadEarlierNearTop();
  };

  // Reaching the top only loads older pages through a scroll event, and a view that cannot scroll (a page shorter than
  // it: the main conversation's newest rounds are often two folded background rounds) or a page that landed without
  // moving it sends none. Each change asks again, so older pages load until the top is out of reach.
  useEffect(() => {
    loadEarlierNearTop();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tr.items, earlier.stretches, earlier.hasMore, earlier.loadingEarlier, earlier.loadEarlier, earlier.due]);

  const toEnd = () => {
    const el = scroller.current;
    if (!el) return;
    follow(true);
    glideTo(() => el.scrollHeight - el.clientHeight);
  };

  // ── sending ──
  // true: it went, or it stays in the chat as a failed bubble (`failed`); false: it never went and nothing shows it
  const deliver = useCallback(
    async (out: Outgoing, id: string, clearDraft?: string, attempt = 0, keepInBox = false): Promise<boolean> => {
      // the main conversation's setup gate holds every path that sends, not only the composer (4.4)
      if (blockedRef.current) {
        blockedRef.current.onClick?.();
        return false;
      }
      const row = getSession(mid, sid);
      if (row && !unknownGate.current.gone ? !sessionCan(row, "send") : !unknownGate.current.isMain) {
        toast(isStoppedAgent(row) ? t("chat-chatview.stopped_line") : sourceSubtitle(session) || t("chat-chatview.session_unavailable"), "error");
        return false;
      }
      const flight = beginMessageSend(mid, sid, id, out.text);
      // a sending bubble shows picked contexts as quote cards, like the transcript will (not the raw tags)
      const p: Pending = { id, at: Date.now(), posting: true, msg: { id: `pending:${id}`, ...parseUserText(out.text), images: out.images.map((image, i) => ({ id: `pending:${id}#${i}`, seq: 0, role: "user", kind: "image", image })) } };
      // a new try: an earlier failure's card no longer applies (its bubble stays, with Retry)
      setSendFailure(null);
      noteWritten(id, p.at);
      setPending((ps) => [...ps.filter((x) => x.id !== id), p]);
      // Quote replies keep an earlier reader's place; at the end, every own message still follows (#10346).
      if (!out.quoteReply || atEnd.current) follow(true);
      const key = outboxKey(mid, sid);
      const launcher = assistant ? pickedLauncher(conversationKey(mid, sid)) : undefined;
      const model = assistant ? pickedModel(conversationKey(mid, sid)) : undefined;
      keepOutgoing(key, { id, text: out.text, images: out.images, at: p.at, launcher, model });
      try {
        // the bubble stays as sending while a gap (a hub restart) is waited out; the id is the op id too
        const reply = await inSendOrder(key, () => apiOp<({ session?: Session; waits_on_computer?: boolean } & DeliveryReply & KeptReply) | undefined>(`${sessionPath(mid, sid)}/messages`, {
          ...flight.request,
          method: "POST",
          opId: id,
          // a switch to another launcher the user picked for this Agent goes with their own message from here, and only
          // with it (#5035): the agent restarts there first
          json: { text: out.text, images: out.images.length ? out.images : undefined, client_msg_id: id, clear_draft: clearDraft, launcher, model,
            apply_switch: !assistant && row?.pending_switch ? true : undefined },
        }));
        if (!flight.current()) return true;
        assertDelivered(reply); // a bare operation receipt is no delivery: in doubt, with Check (#10194)
        if (isKept(reply)) {
          // it did not go in, and the assistant keeps it and sends it by itself: the row's bubble shows it from now on
          flight.finish("failed");
          forgetOutgoing(key, id);
          setPending((ps) => ps.filter((x) => x.id !== id));
          showKept(mid, sid, reply?.undelivered);
          return true;
        }
        flight.finish("delivered");
        const acceptedAt = Date.now();
        acceptOutgoing(key, id, acceptedAt);
        setPending((ps) => ps.map((x) => (x.id === id ? { ...x, posting: false, acceptedAt } : x)));
        // a terminal Agent waiting on a question at the computer reads it only after that is answered (#5890)
        if (reply?.waits_on_computer) toast(t("lib-outbox.waits_on_computer"), "info");
        // your own message went through a Claude's inbox although Persona's plugin is there: it may not count as approval
        const relayed = relayNotice(reply);
        if (relayed) toast(relayed, "info");
        // a Codex's first message: the answer is the thread it began, where the conversation goes on (lib/codex-waiting.ts)
        const began = reply?.session;
        if (began?.session_id && began.session_id !== sid) {
          upsertSession({ ...began, machine_id: mid });
          if (inPage) navigate({ name: "session", mid, sid: began.session_id }, true);
        }
        return true;
      } catch (e) {
        if (!flight.current() || flight.echoed()) return true;
        flight.finish("failed");
        // An ultracode change waits for the turn (docs/protocol/protocol.md "Claude Code ultracode"): the node holds messages back
        // so the next turn cannot overtake it. The bubble stays and says so, the box is free (and its stop button there),
        // and the message goes again once the change is through, with a new id (the hub keeps its answer to this one).
        if (!assistant && isUltracodeQueued(e) && attempt < ULTRACODE_SEND_TRIES) {
          setPending((ps) => ps.map((x) => (x.id === id ? { ...x, waits: true } : x)));
          void ultracodeQueueDone(mid, sid).then((through) => {
            setPending((ps) => ps.filter((x) => x.id !== id));
            if (through) {
              forgetOutgoing(key, id);
              void deliver(out, uuid(), clearDraft, attempt + 1);
            } else failed(e, out, p);
          });
          return true;
        }
        setPending((ps) => ps.filter((x) => x.id !== id));
        // an edit's send the node did not take (#4605): nothing went, so the words go back into the box with the node's
        // reason (a draft in Claude's box…), not into a failed bubble whose Retry would send them without the rewind's care.
        // So does a message that was to carry out a switch to another launcher first (#5035, `switch_*`: the launcher
        // went, the agent did not come up there…): the switch is dropped, and sending again sends it where the agent runs
        if ((keepInBox || switchRefused(e)) && e instanceof ApiError && e.body.delivered === "none") {
          forgetOutgoing(key, id);
          toast(errorText(e), "error");
          return false;
        }
        failed(e, out, p);
        return true;
      } finally {
        flight.finish(); // release the request even if an early echo made its late HTTP error irrelevant
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `failed` reads only stable setters and these
    [mid, sid, session, assistant, follow, inPage],
  );

  /** Keeps a message that did not go as a failed bubble where it was sent, with why, "Retry" and "Delete", the box empty
   *  (lib/outbox.ts keepFailed; as in the apps, #1122). The bubble says why (lib/outbox.ts failureText), so no toast
   *  repeats it; what needs you more gets more: the computer's draft (a dialog that can clear it), the main
   *  conversation's setup (a card above the composer with what to do). */
  function failed(e: unknown, out: Outgoing, p: Pending) {
    keepFailed(mid, sid, { id: p.id, text: out.text, images: out.images, at: p.at, launcher: assistant ? pickedLauncher(conversationKey(mid, sid)) : undefined, model: assistant ? pickedModel(conversationKey(mid, sid)) : undefined }, e);
    if (e instanceof ApiError && e.code === "input_draft") {
      setDraftRefusal({ id: p.id, draft: String(e.body.draft ?? ""), clearable: typeof e.body.clearable === "string" ? e.body.clearable : undefined });
    } else if (assistant && !(e instanceof ApiError && (e.code === "dialog_open" || e.body.delivered === "unknown"))) {
      // the card is for a refusal with something to set up first (a code or an action, with its button); a failure with
      // nothing more to it (the hub away, a server error) is the bubble's alone, said once with its one Retry
      const failure = codedFailure(e);
      if (failure.code || knownAction(failure.action)) setSendFailure({ chat: `${mid}/${sid}`, failure, entry: p.id });
      void refreshReadiness({ force: true });
    }
  }

  const onSend = async (out: Outgoing): Promise<boolean> => {
    // /clear in the main conversation clears its context in place, it is not words for Persona (#5095); an edit of the
    // last message stays an edit (its rewind already ran)
    if (clears && !out.edit && isClearCommand(out.text, out.images.length)) return clearMainContext(sid);
    // /workflows opens the Agent's Background tasks panel at its latest workflow, as cindy's does (#5421): not words for it.
    // Not in a chat drawn inside a panel, whose page shows another session
    if (!assistant && !embedded && !out.edit && isWorkflowsCommand(out.text, out.images.length)) {
      openBackgroundPanel(mid, sid, latestWorkflowRun(session));
      return true;
    }
    // Routed to a task ("Tell "xx""): straight to its agent, not to Persona (docs/assistant/unified-entry.md "Routing").
    if (assistant && out.images.length && getFeedState().routes[sid]) {
      toast(t("chat-chatview.route_no_images"), "error");
      return false;
    }
    if (assistant && !out.images.length && out.text.trim() && getFeedState().routes[sid]) {
      if (splitSkillDraft(out.text).skills.length) {
        toast(t("chat-chatview.skills_route"), "error");
        return false;
      }
      try {
        // in turn: the route is used up by the first message that takes it, and the next one must see that
        if (await inSendOrder(outboxKey(mid, sid), () => sendRouted(sid, out.text))) return true;
      } catch (e) {
        toast(t("chat-chatview.not_delivered", { error: errorText(e) }), "error");
        return false;
      }
    }
    return deliver(out, uuid(), out.edit?.clearDraft, 0, Boolean(out.edit));
  };
  // the composer is memoized: one function, running the latest onSend
  const sendNow = useRef(onSend);
  sendNow.current = onSend;
  const send = useCallback((out: Outgoing) => sendNow.current(out), []);
  // The assistant's tasks (docs/assistant/unified-entry.md): the conversation's feed, and the main one's "Today" card.
  const taskFeed = useFeed((s) => (assistant ? (s.feeds[sid] ?? NO_FEED) : NO_FEED));
  useEffect(() => {
    if (!assistant) return;
    void loadFeed(sid);
    if (sid === mainConversation) void loadToday(sid);
  }, [assistant, sid, mainConversation]);
  // Its urgent calls (docs/assistant/urgent-call.md), each a row at its time among the feed: read by a module of their own.
  const [calls, setCalls] = useState<FeedItem[]>(NO_FEED);
  useEffect(() => {
    if (!assistant) return;
    let stop: (() => void) | undefined;
    let gone = false;
    void import("../assistant/urgent-calls").then((m) => {
      if (!gone) stop = m.watchCalls(sid, setCalls);
    });
    return () => {
      gone = true;
      stop?.();
      setCalls(NO_FEED);
    };
  }, [assistant, sid]);
  const feed = useMemo(() => (calls.length ? [...taskFeed, ...calls].sort((a, b) => Date.parse(a.at) - Date.parse(b.at)) : taskFeed), [taskFeed, calls]);
  // The "Today" card's actions say something to Persona here.
  useEffect(() => {
    if (!assistant) return;
    const say = (e: Event) => {
      const d = (e as CustomEvent<{ mid: string; sid: string; text: string }>).detail;
      if (d.mid === mid && d.sid === sid) void deliver({ text: d.text, images: [] }, uuid());
    };
    window.addEventListener("persona:say", say);
    return () => window.removeEventListener("persona:say", say);
  }, [assistant, mid, sid, deliver]);

  const [stopping, setStopping] = useState(false);
  const stopInFlight = useRef(false); // the press that comes before `stopping` has drawn
  const stopTimer = useRef(0); // an earlier stop's wait must not end a later stop's block
  const stopDone = useCallback(() => {
    clearTimeout(stopTimer.current);
    stopInFlight.current = false;
    setStopping(false);
  }, []);
  const interrupt = useCallback(async () => {
    if (stopInFlight.current || !sessionCan(getSession(mid, sid), "interrupt")) return;
    stopInFlight.current = true;
    setStopping(true);
    const waited = awaitingNow.current;
    try {
      // the hub gives the computer 90 s: past that the link is dead, and a request that never ends must not hold the block
      const r = await apiOp<{ state?: string }>(`${sessionPath(mid, sid)}/interrupt`, { method: "POST", timeout: pastHub(90_000) });
      // a repeat after a gap can read as a success: the hub lost track of the first try, so nothing says it stopped
      if (r.state === "unknown") throw new Error(t("lib-new-agent.outcome_unknown"));
      toast(t("chat-chatview.interrupted"));
      stopMessageFeedback(mid, sid);
      if (waited) setStoppedTurn(waited);
      stopTimer.current = window.setTimeout(stopDone, STOP_SETTLE_MS);
    } catch (e) {
      toast(t("chat-chatview.interrupt_failed", { error: errorText(e) }), "error");
      stopDone();
    }
  }, [mid, sid, stopDone]);
  // Ctrl/⌘+Enter in the box: the queue panel's Send now (QueuePanel.tsx), after the message is queued
  const sendQueueNow = useCallback(async () => {
    try {
      await api(`${sessionPath(mid, sid)}/queue/send-now`, { method: "POST", json: {} });
    } catch (e) {
      toast(t("chat-chatview.send_now_failed", { error: errorText(e) }), "error");
    }
  }, [mid, sid]);
  // the turn ended, and not necessarily by this stop
  useEffect(() => {
    if (state !== "working") stopDone();
  }, [state, stopDone]);

  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      // in the terminal view Esc belongs to the program in the terminal; a message still waiting for its turn to start can be
      // stopped as a running turn can, as the Interrupt button shows for both
      // a worker's conversation in a Team panel, and an Agent's in a task's or an Agent's side panel, leave Esc to the
      // panel (it closes it) and to the page beside it
      if (embedded || inPanel) return;
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (e.target instanceof Element && e.target.closest("[data-session-overview]")) return;
      if ((state === "working" || awaiting) && view !== "term" && !document.querySelector("[role=dialog],[role=menu]")) {
        e.preventDefault();
        void interrupt();
      }
    };
    addEventListener("keydown", key);
    return () => removeEventListener("keydown", key);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state, awaiting, session, view, embedded, inPanel]);

  // Once the hub says the turn is over, four kinds of background work draw the last exchange finished (its work folded,
  // any run's card inside it) instead of keeping the turn's "Working…" clock going: first a workflow (#3434), then
  // background subagents (#5030), commands that only wait (#6045), and finally a real background command or Monitor
  // (#7341). A message of yours on its way, streaming, or an answer being written is a turn again.
  const backgroundState = backgroundTurn(session, subagents, { streaming, outgoing: outgoing > 0, awaiting });
  const backgroundOnly = backgroundState !== null;
  const lastLive = working && !backgroundOnly && (!tr.loading || tr.items.length > 0);
  // the main conversation's readiness: the card for what is missing, and the send button saying it (4.4)
  const gate = useReadinessGate(isMain);
  blockedRef.current = gate.blocked;
  const failedEntry = sendFailure?.chat === `${mid}/${sid}` ? outbox.find((e) => e.id === sendFailure.entry && e.state === "failed") : undefined;
  const failure = failedEntry ? sendFailure!.failure : null;
  // the hub's word on the last turn's model error, for its card (5.2 turn_error_code)
  const turnFix = useAssistantTurnFix(session, Boolean(assistant));
  // A subscription usage limit: its error card says when it resumes. The clock drops the wait once the grace is past.
  useNow(30_000, session?.turn_error_kind === "usage_limit");
  const limitLine = session ? usageLimitLabel(session) : undefined;
  // a line that comes out the same as last time keeps its object, so a push re-renders only the lines it changed
  // (lib/stable-lines.ts); the task index likewise, or every row reading the context would render again
  const lastLines = useRef<ChatLine[] | undefined>(undefined);
  // the main conversation and the leader conversations fold rounds their user did not start: another conversation's
  // relay, a scheduled patrol, a follow-up (docs/assistant/assistant.md "Background rounds", #6595). On a cold load the
  // AssistantInfo has not arrived but the cached main id has: keep the main chat folded (the absent switch means on).
  const foldBackground = useStore((s) =>
    foldsBackgroundRounds(sid, s.assistant ?? (mainCache ? { main: mainCache.scope } : undefined)));
  const lines = useMemo(
    () => stabilizeLines(lastLines.current, chatLines(earlier.liveItems, { live: lastLive && !outgoing, background: foldBackground, main: isMain })),
    [earlier.liveItems, lastLive, outgoing, foldBackground, isMain],
  );
  lastLines.current = lines;
  // a DayLine over the first line of each new calendar day (#8160); day-only keys keep them stable as pages and
  // midnight arrivals land. Days the earlier stretches already open with a divider are not repeated here.
  const dayTaken = useMemo(() => new Set(earlier.stretchDays), [earlier.stretchDays]);
  const dayRows = useMemo(
    () => withDayDividers(lines).filter((row) => {
      const d = dayRowKey(row);
      return d === undefined || !dayTaken.has(d);
    }),
    [lines, dayTaken],
  );
  // the permission mode changes the hub recorded (#10297), each among the rows at its time, read when the chat opens and
  // when its mode or a waiting restart changes, nothing else; their code and words load with an Agent's chat. While older history is
  // still to load (or sits in an earlier engine's stretch), a change from before the first loaded item waits for it.
  const [modes, setModes] = useState<ModeChanges>();
  const modeKey = `${session?.permission_mode}|${session?.bypass_pending}`;
  useEffect(() => {
    if (assistant) return;
    const ctl = new AbortController();
    void import("./mode-changes").then((kit) => kit.watchModeChanges(mid, sid, modeKey, ctl.signal, kit, setModes), () => {});
    return () => ctl.abort();
  }, [mid, sid, assistant, modeKey]);
  const chatRows: ChatRowWithModes[] = useMemo(
    () => (modes?.scope === `${mid}/${sid}` ? modes.kit.withModeChanges(dayRows, modes.changes, tr.hasMore || earlier.stretches.length > 0 ? earlier.liveItems : undefined) : dayRows),
    [modes, mid, sid, dayRows, tr.hasMore, earlier.stretches.length, earlier.liveItems],
  );
  // the last exchange while the chat is at work, even while a message of yours on its way draws it finished (live needs
  // !outgoing above): its words get no reply bar until the work is over (#5314, SegmentView `busy`)
  const busySegment = useMemo(() => (lastLive ? lines.findLast((line) => line.kind === "segment")?.id : undefined), [lastLive, lines]);
  // the last two exchanges are always drawn (the one going on, the one just done); those before them skip drawing while
  // off screen (SegmentView `offscreen`). Not on a touch-only screen: a script moving the view (keeping it still as one
  // is drawn) can stop a fling there.
  const touch = useMedia(NO_HOVER);
  const drawnLast = useMemo(() => new Set(lines.filter((l) => l.kind === "segment").slice(-2).map((l) => l.id)), [lines]);
  const offscreen = (line: ChatLine) => (!touch && line.kind === "segment" && !line.live && !drawnLast.has(line.id) ? watchSegment : undefined);
  const linesNow = useRef(lines);
  linesNow.current = lines;
  const limitOn = limitLine ? lines.reduce((id, line) => (line.kind === "apiError" ? line.id : id), "") : "";
  // failed messages where they were sent: before the first message written after them (lib/outbox.ts placeFailed)
  const placed = useMemo(() => placeFailed(lines, outbox), [lines, outbox]);
  const failedBefore = (id: string) => {
    const list = placed.before.get(id);
    return list ? [<OutboxBubbles key={`failed:${id}`} mid={mid} sid={sid} entries={list} canRetry={!refusedNoRow} />] : [];
  };
  const lastTasks = useRef<TaskIndex | undefined>(undefined);
  const tasks = useMemo(() => stableValue(lastTasks.current, buildTaskIndex(tr.items)), [tr.items]);
  lastTasks.current = tasks;
  // Only lightweight positions are fetched for navigation; older transcript bodies wait for a selection.
  const { seq: revealedSeq, reveal: revealLoadedInputs } = useRevealedInputSeq(`${mid}/${sid}/${tr.history ?? ""}`, tr.loading ? undefined : tr.items[0]?.seq);
  const loadedRailTicks = useMemo(() => {
    if (!fine) return NO_RAIL_TICKS;
    const ticks = navRailTicks(lines);
    return revealedSeq === undefined ? ticks.slice(-USER_INPUT_PAGE_SIZE) : ticks;
  }, [fine, lines, revealedSeq]);
  const inputRevision = useMemo(() => userInputRevision(tr.items), [tr.items]);
  const completeInputIndex = useUserInputs(mid, sid, tr.history, inputRevision, fine && !tr.loading, tr.reload);
  // the live stretch's inputs only (#5054): an earlier stretch of the live session (A → B → A) is drawn by EarlierStretches,
  // so the inputs before the live stretch's oldest loaded item are none of the rail's
  const liveFrom = earlier.liveItems === tr.items ? 0 : (earlier.liveItems[0]?.seq ?? Infinity);
  const inputIndex = useMemo(() => {
    if (!completeInputIndex) return null;
    const inputs = completeInputIndex.inputs.filter((input) => input.seq >= liveFrom);
    return windowUserInputs({ ...completeInputIndex, inputs, total: inputs.length }, revealedSeq);
  }, [completeInputIndex, liveFrom, revealedSeq]);
  const railTicks = useMemo(() => inputIndex ? indexedNavTicks(inputIndex, lines, earlier.liveItems) : loadedRailTicks, [inputIndex, lines, earlier.liveItems, loadedRailTicks]);
  /** Brings a row (or a run of words) into view in the chat's own scroller (scrollIntoView would also scroll an opening
   *  group's clipped box): following the end stops, and the jump's first frames do not pick it up again (`jumping`). */
  const landRow = useCallback(
    (row: HTMLElement | Range, topOffset: number) => {
      const sc = scroller.current;
      if (!sc) return;
      follow(false);
      clearTimeout(jumping.current);
      jumping.current = window.setTimeout(() => (jumping.current = 0), 1200);
      land(row, -topOffset);
      glideTo(() => sc.scrollTop + row.getBoundingClientRect().top - sc.getBoundingClientRect().top + topOffset);
    },
    [follow, land, glideTo],
  );
  const railJump = useCallback(
    async (lineId: string) => {
      const scope = `${mid}/${sid}`;
      const run = ++railRun.current;
      const deadline = Date.now() + 120_000;
      const target = inputIndex?.inputs.find((input) => `seg:${input.id}` === lineId);
      const cancelled = () => railChat.current !== scope || run !== railRun.current || Boolean(inputIndex?.history && trNow.current.history && inputIndex.history !== trNow.current.history);
      stopGlide();
      clearTimeout(landing.current?.quiet);
      landing.current = null;
      follow(false);
      if (!target || containsInput(trNow.current.items, target)) {
        const owner = target ? lineOfItem(linesNow.current, trNow.current.items, target.id) : lineId;
        const row = owner ? scroller.current?.querySelector<HTMLElement>(`[data-line="${CSS.escape(owner)}"]`)?.firstElementChild : null;
        if (row instanceof HTMLElement) { landRow(row, -12); return; }
      }
      if (target && !containsInput(trNow.current.items, target)) {
        // Reuse ordinary backward pages, keeping history contiguous rather than inserting an isolated window.
        await loadEarlierForSearch({
          get: () => trNow.current,
          load: () => loadWithScrollAnchor(() => trNow.current.loadEarlier({ waitForRetry: true })),
          frame: nextFrame,
          stopped: () => cancelled() || Date.now() >= deadline || containsInput(trNow.current.items, target),
          onPage: () => {},
          pages: Number.MAX_SAFE_INTEGER,
          joinEarlier: true,
        });
        if (cancelled()) return;
        if (!containsInput(trNow.current.items, target)) { toast(t("chat-navrail.target_failed"), "error"); return; }
      }
      const result = await untilStill<HTMLElement>({
        frame: nextFrame, now: () => performance.now(), cancelled, giveUpMs: GIVE_UP_MS,
        locate: () => {
          const owner = target ? lineOfItem(linesNow.current, trNow.current.items, target.id) : lineId;
          const row = owner ? scroller.current?.querySelector<HTMLElement>(`[data-line="${CSS.escape(owner)}"]`)?.firstElementChild : null;
          return row instanceof HTMLElement ? { y: row.getBoundingClientRect().top, value: row } : null;
        },
      });
      if (result === "cancelled") return;
      if (result === "missing") { toast(t("chat-navrail.target_failed"), "error"); return; }
      landRow(result.value, -12);
    },
    [mid, sid, inputIndex, follow, landRow, stopGlide, loadWithScrollAnchor],
  );
  const railSource = useMemo<NavRailSource>(() => ({ ticks: railTicks, total: inputIndex?.total, select: railJump }), [railTicks, inputIndex?.total, railJump]);
  // the calls with a terminal result: a subagent call without one is one the agent still waits on (the bar's step)
  const callsAnswered = useMemo(() => terminalCalls(tr.items, tasks), [tr.items, tasks]);
  // the live work lines' clocks on screen (WorkGroup's Elapsed): while one ticks, an older hub's guess at a background
  // workflow yields to the turn (the running bar's inBackground)
  const [clocks, setClocks] = useState(0);
  const countClock = useCallback((delta: 1 | -1) => setClocks((n) => n + delta), []);
  // while a message of yours is on its way the live line steps aside (lastLive needs !outgoing): the time stays where it was
  const barClock = useRef(true);
  if (!outgoing) barClock.current = clocks === 0;
  // "Jump to the step that started it" (#2719): open the groups the call is folded in, stop following the end, bring its row to the middle
  // and light it a moment; a call in a folded run of work is fetched first (jump.ts seekCall).
  const expandFold = tr.expandFold;
  const jumpToCall = useCallback(
    async (call: string) => {
      if (viewNow.current === "term") setView("chat");
      const scope = `${mid}/${sid}:`;
      const frame = () => new Promise<void>((r) => requestAnimationFrame(() => r()));
      const found = await seekCall(scope, call, items.current, expandFold, frame);
      if (!found) return void toast(t("chat-chatview.call_not_loaded"));
      follow(false);
      // the groups just opened grow over the next frames (Collapse): move once the row holds still, the chat's own
      // scroller only (scrollIntoView would also scroll the opening group's clipped box)
      let row: HTMLElement | null = null;
      let last = NaN;
      for (let still = 0, n = 0; n < 40 && still < 3; n++) {
        await frame();
        row = scroller.current?.querySelector<HTMLElement>(`[data-tool-use="${CSS.escape(call)}"]`) ?? null;
        const top = row?.getBoundingClientRect().top ?? NaN;
        still = row && Math.abs(top - last) < 0.5 ? still + 1 : 0;
        last = top;
      }
      const sc = scroller.current;
      if (!row || !sc) return void toast(t("chat-chatview.call_not_loaded"));
      landRow(row, -(sc.clientHeight - row.getBoundingClientRect().height) / 2);
      row.animate?.([{ backgroundColor: "var(--accent-tint)" }, { backgroundColor: "transparent" }], { duration: 1800, easing: "ease-out" });
    },
    [mid, sid, follow, expandFold, landRow, setView],
  );
  const jump = useCallback((call: string) => void jumpToCall(call), [jumpToCall]);
  // A link to one of its subagents' conversations (#5030, lib/subagent-link.ts): that subagent's card in view, opened; a
  // workflow agent's, its run's card. Once the chat and its subagents are in; a subagent no longer listed: just the chat.
  const linkedAgent = useSubagentLink(mid, sid);
  useEffect(() => {
    if (!linkedAgent || childOfCall(linkedAgent) || !session || tr.loading || detail.loading) return;
    doneSubagentLink(mid, sid);
    const agent = subagents?.find((a) => a.id === linkedAgent);
    const call = agent ? subagentCall(agent) : workflowAgentFor(session, `${sid}@${linkedAgent}`)?.run.tool_use_id;
    if (!call) return;
    if (agent) openExpanded([subagentCardKey(`${mid}/${sid}:`, call)]);
    jump(call);
  }, [linkedAgent, session, tr.loading, detail.loading, subagents, mid, sid, jump]);
  // its workflow runs by the Workflow call that launched each (#3434): rebuilt only when a run starts or goes, not with
  // each push of a run's progress (the cards read that themselves), so the exchanges do not all render again
  const runs = session?.workflows;
  const wfKey = workflowCallsKey(runs);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const wfCalls = useMemo(() => workflowCalls(runs), [wfKey]);
  // Preserve conversation identity; execution summaries have the same collapsed default everywhere (#2602).
  const lineStyle = workLineStyle(assistant, (session ?? archivedHere)?.assistant);
  // an extension opened this conversation (#3758): "From Lark /agent" under its first message, once the page reaches it
  const openedWith = useMemo(() => (assistant ? externalOpening(session, tr.items, tr.hasMore) : undefined), [assistant, session, tr.items, tr.hasMore]);
  const openingId = openedWith?.id;
  const openingOrigin = openedWith?.origin;
  // and the extension that opened it, whose own icon its mark is once known (#10358)
  const openingBy = openerOf(openingOrigin, session);
  const openingManager = session?.manager;
  // A stopped agent's closeout is a card above the transcript (#3612, CloseoutCard): while it is there, the reply that
  // carried the raw block, the last exchange's, shows without it.
  const closeoutShown = !assistant && Boolean(shownCloseout(session));
  const closeoutSegment = useMemo(() => {
    if (!closeoutShown) return undefined;
    const last = lines.findLast((line) => line.kind === "segment");
    return last?.kind === "segment" ? last.seg : undefined;
  }, [closeoutShown, lines]);
  // The child sessions folded under this one (#5120, a Codex subagent, an Agent a Paseo Agent created): each a card in
  // the exchange during which it started (lib/child-cards.ts). The placement is kept while nothing moves it, so a push
  // for a child redraws only its card, and a streamed item none of the exchanges.
  // read while the row says it has some, or a link to one waits (its parent's rollup may not be in yet)
  const linkedChild = linkedAgent ? childOfCall(linkedAgent) : undefined;
  // a side's chat reads its own children among its parent's folded rows, while that parent has some besides its sides
  const sideKids = useStore(() => {
    const c = sideParent ? getSession(mid, sideParent)?.children : undefined;
    return Boolean(c && c.total > (c.sides ?? 0));
  });
  const kids = useChildSessions(mid, sid, (sideParent ? sideKids : Boolean(session?.children?.total)) || Boolean(linkedChild), true, sideParent);
  // a lead's workers (#5180) are in its Team panel, not cards in its chat, and so are a parent's side sessions in its Side
  // sessions panel (#7944); the rest of what folds under it are child cards
  const childRows = useMemo(() => (kids.rows.some((c) => isWorker(c) || isSide(c)) ? kids.rows.filter((c) => !isWorker(c) && !isSide(c)) : kids.rows), [kids.rows]);
  const workers = useMemo(() => (leads ? teamWorkers(kids.rows) : []), [leads, kids.rows]);
  const sides = useMemo(() => (sidesHere ? sidesAmong(kids.rows) : []), [sidesHere, kids.rows]);
  // the worker the open Team panel shows: its cards are in its own conversation there, so the dock leaves them out (one
  // request in one place on the page, DESIGN_SYSTEM 3.4); every other worker's wait in this dock, marked as theirs. The
  // side the open Side sessions panel shows, the same way (#7944)
  const teamShown = teamOpen ? shownWorker(workers, mid, sid, teamPanelOf(panel, mid, sid)?.worker)?.session_id : undefined;
  const sideShown = sidesOpen ? shownSide(sides, mid, sid, sidesPanelOf(panel, mid, sid)?.side)?.session_id : undefined;
  const seenAt = useRef(new Map<string, string>());
  const lastPlaces = useRef<Map<string, ChildPlace[]> | undefined>(undefined);
  const places = useMemo(
    () => stableValue(lastPlaces.current, childRows.length ? placeChildren(exchangeStarts(lines), childPlaces(childRows, seenAt.current), !tr.hasMore) : NO_PLACES),
    [lines, childRows, tr.hasMore],
  );
  lastPlaces.current = places;
  // A link to a child's own conversation (lib/subagent-link.ts): its card opened, in view, once its row is in. A child
  // that started before the loaded history: earlier pages until its exchange is in (at most CHILD_LINK_PAGES); a child
  // that is not this session's (any more): just the chat.
  const childLinkPages = useRef({ child: "", pages: 0 });
  useEffect(() => {
    // a link followed to its end gives the next one, the same child's again too, its whole budget of earlier pages
    if (!linkedChild) childLinkPages.current.child = "";
    if (!linkedChild || !session || tr.loading || detail.loading || tr.loadingEarlier) return;
    if (!childRows.some((c) => c.session_id === linkedChild)) {
      if (kids.loaded) doneSubagentLink(mid, sid);
      return;
    }
    if (![...places.values()].some((ps) => ps.some((p) => p.sid === linkedChild))) {
      const pages = childLinkPages.current;
      if (pages.child !== linkedChild) Object.assign(pages, { child: linkedChild, pages: 0 });
      if (tr.hasMore && pages.pages++ < CHILD_LINK_PAGES) void tr.loadEarlier();
      else doneSubagentLink(mid, sid);
      return;
    }
    doneSubagentLink(mid, sid);
    openExpanded([childCardKey(`${mid}/${sid}:`, linkedChild)]);
    jump(childCall(linkedChild));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [linkedChild, session, tr.loading, tr.loadingEarlier, tr.hasMore, detail.loading, kids.loaded, childRows, places, mid, sid, jump]);
  // what runs its subagents: its own agent (a Paseo provider's subagent cards say "Grok", #8142)
  const harness = session && !assistant ? sessionAgentName(session) : undefined;
  const ctx = useMemo<ChatContextValue>(
    () => ({ tasks, subagents: assistant ? NO_SUBAGENTS : (subagents ?? NO_SUBAGENTS), scope: `${mid}/${sid}:`, jumpToCall: jump, workflowCalls: wfCalls, lineStyle, harness, opening: openingId && openingOrigin ? { id: openingId, origin: openingOrigin, opened_by: openingBy, manager: openingManager } : undefined, closeoutSegment: closeoutSegment?.id, children: places, embedded }),
    [tasks, subagents, assistant, mid, sid, jump, wfCalls, lineStyle, harness, openingId, openingOrigin, openingBy, openingManager, closeoutSegment, places, embedded],
  );
  // ── "Search in chat" (ChatSearch.tsx, Android ChatSearchState): where the reader was when it opened, going to a hit (the
  // rows that hold it open, search-reveal.ts), lit for HIT_MS, and "Back to where you were" ──
  const [search, setSearch] = useState<{ open: boolean; query: string }>({ open: false, query: "" });
  const origin = useRef<{ atEnd: boolean; line?: string; offset: number } | null>(null);
  const [picked, setPicked] = useState(false);
  const going = useRef(0);
  // the ring on the hit gone to: one timer, restarted when another (or the same) row lights
  const lit = useRef<{ el: HTMLElement; timer: number } | null>(null);
  const unlight = useCallback(() => {
    if (!lit.current) return;
    clearTimeout(lit.current.timer);
    lit.current.el.removeAttribute("data-search-hit");
    lit.current = null;
  }, []);
  useEffect(() => {
    setSearch({ open: false, query: "" });
    setPicked(false);
    origin.current = null;
    going.current++;
    unlight();
    forgetReveals();
  }, [mid, sid, unlight]);
  const openSearch = useCallback(() => {
    const sc = scroller.current;
    if (sc && !origin.current) {
      // the first line still in view, and how far under the view's top it starts
      origin.current = { atEnd: atEnd.current, ...firstLineInView(sc) };
    }
    setSearch((x) => ({ ...x, open: true }));
  }, []);
  const closeSearch = useCallback(() => {
    setSearch((x) => ({ ...x, open: false }));
    if (!picked) origin.current = null;
  }, [picked]);
  // brings an item into view, opening what holds it, and lights it: a search hit, or a forward's original (#3899)
  const seekItem = useCallback((itemId: string, quiet = false) => {
    const sc = scroller.current;
    if (!sc) return;
    follow(false);
    revealItem(ctx.scope, itemId);
    const run = ++going.current;
    void untilStill<HTMLElement>({
      frame: nextFrame,
      now: () => performance.now(),
      cancelled: () => run !== going.current,
      giveUpMs: GIVE_UP_MS,
      // the rows that hold it take a few frames to open: the line it is on only once they had the time
      locate: (waited) => {
        const exact = sc.querySelector<HTMLElement>(`[data-items~="${CSS.escape(itemId)}"]`);
        const lineId = exact || waited <= FIND_MS ? undefined : lineOfItem(linesNow.current, trNow.current.items, itemId);
        const el = exact ?? (lineId ? sc.querySelector<HTMLElement>(`[data-line="${CSS.escape(lineId)}"]`)?.firstElementChild : null);
        return el instanceof HTMLElement ? { y: el.getBoundingClientRect().top - sc.getBoundingClientRect().top + sc.scrollTop, value: el } : null;
      },
    }).then((r) => {
      if (r === "cancelled") return;
      clearReveal();
      if (r === "missing") return void (quiet || toast(t("chat-chatview.search_not_found")));
      const el = r.value;
      const tall = el.getBoundingClientRect().height > sc.clientHeight;
      el.scrollIntoView({ block: tall ? "start" : "center", behavior: "auto" });
      land(el, el.getBoundingClientRect().top - sc.getBoundingClientRect().top);
      unlight();
      // the ring hugs what it marks: your bubble itself, not its row's padding (#6426)
      const ring = (el.matches("[data-user-bubble]") && el.querySelector<HTMLElement>("[data-hit-target]")) || el;
      void ring.getBoundingClientRect(); // the same row picked again lights again
      ring.setAttribute("data-search-hit", "");
      lit.current = { el: ring, timer: window.setTimeout(unlight, HIT_MS) };
    });
  }, [ctx.scope, follow, unlight, land]);
  // the work the hub folded, fetched for the search while it is open (useFoldSearch, #6281): a hit in it opens its fold
  // into the chat first, as "Jump to the step" does (jump.ts seekCall)
  const folds = useFoldSearch(mid, sid, tr.history, earlier.liveItems, search.open);
  const searchItems = useMemo(() => (folds.items.length ? [...earlier.liveItems, ...folds.items] : earlier.liveItems), [earlier.liveItems, folds.items]);
  const goTo = useCallback((hit: ChatSearchHit) => {
    setSearch((x) => ({ ...x, open: false }));
    setPicked(true);
    const fold = earlier.liveItems.some((i) => i.id === hit.itemId) ? undefined : foldHolding(folds.fetched, hit.itemId);
    if (!fold?.fold) return seekItem(hit.itemId);
    openFoldRange(ctx.scope, fold.seq, fold.fold.through_seq); // its steps land in open groups
    tr.expandFold(fold).then(
      () => requestAnimationFrame(() => seekItem(hit.itemId)),
      () => toast(t("chat-chatview.search_not_found")),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps -- tr.expandFold follows the chat, as seekItem does
  }, [seekItem, earlier.liveItems, folds.fetched, ctx.scope]);
  const backToOrigin = useCallback(() => {
    const o = origin.current;
    origin.current = null;
    setPicked(false);
    landing.current = null;
    const run = ++going.current;
    unlight();
    undoReveals();
    const sc = scroller.current;
    if (!sc || !o) return;
    if (o.atEnd) return void requestAnimationFrame(() => requestAnimationFrame(() => run === going.current && toEnd()));
    // what the hits opened closes over a few hundred ms (Collapse): keep the origin line where it was until it holds
    void untilStill<true>({
      frame: nextFrame,
      now: () => performance.now(),
      cancelled: () => run !== going.current,
      giveUpMs: GIVE_UP_MS,
      locate: () => {
        const el = o.line ? sc.querySelector<HTMLElement>(`[data-line="${CSS.escape(o.line)}"]`)?.firstElementChild : null;
        if (!el) return null;
        const scTop = sc.getBoundingClientRect().top;
        sc.scrollTop += el.getBoundingClientRect().top - scTop - o.offset;
        return { y: el.getBoundingClientRect().top - scTop + sc.scrollTop, value: true };
      },
    }).then((r) => r === "missing" && toast(t("chat-chatview.search_origin_changed")));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [unlight]);
  // "Load earlier and keep searching" (lib/chat-search.ts loadEarlierForSearch): the history's own pages, one after another, the
  // view kept still as each lands above it
  const loadForSearch = useCallback(async (onPage: (n: number) => void, stopped: () => boolean): Promise<LoadOutcome> => {
    const outcome = await loadEarlierForSearch({
      get: () => trNow.current,
      load: () => loadWithScrollAnchor(trNow.current.loadEarlier),
      frame: nextFrame,
      stopped,
      onPage,
    });
    const key = SEARCH_NOTICE[outcome];
    return key ? t(key) : null;
  }, [loadWithScrollAnchor]);
  // a message to open the chat at: a search hit's (`?at=`, #6283) or a forward card's original (#3899, lib/forward.ts
  // jumpWhenOpen). Brought into view once the transcript is in and holds it; one further up is paged back for, as "Load
  // earlier and keep searching" does (at most SEARCH_PAGES pages, the reader scrolling away or another landing stops
  // it); not reached, the chat stays where it opened and says the message is further up.
  useEffect(() => {
    if (landAt) jumpWhenOpen(mid, sid, landAt);
  }, [mid, sid, landAt]);
  const msgLanding = useRef(0);
  useEffect(() => {
    const item = peekJump(mid, sid);
    if (!item || tr.loading) return;
    takeJump(mid, sid);
    const run = ++msgLanding.current;
    const holds = () => trNow.current.items.some((i) => i.id === item);
    if (holds()) return void requestAnimationFrame(() => seekItem(item, true));
    if (!trNow.current.hasMore) {
      void (async () => {
        // A folded assistant reply is not a raw item: resolve it, then open only the fold that holds it.
        const whole = await fullItem(mid, sid, item);
        if (run !== msgLanding.current) return;
        const fold = trNow.current.items.find((f) => insideFold(whole, f));
        if (!fold) return;
        openFoldRange(ctx.scope, fold.seq, fold.fold!.through_seq);
        await trNow.current.expandFold(fold);
        if (run !== msgLanding.current) return;
        requestAnimationFrame(() => seekItem(item));
      })().catch(() => {});
      return;
    }
    void (async () => {
      // the chat may still be filling the screen with older pages: wait for that, then page back (again if it was busy)
      for (let go = 0, until = performance.now() + LANDING_WAIT_MS; go < 3 && run === msgLanding.current && !holds(); go++) {
        while ((trNow.current.loading || trNow.current.loadingEarlier) && performance.now() < until) await nextFrame();
        await loadForSearch(() => {}, () => run !== msgLanding.current || holds());
      }
      if (run !== msgLanding.current) return;
      if (holds()) requestAnimationFrame(() => seekItem(item));
      else toast(t("chat-chatview.message_further_up"));
    })().catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps -- a new landing is a new item asked for, not the items changing
  }, [mid, sid, tr.loading, landAt, seekItem, loadForSearch]);
  useEffect(() => () => void msgLanding.current++, [mid, sid]); // another chat ends a landing on its way
  const rounds = useMemo(() => segs.filter((x) => x.user).length, [segs]);

  // the newest plan of the chat (PlanCard.tsx planCardStep draws its card): while that card is out of view, a pill above the composer
  const plan = useMemo(() => latestPlan(segs), [segs]);
  const planOut = usePlanOutOfView(scroller, plan?.id);
  // Close Plan in the pill's flyout hides it until the plan changes (cindy's PinnedPlanPanel)
  const [planClosed, closePlan] = usePlanClosed(`${mid}/${sid}`, plan);
  const lastLine = lines[lines.length - 1];
  // After the end marker the last reply can still be waiting for its item. Its text stays visible
  // without keeping the exchange working, even if the host already reported completion.
  const wordsFor = (line: ChatLine) => (line.kind === "segment" || line.kind === "supervision") &&
    ((line.live && streaming) || (line === lastLine && tr.streamEnded && canShowPreview)) ? tr.streaming || undefined : undefined;
  // "Retry" on the error banner and the failure note: sends "continue" alone, not what is in the box. Whether it went:
  // deliver can refuse without leaving a bubble (the setup gate, a row that cannot send, a refused launcher switch)
  const retry = useCallback(() => deliver({ text: t("chat-chatview.continue_text"), images: [] }, uuid()), [deliver]);
  // the failure notice "Retry" was pressed on: kept here, not in the note, so it stays gone while the session goes through
  // needs-you and back before the hub clears it (iOS ChatView @State retried)
  const [retriedOn, setRetried] = useState<{ chat: string; reason?: string }>();
  const retried = retriedOn?.chat === `${mid}/${sid}` ? retriedOn.reason : undefined;
  const failureNote = Boolean(session && !assistant && showsFailureNote(session, { retried }));
  // a turn that failed on the refusal fallback's missing model (#2528): "Continue" would send it to the same model; the
  // main conversation's setup gate would only open its missing step, as it does for every send while it holds (`deliver`)
  const canRetry =
    lastLine?.kind === "apiError" && state !== "working" && !outgoing && sessionCan(session, "send") &&
    session?.turn_error_code !== FALLBACK_UNAVAILABLE && session?.turn_error_code !== "compaction_sealed" &&
    session?.turn_error_kind !== "unrecoverable" && !gate.blocked;
  // An exhausted recovery chain (#7824): the last error card names the cause and reset time, and Retry stays offered
  // even for a safeguards-class error the parser calls non-transient. The marker is session-level, so only the last
  // API error line carries it; whether Retry is enabled at all still follows canRetry (idle and able to send).
  const exhausted = lastLine?.kind === "apiError" ? recoveryExhausted(session) : null;
  // The model error the chat stopped on (cindy's ErrorBanner, #5313): its last line, over the composer with Retry and ✕,
  // and left out of the chat meanwhile. Not once it is dismissed (remembered in this browser), Retry was pressed on it
  // (gone at once, before "continue" reaches the transcript), something is on its way or being worked on, or the host's
  // failure note already says why the turn stopped; then, and once anything comes after it, it is a card in the chat.
  const errorChat = `${BASE}/${mid}/${sid}`;
  const [errorClosed, setErrorClosed] = useState(() => dismissedError(errorChat));
  useEffect(() => setErrorClosed(dismissedError(errorChat)), [errorChat]);
  const [errorRetried, setErrorRetried] = useState<string>();
  const tailError = lastLine?.kind === "apiError" ? lastLine : undefined;
  const tailKey = tailError ? `${errorChat}|${errorKey(tailError)}` : "";
  const bannerError =
    tailError && !working && canCompose && !stoppedLine && !failureNote && errorKey(tailError) !== errorClosed && tailKey !== errorRetried ? tailError : undefined;
  // the banner's buttons go with it: the keyboard goes back to the composer, not on a touch screen alone (that would raise
  // the keyboard). Called on a press, by when the composer's box (declared below) is there.
  const backToComposer = () => {
    if (fine) composerBox.current?.querySelector<HTMLTextAreaElement>(COMPOSER_INPUT)?.focus({ preventScroll: true });
  };
  // gone at once; a retry that did not go brings it back, as nothing else on the page could retry then
  const retryError = () => {
    const key = tailKey;
    if (key) setErrorRetried(key);
    backToComposer();
    return retry().then((went) => {
      if (!went && key) setErrorRetried((k) => (k === key ? undefined : k));
      return went;
    });
  };
  const dismissBanner = () => {
    if (!tailError) return;
    dismissError(errorChat, errorKey(tailError));
    setErrorClosed(errorKey(tailError));
    backToComposer();
  };
  // A new banner is brought into view in the box over the composer, which scrolls from its top when what is in it is
  // taller than the dock may be (a short phone, the main conversation's setup card and action bar above it): the chat no
  // longer shows the error. Once per error, so a reader scrolling that box is left alone; the chat itself never moves.
  const dockAbove = useRef<HTMLDivElement>(null);
  const bannerKey = bannerError ? errorKey(bannerError) : "";
  useLayoutEffect(() => {
    const box = dockAbove.current;
    const el = bannerKey ? box?.querySelector("[data-error-banner]") : null;
    if (!box || !el) return;
    const b = box.getBoundingClientRect();
    const r = el.getBoundingClientRect();
    const up = r.top - b.top;
    const down = r.bottom - b.bottom;
    if (up < 0) box.scrollTop += up;
    else if (down > 0) box.scrollTop += Math.min(down, up);
  }, [bannerKey]);
  // Claude Code's "while you were away" recap, open in full while it is the newest thing and not closed: the chat's last
  // line, in the flow with the agent's words (#1360: above the composer it covered the end of the reply)
  const recapKey = `${mid}/${sid}`;
  const [recapClosed, setRecapClosed] = useState(() => closedRecap(recapKey));
  useEffect(() => setRecapClosed(closedRecap(recapKey)), [recapKey]);
  const newestRecap = !assistant && lastLine?.kind === "recaps" ? lastLine.items[lastLine.items.length - 1] : undefined;
  const pendingRecap = newestRecap && recapClosed !== newestRecap.id ? newestRecap : undefined;
  const open = useMemo(() => openRecap(lines, pendingRecap), [lines, pendingRecap]);
  const dismissRecap = () => {
    if (!pendingRecap) return;
    closeRecap(recapKey, pendingRecap.id);
    setRecapClosed(pendingRecap.id);
  };
  // On a phone (#1996): what is under the chat stays within about half of the visible height (dock.ts); the keyboard is
  // up while the composer has the focus on a touch screen alone
  const narrow = useMedia(NARROW);
  // a narrow screen's cards fold and its dock keeps to DOCK_MAX (#1282)
  // a worker's conversation in a Team panel (embedded) and a task's or an Agent's side panel are a phone's width, so
  // their docks are one too: unbounded there, a plan with a question and a permission stood taller than the panel (#1282)
  const compactDock = narrow || Boolean(embedded) || inPanel;
  const short = useMedia(SHORT);
  const [typing, setTyping] = useState(false);
  const keyboard = typing && !fine;
  const composerBox = useRef<HTMLDivElement>(null);
  // after a switch the keyboard goes to what is now shown (not on a touch screen: that would raise the keyboard)
  const termLayer = useRef<HTMLDivElement>(null);
  const shown = useRef(view);
  useEffect(() => {
    if (shown.current === view) return;
    shown.current = view;
    if (!fine) return;
    const frame = requestAnimationFrame(() => {
      const field = view === "term" ? termLayer.current?.querySelector("textarea") : composerBox.current?.querySelector<HTMLTextAreaElement>(COMPOSER_INPUT);
      field?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [view, fine]);
  // A card answered above the composer takes the focused button with it: the keyboard goes back to the composer (#5154).
  // Not on a touch screen alone (that would raise the keyboard), nor when something else took the focus.
  const dockFocus = useRef<Element | null>(null);
  // A card's own field being typed into (#11442): a page made short by Android's keyboard must not fold that card away,
  // until the focus leaves the card (its send arrow or options take it on the tap: folded then, the tap is lost)
  const [cardField, setCardField] = useState(false);
  const onDockFocus = (e: React.FocusEvent) => {
    dockFocus.current = composerBox.current?.contains(e.target) ? null : e.target;
    if (isCardField(e.target)) setCardField(true);
    else if (!inOwnCard(e.target)) setCardField(false);
  };
  const onDockBlur = (e: React.FocusEvent) => {
    if (!inOwnCard(e.relatedTarget)) setCardField(false);
  };
  // Checked after each render only while a control above the composer holds the focus (nothing to do otherwise)
  useEffect(() => {
    if (!dockFocus.current || !lostWith(dockFocus.current, document.activeElement, document.body)) return;
    dockFocus.current = null;
    setCardField(false);
    if (fine) composerBox.current?.querySelector<HTMLTextAreaElement>(COMPOSER_INPUT)?.focus({ preventScroll: true });
  });
  // the run's time counts from the turn's start, as its "Working…" does (not from the session's last state change)
  const stateSince = session?.state_since;
  const liveSeg = useMemo(() => {
    const live = lines.flatMap((l) => l.kind === "supervision" ? l.lines : [l]).findLast((l) => l.kind === "segment" && l.live);
    return live?.kind === "segment" ? live.seg : undefined;
  }, [lines]);
  const processing = !sendFeedback && Boolean(assistant || session?.assistant) && state === "working" && !backgroundOnly &&
    !liveSeg && (tr.loading || (opening && !tr.streaming));
  const runStart = (liveSeg ? turnStart(liveSeg) : undefined) ?? stateSince;
  // what the turn is doing, for the bar's words (cindy's status): kept as it was while it says the same
  const lastStep = useRef<RunStep | undefined>(undefined);
  const step = useMemo(() => sameStep(lastStep.current, runStep(liveSeg, streaming, subagents, callsAnswered)), [liveSeg, streaming, subagents, callsAnswered]);
  lastStep.current = step;
  // as cindy's: gone once every step is done or a later message of the user's started another turn
  const planPill = plan && planOut && !plan.superseded && !planClosed && !planProgress(plan.todos).allDone ? (
    <Suspense key={plan.id} fallback={null}>
      <PlanPill todos={plan.todos} live={lastLive} onClose={closePlan} />
    </Suspense>
  ) : null;
  // Persona's own chats too (#3654), and from the moment the message is in, not from the hub's next push
  const background = useMemo(() => backgroundLine(session), [session]);
  // Streaming makes the turn live again, but an Agent's existing wait / subagent bar stays as it was while the answer
  // streams. Only the new command line is withheld there; in a quiet turn the four-kind order decides the bar.
  const wait = streaming && !outgoing && !awaiting ? backgroundWaitLine(session) : backgroundState?.kind === "waiting" ? backgroundState.line : null;
  const subtasks = streaming ? waitingOnSubagents(session, subagents) && !outgoing && !awaiting : backgroundState?.kind === "subtasks";
  const command = backgroundState?.kind === "command" ? backgroundState.line : null;
  // ✓ Done only for a turn that finished: one waiting on you, stopped or cut off by an error keeps its last words
  const done = turnDone(state, session, lastLine, tr.items);
  // In a Persona conversation the message stream already carries the turn's state for its whole life — the sending /
  // preparing feedback while the message is on its way, then the live work line ("Working…", spinner and clock) once the
  // hub accepts it — so the dock never repeats the foreground turn (#6230). The dock bar stays only for work that outlives
  // the turn (a background workflow, subagent, wait or working command: `backgroundOnly`, where the stream shows only
  // quiet fold rows) and, unchanged, for Agent chats, whose dock keeps the full cindy status bar. Gating on the foreground/background
  // phase (not on the live line being mounted) keeps the bar out even while a transcript loads or a state push is in flight.
  // A Persona chat on either route — the Persona tab's prop, or on the generic /s route the session's own assistant
  // flag — so the dock never repeats its foreground turn (#6230). Background work keeps the bar either way.
  const dockBarShown = !(assistant || session?.assistant) || backgroundOnly;
  const running = session && dockBarShown ? <RunningStatusBar mid={mid} sid={sid} working={state === "working" || awaiting || streaming} done={done} startedAt={runStart} clock={barClock.current} plain={assistant} step={step} background={!backgroundState || backgroundState.kind === "workflow" ? background : null} wait={wait} subtasks={subtasks} command={command} /> : null;
  // at the top of the composer's card, as cindy's pending queue (#5327); a phone on its side keeps it to one line beside
  // the run's status. The same element while nothing in it changes: the composer is memoized.
  const hasQueue = queued.length > 0;
  const queue = useMemo(
    () => (session && hasQueue ? <QueueStrip session={session} draftKey={draftKey} canEdit={canCompose} compact={short} persona={assistant ? persona.name : undefined} onWithdrawn={withdrawn} photosOf={assistant || session.capabilities?.queue_edit ? queuedPhotos : undefined} photoPreviewsOf={queuedPhotoPreviews} /> : null),
    [session, hasQueue, draftKey, canCompose, short, assistant, persona.name, withdrawn, queuedPhotos, queuedPhotoPreviews],
  );
  // with the plan's pill showing, "Jump to bottom" goes beside it (over the chat, they hid each other); the pill keeps
  // one wrapper either way, so a flyout pinned open on it stays when the reader scrolls
  const jumpInDock = Boolean(planPill);
  const pills = planPill ? (
    <div className="flex shrink-0 items-center justify-center gap-2" data-plan-pills>
      {planPill}
      {away && <JumpPill label={jumpLabel(newSince)} onClick={toEnd} inline />}
    </div>
  ) : null;
  // the header and the composer are memoized (a transcript push changes neither): what they get stays the same object
  // until it changes. The assistant's face reads only the last item's role and kind (lib/persona.ts personaState).
  const stopLoop = useCallback(() => void deliver({ text: STOP_LOOP_MESSAGE, images: [] }, uuid()), [deliver]);
  // the Background tasks panel stops the /loop and shows a workflow's call through the page showing the chat (#5421)
  useEffect(() => (assistant || embedded ? undefined : provideBackgroundChat(mid, sid, { stopLoop, jumpToCall: jump })), [assistant, embedded, mid, sid, stopLoop, jump]);
  // cached content stays up when a read fails: the pill says so, the error page is for an empty chat
  const refreshFailed = tr.items.length > 0 ? tr.error || refreshError : undefined;
  const lastItem = assistant ? tr.items[tr.items.length - 1] : undefined;
  const lastRole = lastItem?.role;
  const lastKind = lastItem?.kind;
  const faceLast = useMemo(() => (lastRole && lastKind ? { role: lastRole, kind: lastKind } : undefined), [lastRole, lastKind]);
  // the main conversation's /clear (#5095, lib/main-clear.ts) and every Persona conversation's /permissions (#6476)
  // /workflows opens this page's side panel: a chat drawn inside a panel (the Team panel's worker) does not offer it (#5421)
  const commands = useMemo(
    () => (assistant ? [...(clears ? mainCommands() : []), ...(session ? [permissionsCommand()] : [])] : agentCommands(AGENT_COMMANDS, session).filter((c) => !(embedded && c.text === WORKFLOWS_COMMAND))),
    [assistant, clears, session, embedded],
  );
  // the permission picker after the + (#5324), where the mode can be switched: a Persona conversation whatever its state
  // (the hub applies it, #2416), an agent with modes while it runs; Plan mode is the + panel's check row. An agent that
  // lists its own modes (#8261) gets the picker with those two, and no Plan mode.
  const modesOn = Boolean(session) && (Boolean(assistant) || (hasModes(session) && stateOf(session!) !== "offline"));
  const listedOn = Boolean(session) && !assistant && Boolean(listedModesOf(session)) && stateOf(session!) !== "offline";
  const leading = useMemo(() => (session && (modesOn || listedOn) ? <PermissionPicker session={session} assistant={assistant} /> : null), [session, modesOn, listedOn, assistant]);
  const planMode = usePlanMode(modesOn ? session : undefined, assistant);
  // the model pill: an Agent's (#5032), a Persona conversation's (#5033, its pick kept for the conversation)
  const trailing = useMemo(() => (session ? assistant ? <AssistantModelPill session={session} /> : <AgentModelPill session={session} /> : null), [session, assistant]);

  // the header's Team button (#5180): on a lead with workers, at every width (a phone has no other way to the panel); its dot
  // says a worker needs you while the panel is shut
  const showConversationTasks = Boolean((assistant || inPage) && !embedded && !chrome && session?.space !== "health");
  const conversationTasksOpen = panel?.kind === "conversation-tasks" && panel.mid === mid && panel.sid === sid;
  const conversationTasks = useMemo(() => (showConversationTasks
    ? { open: conversationTasksOpen, onToggle: () => togglePanel({ kind: "conversation-tasks", mid, sid, manager: skillManager }) }
    : undefined), [showConversationTasks, conversationTasksOpen, mid, sid, skillManager]);
  const teamCount = leads ? workerCount(session) : 0;
  const teamNeeds = workers.some((w) => stateOf(w) === "needs_you");
  const toggleTeam = useCallback(() => {
    if (teamPanelOf(getPanel(), mid, sid)) return openPanel(null);
    recordTeam(mid, sid, "open");
    openPanel({ kind: "team", mid, sid });
  }, [mid, sid]);
  const team = useMemo(() => (teamCount > 0 ? { open: teamOpen, needs: teamNeeds, onToggle: toggleTeam } : undefined), [teamCount, teamOpen, teamNeeds, toggleTeam]);
  // the header's Side sessions button (#7944): while it has sides, with their count and a dot while one needs you, both
  // from the side rows held here (a side seen here shows at once), else the hub's rollup for them
  const hubSide = sidesHere && session ? hubSides(session) : undefined;
  const sideCount = kids.loaded ? sides.length : Math.max(sides.length, hubSide?.sides ?? 0);
  const sideNeeds = sides.some((w) => stateOf(w) === "needs_you") || (!kids.loaded && (hubSide?.needs_you ?? 0) > 0);
  const toggleSide = useCallback(() => toggleSides(mid, sid, Boolean(sidesPanelOf(getPanel(), mid, sid))), [mid, sid]);
  const sideControl = useMemo(() => (sideCount > 0 ? { count: sideCount, open: sidesOpen, needs: sideNeeds, onToggle: toggleSide } : undefined), [sideCount, sidesOpen, sideNeeds, toggleSide]);
  const elsewhere = (ix: Interaction) => (Boolean(teamShown) && ix.session_id === teamShown) || (Boolean(sideShown) && (ix.session_id === sideShown || ix.folded_side === sideShown));
  // a worker's own cards in its conversation (the Team panel, embedded) are its own there, not "From worker"
  const docked = useMemo(() => {
    const mine = teamShown || sideShown ? interactions.filter((ix) => !elsewhere(ix)) : interactions;
    return embedded ? mine.map((ix) => (ix.session_id === sid && ix.folded_under ? asOwnCard(ix) : ix)) : mine;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [interactions, teamShown, sideShown, embedded, sid]);
  // a Persona chat's own requests hold its turn: open above the capsules (TurnCards), out of the stack and of Needs you's
  // count; the rest (a worker's, a side session's) stay in the stack and the tray as before (lib/turn-asks.ts)
  const turnSplit = useMemo(() => (assistant ? splitTurnAsks(docked, mid, sid) : { turn: [], rest: docked }), [assistant, docked, mid, sid]);
  const trayAsks = useMemo(() => (assistant ? splitTurnAsks(interactions, mid, sid).rest : interactions), [assistant, interactions, mid, sid]);
  const pendingCards = (
    <PendingCards
      interactions={focusedFirst(turnSplit.rest, focusedCard())}
      answered={teamShown || sideShown ? answered.filter((a) => !elsewhere(a.ix)) : answered}
      delegated={teamShown || sideShown ? delegated.filter((d) => !elsewhere(d.ix)) : delegated}
      offerNext={inPage && !assistant ? { mid, sid } : undefined}
      narrow={compactDock}
      short={short && !cardField}
      keyboard={keyboard}
      onDismissKeyboard={() => composerBox.current?.querySelector<HTMLTextAreaElement>(COMPOSER_INPUT)?.blur()}
    />
  );

  const overviewId = useId();
  const [overviewOpen, setOverviewOpen] = useState(false);
  const showOverview = !assistant && !embedded && Boolean(session);
  useEffect(() => {
    if (!showOverview || !overviewOpen) return;
    // The origin owns both the visible dependent detail and the one Cmd+B remembers. Its closure, including leaving
    // this conversation, invalidates both; while it stays open, Cmd+B can close and restore the detail as usual.
    return () => invalidateOverviewPanel(mid, sid);
  }, [showOverview, overviewOpen, mid, sid]);
  const overview = useMemo(() => showOverview
    ? { id: overviewId, open: overviewOpen, toggle: () => setOverviewOpen((open) => !open) }
    : undefined, [overviewId, overviewOpen, showOverview]);
  // Its background detail takes the side panel's space, rather than squeezing the mounted overview and losing its place.
  const overviewDetail = panelDrawn && backgroundPanelOf(panel, mid, sid)?.from === "overview";

  const page = (
    <div className="@container/session flex h-full min-w-0 flex-col">
      {!embedded && <Header
        session={session}
        starting={starting}
        interactions={interactions}
        overview={overview}
        title={title}
        direction={directionName !== undefined && !isMain && !notFound}
        machineName={machineName}
        assistant={assistant}
        main={isMain}
        siblings={siblings}
        onToggleSidebar={onToggleSidebar}
        onInterrupt={interrupt}
        last={faceLast}
        onSearch={openSearch}
        topCenter={topCenter}
        view={switchable ? view : undefined}
        onView={switchable ? setView : undefined}
        onTerminal={openTerminal}
        viewDot={view === "term" && (interactions.length > 0 || state === "needs_you")}
        termDot={terminalWaits(session, interactions, outbox)}
        actions={headerActions}
        tasks={conversationTasks}
        team={team}
        sides={sideControl}
        panel={chrome}
      />}
      {isMain && !embedded && <MainRuntimeSlot sid={sid} />}
      {!embedded && banner}
      {backPage && (cameFrom ? <Suspense fallback={null}><HandoffBack from={cameFrom} onDismiss={() => setCameFrom(null)} /></Suspense> : cameFrom === undefined ? <Suspense fallback={null}><InboxBack key={`${mid}/${sid}`} mid={mid} sid={sid} session={session} /></Suspense> : null)}
      {/* a known session keeps its chat: a failed background read is retried quietly (runtime-session.ts) */}
      {archivedHere ? <ArchivedNotice session={archivedHere} /> : notFound ? <Suspense fallback={null}><LazyNotFoundNotice assistant={Boolean(assistant)} retry={retryDetail} /></Suspense> : noDetails && <SessionDetailNotice error={detail.error} retry={retryDetail} />}

      <div className="session-workspace flex min-h-0 min-w-0 flex-1 flex-col">
      <div className="relative flex min-h-0 min-w-0 flex-1 flex-col">
      <ViewLayer bare={!switchable} on={view === "chat"} from="start">
      {/* what a stopped agent said of its own work, above the transcript and not part of it (#3612) */}
      {closeoutShown && session && (
        <Suspense fallback={null}>
          <LazyCloseoutCard key={`${mid}/${sid}`} session={session} segment={closeoutSegment} expandFold={tr.expandFold} />
        </Suspense>
      )}
      <div className="relative min-h-0 flex-1">
        {(tr.notLive || refreshFailed) && <NotLivePill error={refreshFailed} onReload={() => { tr.reload(); onRefresh?.(); }} lower={Boolean(picked && !search.open)} />}
        <div ref={scrollRef} onScroll={onScroll} data-chat-scroll className="relative h-full overflow-y-auto [overflow-anchor:none]">
          <ChatActionsContext.Provider value={actions}>
          <div data-chat-content className="mx-auto flex w-full max-w-[880px] flex-col gap-3.5 px-5 pt-7 pb-10 sm:px-8">
            {session?.start_error && (session.start_error_code ? (
              // a refusal with a code (#1642): the hub's whole sentence in the user's language, its remedy last, under a title
              <div role="alert" data-testid="start-error-coded" className="rounded-control bg-inset px-3 py-2 text-[13px]">
                <div className="font-medium text-red">{t("chat-chatview.start_failed_title")}</div>
                <p className="mt-0.5 leading-relaxed text-ink-2 [overflow-wrap:anywhere]">{session.start_error}</p>
              </div>
            ) : <div role="alert" className="rounded-control bg-inset px-3 py-2 text-[13px] text-red">{t("chat-chatview.start_failed", { error: session.start_error })}</div>)}
            {session?.start_error_code === "shell_waiting" && <Suspense fallback={null}><ShellQuestion session={session} /></Suspense>}
            {starting && !tr.loading && !outgoing && <Loading label={t("chat-chatview.starting_agent")} />}
            {/* over the content's top padding, in the scroller: starting and ending an older page move nothing on screen,
                and nothing writes scrollTop for it (that would stop a touch fling) */}
            {earlier.loadingEarlier && (
              <div data-loading-earlier className="pointer-events-none absolute inset-x-0 top-1 flex justify-center">
                <Loading label={t("chat-chatview.loading_earlier")} />
              </div>
            )}
            {/* also over a chat whose first page came back empty (#2450): it reads as loaded and empty, not as still loading;
                not under a notice that the chat's details are missing */}
            {((!earlier.hasMore && !tr.loading && (tr.items.length > 0 || earlier.stretches.length > 0 || (!tr.error && !starting && !session?.start_error && !noDetails))) || opening) && <div className="text-center text-[11.5px] text-ink-3">{session?.cwd && !assistant ? t("chat-chatview.started_in", { path: shortPath(session.cwd) }) : t("chat-chatview.chat_start")}</div>}
            {opening && !dayTaken.has(new Date().toLocaleDateString("sv")) && <DayLine date={startOfDay(Date.now())} />}
            {waitsFirst && !tr.items.length && (
              // a Codex opened with no first message (lib/codex-waiting.ts): it waits for one, from here or its terminal
              <p data-testid="codex-waiting" className="rounded-control bg-inset px-3 py-2 text-[13px] leading-relaxed text-ink-2">
                {t("chat-chatview.codex_waiting", { computer: machineName })}
              </p>
            )}
            {tr.loading && !tr.items.length && !opening && (
              <div className="flex justify-center py-16">
                <Loading label={starting ? t("chat-chatview.preparing") : t("chat-chatview.loading_chat")} />
              </div>
            )}
            {tr.error && !tr.items.length && (
              <div className="flex flex-col items-center gap-3 py-16 text-center">
                <p className="text-[13px] text-ink-2">{tr.error}</p>
                <Button onClick={tr.reload} icon="retry">
                  {t("chat-chatview.retry")}
                </Button>
              </div>
            )}
            <LiveClockContext.Provider value={countClock}>
            <LatestMessageTime.Provider value={latestTime}>
            <ChatContext.Provider value={ctx}>
              <CardConversation.Provider value={assistant ? sid : ""}>
              {earlier.host && <Suspense fallback={null}><EarlierStretches {...earlier.host} offscreen={offscreen} /></Suspense>}
              {assistant
                ? displayLines(lines, feed, tr.hasMore ? loadedFrom(tr.items) : undefined).flatMap((d) => [
                    ...failedBefore(d.kind === "chat" ? d.id : ""),
                    d.kind === "feed" ? (
                      <FeedLineView key={d.id} item={d.item} simplified />
                    ) : d.kind === "quick" ? (
                      <QuickResultCard key={d.id} result={d.result} />
                    ) : d.kind === "day" ? (
                      <DayLine key={d.id} date={d.date} />
                    ) : d.line.id === bannerError?.id ? null : (
                      <ChatLineView
                        key={d.id}
                        line={d.line}
                        mid={mid}
                        sid={sid}
                        streaming={wordsFor(d.line)}
                        expandFold={tr.expandFold}
                        launcher={session?.launcher}
                        fix={d.line.id === lastLine?.id ? turnFix : undefined}
                        limit={d.line.kind === "apiError" && d.line.id === limitOn ? limitLine : undefined}
                        exhausted={d.line.kind === "apiError" && d.line.id === lastLine?.id ? exhausted : undefined}
                        assistant={assistant}
                        offscreen={offscreen(d.line)}
                        busy={d.line.id === busySegment || undefined}
                      />
                    ),
                  ])
                : chatRows.flatMap((row) => row.kind === "day" ? (
                  <div key={row.id} data-day-divider={row.id}><DayLine date={row.date} /></div>
                ) : row.kind === "mode" ? (
                  modes && <modes.kit.ModeChangeDivider key={row.id} change={row.change} session={session} />
                ) : [...failedBefore(row.line.id), open?.line === row.line.id ? (
                <OpenRecapLine key={row.line.id} open={open} onClose={dismissRecap} />
              ) : row.line.id === bannerError?.id ? null : (
                <ChatLineView
                  key={row.line.id}
                  line={row.line}
                  // only a live line streams: the others keep their memo
                  mid={mid}
                  sid={sid}
                  streaming={wordsFor(row.line)}
                  expandFold={tr.expandFold}
                  limit={row.line.kind === "apiError" && row.line.id === limitOn ? limitLine : undefined}
                  exhausted={row.line.kind === "apiError" && row.line.id === lastLine?.id ? exhausted : undefined}
                  assistant={assistant}
                  offscreen={offscreen(row.line)}
                  busy={row.line.id === busySegment || undefined}
                />
              )])}
              </CardConversation.Provider>
            </ChatContext.Provider>
            </LatestMessageTime.Provider>
            </LiveClockContext.Provider>
            {/* what is on its way, and failed messages with nothing sent after them, in the order they were written */}
            {[...shownPending.map((p) => ({ at: p.at, el: (
              <div key={p.id} className="flex flex-col gap-3">
                <UserBubble msg={p.msg} mid={mid} sid={sid} pending sendState="sending" />
              </div>
            ) })), ...placed.rest.map((e) => ({ at: e.at, el: <OutboxBubbles key={`outbox:${e.id}`} mid={mid} sid={sid} entries={[e]} canRetry={!refusedNoRow} /> })),
              ...keptUndelivered.map((m) => ({ at: Date.parse(m.at) || 0, el: (
                <PageBoundary key={`undelivered:${m.id}`} page="undelivered">
                  <Suspense fallback={<UserBubble msg={keptBubble(m)} mid={mid} sid={sid} sendState="failed" />}>
                    <UndeliveredBubble mid={mid} sid={sid} m={m} />
                  </Suspense>
                </PageBoundary>
              ) }))]
              .sort((a, b) => a.at - b.at)
              .map((x) => x.el)}
            {(shownPending.some((p) => p.waits) || processing || (sendFeedback !== undefined ? Boolean(sendFeedback) : shownPending.length > 0 || outbox.some((e) => e.state === "sending") || (starting && outgoing > 0))) && (
              <div className="flex items-center gap-2.5" data-message-feedback={sendFeedback || (processing ? "processing" : "sending")}>
                {sendFeedback === "unknown" ? <span role="status" className="text-ink-3">{t("lib-outbox.delivery_unknown")}</span> : <Loading label={shownPending.some((p) => p.waits) ? t("ultracode.send_waits") : sendFeedback === "processing" || processing ? t("chat-segmentview.processing") : starting ? t("chat-chatview.preparing") : online ? t("chat-segmentview.sending") : t("chat-chatview.waiting_for_hub")} />}
              </div>
            )}
            {voiceState && <VoiceTail owner={voiceOwner} persona={persona.name} />}
          </div>
          <CommentMarks scroller={scroller} jump={landRow} />
          </ChatActionsContext.Provider>
        </div>
        {/* A transient edge overlay; native scrolling and the centered text column keep their existing layout. */}
        {railSource.ticks.length > 0 && (
          <Suspense fallback={null}>
            <NavRail ticks={railSource.ticks} total={railSource.total} scroller={scrollEl} onJump={railSource.select} reset={`${mid}/${sid}`} />
          </Suspense>
        )}
        {away && !jumpInDock && <JumpPill label={jumpLabel(newSince)} onClick={toEnd} />}
        {picked && !search.open && <ReturnPill onClick={backToOrigin} />}
      </div>

      <div
        data-chat-dock
        // at most DOCK_MAX on every width (#5375): what sits over the composer gives up its height first and scrolls in
        // its own box, the chat above shrinks by what the dock takes and is never drawn over. Should the composer alone
        // need more (a keyboard leaving little room), it grows up over the chat (justify-end) instead of down off the screen.
        className="mx-auto flex max-h-(--dock-max) w-full max-w-[880px] flex-col justify-end gap-2 px-4 pb-4 sm:px-7 [@media(max-height:500px)]:pb-2"
        style={{ "--dock-max": DOCK_MAX } as React.CSSProperties}
        onInput={pendingRecap ? dismissRecap : undefined}
        onFocus={onDockFocus}
        onBlur={onDockBlur}
      >
        {/* everything over the composer: it shrinks and scrolls before the composer does (the composer grew, and its
            cards, task questions and notes stood taller than the dock, drawn up over the chat or pushing it to nothing);
            the box clips what is in it, so its children keep a little room for their shadows (p-1; -m-1 gives it back) */}
        <div ref={dockAbove} data-chat-dock-above data-scroll-box hidden={Boolean(picker)} className="-m-1 flex min-h-0 shrink flex-col gap-2 overflow-y-auto overscroll-contain p-1 empty:hidden">
        {/* a phone on its side: the plan's pill, the run's status and the queue share one line. One wrapper either way
            (no box of its own otherwise), so the pill and a flyout pinned on it stay when the phone turns */}
        {(pills || running || (short && queue)) && (
          <div className={short ? "flex shrink-0 items-center gap-2" : "contents"}>
            {pills && <div className={short ? "shrink-0" : "contents"}>{pills}</div>}
            {running && <div className={short ? "shrink-0" : "contents"}>{running}</div>}
            {short && queue && <div className="min-w-0 flex-1">{queue}</div>}
          </div>
        )}
        {assistant && (
          <>
            {voiceState === "live" && <TaskQuestions conversation={sid} />}
            {/* the cards this chat's own turn waits on, open above the capsules (also while a voice call is live); the cards
                of the sessions folded under it are in the chat under the bar (#4883, as iOS): Needs you counts them and
                lists a row for each that closes the tray onto its card. */}
            <TurnCards asks={turnSplit.turn} />
            {voiceState !== "live" && <AssistantActionBar key={sid} conversation={sid} ownAsks={trayAsks} />}
            {voiceState !== "live" && <RouteChip conversation={sid} />}
            {/* items handed to this conversation that are still open: "Wrap up" asks it for the final write-back (#5932) */}
            {voiceState !== "live" && isMain && <Suspense fallback={null}><LazyWrapUpChips conversation={sid} /></Suspense>}
          </>
        )}
        {pendingCards}
        {!assistant && session?.model_fallback && <Suspense fallback={null}><ModelFallbackCard session={session} headless={false} /></Suspense>}
        {assistant && session && <CompactionStrip session={session} />}
        {/* the failed bubble above has the one Retry; the card says what to set up first */}
        {failure ? <FailureCard failure={failure} onClose={() => setSendFailure(null)} onHelp={gate.help} onConfirm={gate.confirm} /> : <ReadinessCard gate={gate} />}
        <ChatOnlyNote gate={gate} />
        {gate.dialog}
        {session && !assistant && <AttentionNote session={session} />}
        {session && !assistant && <ConflictNote session={session} />}
        {/* a resume turned ultracode off (#3428): one line with "Turn back on" */}
        {session && !assistant && <UltracodeLostNote session={session} />}
        {session && failureNote && (
          <FailureNote
            session={session}
            onRetry={() => {
              const chat = `${mid}/${sid}`;
              const reason = failureReason(session);
              setRetried({ chat, reason });
              // a retry that did not go brings the note back
              void retryError().then((went) => {
                if (!went) setRetried((r) => (r?.chat === chat && r.reason === reason ? undefined : r));
              });
            }}
          />
        )}
        {voiceState === "ended" && <VoiceEnded owner={voiceOwner} />}
        {/* the error the chat stopped on, right over the composer as cindy places it (#5313); keyed by the error, so a new
            one opens with its details folded */}
        {bannerError && (
          <ErrorBanner
            key={errorKey(bannerError)}
            line={bannerError}
            assistant={assistant}
            limit={bannerError.id === limitOn ? limitLine : undefined}
            launcher={session?.launcher}
            exhausted={exhausted}
            fix={turnFix}
            onRetry={canRetry ? () => void retryError() : undefined}
            onEditLastMessage={onEditLastMessage}
            onDismiss={dismissBanner}
          />
        )}
        {/* the goal it works toward, right over the composer as cindy's goal indicator (#5421) */}
        {session?.goal && !assistant && <Suspense fallback={null}><GoalBar goal={session.goal} /></Suspense>}
        </div>
        {voiceEntry.dialog}
        {voiceState === "live" && voiceCall && (
          <div className="shrink-0">
            <VoiceBar call={voiceCall} session={session} cards={interactions} label={voiceLabel} persona={persona.name} onStopAgent={interrupt} />
          </div>
        )}
        {/* picking messages to forward (#3838): the bar in the composer's place, which stays with its draft */}
        {picker && pickParts.Bar && <pickParts.Bar pick={picker} chat={forwardFrom} onEnd={endPick} />}
        {/* kept while a call is on (its draft, attachments and quotes stay), the bar in its place */}
        <div ref={composerBox} className="shrink-0" hidden={voiceState === "live" || Boolean(picker)}>
        {stoppedLine ? (
          <StoppedAgentLine mid={mid} sid={sid} onTerminal={openTerminal} />
        ) : (
        <Composer
          draftKey={draftKey}
          placeholder={offlineLine ? offlineLine : waitsFirst ? t("chat-chatview.codex_first_placeholder") : refusedNoRow ? t(gone ? "chat-chatview.session_gone_placeholder" : "chat-chatview.connecting_placeholder") : viewOnlyRow(session) ? t("chat-chatview.view_only") : computerAway && awayLine ? awayLine : state === "working" || state === "needs_you" ? t("chat-chatview.queue_placeholder") : assistant ? askPlaceholder ?? t("chat-chatview.ask_placeholder", { name: persona.name }) : state === "offline" ? t("chat-chatview.resume_placeholder") : t("chat-chatview.agent_placeholder")}
          machineId={mid}
          uploadDest={session?.space === "health" ? "health" : undefined}
          working={state === "working" || awaiting}
          needsYou={state === "needs_you"}
          onSend={send}
          onSendNow={session && (session.capabilities?.queue_send_now ?? (assistant || (session.agent === "claude" && session.source_kind !== "runtime"))) ? sendQueueNow : undefined}
          disabled={Boolean(offlineLine) || (!noRow && !hydrating && !sessionCan(session, "send"))}
          onEditLastMessage={onEditLastMessage}
          editingLast={editProp}
          loadingDraft={editingLast}
          // Persona's conversations too, the main one included (#1467): the node stops their turn like an agent's
          onStop={sessionCan(session, "interrupt") ? interrupt : undefined}
          stopping={stopping}
          commands={commands}
          suggestion={session?.suggestion}
          suggestionRisky={session?.suggestion_risky}
          mention={mention}
          autoFocus
          carryDraft={Boolean(assistant)}
          onFocusChange={setTyping}
          blocked={gate.blocked ?? noRowBlock}
          leading={leading}
          trailing={trailing}
          planMode={planMode}
          voice={voiceEntry.offer}
          top={short ? undefined : queue}
        />
        )}
        {!stoppedLine && session && (
          assistant
            ? sourceSubtitle(session) && <div data-composer-usage className="px-2 pt-0.5"><ComposerSource session={session} assistant /></div>
            : (
              <Suspense fallback={null}>
                <AgentStatusLine key={draftKey} session={session} source={sourceSubtitle(session) ? <ComposerSource session={session} assistant /> : undefined} />
              </Suspense>
            )
        )}
        </div>
      </div>
      </ViewLayer>
      {switchable && termKept && (
        <ViewLayer on={view === "term"} from="end" enter layerRef={termLayer}>
          {/* Files opens beside it (the Files panel, as on the "Terminal" page); the page's header is the panel's, so the panel keeps a toolbar of its own controls ("Single pane" / "Full view"…) */}
          <PageBoundary page={`term:${mid}/${sid}`}>
            <Suspense fallback={<div className="flex h-full items-center justify-center"><Loading /></div>}>
              <TerminalPanel mid={mid} sid={sid} onFiles={session?.cwd ? openFiles : undefined} onClose={() => setView("chat")} embedded />
            </Suspense>
          </PageBoundary>
        </ViewLayer>
      )}
      </div>
      {session && overview?.open && (
        <PageBoundary page={`overview:${mid}/${sid}`}>
          <Suspense fallback={<div className="p-3"><Loading /></div>}>
            <SessionOverview id={overviewId} session={session} machineName={machineName} items={tr.items} tasks={tasks}
              children={kids.rows} onJumpToCall={jump} hidden={overviewDetail} />
          </Suspense>
        </PageBoundary>
      )}
      </div>

      <ChatSearch
        open={search.open}
        query={search.query}
        onQuery={(query) => setSearch((x) => ({ ...x, query }))}
        items={searchItems}
        foldsLoading={folds.loading}
        foldsPartial={folds.partial}
        rounds={rounds}
        hasMore={earlier.liveHasMore}
        onLoadEarlier={loadForSearch}
        onPick={goTo}
        onClose={closeSearch}
      />
      <Dialog
        open={Boolean(draftRefusal)}
        onClose={() => setDraftRefusal(null)}
        title={t("chat-chatview.draft_title")}
        footer={
          <>
            <Button tone="quiet" onClick={() => setDraftRefusal(null)}>
              {t("chat-chatview.not_now")}
            </Button>
            {draftRefusal?.clearable !== undefined && (
              <Button
                tone="primary"
                onClick={() => {
                  const r = draftRefusal!;
                  setDraftRefusal(null);
                  if (!refusedNoRow) void retryOutbox(mid, sid, r.id, r.clearable); // under a new id: the refused one keeps its answer
                }}
              >
                {t("chat-chatview.clear_and_send")}
              </Button>
            )}
          </>
        }
      >
        <p className="text-[13px] leading-relaxed text-ink-2">{t("chat-chatview.draft_detail")}{draftRefusal?.clearable === undefined && t("chat-chatview.draft_multiline")}</p>
        {draftRefusal?.draft && <pre className="mt-2.5 max-h-40 overflow-auto rounded-[8px] bg-inset px-2.5 py-2 font-mono text-[12px] whitespace-pre-wrap text-ink shadow-hairline">{draftRefusal.draft}</pre>}
      </Dialog>
    </div>
  );
  // a link tapped in the chat opens what it opens as the chat's manager
  return <LinkManager.Provider value={session?.manager}>{page}</LinkManager.Provider>;
}

/** Over the chat's top edge when the page keeps getting its items by asking for them, not by push (lib/stale-page.ts). */
function NotLivePill({ onReload, lower, error }: { onReload: () => void; /** The search's return pill is at the same edge: under it. */ lower?: boolean; error?: string }) {
  return (
    <div role="status" data-testid="chat-not-live" className={lower ? "absolute top-14 left-1/2 z-10 -translate-x-1/2" : "absolute top-3 left-1/2 z-10 -translate-x-1/2"} style={{ animation: "fade-up 150ms ease-out both" }}>
      <div className="flex h-8 items-center gap-2 rounded-full border border-line bg-surface px-3 text-[12px] leading-none whitespace-nowrap text-ink-2 shadow-raised">
        <span title={error}>{t(error ? "chat-chatview.refresh_failed" : "chat-chatview.not_live")}</span>
        <button type="button" onClick={onReload} className="touch-hit font-medium text-accent-ink hover:underline">
          {t("chat-chatview.not_live_reload")}
        </button>
      </div>
    </div>
  );
}

/** After going to a search hit: back to where the reader was when the search opened (Android ReturnChip). */
function ReturnPill({ onClick }: { onClick: () => void }) {
  return (
    <div className="absolute top-3 left-1/2 z-10 -translate-x-1/2" style={{ animation: "fade-up 150ms ease-out both" }}>
      <button
        type="button"
        onClick={onClick}
        data-search-return
        className="touch-hit flex h-8 items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-[12px] leading-none font-medium whitespace-nowrap text-ink-2 shadow-raised transition-colors duration-150 hover:bg-hover active:scale-[0.98]"
      >
        <Icon name="arrowLeft" size={14} className="shrink-0" />
        <span className="translate-y-[0.5px]">{t("chat-chatview.search_return")}</span>
      </button>
    </div>
  );
}

/** Away from the end: "New messages" / "Jump to bottom" over the chat's bottom edge (the reference client's JumpToBottomChip). */
function JumpPill({ label, onClick, inline }: { label: string; onClick: () => void; /** In a row (beside the plan's pill), not over the chat. */ inline?: boolean }) {
  return (
    <div aria-live="polite" className={inline ? "shrink-0" : "absolute bottom-3 left-1/2 z-10 -translate-x-1/2"} style={{ animation: "fade-up 150ms ease-out both" }}>
      <button
        type="button"
        onClick={onClick}
        aria-label={label}
        data-jump-pill
        className="touch-hit flex h-8 items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-[12px] leading-none font-medium whitespace-nowrap text-ink-2 shadow-raised transition-colors duration-150 hover:bg-hover active:scale-[0.98]"
      >
        <Icon name="arrowDown" size={14} className="shrink-0" />
        <span className="translate-y-[0.5px]">{label}</span>
      </button>
    </div>
  );
}

/** What the line under the composer says: where the chat comes from and, for an Agent, its folder (a Persona conversation
 *  shows no folder). The launcher and model are the composer's model pill (#5032, #5033). */
function composerSource(session: Session, assistant?: boolean): string[] {
  return [sourceSubtitle(session), ...(assistant ? [] : [shortPath(session.cwd)])].filter((p): p is string => Boolean(p));
}

/** Under the composer at the left (#5324, as cindy's working-folder line): the source and folder in muted 12 px, truncating,
 *  with the full values on hover. At phone width (#3533) it would show a letter or two, so it is a button that lists them. */
export function ComposerSource({ session, assistant }: { session: Session; assistant?: boolean }) {
  const parts = composerSource(session, assistant);
  const info = useMenu();
  if (!parts.length) return <span />;
  return (
    <span data-composer-source className="flex min-w-0 items-center gap-1 text-[12px] text-ink-3">
      <span className="flex min-w-0 items-center gap-1 max-sm:hidden" title={[sourceSubtitle(session), session.cwd].filter(Boolean).join(" · ")}>
        {!assistant && session.cwd && <Icon name="folder" size={12} className="shrink-0" />}
        <span className="min-w-0 truncate">{parts.join(" · ")}</span>
      </span>
      <IconButton icon="info" label={t("chat-chatview.session_info")} onClick={info.open} active={Boolean(info.anchor)} size={22} iconSize={13} className="sm:hidden" />
      {info.anchor && <Menu anchor={info.anchor} onClose={info.close} side="top" align="left" width={280} header={t("chat-chatview.session_info")} items={footerRows(session, assistant)} />}
    </span>
  );
}

/** The line under the composer as menu rows (#3533): the value in full (wrapped), what it is beside it; choosing one
 *  copies it, except the source, which is a sentence to read rather than a value to paste. The launcher and model are
 *  the model pill's (#5032). */
export function footerRows(session: Session, assistant?: boolean): MenuItem[] {
  const rows: [string, string, string | undefined][] = [
    ["source", t("chat-chatview.composer_source"), sourceSubtitle(session)],
    ...(assistant ? [] : ([["folder", t("chat-sessiondialogs.folder"), shortPath(session.cwd)]] as [string, string, string | undefined][])),
  ];
  return rows.filter(([, , v]) => v).map(([key, what, v]) => ({ key, label: v!, hint: what, wrap: true, onSelect: key === "source" ? () => {} : () => void copyText(key === "folder" ? session.cwd! : v!) }));
}

/** The session is live in another engine host too (#2576): this one owns it, and messages go here only. */
function ConflictNote({ session }: { session: Session }) {
  const text = alsoOpenText(session);
  if (!text) return null;
  return (
    <div role="status" data-testid="chat-also-open" className="flex items-start gap-2 rounded-[12px] bg-orange-tint px-3 py-2 text-[12.5px]" style={{ animation: "fade-up 200ms ease-out both" }}>
      <span className="flex h-[18px] shrink-0 items-center text-orange"><Icon name="warning" size={13} /></span>
      <span className="line-clamp-2 min-w-0 flex-1 text-ink-2">{text}</span>
    </div>
  );
}

/** Above the composer while a session needs you and its host says why (a botmux attention, iOS AttentionNote):
 *  the reason and "Open in Lark" its topic. */
function AttentionNote({ session }: { session: Session }) {
  const reason = attentionReason(session);
  if (!reason) return null;
  const link = externalLink(session);
  return (
    <div role="status" data-testid="chat-attention" className="flex items-start gap-2 rounded-[12px] border-l-2 border-attention bg-inset px-3 py-2 text-[12.5px]" style={{ animation: "fade-up 200ms ease-out both" }}>
      <span className="flex h-[18px] shrink-0 items-center"><StatusMark state="needs_you" /></span>
      <span className="line-clamp-3 min-w-0 flex-1 text-ink-2">{reason}</span>
      {link && (
        <a href={link} target="_blank" rel="noopener noreferrer" className="flex shrink-0 items-center gap-1 whitespace-nowrap font-medium text-accent hover:underline">
          <Icon name="external" size={12} />
          {t("chat-chatview.open_in_lark")}
        </a>
      )}
    </div>
  );
}

// memo: a chat renders again for each transcript item, and the header shows none of them
// One line, as cindy's session header (`SessionContentHeader`, #5421): left to right the pin mark, the /loop mark (cindy's
// schedule badge), the Agent's mark ("Agent runs on {computer}"), the title (double-click renames it in place), ⋯ and the
// git branch; at the right end the side panels' buttons (Background tasks, Team, Files) and the terminal view, where
// cindy keeps its right sidebar's. The state is said above the composer (the running status bar) and in the sidebar,
// the model is the composer's pill, the permission mode the composer's picker; the rest is in ⋯ and Session info.
export const Header = memo(function Header({
  session,
  starting = false,
  interactions = [],
  overview,
  title: baseTitle,
  machineName,
  assistant,
  main,
  siblings,
  onToggleSidebar,
  onInterrupt,
  last,
  onSearch,
  topCenter,
  view,
  onView,
  onTerminal,
  viewDot,
  termDot,
  actions,
  tasks,
  team,
  sides,
  panel: chrome,
  direction = false,
}: {
  session?: Session;
  starting?: boolean;
  interactions?: Interaction[];
  overview?: { id: string; open: boolean; toggle: () => void };
  title: string;
  machineName: string;
  assistant?: boolean;
  /** The assistant's main conversation: no "Rename". */
  main?: boolean;
  /** A leader conversation, its title its direction's name: no in-place rename of that title (it would rename the
   *  conversation's own title, which the header does not show); ⋯ Rename does that, as the sidebar row's does. */
  direction?: boolean;
  siblings: Session[];
  onToggleSidebar?: () => void;
  onInterrupt: () => void;
  /** The assistant's face: the last item's role and kind. */
  last?: Pick<Item, "role" | "kind">;
  /** "Search in chat" (ChatSearch.tsx). */
  onSearch?: () => void;
  topCenter?: React.ReactNode;
  /** On an agent's page with a terminal to show: the view in the middle, and how to change it (the terminal button). */
  view?: SessionView;
  onView?: (view: SessionView) => void;
  /** Shows the agent's terminal as its stopped line's Terminal does (in place, or its page's terminal view). */
  onTerminal?: () => void;
  /** The conversation has something that wants an answer while the terminal is shown. */
  viewDot?: boolean;
  /** The terminal waits on a dialog while the conversation is shown (lib/session-view.ts terminalWaits, #7343). */
  termDot?: boolean;
  /** The page's own buttons, at the end of the right side (ChatView `headerActions`). */
  actions?: React.ReactNode;
  /** The conversation's tasks panel: a button from 640 px, a row of ⋯ on a phone (the title keeps its room there). */
  tasks?: { open: boolean; onToggle: () => void };
  /** A lead with workers (#5180): its Team panel's button, whether the panel is open, and whether a worker needs you. */
  team?: { open: boolean; needs: boolean; onToggle: () => void };
  /** A session with side sessions (#7944): how many, whether its Side sessions panel is open, whether one needs you. */
  sides?: { count: number; open: boolean; needs: boolean; onToggle: () => void };
  panel?: PanelChrome;
}) {
  const preferTab = useHerdrTabName();
  const primaryTab = session && !assistant && !direction ? sessionDisplayTab(session, preferTab) : "";
  const title = primaryTab || baseTitle;
  const titleDetail = primaryTab && session ? sessionTitleDetail(session, preferTab) : "";
  const identity = useAssistantIdentity();
  const layout = useLayout();
  const narrow = useMedia(NARROW);
  const phone = useMedia(PHONE);
  // A rail and a phone drawer have no assistant head beside the page. Older hubs have a product head instead.
  const headBesidePage = !narrow && sidebarShown(layout) && !sidebarRail(layout) && identity !== null;
  const menu = useMenu();
  // the ⋯ menu's Rename…, Rename space… and Delete… open dialogs from a chunk of their own: on its way once it opens (#10382)
  useEffect(() => {
    if (menu.anchor) preloadSessionDialogs();
  }, [menu.anchor]);
  const tabs = useMenu();
  const panel = usePanel();
  const mutes = useStore((s) => s.mutes);
  const features = useStore((s) => s.features);
  // archived (the Archived page listed it, or it was archived here): the menu offers Restore instead of archive / delete
  const archived = useArchived(session?.machine_id ?? "", session?.session_id ?? "");
  const [dialog, setDialog] = useState<"info" | "rename" | "space" | "delete" | null>(null);
  // the background supervisor is built in (#5082): not renamed here either, as in its row's menu
  const builtIn = useStore((s) => isBackgroundSupervisor(s.assistant, session?.session_id));
  const muted = Boolean(session && !assistant && isMuted(session, mutes));
  const pinned = useStore((s) => Boolean(session && isPinned(session, s.pins)));
  // "Mute": the hub stops pushing this agent's "Needs you" / "Finished" (an agent's switch sits on top of its space's)
  const flipMute = async () => {
    if (!session) return;
    const next = toggleMute(session, getState().mutes);
    try {
      await saveMutes(next);
      toast(isMuted(session, next) ? t("chat-chatview.muted") : t("chat-chatview.unmuted"), "ok");
    } catch (e) {
      toast(t("chat-chatview.change_failed", { error: errorText(e) }), "error");
    }
  };
  const [linkTodoOpen, setLinkTodoOpen] = useState(false);
  const [sharingOpen, setSharingOpen] = useState(false);
  const allowed = session ? sessionMenuActions(session) : [];
  // Live link loads with the click (lib/attach-live.ts), off the first screen
  const attach = () => session && import("../../lib/attach-live").then((m) => m.attachLive(session));
  // Resume in terminal (#5521): a stopped Agent comes back in a terminal on its computer. The same action as its stopped
  // line's Resume: the terminal shown as that line's Terminal shows it, then the Resume both share.
  const machine = useStore((s) => s.machines.find((m) => m.id === session?.machine_id));
  const resumeOffer = onTerminal ? terminalResume(session, machine) : "hidden";
  const resumeHere = () => {
    if (session && onTerminal) void resumeInTerminal(session.machine_id, session.session_id, onTerminal);
  };
  // a task's or an Agent's side panel leaves the space's other agents out of ⋯
  const others = chrome ? [] : siblings.filter((s) => s.session_id !== session?.session_id);
  // the session's files and Git open in the side panel (docs/web/web.md "Side panel"); the assistant has none. Its terminal is
  // the page's other view, switched in place (view / onView)
  const here = (p: typeof panel, kind: string) => Boolean(p && p.kind === kind && "sid" in p && session && p.mid === session.machine_id && p.sid === session.session_id);
  const showTerminal = Boolean(view && onView);
  // the side panel shows no terminal in place: its terminal button opens the agent's page in the terminal view, and the
  // panel closes, its chat being that page now (#7343)
  const jumpTerminal = !showTerminal && Boolean(onTerminal) && terminalJump(session, { inPanel: Boolean(chrome), assistant });
  const openTerminalPage = () => {
    onTerminal?.();
    chrome?.onClose();
  };
  // the panel's Agent as its own page, the page it has in the Agents list; leaveChat keeps the conversation's place for
  // Back, as the "Full page" way of opening Agents does (lib/agent-open.ts). The panel closes, its chat being that page now.
  const openAgentPage = () => {
    if (!chrome?.page) return;
    leaveChat(chrome.page, true, getPanel() ?? undefined);
    chrome.onClose();
  };
  const showFiles = Boolean(session && !assistant && session.cwd);
  const flipView = () => view && onView?.(otherView(view));
  const openFiles = () => session && togglePanel({ kind: "files", mid: session.machine_id, sid: session.session_id, cwd: session.cwd });
  const openGit = () => session && togglePanel({ kind: "git", mid: session.machine_id, sid: session.session_id, cwd: session.cwd });
  // the session's dev server, from a browser on its computer (docs/web/preview.md); a hub without previews offers none
  const previews = useStore((s) => hubHas(s.features, PREVIEW_FEATURE));
  const openPreview = () => session && togglePanel({ kind: "preview", mid: session.machine_id, sid: session.session_id, cwd: session.cwd });
  const openReview = () => session && togglePanel({ kind: "review", mid: session.machine_id, sid: session.session_id, cwd: session.cwd });
  const reviews = useStore((s) => hubHas(s.features, REVIEW_FEATURE));
  const [sessionReviewItems, setSessionReviewItems] = useState<MenuItem[]>([]);
  // the comparison this Agent is a run of (#8916, kept in this browser): its module and words load after the first screen,
  // long before ⋯ opens; re-read with the header
  const [compareOf, setCompareOf] = useState<null | ((s: Session) => { id: string; label: string } | undefined)>(null);
  useEffect(() => {
    if (assistant) return;
    let live = true;
    void import("../../lib/compare-runs").then((m) => { if (live) setCompareOf(() => m.compareMenu); }, () => {});
    return () => { live = false; };
  }, [assistant]);
  const compare = session && compareOf ? compareOf(session) : undefined;
  const othersNeed = others.some((s) => stateOf(s) === "needs_you");
  // the Agent's background work (#5421): the button shows while there is some, the loop mark while a /loop drives it
  const now = useNow(30_000, Boolean(session?.loop && !assistant));
  const running = session && !assistant ? backgroundCount(session) : 0;
  // a runtime's session renames where its node says it can (capabilities.rename, #6294), as Close goes by capabilities.close
  const canRename = Boolean(session && !main && !builtIn && sessionCanRename(session));
  const [editing, setEditing] = useState(false);
  const spaceRows: MenuItem[] = siblings
    .slice()
    .sort((a, b) => (a.slot_index ?? 0) - (b.slot_index ?? 0))
    .map((s) => ({
      key: s.session_id,
      label: `${tabLabel(s) || sessionTitle(s)} · ${sessionTitle(s)}`,
      state: stateOf(s),
      checked: s.session_id === session?.session_id,
      onSelect: () => navigate({ name: "session", mid: s.machine_id, sid: s.session_id }),
    }));
  const { items: assistantItems, dialogs: assistantDialogs } = useAssistantMenu(assistant ? session : undefined);
  const feishu = externalLink(session);
  const linkedTodo = session?.todo ?? session?.completed_todo;

  // Conversation tasks' button gives way on a phone: ⋯ has it there, also before the session is in
  const tasksItem: MenuItem[] = tasks && phone ? [{ key: "conversation-tasks", label: t("sidepanel.conversation_tasks"), icon: "todo", checked: tasks.open, onSelect: tasks.onToggle }] : [];
  const own: MenuItem[] = session
    ? [
        // the space's other agents (#5421: in ⋯ at every width, as cindy's header has no such button)
        ...(others.length > 0 ? [spaceAgentsItem(others, () => tabs.openAt(menu.anchor))] : []),
        ...assistantItems,
        ...(!assistant && !main ? [{ key: "share", label: t("chat-chatview.share"), icon: "link" as const, onSelect: () => setSharingOpen(true) }] : []),
        ...(linkedTodo
          ? [
              {
                key: "todo-detail",
                label: t(session.todo ? "chat-chatview.todo_label" : "chat-chatview.todo_completed_label", { title: linkedTodo.title }),
                icon: "todo" as const,
                onSelect: () => navigate(`/todos?id=${encodeURIComponent(linkedTodo.id)}`),
              },
              ...(session.todo ? [
              {
                key: "unlink-todo",
                label: t("chat-chatview.unlink_todo"),
                icon: "x" as const,
                onSelect: async () => {
                  try {
                    await unlinkAgentFromTodo(linkedTodo.id, session.machine_id, session.session_id);
                    await refreshTodos();
                    await refresh();
                    toast(t("chat-chatview.todo_unlinked"), "ok");
                  } catch (e) {
                    toast(t("chat-chatview.unlink_failed", { error: errorText(e) }), "error");
                  }
                },
              },
              {
                key: "relink-todo",
                label: t("chat-chatview.relink_todo"),
                icon: "todo" as const,
                onSelect: () => setLinkTodoOpen(true),
              },
              ] : []),
            ]
          : !assistant
            ? [
                {
                  key: "link-todo",
                  label: t("chat-chatview.link_todo"),
                  icon: "todo" as const,
                  onSelect: () => setLinkTodoOpen(true),
                },
              ]
            : []),
        ...(allowed.includes("attach") ? [{ key: "attach", label: t("chat-chatview.attach"), icon: "link" as const, onSelect: () => void attach() }] : []),
        // its Side sessions panel (#7944), as the header's button
        ...(sides ? [{ key: "sides", label: t("chat-chatview.side_sessions_count", { count: sides.count }), icon: "sideSessions" as const, checked: sides.open, onSelect: sides.onToggle }] : []),
        ...(showTerminal ? [{ key: "terminal", label: t("chat-chatview.terminal"), icon: "terminal" as const, checked: view === "term", onSelect: flipView }] : []),
        ...(jumpTerminal ? [{ key: "terminal", label: t("chat-chatview.terminal"), icon: "terminal" as const, onSelect: openTerminalPage }] : []),
        ...(resumeOffer !== "hidden"
          ? [{ key: "resume-terminal", label: t("chat-chatview.resume_in_terminal"), icon: "play" as const, disabled: resumeOffer === "no_engine", detail: resumeOffer === "no_engine" ? t("chat-chatview.resume_no_engine", { computer: machineName }) : undefined, onSelect: resumeHere }]
          : []),
        ...(showFiles ? [{ key: "files", label: t("chat-chatview.files"), icon: "folder" as const, checked: here(panel, "files"), onSelect: openFiles }, { key: "git", label: "Git", icon: "git" as const, checked: here(panel, "git"), onSelect: openGit }, ...(reviews ? [{ key: "review", label: t("chat-chatview.review_changes"), icon: "pullRequest" as const, checked: here(panel, "review"), onSelect: openReview }] : [])] : []),
        ...tasksItem,
        ...sessionReviewItems,
        // a run of a comparison (#8916): back to its runs' page
        ...(compare ? [{ key: "compare", label: compare.label, icon: "branch" as const, onSelect: () => navigate({ name: "compare", id: compare.id }) }] : []),
        ...(showFiles && previews ? [{ key: "preview", label: t("chat-chatview.preview"), icon: "web" as const, checked: here(panel, "preview"), onSelect: openPreview }] : []),
        ...(allowed.includes("stop") ? [{ key: "stop", label: t("chat-chatview.interrupt"), icon: "stop" as const, hint: "Esc", onSelect: onInterrupt }] : []),
        ...(feishu ? [{ key: "feishu", label: t("chat-chatview.open_in_lark"), icon: "external" as const, onSelect: () => openLink(feishu) }] : []),
        ...(onSearch ? [{ key: "search", label: t("chat-chatview.search_menu"), icon: "search" as const, onSelect: onSearch }] : []),
        // the whole conversation as Markdown (#7375): its dialog is a chunk of its own
        { key: "export", label: t("chat-chatview.export"), icon: "download", onSelect: () => import("./ExportDialog").then((m) => m.openExportDialog({ session, title, machineName, assistant: Boolean(assistant) }), (e: unknown) => toast(errorText(e), "error")) },
        { key: "info", label: t("chat-chatview.session_info"), icon: "info", onSelect: () => setDialog("info") },
        ...(canRename ? [{ key: "rename", label: t("chat-chatview.rename"), icon: "pencil" as const, onSelect: () => setDialog("rename") }] : []),
        ...(!assistant && session.space_id && !session.source_kind ? [{ key: "space", label: t("chat-chatview.rename_space"), icon: "folder" as const, onSelect: () => setDialog("space") }] : []),
        ...(!assistant ? [{ key: "mute", label: muted ? t("chat-chatview.unmute") : t("chat-chatview.mute"), icon: muted ? ("bell" as const) : ("bellOff" as const), onSelect: () => void flipMute() }] : []),
        ...(!assistant ? [{ key: "pin", label: pinned ? t("chat-chatview.unpin") : t("chat-chatview.pin"), icon: "pin" as const, onSelect: () => void flipPin(session) }] : []),
        { key: "sid", label: t("chat-chatview.copy_session_id"), icon: "copy", onSelect: () => copyText(session.session_id) },
        ...(session.cwd ? [{ key: "cwd", label: t("chat-chatview.copy_folder"), icon: "folder" as const, onSelect: () => copyText(session.cwd) }] : []),
        ...(allowed.includes("resume") ? [{ key: "resume", label: t("chat-chatview.copy_resume_command"), icon: "terminal" as const, onSelect: () => copyText(resumeCommand(session)) }] : []),
        // Archive and Close… as the row's menu has them (#7125); archived, Restore, and Close… while its agent is still open
        ...(archived
          ? [{ key: "restore", label: t("chat-chatview.restore"), icon: "undo" as const, divider: true, onSelect: () => void restoreAgent(archived, appArchiveDeps) }, ...closeItems({ ...session, archived: true }, features)]
          : archiveCloseItems(session, features)),
        ...(!archived && offerSessionDelete(session, features) ? [{ key: "delete", label: t("chat-chatview.delete_chat"), icon: "trash" as const, danger: true, onSelect: () => setDialog("delete") }] : []),
      ]
    : tasksItem;
  // a side panel's own rows (Open in new window, Remove task…) at the top of ⋯
  const panelItems = chrome?.items ?? [];
  const items: MenuItem[] = panelItems.length > 0 && own.length > 0 ? [...panelItems, { ...own[0], divider: true }, ...own.slice(1)] : [...panelItems, ...own];
  // ⋯ follows the title (cindy's "More actions"); on a narrow page whose middle is the shell's switch the title is hidden,
  // and ⋯ goes to the right end instead
  const more = (where: "title" | "end") =>
    items.length > 0 && (
      <span className={cx("relative flex shrink-0", where === "end" ? "lg:hidden" : Boolean(topCenter) && "max-lg:hidden")}>
        <IconButton icon="more" label={t("chat-chatview.more_actions")} onClick={menu.open} active={Boolean(menu.anchor)} className="text-ink-3" />
        {othersNeed && <span aria-hidden className="pointer-events-none absolute top-0.5 right-0.5 size-2 rounded-full bg-attention shadow-[0_0_0_2px_var(--page)]" />}
      </span>
    );
  const branch = !assistant ? session?.branch?.trim() : "";
  const titleClass = "min-w-0 text-[14px] font-medium text-ink";
  const sideLabel = sides ? [t("chat-chatview.side_sessions_count", { count: sides.count }), sides.needs ? t("chat-chatview.side_sessions_needs_you") : ""].filter(Boolean).join(" · ") : "";

  return (
    <>
      {menu.anchor && <SessionReviewMenuItem onItem={setSessionReviewItems} session={assistant ? undefined : session} busy={starting} ix={interactions} />}
      <header className="@container flex h-14 shrink-0 items-center gap-2 border-b border-line bg-page/85 px-3 backdrop-blur-md sm:px-4">
      <div className={headerSlot(Boolean(topCenter), "start")}>
        {onToggleSidebar && <ChatSidebarButton session={session} counted={!topCenter} onClick={onToggleSidebar} />}
      </div>
      <div data-chat-title-row className={cx("flex min-w-0 flex-1 items-center gap-0.5", Boolean(topCenter) && "max-lg:hidden")}>
        {pinned && !assistant && <span role="img" aria-label={t("chat-chatview.pinned")} title={t("chat-chatview.pinned")} className="mr-1 flex shrink-0 text-ink-3"><Icon name="pin" size={13} /></span>}
        {session?.loop && !assistant && <LoopMark session={session} now={now} onOpen={() => openBackgroundPanel(session.machine_id, session.session_id)} />}
        {session && !assistant && <AgentDeviceMark session={session} machineName={machineName} />}
        {assistant && !headBesidePage && <span data-chat-face className="mr-1.5 flex shrink-0"><AssistantFace session={session} last={last} /></span>}
        {editing && session ? (
          <TitleEditor session={session} title={primaryTab ? session.title || baseTitle : title} onDone={() => setEditing(false)} />
        ) : assistant && main && items.length > 0 ? (
          <h1 className={titleClass}>
            <button type="button" data-chat-title data-assistant-name-menu aria-haspopup="menu" aria-expanded={Boolean(menu.anchor)} onClick={menu.open} className="touch-hit relative flex max-w-full items-center gap-1 whitespace-nowrap rounded-full px-2 py-1 hover:bg-hover focus-visible:bg-hover">
              <span className="truncate">{chrome?.title || title}</span>
              <Icon name="chevron" size={12} className="shrink-0 text-ink-3" />
            </button>
          </h1>
        ) : (
          <h1
            onDoubleClick={canRename && !chrome?.title && !direction ? () => preferTab && primaryTab ? setDialog("rename") : setEditing(true) : undefined}
            title={chrome?.title ? [chrome.title, chrome.sub].filter(Boolean).join(" · ") : [title, titleDetail].filter(Boolean).join("\n")}
            data-chat-title
            className={cx(titleClass, "cursor-default truncate select-none")}
          >
            {chrome?.title || title}
          </h1>
        )}
        {!editing && chrome?.beside}
        {!editing && !(assistant && main) && more("title")}
        {!editing && branch && (
          <span title={t("chat-chatview.branch_title", { branch })} data-chat-branch className="ml-1.5 flex min-w-0 max-w-44 shrink items-center gap-1 rounded-full px-1.5 py-0.5 text-[12px] text-ink-3 @max-[800px]:hidden">
            <Icon name="branch" size={12} className="shrink-0" />
            <span className="truncate">{branch}</span>
          </span>
        )}
      </div>
      {topCenter && <div className="flex min-w-0 flex-1 justify-center lg:hidden">{topCenter}</div>}
      <div className={headerSlot(Boolean(topCenter), "end")}>
      {overview && (
        <button
          type="button"
          data-session-overview-trigger
          aria-label={t("chat-chatview.session_overview")}
          aria-pressed={overview.open}
          aria-description={running > 0 ? t("chat-chatview.background_running", { count: running }) : undefined}
          aria-controls={overview.open ? overview.id : undefined}
          title={t("chat-chatview.session_overview")}
          onClick={overview.toggle}
          className={cx("touch-hit relative flex size-7 shrink-0 items-center justify-center rounded-control transition-colors hover:bg-hover hover:text-ink", overview.open ? "bg-hover text-ink" : "text-ink-3")}
        >
          <Icon name="list" size={15} />
          {running > 0 && !overview.open && (
            <span aria-hidden data-background-count className="pointer-events-none absolute -top-1 -right-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-field px-1 text-[10px] font-semibold text-ink-2 tabular-nums shadow-[0_0_0_2px_var(--page)]">{running}</span>
          )}
        </button>
      )}
      {team && (
        <span className="relative flex shrink-0">
          <IconButton icon="users" label={team.needs && !team.open ? t("chat-chatview.team_needs_you") : t("chat-chatview.team")} onClick={team.onToggle} active={team.open} />
          {team.needs && !team.open && <span aria-hidden className="pointer-events-none absolute top-0.5 right-0.5 size-2 rounded-full bg-attention shadow-[0_0_0_2px_var(--page)]" />}
        </span>
      )}
      {sides && (
        // its side sessions (#7944): the mark and how many, on one line; the attention dot while one needs you
        <span className="relative flex shrink-0">
          <button
            type="button"
            aria-label={sideLabel}
            title={sideLabel}
            aria-expanded={sides.open}
            data-side-sessions={sides.count}
            onClick={sides.onToggle}
            className={cx(
              "touch-hit flex h-7 shrink-0 items-center gap-1 rounded-[8px] px-1.5 text-label leading-none font-medium whitespace-nowrap tabular-nums transition-[background-color,color,transform] duration-150 active:scale-[0.94]",
              sides.open ? "bg-hover text-ink" : "text-ink-3 hover:bg-hover hover:text-ink",
            )}
          >
            <Icon name="sideSessions" size={15} />
            <span>{sides.count}</span>
          </button>
          {sides.needs && <span aria-hidden data-side-sessions-needs className="pointer-events-none absolute top-0.5 right-0.5 size-2 rounded-full bg-attention shadow-[0_0_0_2px_var(--page)]" />}
        </span>
      )}
      {showFiles && <IconButton icon="folder" label={t("chat-chatview.files")} onClick={openFiles} active={here(panel, "files") || here(panel, "git")} className="hidden sm:flex" />}
      {/* the terminal: one button to the other view at every width (cindy's terminal is one of its right sidebar's tabs) */}
      {view && onView && (
        <span className="relative flex shrink-0">
          <IconButton icon={view === "chat" ? "terminal" : "chat"} label={t(view === "chat" ? "chat-chatview.terminal" : "chat-chatview.chat")} onClick={flipView} />
          {(view === "term" ? viewDot : termDot) && <span aria-hidden data-terminal-waits={view === "chat" || undefined} className="pointer-events-none absolute top-0.5 right-0.5 size-1.5 rounded-full bg-attention" />}
        </span>
      )}
      {jumpTerminal && (
        <span className="relative flex shrink-0">
          <IconButton icon="terminal" label={t("chat-chatview.terminal")} onClick={openTerminalPage} />
          {termDot && <span aria-hidden data-terminal-waits className="pointer-events-none absolute top-0.5 right-0.5 size-1.5 rounded-full bg-attention" />}
        </span>
      )}
      {/* wide screens have New chat at the top of the sidebar; a phone's sidebar is a drawer */}
      {assistant && !chrome && session?.space !== "health" && <IconButton icon="compose" label={t("chat-chatview.new_chat")} onClick={() => navigate({ name: "persona-new" })} className="lg:hidden" />}
      {tasks && <IconButton icon="todo" label={t("sidepanel.conversation_tasks")} active={tasks.open} onClick={tasks.onToggle} className="hidden sm:flex" />}
      {!chrome && actions}
      {!assistant && session && (session.agent === "codex" || session.source_kind === "codex_history" || session.source_kind === "codex_app_server") &&
        <Suspense fallback={null}><TakeoverAction key={`${session.machine_id}:${session.session_id}`} session={session} /></Suspense>}
      {Boolean(topCenter) && more("end")}
      {chrome?.page && <IconButton icon="chat" label={t("chat-chatview.open_agent_page")} onClick={openAgentPage} />}
      {chrome && <IconButton icon="x" label={t("sidepanel.close")} onClick={chrome.onClose} />}
      </div>
      {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} align={topCenter && mediaMatches(NARROW) ? "right" : "left"} items={items} />}
      {tabs.anchor && (
        <Menu
          anchor={tabs.anchor}
          onClose={tabs.close}
          align={topCenter && mediaMatches(NARROW) ? "right" : "left"}
          width={360}
          header={session?.space_name ? t("chat-chatview.space_header", { name: session.space_name }) : undefined}
          headerAsWritten
          items={spaceRows}
        />
      )}
      {session && <SessionInfoDialog open={dialog === "info"} onClose={() => setDialog(null)} session={session} machineName={machineName} />}
      {session && <RenameDialog open={dialog === "rename"} onClose={() => setDialog(null)} session={session} />}
      {session && <RenameSpaceDialog open={dialog === "space"} onClose={() => setDialog(null)} session={session} />}
      {session && <DeleteSessionDialog open={dialog === "delete"} onClose={() => setDialog(null)} session={session} machineName={machineName} />}
      <LinkTodoDialog open={linkTodoOpen} session={session} onClose={() => setLinkTodoOpen(false)} />
      {session && sharingOpen && !assistant && !main && <SharingDialog key={`${session.machine_id}/${session.session_id}/${session.manager ?? ""}`} session={session} onClose={() => setSharingOpen(false)} />}
      {assistantDialogs}
    </header>
    </>
  );
});

/** Before the title (#5421, cindy's AgentDeviceIndicator): the Agent's mark with the signal arc of "on another computer" (an
 *  Agent always runs on one of your computers, never in this page), "Agent runs on {computer}" on hover. While that
 *  computer is offline the mark dims and the hover says it goes on once the computer is back. */
export function AgentDeviceMark({ session, machineName }: { session: Session; machineName: string }) {
  const offline = useStore((s) => s.machines.find((m) => m.id === session.machine_id)?.online === false);
  const where = t(offline ? "chat-chatview.agent_device_offline" : "chat-chatview.agent_device", { device: machineName });
  // the hover is cindy's sentence; the spoken name says which Agent too, as the mark alone did
  return (
    <span role="img" aria-label={t("chat-chatview.agent_device_label", { agent: sessionMarkName(session), where })} title={where} data-agent-device={offline ? "offline" : "online"} className={cx("mr-2 inline-flex shrink-0", offline && "opacity-75")}>
      <SessionMark s={session} size={14} decorative remote className="text-ink-3" />
    </span>
  );
}

/** Before the title while a /loop drives the Agent (#5421, cindy's schedule badge): the loop's mark; its cadence and next
 *  wake-up on hover; a click opens the Background tasks panel, where the loop is and stops. */
function LoopMark({ session, now, onOpen }: { session: Session; now: number; onOpen: () => void }) {
  const loop = session.loop!;
  const hint = [t("chat-chatview.loop_mark"), loopCadence(loop), loopNext(loop, now)].filter(Boolean).join(" · ");
  return (
    <button type="button" onClick={onOpen} aria-label={hint} title={hint} data-loop-mark className="mr-1 flex size-4 shrink-0 items-center justify-center rounded-[4px] text-ink-3 transition-colors hover:text-ink">
      <Icon name="loop" size={13} />
    </button>
  );
}

/** The title being renamed in place (#5421, cindy's SessionRenameInput): Enter or leaving the field saves, Esc cancels
 *  (and keeps Esc from stopping the Agent). An unchanged or empty title saves nothing. */
function TitleEditor({ session, title, onDone }: { session: Session; title: string; onDone: () => void }) {
  const [value, setValue] = useState(title);
  const done = useRef(false);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, []);
  const commit = () => {
    if (done.current) return;
    done.current = true;
    onDone();
    if (value.trim() && value.trim() !== title) void renameTitle(session, value);
  };
  return (
    <input
      ref={input}
      value={value}
      aria-label={t("chat-chatview.rename_title")}
      data-chat-title-input
      onChange={(e) => setValue(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.nativeEvent.isComposing) return;
        if (e.key === "Enter") {
          e.preventDefault();
          commit();
        } else if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          done.current = true;
          onDone();
        }
      }}
      className="h-7 w-72 max-w-[40vw] min-w-0 rounded-control bg-surface px-2 text-[14px] font-medium text-ink shadow-btn outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]"
    />
  );
}

/** The ⋯ row for the space's other agents (#5421: the header has no "Same space" button at any width): it opens their menu
 *  (`open`, at ⋯), red when one of them needs you. */
export function spaceAgentsItem(others: Session[], open: () => void): MenuItem {
  return {
    key: "space-agents",
    label: t("chat-chatview.same_space_count", { count: others.length }),
    icon: "layers",
    state: others.some((s) => stateOf(s) === "needs_you") ? "needs_you" : undefined,
    onSelect: open,
  };
}

/** The header's side slots around a switch in its middle (narrow screens, header.ts); wide, or without one, no boxes
 *  at all. */
function headerSlot(centered: boolean, side: "start" | "end"): string {
  if (!centered) return "contents";
  return cx(HEADER_SLOT, side === "start" ? HEADER_SLOT_START : HEADER_SLOT_END, "lg:contents");
}
