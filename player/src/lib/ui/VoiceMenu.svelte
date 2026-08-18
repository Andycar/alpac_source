<script lang="ts">
  import { currentElement } from '../stores/player';
  import { activeMenu } from '../stores/ui';
  import { autoFocusMenu } from '../utils/menu-focus';

  let {
    visible = false,
    onVoiceSelect,
  }: {
    visible: boolean;
    onVoiceSelect: (voice: { name: string; index: number }) => void;
  } = $props();

  const voices = $derived($currentElement?.voices || []);
  const currentIndex = $derived($currentElement?.voice_index ?? 0);
</script>

{#if visible}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="lwp-menu" use:autoFocusMenu onwheel={(e) => e.stopPropagation()}>
    <div class="lwp-menu-title">Озвучка</div>
    {#each voices as voice}
      <div
        class="lwp-menu-item" tabindex="0"
        class:active={voice.index === currentIndex}
        onclick={() => { onVoiceSelect(voice); activeMenu.set('none'); }}
      >
        <span class="lwp-check">&#10003;</span>
        {voice.name}
      </div>
    {/each}
    {#if voices.length === 0}
      <div class="lwp-menu-item" tabindex="0" style="color: rgba(255,255,255,0.4)">Нет озвучек</div>
    {/if}
  </div>
{/if}
