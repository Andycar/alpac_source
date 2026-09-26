// pages/torrbalancer.js — TorrServer balancer admin.
//
// Manages a pool of backend TorrServer instances. Requests are load-balanced
// sticky-by-infohash (a torrent always streams from the backend it was added
// to). Per-group access ("premium → these servers") is configured on the
// Группы tab, not here.
//
// API: GET /api/torrbalancer → { active, settings, backends[] }
//      POST actions: add | update | delete | probe | sshrestart | settings | test
//   backend (BackendStatus): { id, name, host, login, has_auth, enabled,
//     healthy, active_conns, total_served, total_failed, torrent_count,
//     last_latency_ms, weight, last_error, uptime_pct,
//     has_ssh, ssh_host, ssh_port, ssh_user, ssh_cmd }

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  data: null,
  filter: '',
  loading: true,
  editing: null,      // copy of backend being edited (or new)
  editingNew: false,
  saving: false,
  testing: false,
  settingsDraft: null,
  settingsDirty: false,
};

async function load() {
  state.loading = true;
  try {
    state.data = await api.torrbalancer();
    state.settingsDraft = state.data.settings ? JSON.parse(JSON.stringify(state.data.settings)) : null;
    state.settingsDirty = false;
  } catch (e) {
    toast.error('Не удалось загрузить балансер: ' + e.message);
    state.data = null;
  } finally {
    state.loading = false;
  }
}

async function act(action, payload, success) {
  try {
    const r = await api.torrbalancerAction({ action, ...(payload || {}) });
    if (r && r.ok === false) throw new Error(r.error || 'failed');
    if (r && r.error) throw new Error(r.error);
    if (success) toast.success(success);
    return r;
  } catch (e) {
    toast.error(e.message);
    throw e;
  }
}

function filteredBackends() {
  const list = (state.data && state.data.backends) || [];
  const f = state.filter.trim().toLowerCase();
  if (!f) return list;
  return list.filter(b =>
    (b.name || '').toLowerCase().includes(f) || (b.host || '').toLowerCase().includes(f));
}

function paint() {
  const $root = document.getElementById('tb-root');
  if (!$root) return;

  const d = state.data || {};
  const list = filteredBackends();
  const healthy = list.filter(b => b.healthy).length;
  const active = list.reduce((a, b) => a + (b.active_conns || 0), 0);
  const torrents = list.reduce((a, b) => a + (b.torrent_count || 0), 0);

  render(html`
    <section class="hero">
      <h1>TorrServer Балансер</h1>
      <p>Пул бэкенд-серверов TorrServer. Нагрузка распределяется <b>липко по торренту</b> (один торрент всегда стримится с того же сервера). Доступ группам («премиум → эти сервера») настраивается на вкладке <b>Группы</b>.</p>
      <div class="hero-actions">
        <span class="chip ${d.active ? 'accent' : ''}">${d.active ? 'АКТИВЕН' : 'нет включённых серверов'}</span>
        ${d.active ? '' : html`<span class="chip">используется одиночный <code>[torrserver]</code> из конфига</span>`}
        <button class="btn-refresh" @click=${async () => { await load(); paint(); }}>↻ Обновить</button>
      </div>
    </section>

    <div class="grid cols-4">
      <l-stat accent="blue"   label="Всего серверов"     value=${list.length}></l-stat>
      <l-stat accent="mint"   label="Здоровых"           value=${healthy}></l-stat>
      <l-stat accent="violet" label="Активных коннектов" value=${active}></l-stat>
      <l-stat accent="pink"   label="Торрентов"          value=${torrents}></l-stat>
    </div>

    ${state.settingsDraft ? renderSettings() : ''}

    <div class="section-title">
      <span>Серверы</span>
      <span style="color:var(--text-3);font-size:var(--fs-xs);font-weight:var(--fw-medium);text-transform:none;letter-spacing:0">${list.length}</span>
    </div>
    <l-card>
      <div slot="actions" style="display:flex;gap:8px;flex-wrap:wrap">
        <l-input size="sm" icon="🔎" placeholder="Имя / хост" .value=${state.filter} clearable
          @input=${e => { state.filter = e.detail.value; paint(); }} style="width:240px"></l-input>
        <l-button variant="primary" icon="＋" @click=${openAdd}>Добавить сервер</l-button>
      </div>
      ${state.loading
        ? html`<div class="empty">Загрузка…</div>`
        : list.length === 0
          ? html`<div class="empty">${state.filter ? 'Ничего не найдено' : 'Серверов нет. Нажмите «Добавить сервер».'}</div>`
          : html`<div class="cn-grid">${list.map(backendCard)}</div>`}
    </l-card>

    ${state.editing ? renderEditor() : ''}

    <style>${pageStyles}</style>
  `, $root);
}

// ----- SETTINGS -----

function renderSettings() {
  const s = state.settingsDraft;
  return html`
    <div class="section-title">Параметры пула</div>
    <l-card accent="violet">
      <div class="settings-mini-grid">
        ${numField('Fail threshold', 'fail_threshold', 'неудач health-пробы (/echo + /torrents) до unhealthy', 1, 50)}
        ${numField('Recover threshold', 'recover_threshold', 'успехов до healthy', 1, 50)}
        ${numField('Probe interval', 'probe_interval_sec', 'сек между health-чеками', 5, 600)}
      </div>
      <div class="settings-mini-grid" style="margin-top:var(--s-3)">
        ${boolField('Debug-заголовок', 'debug_header', 'добавлять X-Lampac-TS-Backend в ответы (видно, какой сервер обслужил торрент)')}
        ${boolField('Автолечение зомби', 'auto_restart_zombie', 'бэкенд завис наполовину (/echo жив, торренты/стримы молчат) → отправить /shutdown (под systemd это авто-перезапуск), а если через 45с торренты так и мертвы и настроен SSH — перезапустить машиной. Алерт админам в TG приходит в любом случае')}
      </div>
      <div class="settings-foot">
        <button class="btn-cta" ?disabled=${!state.settingsDirty || state.saving}
                @click=${async () => {
                  state.saving = true; paint();
                  try {
                    const r = await act('settings', { settings: s }, 'Настройки сохранены');
                    if (r && r.settings) state.settingsDraft = JSON.parse(JSON.stringify(r.settings));
                    state.settingsDirty = false;
                    await load();
                  } finally { state.saving = false; }
                  paint();
                }}>${state.saving ? 'Сохраняю…' : 'Сохранить настройки'}</button>
        <button class="btn-link" ?disabled=${!state.settingsDirty}
                @click=${() => { state.settingsDraft = JSON.parse(JSON.stringify(state.data.settings)); state.settingsDirty = false; paint(); }}>
          Отменить
        </button>
      </div>
    </l-card>
  `;
}

function numField(label, key, hint, min, max) {
  const s = state.settingsDraft;
  return html`
    <div class="settings-row">
      <label class="l">${label}</label>
      <input class="settings-input" type="number" min=${min} max=${max}
             .value=${String(s[key] ?? 0)}
             @input=${e => { s[key] = parseInt(e.target.value, 10) || 0; state.settingsDirty = true; }}/>
      <span class="hint">${hint}</span>
    </div>
  `;
}

function boolField(label, key, hint) {
  const s = state.settingsDraft;
  return html`
    <label class="settings-row settings-bool">
      <div>
        <div class="l">${label}</div>
        <div class="hint">${hint}</div>
      </div>
      <label class="switch">
        <input type="checkbox" ?checked=${!!s[key]} @change=${e => { s[key] = e.target.checked; state.settingsDirty = true; paint(); }}/>
        <span class="switch-track"></span>
      </label>
    </label>
  `;
}

// ----- BACKEND CARDS + EDITOR -----

function backendCard(b) {
  const tone = b.healthy ? 'ok' : (b.enabled ? 'danger' : 'muted');
  const lbl = b.healthy ? 'Online' : (b.enabled ? 'Offline' : 'Выключен');
  return html`
    <div class="cn-card ${b.healthy ? 'ok' : 'down'} ${b.enabled ? '' : 'off'}">
      <div class="cn-head">
        <div class="cn-name">
          <span>${b.name || b.host}</span>
          ${b.has_auth ? html`<span class="cn-region" title="Basic-Auth настроен">🔒</span>` : ''}
          ${b.has_ssh ? html`<span class="cn-region" title="SSH-управление настроено (${b.ssh_user || 'root'}@${b.ssh_host || 'хост бэкенда'})">🖧</span>` : ''}
        </div>
        <l-pill tone=${tone} dot=${b.healthy || (b.enabled && !b.healthy)}>${lbl}</l-pill>
      </div>
      <div class="cn-host"><code>${b.host}</code></div>
      <div class="cn-metrics">
        <div class="cn-m"><div class="cn-m-l">Latency</div><div class="cn-m-v">${b.last_latency_ms || 0}<small>ms</small></div></div>
        <div class="cn-m"><div class="cn-m-l">Uptime</div><div class="cn-m-v">${formatPct(b.uptime_pct)}</div></div>
        <div class="cn-m"><div class="cn-m-l">Торренты</div><div class="cn-m-v">${b.torrent_count || 0}</div></div>
        <div class="cn-m"><div class="cn-m-l">Активн.</div><div class="cn-m-v">${b.active_conns || 0}</div></div>
        <div class="cn-m" title="Скорость отдачи, замеренная на реальных потоках. Пока замера нет, сервер идёт по разведочной ступени.">
          <div class="cn-m-l">Канал</div><div class="cn-m-v">${formatRate(b.rate_bytes_per_sec)}</div>
        </div>
      </div>
      ${b.last_error ? html`<div class="cn-err">⚠ ${b.last_error}</div>` : ''}
      ${b.notes ? html`<div class="cn-notes">${b.notes}</div>` : ''}
      <div class="cn-foot">
        <span class="cn-weight" title=${weightHint(b)}>вес: <b>${b.weight ?? 1}</b>${tierSuffix(b)} · served ${shortNum(b.total_served || 0)}</span>
        <div class="cn-acts">
          <l-button size="sm" variant="ghost" @click=${async () => { try { await act('probe', { id: b.id }, 'Проверено'); await load(); paint(); } catch (e) {} }}>Probe</l-button>
          ${b.has_ssh ? html`<l-button size="sm" variant="ghost" title="Перезапустить машину по SSH" @click=${async () => {
            if (!confirm(`SSH-рестарт «${b.name || b.host}»?\nКоманда: ${b.ssh_cmd || 'systemctl restart torrserver'}`)) return;
            try {
              const r = await act('sshrestart', { id: b.id }, 'SSH-рестарт выполнен');
              if (r && r.output) toast.success(r.output.slice(0, 200));
              await load(); paint();
            } catch (e) {}
          }}>SSH⟳</l-button>` : ''}
          <l-button size="sm" variant="ghost" @click=${() => openEdit(b)}>✏</l-button>
          <l-button size="sm" variant=${b.enabled ? 'ghost' : 'success'} @click=${async () => { try { await act('update', { id: b.id, patch: { enabled: !b.enabled } }, b.enabled ? 'Выключен' : 'Включён'); await load(); paint(); } catch (e) {} }}>${b.enabled ? '⏸' : '▶'}</l-button>
          <l-button size="sm" variant="danger" @click=${async () => {
            if (!confirm(`Удалить сервер "${b.name || b.host}"?`)) return;
            try { await act('delete', { id: b.id }, 'Удалён'); await load(); paint(); } catch (e) {}
          }}>×</l-button>
        </div>
      </div>
    </div>
  `;
}

function openEdit(b) {
  // Passwords are never returned by the API — leave blank (means "keep current").
  state.editing = { id: b.id, name: b.name || '', host: b.host || '', login: b.login || '', password: '', weight: b.weight || 1, notes: b.notes || '', enabled: b.enabled !== false, has_auth: b.has_auth,
    ssh_host: b.ssh_host || '', ssh_port: b.ssh_port || 0, ssh_user: b.ssh_user || '', ssh_password: '', ssh_cmd: b.ssh_cmd || '', has_ssh: b.has_ssh };
  state.editingNew = false;
  paint();
}
function openAdd() {
  state.editing = { name: '', host: '', login: '', password: '', weight: 1, notes: '', enabled: true,
    ssh_host: '', ssh_port: 0, ssh_user: '', ssh_password: '', ssh_cmd: '' };
  state.editingNew = true;
  paint();
}
function closeEditor() {
  state.editing = null;
  state.editingNew = false;
  state.saving = false;
  state.testing = false;
  paint();
}

function renderEditor() {
  const b = state.editing;
  return html`
    <div class="editor-overlay" @click=${e => { if (e.target.classList.contains('editor-overlay')) closeEditor(); }}>
      <div class="editor-panel">
        <div class="editor-head">
          <div class="editor-title">${state.editingNew ? 'Добавить сервер' : ('Сервер: ' + (b.name || b.host))}</div>
          <p>Хост, логин/пароль и вес сохраняются в database/torrbalancer/. Изменения применяются сразу — рестарт не нужен.</p>
        </div>
        <div class="editor-body">
          <div class="form-row">
            <label>Имя</label>
            <input type="text" placeholder="ts-de-1" .value=${b.name || ''} @input=${e => b.name = e.target.value}/>
          </div>
          <div class="form-row">
            <label>Хост</label>
            <input type="text" placeholder="http://1.2.3.4:8090" .value=${b.host || ''} @input=${e => b.host = e.target.value}/>
            <span class="hint">с протоколом, без trailing slash</span>
          </div>
          <div class="form-row inline">
            <div style="flex:1">
              <label>Логин (Basic-Auth)</label>
              <input type="text" placeholder="ts" .value=${b.login || ''} @input=${e => b.login = e.target.value}/>
            </div>
            <div style="flex:1">
              <label>Пароль</label>
              <input type="password" placeholder=${state.editingNew ? 'пароль' : (b.has_auth ? '•••• (не менять)' : 'без пароля')} .value=${b.password || ''} @input=${e => b.password = e.target.value}/>
            </div>
          </div>
          <div class="form-row inline">
            <div style="flex:1">
              <label>Вес</label>
              <input type="number" min="1" max="100" .value=${String(b.weight || 1)} @input=${e => b.weight = parseInt(e.target.value, 10) || 1}/>
              <span class="hint">больше вес → больше торрентов</span>
            </div>
            <div style="flex:1">
              <label class="switch" style="margin-top:22px">
                <input type="checkbox" ?checked=${b.enabled !== false} @change=${e => b.enabled = e.target.checked}/>
                <span class="switch-track"></span>
                <span>Сервер активен</span>
              </label>
            </div>
          </div>
          <div class="form-row">
            <label>Заметки (необязательно)</label>
            <textarea rows="2" placeholder="датацентр, владелец, и т.п." @input=${e => b.notes = e.target.value}>${b.notes || ''}</textarea>
          </div>

          <div class="ssh-title">SSH-управление машиной <span class="hint">— «полное управление»: кнопка SSH⟳ на карточке и автолечение, когда /shutdown не помог. Пусто = выключено.</span></div>
          <div class="form-row inline">
            <div style="flex:2">
              <label>SSH-хост</label>
              <input type="text" placeholder="пусто → хост бэкенда" .value=${b.ssh_host || ''} @input=${e => b.ssh_host = e.target.value}/>
            </div>
            <div style="flex:1">
              <label>Порт</label>
              <input type="number" min="0" max="65535" placeholder="22" .value=${b.ssh_port ? String(b.ssh_port) : ''} @input=${e => b.ssh_port = parseInt(e.target.value, 10) || 0}/>
            </div>
          </div>
          <div class="form-row inline">
            <div style="flex:1">
              <label>Пользователь</label>
              <input type="text" placeholder="root" .value=${b.ssh_user || ''} @input=${e => b.ssh_user = e.target.value}/>
            </div>
            <div style="flex:1">
              <label>SSH-пароль</label>
              <input type="password" placeholder=${state.editingNew ? 'пароль' : (b.has_ssh ? '•••• (не менять)' : 'нет — SSH выключен')} .value=${b.ssh_password || ''} @input=${e => b.ssh_password = e.target.value}/>
            </div>
          </div>
          <div class="form-row">
            <label>Команда рестарта</label>
            <input type="text" placeholder="systemctl restart torrserver" .value=${b.ssh_cmd || ''} @input=${e => b.ssh_cmd = e.target.value}/>
          </div>
        </div>
        <div class="editor-foot">
          <button class="btn-secondary" ?disabled=${state.testing} @click=${testConn}>${state.testing ? 'Проверяю…' : '⚡ Проверить связь'}</button>
          <span style="flex:1"></span>
          <button class="btn-link" @click=${closeEditor}>Отмена</button>
          <button class="btn-cta" ?disabled=${state.saving} @click=${saveBackend}>${state.saving ? 'Сохраняю…' : 'Сохранить'}</button>
        </div>
      </div>
    </div>
  `;
}

async function testConn() {
  const b = state.editing;
  if (!b.host || !b.host.trim()) { toast.error('Укажите хост'); return; }
  state.testing = true; paint();
  try {
    const r = await api.torrbalancerAction({ action: 'test', host: b.host, login: b.login, password: b.password });
    if (r && r.ok) toast.success('Связь есть: ' + (r.message || 'OK'));
    else toast.error('Нет связи: ' + ((r && r.message) || 'ошибка'));
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.testing = false; paint();
  }
}

async function saveBackend() {
  const b = state.editing;
  if (!b.host || !b.host.trim()) { toast.error('Хост обязателен'); return; }
  state.saving = true; paint();
  try {
    if (state.editingNew) {
      await act('add', { name: b.name, host: b.host, login: b.login, password: b.password, weight: b.weight, enabled: b.enabled, notes: b.notes,
        ssh_host: b.ssh_host, ssh_port: b.ssh_port, ssh_user: b.ssh_user, ssh_password: b.ssh_password, ssh_cmd: b.ssh_cmd }, 'Сервер добавлен');
    } else {
      const patch = { name: b.name, host: b.host, login: b.login, weight: b.weight, notes: b.notes, enabled: b.enabled,
        ssh_host: b.ssh_host, ssh_port: b.ssh_port, ssh_user: b.ssh_user, ssh_cmd: b.ssh_cmd };
      // Only send passwords when the admin typed a new one (blank = keep current).
      if (b.password && b.password.length > 0) patch.password = b.password;
      if (b.ssh_password && b.ssh_password.length > 0) patch.ssh_password = b.ssh_password;
      await act('update', { id: b.id, patch }, 'Сохранено');
    }
    closeEditor();
    await load();
    paint();
  } catch (e) {
    state.saving = false;
    paint();
  }
}

// ----- helpers -----

/**
 * Скорость отдачи, замеренная на живых потоках (rate_bytes_per_sec). «—» значит, что сервер ещё
 * ни разу не отдавал поток: это НЕ ноль и не приговор, просто мерить пока нечего.
 */
function formatRate(bps) {
  if (!bps || bps <= 0) return html`<span style="opacity:.55">—</span>`;
  const mb = bps / 1048576;
  return html`${mb >= 10 ? Math.round(mb) : mb.toFixed(1)}<small>МБ/с</small>`;
}

/**
 * Долю раздач решает НЕ настроенный вес сам по себе, а вес × ступень скорости. Ступень берётся из
 * замеров: нет замера — разведочная 4, <500 КБ/с — 1, <2 МБ/с — 2, <8 МБ/с — 4, выше — 8.
 * Без этой подписи оператор менял вес и не понимал, почему сильный сервер простаивает.
 */
function tierSuffix(b) {
  const tier = b.speed_tier || 0;
  if (!tier) return '';
  const eff = (b.weight ?? 1) * tier;
  const explore = !b.rate_bytes_per_sec || b.rate_bytes_per_sec <= 0;
  return html` <span style="opacity:.7">×${tier}${explore ? ' (разведка)' : ''} = <b>${eff}</b></span>`;
}

function weightHint(b) {
  const tier = b.speed_tier || 0;
  const eff = (b.weight ?? 1) * tier;
  return `Доля раздач считается как вес × ступень скорости = ${b.weight ?? 1} × ${tier} = ${eff}.` +
    (!b.rate_bytes_per_sec || b.rate_bytes_per_sec <= 0
      ? ' Замеров ещё нет — сервер идёт по разведочной ступени, первый же поток поставит его на место.'
      : ` Ступень получена из замеренных ${(b.rate_bytes_per_sec / 1048576).toFixed(1)} МБ/с.`);
}

function formatPct(p) {
  if (!Number.isFinite(p)) return '—';
  return Math.round(p) + '%';
}
function shortNum(n) {
  if (n < 1000) return String(n);
  if (n < 1e6) return (n / 1000).toFixed(1) + 'k';
  return (n / 1e6).toFixed(1) + 'M';
}

const pageStyles = `
  .hero { margin-bottom: var(--s-4); }
  .btn-refresh {
    background: var(--bg-2); color: var(--accent);
    border: 1px solid rgba(119,145,255,0.32);
    padding: 5px 12px; font-size: var(--fs-xs); border-radius: var(--r-pill);
    cursor: pointer; font-weight: var(--fw-semibold);
    transition: background 120ms, color 120ms;
  }
  .btn-refresh:hover { background: var(--accent); color: white; }
  .empty { padding: var(--s-5); text-align: center; color: var(--text-3); font-size: var(--fs-sm); }

  .settings-mini-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: var(--s-3) var(--s-4); }
  .settings-row { display: flex; flex-direction: column; gap: 6px; }
  .settings-row .l { font-size: var(--fs-sm); color: var(--text-1); font-weight: var(--fw-semibold); }
  .settings-row .hint { font-size: var(--fs-xs); color: var(--text-3); }
  .settings-input { background: var(--bg-2); border: 1px solid var(--border-2); padding: 7px 10px; border-radius: var(--r-2); color: var(--text-0); font-size: var(--fs-sm); width: 100%; }
  .settings-input:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
  .settings-bool { flex-direction: row !important; align-items: center; justify-content: space-between; padding: 10px 14px; background: var(--bg-2); border: 1px solid var(--border-1); border-radius: var(--r-2); cursor: pointer; }
  .settings-bool .l { font-weight: var(--fw-semibold); }
  .settings-bool .hint { font-size: 11px; color: var(--text-3); margin-top: 2px; }
  .settings-foot { margin-top: var(--s-4); padding-top: var(--s-3); border-top: 1px solid var(--border-1); display: flex; gap: 8px; align-items: center; }

  .switch { display: inline-flex; align-items: center; gap: 8px; cursor: pointer; }
  .switch input { display: none; }
  .switch-track { width: 40px; height: 22px; background: var(--bg-3); border-radius: var(--r-pill); position: relative; transition: background 200ms; }
  .switch-track::after { content: ''; position: absolute; top: 2px; left: 2px; width: 18px; height: 18px; border-radius: 50%; background: white; transition: transform 200ms var(--ease-spring); box-shadow: var(--shadow-sm); }
  .switch input:checked + .switch-track { background: var(--g-accent); }
  .switch input:checked + .switch-track::after { transform: translateX(18px); }

  .cn-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: var(--s-3); }
  .cn-card { padding: var(--s-3); background: var(--bg-2); border: 1px solid var(--border-1); border-radius: var(--r-3); display: flex; flex-direction: column; gap: 10px; position: relative; overflow: hidden; transition: border-color var(--t-fast), opacity var(--t-fast); }
  .cn-card::before { content: ''; position: absolute; top: 0; left: 0; right: 0; height: 2px; background: linear-gradient(90deg, var(--accent-from, var(--accent)), var(--accent-to, var(--accent-2))); }
  .cn-card.ok   { --accent-from: var(--success); --accent-to: #06b6d4; }
  .cn-card.down { --accent-from: var(--danger);  --accent-to: #f43f5e; }
  .cn-card.off  { opacity: 0.6; }
  .cn-card:hover { border-color: var(--border-3); }
  .cn-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
  .cn-name { display: flex; align-items: center; gap: 8px; font-weight: 600; font-size: var(--fs-md); }
  .cn-region { font-size: var(--fs-xs); color: var(--text-3); background: var(--bg-3); padding: 1px 8px; border-radius: var(--r-pill); }
  .cn-host { font-family: var(--font-mono); font-size: var(--fs-xs); }
  .cn-host code { color: var(--accent); background: var(--bg-3); padding: 2px 6px; border-radius: 4px; }
  .cn-metrics { display: grid; grid-template-columns: repeat(4, 1fr); gap: 6px; }
  .cn-m { background: var(--bg-3); padding: 8px; border-radius: var(--r-2); }
  .cn-m-l { font-size: 9px; text-transform: uppercase; color: var(--text-3); letter-spacing: 0.08em; }
  .cn-m-v { font-family: var(--font-display); font-weight: 700; font-size: var(--fs-md); font-variant-numeric: tabular-nums; }
  .cn-m-v small { font-size: 0.55em; color: var(--text-3); margin-left: 2px; }
  .cn-err { font-size: var(--fs-xs); font-family: var(--font-mono); color: #fca5a5; background: var(--danger-soft); padding: 6px 10px; border-radius: var(--r-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cn-notes { font-size: var(--fs-xs); color: var(--text-2); font-style: italic; padding: 4px 0; border-top: 1px dashed var(--border-1); }
  .cn-foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
  .cn-weight { font-size: var(--fs-xs); color: var(--text-3); }
  .cn-weight b { color: var(--text-1); font-family: var(--font-mono); }
  .cn-acts { display: flex; gap: 4px; }

  .btn-cta { background: var(--g-accent); color: white; border: 0; padding: 8px 18px; border-radius: var(--r-pill); font-weight: var(--fw-semibold); cursor: pointer; font-size: var(--fs-sm); box-shadow: var(--shadow-md); transition: transform 140ms var(--ease-spring), box-shadow 200ms; }
  .btn-cta:hover { transform: translateY(-1px); box-shadow: var(--shadow-glow); }
  .btn-cta:disabled { opacity: 0.5; cursor: not-allowed; transform: none; box-shadow: none; }
  .btn-secondary { background: var(--bg-2); color: var(--text-1); border: 1px solid var(--border-2); padding: 6px 14px; border-radius: var(--r-pill); font-size: var(--fs-xs); cursor: pointer; font-weight: var(--fw-semibold); }
  .btn-secondary:hover { background: var(--bg-3); border-color: var(--border-3); }
  .btn-secondary:disabled { opacity: 0.5; cursor: not-allowed; }
  .btn-link { background: transparent; color: var(--text-2); border: 0; padding: 6px 12px; font-size: var(--fs-sm); cursor: pointer; }
  .btn-link:hover { color: var(--text-0); }
  .btn-link:disabled { opacity: 0.4; cursor: not-allowed; }

  .editor-overlay { position: fixed; inset: 0; background: var(--surface-overlay); backdrop-filter: blur(16px); z-index: var(--z-modal); display: grid; place-items: center; padding: var(--s-5); animation: fade-in var(--t-normal) var(--ease-spring) both; }
  .editor-panel { width: min(560px, 100%); background: var(--bg-1); border: 1px solid var(--border-2); border-radius: var(--r-4); box-shadow: var(--shadow-xl); overflow: hidden; }
  .editor-head { padding: var(--s-5); background: radial-gradient(80% 100% at 0% 0%, rgba(119,145,255,0.16), transparent 60%); border-bottom: 1px solid var(--border-1); }
  .editor-title { margin: 0; font-family: var(--font-display); font-size: var(--fs-xl); font-weight: var(--fw-bold); letter-spacing: -0.018em; background: var(--g-text-accent); -webkit-background-clip: text; background-clip: text; color: transparent; }
  .editor-head p { margin: 4px 0 0; color: var(--text-2); font-size: var(--fs-sm); }
  .editor-body { padding: var(--s-4) var(--s-5); display: flex; flex-direction: column; gap: var(--s-3); }
  .editor-foot { padding: var(--s-3) var(--s-5); display: flex; gap: var(--s-2); align-items: center; border-top: 1px solid var(--border-1); }
  .form-row { display: flex; flex-direction: column; gap: 4px; }
  .form-row.inline { flex-direction: row; gap: var(--s-3); }
  .form-row label { font-size: var(--fs-xs); color: var(--text-2); font-weight: var(--fw-semibold); }
  .form-row input, .form-row textarea { background: var(--bg-2); border: 1px solid var(--border-2); padding: 8px 10px; border-radius: var(--r-2); color: var(--text-0); font-size: var(--fs-sm); }
  .form-row textarea { resize: vertical; min-height: 50px; font-family: var(--font-text); }
  .form-row input:focus, .form-row textarea:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
  .form-row .hint { font-size: 11px; color: var(--text-3); }
  .ssh-title { margin-top: var(--s-2); padding-top: var(--s-3); border-top: 1px dashed var(--border-2); font-size: var(--fs-sm); font-weight: var(--fw-semibold); color: var(--text-1); }
  .ssh-title .hint { font-size: 11px; color: var(--text-3); font-weight: var(--fw-regular); }
`;

export async function render_($mount) {
  $mount.innerHTML = `<div id="tb-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
