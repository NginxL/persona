// Settings › Labs (#9877, docs/web/web.md "Labs"): trial features and choices between a stable and a new implementation.
// Each module that has one injects a configuration source (lib/configuration/contract.ts); this page draws each with the
// generic form (configuration/ConfigurationForm.tsx), which knows none of them. Today: the terminal renderer
// (lib/labs-terminal.ts), kept by this browser.
import { t } from "../../i18n";
import type { ConfigurationSource } from "../../lib/configuration/contract";
import type { ConfigurationGuidance } from "../../lib/configuration/guidance";
import { terminalLabsSource } from "../../lib/labs-terminal";
import { Dialog } from "../../ui/atoms";
import { ConfigurationForm } from "./configuration/ConfigurationForm";
import { Group, SettingsHint } from "./SettingsParts";

export interface LabsModule {
  source: ConfigurationSource;
  guidance?: ConfigurationGuidance;
}

/** Modules in display order. Each registers its source and optional localized guidance. */
const MODULES: readonly LabsModule[] = [{ source: terminalLabsSource() }];

export function LabsSettings({ modules = MODULES }: { modules?: readonly LabsModule[] } = {}) {
  return (
    <>
      <Group anchor="labs" footer={t("settings-labs.scope_note")}>
        <SettingsHint>{t("settings-labs.intro")}</SettingsHint>
      </Group>
      {modules.map(({ source, guidance }) => (
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
