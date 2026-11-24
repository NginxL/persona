// The recording module's descriptor (#13407, web half; #13430; docs/web/web.md "Modules", docs/assistant/meeting-minutes.md,
// docs/assistant/day-recap.md): the meeting recording, minutes and day-recap parts of a recording's knowledge page, through
// the `kb.detail` slot. Its code and its strings (recording.json, recording-*.json) load when a recording opens.
//
// Its hub flag is `module.recording` (the rule on #13844, core/module-host.ts): off only on a hub that publishes module
// flags without it, so an older hub keeps it on as before. Settings › Labs › Features turns it off in a browser too;
// off, recordings are absent from Knowledge (the Meetings list, searches, their addresses).
import { byLanguage, t, type CatalogModule } from "../../i18n";
import { moduleFeature, type ModuleDescriptor } from "../../core/module-host";

export const recording: ModuleDescriptor = {
  id: "recording",
  feature: moduleFeature("recording"),
  title: () => t("sidebar.module_recording"),
  description: () => t("sidebar.module_recording_hint"),
  kbKinds: ["recordings"],
  i18nNamespaces: ["recording"],
  locales: byLanguage(import.meta.glob<CatalogModule>("../../i18n/locales/*/_meta.json", { query: "?catalog=module-recording" })),
  load: () => import("./RecordingDetail").then((m) => ({ KBDetail: m.RecordingDetail })),
};
