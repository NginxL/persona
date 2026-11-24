# Sidebars

Rules for every sidebar row: the Persona sidebar on iPhone (drawer) and iPad (column), the Mac's sections column and
Persona list, and the web sidebar. Rows get their size from one set of tokens, follow each platform's own sidebar, and
are the same size in every language (#1647). The file name is under `docs/apple/`, but the web rules are here too so
the platforms stay in step.

## Row size follows the platform

| Platform | Row | Where it comes from |
| --- | --- | --- |
| iPhone, iPad | 44 pt minimum | The system sidebar's row (`List(.sidebar)`) and the smallest touch target in the HIG |
| Mac | 24 / 28 / 32 pt | The system's Sidebar icon size (System Settings › Appearance: small / medium / large; `NSTableViewDefaultSizeMode`), medium when unset |
| Web | 32 px, 44 px for touch | The desktop sidebar row (macOS medium plus the web's larger 13 px text); `touch-hit` / `min-h-11` for fingers |

BeautifulUI keeps its own `BUISidebarRow` (32 pt), which is part of that style's spec.

The Agents list, the sidebar column beside a chat on iPad and an open iPhone Duo, keeps the same 44 pt floor for every
row, heading and fold control a finger taps, in every grouping and display (#3753): `Theme.Size.tapTarget` is its least
row height, and a row that is one button is that button from edge to edge (`docs/apple/ios.md`, "Information
architecture"). Its rows are not sidebar rows in the sense of this file: they keep the list's own fonts and insets.

## Tokens, not the text's height

A row's height is a minimum plus padding above and below its text, never the height the text happens to have. Text in
different scripts, weights or fonts then sits in rows of the same size.

- **Apple**: `Theme.Sidebar` (`apple/Shared/Design/SidebarMetrics.swift`): `row` (least height), `rowPadding` (above
  and below the text), `glyph` and `glyphColumn` (a symbol's box and the column it sits in), `gap` (symbol to words),
  `mark` (a conversation's round mark) and `radius` (the selected or hovered shade). Apply them through `scaledFrame`,
  so rows still grow with the text size:

  ```swift
  HStack(spacing: Theme.Sidebar.gap) {
      ThemedIcon(Theme.Icon.inbox).font(Theme.Font.body)
          .scaledFrame(width: Theme.Sidebar.glyph, height: Theme.Sidebar.glyph)
          .scaledFrame(width: Theme.Sidebar.glyphColumn)
      Text(title).font(Theme.Font.body).lineLimit(1)
      Spacer(minLength: 0)
  }
  .padding(.horizontal, Theme.Space.m)
  .padding(.vertical, Theme.Sidebar.rowPadding)
  .scaledFrame(minHeight: Theme.Sidebar.row)
  ```

- **Web**: `--sidebar-row` and `--sidebar-row-pad` (`web/src/styles.css`) through the `sidebar-row` utility
  (min-height, padding and a fixed 18 px line). A row adds no `py-*` of its own. The top-level headings take
  `--sidebar-heading` through the `sidebar-heading` utility (below, "The Sidebar layout's headings").

## Type

- Text uses the `Theme.Font` levels: on iOS they are text styles (`body`, `subheadline`, …), so they follow Dynamic
  Type; on the Mac they follow the app's text size. No point sizes (`.system(size:)`) in sidebar code.
- Row titles are `Theme.Font.body` on one line (`lineLimit(1)`); secondary lines and counts are `caption` / `secondary`.
- Group headings use sentence case ("Persona chats", "Recent"), never uppercase or letter spacing: Chinese and Japanese
  have no case, so all caps made English headings look heavier than the same heading in Chinese.

## Every language the same

- No language-specific branches for size: English, Chinese and Japanese use the same rows, paddings and fonts. If a
  language looks wrong, fix the token or the layout for all of them.
- The row must fit a line of CJK text without clipping: PingFang's line is taller than SF's at the same size, and the
  row's padding is what keeps its glyphs inside. Check zh-Hans screenshots, light and dark, whenever a row changes.

## Icons

Symbols in rows are `Theme.Icon` meanings drawn with `ThemedIcon` (or `Image(systemName:)` sized into `glyph` where a
wide symbol must scale down), in the style's icon language (`docs/apple/icons.md`). Every row's symbol sits in the same
`glyphColumn`, so the words line up down the column.

## Adding a row

1. Use `Theme.Sidebar` (Apple) or `sidebar-row` (web) as above; never a number, `Theme.Size.avatarL` / `box50` /
   `tapTarget`, or a row-specific radius.
2. A new Swift file that draws sidebar rows goes into `SidebarMetricsTests.files`.
3. Screenshot it in en and zh-Hans, light and dark (iPhone, iPad, Mac, web as it applies) and look at it.

## The Sidebar layout's headings

The Sidebar layout (#6332, `docs/apple/ios.md` "Sidebar layout (#6332)") heads its sections with a fold button: the name in
`captionStrong`, a chevron, what the folded section still counts, and the section's own buttons (Persona Chats' ✎). The
button is a sidebar row by size (`Theme.Sidebar.rowPadding`, `Theme.Sidebar.row`), so a finger always finds it and the
words sit as the rows' do; the fold is remembered on the device (`sidebar.fold.places`, `sidebar.fold.chats`).

On the web the top-level headings (Pages, Persona chats, Personal Agents, Agents) share one box too (#10637): the
`sidebar-heading` utility sets their title row's content height to `--sidebar-heading`, 24 px (the Agents title row's
fold-all button, its tallest control) and 44 px on a touch screen, and centres the controls beside the title in it, so
they never set its height and the headings stand evenly apart, folded or not.

## Pinned directions

The directions (`assistant.leaders`) are the group headed **Pinned** (zh-Hans 常驻, ja 常駐; the catalog key
is still `Directions`, the owner's renaming of 2026-11-11). A direction is usually its leader conversation; since #8236
it can instead be an outside reporting direction with no Persona conversation. Both kinds stay in the hub's order under
their own stable direction id. With the hub's direction tree (#4352,
`GET /api/v1/assistant/directions/tree`, `AppModel.directionTree`) each row folds open; without it (an older hub, or
before the tree is read) the rows are the plain rows they were, and with no leaders the sidebar has no
Pinned group at all.

- **Outside directions** keep the same name, "N need you" and running mark from the tree as a conversation-backed row.
  A complete `machine_id` + `session_id` target opens that existing Agent through `openTodoAgentChat`, never a made-up
  Persona conversation. A targetless direction is a plain, non-tappable row with no chevron. Neither kind has the
  conversation's long-press menu, swipe action, rename, notes or end-direction action. The `/assistant/leaders` fallback
  supplies the outside marker, name and optional target but no counts; if it answers after the tree, the tree's entry
  wins so the late fallback cannot erase counts or running. An unknown id that is neither a listed conversation nor an
  outside direction is still left out. External ids do not trigger a missing-conversation reread.

- **Row**: the leader conversation's own row as #4331 drew it (tap, long-press menu, swipe) with what its direction has
  going, read from the same tree (`DirectionTree.leader(_:)`): "N need you" in words, and one activity mark,
  `RowStateMark`'s spinner, while its own turn runs or anything under it does (the tree's `running`: an Agent of its
  to-dos, or one under it, at work, whatever the to-do's state; #6230, #6626). Its own Needs You / Unread dot wins over
  what runs under it, and VoiceOver says Working for the spinner either way. No "N working" beside the spinner: one
  meaning, one expression (#6626). A chevron button sits at its end
  (`safeAreaInset`). The button folds the row; its label is the direction's name with its "N need you", its value Expanded /
  Collapsed; it is at least `Theme.Size.tapTarget` wide and
  `Theme.Sidebar.row` high. The row's selected fill and Mac hover shade span the conversation row and the button
  together (`row(_:named:drawsShade:)` leaves its own off), as wide as every other row's. Folded by default; each device remembers each direction in
  `assistant.direction.open.<leader id>` (`@StoredValue`).
- **Open**: one row per open to-do of the direction, in the tree's order, set in as Auto Chats' sources are: its title
  and its state word in its tone (等你 Needs You's, 在做 Working's, 卡住 and 待验收 Caution's; `DirectionTaskStateWord`).
  A tap opens the to-do's card (`TodoDetailView` in a sheet, on iPhone, iPad and Mac alike). The sheet hangs on the
  direction's own row, not the to-do's: accepting or closing the to-do drops it from the tree, and folding the direction
  hides it, without closing the card. A to-do with Agents has its
  own chevron (folded by default, not remembered) that lists them a level in: the live state dot (the tree's state for
  a sub-Agent the app does not list), the title, the state's name in its tone (not the dot's color alone); a tap
  closes the drawer and opens the session (`openTodoAgentChat`). An Agent with sub-Agents has its own chevron too.
  These chevrons say what they fold ("Agents of <to-do>", "Subagents of <Agent>"), not the bare title the row's own
  button says, with Expanded / Collapsed.
- **Secretary** (书记官): no row in the sidebar (the main conversation's row stands for it, #4331); its to-dos are in the
  To-dos and Agents pages' Pinned grouping.
- **Filed sessions and All ›** (#11516): while the hub lists `direction_sessions` and the app holds the default manager's
  tree (`DirectionFilings.shows`), the open row lists after its to-dos the tree's `sessions`, the live sessions the owner
  filed there or its leader opened (`working` or `needs_you`), what needs you first, then the newest added (the hub
  gives them by computer and session id; the app orders them, `DirectionFilings.order`). They are drawn as the Agent
  rows are (state dot, title, state word, one level in from the head like the to-dos), keyed by `machine_id/session_id`,
  and a tap closes the drawer and opens the session. A last row, **All ›** (全部 / すべて; spoken "Tasks and Chats, <the
  direction's name>", and "All" for Voice Control), opens the page of everything under the direction (`DirectionPage`,
  `docs/design/DESIGN_SYSTEM.md` 3.4 "A direction's page"); on the phone it first puts the shell's Assistant page in
  front, as the drawer's other picks do, so the page opens even when Assistant was never shown in this run. "No open to-dos" shows only while there are
  neither to-dos nor filed rows. The row's "N need you" is unchanged: filings change neither `counts` nor `running`
  (whether they should is #5974). Filing takes nothing out of Recent, All Chats or the Agents list, so a filed session
  that needs you keeps its state wherever it was shown. Why a row of its own and not a section: the open row already is
  the direction's short list, and its to-dos and its filed sessions are one owner's matter; the page has the rest.
  The tree's `sessions` are taken out of the tree when it is read (`DirectionFilings.takeFiled`) into one observable box
  per direction, so a filed session's turn redraws only that direction's `DirectionFiledRows`, never the tree's readers
  (the sidebar, the To-dos and Agents groupings), which compare the tree as before #11516 (P5).
- **Move To…** (移到… / 移動…, #11516): the long-press menu of a conversation row, here and in All Chats, after Pin,
  and the Agent menus (row and the chat's ⋯, after the to-do's own entries) file the session under one of the current
  directions, by name in the tree's order, or **On Its Own**. Not offered on the main conversation, the background
  supervisor, a current leader conversation, an Agent of an open supervised to-do or a session under one (it follows
  its task; the hub's 409 `direction_supervised_task` covers a lag), or another hub's session; an Agent on the owner's own
  unsupervised to-do is offered it, and nothing is offered while a pick of the same session is out (one at a time).
  Where the session is drawn under a direction (this row, the page) it moves at once and says Moving… until the hub
  answers; a refusal puts it back with the hub's sentence (the hub's wording, which still names the group: #11395's to
  change). A change shows a toast with Undo (`Toast` with an action, as the calendar's move), which sends the answer's
  `previous` exactly, `null` written out, through the client the move went out with; the same place again says where
  it already is, without Undo. It is sent once, only while the hub is reachable, never held for later; a lost answer
  (cut off, timed out, `delivered: "unknown"`, or 503 `assistant_offline`, a cut link between hub and manager) reads
  the tree and the page again and offers no Undo (`DirectionFilings`). A switch of hub or assistant drops the moves
  out, their late answers and their toasts' Undo. A manager that answers the routes 404 or 405 without their codes
  hides that hub's entries until its next hello.
- **Unpin** keeps what was filed: while the hub files sessions the confirmation adds "What you added to it stays." in a
  string of its own (filings come back if the same chat is pinned again, `docs/assistant/unified-entry.md` "The
  direction tree").
- **Code**: `AssistantSidebarDirections.swift` (`AssistantSidebarDirection`, the rows `DirectionTaskRow`,
  `DirectionAgentRow`, `DirectionFiledRows`), `DirectionPage.swift` (the page, `MoveToMenu`), `DirectionFilings.swift`;
  `AssistantSidebar.swift` only places them where the direction rows were and resolves conversation-backed versus outside
  entries by direction id. Tests: `DirectionTreeTests`, `DirectionFilingsTests`, `AgentActionMenuTests`,
  `AssistantTopBarTests`, `AssistantLeadersTests`, `OutsideDirectionsTests` and `DirectionRowsUITests` (with
  `MOCK_DIRECTION_SESSIONS=1`); the opt-in outside-direction mock's real row interactions and sidebar / To-dos / Agents
  captures are in `OutsideDirectionsUITests` (`MOCK_OUTSIDE_DIRECTIONS=1`).

## Checks

| Test | Guards |
| --- | --- |
| `apple/PersonaTests/SidebarMetricsTests.swift` | Rows' `minHeight` is only `Theme.Sidebar.row` (headings: `HeadingMenu.minHeight`); each has `Theme.Sidebar.rowPadding`; no literal heights, vertical paddings or point sizes; the iOS row is 44 pt; a line of body text in SF and PingFang fits the row |
| Retired native Mac sidebar tests | Historical only: the Mac row was 24 / 28 / 32 pt for the system's small / medium / large Sidebar icon size; the target and its tests were removed |
| `web/src/views/SidebarRowTokens.test.ts` | `--sidebar-row` is 32 px; every sidebar row uses `sidebar-row` with no `py-*`; no heading is uppercased or letter-spaced |
| `web/src/views/SidebarHeadingBox.test.tsx`, `web/e2e/sidebar-heading-spacing.mjs` | Every top-level heading's row takes `sidebar-heading` (24 px, 44 on touch, the fold-all button's size), folded and unfolded, with both Agents lists; in Chrome, equal heights and gaps within 0.5 px |
| `apple/PersonaTests/TokenRatchetTests.swift` | Literal paddings, frames and radii in views only go down |
