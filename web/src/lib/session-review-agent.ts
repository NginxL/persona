// An independent, read-only review of one Agent session's work (#1808). The hub owns the reviewer and its lifecycle;
// the web asks for availability only while the session menu is open and starts one only from that menu.
import { api, enc } from "./api";
import { isSessionReview } from "./types";

export { isSessionReview };

export interface SessionReviewStatus {
  running: boolean;
  has_changes: boolean;
  started_at?: string;
}

export function sessionReviewRoute(mid: string, sid: string): string {
  return `/sessions/${enc(mid)}/${enc(sid)}/review`;
}

export function loadSessionReview(mid: string, sid: string): Promise<SessionReviewStatus> {
  return api<SessionReviewStatus>(sessionReviewRoute(mid, sid));
}

export function startSessionReview(mid: string, sid: string): Promise<unknown> {
  return api<unknown>(sessionReviewRoute(mid, sid), { method: "POST" });
}
