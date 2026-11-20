// Messages typed while the agent works and not yet taken (Session.queued): the panel at the top of the composer that
// shows them is its own chunk (QueuePanel.tsx, cindy's pending queue, #5327), loaded once something is queued. This
// eager wrapper only keeps that loading boundary; draft and photo restoration stays with the intent-loaded consumers.
import { Suspense } from "react";
import { lazyPage } from "../../lib/lazy-page";
import type { QueuePanelProps } from "./QueuePanel";

// Not `idle`: ChatView renders QueueStrip only while a message is queued (hasQueue), so this chunk loads on that first
// queued message instead of in every cold start's idle tier. The Suspense below holds the composer while it arrives.
const QueuePanel = lazyPage(() => import("./QueuePanel").then((m) => m.QueuePanel));

/** The queued messages at the top of the composer (QueuePanel.tsx), drawn once its chunk is in. */
export function QueueStrip(props: QueuePanelProps) {
  return (
    <Suspense fallback={null}>
      <QueuePanel {...props} />
    </Suspense>
  );
}
