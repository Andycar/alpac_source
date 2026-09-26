// pages/iptv-registry.js — СВОЙ реестр IPTV-каналов («Мои каналы»).
//
// Канал реестра живёт на этом сервере: стабильный id, несколько источников
// потока (закреплённые руками + собранные из донорских плейлистов),
// автофейловер по health-check. Здесь: состав каналов, живость источников,
// закрепление своих URL, ingest/refresh, экспорт M3U.
//
// API:
//   GET  /api/iptv-registry                    → {name, enabled, total, channels[]}
//   POST /api/iptv-registry {action,…}          upsert | delete | disable | ingest | refresh

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  name: '',
  channels: [],
  filter: '',
  onlyProblem: false, // только каналы без живых источников
  selected: null,     // id канала, раскрытого в панели источников
  loading: true,
  disabled: false,    // реестр выключен в конфиге
  newPinURL: '',
  newPinQuality: '',
};

async function load() {
  state.loading = true;
  try {
    const d = await api.get('/iptv-registry');
    state.name = (d && d.name) || 'Alpac IPTV';
    state.channels = (d && d.channels) || [];
    state.disabled = false;
  } catch (e) {
    if (e.status === 503) {
      state.disabled = true;
    } else {
      toast.error('Не удалось загрузить реестр: ' + e.message);
    }
  } finally {
    state.loading = false;
  }
}

async function act(body, okMsg) {
  try {
    const r = await api.post('/iptv-registry', body);
    if (r && r.error) throw new Error(r.error);
    if (okMsg) toast.success(okMsg);
    await load();
    paint();
    return r;
  } catch (e) {
    toast.error(e.message);
  }
}

function ingest(addNew) {
  return act({ action: 'ingest', add_new: addNew },
    addNew ? 'Импорт выполнен' : 'Источники пересобраны');
}

function refreshDonors() {
  return act({ action: 'refresh' }, 'Доноры перечитываются в фоне');
}

function toggleChannel(ch) {
  return act({ action: 'disable', id: ch.id, disabled: !ch.disabled },
    ch.disabled ? 'Канал включён' : 'Канал выключен');
}

function deleteChannel(ch) {
  if (!confirm(`Удалить канал «${ch.name}» из реестра?`)) return;
  if (state.selected === ch.id) state.selected = null;
  return act({ action: 'delete', id: ch.id }, 'Канал удалён');
}

function addPinned(ch) {
  const url = state.newPinURL.trim();
  if (!url) return toast.warn('Введите URL потока');
  const pinned = (ch.pinned || []).map(stripAlive);
  pinned.push({ url, quality: state.newPinQuality || undefined });
  state.newPinURL = '';
  return act({ action: 'upsert', channel: { id: ch.id, pinned } }, 'Источник закреплён');
}

function removePinned(ch, url) {
  const pinned = (ch.pinned || []).filter(s => s.url !== url).map(stripAlive);
  return act({ action: 'upsert', channel: { id: ch.id, pinned } }, 'Источник откреплён');
}

// alive — вычисляемое поле снапшота, в upsert его не шлём
function stripAlive({ alive, ...src }) { return src; }

function filtered() {
  let rows = state.channels;
  if (state.onlyProblem) rows = rows.filter(c => !c.disabled && c.alive_sources === 0);
  const f = state.filter.trim().toLowerCase();
  if (f) {
    rows = rows.filter(c =>
      (c.name || '').toLowerCase().includes(f)
      || (c.group || '').toLowerCase().includes(f)
      || (c.tvg_id || '').toLowerCase().includes(f)
    );
  }
  return rows;
}

function aliveTone(c) {
  if (c.disabled) return 'muted';
  if (c.alive_sources === 0) return 'danger';
  if (c.alive_sources === 1) return 'warn';
  return 'ok';
}

// «заморожен» — отдельно от «мёртв»: источник отвечает, сегменты качаются, но
// окно эфира стоит, и зритель видит один и тот же кусок по кругу.
function srcState(s) {
  if (s.frozen) return { tone: 'warn', text: 'заморожен' };
  return s.alive ? { tone: 'ok', text: 'жив' } : { tone: 'danger', text: 'мёртв' };
}

function sourceRow(ch, s, pinnedRow) {
  const st = srcState(s);
  return html`
    <div class="ipr-src">
      <l-pill tone=${st.tone}>${st.text}</l-pill>
      ${s.quality ? html`<l-pill tone="info">${s.quality}</l-pill>` : ''}
      <code class="ipr-src__url" title=${s.url}>${s.url}</code>
      <span class="ipr-src__from">${pinnedRow ? 'закреплён' : (s.from ? 'донор ' + s.from : '')}</span>
      ${pinnedRow
        ? html`<l-button size="sm" variant="danger" @click=${() => removePinned(ch, s.url)}>×</l-button>`
        : ''}
    </div>`;
}

function sourcesPanel(ch) {
  return html`
    <l-card style="margin-top:var(--s-4)">
      <div slot="title">
        <span style="font-weight:600">Источники: ${ch.name}</span>
        <span style="color:var(--text-2);font-size:var(--fs-xs);margin-left:8px">${ch.id}</span>
      </div>
      <div slot="actions">
        <l-button size="sm" variant="secondary" @click=${() => { state.selected = null; paint(); }}>Закрыть</l-button>
      </div>

      ${(ch.pinned || []).length
        ? html`<div class="ipr-src-group">Закреплённые (играют первыми, импорт их не трогает)</div>
               ${(ch.pinned || []).map(s => sourceRow(ch, s, true))}`
        : ''}
      <div class="ipr-src-group">Из доноров (пересобираются автоматически)</div>
      ${(ch.auto || []).length
        ? (ch.auto || []).map(s => sourceRow(ch, s, false))
        : html`<div style="color:var(--text-3);padding:6px 0">нет — канал держится только на закреплённых</div>`}

      <div class="ipr-pin-form">
        <l-input
          placeholder="https://…/stream.m3u8 — свой источник"
          .value=${state.newPinURL}
          @input=${e => { state.newPinURL = e.detail.value; }}
          style="flex:1;min-width:240px"
        ></l-input>
        <l-select
          size="md"
          placeholder="Качество"
          .value=${state.newPinQuality}
          .options=${['', '4K', 'FHD', 'HD', 'SD'].map(q => ({ value: q, label: q || '—' }))}
          @change=${e => { state.newPinQuality = e.detail.value; }}
          style="width:110px"
        ></l-select>
        <l-button variant="primary" icon="📌" @click=${() => addPinned(ch)}>Закрепить</l-button>
      </div>
    </l-card>`;
}

function paint() {
  const $root = document.getElementById('iptv-registry-root');
  if (!$root) return;

  if (state.disabled) {
    render(html`
      <l-card title="Реестр выключен">
        <div style="line-height:1.6;color:var(--text-1)">
          Свой реестр каналов не включён. В <code>config.toml</code>:
          <pre style="background:var(--bg-3);padding:10px 14px;border-radius:8px;margin-top:8px">[iptv]
enable = true
registry = true        # свой реестр («Мои каналы»)
# registry_only = true # спрятать «Все каналы», наружу только реестр
# registry_auto_add = true # авто-добавление новых каналов доноров</pre>
          Пустой реестр засеется из <code>global_playlists</code> при старте автоматически.
        </div>
      </l-card>`, $root);
    return;
  }

  const rows = filtered();
  const enabled = state.channels.filter(c => !c.disabled);
  const aliveTotal = enabled.reduce((n, c) => n + c.alive_sources, 0);
  const srcTotal = enabled.reduce((n, c) => n + c.total_sources, 0);
  const problems = enabled.filter(c => c.alive_sources === 0).length;
  const sel = state.selected && state.channels.find(c => c.id === state.selected);

  render(html`
    <div class="page-summary">
      <l-stat accent="blue"   label="Каналов" value=${state.channels.length}></l-stat>
      <l-stat accent="green"  label="Включено" value=${enabled.length}></l-stat>
      <l-stat accent="purple" label="Источники (живые/все)" value=${aliveTotal + ' / ' + srcTotal}></l-stat>
      <l-stat accent=${problems ? 'pink' : 'green'} label="Без живых источников" value=${problems}></l-stat>
    </div>

    <l-card style="margin-top:var(--s-5)">
      <div slot="title"><span style="font-weight:600;font-size:var(--fs-md)">${state.name}</span></div>
      <div slot="actions" class="ipr-actions">
        <l-input
          size="sm" icon="🔎"
          placeholder="Канал / группа / tvg-id"
          .value=${state.filter}
          clearable
          @input=${e => { state.filter = e.detail.value; paint(); }}
          style="width:220px"
        ></l-input>
        <l-button size="sm" variant=${state.onlyProblem ? 'primary' : 'secondary'}
          @click=${() => { state.onlyProblem = !state.onlyProblem; paint(); }}>Проблемные</l-button>
        <l-button size="sm" variant="secondary" icon="↻" title="Пересобрать источники из скачанных доноров"
          @click=${() => ingest(false)}>Пересобрать</l-button>
        <l-button size="sm" variant="secondary" icon="＋" title="Добавить незнакомые каналы доноров в реестр"
          @click=${() => { if (confirm('Добавить в реестр все каналы доноров, которых ещё нет?')) ingest(true); }}>Импорт новых</l-button>
        <l-button size="sm" variant="secondary" icon="⇣" title="Перечитать донорские плейлисты с апстрима"
          @click=${refreshDonors}>Доноры</l-button>
        <l-button size="sm" variant="secondary" icon="📄"
          @click=${() => window.open('/api/iptv/export.m3u', '_blank')}>M3U</l-button>
      </div>

      ${state.loading
        ? html`<div style="padding:32px;text-align:center;color:var(--text-2)">Загрузка…</div>`
        : html`
          <l-table
            .columns=${[
              { key: 'number', label: '№', align: 'r', sortable: true },
              { key: 'name', label: 'Канал', sortable: true, cell: c => html`
                  <span style="display:inline-flex;align-items:center;gap:8px;${c.disabled ? 'opacity:.45' : ''}">
                    ${c.logo ? html`<img src=${c.logo} class="ipr-logo" loading="lazy">` : ''}
                    <span>${c.name}</span>
                  </span>` },
              { key: 'group', label: 'Группа', cell: c => c.group || html`<span style="color:var(--text-3)">—</span>` },
              { key: 'tvg_id', label: 'tvg-id', cell: c => c.tvg_id
                  ? html`<code style="font-size:var(--fs-xs);color:var(--text-2)">${c.tvg_id}</code>`
                  : html`<span style="color:var(--text-3)">—</span>` },
              { key: 'alive_sources', label: 'Источники', align: 'r', sortable: true, cell: c => html`
                  <l-pill tone=${aliveTone(c)}>${c.alive_sources} / ${c.total_sources}</l-pill>` },
              { key: '_act', label: '', align: 'r', cell: c => html`
                  <span style="display:inline-flex;gap:6px">
                    <l-button size="sm" variant="secondary" title="Источники"
                      @click=${() => { state.selected = c.id === state.selected ? null : c.id; paint(); }}>⚙</l-button>
                    <l-button size="sm" variant=${c.disabled ? 'primary' : 'secondary'} title=${c.disabled ? 'Включить' : 'Выключить'}
                      @click=${() => toggleChannel(c)}>${c.disabled ? '▶' : '⏸'}</l-button>
                    <l-button size="sm" variant="danger" title="Удалить" @click=${() => deleteChannel(c)}>×</l-button>
                  </span>` },
            ]}
            .rows=${rows}
            empty=${state.filter || state.onlyProblem
              ? 'Ничего не найдено'
              : 'Реестр пуст. «Импорт новых» соберёт каналы из донорских плейлистов.'}
          ></l-table>`}
    </l-card>

    ${sel ? sourcesPanel(sel) : ''}
  `, $root);
}

const styleId = 'l-iptv-registry-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: repeat(2, 1fr); } }
    .ipr-actions { display:flex; gap:8px; flex-wrap:wrap; align-items:center; }
    .ipr-logo { width:26px; height:18px; object-fit:contain; border-radius:3px; background:var(--bg-3); }
    .ipr-src { display:flex; align-items:center; gap:8px; padding:5px 0; border-bottom:1px solid var(--bg-3); }
    .ipr-src__url { flex:1; overflow:hidden; text-overflow:ellipsis; white-space:nowrap;
      font-family:var(--font-mono); font-size:var(--fs-xs); color:var(--text-1); }
    .ipr-src__from { flex-shrink:0; font-size:var(--fs-xs); color:var(--text-3); }
    .ipr-src-group { margin:12px 0 4px; font-size:var(--fs-xs); text-transform:uppercase;
      letter-spacing:.04em; color:var(--text-2); }
    .ipr-pin-form { display:flex; gap:8px; flex-wrap:wrap; align-items:center; margin-top:14px; }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="iptv-registry-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
