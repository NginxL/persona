# persona #6271 inventory — 2026-11-17 UTC

Audit for #6271, before broad template edits: 36 existing production Inbox templates. The maintained contract is in [Prompts](../../assistant/prompts.md). This record describes the starting callers and the implementation boundaries, not current ownership or live-model acceptance.

## Bounded phases (before edits)

1. Typed locale plumbing + ephemeral item/kind and model input wrappers. Triage (askTriage + subscription preview), draft (including extension never-quote retry), grouping tool loop, settled, task judge, folders and merged title each capture locale for a model request. Keep IDs, JSON keys, event/tool quotas, checks, source text, source kind descriptions and rule precedence unchanged. Localize Inbox-only shared-fragment wrappers without altering unrelated shared callers.
2. Complete en/ja branches for model instructions and all included Inbox fragments, with empty/zh-Hans bytes identical. Draft: mail follows incoming language/formality; WeChat counterpart language wins, owner language fallback; other reply policy stays unchanged. Folder/title source names and existing folders remain untouched. No translated model values are parsed: JSON enums/keys/evidence are canonical; evidence must remain a verbatim task quote.
3. Handling deliveries, resumed processing context, handoff and handoff-note. Queued inboxNote records remain raw data and render at delivery time; existing recap/chain/knowledge/decisions are never rewritten. Localized handoff-note provenance must use canonical language-independent metadata, not translated regex guesses; retain legacy Chinese recognition and user-authorship/client_msg_id protection. Agent first-line fallback in origins.go is legacy only; persisted event/session evidence and fixed title ownership remain authoritative. Do not change authorization/suggestion/execution gates.
4. Caller-level en/zh-Hans/ja goldens, empty-byte comparison, synthetic send/handoff/parser checks, source/approval/never-quote and model-output boundary tests, negative controls, downward-only ratchet, full affected suites + build/guards/docs/privacy. Report unavailable live-model evidence honestly; documented live judge table specifically lists agents/triage, task done, delegate, urgent calls (none are #6271 templates).

## Production callers and template coverage

- inbox_triage.go askTriage/triageModel + inbox_triage_preview.go: triage, tiers, item, kinds github/imessage/larkcli, extension_context, shared/user_rules. askTriage currently formats input before reading hello; preview formats outside goroutines. Capture same locale in input and system. promptItem currently used by other callers; external kindWords strings are source data, not translatable UI.
- inbox_triage.go draftReply: draft, draft_input, item, kinds, extension_context; retries share draft/input. OwnerNotes and instruction unchanged, source mail language retained.
- inbox_group.go askGroup: group, tiers, group_input, shared/user_rules. Kind from connector, context self marker `我（自己说的，不归类）`, owner mention markers `（@了我）` / `（@所有人）` remain literal wire/source vocabulary, explained in translations. Tool calls/assign/events validated structurally.
- inbox_settle.go askSettled: settled, settled_input. Mine flag -> translated label, IDs and source words unchanged; replied without owner message downgraded to over by existing code.
- inbox_handle.go judgeInboxTask: judge, judge_input, item, shared/user_rules. Existing evidence validation checks quote in task title/notes, not prose.
- inbox_folders.go fileBatch: folders, folders_input. Existing paths/examples are data, never rewritten for locale alone.
- inbox_merge_title.go rewriteMergedTitle: merge_title, merge_title_input. Existing titles/source names data.
- inbox_agent.go startInboxAgents -> inboxAgentFor -> RenderInboxAgent: agent, agent_title, item, agent_ref, handling notes, shared/kb_blocks. inboxAgentFor reconstructs resume recap/chain and carried pending notes. Capture owner + locale once before inbox lock, pass into all prompt fragments. Existing shared KB blocks source summaries stay ctx-escaped, Inbox wrapper headings can be localized without changing shared KB contract (owned by #6275).
- inbox_handle.go renderNote/deliver pending notes: all 11 handling/* templates. owner currently captured before lock; read locale at same time. inbox_handoff.go renderHandoffNote/tellHandoff: same notes with Handoff flag then handoff_note; include Failed/Who/Next/Checks (currently omitted in handoff helper, preserve behavior unless required for correctness).
- inbox_handoff.go handoff -> RenderInboxHandoff: handoff context + actual user Text outside it; preserve literal structure, ctx/attr protection and typed owner intent.
- subscriptions/deliver.go kindsOf -> RenderSubscriptionKind; InboxSources and preview_things.go PreviewThings: kinds/subscription. These are freshly generated descriptor data, not persisted incoming source text. Use existing Options.Lang callback and propagate at source generation; never translate external connector descriptions.

## Parsers and provenance

- prompts/injected.go ParseInjected has inboxHeadRe + inboxMarker for Chinese handoff_note; localizations need canonical marker like existing persona-wake, strict canonical JSON / reject malformed, duplicate, unknown/trailing data. Hub injected.go localizes State=inbox display already. UserAuthoredContext and hub client_msg_id must continue excluding authored copies.
- assistant/origins.go inboxFirst Chinese first-line title fallback only for historical sessions; originEvidence.inbox/session binding + titleByInbox carry current processing origins. Consider explicit structured new first-line identity only if needed; no heuristic English/Japanese title ownership.
- assistant/inbox_handle.go inboxProcessingPrompt wraps first and subsequent processing deliveries using structural turn tokens; translations must preserve wrapper and not invent or copy authority.
- Model output parsing stays JSON/structural, fixed tokens unchanged; no parser of localized prose. Shared user_rules callers outside Inbox must not be changed incidentally.
