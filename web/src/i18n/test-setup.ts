// Vitest: every test file runs with the zh-Hans catalog loaded, as the page does before its modules load. The admin
// UI's namespaces too, for the admin tests, and the feature modules' (src/modules, loaded with each module's code on
// the page), for their own tests.
import { adminCatalog } from "../admin/catalog";
import { byLanguage, initI18n, type CatalogModule } from "./index";

const moduleCatalogs = byLanguage(import.meta.glob<CatalogModule>("./locales/*/_meta.json", { query: "?catalog=modules" }));

await initI18n("zh-Hans", adminCatalog, moduleCatalogs);
