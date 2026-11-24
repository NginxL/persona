// Settings › Labs (#9877, docs/web/web.md "Labs") admits a temporary trial or a stable/new implementation choice.
// Keep the stable default and a lossless rollback. Link a tracking issue with a plan to graduate or remove the trial.
// Put permanent module switches in General and other lasting preferences in their owning category. Keep irreversible
// changes, internal debug controls and placeholder demos out of Labs. ConfigurationForm draws each trial's source.
import { t } from "../../i18n";
import type { ConfigurationSource } from "../../lib/configuration/contract";
import type { ConfigurationGuidance } from "../../lib/configuration/guidance";
import { workGuideLabsSource } from "../../lib/labs-work-guide";
import { useChosenManager, useKnownDefault } from "../../lib/manager-choice";
import { terminalLabsSource } from "../../lib/labs-terminal";
import { Dialog, Loading } from "../../ui/atoms";
import { ConfigurationForm } from "./configuration/ConfigurationForm";
import { Group, SettingsHint } from "./SettingsParts";

export interface LabsModule {
  source: ConfigurationSource;
  guidance?: ConfigurationGuidance;
}

const defaultModules: readonly LabsModule[] = [{ source: terminalLabsSource() }];

// This field follows the selected manager. The browser-local experiments below render
// immediately, even while the roster is resolving the default manager.
function WorkGuideSettings() {
  const chosen = useChosenManager();
  const knownDefault = useKnownDefault();
  const manager = chosen ?? knownDefault;
  return manager ? <div className="mb-6"><ConfigurationForm key={manager} source={workGuideLabsSource(manager)} /></div> : <Loading />;
}

export function LabsSettings({ modules }: { modules?: readonly LabsModule[] } = {}) {
  return (
    <>
      <Group anchor="labs" footer={t("settings-labs.scope_note")}>
        <SettingsHint>{t("settings-labs.intro")}</SettingsHint>
      </Group>
      {!modules && <WorkGuideSettings />}
      {(modules ?? defaultModules).map(({ source, guidance }) => (
        <div key={source.key} className="mb-6">
          <ConfigurationForm source={source} guidance={guidance} />
        </div>
      ))}
    </>
  );
}

/** Labs over the current page (a sheet on a phone), opened from a feature's own menu (Terminal settings › Renderer): the
 *  same page as Settings › Labs, without leaving, so nothing open behind it (a chat's draft) is unmounted. */
export function LabsDialog({ open, onClose, modules }: { open: boolean; onClose: () => void; modules?: readonly LabsModule[] }) {
  return (
    <Dialog open={open} onClose={onClose} title={t("settings-nav.tab_labs")} width={600}>
      <div data-labs-dialog className="-mb-6">
        <LabsSettings modules={modules} />
      </div>
    </Dialog>
  );
}
