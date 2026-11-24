// A recorded meeting's live view on its Knowledge detail (#3862 phase 2, docs/assistant/meeting-minutes.md "Phase 2:
// live view"): while Persona attends a room meeting that records, **Topics** (the room's, in its order: done, the current
// one, those still open; once it set any) and **Live** (the newest lines of the live log, at their clock time, the newest
// last). Only on a hub that lists `meeting_live_view`; useRecordingMeeting reads both from `GET /api/v1/recordings/{id}/live`
// and again on `meeting_live` or its 5-second poll. Read-only; the transcript keeps every line once uploaded.
import { useId, useLayoutEffect, useRef } from "react";
import { t } from "../../i18n";
import { type LiveLine, type MeetingTopic } from "../../lib/kb";
import { recordingClock, type RecordingView } from "./recording-view";
import { cx, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Card, SectionTitle } from "../../views/knowledge/KBParts";
import type { MeetingState } from "./RecordingConversation";

/** How close to the end of the Live box still counts as following it (new lines then scroll into view). */
const FOLLOW_PX = 24;

export function RecordingLiveView({ rec, meeting }: { rec: RecordingView; meeting: MeetingState }) {
  const live = meeting.live;
  if (!live) return null;
  return (
    <>
      <LiveTopics topics={live.topics} rec={rec} />
      <LiveLines lines={live.lines} cut={!!live.cut} rec={rec} />
    </>
  );
}

const STATE_LABEL: Record<MeetingTopic["state"], string> = {
  done: "recording.live_topic_done",
  current: "recording.live_topic_current",
  open: "recording.live_topic_open",
};

/** **Topics**, once the room set any (a room started before it had the tool never does): one chip per topic in the
 *  room's order, its state in words (the current one's visible, the others' for screen readers, beside a check for a
 *  done one); under them, the time the current topic began and the room's note on it. The other topics' time and note
 *  are read to screen readers in their chip and shown in its tooltip. */
function LiveTopics({ topics, rec }: { topics?: MeetingTopic[]; rec: RecordingView }) {
  const heading = useId();
  if (!topics?.length) return null;
  const since = (topic: MeetingTopic) => typeof topic.at === "number" ? t("recording.live_topic_since", { time: recordingClock(rec.startedAt, topic.at, rec.tz) }) : "";
  const now = topics.find((topic) => topic.state === "current");
  const nowDetail = now ? [since(now), now.note].filter(Boolean).join(" · ") : "";
  return (
    <section aria-labelledby={heading} data-live-topics>
      <SectionTitle title={<span id={heading}>{t("recording.live_topics_title")}</span>} />
      <Card>
        <div className="px-4 py-2.5">
          <ol className="flex flex-wrap gap-1.5">
            {topics.map((topic, i) => {
              const state = t(STATE_LABEL[topic.state]);
              const current = topic.state === "current";
              const detail = [since(topic), topic.note].filter(Boolean).join(" · ");
              return (
                <li key={i} data-topic={topic.state} title={[state, topic.title, detail].filter(Boolean).join(" · ")}
                  className={cx("inline-flex h-7 min-w-0 max-w-full items-center gap-1 whitespace-nowrap rounded-full px-2.5 text-[12.5px]",
                    current ? "bg-accent-tint font-medium text-accent-ink" : topic.state === "done" ? "bg-field text-ink-3" : "bg-field text-ink-2")}>
                  {topic.state === "done" && <Icon name="check" size={12} stroke={2.2} className="shrink-0" />}
                  {current ? <span className="shrink-0 font-semibold">{state}</span> : <span className="sr-only">{state}</span>}
                  <span className="min-w-0 truncate">{topic.title}</span>
                  {!current && detail && <span className="sr-only">{`, ${detail}`}</span>}
                </li>
              );
            })}
          </ol>
          {nowDetail && <p data-live-topic-now className="mt-1.5 text-[12px] leading-relaxed break-words text-ink-3 select-text">{nowDetail}</p>}
        </div>
      </Card>
    </section>
  );
}

/** **Live**: the newest lines at their clock time, in a box that follows the newest while it is scrolled to its end and
 *  that the keyboard reaches. `cut`: the reader skipped or let go of earlier lines, which the transcript keeps. */
function LiveLines({ lines, cut, rec }: { lines?: LiveLine[]; cut: boolean; rec: RecordingView }) {
  const heading = useId();
  const box = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  const newest = lines?.at(-1)?.n;
  useLayoutEffect(() => {
    const el = box.current;
    if (el && following.current) el.scrollTop = el.scrollHeight;
  }, [newest]);
  return (
    <section aria-labelledby={heading} data-live-lines>
      <SectionTitle title={<span id={heading}>{t("recording.live_title")}</span>} />
      <Card>
        {!lines ? (
          <div className="flex justify-center py-3"><Spinner /></div>
        ) : !lines.length ? (
          <p className="px-4 py-2.5 text-[12.5px] leading-relaxed text-ink-3">{t("recording.live_empty")}</p>
        ) : (
          <div ref={box} role="log" aria-labelledby={heading} tabIndex={0}
            className="max-h-72 overflow-y-auto rounded-card px-4 py-1.5 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
            onScroll={(ev) => { const el = ev.currentTarget; following.current = el.scrollHeight - el.scrollTop - el.clientHeight <= FOLLOW_PX; }}>
            {cut && <p data-live-earlier className="py-1 text-[11.5px] text-ink-3">{t("recording.live_earlier")}</p>}
            <ol>
              {lines.map((l) => (
                <li key={l.n} data-live-line={l.n} className="flex gap-3 py-1 text-[13px] leading-relaxed">
                  <span className="w-[60px] shrink-0 pt-px text-[11.5px] text-ink-3 tabular-nums">{recordingClock(rec.startedAt, l.start, rec.tz)}</span>
                  <span className="min-w-0 flex-1 break-words text-ink select-text">{l.text}</span>
                </li>
              ))}
            </ol>
          </div>
        )}
      </Card>
    </section>
  );
}
