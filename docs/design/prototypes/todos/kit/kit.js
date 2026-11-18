/*
 * Persona To-dos prototype kit: core.
 *
 *   Kit.variant(id, { name, summary, order })        register a design variant
 *   Kit.frame(variantId, frameId, { device, title, note, important, chrome, render(ctx) })
 *   Kit.device(kind, { content, …options })           draw a device with its system chrome
 *   Kit.renderFrame(variantId, frameId, env)          → { html, w, h, frame } (used by frame.html and index.html)
 *
 * Plain script (no modules, no fetch) so the pages work from file://. Components live in kit/components.js
 * (Kit.ui), icons in kit/icons.js (Kit.icon). See README.md for the whole API.
 */
(function () {
  const Kit = (window.Kit = window.Kit || {});
  Kit.version = '1.0.0';

  /* ---------------------------------------------------------------- utilities */

  const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  /** Escape text for HTML. Component text options accept HTML on purpose; escape data you did not write. */
  Kit.esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ESC[c]);
  /** Join an array of HTML strings, skipping empty values. */
  Kit.join = (list, sep) => (list || []).filter((x) => x !== false && x != null && x !== '').join(sep || '');
  /** `cond ? html : ''` */
  Kit.when = (cond, html) => (cond ? (typeof html === 'function' ? html() : html) : '');
  /** Class list helper: Kit.cls('a', cond && 'b') → 'a b' */
  Kit.cls = (...names) => names.filter(Boolean).join(' ');
  /** data-go attribute for a frame id ('iphone/detail', or 'V2:iphone/detail' for another variant), with an optional
      transition frame.html plays on the way: 'push' | 'pop' | 'fade' | 'pane-open' | 'pane-close' | 'none'. */
  Kit.goAttr = (id, transition) => (id ? ` data-go="${Kit.esc(id)}"${transition ? ` data-transition="${Kit.esc(transition)}"` : ''}` : '');
  /** data-toast attribute: a tap shows a 2-s toast naming an unbuilt destination ("Opens: Search To-dos"). */
  Kit.toastAttr = (text) => (text ? ` data-toast="${Kit.esc(text)}"` : '');
  /** Every navigation attribute of a component option object: { go, transition, toast }. */
  Kit.navAttrs = (o) => (o ? Kit.goAttr(o.go, o.transition) + (o.go ? '' : Kit.toastAttr(o.toast)) : '');

  /** The time and date the status bars show: window.Data.clock when the data sets one, else 9:41. */
  Kit.clock = () => Object.assign({ time: '9:41', date: 'Tue Nov 18' }, (window.Data && window.Data.clock) || {});

  /** Session state for live click-throughs (drafts, answers made on the way). frame.html's Reset clears it. Frames must
      still render statically for screenshots: read it only when ctx.live is true. */
  Kit.store = {
    get(key, fallback) {
      try { const v = sessionStorage.getItem('kit:' + key); return v == null ? fallback : JSON.parse(v); } catch (e) { return fallback; }
    },
    set(key, value) { try { sessionStorage.setItem('kit:' + key, JSON.stringify(value)); } catch (e) { /* file:// without storage */ } },
    clear() { try { sessionStorage.clear(); } catch (e) { /* ignore */ } },
  };

  const injected = {};
  /** Add a <style> once per key. Use it from a variant's screens.js: Kit.css('v1', `[data-variant="V1"] .x{…}`) */
  Kit.css = function css(key, text) {
    if (text === undefined) { text = key; key = 'css-' + Object.keys(injected).length; }
    if (injected[key]) { injected[key].textContent = text; return; }
    const el = document.createElement('style');
    el.setAttribute('data-kit-css', key);
    el.textContent = text;
    document.head.appendChild(el);
    injected[key] = el;
  };

  /* ---------------------------------------------------------------- devices */

  // Sizes in points (CSS px = pt / dp). Safe areas: where system chrome sits over the content.
  // Duo sizes and side-column metrics from docs/apple/ios.md ("iPhone Duo") and Theme.Shell.
  const DEVICES = {
    'iphone': { w: 402, h: 874, label: 'iPhone 17 Pro', group: 'iPhone', radius: 62, kb: 336,
      safe: { top: 62, bottom: 34 } },
    'duo-outer': { w: 466, h: 678, label: 'iPhone Duo · outer display', group: 'iPhone Duo', radius: 44, kb: 300, kbEstimate: true,
      side: true, camera: true, sideTop: 174, safe: { top: 0, bottom: 20 } },
    'duo-inner-portrait': { w: 669, h: 951, label: 'iPhone Duo · open, upright', group: 'iPhone Duo', radius: 30, kb: 300, kbEstimate: true,
      crease: 'v', safe: { top: 38, bottom: 20 } },
    'duo-inner-landscape': { w: 951, h: 669, label: 'iPhone Duo · open, on its side', group: 'iPhone Duo', radius: 30, kb: 300, kbEstimate: true,
      side: true, sideTop: 124, crease: 'h', safe: { top: 0, bottom: 20 } },
    'ipad11-landscape': { w: 1194, h: 834, label: 'iPad 11″ · landscape', group: 'iPad', radius: 18, kb: 360,
      safe: { top: 24, bottom: 20 } },
    'ipad11-portrait': { w: 834, h: 1194, label: 'iPad 11″ · portrait', group: 'iPad', radius: 18, kb: 300,
      safe: { top: 24, bottom: 20 } },
    'ipad13-landscape': { w: 1376, h: 1032, label: 'iPad 13″ · landscape', group: 'iPad', radius: 18, kb: 380,
      safe: { top: 24, bottom: 20 } },
    'ipad13-portrait': { w: 1032, h: 1376, label: 'iPad 13″ · portrait', group: 'iPad', radius: 18, kb: 320,
      safe: { top: 24, bottom: 20 } },
    'web': { w: 1440, h: 900, label: 'Web · 1440 × 900', group: 'Web', radius: 10, web: true,
      safe: { top: 0, bottom: 0 } },
    'android-fold': { w: 673, h: 841, label: 'Android foldable · inner', group: 'Android', radius: 26, kb: 300,
      crease: 'v', android: true, safe: { top: 28, bottom: 24 } },
    // No system chrome: spec sheets, flows, component catalogs.
    'canvas': { w: 1440, h: 1400, label: 'Canvas · 1440 × 1400 (no device)', group: 'Canvas', radius: 0, canvas: true,
      safe: { top: 0, bottom: 0 } },
  };
  Kit.devices = DEVICES;
  Kit.deviceGroups = ['iPhone', 'iPhone Duo', 'iPad', 'Web', 'Android', 'Canvas'];
  Kit.deviceOrder = Object.keys(DEVICES);

  const icon = (n, s, o) => Kit.icon(n, s, o);

  function battery(level) {
    return `<span class="dev-batt" aria-hidden="true"><i style="width:${Math.round((level || 1) * 100)}%"></i></span>`;
  }

  function iphoneStatus() {
    return `<div class="dev-status dev-status--iphone">
      <span class="dev-time">${Kit.esc(Kit.clock().time)}</span>
      <span class="dev-island"></span>
      <span class="dev-status-r">${icon('status-signal', 18)}${icon('status-wifi', 17)}${battery(0.8)}</span>
    </div>`;
  }

  function ipadStatus() {
    return `<div class="dev-status dev-status--ipad">
      <span class="dev-time">${Kit.esc(Kit.clock().time)} <span class="dev-date">${Kit.esc(Kit.clock().date)}</span></span>
      <span class="dev-status-r">${icon('status-wifi', 15)}<span class="dev-pct">100%</span>${battery(1)}</span>
    </div>`;
  }

  function duoPortraitStatus() {
    return `<div class="dev-status dev-status--duo">
      <span class="dev-time">${Kit.esc(Kit.clock().time)}</span>
      <span class="dev-status-r">${icon('status-signal', 17)}${icon('status-wifi', 16)}${battery(0.8)}</span>
    </div>`;
  }

  function androidStatus() {
    return `<div class="dev-status dev-status--android">
      <span class="dev-time">${Kit.esc(Kit.clock().time)}</span>
      <span class="dev-status-r">${icon('status-wifi', 15)}${icon('status-signal', 14)}<span class="dev-abatt"><i></i></span></span>
    </div>`;
  }

  function webChrome(o) {
    const url = Kit.esc(o.url || 'persona.example.com/todos');
    return `<div class="dev-browser">
      <div class="dev-browser-tabs">
        <span class="dev-lights"><i></i><i></i><i></i></span>
        <span class="dev-tab">${Kit.ui ? Kit.ui.face(16) : ''}<span>${Kit.esc(o.tabTitle || 'Persona · To-dos')}</span>${icon('xmark', 11)}</span>
        <span class="dev-tab-plus">${icon('plus', 13)}</span>
      </div>
      <div class="dev-browser-bar">
        ${icon('chevron-left', 16)}${icon('chevron-right', 16)}${icon('arrow-clockwise', 15)}
        <span class="dev-url">${icon('info-circle', 13)}<span>${url}</span></span>
        ${icon('ellipsis', 16)}
      </div>
    </div>`;
  }

  /* Duo side column: system status, then the page's Back, its toolbar items (symbol over a short name),
     and the Duo tab bar in the lower middle with the shell rail's buttons at the foot. */
  function sideItem(it, cls) {
    if (typeof it === 'string') return it;
    const glyph = it.face ? Kit.ui.face(26) : icon(it.icon || 'ellipsis', 21);
    return `<span class="${cls}${it.active ? ' is-active' : ''}"${Kit.goAttr(it.go, it.transition)} title="${Kit.esc(it.label || '')}">${glyph}${it.label ? `<span class="dev-side-label">${Kit.esc(it.label)}</span>` : ''}</span>`;
  }

  function duoTabs(tabs, axis) {
    const t = tabs === true ? {} : tabs;
    const items = t.items || [
      { icon: 'sparkle', label: 'Assistant' },
      { icon: 'agents', label: 'Agents', count: t.agentsCount != null ? t.agentsCount : 2 },
    ];
    const sel = t.selected || 0;
    return `<div class="dev-tabbar dev-tabbar--${axis}">${items.map((it, i) => `
      <span class="dev-tab-item${i === sel ? ' is-on' : ''}"${Kit.goAttr(it.go, it.transition)}>
        <span class="dev-tab-well">${icon(it.icon, 21)}${it.count ? `<span class="dev-tab-count">${it.count}</span>` : ''}</span>
        <span class="dev-tab-name">${Kit.esc(it.label)}</span>
      </span>`).join('')}</div>`;
  }

  function sideColumn(spec, d, o) {
    const s = spec === true ? {} : spec || {};
    const top = d.sideTop;
    let html = '<div class="dev-side">';
    if (d.camera) html += '<span class="dev-camera"></span>';
    html += `<div class="dev-side-status${d.camera ? ' has-camera' : ''}">
      <span class="dev-time">${Kit.esc(Kit.clock().time)}</span>${icon('status-signal', 15)}${icon('status-wifi', 15)}${battery(0.8)}
    </div>`;
    let y = top;
    if (s.back) {
      const back = s.back === true ? { icon: 'chevron-left', label: 'Back' } : typeof s.back === 'string' ? { icon: 'chevron-left', label: s.back } : s.back;
      html += `<span class="dev-side-back" style="top:${y}px"${Kit.goAttr(back.go, back.transition)} title="${Kit.esc(back.label || 'Back')}">${icon(back.icon || 'chevron-left', 22)}</span>`;
      y += 48 + 12;
    }
    if (s.actions && s.actions.length) {
      html += `<div class="dev-side-actions" style="top:${y}px">${s.actions.map((a) => sideItem(a, 'dev-side-item')).join('')}</div>`;
    }
    if (s.search) {
      html += `<span class="dev-side-search" style="bottom:${(d.safe.bottom || 0) + 38 - 48}px">${icon('magnifying-glass', 21)}</span>`;
    }
    // The tab bar's foot stands 178.7 pt above the screen's foot on both displays (Theme.Shell.sideRail…).
    if (s.tabs) html += `<div class="dev-side-tabs" style="bottom:178.7px">${duoTabs(s.tabs, 'v')}</div>`;
    const foot = s.foot === undefined || s.foot === 'auto'
      ? (s.tabs && d.camera ? [{ face: true, label: '' }, { icon: 'square-pencil' }] : [])
      : s.foot || [];
    if (foot.length) {
      html += `<div class="dev-side-foot">${foot.slice().reverse().map((f) => sideItem(f, 'dev-side-rail')).join('')}</div>`;
    }
    html += '</div>';
    return html;
  }

  /**
   * Kit.device(kind, options) → HTML of one device screen with its system chrome.
   * options: content (HTML), bg (CSS color for the screen, default var(--page)), statusBar (true), home (true),
   *   keyboard (true | height | { height, predictive, suggestions, label }; the Duo's ≈ 300 is labelled "to measure"), side ({ back, actions, tabs, foot, search } on Duo outer / inner landscape),
   *   tabs (Duo inner portrait: the Duo tab bar along the foot), crease ('flat' | 'half' | 'off'),
   *   windowControls (iPad), browser (web: draw a minimal browser window), url, cls, attrs.
   */
  Kit.device = function device(kind, o) {
    o = o || {};
    const d = DEVICES[kind];
    if (!d) return `<div class="k-error">Unknown device “${Kit.esc(kind)}”. Known: ${Object.keys(DEVICES).join(', ')}</div>`;
    const kbo = o.keyboard && typeof o.keyboard === 'object' ? o.keyboard : {};
    const kb = o.keyboard ? (typeof o.keyboard === 'number' ? o.keyboard : kbo.height || d.kb) : 0;
    const chromeTop = d.web && o.browser ? 76 : 0;
    const sideW = d.side ? 84 : 0;
    const vars = [
      `--dev-w:${d.w}px`, `--dev-h:${d.h}px`, `--dev-radius:${d.radius}px`,
      `--dev-bg:${o.bg || 'var(--page)'}`,
      `--safe-top:${d.safe.top}px`, `--dev-safe-top:${d.safe.top}px`,
      `--safe-bottom:${kb ? kb : d.safe.bottom}px`,
      `--home-room:${d.safe.bottom}px`,
      `--kb-h:${kb}px`, `--side-w:${sideW}px`, `--chrome-top:${chromeTop}px`,
      `--content-w:${d.w - sideW}px`, `--content-h:${d.h - chromeTop}px`,
    ];
    if (o.windowControls) vars.push('--wc-room:76px');
    const cls = Kit.cls('dev', 'dev--' + kind, d.side && 'dev--side', d.web && 'k-web', d.android && 'dev--android',
      kb && 'has-kb', o.cls);
    let html = `<div class="${cls}" data-device="${kind}" style="${vars.join(';')}"${o.attrs ? ' ' + o.attrs : ''}>`;
    html += '<div class="dev-screen">';
    if (chromeTop) html += webChrome(o);
    html += `<div class="dev-content">${o.content || ''}</div>`;

    const crease = o.crease || 'flat';
    if (d.crease && crease !== 'off') {
      html += `<div class="dev-crease dev-crease--${d.crease}${crease === 'half' ? ' is-half' : ''}${d.android ? ' is-hinge' : ''}"></div>`;
    }
    if (o.windowControls) html += '<span class="dev-wc"><i></i><i></i><i></i></span>';
    if (kb) {
      const label = kbo.label !== undefined ? kbo.label : d.kbEstimate ? `keyboard ≈ ${kb} pt (to measure)` : '';
      html += `<div class="dev-kb">${Kit.ui.keyboard(Object.assign({ wide: d.w - sideW > 700 }, kbo, { height: kb, label }))}</div>`;
    }
    if (o.statusBar !== false) {
      if (kind === 'iphone') html += iphoneStatus();
      else if (kind.startsWith('ipad')) html += ipadStatus();
      else if (kind === 'duo-inner-portrait') html += duoPortraitStatus();
      else if (d.android) html += androidStatus();
    }
    if (d.side) html += sideColumn(o.side, d, o);
    if (o.tabs && kind === 'duo-inner-portrait') html += `<div class="dev-foot-tabs">${duoTabs(o.tabs, 'h')}</div>`;
    if (o.home !== false && !d.web && !d.canvas) html += `<span class="dev-home${d.android ? ' dev-home--android' : ''}"></span>`;
    html += '</div></div>';
    return html;
  };

  /* ---------------------------------------------------------------- registry */

  const variants = [];
  const byId = {};

  /** Register (or update) a variant. order: lower sorts first in the gallery (the kit demo uses 99). */
  Kit.variant = function variant(id, meta) {
    meta = meta || {};
    let v = byId[id];
    if (!v) {
      v = { id, name: id, summary: '', order: 50, seq: variants.length, frames: [] };
      byId[id] = v;
      variants.push(v);
    }
    Object.assign(v, meta, { id, frames: v.frames });
    return v;
  };

  // A frame id may carry a state: '<frame>~<state>' ('iphone/list~calm'), registered like any frame.
  const FRAME_ID = /^[A-Za-z0-9][A-Za-z0-9/_.-]*(~[A-Za-z0-9_.-]+)?$/;
  /** 'iphone/list~calm' → 'iphone/list' */
  Kit.baseOf = (id) => String(id || '').split('~')[0];
  /** 'iphone/list~calm' → 'calm' ('' without a state) */
  Kit.stateOf = (id) => String(id || '').split('~')[1] || '';

  /** Register a frame of a variant. def: { device, title, note, important, chrome, render(ctx) → HTML | { content, …device options },
      fold, unfold (Duo: the frame frame.html's Fold / Unfold opens), auto: { go, after (ms), transition } (a timed step,
      e.g. Sending → Processing; frame.html runs it unless the hash has shot=1) } */
  Kit.frame = function frame(variantId, frameId, def) {
    def = def || {};
    const v = byId[variantId] || Kit.variant(variantId, {});
    if (!FRAME_ID.test(frameId)) console.warn(`[kit] frame id “${frameId}” should use letters, digits and / _ . - only (plus one ~state)`);
    if (!DEVICES[def.device]) console.warn(`[kit] frame ${variantId}:${frameId} has unknown device “${def.device}”`);
    const f = Object.assign({ title: frameId, note: '', important: false }, def, { id: frameId, variant: variantId });
    const at = v.frames.findIndex((x) => x.id === frameId);
    if (at >= 0) { console.warn(`[kit] frame ${variantId}:${frameId} registered twice; the last one wins`); v.frames[at] = f; }
    else v.frames.push(f);
    return f;
  };

  Kit.variants = () => variants.slice().sort((a, b) => (a.order - b.order) || (a.seq - b.seq));
  Kit.getVariant = (id) => byId[id] || null;
  Kit.frames = (variantId) => (byId[variantId] ? byId[variantId].frames.slice() : []);
  Kit.findFrame = (variantId, frameId) => (byId[variantId] ? byId[variantId].frames.find((f) => f.id === frameId) || null : null);

  /** The context a frame's render(ctx) receives. */
  Kit.context = function context(variantId, f, env) {
    env = env || {};
    const d = DEVICES[f.device] || { w: 0, h: 0 };
    return {
      variant: variantId,
      frameId: f.id,
      baseId: Kit.baseOf(f.id),             // 'iphone/list' for 'iphone/list~calm'
      state: Kit.stateOf(f.id),             // 'calm' for 'iphone/list~calm', '' otherwise
      shot: !!env.shot,                     // frame.html with &shot=1 (screenshots): render statically
      live: (env.mode || 'frame') === 'frame' && !env.shot, // a click-through in frame.html: Kit.store may be read
      frame: f,
      device: f.device,
      size: { w: d.w, h: d.h },
      theme: env.theme || document.documentElement.getAttribute('data-theme') || 'light',
      mode: env.mode || 'frame',            // 'frame' (frame.html) or 'gallery' (index.html)
      data: window.Data,
      ui: Kit.ui,
      icon: Kit.icon,
      esc: Kit.esc,
      join: Kit.join,
      when: Kit.when,
      /** ctx.go('iphone/detail', 'push') → ' data-go="iphone/detail" data-transition="push"' for a clickable element */
      go: (id, transition) => Kit.goAttr(id, transition),
      /** ctx.toast('Opens: Search To-dos') → ' data-toast="…"': a 2-s toast for a destination the prototype doesn't build */
      toast: (text) => Kit.toastAttr(text),
      store: Kit.store,
    };
  };

  function errorBox(where, err) {
    console.error(err);
    return `<div class="k-error"><b>${Kit.esc(where)}</b><pre>${Kit.esc(err && (err.stack || err.message) || err)}</pre></div>`;
  }

  /** Render one registered frame inside its device. → { html, w, h, frame } or null when unknown. */
  Kit.renderFrame = function renderFrame(variantId, frameId, env) {
    const f = Kit.findFrame(variantId, frameId);
    if (!f) return null;
    const d = DEVICES[f.device] || { w: 400, h: 300 };
    const opts = Object.assign({}, f.chrome || {});
    try {
      const out = typeof f.render === 'function' ? f.render(Kit.context(variantId, f, env)) : '';
      if (typeof out === 'string') opts.content = out;
      else if (out && typeof out === 'object') Object.assign(opts, out);
    } catch (err) {
      opts.content = errorBox(`${variantId}:${frameId} failed to render`, err);
    }
    opts.attrs = Kit.join([opts.attrs, `data-variant="${Kit.esc(variantId)}"`, `data-frame="${Kit.esc(frameId)}"`], ' ');
    let html;
    try { html = Kit.device(f.device, opts); }
    catch (err) { html = errorBox(`${variantId}:${frameId}: device chrome failed`, err); }
    return { html, w: d.w, h: d.h, frame: f };
  };

  /** After frames are in the DOM: mark one-line strips that overflow (capsule rows, filter bars) so they fade at the
      trailing edge instead of being cut hard. frame.html and index.html call it; call it yourself if you render elsewhere. */
  Kit.afterRender = function afterRender(root) {
    (root || document).querySelectorAll('.k-caps, .k-filters-in, .k-sbar-in, .k-res-in').forEach((el) => {
      // A capsule row reveals its whole selected chip (as the Inbox's bar scrolls to the chosen entry), then fades the
      // edge that is cut: the trailing edge, or the leading one once the row is scrolled to its end.
      const on = el.querySelector('.is-on');
      if (on && el.scrollWidth > el.clientWidth + 1) {
        const right = on.offsetLeft + on.offsetWidth - el.offsetLeft;
        if (right > el.clientWidth) el.scrollLeft = right - el.clientWidth + 8;
      }
      const cut = el.scrollWidth > el.clientWidth + 1;
      el.classList.toggle('is-overflowing', cut && el.scrollLeft + el.clientWidth < el.scrollWidth - 1);
      el.classList.toggle('is-scrolled', cut && el.scrollLeft > 0);
    });
  };

  /* ---------------------------------------------------------------- hash state */

  Kit.hash = {
    parse(str) {
      const out = {};
      String(str == null ? location.hash : str).replace(/^#/, '').split('&').forEach((pair) => {
        if (!pair) return;
        const i = pair.indexOf('=');
        const k = decodeURIComponent(i < 0 ? pair : pair.slice(0, i));
        out[k] = i < 0 ? '' : decodeURIComponent(pair.slice(i + 1));
      });
      return out;
    },
    build(obj) {
      return Object.keys(obj).filter((k) => obj[k] !== undefined && obj[k] !== null && obj[k] !== false)
        .map((k) => (obj[k] === '' || obj[k] === true ? encodeURIComponent(k) : `${encodeURIComponent(k)}=${encodeURIComponent(obj[k]).replace(/%2F/g, '/').replace(/%3A/g, ':')}`))
        .join('&');
    },
  };

  /** Resolve a data-go value against the current variant: 'f' or 'V2:f' → { v, f } */
  Kit.resolveGo = function resolveGo(value, currentVariant) {
    const i = value.indexOf(':');
    return i > 0 ? { v: value.slice(0, i), f: value.slice(i + 1) } : { v: currentVariant, f: value };
  };

  Kit.setTheme = function setTheme(theme) {
    document.documentElement.setAttribute('data-theme', theme === 'dark' ? 'dark' : 'light');
  };
})();
