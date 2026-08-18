<script lang="ts">
  let {
    title = '',
    season,
    episode,
    voiceName,
    isTranscoding = false,
    onBack,
  }: {
    title: string;
    season?: number;
    episode?: number;
    voiceName?: string;
    isTranscoding?: boolean;
    onBack: () => void;
  } = $props();

  const episodeText = $derived(
    season && episode ? `S${season}:E${episode}` : episode ? `E${episode}` : '',
  );
</script>

<div class="lwp-top-bar">
  <button class="lwp-back-btn" onclick={onBack} title="Назад (Esc)">
    <svg viewBox="0 0 24 24" width="24" height="24">
      <path d="M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z" fill="currentColor"/>
    </svg>
  </button>
  <span class="lwp-title">{title}</span>
  {#if episodeText}
    <span class="lwp-episode-info">{episodeText}</span>
  {/if}
  {#if voiceName}
    <span class="lwp-episode-info">/ {voiceName}</span>
  {/if}
  {#if isTranscoding}
    <span class="lwp-transcoding-badge">ТРАНСКОДИРОВАНИЕ</span>
  {/if}
</div>
