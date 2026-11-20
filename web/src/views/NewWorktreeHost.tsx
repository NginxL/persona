import { Suspense, useEffect, useState } from "react";
import { t } from "../i18n";
import { lazyPage } from "../lib/lazy-page";
import { onOpenNewWorktree, type WorktreeRequest } from "../lib/worktree-request";
import { Button, Dialog, Loading } from "../ui/atoms";
import { OverlayBoundary } from "./LazyOverlay";

const NewWorktree = lazyPage(() => import("./NewWorktree").then((m) => m.NewWorktree));

/** Keep the creation composer out of the shell's first-screen download. */
export function NewWorktreeHost() {
  const [request, setRequest] = useState<WorktreeRequest | null>(null);
  useEffect(() => onOpenNewWorktree(setRequest), []);
  const close = () => setRequest(null);
  // Claim the menu's phone Back entry while the chunk loads, before its delayed history.back can pop a new sheet.
  return request ? <OverlayBoundary open onClose={close}>
    <Suspense fallback={<Dialog open title={t("worktree.dialog_title")} onClose={close} footer={<Button onClick={close}>{t("common.cancel")}</Button>}>
      <Loading label={t("worktree.reading")} />
    </Dialog>}>
      <NewWorktree key={JSON.stringify([request.target, request.source, request.parent])} request={request} onClose={close} />
    </Suspense>
  </OverlayBoundary> : null;
}
