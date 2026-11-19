# Portable configuration forms

This is the version 1 data contract for module-supplied settings, first used by
Settings › Labs (#9877). Web, iOS and Android can render the same JSON with their
own design-system controls. The generic form does not know what an assistant,
hub, browser, experiment or storage key is. The module supplies a source already
bound to the resource being configured.

The schema is `configuration.schema.json`: `Document`, `Update` and `Error` are
named definitions under `$defs`. `all-types.json` is a synthetic complete response,
not a set of enabled product features. `validation-cases.json` holds shared
write-validation cases. The Go types and checks live in
`server/internal/configuration`; the Web implementation uses the same contract.
Native rendering is not implemented by this change.

## Read and display

A `Document` has `schema_version: 1`, an opaque resource `id`, an opaque
`revision`, a localized `title`, optional `description`, ordered `groups`, and a
`values` map. Each group has `id`, `title`, optional `description` and ordered
`fields`. Group IDs are unique within the document; field IDs are unique across
all groups. IDs, revision, titles and option values must contain a non-whitespace
character. Their bytes are preserved, not trimmed or case-folded.

Every field has `id`, `kind`, `title` and a required typed `default`. It can also
have `description`, `read_only`, `disabled_reason` and ordered `details`, each a
`{title, description}` pair. A nonempty disabled reason requires `read_only: true`.
Details carry module-owned scope, activation and rollback explanations without
requiring the renderer to understand those concepts. All text is plain text, not
HTML, code or a localization key. The source localizes its own text for the caller;
each client localizes generic controls and validation messages through its resources.

| `kind` | Value and default | Required options member | Presentation and constraints |
| --- | --- | --- | --- |
| `toggle` | JSON boolean | None | Switch; `false` is a value, not absence. |
| `text` | JSON string | `text` | `multiline`, `placeholder`, `min_length`, `max_length`. Empty text is valid when its minimum permits it. Length counts Unicode scalar values, so an emoji is one character rather than two UTF-16 units. |
| `select` | JSON string | `select` | Nonempty `options`, optional `presentation`: `list` (default), `dropdown` or `segmented`. Only a declared option value is valid. |
| `multiselect` | JSON array of strings | `multiselect` | Nonempty `options`, optional `min_selected` and `max_selected`; unique declared values only. Checkbox list; the source preserves supplied order, while membership determines which boxes are checked. |
| `number` | JSON number | `number` | `integer`, `minimum`, `maximum`, positive `step`. Finite values within ±9,007,199,254,740,991; `step` is an input increment hint, not a divisibility constraint. |

Each option is `{value, title, description?}`. Option values are unique within a
field. Exactly the options member selected by `kind` is present; a toggle has no
options member. Bounds are nonnegative counts or finite numbers, with minimum no
greater than maximum. A multiselect's minimum cannot exceed its option count.
Defaults must satisfy the same constraints as writes.
The Go validator checks a JSON number's portable magnitude and integrality
before converting it to a floating-point value.

`values` contains a valid effective value for **every** declared field and no
other keys. A source resolves missing stored overrides to defaults before
returning the document. `null` is never an effective value or a request to reset.
Read failures are errors, not empty settings or invented defaults. Optional
`notices` maps field IDs to `{kind: "info" | "warning", message}`; a source can
explain a recognized legacy-value fallback without exposing raw stored data.

The document and an update each fit within 64 KiB of encoded JSON. Arrays and
maps are emitted as `[]` and `{}`, not `null`. The JSON Schema checks structure;
the Go/Web checks also enforce uniqueness, corresponding values, option
membership, bounds and update semantics that require the current document.

## Change and reset

An update identifies the exact snapshot it was edited from:

```json
{
  "schema_version": 1,
  "id": "example-preferences",
  "revision": "revision-1",
  "changes": { "compact": true, "sections": ["summary", "files"] },
  "reset": ["label"]
}
```

The update member names above are case-sensitive. Only named fields change.
Omitted fields remain untouched, including fields an older client cannot render.
`reset` removes an override; the current default is
then effective. It does not pin today's default as a stored user choice. At least
one change or reset is required. Duplicate resets, a field named in both operations,
unknown IDs, read-only fields and invalid values reject the **whole** update.
There is no implicit coercion or silent replacement of invalid writes with defaults.

The owning writer checks resource identity, revision, authorization, definitions
and values **inside its atomic commit boundary**, then returns a complete canonical
`Document`. The revision changes with values, definitions or permissions; the source
decides its representation. A preflight read followed by an unconditional write is
not an implementation of this contract. `configuration.CheckUpdate` and
`configuration.ApplyUpdate` help a provider validate within its transaction;
the latter returns a detached effective-value map and performs no persistence.
The provider still sees `reset` to remove its stored overrides.

The form retains text and other dirty edits until a write succeeds. A failed
write does not report success or erase the draft. A conflict requires a read and
review of the draft against the new snapshot, with no automatic retry. A response
for a previously selected source must not replace the new source's state.
Changing the page location must not unmount a background chat or discard its input.

## Injected transport and authority

Local and remote sources expose the same document/update contract. A source owns
read, save, subscriptions, binding identity, persistence and any required access
checks. A component receives that source; it does not discover an owner from a
field, call an arbitrary URL in a response, or select a storage scope.

For remote sources, `configuration.NewHandler` supplies **GET** (read document)
and **PATCH** (atomic update, returning the resulting document) at an endpoint
chosen by the module. Its injected `Resolver` authorizes each request and returns
an already-bound `Provider`. The provider rechecks permissions at commit as
necessary. The handler does not register a global route, a new database table or
an assistant-specific setting service. Unknown request members and malformed
JSON are refused. Responses use `Cache-Control: no-store`.

The first real integration, terminal rendering, is browser-local and uses a local
source. It has no dependency on this HTTP handler or on a new server endpoint.
A server-backed module can mount the adapter within its existing authenticated
routes and supply its own transactional storage. Tests exercise the actual GET /
PATCH adapter with an isolated transactional provider; no production server
configuration is changed or simulated as a shipped trial.

Errors follow the API's `error` / `code` body, with optional `field_errors` entries
`{id, code, message?}`. The client localizes a field code unless a module supplies
its own localized business message. Unknown errors remain visible.

| Error code | HTTP status | Client behavior |
| --- | --- | --- |
| `configuration_bad_request` | 400 | Preserve the draft; the request cannot be decoded. |
| `configuration_version` | 400 | Block writes; the document/update format is unsupported. |
| `configuration_too_large` | 413 | Preserve the draft and explain the size limit. |
| `configuration_invalid` | 422 | Show field errors and keep edits. |
| `configuration_conflict` | 409 | Reload explicitly and reconcile edits before saving. |
| `configuration_forbidden` | 403 | Explain the restriction; do not attempt another target. |
| `configuration_not_found` | 404 | The bound resource is unavailable. |
| `configuration_unavailable` | 503 | Keep edits; reload before trying again. The outcome of a lost write may be unknown. |

Field codes are `type`, `option`, `duplicate`, `min_length`, `max_length`,
`min_selected`, `max_selected`, `integer`, `minimum`, `maximum`, `range`,
`unknown`, `read_only` and `unsupported`. Read-only flags only describe permissions;
they never authorize a write. This contract does not carry secret values or provide
secret replacement semantics; secret-entry flows retain their existing APIs.

## Compatibility and verification

A client with an unknown document version blocks editing. Within a supported
version, an unknown field kind stays visible as unsupported/read-only and its raw
value is preserved; partial updates to known fields never delete it. Readers may
ignore extra metadata they do not understand. Producers validate their known
definitions before emitting them. New semantic control kinds require client
support; an incompatible change to existing semantics requires a new version.

Local adapters preserve established storage keys and encodings. The terminal
adapter retains `persona.terminal.webgl` and the legacy value `1`; clearing its
override restores Standard. Its activation and automatic fallback behavior stay
owned by the terminal feature. Removing a field from the form does not itself
delete saved state or authorize a migration.

Run `go test ./internal/configuration` from `server/` and
`python3 scripts/test/test_configuration_schema.py` from the repository root.
The shared example and validation cases are also inputs to the Web tests. The
HTTP tests cover rejected writes, atomic mixed updates, preservation/reset,
resource/revision conflicts, concurrent writers, authorization, malformed and
oversized requests, canonical responses and localized failures. Native UI tests
belong to the later client integrations; this change makes no native execution claim.
