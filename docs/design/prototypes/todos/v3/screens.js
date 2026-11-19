/*
 * Variant V3 · Spine (synthesis §3.3, from concept E): one to-do region, two zooms.
 *
 * Nothing on the left ever moves by itself. The to-do region keeps a still spine (living mark, title, state, the open
 * ask) and its body switches between Details and Conversation under it, with one composer fixed at its foot. When the
 * to-do region is at least 800 wide (the owner's Focus on iPad, the rail on the web) the body splits into
 * Details │ Conversation under one spine. ("Paper" is the internal name of the Details zoom; §2.11 keeps it off screens.)
 *
 * The body switch is the to-do region's principal bar item (the navigation bar's centre on iPhone, the region's toolbar
 * on wider screens, the top of the content on the Duo's outer display), so it never moves when it is tapped; the spine
 * under it condenses freely.
 *
 * V3's own pieces live here (the kit has no LivingMark, Spine, BodySwitch, crew line or run card): livingMark(),
 * spine(), bodySwitch(), askLine(), crewLine(), runCard(). Everything else is Kit.ui and window.Data.
 *
 * Motion. The harness animates a whole screen; V3's body switch moves the body only (shared axis X on phones and
 * inside a Duo half, a cross-fade elsewhere) while the spine and the composer stay still. A tap on an element with
 * data-v3-anim records the move; the next live render plays it on the body (.v3-anim-*), and the harness gets
 * transition "none". Reduce Motion turns it into the 0.2 s fade. A right-click on an element with data-v3-long stands
 * in for a long press.
 */
(function () {
  'use strict';
  const V = 'V3';
  const ui = Kit.ui;
  const esc = Kit.esc;
  const I = (n, s, o) => Kit.icon(n, s, o);
  const navA = (o) => Kit.navAttrs(o);
  const go = (id, transition) => ({ go: id, transition: transition || 'none' });
  const opens = (what) => ({ toast: `Opens: ${what}` });
  const F = (id, def) => Kit.frame(V, id, def);
  const counts = () => window.Data.counts;

  // Owner-facing words of the variant (§2.11: Paper and Spine are internal only).
  const SEG = { paper: 'Details', conversation: 'Conversation' };
  const ASKED_ABOVE = '· answer above ↑';
  const ASKED_BESIDE = '· answer beside';
  const NEUTRAL_SUGG = ['I', 'The', 'Thanks'];
  // Lines that would repeat a word on screen ("Widget is building the widget", "Working · 2 working").
  const SAY = { 'Widget is building the widget': 'Widget is building it', '2 working · 1 done': '2 running · 1 done' };
  const say = (text) => (text && SAY[text]) || text;

  Kit.variant(V, {
    name: 'Spine',
    order: 30,
    summary: 'One to-do region, two zooms. A still <b>spine</b> (living mark, title, state, the open ask) heads the to-do; its body switches between <b>Details</b> and <b>Conversation</b> under it, with one composer at its foot. The switch sits in the bar, so it never moves. Nothing on the left moves by itself; at ≥ 800 pt (the owner’s <b>Focus</b>, the web’s rail) the body splits Details │ Conversation under one spine.',
  });

  /* ---------------------------------------------------------------- local icons */

  // The kit has no "arrow.up.left.and.arrow.down.right", no Android back arrow and no dashed clock ring: register
  // V3-prefixed paths in the icon table at runtime.
  const PATHS = Kit.icon && Kit.icon.paths;
  if (PATHS && !PATHS['v3-focus']) {
    PATHS['v3-focus'] = '<path d="M13.8 4.2h6v6M19.8 4.2l-6.3 6.3M10.2 19.8h-6v-6M4.2 19.8l6.3-6.3"/>';
    PATHS['v3-exit-focus'] = '<path d="M19.8 10.2h-6v-6M13.8 10.2l6-6M4.2 13.8h6v6M10.2 13.8l-6 6"/>';
    PATHS['v3-back-android'] = '<path d="M19.5 12H4.8M10.8 5.8L4.6 12l6.2 6.2"/>';
    // Waiting: a dashed ring (12 dashes) with clock hands, so it never reads as the solid ○ that ticks.
    PATHS['v3-waiting'] = '<circle cx="12" cy="12" r="10.6" stroke-dasharray="3.1 2.45"/><path d="M12 7.4V12l3 2"/>';
  }

  /* ---------------------------------------------------------------- after render: measured once */

  // Runs after the kit's own pass (frame.html and index.html call Kit.afterRender); touches V3 frames only.
  const baseAfter = Kit.afterRender;
  Kit.afterRender = function v3AfterRender(root) {
    const out = baseAfter.apply(this, arguments);
    try { measureV3(root || document); } catch (err) { /* never break a page */ }
    return out;
  };
  function measureV3(root) {
    const all = (sel) => root.querySelectorAll(`[data-variant="V3"] ${sel}`);
    // Replies and bubbles never end on a lone word ("… for / two."): their last two words stay together.
    all('.k-rd-reply, .k-rd-bubble, .v3-lx-r > span, .k-ac-body').forEach((el) => {
      const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
      let last = null;
      for (let n = walker.nextNode(); n; n = walker.nextNode()) if (/\S \S/.test(n.nodeValue)) last = n;
      if (last) last.nodeValue = last.nodeValue.replace(/ (\S+\s*)$/, '\u00a0$1');
    });
    // The composer's placeholder is whole or cut at a word: "Message about this to-do…" becomes "Message about this…"
    // when the field is narrower (the chip already names the addressee). Measured once, as MeasuredOnce would.
    all('.k-tc-f:not(.has-text)').forEach((f) => {
      if (f.scrollWidth <= f.clientWidth + 1) return;
      const words = f.textContent.replace(/…$/, '').split(' ');
      while (words.length > 1) {
        words.pop();
        f.textContent = `${words.join(' ')}…`;
        if (f.scrollWidth <= f.clientWidth + 1) break;
      }
    });
    // Results keep every outcome ("What’s New · draft"): when the chips don't fit beside their title, the title stands
    // above them instead of cutting the last chip.
    all('.k-res:not(.is-stacked)').forEach((res) => {
      const row = res.querySelector('.k-res-in');
      if (!row || row.scrollWidth <= row.clientWidth + 1) return;
      res.classList.add('is-stacked');
      const cut = row.scrollWidth > row.clientWidth + 1;
      row.classList.toggle('is-overflowing', cut);
    });
    // A typed draft longer than its field keeps its end in view and fades the cut leading edge.
    all('.k-tc-f.has-text').forEach((f) => {
      f.classList.toggle('v3-cut', f.scrollWidth > f.clientWidth + 1);
    });
    // The section bar draws whole chips only. All (first), Needs you and the chosen chip always stay; a chip that would
    // be cut leaves the row (from the end) and moves into the grouping icon's menu, so no label or count is ever drawn
    // half.
    all('.v3-bar .k-sbar').forEach((bar) => {
      const row = bar.querySelector('.k-sbar-in');
      if (!row) return;
      const chips = Array.from(row.children);
      chips.forEach((c) => { c.style.display = ''; });
      row.scrollLeft = 0;
      row.classList.remove('is-overflowing', 'is-scrolled');
      const fits = () => row.scrollWidth <= row.clientWidth + 1;
      const on = row.querySelector('.is-on');
      const hidden = [];
      for (let i = chips.length - 1; i > 1 && !fits(); i -= 1) {
        if (chips[i] === on) continue;
        chips[i].style.display = 'none';
        hidden.unshift(chips[i]);
      }
      const g = bar.querySelector('.v3-sbar-g');
      // A column too narrow for Needs you and In progress side by side (320) hands Group by to the column's ⋯, so
      // the In progress count stays readable beside Needs you (§3.4 task 1).
      let groupMoved = false;
      if (g && chips[2] && hidden[0] === chips[2] && !g.hasAttribute('data-go')) {
        g.style.display = 'none';
        chips[2].style.display = '';
        if (fits()) { hidden.shift(); groupMoved = true; } else { chips[2].style.display = 'none'; g.style.display = ''; }
      }
      const names = hidden.map((c) => c.textContent.replace(/\s+/g, ' ').trim().replace(/(\D)(\d+)$/, '$1 $2'));
      if (g && !groupMoved && hidden.length && g.hasAttribute('data-toast')) {
        g.setAttribute('data-toast', `Opens: ${names.join(' · ')} · Group by Phase · Project · Due Date`);
      }
      if (groupMoved) {
        const page = bar.closest('.k-page');
        const more = page && Array.from(page.querySelectorAll('.k-nav-trail [data-toast]')).find((b) => /Sort By/.test(b.getAttribute('data-toast')));
        if (more) more.setAttribute('data-toast', `Opens: ${names.concat(['Group by…']).join(' · ')} · ${more.getAttribute('data-toast').replace(/^Opens: /, '')}`);
      }
    });
    // Scroll views at rest or at their foot. The details above the keyboard sit at the top while they fit (no edge)
    // and at their foot once they overflow (the top passes under the spine's edge). A thread is anchored at its latest
    // round. Only while something passes under the spine does its scroll-edge blur show, and a line the edge would
    // slice is not drawn, so the edge always falls on a clean gap.
    all('.v3-foot').forEach((foot) => {
      const paper = foot.firstElementChild;
      if (!paper) return;
      foot.classList.remove('is-fit');
      const over = paper.offsetHeight > foot.clientHeight + 1;
      foot.classList.toggle('is-fit', !over);
      underEdge(foot, over, '.v3-paper > *:not(.v3-lx), .v3-lx-line, .v3-lx-sub', paper);
    });
    all('.k-thread.is-bottom').forEach((th) => {
      const first = th.firstElementChild;
      const over = !!first && first.offsetTop < parseFloat(getComputedStyle(th).paddingTop) - 1;
      underEdge(th, over, '.k-thread > .k-rd-day, .k-thread > .k-rl, .k-rd-from, .k-rd-bubble, .k-rd-st, .k-rd-items > :not(.k-rl-steps), .k-rl-steps > div', th);
    });
  }
  /** The ink of a line: its own box when it is a filled shape (a bubble, a chip), else the box of its text and glyphs. */
  function inkRect(a) {
    const cs = getComputedStyle(a);
    const filled = (cs.backgroundColor && !/^(transparent|rgba\(0, 0, 0, 0\))$/.test(cs.backgroundColor)) || cs.boxShadow !== 'none';
    if (filled) return a.getBoundingClientRect();
    const r = document.createRange();
    r.selectNodeContents(a);
    const rr = r.getBoundingClientRect();
    return rr.height ? rr : a.getBoundingClientRect();
  }
  /**
   * Turn the scroll edge of the bar above a body on or off. While on, pick the scroll position so the edge falls on a
   * gap: move the content up by at most 32 pt (the foot's padding grows) until no line straddles the edge's opaque
   * part; failing that, leave out a line the edge would slice (more than a fifth of it under the edge). Lines are
   * measured by their ink (text and filled shapes), not their 44-pt boxes.
   */
  function underEdge(box, over, atoms, shiftEl) {
    const page = box.closest('.k-page');
    const head = page && Array.from(page.children).find((c) => c.classList.contains('v3-spine') || c.classList.contains('v3-panehead'));
    if (head) head.classList.toggle('is-under', over);
    if (!over) return;
    const rect = box.getBoundingClientRect();
    const scale = rect.height / (box.offsetHeight || 1) || 1;
    const list = Array.from(box.querySelectorAll(atoms)).map((a) => {
      const r = inkRect(a);
      return { a, t: (r.top - rect.top) / scale, b: (r.bottom - rect.top) / scale };
    }).filter((x) => x.b > x.t);
    const HID = 6;
    const CLEAR = 22;
    const MAX = 32;
    const clean = (sh) => list.every((x) => x.b - sh <= HID || x.t - sh >= CLEAR);
    let sh = 0;
    while (sh <= MAX && !clean(sh)) sh += 1;
    if (sh <= MAX) {
      if (sh && shiftEl) shiftEl.style.paddingBottom = `${parseFloat(getComputedStyle(shiftEl).paddingBottom) + sh}px`;
      return;
    }
    list.forEach((x) => {
      if (x.b > HID && x.t < CLEAR && (x.b - CLEAR) / (x.b - x.t) < 0.8) x.a.style.visibility = 'hidden';
    });
    // A bubble left out takes its sender and state line with it (never an orphaned "Delivered to Persona").
    box.querySelectorAll('.k-rd').forEach((rd) => {
      const bub = rd.querySelector(':scope > .k-rd-bubble');
      if (bub && bub.style.visibility === 'hidden') rd.querySelectorAll(':scope > .k-rd-st, :scope > .k-rd-from').forEach((el) => { el.style.visibility = 'hidden'; });
    });
  }

  /* ---------------------------------------------------------------- body-only motion, long press, session origin */

  const ANIM = 'kit:v3-anim';
  const FROM = 'kit:v3-session-from';
  document.addEventListener('click', (e) => {
    const t = e.target;
    if (!t || !t.closest) return;
    const hit = t.closest('[data-go], [data-toast]');
    if (!hit || !hit.hasAttribute('data-go')) return;
    try {
      const a = hit.getAttribute('data-v3-anim');
      if (a) sessionStorage.setItem(ANIM, JSON.stringify({ a, at: Date.now() }));
      else sessionStorage.removeItem(ANIM);
      // Back from a run's session lands where it started (the list, the details or its run card).
      const target = hit.getAttribute('data-go');
      const here = (Kit.hash.parse().f || '');
      if (/release-widget-session/.test(target) && !/release-widget-session/.test(here)) sessionStorage.setItem(FROM, JSON.stringify(here));
    } catch (err) { /* file:// without storage */ }
  }, true);
  document.addEventListener('contextmenu', (e) => {
    const el = e.target && e.target.closest && e.target.closest('[data-v3-long]');
    if (!el) return;
    const p = Kit.hash.parse();
    if (!p.v || !p.f) return;
    e.preventDefault();
    location.hash = Kit.hash.build({ v: p.v, f: el.getAttribute('data-v3-long'), theme: p.theme || 'light', scale: p.scale });
  }, true);
  function takeAnim(ctx) {
    if (!ctx.live) return '';
    try {
      const raw = sessionStorage.getItem(ANIM);
      sessionStorage.removeItem(ANIM);
      const v = raw && JSON.parse(raw);
      return v && Date.now() - v.at < 1500 ? ` v3-anim-${v.a}` : '';
    } catch (err) { return ''; }
  }
  const attr = (html, a) => html.replace(/^(\s*<[a-zA-Z0-9]+)/, `$1 ${a}`);
  const withAnim = (html, name) => (name ? attr(html, `data-v3-anim="${name}"`) : html);

  /* ---------------------------------------------------------------- LivingMark */

  const LM_WORD = { needs: 'needs you', later: 'can wait', working: 'working', ready: 'just finished', waiting: 'waiting', todo: 'to do', done: 'done' };
  /** The row's state as one mark: ○ to do · ◉ needs you · ⊙ can wait (listed, not counted: the blue ring holding three
      small dots, so it never reads as the ○ that ticks) · ◔ working · ◎ just finished · ◌ waiting (dashed ring with clock
      hands) · ● done. */
  function lmKind(t) {
    if (!t) return 'todo';
    if (t.ticked || t.phase === 'done') return 'done';
    if (t.section === 'can_wait') return 'later';
    return { needs_you: 'needs', working: 'working', ready: 'ready', waiting: 'waiting' }[t.phase] || 'todo';
  }
  /** LivingMark: 24 glyph, 44 hit. Only ○ ticks on a tap; any other mark opens the to-do (o.nav). o.long: the long
      press (right-click here) → the Mark Done frame. */
  function livingMark(kind, o) {
    o = o || {};
    const s = o.size || 24;
    const inner = kind === 'needs' ? '<i></i>'
      : kind === 'later' ? '<i></i><i></i><i></i>'
        : kind === 'ready' ? I('checkmark', Math.round(s * 0.56), { stroke: 2.7 })
          : kind === 'done' ? I('checkmark-circle-fill', s)
            : kind === 'waiting' ? I('v3-waiting', s, { stroke: 1.7 })
              : '';
    const label = `${o.title ? o.title + ', ' : ''}${LM_WORD[kind]}. Actions: ${kind === 'todo' ? 'Mark Done' : 'Open, Mark Done'}`;
    return `<span class="v3-lm v3-lm--${kind}" style="--lm:${s}px" role="button" aria-label="${esc(label)}"${navA(o.nav)}${o.long ? ` data-v3-long="${esc(o.long)}"` : ''}>${inner}</span>`;
  }

  /* ---------------------------------------------------------------- crew line, run card */

  const runMark = (st) => (st === 'needs' ? ui.lineMark('needs', 8) : st === 'working' ? ui.spinner(11) : st === 'done' ? ui.lineMark('ready', 12) : '');
  /** Crew line: only with two or more runs. Capsules by title and state; o.on: the run whose card is open. */
  function crewLine(t, o) {
    o = o || {};
    const runs = (t && t.runs) || [];
    if (runs.length < 2) return '';
    const nav = o.nav || {};
    return `<div class="k-caps v3-crew">${runs.map((r) => `<span class="v3-cr${o.on === r.id ? ' is-on' : ''}"${navA(nav[r.id])} aria-label="${esc(r.title)}, ${esc(ui.phases[r.state === 'needs' ? 'needs_you' : r.state === 'done' ? 'done' : 'working'].word)}">${runMark(r.state)}<span>${esc(r.title)}</span></span>`).join('')}</div>`;
  }
  /** The run card that unfolds inside the spine from the crew line: who, the latest line, its ask door, Open Session ›.
      The door line folds into the card while it is open (drawn once). */
  function runCard(D, o) {
    o = o || {};
    const r = D.run('widget');
    const a = D.ask('a2');
    return `<div class="v3-rc">
      <div class="v3-rc-h"><b>${esc(r.title)}</b><span class="k-sep">·</span><span>${esc(r.role)}</span><span class="k-sep">·</span><span class="v3-rc-st">${esc(ui.phases.needs_you.word)}</span><span class="v3-rc-x" role="button" aria-label="Fold the card"${navA(o.close)}>${I('chevron-up', 13)}</span></div>
      <div class="v3-rc-l">Latest: planned the widget target</div>
      <div class="v3-rc-a">${ui.lineMark('needs')}<span>${esc(a.title)}</span></div>
      <span class="k-ac-btn is-primary"${navA(o.review)}><span>${esc(a.door)}</span>${I('chevron-right', 13)}</span>
      <div class="v3-rc-open"${navA(o.open)}><span>Open Session</span>${I('chevron-right', 12)}</div>
    </div>`;
  }

  /* ---------------------------------------------------------------- BodySwitch, ask line, Spine */

  /** BodySwitch: Details · Conversation in one glass capsule (the bar's principal item; a blue dot on Conversation for
      unread replies). It changes the zoom of one object; it is not a tab bar. o: { nav: { paper, conversation }, dot,
      anim: { paper, conversation }, compact } */
  function bodySwitch(zoom, o) {
    o = o || {};
    const seg = (id) => {
      const on = zoom === id;
      const n = on ? '' : navA((o.nav || {})[id]);
      const a = !on && o.anim && o.anim[id] ? ` data-v3-anim="${o.anim[id]}"` : '';
      const dot = id === 'conversation' && o.dot && !on ? '<i class="v3-dot" aria-label="Unread replies"></i>' : '';
      return `<span class="v3-seg${on ? ' is-on' : ''}" role="tab" aria-selected="${on}"${n}${a}><span>${SEG[id]}</span>${dot}</span>`;
    };
    return `<div class="v3-sw${o.compact ? ' is-compact' : ''}" role="tablist" aria-label="Show">${seg('paper')}${seg('conversation')}</div>`;
  }

  /** "Options ⌄" / "Options ⌃" (o.mini: the chevron alone, for a narrow half). */
  const optToggle = (open, nav, mini) => `<span class="k-ans-opt v3-opt${mini ? ' is-mini' : ''}${open ? ' is-open' : ''}" role="button" aria-label="Options"${navA(nav)}>${mini ? '' : '<span>Options</span>'}${I(open ? 'chevron-up' : 'chevron-down', mini ? 13 : 11)}</span>`;

  /** The open ask folded to one 44-pt line: its dot in the mark's column, the question on the title's edge, then
      [OK: Hanami] and Options ⌄ right after it. o: { ok: { label, …nav } | false, door: { label, …nav }, options: nav,
      open (⌃: the card is laid over), mini (the chevron alone), answered: { text, undo } (✓ You chose … · Undo) } */
  function askLine(title, o) {
    o = o || {};
    if (o.answered) {
      return `<div class="k-al v3-al is-answered"><span class="v3-al-dot">${I('checkmark-circle-fill', 18)}</span><span class="v3-al-a">${o.answered.text}</span><span class="k-sep">·</span><span class="v3-undo"${navA(o.answered.undo)}>Undo</span></div>`;
    }
    const ok = o.ok ? `<span class="k-ans k-ans--ok"${navA(o.ok)}><span>${o.ok.label}</span></span>` : '';
    const door = o.door ? `<span class="k-ans k-ans--door"${navA(o.door)}><span>${o.door.label}</span>${I('chevron-right', 12)}</span>` : '';
    const opt = o.options ? optToggle(o.open, o.options, o.mini) : '';
    return `<div class="k-al v3-al"><span class="v3-al-dot">${ui.lineMark('needs')}</span><span class="k-al-q">${title}</span>${ok}${door}${opt}</div>`;
  }

  /**
   * Spine: sticky at the top of the to-do region, under the bar that holds the switch.
   * o: { mode: 'expanded' (mark + title 22, state ≤ 2 lines) | 'condensed' (title 17 · state word, or the state on its
   *      own line with stateRow; then the ask line), stateText, short, kind, markNav, crew (HTML), card (HTML), ask (HTML),
   *      readable (content on the 680 column), recap (HTML under the state), cls }
   * Every line that continues the title starts on the title's edge (24 mark + 12). Its scroll-edge blur shows only
   * while the body passes under it (measureV3 decides, after layout).
   */
  function spine(t, o) {
    o = o || {};
    const kind = o.kind || lmKind(t);
    const st = t.state;
    const ph = ui.phases[t.phase] || { tone: 'idle' };
    const text = keepTogether(say(o.stateText != null ? o.stateText : st ? (o.short && st.short) || st.text : ''));
    const word = o.word || (st && st.word);
    const mark = livingMark(kind, { size: 24, title: t.title, nav: o.markNav });
    let html = '';
    if (o.mode === 'condensed') {
      const w = word && !o.stateRow ? `<span class="k-sep">·</span><span class="v3-sp-w k-tone--${o.tone || ph.tone}">${word}</span>` : '';
      html += `<div class="v3-sp-c1">${mark}<span class="v3-sp-ct">${t.title}</span>${w}</div>`;
      if (o.stateRow && word) html += `<div class="v3-sp-in v3-sp-c2">${ui.stateLine(t.phase, text, { word, clamp: 1, tone: o.tone })}</div>`;
    } else {
      html += `<div class="v3-sp-t">${mark}<span class="v3-sp-tt">${t.title}</span></div>`;
      if (word) html += `<div class="v3-sp-in v3-sp-s">${ui.stateLine(t.phase, text, { word, tone: o.tone })}</div>`;
      if (o.recap) html += `<div class="v3-sp-in v3-sp-recap">${o.recap}</div>`;
    }
    if (o.crew) html += `<div class="v3-sp-in v3-sp-crew">${o.crew}</div>`;
    if (o.card) html += `<div class="v3-sp-card">${o.card}</div>`;
    if (o.ask) html += `<div class="v3-sp-ask">${o.ask}</div>`;
    if (o.readable) html = `<div class="v3-sp-rd">${html}</div>`;
    return `<div class="v3-spine is-${o.mode || 'expanded'}${o.cls ? ' ' + o.cls : ''}">${html}</div>`;
  }
  /** A line break never splits a count from its noun ("1 working", "3 ryokans") or leaves the last word alone. */
  function keepTogether(text) {
    if (!text) return text;
    return String(text).replace(/(\d[\d,.]*) (?=[^\s·])/g, '$1\u00a0').replace(/ (\S+)$/, '\u00a0$1');
  }

  /** The to-do region's bar: the switch as its principal item (centre, or leading beside other items). */
  const regionBar = (o) => ui.navbar({ leading: o.leading || [], center: o.center, trailing: o.trailing || [], align: o.align, wc: o.wc, cls: Kit.cls('v3-regbar', o.cls) });

  /* ---------------------------------------------------------------- details pieces */

  /** The ask card at its home (P1): ≤ 2 equal secondary buttons, the primary full width, then the link row. */
  function askHome(D, o) {
    o = o || {};
    const a = D.ask('a1');
    const on = o.on || {};
    const rec = a.options.find((x) => x.recommended);
    const exitKey = { 'Later': 'later', 'Don’t handle': 'dont', 'Discuss…': 'discuss' };
    return ui.askCard({
      title: a.title, body: a.why,
      secondary: a.options.filter((x) => x !== rec).map((x) => Object.assign({ label: x.label }, on[x.id])),
      primary: Object.assign({ label: o.narrow ? rec.label : rec.primaryLabel }, on[rec.id]),
      links: a.exits.map((x) => Object.assign({ label: x }, on[exitKey[x]])),
    });
  }
  const askOn = (o) => Object.assign({
    hanami: opens('✓ You chose Ryokan Hanami · Undo'), kaede: opens('the same answer with Kaede'), tsuki: opens('the same answer with Tsuki'),
    later: opens('Which ryokan? · later today'), dont: opens('“Persona won’t book a ryokan. You can undo this.”'), discuss: opens('the composer with “Answer: Which ryokan? ×”'),
  }, o || {});
  const withToasts = (list, fn) => (list || []).map((x) => Object.assign({}, x, fn(x)));
  const stepsOf = (t, o) => ui.stepsFor(Object.assign({}, t, { steps: withToasts(t.steps, (s) => opens(s.text)) }), o);
  /** "Steps 2 of 5 ✓ ✓ ◉ ○ ○ ›": the steps folded to one line, so Results and Latest stay above the composer. */
  const stepsSum = (t, nav) => ui.stepsSummary(t, Object.assign({ marks: true }, nav || opens('the 5 steps, unfolded in place')));
  // Results on compact widths use the short words of the §3.3 wireframes ("Flights · held", "Shortlist · 3", "What’s New").
  const SHORT_RESULT = { 'Ryokan shortlist': ['Shortlist'], Flights: [null, 'held'] };
  const resultsOf = (t, o) => ui.resultsStrip(withToasts(t.results, (r) => {
    const s = o && o.short && SHORT_RESULT[r.label];
    return Object.assign(opens(`${r.label} · ${r.outcome} in the link card`), s ? { label: s[0] || r.label, outcome: s[1] != null ? s[1] : r.outcome } : {});
  }));
  const chipsOf = (t, o) => ui.chipsFor(Object.assign({}, t, { chips: withToasts(t.chips, (c) => opens(c.label)) }), { add: o && o.add === false ? false : opens('Add Date, Reminder or Project') });

  /** V3's one-line Conversation block (rule 7): "Latest · You “near Gion please…” ›". The status, the reply and relay
      lines that follow a send sit on the quote's edge. o: { text, who ('You'), quote (true), nav, anim, status, reply,
      extra (HTML) } */
  function latest(o) {
    const q = o.quote === false ? o.text : `${o.who || 'You'} “${o.text}”`;
    const subs = [];
    if (o.status) subs.push(`<div class="v3-lx-sub v3-lx-st${/^Sending/.test(o.status) ? ' is-faint' : ''}">${o.status}</div>`);
    if (o.reply) subs.push(`<div class="v3-lx-sub v3-lx-r"><i>└</i><span><b>${o.reply.who}:</b> ${o.reply.text}</span></div>`);
    if (o.extra) subs.push(`<div class="v3-lx-sub v3-lx-x">${o.extra}</div>`);
    return `<div class="v3-lx">
      <div class="v3-lx-line"${navA(o.nav)}${o.anim ? ` data-v3-anim="${o.anim}"` : ''}><span class="v3-lx-k">Latest<span class="k-sep">·</span></span><span class="v3-lx-v"><span class="v3-lx-q">${q}</span>${I('chevron-right', 12)}</span></div>
      ${subs.join('')}
    </div>`;
  }

  /** The details scrolled to its foot above the keyboard: its top passes under the spine's edge, its latest words stay
      above the composer. */
  const footOf = (html) => `<div class="v3-foot"><div class="v3-paper">${html}</div></div>`;
  const paperFoot = (t, latestHtml, o) => footOf(`${chipsOf(t)}${ui.notes(t.notes, opens('the notes, edited in place'))}${stepsSum(t)}${resultsOf(t, { short: !(o && o.wide) })}${latestHtml}`);

  /* ---------------------------------------------------------------- the list */

  /**
   * One list row with the living mark in the leading slot and no mark on line 2; T02 carries the crew line.
   * o: { kind, markNav, long, go/transition/toast, answer (merged into the row's answer; false: none), pointer ([lead,
   *      text]: the ask shrunk to one line while its home is on screen), lineHtml, answered, undo, ticked, selected,
   *      openCard: { card, close } (the unfolded card: the row keeps only "Options ⌃", in the place it was tapped; line 3
   *      follows the card), line3 (false: none), crew (false: no capsules, e.g. in a list column), crewNav, crewOn, cls }
   */
  function rowFor(t, o) {
    o = o || {};
    const kind = o.kind || lmKind(t);
    const props = ui.rowProps(t, {
      lead: livingMark(kind, { nav: o.markNav, long: o.long, title: t.title }),
      mark: null, selected: !!o.selected,
      go: o.go, transition: o.transition, toast: o.toast,
    });
    const crew = (t.runs || []).length >= 2 && o.crew !== false;
    // Line 3 carries set facts the grouping doesn't show; a crew line already says the steps.
    props.line3 = o.line3 === false || !t.line3 ? null : crew ? t.line3.split(' · ')[0] : t.line3;
    if (o.cls) props.cls = o.cls;
    if (o.pointer) Object.assign(props, { answer: null, lineLead: o.pointer[0], line: o.pointer[1], tone: 'needs', toneAll: false });
    else if (o.answer === false) props.answer = null;
    else if (props.answer && o.answer) Object.assign(props.answer, o.answer);
    if (props.line) props.line = say(props.line);
    if (o.lineHtml) props.lineHtml = o.lineHtml;
    if (o.answered) { props.answered = o.answered; props.undo = o.undo; }
    if (o.ticked) props.ticked = true;
    if (o.openCard) {
      // The answer row stays where it was tapped: the OK button keeps its room but is not drawn (the card below holds the
      // answers, drawn once), so "Options ⌃" stands exactly where "Options ⌄" was. Line 3 follows the card.
      const a = props.answer || {};
      const ghost = a.label ? `<span class="k-ans k-ans--${a.kind || 'ok'} v3-ghost" aria-hidden="true"><span>${a.label}</span></span>` : '';
      const l3 = props.line3 ? `<div class="k-tr-l3 v3-l3-after">${props.line3}</div>` : '';
      props.answer = null;
      props.line3 = null;
      props.below = `<div class="k-tr-ans">${ghost}${optToggle(true, o.openCard.close)}</div>${o.openCard.card}${l3}`;
    }
    if (crew && !o.lineHtml && !o.pointer && !o.answered) {
      const r = t.row || {};
      const tone = r.tone ? ` k-tone--${r.tone}` : '';
      props.lineHtml = `<span class="k-tr-text">${r.lead ? `<span class="k-tr-lt${tone}">${r.lead}</span>` : ''}${r.lead && r.text ? '<span class="k-sep">·</span>' : ''}${say(r.text) || ''}</span>${crewLine(t, { nav: o.crewNav, on: o.crewOn })}`;
    }
    return ui.todoRow(props);
  }

  /** Sections of All To-dos for a Data view. o: { filter, rows (per section), rowOpts(t), canWaitOpen, foldNav,
      headerOpts: { <section id>: options } } */
  function sectionsHtml(D, o) {
    o = o || {};
    const calm = D.isCalm('all');
    const fold = o.foldNav || {};
    let html = '';
    D.sections('all', o.filter || 'all').forEach((s) => {
      const rows = (o.rows && o.rows[s.id]) || s.todos;
      if (s.fold) {
        const open = s.id === 'can_wait' && o.canWaitOpen;
        html += ui.foldRow(s.title, Object.assign({ count: s.count, open }, fold[s.id]));
        if (open) html += ui.todoList(rows.map((t) => rowFor(t, o.rowOpts(t))));
        return;
      }
      if (!calm) html += ui.sectionHeaderFor(s, (o.headerOpts || {})[s.id]);
      html += ui.todoList(rows.map((t) => rowFor(t, o.rowOpts(t))));
    });
    return html;
  }

  /**
   * The section bar (E2: the Inbox's capsule bar) for a Data view; hidden by the calm rule. Needs you leads the row;
   * In progress and To do follow while they have rows; Done only while it has rows; All, where the list lands, is pinned
   * in the trailing slot with the grouping icon, so the Needs you count is always in view.
   * o: { group (nav of the grouping icon), counts: { <chip id>: n } (counts drawn before a row re-files) }
   */
  function barHtml(D, selected, goFor, o) {
    o = o || {};
    if (D.isCalm('all')) return '';
    const sel = selected || 'all';
    // All is the first chip (the landing choice first, as in every bar)
    const items = D.bar('all').filter((x) => !(x.id === 'done' && x.empty))
      .map((x) => Object.assign({}, x, (o.counts || {})[x.id] != null ? { count: o.counts[x.id] } : {}, goFor(x.id)));
    const idx = items.findIndex((x) => x.id === sel);
    const group = `<span class="v3-sbar-g" role="button" aria-label="Group by"${navA(o.group || opens('Group by Phase · Project · Due Date'))}>${I('line-3-decrease', 17)}</span>`;
    return `<div class="v3-bar">${ui.sectionBar(items, { selected: idx < 0 ? 99 : idx, trailing: group })}</div>`;
  }

  const placeItems = (allNav) => ui.placeMenuItems({
    selected: 'all',
    go: { all: allNav, today: opens('Today'), upcoming: opens('Upcoming'), kyoto: opens('Kyoto trip'), weather: opens('Weather App'), home: opens('Home'), projects: opens('All Projects') },
  });
  const treeGo = (todosNav) => ({
    inbox: opens('Inbox'), todos: todosNav, today: opens('Today'), upcoming: opens('Upcoming'), kyoto: opens('Kyoto trip'),
    weather: opens('Weather App'), home: opens('Home'), projects: opens('All Projects'), scheduled: opens('Scheduled Tasks'),
    knowledge: opens('Knowledge'), machines: opens('Machines'), persona: opens('Persona, the main chat'),
    'release-lead': opens('Release lead, under Persona Chats › Pinned'), agents: opens('Agents'), chats: opens('Persona Chats'),
  });
  /** The sidebar tree, with the Agents row's count of sessions that need the owner (§2.7, the Agents badge). */
  function tree(D, o) {
    const goes = treeGo(o.todos);
    const html = ui.sidebarTree(Object.assign({ selected: 'todos', counts: D.counts, go: goes }, o.tree || {}));
    const plain = ui.sbRow(Object.assign({ selected: false }, { icon: 'agents', label: 'Agents', disclosure: 'closed' }, goes.agents));
    const counted = ui.sbRow(Object.assign({ selected: false }, { icon: 'agents', label: 'Agents', disclosure: 'closed', count: D.counts.agentsBadge, countLook: 'needs' }, goes.agents));
    return html.replace(plain, counted);
  }
  const moreItems = () => [
    Object.assign({ label: 'Sort By', sub: 'Recent Activity' }, opens('Sort By')),
    Object.assign({ label: 'Show Someday' }, opens('Show Someday')),
    Object.assign({ label: 'Show Done Today' }, opens('Show Done Today')),
  ];
  const groupItems = (closeNav) => [
    { header: 'Group by' },
    Object.assign({ label: 'Phase', check: true }, closeNav),
    Object.assign({ label: 'Project' }, opens('All To-dos grouped by Project')),
    Object.assign({ label: 'Due Date' }, opens('All To-dos grouped by Due Date')),
  ];

  /* ---------------------------------------------------------------- shared overlays */

  /** A menu anchored at (top, right), over a transparent layer that closes it. */
  const anchoredMenu = (items, top, right, closeNav, o) => `<div class="v3-dismiss"${navA(closeNav)}></div><div class="v3-anchor" style="top:${top}px;${o && o.left != null ? `left:${o.left}px` : `right:${right}px`}">${ui.menu(items, { align: o && o.left != null ? 'leading' : 'trailing', width: o && o.width })}</div>`;
  /** iOS 26 alert: the to-do, the question, stacked buttons (labels never wrap). */
  const alertBox = (o) => `<div class="k-scrim"${navA(o.cancel)}></div><div class="v3-alert-wrap"><div class="v3-alert" role="alertdialog">
      <div class="v3-alert-t">${o.title}</div><div class="v3-alert-m">${o.message}</div>
      <div class="v3-alert-bs">${o.buttons.map((b) => `<span class="v3-alert-b${b.danger ? ' is-danger' : ''}${b.bold ? ' is-bold' : ''}"${navA(b.nav)}>${b.label}</span>`).join('')}</div>
    </div></div>`;
  /** The full ask card laid over a body (dimmed 0.2) from the spine down. */
  const overCard = (card, closeNav) => `<div class="v3-over"><div class="k-scrim"${navA(closeNav)}></div><div class="v3-over-card">${card}</div></div>`;
  /** A positive in-place line on a row: "✓ Starts when Desk Mac is back · Undo". */
  const okLine = (text, undo) => `${I('checkmark', 13)}<span class="k-tr-text"><span class="v3-pos">${text}</span><span class="k-sep">·</span><span class="v3-undo"${navA(undo)}>Undo</span></span>`;

  /* ================================================================ iPhone · the list (V3-01) */

  const L = 'iphone/list';
  function phoneRowOpts(t, st) {
    st = st || {};
    const kind = lmKind(t);
    const o = { toast: `Opens: ${t.title}`, markNav: kind === 'todo' ? { toast: 'Done · Undo' } : opens(`${t.title}`) };
    switch (t.id) {
      case 't01':
        Object.assign(o, { go: 'iphone/kyoto-paper', transition: 'push', markNav: go('iphone/kyoto-paper', 'push'),
          answer: { go: L + '~answered', transition: 'none', options: go(L + '~options') } });
        break;
      case 't02':
        Object.assign(o, { go: st.approved ? 'iphone/release-paper~approved' : 'iphone/release-paper', transition: 'push',
          markNav: go(st.approved ? 'iphone/release-paper~approved' : 'iphone/release-paper', 'push'),
          answer: { go: 'iphone/release-widget-session', transition: 'push' },
          crewNav: { widget: go('iphone/release-paper~run-card', 'push'), 'release-notes': opens('Release notes · its card in the spine'), screenshots: opens('Screenshots · its card in the spine') } });
        break;
      case 't03':
        Object.assign(o, { markNav: opens('Sort last year’s photos, at its ask'),
          answer: { go: L + '~back', options: go(L + '~options-photos') } });
        break;
      case 't13': o.answer = opens('Renew the car insurance, with the composer focused'); break;
      case 't05': o.long = L + '~mark-done'; break;
      case 't07': Object.assign(o, { go: 'iphone/dentist-paper', transition: 'push', markNav: go('iphone/dentist-paper', 'push') }); break;
      // ○ ticks in place: the bar keeps the owner's choice (All, or To do), and Undo returns to the same view.
      case 't11': o.markNav = go(st.calm ? L + '~calm-ticked' : st.filter === 'todo' ? L + '~ticked' : L + '~end-ticked'); break;
      default: break;
    }
    return o;
  }

  /**
   * The iPhone list page. o: { D, filter, rows (per-section override), rowState, rowOverride, canWaitOpen, headerOpts,
   * barCounts, menu, more, group, drawer, alert, toast: { text, undo }, end (scrolled to the end), scroll (px scrolled),
   * pill (false: none), empty }
   */
  function phoneList(ctx, o) {
    o = o || {};
    const D = o.D || ctx.data;
    const anim = takeAnim(ctx);
    const rs = Object.assign({ filter: o.filter || 'all', end: !!o.end }, o.rowState);
    const calm = D.isCalm('all');
    const lead = withAnim(ui.btn({ icon: 'line-3-horizontal', title: 'Show Sidebar', go: o.drawer ? L : L + '~drawer', transition: 'none' }), 'drawer');
    const center = ui.titleMenu({ title: 'All To-dos', open: !!o.menu, go: o.menu ? L : L + '~menu', transition: 'none', items: placeItems(go(L)) });
    const bar = barHtml(D, o.filter, (id) => go(id === 'all' ? L : `${L}~filter-${id}`), { group: go(o.group ? L : L + '~group'), counts: o.barCounts });
    const top = ui.navbar({
      leading: [lead], center,
      trailing: [{ icon: 'magnifying-glass', title: 'Search', toast: 'Opens: Search To-dos' }, { icon: 'ellipsis', title: 'More', go: o.more ? L : L + '~more', transition: 'none' }],
      below: bar, fog: !!(o.end || o.scroll),
    });
    let list = sectionsHtml(D, {
      filter: o.filter, rows: o.rows, canWaitOpen: o.canWaitOpen, headerOpts: o.headerOpts,
      rowOpts: (t) => Object.assign(phoneRowOpts(t, rs), { line3: calm ? false : undefined }, (o.rowOverride || {})[t.id]),
      foldNav: { can_wait: go(o.canWaitOpen ? L : L + '~can-wait'), someday: opens('Someday') },
    });
    if (o.empty) list = ui.empty(o.empty);
    const overlay = [
      o.drawer || o.pill === false ? '' : ui.pill({ label: 'New To-Do', go: 'iphone/capture-passport~empty', transition: 'fade' }),
      o.toast ? ui.toast(o.toast.text, { action: o.toast.undo ? 'Undo' : '', actionNav: o.toast.undo, bottom: 110 }) : '',
      o.menu ? `<div class="v3-dismiss is-under"${navA(go(L))}></div>` : '',
      o.more ? anchoredMenu(moreItems(), 110, 16, go(L), { width: 240 }) : '',
      o.group ? anchoredMenu(groupItems(go(L)), 176, 16, go(L), { width: 220 }) : '',
      o.drawer ? ui.drawer({ content: tree(D, { todos: go(L) }), scrimNav: go(L) }) : '',
      o.alert || '',
    ].join('');
    const body = o.end ? `<div class="v3-end">${list}</div>` : `<div class="v3-list"${o.scroll ? ` style="margin-top:-${o.scroll}px"` : ''}>${list}</div>`;
    return ui.page({ top, body, overlay, cls: 'v3-listpage' + (o.drawer || o.pill === false ? '' : ' has-pill') + anim });
  }

  const LIST_NOTE = 'Rows as V1 with the <b>living mark</b> in the leading slot and no mark on line 2: ◉ needs you · ◔ working · ◎ just finished · ◌ waiting (dashed, with clock hands) · ○ to do · ● done; a Can wait row wears the blue ring holding three small dots (listed, not counted, never tickable). Only ○ ticks on a tap; ◉ on T01 opens the details at its ask; ◎ opens T07. T02’s row carries the crew line (three runs). The bar (E2) leads with Needs you and draws whole chips only; All, where the list lands, is pinned at its trailing end beside the grouping icon, past a hairline. Rows passing under the New To-Do pill and the home indicator soften into a bottom scroll edge. Right-click ◔ on “Draft the school newsletter” = long press.';

  F(L, {
    device: 'iphone', important: true,
    title: 'iPhone · All To-dos with living marks',
    note: `V3-01. ${LIST_NOTE} Counts from Data: Needs you (${counts().needs}) on the bar and the header, In progress ${counts().inProgress}; sections in the E1 order Needs you · Working · Waiting · To do · Just finished.`,
    render: (ctx) => phoneList(ctx),
  });
  F(L + '~options', {
    device: 'iphone', title: 'iPhone · Options ⌄ unfolds the ask card in place',
    note: 'The full P1 card under T01’s row: Kaede · Tsuki, then Ryokan Hanami · Recommended, then Later · Don’t handle · Discuss…. The row keeps only “Options ⌃”, so the primary answer is drawn once.',
    render: (ctx) => phoneList(ctx, {
      rowOverride: { t01: { openCard: { close: go(L), card: askHome(ctx.data, { on: askOn({ hanami: go(L + '~answered'), later: go(L), discuss: go('iphone/kyoto-paper~discuss', 'push') }) }) } } },
    }),
  });
  F(L + '~options-photos', {
    device: 'iphone', title: 'iPhone · Options ⌄ on a failed start',
    note: 'T03’s own ask unfolds and the list scrolls it clear of the pill: Use MacBook Instead, then Start When It’s Back, then the link row. The row keeps only “Options ⌃”.',
    render: (ctx) => phoneList(ctx, {
      scroll: 344,
      rowOverride: { t03: { openCard: { close: go(L), card: ui.askCardFor('a3', { on: { primary: go(L + '~back'), other: opens('Sort last year’s photos on MacBook'), later: go(L), dont: opens('“Persona won’t sort the photos. You can undo this.”'), discuss: opens(`Sort last year’s photos, with “Answer: ${window.Data.ask('a3').title} ×”`) } }) } } },
    }),
  });

  // While an answered row waits for the list to be idle, the sections keep the counts of the rows they draw; only N
  // drops at once (§2.4: rows never move under a finger).
  const drawnCounts = (base) => ({ progress: base.counts.inProgress });
  F(L + '~answered', {
    device: 'iphone', title: 'iPhone · OK: Ryokan Hanami answered from the row',
    note: 'Line 2 reads “✓ You chose Ryokan Hanami · Undo” for 4 s; N drops to (2) on the bar and the header at once, while the other counts stay on the rows drawn. The row waits for the list to be idle, then moves (timed step).',
    auto: { go: L + '~booking', after: 4000, transition: 'none' },
    render: (ctx) => {
      const D = ctx.data.view({ answered: { a1: 'hanami' } });
      const base = ctx.data.sections('all', 'all');
      const rows = { needs: base.find((s) => s.id === 'needs').todos, working: base.find((s) => s.id === 'working').todos };
      return phoneList(ctx, { D, rows, barCounts: drawnCounts(ctx.data), headerOpts: { working: { count: rows.working.length } },
        rowOverride: { t01: { kind: 'working', answered: 'Ryokan Hanami', undo: go(L) } } });
    },
  });
  F(L + '~booking', {
    device: 'iphone', title: 'iPhone · the answered row moved to Working',
    note: 'Once the list is idle, T01 fades into Working: ◔ “Persona is booking Ryokan Hanami”. N is 2 everywhere; In progress counts it.',
    render: (ctx) => phoneList(ctx, { D: ctx.data.view({ answered: { a1: 'hanami' } }) }),
  });
  F(L + '~back', {
    device: 'iphone', title: 'iPhone · Start When It’s Back',
    note: 'The answer shows in place: ◌ “✓ Starts when Desk Mac is back · Undo”. N drops to (2) at once; the row re-files into Waiting once the list is idle (timed step).',
    auto: { go: L + '~back-moved', after: 4000, transition: 'none' },
    render: (ctx) => {
      const D = ctx.data.view({ a3: 'back' });
      const base = ctx.data.sections('all', 'all');
      const rows = { needs: base.find((s) => s.id === 'needs').todos, waiting: base.find((s) => s.id === 'waiting').todos };
      return phoneList(ctx, { D, rows, barCounts: drawnCounts(ctx.data), headerOpts: { waiting: { count: rows.waiting.length } },
        rowOverride: { t03: { kind: 'waiting', answer: false, lineHtml: okLine('Starts when Desk Mac is back', go(L)) } } });
    },
  });
  F(L + '~back-moved', {
    device: 'iphone', title: 'iPhone · the failed start re-filed into Waiting',
    note: 'Once idle, T03 fades into Waiting (“Starts when Desk Mac is back”), uncounted in N; the list is shown scrolled to it.',
    render: (ctx) => phoneList(ctx, { D: ctx.data.view({ a3: 'back' }), scroll: 330 }),
  });
  F(L + '~approved', {
    device: 'iphone', title: 'iPhone · back from approving Widget’s plan',
    note: 'After Approve in the Widget session, T02 is Working; N is (2) on the bar and the header.',
    render: (ctx) => phoneList(ctx, { D: ctx.data.view({ approved: { a2: true } }), rowState: { approved: true } }),
  });
  F(L + '~can-wait', {
    device: 'iphone', title: 'iPhone · Can wait unfolded',
    note: 'T13 is listed, not counted: its mark is the blue ring holding three small dots (not the ○ that ticks), and line 2 reads “Awaiting Reply · Persona asked about roadside cover” with Reply.',
    render: (ctx) => phoneList(ctx, { canWaitOpen: true }),
  });
  F(L + '~mark-done', {
    device: 'iphone', title: 'iPhone · long press on ◔ (Mark Done)',
    note: 'A long press on any mark offers Mark Done; with a run active it asks first.',
    render: (ctx) => phoneList(ctx, { alert: alertBox({ title: 'Draft the school newsletter', message: 'Persona is still working on this. Mark it done and stop?', cancel: go(L), buttons: [{ label: 'Done and Stop', danger: true, nav: opens('All To-dos with the newsletter done · Undo') }, { label: 'Cancel', bold: true, nav: go(L) }] }) }),
  });
  F(L + '~filter-needs', {
    device: 'iphone', title: 'iPhone · bar: Needs you', note: 'Needs you only, with Can wait folded at its foot.',
    render: (ctx) => phoneList(ctx, { filter: 'needs' }),
  });
  F(L + '~filter-progress', {
    device: 'iphone', title: 'iPhone · bar: In progress',
    note: `Working and Waiting (the tray’s In progress, ${counts().inProgress}), then Just finished at its foot without a count (E1: an uncounted group).`,
    render: (ctx) => phoneList(ctx, { filter: 'progress', headerOpts: { just_finished: { look: 'none' } } }),
  });
  F(L + '~filter-todo', {
    device: 'iphone', title: 'iPhone · bar: To do', note: 'The owner’s own to-dos, Someday folded. ○ is the only mark that ticks.',
    render: (ctx) => phoneList(ctx, { filter: 'todo' }),
  });
  F(L + '~ticked', {
    device: 'iphone', title: 'iPhone · ○ ticked', note: '○ on Renew passport ticks: strike-through, a haptic and “Done · Undo”; the row leaves once the list is idle.',
    render: (ctx) => phoneList(ctx, { filter: 'todo', rowOverride: { t11: { kind: 'done', ticked: true, markNav: go(L + '~filter-todo') } }, toast: { text: 'Done', undo: go(L + '~filter-todo') } }),
  });
  F(L + '~end', {
    device: 'iphone', title: 'iPhone · the list scrolled to its end',
    note: 'The rest of the marks: ◌ waiting, ○ to do, ◎ just finished and Someday · 1 folded. (A scroll position, drawn as a state.) ○ on Renew passport ticks here, under All.',
    render: (ctx) => phoneList(ctx, { end: true }),
  });
  F(L + '~end-ticked', {
    device: 'iphone', title: 'iPhone · ○ ticked under All',
    note: '○ on Renew passport ticks in place: strike-through, a haptic and “Done · Undo”; the bar stays on All, and Undo returns here. The row leaves once the list is idle.',
    render: (ctx) => phoneList(ctx, { end: true, rowOverride: { t11: { kind: 'done', ticked: true, markNav: go(L + '~end') } }, toast: { text: 'Done', undo: go(L + '~end') } }),
  });
  F(L + '~calm', {
    device: 'iphone', title: 'iPhone · calm (first use)',
    note: 'Only plain to-dos and nothing needs the owner: no bar, no section headers, two one-line rows and Someday · 1.',
    render: (ctx) => phoneList(ctx, { D: ctx.data.view({ calm: true }), rowState: { calm: true } }),
  });
  F(L + '~calm-ticked', {
    device: 'iphone', title: 'iPhone · calm, ○ ticked', note: 'The tick of a plain to-do: strike-through and “Done · Undo”.',
    render: (ctx) => phoneList(ctx, { D: ctx.data.view({ calm: true }), rowState: { calm: true }, rowOverride: { t11: { kind: 'done', ticked: true, markNav: go(L + '~calm') } }, toast: { text: 'Done', undo: go(L + '~calm') } }),
  });
  F(L + '~drawer', {
    device: 'iphone', title: 'iPhone · the drawer',
    note: `The sidebar tree over the dimmed list: To-dos (${counts().needs}) with Today, Upcoming and the projects’ blue parts; Release lead · Pinned (1) under Persona Chats; Agents ${counts().agentsBadge}.`,
    render: (ctx) => phoneList(ctx, { drawer: true }),
  });
  F(L + '~menu', {
    device: 'iphone', title: 'iPhone · the title menu', note: 'All To-dos ✓ · Today · Upcoming · the projects with their blue parts · All Projects….',
    render: (ctx) => phoneList(ctx, { menu: true }),
  });
  F(L + '~group', {
    device: 'iphone', title: 'iPhone · the bar’s grouping icon',
    note: 'E2: the grouping icon inside the bar opens Group by: Phase ✓ · Project · Due Date.',
    render: (ctx) => phoneList(ctx, { group: true }),
  });
  F(L + '~more', {
    device: 'iphone', title: 'iPhone · the list’s ⋯', note: 'Sort By · Show Someday · Show Done Today (grouping lives in the bar, E2).',
    render: (ctx) => phoneList(ctx, { more: true }),
  });
  F(L + '~added', {
    device: 'iphone', title: 'iPhone · a new to-do added (S1)',
    note: 'Add files “Renew passport” under To do, due Fri: the list scrolls to it, its row is lit for a moment, To do counts 2 again, and “Added · Undo”.',
    render: (ctx) => phoneList(ctx, { end: true, rowOverride: { t11: { cls: 'v3-new' } }, toast: { text: 'Added', undo: go(L) } }),
  });

  /* ================================================================ iPhone · T01, details and conversation (V3-02 … V3-04) */

  const KP = 'iphone/kyoto-paper';
  const KC = 'iphone/kyoto-conversation';
  const KCK = 'iphone/kyoto-conversation-keyboard';
  const R4_STATUS = { sending: 'Sending', processing: 'Processing', delivered: 'Delivered to Persona', replied: 'Delivered to Persona' };
  // Details state ↔ conversation state: the switch keeps the moment (drafts, sends) and only changes the zoom.
  const K_P2C = { '': KC, steps: KC, later: KC, menu: KC, answered: KC + '~answered', typing: KC + '~typing', discuss: KC + '~discuss', sending: KC + '~sending', processing: KC + '~processing', delivered: KC + '~delivered', sent: KC + '~replied', followup: KCK };
  const K_C2P = { '': KP, options: KP, run: KP, answered: KP + '~answered', typing: KP + '~typing', discuss: KP + '~discuss', sending: KP + '~sending', processing: KP + '~processing', delivered: KP + '~delivered', replied: KP + '~sent', keyboard: KP + '~followup' };
  const KB_SUGG = { A: ['included', 'include', 'includes'], B: ['room', 'rooms', 'ryokan'] };

  /** The thread's rounds for T01. o: { r4 (state), answered (option id) } → { rounds, per } */
  function kyotoRounds(D, raw, o) {
    const rounds = raw.rounds.slice();
    const per = {};
    if (o.r4) { rounds.push(raw.extraRounds.r4); per.r4 = { status: R4_STATUS[o.r4], hide: o.r4 === 'replied' ? [] : ['reply'] }; }
    if (o.answered) {
      // While the spine shows "✓ You chose … · Undo" (4 s), the round shows only the run the answer started; the asked
      // line becomes the decision line ("Ryokan: Hanami") once the confirmation ends, so the answer is never said twice.
      const label = D.ask('a1').options.find((x) => x.id === o.answered).label;
      const i = rounds.findIndex((r) => r.id === 'r3');
      const r3 = Object.assign({}, rounds[i]);
      r3.items = r3.items.filter((it) => it.kind !== 'asked').concat([{ kind: 'live', who: 'Persona', text: `booking ${label}` }]);
      rounds[i] = r3;
    }
    return { rounds, per };
  }

  /**
   * T01 on iPhone. o: { zoom, state (the key in K_P2C / K_C2P), kb, draft, draftKey, chip ('answer'), r4, answered, later,
   * menu, steps, options, run }
   */
  function kyotoPhone(ctx, o) {
    const anim = takeAnim(ctx);
    const D = o.answered ? ctx.data.view({ answered: { a1: o.answered } }) : ctx.data;
    const t = D.todo('t01');
    const raw = ctx.data.todo('t01');
    const a1 = D.ask('a1');
    const paper = o.zoom === 'paper';
    const self = paper ? (o.state ? `${KP}~${o.state}` : KP) : o.state === 'keyboard' ? KCK : (o.state ? `${KC}~${o.state}` : KC);
    const condensed = !paper || o.kb;
    const swNav = { paper: go(paper ? self : K_C2P[o.state || ''] || KP), conversation: go(paper ? K_P2C[o.state || ''] || KC : self) };
    const choice = o.answered ? a1.options.find((x) => x.id === o.answered).label : '';

    let ask = '';
    if (o.answered && !paper) ask = askLine('', { answered: { text: `You chose ${choice}`, undo: go(KC) } });
    else if (t.asks.length && condensed && !o.later) {
      ask = askLine(a1.title, {
        ok: o.options ? false : { label: raw.answer.short, go: paper ? KP + '~answered' : KC + '~answered', transition: 'none' },
        options: o.options ? go(KC) : paper ? go(KP) : go(KC + '~options'), open: !!o.options,
      });
    }
    // "Since you looked: flights held · 1 question" under the state while there are unseen changes (§2.5 #3); it opens
    // the conversation at the first unseen round.
    const recap = !condensed && t.recap ? withAnim(ui.recapLine(t.recap, go(KC, 'none')), 'body-left') : '';
    const sp = spine(t, { mode: condensed ? 'condensed' : 'expanded', stateRow: !o.kb && !paper, short: condensed, ask, recap });

    let body;
    const r4 = raw.extraRounds.r4;
    const lastText = raw.rounds[raw.rounds.length - 1].text;
    if (paper && o.kb) {
      // The details scrolled to their foot above the keyboard: the to-do stays in view in the spine.
      const lx = o.r4
        ? latest({ text: r4.text, nav: swNav.conversation, anim: 'body-left', status: R4_STATUS[o.r4], reply: o.r4 === 'replied' ? r4.items[0] : null })
        : latest({ text: lastText, nav: swNav.conversation, anim: 'body-left' });
      body = paperFoot(t, lx);
    } else if (paper) {
      const on = askOn({ hanami: go(KP + '~answered'), later: go(KP + '~later'), discuss: go(KP + '~discuss') });
      const card = o.answered ? ui.askCard({ answered: `You chose ${choice}`, undo: go(KP) })
        : o.later ? ui.askCard({ title: a1.title, later: a1.later, go: KP, transition: 'none' })
          : askHome(D, { on });
      const steps = o.steps ? stepsOf(t, { headNav: go(KP) }) : stepsSum(t, go(KP + '~steps'));
      body = `<div class="v3-paper">
        ${card}
        ${chipsOf(t)}
        ${ui.notes(t.notes, opens('the notes, edited in place'))}
        ${steps}
        ${resultsOf(t, { short: true })}
        ${latest({ text: lastText, nav: swNav.conversation, anim: 'body-left' })}
      </div>`;
    } else {
      const th = kyotoRounds(D, raw, o);
      body = ui.thread(th.rounds, {
        anchor: 'bottom', askedSuffix: ASKED_ABOVE, per: th.per,
        open: o.run ? { 'Ryokan search': true } : undefined,
        nav: { session: opens('Open Session · the run has ended') },
        bgNav: opens('the Background round, unfolded'),
      });
      // The run line of the latest round unfolds in place.
      const i = body.lastIndexOf('class="k-rl k-rl--run');
      if (i >= 0) body = `${body.slice(0, i)}${Kit.goAttr(o.run ? KC : KC + '~run', 'none')} ${body.slice(i)}`;
      if (o.options) body += overCard(askHome(D, { on: askOn({ hanami: go(KC + '~answered'), later: go(KC), discuss: go(KC + '~discuss') }) }), go(KC));
    }

    const draft = o.draft;
    const sendTo = paper ? (o.state === 'followup' ? opens('Sending → Processing → Delivered to Persona') : go(KP + '~sending')) : (o.state === 'keyboard' ? opens('Sending → Processing → Delivered to Persona') : go(KC + '~sending'));
    const typingSelf = paper ? KP + '~typing' : KC + '~typing';
    let comp = ui.todoComposer({
      to: o.chip === 'answer' ? { answer: a1.title, go: typingSelf, transition: 'none' } : 'Persona',
      text: draft, send: draft ? sendTo : null,
    });
    if (!o.kb) comp = `<div class="v3-tap"${navA(go(paper ? KP + '~typing' : KCK))}>${comp}</div>`;

    const overlay = o.menu ? anchoredMenu(raw.menu.map((m) => Object.assign({ label: m, danger: /^Delete/.test(m) }, opens(m))), 110, 16, go(KP), { width: 304 }) : '';
    const nav = ui.navbar({
      leading: [{ icon: 'chevron-left', title: 'All To-dos', go: L, transition: 'pop' }],
      center: bodySwitch(o.zoom, { nav: swNav, dot: paper && (!o.r4 || o.r4 === 'replied'), anim: { conversation: 'body-left', paper: 'body-right' } }),
      trailing: [paper ? { icon: 'ellipsis', title: 'More', go: o.menu ? KP : KP + '~menu', transition: 'none' } : { icon: 'ellipsis', title: 'More', toast: 'Opens: Copy Link · Chat Topic… · Decisions' }],
    });
    const out = ui.page({ top: nav + sp, body: `<div class="v3-bin">${body}</div>`, bottom: comp, pad: false, overlay, cls: 'v3-todo' + anim });
    return o.kb ? { content: out, keyboard: { suggestions: draft ? KB_SUGG[o.draftKey || 'A'] : NEUTRAL_SUGG } } : out;
  }

  const DRAFT_A = ctxDraft('quickNote');
  const DRAFT_B = 'and ask about a quiet r';
  function ctxDraft(k) { return window.Data && window.Data.todo('t01') ? window.Data.todo('t01').drafts[k] : ''; }

  F(KP, {
    device: 'iphone', important: true,
    title: 'iPhone · Kyoto: spine + Details',
    note: 'V3-02. The bar holds ‹, the body switch (Details · Conversation •) and ⋯; the switch never moves. The spine is expanded: ◉ title 22, the state and “Since you looked: flights held · 1 question” (it opens the conversation). Its ask line is absent because the full card is in view (drawn once). The details: the ask card at its home, chips, notes, “Steps 2 of 5” folded to one line, Results and the one-line “Latest · You “…” ›”, all above the composer. Conversation (or Latest ›) slides the body left under the still spine; the composer stays. ⤢ is hidden on phones.',
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper' }),
  });
  F(KP + '~steps', {
    device: 'iphone', title: 'iPhone · Kyoto details: Steps unfolded',
    note: '“Steps 2 of 5 ›” unfolds the five steps in place (who has each); the page now scrolls on to Results and Latest. Tap the Steps header to fold them.',
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'steps', steps: true }),
  });
  F(KP + '~answered', {
    device: 'iphone', title: 'iPhone · Kyoto details: Ryokan Hanami chosen',
    note: 'The card collapses to “✓ You chose Ryokan Hanami · Undo”; the mark turns ◔ and the state reads “Working · Persona is booking Ryokan Hanami”; the step is ✓ · you.',
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'answered', answered: 'hanami' }),
  });
  F(KP + '~later', {
    device: 'iphone', title: 'iPhone · Kyoto details: Later',
    note: 'Later folds the card to one line, “Which ryokan? · later today ›”; the to-do stays in Needs you.',
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'later', later: true }),
  });
  F(KP + '~menu', {
    device: 'iphone', title: 'iPhone · Kyoto details: ⋯',
    note: 'Copy Link · Move to Project… · Someday · Decisions (2) · Chat Topic… · Start an Agent on a Computer… · Delete….',
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'menu', menu: true }),
  });
  F(KP + '~typing', {
    device: 'iphone', title: 'iPhone · Kyoto details: telling Persona from the details (S5 lite)',
    note: 'The composer is focused on the details: the card scrolls out of view, so the spine condenses (title · state, the open ask) and the details show their foot. Nothing opens. ↑ sends.',
    chrome: { keyboard: true },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'typing', kb: true, draft: DRAFT_A }),
  });
  F(KP + '~discuss', {
    device: 'iphone', title: 'iPhone · Kyoto details: Discuss… (answer chip)',
    note: 'Discuss… focuses the composer with “Answer: Which ryokan? ×”: the message answers the ask. × returns to To Persona.',
    chrome: { keyboard: true },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'discuss', kb: true, chip: 'answer' }),
  });
  F(KP + '~sending', {
    device: 'iphone', title: 'iPhone · Kyoto details: Sending', note: 'The round’s state under the Latest line: Sending (faint, < 0.1 s).',
    chrome: { keyboard: true }, auto: { go: KP + '~processing', after: 350, transition: 'none' },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'sending', kb: true, r4: 'sending', draft: DRAFT_A }),
  });
  F(KP + '~processing', {
    device: 'iphone', title: 'iPhone · Kyoto details: Processing', note: 'The hub stored it; the field clears and the keyboard stays up.',
    chrome: { keyboard: true }, auto: { go: KP + '~delivered', after: 900, transition: 'none' },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'processing', kb: true, r4: 'processing' }),
  });
  F(KP + '~delivered', {
    device: 'iphone', title: 'iPhone · Kyoto details: Delivered to Persona', note: 'Taken by the steward; about 3 s later the reply folds in.',
    chrome: { keyboard: true }, auto: { go: KP + '~sent', after: 2600, transition: 'none' },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'delivered', kb: true, r4: 'delivered' }),
  });
  F(KP + '~sent', {
    device: 'iphone', title: 'iPhone · Kyoto details: the reply lands in the details',
    note: '“└ Persona: Hanami and Tsuki include breakfast; Kaede doesn’t.” Nothing opened; the Conversation segment carries the unread dot.',
    chrome: { keyboard: true },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'sent', kb: true, r4: 'replied' }),
  });
  F(KP + '~followup', {
    device: 'iphone', title: 'iPhone · Kyoto details: a follow-up while the last one processes',
    note: 'V3-04’s moment at the Details zoom: the draft and keyboard stay when the zoom changes.',
    chrome: { keyboard: true },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'paper', state: 'followup', kb: true, r4: 'processing', draft: DRAFT_B, draftKey: 'B' }),
  });

  F(KC, {
    device: 'iphone', important: true,
    title: 'iPhone · Kyoto: spine + Conversation',
    note: 'V3-03. The same page, the body at the other zoom: the switch stayed where it was tapped; the spine is condensed (title, state, the ask answerable: OK: Hanami · Options ⌄); the body holds every round, latest at the bottom, under a soft scroll edge. The thread shows the ask only as “● Asked: Which ryokan? · answer above ↑”. Details slides the body right; ‹ always pops to the list.',
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation' }),
  });
  F(KC + '~options', {
    device: 'iphone', title: 'iPhone · Kyoto conversation: Options ⌄',
    note: 'The full ask card unfolds from the spine over the thread (dimmed 0.2); the spine keeps only “Options ⌃”, so the answers are drawn once. Answering folds it back.',
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation', state: 'options', options: true }),
  });
  F(KC + '~answered', {
    device: 'iphone', title: 'iPhone · Kyoto conversation: OK: Hanami',
    note: 'Answered from the spine: its ask line reads “✓ You chose Ryokan Hanami · Undo” for 4 s, the state turns ◔ “Working · booking Ryokan Hanami”, and the round shows only the run the answer started (“◔ Persona · booking Ryokan Hanami”); the asked line turns into the decision line once the 4-s confirmation ends, so the answer is never said twice.',
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation', state: 'answered', answered: 'hanami' }),
  });
  F(KC + '~run', {
    device: 'iphone', title: 'iPhone · Kyoto conversation: a run line unfolded',
    note: '▸ unfolds the run’s latest 3 steps, one line each, and Open Session › (the run has ended).',
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation', state: 'run', run: true }),
  });
  F(KC + '~typing', {
    device: 'iphone', title: 'iPhone · Kyoto conversation: typing',
    note: 'The same draft as in the details: switching the zoom never touches the composer.',
    chrome: { keyboard: true },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation', state: 'typing', kb: true, draft: DRAFT_A }),
  });
  F(KC + '~discuss', {
    device: 'iphone', title: 'iPhone · Kyoto conversation: answer chip',
    note: 'Discuss… at the conversation zoom: “Answer: Which ryokan? ×”.',
    chrome: { keyboard: true },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation', state: 'discuss', kb: true, chip: 'answer' }),
  });
  F(KC + '~sending', {
    device: 'iphone', title: 'iPhone · Kyoto conversation: Sending', note: 'Sending (faint) under the bubble.',
    chrome: { keyboard: true }, auto: { go: KC + '~processing', after: 350, transition: 'none' },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation', state: 'sending', kb: true, r4: 'sending', draft: DRAFT_A }),
  });
  F(KC + '~processing', {
    device: 'iphone', title: 'iPhone · Kyoto conversation: Processing', note: 'The status line is replaced in place.',
    chrome: { keyboard: true }, auto: { go: KC + '~delivered', after: 900, transition: 'none' },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation', state: 'processing', kb: true, r4: 'processing' }),
  });
  F(KC + '~delivered', {
    device: 'iphone', title: 'iPhone · Kyoto conversation: Delivered to Persona', note: 'Then the reply.',
    chrome: { keyboard: true }, auto: { go: KC + '~replied', after: 2600, transition: 'none' },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation', state: 'delivered', kb: true, r4: 'delivered' }),
  });
  F(KC + '~replied', {
    device: 'iphone', title: 'iPhone · Kyoto conversation: the reply',
    note: '“Persona: Hanami and Tsuki include breakfast; Kaede doesn’t.”',
    chrome: { keyboard: true },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation', state: 'replied', kb: true, r4: 'replied' }),
  });
  F(KCK, {
    device: 'iphone', important: true,
    title: 'iPhone · Kyoto conversation, keyboard up',
    note: 'V3-04. Keyboard budget: status 54 + bar 44 (with the switch) + spine 76 (title · Needs you, the open ask) + ≈ 280 of thread + composer 52 + keyboard 336. Title, state and the open ask stay in view while typing the follow-up “and ask about a quiet r…”.',
    chrome: { keyboard: true },
    render: (ctx) => kyotoPhone(ctx, { zoom: 'conversation', state: 'keyboard', kb: true, r4: 'processing', draft: DRAFT_B, draftKey: 'B' }),
  });

  /* ================================================================ iPhone · T02, the crew line and the run card (V3-05, V3-06) */

  const RP = 'iphone/release-paper';
  const RC = 'iphone/release-conversation';
  const RS = 'iphone/release-widget-session';
  const S5 = ['s5', 'sending', 'processing', 'sent', 'taken', 'send-now'];
  const S5_STATUS = { sending: 'Sending', processing: 'Processing' };
  const S5_SUGG = ['notes', 'note', 'noted'];

  /** The S5 round's lines after Delivered: the steward's reply, the relay (queued / taken / sent now), the live run line. */
  function s5Lines(t, s5, sendNowNav) {
    const r2 = t.extraRounds.r2;
    const relay = s5 === 'sent' ? ui.rline.relay({ to: 'Release notes', state: 'queued', sendNow: true, sendNowNav })
      : s5 === 'taken' ? ui.rline.relay({ to: 'Release notes', state: 'delivered' })
        : `<div class="k-rl k-rl--relay">${I('arrow-up-right', 12)}<span>${t.relayStates.now}</span></div>`;
    return { reply: r2.items[0], relay, live: s5 === 'sent' ? '' : ui.rline.live({ who: 'Release notes', text: 'adding the widget' }) };
  }

  /** Work details (Agents audience): one glyph column of 20 pt, the Done when row wraps instead of cutting. */
  function workDetailsOf(t, o) {
    const wd = t.workDetails;
    const rows = o.open ? wd.rows.map((r) => {
      const g = r.mark ? ui.lineMark(r.mark, r.mark === 'needs' ? 8 : r.mark === 'working' ? 11 : 12) : I(r.icon, 14);
      const nav = r.run === 'widget' ? go(RS, 'push') : r.run ? opens(`${r.text.split(' · ')[0]}’s session`) : opens(r.text);
      if (r.icon === 'pr') {
        // The code change: its line, then Review Changes (the session's review view, §2.5 #11).
        return `<div class="k-wd-r v3-wd-r is-wrap"><span class="v3-wd-g">${g}</span><span class="v3-wd-t"><span class="v3-wd-tx">${r.text}</span><span class="v3-wd-act" role="button"${navA(opens('Review Changes · the code change, file by file'))}>Review Changes${I('chevron-right', 11)}</span></span></div>`;
      }
      return `<div class="k-wd-r v3-wd-r${r.icon === 'checklist' ? ' is-wrap' : ''}"${navA(nav)}><span class="v3-wd-g">${g}</span><span class="v3-wd-t">${r.text}</span>${r.run ? I('chevron-right', 11) : ''}</div>`;
    }).join('') : '';
    return `<div class="k-wd${o.open ? ' is-open' : ''}"><div class="k-wd-line"${navA(o.nav)}><b>Work details</b><span class="k-sep">·</span><span>${wd.summary}</span>${I(o.open ? 'chevron-down' : 'chevron-right', 11)}</div>${o.open ? `<div class="k-wd-rows">${rows}</div>` : ''}</div>`;
  }

  /** T02's rounds: with the door in the spine the needs line points up to it; after Approve it is Widget's live line. */
  function releaseRounds(raw, o) {
    return raw.rounds.map((r) => Object.assign({}, r, {
      items: r.items.map((it) => (it.kind !== 'needs' ? it
        : o.approved ? { kind: 'live', who: it.who, text: 'building the widget' }
          : Object.assign({}, it, { text: `${it.text} <i>${ASKED_ABOVE}</i>` }))),
    }));
  }

  /**
   * T02 on iPhone. o: { zoom, state, kb, runCard, work, approved, toMenu }
   */
  function releasePhone(ctx, o) {
    const anim = takeAnim(ctx);
    const D = o.approved ? ctx.data.view({ approved: { a2: true } }) : ctx.data;
    const t = D.todo('t02');
    const raw = ctx.data.todo('t02');
    const paper = o.zoom === 'paper';
    const st = o.state || '';
    const keep = S5.includes(st) || st === 'approved' ? '~' + st : '';
    const other = (paper ? RC : RP) + keep;
    const condensed = !paper || o.kb;
    const sw = { nav: paper ? { conversation: go(other) } : { paper: go(other) }, anim: { conversation: 'body-left', paper: 'body-right' } };
    const review = go(RS, 'push');
    // Condensed, the crew line is hidden, so the door names its run and what it asks ("Widget · needs your OK"), whole
    // beside Review Plan › on a 402 iPhone.
    const wStep = (t.steps || []).find((x) => x.run === 'widget');
    const doorQ = condensed ? `${D.run('widget').title} · ${wStep ? wStep.word : D.ask('a2').title}` : D.ask('a2').title;
    const door = t.asks.length ? askLine(doorQ, { door: Object.assign({ label: D.ask('a2').door }, review) }) : '';
    const crewNav = {
      widget: go(o.runCard ? RP : RP + '~run-card'),
      'release-notes': opens('Release notes · its card in the spine'),
      screenshots: opens('Screenshots · its card in the spine'),
    };
    const sp = spine(t, {
      mode: condensed ? 'condensed' : 'expanded', short: condensed, stateRow: !paper && !o.kb,
      crew: condensed ? '' : crewLine(t, { nav: crewNav, on: o.runCard ? 'widget' : '' }),
      card: o.runCard ? runCard(D, { review, open: go(RS, 'push'), close: go(RP) }) : '',
      ask: o.runCard ? '' : door,
    });

    const s5 = S5.includes(st) && st !== 's5' ? st : '';
    const scope = ['sent', 'taken', 'send-now'].includes(s5) ? `<div class="v3-scope">${ui.rline.scope({ text: '+ home-screen widget in the release notes', undo: opens('“Scope change undone” in the round') })}</div>` : '';
    let body;
    if (paper) {
      let lx;
      if (s5) {
        const lines = ['sent', 'taken', 'send-now'].includes(s5) ? s5Lines(raw, s5, go(RP + '~send-now')) : null;
        lx = latest({
          text: raw.extraRounds.r2.text, nav: go(other), anim: 'body-left',
          status: S5_STATUS[s5] || 'Delivered to Release lead',
          reply: lines ? lines.reply : null,
          extra: lines ? `${lines.relay}${lines.live}` : '',
        });
      } else {
        // The latest of T02's conversation is the lead's split (the V3-05 wireframe).
        lx = latest({ text: raw.rounds[raw.rounds.length - 1].fold, quote: false, nav: go(other), anim: 'body-left' });
      }
      const brief = ui.briefLineFor(t, opens('What you asked · Done when: build uploaded, notes approved, widget ships'));
      const work = workDetailsOf(t, { open: !!o.work, nav: go(o.work ? RP : RP + '~work') });
      body = o.kb
        ? footOf(`${chipsOf(t)}${brief}${scope}${lx}`)
        : `<div class="v3-paper">${chipsOf(t)}${brief}${scope}${resultsOf(t, { short: true })}${lx}${work}</div>`;
    } else {
      const rounds = releaseRounds(raw, o);
      const per = {};
      if (s5) {
        rounds.push(raw.extraRounds.r2);
        if (S5_STATUS[s5]) per.r2 = { status: S5_STATUS[s5], hide: ['reply', 'relay', 'scope', 'live'] };
        else per.r2 = { relay: s5 === 'send-now' ? 'now' : s5 === 'taken' ? 'delivered' : 'queued', hide: s5 === 'sent' ? ['live'] : [] };
      }
      let th = ui.thread(rounds, { anchor: 'bottom', per, nav: { sendNow: go(RC + '~send-now'), undo: opens('“Scope change undone” in the round') } });
      // E3: Send Now is the run's own send-now, never an interrupt; the line is Data's wording.
      th = th.replace(/Sent now to Release notes/g, raw.relayStates.now);
      body = th;
    }

    const draftOn = st === 's5' || st === 'sending';
    const toMenu = o.toMenu ? ui.menu(raw.composer.menu.map((m) => Object.assign({}, m, opens(`To ${m.label}`))), { align: 'leading', width: 270 }) : '';
    const chipNav = paper && !o.kb ? go(o.toMenu ? RP : RP + '~to-menu') : opens('the addressee menu: Release lead ✓ · Persona · Widget · Release notes');
    let comp = ui.todoComposer({
      to: Object.assign({}, raw.composer.to, chipNav),
      text: draftOn ? raw.drafts.s5 : '', send: st === 's5' ? go(`${paper ? RP : RC}~sending`) : null,
      menu: toMenu,
    });
    if (!o.kb) comp = `<div class="v3-tap"${navA(go(`${paper ? RP : RC}~s5`))}>${comp}</div>`;

    const nav = ui.navbar({
      leading: [{ icon: 'chevron-left', title: 'All To-dos', go: o.approved ? L + '~approved' : L, transition: 'pop' }],
      center: bodySwitch(o.zoom, sw),
      trailing: [{ icon: 'ellipsis', title: 'More', toast: 'Opens: Copy Link · Move to Project… · Someday · Decisions · Chat Topic… · Delete…' }],
    });
    const overlay = o.toMenu ? `<div class="v3-dismiss is-under"${navA(go(RP))}></div>` : '';
    const out = ui.page({ top: nav + sp, body: `<div class="v3-bin">${body}</div>`, bottom: comp, pad: false, overlay, cls: 'v3-todo' + anim });
    return o.kb ? { content: out, keyboard: { suggestions: draftOn ? S5_SUGG : NEUTRAL_SUGG } } : out;
  }

  F(RP, {
    device: 'iphone',
    title: 'iPhone · Release 1.3: crew line in the spine',
    note: 'V3-05 (left). Three runs, so the spine carries the crew line “● Widget ◔ Release notes ✓ Screenshots” and the door “● Plan awaiting approval [Review Plan ›]”. The details have no Steps block: the runs are the crew line. Latest is the lead’s split. Composer: To Release lead ▾.',
    render: (ctx) => releasePhone(ctx, { zoom: 'paper' }),
  });
  F(RP + '~run-card', {
    device: 'iphone',
    title: 'iPhone · Release 1.3: Widget’s run card in the spine',
    note: 'V3-05 (right). The Widget capsule unfolds its card inside the spine: who, the latest line, its ask door, Open Session ›. The door line folds into the card (drawn once). ⌃ or the capsule folds it.',
    render: (ctx) => releasePhone(ctx, { zoom: 'paper', state: 'run-card', runCard: true }),
  });
  F(RP + '~work', {
    device: 'iphone', title: 'iPhone · Release 1.3: Work details unfolded',
    note: 'The only place with computers, roles and the code change: Agents by title, role, computer and state; the change; Done when.',
    render: (ctx) => releasePhone(ctx, { zoom: 'paper', state: 'work', work: true }),
  });
  F(RP + '~to-menu', {
    device: 'iphone', title: 'iPhone · Release 1.3: the addressee menu',
    note: 'To Release lead ▾: Release lead ✓ · Persona · Widget (worker · Needs you) · Release notes (helper · Working).',
    render: (ctx) => releasePhone(ctx, { zoom: 'paper', state: 'to-menu', toMenu: true }),
  });
  F(RP + '~approved', {
    device: 'iphone', title: 'iPhone · Release 1.3 after Approve',
    note: 'Back from the session: the door is gone, the mark is ◔, “Working · 2 running · 1 done”, Widget ◔ in the crew line; N is (2).',
    render: (ctx) => releasePhone(ctx, { zoom: 'paper', state: 'approved', approved: true }),
  });
  F(RP + '~s5', {
    device: 'iphone', title: 'iPhone · Release 1.3: S5 typed in the details',
    note: 'S5: “also add the home-screen widget to the release notes”, To Release lead. The spine keeps title, state and the door above the keyboard.',
    chrome: { keyboard: true },
    render: (ctx) => releasePhone(ctx, { zoom: 'paper', state: 's5', kb: true }),
  });
  F(RP + '~sending', {
    device: 'iphone', title: 'iPhone · Release 1.3: Sending', note: 'Sending → Processing → Delivered to Release lead, under the Latest line.',
    chrome: { keyboard: true }, auto: { go: RP + '~processing', after: 350, transition: 'none' },
    render: (ctx) => releasePhone(ctx, { zoom: 'paper', state: 'sending', kb: true }),
  });
  F(RP + '~processing', {
    device: 'iphone', title: 'iPhone · Release 1.3: Processing', note: 'The field clears; the keyboard stays.',
    chrome: { keyboard: true }, auto: { go: RP + '~sent', after: 1200, transition: 'none' },
    render: (ctx) => releasePhone(ctx, { zoom: 'paper', state: 'processing', kb: true }),
  });
  F(RP + '~sent', {
    device: 'iphone', title: 'iPhone · Release 1.3: Delivered to Release lead, queued for Release notes',
    note: 'The reply, “↗ Queued for Release notes · after this step [Send Now]”, and the scope line “+ home-screen widget … · Undo” under What you asked. After 6 s the relay is taken.',
    chrome: { keyboard: true }, auto: { go: RP + '~taken', after: 6000, transition: 'none' },
    render: (ctx) => releasePhone(ctx, { zoom: 'paper', state: 'sent', kb: true }),
  });
  F(RP + '~taken', {
    device: 'iphone', title: 'iPhone · Release 1.3: taken by Release notes',
    note: '“↗ Delivered to Release notes” and “◔ Release notes · adding the widget”: the work itself is the proof.',
    chrome: { keyboard: true },
    render: (ctx) => releasePhone(ctx, { zoom: 'paper', state: 'taken', kb: true }),
  });
  F(RP + '~send-now', {
    device: 'iphone', title: 'iPhone · Release 1.3: Send Now',
    note: 'Send Now is the run’s own send-now, not an interrupt: “↗ Sent now to Release notes” (errata E3).',
    chrome: { keyboard: true },
    render: (ctx) => releasePhone(ctx, { zoom: 'paper', state: 'send-now', kb: true }),
  });

  F(RC, {
    device: 'iphone', title: 'iPhone · Release 1.3: spine + Conversation',
    note: 'The condensed spine keeps the door and names its run (“● Widget · needs your OK [Review Plan ›]”), since the crew line is hidden; the thread is Release lead’s rounds with one line per run, and Widget’s line points up to the door (“· answer above ↑”).',
    render: (ctx) => releasePhone(ctx, { zoom: 'conversation' }),
  });
  F(RC + '~approved', {
    device: 'iphone', title: 'iPhone · Release 1.3 conversation after Approve', note: 'No door; Working. Widget’s line in the round is live: “◔ Widget · building the widget”.',
    render: (ctx) => releasePhone(ctx, { zoom: 'conversation', state: 'approved', approved: true }),
  });
  F(RC + '~s5', {
    device: 'iphone', title: 'iPhone · Release 1.3 conversation: S5 typed', note: 'The same draft at the conversation zoom.',
    chrome: { keyboard: true },
    render: (ctx) => releasePhone(ctx, { zoom: 'conversation', state: 's5', kb: true }),
  });
  F(RC + '~sending', {
    device: 'iphone', title: 'iPhone · Release 1.3 conversation: Sending', note: 'Sending → Processing → Delivered to Release lead.',
    chrome: { keyboard: true }, auto: { go: RC + '~processing', after: 350, transition: 'none' },
    render: (ctx) => releasePhone(ctx, { zoom: 'conversation', state: 'sending', kb: true }),
  });
  F(RC + '~processing', {
    device: 'iphone', title: 'iPhone · Release 1.3 conversation: Processing', note: 'The status line is replaced in place.',
    chrome: { keyboard: true }, auto: { go: RC + '~sent', after: 1200, transition: 'none' },
    render: (ctx) => releasePhone(ctx, { zoom: 'conversation', state: 'processing', kb: true }),
  });
  F(RC + '~sent', {
    device: 'iphone', title: 'iPhone · Release 1.3 conversation: S5 delivered and queued',
    note: 'Release lead’s reply, the relay queued with Send Now, the scope line with Undo.',
    chrome: { keyboard: true }, auto: { go: RC + '~taken', after: 6000, transition: 'none' },
    render: (ctx) => releasePhone(ctx, { zoom: 'conversation', state: 'sent', kb: true }),
  });
  F(RC + '~taken', {
    device: 'iphone', title: 'iPhone · Release 1.3 conversation: taken', note: '“↗ Delivered to Release notes”, then the live run line.',
    chrome: { keyboard: true },
    render: (ctx) => releasePhone(ctx, { zoom: 'conversation', state: 'taken', kb: true }),
  });
  F(RC + '~send-now', {
    device: 'iphone', title: 'iPhone · Release 1.3 conversation: Send Now', note: '“↗ Sent now to Release notes” (never “interrupted”).',
    chrome: { keyboard: true },
    render: (ctx) => releasePhone(ctx, { zoom: 'conversation', state: 'send-now', kb: true }),
  });

  /** V3-06: the Widget run, pushed (the V1-06 layout): the run’s own chat, its plan card at the foot (the home of a2). */
  function widgetSession(ctx, o) {
    const anim = takeAnim(ctx);
    const D = ctx.data;
    const t = D.todo('t02');
    const s = t.session;
    let from = o.approved ? RP + '~approved' : RP + '~run-card';
    if (ctx.live) {
      const f = Kit.store.get('v3-session-from', null);
      if (f) from = /^iphone\/list/.test(f) ? (o.approved ? L + '~approved' : L) : o.approved ? RP + '~approved' : f;
    }
    const row2 = o.approved
      ? `<div class="k-ph-r2"><span class="k-ph-t"><span class="k-ph-q">Widget</span><span class="k-sep">·</span>${ui.spinner(12)}<span class="v3-ph-work">Working</span><span class="k-sep">·</span><span>building the widget</span></span></div>`
      : `<div class="k-ph-r2"><span class="k-ph-t"><span class="k-ph-q">Widget</span><span class="k-sep">·</span>${ui.lineMark('needs')}<span class="k-ph-w">Needs you</span><span class="k-sep">·</span><span>plan below ↓</span></span></div>`;
    const top = ui.pinnedHeader({ title: t.title, back: go(from, 'pop'), more: opens('Open Full Session (terminal, files, changes) · Try Another Way · Stop'), row2 });
    // The plan at its home (P1): Edit · Don't Start, Approve, then the link row. Don't Start is this ask's Don't handle,
    // so the link row keeps Later (every ask has its three outcomes, §2.7).
    const a2 = D.ask('a2');
    const card = o.approved
      ? ui.askCard({ answered: 'Approved', undo: go(RS) })
      : ui.askCard({
        title: a2.title, plan: a2.plan, estimate: a2.estimate,
        secondary: a2.secondary.map((x) => Object.assign({ label: x }, x === 'Edit' ? opens('the plan, editable') : opens('“Widget won’t start. You can undo this.”'))),
        primary: Object.assign({ label: a2.primary }, go(RS + '~approved')),
        links: a2.exits.filter((x) => x !== 'Don’t handle').map((x) => Object.assign({ label: x }, opens('Plan awaiting approval · later today'))),
      });
    const body = `<div class="v3-sess">
      <div class="v3-sess-h">${esc(s.header)}</div>
      <div class="v3-sess-t">${esc(s.text)}</div>
      ${ui.fold(s.fold)}
      <div class="v3-sess-card">${card}</div>
    </div>`;
    const comp = ui.todoComposer({ to: { label: 'Widget', run: true, menu: true }, placeholder: 'Message Widget…' });
    return ui.page({ top, body: `<div class="v3-bin">${body}</div>`, bottom: `<div class="v3-tap"${navA(opens('the keyboard, To Widget'))}>${comp}</div>`, pad: false, cls: 'v3-todo' + anim });
  }
  F(RS, {
    device: 'iphone',
    title: 'iPhone · Widget’s session (pushed from the run card)',
    note: 'V3-06, the V1-06 layout. The pinned header names the to-do and points down to the home (“plan below ↓”); the plan card sits at the foot; the chip To Widget ▾ is purple and sends directly. ‹ lands where it started.',
    render: (ctx) => widgetSession(ctx, {}),
  });
  F(RS + '~approved', {
    device: 'iphone', title: 'iPhone · Widget’s session: Approved',
    note: '“✓ Approved · Undo”; the header reads “Widget · ◔ Working · building the widget”. ‹ → the details, updated.',
    render: (ctx) => widgetSession(ctx, { approved: true }),
  });

  /* ================================================================ iPhone · T07 (◎ opens it, which marks it seen) */

  const DP = 'iphone/dentist-paper';
  function dentistPhone(ctx, o) {
    const anim = takeAnim(ctx);
    const t = ctx.data.todo('t07');
    const done = !!o.done;
    const tt = done ? Object.assign({}, t, { phase: 'done', state: { word: 'Done', text: 'just now' } }) : t;
    const sp = spine(tt, { mode: 'expanded', kind: done ? 'done' : 'ready' });
    const body = `<div class="v3-paper">
      ${done ? '' : `<span class="k-ac-btn is-primary v3-primary"${navA(go(DP + '~done'))}><span>${t.primaryAction}</span></span>`}
      ${chipsOf(t, { add: !done })}
      ${ui.notes('', opens('the notes, edited in place'))}
      ${resultsOf(t)}
    </div>`;
    const nav = ui.navbar({
      leading: [{ icon: 'chevron-left', title: 'All To-dos', go: L, transition: 'pop' }],
      center: bodySwitch('paper', { nav: { conversation: opens('Book the dentist · its conversation') }, anim: {} }),
      trailing: [{ icon: 'ellipsis', title: 'More', toast: done ? 'Opens: Copy Link · Reopen · Delete…' : 'Opens: Copy Link · Move to Project… · Someday · Delete…' }],
    });
    const overlay = done ? ui.toast('Done', { action: 'Undo', actionNav: go(DP), bottom: 104 }) : '';
    // A closed to-do's composer asks; it never reopens the to-do (§2.5 #12).
    const comp = ui.composerFor(t, done ? { placeholder: 'Ask about this to-do…' } : {});
    return ui.page({ top: nav + sp, body: `<div class="v3-bin">${body}</div>`, bottom: `<div class="v3-tap"${navA(opens('the keyboard, To Persona'))}>${comp}</div>`, pad: false, overlay, cls: 'v3-todo' + anim });
  }
  F(DP, {
    device: 'iphone', title: 'iPhone · Book the dentist (◎ just finished)',
    note: 'Opened from ◎ on the list, which marks it seen. One primary action by state: Mark Done. No ask, so the spine has no ask line.',
    render: (ctx) => dentistPhone(ctx, {}),
  });
  F(DP + '~done', {
    device: 'iphone', title: 'iPhone · Book the dentist: Mark Done', note: 'The mark fills ●; “Done · Undo”. Closed, the composer reads “Ask about this to-do…” and the + chip goes.',
    render: (ctx) => dentistPhone(ctx, { done: true }),
  });

  /* ================================================================ iPhone · the walkthrough's other doors (§3.4) */

  // The Assistant's Needs you tray (S7), the lock screen (S2 from outside the app) and capture (S1), so the owner's
  // walkthrough runs inside V3. Their components are shared; the doors open V3's frames.
  const TRAY = 'iphone/assistant-tray';
  const LOCK = 'iphone/lock-push';
  const CAP = 'iphone/capture-passport';

  function trayPage(ctx, o) {
    o = o || {};
    const base = ctx.data;
    const D = o.answered ? base.view({ answered: { a1: 'hanami' } }) : base;
    const c = D.counts;
    const top = ui.navbar({
      leading: [{ icon: 'line-3-horizontal', title: 'Show Sidebar', toast: 'Opens: the sidebar' }],
      center: `<div class="v3-shell"><span class="is-on">Assistant</span><span${navA(opens('Agents'))}>Agents ${ui.badge(c.agentsBadge, { look: 'red' })}</span></div>`,
      trailing: [{ icon: 'square-pencil', title: 'New Chat', toast: 'Opens: New Chat' }],
    });
    const chat = `<div class="v3-chat">
      ${ui.msg.day('Today 9:58')}
      ${ui.msg.user('What needs me this morning?')}
      ${ui.msg.persona(`${base.counts.needs} to-dos need you, and ${base.counts.elsewhere} Agent elsewhere is asking for something. They are all in Needs you below.`, { plain: true })}
    </div>`;
    const on = { a2: go(RS, 'push'), a3: opens('Starts when Desk Mac is back · Undo'), t01: go(KP, 'push'), t02: go(RP, 'push'), t03: opens('Sort last year’s photos') };
    // The rows are drawn from the unchanged data while the answer shows in place; the numbers come from the new view.
    const rows = base.todos.filter((t) => t.section === 'needs').map((t) => {
      const ask = base.ask(t.asks[0]);
      if (t.id === 't01' && o.answered) return { title: t.title, ask: `${I('checkmark', 12)} You chose Ryokan Hanami<span class="k-sep">·</span><span class="v3-undo"${navA(go(TRAY))}>Undo</span>`, tone: 'positive', buttons: [] };
      const button = ask.kind === 'plan' ? Object.assign({ label: 'Review', door: true }, on.a2) : Object.assign({ label: t.answer.label, primary: true }, t.id === 't01' ? go(TRAY + '~answered') : on[ask.id]);
      return Object.assign({ title: t.title, ask: ask.kind === 'plan' ? `${base.run(ask.run).title}: ${ask.title}` : t.row.lead + (t.row.text && t.row.all ? ` · ${t.row.text}` : ''), tone: t.row.all ? t.row.tone : '', buttons: [button] }, on[t.id]);
    });
    const e = base.elsewhere;
    const tray = ui.needsTray({ header: `Needs you · ${c.assistantNeeds}`, groups: [
      { title: `To-dos · ${c.needs}`, rows },
      { title: `Elsewhere · ${c.elsewhere}`, rows: [{ title: e.title, ask: e.ask, buttons: e.actions.map((x) => Object.assign({ label: x.label, primary: !!x.primary }, opens(x.primary ? 'Allowed once · Tidy the settings code runs the tests' : 'Denied · Tidy the settings code carries on without the tests'))) }] },
    ] });
    const entries = `<div class="v3-entries"><span class="v3-entry is-on"><span>Needs you</span>${ui.badge(c.assistantNeeds, { look: 'needs' })}</span><span class="v3-entry"${navA(opens('In progress: Working and Waiting'))}><span>In progress</span><span class="v3-entry-n">${base.counts.inProgress}</span></span></div>`;
    const comp = ui.todoComposer({ to: false, placeholder: 'Ask Persona…', send: opens('Sends to Persona') });
    return ui.page({ top, body: chat, bottom: entries + comp, overlay: `<div class="v3-tray">${tray}</div>`, cls: 'v3-trayp' });
  }
  F(TRAY, {
    device: 'iphone', title: 'iPhone · the Assistant’s Needs you tray (S7)',
    note: `The main chat with Needs you open: “Needs you · ${counts().assistantNeeds}” = “To-dos · ${counts().needs}” + “Elsewhere · ${counts().elsewhere}”, the same ${counts().needs} as the sidebar, the bar and the header in V3-01. A to-do row answers in place, or opens its details (pushed).`,
    render: (ctx) => trayPage(ctx),
  });
  F(TRAY + '~answered', {
    device: 'iphone', title: 'iPhone · answered in the tray',
    note: 'OK: Ryokan Hanami answers in place: “✓ You chose Ryokan Hanami · Undo” for 4 s, while the header and the To-dos group already count one fewer, the same moment as on the list.',
    render: (ctx) => trayPage(ctx, { answered: true }),
  });

  /** The Fleet Live Activity (E5): it counts sessions that need the owner, shows at most two, and only the first
      actionable one carries buttons (a plan: View · Approve). */
  function fleetHtml(D) {
    const w = D.run('widget');
    const a = D.ask('a2');
    const e = D.elsewhere;
    return `<div class="k-fleet v3-fleet">
      <div class="k-fleet-h">${ui.face(22)}<b>Persona</b><span class="k-sep">·</span><span>${D.counts.fleet}</span></div>
      <div class="k-fleet-r v3-fleet-r"${navA(go(RS, 'fade'))}><div class="k-fleet-t"><div class="k-fleet-ti">${D.todo('t02').title}</div><div class="k-fleet-q">${w.title}: ${a.title}</div></div><div class="v3-fleet-bs"><span class="k-fleet-b"${navA(go(RS, 'fade'))}>View</span><span class="k-fleet-b is-rec"${navA(opens('Approved from the Live Activity · Widget starts building'))}>Approve</span></div></div>
      <div class="k-fleet-r"${navA(opens('Tidy the settings code in Agents'))}><div class="k-fleet-t"><div class="k-fleet-ti">${e.title}</div><div class="k-fleet-q">${e.ask}</div></div></div>
    </div>`;
  }
  function lockPage(ctx, o) {
    const D = ctx.data;
    const noti = o.answered ? '' : ui.notificationFor('a1', { go: KP, transition: 'fade', on: {
      hanami: go(LOCK + '~answered', 'fade'), kaede: opens('Kaede, answered from the lock screen'), tsuki: opens('Tsuki, answered from the lock screen'),
    } });
    return ui.lockScreen({ content: noti + fleetHtml(D) });
  }
  F(LOCK, {
    device: 'iphone', title: 'iPhone · the lock screen (S2 from outside the app)',
    note: 'The to-do’s own ask arrives as the TODO_DECIDE push, shown expanded: the title, the named ask and the recommendation, then three actions, recommended first (proposed: today it has one, Take Recommendation). The body opens the details at the card. Below, the Fleet Live Activity counts sessions that need the owner (E5): “2 need you”, the first actionable one with View · Approve.',
    render: (ctx) => lockPage(ctx, {}),
  });
  F(LOCK + '~answered', {
    device: 'iphone', title: 'iPhone · answered from the lock screen',
    note: 'Ryokan Hanami (Recommended) answers in place: the notification goes; the Live Activity is unchanged, since it counts sessions and the ryokan was the to-do’s own ask.',
    render: (ctx) => lockPage(ctx, { answered: true }),
  });

  /** The capture sheet over the list. Renew passport does not exist yet, so the bar's To do counts one fewer until Add
      files it (a state made in the prototype). An empty field disables both actions. */
  function capturePage(ctx, o) {
    const empty = !o.text;
    let sheet = ui.captureSheet(Object.assign({
      close: go(L, 'fade'), add: empty ? null : go(L + '~added', 'fade'),
      ask: empty ? null : opens('Renew passport, handed to Persona: “I can book the photo booth and fill the form. Want me to start?”'),
    }, o));
    sheet = sheet.replace('<div class="k-cs">', `<div class="k-cs v3-cs${empty ? ' is-empty' : ''}">`);
    return phoneList(ctx, { pill: false, alert: sheet, barCounts: { todo: ctx.data.counts.todo - 1 } });
  }
  F(CAP + '~empty', {
    device: 'iphone', chrome: { keyboard: true }, title: 'iPhone · New To-Do, just opened',
    note: 'The pill opens the capture sheet docked on the keyboard: ✕, one field with a mic, Add & Ask Persona and Add (both disabled until there is text). Behind it the bar counts To do 1: Renew passport does not exist yet. Tap the field to type.',
    render: (ctx) => ({ content: `<div class="v3-tap-all"${navA(go(CAP, 'none'))}>${capturePage(ctx, { text: '', placeholder: 'What do you want done?', chips: [] })}</div>`, keyboard: { suggestions: NEUTRAL_SUGG } }),
  });
  F(CAP, {
    device: 'iphone', chrome: { keyboard: true }, title: 'iPhone · capture “Renew passport fri” (S1)',
    note: 'Typing underlines “fri” and grows a dashed chip “Fri 20 Nov”, which turns solid on Return (tap the chip here). Add files it under To do.',
    render: (ctx) => {
      const c = ctx.data.todo('t11').capture;
      return { content: capturePage(ctx, { text: c.text, token: c.token, chips: [Object.assign({ label: c.parsed, dashed: true, clear: true }, go(CAP + '~parsed'))] }), keyboard: { suggestions: ['Friday', 'fri', 'from'] } };
    },
  });
  F(CAP + '~parsed', {
    device: 'iphone', chrome: { keyboard: true }, title: 'iPhone · capture after Return',
    note: 'Return turns the parsed chip solid (✕ removes it). + Project and + Remind stay optional. Add → All To-dos with “Added · Undo”.',
    render: (ctx) => {
      const c = ctx.data.todo('t11').capture;
      return { content: capturePage(ctx, { text: c.text.replace(/\s*fri$/, ''), chips: [Object.assign({ label: c.parsed, clear: true }, go(CAP))] }), keyboard: { suggestions: NEUTRAL_SUGG } };
    },
  });

  /* ================================================================ iPhone Duo · outer display (V3-07) */

  const DO = 'duo-outer';
  // E4: the page's bar items in the side column are 48-pt glass circles with symbols only; the name is the label.
  const sideTile = (icon, label, nav) => `<span class="dev-side-item" role="button" aria-label="${esc(label)}" title="${esc(label)}"${navA(nav)}>${I(icon, 21)}</span>`;
  const duoTabs = (ctx) => ({ selected: 0, agentsCount: ctx.data.counts.agentsBadge });
  // The shell rail's face and New Chat at the column's foot, under the Duo tab bar (E4), on both side-column postures.
  // (Drawn here as HTML so their taps can show a toast; the kit's side items take only data-go.)
  const railFoot = () => [
    `<span class="dev-side-rail" title="Persona" role="button" aria-label="Persona"${navA(opens('Persona, the main chat'))}>${ui.face(26)}</span>`,
    `<span class="dev-side-rail" title="New Chat" role="button" aria-label="New Chat"${navA(opens('New Chat'))}>${I('square-pencil', 21)}</span>`,
  ];
  // Three predictions; the "keyboard ≈ 300 pt (to measure)" estimate sits in the keyboard's foot (screens.css), off the
  // predictive bar and off the home indicator.
  const DUO_KB = (draft) => ({ suggestions: draft ? ['breakfast', 'break', 'breakfasts'] : NEUTRAL_SUGG, label: '' });

  function duoOuterList(ctx) {
    const D = ctx.data;
    const anim = takeAnim(ctx);
    const head = `<div class="v3-duo-head">${ui.titleMenu({ title: 'All To-dos', toast: 'Opens: the places menu (All To-dos ✓ · Today · Upcoming · projects)' })}</div>${barHtml(D, 'all', (id) => opens(id === 'all' ? 'All' : `the ${id === 'needs' ? 'Needs you' : id === 'progress' ? 'In progress' : 'To do'} filter`))}`;
    const list = sectionsHtml(D, {
      rowOpts: (t) => {
        const o = { toast: `Opens: ${t.title} (cross-fade)`, markNav: lmKind(t) === 'todo' ? { toast: 'Done · Undo' } : opens(t.title), crew: false };
        if (t.id === 't01') Object.assign(o, { go: DO + '/kyoto-paper', transition: 'fade', toast: undefined, markNav: go(DO + '/kyoto-paper', 'fade') });
        // One row looks the same on every device: plans show one door; Options only where the ask has options.
        if (t.answer) o.answer = { toast: t.id === 't01' ? 'Opens: ✓ You chose Ryokan Hanami · Undo (as on iPhone)' : `Opens: ${t.title} · ${t.answer.label}`, options: t.answer.options ? opens('the full ask card under the row') : undefined };
        return o;
      },
      foldNav: { can_wait: opens('Can wait, unfolded'), someday: opens('Someday') },
    });
    return {
      content: ui.page({ top: `<div class="v3-duo-top">${head}</div>`, body: `<div class="v3-list">${list}</div>`, overlay: ui.pill({ label: 'New To-Do', toast: 'Opens: New To-Do', bottom: 24 }), cls: 'v3-listpage has-pill' + anim }),
      side: { actions: [sideTile('line-3-horizontal', 'Show Sidebar', opens('the sidebar over the page')), sideTile('magnifying-glass', 'Search', opens('Search To-dos')), sideTile('ellipsis', 'More', opens('Sort By · Show Someday · Show Done Today'))], tabs: duoTabs(ctx), foot: railFoot() },
    };
  }

  /** T01 on the Duo outer display: 382 wide, Back and More in the side column; the switch heads the content. */
  function kyotoDuoOuter(ctx, o) {
    const anim = takeAnim(ctx);
    const D = ctx.data;
    const t = D.todo('t01');
    const paper = o.zoom === 'paper';
    const condensed = !paper || o.kb;
    const ids = { paper: o.kb ? DO + '/kyoto-conversation-keyboard~paper' : DO + '/kyoto-paper', conversation: o.kb ? DO + '/kyoto-conversation-keyboard' : DO + '/kyoto-conversation' };
    const ask = condensed ? askLine(D.ask('a1').title, { ok: Object.assign({ label: t.answer.short }, opens('✓ You chose Ryokan Hanami · Undo (as on iPhone)')), options: opens('the full ask card over the thread') }) : '';
    const sw = bodySwitch(o.zoom, { nav: { paper: go(ids.paper, 'fade'), conversation: go(ids.conversation, 'fade') }, dot: paper && !o.kb, anim: { paper: 'body-fade', conversation: 'body-fade' } });
    const sp = spine(t, { mode: condensed ? 'condensed' : 'expanded', short: true, stateRow: condensed && !o.kb, ask });
    let body;
    const lastText = t.rounds[t.rounds.length - 1].text;
    if (paper && o.kb) {
      body = paperFoot(t, latest({ text: lastText, nav: go(ids.conversation, 'fade'), anim: 'body-fade' }));
    } else if (paper) {
      body = `<div class="v3-paper">${askHome(D, { on: askOn() })}${chipsOf(t)}${ui.notes(t.notes, opens('the notes'))}${stepsSum(t)}${resultsOf(t, { short: true })}${latest({ text: lastText, nav: go(ids.conversation, 'fade'), anim: 'body-fade' })}</div>`;
    } else {
      body = ui.thread(t.rounds, { anchor: 'bottom', askedSuffix: ASKED_ABOVE, bgNav: opens('the Background round') });
    }
    const draft = o.kb ? t.drafts.duo : '';
    let comp = ui.todoComposer({ to: 'Persona', text: draft, inset: 8, send: draft ? opens('Sending → Processing → Delivered to Persona') : null });
    if (!o.kb) comp = `<div class="v3-tap"${navA(go(DO + '/kyoto-conversation-keyboard', 'fade'))}>${comp}</div>`;
    const content = ui.page({ top: `<div class="v3-duo-bar">${sw}</div>` + sp, body: `<div class="v3-bin">${body}</div>`, bottom: comp, pad: false, cls: 'v3-todo v3-duo-outer' + anim });
    const out = {
      content,
      side: { back: { go: DO + '/list', transition: 'fade', label: 'All To-dos' }, actions: [sideTile('ellipsis', 'More', opens('Copy Link · Chat Topic… · Decisions'))], tabs: duoTabs(ctx), foot: railFoot() },
    };
    if (o.kb) out.keyboard = DUO_KB(draft);
    return out;
  }

  F(DO + '/kyoto-conversation-keyboard', {
    device: 'duo-outer', important: true,
    title: 'Duo outer · Kyoto conversation, keyboard up',
    note: 'V3-07. Content 382, the side column on the right: Back alone at its top and More 12 below (48-pt glass circles, symbols only, E4), the Duo tab bar and the face at its foot. The switch heads the content and never moves; the spine keeps title, state and the ask. Details ⇄ Conversation cross-fades the body only. Unfold keeps the draft. (Keyboard ≈ 300 pt, to measure.)',
    unfold: 'duo-inner-portrait/list-conversation~draft',
    render: (ctx) => kyotoDuoOuter(ctx, { zoom: 'conversation', kb: true }),
  });
  F(DO + '/kyoto-conversation-keyboard~paper', {
    device: 'duo-outer', title: 'Duo outer · Kyoto details, keyboard up',
    note: 'The Details zoom while typing: the body cross-fades to the details’ foot; the switch, the spine, the draft and the keyboard stay.',
    unfold: 'duo-inner-portrait/list-paper~draft',
    render: (ctx) => kyotoDuoOuter(ctx, { zoom: 'paper', kb: true }),
  });
  F(DO + '/kyoto-paper', {
    device: 'duo-outer', title: 'Duo outer · Kyoto: spine + Details',
    note: 'The details at 382 under the expanded spine (its state on one line), at rest: the card at its home, the chips, the notes and Steps; Results and the Latest line are one scroll below, where the spine condenses and carries the ask (see ~paper on the keyboard frame). Back in the side column pops to the list (cross-fade).',
    unfold: 'duo-inner-portrait/list-paper',
    render: (ctx) => kyotoDuoOuter(ctx, { zoom: 'paper' }),
  });
  F(DO + '/kyoto-conversation', {
    device: 'duo-outer', title: 'Duo outer · Kyoto: spine + Conversation',
    note: 'The conversation zoom with the keyboard down; the composer focuses into V3-07.',
    unfold: 'duo-inner-portrait/list-conversation',
    render: (ctx) => kyotoDuoOuter(ctx, { zoom: 'conversation' }),
  });
  F(DO + '/list', {
    device: 'duo-outer', title: 'Duo outer · All To-dos',
    note: 'Back’s target. The page’s items (Show Sidebar, Search, More) stand in the side column as symbols; T01 opens its details by cross-fade. T02 shows one door (Review Plan ›), as on iPhone.',
    unfold: 'duo-inner-portrait/list-paper',
    render: (ctx) => duoOuterList(ctx),
  });

  /* ================================================================ iPhone Duo · inner display, upright (V3-08, V3-09) */

  const DI = 'duo-inner-portrait';
  const POINTER = ['Which ryokan?', 'answer beside →'];
  /** Rows in a list column beside the to-do region: the selected row's ask shrinks to a pointer while the region shows
      its home or the spine's answerable line (§2.7, drawn once per screen). */
  function wideRowOpts(t, sel, where) {
    // Crew capsules stay on the iPhone list, where they fit whole; in a list column line 3 carries the steps instead.
    const o = { toast: `Opens: ${t.title} in the to-do region (selection only, not animated)`, markNav: lmKind(t) === 'todo' ? { toast: 'Done · Undo' } : opens(`${t.title} in the to-do region`), crew: false };
    if (t.id === sel) { o.selected = true; delete o.toast; o.markNav = opens('the ask in the to-do region'); o.pointer = POINTER; return o; }
    if (t.answer) o.answer = { toast: `Opens: ${t.title} · ${t.answer.label} (${where})`, options: t.answer.options ? opens('the full ask card under the row') : undefined };
    return o;
  }
  /** A list column: the bar, the sections, T01 selected. o: { leading, trailing, align, wc, where, sel, rowGo, bg, compact } */
  function listColumn(ctx, o) {
    const D = ctx.data;
    const top = ui.navbar({
      leading: o.leading ? [o.leading] : [],
      center: ui.titleMenu({ title: 'All To-dos', toast: 'Opens: the places menu' }),
      align: o.align, wc: o.wc,
      trailing: o.trailing || [{ icon: 'plus', title: 'New To-Do', toast: 'Opens: New To-Do' }, { icon: 'ellipsis', title: 'More', toast: 'Opens: Sort By · Show Someday · Show Done Today' }],
      below: barHtml(D, 'all', (id) => opens(id === 'all' ? 'All' : `the ${id === 'needs' ? 'Needs you' : id === 'progress' ? 'In progress' : 'To do'} filter`)),
    });
    const list = sectionsHtml(D, {
      rowOpts: (t) => Object.assign(wideRowOpts(t, o.sel === undefined ? 't01' : o.sel, o.where || 'in place'), (o.rowGo || {})[t.id]),
      foldNav: { can_wait: opens('Can wait, unfolded'), someday: opens('Someday') },
    });
    return ui.page({ top, body: `<div class="v3-list">${list}</div>`, bg: o.bg, cls: 'v3-listpage v3-col' });
  }

  /**
   * The to-do region beside a list (Duo half, iPad, web, Android). o: { zoom, kb, draft, sw (nav), swAnim, leading,
   * trailing, align ('leading': the switch leads the bar), compact (switch), narrow (Options folds to its chevron),
   * short, readable, r4, send, compNav, wc, anim }
   */
  function todoRegion(ctx, o) {
    const D = ctx.data;
    const t = D.todo('t01');
    const paper = o.zoom === 'paper';
    const condensed = !paper || o.kb;
    const ask = !condensed ? '' : askLine(D.ask('a1').title, { ok: Object.assign({ label: t.answer.short }, opens('✓ You chose Ryokan Hanami · Undo')), options: opens('the full ask card over the thread'), mini: o.narrow });
    const swAnim = o.swAnim || { conversation: 'body-left', paper: 'body-right' };
    const sp = spine(t, { mode: condensed ? 'condensed' : 'expanded', short: !!o.short, stateRow: condensed && !o.kb, ask, readable: o.readable });
    let body;
    const lastText = t.rounds[t.rounds.length - 1].text;
    const lx = latest({ text: lastText, nav: o.sw.conversation, anim: swAnim.conversation });
    if (paper && o.kb) {
      body = paperFoot(t, lx, { wide: !o.narrow });
    } else if (paper) {
      body = `<div class="v3-paper${o.readable ? ' v3-readable' : ''}">${askHome(D, { narrow: o.narrow, on: askOn() })}${chipsOf(t)}${ui.notes(t.notes, opens('the notes'))}${stepsSum(t)}${resultsOf(t, { short: o.narrow })}${lx}</div>`;
    } else {
      const rounds = t.rounds.slice();
      const per = {};
      if (o.r4) { rounds.push(t.extraRounds.r4); per.r4 = { status: 'Delivered to Persona', hide: o.r4 === 'replied' ? [] : ['reply'] }; }
      body = ui.thread(rounds, { anchor: 'bottom', askedSuffix: ASKED_ABOVE, per, bgNav: opens('the Background round') });
      if (o.readable) body = `<div class="v3-thread-wrap">${body}</div>`;
    }
    const draft = o.draft || '';
    let comp = ui.todoComposer({ to: 'Persona', text: draft, send: draft ? (o.send || opens('Sending → Processing → Delivered to Persona')) : null });
    if (!draft && o.compNav) comp = `<div class="v3-tap"${navA(o.compNav)}>${comp}</div>`;
    if (o.readable) comp = `<div class="v3-readable-c">${comp}</div>`;
    const sw = o.noSwitch ? '' : bodySwitch(o.zoom, { nav: o.sw, dot: paper && !o.kb, anim: swAnim, compact: o.compact });
    const top = regionBar({ leading: o.leading, center: sw, trailing: o.trailing, align: o.align, wc: o.wc });
    return ui.page({ top: top + sp, body: `<div class="v3-bin">${body}</div>`, bottom: comp, pad: false, cls: 'v3-todo' + (o.anim || '') });
  }

  const focusBtn = (nav, small) => Object.assign({ icon: 'v3-focus', label: small ? undefined : 'Focus', title: 'Focus' }, nav);
  function duoInner(ctx, o) {
    const anim = takeAnim(ctx);
    const list = listColumn(ctx, { leading: { icon: 'line-3-horizontal', title: 'Show Sidebar', toast: 'Opens: the sidebar over the left half' }, where: 'in the left half' });
    const ids = { paper: o.kb ? DI + '/list-paper~draft' : DI + '/list-paper', conversation: o.kb ? DI + '/list-conversation~draft' : DI + '/list-conversation' };
    const right = todoRegion(ctx, {
      zoom: o.zoom, kb: o.kb, draft: o.kb ? ctx.data.todo('t01').drafts.duo : '', narrow: true, short: true, compact: true, align: 'leading',
      r4: o.zoom === 'conversation' && !o.kb ? 'replied' : null,
      sw: { paper: go(ids.paper), conversation: go(ids.conversation) },
      send: go(DI + '/list-conversation'),
      compNav: go(o.zoom === 'paper' ? DI + '/list-paper~draft' : DI + '/list-conversation~draft'),
      trailing: [focusBtn(go(DI + '/list-conversation~focus', 'fade'), true), { icon: 'ellipsis', title: 'More', toast: 'Opens: Copy Link · Move to Project… · Someday · Delete…' }],
      anim,
    });
    const out = { content: ui.split([{ w: 334, html: list }, { flex: 1, html: right, line: true }]) };
    if (o.kb) out.keyboard = DUO_KB(true);
    return out;
  }

  /** Focus: one spine across the to-do (its text on the details' readable column), Details │ Conversation under it.
      o: { dev, convW, paperW, listW, listOver, rail (web), web, duo } */
  function focusFrame(ctx, o) {
    const anim = takeAnim(ctx);
    const D = ctx.data;
    const t = D.todo('t01');
    const P = `${o.dev}/`;
    const inset = o.web ? 24 : Math.max(16, Math.round(((o.paperW || 0) - 712) / 2) + 16);
    const more = { icon: 'ellipsis', title: 'More', toast: 'Opens: Copy Link · Move to Project… · Someday · Decisions · Delete…' };
    const exit = o.duo ? go(DI + '/list-conversation', 'fade') : go(P + 'conversation', 'pane-close');
    const tool = o.rail ? '' : ui.navbar({
      leading: [{ icon: 'chevron-left', label: 'List', title: 'Show List', go: o.duo ? undefined : P + 'focus-split~list-over', transition: 'none', toast: o.duo ? 'Opens: the list over the left half' : undefined }],
      trailing: [Object.assign({ icon: 'v3-exit-focus', label: 'Exit Focus' }, exit), more], cls: 'v3-ftool',
    });
    const id = `<div class="v3-fid" style="padding-left:${inset}px">
      <div class="v3-fid-in">
        <div class="v3-sp-t">${livingMark('needs', { size: 24, title: t.title, nav: opens('the ask card, in view') })}<span class="v3-sp-tt">${t.title}</span></div>
        <div class="v3-sp-in v3-sp-s">${ui.stateLine(t.phase, t.state.text, { word: t.state.word })}</div>
      </div>
      ${o.rail ? ui.btn(more) : ''}
    </div>`;
    const paper = `<div class="v3-paper v3-readable${o.duo ? '' : ' v3-steps-2'}">${askHome(D, { narrow: o.duo, on: askOn() })}${chipsOf(t)}${ui.notes(t.notes, opens('the notes'))}${o.duo ? stepsSum(t) : stepsOf(t)}${resultsOf(t, { short: o.duo })}</div>`;
    const rounds = t.rounds.concat([t.extraRounds.r4]);
    const conv = ui.page({
      top: `<div class="v3-panehead">${ui.paneHeader({ channel: 'Conversation', more: opens('Copy Link · Chat Topic… · Decisions'), close: false })}</div>`,
      body: ui.thread(rounds, { anchor: 'bottom', askedSuffix: ASKED_BESIDE, bgNav: opens('the Background round') }),
      bottom: ui.todoComposer({ to: 'Persona' }), pad: false, cls: 'v3-conv-col' + (o.duo ? ' is-duo' : ''),
    });
    if (o.duo) {
      // The Duo inner display: the spine lives in the left half only, so nothing straddles the crease.
      const left = `<div class="k-page v3-focus${anim}"><div class="v3-fspine">${tool}${id}</div><div class="v3-fbody"><div class="v3-bin">${paper}</div></div></div>`;
      return ui.split([{ w: 334, html: left }, { flex: 1, html: conv, line: true }]);
    }
    const split = ui.split([{ flex: 1, html: `<div class="v3-bin">${paper}</div>` }, { w: o.convW, html: conv, line: true }]);
    let overlay = '';
    if (o.listOver) {
      const lc = listColumn(ctx, { align: 'leading', where: 'swaps the details and slides the list away', trailing: [{ icon: 'plus', title: 'New To-Do', toast: 'Opens: New To-Do' }, { icon: 'ellipsis', title: 'More', toast: 'Opens: Sort By · Show Someday · Show Done Today' }] });
      overlay = `<div class="k-scrim v3-scrim-soft"${navA(go(P + 'focus-split'))}></div><div class="v3-listover" style="width:${o.listW}px">${lc}</div>`;
    }
    return `<div class="k-page v3-focus${anim}">
      <div class="v3-fspine${o.web ? ' is-web' : ''}">${tool}${id}</div>
      <div class="v3-fbody">${split}</div>
      ${overlay}
    </div>`;
  }

  F(DI + '/list-paper', {
    device: 'duo-inner-portrait',
    title: 'Duo open · list ┊ spine + Details',
    note: 'V3-08. List 334 ┊ to-do 335 at the crease. T01 selected: its row shrinks the ask to “Which ryokan? · answer beside →”, since the card is in view beside it (drawn once). The right half’s bar leads with the switch; ⤢ and ⋯ trail. The primary shortens to “Ryokan Hanami” at this width. Fold → the outer display’s details.',
    fold: DO + '/kyoto-paper',
    render: (ctx) => duoInner(ctx, { zoom: 'paper' }),
  });
  F(DI + '/list-paper~draft', {
    device: 'duo-inner-portrait', title: 'Duo open · Details with the draft (unfolded mid-draft)',
    note: 'Unfolded from the outer display’s details while typing: the draft “also check breakf” and its caret are intact; the keyboard rises under both halves.',
    fold: DO + '/kyoto-conversation-keyboard~paper',
    render: (ctx) => duoInner(ctx, { zoom: 'paper', kb: true }),
  });
  F(DI + '/list-conversation', {
    device: 'duo-inner-portrait', important: true,
    title: 'Duo open · the list stays ┊ spine + Conversation',
    note: 'V3-09. The list never leaves the left half, and its Needs you (3) leads the bar. In the right half the switch slides the body within the half (the bars are horizontal here, so a slide is allowed) and stays clear of the crease. ⤢ Focus gives Details ┊ Conversation. Fold → V3-07.',
    fold: DO + '/kyoto-conversation-keyboard',
    render: (ctx) => duoInner(ctx, { zoom: 'conversation' }),
  });
  F(DI + '/list-conversation~draft', {
    device: 'duo-inner-portrait', title: 'Duo open · Conversation with the draft (S9: unfolded)',
    note: 'Unfolded from V3-07 mid-draft: selection, zoom, the draft “also check breakf” and its caret survive. ↑ sends.',
    fold: DO + '/kyoto-conversation-keyboard',
    render: (ctx) => duoInner(ctx, { zoom: 'conversation', kb: true }),
  });
  F(DI + '/list-conversation~focus', {
    device: 'duo-inner-portrait', title: 'Duo open · Focus: Details ┊ Conversation',
    note: 'The owner chose ⤢: the spine stays in the left half only, so nothing straddles the crease; the conversation takes the right half with the one composer. The thread says “Asked: Which ryokan? · answer beside”: the card is the home.',
    fold: DO + '/kyoto-conversation-keyboard',
    render: (ctx) => focusFrame(ctx, { duo: true, dev: DI }),
  });

  /* ================================================================ iPhone Duo · inner display on its side (§3.3 rule 3) */

  const DL = 'duo-inner-landscape';
  function duoLandscape(ctx, o) {
    const anim = takeAnim(ctx);
    const list = listColumn(ctx, { trailing: [], where: 'in the to-do region' });
    const right = todoRegion(ctx, {
      zoom: o.zoom, r4: o.zoom === 'conversation' ? 'replied' : null,
      sw: { paper: go(DL + '/list-paper', 'fade'), conversation: go(DL + '/list-conversation', 'fade') },
      swAnim: { paper: 'body-fade', conversation: 'body-fade' },
      compNav: opens('the keyboard'), anim,
    });
    return {
      content: ui.split([{ w: 320, html: list }, { flex: 1, html: right, line: true }]),
      side: { actions: [sideTile('line-3-horizontal', 'Show Sidebar', opens('the sidebar over the list')), sideTile('plus', 'New To-Do', opens('New To-Do')), sideTile('v3-focus', 'Focus', opens('Focus: Details │ Conversation 400')), sideTile('ellipsis', 'More', opens('Copy Link · Move to Project… · Someday · Delete…'))], tabs: duoTabs(ctx), foot: railFoot() },
    };
  }
  F(DL + '/list-conversation', {
    device: DL,
    title: 'Duo on its side · list 320 │ spine + Conversation 547',
    note: 'Rule 3: under the side bars the body cross-fades. The page’s items stand in the side column as 48-pt symbols (Show Sidebar, New To-Do, Focus, More; E4); the switch heads the to-do region and never moves. Fold → the outer display.',
    fold: DO + '/kyoto-conversation',
    render: (ctx) => duoLandscape(ctx, { zoom: 'conversation' }),
  });
  F(DL + '/list-paper', {
    device: DL,
    title: 'Duo on its side · list 320 │ spine + Details 547',
    note: 'The Details zoom (cross-fade), at rest: the card, chips, notes, Steps and Results; Latest is one scroll below on this 669-pt-tall display. The selected row points to the card beside it.',
    fold: DO + '/kyoto-paper',
    render: (ctx) => duoLandscape(ctx, { zoom: 'paper' }),
  });

  /* ================================================================ iPad (V3-10 … V3-13) */

  function ipadFrame(ctx, o) {
    const anim = takeAnim(ctx);
    const dev = o.dev;
    const P = `${dev}/`;
    const sb = tree(ctx.data, { todos: opens('All To-dos (already in front)') });
    const list = listColumn(ctx, { align: 'leading', where: 'in place', trailing: [{ icon: 'plus', title: 'New To-Do', toast: 'Opens: New To-Do' }, { icon: 'ellipsis', title: 'More', toast: 'Opens: Sort By · Show Someday · Show Done Today' }] });
    const region = todoRegion(ctx, {
      zoom: o.zoom, short: false, align: 'leading',
      sw: { paper: go(P + 'paper'), conversation: go(P + 'conversation') },
      compNav: opens('the keyboard; the layout never changes'),
      trailing: [focusBtn(go(P + 'focus-split', 'pane-open')), { icon: 'ellipsis', title: 'More', toast: 'Opens: Copy Link · Move to Project… · Someday · Decisions · Delete…' }],
      readable: o.readable, anim,
    });
    return ui.split([{ w: 400, html: sb }, { w: o.listW, html: list, line: true }, { flex: 1, html: region, line: true }]);
  }

  const IP = 'ipad11-landscape';
  F(IP + '/paper', {
    device: IP,
    title: 'iPad 11″ · sidebar │ list │ spine + Details',
    note: 'V3-10. Sidebar 400 │ list 320 │ to-do 474; the switch leads the to-do’s bar (on Details), ⤢ Focus and ⋯ trail. The selected row points to the card beside it. The left two regions never move.',
    render: (ctx) => ipadFrame(ctx, { dev: IP, zoom: 'paper', listW: 320 }),
  });
  F(IP + '/conversation', {
    device: IP, important: true,
    title: 'iPad 11″ · nothing on the left moved',
    note: 'V3-11. Sidebar 400 │ list 320 │ to-do 474: spine + Conversation. Only the to-do’s body changed zoom (a slide inside the region) under a switch that stayed put; the sidebar, the list and its bar (Needs you 3 first) stayed. ⤢ Focus is the owner’s choice, never automatic.',
    render: (ctx) => ipadFrame(ctx, { dev: IP, zoom: 'conversation', listW: 320 }),
  });
  F(IP + '/focus-split', {
    device: IP, important: true,
    title: 'iPad 11″ · Focus: one spine, Details 794 │ Conversation 400',
    note: 'V3-12. The owner chose Focus: the sidebar and list left in one 0.3 s transition. The bar holds ‹ List, Exit Focus and ⋯; the spine spans 1194 with its text on the details’ readable column; the body splits; the switch is hidden. The card in the details is the home, so the thread says “· answer beside”. One composer, at the conversation’s foot. Focus is kept for this place until exited.',
    render: (ctx) => focusFrame(ctx, { dev: IP, convW: 400, paperW: 794, listW: 320 }),
  });
  F(IP + '/focus-split~list-over', {
    device: IP, title: 'iPad 11″ · Focus: ‹ List laid over',
    note: '‹ List lays the list over everything, dimmed 0.2; a pick swaps the details and slides the list away. Focus stays.',
    render: (ctx) => focusFrame(ctx, { dev: IP, convW: 400, paperW: 794, listW: 320, listOver: true }),
  });
  const IP13 = 'ipad13-landscape';
  F(IP13 + '/conversation', {
    device: IP13,
    title: 'iPad 13″ · sidebar │ list 340 │ spine + Conversation 636',
    note: 'V3-13. Normally sidebar 400 │ list 340 │ to-do 636 with the switch; the spine sits on the readable column.',
    render: (ctx) => ipadFrame(ctx, { dev: IP13, zoom: 'conversation', listW: 340, readable: true }),
  });
  F(IP13 + '/paper', {
    device: IP13, title: 'iPad 13″ · spine + Details 636', note: 'The same region at the Details zoom (content ≤ 680).',
    render: (ctx) => ipadFrame(ctx, { dev: IP13, zoom: 'paper', listW: 340, readable: true }),
  });
  F(IP13 + '/focus-split', {
    device: IP13, title: 'iPad 13″ · Focus: Details 976 │ Conversation 400',
    note: 'V3-13. Focus gives details 976 (content ≤ 680, centred, the spine on the same column) │ conversation 400 under one spine.',
    render: (ctx) => focusFrame(ctx, { dev: IP13, convW: 400, paperW: 976, listW: 340 }),
  });
  F(IP13 + '/focus-split~list-over', {
    device: IP13, title: 'iPad 13″ · Focus: ‹ List laid over', note: 'The list 340 over everything, dimmed 0.2.',
    render: (ctx) => focusFrame(ctx, { dev: IP13, convW: 400, paperW: 976, listW: 340, listOver: true }),
  });

  /* ================================================================ Web 1440 (V3-14) */

  const W = 'web';
  function rail(ctx) {
    const c = ctx.data.counts;
    const it = (icon, label, o) => `<span class="v3-rail-i${o && o.on ? ' is-on' : ''}" title="${label}"${navA(o && o.nav)}>${I(icon, 18)}${o && o.badge ? `<span class="v3-rail-b${o.neutral ? ' is-neutral' : ''}">${o.badge}</span>` : ''}</span>`;
    return `<div class="v3-rail">
      <span class="v3-rail-face"${navA(opens('Persona, the main chat'))}>${ui.face(28)}</span>
      ${it('sidebar-left', 'Show Sidebar (Mod+Alt+B)', { nav: go(W + '/rail-split~sidebar', 'pane-close') })}
      <span class="v3-rail-gap"></span>
      ${it('tray-full', 'Inbox', { nav: opens('Inbox'), badge: c.inbox, neutral: true })}
      ${it('checklist', 'To-dos', { on: true, badge: c.needs })}
      ${it('agents', 'Agents', { nav: opens('Agents'), badge: c.agentsBadge })}
      ${it('magnifying-glass', 'Search', { nav: opens('Search To-dos (⌘K)') })}
      <span class="k-grow"></span>
      ${it('gear', 'Settings', { nav: opens('Settings') })}
    </div>`;
  }
  function webSidebar(ctx) {
    const html = tree(ctx.data, { todos: opens('All To-dos (already in front)'), tree: { header: false } });
    const head = `<div class="k-tree-head">${ui.btn({ icon: 'sidebar-left', size: 's', title: 'Collapse to the Rail (Mod+Alt+B)', go: W + '/rail-split', transition: 'pane-open' })}<span class="k-tree-name">Persona</span>${ui.btn({ icon: 'square-pencil', size: 's', title: 'New Chat', toast: 'Opens: New Chat' })}</div>`;
    return html.replace('<div class="k-sb-scroll">', head + '<div class="k-sb-scroll">');
  }
  function webList(ctx) {
    return listColumn(ctx, { align: 'leading', where: 'in place', trailing: [{ icon: 'plus', label: 'New To-Do', look: 'fill', size: 's', toast: 'Opens: New To-Do' }, { icon: 'ellipsis', title: 'More', toast: 'Opens: Sort By · Show Someday · Show Done Today' }] });
  }
  F(W + '/rail-split', {
    device: W,
    title: 'Web 1440 · rail │ list │ one spine over Details │ Conversation',
    note: 'V3-14. The owner collapsed the sidebar to the rail (Mod+Alt+B): rail 56 │ list 360 │ details 584 │ conversation 440 with the spine across both, its text on the details’ edge. The rail keeps its counts: Inbox 2, To-dos (3), Agents 2. At ≥ 1600 it splits without the rail. The rail’s first button brings the sidebar back.',
    render: (ctx) => {
      const region = focusFrame(ctx, { dev: W, convW: 440, paperW: 584, rail: true, web: true });
      return ui.split([{ w: 56, html: rail(ctx) }, { w: 360, html: webList(ctx), line: true }, { flex: 1, html: region, line: true }]);
    },
  });
  F(W + '/rail-split~sidebar', {
    device: W,
    title: 'Web 1440 · sidebar 284 │ list 360 │ to-do 796 with the switch',
    note: 'Before the rail: the to-do region is 796 (content ≤ 680, the spine on the same column) and its body switches Details ⇄ Conversation from the bar’s centre. The sidebar’s collapse button (or Mod+Alt+B) → the rail split.',
    render: (ctx) => {
      const anim = takeAnim(ctx);
      const region = todoRegion(ctx, {
        zoom: 'paper', readable: true,
        sw: { conversation: opens('the Conversation zoom (the switch, as on iPad)') },
        compNav: opens('the composer (Mod+I)'),
        trailing: [{ icon: 'ellipsis', title: 'More', toast: 'Opens: Copy Link · Move to Project… · Someday · Delete…' }],
        anim,
      });
      return ui.split([{ w: 284, html: webSidebar(ctx) }, { w: 360, html: webList(ctx), line: true }, { flex: 1, html: region, line: true }]);
    },
  });

  /* ================================================================ Android foldable, book posture (V3-15) */

  const A = 'android-fold';
  /** The place summary in the E1 order: need you · working · waiting · to do · just finished, then can wait. */
  function summaryOf(D, goes) {
    const order = ['needs', 'working', 'waiting', 'todo', 'just_finished', 'can_wait'];
    const lines = D.summary('all').slice().sort((a, b) => order.indexOf(a.id) - order.indexOf(b.id)).map((l) => Object.assign({}, l, goes[l.id]));
    const names = D.projects.map((x) => x.name);
    const c = D.countsOf('all');
    // The open count already holds the to-dos without a project: the parts add up to it.
    const loose = D.counts.withoutProject;
    return ui.placeSummary({
      title: D.place('all').title,
      sub: `${c.open} open · ${c.open - loose} in ${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}${loose ? `, ${loose} without a project` : ''}`,
      lines, foot: ['Select a to-do to see it here.'],
    });
  }
  function androidFrame(ctx, o) {
    const anim = takeAnim(ctx);
    const list = listColumn(ctx, {
      leading: { icon: 'line-3-horizontal', title: 'Open Drawer', look: 'plain', toast: 'Opens: the navigation drawer' },
      trailing: [{ icon: 'magnifying-glass', look: 'plain', title: 'Search', toast: 'Opens: Search To-dos' }, { icon: 'ellipsis', look: 'plain', title: 'More', toast: 'Opens: Sort By · Show Someday · Show Done Today' }],
      where: 'in the right pane', sel: o.summary ? null : 't01',
      rowGo: o.summary ? { t01: Object.assign(go(A + '/list-conversation', 'fade'), { markNav: go(A + '/list-conversation', 'fade') }) } : {},
    });
    let right;
    if (o.summary) {
      right = `<div class="k-page v3-col">${summaryOf(ctx.data, { needs: opens('the Needs you section'), working: opens('the Working section'), just_finished: opens('the Just finished section'), waiting: opens('the Waiting section'), todo: opens('the To do section'), can_wait: opens('Can wait, unfolded') })}</div>`;
    } else {
      right = todoRegion(ctx, {
        zoom: 'conversation', short: true, narrow: true, compact: true, r4: 'replied',
        sw: { paper: opens('the Details zoom (the body slides within the pane)') },
        compNav: opens('the keyboard'),
        leading: [{ icon: 'v3-back-android', title: 'Back', look: 'plain', go: A + '/list-conversation~back', transition: 'fade' }],
        trailing: [{ icon: 'ellipsis', look: 'plain', title: 'More', toast: 'Opens: Copy Link · Move to Project… · Someday · Delete…' }],
        anim,
      });
    }
    return ui.split([{ w: 336, html: list }, { flex: 1, html: right, line: true }], { cls: 'v3-android' });
  }
  F(A + '/list-conversation', {
    device: A,
    title: 'Android foldable · list ┊ spine + Conversation',
    note: 'V3-15. Book posture: list ┊ to-do at the hinge, the body switch in the right pane’s top app bar. Predictive Back pops to the list (the to-do pane clears).',
    render: (ctx) => androidFrame(ctx, {}),
  });
  F(A + '/list-conversation~back', {
    device: A,
    title: 'Android foldable · after Back',
    note: 'Predictive Back popped to the list: nothing selected, so the right pane shows the place summary in the E1 order (need you · working · waiting · to do · just finished · can wait).',
    render: (ctx) => androidFrame(ctx, { summary: true }),
  });
})();
