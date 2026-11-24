// "Upload a checkup report" (docs/assistant/health.md "Checkup reports"): the picked files go to the assistant's machine, into the health space's
// reports/, and the words that start the conversation name them.
import { api, enc } from "../../lib/api";
import type { Session } from "../../lib/types";
import { MAX_FILE, quotePath, sendFile } from "../../lib/attachments";
import { t } from "../../i18n";

export const REPORT_FILES = "application/pdf,image/*";

/** Starts a conversation in the health space: the default manager's whichever the Persona page shows, as its list
 *  (`?space=` names no manager, docs/assistant/managers.md "Clients"). */
export function startHealthConversation(body: Record<string, unknown>) {
  return api<{ session: Session }>("/assistant/conversations?space=health", { method: "POST", json: { ...body, space: "health" } });
}

/** Uploads the files one after another and returns the first message of their conversation.
 *  A file over the limit or a failed upload throws: nothing is started for part of what was picked. */
export async function uploadReports(mid: string, files: File[], dest: "health"): Promise<string> {
  const big = files.find((f) => f.size > MAX_FILE);
  if (big) throw new Error(t("chat-composer.file_too_large"));
  const paths: string[] = [];
  for (const f of files) paths.push(quotePath(await sendFile(mid, f, { dest })));
  return t("health.report_prompt", { paths: paths.join(" ") });
}

/** One report in reports/ (GET /machines/{mid}/health/reports, docs/protocol/protocol.md): an original, its record, or both. */
export interface HealthReport {
  name: string;
  title: string;
  path?: string;
  record_path?: string;
  size: number;
  modified_at: string;
}

export const reportsPath = (mid: string) => `/machines/${enc(mid)}/health/reports`;
