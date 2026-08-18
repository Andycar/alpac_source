<script lang="ts">
  /**
   * AmbilightLayer — samples the four edges of the active video every
   * ~500ms and exposes them as CSS custom properties so the layer's
   * gradient halos and edge-glow effects pick up the dominant scene
   * colors. Inspired by Philips Ambilight TVs.
   *
   * Performance: one 32×18 canvas read every 500ms. Costs <1ms on
   * desktop. Disabled automatically when:
   *   - settings.ambilightMode === 'off'
   *   - document is hidden (tab in background)
   *   - isLowPowerTV() returns true
   *   - the canvas read throws (CORS-tainted DRM stream)
   */

  import { onMount, onDestroy } from 'svelte';
  import { extractEdgeColors, lerpEdgeColors, type EdgeColors } from '../utils/colorExtract';
  import { ambilightColors } from '../stores/ambilight';
  import { settings } from '../stores/settings';
  import { isLowPowerTV } from '../bridge/PlatformDetect';

  let { videoEl }: { videoEl: HTMLVideoElement } = $props();

  let layerEl = $state<HTMLDivElement>(undefined!);
  let scratch: HTMLCanvasElement | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;
  let lastColors: EdgeColors | null = null;
  let disabled = false;
  // Track recent readback durations — if the GPU sync is consistently
  // slow (>20ms per sample), we self-disable so the player decode
  // pipeline isn't starved. The readback can be slow on some GPUs /
  // protected streams even though they aren't fully CORS-tainted.
  let slowSamples = 0;

  // 1500ms is enough for the visual effect (LERP smooths intermediates)
  // and gives 3x less GPU contention than the original 500ms.
  const SAMPLE_INTERVAL = 1500;
  const LERP_ALPHA = 0.30;
  const SLOW_THRESHOLD_MS = 20;
  const MAX_SLOW_BEFORE_DISABLE = 3;

  function shouldRun(): boolean {
    if (disabled) return false;
    if (typeof document === 'undefined' || document.hidden) return false;
    if (isLowPowerTV()) return false;
    const mode = $settings.ambilightMode;
    if (mode === 'off') return false;
    if (!videoEl || videoEl.paused || videoEl.readyState < 2) return false;
    if (!videoEl.videoWidth || !videoEl.videoHeight) return false;
    return true;
  }

  function tick() {
    if (!shouldRun()) return;
    if (!scratch) scratch = document.createElement('canvas');
    let next: EdgeColors;
    const t0 = performance.now();
    try {
      next = extractEdgeColors(videoEl, scratch);
    } catch (err) {
      // CORS-tainted (DRM) — disable for this session, never throws again.
      disabled = true;
      console.log('[LWP] Ambilight disabled: canvas tainted (DRM/cross-origin)');
      return;
    }
    const dt = performance.now() - t0;
    if (dt > SLOW_THRESHOLD_MS) {
      slowSamples++;
      if (slowSamples >= MAX_SLOW_BEFORE_DISABLE) {
        disabled = true;
        console.log(`[LWP] Ambilight self-disabled: GPU readback too slow (${dt.toFixed(1)}ms)`);
        clearVisuals();
        return;
      }
    } else if (slowSamples > 0) {
      // Decay the slow counter on a fast sample so transient glitches
      // don't permanently disable the feature.
      slowSamples = Math.max(0, slowSamples - 1);
    }
    const smoothed = lastColors ? lerpEdgeColors(lastColors, next, LERP_ALPHA) : next;
    lastColors = smoothed;
    ambilightColors.set(smoothed);
    applyToLayer(smoothed);
  }

  function applyToLayer(c: EdgeColors) {
    if (!layerEl) return;
    const el = layerEl;
    el.style.setProperty('--lwp-amb-top', c.top);
    el.style.setProperty('--lwp-amb-right', c.right);
    el.style.setProperty('--lwp-amb-bottom', c.bottom);
    el.style.setProperty('--lwp-amb-left', c.left);
    el.style.setProperty('--lwp-amb-accent', c.accent);
  }

  function clearVisuals() {
    if (!layerEl) return;
    layerEl.style.removeProperty('--lwp-amb-top');
    layerEl.style.removeProperty('--lwp-amb-right');
    layerEl.style.removeProperty('--lwp-amb-bottom');
    layerEl.style.removeProperty('--lwp-amb-left');
    layerEl.style.removeProperty('--lwp-amb-accent');
    lastColors = null;
  }

  onMount(() => {
    timer = setInterval(tick, SAMPLE_INTERVAL);
    // Pause sampling on visibility change to save CPU when tab inactive.
    const vis = () => {
      if (document.hidden) clearVisuals();
    };
    document.addEventListener('visibilitychange', vis);
    return () => {
      document.removeEventListener('visibilitychange', vis);
    };
  });

  onDestroy(() => {
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
    scratch = null;
    lastColors = null;
  });

  // React to settings.ambilightMode changes.
  $effect(() => {
    void $settings.ambilightMode;
    if ($settings.ambilightMode === 'off') {
      clearVisuals();
    }
  });
</script>

<div
  class="lwp-ambilight"
  class:lwp-ambilight-subtle={$settings.ambilightMode === 'subtle'}
  class:lwp-ambilight-rich={$settings.ambilightMode === 'rich'}
  class:lwp-ambilight-edge-glow={$settings.edgeGlow}
  bind:this={layerEl}
  aria-hidden="true"
></div>
