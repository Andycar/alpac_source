<script lang="ts">
  let {
    seekSide,
    seekSeconds,
    volumeLevel,
    brightnessLevel,
  }: {
    seekSide: 'left' | 'right' | null;
    seekSeconds: number;
    volumeLevel: number | null;
    brightnessLevel: number | null;
  } = $props();

  let seekVisible = $state(false);
  let seekTimer: ReturnType<typeof setTimeout> | null = null;
  let lastSeekSide = $state<'left' | 'right'>('right');
  let lastSeekSeconds = $state(10);

  // React to seek triggers
  $effect(() => {
    if (seekSide) {
      lastSeekSide = seekSide;
      lastSeekSeconds = seekSeconds;
      seekVisible = true;
      if (seekTimer) clearTimeout(seekTimer);
      seekTimer = setTimeout(() => {
        seekVisible = false;
      }, 600);
    }
  });
</script>

<!-- Double-tap seek indicator -->
{#if seekVisible}
  <div class="lwp-seek-indicator lwp-seek-{lastSeekSide}">
    <svg viewBox="0 0 24 24" width="28" height="28" fill="currentColor">
      {#if lastSeekSide === 'left'}
        <path d="M11.99 5V1l-5 5 5 5V7c3.31 0 6 2.69 6 6s-2.69 6-6 6-6-2.69-6-6h-2c0 4.42 3.58 8 8 8s8-3.58 8-8-3.58-8-8-8z"/>
      {:else}
        <path d="M12.01 5V1l5 5-5 5V7c-3.31 0-6 2.69-6 6s2.69 6 6 6 6-2.69 6-6h2c0 4.42-3.58 8-8 8s-8-3.58-8-8 3.58-8 8-8z"/>
      {/if}
    </svg>
    <span>{lastSeekSide === 'left' ? '-' : '+'}{lastSeekSeconds}с</span>
  </div>
{/if}

<!-- Volume indicator (vertical swipe right half) -->
{#if volumeLevel !== null}
  <div class="lwp-gesture-indicator">
    <svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor">
      {#if volumeLevel === 0}
        <path d="M16.5 12c0-1.77-1.02-3.29-2.5-4.03v2.21l2.45 2.45c.03-.2.05-.41.05-.63zm2.5 0c0 .94-.2 1.82-.54 2.64l1.51 1.51C20.63 14.91 21 13.5 21 12c0-4.28-2.99-7.86-7-8.77v2.06c2.89.86 5 3.54 5 6.71zM4.27 3L3 4.27 7.73 9H3v6h4l5 5v-6.73l4.25 4.25c-.67.52-1.42.93-2.25 1.18v2.06c1.38-.31 2.63-.95 3.69-1.81L19.73 21 21 19.73l-9-9L4.27 3zM12 4L9.91 6.09 12 8.18V4z"/>
      {:else}
        <path d="M3 9v6h4l5 5V4L7 9H3zm13.5 3c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM14 3.23v2.06c2.89.86 5 3.54 5 6.71s-2.11 5.85-5 6.71v2.06c4.01-.91 7-4.49 7-8.77s-2.99-7.86-7-8.77z"/>
      {/if}
    </svg>
    <div class="lwp-gesture-bar">
      <div class="lwp-gesture-bar-fill" style="height: {volumeLevel * 100}%"></div>
    </div>
    <span>{Math.round(volumeLevel * 100)}%</span>
  </div>
{/if}

<!-- Brightness indicator (vertical swipe left half) -->
{#if brightnessLevel !== null}
  <div class="lwp-gesture-indicator">
    <svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor">
      <path d="M20 8.69V4h-4.69L12 .69 8.69 4H4v4.69L.69 12 4 15.31V20h4.69L12 23.31 15.31 20H20v-4.69L23.31 12 20 8.69zM12 18c-3.31 0-6-2.69-6-6s2.69-6 6-6 6 2.69 6 6-2.69 6-6 6zm0-10c-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4-1.79-4-4-4z"/>
    </svg>
    <div class="lwp-gesture-bar">
      <div class="lwp-gesture-bar-fill" style="height: {Math.min(100, (brightnessLevel / 1.5) * 100)}%"></div>
    </div>
    <span>{Math.round(brightnessLevel * 100)}%</span>
  </div>
{/if}
