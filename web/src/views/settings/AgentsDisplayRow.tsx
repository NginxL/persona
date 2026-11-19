import { useEffect } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { refreshAgentsDisplay, retryAgentsDisplay, saveAgentsDisplay, useAgentsDisplay } from "../../lib/agents-display";
import { href } from "../../lib/router";
import { Spinner } from "../../ui/atoms";
import { Row, RowFailed, SettingsHint, Switch } from "./SettingsParts";

export function AgentsDisplayRow() {
  const { value, saving, error } = useAgentsDisplay();
  useEffect(() => { void refreshAgentsDisplay(); }, []);
  return <>
    <Row label={t("settings.show_botmux")} hint={t("settings.show_botmux_hint")} wrapHint anchor="botmux-display">
      {value ? <Switch on={value.show_botmux} disabled={saving} onChange={(on) => void saveAgentsDisplay(on)} label={t("settings.show_botmux")} /> : !error && <Spinner />}
    </Row>
    {saving && <SettingsHint><span role="status">{t("settings.botmux_saving")}</span></SettingsHint>}
    {error !== undefined && <RowFailed text={errorText(error)} retry={t("app.retry")} onRetry={() => void retryAgentsDisplay()} />}
    <SettingsHint>{t("settings.show_botmux_inventory")} <a className="text-accent hover:underline" href={href({ name: "machines" })}>{t("settings.show_botmux_computers")}</a></SettingsHint>
  </>;
}
