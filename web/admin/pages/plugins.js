// pages/plugins.js — toggle grid for Lampa init_plugins.
//
// API:
//   GET  /api/plugins → {initPlugins: {dlna: bool, ...}, sisi: ..., torrserver: ..., ...}
//   POST /api/plugins  { initPlugins: {...} }
//
// Only the init_plugins map is editable here — sub-system config (TorrServer
// internals, sisi advanced, transcoding) lives in the legacy panel since
// each has its own dense form. The "↗ legacy" badge surfaces that link.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

// Metadata for each init plugin — label, description, group, icon. Order
// here drives the rendering order within each group.
const PLUGIN_META = {
  // Core media path
  online:           { group: 'Источники', icon: '🎬', label: 'Online',           desc: 'Балансеры (/lite/*): kinotochka, collaps, mirage и др.' },
  sisi:             { group: 'Источники', icon: '🔞', label: 'SISI',             desc: 'Adult источники (vk, drochub, custom).' },
  catalog:          { group: 'Источники', icon: '📚', label: 'Catalog',          desc: 'Каталоги фильмов / сериалов.' },
  youtube_feed:     { group: 'Источники', icon: '▶',  label: 'YouTube feed',     desc: 'Лента YouTube в Lampa.' },
  // Player extensions
  hls_tracks:       { group: 'Плеер',     icon: '🎵', label: 'HLS tracks',       desc: 'Переключение аудио/субтитров в HLS.' },
  tracks:           { group: 'Плеер',     icon: '🎚', label: 'Tracks',           desc: 'Универсальный селектор дорожек.' },
  voice_switcher:   { group: 'Плеер',     icon: '🗣', label: 'Voice switcher',   desc: 'Быстрая смена озвучки.' },
  player_redesign:  { group: 'Плеер',     icon: '✨', label: 'Player redesign',  desc: 'Новый дизайн плеера.' },
  dualsubs:         { group: 'Плеер',     icon: '🔠', label: 'Dual subs',        desc: 'Двойные субтитры (учебные).' },
  external_player:  { group: 'Плеер',     icon: '↗',  label: 'External player',  desc: 'Открыть в VLC / MX / Vimu.' },
  web_player:       { group: 'Плеер',     icon: '🌐', label: 'Web player',       desc: 'HTML5 плеер.' },
  web_player_android:{group: 'Плеер',     icon: '🤖', label: 'Web (Android)',    desc: 'HTML5 плеер на Android клиентах.' },
  // Infra
  torrserver:       { group: 'Сервисы',   icon: '🧲', label: 'TorrServer',       desc: 'Поток торрентов через TorrServer.' },
  transcoding:      { group: 'Сервисы',   icon: '⚙',  label: 'Transcoding',      desc: 'ffmpeg для несовместимых форматов.' },
  tmdb_proxy:       { group: 'Сервисы',   icon: '🎞', label: 'TMDB proxy',       desc: 'Прокси TMDB API с кэшем.' },
  dlna:             { group: 'Сервисы',   icon: '📡', label: 'DLNA',             desc: 'UPnP/DLNA сервер для устройств в LAN.' },
  // UX
  theme:            { group: 'UI',        icon: '🎨', label: 'Theme',            desc: 'Кастомные темы Lampa.' },
  screensaver:      { group: 'UI',        icon: '💤', label: 'Screensaver',      desc: 'Экранная заставка.' },
  remote:           { group: 'UI',        icon: '🎮', label: 'Remote',           desc: 'Управление со смартфона.' },
  ads_free:         { group: 'UI',        icon: '🚫', label: 'Ads free',         desc: 'Скрыть рекламные блоки.' },
  // Storage
  bookmark:         { group: 'Данные',    icon: '🔖', label: 'Bookmarks',        desc: 'Закладки с синхронизацией.' },
  timecode:         { group: 'Данные',    icon: '⏱', label: 'Timecodes',        desc: 'Тайм-коды просмотра.' },
  sync:             { group: 'Данные',    icon: '🔄', label: 'Sync',             desc: 'Синхронизация между устройствами.' },
  backup:           { group: 'Данные',    icon: '💾', label: 'Backup',           desc: 'Бэкап настроек / закладок.' },
  migrate:          { group: 'Данные',    icon: '📥', label: 'Migrate',          desc: 'Миграция со старого Lampa.' },
  // Misc
  stats:            { group: 'Прочее',    icon: '📊', label: 'Stats',            desc: 'Сбор анонимной статистики просмотров.' },
  opensubs:         { group: 'Прочее',    icon: '💬', label: 'OpenSubs',         desc: 'OpenSubtitles интеграция.' },
  failover:         { group: 'Прочее',    icon: '🔁', label: 'Failover',         desc: 'Авто-переключение между балансерами.' },
};

const GROUP_ORDER = ['Источники', 'Плеер', 'Сервисы', 'Данные', 'UI', 'Прочее'];

let state = {
  initPlugins: {},
  loading: true,
  saving: false,
  dirty: false,
  filter: '',
};

let pristine = {}; // initPlugins snapshot from last successful load/save

async function load() {
  state.loading = true;
  try {
    const d = await api.get('/plugins');
    state.initPlugins = { ...(d.initPlugins || {}) };
    pristine = { ...state.initPlugins };
    state.dirty = false;
  } catch (e) {
    toast.error('Не удалось загрузить плагины: ' + e.message);
  } finally {
    state.loading = false;
  }
}

async function save() {
  state.saving = true; paint();
  try {
    const r = await api.post('/plugins', { initPlugins: state.initPlugins });
    if (r && r.error) throw new Error(r.error);
    pristine = { ...state.initPlugins };
    state.dirty = false;
    toast.success('Сохранено');
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.saving = false;
    paint();
  }
}

function reset() {
  state.initPlugins = { ...pristine };
  state.dirty = false;
  paint();
}

function toggle(name) {
  state.initPlugins[name] = !state.initPlugins[name];
  state.dirty = JSON.stringify(state.initPlugins) !== JSON.stringify(pristine);
  paint();
}

function grouped() {
  const buckets = {};
  for (const [name, value] of Object.entries(state.initPlugins)) {
    const meta = PLUGIN_META[name] || { group: 'Прочее', label: name, icon: '◆', desc: '' };
    if (!buckets[meta.group]) buckets[meta.group] = [];
    buckets[meta.group].push({ name, value, ...meta });
  }
  return buckets;
}

function filteredGrouped() {
  const f = state.filter.trim().toLowerCase();
  const all = grouped();
  if (!f) return all;
  const out = {};
  for (const [g, items] of Object.entries(all)) {
    const matched = items.filter(it =>
      it.name.toLowerCase().includes(f) ||
      it.label.toLowerCase().includes(f) ||
      (it.desc || '').toLowerCase().includes(f)
    );
    if (matched.length) out[g] = matched;
  }
  return out;
}

function paint() {
  const $root = document.getElementById('plugins-root');
  if (!$root) return;
  const all = state.initPlugins;
  const total = Object.keys(all).length;
  const on = Object.values(all).filter(Boolean).length;
  const off = total - on;
  const buckets = filteredGrouped();
  const groups = GROUP_ORDER.filter(g => buckets[g] && buckets[g].length);

  render(html`
    <div class="page-summary">
      <l-stat accent="blue"  label="Всего"     value=${total}></l-stat>
      <l-stat accent="green" label="Включены"  value=${on}></l-stat>
      <l-stat accent="pink"  label="Выключены" value=${off}></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)">
      <div slot="title">
        <span style="font-weight:600;font-size:var(--fs-md)">Init plugins</span>
        ${state.dirty ? html`<l-pill tone="warn">несохранено</l-pill>` : html`<l-pill tone="muted">синхр.</l-pill>`}
      </div>
      <div slot="actions" style="display:flex;gap:8px;align-items:center;">
        <l-input
          size="sm" icon="🔎"
          placeholder="Найти плагин"
          .value=${state.filter}
          clearable
          @input=${e => { state.filter = e.detail.value; paint(); }}
          style="width:240px"
        ></l-input>
        <l-button variant="ghost"  icon="↺" @click=${reset}   ?disabled=${!state.dirty}>Сброс</l-button>
        <l-button variant="primary" icon="💾" @click=${save}    ?loading=${state.saving} ?disabled=${!state.dirty || state.saving}>Сохранить</l-button>
      </div>
      ${state.loading
        ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Загрузка…</div>`
        : groups.length === 0
          ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">Ничего не найдено</div>`
          : groups.map(g => html`
            <div class="plg-grp">
              <div class="plg-grp-h">${g} <span class="plg-grp-c">${buckets[g].length}</span></div>
              <div class="plg-grid">
                ${buckets[g].map(p => pluginCard(p))}
              </div>
            </div>
          `)}
    </l-card>
  `, $root);
}

function pluginCard(p) {
  return html`
    <label class="plg-card ${p.value ? '' : 'off'}">
      <div class="plg-l">
        <div class="plg-icon">${p.icon}</div>
        <div class="plg-meta">
          <div class="plg-label">${p.label}</div>
          <div class="plg-desc">${p.desc || ''}</div>
        </div>
      </div>
      <span class="toggle">
        <input type="checkbox" ?checked=${p.value} @change=${() => toggle(p.name)}>
        <span class="track"><span class="thumb"></span></span>
      </span>
    </label>
  `;
}

// Page-scoped styles.
const styleId = 'l-plugins-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: 1fr; } }

    .plg-grp { margin-top: var(--s-4); }
    .plg-grp-h {
      font-family: var(--font-display); font-size: var(--fs-md); font-weight: 600;
      margin-bottom: var(--s-3); color: var(--text-1);
      display: flex; align-items: center; gap: 8px;
    }
    .plg-grp-c {
      font-size: var(--fs-xs); font-weight: 600; color: var(--text-3);
      background: var(--bg-3); padding: 1px 8px; border-radius: var(--r-pill);
    }
    .plg-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
      gap: var(--s-3);
    }
    .plg-card {
      display: flex; align-items: center; justify-content: space-between;
      gap: var(--s-3); padding: var(--s-3);
      background: var(--bg-2);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      cursor: pointer;
      transition: border-color var(--t-fast), background var(--t-fast), opacity var(--t-fast);
    }
    .plg-card:hover { border-color: var(--border-3); background: var(--bg-3); }
    .plg-card.off { opacity: 0.55; }
    .plg-l { display: flex; align-items: center; gap: 10px; min-width: 0; }
    .plg-icon {
      width: 32px; height: 32px; border-radius: var(--r-2);
      background: linear-gradient(135deg, rgba(108,140,255,0.18), rgba(141,107,255,0.10));
      display: grid; place-items: center; font-size: 16px; flex-shrink: 0;
    }
    .plg-meta { min-width: 0; }
    .plg-label { font-weight: 600; font-size: var(--fs-sm); line-height: 1.2; }
    .plg-desc {
      font-size: var(--fs-xs); color: var(--text-3); line-height: 1.4;
      margin-top: 2px;
      overflow: hidden; text-overflow: ellipsis;
      display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
    }
    /* Toggle (re-uses balancer page styling pattern). */
    .toggle { display: inline-block; cursor: pointer; flex-shrink: 0; }
    .toggle input { display: none; }
    .toggle .track {
      display: inline-block; width: 40px; height: 22px;
      background: var(--bg-4); border-radius: var(--r-pill);
      position: relative; transition: background var(--t-fast) var(--ease-out);
    }
    .toggle .thumb {
      position: absolute; top: 2px; left: 2px;
      width: 18px; height: 18px; border-radius: 50%;
      background: white; box-shadow: 0 1px 3px rgba(0,0,0,0.35);
      transition: transform var(--t-fast) var(--ease-spring);
    }
    .toggle input:checked + .track {
      background: linear-gradient(135deg, var(--success), #06b6d4);
    }
    .toggle input:checked + .track .thumb { transform: translateX(18px); }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="plugins-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
