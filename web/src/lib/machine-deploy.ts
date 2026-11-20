import { t } from "../i18n";
import type { Machine, MachineLinkSwitch } from "./types";

const PHASES = {
  waiting_idle: { label: t("lib-machine-deploy.waiting_idle"), message: t("lib-machine-deploy.waiting_idle_message") },
  transferring: { label: t("lib-machine-deploy.transferring"), message: t("lib-machine-deploy.transferring_message") },
  restarting: { label: t("lib-machine-deploy.restarting"), message: t("lib-machine-deploy.restarting_message") },
  confirming: { label: t("lib-machine-deploy.confirming"), message: t("lib-machine-deploy.confirming_message") },
} satisfies Record<NonNullable<Machine["deploy_phase"]>, { label: string; message: string }>;

/** A switch to Persona Link is under way (#10662, lib/link-switch.ts): the computer may be offline and of kind link
 *  meanwhile, as expected. Here, not there, because the first screen reads it. */
export function linkSwitchRunning(s?: MachineLinkSwitch | null): boolean {
  const p = s?.phase;
  return p === "checking" || p === "installing" || p === "connecting" || p === "rolling_back";
}

/** Offline on purpose: an update restarting its node, or a switch to Persona Link reconnecting it (#10662). Pure, so
 *  the machine alerts read it without counting as a deployment read (lib/machine-alerts.ts). */
export function disconnectExpected(m: Machine): boolean {
  return !m.online && ((!m.deploy_error && (m.deploy_phase === "restarting" || m.deploy_phase === "confirming")) || linkSwitchRunning(m.link_switch));
}

/** Older hubs can deploy SSH nodes, but never advertised an update path for linked nodes. */
export function machineDeployment(m: Machine) {
  const linked = m.kind === "link" || m.kind === "service";
  const supported = m.kind !== "local" && (m.can_deploy ?? !linked);
  const phase = m.deploy_phase && PHASES[m.deploy_phase];
  const busy = !!(m.deploying || phase);
  const canDeploy = supported && (!linked || m.online);
  const reason = !supported
    ? m.deploy_reason || (m.kind === "local"
      ? t("lib-machine-deploy.reason_local")
      : linked
        ? t("lib-machine-deploy.reason_linked")
        : t("lib-machine-deploy.reason_unsupported"))
    : !canDeploy ? t("lib-machine-deploy.reason_offline") : "";
  return {
    visible: !!(m.node_mismatch || busy || m.deploy_error),
    busy,
    expectedDisconnect: disconnectExpected(m),
    supported,
    canDeploy: canDeploy && !busy,
    label: busy ? phase?.label || t("lib-machine-deploy.deploying") : t("lib-machine-deploy.redeploy"),
    message: busy ? phase?.message || t("lib-machine-deploy.deploying_message") : m.node_mismatch ? t("lib-machine-deploy.mismatch") : "",
    error: m.deploy_error ? t("lib-machine-deploy.last_failed", { error: m.deploy_error }) : "",
    reason,
  };
}
