/**
 * Lampac Web Player — Premium Video Player Plugin for Lampa
 *
 * Entry point: loaded as Lampa plugin (IIFE bundle).
 * Only activates on web browsers. Android/TV/WebView clients are unaffected.
 */

import { shouldActivatePlayer } from './lib/bridge/PlatformDetect';
import { LampaBridge } from './lib/bridge/LampaBridge';
import cssText from './lib/styles/player.css?inline';
import themesCss from './lib/styles/themes.css?inline';

(function () {
  // Guard: prevent double initialization
  if ((window as any).lampac_webplayer) return;
  (window as any).lampac_webplayer = true;

  // Capture the origin we were loaded from. Lampa pages can be hosted on
  // a different domain than the lampac server (e.g. lampa.app pointing at
  // a self-hosted backend), in which case `location.host` is wrong for
  // server-side endpoints like the watchparty WS relay. We pin the
  // lampac origin once at script-load time using document.currentScript.
  try {
    const cs = (document as any).currentScript as HTMLScriptElement | null;
    let src = cs?.src || '';
    if (!src) {
      // Fallback: scan all script tags for one ending in webplayer.js.
      const all = Array.from(document.scripts) as HTMLScriptElement[];
      const match = all.find((s) => /\/webplayer\.js(\?|$)/i.test(s.src));
      if (match) src = match.src;
    }
    if (src) {
      const u = new URL(src, location.href);
      (window as any).__lwpServerOrigin = u.origin;
    }
  } catch {
    /* ignore — fall back to location.* later */
  }

  // Only activate on web browsers (or when server explicitly enables for Android)
  if (!shouldActivatePlayer()) {
    return;
  }

  const Lampa = (window as any).Lampa;

  // Register settings for player selection (user can toggle new/old player)
  let settingRegistered = false;
  function registerPlayerSetting() {
    if (settingRegistered) return;
    if (!Lampa?.SettingsApi || !Lampa?.Storage || !Lampa?.Lang) return;
    settingRegistered = true;
    try {
      // Default: enabled
      if (Lampa.Storage.get('lwp_enabled', '') === '') {
        Lampa.Storage.set('lwp_enabled', true);
      }
      Lampa.SettingsApi.addParam({
        component: 'player',
        param: {
          name: 'lwp_enabled',
          type: 'trigger',
          default: true,
        },
        field: {
          name: 'Встроенный плеер (Shaka/HLS.js)',
          description: 'Включить новый встроенный плеер. Выключите для стандартного плеера Lampa.',
        },
      });
    } catch {
      // Settings API may not be available
    }
  }

  // Register setting when Lampa is ready
  if (Lampa?.Listener) {
    Lampa.Listener.follow('app', () => {
      setTimeout(registerPlayerSetting, 100);
    });
  }
  if ((window as any).appready) {
    setTimeout(registerPlayerSetting, 100);
  }

  // Check if user disabled the new player
  if (Lampa?.Storage && Lampa.Storage.get('lwp_enabled', '') === false) {
    console.log('[LWP] New player disabled by user setting');
    return;
  }

  // Pick up ?watchparty=ROOM (+ optional card/tmdb/type/title) from the
  // URL so an invite link can pre-fill the room ID AND navigate user B
  // directly to the same movie/series card in Lampa (no manual search).
  try {
    const params = new URLSearchParams(location.search);
    const wpRoom = params.get('watchparty');
    if (wpRoom) {
      (window as any).__lwpPendingWatchpartyRoom = wpRoom.substring(0, 32);

      const cardId = params.get('card') || params.get('tmdb');
      const cardType = (params.get('t') || 'movie').toLowerCase();
      const cardTitle = params.get('title') || '';
      const season = params.get('s');
      const episode = params.get('e');
      if (cardId) {
        (window as any).__lwpPendingInvite = {
          id: cardId,
          type: cardType === 'tv' ? 'tv' : 'movie',
          title: cardTitle.substring(0, 120),
          season: season ? parseInt(season, 10) || undefined : undefined,
          episode: episode ? parseInt(episode, 10) || undefined : undefined,
        };
        scheduleInviteNavigation();
      }
    }
  } catch { /* ignore */ }

  /**
   * Open the linked card in Lampa once the Activity API is ready.
   * We wait for `Lampa.Activity.push` to exist (i.e. after the core
   * components have registered) and then push the target card.
   *
   * We do NOT auto-click a Play button — the user still chooses their
   * preferred balancer and audio track. That's intentional UX: the link
   * lands them on the right content, rest stays manual.
   */
  function scheduleInviteNavigation() {
    const start = Date.now();
    const pending = (window as any).__lwpPendingInvite;
    if (!pending) return;

    const tryPush = () => {
      const L = (window as any).Lampa;
      if (!L?.Activity?.push) return false;
      try {
        L.Activity.push({
          component: 'full',
          id: pending.id,
          source: 'tmdb',
          method: pending.type,
          card: {
            id: pending.id,
            title: pending.title || '',
            original_title: pending.title || '',
            name: pending.title || '',
            original_name: pending.title || '',
            img: '',
            background_image: '',
          },
        });
        console.log('[LWP] Invite navigation: pushed card', pending);
        // Clear so we don't re-push on navigation back.
        (window as any).__lwpPendingInvite = null;
        return true;
      } catch (err) {
        console.warn('[LWP] Invite push failed:', err);
        return false;
      }
    };

    if (tryPush()) return;
    const t = setInterval(() => {
      if (tryPush() || Date.now() - start > 30_000) clearInterval(t);
    }, 300);
  }

  console.log('[LWP] Lampac Web Player initializing...');

  // Inject CSS — base styles followed by theme palette so themes override.
  const style = document.createElement('style');
  style.textContent = cssText + '\n' + themesCss;
  document.head.appendChild(style);

  // Initialize the bridge
  const bridge = new LampaBridge();
  bridge.register();

  // Watch for setting change — if user toggles mid-session, reload
  if (Lampa?.Storage?.listener) {
    Lampa.Storage.listener.follow('change', (e: any) => {
      if (e?.name === 'lwp_enabled') {
        window.location.reload();
      }
    });
  }

  console.log('[LWP] Lampac Web Player ready');
})();
