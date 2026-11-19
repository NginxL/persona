// An agent's row in a list of a machine's or a space's agents: its state, title, tab and latest line; opens its chat.
import { sessionPreview, sessionTitle, stateOf } from "../../lib/format";
import { agentMeta, loudState } from "../../lib/machine-detail";
import { plainMarkdown } from "../../lib/plain-markdown";
import { href, onLinkClick } from "../../lib/router";
import type { Session } from "../../lib/types";
import { StatePill } from "../../ui/atoms";
import { StatusMark } from "../../ui/StatusMark";
import { Icon } from "../../ui/icons";

export function AgentRow({ s }: { s: Session }) {
  const st = stateOf(s);
  const to = { name: "session" as const, mid: s.machine_id, sid: s.session_id };
  const meta = agentMeta(s);
  // Markdown on one line, without its marks (#10939)
  const preview = plainMarkdown(sessionPreview(s));
  return (
    <a href={href(to)} onClick={(e) => onLinkClick(e, to)} className="group flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-hover">
      <span className="flex size-4 shrink-0 items-center justify-center">
        <StatusMark state={st} />
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-baseline gap-2">
          <span className="truncate text-[13px] font-medium text-ink">{sessionTitle(s)}</span>
          {meta && <span className="hidden shrink-0 font-mono text-[11px] text-ink-3 sm:inline">{meta}</span>}
        </span>
        {(preview || meta) && (
          <span className="mt-0.5 block truncate text-[12px] text-ink-3">
            {meta && <span className="font-mono text-[11px] sm:hidden">{meta}{preview ? " · " : ""}</span>}
            {preview}
          </span>
        )}
      </span>
      {loudState(st) && <StatePill state={st} />}
      <Icon name="chevronRight" size={13} stroke={2.2} className="shrink-0 text-ink-3 transition-transform duration-150 group-hover:translate-x-0.5" />
    </a>
  );
}
