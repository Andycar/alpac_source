// <l-pill> — semantic chip used for statuses.
//
// Properties:
//   tone   'ok' | 'warn' | 'danger' | 'info' | 'muted'   (default muted)
//   dot    bool — show the pulsing dot
import { LitElement, html, css } from './_lit.js';

class LPill extends LitElement {
  static properties = {
    tone: { type: String, reflect: true },
    dot:  { type: Boolean, reflect: true },
  };
  constructor() {
    super();
    this.tone = 'muted';
    this.dot = false;
  }
  render() {
    return html`<span class="pill"><span ?hidden=${!this.dot} class="dot"></span><slot></slot></span>`;
  }
  static styles = css`
    :host { display: inline-flex; }
    .pill {
      display: inline-flex; align-items: center; gap: 6px;
      height: 22px; padding: 0 10px;
      border-radius: var(--r-pill);
      font-size: var(--fs-xs); font-weight: var(--fw-semibold);
      letter-spacing: 0.02em; text-transform: uppercase;
      color: var(--c); background: var(--cs);
    }
    .dot {
      width: 6px; height: 6px;
      border-radius: 50%;
      background: currentColor;
      box-shadow: 0 0 0 0 currentColor;
      animation: pulse 1.8s var(--ease-out) infinite;
    }
    @keyframes pulse {
      0%   { box-shadow: 0 0 0 0 currentColor; opacity: 1; }
      80%  { box-shadow: 0 0 0 6px transparent; opacity: 0.6; }
      100% { box-shadow: 0 0 0 0 transparent; opacity: 1; }
    }
    :host([tone="ok"])     { --c: var(--success); --cs: var(--success-soft); }
    :host([tone="warn"])   { --c: var(--warn);    --cs: var(--warn-soft); }
    :host([tone="danger"]) { --c: var(--danger);  --cs: var(--danger-soft); }
    :host([tone="info"])   { --c: var(--info);    --cs: var(--info-soft); }
    :host([tone="muted"])  { --c: var(--text-2);  --cs: var(--bg-3); }
  `;
}
customElements.define('l-pill', LPill);
