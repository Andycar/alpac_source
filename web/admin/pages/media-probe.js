// pages/media-probe.js — run ffprobe against a user-supplied URL.
//
// API:
//   GET /api/media/probe?url=https://...   → {ok, error, probe, summary, took_ms, target_url}
//
// Useful for "почему этот стрим не играет?" diagnostics. Output is the full
// ffprobe JSON formatted as a tree + a compact stream summary.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  url: '',
  result: null,
  loading: false,
  history: [], // last few probes in-session
};

async function probe() {
  const url = state.url.trim();
  if (!url) return toast.warn('Введите URL');
  state.loading = true; state.result = null; paint();
  try {
    const r = await api.get('/media/probe?url=' + encodeURIComponent(url));
    state.result = r;
    if (r && r.ok) {
      toast.success(`probe ok (${r.took_ms}ms)`);
      // Push to history (dedup by url, max 8).
      state.history = [{ url, at: new Date(), took_ms: r.took_ms, summary: r.summary }, ...state.history.filter(h => h.url !== url)].slice(0, 8);
    } else {
      toast.error('probe failed' + (r && r.error ? ': ' + r.error : ''));
    }
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.loading = false; paint();
  }
}

function paint() {
  const $root = document.getElementById('mp-root');
  if (!$root) return;
  render(html`
    <l-card title="Probe URL (ffprobe)">
      <div class="mp-bar">
        <l-input
          icon="🔗"
          placeholder="https://example.com/stream.m3u8 / .mp4 / .mpd"
          .value=${state.url}
          @input=${e => { state.url = e.detail.value; }}
          @keydown=${e => { if (e.key === 'Enter') probe(); }}
          style="flex:1;min-width:240px"
        ></l-input>
        <l-button variant="primary" icon="▶" @click=${probe} ?loading=${state.loading}>Probe</l-button>
      </div>
      <div class="mp-hint">
        ffprobe запускается локально на сервере; ответ может содержать upstream-headers, не публикуйте.
        Таймаут 10s.
      </div>
    </l-card>

    ${state.result ? probeResult(state.result) : ''}

    ${state.history.length ? html`
      <l-card style="margin-top:var(--s-5)" title="История (текущая сессия)">
        ${state.history.map(h => html`
          <button class="mp-hist" @click=${() => { state.url = h.url; paint(); probe(); }}>
            <span class="mp-hist-time">${h.at.toLocaleTimeString('ru-RU')}</span>
            <span class="mp-hist-took">${h.took_ms}ms</span>
            <span class="mp-hist-url">${h.url}</span>
          </button>
        `)}
      </l-card>` : ''}
  `, $root);
}

function probeResult(r) {
  const ok = !!r.ok;
  const summary = r.summary || {};
  const probe = r.probe || {};
  const streams = (probe.streams || []);
  const format = probe.format || {};
  return html`
    <l-card style="margin-top:var(--s-5)" title=${ok ? '✓ Результат' : '✗ Ошибка'}>
      <div slot="actions">
        <l-pill tone=${ok ? 'ok' : 'danger'} dot>${ok ? 'ok' : 'failed'}</l-pill>
        <l-pill tone="muted">${r.took_ms}ms</l-pill>
      </div>

      ${!ok && r.error
        ? html`<div class="mp-err">${r.error}</div>`
        : ''}

      ${ok ? html`
        <div class="mp-summary">
          ${summaryRow('Container', format.format_name)}
          ${summaryRow('Длительность', formatDuration(format.duration))}
          ${summaryRow('Битрейт', formatBitrate(format.bit_rate))}
          ${summaryRow('Size', formatBytes(format.size))}
          ${summary.video ? summaryRow('Видео', summary.video) : ''}
          ${summary.audio ? summaryRow('Аудио', summary.audio) : ''}
          ${summary.subtitles ? summaryRow('Субтитры', summary.subtitles) : ''}
        </div>

        ${streams.length ? html`
          <div class="mp-streams-h">Потоки (${streams.length})</div>
          <div class="mp-streams">
            ${streams.map(s => streamCard(s))}
          </div>
        ` : ''}

        <details style="margin-top:var(--s-4)">
          <summary class="mp-raw-h">Raw ffprobe JSON</summary>
          <pre class="mp-raw">${JSON.stringify(probe, null, 2)}</pre>
        </details>
      ` : ''}
    </l-card>
  `;
}

function summaryRow(label, value) {
  if (value == null || value === '') return '';
  return html`<div class="mp-row"><span>${label}</span><b>${value}</b></div>`;
}

function streamCard(s) {
  const isV = s.codec_type === 'video';
  const isA = s.codec_type === 'audio';
  const isS = s.codec_type === 'subtitle';
  const tone = isV ? 'info' : isA ? 'ok' : isS ? 'warn' : 'muted';
  return html`
    <div class="mp-stream ${s.codec_type}">
      <div class="mp-stream-h">
        <l-pill tone=${tone}>${s.codec_type || '?'}</l-pill>
        <span class="mp-stream-codec">${s.codec_name || '—'}${s.profile ? ' · ' + s.profile : ''}</span>
        ${s.index != null ? html`<span class="mp-stream-idx">#${s.index}</span>` : ''}
      </div>
      <div class="mp-stream-meta">
        ${isV ? html`
          <span>${s.width || '?'}×${s.height || '?'}</span>
          ${s.r_frame_rate ? html`<span>${frameRate(s.r_frame_rate)}</span>` : ''}
          ${s.bit_rate ? html`<span>${formatBitrate(s.bit_rate)}</span>` : ''}
        ` : ''}
        ${isA ? html`
          ${s.channels ? html`<span>${s.channels}ch${s.channel_layout ? ' · ' + s.channel_layout : ''}</span>` : ''}
          ${s.sample_rate ? html`<span>${s.sample_rate} Hz</span>` : ''}
          ${s.bit_rate ? html`<span>${formatBitrate(s.bit_rate)}</span>` : ''}
        ` : ''}
        ${(s.tags && s.tags.language) ? html`<span>lang: ${s.tags.language}</span>` : ''}
        ${(s.tags && s.tags.title) ? html`<span title=${s.tags.title}>«${s.tags.title.slice(0, 30)}»</span>` : ''}
      </div>
    </div>
  `;
}

function formatDuration(d) {
  const n = parseFloat(d);
  if (!Number.isFinite(n) || n <= 0) return '—';
  const h = Math.floor(n / 3600);
  const m = Math.floor((n % 3600) / 60);
  const s = Math.floor(n % 60);
  return (h ? h + 'h ' : '') + (m ? m + 'm ' : '') + s + 's';
}
function formatBitrate(b) {
  const n = parseFloat(b);
  if (!Number.isFinite(n) || n <= 0) return '—';
  if (n >= 1e6) return (n / 1e6).toFixed(2) + ' Mbps';
  return (n / 1e3).toFixed(0) + ' kbps';
}
function formatBytes(b) {
  const n = parseFloat(b);
  if (!Number.isFinite(n) || n <= 0) return '—';
  const u = ['B', 'KB', 'MB', 'GB']; let i = 0; let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return v.toFixed(1) + ' ' + u[i];
}
function frameRate(r) {
  if (!r || r === '0/0') return '—';
  const [a, b] = String(r).split('/');
  const n = parseFloat(a) / parseFloat(b || '1');
  return Number.isFinite(n) ? n.toFixed(2) + ' fps' : '—';
}

const styleId = 'l-mp-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .mp-bar { display: flex; gap: 8px; align-items: center; }
    .mp-hint { font-size: var(--fs-xs); color: var(--text-3); margin-top: var(--s-2); }
    .mp-err {
      margin-top: var(--s-3); padding: 10px 12px;
      color: #fca5a5; background: var(--danger-soft);
      border-radius: var(--r-2);
      font-family: var(--font-mono); font-size: var(--fs-sm);
      white-space: pre-wrap; word-break: break-word;
    }
    .mp-summary {
      display: grid; grid-template-columns: 1fr 1fr; gap: 6px 24px;
      margin-top: var(--s-3);
    }
    @media (max-width: 700px) { .mp-summary { grid-template-columns: 1fr; } }
    .mp-row { display: flex; justify-content: space-between; padding: 6px 0; border-bottom: 1px dashed var(--border-1); font-size: var(--fs-sm); }
    .mp-row span { color: var(--text-2); }
    .mp-row b { color: var(--text-0); font-family: var(--font-mono); font-size: var(--fs-xs); }
    .mp-streams-h {
      margin-top: var(--s-4); padding-bottom: var(--s-2);
      font-family: var(--font-display); font-weight: 600;
      font-size: var(--fs-md);
      border-bottom: 1px solid var(--border-1);
    }
    .mp-streams { display: flex; flex-direction: column; gap: 8px; margin-top: var(--s-3); }
    .mp-stream {
      padding: var(--s-3);
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-2);
    }
    .mp-stream.video    { border-left: 3px solid var(--info); }
    .mp-stream.audio    { border-left: 3px solid var(--success); }
    .mp-stream.subtitle { border-left: 3px solid var(--warn); }
    .mp-stream-h { display: flex; align-items: center; gap: 8px; }
    .mp-stream-codec { font-family: var(--font-mono); font-size: var(--fs-sm); color: var(--text-0); }
    .mp-stream-idx { margin-left: auto; font-size: var(--fs-xs); color: var(--text-3); font-family: var(--font-mono); }
    .mp-stream-meta { margin-top: 4px; display: flex; gap: 12px; flex-wrap: wrap; color: var(--text-2); font-size: var(--fs-xs); }
    .mp-raw-h { cursor: pointer; color: var(--accent); font-size: var(--fs-sm); }
    .mp-raw {
      margin-top: var(--s-2); padding: var(--s-3);
      background: var(--bg-0); border: 1px solid var(--border-1);
      border-radius: var(--r-2);
      font-family: var(--font-mono); font-size: var(--fs-xs);
      max-height: 360px; overflow: auto; white-space: pre;
      color: var(--text-1);
    }
    .mp-hist {
      display: flex; align-items: center; gap: 12px; width: 100%;
      padding: 8px 10px; background: transparent; border: 1px solid var(--border-1);
      border-radius: var(--r-2); cursor: pointer; color: var(--text-1);
      margin-bottom: 4px;
      transition: background var(--t-fast);
      text-align: left;
    }
    .mp-hist:hover { background: var(--bg-3); }
    .mp-hist-time { font-size: var(--fs-xs); color: var(--text-3); font-family: var(--font-mono); flex-shrink: 0; }
    .mp-hist-took { font-size: 10px; padding: 1px 6px; background: var(--bg-3); border-radius: 4px; color: var(--text-2); flex-shrink: 0; }
    .mp-hist-url { font-family: var(--font-mono); font-size: var(--fs-xs); color: var(--accent); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="mp-root"></div>`;
  paint();
}
export { render_ as render };
