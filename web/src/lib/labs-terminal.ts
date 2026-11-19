// Settings › Labs' first module (#9877): the Single pane terminal renderer (lib/labs.ts) as a configuration Document
// (lib/configuration/contract.ts), so the Labs page draws it with the generic form. Local to this browser: no request
// leaves the page. The same rules as any source: an update names this Document and the revision it was made against,
// is checked here, and is answered with the Document as committed.
import { t } from "../i18n";
import { checkUpdate, CONFIGURATION_VERSION, ConfigurationError, type ConfigurationDocument, type ConfigurationSource, type ConfigurationUpdate } from "./configuration/contract";
import { readTerminalRenderer, subscribeTerminalRenderer, writeTerminalRenderer, type TerminalRenderer } from "./labs";

export const TERMINAL_LABS_ID = "terminal";
const FIELD = "renderer";

/** The revision: what is stored (another tab writing changes it too). */
function revision(state: ReturnType<typeof readTerminalRenderer>): string {
  return state.source === "unreadable" ? "unreadable" : state.raw === null ? "default" : `saved:${state.raw}`;
}

function document(): ConfigurationDocument {
  const state = readTerminalRenderer();
  const notice = state.source === "invalid" ? t("settings-labs.invalid") : state.source === "unreadable" ? t("settings-labs.unreadable") : "";
  return {
    schema_version: CONFIGURATION_VERSION,
    id: TERMINAL_LABS_ID,
    revision: revision(state),
    title: t("settings-labs.area_agents"),
    groups: [
      {
        id: "terminal",
        title: t("settings-labs.terminal_group"),
        fields: [
          {
            id: FIELD,
            kind: "select",
            title: t("settings-labs.terminal_renderer"),
            description: t("settings-labs.terminal_renderer_hint"),
            default: "standard",
            select: {
              presentation: "segmented",
              options: [
                { value: "standard", title: t("settings-labs.terminal_renderer_standard") },
                { value: "webgl", title: t("settings-labs.terminal_renderer_webgl") },
              ],
            },
            details: [
              { title: t("settings-labs.scope"), description: t("settings-labs.scope_browser") },
              { title: t("settings-labs.activation"), description: t("settings-labs.terminal_renderer_activation") },
              { title: t("settings-labs.rollback"), description: t("settings-labs.terminal_renderer_rollback") },
            ],
          },
        ],
      },
    ],
    values: { [FIELD]: state.value },
    ...(notice ? { notices: { [FIELD]: { kind: "warning", message: notice } } } : {}),
  };
}

function update(u: ConfigurationUpdate): ConfigurationDocument {
  // the writer's rules (schema/configuration/README.md "Change and reset"), against what is stored now
  const refused = checkUpdate(document(), u);
  if (refused) throw refused;
  const next = u.reset?.includes(FIELD) ? "standard" : (u.changes?.[FIELD] as TerminalRenderer);
  if (!writeTerminalRenderer(next).ok) throw new ConfigurationError("storage_failed", t("settings-labs.save_failed"));
  return document();
}

/** The terminal renderer's Labs source: reads and writes this browser's choice, and follows other tabs. */
export function terminalLabsSource(): ConfigurationSource {
  return {
    key: `labs:${TERMINAL_LABS_ID}`,
    read: async () => document(),
    update: async (u) => update(u),
    subscribe: subscribeTerminalRenderer,
  };
}
