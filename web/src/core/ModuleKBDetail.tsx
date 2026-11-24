// The knowledge detail's `kb.detail` slot (core/module-host.ts, docs/web/web.md "Modules"): a module that names a
// knowledge kind (`kbKinds`) adds parts to the page of each entry of that kind (the recording module: a meeting's
// minutes, conversation and transcript). The page (views/knowledge/KBDetail.tsx) lays itself out as a function of those
// parts; this draws it with the module's parts once the module's code is in, and with none before (its chunk loads when
// such an entry opens, never at start) or when no module that is on names the kind.
import { useEffect, useState, type ComponentType } from "react";
import { loadModule, moduleKBKindAt, moduleState, useModuleStates, type ModuleKBDetailParts, type ModuleKBDetailProps } from "./module-host";

const NO_PARTS: ModuleKBDetailParts = {};
// loaded once per module id
const drawn = new Map<string, ComponentType<ModuleKBDetailProps>>();

/** The module that adds parts to an entry of this kind, while it is on; null otherwise. */
export function useKBKindModule(kind: string): string | null {
  useModuleStates();
  const d = moduleKBKindAt(kind);
  return d && d !== "absent" && moduleState(d) === "on" ? d.id : null;
}

/** Whether entries of this kind are there: a kind a module names only while that module is on or pending. */
export function useKBKindShown(kind: string): boolean {
  useModuleStates();
  return moduleKBKindAt(kind) !== "absent";
}

/** Loads the parts the module that names this kind adds (its strings and code), once; nothing for a kind no module
 *  that is on names. The page calls it as it opens; a test, before it draws. */
export function loadKBDetailParts(kind: string): Promise<ComponentType<ModuleKBDetailProps> | null> {
  const d = moduleKBKindAt(kind);
  if (!d || d === "absent" || moduleState(d) !== "on") return Promise.resolve(null);
  const known = drawn.get(d.id);
  if (known) return Promise.resolve(known);
  const id = d.id;
  return loadModule(id).then((code) => {
    if (!code.KBDetail) throw new Error(`module "${id}" has no knowledge detail`);
    drawn.set(id, code.KBDetail);
    return code.KBDetail;
  });
}

/** The page of a knowledge entry, with the parts the module that names its kind adds. */
export function ModuleKBDetail(props: ModuleKBDetailProps) {
  const id = useKBKindModule(props.kind);
  const [Parts, setParts] = useState<ComponentType<ModuleKBDetailProps> | null>(() => (id ? drawn.get(id) ?? null : null));
  const { kind } = props;
  useEffect(() => {
    if (!id) return setParts(null);
    let live = true;
    loadKBDetailParts(kind).then(
      (parts) => live && setParts(() => parts),
      // the page stays as it is without the module's parts: its own sections still work
      (err) => console.error(err),
    );
    return () => {
      live = false;
    };
  }, [id, kind]);
  if (!id || !Parts) return <>{props.children(NO_PARTS)}</>;
  return <Parts {...props} />;
}

/** Tests only: forget the loaded parts. */
export function resetKBDetailPartsForTest(): void {
  drawn.clear();
}
