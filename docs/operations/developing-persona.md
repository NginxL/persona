# Developing Persona: recording and exporting a device's diagnostics

**Who it serves**: people developing or investigating Persona (the owner, testers), not everyday Assistant users. So it
lives only in Settings › About, is off by default and shows nothing anywhere else (#9460).

A switch, **Developing Persona**, in each client's About. While it is on, the device records its own logs, errors and
energy use; **Export Diagnostics…** packs them into one zip the owner can share or send to an Agent. Nothing goes to the
hub or anyone else unless the owner sends the file, and then only as an ordinary file in one of their own chats.

| Client | State |
| --- | --- |
| iPhone / iPad | #9460: switch, recording, MetricKit, export with Send to Agent and Share File |
| Web, desktop app | #9617: switch, recording, export with Send to Agent and Download; #9675: the desktop app's main process |
| Android | planned, #9618 |
| Watch | not in the first version (MetricKit is not available on watchOS) |

## The switch

- Settings › About, under Version. Stored on the device (iOS `UserDefaults` `devMode.on`, `devMode.since`), never
  synced. Footer: what it records, that the data stays on the device, that turning it off deletes it.
- While on, the group adds **Recorded** (since when, how much) and **Export Diagnostics…**, and on iOS **Call Handle**
  (**Generic**, the default, or **Email**, `devMode.callHandle`): the kind of handle a call to Persona carries to the
  system, an on-device experiment for the Phone app's Recents (#9887, `docs/apple/calls.md` "Recents and intents"). It
  counts only while the switch is on; turned off, calls carry the generic handle again.
- Turning it off deletes everything recorded. When something is recorded it asks first: **Export First…** opens the
  export page, **Turn Off and Delete** goes ahead.

## What is recorded (iOS)

Only numbers and words the app's code chose. Kept at most 24 hours of events, 14 days of MetricKit payloads, 7 days of
errors and 50 MB in all, the oldest dropped first (`DevStore`, `Application Support/dev-mode`, out of backups).

- **Events** (`events.jsonl`, one JSON object a line, `t` in UTC and `kind`):
  - `minute`: one a minute while the app is in front, one for each stretch in the background. The app process's CPU
    time, interrupt and idle wakeups and energy for that span (`task_info(TASK_POWER_INFO_V2)`, the numbers the
    simulator's `proc_pid_rusage` gives), hub requests by route pattern (count, bytes in and out, median and longest
    time, status codes), hub WebSocket frames by message type, bytes, connects and drops, seconds per screen, network
    path (`NWPathMonitor`: Wi-Fi, cellular, other; expensive, constrained), thermal state, battery, Low Power Mode, build.
  - `foreground`: one per period in front: length, CPU, wakeups, energy, requests, bytes, frames, screens and their
    seconds, thermal state and battery at start and end.
  - `app`, `page`, `thermal`, `power`, `battery`, `path`, `socket`, `request_error` (the first of each failing route and
    status in a minute).
- **Errors** (`errors.jsonl`): the app's error reports (`ErrorReports`, uncaught exceptions with call stacks), kept here
  too while the switch is on; whether they also go to the hub's `error_reports` is unchanged.
- **MetricKit** (`metrickit/`): daily `MXMetricPayload`s and `MXDiagnosticPayload`s (crashes, hangs, CPU and disk-write
  exceptions) as delivered, plus `pastPayloads` / `pastDiagnosticPayloads` when the switch is turned on.
- **Log** (`log.txt`): this launch's own `Logger` lines (subsystem `persona`), read from `OSLogStore` at export; iOS
  keeps no earlier launch's log for the app.

Screens are named by the code: the tab (`assistant`, `agents`, `inbox`, `settings`, `feishu`) or a screen opened over it
with `.devScreen(…)` (`chat`, `assistant-chat`, `screen`, `knowledge`).

**Cost while on**: no timer in the background; in front one wakeup a minute rolls the summary. Counters live in memory
behind a lock and lines are appended in batches on a utility queue (crash reports are written at once). While off, the
hooks (`DevProbe`, `HubRedirects.finished`) are nil checks.

## What is recorded (web and the desktop app)

The same lines and limits in spirit, kept in IndexedDB (`persona-dev-mode`) at most 24 hours and 5 MB. The switch is
`localStorage` `persona.devMode` (when it was turned on) in Settings › About Persona; turning it off deletes the records,
asking first when something is recorded.

- `minute` (a minute while the page is visible, one per hidden stretch): hub requests by route pattern (count, bytes as
  declared, median and longest time to the answer, status codes), hub WebSocket frames by type, bytes, connects and drops,
  seconds per page (the address's first word: `s` for an Agent's chat, `settings`, `home`), `online`, `memory_mb` where the
  browser tells it.
- `foreground` per visible period; `app` (recording, visible, hidden), `page`, `network`, `socket`, `request_error`.
- `errors.jsonl`: uncaught errors, unhandled rejections and freezes of 5 s or more (`lib/freeze-watch.ts`), whether or not
  the hub takes error reports. `log.txt`: `console.error` / `console.warn` lines, scrubbed (an error a boundary caught is
  logged there too).
- The manifest names the platform (`web`, or `desktop` inside the desktop app), the system and browser, the web build (its
  entry script's name) and the hub's version.

**No first-screen cost.** Everything lives in chunks that load only while the switch is on (`lib/dev-mode.ts`,
`lib/dev-package.ts` with jszip, `views/settings/DevExportDialog.tsx`). The start-up check and the WebSocket wrap are in
`lib/dev-probe.ts`, loaded by `lib/error-reports-start.ts`, which runs before the app opens its socket and is outside the
first screen's budget. The recorder hooks `fetch`, `history`, `popstate`, `error`, `unhandledrejection`, the console and
the freeze watch itself and puts them back when the switch goes off; it imports nothing from the first screen's modules
that they do not already export, so its route word list is a copy of `lib/error-reports.ts`'s (`dev-mode.test.ts` keeps
them equal). A socket already open when the switch is turned on is counted from its next connection.

## The desktop app (#9675)

The desktop app shows the web page, so its switch, recording and export are the web's. While the switch is on the page
also tells the shell (`personaDesktop.setDevDiagnostics(true)`; off: `false`, which deletes what the shell kept), and the
main process records what the page cannot see, in `<user data>/dev-diagnostics/main.jsonl` (2 MB, then moved to
`main.1.jsonl`; about 4 MB in all): `shell_minute` (each process type's count, CPU percent, idle wakeups a second and
memory, from `app.getAppMetrics()`, and whether on battery), `power` (suspend, resume, on battery / AC, lock),
`thermal` (macOS), `process_gone` (a renderer or child process: type, reason, exit code), `window` (unresponsive /
responsive) and `shell` (recording started, versions). `crashReporter` runs with `uploadToServer: false`: dumps stay on
the computer; the package only counts those written since recording began (`counts.crash_dumps`).

The export reads the lines with `personaDesktop.readDevDiagnostics()` and adds them as `desktop.jsonl`, by the same
categories (`shell_minute`, `power`, `thermal` with Energy; `process_gone`, `window` with Errors; the rest with Logs).
Both operations answer only the hub page's main frame on an http(s) address (`readerAllowed`); a browser or an older
shell has neither and the package goes without. Code: `desktop/src/dev-diagnostics-core.ts` (lines, rotation, metrics,
the sender check; tests in `desktop/test/dev-diagnostics.test.ts`), `desktop/src/dev-diagnostics.ts` (Electron wiring,
installed at the end of `desktop/src/main.ts`), `desktop/src/preload.cts`, `spec/desktop-bridge.d.ts`.

## The package

`persona-diagnostics-<platform>-<yyyyMMdd-HHmm>.zip` (local time), a folder of the same name inside:

```
README.md          for the Agent reading it: what each file is, how to read it, what was scrubbed
manifest.json      format "persona-diagnostics", version 1, platform, os, os_version, device_model, app_version, build,
                   commit, utc_offset_minutes, recording_since, exported_at, window {from, to}, categories, counts
events.jsonl       the timeline above
errors.jsonl       error reports, in the hub's error report format
metrickit/*.json   MetricKit payloads as delivered (strings scrubbed, identifiers kept)
log.txt            this launch's log lines
```

The export page lists four categories; the first three can be left out: **Logs** (`log.txt` and the event kinds
other than the energy ones), **Errors and Crashes** (`errors.jsonl`, `metrickit/diagnostics-*`), **Energy and
Performance** (`minute`, `foreground`, `thermal`, `power`, `battery`, `path`, `metrickit/metrics-*`), and **Device and App
Version** (`manifest.json`, `README.md`, always).

## Send to Agent

The export page's first button. It builds the package and opens **Send To**, the targets of Forward To
(`ForwardTargets`, the same rows `ForwardFixedRow` / `ForwardSessionRow`): Persona's main chat, the recent targets, each
computer's live Agents, and search. Picking one opens that chat with the zip as a file card over its box, uploaded to the
computer the chat runs on as any picked file is (`ComposerAttachments.files`), and a note in the box ("Here are the Persona
diagnostics from my device (since …). Please find out what is using the battery and what is failing.") unless the box
already holds words. Nothing is sent until the owner sends it; the Agent reads the zip from that computer. Persona with no
main conversation yet: the new conversation's box. **Share File…** is the system share sheet.

On the web the export dialog's first button is the same: Send To lists Forward to's targets with its rows (exported from
`views/chat/ForwardDialog.tsx`), and the zip goes into the chosen chat's composer files (`lib/composer-files.ts` `addFiles`,
uploaded to that chat's computer) with the note put in the box before any words it holds (`sendDraft` `unsent`).
**Download** saves the zip.


## What is never included

Message text, knowledge base content, keys, tokens, passwords, cookies, pairing links, the device's name, the hub's
address, machine names, accounts. Recorded lines carry no free text; free text that does go in (error messages, log
lines, MetricKit strings) passes `DevScrub.text`:

- a URL keeps its path only (no scheme, host, query or fragment, so no pairing code or token);
- `Bearer` / `Basic` values, `token=`, `key=`, `secret=`, `password=`, `cookie=`, `authorization:` values, JWTs and runs of 32
  or more letters and digits become `<redacted>`; e-mail addresses `<email>`; IPv4 and IPv6 addresses `<ip>`; host names
  `<host>`; `/Users/<name>` and `/home/<name>` `~`;
- each text is cut to 1 KB.

Route patterns are the web error reports' (`pathPattern` in `web/src/lib/error-reports.ts`): every path segment that is
not one of the API's words is `:id`. `DevModeTests` keeps the Swift list equal to the web's.

## Code (iOS)

`apple/Persona/DevMode/`: `DevMode` (the switch, lifecycle, device observers, the minute roll), `DevRecorder` (counting
and lines), `DevStore` (files, limits), `DevMetrics` (MetricKit), `DevPackage` (the zip), `DevScrub`, `DevModeViews`
(About's group, Export Diagnostics), `DevSend` (Send To and the hand-off to a chat's box). Shared code reports through `apple/Shared/DevProbe.swift`; requests through
`HubRedirects.finished` in PersonaWire. DEBUG: `-openAbout YES` opens About, `-openAbout export` its export page.
Tests: `apple/PersonaTests/DevModeTests.swift`.

## Code (web)

`web/src/lib/dev-probe.ts` (the switch's key, the WebSocket wrap), `web/src/lib/dev-mode.ts` (recorder, store, hooks),
`web/src/lib/dev-scrub.ts`, `web/src/lib/dev-package.ts`, `web/src/views/settings/DevModeSettings.tsx` (About Persona's
rows), `web/src/views/settings/DevExportDialog.tsx` (export, Send To, Download). Tests: `web/src/lib/dev-mode.test.ts`,
`web/src/views/settings/DevModeSettings.test.tsx`.
