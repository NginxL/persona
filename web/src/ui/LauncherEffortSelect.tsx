import { t } from "../i18n";
import { effortTitle } from "../lib/session-actions";

function effortLabel(level: string) {
  return level === "off" || level === "none" ? t("settings-modelconfiguration.api_effort_off") : effortTitle(level);
}

export function LauncherEffortSelect({ value, choices, defaultEffort, defaultLabel, className, disabled, onChange }: {
  value: string;
  choices: readonly string[];
  defaultEffort?: string;
  defaultLabel?: string;
  className: string;
  disabled?: boolean;
  onChange: (effort: string) => void;
}) {
  const inherited = defaultEffort ? effortLabel(defaultEffort) : "";
  const label = defaultLabel ? [defaultLabel, inherited].filter(Boolean).join(" · ")
    : inherited ? t("settings-modelconfiguration.api_effort_inherit", { effort: inherited }) : t("settings-modelconfiguration.follow_default");
  return <label className="block">
    <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelconfiguration.effort")}</span>
    <select className={className} value={value} disabled={disabled || (!choices.length && !value)} onChange={(e) => onChange(e.target.value)}>
      <option value="">{label}</option>
      {value && !choices.includes(value) && <option value={value}>{t("settings-modelconfiguration.model_current", { id: effortLabel(value) })}</option>}
      {choices.map((level) => <option key={level} value={level}>{effortLabel(level)}</option>)}
    </select>
    {!choices.length && <span className="mt-1 block text-submeta text-ink-3">{t("settings-modelconfiguration.api_effort_unsupported")}</span>}
  </label>;
}
