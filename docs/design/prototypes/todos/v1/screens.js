/*
 * V1 · Paper and Pane (synthesis §3.1; the recommended direction of §2).
 *
 * A to-do is one page, the paper. A quick note goes out from the composer docked at its foot. When the owner wants to
 * talk, the Conversation arrives on the right: a pane at regular widths (the leftmost region steps aside in the same
 * transition), a push with the to-do pinned as its header on phones, a cross-fade on the iPhone Duo.
 *
 * Every word and number comes from window.Data (data/todos.js) through the shared components (kit/components.js); this
 * file never types a count. Prototype states are drawn from Data.view(…), so answering a request drops N everywhere.
 *
 * Frame ids follow the spec (V1-01 … V1-18) with the kit's device ids ('web', 'android-fold') and '-' where the spec
 * writes '+' (frame ids take letters, digits and / _ . -). A state is '<frame>~<state>'. The walk is in v1/notes.md.
 */
(function () {
  const V = 'V1';
  const ui = Kit.ui;
  const I = (n, s) => Kit.icon(n, s);
  const A = (n) => Kit.navAttrs(n);
  const N = (go, transition) => ({ go, transition });
  const T = (text) => ({ toast: text });
  const PUSH = 'push';
  const POP = 'pop';
  const FADE = 'fade';
  const NONE = 'none';
  const OPEN = 'pane-open';
  const CLOSE = 'pane-close';

  Kit.variant(V, {
    name: 'Paper and Pane',
    order: 1,
    summary: 'The to-do is a page (the paper) with a composer at its foot; the Conversation arrives on the right as a pane at regular widths, or is pushed with the to-do pinned on phones. At most three regions show; the leftmost steps aside. Recommended (synthesis §2, §3.1).',
  });

  /* ================================================================== data views */

  // Draw a frame over Data.view(state): the shared adapters read window.Data, so swap it for the render only.
  function withView(state, fn) {
    const base = window.Data;
    if (!state) return fn(base);
    const v = base.view(state);
    window.Data = v;
    try { return fn(v); } finally { window.Data = base; }
  }

  function frame(id, def) {
    const draw = def.render;
    Kit.frame(V, id, Object.assign({}, def, { render: (ctx) => withView(def.view, (d) => draw(ctx, d)) }));
  }

  const D0 = window.Data; // the unchanged data set (rows that keep their places until the list is idle)

  // A list scrolled to a section (iPad 13″'s summary lines): after a frame is drawn, the list's body scrolls to the mark,
  // and the browser clamps it at the list's end as a real scroll view does. Only V1's own marks are touched.
  if (!Kit.v1ScrollHook) {
    const after = Kit.afterRender;
    Kit.v1ScrollHook = true;
    Kit.afterRender = function afterRender(root) {
      const out = after.apply(this, arguments);
      (root || document).querySelectorAll('.v1-scroll-mark').forEach((m) => {
        const body = m.closest('.k-body');
        if (body) body.scrollTop = Math.max(0, m.offsetTop - 2);
      });
      return out;
    };
  }
  const ANSWERED = (opt) => ({ answered: { a1: opt || 'hanami' } });

  /** The place summary with its lines in the sections' order (Data.sectionDefs), Can wait last, so the summary reads in
      the same order as the list beside it (Data.summary() uses that order too; sorting keeps V1 independent of it), and
      says "1 needs you" in the singular. */
  function placeSummary(d, place, o) {
    const order = d.sectionDefs.map((x) => x.id).filter((id) => id !== 'can_wait').concat(['can_wait']);
    const lines = (p) => d.summary(p).slice().sort((a, b) => order.indexOf(a.id) - order.indexOf(b.id))
      .map((l) => (l.id === 'needs' && l.count === 1 ? Object.assign({}, l, { label: 'needs you' }) : l));
    const view = Object.assign(Object.create(d), { summary: lines });
    const keep = window.Data;
    window.Data = view;
    let html;
    try { html = ui.placeSummaryFor(place, o); } finally { window.Data = keep; }
    if (place !== 'all') return html;
    // The sentence adds up as it is read: “12 open: 11 in Kyoto trip, Weather App and Home, 1 without a project”.
    const open = d.countsOf('all').open;
    const loose = d.counts.withoutProject;
    const names = d.projects.map((x) => x.name);
    const sub = `${open} open: ${open - loose} in ${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}${loose ? `, <span class="v1-nw">${loose} without a project</span>` : ''}`;
    return html.replace(/<div class="k-psum-sub">[^<]*<\/div>/, `<div class="k-psum-sub">${sub}</div>`);
  }
  const optionOf = (d, id) => d.ask('a1').options.find((o) => o.id === id);
  /** "2 need you" / "1 needs you": the Fleet header and anything else that counts people's attention. */
  const needYou = (n) => `${n} ${n === 1 ? 'needs' : 'need'} you`;

  /* ================================================================== the composer */

  // The placeholder fits the room the chip leaves: the full sentence on the wide papers (iPad 13″, the web); a short
  // "Message…" on phones, the Duo and in every pane, where the chip, mic and send leave about 170 pt and the sentence
  // would be cut (synthesis §3.1 draws "Message…" there too).
  const SHORT = 'Message…';
  const LONG = 'Message about this to-do…';
  /** ui.composerFor with the fitted placeholder. Typed text wraps and the field grows to 3 lines (as Messages does), with
      the chip, mic and send on the last line; an answer chip stands above the field while there is text, so the draft keeps
      the width. */
  function composer(t, o) {
    o = Object.assign({ placeholder: SHORT }, o || {});
    // a wide chip (“Answer: Which ryokan? ×”, “To Release lead ▾”, a run) stands above the field while there is text
    const to = o.to || (t.composer && t.composer.to);
    if (o.text && to && typeof to === 'object' && (to.answer || to.menu || to.run)) o.label = 'above';
    let html = ui.composerFor(t, o);
    if (o.focused && !o.text) html = html.replace('<span class="k-tc-f">', '<span class="k-tc-f v1-tc-focus"><i class="k-caret"></i>');
    return html;
  }

  /* ================================================================== the section bar (E2) */

  /**
   * The section bar: the Inbox's chips with the landing choice first (All · Needs you · In progress · To do · Done; the
   * owner's 11-18 decision). Done shows only while it has rows; the grouping symbol sits inside the capsule's trailing
   * end (Group By: Phase · Project · Due Date). Landing on All leaves the strip at its start, so All and Needs you with
   * its blue N show in every column. The other chips scroll and fade at their cut edge; a chip the owner picked is
   * scrolled into view as the Inbox's bar does.
   * o: { place, filter, go: { <chip id>: nav }, counts: { <chip id>: n } (a hub count that moved before the rows re-file),
   *      group: nav (the grouping symbol), compact (a column under 400 pt: In progress and To do drop their neutral
   *      counts, so more chips fit; Needs you keeps its blue N), web }
   */
  function sectionBar(d, o) {
    o = o || {};
    const place = o.place || 'all';
    const sel = o.filter || 'all';
    const goes = o.go || {};
    const items = d.bar(place).filter((x) => !(x.id === 'done' && x.empty)).map((x) => Object.assign({}, x,
      o.counts && o.counts[x.id] != null ? { count: o.counts[x.id] } : {},
      o.compact && x.look !== 'needs' ? { count: null } : {},
      goes[x.id] || T(`Shows ${x.label}`)));
    // All comes first, the landing choice at the strip's start (the Inbox's rule: its landing choice, Needs you, is its
    // first), so Needs you and its count sit beside it in any column; the trailing end holds only the grouping control.
    const at = items.findIndex((x) => x.id === sel);
    const group = `<span class="v1-group"${A(o.group || T('Opens: Group By (Phase · Project · Due Date)'))} title="Group By">${I('line-3-decrease', o.web ? 15 : 19)}</span>`;
    const html = ui.sectionBar(items, { selected: at, trailing: group });
    return `<div class="v1-bar${o.compact ? ' is-compact' : ''}">${html}</div>`;
  }

  /* ================================================================== small pieces */

  const backCapsule = (label, nav) => ui.btn(Object.assign({ icon: 'chevron-left', label, title: 'Back' }, nav));
  const convBtn = (nav, o) => Object.assign({ icon: 'bubble-left-right', title: 'Conversation' }, o || {}, nav || {});
  const moreBtn = (nav) => Object.assign({ icon: 'ellipsis', title: 'More' }, nav || T('Opens: More'));
  const anchored = (html, pos, cls) => `<div class="v1-anchor${cls ? ' ' + cls : ''}" style="${pos}">${html}</div>`;
  const tap = (html, nav) => (nav ? `<div class="v1-tap"${A(nav)}>${html}</div>` : html);
  const hr = () => '<div class="v1-hr"></div>';

  /** An iOS 26 alert: title, message, two capsule buttons. */
  function alertBox(o) {
    return `<div class="k-scrim"${A(o.scrimNav)}></div><div class="v1-alert">
      <div class="v1-alert-t">${o.title}</div>${o.body ? `<div class="v1-alert-b">${o.body}</div>` : ''}
      <div class="v1-alert-acts">${o.actions.map((a) => `<span class="v1-alert-a${a.danger ? ' is-danger' : ''}${a.bold ? ' is-bold' : ''}"${A(a.nav)}>${a.label}</span>`).join('')}</div>
    </div>`;
  }

  // Rounds as the paper's block shows them: an older round by its weekday, today's by its time.
  const dayShort = (r) => (r.day && r.day !== 'Today' ? r.day.split(' ')[0] : r.time);

  /** One round on the paper (kit classes, as ui.conversationBlock draws them): the owner's words, then the message
      state, then the folded lines. r: { who, quote, text, status, tone, subs: [HTML], extra: [HTML] } */
  function cbRound(r, nav) {
    const q = r.quote != null ? `“${r.quote}”` : r.text || '';
    return `<div class="k-cb-r"${A(nav)}>
      <div class="k-cb-you">${r.who ? `<span class="k-cb-who">${r.who}</span>` : ''}<span class="k-cb-q">${q}</span>${r.when ? `<span class="k-cb-who v1-cb-when">${r.when}</span>` : ''}</div>
      ${r.status ? `<div class="k-cb-st k-rd-st--${r.tone || 'plain'}">${r.status}</div>` : ''}
      ${(r.subs || []).map((s) => `<div class="k-cb-sub"><i>└</i><span>${s}</span></div>`).join('')}
      ${(r.extra || []).map((x) => `<div class="v1-cb-x">${x}</div>`).join('')}
    </div>`;
  }
  const cbFromData = (r) => ({ who: `You · ${dayShort(r)}`, quote: r.text, subs: r.fold ? [r.fold] : [] });
  function cbBlock(rows, o) {
    return `<div class="k-cb"><div class="k-cb-head"><span>Conversation</span><span class="k-cb-all"${A(o.all)}>All ${o.total} ›</span></div>${rows.join('')}</div>`;
  }

  const statusTone = (s) => (/^Sending/.test(s) ? 'faint' : /^Not delivered/.test(s) ? 'danger' : /^Delivery unknown/.test(s) ? 'caution' : 'plain');
  /** ui.askCardFor with its price line breaking only between options (“Hanami ¥58k · Kaede ¥52k · Tsuki ¥59k a night”). */
  const askCardFor = (ask, o) => ui.askCardFor(ask, o).replace(/<div class="k-ac-prices">([^<]*)<\/div>/, (m, txt) => `<div class="k-ac-prices">${keepParts(txt)}</div>`);
  /** A state sentence whose “· part”s never break inside: “Widget’s plan needs your OK · 1 working · 1 done”. */
  const keepParts = (text) => {
    const parts = String(text || '').split(' · ');
    const last = parts.length - 1;
    return parts.map((s, i) => (i === 0 ? s : `<span class="v1-nw">${s}</span>`) + (i < last ? '&nbsp;·' : '')).join(' ');
  };

  /* ================================================================== the list */

  /**
   * The sections of a place (Data.sections), drawn with the shared TodoRow, SectionHeader and fold rows.
   * o: { place, filter, sel, line3 (phones), go(t) → nav, ans(t) → answer nav, tick(t) → nav, foldNav: { can_wait, someday },
   *      open: { can_wait }, answered: { <todo id>: label }, undo, optionsOpen: { id, html }, swiped, ticked, saving,
   *      startAt (a section id: the list scrolled to it), lit (a section id), counts: { <section id>: n } (a hub count
   *      that moved before the row re-files) }
   */
  function sectionsHtml(d, o) {
    const place = o.place || 'all';
    const calm = d.isCalm(place);
    let started = !o.startAt;
    const out = [];
    d.sections(place, o.filter || 'all').forEach((s) => {
      if (!started && s.id !== o.startAt) return;
      started = true;
      const rows = s.todos.map((t) => rowHtml(d, t, o, calm));
      if (s.fold) {
        const open = !!(o.open && o.open[s.id]);
        out.push(ui.foldRow(s.title, Object.assign({ count: s.count, open }, (o.foldNav || {})[s.id] || T(`Opens: ${s.title}`))));
        if (open) out.push(ui.todoList(rows, { cls: 'v1-folded-rows' }));
        return;
      }
      // scrollTo: the list is scrolled so this section's header is at the top, as far as the list's end allows
      if (o.scrollTo === s.id) out.push('<i class="v1-scroll-mark"></i>');
      if (!calm) {
        const count = o.counts && o.counts[s.id] != null ? o.counts[s.id] : s.count;
        out.push(ui.sectionHeader(s.title, { count, look: s.look, lit: o.lit === s.id }));
      }
      out.push(ui.todoList(rows));
    });
    return out.join('');
  }

  function rowHtml(d, t0, o, calm) {
    // swap: draw a row from a changed view while it keeps its place (answered, not yet re-filed)
    const t = (o.swap && o.swap[t0.id]) || t0;
    const p = { selected: o.sel === t.id };
    if (!o.line3 || calm) p.line3 = null;
    else if (t.line3 && t.project && t.project === (o.place || 'all')) {
      // line 3 holds facts the grouping doesn't show: inside a project's place, not the project's name (§2.4)
      const pj = d.project(t.project);
      p.line3 = pj ? t.line3.replace(`${pj.name} · `, '').replace(pj.name, '') || null : t.line3;
    }
    Object.assign(p, o.go ? o.go(t) : {});
    if (t.answer) p.answer = Object.assign({}, t.answer, o.ans ? o.ans(t) : {});
    // drawn once: while a pane shows the request's home, its door on this screen keeps line 2 and loses its button
    if (o.pointer && o.pointer[t.id]) p.answer = null;
    if (o.answered && o.answered[t.id]) { p.answered = o.answered[t.id]; p.undo = o.undo; }
    if (o.optionsOpen && o.optionsOpen.id === t.id) {
      // Options ⌃: the answer line keeps only the fold control, the card below carries every answer (one primary on screen);
      // line 3 stays the row's last line, under the card
      p.answer = null;
      const up = `<div class="k-tr-ans"><span class="k-ans-opt is-open"${A(o.optionsOpen.close)}><span>Options</span>${I('chevron-up', 11)}</span></div>`;
      const l3 = p.line3 !== undefined ? p.line3 : t.line3;
      p.below = `${up}<div class="v1-below">${o.optionsOpen.html}</div>${l3 ? `<div class="k-tr-l3 v1-l3-after">${l3}</div>` : ''}`;
      p.line3 = null;
    }
    if (o.swiped === t.id) p.swiped = true;
    if (o.ticked === t.id) { p.ticked = true; p.lead = 'done'; }
    if (o.saving === t.id) p.lineHtml = `<span class="v1-saving">${I('arrow-clockwise', 12)}<span>Saving…</span></span>`;
    if (o.tick) p.tickNav = o.tick(t);
    return ui.todoRowFor(t, p);
  }

  /** The list column (a pane at regular widths): leading-aligned title menu, + and ⋯, the section bar, the sections.
      o.android: the Material top app bar (☰, title, +, ⋮) over the same bar and rows. */
  function listPane(d, o) {
    const place = o.place || 'all';
    const p = d.place(place);
    const calm = d.isCalm(place);
    const empty = !d.sections(place, 'all').length;
    const lead = o.menuBtn ? [Object.assign({ icon: 'line-3-horizontal', title: 'Show Sidebar' }, o.menuBtn)] : [];
    // every list column is under 400 pt (320 to 360): the bar's neutral counts give way so more chips show
    const bar = calm || empty ? '' : sectionBar(d, { place, filter: o.filter, go: o.barGo, counts: o.barCounts, compact: true });
    const titleNav = o.titleNav || T('Opens: the places menu (All To-dos, Today, Upcoming, projects)');
    const top = o.android
      ? `<div class="v1-alist">${abar({ lead: o.menuBtn ? 'line-3-horizontal' : null, leadNav: o.menuBtn, title: `<span class="v1-abar-menu"${A(titleNav)}>${p.title}${I('chevron-down', 13)}</span>`, trail: [{ icon: 'plus', nav: T('Opens: New To-Do') }, { icon: 'ellipsis', cls: 'is-vert', nav: T('Opens: Sort By, Show Someday, Show Done Today') }] })}${bar}</div>`
      : ui.navbar({
        align: 'leading', wc: o.wc,
        leading: lead,
        center: ui.titleMenu(Object.assign({ title: p.title }, titleNav)),
        trailing: [{ icon: 'plus', title: 'New To-Do', toast: 'Opens: New To-Do' }, moreBtn(T('Opens: Sort By, Show Someday, Show Done Today'))],
        below: bar, fog: o.fog,
      });
    // Drawn once (§2.7): the selected row's paper is beside the list (or under it), and the paper holds the request's
    // home or door, so the row keeps its line 2 and drops its buttons.
    const rows = Object.assign({}, o, { pointer: o.pointer || (o.sel ? { [o.sel]: true } : null) });
    const body = empty ? placeEmpty(d, place, { link: o.emptyLink }) : sectionsHtml(d, rows);
    return ui.page({ top, body, overlay: o.overlay || '' });
  }

  /** A place's empty state. o.link: false where the place summary beside the list already links what comes next (one
      home per link on screen). */
  function placeEmpty(d, place, o) {
    const p = d.place(place);
    if (place === 'today') {
      const link = o && o.link === false ? null : { label: `${d.thisWeek()} coming up this week`, look: 'plain', trailingIcon: 'chevron-right', toast: 'Opens: Upcoming' };
      return `<div class="v1-empty">${ui.empty({ icon: 'sun-fill', title: p.empty.title, action: link })}</div>`;
    }
    return `<div class="v1-empty">${ui.empty({ icon: 'checklist', title: p.empty.title, body: p.empty.body || '', action: p.empty.action ? { label: p.empty.action, icon: 'plus', toast: 'Opens: New To-Do' } : null })}</div>`;
  }

  /** The sidebar tree with every row wired: places in this family's frames, the rest to toasts. */
  function tree(d, o) {
    const go = Object.assign({
      inbox: T('Opens: Inbox'), upcoming: T('Opens: Upcoming'), weather: T('Opens: Weather App'), home: T('Opens: Home'),
      projects: T('Opens: All Projects'), scheduled: T('Opens: Scheduled Tasks'), knowledge: T('Opens: Knowledge'),
      machines: T('Opens: Machines'), chats: T('Opens: Persona Chats'), persona: T('Opens: Persona, the main chat'),
      'release-lead': T('Opens: Release lead, a Pinned direction'), agents: T('Opens: Agents'),
      today: T('Opens: Today'), kyoto: T('Opens: Kyoto trip'), todos: T('Opens: All To-dos'),
    }, o.go || {});
    // Agents carries its count of sessions that need the owner, the same number as the web rail and the Duo tab bar
    const agents = `<span class="k-sbr-label"><span>Agents</span></span>`;
    return ui.sidebarTree({ selected: o.selected || 'todos', go, wc: o.wc, width: o.width })
      .replace(agents, `${agents}${d.counts.agentsBadge ? `<span class="k-sbr-count">${d.counts.agentsBadge}</span>` : ''}`);
  }

  /* ================================================================== the paper */

  /**
   * The paper's body, top to bottom (synthesis §2.5): tick + title, state line, Since you looked, the ask card (home,
   * door, pointer, answered or later), chips, notes or the brief, scope, Persona can help, Steps, Results, the
   * Conversation block (or its pointer), Work details.
   * o: { state ({ phase, word, text } | null), recap (false), recapNav, ask ('pointer' | 'later' | 'none' | { answered, undo }),
   *      askOn, narrow, compactAsk, pointerNav, chips (false), scope (HTML), help (helpCard options), steps (list),
   *      stepNav(s), stepsMode ('summary' | 'none'), stepsNav, summaryResults, compactSteps, suggested, keep, edit,
   *      results (false), conv ('pointer' | 'none' | HTML), extra (rounds), allNav, roundNav, convCount, work ('open' | false),
   *      workNav, workRows(rows), notes (false) }
   */
  function paperBody(d, t, o) {
    o = o || {};
    const p = [];
    p.push(ui.paperTitle({ title: t.title, lead: t.closed ? 'done' : 'tick' }));
    const st = o.state !== undefined ? o.state : t.state && t.state.text ? { phase: t.phase, word: t.state.word, text: t.state.text } : null;
    if (st) p.push(ui.stateLine(st.phase, keepParts(st.text), { word: st.word }));
    if (t.recap && o.recap !== false) p.push(ui.recapLine(t.recap, o.recapNav || T('Opens: the round that changed')));

    if (o.ask !== 'none') {
      const askId = (t.asks || [])[0];
      if (o.ask && o.ask.answered) p.push(ui.askCard({ answered: o.ask.answered, undo: o.ask.undo }));
      else if (askId) {
        const a = d.ask(askId);
        if (o.ask === 'later') p.push(askCardFor(a, { later: true, on: o.askOn }));
        else if (o.ask === 'pointer') p.push(ui.askPointer(a.pointer, o.pointerNav));
        else p.push(askCardFor(a, { as: a.home === 'run' ? 'door' : 'home', on: o.askOn || {}, narrow: o.narrow, compact: o.compactAsk }));
      }
    }

    if (o.chips !== false) p.push(chipsRow(t));
    if (t.notes && o.notes !== false) p.push(ui.notes(t.notes));
    else if (!t.brief && o.notes !== false) p.push(ui.notes(''));
    if (t.brief && o.brief !== false) p.push(ui.briefLineFor(t, o.briefNav || T('Opens: What you asked and Done when')));
    if (o.scope) p.push(o.scope);
    if (o.help) p.push(ui.helpCard(o.help));

    const steps = (o.steps || t.steps || []).map((s) => Object.assign({}, s, o.stepNav ? o.stepNav(s) : T(`Opens: ${s.text}`)));
    const done = steps.filter((s) => s.state === 'done').length;
    if (steps.length && o.stepsMode === 'summary') {
      p.push(ui.stepsSummary({ steps }, Object.assign({ results: o.summaryResults }, o.stepsNav || T('Opens: Steps'))));
    } else if (steps.length && o.stepsMode !== 'none') {
      p.push(ui.stepsList(steps, { count: `${done} of ${steps.length}`, compact: o.compactSteps }));
    } else if (o.suggested) {
      p.push(ui.stepsList([], { count: 'Suggested', suggested: o.suggested, keep: o.keep, edit: o.edit }));
    }

    if (t.results && t.results.length && o.results !== false) {
      p.push(ui.resultsStrip(t.results.map((r) => Object.assign({}, r, T(`Opens: ${r.label} in the link card`)))));
    }

    if (o.conv === 'pointer') p.push(ui.conversationBlock({ pointer: true, pointerNav: o.pointerConvNav }));
    else if (typeof o.conv === 'string' && o.conv !== 'none') p.push(o.conv);
    else if (o.conv !== 'none') {
      const rounds = (t.rounds || []).concat(o.extra || []).filter((r) => r.kind !== 'background');
      if (rounds.length) {
        const rows = rounds.slice(-(o.convCount || 2)).map((r) => cbRound(r.cb || cbFromData(r), o.roundNav));
        p.push(cbBlock(rows, { all: o.allNav, total: (t.allCount || rounds.length) + (o.extra || []).length }));
      }
    }

    if (t.workDetails && o.work !== false) {
      const rows = (o.workRows ? o.workRows(t.workDetails.rows) : t.workDetails.rows);
      p.push(workDetails(Object.assign({ summary: t.workDetails.summary, rows, open: o.work === 'open' }, o.workNav || {})));
    }
    return `<div class="v1-paper${o.cls ? ' ' + o.cls : ''}">${p.join('')}</div>`;
  }

  /** Work details (ui.workDetails' look): its rows put every mark in one 20-pt column, 8 pt before the text, and a row
      may take two lines rather than cut its last words. */
  function workDetails(o) {
    const rows = o.open ? `<div class="k-wd-rows">${(o.rows || []).map((r) => `<div class="k-wd-r v1-wd-r"${A(r)}><span class="v1-wd-ic">${r.mark ? ui.lineMark(r.mark) : r.icon ? I(r.icon, 14) : ''}</span><span class="v1-wd-t">${r.text}</span>${r.action ? `<span class="k-wd-a">${r.action}</span>` : r.run ? I('chevron-right', 11) : ''}</div>`).join('')}</div>` : '';
    return `<div class="k-wd${o.open ? ' is-open' : ''}"><div class="k-wd-line"${A(o)}><b>Work details</b>${o.summary ? `<span class="k-sep">·</span><span>${o.summary}</span>` : ''}${I(o.open ? 'chevron-down' : 'chevron-right', 11)}</div>${rows}</div>`;
  }

  /** The to-do's chips (date, project) and “+”; each opens its picker. */
  const chipsRow = (t) => ui.todoChips((t.chips || []).map((c) => Object.assign({}, c, T(c.icon === 'calendar' ? 'Opens: Date and Reminder' : `Opens: ${c.label}`))),
    { add: t.closed ? false : T('Opens: Add Date, Reminder, Priority or Project') });

  /** The paper as a page in a column or on a phone: bar, body (optionally scrolled), the docked composer. */
  function paperPage(d, t, o) {
    o = o || {};
    const trailing = [];
    // convHint: the keyboard shortcut beside the name on the web (“Conversation ⌘B”)
    if (o.conv !== false) trailing.push(o.convLabel ? convBtn(o.convNav, { label: `Conversation${o.convHint ? ` <span class="v1-kbd">${o.convHint}</span>` : ''}`, title: `Conversation${o.convHint ? ` (${o.convHint})` : ''}`, look: 'glass', size: o.convSize, active: o.convActive, dot: o.dot }) : convBtn(o.convNav, { dot: o.dot, active: o.convActive }));
    trailing.push(moreBtn(o.moreNav));
    // An inline title ellipsizes 8 pt clear of the trailing glass group (the kit's centre column would grow into it).
    const title = o.title ? `<span class="v1-bar-t">${o.title}</span>` : '';
    const top = o.top != null ? o.top : ui.navbar({ leading: o.leading || [], title, trailing, fog: true, wc: o.wc, cls: o.navCls });
    const body = o.offset === 'end'
      ? `<div class="v1-scroll v1-end">${paperBody(d, t, o.paper)}</div>`
      : `<div class="v1-scroll${o.readable ? ' v1-readable' : ''}" style="margin-top:${-(o.offset || 0)}px">${paperBody(d, t, o.paper)}</div>`;
    let bottom = '';
    if (o.composer !== false) {
      const comp = composer(t, Object.assign({ send: T('Sends the message') }, o.composer || {}));
      bottom = tap(o.readable ? `<div class="v1-readable">${comp}</div>` : comp, o.composerNav);
    }
    return ui.page({ top, body, bottom, overlay: o.overlay || '', pad: o.pad, cls: o.cls });
  }

  /* ================================================================== conversations */

  /** A to-do's rounds with run and background lines wired (they unfold in place). */
  function wiredRounds(rounds, o) {
    return rounds.map((r) => {
      if (r.kind === 'background' || !r.items) return r;
      return Object.assign({}, r, { items: r.items.map((it) => (it.kind === 'run' && o.runNav ? Object.assign({}, it, o.runNav) : it)) });
    });
  }

  /** A thread (rounds with day separators, latest at the bottom), as ui.thread draws it, plus an unfolded Background
      round's detail line. o: ui.convRound options, per: { <round id>: options }, bgOpen, bgDetail */
  function threadHtml(rounds, o) {
    o = o || {};
    let day = null;
    const out = [];
    rounds.forEach((r) => {
      if (r.day && r.day !== day) { out.push(ui.rline.day(r.day)); day = r.day; }
      out.push(ui.convRound(r, Object.assign({}, o, (o.per || {})[r.id])));
      if (r.kind === 'background' && o.bgOpen && o.bgDetail) out.push(`<div class="v1-bg-open">${o.bgDetail}</div>`);
    });
    return `<div class="k-thread is-bottom">${out.join('')}${o.tail || ''}</div>`;
  }

  /** The steward's conversation or a run's session as a pane: PaneHeader, the thread, the composer. */
  function stewardPane(d, t, o) {
    o = o || {};
    const rounds = wiredRounds((t.rounds || []).concat(o.extra || []), o);
    const head = ui.paneHeader({
      channel: o.channel || t.steward, menu: !!(t.channels && t.channels.length), more: o.moreNav || T('Opens: Copy Link, Chat Topic, Decisions'),
      close: o.close, switchNav: o.switchNav || (t.channels ? T('Opens: the channel menu') : null), open: o.menuOpen || '',
    });
    const body = o.body || ui.thread(rounds, Object.assign({ anchor: 'bottom' }, o.thread || {}));
    const comp = composer(t, Object.assign({ send: T('Sends the message') }, o.composer || {}));
    return ui.page({ top: head, body, pad: false, bottom: comp, overlay: o.overlay || '' });
  }

  /** The plan card at its home (P1): Edit and Don’t Start, then Approve, then the link row. Every ask has three outcomes
      (§2.7): Approve, Don’t Start (the No) and Later (not now, it stays in Needs you). */
  function planHome(d, approve) {
    const a = d.ask('a2');
    const on = { Edit: T('Opens: Edit the plan'), 'Don’t Start': T('Opens: Don’t Start (Widget stops before it starts)') };
    return ui.askCard({
      title: a.title, plan: a.plan, estimate: a.estimate,
      secondary: a.secondary.map((x) => Object.assign({ label: x }, on[x] || T(`Opens: ${x}`))),
      primary: Object.assign({ label: a.primary }, approve),
      links: [Object.assign({ label: 'Later' }, T('Later · it stays in Needs you'))],
    });
  }

  /** The Widget run's own session (the home of a2): its text, tool work folded to one line, the plan card at the foot. */
  function widgetSession(d, o) {
    o = o || {};
    const t = d.todo('t02');
    const s = t.session;
    const run = d.run('widget');
    const approved = run.state !== 'needs';
    const msgs = `<div class="v1-sess">
      <div class="v1-sess-h">${s.header}</div>
      <div class="v1-sess-t">${s.text}</div>
      ${ui.fold(s.fold, T('Opens: the 9 files Widget read'))}
      ${approved ? `<div class="v1-sess-t">${ui.lineMark('working')} <span>Building the widget: timeline provider first.</span></div>` : ''}
    </div>`;
    const card = approved
      ? ui.askCard({ answered: d.ask('a2').approvedText, undo: o.undo })
      : planHome(d, o.approve);
    const comp = ui.todoComposer({ to: { label: run.title, run: true, menu: true, toast: 'Opens: To Widget, or the steward' }, placeholder: `Message ${run.title}…`, inset: o.inset, send: T('Sends the message to Widget') });
    return { body: `<div class="v1-anchor-bottom">${msgs}</div>`, bottom: `<div class="v1-pending">${card}</div>${comp}` };
  }

  function widgetPane(d, o) {
    o = o || {};
    const run = d.run('widget');
    const s = widgetSession(d, o);
    const head = ui.paneHeader({
      channel: run.title, menu: true, state: run.state === 'needs' ? 'needs' : 'working', word: run.state === 'needs' ? 'Needs you' : 'Working',
      more: T('Opens: Open Full Session, Try Another Way, Stop'), close: o.close, switchNav: o.switchNav, open: o.menuOpen || '',
    });
    // the state reads in its phase's tone (Working orange, Needs you blue), as on the paper, the steps and the rows
    const top = run.state === 'needs' ? head : head.replace('class="k-pnh-st"', 'class="k-pnh-st v1-pnh-working"');
    return ui.page({ top, body: s.body, bottom: s.bottom, pad: false, overlay: o.overlay || '' });
  }

  /** The pane's channel menu (Release lead · Widget · Release notes · Screenshots), the current one checked. */
  function channelMenu(d, current, navs) {
    const t = d.todo('t02');
    const items = t.channels.map((c) => {
      const id = c.run || 'steward';
      return Object.assign({ label: c.label, sub: c.run ? `${d.run(c.run).role} · ${c.sub}` : c.sub, check: id === current }, navs[id] || T(`Opens: ${c.label} in the pane`));
    });
    return ui.menu(items, { align: 'leading', width: 280 });
  }

  /* ================================================================== S5 on Release Weather App 1.3 */

  // The S5 round (synthesis §3.0.5 T02 R2) at each stage: sending · processing · delivered (queued for Release notes) ·
  // taken (delivered to it) · now (Send Now) · undone (scope undone).
  const S5_STAGES = ['sending', 'processing', 'delivered', 'taken', 'now', 'undone'];
  function s5Round(d, stage) {
    const r = d.todo('t02').extraRounds.r2;
    const status = stage === 'sending' ? r.statuses[0] : stage === 'processing' ? r.statuses[1] : r.statuses[2];
    return Object.assign({}, r, { status });
  }
  const s5Relay = (stage) => (stage === 'taken' || stage === 'undone' ? 'delivered' : stage === 'now' ? 'now' : 'queued');
  // Undo removed the scope, so Release notes is not adding the widget any more: the run line goes with it.
  const s5Live = (stage) => stage === 'taken' || stage === 'now';

  /** “↗ Queued for Release notes · after this step [Send Now]” and its later states, worded by the data. */
  function relayHtml(d, state, sendNowNav) {
    const t = d.todo('t02');
    const it = t.extraRounds.r2.items.find((x) => x.kind === 'relay');
    return `<div class="k-rl k-rl--relay">${I('arrow-up-right', 12)}<span>${t.relayStates[state]}</span>${state === 'queued' && it.sendNow ? `<span class="k-rl-btn"${A(sendNowNav)}>Send Now</span>` : ''}</div>`;
  }
  const undoneLine = () => `<div class="k-rl">${I('arrow-uturn-left', 12)}<span>Scope change undone</span></div>`;

  /** The S5 round in the pane, at full zoom: the owner's bubble and its message state, then the steward's reply, the
      relay, the scope line with Undo and the run line (ui.convRound's look, with the relay worded by the data). */
  function s5RoundHtml(d, stage, o) {
    const r = s5Round(d, stage);
    const it = r.items;
    const after = stage !== 'sending' && stage !== 'processing';
    // one order at both zooms (the pane's round and the paper's block): the reply, the relay, then the scope line (or
    // “Scope change undone”), then the run line
    const items = after ? [
      ui.rline.reply(it[0]),
      relayHtml(d, s5Relay(stage), o.sendNow),
      stage === 'undone' ? undoneLine() : ui.rline.scope({ text: it[2].text, undo: o.undo }),
      s5Live(stage) ? ui.rline.live(it[3]) : '',
    ] : [];
    return `<div class="k-rd"><div class="k-rd-bubble">${r.text}</div><div class="k-rd-st k-rd-st--${statusTone(r.status)}">${r.status}</div>${items.join('') ? `<div class="k-rd-items">${items.join('')}</div>` : ''}</div>`;
  }

  /** The S5 round on the paper's block (one line each), with its message state, relay and run lines. */
  function s5Block(d, stage, o) {
    const t = d.todo('t02');
    const r = s5Round(d, stage);
    const items = r.items;
    const after = stage !== 'sending' && stage !== 'processing';
    const extra = [];
    if (after) {
      extra.push(relayHtml(d, s5Relay(stage), o.sendNow));
      if (stage === 'undone') extra.push(undoneLine());
      if (s5Live(stage)) extra.push(ui.rline.live(items[3]));
    }
    const rows = [
      cbRound(cbFromData(t.rounds[0]), o.roundNav),
      cbRound({ who: 'You · now', quote: r.text, status: r.status, tone: statusTone(r.status), subs: after ? [r.fold] : [], extra }, o.roundNav),
    ];
    return cbBlock(rows, { all: o.all, total: t.allCount + 1 });
  }
  const s5Scope = (d, stage, undo, short) => {
    if (!['delivered', 'taken', 'now'].includes(stage)) return '';
    const t = d.todo('t02');
    // short: “+ home-screen widget”; without undo it is plain text (the pane beside carries the one Undo)
    if (short) return `<div class="v1-scope"><div class="k-rl k-rl--scope"><span>${t.scopeLine.split(' · ')[0]}</span>${undo ? `<span class="k-sep">·</span><span class="k-rl-undo"${A(undo)}>Undo</span>` : ''}</div></div>`;
    return `<div class="v1-scope">${ui.rline.scope({ text: t.extraRounds.r2.items[2].text, undo })}</div>`;
  };

  /* ================================================================== iPhone */

  const PH = {
    list: 'iphone/list', kyoto: 'iphone/kyoto', quick: 'iphone/kyoto-quicknote-keyboard', convo: 'iphone/conversation-kyoto-keyboard',
    release: 'iphone/release', widget: 'iphone/release-widget-session', capture: 'iphone/capture-passport', passport: 'iphone/passport',
    lock: 'iphone/lock-push', tray: 'iphone/assistant-tray', insurance: 'iphone/insurance-reply', rconvo: 'iphone/conversation-release',
  };

  const phoneRowGo = (t) => ({ t01: N(PH.kyoto, PUSH), t02: N(PH.release, PUSH), t11: N(PH.passport, PUSH) }[t.id] || T(`Opens: ${t.title}`));
  const phoneAns = (t) => ({
    t01: { go: `${PH.list}~kyoto-answered`, transition: FADE, options: N(`${PH.list}~options`, FADE) },
    t02: N(PH.widget, PUSH),
    t03: { go: `${PH.list}~back`, transition: FADE, options: N(`${PH.list}~options-t03`, FADE) },
    t13: N(PH.insurance, PUSH),
  }[t.id] || {});
  const phonePlaces = {
    all: N(PH.list, NONE), today: N(`${PH.list}~today`, NONE), kyoto: N(`${PH.list}~kyoto`, NONE),
    upcoming: T('Opens: Upcoming'), weather: T('Opens: Weather App'), home: T('Opens: Home'), projects: T('Opens: All Projects'),
  };
  const phoneBar = {
    all: N(PH.list, NONE), needs: N(`${PH.list}~filter-needs`, NONE), progress: N(`${PH.list}~filter-progress`, NONE),
    todo: N(`${PH.list}~filter-todo`, NONE),
  };

  /** V1-01: the list on a phone. o: { place, filter, menu, more, group, drawer, toast, rows: {…sectionsHtml options}, pill
      (true), barCounts, body, offset, overlay } */
  function phoneList(d, o) {
    o = o || {};
    const place = o.place || 'all';
    const p = d.place(place);
    const calm = d.isCalm(place);
    const empty = !d.sections(place, o.filter || 'all').length;
    const menuItems = ui.placeMenuItems({ selected: place, go: phonePlaces });
    const titleNav = place !== 'all' ? T('Opens: the places menu') : N(o.menu ? PH.list : `${PH.list}~menu`, FADE);
    const title = ui.titleMenu(Object.assign({ title: p.title, open: !!o.menu, items: menuItems, width: 270 }, titleNav));
    const bar = calm || empty ? '' : sectionBar(d, {
      place, filter: o.filter, go: place === 'all' ? phoneBar : {}, counts: o.barCounts,
      group: place === 'all' ? N(o.group ? PH.list : `${PH.list}~group`, FADE) : null,
    });
    const top = ui.navbar({
      leading: [{ icon: 'line-3-horizontal', title: 'Show Sidebar', go: `${PH.list}~drawer`, transition: FADE }],
      center: title,
      trailing: [{ icon: 'magnifying-glass', title: 'Search', toast: 'Opens: Search To-dos' }, { icon: 'ellipsis', title: 'More', go: `${PH.list}~more`, transition: FADE }],
      below: bar,
      fog: !!(o.rows && o.rows.startAt),
    });
    let body = o.body;
    if (body) { /* drawn by the caller */ } else if (empty) body = placeEmpty(d, place);
    else {
      body = sectionsHtml(d, Object.assign({
        place, filter: o.filter, line3: true, go: phoneRowGo, ans: phoneAns,
        foldNav: { can_wait: N(`${PH.list}~can-wait`, FADE), someday: T('Opens: Someday') },
        tick: (t) => (t.id === 't11' ? N(`${PH.list}~ticked`, FADE) : null),
      }, o.rows || {}));
    }
    if (o.offset === 'end') body = `<div class="v1-end v1-end--pill">${body}</div>`;
    else if (o.offset) body = `<div style="margin-top:${-o.offset}px">${body}</div>`;
    const overlay = [];
    if (o.pill !== false) overlay.push(ui.pill({ label: 'New To-Do', go: `${PH.capture}~empty`, transition: FADE }));
    if (o.toast) overlay.push(o.toast);
    if (o.more) {
      // E2: Group By lives in the bar's grouping symbol; ⋯ keeps Sort and Show.
      overlay.push(`<div class="v1-catch"${A(N(PH.list, FADE))}></div>`);
      overlay.push(anchored(ui.menu([
        Object.assign({ label: 'Sort By', sub: 'Recent activity', icon: 'chevron-up-down' }, T('Opens: Sort By')), '-',
        Object.assign({ label: 'Show Someday', icon: 'clock' }, T('Shows Someday under To do')), Object.assign({ label: 'Show Done Today', icon: 'checkmark-circle' }, T('Shows Done Today at the foot')),
      ], { align: 'trailing', width: 250 }), 'top:112px;right:16px'));
    }
    if (o.group) {
      overlay.push(`<div class="v1-catch"${A(N(PH.list, FADE))}></div>`);
      overlay.push(anchored(ui.menu([
        { header: 'Group By' }, { label: 'Phase', check: true, go: PH.list, transition: FADE },
        Object.assign({ label: 'Project' }, T('Groups by project; Needs you stays a section at the top')),
        Object.assign({ label: 'Due Date' }, T('Groups by due date; Needs you stays a section at the top')),
      ], { align: 'trailing', width: 230 }), 'top:158px;right:16px'));
    }
    if (o.drawer) {
      overlay.push(ui.drawer({ scrimNav: N(PH.list, FADE), content: tree(d, { selected: place === 'all' ? 'todos' : place, go: { todos: N(PH.list, FADE), today: N(`${PH.list}~today`, FADE), kyoto: N(`${PH.list}~kyoto`, FADE) } }) }));
    }
    if (o.overlay) overlay.push(o.overlay);
    return ui.page({ top, body, overlay: overlay.join('') });
  }

  const listNote = 'Place All To-dos, nothing selected. Toolbar: ☰ (the drawer), the title menu “All To-dos ▾”, ⌕ and ⋯ (Sort, Show); the section bar is the Inbox’s chips with the landing choice first (All · Needs you (3) · In progress · To do; Done only while it has rows; the grouping symbol inside it). To-dos lands on All, at the bar’s start, so Needs you and its blue N always show beside it; sections follow the hub’s order (Needs you · Working · Waiting · To do · Just finished). Safe asks answer on the row (OK: Ryokan Hanami), a plan shows one door (Review Plan ›), a failed start offers Start When It’s Back; “Can wait · 1” is listed, not counted. Line 3 carries the facts grouping doesn’t show.';

  frame(PH.list, {
    device: 'iphone', important: true, title: 'V1-01 · iPhone · To-dos list (All To-dos)',
    note: `${listNote} Tap the Kyoto row (→ its paper), OK: Ryokan Hanami, Options ⌄, Review Plan ›, Start When It’s Back, Can wait · 1, the bar chips and its grouping symbol, ☰, the title, ⋯ and + New To-Do.`,
    render: (ctx, d) => phoneList(d),
  });
  frame(`${PH.list}~scrolled`, {
    device: 'iphone', title: 'V1-01 · scrolled: Just finished, Waiting, To do, Someday',
    note: 'Scrolled to the end: Waiting (who or what, since when), To do (Renew passport is a one-line row; Buy groceries carries “Home · Steps 1/4”), Just finished (unseen rows carry the green dot), then “Someday · 1 ›” clear of the pill. The bar stays under the toolbar. Tick “Renew passport”.',
    render: (ctx, d) => phoneList(d, { offset: 'end', rows: { startAt: 'waiting' } }),
  });
  frame(`${PH.list}~kyoto-answered`, {
    device: 'iphone', title: 'V1-01 · answered from the row', view: ANSWERED('hanami'),
    note: 'OK: Ryokan Hanami: line 2 reads “✓ You chose Ryokan Hanami · Undo” for 4 s while the row keeps its place; the Needs you header and the bar’s N drop at once (the drawer too). In progress keeps its number until the row re-files: after 4 s, while the list is idle, the row fades into Working.',
    auto: { go: `${PH.list}~kyoto-booking`, after: 4000, transition: FADE },
    render: (ctx, d) => phoneListAnswered(d),
  });
  function phoneListAnswered(d) {
    // The rows keep their places (the unchanged data, the Kyoto row drawn from the answered view: its line 3 already
    // says Steps 3/5); only N moves at once, in the header and the bar alike.
    const body = sectionsHtml(D0, {
      place: 'all', line3: true, go: phoneRowGo, ans: phoneAns, answered: { t01: d.todo('t01').answered.label }, undo: N(PH.list, FADE),
      foldNav: { can_wait: N(`${PH.list}~can-wait`, FADE) }, counts: { needs: d.counts.needs }, swap: { t01: d.todo('t01') },
    });
    return phoneList(D0, { body, barCounts: { needs: d.counts.needs } });
  }
  frame(`${PH.list}~kyoto-booking`, {
    device: 'iphone', title: 'V1-01 · answered, re-filed into Working', view: ANSWERED('hanami'),
    note: 'Once the list is idle the answered row fades (0.2 s) into Working, “◐ Persona is booking Ryokan Hanami”. Needs you, the bar and the drawer all show the new N; Undo on the toast reverts everything.',
    render: (ctx, d) => phoneList(d, { toast: ui.toast(`${d.todo('t01').answered.text}`, { icon: 'checkmark-circle-fill', action: 'Undo', actionNav: N(PH.list, FADE), bottom: 110 }) }),
  });
  d1Options();
  function d1Options() {
    frame(`${PH.list}~options`, {
      device: 'iphone', title: 'V1-01 · Options ⌄ unfolds the ask card in place',
      note: 'The full AskCard (P1) unfolds under the Kyoto row: why and the prices, Kaede and Tsuki sharing one row, the recommended Ryokan Hanami as the full-width primary, then “Later · Don’t handle · Discuss…”. The row’s own answer folds into “Options ⌃”, so one primary shows; line 2 is the card’s title (drawn once).',
      render: (ctx, d) => phoneList(d, { rows: { optionsOpen: { id: 't01', close: N(PH.list, FADE), html: askCardFor('a1', { on: {
        hanami: N(`${PH.list}~kyoto-answered`, FADE), kaede: N(`${PH.list}~kyoto-answered`, FADE), tsuki: N(`${PH.list}~kyoto-answered`, FADE),
        later: N(PH.list, FADE), dont: N(`${PH.kyoto}~dont-confirm`, PUSH), discuss: N(`${PH.quick}~answer-chip`, PUSH),
      } }) }, ans: (t) => (t.id === 't01' ? { go: `${PH.list}~kyoto-answered`, transition: FADE, options: N(PH.list, FADE) } : phoneAns(t)) } }),
    });
    frame(`${PH.list}~options-t03`, {
      device: 'iphone', title: 'V1-01 · Options ⌄ on a failed start',
      note: 'The failed start’s card unfolds in place: Use MacBook Instead, then the primary Start When It’s Back, then the exits; the row’s own button folds into “Options ⌃”. A computer appears only by the owner’s name for it, and only in a failed-start or waiting reason.',
      render: (ctx, d) => phoneList(d, { offset: 250, rows: { optionsOpen: { id: 't03', close: N(PH.list, FADE), html: askCardFor('a3', { on: {
        primary: N(`${PH.list}~back`, FADE), other: T('Starts on MacBook instead'), later: N(PH.list, FADE), dont: T('Opens: the confirmation for Don’t handle'), discuss: T('Opens: the paper with “Answer: …” in the composer'),
      } }) }, ans: (t) => (t.id === 't03' ? { go: `${PH.list}~back`, transition: FADE, options: N(PH.list, FADE) } : phoneAns(t)) }, }),
    });
  }
  frame(`${PH.list}~back`, {
    device: 'iphone', title: 'V1-01 · Start When It’s Back', view: { a3: 'back' },
    note: 'The failed start moves to Waiting (“◷ Starts when Desk Mac is back”, not counted) and N drops at once. One tap; no “failed” word anywhere.',
    render: (ctx, d) => phoneList(d, { toast: ui.toast(d.ask('a3').after, { icon: 'clock', action: 'Undo', actionNav: N(PH.list, FADE), bottom: 110 }) }),
  });
  frame(`${PH.list}~can-wait`, {
    device: 'iphone', title: 'V1-01 · Can wait unfolded',
    note: 'Can wait lists what is for the owner but not counted: Persona asked in plain text and waits for a reply. Reply opens the paper with the composer focused.',
    render: (ctx, d) => phoneList(d, { rows: { startAt: 'needs', open: { can_wait: true }, foldNav: { can_wait: N(PH.list, FADE) } } }),
  });
  frame(`${PH.list}~swiped`, {
    device: 'iphone', title: 'V1-01 · trailing swipe',
    note: 'One trailing swipe, as in the Inbox: Done (a full swipe completes) and Someday. Long press offers Today, Date…, Project…, Someday, Message Persona…, Copy Link, Delete….',
    render: (ctx, d) => phoneList(d, { offset: 'end', rows: { startAt: 'waiting', swiped: 't09' } }),
  });
  frame(`${PH.list}~ticked`, {
    device: 'iphone', title: 'V1-01 · a tick',
    note: 'The tick is optimistic: strike-through, a haptic and “Done · Undo” for 4 s; the row leaves for Done once the list is idle.',
    render: (ctx, d) => phoneList(d, { offset: 'end', rows: { startAt: 'waiting', ticked: 't11', tick: () => N(`${PH.list}~scrolled`, FADE) }, toast: ui.toast('Done', { icon: 'checkmark-circle-fill', action: 'Undo', actionNav: N(`${PH.list}~scrolled`, FADE), bottom: 110 }) }),
  });
  frame(`${PH.list}~passport-added`, {
    device: 'iphone', title: 'V1-01 · captured (S1)',
    note: 'After Add: the sheet closes; “Renew passport · Fri” lands in To do with a faint “Saving…” (under 1 s) and the toast “Added · Undo”. Nothing about Agents, computers or folders.',
    render: (ctx, d) => phoneList(d, { offset: 'end', rows: { startAt: 'waiting', saving: 't11' }, toast: ui.toast('Added', { icon: 'checkmark-circle-fill', action: 'Undo', actionNav: N(`${PH.list}~scrolled`, FADE), bottom: 110 }) }),
  });
  [['needs', 'Needs you'], ['progress', 'In progress'], ['todo', 'To do']].forEach(([f, label]) => {
    frame(`${PH.list}~filter-${f}`, {
      device: 'iphone', title: `V1-01 · the bar on ${label}`,
      note: f === 'needs' ? 'Needs you only, with Can wait folded at its foot. The choice is not remembered.'
        : f === 'progress' ? 'In progress, as the tray’s entry (#8237): Working and Waiting, with Just finished listed at its foot and not counted in the chip.'
          : 'To do, with Someday folded at its foot. (Done is not offered while nothing has closed: E2 shows the other choices only while they have rows.)',
      render: (ctx, d) => phoneList(d, { filter: f }),
    });
  });
  frame(`${PH.list}~calm`, {
    device: 'iphone', title: 'V1-01 · calm (a first-use account)', view: { calm: true },
    note: 'The calm rule: while only the owner’s plain To do rows exist and nothing needs the owner, no bar and no section headers. A plain list, as in Reminders, with “Someday · 1 ›”.',
    render: (ctx, d) => phoneList(d),
  });
  frame(`${PH.list}~drawer`, {
    device: 'iphone', title: 'V1-01 · the drawer',
    note: 'The drawer (82% of the width, ≤ 380) over the dimmed page shows the sidebar tree: To-dos with the blue N and its children Today, Upcoming, then the projects with their blue part of N. The Pinned direction keeps its row under Persona Chats.',
    render: (ctx, d) => phoneList(d, { drawer: true, pill: false }),
  });
  frame(`${PH.list}~menu`, {
    device: 'iphone', title: 'V1-01 · the title menu',
    note: 'The title menu is the second way to the same places: All To-dos ✓, Today, Upcoming, then the projects with their blue part of N, All Projects…. Picking a place swaps the root, never animated.',
    render: (ctx, d) => phoneList(d, { menu: true }),
  });
  frame(`${PH.list}~more`, {
    device: 'iphone', title: 'V1-01 · ⋯ (Sort, Show)',
    note: 'The list’s ⋯ keeps Sort By and Show (Show Someday, Show Done Today); Group By moved into the bar’s grouping symbol (E2).',
    render: (ctx, d) => phoneList(d, { more: true }),
  });
  frame(`${PH.list}~group`, {
    device: 'iphone', title: 'V1-01 · the bar’s grouping symbol (Group By)',
    note: 'The grouping symbol inside the bar’s capsule opens Group By: Phase (default) · Project · Due Date. In every grouping Needs you stays a section at the top.',
    render: (ctx, d) => phoneList(d, { group: true }),
  });
  frame(`${PH.list}~today`, {
    device: 'iphone', title: 'V1-01 · Today (its empty state)',
    note: 'A place with identity and its own empty state. Nothing is due today in the mock, so Today says so and points to what comes this week.',
    render: (ctx, d) => phoneList(d, { place: 'today' }),
  });
  frame(`${PH.list}~kyoto`, {
    device: 'iphone', title: 'V1-01 · the place Kyoto trip',
    note: 'Picked from the title menu or the drawer: the root swaps (not animated); the title becomes the place; a to-do captured here lands in Kyoto trip.',
    render: (ctx, d) => phoneList(d, { place: 'kyoto' }),
  });

  /* ---------------------------------------------------------------- V1-02 the paper (T01) */

  const kyotoAskOn = (base) => ({
    hanami: N(`${PH.kyoto}~answered`, FADE), kaede: N(`${PH.kyoto}~answered-kaede`, FADE), tsuki: N(`${PH.kyoto}~answered-tsuki`, FADE),
    later: N(`${PH.kyoto}~later`, FADE), dont: N(`${PH.kyoto}~dont-confirm`, FADE), discuss: N(`${PH.quick}~answer-chip`, FADE),
    laterOpen: N(base || PH.kyoto, FADE),
  });

  /** V1-02 at phone width. o: { t, back: { label, nav }, offset, scrolled, ask, state, extra, overlay, steps, dot, recap } */
  function kyotoPhone(d, o) {
    o = o || {};
    const t = o.t || d.todo('t01');
    const back = o.back || { label: d.place('all').title, nav: N(PH.list, POP) };
    const scrolled = !!o.scrolled;
    return paperPage(d, t, {
      leading: [scrolled ? Object.assign({ icon: 'chevron-left', title: 'Back' }, back.nav) : backCapsule(back.label, back.nav)],
      title: scrolled ? t.title : '',
      convNav: N(`${PH.convo}~keyboard-down`, PUSH), dot: o.dot !== false,
      moreNav: N(`${PH.kyoto}~menu`, FADE),
      offset: o.offset,
      paper: Object.assign({
        askOn: kyotoAskOn(),
        recapNav: N(`${PH.kyoto}~scrolled`, NONE),
        allNav: N(`${PH.convo}~keyboard-down`, PUSH), roundNav: N(`${PH.convo}~keyboard-down`, PUSH),
      }, o.paper || {}),
      composerNav: o.composerNav || N(PH.quick, FADE),
      overlay: o.overlay,
    });
  }

  frame(PH.kyoto, {
    device: 'iphone', important: true, title: 'V1-02 · iPhone · the paper (Plan the Kyoto trip)',
    note: 'The item comes first: tick + title, “Needs you · Persona found 3 ryokans near Gion”, Since you looked, then the ask card at its home (P1: Kaede and Tsuki, then the recommended Ryokan Hanami full width, then Later · Don’t handle · Discuss…), chips, notes, the folded brief, Steps (scroll for Results and the Conversation block). The composer is docked at the foot with “To Persona” (no ▾: no runs, no direction). No Agent, runtime or computer word.',
    render: (ctx, d) => kyotoPhone(d),
  });
  frame(`${PH.kyoto}~scrolled`, {
    device: 'iphone', title: 'V1-02 · scrolled to the foot',
    note: 'The bar shows the title once it scrolls away. Steps 2 of 5 with who has each, Results (Flights · held until Fri, Ryokan shortlist · 3), and the Conversation block: the latest two rounds, one line each, then All 4 ›.',
    render: (ctx, d) => kyotoPhone(d, { scrolled: true, offset: 'end' }),
  });
  [['hanami', ''], ['kaede', '-kaede'], ['tsuki', '-tsuki']].forEach(([opt, suffix]) => {
    frame(`${PH.kyoto}~answered${suffix}`, {
      device: 'iphone', important: !suffix, title: `V1-02 · answered${suffix ? ` (${suffix.slice(1)})` : ''}`, view: ANSWERED(opt),
      note: 'The card collapses (0.25 s) to “✓ You chose … · Undo” for 4 s. The state line becomes “Working · Persona is booking …”, Since you looked goes, and the step reads “✓ Pick a ryokan · you”. The answer leaves every door at once.',
      auto: suffix ? undefined : { go: `${PH.kyoto}~answered-round`, after: 4000, transition: FADE },
      render: (ctx, d) => {
        const t = d.todo('t01');
        return kyotoPhone(d, { t, dot: false, paper: { ask: { answered: t.answered.text, undo: N(PH.kyoto, FADE) } } });
      },
    });
  });
  frame(`${PH.kyoto}~answered-round`, {
    device: 'iphone', title: 'V1-02 · answered, 4 s later', view: ANSWERED('hanami'),
    note: 'After 4 s the card is gone and the answer is a round in the Conversation block: “You chose Ryokan Hanami (from the card)” └ “◐ Persona · booking”. Scrolled to the block.',
    render: (ctx, d) => {
      const t = d.todo('t01');
      const extra = [{ id: 'ans', day: 'Today', time: 'now', cb: { who: 'You · now', text: `chose ${optionOf(d, 'hanami').label} <span class="v1-faint">(from the card)</span>`, subs: [`${ui.spinner(10)} Persona · booking`] } }];
      // The page is short once the card is gone: scrolled to its foot, the title is still on the page, so the bar has none.
      return kyotoPhone(d, { t, dot: false, offset: 'end', paper: { ask: 'none', extra } });
    },
  });
  frame(`${PH.kyoto}~later`, {
    device: 'iphone', title: 'V1-02 · Later',
    note: 'Later is “not now”, never a No: the card folds to one line, “Which ryokan? · later today ›”, and the to-do stays in Needs you.',
    render: (ctx, d) => kyotoPhone(d, { paper: { ask: 'later' } }),
  });
  frame(`${PH.kyoto}~dont-confirm`, {
    device: 'iphone', title: 'V1-02 · Don’t handle (confirm)',
    note: 'Don’t handle declines the request: it is recorded and can be undone.',
    render: (ctx, d) => kyotoPhone(d, { overlay: alertBox({ title: 'Persona won’t book a ryokan.', body: 'You can undo this.', scrimNav: N(PH.kyoto, FADE), actions: [{ label: 'Cancel', nav: N(PH.kyoto, FADE) }, { label: 'Don’t Handle', bold: true, nav: N(`${PH.kyoto}~dont`, FADE) }] }) }),
  });
  frame(`${PH.kyoto}~dont`, {
    device: 'iphone', title: 'V1-02 · not handled',
    note: 'The to-do moves to Working: “Persona is continuing without a ryokan”. The step says so; Undo brings the request back.',
    render: (ctx, d) => {
      const t = d.todo('t01');
      const steps = t.steps.map((s) => (s.state === 'needs' ? { text: s.text, state: 'idle', word: 'not handled' } : s));
      return kyotoPhone(d, {
        dot: false,
        paper: { state: { phase: 'working', word: d.words.working, text: 'Persona is continuing without a ryokan' }, recap: false, ask: 'none', steps },
        overlay: ui.toast('Not handled', { action: 'Undo', actionNav: N(PH.kyoto, FADE) }),
      });
    },
  });
  frame(`${PH.kyoto}~menu`, {
    device: 'iphone', title: 'V1-02 · ⋯',
    note: 'Behind ⋯: Copy Link · Move to Project… · Someday · Decisions (2) · Chat Topic… · Start an Agent on a Computer… (the Agents audience; New Agent prefilled) · Delete….',
    render: (ctx, d) => {
      const t = d.todo('t01');
      const items = t.menu.map((m) => Object.assign({ label: m, danger: /^Delete/.test(m) }, T(`Opens: ${m.replace('…', '')}`)));
      items.splice(items.length - 1, 0, '-');
      return kyotoPhone(d, { overlay: `<div class="v1-catch"${A(N(PH.kyoto, FADE))}></div>` + anchored(ui.menu(items, { align: 'trailing', width: 290 }), 'top:108px;right:16px') });
    },
  });
  frame(`${PH.kyoto}~from-assistant`, {
    device: 'iphone', title: 'V1-02 · opened from the Assistant’s Needs you',
    note: 'A to-do picked in the Assistant’s tray is pushed on the Assistant’s stack: Back returns to the chat (owner decision 2).',
    render: (ctx, d) => kyotoPhone(d, { back: { label: 'Assistant', nav: N(PH.tray, POP) } }),
  });

  /* ---------------------------------------------------------------- V1-03 composer focused on the paper (folded) */

  /** The paper folded above the keyboard: the bar shows the title with the state under it, the ask as one line (44),
      then “Steps · Results” and the Conversation block's latest rounds right above the composer. */
  function foldedPage(d, t, o) {
    o = o || {};
    const short = t.state && (t.state.short || t.state.text);
    const sub = o.sub != null ? o.sub : ui.stateLine(t.phase, keepParts(short || ''), { word: t.state && t.state.word, clamp: 1 });
    // The title and the state stack inside the bar (17 over 13) and end before the trailing group, as a navigation
    // subtitle does; nothing passes under the glass.
    const top = ui.navbar({
      align: 'leading', cls: `v1-fold-nav${o.askLine ? '' : ' is-bare'}`,
      leading: [Object.assign({ icon: 'chevron-left', title: 'Back' }, o.back)],
      center: `<div class="v1-fold-tt"><div class="v1-fold-title">${t.title}</div><div class="v1-fold-sub">${sub}</div></div>`,
      trailing: [convBtn(o.convNav, { dot: o.dot }), moreBtn(o.moreNav)],
      below: o.askLine ? `<div class="v1-askbar">${o.askLine}</div>` : '',
    });
    // The folded paper is the paper scrolled to its end: what it still says (the recap, chips, notes, steps and results,
    // the scope), then the latest two rounds right above the composer. When it is taller than the room, its top passes
    // under the bar's scroll-edge fade, as a scrolled page does; no blank band opens between the two.
    const head = (o.head || []).length ? `<div class="v1-fold-head">${o.head.join('')}</div>` : '';
    const body = `<div class="v1-anchor-bottom${o.top ? ' is-top' : ''}"${A(o.tapNav)}>${head}${(o.stack || []).join('')}</div>`;
    const comp = composer(t, Object.assign({ send: T('Sends the message'), focused: true }, o.composer || {}));
    return ui.page({ top, body, bottom: tap(comp, o.composerNav), overlay: o.overlay || '' });
  }

  function kyotoFolded(d, o) {
    o = o || {};
    const t = d.todo('t01');
    const answered = t.answered;
    const askLine = answered
      ? `<div class="k-ac is-answered v1-ans-line">${I('checkmark-circle-fill', 18)}<span class="k-ac-ans">${answered.text}${o.fromMsg ? ' <span class="v1-faint">· from your message</span>' : ''}</span><span class="k-sep">·</span><span class="k-ac-undo"${A(N(PH.quick, FADE))}>Undo</span></div>`
      : ui.askLineFor(t, { ok: Object.assign({ label: t.answer.short }, N(`${PH.quick}~answered`, FADE)), options: N(`${PH.kyoto}`, FADE) });
    const rounds = (t.rounds || []).filter((r) => r.kind !== 'background').concat(o.extra || []);
    const rows = rounds.slice(-(o.count || 2)).map((r) => cbRound(r.cb || cbFromData(r), N(`${PH.convo}~keyboard-down`, PUSH)));
    // What the paper still says above the latest rounds: Since you looked (until answered), the chips, the owner's notes,
    // then steps and results on one line.
    const head = [
      !answered && t.recap ? ui.recapLine(t.recap, N(PH.kyoto, FADE)) : '',
      chipsRow(t),
      t.notes ? ui.notes(t.notes, N(PH.kyoto, FADE)) : '',
      ui.briefLineFor(t, N(PH.kyoto, FADE)),
      ui.stepsSummary(t, Object.assign({ results: t.results.length }, N(PH.kyoto, FADE))),
    ].filter(Boolean);
    const stack = [cbBlock(rows, { all: N(`${PH.convo}~keyboard-down`, PUSH), total: t.allCount + (o.extra || []).filter((r) => r.id !== 'ans').length })];
    return foldedPage(d, t, {
      back: N(PH.kyoto, POP), convNav: N(o.convTo || `${PH.convo}~moved-draft`, PUSH), dot: !answered, moreNav: N(`${PH.kyoto}~menu`, FADE),
      askLine, head, stack, tapNav: N(PH.kyoto, FADE),
      composer: o.composer, composerNav: o.composerNav,
    });
  }

  const r4 = (d, status) => Object.assign({}, d.todo('t01').extraRounds.r4, { status });
  const r4Row = (d, status, reply) => {
    const r = r4(d, status);
    // the reply in full, on up to two lines: its last words are the part that decides (“Kaede doesn’t.”)
    const said = (r.items || []).find((x) => x.kind === 'reply');
    const line = said ? `${said.who}: ${said.text}` : r.fold;
    return { id: 'r4', cb: { who: 'You · now', quote: r.text, status, tone: statusTone(status), subs: reply ? [`<span class="v1-two">${line}</span>`] : [] } };
  };

  frame(PH.quick, {
    device: 'iphone', important: true, chrome: { keyboard: true }, title: 'V1-03 · iPhone · composer focused on the paper',
    note: 'Telling it something needs no conversation. The paper folds to keep the item above the keyboard: the bar shows the title with the state under it, the ask folds to one line (● Which ryokan? [OK: Hanami] Options ⌄), steps and results to one line, and the latest rounds sit just above the composer. Title, state and the open ask stay in view. Type to continue.',
    render: (ctx, d) => kyotoFolded(d, { composerNav: N(`${PH.quick}~typing`, NONE), convTo: `${PH.convo}~keyboard-down` }),
  });
  frame(`${PH.quick}~typing`, {
    device: 'iphone', chrome: { keyboard: true }, title: 'V1-03 · typing the note',
    note: 'The draft “also check that breakfast is included” to Persona. ↑ sends it; nothing opens.',
    render: (ctx, d) => kyotoFolded(d, { composer: { text: d.todo('t01').drafts.quickNote, send: N(`${PH.quick}~sent`, NONE) } }),
  });
  const quickChain = [['sent', 'Sending', 150], ['processing', 'Processing', 800], ['delivered', null, 3000], ['reply', null, 0]];
  quickChain.forEach(([st, label, after], i) => {
    const next = quickChain[i + 1];
    frame(`${PH.quick}~${st}`, {
      device: 'iphone', important: st === 'reply', chrome: { keyboard: true },
      title: `V1-03 · sent: ${st === 'reply' ? 'the reply folds in' : label || 'Delivered to Persona'}`,
      note: st === 'sent' ? 'After ↑ the round appears at the foot of the block: “You · now “also check that breakfast is included”” with its message state, Sending (faint, under 0.1 s).'
        : st === 'processing' ? 'The hub stored it: Processing. The field clears now; the draft is kept until then. The keyboard stays up.'
          : st === 'delivered' ? 'Taken by the addressee: “Delivered to Persona”, said once, replacing the line above.'
            : 'About 3 s later Persona’s reply folds in under the round: “Hanami and Tsuki include breakfast; Kaede doesn’t.” Nothing opened.',
      auto: next ? { go: `${PH.quick}~${next[0]}`, after, transition: NONE } : undefined,
      render: (ctx, d) => {
        const t = d.todo('t01');
        const status = label || t.extraRounds.r4.status;
        return kyotoFolded(d, { extra: [r4Row(d, status, st === 'reply')], convTo: st === 'reply' || st === 'delivered' ? PH.convo : `${PH.convo}~keyboard-down`, composer: st === 'sent' ? { text: t.drafts.quickNote, caret: false } : {} });
      },
    });
  });
  frame(`${PH.quick}~answer-chip`, {
    device: 'iphone', chrome: { keyboard: true }, title: 'V1-03 · Discuss… (the answer chip)',
    note: 'Entered from Discuss…: the chip reads “Answer: Which ryokan? ×”, so this message answers the request and ordinary messages never answer by accident. × resets the chip to To Persona.',
    render: (ctx, d) => kyotoFolded(d, { composer: { to: Object.assign({ answer: d.ask('a1').title }, N(PH.quick, FADE)), text: 'Kaede, it’s cheaper', send: N(`${PH.quick}~answered-msg`, FADE) } }),
  });
  frame(`${PH.quick}~answered`, {
    device: 'iphone', chrome: { keyboard: true }, title: 'V1-03 · OK: Hanami from the folded paper', view: ANSWERED('hanami'),
    note: 'OK: Hanami answers in place, as on the paper: “✓ You chose Ryokan Hanami · Undo”, the state turns to Working. The keyboard stays up.',
    render: (ctx, d) => kyotoFolded(d),
  });
  frame(`${PH.quick}~answered-msg`, {
    device: 'iphone', chrome: { keyboard: true }, title: 'V1-03 · answered by a message', view: ANSWERED('kaede'),
    note: 'Sending “Kaede, it’s cheaper” with the answer chip records the answer on the request (from your message), and the chip returns to To Persona.',
    render: (ctx, d) => kyotoFolded(d, { fromMsg: true, extra: [{ id: 'kaede', cb: { who: 'You · now', quote: 'Kaede, it’s cheaper', status: 'Delivered to Persona', subs: [`${I('checkmark', 11)} You chose Kaede · from your message`] } }] }),
  });

  /* ---------------------------------------------------------------- V1-04 the pushed Conversation */

  /** V1-04: pushed from the right, the to-do pinned as its header (92). o: { extra (rounds), draft, keyboard, header (HTML),
      open (run line), bgOpen, overlay } */
  /** The pinned header as V1-04 draws it: “‹ title ⋯” over “● Needs you · Which ryokan?” over “[OK: Hanami] Options ⌄”
      (the state word stays: §3.0.4 row 2 is the state word and the ask). o: { back, more, ok, options, open (the card is
      unfolded below it: no OK, since the card's primary is the one answer on screen, and “Options ⌃”), close } */
  function askHeader(d, t, o) {
    o = o || {};
    const base = { layout: 'two', back: o.back, more: o.more };
    if (o.open) {
      const ask = d.ask(t.asks[0]);
      const text = `<span class="k-ph-t">${ui.lineMark('needs')}<span class="k-ph-w">${t.state.word}</span><span class="k-sep">·</span><span class="k-ph-q">${ask.title}</span></span>`;
      const opt = `<span class="k-ans-opt is-open"${A(o.close)}><span>Options</span>${I('chevron-up', 11)}</span>`;
      // “Options ⌃” stands where OK stood, at the leading edge, right above the card it folds
      base.row2 = `<div class="k-ph-r2">${text}</div><div class="k-ph-r3 v1-r3-open">${opt}<span class="k-grow"></span></div>`;
    } else {
      base.ok = o.ok;
      base.options = o.options;
    }
    return ui.pinnedHeaderFor(t, base);
  }

  function kyotoConvo(d, o) {
    o = o || {};
    const t = d.todo('t01');
    const header = o.header || askHeader(d, t, {
      back: N(PH.kyoto, POP), more: N(`${PH.convo}~menu`, FADE),
      ok: Object.assign({ label: t.answer.short }, N(`${PH.convo}~answered`, FADE)), options: N(`${PH.convo}~options`, FADE),
      open: o.optionsOpen, close: N(`${PH.convo}~keyboard-down`, FADE),
    });
    const rounds = wiredRounds((t.rounds || []).concat(o.extra || []), { runNav: N(o.open ? `${PH.convo}~keyboard-down` : `${PH.convo}~run-open`, FADE) });
    const body = threadHtml(rounds, {
      open: o.open ? { 'Ryokan search': true } : null, bgOpen: o.bgOpen, bgDetail: 'Flights still ¥84,200 for two · checked at 9:00 and 21:00',
      bgNav: N(o.bgOpen ? `${PH.convo}~keyboard-down` : `${PH.convo}~bg-open`, FADE), nav: { session: T('Opens: Ryokan search’s session (the run has ended)') },
    });
    const comp = composer(t, { text: o.draft || '', send: T('Sends the message to Persona') });
    return ui.page({ top: header, body, pad: false, bottom: comp, overlay: o.overlay || '' });
  }

  frame(PH.convo, {
    device: 'iphone', important: true, chrome: { keyboard: true }, title: 'V1-04 · iPhone · the pushed Conversation, keyboard up',
    note: 'Pushed from the right. The pinned header is the paper collapsed: “‹ Plan the Kyoto trip ⋯” over “● Needs you · Which ryokan?” over “[OK: Hanami] Options ⌄”, the same element as the card on the paper. Every round at full zoom, scrolled to the latest; tool work stays one line per run; the request shows in the thread only as “● Asked: Which ryokan?” (drawn once). About 290 pt of thread above the keyboard.',
    render: (ctx, d) => kyotoConvo(d, { extra: [d.todo('t01').extraRounds.r4], draft: d.todo('t01').drafts.pane }),
  });
  frame(`${PH.convo}~keyboard-down`, {
    device: 'iphone', title: 'V1-04 · keyboard down',
    note: 'Opened with ◫ or All 4 › from the paper: the full thread scrolled to the latest round, the composer at the foot. ‹, an edge swipe or a tap on the title pops back on the same path.',
    render: (ctx, d) => kyotoConvo(d),
  });
  frame(`${PH.convo}~moved-draft`, {
    device: 'iphone', chrome: { keyboard: true }, title: 'V1-04 · the draft moved in',
    note: '◫ from the focused paper carries the draft into the Conversation’s composer: one draft per to-do, kept above the view tree.',
    render: (ctx, d) => kyotoConvo(d, { draft: d.todo('t01').drafts.quickNote }),
  });
  frame(`${PH.convo}~options`, {
    device: 'iphone', title: 'V1-04 · Options ⌄ from the pinned header',
    note: 'The full AskCard unfolds from the header down over the dimmed thread; the header’s OK folds away while it shows (one primary on screen) and Options turns ⌃. Answering folds it back.',
    render: (ctx, d) => kyotoConvo(d, { optionsOpen: true, overlay: `<div class="v1-dim"${A(N(`${PH.convo}~keyboard-down`, FADE))}></div><div class="v1-drop">${askCardFor('a1', { on: {
      hanami: N(`${PH.convo}~answered`, FADE), kaede: N(`${PH.convo}~answered`, FADE), tsuki: N(`${PH.convo}~answered`, FADE),
      later: N(`${PH.convo}~keyboard-down`, FADE), dont: N(`${PH.kyoto}~dont-confirm`, POP), discuss: N(`${PH.convo}~keyboard-down`, FADE),
    } })}</div>` }),
  });
  frame(`${PH.convo}~answered`, {
    device: 'iphone', title: 'V1-04 · answered in the header', view: ANSWERED('hanami'),
    note: 'OK: Hanami answers in place. Row 2 of the header becomes “◐ Working · Persona is booking Ryokan Hanami”, with the toast “✓ You chose Ryokan Hanami · Undo”.',
    render: (ctx, d) => {
      const t = d.todo('t01');
      const header = ui.pinnedHeader({ title: t.title, back: N(PH.kyoto, POP), more: N(`${PH.convo}~menu`, FADE), row2: `<div class="k-ph-r2">${ui.stateLine(t.phase, t.state.text, { word: t.state.word, mark: true, clamp: 1 })}</div>` });
      return kyotoConvo(d, { header, overlay: ui.toast(t.answered.text, { icon: 'checkmark-circle-fill', action: 'Undo', actionNav: N(`${PH.convo}~keyboard-down`, FADE), bottom: 110 }) });
    },
  });
  frame(`${PH.convo}~run-open`, {
    device: 'iphone', title: 'V1-04 · a run line unfolded',
    note: '▸ on a run line unfolds its latest 3 steps, one line each, and “Open Session ›” (the run has ended).',
    render: (ctx, d) => kyotoConvo(d, { open: true }),
  });
  frame(`${PH.convo}~bg-open`, {
    device: 'iphone', title: 'V1-04 · the Background round unfolded',
    note: 'Work nobody asked for (checks, patrols) forms one folded Background round per day; it unfolds in place.',
    render: (ctx, d) => kyotoConvo(d, { bgOpen: true }),
  });
  frame(`${PH.convo}~menu`, {
    device: 'iphone', title: 'V1-04 · ⋯', note: 'Copy Link · Chat Topic… · Decisions.',
    render: (ctx, d) => kyotoConvo(d, { overlay: `<div class="v1-catch"${A(N(`${PH.convo}~keyboard-down`, FADE))}></div>` + anchored(ui.menu(['Copy Link', 'Chat Topic…', 'Decisions'].map((m) => Object.assign({ label: m }, T(`Opens: ${m.replace('…', '')}`))), { align: 'trailing', width: 220 }), 'top:108px;right:16px') }),
  });

  /* ---------------------------------------------------------------- V1-05 Release Weather App 1.3 (T02) */

  const releaseStepNav = (s) => (s.run === 'widget' ? N(PH.widget, PUSH) : T(`Opens: the ${s.text} session`));
  const workRowsNav = (rows) => rows.map((r) => Object.assign({}, r, r.run === 'widget' ? N(PH.widget, PUSH) : r.run ? T(`Opens: the ${r.text.split(' · ')[0]} session`) : T('Opens: the code change in Agents (Review Changes)')));

  function releasePhone(d, o) {
    o = o || {};
    const t = d.todo('t02');
    const scrolled = !!o.scrolled;
    return paperPage(d, t, {
      leading: [scrolled ? Object.assign({ icon: 'chevron-left', title: 'Back' }, N(PH.list, POP)) : backCapsule(d.place('all').title, N(PH.list, POP))],
      title: scrolled ? t.title : '',
      convNav: N(PH.rconvo, PUSH), moreNav: T('Opens: Copy Link, Move to Project, Someday, Decisions, Chat Topic, Delete'),
      offset: o.offset,
      paper: Object.assign({
        askOn: { door: N(PH.widget, PUSH), later: T('Later · it stays in Needs you'), dont: T('Opens: the confirmation for Don’t handle') },
        recapNav: N(`${PH.release}~scrolled`, NONE),
        stepNav: releaseStepNav, allNav: N(PH.rconvo, PUSH), roundNav: N(PH.rconvo, PUSH),
        work: o.work, workNav: N(o.work === 'open' ? `${PH.release}~scrolled` : `${PH.release}~work`, FADE), workRows: workRowsNav,
      }, o.paper || {}),
      composer: { to: Object.assign({}, t.composer.to, N(`${PH.release}~chip-menu`, FADE)) },
      composerNav: N(`${PH.release}~s5`, FADE),
      overlay: o.overlay,
    });
  }

  frame(PH.release, {
    device: 'iphone', title: 'V1-05 · iPhone · the paper (Release Weather App 1.3)',
    note: 'S4: the request is a door headed “From Widget”: a plan never answers in place, so one button, Review Plan ›. Parallel work is drawn as Steps with who has each. The composer chip says To Release lead ▾ (the steward is the Pinned lead). Work details stays folded at the foot.',
    render: (ctx, d) => releasePhone(d),
  });
  frame(`${PH.release}~scrolled`, {
    device: 'iphone', title: 'V1-05 · scrolled to the foot',
    note: 'Steps, Results (Screenshots · 6, What’s New · draft), the Conversation block (All 2 ›) and the one folded line Work details · 3 Agents · 1 change ▸.',
    render: (ctx, d) => releasePhone(d, { scrolled: true, offset: 'end' }),
  });
  frame(`${PH.release}~work`, {
    device: 'iphone', title: 'V1-05 · Work details unfolded',
    note: 'For the Agents audience only: each run by title, role, computer and state, the code change, and Done when. Never on the row or the default paper.',
    render: (ctx, d) => releasePhone(d, { scrolled: true, offset: 'end', work: 'open' }),
  });
  frame(`${PH.release}~chip-menu`, {
    device: 'iphone', title: 'V1-05 · To Release lead ▾',
    note: 'The addressee menu: Release lead ✓ · Persona · each active run by title, role and state. Never a runtime, computer or model.',
    render: (ctx, d) => {
      const t = d.todo('t02');
      const menu = ui.menu(t.composer.menu.map((m) => Object.assign({ label: m.label, sub: m.sub, check: m.check }, T(`To ${m.label}`))), { align: 'leading', width: 280 });
      return releasePhone(d, { overlay: `<div class="v1-catch"${A(N(PH.release, FADE))}></div>` + anchored(menu, 'left:16px;bottom:96px', 'is-up') });
    },
  });
  frame(`${PH.release}~approved`, {
    device: 'iphone', title: 'V1-05 · after approving Widget’s plan', view: { approved: { a2: true } },
    note: 'Back from the Widget session: the door is gone, the state reads “Working · 2 working · 1 done”, the step “◐ Widget · working”, and N drops everywhere (the list, the tray, the drawer).',
    render: (ctx, d) => releasePhone(d, { paper: { recap: false } }),
  });

  // S5 on the phone: the composer focused (the paper folded), then the round's stages.
  function releaseFolded(d, stage, o) {
    o = o || {};
    const t = d.todo('t02');
    const a = d.ask('a2');
    const askLine = ui.askLine({ title: `${d.run(a.run).title}: ${a.title}`, door: Object.assign({ label: a.door }, N(PH.widget, PUSH)) });
    // What the folded paper still says: Since you looked, the chips, What you asked (the scope line lands under it), then
    // the steps with who has each (the S5 note is about one of them), one line each.
    const head = [ui.recapLine(t.recap, N(PH.release, FADE)), chipsRow(t), ui.briefLineFor(t, N(PH.release, FADE))];
    const stack = [];
    const scope = stage ? s5Scope(d, stage, N(`${PH.release}~s5-undone`, FADE)) : '';
    if (scope) head.push(scope);
    head.push(ui.stepsList(t.steps.map((x) => Object.assign({}, x, releaseStepNav(x))), { title: false, compact: true }));
    stack.push(stage ? s5Block(d, stage, { all: N(PH.rconvo, PUSH), roundNav: N(PH.rconvo, PUSH), sendNow: N(`${PH.release}~s5-now`, NONE) })
      : cbBlock([cbRound(cbFromData(t.rounds[0]), N(PH.rconvo, PUSH))], { all: N(PH.rconvo, PUSH), total: t.allCount }));
    return foldedPage(d, t, {
      back: N(PH.release, POP), convNav: N(PH.rconvo, PUSH), moreNav: T('Opens: More'), askLine, head, stack, tapNav: N(PH.release, FADE),
      composer: Object.assign({ to: Object.assign({}, t.composer.to, T('Opens: the addressee menu')) }, o.composer || {}),
    });
  }
  frame(`${PH.release}~s5`, {
    device: 'iphone', chrome: { keyboard: true }, title: 'V1-05 · S5: a scope change mid-turn',
    note: 'S5: “also add the home-screen widget to the release notes”, To Release lead. The paper folds as on the Kyoto paper; the door stays one line with Review Plan ›.',
    render: (ctx, d) => releaseFolded(d, null, { composer: { text: d.todo('t02').drafts.s5, send: N(`${PH.release}~s5-sending`, NONE) } }),
  });
  const s5Chain = [['sending', 150], ['processing', 800], ['delivered', 6000], ['taken', 0]];
  S5_STAGES.forEach((stage) => {
    const i = s5Chain.findIndex((x) => x[0] === stage);
    const next = i >= 0 && s5Chain[i + 1] ? s5Chain[i + 1][0] : null;
    frame(`${PH.release}~s5-${stage}`, {
      device: 'iphone', chrome: { keyboard: true }, title: `V1-05 · S5 ${stage === 'now' ? 'Send Now' : stage}`,
      note: {
        sending: 'Sending (faint): the round appears at the foot of the block.',
        processing: 'Processing: the hub stored it; the field clears.',
        delivered: 'Delivered to Release lead. Its reply folds in, and the relay says what it did: “↗ Queued for Release notes · after this step [Send Now]”. Under What you asked: “Scope: + home-screen widget in the release notes · Undo”. After 6 s Release notes takes it.',
        taken: 'Taken: “↗ Delivered to Release notes” and the run line “◐ Release notes · adding the widget”. The owner can say who received it and that it is being acted on.',
        now: 'Send Now (only on runs that can take it): the relay line reads as delivered at once.',
        undone: 'Undo on the scope line removes the scope entry and posts “Scope change undone” in the round.',
      }[stage],
      auto: next ? { go: `${PH.release}~s5-${next}`, after: s5Chain[i][1], transition: NONE } : undefined,
      render: (ctx, d) => releaseFolded(d, stage, { composer: stage === 'sending' ? { text: d.todo('t02').drafts.s5, caret: false } : {} }),
    });
  });

  frame(PH.rconvo, {
    device: 'iphone', title: 'V1-05 · ◫ the pushed Conversation (Release lead)',
    note: 'The V1-04 layout with Release Weather App 1.3’s rounds: the pinned header carries the state word and the door (“● Needs you · Widget: Plan awaiting approval” over Review Plan ›), the thread shows Widget’s request only as a line, and the composer says To Release lead ▾.',
    render: (ctx, d) => {
      const t = d.todo('t02');
      const a2 = d.ask('a2');
      const header = ui.pinnedHeaderFor(t, { layout: 'two', ask: `${d.run(a2.run).title}: ${a2.title}`, back: N(PH.release, POP), more: T('Opens: Copy Link, Chat Topic, Decisions'), door: Object.assign({ label: a2.door }, N(PH.widget, PUSH)) });
      const body = ui.thread(t.rounds, { anchor: 'bottom' });
      return ui.page({ top: header, body, pad: false, bottom: composer(t, { to: Object.assign({}, t.composer.to, T('Opens: the addressee menu')) }) });
    },
  });

  /* ---------------------------------------------------------------- V1-06 the Widget run (home of a2) */

  function widgetPhone(d, o) {
    o = o || {};
    const t = d.todo('t02');
    const run = d.run('widget');
    const needs = run.state === 'needs';
    const row2 = `<div class="k-ph-r2"><span class="v1-run-st"><b>${run.title}</b><span class="k-sep">·</span>${needs ? `${ui.lineMark('needs')}<span class="v1-w-needs">${d.words.needs_you}</span><span class="k-sep">·</span><span>plan below</span>${I('arrow-down', 13)}` : `${ui.lineMark('working')}<span class="v1-w-working">${d.words.working}</span><span class="k-sep">·</span><span>${run.latest}</span>`}</span></div>`;
    const header = ui.pinnedHeader({ title: t.title, back: o.back === false ? false : N(o.backTo || PH.release, POP), more: o.back === false ? false : N(`${PH.widget}~menu`, FADE), row2 });
    const s = widgetSession(d, { approve: N(`${PH.widget}~approved`, FADE), undo: N(PH.widget, FADE), inset: o.inset });
    return ui.page({ top: header, body: s.body, pad: false, bottom: s.bottom, overlay: o.overlay || '' });
  }
  frame(PH.widget, {
    device: 'iphone', title: 'V1-06 · iPhone · the Widget run, pushed (the plan’s home)',
    note: 'S4 at the request’s home: the run’s own chat with tool work folded to one line, the plan card at the foot above the composer (the PendingCards look). The pinned header names the to-do and points down to the home (“plan below ↓”), so the request is drawn once. The chip To Widget ▾ is purple and sends directly.',
    render: (ctx, d) => widgetPhone(d),
  });
  frame(`${PH.widget}~approved`, {
    device: 'iphone', title: 'V1-06 · approved', view: { approved: { a2: true } },
    note: 'Approve: the card becomes “✓ Approved · Undo” and the header reads “Widget · ◐ Working · building the widget”. ‹ returns to the paper, updated.',
    render: (ctx, d) => widgetPhone(d, { backTo: `${PH.release}~approved` }),
  });
  frame(`${PH.widget}~menu`, {
    device: 'iphone', title: 'V1-06 · ⋯', note: 'Open Full Session (the Agent’s full page: terminal, files, changes) · Try Another Way · Stop.',
    render: (ctx, d) => widgetPhone(d, { overlay: `<div class="v1-catch"${A(N(PH.widget, FADE))}></div>` + anchored(ui.menu([
      Object.assign({ label: 'Open Full Session', icon: 'arrow-up-right' }, T('Opens the Agent’s full page: terminal, files, changes')),
      Object.assign({ label: 'Try Another Way', icon: 'arrow-clockwise' }, T('Opens: Try Another Way')), '-',
      Object.assign({ label: 'Stop', icon: 'stop-circle', danger: true }, T('Opens: the confirmation for Stop')),
    ], { align: 'trailing', width: 250 }), 'top:108px;right:16px') }),
  });

  /* ---------------------------------------------------------------- V1-07 capture (S1) and the new paper */

  function captureFrame(d, o) {
    const t = d.todo('t11');
    const sheet = ui.captureSheet(Object.assign({
      close: N(PH.list, FADE), add: N(`${PH.list}~passport-added`, FADE), ask: N(`${PH.passport}~asked`, PUSH),
    }, o));
    void t;
    // The flow starts before the passport exists (§3.1 V1-07): the list behind counts To do without it.
    return phoneList(d, { pill: false, overlay: `<div class="k-scrim v1-scrim-more"></div>${sheet}`, barCounts: { todo: d.counts.todo - 1 } });
  }
  frame(`${PH.capture}~empty`, {
    device: 'iphone', chrome: { keyboard: true }, title: 'V1-07 · New To-Do, just opened',
    note: 'The capture sheet, fitted to its content and docked above the keyboard: ✕, one field with a mic, Add & Ask Persona (secondary) and Add (primary). Type to continue.',
    render: (ctx, d) => `<div class="v1-tap-all"${A(N(PH.capture, FADE))}>${captureFrame(d, { text: '', placeholder: 'New To-Do', chips: [] })}</div>`,
  });
  frame(PH.capture, {
    device: 'iphone', chrome: { keyboard: true }, title: 'V1-07 · iPhone · capture “Renew passport fri”',
    note: 'S1: typing “Renew passport fri” underlines “fri” and grows a dashed chip “Fri 20 Nov”, which turns solid on Return (tap the chip here). Add files it under To do; Add & Ask Persona also hands it to Persona.',
    render: (ctx, d) => {
      const c = d.todo('t11').capture;
      return captureFrame(d, { text: c.text, token: c.token, chips: [Object.assign({ label: c.parsed, dashed: true, clear: true }, N(`${PH.capture}~parsed`, FADE))] });
    },
  });
  frame(`${PH.capture}~parsed`, {
    device: 'iphone', chrome: { keyboard: true }, title: 'V1-07 · after Return',
    note: 'Return turns the parsed chip solid (✕ removes it). + Project and + Remind stay optional.',
    render: (ctx, d) => {
      const c = d.todo('t11').capture;
      return captureFrame(d, { text: c.text.replace(` ${c.token}`, ''), chips: [{ label: c.parsed, clear: true }] });
    },
  });

  function passportPaper(d, o) {
    o = o || {};
    const t = Object.assign({}, d.todo('t11'), { notes: '' }, o.t || {});
    return paperPage(d, t, {
      leading: [backCapsule(d.place('all').title, N(PH.list, POP))],
      convNav: T('Opens: the Conversation (pushed)'),
      paper: Object.assign({
        help: o.help === false ? null : { steps: N(`${PH.passport}~steps`, FADE), doIt: N(`${PH.passport}~do-it`, FADE), close: N(`${PH.passport}~help-closed`, FADE) },
      }, o.paper || {}),
      composerNav: T('Opens: the composer, focused'),
      overlay: o.overlay,
    });
  }
  frame(PH.passport, {
    device: 'iphone', title: 'V1-07 · the new paper (five things)',
    note: 'A brand-new to-do shows five things: the title, the date chip with +, Add Notes, Persona can help (Break Into Steps · Do It for Me; ✕ hides it for this to-do) and the composer. No state line: a plain To do has nothing to say.',
    render: (ctx, d) => passportPaper(d),
  });
  const askChain = [['asked', 'Sending', 150], ['asked-processing', 'Processing', 800], ['asked-delivered', 'Delivered to Persona', 2500], ['asked-reply', 'Delivered to Persona', 0]];
  askChain.forEach(([st, status, after], i) => {
    const next = askChain[i + 1];
    frame(`${PH.passport}~${st}`, {
      device: 'iphone', title: `V1-07 · Add & Ask Persona: ${st === 'asked-reply' ? 'the reply' : status}`,
      note: st === 'asked-reply' ? 'Persona replies on the paper: “I can book the photo booth and fill the form. Want me to start?”' : 'Add & Ask Persona pushes the new paper with a round “You: Renew passport” running Sending → Processing → Delivered to Persona. Persona can help is gone: the owner already handed it over.',
      auto: next ? { go: `${PH.passport}~${next[0]}`, after, transition: NONE } : undefined,
      render: (ctx, d) => {
        const t = d.todo('t11');
        const subs = st === 'asked-reply' ? [`<span class="v1-two">Persona: ${t.capture.reply}</span>`] : [];
        const block = cbBlock([cbRound({ who: 'You · now', quote: t.title, status, tone: statusTone(status), subs })], { all: T('Opens: the Conversation'), total: 1 });
        // Persona has the to-do now, so Persona can help goes once the round exists
        return passportPaper(d, { help: false, paper: { conv: block } });
      },
    });
  });
  frame(`${PH.passport}~steps`, {
    device: 'iphone', title: 'V1-07 · Break Into Steps',
    note: 'Suggested steps arrive dashed and in italics, with Keep Steps and Edit; nothing is added until the owner keeps them.',
    render: (ctx, d) => passportPaper(d, { help: false, paper: { suggested: d.todo('t11').suggestedSteps, keep: N(`${PH.passport}~kept`, FADE), edit: T('Opens: Edit the steps') } }),
  });
  frame(`${PH.passport}~kept`, {
    device: 'iphone', title: 'V1-07 · steps kept',
    note: 'Kept steps become real steps (each is its own to-do). Persona can help goes once steps exist.',
    render: (ctx, d) => passportPaper(d, { help: false, paper: { steps: d.todo('t11').suggestedSteps.map((s) => ({ text: s, state: 'idle' })) } }),
  });
  frame(`${PH.passport}~do-it`, {
    device: 'iphone', title: 'V1-07 · Do It for Me',
    note: 'A round “You asked Persona to take care of it”; the state becomes “Working · Persona is filling the form”.',
    render: (ctx, d) => {
      const block = cbBlock([cbRound({ who: 'You · now', text: 'asked Persona to take care of it', status: 'Delivered to Persona', subs: [`${ui.spinner(10)} Persona · filling the form`] })], { all: T('Opens: the Conversation'), total: 1 });
      return passportPaper(d, { help: false, paper: { state: { phase: 'working', word: d.words.working, text: 'Persona is filling the form' }, conv: block } });
    },
  });
  frame(`${PH.passport}~help-closed`, {
    device: 'iphone', title: 'V1-07 · Persona can help dismissed',
    note: '✕ hides Persona can help for this to-do only. The page is now four things.',
    render: (ctx, d) => passportPaper(d, { help: false }),
  });

  /* ---------------------------------------------------------------- Can wait: Reply (the generic paper, focused) */

  frame(PH.insurance, {
    device: 'iphone', chrome: { keyboard: true }, title: 'V1-01 → Reply · Renew the car insurance',
    note: 'Reply on the Can wait row opens the generic paper with the composer focused: Persona asked in plain text and waits for a reply (Awaiting Reply). The bar keeps the title and the state word; the page shows its chips, notes and Persona’s question, and the composer answers it.',
    render: (ctx, d) => {
      const t = d.todo('t13');
      const stack = [`<div class="v1-question">${ui.face(22)}<div><div class="v1-question-w">Persona · ${t.trailing} ago</div><div class="v1-question-t">${t.question}</div></div></div>`];
      // A short generic paper reads from the top, as a page does: chips, notes, then Persona's question.
      const head = [chipsRow(t), ui.notes('', T('Opens: Notes')), stack.pop()];
      // The question is the card right above the composer, so the bar's subtitle keeps only the state word.
      // Awaiting Reply is listed under Can wait, not counted: it reads in the row's quiet tone, never the Needs you blue
      const sub = ui.stateLine(t.phase, '', { word: t.state.word, clamp: 1, tone: 'idle' });
      return foldedPage(d, t, { back: N(`${PH.list}~can-wait`, POP), convNav: T('Opens: the Conversation (pushed)'), moreNav: T('Opens: More'), head, stack, sub, composer: { placeholder: 'Reply to Persona…' } });
    },
  });

  /* ---------------------------------------------------------------- V1-08 lock screen, V1-09 the Assistant's tray */

  /** The Fleet Live Activity: it counts sessions that need the owner (the Agents count, Data.counts.fleet), shows at most
      two, and only the first actionable one carries buttons (a plan today: View · Approve); the rest folds to “+N”. */
  function fleetHtml(d, o) {
    const rows = [];
    d.todos.forEach((t) => (t.runs || []).filter((r) => r.session && r.state === 'needs').forEach((r) => {
      const ask = (t.asks || []).map((id) => d.ask(id)).find((a) => a.run === r.id);
      rows.push({ title: t.title, ask: ask ? `${r.title}: ${ask.title}` : r.title, plan: !!(ask && ask.kind === 'plan'), nav: N(PH.widget, FADE) });
    }));
    if (d.counts.elsewhere) rows.push({ title: d.elsewhere.title, ask: d.elsewhere.ask, tool: true, nav: T('Opens: Tidy the settings code in Agents') });
    const shown = rows.slice(0, 2);
    const more = rows.length - shown.length;
    // only the first actionable row carries buttons: a plan's View · Approve, a tool's Deny · Allow Once
    const buttons = (r, i) => (i !== 0 ? ''
      : r.plan ? `<span class="k-fleet-b"${A(o.view)}>View</span><span class="k-fleet-b is-rec"${A(o.approve)}>Approve</span>`
        : r.tool ? d.elsewhere.actions.map((x) => `<span class="k-fleet-b${x.primary ? ' is-rec' : ''}"${A(x.primary ? o.allow : o.deny)}>${x.label}</span>`).join('') : '');
    return `<div class="k-fleet v1-fleet">
      <div class="k-fleet-h">${ui.face(22)}<b>Persona</b><span class="k-sep">·</span><span>${needYou(d.counts.agentsBadge)}</span></div>
      ${shown.map((r, i) => `<div class="k-fleet-r"${A(r.nav)}><div class="k-fleet-t"><div class="k-fleet-ti">${r.title}</div><div class="k-fleet-q">${r.ask}</div></div>${buttons(r, i)}</div>`).join('')}
      ${more > 0 ? `<div class="k-fleet-more">+${more}</div>` : ''}
    </div>`;
  }
  function lockFrame(d) {
    const answered = !d.todo('t01').asks.length;
    // The expanded notification (long press): the platter, then its actions in their own rounded list under it, as iOS
    // draws them.
    const ask = d.ask('a1');
    const on = { hanami: N(`${PH.lock}~answered-from-lock`, FADE), kaede: T('Answers Kaede from the lock screen'), tsuki: T('Answers Tsuki from the lock screen') };
    const noti = answered ? '' : `<div class="v1-noti">${ui.notification({ title: ask.push.title, body: ask.push.body, expanded: false, go: PH.kyoto, transition: FADE })}
      <div class="v1-noti-acts">${ask.push.actions.map((x) => `<div class="v1-noti-a${x.recommended ? ' is-rec' : ''}"${A(on[x.option])}>${x.label}</div>`).join('')}</div></div>`;
    const fleet = d.counts.agentsBadge ? fleetHtml(d, {
      view: N(PH.widget, FADE), approve: N(`${PH.lock}~approved`, FADE),
      allow: T('Allows running the test suite once'), deny: T('Denied · Tidy the settings code carries on without the tests'),
    }) : '';
    return ui.lockScreen({ content: noti + fleet });
  }
  frame(PH.lock, {
    device: 'iphone', title: 'V1-08 · iPhone · the lock screen (S2 from outside the app)',
    note: 'The to-do’s own request arrives as the TODO_DECIDE push, shown expanded (the long-press state): the to-do’s title, the named request and the recommendation, then three actions, recommended first (proposed: today it has one action, Take Recommendation). Tap the body to open the paper with the card in view. Below, the Fleet Live Activity counts sessions that need the owner (as Agents does): at most two, only the first actionable one with buttons (a plan: View · Approve).',
    render: (ctx, d) => lockFrame(d),
  });
  frame(`${PH.lock}~answered-from-lock`, {
    device: 'iphone', title: 'V1-08 · answered from the lock screen', view: ANSWERED('hanami'),
    note: 'Ryokan Hanami (Recommended) answers from the lock screen: the notification goes. The Live Activity is unchanged, since it counts Agent sessions and the ryokan was the to-do’s own request.',
    render: (ctx, d) => lockFrame(d),
  });

  frame(`${PH.lock}~approved`, {
    device: 'iphone', title: 'V1-08 · Approve from the Live Activity', view: { approved: { a2: true } },
    note: 'Approve on the Live Activity approves Widget’s plan in place: its row goes and the header counts one session fewer (“1 needs you”). The tool approval elsewhere is now the first actionable row, so it carries the buttons: Deny · Allow Once.',
    render: (ctx, d) => lockFrame(d),
  });

  /** A tray row's line, drawn as the list's line 2: the phase mark, the request (or the row's lead) in its tone, then
      the rest in --ink-2 (a whole-line tone, such as Didn’t start, comes from the row). */
  function trayLine(dd, t, ask) {
    const r = t.row || {};
    const lead = ask && ask.kind === 'plan' ? `${dd.run(ask.run).title}: ${ask.title}` : r.lead || '';
    const text = r.text && (!ask || r.all) ? r.text : '';
    const leadHtml = lead ? `<span class="${r.tone && !r.all ? `k-tone--${r.tone} is-toned v1-nt-lead` : 'v1-nt-lead'}">${lead}</span>` : '';
    return `<span class="v1-nt-line">${ui.lineMark(r.mark, r.mark === 'needs' ? 7 : 12)}${leadHtml}${lead && text ? '<span class="k-sep">·</span>' : ''}${text ? `<span>${text}</span>` : ''}</span>`;
  }
  function trayFrame(d, o) {
    o = o || {};
    const c = d.counts;
    const seg = `<div class="v1-shell"><span class="is-on">Assistant</span><span${A(T('Opens: Agents'))}>Agents ${ui.badge(c.agentsBadge, { look: 'red' })}</span></div>`;
    const top = ui.navbar({ leading: [{ icon: 'line-3-horizontal', title: 'Show Sidebar', go: `${PH.tray}~drawer`, transition: FADE }], center: seg, trailing: [{ icon: 'square-pencil', title: 'New Chat', toast: 'Opens: New Chat' }] });
    // A message from 9:58 keeps its words: it was written over that moment's numbers, not the live ones.
    const chat = `<div class="v1-chat${o.dim === false ? '' : ' is-dim'}">
      ${ui.msg.day('Today 9:58')}
      ${ui.msg.user('What needs me this morning?')}
      ${ui.msg.persona(`${D0.counts.needs} to-dos need you, and ${D0.counts.elsewhere} Agent elsewhere is asking for something. They are all in Needs you below, so you can answer them there.`, { plain: true })}
    </div>`;
    const entries = `<div class="v1-entries">
      <span class="v1-entry${o.panel === 'needs' ? ' is-on' : ''}"${A(N(o.panel === 'needs' ? `${PH.tray}~closed` : PH.tray, FADE))}><span>Needs you</span>${ui.badge(c.assistantNeeds, { look: 'needs' })}</span>
      <span class="v1-entry${o.panel === 'progress' ? ' is-on' : ''}"${A(N(o.panel === 'progress' ? `${PH.tray}~closed` : `${PH.tray}~in-progress`, FADE))}><span>In progress</span><span class="v1-entry-n">${o.answeredRow ? D0.counts.inProgress : c.inProgress}</span></span>
    </div>`;
    const comp = ui.todoComposer({ to: false, placeholder: 'Ask Persona…', send: T('Sends to Persona') });
    let panel = '';
    if (o.panel === 'needs') {
      // The rows read as the list's line 2 does (the mark, the request in its tone), with one button each. While an answer
      // shows in place (4 s, “✓ You chose … · Undo”) the rows are the unchanged data's and the numbers the new view's.
      const src = o.answeredRow ? D0 : d;
      const on = { a1: N(`${PH.tray}~answered`, FADE), a2: N(PH.widget, PUSH), a3: N(`${PH.tray}~back`, FADE), t01: N(`${PH.kyoto}~from-assistant`, PUSH), t02: N(PH.release, PUSH), t03: T('Opens: Sort last year’s photos') };
      const rows = src.todos.filter((t) => t.section === 'needs').map((t) => {
        if (o.answeredRow && t.id === 't01') return { title: t.title, ask: `${I('checkmark', 12)} ${d.todo('t01').answered.text}<span class="k-sep">·</span><span class="v1-undo"${A(N(PH.tray, FADE))}>Undo</span>`, tone: 'positive', buttons: [] };
        const ask = src.ask(t.asks[0]);
        const button = ask.kind === 'plan' ? Object.assign({ label: 'Review', door: true }, on.a2) : Object.assign({ label: t.answer.label, primary: true }, on[ask.id]);
        return Object.assign({ title: t.title, ask: trayLine(src, t, ask), tone: t.row.all ? t.row.tone : '', buttons: [button] }, on[t.id]);
      });
      const e = d.elsewhere;
      const groups = [{ title: `To-dos · ${c.needs}`, rows }];
      if (c.elsewhere) {
        groups.push({ title: `Elsewhere · ${c.elsewhere}`, rows: [{ title: e.title, ask: e.ask, buttons: e.actions.map((x) => Object.assign({ label: x.label, primary: !!x.primary }, x.primary ? N(`${PH.tray}~allowed`, FADE) : T('Denied · Tidy the settings code carries on without the tests'))) }] });
      }
      panel = ui.needsTray({ header: `Needs you · ${c.assistantNeeds}`, groups });
    } else if (o.panel === 'progress') {
      const g = (sec, title, counted) => {
        const rows = d.todos.filter((t) => t.section === sec).map((t) => Object.assign({ title: t.title, ask: trayLine(d, t), buttons: [] }, T(`Opens: ${t.title}`)));
        // Just finished is listed at the foot without a number: In progress counts Working + Waiting (#8237)
        return { title: counted ? `${title} · ${rows.length}` : title, rows };
      };
      panel = ui.needsTray({ header: `In progress · ${c.inProgress}`, groups: [g('working', d.words.working, true), g('waiting', d.words.waiting, true), g('just_finished', d.words.ready, false)] });
    }
    const overlay = (panel ? `<div class="v1-tray">${panel}</div>` : '') + (o.drawer ? ui.drawer({ scrimNav: N(PH.tray, FADE), content: tree(d, { selected: 'persona', go: { todos: N(PH.list, FADE) } }) }) : '') + (o.toast || '');
    return ui.page({ top, body: chat, bottom: entries + comp, overlay });
  }
  frame(PH.tray, {
    device: 'iphone', title: 'V1-09 · iPhone · the Assistant’s Needs you tray (S7)',
    note: 'The main chat with Needs you open (#8237’s two entries, unchanged). The tray groups its rows “To-dos · N” and “Elsewhere · M”, so the parts add up to the header’s number in front of the owner; the shell switch shows Agents with its own count of sessions. A to-do row answers in place, or opens its paper pushed on the Assistant stack.',
    render: (ctx, d) => trayFrame(d, { panel: 'needs' }),
  });
  frame(`${PH.tray}~answered`, {
    device: 'iphone', title: 'V1-09 · answered in the tray', view: ANSWERED('hanami'),
    note: 'OK: Ryokan Hanami answers in place: the row reads “✓ You chose Ryokan Hanami · Undo” for 4 s, while the header and the To-dos group already count one fewer, the same moment as everywhere else.',
    render: (ctx, d) => trayFrame(d, { panel: 'needs', answeredRow: true }),
  });
  frame(`${PH.tray}~back`, {
    device: 'iphone', title: 'V1-09 · Start When It’s Back in the tray', view: { a3: 'back' },
    note: 'Start When It’s Back moves the photos to Waiting; the tray drops it.',
    render: (ctx, d) => trayFrame(d, { panel: 'needs' }),
  });
  frame(`${PH.tray}~allowed`, {
    device: 'iphone', title: 'V1-09 · Allow Once (Elsewhere)', view: { elsewhere: 'allowed' },
    note: 'Allow Once answers the Agent opened by hand; the Elsewhere group goes and the header counts the To-dos alone.',
    render: (ctx, d) => trayFrame(d, { panel: 'needs' }),
  });
  frame(`${PH.tray}~in-progress`, {
    device: 'iphone', title: 'V1-09 · the In progress entry',
    note: 'In progress (#8237): Working and Waiting, whose sum is the number on the entry, then Just finished as an uncounted group at its foot.',
    render: (ctx, d) => trayFrame(d, { panel: 'progress' }),
  });
  frame(`${PH.tray}~closed`, {
    device: 'iphone', title: 'V1-09 · the chat with both entries closed',
    note: 'The two entries sit above the Assistant’s composer: Needs you with the blue count, In progress with a neutral one.',
    render: (ctx, d) => trayFrame(d, { dim: false }),
  });
  frame(`${PH.tray}~drawer`, {
    device: 'iphone', title: 'V1-09 · the drawer from the chat',
    note: 'The same tree as in To-dos: the To-dos row shows N in blue, the Pinned direction its own count.',
    render: (ctx, d) => trayFrame(d, { dim: false, drawer: true }),
  });

  /* ================================================================== iPhone Duo */

  const DUO = {
    outer: 'duo-outer/conversation-kyoto-keyboard', outerWidget: 'duo-outer/release-widget-session',
    lp: 'duo-inner-portrait/list-paper', pc: 'duo-inner-portrait/paper-conversation', land: 'duo-inner-landscape/release-pane-on-widget',
  };
  const duoTabs = (d) => ({ selected: 0, agentsCount: d.counts.agentsBadge });
  /**
   * The Duo side column (E4): Back alone on top, a 48-pt glass circle; the page's other items 12 pt below it as 48-pt
   * circles with symbols only (the name is the accessibility label), in one shared glass 4 pt apart; the Duo tab bar and
   * the face stand at the foot. The kit passes markup strings through, so V1 draws the items itself (an unbuilt item
   * then shows its toast too). o: { back: nav, items: [{ icon, label, nav, active, dot }], foot }
   */
  function duoSide(d, o) {
    const back = o.back ? `<span class="v1-sb"${A(o.back)} title="Back" aria-label="Back">${I('chevron-left', 22)}</span>` : '';
    const items = (o.items || []).map((it) => `<span class="v1-si${it.active ? ' is-active' : ''}"${A(it.nav)} title="${it.label}" aria-label="${it.label}">${I(it.icon, 21)}${it.dot ? '<i class="v1-si-dot"></i>' : ''}</span>`).join('');
    // the shell rail at the foot on both displays: New Chat, then the face at the very foot (§3.0.2)
    return { actions: [back, items ? `<span class="v1-sg">${items}</span>` : ''].filter(Boolean), tabs: duoTabs(d), foot: o.foot || RAIL_FOOT };
  }
  // The column's foot lists its items top to bottom (the kit stacks the last one lowest).
  const RAIL_FOOT = [{ icon: 'square-pencil', label: 'New Chat' }, { face: true, label: 'Persona' }];

  /** The Duo inner portrait keyboard as two halves broken at the crease (x = 334.5, a 12-pt gutter): q–t | y–p, a–g | h–l,
      ⇧ z–v | b–m ⌫, 123 and space | space and return; one suggestion and the size label on the left, two on the right.
      The frame hides the device's one-piece keyboard (cls v1-splitkb) and keeps its height for the safe area. */
  function splitKeyboard() {
    const key = (t, cls) => `<span class="k-key${cls ? ' ' + cls : ''}">${t}</span>`;
    const keys = (str) => str.split('').map((c) => key(c)).join('');
    const half = (side, sugg, rows, foot) => `<div class="k-kb v1-kb-half is-${side}"><div class="k-kb-sugg">${sugg}</div><div class="k-kb-rows">${rows.map((r) => `<div class="k-kb-row${r[1] ? ' ' + r[1] : ''}">${r[0]}</div>`).join('')}</div><div class="k-kb-foot">${foot}</div></div>`;
    const left = half('l', '<span>“onsen”</span><i></i><span class="k-kb-label">keyboard ≈ 300 pt (to measure)</span>', [
      [keys('qwert')], [keys('asdfg'), 'v1-kb-mid'], [key(I('shift', 20), 'is-fn is-wide') + keys('zxcv')], [key('123', 'is-fn is-wider') + key('', 'is-space')],
    ], I('face-smile', 25));
    const right = half('r', '<span>only</span><i></i><span>one</span>', [
      [keys('yuiop')], [keys('hjkl'), 'v1-kb-mid'], [keys('bnm') + key(I('delete-left', 21), 'is-fn is-wide')], [key('', 'is-space') + key('return', 'is-fn is-wider')],
    ], I('mic', 23));
    return `<div class="v1-kb2">${left}${right}</div>`;
  }

  /* ---------------------------------------------------------------- V1-10 outer display */

  frame(DUO.outer, {
    device: 'duo-outer', chrome: { keyboard: true }, unfold: DUO.pc,
    title: 'V1-10 · Duo outer · the Conversation, keyboard up (S9 starts)',
    note: 'Content is 382 wide; the page’s Back (alone) and More (a symbol-only circle 12 pt below it) stand at the top of the side column, the Duo tab bar and the face at its foot. No toolbar in the content: the pinned header is the first thing in it and keeps the title, “● Needs you · Which ryokan?” and the answers; about 190 pt of thread shows above the composer (keyboard ≈ 300, to measure). Paper ⇄ conversation cross-fade in place; nothing slides. Unfold keeps the draft and its caret.',
    render: (ctx, d) => {
      const t = d.todo('t01');
      const header = askHeader(d, t, { back: false, more: false, ok: Object.assign({ label: t.answer.short }, T('Answers Ryokan Hanami')), options: T('Opens: the full request card') });
      const body = ui.thread(t.rounds, { anchor: 'bottom' });
      const comp = composer(t, { text: t.drafts.duo, inset: 8, send: T('Sends the message to Persona') });
      return {
        content: ui.page({ top: header, body, pad: false, bottom: comp, cls: 'v1-duo-outer' }),
        side: duoSide(d, { back: N(`${DUO.outer}~paper`, FADE), items: [{ icon: 'ellipsis', label: 'More', nav: T('Opens: Copy Link, Chat Topic, Decisions') }] }),
      };
    },
  });
  frame(`${DUO.outer}~paper`, {
    device: 'duo-outer', unfold: DUO.lp, title: 'V1-10 · Duo outer · the paper',
    note: 'The paper at 382 wide, its controls in the side column: Back alone, then Conversation (with the blue unread dot) and More as symbol-only circles in one glass. Conversation cross-fades to the pinned Conversation in place (0.2 s).',
    render: (ctx, d) => {
      const t = d.todo('t01');
      const content = paperPage(d, t, {
        top: '<div class="v1-duo-top"></div>',
        paper: { askOn: { hanami: T('Answers Ryokan Hanami'), kaede: T('Answers Kaede'), tsuki: T('Answers Tsuki'), later: T('Later · folds to one line'), dont: T('Opens: the confirmation for Don’t handle'), discuss: N(DUO.outer, FADE) }, allNav: N(DUO.outer, FADE), roundNav: N(DUO.outer, FADE) },
        composer: { inset: 8 }, composerNav: N(DUO.outer, FADE),
      });
      return {
        content,
        side: duoSide(d, { back: T('Opens: All To-dos (the list)'), items: [
          { icon: 'bubble-left-right', label: 'Conversation', nav: N(DUO.outer, FADE), dot: true },
          { icon: 'ellipsis', label: 'More', nav: T('Opens: Copy Link, Move to Project, Someday, Decisions, Delete') },
        ] }),
      };
    },
  });
  frame(DUO.outerWidget, {
    device: 'duo-outer', unfold: DUO.land, title: 'V1-13 → Duo outer · the Widget run (folded)',
    note: 'Folding the open Duo on the Widget run keeps it in front: the pinned header names the to-do and points down to the plan, its home. Unfold returns to the side-by-side pane.',
    render: (ctx, d) => ({
      content: widgetPhone(d, { back: false, inset: 8 }),
      side: duoSide(d, { back: T('Opens: Release Weather App 1.3 (the paper)'), items: [{ icon: 'ellipsis', label: 'More', nav: T('Opens: Open Full Session, Try Another Way, Stop') }] }),
    }),
  });

  /* ---------------------------------------------------------------- V1-11 inner portrait: list ┊ paper */

  function duoList(d, o) {
    return listPane(d, Object.assign({
      sel: 't01', menuBtn: N(`${DUO.lp}~sidebar`, FADE), titleNav: null,
      go: (t) => ({ t01: N(DUO.lp, NONE), t02: N(`${DUO.lp}~release`, NONE) }[t.id] || T(`Opens: ${t.title} beside the list`)),
      ans: (t) => (t.id === 't01' ? Object.assign({ label: t.answer.label }, T('Answers Ryokan Hanami'), { options: T('Opens: the card in place') }) : t.id === 't02' ? T('Opens: the Widget run in the right half') : T(`Opens: ${t.title}`)),
      foldNav: { can_wait: T('Opens: Can wait') },
    }, o || {}));
  }
  function duoPaper(d, t, o) {
    o = o || {};
    return paperPage(d, t, Object.assign({
      leading: o.leading || [], convNav: o.convNav, dot: o.dot, moreNav: T('Opens: Copy Link, Move to Project, Someday, Decisions, Delete'),
      paper: Object.assign({ narrow: true, stepsMode: 'summary', stepsNav: T('Opens: Steps'), convCount: 1, recap: false }, o.paper || {}),
      // a 335-pt half: with the steward's chip (“To Release lead ▾”) the field keeps its room and dictation stays on the keyboard
      composer: { mic: !(t.composer && t.composer.to && t.composer.to.menu) },
      composerNav: o.composerNav || T('Opens: the composer, focused'),
    }, o.page || {}));
  }

  frame(DUO.lp, {
    device: 'duo-inner-portrait', important: true, fold: `${DUO.outer}~paper`,
    title: 'V1-11 · Duo inner portrait · list ┊ paper at the crease',
    note: 'List 334 ┊ paper 335, split at the crease (x = 334.5, nothing straddles it): this needs the 320-pt paper minimum (owner decision 2). The sidebar is hidden; ☰ lays it over the left half. The primary answer drops “· Recommended” where it would not fit (measured once). ◫ cross-fades each half (list → paper, paper → conversation); another row swaps the paper, not animated.',
    render: (ctx, d) => {
      const t = d.todo('t01');
      return ui.split([
        { w: 334, html: duoList(d) },
        { w: 335, html: duoPaper(d, t, { convNav: N(DUO.pc, FADE), dot: true, paper: { askOn: { hanami: T('Answers Ryokan Hanami'), kaede: T('Answers Kaede'), tsuki: T('Answers Tsuki'), later: T('Later · folds to one line'), dont: T('Opens: the confirmation for Don’t handle'), discuss: N(DUO.pc, FADE) }, allNav: N(DUO.pc, FADE), roundNav: N(DUO.pc, FADE) } }) },
      ]);
    },
  });
  frame(`${DUO.lp}~release`, {
    device: 'duo-inner-portrait', title: 'V1-11 · another row selected',
    note: 'Selecting Release Weather App 1.3 swaps the paper, not animated; the selection bar moves. Its door (Review Plan ›) opens the Widget run in the right half.',
    render: (ctx, d) => {
      const t = d.todo('t02');
      return ui.split([
        { w: 334, html: duoList(d, { sel: 't02' }) },
        { w: 335, html: duoPaper(d, t, { convNav: T('Opens: Release lead’s conversation in the right half'), paper: { askOn: { door: T('Opens: the Widget run in the right half'), later: T('Later'), dont: T('Opens: the confirmation for Don’t handle') }, stepNav: (s) => T(`Opens: the ${s.text} session`), workRows: workRowsNav, workNav: T('Opens: Work details') } }) },
      ]);
    },
  });
  frame(`${DUO.lp}~sidebar`, {
    device: 'duo-inner-portrait', title: 'V1-11 · ☰ the sidebar over the left half', fold: `${DUO.outer}~paper`,
    note: 'The sidebar lies over the left half (its rows at the 320-pt metrics, the half filled up to the crease) while To-dos is in front; the right half dims 0.2. It stays left of the crease; a pick slides it away.',
    render: (ctx, d) => {
      const t = d.todo('t01');
      const html = ui.split([{ w: 334, html: duoList(d) }, { w: 335, html: duoPaper(d, t, { dot: true }) }]);
      // the sidebar takes the whole left half, up to the crease (no dimmed sliver of the list beside it)
      return html + ui.drawer({ width: 334, scrimNav: N(DUO.lp, FADE), content: tree(d, { width: 334, go: { todos: N(DUO.lp, FADE) } }) });
    },
  });

  /* ---------------------------------------------------------------- V1-12 inner portrait: paper ┊ conversation */

  function duoPC(d, o) {
    o = o || {};
    const t = d.todo('t01');
    const over = o.listOver ? `<div class="v1-over-scrim"${A(N(DUO.pc, FADE))}></div><div class="v1-over v1-over--half">${duoList(d, { menuBtn: null, go: (x) => (x.id === 't01' ? N(DUO.pc, FADE) : T(`Opens: ${x.title} beside its conversation`)) })}</div>` : '';
    const paper = duoPaper(d, t, {
      leading: [backCapsule('List', N(`${DUO.pc}~list-over`, FADE))], convNav: N(DUO.lp, FADE),
      paper: { compactAsk: true, conv: 'pointer', notes: false, brief: false, askOn: { hanami: T('Answers Ryokan Hanami'), kaede: T('Answers Kaede'), tsuki: T('Answers Tsuki'), later: T('Later · folds to one line'), dont: T('Opens: the confirmation for Don’t handle'), discuss: T('Puts “Answer: Which ryokan?” in the composer beside') } },
      page: { composer: false, convActive: true, overlay: over },
    });
    const pane = stewardPane(d, t, {
      close: N(DUO.lp, FADE), thread: { days: true },
      extra: [], composer: { text: t.drafts.duo, send: T('Sends the message to Persona') },
    });
    // the keyboard rises under both halves in two blocks broken at the crease, so no key straddles it
    return { content: ui.split([{ w: 334, html: paper }, { w: 335, html: pane }]) + splitKeyboard(), cls: 'v1-splitkb' };
  }
  frame(DUO.pc, {
    device: 'duo-inner-portrait', important: true, chrome: { keyboard: true }, fold: DUO.outer,
    title: 'V1-12 · Duo inner portrait · paper ┊ conversation (S9: unfolded mid-conversation)',
    note: 'Paper 334 ┊ conversation 335: the list stepped aside (‹ List). The paper keeps its card (the pane shows the steward’s conversation, not the request’s home); its Conversation block is the one line “Conversation · open beside →”; it has no composer, which moved into the pane with the draft and its caret. The keyboard rises under both halves; title, state and request stay visible on the left. Fold returns to the outer display with the draft intact.',
    render: (ctx, d) => duoPC(d),
  });
  frame(`${DUO.pc}~list-over`, {
    device: 'duo-inner-portrait', chrome: { keyboard: true }, fold: DUO.outer, title: 'V1-12 · ‹ List over the left half',
    note: '‹ List slides the list over the left half from the leading edge with the paper dimmed; a pick swaps the paper and slides the list away. Nothing crosses the crease.',
    render: (ctx, d) => duoPC(d, { listOver: true }),
  });

  /* ---------------------------------------------------------------- V1-13 inner landscape: paper │ pane on the Widget run */

  function landFrame(d, o) {
    o = o || {};
    const t = d.todo('t02');
    const run = d.run('widget');
    const approved = run.state !== 'needs';
    const closed = o.mode === 'closed';
    const steward = o.mode === 'steward';
    const base = DUO.land;
    const paper = paperPage(d, t, {
      top: '<div class="v1-duo-top"></div>',
      paper: {
        // one rule at the same moment: Since you looked shows until the plan is approved, whichever channel the pane shows
        ask: closed || steward ? undefined : 'pointer', recap: !approved,
        askOn: { door: N(base, NONE), later: T('Later'), dont: T('Opens: the confirmation for Don’t handle') },
        stepNav: (s) => (s.run === 'widget' ? N(base, NONE) : T(`Shows the ${s.text} session in the pane`)),
        conv: steward ? 'pointer' : undefined, allNav: N(`${base}~steward`, NONE), roundNav: N(`${base}~steward`, NONE),
        workRows: workRowsNav, workNav: T('Opens: Work details'),
      },
      composer: closed ? {} : false, composerNav: T('Opens: the composer, focused'),
    });
    const menuNavs = { steward: N(`${base}~steward`, NONE), widget: N(base, NONE) };
    let pane = '';
    if (!closed) {
      if (steward) pane = stewardPane(d, t, { close: N(`${base}~closed`, FADE), switchNav: N(`${base}~channels-steward`, NONE), menuOpen: o.menu ? channelMenu(d, 'steward', menuNavs) : '' });
      else pane = widgetPane(d, { close: N(`${base}~closed`, FADE), switchNav: N(o.menu ? base : `${base}~channels`, NONE), menuOpen: o.menu ? channelMenu(d, 'widget', menuNavs) : '', approve: N(`${base}~approved`, FADE) });
    }
    const content = closed
      ? ui.split([{ w: 320, html: listPane(d, { sel: 't02', go: (x) => (x.id === 't02' ? N(`${base}~closed`, NONE) : T(`Opens: ${x.title}`)), ans: (x) => (x.id === 't02' ? N(base, FADE) : T(`Opens: ${x.title}`)), foldNav: { can_wait: T('Opens: Can wait') } }) }, { w: 547, html: paper, line: true }])
      : ui.split([{ w: 467, html: paper }, { w: 400, html: pane, line: true }]);
    const overlay = o.sidebar ? ui.drawer({ width: 320, scrimNav: N(base, FADE), content: tree(d, { go: { todos: N(`${base}~closed`, FADE) } }) }) : '';
    return {
      content: content + overlay,
      // the tabletop fold's dashed hint shows in the margins at each pane edge and in the side column, never across text
      cls: closed ? 'v1-fold-320' : 'v1-fold-467',
      side: duoSide(d, {
        items: [
          { icon: 'sidebar-left', label: 'Sidebar', nav: N(o.sidebar ? base : `${base}~sidebar`, FADE), active: !!o.sidebar },
          { icon: 'bubble-left-right', label: 'Conversation', active: !closed, nav: N(closed ? base : `${base}~closed`, FADE) },
          { icon: 'ellipsis', label: 'More', nav: T('Opens: Copy Link, Move to Project, Someday, Decisions, Delete') },
        ],
        foot: RAIL_FOOT,
      }),
    };
  }
  frame(DUO.land, {
    device: 'duo-inner-landscape', fold: DUO.outerWidget,
    title: 'V1-13 · Duo inner landscape · paper │ pane on the Widget run',
    note: 'Paper 467 │ conversation 400 │ side column 84. The list stepped aside; the sidebar lies over while To-dos is in front. Drawn once: the pane shows the Widget run, the plan’s home, so the paper’s door shrinks to “Plan awaiting approval · answer in the conversation →”. The paper keeps its Conversation block (the pane is the run, not the to-do’s conversation); All 2 › switches the pane to the steward. The pane has no system bar: its switcher and ✕ sit inside it.',
    render: (ctx, d) => landFrame(d),
  });
  frame(`${DUO.land}~approved`, {
    device: 'duo-inner-landscape', fold: DUO.outerWidget, title: 'V1-13 · approved in the pane', view: { approved: { a2: true } },
    note: 'Approve: the plan resolves, the pointer disappears and the state reads “Working · 2 working · 1 done”; the Agents count in the tab bar drops too.',
    render: (ctx, d) => landFrame(d),
  });
  frame(`${DUO.land}~channels`, {
    device: 'duo-inner-landscape', fold: DUO.outerWidget, title: 'V1-13 · the channel switcher',
    note: 'Widget ▾ opens the channels: Release lead (the conversation) · Widget · Release notes · Screenshots, each by title, role and state. Switching is not animated.',
    render: (ctx, d) => landFrame(d, { menu: true }),
  });
  frame(`${DUO.land}~steward`, {
    device: 'duo-inner-landscape', fold: DUO.outerWidget, title: 'V1-13 · the pane on Release lead',
    note: 'On the steward channel the door comes back in full (the pane no longer shows its home) and the paper’s Conversation block becomes the one-line pointer.',
    render: (ctx, d) => landFrame(d, { mode: 'steward' }),
  });
  frame(`${DUO.land}~channels-steward`, {
    device: 'duo-inner-landscape', fold: DUO.outerWidget, title: 'V1-13 · the switcher on Release lead',
    note: 'Release lead ▾: the same menu, Release lead checked.',
    render: (ctx, d) => landFrame(d, { mode: 'steward', menu: true }),
  });
  frame(`${DUO.land}~closed`, {
    device: 'duo-inner-landscape', fold: DUO.outerWidget, title: 'V1-13 · the pane closed',
    note: '✕ (or the Conversation item in the side column): list 320 │ paper 547, cross-faded in place. The door is back on the paper with Review Plan ›.',
    render: (ctx, d) => landFrame(d, { mode: 'closed' }),
  });
  frame(`${DUO.land}~sidebar`, {
    device: 'duo-inner-landscape', fold: DUO.outerWidget, title: 'V1-13 · the sidebar over the page',
    note: 'Sidebar in the side column lays the sidebar (320) over the page while To-dos is in front (owner decision 2).',
    render: (ctx, d) => landFrame(d, { sidebar: true }),
  });

  /* ================================================================== iPad */

  const IP = { closed: 'ipad11-landscape/closed', open: 'ipad11-landscape/open', sum: 'ipad13-landscape/summary', p13: 'ipad13-landscape/paper', o13: 'ipad13-landscape/open', pc: 'ipad11-portrait/closed', po: 'ipad11-portrait/open' };

  /** The paper pane on iPad: “Conversation” (title + symbol) and ⋯ on its bar. */
  function ipadPaper(d, t, o) {
    o = o || {};
    return paperPage(d, t, Object.assign({
      leading: o.leading || [], convLabel: true, convNav: o.convNav, convActive: o.convActive,
      moreNav: T('Opens: Copy Link, Move to Project, Someday, Decisions, Chat Topic, Delete'),
      paper: Object.assign({ compactSteps: true }, o.paper || {}),
      composerNav: T('Opens: the composer, focused'),
      composer: o.composer, wc: o.wc,
    }, o.page || {}));
  }

  const releasePaperOpts = (base, o) => Object.assign({
    askOn: { door: N(o.doorTo, OPEN), later: T('Later · it stays in Needs you'), dont: T('Opens: the confirmation for Don’t handle') },
    stepNav: (s) => (s.run === 'widget' ? N(o.stepWidget || o.doorTo, o.stepT || OPEN) : T(`Shows the ${s.text} session in the pane`)),
    allNav: N(o.allTo || base, o.allT || OPEN), roundNav: N(o.allTo || base, o.allT || OPEN), workRows: workRowsNav, workNav: T('Opens: Work details'),
  }, o.paper || {});

  /* ---------------------------------------------------------------- V1-14 iPad 11 landscape, closed */

  function ipadSidebarGo(base) { return { todos: N(base, NONE), today: N(`${base}~today`, NONE), kyoto: N(`${base}~kyoto-place`, NONE) }; }

  function ipadClosed(d, o) {
    o = o || {};
    const base = IP.closed;
    const place = o.place || 'all';
    const sel = o.sel === undefined ? 't02' : o.sel;
    const list = listPane(d, {
      place, sel, emptyLink: false,
      go: (t) => ({ t01: N(`${base}~kyoto`, NONE), t02: N(base, NONE) }[t.id] || T(`Opens: ${t.title} in the paper region`)),
      ans: (t) => (t.id === 't02' ? N(`${IP.open}~widget`, OPEN) : t.id === 't01' ? Object.assign({ label: t.answer.label }, T('Answers Ryokan Hanami'), { options: N(`${base}~kyoto`, NONE) }) : T(`Opens: ${t.title}`)),
      foldNav: { can_wait: T('Opens: Can wait') },
    });
    let paper;
    if (sel === 't02') {
      // Since you looked stays until the paper has been seen: after approving in the pane it has, so it goes (as on the phone).
      const seen = d.run('widget').state !== 'needs';
      paper = ipadPaper(d, d.todo('t02'), { convNav: N(o.approvedPane ? `${IP.open}~widget-approved` : `${IP.open}~before-send`, OPEN), paper: releasePaperOpts(IP.open, { doorTo: `${IP.open}~widget`, allTo: `${IP.open}~before-send`, paper: { recap: !seen } }) });
    } else if (sel === 't01') {
      paper = ipadPaper(d, d.todo('t01'), { convNav: T('Opens the Conversation pane beside the paper'), paper: { askOn: { hanami: T('Answers Ryokan Hanami'), kaede: T('Answers Kaede'), tsuki: T('Answers Tsuki'), later: T('Later'), dont: T('Opens: the confirmation'), discuss: T('Opens the pane with “Answer: Which ryokan?”') }, allNav: T('Opens the Conversation pane') } });
    } else if (place !== 'today') {
      paper = ui.page({ body: placeSummary(d, place, { go: { needs: N(`${base}~kyoto`, NONE) } }), pad: false });
    } else {
      // Today with nothing due: the paper region still says where the place stands and what comes next.
      const sum = ui.placeSummary({ title: d.place('today').title, sub: 'Nothing due today', lines: [Object.assign({ count: d.thisWeek(), label: 'coming up this week' }, T('Opens: Upcoming'))] });
      paper = ui.page({ body: sum, pad: false });
    }
    return ui.split([
      { w: 400, html: tree(d, { selected: place === 'all' ? 'todos' : place, go: ipadSidebarGo(base) }) },
      { w: 320, html: list, line: true },
      { w: 474, html: paper, line: true },
    ]);
  }
  frame(IP.closed, {
    device: 'ipad11-landscape', important: true, title: 'V1-14 · iPad 11″ landscape · sidebar │ list │ paper (Release Weather App 1.3)',
    note: 'Three regions: sidebar 400 │ list 320 │ paper 474. The To-dos row is shaded (All To-dos in front) with its children; the Pinned direction keeps its own row under Persona Chats. “Conversation” (title + symbol) opens the pane: one 0.3 s transition, the sidebar leaves to the leading edge while the pane enters from the trailing edge and list + paper shift −400 pt without changing width. Review Plan › opens the pane on the Widget run.',
    render: (ctx, d) => ipadClosed(d),
  });
  frame(`${IP.closed}~kyoto`, {
    device: 'ipad11-landscape', title: 'V1-14 · another row selected',
    note: 'A selection change, not animated: the row highlight moves and the paper swaps to Plan the Kyoto trip, its card at home.',
    render: (ctx, d) => ipadClosed(d, { sel: 't01' }),
  });
  frame(`${IP.closed}~kyoto-place`, {
    device: 'ipad11-landscape', title: 'V1-14 · the place Kyoto trip',
    note: 'Sidebar “Kyoto trip”: a root swap, not animated. The list title becomes the place, with only its to-dos; with nothing selected the paper region shows the place summary. A to-do captured here lands in Kyoto trip.',
    render: (ctx, d) => ipadClosed(d, { place: 'kyoto', sel: null }),
  });
  frame(`${IP.closed}~today`, {
    device: 'ipad11-landscape', title: 'V1-14 · the place Today',
    note: 'Sidebar “Today”: a root swap. Nothing is due today in the mock, so Today shows its own empty state and points to what is coming this week.',
    render: (ctx, d) => ipadClosed(d, { place: 'today', sel: null }),
  });
  frame(`${IP.closed}~approved`, {
    device: 'ipad11-landscape', title: 'V1-14 · back after approving', view: { approved: { a2: true } },
    note: 'After the plan is approved in the pane and the pane is closed: the door is gone, the row re-filed into Working once the list was idle, and N dropped in the sidebar, the bar and the header at once.',
    render: (ctx, d) => ipadClosed(d),
  });

  /* ---------------------------------------------------------------- V1-15 iPad 11 landscape, open (S5) */

  function ipadOpen(d, o) {
    o = o || {};
    const base = IP.open;
    const t = d.todo('t02');
    const stage = o.stage === undefined ? 'delivered' : o.stage;
    const widget = o.channel === 'widget';
    const list = listPane(d, {
      sel: 't02', menuBtn: N(`${base}~sidebar-over`, FADE),
      go: (x) => (x.id === 't02' ? N(base, NONE) : T(`Opens: ${x.title} with the pane kept open`)),
      ans: (x) => (x.id === 't02' ? N(`${base}~widget`, NONE) : T(`Opens: ${x.title}`)),
      foldNav: { can_wait: T('Opens: Can wait') },
    });
    // The paper's scope line is the short one (“+ home-screen widget”): the pane beside it says it in full with the one
    // Undo, so the paper's line is plain text while the round shows (one control per action on screen).
    const scope = !widget && stage ? s5Scope(d, stage, null, true) : '';
    const paper = ipadPaper(d, t, {
      convNav: N(o.closeTo || IP.closed, CLOSE), convActive: true,
      paper: releasePaperOpts(base, {
        doorTo: `${base}~widget`, stepWidget: `${base}~widget`, stepT: NONE, allTo: base, allT: NONE,
        paper: { ask: widget ? (d.run('widget').state === 'needs' ? 'pointer' : undefined) : undefined, conv: widget ? undefined : 'pointer', scope, recap: !o.approved },
      }),
      composer: false,
    });
    let pane;
    const menuNavs = { steward: N(base, NONE), widget: N(`${base}~widget`, NONE) };
    if (widget) {
      pane = widgetPane(d, { close: N(o.closeTo || IP.closed, CLOSE), switchNav: N(o.menu ? `${base}~widget` : `${base}~channels-widget`, NONE), menuOpen: o.menu ? channelMenu(d, 'widget', menuNavs) : '', approve: N(`${base}~widget-approved`, FADE), undo: N(`${base}~widget`, FADE) });
    } else {
      // The draft stays in the field until the hub has it (Processing), as on the phone.
      const comp = stage === 'before' ? { text: t.drafts.s5, send: N(`${base}~sent`, NONE) }
        : stage === 'sending' ? { text: t.drafts.s5, caret: false } : { send: T('Sends the message to Release lead') };
      const s5 = stage && stage !== 'before' ? ui.rline.day('Today') + s5RoundHtml(d, stage, { sendNow: N(`${base}~send-now`, NONE), undo: N(`${base}~undone`, NONE) }) : '';
      const body = threadHtml(t.rounds, { tail: s5 });
      pane = stewardPane(d, t, {
        close: N(IP.closed, CLOSE), body, switchNav: N(o.menu ? base : `${base}~channels`, NONE), menuOpen: o.menu ? channelMenu(d, 'steward', menuNavs) : '',
        composer: Object.assign({ to: Object.assign({}, t.composer.to, T('Opens: the addressee menu')) }, comp),
      });
    }
    const over = o.sidebarOver ? ui.drawer({ width: 400, scrimNav: N(base, FADE), content: tree(d, { go: { todos: N(base, FADE) } }) }) : '';
    return ui.split([{ w: 320, html: list }, { w: 474, html: paper, line: true }, { w: 400, html: pane, line: true }]) + over;
  }
  frame(IP.open, {
    device: 'ipad11-landscape', important: true, title: 'V1-15 · iPad 11″ landscape · list │ paper │ conversation (S5)',
    note: 'The sliding window: list 320 │ paper 474 (the same width as before) │ conversation 400; the sidebar stepped aside (transient; ☰ lays it over the list). The paper’s Conversation block is the pointer “Conversation · open beside →”, so the conversation is never drawn twice; the Widget door stays, because the pane shows the steward, not Widget’s session. S5 in the pane: Delivered to Release lead, its reply, “↗ Queued for Release notes · after this step [Send Now]”, and the scope line with Undo, which also appears under What you asked.',
    render: (ctx, d) => ipadOpen(d),
  });
  frame(`${IP.open}~before-send`, {
    device: 'ipad11-landscape', title: 'V1-15 · before sending',
    note: 'The composer, now in the pane, holds “also add the home-screen widget to the release notes”. Focusing it never changes the layout.',
    render: (ctx, d) => ipadOpen(d, { stage: 'before' }),
  });
  const openChain = [['sent', 'sending', 150], ['processing', 'processing', 800], ['delivered', 'delivered', 6000], ['taken', 'taken', 0]];
  openChain.forEach(([st, stage, after], i) => {
    const next = openChain[i + 1];
    frame(`${IP.open}~${st}`, {
      device: 'ipad11-landscape', title: `V1-15 · S5 ${st}`,
      note: {
        sent: 'Sending: the round appears in the pane, faint.', processing: 'Processing: the hub stored it.',
        delivered: 'Delivered to Release lead; the reply and the queued relay with Send Now; Scope with Undo (the same picture as the frame, as part of the timed walk). After 6 s Release notes takes it.',
        taken: 'Taken: “↗ Delivered to Release notes” and the run line “◐ Release notes · adding the widget”.',
      }[st],
      auto: next ? { go: `${IP.open}~${next[0]}`, after, transition: NONE } : undefined,
      render: (ctx, d) => ipadOpen(d, { stage }),
    });
  });
  frame(`${IP.open}~send-now`, {
    device: 'ipad11-landscape', title: 'V1-15 · Send Now',
    note: 'Send Now, on a run that can take it, delivers at once; the relay line says so and the run line follows.',
    render: (ctx, d) => ipadOpen(d, { stage: 'now' }),
  });
  frame(`${IP.open}~undone`, {
    device: 'ipad11-landscape', title: 'V1-15 · scope undone',
    note: 'Undo on the scope line removes the scope entry (on the paper too) and posts “Scope change undone” in the round.',
    render: (ctx, d) => ipadOpen(d, { stage: 'undone' }),
  });
  frame(`${IP.open}~channels`, {
    device: 'ipad11-landscape', title: 'V1-15 · Release lead ▾ (the channels)',
    note: 'The channel menu: Release lead ✓ · Widget · Release notes · Screenshots. A step row on the paper switches the pane to that run, not animated.',
    render: (ctx, d) => ipadOpen(d, { menu: true }),
  });
  frame(`${IP.open}~widget`, {
    device: 'ipad11-landscape', title: 'V1-15 · the pane on the Widget run (from Review Plan ›)',
    note: 'Entered from Review Plan ›: the pane is on the Widget channel with the plan card at its foot, so the paper’s door becomes the pointer (drawn once, as V1-13 at 400 wide). The paper shows its Conversation block again.',
    render: (ctx, d) => ipadOpen(d, { channel: 'widget', stage: null }),
  });
  frame(`${IP.open}~channels-widget`, {
    device: 'ipad11-landscape', title: 'V1-15 · Widget ▾',
    note: 'The same channel menu from the Widget channel, Widget checked.',
    render: (ctx, d) => ipadOpen(d, { channel: 'widget', stage: null, menu: true }),
  });
  frame(`${IP.open}~widget-approved`, {
    device: 'ipad11-landscape', title: 'V1-15 · approved in the pane', view: { approved: { a2: true } },
    note: 'Approve in the pane: “✓ Approved · Undo”; the pointer goes and the state reads “Working · 2 working · 1 done”; N drops in the list at once. ✕ returns to the closed layout on the same path.',
    render: (ctx, d) => ipadOpen(d, { channel: 'widget', stage: null, approved: true, closeTo: `${IP.closed}~approved` }),
  });
  frame(`${IP.open}~sidebar-over`, {
    device: 'ipad11-landscape', title: 'V1-15 · ☰ the sidebar over the list',
    note: 'While the pane is open, ☰ brings the sidebar (400) back over the list and the paper’s leading edge with a 0.2 dim; the regions never move. A pick slides it away.',
    render: (ctx, d) => ipadOpen(d, { sidebarOver: true }),
  });

  /* ---------------------------------------------------------------- V1-16 iPad 13 landscape: the place summary */

  function ipad13(d, o) {
    o = o || {};
    const base = IP.sum;
    const sel = o.sel || null;
    const list = listPane(d, {
      sel, scrollTo: o.lit, lit: o.lit, open: o.canWait ? { can_wait: true } : null, fog: !!o.lit,
      // with the pane open the sidebar has stepped aside: ☰ at the list's leading edge lays it over the list
      menuBtn: o.open ? N(o.sidebarOver ? IP.o13 : `${IP.o13}~sidebar-over`, FADE) : null,
      go: (t) => (t.id === 't01' ? N(IP.p13, NONE) : T(`Opens: ${t.title} in the paper region`)),
      ans: (t) => (t.id === 't01' ? Object.assign({ label: t.answer.label }, T('Answers Ryokan Hanami'), { options: N(IP.p13, NONE) }) : t.id === 't02' ? T('Opens the pane on the Widget run') : T(`Opens: ${t.title}`)),
      foldNav: { can_wait: N(o.canWait ? base : `${base}~can-wait`, NONE) },
    });
    const lines = { needs: 'needs', working: 'working', just_finished: 'just_finished', waiting: 'waiting', todo: 'todo', can_wait: 'can-wait' };
    const go = {};
    Object.keys(lines).forEach((k) => { go[k] = Object.assign(N(k === 'can_wait' ? `${base}~can-wait` : `${base}~lit-${k}`, NONE), { lit: o.lit === k }); });
    let right;
    if (o.open) right = [{ w: 636, html: ipadPaper(d, d.todo('t01'), { convNav: N(IP.p13, CLOSE), convActive: true, composer: false, paper: kyotoWideOpts({ conv: 'pointer' }) }), line: true }, { w: 400, html: stewardPane(d, d.todo('t01'), { close: N(IP.p13, CLOSE) }), line: true }];
    else if (sel) right = [{ w: 636, html: ipadPaper(d, d.todo('t01'), { convNav: N(IP.o13, OPEN), composer: { placeholder: LONG }, paper: kyotoWideOpts({ allNav: N(IP.o13, OPEN) }) }), line: true }];
    else {
      const sum = placeSummary(d, 'all', { go });
      right = [{ w: 636, html: ui.page({ body: sum, pad: false }), line: true }];
    }
    const cols = o.open ? [{ w: 340, html: list }] : [{ w: 400, html: tree(d, { go: { todos: N(base, NONE) } }) }, { w: 340, html: list, line: true }];
    const over = o.sidebarOver ? ui.drawer({ width: 400, scrimNav: N(IP.o13, FADE), content: tree(d, { go: { todos: N(IP.o13, FADE) } }) }) : '';
    return ui.split(cols.concat(right)) + over;
  }
  const kyotoWideOpts = (o) => Object.assign({
    askOn: { hanami: T('Answers Ryokan Hanami'), kaede: T('Answers Kaede'), tsuki: T('Answers Tsuki'), later: T('Later · folds to one line'), dont: T('Opens: the confirmation'), discuss: T('Opens the pane with “Answer: Which ryokan?”') },
    allNav: T('Opens the Conversation pane'), roundNav: T('Opens the Conversation pane'), compactSteps: true,
  }, o || {});

  frame(IP.sum, {
    device: 'ipad13-landscape', title: 'V1-16 · iPad 13″ landscape · the place summary (S7)',
    note: 'Sidebar 400 │ list 340 │ the paper region 636 showing the place summary, since nothing is selected: “All To-dos · 12 open …”, then one tappable line per part (N need you, working, just finished, waiting, to do, can wait) and how the Assistant’s number is made. S7 in one glance; each line scrolls the list to its section and lights the header for 1 s.',
    render: (ctx, d) => ipad13(d),
  });
  ['needs', 'working', 'just_finished', 'waiting', 'todo'].forEach((sec) => {
    frame(`${IP.sum}~lit-${sec}`, {
      device: 'ipad13-landscape', title: `V1-16 · summary → ${sec.replace('_', ' ')}`,
      note: 'The list scrolls to the section (as far as the list’s end allows) and lights its header for 1 s.',
      auto: { go: IP.sum, after: 1000, transition: FADE },
      render: (ctx, d) => ipad13(d, { lit: sec }),
    });
  });
  frame(`${IP.sum}~can-wait`, {
    device: 'ipad13-landscape', title: 'V1-16 · summary → can wait',
    note: '“1 can wait” opens the Can wait fold: listed for the owner, not counted.',
    render: (ctx, d) => ipad13(d, { canWait: true }),
  });
  frame(IP.p13, {
    device: 'ipad13-landscape', title: 'V1-16 · a row selected (Plan the Kyoto trip)',
    note: 'Selecting a row shows its paper (636, content ≤ 680). Conversation opens the pane as a pure −400-pt shift: list 340 │ paper 636 │ conversation 400.',
    render: (ctx, d) => ipad13(d, { sel: 't01' }),
  });
  frame(IP.o13, {
    device: 'ipad13-landscape', title: 'V1-16 · with the conversation open',
    note: 'List 340 │ paper 636 │ conversation 400: the sidebar stepped aside and nothing changed width. The paper keeps its card (the pane is the steward’s conversation) and its block becomes the pointer.',
    render: (ctx, d) => ipad13(d, { sel: 't01', open: true }),
  });
  frame(`${IP.o13}~sidebar-over`, {
    device: 'ipad13-landscape', title: 'V1-16 · ☰ the sidebar over the list',
    note: 'With the pane open the sidebar has stepped aside; ☰ at the list’s leading edge lays it (400) over the list with a 0.2 dim. The regions never move; a pick slides it away.',
    render: (ctx, d) => ipad13(d, { sel: 't01', open: true, sidebarOver: true }),
  });

  /* ---------------------------------------------------------------- iPad 11 portrait (also in V1) */

  frame(IP.pc, {
    device: 'ipad11-portrait', title: 'V1 · iPad 11″ portrait · list │ paper',
    note: 'List 320 │ paper 514; the sidebar lies over the page while To-dos is in front (☰). Conversation opens the pane: paper 434 │ conversation 400, the list steps aside.',
    render: (ctx, d) => ipadPortrait(d),
  });
  frame(`${IP.pc}~sidebar`, {
    device: 'ipad11-portrait', title: 'V1 · iPad 11″ portrait · the sidebar over the page',
    note: 'The sidebar (400) slides over the list and paper with a 0.2 dim; a pick slides it away.',
    render: (ctx, d) => ipadPortrait(d, { sidebar: true }),
  });
  frame(IP.po, {
    device: 'ipad11-portrait', title: 'V1 · iPad 11″ portrait · paper │ conversation',
    note: 'Paper 434 │ conversation 400: “‹ List” at the paper’s leading edge lays the list over the paper. The paper keeps its card; its Conversation block is the pointer; the composer moved into the pane.',
    render: (ctx, d) => ipadPortraitOpen(d),
  });
  frame(`${IP.po}~list-over`, {
    device: 'ipad11-portrait', title: 'V1 · iPad 11″ portrait · ‹ List over the paper',
    note: 'The list (320) slides over the paper from the leading edge, the paper dimmed; a pick swaps the paper and slides the list away.',
    render: (ctx, d) => ipadPortraitOpen(d, { listOver: true }),
  });
  function ipadPortrait(d, o) {
    o = o || {};
    const t = d.todo('t01');
    const list = listPane(d, {
      sel: 't01', menuBtn: N(`${IP.pc}~sidebar`, FADE),
      go: (x) => (x.id === 't01' ? N(IP.pc, NONE) : T(`Opens: ${x.title} in the paper`)),
      ans: (x) => (x.id === 't01' ? Object.assign({ label: x.answer.label }, T('Answers Ryokan Hanami'), { options: N(IP.pc, NONE) }) : T(`Opens: ${x.title}`)),
      foldNav: { can_wait: T('Opens: Can wait') },
    });
    const paper = ipadPaper(d, t, { convNav: N(IP.po, OPEN), paper: kyotoWideOpts({ allNav: N(IP.po, OPEN), roundNav: N(IP.po, OPEN) }) });
    const html = ui.split([{ w: 320, html: list }, { w: 514, html: paper, line: true }]);
    return html + (o.sidebar ? ui.drawer({ width: 400, scrimNav: N(IP.pc, FADE), content: tree(d, { go: { todos: N(IP.pc, FADE) } }) }) : '');
  }
  function ipadPortraitOpen(d, o) {
    o = o || {};
    const t = d.todo('t01');
    let over = '';
    if (o.listOver) {
      const list = listPane(d, { sel: 't01', go: (x) => (x.id === 't01' ? N(IP.po, FADE) : T(`Opens: ${x.title} in the paper`)), ans: () => T('Answers in place'), foldNav: { can_wait: T('Opens: Can wait') } });
      over = `<div class="v1-over-scrim"${A(N(IP.po, FADE))}></div><div class="v1-over">${list}</div>`;
    }
    const paper = ipadPaper(d, t, { leading: [backCapsule('List', N(`${IP.po}~list-over`, FADE))], convNav: N(IP.pc, CLOSE), convActive: true, composer: false, paper: kyotoWideOpts({ conv: 'pointer' }) });
    const pane = stewardPane(d, t, { close: N(IP.pc, CLOSE) });
    // the list lies over the paper's leading edge and the dim covers the whole window (paper and pane), as the sidebar does
    return ui.split([{ w: 434, html: paper }, { w: 400, html: pane, line: true }]) + over;
  }

  /* ================================================================== Web */

  const WEB = { open: 'web/open' };
  function webRail(d) {
    const c = d.counts;
    const item = (icon, label, o) => `<span class="v1-rail-i${o && o.on ? ' is-on' : ''}"${A(o && o.nav)} title="${label}">${I(icon, 18)}${o && o.badge != null ? `<span class="v1-rail-b">${o.badge}</span>` : ''}</span>`;
    return `<div class="v1-rail">
      <span class="v1-rail-face"${A(N(`${WEB.open}~closed`, NONE))} title="Show Sidebar">${ui.face(28)}</span>
      ${item('sparkle', 'Assistant', { nav: T('Opens: the Assistant') })}
      ${item('agents', 'Agents', { nav: T('Opens: Agents'), badge: ui.badge(c.agentsBadge, { look: 'red' }) })}
      ${item('checklist', 'To-dos', { on: true, nav: N(`${WEB.open}~closed`, NONE), badge: ui.badge(c.needs, { look: 'needs' }) })}
      ${item('tray-full', 'Inbox', { nav: T('Opens: Inbox') })}
      ${item('magnifying-glass', 'Search', { nav: T('Opens: Search To-dos (⌘K)') })}
      <span class="k-grow"></span>
      ${item('gear', 'Settings', { nav: T('Opens: Settings') })}
    </div>`;
  }
  function webFrame(d, o) {
    o = o || {};
    const t = d.todo('t01');
    const open = !o.closed;
    const list = ui.page({
      top: ui.navbar({
        align: 'leading', center: ui.titleMenu({ title: d.place('all').title, toast: 'Opens: the places menu' }),
        trailing: [{ icon: 'plus', label: 'New', look: 'fill', size: 's', toast: 'Opens: New To-Do (⌘N)' }],
        below: sectionBar(d, { web: true, compact: true }),
      }),
      body: sectionsHtml(d, { sel: 't01', pointer: { t01: true }, go: (x) => (x.id === 't01' ? N(WEB.open + (open ? '' : '~closed'), NONE) : T(`Opens: ${x.title}`)), ans: (x) => (x.id === 't01' ? Object.assign({ label: x.answer.label }, T('Answers Ryokan Hanami'), { options: T('Unfolds the card in place') }) : T(`Opens: ${x.title}`)), foldNav: { can_wait: T('Opens: Can wait') } }),
    });
    const paper = paperPage(d, t, {
      leading: [], convLabel: true, convSize: 's', convActive: open, convHint: '⌘B', convNav: N(open ? `${WEB.open}~closed` : WEB.open, open ? CLOSE : OPEN),
      moreNav: T('Opens: Copy Link, Move to Project, Someday, Decisions, Chat Topic, Delete'),
      readable: true,
      paper: kyotoWideOpts({ conv: open ? 'pointer' : undefined, allNav: N(WEB.open, OPEN), roundNav: N(WEB.open, OPEN) }),
      composer: open ? false : { placeholder: LONG }, composerNav: T('Focuses the composer (Mod+I)'),
    });
    if (open) {
      const pane = stewardPane(d, t, { close: N(`${WEB.open}~closed`, CLOSE) });
      return ui.split([{ w: 56, html: webRail(d) }, { w: 360, html: list, line: true }, { w: 584, html: paper, line: true }, { w: 440, html: pane, line: true }]);
    }
    return ui.split([{ w: 284, html: tree(d, { width: 284, go: { todos: N(`${WEB.open}~closed`, NONE) } }) }, { w: 360, html: list, line: true }, { w: 796, html: paper, line: true }]);
  }
  frame(WEB.open, {
    device: 'web', title: 'V1-17 · Web 1440 · rail │ list │ paper │ conversation',
    note: 'URL /todos/all?id=t01&talk=1. Rail 56 │ list 360 │ paper 584 │ conversation 440: four regions at 1440. The pane uses the side panel’s mechanics at one width (440; a named change from today’s resizable panel); the rail is a transient collapse that never overwrites the stored sidebar width. Needs you is blue. Keys: J/K move, X ticks, Enter opens, Mod+B toggles the conversation, Mod+I focuses the composer, Mod+Alt+B the rail, Esc closes only overlays.',
    render: (ctx, d) => webFrame(d),
  });
  frame(`${WEB.open}~closed`, {
    device: 'web', title: 'V1-17 · Web 1440 · conversation closed',
    note: 'Sidebar 284 │ list 360 │ paper 796 with its content ≤ 680, centred; the composer is docked at the paper’s foot. Below 1280 the list steps aside; 640–1023 lays the pane over the paper; below 640 is one page with the pinned header.',
    render: (ctx, d) => webFrame(d, { closed: true }),
  });

  /* ================================================================== Android foldable */

  const AND = 'android-fold/paper-conversation';
  const abar = (o) => `<div class="v1-abar">${o.lead ? `<span class="v1-abar-b${o.leadCls ? ' ' + o.leadCls : ''}"${A(o.leadNav)}>${I(o.lead, 20)}</span>` : ''}<span class="v1-abar-t">${o.title}</span>${(o.trail || []).map((b) => `<span class="v1-abar-b${b.cls ? ' ' + b.cls : ''}"${A(b.nav)}>${I(b.icon, 20)}</span>`).join('')}</div>`;
  function androidFrame(d, o) {
    o = o || {};
    const t = d.todo('t01');
    const listOpen = !!o.list;
    const paper = paperPage(d, t, {
      top: abar({ lead: listOpen ? null : 'arrow-right', leadCls: 'is-back', leadNav: N(`${AND}~list-paper`, FADE), title: '', trail: [{ icon: 'bubble-left-right', cls: listOpen ? '' : 'is-on', nav: N(listOpen ? AND : `${AND}~list-paper`, listOpen ? OPEN : CLOSE) }, { icon: 'ellipsis', cls: 'is-vert', nav: T('Opens: More') }] }),
      paper: { narrow: true, compactAsk: !listOpen, stepsMode: 'summary', stepsNav: T('Opens: Steps'), conv: listOpen ? undefined : 'pointer', convCount: 1, recap: false, results: listOpen, askOn: { hanami: T('Answers Ryokan Hanami'), kaede: T('Answers Kaede'), tsuki: T('Answers Tsuki'), later: T('Later'), dont: T('Opens: the confirmation'), discuss: T('Puts “Answer: Which ryokan?” in the composer') }, allNav: N(AND, OPEN), roundNav: N(AND, OPEN) },
      composer: listOpen ? {} : false, composerNav: T('Opens: the composer, focused'),
    });
    if (listOpen) {
      const list = listPane(d, { android: true, menuBtn: T('Opens: the navigation drawer'), sel: 't01', go: (x) => (x.id === 't01' ? N(`${AND}~list-paper`, NONE) : T(`Opens: ${x.title}`)), ans: () => T('Answers in place'), foldNav: { can_wait: T('Opens: Can wait') } });
      return ui.split([{ w: 336, html: list }, { w: 337, html: paper }]);
    }
    const pane = ui.page({
      top: abar({ title: t.steward, trail: [{ icon: 'xmark', nav: N(`${AND}~list-paper`, CLOSE) }] }),
      body: ui.thread(t.rounds, { anchor: 'bottom' }), pad: false,
      bottom: composer(t, { send: T('Sends the message to Persona') }),
    });
    return ui.split([{ w: 336, html: paper }, { w: 337, html: pane }]);
  }
  frame(AND, {
    device: 'android-fold', title: 'V1-18 · Android foldable · paper ┊ conversation (book posture)',
    note: 'Paper ┊ conversation at the hinge (SupportingPaneSceneStrategy, HingePolicy.AvoidSeparating); a top app bar per pane (the paper’s bar shows its title only once it scrolls away, as on every client). Predictive Back closes the conversation first. Tabletop: content above the hinge, composer and keyboard below. Layout only: the frames reuse the iOS components.',
    render: (ctx, d) => androidFrame(d),
  });
  frame(`${AND}~list-paper`, {
    device: 'android-fold', title: 'V1-18 · Back closes the conversation first',
    note: 'Predictive Back from the conversation returns to list ┊ paper; the composer is back at the paper’s foot.',
    render: (ctx, d) => androidFrame(d, { list: true }),
  });
})();
