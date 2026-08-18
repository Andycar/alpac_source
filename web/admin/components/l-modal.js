// <l-modal> — backdrop-blur dialog. open/close attribute, slot-based body.
//   <l-modal open title="Confirm">
//     ...content...
//     <div slot="actions"><l-button>OK</l-button></div>
//   </l-modal>

import { LitElement, html, css, classMap } from './_lit.js';

class LModal extends LitElement {
  static properties = {
    open: { type: Boolean, reflect: true },
    title: { type: String },
  };
  constructor() { super(); this.open = false; this.title = ''; }
  _close() {
    this.open = false;
    this.dispatchEvent(new CustomEvent('close', { bubbles: true, composed: true }));
  }
  _backdrop(e) { if (e.target === this.renderRoot.querySelector('.backdrop')) this._close(); }
  render() {
    return html`
      <div class="backdrop ${classMap({ open: this.open })}" @click=${this._backdrop}>
        <div class="dialog">
          <div class="head">
            <div class="title">${this.title}</div>
            <button class="x" @click=${this._close} aria-label="Close">×</button>
          </div>
          <div class="body"><slot></slot></div>
          <div class="actions"><slot name="actions"></slot></div>
        </div>
      </div>
    `;
  }
  static styles = css`
    .backdrop {
      position: fixed; inset: 0;
      background: rgba(7, 9, 13, 0.55);
      backdrop-filter: blur(8px);
      display: grid; place-items: center;
      z-index: var(--z-modal);
      opacity: 0; pointer-events: none;
      transition: opacity var(--t-normal);
    }
    .backdrop.open { opacity: 1; pointer-events: all; }
    .dialog {
      width: min(520px, calc(100% - 32px));
      background: var(--bg-1);
      border: 1px solid var(--border-2);
      border-radius: var(--r-4);
      box-shadow: var(--shadow-lg);
      transform: translateY(8px) scale(0.98);
      transition: transform var(--t-normal) var(--ease-spring);
      display: flex; flex-direction: column;
      max-height: calc(100vh - 64px);
    }
    .backdrop.open .dialog { transform: none; }
    .head {
      display: flex; align-items: center; justify-content: space-between;
      padding: var(--s-4) var(--s-4) var(--s-3);
      border-bottom: 1px solid var(--border-1);
    }
    .title { font-family: var(--font-display); font-size: var(--fs-md); font-weight: var(--fw-semibold); }
    .x { background: transparent; border: none; color: var(--text-2); font-size: 22px; cursor: pointer; line-height: 1; padding: 0; }
    .x:hover { color: var(--text-0); }
    .body { padding: var(--s-4); overflow-y: auto; color: var(--text-1); }
    .actions {
      padding: var(--s-3) var(--s-4) var(--s-4);
      border-top: 1px solid var(--border-1);
      display: flex; justify-content: flex-end; gap: var(--s-2);
    }
  `;
}
customElements.define('l-modal', LModal);
