// The id the water and protein pages (#13176) are drawn under by ExtensionUI: the default manager's own, like the
// subscription pages (lib/subscriptions-ui.ts), and no extension can have it (ids never start with "-"). Their page id
// is `pages`; the state `{"kind":"water"}` or `{"kind":"protein"}` says which one (docs/assistant/health.md).
export const HEALTH_INTAKE_UI = "-health-intake";
export const HEALTH_INTAKE_PAGE = "pages";
export type HealthIntakeKind = "water" | "protein";

/** The first state of a page; the hub's own state replaces it from the first answer on. */
export const healthIntakeState = (kind: HealthIntakeKind): string => JSON.stringify({ kind });

/** Where a page opens in the app: the route the renderer opens a pushed page on (`/extension/<id>/<ui>?state=`,
 *  docs/protocol/deep-links.md), without the sub-path. */
export const healthIntakePath = (kind: HealthIntakeKind): string =>
  `/extension/${encodeURIComponent(HEALTH_INTAKE_UI)}/${encodeURIComponent(HEALTH_INTAKE_PAGE)}?state=${encodeURIComponent(healthIntakeState(kind))}`;
