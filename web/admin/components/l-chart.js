// <l-chart> — minimal line/area chart on canvas. No heavyweight library.
//
// Properties:
//   data    Array<{label, value}>   OR   Array<number>
//   color   string (CSS color)
//   height  number  (default 180)
//   area    bool — fill under the line
//   grid    bool — draw gridlines + axis ticks
//   format  'int' | 'ms' | 'bytes' | 'pct'   default 'int'
//
// Resamples to fit width; redraws on resize via ResizeObserver.

import { LitElement, html, css } from './_lit.js';

class LChart extends LitElement {
  static properties = {
    data:   { type: Array },
    color:  { type: String },
    height: { type: Number },
    area:   { type: Boolean },
    grid:   { type: Boolean },
    format: { type: String },
  };

  constructor() {
    super();
    this.data = [];
    this.color = 'var(--accent)';
    this.height = 180;
    this.area = true;
    this.grid = true;
    this.format = 'int';
    this._ro = null;
  }

  firstUpdated() {
    this._ro = new ResizeObserver(() => this._draw());
    this._ro.observe(this.renderRoot.querySelector('canvas'));
  }
  updated() { this._draw(); }
  disconnectedCallback() {
    super.disconnectedCallback();
    if (this._ro) this._ro.disconnect();
  }

  _draw() {
    const canvas = this.renderRoot.querySelector('canvas');
    if (!canvas) return;
    const dpr = window.devicePixelRatio || 1;
    const w = canvas.clientWidth, h = canvas.clientHeight;
    canvas.width = w * dpr;
    canvas.height = h * dpr;
    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, w, h);

    const series = (this.data || []).map(d => typeof d === 'number' ? d : d.value);
    if (series.length < 2) {
      ctx.fillStyle = 'rgba(255,255,255,0.3)';
      ctx.font = '12px var(--font-text), sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText('Нет данных', w / 2, h / 2);
      return;
    }

    const min = Math.min(...series, 0);
    const max = Math.max(...series);
    const range = (max - min) || 1;
    const padL = 36, padR = 8, padT = 12, padB = 22;
    const plotW = w - padL - padR;
    const plotH = h - padT - padB;
    const xStep = plotW / (series.length - 1);

    if (this.grid) {
      ctx.strokeStyle = 'rgba(255,255,255,0.06)';
      ctx.lineWidth = 1;
      ctx.fillStyle = 'rgba(255,255,255,0.4)';
      ctx.font = '10px var(--font-text), sans-serif';
      ctx.textAlign = 'right';
      ctx.textBaseline = 'middle';
      const ticks = 4;
      for (let i = 0; i <= ticks; i++) {
        const y = padT + (plotH * i) / ticks;
        ctx.beginPath();
        ctx.moveTo(padL, y);
        ctx.lineTo(w - padR, y);
        ctx.stroke();
        const v = max - (range * i) / ticks;
        ctx.fillText(formatValue(v, this.format), padL - 6, y);
      }
    }

    // Resolve CSS color (e.g. 'var(--accent)') to computed RGB.
    const probe = document.createElement('span');
    probe.style.color = this.color;
    document.body.appendChild(probe);
    const stroke = getComputedStyle(probe).color || this.color;
    document.body.removeChild(probe);

    const points = series.map((v, i) => [
      padL + i * xStep,
      padT + plotH - ((v - min) / range) * plotH,
    ]);

    if (this.area) {
      const grad = ctx.createLinearGradient(0, padT, 0, padT + plotH);
      grad.addColorStop(0, withAlpha(stroke, 0.32));
      grad.addColorStop(1, withAlpha(stroke, 0.0));
      ctx.beginPath();
      ctx.moveTo(points[0][0], padT + plotH);
      for (const p of points) ctx.lineTo(p[0], p[1]);
      ctx.lineTo(points[points.length - 1][0], padT + plotH);
      ctx.closePath();
      ctx.fillStyle = grad;
      ctx.fill();
    }

    ctx.beginPath();
    points.forEach((p, i) => i === 0 ? ctx.moveTo(p[0], p[1]) : ctx.lineTo(p[0], p[1]));
    ctx.strokeStyle = stroke;
    ctx.lineWidth = 2;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.stroke();

    // Last point dot
    const last = points[points.length - 1];
    ctx.fillStyle = stroke;
    ctx.beginPath();
    ctx.arc(last[0], last[1], 3.5, 0, Math.PI * 2);
    ctx.fill();
    ctx.strokeStyle = 'rgba(255,255,255,0.18)';
    ctx.lineWidth = 1;
    ctx.stroke();
  }

  render() {
    return html`<canvas style="height:${this.height}px"></canvas>`;
  }

  static styles = css`
    :host { display: block; }
    canvas { width: 100%; display: block; }
  `;
}

function formatValue(v, fmt) {
  switch (fmt) {
    case 'ms':    return Math.round(v) + 'ms';
    case 'bytes': return formatBytes(v);
    case 'pct':   return (v * 100).toFixed(0) + '%';
    case 'int':   return Math.round(v).toLocaleString();
    default:      return String(Math.round(v));
  }
}
function formatBytes(n) {
  if (!Number.isFinite(n)) return '0';
  const units = ['B', 'KB', 'MB', 'GB'];
  let i = 0; let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return v.toFixed(1) + ' ' + units[i];
}
function withAlpha(rgb, a) {
  // 'rgb(r, g, b)' → 'rgba(r,g,b,a)'; pass other formats through.
  const m = rgb.match(/^rgb\((\d+),\s*(\d+),\s*(\d+)\)$/);
  if (m) return `rgba(${m[1]},${m[2]},${m[3]},${a})`;
  return rgb;
}

customElements.define('l-chart', LChart);
