<script lang="ts">
  /**
   * ChatSidebar — Twitch-style chat that lives on the right edge of the
   * player for the duration of the watchparty session.
   *
   * Three modes (see `chatSidebarMode` store):
   *   - 'hidden'  → only a thin "bubble" tab on the right edge; click to
   *                 expand. No rendering of messages when hidden.
   *   - 'side'    → full sidebar: title bar with room id + peers,
   *                 scrollable messages (auto-scroll on new), input at
   *                 bottom. Width 340px, height 100% of player.
   *   - 'overlay' → transparent floating messages on the right side,
   *                 no input field, pointer-events: none so controls
   *                 underneath stay clickable. Old messages fade.
   *
   * This component is mounted inside Player.svelte and always rendered.
   * When the user hasn't joined a room, chatSidebarMode is 'hidden' so
   * nothing visible. After successful join() in WatchpartyPanel the
   * mode is flipped to 'side' so the chat becomes available immediately.
   */

  import { tick } from 'svelte';
  import { get } from 'svelte/store';
  import { chatLog } from '../stores/chat';
  import { watchpartySession, watchpartySyncInstance } from '../stores/watchparty';
  import { chatSidebarMode } from '../stores/ui';
  import { nickColor } from '../utils/nickColor';
  import type { WatchpartySync } from '../engine/features/WatchpartySync';

  let chatScroll = $state<HTMLDivElement | null>(null);
  let chatInput = $state('');
  let inputEl = $state<HTMLInputElement | null>(null);

  // Auto-scroll on new message.
  $effect(() => {
    void $chatLog.length;
    if (chatScroll) {
      tick().then(() => {
        if (chatScroll) chatScroll.scrollTop = chatScroll.scrollHeight;
      });
    }
  });

  // --- Overlay mode: visible messages expire after a few seconds so the
  // chat doesn't pile up forever over the video. We compute a filtered
  // list reactively from $chatLog + time ticks.
  const OVERLAY_TTL_MS = 12_000;
  let now = $state(Date.now());
  $effect(() => {
    if ($chatSidebarMode !== 'overlay') return;
    const id = setInterval(() => { now = Date.now(); }, 1000);
    return () => clearInterval(id);
  });
  const overlayVisible = $derived.by(() => {
    return $chatLog.filter((m) => now - m.at < OVERLAY_TTL_MS).slice(-8);
  });

  function getSync(): WatchpartySync | null {
    return (get(watchpartySyncInstance) as WatchpartySync | null) ?? null;
  }

  function sendChat() {
    const s = getSync();
    if (!s) return;
    const txt = chatInput.trim();
    if (!txt) return;
    s.sendChat(txt);
    chatInput = '';
  }

  function onKeydown(e: KeyboardEvent) {
    // Guard: stop keypresses escaping into the global player handler
    // even though isTypingTarget should already cover it — belt+braces.
    e.stopPropagation();
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendChat();
    }
  }

  function setSide() {
    chatSidebarMode.set('side');
    // Focus the input so user can immediately type after switching back.
    tick().then(() => inputEl?.focus());
  }
  function setOverlay() { chatSidebarMode.set('overlay'); }
  function setHidden() { chatSidebarMode.set('hidden'); }

  function formatTime(at: number): string {
    const d = new Date(at);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
</script>

<!-- Collapsed tab (visible only when sidebar is hidden AND we're in a room) -->
{#if $watchpartySession && $chatSidebarMode === 'hidden'}
  <button
    class="lwp-chat-tab"
    onclick={setSide}
    title="Открыть чат"
    aria-label="Открыть чат"
  >
    <svg viewBox="0 0 24 24" width="20" height="20" fill="currentColor">
      <path d="M20 2H4c-1.1 0-1.99.9-1.99 2L2 22l4-4h14c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm-2 12H6v-2h12v2zm0-3H6V9h12v2zm0-3H6V6h12v2z"/>
    </svg>
    {#if $chatLog.length > 0}
      <span class="lwp-chat-tab-badge">{Math.min($chatLog.length, 99)}</span>
    {/if}
  </button>
{/if}

<!-- Side-panel mode: full chat with header/scroll/input -->
{#if $watchpartySession && $chatSidebarMode === 'side'}
  <aside class="lwp-chat-sidebar" aria-label="Watchparty chat">
    <header class="lwp-chat-sidebar-header">
      <div class="lwp-chat-sidebar-title">
        <span class="lwp-chat-room-indicator" aria-hidden="true"></span>
        <span>
          {$watchpartySession.roomId}
          <small>· {$watchpartySession.peers}</small>
        </span>
      </div>
      <div class="lwp-chat-sidebar-actions">
        <button
          class="lwp-chat-icon-btn"
          onclick={setOverlay}
          title="Прозрачный режим"
          aria-label="Прозрачный режим"
        >
          <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor">
            <path d="M12 4.5C7 4.5 2.73 7.61 1 12c1.73 4.39 6 7.5 11 7.5s9.27-3.11 11-7.5c-1.73-4.39-6-7.5-11-7.5zM12 17c-2.76 0-5-2.24-5-5s2.24-5 5-5 5 2.24 5 5-2.24 5-5 5zm0-8c-1.66 0-3 1.34-3 3s1.34 3 3 3 3-1.34 3-3-1.34-3-3-3z"/>
          </svg>
        </button>
        <button
          class="lwp-chat-icon-btn"
          onclick={setHidden}
          title="Свернуть чат"
          aria-label="Свернуть чат"
        >
          <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor">
            <path d="M9 19V5l-7 7 7 7zm2-7l7-7v14l-7-7z" transform="scale(1,1)"/>
            <path d="M14 5v14l7-7-7-7z" fill="none"/>
          </svg>
        </button>
      </div>
    </header>

    <div class="lwp-chat-sidebar-messages" bind:this={chatScroll}>
      {#if $chatLog.length === 0}
        <div class="lwp-chat-sidebar-empty">
          <div>Комната {$watchpartySession.roomId}</div>
          <small>Сообщений пока нет. Напиши первым ↓</small>
        </div>
      {:else}
        {#each $chatLog as msg (msg.id)}
          <div class="lwp-chat-sidebar-msg" class:self={msg.self}>
            <span
              class="lwp-chat-sidebar-nick"
              style="color: {nickColor(msg.nick)};"
            >{msg.nick}</span><span class="lwp-chat-sidebar-colon">:</span>
            <span class="lwp-chat-sidebar-text">{msg.text}</span>
            <time class="lwp-chat-sidebar-time">{formatTime(msg.at)}</time>
          </div>
        {/each}
      {/if}
    </div>

    <div class="lwp-chat-sidebar-input-row">
      <input
        type="text"
        class="lwp-chat-sidebar-input"
        placeholder="Сообщение…"
        bind:value={chatInput}
        bind:this={inputEl}
        onkeydown={onKeydown}
        maxlength="280"
      />
      <button
        class="lwp-chat-sidebar-send"
        onclick={sendChat}
        aria-label="Отправить"
      >
        <svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor">
          <path d="M2.01 21L23 12 2.01 3 2 10l15 2-15 2z"/>
        </svg>
      </button>
    </div>
  </aside>
{/if}

<!-- Overlay mode: transparent floating messages (Twitch theatre-mode chat) -->
{#if $watchpartySession && $chatSidebarMode === 'overlay'}
  <!-- Wrapper receives clicks only on the toggle buttons via pointer-events -->
  <div class="lwp-chat-overlay" aria-hidden="true">
    <div class="lwp-chat-overlay-messages">
      {#each overlayVisible as msg (msg.id)}
        <div class="lwp-chat-overlay-msg">
          <span
            class="lwp-chat-overlay-nick"
            style="color: {nickColor(msg.nick)};"
          >{msg.nick}</span><span class="lwp-chat-overlay-colon">:</span>
          <span class="lwp-chat-overlay-text">{msg.text}</span>
        </div>
      {/each}
    </div>
  </div>
  <!-- Tiny control to exit overlay (restore sidebar) -->
  <button
    class="lwp-chat-overlay-exit"
    onclick={setSide}
    title="Вернуть чат"
    aria-label="Вернуть чат"
  >
    <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor">
      <path d="M20 2H4c-1.1 0-1.99.9-1.99 2L2 22l4-4h14c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm-2 12H6v-2h12v2zm0-3H6V9h12v2zm0-3H6V6h12v2z"/>
    </svg>
  </button>
{/if}
