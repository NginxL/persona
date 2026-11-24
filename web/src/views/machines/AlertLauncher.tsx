// The alert sheet's section for launchers that fail on a computer (#8037, docs/server/launcher-health.md): each failing
// or blocked one (what kind of failure, its error line, when it was checked, how many starts failed, a usage limit's
// reset), the ones that work there as alternatives, and Test again (the hub's own check of that launcher on that
// computer; the new record shows in place). The hub has no per-computer default launcher to switch, so the way to use
// another one is Settings › Models, which this links to.
import { useEffect, useState } from "react";
import { formatDate, t } from "../../i18n";
import { api, enc, errorText } from "../../lib/api";
import { blockReason } from "../../lib/launcher-health";
import { launcherBlockedReason, launcherErrorText, launcherFailing, type LauncherHealthRecord } from "../../lib/machine-alerts";
import { providerTitle } from "../../lib/provider-names";
import { relativeTime } from "../../lib/relative-time";
import { navigate } from "../../lib/router";
import type { Launcher, Machine } from "../../lib/types";
import { Button, cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { settingsHref } from "../settings/nav";
import { DiagnosisLoading, SheetPart } from "./AlertParts";

/** What kind of failure a record is, in a line (lib/launcher-health.ts's words where they fit). */
export function failureKind(kind: string | undefined): string {
  if (kind === "usage_limit") return t("machines-alerts.launcher_usage_limit");
  if (kind === "onboarding") return t("machines-alerts.launcher_onboarding");
  return blockReason(kind ?? "");
}

export function LauncherSection({ machine, records, onRecords, onReload }: { machine: Machine; records: readonly LauncherHealthRecord[]; onRecords: (records: LauncherHealthRecord[]) => void; onReload?: () => void }) {
  const mine = records.filter((r) => r.machine === machine.id);
  const failing = mine.filter(launcherFailing);
  const [list, setList] = useState<Launcher[] | null | undefined>(undefined);
  const [checking, setChecking] = useState("");
  const [checked, setChecked] = useState<Record<string, string>>({});
  useEffect(() => {
    let dead = false;
    api<{ launchers?: Launcher[] }>(`/machines/${enc(machine.id)}/launchers`).then(
      (r) => !dead && setList(r.launchers ?? []),
      () => !dead && setList(null),
    );
    return () => {
      dead = true;
    };
  }, [machine.id]);
  const broken = new Set(failing.map((r) => r.launcher));
  const healthy = (list ?? []).filter((l) => !broken.has(l.name) && (!l.health || !launcherFailing(l.health)));
  const title = (name: string) => providerTitle(list?.find((l) => l.name === name) ?? { name });

  const testAgain = async (launcher: string) => {
    setChecking(launcher);
    try {
      const r = await api<{ records?: LauncherHealthRecord[] }>(`/launchers/health/check?machine=${enc(machine.id)}&launcher=${enc(launcher)}`, { method: "POST", timeout: 180_000 });
      const got = (r.records ?? []).filter((x) => x.launcher === launcher);
      onRecords(got);
      // No record for it (#14534): the one shown may be gone or newer on the hub, so the sheet shows what it holds now.
      if (!got.length) onReload?.();
      const now = got[0];
      setChecked((c) => ({ ...c, [launcher]: !now ? t("machines-alerts.launcher_no_result") : launcherFailing(now) ? t("machines-alerts.launcher_still_failing") : t("machines-alerts.launcher_works") }));
    } catch (e) {
      setChecked((c) => ({ ...c, [launcher]: t("machines-alerts.launcher_check_failed", { error: errorText(e) }) }));
    } finally {
      setChecking("");
    }
  };

  return (
    <>
      <SheetPart part="what">
        {!failing.length && <p className="text-meta text-ink-2">{t("machines-alerts.launcher_all_ok")}</p>}
        <ul className="space-y-2" data-testid="failing-launchers">
          {failing.map((r) => (
            <li key={r.launcher} className="rounded-control bg-field px-3 py-2" data-launcher={r.launcher} data-state={r.state}>
              <div className="flex items-center gap-2">
                <Icon name="terminal" size={13} className={r.state === "blocked" ? "text-red" : "text-orange"} />
                <span className="min-w-0 flex-1 truncate text-meta font-medium text-ink">{title(r.launcher)}</span>
                <span className="shrink-0 text-submeta text-ink-2">{r.state === "blocked" ? t("machines-alerts.launcher_blocked") : t("machines-alerts.launcher_failing")}</span>
              </div>
              <p className="mt-0.5 text-meta text-ink-2">{failureKind(r.kind)}</p>
              {launcherErrorText(r) && <p className="mt-0.5 line-clamp-3 break-words font-mono text-submeta text-ink-3" title={launcherErrorText(r)}>{launcherErrorText(r)}</p>}
              <p className="mt-1 text-submeta text-ink-3 tabular-nums">
                {[
                  r.checked_at && t("machines-alerts.launcher_checked", { time: relativeTime(r.checked_at) }),
                  (r.fails ?? 0) > 0 && t("machines-alerts.launcher_fails", { count: r.fails ?? 0 }),
                  r.resets_at && t("machines-alerts.launcher_resets", { time: formatDate(Date.parse(r.resets_at), { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) }),
                ].filter(Boolean).join(" · ")}
              </p>
              {launcherBlockedReason(r) && <p className="mt-0.5 text-submeta text-ink-3">{launcherBlockedReason(r)}</p>}
              <div className="mt-1.5 flex flex-wrap items-center gap-2">
                <Button size="sm" icon="retry" busy={checking === r.launcher} disabled={!!checking || !machine.online} onClick={() => void testAgain(r.launcher)} testId="test-again">
                  {t("machines-alerts.test_again")}
                </Button>
                {checked[r.launcher] && <span className="text-submeta text-ink-2" role="status">{checked[r.launcher]}</span>}
              </div>
            </li>
          ))}
        </ul>
      </SheetPart>
      <SheetPart part="why">
        <p className="text-meta leading-relaxed text-ink-2">{t("machines-alerts.launcher_why")}</p>
      </SheetPart>
      <SheetPart part="do">
        <h4 className="mb-1 text-meta font-medium text-ink">{t("machines-alerts.launcher_alternatives")}</h4>
        {list === undefined && <DiagnosisLoading label={t("machines-alerts.launcher_loading")} />}
        {list === null && <p className="text-meta text-ink-3">{t("machines-alerts.launcher_list_failed")}</p>}
        {list && (healthy.length ? (
          <ul className="flex flex-wrap gap-1.5" data-testid="healthy-launchers">
            {healthy.map((l) => (
              <li key={l.name} className={cx("inline-flex h-6 items-center gap-1 rounded-full bg-field px-2 text-label text-ink")}>
                <Icon name="check" size={11} className="text-green" />
                {providerTitle(l)}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-meta text-ink-3">{t("machines-alerts.launcher_no_alternative")}</p>
        ))}
        <p className="mt-2 text-submeta text-ink-3">{t("machines-alerts.launcher_switch_note")}</p>
        <div className="mt-1.5">
          <Button size="sm" tone="quiet" icon="sliders" onClick={() => navigate(settingsHref("models", null, false, null, "providers"))}>
            {t("machines-alerts.launcher_settings")}
          </Button>
        </div>
      </SheetPart>
    </>
  );
}
