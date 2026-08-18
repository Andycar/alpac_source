<script lang="ts">
  import { formatTime } from '../utils/time';
  import {
    currentTime,
    duration,
    progress,
    bufferedProgress,
    engine,
  } from '../stores/player';
  import { resetIdleTimer } from '../stores/ui';
  import { chapterMarkers } from '../stores/chapters';
  import { currentBookmarks } from '../stores/bookmarks';
  import { settings } from '../stores/settings';
  import ThumbnailPreview from './ThumbnailPreview.svelte';

  let timelineEl = $state<HTMLDivElement>(undefined!);
  let isDragging = $state(false);
  let hoverTime = $state(0);
  let hoverX = $state(0);
  let showHover = $state(false);

  const containerWidth = $derived(timelineEl?.offsetWidth || 0);

  function seekTo(e: MouseEvent | Touch) {
    if (!timelineEl) return;
    const rect = timelineEl.getBoundingClientRect();
    const pct = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    const time = pct * $duration;
    $engine?.seek(time);
    resetIdleTimer();
  }

  function onMouseDown(e: MouseEvent) {
    e.stopPropagation();
    isDragging = true;
    seekTo(e);
    const onMove = (ev: MouseEvent) => seekTo(ev);
    const onUp = () => {
      isDragging = false;
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  }

  function onMouseMove(e: MouseEvent) {
    if (!timelineEl) return;
    const rect = timelineEl.getBoundingClientRect();
    const pct = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    hoverTime = pct * $duration;
    hoverX = e.clientX - rect.left;
    showHover = true;
  }

  function onTouchStart(e: TouchEvent) {
    if (e.touches.length === 1) {
      // Stop propagation so gesture handler on container doesn't
      // intercept timeline touches as taps (would toggle controls instead of seeking)
      e.stopPropagation();
      isDragging = true;
      seekTo(e.touches[0]);
    }
  }

  function onTouchMove(e: TouchEvent) {
    if (isDragging && e.touches.length === 1) {
      e.preventDefault();
      e.stopPropagation();
      seekTo(e.touches[0]);
    }
  }

  function onTouchEnd(e: TouchEvent) {
    e.stopPropagation();
    isDragging = false;
  }
</script>

<div
  class="lwp-timeline"
  bind:this={timelineEl}
  onmousedown={onMouseDown}
  onmousemove={onMouseMove}
  onmouseleave={() => (showHover = false)}
  ontouchstart={onTouchStart}
  ontouchmove={onTouchMove}
  ontouchend={onTouchEnd}
  role="slider"
  aria-valuenow={$currentTime}
  aria-valuemin={0}
  aria-valuemax={$duration}
  tabindex="-1"
>
  <div class="lwp-timeline-track">
    <div class="lwp-timeline-buffered" style="width: {$bufferedProgress}%"></div>
    <div class="lwp-timeline-progress" style="width: {$progress}%"></div>
  </div>
  <div class="lwp-timeline-thumb" style="left: {$progress}%"></div>

  <!-- Chapter markers (Wave 1 B2) -->
  {#if $settings.showChapterMarkers && $chapterMarkers.length > 0 && $duration > 0}
    {#each $chapterMarkers as ch (ch.start + '_' + ch.title)}
      <div
        class="lwp-chapter-tick"
        style="left: {(ch.start / $duration) * 100}%"
        title={ch.title}
      ></div>
    {/each}
  {/if}

  <!-- Bookmarks (Pack 2) -->
  {#if $currentBookmarks.length > 0 && $duration > 0}
    {#each $currentBookmarks as b (b.createdAt)}
      <div
        class="lwp-bookmark-dot"
        style="left: {(b.time / $duration) * 100}%"
        title={b.label}
      ></div>
    {/each}
  {/if}

  {#if showHover && !isDragging}
    <ThumbnailPreview
      time={hoverTime}
      x={hoverX}
      visible={true}
      {containerWidth}
    />
    <div
      class="lwp-timeline-hover"
      style="left: {hoverX}px; position: absolute; bottom: 24px; transform: translateX(-50%);
             background: rgba(0,0,0,0.85); padding: 4px 8px; border-radius: 4px; font-size: 12px;
             pointer-events: none; white-space: nowrap;"
    >
      {formatTime(hoverTime)}
    </div>
  {/if}
</div>
