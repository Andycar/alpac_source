<script lang="ts">
  /**
   * ShortcutsOverlay — quick-reference card shown on '?' / 'F1'.
   *
   * Single source of truth for keyboard shortcuts visible to the user.
   * If you add a new key handler in keyboard.ts, mirror it here so users
   * can discover it.
   */

  import { showShortcuts } from '../stores/ui';

  const groups: Array<{ title: string; rows: Array<[string, string]> }> = [
    {
      title: 'Воспроизведение',
      rows: [
        ['Space / K',    'Play / Pause'],
        ['← / →',         'Перемотка ±10с'],
        ['Shift + ← / →', 'Перемотка ±5с'],
        ['↑ / ↓',         'Громкость ±5%'],
        ['M',             'Mute'],
        [', / .',         'Кадр назад / вперёд (на паузе)'],
        ['[ / ]',         'Скорость ниже / выше'],
        ['F',             'Полный экран'],
        ['P',             'Picture-in-Picture'],
        ['Shift + P',     'Предыдущий эпизод'],
        ['N',             'Следующий эпизод'],
      ],
    },
    {
      title: 'Меню и панели',
      rows: [
        ['Q',  'Качество'],
        ['A',  'Озвучка'],
        ['C',  'Субтитры'],
        ['E',  'Эпизоды'],
        ['S',  'Скриншот'],
        ['I',  'Статистика потока'],
        ['D',  'DRM диагностика'],
        ['W',  'Совместный просмотр'],
        ['B',  'Добавить закладку'],
        ['Shift + B', 'Список закладок'],
        ['V',  'Голосовое управление (Chromium)'],
        ['?',  'Эта справка'],
        ['Esc', 'Закрыть / выйти'],
      ],
    },
    {
      title: 'TV пульт (color keys)',
      rows: [
        ['🔴 Red',    'Статистика'],
        ['🟢 Green',  'Ambilight'],
        ['🟡 Yellow', 'Озвучка'],
        ['🔵 Blue',   'Субтитры'],
      ],
    },
    {
      title: 'Жесты (touch)',
      rows: [
        ['Tap',                'Показать / скрыть контролы'],
        ['Double-tap слева',   'Перемотка −10с'],
        ['Double-tap справа',  'Перемотка +10с'],
        ['Drag вертикально слева',  'Яркость'],
        ['Drag вертикально справа', 'Громкость'],
        ['Long-press',         'Ускорение (настройка)'],
        ['Pinch (2 пальца)',   'Зум 1.0×–2.5×'],
      ],
    },
  ];

  function close() { showShortcuts.set(false); }
</script>

{#if $showShortcuts}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="lwp-shortcuts-backdrop" onclick={close}></div>
  <div class="lwp-shortcuts-overlay" role="dialog" aria-label="Keyboard shortcuts">
    <div class="lwp-shortcuts-header">
      <span>Горячие клавиши</span>
      <button class="lwp-btn lwp-btn-ghost" onclick={close} aria-label="Close">×</button>
    </div>
    <div class="lwp-shortcuts-grid">
      {#each groups as g}
        <div class="lwp-shortcuts-group">
          <div class="lwp-shortcuts-group-title">{g.title}</div>
          {#each g.rows as [key, desc]}
            <div class="lwp-shortcuts-row">
              <kbd>{key}</kbd>
              <span>{desc}</span>
            </div>
          {/each}
        </div>
      {/each}
    </div>
  </div>
{/if}
