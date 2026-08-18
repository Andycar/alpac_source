// pages/server-stats.js — full server overview replacing the old admin's
// "Сервер" tab. Premium dashboard: ring charts (memory, uptime), KPI tiles
// (latency, throughput), processes (proxy/torrserver/ytdlp), browser pool,
// transcoding jobs, top routes + top proxy plugins. Auto-refresh via SSE
// /api/stats/stream with polling fallback (mirrors dashboard.js).

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';

let stream = null;
let pollTimer = null;
let lastTick = 0;

function fmtBytes(mb) {
  if (!Number.isFinite(mb)) return '—';
  if (mb < 1)       return (mb * 1024).toFixed(0) + ' KB';
  if (mb < 1024)    return mb.toFixed(mb < 10 ? 2 : 1) + ' MB';
  return (mb / 1024).toFixed(2) + ' GB';
}
function fmtDur(sec) {
  if (!Number.isFinite(sec) || sec < 0) return '—';
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = Math.floor(sec % 60);
  if (d) return `${d}д ${h}ч ${m}м`;
  if (h) return `${h}ч ${m}м ${s}с`;
  if (m) return `${m}м ${s}с`;
  return `${s}с`;
}
function fmtCount(n)  { return Number(n || 0).toLocaleString('ru-RU'); }
function fmtMs(n)     { return Math.round(Number(n || 0)) + ' мс'; }
function fmtCPU(sec) {
  if (!Number.isFinite(sec) || sec <= 0) return '0 с';
  if (sec < 60) return sec.toFixed(1) + ' с';
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  if (m < 60) return `${m}м ${s}с`;
  const h = Math.floor(m / 60);
  return `${h}ч ${m % 60}м`;
}
const PROC_STATE_LABEL = { R: 'running', S: 'sleeping', D: 'disk-wait', Z: 'zombie', T: 'stopped', I: 'idle' };
const PROC_STATE_TONE  = { R: 'ok', S: 'muted', D: 'warn', Z: 'danger', T: 'warn', I: 'muted' };

// SVG ring chart — `pct` 0..1, hue 'blue'|'violet'|'pink'|'cyan'|'mint'|'amber'
function ring({ pct, hue, label, value, sub }) {
  const r = 44;
  const c = 2 * Math.PI * r;
  const o = c * (1 - Math.max(0, Math.min(1, pct)));
  const gradId = `g-${hue}-${Math.floor(Math.random()*1e9)}`;
  const colors = {
    blue:   ['#38d4ff', '#7791ff'],
    violet: ['#7791ff', '#b266ff'],
    pink:   ['#b266ff', '#ff6bd6'],
    cyan:   ['#34e0a1', '#38d4ff'],
    mint:   ['#34e0a1', '#7791ff'],
    amber:  ['#ffb547', '#ff6bd6'],
  }[hue] || ['#7791ff', '#b266ff'];
  return html`
    <div class="ring-wrap">
      <div class="ring-box">
        <svg viewBox="0 0 100 100" class="ring">
          <defs>
            <linearGradient id=${gradId} x1="0" y1="0" x2="1" y2="1">
              <stop offset="0%" stop-color=${colors[0]}/>
              <stop offset="100%" stop-color=${colors[1]}/>
            </linearGradient>
          </defs>
          <circle cx="50" cy="50" r=${r} fill="none" stroke="var(--bg-3)" stroke-width="8"/>
          <circle cx="50" cy="50" r=${r} fill="none" stroke="url(#${gradId})"
                  stroke-width="8" stroke-linecap="round"
                  stroke-dasharray=${c}
                  stroke-dashoffset=${o}
                  transform="rotate(-90 50 50)"
                  style="transition:stroke-dashoffset 600ms cubic-bezier(0.22,1,0.36,1)"/>
        </svg>
        <div class="ring-text">
          <div class="ring-value">${value}</div>
          ${sub ? html`<div class="ring-sub">${sub}</div>` : ''}
        </div>
      </div>
      <div class="ring-label">${label}</div>
    </div>
  `;
}

function procPill(label, alive, hint) {
  return html`
    <div class="proc-pill">
      <span class="proc-dot ${alive ? 'on' : 'off'}"></span>
      <div class="proc-text">
        <div class="proc-label">${label}</div>
        <div class="proc-hint">${hint}</div>
      </div>
    </div>
  `;
}

// Process card for OS/процесс section.
// Shape: { pid, rss_kb, cpu_user_sec, cpu_sys_sec, threads, state }
function renderProcessCard(name, p) {
  const isSelf  = name === 'lampac-go';
  const isProxy = name.startsWith('proxy:');
  const isCust  = name.startsWith('custbal:');
  const isFFmpeg = name.startsWith('ffmpeg:');
  // Strip the prefix from the display name where present.
  let displayName = name;
  let tag = '';
  if (isProxy)  { displayName = name.slice('proxy:'.length);   tag = 'proxy'; }
  if (isCust)   { displayName = name.slice('custbal:'.length); tag = 'custbal'; }
  if (isFFmpeg) { displayName = name.slice('ffmpeg:'.length);  tag = 'ffmpeg'; }
  if (isSelf)   { tag = 'main'; }
  const rssMB = (p.rss_kb || 0) / 1024;
  const cpuTotal = (p.cpu_user_sec || 0) + (p.cpu_sys_sec || 0);
  const stateLabel = PROC_STATE_LABEL[p.state] || p.state || '?';
  const stateTone  = PROC_STATE_TONE[p.state] || 'muted';
  return html`
    <div class="ps-card ${isSelf ? 'self' : ''}">
      <div class="ps-head">
        <div class="ps-name" title=${name}>${displayName || name}</div>
        ${tag ? html`<span class="ps-tag ps-tag-${isSelf ? 'self' : (isProxy ? 'proxy' : (isCust ? 'cust' : 'ff'))}">${tag}</span>` : ''}
      </div>
      <div class="ps-stats">
        <div class="ps-stat">
          <span class="ps-stat-l">PID</span>
          <span class="ps-stat-v">${p.pid}</span>
        </div>
        <div class="ps-stat">
          <span class="ps-stat-l">RSS</span>
          <span class="ps-stat-v">${fmtBytes(rssMB)}</span>
        </div>
        <div class="ps-stat">
          <span class="ps-stat-l">CPU</span>
          <span class="ps-stat-v">${fmtCPU(cpuTotal)}</span>
        </div>
        <div class="ps-stat">
          <span class="ps-stat-l">threads</span>
          <span class="ps-stat-v">${p.threads || 0}</span>
        </div>
        <div class="ps-stat" style="grid-column:span 2">
          <span class="ps-stat-l">state</span>
          <span class=${'pill ' + stateTone}><span class="dot"></span> ${stateLabel}</span>
        </div>
      </div>
    </div>
  `;
}

// Pretty-renderer for the Chrome browser pool stats.
// Server shape: { allocator_active: bool, max_concurrent: int, active_sessions: int,
//                 available_slots: int, stream_cache_max: int, stream_cache_ttl_h: int }
function renderChromePool(chrome) {
  const active = chrome.active_sessions ?? 0;
  const max    = chrome.max_concurrent ?? 0;
  const avail  = chrome.available_slots ?? Math.max(0, max - active);
  const allocOn = !!chrome.allocator_active;
  const cacheMax = chrome.stream_cache_max ?? 0;
  const cacheTTL = chrome.stream_cache_ttl_h ?? 0;
  // Slots strip: fill `active` cells in accent, rest in muted
  const slots = [];
  for (let i = 0; i < max; i++) slots.push(i < active);
  return html`
    <div class="chrome-grid">
      <div class="chrome-row">
        <span class="cl">Аллокатор</span>
        <span class="cv">${allocOn
          ? html`<span class="pill ok"><span class="dot"></span> активен</span>`
          : html`<span class="pill muted"><span class="dot"></span> idle</span>`}</span>
      </div>
      <div class="chrome-row">
        <span class="cl">Сессии</span>
        <span class="cv">${active} / ${max}${avail > 0 ? html`<span class="cv-sub"> · свободно ${avail}</span>` : ''}</span>
      </div>
      ${slots.length ? html`
        <div class="chrome-slots">
          ${slots.map(busy => html`<span class=${'chrome-slot ' + (busy ? 'busy' : 'free')}></span>`)}
        </div>
      ` : ''}
      <div class="chrome-row">
        <span class="cl">Кэш стримов</span>
        <span class="cv">${fmtCount(cacheMax)} записей · TTL ${cacheTTL} ${cacheTTL === 1 ? 'час' : 'часов'}</span>
      </div>
    </div>
  `;
}

// Pretty-renderer for sidecar proxies (xray / mihomo / etc.).
// Shape: { label, engine, pid, socks_addr, balancers[], alive, uptime_sec }
function proxyCard(p) {
  const label = p.label || p.engine || '?';
  const sub   = p.socks_addr || (p.port ? ':' + p.port : '');
  const bals  = Array.isArray(p.balancers) ? p.balancers : [];
  const uptime = p.uptime_sec || p.uptimeSec || 0;
  return html`
    <div class="proxy-card">
      <div class="proxy-head">
        <div class="proxy-dot ${p.alive ? 'on' : 'off'}"></div>
        <div class="proxy-name">${label}</div>
        <div class="proxy-engine">${p.engine || ''}</div>
      </div>
      <div class="proxy-meta">
        ${sub ? html`<span class="chip"><span class="m-key">socks</span>${sub}</span>` : ''}
        ${p.pid > 0 ? html`<span class="chip"><span class="m-key">PID</span>${p.pid}</span>` : ''}
        ${uptime > 0 ? html`<span class="chip"><span class="m-key">up</span>${fmtDur(uptime)}</span>` : ''}
      </div>
      ${bals.length ? html`
        <div class="proxy-bals">
          ${bals.map(b => html`<span class="bal-chip">${b}</span>`)}
        </div>
      ` : html`<div class="proxy-no-bals">не привязан ни к одному балансеру</div>`}
    </div>
  `;
}

function applySnap($mount, snap) {
  const mem = snap.mem || snap.memory || {};
  const reqs = snap.requests || {};
  const lat = reqs.percentiles || reqs.latency_ms || {};
  const heapMB = mem.heap_alloc_mb ?? mem.heap_inuse_mb ?? 0;
  const sysMB  = mem.sys_mb ?? 0;
  const heapPct = sysMB > 0 ? Math.min(1, heapMB / sysMB) : 0;
  const goroutines = snap.goroutines || 0;
  const uptime = snap.uptime_sec || 0;
  const numCPU = snap.num_cpu || 1;
  const reqMin = reqs.req_min ?? reqs.per_minute ?? 0;
  const reqHour = reqs.req_hour ?? 0;
  const active = reqs.active ?? 0;
  const avg = reqs.latency_avg ?? lat.avg ?? 0;
  const p50 = lat.p50 ?? lat.P50 ?? 0;
  const p95 = lat.p95 ?? lat.P95 ?? 0;
  const p99 = lat.p99 ?? lat.P99 ?? 0;

  const processes = snap.processes || {};
  const proxyList = Array.isArray(processes.proxy) ? processes.proxy : [];
  const ts = processes.torrserver || {};
  const yt = processes.ytdlp || {};
  const chrome = snap.chrome || {};
  const transcoding = snap.transcoding || {};
  const osInfo = snap.os || {};
  const psInfo = snap.process_stats || {};

  const topRoutes = reqs.top_routes || [];
  const topProxies = reqs.top_proxy_plugins || [];
  const customBalancers = snap.custom_balancers || [];

  // uptime "fill" — visually shows 1 day = full ring, more = full
  const uptimePct = Math.min(1, uptime / 86400);

  render(html`
    <section class="hero">
      <div style="display:flex;justify-content:space-between;align-items:flex-start;gap:var(--s-5);flex-wrap:wrap">
        <div>
          <h1>Сервер</h1>
          <p>${osInfo.hostname || 'localhost'} · ${osInfo.goos}/${osInfo.goarch} · PID ${osInfo.pid} · Go ${snap.go_version} · ${numCPU} CPU</p>
          <div class="hero-actions">
            <span class="chip accent">v ${snap.app_version || '—'}</span>
            <span class="chip">uptime ${fmtDur(uptime)}</span>
            <span class="chip">proxylinks ${fmtCount(snap.proxylink_entries)}</span>
          </div>
        </div>
      </div>
    </section>

    <div class="grid cols-4">
      <l-card accent="violet" compact>
        ${ring({ pct: heapPct, hue: 'violet', label: 'Heap', value: fmtBytes(heapMB), sub: `/ sys ${fmtBytes(sysMB)}` })}
      </l-card>
      <l-card accent="cyan" compact>
        ${ring({ pct: uptimePct, hue: 'cyan', label: 'Uptime', value: fmtDur(uptime).split(' ')[0], sub: fmtDur(uptime).split(' ').slice(1).join(' ') || 'started' })}
      </l-card>
      <l-card accent="mint" compact>
        ${ring({ pct: Math.min(1, goroutines / 1000), hue: 'mint', label: 'Goroutines', value: fmtCount(goroutines), sub: 'runtime threads' })}
      </l-card>
      <l-card accent="amber" compact>
        ${ring({ pct: Math.min(1, p99 / 2000), hue: 'amber', label: 'p99 latency', value: fmtMs(p99), sub: 'last minute' })}
      </l-card>
    </div>

    <div class="section-title">Throughput</div>
    <div class="grid cols-4">
      <l-stat accent="blue"   label="Запросов / мин"  value=${fmtCount(reqMin)}    icon="↗"></l-stat>
      <l-stat accent="violet" label="Запросов / час"  value=${fmtCount(reqHour)}   icon="∑"></l-stat>
      <l-stat accent="pink"   label="Активных"         value=${fmtCount(active)}    icon="●"></l-stat>
      <l-stat accent="green"  label="GC"               value=${fmtCount(mem.gc_count)} suffix=${'pause ' + Math.round(mem.gc_pause_ms || 0) + ' мс'} icon="♻"></l-stat>
    </div>

    <div class="section-title">Latency</div>
    <div class="grid cols-4">
      <l-stat accent="blue"  label="avg" value=${fmtMs(avg)} delta-invert></l-stat>
      <l-stat accent="green" label="p50" value=${fmtMs(p50)} delta-invert></l-stat>
      <l-stat accent="amber" label="p95" value=${fmtMs(p95)} delta-invert></l-stat>
      <l-stat accent="pink"  label="p99" value=${fmtMs(p99)} delta-invert></l-stat>
    </div>

    <div class="section-title">Процессы</div>
    <div class="grid cols-3">
      <l-card title=${'Прокси · ' + proxyList.length} subtitle="xray / mihomo sidecars" accent="violet">
        ${proxyList.length ? html`
          <div class="proxy-list">
            ${proxyList.map(p => proxyCard(p))}
          </div>
        ` : html`<div class="empty">Прокси не настроены</div>`}
      </l-card>

      <l-card title="TorrServer" subtitle=${ts.inprocess ? 'in-process' : (ts.external ? 'external' : 'local port')} accent="cyan">
        ${ts.configured ? html`
          <dl class="kv">
            <dt>Status</dt><dd>${ts.alive ? html`<span class="pill ok"><span class="dot"></span> alive</span>` : html`<span class="pill danger"><span class="dot"></span> down</span>`}</dd>
            ${ts.url ? html`<dt>URL</dt><dd>${ts.url}</dd>` : ''}
            ${ts.port ? html`<dt>Port</dt><dd>${ts.port}</dd>` : ''}
            ${ts.cache_size != null ? html`<dt>Cache</dt><dd>${fmtBytes(ts.cache_size / 1048576)}</dd>` : ''}
            ${ts.preload_size != null ? html`<dt>Preload</dt><dd>${fmtBytes(ts.preload_size / 1048576)}</dd>` : ''}
            ${ts.active_torrents != null ? html`<dt>Active torrents</dt><dd>${ts.active_torrents}</dd>` : ''}
          </dl>
        ` : html`<div class="empty">Не настроен</div>`}
      </l-card>

      <l-card title="yt-dlp" subtitle="YouTube fetcher" accent="pink">
        ${yt.available ? html`
          <dl class="kv">
            <dt>Status</dt><dd><span class="pill ok"><span class="dot"></span> available</span></dd>
            ${yt.version ? html`<dt>Version</dt><dd>${yt.version}</dd>` : ''}
            ${yt.ffmpeg ? html`<dt>ffmpeg</dt><dd>${yt.ffmpeg}</dd>` : ''}
            ${yt.cookies != null ? html`<dt>Cookies</dt><dd>${yt.cookies ? 'on' : 'off'}</dd>` : ''}
            ${yt.js_runtime ? html`<dt>JS runtime</dt><dd>${yt.js_runtime}</dd>` : ''}
            ${yt.active_mux_jobs != null ? html`<dt>Active mux</dt><dd>${yt.active_mux_jobs} (cache ${yt.mux_cache_size})</dd>` : ''}
          </dl>
        ` : html`<div class="empty">Не установлен</div>`}
      </l-card>
    </div>

    <div class="grid cols-2" style="margin-top:var(--s-4)">
      <l-card title="Chrome pool" subtitle="chromedp / playwright" accent="blue">
        ${chrome && Object.keys(chrome).length ? renderChromePool(chrome) : html`<div class="empty">Не активен</div>`}
      </l-card>

      <l-card title="Транскодинг" subtitle="HLS jobs" accent="amber">
        ${transcoding.enabled ? html`
          <dl class="kv">
            <dt>Active jobs</dt><dd>${transcoding.active_jobs ?? 0}</dd>
            <dt>Disk used</dt><dd>${fmtBytes((transcoding.disk_used ?? 0) / 1048576)}</dd>
            <dt>Disk budget</dt><dd>${fmtBytes((transcoding.disk_budget ?? 0) / 1048576)}</dd>
          </dl>
          ${Array.isArray(transcoding.jobs) && transcoding.jobs.length ? html`
            <div style="margin-top:var(--s-3);font-size:var(--fs-sm);color:var(--text-2)">
              ${transcoding.jobs.length} job(s) running
            </div>
          ` : ''}
        ` : html`<div class="empty">Транскодинг выключен</div>`}
      </l-card>
    </div>

    ${customBalancers.length ? html`
      <div class="section-title">Кастомные балансеры (sidecars)</div>
      <l-card variant="glass">
        <l-table
          .columns=${[
            { key: 'name', label: 'Name' },
            { key: 'port', label: 'Port', align: 'r' },
            { key: 'status', label: 'Status' },
            { key: 'pid', label: 'PID', align: 'r' },
          ]}
          .rows=${customBalancers}
          empty="—"
        ></l-table>
      </l-card>
    ` : ''}

    <div class="section-title">Топ-маршруты</div>
    <div class="grid cols-2">
      <l-card title="По времени отклика" subtitle="avg latency · последний интервал" accent="violet">
        <l-table
          .columns=${[
            { key: 'route', label: 'Маршрут' },
            { key: 'count', label: 'REQ', align: 'r', format: 'int', sortable: true },
            { key: 'avg_ms', label: 'AVG', align: 'r', format: 'ms', sortable: true },
            { key: 'p95_ms', label: 'P95', align: 'r', format: 'ms', sortable: true },
            { key: 'p99_ms', label: 'P99', align: 'r', format: 'ms', sortable: true },
            { key: 'errors_5xx', label: '5XX', align: 'r', format: 'int' },
          ]}
          .rows=${topRoutes}
          empty="Нет данных"
        ></l-table>
      </l-card>
      <l-card title="Топ прокси-плагины" subtitle="latency по plugin" accent="cyan">
        <l-table
          .columns=${[
            { key: 'plugin', label: 'PL' },
            { key: 'count', label: 'REQ', align: 'r', format: 'int', sortable: true },
            { key: 'avg_ms', label: 'AVG', align: 'r', format: 'ms', sortable: true },
            { key: 'p95_ms', label: 'P95', align: 'r', format: 'ms', sortable: true },
            { key: 'p99_ms', label: 'P99', align: 'r', format: 'ms', sortable: true },
          ]}
          .rows=${topProxies}
          empty="Нет данных"
        ></l-table>
      </l-card>
    </div>

    ${psInfo && Object.keys(psInfo).length ? html`
      <div class="section-title">OS / процессы <span style="color:var(--text-3);font-size:var(--fs-xs);font-weight:var(--fw-medium);text-transform:none;letter-spacing:0">${Object.keys(psInfo).length}</span></div>
      <div class="ps-grid">
        ${Object.entries(psInfo)
          .sort(([,a],[,b]) => (b.rss_kb||0) - (a.rss_kb||0))
          .map(([name, p]) => renderProcessCard(name, p))}
      </div>
    ` : ''}

    <style>
      .hero { margin-bottom: var(--s-5); }

      /* Ring chart — centered overlay over a square SVG box */
      .ring-wrap {
        display: flex; flex-direction: column; align-items: center;
        gap: var(--s-2);
        padding: var(--s-3) 0 var(--s-2);
      }
      .ring-box {
        position: relative;
        width: 140px; height: 140px;
      }
      .ring { width: 100%; height: 100%; display: block; }
      .ring-text {
        position: absolute; inset: 0;
        display: flex; flex-direction: column;
        align-items: center; justify-content: center;
        text-align: center; pointer-events: none;
        padding: 0 12px;
      }
      .ring-value {
        font-family: var(--font-display);
        font-size: var(--fs-xl); font-weight: var(--fw-bold);
        letter-spacing: -0.02em; line-height: 1.1;
        background: var(--g-text-accent);
        -webkit-background-clip: text;
                background-clip: text;
        color: transparent;
      }
      .ring-sub   { font-size: var(--fs-xs); color: var(--text-2); margin-top: 4px; line-height: 1.2; }
      .ring-label { font-size: var(--fs-sm); color: var(--text-1); font-weight: var(--fw-semibold); margin-top: 4px; }

      /* Proxy sidecar card */
      .proxy-list { display: flex; flex-direction: column; gap: var(--s-2); }
      .proxy-card {
        padding: 10px 12px;
        background: var(--bg-2);
        border: 1px solid var(--border-1);
        border-radius: var(--r-2);
        transition: border-color var(--t-fast);
      }
      .proxy-card:hover { border-color: var(--border-3); }
      .proxy-head { display: flex; align-items: center; gap: 8px; }
      .proxy-dot {
        width: 9px; height: 9px; border-radius: 50%; flex-shrink: 0;
      }
      .proxy-dot.on  { background: var(--success); box-shadow: 0 0 8px var(--success); animation: pulse-soft 2.4s infinite; }
      .proxy-dot.off { background: var(--danger); }
      .proxy-name {
        font-family: var(--font-display);
        font-size: var(--fs-sm); font-weight: var(--fw-semibold);
        flex: 1; min-width: 0;
        overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      }
      .proxy-engine {
        font-family: var(--font-mono); font-size: var(--fs-xs);
        color: var(--text-3);
        padding: 1px 8px; border-radius: var(--r-pill);
        background: var(--bg-1);
      }
      .proxy-meta {
        display: flex; gap: 6px; flex-wrap: wrap;
        margin-top: 6px;
      }
      .proxy-meta .chip {
        font-family: var(--font-mono); font-size: 11px;
        padding: 2px 8px;
      }
      .proxy-meta .m-key {
        color: var(--text-3); margin-right: 4px;
        font-family: var(--font-text); font-weight: var(--fw-semibold);
        text-transform: uppercase; font-size: 9px; letter-spacing: 0.06em;
      }
      .proxy-bals {
        display: flex; gap: 4px; flex-wrap: wrap;
        margin-top: 8px;
        padding-top: 6px;
        border-top: 1px dashed var(--border-1);
      }
      .bal-chip {
        font-family: var(--font-mono); font-size: 10.5px;
        padding: 2px 7px;
        background: var(--accent-soft); color: var(--accent);
        border-radius: 4px; line-height: 1.4;
      }
      .proxy-no-bals {
        margin-top: 8px; padding-top: 6px;
        border-top: 1px dashed var(--border-1);
        font-size: var(--fs-xs); color: var(--text-3); font-style: italic;
      }

      /* Chrome pool */
      .chrome-grid { display: flex; flex-direction: column; gap: var(--s-2); }
      .chrome-row {
        display: flex; align-items: center; justify-content: space-between;
        padding: 4px 0;
        font-size: var(--fs-sm);
        border-bottom: 1px dashed var(--border-1);
      }
      .chrome-row:last-child { border-bottom: 0; }
      .chrome-row .cl { color: var(--text-2); }
      .chrome-row .cv {
        color: var(--text-0); font-family: var(--font-mono);
        display: inline-flex; align-items: baseline; gap: 6px;
      }
      .chrome-row .cv-sub { color: var(--text-3); font-size: var(--fs-xs); }
      .chrome-slots {
        display: flex; gap: 3px; flex-wrap: wrap;
        margin: 4px 0 8px;
      }
      .chrome-slot {
        width: 16px; height: 6px; border-radius: 3px;
        background: var(--bg-3);
      }
      .chrome-slot.busy {
        background: var(--g-accent);
        box-shadow: 0 0 6px rgba(119,145,255,0.5);
      }

      /* OS / processes grid */
      .ps-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
        gap: var(--s-3);
      }
      .ps-card {
        padding: var(--s-3);
        background: var(--bg-1);
        border: 1px solid var(--border-1);
        border-radius: var(--r-2);
        transition: border-color var(--t-fast);
      }
      .ps-card:hover { border-color: var(--border-3); }
      .ps-card.self {
        background: radial-gradient(80% 100% at 0% 0%, rgba(119,145,255,0.12), transparent 60%), var(--bg-1);
        border-color: rgba(119,145,255,0.32);
      }
      .ps-head { display: flex; justify-content: space-between; align-items: center; gap: var(--s-2); margin-bottom: var(--s-2); }
      .ps-name {
        font-family: var(--font-display); font-weight: var(--fw-semibold);
        font-size: var(--fs-sm); letter-spacing: -0.01em;
        overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
        flex: 1; min-width: 0;
      }
      .ps-tag {
        font-size: 9px; font-weight: var(--fw-bold); letter-spacing: 0.06em;
        text-transform: uppercase;
        padding: 1px 7px; border-radius: var(--r-pill);
        background: var(--bg-3); color: var(--text-2);
      }
      .ps-tag-self  { background: var(--accent-soft); color: var(--accent); }
      .ps-tag-proxy { background: rgba(178,102,255,0.16); color: #c89cff; }
      .ps-tag-cust  { background: rgba(56,212,255,0.16);  color: #8fdfff; }
      .ps-tag-ff    { background: rgba(255,181,71,0.16);  color: #ffd58a; }
      .ps-stats {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 4px 12px;
      }
      .ps-stat { display: flex; justify-content: space-between; align-items: baseline; font-size: var(--fs-xs); }
      .ps-stat-l { color: var(--text-3); text-transform: uppercase; font-size: 10px; letter-spacing: 0.06em; }
      .ps-stat-v { color: var(--text-0); font-family: var(--font-mono); font-size: 12px; }

      .empty { color: var(--text-3); font-size: var(--fs-sm); padding: var(--s-3) 0; text-align: center; }
    </style>
  `, $mount);
}

async function fetchOnce($mount) {
  try {
    const snap = await api.stats();
    applySnap($mount, snap);
    lastTick = Date.now();
  } catch (e) {
    render(html`<l-card><div style="color:var(--danger)">Stats unavailable: ${e.message}</div></l-card>`, $mount);
  }
}

export async function render_($mount) {
  render(html`<l-card loading title="Загрузка..."></l-card>`, $mount);
  await fetchOnce($mount);

  if (stream) stream.close();
  stream = api.sse('/stats/stream');
  stream.on('snapshot', (snap) => { if (snap) { lastTick = Date.now(); applySnap($mount, snap); } });
  stream.on('message', () => { lastTick = Date.now(); });

  if (pollTimer) clearInterval(pollTimer);
  pollTimer = setInterval(() => {
    if (document.visibilityState !== 'visible') return;
    if (Date.now() - lastTick > 12000) fetchOnce($mount);
  }, 5000);

  const guard = new MutationObserver(() => {
    if (!document.body.contains($mount)) {
      if (stream) stream.close(); stream = null;
      if (pollTimer) clearInterval(pollTimer); pollTimer = null;
      guard.disconnect();
    }
  });
  if ($mount.parentNode) guard.observe($mount.parentNode, { childList: true });
}

export { render_ as render };
