// tg-bridge.js — Telegram WebApp integration.
//
// When loaded inside a Telegram WebApp client (Telegram.WebApp is present):
//   - applies Telegram theme params to our CSS tokens
//   - exchanges initData for an admin session cookie (one-shot)
//   - exposes mainButton/backButton/haptic helpers
//
// Outside Telegram (regular web admin) this module no-ops on import; pages
// can still call `tg.isAvailable` etc to branch behaviour safely.

export const tg = {
  isAvailable: false,
  webapp: null,
  user: null,

  /** Apply Telegram themeParams to :root CSS variables. */
  applyTheme() {
    if (!this.isAvailable) return;
    const t = this.webapp.themeParams || {};
    const root = document.documentElement;
    root.classList.add('theme-tg');
    const map = {
      '--bg-0': t.bg_color || t.secondary_bg_color,
      '--bg-1': t.secondary_bg_color || t.bg_color,
      '--text-0': t.text_color,
      '--text-2': t.hint_color,
      '--accent': t.button_color,
    };
    for (const [k, v] of Object.entries(map)) {
      if (v) root.style.setProperty(k, v);
    }
  },

  /** POST initData → backend, exchange for cookies. Returns admin info or throws.
   *  Also pins window.__adminApiBase from the response so api.js routes calls
   *  to /<adminPath>/api/* — without this the TG WebApp falls back to /api/*
   *  which isn't registered and 404s every request. */
  async authenticate() {
    if (!this.isAvailable) throw new Error('not in Telegram');
    const resp = await fetch('/api/tg-admin/auth', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ init_data: this.webapp.initData }),
    });
    if (!resp.ok) {
      const text = await resp.text();
      throw new Error(`TG auth failed (${resp.status}): ${text}`);
    }
    const data = await resp.json();
    // Pin the API base BEFORE returning — guarantees that any await callsite
    // chaining api.* will already use the right /<adminPath>/api/ root.
    //
    // Defensive: a malformed value like "//api" would be interpreted by the
    // browser as a protocol-relative URL (host=`api`), so we require the
    // string to start with exactly one slash and to contain at least one
    // non-slash character before falling through to it.
    const isOk = (s) => typeof s === 'string'
      && s.length > 1
      && s[0] === '/'
      && s[1] !== '/';
    if (data && isOk(data.api_base)) {
      window.__adminApiBase = data.api_base;
    } else if (data && data.admin_path && /^[A-Za-z0-9_-]+$/.test(data.admin_path)) {
      window.__adminApiBase = '/' + data.admin_path + '/api';
    } else {
      throw new Error('TG auth response missing valid admin_path / api_base');
    }
    return data;
  },

  /** Imperative wrappers for the Telegram MainButton. */
  mainButton: {
    show(text, onClick) {
      if (!tg.isAvailable) return;
      const mb = tg.webapp.MainButton;
      mb.setText(text);
      mb.onClick(onClick);
      mb.show();
    },
    hide() {
      if (!tg.isAvailable) return;
      tg.webapp.MainButton.hide();
    },
    loading(on) {
      if (!tg.isAvailable) return;
      const mb = tg.webapp.MainButton;
      on ? mb.showProgress() : mb.hideProgress();
    },
  },

  backButton: {
    show(onClick) {
      if (!tg.isAvailable) return;
      const bb = tg.webapp.BackButton;
      bb.onClick(onClick);
      bb.show();
    },
    hide() {
      if (!tg.isAvailable) return;
      tg.webapp.BackButton.hide();
    },
  },

  /** Haptic feedback wrapper. tone: 'light' | 'medium' | 'heavy' | 'success' | 'error' | 'warning' */
  haptic(tone = 'light') {
    if (!this.isAvailable) return;
    const h = this.webapp.HapticFeedback;
    if (!h) return;
    try {
      if (['success', 'error', 'warning'].includes(tone)) h.notificationOccurred(tone);
      else h.impactOccurred(tone);
    } catch {}
  },

  /** Tells Telegram the user has finished interacting. Calls close(). */
  close() {
    if (this.isAvailable) this.webapp.close();
  },
};

// One-shot initialisation on script load.
(function init() {
  const w = window.Telegram?.WebApp;
  if (!w) return;
  tg.isAvailable = true;
  tg.webapp = w;
  tg.user = w.initDataUnsafe?.user || null;
  try {
    w.ready();
    w.expand();
  } catch {}
  tg.applyTheme();
  // Re-apply theme when Telegram switches dark/light at runtime.
  w.onEvent?.('themeChanged', () => tg.applyTheme());
})();
