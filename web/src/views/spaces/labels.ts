// Plain words for the information categories (#13500): what a person reads for each, everywhere a category shows.
import { t } from "../../i18n";
import { INFO_CATEGORIES } from "../../lib/tracks";

/** A category's name in the interface language; one this page does not know shows as the hub wrote it. */
export function categoryLabel(c: string): string {
  return (INFO_CATEGORIES as readonly string[]).includes(c) ? t(`spaces.category_${c}`) : c;
}
