# HarmonyOS device and integration validation

Use an isolated Hub, synthetic tasks and dedicated Agent profiles. A passed portable test, an SDK probe and a screenshot establish different facts; retain the command output and exact HAP hash for each run. Never use a personal workspace's messages, credentials or external recipients as fixtures.

## Device matrix

|Category|Required cases|Layout decision|
|---|---|---|
|Straight phone|Root list, chat, decision, file preview, keyboard, 200% text, background/foreground|Use the actual native compact window class|
|Mate X-style conventional foldable|Cover, expanded portrait, expanded landscape, half fold, rotation with an unsent draft|An expanded MD window remains a native stack|
|Pura X Max-style wide foldable|Native split, drag the master width, collapse/restore, select/filter/scroll the current Agent, child navigation|LG/XL use native Navigation split; title follows whether the selected row identifies the detail|

Record measured pixels, density and window class with the native captures attached to the PR. Keep development images, UI trees and capture indexes outside the checkout, as required by the documentation media policy. An emulator lacking a valid crease region cannot establish hover or hinge avoidance. Verify that capability separately on an image or device that supplies real geometry.

Across the matrix, inspect Glass/Brief/Board, light/dark, English/Chinese/Japanese, system font scale and the keyboard. Reopen the application after an SDK runner exits before collecting UI evidence. The runner may close the main Ability. Restore the saved main HAP after building a test HAP if the build replaced its default output artifact.

For assistant questions, cover ordinary and secondary Persona chats, multiple pending cards, ordinary withdrawal after a message, and `wait:false` retention. Mix a question with permission/plan requests in both orders and return two answer receipts out of order. Keep an unsent draft and the native IME active as cards update. Inspect both 2× and 3.2× text scales.

For Apps, install the checked-in native extension fixture on the isolated Hub. Open its navigation pages from the root menu, then return through the native destination stack. Cover a directory longer than eight entries, badge updates, an empty directory, old-Hub feature absence, failure/Retry, disable/uninstall, disconnection and an owner change. A previously visible page must not retain authority after invalidation.

For computer transfer, use two owned nodes under the same manager, separate synthetic source and receive directories, and switches initially off. Start the transfer from the HAP and verify the received file against the source bytes. Test cancel, source revision change, a switch disabled during transfer, manager restart/404, response loss during enable and disable, accepted creation followed by failed refresh, and creation response loss without an ID. Confirm that no unknown operation automatically creates a replacement. Close and reopen the sheet while disabling either switch; a retained second view must not recover an old enabled value before the write settles. Deliver a terminal event before a delayed 404 and confirm the terminal result remains visible. Reopen an active task and confirm its target and receive folder remain identifiable. Retain native screenshots and UI trees on all three device categories; a service-side or fake-node test alone does not establish this journey.

## Environments to prepare

These integrations need their own environment before their end-to-end behavior can be accepted. Their unavailability is not a passing result and does not justify omitting a client implementation.

|Environment|Preparation|Acceptance observation|
|Other Agent runtimes|A supported CLI runtime on an owned trusted computer, configured launcher/model, disposable repository|Create a session, stream a response, answer its question/permission/plan, stop, disconnect and recover without replay|
|Voice extension|A configured compatible voice service and V1/V2 endpoint, microphone permission and audio output|Negotiate the implemented version and target; record only after ready; interrupt/background/reconnect without changing the admitted run or silently resuming capture|
|External Inbox channels|Dedicated test connector/account and explicit synthetic recipient/thread, including attachments|Read acknowledgement covers visible content; confirmed reply sends once; failure preserves the draft; child updates refresh the parent|
|Mac screen sharing|An owned Mac with screen-sharing permission/TCC and a supported Hub/node screen route|Read-only viewing, explicit control, coordinate mapping, disconnect/revoke releases all input|
|System photo editor|A device/image with a registered `photoEditor` handler and a synthetic image|Read-only URI grant, original unchanged, returned new copy staged only in the captured conversation|
|Camera and sensors|Physical camera/location/sensor hardware and permission combinations|Grant, deny, revoke, background and cancel; no action continues after its target or permission retires|
|Fold geometry and accessibility|A real conventional/wide foldable with crease geometry, screen reader and system input methods|Native hover keeps actions away from the hinge; draft/focus/selection survive; all actions are reachable and spoken correctly|
|Background notifications|Device-qualified notification capability with grant/deny, lock screen and process termination|Open the captured route and fetch current facts; expired requests never execute from stale notification content|
|Release performance|An optimized signed build on a named physical phone and foldable|Record the measurements below; emulator speed is not a physical-device performance result|

Local ChatGPT-authenticated Codex, native storage/transport/SSH, recording and Mandarin on-device recognition, and native contact/calendar writes can be tested independently of those external services. Use the [client guide](harmony.md#verification) and checked-in fixtures. Contact/calendar mutations must use owned temporary entries and remove only the IDs created by the run.

## Physical performance procedure

Record device, OS/API, build mode and hash, refresh rate, temperature, fixture size and profiler version. Use 200 sessions and 10,000 messages for the retained-list/transcript workload. Run at least 30 measured samples after a separate warm-up; report the distribution and p95, not only a best run.

- Cold local interactivity: process stopped, saved local state present; target p95 at most 2 seconds. Record network readiness separately.
- Warm local interactivity: background then foreground; target p95 at most 1 second.
- Local send echo: input submission to the optimistic native message row; target p95 at most 100 ms. Record server first-token time separately.
- Scroll/typing: collect App, RenderService and GPU frame timings. At 60 Hz target p95 at most 16.7 ms and fewer than 5% of frames exceeding two frame intervals; use 8.3 ms for a 120 Hz target.
- Repeat split drag, fold/rotation, large-text IME and a streaming update while an input is focused. Retain memory and dropped-frame evidence alongside screenshots.

These are qualification targets. Do not present them as measured results without the recorded run. Store signing identity and service credentials outside the repository. AppGallery upload, submission and optional system-service entitlements require a separate release workflow.
