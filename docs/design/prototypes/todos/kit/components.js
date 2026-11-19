/*
 * Persona To-dos prototype kit: components (Kit.ui).
 *
 * Every helper returns an HTML string. Text options accept HTML (so <b> works); escape data you did not
 * write with Kit.esc. Clickable things take `go: 'frameId'` and become data-go links between frames.
 * Styles are in kit/components.css; all colors come from kit/tokens.css.
 */
(function () {
  const Kit = window.Kit;
  const ui = (Kit.ui = Kit.ui || {});
  const I = (n, s, o) => Kit.icon(n, s, o);
  const esc = Kit.esc;
  const go = Kit.goAttr;
  const J = Kit.join;
  const arr = (x) => (x == null || x === false ? [] : Array.isArray(x) ? x : [x]);

  /* ------------------------------------------------------------ states and tones */

  // One state vocabulary for every screen: the mark, the word (iOS Title Case) and the tone.
  // Variants may change words through ui.states before rendering, never per screen.
  ui.states = {
    needs: { word: 'Needs You', tone: 'needs' },
    working: { word: 'Working', tone: 'working' },
    waiting: { word: 'Waiting', tone: 'waiting' },
    ready: { word: 'Ready', tone: 'ready' },
    unread: { word: 'Unread', tone: 'ready' },
    idle: { word: 'Not Started', tone: 'idle' },
    later: { word: 'Later', tone: 'idle' },
    done: { word: 'Done', tone: 'idle' },
    stuck: { word: 'Stuck', tone: 'caution' },
    review: { word: 'Awaiting Review', tone: 'caution' },
    offline: { word: 'Offline', tone: 'idle' },
  };
  ui.stateWord = (state) => (ui.states[state] || { word: state }).word;
  ui.toneOf = (state) => (ui.states[state] || { tone: state }).tone;

  /** A spinner (working). */
  ui.spinner = (size) => `<span class="k-spin" style="--s:${size || 12}px"></span>`;

  /** State mark: needs (blue dot), working (amber spinner), waiting (light amber clock), ready / unread (green dot),
      idle / later / offline (gray dot), done (gray check), stuck / review (caution dot). */
  ui.mark = function mark(state, o) {
    o = o || {};
    const s = o.size;
    switch (state) {
      case 'needs': return `<span class="k-mark k-mark--needs" style="--s:${s || 9}px"></span>`;
      case 'working': return ui.spinner(s || 12);
      case 'waiting': return `<span class="k-mark k-mark--waiting">${I('clock', s || 13)}</span>`;
      case 'done': return `<span class="k-mark k-mark--done">${I('checkmark-circle-fill', s || 13)}</span>`;
      case 'later': return `<span class="k-mark k-mark--later" style="--s:${s || 9}px"></span>`;
      default: return `<span class="k-mark k-mark--${ui.toneOf(state)}" style="--s:${s || 9}px"></span>`;
    }
  };

  /** State line: mark + state word in its tone, then " · text" in secondary ink.
      o: { word (override), text, size, plain (no mark) } */
  ui.state = function state(st, o) {
    o = typeof o === 'string' ? { text: o } : o || {};
    const word = o.word != null ? o.word : ui.stateWord(st);
    return `<span class="k-stateline"><span class="k-state k-tone--${ui.toneOf(st)}">${o.plain ? '' : ui.mark(st, { size: o.size })}${word ? `<span class="k-state-word">${word}</span>` : ''}</span>${o.text ? `${word ? '<span class="k-sep">·</span>' : ''}<span class="k-state-text">${o.text}</span>` : ''}</span>`;
  };

  /** CountBadge. look: 'needs' (blue on tint), 'neutral' (white on gray), 'plain' (secondary number), 'red' (tab badge) */
  ui.badge = (n, o) => `<span class="k-badge k-badge--${(o && o.look) || 'neutral'}">${n}</span>`;

  /** Chip. o: { icon, tone (state or role or 'hue-blue'…), look: 'tone' | 'neutral' | 'outline' | 'solid', size: 'compact' | 'regular', trailing, go } */
  ui.chip = function chip(label, o) {
    o = o || {};
    const look = o.look || (o.tone ? 'tone' : 'neutral');
    const tone = o.tone ? (ui.states[o.tone] ? ui.toneOf(o.tone) : o.tone) : '';
    return `<span class="${Kit.cls('k-chip', 'k-chip--' + look, 'k-chip--' + (o.size || 'compact'), tone && 'k-tone--' + tone, o.cls)}"${go(o.go)}>${o.icon ? I(o.icon, o.size === 'regular' ? 14 : 12) : ''}${o.dot ? ui.mark(o.dot, { size: 7 }) : ''}<span>${label}</span>${o.trailing || ''}</span>`;
  };

  /** Due chip; overdue turns danger red. */
  ui.due = (text, o) => `<span class="k-due${o && o.overdue ? ' is-overdue' : ''}">${I((o && o.icon) || 'calendar', 13)}<span>${text}</span></span>`;
  /** Project chip: the project's hue on its symbol, the name in secondary ink. */
  ui.project = (name, o) => `<span class="k-proj" style="--hue:var(--hue-${(o && o.hue) || 'blue'})">${I((o && o.icon) || 'project', 13)}<span>${name}</span></span>`;
  /** Priority flag (caution tone). */
  ui.flag = () => `<span class="k-flag">${I('flag-fill', 13)}</span>`;
  /** A meta line: chips with even gaps. */
  ui.meta = (items) => `<span class="k-meta">${J(arr(items))}</span>`;
  /** A row of chips with an optional "+ Add" chip. */
  ui.chips = (items, o) => `<div class="k-chips">${J(arr(items))}${o && o.add ? ui.chip(o.add, { icon: 'plus', look: 'outline', go: o.addGo }) : ''}</div>`;

  /* ------------------------------------------------------------ Persona face, avatars */

  const FACE = `<svg viewBox="0 0 40 40" aria-hidden="true"><circle cx="20" cy="20" r="20" fill="var(--face-bg)"/>
    <path d="M9.6 28.4c-1.7-8.8 3.2-17.2 11.4-17.2 7.4 0 11.6 5.5 11 12.5-.4 4.4-2.8 6.9-6.1 7.2l-12.4.3c-2.1 0-3.6-1-3.9-2.8z" fill="#fff" stroke="#1C1C1E" stroke-width="1.7" stroke-linejoin="round"/>
    <circle cx="17.2" cy="20.4" r="1.55" fill="#1C1C1E"/><circle cx="23.8" cy="19.7" r="1.55" fill="#1C1C1E"/>
    <ellipse cx="13.7" cy="23.6" rx="2" ry="1.45" fill="#F26B4F"/>
    <path d="M29.4 11.2l1.7-2.3M31.9 13.9l2.6-1.1M27 9.6l.3-2.7" stroke="var(--face-lines)" stroke-width="1.4" stroke-linecap="round"/></svg>`;

  /** Persona's face. o: { state: 'needs' | 'working' | … (a dot on the corner) } */
  ui.face = (size, o) => `<span class="k-face" style="--s:${size || 32}px">${FACE}${o && o.state ? `<i class="k-face-dot">${ui.mark(o.state, { size: Math.max(7, Math.round((size || 32) * 0.26)) })}</i>` : ''}</span>`;

  /** Monogram avatar. o: { hue, size } */
  ui.avatar = (text, o) => {
    o = o || {};
    return `<span class="k-avatar" style="--s:${o.size || 32}px;--hue:var(--hue-${o.hue || 'indigo'})">${o.icon ? I(o.icon, Math.round((o.size || 32) * 0.5)) : text}</span>`;
  };

  /* ------------------------------------------------------------ buttons */

  const BTN_ICON = { xs: 13, s: 15, m: 19, l: 21 };
  /** Button. o: { icon, label, look: 'glass' | 'prominent' | 'tinted' | 'fill' | 'plain' | 'bare' | 'danger', size: 'xs' | 's' | 'm' | 'l', badge, active, go,
      transition, toast, face, dot (a blue unread dot on the corner, e.g. the Conversation toggle) }
      Icon-only buttons are circles; labels never wrap. A string is passed through as HTML. */
  ui.btn = function btn(o) {
    if (typeof o === 'string') return o;
    o = o || {};
    const size = o.size || 'm';
    const isIcon = (o.icon || o.face) && !o.label;
    const inner = J([
      o.face ? ui.face(size === 'l' ? 40 : size === 'm' ? 36 : 26) : '',
      o.icon ? I(o.icon, BTN_ICON[size]) : '',
      o.label ? `<span class="k-btn-label">${o.label}</span>` : '',
      o.trailingIcon ? I(o.trailingIcon, BTN_ICON[size] - 5) : '',
      o.badge != null ? `<span class="k-btn-badge">${ui.badge(o.badge, { look: o.badgeLook || 'red' })}</span>` : '',
      o.dot ? '<i class="k-btn-dot"></i>' : '',
    ]);
    return `<span class="${Kit.cls('k-btn', 'k-btn--' + (o.look || 'glass'), 'k-btn--' + size, isIcon && 'is-icon', o.face && 'has-face', o.active && 'is-active', o.cls)}"${Kit.navAttrs(o)} title="${esc(o.title || (o.label || '').replace(/<[^>]+>/g, ''))}">${inner}</span>`;
  };
  /** Several icon buttons sharing one glass capsule (iOS 26 groups bar items). */
  ui.btnGroup = (items, o) => `<span class="k-btn-group${o && o.size ? ' k-btn-group--' + o.size : ''}">${arr(items).map((b) => (typeof b === 'string' ? b : ui.btn(Object.assign({ size: (o && o.size) || 'm' }, b, { look: 'bare' })))).join('')}</span>`;
  /** One row of capsule buttons that never wraps (inline answers). items: [{ label, recommended, icon, go }] */
  ui.capsules = (items, o) => `<div class="k-caps${o && o.fade ? ' is-fading' : ''}${o && o.size ? ' k-caps--' + o.size : ''}">${arr(items).map((it) => (typeof it === 'string' ? `<span class="k-cap">${it}</span>` : `<span class="k-cap${it.recommended ? ' is-rec' : ''}${it.quiet ? ' is-quiet' : ''}"${go(it.go)}>${it.icon ? I(it.icon, 14) : ''}<span>${it.label}</span></span>`)).join('')}</div>`;

  /* ------------------------------------------------------------ bars and layout */

  /** Top bar. o: { back: true | { go }, leading: [btn…], title, subtitle, trailing: [btn…], group (default true: trailing
      icon buttons share a capsule), large: 'Title' (large title row), largeSub, below (HTML under the bar, e.g. a filter bar),
      align: 'leading' (title at the leading edge, web-style), wc (leave room for iPad window controls),
      fog (a scroll-edge fade under the bar, for content shown scrolled under it), center (HTML instead of the title) } */
  ui.navbar = function navbar(o) {
    o = o || {};
    const lead = [];
    if (o.back) lead.push(ui.btn({ icon: 'chevron-left', go: o.back.go || (typeof o.back === 'string' ? o.back : undefined), title: 'Back' }));
    arr(o.leading).forEach((b) => lead.push(ui.btn(b)));
    const trailItems = arr(o.trailing);
    const allIcons = trailItems.every((b) => typeof b === 'object' && b.icon && !b.label && !b.look);
    const trail = trailItems.length > 1 && o.group !== false && allIcons ? ui.btnGroup(trailItems) : trailItems.map(ui.btn).join('');
    const mid = o.center || (o.title ? `<div class="k-nav-title">${o.title}</div>${o.subtitle ? `<div class="k-nav-sub">${o.subtitle}</div>` : ''}` : '');
    return `<div class="${Kit.cls('k-nav', o.wc && 'has-wc', o.fog && 'has-fog', o.align === 'leading' && 'is-leading', o.cls)}">
      <div class="k-nav-bar"><div class="k-nav-lead">${lead.join('')}</div><div class="k-nav-mid">${mid}</div><div class="k-nav-trail">${trail}</div></div>
      ${o.large ? `<div class="k-large"><div class="k-large-title">${o.large}</div>${o.largeSub ? `<div class="k-large-sub">${o.largeSub}</div>` : ''}</div>` : ''}
      ${o.below || ''}
    </div>`;
  };

  /** Page: a top bar, a body and a floating bottom (composer or toolbar) with the page color fading in under it.
      o: { top, body, bottom, bg, pad (default: the page margin on both sides; a number = px all round; false = none), overlay (HTML over
      the whole page, e.g. a sheet), cls }. The bottom sits in the flow: the body ends above it and passes under its fog. */
  ui.page = function page(o) {
    o = o || {};
    const pad = o.pad === false ? '0' : typeof o.pad === 'number' ? o.pad + 'px' : o.pad || '0 var(--page-margin)';
    return `<div class="${Kit.cls('k-page', o.cls)}" style="${o.bg ? `--pg-bg:${o.bg};` : ''}">
      ${o.top || ''}
      <div class="k-body" style="padding:${pad}">${o.body || ''}</div>
      ${o.bottom ? `<div class="k-bottom">${o.bottom}</div>` : ''}
      ${o.overlay || ''}
    </div>`;
  };
  /** Floating bottom area on its own (inside any positioned container). */
  ui.bottom = (html, o) => `<div class="k-bottom${o && o.bare ? ' is-bare' : ''}">${html}</div>`;

  /** Horizontal panes. cols: [{ w (px) | flex, html, bg, line (hairline on the leading edge), cls }] */
  ui.split = function split(cols, o) {
    return `<div class="k-split${o && o.cls ? ' ' + o.cls : ''}">${arr(cols).map((c) => {
      const style = J([c.w ? `width:${c.w}px;flex:none` : `flex:${c.flex || 1};min-width:0`, c.bg ? `--pg-bg:${c.bg};background:${c.bg}` : ''], ';');
      return `<div class="${Kit.cls('k-col', c.line && 'has-line', c.cls)}" style="${style}">${c.html || ''}</div>`;
    }).join('')}</div>`;
  };
  /** Vertical stack of panes (detail above, conversation below). rows: [{ h | flex, html, bg, line }] */
  ui.vsplit = function vsplit(rows) {
    return `<div class="k-vsplit">${arr(rows).map((r) => `<div class="${Kit.cls('k-row-pane', r.line && 'has-line')}" style="${J([r.h ? `height:${r.h}px;flex:none` : `flex:${r.flex || 1};min-height:0`, r.bg ? `--pg-bg:${r.bg};background:${r.bg}` : ''], ';')}">${r.html || ''}</div>`).join('')}</div>`;
  };

  /** Floating bottom toolbar (iOS 26): glass items at the leading and trailing ends, optional center. */
  ui.toolbar = (o) => `<div class="k-toolbar"><span>${arr(o.leading).map(ui.btn).join('')}</span><span class="k-toolbar-mid">${o.center || ''}</span><span>${arr(o.trailing).map(ui.btn).join('')}</span></div>`;

  /** Search capsule. o: { placeholder, mic, size: 's' | 'm', go } */
  ui.search = (o) => {
    o = typeof o === 'string' ? { placeholder: o } : o || {};
    return `<div class="k-search${o.size === 's' ? ' k-search--s' : ''}${o.glass ? ' k-search--glass' : ''}"${go(o.go)}>${I('magnifying-glass', 16)}<span class="k-search-ph">${o.placeholder || 'Search'}</span>${o.mic ? I('mic', 16) : ''}${o.kbd ? `<kbd>${o.kbd}</kbd>` : ''}</div>`;
  };

  /** iOS segmented control. */
  ui.segmented = (items, selected, o) => `<div class="k-seg${o && o.size === 's' ? ' k-seg--s' : ''}">${arr(items).map((t, i) => `<span class="${i === (selected || 0) ? 'is-on' : ''}">${t}</span>`).join('')}</div>`;

  /** Capsule filter bar (the Inbox's section bar). items: [{ label, count, look: 'needs' | 'neutral' | 'plain', go }] */
  ui.filterBar = (items, o) => {
    o = o || {};
    return `<div class="k-filters${o.fade ? ' is-fading' : ''}"><div class="k-filters-in">${arr(items).map((it, i) => `<span class="k-filter${i === (o.selected || 0) ? ' is-on' : ''}"${go(it.go)}><span>${it.label}</span>${it.count != null ? ui.badge(it.count, { look: it.look || 'neutral' }) : ''}</span>`).join('')}</div>${o.trailing ? `<span class="k-filters-trail">${o.trailing}</span>` : ''}</div>`;
  };

  /** Section header: 13 semibold, sentence case, with a CountBadge. o: { count, look, tone, fold: 'open' | 'closed', trailing } */
  ui.section = (title, o) => {
    o = o || {};
    return `<div class="k-sec${o.tone ? ' k-tone--' + ui.toneOf(o.tone) + ' is-toned' : ''}">${o.fold ? I(o.fold === 'open' ? 'chevron-down' : 'chevron-right', 11) : ''}<span class="k-sec-title">${title}</span>${o.count != null ? ui.badge(o.count, { look: o.look || 'neutral' }) : ''}<span class="k-sec-trail">${o.trailing || ''}</span></div>`;
  };

  /* ------------------------------------------------------------ rows */

  function checkbox(kind) {
    if (kind === 'none') return '';
    if (kind === 'done') return `<span class="k-check is-done">${I('checkmark-circle-fill', 24)}</span>`;
    return '<span class="k-check"></span>';
  }

  /** A to-do row. o: { title, check: 'open' | 'done' | 'none', state, word, stateText, sub, meta: [chips], answers: [capsules],
      more ('2 more' or { label, go }), trailing ('2h' or HTML), chevron, selected, priority, go, dense, cls } */
  ui.row = function row(o) {
    o = o || {};
    const check = o.check || (o.done ? 'done' : 'open');
    const more = o.more ? (typeof o.more === 'string' ? { label: o.more } : o.more) : null;
    return `<div class="${Kit.cls('k-row', o.selected && 'is-selected', check === 'done' && 'is-done', o.dense && 'is-dense', check === 'none' && 'no-check', o.cls)}"${go(o.go)}>
      ${check === 'none' ? '' : `<span class="k-row-check">${checkbox(check)}</span>`}
      <div class="k-row-main">
        <div class="k-row-head"><span class="k-row-title">${o.title || ''}</span>${o.priority ? ui.flag() : ''}${o.trailing ? `<span class="k-row-trail">${o.trailing}</span>` : ''}${o.chevron ? `<span class="k-row-chev">${I('chevron-right', 13)}</span>` : ''}</div>
        ${o.state ? `<div class="k-row-line">${ui.state(o.state, { word: o.word, text: o.stateText })}</div>` : ''}
        ${o.sub ? `<div class="k-row-sub">${o.sub}</div>` : ''}
        ${o.meta && arr(o.meta).length ? `<div class="k-row-meta">${J(arr(o.meta))}</div>` : ''}
        ${o.answers ? ui.capsules(o.answers, { fade: o.fadeAnswers }) : ''}
        ${more ? `<div class="k-row-more"${go(more.go)}>${I('chevron-right', 11)}<span>${more.label}</span></div>` : ''}
      </div>
    </div>`;
  };

  /** A list of rows. o: { inset (inset-grouped cell), header (HTML above the group) } */
  ui.list = (rows, o) => `${o && o.header ? o.header : ''}<div class="k-list${o && o.inset ? ' is-inset' : ''}">${J(arr(rows))}</div>`;

  /** A simple settings-like row: icon, label, value, chevron. */
  ui.cell = (o) => `<div class="k-cell"${go(o.go)}>${o.icon ? `<span class="k-cell-ic" style="--hue:var(--hue-${o.hue || 'gray'})">${I(o.icon, 16)}</span>` : ''}<span class="k-cell-label">${o.label}</span><span class="k-cell-value">${o.value || ''}</span>${o.chevron !== false ? I('chevron-right', 13) : ''}</div>`;

  /* ------------------------------------------------------------ cards */

  /** Generic surface. o: { look: 'cell' | 'card' | 'glass' | 'tint', pad, go } */
  ui.card = (html, o) => `<div class="k-card k-card--${(o && o.look) || 'card'}"${go(o && o.go)}${o && o.pad != null ? ` style="padding:${o.pad}px"` : ''}>${html}</div>`;

  /** Ask card: something only the owner can move. o: { from, age, icon, question, detail, recommended: { label, why, go },
      options: [{ label, go }] (≤ 3), dismiss ('Not now'), other ('Other answer…'), compact } */
  ui.ask = function ask(o) {
    o = o || {};
    const rec = o.recommended;
    const caps = [];
    if (rec) caps.push({ label: rec.label, recommended: true, go: rec.go, icon: rec.icon });
    arr(o.options).slice(0, 3).forEach((x) => caps.push(typeof x === 'string' ? { label: x } : x));
    return `<div class="k-ask${o.compact ? ' is-compact' : ''}"${go(o.go)}>
      <div class="k-ask-from">${I(o.icon || 'bubble-exclamation-fill', 15)}<span>${o.from || 'Persona asks'}</span>${o.age ? `<span class="k-ask-age">· ${o.age}</span>` : ''}</div>
      <div class="k-ask-q">${o.question || ''}</div>
      ${o.detail ? `<div class="k-ask-detail">${o.detail}</div>` : ''}
      ${rec && rec.why ? `<div class="k-ask-why">${I('sparkle', 11)}<span><b>Recommended:</b> ${rec.why}</span></div>` : ''}
      ${caps.length ? ui.capsules(caps) : ''}
      ${o.dismiss || o.other ? `<div class="k-ask-foot">${o.other ? `<span class="k-link">${o.other}</span>` : ''}${o.dismiss ? `<span class="k-link is-quiet">${o.dismiss}</span>` : ''}</div>` : ''}
    </div>`;
  };

  /** Folded summary line (super rule 2): "▸ Worked 6 min · searched 14 ryokans". o: { open, icon } */
  ui.fold = (text, o) => `<div class="k-fold"${go(o && o.go)}>${o && o.icon ? I(o.icon, 13) : ''}<span>${text}</span>${I(o && o.open ? 'chevron-down' : 'chevron-right', 11)}</div>`;

  /** Activity round: the owner's instruction, the work folded under it, the result.
      o: { instruction, receipt, summary, result, resultState ('done' | 'needs' | 'working' | …), when } */
  ui.round = function round(o) {
    o = o || {};
    return `<div class="k-round">
      ${o.when ? `<div class="k-round-when">${o.when}</div>` : ''}
      ${o.instruction ? ui.msg.user(o.instruction, { receipt: o.receipt }) : ''}
      ${o.summary ? ui.fold(o.summary, { go: o.summaryGo }) : ''}
      ${o.result ? `<div class="k-round-result">${o.resultState ? ui.mark(o.resultState) : ''}<span>${o.result}</span></div>` : ''}
    </div>`;
  };

  /** Run card: one Agent's run for the to-do. o: { role, where, latest, state, word, icon, hue, go, chevron } */
  ui.run = function run(o) {
    o = o || {};
    return `<div class="k-run"${go(o.go)}>
      <span class="k-run-ic" style="--hue:var(--hue-${o.hue || 'indigo'})">${o.face ? ui.face(32) : I(o.icon || 'sparkle', 17)}</span>
      <div class="k-run-main">
        <div class="k-run-head"><span class="k-run-role">${o.role || ''}</span>${o.state ? `<span class="k-run-state">${ui.state(o.state, { word: o.word })}</span>` : ''}</div>
        ${o.where ? `<div class="k-run-where">${o.where}</div>` : ''}
        ${o.latest ? `<div class="k-run-latest">${o.latest}</div>` : ''}
      </div>
      ${o.chevron ? `<span class="k-run-chev">${I('chevron-right', 13)}</span>` : ''}
    </div>`;
  };

  const DELIV_ICON = { pr: 'pr', doc: 'doc', file: 'doc', link: 'link', booking: 'bed', flight: 'airplane', order: 'cart', build: 'wrench' };
  /** Deliverable: what the work produced. o: { kind: 'pr' | 'doc' | 'booking' | 'flight' | 'link' | 'order' | 'build', icon, title, sub,
      outcome ('Merged', 'Booked'), tone ('ready' | 'needs' | 'working' | 'idle' | …), look: 'card' | 'chip', go } */
  ui.deliverable = function deliverable(o) {
    o = o || {};
    const ic = o.icon || DELIV_ICON[o.kind] || 'doc';
    const tone = o.tone || 'ready';
    if (o.look === 'chip') {
      return `<span class="k-deliv-chip"${go(o.go)}>${I(ic, 13)}<span>${o.title}</span>${o.outcome ? `<span class="k-deliv-out k-tone--${ui.toneOf(tone)}">${o.outcome}</span>` : ''}</span>`;
    }
    return `<div class="k-deliv"${go(o.go)}>
      <span class="k-deliv-ic k-tone--${ui.toneOf(tone)}">${I(ic, 18)}</span>
      <div class="k-deliv-main"><div class="k-deliv-title">${o.title || ''}</div><div class="k-deliv-sub">${o.outcome ? `<span class="k-deliv-out k-tone--${ui.toneOf(tone)}">${o.outcome}</span>` : ''}${o.outcome && o.sub ? '<span class="k-sep">·</span>' : ''}${o.sub ? `<span>${o.sub}</span>` : ''}</div></div>
      <span class="k-deliv-open">${I(o.openIcon || 'arrow-up-right', 14)}</span>
    </div>`;
  };

  /** Steps checklist. items: [{ text, done, state ('needs' | 'working' | …), by (HTML on the trailing side), sub, suggested }]
      o: { title, count, add ('Add step'), suggestTitle, addAll } — suggested steps gather in a dashed box at the end. */
  ui.steps = function steps(items, o) {
    o = o || {};
    const list = arr(items);
    const real = list.filter((s) => !s.suggested);
    const sugg = list.filter((s) => s.suggested);
    const lead = (s) => {
      if (s.done) return `<span class="k-step-check is-done">${I('checkmark-circle-fill', 22)}</span>`;
      if (s.state === 'needs') return '<span class="k-step-check is-needs"><span class="k-check"></span><i></i></span>';
      if (s.state === 'working') return `<span class="k-step-check is-working">${ui.spinner(22)}</span>`;
      return '<span class="k-step-check"><span class="k-check"></span></span>';
    };
    const row = (s) => `<div class="k-step${s.done ? ' is-done' : ''}"${go(s.go)}>${lead(s)}<span class="k-step-text">${s.text}${s.sub ? `<span class="k-step-sub">${s.sub}</span>` : ''}</span>${s.by ? `<span class="k-step-by">${s.by}</span>` : ''}</div>`;
    const srow = (s) => `<div class="k-step is-suggested"${go(s.go)}><span class="k-step-check"><span class="k-check is-dashed"></span></span><span class="k-step-text">${s.text}${s.sub ? `<span class="k-step-sub">${s.sub}</span>` : ''}</span><span class="k-cap is-small">${I('plus', 12)}<span>Add</span></span></div>`;
    return `<div class="k-steps">
      ${o.title ? ui.section(o.title, { count: o.count, look: 'plain', trailing: o.trailing }) : ''}
      <div class="k-steps-list">${real.map(row).join('')}${o.add ? `<div class="k-step is-add"><span class="k-step-check">${I('plus', 16)}</span><span class="k-step-text">${o.add}</span></div>` : ''}</div>
      ${sugg.length ? `<div class="k-suggest"><div class="k-suggest-head">${I('wand', 14)}<span>${o.suggestTitle || 'Persona suggests'}</span><span class="k-link">${o.addAll || 'Add All'}</span></div>${sugg.map(srow).join('')}</div>` : ''}
    </div>`;
  };

  /* ------------------------------------------------------------ sheets and panes */

  /** Bottom sheet (visual only). o: { detent: 'small' | 'medium' | 'large' | px, title, leading, trailing, content, grabber (true), scrim (true) }
      Partial detents float inset with all corners rounded (iOS 26); large spans the width. Put it inside a positioned container. */
  ui.sheet = function sheet(o) {
    o = o || {};
    const det = o.detent || 'medium';
    const style = typeof det === 'number' ? `height:${det}px` : '';
    const head = o.title || o.leading || o.trailing
      ? `<div class="k-sheet-head"><span class="k-sheet-lead">${arr(o.leading).map(ui.btn).join('')}</span><span class="k-sheet-title">${o.title || ''}</span><span class="k-sheet-trail">${arr(o.trailing).map(ui.btn).join('')}</span></div>`
      : '';
    return `${o.scrim !== false ? '<div class="k-scrim"></div>' : ''}<div class="k-sheet k-sheet--${typeof det === 'number' ? 'px' : det}" style="${style}">${o.grabber !== false ? '<span class="k-grabber"></span>' : ''}${head}<div class="k-sheet-body">${o.content || ''}</div></div>`;
  };
  /** Scrim on its own. */
  ui.scrim = () => '<div class="k-scrim"></div>';
  /** Trailing inspector pane. o: { width (400), content, overlay (floats over the page with glass, for mid widths) } */
  ui.inspector = (o) => `<div class="k-inspector${o.overlay ? ' is-overlay' : ''}" style="width:${o.width || 400}px">${o.content || ''}</div>`;

  /* ------------------------------------------------------------ conversation */

  ui.msg = {
    /** The owner's message. o: { receipt ('Sent to ‘Hotel search’ · Delivered'), to (chip above) } */
    user: (text, o) => `<div class="k-msg k-msg--user">${o && o.to ? `<span class="k-msg-to">${o.to}</span>` : ''}<div class="k-bubble">${text}</div>${o && o.receipt ? `<div class="k-receipt">${o.receipt}</div>` : ''}</div>`,
    /** Persona's reply (in the Persona box). o: { name, plain } */
    persona: (text, o) => `<div class="k-msg k-msg--persona">${o && o.name ? `<div class="k-msg-name">${ui.face(18)}<span>${o.name}</span></div>` : ''}<div class="${o && o.plain ? 'k-msg-plain' : 'k-pbox'}">${text}</div></div>`,
    /** An Agent's message, with its role. o: { name, icon, hue } */
    agent: (text, o) => `<div class="k-msg k-msg--agent"><div class="k-msg-name"><span class="k-run-ic is-mini" style="--hue:var(--hue-${(o && o.hue) || 'indigo'})">${I((o && o.icon) || 'sparkle', 11)}</span><span>${(o && o.name) || 'Agent'}</span></div><div class="k-msg-plain">${text}</div></div>`,
    /** Folded tool steps: "▸ 4 steps" */
    tools: (summary, o) => `<div class="k-msg k-msg--tools">${ui.fold(summary, o)}</div>`,
    /** A receipt or system line. tone: 'ready' | 'needs' | … */
    receipt: (text, o) => `<div class="k-msg k-msg--receipt${o && o.align === 'center' ? ' is-center' : ''}">${o && o.state ? ui.mark(o.state, { size: 7 }) : ''}<span>${text}</span></div>`,
    /** Day or time divider. */
    day: (text) => `<div class="k-msg k-msg--day">${text}</div>`,
    /** Any card inside the stream. */
    card: (html) => `<div class="k-msg k-msg--card">${html}</div>`,
  };

  /** The addressee chip: who receives what you type. to: 'Persona' or { label, icon, face, agent (tinted), clear } */
  ui.addressee = function addressee(to, o) {
    const t = typeof to === 'string' ? { label: to, face: /persona/i.test(to) } : to || {};
    const lead = t.face ? ui.face(18) : t.icon ? `<span class="k-to-ic">${I(t.icon, 12)}</span>` : '';
    return `<span class="${Kit.cls('k-to', t.agent && 'is-agent', o && o.inside && 'is-inside')}"${go(t.go)}>${lead}<span>${t.prefix === false ? '' : (t.prefix || 'To') + ' '}${t.label}</span>${I(t.clear ? 'xmark' : 'chevron-down', 10)}</span>`;
  };

  /** Composer. o: { placeholder, text (typed text, shows the send button), to (addressee), toPlacement: 'above' | 'inside',
      plus (true), mic (true), stop, note (a line above, e.g. "Persona is working · Steer or queue") } */
  ui.composer = function composer(o) {
    o = o || {};
    const above = o.to && o.toPlacement !== 'inside';
    const trailing = o.stop
      ? `<span class="k-send is-stop">${I('stop', 13)}</span>`
      : o.text ? `<span class="k-send">${I('arrow-up', 17)}</span>` : o.mic !== false ? `<span class="k-comp-btn">${I('mic', 20)}</span>` : '';
    return `<div class="k-compose${o.cls ? ' ' + o.cls : ''}">
      ${above || o.note ? `<div class="k-compose-top">${above ? ui.addressee(o.to) : ''}${o.note ? `<span class="k-compose-note">${o.note}</span>` : ''}</div>` : ''}
      <div class="k-composer">
        ${o.plus !== false ? `<span class="k-comp-btn">${I('plus', 21)}</span>` : '<span class="k-comp-gap"></span>'}
        ${o.to && !above ? ui.addressee(o.to, { inside: true }) : ''}
        <span class="k-comp-field${o.text ? ' has-text' : ''}">${o.text ? `${o.text}<i class="k-caret"></i>` : o.placeholder || 'Message'}</span>
        ${trailing}
      </div>
    </div>`;
  };

  /** Conversation pane. o: { header: { title, state, stateText, word, back, trailing, sub }, messages: [HTML], pinned (HTML above the
      composer, e.g. an open ask), composer: { …ui.composer options }, bg } */
  ui.convo = function convo(o) {
    o = o || {};
    const h = o.header || {};
    const head = `<div class="k-convo-head${h.wc ? ' has-wc' : ''}">
      ${h.back ? ui.btn({ icon: 'chevron-left', go: h.back.go || (typeof h.back === 'string' ? h.back : undefined) }) : ''}
      ${arr(h.leading).map(ui.btn).join('')}
      <div class="k-convo-id"${go(h.go)}><div class="k-convo-title">${h.title || ''}</div>${h.state ? `<div class="k-convo-state">${ui.state(h.state, { word: h.word, text: h.stateText })}</div>` : h.sub ? `<div class="k-convo-state">${h.sub}</div>` : ''}</div>
      ${arr(h.trailing).length > 1 && arr(h.trailing).every((b) => b.icon && !b.label) ? ui.btnGroup(h.trailing) : arr(h.trailing).map(ui.btn).join('')}
    </div>`;
    return ui.page({
      bg: o.bg,
      top: head + (o.below || ''),
      pad: false,
      body: `<div class="k-msgs">${J(arr(o.messages))}</div>`,
      bottom: (o.pinned ? `<div class="k-pinned">${o.pinned}</div>` : '') + (o.composer === false ? '' : ui.composer(o.composer)),
      cls: 'k-convo',
    });
  };

  /* ------------------------------------------------------------ sidebar */

  /** Sidebar row. o: { icon, mark (HTML instead of the icon), label, sub, count, countLook ('plain' | 'needs' | 'neutral'),
      selected, indent (0 | 1 | 2), disclosure ('open' | 'closed'), time, go, dim } */
  ui.sbRow = function sbRow(o) {
    o = o || {};
    const count = o.count == null ? '' : o.countLook && o.countLook !== 'plain' ? ui.badge(o.count, { look: o.countLook }) : `<span class="k-sbr-count">${o.count}</span>`;
    return `<div class="${Kit.cls('k-sbr', o.selected && 'is-selected', o.dim && 'is-dim', o.sub && 'has-sub')}" style="--indent:${o.indent || 0}"${go(o.go)}>
      <span class="k-sbr-glyph${o.tone ? ' k-tone--' + o.tone : ''}">${o.mark || (o.icon ? I(o.icon) : '')}</span>
      <span class="k-sbr-label"><span>${o.label}</span>${o.sub ? `<small>${o.sub}</small>` : ''}</span>
      ${count}${o.time ? `<span class="k-sbr-time">${o.time}</span>` : ''}
      ${o.disclosure ? `<span class="k-sbr-disc">${I(o.disclosure === 'open' ? 'chevron-down' : 'chevron-right', 12)}</span>` : ''}
    </div>`;
  };

  ui.sidebarPages = [
    { id: 'inbox', icon: 'tray-full', label: 'Inbox' },
    { id: 'todos', icon: 'checklist', label: 'To-dos', countLook: 'needs' },
    { id: 'scheduled', icon: 'alarm', label: 'Scheduled' },
    { id: 'knowledge', icon: 'books', label: 'Knowledge' },
    { id: 'health', icon: 'heart', label: 'Health' },
    { id: 'machines', icon: 'laptop-phone', label: 'Machines' },
  ];

  /** Persona's sidebar. o: { selected ('persona' | 'inbox' | 'todos' | …), counts: { inbox, todos, … }, children: { todos: [sbRow opts] }
      (a disclosure under a row), open: { todos: true }, chats: [{ title, time }], go: { todos: 'frameId', … }, search, foot (true),
      header (true), collapse (true), name, status, after (HTML after the pages) } */
  ui.sidebar = function sidebar(o) {
    o = o || {};
    const counts = o.counts || {};
    const goes = o.go || {};
    const kids = o.children || {};
    const open = o.open || {};
    const rows = [];
    rows.push(ui.sbRow({ mark: ui.face(24), label: 'Persona', sub: o.mainSub || 'Main chat', selected: o.selected === 'persona', go: goes.persona }));
    rows.push(`<div class="k-sb-sec"><span>Pages</span>${I('chevron-down', 10)}</div>`);
    (o.pages || ui.sidebarPages).forEach((p) => {
      const hasKids = kids[p.id] && kids[p.id].length;
      rows.push(ui.sbRow({
        icon: p.icon, label: p.label, count: counts[p.id], countLook: p.countLook || 'plain',
        selected: o.selected === p.id, go: goes[p.id],
        disclosure: hasKids ? (open[p.id] === false ? 'closed' : 'open') : p.disclosure,
      }));
      if (hasKids && open[p.id] !== false) kids[p.id].forEach((k) => rows.push(ui.sbRow(Object.assign({ indent: 1 }, k))));
    });
    if (o.after) rows.push(o.after);
    const chats = o.chats === false ? [] : o.chats || [
      { title: 'Weekend plans', time: '2h' },
      { title: 'Reading notes for May', time: 'Yesterday' },
      { title: 'Ideas for the garden', time: 'Mon' },
    ];
    if (chats.length) {
      rows.push(`<div class="k-sb-sec"><span>Persona chats</span>${I('chevron-down', 10)}<span class="k-sb-sec-btn">${I('square-pencil', 15)}</span></div>`);
      chats.forEach((c) => rows.push(ui.sbRow({ icon: 'bubble', label: c.title, time: c.time, go: c.go, selected: c.selected })));
    }
    return `<div class="k-sb${o.cls ? ' ' + o.cls : ''}"${o.width ? ` style="width:${o.width}px"` : ''}>
      ${o.header === false ? '' : `<div class="k-sb-head${o.wc ? ' has-wc' : ''}">${ui.face(34, { state: o.faceState })}<span class="k-sb-who"><b>${o.name || 'Persona'}</b><span>${o.status || 'Online'}</span></span>${I('chevron-down', 11)}<span class="k-sb-spacer"></span>${o.collapse === false ? '' : ui.btn({ icon: 'sidebar-left', look: 'plain', size: 's' })}</div>`}
      ${o.search === false ? '' : `<div class="k-sb-search">${ui.search({ placeholder: 'Search', size: 's', kbd: o.kbd })}</div>`}
      <div class="k-sb-scroll">${rows.join('')}</div>
      ${o.foot === false ? '' : `<div class="k-sb-foot">${ui.btn({ icon: 'square-pencil', label: 'New Chat', size: 's' })}<span class="k-sb-spacer"></span>${ui.btn({ icon: 'gear', size: 's', title: 'Settings' })}</div>`}
    </div>`;
  };

  /* ------------------------------------------------------------ misc */

  /** Empty state. o: { icon, title, body, action: { label, go } } */
  ui.empty = (o) => `<div class="k-empty"><span class="k-empty-ic">${I(o.icon || 'checklist', 26)}</span><div class="k-empty-title">${o.title || ''}</div>${o.body ? `<div class="k-empty-body">${o.body}</div>` : ''}${o.action ? ui.btn(Object.assign({ look: 'tinted', size: 's' }, o.action)) : ''}</div>`;
  ui.divider = () => '<div class="k-divider"></div>';
  /** A hairline-separated block with a small heading, for detail pages. */
  ui.block = (title, html, o) => `<section class="k-block">${title ? ui.section(title, o) : ''}${html}</section>`;

  /** iOS keyboard (drawn by Kit.device when a frame sets keyboard: true or { … }). o: { height (336 on iPhone, ≈ 300 on the
      Duo), wide, predictive (true: the 44-pt predictive bar), suggestions, label ('keyboard ≈ 300 pt (to measure)') } */
  ui.keyboard = function keyboard(o) {
    o = o || {};
    const sugg = o.suggestions || ['onsen', 'only', 'one'];
    const key = (t, cls) => `<span class="k-key${cls ? ' ' + cls : ''}">${t}</span>`;
    const row = (letters) => letters.split('').map((c) => key(c)).join('');
    return `<div class="k-kb${o.wide ? ' is-wide' : ''}" style="height:${o.height || 336}px">
      ${o.predictive === false ? '<div class="k-kb-nosugg"></div>' : `<div class="k-kb-sugg">${(o.label ? sugg.slice(0, 2) : sugg).map((s, i) => `<span>${i === 0 ? `“${s}”` : s}</span>`).join('<i></i>')}${o.label ? `<i></i><span class="k-kb-label">${o.label}</span>` : ''}</div>`}
      <div class="k-kb-rows">
        <div class="k-kb-row">${row('qwertyuiop')}</div>
        <div class="k-kb-row is-mid">${row('asdfghjkl')}</div>
        <div class="k-kb-row">${key(I('shift', 20), 'is-fn is-wide')}<span class="k-kb-gap"></span>${row('zxcvbnm')}<span class="k-kb-gap"></span>${key(I('delete-left', 21), 'is-fn is-wide')}</div>
        <div class="k-kb-row">${key('123', 'is-fn is-wider')}${key('', 'is-space')}${key('return', 'is-fn is-wider')}</div>
      </div>
      <div class="k-kb-foot">${I('face-smile', 25)}${I('mic', 23)}</div>
      ${o.label && o.predictive === false ? `<span class="k-kb-label is-corner">${o.label}</span>` : ''}
    </div>`;
  };
})();

/*
 * ===================================================================================== To-dos components
 * The shared components of the To-dos redesign (synthesis §3.0.4), used by more than one variant. Each takes plain
 * options; the …For(…) adapters build those options from window.Data (data/todos.js) so every variant shows the same
 * words and numbers. Clickable options take { go, transition, toast } (see Kit.navAttrs). Signatures: README.md.
 */
(function () {
  const Kit = window.Kit;
  const ui = Kit.ui;
  const I = (n, s, o) => Kit.icon(n, s, o);
  const J = Kit.join;
  const nav = (o) => Kit.navAttrs(o);
  const arr = (x) => (x == null || x === false ? [] : Array.isArray(x) ? x : [x]);
  const D = () => window.Data || {};
  const asNav = (x) => (x == null ? {} : typeof x === 'string' ? { go: x } : x);

  /* ---------------------------------------------------------------- phases, marks */

  // Phase → state word (sentence case, synthesis §2.11) and tone. Data.words overrides the words when present.
  ui.phases = {
    needs_you: { word: 'Needs you', tone: 'needs' },
    working: { word: 'Working', tone: 'working' },
    ready: { word: 'Just finished', tone: 'ready' },
    waiting: { word: 'Waiting', tone: 'idle' },
    todo: { word: 'To do', tone: 'idle' },
    someday: { word: 'Someday', tone: 'idle' },
    done: { word: 'Done', tone: 'idle' },
  };
  const phaseOf = (p) => {
    const base = ui.phases[p] || { word: p, tone: ui.toneOf ? ui.toneOf(p) : 'idle' };
    const w = D().words && D().words[p];
    return w ? Object.assign({}, base, { word: w }) : base;
  };

  /** Line-2 mark: 'needs' (blue dot 9) · 'working' (the amber arc) · 'ready' (green ✓) · 'waiting' (an idle clock ◷) ·
      'done' (gray ✓) · 'danger' (red dot). Anything else: none. */
  ui.lineMark = function lineMark(mark, size) {
    switch (mark) {
      case 'needs': return `<span class="k-mark k-mark--needs" style="--s:${size || 9}px"></span>`;
      case 'working': return ui.spinner(size || 12);
      case 'ready': return `<span class="k-lm k-lm--ready">${I('checkmark', size || 13)}</span>`;
      case 'waiting': return `<span class="k-lm k-lm--waiting">${I('clock', size || 13)}</span>`;
      case 'done': return `<span class="k-lm k-lm--done">${I('checkmark', size || 13)}</span>`;
      case 'danger': return `<span class="k-mark k-mark--danger" style="--s:${size || 8}px"></span>`;
      default: return '';
    }
  };

  /** The tick: a 24-pt circle in a 44-pt hit area. state: 'open' | 'done' (filled ✓). o: { go, transition, toast, label } */
  ui.tick = (state, o) => `<span class="k-tick-hit"${nav(o)} role="checkbox" aria-checked="${state === 'done'}"${o && o.label ? ` aria-label="${Kit.esc(o.label)}"` : ''}><span class="k-tick${state === 'done' ? ' is-done' : ''}">${state === 'done' ? I('checkmark-circle-fill', 24) : ''}</span></span>`;

  /* ---------------------------------------------------------------- TodoRow */

  function answerRow(a, o) {
    if (!a) return '';
    const kind = a.kind || 'ok';
    const btn = `<span class="k-ans k-ans--${kind}"${nav(a)}><span>${a.label}</span>${kind === 'door' ? I('chevron-right', 12) : ''}</span>`;
    const opt = a.options ? `<span class="k-ans-opt${a.open ? ' is-open' : ''}"${nav(asNav(a.options === true ? null : a.options))}><span>Options</span>${I(a.open ? 'chevron-up' : 'chevron-down', 11)}</span>` : '';
    return `<div class="k-tr-ans">${btn}${opt}</div>`;
  }

  /**
   * TodoRow (synthesis §2.4 anatomy). Heights: 52 one-line, 72 two-line, +44 with answers, +20 with line 3.
   * o: { title, lead: 'tick' (default) | 'done' | 'none' | HTML (a pluggable leading slot, e.g. V3's living mark), tickNav,
   *      mark (line 2: 'needs' | 'working' | 'ready' | 'waiting' | 'done' | 'danger'), lineLead (the toned part, e.g. the
   *      ask's name), line (the rest, --ink-2), tone ('needs' | 'working' | 'ready' | 'danger' | 'idle'), toneAll (tone the
   *      whole line), lineHtml (replaces line 2), answered ('Ryokan Hanami': line 2 becomes "✓ You chose … · Undo" and the
   *      answers go), answer: { kind: 'ok' | 'door' | 'reply', label, options (true | { go }), open, go, transition, toast },
   *      line3, trailing ('Apr 3', '16m'), unseen (a 7-pt dot before the trailing text), priority, ticked (strike-through),
   *      selected (fill-well + the 3-pt accent bar ▐), swiped (Done · Someday revealed), below (HTML under the row, e.g. the
   *      unfolded ask card), go, transition, toast, cls }
   */
  ui.todoRow = function todoRow(o) {
    o = o || {};
    const done = o.lead === 'done' || o.ticked;
    let lead = '';
    if (o.lead === 'none') lead = '';
    else if (o.lead && o.lead !== 'tick' && o.lead !== 'done') lead = `<span class="k-tr-lead">${o.lead}</span>`;
    else lead = `<span class="k-tr-lead">${ui.tick(done ? 'done' : 'open', Object.assign({ label: 'Complete, ' + String(o.title || '').replace(/<[^>]+>/g, '') }, o.tickNav))}</span>`;
    let line2 = '';
    if (o.answered) {
      line2 = `<div class="k-tr-line k-tr-answered">${I('checkmark', 13)}<span class="k-tr-text">You chose ${o.answered}</span><span class="k-sep">·</span><span class="k-tr-undo"${nav(o.undo)}>Undo</span></div>`;
    } else if (o.lineHtml) {
      line2 = `<div class="k-tr-line">${o.lineHtml}</div>`;
    } else if (o.lineLead || o.line) {
      const tone = o.tone ? ` k-tone--${o.tone}` : '';
      line2 = `<div class="k-tr-line${o.toneAll ? ' is-toned' + tone : ''}">${ui.lineMark(o.mark)}<span class="k-tr-text">${o.lineLead ? `<span class="k-tr-lt${tone}">${o.lineLead}</span>` : ''}${o.lineLead && o.line ? '<span class="k-sep">·</span>' : ''}${o.line || ''}</span></div>`;
    }
    const trail = o.trailing || o.unseen ? `<span class="k-tr-trail">${o.unseen ? '<i class="k-tr-unseen" title="Unseen"></i>' : ''}${o.trailing || ''}</span>` : '';
    const main = `<div class="k-tr-main">
        <div class="k-tr-head"><span class="k-tr-title">${o.title || ''}</span>${o.priority ? `<span class="k-tr-flag">${I('flag-fill', 13)}</span>` : ''}${trail}</div>
        ${line2}
        ${o.answered ? '' : answerRow(o.answer, o)}
        ${o.line3 ? `<div class="k-tr-l3">${o.line3}</div>` : ''}
        ${o.below || ''}
      </div>`;
    const swipe = o.swiped ? `<span class="k-tr-swipe"><span class="k-tr-sw is-done">${I('checkmark', 17)}<b>Done</b></span><span class="k-tr-sw is-later">${I('clock', 17)}<b>Someday</b></span></span>` : '';
    return `<div class="${Kit.cls('k-tr', o.selected && 'is-selected', o.ticked && 'is-ticked', done && 'is-done', lead === '' && 'no-lead', o.swiped && 'is-swiped', o.cls)}"${nav(o)}>
      <div class="k-tr-slide">${lead}${main}</div>${swipe}
    </div>`;
  };

  /** Row options from a Data to-do (override any of them with o). */
  ui.rowProps = function rowProps(t, o) {
    if (!t) return o || {};
    const r = t.row || {};
    return Object.assign({
      title: t.title, lead: t.closed ? 'done' : 'tick', ticked: !!t.ticked,
      mark: r.mark, lineLead: r.lead, line: r.text, tone: r.tone, toneAll: r.all,
      trailing: t.trailing, unseen: !!t.unseen, priority: false,
      answer: t.answer ? Object.assign({}, t.answer) : null,
      line3: t.line3,
    }, o || {});
  };
  /** TodoRow for a Data to-do: ui.todoRowFor(Data.todo('t01'), { selected: true, go: 'iphone/kyoto' }) */
  ui.todoRowFor = (t, o) => ui.todoRow(ui.rowProps(t, o));
  /** A plain list of rows (no inset). */
  ui.todoList = (rows, o) => `<div class="k-tl${o && o.cls ? ' ' + o.cls : ''}">${J(arr(rows))}</div>`;

  /* ---------------------------------------------------------------- section header, fold row, section bar */

  /** SectionHeader: sentence case, 13 semibold, top padding 16, height 32. The count trails: look 'needs' = the blue
      CountBadge; 'neutral' = a plain number in --ink-2; 'none' = no count. o: { count, look, tone, lit (lit for 1 s after a
      summary tap), fold ('open' | 'closed'), trailing, go, transition, toast } */
  ui.sectionHeader = function sectionHeader(title, o) {
    o = o || {};
    const look = o.look || 'neutral';
    const count = o.count == null || look === 'none' ? '' : look === 'needs' ? ui.badge(o.count, { look: 'needs' }) : `<span class="k-sh-n">${o.count}</span>`;
    return `<div class="${Kit.cls('k-sh', o.lit && 'is-lit', o.tone && 'k-tone--' + o.tone, o.tone && 'is-toned')}"${nav(o)}>${o.fold ? I(o.fold === 'open' ? 'chevron-down' : 'chevron-right', 11) : ''}<span class="k-sh-title">${title}</span><span class="k-sh-trail">${o.trailing || ''}${count}</span></div>`;
  };
  ui.sectionHeaderFor = (sec, o) => ui.sectionHeader(sec.title, Object.assign({ count: sec.count, look: sec.look }, o));

  /** A folded section as one line: "Can wait · 1 ›", "Someday · 1 ›", "Done Today · 2 ›". o: { count, open, indent (true:
      aligned with the titles), go, transition, toast } */
  ui.foldRow = (label, o) => {
    o = o || {};
    return `<div class="k-frow${o.indent === false ? '' : ' is-indent'}${o.open ? ' is-open' : ''}"${nav(o)}><span>${label}${o.count != null ? ` · ${o.count}` : ''}</span>${I(o.open ? 'chevron-down' : 'chevron-right', 12)}</div>`;
  };

  /** SectionBar: the Inbox's CapsuleFilterBar. Chips 32 tall, padding 12, gap 8, one row that fades at the cut edge;
      the selected chip is filled with --shell-ink. items: [{ id, label, count, look ('needs' = the blue CountBadge), go, … }]
      o: { selected (id or index), trailing (HTML, e.g. a ⋯ button), cls } */
  ui.sectionBar = function sectionBar(items, o) {
    o = o || {};
    const list = arr(items);
    const sel = typeof o.selected === 'number' ? o.selected : Math.max(0, list.findIndex((x) => x.id === (o.selected || 'all')));
    // A choice is its 44 × 44 tap target (.k-sbar-chip) around the visible pill (.k-sbar-pill), as CapsuleFilterItem is;
    // the trailing control follows the hairline the bar draws, as CapsuleFilterBar's showsTrailing does.
    return `<div class="k-sbar${o.cls ? ' ' + o.cls : ''}"><div class="k-sbar-in">${list.map((it, i) => `<span class="k-sbar-chip${i === sel ? ' is-on' : ''}"${nav(it)}><span class="k-sbar-pill"><span>${it.label}</span>${it.count != null ? (it.look === 'needs' ? ui.badge(it.count, { look: 'needs' }) : `<span class="k-sbar-n">${it.count}</span>`) : ''}</span></span>`).join('')}</div>${o.trailing ? `<i class="k-sbar-sep"></i><span class="k-sbar-trail">${o.trailing}</span>` : ''}</div>`;
  };
  /** SectionBar from Data.bar(place). o: { place, selected, go: { all: 'frameId', needs: …, … }, trailing } */
  ui.sectionBarFor = (o) => {
    o = o || {};
    const goes = o.go || {};
    return ui.sectionBar(D().bar(o.place || 'all').map((x) => Object.assign({}, x, asNav(goes[x.id]))), o);
  };

  /* ---------------------------------------------------------------- menus, title menu */

  /** A system menu (iOS 26 look). items: [{ label, sub, check, icon, count, look, danger, disabled, go, transition, toast },
      '-' (divider), { header }] o: { align: 'center' | 'leading' | 'trailing', top, width (250), style, cls } */
  ui.menu = function menu(items, o) {
    o = o || {};
    const style = J([o.width ? `width:${o.width}px` : '', o.top != null ? `top:${o.top}px` : '', o.style || ''], ';');
    return `<div class="${Kit.cls('k-menu', 'is-' + (o.align || 'center'), o.cls)}"${style ? ` style="${style}"` : ''}>${arr(items).map((it) => {
      if (it === '-' || (it && it.divider)) return '<div class="k-menu-div"></div>';
      if (typeof it === 'string') it = { label: it };
      if (it.header) return `<div class="k-menu-head">${it.header}</div>`;
      return `<div class="${Kit.cls('k-menu-item', it.danger && 'is-danger', it.disabled && 'is-dim')}"${nav(it)}><span class="k-menu-check">${it.check ? I('checkmark', 15) : ''}</span><span class="k-menu-label"><span>${it.label}</span>${it.sub ? `<small>${it.sub}</small>` : ''}</span>${it.count != null ? ui.badge(it.count, { look: it.look || 'needs' }) : ''}${it.icon ? `<span class="k-menu-ic">${I(it.icon, 18)}</span>` : ''}</div>`;
    }).join('')}</div>`;
  };

  /** The places as menu items: All To-dos · Today · Upcoming · ─ · each project with its blue part of N · All Projects…
      o: { selected (place id), go: { all, today, …, projects }, extra: [items appended after a divider] } */
  ui.placeMenuItems = (o) => {
    o = o || {};
    const goes = o.go || {};
    const c = D().counts || {};
    const sel = o.selected || 'all';
    const items = [
      Object.assign({ label: 'All To-dos', check: sel === 'all' }, asNav(goes.all)),
      Object.assign({ label: 'Today', check: sel === 'today' }, asNav(goes.today)),
      Object.assign({ label: 'Upcoming', check: sel === 'upcoming' }, asNav(goes.upcoming)),
      '-',
    ].concat((D().projects || []).map((p) => Object.assign({ label: p.name, check: sel === p.id, count: (c.byProject || {})[p.id] || null }, asNav(goes[p.id]))))
      .concat([Object.assign({ label: 'All Projects…' }, asNav(goes.projects))]);
    return o.extra ? items.concat(['-'], o.extra) : items;
  };

  /** TitleMenu: the inline title + ▾ (17 semibold); open: true draws the menu under it. o: { title, open, items (default
      ui.placeMenuItems(o)), selected, align, go, transition, toast, menuGo } */
  ui.titleMenu = function titleMenu(o) {
    o = o || {};
    return `<span class="k-tm${o.open ? ' is-open' : ''}"><span class="k-tm-btn"${nav(o)}><span class="k-tm-title">${o.title || 'All To-dos'}</span>${I('chevron-down', 12)}</span>${o.open ? ui.menu(o.items || ui.placeMenuItems(o), { align: o.align || 'center', cls: 'k-tm-menu', width: o.width }) : ''}</span>`;
  };

  /* ---------------------------------------------------------------- asks */

  const btnNav = (b) => (typeof b === 'string' ? { label: b } : b || {});

  /**
   * AskCard, P1 layout: --card, radius 14, padding 16. The title (heading 17) with a 9-pt needs dot, or "From <run>" over
   * the title for a door; the body (secondary 15, ≤ 3 lines); then a row of at most 2 equal secondary buttons (44, tinted
   * --fill-chip), the primary full width (44, accent fill), and the link row "Later · Don’t handle · Discuss…".
   * o: { title, from (door: the run's title), body, prices, plan: [lines], estimate, secondary: [{ label, go, … }] (≤ 2),
   *      primary: { label, chevron, go, … }, links: [{ label, go, … }], answered ('You chose Ryokan Hanami'), undo,
   *      later ('later today': folds to one line), compact (no body), go }
   */
  ui.askCard = function askCard(o) {
    o = o || {};
    if (o.answered) {
      return `<div class="k-ac is-answered">${I('checkmark-circle-fill', 18)}<span class="k-ac-ans">${o.answered}</span><span class="k-sep">·</span><span class="k-ac-undo"${nav(o.undo)}>Undo</span></div>`;
    }
    if (o.later) {
      return `<div class="k-ac is-later"${nav(o)}>${ui.lineMark('needs')}<span class="k-ac-lt">${o.title}</span><span class="k-ac-ls">· ${o.later}</span>${I('chevron-right', 12)}</div>`;
    }
    const sec = arr(o.secondary).slice(0, 2).map(btnNav);
    const pri = o.primary ? btnNav(o.primary) : null;
    const links = arr(o.links).map(btnNav);
    return `<div class="k-ac${o.from ? ' is-door' : ''}${o.compact ? ' is-compact' : ''}"${nav(o)}>
      ${o.from ? `<div class="k-ac-from">From ${o.from}</div>` : ''}
      <div class="k-ac-title">${o.from ? '' : ui.lineMark('needs')}<span>${o.title || ''}</span></div>
      ${o.body && !o.compact ? `<div class="k-ac-body">${o.body}</div>` : ''}
      ${o.prices && !o.compact ? `<div class="k-ac-prices">${o.prices}</div>` : ''}
      ${o.plan ? `<ol class="k-ac-plan">${arr(o.plan).map((p, i) => `<li><span>${i + 1}</span>${p}</li>`).join('')}</ol>` : ''}
      ${o.estimate ? `<div class="k-ac-est">${o.estimate}</div>` : ''}
      ${sec.length ? `<div class="k-ac-row">${sec.map((b) => `<span class="k-ac-btn"${nav(b)}><span>${b.label}</span></span>`).join('')}</div>` : ''}
      ${pri ? `<span class="k-ac-btn is-primary${sec.length ? '' : ' is-first'}"${nav(pri)}><span>${pri.label}</span>${pri.chevron ? I('chevron-right', 13) : ''}</span>` : ''}
      ${links.length ? `<div class="k-ac-links">${links.map((l) => `<span${nav(l)}>${l.label}</span>`).join('<i>·</i>')}</div>` : ''}
    </div>`;
  };

  /**
   * AskCard from a Data ask. o: { as: 'home' | 'door' (default: a run's ask is a door on the paper, a to-do's ask its
   * home), on: { <option id> | primary | later | dont | discuss | door | edit | dontStart | approve | other | undo |
   * laterOpen: nav }, answered (true | 'You chose Kaede'), later, compact, narrow (the primary without "· Recommended") }
   */
  ui.askCardFor = function askCardFor(ask, o) {
    o = o || {};
    if (typeof ask === 'string') ask = D().ask(ask);
    if (!ask) return '';
    const on = o.on || {};
    const exitKey = { 'Later': 'later', 'Don’t handle': 'dont', 'Discuss…': 'discuss' };
    const links = (ask.exits || []).map((x) => Object.assign({ label: x }, asNav(on[exitKey[x]])));
    if (o.answered) return ui.askCard({ answered: o.answered === true ? (ask.approvedText || 'Answered') : o.answered, undo: asNav(on.undo) });
    if (o.later) return ui.askCard(Object.assign({ title: ask.title, later: ask.later || 'later' }, asNav(on.laterOpen)));
    if (ask.kind === 'plan') {
      const door = o.as ? o.as === 'door' : true;
      if (door) {
        return ui.askCard({ from: D().run(ask.run) ? D().run(ask.run).title : 'an Agent', title: ask.title, body: ask.summary, compact: o.compact,
          primary: Object.assign({ label: ask.door, chevron: true }, asNav(on.door)), links });
      }
      return ui.askCard({ title: ask.title, plan: ask.plan, estimate: ask.estimate,
        secondary: (ask.secondary || []).map((x) => Object.assign({ label: x }, asNav(on[x === 'Edit' ? 'edit' : 'dontStart']))),
        primary: Object.assign({ label: ask.primary }, asNav(on.approve)) });
    }
    if (ask.options) {
      const rec = ask.options.find((x) => x.recommended) || ask.options[0];
      return ui.askCard({ title: ask.title, body: ask.why, prices: ask.prices, compact: o.compact,
        secondary: ask.options.filter((x) => x !== rec).map((x) => Object.assign({ label: x.label }, asNav(on[x.id]))),
        primary: Object.assign({ label: o.narrow ? rec.label : rec.primaryLabel || rec.label }, asNav(on[rec.id] || on.primary)), links });
    }
    return ui.askCard({ title: ask.title, body: ask.why, compact: o.compact,
      secondary: (ask.secondary || []).map((x) => Object.assign({ label: x }, asNav(on.other))),
      primary: Object.assign({ label: ask.primary }, asNav(on.primary)), links });
  };

  /** The ask folded to one line (44): "● Which ryokan? [OK: Hanami] Options ⌄", or a door "● Plan awaiting approval
      [Review Plan ›]". o: { title, word (a state word before it), mark ('needs' | HTML), ok: { label, go, … },
      door: { label, go, … }, options: true | { go, … }, cls } */
  ui.askLine = function askLine(o) {
    o = o || {};
    const mark = o.mark === undefined || o.mark === 'needs' ? ui.lineMark('needs') : o.mark || '';
    const ok = o.ok ? `<span class="k-ans k-ans--ok"${nav(btnNav(o.ok))}><span>${btnNav(o.ok).label}</span></span>` : '';
    const door = o.door ? `<span class="k-ans k-ans--door"${nav(btnNav(o.door))}><span>${btnNav(o.door).label}</span>${I('chevron-right', 12)}</span>` : '';
    const opt = o.options ? `<span class="k-ans-opt"${nav(o.options === true ? null : o.options)}><span>Options</span>${I('chevron-down', 11)}</span>` : '';
    return `<div class="k-al${o.cls ? ' ' + o.cls : ''}"><span class="k-al-t">${mark}${o.word ? `<span class="k-al-w">${o.word}</span><span class="k-sep">·</span>` : ''}<span class="k-al-q">${o.title || ''}</span></span>${ok}${door}${opt}</div>`;
  };
  /** askLine from a Data to-do's open ask (null when it has none). o: { word, ok, options, door } override */
  ui.askLineFor = (t, o) => {
    if (!t || !t.asks || !t.asks.length) return '';
    const ask = D().ask(t.asks[0]);
    const a = t.answer || {};
    if (ask.kind === 'plan') return ui.askLine(Object.assign({ title: ask.title, door: { label: ask.door } }, o));
    return ui.askLine(Object.assign({ title: ask.title, ok: { label: a.short || a.label }, options: true }, o));
  };

  /** AskPointer: one line, no buttons, drawn once while the request's home is on screen.
      "Plan awaiting approval · answer in the conversation →" o: { go, transition, toast } */
  ui.askPointer = (text, o) => {
    const parts = String(text || '').split(' · ');
    return `<div class="k-ap"${nav(o)}>${ui.lineMark('needs')}<span><span class="k-ap-t">${parts[0]}</span>${parts.length > 1 ? `<span class="k-sep">·</span>${parts.slice(1).join(' · ')}` : ''}</span></div>`;
  };

  /* ---------------------------------------------------------------- paper pieces */

  /** StateLine: the state word in its tone · the hub's sentence in --ink-2, secondary 15, ≤ 2 lines.
      ui.stateLine('needs_you', 'Persona found 3 ryokans near Gion', { word, mark, clamp (2), cls }) */
  ui.stateLine = function stateLine(phase, text, o) {
    o = o || {};
    const ph = phaseOf(phase);
    const word = o.word != null ? o.word : ph.word;
    return `<div class="k-stl k-tone--${o.tone || ph.tone}${o.clamp === 1 ? ' is-one' : ''}${o.cls ? ' ' + o.cls : ''}">${o.mark ? `${ui.lineMark(o.mark === true ? (ph.tone === 'needs' ? 'needs' : ph.tone === 'working' ? 'working' : '') : o.mark)} ` : ''}${word ? `<span class="k-stl-w">${word}</span>` : ''}${word && text ? '<span class="k-sep">·</span>' : ''}${text ? `<span class="k-stl-t">${text}</span>` : ''}</div>`;
  };
  ui.stateLineFor = (t, o) => (t && t.state ? ui.stateLine(t.phase, (o && o.short && t.state.short) || t.state.text, Object.assign({ word: t.state.word }, o)) : '');

  /** RecapLine: "Since you looked: flights held · 1 question" (caption 13, --ink-2). o: { go, … } */
  ui.recapLine = (text, o) => (text ? `<div class="k-recap"${nav(o)}>Since you looked: ${text}</div>` : '');

  /** Tick + title of the paper (title 22 semibold). o: { lead ('tick' | 'done' | HTML), title, tickNav } */
  ui.paperTitle = (o) => {
    o = o || {};
    const lead = o.lead && o.lead !== 'tick' && o.lead !== 'done' ? o.lead : ui.tick(o.lead === 'done' ? 'done' : 'open', o.tickNav);
    return `<div class="k-pt"><span class="k-pt-lead">${lead}</span><span class="k-pt-title">${o.title || ''}</span></div>`;
  };

  /** Chips: capsules 32 tall on --fill-chip, icon 14 + label 15; "+" is a 32 circle. items: [{ label, icon, hue (colors
      the icon), tone, dashed, clear (a trailing ✕), go, … }] o: { add (true | nav) } */
  ui.todoChips = function todoChips(items, o) {
    o = o || {};
    return `<div class="k-tch">${arr(items).map((c) => `<span class="${Kit.cls('k-tch-c', c.dashed && 'is-dashed', c.tone && 'k-tone--' + c.tone, c.tone && 'is-toned', c.outline && 'is-outline')}"${nav(c)}${c.hue ? ` style="--hue:var(--hue-${c.hue})"` : ''}>${c.icon ? I(c.icon, 14) : ''}<span>${c.label}</span>${c.clear ? I('xmark', 11) : ''}</span>`).join('')}${o.add ? `<span class="k-tch-add"${nav(o.add === true ? null : o.add)} title="Add">${I('plus', 15)}</span>` : ''}</div>`;
  };
  ui.chipsFor = (t, o) => ui.todoChips(t.chips || [], Object.assign({ add: !t.closed }, o));

  /** Notes: live text (no Edit mode), or the one-line placeholder "Add Notes". o: { placeholder, go, … } */
  ui.notes = (text, o) => (text ? `<div class="k-notes"${nav(o)}>${text}</div>` : `<div class="k-notes is-empty"${nav(o)}>${(o && o.placeholder) || 'Add Notes'}</div>`);

  /** "What you asked · Done when 1 of 3 ›" (folded), or open with the brief: o: { text (the brief: open form, 3 lines + More),
      doneWhen ('1 of 3'), go, … } */
  ui.briefLine = (o) => {
    o = o || {};
    if (o.text) {
      return `<div class="k-brief"><div class="k-blk-h">What you asked</div><div class="k-brief-t">${o.text}</div><div class="k-brief-dw"${nav(o)}><span>Done when · ${o.doneWhen || ''}</span>${I('chevron-right', 12)}</div></div>`;
    }
    return `<div class="k-brief-line"${nav(o)}><span>What you asked</span>${o.doneWhen ? `<span class="k-sep">·</span><span>Done when ${o.doneWhen}</span>` : ''}${I('chevron-right', 12)}</div>`;
  };
  ui.briefLineFor = (t, o) => (t && t.brief ? ui.briefLine(Object.assign({ doneWhen: t.doneWhen ? `${t.doneWhen.met} of ${t.doneWhen.total}` : '' }, o)) : '');

  /** "Persona can help · ✕" with [Break Into Steps] [Do It for Me]: a fresh to-do with no runs and no steps.
      o: { steps: nav, doIt: nav, close: nav } */
  ui.helpCard = (o) => {
    o = o || {};
    return `<div class="k-help"><div class="k-help-h"><span>${I('sparkle', 14)}Persona can help</span><span class="k-help-x"${nav(o.close)}>${I('xmark', 13)}</span></div><div class="k-help-b"><span class="k-ac-btn"${nav(o.steps)}><span>Break Into Steps</span></span><span class="k-ac-btn"${nav(o.doIt)}><span>Do It for Me</span></span></div></div>`;
  };

  /** A block header on the paper: "Steps · · · 2 of 5" (13 semibold; the trailing part in --ink-2). */
  ui.blockHead = (title, trailing, o) => `<div class="k-blk-h"${nav(o)}><span>${title}</span>${trailing ? `<span class="k-blk-tr">${trailing}</span>` : ''}</div>`;

  /* ---------------------------------------------------------------- steps */

  function stepCircle(state) {
    switch (state) {
      case 'done': return `<span class="k-stc is-done">${I('checkmark-circle-fill', 21)}</span>`;
      case 'needs': return '<span class="k-stc is-needs"><i></i></span>';
      case 'working': return `<span class="k-stc is-working">${ui.spinner(20)}</span>`;
      case 'waiting': return `<span class="k-stc is-waiting">${I('clock', 21)}</span>`;
      case 'suggested': return '<span class="k-stc is-suggested"></span>';
      default: return '<span class="k-stc is-idle"></span>';
    }
  }
  /** StepsList: rows 44, a 20-pt circle (✓ positive, ◉ needs, ◐ working, ◷ waiting, ○ idle) + text 17 + "· who" (caption);
      a step with a run shows its state word in its tone. Suggested steps: dashed circles, italic, then [Keep Steps] [Edit].
      steps: [{ text, state: 'done' | 'needs' | 'working' | 'waiting' | 'idle', who, word, go, … }]
      o: { title ('Steps'; false for none), count ('2 of 5'), limit (5, then "Show All N Steps"), suggested: [text…],
           keep: nav, edit: nav, all: nav, headNav, compact (rows 40) } */
  ui.stepsList = function stepsList(steps, o) {
    o = o || {};
    const list = arr(steps);
    const limit = o.limit || 5;
    const shown = list.slice(0, limit);
    const wordTone = { needs: 'needs', working: 'working', done: 'ready', waiting: 'idle', idle: 'idle' };
    const row = (s) => `<div class="k-st${s.state === 'done' ? ' is-done' : ''}${o.compact ? ' is-compact' : ''}"${nav(s)}>${stepCircle(s.state)}<span class="k-st-t"><span class="k-st-x">${s.text}</span>${s.who ? `<span class="k-st-who"> · ${s.who}</span>` : ''}${s.word ? `<span class="k-st-word k-tone--${wordTone[s.state] || 'idle'}"> · ${s.word}</span>` : ''}</span>${s.chevron ? I('chevron-right', 12) : ''}</div>`;
    const sugg = arr(o.suggested);
    return `<div class="k-steps2">
      ${o.title === false ? '' : ui.blockHead(o.title || 'Steps', o.count || '', o.headNav)}
      <div class="k-st-list">${shown.map(row).join('')}</div>
      ${list.length > limit ? `<div class="k-st-all"${nav(o.all)}>Show All ${list.length} Steps</div>` : ''}
      ${sugg.length ? `<div class="k-st-sugg">${sugg.map((t) => `<div class="k-st is-suggested">${stepCircle('suggested')}<span class="k-st-t"><span class="k-st-x">${t}</span></span></div>`).join('')}<div class="k-st-keep"><span class="k-ac-btn is-tinted"${nav(o.keep)}><span>Keep Steps</span></span><span class="k-ac-btn"${nav(o.edit)}><span>Edit</span></span></div></div>` : ''}
    </div>`;
  };
  ui.stepsFor = (t, o) => {
    const steps = (t && t.steps) || [];
    if (!steps.length && !(o && o.suggested)) return '';
    const done = steps.filter((s) => s.state === 'done').length;
    return ui.stepsList(steps, Object.assign({ count: steps.length ? `${done} of ${steps.length}` : '' }, o));
  };
  /** One-line steps summary: "Steps 2 of 5 · Results 2" or with marks "Steps 2/5 ✓ ✓ ◉ ○ ○ ›". o: { results, marks, go } */
  ui.stepsSummary = (t, o) => {
    o = o || {};
    const steps = (t && t.steps) || [];
    const done = steps.filter((s) => s.state === 'done').length;
    const marks = o.marks ? `<span class="k-ss-m">${steps.map((s) => stepCircle(s.state)).join('')}</span>` : '';
    return `<div class="k-ss"${nav(o)}><span>Steps ${done} of ${steps.length}</span>${marks}${o.results != null ? `<span class="k-sep">·</span><span>Results ${o.results}</span>` : ''}${I('chevron-right', 12)}</div>`;
  };

  /* ---------------------------------------------------------------- results, work details */

  /** ResultsStrip: chips 36 tall on --card, "▤ label · outcome" (doc) or "⤴ …" (change), one row that fades.
      results: [{ kind: 'doc' | 'change', label, outcome, go, … }] o: { title ('Results'; false for none), inline (true: the
      title on the same row) } */
  ui.resultsStrip = function resultsStrip(results, o) {
    o = o || {};
    const list = arr(results);
    if (!list.length) return '';
    const chips = list.map((r) => `<span class="k-res-c"${nav(r)}>${I(r.kind === 'change' ? 'arrow-turn-up-right' : 'doc', 14)}<span class="k-res-l">${r.label}</span>${r.outcome ? `<span class="k-res-o">· ${r.outcome}</span>` : ''}</span>`).join('');
    const title = o.title === false ? '' : `<span class="k-res-h">${o.title || 'Results'}</span>`;
    return `<div class="k-res${o.inline === false ? ' is-stacked' : ''}">${title}<div class="k-res-in">${chips}</div></div>`;
  };
  ui.resultsFor = (t, o) => ui.resultsStrip((t && t.results) || [], o);

  /** Work details: one folded line (Agents audience), "Work details · 3 Agents · 1 change ▸"; open lists its rows.
      o: { summary, open, rows: [{ mark, icon, text, action, go, … }], go, … } */
  ui.workDetails = (o) => {
    o = o || {};
    const rows = o.open ? `<div class="k-wd-rows">${arr(o.rows).map((r) => `<div class="k-wd-r"${nav(r)}>${r.mark ? ui.lineMark(r.mark) : r.icon ? I(r.icon, 14) : ''}<span>${r.text}</span>${r.action ? `<span class="k-wd-a">${r.action}</span>` : r.run ? I('chevron-right', 11) : ''}</div>`).join('')}</div>` : '';
    return `<div class="k-wd${o.open ? ' is-open' : ''}"><div class="k-wd-line"${nav(o)}><b>Work details</b>${o.summary ? `<span class="k-sep">·</span><span>${o.summary}</span>` : ''}${I(o.open ? 'chevron-down' : 'chevron-right', 11)}</div>${rows}</div>`;
  };
  ui.workDetailsFor = (t, o) => (t && t.workDetails ? ui.workDetails(Object.assign({ summary: t.workDetails.summary, rows: t.workDetails.rows }, o)) : '');

  /* ---------------------------------------------------------------- conversation */

  const quote = (s) => `“${s}”`;
  const statusTone = (s) => (/^Not delivered/.test(s) ? 'danger' : /^Delivery unknown/.test(s) ? 'caution' : /^Sending/.test(s) ? 'faint' : 'plain');

  /** One round's lines on the paper: "You · 10:02 “…”" (15) over "└ ▸ …" (13 --ink-2). */
  function blockRound(r, o) {
    const when = r.time === 'now' ? 'now' : r.time;
    const sub = r.fold || '';
    return `<div class="k-cb-r"${nav(o)}><div class="k-cb-you"><span class="k-cb-who">You · ${when}</span><span class="k-cb-q">${quote(r.text)}</span></div>${sub ? `<div class="k-cb-sub"><i>└</i><span>${sub}</span></div>` : ''}${r.liveStatus ? `<div class="k-cb-st k-rd-st--${statusTone(r.liveStatus)}">${r.liveStatus}</div>` : ''}</div>`;
  }

  /** ConversationBlock (paper): "Conversation" + "All N ›", then the latest rounds, one line each (owner words, then the
      folded reply or run line). o: { rounds (data rounds; background rounds are skipped), count (2), total (N), all: nav,
      roundNav, pointer (true: "Conversation · open beside →" alone), latest (true: one line "Latest · You “…” ›") } */
  ui.conversationBlock = function conversationBlock(o) {
    o = o || {};
    if (o.pointer) return `<div class="k-cb is-pointer"${nav(o.pointerNav || o.all)}>${I('bubble-left-right', 15)}<span><b>Conversation</b> · open beside</span>${I('arrow-right', 13)}</div>`;
    const rounds = arr(o.rounds).filter((r) => r.kind !== 'background');
    if (o.latest) {
      const r = rounds[rounds.length - 1];
      return r ? `<div class="k-cb is-latest"${nav(o.all)}><span class="k-cb-who">Latest</span><span class="k-sep">·</span><span class="k-cb-q">You ${quote(r.text)}</span>${I('chevron-right', 12)}</div>` : '';
    }
    const shown = rounds.slice(-(o.count || 2));
    return `<div class="k-cb"><div class="k-cb-head"><span>Conversation</span><span class="k-cb-all"${nav(o.all)}>All ${o.total != null ? o.total : rounds.length} ›</span></div>${shown.map((r) => blockRound(r, o.roundNav)).join('')}</div>`;
  };
  ui.conversationBlockFor = (t, o) => {
    o = o || {};
    const extra = arr(o.extra).map((id) => (typeof id === 'string' ? t.extraRounds[id] : id));
    const rounds = (t.rounds || []).concat(extra);
    if (!rounds.length && !o.pointer) return '';
    return ui.conversationBlock(Object.assign({ rounds, total: (t.allCount || rounds.length) + extra.length }, o));
  };

  /* Round lines. Each returns HTML; ui.convRound draws a data round with them. */
  ui.rline = {
    /** "▸ Ryokan search worked 9 min → 3 ryokans" (13); open: the 3 latest steps, one line each, and "Open Session ›" */
    run: (o) => `<div class="k-rl k-rl--run${o.open ? ' is-open' : ''}"${nav(o)}>${I(o.open ? 'chevron-down' : 'chevron-right', 10)}<span><b>${o.who || ''}</b> ${o.text || ''}${o.done ? ` ${I('checkmark', 11)}` : ''}</span></div>${o.open ? `<div class="k-rl-steps">${arr(o.steps).map((s) => `<div>${s}</div>`).join('')}${o.session ? `<div class="k-rl-open"${nav(o.session)}>Open Session ›</div>` : ''}</div>` : ''}`,
    /** "◐ Release notes · drafting What’s New" (a live run line) */
    live: (o) => `<div class="k-rl k-rl--live"${nav(o)}>${ui.spinner(11)}<span><b>${o.who}</b> · ${o.text}</span></div>`,
    /** "● Widget · Plan awaiting approval" */
    needs: (o) => `<div class="k-rl k-rl--needs"${nav(o)}>${ui.lineMark('needs', 7)}<span><b>${o.who}</b> · ${o.text}</span></div>`,
    /** "↗ Queued for Release notes · after this step [Send Now]"; state: 'queued' | 'delivered' | 'now' */
    relay: (o) => {
      const t = o.state === 'delivered' ? `Delivered to ${o.to}` : o.state === 'now' ? `Sent now to ${o.to}` : `Queued for ${o.to} · after this step`;
      return `<div class="k-rl k-rl--relay">${I('arrow-up-right', 12)}<span>${t}</span>${o.state === 'queued' && o.sendNow ? `<span class="k-rl-btn"${nav(o.sendNowNav)}>Send Now</span>` : ''}</div>`;
    },
    /** "Scope: + home-screen widget in the release notes · Undo" (13 positive) */
    scope: (o) => `<div class="k-rl k-rl--scope"><span>Scope: ${o.text}</span><span class="k-sep">·</span><span class="k-rl-undo"${nav(o.undo)}>Undo</span></div>`,
    /** "✓ Decision · Flights: option B" */
    decision: (o) => `<div class="k-rl k-rl--decision">${I('checkmark', 12)}<span>Decision · ${o.text}</span></div>`,
    /** "✓ You kept the steps · 5" */
    kept: (o) => `<div class="k-rl k-rl--decision">${I('checkmark', 12)}<span>${o.text}</span></div>`,
    /** "● Asked: Which ryokan?" (no buttons: the ask is drawn once elsewhere). o: { text, suffix ('(in the spine ↑)') } */
    asked: (o) => `<div class="k-rl k-rl--asked"${nav(o)}>${ui.lineMark('needs', 7)}<span>Asked: ${o.text}${o.suffix ? ` <i>${o.suffix}</i>` : ''}</span></div>`,
    /** "▣ Flights · held until Fri" */
    result: (o) => `<div class="k-rl k-rl--result"${nav(o)}>${I('doc', 12)}<span>${o.text}</span></div>`,
    /** The steward's or a run's text, left, without a bubble: "Persona: …" */
    reply: (o) => `<div class="k-rd-reply"><b>${o.who}:</b> ${o.text}</div>`,
    /** A day separator, caption 13, centred */
    day: (text) => `<div class="k-rd-day">${text}</div>`,
    /** The folded Background round: "Background · Persona checked flight prices · no change ▸" */
    background: (o) => `<div class="k-rl k-rl--bg${o.open ? ' is-open' : ''}"${nav(o)}><span><b>Background</b> · ${o.text}</span>${I(o.open ? 'chevron-down' : 'chevron-right', 10)}</div>`,
  };

  /** Round (pane / pushed page): the owner's bubble on the right (--bubble-user, radius 20, ≤ 75%) with its state line
      under it; then the steward's text, run, relay, scope, decision and asked lines. r: a Data round.
      o: { status (override: 'Sending' | 'Processing' | 'Delivered to …' | 'Not delivered · Retry' | …), open: { <run who>: true },
           relay ('queued' | 'delivered' | 'now'), askedSuffix, hide: [kinds], nav: { sendNow, undo, session } } */
  ui.convRound = function convRound(r, o) {
    o = o || {};
    if (r.kind === 'background') return ui.rline.background(Object.assign({ text: r.text }, o.bgNav, { open: o.bgOpen }));
    const n = o.nav || {};
    const status = o.status || r.status;
    const items = arr(r.items).filter((it) => !(o.hide || []).includes(it.kind)).map((it) => {
      switch (it.kind) {
        case 'reply': return ui.rline.reply(it);
        case 'run': return ui.rline.run(Object.assign({}, it, { open: o.open && o.open[it.who], session: n.session }));
        case 'live': return ui.rline.live(it);
        case 'needs': return ui.rline.needs(it);
        case 'relay': return ui.rline.relay(Object.assign({}, it, { state: o.relay || it.state, sendNowNav: n.sendNow }));
        case 'scope': return ui.rline.scope(Object.assign({}, it, { undo: n.undo }));
        case 'decision': return ui.rline.decision(it);
        case 'kept': return ui.rline.kept(it);
        case 'asked': return ui.rline.asked(Object.assign({}, it, { suffix: o.askedSuffix }));
        case 'result': return ui.rline.result(it);
        default: return '';
      }
    }).join('');
    return `<div class="k-rd">
      ${r.from ? `<div class="k-rd-from">from ${r.from}</div>` : ''}
      <div class="k-rd-bubble">${r.text}</div>
      ${status ? `<div class="k-rd-st k-rd-st--${statusTone(status)}">${status}</div>` : ''}
      ${items ? `<div class="k-rd-items">${items}</div>` : ''}
    </div>`;
  };

  /** A whole thread: rounds with a day separator wherever the day changes. o: { anchor: 'bottom' (scrolled to the latest:
      the thread sits on the bottom edge and the top is cut), pad, rounds' options (passed to convRound), per: { <round id>:
      options } } */
  ui.thread = function thread(rounds, o) {
    o = o || {};
    let day = null;
    const out = [];
    arr(rounds).forEach((r) => {
      if (o.days !== false && r.day && r.day !== day) { out.push(ui.rline.day(r.day)); day = r.day; }
      out.push(ui.convRound(r, Object.assign({}, o, (o.per || {})[r.id])));
    });
    return `<div class="k-thread${o.anchor === 'bottom' ? ' is-bottom' : ''}"${o.pad ? ` style="padding:${o.pad}"` : ''}>${out.join('')}</div>`;
  };

  /* ---------------------------------------------------------------- composer and addressee */

  /** AddresseeChip (inside the composer): a 28 capsule on --fill-chip, 13 semibold "To Persona" (+ ▾ when the menu has more
      than one entry); a purple --ai tint when it names a run; "Answer: Which ryokan? ×" after Discuss…
      to: 'Persona' | { label, menu, run, answer, go, … } */
  ui.addresseeChip = function addresseeChip(to) {
    const t = typeof to === 'string' ? { label: to } : to || {};
    if (t.answer) return `<span class="k-ach is-answer"${nav(t)}><span>Answer: ${t.answer}</span>${I('xmark', 10)}</span>`;
    return `<span class="${Kit.cls('k-ach', t.run && 'is-run')}"${nav(t)}><span>To ${t.label || 'Persona'}</span>${t.menu ? I('chevron-down', 10) : ''}</span>`;
  };

  /** Composer: the floating footer, a glass capsule 52 tall, radius 32, inset 16 (8 on the Duo outer display). The
      AddresseeChip leads inside it; the field (15); mic 32 and send ↑ 32 (accent; dimmed while empty).
      o: { to (see addresseeChip; false: no chip, e.g. the Assistant's own composer), placeholder ('Message about this to-do…'), text (typed text, with a caret), caret (true),
           label: 'above' (V2: no chip inside; while there is text the label "To Persona ▾" stands above the field),
           note (a line above), inset (16 | 8), menu (HTML: the addressee menu, drawn open above the chip), mic (true),
           send: nav, cls } */
  ui.todoComposer = function todoComposer(o) {
    o = o || {};
    const hasText = !!o.text;
    const above = o.label === 'above';
    const chip = above || o.to === false ? '' : ui.addresseeChip(o.to || 'Persona');
    const field = hasText ? `<span class="k-tc-f has-text"><span class="k-tc-in">${o.text}${o.caret === false ? '' : '<i class="k-caret"></i>'}</span></span>` : `<span class="k-tc-f">${o.placeholder || 'Message about this to-do…'}</span>`;
    const lbl = above && hasText ? `<div class="k-tc-lbl">${ui.addresseeChip(Object.assign({}, typeof o.to === 'string' ? { label: o.to } : o.to || {}, { menu: true }))}</div>` : '';
    const inset = o.inset == null ? 16 : o.inset;
    return `<div class="k-tc${o.cls ? ' ' + o.cls : ''}" style="--tc-inset:${inset}px">
      ${o.note ? `<div class="k-tc-note">${o.note}</div>` : ''}
      ${lbl}
      ${o.menu ? `<div class="k-tc-menu">${o.menu}</div>` : ''}
      <div class="k-tc-cap">${chip}${field}${o.mic === false ? '' : `<span class="k-tc-mic">${I('mic', 18)}</span>`}<span class="k-tc-send${hasText ? '' : ' is-off'}"${nav(o.send)}>${I('arrow-up', 16)}</span></div>
    </div>`;
  };
  /** Composer for a Data to-do (its steward chip and menu). o: overrides */
  ui.composerFor = (t, o) => ui.todoComposer(Object.assign({ to: (t.composer && t.composer.to) || 'Persona', placeholder: t.composer && t.composer.placeholder }, o));

  /* ---------------------------------------------------------------- headers */

  /** PinnedHeader (phones, 92): row 1 = ‹ (glass 44) + title (17 semibold, 1 line) + ⋯; row 2 = the state word and the
      ask's name (15) + [OK: option] (capsule 32) + "Options ⌄"; a hairline under it. Tapping the title area goes back.
      o: { title, back: nav | false (Duo: Back lives in the side column), more: nav | false, titleNav, word, mark ('needs'),
           ask, ok: { label, … }, door: { label, … }, options: true | nav, row2 (HTML instead), layout: 'one' (92, default) |
           'two' (the buttons on their own line, as V1-04's wireframe) } */
  ui.pinnedHeader = function pinnedHeader(o) {
    o = o || {};
    const back = o.back === false ? '' : ui.btn(Object.assign({ icon: 'chevron-left', title: 'Back' }, asNav(o.back === true ? null : o.back)));
    const more = o.more === false ? '' : ui.btn(Object.assign({ icon: 'ellipsis', title: 'More' }, asNav(o.more === true ? null : o.more)));
    const text = `<span class="k-ph-t">${o.mark === false ? '' : ui.lineMark(o.mark || 'needs')}${o.word ? `<span class="k-ph-w">${o.word}</span>` : ''}${o.word && o.ask ? '<span class="k-sep">·</span>' : ''}${o.ask ? `<span class="k-ph-q">${o.ask}</span>` : ''}</span>`;
    const ok = o.ok ? `<span class="k-ans k-ans--ok"${nav(btnNav(o.ok))}><span>${btnNav(o.ok).label}</span></span>` : '';
    const door = o.door ? `<span class="k-ans k-ans--door"${nav(btnNav(o.door))}><span>${btnNav(o.door).label}</span>${I('chevron-right', 12)}</span>` : '';
    const opt = o.options ? `<span class="k-ans-opt"${nav(o.options === true ? null : o.options)}><span>Options</span>${I('chevron-down', 11)}</span>` : '';
    const row2 = o.row2 != null ? o.row2 : o.layout === 'two'
      ? `<div class="k-ph-r2">${text}</div><div class="k-ph-r3">${ok}${door}<span class="k-grow"></span>${opt}</div>`
      : `<div class="k-ph-r2">${text}${ok}${door}${opt}</div>`;
    return `<div class="k-ph${o.layout === 'two' ? ' is-two' : ''}${back ? '' : ' no-back'}">
      <div class="k-ph-r1">${back}<div class="k-ph-title"${nav(o.titleNav || (o.back && o.back !== true ? asNav(o.back) : null))}>${o.title || ''}</div>${more}</div>
      ${row2}
    </div>`;
  };
  /** PinnedHeader for a Data to-do: one line "● Which ryokan? [OK: Hanami] Options ⌄" (layout 'two': "● Needs you · Which
      ryokan?" over the buttons). */
  ui.pinnedHeaderFor = (t, o) => {
    const ask = t.asks && t.asks.length ? D().ask(t.asks[0]) : null;
    const a = t.answer || {};
    const two = o && o.layout === 'two';
    const base = { title: t.title, word: t.state && (two || !ask) ? t.state.word : '', ask: ask ? ask.title : t.state ? t.state.text : '', mark: ask ? 'needs' : false };
    if (ask && ask.kind === 'plan') base.door = { label: ask.door };
    else if (ask) { base.ok = { label: a.short || a.label }; base.options = true; }
    return ui.pinnedHeader(Object.assign(base, o));
  };

  /** PaneHeader: 52 tall inside the pane (no system bar of its own): the channel switcher "Persona ▾" (heading 17), an
      optional state, ⋯ and ✕ (glass 36). o: { channel, menu (true: ▾), state ('needs' …), word, more: nav, close: nav,
      open (HTML: the channel menu drawn open), switchNav } */
  ui.paneHeader = function paneHeader(o) {
    o = o || {};
    const st = o.state ? `<span class="k-pnh-st">${ui.lineMark(o.state === 'needs' ? 'needs' : o.state, 7)}<span>${o.word || ''}</span></span>` : '';
    return `<div class="k-pnh">
      <span class="k-pnh-ch"${nav(o.switchNav)}><span class="k-pnh-name">${o.channel || 'Persona'}</span>${o.menu ? I('chevron-down', 12) : ''}${st}${o.open || ''}</span>
      <span class="k-grow"></span>
      ${o.more === false ? '' : `<span class="k-pnh-b"${nav(asNav(o.more))}>${I('ellipsis', 17)}</span>`}
      ${o.close === false ? '' : `<span class="k-pnh-b"${nav(asNav(o.close))}>${I('xmark', 15)}</span>`}
    </div>`;
  };

  /* ---------------------------------------------------------------- place summary, toast, pill, drawer */

  /** PlaceSummary (wide screens, nothing selected): a centred column 360: display 28 title, a secondary line, then tappable
      lines 44 tall with a trailing ›. o: { title, sub, lines: [{ count, label, look ('needs'), lit, go, … }], foot: [HTML] } */
  ui.placeSummary = function placeSummary(o) {
    o = o || {};
    return `<div class="k-psum"><div class="k-psum-in">
      <div class="k-psum-title">${o.title || ''}</div>
      ${o.sub ? `<div class="k-psum-sub">${o.sub}</div>` : ''}
      <div class="k-psum-lines">${arr(o.lines).map((l) => `<div class="k-psum-l${l.lit ? ' is-lit' : ''}"${nav(l)}>${l.look === 'needs' ? ui.badge(l.count, { look: 'needs' }) : `<b>${l.count}</b>`}<span>${l.label}</span>${I('chevron-right', 13)}</div>`).join('')}</div>
      ${arr(o.foot).map((f) => `<p class="k-psum-foot">${f}</p>`).join('')}
    </div></div>`;
  };
  /** PlaceSummary for a place from Data (S7: one glance). o: { go: { needs, working, … }, foot } */
  ui.placeSummaryFor = (place, o) => {
    o = o || {};
    const d = D();
    const c = d.countsOf(place);
    const p = d.place(place) || { title: place };
    const goes = o.go || {};
    const names = (d.projects || []).map((x) => x.name);
    const sub = place === 'all' || !place
      ? `${c.open} open across ${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}${d.counts.withoutProject ? ` (${d.counts.withoutProject} more without a project)` : ''}`
      : `${c.open} open`;
    const foot = o.foot || (place === 'all' || !place
      ? [`The Assistant’s Needs you shows ${d.counts.assistantNeeds}: these ${d.counts.needs} and ${d.counts.elsewhere} Agent elsewhere.`, 'Select a to-do, or ⌘N for a new one.']
      : []);
    return ui.placeSummary({ title: p.title, sub, lines: d.summary(place).map((l) => Object.assign({}, l, asNav(goes[l.id]))), foot });
  };

  /** Toast: a glass capsule 44 at bottom 96 (above the composer or pill): "Done · Undo". o: { action ('Undo'), icon,
      bottom (96), actionNav } */
  ui.toast = (text, o) => {
    o = o || {};
    return `<div class="k-toast" style="bottom:${o.bottom != null ? o.bottom : 96}px">${o.icon ? I(o.icon, 16) : ''}<span>${text}</span>${o.action ? `<span class="k-sep">·</span><b class="k-toast-a"${nav(o.actionNav)}>${o.action}</b>` : ''}</div>`;
  };

  /** A floating accent pill (46 tall), centred: "+ New To-Do". o: { icon ('plus'), label, bottom (50), go, … } */
  ui.pill = (o) => {
    o = o || {};
    return `<span class="k-pill" style="bottom:calc(${o.bottom != null ? o.bottom : 50}px)"${nav(o)}>${I(o.icon || 'plus', 17)}<span>${o.label || 'New To-Do'}</span></span>`;
  };

  /** Drawer: the sidebar laid over the dimmed page from the leading edge (min(82% of the width, 380)). o: { content, width,
      scrimNav } */
  ui.drawer = (o) => {
    o = o || {};
    return `<div class="k-scrim"${nav(o.scrimNav)}></div><div class="k-drawer"${o.width ? ` style="width:${o.width}px"` : ''}>${o.content || ''}</div>`;
  };

  /* ---------------------------------------------------------------- sidebar tree */

  /** SidebarTree: Persona's sidebar with the To-dos row and its children (Today · Upcoming · a 12-pt gap · the projects with
      their blue part of N · All Projects…), then Scheduled Tasks, Knowledge, Machines, Persona Chats (Persona; Release lead ·
      Pinned with its count), Agents ›. Rows 44, radius 10; the chosen row is shaded --fill-well.
      o: { selected ('inbox' | 'todos' (= All To-dos) | 'today' | 'upcoming' | <project id> | 'persona' | 'release-lead' | …),
           open (true: To-dos unfolded), starred ([project ids]: V2's sidebar shows only starred places, with ☆),
           counts (default Data.counts), header (true), compose (true), go: { <row id>: nav }, wc, width, cls } */
  ui.sidebarTree = function sidebarTree(o) {
    o = o || {};
    const d = D();
    const c = o.counts || d.counts || {};
    const goes = o.go || {};
    const sel = o.selected || 'todos';
    const row = (id, opts) => ui.sbRow(Object.assign({ selected: sel === id }, opts, asNav(goes[id])));
    const rows = [];
    rows.push(row('inbox', { icon: 'tray-full', label: 'Inbox', count: c.inbox, countLook: 'plain' }));
    const open = o.open !== false;
    rows.push(row('todos', { icon: 'checklist', label: 'To-dos', count: c.needs || null, countLook: 'needs', disclosure: open ? 'open' : 'closed' }));
    if (open) {
      if (o.starred) {
        arr(o.starred).forEach((pid) => {
          const p = d.project(pid);
          if (p) rows.push(row(pid, { icon: 'star', label: p.name, indent: 1, count: (c.byProject || {})[pid] || null, countLook: 'needs', tone: 'caution' }));
        });
      } else {
        rows.push(row('today', { icon: 'sun-fill', label: 'Today', indent: 1 }));
        rows.push(row('upcoming', { icon: 'calendar', label: 'Upcoming', indent: 1 }));
        rows.push('<div class="k-tree-gap"></div>');
        (d.projects || []).forEach((p) => rows.push(row(p.id, { mark: `<span style="color:var(--hue-${p.hue})">${I('project')}</span>`, label: p.name, indent: 1, count: (c.byProject || {})[p.id] || null, countLook: 'needs' })));
        rows.push(row('projects', { icon: 'ellipsis-circle', label: 'All Projects…', indent: 1 }));
      }
    }
    rows.push(row('scheduled', { icon: 'alarm', label: 'Scheduled Tasks' }));
    rows.push(row('knowledge', { icon: 'books', label: 'Knowledge' }));
    rows.push(row('machines', { icon: 'laptop-phone', label: 'Machines' }));
    rows.push(`<div class="k-tree-h"${nav(asNav(goes.chats))}><span>Persona Chats</span>${I('chevron-down', 10)}</div>`);
    rows.push(row('persona', { mark: ui.face(24), label: 'Persona', indent: 1 }));
    (d.directions || []).forEach((dir) => rows.push(row(dir.id, { icon: 'pin', label: `${dir.title} · Pinned`, indent: 1, count: (c.pinned || {})[dir.id] || null, countLook: 'needs' })));
    rows.push(row('agents', { icon: 'agents', label: 'Agents', disclosure: 'closed' }));
    return `<div class="k-sb k-tree${o.cls ? ' ' + o.cls : ''}"${o.width ? ` style="width:${o.width}px"` : ''}>
      ${o.header === false ? '' : `<div class="k-tree-head${o.wc ? ' has-wc' : ''}">${ui.btn({ icon: 'sidebar-left', size: 's', title: 'Hide Sidebar' })}<span class="k-tree-name">Persona</span>${o.compose === false ? '' : ui.btn({ icon: 'square-pencil', size: 's', title: 'New Chat' })}</div>`}
      <div class="k-sb-scroll">${rows.join('')}</div>
    </div>`;
  };

  /* ---------------------------------------------------------------- capture */

  /** CaptureSheet: a sheet fitted to its content, docked above the keyboard; ✕ top-leading; a one-line field with a mic;
      parsed chips (dashed until Return); [Add & Ask Persona] secondary, [Add] primary.
      o: { title ('New To-Do'), text, token (the parsed word, underlined), placeholder, chips: [{ label, dashed, clear }],
           adds (['Project', 'Remind']), close: nav, add: nav, ask: nav, scrim (true) } */
  ui.captureSheet = function captureSheet(o) {
    o = o || {};
    let text = o.text || '';
    if (text && o.token && text.endsWith(o.token)) text = `${text.slice(0, -o.token.length)}<u>${o.token}</u>`;
    const field = o.text ? `<span class="k-cs-f has-text"><span class="k-tc-in">${text}<i class="k-caret"></i></span></span>` : `<span class="k-cs-f">${o.placeholder || 'New To-Do'}</span>`;
    const chips = arr(o.chips).map((c) => Object.assign({ icon: 'calendar' }, c));
    const adds = (o.adds || ['Project', 'Remind']).map((a) => ({ label: a, icon: 'plus', outline: true }));
    return `${o.scrim === false ? '' : '<div class="k-scrim"></div>'}<div class="k-cs">
      <div class="k-cs-head">${ui.btn(Object.assign({ icon: 'xmark', size: 's', title: 'Close' }, asNav(o.close)))}<span class="k-cs-title">${o.title || 'New To-Do'}</span><span class="k-cs-sp"></span></div>
      <div class="k-cs-row">${field}<span class="k-cs-mic">${I('mic', 18)}</span></div>
      ${ui.todoChips(chips.concat(adds))}
      <div class="k-cs-actions"><span class="k-ac-btn"${nav(o.ask)}><span>Add &amp; Ask Persona</span></span><span class="k-ac-btn is-primary"${nav(o.add)}><span>Add</span></span></div>
    </div>`;
  };

  /* ---------------------------------------------------------------- the Assistant's Needs you tray */

  /** The Assistant's Needs you tray (V1-09): rows grouped "To-dos · 3" and "Elsewhere · 1" so the parts add up to the
      header's 4. o: { header ('Needs you · 4'), groups: [{ title, rows: [{ title, ask, tone, buttons: [{ label, primary,
      door, go, … }], go, … }] }] } */
  ui.needsTray = function needsTray(o) {
    o = o || {};
    const b = (x) => `<span class="k-nt-b${x.primary ? ' is-primary' : ''}${x.door ? ' is-door' : ''}"${nav(x)}><span>${x.label}</span>${x.door ? I('chevron-right', 11) : ''}</span>`;
    return `<div class="k-nt">
      <div class="k-nt-h">${o.header || ''}</div>
      ${arr(o.groups).map((g) => `<div class="k-nt-g"><div class="k-nt-gh">${g.title}</div>${arr(g.rows).map((r) => `<div class="k-nt-r"${nav(r)}><div class="k-nt-t">${r.title}</div><div class="k-nt-l"><span class="k-nt-q${r.tone ? ' k-tone--' + r.tone + ' is-toned' : ''}">${r.ask}</span><span class="k-nt-bs">${arr(r.buttons).map(b).join('')}</span></div></div>`).join('')}</div>`).join('')}
    </div>`;
  };
  /** The tray from Data: every to-do that needs the owner (one button each: the safe answer, or Review › for a plan), then
      Elsewhere. o: { on: { a1, a2, a3, deny, allow, <todo id>: nav (the row) } } */
  ui.needsTrayFor = (o) => {
    o = o || {};
    const d = D();
    const on = o.on || {};
    const rows = d.todos.filter((t) => t.section === 'needs').map((t) => {
      const ask = d.ask(t.asks[0]);
      const a = t.answer || {};
      const button = ask.kind === 'plan' ? { label: 'Review', door: true } : { label: a.label, primary: true };
      return Object.assign({ title: t.title, ask: ask.kind === 'plan' ? `${d.run(ask.run).title}: ${ask.title}` : t.row.lead + (t.row.text && t.row.all ? ` · ${t.row.text}` : ''), tone: t.row.all ? t.row.tone : '',
        buttons: [Object.assign(button, asNav(on[ask.id]))] }, asNav(on[t.id]));
    });
    const e = d.elsewhere;
    const groups = [{ title: `To-dos · ${d.counts.needs}`, rows }];
    if (d.counts.elsewhere) {
      groups.push({ title: `Elsewhere · ${d.counts.elsewhere}`, rows: [{ title: e.title, ask: e.ask,
        buttons: e.actions.map((x) => Object.assign({ label: x.label, primary: !!x.primary }, asNav(on[x.primary ? 'allow' : 'deny']))) }] });
    }
    return ui.needsTray({ header: `Needs you · ${d.counts.assistantNeeds}`, groups });
  };

  /* ---------------------------------------------------------------- lock screen, notification, Live Activity */

  /** LockScreen (iPhone): wallpaper, the lock, date and big time, a stack of content (notifications, Live Activity) and the
      flashlight and camera at the foot. Use with chrome { statusBar: true } or false. o: { date, time, content } */
  ui.lockScreen = function lockScreen(o) {
    o = o || {};
    const n = D().now || {};
    return `<div class="k-lock">
      <div class="k-lock-top">${I('lock-fill', 18)}<div class="k-lock-date">${o.date || n.lockDate || ''}</div><div class="k-lock-time">${o.time || n.time || ''}</div></div>
      <div class="k-lock-stack">${o.content || ''}</div>
      <div class="k-lock-foot"><span>${I('flashlight', 22)}</span><span>${I('camera', 22)}</span></div>
    </div>`;
  };

  /** Notification on the lock screen; expanded shows its actions (recommended first). o: { app ('Persona'), time ('now'),
      title, body, actions: [{ label, recommended, go, … }], expanded, go, … } */
  ui.notification = function notification(o) {
    o = o || {};
    return `<div class="k-noti${o.expanded ? ' is-expanded' : ''}"${nav(o)}>
      <div class="k-noti-head"><span class="k-noti-ic">${ui.face(22)}</span><span class="k-noti-app">${o.app || 'Persona'}</span><span class="k-noti-time">${o.time || 'now'}</span></div>
      <div class="k-noti-title">${o.title || ''}</div>
      <div class="k-noti-body">${o.body || ''}</div>
      ${o.expanded && o.actions ? `<div class="k-noti-acts">${arr(o.actions).map((a) => `<div class="k-noti-a${a.recommended ? ' is-rec' : ''}"${nav(a)}>${a.label}</div>`).join('')}</div>` : ''}
    </div>`;
  };
  /** The TODO_DECIDE push for a Data ask. o: { expanded, on: { <option>: nav }, go } */
  ui.notificationFor = (askId, o) => {
    o = o || {};
    const ask = D().ask(askId);
    if (!ask || !ask.push) return '';
    const on = o.on || {};
    return ui.notification(Object.assign({ title: ask.push.title, body: ask.push.body, expanded: o.expanded !== false,
      actions: ask.push.actions.map((a) => Object.assign({}, a, asNav(on[a.option]))) }, o));
  };

  /** Fleet Live Activity: header "Persona · 4 need you", then one row per to-do that needs the owner with one button (the
      recommended safe answer, or Open). o: { header, rows: [{ title, ask, button: { label, recommended, go, … } }], more } */
  ui.fleetActivity = function fleetActivity(o) {
    o = o || {};
    return `<div class="k-fleet">
      <div class="k-fleet-h">${ui.face(22)}<b>Persona</b><span class="k-sep">·</span><span>${o.header || ''}</span></div>
      ${arr(o.rows).map((r) => `<div class="k-fleet-r"${nav(r)}><div class="k-fleet-t"><div class="k-fleet-ti">${r.title}</div><div class="k-fleet-q">${r.ask}</div></div><span class="k-fleet-b${r.button && r.button.recommended ? ' is-rec' : ''}"${nav(r.button)}>${r.button ? r.button.label : 'Open'}</span></div>`).join('')}
      ${o.more ? `<div class="k-fleet-more">${o.more}</div>` : ''}
    </div>`;
  };
  /** Fleet Live Activity from Data: it lists sessions, as it does today (live-activity.md), not to-dos: the open to-dos'
   *  runs that need the owner, then the session that belongs to no to-do (Elsewhere); at most two, then "N more".
   *  o: { on: { <run id or 'elsewhere'>: go/toast }, limit } */
  ui.fleetActivityFor = (o) => {
    o = o || {};
    const d = D();
    const on = o.on || {};
    const sessions = [];
    d.todos.filter((t) => !t.closed).forEach((t) => (t.runs || []).filter((r) => r.session && r.state === 'needs').forEach((r) => {
      const ask = (t.asks || []).map((id) => d.ask(id)).find((a) => a && a.run === r.id);
      sessions.push({ title: r.title, ask: ask ? ask.title : t.title, button: Object.assign({ label: ask && ask.kind === 'plan' ? 'View' : 'Open' }, asNav(on[r.id])) });
    }));
    if (d.counts.elsewhere) {
      const e = d.elsewhere;
      const primary = (e.actions || []).find((x) => x.primary);
      sessions.push({ title: e.title, ask: e.ask, button: Object.assign({ label: primary ? primary.label : 'Open', recommended: true }, asNav(on.elsewhere)) });
    }
    const limit = o.limit || 2;
    const rows = sessions.slice(0, limit);
    const more = sessions.length - rows.length;
    return ui.fleetActivity({ header: d.counts.fleet, rows, more: more > 0 ? `${more} more` : '' });
  };
})();
