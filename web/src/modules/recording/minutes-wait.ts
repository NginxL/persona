/** What the minutes line says while the minutes wait (#13461, spec/fixtures/minutes-wait, docs/assistant/meeting-minutes.md
 *  "Gaps and the final pass"): the hub checking the whole recording while it finishes a hub-mode transcript, with the
 *  minutes done of the total once its final pass has started (`transcription.final_done` of `end`, recording seconds);
 *  writing the minutes otherwise. */
export type MinutesWait = { line: "checking"; done?: number; total?: number } | { line: "writing" };

const num = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);

/** The line while the minutes are `pending` and none are shown; null otherwise (the minutes, Updating, or their failure). */
export function minutesWait(transcription: unknown, minutesState: unknown, hasMinutes: boolean): MinutesWait | null {
  if (minutesState !== "pending" || hasMinutes) return null;
  if (!transcription || typeof transcription !== "object" || Array.isArray(transcription)) return { line: "writing" };
  const tr = transcription as Record<string, unknown>;
  if (tr.mode !== "hub" || tr.state !== "finishing") return { line: "writing" };
  const done = num(tr.final_done), end = num(tr.end);
  if (done === undefined || end === undefined || done < 0 || end <= 0) return { line: "checking" };
  const total = Math.max(1, Math.round(end / 60));
  return { line: "checking", done: Math.min(Math.floor(done / 60), total), total };
}
