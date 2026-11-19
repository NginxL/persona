/*
 * Variant V2 · Rounds (synthesis §3.2, from concept B).
 *
 * One column everywhere. The to-do page carries its latest three rounds expanded; there is never a conversation
 * region. The whole Conversation and an Agent's session replace the to-do inside its own column, and nothing else on
 * screen moves. Everything else (data, list, rows, partition, section bar, ask cards and doors, counts) is shared.
 *
 * Every number and word comes from window.Data (data/todos.js) through the shared components (Kit.ui). The few strings
 * below that the data set lacks are the V2 rounds the spec writes out (V2-03 #sent) and V2's own phrasings (the Agents
 * line, "Tell Persona about this…"); see v2/notes.md.
 *
 * Frame ids follow the spec's names with "+" written "-" (the kit's ids allow letters, digits and / _ . - only):
 * duo-inner-portrait/list-todo (V2-08), web/todo (V2-13), android-fold/list-todo (V2-14).
 */
(function () {
  'use strict';
  const V = 'V2';
  const ui = Kit.ui;
  const D0 = window.Data;
  const I = (n, s) => Kit.icon(n, s);
  const J = Kit.join;
  const nav = (o) => Kit.navAttrs(o);

  Kit.variant(V, {
    name: 'Rounds',
    order: 20,
    summary: 'One column everywhere: the to-do page carries its latest three rounds, and the whole Conversation or an Agent’s session replaces the to-do inside its own column. Nothing else on screen moves. Does the owner need a conversation pane at all?',
  });

  /* ------------------------------------------------------------------ navigation (§2.9) */

  // iPhone: push / pop. Duo: every page step cross-fades. Selection, place and state changes: none. On regular widths the
  // in-column push is played by columnPush() below (the kit's push would slide the sidebar and the list too).
  const push = (go) => ({ go, transition: 'push' });
  const pop = (go) => ({ go, transition: 'pop' });
  const fade = (go) => ({ go, transition: 'fade' });
  const swap = (go) => ({ go, transition: 'none' });
  const say = (toast) => ({ toast });

  /* ------------------------------------------------------------------ data views */

  const VIEWS = {};
  /** Data.view(state), cached: counts follow every state (answering a1 gives N = 2 everywhere). */
  const view = (st) => {
    if (!st) return D0;
    const k = JSON.stringify(st);
    return VIEWS[k] || (VIEWS[k] = D0.view(st));
  };
  const ANS = (opt) => ({ answered: { a1: opt || 'hanami' } });
  const ANSWERED = ANS('hanami');
  const APPROVED = { approved: { a2: true } };
  const BACK = { a3: 'back' };

  const A1 = D0.ask('a1');
  const optLabel = (id) => (A1.options.find((o) => o.id === id) || A1.options[0]).label;
  const OPTS = A1.options.map((o) => o.id); // hanami · kaede · tsuki

  /**
   * Where each answer of a1 lands, per option (Kaede and Tsuki behave as Ryokan Hanami, with their own label: V1-02).
   * list: the row's 4-s "✓ You chose …" · idle: re-filed · kyoto: the card's 4 s · round: the answer as a round ·
   * conv: answered in the Conversation · duo: answered on the Duo's card.
   */
  const ANSWER_AT = {
    hanami: { list: '~answered', idle: '~answered-idle', kyoto: '~answered', round: '~answered-round', conv: '~answered', duo: '~answered' },
    kaede: { list: '~answered-kaede', idle: '~answered-kaede-idle', kyoto: '~kaede', round: '~kaede-round', conv: '~answered-kaede', duo: '~answered-kaede' },
    tsuki: { list: '~answered-tsuki', idle: '~answered-tsuki-idle', kyoto: '~tsuki', round: '~tsuki-round', conv: '~answered-tsuki', duo: '~answered-tsuki' },
  };
  /** { hanami: nav, kaede: nav, tsuki: nav } for a card's option buttons: fn(option id) → nav */
  const optionsTo = (fn) => { const o = {}; OPTS.forEach((id) => { o[id] = fn(id); }); return o; };

  // Don't handle (V1-02): recorded and undoable; T01 moves to Working without a ryokan, the step reads skipped. The shared
  // data set has no declined state (kit request), so V2 makes one locally from the answered view: N = 2 everywhere.
  const DECLINED = (() => {
    const D = D0.view(ANSWERED);
    const t = D.todo('t01');
    const text = 'Persona is continuing without a ryokan';
    Object.assign(t, {
      row: { mark: 'working', text }, state: { word: 'Working', text },
      answered: { ask: 'a1', declined: true, text: 'Not handled' }, line3: D0.todo('t01').line3,
    });
    t.steps = D0.todo('t01').steps.map((s) => (s.state === 'needs' ? { text: s.text, state: 'skipped' } : s));
    return D;
  })();

  // Toasts for an ask card's buttons where the frame does not build the answered state (every button answers: §3.0.1).
  const A1_SAY = (o) => Object.assign(optionsTo((id) => say(`You chose ${optLabel(id)} · Undo`)), {
    later: say('Folds to one line: Which ryokan? · later today'), dont: say('Asks first: Persona won’t book a ryokan'),
  }, o);
  const A2_SAY = (o) => Object.assign({ later: say('Later: folds to one line until later today'), dont: say('Asks first: Release lead won’t build the widget') }, o);

  // Short forms of words that would strand on a line of their own: prices keep "a night", durations keep their unit.
  const NB = '\u00a0';
  const glue = (s) => String(s == null ? '' : s)
    .replace(/(¥\d+k) a night/g, `$1${NB}a${NB}night`)
    .replace(/under (¥\d+k)/g, `under${NB}$1`)
    .replace(/about (\d+) (h|min)\./g, `about${NB}$1${NB}$2.`)
    .replace(/unless you answer\./g, `unless${NB}you${NB}answer.`);

  // Short forms the spec writes for one-line places that the shared data set does not carry (listed in notes.md and as
  // a kit request): the steps summary names its steps (V2-02 "✓ Dates ✓ Flights ◉ Ryokan"), the Agents line its runs
  // (V2 rule 7 "● Widget ◐ Notes ✓ Screens"), the Background round its short text (V2-02, V2-04).
  const STEP_SHORT = { 'Choose dates': 'Dates', 'Book flights': 'Flights', 'Pick a ryokan': 'Ryokan', 'Get JR passes': 'JR passes', 'Plan each day': 'Days' };
  const RUN_SHORT = { widget: 'Widget', 'release-notes': 'Notes', screenshots: 'Screens' };
  const BG_TEXT = { 'bg-tue': { paper: 'checked prices', page: 'checked prices · no change' } };
  const ADDED_AT = { t01: '9:11' }; // "⊕ Added from the Assistant · 9:11" (V2-04)
  // The results strip's chips as the spec writes them (V2-02 "[Flights · held] [Shortlist · 3]", V2-05 "[Screenshots · 6]
  // [What’s New]"), so both chips show whole on a phone.
  const RESULT_SHORT = {
    Flights: { label: 'Flights', outcome: 'held' }, 'Ryokan shortlist': { label: 'Shortlist', outcome: '3' },
    Screenshots: { label: 'Screenshots', outcome: '6' }, 'What’s New': { label: 'What’s New', outcome: '' },
  };
  // The ask's body in a 335-pt half (V2-08): one line, so the half reaches the latest round.
  const SHORT_WHY = { a1: 'Hanami: 4 min to Gion.' };
  // The drawn-once row line while the ask's home is on screen beside the list (§2.7): "● Which ryokan? · answer beside →";
  // the ask's own name, so one request has one name everywhere (§3.0.4 AskPointer)
  const POINTER = { a1: A1.title, a2: D0.ask('a2').title };
  const asNav = (x) => (typeof x === 'string' ? { go: x } : x || {});
  // The price line keeps each price whole ("Tsuki ¥59k a night" never leaves "a night" alone), and a wrap never starts or
  // ends a line with its "·": each price carries its separator, clipped where it would lead a line (see .v2-pl).
  const PRICES = A1.prices;
  const PRICES_GLUED = `<span class="v2-pl">${PRICES.split(' · ').map((x) => `<span>${x}</span>`).join('')}</span>`;
  const gluePrices = (html) => html.replace(PRICES, PRICES_GLUED);

  /**
   * The section bar (E2), from the data the frame draws: Needs you from the counts view (N changes at once, V1-01), the
   * other chips from the rows drawn (a row that has not re-filed yet still counts where it is drawn). All, the last
   * choice and where To-dos lands, is pinned at the end of the capsule before the grouping icon (Group by Phase ·
   * Project · Due Date), so the bar always shows its current choice and Needs you (N) whole at its start (E2: "Needs you
   * and All always show"); the scrolling row holds Needs you · In progress · To do · Done (fitBars below).
   * o: { D, counts, place, selected, go: { <chip id>: nav }, group: nav | false (a column of 340 or less hands Group by to
   *      its ⋯, so Needs you and In progress both stay whole beside All) }
   */
  function bar(o) {
    o = o || {};
    const D = o.D || D0;
    const CD = o.counts || D;
    const place = o.place || 'all';
    const sel = o.selected || 'all';
    const goes = o.go || {};
    const n = (CD.bar(place).find((x) => x.id === 'needs') || {}).count;
    const chips = D.bar(place);
    // All is the first chip (the landing choice first, as in every bar)
    const items = chips.map((x) => Object.assign({}, x, x.id === 'needs' ? { count: n } : {}, goes[x.id] || say(`Shows ${x.label}`)));
    const idx = items.findIndex((x) => x.id === sel);
    const group = o.group === false ? '' : ui.btn(Object.assign({ icon: 'list-bullet', look: 'plain', title: 'Group by' }, o.group || say('Opens: Group by · Phase ✓ · Project · Due Date')));
    return `<div class="v2-bar">${ui.sectionBar(items, { selected: idx, trailing: group })}</div>`;
  }

  // A list column of 340 or less (Duo inner 334, iPad 320 / 340, Android 336) hands Group by to its ⋯, so the bar keeps
  // Needs you and In progress whole beside All.
  const NARROW_MORE = 'Opens: Group by (Phase ✓ · Project · Due Date) · Sort · Show Someday · Show Done Today';

  /*
   * After every render (V2 frames only; the kit's pass runs first):
   * - the section bar shows whole chips: a chosen chip scrolls into view with the row starting on a chip boundary; the
   *   row fades out right after its last whole chip, and a chip of which less than 24 pt would show is not drawn, so no
   *   count or label is ever a sliver;
   * - a line cut with "…" ends on its last visible character, with no space before the "…" (UIKit trims it);
   * - an empty composer's predictive bar offers sentence starters without the typed-word quotes;
   * - the Duo tab bar's items name what they open (the kit's tabs take no toast).
   */
  (function afterRenderV2Hook() {
    const base = Kit.afterRender;
    Kit.afterRender = function afterRenderV2(root) {
      base(root);
      const scope = root || document;
      const all = (sel) => Array.prototype.slice.call(scope.querySelectorAll(sel));
      all('[data-variant="V2"] .k-sbar-in').forEach(fitBar);
      all('[data-variant="V2"]').forEach(trimEllipses);
      all('[data-variant="V2"] .dev-kb').forEach((kb) => {
        const dev = kb.closest('[data-variant="V2"]');
        if (!dev || dev.querySelector('.k-tc-f.has-text')) return;
        const first = kb.querySelector('.k-kb-sugg > span:first-child');
        if (first) first.textContent = first.textContent.replace(/[“”]/g, '');
      });
      all('[data-variant="V2"] .dev-side-back:not([data-go])').forEach((b) => b.setAttribute('data-toast', 'Opens: the sidebar'));
      all('[data-variant="V2"] .dev-tab-item').forEach((tab, i) => {
        if (tab.hasAttribute('data-go') || tab.hasAttribute('data-toast')) return;
        const c = D0.counts;
        tab.setAttribute('data-toast', i === 0
          ? `Opens: the Assistant · Needs you ${c.assistantNeeds} (To-dos ${c.needs} · Elsewhere ${c.elsewhere})`
          : `Opens: Agents · ${c.fleet}`);
      });
    };

    function fitBar(el) {
      const chips = Array.prototype.slice.call(el.children).filter((c) => c.classList.contains('k-sbar-chip'));
      chips.forEach((c) => { c.style.visibility = ''; });
      Array.prototype.slice.call(el.querySelectorAll('.v2-sbar-pad')).forEach((p) => p.remove());
      el.scrollLeft = 0;
      const W = el.clientWidth;
      const L = (c) => c.offsetLeft; // the row is the chips' offset parent (screens.css)
      const R = (c) => c.offsetLeft + c.offsetWidth;
      const on = el.querySelector('.is-on');
      let start = 0;
      if (on && R(on) > W) {
        const first = chips.find((c) => R(on) - L(c) + 8 <= W) || on;
        start = Math.max(0, L(first) - 4);
        const max = el.scrollWidth - W;
        if (start > max) el.insertAdjacentHTML('beforeend', `<span class="v2-sbar-pad" style="flex:none;width:${Math.ceil(start - max)}px"></span>`);
        el.scrollLeft = start;
        start = el.scrollLeft;
      }
      const end = start + W;
      let lastWhole = null;
      let partial = null;
      chips.forEach((c) => {
        if (L(c) >= start - 0.5 && R(c) <= end + 0.5) lastWhole = c;
        else if (L(c) > start && L(c) < end) {
          // a chip cut at the edge fades from the last whole chip, so the strip says there is more past it (an empty
          // gap would read as a missing choice)
          partial = c;
        }
      });
      const cut = el.scrollWidth > W + 1 && chips.some((c) => R(c) > end + 0.5);
      el.classList.toggle('is-overflowing', cut);
      el.classList.toggle('is-scrolled', start > 0);
      let mask = '';
      const lead = start > 0 ? 'transparent, #000 12px, ' : '';
      if (partial && lastWhole) mask = `linear-gradient(90deg, ${lead}#000 ${Math.round(R(lastWhole) - start)}px, transparent ${W}px)`;
      else if (start > 0) mask = 'linear-gradient(90deg, transparent, #000 12px)';
      el.style.setProperty('-webkit-mask-image', mask || 'none');
      el.style.setProperty('mask-image', mask || 'none');
    }

    // A one-line text cut by CSS ellipsis keeps the space before "…"; cut it here at the last character that fits.
    function trimEllipses(dev) {
      const els = Array.prototype.slice.call(dev.querySelectorAll('*')).filter((el) => {
        if (!el.firstChild || el.scrollWidth <= el.clientWidth + 1) return false;
        const cs = getComputedStyle(el);
        return cs.textOverflow === 'ellipsis' && cs.whiteSpace.indexOf('nowrap') === 0;
      });
      els.forEach((el) => {
        const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
        let node = null;
        while (walker.nextNode()) if (walker.currentNode.data.trim()) node = walker.currentNode;
        if (!node) return;
        const text = node.data;
        const fits = (k) => { node.data = text.slice(0, k).replace(/[\s ·,]+$/, '') + '…'; return el.scrollWidth <= el.clientWidth + 1; };
        if (!fits(0)) { node.data = text; return; }
        let lo = 0;
        let hi = text.length - 1;
        while (lo < hi) { const mid = Math.ceil((lo + hi) / 2); if (fits(mid)) lo = mid; else hi = mid - 1; }
        fits(lo);
      });
    }
  })();
  const steward = (t) => (t.composer && t.composer.to && t.composer.to.label) || t.steward || 'Persona';
  const toMin = (s) => { const m = /^(\d+):(\d+)$/.exec(s || ''); return m ? +m[1] * 60 + +m[2] : null; };
  const dayShort = (d) => (!d || d === 'Today' ? '' : String(d).split(' ')[0]);
  const agentsOf = (t) => (t.runs || []).filter((r) => r.session);
  const isTeam = (t) => agentsOf(t).length > 1;

  // The rounds the spec writes out for V2 that the shared data set does not carry (V2-03 #sent; the answer's round).
  const TEA = {
    id: 'v2tea', day: 'Today', time: 'now', status: 'Delivered to Persona',
    text: 'also a tea ceremony on Apr 5',
    items: [
      { kind: 'reply', who: 'Persona', text: 'I’ll look for one on Apr 5 near Gion.' },
      { kind: 'scope', text: '+ tea ceremony on Apr 5' },
    ],
  };
  const answerRound = (label) => ({
    id: 'v2ans', day: 'Today', time: 'now', status: 'Delivered to Persona', answer: 'from the card', text: label,
    items: [{ kind: 'live', who: 'Persona', text: 'booking' }],
  });
  // Discuss… then "Kaede, it’s cheaper" with the chip "Answer: Which ryokan?": the message records the answer (V1-03
  // #answer-chip), "from your message"
  const ANSWER_MSG = 'Kaede, it’s cheaper';
  const answerMsgRound = () => ({
    id: 'v2msg', day: 'Today', time: 'now', status: 'Delivered to Persona', text: ANSWER_MSG,
    items: [{ kind: 'chose', text: optLabel('kaede'), how: 'from your message' }, { kind: 'live', who: 'Persona', text: 'booking' }],
  });

  /* ------------------------------------------------------------------ the V2 state line */

  /** B's state line: the age of a fresh ask ("· 12 min"); for several Agents "1 of 3 Agents asks" / "2 Agents". */
  function stateV2(t) {
    if (!t.state) return null;
    const runs = agentsOf(t);
    if (runs.length > 1) {
      const needs = runs.filter((r) => r.state === 'needs').length;
      const working = runs.filter((r) => r.state === 'working').length;
      if (t.phase === 'needs_you' && needs) return { phase: t.phase, word: t.state.word, text: `${needs} of ${runs.length} Agents asks` };
      // V2-11: "Working · 2 Agents" (the Agents line under it names all three)
      if (t.phase === 'working') return { phase: t.phase, word: t.state.word, text: `${working} Agents` };
    }
    let text = t.state.text;
    if (t.phase === 'needs_you') {
      const asked = (t.rounds || []).filter((r) => r.day === 'Today' && (r.items || []).some((i) => i.kind === 'asked')).pop();
      const a = asked && toMin(asked.time);
      const b = toMin(D0.now.time);
      // no-break spaces: the age never starts or ends a line on its own ("near Gion · 12 min")
      if (a != null && b != null && b > a) text += ` · ${b - a} min`;
    }
    return { phase: t.phase, word: t.state.word, text };
  }

  /** State line with its mark centred under the tick (the text column starts with the title's). */
  function stateRow(st, o) {
    if (!st) return '';
    o = o || {};
    const mark = st.phase === 'needs_you' ? 'needs' : st.phase === 'working' ? 'working' : st.phase === 'ready' ? 'ready' : st.phase === 'waiting' ? 'waiting' : '';
    return `<div class="v2-state"><span class="v2-state-m">${ui.lineMark(mark)}</span>${ui.stateLine(st.phase, st.text, { word: st.word, clamp: o.clamp || 2 })}</div>`;
  }

  /* ------------------------------------------------------------------ rounds (B's tree) */

  const relayNow = (to) => {
    const t = D0.todos.find((x) => x.relayStates && x.relayStates.now && x.relayStates.now.indexOf(to) >= 0);
    return t ? t.relayStates.now : `Sent now to ${to}`;
  };
  const STATUS_TONE = (s) => (/^Not delivered/.test(s) ? 'danger' : /^(Delivery unknown|Not sure)/.test(s) ? 'caution' : /^Sending/.test(s) ? 'faint' : 'plain');

  function itemHtml(it, o) {
    const n = o.nav || {};
    switch (it.kind) {
      case 'reply': return ui.rline.reply(it);
      case 'run': return ui.rline.run(Object.assign({}, it, { open: !!(o.open && o.open[it.who]), session: n.session }, o.runNav ? o.runNav(it) : {}));
      case 'live':
        // once a newer round carries this run's live line, the older one reads as past (no spinner): one state per run
        if (o.past && o.past.indexOf(it.who) >= 0) return `<div class="k-rl v2-rl-past">${I('chevron-right', 10)}<span><b>${it.who}</b> · ${it.text}</span></div>`;
        return ui.rline.live(it);
      case 'needs': return ui.rline.needs(Object.assign({}, it, n.needs));
      case 'relay': {
        const st = o.relay || it.state;
        // errata E3: after Send Now the line reads "Sent now to …" (Data.relayStates.now), never "interrupted"
        if (st === 'now') return `<div class="k-rl k-rl--relay">${I('arrow-up-right', 12)}<span>${relayNow(it.to)}</span></div>`;
        // the relay's words stay on one line; Send Now follows them, or takes the next line when there is no room
        const glue = (x) => x.replace(/ /g, '\u00a0');
        const words = st === 'delivered' ? `Delivered to ${it.to}` : `Queued for ${it.to} · after this step`;
        return ui.rline.relay(Object.assign({}, it, { state: st, sendNowNav: n.sendNow })).replace(words, glue(words));
      }
      case 'scope': return ui.rline.scope(Object.assign({}, it, { undo: n.undo }));
      case 'decision': return ui.rline.decision(it);
      case 'kept': return ui.rline.kept(it);
      case 'asked':
        // answered (or declined): the asked line loses its blue mark and "(above)", in --ink-2. The choice itself is said
        // once: by the card's 4-s line, then by its own round (§2.7), never again here
        if (o.resolved) return `<div class="k-rl v2-rl-done">${I(o.resolved === 'declined' ? 'minus' : 'checkmark', 12)}<span>Asked: ${it.text}</span></div>`;
        return ui.rline.asked(Object.assign({}, it, { suffix: o.askedSuffix }));
      case 'chose': return `<div class="k-rl v2-rl-done">${I('checkmark', 12)}<span>You chose <b>${it.text}</b><span class="k-sep">·</span>${it.how}</span></div>`;
      case 'result': return ui.rline.result(it);
      case 'undone': return `<div class="k-rl v2-rl-undone">${I('arrow-uturn-left', 12)}<span>${it.text}</span></div>`;
      default: return '';
    }
  }

  /** The owner's words as one inline run: "You · 10:02 “near Gion please, under ¥60k” ✓". The last word and the ✓ never
      part, so the message state sits right after the words it belongs to. */
  function ownerWords(who, text, ok, cls) {
    // the last two words travel together, so a wrapped quote never ends on one short word ("under / ¥60k”")
    const words = glue(text).split(' ');
    const last = words.splice(-Math.min(2, words.length)).join(' ');
    return `<span class="v2-rnd-who">${who}</span> <span class="v2-rnd-q${cls ? ' ' + cls : ''}">${words.length ? words.join(' ') + ' ' : ''}<span class="v2-nw">${last}${ok}</span></span>`;
  }

  /**
   * One round: the owner's exact words and their message state ("✓" once delivered; the live state while it travels),
   * then what the round produced, each on a branch of the tree.
   * o: { status (the live state), timeOnly, full (the whole Conversation: words never clamp), filter ('messages': owner
   *      and steward text only), maxItems, open, runNav, nav: { session, sendNow, undo, needs }, relay, askedSuffix,
   *      resolved ('answered' | 'declined': the asked line), past: [run names], bgNav, withDay, go (the whole round),
   *      added ("⊕ Added from … · 9:11") }
   */
  function round(r, o) {
    o = o || {};
    if (r.kind === 'background') {
      const short = (BG_TEXT[r.id] || {})[o.full ? 'page' : 'paper'] || r.text;
      const text = o.withDay && r.when ? `${r.when} · ${short}` : short;
      return `<div class="v2-rnd is-bg">${ui.rline.background(Object.assign({ text }, o.bgNav || say('Unfolds what Persona checked on its own')))}</div>`;
    }
    const when = r.time === 'now' ? 'now' : o.timeOnly || r.day === 'Today' ? r.time : `${dayShort(r.day)} ${r.time}`;
    const st = o.status != null ? o.status : r.status;
    const settled = o.status == null && /^Delivered to /.test(r.status || '');
    const ok = settled ? `<span class="v2-rnd-ok" title="${Kit.esc(r.status)}">${I('checkmark', 12)}</span>` : '';
    // a 335-pt half draws the owner's words on one line, cut before the ✓ (V2-08: “near Gion…” ✓)
    const head = o.oneLine
      ? `<span class="v2-rnd-one"><span class="v2-rnd-one-t"><span class="v2-rnd-who">${r.answer ? 'You chose' : `You · ${when}`}</span> <span class="v2-rnd-q">${r.answer ? `<b>${r.text}</b> · ${r.answer}` : `“${r.text}”`}</span></span>${ok}</span>`
      : r.answer
        ? ownerWords('You chose', `<b>${r.text}</b> · ${r.answer}`, ok, 'is-answer')
        : ownerWords(`You\u00a0·\u00a0${when}`, `“${r.text}”`, ok);
    const live = !settled && st ? `<div class="v2-rnd-st k-rd-st--${STATUS_TONE(st)}">${st}</div>` : '';
    let items = (r.items || []).slice();
    if (o.filter === 'messages') items = items.filter((it) => it.kind === 'reply');
    if (o.extraItems) items = items.concat(o.extraItems);
    if (o.hide) items = items.filter((it) => o.hide.indexOf(it.kind) < 0);
    if (o.maxItems) items = items.slice(-o.maxItems);
    const body = items.map((it) => itemHtml(it, o)).filter(Boolean).map((h) => `<div class="v2-it">${h}</div>`).join('');
    const added = o.added && r.from ? `<div class="v2-added">${I('plus-circle-fill', 13)}<span>Added from ${r.from}${o.added !== true ? ` · ${o.added}` : ''}</span></div>` : '';
    return `${added}<div class="v2-rnd${o.full ? ' is-full' : ''}"${nav(o.go)}>
      <div class="v2-rnd-h${o.oneLine ? ' is-one' : ''}">${head}</div>${live}
      ${body ? `<div class="v2-rnd-items">${body}</div>` : ''}
    </div>`;
  }

  /** A whole thread with day separators (the Conversation page). o: round options + per: { <round id>: options },
      addedAt (the first round's "Added from … · 9:11") */
  function thread(rounds, o) {
    o = o || {};
    let day = null;
    const out = [];
    rounds.forEach((r, i) => {
      if (o.filter === 'messages' && r.kind === 'background') return; // Messages: only the owner's and the steward's text
      if (r.day && r.day !== day) { out.push(ui.rline.day(r.day)); day = r.day; }
      // Messages: only the owner's words and the steward's text, so the system line "⊕ Added from …" steps aside too
      const added = i === 0 && o.filter !== 'messages' ? o.addedAt || true : false;
      out.push(round(r, Object.assign({ timeOnly: true, full: true, withDay: false, added }, o, (o.per || {})[r.id])));
    });
    return out.join('');
  }

  /** The paper's Conversation block: "Conversation · All N ›" and the latest rounds expanded (V2 rule 1). p.head: false
      leaves the block's name out (the keyboard tail starts on a whole round). */
  function convBlock(t, p) {
    // a 335-pt half shows the rounds the owner spoke in (V2-08 draws R2 and R3), Background left to the whole Conversation
    const all = (p.rounds || t.rounds || []).concat(p.extra || []).filter((r) => !(p.noBg && r.kind === 'background'));
    if (!all.length) return '';
    const shown = all.slice(-(p.count || 3));
    const total = (t.allCount || all.length) + (p.extra ? p.extra.length : 0);
    const L = p.L || {};
    const resolved = t.answered ? (t.answered.declined ? 'declined' : 'answered') : null;
    const ro = Object.assign({ withDay: true, go: L.conv, nav: L.roundNav, maxItems: p.maxItems, resolved, oneLine: !!p.oneLine }, p.roundOpts);
    return `<section class="v2-cb">
      ${p.head === false ? '' : `<div class="v2-cb-h"><span class="v2-blk">Conversation</span><span class="v2-cb-all"${nav(L.conv)}>All ${total} ›</span></div>`}
      ${shown.map((r) => round(r, Object.assign({}, ro, (p.per || {})[r.id]))).join('')}
    </section>`;
  }

  /* ------------------------------------------------------------------ the paper's one-line blocks */

  // Every block on the paper is named the same way (13 semibold --ink-2: Steps · Agents · Results · Conversation), with
  // its value or marks after the name in 15.
  const runMark = (r) => (r.state === 'needs' ? 'needs' : r.state === 'working' ? 'working' : 'done');
  const runWord = (r) => ({ needs: 'Needs you', working: 'Working', done: 'Done' }[r.state] || r.state);

  function stepMark(state) {
    switch (state) {
      case 'done': return `<span class="v2-sm is-done">${I('checkmark-circle-fill', 15)}</span>`;
      case 'needs': return '<span class="v2-sm is-needs"><i></i></span>';
      case 'working': return `<span class="v2-sm">${ui.spinner(12)}</span>`;
      case 'skipped': return `<span class="v2-sm is-skipped">${I('minus', 10)}</span>`;
      default: return '<span class="v2-sm is-idle"></span>';
    }
  }

  /** "Steps 2/5 ✓ Dates ✓ Flights ◉ Ryokan ›" (V2-02): the first steps by name, the one that needs the owner always among
      them. o: { count (3), skipped (the needs step reads skipped: Don't handle), …nav } */
  function stepsLine(t, o) {
    o = o || {};
    const steps = t.steps || [];
    const done = steps.filter((s) => s.state === 'done').length;
    const k = o.count || 3;
    // anchored on the step that needs the owner, or else on the latest done step, so the line shows where things are
    const ni = steps.findIndex((s) => s.state === 'needs');
    const ai = ni >= 0 ? ni : steps.map((s) => s.state).lastIndexOf('done');
    const shown = ai >= k ? steps.slice(ai - k + 1, ai + 1) : steps.slice(0, k);
    const item = (s) => `<span class="v2-ss-i">${stepMark(o.skipped && s.state === 'needs' ? 'skipped' : s.state)}<span>${STEP_SHORT[s.text] || s.text}</span></span>`;
    return `<div class="v2-ss"${nav(o)}><span class="v2-blk">Steps ${done}/${steps.length}</span><span class="v2-ss-in">${shown.map(item).join('')}</span><span class="v2-chev">${I('chevron-right', 12)}</span></div>`;
  }

  /** "Results · Flights · Ryokan shortlist ›" on one line, for panes too narrow for the strip of chips. */
  function resultsLine(t, o) {
    return `<div class="v2-ss"${nav(o)}><span class="v2-blk">Results</span><span class="v2-ss-in"><span class="v2-ss-t">${(t.results || []).map((r) => r.label).join(' · ')}</span></span><span class="v2-chev">${I('chevron-right', 12)}</span></div>`;
  }

  /** "3 Agents ● Widget ◐ Notes ✓ Screens ⌄" (V2 rule 7), unfolding to one row per run with Open Session ›. */
  function agentsLine(t, L, open) {
    const runs = agentsOf(t);
    const line = `<div class="v2-ss v2-ag"${nav(L.agents)}><span class="v2-blk">${runs.length} Agents</span><span class="v2-ss-in">${runs.map((r) => `<span class="v2-ss-i">${ui.lineMark(runMark(r))}<span>${RUN_SHORT[r.id] || r.title}</span></span>`).join('')}</span><span class="v2-chev">${I(open ? 'chevron-up' : 'chevron-down', 12)}</span></div>`;
    if (!open) return line;
    const steps = t.steps || [];
    const rows = runs.map((r) => {
      const step = steps.find((s) => s.run === r.id) || {};
      const trail = r.state === 'done'
        ? `<span class="v2-ag-out">${step.word || ''}</span>`
        : `<span class="v2-ag-open"${nav((L.session || {})[r.id] || say(`Opens: ${r.title}’s session`))}>Open Session ›</span>`;
      return `<div class="v2-ag-row">${ui.lineMark(runMark(r))}<span class="v2-ag-t">${r.title}</span><span class="v2-ag-m">· ${r.role} · <span class="k-tone--${runMark(r) === 'done' ? 'idle' : runMark(r)} v2-ag-w">${runWord(r)}</span></span>${trail}</div>`;
    }).join('');
    return `<div class="v2-ag-wrap is-open">${line}<div class="v2-ag-rows">${rows}</div></div>`;
  }

  /* ------------------------------------------------------------------ the to-do page (B's order) */

  /**
   * The page body, top to bottom: tick + title; the state line with its age; the recap; the ask (home card or door);
   * notes or the brief; chips; the steps summary or the Agents line; results; Conversation (latest 3 rounds); Work details.
   * p: { L (links), ask: 'card' | 'answered' | 'later' | 'none', answeredLabel, stepsOpen, agentsOpen, workOpen, rounds,
   *      extra, roundOpts, per, count, maxItems, from: 'chips' (a page scrolled down to its chips), narrow, recap,
   *      state (override), scope (a scope line under the brief), prices }
   */
  function paperBody(t, p) {
    p = p || {};
    const L = p.L || {};
    const parts = [];
    // a page scrolled down starts at a block: 'chips', 'results' or 'conv' (the keyboard tail starts on a whole block)
    const order = ['top', 'chips', 'steps', 'results', 'conv'];
    const from = order.indexOf(p.from || 'top');
    const has = (k) => order.indexOf(k) >= from;
    if (has('top')) {
      const st = p.state || stateV2(t);
      parts.push(`<div class="v2-head">${ui.paperTitle({ title: t.title, lead: t.closed ? 'done' : 'tick', tickNav: L.tick || say('Done · Undo') })}${stateRow(st)}${p.recap !== false && t.recap ? `<div class="v2-recap">${ui.recapLine(t.recap, say('Scrolls to the rounds since you looked'))}</div>` : ''}</div>`);
      // the ask: a to-do's own ask is its home card here; a run's ask is a door
      const ask = t.asks && t.asks[0] ? D0.ask(t.asks[0]) : null;
      if (p.ask === 'answered') {
        const card = ui.askCard({ answered: p.answeredLabel || (t.answered && t.answered.text) || `You chose ${optLabel('hanami')}`, undo: L.undo });
        parts.push(p.declined ? `<div class="v2-declined">${card}</div>` : card);
      } else if (p.ask === 'later' && ask) {
        parts.push(ui.askCardFor(ask, { later: true, on: { laterOpen: L.laterOpen } }));
      } else if (ask && p.ask !== 'none') {
        if (ask.kind === 'plan') parts.push(ui.askCardFor(Object.assign({}, ask, { summary: glue(ask.summary) }), { as: 'door', on: L.a2 || A2_SAY() }));
        else {
          // a 335-pt half gets the one-line body the spec draws (V2-08), so the half reaches the latest round
          const a = p.shortAsk && SHORT_WHY[ask.id] ? Object.assign({}, ask, { why: SHORT_WHY[ask.id], prices: null }) : Object.assign({}, ask, { why: glue(ask.why) });
          parts.push(gluePrices(ui.askCardFor(a, { on: L.a1 || A1_SAY(), narrow: p.narrow })));
        }
      }
      if (t.notes) parts.push(ui.notes(glue(t.notes), say('Edits in place')));
      if (t.brief && !t.notes) parts.push(`<div class="v2-brief"${nav(say('Opens: What you asked · Done when'))}><span class="v2-brief-t">${t.brief}</span><span class="v2-more">More</span></div>`);
      if (p.scope) parts.push(`<div class="v2-scope">${ui.rline.scope({ text: p.scope, undo: L.scopeUndo })}</div>`);
    }
    if (has('chips')) parts.push(ui.chipsFor(t, { add: say('Opens: Date, Reminder, Project…') }));
    if (has('steps')) {
      if (isTeam(t)) parts.push(agentsLine(t, L, p.agentsOpen));
      else if ((t.steps || []).length) {
        // one format for the count, folded or open: "Steps 2/5"
        const done = t.steps.filter((x) => x.state === 'done').length;
        parts.push(p.stepsOpen
          ? `<div class="v2-steps">${ui.stepsFor(t, { headNav: L.steps, count: `${done}/${t.steps.length}` })}</div>`
          : stepsLine(t, Object.assign({ skipped: p.stepSkipped, count: p.narrow ? 2 : 3 }, L.steps)));
      }
    }
    if (has('results') && (t.results || []).length) {
      parts.push(p.narrow
        ? resultsLine(t, say('Opens: the results'))
        : `<div class="v2-res">${ui.resultsStrip(t.results.map((r) => Object.assign({}, r, RESULT_SHORT[r.label] || {}, say(`Opens: ${r.label}`))))}</div>`);
    }
    parts.push(convBlock(t, p));
    if (t.workDetails && !p.noWork) parts.push(workDetailsV2(t, L, p.workOpen));
    return `<div class="v2-paper${from > 0 ? ' is-scrolled' : ''}${p.anchor === 'bottom' ? ' is-bottom' : ''}">${parts.join('')}</div>`;
  }

  /** A "·"-separated line that wraps only between its parts, never leaving its last part alone ("· open"). */
  function segGlue(text) {
    const parts = String(text).split(' · ').map((x) => x.replace(/ /g, NB));
    if (parts.length > 1) parts.splice(-2, 2, parts.slice(-2).join(`${NB}·${NB}`));
    return parts.join(`${NB}· `);
  }

  /** Work details (§2.5: one folded line, named like every other block). Open, every row opens something (›); "Done
      when" draws its marks as the steps line does, from the to-do's Done when. */
  function workDetailsV2(t, L, open) {
    const rows = t.workDetails.rows.map((r) => {
      if (/^Done when/.test(r.text) && t.doneWhen) {
        const items = t.doneWhen.items.map((x) => `<span class="v2-dw-i">${stepMark(x.done ? 'done' : 'idle')}<span>${x.text}</span></span>`).join('');
        return Object.assign({}, r, { text: `<span class="v2-dw">Done when ${items}</span>`, run: true }, say('Opens: What you asked · Done when'));
      }
      if (r.run) return Object.assign({}, r, { text: segGlue(r.text) }, r.run === 'widget' && L.session && L.session.widget ? L.session.widget : say(`Opens: ${r.text.split(' · ')[0]}’s session`));
      return Object.assign({}, r, { text: segGlue(r.text), run: true }, say(`Opens: ${r.text.split(' · ')[0]}`));
    });
    return `<div class="v2-wd">${ui.workDetailsFor(t, Object.assign({ open: !!open }, L.work, { rows }))}</div>`;
  }

  /* ------------------------------------------------------------------ composer (V2 rule 6) */

  /**
   * Idle, the placeholder names the addressee ("Tell Persona about this…"); while there is text the label "To Persona ▾"
   * stands above the field. c: { text, send, inset, labelNav, answer ('Which ryokan?'), answerNav, run ('Release notes'),
   * mode ('After its step'), modeNav, menu (HTML), placeholder }
   */
  function composer(t, c) {
    c = c || {};
    const who = steward(t);
    const placeholder = c.placeholder || (t.closed ? 'Ask about this to-do…' : `Tell ${who} about this…`);
    if (c.answer || c.run) {
      const anchor = (html, menu) => (menu ? `<span class="v2-anchor">${html}<span class="v2-lbl-menu">${menu}</span></span>` : html);
      const chips = c.answer
        ? ui.addresseeChip(Object.assign({ answer: c.answer }, c.answerNav))
        : anchor(ui.addresseeChip(Object.assign({ label: c.run, run: true, menu: true }, c.labelNav)), c.labelMenu)
          + (c.mode ? anchor(`<span class="k-ach v2-mode"${nav(c.modeNav)}><span>${c.mode}</span>${I('chevron-down', 10)}</span>`, c.modeMenu) : '');
      return ui.todoComposer({ to: false, note: `<span class="v2-lbl">${chips}</span>`, text: c.text, placeholder: c.answer ? 'Your answer…' : placeholder, send: c.send, inset: c.inset });
    }
    // the steward's label is the same capsule as a run's or an answer's, drawn by V2 (the kit's 'above' label always adds
    // a ▾): "To Persona" has no ▾ while the menu would hold only Persona (§2.6), "To Release lead ▾" opens its menu
    const hasMenu = ((t.composer && t.composer.menu) || []).length > 1;
    const chip = ui.addresseeChip(Object.assign({ label: who, menu: hasMenu }, hasMenu ? c.labelNav : null));
    const label = c.text ? `<span class="v2-lbl">${c.labelMenu ? `<span class="v2-anchor">${chip}<span class="v2-lbl-menu">${c.labelMenu}</span></span>` : chip}</span>` : '';
    return ui.todoComposer({ to: false, note: label, text: c.text, placeholder, send: c.send, inset: c.inset });
  }

  /* ------------------------------------------------------------------ overlays */

  /** A menu anchored at a point of the page, with a catcher behind it that closes it. */
  function menuAt(items, o) {
    const pos = J([o.top != null ? `top:${o.top}px` : '', o.right != null ? `right:${o.right}px` : '', o.left != null ? `left:${o.left}px` : ''], ';');
    return `<div class="v2-catch"${nav(o.close)}></div><div class="v2-menu-at" style="${pos}">${ui.menu(items, { align: o.align || 'trailing', width: o.width })}</div>`;
  }

  /** An iOS 26 alert (glass, capsule buttons). */
  function alertBox(o) {
    return `<div class="k-scrim"${nav(o.cancel)}></div><div class="v2-alert"><div class="v2-alert-t">${o.title}</div>${o.body ? `<div class="v2-alert-b">${o.body}</div>` : ''}<div class="v2-alert-acts"><span class="v2-alert-btn"${nav(o.cancel)}>Cancel</span><span class="v2-alert-btn is-danger"${nav(o.ok)}>${o.okLabel}</span></div></div>`;
  }

  /* ------------------------------------------------------------------ the list (shared by every device) */

  /**
   * Rows of a place in sections (identical to V1-01's list). c: { D, counts (the D whose counts the headers show),
   * place, filter, from (start at this section: a scrolled list), selected, rowNav(t), ans: { <todo id>: { ok, opt } },
   * open: { can_wait, someday }, foldNav: { can_wait, someday }, answered: { <id>: label }, undo, optionsOpen (id),
   * optionsOn, ticked: [ids], swiped: id, pointer: { <id>: true | 'door' } (drawn once: the ask's home, or its door, is
   * on screen beside the list), narrow, tick(t) }
   */
  function listBody(c) {
    const D = c.D || D0;
    const CD = c.counts || D;
    const place = c.place || 'all';
    const secs = D.sections(place, c.filter || 'all');
    const calm = !c.filter && D.isCalm(place);
    const out = [];
    let started = !c.from || !secs.some((s) => s.id === c.from); // a scrolled list starts at a section, in Data's order
    secs.forEach((s) => {
      if (!started && s.id === c.from) started = true;
      if (!started) return;
      if (s.fold) {
        const open = !!(c.open && c.open[s.id]);
        out.push(ui.foldRow(s.title, Object.assign({ count: s.count, open }, (c.foldNav || {})[s.id] || say(`Unfolds ${s.title}`))));
        if (open) out.push(ui.todoList(s.todos.map((t) => rowHtml(t, c))));
        return;
      }
      if (!calm) {
        // Needs you is N, the hub's one number: it changes at once with the counts view (V1-01). Every other header
        // counts the rows drawn under it, so an answered row that has not re-filed yet never leaves a header off by one.
        const n = s.id === 'needs' ? (CD.sections(place, c.filter || 'all').find((x) => x.id === s.id) || { count: 0 }).count : s.count;
        out.push(ui.sectionHeader(s.title, { count: n, look: s.look }));
      }
      out.push(ui.todoList(s.todos.map((t) => rowHtml(t, c))));
    });
    return `<div class="v2-list${c.narrow ? ' v2-narrow' : ''}">${out.join('')}</div>`;
  }

  function rowHtml(t, c) {
    const p = ui.rowProps(t, {});
    Object.assign(p, c.rowNav ? c.rowNav(t) : say(`Opens: ${t.title}`));
    p.tickNav = c.tick ? c.tick(t) : say(t.phase === 'working' ? 'Asks first: Persona is still working on this' : 'Done · Undo');
    if (p.answer) {
      const a = (c.ans || {})[t.id] || {};
      p.answer = Object.assign({}, p.answer, a.ok || say(`Answers: ${p.answer.label}`));
      if (p.answer.options) p.answer.options = a.opt || say('Unfolds the full card');
      if (c.optionsOpen === t.id) p.answer.open = true;
    }
    // a narrow list drops "· Hanami recommended": the OK button under it already names the option
    if (c.narrow && t.answer && t.answer.option && /recommended$/.test(p.line || '')) p.line = '';
    if (c.selected === t.id) p.selected = true;
    if (c.answered && c.answered[t.id]) { p.answered = c.answered[t.id]; p.undo = c.undo; }
    if (c.optionsOpen === t.id) {
      // the full card is unfolded under the row: its buttons are the answer, so the row keeps only "Options ⌃" (drawn once)
      p.below = gluePrices(ui.askCardFor(t.asks[0], { on: c.optionsOn || {}, narrow: c.narrow }));
      p.cls = 'v2-opt-open';
    }
    if (c.ticked && c.ticked.indexOf(t.id) >= 0) p.ticked = true;
    if (c.swiped === t.id) p.swiped = true;
    if (c.pointer && c.pointer[t.id] && t.asks && t.asks.length) {
      // drawn once: the ask's home (or, for a run's ask, its door) is on screen beside the list, so the row only points
      // at it (§2.7): "● Which ryokan? · answer beside →", "● Plan awaiting approval · open beside →"
      const ask = D0.ask(t.asks[0]);
      p.answer = null;
      p.mark = 'needs'; p.tone = 'needs'; p.toneAll = false;
      // the pointer wraps rather than cut in a narrow list, only after its "·": the ask's name and "answer beside →"
      // each stay whole on their line (a zero-width break follows the separator)
      p.lineLead = String(ask ? POINTER[ask.id] || ask.title : p.lineLead).replace(/ /g, NB);
      p.line = '\u200b' + (c.pointer[t.id] === 'door' ? 'open beside →' : 'answer beside →').replace(/ /g, NB);
      p.cls = 'v2-ptr';
    }
    if (c.chose && c.chose[t.id]) {
      // answered on the card beside: the row confirms the choice once, without a second Undo, until it re-files
      p.answer = null;
      p.lineHtml = `<span class="v2-row-chose">${I('checkmark', 13)}<span>You chose ${c.chose[t.id]}</span></span>`;
    }
    return ui.todoRow(p);
  }

  /* ------------------------------------------------------------------ iPhone */

  const IP = 'iphone/';
  const LIST = IP + 'list';
  const KYOTO = IP + 'kyoto';
  const KYKB = IP + 'kyoto-keyboard';
  const CONV = IP + 'conversation-kyoto';
  const REL = IP + 'release';
  const SESS = IP + 'release-widget-session';

  // The places as menu items; V2's title menu carries "Show in Sidebar" for the current place (V2 rule 5).
  function placeItems(place, base) {
    const built = base === LIST;
    const extra = place === 'today'
      ? [{ label: 'Show in Sidebar', icon: 'star', toast: 'Today now shows under To-dos in the sidebar' }]
      : place === 'kyoto' ? [{ label: 'Remove from Sidebar', icon: 'star-fill', toast: 'Kyoto trip no longer shows in the sidebar' }] : null;
    return ui.placeMenuItems({
      selected: place,
      go: {
        all: swap(base), today: built ? swap(base + '~today') : say('Opens: Today'), upcoming: say('Opens: Upcoming'),
        kyoto: built ? swap(base + '~kyoto') : say('Opens: Kyoto trip'),
        weather: say('Opens: Weather App'), home: say('Opens: Home'), projects: say('Opens: All Projects'),
      },
      extra,
    });
  }

  /** V2's sidebar: To-dos shows only the places the owner starred (Kyoto trip and Weather App to start). */
  const STARRED = ['kyoto', 'weather'];
  function tree(sel, go, o) {
    // V2 rule 9: the sidebar never steps aside; the owner hides it with its own button (a toast here)
    // starred places carry a filled star in the sidebar's one glyph color (a hollow amber star reads "not starred" and
    // competes with the blue counts)
    return sidebarTreeHtml(sel, go, o)
      .split(`<span class="k-sbr-glyph k-tone--caution">${Kit.icon('star')}</span>`).join(`<span class="k-sbr-glyph">${Kit.icon('star-fill')}</span>`)
      .replace('title="Hide Sidebar"', 'title="Hide Sidebar" data-toast="Hides the sidebar: the list and the to-do widen"')
      .replace('title="New Chat"', 'title="New Chat" data-toast="Opens: New Chat"');
  }
  function sidebarTreeHtml(sel, go, o) {
    return ui.sidebarTree(Object.assign({
      selected: sel, starred: STARRED, counts: (o && o.counts) || D0.counts,
      go: Object.assign({
        inbox: say('Opens: Inbox'), scheduled: say('Opens: Scheduled Tasks'), knowledge: say('Opens: Knowledge'),
        machines: say('Opens: Machines'), persona: say('Opens: Persona'), 'release-lead': say('Opens: Release lead'),
        agents: say('Opens: Agents'), chats: say('Opens: Persona Chats'), weather: say('Opens: Weather App'),
      }, go),
    }, o || {}));
  }

  const PLACE_TITLE = (place) => (D0.place(place) || { title: 'All To-dos' }).title;

  /**
   * The iPhone list (V2-01 = V1-01). s: { D, counts, place, filter, menu, more, drawer, answered (the row's 4 s), opt (the
   * option chosen), idle (re-filed after an answer), declined, approved, optionsOpen, open, ticked, swiped, from, toast }
   */
  function iphoneList(s) {
    const D = s.D || D0;
    const CD = s.counts || D;
    const place = s.place || 'all';
    const base = LIST;
    const filters = { all: swap(base), needs: swap(base + '~filter-needs'), progress: swap(base + '~filter-progress'), todo: swap(base + '~filter-todo'), done: swap(base + '~filter-done') };
    const empty = place === 'today';
    const calm = !s.filter && D.isCalm(place);
    const menuId = place === 'all' ? base + '~menu' : place === 'today' ? base + '~today-menu' : base + '~kyoto-menu';
    const closeMenu = place === 'all' ? base : place === 'today' ? base + '~today' : base + '~kyoto';
    const top = ui.navbar({
      leading: [Object.assign({ icon: 'line-3-horizontal', title: 'Sidebar' }, swap(base + '~drawer'))],
      center: ui.titleMenu(Object.assign({ title: PLACE_TITLE(place), open: !!s.menu, items: placeItems(place, base) }, swap(s.menu ? closeMenu : menuId))),
      trailing: [{ icon: 'magnifying-glass', toast: 'Opens: Search To-dos' }, Object.assign({ icon: 'ellipsis' }, swap(s.more ? base : base + '~more'))],
      below: empty || calm ? '' : bar({
        D, counts: CD, place, selected: s.filter || 'all',
        go: place === 'all' ? filters : { needs: say('Shows Needs you in Kyoto trip'), done: say('Shows Done in Kyoto trip'), all: swap(base + '~kyoto') },
        group: place === 'all' ? swap(s.group ? base : base + '~group') : say('Opens: Group by · Phase ✓ · Project · Due Date'),
      }),
      fog: !!s.from,
    });
    let body;
    if (empty) {
      body = `<div class="v2-empty">${ui.empty({ icon: 'sun-fill', title: D.place('today').empty.title })}<div class="v2-empty-more"${nav(say('Opens: Upcoming'))}>${D.thisWeek()} coming up this week ›</div></div>`;
    } else if (s.filter === 'done') {
      body = `<div class="v2-empty">${ui.empty({ icon: 'checkmark-circle', title: 'What you finish shows up here.' })}</div>`;
    } else {
      body = listBody({
        D, counts: CD, place, filter: s.filter, from: s.from, open: s.open,
        rowNav: (t) => (t.id === 't01'
          ? push(KYOTO + (s.answered ? ANSWER_AT[s.opt || 'hanami'].kyoto : s.idle ? ANSWER_AT[s.opt || 'hanami'].round : s.declined ? '~dont' : ''))
          : t.id === 't02' ? push(s.approved ? REL + '~approved' : REL) : say(`Opens: ${t.title}`)),
        ans: {
          t01: { ok: swap(base + ANSWER_AT.hanami.list), opt: swap(s.optionsOpen ? base : base + '~options') },
          t02: { ok: push(SESS) },
          t03: { ok: swap(base + '~photos-back'), opt: say('Unfolds: Use MacBook Instead · Later') },
          t13: { ok: { go: 'V1:iphone/insurance-reply', transition: 'push' } }, // shared with V1 (§3.2: everything else is identical)
        },
        foldNav: { can_wait: swap(s.open && s.open.can_wait ? base : base + '~can-wait'), someday: say('Unfolds Someday') },
        tick: (t) => (t.id === 't11' ? swap(base + '~ticked') : say(t.phase === 'working' ? 'Asks first: Persona is still working on this' : 'Done · Undo')),
        answered: s.answered ? { t01: optLabel(s.opt || 'hanami') } : null, undo: swap(base),
        optionsOpen: s.optionsOpen,
        optionsOn: Object.assign(optionsTo((id) => swap(base + ANSWER_AT[id].list)), { later: swap(base), dont: say('Asks first: Persona won’t book a ryokan'), discuss: push(CONV + '~answer') }),
        ticked: s.ticked, swiped: s.swiped,
      });
    }
    const overlay = J([
      // capture is shared with V1 (V1-07), so New To-Do opens V1's capture sheet
      ui.pill({ label: 'New To-Do', go: 'V1:iphone/capture-passport~empty', transition: 'fade' }),
      s.toast || '',
      // E2: grouping lives behind the bar's trailing icon; Sort and Show stay in the list's ⋯
      s.more ? menuAt([
        { label: 'Sort', sub: 'Recent activity', icon: 'line-3-decrease', toast: 'Opens: Sort' }, '-',
        { label: 'Show Someday', toast: 'Shows Someday' }, { label: 'Show Done Today', toast: 'Shows Done Today' },
      ], { top: 112, right: 16, close: swap(base) }) : '',
      s.group ? menuAt([
        { header: 'Group by' }, { label: 'Phase', check: true, go: base, transition: 'none' }, { label: 'Project', toast: 'Groups by project' }, { label: 'Due Date', toast: 'Groups by due date' },
      ], { top: 176, right: 16, close: swap(base), width: 220 }) : '',
      s.menu ? `<div class="v2-catch is-under"${nav(swap(closeMenu))}></div>` : '',
      s.drawer ? ui.drawer({ content: tree('todos', { todos: swap(base), kyoto: swap(base + '~kyoto') }, { counts: CD.counts }), scrimNav: swap(base) }) : '',
    ]);
    return ui.page({ top, body, overlay, cls: 'v2-listpage' });
  }

  const LIST_STATES = [
    ['', 'iPhone · To-dos list (V2-01)', 'Identical to V1-01: All To-dos in sections, the section bar, inline answers. ☰ opens the drawer; the title is the place menu.', {}],
    ['calm', 'List · calm (first use)', 'Only plain to-dos and nothing needs you: no bar, no section headers, just rows and “Someday · 1 ›” (calm rule).', { D: view({ calm: true }) }],
    ['drawer', 'List · drawer', 'The drawer shows the same tree as the iPad sidebar. V2 rule 5: under To-dos only the starred places, Kyoto trip and Weather App.', { drawer: true }],
    ['menu', 'List · title menu', 'The title is the menu of places: All To-dos ✓ · Today · Upcoming · the projects with their part of N · All Projects…', { menu: true }],
    ['more', 'List · ⋯ menu', 'Sort (Recent activity), Show Someday, Show Done Today. Grouping moved to the bar’s trailing icon (E2).', { more: true }],
    ['group', 'List · Group by', 'The grouping icon at the end of the bar (E2): Group by Phase ✓ · Project · Due Date.', { group: true }],
    ['today', 'List · Today (empty)', 'Today picked in the title menu: its own empty state and “3 coming up this week ›”.', { place: 'today' }],
    ['today-menu', 'List · Today’s title menu', 'V2 rule 5: the title menu offers “Show in Sidebar” (☆) for the current place.', { place: 'today', menu: true }],
    ['kyoto', 'List · Kyoto trip', 'A starred project as the place: its own title, its own bar and sections (T01 only).', { place: 'kyoto' }],
    ['kyoto-menu', 'List · Kyoto trip’s title menu', 'The place is starred, so the menu offers “Remove from Sidebar”.', { place: 'kyoto', menu: true }],
    ['answered', 'List · answered from the row', 'OK: Ryokan Hanami → “✓ You chose Ryokan Hanami · Undo”. N goes (3) → (2) at once in the header and the bar; the other counts follow the rows drawn (Working 2, In progress 5) until the row re-files after 4 s.', { answered: true, opt: 'hanami', counts: view(ANS('hanami')) }, { auto: { go: LIST + '~answered-idle', after: 4000, transition: 'none' } }],
    ['answered-idle', 'List · answered, re-filed', 'Once the list is idle the row fades into Working: “Persona is booking Ryokan Hanami”. N = 2 everywhere; Working 3 and In progress 6.', { D: view(ANS('hanami')), opt: 'hanami', idle: true }],
    ['answered-kaede', 'List · Kaede chosen on the card', 'Kaede on the unfolded card: “✓ You chose Kaede · Undo”, N (3) → (2) at once; the row re-files after 4 s.', { answered: true, opt: 'kaede', counts: view(ANS('kaede')) }, { auto: { go: LIST + '~answered-kaede-idle', after: 4000, transition: 'none' } }],
    ['answered-kaede-idle', 'List · Kaede, re-filed', 'Working: “Persona is booking Kaede”. N = 2 everywhere; Working 3, In progress 6.', { D: view(ANS('kaede')), opt: 'kaede', idle: true }],
    ['answered-tsuki', 'List · Tsuki chosen on the card', 'Tsuki on the unfolded card: “✓ You chose Tsuki · Undo”, N (3) → (2) at once.', { answered: true, opt: 'tsuki', counts: view(ANS('tsuki')) }, { auto: { go: LIST + '~answered-tsuki-idle', after: 4000, transition: 'none' } }],
    ['answered-tsuki-idle', 'List · Tsuki, re-filed', 'Working: “Persona is booking Tsuki”. N = 2 everywhere.', { D: view(ANS('tsuki')), opt: 'tsuki', idle: true }],
    ['declined', 'List · after Don’t handle', 'Back from the page after Don’t handle: T01 is Working, “Persona is continuing without a ryokan”, and its OK is gone from every door at once. N = 2 everywhere; Working 3, In progress 6.', { D: DECLINED, declined: true }],
    ['options', 'List · Options ⌄ on a row', 'Options unfolds the full ask card under the row, in place (P1: Kaede · Tsuki, Ryokan Hanami · Recommended, Later · Don’t handle · Discuss…).', { optionsOpen: 't01' }],
    ['photos-back', 'List · Start When It’s Back', 'T03 moves to Waiting: “Starts when Desk Mac is back”. N (3) → (2); Waiting 4, In progress 6.', { D: view(BACK) }],
    ['can-wait', 'List · Can wait unfolded', 'Can wait · 1 › unfolds T13: “Awaiting Reply” with Reply, which opens the reply shared with V1.', { open: { can_wait: true } }],
    ['filter-needs', 'List · bar: Needs you', 'The Needs you section only, Can wait folded at its foot.', { filter: 'needs' }],
    ['filter-progress', 'List · bar: In progress', 'The tray’s In progress (#8237): Working and Waiting, counted; Just finished listed at its foot, not counted.', { filter: 'progress' }],
    ['filter-todo', 'List · bar: To do', 'To do, with Someday folded at its foot.', { filter: 'todo' }],
    ['filter-done', 'List · bar: Done', 'Nothing closed yet: the Done choice’s empty state.', { filter: 'done' }],
    ['scrolled', 'List · scrolled down', 'Below the fold: the remaining sections, in the shared order, down to “Someday · 1 ›”.', { from: 'waiting' }],
    ['ticked', 'List · ticked T11', 'The tick: strike-through, a haptic and “Done · Undo” for 4 s; the row leaves once the list is idle.', { from: 'waiting', ticked: ['t11'], toast: ui.toast('Done', { action: 'Undo', actionNav: swap(LIST + '~scrolled'), bottom: 108 }) }],
    ['swiped', 'List · trailing swipe', 'Inbox parity: one trailing swipe reveals Someday and Done (a full swipe completes).', { swiped: 't05' }],
    ['approved', 'List · after approving Widget’s plan', 'Back from the approved plan: T02 is Working. N = 2 in the header and the bar; Working 3, In progress 6.', { D: view(APPROVED), approved: true }],
  ];
  LIST_STATES.forEach(([st, title, note, s, extra]) => {
    Kit.frame(V, LIST + (st ? '~' + st : ''), Object.assign({
      device: 'iphone', title, note,
      render: () => iphoneList(s),
    }, extra || {}));
  });

  /* ---- iPhone · the to-do page (V2-02, V2-05) */

  function kyotoLinks(base, o) {
    o = o || {};
    return {
      conv: push(CONV),
      tick: say('Done · Undo'),
      steps: swap(o.stepsOpen ? base : base + '~steps'),
      undo: swap(KYOTO),
      laterOpen: swap(KYOTO),
      scopeUndo: swap(KYOTO + '~sent-done-undone'),
      roundNav: { undo: swap(KYOTO + '~sent-done-undone') },
      a1: Object.assign(optionsTo((id) => swap(KYOTO + ANSWER_AT[id].kyoto)), {
        later: swap(KYOTO + '~later'), dont: swap(KYOTO + '~dont-confirm'), discuss: push(CONV + '~answer'),
      }),
    };
  }

  function phoneTop(t, o) {
    // ‹ <place> (glass capsule) · ⋯ ; once scrolled the bar shows the title, leading next to ‹ as in the folded bar and
    // the pinned header, so the title never jumps across the bar (§2.5 element 1)
    return ui.navbar({
      leading: [o.scrolled ? Object.assign({ icon: 'chevron-left', title: 'All To-dos' }, o.back) : Object.assign({ icon: 'chevron-left', label: 'All To-dos' }, o.back)],
      title: o.scrolled ? t.title : '',
      align: o.scrolled ? 'leading' : undefined,
      cls: o.scrolled ? 'v2-inline' : '',
      trailing: [Object.assign({ icon: 'ellipsis' }, o.more)],
      fog: !!o.scrolled,
    });
  }

  /** A to-do's ⋯ menu: Delete… stands apart, after a divider (HIG: destructive actions are grouped apart). */
  function todoMenu(labels) {
    const out = [];
    (labels || []).forEach((m) => {
      if (/^Delete/.test(m)) out.push('-');
      out.push({ label: m, toast: `Opens: ${m.replace('…', '')}`, danger: /^Delete/.test(m) });
    });
    return out;
  }

  function iphoneKyoto(s) {
    const D = s.D || D0;
    const t = D.todo('t01');
    const L = kyotoLinks(KYOTO, s);
    // ‹ returns to the list in the same state: N = 2 and no OK once the ask is answered or declined (§2.7)
    const back = pop(LIST + (s.back || ''));
    const p = Object.assign({ L, stepsOpen: s.stepsOpen }, s.p);
    const overlay = J([
      s.menu ? menuAt(todoMenu(t.menu), { top: 112, right: 16, close: swap(KYOTO), width: 316 }) : '',
      s.alert || '',
      s.toast || '',
    ]);
    return ui.page({
      top: phoneTop(t, { back, more: swap(s.menu ? KYOTO : KYOTO + '~menu'), scrolled: s.scrolled }),
      body: paperBody(t, p),
      bottom: composer(t, { labelNav: swap(KYKB) }).replace('class="k-tc"', `class="k-tc"${Kit.goAttr(KYKB, 'none')}`),
      overlay,
      cls: p.anchor === 'bottom' ? 'v2-tail' : '',
    });
  }

  const answeredKyoto = (id) => [
    [ANSWER_AT[id].kyoto.slice(1), `Kyoto · ${optLabel(id)} chosen`, id === 'hanami'
      ? 'The card collapses to “✓ You chose Ryokan Hanami · Undo” (4 s); the state line reads Working, the recap goes, the step is ticked and the asked line loses its blue mark. The round follows.'
      : `The same with ${optLabel(id)}: the card collapses to “✓ You chose ${optLabel(id)} · Undo”; Persona is booking ${optLabel(id)}.`,
    { D: view(ANS(id)), back: ANSWER_AT[id].idle, p: { ask: 'answered' } }, false, { auto: { go: KYOTO + ANSWER_AT[id].round, after: 4000, transition: 'none' } }],
    [ANSWER_AT[id].round.slice(1), `Kyoto · ${optLabel(id)} as a round`, `After 4 s the answer is a round at the foot of the Conversation block: “You chose ${optLabel(id)} · from the card” └ ◐ Persona · booking. The choice is said once, there.`,
      { D: view(ANS(id)), back: ANSWER_AT[id].idle, p: { ask: 'none', extra: [answerRound(optLabel(id))] } }],
  ];

  const KYOTO_STATES = [
    ['', 'iPhone · Kyoto to-do with its rounds (V2-02)', 'B’s order: title, the state line with its age, the recap, the ask card (home), notes, chips, the steps summary, Results, then <b>the latest 3 rounds expanded</b> (R2, the Background line, R3) and All 4 ›. No Conversation toggle: All 4 ›, a round or Discuss… push the whole Conversation (V2-04). The composer opens V2-03.', {}, true],
    ['rounds', 'Kyoto · scrolled to its rounds (V2-02)', 'The idea under test, in view: the same page scrolled down, the Conversation block is the latest 3 rounds expanded (R2, the Background line, R3) above the composer, about 30% of the page. The bar shows the title, leading, once it scrolls away.', { scrolled: true, p: { anchor: 'bottom' } }, true],
    ['steps', 'Kyoto · steps unfolded', 'The steps summary unfolds the full list in place: who has each step, the one that needs you. One format for the count: 2/5.', { stepsOpen: true }],
  ].concat(answeredKyoto('hanami'), answeredKyoto('kaede'), answeredKyoto('tsuki'), [
    ['kaede-msg', 'Kyoto · answered from your message', 'Back from the Conversation after “Kaede, it’s cheaper” with the chip “Answer: Which ryokan?”: the message is a round that records the answer, “✓ You chose Kaede · from your message”.', { D: view(ANS('kaede')), back: ANSWER_AT.kaede.idle, p: { ask: 'none', extra: [answerMsgRound()] } }],
    ['later', 'Kyoto · Later', 'Later folds the card to one line, “Which ryokan? · later today ›”; the to-do stays in Needs you.', { p: { ask: 'later' } }],
    ['dont-confirm', 'Kyoto · Don’t handle (confirm)', 'Don’t handle asks first, naming what will not happen. It is recorded and undoable.', { alert: alertBox({ title: 'Don’t handle this?', body: 'Persona won’t book a ryokan. You can undo this.', okLabel: 'Don’t Handle', ok: swap(KYOTO + '~dont'), cancel: swap(KYOTO) }) }],
    ['dont', 'Kyoto · not handled', 'The to-do moves to Working: “Persona is continuing without a ryokan”. The ryokan step reads skipped; the card says “Not handled · Undo” for 4 s. ‹ returns to a list where N = 2 and T01 has no OK.', { D: DECLINED, back: '~declined', p: { ask: 'answered', declined: true, recap: false, answeredLabel: `Not handled: ${A1.title}` } }],
    ['sent-done', 'Kyoto · the sent round on the page', 'The keyboard down after V2-03 #sent (the folded title): the page scrolled to its rounds, the tea-ceremony round at the foot with Persona’s reply and “Scope: + tea ceremony on Apr 5 · Undo”. The reply lands on the paper.', { scrolled: true, p: { anchor: 'bottom', extra: [TEA] } }],
    ['sent-done-undone', 'Kyoto · scope undone on the page', 'Undo on the scope line removes it and says so in the round.', { scrolled: true, p: { anchor: 'bottom', extra: [TEA], per: { v2tea: { hide: ['scope'], extraItems: [{ kind: 'undone', text: 'Scope change undone' }] } } } }],
    ['menu', 'Kyoto · ⋯ menu', 'Copy Link · Move to Project… · Someday · Decisions (2) · Chat Topic… · Start an Agent on a Computer… · Delete…', { menu: true }],
  ]);
  KYOTO_STATES.forEach(([st, title, note, s, important, extra]) => {
    Kit.frame(V, KYOTO + (st ? '~' + st : ''), Object.assign({ device: 'iphone', title, note, important: !!important, render: () => iphoneKyoto(s) }, extra || {}));
  });

  /* ---- iPhone · keyboard up: the page folds (V2 rule 8, V2-03) */

  /** The bar folded: ‹ + title over the state (60), then the ask on one line (44) and a hairline. */
  function foldedHead(t, o) {
    o = o || {};
    const st = o.state || { phase: t.phase, word: t.state.word, text: o.sub };
    const mark = st.phase === 'needs_you' ? 'needs' : st.phase === 'working' ? 'working' : '';
    return `<div class="v2-fold${o.duo ? ' is-duo' : ''}">
      <div class="v2-fold-r1">
        ${o.duo || o.noBack ? '' : ui.btn(Object.assign({ icon: 'chevron-left', title: 'Back' }, o.back))}
        <div class="v2-fold-id"${nav(o.titleNav)}>
          ${o.duo
            ? `<div class="v2-fold-one"><span class="v2-fold-t">${t.title}</span><span class="v2-fold-sep">·</span>${ui.lineMark(mark)}<span class="v2-fold-w k-tone--${mark || 'idle'}">${st.word}</span></div>`
            : `<div class="v2-fold-t">${t.title}</div><div class="v2-fold-s">${ui.lineMark(mark)}<span class="v2-fold-w k-tone--${mark || 'idle'}">${st.word}</span>${st.text ? `<span class="k-sep">·</span><span>${st.text}</span>` : ''}</div>`}
        </div>
        ${o.duo ? '' : ui.btn(Object.assign({ icon: 'ellipsis', title: 'More' }, o.more))}
      </div>
      <div class="v2-fold-ask">${o.ask}</div>
    </div>`;
  }

  /** The page over the keyboard: scrolled to its foot, so the latest round sits just above the composer (V2 rule 8) and
      the page above it fills the room up to the folded bar, passing under its scroll edge (never an empty band). The
      tail starts at the steps line and is anchored at the bottom, so whatever does not fit is what scrolled away.
      p: paperBody options (count: rounds in the block) */
  function kbTail(t, p) {
    return paperBody(t, Object.assign({ from: 'steps', anchor: 'bottom', noWork: true }, p, { count: p.count || 3, head: p.head !== false }));
  }

  /** The keyboard's predictive bar follows the field: the typed word in quotes, then likely words; sentence starters
      over an empty field (the kit's default reads “onsen” everywhere). */
  const SUGG_NEXT = { 5: ['and', 'please'], cheaper: ['and', 'too'], notes: ['and', 'too'], breakf: ['breakfast', 'breakfasts'] };
  function kbFor(text, o) {
    let sugg = ['I', 'The', 'I’m'];
    if (text) {
      const last = String(text).trim().split(/\s+/).pop();
      if (/[?.!]$/.test(last)) sugg = ['I', 'Thanks', 'The'];
      else {
        const w = last.replace(/[^\w’']/g, '');
        sugg = [w].concat(SUGG_NEXT[w] || ['and', 'please']);
      }
    }
    return Object.assign({ suggestions: sugg }, o);
  }

  /** The ask folded to one line. The bar's subtitle already carries the needs dot, so the line leads with the ask's name
      alone (one blue dot in the folded header). */
  function kyotoAskLine(o) {
    o = o || {};
    if (o.answered) return `<div class="v2-al-done">${ui.askCard({ answered: `You chose ${optLabel('hanami')}`, undo: o.undo })}</div>`;
    return ui.askLine({ title: A1.title, mark: '', ok: Object.assign({ label: `OK: ${A1.options[0].short}` }, o.ok), options: o.opt });
  }

  function iphoneKyotoKeyboard(s) {
    const t = (s.D || D0).todo('t01');
    const per = s.sent ? { [s.sent.round.id]: { status: s.sent.status, nav: { undo: swap(KYKB + '~sent-undone') }, hide: s.sent.hide, extraItems: s.sent.extraItems } } : {};
    return ui.page({
      top: foldedHead(t, {
        // back and the title follow the state: an answer is gone from the list at once; a reply lands on the paper
        back: pop(s.answered ? LIST + ANSWER_AT.hanami.idle : LIST), more: say('Opens: the ⋯ menu'),
        titleNav: swap(KYOTO + (s.answered ? ANSWER_AT.hanami.kyoto : s.sent ? (s.undone ? '~sent-done-undone' : '~sent-done') : '')),
        state: s.answered ? { phase: 'working', word: 'Working', text: `Persona is booking ${optLabel('hanami')}` } : null,
        sub: t.state.short,
        ask: kyotoAskLine({ answered: s.answered, undo: swap(KYKB), ok: swap(KYKB + '~answered'), opt: swap(KYOTO) }),
      }),
      body: kbTail(t, { L: { conv: push(CONV), steps: say('Unfolds the steps'), roundNav: { undo: swap(KYKB + '~sent-undone') } }, extra: s.sent ? [s.sent.round] : null, per, count: s.count }),
      bottom: composer(t, { text: s.text, send: s.text ? swap(KYKB + '~sent') : null }),
      cls: 'v2-kbpage',
    });
  }

  const TEA_TEXT = TEA.text;
  const KB_STATES = [
    ['', 'iPhone · Kyoto with the keyboard up (V2-03)', 'B’s keyboard budget: the title and state fold into the bar (60), the ask to one line (44), and the latest round stays above the composer; while there is text the label “To Persona ▾” stands above the field. About 370 pt of the to-do shows. ↑ sends.', { text: TEA_TEXT }, true],
    ['sent', 'Keyboard · Sending', 'The round “You · now …” appears with its state: Sending (faint, under 0.1 s). The draft is kept until the hub stores it.', { text: TEA_TEXT, sent: { round: TEA, status: 'Sending', hide: ['reply', 'scope'] } }, false, { auto: { go: KYKB + '~sent-2', after: 400, transition: 'none' } }],
    ['sent-2', 'Keyboard · Processing', 'The hub stored it: Processing. The field clears; the keyboard stays up. Nothing opens.', { sent: { round: TEA, status: 'Processing', hide: ['reply', 'scope'] } }, false, { auto: { go: KYKB + '~sent-3', after: 800, transition: 'none' } }],
    ['sent-3', 'Keyboard · Delivered to Persona', 'Taken by the addressee: Delivered to Persona, said once (the line is replaced as it advances).', { sent: { round: TEA, status: 'Delivered to Persona', hide: ['reply', 'scope'] } }, false, { auto: { go: KYKB + '~sent-4', after: 2500, transition: 'none' } }],
    ['sent-4', 'Keyboard · the reply and the scope', 'The reply lands in the round (“I’ll look for one on Apr 5 near Gion.”) with the scope line “Scope: + tea ceremony on Apr 5 · Undo”; the state is now the round’s ✓.', { sent: { round: TEA } }],
    ['sent-undone', 'Keyboard · scope undone', 'Undo on the scope line removes it and says so in the round.', { undone: true, sent: { round: TEA, hide: ['scope'], extraItems: [{ kind: 'undone', text: 'Scope change undone' }] } }],
    ['answered', 'Keyboard · answered in place', 'OK: Hanami answers from the folded ask (“✓ You chose Ryokan Hanami · Undo”); the bar reads Working and the asked line loses its blue mark (the choice is said once). ‹ returns to a list with N = 2.', { text: TEA_TEXT, answered: true, D: view(ANSWERED) }],
  ];
  KB_STATES.forEach(([st, title, note, s, important, extra]) => {
    Kit.frame(V, KYKB + (st ? '~' + st : ''), Object.assign({ device: 'iphone', title, note, important: !!important, chrome: { keyboard: kbFor(s.text) }, render: () => iphoneKyotoKeyboard(s) }, extra || {}));
  });

  /* ---- the whole Conversation (V2-04, V2-09, V2-12, web, Android) */

  const CONV_FILTERS = [{ id: 'all', label: 'All' }, { id: 'messages', label: 'Messages' }, { id: 'decisions', label: 'Decisions' }, { id: 'results', label: 'Results' }];

  function convFilters(sel, goes) {
    return `<div class="v2-cf">${ui.sectionBar(CONV_FILTERS.map((f) => Object.assign({}, f, goes[f.id] || say(`Shows ${f.label}`))), { selected: sel || 'all' })}</div>`;
  }

  /**
   * The ask row of the Conversation's header: the paper's ask folded, drawn once (V2-04, V2-09, V2-12).
   * One line where there is room, "● Needs you · Which ryokan? [OK: Hanami] ⌄" (the chevron unfolds the full card); in a
   * narrow column two lines, "● Needs you · Which ryokan?" over "[OK: Hanami] Options ⌄". A run's ask is its door,
   * "● Needs you · Widget’s plan [Review Plan ›]". o: { answered, ok, opt, door, narrow, open (the card is unfolded) }
   */
  function convAskRow(t, o) {
    if (o.answered) return `<div class="v2-askrow">${ui.stateLine('working', t.state.text, { mark: true, clamp: 1 })}</div>`;
    const ask = t.asks && t.asks[0] ? D0.ask(t.asks[0]) : null;
    if (!ask) return `<div class="v2-askrow">${ui.stateLine(t.phase, t.state.short || t.state.text, { word: t.state.word, mark: true, clamp: 1 })}</div>`;
    const plan = ask.kind === 'plan';
    const title = POINTER[ask.id] || ask.title;
    const btns = plan
      ? { door: Object.assign({ label: ask.door }, o.door) }
      : { ok: Object.assign({ label: `OK: ${A1.options[0].short}` }, o.ok), options: o.opt || true };
    const open = o.open ? ' is-open' : '';
    if (o.narrow) {
      return `<div class="v2-askrow is-two${open}"><div class="v2-askrow-t">${ui.lineMark('needs')}<span class="v2-askrow-w">${t.state.word}</span><span class="k-sep">·</span><span class="v2-askrow-q">${title}</span></div>${ui.askLine(Object.assign({ title: '', mark: '' }, btns))}</div>`;
    }
    return `<div class="v2-askrow is-chev${open}">${ui.askLine(Object.assign({ title, word: t.state.word }, btns))}</div>`;
  }

  /** The Conversation's ⋯: the same labels as the paper's ⋯ ("Decisions (2)"). */
  const decisionsLabel = (t) => ((t.decisions || []).length ? `Decisions (${t.decisions.length})` : 'Decisions');
  const convMenu = (t) => [{ label: 'Copy Link', toast: 'Copied' }, { label: 'Chat Topic…', toast: 'Opens: Chat Topic' }, { label: decisionsLabel(t), toast: 'Shows the decisions' }];
  const convMenuSay = (t) => say(`Opens: ${convMenu(t).map((m) => m.label).join(' · ')}`);

  function decisionsList(t) {
    return `<div class="v2-dl">${(t.decisions || []).map((d) => `<div class="v2-dl-r"><span class="v2-dl-ic">${I('checkmark', 13)}</span><div><div class="v2-dl-t">${d.text}</div><div class="v2-dl-s">${d.who === 'you' ? 'You' : d.who} · ${d.when}</div></div></div>`).join('')}</div>`;
  }
  function resultsList(t) {
    return `<div class="v2-dl">${(t.results || []).map((r) => `<div class="v2-dl-r"${nav(say(`Opens: ${r.label}`))}><span class="v2-dl-ic is-doc">${I('doc', 14)}</span><div><div class="v2-dl-t">${r.label} · ${r.outcome}</div>${r.detail ? `<div class="v2-dl-s">${r.detail}</div>` : ''}</div><span class="v2-dl-chev">${I('chevron-right', 12)}</span></div>`).join('')}</div>`;
  }

  /**
   * A to-do's whole Conversation as a page (pushed on phones; replacing the to-do in its column elsewhere).
   * Every view of it follows one anchoring rule: a thread shorter than the page starts under the header; a longer one
   * keeps its latest round above the composer (the top passes under the header).
   * c: { head: 'pinned' | 'column', back, more, filter, filterGo, answered, options, optionsOn, optionsClose, rounds,
   *      extra, open, per, composer: {…}, ok, opt, door, toast, menu, menuClose, narrow, runToggle }
   */
  function conversationPage(t, c) {
    const rounds = (c.rounds || t.rounds).concat(c.extra || []);
    const askRow = convAskRow(t, { answered: c.answered, ok: c.ok, opt: c.opt, door: c.door, narrow: c.narrow, open: c.options });
    const drop = c.options ? `<div class="v2-drop">${gluePrices(ui.askCardFor('a1', { on: c.optionsOn || {}, narrow: c.narrow }))}</div>` : '';
    const more = c.more === false ? false : c.more || convMenuSay(t);
    let top;
    if (c.head === 'column') {
      top = `<div class="v2-colhead">${ui.navbar({
        leading: [Object.assign({ icon: 'chevron-left', label: t.title }, c.back)],
        trailing: [Object.assign({ icon: 'ellipsis' }, more)],
      })}<div class="v2-colhead-in"><div class="v2-dropwrap">${askRow}${drop}</div>${convFilters(c.filter, c.filterGo || {})}</div></div>`;
    } else {
      top = ui.pinnedHeader({
        title: t.title, back: c.back, more, titleNav: c.back,
        row2: `<div class="v2-dropwrap">${askRow}${drop}</div>${convFilters(c.filter, c.filterGo || {})}`,
      });
    }
    let body;
    if (c.filter === 'decisions') body = `<div class="v2-thread">${decisionsList(t)}<i class="v2-fill"></i></div>`;
    else if (c.filter === 'results') body = `<div class="v2-thread">${resultsList(t)}<i class="v2-fill"></i></div>`;
    else {
      const resolved = t.answered ? (t.answered.declined ? 'declined' : 'answered') : null;
      body = `<div class="v2-thread">${thread(rounds, {
        filter: c.filter, open: c.open, askedSuffix: resolved ? '' : '(above)', resolved, addedAt: ADDED_AT[t.id],
        runNav: (it) => (it.who === 'Ryokan search' && c.runToggle ? swap(c.runToggle) : say(`Unfolds: ${it.who}’s steps`)),
        nav: { session: say('Ryokan search has ended: its session is under Agents') },
        per: c.per, past: c.past,
      })}<i class="v2-fill"></i></div>`;
    }
    const overlay = J([
      c.options ? `<div class="v2-dim"${nav(c.optionsClose)}></div>` : '',
      c.menu ? menuAt(convMenu(t), { top: 112, right: 16, close: c.menuClose }) : '',
      c.toast || '',
    ]);
    // while a toast shows, the thread's foot rises above it, so the toast never covers the change it reports
    return ui.page({ top, body, bottom: composer(t, c.composer || {}), overlay, cls: 'v2-convpage' + (c.head === 'column' ? ' v2-col' : '') + (c.options ? ' v2-opts' : '') + (c.toast ? ' v2-has-toast' : '') });
  }

  function iphoneConversation(s, ctx) {
    const D = s.D || D0;
    const t = D.todo('t01');
    const st = (x) => swap(CONV + (x ? '~' + x : ''));
    const base = st('');
    // ‹ returns to V2-02 at the same scroll position: the page or its rounds, whichever All 4 › was tapped on (V2-04)
    const from = ctx && ctx.live ? Kit.store.get('v2.convFrom', KYOTO) : KYOTO;
    return conversationPage(t, Object.assign({
      head: 'pinned', back: pop(s.back || from), more: st(s.menu ? '' : 'menu'), menu: s.menu, menuClose: base,
      filterGo: { all: st(''), messages: st('messages'), decisions: st('decisions'), results: st('results') },
      ok: st(ANSWER_AT.hanami.conv.slice(1)), opt: st(s.options ? '' : 'options'), optionsClose: base,
      optionsOn: Object.assign(optionsTo((id) => st(ANSWER_AT[id].conv.slice(1))), { later: base, dont: say('Asks first: Persona won’t book a ryokan'), discuss: st('answer') }),
      runToggle: CONV + (s.open ? '' : '~run'),
      composer: { send: say('Sends: the round appears at the foot'), labelNav: st('keyboard') },
    }, s));
  }

  const CONV_STATES = [
    ['', 'iPhone · the whole Conversation, pushed (V2-04)', 'B’s History: the pinned header carries the ask with [OK: Hanami] and ⌄ for the full card; filter chips All · Messages · Decisions · Results; day groups; every round open with its message state, tool work one line per run. The composer is the page’s, with the same draft. ‹ returns to V2-02 where you left it.', {}, true],
    ['messages', 'Conversation · Messages', 'Only the owner’s words and the steward’s replies: run lines, decisions and the Background round step aside.', { filter: 'messages' }],
    ['decisions', 'Conversation · Decisions', 'The recorded decisions: Dates: Apr 3–9 · Flights: option B.', { filter: 'decisions' }],
    ['results', 'Conversation · Results', 'What the work produced: Flights, the Ryokan shortlist.', { filter: 'results' }],
    ['options', 'Conversation · the full card', '⌄ (or the ask’s name) unfolds the full card over the thread, everything else dimmed; its buttons are the answer, so OK steps aside. Answering folds it back.', { options: true }],
  ].concat(OPTS.map((id) => [ANSWER_AT[id].conv.slice(1), id === 'hanami' ? 'Conversation · answered from the header' : `Conversation · ${optLabel(id)} on the full card`,
    `${id === 'hanami' ? 'OK: Hanami' : `${optLabel(id)} on the unfolded card`} answers: the header reads “◐ Working · Persona is booking ${optLabel(id)}”, with “✓ You chose ${optLabel(id)} · Undo” for 4 s above the thread’s foot; the asked line loses its blue mark (the choice is said once). ‹ returns to the page, answered.`,
    { answered: true, D: view(ANS(id)), back: KYOTO + ANSWER_AT[id].kyoto, toast: ui.toast(`${I('checkmark-circle-fill', 16)} You chose ${optLabel(id)}`, { action: 'Undo', actionNav: swap(CONV), bottom: 108 }) }]), [
    ['answered-msg', 'Conversation · answered by your message', 'From Discuss…: sending “Kaede, it’s cheaper” with the chip “Answer: Which ryokan?” records the answer on the ask: the round reads “✓ You chose Kaede · from your message”, the header “◐ Working · Persona is booking Kaede”.', { answered: true, D: view(ANS('kaede')), back: KYOTO + '~kaede-msg', extra: [answerMsgRound()], toast: ui.toast(`${I('checkmark-circle-fill', 16)} You chose ${optLabel('kaede')} · from your message`, { action: 'Undo', actionNav: swap(CONV + '~answer'), bottom: 108 }) }],
    ['run', 'Conversation · a run line unfolded', '▸ on a run line unfolds its latest 3 steps and Open Session › (the run has ended).', { open: { 'Ryokan search': true } }],
    ['menu', 'Conversation · ⋯ menu', 'Copy Link · Chat Topic… · Decisions (2), the same labels as the paper’s ⋯.', { menu: true }],
    ['answer', 'Conversation · Discuss…', 'Discuss… on the card opens the Conversation with the chip “Answer: Which ryokan? ×” and the keyboard up: an ordinary message never answers an ask by accident. ↑ records “Kaede” on the ask; × returns to the steward.', { chrome: true, composer: { answer: A1.title, answerNav: swap(CONV), text: ANSWER_MSG, send: swap(CONV + '~answered-msg') } }],
    ['keyboard', 'Conversation · typing', 'The same composer, keyboard up: the label “To Persona” above the field; the thread keeps its latest rounds in view.', { chrome: true, composer: { text: D0.todo('t01').extraRounds.v2a.text, send: swap(CONV + '~long') } }],
    ['long', 'Conversation · a longer exchange', 'Two more rounds at full zoom (the V2 extra rounds). The paper is one ‹ away.', { extra: [D0.todo('t01').extraRounds.v2a, D0.todo('t01').extraRounds.v2b] }],
  ]);
  CONV_STATES.forEach(([st, title, note, s, important]) => {
    Kit.frame(V, CONV + (st ? '~' + st : ''), {
      device: 'iphone', title, note, important: !!important,
      chrome: s.chrome ? { keyboard: kbFor(s.composer && s.composer.text) } : undefined,
      render: (ctx) => iphoneConversation(s, ctx),
    });
  });

  const CONVR = IP + 'conversation-release';
  Kit.frame(V, CONVR, {
    device: 'iphone',
    title: 'iPhone · Release’s whole Conversation, pushed',
    note: 'All 2 › or a round on V2-05: the same page for Release Weather App 1.3. The header carries the run’s ask as its door, “● Needs you · Widget’s plan [Review Plan ›]”; the thread shows Widget’s request only as a line. “To Release lead ▾” writes to the steward.',
    render: () => {
      const t = D0.todo('t02');
      return conversationPage(t, {
        // the run's ask on two lines: "● Needs you · Plan awaiting approval" over [Review Plan ›] (whole on a phone)
        head: 'pinned', back: pop(REL), door: push(SESS), narrow: true,
        filterGo: { all: swap(CONVR) },
        composer: { send: say('Sends: the round appears at the foot') },
      });
    },
  });

  /* ---- iPhone · Release (V2-05) */

  function releaseLinks(base, s, o) {
    o = o || {};
    return {
      conv: push(CONVR),
      tick: say('Asks first: Release lead is still working on this'),
      agents: swap(base + (s.agentsOpen ? '' : '~agents')),
      work: swap(base + (s.workOpen ? '' : '~work')),
      session: { widget: o.session || push(SESS) },
      a2: { door: o.session || push(SESS), later: say('Later: folds to one line until later today'), dont: say('Asks first: Release lead won’t build the widget') },
      scopeUndo: swap(REL + '~scope-undone'),
      roundNav: { sendNow: swap(REL + '~send-now'), undo: swap(REL + '~scope-undone') },
    };
  }

  const R2 = D0.todo('t02').extraRounds.r2;
  /** T02's first round once the plan is approved: the request becomes a decision and Widget works. */
  const approvedR1 = () => ({ r1: { hide: ['needs'], extraItems: [{ kind: 'decision', text: `${D0.run('widget').title}’s plan approved` }, { kind: 'live', who: D0.run('widget').title, text: view(APPROVED).run('widget').latest }] } });
  const S5_TEXT = R2.text;

  function iphoneRelease(s) {
    const D = s.D || D0;
    const t = D.todo('t02');
    const L = releaseLinks(REL, s);
    if (s.kb) return iphoneReleaseKeyboard(t, s, L);
    const p = Object.assign({ L, agentsOpen: s.agentsOpen, workOpen: s.workOpen }, s.p);
    return ui.page({
      top: phoneTop(t, { back: pop(s.approved ? LIST + '~approved' : LIST), more: swap(s.menu ? REL : REL + '~menu'), scrolled: s.p && (s.p.from || s.p.anchor) }),
      body: paperBody(t, p),
      bottom: composer(t, { labelNav: swap(REL + '~typing') }).replace('class="k-tc"', `class="k-tc"${Kit.goAttr(REL + '~typing', 'none')}`),
      overlay: J([
        s.menu ? menuAt(todoMenu(['Copy Link', 'Move to Project…', 'Someday', 'Chat Topic…', 'Start an Agent on a Computer…', 'Delete…']), { top: 112, right: 16, close: swap(REL), width: 316 }) : '',
      ]),
      cls: p.anchor === 'bottom' ? 'v2-tail' : '',
    });
  }

  /** S5 with the keyboard up: the folded bar, the door on one line, the S5 round above the composer. */
  function iphoneReleaseKeyboard(t, s, L) {
    const ask = ui.askLine({ title: `${D0.run('widget').title}: ${D0.ask('a2').title}`, mark: '', door: Object.assign({ label: D0.ask('a2').door }, push(SESS)) });
    const st = stateV2(t);
    const menuItems = (t.composer.menu || []).map((m) => Object.assign({ label: m.label, sub: m.sub, check: m.check }, m.run === 'release-notes' ? swap(REL + '~to-notes') : m.run ? say(`Writes to ${m.label} directly`) : m.label === 'Persona' ? say('Writes to Persona') : swap(REL + '~typing')));
    // both menus open above the label row, next to the chip that opened them, so the chip stays in view
    const c = s.toNotes
      ? { text: S5_TEXT, run: 'Release notes', labelNav: swap(REL + '~to-menu'), mode: 'After its step', modeNav: swap(REL + (s.modeMenu ? '~to-notes' : '~mode-menu')), send: say('Queued for Release notes · after its step'),
        modeMenu: s.modeMenu ? ui.menu([Object.assign({ label: 'After its step', check: true }, swap(REL + '~to-notes')), { label: 'Now', sub: 'Send Now', toast: 'Sends now' }], { align: 'leading', width: 220 }) : '' }
      : { text: s.text, labelNav: swap(REL + (s.toMenu ? '~typing' : '~to-menu')), send: s.text ? swap(REL + '~s5') : null, labelMenu: s.toMenu ? ui.menu(menuItems, { align: 'leading', width: 290 }) : '' };
    const per = s.s5 ? { [s.s5.id]: s.roundOpts || {}, r1: { past: s.past } } : {};
    return ui.page({
      top: foldedHead(t, { back: pop(LIST), more: say('Opens: the ⋯ menu'), titleNav: swap(s.s5 ? REL + '~s5-done' : REL), state: st, ask }),
      body: kbTail(t, { L, extra: s.s5 ? [s.s5] : null, per, count: s.count || 1, head: s.head !== false }),
      bottom: composer(t, c),
      cls: 'v2-kbpage',
    });
  }

  const REL_STATES = [
    ['', 'iPhone · Release 1.3 with several Agents (V2-05)', 'The steward is Release lead (“Tell Release lead about this…”). The ask is a door, “From Widget”, with Review Plan ›. V2 rule 7: the Agents line “3 Agents ● Widget ◐ Notes ✓ Screens ⌄” and the state “Needs you · 1 of 3 Agents asks”. Work details stays folded.', {}, true],
    ['agents', 'Release · Agents unfolded', 'One row per run: Widget · worker · Needs you · Open Session ›; Release notes · helper · Working · Open Session ›; Screenshots · helper · Done · 6 images.', { agentsOpen: true }],
    ['work', 'Release · Work details', 'The one folded line for code work, open (the page scrolled to its foot): the runs by computer, the code change, Done when.', { workOpen: true, p: { anchor: 'bottom' } }],
    ['menu', 'Release · ⋯ menu', 'Copy Link · Move to Project… · Someday · Chat Topic… · Start an Agent on a Computer… · Delete…', { menu: true }],
    ['typing', 'Release · typing (S5)', 'The keyboard folds the page; while there is text the label “To Release lead ▾” stands above the field. ↑ sends.', { kb: true, text: S5_TEXT }],
    ['to-menu', 'Release · the addressee menu', 'Release lead ✓ · Persona · Widget (worker · Needs you) · Release notes (helper · Working): steering a run directly.', { kb: true, text: S5_TEXT, toMenu: true }],
    ['to-notes', 'Release · to Release notes', 'Picking Release notes: the label turns purple, “To Release notes ▾”, and a mode menu appears, “After its step ▾”.', { kb: true, toNotes: true }],
    ['mode-menu', 'Release · After its step ▾', 'The mode menu: After its step ✓ · Now (Send Now).', { kb: true, toNotes: true, modeMenu: true }],
    ['s5', 'S5 · Sending', 'The round appears: Sending.', { kb: true, s5: R2, count: 2, roundOpts: { status: 'Sending', hide: ['reply', 'relay', 'scope', 'live'] }, text: S5_TEXT }, false, { auto: { go: REL + '~s5-2', after: 400, transition: 'none' } }],
    ['s5-2', 'S5 · Processing', 'Processing; the field clears.', { kb: true, s5: R2, count: 2, roundOpts: { status: 'Processing', hide: ['reply', 'relay', 'scope', 'live'] } }, false, { auto: { go: REL + '~s5-3', after: 800, transition: 'none' } }],
    ['s5-3', 'S5 · Delivered to Release lead', 'Delivered to Release lead, and its reply: “Adding it to 1.3. I’ll tell Release notes after its current step.”', { kb: true, s5: R2, count: 2, roundOpts: { status: 'Delivered to Release lead', hide: ['relay', 'scope', 'live'] } }, false, { auto: { go: REL + '~queued', after: 1800, transition: 'none' } }],
    ['queued', 'S5 · Queued for Release notes', 'The relay line “↗ Queued for Release notes · after this step [Send Now]” and the scope line “Scope: + home-screen widget in the release notes · Undo”. After 6 s it is taken.', { kb: true, s5: R2, roundOpts: { hide: ['live'], relay: 'queued' } }, false, { auto: { go: REL + '~taken', after: 6000, transition: 'none' } }],
    ['taken', 'S5 · taken by Release notes', '“↗ Delivered to Release notes” and the run line “◐ Release notes · adding the widget”: who received it and that it is acted on.', { kb: true, s5: R2, past: ['Release notes'], roundOpts: { relay: 'delivered' } }],
    ['send-now', 'S5 · Send Now', 'Send Now on the queued line delivers at once, the run’s own send-now: “↗ Sent now to Release notes” (never an interrupt).', { kb: true, s5: R2, past: ['Release notes'], roundOpts: { relay: 'now' } }],
    ['scope-undone', 'S5 · scope undone', 'Undo on the scope line removes the scope entry and posts “Scope change undone” in the round.', { kb: true, s5: R2, past: ['Release notes'], roundOpts: { relay: 'delivered', hide: ['scope'], extraItems: [{ kind: 'undone', text: 'Scope change undone' }] } }],
    ['s5-done', 'S5 · on the page', 'Keyboard down, the page scrolled to its rounds: the S5 message is a new round in the Conversation block, latest at the foot, with its scope line and Undo (said once: the round is the record). R1’s Release notes line now reads as past.', { p: { anchor: 'bottom', extra: [R2], roundOpts: {}, per: { r2: { relay: 'delivered' }, r1: { past: ['Release notes'] } } } }],
    ['approved', 'Release · after the plan is approved', 'Back from Widget’s session: the door is gone, the state reads “Working · 2 Agents” (V2-11), Widget is working. N = 2 everywhere.', { D: view(APPROVED), approved: true, p: { per: approvedR1() } }],
  ];
  REL_STATES.forEach(([st, title, note, s, important, extra]) => {
    Kit.frame(V, REL + (st ? '~' + st : ''), Object.assign({ device: 'iphone', title, note, important: !!important, chrome: s.kb ? { keyboard: kbFor(s.text || (s.toNotes ? S5_TEXT : '')) } : undefined, render: () => iphoneRelease(s) }, extra || {}));
  });

  /* ---- an Agent's session (V2-06 = V1-06; V2-11 in the column) */

  /**
   * The Widget run's own session, the home of a2: the header names the to-do and points down to the plan (drawn once);
   * the plan card sits at the foot above the composer, which writes to Widget directly.
   * c: { head: 'pinned' | 'column', back, approved, approveNav, undo, menu, more, inset }
   */
  function sessionPage(c) {
    const t = D0.todo('t02');
    const s = t.session;
    const run = D0.run('widget');
    const a2 = D0.ask('a2');
    const stateRowHtml = c.approved
      ? `<div class="v2-sess-st"><b>${run.title}</b><span class="k-sep">·</span>${ui.lineMark('working')}<span class="k-tone--working v2-w">Working</span><span class="k-sep">·</span><span>building the widget</span></div>`
      : `<div class="v2-sess-st"><b>${run.title}</b><span class="k-sep">·</span>${ui.lineMark('needs')}<span class="k-tone--needs v2-w">Needs you</span><span class="k-sep">·</span><span>plan below ↓</span></div>`;
    let top;
    if (c.head === 'column') {
      top = `<div class="v2-colhead">${ui.navbar({ leading: [Object.assign({ icon: 'chevron-left', label: t.title }, c.back)], trailing: [Object.assign({ icon: 'ellipsis' }, c.more)] })}<div class="v2-colhead-in">${stateRowHtml}</div></div>`;
    } else {
      top = ui.pinnedHeader({ title: t.title, back: c.back, more: c.more, titleNav: c.back, row2: `<div class="k-ph-r2">${stateRowHtml}</div>` });
    }
    const body = `<div class="v2-sess">
      <div class="v2-sess-who">${s.header}</div>
      <div class="v2-sess-text">${s.text}</div>
      ${ui.fold(s.fold, { go: undefined })}
    </div>`;
    const card = c.approved
      ? ui.askCard({ answered: a2.approvedText, undo: c.undo })
      // P1 at the home: Edit · Don’t Start, then Approve, then the link row (three outcomes on every ask, §2.7): Later
      // here; Don’t Start is the plan's own decline
      : ui.askCard({
        title: a2.title, plan: a2.plan, estimate: a2.estimate,
        secondary: (a2.secondary || []).map((x) => Object.assign({ label: x }, x === 'Edit' ? say('Opens: Edit the plan') : say('Asks first: Widget won’t start'))),
        primary: Object.assign({ label: a2.primary }, c.approveNav),
        links: [Object.assign({ label: 'Later' }, say('Later: the plan waits until later today'))],
      });
    return ui.page({
      top, body,
      bottom: `<div class="v2-pending">${card}</div>${ui.todoComposer({ to: { label: run.title, run: true, menu: true, toast: 'Menu: Widget ✓ · Release lead' }, placeholder: `Message ${run.title}…`, inset: c.inset })}`,
      overlay: c.menu ? menuAt([].concat(...s.menu.map((m) => (m === 'Stop' ? ['-'] : []).concat([{ label: m, toast: m === 'Open Full Session' ? 'Opens the Agent’s full page: terminal, files, changes' : `Opens: ${m}`, danger: m === 'Stop' }]))), { top: 112, right: 16, close: c.menuClose }) : '',
      cls: 'v2-sesspage' + (c.head === 'column' ? ' v2-col' : ''),
    });
  }

  [
    ['', 'iPhone · Widget’s session, pushed (V2-06)', 'Identical to V1-06: the run’s own chat with tool work on one line; the header names the to-do and points down to the plan (drawn once); the plan card at the foot; “To Widget ▾” (purple) writes to it directly.', {}],
    ['approved', 'Widget session · approved', 'Approve: the card becomes “✓ Approved · Undo”; the header reads “Widget · ◐ Working · building the widget”. ‹ returns to V2-05, updated.', { approved: true }],
    ['menu', 'Widget session · ⋯ menu', 'Open Full Session (the Agent’s full page) · Try Another Way · Stop.', { menu: true }],
  ].forEach(([st, title, note, s]) => {
    Kit.frame(V, SESS + (st ? '~' + st : ''), {
      device: 'iphone', title, note,
      render: () => sessionPage(Object.assign({
        head: 'pinned', back: pop(s.approved ? REL + '~approved' : REL), approveNav: swap(SESS + '~approved'), undo: swap(SESS),
        more: swap(s.menu ? SESS : SESS + '~menu'), menuClose: swap(SESS),
      }, s)),
    });
  });

  /* ------------------------------------------------------------------ iPhone Duo */

  const DO = 'duo-outer/';
  const DIP = 'duo-inner-portrait/';
  const DUO_LIST = DIP + 'list-todo';
  const DUO_CONV = DIP + 'conversation-in-column';

  // The side column's page items (E4): 48-pt symbols in one shared glass, no names (only the Duo tab bar has names).
  // Raw items, so a tap can show a toast (the kit's items take go only).
  const sideBtn = (icon, title, o) => `<span class="dev-side-item" title="${Kit.esc(title)}"${nav(o)}>${I(icon, 21)}</span>`;
  const sideMore = (text) => sideBtn('ellipsis', 'More', say(text));

  function outerPage(top, body, bottom, overlay) {
    return ui.page({ top: `<div class="v2-outer-top">${top || ''}</div>`, body, bottom, overlay, cls: 'v2-outer' });
  }
  const TODO_MORE = 'Opens: Copy Link · Move to Project… · Someday · Delete…';

  /**
   * The outer display's list. ☰ (the Back position) lays the sidebar over the list; + and ⋯ share one glass 12 pt below
   * it (E4: two page items, so the column stays clear of the Duo tab bar; Search sits in ⋯ as on a phone's drawer).
   * s: { D (after an answer: re-filed, N = 2), opt, sidebar }
   */
  function outerList(s) {
    const D = s.D || D0;
    const base = DO + 'list' + (s.opt ? ANSWER_AT[s.opt].idle : '');
    const content = outerPage(
      `<div class="v2-outer-title">${ui.titleMenu(say('Opens: the place menu · All To-dos ✓ · Today · Upcoming · the projects'))}</div>${bar({ D, go: { all: fade(base) } })}`,
      listBody({
        D,
        rowNav: (t) => (t.id === 't01' ? fade(DO + 'kyoto' + (s.opt ? ANSWER_AT[s.opt].round : '')) : t.id === 't02' ? fade(DO + 'release') : say(`Opens: ${t.title}`)),
        ans: {
          t01: { ok: fade(DO + 'kyoto' + ANSWER_AT.hanami.duo), opt: fade(DO + 'kyoto') },
          t02: { ok: say('Opens: Widget’s session') },
          t03: { ok: say('Moves to Waiting: Starts when Desk Mac is back'), opt: say('Unfolds: Use MacBook Instead · Later') },
          t13: { ok: say('Opens: the reply') },
        },
        foldNav: { can_wait: say('Unfolds Can wait'), someday: say('Unfolds Someday') },
      }),
      '',
      s.sidebar ? ui.drawer({ content: tree('todos', { todos: fade(base), kyoto: say('Opens: Kyoto trip, the place') }, { counts: D.counts }), scrimNav: fade(base), width: 320 }) : '',
    );
    return {
      content,
      side: {
        // after an answer the sidebar is not built (a toast names it: screens.js afterRender)
        back: { icon: 'line-3-horizontal', label: 'Sidebar', go: s.sidebar ? base : s.opt ? undefined : DO + 'list~sidebar', transition: 'fade' },
        actions: [sideBtn('plus', 'New To-Do', say('Opens: New To-Do')), sideMore('Opens: Search To-dos · Sort · Show Someday · Show Done Today')],
        tabs: { selected: 0 },
      },
    };
  }

  Kit.frame(V, DO + 'list', {
    device: 'duo-outer',
    title: 'Duo outer · To-dos list',
    note: 'The list at 382 wide. In the side column (E4): ☰ alone at the top, + and ⋯ in one glass 12 pt below it (Search is in ⋯), the Duo tab bar and the face at its foot. A row cross-fades to its page.',
    unfold: DUO_LIST,
    render: () => outerList({}),
  });
  Kit.frame(V, DO + 'list~sidebar', {
    device: 'duo-outer',
    title: 'Duo outer · the sidebar over the list',
    note: '☰ lays the sidebar (320) over the dimmed list; under To-dos only the starred places (V2 rule 5). The scrim or ☰ closes it.',
    unfold: DUO_LIST + '~sidebar',
    render: () => outerList({ sidebar: true }),
  });
  OPTS.forEach((id) => {
    Kit.frame(V, DO + 'list' + ANSWER_AT[id].idle, {
      device: 'duo-outer',
      title: `Duo outer · list after ${optLabel(id)}`,
      note: `Back from the answered page: T01 re-filed under Working (“Persona is booking ${optLabel(id)}”), no OK on its row. N = 2.`,
      unfold: DUO_LIST + ANSWER_AT[id].duo,
      render: () => outerList({ D: view(ANS(id)), opt: id }),
    });
  });

  /** The outer display's Kyoto page. s: { opt (answered: the card's 4 s, or after it the round), round } */
  function outerKyoto(s) {
    const D = s.opt ? view(ANS(s.opt)) : D0;
    const t = D.todo('t01');
    const L = {
      conv: fade(DO + 'conversation-kyoto'), steps: say('Unfolds the steps'), undo: fade(DO + 'kyoto'),
      a1: Object.assign(optionsTo((id) => fade(DO + 'kyoto' + ANSWER_AT[id].duo)), {
        later: say('Folds to one line: Which ryokan? · later today'), dont: say('Asks first: Persona won’t book a ryokan'), discuss: fade(DO + 'conversation-kyoto'),
      }),
    };
    const p = !s.opt ? { L } : s.round ? { L, ask: 'none', extra: [answerRound(optLabel(s.opt))], anchor: 'bottom' } : { L, ask: 'answered' };
    return {
      content: outerPage('', paperBody(t, p), composer(t, { inset: 8 }).replace('class="k-tc"', `class="k-tc"${Kit.goAttr(DO + 'kyoto-keyboard', 'fade')}`)),
      side: { back: fade(DO + 'list' + (s.opt ? ANSWER_AT[s.opt].idle : '')), actions: [sideMore(TODO_MORE)], tabs: { selected: 0 } },
    };
  }

  Kit.frame(V, DO + 'kyoto', {
    device: 'duo-outer',
    title: 'Duo outer · Kyoto to-do',
    note: 'The V2-02 page at 382 wide; Back and ⋯ in the side column. The composer opens V2-07; All 4 › cross-fades to the Conversation. Every answer on the card works here.',
    unfold: DUO_LIST,
    render: () => outerKyoto({}),
  });
  OPTS.forEach((id) => {
    Kit.frame(V, DO + 'kyoto' + ANSWER_AT[id].duo, {
      device: 'duo-outer',
      title: `Duo outer · ${optLabel(id)} chosen`,
      note: `The card collapses to “✓ You chose ${optLabel(id)} · Undo” for 4 s; the state reads Working. Back returns to a list where N = 2.`,
      unfold: DUO_LIST + ANSWER_AT[id].duo,
      auto: { go: DO + 'kyoto' + ANSWER_AT[id].round, after: 4000, transition: 'none' },
      render: () => outerKyoto({ opt: id }),
    });
    Kit.frame(V, DO + 'kyoto' + ANSWER_AT[id].round, {
      device: 'duo-outer',
      title: `Duo outer · ${optLabel(id)} as a round`,
      note: `After 4 s the answer is a round at the foot of the Conversation block, “You chose ${optLabel(id)} · from the card”.`,
      unfold: DUO_LIST + ANSWER_AT[id].duo,
      render: () => outerKyoto({ opt: id, round: true }),
    });
  });

  Kit.frame(V, DO + 'kyoto-keyboard', {
    device: 'duo-outer', important: false,
    title: 'Duo outer · Kyoto, keyboard up (V2-07)',
    note: 'The header folds to one 56-pt line (title · state) plus the one-line ask (44), with the latest round above the composer and the page above it filling the room: about 280 pt of the to-do over a keyboard of ≈ 300 pt (to measure). Back and ⋯ stand at the top of the side column, the Duo tab bar and the face at its foot. Unfold keeps the draft.',
    chrome: { keyboard: kbFor('also check breakf') },
    unfold: DUO_LIST + '~draft',
    render: () => {
      const t = D0.todo('t01');
      return {
        content: ui.page({
          top: foldedHead(t, { duo: true, ask: kyotoAskLine({ ok: fade(DO + 'kyoto' + ANSWER_AT.hanami.duo), opt: fade(DO + 'kyoto') }), titleNav: fade(DO + 'kyoto') }),
          body: kbTail(t, { L: { conv: fade(DO + 'conversation-kyoto'), steps: say('Unfolds the steps') }, count: 2 }),
          bottom: composer(t, { text: t.drafts.duo, inset: 8, send: say('Sends: Sending → Processing → Delivered to Persona') }),
          cls: 'v2-kbpage v2-outer',
        }),
        side: { back: fade(DO + 'kyoto'), actions: [sideMore(TODO_MORE)], tabs: { selected: 0 } },
      };
    },
  });

  Kit.frame(V, DO + 'conversation-kyoto', {
    device: 'duo-outer',
    title: 'Duo outer · the whole Conversation',
    note: 'The V2-04 page at 382 wide: the header keeps the ask; Back cross-fades to the to-do. Pages cross-fade in place; nothing slides.',
    unfold: DUO_CONV,
    render: () => {
      const t = D0.todo('t01');
      return {
        content: conversationPage(t, {
          head: 'pinned', back: false, more: false, narrow: true,
          ok: fade(DO + 'kyoto' + ANSWER_AT.hanami.duo), opt: say('Unfolds the full card over the thread'),
          composer: { inset: 8 },
        }),
        side: { back: fade(DO + 'kyoto'), actions: [sideMore(convMenuSay(t).toast)], tabs: { selected: 0 } },
      };
    },
  });

  Kit.frame(V, DO + 'release', {
    device: 'duo-outer',
    title: 'Duo outer · Release 1.3',
    note: 'The V2-05 page at 382 wide: the door “From Widget”, the Agents line, Results and the latest rounds; Back and ⋯ in the side column. Where the Duo’s Release selection folds to.',
    unfold: DUO_LIST + '~release',
    render: () => {
      const t = D0.todo('t02');
      const L = {
        conv: say('Opens: Release’s whole Conversation'), agents: say('Unfolds the Agents'), work: say('Unfolds Work details'),
        session: { widget: say('Opens: Widget’s session') }, a2: A2_SAY({ door: say('Opens: Widget’s session, the plan') }),
      };
      return {
        content: outerPage('', paperBody(t, { L }), composer(t, { inset: 8 })),
        side: { back: fade(DO + 'list'), actions: [sideMore(TODO_MORE)], tabs: { selected: 0 } },
      };
    },
  });

  /* ---- Duo inner, upright: list 334 ┊ to-do 335 (V2-08, V2-09) */

  function duoList(s) {
    const D = s.D || D0;
    const base = DUO_LIST;
    return ui.page({
      top: ui.navbar({
        leading: [Object.assign({ icon: 'line-3-horizontal', title: 'Sidebar' }, swap(base + '~sidebar'))],
        center: ui.titleMenu(Object.assign({ title: 'All To-dos', open: !!s.menu, items: placeItems('all', base) }, swap(s.menu ? base : base + '~menu'))),
        trailing: [{ icon: 'plus', toast: 'Opens: New To-Do' }, { icon: 'ellipsis', toast: NARROW_MORE }],
        below: bar({ D, counts: s.counts, go: { all: swap(base) }, group: false }),
      }),
      body: listBody({
        D, counts: s.counts || D, selected: s.sel, narrow: true,
        rowNav: (t) => (t.id === 't01' ? swap(base + (s.opt ? ANSWER_AT[s.opt].duo : '')) : t.id === 't02' ? swap(base + '~release') : say(`Opens: ${t.title}`)),
        ans: { t01: { ok: swap(base + ANSWER_AT.hanami.duo), opt: swap(base) }, t02: { ok: say('Opens: Widget’s session in the to-do half') }, t03: { ok: say('Moves to Waiting: Starts when Desk Mac is back'), opt: say('Unfolds the full card') } },
        chose: s.chose, pointer: s.pointer, foldNav: { can_wait: say('Unfolds Can wait'), someday: say('Unfolds Someday') },
      }),
      overlay: s.menu ? `<div class="v2-catch is-under"${nav(swap(base))}></div>` : '',
      cls: 'v2-listpage',
    });
  }

  function duoPaper(t, s) {
    const base = DUO_LIST;
    const kyoto = t.id === 't01';
    const L = kyoto
      ? { conv: fade(DUO_CONV), steps: say('Unfolds the steps'), undo: swap(base), a1: A1_SAY(Object.assign(optionsTo((id) => swap(base + ANSWER_AT[id].duo)), { discuss: fade(DUO_CONV) })) }
      : { conv: say('Opens: the whole Conversation in this half'), agents: say('Unfolds the Agents'), work: say('Unfolds Work details'), session: { widget: say('Opens: Widget’s session in this half') }, a2: A2_SAY({ door: say('Opens: Widget’s session in this half') }) };
    if (s.kb) {
      // keyboard up in the to-do half: the page folds as on the phone (V2 rule 8); the list half stays put
      return ui.page({
        top: foldedHead(t, { noBack: true, sub: t.state.short, more: say(TODO_MORE), titleNav: say('Tap outside the field: the keyboard goes down'), ask: kyotoAskLine({ ok: swap(base + ANSWER_AT.hanami.duo), opt: swap(base) }) }),
        body: kbTail(t, { L, narrow: true, shortAsk: true, count: 3 }),
        bottom: composer(t, { text: s.text, send: say('Sends: Sending → Processing → Delivered to Persona') }),
        cls: 'v2-pane v2-kbpage',
      });
    }
    // a 335-pt half: the card's one-line body (V2-08), R2 down to its result, so R3 (the round that asked) shows above
    // the composer
    return ui.page({
      top: ui.navbar({ trailing: [{ icon: 'ellipsis', toast: TODO_MORE }] }),
      body: paperBody(t, Object.assign({ L, narrow: true, shortAsk: true, noBg: true, oneLine: true, count: 2, maxItems: 2, per: { r2: { maxItems: 1 } } }, s.p)),
      bottom: composer(t, { text: s.text, send: s.text ? say('Sends: Sending → Processing → Delivered to Persona') : null }),
      cls: 'v2-pane',
    });
  }

  function duoSidebar() {
    return `<div class="v2-over">${ui.drawer({ content: tree('todos', { todos: swap(DUO_LIST), kyoto: say('Opens: Kyoto trip, the place') }), scrimNav: swap(DUO_LIST), width: 320 })}</div>`;
  }

  const DUO_STATES = [
    ['', 'Duo open, upright · list ┊ to-do (V2-08)', 'The only two-pane state on the inner display: list 334 ┊ to-do 335 at the crease (x = 334.5, nothing straddles it). The to-do half shows the V2 page with its rounds and the ask’s home card, so the selected row only points at it (“● Which ryokan? · answer beside →”, drawn once). All 4 › replaces the right half (V2-09); another row swaps it, not animated.', { sel: 't01', pointer: { t01: true } }, true, { fold: DO + 'kyoto' }],
  ].concat(OPTS.map((id) => [ANSWER_AT[id].duo.slice(1), `Duo upright · ${optLabel(id)} chosen`, `${optLabel(id)} on the card answers: the card collapses to “✓ You chose ${optLabel(id)} · Undo” and the row confirms it once (“✓ You chose ${optLabel(id)}”, no second Undo) until it re-files; the asked line loses its blue mark. N (3) → (2) at once.`, { sel: 't01', opt: id, chose: { t01: optLabel(id) }, counts: view(ANS(id)), paperD: view(ANS(id)), p: { ask: 'answered' } }, false, { fold: DO + 'kyoto' + ANSWER_AT[id].duo }]), [
    ['release', 'Duo upright · Release selected', 'Another row swaps the to-do half, not animated; the ▐ selection moves. T01’s row gets its OK back (its home is no longer on screen); T02’s row points at the door beside it (“● Plan awaiting approval · open beside →”, drawn once).', { sel: 't02', todo: 't02', pointer: { t02: 'door' } }, false, { fold: DO + 'release' }],
    ['sidebar', 'Duo upright · sidebar over the list', '☰ lays the sidebar (320) over the left half, page dimmed; under To-dos only the starred places.', { sel: 't01', sidebar: true, pointer: { t01: true } }, false, { fold: DO + 'list~sidebar' }],
    ['menu', 'Duo upright · title menu', 'The place menu, the second way in.', { sel: 't01', menu: true, pointer: { t01: true } }, false, { fold: DO + 'list' }],
    ['draft', 'Duo upright · unfolded mid-draft (S9)', 'Unfolded from V2-07: the draft “also check breakf” and its caret survive in the to-do half’s composer, which folds the page as on the outer display; the selection is kept; the keyboard spans both halves.', { sel: 't01', text: D0.todo('t01').drafts.duo, kb: true, pointer: { t01: true } }, false, { fold: DO + 'kyoto-keyboard' }],
  ]);
  DUO_STATES.forEach(([st, title, note, s, important, extra]) => {
    Kit.frame(V, DUO_LIST + (st ? '~' + st : ''), Object.assign({
      device: 'duo-inner-portrait', title, note, important: !!important,
      chrome: s.kb ? { keyboard: kbFor(s.text) } : undefined,
      render: () => {
        const todo = (s.paperD || D0).todo(s.todo || 't01');
        return ui.split([
          { w: 334, html: duoList(s) + (s.sidebar ? duoSidebar() : '') },
          { w: 335, html: duoPaper(todo, s), line: false },
        ], { cls: 'v2-duo' });
      },
    }, extra || {}));
  });

  Kit.frame(V, DUO_CONV, {
    device: 'duo-inner-portrait',
    title: 'Duo upright · the Conversation in the to-do half (V2-09)',
    note: 'All 4 › replaces the right half with the whole Conversation; the left half keeps the list and its selection. The half cross-fades, so nothing crosses the crease. The list row’s ask shrinks to one line: the header beside it carries the ask (drawn once). “‹ Plan the Kyoto trip” comes back.',
    fold: DO + 'conversation-kyoto',
    render: () => {
      const t = D0.todo('t01');
      return ui.split([
        { w: 334, html: duoList({ sel: 't01', pointer: { t01: true } }) },
        { w: 335, html: conversationPage(t, { head: 'column', back: fade(DUO_LIST), narrow: true, ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') }) },
      ], { cls: 'v2-duo' });
    },
  });

  /* ------------------------------------------------------------------ iPad */

  const IPD = 'ipad11-landscape/';
  const IPAD_TODO = IPD + 'todo';
  const IPAD_SESS = IPD + 'session-in-column';
  const IPAD_CONV = IPD + 'conversation-in-column';
  const I13 = 'ipad13-landscape/';

  function ipadList(s, w) {
    const D = s.D || D0;
    return ui.page({
      top: ui.navbar({
        align: 'leading',
        center: ui.titleMenu(Object.assign({ title: 'All To-dos', open: !!s.menu, items: placeItems('all', s.listBase || IPAD_TODO), align: 'leading' },
          (s.listBase || IPAD_TODO) !== IPAD_TODO ? say('Opens: the place menu') : swap(s.menu ? IPAD_TODO : IPAD_TODO + '~menu'))),
        trailing: [{ icon: 'plus', toast: 'Opens: New To-Do' }, { icon: 'ellipsis', toast: NARROW_MORE }],
        below: bar({ D, counts: s.counts, go: { all: swap(s.listBase || IPAD_TODO) }, group: false }),
      }),
      body: listBody({
        D, counts: s.counts || D, selected: s.sel, narrow: w < 360, pointer: s.pointer,
        rowNav: s.rowNav, ans: s.ans, foldNav: { can_wait: say('Unfolds Can wait'), someday: say('Unfolds Someday') },
      }),
      overlay: s.menu ? `<div class="v2-catch is-under"${nav(swap(s.listBase || IPAD_TODO))}></div>` : '',
      cls: 'v2-listpage',
    });
  }

  function ipadPaper(t, s) {
    return ui.page({
      top: ui.navbar({ trailing: [{ icon: 'ellipsis', toast: 'Opens: Copy Link · Move to Project… · Someday · Delete…' }] }),
      body: `<div class="v2-readable">${paperBody(t, s.p)}</div>`,
      bottom: `<div class="v2-readable">${composer(t, { send: null })}</div>`,
      cls: 'v2-col',
    });
  }

  function ipad11(s) {
    const D = s.D || D0;
    const todo = D.todo(s.sel);
    const kyoto = s.sel === 't01';
    const L = kyoto
      ? { conv: swap(IPAD_CONV), steps: say('Unfolds the steps'), a1: A1_SAY({ discuss: swap(IPAD_CONV) }) }
      : { conv: swap(s.approved ? IPAD_TODO + '~approved' : IPAD_CONV + '~release'), agents: swap(IPAD_TODO + (s.agentsOpen ? '' : '~agents')), work: say('Unfolds Work details'), session: { widget: swap(IPAD_SESS) }, a2: A2_SAY({ door: swap(IPAD_SESS) }) };
    if (s.approved) L.conv = say('Opens: the whole Conversation in this column');
    // drawn once: T01's home card, or T02's door, is in the column beside the list, so the selected row only points at it
    const list = ipadList({
      D, sel: s.sel, menu: s.menu, listBase: IPAD_TODO, pointer: kyoto ? { t01: true } : { t02: 'door' },
      rowNav: (t) => (t.id === 't01' ? swap(IPAD_TODO + '~kyoto') : t.id === 't02' ? swap(s.approved ? IPAD_TODO + '~approved' : IPAD_TODO) : say(`Opens: ${t.title}`)),
      ans: { t01: { ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') }, t02: { ok: swap(IPAD_SESS) }, t03: { ok: say('Moves to Waiting: Starts when Desk Mac is back'), opt: say('Unfolds the full card') } },
    }, 320);
    return ui.split([
      { w: 400, html: tree('todos', { todos: swap(IPAD_TODO), kyoto: say('Opens: Kyoto trip, the place') }, { counts: D.counts }) },
      { w: 320, html: list, line: true },
      { flex: 1, html: ipadPaper(todo, { p: { L, agentsOpen: s.agentsOpen, per: s.per } }), line: true },
    ]);
  }

  [
    ['', 'iPad 11″ landscape · sidebar │ list │ to-do (V2-10)', 'Sidebar 400 │ list 320 │ to-do 474. V2 rule 5: under To-dos only the starred places (Kyoto trip, Weather App); the list’s title menu holds every place. The to-do column has no Conversation toggle. Review Plan › pushes Widget’s session inside this column (V2-11); the sidebar never steps aside.', { sel: 't02' }, true],
    ['agents', 'iPad · Agents unfolded', 'The Agents line unfolds in place: Open Session › on Widget pushes its session inside the column.', { sel: 't02', agentsOpen: true }],
    ['menu', 'iPad · the list’s title menu', 'Every place; the projects carry their blue part of N.', { sel: 't02', menu: true }],
    ['kyoto', 'iPad · Kyoto selected', 'Another row: a selection change, not animated. The column shows T01’s page with its rounds and the ask’s home card, so the row only points at it (drawn once). All 4 › pushes the Conversation inside the column.', { sel: 't01' }],
    ['approved', 'iPad · back from the approved plan', '“‹ Release Weather App 1.3” returns with the card gone and the state “Working · 2 Agents”; T02 moves to Working and N = 2 in the sidebar and the bar.', { sel: 't02', D: view(APPROVED), approved: true, per: approvedR1() }],
  ].forEach(([st, title, note, s, important]) => {
    Kit.frame(V, IPAD_TODO + (st ? '~' + st : ''), { device: 'ipad11-landscape', title, note, important: !!important, col: 0, sel: s.sel, render: () => ipad11(s) });
  });

  [
    ['', 'iPad 11″ · Widget’s session in the to-do column (V2-11)', 'Review Plan › pushes Widget’s session inside the to-do column (a hand-built stack: “‹ Release Weather App 1.3”, no edge swipe). The sidebar and the list are unchanged and the selection is kept; the list row’s door shrinks to one line because the plan’s home is beside it. The composer writes to Widget directly.', {}, true],
    ['approved', 'iPad · Widget’s plan approved', 'Approve resolves the plan: “✓ Approved · Undo”, the header reads Working. ‹ returns to V2-10 with the card gone.', { approved: true }],
  ].forEach(([st, title, note, s, important]) => {
    Kit.frame(V, IPAD_SESS + (st ? '~' + st : ''), {
      device: 'ipad11-landscape', title, note, important: !!important, col: 1, sel: 't02',
      render: () => {
        const D = s.approved ? view(APPROVED) : D0;
        const list = ipadList({
          D, sel: 't02', listBase: IPAD_TODO, pointer: s.approved ? null : { t02: true },
          rowNav: (t) => (t.id === 't01' ? swap(IPAD_TODO + '~kyoto') : t.id === 't02' ? swap(s.approved ? IPAD_TODO + '~approved' : IPAD_TODO) : say(`Opens: ${t.title}`)),
          ans: { t01: { ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') }, t03: { ok: say('Moves to Waiting'), opt: say('Unfolds the full card') } },
        }, 320);
        return ui.split([
          { w: 400, html: tree('todos', { todos: swap(IPAD_TODO) }, { counts: D.counts }) },
          { w: 320, html: list, line: true },
          { flex: 1, html: sessionPage({ head: 'column', back: swap(s.approved ? IPAD_TODO + '~approved' : IPAD_TODO), approved: s.approved, approveNav: swap(IPAD_SESS + '~approved'), undo: swap(IPAD_SESS), more: say('Opens: Open Full Session · Try Another Way · Stop') }), line: true },
        ]);
      },
    });
  });

  /* ---- iPad 11″ · the whole Conversation in the to-do column (V2 rule 2) */

  [
    ['', 'iPad 11″ · the Conversation in the to-do column', 'All 4 › on T01 pushes the whole Conversation inside the to-do column (“‹ Plan the Kyoto trip”); the sidebar and the list stay, and the list row points at the header beside it (drawn once).', { sel: 't01' }],
    ['release', 'iPad 11″ · Release’s Conversation in the column', 'All 2 › on T02: the same page for Release Weather App 1.3, its header carrying Widget’s door (Review Plan › pushes the session in the column).', { sel: 't02' }],
  ].forEach(([st, title, note, s]) => {
    Kit.frame(V, IPAD_CONV + (st ? '~' + st : ''), {
      device: 'ipad11-landscape', title, note, col: 1, sel: s.sel,
      render: () => {
        const t = D0.todo(s.sel);
        const back = swap(s.sel === 't01' ? IPAD_TODO + '~kyoto' : IPAD_TODO);
        const list = ipadList({
          sel: s.sel, listBase: IPAD_TODO, pointer: { [s.sel]: s.sel === 't02' ? 'door' : true },
          rowNav: (x) => (x.id === 't01' ? swap(IPAD_TODO + '~kyoto') : x.id === 't02' ? swap(IPAD_TODO) : say(`Opens: ${x.title}`)),
          ans: { t01: { ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') }, t02: { ok: swap(IPAD_SESS) }, t03: { ok: say('Moves to Waiting'), opt: say('Unfolds the full card') } },
        }, 320);
        return ui.split([
          { w: 400, html: tree('todos', { todos: swap(IPAD_TODO) }) },
          { w: 320, html: list, line: true },
          { flex: 1, html: conversationPage(t, { head: 'column', back, door: swap(IPAD_SESS), ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') }), line: true },
        ]);
      },
    });
  });

  function ipad13(conv) {
    const t = D0.todo('t01');
    const base = conv ? I13 + 'long-conversation' : I13 + 'todo';
    const list = ipadList({
      sel: 't01', listBase: base, pointer: { t01: true },
      rowNav: (x) => (x.id === 't01' ? swap(I13 + 'todo') : say(`Opens: ${x.title}`)),
      ans: { t01: { ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') }, t02: { ok: say('Opens: Widget’s session in the to-do column') }, t03: { ok: say('Moves to Waiting'), opt: say('Unfolds the full card') } },
    }, 340);
    const col = conv
      ? conversationPage(t, { head: 'column', back: swap(I13 + 'todo'), extra: [t.extraRounds.v2a, t.extraRounds.v2b], ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') })
      : ipadPaper(t, { p: { L: { conv: swap(I13 + 'long-conversation'), steps: say('Unfolds the steps'), a1: A1_SAY({ discuss: swap(I13 + 'long-conversation') }) }, extra: [t.extraRounds.v2a, t.extraRounds.v2b] } });
    return ui.split([
      { w: 400, html: tree('todos', { todos: swap(I13 + 'todo') }) },
      { w: 340, html: list, line: true },
      { flex: 1, html: col, line: true },
    ]);
  }

  Kit.frame(V, I13 + 'long-conversation', {
    device: 'ipad13-landscape', important: true, col: 1, sel: 't01',
    title: 'iPad 13″ · a long conversation in the to-do column (V2-12)',
    note: '<b>The cost of no pane.</b> A longer exchange (the V2 extra rounds) open as the whole Conversation in the to-do’s column (636). The paper itself (notes, steps, results) is not visible beside it. Compare with V1-15.',
    render: () => ipad13(true),
  });
  Kit.frame(V, I13 + 'todo', {
    device: 'ipad13-landscape', col: 0, sel: 't01',
    title: 'iPad 13″ · the to-do in its column',
    note: 'Where “‹ Plan the Kyoto trip” returns: the page with its latest 3 rounds (the longer exchange included); All N › pushes the Conversation back in the column.',
    render: () => ipad13(false),
  });

  /* ------------------------------------------------------------------ web 1440 (V2-13) */

  const WEB = 'web/todo';
  function web(s) {
    const D = D0;
    const t = D.todo(s.sel);
    const kyoto = s.sel === 't01';
    const listP = ui.page({
      top: ui.navbar({
        align: 'leading', center: ui.titleMenu(say('Opens: the place menu')),
        trailing: [{ icon: 'plus', label: 'New To-Do', look: 'fill', size: 's', toast: 'Opens: New To-Do' }],
        below: bar({ go: { all: swap(WEB) } }),
      }),
      body: listBody({
        selected: s.sel, pointer: s.pointer,
        rowNav: (x) => (x.id === 't01' ? swap(WEB) : x.id === 't02' ? swap(WEB + '~release') : say(`Opens: ${x.title}`)),
        ans: { t01: { ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') }, t02: { ok: swap(WEB + '~session') }, t03: { ok: say('Moves to Waiting'), opt: say('Unfolds the full card') } },
      }),
      cls: 'v2-listpage',
    });
    let col;
    if (s.conv) col = conversationPage(t, { head: 'column', back: swap(WEB), ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') });
    else if (s.session) col = sessionPage({ head: 'column', back: swap(WEB + '~release'), approveNav: say('Approved: the column returns with the card gone'), more: say('Opens: Open Full Session · Try Another Way · Stop') });
    else {
      const L = kyoto
        ? { conv: swap(WEB + '~conversation'), steps: say('Unfolds the steps'), a1: A1_SAY({ discuss: swap(WEB + '~conversation') }) }
        : { conv: say('Opens: the whole Conversation in this column'), agents: say('Unfolds the Agents'), work: say('Unfolds Work details'), session: { widget: swap(WEB + '~session') }, a2: A2_SAY({ door: swap(WEB + '~session') }) };
      col = ipadPaper(t, { p: { L } });
    }
    return ui.split([
      { w: 284, html: tree('todos', { todos: swap(WEB) }) },
      { w: 360, html: listP, line: true },
      { flex: 1, html: `<div class="v2-webcol">${col}</div>`, line: true },
    ]);
  }
  [
    ['', 'Web 1440 · sidebar │ list │ to-do (V2-13)', 'Sidebar 284 │ list 360 │ to-do 796 (content ≤ 680, centred, the web’s E6 radii and type). No side panel for To-dos in V2: one model on every client. The ask’s home card is in the column, so the list row points at it (drawn once). URL /todos/all?id=t01.', { sel: 't01', pointer: { t01: true } }, 0],
    ['conversation', 'Web · the Conversation in the to-do column', 'All 4 › replaces the to-do column (/todos/all/t01/conversation); the sidebar and the list stay.', { sel: 't01', conv: true, pointer: { t01: true } }, 1],
    ['release', 'Web · Release selected', 'A selection change: the to-do column shows T02 (/todos/all?id=t02); its row points at the door beside it (“● Plan awaiting approval · open beside →”, drawn once).', { sel: 't02', pointer: { t02: 'door' } }, 0],
    ['session', 'Web · Widget’s session in the to-do column', 'Open Session / Review Plan › replace the to-do column (/todos/all/t02/runs/widget); the list row’s door shrinks to one line.', { sel: 't02', session: true, pointer: { t02: true } }, 1],
  ].forEach(([st, title, note, s, colDepth]) => {
    Kit.frame(V, WEB + (st ? '~' + st : ''), { device: 'web', title, note, col: colDepth, sel: s.sel, render: () => web(s) });
  });

  /* ------------------------------------------------------------------ Android foldable (V2-14) */

  const AND = 'android-fold/list-todo';
  function android(conv) {
    const t = D0.todo('t01');
    const list = ui.page({
      top: ui.navbar({
        leading: [{ icon: 'line-3-horizontal', toast: 'Opens: the navigation drawer' }],
        center: ui.titleMenu(say('Opens: the place menu')),
        trailing: [{ icon: 'magnifying-glass', toast: 'Opens: Search To-dos' }, { icon: 'ellipsis', toast: NARROW_MORE }],
        below: bar({ go: { all: swap(AND) }, group: false }),
      }),
      body: listBody({
        selected: 't01', narrow: true, pointer: { t01: true },
        rowNav: (x) => (x.id === 't01' ? swap(AND) : say(`Opens: ${x.title}`)),
        ans: { t01: { ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') }, t02: { ok: say('Opens: Widget’s session in the detail pane') }, t03: { ok: say('Moves to Waiting'), opt: say('Unfolds the full card') } },
      }),
      overlay: ui.pill(Object.assign({ label: 'New To-Do' }, say('Opens: New To-Do'))),
      cls: 'v2-listpage',
    });
    const detail = conv
      ? conversationPage(t, { head: 'column', back: swap(AND), narrow: true, ok: say(`You chose ${optLabel('hanami')} · Undo`), opt: say('Unfolds the full card') })
      : ui.page({
        top: ui.navbar({ trailing: [{ icon: 'ellipsis', toast: 'Opens: the overflow menu' }] }),
        body: paperBody(t, { L: { conv: swap(AND + '~conversation'), steps: say('Unfolds the steps'), a1: A1_SAY({ discuss: swap(AND + '~conversation') }) }, narrow: true, shortAsk: true, noBg: true, oneLine: true, count: 2, maxItems: 2, per: { r2: { maxItems: 1 } } }),
        bottom: composer(t, {}),
        cls: 'v2-col v2-pane',
      });
    return ui.split([{ w: 336, html: list }, { w: 337, html: detail }], { cls: 'v2-duo' });
  }
  Kit.frame(V, AND, {
    device: 'android-fold', col: 0, sel: 't01',
    title: 'Android foldable · list ┊ to-do (V2-14)',
    note: 'ListDetailSceneStrategy: list ┊ to-do at the hinge, no extra pane. The ask’s home card is in the detail pane, so the row points at it. The Conversation and a session are pushed inside the detail pane; predictive Back pops them first.',
    render: () => android(false),
  });
  Kit.frame(V, AND + '~conversation', {
    device: 'android-fold', col: 1, sel: 't01',
    title: 'Android · the Conversation pushed in the detail pane',
    note: 'All 4 › pushes the Conversation inside the detail pane (a Nav3 entry scoped to it); Back pops it first.',
    render: () => android(true),
  });

  /* ------------------------------------------------------------------ continuity: back to the same scroll position */

  // V2-04: ‹ returns to V2-02 at the same scroll position. All 4 › (or a round) on the page or on its rounds records where
  // it was tapped; the Conversation's ‹ reads it in a click-through (Kit.store, frame.html only; shots stay the same).
  (function convOrigin() {
    if (!document.getElementById('root')) return;
    document.addEventListener('click', (e) => {
      const el = e.target.closest && e.target.closest('[data-go]');
      if (!el) return;
      const p = Kit.hash.parse();
      if (p.v !== V || Kit.baseOf(p.f) !== KYOTO) return;
      const t = Kit.resolveGo(el.getAttribute('data-go'), p.v);
      if (t.v === V && Kit.baseOf(t.f) === CONV) Kit.store.set('v2.convFrom', p.f);
    }, true);
  })();

  /* ------------------------------------------------------------------ shared V1 frames: the way back */

  // Capture (+ New To-Do) and T13's reply are shared with V1 (§3.2: everything else is identical), so those taps open V1's
  // frames. A click-through that left V2 that way comes back to V2: the shared frame's own way back to its list (✕, ‹)
  // returns to V2's list instead of V1's (Kit.store, frame.html only; kit request: a cross-variant link with a return).
  (function sharedReturn() {
    if (!document.getElementById('root')) return;
    const KEY = 'v2.return';
    document.addEventListener('click', (e) => {
      const el = e.target.closest && e.target.closest('[data-go]');
      if (!el) return;
      const p = Kit.hash.parse();
      const t = Kit.resolveGo(el.getAttribute('data-go'), p.v);
      if (p.v === V) {
        Kit.store.set(KEY, t.v !== V ? Kit.baseOf(p.f) : '');
        return;
      }
      const back = Kit.store.get(KEY, '');
      if (back && t.v !== V && Kit.baseOf(t.f) === LIST) {
        el.setAttribute('data-go', `${V}:${LIST}`);
        Kit.store.set(KEY, '');
      }
    }, true);
  })();

  /* ------------------------------------------------------------------ the in-column push (V2 rule 4) */

  // On regular widths the whole Conversation and a session replace the to-do inside its own column, a 0.3-s push there
  // while the sidebar and the list stay still. The kit's push moves all of the content, so these links use 'none' and
  // this plays the push on the column alone (frames carry col: 0 for the to-do, 1 for what replaces it, and sel: the
  // to-do). Reduce Motion turns it into the 0.2-s fade. frame.html only; screenshots never click.
  (function columnPush() {
    if (!document.getElementById('root')) return;
    let pending = null;
    document.addEventListener('click', (e) => {
      const el = e.target.closest && e.target.closest('[data-go]');
      // only links inside the to-do column (its back, All N ›, Review Plan ›, Open Session) and a row's door push;
      // a row pick or a sidebar pick is a selection or a place change, never animated
      if (!el || el.getAttribute('data-transition') !== 'none' || !el.closest('.v2-col, .k-ans--door')) return;
      const p = Kit.hash.parse();
      if (p.v !== V) return;
      const t = Kit.resolveGo(el.getAttribute('data-go'), p.v);
      if (t.v !== V) return;
      const from = Kit.findFrame(V, p.f);
      const to = Kit.findFrame(V, t.f);
      if (!from || !to || from.device !== to.device || from.col == null || to.col == null || from.col === to.col || from.sel !== to.sel) return;
      const colEl = document.querySelector('#root .v2-col');
      if (!colEl) return;
      pending = { dir: to.col > from.col ? 'push' : 'pop', html: colEl.outerHTML, at: Date.now() };
    }, true);
    window.addEventListener('hashchange', () => {
      const p = pending;
      pending = null;
      if (!p || Date.now() - p.at > 1500) return;
      setTimeout(() => play(p), 0);
    });
    function play(p) {
      const neu = document.querySelector('#root .v2-col');
      if (!neu || !neu.parentNode || !neu.animate) return;
      const holder = document.createElement('div');
      holder.innerHTML = p.html;
      const old = holder.firstElementChild;
      if (!old) return;
      old.style.pointerEvents = 'none';
      neu.parentNode.insertBefore(old, neu);
      const done = () => old.remove();
      const reduce = document.documentElement.classList.contains('kf-reduce');
      const ease = 'cubic-bezier(.32, .72, 0, 1)';
      let anims;
      if (reduce) {
        neu.style.zIndex = '2';
        anims = [neu.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 200, easing: 'ease' })];
      } else if (p.dir === 'push') {
        neu.style.zIndex = '2';
        neu.style.boxShadow = '-10px 0 30px rgba(0, 0, 0, .14)';
        anims = [
          neu.animate([{ transform: 'translateX(100%)' }, { transform: 'translateX(0)' }], { duration: 300, easing: ease }),
          old.animate([{ transform: 'translateX(0)', opacity: 1 }, { transform: 'translateX(-30%)', opacity: 0.85 }], { duration: 300, easing: ease, fill: 'forwards' }),
        ];
      } else {
        old.style.zIndex = '2';
        old.style.boxShadow = '-10px 0 30px rgba(0, 0, 0, .14)';
        anims = [
          old.animate([{ transform: 'translateX(0)' }, { transform: 'translateX(100%)' }], { duration: 300, easing: ease, fill: 'forwards' }),
          neu.animate([{ transform: 'translateX(-30%)', opacity: 0.85 }, { transform: 'translateX(0)', opacity: 1 }], { duration: 300, easing: ease }),
        ];
      }
      Promise.all(anims.map((a) => a.finished)).then(() => { done(); neu.style.zIndex = ''; neu.style.boxShadow = ''; }, done);
    }
  })();
})();
