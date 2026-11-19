// The question editor loads only when its card action opens it; the first screen
// carries just the additive Have a question action.
import { useRef, useState } from "react";
import { APPROVAL_QUESTION_MAX, approvalQuestionReady, askApprovalQuestion } from "../../lib/approval-question";
import { errorText } from "../../lib/api";
import { uuid } from "../../lib/format";
import { composing } from "../../lib/send-history";
import type { Interaction } from "../../lib/types";
import { t } from "../../i18n";
import { Button, cx, toast } from "../../ui/atoms";

export function ApprovalQuestion({ ix, busy, onClose }: { ix: Interaction; busy: boolean; onClose: () => void }) {
  const [question, setQuestion] = useState("");
  const [sending, setSending] = useState(false);
  const delivery = useRef({ text: "", id: "" });
  const ask = async () => {
    if (sending || busy || !approvalQuestionReady(question)) return;
    // An uncertain retry of the unchanged draft keeps both IDs. Editing starts a
    // new question instead of accidentally deduplicating different owner text.
    if (delivery.current.text !== question || !delivery.current.id) {
      delivery.current = { text: question, id: uuid() };
    }
    setSending(true);
    try {
      await askApprovalQuestion(ix, question, delivery.current.id);
      toast(t("approval-question.sent_pending"), "ok");
      onClose();
    } catch (e) {
      // Keep the editor and every character on failure; Allow and Deny stay live.
      toast(t("chat-interactions.send_failed", { error: errorText(e) }), "error");
    } finally {
      setSending(false);
    }
  };
  const tooLong = Array.from(question.trim()).length > APPROVAL_QUESTION_MAX;
  return (
    <div data-approval-question className="mt-2 rounded-control bg-inset p-2 shadow-hairline">
      <textarea
        autoFocus
        rows={3}
        value={question}
        disabled={busy || sending}
        onChange={(e) => setQuestion(e.target.value)}
        onKeyDown={(e) => {
          if (e.key !== "Enter" || e.shiftKey || composing(e.nativeEvent)) return;
          e.preventDefault();
          e.stopPropagation();
          void ask();
        }}
        placeholder={t("approval-question.placeholder")}
        aria-label={t("chat-interactions.have_question")}
        className="min-h-16 w-full resize-y rounded-control bg-surface px-2.5 py-2 text-meta leading-relaxed text-ink shadow-btn outline-none placeholder:text-ink-3"
      />
      <div className="mt-1.5 flex items-center justify-between gap-2">
        <span className={cx("min-w-0 text-submeta leading-snug", tooLong ? "text-attention" : "text-ink-3")}>
          {t(tooLong ? "approval-question.too_long" : "approval-question.pending_note")}
        </span>
        <span className="flex shrink-0 gap-1.5">
          <Button tone="quiet" disabled={sending} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="secondary" busy={sending} disabled={busy || !approvalQuestionReady(question)} onClick={() => void ask()}>
            {t("approval-question.send")}
          </Button>
        </span>
      </div>
    </div>
  );
}
