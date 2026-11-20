// Adapt the selected Assistant's settings resource to the Labs form.
import { t } from "../i18n";
import { api, ApiError } from "./api";
import { MANAGER_HEADER } from "./manager-choice";
import { checkUpdate, CONFIGURATION_VERSION, ConfigurationError, type ConfigurationDocument, type ConfigurationSource } from "./configuration/contract";

const field = "work_guide";
type Settings = { manager?: string; work_guide?: boolean };

function document(manager: string, state: Settings, saved = false): ConfigurationDocument {
  if (state.manager !== manager) throw new ConfigurationError("configuration_unavailable");
  const supported = typeof state.work_guide === "boolean";
  return {
    schema_version: CONFIGURATION_VERSION,
    id: "assistant-work",
    revision: String(state.work_guide),
    title: t("settings-labs.work_area"),
    groups: [{ id: "work", title: t("settings-labs.trial"), fields: [{
      id: field, kind: "toggle", default: false,
      title: t("settings-labs.work_title"), description: t("settings-labs.work_hint"),
      ...(!supported && { read_only: true, disabled_reason: t("settings-labs.work_unsupported") }),
      details: [
        { title: t("settings-labs.scope"), description: t("settings-labs.work_scope") },
        { title: t("settings-labs.activation"), description: t("settings-labs.work_activation") },
        { title: t("settings-labs.rollback"), description: t("settings-labs.work_rollback") },
      ],
    }] }],
    values: { [field]: state.work_guide === true },
    ...(saved && { notices: { [field]: { kind: "info", message: t("settings-labs.work_saved") } } }),
  };
}

export function workGuideLabsSource(manager: string): ConfigurationSource {
  const headers = { [MANAGER_HEADER]: manager };
  return {
    key: `labs:assistant-work:${manager}`,
    read: async (signal) => document(manager, await api<Settings>("/assistant", { headers, signal })),
    update: async (update, signal) => {
      const expected = update.revision === "true";
      const refused = checkUpdate(document(manager, { manager, work_guide: expected }), update);
      if (refused) throw refused;
      const value = update.reset?.includes(field) ? false : update.changes?.[field];
      try {
        const state = await api<Settings>("/assistant", { method: "PUT", headers, signal,
          json: { work_guide: value, expected_work_guide: expected } });
        if (state.work_guide !== value) throw new ConfigurationError("configuration_unavailable");
        return document(manager, state, true);
      } catch (e) {
        if (e instanceof ApiError) throw ConfigurationError.fromBody(e.body, e.status);
        throw e;
      }
    },
  };
}
