# To-dos prototype kit

A static HTML kit for drawing the To-dos redesign variants on iPhone, iPhone Duo, iPad, the web and Android. It
draws devices with their system chrome and offers Persona's components in the default style (Liquid Glass, theme
color System Blue) in light and dark. Variants are plain scripts that register frames; two pages show them.

- Works from `file://` in Chrome: no modules, no `fetch`, no build step, no dependencies, no external resources.
- `index.html`: the gallery. Pick a variant, switch the theme and the zoom, filter to key frames, or **Compare** one frame
  id across variants side by side. A tile opens that frame.
- `index.html` groups each variant's frames by device, ★ (important) frames first, a frame's states right after it,
  each with its title and note.
- `frame.html#v=KIT&f=iphone/list&theme=dark`: exactly one frame at its device size with no page padding, for
  screenshots and click-throughs. `&scale=0.5` zooms it for viewing. Clicking anything with `data-go` moves to that
  frame, playing its `data-transition`; a floating harness strip offers Light / Dark, Reduce Motion, Hotspots, Reset
  and Fold / Unfold (see "Harness"). Keys: `t` theme, `m` Reduce Motion, `h` hotspots, `←` / `→` previous / next
  frame, `g` gallery. `&shot=1` (added by the screenshot tools) renders the frame statically with no strip.

The `KIT` variant (`v0/demo.js`) is the kit's own demo: every device and the main components, plus a catalog of every
icon and component (`canvas/catalog`). It is a reference, not a design proposal.

## Files

| Path | What |
| --- | --- |
| `kit/tokens.css` | Colors (light / dark), type, spacing, radii, sizes as CSS variables; the web's smaller scale under `.k-web` |
| `kit/devices.css`, `kit/kit.js` | Device chrome; `Kit.variant`, `Kit.frame`, `Kit.device`, `Kit.renderFrame`, utilities |
| `kit/icons.js` | `Kit.icon(name, size)`: inline SVG drawn like SF Symbols |
| `kit/components.css`, `kit/components.js` | `Kit.ui.*` components: the general ones, then the To-dos components of synthesis §3.0.4 |
| `kit/frame-page.js`, `kit/gallery.js`, `kit/gallery.css` | The two pages: the harness (strip, transitions, toasts, timed steps) and the gallery |
| `data/todos.js` | The shared data set of synthesis §3.0.5 (`window.Data`: T01–T14, asks, runs, rounds, counts and helpers) |
| `v0/demo.js` | The `KIT` demo |
| `v1/` … `v3/` | One folder per variant: `screens.js` (frames) and `screens.css` (styles); stubs to start from |
| `tools/shoot.sh`, `tools/shoot-all.sh` | Screenshots with headless Chrome |

Both pages load, in order: the kit, `data/todos.js`, `v0/demo.js`, `v1`–`v3` `screens.js`, then the page script; and
`v1`–`v3` `screens.css`. A variant never needs to edit either page.

## Add a variant

1. In `v1/screens.js` (the stub is ready):

   ```js
   (function () {
     Kit.variant('V1', { name: 'Paper & Pane', summary: 'One sentence on the idea.' });

     Kit.frame('V1', 'iphone/list', {
       device: 'iphone',
       title: 'iPhone · To-dos list',
       note: 'What this frame shows and why.',
       important: true,                       // a key frame: shown by the gallery's "Key frames" filter
       render: (ctx) => {
         const ui = ctx.ui;
         return ui.page({
           top: ui.navbar({ large: 'To-dos', trailing: [{ icon: 'magnifying-glass' }, { icon: 'ellipsis' }] }),
           body: ui.list(ctx.data.todos.map((t) => ui.row({ title: t.title, state: t.phase, go: 'iphone/detail' }))),
           bottom: ui.composer({ placeholder: 'New To-do' }),
         });
       },
     });
   })();
   ```

2. Put styles in `v1/screens.css`, every rule scoped to the variant: `[data-variant="V1"] .v1-hero { … }`. Every
   device root carries `data-variant` and `data-frame`. `Kit.css('v1', '…')` injects a scoped block from JS too.
3. Open `index.html#v=V1`, then `frame.html#v=V1&f=iphone/list` for a frame at full size.
4. Screenshot every frame in both themes and look at them (below).

## API

### Registration

```js
Kit.variant(id, { name, summary, order })      // order: lower first in the tabs (default 50; the KIT demo is 99)
Kit.frame(variantId, frameId, {
  device,          // a device id (table below)
  title, note,     // shown under the tile; note accepts HTML
  important,       // ★ frame: first in its device group; the gallery's "★ Frames Only" filter
  chrome,          // default device options for this frame, e.g. { keyboard: true } or { crease: 'half' }
  fold, unfold,    // Duo frames: the frame id the strip's Fold / Unfold opens ('iphone/kyoto', 'V1:duo-inner-portrait/…')
  auto,            // a timed step: { go: 'iphone/kyoto~sent-2', after: 1200, transition: 'none' } (never with shot=1)
  render(ctx),     // → an HTML string (the screen's content), or { content, …device options }
});
```

**Frame ids** are `<device>/<screen>` (`iphone/list`, `ipad13-landscape/list-detail-conversation`): letters, digits and
`/ _ . -`. Use the **same id in every variant** for the same scene, so Compare lines them up. Docs write the web
device's frames with their variant (`V1:web/open`), so the docs checks don't read them as paths under `web/`.

**States are frames.** A frame id may carry one state: `<frame>~<state>` (`iphone/list~calm`, `iphone/kyoto~answered`),
registered like any frame. The gallery puts states right after their frame, the strip shows the state, and
`ctx.baseId` / `ctx.state` tell a shared render function which one it draws. File names keep the `~`
(`iphone-list~calm-light.png`). Timed sequences (Sending → Processing → Delivered) are states chained with `auto`.

**`ctx`** (what `render` receives): `variant`, `frameId`, `baseId` (`'iphone/list'` for `'iphone/list~calm'`), `state`
(`'calm'`, or `''`), `frame`, `device`, `size: { w, h }`, `theme` (`'light' | 'dark'`), `mode` (`'frame' | 'gallery'`),
`shot` (screenshot run), `live` (a click-through in frame.html: `Kit.store` may be read), `data` (`window.Data`), `ui`
(`Kit.ui`), `icon` (`Kit.icon`), `esc`, `join`, `when`, `store` (`Kit.store`), and:

- `ctx.go(frameId, transition)` → ` data-go="frameId" data-transition="push"` for any element you write by hand:
  `` `<div class="v1-card"${ctx.go('iphone/kyoto', 'push')}>…</div>` ``. `'V2:iphone/list'` links to another variant.
- `ctx.toast(text)` → ` data-toast="…"`: a tap shows a 2-s toast naming a destination the prototype doesn't build
  ("Opens: Search To-dos"), never a blank page (synthesis §3.0.1).

Components take the same as options on anything clickable: `{ go, transition, toast }` (`Kit.navAttrs(o)` writes them).
Transitions: `push` (the new frame slides in from the trailing edge, 0.3 s) · `pop` (the reverse) · `fade` (0.2 s
cross-fade; use it on the Duo, where nothing slides) · `pane-open` / `pane-close` (a 0.3 s cross-fade and slide) · `none`.
Only the content moves; the status bar, the Duo side column and the keyboard stay. Between different devices (Fold /
Unfold) the whole device cross-fades. Reduce Motion turns every transition into the 0.2 s fade.

Return an object to fill device slots, for example the Duo side column:

```js
render: (ctx) => ({
  content: ctx.ui.page({ … }),
  side: { back: { go: 'iphone/list' }, actions: [{ icon: 'bubble-left-right', label: 'Talk' }, { icon: 'ellipsis', label: 'More' }], tabs: { selected: 0 } },
})
```

### Devices (`Kit.device(kind, options)`)

Sizes are points (CSS px). The device sets `--safe-top` and `--safe-bottom` (the keyboard's height while it is up), so
`ui.navbar`, `ui.page`'s bottom, `ui.sidebar` and `ui.convo` sit clear of the system chrome on their own.

| Device id | Size | System chrome |
| --- | --- | --- |
| `iphone` | 402 × 874 | Dynamic Island, status bar, home indicator; safe top 62, bottom 34; keyboard 336 |
| `duo-outer` | 466 × 678 | **side column down the right edge (84)**: camera, status, Back at 174, toolbar items (symbol over name), the Duo tab bar 178.7 above the foot, the rail's face and action at the foot. Content is the 382 to its left |
| `duo-inner-portrait` | 669 × 951 | horizontal status bar; vertical crease at 334.5; optional Duo tab bar along the foot (`tabs`) |
| `duo-inner-landscape` | 951 × 669 | side column as on the outer display (Back at 124, no camera); horizontal crease at 334.5 (tabletop). Content 867 |
| `ipad11-landscape` | 1194 × 834 | iPad status bar, home indicator; optional iPadOS window controls |
| `ipad11-portrait` | 834 × 1194 | as above |
| `ipad13-landscape` | 1376 × 1032 | as above |
| `ipad13-portrait` | 1032 × 1376 | as above |
| `web` | 1440 × 900 | none, or a minimal browser window (`browser: true`, 76 tall); the web's smaller type scale (`.k-web`) |
| `android-fold` | 673 × 841 | Android status bar, gesture bar, hinge at 336.5 (secondary) |
| `canvas` | 1440 × 1400 | none: spec sheets, flows, catalogs |

The status bars show `Data.clock` (10:14, Wed Nov 18) when the data sets it, else 9:41.

Options: `content`, `bg` (screen color, default `var(--page)`), `statusBar` (default on), `home` (home indicator,
default on), `keyboard` (`true`, a height, or `{ height, predictive, suggestions, label }`; 336 on iPhone, ≈ 300 on the
Duo, where the keyboard is labelled "keyboard ≈ 300 pt (to measure)"), `crease` (`'flat'` default, `'half'` for a half-folded hint, `'off'`),
`side` (`{ back, actions, tabs, foot, search }` on the two Duo side-column screens), `tabs` (Duo inner portrait's foot
tab bar), `windowControls` (iPad), `browser`, `url` (web).

- `side.back`: `true`, a label, or `{ icon, label, go }` (a 48 pt glass circle).
- `side.actions`: `[{ icon, label, go, active }]`, one glass capsule, symbol over a short name.
- `side.tabs`: `true` or `{ items: [{ icon, label, count, go }], selected }`; default Assistant · Agents.
- `side.foot`: the shell rail's buttons at the foot (default on the outer display with tabs: the face and New Chat).
- With `windowControls`, pass `wc: true` to the leading column's `ui.navbar` (or `ui.sidebar`) to keep clear of them.

### Components (`Kit.ui`)

All return HTML strings; text options accept HTML. Full option lists are in the comments of `kit/components.js`.

| Group | Helpers |
| --- | --- |
| Layout | `page({ top, body, bottom, bg, pad, overlay })` · `split([{ w \| flex, html, bg, line }])` · `vsplit([{ h \| flex, html }])` · `bottom(html)` · `toolbar({ leading, center, trailing })` · `block(title, html)` · CSS `k-stack`, `k-hstack`, `k-grow`, `k-readable` |
| Bars | `navbar({ back, leading, title, subtitle, trailing, large, largeSub, below, align, wc, fog })` · `search({ placeholder, mic, size })` · `segmented(items, selected)` · `filterBar([{ label, count, look }], { selected })` · `section(title, { count, look, tone, fold, trailing })` |
| Buttons | `btn({ icon, label, look: glass \| prominent \| tinted \| fill \| plain \| danger, size: xs \| s \| m \| l, go })` · `btnGroup([…])` (icon buttons sharing one glass capsule) · `capsules([{ label, recommended, quiet, go }])` (one row of answers that never wraps; fades when it overflows) |
| Rows | `row({ title, check, state, stateText, sub, meta, answers, more, trailing, chevron, selected, priority, dense, go })` · `list(rows, { inset })` · `cell({ icon, label, value })` |
| States | `mark(state)` · `state(state, { text, word })` · `badge(n, { look: needs \| neutral \| plain \| red })` · `chip(label, { icon, tone, look: tone \| neutral \| outline \| solid })` · `due(text, { overdue })` · `project(name, { hue })` · `flag()` · `spinner(size)` · `ui.states` (the one word list) |
| Cards | `ask({ from, question, detail, recommended: { label, why }, options, dismiss, other })` · `round({ instruction, receipt, summary, result, resultState })` · `fold(text)` · `run({ role, where, latest, state, icon, hue })` · `deliverable({ kind, title, outcome, sub, tone, look: card \| chip })` · `steps([{ text, done, state, by, suggested }], { title, count, add })` · `card(html, { look })` · `empty({ icon, title, body })` |
| Sheets | `sheet({ detent: small \| medium \| large \| px, title, leading, trailing, content })` · `scrim()` · `inspector({ width, content, overlay })` |
| Conversation | `convo({ header: { title, state, stateText, sub, back, trailing }, messages, pinned, composer })` · `msg.user(text, { receipt, to })` · `msg.persona(text)` · `msg.agent(text, { name, icon })` · `msg.tools(summary)` · `msg.receipt(text, { state })` · `msg.day(text)` · `composer({ placeholder, text, to, toPlacement: above \| inside, plus, mic, stop, note })` · `addressee(to)` |
| Persona | `sidebar({ selected, counts, children: { todos: [rows] }, open, chats, go, wc })` (rows 44, radius 10; 400 on iPad, 320 on the Duo, 284 on the web: the column you put it in sets the width) · `sbRow({ icon, label, count, countLook, indent, disclosure, selected })` · `face(size, { state })` · `avatar(text, { hue })` · `keyboard({ height, wide, predictive, suggestions, label })` |

`btn` also takes `dot` (a blue unread dot, e.g. on the Conversation toggle) and `transition` / `toast`.

### To-dos components (synthesis §3.0.4)

Every component a variant shares. Plain options first; the `…For(…)` adapters fill them from `window.Data`, so every
variant shows the same words and numbers (pass any option to override). Clickable options take `{ go, transition, toast }`
(written `nav` below; a string is a frame id). Phase words are sentence case ("Needs you", "Just finished").

| Component | Signature |
| --- | --- |
| TodoRow | `todoRow({ title, lead: 'tick' \| 'done' \| 'none' \| HTML, tickNav, mark, lineLead, line, tone, toneAll, lineHtml, answer: { kind: 'ok' \| 'door' \| 'reply', label, options: true \| nav, open, …nav }, answered, undo, line3, trailing, unseen, priority, ticked, selected, swiped, below, …nav })` · `todoRowFor(todo, o)` · `rowProps(todo, o)` · `todoList(rows)` · `tick('open' \| 'done', nav)` · `lineMark('needs' \| 'working' \| 'ready' \| 'waiting' \| 'done' \| 'danger')`. Heights 52 / 72, +44 answers, +20 line 3. `lead` HTML is the pluggable slot (V3's living mark); `selected` draws `--fill-well` and the 3-pt accent bar; `unseen` the 7-pt dot before the time; `answered: 'Ryokan Hanami'` gives "✓ You chose … · Undo"; `below` holds the unfolded ask card |
| SectionHeader | `sectionHeader(title, { count, look: 'needs' \| 'neutral' \| 'none', tone, lit, fold, trailing, …nav })` · `sectionHeaderFor(section, o)` · `foldRow(label, { count, open, indent, …nav })` ("Can wait · 1 ›", "Someday · 1 ›") |
| SectionBar | `sectionBar([{ id, label, count, look: 'needs', …nav }], { selected: id \| index, trailing })` · `sectionBarFor({ place, selected, go: { all, needs, progress, todo, done }, trailing })` (the Inbox CapsuleFilterBar at its shipped sizes, with the landing choice first, All · Needs you · In progress · To do · Done: a 44-pt capsule whose choices are 44-pt tap targets around 28-pt pills, the chosen pill in the accent at 12%, 8 in from the capsule's ends so its curve follows the capsule's; the kit draws the hairline before `trailing`; one row that fades; the proposal's section 3.3) |
| TitleMenu | `titleMenu({ title, open, items, selected, align, width, …nav })` · `placeMenuItems({ selected, go: { all, today, upcoming, <project>, projects }, extra })` · `menu(items, { align: 'center' \| 'leading' \| 'trailing', top, width, cls })`, items `{ label, sub, check, icon, count, look, danger, disabled, …nav }`, `'-'` divider, `{ header }` |
| AskCard | `askCard({ title, from, body, prices, plan: [lines], estimate, secondary: [{ label, …nav }] (≤ 2), primary: { label, chevron, …nav }, links: [{ label, …nav }], answered, undo, later, compact })` · `askCardFor('a1' \| ask, { as: 'home' \| 'door', on: { <option id> \| primary \| later \| dont \| discuss \| door \| edit \| dontStart \| approve \| other \| undo \| laterOpen: nav }, answered, later, compact, narrow })`. P1: ≤ 2 equal secondary buttons, the primary full width, then "Later · Don’t handle · Discuss…"; a door is headed "From Widget"; `answered` is the one-line "✓ You chose … · Undo"; `later` folds to "Which ryokan? · later today ›"; `narrow` drops "· Recommended" |
| Ask line | `askLine({ title, word, mark, ok: { label, …nav }, door: { label, …nav }, options })` · `askLineFor(todo, o)`: the ask folded to one 44-pt line, "● Which ryokan? [OK: Hanami] Options ⌄" |
| AskPointer | `askPointer('Plan awaiting approval · answer in the conversation →', nav)` |
| StateLine | `stateLine(phase, text, { word, tone, mark, clamp: 1 \| 2 })` · `stateLineFor(todo, { short, mark })` |
| RecapLine | `recapLine('flights held · 1 question', nav)` → "Since you looked: …" |
| Paper pieces | `paperTitle({ title, lead, tickNav })` (tick + title 22) · `notes(text, { placeholder: 'Add Notes', …nav })` · `briefLine({ doneWhen, text, …nav })` · `briefLineFor(todo, o)` ("What you asked · Done when 1 of 3 ›"; with `text` the open brief) · `helpCard({ steps, doIt, close })` ("Persona can help") · `blockHead(title, trailing, nav)` · `workDetails({ summary, open, rows: [{ mark, icon, text, action, …nav }], …nav })` · `workDetailsFor(todo, o)` |
| Chips | `todoChips([{ label, icon, hue, tone, dashed, outline, clear, …nav }], { add: true \| nav })` · `chipsFor(todo, o)` (32 capsules, "+" a 32 circle; `dashed` until Return in capture) |
| StepsList | `stepsList([{ text, state: 'done' \| 'needs' \| 'working' \| 'waiting' \| 'idle', who, word, chevron, …nav }], { title, count, limit: 5, all, suggested: [text], keep, edit, headNav, compact })` · `stepsFor(todo, o)` · `stepsSummary(todo, { results, marks, …nav })` ("Steps 2 of 5 · Results 2 ›") |
| ResultsStrip | `resultsStrip([{ kind: 'doc' \| 'change', label, outcome, …nav }], { title, inline })` · `resultsFor(todo, o)` |
| ConversationBlock | `conversationBlock({ rounds, count: 2, total, all: nav, roundNav, pointer, pointerNav, latest })` · `conversationBlockFor(todo, { extra: ['r4'], count, pointer, latest, all })`: "Conversation · All N ›" and the latest rounds folded to "You · 10:02 “…”" over "└ …" (a round's `liveStatus` adds its message state); `pointer` = "Conversation · open beside →"; `latest` = "Latest · You “…” ›" |
| Round | `convRound(round, { status, open: { <run who>: true }, relay: 'queued' \| 'delivered' \| 'now', askedSuffix, hide: [kinds], nav: { sendNow, undo, session }, bgOpen, bgNav })` · `thread(rounds, { anchor: 'bottom', days, pad, per: { <round id>: options }, …round options })` · lines `rline.run({ who, text, open, steps, session, done })`, `rline.live`, `rline.needs`, `rline.relay({ to, state, sendNow, sendNowNav })`, `rline.scope({ text, undo })`, `rline.decision`, `rline.kept`, `rline.asked({ text, suffix })`, `rline.result`, `rline.reply({ who, text })`, `rline.day(text)`, `rline.background({ text, open })`. Message states: Sending (faint) · Processing · Delivered to … · Queued … · Not delivered · Retry (danger) · Delivery unknown · Check (caution) |
| Composer | `todoComposer({ to: 'Persona' \| { label, menu, run, answer, …nav } \| false, placeholder, text, caret, label: 'above', note, inset: 16 \| 8, menu: HTML, mic, send: nav })` · `composerFor(todo, o)` · `addresseeChip(to)`: the glass capsule 52 (radius 32), the chip inside ("To Persona", ▾ with a menu, purple when it names a run, "Answer: Which ryokan? ×"), mic and ↑ (dimmed while empty). Typed text keeps its end and caret in view. In `ui.page({ bottom })` it sits 16 from the edges |
| PinnedHeader | `pinnedHeader({ title, back: nav \| false, more: nav \| false, titleNav, word, mark, ask, ok, door, options, row2: HTML, layout: 'one' \| 'two' })` · `pinnedHeaderFor(todo, o)`: 92 pt, "‹ title ⋯" over "● Which ryokan? [OK: Hanami] Options ⌄" (`layout: 'two'` adds the state word and puts the buttons on a third line); `back: false` on the Duo, whose Back is in the side column |
| PaneHeader | `paneHeader({ channel, menu, state, word, more: nav \| false, close: nav \| false, open: HTML (a menu), switchNav })`: "Persona ▾", ⋯, ✕ (glass 36) |
| PlaceSummary | `placeSummary({ title, sub, lines: [{ count, label, look, lit, …nav }], foot: [HTML] })` · `placeSummaryFor(place, { go: { needs, working, just_finished, waiting, todo, can_wait }, foot })` |
| Toast, pill, drawer | `toast(text, { action: 'Undo', actionNav, icon, bottom: 96 })` · `pill({ label: 'New To-Do', icon, bottom: 50, …nav })` · `drawer({ content, width, scrimNav })` (put them in `ui.page({ overlay })`) |
| SidebarTree | `sidebarTree({ selected: 'todos' \| 'today' \| 'upcoming' \| <project id> \| 'inbox' \| 'persona' \| 'release-lead' \| …, open, starred: [project ids], counts, header, compose, go: { <row id>: nav }, wc, width, cls })`: Inbox 2 · To-dos (3) › Today · Upcoming · 12-pt gap · Kyoto trip (1) · Weather App (1) · Home (1) · All Projects… · Scheduled Tasks · Knowledge · Machines · Persona Chats (Persona; Release lead · Pinned (1)) · Agents ›. `starred` gives V2's starred places only |
| CaptureSheet | `captureSheet({ title, text, token, placeholder, chips: [{ label, dashed, clear }], adds: ['Project', 'Remind'], close, add, ask, scrim })` (docked above the keyboard: use with `chrome: { keyboard: true }`) |
| Needs you tray | `needsTray({ header, groups: [{ title, rows: [{ title, ask, tone, buttons: [{ label, primary, door, …nav }], …nav }] }] })` · `needsTrayFor({ on: { a1, a2, a3, deny, allow, <todo id> } })`: "Needs you · 4" = "To-dos · 3" + "Elsewhere · 1" |
| Lock screen | `lockScreen({ date, time, content })` · `notification({ app, time, title, body, actions: [{ label, recommended, …nav }], expanded, …nav })` · `notificationFor('a1', { expanded, on: { <option>: nav } })` · `fleetActivity({ header, rows: [{ title, ask, button: { label, recommended, …nav } }], more })` · `fleetActivityFor({ on: { <run id> \| elsewhere }, limit: 2 })` ("Persona · 2 need you": the sessions that need the owner, the Widget run then Elsewhere, one button per row) |

States: `needs` (blue dot), `working` (amber spinner), `waiting` (light amber clock), `ready` / `unread` (green),
`idle`, `later`, `done` (gray), `stuck` / `review` (caution). Tones for chips also include `accent`, `ai`, `danger`,
`caution`, `positive`.

### Shared data (`data/todos.js`, `window.Data`)

The data set of synthesis §3.0.5, made up: "now" is Wednesday 18 November 2026, 10:14; computers MacBook and Desk Mac;
projects Kyoto trip, Weather App, Home; the Pinned direction Release lead; T01–T14; the "Elsewhere" session (Tidy the
settings code). Never type a number into a frame: read it here, so every place shows the same count.

| Helper | Returns |
| --- | --- |
| `Data.todos` · `Data.todo('t01')` | to-dos in rank order; one by id (`'t01'` or `'T01'`): `title, project, phase, section, row { mark, lead, text, tone, all }, trailing, unseen, answer, line3, state { word, text, short }, recap, chips, notes, brief, doneWhen, steward, asks, steps, runs, results, decisions, rounds, extraRounds, allCount, workDetails, composer, channels, session, drafts, capture, suggestedSteps` |
| `Data.ask('a1')` · `Data.run('widget')` · `Data.project('kyoto')` · `Data.computer('deskmac')` · `Data.place('all')` | asks a1 (Which ryokan?), a2 (Plan awaiting approval, home: the Widget run), a3 (Couldn’t start) with options, prices, defaults, exits, push and Live Activity text; runs with role, computer, state, capabilities; places with their empty states |
| `Data.counts` | `needs` 3 · `canWait` 1 · `working` 2 · `justFinished` 2 · `waiting` 3 · `todo` 2 · `someday` 1 · `inProgress` 5 (Working + Waiting; Just finished is not counted) · `open` 12 · `byProject` { kyoto 1, weather 1, home 1 } · `withoutProject` 1 · `elsewhere` 1 · `assistantNeeds` 4 · `agentsBadge` 2 · `fleet` "2 need you" (sessions, not to-dos) · `inbox` 2 · `pinned` { 'release-lead': 1 } |
| `Data.sections(place, filter)` | `[{ id, title, count, look, fold, todos }]` in order; place `'all' \| 'today' \| 'upcoming' \| <project id>`, filter `'all' \| 'needs' \| 'progress' \| 'todo' \| 'done'`; empty sections left out |
| `Data.bar(place)` · `Data.summary(place)` · `Data.countsOf(place)` · `Data.upcoming()` · `Data.thisWeek()` · `Data.isCalm(place)` | the section bar's chips; the place summary's lines in the section order, Can wait last; a place's counts; Upcoming's day groups; "3 coming up this week"; the calm rule |
| `Data.view(state)` | the same API over a changed copy, for states made in the prototype: `{ answered: { a1: 'hanami' \| 'kaede' \| 'tsuki' }, approved: { a2: true }, a3: 'back', ticked: ['t11'], elsewhere: 'allowed', calm: true }`. Counts follow (answering a1 gives N = 2 everywhere) |
| `Data.check()` | `[]` when the counts equal the spec's numbers (`Data.expected`); the page warns in the console otherwise |

### Harness (`frame.html`)

- **Strip**: a floating strip at the bottom right, outside the device when the window is larger: the frame id (★, its
  `~state`), Light / Dark, **Reduce Motion** (also on when the system asks), **Hotspots** (2-pt dashed outlines on
  everything with `data-go`, orange for `data-toast`), **Reset** (clears `sessionStorage`, then redraws),
  **Fold** / **Unfold** (Duo frames with a `fold` / `unfold` option), ← / →, Gallery, and ▸ to fold the strip away. The
  strip's toggles are kept in `localStorage`; `&shot=1` hides it.
- **Transitions** play only on a click (`data-go` + `data-transition`); a frame opened by its hash renders statically.
- **Toasts**: `data-toast` (no `data-go`) shows the text for 2 s at bottom 96.
- **Timed steps**: a frame's `auto` moves on after `after` ms, unless `shot=1`.
- **Continuity**: `Kit.store.get(key, fallback)` / `Kit.store.set(key, value)` keep click-through state (a draft typed
  on the Duo's outer display, answers) in `sessionStorage`; Reset clears it. Read it only when `ctx.live`, so screenshots
  and the gallery stay the same.

### Icons (`Kit.icon(name, size?)`)

SF Symbol names with dashes (`checkmark-circle-fill`, `calendar-clock`, `bubble-left-right`, …) plus the app's meanings
as aliases (`todo`, `inbox`, `knowledge`, `scheduled`, `machines`, `due`, `handoff`, `decision`, `plan`, `inspector`,
`conversation`, `breakdown`, …; see `ALIAS` in `kit/icons.js`). The `canvas/catalog` frame shows every name. Color is
`currentColor`; without a size the icon is `1em`. Add one by putting its SVG path in `P` in `kit/icons.js`.

### Utilities

`Kit.esc(text)` (escape data you did not write) · `Kit.join(list)` · `Kit.when(cond, html)` · `Kit.cls(...)` ·
`Kit.css(key, text)` · `Kit.renderFrame(variant, frame, { theme, mode, shot })` · `Kit.afterRender(root)` · `Kit.devices` ·
`Kit.variants()` · `Kit.frames(variant)` · `Kit.goAttr(id, transition)` · `Kit.toastAttr(text)` · `Kit.navAttrs({ go,
transition, toast })` · `Kit.baseOf(id)` · `Kit.stateOf(id)` · `Kit.store` · `Kit.clock()`.

## Screenshots

The window must equal the device size, or the frame is cut or padded.

```sh
# every frame of a variant, light and dark, 2×: <out dir>/<variant>/<frame id with / → ->-<theme>.png
tools/shoot-all.sh V1 /path/to/shots            # or: all; optional [scale] ["light dark"]
# one frame (shoot.sh adds shot=1 when the hash lacks it)
tools/shoot.sh frame.html "v=V1&f=iphone/list~calm&theme=dark" 402 874 /path/to/out.png 2
```

Both tools put `shot=1` in the hash: no harness strip, no transitions, no timed steps (headless Chrome's virtual time
would otherwise run them), so the PNG is the frame's static state.

`shoot-all.sh` reads the frame list from `frame.html#list=V1` (a JSON list in `<pre id="frames">`, and
`variant / id / device / w / h` lines in `<pre id="frames-tsv">`) with headless Chrome's `--dump-dom`, then runs
`shoot.sh` per frame and theme, 4 at a time (`JOBS`). Every Chrome run has a time limit (`SHOOT_TIMEOUT`, 60 s); the
last line is the verdict (`verdict: ok N / failed M`). `CHROME` overrides the browser path. Keep screenshots out of the
repository unless a PR needs them.

Then look at every PNG: clipped or wrapped text, misaligned chrome, colors in dark, frames not filling the window.

## Conventions

- **Light and dark**, always; colors only from `kit/tokens.css` variables (no hex in a variant; add a token if one is
  missing). Type, spacing and radii from the tokens too.
- **Button text never wraps**: one line; capsule rows stay one row and fade at the edge; what does not fit goes into ⋯.
- **No runtime or path words on default screens** (super rule 4): no model ids, session ids, paths, repositories,
  engine or launcher names for everyday to-dos. Developer detail belongs behind a disclosure or in Agents.
- **One state vocabulary**: words, marks and tones from `ui.phases` (the To-dos components, sentence case as in the
  synthesis glossary: "Needs you", "Just finished") or `ui.states` (the general components); section headers in sentence
  case; buttons in Title Case ("Start When It’s Back", "Review Plan").
- **Counts come from `Data`** (`Data.counts`, `Data.sections`, `Data.view(state)`), never typed into a frame: the
  sidebar, the bar, the section header, the tray and the Live Activity must agree (synthesis §3.0.5).
- **Tool steps fold into one line** (super rule 2): `ui.fold` / `msg.tools`, never expanded by default.
- **iPhone Duo**: on the outer display and the inner display on its side, the page's controls live in the side column
  (symbol and name); nothing slides sideways there (the app cross-fades). Keep each pane on one side of a crease.
- **Public repository**: made-up people, places and data; placeholders such as `persona.example.com`; no external
  fonts, scripts or images.
- **Variants never leak**: scope CSS to `[data-variant="V1"]`, prefix classes (`v1-…`), avoid element `id`s (the
  gallery draws many frames on one page).

## Limits

- Liquid Glass is approximated with `backdrop-filter: blur(20px) saturate(180%)` over a translucent fill and a 0.5 pt
  stroke; the real material refracts and adapts.
- Text uses the system font through `-apple-system, BlinkMacSystemFont, system-ui`; off a Mac the text falls back to
  other fonts and lines break elsewhere.
- Frames are static pictures: no scrolling. Motion is limited to spinners (off with Reduce Motion), the gallery's theme
  change and frame.html's transitions between frames, which approximate the system's (the content slides or fades as a
  whole; the pane transitions are a cross-fade and slide, not two columns moving).
- iPhone and iPad sizes and safe areas are approximate; the Duo's side-column metrics follow `docs/apple/ios.md`
  ("iPhone Duo") and `Theme.Shell`. Android frames reuse the iOS components; they show layout, not Material styling.
