<script lang="ts">
  /**
   * ToastHost — renders the active toasts in a top-right column.
   * Subscribes to the toasts store and animates in/out via CSS.
   */

  import { toasts, dismissToast } from '../stores/toasts';
</script>

<div class="lwp-toast-host" aria-live="polite">
  {#each $toasts as t (t.id)}
    <div
      class="lwp-toast lwp-toast-{t.kind}"
      role="status"
    >
      <span class="lwp-toast-text">{t.text}</span>
      <button
        class="lwp-toast-close"
        aria-label="Dismiss"
        onclick={() => dismissToast(t.id)}
      >×</button>
    </div>
  {/each}
</div>
