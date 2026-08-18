// pages/dashboard.js — command-center overview.
//
// Deliberately NOT a clone of the Server page (which owns the deep runtime
// metrics: heap rings, GC, goroutines, latency percentiles, per-process OS
// stats). The Dashboard answers "is everything OK and what needs my
// attention?" at a glance, then deep-links into the relevant tab.
//
// Layout:
//   1. Hero — greeting, host/version/uptime chips
//   2. KPI row — users, active devices, balancers enabled, requests/min
//   3. Health grid — one card per subsystem (server/balancers/cluster/
//      torrserver/transcoding/ytdlp), status pill + one-line summary, the
//      whole card deep-links to its tab
//   4. Quick actions — common jumps (users / broadcast / logs / WAF)
//
// Data: parallel Promise.allSettled over /stats /users /balancers /cluster
// so a single failing endpoint never blanks the whole page. The requests
// tile refreshes live via the /stats/stream SSE.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { icon } from '../icons.js';

let stream = null;
let pollTimer = null;
let lastTick = 0;
let snap = {};        // /api/stats
let usersData = null; // /api/users
let balData = null;   // /api/balancers
let cluData = null;   // /api/cluster

function fmtCount(n) { return Number(n || 0).toLocaleString('ru-RU'); }
function fmtDur(sec) {
  if (!Number.isFinite(sec) || sec < 0) return '—';
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  if (d) return `${d}д ${h}ч`;
  if (h) return `${h}ч ${m}м`;
  if (m) return `${m}м`;
  return `${Math.floor(sec)}с`;
}
function nav(key) { window.location.hash = '#/' + key; }

async function loadAll() {
  const [s, u, b, c] = await Promise.allSettled([
    api.stats(),
    api.users().catch(() => null),
    api.balancers().catch(() => null),
    api.get('/cluster').catch(() => null),
  ]);
  if (s.status === 'fulfilled') snap = s.value || {};
  if (u.status === 'fulfilled') usersData = u.value;
  if (b.status === 'fulfilled') balData = b.value;
  if (c.status === 'fulfilled') cluData = c.value;
}

function deriveUsers() {
  const rows = Array.isArray(usersData) ? usersData : (usersData?.users || []);
  const total = rows.length;
  const devices = rows.reduce((a, r) => a + (r.device_count || 0), 0);
  const expired = rows.filter(r => r.expired).length;
  return { total, devices, expired, active: total - expired };
}

function deriveBalancers() {
  const list = (balData && balData.balancers) || [];
  let enabled = 0;
  for (const b of list) {
    const f = b.fields || {};
    if (f.enabled === true || f.enable === true) enabled++;
  }
  return { total: list.length, enabled };
}

function deriveCluster() {
  if (!cluData) return null;
  const nodes = cluData.nodes || [];
  const healthy = nodes.filter(n => n.healthy).length;
  return { enabled: !!cluData.enabled, mode: cluData.mode, total: nodes.length, healthy };
}

function healthCard({ glyph, title, accent, statusTone, statusLabel, lines, key }) {
  return html`
    <div class="hc ${'hc-' + (accent || 'violet')}" @click=${() => nav(key)} role="button" tabindex="0"
         @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); nav(key); } }}>
      <div class="hc-top">
        <div class="hc-icon">${icon(glyph || key, 22)}</div>
        <span class="pill ${statusTone}"><span class="dot"></span> ${statusLabel}</span>
      </div>
      <div class="hc-title">${title}</div>
      <div class="hc-lines">
        ${lines.map(l => html`<div class="hc-line"><span>${l[0]}</span><b>${l[1]}</b></div>`)}
      </div>
      <div class="hc-go">Открыть →</div>
    </div>
  `;
}

function paint($mount) {
  const reqs = snap.requests || {};
  const reqMin = reqs.req_min ?? 0;
  const active = reqs.active ?? 0;
  const proc = snap.processes || {};
  const ts = proc.torrserver || {};
  const yt = proc.ytdlp || {};
  const transcoding = snap.transcoding || {};
  const os = snap.os || {};

  const u = deriveUsers();
  const b = deriveBalancers();
  const clu = deriveCluster();

  render(html`
    <section class="hero">
      <h1>Командный центр</h1>
      <p>${os.hostname || 'localhost'} · ${snap.app_version || '—'} · аптайм ${fmtDur(snap.uptime_sec)}. Всё важное — здесь; за деталями жми в карточку.</p>
      <div class="hero-actions">
        <span class="chip accent">${snap.num_cpu || '?'} CPU</span>
        <span class="chip">goroutines ${fmtCount(snap.goroutines)}</span>
        <span class="chip">proxylinks ${fmtCount(snap.proxylink_entries)}</span>
      </div>
    </section>

    <div class="grid cols-4">
      <l-stat accent="blue"   label="Пользователи"       value=${fmtCount(u.total)}   suffix=${u.expired ? u.expired + ' просроч.' : ''} icon="👥"></l-stat>
      <l-stat accent="violet" label="Активные устройства" value=${fmtCount(u.devices)} icon="📱"></l-stat>
      <l-stat accent="mint"   label="Балансеры вкл"       value=${b.enabled + ' / ' + b.total} icon="⚙"></l-stat>
      <l-stat accent="pink"   label="Запросов / мин"      value=${fmtCount(reqMin)}    suffix=${active ? active + ' активных' : ''} icon="↗"></l-stat>
    </div>

    <div class="section-title">Состояние систем</div>
    <div class="hc-grid">
      ${healthCard({
        glyph: 'server', title: 'Сервер', accent: 'violet', key: 'server-stats',
        statusTone: 'ok', statusLabel: 'работает',
        lines: [['Аптайм', fmtDur(snap.uptime_sec)], ['Запросы/мин', fmtCount(reqMin)], ['Goroutines', fmtCount(snap.goroutines)]],
      })}

      ${healthCard({
        glyph: 'balancers', title: 'Балансеры', accent: b.enabled > 0 ? 'mint' : 'amber', key: 'balancers',
        statusTone: b.enabled > 0 ? 'ok' : 'warn',
        statusLabel: b.enabled > 0 ? `${b.enabled} активно` : 'нет активных',
        lines: [['Включено', `${b.enabled} / ${b.total}`], ['Источники', 'управление']],
      })}

      ${clu ? healthCard({
        glyph: 'cluster', title: 'Кластер', accent: clu.enabled ? 'cyan' : 'amber', key: 'cluster',
        statusTone: clu.enabled ? (clu.healthy === clu.total ? 'ok' : 'warn') : 'muted',
        statusLabel: clu.enabled ? `${clu.healthy}/${clu.total} нод` : 'выключен',
        lines: [['Режим', clu.mode || '—'], ['Здоровых нод', `${clu.healthy} / ${clu.total}`]],
      }) : healthCard({
        glyph: 'cluster', title: 'Кластер', accent: 'muted', key: 'cluster',
        statusTone: 'muted', statusLabel: 'выключен',
        lines: [['Статус', 'не настроен']],
      })}

      ${healthCard({
        glyph: 'transcoding', title: 'TorrServer', accent: ts.alive ? 'cyan' : 'amber', key: 'server-stats',
        statusTone: ts.configured ? (ts.alive ? 'ok' : 'danger') : 'muted',
        statusLabel: ts.configured ? (ts.alive ? 'alive' : 'down') : 'не настроен',
        lines: ts.configured
          ? [['Тип', ts.inprocess ? 'in-process' : (ts.external ? 'external' : 'local')], ['Торренты', String(ts.active_torrents ?? '—')]]
          : [['Статус', 'не настроен']],
      })}

      ${healthCard({
        glyph: 'transcoding', title: 'Транскодинг', accent: 'amber', key: 'transcoding',
        statusTone: transcoding.enabled ? (transcoding.active_jobs > 0 ? 'info' : 'ok') : 'muted',
        statusLabel: transcoding.enabled ? `${transcoding.active_jobs || 0} jobs` : 'выключен',
        lines: transcoding.enabled
          ? [['Active jobs', String(transcoding.active_jobs || 0)]]
          : [['Статус', 'выключен']],
      })}

      ${healthCard({
        glyph: 'deps', title: 'yt-dlp', accent: 'pink', key: 'deps',
        statusTone: yt.available ? 'ok' : 'warn',
        statusLabel: yt.available ? 'установлен' : 'отсутствует',
        lines: yt.available
          ? [['Версия', yt.version || '—'], ['ffmpeg', yt.ffmpeg ? 'есть' : 'нет']]
          : [['Статус', 'не установлен']],
      })}
    </div>

    <div class="section-title">Быстрые действия</div>
    <div class="qa-grid">
      ${quickAction('users', 'Пользователи', 'токены, устройства, доступ', 'users')}
      ${quickAction('broadcast', 'Рассылка', 'сообщение всем в TG', 'broadcast')}
      ${quickAction('logs', 'Логи', 'живой поток событий', 'logs')}
      ${quickAction('bans', 'WAF', 'баны, гео, rate-limit', 'waf')}
      ${quickAction('promo', 'Промокоды', 'выдать доступ', 'promo')}
      ${quickAction('telemetry', 'Телеметрия', 'графики и тренды', 'telemetry')}
    </div>

    <style>
      .hero { margin-bottom: var(--s-5); }

      .hc-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
        gap: var(--s-3);
      }
      .hc {
        position: relative;
        background: var(--bg-1);
        border: 1px solid var(--border-1);
        border-radius: var(--r-3);
        padding: var(--s-4);
        cursor: pointer;
        overflow: hidden;
        isolation: isolate;
        transition: transform var(--t-normal) var(--ease-spring),
                    box-shadow var(--t-normal) var(--ease-spring),
                    border-color var(--t-fast);
      }
      .hc::before {
        content: '';
        position: absolute; top: 0; left: 0; right: 0; height: 3px;
        background: var(--g-accent);
      }
      .hc-violet::before { background: var(--g-accent); }
      .hc-mint::before   { background: var(--g-mint); }
      .hc-cyan::before   { background: var(--g-cyan); }
      .hc-amber::before  { background: var(--g-amber); }
      .hc-pink::before   { background: var(--g-fire); }
      .hc-muted::before  { background: linear-gradient(90deg, var(--border-3), transparent); }
      .hc:hover {
        transform: translateY(-2px);
        box-shadow: var(--shadow-lg);
        border-color: var(--border-2);
      }
      .hc:hover .hc-go { opacity: 1; transform: none; }
      .hc:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }

      .hc-top { display: flex; align-items: center; justify-content: space-between; gap: var(--s-2); }
      .hc-icon {
        width: 38px; height: 38px;
        border-radius: var(--r-2);
        background: var(--bg-3);
        display: grid; place-items: center;
        font-size: 18px;
        color: var(--text-1);
      }
      .hc-violet .hc-icon { color: var(--accent); }
      .hc-mint .hc-icon   { color: var(--success); }
      .hc-cyan .hc-icon   { color: var(--accent-4); }
      .hc-amber .hc-icon  { color: var(--warn); }
      .hc-pink .hc-icon   { color: var(--accent-3); }
      .hc-icon .lic { width: 22px; height: 22px; }
      .hc-title {
        font-family: var(--font-display);
        font-weight: var(--fw-semibold);
        font-size: var(--fs-md);
        letter-spacing: -0.01em;
        margin: var(--s-3) 0 var(--s-2);
      }
      .hc-lines { display: flex; flex-direction: column; gap: 4px; }
      .hc-line {
        display: flex; align-items: baseline; justify-content: space-between;
        font-size: var(--fs-sm);
        padding: 3px 0;
        border-top: 1px dashed var(--border-1);
      }
      .hc-line:first-child { border-top: 0; }
      .hc-line span { color: var(--text-2); }
      .hc-line b { color: var(--text-0); font-family: var(--font-mono); font-size: 12.5px; font-weight: var(--fw-semibold); }
      .hc-go {
        margin-top: var(--s-3);
        font-size: var(--fs-xs); color: var(--accent); font-weight: var(--fw-semibold);
        opacity: 0; transform: translateX(-4px);
        transition: opacity var(--t-fast), transform var(--t-fast) var(--ease-spring);
      }

      .qa-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
        gap: var(--s-2);
      }
      .qa {
        display: flex; align-items: center; gap: var(--s-3);
        padding: 12px 14px;
        background: var(--bg-2);
        border: 1px solid var(--border-1);
        border-radius: var(--r-2);
        cursor: pointer;
        transition: background var(--t-fast), border-color var(--t-fast), transform var(--t-fast) var(--ease-spring);
      }
      .qa:hover { background: var(--bg-3); border-color: var(--border-3); transform: translateY(-1px); }
      .qa:active { transform: translateY(0); }
      .qa-icon { flex-shrink: 0; width: 28px; height: 28px; display: grid; place-items: center; color: var(--accent); }
      .qa-icon .lic { width: 20px; height: 20px; }
      .qa-text { min-width: 0; line-height: 1.25; }
      .qa-title { font-weight: var(--fw-semibold); font-size: var(--fs-sm); }
      .qa-sub { font-size: var(--fs-xs); color: var(--text-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    </style>
  `, $mount);
}

function quickAction(glyph, title, sub, key) {
  return html`
    <div class="qa" @click=${() => nav(key)} role="button" tabindex="0"
         @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); nav(key); } }}>
      <span class="qa-icon">${icon(glyph, 20)}</span>
      <div class="qa-text">
        <div class="qa-title">${title}</div>
        <div class="qa-sub">${sub}</div>
      </div>
    </div>
  `;
}

export async function render_($mount) {
  render(html`<l-card loading title="Загрузка..."></l-card>`, $mount);
  await loadAll();
  paint($mount);
  lastTick = Date.now();

  // Live request tile via SSE — only `requests`/`processes` change minute to
  // minute, so we merge the snapshot and repaint.
  if (stream) stream.close();
  stream = api.sse('/stats/stream');
  stream.on('snapshot', (s) => {
    if (!s) return;
    lastTick = Date.now();
    snap = s;
    paint($mount);
  });
  stream.on('message', () => { lastTick = Date.now(); });

  // Periodic full refresh (users/balancers/cluster don't stream) every 30s.
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = setInterval(async () => {
    if (document.visibilityState !== 'visible') return;
    await loadAll();
    paint($mount);
  }, 30000);

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
