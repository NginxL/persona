# deep-links

The cases every client checks against the route table in `docs/protocol/deep-links.md` (#9926): the same link opens
the same page on the iPhone, the web, the desktop app and Android, and the hub writes every page's link one way.

All addresses are made up (`example.com`, `example.org`, `example.net`, `2001:db8::/32`).

## Pages

A page is an object whose `page` names it, with that page's values (decoded, never escaped):

| `page` | Values | Canonical path |
| --- | --- | --- |
| `persona` | | `/persona` |
| `persona-new` | | `/persona/new` |
| `conversation` | `id`, optional `machine`, `manager`, `at` | `/persona/<id>` |
| `session` | `machine`, `session`, optional `at`, `view` | `/s/<machine>/<session>` |
| `agents` | | `/agents` |
| `inbox` | | `/inbox` |
| `inbox-item` | `id`, optional `manager` | `/inbox/<id>` |
| `todos` | optional `view`, `mode`, `date` | `/todos` |
| `todo` | `id` | `/todos/<id>` |
| `knowledge` | optional `kind` | `/kb`, `/kb/<kind>` |
| `knowledge-entry` | `kind`, `id` | `/kb/<kind>/<id>` |
| `machines` | | `/machines` |
| `machine` | `id`, optional `range` | `/machines/<id>` |
| `settings` | optional `tab`, `item` (the tab's item key's value), `manager` | `/settings` |
| `extension` | `extension`, `ui`, optional `state` | `/extension/<extension>/<ui>` |

A parsed page may also carry `hub`: the id of the hub it is on (the matching hub's for a web link, `hub=` for an app
link). A page without `hub` opens on the hub the app is showing.

## Structure

```json
{
  "version": 1,
  "hubs": { "<profile>": { "id": "<hub id>", "addresses": ["<base URL>", "..."] } },
  "build": [{ "name": "...", "page": { "page": "todo", "id": "t-19" }, "path": "/todos/t-19" }],
  "links": [{ "name": "...", "base": "<public_url>", "page": {}, "web": "<web link>", "app": "<app link>" }],
  "parse": [{ "name": "...", "url": "<link>", "hubs": ["<profile>", "..."], "page": {} }]
}
```

- `build`: the hub's builder writes `path` for `page`, byte for byte. Path segments are escaped as Go's
  `url.PathEscape` does; query values as `url.QueryEscape`; query keys in the table's order (`tab` before the tab's item
  key, `machine`, `manager`, `at`).
- `links`: the web link is `base` (its trailing `/` dropped) plus the path, `""` when `base` is; the app link is
  `persona://open` plus the path.
- `parse`: `hubs` lists the paired hubs (the first is the one the app is showing). `page` is what the link opens, or
  `null` with `reason`:
  - `external`: not one of the paired hubs' addresses (or not a web or app link at all). Clients open it as before.
  - `not-a-page`: on a paired hub, or a `persona://` link, but no page of the table. Clients keep their own handling
    (the web card, an action link's own parser).
  - `unsafe`: refused; never opened as a page or handed on.
  App-link cases may carry `route`: the path and query the desktop app hands to the web app (keys no row lists and
  `hub` dropped). Values are compared decoded, so `%3A` and `:` in a segment are the same page.

## Who reads it

| Client | Sections |
| --- | --- |
| Hub (Go) | `build`, `links` |
| iPhone (`HubPageLinkTests`) | `parse` |
| Web | `parse` (the web treats a case's only hub as its own origin; cases with two hubs are iPhone's and Android's) |
| Desktop | `parse` cases with a `route` (accepted, handing on that route) and app-link cases that are `unsafe` (refused) |
| Android | `parse` (#9932) |
