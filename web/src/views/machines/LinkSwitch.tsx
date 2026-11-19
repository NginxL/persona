// Switching an ssh computer to Persona Link (#10662), on the computer's page and the admin page's Machines row: for
// people who work with their computers' Agents. It shows only what the machine list says (`link_switch`; an older hub
// says nothing and this shows nothing). Available: one button, a confirmation, "Starting…" at once, then the steps as
// the list reports them, and how it ended; not available: why, in plain words. The page passes its own request (the
// app's `api` or the admin page's `adminApi`) and its own way to read its machine list again (lib/link-switch.ts).
import { useEffect, useState, type ReactNode } from "react";
import { t } from "../../i18n";
import { tRich } from "../../i18n/rich";
import { LINK_SWITCH_STEPS, linkSwitchNotice, linkSwitchRunning, retireLinkSwitch, shownLinkSwitch, startLinkSwitch, useLinkSwitchAttempt, type LinkSwitchNotice } from "../../lib/link-switch";
import type { MachineLinkSwitch } from "../../lib/types";
import { Button, cx, Dialog, Spinner } from "../../ui/atoms";
import { Steps } from "../../ui/Steps";

// Why a switch cannot start, or why one failed (the hub's codes, proto.LinkSwitchReason*), each a sentence of its own.
// linger_off names a command (below); an unknown code gets the caller's generic sentence.
const WHY: Record<string, string> = {
  offline: "machines-machinedetail.machine_switch_link_why_offline",
  unsupported_os: "machines-machinedetail.machine_switch_link_why_unsupported_os",
  link_unavailable: "machines-machinedetail.machine_switch_link_why_link_unavailable",
  hub_old: "machines-machinedetail.machine_switch_link_why_hub_old",
  pending: "machines-machinedetail.machine_switch_link_why_pending",
  custom_node: "machines-machinedetail.machine_switch_link_why_custom_node",
  unsafe_permissions: "machines-machinedetail.machine_switch_link_why_unsafe_permissions",
  hub_unanswered: "machines-machinedetail.machine_switch_link_why_hub_unanswered",
  busy: "machines-machinedetail.machine_switch_link_why_busy",
  in_progress: "machines-machinedetail.machine_switch_link_why_in_progress",
  no_systemd: "machines-machinedetail.machine_switch_link_why_no_systemd",
  existing_link: "machines-machinedetail.machine_switch_link_why_existing_link",
  no_node: "machines-machinedetail.machine_switch_link_why_no_node",
  no_package: "machines-machinedetail.machine_switch_link_why_no_package",
  transfer_failed: "machines-machinedetail.machine_switch_link_why_transfer_failed",
  install_failed: "machines-machinedetail.machine_switch_link_why_install_failed",
  link_timeout: "machines-machinedetail.machine_switch_link_why_link_timeout",
  config_changed: "machines-machinedetail.machine_switch_link_why_config_changed",
  interrupted: "machines-machinedetail.machine_switch_link_why_interrupted",
  unknown: "machines-machinedetail.machine_switch_link_why_unknown",
};
const UNAVAILABLE = "machines-machinedetail.machine_switch_link_why_unavailable";
// Passing reasons the page says in its own way already (offline, a node update running): nothing about a switch then.
const PASSING = new Set(["offline", "busy"]);

const STEP_LABEL: Record<(typeof LINK_SWITCH_STEPS)[number], string> = {
  checking: "machines-machinedetail.machine_switch_link_step_checking",
  installing: "machines-machinedetail.machine_switch_link_step_installing",
  connecting: "machines-machinedetail.machine_switch_link_step_connecting",
};

/** `user`: the account on that computer (its home folder's name), for the command linger_off asks an administrator to run. */
function why(code: string | undefined, fallback: string, user: string | undefined): ReactNode {
  if (code === "linger_off") {
    const command = `sudo loginctl enable-linger ${user || t("machines-machinedetail.machine_switch_link_linger_user")}`;
    return tRich("machines-machinedetail.machine_switch_link_why_linger_off", { command: <code className="font-mono">{command}</code> });
  }
  return t((code && WHY[code]) || fallback);
}

function noticeText(n: LinkSwitchNotice, user: string | undefined): ReactNode {
  if (n.kind === "forbidden") return t("machines-machinedetail.machine_switch_link_forbidden");
  if (n.kind === "uncertain") return t("machines-machinedetail.machine_switch_link_uncertain");
  return why(n.code, UNAVAILABLE, user);
}

export function LinkSwitch({ machineId, name, state, post, refresh, user, className }: {
  machineId: string;
  name: string;
  /** the machine list's `link_switch` */
  state?: MachineLinkSwitch | null;
  /** POST …/machines/{id}/link-switch, with the page's own credentials */
  post: () => Promise<{ link_switch?: MachineLinkSwitch } | undefined>;
  /** reads the page's machine list again */
  refresh: () => unknown;
  user?: string;
  className?: string;
}) {
  const attempt = useLinkSwitchAttempt(machineId);
  const [asking, setAsking] = useState(false);
  // once the list has caught up with a start made here, only the list speaks
  useEffect(() => retireLinkSwitch(machineId, state ?? undefined), [machineId, state, attempt]);
  if (!state) return null;
  const shown = shownLinkSwitch(state, attempt) ?? state;
  if (!shown.available && !shown.phase && !attempt && PASSING.has(shown.reason ?? "")) return null;
  const notice = linkSwitchNotice(state, attempt);
  const starting = !!attempt?.starting;
  const running = linkSwitchRunning(shown);
  const phase = shown.phase || "";
  const canStart = shown.available && !running && !starting;
  const tone = starting || running ? "busy" : phase === "failed" ? "failed" : phase === "succeeded" ? "done" : shown.available ? "ready" : "quiet";
  const confirm = () => {
    setAsking(false);
    void startLinkSwitch(machineId, state, post, refresh);
  };

  let body: ReactNode;
  if (starting) {
    body = (
      <p className="flex items-center gap-1.5">
        <Spinner size={10} />
        {t("machines-machinedetail.machine_switch_link_starting")}
      </p>
    );
  } else if (phase === "rolling_back") {
    body = (
      <>
        <p className="flex items-center gap-1.5 font-medium text-ink">
          <Spinner size={10} />
          {t("machines-machinedetail.machine_switch_link_rolling_back")}
        </p>
        {shown.error && <p className="mt-0.5">{why(shown.error, WHY.unknown, user)}</p>}
      </>
    );
  } else if (running) {
    const step = LINK_SWITCH_STEPS.indexOf(phase as (typeof LINK_SWITCH_STEPS)[number]);
    body = (
      <>
        <p className="mb-1 font-medium text-ink">{t("machines-machinedetail.machine_switch_link_switching")}</p>
        <Steps
          steps={LINK_SWITCH_STEPS.map((s) => ({ key: s, label: t(STEP_LABEL[s]) }))}
          step={Math.max(0, step)}
          outcome="running"
          label={t("machines-machinedetail.machine_switch_link_steps")}
          testId="link-switch-steps"
        />
      </>
    );
  } else if (phase === "succeeded") {
    body = <p>{t("machines-machinedetail.machine_switch_link_succeeded")}</p>;
  } else if (phase === "failed") {
    const reason = why(shown.error, WHY.unknown, user);
    body = (
      <>
        <p data-link-switch-failed={shown.rollback_unconfirmed ? "unconfirmed" : "rolled_back"}>
          {tRich(shown.rollback_unconfirmed ? "machines-machinedetail.machine_switch_link_failed_unconfirmed" : "machines-machinedetail.machine_switch_link_failed", { reason })}
        </p>
        {!shown.available && shown.reason && <p className="mt-0.5 text-ink-2">{why(shown.reason, UNAVAILABLE, user)}</p>}
      </>
    );
  } else if (shown.available) {
    body = <p>{t("machines-machinedetail.machine_switch_link_intro")}</p>;
  } else {
    body = <p>{why(shown.reason, UNAVAILABLE, user)}</p>;
  }

  return (
    <div
      className={cx(
        "flex items-center gap-2 rounded-control py-1.5 pl-2.5 pr-1.5 text-label",
        // under way: the inset fill, so the steps' field circles read on it
        tone === "failed" ? "bg-red-tint text-red" : tone === "done" ? "bg-green-tint text-green" : tone === "busy" ? "bg-inset text-ink-2 shadow-hairline" : tone === "quiet" ? "bg-field text-ink-3" : "bg-field text-ink-2",
        className,
      )}
      data-link-switch={starting ? "starting" : phase || (shown.available ? "available" : "unavailable")}
    >
      <div className="min-w-0 flex-1 break-words" role="status">
        {body}
        {notice && <p className="mt-0.5 text-red" data-link-switch-notice={notice.kind}>{noticeText(notice, user)}</p>}
      </div>
      {canStart && (
        <Button tone="secondary" size="sm" icon="link" onClick={() => setAsking(true)} testId="link-switch-start">
          {t("machines-machinedetail.machine_switch_link_action")}
        </Button>
      )}
      <Dialog
        open={asking}
        onClose={() => setAsking(false)}
        title={t("machines-machinedetail.machine_switch_link_title", { name })}
        footer={
          <>
            <Button tone="quiet" onClick={() => setAsking(false)}>
              {t("common.cancel")}
            </Button>
            <Button tone="primary" disabled={starting} onClick={confirm} testId="link-switch-confirm">
              {t("machines-machinedetail.machine_switch_link_confirm")}
            </Button>
          </>
        }
      >
        <p className="text-meta leading-relaxed text-ink-2">{t("machines-machinedetail.machine_switch_link_body_what")}</p>
        <p className="mt-2 text-meta leading-relaxed text-ink-2">{t("machines-machinedetail.machine_switch_link_body_reconnect")}</p>
        <p className="mt-2 text-meta leading-relaxed text-ink-2">{t("machines-machinedetail.machine_switch_link_body_kept")}</p>
      </Dialog>
    </div>
  );
}

/** The account a Linux computer's node runs as, from its home folder (/home/<user>, /root). */
export function homeUser(home?: string): string | undefined {
  return home?.split("/").filter(Boolean).pop() || undefined;
}
