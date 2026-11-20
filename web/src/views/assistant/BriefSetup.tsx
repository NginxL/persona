// The briefing's recommended-setup block (#5085), in its own chunk: drawn on the first briefing of a week at most, so its
// code and words stay off the first screen (#11794; web/src/startup-preload.ts FIRST_SCREEN_BUDGET).
import { t } from "../../i18n";
import { briefSetupPage, type BriefSetup } from "../../lib/feed";
import { navigate } from "../../lib/router";
import { Button } from "../../ui/atoms";
import { Section } from "./Brief";

/** Which mechanism that is off would have removed how many asks: the hub's own counts and words, each with the
 *  settings page it is turned on at (nothing is turned on from here). */
export function BriefSetupRows({ setup }: { setup: BriefSetup[] }) {
  return (
    <Section icon="spark" tint="bg-field text-ink-2" title={t("assistant-brief.setup_head")}>
      <div className="py-1 text-label text-ink-2">{t("assistant-brief.setup_intro")}</div>
      {setup.map((x) => {
        const page = briefSetupPage(x.id);
        return (
          <div key={x.id} className="flex items-center gap-3 py-1.5" data-brief-row="setup">
            <div className="min-w-0 flex-1 text-meta [overflow-wrap:anywhere]">{x.text}</div>
            {page && <Button onClick={() => navigate(page)}>{t("assistant-brief.setup_open")}</Button>}
          </div>
        );
      })}
    </Section>
  );
}
