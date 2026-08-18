<script lang="ts">
  import { audioTracks, engine } from '../stores/player';
  import { activeMenu } from '../stores/ui';
  import { autoFocusMenu } from '../utils/menu-focus';

  let { visible = false }: { visible: boolean } = $props();

  function select(id: number) {
    $engine?.selectAudioTrack(id);
    activeMenu.set('none');
  }
</script>

{#if visible}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="lwp-menu" use:autoFocusMenu onwheel={(e) => e.stopPropagation()}>
    <div class="lwp-menu-title">Аудио</div>
    {#each $audioTracks as track}
      <div
        class="lwp-menu-item" tabindex="0"
        class:active={track.active}
        onclick={() => select(track.id)}
      >
        <span class="lwp-check">&#10003;</span>
        {track.label}
      </div>
    {/each}
    {#if $audioTracks.length === 0}
      <div class="lwp-menu-item" style="color: rgba(255,255,255,0.4)">Нет дорожек</div>
    {/if}
  </div>
{/if}
