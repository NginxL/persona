// The question marks of a Persona chat's rail (#13021, useQuestionMarks.ts), computed in a chunk of their own: the chat
// (ChatView) mounts this under Suspense and gets the marks and the "opened" mark back through callbacks, so the rules
// (lib/question-states.ts) and the read marks (lib/question-reads.ts) stay off the main chat's first screen
// (src/startup-preload.ts FIRST_SCREEN_BUDGET). Draws nothing.
import { useEffect } from "react";
import type { NavRailTick } from "../../lib/nav-rail";
import type { QuestionMark } from "../../lib/question-states";
import type { ChatLine } from "../../lib/turns";
import { useQuestionMarks } from "./useQuestionMarks";

export default function QuestionMarksHost({ onMarks, onOpened, ...input }: {
  mid: string;
  sid: string;
  on: boolean;
  lines: ChatLine[];
  busy: string | undefined;
  ticks: NavRailTick[];
  scroller: HTMLElement | null;
  /** the marks, each time they change (undefined while off) */
  onMarks: (marks: ReadonlyMap<string, QuestionMark> | undefined) => void;
  /** how to mark a question's answer opened (a jump to it) */
  onOpened: (opened: ((tickId: string) => void) | undefined) => void;
}) {
  const { marks, opened } = useQuestionMarks(input);
  useEffect(() => onMarks(marks), [marks, onMarks]);
  useEffect(() => {
    onOpened(opened);
    return () => onOpened(undefined);
  }, [opened, onOpened]);
  useEffect(() => () => onMarks(undefined), [onMarks]);
  return null;
}
