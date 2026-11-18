// A Persona chat's own pick kept on the hub (`assistant_model_pick`, #5033): PUT and DELETE
// /assistant/conversations/{id}/model. The model panel picks and goes back to the default with these, and a model error's
// card offers the way back too (views/chat/ModelPill.tsx useAssistantTurnFix). Apart from lib/assistant-chat.ts, which
// this page's own picks keep without the store or the network.
import { t } from "../i18n";
import { toast } from "../ui/atoms";
import { api, errorText } from "./api";
import { getSession, upsertSession } from "./store";
import type { Session } from "./types";

export const conversationModelPath = (s: Session) => `/assistant/conversations/${encodeURIComponent(s.session_id)}/model`;

/** DELETE …/model: the conversation follows the default again (another chat: keeps what it runs). Drawn at once, put
 *  back with the hub's words if it says no. */
export async function dropConversationPick(s: Session): Promise<boolean> {
  const before = s.model_pick;
  upsertSession({ ...s, model_pick: undefined });
  try {
    await api(conversationModelPath(s), { method: "DELETE" });
    return true;
  } catch (e) {
    upsertSession({ ...(getSession(s.machine_id, s.session_id) ?? s), model_pick: before });
    toast(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    return false;
  }
}
