# Scheduled task execution form

The synthetic browser fixture uses intercepted HTTP and WebSocket responses exclusively; it contacts no real hub,
task or model. Review images belong to [PR #6806](https://github.com/nwverse/persona/pull/6806), not the source tree.

Run against an owned loopback Vite server, writing captures outside the checkout:

```sh
node web/e2e/task-execution.mjs http://127.0.0.1:5173 \
  --shots <capture-directory> --chromium <chromium-executable>
```

The fixture covers Cron, inherited and explicit execution summaries before the prompt, save/reopen, directory choices,
source-chat validation/toggle/clear and unavailable-launcher recovery. It checks desktop and phone widths in light and
dark themes, doubles text size, verifies controls remain within the dialog and records page errors. The largest-text
form scrolls with its footer available. Model discovery loading/failure is checked after changing computers; an older
context's model must not remain selectable. Deferred-response component tests also cover late responses after failure.
The roster has two assistants: light-theme runs follow the default and dark-theme runs select the other assistant.
Every task/default/launcher/model read and task save asserts the matching manager; task launcher reads explicitly
request task inheritance. Component regressions cover assistant switches during each discovery stage, null defaults,
loading and failures, including a transport that delivers an old response after cancellation.

A real model-catalog 404 exercises the nullable older-hub contract: expanding the editor keeps the saved unknown model
and effort, and selecting a manually entered model ID saves only that model change.

The failed Run now flow holds a successful save response while the title is edited, then refuses the separate run.
The same open form must display the canonical provider/computer pins, retain the newer title, and save an unrelated
edit after defaults change without clearing those pins or sending another run. It captures the retained form in
both themes and text sizes; service regressions separately assert persisted bindings and actual dispatch after restart.

A legacy source task with unchanged launcher/effort metadata permits a sparse title save without an execution warning.
Changing its effort still disables Save; restoring the saved value permits it again. Captures cover both text sizes,
themes and widths, and the intercepted write must omit every execution field and retain the source conversation.

The form reuses the existing dialog, fields, native selects, model picker and semantic tokens. Advanced options stay
collapsed by default. Follow the repository's screenshot-in-PR workflow when publishing captures.
