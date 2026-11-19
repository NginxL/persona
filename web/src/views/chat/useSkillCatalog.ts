// Execution-catalog I/O shared by the @ panel and / palette. Both callers are lazy and mount only on picker intent.
import { useEffect, useState, useSyncExternalStore } from "react";
import { api, enc, credentialOwner, subscribeCredentialOwner } from "../../lib/api";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import type { SkillCatalog, SkillSource } from "../../lib/skill-mentions";

export type SkillCatalogState = { catalog?: SkillCatalog; status?: "loading" | "error" | "partial" };

/** One transient snapshot owned by the mounted composer. Every opening revalidates it. */
export type CatalogMemory = { current?: { key: string; catalog: SkillCatalog } };

/** Read once per picker opening/environment, never per keystroke. Late responses cannot fill another environment. */
export function useSkillCatalog(source: SkillSource | undefined, machine: string | undefined, cwd: string | undefined, memory?: CatalogMemory): SkillCatalogState {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, () => "");
  const key = JSON.stringify([owner, machine, cwd, source?.manager, source?.session, source?.launcher, source?.environment, source?.agent]);
  const [state, setState] = useState<SkillCatalogState & { key: string }>({ key: "" });
  useEffect(() => {
    if (!source || !machine) return;
    const ac = new AbortController();
    const query = new URLSearchParams({ machine_id: machine });
    if (source.session) query.set("session_id", source.session);
    if (source.launcher) query.set("launcher", source.launcher);
    const path = source.agent ? `/sessions/${enc(machine)}/${enc(source.session ?? "")}/skills` : `/assistant/skills?${query}`;
    api<SkillCatalog>(path, { signal: ac.signal, headers: { [MANAGER_HEADER]: source.manager } })
      .then((catalog) => {
        if (ac.signal.aborted) return;
        if (catalog.catalog_version !== 1) throw new Error("Unsupported skill catalog");
        if (memory) memory.current = { key, catalog };
        setState({ key, catalog, status: catalog.complete ? undefined : "partial" });
      })
      .catch(() => {
        if (ac.signal.aborted) return;
        if (memory) memory.current = undefined;
        setState({ key, status: "error" });
      });
    return () => ac.abort();
    // Each request primitive is in key; keystrokes deliberately are not.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, memory]);
  if (!source || !machine) return { status: "error" };
  if (state.key === key) return state;
  const cached = memory?.current;
  return cached?.key === key ? { catalog: cached.catalog, status: cached.catalog.complete ? undefined : "partial" } : { status: "loading" };
}
