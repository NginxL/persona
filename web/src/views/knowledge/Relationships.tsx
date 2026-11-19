import { useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, enc, errorText } from "../../lib/api";
import { importanceTone, linkGroups, type KBDetail, type KBDetailResponse, type KBEntry } from "../../lib/kb";
import { matchesRemovalEnd, relationshipAllowance, relationshipRemoval, removalConfirmed, removalUpdate, type Allowance, type Removal } from "../../lib/kb-relationships";
import { href } from "../../lib/router";
import { Button, Dialog, IconButton, Switch } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Card, importanceLabel, MoreRow, onPlainClick, SectionTitle, Tile, TonePill } from "./KBParts";

type Props = {
  detail: KBDetail;
  name: string;
  auth: { token: string; tokenOwner: string; headers: Record<string, string> };
  current: () => boolean;
  onOpen: (e: KBEntry) => void;
  onRefreshed: (response: KBDetailResponse) => void;
  onSaved: () => void;
};
const SHOWN = 4;

/** The confirmation outlives a disappearing row after a partial write. Route and owner changes remount its parent. */
export function Relationships(props: Props) {
  const [openKinds, setOpenKinds] = useState<Set<string>>(new Set());
  const [intent, setIntent] = useState<Removal | null>(null);
  const [confirmed, setConfirmed] = useState<"" | "excluded" | "locked">("");
  const [dialogOpen, setDialogOpen] = useState(false);
  return <>
    {intent && !dialogOpen && <Card>
      <div role="status" className="space-y-3 p-4 text-sm leading-relaxed text-ink-2">
        <p className="break-words">{t("knowledge-relationships.pending", { first: intent.ends[0].name, second: intent.ends[1].name })}</p>
        <Button onClick={() => props.current() && setDialogOpen(true)}>{t("knowledge-relationships.review")}</Button>
      </div>
    </Card>}
    {linkGroups(props.detail.links).map((g) => {
      const open = openKinds.has(g.kind);
      return <section key={g.kind}>
        <SectionTitle title={g.title} count={g.items.length} />
        <Card>
          {(open ? g.items : g.items.slice(0, SHOWN)).map((x) => {
            const removal = relationshipRemoval(props.detail, props.name, x);
            return <div key={`${x.kind}/${x.id}`} className="flex items-center pr-2">
              <a href={href({ name: "kb", kind: x.kind, id: x.id })} onClick={(ev) => onPlainClick(ev, () => props.onOpen(x))}
                className="flex min-w-0 flex-1 items-center gap-3 px-4 py-2.5 transition-colors hover:bg-hover">
                <Tile entry={x} size={30} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px] font-medium text-ink" title={x.name}>{x.name}</span>
                  {x.one_line && <span className="block truncate text-[12px] text-ink-3">{x.one_line}</span>}
                </span>
                {x.importance && <TonePill tone={importanceTone(x.importance)} strong>{importanceLabel(x.importance)}</TonePill>}
                <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3" />
              </a>
              {removal && <IconButton icon="x" label={t("knowledge-relationships.action")} disabled={intent !== null} onClick={() => {
                if (props.current() && !intent) { setConfirmed(""); setIntent(removal); setDialogOpen(true); }
              }} />}
            </div>;
          })}
          {g.items.length > SHOWN && <MoreRow open={open} label={t("knowledge-kbdetail.all_items", { count: g.items.length })} onClick={() => setOpenKinds((s) => {
            const n = new Set(s); if (n.has(g.kind)) n.delete(g.kind); else n.add(g.kind); return n;
          })} />}
        </Card>
      </section>;
    })}
    {confirmed && <p role="status" className="text-sm text-ink-2">{t(confirmed === "locked" ? "knowledge-relationships.success" : "knowledge-relationships.success_excluded")}</p>}
    <RemovedLinks {...props} onAllowed={() => setConfirmed("")} />
    {intent && <RemovalDialog {...props} intent={intent} open={dialogOpen} onClose={(keep) => { setDialogOpen(false); if (!keep) setIntent(null); }} onConfirmed={(locked) => { setConfirmed(locked ? "locked" : "excluded"); setIntent(null); setDialogOpen(false); props.onSaved(); }} />}
  </>;
}

/** The links the user removed (detail.removed): Persona keeps them out; Allow again lets it link them on its own. */
function RemovedLinks(props: Props & { onAllowed: () => void }) {
  const [busy, setBusy] = useState("");
  const [failure, setFailure] = useState<{ key: string; text: string } | null>(null);
  const rows = (props.detail.removed ?? []).flatMap((x) => {
    const allowance = relationshipAllowance(props.detail, x);
    return allowance ? [{ x, allowance, key: `${x.kind}/${x.id}` }] : [];
  });
  if (!rows.length) return null;
  const self = typeof props.detail.meta?.id === "string" ? props.detail.meta.id : props.detail.id;
  const allow = async (x: KBEntry, key: string, allowance: Allowance) => {
    if (busy || !props.current()) return;
    setBusy(key); setFailure(null);
    try {
      await api(`/kb/${enc(allowance.target.kind)}/${enc(allowance.target.id)}`, { ...props.auth, method: "PUT", json: allowance.update });
      if (!props.current()) return;
      const r = await api<KBDetailResponse>(`/kb/${enc(props.detail.kind)}/${enc(self)}`, props.auth);
      if (!props.current()) return;
      props.onRefreshed(r);
      if (r.detail.removed?.some((y) => y.kind === x.kind && y.id === x.id)) setFailure({ key, text: t("knowledge-relationships.allow_unconfirmed", { name: x.name }) });
      else { props.onAllowed(); props.onSaved(); }
    } catch (err) {
      if (props.current()) setFailure({ key, text: t("knowledge-relationships.allow_failed", { error: errorText(err) }) });
    } finally {
      setBusy("");
    }
  };
  return <section>
    <SectionTitle title={t("knowledge-relationships.removed_title")} count={rows.length} />
    <Card>
      <p className="px-4 pt-3 text-label leading-snug text-ink-3">{t("knowledge-relationships.removed_hint")}</p>
      {rows.map(({ x, allowance, key }) => <div key={key}>
        <div className="flex items-center gap-3 px-4 py-2.5">
          <Tile entry={x} size={30} />
          <span className="min-w-0 flex-1 truncate text-meta font-medium text-ink-2" title={x.name}>{x.name}</span>
          <Button size="sm" className="touch-hit" busy={busy === key} disabled={busy !== ""} aria-label={t("knowledge-relationships.allow_label", { name: x.name })} onClick={() => void allow(x, key, allowance)}>
            {t("knowledge-relationships.allow")}
          </Button>
        </div>
        {failure?.key === key && <p role="alert" className="break-words px-4 pb-2.5 text-label text-red">{failure.text}</p>}
      </div>)}
    </Card>
  </section>;
}

function RemovalDialog({ intent, open, auth, current, onRefreshed, onClose, onConfirmed }: Props & { intent: Removal; open: boolean; onClose: (keep: boolean) => void; onConfirmed: (locked: boolean) => void }) {
  const [phase, setPhase] = useState<"confirm" | "writing" | "reading" | "retry-write" | "read-error" | "unconfirmed">("confirm");
  // Off by default: only this link stays out. Fixed once the first write goes out, so a retry sends the same intent.
  const [lockLists, setLockLists] = useState(false);
  const [error, setError] = useState("");
  const busy = useRef(false);
  const alive = useRef(true);
  const retired = useRef(false);
  const visible = useRef(open);
  visible.current = open;
  const reads = useRef(new AbortController());
  const attempt = useRef({ accepted: false, error: "" });
  useEffect(() => {
    alive.current = true;
    reads.current = new AbortController();
    return () => { alive.current = false; reads.current.abort(); };
  }, []);
  const valid = () => alive.current && !retired.current && current();
  const close = () => {
    if (busy.current || !visible.current || !valid()) return;
    // Fence retained DOM callbacks before React commits dismissal.
    visible.current = false;
    retired.current = phase === "confirm";
    onClose(!retired.current);
  };

  const readback = async () => {
    if (!valid()) return;
    setPhase("reading");
    const results = await Promise.allSettled(intent.ends.map((end) => api<KBDetailResponse>(`/kb/${enc(end.kind)}/${enc(end.id)}`, { ...auth, signal: reads.current.signal })));
    if (!valid()) return;
    const details: KBDetail[] = [];
    let failure = "";
    results.forEach((result, i) => {
      if (result.status === "rejected") { failure ||= errorText(result.reason); return; }
      const r = result.value;
      if (!matchesRemovalEnd(intent, r.detail, i)) { failure ||= t("knowledge-confirmedfacts.entry_changed"); return; }
      details[i] = r.detail;
      // Publish authoritative current detail even if reading the other end failed.
      if (i === 0) onRefreshed(r);
    });
    busy.current = false;
    if (failure) {
      setError(failure); setPhase("read-error");
    } else if (removalConfirmed(intent, details, lockLists)) {
      onConfirmed(lockLists);
    } else {
      setError(attempt.current.error);
      setPhase(attempt.current.accepted ? "unconfirmed" : "retry-write");
    }
  };
  const remove = async () => {
    if (busy.current || !visible.current || !valid() || (phase !== "confirm" && phase !== "retry-write")) return;
    busy.current = true;
    setPhase("writing"); setError("");
    attempt.current = { accepted: false, error: "" };
    try {
      await api(`/kb/${enc(intent.target.kind)}/${enc(intent.target.id)}`, { ...auth, method: "PUT", json: removalUpdate(intent, lockLists) });
      attempt.current.accepted = true;
    } catch (err) {
      attempt.current.error = errorText(err);
    }
    // A failed multi-entity write can already have changed either end. Reread before offering the same idempotent intent.
    if (valid()) await readback();
  };
  const retryRead = () => { if (!busy.current && visible.current && valid()) { busy.current = true; void readback(); } };
  const working = phase === "writing" || phase === "reading";
  return <Dialog open={open} onClose={close} title={t("knowledge-relationships.title")} footer={<>
    <Button tone="quiet" disabled={working} onClick={close}>{t(phase === "confirm" ? "common.cancel" : "ui-atoms.close")}</Button>
    {(phase === "confirm" || phase === "retry-write") && <Button tone="danger" onClick={() => void remove()}>{t(phase === "confirm" ? "knowledge-relationships.remove" : "knowledge-relationships.retry_remove")}</Button>}
    {(phase === "read-error" || phase === "unconfirmed") && <Button onClick={retryRead}>{t("knowledge-relationships.retry_read")}</Button>}
  </>}>
    <div className="space-y-3 break-words text-sm leading-relaxed text-ink-2">
      <p>{t("knowledge-relationships.confirm", { first: intent.ends[0].name, second: intent.ends[1].name })}</p>
      <p>{t("knowledge-relationships.keep")}</p>
      <div className="rounded-control bg-field p-3">
        <p className="font-medium text-ink">{t("knowledge-relationships.exclude_title")}</p>
        <p className="mt-2">{t("knowledge-relationships.exclude", { first: intent.ends[0].name, second: intent.ends[1].name })}</p>
        <div className="mt-3 flex items-center gap-3">
          <span className="min-w-0 flex-1 font-medium text-ink">{t("knowledge-relationships.lock_option")}</span>
          <Switch on={lockLists} onChange={setLockLists} disabled={phase !== "confirm"} label={t("knowledge-relationships.lock_option")} />
        </div>
        {lockLists && <>
          {intent.ends.map((end) => <p key={`${end.kind}/${end.id}`} className="mt-2">{t("knowledge-relationships.lock", { name: end.name, list: t(`knowledge-relationships.list_${end.field}`) })}</p>)}
          <p className="mt-2">{t("knowledge-relationships.lock_hint")}</p>
        </>}
      </div>
      {working && <p role="status">{t(phase === "writing" ? "knowledge-relationships.removing" : "knowledge-relationships.reading")}</p>}
      {phase === "read-error" && <p role="alert">{t(attempt.current.accepted ? "knowledge-relationships.accepted_read_error" : "knowledge-relationships.rejected_read_error", { error, writeError: attempt.current.error })}</p>}
      {phase === "retry-write" && <p role="alert">{t("knowledge-relationships.partial", { error })}</p>}
      {phase === "unconfirmed" && <p role="alert">{t("knowledge-relationships.unconfirmed")}</p>}
    </div>
  </Dialog>;
}
