// "Close…" (#7125): the dialog lib/close-agent.ts opens for every menu that closes an Agent: its row's, the chat's ⋯, and
// "Close pane" on its pane in the Agents tree, the Terminal page and the command palette. The dialog is its own chunk,
// loaded when one of those actions opens it, so it stays off both the first screen and the idle preload.
import { useEffect, useState } from "react";
import { onOpenCloseAgent } from "../lib/close-agent";
import { machineWithHub } from "../lib/mesh";
import { useStore } from "../lib/store";
import type { Session } from "../lib/types";
import { lazyDialog } from "./LazyOverlay";

const CloseAgentDialog = lazyDialog(() => import("./chat/CloseAgentDialog").then((m) => m.CloseAgentDialog));

export function CloseAgentHost() {
  const [session, setSession] = useState<Session | null>(null);
  useEffect(() => onOpenCloseAgent(setSession), []);
  const machines = useStore((st) => st.machines);
  if (!session) return null;
  return (
    <CloseAgentDialog key={`${session.machine_id}/${session.session_id}`} open session={session} machineName={machineWithHub(session.machine_id, machines)} onClose={() => setSession(null)} />
  );
}
