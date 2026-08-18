<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { PlaybackEngine, type PlayElement } from '../engine/PlaybackEngine';
  import { PreloadEngine, type PreloadSwapResult } from '../engine/features/PreloadEngine';
  import {
    engine,
    currentElement,
    currentTime,
    duration,
    playing,
    volume as volumeStore,
    muted as mutedStore,
    playlist,
    playlistIndex,
    bindEngineToStores,
    castAvailable,
    castConnected,
  } from '../stores/player';
  import { networkProfile } from '../stores/network';
  import { isLowPowerTV } from '../bridge/PlatformDetect';
  import { CastManager } from '../engine/CastManager';
  import {
    controlsVisible,
    resetIdleTimer,
    setupFullscreenListener,
    activeMenu,
    anyMenuOpen,
    chatSidebarMode,
  } from '../stores/ui';
  import { settings, getQualityForSource } from '../stores/settings';
  import { setupKeyboard, type KeyboardController } from '../utils/keyboard';
  import {
    setupGestures,
    type GestureController,
    type GestureCallbacks,
  } from '../utils/gestures';
  import { saveTimeline, loadTimeline, clearTimeline, setLampaTimeline, getLampaTimeline } from '../bridge/StorageSync';
  import TopBar from './TopBar.svelte';
  import Controls from './Controls.svelte';
  import Spinner from './Spinner.svelte';
  import EpisodeDrawer from './EpisodeDrawer.svelte';
  import GestureOverlay from './GestureOverlay.svelte';
  import StatsOverlay from './StatsOverlay.svelte';
  import DRMDiagnosticsOverlay from './DRMDiagnosticsOverlay.svelte';
  import WatchpartyPanel from './WatchpartyPanel.svelte';
  import ToastHost from './ToastHost.svelte';
  import ShortcutsOverlay from './ShortcutsOverlay.svelte';
  import ResumePrompt from './ResumePrompt.svelte';
  import { pendingResume } from '../stores/resume';
  import BookmarksPanel from './BookmarksPanel.svelte';
  import ChatSidebar from './ChatSidebar.svelte';
  import { pushToast } from '../stores/toasts';
  import { loadBookmarksFor } from '../stores/bookmarks';
  import { watchpartySession, watchpartySyncInstance } from '../stores/watchparty';
  import { applySubtitlePosition } from '../utils/subtitlePosition';
  import { assignOutputDevice } from '../engine/features/AudioOutput';
  import { VoiceControl } from '../engine/features/VoiceControl';
  import { hasSpeechRecognition } from '../utils/support';
  import SkipButton from './SkipButton.svelte';
  import NextEpisodeOverlay from './NextEpisodeOverlay.svelte';
  import MiniPlayer from './MiniPlayer.svelte';
  import AmbilightLayer from './AmbilightLayer.svelte';
  import { SkinManager } from './SkinManager';
  import { get } from 'svelte/store';

  let {
    element,
    onClose,
  }: {
    element: PlayElement;
    onClose: () => void;
  } = $props();

  let containerEl = $state<HTMLDivElement>(undefined!);
  let videoEl = $state<HTMLVideoElement>(undefined!);
  let videoStageEl = $state<HTMLDivElement>(undefined!);
  let eng: PlaybackEngine;
  let kbd: KeyboardController;
  let gesture: GestureController;
  let cleanupFullscreen: () => void;
  let isLoading = $state(true);
  let isTranscoding = $state(false);
  let timelineSaveInterval: ReturnType<typeof setInterval> | null = null;
  let castManager: CastManager | null = null;
  let preload: PreloadEngine | null = null;
  let zeroGapInProgress = false;
  let skin: SkinManager | null = null;
  let settingsCleanup: (() => void) | null = null;
  let voice: VoiceControl | null = null;
  let voiceActive = $state(false);
  let voiceTranscript = $state('');
  let voiceTranscriptTimer: ReturnType<typeof setTimeout> | null = null;

  // Play/Pause overlay animation
  let playPauseIcon = $state<'play' | 'pause' | null>(null);
  let playPauseTimer: ReturnType<typeof setTimeout> | null = null;
  let isFirstPlay = true;

  // Volume OSD
  let volumeOSD = $state<number | null>(null);
  let volumeOSDTimer: ReturnType<typeof setTimeout> | null = null;
  let lastVolumeForOSD = -1;

  // Long-press speed boost
  let speedBoostActive = $state(false);
  let savedSpeedBeforeBoost = 1;

  // Next episode overlay
  let showNextEpisode = $state(false);

  // Gesture overlay state
  let gestureSeekSide = $state<'left' | 'right' | null>(null);
  let gestureSeekSeconds = $state(10);
  let gestureVolume = $state<number | null>(null);
  let gestureBrightness = $state<number | null>(null);

  const gestureCallbacks: GestureCallbacks = {
    onDoubleTapSeek(side, seconds) {
      gestureSeekSide = side;
      gestureSeekSeconds = seconds;
      // Reset after animation
      setTimeout(() => (gestureSeekSide = null), 700);
    },
    onVolumeChange(vol) {
      gestureVolume = vol;
    },
    onBrightnessChange(brightness) {
      gestureBrightness = brightness;
    },
    onLongPressStart() {
      onLongPressStart();
    },
    onLongPressEnd() {
      onLongPressEnd();
    },
    onPinch(scale) {
      // Apply CSS transform on the active video. clamp matches gesture range.
      if (videoEl) {
        videoEl.style.transformOrigin = 'center center';
        videoEl.style.transform = `scale(${scale.toFixed(3)})`;
      }
    },
    onPinchEnd() {
      // Snap back to neutral when very close to 1.0 (avoids stuck zoom).
      if (videoEl) {
        const m = /scale\(([\d.]+)\)/.exec(videoEl.style.transform || '');
        const cur = m ? parseFloat(m[1]) : 1;
        if (cur > 0.95 && cur < 1.05) {
          videoEl.style.transition = 'transform 200ms ease-out';
          videoEl.style.transform = '';
          setTimeout(() => { if (videoEl) videoEl.style.transition = ''; }, 220);
        }
      }
    },
  };

  onMount(async () => {
    eng = new PlaybackEngine(videoEl);
    await eng.init({ lowPowerTV: isLowPowerTV() });
    bindEngineToStores(eng);

    // Apply saved settings
    const s = get(settings);
    eng.volume = s.volume;
    eng.muted = s.muted;
    eng.playbackRate = s.playbackRate;
    volumeStore.set(s.volume);
    mutedStore.set(s.muted);

    // Apply saved audio processing (lazy — only init if non-default)
    if (s.audioBoost > 1.01 || s.audioCompressor !== 'off') {
      eng.setAudioBoost(s.audioBoost);
      eng.setAudioCompressor(s.audioCompressor);
    }

    // Wave 1 B4 — apply Audio Pro settings if any are non-default.
    if (s.voiceBoost) eng.setVoiceBoost(true);
    if (s.loudnessTarget !== 'off') eng.setLoudnessTarget(s.loudnessTarget);
    if (s.hrtfEnabled) {
      eng.setHRTF(true).catch(() => { /* asset missing or unsupported */ });
    }

    currentElement.set(element);

    // Store Lampa timeline object reference for in-place updates
    if (element.timeline && typeof element.timeline === 'object') {
      setLampaTimeline(element.timeline);
    }

    // Set up playlist if provided
    if (element.playlist?.length) {
      playlist.set(element.playlist);
      const idx = element.playlist.findIndex(
        (ep) => ep.url === element.url,
      );
      playlistIndex.set(idx >= 0 ? idx : 0);
    }

    // Volume OSD (react to volume changes via keyboard/gesture).
    // This subscription is independent of the engine instance — set up once.
    volumeStore.subscribe((v) => {
      if (lastVolumeForOSD < 0) { lastVolumeForOSD = v; return; }
      if (Math.abs(v - lastVolumeForOSD) > 0.001) {
        lastVolumeForOSD = v;
        triggerVolumeOSD(v);
      }
    });

    // Wire all engine event handlers. Extracted into a function so the
    // zero-gap swap path (tryZeroGapAdvance) can re-attach to the new engine.
    attachEngineHandlers();

    await eng.load(element);

    // Init zero-gap preload (Wave 1 B1)
    if (videoStageEl) {
      preload = new PreloadEngine(videoStageEl, () => {
        const idx = get(playlistIndex);
        const list = get(playlist);
        if (idx < 0 || idx >= list.length - 1) return null;
        return { element: list[idx + 1], episodeIndex: idx + 1 };
      });
    }

    // Periodically save playback position (every 5s)
    timelineSaveInterval = setInterval(() => {
      const curEl = get(currentElement);
      if (curEl && eng && !eng.paused) {
        saveTimeline(curEl.url, eng.currentTime, eng.duration);
      }
    }, 5000);

    // Set up controls with callbacks
    kbd = setupKeyboard(eng, containerEl, {
      onNext,
      onPrev,
      onClose: onBack,
    });
    gesture = setupGestures(eng, containerEl, gestureCallbacks);
    cleanupFullscreen = setupFullscreenListener();
    resetIdleTimer();

    // Voice control (Pack 4) — Chromium only.
    if (hasSpeechRecognition()) {
      voice = new VoiceControl(eng, {
        lang: 'ru-RU',
        onCommand: (cmd) => {
          // 'next' / 'prev' / 'subtitles' / 'audio' / 'quality' need UI hooks.
          if (cmd.type === 'next') onNext();
          else if (cmd.type === 'prev') onPrev();
          else if (cmd.type === 'subtitles') activeMenu.set('subtitles');
          else if (cmd.type === 'audio') activeMenu.set('audio');
          else if (cmd.type === 'quality') activeMenu.set('quality');
          pushToast(`🎙 ${cmd.type.replace('_', ' ')}`, { kind: 'info', ttl: 1200 });
        },
        onTranscript: (text, isFinal) => {
          voiceTranscript = text;
          if (voiceTranscriptTimer) clearTimeout(voiceTranscriptTimer);
          voiceTranscriptTimer = setTimeout(() => { voiceTranscript = ''; }, isFinal ? 1500 : 4000);
        },
        onError: (err) => {
          if (err === 'not-allowed' || err === 'service-not-allowed') {
            pushToast('Микрофон не разрешён', { kind: 'warn' });
            voiceActive = false;
          }
        },
      });
      // Expose toggle via global so keyboard.ts can call without import cycle.
      (window as any).__lwpToggleVoice = () => {
        const ok = voice?.toggle();
        voiceActive = !!ok;
        pushToast(voiceActive ? '🎙 Голосовое управление включено' : '🎙 Выключено', {
          kind: 'info', ttl: 1500,
        });
      };
    }

    // Init Cast/AirPlay
    castManager = new CastManager(videoEl);
    castManager.onAvailabilityChange = (a) => castAvailable.set(a);
    castManager.onConnectionChange = (c) => castConnected.set(c);
    castManager.init();

    // Apply theme + density (Wave 1 B3) and Audio Pro toggles (B4).
    skin = new SkinManager(containerEl);
    let prevVoice = get(settings).voiceBoost;
    let prevLoud: typeof prevVoice extends boolean ? any : any = get(settings).loudnessTarget;
    let prevHrtf = get(settings).hrtfEnabled;
    const applyReactive = () => {
      const s = get(settings);
      skin?.applyTheme(s.themeName);
      skin?.applyDensity(s.density);
      skin?.applyEdgeGlow(s.edgeGlow);
      if (s.voiceBoost !== prevVoice) {
        prevVoice = s.voiceBoost;
        eng.setVoiceBoost(s.voiceBoost);
      }
      if (s.loudnessTarget !== prevLoud) {
        prevLoud = s.loudnessTarget;
        eng.setLoudnessTarget(s.loudnessTarget);
      }
      if (s.hrtfEnabled !== prevHrtf) {
        prevHrtf = s.hrtfEnabled;
        eng.setHRTF(s.hrtfEnabled).catch(() => { /* ignore */ });
      }
    };
    applyReactive();
    const unsubSkin = settings.subscribe(applyReactive);

    // Wave 2 C1 — re-apply buffer strategy on network/battery changes.
    // Throttled by store identity equality + the BufferStrategy own
    // idempotency, so spurious 'change' events are cheap.
    let lastEffective = '';
    let lastSaveData = false;
    let lastBatteryBucket = -1;
    const unsubNetwork = networkProfile.subscribe((np) => {
      const battBucket = isFinite(np.batteryLevel)
        ? Math.floor(np.batteryLevel * 10) // 10% buckets
        : -1;
      if (
        np.effectiveType !== lastEffective ||
        np.saveData !== lastSaveData ||
        battBucket !== lastBatteryBucket
      ) {
        lastEffective = np.effectiveType;
        lastSaveData = np.saveData;
        lastBatteryBucket = battBucket;
        eng?.refreshBufferProfile();
      }
    });

    settingsCleanup = () => {
      unsubSkin();
      unsubNetwork();
    };

    containerEl.focus();
  });

  onDestroy(() => {
    // Save timeline position on exit
    const curEl = get(currentElement);
    if (curEl && eng) {
      saveTimeline(curEl.url, eng.currentTime, eng.duration);
    }

    // Save settings
    if (eng) {
      settings.update((s) => ({
        ...s,
        volume: eng.volume,
        muted: eng.muted,
        playbackRate: eng.playbackRate,
      }));
    }

    if (timelineSaveInterval) clearInterval(timelineSaveInterval);
    kbd?.destroy();
    gesture?.destroy();
    cleanupFullscreen?.();
    castManager?.destroy();
    preload?.destroy();
    preload = null;
    settingsCleanup?.();
    settingsCleanup = null;
    skin = null;
    voice?.destroy();
    voice = null;
    if (typeof window !== 'undefined') delete (window as any).__lwpToggleVoice;
    // Tear down the watchparty WS (if any) when the player closes —
    // the module-level sync instance is no longer owned once the user
    // leaves playback, regardless of whether the panel was open.
    try {
      const wp = get(watchpartySyncInstance) as any;
      wp?.destroy?.();
    } catch { /* ignore */ }
    watchpartySyncInstance.set(null);
    watchpartySession.set(null);
    eng?.destroy();
  });

  function triggerPlayPauseOverlay(icon: 'play' | 'pause') {
    playPauseIcon = icon;
    if (playPauseTimer) clearTimeout(playPauseTimer);
    playPauseTimer = setTimeout(() => { playPauseIcon = null; }, 500);
  }

  function triggerVolumeOSD(vol: number) {
    volumeOSD = vol;
    if (volumeOSDTimer) clearTimeout(volumeOSDTimer);
    volumeOSDTimer = setTimeout(() => { volumeOSD = null; }, 1500);
  }

  function onLongPressStart() {
    const s = get(settings);
    if (!s.longPressSpeedBoost || s.longPressSpeedBoost <= 0) return;
    savedSpeedBeforeBoost = eng.playbackRate;
    eng.playbackRate = s.longPressSpeedBoost;
    speedBoostActive = true;
  }

  function onLongPressEnd() {
    if (!speedBoostActive) return;
    eng.playbackRate = savedSpeedBeforeBoost;
    speedBoostActive = false;
  }

  function onNextEpisodePlay() {
    showNextEpisode = false;
    onNext();
  }

  function onNextEpisodeCancel() {
    showNextEpisode = false;
  }

  function onMouseMove() {
    resetIdleTimer();
  }

  function onBack() {
    // Save position before closing
    const curEl = get(currentElement);
    if (curEl && eng) {
      saveTimeline(curEl.url, eng.currentTime, eng.duration);
    }
    // Invoke callback (mark watched)
    if (element.callback && get(currentTime) > 0) {
      try { element.callback(); } catch {}
    }
    onClose();
  }

  /**
   * Wire engine event handlers. Re-callable so we can re-attach after a
   * zero-gap swap replaces `eng` with the secondary engine.
   *
   * The `loaded` handler resumes from saved StorageSync position (fallback
   * for the very first load when element.timeline was empty). For
   * subsequent loads (post-swap) timeline is already applied by the
   * preload engine, so the fallback is a no-op.
   */
  function attachEngineHandlers() {
    eng.on('loaded', () => {
      isLoading = false;
      isTranscoding = eng.isTranscoding;

      const curEl = get(currentElement) || element;
      // Load bookmarks for the freshly-loaded URL.
      loadBookmarksFor(curEl.url);

      // Re-apply subtitle position (shaka rebuilds the text container per load).
      const sNow = get(settings);
      setTimeout(() => applySubtitlePosition(sNow.subtitlePosition, sNow.subtitleOffset), 200);

      // Re-apply preferred audio output device (Chromium-only).
      if (sNow.audioOutputDeviceId && eng.video) {
        assignOutputDevice(eng.video, sNow.audioOutputDeviceId).catch(() => { /* ignore */ });
      }
      const tlTime = typeof curEl.timeline === 'number'
        ? curEl.timeline
        : (curEl.timeline?.time ?? 0);
      if (tlTime <= 0) {
        const saved = loadTimeline(curEl.url);
        if (saved > 30) {
          // Surface the resume prompt instead of a silent seek so users
          // can choose "start over" if they want to rewatch.
          pendingResume.set(saved);
          console.log('[LWP] Pending resume offered:', saved);
        } else if (saved > 0) {
          eng.seek(saved);
        }
      }

      eng.play();
    });

    eng.on('buffering', (b: boolean) => { isLoading = b; });
    eng.on('error', (err: any) => {
      console.error('[LWP] Playback error:', err);
      isLoading = false;
      if (err?.code === 'CODEC_UNSUPPORTED') {
        const codec = err?.data?.codec || 'этот кодек';
        pushToast(
          `${codec} не поддерживается браузером. WASM-декодер пока scaffold — попробуйте Chrome/Edge или другой источник.`,
          { kind: 'warn', ttl: 6000 },
        );
      }
    });

    eng.on('playing', () => {
      if (isFirstPlay) { isFirstPlay = false; return; }
      triggerPlayPauseOverlay('play');
    });
    eng.on('paused', () => {
      if (isFirstPlay) return;
      triggerPlayPauseOverlay('pause');
    });

    // 'trackschanged' fires whenever shaka's variant set is reshuffled,
    // INCLUDING after our own selectVariantTrack calls. If we naively
    // re-apply preferred quality every time, we feedback-loop:
    //   user picks English audio
    //   → selectVariantTrack(English variant)
    //   → 'trackschanged'
    //   → applyPreferredQuality('highest') re-selects highest-bitrate
    //     variant which may have RUSSIAN audio
    //   → audio reverts.
    // Track preferences are now applied EXACTLY ONCE per content load,
    // gated by a flag we reset in 'loaded'.
    let preferencesApplied = false;
    eng.on('loaded', () => { preferencesApplied = false; });
    eng.on('trackschanged', () => {
      if (preferencesApplied) return;
      preferencesApplied = true;
      const s = get(settings);
      const el = get(currentElement);
      const sourceQuality = el?.source ? getQualityForSource(el.source) : null;
      eng.applyPreferredQuality(sourceQuality ?? s.preferredQuality);
      if (s.preferredAudioLang) eng.applyPreferredAudioLang(s.preferredAudioLang);
      if (s.preferredSubLang) eng.applyPreferredSubLang(s.preferredSubLang);
    });

    eng.on('ended', async () => {
      const curEl = get(currentElement);
      if (curEl) clearTimeline(curEl.url);
      // Try preload zero-gap swap first; fall back to playItem otherwise.
      if (await tryZeroGapAdvance()) return;
      const idx = get(playlistIndex);
      const list = get(playlist);
      if (idx < list.length - 1) {
        playItem(list[idx + 1], idx + 1);
      }
    });

    // Combined timeupdate: NextEpisodeOverlay visibility + preload arming.
    //
    // `timeupdate` fires 4-60x per second. Everything here must be fast.
    // We early-out when we're nowhere near the end of the episode so the
    // hot path is just a handful of field reads.
    eng.on('timeupdate', () => {
      const dur = eng.duration;
      const ct = eng.currentTime;
      const remaining = dur - ct;

      // Cheapest early-out: if we're not near the end and overlay is off,
      // there's nothing to do.
      if (remaining > 35 && !showNextEpisode) return;
      if (!isFinite(remaining) || dur <= 0) return;

      const idx = get(playlistIndex);
      const list = get(playlist);
      const hasNextEp = idx < list.length - 1;

      if (hasNextEp && dur > 60 && remaining < 30 && remaining > 0) {
        if (!showNextEpisode) showNextEpisode = true;
      } else if (showNextEpisode && remaining > 32) {
        showNextEpisode = false;
      }

      if (hasNextEp && preload && !zeroGapInProgress) {
        const s = get(settings);
        const buf = eng.buffered;
        const bufferedAhead = buf.length > 0 ? Math.max(0, buf.end(buf.length - 1) - ct) : 0;
        const net = get(networkProfile);
        preload.armForEpisode({
          enabled: !!s.enablePreload,
          startSeconds: s.preloadStartSeconds || 30,
          remaining,
          bufferedAhead,
          networkType: net.effectiveType,
        });
      }
    });
  }

  /**
   * Zero-gap episode advance via PreloadEngine (Wave 1 B1).
   *
   * Returns true if the swap succeeded — caller should NOT also call
   * playItem(). Returns false if no preload was ready, in which case
   * the caller falls back to the normal playItem() path.
   */
  async function tryZeroGapAdvance(): Promise<boolean> {
    if (!preload) return false;
    if (zeroGapInProgress) return true; // suppress concurrent calls
    const swap = preload.consumeIfReady();
    if (!swap) return false;

    zeroGapInProgress = true;
    try {
      const oldEng = eng;
      const oldVideo = videoEl;
      const oldEl = get(currentElement);

      // Persist position + fire Lampa callback for outgoing episode
      // BEFORE swap — so external listeners see the right currentTime.
      if (oldEl && oldEng) {
        saveTimeline(oldEl.url, oldEng.currentTime, oldEng.duration);
        if (oldEl.callback && oldEng.currentTime > 0) {
          try { oldEl.callback(); } catch { /* ignore */ }
        }
      }

      // Set up the next-video to be visible BEFORE we hand off, so the
      // browser has a frame to paint during the crossfade.
      const nextVideo = swap.newVideo;
      const nextEng = swap.newEngine;
      nextVideo.muted = oldEng?.muted ?? false;
      nextVideo.volume = oldEng?.volume ?? 1;
      nextVideo.playbackRate = oldEng?.playbackRate ?? 1;
      nextVideo.style.opacity = '0';
      nextVideo.style.transition = 'opacity 400ms ease-in-out';
      // Ensure stacking — next is on top during fade.
      nextVideo.style.zIndex = '3';
      // Trigger play right away — frames decode while we fade in.
      try { await nextVideo.play(); } catch { /* autoplay blocked, fine */ }

      // Force reflow before applying opacity transition target value.
      void nextVideo.offsetWidth;
      nextVideo.style.opacity = '1';
      if (oldVideo) {
        oldVideo.style.transition = 'opacity 400ms ease-in-out';
        oldVideo.style.opacity = '0';
      }

      // Wait for the fade to complete, then promote nextVideo to be the
      // primary <video> element. We replace the engine reference and
      // re-bind the stores so all subscribers point at the new engine.
      await new Promise((r) => setTimeout(r, 420));

      // Replace primary refs
      eng = nextEng;
      videoEl = nextVideo;
      // Strip the inline overrides — primary video styles come from CSS.
      nextVideo.style.zIndex = '';
      nextVideo.style.position = '';
      nextVideo.style.opacity = '';
      nextVideo.style.transition = '';
      nextVideo.style.pointerEvents = '';
      nextVideo.classList.remove('lwp-video-next');

      // Update Lampa timeline tracking (per-episode object)
      const newEl = swap.element;
      currentElement.set(newEl);
      playlistIndex.set(swap.episodeIndex);
      if (newEl.timeline && typeof newEl.timeline === 'object') {
        setLampaTimeline(newEl.timeline);
      } else {
        const freshTl = { time: 0, duration: 0, percent: 0 };
        setLampaTimeline(freshTl);
        newEl.timeline = freshTl;
      }

      // Re-bind stores to the new engine so qualityTracks / audioTracks
      // / time updates flow from the new playback.
      bindEngineToStores(nextEng);
      // Push current state immediately (bind only listens to events).
      currentTime.set(nextEng.currentTime);
      duration.set(nextEng.duration);
      isTranscoding = nextEng.isTranscoding;

      // Re-wire keyboard / gestures to the new engine. The container
      // doesn't change, so we only swap the engine reference inside
      // the controllers.
      kbd?.destroy();
      gesture?.destroy();
      kbd = setupKeyboard(nextEng, containerEl, { onNext, onPrev, onClose: onBack });
      gesture = setupGestures(nextEng, containerEl, gestureCallbacks);

      // Wire engine event handlers to the new instance. We replicate
      // the same listener block from onMount() rather than tracking
      // each subscription — destroying the old engine clears its bus.
      attachEngineHandlers();

      // Fire-and-forget destroy of the old engine + remove its <video>.
      if (oldEng) {
        oldEng.destroy().catch(() => { /* ignore */ });
      }
      if (oldVideo?.parentElement) {
        oldVideo.parentElement.removeChild(oldVideo);
      }

      isLoading = false;
      console.log('[LWP] Zero-gap swap completed for episode index', swap.episodeIndex);
      pushToast('Эпизод переключён без паузы', { kind: 'success', ttl: 1800 });
      return true;
    } catch (err) {
      console.warn('[LWP] Zero-gap swap failed, falling back:', err);
      return false;
    } finally {
      zeroGapInProgress = false;
    }
  }

  async function playItem(el: PlayElement, index: number) {
    // Manual switch invalidates any pending preload — cancel it.
    preload?.cancel();

    // Save position of current item before switching
    const curEl = get(currentElement);
    if (curEl && eng) {
      saveTimeline(curEl.url, eng.currentTime, eng.duration);
    }

    // Also fire callback for the current episode (marks it as "watched" in Lampa)
    if (curEl?.callback && eng && eng.currentTime > 0) {
      try { curEl.callback(); } catch {}
    }

    isLoading = true;
    currentElement.set(el);
    playlistIndex.set(index);
    activeMenu.set('none');

    // Update Lampa timeline reference for the new episode.
    // Each playlist item may carry its own timeline object.
    if (el.timeline && typeof el.timeline === 'object') {
      setLampaTimeline(el.timeline);
    } else {
      // Create a fresh timeline object for Lampa so progress is tracked
      const freshTl = { time: 0, duration: 0, percent: 0 };
      setLampaTimeline(freshTl);
      // Attach it to the element so future saves use it
      el.timeline = freshTl;
    }

    await eng.load(el);
    isTranscoding = eng.isTranscoding;
    await eng.play();
  }

  function onPrev() {
    const idx = get(playlistIndex);
    const list = get(playlist);
    if (idx > 0) {
      playItem(list[idx - 1], idx - 1);
    }
  }

  function onNext() {
    const idx = get(playlistIndex);
    const list = get(playlist);
    if (idx < list.length - 1) {
      playItem(list[idx + 1], idx + 1);
    }
  }

  function onVoiceSelect(voice: { name: string; index: number }) {
    console.log('[LWP] Voice select:', voice.name, 'index:', voice.index);
    const Lampa = (window as any).Lampa;
    if (Lampa?.PlayerPanel?.listener) {
      Lampa.PlayerPanel.listener.send('voice', { index: voice.index });
    }
  }

  function onEpisodeSelect(el: PlayElement, index: number) {
    playItem(el, index);
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="lwp-root"
  class:lwp-controls-visible={$controlsVisible}
  data-chat={$chatSidebarMode}
  bind:this={containerEl}
  onmousemove={onMouseMove}
  tabindex="-1"
>
  <!-- Video stage — wraps the active video and any zero-gap preload sibling.
       PreloadEngine appends a second <video class="lwp-video-next"> here. -->
  <div class="lwp-video-stage" bind:this={videoStageEl}>
    <!-- svelte-ignore a11y_media_has_caption -->
    <video
      class="lwp-video"
      bind:this={videoEl}
      poster={element.poster}
      playsinline
      crossorigin="anonymous"
      {...{ 'x-webkit-airplay': 'allow' } as any}
    ></video>
    <!-- Ambilight layer (Wave 1 B3) — only rendered after videoEl exists. -->
    {#if videoEl}
      <AmbilightLayer videoEl={videoEl} />
    {/if}
  </div>

  <!-- Gradient overlays -->
  <div class="lwp-gradient-top"></div>
  <div class="lwp-gradient-bottom"></div>

  <!-- Loading spinner -->
  <Spinner visible={isLoading} />

  <!-- Play/Pause overlay animation -->
  {#if playPauseIcon}
    <div class="lwp-play-overlay" class:lwp-play-overlay-play={playPauseIcon === 'play'}>
      <svg viewBox="0 0 24 24" fill="currentColor">
        {#if playPauseIcon === 'play'}
          <path d="M8 5v14l11-7z"/>
        {:else}
          <path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z"/>
        {/if}
      </svg>
    </div>
  {/if}

  <!-- Volume OSD -->
  {#if volumeOSD !== null}
    <div class="lwp-volume-osd">
      <svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor">
        {#if volumeOSD === 0}
          <path d="M16.5 12c0-1.77-1.02-3.29-2.5-4.03v2.21l2.45 2.45c.03-.2.05-.41.05-.63zm2.5 0c0 .94-.2 1.82-.54 2.64l1.51 1.51C20.63 14.91 21 13.5 21 12c0-4.28-2.99-7.86-7-8.77v2.06c2.89.86 5 3.54 5 6.71zM4.27 3L3 4.27 7.73 9H3v6h4l5 5v-6.73l4.25 4.25c-.67.52-1.42.93-2.25 1.18v2.06c1.38-.31 2.63-.95 3.69-1.81L19.73 21 21 19.73l-9-9L4.27 3zM12 4L9.91 6.09 12 8.18V4z"/>
        {:else}
          <path d="M3 9v6h4l5 5V4L7 9H3zm13.5 3c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM14 3.23v2.06c2.89.86 5 3.54 5 6.71s-2.11 5.85-5 6.71v2.06c4.01-.91 7-4.49 7-8.77s-2.99-7.86-7-8.77z"/>
        {/if}
      </svg>
      <span>{Math.round(volumeOSD * 100)}%</span>
    </div>
  {/if}

  <!-- Speed boost badge -->
  {#if speedBoostActive}
    <div class="lwp-speed-badge">
      {get(settings).longPressSpeedBoost || 2}× ⏩
    </div>
  {/if}

  <!-- Gesture overlay (seek/volume/brightness indicators) -->
  <GestureOverlay
    seekSide={gestureSeekSide}
    seekSeconds={gestureSeekSeconds}
    volumeLevel={gestureVolume}
    brightnessLevel={gestureBrightness}
  />

  <!-- Top bar (use currentElement store to update on episode change) -->
  <TopBar
    title={($currentElement || element).title || ''}
    season={($currentElement || element).season}
    episode={($currentElement || element).episode}
    voiceName={($currentElement || element).voice_name}
    {isTranscoding}
    onBack={onBack}
  />

  <!-- Menu backdrop — click/tap anywhere outside menu to close it -->
  <!-- Must be at lwp-root level (not inside lwp-bottom with transform) so position:fixed works -->
  {#if $anyMenuOpen && $activeMenu !== 'episodes'}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="lwp-menu-backdrop" onclick={() => { activeMenu.set('none'); resetIdleTimer(); }}></div>
  {/if}

  <!-- Bottom controls -->
  {#if containerEl}
    <Controls
      container={containerEl}
      {onPrev}
      {onNext}
      {onVoiceSelect}
    />
  {/if}

  <!-- Skip intro button -->
  <SkipButton />

  <!-- Next episode countdown -->
  <NextEpisodeOverlay
    visible={showNextEpisode}
    onPlay={onNextEpisodePlay}
    onCancel={onNextEpisodeCancel}
  />

  <!-- Stats overlay -->
  <StatsOverlay />

  <!-- DRM diagnostics overlay (Wave 2 C4) -->
  <DRMDiagnosticsOverlay />

  <!-- Mini player (Wave 1 D3) — Document PiP / floating fallback -->
  <MiniPlayer />

  <!-- Voice control indicator (Pack 4) -->
  {#if voiceActive}
    <div class="lwp-voice-indicator">
      <span class="lwp-voice-pulse"></span>
      <span class="lwp-voice-label">{voiceTranscript || 'Слушаю…'}</span>
    </div>
  {/if}

  <!-- Resume prompt (Continue from N:NN?) -->
  <ResumePrompt />

  <!-- Toast notifications (top-right) -->
  <ToastHost />

  <!-- Keyboard shortcuts cheat sheet (toggled by '?' / F1) -->
  <ShortcutsOverlay />

  <!-- Watchparty panel (Wave 2 C2) — floating sheet, opens via 'w' key -->
  {#if $activeMenu === 'watchparty'}
    <div class="lwp-menu lwp-menu-floating">
      <div class="lwp-menu-header">
        <span>Совместный просмотр</span>
        <button
          class="lwp-btn lwp-btn-ghost"
          aria-label="Close"
          onclick={() => activeMenu.set('none')}
        >×</button>
      </div>
      <WatchpartyPanel />
    </div>
  {/if}

  <!-- Chat sidebar (Twitch-style) — always mounted, self-hidden when
       user isn't in a room. See lib/ui/ChatSidebar.svelte. -->
  <ChatSidebar />

  <!-- Bookmarks panel (Pack 2) — opens via Shift+B or Settings action -->
  {#if $activeMenu === 'bookmarks'}
    <div class="lwp-menu lwp-menu-floating">
      <div class="lwp-menu-header">
        <span>Закладки</span>
        <button
          class="lwp-btn lwp-btn-ghost"
          aria-label="Close"
          onclick={() => activeMenu.set('none')}
        >×</button>
      </div>
      <BookmarksPanel />
    </div>
  {/if}

  <!-- Episode drawer -->
  <EpisodeDrawer
    visible={$activeMenu === 'episodes'}
    onSelect={onEpisodeSelect}
    onClose={() => activeMenu.set('none')}
  />
</div>
