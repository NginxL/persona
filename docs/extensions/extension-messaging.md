# Extension point `messaging`: message sources (#2138)

The App's **Messages** entry (conversation list, chat, sending, pinning, threads, reactions, read state…) is provided by extensions: the App has just one set of **generic native messaging components**,
and the **data and actions** come from an extension that contributes `messaging`. The first source is Lark (the extension that connects to Lark); other IMs later (Telegram…) use the same protocol.

Why not a web page / shipped code: iOS enforces code signing at runtime (App Store, TestFlight and ad-hoc alike), so native code cannot be downloaded from an extension and run. Web / JavaScript can be shipped,
but a heavily interactive UI like chat feels bad that way, and the extension code would run on the phone with access to the App's credentials, which conflicts with "extensions don't run code in the App" (`docs/extensions/extensions.md` ("Declarative UI")).
So this follows the same idea as declarative UI: **the extension supplies only data and actions, and the App draws them with its own design system**; the "components" here are just message-level (conversations, messages, segments, composer modes).

This document is the protocol spec; the data format is defined by `schema/extension/messaging.schema.json` (the types for all four clients are generated from it, see `schema/extension/README.md`). Type names here leave out the `Messaging` prefix that the schema and the generated types carry: `Outgoing` is `MessagingOutgoing`.
The plan and phases are in #2138; the Lark implementation of this protocol lives in the repository of the extension that connects to Lark.

## 1. Overview

| Who | Does what |
| --- | --- |
| Extension | Contributes `messaging` in its manifest (one entry per source); implements the endpoints below on its HTTP side; sends `messaging.changed` when data changes; all vendor-specific handling (decoding, encryption / decryption, card conversion, display settings) stays in the extension |
| hub | Lists sources; authenticates `/api/v1/messaging/{source}/…` and forwards it to the extension; checks the extension's answers against the schema and withholds non-conforming ones from the App; wraps writes in send receipts; pushes events to online Apps |
| App | Shows the **Messages** entry when there is a source (name and icon come from the source); conversation list, chat, composer, outbox, drafts, cache, offline and notifications are all generic; the source's `capabilities` decide which actions are shown |

**Not in this protocol**: settings pages use the existing declarative UI (`ui`'s `extension_page`, pointed to by the source's `settings_ui`); Inbox items stay with connectors (an item carries the conversation's location, see "9");
assistant tools and knowledge-base archiving stay with connectors / `assistant`.

## 2. Manifest

```json
"contributes": {
  "messaging": [{
    "id": "lark",
    "name": "Lark",
    "icon": "chat",
    "capabilities": ["accounts", "pinned", "mentions", "attachments", "attachment_replies", "threads", "topics",
                     "reactions", "read_receipts", "chat_pins", "profiles", "url_previews", "documents", "doc_comments",
                     "edit_history", "translation"],
    "composer_modes": [
      {"id": "sealed", "title": "Encrypted message", "icon": "lock", "attachments": false, "mentions": false, "prepare": true}
    ],
    "settings_ui": "display",
    "links": [{"hosts": ["*.feishu.cn", "*.larkoffice.com", "*.larksuite.com"], "opens": "document"}]
  }]
}
```

| Field | Meaning |
| --- | --- |
| `id` | Source id, `^[a-z][a-z0-9_]{0,31}$`, globally unique (an extension that clashes with another will not install). It is also `{source}` in endpoint paths, `source` in events, and the namespace for App cache and drafts |
| `name` | The entry's name (tab bar, shell, settings), 1–12 characters |
| `icon` | One of the fixed icon set (the icon set in `docs/extensions/extensions.md` ("UI version 3")); unknown ones draw `chat` |
| `capabilities` | What this source supports (see "3"); the App shows only supported actions. Neither the hub nor the App rejects unknown ones (from newer extensions); the App ignores them |
| `composer_modes` | Composer modes besides "normal" (see "7"); at most 6 |
| `settings_ui` | The id of a `ui` contribution (`extension_page`) of the same extension: the **Settings** button at the top right of the Messages page and this source's entry under **Settings › Connectors** open it. The page carries no account: a source with several accounts puts them in sections on one page |
| `links` | Links to these hosts tapped in the App are handed to this source: `opens` is `document` (document reader, needs `documents`), `post` (post, needs `posts`) or `ui:<id>` (a full-page UI of the same extension, with the link in `context.item.url`); `paths?` can further restrict by path prefix. **The first match in list order wins** (put specific ones first). `document` and `post` hand the whole link to the extension: `GET documents?url=<link>`, `GET posts/<escaped link>` (see "10") |

Requirements: `runtime.http` (endpoints go to the extension's HTTP side, as with `api`), and `activation_events` includes `on_startup` (needed for live push).
One extension can contribute several sources (for example, a work account and a personal account on two separate systems); the v1 App shows only the first enabled source; a multi-source entry comes later.

## 3. Capabilities (`capabilities`)

| Capability | Enables |
| --- | --- |
| `accounts` | Several accounts under one source: `GET accounts`, every endpoint takes `account`, account switcher at the top of the list |
| `pinned` | Pinned conversations: `GET pinned`, pinned bar at the top of the list |
| `mentions` | @-mentions in the composer: `GET conversations/{id}/members` |
| `attachments` | Sending images and files |
| `attachment_replies` | Images and files can also quote a message or go into a thread |
| `threads` | Message threads (reply in thread, thread details): `GET messages/{id}/thread` |
| `topics` | Topic groups (one card per topic) |
| `reactions` | Emoji reactions: `GET emojis`, `GET reactions/quick`, `POST messages/{id}/reactions` |
| `read_receipts` | Read counts on messages I sent: `POST conversations/{id}/read-states` |
| `chat_pins` | In-chat Pins: `GET / POST conversations/{id}/pins` |
| `profiles` | Tap an avatar to view the person: `GET users/{id}`; **Message** is `POST users/{id}/chat` |
| `url_previews` | Link previews: `GET messages/{id}/url-preview` |
| `documents` | Document reader: `GET documents` (see "10") |
| `doc_comments` | Document comments (needs `documents`) |
| `posts` | Posts (Moments and the like): `GET posts/{id}`, `GET posts/{id}/comments` |
| `edit_history` | Shows an **Edited** entry when a message carries `edit_history` |
| `translation` | This source's conversations can be picked under **Settings › Translation**; the App translates with the hub's `/api/v1/translate` |

## 4. Endpoints

The App calls the hub: `/api/v1/messaging` and `/api/v1/messaging/{source}/…`. The hub authenticates (read-only devices can still only read) and forwards the latter as is to the extension's HTTP side, same path,
with the same headers as `api` (credentials stripped, `X-Persona-Device` and `X-Persona-Role` added). A disabled extension or a missing source returns 404 `not_found`.
For sources with `accounts`, every endpoint accepts the query `account` (an opaque string from `GET accounts`); without it, the source's default account is used.

| Method, path (after `{source}`) | Request | Answer |
| --- | --- | --- |
| `GET /api/v1/messaging` | — | `{sources: [Source]}`: enabled sources; `Source` is the manifest entry plus `extension` (the extension id) |
| `GET status` | — | `{configured, connected, signed_in, name?, error?}`: for the settings page and the "Not connected" hint |
| `GET me` | — | `{account, user_id, name?}`: who this account is right now (outbox and drafts are keyed by it; see "8") |
| `GET accounts` | — | `{accounts: [{id, name?, avatar?, tenant?, signed_in, unread?}], default}` |
| `GET conversations` | `cursor?`, `limit?` (≤ 100) | `ConversationPage {account, conversations: [Conversation], next_cursor?}` |
| `GET pinned` | — | `{account, conversations: [Conversation]}` |
| `GET conversations/{id}/messages` | `before?` / `after?` / `around?` (position), `limit?` (≤ 100), `topic?` | `MessagePage` (see "5") |
| `GET messages/{id}` | — | `{account, message: Message}`: a single message (used by the outbox to confirm delivery) |
| `GET messages/{id}/thread` | `before?`, `limit?` | `MessagePage`, with `root` as the thread root |
| `POST conversations/{id}/messages` | `Outgoing` (see "6") | `SendReceipt {message_id?}` |
| `POST conversations/{id}/attachments` | query: `client_id`, `name`, `mime`, `kind` (`image` / `file`), `reply_to?`, `thread?`; the body is the file bytes | `SendReceipt` |
| `POST conversations/{id}/compose` | `{mode, text, reply_to?, values?}` | `{ready: true, token}` or `{view}` (see "7") |
| `POST conversations/{id}/read` | `{position, message_ids?}` | `{}` |
| `POST conversations/{id}/read-states` | `{message_ids}` (≤ 100) | `{states: [{message_id, read, unread}]}` |
| `GET conversations/{id}/members` | `q?`, `limit?` | `{members: [Person]}` |
| `GET conversations/{id}/pins` | `cursor?`, `limit?` | `{account, items: [ChatPin], next_cursor?, total?}` |
| `GET conversations/{id}/pins/{message}` | `topic?` | `{account, target: {message, root?, page: MessagePage}}`: jump to the pinned message |
| `POST conversations/{id}/pins` | `{message_id, pin_id?, remove}` | `{account, pinned}` |
| `POST messages/{id}/reactions` | `{key, remove}` | `{}` |
| `POST messages/{id}/actions/{action}` | `{values?}` | `{view?, toast?, open_url?}`: actions on a message and card buttons (see "5") |
| `GET messages/{id}/url-preview` | `url` | `URLPreview` |
| `GET emojis` | — | `{emojis: {key: {label, url}}, groups?: [{title, keys}]}` |
| `GET reactions/quick` | — | `{keys: [key]}` |
| `GET users/{id}` | `conversation?` | `Profile` |
| `POST users/{id}/chat` | `{}` | `{conversation_id}` |
| `GET media/{key}` | `size?` (thumbnail edge length) | Bytes; the extension's `Cache-Control` applies |
| `GET documents` | `url` | `Document` (see "10") |
| `GET documents/comments` | `url` | `{threads: [CommentThread]}` |
| `POST documents/comments`, `…/{thread}/replies` | `CommentWrite` (see "10") | `{}` |
| `POST documents/comments/{thread}/resolve` | `{url, resolved}` (`false` reopens) | `{}` |
| `GET posts/{id}`, `GET posts/{id}/comments` | — | `Post`, `{comments: [PostComment]}`; `{id}` can be a post id or the whole link handed over by `links` (escaped into one path segment); the extension recognizes either |

**The hub checks answers**: answers with a schema in the table above (`ConversationPage`, `MessagePage`, `Message`…) are checked by the hub against `messaging.schema.json` (`wire.Validate`).
Non-conforming ones return 502 `extension_bad_response` (logged with the extension id and the first non-conforming path), and the App treats it as a failed read. Unknown fields are kept as is (new extension, old hub).
When a newer extension sends a segment `kind` the hub does not know, the hub replaces it with a text segment of its `text` (dropped if there is no text), replaces unknown document blocks with paragraphs, and checks again (`fitMessagingAnswer`); other unknown enum values are still 502 and need a version bump to add. Everything the App receives is something it understands. Strings and arrays have limits (written in the schema); exceeding them is also 502.

**Pictures of people and conversations.** A source can put its original `avatar` on an account, conversation, person or profile. Use `media/<key>` (including a version query when needed) or a public HTTPS image address. A changed picture gets a new value. The manager derives the optional `avatar_url` on those fields in checked answers, including senders and forwarded messages. Existing clients still receive the original `avatar`. The account list uses each row's own ID. On a multi-account source an unqualified successful read that contains relative pictures and omits its account is read again with the identity from `GET me`. A missing identity leaves the original field available without assigning a permanently cached picture to the wrong account.

Apps load `avatar_url` from the hub and manager that returned the row, with their own display size. The manager reads relative media from the registered source's `GET media/{key}` and shares its original bytes across thumbnail sizes within that account. Public HTTPS uses the hub's existing [avatar fetcher](../protocol/protocol.md#avatars-10923). A client talking to an older hub can use its original relative media route. For an old raw HTTPS value, the Apple app tries the same hub avatar endpoint and shows the person's initial if the hub cannot supply it. Attachment, cover, sticker and emoji media follow their existing paths.

**No duplicate sends**: the hub does not wrap `messaging` sends in receipts (a receipt would also store rejections that can be fixed and retried, like "turn it on in settings first" or "encrypted content expired", and keep replaying them for the same `client_id`).
The extension deduplicates by `client_id` itself: the same `client_id` goes out only once, and a retry returns the first result. Failures before anything was written to the vendor (account read timeout, invalid body, refusal) and rejections (`input_required`, `unsupported`, `not_signed_in`, `account_changed`, `rate_limited`) do not count as sent; after fixing, send again with the same `client_id`. If the previous attempt with the same `client_id` has not finished, or the extension itself does not know whether it went out, return 502 `result_unknown` (do not return a 2xx without `message_id`); the App does not resend automatically and reads once to confirm.

**Errors**: `{error: "one sentence for humans", code?}`; `code` uses only the values below (the App acts on them; anything else is treated as a generic error and shows `error`):

| `code` | Meaning | App |
| --- | --- | --- |
| `not_signed_in` | Not signed in / sign-in expired on the source's side | "Please sign in again on the hub machine", no retry |
| `account_changed` | The requested `account` is no longer the current account | Drop state for the old account and reread |
| `input_required` | The user must provide something more before the write (unlock, options); the answer carries a `view` | Show the `view` (declarative UI); after submitting, resend the same request with `values` |
| `result_unknown` | The write may or may not have succeeded | No automatic retry; show "Result unknown" and read once to confirm |
| `unsupported` | This source / account does not support the action | Hide the action |
| `rate_limited` | Too fast | Retry later |

## 5. Conversations and messages

**Conversation**: `id`, `name`, `avatar?`, `kind` (`direct` / `group` / `topic_group` / `bot`), `unread` (count), `marked_unread?`, `mentioned?` (has an unread @me),
`muted?`, `pinned?`, `preview` (a one-line summary, already processed by the extension: decoding, encrypted display text, recalls), `preview_sender?`, `preview_mine?`, `updated_at`,
`read_position?`, `mention_position?` (for positioning when opened).

**Position**: each message's `position` is a **non-negative decimal integer string** within its conversation, larger for newer; the App compares by (length, lexicographic order). The paging parameters `before` / `after` / `around` are all positions.
When the extension's cursor is not a position, it goes in `older_cursor` / `newer_cursor`, and the App passes it back as is in `before` (an older page) or `after` (a newer page) for the next page; the extension tells for itself whether it received a position or a cursor.

**MessagePage**: `account`, `messages` (old → new), `has_older`, `has_newer?`, `older_cursor?`, `newer_cursor?`, `root?` (the thread root, only for `thread`), `topic?` (this conversation is a topic group and `messages` are the topic roots: when the App opens a conversation without knowing its kind, for example from a profile page or the Inbox, it draws them as topic cards based on this).

**Message**:

| Field | Meaning |
| --- | --- |
| `id`, `position`, `created_at` | |
| `sender` | `{id?, name, avatar?, kind?}`; `kind` is `user` / `bot` / `system` (system messages are drawn as one centered line) |
| `mine` | Sent by me |
| `client_id?`, `state?` | The `client_id` of a message I sent (outbox reconciliation, see "8"); `state` is `pending` / `failed` (not yet sent out on the source's side) |
| `parts` | Body segments (see below), drawn in order |
| `text` | Plain-text version (for copy, notifications, summaries, translation); already processed by the extension (decoded; for encrypted messages, the display text) |
| `reply_to?` | Quote: `{id, sender_name, summary, recalled?}`. `summary` is one line the extension prepared. `recalled`: the quoted message was recalled and `summary` is its retained text (the App adds 「已撤回」 after it); when the recall is not shown it is left out and `summary` says 「消息已撤回」 |
| `thread?` | Thread: `{count, latest_at?}`; with the `threads` capability, shows "N replies", which opens the thread details |
| `reactions?` | `[{key, count, mine?, users?: [name]}]` |
| `edited?`, `edit_history?` | Edited; `edit_history: [{edited_at, parts, text}]` (the extension decides whether to provide it: when Lark's **Edit History** switch is off, it doesn't) |
| `recalled?` | Recalled: if there are `parts`, draw the body and mark it **Recalled** (kept by the extension, viewable when the switch allows); otherwise draw **Message recalled** |
| `decorations?` | `[{text, icon?, tone?}]`: small text under the body, such as "Encrypted message · display text"; at most four entries |
| `private?` | The body must not be written to disk (a decrypted encrypted message): the App keeps it only in memory, and the cache stores `private_text` (the display text) in place of `text` |
| `private_text?` | See above |
| `actions?` | The extension's actions in the long-press menu: `[{id, title, icon?, destructive?, confirm?}]`; tapping one calls `POST messages/{id}/actions/{id}` |
| `can_pin?`, `can_unpin?`, `can_reply?`, `can_react?` | Omitted means allowed (when the matching capability is present) |
| `read?` | Whether I have read this message from someone else (for reporting read state) |

**Part** (a segment, distinguished by `kind`; for a `kind` the App does not know, draw the text of its `text` field, or skip it if there is none):

| `kind` | Fields | Drawn as |
| --- | --- | --- |
| `text` | `text`, `marks?` (`bold` / `italic` / `underline` / `strike` / `code`), `url?`, `block?` (`paragraph` / `heading1…3` / `quote` / `code` / `list_item`, `list?: {ordered, level, start?}`), `block_id?` | Inline text; consecutive segments with the same `block_id` form one block (the same paragraph, the same list item), a different one starts a new block; without `block_id`, consecutive blocks of the same kind merge into one |
| `mention` | `text`, `user_id?`, `all?`, `block?`, `block_id?`, `list?` | @someone (highlighted, tap to view the profile) |
| `link` | `text`, `url`, `block?`, `block_id?`, `list?` | Link; hosts in `links` are handed to the source to open |
| `emoji` | `key`, `text`, `block?`, `block_id?`, `list?` | Inline emoji (an image from `emojis`), or `text` if there is no image. `key` is case-sensitive and opaque: `emojis`, `reactions/quick`, a message's `reactions` and what is sent back when reacting all use the same spelling |
| `sticker` | `media`, `width?`, `height?` | Sticker |
| `image` | `media`, `width?`, `height?` | Image (tap for full size) |
| `file` | `name`, `media?`, `size?`, `ext?`, `expired?` | File card |
| `video` | `media`, `duration_ms?`, `poster?` (`{media, width, height}`) | Video |
| `audio` | `media`, `duration_ms?` | Voice message |
| `code` | `text`, `language?` | Code block |
| `divider` | — | Divider |
| `card` | `view` (a declarative View, `view.schema.json`), `fallback` | Card: the extension converts the source's card into a View; if it can't be drawn, draw `fallback` |
| `forward` | `title`, `messages: [Message]` (at most 50) or `previews: [{sender_name, text}]`, `count?` | Merged forward (tap to see all) |
| `share` | `title`, `subtitle?`, `image?`, `fields?: [{label, value}]`, `url?` | Shared group cards, contact cards, events, meetings… |
| `system` | `text` | System notice (centered) |

An @, a link or an emoji in a paragraph or a list item says which block it is in with `block`, `block_id` and `list`, as `text` does; otherwise the App draws it in the block of the text around it: the one before it, or the one after when nothing before it is in a block.

`media` is a path relative to the source (`media/<key>` or `messages/<id>/media/<key>`, query allowed); the App builds `/api/v1/messaging/{source}/<media>` and fetches it with credentials and `account`. Absolute https addresses are fetched directly without credentials.

## 6. Sending

**Outgoing**: `client_id` (required, lowercase UUID; unchanged on App retries), `text`, `mentions?: [{user_id, name}]`, `reply_to?` (the quoted message id), `thread?` (`true`: send into the thread of `reply_to`),
`mode?` (composer mode id, see "7"), `prepared?` (the `token` from `compose`), `expect_user?` (the `user_id` from `GET me`: if the account is no longer signed in as this person, don't send and return 409 `account_changed`, so an old draft is never sent after the signed-in person changes).

Attachments: `POST conversations/{id}/attachments`, one request per attachment, each with its own `client_id`; images and files both go through it (distinguished by `kind`), and the extension decides how to pass them to the source.

The answer is `SendReceipt {message_id?}`: with `message_id`, the source confirmed it received the message; without, it was accepted but has no id yet (files, for example), and the App does not resend automatically.

## 7. Composer modes

The manifest's `composer_modes` are the modes besides "normal": `id`, `title`, `icon`, `description?` (a line above the composer when it is selected), `attachments` (whether attachments are allowed), `mentions` (whether @ is allowed), `threads?` (whether it can send into threads; omitted means no), `prepare?`.
Whether a mode is usable depends on the account: `GET status` returns `modes: [id]`, the modes usable right now.

- Modes without `prepare`: the app sends `Outgoing` with `mode`; the extension processes its text before sending.
- Modes with `prepare` (encryption): before sending, the App calls `POST conversations/{id}/compose {mode, text, reply_to?, values?}`:
  - `{ready: true, token}`: send with `prepared: token`;
  - `{view}`: the user must do something first (pick the display text, enter the keystore password): the App shows this View, and after the user submits, calls `compose` again with `values`. This View **may only contain** `text` / `field` (including `choice` and `secret`) and a `button` with `submit`: the App sends back only the field values; button `id`s and row actions never reach the extension (use `choice` to pick an option). The same goes for the View carried by an `input_required` error.
  - The `token` is single-use and expires after 10 minutes; sending with an expired one returns 409 `input_required`, and the App calls `compose` again.
- Drafts remember their mode; when the mode is not in `status.modes`, the draft is kept and the send button is grayed out with an explanation.

## 8. Outbox, drafts and accounts

All of these are generic in the App; the extension just has to hold to the following:

- **`client_id` idempotency**: the same `client_id` is sent only once (the hub's receipt is one layer; the extension must also guard: if the source has an outbox, use the source's).
- **Echo reconciliation**: when my sent messages are read back, include the `client_id` (if the source can store it); the App uses it to replace the local copy with the real one. If it can't be stored, the App falls back to reconciling by "text + time" (text messages only).
- **Delivery confirmation**: delivered means `GET messages/{id}` can read it, it is `mine`, and `state` is not `pending` / `failed`.
- **Accounts**: with `accounts`, every account-related answer carries `account` (`ConversationPage`, `MessagePage`, Pins…); if the account the App sent differs from the one in the answer, treat it as `account_changed`.
  `GET me` splits the outbox by account (when the signed-in person changes, unsent messages from the old account are not sent from the new one).

## 9. Live events and the Inbox

**Events**: the extension sends `messaging.changed {source, conversations?: [id], account?, settings_changed?}` (an event of the process protocol, so the source's extension must speak the process protocol).
`account` is as in requests: left out for the default account. Empty `conversations` (or more than 500) means read everything again.
the hub coalesces them within one second and pushes the WebSocket message `{type: "messaging", source, conversations, account?, settings_changed?}`. The App: an open conversation rereads its latest page right away;
the list rereads; conversations not on screen are noted and read when the App returns to the foreground or they are opened; `settings_changed` rereads `status` (composer modes) and the settings page. Everything still works without events (while in the foreground, the App polls the open conversation every 60 seconds).

**Inbox**: turning a source's conversations into Inbox items is still done by the same extension's connector; the conversation location on an item changes from the Lark-specific `lark` field to the generic
`conversation: {source, id, account?, start_position?, read_position?, mention_position?}`; tapping the item opens that conversation at `start_position`.

**Notifications**: pushes sent by the hub carry the same `conversation`; tapping one opens the conversation.

## 10. Documents and posts

**Document** (`documents` capability): `{title, url, blocks: [Block], permission?: {copy, export}, partial?}`. A `Block` is `heading` (`level`), `paragraph`, `list` (`ordered`, `items`),
`quote`, `code` (`language`), `callout`, `table` (`rows`), `image` (`media`), `divider`, `embed` (`title`, `url`); inline content is the `text` / `mention` / `link` of `Part`.
The App draws it natively, with tapping comments and commenting on selected text (`doc_comments`). The source's document format (Lark's ProseMirror JSON) is converted to `Document` in the extension.

**Comments**: `CommentThread {id, quote?, block_id?, resolved, replies: [{id, author: Person, parts, created_at, edited?}]}`; writing a comment is `{url, parts, mentions?, quote?, block_id?, client_id}`.

**Post** (`posts` capability, Moments and the like): `{id, author: Person, parts, images?: [{media, width, height}], reactions?, comment_count?, created_at, categories?}`; comments are `PostComment {id, author, parts, created_at, hot?}`.

## 11. Entry points in the App

- When `GET /api/v1/messaging` has a source (and the user has not turned it off in settings), the tab bar / shell gets one more entry, with the source's name and icon; with no source there is none. The old **Show Messages on Home** switch migrates to "Show <source name>".
- Under **Settings › Connectors**, the row of the connector matching the source leads to `settings_ui` (a declarative page) and the account list.
- Tapped links listed in `links` are handed to the source (document reader, post, or the extension's full-page UI).
- The App no longer contains any source's name, error codes, event types or paths; cache, drafts and outbox are split into directories by `source`.

## 12. Versions and compatibility

- Extension point `messaging` is version 1; the hub's `features` includes `messaging`, and the App uses this path only when it sees it.
- During migration, the extension that connects to Lark provides both the old `/api/v1/lark` (still used by Mac, Android and web) and `messaging`; iOS uses the new one when `messaging` is there, otherwise the old one. An app that follows `messaging` says `lark_via_messaging` in `client_hello`, and the hub then leaves the old WS `lark` notice out for it (#9591; docs/protocol/protocol.md, the `lark` row).
  The old iOS implementation is deleted after iOS acceptance passes (#2138 phase 4); the old endpoints are deleted once all four clients have migrated.
- Adding fields is backward compatible: old Apps skip fields, segments and capabilities they do not know; an extension must not use new fields to convey something old Apps must understand (that would be a new version).

## 13. How Lark maps (implementation notes for the extension that connects to Lark)

| Lark / Lark gateway | This protocol |
| --- | --- |
| `ol_account` (number) | `account` (opaque string) |
| `userId` check against `/raw/accounts` | `GET me` |
| `/raw/messages/{id}` delivery confirmation | `GET messages/{id}` |
| Card element tree (`LarkCard`), summary cards (merged forwards, shares) | `card` (converted to a View), `forward`, `share` |
| Anti-recall and edit-history switches | The extension decides by these switches whether to provide the `recalled` body and `edit_history`; the switch page is `settings_ui` |
| Encryption | Composer mode `sealed` (`prepare`: pick the display text, unlock); on read, the extension decrypts (when the keystore is unlocked), marks it `private` and decorates it "Encrypted message"; if it can't decrypt, it provides the display text |
| `lark_not_logged_in`, `lark_account_changed`, `VAULT_LOCKED` | `not_signed_in`, `account_changed`, `input_required` (with an unlock View) |
| Documents (`/raw/documents/*`) | `GET documents` (converted to `Document` by the extension) |
| Moments | `posts` |
| WS `{type: "lark", feed_ids, ol_account}` | `messaging.changed` |
| Inbox `InboxLark` | The item's `conversation` |
