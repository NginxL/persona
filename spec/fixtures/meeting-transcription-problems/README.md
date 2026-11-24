# Why the hub isn't transcribing a meeting

In a hub-mode meeting the hub tells the phone, with the audio socket's `problem` frame and `transcription.problem`, why transcription is degraded (#13446, [docs/assistant/meeting-minutes.md "Problems"](../../../docs/assistant/meeting-minutes.md)). The transcription contribution ends a job with an error code (an open set); the manager passes its kind on as the `problem` code (an open set too); the phone's fallback notice says why, in the owner's words, and what to do. It never shows the error's message, a log id or account details.

| File | Covers |
| --- | --- |
| `cases.json` | Each kind: refused, busy, unreachable (including no upstream), failed (including a frame the extension couldn't read and a code from a newer extension); a problem from a newer hub; `no_interim` |

## Rules

1. Extension code → `problem` (the manager, `problemOfJob`): `upstream_forbidden` → `upstream_forbidden`; `upstream_busy` → `upstream_busy`; `upstream_unavailable` and `no_upstream` → `upstream_unavailable`; anything else → `upstream_failed`. A job that can't be opened at all (the contribution not running, no `ready` in 30 seconds) is `upstream_unavailable` too.
2. `problem` → what the phone says (`MeetingTranscriptionWords.upstreamKind`): `upstream_forbidden` refused, `upstream_busy` busy, `upstream_unavailable` unreachable, `upstream_failed` failed; any other code (`kind: null`) has no reason of its own: the phone says only that the hub isn't transcribing, as before #13446.

## Structure

- `{"version": 1, "cases": [...]}`; `extension_code` is the job's fatal error code (absent: a phone-only case), `problem` the code the phone gets, `kind` what the phone makes of it (`refused`, `busy`, `unreachable`, `failed`, or `null`).
- All data is made up.

## Verification

- Hub: `cd server && go test ./internal/assistant -run TestTranscriptionProblemsFollowTheSharedCases`.
- iPhone: `apple/PersonaTests/MeetingTranscriptionTests.swift` (`MeetingTranscriptionShownTests`).
