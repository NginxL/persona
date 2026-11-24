// "Disconnect this browser" in Settings › General and the account menu (#1873, lib/disconnect.ts): it asks first, since
// getting back in takes pairing again (DisconnectConfirm.tsx); the confirm button is busy while the hub revokes the
// credential; a revoke that fails, or a credential the hub cannot revoke from here, asks before signing out instead of
// signing out as if it had worked.
import { Suspense, useState } from "react";
import { handleUnauthorized } from "../lib/api";
import { revokeThisBrowser, signOutHere } from "../lib/disconnect";
import { lazyPage } from "../lib/lazy-page";
import { toast } from "../ui/atoms";
import { t } from "../i18n";
import { OverlayBoundary } from "./LazyOverlay";
import type { DisconnectProblem } from "./DisconnectConfirm";

// Only a browser that is disconnecting needs the confirmation dialog.
const DisconnectConfirm = lazyPage(() => import("./DisconnectConfirm").then((m) => m.DisconnectConfirm));

export function useDisconnect(): { start: () => void; busy: boolean; dialog: React.ReactNode } {
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<DisconnectProblem | null>(null);
  // Keep the dialogs mounted after the first opening so they can animate closed and retry their chunk.
  const [asked, setAsked] = useState(false);
  const [asking, setAsking] = useState(false);
  const ask = () => {
    if (busy) return;
    setAsked(true);
    setAsking(true);
  };
  const revoke = async () => {
    if (busy) return;
    setBusy(true);
    // signing out on purpose: a request that gets a 401 meanwhile must not show "this browser was signed out"
    const before = handleUnauthorized(() => {});
    const r = await revokeThisBrowser();
    if (r.ok) return signOutHere();
    handleUnauthorized(before); // still signed in here until the user says otherwise
    setBusy(false);
    setAsking(false);
    setProblem(r);
  };
  const close = () => !busy && setProblem(null);
  const signOut = () => {
    handleUnauthorized(() => {});
    signOutHere(problem?.id ?? "");
  };
  // Both dialogs share the same chunk and stay mounted after first opening so they can animate closed.
  const dialog = asked && (
    <OverlayBoundary open={asking || problem !== null} onClose={() => { setAsking(false); setProblem(null); toast(t("pageboundary.chunk_body"), "error"); }}>
      <Suspense fallback={null}>
        <DisconnectConfirm open={asking} busy={busy} onCancel={() => setAsking(false)} onConfirm={() => void revoke()}
          problem={problem} onProblemClose={close} onSignOut={signOut} />
      </Suspense>
    </OverlayBoundary>
  );
  return { start: ask, busy, dialog };
}
