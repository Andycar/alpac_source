// <l-button> — single button with variants. Spring-press feedback.
//
// Properties:
//   variant: 'primary' | 'secondary' | 'ghost' | 'danger' | 'success'
//   size:    'sm' | 'md' | 'lg'   (default md)
//   icon:    leading icon text (string / emoji)
//   loading: shows spinner instead of icon
//   disabled bool

import { LitElement, html, css, classMap } from './_lit.js';

class LButton extends LitElement {
  static properties = {
    variant:  { type: String, reflect: true },
    size:     { type: String, reflect: true },
    icon:     { type: String },
    loading:  { type: Boolean, reflect: true },
    disabled: { type: Boolean, reflect: true },
  };

  constructor() {
    super();
    this.variant = 'secondary';
    this.size = 'md';
    this.icon = '';
    this.loading = false;
    this.disabled = false;
  }

  render() {
    return html`
      <button
        class="btn ${classMap({
          primary:   this.variant === 'primary',
          secondary: this.variant === 'secondary',
          ghost:     this.variant === 'ghost',
          danger:    this.variant === 'danger',
          success:   this.variant === 'success',
          sm: this.size === 'sm',
          md: this.size === 'md',
          lg: this.size === 'lg',
        })}"
        ?disabled=${this.disabled || this.loading}
      >
        ${this.loading
          ? html`<span class="spinner"></span>`
          : (this.icon ? html`<span class="icon">${this.icon}</span>` : '')}
        <span class="label"><slot></slot></span>
      </button>
    `;
  }

  static styles = css`
    :host { display: inline-flex; }
    .btn {
      display: inline-flex;
      align-items: center;
      gap: var(--s-2);
      border: 1px solid transparent;
      border-radius: var(--r-2);
      font-family: var(--font-text);
      font-weight: var(--fw-semibold);
      cursor: pointer;
      user-select: none;
      transition: transform var(--t-fast) var(--ease-spring),
                  background var(--t-fast) var(--ease-out),
                  border-color var(--t-fast) var(--ease-out),
                  box-shadow var(--t-fast) var(--ease-out),
                  opacity var(--t-fast);
      white-space: nowrap;
    }
    .btn:active:not(:disabled) { transform: scale(0.97); }
    .btn:disabled { opacity: 0.5; cursor: not-allowed; }
    .btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }

    .btn.sm { height: 28px; padding: 0 10px; font-size: var(--fs-xs); }
    .btn.md { height: 36px; padding: 0 14px; font-size: var(--fs-sm); }
    .btn.lg { height: 44px; padding: 0 18px; font-size: var(--fs-base); }

    .btn.primary {
      background: linear-gradient(135deg, var(--accent), var(--accent-2));
      color: white;
      box-shadow: var(--shadow-sm), 0 4px 12px rgba(108, 140, 255, 0.25);
    }
    .btn.primary:hover:not(:disabled) {
      box-shadow: var(--shadow-md), 0 8px 22px rgba(108, 140, 255, 0.35);
    }

    .btn.secondary {
      background: var(--bg-2);
      color: var(--text-0);
      border-color: var(--border-2);
    }
    .btn.secondary:hover:not(:disabled) { background: var(--bg-3); border-color: var(--border-3); }

    .btn.ghost {
      background: transparent;
      color: var(--text-1);
      border-color: transparent;
    }
    .btn.ghost:hover:not(:disabled) { background: var(--bg-3); color: var(--text-0); }

    .btn.danger {
      background: var(--danger-soft);
      color: var(--danger);
      border-color: rgba(248, 113, 113, 0.25);
    }
    .btn.danger:hover:not(:disabled) {
      background: rgba(248, 113, 113, 0.22);
      border-color: rgba(248, 113, 113, 0.45);
    }

    .btn.success {
      background: var(--success-soft);
      color: var(--success);
      border-color: rgba(52, 211, 153, 0.25);
    }
    .btn.success:hover:not(:disabled) {
      background: rgba(52, 211, 153, 0.22);
      border-color: rgba(52, 211, 153, 0.45);
    }

    .icon { display: inline-flex; align-items: center; font-size: 1.1em; }
    .label { display: inline-flex; align-items: center; }

    .spinner {
      width: 14px; height: 14px;
      border-radius: 50%;
      border: 2px solid currentColor;
      border-right-color: transparent;
      animation: spin 0.8s linear infinite;
      opacity: 0.85;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
  `;
}

customElements.define('l-button', LButton);
