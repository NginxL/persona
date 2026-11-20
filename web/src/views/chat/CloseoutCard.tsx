// A stopped agent's closeout, above its transcript (#3612, docs/assistant/agent-work.md "Closeout"): whether it says the work
// is done, what changed, what it produced, whether it is merged and deployed, what it verified and what it still needs.
// It is the agent's own claim and the card says so. Beside "merged" and "deployed" stands what the hub found when it
// looked itself: confirmed, contradicted (in the caution color, with the warning mark: color is never the only signal)
// or not checked. It is not part of the transcript: the reply that carried the raw block shows without it while this
// card is there (lib/closeout.ts stripCloseout).
// Verified commands get only the facts recorded in this closeout's own exchange, never a badge: CloseoutEvidence, a chunk
// of its own, so its matching code and strings stay off the first screen (#8118).
import { useEffect, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { closeoutClaim, shownCloseout, type Claim } from "../../lib/closeout";
import { clickLink, linkHref } from "../../lib/links";
import type { Segment } from "../../lib/turns";
import type { Item, Session } from "../../lib/types";
import { Collapse, cx } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { prLink } from "../TodoStageBar";
import { CloseoutEvidence, closeoutEvidenceChunk, loadCloseoutEvidence, onCloseoutEvidenceChunk } from "./closeout-chunks";

const KIND_ICON: Record<string, IconName> = { pr: "pullRequest", commit: "commit", branch: "branch", doc: "file", build: "tool", url: "link" };

// The evidence chunk starts loading as soon as the store holds a session whose closeout a card would show, before any
// card mounts (preloadCloseout, #8139). Session lists carry only where a closeout stands; did and Verified come
// with the session's own read, so the chunk is normally in before a card has a Verified line to draw the row under. When
// that read still comes first, the card keeps drawing without its did, Verified and evidence rows until the chunk is in
// and so is the closeout's exchange (the row's facts come from it): HOLD_MS at most, and never again once they showed.
// The card grows once, as it does when the read arrives, and not again when the chunk or the transcript lands. After a
// failed load they draw without the row, which joins them when a later card's load brings the chunk in.
const HOLD_MS = 1000;

type ExpandFold = (fold: Item) => Promise<boolean | void>;

/** An artifact's reference as a web link, when it is one (http or https only). */
function webLink(ref: string): string | undefined {
  try {
    const u = new URL(ref.trim());
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : undefined;
  } catch {
    return undefined;
  }
}

export function CloseoutCard({ session, segment, expandFold }: { session: Session; segment?: Segment; expandFold?: ExpandFold }) {
  const c = shownCloseout(session);
  const [open, setOpen] = useState(true);
  const evidenceChunk = useSyncExternalStore(onCloseoutEvidenceChunk, closeoutEvidenceChunk, closeoutEvidenceChunk);
  const hasVerified = Boolean(c?.verified);
  const waiting = hasVerified && (evidenceChunk === "idle" || evidenceChunk === "loading" || !segment);
  const [released, release] = useState(false);
  const hold = waiting && !released;
  useEffect(() => {
    if (hasVerified) loadCloseoutEvidence();
  }, [hasVerified]);
  useEffect(() => {
    if (!hasVerified) return;
    if (!waiting) {
      release(true);
      return;
    }
    const timer = setTimeout(() => release(true), HOLD_MS);
    return () => clearTimeout(timer);
  }, [hasVerified, waiting]);
  if (!c) return null;
  const merged = closeoutClaim(c, "merged");
  const deployed = closeoutClaim(c, "deployed");
  const contradicted = merged?.state === "contradicted" || deployed?.state === "contradicted";
  const artifacts = session.work?.artifacts ?? [];
  // held: drawn as the session list has it, without what the session's own read brought
  const did = hold ? [] : (c.did ?? []);
  const verified = hold ? undefined : c.verified;
  const status = c.status === "blocked" && c.waiting ? t("chat-chatview.closeout_blocked_on", { waiting: c.waiting }) : t(`chat-chatview.closeout_status_${c.status}`);
  const hasBody = did.length > 0 || artifacts.length > 0 || Boolean(merged || deployed || verified || c.missing);
  return (
    // the chat column's own width and side padding, so the card lines up with the transcript under it
    <div className="mx-auto w-full max-w-[880px] shrink-0 px-5 pt-3 sm:px-8">
      <section data-closeout={c.status} data-closeout-contradicted={contradicted || undefined} aria-label={t("chat-chatview.closeout_label")} className="overflow-hidden rounded-card bg-surface shadow-card">
        <button
          type="button"
          disabled={!hasBody}
          aria-expanded={hasBody ? open : undefined}
          onClick={() => setOpen((o) => !o)}
          className={cx("flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left pointer-coarse:min-h-11", hasBody && "cursor-pointer hover:bg-hover")}
        >
          <span className={cx("flex shrink-0 items-center", contradicted ? "text-orange" : c.status === "done" ? "text-green" : "text-ink-2")}>
            <Icon name={contradicted ? "warning" : c.status === "done" ? "check" : c.status === "blocked" ? "clock" : "info"} size={14} stroke={2.2} />
          </span>
          <span className="min-w-0 truncate text-[13px] font-semibold text-ink" title={status}>{status}</span>
          <span className="min-w-0 flex-1 truncate text-[11.5px] text-ink-3">{t("chat-chatview.closeout_own_claim")}</span>
          {hasBody && (
            <span className="flex shrink-0 items-center text-ink-3" aria-hidden>
              <Icon name="chevronRight" size={13} stroke={2.2} className="transition-transform duration-150 motion-reduce:transition-none" style={{ transform: open ? "rotate(90deg)" : "none" }} />
            </span>
          )}
        </button>
        {hasBody && (
          <Collapse open={open}>
            <dl className="grid max-h-[40vh] grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5 overflow-y-auto border-t border-line px-3 py-2.5 text-[12.5px] leading-snug">
              {did.length > 0 && (
                <Field label={t("chat-chatview.closeout_did")}>
                  {did.length === 1 ? did[0] : <ul className="flex list-disc flex-col gap-0.5 pl-4">{did.map((line, i) => <li key={i}>{line}</li>)}</ul>}
                </Field>
              )}
              {artifacts.length > 0 && (
                <Field label={t("chat-chatview.closeout_artifacts")}>
                  <span className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
                    {artifacts.map((a, i) => <Artifact key={i} kind={a.kind} reference={a.ref} />)}
                  </span>
                </Field>
              )}
              {merged && <Field label={t("chat-chatview.closeout_merged")}><ClaimLine what="merged" claim={merged} /></Field>}
              {deployed && <Field label={t("chat-chatview.closeout_deployed")}><ClaimLine what="deployed" claim={deployed} /></Field>}
              {verified && <Field label={t("chat-chatview.closeout_verified")}>{verified}</Field>}
              {verified && evidenceChunk === "in" && <CloseoutEvidence session={session} verified={verified} open={open} segment={segment} expandFold={expandFold} />}
              {c.missing && <Field label={t("chat-chatview.closeout_missing")}>{c.missing}</Field>}
            </dl>
          </Collapse>
        )}
      </section>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <>
      <dt className="whitespace-nowrap text-ink-3">{label}</dt>
      <dd className="min-w-0 text-ink-2 [overflow-wrap:anywhere]">{children}</dd>
    </>
  );
}

function Artifact({ kind, reference }: { kind: string; reference: string }) {
  const url = webLink(reference);
  const icon = KIND_ICON[kind] ?? "link";
  if (!url) {
    return (
      <span className="inline-flex min-w-0 items-center gap-1 text-ink-2">
        <Icon name={icon} size={12} className="shrink-0 text-ink-3" />
        <span className="min-w-0 font-mono [overflow-wrap:anywhere]">{reference}</span>
      </span>
    );
  }
  // a pull or merge request reads "PR #12", as on a task's page; any other link reads as its address
  const pr = kind === "pr" ? prLink(url) : null;
  const text = pr?.text ?? url.replace(/^https?:\/\//, "");
  return (
    <a
      href={linkHref(url)}
      target="_blank"
      rel="noreferrer noopener"
      aria-label={pr?.label}
      title={url}
      onClick={(e) => clickLink(e, url, text)}
      className="inline-flex min-w-0 max-w-full items-center gap-1 font-medium text-accent-ink hover:underline"
    >
      <Icon name={icon} size={12} className="shrink-0" />
      <span className="min-w-0 truncate">{text}</span>
    </a>
  );
}

/** What the agent said, then what the hub found: three states that differ in words, mark and color. */
function ClaimLine({ what, claim }: { what: "merged" | "deployed"; claim: Claim }) {
  const said = claim.said === undefined ? t("chat-chatview.closeout_not_said") : t(claim.said ? "chat-chatview.closeout_yes" : "chat-chatview.closeout_no");
  const found = claim.found === undefined ? "" : t(`chat-chatview.closeout_found_${what}_${claim.found ? "yes" : "no"}`);
  return (
    <span data-closeout-claim={what} data-state={claim.state} className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
      <span className="text-ink">{said}</span>
      {claim.state === "confirmed" ? (
        <span className="inline-flex items-center gap-1 text-green">
          <Icon name="check" size={12} stroke={2.4} className="shrink-0" />
          {t("chat-chatview.closeout_confirmed")}
        </span>
      ) : claim.state === "contradicted" ? (
        <span className="inline-flex items-center gap-1 rounded-control bg-orange-tint px-1.5 font-medium text-orange">
          <Icon name="warning" size={12} stroke={2.2} className="shrink-0" />
          {found}
        </span>
      ) : claim.state === "found" ? (
        <span className="text-ink-2">{found}</span>
      ) : (
        <span className="text-ink-3">{t("chat-chatview.closeout_unchecked")}</span>
      )}
    </span>
  );
}
