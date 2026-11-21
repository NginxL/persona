// The shared state for Arrange chats (#12017): immediate moves through one serialized hub writer, accessible steps,
// failure feedback, and Reset with an Undo that never replaces a newer arrangement.
import { useState } from "react";
import { errorText } from "./api";
import { conversationOrderAfterMove, conversationOrderAfterStep } from "./conversation-order-edit";
import { saveConversationOrder } from "./conversation-order-save";
import { getState, useStore } from "./store";
import type { Session } from "./types";
import { toast } from "../ui/atoms";
import { t } from "../i18n";

export function useConversationArrange(rows: readonly Session[]) {
  const saved = useStore((s) => s.assistant?.conversation_order);
  const [arranging, setArranging] = useState(false);
  const write = async (order: string[]) => {
    try {
      await saveConversationOrder(order);
    } catch (error) {
      toast(t("conversation-order.order_save_failed", { error: errorText(error) }), "error");
      throw error;
    }
  };
  const move = (from: string, to: string) => {
    void write(conversationOrderAfterMove(rows, saved, from, to)).catch(() => {});
  };
  const step = (id: string, by: -1 | 1) => {
    void write(conversationOrderAfterStep(rows, saved, id, by)).catch(() => {});
  };
  const reset = () => {
    const before = saved ?? [];
    if (!before.length) return;
    void write([]).then(() => {
      toast(t("conversation-order.order_reset_done"), "info", {
        label: t("conversation-menu.undo"),
        run: () => {
          const current = getState().assistant?.conversation_order ?? [];
          if (current.length) toast(t("conversation-order.order_undo_stale"), "info");
          else void write(before).catch(() => {});
        },
      });
    }).catch(() => {});
  };
  return { saved, arranging, start: () => setArranging(true), done: () => setArranging(false), move, step, reset };
}
