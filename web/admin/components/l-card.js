// <l-card> — premium surface with optional title, action slot, gradient border
// glow on hover, accent variants for chromatic emphasis.
//
// Slots:
//   default     — body content
//   title       — overrides the `title` attribute when richer markup is needed
//   actions     — top-right action buttons (toolbar)
//   footer      — optional footer row
//
// Properties:
//   title       string
//   subtitle    string
//   variant     '' | 'glass' | 'flat' | 'hero'  (default '' = elevated)
//   accent      '' | 'blue' | 'violet' | 'pink' | 'cyan' | 'mint' | 'amber'
//                — adds a chromatic glow + gradient top-stripe
//   hover       bool — adds the subtle lift-on-hover affordance
//   loading     bool — shows skeleton overlay covering body
//   compact     bool — tighter padding for dense grids

import { LitElement, html, css, classMap } from './_lit.js';

class LCard extends LitElement {
  static properties = {
    title:    { type: String },
    subtitle: { type: String },
    variant:  { type: String, reflect: true },
    accent:   { type: String, reflect: true },
    hover:    { type: Boolean, reflect: true },
    loading:  { type: Boolean, reflect: true },
    compact:  { type: Boolean, reflect: true },
  };

  constructor() {
    super();
    this.title = '';
    this.subtitle = '';
    this.variant = '';
    this.accent = '';
    this.hover = false;
    this.loading = false;
    this.compact = false;
  }

  render() {
    const hasHeader = this.title || this.subtitle || this._hasSlot('title') || this._hasSlot('actions');
    return html`
      <div class="card ${classMap({
        glass: this.variant === 'glass',
        flat:  this.variant === 'flat',
        hero:  this.variant === 'hero',
        hover: this.hover,
        compact: this.compact,
      })}">
        ${this.accent ? html`<div class="stripe stripe-${this.accent}"></div>` : ''}
        ${hasHeader ? html`
          <div class="head">
            <div class="head-text">
              <slot name="title">
                ${this.title ? html`<div class="title">${this.title}</div>` : ''}
              </slot>
              ${this.subtitle ? html`<div class="subtitle">${this.subtitle}</div>` : ''}
            </div>
            <div class="head-actions"><slot name="actions"></slot></div>
          </div>` : ''}
        <div class="body">
          <slot></slot>
        </div>
        <slot name="footer"></slot>
        ${this.loading ? html`<div class="loading-veil"><div class="spinner"></div></div>` : ''}
      </div>
    `;
  }

  _hasSlot(name) {
    return !!this.querySelector(`[slot="${name}"]`);
  }

  static styles = css`
    :host {
      display: block;
      animation: fade-up var(--t-normal) var(--ease-spring) both;
    }
    @keyframes fade-up {
      from { opacity: 0; transform: translateY(8px); }
      to { opacity: 1; transform: none; }
    }

    .card {
      position: relative;
      background: var(--bg-1);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      box-shadow: var(--shadow-md);
      overflow: hidden;
      isolation: isolate;
      transition: transform var(--t-normal) var(--ease-spring),
                  box-shadow var(--t-normal) var(--ease-spring),
                  border-color var(--t-fast);
    }

    /* Gradient border halo on every card via a pseudo-mask ring */
    .card::after {
      content: '';
      position: absolute;
      inset: 0;
      border-radius: inherit;
      padding: 1px;
      background: var(--g-border-soft);
      -webkit-mask: linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0);
              mask: linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0);
      -webkit-mask-composite: xor;
              mask-composite: exclude;
      pointer-events: none;
      opacity: 0.8;
      transition: opacity var(--t-normal);
    }
    .card:hover::after { opacity: 1; background: var(--g-border); }

    .card.glass {
      background: var(--surface-glass);
      backdrop-filter: blur(24px) saturate(180%);
      -webkit-backdrop-filter: blur(24px) saturate(180%);
    }
    .card.flat {
      background: var(--bg-1);
      box-shadow: var(--shadow-sm);
    }
    .card.hero {
      background:
        radial-gradient(80% 100% at 0% 0%, rgba(119,145,255,0.16), transparent 60%),
        radial-gradient(60% 80% at 100% 100%, rgba(178,102,255,0.14), transparent 60%),
        var(--bg-1);
    }
    .card.hover { cursor: pointer; }
    .card.hover:hover {
      transform: translateY(-2px);
      box-shadow: var(--shadow-lg);
      border-color: var(--border-2);
    }
    .card.compact .body { padding: var(--s-3); }
    .card.compact .head { padding: var(--s-3) var(--s-3) 0; }

    /* Accent stripe — 3px top bar with the accent gradient */
    .stripe {
      position: absolute;
      top: 0; left: 0; right: 0;
      height: 3px;
      z-index: 1;
    }
    .stripe-blue   { background: var(--g-cyan); }
    .stripe-violet { background: var(--g-accent); }
    .stripe-pink   { background: var(--g-fire); }
    .stripe-cyan   { background: var(--g-cyan); }
    .stripe-mint   { background: var(--g-mint); }
    .stripe-amber  { background: var(--g-amber); }

    :host([accent="blue"]) .card,
    :host([accent="violet"]) .card {
      box-shadow: var(--shadow-md), 0 12px 32px rgba(119,145,255,0.10);
    }
    :host([accent="pink"]) .card {
      box-shadow: var(--shadow-md), 0 12px 32px rgba(255,107,214,0.10);
    }
    :host([accent="cyan"]) .card {
      box-shadow: var(--shadow-md), 0 12px 32px rgba(56,212,255,0.10);
    }
    :host([accent="mint"]) .card {
      box-shadow: var(--shadow-md), 0 12px 32px rgba(52,224,161,0.10);
    }
    :host([accent="amber"]) .card {
      box-shadow: var(--shadow-md), 0 12px 32px rgba(255,181,71,0.10);
    }

    .head {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      gap: var(--s-3);
      padding: var(--s-4) var(--s-4) 0;
    }
    .head-text { min-width: 0; }
    .title {
      font-family: var(--font-display);
      font-size: var(--fs-md);
      font-weight: var(--fw-semibold);
      letter-spacing: -0.01em;
      line-height: 1.2;
    }
    .subtitle {
      font-size: var(--fs-sm);
      color: var(--text-2);
      margin-top: 2px;
    }
    .head-actions { display: flex; gap: var(--s-2); flex-shrink: 0; align-items: center; }

    .body {
      padding: var(--s-4);
    }
    .head + .body {
      padding-top: var(--s-3);
    }

    .loading-veil {
      position: absolute;
      inset: 0;
      background: var(--surface-overlay);
      display: grid;
      place-items: center;
      backdrop-filter: blur(6px);
      z-index: 2;
    }
    .spinner {
      width: 26px; height: 26px;
      border-radius: 50%;
      border: 2px solid var(--border-2);
      border-top-color: var(--accent);
      border-right-color: var(--accent-2);
      animation: spin 0.9s linear infinite;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
  `;
}

customElements.define('l-card', LCard);
