// pages/calendar.js — TV-show release calendar stats.
//
// API:
//   GET  /api/calendar              → {users, shows, popular[]}
//   POST /api/calendar/check-now    → trigger manual upstream poll

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  users: 0,
  shows: 0,
  popular: [],
  loading: true,
  checking: false,
};

async function load() {
  state.loading = true;
  try {
    const d = await api.get('/calendar');
    state.users = d && d.users || 0;
    state.shows = d && d.shows || 0;
    state.popular = (d && d.popular) || [];
  } catch (e) {
    toast.error('Не удалось загрузить календарь: ' + e.message);
  } finally {
    state.loading = false;
  }
}

async function checkNow() {
  if (!confirm('Запустить ручной poll TMDB?\nЭто может занять несколько минут.')) return;
  state.checking = true; paint();
  try {
    const r = await api.post('/calendar/check-now');
    if (r && r.error) throw new Error(r.error);
    toast.success(r && r.msg || 'Poll запущен');
    // Re-load after some time — backend polls async; status will update.
    setTimeout(() => { load().then(paint); }, 3000);
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.checking = false; paint();
  }
}

function tmdbPoster(path) {
  if (!path) return null;
  // TMDB images come from image.tmdb.org/t/p/w185.
  return 'https://image.tmdb.org/t/p/w185' + path;
}

function paint() {
  const $root = document.getElementById('cal-root');
  if (!$root) return;
  const rows = state.popular || [];
  render(html`
    <div class="page-summary">
      <l-stat accent="blue"   label="Подписчики" value=${state.users}    icon="👥"></l-stat>
      <l-stat accent="purple" label="Сериалы"    value=${state.shows}    icon="📺"></l-stat>
      <l-stat accent="green"  label="Топ-показ"  value=${rows.length}    icon="⭐"></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)" title="Управление">
      <div slot="actions" style="display:flex;gap:8px;">
        <l-button variant="secondary" icon="↻" @click=${async () => { await load(); paint(); }}>Обновить</l-button>
        <l-button variant="primary"   icon="🔍" @click=${checkNow} ?loading=${state.checking}>Проверить TMDB сейчас</l-button>
      </div>
      <p style="margin-top:var(--s-3);color:var(--text-2);font-size:var(--fs-sm)">
        Bot опрашивает TMDB по расписанию (см. <code>[calendar] update_interval</code> в config.toml).
        Кнопка <b>Проверить сейчас</b> запускает внеплановый цикл — полезно после ручных подписок.
      </p>
    </l-card>

    <l-card style="margin-top:var(--s-5)" title="Самые популярные сериалы">
      ${state.loading
        ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Загрузка…</div>`
        : rows.length === 0
          ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">Никто пока не подписан на сериалы</div>`
          : html`
            <div class="cal-grid">
              ${rows.map((s, idx) => showCard(s, idx))}
            </div>`}
    </l-card>
  `, $root);
}

function showCard(s, idx) {
  const poster = tmdbPoster(s.poster_path);
  return html`
    <div class="cal-card">
      <div class="cal-rank">#${idx + 1}</div>
      ${poster
        ? html`<img class="cal-poster" src=${poster} alt=${s.title} loading="lazy">`
        : html`<div class="cal-poster placeholder">${(s.title || '?').slice(0, 1).toUpperCase()}</div>`}
      <div class="cal-meta">
        <div class="cal-title">${s.title}</div>
        <div class="cal-subs">${s.subscribers || 0} подписчиков</div>
        <div class="cal-ids">
          ${s.tmdb_id ? html`<l-pill tone="muted">tmdb #${s.tmdb_id}</l-pill>` : ''}
          ${s.imdb_id ? html`<l-pill tone="muted">${s.imdb_id}</l-pill>` : ''}
        </div>
      </div>
    </div>
  `;
}

const styleId = 'l-cal-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: 1fr; } }
    .cal-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
      gap: var(--s-3);
    }
    .cal-card {
      position: relative;
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      overflow: hidden;
      transition: transform var(--t-normal) var(--ease-spring), border-color var(--t-fast);
    }
    .cal-card:hover { transform: translateY(-2px); border-color: var(--border-3); }
    .cal-rank {
      position: absolute; top: 8px; left: 8px;
      width: 28px; height: 28px; border-radius: 50%;
      background: linear-gradient(135deg, var(--accent), var(--accent-2));
      display: grid; place-items: center;
      color: white; font-weight: 700; font-size: var(--fs-xs);
      z-index: 2;
      box-shadow: var(--shadow-sm);
    }
    .cal-poster {
      width: 100%; aspect-ratio: 2 / 3;
      object-fit: cover;
      display: block;
      background: var(--bg-3);
    }
    .cal-poster.placeholder {
      display: grid; place-items: center;
      font-family: var(--font-display);
      font-size: 48px; font-weight: 800;
      color: var(--text-3);
      background: linear-gradient(135deg, var(--bg-2), var(--bg-3));
    }
    .cal-meta { padding: var(--s-3); }
    .cal-title {
      font-family: var(--font-display); font-weight: 600;
      font-size: var(--fs-md); line-height: 1.25;
      overflow: hidden; text-overflow: ellipsis;
      display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
      min-height: 2.5em;
    }
    .cal-subs { font-size: var(--fs-xs); color: var(--text-2); margin-top: 4px; }
    .cal-ids { display: flex; gap: 4px; flex-wrap: wrap; margin-top: 8px; }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="cal-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
