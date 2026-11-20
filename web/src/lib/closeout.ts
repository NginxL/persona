// An agent's closeout (docs/assistant/agent-work.md "Closeout", #3612): the record it ends a turn with, saying whether the
// work is done, what changed, whether it is merged and deployed, what was verified and what is missing. It is the
// agent's own claim. Where the hub looked itself (`checked`), its finding stands beside the claim: confirmed,
// contradicted, or not checked. The session's page draws it as a card (views/chat/CloseoutCard.tsx), list rows as a
// short suffix of their line (lib/format.ts sessionPreview).
import { t } from "../i18n";
import type { Closeout, Session } from "./types";

export type CloseoutStatus = "done" | "not_done" | "blocked";

const STATUSES: readonly string[] = ["done", "not_done", "blocked"];

/** The closeout to show for a session: one with a status this page knows, while the session is not working (a new
 *  turn has no closeout yet, and the previous turn's no longer says where the work stands). */
export function shownCloseout(s: Pick<Session, "work" | "state"> | undefined): (Closeout & { status: CloseoutStatus }) | undefined {
  const c = s?.work?.closeout;
  if (!c || !STATUSES.includes(c.status)) return undefined;
  if (s.state === "working" || s.work?.phase === "working") return undefined;
  return c as Closeout & { status: CloseoutStatus };
}

/** How a claim stands against the hub's finding. `found`: the agent did not say, the hub's finding stands alone. */
export type ClaimState = "confirmed" | "contradicted" | "unchecked" | "found";

export interface Claim {
  /** What the agent said; undefined when it did not say. */
  said?: boolean;
  /** What the hub found; undefined when it did not look or could not tell. */
  found?: boolean;
  state: ClaimState;
}

const yesNo = (v: string | undefined): boolean | undefined => (v === "yes" ? true : v === "no" ? false : undefined);

/** The claim on merged or deployed with the hub's finding; undefined when neither the agent said nor the hub found. */
export function closeoutClaim(c: Closeout, what: "merged" | "deployed"): Claim | undefined {
  const said = yesNo(c[what]);
  const found = yesNo(c.checked?.[what]);
  if (said === undefined && found === undefined) return undefined;
  if (said === undefined) return { found, state: "found" };
  if (found === undefined) return { said, state: "unchecked" };
  return { said, found, state: said === found ? "confirmed" : "contradicted" };
}

/** Whether the hub found a claim of the closeout to be untrue. */
export function closeoutContradicted(c: Closeout): boolean {
  return closeoutClaim(c, "merged")?.state === "contradicted" || closeoutClaim(c, "deployed")?.state === "contradicted";
}

// One claim in a few words for a row: the hub's finding when it has one ("merged ✓"), else the agent's word as it
// said it ("merged"); a "yes" the hub found untrue is marked ("⚠ says merged, isn't").
function claimWords(c: Closeout, what: "merged" | "deployed"): string | undefined {
  const claim = closeoutClaim(c, what);
  if (!claim) return undefined;
  if (claim.state === "contradicted" && claim.said) return t(`lib-work.closeout_${what}_contradicted`);
  if (claim.found !== undefined) return t(claim.found ? `lib-work.closeout_${what}_confirmed` : `lib-work.closeout_${what}_no`);
  return t(claim.said ? `lib-work.closeout_${what}_yes` : `lib-work.closeout_${what}_no`);
}

/** "merged · not deployed": where the closeout stands on merged and deployed, for the end of a row's line; "" when it
 *  says nothing on either and the hub found nothing. */
export function closeoutSuffix(c: Closeout): string {
  return [claimWords(c, "merged"), claimWords(c, "deployed")].filter(Boolean).join(" · ");
}

/** A row's line from the closeout alone, for a turn the hub gave no line of its own (one it could not call finished):
 *  what a blocked one waits for, what a not-done one still needs; undefined for a done one. */
export function closeoutLine(c: Closeout): string | undefined {
  if (c.status === "blocked") return c.waiting ? t("lib-work.closeout_blocked_on", { waiting: c.waiting }) : t("lib-work.closeout_blocked");
  if (c.status === "not_done") return c.missing ? t("lib-work.closeout_not_done_missing", { missing: c.missing }) : t("lib-work.closeout_not_done");
  if (c.status === "done") return t("lib-work.closeout_done");
  return undefined;
}
