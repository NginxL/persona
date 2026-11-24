// A 常驻 row's disclosure in the sidebar (#4352): each leader row under Persona chats (#4610: its conversation's row with
// "Doing N · Needs you N") gets a chevron that folds open to the tasks the hub's direction tree lists under it (the one
// tree, lib/direction-tree.ts, the counts come from too), each task in its state word and opening its to-do, each task
// folding open to its agents, each agent to its own. Folded (the default, kept per leader on this device) the row is
// #4610's own. A leader the tree does not list (an older hub, a leader made a moment ago) keeps the plain row.
import { memo, useMemo } from "react";
import { t } from "../i18n";
import { useDirectionTree, type Direction } from "../lib/direction-tree";
import { directionTaskNode } from "../lib/direction-agent-tree";
import { useAgentSessions } from "../lib/sessions";
import { useStore } from "../lib/store";
import { useFolded } from "../lib/use-folded";
import { Collapse, cx, sidebarFoldMotion } from "../ui/atoms";
import { Icon } from "../ui/icons";
import type { GoFn, RegFn } from "./SidebarParts";
import { TreeTodoRows, useMachineLabel } from "./SidebarTree";

type Props = {
  /** The direction's opaque id (its leader conversation's when it has one, never the external Agent's session id). */
  id: string;
  /** Its name as the row shows it, for the disclosure's label. */
  name: string;
  /** The row #4610 draws for it. */
  row: React.ReactNode;
  reg: RegFn;
  hover: (k: string | null) => void;
  go: GoFn;
  onNavigate?: () => void;
};

/** A 常驻 row and, with the tree, its disclosure and its tasks. */
export function SidebarDirectionRow({ id, row, ...p }: Props) {
  const tree = useDirectionTree();
  const node = useMemo(() => tree?.directions.find((d) => d.id === id && !d.secretary), [tree, id]);
  if (!node) return <>{row}</>;
  return <DirectionTasks key={id} id={id} row={row} node={node} {...p} />;
}

const DirectionTasks = memo(function DirectionTasks({ id, name, row, node, reg, hover, go, onNavigate }: Props & { node: Direction }) {
  const all = useAgentSessions();
  const machines = useStore((s) => s.machines);
  const machineLabel = useMachineLabel(machines);
  // only the saved fold (or folded): an open agent under it does not unfold it. Its rows are copies of the Agents list's,
  // which owns the open row (highlight, scroll to it): these register under their own keys and are never the open one
  const [folded, toggle] = useFolded(`direction:${id}`, true);
  const todos = useMemo(() => {
    const sessions = new Map(all.map((s) => [`${s.machine_id}/${s.session_id}`, s]));
    const placed = new Set<string>();
    return (node.tasks ?? []).map((td) => directionTaskNode(td, sessions, placed, { keyPrefix: "direction:task:", folded: true, standIns: true }));
  }, [all, node]);
  const tasks = todos.length;
  // leading, as every fold in the tree under it; small, so the row keeps #4610's width for its name, with a finger's
  // 44 on a touch screen (touch-hit) from a wider box that keeps that area inside the sidebar. The label says the
  // action in words (#6214): a bare glyph alone read as a dead "fork" icon.
  const chevron = "touch-hit relative z-20 -mr-1 flex size-4 shrink-0 items-center justify-center rounded-chip text-ink-3 transition-colors hover:bg-hover hover:text-ink-2 [@media(pointer:coarse)]:w-7";
  const foldLabel = folded ? t("sidebar.direction_tasks_expand", { name }) : t("sidebar.direction_tasks_collapse", { name });
  return (
    <div data-direction={id}>
      <div className="flex items-center">
        {tasks > 0 ? (
          <button type="button" aria-expanded={!folded} aria-label={foldLabel} title={foldLabel} onClick={toggle} className={chevron}>
            <Icon name="chevron" size={10} stroke={2.4} className="transition-transform" style={{ ...sidebarFoldMotion(!folded), transform: folded ? "rotate(-90deg)" : "none" }} />
          </button>
        ) : (
          // nothing to open: the name stays in the column of the others
          <span aria-hidden className={cx(chevron, "invisible")} />
        )}
        <div className="min-w-0 flex-1">{row}</div>
      </div>
      {tasks > 0 && (
        <Collapse open={!folded} sidebarFold>
          <div className="flex flex-col gap-px sidebar-indent" inert={folded || undefined}>
            {todos.map((td) => (
              <TreeTodoRows key={td.key} td={td} machineLabel={machineLabel} noContext keyPrefix={`d:${id}:`} reg={reg} hover={hover} activeKey="" go={go} onNavigate={onNavigate} />
            ))}
          </div>
        </Collapse>
      )}
    </div>
  );
});
