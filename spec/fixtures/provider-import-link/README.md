# Provider import links

Cases for provider import links (#5420): a provider's settings as cindy's v1 import JSON, carried in a link. The hub
reads every link (`POST /api/v1/launchers/import/parse`, `server/internal/assistant/models_import_link_test.go`); the
web app builds the links it copies (`web/src/lib/provider-link.test.ts`). Rules: `docs/integrations/launchers.md`
("Import links").

- `encode`: a provider as `GET /api/v1/launchers` lists it (and a key pasted into the copy dialog, `key`) and the
  payload and fragment the web builds for it, or `payload: null` when it builds none. The payload is serialized with
  `JSON.stringify` key order, no spaces, then base64url without padding.
- `links`: a text pasted or opened, the format the hub reads it as (`link`, or `""`), the drafts it gives (the fields
  compared: `kind`, `api`, `base_url`, `display_name`, `auth`, `api_key`, `model`, the model IDs in order, and the
  draft's notes when listed) and the notes about the text as a whole. The first two links are the first two `encode`
  cases opened again. The `persona://` case is the same cindy v1 payload and draft as the cindy link from cindy's docs,
  opened by the iOS and Android apps (#5428).

Every address is under `example.invalid` and every key is a placeholder.
