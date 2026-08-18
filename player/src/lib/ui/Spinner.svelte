<script lang="ts">
  let { visible = false }: { visible: boolean } = $props();
</script>

{#if visible}
  <div class="lwp-spinner">
    <div class="lwp-spinner-ring lwp-spinner-ring-1"></div>
    <div class="lwp-spinner-ring lwp-spinner-ring-2"></div>
    <div class="lwp-spinner-ring lwp-spinner-ring-3"></div>
  </div>
{/if}
