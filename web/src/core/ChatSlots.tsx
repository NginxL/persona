// The chat slots (core/module-host.ts ChatSlotComponents, docs/web/web.md "Modules"): where the chat page, the composer
// and the pending cards draw what enabled modules' code fills. A module that names a slot has its code loaded when a
// page first reaches the slot; until it is in, and for a module that is off, the slot draws nothing, so with no module
// filling a slot every screen is as it was. A module's part that throws is left out, never the page.
import { Component, createElement, Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { Interaction } from "../lib/types";
import { lazyPage, type LazyPageComponent } from "../lib/lazy-page";
import { cardRendererOf, chatSlotFillers, loadModule, useModuleStates, type ChatBarPlace, type ChatPageProps, type ChatSlot, type ChatSlotComponents, type ComposerAction, type ModuleCardProps } from "./module-host";

/** Leaves out a module's part that throws, rather than the page around it. */
class SlotBoundary extends Component<{ id: string; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidCatch(error: unknown) {
    console.error(`module "${this.props.id}" failed in a chat slot`, error);
  }
  render() {
    return this.state.failed ? null : this.props.children;
  }
}

/** The enabled modules' components for a slot, each once its code is in; asks for the code of those not in yet. */
export function useChatSlot<S extends ChatSlot>(slot: S): { id: string; Component: NonNullable<ChatSlotComponents[S]> }[] {
  const states = useModuleStates();
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const fillers = useMemo(() => chatSlotFillers(slot), [slot, states]);
  const missing = fillers.filter((f) => !f.Component).map((f) => f.id).join(",");
  useEffect(() => {
    for (const id of missing ? missing.split(",") : []) loadModule(id).catch((e: unknown) => console.error(`module "${id}" could not load`, e));
  }, [missing]);
  return useMemo(() => fillers.filter((f): f is { id: string; Component: NonNullable<ChatSlotComponents[S]> } => Boolean(f.Component)), [fillers]);
}

/** `chat.tail`: what the modules draw at the end of the thread. */
export function ChatTail(props: ChatPageProps) {
  const fillers = useChatSlot("chat.tail");
  return (
    <>
      {fillers.map(({ id, Component: Part }) => (
        <SlotBoundary key={id} id={id}>
          <Part {...props} />
        </SlotBoundary>
      ))}
    </>
  );
}

/** `interaction.asked`: the modules' marks in a pending card's kicker row. */
export function InteractionMarks({ interaction }: { interaction: Interaction }) {
  const fillers = useChatSlot("interaction.asked");
  return (
    <>
      {fillers.map(({ id, Component: Part }) => (
        <SlotBoundary key={id} id={id}>
          <Part interaction={interaction} />
        </SlotBoundary>
      ))}
    </>
  );
}

/** A per-module callback that keeps one value for each module, stable across renders. */
function useReports<T>(): { values: Record<string, T | undefined>; reportFor: (id: string) => (value: T | undefined) => void } {
  const [values, setValues] = useState<Record<string, T | undefined>>({});
  const fns = useRef(new Map<string, (value: T | undefined) => void>());
  const reportFor = useCallback((id: string) => {
    let fn = fns.current.get(id);
    if (!fn) {
      fn = (value) => setValues((v) => (v[id] === value ? v : { ...v, [id]: value }));
      fns.current.set(id, fn);
    }
    return fn;
  }, []);
  return { values, reportFor };
}

/** `composer.trailingAction`: the modules' parts for this page (`slot`, drawn anywhere in it), and the action the
 *  composer draws, the first module's that offers one. */
export function useComposerAction(page: ChatPageProps): { action: ComposerAction | undefined; slot: ReactNode } {
  const fillers = useChatSlot("composer.trailingAction");
  const { values, reportFor } = useReports<ComposerAction>();
  const action = fillers.map((f) => values[f.id]).find(Boolean);
  const slot = fillers.map(({ id, Component: Part }) => (
    <SlotBoundary key={id} id={id}>
      <Part {...page} offer={reportFor(id)} />
    </SlotBoundary>
  ));
  return { action, slot };
}

/** `chat.bar`: the modules' bars for each place, and whether one takes the composer's place now. */
export function useChatBar(page: ChatPageProps): { taken: boolean; bar: (place: ChatBarPlace) => ReactNode } {
  const fillers = useChatSlot("chat.bar");
  const { values, reportFor } = useReports<boolean>();
  const taken = fillers.some((f) => values[`${f.id}/dock`] || values[`${f.id}/composer`]);
  const bar = (place: ChatBarPlace) =>
    fillers.map(({ id, Component: Part }) => (
      <SlotBoundary key={id} id={id}>
        <TakeReporter report={reportFor(`${id}/${place}`)}>{(take) => <Part {...page} place={place} take={take} />}</TakeReporter>
      </SlotBoundary>
    ));
  return { taken, bar };
}

/** Hands a bar its `take`, and gives the place back when the bar goes (its module switched off, the page left). The
 *  bar says it in a layout effect, so the composer gives its place before the frame is painted. */
function TakeReporter({ report, children }: { report: (taken: boolean | undefined) => void; children: (take: (taken: boolean) => void) => ReactNode }) {
  useLayoutEffect(() => () => report(undefined), [report]);
  return <>{children(report)}</>;
}

// ── card renderers ─────────────────────────────────────

const cards = new Map<string, LazyPageComponent<ModuleCardProps>>();

function cardOf(module: string, kind: string): LazyPageComponent<ModuleCardProps> {
  const key = `${module}/${kind}`;
  let card = cards.get(key);
  if (!card) {
    card = lazyPage(() => loadModule(module).then((code) => code.cards![kind]!), { listed: false });
    cards.set(key, card);
  }
  return card;
}

/** The enabled module that draws a card of this kind, if one does (core/module-host.ts `cardRenderers`). */
export function useCardRenderer(kind: string): string | undefined {
  useModuleStates();
  return cardRendererOf(kind)?.id;
}

/** A pending card of a kind a module draws: its module's card, loaded on first use. */
export function ModuleCard({ module, interaction }: { module: string; interaction: Interaction }) {
  return (
    <SlotBoundary id={module}>
      <Suspense fallback={null}>{createElement(cardOf(module, interaction.kind), { interaction })}</Suspense>
    </SlotBoundary>
  );
}

/** Tests only. */
export function forgetModuleCards(): void {
  cards.clear();
}
