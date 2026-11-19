# To-do layout fixtures

The To-dos window rule (#10465; `docs/design/proposals/todos-redesign.md` ("3.8 Layout per device: the window rule")): a
window, its text size and what the owner asked for → which regions show, in which form and how wide.
`scripts/test/test_todo_fixtures.py` checks the format, that the regions fill the window, that every window of the
proposal's table is here at the default text size with the conversation closed and open and the table's widths, and
holds every case to the rule below. No client reads them yet: each will replay them with its own pure function
(proposal 3.8's `todoPanes`), never in the shared core (proposal 7.2), iOS first (#10465), the web and Android from
their stages.

| File | What it pins | Cases |
| --- | --- | --- |
| `todo_layout.json` | A window and what is wanted → four regions, each with a form and a width | 60: every window of proposal 3.8's table with the conversation closed and open, the text sizes that drop a region, the 24-pt hysteresis, regions called over the page, and the iPhone Duo's halves under `crease_split` |

## Format

`{"version": 1, "cases": [...]}`. A case is `{name, window, description, source, input, expected}`; the descriptions
are English.

- `window`: the row of proposal 3.8's table the case is: `iphone`, `duo_outer`, `duo_inner_portrait`,
  `duo_inner_landscape`, `ipad_mini_portrait`, `ipad11_portrait`, `ipad13_portrait`, `ipad11_landscape`,
  `ipad13_landscape`, `ipad_window` (600–759 pt wide), `web_1440`, `web_1600` (the narrowest of "1600 and wider"),
  `android_book`; or `other`.
- `input.width`, `input.height`: the window in points (the scene's, never the screen's), side column included.
- `input.side_column`: what the iPhone Duo's side bars take from the width (84, the safe-area inset the system gives
  them), else 0. The regions share the room, `width − side_column`.
- `input.text_scale`: `Theme.sizeScale` at the interface's text size, 1 at the default: xLarge 1.1176 (19/17),
  xxLarge 1.2353, xxxLarge 1.3529; below 1 at the smaller sizes. The interface's text size stops at xxxLarge
  (`Theme.interfaceTypeSize`), and so does this scale, though `Theme.sizeScale` itself goes on to 1.6 at the
  accessibility sizes: proposal 3.8's minimums grow "up to the xxxLarge cap".
- `input.posture`: `flat`, or `half_folded` (a foldable held like a book).
- `input.device`: `iphone`, `duo_outer` (the iPhone Duo folded), `duo_inner` (open), `ipad`, `web`, or `android` (a
  foldable's inner display).
- `input.crease_split`: owner decision 2 (iv), the Duo's inner display as two panes at the crease with a 320-pt paper
  minimum for To-dos. False until the Duo's measurement on the simulator (proposal 3.9) and the owner decide; the
  Duo's cases with it true pin what the decision would give. True for an Android foldable in book posture, whose
  window splits at its hinge.
- `input.wants.deepest`: the deepest region the owner asked for: `list` (a place, nothing selected), `paper` (a to-do)
  or `conversation` (its conversation open).
- `input.wants.called` (optional): a region the owner called over the page: `sidebar` (☰, or the leading-edge slide)
  or `list` (‹ List).
- `input.previous`: the window's previous layout, `{text_scale, wants, regions}`, or `null` for its first.
- `expected.regions`: `{region, form, width}` for `sidebar`, `list`, `paper` and `conversation`, in that order. Forms:
  `beside` (a column of the window), `over` (laid over the page, which keeps its columns), `pushed` (the one page, over
  the others), `hidden`, and on the web `rail` (the sidebar drawn as its rail). Widths are points to two decimals:
  compare within 0.01. A hidden region is 0 wide.

All windows are the V1 prototype's devices (`docs/design/prototypes/todos/README.md`) or plain sizes.

## The rule

Tokens, multiplied by `text_scale` except the sidebar's (proposal 3.8: minimums scale with the text, so larger text
drops a region by arithmetic, with no accessibility-size branch):

| Token | iOS | Web |
| --- | --- | --- |
| Sidebar (not scaled) | 400 on the iPad (`Theme.Size.sidebar`); 320 on the Duo (`Theme.Size.sidebarPhone`) | 284, or its rail 56 |
| List | 300 in iPad windows 600–759 pt wide; else 340 where the window's shorter side is 1032 or more (the 13-inch iPad, either way up); else 320 | 360 |
| Paper minimum | 400 (`Theme.Size.splitPageMin`); 320 on the Duo's inner display with `crease_split` | 400 |
| Conversation | 400 | 440 |
| Hysteresis | 24 | 24 |

1. **Phones** (`iphone`, `duo_outer`) show one page: the list (`beside`, all the room) when nothing is selected, else
   the deepest region `pushed` over it. A called sidebar is the app's drawer over the page: 82% of the room, at most
   380.
2. **A crease** (`crease_split` on `duo_inner` or `android`, the window taller than wide): the leading half is
   `floor(room / 2)` and the trailing half the rest. While the leading half holds 320 × `text_scale`, list ┊ paper take
   the halves, or paper ┊ conversation with the list stepped aside; a called sidebar or list lies over the leading half.
   Otherwise the window is one page, as a phone's, and a called sidebar lies over it at 320.
3. **Every other window** shows the deepest region asked for and as many regions to its left as fit, the leftmost
   giving way first:
   - With the conversation, it is `beside` while the paper's minimum and the conversation fit, else `pushed` and
     nothing else shows. The list is beside while list, paper minimum and conversation fit; the sidebar while it fits
     beside those too; on the web the sidebar becomes its rail when only the rail fits.
   - Without it, list and paper are beside while the list and the paper's minimum fit, else one page: the list alone
     (`beside`) for `list`, the paper `pushed` for `paper`. The sidebar is beside while it fits beside them; where it
     does not, it is hidden while To-dos is in front (the per-place overlay, decision 2 (iii)).
   - The paper takes the room the others leave. With nothing selected its region holds the place summary.
   - A called sidebar lies over the page at its own width while it is hidden; a called list lies over the paper at its
     width while it has stepped aside.
4. **Hysteresis.** A region the previous layout showed beside (or as the rail) keeps that form while the room is
   short of what it needs by at most 24 pt; a region that did not show in a form comes back in it only once it fits,
   so a rail held is only the rail, and the full sidebar returns once it fits. The band holds between layouts at the
   same text size with the conversation open in both or closed in both: a selection keeps it, while changing the text
   size or opening or closing the conversation decides afresh.

## Cases

| Case | Window | Wants | Regions shown |
| --- | --- | --- | --- |
| `iphone_list` | 402 × 874 | list | List 402 |
| `iphone_paper` | 402 × 874 | paper | Paper pushed 402 |
| `iphone_conversation` | 402 × 874 | conversation | Conversation pushed 402 |
| `iphone_drawer` | 402 × 874, sidebar called | list | Sidebar over 329.64 │ List 402 |
| `duo_outer_paper` | 466 × 678, side column 84 | paper | Paper pushed 382 |
| `duo_outer_conversation` | 466 × 678, side column 84 | conversation | Conversation pushed 382 |
| `duo_inner_portrait_halves` | 669 × 951, crease_split | paper | List 334 │ Paper 335 |
| `duo_inner_portrait_halves_half_folded` | 669 × 951, crease_split, half-folded | paper | List 334 │ Paper 335 |
| `duo_inner_portrait_pane` | 669 × 951, crease_split | conversation | Paper 334 │ Conversation 335 |
| `duo_inner_portrait_sidebar_over_the_half` | 669 × 951, crease_split, sidebar called | paper | Sidebar over 334 │ List 334 │ Paper 335 |
| `duo_inner_portrait_list_over_the_half` | 669 × 951, crease_split, list called | conversation | List over 334 │ Paper 334 │ Conversation 335 |
| `duo_inner_portrait_one_page` | 669 × 951 | paper | Paper pushed 669 |
| `duo_inner_portrait_one_page_conversation` | 669 × 951 | conversation | Conversation pushed 669 |
| `duo_inner_portrait_list_alone` | 669 × 951 | list | List 669 |
| `duo_inner_portrait_one_page_sidebar_over` | 669 × 951, sidebar called | list | Sidebar over 320 │ List 669 |
| `duo_inner_portrait_halves_at_xlarge` | 669 × 951, xLarge, crease_split | paper | Paper pushed 669 |
| `duo_inner_portrait_pane_at_xlarge` | 669 × 951, xLarge, crease_split | conversation | Conversation pushed 669 |
| `duo_inner_portrait_text_size_skips_the_band` | 669 × 951, xLarge, crease_split, after a layout | paper | Paper pushed 669 |
| `duo_inner_landscape_closed` | 951 × 669, side column 84 | paper | List 320 │ Paper 547 |
| `duo_inner_landscape_open` | 951 × 669, side column 84 | conversation | Paper 467 │ Conversation 400 |
| `duo_inner_landscape_sidebar_over` | 951 × 669, side column 84, sidebar called | paper | Sidebar over 320 │ List 320 │ Paper 547 |
| `duo_inner_landscape_open_at_xlarge` | 951 × 669, side column 84, xLarge | conversation | Conversation pushed 867 |
| `duo_inner_landscape_open_at_xlarge_with_the_320_paper` | 951 × 669, side column 84, xLarge, crease_split | conversation | Paper 419.96 │ Conversation 447.04 |
| `ipad_mini_portrait_closed` | 744 × 1133 | paper | List 300 │ Paper 444 |
| `ipad_mini_portrait_open` | 744 × 1133 | conversation | Conversation pushed 744 |
| `ipad11_portrait_closed` | 834 × 1194 | paper | List 320 │ Paper 514 |
| `ipad11_portrait_open` | 834 × 1194 | conversation | Paper 434 │ Conversation 400 |
| `ipad11_portrait_sidebar_over` | 834 × 1194, sidebar called | paper | Sidebar over 400 │ List 320 │ Paper 514 |
| `ipad11_portrait_list_over` | 834 × 1194, list called | conversation | List over 320 │ Paper 434 │ Conversation 400 |
| `ipad11_portrait_open_at_xlarge` | 834 × 1194, xLarge | conversation | Conversation pushed 834 |
| `ipad13_portrait_closed` | 1032 × 1376 | paper | List 340 │ Paper 692 |
| `ipad13_portrait_open` | 1032 × 1376 | conversation | Paper 632 │ Conversation 400 |
| `ipad13_portrait_open_at_xxlarge` | 1032 × 1376, xxLarge | conversation | Paper 537.88 │ Conversation 494.12 |
| `ipad13_portrait_open_at_xxxlarge` | 1032 × 1376, xxxLarge | conversation | Conversation pushed 1032 |
| `ipad11_landscape_closed` | 1194 × 834 | paper | Sidebar 400 │ List 320 │ Paper 474 |
| `ipad11_landscape_open` | 1194 × 834 | conversation | List 320 │ Paper 474 │ Conversation 400 |
| `ipad11_landscape_open_sidebar_over` | 1194 × 834, sidebar called | conversation | Sidebar over 400 │ List 320 │ Paper 474 │ Conversation 400 |
| `ipad11_landscape_at_xlarge` | 1194 × 834, xLarge | paper | List 357.63 │ Paper 836.37 |
| `ipad11_landscape_open_at_xlarge` | 1194 × 834, xLarge | conversation | Paper 746.96 │ Conversation 447.04 |
| `ipad13_landscape_summary` | 1376 × 1032 | list | Sidebar 400 │ List 340 │ Paper 636 |
| `ipad13_landscape_closed` | 1376 × 1032 | paper | Sidebar 400 │ List 340 │ Paper 636 |
| `ipad13_landscape_open` | 1376 × 1032 | conversation | List 340 │ Paper 636 │ Conversation 400 |
| `ipad_window_list_and_paper` | 700 × 900 | paper | List 300 │ Paper 400 |
| `ipad_window_open` | 700 × 900 | conversation | Conversation pushed 700 |
| `ipad_window_one_page` | 650 × 900 | paper | Paper pushed 650 |
| `ipad_window_shrinks_inside_the_band` | 690 × 900, after a layout | paper | List 300 │ Paper 390 |
| `ipad_window_shrinks_past_the_band` | 670 × 900, after a layout | paper | Paper pushed 670 |
| `ipad_window_grows_inside_the_band` | 690 × 900, after a layout | paper | Paper pushed 690 |
| `ipad_window_selection_keeps_the_band` | 690 × 900, after a layout | paper | List 300 │ Paper 390 |
| `ipad_split_sidebar_held` | 1110 × 834, after a layout | paper | Sidebar 400 │ List 320 │ Paper 390 |
| `ipad_split_sidebar_first_layout` | 1110 × 834 | paper | List 320 │ Paper 790 |
| `ipad_split_pane_held` | 790 × 900, after a layout | conversation | Paper 390 │ Conversation 400 |
| `ipad_split_opening_the_pane_skips_the_band` | 1100 × 834, after a layout | conversation | Paper 700 │ Conversation 400 |
| `web_1440_closed` | 1440 × 900 | paper | Sidebar 284 │ List 360 │ Paper 796 |
| `web_1440_open` | 1440 × 900 | conversation | Rail 56 │ List 360 │ Paper 584 │ Conversation 440 |
| `web_1600_closed` | 1600 × 1000 | paper | Sidebar 284 │ List 360 │ Paper 956 |
| `web_1600_open` | 1600 × 1000 | conversation | Sidebar 284 │ List 360 │ Paper 516 │ Conversation 440 |
| `web_band_holds_the_rail_not_the_sidebar` | 1460 × 900, after the rail | conversation | Rail 56 │ List 360 │ Paper 604 │ Conversation 440 |
| `android_book_closed` | 673 × 841, crease_split, half-folded | paper | List 336 │ Paper 337 |
| `android_book_open` | 673 × 841, crease_split, half-folded | conversation | Paper 336 │ Conversation 337 |

The text sizes that first drop a region, by the rule (the sidebar and the system split's page minimum keep their
widths): the Duo's halves at xLarge (334 < 357.6); the iPad 11-inch on its side, the sidebar at xLarge (list and paper
need 804.7 of 794) and with the conversation the list at xLarge (1251.7 of 1194); the iPad 11-inch upright, the pane at
xLarge (894.1 of 834); the iPad 13-inch upright, the pane at xxxLarge (1082.3 of 1032).

## Judgement calls

Where the proposal leaves a choice open, these cases make it; change a case and this list together.

- **The iPad mini upright is a window of 600–759**, so its list is 300 and its paper 444, as proposal 3.8's
  `todoList` token says for iPad windows of 600–759 (its table first wrote List 320 │ Paper 424); the rule reads widths,
  not models, so a 744-pt window looks the same on every iPad.
- **The list is 340 where the window's shorter side is 1032 or more.** The table gives 340 to both 13-inch rows and
  320 to the 11-inch on its side (1194 wide), which no width threshold yields; the shorter side does, and the list
  keeps its width when the iPad turns.
- **The band keeps a region that fits less well, not one that fits better.** A region that showed stays while it is
  short by up to 24 pt; one that did not comes back as soon as it fits. The other reading, coming back only with
  24 pt to spare, would leave the Duo's halves (14 pt to spare) on one page after an unfold from the outer display
  or a text size set back to the default. The cost: a region up to 24 pt under its minimum inside the band. The band
  holds a form, not just a region: a rail held does not bring the full sidebar back early
  (`web_band_holds_the_rail_not_the_sidebar`).
- **The band does not outlast a text size change or the conversation opening or closing**, which are the owner's own
  choices rather than a window settling (`duo_inner_portrait_text_size_skips_the_band`,
  `ipad_split_opening_the_pane_skips_the_band`). A selection does not end it: list and paper are one arrangement,
  whatever the paper's region holds (`ipad_window_selection_keeps_the_band`).
- **`crease_split` is all of decision 2 (iv)**: the halves upright and the 320-pt paper minimum, which also keeps the
  pane on the Duo's side at xLarge (`duo_inner_landscape_open_at_xlarge_with_the_320_paper`).
- **Posture moves no region**: the halves are the same flat and half-folded; the Duo's measurement checks how they
  read both ways.
- **A sidebar over the crease's leading half takes the half's width** (334, up to the crease), as the prototype's
  `duo-inner-portrait/list-paper~sidebar` draws it; one page on the Duo lays it over at its own 320.
- **An Android foldable's halves hold the Duo's 320-pt minimum.**
- **The web's paper minimum is 400** until the web's stage gives it a token; the table's two web rows hold for any
  minimum from 357 to 516.
- **Regions under a pushed page are hidden**: what Back returns to belongs to the navigation, not to the window.

## Not pinned yet

- Web windows under 1024 (proposal 3.2: the conversation lies over the paper from 640 to 1023, one page below 640) and
  the web's own tokens: the web's stage.
- iPad windows under 600, which are compact and drawn as a phone's; an Android foldable flat, and Android's tokens
  beyond the hinge.
- The owner hiding the sidebar on purpose (the split's remembered choice), which applies after this rule.
- The keyboard, which never changes the layout (proposal 3.8, "The keyboard").

## Run

```sh
python3 -B scripts/test/test_todo_fixtures.py   # the format, the table's windows, the rule above
```
