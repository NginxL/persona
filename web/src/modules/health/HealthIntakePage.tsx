// The water and protein pages (#13176, docs/assistant/health.md): the default manager's own declarative
// pages (today, the records with Undo, the 7 and 30 day charts, the goal and reminder settings), drawn by the extension
// pages' renderer under the reserved id (lib/health-intake-ui.ts) the way the subscription pages are, so the web has no
// second renderer. A full page at `/extension/-health-intake/pages?state=`: the route the renderer already opens its pushed
// pages on, which Today's rings link to. The page draws again when the hub says a record was added or undone
// (`health_intake`, ExtensionView.tsx), so a press here and a log on the phone both show.
import { Suspense, useMemo, useState } from "react";
import { t } from "../../i18n";
import { ExtensionOwnerScope } from "../../lib/extension-owner";
import { HEALTH_INTAKE_PAGE, HEALTH_INTAKE_UI, healthIntakeState, type HealthIntakeKind } from "../../lib/health-intake-ui";
import { lazyPage } from "../../lib/lazy-page";
import { navigate } from "../../lib/router";
import { IconButton, Loading } from "../../ui/atoms";

const ExtensionUI = lazyPage(() => import("../../views/settings/ExtensionView").then((m) => m.ExtensionUI), { routes: ["extension"] });

/** Which page a state asks for: the hub's default (water) for none or one it can't read. */
export function kindOf(state: string | undefined): HealthIntakeKind {
  try {
    const k = (JSON.parse(state ?? "{}") as { kind?: unknown }).kind;
    return k === "protein" ? "protein" : "water";
  } catch {
    return "water";
  }
}

export function HealthIntakePage({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  // the state a ring's link carries (?state=); none opens water, as the hub does
  const state = useMemo(() => new URLSearchParams(location.search).get("state") ?? healthIntakeState("water"), []);
  const kind = kindOf(state);
  const title = kind === "protein" ? t("health.today_protein") : t("health.today_water");
  const [n, setN] = useState(0);
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("health.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <IconButton icon="arrowLeft" label={t("health.title")} onClick={() => navigate("/health/new")} />
        <h1 className="min-w-0 flex-1 truncate text-sm font-semibold text-ink">{title}</h1>
        <IconButton icon="retry" label={t("health.refresh")} onClick={() => setN((x) => x + 1)} />
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[880px] flex-col px-4 py-6 sm:px-8">
          <Suspense fallback={<Loading />}>
            <ExtensionOwnerScope owner={HEALTH_INTAKE_UI}>
              <ExtensionUI key={`${kind}/${n}`} extension={HEALTH_INTAKE_UI} ui={HEALTH_INTAKE_PAGE} title={title} bare state={state} />
            </ExtensionOwnerScope>
          </Suspense>
        </div>
      </div>
    </div>
  );
}

/** The page as the module host draws it (core/ModulePage.tsx ModuleExtensionPage). */
export function HealthIntakeModulePage({ onToggleSidebar }: { onToggleSidebar?: () => void }) {
  return <HealthIntakePage onToggleSidebar={onToggleSidebar ?? (() => {})} />;
}
