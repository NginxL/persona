// Pane operations belong to the open terminal, while session menus describe the conversation (#10239).
import { useMemo, useRef, useState } from "react";
import { t } from "../../../i18n";
import { errorText } from "../../../lib/api";
import { paneHost } from "../../../lib/agent-groups";
import { defaultNodeDeps, nodeActions, sessionNode, type MenuNode } from "../../../lib/node-actions";
import { getSession, refresh, useStore } from "../../../lib/store";
import { isPaneAddress } from "../../../lib/terminals";
import { IconButton, Menu, toast, type MenuItem } from "../../../ui/atoms";

export function PaneActions({ mid, sid, fullSupported, onSplit }: { mid: string; sid: string; fullSupported: boolean; onSplit: () => void }) {
  const session = useStore((s) => getSession(mid, sid) ?? (isPaneAddress(sid) ? s.sessions[mid]?.find((row) => row.pane_id === sid.slice(5)) : undefined));
  const online = useStore((s) => s.machines.find((m) => m.id === mid)?.online !== false);
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  const deps = useMemo(() => defaultNodeDeps(() => void refresh().catch(() => {})), []);
  const node: MenuNode = session
    ? sessionNode(session, online, { sessionOnly: false, host: session.space_kind || paneHost(session.pane_id) })
    : { kind: "pane", mid, online, paneId: isPaneAddress(sid) ? sid.slice(5) : undefined, host: paneHost(sid.slice(5)) };
  if (!node.paneId) return null;
  const items = nodeActions(node, deps);
  const split = items.find((it) => it.key === "node-split");
  const runSplit = async (it: MenuItem) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    try {
      await it.onSelect();
      onSplit();
    } finally {
      pending.current = false;
      setBusy(false);
    }
  };
  const splits = fullSupported && split
    ? (split.children as MenuItem[]).map((it) => ({ ...it, disabled: busy, onSelect: () => runSplit(it) }))
    : [];
  const more = items.filter((it) => it.key === "node-close-pane" || it.key === "node-copy-id").map((it) => ({
    ...it, divider: false, ...(it.key === "node-close-pane" ? { label: t("session-terminalpanel.close_this_pane") } : {}),
  }));

  return <>
    {splits.map((it, i) => <IconButton key={it.key} icon={i === 0 ? "sidebar" : "layers"} label={it.label} disabled={busy} onClick={() => void Promise.resolve(it.onSelect()).catch((e) => toast(errorText(e), "error"))} />)}
    {more.length > 0 && <IconButton icon="more" label={t("session-terminalpanel.pane_actions")} active={Boolean(anchor)} disabled={busy}
      onClick={(e) => {
        e.currentTarget.focus();
        setAnchor(anchor ? null : e.currentTarget);
      }}
      onKeyDown={(e) => {
        if (anchor || !["Enter", " ", "ArrowDown"].includes(e.key)) return;
        e.preventDefault();
        e.currentTarget.focus();
        setAnchor(e.currentTarget);
      }} />}
    {anchor && <Menu anchor={anchor} align="right" focusScoped onClose={() => setAnchor(null)} items={[
      ...(splits.length && split ? [{ ...split, divider: false, disabled: busy, children: splits }] : []),
      ...more,
    ]} />}
  </>;
}
