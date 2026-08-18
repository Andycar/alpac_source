// pages/appreplace.js — string-replacement rules applied to Lampa client
// (app.min.js and friends). Each rule has a regex pattern + replacement;
// rules are applied at JS-serve time by applyPublicBrandingJS or similar.
//
// API:
//   GET  /api/appreplace → [{name, pattern, replacement, enabled}, ...]
//   POST /api/appreplace → bare array (same shape) — server validates each
//                           regex and writes to config.toml [web].app_replace
//
// The user noted the OLD admin didn't save AppReplace. The handler DOES exist
// and works — likely the old admin's form sent a wrong shape. This v2 page
// sends the exact array the handler expects.

import { html, render } from '../components/_lit.js';
import { api } from '../api.js';
import { toast } from '../components/l-toast.js';

let rules = [];
let testInput = '';
let testIdx = 0;

function paint($mount) {
  render(html`
    <section class="hero">
      <h1>AppReplace</h1>
      <p>Regex-замены, которые применяются к JS-файлам Lampa перед отдачей клиенту. Используй для кастомного брендинга, патчей багов в кэшированных версиях, или подмены URL.</p>
      <div class="hero-actions">
        <span class="chip accent">${rules.length} правил</span>
        <span class="chip">${rules.filter(r => r.enabled).length} активных</span>
        <button class="btn-cta" @click=${() => addRule($mount)}>+ Новое правило</button>
      </div>
    </section>

    ${rules.length ? html`
      <div class="rules-list">
        ${rules.map((r, i) => renderRule($mount, r, i))}
      </div>
    ` : html`
      <l-card variant="glass">
        <div class="empty-state">
          <div class="empty-icon">⤿</div>
          <div class="empty-title">Правил нет</div>
          <div class="empty-hint">Создай первое правило — regex-паттерн будет проверен при сохранении.</div>
          <button class="btn-cta" @click=${() => addRule($mount)} style="margin-top:var(--s-3)">+ Добавить правило</button>
        </div>
      </l-card>
    `}

    ${rules.length ? html`
      <div class="footer-actions">
        <button class="btn-save" @click=${() => save($mount)}>Сохранить все (${rules.length})</button>
      </div>
    ` : ''}

    <style>
      .btn-cta { background: var(--g-accent); color: white; border: 0; padding: 8px 16px; border-radius: var(--r-pill); font-weight: var(--fw-semibold); cursor: pointer; font-size: var(--fs-sm); box-shadow: var(--shadow-md); transition: transform 140ms var(--ease-spring); }
      .btn-cta:hover { transform: translateY(-1px); box-shadow: var(--shadow-glow); }

      .rules-list { display: flex; flex-direction: column; gap: var(--s-3); }

      .rule-card {
        position: relative;
        background: var(--bg-1);
        border: 1px solid var(--border-1);
        border-radius: var(--r-3);
        padding: var(--s-4);
        transition: border-color 200ms, box-shadow 200ms;
      }
      .rule-card.disabled { opacity: 0.55; }
      .rule-card:hover { border-color: var(--border-2); }
      .rule-card.testing { border-color: var(--accent); box-shadow: var(--shadow-glow); }

      .rule-head { display: flex; align-items: center; gap: var(--s-3); margin-bottom: var(--s-3); }
      .rule-num { width: 28px; height: 28px; border-radius: 50%; background: var(--bg-3); display: grid; place-items: center; font-family: var(--font-mono); font-size: 12px; color: var(--text-2); flex-shrink: 0; }
      .rule-name { flex: 1; background: transparent; border: 0; color: var(--text-0); font-family: var(--font-display); font-size: var(--fs-md); font-weight: var(--fw-semibold); padding: 4px 0; outline: none; min-width: 0; }
      .rule-name:focus { border-bottom: 2px solid var(--accent); }
      .rule-name::placeholder { color: var(--text-3); font-weight: var(--fw-medium); }

      .rule-fields { display: grid; grid-template-columns: 1fr 1fr; gap: var(--s-3); margin-bottom: var(--s-2); }
      @media (max-width: 720px) { .rule-fields { grid-template-columns: 1fr; } }

      .rule-field { display: flex; flex-direction: column; gap: 4px; }
      .rule-field label { font-size: var(--fs-xs); color: var(--text-2); font-weight: var(--fw-semibold); text-transform: uppercase; letter-spacing: 0.04em; }
      .rule-field textarea {
        background: var(--bg-0);
        border: 1px solid var(--border-2);
        padding: 10px 12px;
        border-radius: var(--r-2);
        color: var(--text-0);
        font-family: var(--font-mono);
        font-size: 12.5px;
        min-height: 64px;
        resize: vertical;
        line-height: 1.5;
      }
      .rule-field textarea:focus {
        border-color: var(--accent);
        outline: none;
        box-shadow: 0 0 0 3px var(--accent-soft);
      }
      .rule-field.has-error textarea {
        border-color: var(--danger);
        box-shadow: 0 0 0 3px var(--danger-soft);
      }
      .rule-field .error-msg { font-size: var(--fs-xs); color: var(--danger); font-family: var(--font-mono); }

      .switch { display: inline-flex; align-items: center; gap: 8px; cursor: pointer; }
      .switch input { display: none; }
      .switch-track { width: 36px; height: 20px; background: var(--bg-3); border-radius: var(--r-pill); position: relative; transition: background 200ms; }
      .switch-track::after { content: ''; position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; border-radius: 50%; background: white; transition: transform 200ms var(--ease-spring); box-shadow: var(--shadow-sm); }
      .switch input:checked + .switch-track { background: var(--g-accent); }
      .switch input:checked + .switch-track::after { transform: translateX(16px); }

      .rule-foot { display: flex; align-items: center; justify-content: space-between; margin-top: var(--s-3); gap: var(--s-3); flex-wrap: wrap; }

      .btn-icon {
        background: transparent; border: 1px solid var(--border-2); color: var(--text-2);
        padding: 4px 10px; border-radius: var(--r-2); cursor: pointer; font-size: 14px;
      }
      .btn-icon:hover { color: var(--text-0); background: var(--bg-2); }
      .btn-icon.danger { color: var(--danger); border-color: rgba(255,107,122,0.32); }
      .btn-icon.danger:hover { background: var(--danger); color: white; border-color: transparent; }
      .btn-icon.primary { color: var(--accent); border-color: rgba(119,145,255,0.32); }
      .btn-icon.primary:hover { background: var(--accent); color: white; }

      .test-panel {
        margin-top: var(--s-3);
        padding: var(--s-3);
        background: var(--bg-0);
        border: 1px dashed var(--border-2);
        border-radius: var(--r-2);
      }
      .test-panel textarea {
        width: 100%; background: var(--bg-1);
        border: 1px solid var(--border-1);
        padding: 8px 10px; border-radius: var(--r-2);
        color: var(--text-0); font-family: var(--font-mono);
        font-size: 12px; min-height: 50px; resize: vertical;
      }
      .test-out {
        margin-top: var(--s-2); padding: 10px 12px;
        background: var(--bg-1); border-radius: var(--r-2);
        font-family: var(--font-mono); font-size: 12px;
        color: var(--text-1); white-space: pre-wrap; word-break: break-word;
        min-height: 36px; border: 1px solid var(--border-1);
      }
      .test-out.match { border-color: var(--success); }
      .test-out.no-match { border-color: var(--warn); color: var(--text-2); font-style: italic; }
      .test-out .hl { background: var(--accent-soft); color: var(--accent); padding: 0 2px; border-radius: 2px; }

      .empty-state { text-align: center; padding: var(--s-7) var(--s-4); }
      .empty-icon { font-size: 48px; opacity: 0.4; margin-bottom: var(--s-3); }
      .empty-title { font-family: var(--font-display); font-size: var(--fs-lg); font-weight: var(--fw-semibold); }
      .empty-hint { color: var(--text-2); font-size: var(--fs-sm); margin-top: 6px; max-width: 40ch; margin-left: auto; margin-right: auto; line-height: 1.5; }

      .footer-actions { position: sticky; bottom: 0; padding: var(--s-3) 0; margin-top: var(--s-5); text-align: right; }
      .btn-save { background: var(--g-accent); color: white; border: 0; padding: 10px 22px; border-radius: var(--r-pill); font-weight: var(--fw-semibold); cursor: pointer; box-shadow: var(--shadow-md); transition: transform 140ms var(--ease-spring), box-shadow 200ms; font-size: var(--fs-md); }
      .btn-save:hover { transform: translateY(-1px); box-shadow: var(--shadow-glow); }
    </style>
  `, $mount);
}

function renderRule($mount, r, idx) {
  let regexError = '';
  if (r.pattern) {
    try { new RegExp(r.pattern); } catch (e) { regexError = e.message; }
  }
  return html`
    <div class="rule-card ${!r.enabled ? 'disabled' : ''} ${testIdx === idx ? 'testing' : ''}">
      <div class="rule-head">
        <div class="rule-num">${idx + 1}</div>
        <input class="rule-name" placeholder="Название правила…" .value=${r.name || ''}
               @input=${(e) => { r.name = e.target.value; }}/>
        <label class="switch" title="enabled">
          <input type="checkbox" ?checked=${r.enabled} @change=${(e) => { r.enabled = e.target.checked; paint($mount); }}/>
          <span class="switch-track"></span>
        </label>
      </div>

      <div class="rule-fields">
        <div class="rule-field ${regexError ? 'has-error' : ''}">
          <label>Pattern (regex)</label>
          <textarea spellcheck="false" placeholder='\\bhello\\b' @input=${(e) => { r.pattern = e.target.value; paint($mount); }}>${r.pattern || ''}</textarea>
          ${regexError ? html`<div class="error-msg">⚠ ${regexError}</div>` : ''}
        </div>
        <div class="rule-field">
          <label>Replacement</label>
          <textarea spellcheck="false" placeholder="goodbye" @input=${(e) => { r.replacement = e.target.value; }}>${r.replacement || ''}</textarea>
        </div>
      </div>

      <div class="rule-foot">
        <div style="display:flex;gap:6px">
          <button class="btn-icon primary" title="Протестировать" @click=${() => { testIdx = testIdx === idx ? -1 : idx; paint($mount); }}>⌕ Тест</button>
          <button class="btn-icon" title="Дублировать" @click=${() => dupRule($mount, idx)}>⎘</button>
          <button class="btn-icon" title="↑" @click=${() => moveRule($mount, idx, -1)}>↑</button>
          <button class="btn-icon" title="↓" @click=${() => moveRule($mount, idx, +1)}>↓</button>
        </div>
        <button class="btn-icon danger" title="Удалить" @click=${() => delRule($mount, idx)}>✕</button>
      </div>

      ${testIdx === idx ? renderTester($mount, r) : ''}
    </div>
  `;
}

function renderTester($mount, r) {
  let result = null;
  let outClass = '';
  try {
    const re = new RegExp(r.pattern, 'g');
    const matched = re.test(testInput);
    if (matched) {
      const replaced = testInput.replace(new RegExp(r.pattern, 'g'), r.replacement || '');
      // highlight what changed (visualise on the OUTPUT)
      result = html`<div class="test-out match">${replaced}</div>`;
      outClass = 'match';
    } else {
      result = html`<div class="test-out no-match">— нет совпадений —</div>`;
    }
  } catch (e) {
    result = html`<div class="test-out no-match" style="color:var(--danger)">Bad regex: ${e.message}</div>`;
  }
  return html`
    <div class="test-panel">
      <textarea placeholder="Тестовый ввод (вставь сюда фрагмент app.min.js)…" @input=${(e) => { testInput = e.target.value; paint($mount); }}>${testInput}</textarea>
      ${result}
    </div>
  `;
}

function addRule($mount) {
  rules.push({ name: '', pattern: '', replacement: '', enabled: true });
  paint($mount);
}
function dupRule($mount, i) { rules.splice(i + 1, 0, { ...rules[i] }); paint($mount); }
function moveRule($mount, i, delta) {
  const j = i + delta;
  if (j < 0 || j >= rules.length) return;
  [rules[i], rules[j]] = [rules[j], rules[i]];
  paint($mount);
}
function delRule($mount, i) {
  if (!confirm('Удалить правило?')) return;
  rules.splice(i, 1);
  if (testIdx === i) testIdx = -1;
  paint($mount);
}

async function save($mount) {
  // Validate all regexes client-side first.
  for (let i = 0; i < rules.length; i++) {
    if (!rules[i].pattern) continue;
    try { new RegExp(rules[i].pattern); }
    catch (e) {
      toast.error(`Правило #${i+1}: bad regex — ${e.message}`);
      return;
    }
  }
  try {
    await api.saveAppreplace(rules);
    toast.success(`Сохранено ${rules.length} правил`);
  } catch (e) {
    toast.error(e.message);
  }
}

export async function render_($mount) {
  render(html`<l-card loading title="Загрузка..."></l-card>`, $mount);
  testIdx = -1;
  testInput = '';
  try {
    const data = await api.appreplace();
    rules = Array.isArray(data) ? data.map(r => ({ ...r })) : [];
    paint($mount);
  } catch (e) {
    render(html`<l-card><div style="color:var(--danger)">Не загрузилось: ${e.message}</div></l-card>`, $mount);
  }
}

export { render_ as render };
