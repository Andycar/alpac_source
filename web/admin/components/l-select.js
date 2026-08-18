// <l-select> — styled native <select>. Drops the platform chrome but uses a
// real <select> under the hood so keyboard nav + iOS wheel picker still work.
//
// Properties:
//   options Array<{value, label}>
//   value   string (current value)
//   size    'sm'|'md'
//   placeholder string

import { LitElement, html, css } from './_lit.js';

class LSelect extends LitElement {
  static properties = {
    options:     { type: Array },
    value:       { type: String },
    size:        { type: String, reflect: true },
    placeholder: { type: String },
    disabled:    { type: Boolean, reflect: true },
  };
  constructor() {
    super();
    this.options = [];
    this.value = '';
    this.size = 'md';
    this.placeholder = '';
    this.disabled = false;
  }
  _onChange(e) {
    this.value = e.target.value;
    this.dispatchEvent(new CustomEvent('change', {
      detail: { value: this.value }, bubbles: true, composed: true,
    }));
  }
  render() {
    return html`
      <div class="wrap">
        <select .value=${this.value} ?disabled=${this.disabled} @change=${this._onChange}>
          ${this.placeholder ? html`<option value="" ?selected=${!this.value}>${this.placeholder}</option>` : ''}
          ${(this.options || []).map(o =>
            typeof o === 'string'
              ? html`<option value=${o} ?selected=${o === this.value}>${o}</option>`
              : html`<option value=${o.value} ?selected=${o.value === this.value}>${o.label || o.value}</option>`
          )}
        </select>
        <span class="caret">⌄</span>
      </div>
    `;
  }
  static styles = css`
    :host { display: inline-flex; }
    .wrap {
      position: relative;
      display: inline-flex;
      align-items: center;
      width: 100%;
      background: var(--bg-2);
      border: 1px solid var(--border-2);
      border-radius: var(--r-2);
      transition: border-color var(--t-fast);
    }
    .wrap:hover { border-color: var(--border-3); }
    .wrap:focus-within {
      border-color: var(--accent);
      box-shadow: 0 0 0 3px var(--accent-soft);
    }
    select {
      appearance: none;
      -webkit-appearance: none;
      background: transparent;
      border: none; outline: none;
      color: var(--text-0);
      font: inherit;
      padding: 0 28px 0 12px;
      width: 100%;
      cursor: pointer;
    }
    select option { background: var(--bg-1); color: var(--text-0); }
    :host([size="sm"]) select { height: 28px; font-size: var(--fs-xs); }
    :host([size="md"]) select, select { height: 36px; font-size: var(--fs-sm); }
    .caret {
      position: absolute; right: 10px; top: 50%;
      transform: translateY(-50%);
      color: var(--text-2);
      pointer-events: none;
      font-size: 14px;
      line-height: 1;
    }
  `;
}
customElements.define('l-select', LSelect);
