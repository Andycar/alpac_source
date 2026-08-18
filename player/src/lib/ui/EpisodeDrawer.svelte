<script lang="ts">
  import { tick } from 'svelte';
  import { playlist, playlistIndex, currentElement, currentTime, duration } from '../stores/player';
  import { loadTimelineProgress } from '../bridge/StorageSync';
  import type { PlayElement } from '../engine/PlaybackEngine';

  let {
    visible = false,
    onSelect,
    onClose,
  }: {
    visible: boolean;
    onSelect: (element: PlayElement, index: number) => void;
    onClose: () => void;
  } = $props();

  let drawerEl = $state<HTMLDivElement>(undefined!);

  // Scroll to active episode when drawer opens
  $effect(() => {
    if (visible && drawerEl) {
      tick().then(() => {
        const active = drawerEl.querySelector('.lwp-episode-item.active');
        if (active) {
          active.scrollIntoView({ block: 'center', behavior: 'instant' });
        }
      });
    }
  });

  /**
   * Per-episode progress fraction (0..1). For the actively playing
   * episode we use the live currentTime/duration so the bar grows during
   * playback. For others, read from saved timeline storage.
   */
  function progressFor(ep: PlayElement, idx: number): number {
    if (idx === $playlistIndex && $duration > 0) {
      return Math.min(1, $currentTime / $duration);
    }
    return loadTimelineProgress(ep.url);
  }

  // Group episodes by season if mixed seasons exist
  const groupedBySeason = $derived(() => {
    const list = $playlist;
    if (list.length === 0) return null;
    const seasons = new Set(list.map((ep) => ep.season).filter(Boolean));
    if (seasons.size <= 1) return null;
    const groups: Map<number, { episodes: PlayElement[]; indices: number[] }> = new Map();
    list.forEach((ep, i) => {
      const s = ep.season || 0;
      if (!groups.has(s)) groups.set(s, { episodes: [], indices: [] });
      groups.get(s)!.episodes.push(ep);
      groups.get(s)!.indices.push(i);
    });
    return groups;
  });
</script>

{#if visible}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="lwp-drawer-backdrop" onclick={onClose}></div>
  <div class="lwp-episodes" bind:this={drawerEl}>
    <div class="lwp-episodes-header">
      <span class="lwp-episodes-title">Эпизоды</span>
      <button class="lwp-btn" onclick={onClose} style="min-width: 32px; min-height: 32px; padding: 4px;">
        <svg viewBox="0 0 24 24" width="20" height="20" fill="currentColor">
          <path d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
        </svg>
      </button>
    </div>

    {#if groupedBySeason()}
      {#each [...groupedBySeason()!.entries()].sort(([a], [b]) => a - b) as [season, group]}
        <div class="lwp-season-header">Сезон {season}</div>
        {#each group.episodes as ep, j}
          {@const globalIdx = group.indices[j]}
          <div
            class="lwp-episode-item" tabindex="0"
            class:active={globalIdx === $playlistIndex}
            onclick={() => onSelect(ep, globalIdx)}
          >
            {#if ep.episode}
              E{ep.episode}
            {:else}
              {ep.title || `Эпизод ${j + 1}`}
            {/if}
            {#if ep.title && ep.episode}
              <span class="lwp-episode-subtitle">{ep.title}</span>
            {/if}
            {#if progressFor(ep, globalIdx) > 0.01}
              {@const p = progressFor(ep, globalIdx)}
              <div class="lwp-episode-progress">
                <div class="lwp-episode-progress-fill" style="width: {p * 100}%"></div>
                {#if p > 0.95}<span class="lwp-episode-watched-dot" aria-label="watched"></span>{/if}
              </div>
            {/if}
          </div>
        {/each}
      {/each}
    {:else}
      {#each $playlist as ep, i}
        <div
          class="lwp-episode-item" tabindex="0"
          class:active={i === $playlistIndex}
          onclick={() => onSelect(ep, i)}
        >
          {#if ep.season && ep.episode}
            S{ep.season}:E{ep.episode}
          {:else if ep.episode}
            Эпизод {ep.episode}
          {:else}
            {ep.title || `Эпизод ${i + 1}`}
          {/if}
          {#if ep.title && ep.season}
            <span class="lwp-episode-subtitle">{ep.title}</span>
          {/if}
          {#if progressFor(ep, i) > 0.01}
            {@const p = progressFor(ep, i)}
            <div class="lwp-episode-progress">
              <div class="lwp-episode-progress-fill" style="width: {p * 100}%"></div>
              {#if p > 0.95}<span class="lwp-episode-watched-dot" aria-label="watched"></span>{/if}
            </div>
          {/if}
        </div>
      {/each}
    {/if}
  </div>
{/if}
