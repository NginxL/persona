// The alert sheet's section for a computer whose Persona is not the hub's build, or whose last update failed (#8037):
// both builds side by side and how far apart they are, why it matters, how many Agents are working (an update waits
// for them); Redeploy behind a confirmation, then a stepper the machine's pushed `deploy_phase` drives (waiting for
// idle → transferring → restarting → confirming → done) ending in the build the node reports, or the error and Try
// again. Where the hub cannot update it (`can_deploy` false), the hub's reason instead.
import { useEffect, useRef, useState } from "react";
import { formatDate, t } from "../../i18n";
import { stateOf } from "../../lib/format";
import { buildGap, buildTime, gapText } from "../../lib/machine-alerts";
import { useStore } from "../../lib/store";
import type { Machine, Session } from "../../lib/types";
import { Button, Dialog } from "../../ui/atoms";
import { Steps } from "../../ui/Steps";
import { useDeploy } from "./MachineDeploy";
import { SheetPart } from "./AlertParts";

const STEPS = ["waiting_idle", "transferring", "restarting", "confirming", "done"] as const;
type Step = (typeof STEPS)[number];

const stepLabel = (s: Step): string => {
  switch (s) {
    case "waiting_idle":
      return t("lib-machine-deploy.waiting_idle");
    case "transferring":
      return t("lib-machine-deploy.transferring");
    case "restarting":
      return t("lib-machine-deploy.restarting");
    case "confirming":
      return t("lib-machine-deploy.confirming");
    default:
      return t("machines-alerts.step_done");
  }
};

/** Where an update stands: the step under way (or reached), and how it ended. */
export interface DeployProgress {
  step: number;
  outcome: "running" | "done" | "failed" | "idle";
}

/** The stepper's state from what the page saw: `started` (Redeploy was pressed here), `sawBusy` (the hub has said it is
 *  updating since), the furthest step seen, and the machine as pushed now. */
export function deployProgress(m: Machine, started: boolean, sawBusy: boolean, furthest: number, requesting: boolean, refused: string): DeployProgress {
  const busy = !!(m.deploying || m.deploy_phase);
  if (busy) return { step: m.deploy_phase ? STEPS.indexOf(m.deploy_phase) : Math.max(0, furthest), outcome: "running" };
  if (!started) return { step: 0, outcome: "idle" };
  if (requesting) return { step: 0, outcome: "running" };
  if (refused) return { step: Math.max(0, furthest), outcome: "failed" };
  if (sawBusy && m.deploy_error) return { step: Math.max(0, furthest), outcome: "failed" };
  if (!m.node_mismatch && !m.deploy_error && m.online) return { step: STEPS.length - 1, outcome: "done" };
  if (sawBusy && m.node_mismatch) return { step: Math.max(0, furthest), outcome: "failed" };
  return { step: 0, outcome: "running" };
}

export function DeploySection({ machine, agents }: { machine: Machine; agents: readonly Session[] }) {
  const hubVersion = useStore((s) => s.version);
  const { deployment, pending, deploy, error } = useDeploy(machine);
  const [asking, setAsking] = useState(false);
  const [started, setStarted] = useState(false);
  const sawBusy = useRef(false);
  const furthest = useRef(0);
  const busy = !!(machine.deploying || machine.deploy_phase);
  if (busy && started) sawBusy.current = true;
  if (machine.deploy_phase) furthest.current = Math.max(furthest.current, STEPS.indexOf(machine.deploy_phase));
  const progress = deployProgress(machine, started || busy, sawBusy.current || busy, furthest.current, pending, error);
  const working = agents.filter((s) => stateOf(s) === "working").length;
  const nodeVersion = machine.node_version;
  const gap = buildGap(hubVersion, nodeVersion);
  // a stepper already under way (another device pressed it) shows as well
  useEffect(() => {
    if (busy && !started) {
      sawBusy.current = true;
      setStarted(true);
    }
  }, [busy, started]);

  const run = async () => {
    setAsking(false);
    sawBusy.current = false;
    furthest.current = 0;
    setStarted(true);
    await deploy();
  };

  return (
    <>
      <SheetPart part="what">
        <div className="grid grid-cols-2 gap-3" data-testid="builds">
          <Build label={t("machines-alerts.build_hub")} version={hubVersion} />
          <Build label={t("machines-alerts.build_node", { machine: machine.name })} version={nodeVersion} />
        </div>
        <p className="mt-2 text-meta text-ink-2" data-testid="build-gap">
          {!hubVersion ? t("machines-alerts.build_hub_unknown")
            : gap === undefined ? t("machines-alerts.build_gap_unknown")
            : gap > 0 ? t("machines-alerts.build_older", { gap: gapText(gap) })
            : gap < 0 ? t("machines-alerts.build_newer", { gap: gapText(gap) })
            : t("machines-alerts.build_same_time")}
        </p>
        {machine.deploy_error && progress.outcome !== "running" && <p className="mt-1 break-words text-meta text-red" data-testid="deploy-error">{t("lib-machine-deploy.last_failed", { error: machine.deploy_error })}</p>}
      </SheetPart>
      <SheetPart part="why">
        {progress.outcome === "done" ? (
          <p className="text-meta leading-relaxed text-ink-2">{t("machines-alerts.mismatch_resolved")}</p>
        ) : (
          <>
            <p className="text-meta leading-relaxed text-ink-2">{t("machines-alerts.mismatch_why")}</p>
            <p className="mt-1 text-meta text-ink-2" data-testid="working-count">{working ? t("machines-alerts.mismatch_working", { count: working }) : t("machines-alerts.mismatch_none_working")}</p>
          </>
        )}
      </SheetPart>
      <SheetPart part="do">
        {progress.outcome !== "idle" && <Steps steps={STEPS.map((s) => ({ key: s, label: stepLabel(s) }))} step={progress.step} outcome={progress.outcome} label={t("machines-alerts.stepper_label")} testId="deploy-stepper" />}
        {progress.outcome === "done" && <p className="mt-2 text-meta text-green" role="status" data-testid="deploy-done">{t("machines-alerts.deploy_done", { build: nodeVersion || "—" })}</p>}
        {progress.outcome === "failed" && (
          <p className="mt-2 break-words text-meta text-red" role="alert" data-testid="deploy-failed">
            {t("machines-alerts.deploy_failed_why", { error: error || machine.deploy_error || t("machines-alerts.deploy_still_mismatch") })}
          </p>
        )}
        {!deployment.supported ? (
          <p className="text-meta leading-relaxed text-ink-2" data-testid="deploy-reason">{deployment.reason}</p>
        ) : progress.outcome !== "running" && progress.outcome !== "done" ? (
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Button tone="primary" icon="retry" disabled={!deployment.canDeploy} title={deployment.reason || undefined} onClick={() => setAsking(true)} testId="redeploy">
              {progress.outcome === "failed" ? t("machines-alerts.try_again") : t("lib-machine-deploy.redeploy")}
            </Button>
            {deployment.reason && <span className="text-submeta text-ink-3">{deployment.reason}</span>}
          </div>
        ) : null}
      </SheetPart>
      <Dialog
        open={asking}
        onClose={() => setAsking(false)}
        title={t("machines-alerts.redeploy_confirm_title", { machine: machine.name })}
        footer={
          <>
            <Button tone="quiet" onClick={() => setAsking(false)}>
              {t("common.cancel")}
            </Button>
            <Button tone="primary" onClick={() => void run()} testId="redeploy-confirm">
              {t("lib-machine-deploy.redeploy")}
            </Button>
          </>
        }
      >
        <p className="text-meta leading-relaxed text-ink-2">{t("machines-alerts.redeploy_confirm_body", { machine: machine.name })}</p>
        <p className="mt-2 text-meta leading-relaxed text-ink-2">{working ? t("machines-alerts.redeploy_confirm_working", { count: working }) : t("machines-alerts.redeploy_confirm_idle")}</p>
      </Dialog>
    </>
  );
}

function Build({ label, version }: { label: string; version?: string }) {
  const at = buildTime(version);
  return (
    <div className="min-w-0 rounded-control bg-field px-3 py-2">
      <div className="truncate text-submeta text-ink-3">{label}</div>
      <div className="truncate font-mono text-meta text-ink" title={version || undefined}>{version || "—"}</div>
      {at !== undefined && <div className="text-submeta text-ink-3 tabular-nums">{formatDate(at, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}</div>}
    </div>
  );
}
