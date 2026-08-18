<script lang="ts">
  import { textTracks, engine } from '../stores/player';
  import { activeMenu } from '../stores/ui';
  import { autoFocusMenu } from '../utils/menu-focus';

  let { visible = false }: { visible: boolean } = $props();

  let subsOff = $state(true);

  function select(id: number) {
    $engine?.selectTextTrack(id);
    subsOff = false;
    activeMenu.set('none');
  }

  function turnOff() {
    $engine?.selectTextTrack('off');
    subsOff = true;
    activeMenu.set('none');
  }
</script>

{#if visible}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="lwp-menu" use:autoFocusMenu onwheel={(e) => e.stopPropagation()}>
    <div class="lwp-menu-title">Субтитры</div>
    <div
      class="lwp-menu-item" tabindex="0"
      class:active={subsOff}
      onclick={turnOff}
    >
      <span class="lwp-check">&#10003;</span>
      Выкл
    </div>
    {#each $textTracks as track}
      <div
        class="lwp-menu-item" tabindex="0"
        class:active={!subsOff && track.active}
        onclick={() => select(track.id)}
      >
        <span class="lwp-check">&#10003;</span>
        {track.label}
      </div>
    {/each}
  </div>
{/if}
