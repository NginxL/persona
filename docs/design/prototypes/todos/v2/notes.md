# V2 · Rounds

Prototype of synthesis §3.2 (from concept B). Open `index.html#v=V2` for the gallery, or
`frame.html#v=V2&f=iphone/list` to click through.

## The idea

The to-do is the conversation's home, and there is never a conversation region. The to-do's page carries its
latest three rounds, expanded: the owner's exact words with their message state (✓ once delivered), and under them,
on the branches of a small tree, the steward's reply, run lines, relays, scope changes, decisions and results. Going
deeper (the whole Conversation, or an Agent's session) replaces the to-do inside its own column, and nothing else on
screen moves. The prototype asks one question by clicking: does the owner need the right pane at all?

What differs from V1 (everything else is shared: data, list, rows, partition, section bar, ask cards and doors,
counts):

| V2 rule | Where to see it |
| --- | --- |
| 1 The page shows the latest 3 rounds expanded | `iphone/kyoto`, `iphone/kyoto~rounds` |
| 2 No Conversation toggle, no pane; All N ›, a round or Discuss… open the whole Conversation (All · Messages · Decisions · Results, day groups, pinned header) | `iphone/conversation-kyoto` and its states, `iphone/conversation-release`, `ipad11-landscape/conversation-in-column` |
| 3, 4 A session replaces the to-do in its column; a hand-built stack with an explicit "‹ <to-do title>" | `ipad11-landscape/session-in-column`, `V2:web/todo~session` |
| 5 The sidebar shows only starred places; the title menu holds every place and "Show in Sidebar" | `iphone/list~drawer`, `iphone/list~today-menu`, `ipad11-landscape/todo` |
| 6 The composer's placeholder names the addressee; with text, the label "To Persona" stands above the field (▾ only when its menu has more than one entry, so "To Release lead ▾") | every composer; `iphone/kyoto-keyboard`, `iphone/release~typing` |
| 7 The Agents line "3 Agents ● Widget ◐ Notes ✓ Screens ⌄" and "Needs you · 1 of 3 Agents asks" | `iphone/release`, `iphone/release~agents` |
| 8 Keyboard up: title and state fold into the bar, the ask to one line, the latest rounds above the composer | `iphone/kyoto-keyboard`, `duo-outer/kyoto-keyboard` |
| 9 The sidebar never steps aside | every regular-width frame |

## Frames

★ = the spec's important frames. V2-02 ★ is drawn twice: landing (the ask card fills the first screen) and scrolled
to its rounds (`~rounds`, the idea under test in view). A frame's states (`~state`) follow it in the gallery.

| Spec | Frame id | States |
| --- | --- | --- |
| V2-01 | `iphone/list` (= V1-01) | `~calm` `~drawer` `~menu` `~more` `~group` `~today` `~today-menu` `~kyoto` `~kyoto-menu` `~answered` → `~answered-idle`, `~answered-kaede` → `~answered-kaede-idle`, `~answered-tsuki` → `~answered-tsuki-idle`, `~declined` `~options` `~photos-back` `~can-wait` `~filter-needs` `~filter-progress` `~filter-todo` `~filter-done` `~scrolled` `~ticked` `~swiped` `~approved` |
| V2-02 ★ | `iphone/kyoto` | `~rounds` ★ `~steps` `~answered` → `~answered-round`, `~kaede` → `~kaede-round`, `~tsuki` → `~tsuki-round`, `~kaede-msg` `~later` `~dont-confirm` → `~dont` `~sent-done` `~sent-done-undone` `~menu` |
| V2-03 ★ | `iphone/kyoto-keyboard` | `~sent` → `~sent-2` → `~sent-3` → `~sent-4` (Sending → Processing at 0.4 s → Delivered to Persona at 1.2 s → reply and scope) `~sent-undone` `~answered` |
| V2-04 ★ | `iphone/conversation-kyoto` | `~messages` `~decisions` `~results` `~options` `~answered` `~answered-kaede` `~answered-tsuki` `~answer` (after Discuss…) → `~answered-msg` `~run` `~menu` `~keyboard` → `~long` |
| (V2 rule 2) | `iphone/conversation-release` | Release's whole Conversation, from All 2 › on V2-05 |
| V2-05 ★ | `iphone/release` | `~agents` `~work` `~menu` `~typing` `~to-menu` `~to-notes` `~mode-menu` `~s5` → `~s5-2` → `~s5-3` → `~queued` → `~taken`, `~send-now` `~scope-undone` `~s5-done` `~approved` |
| V2-06 | `iphone/release-widget-session` (= V1-06) | `~approved` `~menu` |
| V2-07 | `duo-outer/kyoto-keyboard` | unfold → `duo-inner-portrait/list-todo~draft` |
| (walk-through) | `duo-outer/list` (`~sidebar`, `~answered-idle`, `~answered-kaede-idle`, `~answered-tsuki-idle`), `duo-outer/kyoto` (`~answered` → `~answered-round`, and the same for Kaede and Tsuki), `duo-outer/conversation-kyoto`, `duo-outer/release` | each unfolds to its inner counterpart |
| V2-08 ★ | `duo-inner-portrait/list-todo` | `~answered` `~answered-kaede` `~answered-tsuki` `~release` `~sidebar` `~menu` `~draft` (S9: the draft survives Unfold) |
| V2-09 | `duo-inner-portrait/conversation-in-column` | fold → `duo-outer/conversation-kyoto` |
| V2-10 ★ | `ipad11-landscape/todo` | `~agents` `~menu` `~kyoto` `~approved` |
| V2-11 ★ | `ipad11-landscape/session-in-column` | `~approved` |
| (V2 rule 2) | `ipad11-landscape/conversation-in-column` | `~release` (All N › in the column, T01 and T02) |
| V2-12 ★ | `ipad13-landscape/long-conversation` | (back: `ipad13-landscape/todo`) |
| V2-13 | `V2:web/todo` (the kit's `web` device, 1440 × 900) | `~conversation` `~release` `~session` |
| V2-14 | `android-fold/list-todo` | `~conversation` |

Frame ids follow the spec with "+" written "-" and the kit's device ids (`duo-inner-portrait/list-todo` is V2-08's
`list+todo`, `V2:web/todo` is `web-1440/todo`, `android-fold/list-todo` is `android-foldable/list+todo`). V1's named states
`#closed` and `#widget` have no V2 counterpart (there is no pane to close; V2-11 is the Widget session in the column).
Every Duo frame names its Fold / Unfold counterpart.

## How to walk it

The synthesis §3.4 tasks, in V2:

1. **S7, counts.** `iphone/list`: Needs you (3) in the bar, the header and the drawer (`~drawer`); every other count
   reads Data. Answer from a row (`~answered`): N is (2) at once in the bar and the header, the other counts follow
   the rows drawn until the row re-files (`~answered-idle`: Working 3, In progress 6). On the Duo outer display the
   Assistant tab names the tray's numbers (Needs you 4 = To-dos 3 · Elsewhere 1); the tray itself is shared with V1
   (`V1:iphone/assistant-tray`).
2. **S2.** On `iphone/list` tap **OK: Ryokan Hanami** (one tap, "✓ You chose … · Undo"). **Options ⌄** unfolds the
   card in the row; Kaede and Tsuki land in their own states ("✓ You chose Kaede · Undo", then "Persona is booking
   Kaede").
3. **S5 lite.** `iphone/kyoto` → tap the composer → `iphone/kyoto-keyboard` → ↑. Nothing opens: Sending → Processing →
   Delivered to Persona, then the reply and the scope line land in the rounds over the keyboard. Tap the folded title:
   the keyboard goes down and the page shows the new round (`iphone/kyoto~sent-done`).
4. **A longer exchange.** All 4 › → `iphone/conversation-kyoto` (pushed; ‹ returns to where you tapped it, the page or
   `~rounds`) → the composer → `~keyboard` → ↑ → `~long`. On the iPad 13″ the same exchange replaces the to-do in its
   column (`ipad13-landscape/long-conversation`): the paper is not beside it. That is V2's trade-off.
5. **Answer by message.** On the card, **Discuss…** → `~answer` (the chip "Answer: Which ryokan? ×", "Kaede, it's
   cheaper") → ↑ → `~answered-msg` ("✓ You chose Kaede · from your message") → ‹ → `iphone/kyoto~kaede-msg`.
6. **Don't handle.** `iphone/kyoto` → Don't handle → confirm → `~dont` → ‹ → `iphone/list~declined` (N = 2, no OK).
7. **S4.** `ipad11-landscape/todo` → **Review Plan ›**: Widget's session is pushed inside the to-do column (the sidebar
   and the list stay; the list row's line becomes "● Plan awaiting approval · answer beside →", drawn once) →
   **Approve** → "‹ Release Weather App 1.3" returns with "Working · 2 Agents" and N = 2.
8. **S5.** `iphone/release` → the composer (`~typing`) → ↑: Delivered to Release lead → "↗ Queued for Release notes ·
   after this step [Send Now]" with "Scope: … · Undo" → after 6 s "↗ Delivered to Release notes" and "◐ Release notes ·
   adding the widget" (R1's older line now reads as past). Tap the folded title for the page (`~s5-done`). Steering
   directly: the label → Release notes → "After its step ▾".
9. **S8.** `iphone/list` → **Start When It's Back** → `~photos-back` (Waiting 4, N = 2).
10. **S9.** `duo-outer/kyoto-keyboard` (draft "also check breakf") → **Unfold** → `duo-inner-portrait/list-todo~draft`:
    the draft, its caret and the selection survive. All 4 › on the inner display cross-fades the right half only.
11. **S1.** Capture is shared with V1 (V1-07): **+ New To-Do** opens `V1:iphone/capture-passport~empty`, and T13's
    Reply opens `V1:iphone/insurance-reply`. Their way back to the list (✕, ‹) returns to V2's list in a click-through.
    `iphone/list~calm` is the first-use list.

Motion (§2.9): iPhone pushes and pops; every Duo step cross-fades; selection, place and state changes are not
animated. On regular widths the in-column push plays on the to-do column alone (see below); Reduce Motion turns it into
the 0.2-s fade.

## Rules the frames keep

- **Drawn once.** While an ask's home is on screen beside the list (the to-do's card, the Conversation's header, the
  run's session), its list row only points at it: "● Which ryokan? · answer beside →". A run's ask whose door is beside
  the list (the to-do column's "From Widget" card, the Conversation header) reads "● Plan awaiting approval · open
  beside →". With the full card unfolded under a row or a header, OK steps aside. One answer gives one Undo (on the
  card; the row says "✓ You chose …"), and the choice is said once: by the card's 4-s line, then by its own round. The
  asked line only loses its blue mark.
- **One request, one name.** The pointer and the Conversation header use the ask's own title ("Plan awaiting
  approval"); the row's lead and the door card add who asks ("Widget: …", "From Widget").
- **One number.** N comes from the counts view everywhere at once (bar, header, sidebar); the other headers and chips
  count the rows drawn, so a header never disagrees with its rows. After an answer or Don't handle, every way back
  lands on a list with N = 2.
- **Section bar (E2).** Needs you · In progress · To do · Done scroll; **All is pinned at the end**, before the grouping
  icon, so the bar always shows its current choice and Needs you (N) whole. Only whole chips are drawn; a chip cut at
  the edge shows only when its label can be read (44 pt or more) and fades from the last whole chip. In a list column
  of 340 or less (Duo inner, iPad, Android) Group by moves into the list's ⋯ so In progress stays whole.
- **Whole lines.** The owner's words wrap with their last two words and the ✓ together (2 lines on the paper, in full
  in the Conversation; one line cut before the ✓ in a 335-pt half); prices never strand "a night"; durations keep
  their unit ("about 2 h."); a line cut with "…" ends on its last character, with no space before the "…".
- **Keyboard tail.** Over the keyboard the page is scrolled to its foot: the latest round sits above the composer and
  the page above it fills the room up to the folded bar, passing under its 24-pt scroll edge (blur and fade; never an empty band).
- **Anchoring.** A short thread or session starts under its header (V2-11, V1-06); a long thread keeps its latest
  round above the composer.

## Deviations from the spec text (for the lead)

- **All first in the bar**, as the owner decided on 2026-11-18 (the landing choice first; the V1-01 / V2-08 / V2-10
  wireframes draw it so too).
- **Group by in ⋯ for list columns of 340 or less** (E2 puts the grouping icon in the capsule): the icon would cut In
  progress at 320–336.
- **T02's row points at the door beside it** in V2-10, the Duo `~release` and the web `~release` (the V2-10 wireframe
  draws both the row's Review Plan › and the door card). Drawn once wins.
- **V2-03's folded subtitle** reads "● Needs you · 3 ryokans near Gion" instead of "· Which ryokan?", so the ask's
  name is said once, on the ask line under it.
- **V2-08's card body** is the wireframe's one line ("Hanami: 4 min to Gion.", no price line), and the half shows R2
  and R3 without the Background line, so the round that asked stays above the composer.
- **"Working · 2 Agents"** (V2-11's words) after approval; the Agents line under it names all three runs.

## Built locally (and why)

- **In-column push.** The kit's `push` slides the whole content, which would move the sidebar and the list. V2 frames
  on regular widths carry `col` (0 for the to-do, 1 for what replaces it) and `sel`; links use `transition: 'none'`,
  and `columnPush()` in `screens.js` animates `.v2-col` alone in `frame.html` when the tapped link sits in the to-do
  column or is a row's door (a row or sidebar pick stays unanimated). Screenshots never click, so they are unaffected.
- **Section bar.** `bar()` builds the bar from the frame's data views (the kit's `sectionBarFor` reads only the base
  data), pins All and adds the grouping icon. A V2-only wrapper around `Kit.afterRender` (`fitBar`) scrolls a chosen
  chip into view on a chip boundary and draws whole chips only.
- **After render** (same wrapper): trims CSS ellipses so "…" follows the last character; drops the typed-word quotes
  from an empty field's predictive bar; gives the Duo tab bar's items a toast (the kit's tabs take `go` only) and a
  side-column ☰ without a destination its toast.
- **Answers and Don't handle.** `ANSWER_AT` maps each option of a1 to its states on every device. The shared data has
  no declined state, so V2 builds one from the answered view (`DECLINED`: T01 Working, "Persona is continuing without
  a ryokan", the step skipped, N = 2).
- **Back to the same scroll position.** A capture-phase click listener stores the tapped Kyoto frame in `Kit.store`
  (`v2.convFrom`); the Conversation's ‹ reads it in a click-through only.
- **Shared V1 frames.** A capture-phase listener remembers that a click-through left V2 for a shared V1 frame, and
  turns that frame's way back to `iphone/list` into V2's list (`v2.return`).
- **Duo side column.** Page items are raw symbol-only items (E4): two at most under Back, 48-pt circles in one shared
  glass, so the capsule ends well above the Duo tab bar. The selected Duo tab is an accent lozenge.
- **Device chrome** (until the kit follows §3.0.2): the Duo outer home indicator is vertical on the right edge, and
  the Dynamic Island has no ring in dark.
- **Composer.** V2 draws the steward's label as the same capsule as a run's or an answer's (the kit's `label: 'above'`
  always adds ▾ and draws it plain); the field grows to 5 lines; label menus open above the label row. Keyboards get
  suggestions that follow the field (`kbFor`).
- **Web.** V2 applies E6 (radius card 10, control 8, chip 6; UI text from 13, chat body 15) under `.k-web`, and caps
  everything that replaces the to-do at the readable width.
- **Round tree.** V2's rounds (owner's words + ✓, items on ├ └ branches) are drawn in `screens.js` from Data's rounds
  with the kit's `ui.rline.*` lines; lines in the tree wrap instead of cutting, so Send Now and Undo always show.
- **Strings the data set lacks**, written once in `screens.js`: the V2-03 `#sent` round ("also a tea ceremony on
  Apr 5", "I'll look for one on Apr 5 near Gion.", "+ tea ceremony on Apr 5"), the answer's rounds ("You chose … · from
  the card", "Kaede, it's cheaper" → "You chose Kaede · from your message"), the short forms the spec writes (steps
  "Dates · Flights · Ryokan · JR passes · Days", runs "Widget · Notes · Screens", results "Flights · held", "Shortlist ·
  3", "Screenshots · 6", "What's New", Background "checked prices" / "checked prices · no change", "Hanami: 4 min to
  Gion.", "Added from the Assistant · 9:11"), V2's phrasings ("Tell Persona about this…", "1 of 3 Agents asks", "2
  Agents", "answer beside →", "open beside →"), Don't handle ("Persona won't book a ryokan. You can undo this.",
  "Persona is continuing without a ryokan").

Counts, section order, bar order and words come from `data/todos.js`, which carries the synthesis's fact-check errata
(Needs you · Working · Waiting · To do · Just finished; the bar All · Needs you · In progress · To do · Done; In progress
= Working + Waiting; "Didn't start"). V2 types no number. After Send Now the relay reads the data's "Sent now to
Release notes" (errata E3), because the kit's `rline.relay` 'now' state still says "interrupted its step".
