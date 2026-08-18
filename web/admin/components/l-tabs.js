// <l-tabs> — segmented tabs with sliding pill indicator.
//
// Properties:
//   tabs   Array<{key, label, badge?}>
//   active current key
//
// Emits 'tab-change' with detail={key}.

import { LitElement, html, css, classMap } from './_lit.js';

class LTabs extends LitElement {
  static properties = {
    tabs:   { type: Array },
    active: { type: String, reflect: true },
  };
  constructor() {
    super();
    this.tabs = [];
    this.active = '';
  }
  _select(key) {
    if (key === this.active) return;
    this.active = key;
    this.dispatchEvent(new CustomEvent('tab-change', { detail: { key }, bubbles: true, composed: true }));
  }
  render() {
    return html`
      <div class="tabs" role="tablist">
        ${this.tabs.map(t => html`
          <button
            class="tab ${classMap({ active: t.key === this.active })}"
            role="tab"
            aria-selected=${t.key === this.active}
            @click=${() => this._select(t.key)}
          >
            <span>${t.label}</span>
            ${t.badge != null ? html`<span class="badge">${t.badge}</span>` : ''}
          </button>
        `)}
      </div>
    `;
  }
  static styles = css`
    :host { display: inline-flex; }
    .tabs {
      display: inline-flex;
      gap: 2px;
      padding: 4px;
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-pill);
    }
    .tab {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 6px 14px;
      background: transparent;
      border: none;
      border-radius: var(--r-pill);
      color: var(--text-2);
      font-size: var(--fs-sm);
      font-weight: var(--fw-medium);
      cursor: pointer;
      transition: color var(--t-fast), background var(--t-fast);
      position: relative;
    }
    .tab:hover { color: var(--text-0); }
    .tab.active {
      color: var(--text-0);
      background: linear-gradient(135deg, var(--accent), var(--accent-2));
      box-shadow: var(--shadow-sm), 0 4px 14px rgba(108,140,255,0.25);
    }
    .badge {
      font-size: 10px;
      padding: 1px 6px;
      border-radius: var(--r-pill);
      background: rgba(255,255,255,0.18);
      font-weight: var(--fw-semibold);
    }
    .tab:not(.active) .badge { background: var(--bg-3); color: var(--text-2); }
  `;
}
customElements.define('l-tabs', LTabs);
