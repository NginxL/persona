# Single-group sync driver

These fixtures describe the connection decisions shared by phone clients. Their expectations were written from
the Swift source and tests named in each case. The pure Rust driver replays three topics on the host.

| Topic | Cases | Source and behavior |
| --- | ---: | --- |
| [Reconnect backoff](reconnect_backoff.json) | 4 | `LinkDrop.retryDelay`, `LiveConnection.dropped` and `scheduleReconnect`: initial failures, ordinary and announced drops, the restart fast window, extending a drop, and the 401 retry floor. |
| [Event cursor](event_cursor.json) | 3 | `EventCursor` and `LiveConnection.note`: full and resumed hellos, replay interrupted by another open, changed and absent streams, private extension frames, config changes and credential renewal. |
| [Lifecycle](lifecycle.json) | 3 | `HubLinks`, `ConnectionStatus.pathChanged` and the remote-notification callback in `PersonaApp`: foreground, deliberate suspension, individual push deadlines, returning paths and stalled sockets. |

## Running and reading a case

From the repository root, run `bash scripts/ci/core.sh`. To run the focused replay, use:

```sh
cd native/app-core
cargo test --locked -p persona-core --test sync_driver
```

The version 1 envelope follows the [continuity fixtures](../continuity/README.md). Each case starts with the full
fresh state and the host's advertised WebSocket features. Every event has a monotonic millisecond offset
`now`, measured from the start of the case. An event and its expected state and ordered `actions` share an index.
The comparison includes every field, the whole timer queue in insertion order and the exact action parameters.
The corpus also checks that changed state, reordered actions and missing steps fail the replay.

- `connect` takes two opaque numeric handles, one for the hub configuration and one for the credential. Changing
  either starts a new cursor. `renew_credential` trades the matching handle while retaining the same stream.
- `open_socket` contains the native client's unchanged `features` and the ordered resume query. A nonempty stream
  carries both `event_stream` and `event_seq`. A socket generation belongs to exactly one attempt. A failed handle
  has already closed. Replacing or suspending a live attempt emits a going-away close for its old generation.
- `frame` supplies decoded metadata. The first frame both advances the cursor and establishes the socket. The
  client hello precedes `did_connect`, which hands the native caller the decision to refetch or use replay.
  An unnumbered broadcast cannot move the cursor. Private extension frames have their own numbering.
- `socket_failed` carries the host's random sample in `[0, 1)`, even if ordinary backoff requires none.
  The driver truncates fractional milliseconds at the scheduling boundary. Deadlines use the iOS constants.
  In particular, an announced restart lasts 30 seconds from the original drop, including after an upgrade from
  ordinary grace. Its fast tries do not advance the subsequent ordinary retry ordinal.
- `schedule` supplies a complete ticket (kind, id and absolute deadline). Echo the ticket on `tick`.
  A reconnect belongs to its socket generation. Grace belongs to the **drop** that started on that generation
  and survives the socket attempts made during the drop. A superseded, early or repeated tick does nothing.
  Every push owns a different suspension ticket; at delivery it tests the current foreground/background state.
- `socket_health` reports the native heartbeat's stalled result for the named generation. The `phase` ticket
  delays a visible connection title by 1500 ms. Returning online or exhausting grace updates the title immediately.
  The snapshot records the displayed title. Pending requests, socket state and retry deadlines are independent.

## Boundary

This slice has no chat payload. `did_connect` is its boundary with the host's existing refetch and subscription
work. Per-chat batches, gaps and fill requests still belong to `persona_core::continuity::Session`.
Host socket and credential ownership, raw decoding, heartbeat and catch-up scheduling, multi-group routing,
background prefetch, write retries, storage and runtime/FFI integration are outside these cases. Production
adapters and their choice of random source remain open. Physical-device acceptance is **blocked: no physical device**.
