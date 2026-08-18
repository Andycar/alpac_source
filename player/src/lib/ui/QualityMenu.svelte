<script lang="ts">
  import { qualityTracks, autoQuality, engine, currentElement } from '../stores/player';
  import { activeMenu } from '../stores/ui';
  import { setQualityForSource, getQualityForSource } from '../stores/settings';
  import { autoFocusMenu } from '../utils/menu-focus';

  let { visible = false }: { visible: boolean } = $props();

  const qualityMap = $derived($currentElement?.quality || null);
  const source = $derived($currentElement?.source || '');
  const rememberedQuality = $derived(source ? getQualityForSource(source) : null);

  function selectShaka(id: number | 'auto') {
    if (id === 'auto') {
      autoQuality.set(true);
      $engine?.selectQuality('auto');
      if (source) setQualityForSource(source, 'auto');
    } else {
      autoQuality.set(false);
      $engine?.selectQuality(id);
      // Remember by height
      const track = $qualityTracks.find((t) => t.id === id);
      if (source && track) setQualityForSource(source, track.height);
    }
    activeMenu.set('none');
  }

  async function selectUrl(label: string, url: string) {
    autoQuality.set(false);
    await $engine?.switchQualityUrl(url);
    if (source) setQualityForSource(source, Number(label) || label as any);
    activeMenu.set('none');
  }
</script>

{#if visible}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="lwp-menu" use:autoFocusMenu onwheel={(e) => e.stopPropagation()}>
    <div class="lwp-menu-title">Качество{#if source}&ensp;<span class="lwp-source-badge">{source}</span>{/if}</div>

    {#if qualityMap && Object.keys(qualityMap).length > 0}
      <!-- URL-based quality map from play element -->
      {#each Object.entries(qualityMap).sort(([a], [b]) => Number(b) - Number(a)) as [label, url]}
        <div class="lwp-menu-item" tabindex="0" onclick={() => selectUrl(label, url)}>
          <span class="lwp-check">&#10003;</span>
          {label}p
        </div>
      {/each}
    {:else}
      <!-- Shaka variant tracks -->
      <div
        class="lwp-menu-item"
        class:active={$autoQuality}
        onclick={() => selectShaka('auto')}
      >
        <span class="lwp-check">&#10003;</span>
        Авто
        {#if rememberedQuality === 'auto'}<span class="lwp-remembered"></span>{/if}
      </div>
      {#each $qualityTracks as track}
        <div
          class="lwp-menu-item"
          class:active={!$autoQuality && track.active}
          onclick={() => selectShaka(track.id)}
        >
          <span class="lwp-check">&#10003;</span>
          {track.label}
          {#if rememberedQuality === track.height}<span class="lwp-remembered"></span>{/if}
        </div>
      {/each}
    {/if}
  </div>
{/if}
