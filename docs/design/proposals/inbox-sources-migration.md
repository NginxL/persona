# Proposal: built-in Inbox sources become an extension's read tools plus a default subscription

Status: approved by the owner on 2026-11-13 (#6150), with the decisions in section 5. Step C of the Inbox Sources work,
after #5278 and #5741. It is built one step at a time (section 4), each its own issue and PR; nothing is built yet. Covers the extension manager (`server/internal/extmgr`,
`server/internal/subscriptions`), the assistant's Inbox (`server/internal/assistant`) and the Inbox Sources pages; the
clients change only where a step says so.

## 1. Goal

**One sentence**: every Inbox source works one way, a read tool and a subscription that says what to pull and where it
goes, and moving a built-in source to that way loses nothing the owner uses today.

**Reached when**, for each migrated source:

| # | The owner sees | Checked by |
| --- | --- | --- |
| G1 | The same items as before, with nothing missed or doubled at the switch | A replay test: one day of the source's fixtures through the built-in and through the subscription gives the same item ids, titles, senders and tiers |
| G2 | Reply, Draft and the AI twin work as before | The source's reply and twin tests pass against the subscription item |
| G3 | Mutes, policies and items already done still apply | Existing mutes and done threads match the migrated items (test on a copied state) |
| G4 | It can be switched back | Turning the migration off restores the built-in with no re-delivered or lost items (test) |
| G5 | One list, one set of controls | Inbox Sources shows the source as a subscription; the built-in row is gone |

## 2. What happens today

Read from the code on 2026-11-13. Functions are in `server/internal/` unless the path says otherwise.

### 2a. Two pipelines

| Fact | Where |
| --- | --- |
| Built-in sources (mail, GitHub, iMessage, Lark through `larkcli`) are connectors the manager reads itself: a poll every 2 minutes, mail every 3, a kick debounced 3 s | `inboxPoll`, `inboxMailPoll`, `inboxDebounce`, `collectInbox` in `extmgr/inbox.go`; `readMail`, `readConnectors` in `extmgr/inbox_read.go` |
| Subscriptions call an extension's or connector's read tool on their own schedule: checked every 30 s, at least 5 minutes between pulls, 20 s between calls and 120 calls an hour per connector | `tickEvery`, `callGap`, `defaultPerHour` in `subscriptions/runner.go`; `minInterval` in `subscriptions/service.go` |
| A subscription hands the Inbox its items on poll and full rounds as a source with an id, a name, `Extension: true`, kinds and counts, never `Group`, `Meta`, `Settles` or `Migrated` | `readSubscriptions` in `extmgr/subscriptions.go` |
| The built-ins already have read tools a subscription can call: mail's derived catalog (`search` by account, mailbox, query), GitHub's `search`/`read`/`diff`/`checks`, iMessage's JSON reads. `larkcli` has text tools only, which a recipe can use only as snapshots | `ReadCatalog` in `connector/catalog.go`; `readTools` in `modules/github/tools.go`; `CallJSON` in `modules/imessage/persona.go`, `extmgr/mail_connector.go`; `docs/extensions/subscriptions.md` "Recipe format" |

### 2b. What a subscription item lacks

| Ability | Built-in today | Subscription item today | Where |
| --- | --- | --- | --- |
| Reply | GitHub comments unless locked; Lark when the latest message has an id; mail from the receiving account with threading headers; iMessage none (`sms:` link). Sent once per `send_id` | `can_reply` false. Even if set, `connectorOf(<subscription id>)` finds no connector: subscription ids (`sub_<hex>`) can never be connector ids | `replyInboxDo` in `assistant/inbox_triage.go`; `replyConnectorItem` in `assistant/inbox_connectors.go`; `inboxAppCall`, `sendInboxOnce` in `extmgr/inbox_calls.go`, `extmgr/inbox_reply.go`; `newID` in `subscriptions/service.go` |
| AI twin | Its prompt names the Lark feed and account, the mail account, mailbox and UID, the doc or moment, GitHub's URL and read tools; Lark and mail chats bring knowledge-base context | Only the subscription id: the prompt's line on where a reply goes ends empty, no URL (printed for GitHub only), no connector or tool to read with, no KB chat | `escalate` in `assistant/inbox_handle.go`; `inboxAgentFor`, `kbChatOf` in `assistant/inbox_agent.go`; `prompts/inbox/agent_ref.md` |
| Read state | Inbound only: mail's seen flags (also for items that fell off the page, `settleMailOffPage` + `inbox.mail_flags`), connectors' `Read`, GitHub's inferred read, settling. Nothing writes read or done back to any source | No `Read`, no `Settles`; `Done` only on the failing notice. A recipe has no `seen` field | `applyMail`, `mailReadElsewhere`, `settleMailOffPage` in `assistant/`; `InboxSettler` in `connector/inbox.go`; `ConnectorSubscriptionFields` in `sdk/go/wire/wire_gen.go`; `docs/assistant/inbox.md` "Connector: GitHub (2026-11-02)" |
| Real time | All polled (no IMAP IDLE, no GitHub webhooks). An extension program's `changed` event kicks the Inbox at once | `changed` never starts a run; at best the next check, 5 minutes apart. Push sources exist only for a full-access paired device over HTTP: 20 items a push, 300 an hour, items carry key, title, body, app, link, time | `connectorChanged`, `kickInbox` in `extmgr/connectors.go`; `Push`, `pushItemsMost`, `pushPerHourMost` in `subscriptions/push.go`; `subscriptionRoutes` in `extmgr/subscriptions.go` |
| Identity for mutes and policies | Mutes and policies key on source + tag + value: mail by address, GitHub by repo, Lark by chat | Tags are `source` and `sender` = the lowercased display name. Digest, snapshot and failing rows have no sender, so no mute or policy option at all | `mutesOf`, `mutedBy`, `mutableTags` in `assistant/inbox_mutes.go`; `deliverer.tags` in `subscriptions/deliver.go`; `docs/assistant/inbox.md` "Policies (2026-11-06, #5747)" |
| Item ids and done threads | `<source>:sha1(thread, last)`; "already handled" per source + thread | `<sub>:sha1(...)`: a different source, so none of a built-in's done threads match | `applyConnector` in `assistant/inbox_connectors.go`; `threadThing` in `subscriptions/deliver.go` |
| Read errors | `mail:<account>` and connector read errors show in the Inbox at once | A failing pull shows only as the failing notice after 24 hours | `readMail` in `extmgr/inbox_read.go`; `failing` in `subscriptions/deliver.go` |
| Knowledge base | Media, history import, poll interval, iMessage's fence; mail per account with UIDVALIDITY cursors and one chat per correspondent | `ArchiveSource{ID}` only: changes 100 a page from a seq cursor, chats by thread hash, the sender's name only, kept 30 days, no history import | `kbArchives` in `extmgr/kb_sources.go`; `mailConnector.Changes` in `extmgr/mail_connector.go`; `Service.Changes`, `chatOf` in `subscriptions/deliver.go` |
| Triage | Admitted at once, tiers from the connector, rules from `larkcli` options or the extension | Triaged before admission; tiers from the recipe's rule fallback; rules the subscription's own or the global ones | `docs/assistant/inbox.md` "AI triage"; `fallback` in `subscriptions/deliver.go`; `builtinSources.rules` in `extmgr/inbox_sources.go` |
| Turning on | On unless switched off; settings in `manager.json` (`github.inbox`, `ignore_branches`, `lark.cli.profile`) | Created with both destinations off; turning on needs a full-access device and a confirmation; the assistant's saves need the phone's approval | `Service.Create`; `docs/extensions/subscriptions.md` "Pages", "Assistant tools"; `docs/operations/config.md` "manager.json" |

A precedent exists for moving a source while keeping its ids: `MigratableConnectors` (lark, github) lets a same-id
extension take over a built-in connector with its item ids, mutes and knowledge base (`connectorwire/inbox.go`;
`docs/extensions/extensions.md` "GitHub: from hub to extension (#2830)").

## 3. Design

Subscriptions stay read-only: a recipe never calls a write tool (`docs/extensions/subscriptions.md` "Read-only tools").
Replying, the twin and read state go through the connector that the subscription reads, as they do for a built-in today.

### 3a. A migrated subscription keeps the built-in's identity

A built-in's default subscription has a fixed id, `sub_m_<connector id>` (one per connector, one per mail account; P5a,
#6265). Its Inbox items use the built-in's source id and item ids, not `sub_<hex>`. Existing mutes, policies, done threads,
`read_error` keys and knowledge-base chats then match without rewriting state, as `MigratableConnectors` does. Only the
manager can create such a subscription. It can't be duplicated, and deleting it means switching the migration back (3g).

### 3b. Recipe fields for identity, reply and read state

`ConnectorSubscriptionFields` gains optional `sender_id`, `chat`, `chat_name`, `reply_to` and `seen`, each mapped from
the read tool's JSON like the existing fields:

- `sender_id` and `chat` become tags (`sender` by id, `chat` named by `chat_name`), so mutes and policies can name them.
  The display name stays the label. Done in P1a (#6165).
- `seen` becomes the item's `Read`. An item missing from a full page settles as the built-ins' `Settles` does, so read
  elsewhere means read here (inbound only, as today).
- `reply_to` is the line saying where a reply goes and who sees it, as a built-in item's `ReplyTo` ("Comment on
  example-org/example-repo#12 (anyone who can see the repo)"). With it set and the connector able to reply, the item gets
  `CanReply`. The reply itself addresses the connector's own thread (the recipe's existing `thread`) and the newest
  item's `id` as `Last`, which a subscription item already holds.

Recipes without the new fields behave as today. The SDK's wire types and `schema/` change with them.

For a built-in whose rows carry more than a recipe can map (iMessage: one row per conversation, done when the owner
replied, Needs you for a direct message, an `sms:` link, the last messages as context), a recipe in `things` mode
reads a tool that answers the rows ready-made (the owner's choice on 2026-11-13; #6296). The connector computes them
with the code its `Collect` uses, and the subscription only schedules, keeps, switches and delivers them, so the rows
before and after the switch are the same by construction. Each such source proves it with a test that compares its
`Collect` and its tool's rows field by field on the same fixtures.

### 3c. Reply goes to the subscription's connector

For an item from a subscription with `reply_to`, the assistant sends `inbox.reply {source, reply, send_id}` as today.
The manager resolves the source to the subscription's connector, not to a connector with that id, and calls its
`Inbox.Reply` with the connector's thread and `Last` (`InboxReply` in `connectorwire/inbox.go`). Send-once
(`sendInboxOnce`), mail's receiving account and threading headers are unchanged. A connector with no reply (iMessage) keeps `CanReply` false. Draft and the twin's reply suggestion then check
`CanReply` before offering (today `suggestIn.check` doesn't).

### 3d. The twin gets what it needs to read

`agent_ref.md` prints, for any connector item, the link, the connector and source the subscription reads, the read
tool it may call and its `reply_to`, instead of only GitHub's. `kbChatOf` uses the subscription's
knowledge-base chat (`chatOf`). The twin's escalation rules don't change.

### 3e. Real time

- A connector's `changed` event also starts a run of that connector's subscriptions (P4a, #6247): people's at most
  once a minute, within `callGap` and the hourly cap.
- A switched built-in's default subscription runs 3 seconds after `changed`, as the Inbox's own kick of the built-in
  does (`inboxDebounce`), a burst of events once. Its runs replace the built-in's `Collect` calls one for one, so they
  are outside the subscriptions' hourly budget and call gap; a pause the connector asked for still holds (P4b, #6381).
- Its rows come from a generic `inbox_things` that the subscriptions add to every connector with an Inbox, answering
  the connector's `Collect` (or the connector's own `inbox_things`, as iMessage's). Lark's events reach the Inbox as
  fast as today with no new protocol: the Lark extension already sends `changed` and answers `Collect`.
- The round's entry for a switched source carries its built-in's abilities (settle, group, meta, kinds, migrated), as
  `readConnectors` gives them, so nothing it does is lost at the switch.

### 3f. The knowledge base stays on the built-in path at first

The subscription knowledge-base path lacks media, history import and mail's per-account cursors. A migrated source
therefore moves its **Inbox** feed first. Its archive stays with `kbArchives` until the subscription path has what the
source uses, which is its own later step per source. Inbox Sources shows both switches on the one subscription row
either way.

### 3g. Switching, compatibility and rollback

- **Per source**, the default subscription's `migrated` mark (P5a, #6265; it takes the place of a separate
  `inbox.migrated` setting), changed from Inbox Sources with one confirmation:
  - **On**: the built-in's Inbox feed goes off and its default subscription on, in one step. The subscription's
    catch-up reaches `catchupBack`'s day before and comes as rows, never a digest: the rows the built-in already
    brought are known by their item ids (3a), so nothing is missed and nothing comes in twice (as built in P5a).
  - **Off**: the reverse. The subscription stays, off, with its seen keys, so switching on again re-delivers nothing.
- **Defaults**: an existing install keeps the built-in until the owner switches it. A new install gets a source's
  default subscription instead of its built-in once that source has run switched on, on the owner's hub, for a week
  without a difference (decision 6).
- **Mixed versions** (as built in P5a: `docs/extensions/subscriptions.md` "Built-in sources switched to their default
  subscription"):
  - The default subscription's own destination switches stay off in storage, so a manager that does not know the mark
    never pulls it, and the built-in's feed, never switched off, comes back on a rollback. The switch is on the
    manager-drawn pages, so no client feature flag is needed.
  - An assistant without the new reply routing must not get `CanReply` for those items. The assistant reports that it
    routes them (part of P2), and the manager sets `CanReply` only then, so nothing offers a reply that fails.
  - An older manager ignores `migrates` and the new recipe fields. Without the setting it never switches, so rolling
    back the hub restores the built-ins.
- **Docs**: `docs/extensions/subscriptions.md` "Built-in sources switched to their default subscription" holds the
  mixed-version behavior (`docs/protocol/rolling-upgrade-compatibility.md` stays the rename's checklist).

## 4. Order

Smallest gap first, so each step proves part of the design before the harder sources:

| Step | What | Needs |
| --- | --- | --- |
| P1 | Recipe fields: P1a `sender_id`, `chat`, `chat_name` (#6165); P1b `seen` (#6176); P1c a failing pull shows as the source's read error at once (#6189) | 3b |
| P2 | The `reply_to` field; reply through the subscription's connector (#6198). The apps already offer Draft only with `CanReply` | 3b, 3c |
| P3 | The twin's source info and knowledge-base chat for subscription items (#6222) | 3d |
| P4 | P4a `changed` starts runs (#6247); P4b a generic `inbox_things` over `Collect`, a switched source 3 seconds after `changed`, its built-in's abilities in the round (#6381) | 3e |
| P5 | P5a the default subscription's fixed id and `migrated` mark, delivery under the built-in's id, catch-up as rows, rollback (#6265); P5b the switch on the Inbox Sources pages, with M1 (#6345) | 3a, 3g |
| M1 | **iMessage**: a `things` recipe over its `inbox_things` tool (#6296, #6317), the `changed` kick, the switch on the pages (P5b) | P4a, P5, things |
| M2 | **GitHub**: a `things` recipe over the generic `inbox_things` (P4b), so comments as replies, inferred read and repo mutes stay its `Collect`'s; a comparison test; the switch held back from the pages until M1's week was over (#6404), offered since (#13599) | P4b, P5 |
| M3 | **Lark**: a `things` recipe over the generic `inbox_things` (P4b), which answers the Lark extension's `Collect`, run 3 seconds after its `changed` events (decision 4: no JSON read tools or push source needed); a things recipe hands its latest answer, so a conversation's batch of new messages and its events' state stay apart (#13637); a comparison test; the switch held back until M2's week is over (#13672). `larkcli` stays a built-in: it is not on the owner's hub to have its week there | P4b, P5, #13637 |
| M4 | **Mail**, not started (#13685; below): a `Collect` on the mail connector whose rows carry a mail block, one default subscription per account, the assistant routing a switched account's rows back into its mail path | P4b, P5, #13637 |
| K1… | Each source's knowledge-base feed, once the subscription path has what it uses (3f) | per source |

Each M step ships behind the switch and lands with the G1–G4 tests for that source. A source moves on new installs
only after it has run switched on, on the owner's hub, for a week without a difference.

**Mail (M4)** is not moved yet (#13685). When it is, it goes the way of decision 4:

- **How**: the mail connector gets an Inbox whose `Collect` hands each account's latest page (its mailboxes' messages
  with UIDs, Message-IDs, seen and answered flags, UIDVALIDITY) as rows carrying a mail block, a new `ThingExtra`
  block as Lark's and GitHub's have theirs. Each account gets a default subscription. The assistant routes a switched
  account's rows back into its existing mail path (`collectMail` in `server/internal/assistant/inbox_mail.go`), so
  replies with threading headers, peeked bodies, seen and answered flags, off-page settling and per-account read
  errors stay what they are. The rows are the built-in's by construction, and the regression risk is in the routing
  only.
- **Not one items-mode subscription per account** over mail's read tools, which this table first planned. Mail's Inbox
  behavior lives in the assistant's mail path and is keyed on IMAP details: UIDs and UIDVALIDITY baselines, bodies read
  without marking them read, seen and answered flags that file items, settling off the page. Rebuilding each of them
  inside subscriptions would change the mail rows people see, on the largest source, for no gain the move needs.
- **When**: after GitHub and Lark have each run switched on, on the owner's hub, for a week without a difference
  (decision 6). Mail could not be switched sooner, and work written before then would sit unused while `main` moves on.

## 5. Decisions (owner, 2026-11-13)

1. **Identity (3a)**: a migrated subscription keeps the built-in's source and item ids. Mutes, policies, done threads
   and read errors then match without rewriting state, as with `MigratableConnectors`.
2. **Consent at the switch (3g)**: the built-in was already on, so switching keeps the source on, with one confirmation
   for the source. Its default subscription doesn't ask again as a new subscription would.
3. **Knowledge base (3f)**: the Inbox feed moves first. Each source's archive is a later step, once the subscription
   path has what the source uses.
4. **Real time (3e)**, changed on 2026-11-13 (P4b, #6381): no push sources owned by extensions. A switched source's
   default subscription reads a generic `inbox_things` that answers the connector's `Collect`, and runs 3 seconds after
   its `changed` event. It was first decided that Lark's events would come through a push source owned by an
   extension. The owner chose this instead because:
   - it adds no extension protocol (no new method in the schema, two SDKs and the docs);
   - the private Lark extension needs no change: it already sends `changed` and answers `Collect`;
   - the rows are the built-in's by construction (the same `Collect`), where pushed items would have to be proven equal
     to the connector's state;
   - it is as fast as the built-in today (its kick's 3 seconds).

   Each connector's `Collect` must have no side effects, which its M step's comparison test confirms.
5. **Order (4)**: iMessage, GitHub, Lark, mail.
6. **New installs (3g)**: a source switches on new installs only after it has run switched on, on the owner's hub, for
   a week without a difference. It doesn't wait for the other sources.

## 6. Principles self-check (`docs/design/PRINCIPLES.md`)

- **1 Defaults first**: existing installs change nothing until a source is switched; new installs get the same sources,
  on, as today.
- **2 Progressive disclosure**: one switch per source in Inbox Sources; recipes stay hidden behind the subscription's
  page.
- **3 User control**: every switch is visible, takes one confirmation and can be switched back (3g, G4).
- **4 Consistency first**: one pipeline and one row type for every source; mutes, policies and replies behave the same
  on both sides of the switch.
- **5 Model and product separate**: no model change; the twin only gets source facts it lacked.
- **6 Checkable**: G1–G5, each a test or a screenshot.
- **7 Fast, local-first and stateful**: the `changed` kick keeps a migrated source as fast as its built-in. Failures keep
  showing as read errors, which needs P1 to report a failing pull at once rather than after 24 hours.
