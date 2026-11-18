/*
 * frame.html: renders exactly one frame at its device size with no page padding, for screenshots and click-throughs.
 *
 *   frame.html#v=V1&f=iphone/list&theme=dark[&scale=0.5][&shot=1]  one frame; a state is a frame too: f=iphone/list~calm
 *   frame.html#list=V1  (or list=all)                       machine-readable frame list in <pre id="frames"> (JSON)
 *                                                            and <pre id="frames-tsv"> (variant, id, device, w, h)
 *   frame.html                                              links to every registered frame
 *
 * Clicking an element with data-go="<frameId>" (or "V2:<frameId>") moves to that frame, playing its data-transition:
 * push (the new frame slides in from the trailing edge, 0.3 s) · pop (the reverse) · fade (0.2 s cross-fade) ·
 * pane-open / pane-close (a 0.3 s cross-fade and slide) · none. Reduce Motion (the system setting or the strip's toggle)
 * turns every transition into the 0.2 s fade. A frame loaded directly by its hash always renders statically.
 * An element with data-toast (and no data-go) shows a 2-s toast naming a destination the prototype doesn't build.
 * A frame's `auto: { go, after, transition }` moves on by itself (Sending → Processing → Delivered), never with shot=1.
 *
 * The floating strip (hidden with &shot=1, which the screenshot tools add): Light / Dark, Reduce Motion, Hotspots
 * (dashed outlines on everything tappable), Reset (clears sessionStorage), Fold / Unfold (Duo frames with a `fold` or
 * `unfold` option), previous / next, the gallery. Keys: t theme, m Reduce Motion, h hotspots, ← / → previous / next
 * frame, g the gallery.
 */
(function () {
  const root = document.getElementById('root');
  const doc = document.documentElement;

  /* ---------------------------------------------------------------- harness preferences (localStorage, not Reset) */

  const PREF = 'kit.harness.';
  const pref = {
    get(k, d) { try { const v = localStorage.getItem(PREF + k); return v == null ? d : v === '1'; } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem(PREF + k, v ? '1' : '0'); } catch (e) { /* file:// without storage */ } },
  };
  const systemReduce = () => !!(window.matchMedia && matchMedia('(prefers-reduced-motion: reduce)').matches);
  const reduceMotion = () => pref.get('reduce', false) || systemReduce();
  function applyPrefs(shot) {
    doc.classList.toggle('kf-reduce', !shot && reduceMotion());
    doc.classList.toggle('kf-hot', !shot && pref.get('hot', false));
  }

  /* ---------------------------------------------------------------- pages */

  function setBodySize(w, h) {
    const s = document.body.style;
    s.width = w + 'px'; s.height = h + 'px';
    doc.style.width = w + 'px';
    doc.style.height = h + 'px';
  }

  function renderList(which) {
    const vs = which && which !== 'all' ? [Kit.getVariant(which)].filter(Boolean) : Kit.variants();
    const frames = [];
    vs.forEach((v) => v.frames.forEach((f) => {
      const d = Kit.devices[f.device] || { w: 0, h: 0 };
      frames.push({ variant: v.id, id: f.id, device: f.device, w: d.w, h: d.h, title: f.title, important: !!f.important });
    }));
    document.title = 'Frames · ' + (which || 'all');
    doc.className = 'kf-doc';
    root.innerHTML = `<pre id="frames">${Kit.esc(JSON.stringify(frames, null, 1))}</pre>
      <pre id="frames-tsv">${frames.map((f) => [f.variant, f.id, f.device, f.w, f.h].join('\t')).join('\n')}</pre>`;
  }

  function renderIndex(msg) {
    doc.className = 'kf-doc';
    const theme = Kit.hash.parse().theme || 'light';
    root.innerHTML = `<div class="kf-index">${msg ? `<p class="kf-msg">${msg}</p>` : ''}<h1>Frames</h1>${Kit.variants().map((v) => `
      <h2>${Kit.esc(v.id)} · ${Kit.esc(v.name)}</h2>
      <ul>${v.frames.map((f) => `<li><a href="#${Kit.hash.build({ v: v.id, f: f.id, theme })}">${f.important ? '★ ' : ''}${Kit.esc(f.id)}</a> <span>${Kit.esc(f.title)} · ${Kit.esc(f.device)}</span></li>`).join('') || '<li><span>No frames yet.</span></li>'}</ul>`).join('')}
      <p><a href="index.html">Open the gallery</a></p></div>`;
    strip(null);
  }

  /* ---------------------------------------------------------------- transitions */

  const EASE_PUSH = 'cubic-bezier(.32, .72, 0, 1)';
  let pending = null; // the transition of the click that changed the hash
  let autoTimer = null;

  function settle(els) {
    els.forEach((el) => { if (el) { el.style.transform = ''; el.style.opacity = ''; el.style.background = ''; el.style.boxShadow = ''; el.style.zIndex = ''; } });
  }

  /** Animate from the previous device to the one now in root. Same device: only the content moves, the system chrome stays. */
  function animate(prevDev, nextDev, kind) {
    if (!prevDev || !nextDev || !kind || kind === 'none' || !Element.prototype.animate) return;
    if (reduceMotion()) kind = 'fade';
    const sameDevice = prevDev.getAttribute('data-device') === nextDev.getAttribute('data-device');
    if (!sameDevice) {
      // Different canvases (Fold / Unfold): cross-fade the whole device.
      prevDev.style.position = 'absolute'; prevDev.style.left = '0'; prevDev.style.top = '0'; prevDev.style.zIndex = '2';
      prevDev.style.pointerEvents = 'none';
      root.style.position = 'relative';
      root.appendChild(prevDev);
      const a = prevDev.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 200, easing: 'ease', fill: 'forwards' });
      a.onfinish = () => prevDev.remove();
      return;
    }
    const oldC = prevDev.querySelector('.dev-content');
    const newC = nextDev.querySelector('.dev-content');
    if (!oldC || !newC) return;
    oldC.style.pointerEvents = 'none';
    const under = kind === 'pop' || kind === 'pane-close' ? newC : oldC;
    const over = under === oldC ? newC : oldC;
    newC.parentNode.insertBefore(oldC, newC);
    under.style.zIndex = '1'; over.style.zIndex = '2';
    [oldC, newC].forEach((el) => { el.style.background = 'var(--dev-bg)'; });
    const done = () => { oldC.remove(); settle([newC]); };
    let anims = [];
    if (kind === 'push') {
      newC.style.boxShadow = '-10px 0 30px rgba(0, 0, 0, .14)';
      anims = [
        newC.animate([{ transform: 'translateX(100%)' }, { transform: 'translateX(0)' }], { duration: 300, easing: EASE_PUSH }),
        oldC.animate([{ transform: 'translateX(0)', opacity: 1 }, { transform: 'translateX(-30%)', opacity: 0.85 }], { duration: 300, easing: EASE_PUSH, fill: 'forwards' }),
      ];
    } else if (kind === 'pop') {
      oldC.style.boxShadow = '-10px 0 30px rgba(0, 0, 0, .14)';
      anims = [
        oldC.animate([{ transform: 'translateX(0)' }, { transform: 'translateX(100%)' }], { duration: 300, easing: EASE_PUSH, fill: 'forwards' }),
        newC.animate([{ transform: 'translateX(-30%)', opacity: 0.85 }, { transform: 'translateX(0)', opacity: 1 }], { duration: 300, easing: EASE_PUSH }),
      ];
    } else if (kind === 'pane-open' || kind === 'pane-close') {
      const dx = kind === 'pane-open' ? 48 : -48;
      anims = [
        newC.animate([{ transform: `translateX(${dx}px)`, opacity: 0 }, { transform: 'translateX(0)', opacity: 1 }], { duration: 300, easing: 'cubic-bezier(.2, .8, .2, 1)' }),
        oldC.animate([{ transform: 'translateX(0)', opacity: 1 }, { transform: `translateX(${-dx}px)`, opacity: 0 }], { duration: 300, easing: 'cubic-bezier(.2, .8, .2, 1)', fill: 'forwards' }),
      ];
    } else {
      // fade (and every transition under Reduce Motion): the new frame fades in over the old one
      over.style.zIndex = '1'; under.style.zIndex = '1';
      newC.style.zIndex = '2';
      anims = [newC.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 200, easing: 'ease' })];
    }
    Promise.all(anims.map((a) => a.finished)).then(done, done);
  }

  /* ---------------------------------------------------------------- toasts for unbuilt destinations */

  let toastTimer = null;
  function toast(text) {
    const screen = root.querySelector('.dev-screen');
    if (!screen) return;
    const old = screen.querySelector('.kf-toast');
    if (old) old.remove();
    clearTimeout(toastTimer);
    screen.insertAdjacentHTML('beforeend', Kit.ui.toast(Kit.esc(text)).replace('class="k-toast"', 'class="k-toast kf-toast"'));
    const el = screen.querySelector('.kf-toast');
    if (el && el.animate && !reduceMotion()) el.animate([{ opacity: 0, transform: 'translate(-50%, 8px)' }, { opacity: 1, transform: 'translate(-50%, 0)' }], { duration: 200, easing: 'ease-out' });
    toastTimer = setTimeout(() => {
      if (!el) return;
      if (el.animate) el.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 200, fill: 'forwards' }).onfinish = () => el.remove();
      else el.remove();
    }, 2000);
  }

  /* ---------------------------------------------------------------- the floating strip */

  function strip(frame, p) {
    let el = document.getElementById('kf-strip');
    if (!frame || !p || p.shot) { if (el) el.remove(); return; }
    if (!el) {
      el = document.createElement('div');
      el.id = 'kf-strip';
      document.body.appendChild(el);
      el.addEventListener('click', onStrip);
    }
    const collapsed = pref.get('collapsed', false);
    const theme = p.theme === 'dark' ? 'dark' : 'light';
    const btn = (act, label, on, title) => `<button type="button" data-kf="${act}" class="${on ? 'is-on' : ''}" title="${Kit.esc(title || label)}">${label}</button>`;
    const state = Kit.stateOf(frame.id);
    el.className = 'kf-strip' + (collapsed ? ' is-collapsed' : '');
    el.innerHTML = collapsed
      ? btn('expand', '◂ Harness', false, 'Show the harness strip')
      : `<span class="kf-strip-id" title="${Kit.esc(frame.title || '')}">${frame.important ? '★ ' : ''}${Kit.esc(p.v)} · ${Kit.esc(Kit.baseOf(frame.id))}${state ? ` <em>~${Kit.esc(state)}</em>` : ''}</span>
        <span class="kf-seg">${btn('light', 'Light', theme === 'light')}${btn('dark', 'Dark', theme === 'dark')}</span>
        ${btn('reduce', 'Reduce Motion', reduceMotion(), systemReduce() ? 'On in the system settings' : 'Every transition becomes a 0.2 s fade')}
        ${btn('hot', 'Hotspots', pref.get('hot', false), 'Outline everything tappable')}
        ${btn('reset', 'Reset', false, 'Clear the click-through state (sessionStorage)')}
        ${frame.fold ? btn('fold', 'Fold', false, 'Go to ' + frame.fold) : ''}${frame.unfold ? btn('unfold', 'Unfold', false, 'Go to ' + frame.unfold) : ''}
        <span class="kf-seg">${btn('prev', '←', false, 'Previous frame')}${btn('next', '→', false, 'Next frame')}</span>
        ${btn('gallery', 'Gallery', false)}${btn('collapse', '▸', false, 'Hide the strip')}`;
  }

  function step(dir) {
    const p = Kit.hash.parse();
    const list = Kit.frames(p.v);
    if (!list.length) return;
    const i = list.findIndex((f) => f.id === p.f);
    const n = list[(i + dir + list.length) % list.length];
    location.hash = Kit.hash.build(Object.assign({}, p, { f: n.id }));
  }

  function onStrip(e) {
    const b = e.target.closest('[data-kf]');
    if (!b) return;
    e.preventDefault();
    const act = b.getAttribute('data-kf');
    const p = Kit.hash.parse();
    const f = p.v && Kit.findFrame(p.v, p.f);
    if (act === 'light' || act === 'dark') location.hash = Kit.hash.build(Object.assign({}, p, { theme: act }));
    else if (act === 'reduce') { pref.set('reduce', !pref.get('reduce', false)); render(); }
    else if (act === 'hot') { pref.set('hot', !pref.get('hot', false)); render(); }
    else if (act === 'reset') { Kit.store.clear(); render(); toast('Reset · the click-through state is cleared'); }
    else if (act === 'fold' && f) go(f.fold, 'fade');
    else if (act === 'unfold' && f) go(f.unfold, 'fade');
    else if (act === 'prev') step(-1);
    else if (act === 'next') step(1);
    else if (act === 'gallery') location.href = 'index.html#' + Kit.hash.build({ v: p.v, theme: p.theme });
    else if (act === 'collapse' || act === 'expand') { pref.set('collapsed', act === 'collapse'); render(); }
  }

  /* ---------------------------------------------------------------- render */

  function render() {
    clearTimeout(autoTimer);
    const p = Kit.hash.parse();
    if ('list' in p) return renderList(p.list);
    const shot = 'shot' in p && p.shot !== '0';
    Kit.setTheme(p.theme || 'light');
    applyPrefs(shot);
    if (!p.v || !p.f) { pending = null; return renderIndex(); }
    const out = Kit.renderFrame(p.v, p.f, { theme: p.theme || 'light', mode: 'frame', shot });
    if (!out) { pending = null; return renderIndex(`No frame “${Kit.esc(p.f)}” in variant “${Kit.esc(p.v)}”.`); }
    const prevDev = root.firstElementChild && root.firstElementChild.classList.contains('dev') ? root.firstElementChild : null;
    const transition = pending; pending = null;
    doc.className = 'kf-frame';
    applyPrefs(shot);
    const scale = parseFloat(p.scale) || 1;
    root.innerHTML = out.html;
    const dev = root.firstElementChild;
    if (scale !== 1 && dev) dev.style.zoom = scale;
    Kit.afterRender(root);
    setBodySize(Math.round(out.w * scale), Math.round(out.h * scale));
    document.title = `${p.v} · ${out.frame.title}`;
    const bg = dev ? getComputedStyle(dev).backgroundColor : '';
    doc.style.background = bg;
    document.body.style.background = bg;
    if (!shot && transition) animate(prevDev, dev, transition);
    strip(out.frame, Object.assign({}, p, { shot }));
    const auto = out.frame.auto;
    if (!shot && auto && auto.go) autoTimer = setTimeout(() => go(auto.go, auto.transition || 'none'), auto.after || 1200);
  }

  function go(value, transition) {
    const p = Kit.hash.parse();
    const t = Kit.resolveGo(value, p.v);
    const next = Kit.hash.build({ v: t.v, f: t.f, theme: p.theme || 'light', scale: p.scale });
    if (('#' + next) === location.hash || next === location.hash) { render(); return; }
    pending = transition || null;
    location.hash = next;
  }

  document.addEventListener('click', (e) => {
    if (e.target.closest && e.target.closest('#kf-strip')) return;
    const el = e.target.closest && e.target.closest('[data-go], [data-toast]');
    if (!el) return;
    e.preventDefault();
    if (el.hasAttribute('data-go')) go(el.getAttribute('data-go'), el.getAttribute('data-transition'));
    else toast(el.getAttribute('data-toast'));
  });

  document.addEventListener('keydown', (e) => {
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    const p = Kit.hash.parse();
    if (e.key === 't') {
      location.hash = Kit.hash.build(Object.assign({}, p, { theme: p.theme === 'dark' ? 'light' : 'dark' }));
    } else if (e.key === 'm') {
      pref.set('reduce', !pref.get('reduce', false)); render();
    } else if (e.key === 'h') {
      pref.set('hot', !pref.get('hot', false)); render();
    } else if ((e.key === 'ArrowRight' || e.key === 'ArrowLeft') && p.v) {
      step(e.key === 'ArrowRight' ? 1 : -1);
    } else if (e.key === 'g') {
      location.href = 'index.html#' + Kit.hash.build({ v: p.v, theme: p.theme });
    }
  });

  window.addEventListener('hashchange', render);
  render();
})();
