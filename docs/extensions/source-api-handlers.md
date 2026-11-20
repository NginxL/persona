# Authenticated source API and assistant handler host contract

Status: proposed for maintainer review under #1185. This is a design contract, not an implementation report or deployment
authorization. Normative **MUST**, **MUST NOT**, **REQUIRED** and **SHALL** statements apply to the later implementation
and its acceptance, not to code claimed to exist today.

**Who this serves**: extension authors and host implementers working in Agents and advanced extension settings. The
everyday Assistant experience, its screens and its existing defaults do not change.

## 1. Scope, current boundary and phase boundary

This design covers generic host capabilities in this repository: authenticated source API contributions, bounded
assistant command handlers, streaming media and durable write confirmation. Managed companion credentials and sensitive
session operations are independent subcontracts in sections 8 and 9, not bonuses of invoking a handler.

### 1.1 Current boundary

Read against `main` at `37f9b16fc2f67b2e008322cfbe73073a6d2d2534`; the paths are evidence of the boundary, not a claim
that the proposed behavior exists.

| Area | Current behavior that the design preserves |
| --- | --- |
| Contribution/runtime | The `api` contribution requires native HTTP (`server/internal/extension/points.go`, `server/internal/extension/manifest.go`). The source runtime requires stdio and forbids HTTP (`server/internal/extension/source_runtime.go`); the trusted adapter rejects a source `Program.fetch` (`server/internal/extension/denohost/adapter.ts`). An SDK field alone is not host support. |
| Authenticated proxy | `server/internal/extmgr/extension_api.go` selects a native HTTP proxy, uses `BeginHTTP`, removes caller credentials and installs host-derived principal headers. It does not dispatch to a source `namespaces.api` handler. |
| Assistant gates | `assistant.credentials` already has an RPC path and bounded gate declarations (`server/internal/extmgr/extension_assistant.go`). The authenticated source service/handler behind a typed command is the missing contract. Existing command allow/confirm/deny gates and one-use confirmed bodies stay in force. |
| Media/egress | Ordinary outbound source HTTP uses the checked runtime broker, with an 8 MiB body bound and a 120-second request bound (`server/internal/extension/egress/runtime.go`). Native extension HTTP streaming is not a source-handler data plane. |
| Source RPC | Existing source RPC ceilings are 8 MiB encoded frames, eight concurrent requests and 128 pending calls (`server/internal/extmgr/extension_source.go`). Process-attempt identity exists, but general handler cancellation and late-result fencing do not. |
| Durable state | The installation-owned storage contract from #1187 binds installation/grant/process attempt and supplies Postgres transactions and replay receipts (`docs/extensions/extensions.md`). It does not confirm an external effect or make guest memory durable. |
| Sensitive sessions | Since #2245, legacy extension tokens put rename under `agents.control` and restricted mode changes under `agents.permission_mode` (`server/internal/extwire/scopes.go`, `server/internal/hub/extension_modes.go`). The latter is limited to extension-opened conversations. These standing token grants are not operation-specific owner tickets and are not source-handler authorization. |

### 1.2 Phase boundary

This PR is **design only**. It MUST NOT add a production route, change a grant, add an SDK capability, activate a host
path, or migrate an integration. No migration is authorized. It does not authorize implementation; later implementation follows its own reviewed
issue, claim, branch, tests and PR. A maintainer's decision to continue planning is not the account owner's consent.

Affected integrations remain blocked until their original behavior passes section 11 without feature reduction. Default
package changes wait for an installed-source isolated-development acceptance. Marketplace indexes, new signing keys,
re-signing and production activation remain behind #1151 and their separate owner decisions. No broad egress, FullHub,
Docker, socket, execution or execution-mode authority follows from this design.

## 2. Declarations and ownership

The existing `api` contribution keeps its native-HTTP, hub-proxied inbound endpoint meaning. A new, independently reviewed `source_handlers`
declaration is REQUIRED so that an existing `api` installation never acquires an inbound route by upgrade alone.

### 2.1 Typed v1 declarations

The following is the normative shape for `source-handler/v1`; generated schema and SDK types are later implementation
artifacts. Every `SchemaRef` MUST resolve to a package-reviewed schema with closed objects (`additionalProperties:
false`), explicit required fields and bounded strings, arrays and bodies. Unknown fields and schema `any` are invalid.

```ts
type ContractVersion = "source-handler/v1";
type SchemaRef = string;                 // immutable package schema ID plus revision
type RouteID = string;                   // stable namespaced identifier, not authorization
type RoutePath = string;                 // canonical absolute template within the reserved prefix

interface SourceHandlerDeclaration {
  version: ContractVersion;
  contribution_id: string;
  handler: "namespaces.api";
  routes: SourceRouteDeclaration[];
  commands: AssistantCommandDeclaration[];
}

type CallerClass = "app" | "assistant" | "extension";

interface SourceRouteBase {
  route_id: RouteID;
  path: RoutePath;
  request_schema: SchemaRef;             // path, query, allowlisted headers and body
  response_schema: SchemaRef;            // allowlisted headers, metadata and body/chunks
  budget: "unary-v1" | "media-v1";
  callers: CallerClass[];                // required, nonempty and unique
}

interface SafeReadRouteDeclaration extends SourceRouteBase {
  method: "GET" | "HEAD";
  effect: "read";
}

interface QueryReadRouteDeclaration extends SourceRouteBase {
  method: "POST";
  effect: "read";
  query_only: true;
  read_justification: string;          // bounded, nonempty no-side-effect rationale
}

type ReadRouteDeclaration = SafeReadRouteDeclaration | QueryReadRouteDeclaration;

interface WriteRouteDeclaration extends SourceRouteBase {
  method: "POST" | "PUT" | "PATCH" | "DELETE";
  effect: "write";
  reconciliation_schema: SchemaRef;      // required request-ID recovery and outcome query
}

type SourceRouteDeclaration = ReadRouteDeclaration | WriteRouteDeclaration;

interface AssistantCommandDeclaration {
  command: "assistant.credentials";
  route_id: RouteID;
  arguments_schema: SchemaRef;
  result_schema: SchemaRef;
  grant: "source.command.credentials";
}

interface CallerContextV1 {
  version: ContractVersion;
  request_id: string;
  installation_id: string;
  contribution_id: string;
  account_id: string | null;
  process_generation: string;
  caller: AppCaller | AssistantCaller | ExtensionCaller;
}

interface AppCaller { kind: "app"; owner_id: string; device_id: string }
interface AssistantCaller { kind: "assistant"; manager_id: string; conversation_id: string; actor_id: string }
interface ExtensionCaller { kind: "extension"; installation_id: string; process_generation: string }
```

A declaration, its exact method/path/schema allowlists, nonempty closed `callers` set, POST-read no-side-effect
justification and write reconciliation schema are part of the reviewed permissions digest. Adding or widening one requires owner approval. An unsupported version,
duplicate ID, missing schema, omitted/empty/duplicate/unknown caller class, wildcard, implicit caller or unknown budget
profile MUST fail closed. Admission intersects the declared `callers` with the verified caller class and independent
grant. GET and HEAD MUST be `effect: "read"`; read declarations are limited to GET, HEAD and query-style POST.
A POST read additionally requires `query_only: true` and a bounded, nonempty `read_justification`, reviewed separately
as having no side effects and included in the permissions digest. Registration rejects a POST read without either field
or whose behavior contradicts that justification. PUT, PATCH and DELETE MUST be `effect: "write"`, and `effect: "write"` is valid only on POST, PUT, PATCH or DELETE.
Registration rejects a read declaration on PUT/PATCH/DELETE and a write without `reconciliation_schema`. GET does not
implicitly declare HEAD, and no method creates an implicit OPTIONS or CORS route.

### 2.2 Route namespace and canonical matching

The manager reserves `/api/v1/extensions/{installation_id}/-/source/v1/{contribution_id}` for this contract. Existing
hub-owned and manager-owned `/-/` operations remain reserved; a source route MUST NOT overlap them, another contribution,
or the native HTTP proxy namespace. The hub MUST remain only an authenticated relay; the extension manager owns
admission, dispatch and lifecycle. The manager performs registration, collision detection and authorization before any
source code runs.

A path template contains literal segments and whole-segment `{name}` parameters only. Two templates for the same method
whose decoded segment shapes can overlap are a registration collision and are rejected; the same canonical path with
different declared methods is allowed. Wildcard, glob, catch-all and prefix routes are rejected. Undeclared methods,
routes and media operations MUST NOT reach the handler.

Request matching uses the escaped path, never a cleaned filesystem path. Canonical normalization rules prevent
encoded-separator, double-encoding and dot-segment normalization bypasses:

1. Reject invalid percent escapes or UTF-8, NUL/control characters, backslashes, empty segments, `.`/`..`, trailing
   slashes (except the declared root), and encoded `/` or `%2f` in either case. Decode exactly once per segment.
2. Revalidate every decoded segment before matching: valid UTF-8, no path separators or backslashes, no NUL/control
   characters, nonempty, and not `.` or `..`. Thus `%2e%2e`, `%5c`, `%00` and percent-encoded invalid UTF-8 return 400
   with no dispatch; decoding a second time is forbidden.
3. Match the exact method and decoded segment sequence against the declaration. Do not call `path.Clean`, redirect to a
   normalized spelling, decode twice or treat a path parameter as more than one segment.
4. Validate path, query, headers and body against the request schema allowlist before dispatch. A route/parameters
   collision or ambiguous canonical form returns 400; an undeclared route returns 404 and an undeclared method 405.

A source receives no listener, Unix socket, or generic proxy capability; all three are forbidden. It also receives no
host filesystem, Docker socket or arbitrary network listener as a substitute.

### 2.3 Registration and lifecycle

The effective handler is the intersection of the installed declaration, the owner-approved grants and the current
process generation. The manager MUST revalidate all three at every admission. Disable, uninstall, grant revocation and
generation replacement close the route to new work and cancel its queued work. A handler reference is an opaque routing
key only; possessing it neither keeps a generation alive nor confers access.

## 3. Host authentication and caller binding

The hub or manager MUST authenticate the caller before dispatch. Authentication establishes who called; authorization is
a separate decision for the exact target and operation.

| Caller kind | Verified authentication | Independent authorization required |
| --- | --- | --- |
| App | A paired device and owner verified by the hub; revocation closes the relay | An app grant for the exact installation, contribution and route; full-device authentication alone is not a source grant |
| Assistant | The manager service identity plus the assistant conversation and attributed actor | The exact command grant and, for each write, section 7 confirmation; an assistant cannot grant itself a command |
| Extension-to-extension | The caller installation and process generation attested by its runtime/manager | An explicit caller-installation → target-installation → contribution → route grant; `extensions.call` alone is insufficient |

The manager MUST construct a host-derived `CallerContextV1` containing the verified caller, target installation,
contribution, host-selected account where applicable, target process generation and a host-derived request ID. An account
ID comes from the approved binding, never from a query, body or identity header.

Every write HTTP request declares `Idempotency-Key: v1.<not_after>.<128-bit random>` in its request schema; a write
assistant command carries the equivalent bounded `request_key` argument. On first admission, the host requires
`not_after` to be between 120 seconds and seven minutes in the future. The seven-minute replay horizon is the five-minute
confirmation expiry plus the 120-second request deadline. The request ID is
`shr_<not_after>_<SHA-256(canonical caller scope, key, not_after)>`, where caller scope is the verified caller kind and
stable ID, target installation, contribution, account, route/command and exact HTTP method. Target process generation is deliberately excluded from
that stable derivation, but remains fenced at dispatch.

A retry presents the original key and fingerprint with a current grant; the host derives the same request ID even when
the caller never received an internal ID or the first response was lost. Current authentication, caller scope and grant
are still REQUIRED, and the key never authorizes a lookup or dispatch. The response also returns the derived request ID
for status lookup. A changed key or expiry is a different operation and automatic retry MUST NOT change either. An
expired request ID is rejected `request_expired` before receipt lookup, even if its tombstone is gone. Read requests
without a replay requirement may instead use a host-generated random request ID.

Before relaying, the manager MUST strip each forged identity header and all credentials, including `Authorization`,
`Proxy-Authorization`, `Cookie`, `X-Persona-*`, `Forwarded`, `X-Forwarded-*` and `X-Real-IP`. Credentials, identity and
hop-by-hop transport headers are forbidden even if a schema tries to declare them. After that unconditional removal,
only headers explicitly allowlisted by the route's request schema survive, with their validated values carried through
typed dispatch. This includes declared `Range`, `If-None-Match`, `If-Modified-Since`, `If-Range`, `If-Match`,
`Idempotency-Key` and `Content-Type`; it is not limited to content/negotiation headers. Identity travels in the
structured host-derived context, not in forwarded headers. A bearer credential is never forwarded to source code.
Response identity headers and `Set-Cookie` are likewise removed.

App, assistant and extension-to-extension grants are separate and independently revocable. A handler handle is not
authorization, and neither a credential reference nor an account handle is a command grant. Every admission rechecks the
caller, target installation/account, contribution, route, generation and grant; cross-binding fails closed.

## 4. Assistant command gates

The v1 command map is closed, bounded and typed. It preserves the existing command allowlist and adds no arbitrary
command name. `assistant.credentials` returns opaque references only; it does not return a provider secret or activate
section 8.

| Command | Handler | Argument/body schema | Grant/permission |
| --- | --- | --- | --- |
| `assistant.credentials` | `namespaces.api`, route `credentials.read` | `AssistantCredentialsRequest` → `AssistantCredentialsResult` | `source.command.credentials`; never `credential.reference` |

The `credentials.read` route is a query-style POST with `query_only: true` and the separately reviewed justification
"return opaque references without changing provider, companion, account or session state".

Adding a command requires a new reviewed row, an exact handler route and a permissions-digest change; pattern matching
and user-supplied handler names are invalid. The manager MUST validate each argument and validate the body against the
closed schemas before dispatch, and MUST retain caller attribution through the command, confirmation, handler and
receipt. Result validation uses the same declared schema.

Read and write commands are explicitly classified. Every write requires its own section 7 confirmation bound to the
validated arguments/body and includes the bounded `request_key` from section 3 in that argument schema; an `allow` rule
cannot silently turn a write into an unconfirmed one. Shell execution and
arbitrary command forwarding are forbidden; a generic hub endpoint is also forbidden. There is no `command`, `args`,
`path` or `url` passthrough that lets the model choose an undeclared operation.

The credential-reference grant is separate from command permission. Command permission neither resolves a reference nor
injects a credential, and a credential reference never authorizes a command. Existing gate-declared variables, their
10-second RPC bound and the existing one-use confirmed-body gate remain outer limits, not replacements for this map.

## 5. Budgets and cancellation

Every route selects a host-defined profile. The effective ceiling is the smallest of the declaration profile, the
existing transport limit and any lower owner/manager limit. The v1 ceilings below are maxima, not targets. Existing RPC
frame limits remain unchanged and MUST never be increased to admit a media test.

| Resource | Ceiling/limit | Unit | What is counted |
| --- | --- | --- | --- |
| Headers, total | 32 | KiB | All admitted names and values, before dispatch; at most 64 headers and 4 KiB per value |
| Request URL | 8 | KiB | Escaped path plus query, including the reserved prefix |
| Query string | 4 | KiB | Encoded query bytes before schema decoding |
| Request body bytes | 1 | MiB | Decoded request content, including a write confirmation fingerprint input |
| Response body bytes | 1 unary; 64 media | MiB | Decoded response bytes selected for this request; HEAD carries no body |
| Encoded RPC frame | 8 | MiB | Complete JSON/base64 frame bytes; the pre-existing ceiling is not raised |
| Encoded media data frame | 64 | KiB | One media message including chunk envelope and base64 data |
| Media chunk | 32 | KiB | Decoded bytes in one ordered data-plane chunk |
| Concurrent requests | 8 per installation; 64 per manager | requests | Only admitted work with active execution or streaming transport; terminal `unknown`, deadline and cancellation release the slot |
| Reconciliation work | 4 per installation; 16 per manager | operations | Background recovery attempts for durable `unknown` rows, tracked separately from live request occupancy and receipt capacity |
| Queued work | 128 per installation; 512 per manager | requests | Admitted work not yet executing, including a reconnect backlog |
| Queue wait | 5 | seconds | Admission timestamp to execution start |
| CPU | 5 per request; 30 per installation-minute | CPU seconds | Runtime CPU attributed to handler execution, not client wait time |
| Wall-clock time/deadline | 120 | seconds | Admission through final chunk/result, including queue time |
| Aggregate per-installation work | 120 requests and 256 response MiB per rolling minute | requests and MiB per minute | Requests plus bytes emitted by the source, even if the caller disconnects |
| Aggregate manager work | 1 response GiB per rolling minute | GiB per minute | All source-handler response bytes across installations |

Request bytes are charged at admission; response bytes and CPU are charged when emitted/consumed. Encoded frame
accounting includes JSON field names, envelope overhead and base64 expansion. A declared response larger than its budget
is rejected before headers; an unknown-length media response MUST use bounded HTTP streaming and exact final-length
accounting. If an unknown-length response would exceed its aggregate budget, the manager aborts it before emitting the
next byte rather than truncating it successfully. No error may be hidden by silently lowering a declared content length.

| Condition | Caller-visible result | Stable source/RPC error code |
| --- | --- | --- |
| Schema, canonical path or media argument invalid | 400 JSON error, no dispatch | `invalid_request` |
| Header, URL or request body over budget | 431, 414 or 413 respectively, no dispatch | `payload_too_large` |
| Declared source response over budget | 502 JSON error before response headers | `response_too_large` |
| Installation queue/rate/aggregate exhausted | 429 with bounded `Retry-After` | `overloaded` |
| Manager capacity exhausted or receipt capacity full | 503, retry only when the operation is known safe | `unavailable` / `receipt_capacity` |
| Deadline, timeout or CPU budget exhausted | 504 if a response is still possible; streaming abort otherwise | `deadline_exceeded` |
| Stale generation or revoked grant | 409/403, no dispatch or result acceptance | `stale_generation` / `permission_denied` |

Disconnect, timeout and caller cancellation MUST propagate a cancellation frame with the request ID and generation.
Disable, uninstall and generation replacement revoke admission, cancel queued and active work, and reject their late
results. A late result or late callback is dropped and audited; it MUST NOT complete a newer request or resurrect an old
generation. Once the atomic `dispatched` transition in section 7 has allowed an external effect, cancellation changes
the caller wait, not the truth of that effect: the durable outcome becomes `unknown` until reconciled. Cancellation that
instead wins while the operation is still `reserved` is `cancelled`, never `unknown`.

If a media budget fails after headers, the manager aborts the connection instead of returning a clean, truncated 200/206.
The client can detect missing bytes from `Content-Length`/`Content-Range` or the missing terminal chunk of an
unknown-length response; no synthetic success or JSON error replaces an already-started body.

## 6. Media semantics

Media uses a bounded host/source control plane and data plane. It MUST NOT turn the 8 MiB buffered outbound fetch into an
inbound API, and it MUST NOT pass an unbounded socket to source code. Whole-body buffering or truncation is not feature
parity.

### 6.1 Control and data plane

| Message | Direction | Required semantics |
| --- | --- | --- |
| `open` | host → source | Request ID, generation, route, method, validated Range/conditions and selected response budget |
| `headers` | source → host | Status, exact content metadata, validators and selected length when known; an explicitly unknown length when it is not, validated before the first client byte |
| `credit` | host → source | Monotonic chunk credit; initially eight chunks (256 KiB), reduced for a slow client |
| `chunk` | source → host | Request ID, generation, sequence, decoded length and at most 32 KiB data; never ahead of credit |
| `end` | source → host | Final sequence and exact aggregate length; valid only after all selected bytes |
| `cancel` / `error` | either direction | Stable reason and terminal sequence; late or mismatched messages are rejected |

The data plane applies a per-chunk budget and an aggregate budget from section 5. Credit-based backpressure MUST propagate from the
client relay to the manager and then to the source. At most eight unacknowledged 32 KiB chunks exist per stream; sequence
gaps, duplicates, excessive chunks and credit overrun fail the stream. The manager never accumulates the whole response
to repair a misbehaving source.

### 6.2 GET, HEAD, Range and cache behavior

GET and HEAD are separate declarations over the same reviewed media metadata schema. A compliant handler MUST preserve:

- Full GET returns 200 with `Content-Type`, `Accept-Ranges` and declared cache policy. A known-length response carries
  exact `Content-Length`; an unknown-length response omits it and uses bounded HTTP chunked/streaming termination, with
  `end` carrying the exact aggregate length. An overrun or missing terminal chunk aborts rather than yielding clean
  truncation. HEAD returns the same status and representation headers (omitting unknown `Content-Length`) with zero body
  bytes and does not open the data plane. Range and 206 responses REQUIRE a known resource size and exact selected
  length before headers; a source that cannot supply them cannot serve that Range request.
- One valid byte Range returns 206 with the exact selected `Content-Length` and `Content-Range`. An unsatisfiable Range
  returns 416 with `Content-Range: bytes */<size>`; malformed and multiple ranges return 400 in v1. An integration that
  requires multipart ranges remains blocked until a later reviewed schema covers them.
- Cache validators `ETag` and `Last-Modified`, `Cache-Control`, `If-None-Match`, `If-Modified-Since` and `If-Range` keep
  their HTTP meanings, including 304 and revalidation before a partial response. A validator is bound to the selected
  representation and account; it is not an authorization handle.
- A selected full or partial response larger than the 64 MiB per-request budget is refused with 502 and the stable
  `response_too_large` code before headers. A client can request bounded ranges; a source MUST NOT silently truncate a
  larger response. If this cannot preserve an existing integration's behavior, that integration is not accepted for migration.

Partial-response cancellation stops further chunks, releases outstanding credit and closes the relay. Bytes already
emitted remain charged. An error before `headers` becomes the declared HTTP error; an error afterward aborts the stream
and is audited as incomplete, never as a successful end.

### 6.3 Host-fetched media and redirects

Approved origins are exact scheme/host/port tuples selected by the host, not by a media URL's text. Credential injection
is optional, separately granted and limited to the matching approved origin and credential handle. Source code never
receives the injected bearer value. A redirect to another origin is permitted only when that exact target tuple is
independently in the operation's approved-origin set; it receives no credential, cookie or identity header unless the
credential's own approved origin is that same tuple.

Every redirect target MUST match an approved origin before any dial, even when it is an otherwise harmless public HTTPS
origin. Every outbound read retains protected-target, checked-DNS, TLS and redirect enforcement in the checked runtime broker.
Each redirect (at most five) is a new checked-DNS and protected-target admission; HTTPS-to-HTTP downgrade, unapproved
ports, unresolved/illegal targets and redirect loops are denied. Redirects of GET/HEAD preserve the method and drop the
body; no redirect can change the approved account or credential origin. The existing 120-second ceiling covers the whole
redirect chain, not each hop separately.

## 7. Durable writes and confirmation

A trusted owner MUST verify an operation-specific confirmation before any write is admitted. Trusted-owner evidence is
the host's existing person-bound approval, never a hub/master token, an extension token, an assistant assertion or a
maintainer decision. If the current caller cannot produce that evidence, the write is refused.

### 7.1 Confirmation binding

| Field | Required rule |
| --- | --- |
| Operation and permitted use | Exact route/command and schema revision; one ticket permits one operation only |
| Caller and grant | Full verified caller context, target installation/contribution/account and process generation |
| Target | Exact canonical resource/target, not a class or prefix |
| Payload fingerprint | SHA-256 of the canonical exact route/command and HTTP method, validated path/query/body, every admitted validated request header, target and schema revision; the payload itself is not stored in the receipt |
| Request ID | The section 3 caller-replayable, host-derived idempotency identity and its embedded `not_after`; cannot be replaced by a retry |
| Ticket ID and owner | Single-use random ticket, stored by hash, and the trusted owner/device that approved it |
| Expiry | At most five minutes from issue and always checked again at reservation/admission |

Header names are lowercased and sorted, and their validated values are canonicalized by the declared schema, so every
representation-, precondition-, routing- or effect-affecting header (including `If-Match`, `If-None-Match` and
`Content-Type`) changes the fingerprint. The confirmation displays the operation, exact target and material validated
header/body changes. The manager recomputes the payload
fingerprint and revalidates current target state and any monotonic target/session revision immediately before admission.
A changed target, body, caller, account, generation, schema, current state or revision makes the ticket stale.

### 7.2 Reservation and receipt state machine

The reservation and ticket consumption MUST be persisted atomically in manager-owned Postgres before any external
effect. Unique keys cover request ID and ticket ID. The admission commit point is the successful reservation transaction;
only then may dispatch begin. The external-operation commit point is the handler's operation-specific commit receipt;
the manager marks `committed` only after durably saving that receipt.

A single atomic Postgres compare-and-swap, keyed by request ID, current state and process generation, MUST fence every
`reserved` → `cancelled` or `reserved` → `dispatched` transition. The dispatcher invokes the handler only after winning
the `dispatched` transition; the canceller claims proven no effect only after winning `cancelled`. The loser performs no
invocation and returns the durable state. A crash after the `dispatched` transition but before the RPC is `unknown`, not
a replay license.

| Current state | Event/condition | Next state | External-effect/replay rule |
| --- | --- | --- | --- |
| `new` | Valid confirmation and atomic reservation commit | `reserved` | Admission commit point reached; no earlier handler invocation |
| `reserved` | Cancellation wins the atomic no-dispatch compare-and-swap | `cancelled` | Proven no external effect; dispatcher loses and cannot invoke |
| `reserved` | Dispatcher wins the atomic dispatch compare-and-swap | `dispatched` | At most one manager admission; a crash after this marker is not a replay license |
| `dispatched` | Valid current-generation commit receipt persists | `committed` | External commit point and redacted receipt are durable |
| `dispatched` | A definite no-effect refusal persists | `failed` | No external effect; same-ID status may be returned |
| `dispatched` | Timeout, disconnect, lost response, cancellation or stale/late callback | `unknown` | Ambiguous outcome; no blind replay and late result is rejected |
| `unknown` | Manager reconciliation proves the external commit | `committed` | Recover the original receipt without another write |
| `unknown` | Trusted reconciliation proves no effect occurred | `failed` | Recovery may finish without a duplicate external effect |

A reservation save failure means no external effect and no handler invocation. A timeout, lost response or cancelled
call after the atomic `dispatched` transition is `unknown`, never assumed failure; cancellation that instead wins while
the operation is still `reserved` is `cancelled`. A new ID is never safe for an automatic retry or replay. An
automatic retry under a new ID and a retry with a changed fingerprint are forbidden; either could cause duplicate
writes. Before `not_after`, repeating the original caller idempotency key and fingerprint with a current grant derives
the same request ID and returns its durable state or performs manager-led reconciliation; it does not blindly dispatch
again. After `not_after`, the same request is rejected `request_expired` without dispatch even when no tombstone remains.

A write declaration MUST define an idempotency/reconciliation schema keyed by request ID. Without a trustworthy way to
distinguish committed from no-effect, the operation remains `unknown` and requires owner-visible resolution. Generic
exactly-once external effects are not claimed. A late result from a replaced generation cannot mark the operation
`committed`; it is rejected and the stored operation remains `unknown` pending trusted reconciliation.

### 7.3 Disable, restart, retention and extension state

Disable, uninstall and revocation cancel `reserved` work only when they win the same atomic compare-and-swap proving it
was never sent; `dispatched` work becomes `unknown`. Generation replacement never transfers a reservation to a new
process. On manager restart, every `reserved` row from the previous generation MUST win only the transition to
`cancelled`: no dispatch marker exists, so no effect is proven, and a fresh confirmation and a new idempotency key are
required. Every
`dispatched` row without a final receipt is recovered as `unknown` first. A previous-generation `reserved` row MUST NOT
be dispatched after restart. Real Postgres manager restart recovery is required, including crash points on both sides
of the reservation and dispatch markers.

Receipt retention is bounded and receipts are redacted, with three independent budgets:

- At most 1,024 unresolved `reserved`/`dispatched`/`unknown` rows per installation. Only this durable-operation cap
  rejects new writes with `receipt_capacity`; unresolved rows are never evicted, and reaching a terminal state frees
  their slots. Durable `unknown` rows do not occupy live request concurrency and may continue bounded reconciliation.
- At most 1,024 terminal audit receipts per installation for 30 days. Older terminal receipts may be compacted or
  evicted after their live replay tombstone is recorded; these audit receipts are not replay authority.
- At most 4,096 live request-ID/fingerprint replay tombstones per installation, each retained until its embedded
  `not_after`, never for 90 days. The replay horizon is exactly the section 3 expiry, at most seven minutes. A full live
  tombstone cap rejects new writes only until the oldest entries expire, a bounded wait of at most seven minutes.

After `not_after`, the request-ID expiry itself requires `request_expired` rejection before lookup, so an expired
tombstone can be deleted without making an old retry indistinguishable from a new operation. Expiry never authorizes a
new request under the same ID. Manager reconciliation of an `unknown` row may continue after caller replay expiry, but
no caller retry can dispatch it again. Receipts store category, target binding, fingerprint, state, issue/expiry
timestamps, budget usage and redacted result code, not payloads or secrets.

The host reservation/receipt composes with the extension-owned durable-state contract from #1187 but is not guest state.
Extension memory is not durable, and extension-owned durable state is a reconciliation cache, not proof of owner
confirmation or an external commit. Existing `client_ops` and installation replay-receipt precedents may supply atomic
storage machinery, but neither alone satisfies this state machine.

## 8. Independent subcontract: managed client credential source

This subcontract requires its own explicit grant, `managed-credential/v1`, reviewed schema, negative tests and
acceptance. It is independent of the core handler contract: core API, command, account or handler authority does not
inherit or authorize credential access.

```ts
interface ManagedCredentialGrantV1 {
  version: "managed-credential/v1";
  provider_id: string;
  companion_profile_id: string;         // exactly one approved companion/profile
  account_id: string;
  credential_fields: string[];          // exact host-side injection allowlist; never guest-visible
  handle_only: true;
}
```

An owner-approved, host-owned provider MUST be bound to exactly one approved companion/profile and one reviewed account.
Source selection is an explicit owner choice among host-enumerated providers; guest code cannot discover deployment
credentials, probe companions or fall back to a different identity. Existing generic secret-provider references can
supply storage/resolution machinery, but they do not by themselves satisfy this subcontract.

The provider supplies only the exact credential fields allowed by `credential_fields` to the host's injection point;
source-visible credentials are limited to an opaque handle per approved use. That handle is audience/origin-bound,
short-lived and tied to installation,
account and process generation; it is not a bearer credential revealed to source. Raw credential fields MUST NOT enter
source memory, logs or receipts. Revocation therefore blocks every later source handle use: only the host ever holds the
upstream secret.

The host, not the guest, performs refresh after expiry and before an approved upstream call. An identity change revokes all
handles and requires fresh owner approval; silently selecting a new account is forbidden. Revocation, companion removal,
provider disable and generation replacement invalidate handles immediately and cancel future injection. Refresh failure
never falls back to an expired secret or another profile.

Docker socket access, general container inspection, arbitrary environment or filesystem reads, shell execution and
`nodes.call` as credential-discovery substitutes are forbidden. Manual secret entry remains an option, but manual entry
is not automatic-refresh parity and MUST NOT be presented as preserving that feature.

## 9. Independent subcontract: sensitive session API

Rename and execution-mode changes each require a separate explicit grant and a reviewed schema:
`session.rename.source/v1` and `session.mode.source/v1`. Each schema is bounded. These operations are independent of the
core contract and of each other;
core API authority, `agents.control`, `agents.permission_mode`, ordinary session read scopes and ordinary session
control scopes do not authorize either operation.

Current legacy behavior remains unchanged by this design: #2245 places rename in broad `agents.control` and allows a
restricted `agents.permission_mode` only for extension-opened conversations. Those standing grants do not satisfy the
per-operation source-handler ticket rules below. This subcontract MUST NOT add modes to a broad existing scope or treat
that legacy scope as owner confirmation. A later migration of legacy scopes needs its own compatibility and owner
review; it is not silently performed here.

### 9.1 Rename

Rename is separate from mode changes and targets an exact `(manager, machine, session)` tuple. The rename operation's
bounded schema accepts only `title`: valid UTF-8, NFC-normalized, trimmed, 1–200 characters, with no control characters
or path syntax. The manager revalidates target ownership/existence and binds the section 7 operation confirmation to that exact rename
target, its monotonically changing title/session revision, current title and requested title. Target substitution is
refused. The actual rename mutation MUST either serialize admission and mutation with every other title writer under
the same lock or carry an atomic expected-current-revision compare-and-swap through the manager/node/session write. A
title revision change between admission and mutation makes the ticket stale, including an A→B→A title sequence; a value
CAS on the title alone is not an ABA-safe fence. A revision mismatch is a definite no-effect refusal and an
indeterminate result is `unknown`. A host/session API without that atomic fence MUST leave source rename blocked.

### 9.2 Execution mode

Mode elevation REQUIRES genuine explicit authorization from the account owner/user and an operation-specific, expiring
ticket. The ticket binds the full caller, exact session, monotonically changing session revision, current mode,
requested mode, request ID, permitted use
(`session.modes` once), target installation/generation and the approving owner/device. It expires at most 60 seconds
after issue and is stored/consumed by hash with the durable reservation.

The manager reads current mode and session revision again at admission, but that check alone is insufficient. The
actual session mutation MUST either serialize admission and mutation with every other mode writer under the same lock
or carry an atomic expected-current-revision compare-and-swap precondition through the manager/node/session write. A
revision change between admission and mutation makes the ticket stale, including an A→B→A mode sequence; a value CAS
on current mode alone is not an ABA-safe fence. A revision mismatch is a definite no-effect refusal, while an
indeterminate mutation result is `unknown`.
A host/session API that cannot provide that atomic fence MUST leave source mode changes blocked.

A stale ticket is refused, including after another mode change;
target substitution is refused; ticket reuse, caller substitution and requested/current-mode substitution are refused.
Automatic elevation is forbidden, and modes are not enabled by default. There is no assistant-initiated self-approval,
time-window allow-all or inferred consent from starting a session. A maintainer's decision is not the account owner's
consent or authorization.

## 10. Isolation and audit

The manager MUST reject a cross-extension handle. It MUST reject a cross-account handle. It MUST reject a stale
generation. It MUST reject a forged confirmation. It also rejects caller/installation/contribution substitution, ticket
reuse, account-selected cache validators used across
accounts and receipts written by the wrong process generation. Failure to load authoritative binding or grant state is
closed, never an anonymous or full-access fallback.

Audit records are bounded and use redacted categories:

| Category | Allowed fields |
| --- | --- |
| Caller/grant lifecycle | Caller kind and stable host IDs, installation/contribution/account binding, grant ID, generation, revocation reason |
| Request/route | Request ID, route/command ID, canonical method, schema revision, payload fingerprint; never the raw payload |
| Budget usage | Counted header/body/frame/chunk bytes, queue/concurrency occupancy, CPU/deadline and overload code |
| Outcome | Reserved/dispatched/committed/failed/unknown/cancelled, confirmation ticket hash, late-result reason and reconciliation result |
| Credential provider | Provider/profile/account binding, approved field names or handle ID, refresh/revocation outcome; never values |

Credentials, message bodies and media content are not audited. The audit MUST NOT contain bearer values, cookies,
credential fields, request/response bodies, titles, media bytes, URLs with sensitive query data or raw upstream replies.
Redaction tests use synthetic secrets and prove their absence from manager logs, receipts, errors and captured traces.

## 11. Implementation acceptance matrix

These are downstream executable acceptance cases, not tests claimed by this design PR. Every implementation PR MUST map
its exact commands and results to these IDs; a documentation conformance pass is not runtime evidence. Review of sections
2–10 precedes implementation. Subcontract rows require independent review and cannot be inherited from a passing core
suite.

| Case ID | Scenario | Expected assertion | Contract section | Test level/evidence |
| --- | --- | --- | --- | --- |
| NEG-FORGED-PRINCIPAL | Send forged principal/caller identity headers and bearer values | Headers are stripped; only verified host context reaches the handler; no bearer reaches source | 2, 3 | Hub→manager→adapter integration with captured context |
| NEG-UNDECLARED-ROUTE | Call an undeclared route, method, HEAD or OPTIONS | 404/405 before source dispatch and no implicit method appears | 2 | Manager route/admission tests |
| NEG-DECLARATION-CALLERS | Register omitted, empty, duplicate or unknown caller classes, then call outside the declared set | Registration/admission fails closed; only the nonempty declared `app`/`assistant`/`extension` intersection proceeds | 2, 3 | Declaration schema and caller-class admission tests |
| NEG-SAFE-METHOD-WRITE | Declare GET or HEAD with `effect: "write"`, or a write without `reconciliation_schema` | Registration rejects both declarations; no safe-method mutation or schema-less recovery path exists | 2, 7 | Discriminated route-declaration tests |
| NEG-UNSAFE-METHOD-READ | Declare PUT, PATCH or DELETE with `effect: "read"` | Registration rejects the declaration and admission cannot bypass write confirmation, idempotency or reconciliation | 2, 7 | Unsafe-method read declaration and admission tests |
| NEG-POST-READ-JUSTIFICATION | Declare a POST read without `query_only` or `read_justification`, or contradict its no-side-effect review | Registration/admission rejects it; only a separately reviewed query-only POST read can bypass write confirmation | 2, 4, 7 | POST-read schema, justification-review and side-effect tests |
| AUTH-HEADER-ALLOWLIST | Send declared Range/conditional/precondition/idempotency headers beside forged credentials and identity headers | Every schema-allowlisted value survives typed dispatch while every credential, identity and hop-by-hop header is stripped | 3, 6, 7 | Relay/header capture and validation tests |
| NEG-PATH-COLLISION | Register a path collision and try encoded separators, dot segments, `%2e%2e`, `%5c`, `%00`, percent-encoded invalid UTF-8 and double encoding | Registration is atomic/fail-closed; decoded-segment revalidation returns 400 with no dispatch, and normalization bypasses never select another route | 2 | Declarative registry and escaped-path relay tests |
| NEG-WRONG-BINDING | Use the wrong installation and wrong account for a valid route/reference | Both bindings are rejected and no source or upstream effect occurs | 2, 3, 8 | Manager admission matrix |
| NEG-STALE-GENERATION | Invoke or answer with a stale process generation | Invocation/result is rejected and cannot complete a replacement request | 2, 3, 5 | Runtime generation replacement tests |
| NEG-CROSS-HANDLE | Present a cross-extension handle and a cross-account handle | Both are rejected; possession never becomes authorization | 3, 10 | RPC handle isolation tests |
| NEG-BAD-TICKET | Use a bad, stale, forged, expired or already-used ticket | Reservation/admission fails closed and ticket consumption remains single-use | 7, 9 | Confirmation and Postgres uniqueness tests |
| NEG-TARGET-SUBSTITUTION | Approve one session/target then substitute another session or target | Target substitution is refused with no external effect | 7, 9 | Exact-target admission tests |
| NEG-FORGED-CONFIRMATION | Forge a confirmation, approver or payload fingerprint | Confirmation is rejected; reservation and receipt are unchanged | 7, 10 | Owner-binding and tamper tests |
| NEG-HEADER-SUBSTITUTION | Approve a write with one `If-Match`/`Content-Type`, then substitute another validated header | Header changes alter the fingerprint; the stale confirmation is refused with no external effect | 3, 7 | Canonical-header confirmation and admission tests |
| NEG-CROSS-METHOD-REPLAY | Reuse the same caller idempotency key, canonical path, schemas, headers, body and target with a different HTTP method or command | Exact route/command and method identity changes both the derived request ID and fingerprint; cross-method replay cannot return the original receipt or dispatch a different operation | 3, 7 | Cross-method idempotency and replay-isolation tests |
| NEG-MEDIA-TARGET | Request a denied media target, protected address or checked-DNS failure | Denied media target has no dial, credential injection or media bytes | 6 | Egress broker adversarial fixtures |
| NEG-REDIRECT | Follow unapproved public HTTPS, approved cross-origin, downgrade, loop and excessive redirects | No unapproved target is dialed; every approved hop is reauthorized; credentials never cross origin and the chain stays bounded | 6 | Redirect-chain integration tests |
| COMMAND-CREDENTIALS | Call `assistant.credentials` through the typed `namespaces.api` route with valid and invalid arguments and grants | Typed dispatch and argument/result schemas hold, caller attribution survives, only opaque references return, command and credential-reference grants stay separate, and the existing 10-second RPC bound holds | 4, 8 | Positive assistant RPC/handler integration plus timeout evidence |
| NEG-OVERSIZED-FRAME | Exceed URL/header/request-body/chunk limits and send an oversized encoded RPC frame | Correct 413/414/431/overload result; existing 8 MiB ceiling is unchanged and memory stays bounded | 5, 6 | Encoded-frame, adapter and relay boundary tests |
| NEG-RESPONSE-TOO-LARGE | Declare a source response larger than the response budget | 502 `response_too_large` before response headers; 413 is used only for oversized request content | 5, 6 | Response-budget gateway tests |
| BUDGET-CONCURRENCY-QUEUE | Saturate installation/manager concurrency and queues beyond their wait ceilings, then finish calls as `unknown` | Occupancy never exceeds 8/64 active or 128/512 queued; excess work returns stable 429/503 within five seconds; `unknown` releases live slots while its durable row remains bounded | 5, 7 | Deterministic occupancy, queue-wait, overload and release tests |
| BUDGET-RECONCILIATION | Saturate background reconciliation independently of live requests | Reconciliation stays within 4/16 operations, consumes no live request slot and observes unresolved receipt capacity | 5, 7 | Reconciliation scheduler and occupancy tests |
| BUDGET-CPU-DEADLINE | Exceed per-request and per-installation CPU and the 120-second request/deadline budget | Stable 429/504 results and cancellation occur at the measured ceiling; late results are rejected | 5 | Instrumented CPU/deadline and cancellation tests |
| BUDGET-AGGREGATE-OVERLOAD | Exceed rolling installation request/byte and manager byte aggregates | Stable 429/503 overload results preserve the 120-request, 256-MiB and 1-GiB ceilings; accounting includes disconnected responses | 5 | Rolling-window and aggregate-overload tests |
| NEG-LATE-CALLBACK | Deliver a late callback/result after cancellation or replacement | Late result is dropped, audited and cannot mutate a newer request/receipt | 5, 7 | Deterministic callback reordering tests |
| CRED-REFRESH-REVOKE | Exercise provider selection, host-only field injection, credential refresh after expiry, identity change and revocation | Only the approved companion/profile works; no raw credential reaches source; identity change re-approves; revocation blocks every handle reuse | 8 | Independent provider integration and negative suite |
| SESSION-RENAME-MODE | Attempt rename, mode elevation, stale tickets and ordinary-scope substitution | Exact rename schema/target holds; each mode change needs a fresh owner ticket and no default elevation occurs | 9 | Independent session subcontract suite |
| SESSION-MODE-CAS | Change the session mode again after ticket admission but before the actual mutation, including an A→B→A sequence | The atomic expected-session-revision precondition refuses the stale write with no effect even when the visible mode value returns to A; an indeterminate result becomes unknown | 7, 9 | Mode-writer revision and ABA race tests |
| SESSION-RENAME-CAS | Change the session title again after rename admission but before the actual mutation, including an A→B→A sequence | The atomic expected-title/session-revision precondition refuses the stale rename with no effect even when the visible title returns to A; an indeterminate result becomes unknown | 7, 9 | Title-writer revision and ABA race tests |
| STREAM-GET | Stream known- and unknown-length full media and vary client read speed | 200 metadata is exact, known lengths use `Content-Length`, unknown lengths terminate by bounded HTTP chunks with exact final accounting, and memory is independent of response size | 5, 6 | Real relay streaming tests |
| STREAM-RANGE | Request a valid single Range and an unsatisfiable Range | Valid single range is exact 206 with validated Range/conditional headers surviving the relay; unsatisfiable is 416 with size | 3, 6 | `http.ServeContent`-style parity and source handler tests |
| STREAM-RANGE-REJECT | Send malformed and multiple Range values | Each returns 400 before source dispatch with no full 200, partial 206 or multipart success body | 6 | Explicit malformed/multiple rejection tests |
| STREAM-HEAD | Run HEAD against every media fixture | GET-equivalent metadata/status, zero body and no source data-plane chunks | 6 | HEAD/GET parity tests |
| STREAM-CACHE | Exercise ETag, Last-Modified, conditional GET and If-Range | Cache validators produce correct 304/partial behavior without cross-account reuse | 6 | Cache-validator matrix |
| STREAM-ERROR | Fail before headers, during a stream and when an unknown-length response crosses its aggregate cap | Early typed error is correct; late error/overrun aborts rather than yielding clean truncated success or a terminal chunk | 5, 6 | Fault-injected relay tests |
| STREAM-BACKPRESSURE | Slow and pause the client while a source produces media | Credit bounds in-flight chunks; backpressure reaches the source and queues stay bounded | 5, 6 | Instrumented slow-client tests with allocation evidence |
| STREAM-DISCONNECT-CANCEL | Disconnect during full/partial responses and cancel explicitly | Cancellation reaches source, credit is released and no late end succeeds | 5, 6 | Disconnect/cancellation race suite |
| DURABLE-SAVE-FAILURE | Fail reservation save immediately before dispatch | Reservation save failure has no external effect; upstream invocation count is zero | 7 | Postgres failure injection plus synthetic upstream counter |
| DURABLE-UNKNOWN-REPLAY | Lose the response after admission, then retry the original caller idempotency key when no internal request ID was received | The authenticated original key derives the same ID; ambiguous outcome stays unknown and reconciles; a new key/ID replay is refused and no duplicate write occurs | 3, 7 | Lost-acknowledgement and caller-visible idempotency fault injection |
| DURABLE-CANCEL | Race cancellation against the atomic `reserved` → `dispatched` transition and cancel after that transition | One atomic compare-and-swap winner is durable; `reserved` cancellation is `cancelled` with no invocation, while post-`dispatched` cancellation becomes `unknown` and reconciles honestly | 5, 7 | Concurrent dispatch/cancel and crash-point matrix |
| DURABLE-RETENTION | Fill unresolved, terminal-receipt and live-tombstone capacities, then age entries past their horizons | Only unresolved rows hold the durable write cap; terminal audit rows compact; live tombstones expire within seven minutes and no replay occurs | 3, 7, 10 | Retention, capacity, expiry and log-redaction tests |
| DURABLE-EXPIRED-REPLAY | Retry an old ID/key after its replay tombstone has expired and been deleted | Embedded `not_after` returns `request_expired` before lookup and no second reservation or dispatch occurs | 3, 7 | Post-expiry replay test with an empty tombstone store |
| RESTART-POSTGRES | Restart real Postgres around reservation, dispatch and receipt commits | Durable receipt/state recovers without replay; failures are unknown rather than falsely failed | 7 | Real Postgres crash/restart suite |
| RESTART-MANAGER | Restart the manager at every reservation/dispatch/unknown transition | Previous-generation `reserved` rows become cancelled without dispatch and need fresh confirmation; `dispatched` rows recover as unknown; durable receipts survive without duplicate effect | 7 | Manager process restart against real Postgres |
| E2E-INSTALLED-SOURCE | Install a source profile on an isolated development hub with synthetic data and exercise its API, assistant command, media and writes end to end | Captured and inspected authentication, assistant-command, media and confirmation flows match sections 3, 4, 6 and 7, including opaque-only credential references and no real account/data | 2–10 | Installed-package E2E plus captured relay/provider/Postgres evidence reviewed by a person |

Source profiles and isolated-development acceptance precede any default-package change. Synthetic routing tests alone are
not end-to-end acceptance. Production activation, marketplace publication and signing remain outside every row above.
