// A feature module's page (core/module-host.ts, docs/web/web.md "Modules"): App draws it for a `module` route. Its code
// and strings load on its first render (loadModule), never while the module is off; the page then draws as any lazy
// page does (lib/lazy-page.ts), with App's Suspense fallback while its chunk is on its way.
//
// Like the built-in pages, an enabled module's page is warmed before it is opened when the pointer, the focus or a
// finger reaches a link to it (its sidebar entry; lib/lazy-page.ts preloadPages, not with Data Saver on). Never at idle:
// that would make every browser fetch every module, outside the idle tier's budget. An off or pending module is never
// warmed: its links route as unknown ones, and loadModule refuses it.
import { createElement } from "react";
import { lazyPage, preloadOnIntent, type LazyPageComponent } from "../lib/lazy-page";
import type { Route } from "../lib/router";
import { loadModule, moduleById, moduleExtensionAt, moduleState, type ModuleDescriptor, type ModuleExtensionPageProps, type ModulePageProps } from "./module-host";

// by descriptor: a module registered again (a test) gets a page of its own
const pages = new WeakMap<ModuleDescriptor, LazyPageComponent<ModulePageProps>>();

function pageOf(d: ModuleDescriptor): LazyPageComponent<ModulePageProps> {
  const id = d.id;
  let page = pages.get(d);
  if (!page) {
    page = lazyPage(
      () =>
        loadModule(id).then((code) => {
          if (!code.Page) throw new Error(`module "${id}" has no page`);
          return code.Page;
        }),
      { listed: false },
    );
    pages.set(d, page);
  }
  return page;
}

/** Warms an enabled module's page: its strings and its code, once. Nothing for a module that is not on. */
export function preloadModulePage(id: string): Promise<unknown> | undefined {
  const d = moduleById(id);
  return d && d.routes?.length && moduleState(d) === "on" ? pageOf(d).preload() : undefined;
}

preloadOnIntent("module", (r) => (r.name === "module" ? preloadModulePage(r.module) : undefined));

/** The page of a module route; nothing while its hub has not said whether it serves the module. */
export function ModulePage({ route, onToggleSidebar }: { route: Extract<Route, { name: "module" }>; onToggleSidebar?: () => void }) {
  const d = moduleById(route.module);
  if (!d || moduleState(d) !== "on") return null;
  return createElement(pageOf(d), { path: route.path, onToggleSidebar });
}

const extensionPages = new WeakMap<ModuleDescriptor, LazyPageComponent<ModuleExtensionPageProps>>();

function extensionPageOf(d: ModuleDescriptor): LazyPageComponent<ModuleExtensionPageProps> {
  let page = extensionPages.get(d);
  if (!page) {
    const id = d.id;
    page = lazyPage(
      () =>
        loadModule(id).then((code) => {
          if (!code.ExtensionPage) throw new Error(`module "${id}" has no extension page`);
          return code.ExtensionPage;
        }),
      { listed: false },
    );
    extensionPages.set(d, page);
  }
  return page;
}

/** A reserved extension-page id's page, drawn by the module that claims it (the health module's water and protein
 *  pages); nothing while that module is not on. */
export function ModuleExtensionPage(props: ModuleExtensionPageProps) {
  const d = moduleExtensionAt(props.extension);
  if (!d || d === "absent" || moduleState(d) !== "on") return null;
  return createElement(extensionPageOf(d), props);
}
