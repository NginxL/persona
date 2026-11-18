# V1 · Paper and Pane

The recommended direction (synthesis §2, frames §3.1). Files: `v1/screens.js` (frames), `v1/screens.css` (styles scoped
to `[data-variant="V1"]`).

## The idea

- **A to-do is one page, the paper.** Top to bottom: tick and title, the state word with the hub's sentence, Since you
  looked, the request card at its home (or a door "From Widget"), chips, notes or What you asked, Persona can help on a
  fresh to-do, Steps with who has each, Results, the Conversation block (the latest two rounds, one line each) and, for
  code work only, one folded line Work details.
- **Telling it something needs no conversation.** The composer is docked at the paper's foot and names its addressee
  ("To Persona", "To Release lead ▾", purple "To Widget ▾" in a run). With the keyboard up the paper folds: the title
  and state stay in the bar, the request becomes one line, and the latest rounds sit right above the composer. The
  message states run Sending → Processing → Delivered to X, and the reply lands on the paper. Nothing opens.
- **Talking opens the Conversation on the right.** A pane at regular widths (iPad, the open Duo, the web, Android), where
  the leftmost region steps aside in the same transition so at most three regions show; a push with the to-do pinned
  as its header on phones; a cross-fade on the iPhone Duo. The paper's block and the pane are one record at two zooms:
  while the pane shows, the block is the one line "Conversation · open beside →".
- **A request is drawn once per screen.** While the pane or a pushed session shows a request's home, its door shrinks
  to "Plan awaiting approval · answer in the conversation →". A safe choice answers on the row, the card, the pinned
  header, the tray and the lock screen; a plan always opens its home (Review Plan ›).
- **One set of numbers.** Every count comes from `window.Data`, and answering in a state frame redraws from
  `Data.view(…)`, so N drops in the sidebar, the bar, the section header, the tray and the Live Activity together.

## Frames

★ = important (the spec's key frames, plus the answered paper and the sent quick note). States follow their frame.

| Spec | Frame | States |
| --- | --- | --- |
| V1-01 ★ | `iphone/list` | `~scrolled` `~kyoto-answered` → `~kyoto-booking` (4 s) `~options` `~options-t03` `~back` `~can-wait` `~swiped` `~ticked` `~passport-added` `~filter-needs` `~filter-progress` `~filter-todo` `~calm` `~drawer` `~menu` `~more` `~group` `~today` `~kyoto` |
| V1-02 ★ | `iphone/kyoto` | `~scrolled` `~answered` ★ → `~answered-round` (4 s) `~answered-kaede` `~answered-tsuki` `~later` `~dont-confirm` `~dont` `~menu` `~from-assistant` |
| V1-03 ★ | `iphone/kyoto-quicknote-keyboard` | `~typing` `~sent` → `~processing` → `~delivered` → `~reply` ★ (timed) `~answer-chip` `~answered` `~answered-msg` |
| V1-04 ★ | `iphone/conversation-kyoto-keyboard` | `~keyboard-down` `~moved-draft` `~options` `~answered` `~run-open` `~bg-open` `~menu` |
| V1-05 | `iphone/release`, `iphone/conversation-release` | `~scrolled` `~work` `~chip-menu` `~approved` `~s5` `~s5-sending` → `~s5-processing` → `~s5-delivered` → `~s5-taken` (6 s) `~s5-now` `~s5-undone` |
| V1-06 | `iphone/release-widget-session` | `~approved` `~menu` |
| V1-07 | `iphone/capture-passport`, `iphone/passport` | `~empty` `~parsed`; the paper: `~asked` → `~asked-processing` → `~asked-delivered` → `~asked-reply` `~steps` `~kept` `~do-it` `~help-closed` |
| V1-01 (Reply) | `iphone/insurance-reply` | the Can wait row's Reply: the generic paper, composer focused |
| V1-08 | `iphone/lock-push` | `~answered-from-lock` `~approved` |
| V1-09 | `iphone/assistant-tray` | `~answered` `~back` `~allowed` `~in-progress` `~closed` `~drawer` |
| V1-10 | `duo-outer/conversation-kyoto-keyboard`, `duo-outer/release-widget-session` | `~paper` |
| V1-11 ★ | `duo-inner-portrait/list-paper` | `~release` `~sidebar` |
| V1-12 ★ | `duo-inner-portrait/paper-conversation` | `~list-over` |
| V1-13 | `duo-inner-landscape/release-pane-on-widget` | `~approved` `~channels` `~steward` `~channels-steward` `~closed` `~sidebar` |
| V1-14 ★ | `ipad11-landscape/closed` | `~kyoto` `~kyoto-place` `~today` `~approved` |
| V1-15 ★ | `ipad11-landscape/open` | `~before-send` `~sent` → `~processing` → `~delivered` → `~taken` (6 s) `~send-now` `~undone` `~channels` `~widget` `~channels-widget` `~widget-approved` `~sidebar-over` |
| V1-16 | `ipad13-landscape/summary`, `ipad13-landscape/paper`, `ipad13-landscape/open` | `~lit-needs` `~lit-working` `~lit-waiting` `~lit-todo` `~lit-just_finished` (each back after 1 s) `~can-wait`; `open~sidebar-over` |
| V1-17 | `V1:web/open` (the kit's `web` device, 1440 × 900) | `~closed` |
| V1-18 | `android-fold/paper-conversation` | `~list-paper` |
| also in V1 | `ipad11-portrait/closed`, `ipad11-portrait/open` | `~sidebar`; `~list-over` |

Frame ids use the kit's device ids (web, `android-fold`) and `-` where the spec writes `+` (frame ids take letters,
digits and `/ _ . -`).

## Walking it

Open `frame.html#v=V1&f=iphone/list` (Hotspots shows what is tappable; an unbuilt destination shows a toast "Opens: …").
The owner's nine tasks of synthesis §3.4:

1. **S7, the counts.** `iphone/list`: the drawer (☰), the bar and the Needs you header agree; `iphone/assistant-tray`
   shows the same N under "To-dos" plus "Elsewhere", adding up to its header. On a wide screen,
   `ipad13-landscape/summary` says it in one glance; each line lights its section.
2. **S2, answer from the row, then the lock screen.** OK: Ryokan Hanami on the row (`~kyoto-answered`, then it re-files
   into Working); on `iphone/lock-push`, Ryokan Hanami (Recommended).
3. **S5 lite.** Kyoto row → the paper → tap the composer → type → ↑: Sending, Processing, Delivered to Persona, the
   reply. The paper never leaves the screen.
4. **A longer exchange.** ◫ on the paper pushes the Conversation with the to-do pinned (`iphone/conversation-kyoto-keyboard`);
   on iPad, Conversation opens the pane beside the paper (`ipad11-landscape/open`).
5. **S4, approve Widget's plan.** Release row → Review Plan › → the Widget run (its home) → Approve → ‹: the door is gone
   and N dropped. On iPad: Review Plan › opens the pane on the Widget channel and the door becomes the pointer.
6. **S5, a scope change mid-turn.** iPad: Conversation → `~before-send` → ↑ → Queued for Release notes · after this
   step [Send Now] → Delivered to Release notes after 6 s; Undo on the scope line. On the phone the same in `iphone/release~s5`.
7. **S8.** Start When It's Back on "Sort last year's photos": one tap, the row moves to Waiting.
8. **S9, fold mid-draft.** `duo-outer/conversation-kyoto-keyboard` → the strip's Unfold → `duo-inner-portrait/paper-conversation`
   with the draft and caret; Fold returns. ✕ cross-fades each half back to list ┊ paper.
9. **S1.** + New To-Do → type → tap the dashed chip (Return) → Add (`~passport-added`), or Add & Ask Persona → the new
   paper with five things; `iphone/list~calm` is the first-use list.

Timed steps (Sending → Processing → Delivered, the 4-s answered line, the 6-s relay) run only in a live click-through,
never in screenshots.

## Choices and deviations

- **Words and numbers follow the shared data**, including the fact-check errata it now carries (section order, the
  Inbox's chips with All first, In progress = Working + Waiting with Just finished uncounted at its foot, "Didn't start",
  "Sent now to …"). Two shared components still draw older forms, so V1 draws them from the data itself: the relay
  line's Send Now state and the Fleet Live Activity, which counts sessions (at most two, buttons on the first
  actionable one only). The place summary's lines are ordered by the data's section order.
- **The section bar** has the Inbox's chips with the landing choice first (the owner's decision of 2026-11-18): All ·
  Needs you (3) · In progress 5 · To do 2, the grouping symbol inside the capsule's trailing end (Group By: Phase ·
  Project · Due Date, `iphone/list~group`); the list's ⋯ keeps Sort and Show. Done shows only while it has rows, so the
  mock offers no Done chip (and no Done filter frame). To-dos lands on All at the strip's start, so Needs you with its
  blue N always shows beside it. In the list columns (320 to 360 pt) In progress and To do drop their neutral counts so
  more chips show; what is cut fades at the edge. The neutral counts stay in the place summary and the tray.
- **Counts move once.** Answering on a row drops N at once (header, bar, drawer, tray); In progress keeps its number
  until the row re-files into Working (`~kyoto-answered` 5 → `~kyoto-booking` 6), in the list and in the tray alike.
- **Drawn once beside the paper** (§2.7, §2.5 one primary): wherever the selected row's paper is on screen (the open
  Duo, iPad, the web, Android), the row keeps its line 2 ("● Which ryokan? · Hanami recommended") and drops its
  buttons; the card or the door on the paper is the one place to answer. **For the lead designer:** the V1-11, V1-14
  and V1-17 wireframes draw the row's answer beside the card; V1 follows §2.7 instead.
- **The pinned header** (V1-04, V1-10, the Release conversation) reads "‹ title ⋯" over "● Needs you · Which ryokan?"
  over "[OK: Hanami] Options ⌄" (§3.0.4: row 2 is the state word and the ask). While Options ⌄ unfolds the card, the
  header's OK folds away and "Options ⌃" stands where it stood: one primary per request on screen. Once answered it
  reads "◐ Working · Persona is booking Ryokan Hanami".
- **The bar shows a title only once it scrolls away** (2.5, element 1), on Android too.
- **The folded paper** (keyboard up) is the paper scrolled to its end: the title with the state under it inside the
  bar (17 over 13, ending before the trailing group), the ask folded to one line, then what the paper still says
  (Since you looked, the chips, the notes, What you asked, "Steps 2 of 5 · Results 2"; on Release the steps with who
  has each, since the S5 note is about one of them) and the latest two rounds right above the composer. When that is
  taller than the room, its top passes under the bar's scroll-edge fade, so no blank band opens and a sent note's
  reply lands where it was typed. **For the lead designer:** §3.1 V1-03 collapses chips and notes into the steps
  line; with them collapsed the ≈ 98 pt of scroll room shows as an empty band above the rounds, so V1 keeps them.
- **The composer** says "Message about this to-do…" on the wide papers (iPad 13″, the web) and "Message…" on phones,
  the Duo and in every pane, where the chip, mic and send leave about 170 pt and the sentence would be cut ("Message
  about this to-…"). A focused empty field shows its caret. Typed text
  wraps and the field grows to three lines with the chip, mic and send on the last line, as Messages does; a wide chip
  ("Answer: Which ryokan? ×", "To Release lead ▾") stands above the field while there is text, so the draft keeps
  the width.
- **Threads** fade out under their header (iOS 26's scroll-edge effect), so no line is sliced at the edge.
- **Duo fold targets:** the inner display folds to the outer display's equivalent (the Kyoto paper or its
  Conversation; the Widget run), not to an iPhone frame.
- **The answered round** appears as the newest line of the Conversation block, in time order as in the thread and the
  folded paper (§3.1 V1-02 says "at the top of the block"; the block's other rounds read oldest first, so the newest
  last keeps one order). Its line reads "You · now  chose Ryokan Hanami (from the card)", as every round does.
- **The Duo side column** follows E4: Back alone (a 48-pt glass circle), the page's other items 12 pt below it as 48-pt
  symbol-only circles in one shared glass, 4 apart (the name is the accessibility label); at the foot the face at the
  very bottom, New Chat above it, and the Duo tab bar 8 pt above them, on the inner display in landscape too. The
  outer display's home indicator runs vertically on its right edge. V1 draws the items itself (the kit's column draws
  named tiles), so unbuilt items show their toast. The chosen Duo tab is the accent symbol and name on a light well.
- **The tabletop fold** on the inner display on its side is drawn as dashes in the margins at each pane edge and across
  the side column, so the hint never runs through a line of text.
- **The Duo inner portrait keyboard** is drawn in two blocks with a 12-pt gutter on the crease (q–t | y–p, a–g | h–l,
  ⇧ z–v | b–m ⌫), so no key straddles it; the sidebar laid over the left half fills it up to the crease.
- **Drawn once on wide screens:** while the pane shows the Widget run (the plan's home), the paper's door shrinks to
  the pointer; while the pane shows a round with the scope change, the paper's scope line is plain text and the round's
  Undo is the one control.
- **S5 Undo** removes the scope: the round reads the reply, the relay, "Scope change undone", and the run line goes
  (Release notes is no longer adding the widget), in the same order on the paper and in the pane.
- **The plan card at its home** adds the link row "Later" under Approve, so it offers the three outcomes (Approve,
  Don't Start, Later) every ask has (§2.7); the V1-06 wireframe omits it.
- **The place summary** says "12 open: 11 in Kyoto trip, Weather App and Home, 1 without a project", so its numbers
  add up as read; on Today the list's empty state keeps only its line and the summary beside it links "3 coming up
  this week". Each summary line scrolls the list to its section as far as the list's end allows.
- **Look:** glass carries no drop shadow (a 0.5-pt stroke only, §3.0.2), secondary text in light uses the solid gray
  (4.5:1, §2.10), the swipe's Someday is indigo, and the tray's rows draw their line as the list's line 2.
- **Web** frames use E6's tokens: cards 10, controls 8, chips 6, chat text 15; the paper's toggle reads
  "Conversation ⌘B". The sidebar's Agents row carries the same count (2) as the rail and the Duo tab bar.
