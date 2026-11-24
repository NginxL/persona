// A pill's visible and spoken effort use the same canonical tier in each locale.
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { initI18n, t } from "../../i18n";

it.each(["en", "zh-Hans", "ja"] as const)("says XHigh in the badge, tooltip and accessible name in %s", async (lang) => {
  await initI18n(lang);
  const { ModelPill } = await import("./ModelPill");
  const html = renderToStaticMarkup(<ModelPill current={{ engine: "claude", provider: "gateway", model: "Opus 5.5 (XHigh)", effort: "xhigh" }} onOpen={() => {}} />);
  expect(html).toContain('<span title="XHigh" data-pill-effort');
  expect(html).toContain(">XHigh</span>");
  expect(html).toContain('title="Claude Code · Opus 5.5 · gateway · XHigh"');
  expect(html).toContain(`aria-label="${t("chat-modelpill.aria_effort", { model: "Opus 5.5", effort: "XHigh" })}"`);
});
