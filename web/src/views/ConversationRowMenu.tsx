// Loaded when a sidebar conversation menu opens; the first screen does not carry its delete and arrange controls (#12017).
import { useState } from "react";
import { useCanDeleteConversation } from "../lib/conversation-delete";
import type { Session } from "../lib/types";
import { Menu } from "../ui/atoms";
import type { MenuAt } from "../ui/menu-model";
import { t } from "../i18n";
import { ConversationDeleteConfirmation, conversationMenuItems } from "./ConversationMenu";

/** The menu of a row (`at`: where a right click opened it; without it, beside `anchor`). */
export function ConversationRowMenu({ s, main, leader, builtIn, anchor, at, onClose, onArrange }: { s: Session; main: boolean; leader?: boolean; builtIn?: boolean; anchor: HTMLElement; at?: MenuAt; onClose: () => void; onArrange?: () => void }) {
  const p = at ?? { anchor };
  const canDelete = useCanDeleteConversation(s, main, builtIn);
  const [deleting, setDeleting] = useState(false);
  if (deleting) return <ConversationDeleteConfirmation s={s} onClose={onClose} />;
  const items = conversationMenuItems(s, { main, leader, builtIn, canDelete, onDelete: () => setDeleting(true) });
  if (onArrange && !builtIn) items.unshift({ key: "arrange", label: t("conversation-menu.arrange_chats"), icon: "menu", onSelect: onArrange });
  return <Menu anchor={p.anchor ?? null} point={p.point} onClose={onClose} align="right" width={200} items={items} />;
}

export default ConversationRowMenu;
