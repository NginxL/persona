// The requests graph: the To-dos page's "By request" grouping (lib/request-graph.ts). Every request the owner made as a
// small DAG drawn left to right: the owner's words, then each piece of work in layers by what it waits for (thin
// connectors with a hollow dot where they leave a card; dashed into a piece still queued). A piece says where it stands
// and which Agent is on it; hovering or focusing it lights it, what it waits for and what waits for it, and dims the
// rest. A piece with a to-do opens that to-do on the page, as a row does. A piece with more than one Agent grows its
// Agent tree under its card (the leader, the workers it handed out, what they spawned and their subagents, ended ones
// grey with a check), each line opening its Agent. Finished requests fold to one line each under "Done"; the pieces
// still queued are listed under the graph. Everything is the hub's own data.
import { useCallback, useMemo, useState, type CSSProperties } from "react";
import { t } from "../i18n";
import { agentName, sessionAgent } from "../lib/agent-kind";
import { useDirectionTree } from "../lib/direction-tree";
import { useFeed } from "../lib/feed";
import { clock, sessionTitle, shortDuration } from "../lib/format";
import { machineWithHub } from "../lib/mesh";
import {
  buildGraph, CARD_H, CARD_W, layoutRequest, lineage, MERGED_H, queuedRows, roundedPath, TREE_HEAD_H, TREE_MORE_H, TREE_PAD, TREE_ROW_H, treeHeight,
  type GraphNode, type GraphRequest, type NodeAgent, type NodeState, type QueueRow, type TreeRow,
} from "../lib/request-graph";
import { navigate } from "../lib/router";
import { effortLabel } from "../lib/routines";
import { modelLabel } from "../lib/session-models";
import { useFullAgentSessions } from "../lib/sessions";
import { stateCount, stateName } from "../lib/state-labels";
import { makeSessionLookup, type SessionLookup, type Todo } from "../lib/todos";
import type { Machine } from "../lib/types";
import { AgentMark } from "../ui/AgentMark";
import { Button, cx, useNow } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { StatusMark } from "../ui/StatusMark";
import { tipHandlers, useTips, type TipApi } from "./TodoRequestGraphTip";

/** The connectors' tones, from the theme's own: a line between the soft line color and the muted ink, and a fainter
 *  one into a piece still queued. Set on the view, so nothing reaches the page's shared styles. */
const EDGE_TONES = {
  "--edge": "color-mix(in srgb, var(--ink-3) 42%, var(--surface))",
  "--edge-muted": "color-mix(in srgb, var(--ink-3) 30%, var(--surface))",
} as CSSProperties;
/** The piece lit by the pointer or the focus: its request and its key. */
type Hot = { req: string; key: string } | null;
/** How often "now" moves: a working piece says how long it has worked, in minutes. */
const TICK_MS = 15_000;

// ── how a state looks ───────────────────────────────────────

/** A state's word: the app's own state names where it has one (lib/state-labels.ts), the graph's for the rest. */
function stateWord(n: Pick<GraphNode, "state" | "waitsStart">): string {
  switch (n.state) {
    case "queued": return n.waitsStart ? t("request-graph.state_planned") : t("request-graph.state_queued");
    case "ready": return t("request-graph.state_ready");
    case "working": return stateName("working");
    case "needs_you": return stateName("needs_you");
    case "review": return t("assistant-tasks.review_pending");
    case "stopped": return stateName("follow_up");
    case "done": return stateName("done");
    case "failed": return stateName("failed");
  }
}

/** The tag's tone (background and text) and its dot. */
const TAG: Record<NodeState, { tag: string; dot: string }> = {
  queued: { tag: "bg-field text-ink-2", dot: "" },
  ready: { tag: "bg-field text-accent-ink", dot: "var(--accent)" },
  working: { tag: "bg-orange-tint text-orange", dot: "var(--status-working)" },
  needs_you: { tag: "bg-field text-attention", dot: "var(--status-needs)" },
  review: { tag: "bg-green-tint text-green", dot: "var(--status-unread)" },
  stopped: { tag: "bg-field text-ink-2", dot: "var(--ink-3)" },
  done: { tag: "bg-field text-ink-3", dot: "" },
  failed: { tag: "bg-field text-red", dot: "var(--red)" },
};

/** A state's dot: filled in its tone, hollow for one that waits. */
function Dot({ color }: { color: string }) {
  return color ? <span aria-hidden className="size-1.5 shrink-0 rounded-full" style={{ background: color }} /> : <span aria-hidden className="size-1.5 shrink-0 rounded-full border border-ink-3" />;
}

function StateTag({ node }: { node: Pick<GraphNode, "state" | "waitsStart"> }) {
  const tone = TAG[node.state];
  return (
    <span className={cx("inline-flex h-5 shrink-0 items-center gap-1 rounded-chip px-1.5 text-xs leading-4 font-medium whitespace-nowrap", tone.tag)} data-node-state={node.state}>
      {node.state === "done" ? <Icon name="check" size={11} /> : <Dot color={tone.dot} />}
      {stateWord(node)}
    </span>
  );
}

/** "Opus · High · MacBook": on what and where it runs; the mark beside it says which Agent (`named`: say it in words too). */
function agentLine(a: NodeAgent, machines: Machine[], named = false): string {
  const s = a.session;
  const model = s?.model ? modelLabel(s.model) : "";
  const who = a.subagent ? a.title || t("request-graph.subagent") : named || !model ? agentName(s?.agent ?? a.agent, s?.agent_label) : "";
  return [who, model, s?.effort ? effortLabel(s.effort) : "", machineWithHub(a.mid, machines)].filter(Boolean).join(" · ");
}

/** An open plan's task: what it will run on, where. */
function taskLine(n: GraphNode, machines: Machine[]): string {
  const task = n.task;
  if (!task) return "";
  return [task.launcher ? agentName(task.launcher) : "", task.model ? modelLabel(task.model) : "", task.effort ? effortLabel(task.effort) : "", task.machine ? machineWithHub(task.machine, machines) : ""].filter(Boolean).join(" · ");
}

const names = (list: { title: string }[]) => list.map((item) => item.title).filter(Boolean).join(t("request-graph.list_sep"));

/** The line beside a piece's tag: how long it works, what it waits for, what it asks, why it did not start. */
function detail(node: GraphNode, now: number): string {
  switch (node.state) {
    case "queued": return node.waitsStart ? t("request-graph.waits_start") : t("request-graph.waits_for", { names: names(node.waitsFor) });
    case "working": return node.since !== undefined ? shortDuration(Math.max(0, now - node.since)) : "";
    case "needs_you": case "failed": case "review": return node.ask;
    case "done": return node.todo?.done_at ? clock(node.todo.done_at) : "";
    default: return "";
  }
}

// ── the view ────────────────────────────────────────────────

/** Opens an Agent's session as its own page (a subagent's: the session that launched it). */
const openSession = (mid: string, sid: string) => navigate({ name: "session", mid, sid });

/** What every request block shares: the hover cards, which trees are folded, and where a tree's line goes. */
interface Shared {
  machines: Machine[];
  now: number;
  selectedId?: string;
  tips: TipApi;
  folded: ReadonlySet<string>;
  onFold: (key: string) => void;
  onOpenTodo: (id: string) => void;
  onOpenAgent: (mid: string, sid: string) => void;
}

export default function TodoRequestGraph({ todos, lookup, machines, selectedId, onOpenTodo, onOpenAgent = openSession }: {
  todos: readonly Todo[]; lookup: SessionLookup; machines: Machine[]; selectedId?: string; onOpenTodo: (id: string) => void;
  onOpenAgent?: (mid: string, sid: string) => void;
}) {
  const plans = useFeed((s) => s.plans);
  const tree = useDirectionTree();
  const now = useNow(TICK_MS);
  // a list's summary rows leave out a session's subagents (running and ended): the complete rows have them
  const full = useFullAgentSessions();
  const complete = useMemo(() => makeSessionLookup(full.sessions), [full.sessions]);
  const graph = useMemo(() => buildGraph({ todos, plans, data: { tree, todos, lookup, complete } }), [todos, plans, tree, lookup, complete]);
  const queue = useMemo(() => queuedRows(graph), [graph]);
  const [hot, setHot] = useState<Hot>(null);
  // the Agent trees folded to their first line (every tree starts open)
  const [folded, setFolded] = useState<ReadonlySet<string>>(() => new Set());
  const onFold = useCallback((key: string) => setFolded((was) => {
    const next = new Set(was);
    if (!next.delete(key)) next.add(key);
    return next;
  }), []);
  const tips = useTips();
  const shared: Shared = { machines, now, selectedId, tips: tips.api, folded, onFold, onOpenTodo, onOpenAgent };
  return (
    <section aria-label={t("request-graph.label")} className="@container flex min-h-0 flex-1 flex-col" data-graph="" style={EDGE_TONES}>
      <div className="min-h-0 min-w-0 flex-1 overflow-auto overscroll-contain" data-graph-zone="">
        {/* as wide as the widest request, so every request's rule runs the whole way when the page scrolls sideways */}
        <div className="w-max min-w-full">
          {graph.active.length === 0 && graph.done.length === 0 ? <Empty /> : graph.active.length === 0 ? <AllDone /> : null}
          {graph.active.map((req) => (
            <RequestBlock key={req.key} req={req} shared={shared} hot={hot?.req === req.key ? hot.key : null}
              onHot={(key) => setHot(key ? { req: req.key, key } : null)} />
          ))}
          {graph.done.length > 0 && <DoneFold done={graph.done} shared={shared} hot={hot} onHot={setHot} />}
        </div>
      </div>
      <QueuedStrip rows={queue} selectedId={selectedId} onHot={setHot} onOpenTodo={onOpenTodo} />
      {tips.layer}
    </section>
  );
}

function Empty() {
  return (
    <div className="flex flex-col items-center justify-center gap-2 px-6 py-16 text-center" data-graph-empty="">
      <span className="flex size-9 items-center justify-center rounded-control bg-canvas text-ink-3"><Icon name="workflow" size={18} /></span>
      <h3 className="text-sm font-semibold text-ink">{t("request-graph.empty_title")}</h3>
      <p className="max-w-sm text-xs leading-5 text-ink-3">{t("request-graph.empty_body")}</p>
    </div>
  );
}

function AllDone() {
  return (
    <div className="flex items-center gap-2 px-4 py-6 text-xs text-ink-3" data-graph-all-done="">
      <Icon name="check" size={13} className="text-green" />
      {t("request-graph.all_done")}
    </div>
  );
}

// ── one request ─────────────────────────────────────────────

function RequestBlock({ req, shared, hot, onHot, flat }: {
  req: GraphRequest; shared: Shared; hot: string | null; onHot: (key: string | null) => void; flat?: boolean;
}) {
  const { folded, selectedId, tips, onOpenTodo } = shared;
  const lay = useMemo(() => layoutRequest(req, (n) => treeHeight(n.tree, !folded.has(n.key))), [req, folded]);
  const lit = useMemo(() => (hot && hot !== "root" ? lineage(req, hot) : null), [req, hot]);
  // edges in the lit lineage are drawn last, over the dimmed ones
  const edges = lit ? [...lay.edges].sort((a, b) => Number(lit.has(a.from) && lit.has(a.to)) - Number(lit.has(b.from) && lit.has(b.to))) : lay.edges;
  const dots = new Map<string, { at: [number, number]; on: boolean; muted: boolean }>();
  for (const e of edges) {
    const k = e.dot.join(",");
    const on = !lit || lit.has(e.from);
    const d = dots.get(k);
    dots.set(k, { at: e.dot, on: (d?.on ?? false) || on, muted: (d?.muted ?? true) && e.dashed });
  }
  return (
    <div className={cx("px-3 py-2", !flat && "border-b border-line")} data-request={req.key} data-request-kind={req.kind}>
      <div className="relative" style={{ width: lay.width, height: lay.height }}>
        <svg width={lay.width} height={lay.height} className="pointer-events-none absolute inset-0 block overflow-visible" aria-hidden data-edges="">
          {edges.map((e) => {
            const on = Boolean(lit && lit.has(e.from) && lit.has(e.to));
            const dim = Boolean(lit) && !on;
            return (
              <path key={`${e.from}>${e.to}`} d={e.d} fill="none" strokeWidth={on ? 1.75 : 1.25} strokeLinecap="round" strokeDasharray={e.dashed ? "3 3" : undefined}
                className="transition-[stroke,opacity] duration-150" data-edge={`${e.from}>${e.to}`} data-lit={on ? "" : undefined}
                style={{ stroke: on ? "var(--accent)" : e.dashed ? "var(--edge-muted)" : "var(--edge)", opacity: dim ? 0.35 : 1 }} />
            );
          })}
          {[...dots.values()].map((d) => (
            <circle key={d.at.join(",")} cx={d.at[0]} cy={d.at[1]} r={3} strokeWidth={1.25} className="transition-[stroke,opacity] duration-150"
              style={{ fill: "var(--surface)", stroke: lit && d.on ? "var(--accent)" : d.muted ? "var(--edge-muted)" : "var(--edge)", opacity: lit && !d.on ? 0.35 : 1 }} />
          ))}
        </svg>
        {!req.merged && <RootCard req={req} at={lay.root} selected={Boolean(req.root) && req.root!.id === selectedId} tips={tips} onOpenTodo={onOpenTodo} onHot={onHot} />}
        {req.nodes.map((n) => {
          const p = lay.nodes.get(n.key)!;
          return <NodeCard key={n.key} node={n} req={req} at={p} shared={shared} dim={Boolean(lit) && !lit!.has(n.key)} lit={hot === n.key}
            selected={Boolean(n.todo) && n.todo!.id === selectedId} onHot={onHot} />;
        })}
      </div>
    </div>
  );
}

/** The root's label: the owner's words when they are the heading, else "Request" (the sentence the work was filed under). */
const rootLabel = (req: GraphRequest) => (req.own ? t("request-graph.root_words") : t("request-graph.root_title"));

/** The owner's words quoted under a heading that says something else. */
const quoteLine = (req: GraphRequest) => t("request-graph.root_quote", { words: req.quote });

function RootCard({ req, at, selected, tips, onOpenTodo, onHot }: { req: GraphRequest; at: { x: number; y: number; h: number }; selected: boolean; tips: TipApi; onOpenTodo: (id: string) => void; onHot: (key: string | null) => void }) {
  const done = req.nodes.filter((n) => n.state === "done").length;
  const label = rootLabel(req);
  const th = tipHandlers(tips, () => (
    <>
      <p className="text-xs text-tooltip-muted">{label}{req.at ? ` · ${clock(req.at)}` : ""}</p>
      <p className="break-words whitespace-pre-line">{req.title}</p>
      {req.quote && (
        <div>
          <p className="text-xs text-tooltip-muted">{t("request-graph.root_words")}</p>
          <p className="text-xs break-words whitespace-pre-line">{req.quote}</p>
        </div>
      )}
      {req.root && <p className="border-t border-tooltip-border pt-1 text-xs text-tooltip-muted">{t("request-graph.tip_open_todo")}</p>}
    </>
  ));
  const body = (
    <>
      <span className="flex h-5 items-center gap-1.5 text-xs leading-4 text-ink-3">
        <Icon name={req.kind === "plan" ? "spark" : "quote"} size={11} className="shrink-0" />
        <span className="min-w-0 flex-1 truncate">{label}</span>
        {req.at && <span className="shrink-0 tabular-nums">{clock(req.at)}</span>}
      </span>
      <span className="mt-1.5 line-clamp-2 max-h-8 shrink-0 overflow-hidden text-xs leading-4 break-words text-ink" data-root-words="">{req.title}</span>
      {req.quote && <span className="mt-1 h-4 shrink-0 truncate text-xs leading-4 text-ink-3" data-root-quote="">{quoteLine(req)}</span>}
      <span className="mt-auto flex h-4 items-center gap-1.5 text-xs leading-4 text-ink-3 tabular-nums">
        {req.nodes.length > 1 && <Progress done={done} total={req.nodes.length} />}
        <span className="truncate">{req.nodes.length > 1 ? t("request-graph.progress", { done, total: req.nodes.length }) : t("request-graph.pieces", { count: req.nodes.length })}</span>
      </span>
    </>
  );
  const cls = cx("absolute flex flex-col overflow-hidden rounded-control bg-canvas px-2.5 py-2 text-left", selected && "ring-2 ring-accent");
  const style = { left: at.x, top: at.y, width: CARD_W, height: at.h };
  if (req.root) {
    const id = req.root.id;
    return (
      <button type="button" className={cx(cls, "hover:bg-hover-2 focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none")} style={style} data-root={req.key} aria-current={selected || undefined}
        {...th} onFocus={(e) => { th.onFocus(e); onHot(null); }} onClick={() => onOpenTodo(id)}>
        {body}
      </button>
    );
  }
  return <div className={cls} style={style} data-root={req.key} onPointerEnter={th.onPointerEnter} onPointerLeave={th.onPointerLeave}>{body}</div>;
}

/** Done of total, as a thin bar. */
function Progress({ done, total }: { done: number; total: number }) {
  return (
    <span aria-hidden className="relative h-1 w-10 shrink-0 overflow-hidden rounded-full bg-hover-2">
      <span className="absolute inset-y-0 left-0 rounded-full" style={{ width: `${(done / Math.max(1, total)) * 100}%`, background: "var(--status-unread)" }} />
    </span>
  );
}

function NodeCard({ node, req, at, shared, dim, lit, selected, onHot }: {
  node: GraphNode; req: GraphRequest; at: { x: number; y: number; h: number }; shared: Shared; dim: boolean; lit: boolean; selected: boolean;
  onHot: (key: string | null) => void;
}) {
  const { machines, now, tips, onOpenTodo } = shared;
  const line = detail(node, now);
  const current = node.agents.filter((a) => a.role !== "before");
  const lead = current[0];
  const queued = node.state === "queued";
  const grown = node.tree.length > 0;
  const headH = req.merged ? MERGED_H : CARD_H;
  const th = tipHandlers(tips, () => <NodeTip node={node} req={req} machines={machines} now={now} />);
  // the card's own look; with an Agent tree it is the box that holds the head and the tree
  const chrome = cx(
    "rounded-control transition-[opacity,box-shadow,background-color] duration-150",
    queued ? "border border-dashed border-line-strong bg-surface" : "bg-surface shadow-card",
    node.state === "needs_you" && "shadow-[inset_2px_0_0_var(--status-needs),var(--shadow-card)]",
    (lit || selected) && "ring-2 ring-accent",
    dim && "opacity-40",
    node.state === "done" && !dim && "opacity-75",
  );
  const head = {
    "data-node": node.key, "data-state": node.state, "data-lit": lit ? "" : undefined, "data-dim": dim ? "" : undefined,
    style: (grown ? { height: headH } : { left: at.x, top: at.y, width: CARD_W, height: headH }) as CSSProperties,
    onPointerEnter: (e: React.PointerEvent<Element>) => { th.onPointerEnter(e); if (!grown) onHot(node.key); },
    onPointerLeave: (e: React.PointerEvent<Element>) => { th.onPointerLeave(e); if (!grown) onHot(null); },
    className: grown
      ? cx("relative flex w-full shrink-0 flex-col px-2.5 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-inset", node.todo && "hover:bg-hover")
      : cx("absolute flex flex-col overflow-hidden px-2.5 py-2 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent", chrome, node.todo && "hover:bg-hover"),
  };
  const body = (
    <>
      {req.merged && (
        // the request and the piece say the same: one card, with what the root card would have said of when (and the
        // owner's words when they said something else)
        <span className="mb-1 flex h-4 min-w-0 items-center gap-1.5 text-xs leading-4 text-ink-3" data-node-request="">
          <Icon name={req.kind === "plan" ? "spark" : "quote"} size={11} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate" data-root-quote={req.quote ? "" : undefined}>{req.quote ? quoteLine(req) : rootLabel(req)}</span>
          {req.at && <span className="shrink-0 tabular-nums">{clock(req.at)}</span>}
        </span>
      )}
      <span className="flex h-5 min-w-0 items-center gap-1.5">
        <StateTag node={node} />
        {line && (
          <span className={cx("min-w-0 flex-1 truncate text-xs leading-4", node.state === "working" ? "text-right text-ink-3 tabular-nums" : node.state === "needs_you" ? "text-ink-2" : "text-ink-3")} data-node-detail="">{line}</span>
        )}
      </span>
      <span className={cx("mt-1.5 line-clamp-2 max-h-8 overflow-hidden text-xs leading-4 font-medium break-words", node.state === "done" ? "text-ink-2" : "text-ink")} data-node-title="">{node.title}</span>
      <span className="mt-auto flex h-5 min-w-0 items-center gap-1.5 text-xs leading-4 text-ink-3" data-node-agent={lead ? lead.key : ""}>
        {lead ? (
          <>
            {/* with a tree under the card, every Agent has its own line there: the head keeps the first one's mark */}
            <Marks agents={grown ? [lead] : current} />
            <span className="min-w-0 flex-1 truncate">{agentLine(lead, machines)}</span>
          </>
        ) : node.task ? (
          <>
            {node.task.launcher && <span className="flex size-4 shrink-0 items-center justify-center rounded-chip opacity-60"><AgentMark agent={node.task.launcher} size={11} decorative /></span>}
            <span className="min-w-0 flex-1 truncate">{taskLine(node, machines) || t("request-graph.no_agent")}</span>
          </>
        ) : (
          <span className="min-w-0 flex-1 truncate italic" data-no-agent="">{t("request-graph.no_agent")}</span>
        )}
      </span>
    </>
  );
  // a task of a plan not started yet has no to-do to open: a card with its hover card only
  const card = !node.todo ? <div {...head}>{body}</div> : (
    <button type="button" {...head} aria-current={selected || undefined} onClick={() => onOpenTodo(node.todo!.id)}
      onPointerDown={th.onPointerDown} onFocus={(e) => { th.onFocus(e); onHot(node.key); }} onBlur={(e) => { th.onBlur(e); onHot(null); }}>
      {body}
    </button>
  );
  if (!grown) return card;
  // the card grows its Agent tree under its head: pointing anywhere on it lights the piece
  return (
    <div className={cx("absolute flex flex-col overflow-hidden", chrome)} style={{ left: at.x, top: at.y, width: CARD_W, height: at.h }} data-node-card={node.key}
      onPointerEnter={() => onHot(node.key)} onPointerLeave={() => onHot(null)}>
      {card}
      <AgentTree node={node} open={!shared.folded.has(node.key)} onFold={() => shared.onFold(node.key)} onOpenAgent={shared.onOpenAgent} />
    </div>
  );
}

/** Up to three Agent marks, each a little over the one before, then "+n". */
function Marks({ agents }: { agents: NodeAgent[] }) {
  const shown = agents.length > 3 ? agents.slice(0, 2) : agents;
  const more = agents.length - shown.length;
  return (
    <span className="flex shrink-0 items-center" data-marks={agents.length}>
      {shown.map((a, i) => (
        <span key={a.key} className={cx("flex size-4 items-center justify-center rounded-chip bg-surface shadow-hairline", i > 0 && "-ml-1")}>
          {a.subagent ? <Icon name="cornerDownRight" size={10} className="text-ink-3" /> : <AgentMark {...(a.session ? sessionAgent(a.session) : { agent: a.agent })} size={10} decorative />}
        </span>
      ))}
      {more > 0 && <span className="-ml-1 flex h-4 min-w-4 items-center justify-center rounded-chip bg-field px-1 text-xs leading-4 font-medium text-ink-2 tabular-nums shadow-hairline">+{more}</span>}
    </span>
  );
}

// ── a piece's Agent tree ────────────────────────────────────

/** The tree's geometry inside the card, in px: the card's side padding, how far each level steps in, the mark's size,
 *  and where a line's mark sits (the middle of its first text line). Levels past MAX_INDENT step in no further. */
const TREE_X = 10;
const TREE_STEP = 14;
const MARK = 16;
const MARK_MID = 9;
const MAX_INDENT = 3;
const indentOf = (depth: number) => TREE_X + Math.min(depth, MAX_INDENT) * TREE_STEP;
const rowH = (r: TreeRow) => (r.kind === "more" ? TREE_MORE_H : TREE_ROW_H);

/** The connectors from each line to the one it hangs under: down the parent's mark column, a rounded turn, into the
 *  line's mark, drawn as the graph's own connectors are. */
function treeEdges(rows: readonly TreeRow[]): string[] {
  const tops: number[] = [];
  rows.reduce((y, r) => (tops.push(y), y + rowH(r)), 0);
  const at = new Map(rows.map((r, i) => [r.key, i]));
  const out: string[] = [];
  rows.forEach((r, i) => {
    const parent = r.kind === "agent" ? r.agent.parent : r.parent;
    const p = parent === undefined ? undefined : at.get(parent);
    if (p === undefined) return;
    const x = indentOf(rows[p].depth) + MARK / 2;
    const x2 = indentOf(r.depth);
    const y = tops[i] + (r.kind === "more" ? TREE_MORE_H / 2 : MARK_MID);
    if (x2 <= x) return; // past the deepest indent: the line sits right under its parent
    out.push(roundedPath([[x, tops[p] + MARK_MID + MARK / 2], [x, y], [x2 - 2, y]]));
  });
  return out;
}

/** A state's word on a tree line: ended, before, or the Agent's own state. */
function treeState(a: NodeAgent): string {
  if (a.role === "before") return t("request-graph.agent_before");
  if (a.ended) return t("request-graph.agent_ended");
  return a.state ? stateName(a.state) : "";
}

function AgentTree({ node, open, onFold, onOpenAgent }: { node: GraphNode; open: boolean; onFold: () => void; onOpenAgent: (mid: string, sid: string) => void }) {
  const rows = node.tree;
  const working = node.agents.filter((a) => !a.ended && a.state === "working").length;
  const paths = useMemo(() => treeEdges(rows), [rows]);
  const height = rows.reduce((h, r) => h + rowH(r), 0);
  return (
    <div className="shrink-0" data-agent-tree={node.key} data-open={open ? "" : undefined}>
      <button type="button" aria-expanded={open} onClick={onFold} style={{ height: TREE_HEAD_H }}
        className="flex w-full min-w-0 items-center gap-1.5 border-t border-line px-2.5 text-left text-xs leading-4 text-ink-3 transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-inset">
        <Icon name="chevronRight" size={12} className={cx("shrink-0 transition-transform", open && "rotate-90")} />
        <span className="min-w-0 flex-1 truncate" data-tree-count={node.agents.length}>
          {[t("request-graph.tree_agents", { count: node.agents.length }), working ? stateCount("working", working) : ""].filter(Boolean).join(" · ")}
        </span>
      </button>
      {open && (
        <ul className="relative" style={{ paddingBottom: TREE_PAD }}>
          <svg width={CARD_W} height={height} className="pointer-events-none absolute top-0 left-0 block overflow-visible" aria-hidden data-tree-edges="">
            {paths.map((d, i) => <path key={i} d={d} fill="none" strokeWidth={1.25} strokeLinecap="round" style={{ stroke: "var(--edge)" }} />)}
          </svg>
          {rows.map((r) => (r.kind === "more" ? (
            <li key={r.key} className="relative flex items-center text-xs leading-4 text-ink-3" style={{ height: TREE_MORE_H, paddingLeft: indentOf(r.depth) + MARK + 6 }} data-tree-more={r.count}>
              {t("request-graph.tree_more", { count: r.count })}
            </li>
          ) : (
            <TreeLine key={r.key} agent={r.agent} depth={r.depth} onOpenAgent={onOpenAgent} />
          )))}
        </ul>
      )}
    </div>
  );
}

function TreeLine({ agent: a, depth, onOpenAgent }: { agent: NodeAgent; depth: number; onOpenAgent: (mid: string, sid: string) => void }) {
  const name = (a.session && sessionTitle(a.session)) || a.title || (a.subagent ? t("request-graph.subagent") : t("request-graph.agent_untitled"));
  const state = treeState(a);
  return (
    <li className="relative" style={{ height: TREE_ROW_H }}>
      <button type="button" onClick={() => onOpenAgent(a.open.mid, a.open.sid)} style={{ paddingLeft: indentOf(depth) }}
        className="flex size-full min-w-0 items-start gap-1.5 pt-px pr-2.5 text-left text-xs leading-4 transition-colors hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-inset"
        data-tree-row={a.key} data-tree-role={a.role} data-tree-depth={a.depth} data-ended={a.ended ? "" : undefined}>
        <span className={cx("flex size-4 shrink-0 items-center justify-center rounded-chip bg-surface shadow-hairline", a.ended && "opacity-60")}>
          {a.subagent || a.role === "subagent" ? <Icon name="cornerDownRight" size={10} className="text-ink-3" />
            : <AgentMark {...(a.session ? sessionAgent(a.session) : { agent: a.agent })} size={10} decorative />}
        </span>
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="flex h-4 min-w-0 items-center gap-1.5">
            <span className={cx("min-w-0 flex-1 truncate", a.ended ? "text-ink-3" : "text-ink")}>{name}</span>
            {a.ended ? <Icon name="check" size={11} className="shrink-0 text-ink-3" />
              : a.state ? <span className="flex shrink-0 items-center"><StatusMark state={a.state} size="sm" label={false} /></span> : null}
          </span>
          <span className="h-4 truncate text-ink-3" data-tree-doing="">{[state, a.doing].filter(Boolean).join(" · ")}</span>
        </span>
      </button>
    </li>
  );
}

const ROLE_KEY: Record<NodeAgent["role"], string> = {
  lead: "",
  worker: "request-graph.role_worker",
  spawned: "request-graph.role_spawned",
  subagent: "request-graph.subagent",
  before: "request-graph.agent_before",
};

function NodeTip({ node, req, machines, now }: { node: GraphNode; req: GraphRequest; machines: Machine[]; now: number }) {
  const byKey = new Map(req.nodes.map((x) => [x.key, x]));
  const line = detail(node, now);
  const strictWaits = node.todo?.prerequisite_blockers !== undefined && (node.state === "queued" || node.state === "ready");
  const waits = strictWaits ? node.waitsFor.map((wait) => ({ ...wait, piece: undefined })) : node.after.map((key) => {
    const piece = byKey.get(key)!;
    return { key, title: piece.title, piece };
  });
  return (
    <>
      <p className="font-medium break-words">{node.title}</p>
      <p className="text-xs text-tooltip-muted">
        {stateWord(node)}
        {node.state === "working" && line ? ` · ${line}` : ""}
      </p>
      {(node.state === "needs_you" || node.state === "failed" || node.state === "review") && line && <p className="break-words whitespace-pre-line">{line}</p>}
      {node.waitsStart && <p className="text-xs break-words">{t("request-graph.waits_start")}</p>}
      {waits.length > 0 && (
        <div>
          <p className="text-xs text-tooltip-muted">{t("request-graph.tip_after")}</p>
          <ul className="space-y-0.5">
            {waits.map(({ key, title, piece }) => (
              <li key={key} className="flex items-center gap-1.5 text-xs">
                {piece?.state === "done" ? <Icon name="check" size={11} className="shrink-0" /> : <span aria-hidden className="size-1.5 shrink-0 rounded-full border border-current" />}
                <span className="min-w-0 break-words">{title}</span>
                {piece && <span className="shrink-0 text-tooltip-muted">· {stateWord(piece)}</span>}
              </li>
            ))}
          </ul>
        </div>
      )}
      <div>
        <p className="text-xs text-tooltip-muted">{t("request-graph.tip_agents")}</p>
        {node.agents.length ? (
          <ul className="space-y-1">
            {node.agents.map((a) => (
              <li key={a.key} className="flex items-start gap-1.5 text-xs">
                <span className="mt-px flex size-4 shrink-0 items-center justify-center">
                  {a.subagent || a.role === "subagent" ? <Icon name="cornerDownRight" size={11} /> : <AgentMark {...(a.session ? sessionAgent(a.session) : { agent: a.agent })} size={12} decorative className="text-tooltip-fg" />}
                </span>
                <span className="min-w-0">
                  <span className="block break-words">{(a.session && sessionTitle(a.session)) || a.title || t("request-graph.agent_untitled")}</span>
                  <span className="block break-words text-tooltip-muted">
                    {[ROLE_KEY[a.role] ? t(ROLE_KEY[a.role]) : "", a.ended && a.role !== "before" ? t("request-graph.agent_ended") : "", agentLine(a, machines, true)].filter(Boolean).join(" · ")}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-xs">{node.task ? taskLine(node, machines) || t("request-graph.no_agent") : t("request-graph.no_agent")}</p>
        )}
      </div>
      <p className="text-xs break-words text-tooltip-muted">{t("request-graph.tip_request", { title: req.title })}</p>
      <p className="border-t border-tooltip-border pt-1 text-xs text-tooltip-muted">{node.todo ? t("request-graph.tip_open_todo") : t("request-graph.waits_start")}</p>
    </>
  );
}

// ── finished requests ───────────────────────────────────────

/** How many finished requests show at a time once the fold is open. */
const DONE_PAGE = 30;

function DoneFold({ done, shared, hot, onHot }: { done: GraphRequest[]; shared: Shared; hot: Hot; onHot: (h: Hot) => void }) {
  const [open, setOpen] = useState(false);
  const [shown, setShown] = useState(DONE_PAGE);
  const [opened, setOpened] = useState<string | null>(null);
  return (
    <div className="px-3 py-2" data-graph-done="">
      <button type="button" aria-expanded={open} onClick={() => setOpen((v) => !v)}
        className="flex h-7 items-center gap-1.5 rounded-control px-1.5 text-xs font-medium text-ink-2 transition-colors hover:bg-hover">
        <Icon name="chevronRight" size={12} className={cx("text-ink-3 transition-transform", open && "rotate-90")} />
        {t("request-graph.done_fold", { count: done.length })}
      </button>
      {open && (
        <ul className="mt-1 space-y-0.5">
          {done.slice(0, shown).map((req) => (
            <li key={req.key} data-done-request={req.key}>
              <button type="button" aria-expanded={opened === req.key} onClick={() => setOpened((v) => (v === req.key ? null : req.key))}
                className="flex h-7 w-full min-w-0 items-center gap-2 rounded-control px-1.5 text-left text-xs transition-colors hover:bg-hover">
                <Icon name="check" size={12} className="shrink-0 text-green" />
                <span className="min-w-0 flex-1 truncate text-ink-2">{req.title}</span>
                <span className="shrink-0 text-ink-3 tabular-nums">{t("request-graph.pieces", { count: req.nodes.length })}</span>
                {req.finished > 0 && <span className="w-24 shrink-0 truncate text-right text-ink-3 tabular-nums">{clock(new Date(req.finished).toISOString())}</span>}
              </button>
              {opened === req.key && (
                <RequestBlock req={req} shared={shared} hot={hot?.req === req.key ? hot.key : null} flat
                  onHot={(key) => onHot(key ? { req: req.key, key } : null)} />
              )}
            </li>
          ))}
          {done.length > shown && (
            <li>
              <Button tone="quiet" size="sm" className="whitespace-nowrap" onClick={() => setShown((n) => n + DONE_PAGE)}>
                {t("request-graph.show_more", { count: Math.min(DONE_PAGE, done.length - shown) })}
              </Button>
            </li>
          )}
        </ul>
      )}
    </div>
  );
}

// ── still queued ────────────────────────────────────────────

/** The pieces still queued under the graph: those nobody took yet and those waiting for others. A row lights its piece
 *  in the graph and opens its to-do. */
function QueuedStrip({ rows, selectedId, onHot, onOpenTodo }: { rows: QueueRow[]; selectedId?: string; onHot: (h: Hot) => void; onOpenTodo: (id: string) => void }) {
  if (!rows.length) return null;
  return (
    <aside aria-label={t("request-graph.queued")} className="max-h-40 shrink-0 overflow-y-auto overscroll-contain border-t border-line bg-canvas px-2 pb-2" data-queued="">
      <h3 className="flex h-7 items-center gap-1.5 px-1.5 text-xs font-medium">
        <Dot color="" />
        <span className="text-ink-2">{t("request-graph.queued")}</span>
        <span className="text-ink-3 tabular-nums" data-queued-count="">{rows.length}</span>
      </h3>
      <ol className="grid grid-cols-1 gap-x-2 gap-y-0.5 @2xl:grid-cols-2 @5xl:grid-cols-3">
        {rows.map((row) => {
          const light = () => onHot({ req: row.request.key, key: row.node.key });
          const id = row.node.todo?.id;
          return (
            <li key={row.key} data-queued-row={row.key} data-queued-kind={row.kind}>
              <button type="button" aria-current={id !== undefined && id === selectedId ? true : undefined} disabled={!id}
                className="flex w-full min-w-0 items-start gap-2 rounded-control px-1.5 py-1.5 text-left transition-colors enabled:hover:bg-hover focus-visible:bg-hover focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none"
                onPointerEnter={light} onPointerLeave={() => onHot(null)} onFocus={light} onBlur={() => onHot(null)} onClick={() => id && onOpenTodo(id)}>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-xs leading-4 font-medium text-ink">{row.title}</span>
                  <span className="block truncate text-xs leading-4 text-ink-3" data-queued-why="">
                    {row.kind === "ready" ? t("request-graph.why_ready") : t("request-graph.waits_for", { names: names(row.waits) })}
                  </span>
                </span>
              </button>
            </li>
          );
        })}
      </ol>
    </aside>
  );
}
