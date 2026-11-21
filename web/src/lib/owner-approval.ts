// An exact owner-action reply is an authenticated app send, not a card
// resolution and not text for the model. Only the server-owned proposal may
// supply its target; quoteReply and ordinary composer text never do.
import { t } from "../i18n";
import { errorText, sessionPath } from "./api";
import { uuid } from "./format";
import { apiOp } from "./ops";
import type { Interaction } from "./types";

export interface OwnerApprovalReply {
  ok?: boolean;
  approval_reply?: boolean;
  interaction_id?: string;
}

export interface OwnerApprovalAttempt {
  target: string;
  id: string;
}

const ownerApprovalAttempts = new Map<string, OwnerApprovalAttempt>();

/** One operation ID survives an uncertain request, remount and user retry; a different card starts a new operation. */
export function ownerApprovalAttempt(ix: Interaction, previous: OwnerApprovalAttempt | null): OwnerApprovalAttempt {
  if (previous?.target === ix.id) return previous;
  const existing = ownerApprovalAttempts.get(ix.id);
  if (existing) return existing;
  const attempt = { target: ix.id, id: uuid() };
  ownerApprovalAttempts.set(ix.id, attempt);
  return attempt;
}

/** Returns an error to show, or the empty string after the exact reply won. */
export async function replyOwnerApproval(ix: Interaction, phrase: string, clientMsgID = uuid()): Promise<string> {
  if (!ix.owner_proposal || !ix.approval_batch_id || !phrase || !ix.owner_approval_phrases?.includes(phrase) || !clientMsgID) {
    return t("chat-interactions.owner_reply_unavailable");
  }
  try {
    const reply = await apiOp<OwnerApprovalReply>(`${sessionPath(ix.machine_id, ix.session_id)}/messages`, {
      method: "POST",
      opId: clientMsgID,
      json: {
        text: phrase,
        client_msg_id: clientMsgID,
        approval_reply_to: ix.id,
      },
    });
    if (reply?.approval_reply !== true || reply.interaction_id !== ix.id) {
      return t("chat-interactions.owner_reply_unavailable");
    }
    ownerApprovalAttempts.delete(ix.id);
    return "";
  } catch (e) {
    return t("chat-interactions.send_failed", { error: errorText(e) });
  }
}
