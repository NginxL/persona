/*
 * KIT: the kit's own demo. It proves every device and the main components; it is not a design proposal.
 * Self-contained (its own mock data), so replacing data/todos.js never breaks it. Read it as an example of
 * how a variant composes Kit.ui; variants V1–V3 live in v1/ … v3/.
 */
(function () {
  const V = 'KIT';
  Kit.variant(V, {
    name: 'Kit demo',
    summary: 'Every device and the main components of the kit, drawn with made-up data. Not a design proposal: a reference for how frames are built (<code>v0/demo.js</code>).',
    order: 99,
  });

  // Demo-only layout, scoped to this variant as every variant's CSS must be.
  Kit.css('kit-demo', `
    [data-variant="KIT"] .d-head { padding-top: 4px; }
    [data-variant="KIT"] .d-head .k-title { margin-bottom: 6px; }
    [data-variant="KIT"] .d-head .k-chips { margin-top: 12px; }
    [data-variant="KIT"] .d-notes { margin-top: 12px; color: var(--ink-2); font-size: var(--fs-secondary); line-height: 1.42; letter-spacing: -.012em; }
    [data-variant="KIT"] .d-gap { height: 16px; }
    [data-variant="KIT"] .d-pane-title { display: flex; align-items: center; gap: 8px; font-size: var(--fs-heading); font-weight: 600; }
    [data-variant="KIT"] .d-sheet-field { padding: 14px 16px; border-radius: var(--r-card); background: var(--cell); font-size: var(--fs-title); font-weight: 600; letter-spacing: -.02em; }
    [data-variant="KIT"] .d-sheet-field small { display: block; margin-top: 4px; font-size: var(--fs-secondary); font-weight: 400; color: var(--ink-3); letter-spacing: -.012em; }
    [data-variant="KIT"] .d-side-title { padding: 18px 0 2px; }
    [data-variant="KIT"] .d-sheet-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
    [data-variant="KIT"] .d-ipadp-nav { padding-right: 424px; padding-left: 24px; }
    [data-variant="KIT"] .d-cat { position: absolute; inset: 0; padding: 32px 40px; background: var(--grouped-page); overflow: hidden; }
    [data-variant="KIT"] .d-cat h2 { margin: 0 0 10px; font-size: var(--fs-heading); font-weight: 600; }
    [data-variant="KIT"] .d-icons { display: grid; grid-template-columns: repeat(15, 1fr); gap: 4px; padding: 12px; border-radius: var(--r-panel); background: var(--cell); }
    [data-variant="KIT"] .d-icon { height: 58px; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 6px; color: var(--ink); }
    [data-variant="KIT"] .d-icon code { font: 9.5px var(--mono); color: var(--ink-2); letter-spacing: 0; max-width: 84px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    [data-variant="KIT"] .d-cols { display: grid; grid-template-columns: repeat(3, 1fr); gap: 20px; margin-top: 22px; align-items: start; }
    [data-variant="KIT"] .d-box { padding: 14px 16px 16px; border-radius: var(--r-panel); background: var(--cell); margin-bottom: 20px; }
    [data-variant="KIT"] .d-box > .k-sec:first-child { padding-top: 0; }
    [data-variant="KIT"] .d-line { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 14px; margin: 6px 0; }
  `);

  /* ---------------------------------------------------------------- mock data */

  const P = { kyoto: ['Kyoto trip', 'teal'], weather: ['Weather App', 'blue'], home: ['Home', 'orange'], health: ['Health', 'pink'] };
  const proj = (ui, id) => ui.project(P[id][0], { hue: P[id][1] });

  const kyotoAsk = (ui, o) => ui.ask(Object.assign({
    from: 'Persona asks', age: '12 min', question: 'Which ryokan should I book?',
    detail: 'All three are near Gion, have a private onsen and fit the budget.',
    recommended: { label: 'Gion Hatanaka', why: '5 min walk to Gion, ¥52k a night, free cancellation.' },
    options: ['Sowaka', 'Hoshinoya'], dismiss: 'Not Now', other: 'Other…',
  }, o));
  const releaseAsk = (ui, o) => ui.ask(Object.assign({
    from: 'Release plan asks', age: '4 min', icon: 'clipboard-list', question: 'Approve the release plan?',
    detail: 'Build 42 to TestFlight today, App Review on Thursday, release on Friday morning.',
    recommended: { label: 'Approve' }, options: ['Review Plan'], dismiss: 'Not Now',
  }, o));

  // The list's rows; `sel` marks the selected row in split layouts.
  function rows(ui, o) {
    o = o || {};
    const dense = !!o.dense;
    const r = {
      kyoto: ui.row({ title: 'Plan the Kyoto trip', state: 'needs', stateText: 'Which ryokan?', meta: [ui.due('Mar 1'), proj(ui, 'kyoto')], answers: o.answers === false ? null : [{ label: 'Gion Hatanaka', recommended: true }, { label: 'Sowaka' }, { label: 'Hoshinoya' }], fadeAnswers: true, go: o.go && o.go.kyoto, selected: o.sel === 'kyoto', dense }),
      release: ui.row({ title: 'Release Weather App 1.3', state: 'needs', stateText: 'Approve the release plan', meta: [ui.due('Today 18:00'), proj(ui, 'weather')], answers: o.answers === false ? null : [{ label: 'Approve', recommended: true }, { label: 'Review Plan' }], more: '2 more running', go: o.go && o.go.release, selected: o.sel === 'release', dense }),
      sso: ui.row({ title: 'Fix login crash for SSO users', state: 'working', stateText: 'Running the tests', meta: [ui.due('Thu'), proj(ui, 'weather')], go: o.go && o.go.sso, selected: o.sel === 'sso', dense }),
      insurance: ui.row({ title: 'Compare health insurance plans', state: 'working', stateText: 'Reading three brochures', meta: [proj(ui, 'health')], dense }),
      deposit: ui.row({ title: 'Get the rental deposit back', state: 'waiting', stateText: 'On the landlord since Mon', meta: [ui.due('Nov 28'), proj(ui, 'home')], dense }),
      passport: ui.row({ title: 'Renew passport', priority: true, meta: [ui.due('Fri'), proj(ui, 'home')], dense }),
      gift: ui.row({ title: 'Buy a gift for Mom’s birthday', meta: [ui.due('Nov 24', { overdue: false })], dense }),
      plants: ui.row({ title: 'Water the balcony plants', check: 'done', meta: [ui.due('Yesterday')], dense }),
    };
    return r;
  }

  function listBody(ui, o) {
    o = o || {};
    const r = rows(ui, o);
    const sec = (t, n, look, tone) => ui.section(t, { count: n, look, tone });
    return ui.list([sec('Needs you', 2, 'needs', 'needs'), r.kyoto, r.release])
      + ui.list([sec('Working', 2), r.sso, r.insurance])
      + ui.list([sec('Waiting', 1), r.deposit])
      + ui.list([sec('To do', 2), r.passport, r.gift]);
  }

  const filters = (ui, o) => ui.filterBar([
    { label: 'All', count: 7 },
    { label: 'Needs You', count: 2, look: 'needs' },
    { label: 'Working', count: 2 },
    { label: 'Waiting', count: 1 },
    { label: 'Done' },
  ], Object.assign({ selected: 0, fade: true }, o));

  function kyotoHead(ui, o) {
    o = o || {};
    return `<div class="d-head">
      <div class="k-title">Plan the Kyoto trip</div>
      ${ui.state('needs', { word: 'Needs You', text: 'Persona found three ryokans' })}
      ${ui.chips([ui.chip('Apr 3–9', { icon: 'calendar', look: 'neutral' }), ui.chip('Kyoto trip', { icon: 'project', look: 'neutral' }), ui.chip('Due Mar 1', { icon: 'calendar-clock', look: 'neutral' })], { add: o.add === false ? '' : 'Add' })}
      ${o.notes === false ? '' : '<div class="d-notes">Two of us. A ryokan near Gion, up to ¥60k a night; one day trip to Nara.</div>'}
    </div>`;
  }

  const kyotoSteps = (ui) => ui.steps([
    { text: 'Book flights', done: true, by: ui.deliverable({ look: 'chip', kind: 'flight', title: 'Apr 3 · Apr 9', outcome: 'Booked' }) },
    { text: 'Pick a ryokan', state: 'needs', by: ui.chip('Needs You', { tone: 'needs' }) },
    { text: 'Plan the day trips', state: 'working', by: `${ui.spinner(10)}<span>Persona</span>` },
    { text: 'Get a JR Pass' },
    { text: 'Reserve a tea ceremony', suggested: true, sub: 'Popular slots near Gion fill a month ahead' },
    { text: 'Book a table for the first night', suggested: true },
  ], { title: 'Steps', count: '1 of 4', add: 'Add Step' });

  function kyotoDetail(ui, o) {
    o = o || {};
    return `${kyotoHead(ui, o)}
      <div class="d-gap"></div>
      ${o.ask === false ? '' : kyotoAsk(ui, o.askOpts)}
      ${ui.block('', kyotoSteps(ui))}
      ${o.more ? ui.block('Results', ui.deliverable({ kind: 'flight', title: 'Flights · Apr 3 and Apr 9', outcome: 'Booked', sub: '2 seats, window' })) : ''}
      ${o.more ? ui.block('Latest', ui.round({ instruction: 'Only places with a private onsen, please.', receipt: 'Sent to ‘Hotel search’ · Delivered', summary: 'Worked 6 min · searched 14 ryokans', result: 'Three fit the budget; all have a private onsen.', resultState: 'needs' })) : ''}`;
  }

  function ssoDetail(ui, o) {
    o = o || {};
    return `<div class="d-head">
        <div class="k-title">Fix login crash for SSO users</div>
        ${ui.state('working', { text: 'Running the tests · 4 min' })}
        ${ui.chips([ui.chip('Due Thu', { icon: 'calendar-clock', look: 'neutral' }), ui.chip('Weather App', { icon: 'project', look: 'neutral' }), ui.chip('High', { icon: 'flag-fill', tone: 'caution' })], { add: o.add === false ? '' : 'Add' })}
        <div class="d-notes">The app crashes when a single sign-on session expires while it is in the background.</div>
      </div>
      ${ui.block('Working on it', ui.run({ role: 'Crash fix', where: 'On your Mac', state: 'working', latest: 'Running 212 tests after the session refresh fix.', icon: 'wrench', hue: 'blue', chevron: true }))}
      ${ui.block('Results', ui.deliverable({ kind: 'pr', title: 'PR #128 · Refresh SSO sessions early', outcome: 'Open', sub: '2 of 3 checks passed', tone: 'working' }))}
      ${ui.block('Latest', ui.round({ instruction: 'Check older iPhones too.', receipt: 'Delivered', summary: 'Worked 12 min · 9 steps', result: 'Reproduced on token expiry; the fix is pushed.', resultState: 'ready' }))}`;
  }

  function releaseDetail(ui, o) {
    o = o || {};
    return `<div class="d-head">
        <div class="k-title">Release Weather App 1.3</div>
        ${ui.state('needs', { text: 'The release plan is ready' })}
        ${ui.chips([ui.chip('Today 18:00', { icon: 'calendar-clock', look: 'neutral' }), ui.chip('Weather App', { icon: 'project', look: 'neutral' })], { add: 'Add' })}
        ${o.notes === false ? '' : '<div class="d-notes">Home-screen widget, faster radar and release notes. Submit for review by Friday.</div>'}
      </div>
      <div class="d-gap"></div>
      ${releaseAsk(ui, o.askOpts)}
      ${ui.block('Working on it', `<div class="k-stack" style="--gap:8px">
        ${ui.run({ role: 'Release plan', where: 'Persona', state: 'needs', latest: 'Plan ready: TestFlight today, review Thursday.', icon: 'clipboard-list', hue: 'blue' })}
        ${ui.run({ role: 'Widget', where: 'On your Mac', state: 'working', latest: 'Fixing the widget’s dark-mode colors.', icon: 'wrench', hue: 'orange' })}
        ${o.compactRuns ? '' : ui.run({ role: 'Release notes', where: 'Persona', state: 'done', latest: 'Drafted in English, Japanese and Chinese.', icon: 'doc', hue: 'green' })}
      </div>`, { count: 3, look: 'plain' })}
      ${ui.block('Results', `<div class="k-stack" style="--gap:8px">
        ${ui.deliverable({ kind: 'doc', title: 'Release notes 1.3', outcome: 'Ready', sub: 'Three languages' })}
        ${ui.deliverable({ kind: 'build', title: 'TestFlight build 42', outcome: 'Processing', sub: 'Uploaded 9:30', tone: 'working' })}
      </div>`)}`;
  }

  const kyotoMessages = (ui) => [
    ui.msg.day('Today 9:20'),
    ui.msg.user('Only places with a private onsen, please.', { receipt: 'Sent to ‘Hotel search’ · Delivered' }),
    ui.msg.tools('Worked 6 min · searched 14 ryokans'),
    ui.msg.agent('Three fit: <b>Gion Hatanaka</b> (¥52k), <b>Sowaka</b> (¥58k) and <b>Hoshinoya</b> (¥59k). All have a private onsen and free cancellation.', { name: 'Hotel search', icon: 'bed', hue: 'teal' }),
    ui.msg.persona('I recommend Gion Hatanaka: it is the closest to Gion and leaves room in the budget for the tea ceremony.'),
  ];

  const releaseMessages = (ui) => [
    ui.msg.day('Today 9:32'),
    ui.msg.user('Also add the widget to 1.3.', { receipt: 'Received · queued for ‘Release plan’' }),
    ui.msg.persona('Got it. The plan will take the widget once the current step ends, about 2 minutes.'),
    ui.msg.tools('3 steps'),
    ui.msg.agent('Plan updated: the widget ships in 1.3, App Review moves to Thursday. Approve it on the to-do when you are ready.', { name: 'Release plan', icon: 'clipboard-list', hue: 'blue' }),
    ui.msg.receipt('Waiting for your approval', { state: 'needs' }),
  ];

  const go = (ctx) => ({ kyoto: 'iphone/detail', release: 'ipad13-landscape/list-detail-conversation', sso: 'duo-outer/detail' });

  /* ---------------------------------------------------------------- iPhone */

  Kit.frame(V, 'iphone/list', {
    device: 'iphone', important: true,
    title: 'iPhone · To-dos list',
    note: 'Place root: large title, the capsule filter bar, sections with CountBadges, rows with inline answers, the floating composer.',
    render: (ctx) => {
      const ui = ctx.ui;
      return ui.page({
        top: ui.navbar({
          leading: [{ icon: 'line-3-horizontal', title: 'Sidebar' }],
          trailing: [{ icon: 'magnifying-glass' }, { icon: 'ellipsis' }],
          large: 'To-dos', largeSub: '2 need you · 7 open',
          below: `<div style="margin-top:10px">${filters(ui)}</div>`,
        }),
        body: listBody(ui, { go: go(ctx) }),
        bottom: ui.composer({ placeholder: 'New To-do', mic: true }),
      });
    },
  });

  Kit.frame(V, 'iphone/detail', {
    device: 'iphone', important: true,
    title: 'iPhone · To-do detail with an ask',
    note: 'Back, the to-do first (title, state line, chips, notes), the ask card, steps with suggestions, the composer with its addressee chip.',
    render: (ctx) => {
      const ui = ctx.ui;
      return ui.page({
        top: ui.navbar({ back: { go: 'iphone/list' }, trailing: [{ icon: 'bubble-left-right', go: 'iphone/conversation' }, { icon: 'ellipsis' }] }),
        body: kyotoDetail(ui),
        bottom: ui.composer({ placeholder: 'Tell Persona about this', to: 'Persona' }),
      });
    },
  });

  Kit.frame(V, 'iphone/conversation', {
    device: 'iphone', important: true,
    title: 'iPhone · Conversation with the keyboard',
    note: 'Pushed conversation with the to-do pinned in its header, folded tool steps, a delivery receipt, an Agent as addressee, the keyboard up.',
    chrome: { keyboard: true },
    render: (ctx) => {
      const ui = ctx.ui;
      return ui.convo({
        header: { back: { go: 'iphone/detail' }, title: 'Plan the Kyoto trip', state: 'needs', stateText: 'Which ryokan?', trailing: [{ icon: 'ellipsis' }] },
        messages: kyotoMessages(ui),
        composer: { text: 'Does Sowaka have a room with a garden view?', to: { label: '‘Hotel search’', icon: 'bed', agent: true } },
      });
    },
  });

  Kit.frame(V, 'iphone/add-sheet', {
    device: 'iphone',
    title: 'iPhone · Add sheet (medium detent)',
    note: 'A bottom sheet at the medium detent over the list: floating, inset, all corners rounded; scrim behind.',
    render: (ctx) => {
      const ui = ctx.ui;
      const sheet = ui.sheet({
        detent: 'medium',
        leading: [{ icon: 'xmark' }],
        title: 'New To-do',
        trailing: [{ icon: 'checkmark', look: 'prominent' }],
        content: `<div class="d-sheet-field">Renew passport<i class="k-caret"></i><small>Notes</small></div>
          <div style="margin-top:14px">${ui.chips([
            ui.chip('Fri', { icon: 'calendar', size: 'regular', tone: 'accent' }),
            ui.chip('Remind Me', { icon: 'bell', size: 'regular', look: 'neutral' }),
            ui.chip('Priority', { icon: 'flag', size: 'regular', look: 'neutral' }),
            ui.chip('Home', { icon: 'project', size: 'regular', look: 'neutral' }),
          ])}</div>
          <div style="margin-top:16px">${ui.card(`<div class="k-hstack" style="--gap:10px">${ui.face(28)}<div class="k-grow"><div class="k-secondary k-strong">Persona can help</div><div class="k-caption k-ink2">Fill in the form and book an appointment</div></div>${ui.btn({ label: 'Ask', look: 'tinted', size: 's' })}</div>`, { look: 'cell' })}</div>`,
      });
      return ui.page({
        top: ui.navbar({ leading: [{ icon: 'line-3-horizontal' }], trailing: [{ icon: 'magnifying-glass' }, { icon: 'ellipsis' }], large: 'To-dos', largeSub: '2 need you · 7 open' }),
        body: listBody(ui, { answers: false }),
        overlay: sheet,
      });
    },
  });

  /* ---------------------------------------------------------------- iPhone Duo */

  Kit.frame(V, 'duo-outer/detail', {
    device: 'duo-outer', important: true,
    title: 'Duo outer · Detail with the side column',
    note: 'System bars down the right edge: status, Back, two toolbar items (symbol over name), the Duo tab bar and the rail’s face and action at the foot.',
    render: (ctx) => {
      const ui = ctx.ui;
      return {
        content: ui.page({
          top: '<div class="d-side-title"></div>',
          body: ssoDetail(ui, { add: false }),
          bottom: ui.composer({ placeholder: 'Tell Persona about this', to: 'Persona', toPlacement: 'inside', plus: false }),
        }),
        side: {
          back: { go: 'iphone/list' },
          actions: [{ icon: 'bubble-left-right', label: 'Talk' }, { icon: 'ellipsis', label: 'More' }],
          tabs: { selected: 0 },
        },
      };
    },
  });

  Kit.frame(V, 'duo-inner-portrait/list-detail', {
    device: 'duo-inner-portrait', important: true,
    title: 'Duo open, upright · List | detail at the crease',
    note: 'Two panes split at the vertical crease (334.5 pt); horizontal bars; each pane keeps its own controls.',
    render: (ctx) => {
      const ui = ctx.ui;
      const r = rows(ui, { sel: 'release', answers: false, dense: true });
      const list = ui.page({
        top: ui.navbar({ leading: [{ icon: 'sidebar-left' }], trailing: [{ icon: 'plus' }], large: 'To-dos', largeSub: '2 need you · 7 open', below: `<div style="margin-top:10px">${filters(ui, { fade: true })}</div>` }),
        body: ui.list([ui.section('Needs you', { count: 2, look: 'needs', tone: 'needs' }), r.kyoto, r.release, ui.section('Working', { count: 2 }), r.sso, r.insurance, ui.section('Waiting', { count: 1 }), r.deposit, ui.section('To do', { count: 2 }), r.passport]),
      });
      const detail = ui.page({
        top: ui.navbar({ trailing: [{ icon: 'bubble-left-right' }, { icon: 'ellipsis' }] }),
        body: releaseDetail(ui, { notes: false, compactRuns: true }),
        bottom: ui.composer({ placeholder: 'Message', to: 'Persona', toPlacement: 'inside', plus: false }),
      });
      return { content: ui.split([{ w: 334, html: list }, { flex: 1, html: detail, line: true }]) };
    },
  });

  Kit.frame(V, 'duo-inner-landscape/detail-conversation', {
    device: 'duo-inner-landscape', important: true,
    title: 'Duo open, on its side · Detail | conversation',
    note: 'Side column on the right (Show Sidebar, toolbar items, tab bar); detail 467 | conversation 400; tabletop crease shown half-folded.',
    chrome: { crease: 'half' },
    render: (ctx) => {
      const ui = ctx.ui;
      const detail = ui.page({ top: '<div class="d-side-title"></div>', body: kyotoDetail(ui, { notes: false, askOpts: { other: '' } }) });
      const convo = ui.convo({
        header: { title: 'Conversation', sub: 'Persona and 2 helpers', trailing: [{ icon: 'xmark', size: 's' }] },
        messages: kyotoMessages(ui),
        composer: { placeholder: 'Message', to: 'Persona', toPlacement: 'inside', plus: false },
      });
      return {
        content: ui.split([{ w: 467, html: detail }, { flex: 1, html: convo, line: true }]),
        side: {
          back: { icon: 'sidebar-left', label: 'Sidebar' },
          actions: [{ icon: 'bubble-left-right', label: 'Talk', active: true }, { icon: 'ellipsis', label: 'More' }],
          tabs: { selected: 0 },
        },
      };
    },
  });

  /* ---------------------------------------------------------------- iPad */

  const sidebarTodos = (ui, sel) => [
    { icon: 'sun', label: 'Today', count: 3, selected: sel === 'today' },
    { icon: 'calendar', label: 'Upcoming', count: 4 },
    { icon: 'project', label: 'Kyoto trip', count: 1, tone: 'ready' },
    { icon: 'project', label: 'Weather App', count: 2 },
  ];

  Kit.frame(V, 'ipad11-landscape/sidebar-list-detail', {
    device: 'ipad11-landscape', important: true,
    title: 'iPad 11″ landscape · Sidebar 400 | list 320 | detail',
    note: 'The app sidebar with a disclosure under To-dos, the list column with its selection highlighted, the detail beside it.',
    render: (ctx) => {
      const ui = ctx.ui;
      const sb = ui.sidebar({
        selected: 'todos-sub', counts: { inbox: 3, todos: 2, machines: '2/3' },
        children: { todos: sidebarTodos(ui, 'today') },
      });
      const r = rows(ui, { sel: 'sso', answers: false, dense: true });
      const list = ui.page({
        top: ui.navbar({ title: 'Today', subtitle: '3 open', trailing: [{ icon: 'plus' }] }),
        body: `<div style="margin:2px 0 6px">${ui.search({ placeholder: 'Search To-dos', size: 's' })}</div>` + ui.list([ui.section('Needs you', { count: 1, look: 'needs', tone: 'needs' }), r.kyoto, ui.section('Working', { count: 1 }), r.sso, ui.section('To do', { count: 1 }), r.passport]),
      });
      const detail = ui.page({
        top: ui.navbar({ trailing: [{ icon: 'bubble-left-right' }, { icon: 'ellipsis' }] }),
        body: `<div class="k-readable">${ssoDetail(ui)}</div>`,
        bottom: `<div class="k-readable">${ui.composer({ placeholder: 'Tell Persona about this', to: 'Persona' })}</div>`,
      });
      return { content: ui.split([{ w: 400, html: sb }, { w: 320, html: list, line: true }, { flex: 1, html: detail, line: true }]) };
    },
  });

  Kit.frame(V, 'ipad11-portrait/detail-conversation', {
    device: 'ipad11-portrait',
    title: 'iPad 11″ portrait · Conversation as an overlay inspector',
    note: 'List hidden behind Show List; the conversation floats over the detail’s trailing side as a glass inspector (mid widths).',
    render: (ctx) => {
      const ui = ctx.ui;
      const convo = ui.convo({
        bg: 'transparent',
        header: { title: 'Conversation', sub: 'Persona and 2 helpers', trailing: [{ icon: 'xmark', size: 's' }] },
        messages: kyotoMessages(ui),
        composer: { placeholder: 'Message', to: 'Persona', toPlacement: 'inside', plus: false },
      });
      return ui.page({
        top: ui.navbar({ leading: [{ icon: 'sidebar-left' }, { icon: 'list-bullet', label: 'To-dos', look: 'glass' }], trailing: [{ icon: 'bubble-left-right', active: true }, { icon: 'ellipsis' }], cls: 'd-ipadp-nav' }),
        body: `<div style="width:394px">${kyotoDetail(ui, { more: true })}</div>`,
        pad: '0 24px',
        overlay: ui.inspector({ width: 392, overlay: true, content: convo }),
      });
    },
  });

  Kit.frame(V, 'ipad13-landscape/list-detail-conversation', {
    device: 'ipad13-landscape', important: true,
    title: 'iPad 13″ landscape · List | detail | conversation',
    note: 'Sidebar hidden while the conversation is open; iPadOS window controls; the instruction’s receipt and the steward relaying it.',
    chrome: { windowControls: true },
    render: (ctx) => {
      const ui = ctx.ui;
      const r = rows(ui, { sel: 'release', dense: true, answers: false });
      const list = ui.page({
        top: ui.navbar({ wc: true, leading: [{ icon: 'sidebar-left' }], trailing: [{ icon: 'plus' }], large: 'To-dos', largeSub: '2 need you · 7 open', below: `<div style="margin-top:10px">${filters(ui, { fade: true })}</div>` }),
        body: ui.list([ui.section('Needs you', { count: 2, look: 'needs', tone: 'needs' }), r.kyoto, r.release, ui.section('Working', { count: 2 }), r.sso, r.insurance, ui.section('Waiting', { count: 1 }), r.deposit, ui.section('To do', { count: 2 }), r.passport, r.gift]),
      });
      const detail = ui.page({
        top: ui.navbar({ trailing: [{ icon: 'bubble-left-right', active: true }, { icon: 'ellipsis' }] }),
        body: `<div class="k-readable">${releaseDetail(ui)}</div>`,
      });
      const convo = ui.convo({
        header: { title: 'Conversation', sub: 'Persona and 3 helpers', trailing: [{ icon: 'xmark', size: 's' }] },
        messages: releaseMessages(ui),
        composer: { placeholder: 'Message', to: 'Persona', toPlacement: 'above', plus: true, note: 'Persona relays it to the right helper' },
      });
      return { content: ui.split([{ w: 340, html: list }, { flex: 1, html: detail, line: true }, { w: 440, html: convo, line: true }]) };
    },
  });

  /* ---------------------------------------------------------------- catalog */

  Kit.frame(V, 'canvas/catalog', {
    device: 'canvas',
    title: 'Catalog · icons and components',
    note: 'Every icon name (Kit.icon) and the main Kit.ui components in one sheet, light and dark.',
    render: (ctx) => {
      const ui = ctx.ui;
      const icons = Kit.icon.names().filter((n) => n.indexOf('status-') !== 0)
        .map((n) => `<div class="d-icon">${Kit.icon(n, 22)}<code>${n}</code></div>`).join('');
      const box = (title, html) => `<div class="d-box">${ui.section(title)}${html}</div>`;
      const line = (html) => `<div class="d-line">${html}</div>`;
      const states = ['needs', 'working', 'waiting', 'ready', 'idle', 'done', 'stuck', 'later'];
      const col1 = box('States: mark, word, tone', states.map((st) => line(ui.state(st, { text: 'state text' }))).join(''))
        + box('Badges, chips, due, project, flag', line(`${ui.badge(2, { look: 'needs' })}${ui.badge(7)}${ui.badge(12, { look: 'plain' })}${ui.badge(3, { look: 'red' })}`)
          + line(`${ui.chip('Needs You', { tone: 'needs' })}${ui.chip('Working', { tone: 'working', dot: 'working' })}${ui.chip('Neutral', { icon: 'calendar' })}${ui.chip('Outline', { look: 'outline', icon: 'plus' })}${ui.chip('Solid', { look: 'solid', tone: 'accent' })}`)
          + line(`${ui.due('Fri')}${ui.due('Overdue · Tue', { overdue: true })}${ui.project('Kyoto trip', { hue: 'teal' })}${ui.project('Weather App')}${ui.flag()}`));
      const col1b = box('Buttons (labels never wrap)', line(`${ui.btn({ icon: 'chevron-left' })}${ui.btn({ icon: 'plus', label: 'New To-do' })}${ui.btnGroup([{ icon: 'bubble-left-right' }, { icon: 'ellipsis' }])}`)
          + line(`${ui.btn({ label: 'Approve', look: 'prominent', size: 's' })}${ui.btn({ label: 'Review', look: 'tinted', size: 's' })}${ui.btn({ label: 'Later', look: 'fill', size: 's' })}${ui.btn({ label: 'Plain', look: 'plain', size: 's' })}${ui.btn({ icon: 'trash', label: 'Delete', look: 'danger', size: 's' })}`)
          + `<div style="margin-top:10px">${ui.capsules([{ label: 'Gion Hatanaka', recommended: true }, { label: 'Sowaka' }, { label: 'Hoshinoya' }, { label: 'Not now', quiet: true }])}</div>`);
      const col2 = box('Bars and fields', `${line(ui.segmented(['List', 'Calendar'], 0))}<div style="margin:10px 0">${ui.filterBar([{ label: 'All', count: 7 }, { label: 'Needs You', count: 2, look: 'needs' }, { label: 'Working', count: 2 }, { label: 'Done' }])}</div>${ui.search({ placeholder: 'Search To-dos', mic: true })}`)
        + box('Addressee and composer', `${line(`${ui.addressee('Persona')}${ui.addressee({ label: '‘Hotel search’', icon: 'bed', agent: true, clear: true })}`)}<div style="margin-top:10px">${ui.composer({ placeholder: 'Tell Persona about this', to: 'Persona', toPlacement: 'inside', plus: false })}</div><div style="margin-top:12px">${ui.composer({ text: 'Also add the widget to 1.3', to: { label: '‘Release plan’', icon: 'clipboard-list', agent: true }, note: 'Working · it will read this after the current step' })}</div>`)
        + box('Rows', ui.list([
          ui.row({ title: 'Plan the Kyoto trip', state: 'needs', stateText: 'Which ryokan?', meta: [ui.due('Mar 1'), ui.project('Kyoto trip', { hue: 'teal' })], answers: [{ label: 'Gion Hatanaka', recommended: true }, { label: 'Sowaka' }] }),
          ui.row({ title: 'Renew passport', priority: true, meta: [ui.due('Fri')], trailing: '2h', chevron: true }),
          ui.row({ title: 'Water the balcony plants', check: 'done', meta: [ui.due('Yesterday')] }),
        ]));
      const col3 = box('Ask card', kyotoAsk(ui, { compact: true, detail: '', other: '' }))
        + box('Run, deliverable, round', `<div class="k-stack" style="--gap:10px">${ui.run({ role: 'Hotel search', where: 'Persona', state: 'working', latest: 'Checking availability for Apr 3–9.', icon: 'bed', hue: 'teal' })}${ui.deliverable({ kind: 'pr', title: 'PR #128 · Refresh SSO sessions early', outcome: 'Merged', sub: 'All checks passed' })}${line(`${ui.deliverable({ look: 'chip', kind: 'booking', title: 'Gion Hatanaka', outcome: 'Booked' })}${ui.deliverable({ look: 'chip', kind: 'doc', title: 'Packing list' })}`)}${ui.round({ instruction: 'Only places with a private onsen.', receipt: 'Sent to ‘Hotel search’ · Delivered', summary: 'Worked 6 min · searched 14 ryokans', result: 'Three fit the budget.', resultState: 'needs' })}</div>`)
        + box('Messages', `<div class="k-stack" style="--gap:10px">${ui.msg.persona('Persona’s reply sits in its box.')}${ui.msg.tools('4 steps')}${ui.msg.receipt('Waiting for your approval', { state: 'needs' })}</div>`);
      return `<div class="d-cat"><h2>Icons · Kit.icon(name, size)</h2><div class="d-icons">${icons}</div>
        <div class="d-cols"><div>${col1}${col1b}</div><div>${col2}</div><div>${col3}</div></div></div>`;
    },
  });

  /* ---------------------------------------------------------------- web, Android */

  Kit.frame(V, 'web/todos', {
    device: 'web',
    title: 'Web 1440 · Sidebar 284 | list | detail',
    note: 'The web app’s smaller desktop scale in a minimal browser window; the same components.',
    chrome: { browser: true, url: 'persona.example.com/todos?id=kyoto' },
    render: (ctx) => {
      const ui = ctx.ui;
      const sb = ui.sidebar({ selected: 'todos-sub', counts: { inbox: 3, todos: 2, machines: '2/3' }, children: { todos: sidebarTodos(ui) }, kbd: '⌘K' });
      const r = rows(ui, { sel: 'kyoto' });
      const list = ui.page({
        top: ui.navbar({ title: 'To-dos', align: 'leading', trailing: [{ icon: 'plus', label: 'New To-do', look: 'fill', size: 's' }] }),
        body: `<div style="margin:0 0 8px">${ui.search({ placeholder: 'Search to-dos', size: 's' })}</div><div style="margin-bottom:4px">${filters(ui, { fade: true })}</div>` + listBody(ui, { sel: 'kyoto' }),
      });
      const detail = ui.page({
        top: ui.navbar({ trailing: [{ icon: 'bubble-left-right', label: 'Conversation', look: 'fill', size: 's' }, { icon: 'ellipsis' }] }),
        body: `<div style="max-width:640px;margin:0 auto">${kyotoDetail(ui, { more: true })}</div>`,
        bottom: `<div style="max-width:640px;margin:0 auto">${ui.composer({ placeholder: 'Tell Persona about this', to: 'Persona', toPlacement: 'inside' })}</div>`,
      });
      return { content: ui.split([{ w: 284, html: sb }, { w: 400, html: list, line: true }, { flex: 1, html: detail, line: true }]) };
    },
  });

  Kit.frame(V, 'android-fold/list-detail', {
    device: 'android-fold',
    title: 'Android foldable · List | detail at the hinge',
    note: 'Secondary: the Android status and gesture bars and the hinge; panes kept off the hinge.',
    render: (ctx) => {
      const ui = ctx.ui;
      const r = rows(ui, { sel: 'kyoto', answers: false, dense: true });
      const list = ui.page({
        top: ui.navbar({ leading: [{ icon: 'line-3-horizontal' }], large: 'To-dos', largeSub: '2 need you · 7 open' }),
        body: ui.list([ui.section('Needs you', { count: 2, look: 'needs', tone: 'needs' }), r.kyoto, r.release, ui.section('Working', { count: 2 }), r.sso, r.insurance, ui.section('To do', { count: 2 }), r.passport, r.gift]),
      });
      const detail = ui.page({
        top: ui.navbar({ trailing: [{ icon: 'bubble-left-right' }, { icon: 'ellipsis' }] }),
        body: kyotoDetail(ui, { notes: false, askOpts: { detail: '', other: '' } }),
        bottom: ui.composer({ placeholder: 'Message', to: 'Persona', toPlacement: 'inside', plus: false }),
      });
      return { content: ui.split([{ w: 336, html: list }, { flex: 1, html: detail, line: true }]) };
    },
  });
})();
