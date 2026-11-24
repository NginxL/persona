// A launcher summary stays visible when its model rows are folded (#10249).
import { t } from "../../i18n";
import { agentName } from "../../lib/agent-kind";
import { modelCount, type PanelProvider, type PanelState } from "../../lib/model-panel";
import { weeklyQuota, quotaReset, type LauncherQuotaAnswer } from "../../lib/quota";
import { cx } from "../atoms";
import { Icon } from "../icons";
import { ProviderMark } from "./ProviderMark";

export function LauncherRow({
  provider: p,
  state,
  quota: answer,
  expanded,
  frozen,
  onToggle,
  onReload,
  onHelp,
}: {
  provider: PanelProvider;
  state: PanelState;
  quota?: LauncherQuotaAnswer;
  expanded: boolean;
  frozen: boolean;
  onToggle: () => void;
  onReload?: (id: string) => void;
  onHelp?: (id: string) => void;
}) {
  const quota = weeklyQuota(answer);
  const current = state.current?.provider === p.id;
  const why = p.unavailable || p.locked;
  const label = p.name;
  const named = p.idNote ? `${label} (${p.idNote})` : label;
  const status = p.status && p.status !== why ? p.status : undefined;
  // the launcher the chat runs on says so and no more: its model is on the pill, in the header and lit in its list
  const detail = current
    ? ""
    : [
        agentName(p.engine, p.engineLabel),
        p.defaultModel || t("chat-modelpill.default_model"),
        p.models !== null && t("ui-modelpanel.model_count", { count: modelCount(p.models) }),
      ]
        .filter(Boolean)
        .join(" · ");
  const quotaText = quota
    ? [
        t("ui-modelpanel.weekly_left", {
          label: quota.window.label,
          percent: quota.remaining,
        }),
        quotaReset(quota.window.resets_at ?? ""),
      ]
        .filter(Boolean)
        .join(" · ")
    : "";
  return (
    <>
      <button
        type="button"
        data-panel-launcher={p.id}
        aria-label={label}
        aria-description={why || undefined}
        title={why ? `${named}: ${why}` : named}
        aria-expanded={expanded}
        disabled={frozen}
        onClick={onToggle}
        className={cx(
          "relative z-[1] flex min-h-11 w-full items-center gap-2 rounded-control px-3 py-2 text-left outline-none transition-colors duration-150 hover:bg-hover-2 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent",
          why && "text-ink-3",
          frozen && "cursor-not-allowed opacity-50",
        )}
      >
        <ProviderMark engine={p.engine} label={p.engineLabel} size={16} />
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-center gap-1.5 text-meta font-medium">
            <span className="min-w-0 truncate">{label}</span>
            {/* folded, why it can't take a pick is a mark and a tooltip, not a line under every such launcher (#12315) */}
            {why && !expanded && <Icon name="info" size={12} className="shrink-0 text-ink-3" />}
            {(current || p.isDefault) && <span className="shrink-0 text-submeta text-ink-3">{current ? t("ui-modelpanel.current_launcher") : t("ui-modelpanel.default_launcher")}</span>}
          </span>
          {detail && (
            <span className="block truncate text-submeta text-ink-3" title={detail}>
              {detail}
            </span>
          )}
          {p.idNote && <span className="block truncate font-mono text-submeta text-ink-3">{p.idNote}</span>}
          {p.alsoNote && <span className="block truncate text-submeta text-ink-3">{p.alsoNote}</span>}
        </span>
        {quota && (
          <span title={quotaText} aria-label={quotaText} className="flex shrink-0 flex-col items-end gap-1 text-submeta text-ink-3">
            <span>{quota.remaining}%</span>
            <span aria-hidden className="h-1 w-8 overflow-hidden rounded-full bg-hover-2">
              <span data-weekly-remaining={quota.remaining} className={cx("block h-full rounded-full", quota.tone === "red" ? "bg-red" : quota.tone === "orange" ? "bg-orange" : "bg-accent")} style={{ width: `${quota.remaining}%` }} />
            </span>
          </span>
        )}
        <Icon name="chevron" size={12} className={cx("shrink-0 text-ink-3", expanded && "rotate-180")} />
      </button>
      {((why && expanded) || status || p.error) && (
        <div className="relative z-[1] px-3 pb-1 text-submeta text-ink-3">
          {why && expanded && (
            <div className="break-words">
              {why}{" "}
              {p.unavailable && onHelp && (
                <button type="button" disabled={frozen} onClick={() => onHelp(p.id)} className="text-accent-ink hover:underline">
                  {t("ui-modelpanel.how_to_fix")}
                </button>
              )}
            </div>
          )}
          {status && <div className="break-words">{status}</div>}
          {p.error && (
            <div role="alert" className="break-words">
              {p.error}{" "}
              {onReload && (
                <button type="button" disabled={frozen} onClick={() => onReload(p.id)} className="text-accent-ink hover:underline">
                  {t("ui-modelpanel.retry")}
                </button>
              )}
            </div>
          )}
        </div>
      )}
    </>
  );
}
