import { useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import type { UserMessage } from "../../lib/turns";
import { Button } from "../../ui/atoms";

export default function EmptyResponse({ user, onRetry }: {
  user: UserMessage;
  onRetry?: (user: UserMessage, signal: AbortSignal) => Promise<boolean>;
}) {
  const pending = useRef<AbortController>(null);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  useEffect(() => () => pending.current?.abort(), []);

  const retry = async () => {
    if (!onRetry || pending.current) return;
    const request = new AbortController();
    pending.current = request;
    setBusy(true);
    try {
      const sent = await onRetry(user, request.signal);
      if (!request.signal.aborted) setFailed(!sent);
    } catch {
      if (!request.signal.aborted) setFailed(true);
    } finally {
      pending.current = null;
      if (!request.signal.aborted) setBusy(false);
    }
  };

  return (
    <div className="flex flex-wrap items-center gap-2 text-sm text-ink-2">
      <span role="status">{t(failed ? "chat-empty-response.recover_failed" : "chat-empty-response.no_response")}</span>
      {onRetry && <Button size="sm" tone="quiet" className="touch-hit" busy={busy} onClick={() => void retry()}>
        {t("chat-lines.retry")}
      </Button>}
    </div>
  );
}
