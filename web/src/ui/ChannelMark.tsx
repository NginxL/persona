// A channel's mark (docs/extensions/channels.md "UI"): Feishu's own for the Feishu bot and /agent, else an icon of its kind —
// WeChat's mark and its terms of use are undecided (lib/connectors.ts), so it draws the generic chat icon until they are.
import { lazy, Suspense, useEffect, useState } from "react";
import type { Channel } from "../lib/channels";
import type { BrandId } from "../lib/connectors";
import { ORIGIN_BRAND, ORIGIN_ICON, openerFrom, openerOf, type AutoOrigin } from "../lib/conversation-origins";
import type { Session } from "../lib/types";
import { cx } from "./atoms";
import { BrandMark } from "./BrandMark";
import { Icon, type IconName } from "./icons";

/** `size` is the tile's; the apps' tile is filled, the others tinted. */
export function ChannelMark({ c, size }: { c: Pick<Channel, "id" | "kind">; size: number }) {
  if (c.id === "feishu" || c.id === "lark_agent") return <BrandTile brand="lark" size={size} />;
  const icon: IconName = c.kind === "app" ? "spark" : c.kind === "voice" ? "mic" : c.kind === "mention" ? "at" : "chat";
  return (
    <span className={cx("flex shrink-0 items-center justify-center", c.kind === "app" ? "bg-accent text-white" : "bg-accent-tint text-accent-ink")} style={{ width: size, height: size, borderRadius: radius(size) }}>
      <Icon name={icon} size={Math.round(size * 0.48)} />
    </span>
  );
}

const OpenerTile = lazy(() => import("./OpenerTile").then((m) => ({ default: m.OpenerTile })));

/** What opened a conversation the hub opened by itself (lib/conversation-origins.ts), in a channel mark's place and size:
 *  Lark's tile for Lark /agent, as its channel's (#3758); else its group's icon in the secondary ink, the one All chats'
 *  filter for it names (Inbox, Scheduled, From a chat, Extensions, Other; #5195). One per row, in place of the channel's.
 *  An extension's conversation (Lark /agent's or another's) whose `session` names the extension that opened it is drawn
 *  with that extension's own icon once it is known (OpenerTile, #10358): a third-party extension that reuses lark.agent
 *  is not drawn as Lark. Named "From Lark /agent" / "From an extension" / the group's name, as its tooltip too, and
 *  "From <the extension's name>" while its icon is drawn, which `onName` tells the words beside it (`openerFrom`);
 *  `decorative` beside words that say it already (under the first message). */
export function OriginMark({ origin, size, decorative, className, session, onName }: { origin: AutoOrigin; size: number; decorative?: boolean; className?: string; session?: Pick<Session, "opened_by" | "origin_ref" | "manager">; onName?: (name: string | undefined) => void }) {
  const brand = ORIGIN_BRAND[origin];
  const [name, setName] = useState<string>();
  const label = openerFrom(origin, name);
  const mark = brand ? <BrandTile brand={brand} size={size} /> : <Icon name={ORIGIN_ICON[origin]} size={size - GLYPH_INSET} className="text-ink-2" />;
  const by = openerOf(origin, session);
  useEffect(() => {
    onName?.(name);
    return () => onName?.(undefined);
  }, [onName, name]);
  return (
    <span data-origin-mark={origin} {...(decorative ? { "aria-hidden": true } : { role: "img", "aria-label": label, title: label })} className={cx("flex shrink-0 items-center justify-center", className)} style={{ width: size, height: size }}>
      {by ? <Suspense fallback={mark}><OpenerTile id={by} manager={session?.manager} brand={brand} size={size} radius={radius(size)} onName={setName}>{mark}</OpenerTile></Suspense> : mark}
    </span>
  );
}

/** How much smaller than its box an icon is drawn, so its weight matches a tile's: 14 in a row's 16, as SourceTag draws
 *  an agent's source, 12 beside a message's 14. */
const GLYPH_INSET = 2;

function BrandTile({ brand, size }: { brand: BrandId; size: number }) {
  return (
    <span className="flex shrink-0 items-center justify-center bg-surface shadow-hairline" style={{ width: size, height: size, borderRadius: radius(size) }}>
      <BrandMark brand={brand} size={size} fit="tile" />
    </span>
  );
}

/** The tile's corner: 9 at a row's 32, in proportion below (a 14 px mark beside a message). */
function radius(size: number): number {
  return Math.max(3, Math.round(size * 0.28));
}
