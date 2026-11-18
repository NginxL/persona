// The installed extensions' icons, for the mark of a conversation an extension opened (#10358, ui/OpenerTile.tsx): the
// hub's GET /api/v1/extensions (every manager's, each item with its `manager`), read when the first such mark is drawn,
// again on every hello (a reconnect, another hub version), after this page installs, upgrades or removes one
// (EXTENSIONS_CHANGED) and when the page comes back into view a minute after the last read. The last list stays while
// the next one is read. Off the first screen: only OpenerTile's lazy chunk imports it.
import { useSyncExternalStore } from "react";
import { api, ApiError } from "./api";
import { connectorBrand, type BrandId } from "./connectors";
import { extRoot, listOf } from "./extension-api";
import { extensionLogoSource } from "./extension-logo";
import { EXTENSIONS_CHANGED } from "./extension-prompts";
import type { HubExtension } from "./extensions";
import { defaultManager, onDefaultManager, shownManager, useChosenManager } from "./manager-choice";
import { getState, onHubEvent } from "./store";

type Listed = Pick<HubExtension, "id" | "manager" | "icon_url" | "contributes"> & { name?: string };

/** Whether extension `x` is the one `brand`'s mark stands for: it brings a connector or a messaging source of that brand
 *  (lib/connectors.ts connectorBrand), as the Lark extension brings `lark`. Its conversations keep the origin's own mark. */
const ownsBrand = (x: Listed, brand: BrandId | undefined) =>
  !!brand && [...(x.contributes?.connectors ?? []), ...(x.contributes?.messaging ?? [])].some((c) => connectorBrand(c) === brand);

/**
 * The icon to draw for extension `id` that opened a conversation of `manager` (none: the shown assistant's, whose
 * conversations the page lists), from the installed extensions (`list`): its `icon_url` when that is a route of the
 * hub's own (lib/extension-logo.ts: an installed or a catalog icon; public artwork is left out), with the manager to
 * ask it of and the extension's name, which the mark's words take once the icon is drawn. Undefined while the list is
 * not read, when that manager has no such extension or it has no icon, and for the extension the origin's mark
 * (`brand`, Lark's for Lark /agent) already stands for.
 */
export function openerIcon(list: readonly Listed[] | undefined, id: string, manager: string | undefined, brand?: BrandId, shown = shownManager(), fallback = defaultManager()): { url: string; manager: string; name?: string } | undefined {
  const want = manager || shown;
  const ext = list?.find((x) => x.id === id && (x.manager || fallback) === want);
  if (!ext?.icon_url || !extensionLogoSource(ext.icon_url)?.path || ownsBrand(ext, brand)) return undefined;
  return { url: ext.icon_url, manager: ext.manager ?? "", name: ext.name?.trim() || undefined };
}

/** A contribution's ids as the list sends them: anything that is not a list of strings is none. */
const ids = (v: unknown): string[] => (Array.isArray(v) ? v.filter((c): c is string => typeof c === "string") : []);

let list: Listed[] | undefined;
let askedAt = 0;
let asking = false;
let again = false;
let started = false;
const listeners = new Set<() => void>();

/** `fresh`: something may have changed since a read under way was asked, so another follows it. */
async function read(fresh: boolean) {
  if (asking) {
    again ||= fresh;
    return;
  }
  const features = getState().features;
  if (!features.includes("extensions") && !features.includes("plugins")) return; // before hello, or a hub without them
  asking = true;
  try {
    const next = listOf(await api<{ extensions?: HubExtension[]; plugins?: HubExtension[] }>(extRoot())).map(({ id, manager, icon_url, name, contributes }) =>
      ({ id, manager, icon_url, name, contributes: { connectors: ids(contributes?.connectors), messaging: ids(contributes?.messaging) } }));
    if (JSON.stringify(next) !== JSON.stringify(list)) {
      list = next;
      listeners.forEach((l) => l());
    }
  } catch (e) {
    if (e instanceof ApiError && e.status === 404 && list?.length !== 0) {
      list = [];
      listeners.forEach((l) => l());
    }
  } finally {
    askedAt = Date.now();
    asking = false;
    if (again) {
      again = false;
      void read(false);
    }
  }
}

/** The list may have changed: read it now while a mark is drawn, else when the next one is. */
function changed() {
  askedAt = 0;
  if (listeners.size) void read(true);
}

function start() {
  if (started) return;
  started = true;
  onHubEvent("hello", changed);
  window.addEventListener(EXTENSIONS_CHANGED, changed);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && listeners.size && Date.now() - askedAt > 60_000) void read(false);
  });
}

function subscribe(l: () => void) {
  start();
  listeners.add(l);
  if (!list || Date.now() - askedAt > 60_000) void read(false);
  return () => {
    listeners.delete(l);
  };
}

const subscribeDefault = (l: () => void) => onDefaultManager(() => l());

/** openerIcon from the installed extensions as last read, drawn again when the list or the assistants' choice changes. */
export function useOpenerIcon(id: string, manager: string | undefined, brand?: BrandId): ReturnType<typeof openerIcon> {
  const known = useSyncExternalStore(subscribe, () => list);
  useChosenManager();
  useSyncExternalStore(subscribeDefault, defaultManager);
  return openerIcon(known, id, manager, brand);
}

/** Tests: nothing read yet. */
export function forgetOpenerIcons() {
  list = undefined;
  askedAt = 0;
  again = false;
}
