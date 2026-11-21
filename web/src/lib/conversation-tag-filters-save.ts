// Optimistic hub write for the exact tag-filter entries. Null restores automatic mode; [] is a real custom empty set.
import { api } from "./api";
import { getState, patchAssistant, refreshAssistant } from "./store";
import type { AssistantInfo } from "./types";

export async function saveConversationTagFilters(filters: string[] | null): Promise<void> {
  const before = getState().assistant?.conversation_tag_filters;
  patchAssistant({ conversation_tag_filters: filters });
  try {
    const answer = await api<AssistantInfo>("/assistant", { method: "PUT", json: { conversation_tag_filters: filters } });
    if (answer.conversation_tag_filters === undefined) throw new Error("hub does not support conversation tag filters");
    patchAssistant({ conversation_tag_filters: answer.conversation_tag_filters });
  } catch (error) {
    patchAssistant({ conversation_tag_filters: before });
    await refreshAssistant().catch(() => {});
    throw error;
  }
}
