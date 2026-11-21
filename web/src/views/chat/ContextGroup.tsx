import { Suspense, useState } from "react";
import { lazyPage } from "../../lib/lazy-page";
import type { Context } from "../../lib/turns";
import { Collapse, cx } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { t } from "../../i18n";

// What a message carries besides what the user wrote (picked page excerpts, everything a Feishu /agent request hands
// its agent): one folded row under the bubble; open, a list of titles; each title opens to its text.

export const CONTEXT_ICONS: Record<string, IconName> = { instructions: "info", doc: "book", edit: "pencil", skill: "spark", repository: "git" };
const ContextDetails = lazyPage(() => import("./ContextDetails").then((m) => m.ContextDetails));

export function contextLabel(c: Context): string {
  if (c.kind === "skill") return `${t("link-contextchips.skill")} · ${c.title ?? ""} · ${c.text.split("\n")[0]}`;
  return c.title || c.url || t("chat-contextgroup.quote");
}

export function contextSummary(cs: Context[]): string {
  if (cs.length === 1) return t(cs[0].kind === "edit" ? "chat-contextgroup.summary_edit" : "chat-contextgroup.summary_single", { label: contextLabel(cs[0]) });
  return t("chat-contextgroup.summary_many", { count: cs.length });
}

export function ContextGroup({ contexts }: { contexts: Context[] }) {
  const [open, setOpen] = useState(false);
  const one = contexts.length === 1;
  return (
    <div className={cx("flex max-w-full flex-col items-end", open && "w-[min(100%,560px)]")}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="flex max-w-full min-w-0 items-center gap-1.5 rounded-control px-2 py-1 text-[12px] whitespace-nowrap text-ink-3 transition-colors duration-100 hover:bg-hover-2 hover:text-ink-2"
      >
        <Icon name={one ? CONTEXT_ICONS[contexts[0].kind] ?? "quote" : "layers"} size={13} className="shrink-0" />
        <span className="min-w-0 truncate">{contextSummary(contexts)}</span>
        <Icon name="chevron" size={13} stroke={2.2} className="shrink-0 transition-transform duration-300" style={{ transform: open ? "rotate(180deg)" : "none" }} />
      </button>
      <Collapse open={open} lazy className="w-full">
        <Suspense fallback={null}><ContextDetails contexts={contexts} /></Suspense>
      </Collapse>
    </div>
  );
}
