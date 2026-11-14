import { useEffect, useMemo, useSyncExternalStore } from "react";
import { credentialOwner, subscribeCredentialOwner } from "../../lib/api";
import type { RuntimeSearchHit, RuntimeSearchSnapshot } from "../../lib/runtime-search";
import type { Session } from "../../lib/types";

const EMPTY: RuntimeSearchSnapshot = { query: "", hits: [], loaded: false, loading: false, loadingMore: false, error: "", moreError: "", hasMore: false, total: 0, expired: false, supported: true, retryable: true };
const empty = () => EMPTY;
const noSubscription = () => () => {};
const noOwner = () => "";

/** A query's own bounded search, loaded only on intent. The model owns the 200 ms debounce and request cancellation. */
export function useRuntimeSearch({ open, jump, query, capable, bounded, module, loadError, retryImport }: {
  open: boolean; jump: boolean; query: string; capable: boolean; bounded: boolean;
  module: typeof import("./SearchSupport") | null; loadError: string; retryImport: () => void;
}) {
  const owner = useSyncExternalStore(capable ? subscribeCredentialOwner : noSubscription, capable ? credentialOwner : noOwner, capable ? credentialOwner : noOwner);
  const enabled = open && jump && !!query.trim() && capable;
  const model = useMemo(() => module && capable ? new module.RuntimeSearchModel() : null, [module, owner, capable]);
  const state = useSyncExternalStore(model?.subscribe ?? noSubscription, model?.snapshot ?? empty, model?.snapshot ?? empty);
  useEffect(() => {
    model?.search(enabled ? query : "");
    return () => { model?.search(""); };
  }, [model, enabled, query]);
  useEffect(() => () => model?.dispose(), [model]);
  const current = enabled && state.query === query.trim();
  const visible = current ? state : EMPTY;
  const error = enabled ? loadError || visible.error : "";
  const loading = enabled && !error && (!model || !current || visible.loading);
  const fallback = open && jump && !!query.trim() && bounded && (!capable || (current && !state.supported));
  return {
    ...visible, enabled, loading, error, fallback,
    Notice: module?.RuntimeSearchNotice, mergeHits: module?.mergeRuntimeHits,
    hits: current && state.supported ? state.hits : EMPTY.hits,
    retry: () => { if (loadError) retryImport(); else void model?.retry(); },
    more: () => void model?.more(),
  };
}

/** Machine/session is a tuple, not a path: two identifiers containing a slash must not collide. */
export const runtimeSearchKey = (session: Session) => JSON.stringify([session.machine_id, session.session_id]);


export type { RuntimeSearchHit };
