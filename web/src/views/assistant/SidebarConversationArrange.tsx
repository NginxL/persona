// Loaded only after Arrange chats is chosen in the sidebar (#12017); the untouched Recent list stays in Sidebar.
import type { ReactNode } from "react";
import { t } from "../../i18n";
import { useConversationArrange } from "../../lib/use-conversation-arrange";
import type { Session } from "../../lib/types";
import { SubHeading } from "../SidebarRows";
import { conversationTitle } from "./conversations";
import { ConversationArrangeBar } from "./ConversationArrangeBar";
import { ConversationOrderRow } from "./ConversationOrderRow";

export function SidebarConversationArrange({ rows, row, onClose }: { rows: readonly Session[]; row: (s: Session) => ReactNode; onClose: () => void }) {
  const { saved, move, step, reset } = useConversationArrange(rows);
  return (
    <>
      <ConversationArrangeBar custom={!!saved?.length} onReset={reset} onDone={onClose} />
      <SubHeading label={t("sidebar.recent_chats")} help={t("sidebar.recent_chats_help")} />
      <div className="sidebar-indent">
        {rows.map((s, index) => (
          <ConversationOrderRow key={s.session_id} id={s.session_id} title={conversationTitle(s)} index={index} count={rows.length} onMove={move} onStep={step}>
            {row(s)}
          </ConversationOrderRow>
        ))}
      </div>
    </>
  );
}

export default SidebarConversationArrange;
