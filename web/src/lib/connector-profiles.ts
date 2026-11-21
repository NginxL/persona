// What this client knows about a connector by its id: its brand mark and its own wording. Kept apart from
// connectors.ts so the inbox, which is on every first screen, can name a connector's reply button without loading the
// connector pages' code and strings (the Connectors settings page, its sign-in steps and thread pages).

import { t } from "../i18n";

/** The marks that exist as files (ui/BrandMark.tsx); a connector with none of its own draws the generic icon. */
export type BrandId = "github" | "telegram" | "lark";

/** What this client knows about one connector by id. Everything is optional: a connector that is not in the table (any
 * plugin's) gets the generic icon and the generic wording, built from its own `name`. Whether an extension may bring its own
 * wording in its manifest is a protocol question, not settled here. */
export interface ConnectorProfile {
  brand?: BrandId;
  /** The inbox button for an item answered in the app itself ("Reply in Telegram"). */
  openTitle?: string;
  /** What switching it on brings in, in the switch's footer; the default is "its content". */
  reads?: string;
  /** The sign-in step's footer: where the credentials live and what is shown of them. */
  signInFooter?: string;
  /** Signing out also ends the session at the service (Telegram's log-out call); false or absent: the hub only forgets its copy. */
  logoutEndsRemoteSession?: boolean;
}

// A Map, not an object: an extension's id may be `constructor` or `valueOf`.
const PROFILES = new Map<string, ConnectorProfile>([
  ["github", { brand: "github" }],
  ["lark", { brand: "lark" }],
  ["larkcli", { brand: "lark" }],
  [
    "telegram",
    {
      brand: "telegram",
      openTitle: t("lib-connectors.telegram_open_title"),
      reads: t("lib-connectors.telegram_reads"),
      signInFooter: t("lib-connectors.telegram_sign_in_footer"),
      logoutEndsRemoteSession: true,
    },
  ],
]);

/** Which mark a connector has of its own; others draw the generic one. */
export function connectorBrand(id: string): BrandId | undefined {
  return PROFILES.get(id)?.brand;
}

/** The inbox button for a connector's item that is answered in the app itself; undefined for a connector with no wording of its own. */
export function connectorOpenTitle(id: string): string | undefined {
  return PROFILES.get(id)?.openTitle;
}

/** The table's entry for a connector, empty for one it does not know. */
export function connectorProfile(id: string): ConnectorProfile {
  return PROFILES.get(id) ?? {};
}
