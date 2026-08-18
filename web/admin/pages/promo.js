// pages/promo.js — promo code generation + management.
//
// API:
//   GET  /api/promo                                                      → promoRow[]
//   POST /api/promo  {action: 'generate', count, days, max_uses, valid_hours}
//   POST /api/promo  {action: 'delete', code}

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  rows: [],
  loading: true,
  filter: '',
  // Generation form
  newCount: 1,
  newDays: 30,
  newMaxUses: 1,
  newValidHours: 0,
  generating: false,
  lastGenerated: null,
};

async function load() {
  state.loading = true;
  try {
    const rows = await api.get('/promo');
    state.rows = Array.isArray(rows) ? rows : (rows.codes || []);
  } catch (e) {
    toast.error('Не удалось загрузить промокоды: ' + e.message);
  } finally {
    state.loading = false;
  }
}

async function generate() {
  state.generating = true; paint();
  try {
    const r = await api.post('/promo', {
      action:      'generate',
      count:       Math.max(1, Math.min(100, state.newCount | 0)),
      days:        Math.max(1, state.newDays | 0),
      max_uses:    Math.max(1, state.newMaxUses | 0),
      valid_hours: Math.max(0, state.newValidHours | 0),
    });
    if (r && r.error) throw new Error(r.error);
    state.lastGenerated = r && r.codes;
    toast.success(`Создано ${(r && r.codes && r.codes.length) || 0} промокод(ов)`);
    await load();
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.generating = false;
    paint();
  }
}

async function removeCode(code) {
  if (!confirm(`Удалить промокод "${code}"?`)) return;
  try {
    const r = await api.post('/promo', { action: 'delete', code });
    if (r && r.error) throw new Error(r.error);
    toast.success('Удалён');
    await load();
    paint();
  } catch (e) {
    toast.error(e.message);
  }
}

function copyToClipboard(text) {
  if (navigator.clipboard) {
    navigator.clipboard.writeText(text).then(
      () => toast.success('Скопировано в буфер'),
      () => toast.error('Не удалось скопировать')
    );
  }
}

function filtered() {
  let rows = state.rows;
  const f = state.filter.trim().toLowerCase();
  if (f) rows = rows.filter(r => (r.code || '').toLowerCase().includes(f));
  return rows;
}

function paint() {
  const $root = document.getElementById('promo-root');
  if (!$root) return;
  const rows = filtered();
  const total = state.rows.length;
  const valid = state.rows.filter(r => r.valid).length;
  const used  = state.rows.reduce((a, r) => a + (r.used_count || 0), 0);
  const expired = total - valid;

  render(html`
    <div class="page-summary">
      <l-stat accent="blue"   label="Всего"        value=${total}></l-stat>
      <l-stat accent="green"  label="Активные"     value=${valid}></l-stat>
      <l-stat accent="amber"  label="Использовано" value=${used}></l-stat>
      <l-stat accent="pink"   label="Истекли"      value=${expired}></l-stat>
    </div>

    <l-card title="Сгенерировать промокоды" style="margin-top:var(--s-5)">
      <div class="pm-form">
        <div class="pm-fld">
          <label>Сколько кодов</label>
          <l-input type="number" .value=${String(state.newCount)} @input=${e => { state.newCount = parseInt(e.detail.value, 10) || 1; }}></l-input>
        </div>
        <div class="pm-fld">
          <label>Дней доступа</label>
          <l-input type="number" .value=${String(state.newDays)} @input=${e => { state.newDays = parseInt(e.detail.value, 10) || 30; }}></l-input>
        </div>
        <div class="pm-fld">
          <label>Использований</label>
          <l-input type="number" .value=${String(state.newMaxUses)} @input=${e => { state.newMaxUses = parseInt(e.detail.value, 10) || 1; }}></l-input>
        </div>
        <div class="pm-fld">
          <label>Валиден часов (0=∞)</label>
          <l-input type="number" .value=${String(state.newValidHours)} @input=${e => { state.newValidHours = parseInt(e.detail.value, 10) || 0; }}></l-input>
        </div>
        <l-button variant="primary" icon="🎟" @click=${generate} ?loading=${state.generating}>Создать</l-button>
      </div>

      ${state.lastGenerated && state.lastGenerated.length ? html`
        <div class="pm-recent">
          <div class="pm-recent-h">Только что созданы (нажмите на код чтобы скопировать)</div>
          <div class="pm-recent-list">
            ${state.lastGenerated.map(c => html`
              <button class="pm-recent-code" @click=${() => copyToClipboard(c.code || c)}>
                <code>${c.code || c}</code>
                <span>📋</span>
              </button>
            `)}
          </div>
        </div>` : ''}
    </l-card>

    <l-card style="margin-top:var(--s-5)">
      <div slot="title"><span style="font-weight:600;font-size:var(--fs-md)">Активные промокоды</span></div>
      <div slot="actions" style="display:flex;gap:8px;">
        <l-input
          size="sm" icon="🔎"
          placeholder="Найти код"
          .value=${state.filter}
          clearable
          @input=${e => { state.filter = e.detail.value; paint(); }}
          style="width:200px"
        ></l-input>
        <l-button variant="secondary" icon="↻" @click=${async () => { await load(); paint(); }}>Обновить</l-button>
      </div>

      ${state.loading
        ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Загрузка…</div>`
        : html`
          <l-table
            .columns=${[
              { key: 'code', label: 'Код', cell: r => html`<code class="pm-code-cell" @click=${() => copyToClipboard(r.code)} title="Кликни чтобы скопировать">${r.code}</code>` },
              { key: 'days', label: 'Дней', align: 'r', sortable: true, format: 'int' },
              { key: 'used_count', label: 'Исп. / Лимит', align: 'c', cell: r => usageCell(r) },
              { key: 'valid', label: 'Статус', cell: r => r.valid ? html`<l-pill tone="ok" dot>Активен</l-pill>` : html`<l-pill tone="muted">Истёк</l-pill>` },
              { key: 'expires_at', label: 'Истекает', cell: r => r.expires_at ? html`<span style="font-family:var(--font-mono);font-size:var(--fs-xs)">${r.expires_at}</span>` : html`<span style="color:var(--text-3)">∞</span>` },
              { key: 'created_at', label: 'Создан', cell: r => html`<span style="font-size:var(--fs-xs);color:var(--text-2)">${r.created_at}</span>` },
              { key: '_act', label: '', align: 'r', cell: r => html`<l-button size="sm" variant="danger" @click=${() => removeCode(r.code)}>×</l-button>` },
            ]}
            .rows=${rows}
            empty=${state.filter ? 'Ничего не найдено' : 'Нет промокодов'}
          ></l-table>`}
    </l-card>
  `, $root);
}

function usageCell(r) {
  const u = r.used_count || 0;
  const m = r.max_uses || 0;
  const exhausted = m > 0 && u >= m;
  return html`<span style="font-variant-numeric:tabular-nums;color:${exhausted ? 'var(--danger)' : 'var(--text-0)'}">${u}${m ? ' / ' + m : ''}</span>`;
}

const styleId = 'l-promo-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: repeat(2, 1fr); } }
    .pm-form { display: flex; gap: 10px; flex-wrap: wrap; align-items: flex-end; }
    .pm-fld { display: flex; flex-direction: column; gap: 4px; min-width: 130px; }
    .pm-fld label {
      font-size: 10px; text-transform: uppercase;
      color: var(--text-3); letter-spacing: 0.08em; font-weight: 600;
    }
    .pm-recent {
      margin-top: var(--s-4);
      padding: var(--s-3);
      background: var(--success-soft);
      border: 1px solid rgba(52,211,153,0.18);
      border-radius: var(--r-2);
    }
    .pm-recent-h {
      font-size: var(--fs-xs); color: var(--success);
      font-weight: 600; margin-bottom: var(--s-2);
    }
    .pm-recent-list { display: flex; gap: 6px; flex-wrap: wrap; }
    .pm-recent-code {
      display: inline-flex; align-items: center; gap: 6px;
      padding: 6px 10px;
      background: var(--bg-1); border: 1px solid var(--border-2);
      border-radius: var(--r-2);
      color: var(--text-0); cursor: pointer;
      font-family: var(--font-mono); font-size: var(--fs-sm);
      transition: background var(--t-fast), border-color var(--t-fast);
    }
    .pm-recent-code:hover { background: var(--bg-3); border-color: var(--accent); }
    .pm-recent-code code { color: var(--accent); }
    .pm-code-cell {
      cursor: pointer;
      font-family: var(--font-mono); font-size: var(--fs-sm);
      color: var(--accent);
      background: var(--bg-3); padding: 2px 8px; border-radius: 4px;
      transition: background var(--t-fast);
    }
    .pm-code-cell:hover { background: var(--accent-soft); }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="promo-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
