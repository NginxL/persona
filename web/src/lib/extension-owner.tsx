// Whose extension view is being drawn, for what a graph's key keeps (UI version 10, #9727): the per-key store is
// scoped by the owner, so two extensions using the same key never share what it keeps. The owner travels as a React
// context: a mutable module global set in a useMemo went stale on a re-render alone (a graph's own state under one of
// two cards on a page read the other card's owner), so the value is read from context — correct on a lone re-render
// and when the parent re-renders unchanged. Whoever knows whose view it draws (ExtensionSlots, an inbox item's body,
// the subscription pages, an assistant card) renders ExtensionOwnerScope around it; without one, the page's own path
// is the owner. A module of its own because both the first screen (ExtensionSlots) and the renderer's chunk
// (views/extensions/ViewBody.tsx) read it, and a static value import of ViewBody would put the whole renderer back
// on the first screen (#1794, #3614).
import { createContext, type ReactNode } from "react";

/** Whose view is being drawn: the extension's id, else the page's own path (never a shared ""). */
export const ownerContext = createContext<string | undefined>(undefined);

/** Wraps whatever an extension draws here in its owner, so the keys of a `graph` it draws keep their own pan, zoom
 *  and folds (views/extensions/ViewBody10.tsx) even when two extensions draw on one page. */
export function ExtensionOwnerScope({ owner, children }: { owner: string; children: ReactNode }) {
  return <ownerContext.Provider value={owner}>{children}</ownerContext.Provider>;
}

// The canvas reads this context in its lazy module; its page-path fallback stays off the first screen.
