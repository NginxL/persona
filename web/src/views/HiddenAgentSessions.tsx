import { useState } from "react";
import { t } from "../i18n";
import { Button } from "../ui/atoms";
import { lazyDialog } from "./LazyOverlay";

// Opened only on a click, so its list and words stay off the first screen (docs/web/web.md, "First screen").
const HiddenAgentSessionsDialog = lazyDialog(() => import("./HiddenAgentSessionsDialog").then((m) => m.HiddenAgentSessionsDialog));

/** A separate, bounded inspection view: never changes the shared default list or attention counts. */
export function HiddenAgentSessions() {
  const [open, setOpen] = useState(false);
  return <>
    <Button tone="quiet" onClick={() => setOpen(true)}>{t("hidden-agent-dirs.inspect")}</Button>
    <HiddenAgentSessionsDialog open={open} onClose={() => setOpen(false)} />
  </>;
}
