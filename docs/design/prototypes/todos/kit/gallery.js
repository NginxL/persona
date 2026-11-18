/*
 * index.html: the gallery. A variant switcher, a theme toggle, and every frame of the chosen variant drawn in
 * its device and scaled down, grouped by device with the ★ (important) frames first; a frame's states
 * ("<frame>~<state>") follow their frame. Each tile shows the frame's title and note. Compare shows each frame id
 * across the variants side by side.
 *
 *   index.html#v=V1&theme=dark&zoom=0.5[&key]     one variant (key: key frames only)
 *   index.html#compare&theme=light                 compare mode
 *
 * A tile opens frame.html for that frame in the same tab; a click on something with data-go inside a tile
 * opens the frame it points to.
 */
(function () {
  const root = document.getElementById('gallery');
  const ZOOMS = [0.33, 0.5, 0.75, 1];

  function state() {
    const p = Kit.hash.parse();
    const vs = Kit.variants();
    const firstWithFrames = vs.find((v) => v.frames.length) || vs[0];
    return {
      v: p.v && Kit.getVariant(p.v) ? p.v : firstWithFrames ? firstWithFrames.id : '',
      compare: 'compare' in p,
      theme: p.theme === 'dark' ? 'dark' : 'light',
      zoom: ZOOMS.indexOf(parseFloat(p.zoom)) >= 0 ? parseFloat(p.zoom) : 0.5,
      key: 'key' in p,
    };
  }
  function link(s, patch) {
    const n = Object.assign({}, s, patch);
    return '#' + Kit.hash.build({ compare: n.compare ? '' : undefined, v: n.compare ? undefined : n.v, theme: n.theme, zoom: n.zoom === 0.5 ? undefined : n.zoom, key: n.key ? '' : undefined });
  }
  const frameHref = (v, f, theme) => 'frame.html#' + Kit.hash.build({ v, f, theme });

  function tile(v, f, s, label) {
    const out = Kit.renderFrame(v, f.id, { theme: s.theme, mode: 'gallery' });
    const z = s.zoom;
    const d = Kit.devices[f.device] || {};
    return `<a class="g-tile${d.web ? ' is-web' : ''}" href="${frameHref(v, f.id, s.theme)}" data-v="${Kit.esc(v)}">
      ${label ? `<div class="g-tile-variant">${label}</div>` : ''}
      <div class="g-shot" style="--z:${z};--r:${d.radius || 0}px;width:${Math.round(out.w * z)}px;height:${Math.round(out.h * z)}px">
        <div class="g-dev" style="zoom:${z}">${out.html}</div>
      </div>
      <div class="g-cap" style="max-width:${Math.max(220, Math.round(out.w * z))}px">
        <div class="g-cap-title">${f.important ? '<i class="g-star" title="Important frame">★</i>' : ''}<b>${Kit.esc(f.title)}</b>${Kit.stateOf(f.id) ? `<i class="g-state" title="A state of ${Kit.esc(Kit.baseOf(f.id))}">~${Kit.esc(Kit.stateOf(f.id))}</i>` : ''}</div>
        ${f.note ? `<div class="g-cap-note">${f.note}</div>` : ''}
        <code>${Kit.esc(f.id)}${f.fold ? ` · fold → ${Kit.esc(f.fold)}` : ''}${f.unfold ? ` · unfold → ${Kit.esc(f.unfold)}` : ''}</code>
      </div>
    </a>`;
  }

  function placeholder(f, s, label) {
    const d = Kit.devices[f.device] || { w: 400, h: 300 };
    return `<div class="g-tile is-missing">${label ? `<div class="g-tile-variant">${label}</div>` : ''}<div class="g-shot g-missing" style="width:${Math.round(d.w * s.zoom)}px;height:${Math.round(d.h * s.zoom)}px"><span>Not drawn</span></div></div>`;
  }

  function topBar(s) {
    const vs = Kit.variants();
    const tabs = vs.map((v) => `<a class="g-tab${!s.compare && v.id === s.v ? ' is-on' : ''}" href="${link(s, { v: v.id, compare: false })}"><b>${Kit.esc(v.id)}</b><span>${Kit.esc(v.name)}</span><em>${v.frames.length}</em></a>`).join('');
    return `<header class="g-top">
      <div class="g-brand">${Kit.ui.face(26)}<div><b>To-dos prototypes</b><span>Persona · static kit</span></div></div>
      <nav class="g-tabs">${tabs}<span class="g-tabs-sep"></span><a class="g-tab${s.compare ? ' is-on' : ''}" href="${link(s, { compare: true })}"><b>Compare</b><span>same frame, side by side</span></a></nav>
      <div class="g-tools">
        <a class="g-chip${s.key ? ' is-on' : ''}" href="${link(s, { key: !s.key })}">★ Frames Only</a>
        <span class="g-seg">${ZOOMS.map((z) => `<a class="${z === s.zoom ? 'is-on' : ''}" href="${link(s, { zoom: z })}">${Math.round(z * 100)}%</a>`).join('')}</span>
        <span class="g-seg">${['light', 'dark'].map((t) => `<a class="${t === s.theme ? 'is-on' : ''}" href="${link(s, { theme: t })}">${t === 'light' ? 'Light' : 'Dark'}</a>`).join('')}</span>
      </div>
    </header>`;
  }

  /** Order within a device: ★ families first (a frame and its ~states), registration order otherwise. */
  function ordered(list) {
    const fams = [];
    const byBase = {};
    list.forEach((f, i) => {
      const base = Kit.baseOf(f.id);
      let fam = byBase[base];
      if (!fam) { fam = byBase[base] = { base, seq: i, frames: [], important: false }; fams.push(fam); }
      fam.frames.push(f);
      if (f.important) fam.important = true;
    });
    fams.forEach((fam) => fam.frames.sort((a, b) => (Kit.stateOf(a.id) ? 1 : 0) - (Kit.stateOf(b.id) ? 1 : 0)));
    fams.sort((a, b) => (b.important - a.important) || (a.seq - b.seq));
    return fams.reduce((out, fam) => out.concat(fam.frames), []);
  }

  function byDevice(frames) {
    const groups = [];
    Kit.deviceOrder.forEach((kind) => {
      const list = ordered(frames.filter((f) => f.device === kind));
      if (list.length) groups.push({ kind, list });
    });
    const unknown = frames.filter((f) => !Kit.devices[f.device]);
    if (unknown.length) groups.push({ kind: 'unknown', list: unknown });
    return groups;
  }

  function renderVariant(s) {
    const v = Kit.getVariant(s.v);
    if (!v) return '<div class="g-empty">No variants registered yet.</div>';
    let frames = v.frames;
    if (s.key) frames = frames.filter((f) => f.important);
    const groups = byDevice(frames);
    const body = groups.map((g) => {
      const d = Kit.devices[g.kind];
      return `<section class="g-group"><h2>${d ? Kit.esc(d.label) : 'Unknown device'}<span>${d ? `${d.w} × ${d.h}` : ''}</span></h2>
        <div class="g-grid">${g.list.map((f) => tile(v.id, f, s)).join('')}</div></section>`;
    }).join('');
    return `<section class="g-intro"><h1>${Kit.esc(v.id)} · ${Kit.esc(v.name)}</h1>${v.summary ? `<p>${v.summary}</p>` : ''}</section>
      ${body || `<div class="g-empty">No ${s.key ? 'key ' : ''}frames yet. Register some in <code>${Kit.esc(v.id.toLowerCase())}/screens.js</code> with <code>Kit.frame('${Kit.esc(v.id)}', …)</code>.</div>`}`;
  }

  function renderCompare(s) {
    let vs = Kit.variants().filter((v) => v.frames.length && v.order < 99);
    if (!vs.length) vs = Kit.variants().filter((v) => v.frames.length);
    const ids = [];
    const first = {};
    vs.forEach((v) => v.frames.forEach((f) => {
      if (s.key && !f.important) return;
      if (!first[f.id]) { first[f.id] = f; ids.push(f.id); }
    }));
    const rank = {};
    Kit.deviceOrder.forEach((kind) => ordered(ids.map((id) => first[id]).filter((f) => f.device === kind)).forEach((f, i) => { rank[f.id] = i; }));
    ids.sort((a, b) => (Kit.deviceOrder.indexOf(first[a].device) - Kit.deviceOrder.indexOf(first[b].device)) || (rank[a] - rank[b]));
    const rows = ids.map((id) => `<section class="g-group g-compare"><h2>${Kit.esc(id)}<span>${Kit.esc((Kit.devices[first[id].device] || {}).label || first[id].device)}</span></h2>
      <div class="g-grid">${vs.map((v) => {
        const f = Kit.findFrame(v.id, id);
        const label = `<b>${Kit.esc(v.id)}</b> ${Kit.esc(v.name)}`;
        return f ? tile(v.id, f, s, label) : placeholder(first[id], s, label);
      }).join('')}</div></section>`).join('');
    return `<section class="g-intro"><h1>Compare</h1><p>Each frame id across ${vs.map((v) => Kit.esc(v.id)).join(', ') || 'the variants'}${vs.length > 1 ? ', side by side' : ''}. Use the same frame ids in every variant so they line up here.</p></section>${rows || '<div class="g-empty">No frames to compare yet.</div>'}`;
  }

  function render() {
    const s = state();
    Kit.setTheme(s.theme);
    document.title = s.compare ? 'Compare · To-dos prototypes' : `${s.v} · To-dos prototypes`;
    root.innerHTML = topBar(s) + `<main class="g-main">${s.compare ? renderCompare(s) : renderVariant(s)}</main>`;
    Kit.afterRender(root);
  }

  root.addEventListener('click', (e) => {
    const el = e.target.closest && e.target.closest('[data-go]');
    if (!el) return;
    const tileEl = el.closest('.g-tile');
    if (!tileEl) return;
    e.preventDefault();
    // In the gallery a data-go inside a tile opens the frame it points to (without its transition).
    const t = Kit.resolveGo(el.getAttribute('data-go'), tileEl.getAttribute('data-v'));
    location.href = frameHref(t.v, t.f, state().theme);
  });
  window.addEventListener('hashchange', render);
  render();
})();
