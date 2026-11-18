// Why a task's Agent did not start, in words for everyone (#8237; iOS `StartFailureWords`). The hub's reason is often a
// raw error ("rpc: connection closed", "context deadline exceeded"); that stays for the task's details. The hand-off card
// and the Needs you row say the cause in a few plain words when the error names a known one, keep a sentence the hub
// already wrote for people (in the owner's language, or a whole English sentence), and otherwise say to open it. The
// Needs you row reads its plan task's code first (lib/start-retry.ts startFailureCodeLine, #10365).
import { t } from "../i18n";

export type StartFailureCause = "connection_lost" | "offline" | "timed_out" | "not_allowed" | "needs_update";

/** The known cause in the raw text, by the words Go, the node and the hub's own sentences use (the hub's
 *  `connectionReason` groups, plus a dropped link); undefined when none is known. */
export function startFailureCause(raw: string): StartFailureCause | undefined {
  const text = raw.toLowerCase();
  const has = (terms: string[]) => terms.some((term) => text.includes(term));
  if (has(["connection closed", "connection reset", "connection lost", "broken pipe", "node exited", "连接断了", "接続が切れ"]) || /(^|[^a-z])eof([^a-z]|$)/.test(text)) return "connection_lost"; // i18n-ignore: matches the hub's text
  if (has(["deadline exceeded", "timed out", "timeout", "超时", "タイムアウト"])) return "timed_out"; // i18n-ignore: matches the hub's text
  if (has(["permission denied", "unauthorized", "unauthorised", "forbidden", "access denied", "publickey", "authentication failed"])) return "not_allowed";
  if (has(["unknown method", "version mismatch", "protocol version", "incompatible"])) return "needs_update";
  if (has(["offline", "not connected", "no route", "unreachable", "connection refused", "no such host", "could not resolve", "离线", "不在线", "オフライン"])) return "offline"; // i18n-ignore: matches the hub's text
  return undefined;
}

/** Written for people: in Chinese or Japanese (the hub writes its sentences in the owner's language), or an English
 *  sentence that starts with a capital and ends with a full stop. Errors from programs are neither. */
export function readsAsSentence(text: string): boolean {
  if (/[\u3040-\u30ff\u4e00-\u9fff]/.test(text)) return true;
  return /^[A-Z]/.test(text) && text.includes(" ") && /[.!?]$/.test(text);
}

/** The reason for people: the cause in words, the hub's own sentence, or undefined (nothing readable: say to open it). */
export function startFailurePlain(raw: string): string | undefined {
  const text = raw.trim();
  if (!text) return undefined;
  const cause = startFailureCause(text);
  if (cause) return t(`assistant-tasks.start_cause_${cause}`);
  return readsAsSentence(text) ? text : undefined;
}

/** The hand-off card's line: "Didn't start: <why>", or that the details say why. */
export function startFailureCardLine(raw: string): string {
  const plain = startFailurePlain(raw);
  return plain ? t("assistant-tasks.start_failed", { error: plain }) : t("assistant-tasks.start_failed_details");
}

/** The Needs you row's line (its status word already says it did not start): why, or that the details say. */
export function startFailureRowLine(raw: string): string {
  return startFailurePlain(raw) ?? t("assistant-tasks.start_failed_details");
}
