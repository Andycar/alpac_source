// <l-stat> — big-number metric tile with optional delta + sparkline.
//
// Properties:
//   label    string  — top label (small caps)
//   value    string  — big number
//   suffix   string  — trailing unit (e.g. "MB", "ms")
//   delta    number  — % change vs previous window; positive=up
//   deltaInvert bool — when true, negative delta is "good" (e.g. latency)
//   trend    number[] — small array for sparkline (last N samples)
//   accent   'blue' | 'purple' | 'green' | 'amber' | 'pink' — accent color
//   icon     string  — emoji / symbol
//
// The sparkline is rendered inline-SVG. Spring-pop on first appearance.

import { LitElement, html, css, classMap } from './_lit.js';

class LStat extends LitElement {
  static properties = {
    label:       { type: String },
    value:       { type: String },
    suffix:      { type: String },
    delta:       { type: Number },
    deltaInvert: { type: Boolean, attribute: 'delta-invert' },
    trend:       { type: Array },
    accent:      { type: String, reflect: true },
    icon:        { type: String },
  };

  constructor() {
    super();
    this.label = '';
    this.value = '';
    this.suffix = '';
    this.delta = NaN;
    this.deltaInvert = false;
    this.trend = [];
    this.accent = 'blue';
    this.icon = '';
  }

  render() {
    const hasDelta = Number.isFinite(this.delta);
    const positive = hasDelta && this.delta > 0;
    const good = hasDelta && (this.deltaInvert ? this.delta < 0 : this.delta > 0);
    const bad  = hasDelta && (this.deltaInvert ? this.delta > 0 : this.delta < 0);

    return html`
      <div class="stat ${classMap({ ['accent-' + this.accent]: true })}">
        <div class="row-top">
          <div class="label">${this.label}</div>
          ${this.icon ? html`<div class="icon">${this.icon}</div>` : ''}
        </div>
        <div class="value-row">
          <div class="value">${this.value}</div>
          ${this.suffix ? html`<div class="suffix">${this.suffix}</div>` : ''}
        </div>
        <div class="row-bot">
          ${hasDelta ? html`
            <div class="delta ${classMap({ good, bad, neutral: !good && !bad })}">
              <span class="delta-arrow">${positive ? '▲' : (this.delta < 0 ? '▼' : '◆')}</span>
              ${Math.abs(this.delta).toFixed(1)}%
            </div>` : html`<div class="delta neutral">&nbsp;</div>`}
          ${(this.trend && this.trend.length > 1) ? sparklineSVG(this.trend) : ''}
        </div>
      </div>
    `;
  }

  static styles = css`
    :host {
      display: block;
      animation: pop var(--t-slow) var(--ease-spring) both;
    }
    @keyframes pop {
      from { opacity: 0; transform: scale(0.96) translateY(6px); }
      to { opacity: 1; transform: none; }
    }
    .stat {
      position: relative;
      padding: var(--s-4);
      background: var(--bg-1);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      box-shadow: var(--shadow-md);
      min-height: 132px;
      display: flex;
      flex-direction: column;
      justify-content: space-between;
      overflow: hidden;
    }
    .stat::before {
      content: '';
      position: absolute;
      top: 0; left: 0; right: 0;
      height: 2px;
      background: linear-gradient(90deg, var(--accent-from), var(--accent-to));
      opacity: 0.9;
    }
    .row-top, .row-bot {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--s-2);
    }
    .label {
      font-size: var(--fs-xs);
      font-weight: var(--fw-semibold);
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: var(--text-2);
    }
    .icon {
      font-size: 18px;
      color: var(--accent-from);
      filter: drop-shadow(0 0 8px var(--accent-from));
    }
    .value-row {
      display: flex;
      align-items: baseline;
      gap: var(--s-2);
      margin: var(--s-3) 0;
    }
    .value {
      font-family: var(--font-display);
      font-size: var(--fs-3xl);
      font-weight: var(--fw-bold);
      letter-spacing: -0.02em;
      line-height: 1;
      color: var(--text-0);
      font-variant-numeric: tabular-nums;
    }
    .suffix {
      font-size: var(--fs-md);
      color: var(--text-2);
      font-weight: var(--fw-medium);
    }
    .delta {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      font-size: var(--fs-xs);
      font-weight: var(--fw-semibold);
      padding: 2px 8px;
      border-radius: var(--r-pill);
    }
    .delta.good    { color: var(--success); background: var(--success-soft); }
    .delta.bad     { color: var(--danger);  background: var(--danger-soft); }
    .delta.neutral { color: var(--text-3); }
    .delta-arrow   { font-size: 9px; line-height: 1; }

    .spark { width: 80px; height: 28px; flex-shrink: 0; }

    /* accent ramp */
    :host([accent="blue"])   { --accent-from: #6c8cff; --accent-to: #8d6bff; }
    :host([accent="purple"]) { --accent-from: #b366ff; --accent-to: #ff6bd0; }
    :host([accent="green"])  { --accent-from: #34d399; --accent-to: #06b6d4; }
    :host([accent="amber"])  { --accent-from: #fbbf24; --accent-to: #fb923c; }
    :host([accent="pink"])   { --accent-from: #ec4899; --accent-to: #f43f5e; }
  `;
}

function sparklineSVG(samples) {
  // Sparkline is built inline so we don't need an SVG library. 80x28 viewbox.
  const w = 80, h = 28, pad = 2;
  const min = Math.min(...samples);
  const max = Math.max(...samples);
  const range = (max - min) || 1;
  const step = (w - pad * 2) / (samples.length - 1);
  const points = samples.map((v, i) => {
    const x = pad + i * step;
    const y = h - pad - ((v - min) / range) * (h - pad * 2);
    return [x, y];
  });
  const d = points.map((p, i) => (i === 0 ? 'M' : 'L') + p[0].toFixed(2) + ',' + p[1].toFixed(2)).join(' ');
  const area = d + ` L${w - pad},${h - pad} L${pad},${h - pad} Z`;
  return html`
    <svg class="spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <linearGradient id="spark-fill" x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%"  stop-color="var(--accent-from)" stop-opacity="0.35"/>
          <stop offset="100%" stop-color="var(--accent-from)" stop-opacity="0"/>
        </linearGradient>
      </defs>
      <path d=${area} fill="url(#spark-fill)"></path>
      <path d=${d} fill="none" stroke="var(--accent-from)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"></path>
    </svg>
  `;
}

customElements.define('l-stat', LStat);
