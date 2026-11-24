// A feature module's Settings category (core/module-host.ts `settingsTabs`, docs/web/web.md "Modules"): Settings draws
// it for a category no core page has. Its module's strings and code load on its first render (loadModule); while they
// load it draws as any lazy page does, and while its module is off it draws nothing.
import { createElement, Suspense } from "react";
import { lazyPage, type LazyPageComponent } from "../lib/lazy-page";
import { Loading } from "../ui/atoms";
import { enabledSettingsTabs, loadModule, useModuleStates, type ModuleSettingsProps } from "./module-host";

const pages = new Map<string, LazyPageComponent<ModuleSettingsProps>>();

function pageOf(module: string, tab: string): LazyPageComponent<ModuleSettingsProps> {
  const key = `${module}/${tab}`;
  let page = pages.get(key);
  if (!page) {
    page = lazyPage(() => loadModule(module).then((code) => code.settings![tab]!), { listed: false });
    pages.set(key, page);
  }
  return page;
}

export function ModuleSettingsPage({ tab }: { tab: string }) {
  useModuleStates();
  const at = enabledSettingsTabs().find((m) => m.key === tab);
  if (!at) return null;
  return <Suspense fallback={<Loading />}>{createElement(pageOf(at.module, tab), { tab })}</Suspense>;
}

/** Tests only. */
export function forgetModuleSettingsPages(): void {
  pages.clear();
}
