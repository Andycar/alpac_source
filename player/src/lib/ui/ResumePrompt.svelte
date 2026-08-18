<script lang="ts">
  /**
   * ResumePrompt — when a saved playback position is detected at load,
   * surface a "Continue from N:NN? / Start over" choice instead of
   * silently jumping. Shown for ~7 seconds; auto-dismisses by accepting
   * the saved position (matching prior behavior).
   *
   * Triggered by Player.svelte calling `pendingResume.set(seconds)`
   * before/after engine.load(). Only visible when seconds > 30 (anything
   * shorter is likely just a tiny offset, not worth asking about).
   */

  import { engine } from '../stores/player';
  import { pendingResume } from '../stores/resume';
  import { formatTime } from '../utils/time';

  let countdown = $state(7);
  let timer: ReturnType<typeof setInterval> | null = null;

  function start() {
    countdown = 7;
    timer && clearInterval(timer);
    timer = setInterval(() => {
      countdown -= 1;
      if (countdown <= 0) accept(); // default to "continue"
    }, 1000);
  }

  function stop() {
    if (timer) { clearInterval(timer); timer = null; }
  }

  function accept() {
    stop();
    const t = $pendingResume;
    if (t !== null && t > 0) {
      $engine?.seek(t);
    }
    pendingResume.set(null);
  }

  function startOver() {
    stop();
    $engine?.seek(0);
    pendingResume.set(null);
  }

  // React to store: start countdown when resume time appears.
  $effect(() => {
    if ($pendingResume !== null && $pendingResume > 30) {
      start();
    } else {
      stop();
    }
  });
</script>

{#if $pendingResume !== null && $pendingResume > 30}
  <div class="lwp-resume-prompt" role="dialog" aria-label="Resume playback">
    <div class="lwp-resume-text">
      Продолжить с <strong>{formatTime($pendingResume)}</strong>?
    </div>
    <div class="lwp-resume-actions">
      <button class="lwp-btn lwp-btn-primary" onclick={accept}>
        Продолжить ({countdown})
      </button>
      <button class="lwp-btn" onclick={startOver}>С начала</button>
    </div>
  </div>
{/if}
