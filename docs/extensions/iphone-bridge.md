# iPhone bridge (`persona-iphone-bridge`)

The assistant operates a paired iPhone the way it operates a Mac ([computer-use.md](computer-use.md)): a node on one of the user's
computers reaches the phone over the network and hands the screen and the actions to the assistant. Nothing is installed on the phone,
and no VPN or loopback tunnel runs on it. The capability is called `iphone` on the wire (`phone` already names the phone data tools).

This document is the **wire contract** of the native bridge that does the phone side on the node: what the node sends it and what it
answers. It describes primitives only; numbering elements, formatting for the model and approvals belong to the node's Go package that
calls it. Pairing and the node's session (records, finding the phone, keeping `serve` running, reconnecting) are #3448.

Status: v1. Checked on real iPhones only as far as "Verification status" says.

**Supported phones: iOS 27 and later** (a product decision). Older iOS refuses remote input, so there the bridge can only look at the
screen and start and end apps.

## What it is

- A single Rust binary, `persona-iphone-bridge`, in `native/iphone-bridge/`, built on the open-source
  [idevice](https://github.com/jkcoxson/idevice) library (MIT).
- It connects with **RemotePairing** (iOS 17 and later): a saved pairing record plus the phone's address on the LAN or on Tailscale. It
  verifies the record, opens the encrypted tunnel the phone offers, runs a **userspace** TCP stack over it, reads the service directory
  (RSD) and talks to the developer services listed there. It never changes the computer's routes, DNS or interfaces: no `utun`, no root.
- A **simulator backend** for development and testing: the same requests against an iOS simulator on this computer (see "Simulator
  backend"), so everything above the bridge can run end to end without a phone.
- Two ways to run it with the same requests and answers: **`call`** (one process per request) and **`serve`** (one process per phone,
  keeping the tunnel open). Every request has a total deadline.
- It is optional for the node, like Cua Driver for a Mac: not part of personad's build or CI. The node looks for it at
  `$PERSONA_IPHONE_BRIDGE`, then in `PATH`, then in `$PERSONA_HOME/bin` (`~/.persona/bin/`), then next to `personad`; without it the
  capability reports "not installed". Releases ship it, and the app can install it on a computer ("Releases").
- Build: `native/iphone-bridge/build.sh` (format, lint, tests, release build; `--sim-runner` adds the simulator test runner; `--install`
  copies them to `~/.persona/bin/`). Needs a Rust toolchain.

## Invocation

```
persona-iphone-bridge version                             # "persona-iphone-bridge 0.1.0 (wire 1)"; also --version
persona-iphone-bridge call                                # one request on stdin, one answer on stdout
persona-iphone-bridge serve --udid <udid> --record <path> [--host <address>] [--port <port>] [--timeout-ms <ms>]
persona-iphone-bridge usb-list --json                     # phones attached over USB (#3448)
persona-iphone-bridge pair --udid <udid> --out <path>     # pair over trusted USB, write the record (#3448)
persona-iphone-bridge match --record <path> --identifier <id> --auth-tag <tag>   # is this Bonjour advertisement the record's phone? (#3448)
persona-iphone-bridge reveal-developer-mode --udid <udid>   # show Developer Mode in the phone's Settings (#8199)
```

### One-shot commands (`call`, `usb-list`, `pair`, `match`, `reveal-developer-mode`, `version`)

| Outcome | stdout | stderr | Exit code |
| --- | --- | --- | --- |
| Success | one line: the answer | free-form diagnostics, may be empty | 0 |
| Failure | empty | last line: the error as JSON (`"ok": false`); diagnostics before it | 1 |

- The node reads only the **last line** of stderr as the error; anything before it is for logs.
- `call` reads one line (at most 1 MiB; the newline may be missing) and ignores anything after it.
- Connecting counts against the request's `timeout_ms`.

### `serve`

One process per phone. `--record` is the pairing record, `--host` the phone's address (required for a phone, not given for a simulator
record: an IP literal, an IPv6 literal with `%zone` such
as a Bonjour link-local address, or a host name), `--port` its RemotePairing port (default 49152, not verified on a real device: pass the
port Bonjour reports), `--timeout-ms` the deadline for connecting (default 30000).

- It connects, then writes `{"event":"ready","device":{…the device_info result…}}`. A failure before that is a one-shot failure (stdout
  empty, error on stderr's last line, exit 1).
- Then it reads requests from stdin, one per line, and writes one answer line per request on **stdout** (`"ok": true`, or `"ok": false`
  with `error`), with the request's `id`, which is required here. `device` in a request is ignored. Requests run one at a time, in order;
  blank lines are skipped.
- It acts only on **newline-terminated** lines: a last line cut off by the end of stdin is dropped as never sent. The node closes stdin
  when a write fails or its deadline passes halfway, and may send that request again on a new connection.
- When an operation fails because the link dropped (`disconnected`), or runs past its `timeout_ms` (`timeout`), the answer is followed by
  `{"event":"disconnected","reason":"…"}` and the process exits with code 1: an abandoned operation leaves the connection in an unknown
  state. It never reconnects by itself.
- EOF on stdin ends it with exit code 0, so a node that restarts itself (closing the pipe) also ends it.
- Read-only operations, safe to retry on a new connection: `device_info`, `screenshot`, `ax_tree`, `list_apps`. Every other operation
  changes the phone; its outcome is unknown when its answer did not arrive, and it must not be replayed.
- Input is answered once every event has been written into the tunnel. The phone does not confirm input events, so `ok` means "sent",
  not "seen on screen"; take a screenshot to check.

### Privacy

Neither stream carries the pairing record's keys or typed text; stderr never carries screenshot bytes. Requests go through stdin, not the
argument list, so they never show in the process list. Screenshots written with `out_file` are readable by this user only.

## Request

```json
{
  "v": 1,
  "id": "c-42",
  "op": "tap",
  "device": {
    "udid": "<udid>",
    "address": "<phone address>",
    "port": 49152,
    "pairing_file": "<PERSONA_HOME>/iphone/<udid>/pairing.record"
  },
  "timeout_ms": 30000,
  "args": { "x": 120, "y": 640 }
}
```

| Field | Type | Meaning |
| --- | --- | --- |
| `v` | int | Wire version, `1`. Required in `call`, optional in `serve`; any other value fails with `bad_request`. |
| `id` | string | Echoed back in the answer and the error. Optional in `call`, required in `serve`. |
| `op` | string | One of the operations below. |
| `device` | object | `call` only; `serve` takes the device from its flags. |
| `device.udid` | string | The phone's UDID; checked against what the phone reports, a mismatch fails with `wrong_device`. |
| `device.address` | string | As `serve --host`; left out for a simulator record. |
| `device.port` | int, optional | RemotePairing port. Default 49152 (not verified on a real device). |
| `device.pairing_file` | string | The pairing record on this computer. The bridge only reads it. |
| `timeout_ms` | int, optional | Deadline for the request (in `call`, connecting included). Default 30000, clamped to 1000–120000; `mount_ddi` defaults to 120000, clamped to 1000–300000. |
| `args` | object, optional | The operation's arguments; omitted means `{}`. |

Unknown fields are ignored, so a newer node can talk to an older bridge within the same `v`. Arguments are checked before anything is
sent to the phone: an invalid one never leaves half a gesture.

## Answer

```json
{ "v": 1, "id": "c-42", "ok": true, "op": "tap", "elapsed_ms": 412, "result": {} }
```

`result` is the operation's result below. Keys may come in any order.

## Error

```json
{ "v": 1, "id": "c-42", "ok": false, "op": "tap", "error": { "code": "unreachable", "message": "connect to <phone address>:49152: …" } }
```

`id` and `op` are present when they could be read from the request. `message` is English, for logs and the assistant; the node maps
`code` to user-facing text.

| Code | When |
| --- | --- |
| `bad_request` | Unparseable line, unknown `v` or `op`, missing or invalid arguments, a point outside the screen |
| `pairing_missing` | The pairing record is absent, unreadable or not one |
| `pairing_rejected` | The phone refused the record (removed from its trusted computers, or reset): pair again |
| `unreachable` | No TCP connection to the phone |
| `wrong_device` | The phone at that address is not `udid` |
| `tunnel_failed` | Connected, but the encrypted tunnel or the service directory failed |
| `developer_mode_off` | The operation needs Developer Mode, which is off |
| `ddi_not_mounted` | The phone does not list the service the operation needs; usually the developer disk image is not mounted |
| `service_unavailable` | A service is listed but could not be started; `message` says which |
| `locked` | The operation needs the phone unlocked |
| `app_not_found` | No app with that bundle ID |
| `unsupported` | Not possible (yet): see the operation |
| `disconnected` | `serve`: the link dropped during this operation; whether an input operation reached the phone is unknown |
| `timeout` | `timeout_ms` passed |
| `trust_pending`, `not_found` | `pair` only: tap Trust on the phone and retry; no such phone on USB |
| `ddi_missing` | `mount_ddi`: no developer disk image in the directory (Xcode brings one; or pass `ddi_dir`) |
| `ddi_unsupported_device` | `mount_ddi`: the directory has neither a cryptex build this phone takes (iOS 27 and later) nor a build for its board and chip; a newer Xcode may |
| `tss_failed` | `mount_ddi`: Apple's signing server could not be reached or gave no ticket |
| `mount_failed` | `mount_ddi`: the phone refused the image or the cryptex, or accepted it without listing it as mounted |
| `internal` | Anything else, including a panic in the bridge or a library it uses: every command catches it and fails as above, exit code 1, `message` saying what panicked and where (`the bridge failed unexpectedly: … (idevice-0.1.68/src/…:518)`), never exit code 101 with a backtrace |

## Coordinates

All positions are in **points** in the current interface orientation, origin at the top-left corner of what the user sees, `x` to the
right and `y` down; fractions are allowed. `screenshot` and `device_info` report `point_width` / `point_height`: a position read off a
screenshot `width` pixels wide maps to points as `x_pt = x_px × point_width / width`.

The bridge turns a point into the touchscreen's own coordinates (the panel as built, in portrait), rotating for landscape and upside down.
It takes a capture to be the panel as built too, and turns it to match the interface: a quarter turn when a portrait-shaped capture comes
with a landscape interface, a half turn for upside down. Both rotations are not verified on a real device.

## Operations

### `device_info`

No arguments. What the phone is and what works on it right now, read from the phone each time: the node's status probe and `serve`'s
ready event.

```json
{
  "udid": "<udid>", "name": "<device name>", "product_type": "iPhone16,2", "os_version": "18.1", "build": "22B83",
  "point_width": 430, "point_height": 932, "scale": 3, "orientation": "portrait",
  "locked": null, "developer_mode": true, "ddi_mounted": true,
  "battery": { "level": 0.81, "charging": false },
  "simulated": false, "transport": "network",
  "capabilities": { "screenshot": true, "ax_tree": false, "input": true, "apps": true },
  "notes": { "ax_tree": "unsupported" }
}
```

- `simulated` / `transport`: `true` / `"simulator"` for the simulator backend, `false` / `"network"` for a phone.

- `orientation`: `portrait` | `portrait_upside_down` | `landscape_left` | `landscape_right` (Home button or indicator on that side).
- Any field the phone did not answer is `null`. `locked` is always `null` in v1: no service found yet says whether the screen is locked.
- `capabilities`: whether the phone lists the services each group needs (`screenshot`; `input` for touches, keys and buttons; `apps`),
  and `ax_tree`, always `false` in v1. `input` is also `false` below **iOS 27**, read from the phone's version (never by trying):
  older iOS has no remote input. For each `false`, `notes` gives the reason with an error code: `developer_mode_off`,
  `ddi_not_mounted`, `unsupported` (for `input`: the iOS version). A listed service can still fail when used; the operation's error says so.
- `ddi_mounted`: whether the developer services are listed at all; `null` when Developer Mode is off, since then nothing is listed either way.
- The developer services come with the disk image, and a session reads the service directory when it connects. While they aren't listed,
  `device_info` reads the directory again (at most every 5 seconds), so an image mounted after the session started, by Xcode or by another
  session's `mount_ddi`, can show up in this one too. A cryptex install resets the phone's service directory connections (on iOS 27.2.1,
  Xcode's was torn down when this bridge installed the image). So when the
  directory can't be read again, or still doesn't list the services although the phone has the image (cryptexd or the image mounter says
  so), the session can't reach them: `device_info` answers `disconnected`, the node connects again, and the new session lists them
  (#12171). Before that, a long-running `serve` kept saying `ddi_not_mounted` until it was restarted.

### `screenshot`

The bridge always asks the phone for PNG (iOS 18.7.3 refuses "jpeg", error 4864) and converts to JPEG itself when asked: scaled down
first, then encoded, tens of milliseconds for a full screen on this computer. `timing_ms` says where the time went: `capture` is the
phone producing the image, `convert` the bridge's part, `stream_open` whether the input display stream was running then. On iOS 27.0
the conversion stays under 40 ms; the capture takes about 0.7 s when no display stream ran recently, and 3–6 s within a few seconds of
one (see "Open questions").

| Arg | Default | Meaning |
| --- | --- | --- |
| `format` | `jpeg` | `jpeg` or `png` |
| `quality` | 85 | JPEG quality 1–100 |
| `max_dimension` | 0 | Scale down so the long edge is at most this many pixels; 0 keeps the native size |
| `out_file` | — | Write the image to this path (0600) instead of returning it; `data` is then left out |

```json
{ "mime": "image/jpeg", "data": "<base64>", "width": 1290, "height": 2796, "point_width": 430, "point_height": 932, "orientation": "portrait",
  "timing_ms": { "capture": 850, "convert": 30, "stream_open": false } }
```

### `ax_tree`

**On a phone, `unsupported` in v1** (the simulator backend has it, see "Simulator backend"). The library this is built on has no client for the phone's accessibility services, and an inspector protocol
written blind without a phone to test on would most likely not work. The assistant reads screenshots instead. The answer shape below is
reserved so the node can be written against it; see "Open questions".

| Arg | Default | Meaning |
| --- | --- | --- |
| `max_depth` | 0 | 0 = unlimited |
| `max_nodes` | 2000 | Stop after this many nodes; `truncated: true` |
| `include_offscreen` | false | Also elements whose frame is outside the screen |

```json
{
  "app": { "bundle_id": "com.apple.Preferences", "name": "Settings", "pid": 412 },
  "orientation": "portrait", "point_width": 430, "point_height": 932, "truncated": false,
  "root": {
    "id": "e1", "role": "application", "label": "Settings", "frame": [0, 0, 430, 932],
    "children": [
      { "id": "e2", "role": "button", "label": "Wi-Fi", "value": "Home", "frame": [16, 210, 398, 52], "enabled": true, "children": [] }
    ]
  }
}
```

Node fields: `id` (unique within one answer only), `role` (lower camel case), `label`, `value`, `identifier`, `hint`, `frame`
(`[x, y, width, height]` in points), `enabled`, `selected`, `focused`, `children`; absent when empty. `app` is `null` on the home screen.

### Input: `tap`, `long_press`, `drag`, `swipe`, `keys`, `button`

**Needs iOS 27 or later.** On older iOS the phone refuses remote input outright (CoreDevice error 9021, "Remote control requires iOS
27.0 or later on this device", seen on iOS 18.7.3), so every input operation answers `unsupported` at once, saying so, without touching
the phone; `capabilities.input` is `false` there. On those phones the bridge can look (screenshots) and start apps, not touch.

The phone takes synthetic input only while a display stream to this computer is running. The bridge starts one before the first input
(about a third of a second), reads and drops its video, and in `serve` keeps it for 10 seconds after the last input so a series of taps
does not restart it. Results are `{}`.

**When the answer comes**: a touch is answered only after a request on the same connection as the touches came back, so the phone has
read them; keys and buttons, which go one way, get another 0.1 s. The display stream stops no sooner than 1 second after the last input
(also when a `call` ends, or `serve` reaches the end of its input), so the phone acts on it. Before this, a tap from a one-shot call
answered `{}` and did nothing on the phone: the stream had stopped and the process ended before the phone acted on it.

- `tap` `{ "x": 120, "y": 640, "count": 1 }`: `count` 1–3 (2 for a double tap).
- `long_press` `{ "x": 120, "y": 640, "duration_ms": 800 }`.
- `drag` `{ "from": {"x": 200, "y": 700}, "to": {"x": 200, "y": 200}, "duration_ms": 500, "hold_ms": 0 }`: `hold_ms` keeps the finger
  down at `from` before moving (drag and drop, moving icons).
- `swipe`: either `from` / `to` as in `drag`, or `{ "direction": "up", "origin": {"x": 215, "y": 466}, "distance": 400 }`:
  `direction` `up` | `down` | `left` | `right` is the way the finger moves; `origin` defaults to the screen center, `distance` (points) to
  half the screen along that axis. `duration_ms` defaults to 250.
- `keys`: exactly one of `{ "text": "hello" }` or `{ "key": "return", "repeat": 1 }`. Keys: `return`, `backspace`, `delete`, `tab`,
  `escape`, `space`, `up`, `down`, `left`, `right`. Text is typed as key presses on a US keyboard layout, so only printable ASCII, tab and
  newline can be typed; any other character (Chinese, accented letters, emoji) fails the whole request with `unsupported`, naming it,
  before anything is typed. At most 4,096 characters. These are **hardware keyboard events**: what they type depends on the input method
  the phone has selected. With a Chinese keyboard (for example the 9-key pinyin layout) `keys {"text": "wifi"}` leaves underlined
  composing text rather than plain letters (seen on iOS 27.0); a search field still used it there, but other fields may not. The bridge
  doesn't switch input methods (see "Open questions").
- `button` `{ "name": "home", "duration_ms": 0 }`: `home` | `lock` | `volume_up` | `volume_down`; `duration_ms` 0 is a short press,
  longer holds it (up to 10 s).

Durations are capped at 10 seconds.

### `launch_app`

`{ "bundle_id": "com.apple.Preferences", "args": [], "env": {}, "kill_existing": false }` → `{ "bundle_id": "…", "pid": 412 }`.

### `terminate_app`

`{ "bundle_id": "…" }` or `{ "pid": 412 }` → `{ "terminated": true, "pid": 412, "how": "kill" | "signal" }` (`how`: which request
ended it), or `{ "terminated": false }` when it was not running.
Through the instruments service, which needs the developer disk image: its process list finds the app's main executable (never an
extension inside the bundle, never a process that isn't an app), then `killPid:`, and if the process is still listed after about
1.5 seconds, `sendSignal:toPid:` with SIGKILL. `true` only once the phone no longer lists the process; if it survives both,
`unsupported` ("the phone accepted the request but process … is still running").

### `list_apps`

`{ "include_system": true }` → `{ "apps": [ { "bundle_id": "…", "name": "…", "version": "…", "type": "user" } ] }`: `type` `user` |
`system` (Apple's own). Through the installation service, which needs no developer disk image. (CoreDevice's own app and process
lists never answered on iOS 18.7.3, so the bridge doesn't use them.) The app operations give up after 20 seconds without an answer with
`service_unavailable`, leaving the session usable.

### `mount_ddi`

`{ "ddi_dir": "<folder>", "force": false }`, both optional. The developer services behind `screenshot`, input and the app operations are
listed only while the phone has the developer disk image mounted (`device_info`: `ddi_mounted`, and `ddi_not_mounted` in `notes`). This
mounts it, **only when asked**: no other operation mounts it on its own. The node offers it to the phone's owner, never to the assistant:
the command `personad iphone mount-ddi <udid> [--ddi-dir <dir>] [--force]` ([iphone-pairing.md](iphone-pairing.md)), and the app's
Prepare, after which the node mounts it again by itself whenever that phone comes back without it (#8199, [iphone-pairing.md](iphone-pairing.md), "Preparing the phone").

The image goes on the way Xcode puts it there (#12171):

- **iOS 27 and later, as a cryptex** through the phone's `cryptexd` (`com.apple.security.cryptexd.remote`), when the phone offers it:
  the manifest's **cryptex build**, the one build identity whose variant ends in `Developer Disk Image Cryptex`. It is the same for
  every phone, so a phone newer than the folder's per-device builds takes it too: Xcode 27.2's per-device builds stop at chip `0x8150`,
  and an iPhone19,4 (chip `0x8160`) failed with `ddi_unsupported_device` before this, while Xcode mounted the image on it.
- **Before iOS 27, as a personalized image** through the image mounter: the build for the phone's board and chip. An iOS 27 phone gets
  this too when the folder has no cryptex build (an older Xcode).

1. Already mounted (and not `force`): the image mounter lists a personalized image, or cryptexd lists the developer disk image cryptex
   (`com.apple.MobileAsset.DDI`, also when Xcode installed it): `{"mounted": true, "already": true}`. If this session doesn't list the
   developer services yet (the image came after it connected), the service directory is read again as in step 7; nothing else happens.
2. Developer Mode off: `developer_mode_off`.
3. The image comes from `ddi_dir`, else `$PERSONA_IPHONE_DDI_DIR`, else Xcode's `/Library/Developer/DeveloperDiskImages/iOS_DDI/Restore`.
   Nothing is downloaded. From its `BuildManifest.plist` the bridge picks the cryptex build and reads its four payloads
   (`Cryptex1,GenericDmg`, `Cryptex1,GenericTrustCache`, `Cryptex1,CryptexInfoPlist`, `Cryptex1,GenericVolume`), or the build for the
   phone's board and chip and the image and trust cache it names. Paths must stay inside the folder. None there: `ddi_missing`; nothing
   this phone takes: `ddi_unsupported_device`.
4. Cryptex only: a locked phone is `locked` before anything is sent, as Xcode checks too (lockdown's `PasswordProtected`, `true` while
   the phone wants its passcode). A locked iOS 27.2.1 phone never answered the install, and its cryptexd then answered no other request
   either until the phone was unlocked; every cryptexd lookup (installed cryptexes, identifiers, nonce) therefore gives up after 5 seconds
   with `locked`. A phone that locks itself during the install (Auto-Lock) leaves it waiting the same way, see "Open questions".
5. The ticket. Cryptex: always from Apple's signing server (TSS), for the cryptex build, this phone's chip ID and ECID
   (`Cryptex1,UDID`) and a nonce from cryptexd. Personalized image: the phone's own for this image if it has one, else one from Apple's
   signing server. A failure there is `tss_failed`, and nothing is uploaded.
6. Install (cryptexd's `install`, the five payloads with the ticket) or upload and mount, then check the phone lists it:
   `{"mounted": true, "already": false, "kind": "cryptex" | "personalized", "image": "<file>", "ticket": "phone" | "apple",
   "services_refreshed": true}`. A refusal is `mount_failed` (or `locked`, `developer_mode_off`). With `force` while the cryptex was
   installed (iOS 27.2.1), cryptexd refused it at once: `invalid asset: ginf`, so `mount_failed`.
7. The service directory is read again so this session sees the developer services. If they still aren't listed, the mount stands and
   is reported with `"services_refreshed": false`; the next request then answers `disconnected` and the node connects again.

**The signing request**: the library sends it to `http://gs.apple.com/TSS/controller?action=2`, **plain HTTP**: it hard-codes the
address and keeps the request body to itself, so the bridge cannot send it over HTTPS without changing the library. For a personalized
image the request carries the phone's board and chip IDs, its **ECID** (unique chip ID), the nonce the phone generated for this image,
and the hashes of the image and trust cache; for the cryptex, the cryptex build's identity, the phone's chip ID and ECID, cryptexd's
nonce, and the hashes of the cryptex's info, trust cache and volume. Either way the ECID crosses the network **in clear text**. The
bridge writes none of this to disk or to its output. Unlock the phone and keep it awake while mounting. Takes up to a couple of minutes
(the image is about 17 MB through the tunnel).

On the simulator backend, `unsupported`: a simulator has its developer services already.

## USB and Bonjour: `usb-list`, `pair`, `match`

In `native/iphone-bridge/src/pair.rs` (#3448). Pairing needs the phone on USB and already trusting this computer (the "Trust This
Computer?" prompt answered once); without that, `pair` answers `trust_pending` and nothing is written.

- `usb-list --json` → `[{"udid": "<udid>", "name": "<device name>", "product_version": "18.1", "model": "iPhone16,2"}]`: the phones
  usbmuxd sees over USB, each once. `name` is `null` until the phone trusts this computer (recent iOS gives its name only in a session).
- `pair --udid <udid> --out <path>` opens `remotepairingdeviced` through lockdown over usbmuxd and runs RemotePairing's manual pairing:
  the phone asks its user to allow it and for the passcode, then hands back the key its Bonjour advertisements are tagged with (`altIRK`).
  The record (keys, host name, that key) is written 0600, and the answer is `{"udid", "name", "model", "product_version",
  "developer_mode"}`. **Pairing works with Developer Mode off**, but the phone then neither advertises over Bonjour nor takes RemotePairing
  connections (seen on iOS 27.0: the handshake failed with `device socket io failed`). `developer_mode` says so up front (`null` when the
  phone didn't say); the node's `pair` command tells the user to turn it on (Settings › Privacy & Security › Developer Mode, then the
  phone restarts), and when a phone paired with it off can't be reached, the node answers `developer_mode_off` instead of `unreachable`.
  Error codes
  besides the common ones: `trust_pending`, `locked`, `not_found` (no such phone on USB), `pairing_rejected` (the user declined).
- The record is the bridge's own format (`record.rs`: the RemotePairing keys plus the host name they were made under, which every later
  connection must send again) and opaque to the node.
- `reveal-developer-mode --udid <udid>` → `{"udid", "revealed": true}` (#8199): iOS keeps the Developer Mode switch out of Settings ›
  Privacy & Security until a developer tool asks for it. This asks, through AMFI's lockdown service (`com.apple.amfi.lockdown`) over
  usbmuxd, so the phone has to be on the cable and trust this computer (else `not_found`, `trust_pending`). It turns nothing on: the
  phone's user turns it on, the phone restarts, and after the restart asks once more. The node runs it right after a pairing that
  reported Developer Mode off. A simulator answers `unsupported`.
- `match --record <path> --identifier <id> --auth-tag <tag>` → `{"match": true}` or `{"match": false}`: whether a `_remotepairing._tcp`
  advertisement (its TXT `identifier` and `authTag`) belongs to the record's phone. The tag is derived from a key inside the record, and the
  identifier is not a stable value to save at pairing, so the node asks the bridge for each advertisement and connects to the first match.

## Simulator backend (development and testing)

The same wire contract against an iOS simulator on this computer, through `xcrun simctl`. It is for developing and testing everything
above the bridge without a phone, and says so: `device_info` reports `"simulated": true`, `"transport": "simulator"`, and a `notes.simulated`
line.

- **Pairing**: `pair --udid <simulator udid>` (any simulator `simctl list devices` shows) writes a record marked as a simulator, 0600, with
  no USB or trust, and prints `{"udid", "name", "model", "product_version", "transport": "simulator"}`. A udid that is not a simulator here
  goes to the USB pairing above.
- **Connecting**: `serve --udid <udid> --record <path>` without `--host`, or `call` without `device.address`. The simulator must already be
  booted (`scripts/app/sim-boot.sh`): the bridge never boots, shuts down or erases one. Not booted: `unreachable`. Gone from this computer:
  `pairing_rejected`. A record for another simulator: `wrong_device`.
- **Operations**: `device_info` (model, iOS version and screen from the simulator's environment), `screenshot` (`simctl io screenshot`),
  `list_apps` (`simctl listapps`), `launch_app` (`simctl launch`; `env` is passed through as `SIMCTL_CHILD_…`), `terminate_app`
  (`simctl terminate`; by pid only for an app process of that simulator, found with its `launchctl list`, never any other process on the
  computer).
- **Accessibility tree and input** go through a test runner (`native/iphone-bridge/sim-runner/`), a UI-test bundle using only public
  XCUIAutomation API, run inside the simulator with `xcodebuild test-without-building`. No window opens on the computer and no macOS
  Accessibility permission is needed. Build it with `native/iphone-bridge/build.sh --sim-runner` (needs Xcode and XcodeGen; simulator only,
  no signing or team); it lands next to the binary as `persona-iphone-sim-runner/` (`--install` copies both), or set
  `PERSONA_IPHONE_SIM_RUNNER` to its folder. Without it, `capabilities.ax_tree` / `input` are `false` and those operations answer
  `unsupported`, naming the build command.
- **The runner's lifetime**: `serve` starts it right away, `call` on first use; it takes about 10–25 seconds to start, and an operation
  waits for it (in `serve` up to 15 seconds, then `service_unavailable`: try again). The bridge picks a free port, the runner listens only
  on `127.0.0.1` there, and the bridge passes it a random token: the runner answers only the one connection that sends it first. It ends
  when the bridge disconnects or exits (even if killed) or says `quit`; the bridge also kills `xcodebuild` if it lingers. Nothing keeps
  running after the bridge. A runner that dies mid-session is started again by the next request.
- **What the runner does**: `ax_tree` is the foreground app's element snapshot, plus any system alert (a permission prompt, which takes
  every touch and the Home button until answered); bare unlabelled containers and elements with no size are folded away. The foreground
  app is the first of the simulator's running, user-visible apps (most recent first) that reports itself in front, else the home screen;
  about 0.3 seconds. Touches are XCUICoordinate taps, presses and drags at screen points; `keys {text}` types any text, Chinese and emoji
  included; `keys {key}` presses the named key and needs a field with keyboard focus (`bad_request` otherwise); `button home` goes to the
  home screen (falling back to bringing SpringBoard forward when a runtime ignores the synthesized press). `lock` and the volume buttons
  answer `unsupported`: a simulator has none. Orientation comes from the runner in `ax_tree`; `screenshot` still reports `portrait`, or
  `landscape_right` when a capture is wider than tall.

### The simulator is not the phone

Results from the simulator backend say nothing about a real iPhone. The differences that matter:

| | Simulator backend | Phone |
| --- | --- | --- |
| Accessibility tree | yes, through the test runner | `unsupported` in v1 |
| `keys {text}` | any text | printable ASCII, tab and newline only |
| Input path | XCUIAutomation inside the simulator | HID events over the tunnel while a display stream is held open: only iOS 27 or later; `unsupported` below |
| `button` | `home` only | `home`, `lock`, `volume_up`, `volume_down` |
| Connection | none: local `simctl` and a loopback runner | RemotePairing, encrypted tunnel, service directory; not verified on a real device |
| Developer Mode, disk image, lock state | not applicable (`null`) | read from the phone |

### Try it

On a computer with Xcode, XcodeGen and a Rust toolchain:

```sh
native/iphone-bridge/build.sh --sim-runner
B=native/iphone-bridge/target/release/persona-iphone-bridge
SIM=$(scripts/app/sim-boot.sh "Persona iPhone Bridge <issue>")      # your own simulator
$B pair --udid "$SIM" --out /tmp/sim.record
$B serve --udid "$SIM" --record /tmp/sim.record                     # waits on stdin; type one request per line:
{"id":"1","op":"screenshot","args":{"out_file":"/tmp/sim.jpg"}}
{"id":"2","op":"launch_app","args":{"bundle_id":"com.apple.Preferences"}}
{"id":"3","op":"ax_tree"}
{"id":"4","op":"tap","args":{"x":201,"y":822}}                       # the center of an element's frame from ax_tree
{"id":"5","op":"keys","args":{"text":"Wi-Fi"}}
{"id":"6","op":"button","args":{"name":"home"}}
```

End with Ctrl-D: `serve` stops the runner and exits 0.

## Releases

How the bridge reaches the computers that need it (#8199):

- **Built with each release.** `scripts/release/publish.sh` runs `native/iphone-bridge/release.sh`, which builds
  `persona-iphone-bridge-<os>-<arch>` for linux/amd64, linux/arm64, darwin/arm64 and darwin/amd64. macOS builds need the
  Apple Rust targets on a Mac. Linux builds need `zig` and `cargo-zigbuild`, and target glibc 2.28. A platform the publishing
  computer can't build is left out with a `note:`, and so is a build that fails: the bridge never holds up a release. `--remap-path-prefix` cuts this
  computer's home and checkout out of panic locations, so the binaries pass `scripts/release/check_bundle_privacy.py`. That
  scan knows aws-lc's two upstream credits (`by <…@openssl.org>`, `by <…@arm.com>`) in that exact context. The darwin builds are
  signed ad hoc as `com.nwverse.persona-iphone-bridge`, like the other programs.
- **Listed in the release.** Each archive is in `SHA256SUMS`, in `latest.json` under `helpers` (`update.Manifest.Helpers`:
  name → platform → `{name, size, sha256, binary_sha256}`), and kept with the node builds under `dl/<version>/`.
  Only builds that are there are listed, and `publish.sh` uploads every one it lists. `personad release verify` and
  `scripts/operations/tag_release.py` check a helper's bytes, but not a build number: it has its own version. On the
  publishing computer's own platform, `version` must say `persona-iphone-bridge …`. A GitHub release whose private upload
  command doesn't carry a listed helper's file is not refused: `tag_release.py` warns and uploads a copy of `latest.json`
  without it, so hubs on that source find no build of the helper.
- **With Persona Link.** `install.sh` puts it at `~/.persona/bin/persona-iphone-bridge` when it installs a computer's
  node service and the release has a build for that platform. A failed or missing download is only a warning.
  `scripts/app/mac-link.sh install` builds it for that Mac (when Rust is there) and puts it in the same place. It leaves the
  app bundle, and Persona Screen and Persona Input in it, untouched.
- **From the app.** `POST /api/v1/machines/{mid}/iphone/install` (the computer turned on) has the manager find the build
  for that computer's OS and architecture (`devices.Devices.HelperDownload`). It looks first in the release of the hub's
  version from the hub's node packages source (`docs/operations/node-update.md` "Node packages"), then in the newest
  release, as node builds are found. It then calls the node's `iphone.install` `{urls, size, sha256, binary_sha256}`.
  The node downloads it itself (from the next address when one fails, so `node_packages.proxy` does not apply). It checks
  the size and both hashes, checks that the binary runs and names itself, and only then renames it into
  `<PERSONA_HOME>/bin`, so a bridge already there stays until the new one passes. Its progress is the iPhone status's
  job (`kind: install`: steps download, verify, install; codes `download_failed`, `checksum_mismatch`, `bad_helper`,
  `install_failed`, `timeout`). With no build for that platform, or no release source (a development hub), the route says
  so without asking the node.

## Verification status

Most of this was written without an iPhone. Every unit test runs against stand-ins: the request / answer / error framing, argument
checks, `call` and `serve` (with a fake phone), coordinate mapping, key tables, screenshot conversion, the record file. What talks to a
phone has run against one only where the table says so:

| Part | Status |
| --- | --- |
| RemotePairing verify, tunnel, userspace TCP, service directory | checked on a real iPhone (iPhone13,4, iOS 18.7.3) over its USB network interface |
| Default port 49152 | the port both iPhones advertised over Bonjour |
| Reaching the phone over Tailscale or Wi-Fi without the cable | not verified on a real device |
| `device_info` fields (lockdown keys for screen size, Developer Mode, battery) | checked on that iPhone: build, battery, 428 × 926 points, scale 3, Developer Mode |
| `screenshot` | checked on that iPhone: PNG about 1 s; JPEG (the phone refuses it, so the bridge converts) about 4 s in `call`. Landscape rotation not verified |
| `launch_app` | checked on that iPhone |
| `list_apps` | checked on that iPhone through the installation service: 121 user apps, 329 in all, under a second (CoreDevice's list never answered there) |
| `terminate_app` | checked on that iPhone: Settings (pid 12411) ended in 3 s, a second call answered `terminated: false`, the screenshot showed the home screen, and the next launch got a new pid (12460). The request that ends it on that iPhone is `killPid:` (`how: "kill"`). Before the fix, `killPid:` with the pid as a plain integer was accepted but ended nothing while the bridge reported success |
| Input on iOS 27.0 (iPhone17,1, 402 × 874 points), in one running `serve` session | **checked**: `tap` on Settings' search field 0.7 s (the field activated and the keyboard came up), `keys "wifi"` 0.1 s (typed, results shown), `swipe up` 0.3 s (keyboard dismissed), `button home` (home screen). The Dynamic Island shows a screen-sharing indicator while the stream is open |
| Input from a one-shot `call` (or the node's `personad iphone call`) on iOS 27.0 | **checked**: `personad iphone call <udid> tap` on Settings' search field answered `{}` in 2 s and the field activated with the keyboard up; the bridge's own `call` with `button home` (2.3 s) went to the home screen. Before the fix the same tap answered `{}` and did nothing (the stream stopped right away) |
| `long_press`, `drag`, `lock` and the volume buttons, coordinates in landscape | **not verified on a real device** |
| Input below iOS 27 | not available: an iOS 18.7.3 iPhone itself refused the display stream with CoreDevice error 9021 ("Remote control requires iOS 27.0 or later"); input answers `unsupported` up front there |
| Other operations on iOS 27.0 (iPhone17,1) | `launch_app` 0.1 s, `terminate_app` (`how: "kill"`, then `false`), `list_apps` 58 user apps in 0.2 s, `device_info` (input, screenshot, apps `true`) |
| `screenshot` on iOS 27.0 | works; conversion 16–40 ms. Capture about 0.7 s when no display stream ran in the last few seconds, 3–6 s soon after one (`timing_ms`: 6180 ms with `stream_open: false` about a second after a previous call's stream stopped; 4587 and 3259 ms with the stream open right after `button home`); once 18 s before `timing_ms` existed |
| Developer Mode off at pairing (iOS 27.0) | pairing succeeded, then connecting failed with `device socket io failed` and there was no Bonjour advertisement. The `developer_mode` field, the `pair` hint and `developer_mode_off` are **not verified on a real device** (nobody turned Developer Mode off to try) |
| `usb-list` | checked on a real iPhone (iPhone13,4, iOS 18.7.3, trusting the computer): it lists the phone with its name |
| `pair` over USB (consent prompt, `altIRK`) | checked on that iPhone while it already trusted the computer: it answered the pairing request with pairing data straight away, without asking its user, and the record was written (0600). The first try panicked there (exit code 101): the library asked the bridge for the PIN and got an empty one; the bridge now gives iPhone's fixed manual-pairing PIN `000000` and turns any panic into an `internal` error. Not verified: the Trust prompt on a phone that doesn't trust the computer yet, and the path where the phone asks its user to allow the pairing (`awaitingUserConsent`) |
| `match` against a real advertisement | checked on that iPhone against its `_remotepairing._tcp` advertisement on the USB network interface (port 49152, TXT `identifier`, `authTag`, `ver`, `minVer`, `flags`): it answered `true`. An advertisement over Wi-Fi not verified |
| `reveal-developer-mode` (AMFI) | **not verified on a real device**: no phone with the switch hidden was at hand |
| `ax_tree` | `unsupported` |
| `mount_ddi` (build selection, the phone's ticket or TSS, upload, mount, service directory again) | checked on the iOS 18.7.3 iPhone: mounted in 3.3 s, then `already: true`; `device_info` showed `ddi_mounted: true` and the developer services. The ticket's source (the phone's own or Apple's signing server) was not recorded: no `--force` remount was run |
| `mount_ddi` as a cryptex (#12171) | checked on iOS 27.2.1 (iPhone19,4, chip `0x8160`, which Xcode 27.2's per-device builds don't list): the image Xcode had installed counted as mounted (`already: true` in 0.2 s, where the bridge before this failed with `ddi_unsupported_device`); installed from scratch in 3.9 s with a ticket from Apple's signing server (Xcode, trying at the same moment, failed with "remote device not connected" and then found this image installed); a locked phone with an install pending answered `locked` within 6 s, where it hung for minutes before; `PasswordProtected` was `true` locked and `false` unlocked. The personalized image on iOS 27: not verified, that phone has no per-device build |
| `device_info` after an image mounted outside the session (#12171) | on iOS 27.2.1, a session opened before another session installed the image went on answering `ddi_not_mounted` with the first version of this change, which only read the directory again; in a second run, with the phone locked, such a session's read failed with a reset connection. Answering `disconnected` there, and the node's new session then listing the services: **not verified on the phone yet** |
| Simulator backend: pair, `device_info`, `screenshot`, apps, `ax_tree` (with a system alert), tap, `keys` (ASCII and Chinese), swipe, `button home`, `serve`, runner cleanup after exit and after `kill -9` | checked on an iOS 27.2 simulator (Xcode 27.2) |

## Open questions

- **Accessibility tree**: needs a client for the phone's accessibility inspector service; to be built and tested with a phone.
- **Text beyond ASCII**: the key channel cannot type it. The phone's pasteboard service plus a paste shortcut might, at the cost of the
  user's clipboard; not done.
- **Locked screen**: lockdown's `PasswordProtected` followed it on iOS 27.2.1 (`true` while the phone wanted its passcode, as CoreDevice's
  lock check reads it), and `mount_ddi` checks it before a cryptex install; `device_info` still reports `locked: null`, older iOS not tried.
- **Keeping the phone awake while it installs the image**: Xcode holds a power assertion on the phone while it mounts; the library has no
  client for that, so a phone that locks itself during a cryptex install leaves the install unanswered and `mount_ddi` ends with
  `timeout`. On iOS 27.2.1 cryptexd answered again once the phone was unlocked; whether the waiting install then completes was not seen.
- **Typing with a non-English input method**: `keys` sends hardware keyboard events, which the phone's current input method interprets
  (a Chinese keyboard composes). A reliable way to type plain text regardless (switching the input method, or the pasteboard plus a paste
  shortcut) is not known yet; the bridge doesn't try.
- **Slow captures**: on iOS 27.0 a screenshot's capture takes about 0.7 s when no display stream ran recently, but 3–6 s within a few
  seconds of one, open or just stopped (and once 18 s). The conversion is not the cause. Whether the phone's capture service waits for
  the stream to wind down, and whether waiting or stopping the stream first would help, is not known; the bridge doesn't change
  anything for it yet.
- **Signing request over HTTPS**: needs the library to let the caller choose the address or send the body itself.
