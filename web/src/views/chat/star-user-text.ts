// Only the star action loads the complete text, before the snapshot's character limit is applied.
import { t } from "../../i18n";
import { fullItem } from "../../lib/api";
import type { StarOrigin } from "../../lib/starred-rows";
import { parseUserText } from "../../lib/turns";

export function starUserText(source?: StarOrigin): (() => Promise<string>) | undefined {
  if (!source?.userInputs?.some((item) => item.kind === "text" && item.full_size)) return undefined;
  const blocks = source.userInputs.filter((item) => item.kind === "text");
  return async () => {
    const words = await Promise.all(blocks.map(async (block) => {
      if (!block.full_size) return parseUserText(block.text ?? "").text;
      const whole = await fullItem(source.mid, source.sid, block.id);
      if (whole.id !== block.id || whole.kind !== "text" || whole.role !== "user" || whole.full_size ||
          block.source_fingerprint && block.source_fingerprint !== whole.source_fingerprint) {
        throw new Error(t("chat-fulltext.failed"));
      }
      return parseUserText(whole.text ?? "").text;
    }));
    let text = "";
    let entry = "";
    for (const [i, block] of blocks.entries()) {
      const next = block.id.split("#")[0];
      if (text) text += source.merged && entry !== next ? "\n\n" : "\n";
      text += words[i];
      entry = next;
    }
    return text;
  };
}
