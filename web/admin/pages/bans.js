// pages/bans.js — banned UIDs / IPs / CIDRs / TG IDs / fingerprints / countries.
//
// API:
//   GET  /api/bans               → {rules[], stats, total}
//   POST /api/bans  action=add    {type, value, reason}
//   POST /api/bans  action=remove {id}

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

const BAN_TYPES = [
  { value: 'uid',         label: 'UID (lampac_unic_id)' },
  { value: 'ip',          label: 'IP (одиночный)' },
  { value: 'cidr',        label: 'CIDR (диапазон)' },
  { value: 'tg_id',       label: 'Telegram ID' },
  { value: 'fingerprint', label: 'Fingerprint' },
  { value: 'country',     label: 'Country (ISO-2)' },
];

let state = {
  rules: [],
  stats: {},
  filter: '',
  type: '',
  loading: true,
  newType:   'ip',
  newValue:  '',
  newReason: '',
};

async function load() {
  state.loading = true;
  try {
    const d = await api.get('/bans');
    state.rules = (d && d.rules) || [];
    state.stats = (d && d.stats) || {};
  } catch (e) {
    toast.error('Не удалось загрузить баны: ' + e.message);
  } finally {
    state.loading = false;
  }
}

async function addBan() {
  if (!state.newValue.trim()) return toast.warn('Введите значение');
  try {
    const r = await api.post('/bans', {
      action: 'add',
      type: state.newType,
      value: state.newValue.trim(),
      reason: state.newReason.trim(),
    });
    if (r && r.ok === false) throw new Error(r.error || 'add failed');
    if (r && r.error) throw new Error(r.error);
    toast.success('Бан добавлен');
    state.newValue = '';
    state.newReason = '';
    await load();
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

async function removeBan(id) {
  if (!confirm('Удалить бан?')) return;
  try {
    const r = await api.post('/bans', { action: 'remove', id });
    if (r && r.error) throw new Error(r.error);
    toast.success('Удалён');
    await load();
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

function filtered() {
  let rows = state.rules;
  if (state.type) rows = rows.filter(r => r.type === state.type);
  const f = state.filter.trim().toLowerCase();
  if (f) {
    rows = rows.filter(r =>
      (r.value || '').toLowerCase().includes(f)
      || (r.reason || '').toLowerCase().includes(f)
      || (r.id || '').toLowerCase().includes(f)
    );
  }
  return rows;
}

function typeTone(t) {
  switch (t) {
    case 'ip':          return 'info';
    case 'cidr':        return 'info';
    case 'uid':         return 'warn';
    case 'tg_id':       return 'warn';
    case 'fingerprint': return 'muted';
    case 'country':     return 'danger';
    default:            return 'muted';
  }
}

function paint() {
  const $root = document.getElementById('bans-root');
  if (!$root) return;
  const rows = filtered();
  const total = state.rules.length;

  render(html`
    <div class="page-summary">
      <l-stat accent="pink"   label="Всего банов" value=${total}></l-stat>
      <l-stat accent="blue"   label="IP / CIDR"   value=${(state.stats.ip || 0) + (state.stats.cidr || 0)}></l-stat>
      <l-stat accent="amber"  label="UID / TG"    value=${(state.stats.uid || 0) + (state.stats.tg_id || 0)}></l-stat>
      <l-stat accent="purple" label="Country"     value=${state.stats.country || 0}></l-stat>
    </div>

    <l-card title="Добавить бан" style="margin-top:var(--s-5)">
      <div class="bn-form">
        <l-select
          size="md"
          .value=${state.newType}
          .options=${BAN_TYPES}
          @change=${e => { state.newType = e.detail.value; paint(); }}
          style="width:200px"
        ></l-select>
        <l-input
          placeholder=${placeholderFor(state.newType)}
          .value=${state.newValue}
          @input=${e => { state.newValue = e.detail.value; }}
          style="flex:1;min-width:180px"
        ></l-input>
        <l-input
          placeholder="Причина (опционально)"
          .value=${state.newReason}
          @input=${e => { state.newReason = e.detail.value; }}
          style="flex:1;min-width:160px"
        ></l-input>
        <l-button variant="primary" icon="🚫" @click=${addBan}>Забанить</l-button>
      </div>
    </l-card>

    <l-card style="margin-top:var(--s-5)">
      <div slot="title"><span style="font-weight:600;font-size:var(--fs-md)">Активные баны</span></div>
      <div slot="actions" style="display:flex;gap:8px;">
        <l-select
          size="sm"
          placeholder="Все типы"
          .value=${state.type}
          .options=${BAN_TYPES}
          @change=${e => { state.type = e.detail.value; paint(); }}
          style="width:180px"
        ></l-select>
        <l-input
          size="sm" icon="🔎"
          placeholder="Значение / причина"
          .value=${state.filter}
          clearable
          @input=${e => { state.filter = e.detail.value; paint(); }}
          style="width:240px"
        ></l-input>
        <l-button variant="secondary" icon="↻" @click=${async () => { await load(); paint(); }}>Обновить</l-button>
      </div>

      ${state.loading
        ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Загрузка…</div>`
        : html`
          <l-table
            .columns=${[
              { key: 'type',  label: 'Тип', cell: r => html`<l-pill tone=${typeTone(r.type)}>${r.type}</l-pill>` },
              { key: 'value', label: 'Значение', cell: r => html`<code style="font-family:var(--font-mono);font-size:var(--fs-xs);color:var(--text-0);background:var(--bg-3);padding:2px 6px;border-radius:4px">${r.value}</code>` },
              { key: 'reason', label: 'Причина', cell: r => r.reason || html`<span style="color:var(--text-3)">—</span>` },
              { key: 'created_at', label: 'Создан', format: 'date', sortable: true },
              { key: 'created_by', label: 'Кем', align: 'r', cell: r => r.created_by ? html`<span style="font-size:var(--fs-xs);color:var(--text-2)">#${r.created_by}</span>` : html`<span style="color:var(--text-3)">—</span>` },
              { key: '_act', label: '', align: 'r', cell: r => html`<l-button size="sm" variant="danger" @click=${() => removeBan(r.id)}>×</l-button>` },
            ]}
            .rows=${rows}
            empty=${state.filter || state.type ? 'Ничего не найдено' : 'Нет активных банов'}
          ></l-table>`}
    </l-card>
  `, $root);
}

function placeholderFor(type) {
  switch (type) {
    case 'ip':          return '203.0.113.5';
    case 'cidr':        return '203.0.113.0/24';
    case 'uid':         return '8-char UID из lampac_unic_id';
    case 'tg_id':       return '123456789';
    case 'fingerprint': return 'fp hash';
    case 'country':     return 'RU, US, CN…';
    default:            return '';
  }
}

const styleId = 'l-bans-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: repeat(2, 1fr); } }
    .bn-form {
      display: flex; gap: 8px; flex-wrap: wrap; align-items: center;
    }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="bans-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
