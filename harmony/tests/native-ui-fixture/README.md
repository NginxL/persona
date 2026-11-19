# Native production-HAP UI fixture

This small loopback TLS backend supplies synthetic data for manual UI regression checks against a production Harmony HAP. It is **not an actual Codex session, a production Hub, or real user data**. It makes no external service calls. The transport framing and certificate-creation helper come from `../native-transport-fixture.py`; this fixture keeps its own TLS key so a restart with the same runtime directory preserves the pairing pin.

Run it from the repository with a private runtime directory outside the checkout:

```sh
fixture_runtime="$(mktemp -d)"
chmod 700 "$fixture_runtime"
python3 harmony/tests/native-ui-fixture.py --runtime "$fixture_runtime" --scenario normal
```

Keep the fixture running in this host terminal. In another terminal, connect the device's loopback port to the host fixture port with HDC reverse forwarding:

```sh
hdc rport tcp:38663 tcp:38663
hdc fport ls
```

If more than one device is connected, add `-t '<serial>'` to both commands. On the device, open the app's Hub pairing screen and paste the `pair_url` value from `$fixture_runtime/descriptor.json`. Pairing through the UI is important: the URI contains `&` separators, which a shell treats as command separators if the value is pasted into an unquoted CLI start argument.

The descriptor includes the synthetic pairing token and certificate pin. Keep the runtime directory private and do not commit or share it; it contains the TLS key, pairing descriptor, state, and request journal. A controlled restart can reuse the same directory and pairing pin. After finishing, stop the fixture with Ctrl+C, remove the reverse forwarding task, and delete the runtime directory:

```sh
hdc fport rm 'tcp:38663 tcp:38663'
hdc fport ls
rm -rf "$fixture_runtime"
```

If more than one device is connected, use `hdc -t '<serial>' fport rm 'tcp:38663 tcp:38663'`. The `fport ls` output should no longer include the fixture port.

Scenarios are `normal` (no active question), `question` (one open `ask_user` question), `pending` (chat sends return HTTP 202 pending), `failed` (retry of the failed synthetic receipt returns a fixture error), and `held` (Agent messages held while Test computer is offline). Every scenario includes a synthetic open plan, one pending Inbox review item, and done, stopped, failed, and interrupted receipt examples. The interrupted receipt starts with `resolution: pending`; a successful retry changes that same receipt to `resolution: resumed`, so an unchanged cached session cannot leave its old Continue action enabled. A failed retry keeps it pending. Opening the Inbox item and confirming its handoff marks only that synthetic item done and returns the synthetic main session; it does not start a real agent or contact any service.

The plan card's start request accepts the UI action but returns the canned plan. Edited task fields in that request are deliberately ignored. Use this fixture to check the plan editor UI and that confirmation sends one POST; it does not verify that edits persist or start work.

The fixture creates `control.json`, `state.json`, and `requests.jsonl` in the private runtime directory. It rereads `control.json` for each request; set `scenario` to one of the listed names and use `receipts` to resolve a pending operation by its id from `requests.jsonl`, for example:

```json
{"scenario":"pending","receipts":{"OPERATION_ID":"done"}}
```

The journal records request counts, route names, status codes, and operation ids. It never writes request bodies. The fixture replays a stored response for a repeated operation id to make UI checks deterministic; this local behavior does not establish exactly-once guarantees for a real Hub.

Set `"question_case": "long"` in `control.json` with scenario `question` for three questions, long options and a final free-text field. The fixture uses the Hub's `can_cancel: true` shape without a top-level prompt or a fabricated revision. `question_resolution_kinds` in state records only the `answer` or `cancel` enum; request bodies and answers are not logged. Local Later or closing the native sheet must leave that count unchanged.

For synthetic conversation-reading checks, set `"reading_case": true` to return 20 fixed messages with stable IDs and sequence numbers 40–59. Even sequence numbers are user rows and odd numbers are assistant rows, forming separate synthetic turns in the native projection. Set `"reading_prepend": true` to add 20–39, or `"reading_append": true` to add 60–64; those flags only apply while `reading_case` is true. The generated text contains no personal data, and every phase uses the same synthetic history ID. `after_seq` responses use the requested cursor as `from_seq`, including empty responses, and keep `to_seq` at least as high as the cursor. `before_seq` responses use the boundaries of the returned older rows. These controls do not emulate real history paging or a real Hub's transcript behavior.

Set `"agent_working": true` or `false` to override only the synthetic `LEAD_SESSION` state in the sessions and assistant responses. Omitting the key preserves the existing recovery-driven state. All four controls accept JSON booleans only; strings and numbers are ignored. These rows and messages are local synthetic UI fixtures, not real agent or transcript data.

### Held Agent messages on a phone or foldable

Start the fixture with `--scenario held` and a fresh runtime directory. Pair two app windows or devices with the same fixture and manager. Open **Agents → Field notes** in both. The computer stays offline while the app stays connected.

1. Find *Pack the field notes.* with the waiting line and **Cancel**. The expired *File the receipts.* and uncertain *Send the revised map.* each show their distinct full manager line, **Retry**, and **Cancel**. The signed-out *Seal the envelope.* offers only **Cancel**.
2. Send a new sentence in the first window. Each window should show the sentence once with the waiting line, including after reopening the chat. The fixture journal should contain one message POST with status 202.
3. Tap **Retry** on the uncertain row in the second window. Both windows should show *Will check the chat when Test computer is back.* with **Cancel**. The journal should show one POST to the held item's retry route. In the first window, tap the row's **Cancel** to open the deletion alert and choose the alert's **Cancel**. Both copies should remain and the journal should have no DELETE. Reopen the alert and choose **Delete**. Both copies should disappear and the journal should have one DELETE.
4. Open **Stopped Agent** and **Read-only Agent**. Check their distinct footer text and the absence of a send or held-message action.
5. Repeat the action checks in a narrow portrait window and on the expanded foldable. Inspect the longest status line and both touch targets at enlarged font size. The reading pane supplies the same controls in both layouts. Capture a screenshot and an ArkUI Inspector tree of each layout when running on a device or emulator.

The Inspector IDs are `chat.held.status.<id>`, `chat.held.retry.<id>`, and `chat.held.cancel.<id>`. While a retry awaits the manager's updated session, `chat.held.update.<id>` appears separately, the manager's previous reason stays visible, and another Retry is disabled. The four seeded IDs are `waiting`, `expired`, `uncertain`, and `signed-out`.

With a synthetic failed row scrolled into view, collect the current device output. Use separate filenames for the compact and expanded windows.

```sh
hdc shell uitest dumpLayout -p /data/local/tmp/held-compact.json
hdc shell uitest screenCap -p /data/local/tmp/held-compact.png
hdc file recv /data/local/tmp/held-compact.json ./held-compact.json
hdc file recv /data/local/tmp/held-compact.png ./held-compact.png
```

Look for the status text and action IDs together in the tree. Record the tested HAP hash, OS/API, actual window class, viewport, density and system font scale with each screenshot. Exercise both actions through the running app on a compact phone and an expanded foldable. The standalone SDK Previewer can measure the shared status component. Full-app navigation, enlarged system font, touch delivery and crease geometry need the corresponding emulator or device. [Device validation matrix](../../../docs/harmony/device-validation.md) · [Huawei UI testing commands](https://developer.huawei.com/consumer/en/doc/harmonyos-guides/uitest-guidelines/)
