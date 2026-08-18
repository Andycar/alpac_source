<script lang="ts">
  import { engine } from '../stores/player';
  import { showStats } from '../stores/ui';
  import { onDestroy } from 'svelte';
  import BufferHealthGraph from './BufferHealthGraph.svelte';

  let stats = $state<Record<string, string>>({});
  let updateInterval: ReturnType<typeof setInterval> | null = null;

  $effect(() => {
    if ($showStats && $engine) {
      // Update immediately
      stats = $engine.getStats();
      // Update every second
      updateInterval = setInterval(() => {
        if ($engine) {
          stats = $engine.getStats();
        }
      }, 1000);
    } else {
      if (updateInterval) {
        clearInterval(updateInterval);
        updateInterval = null;
      }
    }
  });

  onDestroy(() => {
    if (updateInterval) clearInterval(updateInterval);
  });

  function close() {
    showStats.set(false);
  }
</script>

{#if $showStats}
  <div class="lwp-stats-overlay">
    <div class="lwp-stats-header">
      <span class="lwp-stats-title">Статистика потока</span>
      <button class="lwp-stats-close" onclick={close}>
        <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor">
          <path d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
        </svg>
      </button>
    </div>
    <div class="lwp-stats-body">
      <BufferHealthGraph />
      {#each Object.entries(stats) as [key, value]}
        <div class="lwp-stats-row">
          <span class="lwp-stats-key">{key}</span>
          <span class="lwp-stats-value">{value}</span>
        </div>
      {/each}
    </div>
  </div>
{/if}
