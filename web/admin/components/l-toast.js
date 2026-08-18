// <l-toast> — globally-managed toast stack. Use the imperative API:
//
//   import { toast } from './components/l-toast.js';
//   toast.success('Saved');
//   toast.error('Network down');
//
// The element auto-mounts itself the first time toast.* is called.

import { LitElement, html, css, classMap } from './_lit.js';

class LToastHost extends LitElement {
  static properties = { _items: { state: true } };
  constructor() {
    super();
    this._items = [];
    this._seq = 0;
  }
  push(t) {
    const id = ++this._seq;
    this._items = [...this._items, { id, ...t }];
    setTimeout(() => this._dismiss(id), t.ttl || 3600);
  }
  _dismiss(id) {
    this._items = this._items.map(it => it.id === id ? { ...it, out: true } : it);
    setTimeout(() => { this._items = this._items.filter(it => it.id !== id); }, 280);
  }
  render() {
    return html`
      <div class="stack">
        ${this._items.map(it => html`
          <div class="toast ${classMap({ ['t-' + (it.tone || 'info')]: true, out: it.out })}">
            <span class="icon" aria-hidden="true">${toneIcon(it.tone)}</span>
            <span class="msg">${it.message}</span>
            <button class="x" @click=${() => this._dismiss(it.id)} aria-label="Dismiss">×</button>
          </div>
        `)}
      </div>
    `;
  }
  static styles = css`
    :host {
      position: fixed;
      top: 16px;
      right: 16px;
      z-index: var(--z-toast);
      pointer-events: none;
    }
    .stack { display: flex; flex-direction: column; gap: 10px; align-items: flex-end; }
    .toast {
      pointer-events: all;
      display: flex;
      align-items: center;
      gap: 10px;
      padding: 12px 14px 12px 12px;
      min-width: 260px;
      max-width: 380px;
      background: var(--surface-glass-strong);
      backdrop-filter: blur(28px) saturate(180%);
      border: 1px solid var(--border-2);
      border-radius: var(--r-3);
      box-shadow: var(--shadow-lg);
      color: var(--text-0);
      font-size: var(--fs-sm);
      animation: slide-in var(--t-normal) var(--ease-spring) both;
    }
    .toast.out { animation: slide-out var(--t-normal) var(--ease-in) both; }
    @keyframes slide-in {
      from { opacity: 0; transform: translateX(20px) scale(0.96); }
      to { opacity: 1; transform: none; }
    }
    @keyframes slide-out {
      to { opacity: 0; transform: translateX(20px) scale(0.96); }
    }
    .icon {
      width: 24px; height: 24px;
      border-radius: 50%;
      display: inline-flex; align-items: center; justify-content: center;
      font-size: 12px;
      font-weight: var(--fw-bold);
      flex-shrink: 0;
    }
    .t-info    .icon { background: var(--info-soft);    color: var(--info); }
    .t-success .icon { background: var(--success-soft); color: var(--success); }
    .t-warn    .icon { background: var(--warn-soft);    color: var(--warn); }
    .t-error   .icon { background: var(--danger-soft);  color: var(--danger); }
    .msg { flex: 1; line-height: 1.4; }
    .x {
      background: transparent; border: none; color: var(--text-2);
      cursor: pointer; padding: 0; font-size: 18px; line-height: 1;
      transition: color var(--t-fast);
    }
    .x:hover { color: var(--text-0); }
  `;
}
function toneIcon(tone) {
  switch (tone) {
    case 'success': return '✓';
    case 'warn':    return '!';
    case 'error':   return '×';
    default:        return 'i';
  }
}
customElements.define('l-toast', LToastHost);

let _host = null;
function ensureHost() {
  if (_host) return _host;
  _host = document.createElement('l-toast');
  document.body.appendChild(_host);
  return _host;
}

export const toast = {
  info:    (message, opts) => ensureHost().push({ tone: 'info',    message, ...(opts || {}) }),
  success: (message, opts) => ensureHost().push({ tone: 'success', message, ...(opts || {}) }),
  warn:    (message, opts) => ensureHost().push({ tone: 'warn',    message, ...(opts || {}) }),
  error:   (message, opts) => ensureHost().push({ tone: 'error',   message, ttl: 5500, ...(opts || {}) }),
};
