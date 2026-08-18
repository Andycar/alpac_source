<script lang="ts">
  import { playbackRate, engine } from '../stores/player';
  import { activeMenu, isPiP, showStats, showDrmInfo, accentColor, accentColorDark, accentColorLight } from '../stores/ui';
  import { settings, updateSetting } from '../stores/settings';
  import { autoFocusMenu } from '../utils/menu-focus';
  import { applySubtitlePosition } from '../utils/subtitlePosition';
  import { hasOutputDevicePicker } from '../utils/support';
  import { listAudioOutputs, assignOutputDevice, type AudioOutputDevice } from '../engine/features/AudioOutput';
  import { onMount } from 'svelte';

  let { visible = false }: { visible: boolean } = $props();

  // ─── Existing options ────────────────────────────────────────
  const speeds = [0.25, 0.5, 0.75, 1, 1.25, 1.5, 2, 3, 4];
  const fontSizes = [
    { label: 'S', value: 0.7 },
    { label: 'M', value: 1.0 },
    { label: 'L', value: 1.3 },
    { label: 'XL', value: 1.6 },
  ];
  const subColors = [
    { label: 'Белый', value: '#ffffff' },
    { label: 'Жёлтый', value: '#ffff00' },
    { label: 'Голубой', value: '#00ffff' },
    { label: 'Зелёный', value: '#00ff00' },
  ];
  const bgOpacities = [
    { label: 'Выкл', value: 0 },
    { label: 'Низкая', value: 0.25 },
    { label: 'Средняя', value: 0.5 },
    { label: 'Высокая', value: 0.75 },
  ];
  const skipDurations = [30, 60, 90, 120];
  const compressorPresets: Array<{ label: string; value: 'off' | 'light' | 'heavy' }> = [
    { label: 'Выкл', value: 'off' },
    { label: 'Лёгкая', value: 'light' },
    { label: 'Сильная', value: 'heavy' },
  ];
  const speedBoostOptions = [
    { label: 'Выкл', value: 0 },
    { label: '1.5×', value: 1.5 },
    { label: '2×', value: 2 },
    { label: '3×', value: 3 },
  ];
  const subtitlePresets = [
    { label: 'Контрастный', value: 'contrast' },
    { label: 'Netflix', value: 'netflix' },
    { label: 'Кинотеатр', value: 'cinema' },
    { label: 'Минимальный', value: 'minimal' },
  ];

  // ─── Wave 1+2 options ────────────────────────────────────────
  const themes: Array<{ label: string; value: 'cinematic' | 'minimal' | 'classic' | 'oled-black'; swatch: string }> = [
    { label: 'Cinematic', value: 'cinematic', swatch: 'linear-gradient(135deg,#1a1d29,#2a2d3a)' },
    { label: 'Minimal',   value: 'minimal',   swatch: '#0a0a0c' },
    { label: 'Classic',   value: 'classic',   swatch: '#1e1e24' },
    { label: 'OLED',      value: 'oled-black',swatch: '#000000' },
  ];
  const densities: Array<{ label: string; value: 'compact' | 'normal' | 'spacious' }> = [
    { label: 'Компакт',   value: 'compact' },
    { label: 'Нормально', value: 'normal' },
    { label: 'Свободно',  value: 'spacious' },
  ];
  const ambilightModes: Array<{ label: string; value: 'off' | 'subtle' | 'rich' }> = [
    { label: 'Выкл',     value: 'off' },
    { label: 'Мягкий',   value: 'subtle' },
    { label: 'Яркий',    value: 'rich' },
  ];
  const loudnessOptions: Array<{ label: string; value: 'off' | -23 | -16 | -14 }> = [
    { label: 'Выкл',          value: 'off' },
    { label: '-23 LUFS (TV)', value: -23 },
    { label: '-16 LUFS',      value: -16 },
    { label: '-14 LUFS',      value: -14 },
  ];
  const drmRobustnessOptions: Array<{ label: string; value: 'auto' | 'sw' | 'hw' }> = [
    { label: 'Авто',    value: 'auto' },
    { label: 'SW',      value: 'sw' },
    { label: 'HW',      value: 'hw' },
  ];

  // ─── Action helpers ──────────────────────────────────────────
  function setSpeed(rate: number) {
    if ($engine) {
      $engine.playbackRate = rate;
      playbackRate.set(rate);
    }
  }
  function setSubSize(size: number) { updateSetting('subtitleFontSize', size); applySubStyle(); }
  function setSubColor(color: string) { updateSetting('subtitleColor', color); applySubStyle(); }
  function setSubBgOpacity(opacity: number) { updateSetting('subtitleBgOpacity', opacity); applySubStyle(); }
  function applySubStyle() {
    const container = document.querySelector('.shaka-text-container') as HTMLElement;
    if (container) {
      container.style.fontSize = `${$settings.subtitleFontSize * 100}%`;
      const spans = container.querySelectorAll('span');
      spans.forEach((span) => {
        span.style.color = $settings.subtitleColor;
        span.style.backgroundColor = `rgba(0,0,0,${$settings.subtitleBgOpacity})`;
      });
    }
  }
  function applySubPreset(preset: string) {
    const container = document.querySelector('.shaka-text-container') as HTMLElement;
    if (!container) return;
    container.classList.remove('lwp-sub-netflix', 'lwp-sub-cinema', 'lwp-sub-contrast', 'lwp-sub-minimal');
    container.classList.add(`lwp-sub-${preset}`);
  }
  function togglePiP() { $engine?.togglePiP(); activeMenu.set('none'); }
  function toggleStats() { showStats.update((v) => !v); activeMenu.set('none'); }
  function toggleDrmInfo() { showDrmInfo.update((v) => !v); activeMenu.set('none'); }
  function toggleSkipIntro() { updateSetting('skipIntroEnabled', !$settings.skipIntroEnabled); }
  function setSkipDuration(dur: number) { updateSetting('skipIntroDuration', dur); }
  function onBoostChange(e: Event) {
    const val = parseFloat((e.target as HTMLInputElement).value);
    updateSetting('audioBoost', val);
    $engine?.setAudioBoost(val);
  }
  function setCompressor(preset: 'off' | 'light' | 'heavy') {
    updateSetting('audioCompressor', preset);
    $engine?.setAudioCompressor(preset);
  }
  function takeScreenshot() {
    const dataUrl = $engine?.takeScreenshot();
    if (dataUrl) {
      const a = document.createElement('a');
      a.href = dataUrl;
      a.download = `screenshot-${Date.now()}.png`;
      a.click();
    }
    activeMenu.set('none');
  }
  function openWatchparty() {
    activeMenu.set('watchparty');
  }

  // Wave 1+2 action helpers
  function setTheme(name: typeof themes[number]['value']) { updateSetting('themeName', name); }
  function setDensity(d: typeof densities[number]['value']) { updateSetting('density', d); }
  function setAmbilight(m: typeof ambilightModes[number]['value']) { updateSetting('ambilightMode', m); }
  function toggleEdgeGlow() { updateSetting('edgeGlow', !$settings.edgeGlow); }
  function toggleVoiceBoost() { updateSetting('voiceBoost', !$settings.voiceBoost); }
  function setLoudness(t: typeof loudnessOptions[number]['value']) { updateSetting('loudnessTarget', t); }
  function toggleHRTF() { updateSetting('hrtfEnabled', !$settings.hrtfEnabled); }
  function togglePreload() { updateSetting('enablePreload', !$settings.enablePreload); }
  function toggleChapters() { updateSetting('showChapterMarkers', !$settings.showChapterMarkers); }
  function toggleExtThumbs() { updateSetting('useExternalThumbnails', !$settings.useExternalThumbnails); }
  function toggleBatteryCap() { updateSetting('batteryAdaptiveCap', !$settings.batteryAdaptiveCap); }
  function toggleCmcdShare() { updateSetting('shareCmcd', !$settings.shareCmcd); }
  function setDrmRobustness(r: typeof drmRobustnessOptions[number]['value']) { updateSetting('drmRobustness', r); }
  function togglePersistentLicense() { updateSetting('persistentLicenses', !$settings.persistentLicenses); }

  // ─── Pack 3 polish ────────────────────────────────────────
  const subtitlePositions: Array<{ label: string; value: 'bottom' | 'middle' | 'top' }> = [
    { label: 'Низ',     value: 'bottom' },
    { label: 'Центр',   value: 'middle' },
    { label: 'Верх',    value: 'top' },
  ];
  // Curated accent presets — blue/cyan/magenta/orange/green plus auto-from-poster.
  const accentPresets: Array<{ label: string; value: string }> = [
    { label: 'Auto',    value: '' },
    { label: 'Cyan',    value: '#4fc3f7' },
    { label: 'Magenta', value: '#e040fb' },
    { label: 'Orange',  value: '#ffa726' },
    { label: 'Green',   value: '#69f0ae' },
    { label: 'Red',     value: '#ff5252' },
    { label: 'Purple',  value: '#7c4dff' },
    { label: 'Gold',    value: '#ffd54f' },
  ];

  let outputs = $state<AudioOutputDevice[]>([]);
  let outputPickerSupported = $state(false);

  function setSubPosition(p: 'bottom' | 'middle' | 'top') {
    updateSetting('subtitlePosition', p);
    applySubtitlePosition(p, $settings.subtitleOffset);
  }
  function onSubOffsetChange(e: Event) {
    const v = parseFloat((e.target as HTMLInputElement).value);
    updateSetting('subtitleOffset', v);
    applySubtitlePosition($settings.subtitlePosition, v);
  }
  function setAccent(color: string) {
    updateSetting('accentOverride', color);
    if (color) {
      accentColor.set(color);
      accentColorDark.set(darken(color, 0.4));
      accentColorLight.set(lighten(color, 0.3));
      document.querySelector('.lwp-root')?.setAttribute('style',
        `--lwp-accent:${color};--lwp-accent-dark:${darken(color, 0.4)};--lwp-accent-light:${lighten(color, 0.3)};`);
    }
    // (When color === '', the auto-extracted poster colors remain in effect.)
  }
  function darken(hex: string, factor: number): string {
    const { r, g, b } = parseHex(hex);
    return `rgb(${Math.round(r * (1 - factor))},${Math.round(g * (1 - factor))},${Math.round(b * (1 - factor))})`;
  }
  function lighten(hex: string, factor: number): string {
    const { r, g, b } = parseHex(hex);
    return `rgb(${Math.round(r + (255 - r) * factor)},${Math.round(g + (255 - g) * factor)},${Math.round(b + (255 - b) * factor)})`;
  }
  function parseHex(hex: string): { r: number; g: number; b: number } {
    const m = /^#?([\da-f]{2})([\da-f]{2})([\da-f]{2})$/i.exec(hex);
    if (!m) return { r: 79, g: 195, b: 247 };
    return { r: parseInt(m[1], 16), g: parseInt(m[2], 16), b: parseInt(m[3], 16) };
  }
  async function pickOutput(deviceId: string) {
    updateSetting('audioOutputDeviceId', deviceId);
    if ($engine?.video) {
      await assignOutputDevice($engine.video, deviceId);
    }
  }

  onMount(async () => {
    outputPickerSupported = hasOutputDevicePicker();
    if (outputPickerSupported) {
      outputs = await listAudioOutputs();
    }
  });
</script>

{#if visible}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="lwp-menu lwp-settings-menu" use:autoFocusMenu
    onwheel={(e) => e.stopPropagation()}>

    <!-- ════════════════════════════════════════════════
         APPEARANCE — themes / density / ambilight (Wave 1 B3)
         ════════════════════════════════════════════════ -->
    <div class="lwp-menu-section">Внешний вид</div>

    <div class="lwp-menu-title">Цвет акцента</div>
    <div class="lwp-chip-row">
      {#each accentPresets as p}
        <button
          class="lwp-chip lwp-chip-theme"
          class:active={$settings.accentOverride === p.value}
          onclick={() => setAccent(p.value)}
          title={p.value || 'Авто из постера'}
        >
          {#if p.value}
            <span class="lwp-color-dot" style="background: {p.value};"></span>
          {/if}
          {p.label}
        </button>
      {/each}
    </div>

    <div class="lwp-menu-title">Тема</div>
    <div class="lwp-chip-row">
      {#each themes as t}
        <button
          class="lwp-chip lwp-chip-theme"
          class:active={$settings.themeName === t.value}
          onclick={() => setTheme(t.value)}
        >
          <span class="lwp-color-dot" style="background: {t.swatch};"></span>
          {t.label}
        </button>
      {/each}
    </div>

    <div class="lwp-menu-title">Плотность интерфейса</div>
    <div class="lwp-chip-row">
      {#each densities as d}
        <button
          class="lwp-chip"
          class:active={$settings.density === d.value}
          onclick={() => setDensity(d.value)}
        >{d.label}</button>
      {/each}
    </div>

    <div class="lwp-menu-title">Ambilight</div>
    <div class="lwp-chip-row">
      {#each ambilightModes as m}
        <button
          class="lwp-chip"
          class:active={$settings.ambilightMode === m.value}
          onclick={() => setAmbilight(m.value)}
        >{m.label}</button>
      {/each}
      <button
        class="lwp-chip"
        class:active={$settings.edgeGlow}
        onclick={toggleEdgeGlow}
        title="Подсветка краёв в цвет акцента"
      >Edge glow</button>
    </div>

    <div class="lwp-menu-divider"></div>

    <!-- ════════════════════════════════════════════════
         PLAYBACK — preload, chapters, thumbnails
         ════════════════════════════════════════════════ -->
    <div class="lwp-menu-section">Воспроизведение</div>

    <div class="lwp-menu-title">Скорость</div>
    <div class="lwp-chip-row">
      {#each speeds as speed}
        <button
          class="lwp-chip"
          class:active={$playbackRate === speed}
          onclick={() => setSpeed(speed)}
        >{speed}x</button>
      {/each}
    </div>

    <div class="lwp-menu-title">Бесшовные эпизоды</div>
    <div class="lwp-chip-row">
      <button
        class="lwp-chip"
        class:active={$settings.enablePreload}
        onclick={togglePreload}
        title="Предзагрузка следующего эпизода для нулевой паузы"
      >{$settings.enablePreload ? 'Вкл' : 'Выкл'}</button>
    </div>

    <div class="lwp-menu-title">Метки глав на таймлайне</div>
    <div class="lwp-chip-row">
      <button
        class="lwp-chip"
        class:active={$settings.showChapterMarkers}
        onclick={toggleChapters}
      >{$settings.showChapterMarkers ? 'Вкл' : 'Выкл'}</button>
    </div>

    <div class="lwp-menu-title">Превью на таймлайне</div>
    <div class="lwp-chip-row">
      <button
        class="lwp-chip"
        class:active={$settings.useExternalThumbnails}
        onclick={toggleExtThumbs}
        title="Использовать sprite-превью из манифеста (если доступно)"
      >{$settings.useExternalThumbnails ? 'Sprite' : 'Canvas'}</button>
    </div>

    <div class="lwp-menu-title">Пропуск заставки</div>
    <div class="lwp-chip-row">
      <button
        class="lwp-chip"
        class:active={$settings.skipIntroEnabled}
        onclick={toggleSkipIntro}
      >{$settings.skipIntroEnabled ? 'Вкл' : 'Выкл'}</button>
      {#if $settings.skipIntroEnabled}
        {#each skipDurations as dur}
          <button
            class="lwp-chip"
            class:active={$settings.skipIntroDuration === dur}
            onclick={() => setSkipDuration(dur)}
          >{dur}с</button>
        {/each}
      {/if}
    </div>

    <div class="lwp-menu-title">Ускорение при удержании</div>
    <div class="lwp-chip-row">
      {#each speedBoostOptions as opt}
        <button
          class="lwp-chip"
          class:active={$settings.longPressSpeedBoost === opt.value}
          onclick={() => updateSetting('longPressSpeedBoost', opt.value)}
        >{opt.label}</button>
      {/each}
    </div>

    <div class="lwp-menu-divider"></div>

    <!-- ════════════════════════════════════════════════
         AUDIO — boost / compressor + Pro (Wave 1 B4)
         ════════════════════════════════════════════════ -->
    <div class="lwp-menu-section">Звук</div>

    <div class="lwp-menu-title">Усиление громкости</div>
    <div class="lwp-boost-row">
      <input type="range" min="1" max="3" step="0.1"
             value={$settings.audioBoost}
             oninput={onBoostChange} />
      <span class="lwp-boost-label">{Math.round($settings.audioBoost * 100)}%</span>
    </div>

    <div class="lwp-menu-title">Компрессия динамики</div>
    <div class="lwp-chip-row">
      {#each compressorPresets as cp}
        <button
          class="lwp-chip"
          class:active={$settings.audioCompressor === cp.value}
          onclick={() => setCompressor(cp.value)}
        >{cp.label}</button>
      {/each}
    </div>

    <div class="lwp-menu-title">Усиление голоса (диалоги)</div>
    <div class="lwp-chip-row">
      <button
        class="lwp-chip"
        class:active={$settings.voiceBoost}
        onclick={toggleVoiceBoost}
        title="Подъём 300–3400Hz + ducker фоновых звуков"
      >{$settings.voiceBoost ? 'Вкл' : 'Выкл'}</button>
    </div>

    <div class="lwp-menu-title">Нормализация громкости (LUFS)</div>
    <div class="lwp-chip-row">
      {#each loudnessOptions as opt}
        <button
          class="lwp-chip"
          class:active={$settings.loudnessTarget === opt.value}
          onclick={() => setLoudness(opt.value)}
        >{opt.label}</button>
      {/each}
    </div>

    <div class="lwp-menu-title">Объёмный звук в наушниках (HRTF)</div>
    <div class="lwp-chip-row">
      <button
        class="lwp-chip"
        class:active={$settings.hrtfEnabled}
        onclick={toggleHRTF}
        title="Виртуальный 5.1 через ConvolverNode"
      >{$settings.hrtfEnabled ? 'Вкл' : 'Выкл'}</button>
    </div>

    {#if outputPickerSupported && outputs.length > 0}
      <div class="lwp-menu-title">Устройство вывода (только Chromium)</div>
      <div class="lwp-chip-row">
        {#each outputs as dev}
          <button
            class="lwp-chip"
            class:active={$settings.audioOutputDeviceId === dev.deviceId
              || (!$settings.audioOutputDeviceId && dev.deviceId === 'default')}
            onclick={() => pickOutput(dev.deviceId)}
            title={dev.label}
          >{dev.label.length > 20 ? dev.label.substring(0, 18) + '…' : dev.label}</button>
        {/each}
      </div>
    {/if}

    <div class="lwp-menu-divider"></div>

    <!-- ════════════════════════════════════════════════
         SUBTITLES — size / color / bg / preset
         ════════════════════════════════════════════════ -->
    <div class="lwp-menu-section">Субтитры</div>

    <div class="lwp-menu-title">Стиль</div>
    <div class="lwp-chip-row">
      {#each subtitlePresets as preset}
        <button
          class="lwp-chip"
          class:active={$settings.subtitlePreset === preset.value}
          onclick={() => { updateSetting('subtitlePreset', preset.value); applySubPreset(preset.value); }}
        >{preset.label}</button>
      {/each}
    </div>

    <div class="lwp-menu-title">Положение</div>
    <div class="lwp-chip-row">
      {#each subtitlePositions as p}
        <button
          class="lwp-chip"
          class:active={$settings.subtitlePosition === p.value}
          onclick={() => setSubPosition(p.value)}
        >{p.label}</button>
      {/each}
    </div>

    <div class="lwp-menu-title">Смещение по вертикали</div>
    <div class="lwp-boost-row">
      <input type="range" min="-25" max="25" step="1"
             value={$settings.subtitleOffset}
             oninput={onSubOffsetChange} />
      <span class="lwp-boost-label">{$settings.subtitleOffset > 0 ? '+' : ''}{$settings.subtitleOffset}vh</span>
    </div>

    <div class="lwp-menu-title">Размер</div>
    <div class="lwp-chip-row">
      {#each fontSizes as fs}
        <button
          class="lwp-chip"
          class:active={$settings.subtitleFontSize === fs.value}
          onclick={() => setSubSize(fs.value)}
        >{fs.label}</button>
      {/each}
    </div>

    <div class="lwp-menu-title">Цвет</div>
    <div class="lwp-chip-row">
      {#each subColors as sc}
        <button
          class="lwp-chip"
          class:active={$settings.subtitleColor === sc.value}
          onclick={() => setSubColor(sc.value)}
        >
          <span class="lwp-color-dot" style="background: {sc.value};"></span>
          {sc.label}
        </button>
      {/each}
    </div>

    <div class="lwp-menu-title">Фон</div>
    <div class="lwp-chip-row">
      {#each bgOpacities as bg}
        <button
          class="lwp-chip"
          class:active={$settings.subtitleBgOpacity === bg.value}
          onclick={() => setSubBgOpacity(bg.value)}
        >{bg.label}</button>
      {/each}
    </div>

    <div class="lwp-menu-divider"></div>

    <!-- ════════════════════════════════════════════════
         NETWORK & DRM (Wave 2 C1 / C4)
         ════════════════════════════════════════════════ -->
    <div class="lwp-menu-section">Сеть и защита</div>

    <div class="lwp-menu-title">Адаптивный буфер при низкой батарее</div>
    <div class="lwp-chip-row">
      <button
        class="lwp-chip"
        class:active={$settings.batteryAdaptiveCap}
        onclick={toggleBatteryCap}
        title="Понизить качество до 720p при батарее <20% или Save-Data"
      >{$settings.batteryAdaptiveCap ? 'Вкл' : 'Выкл'}</button>
    </div>

    <div class="lwp-menu-title">CMCD телеметрия</div>
    <div class="lwp-chip-row">
      <button
        class="lwp-chip"
        class:active={$settings.shareCmcd}
        onclick={toggleCmcdShare}
        title="Анонимные данные для CDN-балансировки. Вы можете отключить."
      >{$settings.shareCmcd ? 'Делиться' : 'Только локально'}</button>
    </div>

    <div class="lwp-menu-title">DRM защита</div>
    <div class="lwp-chip-row">
      {#each drmRobustnessOptions as opt}
        <button
          class="lwp-chip"
          class:active={$settings.drmRobustness === opt.value}
          onclick={() => setDrmRobustness(opt.value)}
        >{opt.label}</button>
      {/each}
      <button
        class="lwp-chip"
        class:active={$settings.persistentLicenses}
        onclick={togglePersistentLicense}
        title="Хранить DRM лицензии между сессиями"
      >Persistent</button>
    </div>

    <div class="lwp-menu-divider"></div>

    <!-- ════════════════════════════════════════════════
         ACTIONS — modal-likes
         ════════════════════════════════════════════════ -->
    <div class="lwp-menu-item" tabindex="0" onclick={openWatchparty}>
      <svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor" style="flex-shrink: 0;">
        <path d="M16 11c1.66 0 2.99-1.34 2.99-3S17.66 5 16 5c-1.66 0-3 1.34-3 3s1.34 3 3 3zm-8 0c1.66 0 2.99-1.34 2.99-3S9.66 5 8 5C6.34 5 5 6.34 5 8s1.34 3 3 3zm0 2c-2.33 0-7 1.17-7 3.5V19h14v-2.5c0-2.33-4.67-3.5-7-3.5zm8 0c-.29 0-.62.02-.97.05 1.16.84 1.97 1.97 1.97 3.45V19h6v-2.5c0-2.33-4.67-3.5-7-3.5z"/>
      </svg>
      Совместный просмотр (W)
    </div>
    <div class="lwp-menu-item" tabindex="0" onclick={togglePiP}>
      <svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor" style="flex-shrink: 0;">
        <path d="M19 11h-8v6h8v-6zm4 8V4.98C23 3.88 22.1 3 21 3H3c-1.1 0-2 .88-2 1.98V19c0 1.1.9 2 2 2h18c1.1 0 2-.9 2-2zm-2 .02H3V4.97h18v14.05z"/>
      </svg>
      Картинка в картинке (P)
    </div>
    <div class="lwp-menu-item" tabindex="0" onclick={takeScreenshot}>
      <svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor" style="flex-shrink: 0;">
        <path d="M21 19V5c0-1.1-.9-2-2-2H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2zM8.5 13.5l2.5 3.01L14.5 12l4.5 6H5l3.5-4.5z"/>
      </svg>
      Скриншот (S)
    </div>
    <div class="lwp-menu-item" tabindex="0" onclick={toggleStats}>
      <svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor" style="flex-shrink: 0;">
        <path d="M19 3H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zM9 17H7v-7h2v7zm4 0h-2V7h2v10zm4 0h-2v-4h2v4z"/>
      </svg>
      Статистика потока (I)
    </div>
    <div class="lwp-menu-item" tabindex="0" onclick={toggleDrmInfo}>
      <svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor" style="flex-shrink: 0;">
        <path d="M12 1L3 5v6c0 5.55 3.84 10.74 9 12 5.16-1.26 9-6.45 9-12V5l-9-4zm0 10.99h7c-.53 4.12-3.28 7.79-7 8.94V12H5V6.3l7-3.11v8.8z"/>
      </svg>
      DRM диагностика (D)
    </div>
  </div>
{/if}
