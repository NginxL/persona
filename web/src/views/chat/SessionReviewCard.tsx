// A finished independent review is rare: its report and answer controls load with the card, not the chat's first screen.
import { useRef, useState } from "react";
import { t } from "../../i18n";
import type { Interaction } from "../../lib/types";
import { Button } from "../../ui/atoms";
import { Markdown } from "../../ui/Markdown";
import { useChatCardTapGuard } from "./card-tap-guard";
import { PLAN_MAX } from "./dock";
import { Card, resolve, useCardKeys } from "./Interactions";

export function SessionReviewCard({ ix }: { ix: Interaction }) {
  const [busy, setBusy] = useState<string>();
  const sending = useRef(false);
  const q = ix.questions?.[0];
  const options = q?.options ?? [];
  const sendOption = options.find((option) => option.action !== "defer") ?? options[0];
  const dismissOption = options.find((option) => option.action === "defer") ?? options.find((option) => option !== sendOption);
  const go = async (label: string) => {
    if (!q || sending.current) return;
    sending.current = true;
    setBusy(label);
    try {
      await resolve(ix, { answers: { [q.question]: label } });
    } finally {
      sending.current = false;
      setBusy(undefined);
    }
  };
  const keys = useCardKeys({
    enter: () => {
      if (!sendOption || sending.current) return false;
      void go(sendOption.label);
    },
  });
  const tapGuard = useChatCardTapGuard(ix);
  if (!q || options.length !== 2 || !sendOption || !dismissOption || sendOption === dismissOption) return null;
  const kicker = typeof ix.review_count === "number" ? t("lib-turns.findings", { count: ix.review_count }) : ix.summary?.trim() || q.header || t("chat-interactions.question");
  return (
    <Card
      kicker={kicker}
      title={q.question}
      cardKeys={keys}
      tapGuard={tapGuard}
      footer={
        <>
          <Button tone="quiet" busy={busy === dismissOption.label} disabled={busy !== undefined} title={dismissOption.label} className="min-w-0 shrink" onClick={() => void go(dismissOption.label)}>
            <span className="truncate">{dismissOption.label}</span>
          </Button>
          <Button tone="primary" busy={busy === sendOption.label} disabled={busy !== undefined} title={sendOption.label} className="min-w-0 shrink" onClick={() => void go(sendOption.label)}>
            <span className="truncate">{sendOption.label}</span>
          </Button>
        </>
      }
    >
      <div className="overflow-y-auto rounded-[8px] bg-inset px-3 py-2.5 shadow-hairline" style={{ maxHeight: PLAN_MAX }} data-scroll-box>
        <Markdown text={ix.review_report ?? ""} small />
      </div>
    </Card>
  );
}
