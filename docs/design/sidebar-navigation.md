# Sidebar navigation: places, tasks and the drawer (iPhone shell and iPad sidebar)

Status: option 2 chosen and implemented (issue linked from the PR that adds this file). Baseline: `origin/main` @ `65cbb6b95`
(2026-11-13). Audience: everyone who uses the Assistant side of the app; nothing here is Agents-only.

User report: in the iOS app, choosing a page in the sidebar closes the sidebar while a sheet, a cover or a pushed page comes
in at the same moment, and the path the user was on is lost: they cannot get back to where they were, or going back lands
somewhere unexpected.

## 1. How it works today

The iPhone shell (`docs/apple/shell.md`) is `ShellView`: the drawer (`AssistantDrawer`, drawn by `AssistantDrawerLayout`) slides
the strip of two or three pages aside (Assistant, Agents, optional Messages). Each page has its own `NavigationStack`:
Assistant's `path` plus seven `show…` flags in `AssistantView` (`apple/Persona/Views/Assistant/AssistantView.swift:19`, `:26-35`),
Agents' `path` in `ChatsView` (`apple/Persona/Views/Chats/ChatsView.swift:235`). The drawer's places are pushed onto Assistant's
stack. On the iPad the same `AssistantSidebar` is the column of a `NavigationSplitView` (`AssistantView.swift:94-106`) and its
places go through the same `show(_:)` (`padPlaces`, `AssistantView.swift:111-138`).

A drawer row on the iPhone runs `ShellView.go(_:)` (`apple/Persona/Views/Shell/ShellView.swift:127-131`): it sets
`selectedTab = .assistant` (the strip slides there, animated, `ShellState.select`, `ShellState.swift:118-131`), records a one-shot
request (`ShellState.show`, `ShellState.swift:91-93`) and starts the drawer's closing spring (`AssistantDrawer.settle`,
`AssistantSidebar.swift:117-131`), all in one turn. `AssistantView` takes the request on its next update
(`AssistantView.swift:279-283`) and runs `show(_:)` (`AssistantView.swift:378-392`), which first calls `backToRoot()`
(`AssistantView.swift:412-425`) and then pushes.

### Entry → presentation → what happens to the path

| Entry | Code | Presentation | Path |
| --- | --- | --- | --- |
| A conversation row | `ShellView.swift:94-100` → `AssistantView.swift:290-301` | Assistant's root shows that conversation; strip slides if Agents was in front | `backToRoot()`: a place pushed on Assistant, with everything under it, is dropped |
| New Chat | `go(.newConversation)` | Assistant's root, new conversation | dropped, as above |
| Inbox, To-dos, Machines, extension pages | `go` → `show` → `path.append(ShellRoute…)` (`apple/Persona/Views/Shell/ShellHome.swift:429-442`) | `NavigationStack` push on Assistant | the place in front and its sub-pages are dropped first; back returns to the conversation, also when it was opened from Agents |
| Scheduled Tasks, Knowledge, Profile, Recordings | `go` → `show` → `show… = true` (`AssistantView.swift:247-257`) | push by `navigationDestination(isPresented:)` | dropped first, as above; these pushes are not in `path` at all |
| Health | `go(.health)` → `enterHealth()` (`AssistantView.swift:397-410`) | the latest health conversation, or a push of Health | dropped first |
| Settings | `ShellView.swift:105-108` | `.sheet` on the shell (`ShellView.swift:30-36`) | the drawer closes under the rising sheet; closing Settings lands on the page, not the drawer it came from |
| Archived, Auto conversations | `AssistantSidebar.swift:904-915`, presented from the drawer's own hosting view | sheet over the drawer | picking a row dismisses the sheet, closes the drawer and resets the stack in the same turn |
| Search (iPhone Duo open) | `AssistantSidebar.swift:917-923` | `fullScreenCover` | as Archived |
| Assistant / Agents switch | `apple/Persona/DesignSystem/ShellComponents.swift:118-131`, `AppModel.selectHomeTab` (`apple/Persona/App/AppModel.swift:209-217`) | strip pages | choosing Agents returns to its list by design (#5708); Assistant's stack stays |
| Opening the drawer at all | `ShellView.swift:25` (`enabled: shell.atRoot`), `DrawerPan.pushed`; ☰ only in the root top bars | — | never from a pushed page: to change place the user first pops to the root, which drops the place's depth |
| iPad sidebar column | `AssistantView.swift:111-138` | same `show(_:)`, push in the detail stack | switching place drops the previous place's stack; no drawer animation (the column stays) |

Where navigation state lives: Assistant's `path` and flags (`@State` in `AssistantView`), Agents' `path` (`@State` in `ChatsView`),
the request (`ShellState.request`, one-shot). Nothing remembers a place's stack once `backToRoot()` has run: that is why it is lost.

### Conflict scenarios

1. **Close vs push vs slide.** On the Agents list, ☰ → Inbox: the strip slides to Assistant, the drawer's spring closes and the
   stack pushes Inbox, three animations in the same ~0.4 s. The page card the user watches changes twice while it moves.
2. **Close vs sheet.** ☰ → Settings: `drawer.close()` and the sheet's presentation start together; after Done the user is on the
   page, not in the sidebar where they were.
3. **A sheet's result vs close.** ☰ → Archived (or Auto conversations, or Search) → a conversation: the sheet's dismissal, the
   drawer's close and `backToRoot()`'s pop all run in one turn.
4. **A place replaces a place.** Machines → a machine's page: the drawer cannot open there, so the user backs out to the
   conversation, opens the drawer, picks Knowledge; picking Machines again starts at the list. The same loss without backing out:
   Inbox → an Agent chat routes to Agents (`selectedTab = .chats`) with Inbox still pushed on Assistant; ☰ on Agents → Knowledge
   drops Inbox and the chat under it.
5. **Back goes elsewhere.** A place opened from Agents is pushed on Assistant; back returns to the Assistant conversation, while
   the user came from Agents. (Places belong to Assistant: `docs/apple/shell.md`.)
6. **iPad column vs iPhone drawer.** The column never closes, so 1–3 do not happen there, but 4 does: switching places in the
   column drops the previous place's stack through the same `backToRoot()`.

## 2. What other apps and the HIG do

- **HIG, Sidebars / Tab bars:** a sidebar or tab bar switches between top-level areas; each area keeps its own navigation state,
  and choosing the area already in front returns it to its root. The sidebar's selection changes the content; it does not stack
  areas on top of one another. **HIG, Modality:** a modal view is for a self-contained task; when it is dismissed, people are
  back exactly where they started, and a modal should not start while another transition is still moving.
- **Apple Mail / Files (iPad):** choosing a mailbox or a location in the sidebar replaces the detail; each keeps its scroll and
  selection, the sidebar stays. In compact width the sidebar is the stack's root; back returns to it.
- **Slack (iPhone):** Home, DMs and Activity each keep their stack; switching keeps it, tapping the one in front pops to its root.
- **ChatGPT, Claude (iPhone):** the sidebar is a drawer; choosing a place swaps the content under the drawer and only then the
  drawer closes (one motion). Settings and account open as a sheet over the open sidebar; closing it returns to the sidebar.

Two kinds of entry follow from these:

- **Places** (where the user goes): a conversation, New Chat, Inbox, To-dos, Scheduled Tasks, Knowledge, Health, Machines,
  Recordings, extension pages, Profile, and the Assistant / Agents pages themselves.
- **Tasks** (something done and dismissed): Settings, Archived, Auto conversations, Search, New Main Chat.

## 3. Options

### Option 1 — sequence the animations only

Keep every presentation as it is; wait for the drawer to finish closing before pushing, sliding or presenting.

- Pros: smallest change; fixes scenarios 1–3.
- Cons: a slower response (the push starts ~0.4 s after the tap); 4–6 stay: paths are still dropped, and the drawer is still
  unreachable from a pushed page.
- Risk: low, but it does not answer the report (the lost path).

### Option 2 — places keep their stacks; tasks stay over the drawer (recommended)

- **Places:** under the open drawer the page switches without animation (strip, `backToRoot()`, push), then the drawer closes:
  one motion. Each place keeps its own stack: leaving it (for another place, a conversation, or the Assistant root) saves its
  `NavigationPath`; choosing it again restores it. Choosing the place already in front returns it to its root (the tab-bar rule).
  Places stay pushed over the conversation, so back from a place's root still returns to the conversation, as today.
  All drawer places push by value (`ShellRoute`), so their sub-pages pushed by value are part of the saved path.
- **The drawer from any depth:** pages pushed in a place show ☰ next to back (as an iPad split's detail shows the sidebar button
  next to back), so the user can change place without popping.
- **Tasks:** shown over the open drawer, which stays open; closing the task returns to the drawer as it was. A task that
  ends in a place (a conversation picked in Archived, Auto conversations or Search) waits until its sheet is gone, then
  switches as a place does.
- **iPhone:** as above. **iPad:** the column never closes and Settings is already a sheet; places keep their stacks through the
  same code (`AssistantView.show`).
- Pros: fixes 1–4 and 6 with the existing routes (`ShellRoute`, `ShellDestinations`, `ShellState.request`) and no new shell
  structure; the pages look as before except for ☰ on pushed place pages.
- Cons: scenario 5 stays by design (places are Assistant's); only pages pushed by value come back (pages pushed with
  `NavigationLink { … }` or by flags inside a place come back to the nearest value page); scroll positions are rebuilt.
- Risks: a saved path can point at something gone (a removed machine): those pages show their own empty state, and an
  assistant switch forgets every saved path (`assistantSwitches`). ☰ on pushed pages is a visible change, checked in screenshots.

### Option 3 — every place is a root with its own live stack (ChatGPT / Claude model)

The Assistant page shows one place at a time as its root, each in its own `NavigationStack` kept alive; there is no back from a
place to the conversation; ☰ is at every root.

- Pros: the cleanest model; keeps scroll positions and pages pushed any way; fixes 1–6.
- Cons: a rebuild of the shell's Assistant page (its top bar, the switch, the pager, the probes) and of every back flow; about ten
  UI suites (`ShellSwitchTests`, `ShellInboxAgentTests`, `ShellRaceTests`…) assert today's back to the conversation; a stack kept
  alive per place costs memory; the iPad's split detail needs the same change.
- Risk: high for a bug fix; better as its own proposal if the product wants places as roots.

### Comparison

| | Option 1 | Option 2 | Option 3 |
| --- | --- | --- | --- |
| Place: presentation | push, after the close | push under the open drawer, no animation | root swap |
| Task: presentation | sheet, after the close | sheet over the open drawer | sheet over the open drawer |
| When the drawer closes | before anything moves | after the place is in front | after the place is in front |
| Each place keeps its stack | no | yes (value pushes) | yes (everything) |
| Back from a place | conversation | conversation | none (☰) |
| After closing a task | the page | the drawer, as it was | the drawer, as it was |
| Drawer from a pushed page | no | ☰ next to back | ☰ at every root |
| iPad column | unchanged | stacks kept | stacks kept, split rebuilt |
| Size of change | small | medium | large |

## 4. Decision

**Option 2.** Places switch under the open drawer and keep their own stacks; tasks open over the drawer and return to it.

Implementation:

- `ShellPlaces` (`apple/Persona/Views/Shell/ShellPlaces.swift`): which place is in front and the saved path of each place left.
  Pure value type with unit tests (`PersonaTests/ShellPlacesTests`).
- `AssistantView.show(_:)` saves the place in front, then restores or pushes the new one; `backToRoot()` happens without animation
  while the drawer is out; the drawer closes after the switch. Scheduled Tasks, Knowledge, Profile and Recordings push by value
  (`ShellRoute`) like Inbox, so their pushes are part of the path.
- `ShellView`: places snap the strip without animation and leave closing to the page; Settings opens over the open drawer.
- `AssistantSidebar`: Archived, Auto conversations and Search open the picked conversation from the sheet's `onDismiss`.
- `ShellPlaceSidebarButton`: ☰ on pages pushed in a place (`ShellDestinations`).
- The iPad's column sends every place through `show(_:)` too (Knowledge, Scheduled Tasks, Profile and Health used to set flags).
- `DrawerSwipeTests.testDrawerSettingsReturnsToTheDrawerThenPagesSwitch` replaces the test that expected Settings to close the drawer.

## 5. Acceptance

| Item | Check |
| --- | --- |
| A place keeps its stack | ☰ → Machines → a machine → ☰ → Knowledge → ☰ → Machines: the machine's page is in front; back → Machines → conversation (`ShellPlacesUITests.testAPlaceKeepsItsStack`) |
| Choosing the place in front pops it | ☰ → Machines → a machine → ☰ → Machines: the Machines list (same test) |
| A task returns to the drawer | ☰ → Settings: the drawer stays open under the sheet; Done: the drawer is open with its rows (`testSettingsReturnsToTheDrawer`) |
| Close and modal never overlap | the drawer's closing starts only after the place is in front or the task's sheet is gone, read from `ShellTrace` (`testArchivedPickClosesAfterTheSheet`, `testAPlaceSwitchesBeforeTheDrawerCloses`) |
| Saved paths are the place's | unit tests: save on leave, restore on return, re-pick pops, assistant switch forgets (`ShellPlacesTests`) |
| iPad column | Machines → a machine → Knowledge → Machines in the column: the machine's page (`testAPlaceKeepsItsStackBesideTheIPadColumn`) |
| Nothing else moved | `ShellTests`, `ShellSwitchTests`, `AssistantArchiveTests`, `DrawerSwipeTests` stay green |
