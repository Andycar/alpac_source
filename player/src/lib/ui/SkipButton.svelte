<script lang="ts">
  /**
   * SkipButton — two modes:
   *   1. CHAPTER mode: when the manifest declares a chapter whose title
   *      contains "intro" / "opening" / "recap" / "outro" / "ending"
   *      / "credits", we offer to jump to the chapter's end. This is
   *      the "smart skip" path (Wave polish — uses ChapterParser data).
   *   2. WINDOW mode (legacy fallback): no chapters → use the manual
   *      skipIntroDuration setting and show button for the first N
   *      seconds.
   *
   * Both modes hide once the user accepts the skip or the window expires.
   */

  import { currentTime, playing, engine, duration } from '../stores/player';
  import { settings } from '../stores/settings';
  import { activeChapter, chapterMarkers } from '../stores/chapters';
  import type { Chapter } from '../engine/core/types';

  // ─── State ───────────────────────────────────────────────
  let skippedChapters = $state<Set<number>>(new Set()); // by chapter.start
  let windowSkipped = $state(false);
  let windowExpired = $state(false);
  let episodeDuration = $state(0);

  // ─── Smart chapter detection ─────────────────────────────
  const SMART_CHAPTER_RE = /^(intro|opening|opening\s*credits?|recap|previously|outro|ending|credits|end\s*credits?|titles)/i;

  function isSkippableChapter(c: Chapter | null): boolean {
    if (!c) return false;
    if (SMART_CHAPTER_RE.test(c.title || '')) return true;
    if (c.class === 'com.apple.hls.chapter' && SMART_CHAPTER_RE.test(c.title || '')) return true;
    return false;
  }

  const smartActive = $derived(
    $settings.skipIntroEnabled &&
    !!$activeChapter &&
    isSkippableChapter($activeChapter) &&
    !skippedChapters.has($activeChapter!.start) &&
    isFinite($activeChapter!.end) &&
    $activeChapter!.end - $currentTime > 1
  );

  // Smart label adapts to chapter type: intro/recap → "Skip", outro → "Next".
  const smartLabel = $derived.by(() => {
    if (!$activeChapter) return 'Пропустить';
    const t = $activeChapter.title.toLowerCase();
    if (/recap|previously/.test(t)) return 'Пропустить «ранее»';
    if (/outro|ending|credits|titles/.test(t)) return 'К следующему';
    return 'Пропустить заставку';
  });

  // ─── Window-mode fallback ────────────────────────────────
  const inWindow = $derived(
    $currentTime > 5 &&
    $currentTime < $settings.skipIntroDuration + 10
  );

  const windowActive = $derived(
    $settings.skipIntroEnabled &&
    !windowSkipped &&
    !windowExpired &&
    inWindow &&
    $duration > $settings.skipIntroDuration + 30 &&
    // Don't show the legacy window if we have any chapters at all — chapter
    // mode is more accurate and we don't want two competing buttons.
    $chapterMarkers.length === 0
  );

  const visible = $derived($playing && (smartActive || windowActive));

  // ─── Actions ─────────────────────────────────────────────
  function skip() {
    if (!$engine) return;
    if (smartActive && $activeChapter) {
      $engine.seek($activeChapter.end);
      skippedChapters.add($activeChapter.start);
      skippedChapters = skippedChapters;
    } else if (windowActive) {
      const target = $settings.skipIntroDuration;
      if ($currentTime < target) $engine.seek(target);
      windowSkipped = true;
    }
  }

  // Auto-expire window mode if user lets it pass.
  $effect(() => {
    if ($currentTime >= $settings.skipIntroDuration + 10 && !windowSkipped) {
      windowExpired = true;
    }
  });

  // Reset state on new episode (large duration change).
  $effect(() => {
    const d = $duration;
    if (d > 0 && Math.abs(d - episodeDuration) > 30) {
      episodeDuration = d;
      skippedChapters = new Set();
      windowSkipped = false;
      windowExpired = false;
    }
  });
</script>

{#if visible}
  <button class="lwp-skip-btn" onclick={skip}>
    {smartActive ? smartLabel : 'Пропустить заставку'}
    <svg viewBox="0 0 24 24"><path d="M6 18l8.5-6L6 6v12zm10-12v12h2V6h-2z"/></svg>
  </button>
{/if}
