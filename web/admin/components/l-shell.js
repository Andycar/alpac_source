// <l-shell> — top-level chrome: sidebar + topbar + content slot.
//
// Properties:
//   items   = [{key, label, icon, badge?, group?}]
//   active  = current route key
//   user    = {name, role, avatar?}
//   collapsed = bool (sidebar collapsed state, persisted to localStorage)
//   mobile  = bool (force mobile-drawer layout; TG WebApp opts in here)
//   onnavigate = fired as 'navigate' custom event with detail={key}
//
// Layout modes:
//   - desktop (default): persistent sidebar + topbar + content (3-col grid)
//   - collapsed:         narrow icon-only sidebar
//   - mobile / TG:       sidebar hidden by default, hamburger in topbar
//                         opens slide-in drawer, content fills viewport.
//                         Auto-enabled when window.innerWidth < 768 OR when
//                         mounted inside Telegram.WebApp (TG bridge sets
//                         shell.mobile = true after init).
//
// Apple-flavoured details:
//   - sidebar uses backdrop-filter for glass surface
//   - active item has gradient fill + soft glow
//   - logo and section labels use display font
//   - topbar pulls user avatar + theme toggle + search
//   - drawer slides from left, dimmed scrim catches taps to close

import { LitElement, html, css, classMap } from './_lit.js';
import { icon, hasIcon } from '../icons.js';

const STORAGE_KEY = 'admin-v2.sidebar.collapsed';

class LShell extends LitElement {
  static properties = {
    items: { type: Array },
    active: { type: String },
    user: { type: Object },
    collapsed: { type: Boolean, reflect: true },
    mobile:    { type: Boolean, reflect: true },
    drawerOpen: { type: Boolean, reflect: true, attribute: 'drawer-open' },
    title: { type: String },
  };

  constructor() {
    super();
    this.items = [];
    this.active = '';
    this.user = null;
    this.title = '';
    this.collapsed = localStorage.getItem(STORAGE_KEY) === '1';
    this.mobile = false;
    this.drawerOpen = false;
    this._onResize = this._onResize.bind(this);
  }

  connectedCallback() {
    super.connectedCallback();
    // Auto-mobile: narrow viewport OR Telegram WebApp host. The TG bridge
    // could set this attribute directly, but doing it here keeps things
    // working in plain browsers at narrow widths (responsive testing).
    this._onResize();
    window.addEventListener('resize', this._onResize);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    window.removeEventListener('resize', this._onResize);
  }

  _onResize() {
    const wantsMobile =
      window.innerWidth < 768
      || (window.Telegram && window.Telegram.WebApp);
    if (wantsMobile && !this.mobile) {
      this.mobile = true;
    } else if (!wantsMobile && this.mobile && !this._mobileSticky) {
      this.mobile = false;
      this.drawerOpen = false;
    }
  }

  toggle() {
    if (this.mobile) {
      this.drawerOpen = !this.drawerOpen;
      // body-scroll-lock while drawer is open
      document.documentElement.style.overflow = this.drawerOpen ? 'hidden' : '';
      return;
    }
    this.collapsed = !this.collapsed;
    localStorage.setItem(STORAGE_KEY, this.collapsed ? '1' : '0');
  }

  _closeDrawer() {
    if (this.mobile && this.drawerOpen) {
      this.drawerOpen = false;
      document.documentElement.style.overflow = '';
    }
  }

  _navigate(key, e) {
    e?.preventDefault?.();
    this.dispatchEvent(new CustomEvent('navigate', { detail: { key }, bubbles: true, composed: true }));
    // On mobile, navigating from the drawer should close it.
    this._closeDrawer();
  }

  render() {
    const groups = groupItems(this.items);
    const shellClasses = classMap({
      collapsed: this.collapsed && !this.mobile,
      mobile:    this.mobile,
      'drawer-open': this.mobile && this.drawerOpen,
    });
    return html`
      <div class="shell ${shellClasses}">
        ${this.mobile && this.drawerOpen ? html`<div class="scrim" @click=${this._closeDrawer}></div>` : ''}
        <aside class="sidebar">
          <div class="brand">
            <div class="logo" aria-hidden="true" title="alpaca">
              <svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg">
                <defs>
                  <linearGradient id="alpacaBg" x1="0" y1="0" x2="64" y2="64" gradientUnits="userSpaceOnUse">
                    <stop offset="0%" stop-color="#6c8cff"/>
                    <stop offset="100%" stop-color="#8d6bff"/>
                  </linearGradient>
                </defs>
                <!-- rounded square background -->
                <rect x="0" y="0" width="64" height="64" rx="14" fill="url(#alpacaBg)"/>
                <!-- inner shadow ring for depth -->
                <rect x="1" y="1" width="62" height="62" rx="13" fill="none"
                      stroke="rgba(255,255,255,0.18)" stroke-width="1"/>
                <!-- alpaca silhouette -->
                <g fill="#fff" fill-opacity="0.96">
                  <!-- ears -->
                  <ellipse cx="22" cy="14" rx="3.5" ry="7" transform="rotate(-8 22 14)"/>
                  <ellipse cx="42" cy="14" rx="3.5" ry="7" transform="rotate(8 42 14)"/>
                  <!-- inner ears tint -->
                </g>
                <g fill="rgba(141,107,255,0.55)">
                  <ellipse cx="22.2" cy="15" rx="1.4" ry="4" transform="rotate(-8 22 14)"/>
                  <ellipse cx="41.8" cy="15" rx="1.4" ry="4" transform="rotate(8 42 14)"/>
                </g>
                <g fill="#fff" fill-opacity="0.96">
                  <!-- fluffy top crown between ears -->
                  <ellipse cx="32" cy="20" rx="10" ry="6"/>
                  <!-- head -->
                  <ellipse cx="32" cy="30" rx="11.5" ry="9.5"/>
                  <!-- neck / body curve -->
                  <path d="M24 36 C 22 44, 22 52, 26 58 L 38 58 C 42 52, 42 44, 40 36 Z"/>
                </g>
                <!-- eyes -->
                <circle cx="27.5" cy="29" r="1.4" fill="#0c1118"/>
                <circle cx="36.5" cy="29" r="1.4" fill="#0c1118"/>
                <!-- nose -->
                <ellipse cx="32" cy="35.5" rx="2.2" ry="1.4" fill="#0c1118" fill-opacity="0.85"/>
                <!-- subtle smile -->
                <path d="M30 37.5 Q 32 39 34 37.5" stroke="#0c1118" stroke-width="0.9" fill="none" stroke-linecap="round" stroke-opacity="0.7"/>
              </svg>
            </div>
            <span class="brand-name">alcopa<span class="brand-dim">.cc</span></span>
          </div>
          <nav class="nav" aria-label="Main">
            ${groups.map(g => html`
              ${g.label ? html`<div class="nav-group">${g.label}</div>` : ''}
              ${g.items.map(item => html`
                <a
                  class="nav-item ${classMap({ active: item.key === this.active })}"
                  href="#/${item.key}"
                  @click=${(e) => this._navigate(item.key, e)}
                  data-key=${item.key}
                >
                  <span class="nav-icon">${hasIcon(item.key) ? icon(item.key) : (item.icon || '●')}</span>
                  <span class="nav-label">${item.label}</span>
                  ${item.badge != null ? html`<span class="nav-badge">${item.badge}</span>` : ''}
                </a>
              `)}
            `)}
          </nav>
          <button class="collapse-btn" @click=${this.toggle} title="Toggle sidebar">
            ${this.collapsed ? '›' : '‹'}
          </button>
        </aside>
        <div class="main">
          <header class="topbar">
            <button
              class="hamburger"
              aria-label="Открыть меню"
              @click=${this.toggle}
              ?hidden=${!this.mobile}
            >
              <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round">
                <path d="M4 7h16"/><path d="M4 12h16"/><path d="M4 17h16"/>
              </svg>
            </button>
            <h1 class="topbar-title">${this.title}</h1>
            <div class="topbar-right">
              <slot name="topbar"></slot>
              ${this.user ? html`
                <div class="user">
                  <div class="user-avatar">${String(this.user.name || '?').charAt(0).toUpperCase() || '?'}</div>
                  <div class="user-meta">
                    <div class="user-name">${String(this.user.name || 'admin')}</div>
                    <div class="user-role">${String(this.user.role || '')}</div>
                  </div>
                </div>` : ''}
            </div>
          </header>
          <main class="content">
            <slot></slot>
          </main>
        </div>
      </div>
    `;
  }

  static styles = css`
    :host { display: block; }

    .shell {
      display: grid;
      grid-template-columns: var(--sidebar-w) 1fr;
      min-height: 100vh;
      transition: grid-template-columns var(--t-normal) var(--ease-spring);
    }
    .shell.collapsed { grid-template-columns: var(--sidebar-w-collapsed) 1fr; }

    /* Mobile layout: single-column, sidebar becomes off-canvas drawer.
       Triggered by either viewport-width or TG WebApp host (controlled via
       host attribute mobile=true). */
    .shell.mobile { grid-template-columns: 1fr; }
    .shell.mobile .sidebar {
      position: fixed;
      top: 0; bottom: 0; left: 0;
      width: min(86vw, 320px);
      transform: translateX(-100%);
      transition: transform var(--t-slow) var(--ease-spring);
      z-index: var(--z-modal);
      border-right: 1px solid var(--border-2);
      box-shadow: var(--shadow-xl);
    }
    .shell.mobile.drawer-open .sidebar { transform: none; }
    .shell.mobile .collapse-btn { display: none; }

    /* Dimmed scrim that catches taps to close the drawer. */
    .scrim {
      position: fixed; inset: 0;
      background: var(--surface-overlay);
      backdrop-filter: blur(8px);
      z-index: calc(var(--z-modal) - 1);
      animation: fade-in var(--t-normal) var(--ease-spring) both;
    }
    @keyframes fade-in { from { opacity: 0; } to { opacity: 1; } }

    /* Hamburger button — desktop hidden via [hidden], mobile flexed */
    .hamburger {
      display: none;
      align-items: center; justify-content: center;
      width: 40px; height: 40px;
      border-radius: var(--r-2);
      background: transparent;
      border: 1px solid var(--border-2);
      color: var(--text-1);
      cursor: pointer;
      flex-shrink: 0;
      transition: background var(--t-fast), color var(--t-fast);
    }
    .hamburger:hover { background: var(--bg-3); color: var(--text-0); }
    .hamburger:active { transform: scale(0.96); }
    .hamburger[hidden] { display: none !important; }
    .shell.mobile .hamburger { display: inline-flex; }

    /* Legacy fallback for plain browsers without JS-managed .mobile class —
       still hides the static sidebar so the drawer chrome isn't dead weight. */
    @media (max-width: 768px) {
      .shell:not(.mobile) { grid-template-columns: 1fr; }
      .shell:not(.mobile) .sidebar { display: none; }
    }

    .sidebar {
      position: sticky;
      top: 0;
      align-self: start;
      height: 100vh;
      padding: var(--s-4) var(--s-3);
      background: var(--surface-glass-strong);
      backdrop-filter: blur(28px) saturate(180%);
      -webkit-backdrop-filter: blur(28px) saturate(180%);
      border-right: 1px solid var(--border-1);
      display: flex;
      flex-direction: column;
      gap: var(--s-3);
      z-index: var(--z-sidebar);
    }

    .brand {
      display: flex;
      align-items: center;
      gap: var(--s-3);
      padding: var(--s-2) var(--s-2) var(--s-3);
      border-bottom: 1px solid var(--border-1);
    }
    .logo {
      width: 36px; height: 36px;
      border-radius: var(--r-3);
      box-shadow: var(--shadow-glow);
      position: relative;
      flex-shrink: 0;
      overflow: hidden;
      display: grid;
      place-items: center;
    }
    .logo svg {
      width: 100%;
      height: 100%;
      display: block;
    }
    .brand-name {
      font-family: var(--font-display);
      font-size: var(--fs-lg);
      font-weight: var(--fw-bold);
      letter-spacing: -0.01em;
    }
    .brand-dim { color: var(--text-2); font-weight: var(--fw-medium); }
    .shell.collapsed .brand-name { display: none; }

    .nav {
      display: flex;
      flex-direction: column;
      gap: 2px;
      overflow-y: auto;
      flex: 1;
      min-height: 0;
    }
    .nav-group {
      font-size: var(--fs-xs);
      font-weight: var(--fw-semibold);
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: var(--text-3);
      padding: var(--s-3) var(--s-3) var(--s-1);
    }
    .shell.collapsed .nav-group { display: none; }

    .nav-item {
      display: flex;
      align-items: center;
      gap: var(--s-3);
      padding: 9px 12px;
      border-radius: var(--r-2);
      color: var(--text-1);
      text-decoration: none;
      font-size: var(--fs-base);
      font-weight: var(--fw-medium);
      position: relative;
      transition: background var(--t-fast) var(--ease-out),
                  color var(--t-fast) var(--ease-out),
                  transform var(--t-fast) var(--ease-spring);
    }
    .nav-item:hover { background: var(--bg-3); color: var(--text-0); }
    .nav-item:active { transform: scale(0.98); }

    .nav-item.active {
      color: var(--text-0);
      background: linear-gradient(135deg, var(--accent-soft), rgba(141,107,255,0.10));
      box-shadow: inset 0 0 0 1px var(--border-2),
                  0 6px 14px rgba(108, 140, 255, 0.10);
    }
    .nav-item.active::before {
      content: '';
      position: absolute;
      left: -6px; top: 6px; bottom: 6px;
      width: 3px;
      border-radius: var(--r-pill);
      background: linear-gradient(180deg, var(--accent), var(--accent-2));
      box-shadow: 0 0 12px var(--accent);
    }
    .nav-icon {
      width: 22px; height: 22px;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      font-size: 16px;
      flex-shrink: 0;
      color: var(--text-2);
      transition: color var(--t-fast);
    }
    .nav-icon .lic { width: 19px; height: 19px; display: block; }
    .nav-item:hover .nav-icon { color: var(--text-0); }
    .nav-item.active .nav-icon { color: var(--accent); }
    .nav-label { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .shell.collapsed .nav-label { display: none; }
    .shell.collapsed .nav-badge { display: none; }

    .nav-badge {
      font-size: var(--fs-xs);
      color: var(--text-2);
      background: var(--bg-3);
      padding: 2px 8px;
      border-radius: var(--r-pill);
      font-weight: var(--fw-semibold);
    }

    .collapse-btn {
      align-self: stretch;
      background: transparent;
      border: 1px solid var(--border-2);
      color: var(--text-2);
      cursor: pointer;
      padding: 6px;
      border-radius: var(--r-2);
      font-size: 16px;
      transition: background var(--t-fast), color var(--t-fast);
    }
    .collapse-btn:hover { background: var(--bg-3); color: var(--text-0); }

    .main {
      display: flex;
      flex-direction: column;
      min-width: 0;
    }

    .topbar {
      position: sticky;
      top: 0;
      z-index: var(--z-topbar);
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--s-4);
      height: var(--topbar-h);
      padding: 0 var(--s-5);
      background: var(--surface-glass);
      backdrop-filter: blur(20px) saturate(180%);
      -webkit-backdrop-filter: blur(20px) saturate(180%);
      border-bottom: 1px solid var(--border-1);
    }
    .topbar-title {
      font-family: var(--font-display);
      font-size: var(--fs-lg);
      font-weight: var(--fw-semibold);
      letter-spacing: -0.01em;
      margin: 0;
    }
    .topbar-right { display: flex; align-items: center; gap: var(--s-3); }

    .user { display: flex; align-items: center; gap: var(--s-3); padding-left: var(--s-3); border-left: 1px solid var(--border-1); }
    .user-avatar {
      width: 32px; height: 32px;
      border-radius: 50%;
      background: linear-gradient(135deg, var(--accent), var(--accent-2));
      color: white;
      display: flex; align-items: center; justify-content: center;
      font-weight: var(--fw-bold);
      box-shadow: var(--shadow-sm);
    }
    .user-meta { line-height: 1.2; }
    .user-name { font-size: var(--fs-sm); font-weight: var(--fw-semibold); }
    .user-role { font-size: var(--fs-xs); color: var(--text-2); }

    .content {
      flex: 1;
      padding: var(--s-5) var(--s-5) var(--s-7);
      max-width: var(--content-max);
      width: 100%;
      margin: 0 auto;
    }

    /* Mobile tweaks ------------------------------------------------------ */
    .shell.mobile .nav-item {
      padding: 12px 14px;          /* taller tap targets */
      font-size: var(--fs-md);
    }
    .shell.mobile .nav-icon { font-size: 18px; width: 24px; }
    .shell.mobile .topbar { padding: 0 var(--s-3); gap: var(--s-2); }
    .shell.mobile .topbar-title {
      font-size: var(--fs-md);
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      flex: 1;
      min-width: 0;
    }
    .shell.mobile .topbar-right { gap: var(--s-2); }
    .shell.mobile .user { padding-left: 0; border-left: 0; }
    .shell.mobile .user-meta { display: none; }      /* avatar only on phone */
    .shell.mobile .content { padding: var(--s-3) var(--s-3) var(--s-6); }

    /* Static sidebar is the off-canvas drawer on mobile — give it room
       around content + a close-button row in the head. */
    .shell.mobile .brand { padding: var(--s-3) var(--s-3) var(--s-2); }
    .shell.mobile .nav { padding: 0 var(--s-2) var(--s-3); }
    .shell.mobile .nav-group { padding: var(--s-3) var(--s-2) var(--s-1); }

    /* Reduced motion */
    @media (prefers-reduced-motion: reduce) {
      .shell, .shell.mobile .sidebar { transition: none; }
    }
  `;
}

function groupItems(items) {
  // Items can carry a `group` string. We render contiguous items with the
  // same `group` value under one header. Items without `group` get no header.
  const groups = [];
  let cur = null;
  for (const it of items) {
    const g = it.group || '';
    if (!cur || cur.label !== g) {
      cur = { label: g, items: [] };
      groups.push(cur);
    }
    cur.items.push(it);
  }
  return groups;
}

customElements.define('l-shell', LShell);
