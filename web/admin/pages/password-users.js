// pages/password-users.js — alternative-auth user management.
//
// Two sections on one page so admins can reason about the auth flow as a
// whole:
//
//   1. Mode switcher — three buttons (TG / Password / None) plus a
//      short explanation of what flips for end-users. Saving fires
//      POST /api/auth-mode and (server-side) purges anon cookies when
//      we switch AWAY from "none" so a leftover cookie can't grant
//      access after the mode change.
//
//   2. Password users CRUD — table with create / reset / ban / extend /
//      delete actions. Deliberately simpler than /users (no balancer
//      visibility matrix yet); admins can manage balancer visibility
//      through /groups for now.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  mode: 'tg',
  users: [],
  loading: true,
  showCreate: false,
  createUsername: '',
  createPassword: '',
  createGroup: '',
  createDays: 365,
  createComment: '',
  resetTarget: null,
  resetPassword: '',
};

async function load($mount) {
  state.loading = true;
  refresh($mount);
  try {
    const [modeResp, usersResp] = await Promise.all([
      api.authMode().catch(() => ({ mode: 'tg' })),
      api.passwordUsers().catch(() => ({ users: [] })),
    ]);
    state.mode = modeResp.mode || 'tg';
    state.users = (usersResp.users) || [];
  } catch (e) {
    toast.error('Загрузка не удалась: ' + e.message);
  } finally {
    state.loading = false;
    refresh($mount);
  }
}

async function setMode($mount, mode) {
  const prev = state.mode;
  if (mode === prev) return;
  if (mode === 'none') {
    if (!confirm('Открыть полный доступ без авторизации? Любой посетитель сможет пользоваться сервером.')) {
      return;
    }
  }
  if (prev === 'password' && state.users.length > 0 && mode === 'tg') {
    if (!confirm('Переключение на TG-режим оставит парольных пользователей нетронутыми, но они не смогут зайти, пока админ не вернёт парольный режим. Продолжить?')) {
      return;
    }
  }
  try {
    const r = await api.saveAuthMode(mode);
    if (r && r.error) throw new Error(r.error);
    state.mode = r.mode || mode;
    toast.success('Режим: ' + state.mode);
    refresh($mount);
  } catch (e) {
    toast.error('Не удалось сохранить: ' + e.message);
  }
}

async function createUser($mount) {
  if (!state.createUsername.trim() || !state.createPassword.trim()) {
    toast.error('Заполните логин и пароль');
    return;
  }
  const payload = {
    action: 'create',
    username: state.createUsername.trim(),
    password: state.createPassword,
    group_id: state.createGroup.trim() || undefined,
    comment: state.createComment.trim() || undefined,
  };
  if (state.createDays > 0) {
    const d = new Date();
    d.setUTCDate(d.getUTCDate() + Number(state.createDays));
    payload.expires_at = d.toISOString();
  }
  try {
    const r = await api.passwordUsersAction(payload);
    if (r && r.error) throw new Error(r.error);
    toast.success('Создан: ' + state.createUsername);
    state.showCreate = false;
    state.createUsername = '';
    state.createPassword = '';
    state.createComment = '';
    state.createGroup = '';
    await load($mount);
  } catch (e) {
    toast.error('Ошибка: ' + e.message);
  }
}

async function act($mount, body, msg) {
  try {
    const r = await api.passwordUsersAction(body);
    if (r && r.error) throw new Error(r.error);
    if (msg) toast.success(msg);
    await load($mount);
  } catch (e) {
    toast.error(e.message);
  }
}

function modeCard() {
  const opt = (key, title, desc) => html`
    <div
      style="flex:1;border:2px solid ${state.mode === key ? 'var(--accent)' : 'var(--border)'};
             border-radius:12px;padding:14px;cursor:pointer;transition:border-color .15s;
             background:${state.mode === key ? 'rgba(129,140,248,0.08)' : 'transparent'}"
      @click=${(e) => setMode(e.currentTarget.closest('[data-mount]'), key)}
    >
      <div style="font-size:14px;font-weight:600;margin-bottom:4px">${title}</div>
      <div style="font-size:12px;color:var(--text-2);line-height:1.4">${desc}</div>
    </div>
  `;
  return html`
    <l-card title="Способ входа">
      <div style="display:flex;gap:12px;flex-wrap:wrap">
        ${opt('tg', '✈ Telegram', 'Юзеры привязывают аккаунт через @-бот. Текущий вариант по-умолчанию.')}
        ${opt('password', '🔐 Логин/пароль', 'Аккаунты с username+паролем. Создаются в этой вкладке.')}
        ${opt('none', '🌐 Без авторизации', 'Открытый доступ. Каждому посетителю выдаётся анонимный UID.')}
      </div>
      <div style="margin-top:12px;font-size:12px;color:var(--text-3)">
        Текущий режим: <b style="color:var(--text)">${state.mode}</b>.
        Переключение применяется мгновенно, перезапуск не нужен.
      </div>
    </l-card>
  `;
}

function userRow(u) {
  const subBadge = u.expired
    ? html`<l-pill kind="danger">истёк</l-pill>`
    : u.expires_at
      ? html`<l-pill kind="ok">${u.expires_at}</l-pill>`
      : html`<l-pill kind="muted">∞</l-pill>`;
  const banBadge = u.ban ? html`<l-pill kind="danger">бан</l-pill>` : '';
  const premiumBadge = u.premium_active ? html`<l-pill kind="warn">premium</l-pill>` : '';
  return html`
    <tr>
      <td>
        <div style="font-weight:600">${u.username}</div>
        <div style="font-size:11px;color:var(--text-3);font-family:monospace">${u.uid}</div>
      </td>
      <td>${u.group_id || '—'}</td>
      <td>${subBadge} ${premiumBadge} ${banBadge}</td>
      <td>${u.device_count}${u.max_devices > 0 ? '/' + u.max_devices : ''}</td>
      <td>${u.last_login_at || '—'}</td>
      <td style="text-align:right">
        <l-button kind="ghost" size="sm" @click=${(e) => act(e.currentTarget.closest('[data-mount]'),
            { action: 'extend', username: u.username, days: 30 }, '+30 дн')}>+30</l-button>
        <l-button kind="ghost" size="sm" @click=${(e) => {
          const np = prompt('Новый пароль для ' + u.username + ' (мин. 8 символов):');
          if (np && np.length >= 8) {
            act(e.currentTarget.closest('[data-mount]'),
              { action: 'reset_password', username: u.username, password: np }, 'Пароль сброшен');
          } else if (np !== null) {
            toast.error('Минимум 8 символов');
          }
        }}>🔑</l-button>
        <l-button kind=${u.ban ? 'ghost' : 'danger'} size="sm" @click=${(e) => {
          if (u.ban) {
            act(e.currentTarget.closest('[data-mount]'),
              { action: 'unban', username: u.username }, 'Разбанен');
          } else {
            const reason = prompt('Причина бана (опц.):') || '';
            act(e.currentTarget.closest('[data-mount]'),
              { action: 'ban', username: u.username, ban_reason: reason }, 'Забанен');
          }
        }}>${u.ban ? '✓' : '🚫'}</l-button>
        <l-button kind="danger" size="sm" @click=${(e) => {
          if (confirm('Удалить пользователя ' + u.username + '? Это необратимо.')) {
            act(e.currentTarget.closest('[data-mount]'),
              { action: 'delete', username: u.username }, 'Удалён');
          }
        }}>×</l-button>
      </td>
    </tr>
  `;
}

function usersCard($mount) {
  return html`
    <l-card title="Парольные пользователи" subtitle="${state.users.length} ${state.users.length === 1 ? 'аккаунт' : 'аккаунтов'}">
      <div slot="actions">
        <l-button kind="primary" @click=${() => { state.showCreate = true; refresh($mount); }}>+ Создать</l-button>
      </div>
      ${state.loading
        ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">Загрузка…</div>`
        : state.users.length === 0
          ? html`<div style="padding:32px;text-align:center;color:var(--text-3)">
              Ещё нет пользователей. Создайте первого, чтобы начать.
            </div>`
          : html`
            <table style="width:100%;border-collapse:collapse">
              <thead>
                <tr style="text-align:left;color:var(--text-3);font-size:12px;border-bottom:1px solid var(--border)">
                  <th style="padding:8px 6px">Логин / UID</th>
                  <th style="padding:8px 6px">Группа</th>
                  <th style="padding:8px 6px">Статус</th>
                  <th style="padding:8px 6px">Устр.</th>
                  <th style="padding:8px 6px">Последний вход</th>
                  <th style="padding:8px 6px;text-align:right">Действия</th>
                </tr>
              </thead>
              <tbody>${state.users.map(userRow)}</tbody>
            </table>
          `}
    </l-card>
  `;
}

function createModal($mount) {
  if (!state.showCreate) return '';
  const close = () => { state.showCreate = false; refresh($mount); };
  return html`
    <l-modal title="Новый пользователь" open @close=${close}>
      <div style="display:flex;flex-direction:column;gap:12px">
        <l-input
          label="Логин (3-32, латиница/цифры/_/-/.)"
          .value=${state.createUsername}
          @input=${(e) => { state.createUsername = e.target.value; }}
          autocomplete="username"
        ></l-input>
        <l-input
          label="Пароль (мин. 8 символов)"
          type="password"
          .value=${state.createPassword}
          @input=${(e) => { state.createPassword = e.target.value; }}
          autocomplete="new-password"
        ></l-input>
        <l-input
          label="Группа (опционально)"
          .value=${state.createGroup}
          @input=${(e) => { state.createGroup = e.target.value; }}
          placeholder="например, premium"
        ></l-input>
        <l-input
          label="Срок (дней, 0 = бессрочно)"
          type="number"
          .value=${String(state.createDays)}
          @input=${(e) => { state.createDays = Number(e.target.value) || 0; }}
        ></l-input>
        <l-input
          label="Комментарий (виден только админу)"
          .value=${state.createComment}
          @input=${(e) => { state.createComment = e.target.value; }}
        ></l-input>
      </div>
      <div slot="footer" style="display:flex;justify-content:flex-end;gap:8px">
        <l-button kind="ghost" @click=${close}>Отмена</l-button>
        <l-button kind="primary" @click=${() => createUser($mount)}>Создать</l-button>
      </div>
    </l-modal>
  `;
}

function refresh($mount) {
  render(html`
    <div data-mount style="display:flex;flex-direction:column;gap:16px">
      ${modeCard()}
      ${usersCard($mount)}
      ${createModal($mount)}
    </div>
  `, $mount);
}

export async function render_($mount) {
  refresh($mount);
  await load($mount);
}

// Pages export `render` per router.js contract.
export { render_ as render };
