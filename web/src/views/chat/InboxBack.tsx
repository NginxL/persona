// The way back to the Inbox item a full-page conversation belongs to. It shares the hand-off strip's appearance;
// dismissing it lasts only for this mounted conversation page.
import { useState } from "react";
import { t } from "../../i18n";
import { itemRoute, sameItem } from "../../lib/inbox";
import { inboxReturnTo } from "../../lib/inbox-nav";
import { useInboxOrigin, type InboxOrigin } from "../../lib/inbox-origin";
import { navigate } from "../../lib/router";
import type { Session } from "../../lib/types";
import { BackStrip } from "./HandoffBack";

export function inboxBackLabel(item: Pick<InboxOrigin, "title">): string {
  const title = item.title?.trim();
  return title ? t("chat-chatview.inbox_back_title", { title }) : t("chat-chatview.inbox_back_generic");
}

export function InboxBack({ mid, sid, session }: { mid: string; sid: string; session: Session | undefined }) {
  const item = useInboxOrigin(mid, sid, session);
  const [dismissed, setDismissed] = useState(false);
  if (!item || dismissed) return null;
  const back = () => {
    const from = inboxReturnTo(history.state);
    if (from && sameItem(from, item)) return history.back();
    navigate(itemRoute(item));
  };
  return <BackStrip inbox label={inboxBackLabel(item)} dismissLabel={t("chat-chatview.handoff_dismiss")} onBack={back} onDismiss={() => setDismissed(true)} />;
}
