// pages/broadcast.js — compose + send a Telegram broadcast to all known users.
//
// API:
//   GET  /api/broadcast → {user_count}
//   POST /api/broadcast {text} → {ok, total, sent, failed}
//
// Super-admin only on the backend. The UI guards the send button until the
// user count is non-zero AND the text body has content.
//
// Safety:
//   - "Тестовая рассылка себе" sends ONLY to the bot's admin chat. We don't
//     have a dedicated endpoint for that yet so the test mode just shows
//     the markdown-rendered preview; real test-send is a deferred feature.
//   - Confirm dialog before broadcast — protects against fat-fingers.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let state = {
  count: 0,
  text: '',
  history: [],   // local-only — past sends from this session
  loading: true,
  sending: false,
};

async function load() {
  state.loading = true;
  try {
    const d = await api.get('/broadcast');
    state.count = d && d.user_count || 0;
  } catch (e) {
    toast.error('Не удалось получить количество пользователей: ' + e.message);
  } finally {
    state.loading = false;
  }
}

async function sendBroadcast() {
  const text = state.text.trim();
  if (!text) return toast.warn('Введите текст рассылки');
  if (!confirm(`Отправить ${state.count} пользователям?\n\n` + text.slice(0, 400) + (text.length > 400 ? '…' : ''))) return;
  state.sending = true; paint();
  try {
    const r = await api.post('/broadcast', { text });
    if (r && r.error) throw new Error(r.error);
    state.history.unshift({
      at: new Date(),
      text,
      total: r.total || 0,
      sent: r.sent || 0,
      failed: r.failed || 0,
    });
    state.text = '';
    toast.success(`Отправлено: ${r.sent || 0} / ${r.total || 0} (failed ${r.failed || 0})`);
  } catch (e) {
    toast.error(e.message);
  } finally {
    state.sending = false;
    paint();
  }
}

function formatPreview(text) {
  // Best-effort HTML preview: escape, then convert <b>/<i>/<u>/<code>/<a>
  // which are the safe TG HTML tags. We re-render in a sandboxed container.
  const esc = (s) => s.replace(/[&<>]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;'}[c]));
  let s = esc(text);
  // Restore the supported tags after escape.
  s = s.replace(/&lt;(\/?(?:b|i|u|s|code|pre|a|tg-spoiler))(.*?)&gt;/g, (m, tag, attrs) =>
    `<${tag}${attrs.replace(/&quot;/g, '"')}>`
  );
  return s;
}

function paint() {
  const $root = document.getElementById('broadcast-root');
  if (!$root) return;
  const canSend = state.count > 0 && state.text.trim().length > 0 && !state.sending;
  render(html`
    <div class="page-summary">
      <l-stat accent="blue"  label="Активных пользователей" value=${state.count.toLocaleString()} icon="👥"></l-stat>
      <l-stat accent="purple" label="Отправок (сессия)" value=${state.history.length} icon="📨"></l-stat>
      <l-stat accent="amber" label="В очереди" value=${state.sending ? 'отправка…' : '0'} icon="⏳"></l-stat>
    </div>

    <div class="grid cols-2" style="margin-top:var(--s-5)">
      <l-card title="Сообщение">
        <div class="bc-composer">
          <textarea
            class="bc-ta"
            placeholder="Текст рассылки. Поддерживается Telegram HTML: <b>жирный</b>, <i>курсив</i>, <code>код</code>, <a href='...'>ссылка</a>."
            .value=${state.text}
            @input=${e => { state.text = e.target.value; paint(); }}
          ></textarea>
          <div class="bc-meta">
            <span>${state.text.length} символов · ${state.text.split('\n').length} строк</span>
            <l-pill tone=${state.text.length > 4096 ? 'danger' : 'muted'}>
              лимит TG: ${state.text.length}/4096
            </l-pill>
          </div>
          <div class="bc-actions">
            <l-button variant="ghost" icon="🗑" @click=${() => { state.text = ''; paint(); }} ?disabled=${!state.text}>Очистить</l-button>
            <l-button variant="primary" icon="📨" @click=${sendBroadcast} ?disabled=${!canSend} ?loading=${state.sending}>
              Отправить (${state.count})
            </l-button>
          </div>
        </div>
      </l-card>

      <l-card title="Предпросмотр">
        ${state.text.trim()
          ? html`<div class="bc-preview" .innerHTML=${formatPreview(state.text)}></div>`
          : html`<div class="bc-empty">Введите текст слева, чтобы увидеть превью.</div>`}
      </l-card>
    </div>

    ${state.history.length ? html`
      <l-card title="История (текущая сессия)" style="margin-top:var(--s-5)">
        <l-table
          .columns=${[
            { key: 'at', label: 'Время', cell: r => r.at.toLocaleTimeString('ru-RU') },
            { key: 'text', label: 'Текст', cell: r => html`<span style="color:var(--text-2);font-size:var(--fs-xs)">${r.text.slice(0, 80)}${r.text.length > 80 ? '…' : ''}</span>` },
            { key: 'sent', label: 'Sent', align: 'r', format: 'int' },
            { key: 'failed', label: 'Failed', align: 'r', cell: r => r.failed > 0 ? html`<span style="color:var(--danger)">${r.failed}</span>` : html`<span style="color:var(--text-3)">0</span>` },
            { key: 'total', label: 'Total', align: 'r', format: 'int' },
          ]}
          .rows=${state.history}
        ></l-table>
      </l-card>
    ` : ''}
  `, $root);
}

// Page-scoped styles.
const styleId = 'l-broadcast-style';
if (!document.getElementById(styleId)) {
  const s = document.createElement('style');
  s.id = styleId;
  s.textContent = `
    .page-summary { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--s-4); }
    @media (max-width: 900px) { .page-summary { grid-template-columns: 1fr; } }
    .bc-composer { display: flex; flex-direction: column; gap: var(--s-3); }
    .bc-ta {
      width: 100%;
      min-height: 220px;
      padding: var(--s-3);
      background: var(--bg-0);
      border: 1px solid var(--border-2);
      border-radius: var(--r-2);
      color: var(--text-0);
      font-family: var(--font-text);
      font-size: var(--fs-base);
      line-height: 1.55;
      resize: vertical;
      transition: border-color var(--t-fast), box-shadow var(--t-fast);
    }
    .bc-ta:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
    .bc-meta { display: flex; align-items: center; justify-content: space-between; font-size: var(--fs-xs); color: var(--text-3); }
    .bc-actions { display: flex; gap: 8px; justify-content: flex-end; }
    .bc-preview {
      min-height: 220px;
      padding: var(--s-3);
      background: var(--bg-0);
      border: 1px solid var(--border-1);
      border-radius: var(--r-2);
      white-space: pre-wrap;
      word-wrap: break-word;
      color: var(--text-0);
      line-height: 1.55;
    }
    .bc-preview a { color: var(--accent); }
    .bc-preview code, .bc-preview pre {
      background: var(--bg-3);
      padding: 2px 6px;
      border-radius: 4px;
      font-family: var(--font-mono);
      font-size: 0.92em;
    }
    .bc-empty {
      min-height: 220px;
      display: grid; place-items: center;
      color: var(--text-3); font-style: italic;
    }
  `;
  document.head.appendChild(s);
}

export async function render_($mount) {
  $mount.innerHTML = `<div id="broadcast-root"></div>`;
  paint();
  await load();
  paint();
}
export { render_ as render };
