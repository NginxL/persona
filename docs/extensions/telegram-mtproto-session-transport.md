# Controlled Telegram MTProto sessions for source extensions

## Status and phase boundary

**Status: proposed design, not implemented; issue #1167.** The accompanying contract and model tests are synthetic design evidence only. They add no SDK method, production transport, source migration, marketplace change, real-account test, or production-isolation acceptance. Telegram source migration remains **blocked** after this design until a separately reviewed implementation passes the real manager/bridge gate below and the production gate in #1151.

The design preserves the personal-account connector; it is not a Bot API connector and does not remove archive, Inbox, thread, avatar, media, update, or write behavior. A source guest never receives a raw socket, an HTTP CONNECT tunnel, an encrypted-stream endpoint, an authorization key, or a general TL/RPC invocation method.

The minimum runtime capability is `telegram.mtproto.v1`, adapter protocol `1`, initially on supported Linux source-runtime hosts. There is no fallback to an unsandboxed process, JavaScript MTProto implementation, HTTP transport, or arbitrary node program.

## Protocol ownership and trusted session owner

| Component | Owns | Must not receive |
| --- | --- | --- |
| Source Program | Connector policy, typed mapping, CLI/read planning, archive and cached-Inbox policy, native UI state | Secrets, raw TL methods, caller-selected endpoints, native code, sockets, unbounded media |
| Extension manager | Installation/account leases, grants, confirmations, request journal, cache/cursors, sealed storage, budgets, imports, audit, adapter lifecycle | Telegram authorization-key plaintext in its database, logs, events, or snapshots |
| Telegram protocol adapter | MTProto encryption, authentication stages, persistent updates, gotd session state, DC authorization and file locations | Guest-provided code, arbitrary destinations, direct filesystem or environment access |
| Trusted egress | Static DC registry checks, checked-address dialing, optional authenticated HTTP CONNECT proxy | Guest addresses, proxy-side re-resolution, direct fallback after proxy failure |
| Hub | Authenticated transport between owner clients and the manager | MTProto business logic, credentials, session keys, Telegram routes |

The adapter is a separate manager-launched program, `persona-telegram-mtproto`, not code linked into `personad`. The reviewed implementation pinned by this design is pure-Go `gotd/td` **v0.163.0**, matching the legacy connector's session format and recorded dependency checksum. An upgrade or different library needs another protocol review, a pinned version, and compatibility fixtures; session formats are not interchangeable. Retaining this trusted native adapter is an architectural choice, not a claim that all Telegram executable code became source.

The manager and adapter communicate over a manager-created anonymous Unix socket pair with inherited descriptors, peer-credential checks, a random process-generation value, framed protocol-v1 messages, and a 1 MiB control-frame ceiling. The adapter has no listening socket. Trusted egress passes an already-checked connected descriptor for an authorized DC; the adapter gets no general dial operation. Sealed session loads/saves are manager-mediated and generation-fenced. Media uses bounded frames, not shared memory or guest files.

## Typed operations and exact 64-bit values

`schema/extension/telegram-mtproto-session-transport.contract.json` is the machine-readable operation, schema, grant, and budget inventory. It is a design contract, not a registered runtime schema. These names are typed contract names, **not** newly available `Hub.call` methods.

Every request has `contract_version`, `installation_id`, `generation`, `account`, `request_id`, and a deadline. Every result has the same request ID and generation. Unknown fields, additional operations, raw TL bytes, a `method` field, an endpoint/address field, or a caller-provided proxy URL are errors. Requests and events use bounded UTF-8 JSON; media bytes use base64 below the source frame limit.

Telegram `peer_id`, `access_hash`, `random_id`, file ID, size, and cursor values use `i64-decimal` or `u64-decimal`: canonical base-10 strings with optional sign only for signed values, no leading zeroes, and range validation at the boundary. JSON numbers, exponent notation, floating-point values, JavaScript `Number`, and lossy coercions are rejected, including below 2^53. Tests include `9007199254740993`, signed 64-bit boundaries, and `18446744073709551615`. A peer is `{kind: "u"|"g"|"c", id: i64-decimal, access_hash: i64-decimal}`; its stable source key is kind plus canonical ID, never a username or phone number.

Opaque account, authentication-flow, media, and DC handles contain 128 bits of cryptographic randomness in unpadded base64url with a type/version prefix. Their text carries no account or installation facts. The manager resolves one only when installation, connector `telegram`, account identity, current process generation, grant category, and lease expiry all match. A handle copied from another installation or generation is invalid.

| Operation | Caller / grant | Bounded input and result |
| --- | --- | --- |
| `credentials.validate` | Owner host flow | API ID/hash through secret fields; one gated read-only probe; redacted validity |
| `auth.start` | Owner host flow | Validated credential reference and phone secret; opaque flow plus `code_required` and redacted delivery kind |
| `auth.code` | Owner host flow | Opaque flow and code secret; `password_required`, retryable `invalid_code`, or redacted account status |
| `auth.password` | Owner host flow | Opaque flow and 2FA password secret; retryable `invalid_password` or redacted account status |
| `auth.cancel`, `auth.check`, `auth.logout` | Owner host flow | Cancel only that flow, distinguish network/auth expiry, or report local and remote revocation separately |
| `session.restore` | Source `read` | Restore only sealed state for the bound account/generation; never fabricate empty credentials after a load failure |
| `dialogs.list` | Source `read` | `folder=main|archive`, cursor, at most 200; stable peers, folder/mute flags, top-message cursor, next cursor |
| `messages.read` | Source `read` | Exact peer, before/after pagination and time bounds, optional unread/mention filters, at most 50; oldest-first messages, reply references, sender/avatar and media descriptors |
| `messages.get` | Source `read` | Exact peer and 1–50 message IDs; present messages in request order, including reply references |
| `messages.search` | Source `read` | Query, optional exact peer, time/cursor bounds, at most 30; stable message and peer identities |
| `members.list` | Source `read` | Exact peer, cursor, at most 100; optional total count, preserving unknown versus zero |
| `updates.subscribe`, `updates.ack` | Source `updates` | One subscription/account; typed update or explicit `gap`, durable cursor acknowledgement |
| `inbox.collect` | Source `read` | Cached dialogs/context only; zero Telegram requests; preserve exclusions and done state |
| `media.download` | Source `media` | Opaque restart-safe reference, offset and at most 32 KiB; typed data, EOF and resume state |
| `authorization.export` | Trusted adapter only | Host-internal DC authorization transfer; always uses the shared RPC gate |
| `message.send` | Source `write` + confirmation | Exact resolved peer, non-empty text at most 4,096 UTF-16 code units, optional same-peer reply ID, request ID |
| `message.forward` | Source `write` + confirmation | Exact source and destination peers, 1–20 message IDs, one bound random ID per message, request ID |
| `message.react` | Source `write` + confirmation | Exact peer/message, one supported emoji of at most 8 Unicode code points, request ID |
| `message.mark_read` | Source `write` + confirmation | Exact peer and optional up-to message ID, request ID |
| `write.status`, `write.cancel` | Source `write` | Read a receipt or cancel pending work; neither retransmits an ambiguous write |

All CLI and assistant read operations use these typed reads and page limits. Reply references are expanded once; a nested reply is identified but not recursively expanded. Write target resolution accepts only a peer/access-hash record previously returned for this account, and rejects a username-only or remotely guessed target. The source keeps bot, self/Saved Messages, and broadcast exclusions for archive and Inbox. Main and archived dialogs have independent cursors; muted or archived conversations remain archivable without entering Inbox.

## Grants and write authorization

A proposed source manifest requests `telegram.mtproto.v1` and individual categories. Nothing is granted by an HTTP destination, storage permission, `inbox.reply`, or possession of an account handle. `read`, `updates`, and `media` are independent; all four write categories use `write` plus their existing confirmations. `authorization_export` exists only between manager and adapter. The owner authentication flow is not a source grant.

The existing connector confirmation remains mandatory even when a source user directly invokes an action. The host issues a single-use confirmation grant bound to the exact typed operation, source and destination peers/access hashes, message IDs or reply target, full payload digest, caller request ID, installation/account/generation, and expiry. The manager validates it before journaling and again immediately before dispatch. Rejection, expiry, changed payload, or an ambiguous target produces zero write requests. An account/session lease alone never authorizes a write.

Before transmission the manager atomically stores a bounded journal entry `{request_id, operation, target, payload_digest, random_ids, state=reserved}`. The same request ID and fingerprint returns the existing receipt without sending; the same ID with different content returns `request_conflict`. Telegram random IDs are generated once, stored with the reservation, and never regenerated. A journal-full or reservation-save failure sends nothing.

After dispatch the entry is `sent`, then `confirmed` only with Telegram's result. A timeout, disconnect, receipt-save failure, or cancellation after possible dispatch becomes `unknown`, preserving original random IDs and receipt. `write.status` only reads it. Neither manager, adapter, reconnect logic, nor a duplicate call automatically retransmits `sent` or `unknown`; exactly-once remote delivery is not claimed. Canceling work that has not dispatched marks it `canceled`; canceling dispatched work does not erase its outcome.

## Opaque sessions, credentials and sealed state

API hash, phone, login code, 2FA password, MTProto authorization keys, `phone_code_hash`, and proxy credentials terminate at authenticated manager/host flows. They never pass through ordinary `connector.login.step`, source RPC arguments, Program storage, source events, native snapshots, errors, or logs. Existing generic secret delivery to a Program is not used. The owner client submits each secret to a manager route authenticated by the existing owner credential; the manager returns only a redacted stage and opaque flow. This design does not change the existing sign-in screen.

Credential validation makes the legacy read-only probe because a successful TCP/MTProto handshake alone does not validate API credentials. `invalid_code` and `invalid_password` retain only their current host flow for bounded reentry. A flow expires after ten minutes or five failed submissions, whichever comes first, and cancellation erases its transient secret fields. A successful login commits sealed state only if its flow generation is current.

Sealed account state contains API credentials, gotd session bytes, redacted account identity, schema version, and bounded recovery metadata. Plaintext exists only in trusted flow/adapter memory. Session restoration distinguishes `network_failure` from `authorization_expired`: transient failure retains state and permits capped reconnect; authorization expiry prevents reconnect until explicit reauthentication. Corrupt or unsupported state is retained and reported, never replaced by empty credentials.

## Endpoint and proxy policy

Trusted host data ships a reviewed Telegram production-DC registry, `telegram-production-dc-v1`, mapping DC IDs to checked public IP/port endpoints. Guest input cannot add or select an address. A DC option learned over an already authenticated MTProto session is accepted only when its DC ID and endpoint match that registry. Unknown, changed, or caller-supplied endpoints fail with `dc_unverified` and require a reviewed registry update. There is no silent DNS re-resolution. CDN use is outside v1 unless a later reviewed contract pins Telegram's verification keys and checked CDN destinations.

Before every direct dial or CONNECT, trusted egress rechecks installation/generation/grant, flood state, DC registry entry, and pinned numeric address. It refuses loopback, link-local, multicast, unspecified, metadata, hub/local-service, unrelated private, and non-Telegram destinations. A configured host-owned proxy reference is bound to installation and account; its credentials stay sealed. When configured, every MTProto connection uses authenticated HTTP CONNECT to the selected DC's pinned IP and port. The proxy cannot re-resolve or redirect to another address, and proxy failure returns `proxy_unavailable` without trying direct networking. Neither source nor adapter can issue CONNECT itself.

Each permitted DC has one lazy bounded connection pool, created only on demand and closed on revocation. The initial pool limit is one connection per DC, preserving legacy behavior. Media uses the file's known DC; an unknown DC starts at the account DC. Authorization transfer is host-only and its export is gated. `FILE_MIGRATE` re-resolves only through the authenticated account connection and checked registry.

## Lifecycle, cancellation and revocation

Account and DC leases last fifteen minutes and renew only while installation, generation, account, and grants remain valid. Pending calls have explicit deadlines and cancellation. Disable, uninstall, account replacement, grant revocation, and process-generation replacement close only that account's adapter, update stream, DC pools, media streams, and queued calls. Late events and session saves carry a generation and are discarded after fencing.

Logout and revocation stop local transport immediately, even when Telegram cannot be reached. Status separates `local_revoked` from `remote_revoked` or `remote_revocation_pending`. At most one sealed recovery generation per account is retained for remote revocation. No update, read, media, or write transport reconnects after local revocation. A later explicit owner retry may create a one-shot host-only `auth.logout` flow under the same gate and budgets; it cannot restore the account or deliver events. A late login completion, import, or session save cannot resurrect revoked state.

## RPC gate, updates and finite budgets

One account-wide RPC gate is shared by normal reads/writes, login probes, helper requests, and authorization-export RPC. It admits one active request and waits a deterministic-in-tests random 0.5–1 second interval after the previous request completes. Nested authorization export uses the gate exactly once and cannot recursively deadlock the request that caused it.

A Telegram flood wait blocks the whole account until the requested duration plus the pacing interval. During that interval the manager admits no RPC, update reconnect, DC handshake, authorization export, or media connection. Calls fail with redacted `flood_wait` and retry-after metadata; they are not held as unbounded sleepers. There is no automatic retry of a possibly committed write. A reconnect does not clear flood state.

DC connection establishment and key generation happen outside the RPC gate while flood wait is inactive. Different DCs may connect concurrently, and a slow DC handshake cannot block an unrelated RPC or another DC. Only authorization-export RPC and actual Telegram requests enter the gate. Every account still has one gate across all its DCs.

The persistent updates connection coalesces notifications into bounded rounds. The minimum round interval is 60 seconds; any flood wait since the previous round doubles it up to 16 minutes, and a round without one halves it toward 60 seconds. A round refreshes at most 100 dialogs per folder and ten context batches, with at most ten context messages per item. Queue overflow creates a durable `gap` marker and requires reconciliation from stable cursors; it never appears as a complete update stream.

`inbox.collect` reads the manager/source cache and makes no remote request. A refresh populates that cache before invalidating Inbox. Archive commits each completed chat batch and per-peer cursor before returning partial progress; a later error or cancellation retains completed batches and reports `more`. Initial history remains at most 30 days and 500 messages per peer, with at most 200 dialogs per folder per round. Reply references and pagination cursors remain stable across restarts.

These are v1 starting limits, not permission to raise existing source/runtime limits. Implementation may lower them after parity review. Overflow returns a typed error before allocation; no queue silently grows or discards data.

| Resource | Per account / installation | Aggregate manager |
| --- | --- | --- |
| Active accounts and updates connections | 1 each | 32 accounts / 32 updates connections |
| Source calls | 64 pending, 2 MiB queued; 64 KiB/call except typed media | 256 pending / 64 MiB queued |
| RPC | 1 active; 30-second deadline | One scheduler/account, no global RPC serialization |
| Authentication | 1 flow; 10 minutes; 5 failed submissions | 32 flows |
| DC connections | 5 DCs, 1 lazy connection each; 15-second handshake | 128 DC connections, 8 concurrent handshakes |
| Reconnect | 5 attempts/outage; 10 s doubling to 10 minutes, then `reconnect_exhausted` | 32 active reconnect cycles |
| Updates | 1,024 events or 4 MiB; durable gap on overflow | 64 MiB |
| Cached dialogs / stable peers | 400 / 1,000 | Bounded by 32 accounts |
| Media | 2 open streams, 8 MiB buffered; 256 sealed references / 4 MiB | 16 streams / 64 MiB buffered |
| Write journal | 1,024 entries or 4 MiB; unknown entries never silently evicted | 64 MiB |
| Archive/import state | 4 MiB cursors; 1 MiB legacy sealed input | Bounded by 32 accounts |
| Shutdown | 5-second bounded close/cancel | No process-group or descriptor leak |

Reaching a peer-cursor ceiling returns `budget_exhausted`; cursors are never reset to make space. One account's saturation cannot consume another installation's queues, RPC gate, DC handshakes, or media buffers. Aggregate exhaustion rejects new work with `budget_exhausted` while existing neighboring extensions continue.

## Media and cross-DC recovery

A message-media descriptor contains an opaque reference, origin peer/message IDs, file ID/access hash, current file reference, known DC, size, and display metadata in sealed manager state. An avatar descriptor instead retains its origin peer/photo identity. Neither contains a guest filesystem path. The origin identity allows a fresh file reference after restart or expiry. A stale/moved reference triggers one bounded origin-message refresh through the normal RPC gate, then resumes at the acknowledged offset; repeated refresh failures preserve the offset and return a redacted error.

Remote downloads use aligned **512 KiB** parts, except the final part. The adapter/manager never accumulates a whole file. The source receives at most **32 KiB** per `media.download`, below source frame and base64-encoding limits. At most two media streams per account and sixteen per manager may be open; buffered media bytes are capped independently of file size. Avatars use the same typed media path. Closing, cancellation, account replacement, and revocation release only their own streams and buffers.

## Legacy import, handoff and rollback

The versioned host importer accepts the existing sealed connector envelope: API ID/hash, gotd session bytes, redacted account identity, and unrelated archive/Inbox cursors. Input is at most 1 MiB and is schema/bounds checked before any new session starts. The gotd bytes remain gotd-specific; v1 performs no unreviewed conversion. Original sealed bytes and cursors remain unchanged during staging.

Handoff uses one durable account ownership generation:

1. Disable new source events, acquire the account handoff lock, stop the old executable, and wait for its transport to exit.
2. Stage the new adapter with a new generation and validate account identity and restorable state. Old and new owners never attach concurrently.
3. In one manager transaction commit imported state, archive/Inbox cursors, capability version, and the new ownership generation, then enable events.
4. Retain one bounded sealed previous generation for rollback. Rollback fences/closes the new generation before restoring the old executable and original bytes.

Corrupt, unsupported, or transiently unavailable input leaves the original owner/state intact and reports `legacy_unsupported`, `legacy_corrupt`, or `network_failure`; it never commits empty credentials. If validation cannot preserve identity or state, migration remains blocked for an explicit product decision on reauthentication. A late staging commit loses to logout, replacement, or rollback generation checks.

Partial archive cursors import without resetting completed peers. Restart-safe media references retain their origin peer/message so a restart can refresh a file reference without resetting downloaded offsets or archive progress.

## Audit and isolation

Stable redacted error codes cover credential/code/password rejection, network failure versus authorization expiry, flood wait, reconnect exhaustion, unverified DCs, proxy failure, stale leases, denied grants/confirmations, request conflicts, unknown outcomes, queue/budget exhaustion, gaps, cancellation, revocation, unsupported state, and missing objects. Errors may carry retry-after, DC number, counts, and redacted stage, never upstream text containing secrets or message content.

Audit records grant/lease/import/logout lifecycle, operation category, confirmation decision, DC number, queue/byte budget usage, latency, reconnect/flood state, and redacted outcome. It does not record message text, names, phone numbers, usernames, peer/access-hash/random-ID values, credentials, session bytes, proxy secrets, authorization keys, raw media references, or upstream error payloads. Handle fingerprints use a host-keyed redaction function, not the opaque handle itself.

Isolation checks reject cross-account, cross-extension, cross-installation, and stale-generation handles and events. Resource accounting is partitioned by installation and account before aggregate limits are applied. A saturated or revoked account cannot interrupt a neighboring extension.

## Synthetic conformance evidence

`scripts/test/test_telegram_mtproto_design.py` and its test-only model exercise the contract with fake clocks, fake checked dialing, fake Telegram responses, sealed in-memory state, and synthetic identifiers. They cover authentication branches and restoration, failure taxonomy, DC/update/media behavior, exact integers, RPC/flood/slow-connection gates, write confirmation/deduplication/ambiguity, cancellation/revocation, import/rollback/archive/media recovery, forbidden endpoints and proxies, wrong handles, stale events, queue exhaustion, redaction, and a surviving neighboring extension. These tests prove the executable design model only.

Run them with:

```sh
python3 -B scripts/test/test_telegram_mtproto_design.py
```

## Real manager/bridge isolated-hub acceptance

A later implementation PR must separately wire the real manager/adapter and prove, on an isolated hub, that an installed complete source archive passes the same behaviors through the real manager/bridge. It must also verify actual process/descriptor teardown, real sealed-store restart, SDK exact-string handling, package contribution parity, and the unchanged executable's single-owner rollback. Synthetic acceptance remains labeled as such.

Real-account tests require separate explicit approval and sanitized evidence; none is authorized or performed here. Default source publication, market-index changes, signing, production activation, and the production isolation gate in #1151 remain outside this design.
