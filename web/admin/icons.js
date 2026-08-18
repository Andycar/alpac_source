// icons.js — premium line-icon set (Lucide-flavoured, 24×24, currentColor
// stroke). Replaces the plain emoji that used to sit in the sidebar nav,
// dashboard cards and quick actions.
//
// Usage:
//   import { icon } from '../icons.js';
//   html`<span class="ic">${icon('users')}</span>`
//
// Each entry is an `svg` fragment (no wrapping <svg>); `icon()` wraps it in a
// sized <svg> element. Unknown names fall back to a neutral dot so the UI
// never breaks on a typo'd key.

import { html, svg } from './components/_lit.js';

const PATHS = {
  dashboard: svg`<rect x="3" y="3" width="7" height="8" rx="1.6"/><rect x="14" y="3" width="7" height="5" rx="1.6"/><rect x="14" y="11" width="7" height="10" rx="1.6"/><rect x="3" y="14" width="7" height="7" rx="1.6"/>`,
  telemetry: svg`<path d="M3 12h3l2.5-6 4 14 3-9 2 3h3.5"/>`,
  users: svg`<path d="M16 19v-1.5A3.5 3.5 0 0 0 12.5 14h-5A3.5 3.5 0 0 0 4 17.5V19"/><circle cx="10" cy="8" r="3.2"/><path d="M19.5 19v-1.4a3.3 3.3 0 0 0-2.5-3.2"/><path d="M15.5 5.2a3.2 3.2 0 0 1 0 5.6"/>`,
  groups: svg`<circle cx="12" cy="12" r="3"/><circle cx="12" cy="12" r="8"/><path d="M12 4v2M12 18v2M4 12h2M18 12h2"/>`,
  balancers: svg`<path d="M4 7h9M17 7h3M4 12h3M11 12h9M4 17h13M21 17h-1"/><circle cx="15" cy="7" r="2"/><circle cx="9" cy="12" r="2"/><circle cx="17" cy="17" r="2"/>`,
  plugins: svg`<path d="M9 4.5a1.5 1.5 0 0 1 3 0V6h3a1 1 0 0 1 1 1v3h1.5a1.5 1.5 0 0 1 0 3H16v3a1 1 0 0 1-1 1h-3v-1.5a1.5 1.5 0 0 0-3 0V20H6a1 1 0 0 1-1-1v-3H3.5a1.5 1.5 0 0 1 0-3H5V7a1 1 0 0 1 1-1h3z"/>`,
  modules: svg`<path d="M12 3l8 4.5-8 4.5-8-4.5z"/><path d="M4 12l8 4.5 8-4.5"/><path d="M4 16.5L12 21l8-4.5"/>`,
  bans: svg`<circle cx="12" cy="12" r="8.5"/><path d="M6 6l12 12"/>`,
  server: svg`<rect x="3" y="4" width="18" height="7" rx="1.6"/><rect x="3" y="13" width="18" height="7" rx="1.6"/><path d="M7 7.5h.01M7 16.5h.01"/>`,
  cluster: svg`<circle cx="12" cy="5" r="2.2"/><circle cx="5" cy="18" r="2.2"/><circle cx="19" cy="18" r="2.2"/><path d="M12 7.2L6 16M12 7.2L18 16M7 18h10"/>`,
  browser: svg`<circle cx="12" cy="12" r="8.5"/><path d="M3.5 12h17"/><path d="M12 3.5c2.4 2.2 3.6 5.3 3.6 8.5S14.4 18.3 12 20.5c-2.4-2.2-3.6-5.3-3.6-8.5S9.6 5.7 12 3.5z"/>`,
  deps: svg`<path d="M12 3v11"/><path d="M8 10l4 4 4-4"/><path d="M5 20h14"/>`,
  transcoding: svg`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M3 15h18M8 4v16M16 4v16"/>`,
  broadcast: svg`<path d="M3 11l16-6.5v15L3 13z"/><path d="M3 11v2a2 2 0 0 0 2 2h1"/><path d="M9 16.5l1 3.5"/>`,
  promo: svg`<path d="M4 8a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2 2 2 0 0 0 0 4 2 2 0 0 1-2 2H6a2 2 0 0 1-2-2 2 2 0 0 0 0-4z" /><path d="M14 6v12" stroke-dasharray="1.5 2.5"/>`,
  admins: svg`<path d="M12 3l7 2.5v5c0 4.4-3 8.2-7 9.5-4-1.3-7-5.1-7-9.5v-5z"/><path d="M9 12l2 2 4-4"/>`,
  telegram: svg`<path d="M21 5L3.5 11.5l5 1.8L17 7l-6 7.5v3.7l2.6-3 3.4 2.5z"/>`,
  appreplace: svg`<path d="M4 7h7"/><path d="M4 12h5"/><path d="M4 17h7"/><path d="M14 8l3-3 3 3"/><path d="M17 5v8a3 3 0 0 1-3 3h-1"/>`,
  inspector: svg`<circle cx="11" cy="11" r="6.5"/><path d="M20 20l-4.2-4.2"/>`,
  selfupdate: svg`<path d="M5 12a7 7 0 0 1 12-5l2 2"/><path d="M19 5v4h-4"/><path d="M19 12a7 7 0 0 1-12 5l-2-2"/><path d="M5 19v-4h4"/>`,
  probe: svg`<path d="M9 3h6"/><path d="M10 3v6l-5 9a2 2 0 0 0 1.8 3h10.4a2 2 0 0 0 1.8-3l-5-9V3"/><path d="M7.5 15h9"/>`,
  proxycore: svg`<circle cx="12" cy="12" r="2.4"/><path d="M12 3v3M12 18v3M3 12h3M18 12h3"/><path d="M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M18.4 5.6l-2.1 2.1M7.7 16.3l-2.1 2.1"/>`,
  proxy: svg`<path d="M9 15l6-6"/><path d="M10.5 6.5l1-1a4 4 0 0 1 5.7 5.7l-1 1"/><path d="M13.5 17.5l-1 1a4 4 0 0 1-5.7-5.7l1-1"/>`,
  music: svg`<path d="M9 18V6l11-2v12"/><circle cx="6.5" cy="18" r="2.5"/><circle cx="17.5" cy="16" r="2.5"/>`,
  drochub: svg`<path d="M12 3c2 3-1 4.5-1 7a3 3 0 0 0 6 .2c.8 1.3 1.2 2.7 1.2 4.1a6.2 6.2 0 1 1-12.4 0c0-3.6 2.4-5.7 4.2-8C11.4 4.7 12 3.8 12 3z"/>`,
  calendar: svg`<rect x="3.5" y="5" width="17" height="16" rx="2"/><path d="M3.5 9.5h17M8 3v4M16 3v4"/><path d="M7.5 13h2M11 13h2M14.5 13h2M7.5 16.5h2M11 16.5h2"/>`,
  alice: svg`<path d="M5 10a7 7 0 0 1 14 0v5a3 3 0 0 1-3 3"/><path d="M5 10v4a2 2 0 0 0 2 2h1v-6H7a2 2 0 0 0-2 2z"/><path d="M19 10v4a2 2 0 0 1-2 2h-1v-6h1a2 2 0 0 1 2 2z"/><path d="M12 18v2M10 20h4"/>`,
  feedback: svg`<path d="M4 5h16a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H9l-4 4v-4H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1z"/><path d="M8 9.5h8M8 12.5h5"/>`,
  logs: svg`<path d="M5 4h11l3 3v13a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z"/><path d="M15 4v4h4"/><path d="M8 12h7M8 15.5h7M8 8.5h3"/>`,
  config: svg`<path d="M4 8h9M17 8h3"/><path d="M4 16h3M11 16h9"/><circle cx="15" cy="8" r="2.4"/><circle cx="9" cy="16" r="2.4"/>`,
};

// Route-key → icon-key remap where the route name differs from the glyph name.
const ALIAS = {
  'server-stats': 'server',
  'browser-engine': 'browser',
  'tg-settings': 'telegram',
  'media-probe': 'probe',
  'music-sources': 'music',
};

export function icon(name, size = 20) {
  const key = ALIAS[name] || name;
  const body = PATHS[key] || svg`<circle cx="12" cy="12" r="3.2"/>`;
  return html`<svg class="lic" viewBox="0 0 24 24" width=${size} height=${size}
    fill="none" stroke="currentColor" stroke-width="1.8"
    stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`;
}

export function hasIcon(name) {
  const key = ALIAS[name] || name;
  return !!PATHS[key];
}
