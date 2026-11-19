# Lessons: model display names and raw IDs

## Friendly names must never flow back into requests (#2201)

- The input bar and model picker share a display helper. Format only the name portion of GPT IDs; do not maintain a guessed catalog of models. Preserve versions, variants, and date suffixes.
- If a list item's `name` differs from its `id` (case-insensitive), treat it as an explicit alias and preserve it verbatim. An empty name falls back to the ID; non-GPT names keep their existing display behavior.
- Preserve known aliases after the list closes, too. This is temporary UI state scoped to a machine / launcher; it does not change model selection or persisted configuration.
- To decide whether a detail row needs to show the ID as well, keep comparing the raw `name` and `id`, not the friendly name. Otherwise, merely stripping a prefix creates an unnecessary second line for every GPT item.
- Search may match both the friendly name and the raw ID; selection callbacks, requests, and serialization still use the raw ID. Isolated browser verification must check menu text, the label after closing the menu, and intercepted request parameters, not just the helper's output.
- After changing a shared helper, also run the new-session tests that depend on it (#2302). Changes to `AgentModel.label` affect the fallback in `defaultModelDetail`, even if the new-session page itself is unchanged. When updating display expectations, retain request assertions for the raw model ID and effort.

If Android's existing input bar has no model list / current-model data, do not add a new query and selection flow just for a display change. Reuse the session model already passed in, and preserve the existing fallback when it is missing.

## Gateway reply names are not launch selections (#10125)

A gateway can echo a different model ID from the one the launcher accepted. Keep the launch ID in node state and use it for assistant carry-over; leave the echoed name available for display. Distinguish an explicitly empty launch model (launcher default) from missing metadata on an older node. Configuration and conversation picks still take precedence.

Session metadata has two encoders: adding a field to `proto.Session` also requires its line and layout hash in `session_wire.go`. Run the wire/reflection equality and allocation tests together; falling back to reflection preserves data but can break otherwise unrelated session-list allocation budgets.

## Display text is not a wire model (#4663)

- A launcher that names no model sends an empty `model`, not a localized placeholder. Normalize the legacy placeholder at the pure display/fallback boundary too: older nodes still send it, and an empty-catalog fallback must not turn it into a concrete row or a model pick.
- Removing the placeholder must not change launcher inference. An absent model is unknown, never evidence for choosing between several launchers on one endpoint; keep the no-guess test beside the wire tests.

## A launcher label must survive the narrow composer (#10248)

Two launchers can run the same model. Reuse the panel's launcher name in an Agent's pill and keep it alongside the shortened model at phone width; an engine mark alone cannot distinguish accounts. Keep the full current and queued setups accessible, and preserve the model-only fallback when the session has no explicit launcher. A label correction must not change the launcher or model sent to the hub.
