export const AGENT_MESSAGE_HOLD = "agent_message_hold";

export function isAgentMessagePost(method: string, path: string): boolean {
  return method === "POST" && /^\/api\/v1\/sessions\/[^/]+\/[^/]+\/messages$/.test(path);
}

/** A 202 alone is not a delivery receipt. Only accept custody for this exact Agent operation. */
export function heldReceipt202(body: string, operation: string): string {
  try {
    const reply = JSON.parse(body) as Record<string, Object>;
    if (reply.state === "unknown" || reply.delivered === "unknown") return "unknown";
    const held = reply.undelivered as Record<string, Object> | undefined;
    if (operation && reply.delivered === "later" && held?.id === operation && typeof held.computer === "string" &&
      held.computer.length > 0 && (held.state === "waiting" || held.state === "failed")) return "held";
  } catch { /* The operation remains unsettled until the hub says otherwise. */ }
  return "pending";
}

/** Only explicit refusal evidence permits replacing a dispatched write. */
export function writeResultUnknown(status: number, body = ""): boolean {
  if (body) {
    try {
      const data = JSON.parse(body) as Record<string, unknown>;
      if (data.state === "unknown" || data.state === "pending" || data.delivered === "unknown" || data.delivered === "yes") return true;
      if (data.delivered === "none") return false;
    } catch { /* An unreadable response is not proof of no side effect. */ }
  }
  return ![400, 401, 403, 404, 405, 409, 410, 413, 415, 422].includes(status);
}
