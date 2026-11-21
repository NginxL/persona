// Optimistic hub write shared by the sidebar and All chats. The caller reports errors; a refresh reconciles a failed
// write with the hub and with any newer local move instead of blindly restoring an older snapshot.
import { api } from "./api";
import { getState, patchAssistant, refreshAssistant } from "./store";
import type { AssistantInfo } from "./types";

let writes: Promise<unknown> = Promise.resolve();
const same = (a: readonly string[] | undefined, b: readonly string[]) => !!a && a.length === b.length && a.every((id, i) => id === b[i]);

export async function saveConversationOrder(order: string[]): Promise<void> {
  const before = getState().assistant?.conversation_order;
  patchAssistant({ conversation_order: order });
  // Keep rapid drags in their invocation order. An older response must not overwrite a newer optimistic move.
  const write = writes.then(async () => {
    try {
      const answer = await api<AssistantInfo>("/assistant", { method: "PUT", json: { conversation_order: order } });
      // Empty order is omitted by the new hub; a non-empty write must be acknowledged, not mistaken for a reset by an old hub.
      if (answer.conversation_order == null && order.length) throw new Error("hub does not support conversation ordering");
      if (same(getState().assistant?.conversation_order, order)) patchAssistant({ conversation_order: answer.conversation_order ?? [] });
    } catch (error) {
      if (same(getState().assistant?.conversation_order, order)) {
        patchAssistant({ conversation_order: before });
        await refreshAssistant().catch(() => {});
      }
      throw error;
    }
  });
  writes = write.catch(() => {});
  return write;
}
