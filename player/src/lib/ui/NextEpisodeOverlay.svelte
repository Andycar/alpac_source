<script lang="ts">
  import { onDestroy } from 'svelte';
  import { playlist, playlistIndex } from '../stores/player';

  let {
    visible = false,
    onPlay,
    onCancel,
  }: {
    visible: boolean;
    onPlay: () => void;
    onCancel: () => void;
  } = $props();

  let countdown = $state(5);
  let countdownTimer: ReturnType<typeof setInterval> | null = null;

  const nextEpisode = $derived(() => {
    const list = $playlist;
    const idx = $playlistIndex;
    if (idx < list.length - 1) return list[idx + 1];
    return null;
  });

  $effect(() => {
    if (visible) {
      countdown = 5;
      if (countdownTimer) clearInterval(countdownTimer);
      countdownTimer = setInterval(() => {
        countdown--;
        if (countdown <= 0) {
          if (countdownTimer) clearInterval(countdownTimer);
          countdownTimer = null;
          onPlay();
        }
      }, 1000);
    } else {
      if (countdownTimer) {
        clearInterval(countdownTimer);
        countdownTimer = null;
      }
    }
  });

  onDestroy(() => {
    if (countdownTimer) clearInterval(countdownTimer);
  });

  // Countdown ring progress (SVG)
  const ringCircumference = 2 * Math.PI * 22;
  const ringOffset = $derived(ringCircumference * (1 - countdown / 5));
</script>

{#if visible && nextEpisode()}
  <div class="lwp-next-overlay">
    <div class="lwp-next-content">
      <div class="lwp-next-label">Следующая серия</div>
      <div class="lwp-next-title">
        {#if nextEpisode()!.season && nextEpisode()!.episode}
          S{nextEpisode()!.season}:E{nextEpisode()!.episode}
        {:else if nextEpisode()!.episode}
          Эпизод {nextEpisode()!.episode}
        {/if}
        {#if nextEpisode()!.title}
          <span class="lwp-next-episode-title">{nextEpisode()!.title}</span>
        {/if}
      </div>

      <div class="lwp-next-actions">
        <!-- Countdown ring + Play button -->
        <button class="lwp-next-play" onclick={onPlay}>
          <svg class="lwp-next-ring" width="52" height="52" viewBox="0 0 52 52">
            <circle cx="26" cy="26" r="22" fill="none" stroke="rgba(255,255,255,0.15)" stroke-width="3"/>
            <circle cx="26" cy="26" r="22" fill="none" stroke="#e50914" stroke-width="3"
                    stroke-dasharray={ringCircumference} stroke-dashoffset={ringOffset}
                    stroke-linecap="round" transform="rotate(-90 26 26)"
                    style="transition: stroke-dashoffset 1s linear;"/>
          </svg>
          <svg class="lwp-next-play-icon" viewBox="0 0 24 24" fill="currentColor" width="22" height="22">
            <path d="M8 5v14l11-7z"/>
          </svg>
        </button>

        <button class="lwp-next-cancel" onclick={onCancel}>
          Отмена
        </button>
      </div>

      <div class="lwp-next-countdown">через {countdown}с</div>
    </div>
  </div>
{/if}
