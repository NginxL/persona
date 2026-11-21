import { Button } from "../../ui/atoms";
import { t } from "../../i18n";

/** The heading shown only while a conversation list is being arranged (#12017). */
export function ConversationArrangeBar({ custom, onReset, onDone }: { custom: boolean; onReset: () => void; onDone: () => void }) {
  return (
    <div data-conversation-arrange className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-control bg-inset px-3 py-2 shadow-hairline">
      {/* One lazy style per arrange surface; a CSS import would add its asset to both first-screen preload graphs. */}
      <style>{".conversation-order-content [data-row-age],.conversation-order-content [aria-haspopup=menu]{display:none}"}</style>
      <div className="min-w-36 flex-1">
        <h2 className="text-label font-medium text-ink">{t("conversation-order.arrange_title")}</h2>
        <p className="mt-0.5 text-submeta leading-[1.5] text-ink-3">{t("conversation-order.arrange_hint")}</p>
      </div>
      <div className="ml-auto flex max-w-full flex-wrap items-center justify-end gap-2">
        {custom && <Button tone="quiet" size="sm" onClick={onReset}>{t("conversation-order.order_reset")}</Button>}
        <Button tone="primary" size="sm" onClick={onDone}>{t("conversation-order.arrange_done")}</Button>
      </div>
    </div>
  );
}
