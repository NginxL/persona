// Settings › General › Optional features (#14468): ConfigurationForm draws a switch for each registered module.
// These shipped features have lasting per-browser preferences (core/module-switch.ts). Labs is for reversible,
// time-limited trials with a tracking issue and an exit plan. A module the hub does not offer is shown read-only.
import { t } from "../i18n";
import { checkUpdate, CONFIGURATION_VERSION, ConfigurationError, type ConfigurationDocument, type ConfigurationField, type ConfigurationSource, type ConfigurationUpdate } from "../lib/configuration/contract";
import { getState } from "../lib/store";
import { hubServes, moduleState, registeredModules, subscribeModules } from "./module-host";
import { MODULE_SWITCH_KEY, moduleSwitchedOff, setModuleSwitchedOff } from "./module-switch";

export const MODULE_FEATURES_ID = "features";

/** Whether the hub offers the module (core/module-host.ts, the hub flag rule). */
const offered = (feature: string | undefined) => hubServes(feature, getState());

function revision(): string {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(MODULE_SWITCH_KEY);
  } catch {
    // unreadable: every module reads as on
  }
  return `${raw ?? "default"}|${registeredModules().map((d) => `${d.id}:${offered(d.feature)}`).join(",")}`;
}

function document(): ConfigurationDocument {
  const modules = registeredModules();
  const fields = modules.map((d): ConfigurationField => ({
    id: d.id,
    kind: "toggle",
    title: d.title(),
    description: d.description(),
    default: true,
    ...(offered(d.feature) ? {} : { read_only: true, disabled_reason: t("settings-features.features_not_offered") }),
    details: [
      { title: t("settings-labs.scope"), description: t("settings-labs.scope_browser") },
      { title: t("settings-labs.activation"), description: t("settings-features.features_activation") },
      { title: t("settings-labs.rollback"), description: t("settings-features.features_rollback") },
    ],
  }));
  return {
    schema_version: CONFIGURATION_VERSION,
    id: MODULE_FEATURES_ID,
    revision: revision(),
    title: t("settings-features.area_features"),
    groups: [{ id: "modules", title: t("settings-features.features_group"), description: t("settings-features.features_hint"), fields }],
    values: Object.fromEntries(modules.map((d) => [d.id, !moduleSwitchedOff(d.id) && moduleState(d) !== "off"])),
  };
}

function update(u: ConfigurationUpdate): ConfigurationDocument {
  const refused = checkUpdate(document(), u);
  if (refused) throw refused;
  const next: Record<string, boolean> = { ...(u.changes as Record<string, boolean> | undefined) };
  for (const id of u.reset ?? []) next[id] = true;
  for (const [id, on] of Object.entries(next)) {
    if (!setModuleSwitchedOff(id, !on)) throw new ConfigurationError("storage_failed", t("settings-labs.save_failed"));
  }
  return document();
}

/** General omits Optional features when the build registers no modules. */
export function moduleFeaturesSource(): ConfigurationSource | null {
  if (!registeredModules().length) return null;
  return {
    key: `general:${MODULE_FEATURES_ID}`,
    read: async () => document(),
    update: async (u) => update(u),
    // a switch here or in another tab, and the hub's features (what it offers)
    subscribe: subscribeModules,
  };
}
