// pages/tg-settings.js — Telegram bot configuration.
//
// GET  /api/tgsettings → { enable, bot_token, bot_name, admin_id, admin_path,
//                          max_devices_per_user, auto_approve, auto_approve_days,
//                          required_chats: [{chat_id,title,link}],
//                          check_interval_min,
//                          kit: { enable, serverHost, cacheToSeconds },
//                          auth_page: { ...custom theme... } }
// POST /api/tgsettings → save (super-admin only)
// POST /api/tgsettings/regen-path → regenerate admin path (restart needed)

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = null;
let showToken = false;

function paint($mount) {
  if (!state) {
    render(html`<l-card loading title="Загрузка..."></l-card>`, $mount);
    return;
  }

  const kit = state.kit || {};
  const requiredChats = state.required_chats || [];
  const interval = state.check_interval_min || 60;
  const adminPath = state.admin_path || 'cp_???';

  render(html`
    <section class="hero">
      <h1>Telegram</h1>
      <p>Bot для авторизации пользователей, обязательные подписки на каналы, kit для веб-клиента. Изменения применяются hot-reload, рестарт — только при смене admin path.</p>
      <div class="hero-actions">
        <span class="chip ${state.enable ? 'accent' : ''}">${state.enable ? 'BOT ON' : 'BOT OFF'}</span>
        ${state.bot_name ? html`<span class="chip">@${state.bot_name}</span>` : ''}
        <span class="chip">${requiredChats.length} обязательных подписок</span>
        ${kit.enable ? html`<span class="chip accent">KIT ON</span>` : html`<span class="chip">kit off</span>`}
      </div>
    </section>

    <div class="grid cols-2">
      <l-card title="Основные" subtitle="bot, token, лимиты" accent="violet">
        <div class="form-grid">
          <label class="form-row inline">
            <span class="label">Bot включён</span>
            <label class="switch">
              <input type="checkbox" id="tg-enable" ?checked=${state.enable}/>
              <span class="switch-track"></span>
            </label>
          </label>

          <label class="form-row">
            <span class="label">Bot Token</span>
            <div class="token-input">
              <input id="tg-token" type=${showToken ? 'text' : 'password'} .value=${state.bot_token || ''} placeholder="123456:ABC..."/>
              <button class="btn-eye" @click=${() => { showToken = !showToken; paint($mount); }} type="button">${showToken ? '◓' : '◐'}</button>
            </div>
            <span class="hint">Получи у @BotFather</span>
          </label>

          <label class="form-row">
            <span class="label">Имя бота</span>
            <input id="tg-name" type="text" .value=${state.bot_name || ''} placeholder="MyLampacBot"/>
            <span class="hint">Без @, используется в ссылках на чат</span>
          </label>

          <label class="form-row">
            <span class="label">Максимум устройств / пользователь</span>
            <input id="tg-max-devices" type="number" min="-1" max="100" .value=${state.max_devices_per_user || state.max_devices || 3}/>
            <span class="hint">-1 = безлимит</span>
          </label>
        </div>
      </l-card>

      <l-card title="Авто-одобрение" subtitle="без участия админа" accent="cyan">
        <div class="form-grid">
          <label class="form-row inline">
            <span class="label">Авто-одобрять</span>
            <label class="switch">
              <input type="checkbox" id="tg-aa" ?checked=${state.auto_approve}/>
              <span class="switch-track"></span>
            </label>
          </label>

          <label class="form-row">
            <span class="label">Срок (дней)</span>
            <select id="tg-aa-days">
              ${[1,7,30,90,365].map(d => html`<option value=${d} ?selected=${(state.auto_approve_days || 30) === d}>${d}</option>`)}
            </select>
            <span class="hint">После апрува пользователь активен указанное число дней</span>
          </label>

          <div class="form-row" style="margin-top:auto">
            <span class="label">Admin Path</span>
            <div class="admin-path">
              <code>/${adminPath}</code>
              <button class="btn-small danger" @click=${() => regenPath($mount)}>Перегенерировать</button>
            </div>
            <span class="hint">После регенерации требуется перезапуск</span>
          </div>
        </div>
      </l-card>
    </div>

    <div class="section-title">Обязательные подписки</div>
    <l-card title="Каналы / группы" subtitle="бот должен быть админом" accent="amber">
      <div class="form-row inline" style="margin-bottom:var(--s-3)">
        <span class="label">Интервал проверки (мин)</span>
        <select id="tg-interval">
          ${[15,30,60,120,360].map(v => html`<option value=${v} ?selected=${interval === v}>${v}</option>`)}
        </select>
      </div>

      ${requiredChats.length ? html`
        <table class="chats-table">
          <thead><tr><th>Chat ID</th><th>Название</th><th>Ссылка</th><th></th></tr></thead>
          <tbody>
            ${requiredChats.map((c, i) => html`
              <tr>
                <td><code>${c.chat_id}</code></td>
                <td>${c.title || ''}</td>
                <td>${c.link ? html`<a href=${c.link} target="_blank">${c.link}</a>` : ''}</td>
                <td><button class="btn-small danger" @click=${() => removeChat($mount, i)}>✕</button></td>
              </tr>
            `)}
          </tbody>
        </table>
      ` : html`<div class="empty">Подписок нет — бот не будет проверять членство</div>`}

      <div class="chat-add">
        <input id="rc-id" placeholder="Chat ID (-100xxxxx)" type="number"/>
        <input id="rc-title" placeholder="Название"/>
        <input id="rc-link" placeholder="telegram.me/channel"/>
        <button class="btn-small primary" @click=${() => addChat($mount)}>+ Добавить</button>
      </div>
      <span class="hint">ID канала можно узнать через @userinfobot или forward сообщения боту.</span>
    </l-card>

    <div class="section-title">Kit (web-клиент)</div>
    <l-card title="Lampa Kit" subtitle="прокси-плеер для веб-доступа без отдельной TG-привязки" accent="mint">
      <div class="form-grid">
        <label class="form-row inline">
          <span class="label">Kit включён</span>
          <label class="switch">
            <input type="checkbox" id="kit-enable" ?checked=${kit.enable}/>
            <span class="switch-track"></span>
          </label>
        </label>
        <label class="form-row">
          <span class="label">Server Host</span>
          <input id="kit-host" type="text" .value=${kit.serverHost || ''} placeholder="https://your-domain.com"/>
          <span class="hint">Публичный домен сервера, без trailing slash</span>
        </label>
        <label class="form-row">
          <span class="label">Cache TTL (сек)</span>
          <input id="kit-ttl" type="number" min="5" max="3600" .value=${kit.cacheToSeconds || 60}/>
          <span class="hint">Сколько секунд кэшировать proxy-сессию</span>
        </label>
        <label class="form-row inline">
          <span class="label">Разрешить привязку источников</span>
          <label class="switch">
            <input type="checkbox" id="kit-binds" ?checked=${kit.binds_disabled !== true}/>
            <span class="switch-track"></span>
          </label>
        </label>
        <span class="hint">Выкл — вкладка «Привязки» (личные аккаунты/токены Filmix, KinoPub, Rezka и т.д.) скрыта на /kit и /bkit, а сама привязка недоступна. Вкладки «Балансеры» и «Профиль» остаются.</span>
      </div>
    </l-card>

    <div class="footer-actions">
      <button class="btn-save" @click=${() => save($mount)}>Сохранить всё</button>
    </div>

    <style>
      .form-grid { display: flex; flex-direction: column; gap: var(--s-3); }
      .form-row { display: flex; flex-direction: column; gap: 4px; }
      .form-row.inline { flex-direction: row; align-items: center; justify-content: space-between; gap: var(--s-3); }
      .form-row .label { font-size: var(--fs-sm); color: var(--text-2); font-weight: var(--fw-medium); }
      .form-row input, .form-row select {
        background: var(--bg-2); border: 1px solid var(--border-2);
        padding: 8px 10px; border-radius: var(--r-2);
        color: var(--text-0); font-size: var(--fs-base);
      }
      .form-row input:focus, .form-row select:focus {
        border-color: var(--accent); outline: none;
        box-shadow: 0 0 0 3px var(--accent-soft);
      }
      .form-row .hint { font-size: var(--fs-xs); color: var(--text-3); }

      .token-input { display: flex; gap: 6px; }
      .token-input input { flex: 1; font-family: var(--font-mono); font-size: 13px; letter-spacing: 1px; }
      .btn-eye { background: var(--bg-3); border: 1px solid var(--border-2); border-radius: var(--r-2); color: var(--text-1); padding: 0 12px; cursor: pointer; font-size: 14px; }
      .btn-eye:hover { background: var(--accent-soft); color: var(--accent); }

      .switch { display: inline-flex; align-items: center; gap: 8px; cursor: pointer; }
      .switch input { display: none; }
      .switch-track { width: 40px; height: 22px; background: var(--bg-3); border-radius: var(--r-pill); position: relative; transition: background 200ms; }
      .switch-track::after { content: ''; position: absolute; top: 2px; left: 2px; width: 18px; height: 18px; border-radius: 50%; background: white; transition: transform 200ms var(--ease-spring); box-shadow: var(--shadow-sm); }
      .switch input:checked + .switch-track { background: var(--g-accent); }
      .switch input:checked + .switch-track::after { transform: translateX(18px); }

      .admin-path { display: flex; align-items: center; gap: var(--s-3); flex-wrap: wrap; }
      .admin-path code { background: var(--bg-2); border: 1px solid var(--border-1); padding: 4px 10px; border-radius: var(--r-2); font-family: var(--font-mono); font-size: 13px; color: var(--accent); }

      .chats-table { width: 100%; border-collapse: collapse; margin-bottom: var(--s-3); }
      .chats-table th, .chats-table td { text-align: left; padding: 8px 12px; border-bottom: 1px solid var(--border-1); font-size: var(--fs-sm); }
      .chats-table th { font-weight: var(--fw-semibold); color: var(--text-2); text-transform: uppercase; font-size: var(--fs-xs); letter-spacing: 0.04em; }
      .chats-table code { font-family: var(--font-mono); font-size: 12px; color: var(--accent); }

      .chat-add { display: flex; gap: 6px; flex-wrap: wrap; margin-top: var(--s-3); }
      .chat-add input { flex: 1; min-width: 140px; background: var(--bg-2); border: 1px solid var(--border-2); padding: 6px 10px; border-radius: var(--r-2); color: var(--text-0); font-size: var(--fs-sm); }

      .btn-small { background: var(--bg-2); color: var(--text-1); border: 1px solid var(--border-2); padding: 5px 12px; font-size: var(--fs-xs); border-radius: var(--r-pill); cursor: pointer; font-weight: var(--fw-semibold); }
      .btn-small:hover { background: var(--bg-3); }
      .btn-small.primary { background: var(--accent-soft); color: var(--accent); border-color: rgba(119,145,255,0.32); }
      .btn-small.primary:hover { background: var(--accent); color: white; }
      .btn-small.danger { color: var(--danger); border-color: rgba(255,107,122,0.32); }
      .btn-small.danger:hover { background: var(--danger); color: white; }

      .empty { color: var(--text-3); font-size: var(--fs-sm); padding: var(--s-3) 0; text-align: center; }

      .footer-actions { position: sticky; bottom: 0; padding: var(--s-3) 0; margin-top: var(--s-5); text-align: right; backdrop-filter: blur(6px); }
      .btn-save { background: var(--g-accent); color: white; border: 0; padding: 10px 22px; border-radius: var(--r-pill); font-weight: var(--fw-semibold); cursor: pointer; box-shadow: var(--shadow-md); transition: transform 140ms var(--ease-spring), box-shadow 200ms; font-size: var(--fs-md); }
      .btn-save:hover { transform: translateY(-1px); box-shadow: var(--shadow-glow); }
    </style>
  `, $mount);
}

function addChat($mount) {
  const id = +document.getElementById('rc-id').value;
  const title = document.getElementById('rc-title').value.trim();
  const link = document.getElementById('rc-link').value.trim();
  if (!id) { toast.error('Укажи Chat ID'); return; }
  state.required_chats = state.required_chats || [];
  state.required_chats.push({ chat_id: id, title: title || `Chat ${id}`, link });
  paint($mount);
}

function removeChat($mount, idx) {
  state.required_chats.splice(idx, 1);
  paint($mount);
}

async function save($mount) {
  const interval = +document.getElementById('tg-interval').value || 60;
  const body = {
    enable: document.getElementById('tg-enable').checked,
    bot_token: document.getElementById('tg-token').value,
    bot_name: document.getElementById('tg-name').value.trim(),
    max_devices_per_user: +document.getElementById('tg-max-devices').value || 3,
    auto_approve: document.getElementById('tg-aa').checked,
    auto_approve_days: +document.getElementById('tg-aa-days').value || 30,
    required_chats: state.required_chats || [],
    check_interval_min: interval,
    kit: {
      enable: document.getElementById('kit-enable').checked,
      serverHost: document.getElementById('kit-host').value.trim().replace(/\/+$/, ''),
      cacheToSeconds: +document.getElementById('kit-ttl').value || 60,
      binds_disabled: !document.getElementById('kit-binds').checked,
    },
  };
  try {
    const r = await api.saveTGSettings(body);
    if (r.ok) {
      toast.success('Сохранено' + (r.restart_needed ? ' — нужен рестарт' : ''));
      state = { ...state, ...body };
    } else {
      toast.error(r.error || 'Не получилось');
    }
  } catch (e) {
    toast.error(e.message);
  }
}

async function regenPath($mount) {
  if (!confirm('Перегенерировать admin path? Текущий /' + (state.admin_path || '?') + ' станет недоступен после перезапуска сервера.')) return;
  try {
    const r = await api.regenAdminPath();
    if (r.ok) {
      toast.success('Новый путь: /' + r.new_path + ' (рестарт нужен)');
      state.admin_path = r.new_path;
      paint($mount);
    } else {
      toast.error(r.error || 'Не получилось');
    }
  } catch (e) {
    toast.error(e.message);
  }
}

export async function render_($mount) {
  render(html`<l-card loading title="Загрузка..."></l-card>`, $mount);
  try {
    state = await api.tgsettings();
    paint($mount);
  } catch (e) {
    render(html`<l-card><div style="color:var(--danger)">Не загрузилось: ${e.message}</div></l-card>`, $mount);
  }
}

export { render_ as render };
