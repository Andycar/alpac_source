// app.js — entry. Same module is used by both index.html (web admin) and
// tg.html (Telegram WebApp). The TG bridge auto-detects the host; when
// Telegram is present we run initData auth before mounting the shell.

import './components/index.js';
import './tg-bridge.js';
import { tg } from './tg-bridge.js';
import { api } from './api.js';
import * as router from './router.js';
import { toast } from './components/l-toast.js';

// Routes. New pages: append here + create a file under pages/.
router.register({
  key: 'dashboard',
  title: 'Дашборд',
  icon: '◆',
  group: 'Обзор',
  loader: () => import('./pages/dashboard.js'),
});
router.register({
  key: 'telemetry',
  title: 'Телеметрия',
  icon: '📡',
  group: 'Обзор',
  loader: () => import('./pages/telemetry.js'),
});
router.register({
  key: 'healthcheck',
  title: 'Healthcheck',
  icon: '🏥',
  group: 'Обзор',
  loader: () => import('./pages/healthcheck.js'),
});

router.register({
  key: 'users',
  title: 'Пользователи',
  icon: '👥',
  group: 'Управление',
  loader: () => import('./pages/users.js'),
});
router.register({
  key: 'password-users',
  title: 'Пароли',
  icon: '🔐',
  group: 'Управление',
  loader: () => import('./pages/password-users.js'),
});
router.register({
  key: 'groups',
  title: 'Группы',
  icon: '◔',
  group: 'Управление',
  loader: () => import('./pages/groups.js'),
});
router.register({
  key: 'balancers',
  title: 'Балансеры',
  icon: '⚙',
  group: 'Управление',
  loader: () => import('./pages/balancers.js'),
});

// More pages (placeholder until ported):
const placeholder = (label) => ({
  render: ($mount) => {
    $mount.innerHTML = `<l-card title="${label}"><div style="color:var(--text-2)">Эта вкладка пока не перенесена. Откройте старую админку для управления — кнопка в правом верхнем углу.</div></l-card>`;
  },
});
router.register({
  key: 'plugins',
  title: 'Плагины',
  icon: '🧩',
  group: 'Управление',
  loader: () => import('./pages/plugins.js'),
});
router.register({
  key: 'modules',
  title: 'JS модули',
  icon: '🧱',
  group: 'Управление',
  loader: () => import('./pages/modules.js'),
});
router.register({
  key: 'bans',
  title: 'Баны',
  icon: '🚫',
  group: 'Управление',
  loader: () => import('./pages/bans.js'),
});
router.register({
  key: 'waf',
  title: 'WAF',
  icon: '⛔',
  group: 'Управление',
  loader: () => import('./pages/waf.js'),
});
router.register({
  key: 'server-stats',
  title: 'Сервер',
  icon: '⚡',
  group: 'Сервис',
  loader: () => import('./pages/server-stats.js'),
});
router.register({
  key: 'cluster',
  title: 'Кластер',
  icon: '🌐',
  group: 'Сервис',
  loader: () => import('./pages/cluster.js'),
});
router.register({
  key: 'torrents',
  title: 'Торренты',
  icon: '⇣',
  group: 'Сервис',
  loader: () => import('./pages/torrents.js'),
});
router.register({
  key: 'torrbalancer',
  title: 'TS Балансер',
  icon: '⇄',
  group: 'Сервис',
  loader: () => import('./pages/torrbalancer.js'),
});
router.register({
  key: 'browser-engine',
  title: 'Браузер',
  icon: '◐',
  group: 'Сервис',
  loader: () => import('./pages/browser-engine.js'),
});
router.register({
  key: 'deps',
  title: 'Зависимости',
  icon: '⤓',
  group: 'Сервис',
  loader: () => import('./pages/deps.js'),
});
router.register({
  key: 'transcoding',
  title: 'Транскодинг',
  icon: '🎞',
  group: 'Сервис',
  loader: () => import('./pages/transcoding.js'),
});
router.register({
  key: 'broadcast',
  title: 'Рассылка',
  icon: '📨',
  group: 'Сервис',
  loader: () => import('./pages/broadcast.js'),
});
router.register({
  key: 'promo',
  title: 'Промокоды',
  icon: '🎟',
  group: 'Сервис',
  loader: () => import('./pages/promo.js'),
});
router.register({
  key: 'admins',
  title: 'Админы',
  icon: '🛡',
  group: 'Сервис',
  loader: () => import('./pages/admins.js'),
});
router.register({
  key: 'tg-settings',
  title: 'Telegram',
  icon: '✈',
  group: 'Сервис',
  loader: () => import('./pages/tg-settings.js'),
});
router.register({
  key: 'appreplace',
  title: 'AppReplace',
  icon: '⤿',
  group: 'Сервис',
  loader: () => import('./pages/appreplace.js'),
});
router.register({
  key: 'inspector',
  title: 'Inspector',
  icon: '🔍',
  group: 'Система',
  loader: () => import('./pages/inspector.js'),
});
router.register({
  key: 'selfupdate',
  title: 'Обновления',
  icon: '⇪',
  group: 'Система',
  loader: () => import('./pages/selfupdate.js'),
});
router.register({
  key: 'branding',
  title: 'Брендинг',
  icon: '🎨',
  group: 'Система',
  loader: () => import('./pages/branding.js'),
});
router.register({
  key: 'media-probe',
  title: 'Probe URL',
  icon: '🔬',
  group: 'Система',
  loader: () => import('./pages/media-probe.js'),
});
router.register({
  key: 'proxycore',
  title: 'ProxyCore',
  icon: '🛰',
  group: 'Сервис',
  loader: () => import('./pages/proxycore.js'),
});
router.register({
  key: 'proxy',
  title: 'Proxy (legacy)',
  icon: '🔗',
  group: 'Сервис',
  loader: () => import('./pages/proxy.js'),
});
router.register({
  key: 'music-sources',
  title: 'Music источники',
  icon: '🎵',
  group: 'Источники',
  loader: () => import('./pages/music-sources.js'),
});
router.register({
  key: 'iptv-registry',
  title: 'IPTV каналы',
  icon: '📺',
  group: 'Контент',
  loader: () => import('./pages/iptv-registry.js'),
});
router.register({
  key: 'calendar',
  title: 'Календарь',
  icon: '📅',
  group: 'Контент',
  loader: () => import('./pages/calendar.js'),
});
router.register({
  key: 'feedback',
  title: 'Тикеты',
  icon: '💬',
  group: 'Контент',
  loader: () => import('./pages/feedback.js'),
});
router.register({
  key: 'logs',
  title: 'Логи',
  icon: '📜',
  group: 'Управление',
  loader: () => import('./pages/logs.js'),
});
router.register({
  key: 'config',
  title: 'Конфиг',
  icon: '🔧',
  group: 'Управление',
  loader: () => import('./pages/config.js'),
});

async function bootstrap() {
  // TG path: validate initData before doing anything else.
  if (tg.isAvailable) {
    try {
      const auth = await tg.authenticate();
      window.__lampacAdmin = { user: auth, tg: true };
    } catch (e) {
      document.body.innerHTML = `
        <div style="padding:24px;font-family:var(--font-text);color:#fca5a5;text-align:center">
          <div style="font-size:18px;font-weight:600;margin-bottom:8px">Не удалось авторизоваться</div>
          <div style="color:#94a3b8">${e.message}</div>
          <div style="margin-top:16px;color:#94a3b8;font-size:13px">
            Откройте <code>/tg/auth</code> в браузере и войдите как админ один раз —
            после этого Telegram WebApp сможет авторизоваться автоматически.
          </div>
        </div>
      `;
      return;
    }
  }

  // Fetch admin identity for the topbar (best-effort).
  let who = null;
  try { who = await api.whoami(); } catch {}

  // Self-hosted jacred: only surface its embedded web UI when the parser is
  // actually enabled ([parser] jacred_local) — otherwise the tab would lead
  // to a 503 page. Best-effort: on any error we simply don't add the tab.
  try {
    const jr = await api.get('/jacred/status');
    if (jr && jr.enabled) {
      router.register({
        key: 'jacred',
        title: 'Парсер jacred',
        icon: '🔎',
        group: 'Сервис',
        loader: () => import('./pages/jacred.js'),
      });
    }
  } catch {}

  // Build the shell.
  const shell = document.createElement('l-shell');
  shell.items = router.list().map(r => ({
    key: r.key,
    label: r.title,
    icon: r.icon || '●',
    group: r.group || '',
  }));
  shell.user = who ? {
    name: who.username || who.telegram_id || 'admin',
    role: who.is_super ? 'super' : (who.is_admin ? 'admin' : 'user'),
  } : null;

  // TG WebApp: hint the shell to use mobile layout regardless of viewport
  // width (Telegram iframe sometimes reports a desktop-ish width on tablets
  // even though the user is on a phone). Also makes auto-resize re-evaluate.
  if (tg.isAvailable) {
    shell.mobile = true;
  }

  // Legacy panel quick-link in the topbar. Hide inside TG WebApp — opening
  // the legacy panel cracks out of the mini-app session and confuses users.
  if (!tg.isAvailable) {
    // The legacy admin lives at exactly /<adminPath> (NO trailing slash —
    // the chi route is registered as `/cp_xxx`, and `/cp_xxx/` 404s). The SPA
    // is at /<adminPath>/v2/, so a relative `../` resolves to `/cp_xxx/` with
    // the breaking slash. Compute the absolute, slash-free path instead.
    const adminPath = (window.location.pathname.split('/').filter(Boolean)[0]) || '';
    const legacyA = document.createElement('a');
    legacyA.slot = 'topbar';
    legacyA.href = '/' + adminPath;
    legacyA.style.cssText = 'font-size:12px;color:var(--text-2);text-decoration:none;padding:6px 10px;border-radius:var(--r-2);border:1px solid var(--border-2);';
    legacyA.textContent = '← Старая админка';
    shell.appendChild(legacyA);
  }

  document.body.appendChild(shell);

  // Mount slot for pages.
  const mountWrap = document.createElement('div');
  shell.appendChild(mountWrap);

  shell.addEventListener('navigate', e => router.setActive(e.detail.key));
  router.onChange(async ({ key, title }) => {
    shell.active = key;
    shell.title = title;
    await router.mountPage(key, mountWrap, { who });
  });

  router.start();

  if (tg.isAvailable) {
    tg.haptic('light');
    toast.info('Привет, ' + (tg.user?.first_name || 'admin'));
  }
}

bootstrap();
