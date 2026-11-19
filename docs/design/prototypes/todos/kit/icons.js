/*
 * Kit.icon(name, size?, opts?) → inline SVG drawn on a 24-unit grid to read like an SF Symbol
 * (regular weight, rounded caps and joins). Names follow the SF Symbol with dots turned into dashes
 * ("checkmark.circle.fill" → "checkmark-circle-fill"); meaning aliases (todo, inbox, due, …) map to them,
 * as Theme.Icon does in the app. Color is currentColor; size defaults to 1em.
 *
 * Add an icon: put its inner SVG in P below (stroke shapes inherit the stroke; filled parts carry
 * fill="currentColor" stroke="none"). A filled shape with a knocked-out mark (checkmark.circle.fill) is
 * { fill: '<shape …/>', cut: '<path …/>' }: the cut is masked out, so the surface shows through as in SF Symbols.
 */
(function () {
  const Kit = (window.Kit = window.Kit || {});
  const F = 'fill="currentColor" stroke="none"';

  // A gear outline built from teeth, so the glyph is our own drawing.
  function gear(cx, cy, teeth, rOut, rIn) {
    const pts = [];
    const step = (Math.PI * 2) / teeth;
    for (let i = 0; i < teeth; i++) {
      const a = i * step - Math.PI / 2;
      const w = step * 0.2;
      pts.push([a - step / 2 + w, rIn], [a - w * 1.1, rOut], [a + w * 1.1, rOut], [a + step / 2 - w, rIn]);
    }
    return 'M' + pts.map(([a, r]) => (cx + r * Math.cos(a)).toFixed(2) + ' ' + (cy + r * Math.sin(a)).toFixed(2)).join('L') + 'Z';
  }
  function rays(cx, cy, r1, r2, n) {
    let d = '';
    for (let i = 0; i < n; i++) {
      const a = (i * Math.PI * 2) / n;
      d += `M${(cx + r1 * Math.cos(a)).toFixed(2)} ${(cy + r1 * Math.sin(a)).toFixed(2)}L${(cx + r2 * Math.cos(a)).toFixed(2)} ${(cy + r2 * Math.sin(a)).toFixed(2)}`;
    }
    return d;
  }
  const bubble = 'M12 4.2c4.8 0 8.6 3.1 8.6 7.1s-3.8 7.1-8.6 7.1c-1.05 0-2.05-.15-2.98-.43L4.6 19.8l1.3-3.55C4.45 14.98 3.4 13.24 3.4 11.3 3.4 7.3 7.2 4.2 12 4.2z';

  const P = {
    // to-dos
    'checklist': '<path d="M3.6 6.4l1.6 1.6 2.9-3.1M3.6 12.4l1.6 1.6 2.9-3.1M3.6 18.4l1.6 1.6 2.9-3.1"/><path d="M11.5 6.6h9M11.5 12.6h9M11.5 18.6h9"/>',
    'circle': '<circle cx="12" cy="12" r="8.6"/>',
    'circle-dashed': '<circle cx="12" cy="12" r="8.6" stroke-dasharray="2.6 2.4"/>',
    'checkmark-circle': '<circle cx="12" cy="12" r="8.6"/><path d="M8.3 12.3l2.6 2.6 4.9-5.4"/>',
    'checkmark-circle-fill': { fill: '<circle cx="12" cy="12" r="9.6"/>', cut: '<path d="M7.9 12.3l2.8 2.8 5.4-5.8"/>' },
    'checkmark': '<path d="M5 12.6l4.4 4.4L19 7.4"/>',
    'plus': '<path d="M12 5v14M5 12h14"/>',
    'plus-circle-fill': { fill: '<circle cx="12" cy="12" r="9.6"/>', cut: '<path d="M12 7.6v8.8M7.6 12h8.8"/>' },
    'minus': '<path d="M5 12h14"/>',
    'calendar': '<rect x="3.6" y="5" width="16.8" height="15.4" rx="3"/><path d="M3.6 10h16.8M8 3v3.8M16 3v3.8"/>',
    'calendar-clock': '<path d="M20.4 10.8V8a3 3 0 0 0-3-3H6.6a3 3 0 0 0-3 3v9.4a3 3 0 0 0 3 3h4.2"/><path d="M3.6 10h16.8M8 3v3.8M16 3v3.8"/><circle cx="17.2" cy="17.2" r="4.4"/><path d="M17.2 15.2v2.2l1.4 1"/>',
    'bell': '<path d="M6 16.6v-5.4a6 6 0 0 1 12 0v5.4l1.6 2.1H4.4z"/><path d="M10 20.6a2.1 2.1 0 0 0 4 0"/>',
    'flag': '<path d="M5.6 21V4"/><path d="M5.6 4.6h11.6l-2.4 4.2 2.4 4.2H5.6"/>',
    'flag-fill': `<path d="M5.6 21V4"/><path d="M5.6 4.6h11.6l-2.4 4.2 2.4 4.2H5.6z" fill="currentColor"/>`,
    'project': '<path d="M12 3.4l8.6 4.6L12 12.6 3.4 8z"/><path d="M3.4 12.2l8.6 4.6 8.6-4.6M3.4 16.2l8.6 4.6 8.6-4.6"/>',
    'pin': '<path d="M9.2 3.6h5.6l-.9 5.3 3.3 3.6H6.8l3.3-3.6z"/><path d="M12 12.6v8"/>',
    'paperplane': '<path d="M20.8 3.2L10.4 13.6"/><path d="M20.8 3.2l-6.4 17.6-4-7.2-7.2-4z"/>',
    'arrow-turn-up-right': '<path d="M14.4 4.8l5.8 5.6-5.8 5.6"/><path d="M20 10.4H10.2A6 6 0 0 0 4.2 16.4v2.8"/>',
    'pr': '<circle cx="6.2" cy="5.6" r="2.2"/><circle cx="6.2" cy="18.4" r="2.2"/><circle cx="17.8" cy="18.4" r="2.2"/><path d="M6.2 7.8v8.4M17.8 16.2V9a3 3 0 0 0-3-3h-4.4"/><path d="M12.6 3.6L10.2 6l2.4 2.4"/>',
    'signature': '<path d="M3.2 16.6c2.2 0 3.6-7 5.6-7 1.6 0-.8 7.2 1.4 7.2 1.8 0 2.6-4.2 4.2-4.2 1.3 0 .8 3 2.3 3 1 0 2-1 3-2.2"/><path d="M3.2 20.4h17.6"/>',
    'clipboard-list': '<path d="M8.2 4.6H6.6a2 2 0 0 0-2 2v12.8a2 2 0 0 0 2 2h10.8a2 2 0 0 0 2-2V6.6a2 2 0 0 0-2-2h-1.6"/><rect x="8.2" y="3" width="7.6" height="3.4" rx="1.2"/><path d="M8.6 11h.01M11.2 11h4.6M8.6 15.2h.01M11.2 15.2h4.6"/>',
    'diamond-check': '<path d="M12 2.8l9.2 9.2-9.2 9.2L2.8 12z"/><path d="M8.4 12.2l2.5 2.5 4.7-4.9"/>',
    'trash': '<path d="M4.4 6.6h15.2M9.4 6.6V4.4h5.2v2.2"/><path d="M6.4 6.6l1 12.8a1.6 1.6 0 0 0 1.6 1.4h6a1.6 1.6 0 0 0 1.6-1.4l1-12.8"/><path d="M10 10.6v6M14 10.6v6"/>',
    'ellipsis': `<circle cx="5.6" cy="12" r="1.65" ${F}/><circle cx="12" cy="12" r="1.65" ${F}/><circle cx="18.4" cy="12" r="1.65" ${F}/>`,
    'ellipsis-circle': `<circle cx="12" cy="12" r="8.8"/><circle cx="8" cy="12" r="1.25" ${F}/><circle cx="12" cy="12" r="1.25" ${F}/><circle cx="16" cy="12" r="1.25" ${F}/>`,
    'sidebar-left': '<rect x="3" y="4.6" width="18" height="14.8" rx="3.4"/><path d="M9.4 4.6v14.8M5.6 8.4h1.4M5.6 11h1.4"/>',
    'sidebar-right': '<rect x="3" y="4.6" width="18" height="14.8" rx="3.4"/><path d="M14.6 4.6v14.8M17 8.4h1.4M17 11h1.4"/>',
    'bubble-exclamation': `<path d="${bubble}"/><path d="M12 7.8v4.1M12 14.7v.1"/>`,
    'bubble-exclamation-fill': { fill: `<path d="${bubble}"/>`, cut: '<path d="M12 7.8v4.1M12 14.7v.1"/>' },
    'bubble': `<path d="${bubble}"/>`,
    'bubble-left-right': '<path d="M15.4 8.4V6.2a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v5a2 2 0 0 0 2 2h.8v2.6l3-2.6"/><path d="M10.6 10.6a2 2 0 0 1 2-2H19a2 2 0 0 1 2 2v4.8a2 2 0 0 1-2 2h-.8V20l-3-2.6h-2.6a2 2 0 0 1-2-2z"/>',
    'gear': `<path d="${gear(12, 12, 8, 9.4, 7.3)}"/><circle cx="12" cy="12" r="2.9"/>`,
    'clock': '<circle cx="12" cy="12" r="8.6"/><path d="M12 7.4V12l3 2"/>',
    'clock-fill': { fill: '<circle cx="12" cy="12" r="9.4"/>', cut: '<path d="M12 7.4V12l3 2"/>', cutWidth: 1.9 },
    'tray-arrow-up': '<path d="M3.6 13.6V18a2.4 2.4 0 0 0 2.4 2.4h12a2.4 2.4 0 0 0 2.4-2.4v-4.4"/><path d="M3.6 13.6h4.2l1.3 2.4h5.8l1.3-2.4h4.2"/><path d="M12 3.6v8M8.8 6.8L12 3.6l3.2 3.2"/>',
    'arrow-uturn-left': '<path d="M9 14.2L4.2 9.4 9 4.6"/><path d="M4.2 9.4h10.4a5.4 5.4 0 0 1 0 10.8H11"/>',
    'arrow-clockwise': '<path d="M19.6 12a7.6 7.6 0 1 1-2.2-5.4"/><path d="M19.8 4v4.4h-4.4"/>',
    'list-bullet': `<circle cx="4.8" cy="6.4" r="1.25" ${F}/><circle cx="4.8" cy="12" r="1.25" ${F}/><circle cx="4.8" cy="17.6" r="1.25" ${F}/><path d="M9.2 6.4h11M9.2 12h11M9.2 17.6h11"/>`,
    'line-3-decrease': '<path d="M4 7h16M7 12h10M10 17h4"/>',
    'line-3-horizontal': '<path d="M4 6.6h16M4 12h16M4 17.4h16"/>',
    'chevron-right': '<path d="M9.4 5.4L16 12l-6.6 6.6"/>',
    'chevron-left': '<path d="M14.6 5.4L8 12l6.6 6.6"/>',
    'chevron-down': '<path d="M5.4 9.2L12 15.8l6.6-6.6"/>',
    'chevron-up': '<path d="M5.4 14.8L12 8.2l6.6 6.6"/>',
    'chevron-up-down': '<path d="M7.6 9.4L12 5l4.4 4.4M7.6 14.6L12 19l4.4-4.4"/>',
    'magnifying-glass': '<circle cx="10.4" cy="10.4" r="6.4"/><path d="M15.2 15.2l5.2 5.2"/>',
    'xmark': '<path d="M6.2 6.2l11.6 11.6M17.8 6.2L6.2 17.8"/>',
    'xmark-circle-fill': { fill: '<circle cx="12" cy="12" r="9.6"/>', cut: '<path d="M9 9l6 6M15 9l-6 6"/>', cutWidth: 1.9 },
    'exclamation-triangle': '<path d="M10.3 4.4a2 2 0 0 1 3.4 0l7.2 12.6a2 2 0 0 1-1.7 3H4.8a2 2 0 0 1-1.7-3z"/><path d="M12 9.6v4M12 16.6v.1"/>',
    'info-circle': '<circle cx="12" cy="12" r="8.6"/><path d="M12 11v5.2M12 7.9v.1"/>',

    // places and Persona
    'sparkle': `<path d="M12 2.6c.7 5 3.5 7.9 8.6 8.6v.01c-5.1.7-7.9 3.5-8.6 8.6v.01c-.7-5.1-3.5-7.9-8.6-8.6v-.01C8.5 10.5 11.3 7.6 12 2.6z" ${F}/>`,
    'sparkles': `<path d="M10 4.2c.6 4.2 2.9 6.6 7.1 7.2-4.2.6-6.5 3-7.1 7.2-.6-4.2-2.9-6.6-7.1-7.2 4.2-.6 6.5-3 7.1-7.2z" ${F}/><path d="M18.2 2.4c.25 1.8 1.2 2.8 3 3-1.8.25-2.75 1.2-3 3-.25-1.8-1.2-2.75-3-3 1.8-.25 2.75-1.2 3-3z" ${F}/>`,
    'tray-full': '<path d="M3.6 13.6V18a2.4 2.4 0 0 0 2.4 2.4h12a2.4 2.4 0 0 0 2.4-2.4v-4.4l-2.5-7.2A2.4 2.4 0 0 0 15.6 4.8H8.4A2.4 2.4 0 0 0 6.1 6.4z"/><path d="M3.6 13.6h4.2l1.3 2.4h5.8l1.3-2.4h4.2M8.2 8h7.6M7.4 10.8h9.2"/>',
    'books': '<rect x="3.4" y="4" width="4.2" height="16.4" rx="1"/><rect x="7.6" y="4" width="4.6" height="16.4" rx="1"/><path d="M14.4 5.4l3.7-1 4 15.2-3.7 1z"/><path d="M3.4 8h4.2M7.6 8h4.6M3.4 16.4h4.2M7.6 16.4h4.6"/>',
    'alarm': '<circle cx="12" cy="13" r="7"/><path d="M12 9.6V13l2.3 1.5M4.2 6.2l2.6-2.4M19.8 6.2l-2.6-2.4M7.2 18.8l-1.6 1.8M16.8 18.8l1.6 1.8"/>',
    'laptop-phone': '<path d="M3.4 14.6V6.6A1.6 1.6 0 0 1 5 5h10.4A1.6 1.6 0 0 1 17 6.6v1.2"/><path d="M1.6 17.8h12"/><rect x="15.6" y="9.6" width="6.4" height="10.6" rx="1.6"/><path d="M18.2 18h1.2"/>',
    'laptop': '<path d="M4.6 15.4V7a1.6 1.6 0 0 1 1.6-1.6h11.6A1.6 1.6 0 0 1 19.4 7v8.4"/><path d="M2.4 18.4h19.2"/>',
    'heart': '<path d="M12 20.2s-7.6-4.6-7.6-10.3A4.3 4.3 0 0 1 12 7.3a4.3 4.3 0 0 1 7.6 2.6c0 5.7-7.6 10.3-7.6 10.3z"/>',
    'square-pencil': '<path d="M11.2 4.6H6.6a2 2 0 0 0-2 2v10.8a2 2 0 0 0 2 2h10.8a2 2 0 0 0 2-2v-4.6"/><path d="M17.5 3.7a1.9 1.9 0 0 1 2.8 2.7l-7.9 7.9-3.6.9.9-3.6z"/>',
    'sun': `<circle cx="12" cy="12" r="4"/><path d="${rays(12, 12, 6.6, 8.9, 8)}"/>`,
    'sun-fill': `<circle cx="12" cy="12" r="4.6" ${F}/><path d="${rays(12, 12, 7, 9.2, 8)}" stroke-width="2"/>`,
    'agents': '<path d="M8 7.2L3.2 12 8 16.8M16 7.2l4.8 4.8-4.8 4.8M13.6 5l-3.2 14"/>',
    'person-circle': '<circle cx="12" cy="12" r="8.8"/><circle cx="12" cy="10" r="3.1"/><path d="M6.4 18.3c1.3-2.2 3.2-3.3 5.6-3.3s4.3 1.1 5.6 3.3"/>',
    'person-2': '<circle cx="9" cy="8.4" r="3.2"/><path d="M3.4 19.4c.7-3.2 2.8-5 5.6-5s4.9 1.8 5.6 5"/><path d="M15.4 5.4a3.2 3.2 0 0 1 0 6M17.4 14.6c1.8.6 3 2.2 3.4 4.8"/>',

    // composer and conversation
    'mic': '<rect x="9" y="3.2" width="6" height="11" rx="3"/><path d="M5.6 11a6.4 6.4 0 0 0 12.8 0M12 17.4v3.4"/>',
    'arrow-up': '<path d="M12 19.4V4.8M5.8 11l6.2-6.2 6.2 6.2"/>',
    'arrow-up-circle-fill': { fill: '<circle cx="12" cy="12" r="10"/>', cut: '<path d="M12 17V7.4M7.8 11.4L12 7.2l4.2 4.2"/>', cutWidth: 2.1 },
    'arrow-up-right': '<path d="M7 17L17 7M8.6 7H17v8.4"/>',
    'arrow-right': '<path d="M4.8 12h14.4M13 5.8l6.2 6.2-6.2 6.2"/>',
    'arrow-down': '<path d="M12 4.6v14.6M5.8 13l6.2 6.2 6.2-6.2"/>',
    'stop': `<rect x="6.2" y="6.2" width="11.6" height="11.6" rx="2.6" ${F}/>`,
    'stop-circle': `<circle cx="12" cy="12" r="8.8"/><rect x="9" y="9" width="6" height="6" rx="1.2" ${F}/>`,
    'play': `<path d="M8 5.4v13.2L18.6 12z" ${F}/>`,
    'hourglass': '<path d="M6.8 3.6h10.4M6.8 20.4h10.4"/><path d="M8 3.6c0 4 4 5.4 4 8.4s-4 4.4-4 8.4M16 3.6c0 4-4 5.4-4 8.4s4 4.4 4 8.4"/>',
    'eye': '<path d="M2.4 12S6 5.4 12 5.4 21.6 12 21.6 12 18 18.6 12 18.6 2.4 12 2.4 12z"/><circle cx="12" cy="12" r="3.1"/>',
    'folder': '<path d="M3.4 7.4a2 2 0 0 1 2-2h4l2 2.2h7.2a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2H5.4a2 2 0 0 1-2-2z"/><path d="M3.4 10.2h17.2"/>',
    'wand': `<path d="M4 20L15.2 8.8M13.6 7.2l3.2 3.2"/><path d="M18 2.6v3.2M16.4 4.2h3.2M20.6 8.8v2.4M19.4 10h2.4M10.4 2.8v2.4M9.2 4h2.4"/>`,
    'doc': '<path d="M6.6 3.4h7.2l4.6 4.6v11.4a1.6 1.6 0 0 1-1.6 1.6H6.6A1.6 1.6 0 0 1 5 19.4V5a1.6 1.6 0 0 1 1.6-1.6z"/><path d="M13.6 3.6V8.2h4.6M8.4 12.6h7.2M8.4 16.2h5"/>',
    'link': '<path d="M10.2 13.8a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1.1 1.1"/><path d="M13.8 10.2a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1.1-1.1"/>',
    'airplane': '<path d="M10.6 9.4V4.6a1.4 1.4 0 0 1 2.8 0v4.8l7.2 4.4v2l-7.2-2.2v4.6l2 1.6v1.6L12 20.2l-3.4 1v-1.6l2-1.6v-4.6l-7.2 2.2v-2z"/>',
    'bed': '<path d="M3 5.6v13.6M3 14.6h18v4.6M21 14.6v-2.8a3 3 0 0 0-3-3h-7.4v5.8"/><circle cx="6.8" cy="11.4" r="1.9"/>',
    'cart': '<path d="M2.8 4h2.4l2.3 11.2h10.4l2-7.6H6.2"/><circle cx="9" cy="19.2" r="1.4"/><circle cx="16.8" cy="19.2" r="1.4"/>',
    'wrench': '<path d="M20.4 6.6a4.6 4.6 0 0 1-6.1 4.6L7.4 18.1a2 2 0 0 1-2.9-2.9l6.9-6.9a4.6 4.6 0 0 1 4.6-6.1l-2.7 2.7.6 2.6 2.6.6z"/>',
    'globe': '<circle cx="12" cy="12" r="8.6"/><path d="M3.4 12h17.2M12 3.4c2.4 2.3 3.6 5.2 3.6 8.6s-1.2 6.3-3.6 8.6c-2.4-2.3-3.6-5.2-3.6-8.6S9.6 5.7 12 3.4z"/>',
    'face-smile': '<circle cx="12" cy="12" r="8.6"/><path d="M8.6 14.2a4.2 4.2 0 0 0 6.8 0"/><circle cx="9.2" cy="10" r=".9" fill="currentColor"/><circle cx="14.8" cy="10" r=".9" fill="currentColor"/>',
    'delete-left': '<path d="M8.6 5.4h10.8a1.6 1.6 0 0 1 1.6 1.6v10a1.6 1.6 0 0 1-1.6 1.6H8.6L3 12z"/><path d="M11.4 9.4l5.2 5.2M16.6 9.4l-5.2 5.2"/>',
    'shift': '<path d="M12 4.2l8 8.4h-4.4v7.2H8.4v-7.2H4z"/>',
    'bolt': '<path d="M13 2.8L5 13.4h6l-1 7.8 8-10.6h-6z"/>',
    'photo': '<rect x="3.4" y="5" width="17.2" height="14" rx="2.6"/><circle cx="8.6" cy="9.6" r="1.6"/><path d="M3.6 16.4l4.6-4.2 3.4 3 3.6-3.6 5.2 4.8"/>',
    'terminal': '<rect x="3" y="4.6" width="18" height="14.8" rx="3"/><path d="M7 9.6l2.8 2.4L7 14.4M12.4 15h4.4"/>',
    'tag': '<path d="M3.6 12.4V4.6a1 1 0 0 1 1-1h7.8l8.2 8.2a1.4 1.4 0 0 1 0 2l-6.8 6.8a1.4 1.4 0 0 1-2 0z"/><circle cx="8" cy="8" r="1.4"/>',
    'square-and-arrow-up': '<path d="M8.4 9.6H6.6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h10.8a2 2 0 0 0 2-2v-7a2 2 0 0 0-2-2h-1.8"/><path d="M12 3.2v11M8.6 6.6L12 3.2l3.4 3.4"/>',
    'house': '<path d="M4 10.6L12 4l8 6.6V19a1.4 1.4 0 0 1-1.4 1.4H5.4A1.4 1.4 0 0 1 4 19z"/><path d="M9.6 20.4v-5.6h4.8v5.6"/>',
    // To-dos redesign kit (lock screen, starred places, the Conversation toggle)
    'lock-fill': `<path d="M8 10.4V7.6a4 4 0 0 1 8 0v2.8"/><rect x="5.4" y="10.2" width="13.2" height="10.2" rx="2.4" ${F}/>`,
    'flashlight': '<path d="M8.4 3.6h7.2v3.2l-1.6 3.4v9.6a1.2 1.2 0 0 1-1.2 1.2h-1.6a1.2 1.2 0 0 1-1.2-1.2v-9.6L8.4 6.8z"/><path d="M8.6 6.6h6.8M12 13.2v2"/>',
    'camera': '<path d="M4.8 7.4h3l1.6-2.2h5.2l1.6 2.2h3a1.6 1.6 0 0 1 1.6 1.6v9.2a1.6 1.6 0 0 1-1.6 1.6H4.8a1.6 1.6 0 0 1-1.6-1.6V9a1.6 1.6 0 0 1 1.6-1.6z"/><circle cx="12" cy="13.4" r="3.6"/>',
    'star': '<path d="M12 3.6l2.6 5.4 5.8.8-4.2 4.1 1 5.8L12 17l-5.2 2.7 1-5.8-4.2-4.1 5.8-.8z"/>',
    'star-fill': `<path d="M12 3.6l2.6 5.4 5.8.8-4.2 4.1 1 5.8L12 17l-5.2 2.7 1-5.8-4.2-4.1 5.8-.8z" ${F}/>`,
    'rectangle-split': '<rect x="3.4" y="4.6" width="17.2" height="14.8" rx="2.6"/><path d="M14.2 4.8v14.4"/>',

    // status bars
    'status-signal': `<rect x="2" y="15" width="3.6" height="5" rx="1.1" ${F}/><rect x="7.4" y="11.6" width="3.6" height="8.4" rx="1.1" ${F}/><rect x="12.8" y="8" width="3.6" height="12" rx="1.1" ${F}/><rect x="18.2" y="4.4" width="3.6" height="15.6" rx="1.1" ${F}/>`,
    'status-wifi': `<path d="M12 20.4l-2.9-3.5a4.6 4.6 0 0 1 5.8 0z" ${F}/><path d="M6.1 13.3a9.2 9.2 0 0 1 11.8 0M3 9.5a14 14 0 0 1 18 0" stroke-width="2.6"/>`,
  };

  const ALIAS = {
    todo: 'checklist', todos: 'checklist', check: 'checkmark-circle', done: 'checkmark-circle-fill',
    due: 'calendar-clock', 'calendar-badge-clock': 'calendar-clock', reminder: 'bell', priority: 'flag-fill',
    space: 'project', handoff: 'paperplane', route: 'arrow-turn-up-right', decision: 'signature', plan: 'clipboard-list',
    supervised: 'diamond-check', delete: 'trash', more: 'ellipsis', sidebar: 'sidebar-left', inspector: 'sidebar-right',
    needs: 'bubble-exclamation-fill', working: 'gear', time: 'clock', restore: 'tray-arrow-up', undo: 'arrow-uturn-left',
    reopen: 'arrow-uturn-left', retry: 'arrow-clockwise', back: 'chevron-left', disclosure: 'chevron-right',
    expand: 'chevron-down', search: 'magnifying-glass', close: 'xmark', clear: 'xmark-circle-fill',
    persona: 'sparkle', inbox: 'tray-full', knowledge: 'books', scheduled: 'alarm', machines: 'laptop-phone',
    health: 'heart', compose: 'square-pencil', 'new-chat': 'square-pencil', today: 'sun-fill', send: 'arrow-up-circle-fill',
    conversation: 'bubble-left-right', chat: 'bubble', breakdown: 'wand', suggest: 'wand', open: 'arrow-up-right',
    hotel: 'bed', menu: 'line-3-horizontal', settings: 'gear', filter: 'line-3-decrease', waiting: 'clock', person: 'person-circle', 'pull-request': 'pr',
  };

  function strokeFor(size) {
    if (typeof size !== 'number') return 1.75;
    if (size <= 12) return 2.1;
    if (size <= 15) return 1.95;
    if (size <= 19) return 1.8;
    return 1.7;
  }

  let maskSeq = 0;
  // A filled shape with its mark knocked out through a mask (unique id per drawing).
  function knockout(def) {
    const id = 'k-cut-' + (++maskSeq);
    return `<defs><mask id="${id}" maskUnits="userSpaceOnUse" x="0" y="0" width="24" height="24"><rect width="24" height="24" fill="#fff" stroke="none"/>` +
      `<g fill="none" stroke="#000" stroke-width="${def.cutWidth || 2}">${def.cut}</g></mask></defs>` +
      `<g mask="url(#${id})" fill="currentColor" stroke="none">${def.fill}</g>`;
  }

  /** Kit.icon(name, size?, {cls, title, stroke}) → '<svg …>' */
  Kit.icon = function icon(name, size, opts) {
    opts = opts || {};
    const key = P[name] ? name : ALIAS[name];
    const def = P[key];
    const body = def && typeof def === 'object' ? knockout(def) : def;
    if (!body) {
      if (window.console) console.warn('[kit] unknown icon:', name);
      return `<svg class="k-ic k-ic--missing" viewBox="0 0 24 24" data-icon="${name}"><rect x="4" y="4" width="16" height="16" rx="4" fill="none" stroke="currentColor" stroke-dasharray="2 2"/></svg>`;
    }
    // Inline style: the .k-ic class sizes icons to 1em, which would otherwise win over width/height attributes.
    const dim = typeof size === 'number' ? ` width="${size}" height="${size}" style="width:${size}px;height:${size}px"` : '';
    const sw = opts.stroke || strokeFor(size);
    const title = opts.title ? `<title>${opts.title}</title>` : '';
    return `<svg class="k-ic${opts.cls ? ' ' + opts.cls : ''}" data-icon="${key}" viewBox="0 0 24 24"${dim} fill="none" stroke="currentColor" stroke-width="${sw}" stroke-linecap="round" stroke-linejoin="round" aria-hidden="${title ? 'false' : 'true'}">${title}${body}</svg>`;
  };
  Kit.icon.names = () => Object.keys(P);
  Kit.icon.aliases = ALIAS;
  Kit.icon.paths = P;
})();
