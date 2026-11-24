// The health module's descriptor (#13434, the web pilot of #13430; docs/web/web.md "Modules", docs/assistant/health.md):
// what it adds to the shell, small enough to load at start. Its page and its strings (health.json) load on first use.
//
// Its hub flag is module.health (core/module-host.ts, the hub flag rule): a hub that publishes module flags turns it off
// by leaving the flag out; every older hub, which publishes none, keeps it on, as the page always was. Settings › Labs ›
// Features turns it off in a browser.
import { byLanguage, t, type CatalogModule } from "../../i18n";
import { moduleFeature, type ModuleDescriptor } from "../../core/module-host";
import { HEALTH_INTAKE_UI } from "../../lib/health-intake-ui";

export const health: ModuleDescriptor = {
  id: "health",
  feature: moduleFeature("health"),
  title: () => t("sidebar.nav_health"),
  description: () => t("sidebar.nav_health_hint"),
  routes: [{ base: "health", aliases: [["tracks", "health"]], title: () => t("app.title_health") }],
  // the water and protein pages (#13176): the hub's declarative pages under a reserved id, drawn by the extension
  // pages' renderer (`/extension/-health-intake/pages?state=`)
  extensionPages: [HEALTH_INTAKE_UI],
  // right after Inbox, where it always was
  navItems: [{ key: "health", path: "/health", icon: "heart", label: () => t("sidebar.nav_health"), short: () => t("sidebar.nav_health_short"), color: "pink", after: "inbox" }],
  i18nNamespaces: ["health"],
  locales: byLanguage(import.meta.glob<CatalogModule>("../../i18n/locales/*/_meta.json", { query: "?catalog=module-health" })),
  load: () => Promise.all([import("./Health"), import("./HealthIntakePage")]).then(([home, intake]) => ({ Page: home.HealthPage, ExtensionPage: intake.HealthIntakeModulePage })),
};
