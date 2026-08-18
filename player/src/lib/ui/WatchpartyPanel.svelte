<script lang="ts">
  /**
   * WatchpartyPanel — tiny UI for joining a watch-party room.
   *
   * For full UX (chat, voice, polished invites) more work is needed, but
   * this panel covers the core flow:
   *   - generate / paste a room id
   *   - join → shows peer count + room URL
   *   - leave → tears down the WS
   *
   * Wave 2 C2. Server endpoint required:
   *   GET /webplayer/ws/watchparty/:roomId  (WebSocket relay)
   */

  import { engine } from '../stores/player';
  import { watchpartySession, watchpartySyncInstance } from '../stores/watchparty';
  import { chatLog } from '../stores/chat';
  import { WatchpartySync } from '../engine/features/WatchpartySync';
  import { pushToast } from '../stores/toasts';
  import { activeMenu, chatSidebarMode } from '../stores/ui';
  import { tick } from 'svelte';
  import { get } from 'svelte/store';

  // sync lives in a module-level store so the panel can be closed and
  // re-opened without losing the reference to the connected WebSocket.
  // Pre-fill from ?watchparty=ROOM deep link (set by main.ts at plugin
   // load). If absent, generate a fresh id so user A can start a room.
   let roomInput = $state(
    (typeof window !== 'undefined'
      ? ((window as any).__lwpPendingWatchpartyRoom as string | undefined)
      : undefined) || generateRoomId(),
  );
  let nickInput = $state(`viewer-${Math.floor(Math.random() * 1000)}`);
  let connecting = $state(false);
  let lastError = $state('');
  let chatInput = $state('');
  let chatScroll = $state<HTMLDivElement | null>(null);

  /** Typed accessor for the module-level sync instance. */
  function getSync(): WatchpartySync | null {
    return (get(watchpartySyncInstance) as WatchpartySync | null) ?? null;
  }

  // Auto-scroll chat to bottom on new message.
  $effect(() => {
    void $chatLog.length;
    if (chatScroll) {
      tick().then(() => {
        if (chatScroll) chatScroll.scrollTop = chatScroll.scrollHeight;
      });
    }
  });

  function sendChat() {
    const s = getSync();
    if (!s) return;
    const txt = chatInput.trim();
    if (!txt) return;
    s.sendChat(txt);
    chatInput = '';
  }
  function onChatKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendChat();
    }
  }
  function relTime(at: number): string {
    const d = new Date(at);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  /**
   * Build the WebSocket URL for the relay endpoint. Uses the lampac
   * server origin captured at script load (see main.ts) so it works
   * even when Lampa is hosted on a different domain than lampac.
   * Falls back to current page origin if the capture failed.
   */
  function buildWsUrl(roomId: string): string {
    const captured = (window as any).__lwpServerOrigin as string | undefined;
    let httpOrigin: string;
    try {
      httpOrigin = captured ? new URL(captured).origin : location.origin;
    } catch {
      httpOrigin = location.origin;
    }
    // Replace just "http" with "ws" — the trailing "s" (if any) stays,
    // so https://… → wss://…, http://… → ws://…
    const wsOrigin = httpOrigin.replace(/^http/i, 'ws');
    return `${wsOrigin}/webplayer/ws/watchparty/${encodeURIComponent(roomId)}`;
  }

  /** Returns the HTTP origin we use for both the health probe and the WS. */
  function serverOrigin(): string {
    const captured = (window as any).__lwpServerOrigin as string | undefined;
    try {
      return captured ? new URL(captured).origin : location.origin;
    } catch {
      return location.origin;
    }
  }

  /**
   * HTTP pre-flight — hits `/webplayer/health/watchparty`. If the server
   * responds 200 → route exists, and any subsequent WS failure is a
   * reverse-proxy / firewall issue (most likely nginx not forwarding the
   * Upgrade header). If it 404s → deployed binary is too old.
   */
  async function checkServerSupport(): Promise<string | null> {
    const url = `${serverOrigin()}/webplayer/health/watchparty`;
    try {
      const resp = await fetch(url, {
        method: 'GET',
        cache: 'no-store',
        signal: AbortSignal.timeout(5000),
      });
      if (resp.status === 404) {
        return 'Сервер не обновлён — нет маршрута watchparty. Обновите lampac-go до сегодняшней сборки.';
      }
      if (!resp.ok) {
        return `Сервер ответил ${resp.status} на health-проверку watchparty.`;
      }
      return null;
    } catch (err: any) {
      return `Не удалось достучаться до ${url}: ${err?.message || err}`;
    }
  }

  async function join() {
    if (getSync()) return;
    const eng = get(engine);
    if (!eng) {
      lastError = 'Плеер не инициализирован';
      return;
    }
    if (!roomInput.trim()) {
      lastError = 'Укажите ID комнаты';
      return;
    }
    connecting = true;
    lastError = '';
    let candidate: WatchpartySync | null = null;
    try {
      // Pre-flight: HTTP probe → WS. If probe fails we tell the user
      // the concrete reason instead of a vague "WS failed".
      const probeErr = await checkServerSupport();
      if (probeErr) throw new Error(probeErr);

      candidate = new WatchpartySync(eng, {
        wsUrl: buildWsUrl(roomInput.trim()),
        roomId: roomInput.trim(),
        nick: nickInput.trim(),
      });
      try {
        await candidate.connect();
      } catch (wsErr: any) {
        throw new Error(
          `${wsErr?.message || wsErr}\n\nВозможно, прокси перед lampac не пропускает WebSocket Upgrade. Для nginx добавьте:\n  proxy_http_version 1.1;\n  proxy_set_header Upgrade $http_upgrade;\n  proxy_set_header Connection "upgrade";`,
        );
      }
      watchpartySyncInstance.set(candidate);
      pushToast(`Подключено к комнате «${roomInput.trim()}»`, { kind: 'success' });
      // Close this modal and open the Twitch-style chat sidebar.
      activeMenu.set('none');
      chatSidebarMode.set('side');
    } catch (err: any) {
      lastError = err?.message || 'Не удалось подключиться';
      pushToast('Не удалось подключиться к watchparty', { kind: 'error' });
      candidate?.destroy();
    } finally {
      connecting = false;
    }
  }

  async function leave() {
    const s = getSync();
    // Always clear the session/store even if `s` somehow got detached —
    // otherwise the panel gets stuck in the "connected" branch with no
    // way out.
    if (s) {
      try { await s.leave(); } catch { /* ignore */ }
      s.destroy();
    }
    watchpartySyncInstance.set(null);
    watchpartySession.set(null);
    chatSidebarMode.set('hidden');
    pushToast('Вы покинули комнату', { kind: 'info' });
  }

  /**
   * Best-effort snapshot of the currently-opened Lampa card so the
   * invite link can navigate user B straight to the same movie/series.
   *
   * Lampa exposes `Lampa.Activity.active()` which returns the top-of-
   * stack activity. Its shape varies between plugin and card view, but
   * typically includes `component` + `card` + `method`.
   */
  function captureLampaCard(): { id: string; type: 'movie' | 'tv'; title: string } | null {
    const L: any = (window as any).Lampa;
    const activity = L?.Activity?.active?.();
    if (!activity) return null;
    const card = activity.card || activity.movie;
    if (!card) return null;
    const id = card.id || card.tmdb_id;
    if (!id) return null;
    const method: string =
      activity.method ||
      card.type ||
      (card.number_of_seasons || card.seasons ? 'tv' : 'movie');
    const title: string = card.title || card.name || card.original_title || card.original_name || '';
    return {
      id: String(id),
      type: method === 'tv' || method === 'serial' ? 'tv' : 'movie',
      title: String(title).substring(0, 120),
    };
  }

  function copyRoomLink() {
    const room = roomInput.trim();
    if (!room) return;

    let shareUrl: string;
    try {
      const u = new URL(location.href);
      u.searchParams.set('watchparty', room);
      // Attach card info so clicking the link lands user B directly on
      // the right movie/series card.
      const card = captureLampaCard();
      if (card) {
        u.searchParams.set('card', card.id);
        u.searchParams.set('t', card.type);
        if (card.title) u.searchParams.set('title', card.title);
      }
      // Optional: pass current season/episode if we're in a series player.
      const sess = get(watchpartySession);
      if (sess) { /* nothing extra today */ }
      shareUrl = u.toString();
    } catch {
      shareUrl = `${location.origin}${location.pathname}?watchparty=${encodeURIComponent(room)}`;
    }

    try {
      navigator.clipboard.writeText(shareUrl).then(
        () => pushToast('Ссылка скопирована — отправь другу', { kind: 'success', ttl: 2000 }),
        () => pushToast('Скопируй вручную: ' + shareUrl, { kind: 'warn', ttl: 5000 }),
      );
    } catch {
      pushToast('Скопируй вручную: ' + shareUrl, { kind: 'info', ttl: 5000 });
    }
  }

  function generateRoomId(): string {
    return Math.random().toString(36).substring(2, 8);
  }

  // Do NOT destroy sync on component unmount — user might just be
  // closing the panel with × while staying in the room. Cleanup happens
  // only on explicit leave() or when Player.svelte tears down.
</script>

<div class="lwp-watchparty">
  {#if !$watchpartySession}
    <div class="lwp-watchparty-form">
      <label class="lwp-watchparty-label">
        ID комнаты
        <input
          type="text"
          bind:value={roomInput}
          maxlength="32"
          placeholder="например, friends42"
          disabled={connecting}
        />
      </label>
      <label class="lwp-watchparty-label">
        Ник
        <input
          type="text"
          bind:value={nickInput}
          maxlength="20"
          placeholder="как тебя видят другие"
          disabled={connecting}
        />
      </label>
      <button
        class="lwp-btn lwp-btn-primary"
        onclick={join}
        disabled={connecting}
      >
        {connecting ? 'Подключаюсь...' : 'Войти в комнату'}
      </button>
      {#if lastError}
        <div class="lwp-watchparty-error">{lastError}</div>
      {/if}
    </div>
  {:else}
    <div class="lwp-watchparty-active">
      <div class="lwp-watchparty-row">
        <span>Комната</span>
        <strong>{$watchpartySession.roomId}</strong>
      </div>
      <div class="lwp-watchparty-row">
        <span>Ник</span>
        <strong>{$watchpartySession.nick}</strong>
      </div>
      <div class="lwp-watchparty-row">
        <span>Пиров</span>
        <strong>{$watchpartySession.peers}</strong>
      </div>
      <div class="lwp-watchparty-actions">
        <button class="lwp-btn" onclick={copyRoomLink}>Скопировать ссылку</button>
        <button class="lwp-btn lwp-btn-danger" onclick={leave}>Выйти</button>
      </div>
      <div class="lwp-watchparty-hint">
        Чат в боковой панели справа.
        Можешь переключить на прозрачные всплывающие сообщения —
        значок «глаз» в заголовке панели.
      </div>
    </div>
  {/if}
</div>
