// <l-input> — premium text/search field. Supports leading icon, clear button,
// invalid state, password reveal. Mirrors the native input via slotted events
// (you can listen to plain `input`/`change` from outside the shadow DOM).
//
// Properties:
//   value     string
//   type      'text' (default) | 'search' | 'password' | 'email' | 'number'
//   placeholder string
//   icon      string  — leading icon
//   clearable bool    — show clear (×) button when value is non-empty
//   size      'sm'|'md' (default md)
//   invalid   bool    — red border + ring
//   disabled  bool

import { LitElement, html, css, classMap } from './_lit.js';

class LInput extends LitElement {
  static properties = {
    value:       { type: String },
    type:        { type: String },
    placeholder: { type: String },
    icon:        { type: String },
    clearable:   { type: Boolean },
    size:        { type: String, reflect: true },
    invalid:     { type: Boolean, reflect: true },
    disabled:    { type: Boolean, reflect: true },
  };
  constructor() {
    super();
    this.value = '';
    this.type = 'text';
    this.placeholder = '';
    this.icon = '';
    this.clearable = false;
    this.size = 'md';
    this.invalid = false;
    this.disabled = false;
  }
  _onInput(e) {
    this.value = e.target.value;
    this.dispatchEvent(new CustomEvent('input', {
      detail: { value: this.value }, bubbles: true, composed: true,
    }));
  }
  _onChange(e) {
    this.dispatchEvent(new CustomEvent('change', {
      detail: { value: e.target.value }, bubbles: true, composed: true,
    }));
  }
  _clear() {
    this.value = '';
    this.dispatchEvent(new CustomEvent('input', {
      detail: { value: '' }, bubbles: true, composed: true,
    }));
    this.renderRoot.querySelector('input').focus();
  }
  render() {
    return html`
      <div class="wrap ${classMap({ invalid: this.invalid, disabled: this.disabled })}">
        ${this.icon ? html`<span class="icon">${this.icon}</span>` : ''}
        <input
          .value=${this.value}
          type=${this.type === 'search' ? 'text' : this.type}
          placeholder=${this.placeholder}
          ?disabled=${this.disabled}
          @input=${this._onInput}
          @change=${this._onChange}
        />
        ${this.clearable && this.value ? html`
          <button class="clear" @click=${this._clear} aria-label="Clear">×</button>
        ` : ''}
      </div>
    `;
  }
  static styles = css`
    :host { display: inline-flex; }
    .wrap {
      display: inline-flex;
      align-items: center;
      gap: var(--s-2);
      background: var(--bg-2);
      border: 1px solid var(--border-2);
      border-radius: var(--r-2);
      padding: 0 10px;
      width: 100%;
      transition: border-color var(--t-fast),
                  background var(--t-fast),
                  box-shadow var(--t-fast);
    }
    .wrap:hover:not(.disabled) { border-color: var(--border-3); }
    .wrap:focus-within {
      border-color: var(--accent);
      box-shadow: 0 0 0 3px var(--accent-soft);
    }
    .wrap.invalid {
      border-color: var(--danger);
      box-shadow: 0 0 0 3px var(--danger-soft);
    }
    .wrap.disabled { opacity: 0.5; cursor: not-allowed; }

    :host([size="sm"]) .wrap { height: 28px; font-size: var(--fs-xs); }
    :host([size="md"]) .wrap, .wrap { height: 36px; font-size: var(--fs-sm); }

    input {
      flex: 1; min-width: 0;
      background: transparent;
      border: none; outline: none;
      color: var(--text-0);
      font: inherit;
      padding: 0;
    }
    input::placeholder { color: var(--text-3); }
    .icon { color: var(--text-2); font-size: 14px; line-height: 1; }
    .clear {
      background: transparent; border: none; cursor: pointer;
      color: var(--text-2); font-size: 16px; line-height: 1; padding: 0;
      transition: color var(--t-fast);
    }
    .clear:hover { color: var(--text-0); }
  `;
}
customElements.define('l-input', LInput);
