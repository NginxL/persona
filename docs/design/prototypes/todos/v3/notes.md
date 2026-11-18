# V3 · Spine

Variant V3 of the To-dos redesign prototypes (synthesis §3.3, from concept E). Frames are registered in `screens.js`.
Styles are in `screens.css`. The shared kit and `window.Data` provide everything else.

## The idea

Nothing on the left ever moves by itself. Each to-do region keeps a still **spine** at its top: the living mark, the
title, the state, and the open ask while its card is out of view. Its body switches between **Details** and
**Conversation** under the spine, and one composer stays fixed at its foot.

- **The switch never moves.** Details · Conversation is the to-do region's principal bar item: the navigation bar's
  centre on iPhone, the leading item of the region's toolbar on iPad and the Duo's inner display, the top of the
  content on the Duo's outer display. The spine under it condenses freely (title 22 → title 17 · state word, plus the
  answerable ask line), so the control the owner just tapped stays under the finger.
- **Phones.** One page per to-do. The body slides between Details and Conversation (shared axis X, 0.3 s); the spine
  and the composer stay still. ‹ (or an edge swipe) always pops to the list, so a swipe never means two things.
- **Duo.** On the outer display the body cross-fades under the side column's Back and More. On the inner display,
  upright, the list keeps the left half and the to-do switches inside the right half. Nothing straddles the crease at
  334.5. On its side, the page's items move into the side column and the body cross-fades.
- **Regular widths.** The to-do region switches the same way. When it is at least 800 pt wide, the body splits into
  Details │ Conversation under one spine. On iPad that is the owner's **Focus** (⤢, kept per place until Exit Focus);
  on the web it happens when the sidebar collapses to the rail. The spine's text sits on the details' readable column.
- **Living mark** in the leading slot of rows and spines: ○ to do · ◉ needs you · the blue ring holding three small
  dots for Can wait (listed, not counted, never tickable) · ◔ working · ◎ just finished · ◌ waiting (a dashed ring with
  clock hands) · ● done (one grey fill, whoever closed it). Only ○ ticks on a tap, in place: the bar keeps its choice
  and Undo returns to the same view. Any other mark opens the to-do. A long press offers Mark Done.
- **Crew line** on to-dos with two or more runs ("● Widget ◔ Release notes ✓ Screenshots"). A capsule unfolds that
  run's card inside the spine (who, the latest line, its ask door, Open Session ›).
- **Latest.** The details' conversation block is one line, "Latest · You “…” ›", because the full zoom is one switch
  away. The lines a send adds (its status, the reply, relays) sit on the quote's edge.
- **Scroll edges.** The spine's scroll-edge blur shows only while the body passes under it, and the frames pick a
  scroll position where the edge falls on a gap (no bubble or line is sliced). The details above the keyboard sit at
  the top while they fit and at their foot once they overflow, the last line clear of the composer. On the lists, rows
  passing under the New To-Do pill and the home indicator soften into a bottom scroll edge.

## Named deviations from the §3.3 text

- **"Details", not "Paper".** §2.11 lists Paper as internal only, so the segment reads Details · Conversation.
- **The switch is in the bar**, not at the foot of the spine (craft review: a segmented control must not move when
  tapped). The spine is then 76–96 pt, under the spec's 108 that included the switch.
- **Selected rows point, they don't answer.** Beside an open to-do region, the selected row's ask shrinks to
  "Which ryokan? · answer beside →" (§2.7, drawn once per screen); the V3-08 and V3-11 wireframes still draw the
  buttons.
- **Steps fold to one line** ("Steps 2 of 5 ✓ ✓ ◉ ○ ○ ›") on phones, the Duo and the regions beside a list, so Results
  and Latest stay above the composer; the tap unfolds them (`iphone/kyoto-paper~steps`). Focus shows the full list.
- **The thread's pointers** read "· answer above ↑" under a spine and "· answer beside" in Focus.
- **The section bar** pins All (where the list lands) in the trailing slot next to the grouping icon, past a hairline,
  so Needs you and its count lead the row at every width; Done shows only while it has rows (E2). It draws whole chips
  only: chips are 6 apart inside (E2 asks 8) so Needs you 3 · In progress 5 · To do 2 · All fit a 402 iPhone; in
  narrower list columns the chips that don't fit move into the grouping icon's menu, and at 320 (iPad 11″, the Duo on
  its side) Group by moves to the column's ⋯ so In progress 5 stays readable beside Needs you.
- **List columns** (the Duo, iPad, the web, Android) show line 3 ("Weather App · Steps 1/3") instead of the crew
  capsules, which don't fit whole there; the iPhone list keeps the capsules.
- **The condensed spine's door** reads "● Widget · needs your OK [Review Plan ›]": it names the run (the crew line is
  hidden) and fits whole beside its button on a 402 iPhone.
- **The plan card's link row** keeps only Later: Don't Start is that ask's Don't handle (the V1-06 wireframe has no
  link row; §2.7 and E7 want one).
- **Capture.** Behind the capture sheet the bar counts To do 1, since Renew passport does not exist yet; Add files it
  and the count returns to 2 (the kit's `Data.view` has no "not captured yet" state, so the frame passes the count).
- **No "scrolled details" state on iPhone**: with Steps folded, T01's details fit the screen, so the condensed spine
  on the Details zoom shows while typing (`~typing` and the send states) instead.
- **The Duo's details at rest** (`duo-outer/kyoto-paper`, `duo-inner-landscape/list-paper`) show the card, chips,
  notes, Steps (and Results on its side); Results and Latest are one scroll below, where the spine condenses. On the
  iPhone and the upright inner display everything down to Latest is above the composer.
- **The answer is said once.** While the spine shows "✓ You chose Ryokan Hanami · Undo" (4 s), the round shows only the
  run the answer started; the decision line comes after.

## Frames (★ = the spec's eight key frames)

| Spec | Frame id | States |
| --- | --- | --- |
| V3-01 ★ | `iphone/list` | `~options` `~options-photos` `~answered` → `~booking` (timed) `~back` → `~back-moved` (timed) `~approved` `~can-wait` `~mark-done` `~filter-needs` `~filter-progress` `~filter-todo` `~ticked` `~end` `~end-ticked` `~calm` `~calm-ticked` `~drawer` `~menu` `~group` `~more` `~added` |
| V3-02 ★ | `iphone/kyoto-paper` | `~steps` `~answered` `~later` `~menu` `~typing` `~discuss` `~sending` → `~processing` → `~delivered` → `~sent` (timed) `~followup` |
| V3-03 ★ | `iphone/kyoto-conversation` | `~options` `~answered` `~run` `~typing` `~discuss` `~sending` → `~processing` → `~delivered` → `~replied` (timed) |
| V3-04 ★ | `iphone/kyoto-conversation-keyboard` | — |
| V3-05 | `iphone/release-paper` | `~run-card` `~work` `~to-menu` `~approved` `~s5` `~sending` → `~processing` → `~sent` → `~taken` (timed) `~send-now` |
| (V3-05) | `iphone/release-conversation` | `~approved` `~s5` `~sending` → `~processing` → `~sent` → `~taken` (timed) `~send-now` |
| V3-06 | `iphone/release-widget-session` | `~approved` |
| (V3-01 ◎) | `iphone/dentist-paper` | `~done` |
| (§3.4) | `iphone/assistant-tray`, `iphone/lock-push`, `iphone/capture-passport` | `~answered`; `~answered`; `~empty` `~parsed` |
| V3-07 ★ | `duo-outer/kyoto-conversation-keyboard` | `~paper` |
| (V3-07) | `duo-outer/kyoto-paper`, `duo-outer/kyoto-conversation`, `duo-outer/list` | — |
| V3-08 | `duo-inner-portrait/list-paper` | `~draft` |
| V3-09 ★ | `duo-inner-portrait/list-conversation` | `~draft` `~focus` |
| (rule 3) | `duo-inner-landscape/list-conversation`, `duo-inner-landscape/list-paper` | — |
| V3-10 | `ipad11-landscape/paper` | — |
| V3-11 ★ | `ipad11-landscape/conversation` | — |
| V3-12 ★ | `ipad11-landscape/focus-split` | `~list-over` |
| V3-13 | `ipad13-landscape/conversation`, `ipad13-landscape/paper`, `ipad13-landscape/focus-split` | `~list-over` |
| V3-14 | `V3:web/rail-split` (the kit's `web` device, 1440 × 900) | `~sidebar` (before the rail) |
| V3-15 | `android-fold/list-conversation` | `~back` |

The ids use the kit's device ids (`web`, `android-fold`), and `-` replaces the spec's `+` (`list+paper` is
`list-paper`), because frame ids allow letters, digits and `/ _ . -` only.

## How to walk it

Open `frame.html#v=V3&f=iphone/list` in Chrome. Turn on Hotspots (`h`) to see what is tappable; orange outlines are
toasts for places the prototype doesn't build. The §3.4 tasks:

1. **Counts (S7).** On the list, Needs you (3) leads the bar and heads the first section; In progress counts Working
   and Waiting (5). The drawer (☰) shows To-dos (3) and each project's part; `iphone/assistant-tray` shows
   Needs you · 4 = To-dos · 3 + Elsewhere · 1.
2. **Answer from outside (S2).** `iphone/lock-push`: Ryokan Hanami (Recommended) answers on the lock screen; the Fleet
   Live Activity counts sessions ("2 need you").
3. **Answer from the row (S2).** Tap **OK: Ryokan Hanami**: line 2 reads "✓ You chose Ryokan Hanami · Undo" and N
   drops to (2). After 4 s the row moves into Working. **Options ⌄** unfolds the full card in place (the row keeps only
   Options ⌃).
4. **Tell Persona from the details (S5 lite).** Tap the T01 row, then the composer: the spine condenses and keeps the
   title, the state and the ask above the keyboard. Tap ↑ to see Sending → Processing → Delivered to Persona, then the
   reply under Latest. Nothing opens.
5. **Longer conversation.** Tap **Conversation** in the bar (or "Latest ›"). The body slides left under the still
   spine, the switch stays where it was, and the composer stays. **Details** slides it back. On iPad, open
   `frame.html#v=V3&f=ipad11-landscape/conversation` and tap **⤢ Focus**.
6. **Approve Widget's plan (S4).** Open Release 1.3. Tap the **Widget** capsule to unfold its run card in the spine,
   then **Review Plan ›** and **Approve**. ‹ goes back to where you started, now Working with N (2).
7. **Scope change mid-turn (S5).** On Release 1.3, tap the composer, then ↑. You see Delivered to Release lead, the
   relay "Queued for Release notes · after this step" with **Send Now**, and the scope line with Undo. After 6 s it is
   taken; **Send Now** shows "Sent now to Release notes".
8. **Failed start (S8).** On the list, tap **Start When It's Back**: "✓ Starts when Desk Mac is back · Undo" in place,
   N drops to (2); after 4 s the row re-files into Waiting.
9. **Fold and unfold (S9).** Open `frame.html#v=V3&f=duo-outer/kyoto-conversation-keyboard`, then use the strip's
   **Unfold**. The draft "also check breakf", the selection and the Conversation zoom survive. Inside, ⤢ gives
   Details ┊ Conversation.
10. **Capture (S1).** Tap **New To-Do** on the list: the capture sheet (Add and Add & Ask Persona stay disabled until
    there is text); type, Return, Add → the list scrolls to the new row, lit for a moment, with "Added · Undo".
11. **Tick (S1 later).** Scroll the list to its end (`iphone/list~end`) and tap ○ on Renew passport: it ticks in place
    under All; Undo returns to the same view. From the To do chip it ticks under To do.

A right-click on ◔ "Draft the school newsletter" stands in for a long press (Mark Done). The timed states (Sending →
Processing → Delivered, the 4-s answers, the 6-s relay) play only in a click-through, never in screenshots. Reduce
Motion (`m`) turns the body slide into a 0.2 s fade.

## Local stand-ins for kit fixes

`screens.js` and `screens.css` work around these in V3 only (each is listed for the kit): the section bar's whole-chip
layout, pinned slot and Done chip; the composer placeholder cut at a word ("Message about this…"); the faded leading
edge of a cut draft; the Duo side column's page items (48-pt circles, no names), its rail foot on the inner display on
its side (and taps that toast), the tab bar's selection look and the vertical home indicator; the keyboard's "to
measure" label (in the keyboard's foot, so the predictive bar keeps three words); the dashed tabletop fold; the dark
Dynamic Island outline; the place summary's E1 order; the Agents row's count in the sidebar; the relay line's "Sent
now" wording; the folded steps' marks; the web bar's selection look; the capture sheet's empty state and docking; the
plan card's link row; the bottom scroll edge under floating controls; the answered line's separator; orphan words in
replies; and results that stack their title when the chips would be cut.

## Screenshots

```sh
tools/shoot-all.sh V3 <out dir>        # every frame, light and dark
```
