# Voice mode: phase-one plan, risks and design review

Moved unchanged from the [voice guide](../../assistant/voice.md) (2026-11-10), which now describes only the current design: its sections 7 and 8 and the review appendix. Every sub-issue below is done. Issue and PR numbers are the previous tracker's; section numbers (4.5, 4.13, 6.10, ...) are the guide's. What phase two still lacks is listed in the guide's section 0 ("Limits" and "Not yet").

## 7. Work breakdown

Umbrella issue #3489, with sub-issues below. Each PR corresponds to one sub-issue (`Closes #<sub-issue>`) and also `Refs #3489`. All sizes include tests.
When everything is complete, move this section to `docs/archive/`.

| Issue | Title (issues and PRs in English) | Scope | Size | Dependencies |
| --- | --- | --- | --- | --- |
| #3495 | Empirical tests of GPT-Live behavior / docs(lessons): GPT-Live delegation behaviour | No feature code. Connect `personad voice` to a GPT-Live-compatible upstream (local is fine), test every question listed under risk 1 in section 8, write findings in new docs/lessons/voice.md, and choose the constants for 4.5 | S | — |
| #3496 | Voice-mode design document / docs: voice mode design | This document, CLAUDE.md documentation index, two pointers in `docs/extensions/extensions.md` | S | — |
| #3497 | channel: shared turn reading, option matching by ordinal and name / refactor(channel): share turn reading and spoken option matching | 4.12 | M (about 500) | — |
| #3498 | hub: forward caller identity to extensions and disconnect on revocation / feat(hub): tell extensions who is calling and cut them on revoke | 3.6 | S (about 300) | — |
| #3500 | Configure realtime voice upstream in the App / feat(voice): configure the GPT-Live upstream from the app | Section 5: manifest, adoption, environment variables, merge, `needs_setup`, Key/address binding, model fallback, `docs/extensions/extensions.md` | L (about 1000) | #3263 (merged) |
| #3501 | Track voice delegation by round and speak the full reply / fix(voice): follow the delegated turn, not the session's last line | `hublink`, `runs`, `items`, `spoken`, user-speech gate, role-based tool restrictions, `fake` dialect; target remains main chat; `report` / `delegated` / `spoken` prompts and `docs/assistant/prompts.md` | L (about 1300) | #3497 |
| #3502 | Bind voice to the current chat (protocol 2) / feat(voice): bind a voice call to the conversation it was opened from | `/info`, `target`, `send` (409 mapping, `resend`), `run` frames, Fold, status queries, stop prefix, one commentary per delegation, announcement gate, trimmed `resume`; prompts `instructions` / `target` / `handoff` / `agent_message` / `status` / `refused` / `folded` / `send_to_agent` | L (about 1800) | #3495, #3498, #3501 |
| #3503 | Speak rounds started elsewhere, heartbeats, and interruption / feat(voice): observed turns, heartbeats and barge-in | Observed runs (channel filtering), `Work` / `TurnErrorKind`, heartbeat, `progress` / `observed` prompts, protocol 2 interruption `clear` (follow upstream signals, otherwise PCM energy for 0.5 seconds; lessons recommends no backchannel filtering; protocol 1 regression tests) | M (about 900) | #3502 |
| #3504 | Answer Agent questions by voice, direct approvals to cards / feat(voice): answer agent questions by voice, point approvals to the cards | `asks.go`, `ask` frames, approval-word interception, `ask` / `needs_you` / `answered` prompts | M (about 800) | #3502 |
| #3505 | Suspend upstream while waiting for the Agent / feat(voice): pause the upstream while the agent works | 4.9, `link` frames | M (about 600) | #3503 |
| #3506 | iOS design system: voice orb and tokens / feat(ios): voice orb, tokens and icons | 6.6 | S (about 400) | — |
| #3507 | iOS voice-call foundation / feat(ios): voice call engine | `VoiceWire`, `VoiceSocket`, `PhoneVoiceAudio`, `VoiceConversation`, `VoiceCalls`, `VoiceAvailability`, unit tests; coordinate in #1951 first | L (about 1700) | #3496 |
| #3508 | iOS: voice mode in main-chat and Agent pages / feat(ios): voice mode in the main conversation and agent chats | Entry, `VoiceBar`, thread tail, lifecycle, tones, `ask` marker, plan feedback, DEBUG samples, voice_mock.py, screenshots | L (about 2000) | #3506, #3507 |

```
Research #3495 ─────────────┐
Server   #3497 ─▶ #3501 ─▶ #3502 ─▶ #3503 ─▶ #3505
         #3498 ─────────────┘  └─▶ #3504
Config   #3500 (#3263 merged, no other dependencies)
iOS      #3506 ─┐
         #3507 ─┴─▶ #3508 (entry appears only with /info.protocol ≥ 2, after #3502 is deployed)
```

- Can run in parallel from the start: #3495, #3496, #3497, #3498, #3500, #3506, #3507. #3501 is the first server feature PR, benefiting Android immediately.
- #3508 can merge first: the entry stays hidden without hub protocol 2, so it will not ship with known bugs exposed.
- About 11k lines total.

**Phase two (one issue each, unscheduled)**: dedicated iOS realtime-voice settings + `POST /test`; Mac voice; persisted captions interleaved by time in the thread; entry without a main chat (`ShellHome`).

**Acceptance**

| PR | Acceptance |
| --- | --- |
| #3495 | docs/lessons/voice.md answers every risk-1 question and specifies every constant in 4.5 |
| #3497 | Existing assistant tests all pass; new `channel` tests cover pagination, `/full`, Fold segmentation; `MatchOption` recognizes `2 / 第二个 / 二 / second / option name / multi-select` (the Chinese examples mean "second" / "two") |
| #3498 | Hub and extmgr tests: caller-forged `X-Persona-*` stripped; correct headers for full device, extension token, and main token; forwarded WS closed after revocation |
| #3500 | Postgres-backed extmgr tests (`test-server.sh`; if hub lacks Go, use local Postgres container or tunnel and state which in PR) covering same id after adoption, `disabled`, default off without entry, `needs_setup`, no Key echo, Key cleared on host change, upgrade/rollback. Voice unit tests cover merge/id collisions. Manual: filling address and Key in generic iOS page allows voice |
| #3501 | All protocol-1 scenarios in 4.13 pass; Android frame sequence unchanged; `delegate.go` removed; golden diff included in PR |
| #3502 / #3503 / #3504 / #3505 | Respective 4.13 scenarios pass; three-language goldens; `docs/assistant/voice.md`, `docs/extensions/extensions.md`, `docs/assistant/prompts.md` synchronized; run `fake` dialect against real hub and Agent |
| #3506 | Dark/light × two-style screenshots; `IconVocabularyTests`, `TokenRatchetTests`, `StyleSpecTests` pass; watch target compiles |
| #3507 | All 6.10 unit tests pass; no files in #1931 changed, no naming collisions; PR states actual Xcode, and "26.0 unverified" if unavailable |
| #3508 | Screenshot matrix inspected (including AX large text), all buttons single-line; `-openVoice YES` mock flow: connected → delegation → run → suspend → question → result → end; background, tab switch, page leave all end call; release.sh launch-argument check passes |

**Test plan**

- **Server**: local `go test ./internal/modules/voice ./internal/channel ./internal/assistant`; #3498 and #3500 run hub tests with Postgres.
- **iOS**: unit tests, DEBUG sample screenshots, mock flow, all on simulator.
- **Hardware end to end**: after merge, maintainer uses TestFlight against real hub. Echo cancellation, speaker playback, Bluetooth, and lock screen can only be checked on hardware. Checklist:
  1. Main chat: immediate small-talk reply; "Check tomorrow's calendar" delegates and speaks result.
  2. Claude Code session: "Run tests" → "From Voice" in thread; "How far along?" answered immediately without Agent send; heartbeat at 2 minutes; suspension after 60 seconds, automatic reconnect and speech when result arrives.
  3. Add an instruction while working: two combine into one result. "Stop a moment" only stops voice; "Stop Claude" actually stops Agent.
  4. AskUserQuestion: question spoken; "Second" or a casual answer outside options both resolve.
  5. Plans/tool permissions: fast layer category-only; spoken "Approve" not sent; "Return as feedback" works.
  6. Leave text in the computer's composer, then speak: "Clear and send" appears.
  7. Offline session starts.
  8. Interruption (short backchannels do not interrupt), mute, end; screen stays unlocked during call.
  9. Airplane mode for 5 seconds; redeploy hub: both resume and speak the round finished during disconnect.
  10. Agent outputs `回答：1` (Answer: 1), user silent: no answer submitted.
  11. Wrong Key: 403 and "Open Settings"; changed address host clears Key.
  12. Repeat with Codex.
  13. Android regression: main-chat voice works normally.

## 8. Out of scope / later, and risks

**Out of scope / later**

- **Phase two**:
  - persist captions (`voice_logs`) and interleave them by time in the thread;
  - full-screen orb; background and lock-screen calls with Live Activities;
  - switch chats by voice;
  - entry without a main chat;
  - dedicated settings and `/test`;
  - Mac, visionOS, web, watch; Android's Agent entry.
- **Later**:
  - other protocols (decision 3);
  - multiple upstreams in the App (after forms support object arrays);
  - generic Key/address binding (`bound_to`);
  - tell the model actual playback position after interruption (GPT-Live lacks truncate);
  - local speech synthesis (cascaded TTS).
- **Not doing**: approving plans or tools by voice (decision 5).

**Verified, no longer counted as risks**

- `subscribe` does not mark sessions seen (only explicit `seen` does), nor suppress push.
- Member hubs forward extension WS to the primary with original credentials (`peer_http.go`), so identity headers at the primary are correct.

**Risks and mitigations**

1. **Several GPT-Live behaviors remain untested**. #3495 must answer:
   - can a delegation stay open for several minutes;
   - does the first commentary close the delegation;
   - does `say` interrupt model speech;
   - does the model treat wrapped Agent output as instructions;
   - how instruction size affects opening latency.

   Mitigation: #3495 before #3502; pacing and limits are constants; every assumption has a fallback (`say`).
2. **Prompt injection**: Agent output enters the fast layer. Mitigations:
   - user-speech gate;
   - Agent text in data blocks;
   - only read-only tools after target binding;
   - approvals never answered by voice.

   Residual risk: within 30 seconds after user speech, injected content may still mislead the model.
3. **Misrecognized instructions sent immediately** (decision 4): messages carry original-speech context so the Agent can notice problems and ask; sent body visible in thread; user can always say "No, change it to…".
4. **Billing**: suspension (#3505) and idle ending. Screen-off background calls are phase two.
5. **Always-on screen uses battery**: only during calls, only `.phone`; restored at end.
6. **Hub redeploy disconnects calls**: App resumes automatically, Check speaks results completed during the gap.
7. **Conflicts with #1931**: type names, microphone text, call ownership, shared files. Independent names, rules by surface, coordinate #1951 first. #1931 still has not run CI.
8. **Hardware audio only through TestFlight**: simulator may not even support echo cancellation; fallback covers it.
9. **No precedent for adopting hub.json entries**: document in `docs/extensions/extensions.md`; test upgrade, rollback, `disabled`; build atop merged #3263.
10. **Security**: extension holds main token. Mitigations: caller identity headers, role restrictions, disconnect on revocation, Key/address binding, warnings for `ws://`.
11. **Linked hubs**: target not on the extension's hub gets no pushed events, only 5-second polling; results may be seconds late.
12. **Codex terminal-session items may lack `client_msg_id`**: use `Probe`, so the body must start with the task itself.
13. **ChatView is a hot path**: it reads only `isBound`; tail, levels, and captions are separately observed views; verify bottom-pinning remains intact.
14. **Compatibility**: only Xcode 26.0 SDK APIs; Tokens in `Shared/` must compile for watchOS 10; VoiceOver and call speech interfere, requiring hardware checks.
15. **Conflicts with other open PRs**: #2931 and #3342 change `ShellComposerBar`; several PRs change `mock_hub.py`, `Icons.swift`. Mitigation: rebase by merge order; separate voice mock file.

## Appendix: handling review comments

Design-review comments (2026-11-05 / 06) and responses, retained as the rationale for tradeoffs.

| Comment | Response | Reason |
| --- | --- | --- |
| Protocol-1 message id collisions | Accepted | Use `voice-<UUIDv4>`; only `resend` retains original id (4.3) |
| Protocol-2 call survives revocation | Accepted | Register forwarded WS by principal, close on revocation, include in #3498 |
| Protocol-3 function tools bypass permissions | Accepted | Non-full declares only `ask_persona` (#3501: no tools means no delegation); bound target keeps only read-only tools; target rejection fatal |
| Protocol-4 prompt-injection feedback loop | Accepted | User-speech gate + `<agent_output>` data block, remove "(System)" prefix |
| Protocol-5 address change steals Key | Accepted | Changing host clears Key in the same request (5.2) |
| Protocol-6a no utterance-complete event | Accepted | Merge with Experience-3: 1.2-second silence + upstream silent 1.5 seconds |
| Protocol-6b multiple commentary messages for one delegation | Accepted | One final result per delegation; other speech uses `say`, error fallback `say` |
| Protocol-6c thinking swallows acknowledgement | Accepted | No incremental progress; only its own handoff within 3 seconds of delegation creation |
| Protocol-6d reconnect limit never resets | Accepted | Reset after 60 seconds stable; suspension counted separately |
| Protocol-6e codex dialect `say` only 500 bytes | Accepted | Send chunks |
| Protocol-6f truncated `QuestionText`, no path for screen answers | Accepted | `ask.md` uses structured data, speaks only option names; brief `say` confirmation while asking |
| Protocol-7 Fold meets send failure | Accepted | Fold only after 200; rejected messages excluded from `MsgIDs` |
| Protocol-8 premature or duplicate announcements | Accepted | Look adds `TurnErrorKind` / `Work`; create observed run only when target has no run |
| Protocol-9 main-chat target drifts | Accepted | Protocol 2 fixes target at `ready`; protocol 1 unchanged |
| Protocol-10 `clear_draft` is a string | Accepted | `resend` returns raw hub `clearable` (#3502 review: not `draft`, differs with IDE selection markers) |
| Protocol-11 duplicate speech after resume | Accepted | Update `base_seq` after Hold; resume unfinished runs only; relocate on seq rollback |
| Protocol-12 inconsistent compatibility and numbers | Accepted | Interruption `clear` protocol 2 only with `dropAudio`; reconnect 1, 4, 10 seconds; hub waits 90 seconds; nil `capabilities` sendable; background rules phone-only |
| Protocol-13 crossed-out risks and new constraints | Accepted | Old risks 5, 11 moved to "Verified"; nonblocking hubLink reader |
| Experience-1 status cannot be queried, treated as instruction | Accepted | Status prefix answered directly by extension; no incremental progress |
| Experience-2 `clear` on first sound | Partly accepted | Protocol 2 only, backchannel threshold; no "tell model playback position": no truncate or silent path, and model already has full text |
| Experience-3 waiting for utterance completion hangs | Accepted | 1.2-second silence timer |
| Experience-4 lock screen disconnects, continuous billing | Accepted | No auto-lock during call; "Continue" until page leave; suspension separate #3505 |
| Experience-5 "About 1 second" impossible | Accepted | App provides `recent`; snapshot wait ≤500 ms; 3-second pre-`ready` buffer |
| Experience-6 casual answers enter a loop | Accepted | While asking, all delegation counts as answers; plan "Return as feedback" through a tap, consistent with decision 5 |
| Experience-7 other-channel round read aloud | Accepted | Main chat speaks only App/voice rounds; other channels one sentence |
| Experience-8 commands and paths read aloud | Accepted | Broader summary rules; approvals category-only; audible templates mark working/done, golden assertion |
| Experience-9 ambiguous "Stop" | Accepted | Stop only silences voice; stop prefix calls `interrupt`; separate buttons |
| Experience-10 entry hard to find | Accepted | Voice button persistent with empty composer; working dictation moves to expanded row; `voiceChannel` reused |
| Experience-11 squeezed thread, unlike Codex | Partly accepted | Temporary caption rows at thread tail, notifications merged into second row, task dock collapsed, renamed VoiceBar; chronological insertion awaits persistence in phase two |
| Experience-12 accessibility | Accepted | Orb is "Interrupt voice" while fast layer speaks; state announcements only |
| Experience-13 errors silent when not looking | Accepted | Local tones/haptics; localized error codes, raw text in details |
| Experience-14 Mac | Accepted | Separate follow-up issue, outside phase one |
| Scope-1 missing empirical step | Accepted | #3495 prerequisite for #3502; `fake` dialect; #3500 parallel from day one |
| Scope-2 acceptance impossible on dev machine | Accepted | iOS acceptance only simulator/mock/unit tests; hardware checklist after merge; local Postgres tests allowed, stated in PR |
| Scope-3 size underestimated, can cut scope | Partly accepted | Cut dedicated settings, `/test`, `/info.upstreams`, iOS protocol 1; previously separate item merged into #3508; sizes now include tests. Keep trimmed `resume`: frequent hub redeploys, and gap-completed rounds are the results users await. Keep "Clear and send": `input_draft` is otherwise a voice dead end, and App already holds draft |
| Scope-4 #3508 ships with known bugs | Partly accepted | #3501 first server PR. #3508 entry protocol 2 only, may merge first without exposing protocol 1 bugs |
| Scope-5 #3502 too large | Partly accepted | Split #3502, #3503, #3505. Interruption protocol 2 only, Android unaffected, included in #3503 with protocol 1 regressions rather than separate PR |
| Scope-6 lifecycle conflict with #1931 | Partly accepted | Background rules phone-only, coordinate shared files first. Still adapt #1931 audio per decision 6, as shared engine and ask author to use it |
| Scope-7 #3500 conflicts with ongoing extension work | Accepted | Build atop merged #3263; tests; `docs/extensions/extensions.md`; follow `hubJSONWaitsForTriageModel` |
| Scope-8 model-facing text assembled in Go | Accepted | Placeholders, backchannels, folded, observed all templates, with `Language` and three-language goldens |
| Scope-9 old risks 5, 11 answerable now | Accepted | Document in 3.5; nonlocal machines polling-only |
| Scope-10 contradiction between 3.4 and 3.7 | Accepted | Answer permission by `X-Persona-Role`, not protocol version |
| Scope-11 no entry without main chat | Accepted | Protocol 2 removes `startMain`; `ShellHome` entry phase two |
| Scope-12 conflicts with other open PRs | Accepted | Localization mechanism on master at merge; separate voice_mock.py; correct `modelConfigurationSheet(isPresented:purposeID:)` signature |
