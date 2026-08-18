// pages/branding.js — public-facing brand strings.
//
// Single card with a flat form (10 fields). Save writes
// database/branding.json on the server and applies in-memory immediately —
// no restart needed. Empty fields fall back to built-in defaults shown
// next to each input as a hint.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let $mount = null;

let state = {
  current: null,    // current Branding (defaults+toml+override merged)
  defaults: null,   // built-in defaults
  override: null,   // raw override file content (or zero-value)
  hasOverride: false,
  draft: null,      // user-edited copy
  dirty: false,
  saving: false,
  loading: false,
};

const FIELDS = [
  { key: 'name',            label: 'Бренд (заменяет «Lampac»)',          group: 'Общее' },
  { key: 'version',         label: 'Версия (window.lampac_version)',      group: 'Общее' },
  { key: 'online_name_ru',  label: 'Online — название (ru)',              group: 'Имя плагина онлайна' },
  { key: 'online_name_uk',  label: 'Online — название (uk)',              group: 'Имя плагина онлайна' },
  { key: 'online_name_en',  label: 'Online — название (en)',              group: 'Имя плагина онлайна' },
  { key: 'online_name_zh',  label: 'Online — название (zh)',              group: 'Имя плагина онлайна' },
  { key: 'html_title_v2',   label: 'HTML <title> (lampa-v2last/main)',    group: 'HTML страница' },
  { key: 'html_title_lite', label: 'HTML <title> (lampa-lite)',           group: 'HTML страница' },
];

function defaultFor(key) {
  return (state.defaults && state.defaults[snake(key)]) || '';
}

// Server returns fields in PascalCase (Go json default) — Branding struct
// has no `json:"…"` tags so encoding/json uses the field name verbatim:
// Name, OnlineNameRU, HTMLTitleV2, etc. Map to the snake-case form the UI
// uses internally.
function snake(key) {
  return key;
}

// Convert server Branding (PascalCase) into UI-friendly snake_case keys.
function fromServer(b) {
  if (!b) return {};
  return {
    name:            b.Name || '',
    version:         b.Version || '',
    major:           b.Major || 0,
    minor:           b.Minor || 0,
    online_name_ru:  b.OnlineNameRU || '',
    online_name_uk:  b.OnlineNameUK || '',
    online_name_en:  b.OnlineNameEN || '',
    online_name_zh:  b.OnlineNameZH || '',
    html_title_v2:   b.HTMLTitleV2 || '',
    html_title_lite: b.HTMLTitleLite || '',
  };
}

function toServer(d) {
  return {
    Name:          d.name           || '',
    Version:       d.version        || '',
    Major:         parseInt(d.major, 10) || 0,
    Minor:         parseInt(d.minor, 10) || 0,
    OnlineNameRU:  d.online_name_ru || '',
    OnlineNameUK:  d.online_name_uk || '',
    OnlineNameEN:  d.online_name_en || '',
    OnlineNameZH:  d.online_name_zh || '',
    HTMLTitleV2:   d.html_title_v2  || '',
    HTMLTitleLite: d.html_title_lite|| '',
  };
}

async function load() {
  state.loading = true;
  paint();
  try {
    const data = await api.get('/branding');
    state.current = fromServer(data.current);
    state.defaults = fromServer(data.defaults);
    state.override = fromServer(data.override);
    state.hasOverride = !!data.has_override;
    if (!state.dirty || !state.draft) {
      // Pre-fill the draft from the override (so users see what they set
      // last time), not from current — the merged value is shown as
      // placeholder/hint.
      state.draft = { ...state.override };
      state.dirty = false;
    }
  } catch (e) {
    toast.error('Загрузка брендинга: ' + e.message);
  } finally {
    state.loading = false;
    paint();
  }
}

async function save() {
  state.saving = true; paint();
  try {
    const resp = await api.post('/branding', toServer(state.draft || {}));
    state.current = fromServer(resp.current);
    state.defaults = fromServer(resp.defaults);
    state.override = fromServer(resp.override);
    state.hasOverride = !!resp.has_override;
    state.dirty = false;
    toast.success('Брендинг применён');
  } catch (e) {
    toast.error('Сохранение: ' + e.message);
  } finally {
    state.saving = false;
    paint();
  }
}

async function clearOverride() {
  if (!confirm('Сбросить все настройки брендинга? Сервер вернётся к значениям из config.toml/built-in defaults.')) return;
  state.saving = true; paint();
  try {
    const resp = await api.post('/branding', {});
    state.current = fromServer(resp.current);
    state.defaults = fromServer(resp.defaults);
    state.override = fromServer(resp.override);
    state.hasOverride = !!resp.has_override;
    state.draft = { ...state.override };
    state.dirty = false;
    toast.success('Override снят');
  } catch (e) {
    toast.error('Reset: ' + e.message);
  } finally {
    state.saving = false;
    paint();
  }
}

function onChange(key, value) {
  state.draft = state.draft || {};
  state.draft[key] = value;
  state.dirty = true;
  paint();
}

function inputRow(field) {
  const d = state.draft || {};
  const c = state.current || {};
  const def = state.defaults || {};
  const cur = c[field.key] || '';
  const placeholder = def[field.key] || '';
  return html`
    <label class="br-row">
      <div class="br-label">
        <div class="br-l">${field.label}</div>
        <div class="br-hint">Сейчас: <code>${cur || '—'}</code>${placeholder && cur !== placeholder ? html` · default: <code>${placeholder}</code>` : ''}</div>
      </div>
      <input type="text"
             class="br-input"
             .value=${d[field.key] || ''}
             placeholder=${placeholder}
             @input=${e => onChange(field.key, e.target.value)} />
    </label>
  `;
}

function groupedFields() {
  const groups = {};
  for (const f of FIELDS) {
    groups[f.group] = groups[f.group] || [];
    groups[f.group].push(f);
  }
  return Object.entries(groups);
}

function paint() {
  if (!$mount) return;
  render(view(), $mount);
}

function view() {
  return html`
    <style>
      .br-row { display:grid; grid-template-columns: 280px 1fr; gap:16px; align-items:start; padding:10px 0; border-bottom:1px solid var(--border-1); }
      .br-row:last-child { border-bottom:none; }
      .br-label { color:var(--text-1); }
      .br-l { font-weight:500; font-size: var(--fs-sm); }
      .br-hint { font-size: 12px; color:var(--text-2); margin-top:4px; }
      .br-hint code { background:var(--bg-2); padding:1px 6px; border-radius:4px; }
      .br-input { width:100%; background:var(--bg-1); border:1px solid var(--border-1); color:var(--text-1); border-radius:6px; padding:8px 10px; font-size:14px; }
      .br-group-title { font-weight:600; color:var(--text-2); font-size: 12px; text-transform:uppercase; letter-spacing:0.05em; margin: 20px 0 4px; }
      .br-foot { display:flex; gap:12px; align-items:center; flex-wrap:wrap; padding-top:16px; margin-top:8px; border-top:1px solid var(--border-1); }
      .br-foot .muted { color:var(--text-2); font-size:12px; }
    </style>
    <l-card>
      <div slot="title" style="display:flex;align-items:center;gap:8px;">
        <span style="font-weight:600;font-size:var(--fs-md)">Брендинг</span>
        ${state.hasOverride
          ? html`<l-pill tone="ok" dot>Override активен</l-pill>`
          : html`<l-pill tone="muted">Defaults</l-pill>`}
      </div>
      <div slot="actions" style="display:flex;gap:8px;flex-wrap:wrap;">
        <l-button size="sm" variant="ghost" icon="↻" @click=${load} ?loading=${state.loading}>Обновить</l-button>
        <l-button size="sm" variant="danger" icon="♻" @click=${clearOverride} ?disabled=${!state.hasOverride}>Сбросить override</l-button>
      </div>

      <div style="color:var(--text-2);font-size:13px;margin-bottom:8px;">
        Изменения применяются на лету для следующих /online.js, /lampainit.js и Lampa <code>index.html</code>.
        Сохранённые значения переживают рестарт (файл <code>database/branding.json</code>).
        Пустое поле означает «использовать default».
      </div>

      ${groupedFields().map(([gname, fields]) => html`
        <div class="br-group-title">${gname}</div>
        ${fields.map(inputRow)}
      `)}

      <div class="br-foot">
        <l-button variant="primary" ?disabled=${!state.dirty} ?loading=${state.saving}
                  @click=${save}>
          ${state.saving ? 'Сохраняю…' : 'Сохранить'}
        </l-button>
        <l-button variant="ghost" ?disabled=${!state.dirty}
                  @click=${() => { state.draft = { ...(state.override || {}) }; state.dirty = false; paint(); }}>
          Отменить
        </l-button>
        <span class="muted">Override-файл: <code>database/branding.json</code></span>
      </div>
    </l-card>
  `;
}

export async function render_($el) {
  $mount = $el;
  paint();
  await load();
}
export { render_ as render };
