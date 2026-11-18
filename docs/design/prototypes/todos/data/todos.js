/*
 * Shared mock data for every variant: synthesis §3.0.5. Made-up people, places and prices only (public repository).
 * Variants read it through ctx.data (= window.Data); never copy a number into a variant, read it from here, so every
 * frame shows the same counts.
 *
 * "Now" is Wednesday 18 November 2026, 10:14. The owner is "you". Computers: MacBook, Desk Mac. Projects: Kyoto trip,
 * Weather App, Home. One Pinned direction, Release lead, which supervises the Weather App release.
 *
 *   Data.todo('t01')                 a to-do ('t01' or 'T01'); Data.todos is the list in rank order
 *   Data.ask('a1') · Data.run('widget') · Data.project('kyoto') · Data.computer('macbook') · Data.place('all')
 *   Data.counts                      every number a screen shows (N, sections, open, Assistant, Agents badge, …)
 *   Data.sections(place, filter)     sections of a place in order, each { id, title, count, look, fold, todos }
 *   Data.bar(place)                  the section bar's chips (All · Needs you · In progress · To do · Done)
 *   Data.isCalm(place)               the calm rule: only plain To do rows and nothing needs the owner
 *   Data.view(state)                 the same API over a changed copy, for states made in the prototype:
 *                                    { answered: { a1: 'hanami' | 'kaede' | 'tsuki' }, approved: { a2: true },
 *                                      a3: 'back', ticked: ['t11'], elsewhere: 'allowed', calm: true }
 *   Data.check()                     compares Data.counts with the spec's numbers (Data.expected); [] when they match
 *
 * Shapes (all plain objects):
 *   todo  { id, num, title, project, phase, section, canWait, row: { mark, lead, text, tone, all }, trailing, unseen,
 *           answer: { kind: 'ok' | 'door' | 'reply', label, short, ask, option, options }, line3, state: { word, text },
 *           recap, chips[], notes, brief, doneWhen: { items[{ text, done }], met, total }, steward, since, asks[],
 *           steps[{ text, state, who, word, run }], runs[], results[{ kind, label, outcome }], decisions[], rounds[],
 *           allCount, workDetails, composer: { to, menu[] } }
 *   round { id, day, time, from, text, status, fold, items[{ kind: 'reply' | 'run' | 'live' | 'needs' | 'relay' |
 *           'scope' | 'decision' | 'kept' | 'asked' | 'result', … }] } or { id, kind: 'background', day, text }
 * Phase words are sentence case (synthesis §2.11): Needs you · Working · Just finished · Waiting · To do · Someday · Done.
 */
(function () {
  /* ------------------------------------------------------------------ fixed facts */

  const NOW = {
    label: 'Wednesday 18 November 2026, 10:14',
    iso: '2026-11-18T10:14',
    time: '10:14',
    weekday: 'Wednesday',
    lockDate: 'Wednesday 18 November',
    short: 'Wed 18 Nov',
    statusDate: 'Wed Nov 18', // iPad status bar
  };

  const WORDS = {
    // phase → the word a state line shows (sentence case, the glossary in synthesis §2.11)
    needs_you: 'Needs you', working: 'Working', ready: 'Just finished', waiting: 'Waiting', todo: 'To do',
    someday: 'Someday', done: 'Done',
  };
  const TONES = { needs_you: 'needs', working: 'working', ready: 'ready', waiting: 'idle', todo: 'idle', someday: 'idle', done: 'idle' };

  const SECTIONS = [
    { id: 'needs', title: 'Needs you', look: 'needs' },
    { id: 'can_wait', title: 'Can wait', look: 'none', fold: true, parent: 'needs' },
    { id: 'working', title: 'Working', look: 'neutral' },
    { id: 'waiting', title: 'Waiting', look: 'neutral' },
    { id: 'todo', title: 'To do', look: 'neutral' },
    { id: 'just_finished', title: 'Just finished', look: 'neutral' },
    { id: 'someday', title: 'Someday', look: 'none', fold: true },
    { id: 'done', title: 'Done', look: 'none', fold: true },
  ];
  const FILTERS = {
    // Order per DESIGN_SYSTEM 3.4 (To-dos status sections): act on, then unfinished work, then what just finished.
    all: ['needs', 'can_wait', 'working', 'waiting', 'todo', 'just_finished', 'someday'],
    needs: ['needs', 'can_wait'],
    // The tray's In progress (#8237): Working and Waiting, Just finished as an uncounted group at its foot.
    progress: ['working', 'waiting', 'just_finished'],
    todo: ['todo', 'someday'],
    done: ['done'],
  };

  const computers = [
    { id: 'macbook', name: 'MacBook', online: true },
    { id: 'deskmac', name: 'Desk Mac', online: false, offlineSince: '9:58' },
  ];

  const projects = [
    { id: 'kyoto', name: 'Kyoto trip', hue: 'teal' },
    { id: 'weather', name: 'Weather App', hue: 'blue' },
    { id: 'home', name: 'Home', hue: 'orange' },
  ];

  // Pinned directions (常驻) live under Persona Chats › Pinned; they are not To-dos places.
  const directions = [
    { id: 'release-lead', title: 'Release lead', pinned: true, supervises: ['t02'], project: 'weather' },
  ];

  // A session that needs the owner and belongs to no to-do (the Assistant's "Elsewhere").
  const elsewhere = {
    id: 'tidy', title: 'Tidy the settings code', computer: 'macbook', state: 'needs',
    ask: 'Allow running the test suite?', actions: [{ label: 'Deny' }, { label: 'Allow Once', primary: true }],
  };

  // Places of To-dos (synthesis §2.3). Projects follow, one place each.
  const places = [
    { id: 'all', title: 'All To-dos', icon: 'checklist', empty: { title: 'Nothing to do yet.', body: 'Write down anything you want done. Persona can help with the rest.', action: 'New To-Do' } },
    { id: 'today', title: 'Today', icon: 'sun-fill', empty: { title: 'Nothing for today.', more: '3 coming up this week' } },
    { id: 'upcoming', title: 'Upcoming', icon: 'calendar', empty: { title: 'Nothing scheduled.', body: 'To-dos with a date show up here.' } },
  ].concat(projects.map((p) => ({
    id: p.id, title: p.name, icon: 'project', hue: p.hue, project: p.id,
    empty: { title: `Nothing open in ${p.name}.`, action: `New To-Do in ${p.name}` },
  })));

  /* ------------------------------------------------------------------ asks */

  const asks = {
    a1: {
      id: 'a1', todo: 't01', home: 'todo', kind: 'decision', risk: 'safe', lockscreen: 'answer',
      title: 'Which ryokan?',
      why: 'Hanami is 4 min from Gion and has a late check-in. Persona picks it on Fri unless you answer.',
      prices: 'Hanami ¥58k · Kaede ¥52k · Tsuki ¥59k a night',
      options: [
        { id: 'hanami', label: 'Ryokan Hanami', short: 'Hanami', price: '¥58k', recommended: true, primaryLabel: 'Ryokan Hanami · Recommended' },
        { id: 'kaede', label: 'Kaede', short: 'Kaede', price: '¥52k' },
        { id: 'tsuki', label: 'Tsuki', short: 'Tsuki', price: '¥59k' },
      ],
      default: { when: 'Fri 18:00', option: 'hanami', text: 'Persona picks Hanami on Fri unless you answer' },
      exits: ['Later', 'Don’t handle', 'Discuss…'],
      later: 'later today',
      answeredPrefix: 'You chose',
      push: {
        title: 'Plan the Kyoto trip',
        body: 'Which ryokan? Hanami is recommended: 4 min from Gion, ¥58k a night.',
        actions: [{ label: 'Ryokan Hanami (Recommended)', option: 'hanami', recommended: true }, { label: 'Kaede', option: 'kaede' }, { label: 'Tsuki', option: 'tsuki' }],
      },
      live: { ask: 'Which ryokan?', button: 'Hanami' },
    },
    a2: {
      id: 'a2', todo: 't02', home: 'run', run: 'widget', kind: 'plan', risk: 'open', lockscreen: 'open_only',
      title: 'Plan awaiting approval',
      summary: 'One widget target, small and medium sizes, about 2 h.',
      plan: ['Timeline provider', 'Small and medium layouts', 'Snapshot tests'],
      estimate: 'About 2 h.',
      primary: 'Approve', secondary: ['Edit', 'Don’t Start'],
      door: 'Review Plan', exits: ['Later', 'Don’t handle'],
      pointer: 'Plan awaiting approval · answer in the conversation →',
      approvedText: 'Approved',
      live: { ask: 'Plan awaiting approval', button: 'Open' },
    },
    a3: {
      id: 'a3', todo: 't03', home: 'todo', kind: 'retry', risk: 'safe',
      title: 'Didn’t start',
      why: 'Desk Mac has been offline since 9:58.',
      primary: 'Start When It’s Back', secondary: ['Use MacBook Instead'],
      exits: ['Later', 'Don’t handle', 'Discuss…'],
      after: 'Starts when Desk Mac is back',
      live: { ask: 'Didn’t start · Desk Mac is offline', button: 'Start When It’s Back' },
    },
  };

  /* ------------------------------------------------------------------ to-dos (rank order) */

  const P = (id) => projects.find((p) => p.id === id);
  const chipProject = (id) => ({ icon: 'project', label: P(id).name, hue: P(id).hue });

  const todos = [
    {
      id: 't01', num: 'T01', title: 'Plan the Kyoto trip', project: 'kyoto', phase: 'needs_you', section: 'needs',
      row: { mark: 'needs', lead: 'Which ryokan?', text: 'Hanami recommended', tone: 'needs' },
      trailing: 'Apr 3', due: { date: '2027-04-03', label: 'Apr 3' }, dates: 'Apr 3–9',
      answer: { kind: 'ok', label: 'OK: Ryokan Hanami', short: 'OK: Hanami', ask: 'a1', option: 'hanami', options: true },
      line3: 'Kyoto trip · Steps 2/5',
      state: { word: 'Needs you', text: 'Persona found 3 ryokans near Gion', short: '3 ryokans near Gion' },
      recap: 'flights held · 1 question',
      chips: [{ icon: 'calendar', label: 'Apr 3–9' }, chipProject('kyoto')],
      notes: 'Two of us. Ryokan near Gion, under ¥60k a night.',
      brief: 'Plan the Kyoto trip, Apr 3–9, ryokan near Gion, budget ¥60k a night.',
      doneWhen: { items: [{ text: 'Flights booked', done: true }, { text: 'Ryokan booked' }, { text: 'Day plan ready' }], met: 1, total: 3 },
      steward: 'Persona', since: '10:11', asks: ['a1'],
      steps: [
        { text: 'Choose dates', state: 'done', who: 'you' },
        { text: 'Book flights', state: 'done', who: 'Persona' },
        { text: 'Pick a ryokan', state: 'needs', word: 'needs you' },
        { text: 'Get JR passes', state: 'idle', who: 'Persona' },
        { text: 'Plan each day', state: 'idle' },
      ],
      runs: [{ id: 'ryokan-search', title: 'Ryokan search', role: 'helper', state: 'done', ended: '10:11', summary: 'worked 9 min → 3 ryokans' }],
      results: [
        { kind: 'doc', label: 'Flights', outcome: 'held until Fri', detail: '¥84,200 for two' },
        { kind: 'doc', label: 'Ryokan shortlist', outcome: '3' },
      ],
      decisions: [
        { text: 'Dates: Apr 3–9', who: 'you', when: 'Mon 9:12' },
        { text: 'Flights: option B, ¥84,200 for two', who: 'you', when: 'Mon 18:40' },
      ],
      allCount: 4,
      rounds: [
        {
          id: 'r1', day: 'Mon 16 Nov', time: '9:12', from: 'the Assistant', status: 'Delivered to Persona',
          text: 'Plan the Kyoto trip, Apr 3–9, ryokan near Gion, budget ¥60k a night.',
          fold: 'Persona: 5 steps; flights first',
          items: [
            { kind: 'reply', who: 'Persona', text: 'I’d do it in 5 steps. I’ll start with flights.' },
            { kind: 'kept', text: 'You kept the steps · 5' },
            { kind: 'run', who: 'Persona', text: 'worked 6 min → 2 flight options' },
          ],
        },
        {
          id: 'r2', day: 'Mon 16 Nov', time: '18:40', status: 'Delivered to Persona',
          text: 'Option B for flights.',
          fold: 'Persona: held until Fri, ¥84,200',
          items: [
            { kind: 'reply', who: 'Persona', text: 'Held until Friday, ¥84,200 for two.' },
            { kind: 'decision', text: 'Flights: option B' },
            { kind: 'result', text: 'Flights · held until Fri' },
          ],
        },
        { id: 'bg-tue', kind: 'background', day: 'Tue 17 Nov', when: 'Tue', text: 'Persona checked flight prices · no change' },
        {
          id: 'r3', day: 'Today', time: '10:02', status: 'Delivered to Persona',
          text: 'near Gion please, under ¥60k',
          fold: '▸ Ryokan search worked 9 min → 3',
          items: [
            { kind: 'run', who: 'Ryokan search', text: 'worked 9 min → 3 ryokans', steps: ['Searched 41 ryokans near Gion', 'Kept 3 under ¥60k with a late check-in', 'Checked the walk to Gion for each'] },
            { kind: 'asked', text: 'Which ryokan?' },
          ],
        },
      ],
      // Rounds the owner makes in the prototype (V1-03 / V1-04 #sent; V2's longer exchange).
      extraRounds: {
        r4: {
          id: 'r4', day: 'Today', time: 'now', status: 'Delivered to Persona',
          text: 'also check that breakfast is included',
          fold: 'Persona: Hanami and Tsuki include breakfast',
          items: [{ kind: 'reply', who: 'Persona', text: 'Hanami and Tsuki include breakfast; Kaede doesn’t.' }],
        },
        v2a: {
          id: 'v2a', day: 'Today', time: '10:20', status: 'Delivered to Persona', text: 'is Hanami quiet at night?',
          items: [{ kind: 'reply', who: 'Persona', text: 'Reviews say rooms facing the garden are quiet; I can ask for one.' }],
        },
        v2b: {
          id: 'v2b', day: 'Today', time: '10:22', status: 'Delivered to Persona', text: 'yes, and late check-in',
          items: [{ kind: 'reply', who: 'Persona', text: 'Noted for the booking.' }],
        },
      },
      drafts: { quickNote: 'also check that breakfast is included', pane: 'and ask about a quiet room', duo: 'also check breakf' },
      composer: { to: { label: 'Persona' }, menu: [] },
      menu: ['Copy Link', 'Move to Project…', 'Someday', 'Decisions (2)', 'Chat Topic…', 'Start an Agent on a Computer…', 'Delete…'],
    },
    {
      id: 't02', num: 'T02', title: 'Release Weather App 1.3', project: 'weather', phase: 'needs_you', section: 'needs',
      row: { mark: 'needs', lead: 'Widget: Plan awaiting approval', tone: 'needs' },
      trailing: 'Fri', due: { date: '2026-11-20', label: 'Fri' },
      answer: { kind: 'door', label: 'Review Plan', ask: 'a2' },
      line3: 'Weather App · Steps 1/3',
      state: { word: 'Needs you', text: 'Widget’s plan needs your OK · 1 working · 1 done', short: 'Widget’s plan needs your OK' },
      recap: '6 screenshots ready',
      chips: [{ icon: 'calendar', label: 'Fri' }, chipProject('weather')],
      notes: '',
      brief: 'Ship 1.3 with the new forecast view and release notes by Friday. No beta.',
      doneWhen: { items: [{ text: 'Build uploaded' }, { text: 'Release notes approved' }, { text: 'Widget ships' }], met: 0, total: 3 },
      steward: 'Release lead', stewardPinned: true, since: 'Tue 9:40', asks: ['a2'],
      steps: [
        { text: 'Widget', state: 'needs', word: 'needs your OK', run: 'widget' },
        { text: 'Release notes', state: 'working', word: 'working · 3 min', run: 'release-notes' },
        { text: 'Screenshots', state: 'done', word: '6 images', run: 'screenshots' },
      ],
      runs: [
        { id: 'widget', title: 'Widget', role: 'worker', computer: 'macbook', state: 'needs', minutes: '8 min', canSteer: true, session: true, started: '10:06', latest: 'Plan ready for your OK' },
        { id: 'release-notes', title: 'Release notes', role: 'helper', computer: 'macbook', state: 'working', minutes: '3 min', canSteer: true, session: true, latest: 'drafting What’s New' },
        { id: 'screenshots', title: 'Screenshots', role: 'helper', computer: 'macbook', state: 'done', session: true, summary: 'worked 12 min → 6' },
      ],
      results: [{ kind: 'doc', label: 'Screenshots', outcome: '6' }, { kind: 'doc', label: 'What’s New', outcome: 'draft' }],
      decisions: [],
      allCount: 2,
      workDetails: {
        summary: '3 Agents · 1 change',
        rows: [
          { mark: 'needs', text: 'Widget · worker · MacBook · Needs you · 8 min', run: 'widget' },
          { mark: 'working', text: 'Release notes · helper · MacBook · Working · 3 min', run: 'release-notes' },
          { mark: 'done', text: 'Screenshots · helper · MacBook · Done', run: 'screenshots' },
          { icon: 'pr', text: 'Code change #301 · weather-app · checks passed · open' },
          { icon: 'checklist', text: 'Done when: build uploaded ○ · notes approved ○ · widget ships ○' },
        ],
      },
      rounds: [
        {
          id: 'r1', day: 'Tue 17 Nov', time: '9:40', status: 'Delivered to Release lead',
          text: 'Ship 1.3 on Friday, no beta this time.',
          fold: 'Release lead split it into 3 steps',
          items: [
            { kind: 'reply', who: 'Release lead', text: 'Splitting it into widget, notes and screenshots. Starting all three.' },
            { kind: 'run', who: 'Screenshots', text: 'worked 12 min → 6 images', done: true },
            { kind: 'live', who: 'Release notes', text: 'drafting What’s New' },
            { kind: 'needs', who: 'Widget', text: 'Plan awaiting approval' },
          ],
        },
      ],
      extraRounds: {
        // S5, made in the prototype; status runs Sending → Processing → Delivered to Release lead.
        r2: {
          id: 'r2', day: 'Today', time: 'now', status: 'Delivered to Release lead',
          statuses: ['Sending', 'Processing', 'Delivered to Release lead'],
          text: 'also add the home-screen widget to the release notes',
          fold: 'Release lead: adding it to 1.3',
          items: [
            { kind: 'reply', who: 'Release lead', text: 'Adding it to 1.3. I’ll tell Release notes after its current step.' },
            { kind: 'relay', to: 'Release notes', state: 'queued', sendNow: true },
            { kind: 'scope', text: '+ home-screen widget in the release notes' },
            { kind: 'live', who: 'Release notes', text: 'adding the widget' },
          ],
        },
      },
      relayStates: {
        queued: 'Queued for Release notes · after this step',
        delivered: 'Delivered to Release notes',
        now: 'Sent now to Release notes',
      },
      scopeLine: '+ home-screen widget · Undo',
      drafts: { s5: 'also add the home-screen widget to the release notes' },
      // The Widget run's own session (the home of a2).
      session: {
        run: 'widget', header: 'Widget · worker · started 10:06',
        text: 'I read the 1.2 widget code and the design notes. Here is the plan.',
        fold: 'Worked 2 min · read 9 files',
        menu: ['Open Full Session', 'Try Another Way', 'Stop'],
      },
      composer: {
        to: { label: 'Release lead', menu: true },
        menu: [
          { label: 'Release lead', check: true },
          { label: 'Persona' },
          { label: 'Widget', sub: 'worker · Needs you', run: 'widget' },
          { label: 'Release notes', sub: 'helper · Working', run: 'release-notes' },
        ],
      },
      channels: [
        { label: 'Release lead', sub: 'conversation', check: true },
        { label: 'Widget', sub: 'Needs you', run: 'widget' },
        { label: 'Release notes', sub: 'Working', run: 'release-notes' },
        { label: 'Screenshots', sub: 'Done', run: 'screenshots' },
      ],
    },
    {
      id: 't03', num: 'T03', title: 'Sort last year’s photos', project: 'home', phase: 'needs_you', section: 'needs',
      row: { mark: null, lead: 'Didn’t start', text: 'Desk Mac is offline', tone: 'danger', all: true },
      trailing: '16m',
      answer: { kind: 'ok', label: 'Start When It’s Back', ask: 'a3', option: 'back', options: true },
      state: { word: 'Needs you', text: 'Didn’t start · Desk Mac is offline' },
      chips: [chipProject('home')],
      notes: 'Group by trip, delete blurry duplicates, keep the originals.',
      steward: 'Persona', since: '9:58', asks: ['a3'],
      runs: [{ id: 'photo-sorter', title: 'Photo sorter', role: 'worker', computer: 'deskmac', state: 'needs', outcome: 'start failed: offline' }],
      steps: [], results: [], decisions: [], rounds: [], allCount: 1,
      composer: { to: { label: 'Persona' }, menu: [] },
    },
    {
      id: 't13', num: 'T13', title: 'Renew the car insurance', project: 'home', phase: 'needs_you', section: 'can_wait', canWait: true,
      row: { mark: null, lead: 'Awaiting Reply', text: 'Persona asked about roadside cover' },
      trailing: '1h',
      answer: { kind: 'reply', label: 'Reply' },
      state: { word: 'Awaiting Reply', text: 'Persona asked: Do you want roadside cover?' },
      chips: [chipProject('home')],
      question: 'Do you want roadside cover? It adds ¥4,800 a year.',
      steward: 'Persona', steps: [], results: [], decisions: [], rounds: [], allCount: 1,
      composer: { to: { label: 'Persona' }, menu: [] },
    },
    {
      id: 't05', num: 'T05', title: 'Draft the school newsletter', project: 'home', phase: 'working', section: 'working',
      row: { mark: 'working', text: 'Persona is writing part 2 of 3' },
      trailing: 'Thu', due: { date: '2026-11-19', label: 'Thu' },
      state: { word: 'Working', text: 'Persona is writing part 2 of 3 · 12 min' },
      chips: [{ icon: 'calendar', label: 'Thu' }, chipProject('home')],
      steps: [
        { text: 'Part 1 · school news', state: 'done', who: 'Persona' },
        { text: 'Part 2 · the winter fair', state: 'working', who: 'Persona' },
        { text: 'Part 3 · dates for December', state: 'idle', who: 'Persona' },
      ],
      steward: 'Persona', results: [], decisions: [], rounds: [], allCount: 1,
      composer: { to: { label: 'Persona' }, menu: [] },
    },
    {
      id: 't06', num: 'T06', title: 'Get quotes for the garden gate', project: 'home', phase: 'working', section: 'working',
      row: { mark: 'working', text: 'Persona is comparing 2 quotes' },
      trailing: '3m',
      state: { word: 'Working', text: 'Persona is comparing 2 quotes · 3 min' },
      chips: [chipProject('home')],
      steward: 'Persona', steps: [], results: [], decisions: [], rounds: [], allCount: 1,
      composer: { to: { label: 'Persona' }, menu: [] },
    },
    {
      id: 't07', num: 'T07', title: 'Book the dentist', project: 'home', phase: 'ready', section: 'just_finished', unseen: true,
      row: { mark: 'ready', lead: 'Booked Tue 10:00', text: 'check and tick', tone: 'ready' },
      trailing: '8m',
      state: { word: 'Just finished', text: 'Booked Tue 10:00 · check and tick' },
      chips: [chipProject('home')],
      results: [{ kind: 'doc', label: 'Appointment', outcome: 'Tue 24 Nov 10:00' }],
      primaryAction: 'Mark Done',
      steward: 'Persona', steps: [], decisions: [], rounds: [], allCount: 1,
      composer: { to: { label: 'Persona' }, menu: [] },
    },
    {
      id: 't08', num: 'T08', title: 'Fix widget refresh crash', project: 'weather', phase: 'done', section: 'just_finished', unseen: true, closed: true,
      row: { mark: null, text: 'Done by Persona · merged and live' },
      trailing: '8:55', closedAt: '8:55', closedBy: 'Persona',
      state: { word: 'Done', text: 'Done by Persona · merged and live' },
      chips: [chipProject('weather')],
      doneWhen: { items: [{ text: 'Merged', done: true }, { text: 'Deployed', done: true }], met: 2, total: 2 },
      menu: ['Reopen'],
      steward: 'Persona', steps: [], results: [], decisions: [], rounds: [], allCount: 1,
      composer: { to: { label: 'Persona' }, menu: [], placeholder: 'Ask about this to-do…' },
    },
    {
      id: 't04', num: 'T04', title: 'Fix login crash for SSO users', project: 'weather', phase: 'waiting', section: 'waiting', priority: true,
      row: { mark: 'waiting', lead: 'For merge', text: 'checks passed' },
      trailing: '40m',
      state: { word: 'Waiting', text: 'For merge · checks passed · since 9:30' },
      chips: [{ icon: 'flag-fill', label: 'High', tone: 'caution' }, chipProject('weather')],
      brief: 'SSO users see a blank page after sign-in.',
      doneWhen: { items: [{ text: 'Fixed', done: true }, { text: 'Merged' }, { text: 'Deployed' }], met: 1, total: 3 },
      steward: 'Persona',
      steps: [
        { text: 'Reproduce', state: 'done', who: 'Login fix' },
        { text: 'Fix and test', state: 'done', who: 'Login fix' },
        { text: 'Merge', state: 'waiting', who: 'a reviewer' },
        { text: 'Deploy', state: 'idle' },
      ],
      runs: [{ id: 'login-fix', title: 'Login fix', role: 'worker', computer: 'macbook', state: 'done', ended: 'Mon 14:43', summary: 'worked 38 min → found it' }],
      results: [{ kind: 'change', label: 'Code change', outcome: 'ready to review' }],
      decisions: [],
      workDetails: {
        summary: '1 Agent · 1 code change',
        rows: [
          { mark: 'done', text: 'Login fix · worker · MacBook · Ended', run: 'login-fix' },
          { icon: 'pr', text: 'PR #812 · weather-app · checks passed · open · 2 files +42 −7', action: 'Review Changes' },
        ],
      },
      allCount: 3,
      rounds: [
        {
          id: 'r1', day: 'Mon 16 Nov', time: '14:05', from: 'the Assistant', status: 'Delivered to Persona',
          text: 'Fix the login crash for SSO users in the Weather App; crash log attached.',
          fold: '▸ Login fix 38 min → change ready',
          items: [
            { kind: 'run', who: 'Login fix', text: 'worked 38 min → found it: the token refresh races the SSO redirect' },
            { kind: 'result', text: 'Code change · ready to review' },
          ],
        },
        { id: 'bg-today', kind: 'background', day: 'Today', when: 'Today 9:30', text: 'Checks passed' },
      ],
      composer: { to: { label: 'Persona' }, menu: [] },
    },
    {
      id: 't09', num: 'T09', title: 'Update the privacy policy page', project: 'weather', phase: 'waiting', section: 'waiting',
      row: { mark: 'waiting', lead: 'A reviewer to merge', text: 'since Tue' },
      trailing: 'Tue',
      state: { word: 'Waiting', text: 'Waiting for a reviewer to merge · since Tue · Persona checks hourly' },
      wakes: 'when merged', menu: ['Remind Reviewer', 'Stop Waiting'],
      chips: [chipProject('weather')],
      steward: 'Persona', steps: [], results: [], decisions: [], rounds: [], allCount: 1,
      composer: { to: { label: 'Persona' }, menu: [] },
    },
    {
      id: 't10', num: 'T10', title: 'Keep an eye on the app size after 1.3', project: 'weather', phase: 'waiting', section: 'waiting', kind: 'concern',
      row: { mark: 'waiting', lead: 'Persona reports back Fri' },
      trailing: 'Fri',
      state: { word: 'Waiting', text: 'Persona reports back Fri · watching the next 3 builds' },
      chips: [chipProject('weather')],
      steward: 'Persona', steps: [], results: [], decisions: [], rounds: [], allCount: 1,
      composer: { to: { label: 'Persona' }, menu: [] },
    },
    {
      id: 't11', num: 'T11', title: 'Renew passport', project: null, phase: 'todo', section: 'todo',
      row: null, trailing: 'Fri', due: { date: '2026-11-20', label: 'Fri 20 Nov' },
      state: null,
      chips: [{ icon: 'calendar', label: 'Fri 20 Nov' }],
      notes: 'Old one expires in May. Photo booth at the post office.',
      // Capture (V1-07): typed text, the parsed date, and what Break Into Steps suggests.
      capture: { text: 'Renew passport fri', token: 'fri', parsed: 'Fri 20 Nov', reply: 'I can book the photo booth and fill the form. Want me to start?' },
      suggestedSteps: ['Fill the form', 'Get photos taken', 'Book the post office'],
      steward: 'Persona', steps: [], results: [], decisions: [], rounds: [], allCount: 0,
      composer: { to: { label: 'Persona' }, menu: [] },
    },
    {
      id: 't12', num: 'T12', title: 'Buy groceries', project: 'home', phase: 'todo', section: 'todo',
      row: null, trailing: '', line3: 'Home · Steps 1/4',
      state: null,
      chips: [chipProject('home')],
      steps: [
        { text: 'Milk', state: 'done' }, { text: 'Eggs', state: 'idle' }, { text: 'Miso', state: 'idle' }, { text: 'Tofu', state: 'idle' },
      ],
      steward: 'Persona', results: [], decisions: [], rounds: [], allCount: 0,
      composer: { to: { label: 'Persona' }, menu: [] },
    },
    {
      id: 't14', num: 'T14', title: 'Learn to bake sourdough', project: null, phase: 'someday', section: 'someday',
      row: { mark: null, text: 'Someday' }, trailing: '', state: { word: 'Someday', text: '' },
      chips: [], steward: 'Persona', steps: [], results: [], decisions: [], rounds: [], allCount: 0,
      composer: { to: { label: 'Persona' }, menu: [] },
    },
  ];

  // The numbers in synthesis §3.0.5 that every place must show. Data.check() compares the computed counts with these.
  const EXPECTED = {
    needs: 3, canWait: 1, working: 2, justFinished: 2, waiting: 3, todo: 2, someday: 1, inProgress: 5, open: 12,
    byProject: { kyoto: 1, weather: 1, home: 1 }, elsewhere: 1, assistantNeeds: 4, agentsBadge: 2, fleet: '2 need you',
  };

  /* ------------------------------------------------------------------ prototype states (Data.view) */

  const clone = (x) => JSON.parse(JSON.stringify(x));

  function applyState(list, st) {
    const byId = {};
    list.forEach((t) => { byId[t.id] = t; });
    const ans = st.answered || {};
    if (ans.a1) {
      const opt = asks.a1.options.find((o) => o.id === ans.a1) || asks.a1.options[0];
      const t = byId.t01;
      Object.assign(t, {
        phase: 'working', section: 'working', answer: null, asks: [],
        row: { mark: 'working', text: `Persona is booking ${opt.label}` },
        state: { word: 'Working', text: `Persona is booking ${opt.label}` },
        recap: null, answered: { ask: 'a1', option: opt.id, label: opt.label, text: `You chose ${opt.label}` },
      });
      t.steps = t.steps.map((s) => (s.state === 'needs' ? { text: s.text, state: 'done', who: 'you' } : s));
      t.line3 = 'Kyoto trip · Steps 3/5';
    }
    if ((st.approved || {}).a2) {
      const t = byId.t02;
      Object.assign(t, {
        phase: 'working', section: 'working', answer: null, asks: [],
        row: { mark: 'working', text: 'Widget is building the widget' },
        state: { word: 'Working', text: '2 working · 1 done' },
        answered: { ask: 'a2', text: 'Approved' },
      });
      t.steps = t.steps.map((s) => (s.run === 'widget' ? Object.assign({}, s, { state: 'working', word: 'working' }) : s));
      t.runs = t.runs.map((r) => (r.id === 'widget' ? Object.assign({}, r, { state: 'working', latest: 'building the widget' }) : r));
    }
    if (st.a3 === 'back') {
      const t = byId.t03;
      Object.assign(t, {
        phase: 'waiting', section: 'waiting', answer: null, asks: [],
        row: { mark: 'waiting', text: asks.a3.after },
        state: { word: 'Waiting', text: asks.a3.after },
      });
    }
    (st.ticked || []).forEach((id) => {
      const t = byId[id];
      if (t) Object.assign(t, { phase: 'done', section: 'done', closed: true, ticked: true, answer: null });
    });
    let out = list;
    if (st.calm) out = list.filter((t) => ['t11', 't12', 't14'].includes(t.id));
    // Re-rank: sections keep the base order; a to-do that moved joins its new section at the top (newest first).
    const moved = out.filter((t) => t.section !== (todos.find((b) => b.id === t.id) || {}).section);
    return moved.concat(out.filter((t) => !moved.includes(t)));
  }

  /* ------------------------------------------------------------------ the API */

  function build(state) {
    state = state || {};
    const list = applyState(clone(todos), state);
    const byId = {};
    list.forEach((t) => { byId[t.id] = t; });

    const inPlace = (t, place) => {
      if (!place || place === 'all') return true;
      if (place === 'today') return t.due && t.due.date === '2026-11-18';
      if (place === 'upcoming') return !!t.due;
      return t.project === place;
    };
    const open = (t) => !t.closed && t.section !== 'someday' && t.section !== 'done';

    function countsOf(place) {
      const L = list.filter((t) => inPlace(t, place));
      const n = (sec) => L.filter((t) => t.section === sec).length;
      const c = {
        needs: n('needs'), canWait: n('can_wait'), working: n('working'), justFinished: n('just_finished'),
        waiting: n('waiting'), todo: n('todo'), someday: n('someday'), done: n('done'),
        open: L.filter(open).length,
      };
      c.inProgress = c.working + c.waiting; // Just finished is listed under In progress but not counted (#8237)
      return c;
    }

    const counts = countsOf('all');
    counts.byProject = {};
    projects.forEach((p) => { counts.byProject[p.id] = list.filter((t) => t.project === p.id && t.section === 'needs').length; });
    counts.withoutProject = list.filter((t) => open(t) && !t.project).length;
    counts.elsewhere = state.elsewhere === 'allowed' ? 0 : 1;
    counts.assistantNeeds = counts.needs + counts.elsewhere;
    counts.agentsBadge = list.reduce((s, t) => s + (t.closed ? 0 : (t.runs || []).filter((r) => r.session && r.state === 'needs').length), 0) + counts.elsewhere;
    counts.fleet = `${counts.agentsBadge} need you`; // Fleet counts sessions (live-activity.md), not to-dos
    counts.inbox = 2;
    counts.pinned = { 'release-lead': list.filter((t) => t.id === 't02' && t.section === 'needs').length };

    const D = {
      now: NOW,
      clock: { time: NOW.time, date: NOW.statusDate }, // the devices' status bars read this
      owner: 'you',
      words: WORDS,
      tones: TONES,
      state,
      computers, projects, directions, elsewhere, places, asks,
      sectionDefs: SECTIONS,
      todos: list,
      expected: EXPECTED,
      counts,
      countsOf,
      todo: (id) => byId[String(id).toLowerCase()] || null,
      ask: (id) => asks[id] || null,
      project: (id) => projects.find((p) => p.id === id) || null,
      computer: (id) => computers.find((c) => c.id === id) || null,
      place: (id) => places.find((p) => p.id === (id || 'all')) || null,
      run: (id) => {
        for (const t of list) { const r = (t.runs || []).find((x) => x.id === id); if (r) return Object.assign({ todo: t.id }, r); }
        return null;
      },
      /** Sections of a place in order: [{ id, title, count, look ('needs' | 'neutral' | 'none'), fold, todos }].
          filter: 'all' (landing) | 'needs' | 'progress' | 'todo' | 'done'. Empty sections are left out. */
      sections(place, filter) {
        const ids = FILTERS[filter || 'all'] || FILTERS.all;
        return ids.map((sid) => {
          const def = SECTIONS.find((s) => s.id === sid);
          const rows = list.filter((t) => t.section === sid && inPlace(t, place));
          return Object.assign({}, def, { count: rows.length, todos: rows });
        }).filter((s) => s.todos.length);
      },
      /** The section bar's chips for a place: whole labels; Needs you carries the blue count; In progress and To do
          a neutral count; All and Done none. */
      bar(place) {
        const c = countsOf(place);
        // The Inbox's chips, with the landing choice first, as every bar has it (the Inbox lands on Needs you, its
        // first; Agents and To-dos land on All, their first): All · Needs you · In progress · To do · Done. Needs you
        // and All always show, the others while they have rows.
        return [
          { id: 'all', label: 'All', always: true },
          { id: 'needs', label: 'Needs you', count: c.needs, look: 'needs', always: true },
          { id: 'progress', label: 'In progress', count: c.inProgress },
          { id: 'todo', label: 'To do', count: c.todo },
          { id: 'done', label: 'Done', empty: c.done === 0 },
        ].filter((x) => x.always || x.id === 'done' || x.count > 0);
      },
      /** The calm rule (C): only the owner's plain To do rows and nothing needs the owner → no bar, no headers. */
      isCalm(place) {
        const L = list.filter((t) => inPlace(t, place) && t.section !== 'someday' && t.section !== 'done');
        return L.length > 0 && L.every((t) => t.section === 'todo');
      },
      /** Upcoming: dated open to-dos under day headings (soonest first); Today's empty state counts "this week". */
      upcoming() {
        const days = [
          { id: '2026-11-19', title: 'Thu 19 Nov' }, { id: '2026-11-20', title: 'Fri 20 Nov' },
        ];
        const dated = list.filter((t) => t.due && !t.closed && t.section !== 'done');
        const groups = days.map((d) => Object.assign({}, d, { todos: dated.filter((t) => t.due.date === d.id) }));
        groups.push({ id: 'later', title: 'Later', todos: dated.filter((t) => !days.some((d) => d.id === t.due.date)) });
        return groups.filter((g) => g.todos.length);
      },
      /** "3 coming up this week" (Today's empty state): dated to-dos due before Mon 23 Nov. */
      thisWeek: () => list.filter((t) => t.due && !t.closed && t.due.date < '2026-11-23').length,
      /** The place summary's lines (wide screens with nothing selected). */
      summary(place) {
        const c = countsOf(place);
        return [
          // The section order (DESIGN_SYSTEM 3.4), then Can wait, which is listed but not counted.
          { id: 'needs', count: c.needs, label: 'need you', look: 'needs' },
          { id: 'working', count: c.working, label: 'working' },
          { id: 'waiting', count: c.waiting, label: 'waiting' },
          { id: 'todo', count: c.todo, label: 'to do' },
          { id: 'just_finished', count: c.justFinished, label: 'just finished' },
          { id: 'can_wait', count: c.canWait, label: 'can wait' },
        ].filter((x) => x.count > 0);
      },
      view: (st) => build(Object.assign({}, state, st)),
      check() {
        const bad = [];
        const cmp = (k, a, b) => { if (JSON.stringify(a) !== JSON.stringify(b)) bad.push(`${k}: ${JSON.stringify(a)} ≠ expected ${JSON.stringify(b)}`); };
        Object.keys(EXPECTED).forEach((k) => cmp(k, counts[k], EXPECTED[k]));
        return bad;
      },
    };
    return D;
  }

  const Data = build({});
  const problems = Data.check();
  if (problems.length) console.warn('[data] counts differ from synthesis §3.0.5:\n' + problems.join('\n'));
  window.Data = Data;
})();
