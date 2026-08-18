/**
 * Lampac Cluster Servers — Lampa Settings plugin
 *
 * Registers a "Кластер серверов" section in Lampa's Settings showing the
 * availability of each cluster node:
 *   - Uptime % (server-tracked over the last hour of health probes)
 *   - Ping (latency from primary's probes + EWMA of real requests)
 *   - Visual quality bar combining both into a single signal-strength gauge
 *
 * No corner badge, no preferred-server picker — this is purely an
 * informational dashboard tucked into Settings.
 */
(function () {
    'use strict';

    if (window.lampac_cluster_plugin_loaded) return;
    window.lampac_cluster_plugin_loaded = true;

    // --- resolve our server origin (where on.js served us from) -------------

    var BASE = (function () {
        try {
            var scripts = document.getElementsByTagName('script');
            for (var i = scripts.length - 1; i >= 0; i--) {
                var src = scripts[i].src || '';
                var m = src.match(/^(https?:\/\/[^/]+)\/server_widget/);
                if (m) return m[1];
            }
        } catch (e) {}
        return '{localhost}'.replace(/\/$/, '');
    })();

    // --- helpers ------------------------------------------------------------

    function api(path, timeoutMs) {
        return new Promise(function (resolve, reject) {
            var xhr = new XMLHttpRequest();
            xhr.timeout = timeoutMs || 6000;
            xhr.open('GET', BASE + path + (path.indexOf('?') > -1 ? '&' : '?') + '_t=' + Date.now(), true);
            xhr.onload = function () {
                if (xhr.status >= 200 && xhr.status < 300) {
                    try { resolve(JSON.parse(xhr.responseText)); }
                    catch (e) { reject(e); }
                } else reject(new Error('HTTP ' + xhr.status));
            };
            xhr.onerror = function () { reject(new Error('network')); };
            xhr.ontimeout = function () { reject(new Error('timeout')); };
            xhr.send();
        });
    }

    function esc(s) {
        return String(s == null ? '' : s).replace(/[&<>"]/g, function (c) {
            return ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'})[c];
        });
    }

    function fmtMs(v) {
        if (v == null || isNaN(v) || v <= 0) return '—';
        return Math.round(v) + ' ms';
    }

    /**
     * Quality score 0..100 combining uptime and latency.
     * - Uptime is the primary signal (weight 70%)
     * - Latency adds 0-30 points: <50ms = full, scales down to >800ms = 0
     */
    function qualityScore(s) {
        if (!s.healthy) return 0;
        var up = (s.uptime_pct == null ? 100 : s.uptime_pct);
        var lat = s.avg_latency_ms || 0;
        var latScore;
        if (lat === 0) latScore = 30;
        else if (lat < 50) latScore = 30;
        else if (lat < 100) latScore = 25;
        else if (lat < 200) latScore = 20;
        else if (lat < 400) latScore = 12;
        else if (lat < 800) latScore = 6;
        else latScore = 0;
        return Math.round(up * 0.7 + latScore);
    }

    function qualityLabel(score, healthy) {
        if (!healthy) return 'Недоступен';
        if (score >= 85) return 'Отлично';
        if (score >= 65) return 'Хорошо';
        if (score >= 40) return 'Средне';
        return 'Слабо';
    }

    function qualityColor(score, healthy) {
        if (!healthy) return '#ef4444';
        if (score >= 85) return '#22c55e';
        if (score >= 65) return '#84cc16';
        if (score >= 40) return '#f59e0b';
        return '#ef4444';
    }

    // --- legacy-key cleanup -------------------------------------------------
    // Earlier versions of this plugin (and some Lampa forks) wrote a
    // "preferred server" host into localStorage under these keys. The keys
    // were misused by certain forks as a URL-rewrite override, causing all
    // stream URLs to point at the chosen node host (which breaks HTTPS-only
    // pages because nodes typically listen on HTTP-only ports). Strip them
    // on every plugin load so stale data can't keep hijacking URLs.
    (function clearLegacyPrefKeys() {
        var keys = ['lampac_pref_server_host', 'lampac_preferred_server'];
        try {
            keys.forEach(function (k) {
                if (localStorage.getItem(k) != null) localStorage.removeItem(k);
            });
            if (window.Lampa && Lampa.Storage && typeof Lampa.Storage.set === 'function') {
                keys.forEach(function (k) { Lampa.Storage.set(k, ''); });
            }
        } catch (e) {}
    })();

    // --- styles -------------------------------------------------------------

    function injectStyle() {
        if (document.getElementById('lampac-cluster-style')) return;
        var s = document.createElement('style');
        s.id = 'lampac-cluster-style';
        s.textContent =
            '.lampac-cluster-wrap{padding:8px 0}' +
            '.lampac-cluster-summary{display:flex;gap:14px;flex-wrap:wrap;margin-bottom:14px;font-size:1em}' +
            '.lampac-cluster-summary > div{background:rgba(255,255,255,0.04);border:1px solid rgba(255,255,255,0.06);border-radius:12px;padding:10px 14px;min-width:120px}' +
            '.lampac-cluster-summary .lbl{font-size:0.7em;text-transform:uppercase;letter-spacing:0.04em;opacity:0.55;margin-bottom:4px}' +
            '.lampac-cluster-summary .val{font-size:1.6em;font-weight:600;line-height:1}' +
            '.lampac-cluster-summary .sub{font-size:0.7em;opacity:0.55;margin-top:2px}' +
            '.lampac-cluster-list{display:flex;flex-direction:column;gap:10px}' +
            '.lampac-cluster-card{background:rgba(255,255,255,0.04);border:1px solid rgba(255,255,255,0.07);border-radius:14px;padding:14px 16px;transition:border-color 0.2s,background 0.2s;cursor:pointer;outline:none}' +
            '.lampac-cluster-card:hover,.lampac-cluster-card.focus{border-color:#3b82f6;background:rgba(59,130,246,0.08)}' +
            '.lampac-cluster-card.preferred{border-color:#f59e0b;background:rgba(245,158,11,0.07)}' +
            '.lampac-cluster-card.preferred:hover,.lampac-cluster-card.preferred.focus{border-color:#fbbf24}' +
            '.lampac-cluster-row{display:flex;align-items:center;gap:12px;flex-wrap:wrap}' +
            '.lampac-cluster-name{font-size:1.1em;font-weight:600;flex:1;min-width:140px;display:flex;align-items:center;gap:8px}' +
            '.lampac-cluster-star{color:#f59e0b;font-size:1.1em;flex-shrink:0}' +
            '.lampac-cluster-tag{font-size:0.65em;padding:2px 8px;border-radius:99px;background:rgba(255,255,255,0.08);text-transform:uppercase;letter-spacing:0.05em}' +
            '.lampac-cluster-tag.self{background:rgba(59,130,246,0.2);color:#93c5fd}' +
            '.lampac-cluster-status{font-size:0.85em;font-weight:600;text-align:right}' +
            '.lampac-cluster-bar-wrap{height:8px;background:rgba(255,255,255,0.06);border-radius:99px;overflow:hidden;margin-top:10px}' +
            '.lampac-cluster-bar-fill{height:100%;border-radius:99px;transition:width 0.4s ease,background 0.3s ease}' +
            '.lampac-cluster-meta{display:flex;gap:18px;flex-wrap:wrap;margin-top:8px;font-size:0.8em;opacity:0.7}' +
            '.lampac-cluster-meta b{font-weight:600;opacity:0.95}' +
            '.lampac-cluster-cta{display:flex;gap:8px;margin-top:12px;flex-wrap:wrap}' +
            '.lampac-cluster-btn{background:rgba(255,255,255,0.06);border:1px solid rgba(255,255,255,0.08);color:inherit;padding:6px 12px;border-radius:8px;font-size:0.8em;cursor:pointer;outline:none;transition:all 0.15s}' +
            '.lampac-cluster-btn:hover,.lampac-cluster-btn.focus{background:#3b82f6;border-color:#3b82f6;color:#fff}' +
            '.lampac-cluster-btn.primary{background:#f59e0b;border-color:#f59e0b;color:#000}' +
            '.lampac-cluster-btn.primary:hover,.lampac-cluster-btn.primary.focus{background:#fbbf24;border-color:#fbbf24}' +
            '.lampac-cluster-pref-banner{background:rgba(245,158,11,0.1);border:1px solid rgba(245,158,11,0.3);border-radius:10px;padding:10px 14px;margin-bottom:14px;font-size:0.85em;display:flex;justify-content:space-between;align-items:center;gap:10px;flex-wrap:wrap}' +
            '.lampac-cluster-empty{text-align:center;padding:30px;opacity:0.55;font-size:0.9em}' +
            '.lampac-cluster-error{text-align:center;padding:18px;color:#fca5a5;font-size:0.85em}' +
            '.lampac-cluster-loading{text-align:center;padding:18px;opacity:0.55;font-size:0.85em}' +
            '.lampac-cluster-foot{margin-top:14px;font-size:0.7em;opacity:0.45;text-align:center;line-height:1.6}';
        document.head.appendChild(s);
    }

    // --- render -------------------------------------------------------------

    function renderEmpty() {
        return '<div class="lampac-cluster-empty">Кластер не настроен или список не опубликован.<br>' +
            'Включить публикацию: админка → 🌐 Кластер → ⚙️ Настройки → «Публиковать список нод».</div>';
    }

    function renderSummary(servers) {
        var total = servers.length;
        var avail = servers.filter(function (s) { return s.healthy; }).length;
        var pings = servers.filter(function (s) { return s.avg_latency_ms; }).map(function (s) { return s.avg_latency_ms; });
        var avgPing = pings.length ? Math.round(pings.reduce(function (a, b) { return a + b; }, 0) / pings.length) : 0;
        var uptimes = servers.filter(function (s) { return s.uptime_pct != null; }).map(function (s) { return s.uptime_pct; });
        var avgUp = uptimes.length ? Math.round(uptimes.reduce(function (a, b) { return a + b; }, 0) / uptimes.length) : 100;
        return '<div class="lampac-cluster-summary">' +
            '<div><div class="lbl">Серверов</div><div class="val">' + avail + '/' + total + '</div><div class="sub">доступно</div></div>' +
            '<div><div class="lbl">Средний пинг</div><div class="val">' + (avgPing ? avgPing : '—') + '<span style="font-size:0.5em;opacity:0.6"> ms</span></div><div class="sub">по живым нодам</div></div>' +
            '<div><div class="lbl">Доступность</div><div class="val">' + avgUp + '<span style="font-size:0.5em;opacity:0.6"> %</span></div><div class="sub">за последний час</div></div>' +
            '</div>';
    }

    function renderCard(s) {
        var score = qualityScore(s);
        var color = qualityColor(score, s.healthy);
        var label = qualityLabel(score, s.healthy);
        var up = s.uptime_pct == null ? 100 : s.uptime_pct;
        var tags = '';
        if (s.self) tags += '<span class="lampac-cluster-tag self">этот</span>';
        if (s.region) tags += '<span class="lampac-cluster-tag">' + esc(s.region) + '</span>';
        return '<div class="lampac-cluster-card" data-name="' + esc(s.name) + '">' +
            '<div class="lampac-cluster-row">' +
                '<div class="lampac-cluster-name">' + esc(s.name || '—') + ' ' + tags + '</div>' +
                '<div class="lampac-cluster-status" style="color:' + color + '">' + label + '</div>' +
            '</div>' +
            '<div class="lampac-cluster-bar-wrap"><div class="lampac-cluster-bar-fill" style="width:' + score + '%;background:' + color + '"></div></div>' +
            '<div class="lampac-cluster-meta">' +
                '<div><b>Доступность:</b> ' + up.toFixed(1) + '%</div>' +
                '<div><b>Пинг:</b> ' + fmtMs(s.avg_latency_ms) + '</div>' +
                (s.host ? '<div><b>Хост:</b> ' + esc(s.host) + '</div>' : '') +
            '</div>' +
        '</div>';
    }

    function renderAll(data) {
        var servers = (data && data.servers) || [];
        if (!servers.length) return renderEmpty();
        var html = renderSummary(servers);
        html += '<div class="lampac-cluster-list">';
        servers.forEach(function (s) { html += renderCard(s); });
        html += '</div>';
        if (data.strategy) {
            html += '<div class="lampac-cluster-foot">Стратегия маршрутизации сервера: <b>' + esc(data.strategy) + '</b><br>' +
                'Маршрутизация полностью на стороне primary — клиент только отображает состояние.</div>';
        }
        return html;
    }

    function loadAndRender($target) {
        api('/api/servers/list').then(function (data) {
            $target.innerHTML = renderAll(data);
        }).catch(function (err) {
            $target.innerHTML = '<div class="lampac-cluster-error">Не удалось загрузить список серверов: ' + esc(err.message || err) + '</div>';
        });
    }

    // --- Lampa Settings registration ---------------------------------------

    function registerSettings() {
        if (!window.Lampa || !Lampa.SettingsApi) {
            setTimeout(registerSettings, 400);
            return;
        }

        injectStyle();

        // Add a Settings component (entry in the Settings sidebar).
        Lampa.SettingsApi.addComponent({
            component: 'lampac_cluster',
            name: 'Серверы',
            icon: '<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M4 6h16M4 12h16M4 18h16" stroke="currentColor" stroke-width="2" stroke-linecap="round"/><circle cx="6" cy="6" r="1" fill="currentColor"/><circle cx="6" cy="12" r="1" fill="currentColor"/><circle cx="6" cy="18" r="1" fill="currentColor"/></svg>'
        });

        // Static param with custom render — this becomes the dashboard body.
        Lampa.SettingsApi.addParam({
            component: 'lampac_cluster',
            param: {
                name: 'cluster_dashboard',
                type: 'static',
                default: ''
            },
            field: {
                name: 'Доступность серверов кластера',
                description: 'Состояние нод (uptime, пинг, качество соединения). Обновляется каждые 5 секунд.'
            },
            onRender: function (item) {
                // Build our own content div and attach to the settings row.
                try {
                    var $row = item && item.length ? item : $(item);
                    var container = document.createElement('div');
                    container.className = 'lampac-cluster-wrap';
                    container.innerHTML = '<div class="lampac-cluster-loading">Загрузка…</div>';
                    // Replace the entire field row with full-width content.
                    var rowEl = $row.get ? $row.get(0) : $row;
                    if (rowEl && rowEl.parentNode) {
                        rowEl.style.cursor = 'default';
                        // Strip default value display
                        var valueEl = rowEl.querySelector('.settings-param__value');
                        if (valueEl) valueEl.style.display = 'none';
                        // Append our container below
                        var existing = rowEl.querySelector('.lampac-cluster-wrap');
                        if (existing) existing.remove();
                        rowEl.appendChild(container);
                    } else {
                        // Fallback — just append to body if structure is unexpected.
                        document.body.appendChild(container);
                    }

                    loadAndRender(container);
                    // Periodic refresh while settings is open.
                    var poll = setInterval(function () {
                        if (!document.body.contains(container)) {
                            clearInterval(poll);
                            return;
                        }
                        loadAndRender(container);
                    }, 5000);
                } catch (e) {
                    // Defensive — never break Lampa Settings.
                    console.warn('lampac-cluster onRender failed', e);
                }
            }
        });
    }

    // --- boot ---------------------------------------------------------------

    function boot() {
        if (typeof Lampa === 'undefined' || !Lampa.SettingsApi) {
            setTimeout(boot, 500);
            return;
        }
        registerSettings();
    }

    if (document.readyState === 'complete' || document.readyState === 'interactive') {
        setTimeout(boot, 100);
    } else {
        document.addEventListener('DOMContentLoaded', function () { setTimeout(boot, 100); });
    }
})();
