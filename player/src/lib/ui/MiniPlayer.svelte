<script lang="ts">
  /**
   * MiniPlayer — draggable floating video for "keep watching while
   * browsing" UX (Wave 1 D3).
   *
   * Two backends:
   *  1. Document Picture-in-Picture API (Chromium 116+) — opens an
   *     OS-level always-on-top window with our own UI inside.
   *  2. Floating fallback — moves the active <video> into a fixed-position
   *     draggable corner pane inside the same document. Works everywhere.
   *
   * Triggered by Player.svelte calling `enterMiniPlayer()` (we expose it
   * via a global hook on window for now — kept simple).
   */

  import { onDestroy } from 'svelte';
  import { miniPlayerMode, miniPlayerPosition } from '../stores/ui';
  import { engine } from '../stores/player';
  import { get } from 'svelte/store';

  let floatingEl = $state<HTMLDivElement | null>(null);
  let videoSlot = $state<HTMLDivElement | null>(null);
  let dragOffset = { x: 0, y: 0 };
  let dragging = false;
  let docPipWindow: any = null;
  let restoreParent: HTMLElement | null = null;
  let restoreNextSibling: Node | null = null;
  let restoreVideo: HTMLVideoElement | null = null;

  /** Enter mini-player mode. Tries DocPiP, falls back to floating. */
  export async function enter(): Promise<boolean> {
    const eng = get(engine);
    if (!eng) return false;
    const video = eng.video;
    if (!video) return false;

    // Remember where the video lived so we can put it back.
    restoreParent = video.parentElement;
    restoreNextSibling = video.nextSibling;
    restoreVideo = video;

    // Try Document PiP first (Chromium 116+).
    const dpip: any = (window as any).documentPictureInPicture;
    if (dpip?.requestWindow) {
      try {
        docPipWindow = await dpip.requestWindow({ width: 480, height: 270 });
        // Copy minimal styles into the PiP window so the video fills it.
        const styleEl = docPipWindow.document.createElement('style');
        styleEl.textContent = `
          html, body { margin: 0; padding: 0; background: #000; height: 100%; overflow: hidden; }
          video { width: 100%; height: 100%; object-fit: contain; background: #000; }
        `;
        docPipWindow.document.head.appendChild(styleEl);
        docPipWindow.document.body.appendChild(video);
        docPipWindow.addEventListener('pagehide', () => {
          // User closed the PiP window — restore.
          exit();
        });
        miniPlayerMode.set(true);
        return true;
      } catch (err) {
        console.warn('[LWP] DocPiP failed, falling back to floating:', err);
      }
    }

    // Floating fallback.
    if (videoSlot && video.parentElement !== videoSlot) {
      videoSlot.appendChild(video);
    }
    miniPlayerMode.set(true);
    return true;
  }

  /** Leave mini-player mode and restore the video to its original spot. */
  export function exit(): void {
    if (docPipWindow) {
      try {
        if (restoreVideo && restoreParent) {
          restoreParent.insertBefore(restoreVideo, restoreNextSibling);
        }
        docPipWindow.close();
      } catch { /* ignore */ }
      docPipWindow = null;
    } else if (restoreVideo && restoreParent) {
      restoreParent.insertBefore(restoreVideo, restoreNextSibling);
    }
    restoreVideo = null;
    restoreParent = null;
    restoreNextSibling = null;
    miniPlayerMode.set(false);
  }

  // --- Floating drag ---

  function onPointerDown(e: PointerEvent) {
    if (!floatingEl) return;
    const rect = floatingEl.getBoundingClientRect();
    dragOffset.x = e.clientX - rect.left;
    dragOffset.y = e.clientY - rect.top;
    dragging = true;
    floatingEl.setPointerCapture(e.pointerId);
  }

  function onPointerMove(e: PointerEvent) {
    if (!dragging || !floatingEl) return;
    floatingEl.style.left = `${e.clientX - dragOffset.x}px`;
    floatingEl.style.top = `${e.clientY - dragOffset.y}px`;
    floatingEl.style.right = '';
    floatingEl.style.bottom = '';
  }

  function onPointerUp(e: PointerEvent) {
    dragging = false;
    if (!floatingEl) return;
    try { floatingEl.releasePointerCapture(e.pointerId); } catch { /* ignore */ }
    snapToCorner();
  }

  function snapToCorner(): void {
    if (!floatingEl) return;
    const rect = floatingEl.getBoundingClientRect();
    const w = window.innerWidth;
    const h = window.innerHeight;
    const left = rect.left < w / 2;
    const top = rect.top < h / 2;
    floatingEl.style.left = '';
    floatingEl.style.top = '';
    floatingEl.style.right = left ? '' : '16px';
    floatingEl.style.bottom = top ? '' : '16px';
    if (left) floatingEl.style.left = '16px';
    if (top) floatingEl.style.top = '16px';
    miniPlayerPosition.set(
      top ? (left ? 'tl' : 'tr') : (left ? 'bl' : 'br'),
    );
  }

  // Expose enter/exit globally so keyboard shortcuts can call them.
  if (typeof window !== 'undefined') {
    (window as any).__lwpMiniPlayer = {
      enter: () => enter(),
      exit,
    };
  }

  onDestroy(() => {
    exit();
    if (typeof window !== 'undefined') {
      delete (window as any).__lwpMiniPlayer;
    }
  });
</script>

<!-- Floating fallback container — only mounted when active and DocPiP unavailable. -->
{#if $miniPlayerMode && !docPipWindow}
  <div
    class="lwp-mini-player"
    class:lwp-mini-tl={$miniPlayerPosition === 'tl'}
    class:lwp-mini-tr={$miniPlayerPosition === 'tr'}
    class:lwp-mini-bl={$miniPlayerPosition === 'bl'}
    class:lwp-mini-br={$miniPlayerPosition === 'br'}
    bind:this={floatingEl}
    onpointerdown={onPointerDown}
    onpointermove={onPointerMove}
    onpointerup={onPointerUp}
    role="dialog"
    aria-label="Mini player"
  >
    <div class="lwp-mini-slot" bind:this={videoSlot}></div>
    <button
      class="lwp-mini-close"
      onclick={exit}
      aria-label="Close mini player"
    >
      <svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor">
        <path d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
      </svg>
    </button>
  </div>
{/if}
