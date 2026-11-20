// Overview under the greeting (#9745, #10905): the extensions' cards and Overview's own sections in the order the user
// arranged them (lib/overview-cards.ts), kept on the hub for all their devices; with nothing arranged, the sections and
// then the cards, last (#11147). The greeting and its summary stay first (views/Home.tsx). While Overview is
// arranged (`editing`), the editor's heading comes first, the cards take their controls in place of their heads and
// each section is drawn as a bar with its own (the editor's chunk, views/extensions/OverviewArrange.tsx).
import { Fragment, useMemo } from "react";
import { useExtensionSlot } from "../lib/extension-slots";
import { arrangeOverview, overviewRuns, useOverviewPrefs, type SectionId } from "../lib/overview-cards";
import { useFolded } from "../lib/use-folded";
import { OverviewArranging, OverviewCards } from "./extensions/ExtensionSlots";

/** What each of Overview's own sections draws (nothing while it has no rows). */
export type OverviewSections = Record<SectionId, React.ReactNode>;

/** `start`: drawn where the sections begin (Loading, before the first snapshot); `after`: drawn after a section's
 *  place, hidden or not (Unread's: the card that says all is quiet, or how to add a computer). */
export function OverviewBody({ sections, editing, onDone, start, after }: { sections: OverviewSections; editing: boolean; onDone: () => void; start?: React.ReactNode; after?: Partial<Record<SectionId, React.ReactNode>> }) {
  const cards = useExtensionSlot("overview_card");
  const prefs = useOverviewPrefs();
  const items = useMemo(() => arrangeOverview(cards, prefs), [cards, prefs]);
  const runs = overviewRuns(items);
  // the cards fold as one, wherever they are (#11148): kept per browser with the sidebar's folds (`fold:overview-cards`),
  // open until the owner folds them
  const fold = useFolded("overview-cards", false, { all: false });
  const lead = runs.findIndex((r) => !("cards" in r));
  return (
    <>
      {editing && <OverviewArranging items={items} onDone={onDone} />}
      {runs.map((r, i) => {
        if ("cards" in r) return <OverviewCards key={r.key} cards={r.cards} items={items} editing={editing} fold={fold} />;
        if (editing) return <OverviewArranging key={r.key} items={items} section={r.section} />;
        return (
          <Fragment key={r.key}>
            {i === lead && start}
            {!r.hidden && sections[r.section]}
            {after?.[r.section]}
          </Fragment>
        );
      })}
    </>
  );
}
