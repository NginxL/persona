// A small line counting a chat's questions in progress and unread (#13021), from the same marks its question rail draws
// (lib/question-reads.ts useQuestionSummary): never a count of its own. Nothing while there is nothing to come back to.
import { t } from "../../i18n";
import { useQuestionSummary } from "../../lib/question-reads";

export function QuestionSummary({ scope, onOpen }: { scope: string | undefined; onOpen?: () => void }) {
  const s = useQuestionSummary(scope);
  if (!s.working && !s.unread) return null;
  const parts = [
    s.working ? t("chat-navrail.summary_working", { count: s.working }) : "",
    s.unread ? t("chat-navrail.summary_unread", { count: s.unread }) : "",
  ].filter(Boolean);
  const body = (
    <>
      <span className="text-ink-2">{t("chat-navrail.summary")}</span>
      {s.working > 0 && <span data-question-summary-working className="flex items-center gap-1 text-ink"><span aria-hidden className="size-1.5 rounded-full bg-accent" />{parts[0]}</span>}
      {s.unread > 0 && <span data-question-summary-unread className="flex items-center gap-1 text-ink"><span aria-hidden className="size-1.5 rounded-full bg-red" />{parts.at(-1)}</span>}
    </>
  );
  return onOpen ? (
    <button type="button" data-question-summary onClick={onOpen} aria-label={`${t("chat-navrail.summary_open")}: ${parts.join(", ")}`} className="flex items-center gap-3 text-sm hover:underline">
      {body}
    </button>
  ) : (
    <p data-question-summary className="flex items-center gap-3 text-sm">{body}</p>
  );
}
