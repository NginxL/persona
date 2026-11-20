// "Computers": every computer the hub drives and every way it reaches each one (paths), adding computers, persona-link pairing,
// re-pairing a connector, reconnect, remove, switching an ssh computer to Persona Link (#10662, views/machines/LinkSwitch.tsx).
// New computers come through the node service (docs/archive/operations/plan-hub-center.md 0,
// 4): the computer dials the hub (kind link, a persona-link pairing link) or the hub dials the computer's connector
// (kind service, the persona://connector link it prints). Both connect at once; removing waits for a restart.
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { tRich } from "../../i18n/rich";
import { ago } from "../../lib/format";
import { Button, Collapse, cx, Dialog, IconButton, toast, useNow } from "../../ui/atoms";
import { adminApi, AdminError, addLinkMachine, addServiceMachine, errorText, linkPair, repairConnector } from "../api";
import { AdminIcon, type AdminIconName } from "../icons";
import { linkSwitchRunning } from "../../lib/link-switch";
import type { MachineLinkSwitch } from "../../lib/types";
import { LinkSwitch } from "../../views/machines/LinkSwitch";
import { canRemove, countdown, isLegacy, linuxInstallCommand, machineStatus, needsRepair, parseConnectorLink, pathRows, validHostPort, validMachineId, validProxy, viaLabel } from "../logic";
import { devDialog, navigate } from "../router";
import { share, useShared } from "../store";
import type { AdminMachine, LinkPair, MachinesView, MeshView, Restart } from "../types";
import { Card, CodeBlock, ConfirmButton, CopyValue, Dot, EmptyState, ErrorBox, Field, Loader, Notice, PageHeader, Segmented, Tag, useLoad, useVisiblePoll } from "../parts";

const enc = encodeURIComponent;

type AddWay = "link" | "service";

const LEGACY_TIP = t("admin-pages-machines.legacy_tip");

export function Machines() {
  const { mesh, hubId, tls } = useShared();
  const { data, error, loading, reload } = useLoad(async () => {
    const v = await adminApi<MachinesView>("/machines");
    share({ restart: v.restart });
    return v;
  });
  const [adding, setAdding] = useState<AddWay | null>(() => {
    const d = devDialog();
    return d === "add-link" || d === "add" ? "link" : d === "add-service" ? "service" : null;
  });
  const [pairing, setPairing] = useState<AdminMachine | null>(null);
  const [repairing, setRepairing] = useState<AdminMachine | null>(null);

  // Dev only (screenshots): ?dialog=pair / repair opens it on the first computer it fits.
  const [devOpened, setDevOpened] = useState(false);
  useEffect(() => {
    const d = devDialog();
    if (!data || devOpened || (d !== "pair" && d !== "repair")) return;
    setDevOpened(true);
    if (d === "pair") setPairing(data.machines.find((m) => m.kind === "link") ?? null);
    else setRepairing(data.machines.find(needsRepair) ?? null);
  }, [data, devOpened]);

  // Online state and the paths' round trips move on their own; a switch to Persona Link moves faster (#10662).
  const switching = data?.machines.some((m) => linkSwitchRunning(m.link_switch)) ?? false;
  useVisiblePoll(reload, switching ? 3_000 : 15_000, Boolean(data));

  const changed = (r?: Restart) => {
    if (r) share({ restart: r });
    reload();
  };

  return (
    <>
      <PageHeader title={t("admin-pages-machines.title")} subtitle={t("admin-pages-machines.subtitle")} />
      <AddCard onPick={setAdding} noTLS={tls === null} />
      {!data ? (
        loading ? <Loader /> : <ErrorBox error={error} onRetry={reload} />
      ) : (
        <>
          {/* A failed poll keeps the last list on screen; say so, or its online dots and round trips look live. */}
          {error != null && <ErrorBox error={error} onRetry={reload} />}
          <Card
            title={t("admin-pages-machines.count", { count: data.machines.length })}
            footer={t("admin-pages-machines.list_footer")}
          >
            {data.machines.length === 0 ? (
              <EmptyState icon="machine" title={t("admin-pages-machines.empty_title")}>
                {t("admin-pages-machines.empty_body")}
              </EmptyState>
            ) : (
              data.machines.map((m) => (
                <MachineRow key={m.id} m={m} mesh={mesh} selfId={hubId} tls={data.tls} onChanged={changed} onPair={() => setPairing(m)} onRepair={() => setRepairing(m)} />
              ))
            )}
          </Card>
        </>
      )}
      <AddLink open={adding === "link"} onClose={() => setAdding(null)} existing={data?.machines.map((m) => m.id) ?? []} onAdded={() => changed()} />
      <AddService open={adding === "service"} onClose={() => setAdding(null)} existing={data?.machines.map((m) => m.id) ?? []} onAdded={() => changed()} />
      <PairLinkDialog machine={pairing} onClose={() => setPairing(null)} />
      <RepairDialog machine={repairing} onClose={() => setRepairing(null)} onDone={() => changed()} />
    </>
  );
}

// AddCard is the page's lead: the two ways a computer joins, side by side.
function AddCard({ onPick, noTLS }: { onPick: (w: AddWay) => void; noTLS: boolean }) {
  const ways: { w: AddWay; icon: AdminIconName; title: string; fit: string; desc: string; note?: string }[] = [
    {
      w: "link",
      icon: "link",
      title: t("admin-pages-machines.way_link"),
      fit: t("admin-pages-machines.way_link_fit"),
      desc: t("admin-pages-machines.way_link_desc"),
      note: noTLS ? t("admin-pages-machines.way_link_needs_tls") : undefined,
    },
    {
      w: "service",
      icon: "machine",
      title: t("admin-pages-machines.way_service"),
      fit: t("admin-pages-machines.way_service_fit"),
      desc: t("admin-pages-machines.way_service_desc"),
    },
  ];
  return (
    <section className="mb-6">
      <div className="mb-1.5 flex min-h-6 items-end px-1">
        <h2 className="text-[12px] font-medium text-ink-3">{t("admin-pages-machines.add_computer")}</h2>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        {ways.map((o) => (
          <button
            key={o.w}
            type="button"
            onClick={() => onPick(o.w)}
            className="group flex items-start gap-3 rounded-card bg-surface px-3.5 py-3.5 text-left shadow-card transition-[background-color,transform] duration-150 hover:bg-hover active:scale-[0.99]"
          >
            <span className="flex size-9 shrink-0 items-center justify-center rounded-[10px] bg-accent-tint text-accent-ink">
              <AdminIcon name={o.icon} size={17} />
            </span>
            <span className="min-w-0 flex-1">
              <span className="flex items-center gap-2">
                <span className="text-[13.5px] font-semibold text-ink">{o.title}</span>
                {o.note && <Tag tone="orange">{o.note}</Tag>}
              </span>
              <span className="mt-0.5 block text-[12px] font-medium text-ink-2">{o.fit}</span>
              <span className="mt-1 block text-[11.5px] leading-relaxed text-ink-3">{o.desc}</span>
            </span>
            <AdminIcon name="chevronRight" size={15} className="mt-2.5 shrink-0 text-ink-3 transition-transform duration-150 group-hover:translate-x-0.5" />
          </button>
        ))}
      </div>
    </section>
  );
}

function MachineRow({
  m,
  mesh,
  selfId,
  tls,
  onChanged,
  onPair,
  onRepair,
}: {
  m: AdminMachine;
  mesh: MeshView | null | undefined;
  selfId: string;
  tls: boolean;
  onChanged: (r?: Restart) => void;
  onPair: () => void;
  onRepair: () => void;
}) {
  const st = machineStatus(m);
  const [busy, setBusy] = useState("");
  const active = m.state === "active";
  const act = async (what: string, fn: () => Promise<{ restart?: Restart }>, ok: string) => {
    setBusy(what);
    try {
      const r = await fn();
      toast(ok, "ok");
      onChanged(r.restart);
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setBusy("");
    }
  };
  const paths = pathRows(m, mesh, selfId);
  const now = paths.find((p) => p.active);
  // How it is reached now: the path in use (through a member: which one), else how it is configured.
  const how = now ? (now.via === "hub" ? t("admin-pages-machines.via_member", { hub: now.hub }) : now.label) : viaLabel(m.transport) || viaLabel(m.kind);
  const member = m.kind === "hub" ? paths[0]?.hub || mesh?.hubs.find((h) => h.hub_id === m.hub_id)?.name || "" : "";
  const repair = needsRepair(m);
  const icon: AdminIconName = m.kind === "hub" ? "mesh" : m.kind === "link" ? "link" : "machine";
  // A computer of this hub's hub.json (a member's is renamed on its own hub), once it runs.
  const renamable = active && m.kind !== "hub";
  const [editing, setEditing] = useState(false);
  return (
    <div className={cx("px-4 py-3", m.state === "pending_remove" && "opacity-60")}>
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <span className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-[10px] bg-inset text-ink-2 shadow-hairline">
          <AdminIcon name={icon} size={16} />
        </span>
        <div className="min-w-[200px] flex-1">
          {editing ? (
            <RenameForm m={m} onDone={(saved) => (setEditing(false), saved && onChanged())} />
          ) : (
            <div className="flex min-w-0 items-center gap-2">
              <span className="truncate text-[13.5px] font-medium text-ink">{m.name || m.id}</span>
              <span className="shrink-0 font-mono text-[11.5px] text-ink-3">{m.id}</span>
              {renamable && <IconButton icon="write" label={t("admin-pages-machines.rename")} size={24} iconSize={12} onClick={() => setEditing(true)} />}
            </div>
          )}
          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11.5px] text-ink-3">
            <span className="flex items-center gap-1.5 whitespace-nowrap">
              <Dot tone={st.tone} />
              <span className={st.tone === "red" ? "text-red" : st.tone === "green" ? "text-green" : st.tone === "orange" ? "text-orange" : "text-ink-2"}>{st.label}</span>
            </span>
            {[
              <span key="how" className="whitespace-nowrap">
                {how}
                {now?.rtt !== undefined && m.online && <span className="ml-1.5 font-mono tabular-nums">{now.rtt} ms</span>}
              </span>,
              m.address && (
                <span key="addr" className="font-mono">
                  {m.address}
                </span>
              ),
              m.proxy && (
                <span key="proxy" className="whitespace-nowrap">
                  {tRich("admin-pages-machines.via_proxy", { proxy: <span className="font-mono">{m.proxy}</span> })}
                </span>
              ),
              m.ssh_host && (
                <span key="ssh" className="font-mono">
                  {m.ssh_host}
                </span>
              ),
              m.node_version && (
                <span key="ver" className="font-mono">
                  {m.node_version}
                </span>
              ),
              !m.online && active && m.last_seen && (
                <span key="seen" className="whitespace-nowrap">
                  {t("admin-pages-machines.last_seen", { time: ago(m.last_seen) })}
                </span>
              ),
            ]
              .filter(Boolean)
              .flatMap((el, i) => [<span key={`sep${i}`}>·</span>, el])}
            {m.node_mismatch && <Tag tone="orange" title={t("admin-pages-machines.version_mismatch_hint")}>{t("admin-pages-machines.version_mismatch")}</Tag>}
            {isLegacy(m) && <Tag title={LEGACY_TIP}>{t("admin-pages-machines.legacy_tag")}</Tag>}
          </div>
          {member && <div className="mt-1 text-[11.5px] leading-relaxed text-ink-3">{t("admin-pages-machines.member_only", { member })}</div>}
          {m.error && active && <div className="mt-1 text-[11.5px] leading-relaxed break-words text-red">{m.error}</div>}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {repair && active && (
            <Button tone="primary" className="h-7 px-2.5" onClick={onRepair}>
              <AdminIcon name="link" size={13} />
              {t("admin-pages-machines.repair")}
            </Button>
          )}
          {m.kind === "link" && active && (
            <Button className="h-7 px-2.5" onClick={onPair} title={tls ? t("admin-pages-machines.pair_link_hint") : t("admin-pages-machines.pair_link_no_tls")}>
              <AdminIcon name="link" size={13} />
              {t("admin-pages-machines.make_pair_link")}
            </Button>
          )}
          {active && (
            <IconButton
              icon="retry"
              label={t("admin-pages-machines.reconnect")}
              disabled={busy !== ""}
              onClick={() => act("reconnect", () => adminApi(`/machines/${enc(m.id)}/reconnect`, "POST"), t("admin-pages-machines.reconnecting"))}
            />
          )}
          {canRemove(m) && (
            <ConfirmButton
              label={t("admin-pages-machines.delete")}
              confirm={t("admin-pages-machines.delete")}
              busy={busy === "delete"}
              onConfirm={() => act("delete", () => adminApi(`/machines/${enc(m.id)}`, "DELETE"), m.state === "pending_add" ? t("admin-pages-machines.withdrawn") : t("admin-pages-machines.deleted"))}
            />
          )}
        </div>
      </div>
      {active && (!m.hub_id || !selfId || m.hub_id === selfId) && (
        <LinkSwitch
          machineId={m.id}
          name={m.name || m.id}
          state={m.link_switch}
          post={() => adminApi<{ link_switch?: MachineLinkSwitch }>(`/machines/${enc(m.id)}/link-switch`, "POST")}
          refresh={() => onChanged()}
          className="mt-2 ml-12 max-w-[560px]"
        />
      )}
      {paths.length > 1 && <Paths rows={paths} />}
    </div>
  );
}

// RenameForm edits what the hub calls a computer, in place of its name: written to hub.json and taken at once, without
// a restart or a reconnect (docs/operations/admin.md "Pages"). Empty is its id again.
function RenameForm({ m, onDone }: { m: AdminMachine; onDone: (saved: boolean) => void }) {
  const [value, setValue] = useState(m.name && m.name !== m.id ? m.name : "");
  const [busy, setBusy] = useState(false);
  const save = async (e?: React.FormEvent) => {
    e?.preventDefault();
    setBusy(true);
    try {
      const r = await adminApi<{ name: string }>(`/machines/${enc(m.id)}`, "PUT", { name: value.trim() });
      toast(t("admin-pages-machines.renamed", { name: r.name }), "ok");
      onDone(true);
    } catch (err) {
      toast(errorText(err), "error");
      setBusy(false);
    }
  };
  return (
    <form onSubmit={save} className="flex flex-wrap items-center gap-2" onKeyDown={(e) => e.key === "Escape" && (e.stopPropagation(), onDone(false))}>
      <input
        autoFocus
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={m.id}
        aria-label={t("admin-pages-machines.rename_label", { id: m.id })}
        maxLength={40}
        className="h-8 w-[220px] max-w-full rounded-control bg-inset px-2.5 text-[13px] text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-[0_0_0_1px_var(--line-strong),0_0_0_3px_var(--accent-tint)]"
      />
      <Button tone="quiet" className="h-8" onClick={() => onDone(false)}>
        {t("common.cancel")}
      </Button>
      <Button tone="primary" type="submit" className="h-8" busy={busy}>
        {t("common.save")}
      </Button>
      <span className="w-full text-[11.5px] text-ink-3">{t("admin-pages-machines.rename_hint")}</span>
    </form>
  );
}

// Paths: every way to a computer — how, through which hub, whether it works, its round trip, the one in use.
function Paths({ rows }: { rows: ReturnType<typeof pathRows> }) {
  return (
    <div className="mt-2 ml-12 max-w-[560px] divide-y divide-line overflow-hidden rounded-control bg-inset shadow-hairline" role="list" aria-label={t("admin-pages-machines.paths")}>
      {rows.map((r) => (
        <div key={r.key} role="listitem" className="px-2.5 py-1.5 text-[11.5px]">
          <div className="flex items-center gap-2.5">
            <Dot tone={r.ok ? "green" : r.error ? "red" : "gray"} />
            <span className="w-[68px] shrink-0 whitespace-nowrap text-ink-2">{r.label}</span>
            <span className="flex min-w-0 flex-1 items-center gap-1 text-ink-3" title={t("admin-pages-machines.path_through", { hub: r.hub })}>
              <AdminIcon name="mesh" size={11} className="shrink-0" />
              <span className="truncate">{r.hub}</span>
            </span>
            {r.rtt !== undefined && <span className="shrink-0 font-mono text-ink-3 tabular-nums">{r.rtt} ms</span>}
            {r.active ? <Tag tone="accent">{t("admin-pages-machines.path_active")}</Tag> : r.ok ? <Tag>{t("admin-pages-machines.path_standby")}</Tag> : <Tag tone="red">{t("admin-pages-machines.path_down")}</Tag>}
          </div>
          {r.error && (
            <div className="mt-0.5 truncate pl-[18px] text-red" title={r.error}>
              {r.error}
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

const ICONS: { value: string; label: string }[] = [
  { value: "laptop", label: t("admin-pages-machines.icon_laptop") },
  { value: "desktop", label: t("admin-pages-machines.icon_desktop") },
  { value: "server", label: t("admin-pages-machines.icon_server") },
];

// MachineFields: id, name and icon, the same for both ways.
function MachineFields({
  id,
  setId,
  name,
  setName,
  icon,
  setIcon,
  existing,
  autoFocus,
  example,
}: {
  example: { id: string; name: string };
  id: string;
  setId: (v: string) => void;
  name: string;
  setName: (v: string) => void;
  icon: string;
  setIcon: (v: string) => void;
  existing: string[];
  autoFocus?: boolean;
}) {
  const idErr = idError(id, existing);
  return (
    <>
      <div className="grid gap-3.5 sm:grid-cols-2">
        <Field label="id" hint={t("admin-pages-machines.id_hint")} value={id} onChange={(v) => setId(v.toLowerCase())} placeholder={example.id} mono error={idErr || undefined} autoFocus={autoFocus} maxLength={63} />
        <Field label={t("admin-pages-machines.name")} hint={t("admin-pages-machines.name_hint")} value={name} onChange={setName} placeholder={example.name} maxLength={40} />
      </div>
      <div>
        <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("admin-pages-machines.icon")}</span>
        <Segmented label={t("admin-pages-machines.icon")} value={icon} options={ICONS} onChange={setIcon} />
      </div>
    </>
  );
}

function idError(id: string, existing: string[]): string {
  if (!id) return "";
  if (!validMachineId(id)) return t("admin-pages-machines.id_invalid");
  return existing.includes(id) ? t("admin-pages-machines.id_taken") : "";
}

// AddLink: the computer dials the hub. Make the entry, then the persona-link pairing link right away.
function AddLink({ open, onClose, existing, onAdded }: { open: boolean; onClose: () => void; existing: string[]; onAdded: () => void }) {
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [icon, setIcon] = useState("laptop");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [added, setAdded] = useState<AdminMachine | null>(null);
  const ok = id && !idError(id, existing);
  const close = () => {
    onClose();
    setTimeout(() => (setId(""), setName(""), setIcon("laptop"), setError(""), setAdded(null)), 200);
  };
  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!ok || busy) return;
    setBusy(true);
    setError("");
    try {
      const r = await addLinkMachine({ id, name: name.trim() || id, icon });
      setAdded(r.machine?.id ? r.machine : { id, name: name.trim() || id, kind: "link", icon, online: false, state: "active" });
      onAdded();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={close}
      title={added ? t("admin-pages-machines.pair_title", { name: added.name || added.id }) : t("admin-pages-machines.add_link_title")}
      width={540}
      footer={
        added ? (
          <Button tone="primary" onClick={close}>
            {t("admin-pages-machines.done")}
          </Button>
        ) : (
          <>
            <Button tone="quiet" onClick={close}>
              {t("common.cancel")}
            </Button>
            <Button tone="primary" busy={busy} disabled={!ok} onClick={() => submit()}>
              {t("admin-pages-machines.add_and_pair")}
            </Button>
          </>
        )
      }
    >
      {added ? (
        <PairPanel machine={added} onClose={close} />
      ) : (
        <form onSubmit={submit} className="space-y-3.5">
          <p className="text-[12.5px] leading-relaxed text-ink-2">
            {t("admin-pages-machines.add_link_body")}
          </p>
          <MachineFields example={{ id: "laptop", name: t("admin-pages-machines.icon_laptop") }} id={id} setId={setId} name={name} setName={setName} icon={icon} setIcon={setIcon} existing={existing} autoFocus />
          {error && <FormError text={error} />}
          <button type="submit" hidden />
        </form>
      )}
    </Dialog>
  );
}

type Platform = "mac" | "linux" | "windows";

// PairPanel makes a persona-link pairing link for a link computer and says how to use it on each platform.
function PairPanel({ machine, onClose }: { machine: AdminMachine; onClose: () => void }) {
  const [pair, setPair] = useState<LinkPair | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [round, setRound] = useState(0);
  const [platform, setPlatform] = useState<Platform>(machine.icon === "server" ? "linux" : "mac");
  const now = useNow(1000, Boolean(pair));
  useEffect(() => {
    let live = true;
    setPair(null);
    setError(null);
    linkPair(machine.id).then(
      (p) => live && setPair(p),
      (e) => live && setError(e),
    );
    return () => {
      live = false;
    };
  }, [machine.id, round]);
  const left = pair ? countdown(pair.expires_at, now) : "";
  const tls = error instanceof AdminError && error.code === "tls_required";
  if (tls)
    return (
      <Notice tone="orange" icon="lock" title={t("admin-pages-machines.link_needs_tls")} action={<Button onClick={() => (onClose(), navigate("network"))}>{t("admin-pages-machines.enable")}</Button>}>
        {t("admin-pages-machines.link_needs_tls_body")}
      </Notice>
    );
  if (error)
    return (
      <div className="flex flex-wrap items-center gap-3 py-3">
        <p className="min-w-0 flex-1 text-[12.5px] text-red">{errorText(error)}</p>
        <Button icon="retry" onClick={() => setRound((r) => r + 1)}>
          {t("admin-pages-machines.retry")}
        </Button>
      </div>
    );
  if (!pair) return <div className="py-8 text-center text-[12.5px] text-ink-3">{t("admin-pages-machines.making_pair_link")}</div>;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <p className="min-w-0 flex-1 text-[12.5px] leading-relaxed text-ink-2">
          {left
            ? tRich("admin-pages-machines.pair_left", { left: <span className="font-mono text-ink tabular-nums">{left}</span> })
            : tRich("admin-pages-machines.pair_expired", { expired: <span className="text-red">{t("admin-pages-machines.expired")}</span> })}
        </p>
        <Button tone={left ? "quiet" : "primary"} icon="retry" className="h-7 px-2.5" onClick={() => setRound((r) => r + 1)}>
          {t("admin-pages-machines.regenerate")}
        </Button>
      </div>
      <div className={cx("rounded-control bg-inset p-2.5 shadow-hairline transition-opacity", !left && "opacity-50")}>
        <div className="mb-1 text-[11px] font-medium text-ink-3">{t("admin-pages-machines.pair_link")}</div>
        <CopyValue value={pair.pair_url} label={t("admin-pages-machines.copy_pair_link")} />
      </div>
      <div>
        <Segmented
          label={t("admin-pages-machines.platform")}
          value={platform}
          onChange={setPlatform}
          options={[
            { value: "mac", label: "Mac" },
            { value: "linux", label: "Linux" },
            { value: "windows", label: "Windows" },
          ]}
        />
        <div className="mt-2.5 text-[12.5px] leading-relaxed text-ink-2" style={{ animation: "fade-in 160ms ease-out both" }} key={platform}>
          {platform === "mac" && (
            <ol className="list-decimal space-y-1 pl-4 marker:text-ink-3">
              <li>{t("admin-pages-machines.mac_step1")}</li>
              <li>{t("admin-pages-machines.mac_step2")}</li>
              <li>{t("admin-pages-machines.mac_step3")}</li>
            </ol>
          )}
          {platform === "linux" && (
            <div className="space-y-2">
              <p>{t("admin-pages-machines.linux_body")}</p>
              <CodeBlock value={linuxInstallCommand(pair.pair_url)} copyLabel={t("admin-pages-machines.copy_command")} />
              <p className="text-[11.5px] text-ink-3">{t("admin-pages-machines.linux_linger")}</p>
            </div>
          )}
          {platform === "windows" && <p>{t("admin-pages-machines.windows_body")}</p>}
        </div>
      </div>
      <p className="text-[11.5px] leading-relaxed text-ink-3">{t("admin-pages-machines.pair_link_warning")}</p>
    </div>
  );
}

function PairLinkDialog({ machine, onClose }: { machine: AdminMachine | null; onClose: () => void }) {
  return (
    <Dialog open={Boolean(machine)} onClose={onClose} title={machine ? t("admin-pages-machines.pair_link_title", { name: machine.name || machine.id }) : ""} width={540}>
      {machine && <PairPanel machine={machine} onClose={onClose} />}
    </Dialog>
  );
}

// ConnectorLinkField: the persona://connector link pasted from `persona-link pair --listen`, read back to the user.
function ConnectorLinkField({ value, onChange, autoFocus }: { value: string; onChange: (v: string) => void; autoFocus?: boolean }) {
  const parsed = value.trim() ? parseConnectorLink(value) : null;
  const bad = value.trim() !== "" && !parsed;
  return (
    <div>
      <Field label={t("admin-pages-machines.pair_link")} value={value} onChange={onChange} placeholder="persona://connector?addr=…&fp=…&code=…" mono multiline autoFocus={autoFocus} error={bad ? t("admin-pages-machines.connector_link_invalid") : undefined} />
      {parsed && (
        <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11.5px] text-ink-3" style={{ animation: "fade-in 160ms ease-out both" }}>
          <span>
            {tRich("admin-pages-machines.address_value", {
              value: <span className="font-mono text-ink-2">{parsed.addrs.length ? parsed.addrs.join(t("admin-pages-machines.address_separator")) : t("admin-pages-machines.address_missing")}</span>,
            })}
          </span>
          <span className="min-w-0 truncate">
            {tRich("admin-pages-machines.fingerprint_value", { value: <span className="font-mono text-ink-2">{parsed.fp.slice(0, 12)}…</span> })}
          </span>
        </div>
      )}
    </div>
  );
}

// AddService: the hub dials the computer's connector. The hub pairs with the pasted link first (up to ~40 s), then
// writes hub.json and connects.
function AddService({ open, onClose, existing, onAdded }: { open: boolean; onClose: () => void; existing: string[]; onAdded: () => void }) {
  const [link, setLink] = useState("");
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [icon, setIcon] = useState("server");
  const [address, setAddress] = useState("");
  const [proxy, setProxy] = useState("");
  const [more, setMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [added, setAdded] = useState<AdminMachine | null>(null);

  const parsed = link.trim() ? parseConnectorLink(link) : null;
  const needAddress = Boolean(parsed && parsed.addrs.length === 0);
  const addrErr = address.trim() && !validHostPort(address) ? t("admin-pages-machines.address_invalid_example") : "";
  const proxyErr = proxy.trim() && !validProxy(proxy) ? t("admin-pages-machines.proxy_invalid") : "";
  const ok = parsed && id && !idError(id, existing) && !addrErr && !proxyErr && (!needAddress || address.trim());
  useEffect(() => {
    if (needAddress) setMore(true);
  }, [needAddress]);

  const close = () => {
    if (busy) return;
    onClose();
    setTimeout(() => (setLink(""), setId(""), setName(""), setIcon("server"), setAddress(""), setProxy(""), setMore(false), setError(""), setAdded(null)), 200);
  };
  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!ok || busy) return;
    setBusy(true);
    setError("");
    try {
      const r = await addServiceMachine({ id, name: name.trim() || id, icon, pair_link: link.trim(), address: address.trim() || undefined, proxy: proxy.trim() || undefined });
      setAdded(r.machine?.id ? r.machine : { id, name: name.trim() || id, kind: "service", icon, online: false, state: "active", address: address.trim() || parsed?.addrs[0] });
      onAdded();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={close}
      title={added ? t("admin-pages-machines.added") : t("admin-pages-machines.add_service_title")}
      width={560}
      footer={
        added ? (
          <Button tone="primary" onClick={close}>
            {t("admin-pages-machines.done")}
          </Button>
        ) : (
          <>
            <Button tone="quiet" onClick={close} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button tone="primary" busy={busy} disabled={!ok} onClick={() => submit()}>
              {t("admin-pages-machines.pair_and_add")}
            </Button>
          </>
        )
      }
    >
      {added ? (
        <div className="flex items-start gap-3 py-1" style={{ animation: "fade-in 160ms ease-out both" }}>
          <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-green-tint text-green">
            <AdminIcon name="check" size={17} stroke={2.4} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="text-[13.5px] font-medium text-ink">{t("admin-pages-machines.paired_with", { name: added.name || added.id })}</div>
            <p className="mt-0.5 text-[12.5px] leading-relaxed text-ink-2">{t("admin-pages-machines.paired_connecting")}</p>
            {added.address && (
              <p className="mt-1.5 text-[11.5px] text-ink-3">
                {tRich("admin-pages-machines.address_value", { value: <span className="font-mono text-ink-2">{added.address}</span> })}
                {added.proxy && (
                  <>
                    {" "}
                    · {tRich("admin-pages-machines.via_proxy", { proxy: <span className="font-mono text-ink-2">{added.proxy}</span> })}
                  </>
                )}
              </p>
            )}
          </div>
        </div>
      ) : (
        <form onSubmit={submit} className="space-y-3.5">
          <div>
            <p className="mb-2 text-[12.5px] leading-relaxed text-ink-2">{t("admin-pages-machines.add_service_body")}</p>
            <CodeBlock value="persona-link pair --listen" copyLabel={t("admin-pages-machines.copy_command")} />
            <p className="mt-1.5 text-[11.5px] leading-relaxed text-ink-3">
              {t("admin-pages-machines.add_service_note")}
            </p>
          </div>
          <ConnectorLinkField value={link} onChange={setLink} autoFocus />
          <MachineFields example={{ id: "example-workstation", name: t("admin-pages-machines.example_workstation") }} id={id} setId={setId} name={name} setName={setName} icon={icon} setIcon={setIcon} existing={existing} />
          <div>
            <button type="button" onClick={() => setMore((v) => !v)} aria-expanded={more} className="flex items-center gap-1 text-[12px] font-medium whitespace-nowrap text-ink-2 hover:text-ink">
              <AdminIcon name="chevronRight" size={13} className="transition-transform duration-200" style={{ transform: more ? "rotate(90deg)" : undefined }} />
              {needAddress ? t("admin-pages-machines.address_and_proxy") : t("admin-pages-machines.address_and_proxy_optional")}
            </button>
            <Collapse open={more}>
              <div className="grid gap-3.5 pt-3 sm:grid-cols-2">
                <Field
                  label={needAddress ? t("admin-pages-machines.address") : t("admin-pages-machines.address_optional")}
                  hint={needAddress ? t("admin-pages-machines.address_required_hint") : t("admin-pages-machines.address_optional_hint")}
                  value={address}
                  onChange={setAddress}
                  placeholder={parsed?.addrs[0] || "192.0.2.30:18663"}
                  mono
                  error={addrErr || undefined}
                />
                <Field label={t("admin-pages-machines.proxy_optional")} hint={t("admin-pages-machines.proxy_hint")} value={proxy} onChange={setProxy} placeholder="socks5://192.0.2.5:1080" mono error={proxyErr || undefined} />
              </div>
            </Collapse>
          </div>
          {busy && <p className="text-[12.5px] text-ink-3">{t("admin-pages-machines.pairing_long")}</p>}
          {error && <FormError text={error} />}
          <button type="submit" hidden />
        </form>
      )}
    </Dialog>
  );
}

// RepairDialog pairs a service computer's connector again: it was paired with another hub, or reinstalled.
function RepairDialog({ machine, onClose, onDone }: { machine: AdminMachine | null; onClose: () => void; onDone: () => void }) {
  const [link, setLink] = useState("");
  const [address, setAddress] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!machine) return;
    setLink("");
    setAddress("");
    setError("");
  }, [machine]);
  const parsed = link.trim() ? parseConnectorLink(link) : null;
  const addrErr = address.trim() && !validHostPort(address) ? t("admin-pages-machines.address_invalid") : "";
  const ok = parsed && !addrErr;
  const close = () => !busy && onClose();
  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!machine || !ok || busy) return;
    setBusy(true);
    setError("");
    try {
      const r = await repairConnector(machine.id, link.trim(), address.trim());
      toast(r.restart_required ? t("admin-pages-machines.repaired_restart") : t("admin-pages-machines.repaired"), "ok");
      onDone();
      onClose();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={Boolean(machine)}
      onClose={close}
      title={machine ? t("admin-pages-machines.repair_title", { name: machine.name || machine.id }) : ""}
      width={540}
      footer={
        <>
          <Button tone="quiet" onClick={close} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy} disabled={!ok} onClick={() => submit()}>
            {t("admin-pages-machines.repair")}
          </Button>
        </>
      }
    >
      <form onSubmit={submit} className="space-y-3.5">
        <p className="text-[12.5px] leading-relaxed text-ink-2">
          {tRich("admin-pages-machines.repair_body", { command: <code className="font-mono text-[12px] text-ink">persona-link pair --listen</code> })}
        </p>
        <ConnectorLinkField value={link} onChange={setLink} autoFocus />
        <Field label={t("admin-pages-machines.address_optional")} hint={t("admin-pages-machines.repair_address_hint")} value={address} onChange={setAddress} placeholder={machine?.address || "192.0.2.30:18663"} mono error={addrErr || undefined} />
        {busy && <p className="text-[12.5px] text-ink-3">{t("admin-pages-machines.pairing")}</p>}
        {error && <FormError text={error} />}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

function FormError({ text }: { text: string }) {
  return (
    <div role="alert" className="flex items-start gap-2 rounded-control bg-red-tint px-3 py-2 text-[12.5px] leading-relaxed break-words text-red" style={{ animation: "fade-in 160ms ease-out both" }}>
      <AdminIcon name="alert" size={14} className="mt-0.5 shrink-0" />
      <span className="min-w-0">{text}</span>
    </div>
  );
}
