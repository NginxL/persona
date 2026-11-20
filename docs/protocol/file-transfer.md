# File transfer: phone ↔ computer, computer ↔ computer

Sending files to your computers to keep, and between them, over the connections Persona already has (#3065). This page is the design and the server side; the endpoints are specified in [protocol.md](protocol.md) ("Sending files", "Sending files between computers").

## What exists

| Direction | How | Where it lands |
| --- | --- | --- |
| Phone → computer, chat attachment (older apps) | `POST /api/v1/machines/{mid}/files` (no `dest`) | the temp directory, renamed `name-<time>.ext`; temporary |
| Phone → computer, chat attachment kept (#8361) | `…/files?dest=attachment` | the node's `uploads/<date>/` beside the chat's pictures, under its own name; kept |
| Phone → computer, to keep | `POST /api/v1/machines/{mid}/files?dest=inbox` | the computer's receive folder, under its own name |
| Phone → folder the file browser shows | `…/files?dest=dir&dir=…` | that folder ([protocol.md](protocol.md) "Changing files") |
| Computer → phone | `GET /api/v1/machines/{mid}/file/download` (Range) | the phone |
| Computer → computer | `POST /api/v1/files/transfer` | the target's receive folder |
| Agent's computer → its conversation's computer (a task's file, #10511) | the assistant manager: `file.chunk` + `file.upload dest=file` | the conversation computer's `uploads/files/<date>/`, kept |
| Computer → phone, started on the computer | not yet: needs node → hub notifications and a push; evaluated separately | |

The app side (share sheet "Send to <computer>", "Send file to computer", "Save to phone / Send to another computer" in the file preview, Shortcuts) is separate work on top of these endpoints.

The task-file copy (#10511) is not a person's transfer and is not gated by the switch below: the assistant manager copies a file a task's Agent handed over to the computer of the conversation that hears of the task, with the same `file.chunk` / `file.upload` RPCs the user's attachments use in the other direction, and it lands in that computer's `uploads/files/<date>/`, not the receive folder. Only files a task's Agent named in its final reply are taken, and the node's read guard refuses credentials and iCloud ([agent-work.md](../assistant/agent-work.md) "Files a task hands over").

## The switch: off until turned on

Every feature that writes files to a computer is off by default. Each computer has a **file transfer** switch (`internal/devices/transfer_settings.go`), off until the user turns it on:

- Kept by the manager in the settings table under `machine_file_transfer` (`{"<machine id>": true}`), like the clipboard switch. A machine that is not listed is off, and so is every machine while the settings cannot be read (it fails closed; the manager reads them again at most every 10 s, so a switch turned off elsewhere takes effect within that).
- `GET /api/v1/machines/{mid}/file-transfer/settings` → `{enabled, inbox?}`; `PUT` the same path with `{"enabled": bool}` → the same. Only a full device changes it (answer-only devices and extension tokens never reach it). The machine object carries it as `file_transfer: {enabled, inbox?}`, where `inbox` is the receive folder (hub.json `inbox_dir`, else `~/Downloads/Persona`) while it is on; a manager from before the switch sends no `file_transfer`, which clients and the hub read as off.
- While it is off: a `dest=inbox` upload to the machine is 403 `file_transfer_disabled` before the node hears of it (the hub checks the switch as the machine's manager last reported it), and a transfer from or to the machine is 403 `file_transfer_disabled` with `machine` naming it. A transfer running when either end is turned off stops at its next chunk (`failed`, `code: file_transfer_disabled`) and the target's partial file is removed.
- Not affected: chat attachments (`files` without `dest`, or with `dest=attachment`), the terminal's paste, health reports, the file browser's `dest=dir` uploads and edits, and previews / downloads (`file`, `file/download`, thumbnails). These have their own rules and keep working with the switch off.

## Receive folder

- One per computer: hub.json `machines[].inbox_dir` ([config.md](../operations/config.md)), default `~/Downloads/Persona` (`proto.DefaultInboxDir`). The client never names it: a `dir` sent with `dest=inbox` is ignored, so a device can only put files where the owner configured.
- The node accepts `~/…` or an absolute path, and only a folder the phone may write to (`node/writeguard.go`: in the home; not `~/.persona`, `~/Library`, the data folder, key folders, a manager's folder). Before anything is created, the part of the path that exists is resolved link by link (`realPath`) and the result checked: a link out of the home, `../` above it or a relative path are refused and **no folder is made** anywhere. The folder is then created (0755) and checked again like any `dest=dir` folder.
- The file keeps its name (`/`, `\` and control characters become `_`, so a name cannot leave the folder). A taken name becomes `name 2.ext`, `name 3.ext`… as Finder does; nothing in the folder is ever replaced, also not a file that appears while the upload runs (the name is taken with `O_EXCL`). Mode 0644, written to a hidden `.persona-upload-*` in the same folder and renamed when complete.
- Limit 2 GB (`proto.MaxInboxUploadBytes`, the same as a folder upload). The chat's 200 MB stays for attachments: those go to the temp directory, where an agent only reads them. A file kept on purpose is often bigger (a video, a disk image), and nothing holds it whole: the hub forwards 1 MiB chunks (from the phone; between computers the manager does), the node appends them to the part file.
- No expiry: the folder is the owner's, like Downloads.

## Computer to computer

The manager that carries the computers runs it (`internal/devices/transfer.go`): it holds their node connections, so it reads the source with `file.chunk` (the RPC the download uses) and writes the target with `file.upload.*` (`dest=inbox`), a chunk at a time, over them. The hub is only a communication center ([architecture.md](../server/architecture.md)): it relays the apps' requests to the manager (`devicewire.TransferRoutes`) and the manager's events to the apps; the bytes never pass through it. The file is never written to a disk on the way and never whole in memory.

```text
app ── POST /api/v1/files/transfer ─▶ hub ── http.request ─▶ manager
                                                             manager ── file.chunk (length 0) ──▶ A   size, name, revision
                                                             manager ── file.upload.begin ──────▶ B   dest=inbox, dir from hub.json; B echoes dest
app ◀─ 202 {id, state: running} ───── hub ◀────────────────── manager
                                                             loop: file.chunk @revision ◀─ A   (the next one is read …
                                                                   file.upload.chunk ───▶ B    … while the last is written)
                                                             file.upload.end ───────────▶ B   path
app ◀─ file_transfer events ───────── hub ◀─ ws.broadcast ── manager
```

- **Which manager**: the hub sends a POST to the manager of the source computer (`from.machine` in the body), and refuses 409 `different_managers` when the target is another manager's: a transfer between two managers' computers would need one manager to reach the other's, which they cannot. `GET` and `DELETE` go by the request: `?manager=` (the `file_transfer` event carries `manager` for a manager other than the built-in), else the default manager.
- **Pinned revision**: every chunk is asked at the revision the first call returned, and must come back with that size, offset and length. A source changed mid-way fails the transfer with `code: file_changed` (the node now says so with a code; older nodes' message is the same) instead of producing a file that is half one version and half another.
- **Bounded memory**: one reader asks the source while the writer sends the previous chunk to the target, with one chunk waiting between them (`transferWindow`): at most three 1 MiB chunks per transfer. At most 8 transfers run at once (429 `busy` beyond), so the manager holds at most about 24 MiB for transfers. Each call to a machine has a minute.
- **Failure on either side, or cancel**: the target's upload is aborted (`file.upload.abort`, with its own short timeout, not the cancelled context), so no part file stays; the state is `failed` with the error (prefixed with the machine's name, the rpc code kept in `code`) or `cancelled`. A machine that drops its connection fails its pending call at once.
- **Checks before 202**: both machines are known; the source can read the file (its own errors pass through: missing, iCloud, keys); the size is within the limit; the target's node knows `dest=inbox` (it must echo `dest`; an older one that ignores it and opens the file in its temp directory has that upload aborted) and accepts the receive folder. So a request that cannot work is refused synchronously, and a 202 means bytes are flowing.
- **Who may**: only a full device the person signed in with. Answer-only devices (the watch) are 403 by the device-role allowlist; extension tokens have no scope for these routes, and the manager checks the role the hub passes on (`X-Persona-Role`) again. A transfer is always a person's explicit action in an app.
- **Same machine**: allowed (`from` and `to` the same): a copy into the receive folder.
- **Across trust levels**: allowed. A transfer between a computer marked `trust: work` and a personal one, in either direction, is not refused (user decision, 2026-11-10): it is always a person's explicit action, and both ends must have the switch on. Agents and extensions cannot start one.
- **Linked hubs**: a computer reached through a member hub is carried by the primary's manager like any other, so it can be either end; that path is not verified yet.

### State, events and restarts

- A transfer lives in the manager's memory (`internal/devices/transfer.go`): `running` → `done` | `failed` | `cancelled`, with `bytes`, `total`, `result_path` (absolute, on the target), `error`, `code`. Finished ones can be read for an hour.
- **A restart or redeploy of the manager forgets every transfer** (memory can be wiped by anyone's redeploy at any time). That is acceptable here because a transfer is cheap to repeat and leaves nothing half-done: the target's node removes the part file when no chunk comes for 2 minutes (`uploadIdle`), and the source is only read. A client following a transfer that `GET` answers 404 `unknown_transfer` shows it as interrupted and offers "Send again" (the same POST). Persisting transfers in Postgres would not let one resume anyway: the upload on the target is gone with its idle timer.
- Progress: `{"type":"file_transfer","transfer":FileTransfer}` on the WebSocket, at the start and end and at most once a second in between, sent by the manager as `ws.broadcast` and passed on by the hub only to full devices that declared `file_transfer`. Like `extension_operation` it stays out of the event log, so reconnecting does not replay a stream of progress; the app `GET`s what it shows. A client that does not declare it polls `GET` (once a second is fine).
- Cancel: `DELETE` marks it cancelled, cancels the run and waits (up to 15 s) for the abort to reach the target, then answers the final state. Cancelling one that is over changes nothing.

## Compatibility

- Hub features `file_inbox` and `file_transfer`; apps show the entries only when they see them.
- An older hub has neither the parameter (it answers 400 unknown dest) nor the routes (404). While the manager is away the routes answer 503 `assistant_offline`, as its other routes.
- An older node: `dest=inbox` is answered `unknown upload destination`, or ignored by one from before destinations; both are 409 `node_outdated` naming the machine. A node from before `file.chunk` as a source is 409 `node_outdated` too.

## Not done yet

- The app entries listed above; real-device transfers of up to 1 GB.
- Starting from a computer (Mac "Send to phone"): needs a node → hub notification and a push.
- Machines behind a linked member hub.
- Receive folder in the apps' settings (hub.json only for now), and the switch in the apps (the API is there).
