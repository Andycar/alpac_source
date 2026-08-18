<script lang="ts">
  import { formatTime } from '../utils/time';
  import {
    engine,
    playing,
    currentTime,
    duration,
    volume as volumeStore,
    muted as mutedStore,
    qualityTracks,
    audioTracks,
    textTracks,
    hasNext,
    hasPrev,
    currentElement,
    castAvailable,
    castConnected,
    playlist,
  } from '../stores/player';
  import {
    activeMenu,
    anyMenuOpen,
    toggleMenu,
    isFullscreen,
    toggleFullscreen,
    resetIdleTimer,
  } from '../stores/ui';
  import Timeline from './Timeline.svelte';
  import QualityMenu from './QualityMenu.svelte';
  import AudioMenu from './AudioMenu.svelte';
  import SubtitleMenu from './SubtitleMenu.svelte';
  import SettingsMenu from './SettingsMenu.svelte';
  import VoiceMenu from './VoiceMenu.svelte';

  let {
    container,
    onPrev,
    onNext,
    onVoiceSelect,
  }: {
    container: HTMLElement;
    onPrev: () => void;
    onNext: () => void;
    onVoiceSelect: (voice: { name: string; index: number }) => void;
  } = $props();

  function togglePlay() {
    if ($engine) {
      $engine.paused ? $engine.play() : $engine.pause();
    }
  }

  function toggleMute() {
    if ($engine) {
      $engine.muted = !$engine.muted;
      mutedStore.set($engine.muted);
    }
  }

  function onVolumeInput(e: Event) {
    const val = parseFloat((e.target as HTMLInputElement).value);
    if ($engine) {
      $engine.volume = val;
      volumeStore.set(val);
    }
  }

  function onCast() {
    const vid = document.querySelector('.lwp-video') as HTMLVideoElement;
    if (!vid) return;
    // Safari AirPlay
    if ('webkitShowPlaybackTargetPicker' in vid) {
      (vid as any).webkitShowPlaybackTargetPicker();
      return;
    }
    // Remote Playback API (Chrome, etc.)
    if ('remote' in vid) {
      (vid as any).remote.prompt().catch(() => {});
    }
  }

  const activeQualityLabel = $derived(() => {
    const tracks = $qualityTracks;
    const active = tracks.find((t) => t.active);
    return active?.label || 'Авто';
  });

  const hasVoices = $derived(($currentElement?.voices?.length || 0) > 1);

  function closeMenu() {
    activeMenu.set('none');
    resetIdleTimer();
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- Stop touch events from bubbling to gesture handler on video container -->
<div class="lwp-bottom" ontouchstart={(e) => e.stopPropagation()} ontouchend={(e) => e.stopPropagation()}>
  <Timeline />

  <div class="lwp-controls-row">
    <div class="lwp-controls-left">
      <!-- Play/Pause -->
      <button class="lwp-btn" onclick={togglePlay} title={$playing ? 'Пауза (K)' : 'Воспроизвести (K)'}>
        {#if $playing}
          <svg viewBox="0 0 24 24"><path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z"/></svg>
        {:else}
          <svg viewBox="0 0 24 24"><path d="M8 5v14l11-7z"/></svg>
        {/if}
      </button>

      <!-- Prev -->
      {#if $hasPrev}
        <button class="lwp-btn" onclick={onPrev} title="Предыдущая">
          <svg viewBox="0 0 24 24"><path d="M6 6h2v12H6zm3.5 6l8.5 6V6z"/></svg>
        </button>
      {/if}

      <!-- Next -->
      {#if $hasNext}
        <button class="lwp-btn" onclick={onNext} title="Следующая (N)">
          <svg viewBox="0 0 24 24"><path d="M6 18l8.5-6L6 6v12zM16 6v12h2V6h-2z"/></svg>
        </button>
      {/if}

      <!-- Volume -->
      <div class="lwp-volume-group">
        <button class="lwp-btn" onclick={toggleMute} title="Звук (M)">
          {#if $mutedStore || $volumeStore === 0}
            <svg viewBox="0 0 24 24"><path d="M16.5 12c0-1.77-1.02-3.29-2.5-4.03v2.21l2.45 2.45c.03-.2.05-.41.05-.63zm2.5 0c0 .94-.2 1.82-.54 2.64l1.51 1.51C20.63 14.91 21 13.5 21 12c0-4.28-2.99-7.86-7-8.77v2.06c2.89.86 5 3.54 5 6.71zM4.27 3L3 4.27 7.73 9H3v6h4l5 5v-6.73l4.25 4.25c-.67.52-1.42.93-2.25 1.18v2.06c1.38-.31 2.63-.95 3.69-1.81L19.73 21 21 19.73l-9-9L4.27 3zM12 4L9.91 6.09 12 8.18V4z"/></svg>
          {:else if $volumeStore < 0.5}
            <svg viewBox="0 0 24 24"><path d="M18.5 12c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM5 9v6h4l5 5V4L9 9H5z"/></svg>
          {:else}
            <svg viewBox="0 0 24 24"><path d="M3 9v6h4l5 5V4L7 9H3zm13.5 3c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM14 3.23v2.06c2.89.86 5 3.54 5 6.71s-2.11 5.85-5 6.71v2.06c4.01-.91 7-4.49 7-8.77s-2.99-7.86-7-8.77z"/></svg>
          {/if}
        </button>
        <input
          class="lwp-volume-slider"
          type="range"
          min="0"
          max="1"
          step="0.01"
          value={$volumeStore}
          oninput={onVolumeInput}
        />
      </div>

      <!-- Time -->
      <span class="lwp-time">
        {formatTime($currentTime)} / {formatTime($duration)}
      </span>
    </div>

    <div class="lwp-controls-center"></div>

    <div class="lwp-controls-right">
      <!-- Voices -->
      {#if hasVoices}
        <button class="lwp-btn" onclick={() => toggleMenu('voices')} title="Озвучка">
          <svg viewBox="0 0 24 24"><path d="M20 2H4c-1.1 0-2 .9-2 2v18l4-4h14c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm0 14H6l-2 2V4h16v12z"/></svg>
        </button>
      {/if}

      <!-- Subtitles -->
      {#if $textTracks.length > 0 || ($currentElement?.subtitles?.length ?? 0) > 0}
        <button class="lwp-btn" onclick={() => toggleMenu('subtitles')} title="Субтитры (C)">
          <svg viewBox="0 0 24 24"><path d="M20 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2zm0 14H4V6h16v12zM6 10h2v2H6v-2zm0 4h8v2H6v-2zm10 0h2v2h-2v-2zm-6-4h8v2h-8v-2z"/></svg>
        </button>
      {/if}

      <!-- Audio -->
      {#if $audioTracks.length > 1}
        <button class="lwp-btn" onclick={() => toggleMenu('audio')} title="Аудио">
          <svg viewBox="0 0 24 24"><path d="M12 3v9.28c-.47-.17-.97-.28-1.5-.28C8.01 12 6 14.01 6 16.5S8.01 21 10.5 21c2.31 0 4.2-1.75 4.45-4H15V6h4V3h-7z"/></svg>
        </button>
      {/if}

      <!-- Quality -->
      <button
        class="lwp-quality-badge"
        onclick={() => toggleMenu('quality')}
        title="Качество"
      >
        {activeQualityLabel()}
      </button>

      <!-- Episodes (only show when there are 2+ items in playlist) -->
      {#if $playlist.length > 1}
        <button class="lwp-btn" onclick={() => toggleMenu('episodes')} title="Эпизоды">
          <svg viewBox="0 0 24 24"><path d="M4 6h16v2H4zm0 5h16v2H4zm0 5h16v2H4z"/></svg>
        </button>
      {/if}

      <!-- Settings -->
      <button class="lwp-btn" onclick={() => toggleMenu('settings')} title="Настройки">
        <svg viewBox="0 0 24 24"><path d="M19.14 12.94c.04-.3.06-.61.06-.94 0-.32-.02-.64-.07-.94l2.03-1.58a.49.49 0 00.12-.61l-1.92-3.32a.488.488 0 00-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54a.484.484 0 00-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96c-.22-.08-.47 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.05.3-.07.62-.07.94s.02.64.07.94l-2.03 1.58a.49.49 0 00-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6c-1.98 0-3.6-1.62-3.6-3.6s1.62-3.6 3.6-3.6 3.6 1.62 3.6 3.6-1.62 3.6-3.6 3.6z"/></svg>
      </button>

      <!-- Cast -->
      {#if $castAvailable}
        <button
          class="lwp-btn"
          class:lwp-cast-active={$castConnected}
          onclick={onCast}
          title="Транслировать"
        >
          <svg viewBox="0 0 24 24"><path d="M21 3H3c-1.1 0-2 .9-2 2v3h2V5h18v14h-7v2h7c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zM1 18v3h3c0-1.66-1.34-3-3-3zm0-4v2c2.76 0 5 2.24 5 5h2c0-3.87-3.13-7-7-7zm0-4v2c4.97 0 9 4.03 9 9h2c0-6.08-4.93-11-11-11z"/></svg>
        </button>
      {/if}

      <!-- Fullscreen -->
      <button class="lwp-btn" onclick={() => toggleFullscreen(container)} title="Полный экран (F)">
        {#if $isFullscreen}
          <svg viewBox="0 0 24 24"><path d="M5 16h3v3h2v-5H5v2zm3-8H5v2h5V5H8v3zm6 11h2v-3h3v-2h-5v5zm2-11V5h-2v5h5V8h-3z"/></svg>
        {:else}
          <svg viewBox="0 0 24 24"><path d="M7 14H5v5h5v-2H7v-3zm-2-4h2V7h3V5H5v5zm12 7h-3v2h5v-5h-2v3zM14 5v2h3v3h2V5h-5z"/></svg>
        {/if}
      </button>
    </div>
  </div>

  <!-- Menus (inside lwp-bottom so they position relative to controls) -->
  {#if $activeMenu === 'quality'}
    <QualityMenu visible={true} />
  {:else if $activeMenu === 'audio'}
    <AudioMenu visible={true} />
  {:else if $activeMenu === 'subtitles'}
    <SubtitleMenu visible={true} />
  {:else if $activeMenu === 'settings'}
    <SettingsMenu visible={true} />
  {:else if $activeMenu === 'voices'}
    <VoiceMenu visible={true} {onVoiceSelect} />
  {/if}

  </div>
