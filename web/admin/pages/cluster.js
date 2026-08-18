// pages/cluster.js — full cluster admin.
//
// Sections:
//   1. Banner (mode, secrets status, reload)
//   2. Summary tiles (nodes / healthy / active / served/failed)
//   3. Settings: strategy + latency_weight, thresholds, probe interval,
//      max_retries, advertise name/region, public list toggles, force_node
//   4. Routing rules: per-balancer → target (node / local / node-id) with comment
//   5. Nodes grid with edit modal (weight/host/name/region/notes), probe,
//      toggle, delete + "+ Add node" with auto-generated secrets snippet
//   6. Secrets panel: api_key, shared_secret, copy snippet, regenerate
//   7. Local node (this server) stats
//
// API: GET /api/cluster → {enabled, mode, settings, nodes[], local, recent_events,
//                          api_key_set, shared_secret_set}
//      POST actions: add | update | delete | probe | settings | test |
//                    ensure-secrets | regenerate-secrets

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

const STRATEGIES = [
  { key: 'hybrid',       label: 'Hybrid',      hint: 'Латентность × соединения (баланс)' },
  { key: 'least-conns',  label: 'Least Conns', hint: 'Меньше всего активных запросов' },
  { key: 'latency',      label: 'Latency',     hint: 'Чистая лучшая латентность' },
];

const TARGET_OPTIONS = [
  { key: 'node',    label: 'Любая нода',    hint: 'Только удалённые ноды' },
  { key: 'local',   label: 'Этот сервер',   hint: 'Только локально, минуя ноды' },
  { key: 'node-id', label: 'Конкретная нода' },
];

let state = {
  data: null,
  filter: '',
  loading: true,
  // edit modal
  editingNode: null,        // copy of node being edited
  editingNew: false,
  saving: false,
  // settings draft
  settingsDirty: false,
  settingsDraft: null,
  // routing rules draft
  rules: [],
  rulesDirty: false,
  // secrets reveal
  secrets: null,           // {api_key, shared_secret, copy_to_node_config}
  showSecrets: false,
};

async function load() {
  state.loading = true;
  try {
    state.data = await api.get('/cluster');
    state.settingsDraft = state.data.settings ? JSON.parse(JSON.stringify(state.data.settings)) : null;
    state.rules = (state.data.settings && state.data.settings.rules)
      ? JSON.parse(JSON.stringify(state.data.settings.rules))
      : [];
    state.settingsDirty = false;
    state.rulesDirty = false;
  } catch (e) {
    toast.error('Не удалось загрузить кластер: ' + e.message);
    state.data = null;
  } finally {
    state.loading = false;
  }
}

async function act(action, payload, success) {
  try {
    const r = await api.post('/cluster', { action, ...(payload || {}) });
    if (r && r.ok === false) throw new Error(r.error || 'failed');
    if (r && r.error) throw new Error(r.error);
    if (success) toast.success(success);
    return r;
  } catch (e) {
    toast.error(e.message);
    throw e;
  }
}

function filteredNodes() {
  const nodes = (state.data && state.data.nodes) || [];
  const f = state.filter.trim().toLowerCase();
  if (!f) return nodes;
  return nodes.filter(n =>
    (n.name || '').toLowerCase().includes(f)
    || (n.host || '').toLowerCase().includes(f)
    || (n.region || '').toLowerCase().includes(f)
  );
}

// ----- PAINT -----

function paint() {
  const $root = document.getElementById('cluster-root');
  if (!$root) return;

  const d = state.data || {};
  const nodes = filteredNodes();
  const totalServed = nodes.reduce((a, n) => a + (n.total_served || 0), 0);
  const totalFailed = nodes.reduce((a, n) => a + (n.total_failed || 0), 0);
  const activeConns = nodes.reduce((a, n) => a + (n.active_conns || 0), 0);
  const healthy    = nodes.filter(n => n.healthy).length;
  const writable   = d.enabled && d.mode === 'primary';

  render(html`
    <section class="hero">
      <h1>Кластер</h1>
      <p>${d.enabled
        ? html`Узлов: ${nodes.length}, здоровых ${healthy}. Режим <b>${d.mode || '—'}</b>, стратегия <b>${d.settings && d.settings.strategy}</b>.`
        : html`Кластер выключен. Включи <code>[cluster] enable = true</code> и <code>mode = "primary"</code> в config.toml, чтобы управлять нодами отсюда.`}
      </p>
      <div class="hero-actions">
        <span class="chip ${d.enabled ? 'accent' : ''}">${d.enabled ? 'ENABLED' : 'DISABLED'}</span>
        <span class="chip">${d.mode || 'no-mode'}</span>
        ${d.api_key_set ? html`<span class="chip">API key</span>` : html`<span class="chip" style="color:var(--warn)">no API key</span>`}
        ${d.shared_secret_set ? html`<span class="chip">shared secret</span>` : html`<span class="chip" style="color:var(--warn)">no secret</span>`}
        <button class="btn-refresh" @click=${async () => { await load(); paint(); }}>↻ Обновить</button>
      </div>
    </section>

    <div class="grid cols-4">
      <l-stat accent="blue"   label="Всего узлов"        value=${nodes.length}></l-stat>
      <l-stat accent="mint"   label="Здоровых"           value=${healthy}></l-stat>
      <l-stat accent="violet" label="Активных коннектов" value=${activeConns}></l-stat>
      <l-stat accent="pink"   label="Served / Failed"    value=${`${totalServed.toLocaleString()} / ${totalFailed.toLocaleString()}`}></l-stat>
    </div>

    ${writable && state.settingsDraft ? renderSettings($root) : ''}
    ${writable ? renderRules($root) : ''}

    <div class="section-title">
      <span>Узлы</span>
      <span style="color:var(--text-3);font-size:var(--fs-xs);font-weight:var(--fw-medium);text-transform:none;letter-spacing:0">${nodes.length}</span>
    </div>
    <l-card>
      <div slot="actions" style="display:flex;gap:8px;flex-wrap:wrap">
        <l-input size="sm" icon="🔎"
          placeholder="Имя / хост / регион"
          .value=${state.filter}
          clearable
          @input=${e => { state.filter = e.detail.value; paint(); }}
          style="width:240px"></l-input>
        ${writable ? html`<l-button variant="primary" icon="＋" @click=${openAddNode}>Добавить узел</l-button>` : ''}
      </div>
      ${state.loading
        ? html`<div class="empty">Загрузка…</div>`
        : nodes.length === 0
          ? html`<div class="empty">${state.filter ? 'Ничего не найдено' : (writable ? 'Нет узлов. Нажмите «Добавить узел».' : 'Узлов нет.')}</div>`
          : html`<div class="cn-grid">${nodes.map(n => nodeCard(n, writable))}</div>`}
    </l-card>

    ${d.local ? renderLocal(d.local) : ''}
    ${writable ? renderSecrets(d) : ''}
    ${state.editingNode ? renderNodeEditor(d) : ''}

    <style>${pageStyles}</style>
  `, $root);
}

// ----- SETTINGS -----

function renderSettings($root) {
  const s = state.settingsDraft;
  return html`
    <div class="section-title">Стратегия и параметры</div>
    <l-card accent="violet">
      <div class="settings-grid">
        <div class="settings-row">
          <label class="l">Стратегия выбора ноды</label>
          <div class="strategy-tabs">
            ${STRATEGIES.map(opt => html`
              <button class=${'strat-tab ' + (s.strategy === opt.key ? 'active' : '')}
                      @click=${() => { s.strategy = opt.key; state.settingsDirty = true; paint(); }}>
                <div class="strat-name">${opt.label}</div>
                <div class="strat-hint">${opt.hint}</div>
              </button>
            `)}
          </div>
        </div>

        <div class="settings-row" style=${s.strategy === 'hybrid' ? '' : 'opacity:0.5;pointer-events:none'}>
          <label class="l">
            Вес латентности (latency_weight)
            <span class="lv">${(s.latency_weight ?? 0.4).toFixed(2)}</span>
          </label>
          <input type="range" min="0" max="1" step="0.05"
                 .value=${String(s.latency_weight ?? 0.4)}
                 @input=${e => { s.latency_weight = parseFloat(e.target.value); state.settingsDirty = true; paint(); }}/>
          <div class="range-ticks">
            <span>0 — least-conns</span>
            <span>0.5 — баланс</span>
            <span>1 — pure latency</span>
          </div>
        </div>

        <div class="settings-mini-grid">
          ${numField('Fail threshold', 'fail_threshold', 'неудач до unhealthy', 1, 50)}
          ${numField('Recover threshold', 'recover_threshold', 'успехов до healthy', 1, 50)}
          ${numField('Probe interval', 'probe_interval_sec', 'сек между health-чеками', 5, 600)}
          ${numField('Max retries', 'max_retries', '5xx forward attempts', 0, 10)}
        </div>

        <div class="settings-mini-grid">
          <div class="settings-row">
            <label class="l">Имя этого сервера</label>
            <input class="settings-input" type="text"
                   .value=${s.advertise_name || ''}
                   placeholder="primary, edge-de, ..."
                   @input=${e => { s.advertise_name = e.target.value; state.settingsDirty = true; }}/>
            <span class="hint">в X-Lampac-Server header</span>
          </div>
          <div class="settings-row">
            <label class="l">Регион этого сервера</label>
            <input class="settings-input" type="text"
                   .value=${s.advertise_region || ''}
                   placeholder="DE, US, UA, RU"
                   @input=${e => { s.advertise_region = e.target.value; state.settingsDirty = true; }}/>
            <span class="hint">для /api/servers/info</span>
          </div>
        </div>

        <div class="settings-mini-grid">
          ${boolField('Публичный список нод',  'expose_public_list', '/api/servers/list возвращает name+region')}
          ${boolField('Публичные хосты',       'public_hosts',       'выдавать хосты нод в публичном списке (для widget direct-ping)')}
          ${boolField('Принудительно ноды',    'force_node',         'тестовый режим — всегда форвардить на ноду, минуя primary')}
        </div>
      </div>
      <div class="settings-foot">
        <button class="btn-cta" ?disabled=${!state.settingsDirty}
                @click=${async () => {
                  state.saving = true;
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
          Отменить изменения
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

// ----- ROUTING RULES -----

function renderRules($root) {
  const nodes = (state.data && state.data.nodes) || [];
  // Build a flat list of balancer names from local + remote knowledge —
  // we don't have an API to enumerate them here, so just accept arbitrary text.
  return html`
    <div class="section-title">
      <span>Правила маршрутизации</span>
      <span style="color:var(--text-3);font-size:var(--fs-xs);font-weight:var(--fw-medium);text-transform:none;letter-spacing:0">${state.rules.length}</span>
    </div>
    <l-card accent="cyan" subtitle="перекрывают стратегию для конкретных балансеров">
      ${state.rules.length === 0
        ? html`<div class="empty">Правил нет — все балансеры подчиняются стратегии выше.</div>`
        : html`
          <div class="rules-list">
            ${state.rules.map((r, idx) => html`
              <div class="rule-row">
                <input class="rule-bal" placeholder="kinotochka" .value=${r.balancer || ''}
                       @input=${e => { r.balancer = e.target.value; state.rulesDirty = true; }}/>
                <span class="rule-arrow">→</span>
                <select class="rule-target" @change=${e => { r.target = e.target.value; if (r.target !== 'node-id') r.node_id = ''; state.rulesDirty = true; paint(); }}>
                  ${TARGET_OPTIONS.map(t => html`<option value=${t.key} ?selected=${r.target === t.key}>${t.label}</option>`)}
                </select>
                <select class="rule-node" ?disabled=${r.target !== 'node-id'}
                        @change=${e => { r.node_id = e.target.value; state.rulesDirty = true; }}>
                  <option value="">— нода —</option>
                  ${nodes.map(n => html`<option value=${n.id} ?selected=${r.node_id === n.id}>${n.name || n.host}</option>`)}
                </select>
                <input class="rule-comment" placeholder="комментарий…" .value=${r.comment || ''}
                       @input=${e => { r.comment = e.target.value; state.rulesDirty = true; }}/>
                <button class="rule-del" title="Удалить" @click=${() => { state.rules.splice(idx, 1); state.rulesDirty = true; paint(); }}>✕</button>
              </div>
            `)}
          </div>`}
      <div class="rules-foot">
        <button class="btn-secondary" @click=${() => { state.rules.push({ balancer: '', target: 'node', node_id: '', comment: '' }); state.rulesDirty = true; paint(); }}>
          + Добавить правило
        </button>
        ${state.rulesDirty ? html`
          <button class="btn-cta" @click=${saveRules}>Сохранить правила</button>
          <button class="btn-link" @click=${() => { state.rules = state.data.settings && state.data.settings.rules ? JSON.parse(JSON.stringify(state.data.settings.rules)) : []; state.rulesDirty = false; paint(); }}>Отменить</button>
        ` : ''}
      </div>
    </l-card>
  `;
}

async function saveRules() {
  // Strip empty rules + invalid combos before sending.
  const cleaned = state.rules
    .map(r => ({ ...r, balancer: (r.balancer || '').trim() }))
    .filter(r => r.balancer);
  const s = { ...state.settingsDraft, rules: cleaned };
  try {
    await act('settings', { settings: s }, 'Правила сохранены');
    await load();
    paint();
  } catch (e) {}
}

// ----- NODE CARDS + EDITOR -----

function nodeCard(n, writable) {
  const healthyTone = n.healthy ? 'ok' : (n.enabled ? 'danger' : 'muted');
  const healthyLbl  = n.healthy ? 'Online' : (n.enabled ? 'Offline' : 'Disabled');
  return html`
    <div class="cn-card ${n.healthy ? 'ok' : 'down'} ${n.enabled ? '' : 'off'}">
      <div class="cn-head">
        <div class="cn-name">
          <span>${n.name || n.host}</span>
          ${n.region ? html`<span class="cn-region">${n.region}</span>` : ''}
        </div>
        <l-pill tone=${healthyTone} dot=${n.healthy || (n.enabled && !n.healthy)}>${healthyLbl}</l-pill>
      </div>
      <div class="cn-host"><code>${n.host}</code></div>
      <div class="cn-metrics">
        <div class="cn-m"><div class="cn-m-l">Latency</div><div class="cn-m-v">${n.avg_latency_ms || n.last_latency_ms || 0}<small>ms</small></div></div>
        <div class="cn-m"><div class="cn-m-l">Uptime</div><div class="cn-m-v">${formatPct(n.uptime_pct)}</div></div>
        <div class="cn-m"><div class="cn-m-l">Активн.</div><div class="cn-m-v">${n.active_conns || 0}</div></div>
        <div class="cn-m"><div class="cn-m-l">Served</div><div class="cn-m-v">${shortNum(n.total_served || 0)}</div></div>
      </div>
      ${n.last_error ? html`<div class="cn-err">⚠ ${n.last_error}</div>` : ''}
      ${n.notes ? html`<div class="cn-notes">${n.notes}</div>` : ''}
      <div class="cn-foot">
        <span class="cn-weight">weight: <b>${n.weight ?? 1}</b></span>
        ${writable ? html`
          <div class="cn-acts">
            <l-button size="sm" variant="ghost" @click=${async () => { try { await act('probe', { id: n.id }, 'Probe done'); await load(); paint(); } catch(e){} }}>Probe</l-button>
            <l-button size="sm" variant="ghost" @click=${() => openEditNode(n)}>✏</l-button>
            <l-button size="sm" variant=${n.enabled ? 'ghost' : 'success'} @click=${async () => { try { await act('update', { id: n.id, patch: { enabled: !n.enabled } }, n.enabled ? 'Выключен' : 'Включён'); await load(); paint(); } catch(e){} }}>${n.enabled ? '⏸' : '▶'}</l-button>
            <l-button size="sm" variant="danger" @click=${async () => {
              if (!confirm(`Удалить узел "${n.name || n.host}"?`)) return;
              try { await act('delete', { id: n.id }, 'Удалён'); await load(); paint(); } catch(e){}
            }}>×</l-button>
          </div>
        ` : ''}
      </div>
    </div>
  `;
}

function openEditNode(n) {
  state.editingNode = JSON.parse(JSON.stringify(n));
  state.editingNew = false;
  paint();
}
function openAddNode() {
  state.editingNode = { name: '', host: '', weight: 10, region: '', notes: '', enabled: true };
  state.editingNew = true;
  paint();
}
function closeEditor() {
  state.editingNode = null;
  state.editingNew = false;
  state.saving = false;
  paint();
}

function renderNodeEditor(d) {
  const n = state.editingNode;
  return html`
    <div class="editor-overlay" @click=${e => { if (e.target.classList.contains('editor-overlay')) closeEditor(); }}>
      <div class="editor-panel">
        <div class="editor-head">
          <div class="editor-title">${state.editingNew ? 'Добавить узел' : ('Узел: ' + (n.name || n.host))}</div>
          <p>Имя, хост, вес и регион сохраняются в database/cluster/. Изменения применяются ноде сразу — рестарт не нужен.</p>
        </div>
        <div class="editor-body">
          <div class="form-row">
            <label>Имя</label>
            <input type="text" placeholder="edge-de" .value=${n.name || ''} @input=${e => n.name = e.target.value}/>
          </div>
          <div class="form-row">
            <label>Хост</label>
            <input type="text" placeholder="https://example.com" .value=${n.host || ''} @input=${e => n.host = e.target.value}/>
            <span class="hint">с протоколом, без trailing slash</span>
          </div>
          <div class="form-row inline">
            <div style="flex:1">
              <label>Вес</label>
              <input type="number" min="1" max="100" .value=${String(n.weight || 1)} @input=${e => n.weight = parseInt(e.target.value, 10) || 1}/>
            </div>
            <div style="flex:1">
              <label>Регион</label>
              <input type="text" placeholder="DE, RU, US" .value=${n.region || ''} @input=${e => n.region = e.target.value}/>
            </div>
          </div>
          <div class="form-row">
            <label>Заметки (необязательно)</label>
            <textarea rows="2" placeholder="расшифровка ноды, контакты владельца, и т.п."
                      @input=${e => n.notes = e.target.value}>${n.notes || ''}</textarea>
          </div>
          ${!state.editingNew ? html`
            <div class="form-row inline">
              <label class="switch">
                <input type="checkbox" ?checked=${n.enabled !== false} @change=${e => n.enabled = e.target.checked}/>
                <span class="switch-track"></span>
                <span>Узел активен</span>
              </label>
            </div>
          ` : ''}
        </div>
        <div class="editor-foot">
          <button class="btn-link" @click=${closeEditor}>Отмена</button>
          <button class="btn-cta" ?disabled=${state.saving} @click=${saveNode}>${state.saving ? 'Сохраняю…' : 'Сохранить'}</button>
        </div>
      </div>
    </div>
  `;
}

async function saveNode() {
  const n = state.editingNode;
  if (!n.host || !n.host.trim()) { toast.error('Хост обязателен'); return; }
  state.saving = true; paint();
  try {
    let resp;
    if (state.editingNew) {
      resp = await act('add', { name: n.name, host: n.host, weight: n.weight, enabled: n.enabled, region: n.region, notes: n.notes }, 'Узел добавлен');
      if (resp && resp.secrets_generated) {
        state.secrets = {
          api_key: resp.secrets_generated.api_key,
          shared_secret: resp.secrets_generated.shared_secret,
          copy_to_node_config: resp.secrets_generated.copy_to_node_config,
        };
        state.showSecrets = true;
      }
    } else {
      await act('update', {
        id: n.id,
        patch: { name: n.name, host: n.host, weight: n.weight, region: n.region, notes: n.notes, enabled: n.enabled }
      }, 'Сохранено');
    }
    closeEditor();
    await load();
    paint();
  } catch (e) {
    state.saving = false;
    paint();
  }
}

// ----- LOCAL -----

function renderLocal(local) {
  return html`
    <div class="section-title">Локальный узел (этот сервер)</div>
    <l-card accent="mint">
      <div class="cn-local">
        <div><span>Стратегия</span><b>${local.strategy || '—'}</b></div>
        <div><span>Активных</span><b>${local.active_conns || 0}</b></div>
        <div><span>Served</span><b>${(local.total_served || 0).toLocaleString()}</b></div>
        <div><span>Failed</span><b style="color:var(--danger)">${(local.total_failed || 0).toLocaleString()}</b></div>
        <div><span>Unique /5m</span><b>${local.unique_clients_5m || 0}</b></div>
      </div>
    </l-card>
  `;
}

// ----- SECRETS -----

function renderSecrets(d) {
  return html`
    <div class="section-title">Секреты</div>
    <l-card accent="amber" subtitle="api_key и shared_secret должны совпадать на primary и каждой ноде">
      <div class="secrets-row">
        ${d.api_key_set
          ? html`<span class="pill ok"><span class="dot"></span> api_key установлен</span>`
          : html`<span class="pill danger"><span class="dot"></span> api_key пустой</span>`}
        ${d.shared_secret_set
          ? html`<span class="pill ok"><span class="dot"></span> shared_secret установлен</span>`
          : html`<span class="pill danger"><span class="dot"></span> shared_secret пустой</span>`}
        <button class="btn-secondary" @click=${async () => {
          try {
            const r = await act('ensure-secrets', {}, '');
            if (r) {
              state.secrets = { api_key: r.api_key, shared_secret: r.shared_secret, copy_to_node_config: r.copy_to_node_config };
              state.showSecrets = true;
              paint();
            }
          } catch (e) {}
        }}>${state.showSecrets ? 'Обновить' : 'Показать'}</button>
        <button class="btn-danger" @click=${async () => {
          if (!confirm('Сгенерировать НОВЫЕ api_key и shared_secret? Все ноды нужно будет переконфигурировать.')) return;
          try {
            const r = await act('regenerate-secrets', {}, 'Секреты пересозданы');
            if (r) {
              state.secrets = { api_key: r.api_key, shared_secret: r.shared_secret, copy_to_node_config: r.copy_to_node_config };
              state.showSecrets = true;
              await load();
              paint();
            }
          } catch (e) {}
        }}>Перегенерировать</button>
      </div>
      ${state.showSecrets && state.secrets ? html`
        <div class="secrets-box">
          <div class="secrets-pair">
            <span class="k">api_key</span>
            <code>${state.secrets.api_key}</code>
            <button class="btn-copy" @click=${() => { navigator.clipboard.writeText(state.secrets.api_key); toast.success('Скопировано'); }}>📋</button>
          </div>
          <div class="secrets-pair">
            <span class="k">shared_secret</span>
            <code>${state.secrets.shared_secret}</code>
            <button class="btn-copy" @click=${() => { navigator.clipboard.writeText(state.secrets.shared_secret); toast.success('Скопировано'); }}>📋</button>
          </div>
          ${state.secrets.copy_to_node_config ? html`
            <div class="secrets-snippet">
              <div class="snippet-head">
                <span>Скопируй в <code>config.toml</code> каждой ноды:</span>
                <button class="btn-copy" @click=${() => { navigator.clipboard.writeText(state.secrets.copy_to_node_config); toast.success('Скопировано'); }}>📋 Скопировать блок</button>
              </div>
              <pre>${state.secrets.copy_to_node_config}</pre>
            </div>
          ` : ''}
        </div>
      ` : ''}
    </l-card>
  `;
}

// ----- helpers -----

function formatPct(p) {
  if (!Number.isFinite(p)) return '—';
  return Math.round(p * 100) + '%';
}
function shortNum(n) {
  if (n < 1000) return String(n);
  if (n < 1e6)  return (n / 1000).toFixed(1) + 'k';
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

  /* Settings */
  .settings-grid { display: flex; flex-direction: column; gap: var(--s-4); }
  .settings-row { display: flex; flex-direction: column; gap: 6px; }
  .settings-row .l { font-size: var(--fs-sm); color: var(--text-1); font-weight: var(--fw-semibold); display: flex; justify-content: space-between; align-items: baseline; }
  .settings-row .lv {
    font-family: var(--font-display); font-size: var(--fs-lg); font-weight: var(--fw-bold);
    background: var(--g-text-accent);
    -webkit-background-clip: text; background-clip: text; color: transparent;
  }
  .settings-row .hint { font-size: var(--fs-xs); color: var(--text-3); }
  .settings-input {
    background: var(--bg-2); border: 1px solid var(--border-2);
    padding: 7px 10px; border-radius: var(--r-2);
    color: var(--text-0); font-size: var(--fs-sm);
    width: 100%;
  }
  .settings-input:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }

  .settings-mini-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: var(--s-3) var(--s-4);
  }
  .settings-bool {
    flex-direction: row !important;
    align-items: center; justify-content: space-between;
    padding: 10px 14px;
    background: var(--bg-2);
    border: 1px solid var(--border-1);
    border-radius: var(--r-2);
    cursor: pointer;
  }
  .settings-bool .l { font-weight: var(--fw-semibold); }
  .settings-bool .hint { font-size: 11px; color: var(--text-3); margin-top: 2px; }

  /* Strategy tabs */
  .strategy-tabs {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
    gap: 8px;
    margin-top: 4px;
  }
  .strat-tab {
    text-align: left;
    background: var(--bg-2);
    border: 1px solid var(--border-2);
    border-radius: var(--r-3);
    padding: 12px 14px;
    cursor: pointer;
    transition: all 180ms var(--ease-spring);
  }
  .strat-tab:hover { border-color: var(--border-3); transform: translateY(-1px); }
  .strat-tab.active {
    border-color: var(--accent);
    background: linear-gradient(135deg, rgba(119,145,255,0.10), rgba(178,102,255,0.08));
    box-shadow: var(--shadow-glow);
  }
  .strat-name { font-family: var(--font-display); font-weight: var(--fw-semibold); font-size: var(--fs-md); }
  .strat-hint { font-size: var(--fs-xs); color: var(--text-2); margin-top: 4px; }
  .strat-tab.active .strat-name { background: var(--g-text-accent); -webkit-background-clip: text; background-clip: text; color: transparent; }

  /* Range slider */
  .settings-row input[type=range] {
    width: 100%; accent-color: var(--accent);
    height: 4px;
  }
  .range-ticks {
    display: flex; justify-content: space-between;
    font-size: 10px; color: var(--text-3);
    font-family: var(--font-mono);
    margin-top: 4px;
  }

  /* Switch */
  .switch { display: inline-flex; align-items: center; gap: 8px; cursor: pointer; }
  .switch input { display: none; }
  .switch-track { width: 40px; height: 22px; background: var(--bg-3); border-radius: var(--r-pill); position: relative; transition: background 200ms; }
  .switch-track::after { content: ''; position: absolute; top: 2px; left: 2px; width: 18px; height: 18px; border-radius: 50%; background: white; transition: transform 200ms var(--ease-spring); box-shadow: var(--shadow-sm); }
  .switch input:checked + .switch-track { background: var(--g-accent); }
  .switch input:checked + .switch-track::after { transform: translateX(18px); }

  .settings-foot {
    margin-top: var(--s-4);
    padding-top: var(--s-3);
    border-top: 1px solid var(--border-1);
    display: flex; gap: 8px; align-items: center;
  }

  /* Routing rules */
  .rules-list { display: flex; flex-direction: column; gap: 6px; }
  .rule-row {
    display: grid;
    grid-template-columns: 1.2fr auto 1.2fr 1.2fr 2fr auto;
    gap: 8px;
    align-items: center;
    padding: 6px;
    background: var(--bg-2);
    border: 1px solid var(--border-1);
    border-radius: var(--r-2);
  }
  @media (max-width: 720px) { .rule-row { grid-template-columns: 1fr; } .rule-arrow { display: none; } }
  .rule-row input, .rule-row select {
    background: var(--bg-1); border: 1px solid var(--border-2);
    padding: 6px 8px; border-radius: var(--r-2);
    color: var(--text-0); font-size: var(--fs-sm);
  }
  .rule-row input:focus, .rule-row select:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
  .rule-bal { font-family: var(--font-mono); }
  .rule-arrow { color: var(--text-3); font-family: var(--font-mono); text-align: center; }
  .rule-del {
    background: transparent; color: var(--text-3);
    border: 1px solid var(--border-2);
    width: 26px; height: 26px; border-radius: var(--r-2);
    cursor: pointer; font-size: 12px;
  }
  .rule-del:hover { background: var(--danger); color: white; border-color: transparent; }
  .rules-foot { margin-top: var(--s-3); padding-top: var(--s-3); border-top: 1px solid var(--border-1); display: flex; gap: 8px; align-items: center; }

  /* Node grid (kept compatible) */
  .cn-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: var(--s-3); }
  .cn-card { padding: var(--s-3); background: var(--bg-2); border: 1px solid var(--border-1); border-radius: var(--r-3); display: flex; flex-direction: column; gap: 10px; position: relative; overflow: hidden; transition: border-color var(--t-fast), opacity var(--t-fast); }
  .cn-card::before { content: ''; position: absolute; top: 0; left: 0; right: 0; height: 2px; background: linear-gradient(90deg, var(--accent-from, var(--accent)), var(--accent-to, var(--accent-2))); }
  .cn-card.ok    { --accent-from: var(--success); --accent-to: #06b6d4; }
  .cn-card.down  { --accent-from: var(--danger);  --accent-to: #f43f5e; }
  .cn-card.off   { opacity: 0.6; }
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
  .cn-foot { display: flex; align-items: center; justify-content: space-between; }
  .cn-weight { font-size: var(--fs-xs); color: var(--text-3); }
  .cn-weight b { color: var(--text-1); font-family: var(--font-mono); }
  .cn-acts { display: flex; gap: 4px; }
  .cn-local { display: grid; grid-template-columns: repeat(5, 1fr); gap: var(--s-3); }
  @media (max-width: 900px) { .cn-local { grid-template-columns: repeat(2, 1fr); } }
  .cn-local div { display: flex; flex-direction: column; }
  .cn-local span { font-size: 10px; text-transform: uppercase; color: var(--text-3); letter-spacing: 0.08em; }
  .cn-local b { font-family: var(--font-display); font-size: var(--fs-lg); font-weight: 700; font-variant-numeric: tabular-nums; }

  /* Secrets */
  .secrets-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .secrets-box { margin-top: var(--s-3); padding-top: var(--s-3); border-top: 1px solid var(--border-1); display: flex; flex-direction: column; gap: var(--s-2); }
  .secrets-pair { display: flex; align-items: center; gap: 8px; font-family: var(--font-mono); font-size: 12px; }
  .secrets-pair .k { color: var(--text-3); font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; min-width: 110px; }
  .secrets-pair code { flex: 1; padding: 6px 10px; background: var(--bg-0); border: 1px solid var(--border-1); border-radius: var(--r-2); color: var(--accent); word-break: break-all; }
  .btn-copy {
    background: var(--accent-soft); color: var(--accent);
    border: 1px solid rgba(119,145,255,0.32);
    padding: 4px 8px; border-radius: var(--r-pill);
    cursor: pointer; font-size: 12px;
  }
  .btn-copy:hover { background: var(--accent); color: white; }
  .secrets-snippet { margin-top: 8px; }
  .snippet-head { display: flex; justify-content: space-between; align-items: center; font-size: var(--fs-xs); color: var(--text-2); margin-bottom: 4px; }
  .secrets-snippet pre {
    background: var(--bg-0); border: 1px solid var(--border-1);
    border-radius: var(--r-2); padding: 12px;
    font-family: var(--font-mono); font-size: 12px;
    color: var(--text-1); white-space: pre-wrap; word-break: break-word;
    margin: 0;
    max-height: 220px; overflow: auto;
  }

  /* Buttons */
  .btn-cta {
    background: var(--g-accent); color: white; border: 0;
    padding: 8px 18px; border-radius: var(--r-pill);
    font-weight: var(--fw-semibold); cursor: pointer; font-size: var(--fs-sm);
    box-shadow: var(--shadow-md);
    transition: transform 140ms var(--ease-spring), box-shadow 200ms;
  }
  .btn-cta:hover { transform: translateY(-1px); box-shadow: var(--shadow-glow); }
  .btn-cta:disabled { opacity: 0.5; cursor: not-allowed; transform: none; box-shadow: none; }
  .btn-secondary {
    background: var(--bg-2); color: var(--text-1);
    border: 1px solid var(--border-2);
    padding: 6px 14px; border-radius: var(--r-pill);
    font-size: var(--fs-xs); cursor: pointer; font-weight: var(--fw-semibold);
  }
  .btn-secondary:hover { background: var(--bg-3); border-color: var(--border-3); }
  .btn-link {
    background: transparent; color: var(--text-2);
    border: 0;
    padding: 6px 12px;
    font-size: var(--fs-sm); cursor: pointer;
  }
  .btn-link:hover { color: var(--text-0); }
  .btn-link:disabled { opacity: 0.4; cursor: not-allowed; }
  .btn-danger {
    background: transparent; color: var(--danger);
    border: 1px solid rgba(255,107,122,0.32);
    padding: 6px 14px; border-radius: var(--r-pill);
    font-size: var(--fs-xs); cursor: pointer; font-weight: var(--fw-semibold);
  }
  .btn-danger:hover { background: var(--danger); color: white; border-color: transparent; }

  /* Node editor modal */
  .editor-overlay {
    position: fixed; inset: 0;
    background: var(--surface-overlay);
    backdrop-filter: blur(16px);
    z-index: var(--z-modal);
    display: grid; place-items: center; padding: var(--s-5);
    animation: fade-in var(--t-normal) var(--ease-spring) both;
  }
  .editor-panel {
    width: min(560px, 100%);
    background: var(--bg-1); border: 1px solid var(--border-2);
    border-radius: var(--r-4); box-shadow: var(--shadow-xl);
    overflow: hidden;
  }
  .editor-head {
    padding: var(--s-5);
    background: radial-gradient(80% 100% at 0% 0%, rgba(119,145,255,0.16), transparent 60%);
    border-bottom: 1px solid var(--border-1);
  }
  .editor-title {
    margin: 0; font-family: var(--font-display); font-size: var(--fs-xl);
    font-weight: var(--fw-bold); letter-spacing: -0.018em;
    background: var(--g-text-accent); -webkit-background-clip: text;
    background-clip: text; color: transparent;
  }
  .editor-head p { margin: 4px 0 0; color: var(--text-2); font-size: var(--fs-sm); }
  .editor-body { padding: var(--s-4) var(--s-5); display: flex; flex-direction: column; gap: var(--s-3); }
  .editor-foot { padding: var(--s-3) var(--s-5); display: flex; gap: var(--s-2); justify-content: flex-end; border-top: 1px solid var(--border-1); }

  .form-row { display: flex; flex-direction: column; gap: 4px; }
  .form-row.inline { flex-direction: row; gap: var(--s-3); }
  .form-row label { font-size: var(--fs-xs); color: var(--text-2); font-weight: var(--fw-semibold); text-transform: uppercase; letter-spacing: 0.04em; }
  .form-row input, .form-row textarea {
    background: var(--bg-2); border: 1px solid var(--border-2);
    padding: 8px 10px; border-radius: var(--r-2);
    color: var(--text-0); font-size: var(--fs-sm);
  }
  .form-row textarea { resize: vertical; min-height: 50px; font-family: var(--font-text); }
  .form-row input:focus, .form-row textarea:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
  .form-row .hint { font-size: 11px; color: var(--text-3); }
`;

export async function render_($mount) {
  $mount.innerHTML = `<div id="cluster-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
